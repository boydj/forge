# TLS and host-key identity

Two long-lived keys identify the service: the TLS certificate on 1965
(Gemini and Titan) and the ed25519 SSH host key on 22. Clients pin both
(TOFU), so they are the same on every POP and change only by deliberate,
announced rotation. Threat model: T-12, T-30, T-35, T-36; decisions: ADR
0005, 0007; research: `docs/research/protocols.md` sections 1.5, 1.6, 8.

## Service identity

`internal/tlsid.LoadOrCreate` loads `gemini.cert_file`/`gemini.key_file`
or, when the key file is missing, generates:

- ECDSA **P-256**, self-signed, serial random 127-bit;
- validity from one hour ago for **10 years** (`tlsid.Lifetime`);
- `CN` = `hostname`; SAN = `hostname` and `localhost` (IP addresses in the
  name list become IP SANs);
- key usage digital signature + key encipherment, extended key usage
  server auth; key file 0600, certificate 0644, written atomically.

Production nodes do not generate: `scripts/secrets gen-tls <host>` creates
the same shape of certificate (valid to 2036) once, stores it in the SOPS
bundle, and `scripts/deploy` places the identical `server.key`/`server.crt`
on every node under `/run/forge/secrets/tls/` (root:forge 0640). P-256
rather than Ed25519 because some client TLS stacks reject Ed25519 server
certificates (`docs/secrets.md`).

Server TLS settings (`gemini.TLSServerConfig`): TLS 1.2 minimum, Go's
default cipher suites, `ClientAuth = RequestClientCert` with **no**
verification of the client chain, `close_notify` after every response.
Session tickets are per node (anycast connections stay on one POP).

The fingerprint is logged at start (`tls identity ... sha256=<hex of DER>`)
and can be computed from the file with
`openssl x509 -in server.crt -noout -fingerprint -sha256`.

### What clients pin

- **Lagrange** (since 1.6) pins the SHA-256 of the public key plus expiry
  and port: a re-issued certificate with the same key is accepted silently;
  a new key is "untrusted" until the user accepts it.
- **Amfora** pins the SPKI and expiry; **gmni**, Bombadillo-era clients
  and `gg` pin the whole certificate, so even a same-key re-issue warns.
- An expired certificate is refused by all of them; TOFU stores treat an
  expired record as "unknown host", not "mismatch".

## Client identity

Identity is the **SHA-256 of the SubjectPublicKeyInfo** of the presented
certificate (`certificates.spki_sha256`), so a user who re-issues a
certificate with the same key keeps the account; the certificate hash
(`cert_sha256`) is recorded and updated when it changes. No CA, subject or
chain is examined.

| Situation | Response |
| --- | --- |
| no certificate on a page that needs one | `60` with an instruction to create/select an identity (issued on `/account`, a prefix of every authenticated path, so clients offer the same identity everywhere) |
| unknown certificate | pages that need identity: `61`; `/account`: registration page (INPUT username, or enrolment code) |
| known, valid | authenticated; `last_used_at` refreshed at most hourly |
| `NotAfter` in the past | `62 certificate expired` on every path |
| `NotBefore` more than 24 h in the future | `62 certificate expired` (clock skew tolerance) |
| revoked (`revoked_at` set) | `62 certificate revoked` on every path |
| account disabled | `61 account disabled` |

Rules for users:

- **Multiple certificates** per account, each labelled and individually
  revocable (`/account/certs`). The first registered account on a fresh
  forge is the administrator.
- **Enrolment**: on an existing device `/account/certs/enrol-code` (or an
  operator: `forge admin cert enrol-code USER`) produces a 16-hex-digit
  code valid **15 minutes**, single use, stored hashed in `tokens`. On the
  new device open `/account`, choose "add this certificate to an existing
  account", enter the code at the `11` (sensitive input) prompt.
- **Revocation** is immediate: `/account/certs/revoke/<spki>` (not the
  certificate currently in use), or `forge admin cert revoke SPKI`. The
  lookup happens on every request; nothing is cached.
- **Expiry** is the client's choice (Lagrange defaults to year 9999). An
  expired certificate gets `62` everywhere, including `/account`, so a
  user must enrol a replacement from another device or use an operator
  code before it expires. The account page and the front page warn a
  signed-in user whose certificate expires within 30 days.
- Do not reuse a TLS identity's key as an SSH key (ADR 0005).

## Rotating the service certificate

Only when the key is compromised, the algorithm must change, or expiry
approaches. Because Gemini has one certificate per listener there is **no
overlap period on the wire**: every client sees the switch. The procedure
minimises surprise rather than avoiding it.

1. Generate the new pair: `scripts/secrets gen-tls git.<zone>` into the
   SOPS bundle under new keys (`forge_tls_key_next`, `forge_tls_cert_next`)
   so the current one stays deployable.
2. Announce 30 days ahead: the new SHA-256 fingerprint (DER and SPKI forms)
   on the front page and an announcement page, in the project repository
   (readable via SSH, whose host key is an independent identity), and in a
   DNSSEC-signed `TXT` record. The threat model proposes a
   `/.well-known/forge/tls-rotation.gmi` page signed with the old key; it
   is **not implemented** yet, so publish a normal page.
3. Prefer a switch date at the old certificate's `NotAfter`: TOFU clients
   discard expired pins, so first contact after that date is a plain
   "new host" prompt instead of a "certificate changed" warning.
4. Switch **all POPs in one window**: move the `_next` values to
   `forge_tls_key`/`forge_tls_cert`, `scripts/deploy <pop>` on each
   (drain/undrain each POP around its restart). A mixed fleet would make
   users flap between two identities.
5. Keep the old key offline for 90 days in case a rollback is needed, then
   destroy it.

What users see: Lagrange shows the page as untrusted with a "trust this
certificate" action (with the new key; a same-key re-issue is silent);
Amfora asks to accept the changed certificate; gmni/gg refuse until the
stored fingerprint is removed. Monitoring clients (one per region, planned)
should alert on any fingerprint other than the announced one.

## Rotating the SSH host key

`sshd.LoadOrCreateHostKey` accepts only ed25519 and presents **one** host
key; the OpenSSH-style overlap with two `HostKey` entries (threat model
T-36) is recorded as technical debt in `docs/status.md`. Until then the
procedure is:

1. `scripts/secrets gen-hostkey git.<zone>` prints the key and its SSHFP
   line. Publish **both** old and new `SSHFP` records (`4 1 <sha1>`,
   `4 2 <sha256>`, as `forge serve` logs and `sshd.SSHFPRecords` returns) in
   the DNSSEC-signed zone at least a TTL ahead, and the new
   `SHA256:` fingerprint on the front page / account keys page.
2. Switch all POPs in one window via the SOPS bundle and `scripts/deploy`.
3. Remove the old SSHFP record after the switch.

Clients with `VerifyHostKeyDNS yes` and a validating resolver accept the
new key without prompting. Others see OpenSSH's "REMOTE HOST IDENTIFICATION
HAS CHANGED" refusal and must run `ssh-keygen -R git.<zone>` and re-verify
against the published fingerprint; say so in the announcement.

## Key compromise

Any node holds both keys, so a compromised node or provider snapshot means
both are compromised (T-35, T-37).

1. Withdraw the suspect POP: `scripts/deploy drain <pop>` (BGP withdraw);
   if the node itself is untrusted, withdraw from the provider portal.
2. Rotate TLS and host keys immediately, skipping the announcement lead
   time: generate, deploy to the trusted POPs, publish fingerprints and an
   incident note everywhere clients can read them (front page, repository,
   DNS TXT/SSHFP).
3. Remove the node's age recipient from every bundle (`.sops.yaml`,
   `scripts/secrets updatekeys`), rotate the cluster secret and its
   WireGuard key (`scripts/secrets rotate` prints the runbooks), rebuild
   the node from scratch.
4. Audit: events and pushes stamped with that node's name, certificates
   and keys added during the exposure window.
5. Client identities are not affected (the forge stores only public-key
   hashes), but any enrolment code issued during the window should be
   treated as used: `forge admin maintenance` purges expired ones; revoke
   certificates enrolled during the window if in doubt.
