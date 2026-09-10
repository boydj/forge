# Rotate the service TLS certificate

**Purpose**: replace the Gemini/Titan certificate on 1965 on every POP.
Clients pin it (TOFU), so this is a **user-visible, announced event**,
not a calendar chore: the current certificate is valid to 2036-01-01 and
rotates only on compromise, algorithm change, or approaching expiry.

**Preconditions**: all POPs deployable in one sitting; an announcement
channel (front page, project repository, DNS TXT); for a planned rotation,
30 days of lead time. For a compromise skip the lead time (`docs/tls.md`
"Key compromise").

**Duration**: planned: 30 days lead + one deploy window of ~10 min per POP.
Emergency: 1 h.

**Triggers**: key compromise / node compromise (`rebuild-pop.md`), expiry
within 90 days, algorithm deprecation. A user report of "certificate
changed" that you did **not** plan is an incident (possible MITM or a
node serving a generated cert): `incident-checklist.md`.

## What clients will see

- Same key, new certificate: Lagrange and Amfora accept silently
  (they pin the public key); gmni/gg/Bombadillo-style clients warn once.
- New key: every client warns ("untrusted"/"changed"); Lagrange offers
  "trust", gmni/gg refuse until the stored entry is removed.
- Expired old certificate: TOFU stores treat it as unknown, so switching
  **at** the old `NotAfter` turns "changed" into a plain first-contact
  prompt. Prefer that date for planned rotations.
- There is **no overlap on the wire**: one certificate per listener. A
  fleet with mixed certificates looks like a MITM to anycast users, so all
  POPs switch in one window.

## 1. Generate (laptop)

```sh
scripts/secrets gen-tls git.<zone>            # prints forge_tls_key / forge_tls_cert YAML, SHA-256 fingerprint, notAfter
scripts/secrets edit infra/secrets/dev.enc.yaml
#   paste as forge_tls_key_next / forge_tls_cert_next so the current pair stays deployable
git commit -am "secrets: staged next TLS identity"
```

Record both fingerprint forms for the announcement:

```sh
scripts/secrets get infra/secrets/dev.enc.yaml forge_tls_cert_next > next.crt
openssl x509 -in next.crt -noout -fingerprint -sha256                       # certificate (DER) fingerprint
openssl x509 -in next.crt -noout -pubkey | openssl pkey -pubin -outform DER | openssl dgst -sha256   # SPKI (what Lagrange/Amfora pin)
rm next.crt
```

## 2. Announce (planned rotation: 30 days ahead)

- Front page and an announcement page on the forge (`docs/tls.md` step 2).
  The signed `/.well-known/forge/tls-rotation.gmi` page from the threat
  is `forge admin announce "TEXT"` (shown as a Notice on the front page; `--clear` removes it).
- A note in the project repository (readable over SSH, whose host key is an
  independent identity).
- A DNSSEC-signed `TXT` record with the new fingerprint. The
  `cloudflare-dns` module manages A/AAAA/SSHFP only; add the TXT by hand in
  Cloudflare or extend the module. **PLANNED**.
- Switch date: the old certificate's `NotAfter` if that is acceptable.

## 3. Switch all POPs in one window

```sh
scripts/secrets edit infra/secrets/dev.enc.yaml
#   move forge_tls_key_next -> forge_tls_key, forge_tls_cert_next -> forge_tls_cert; keep the old pair as forge_tls_key_prev/_cert_prev
git commit -am "secrets: rotate TLS identity"
for pop in ewr1 <others>; do
  ssh -p 2200 deploy@$pop.nodes.<zone> 'sudo -u forge forge admin pop drain; sudo bgp-announce withdraw' 2>/dev/null
  scripts/deploy $pop --binary bin/forge       # pushes secrets, restarts; build once beforehand with `make build`
  ssh -p 2200 deploy@$pop.nodes.<zone> 'journalctl -u forge -n 20 --no-pager | grep "tls identity"'   # sha256= must be the new one
  ssh -p 2200 deploy@$pop.nodes.<zone> 'sudo bgp-announce announce; sudo -u forge forge admin pop undrain'
done
```

Do them back to back; minutes apart is fine, hours is not.

## Verify

```sh
for pop in ewr1 <others>; do
  echo | openssl s_client -connect $pop.nodes.<zone>:1965 -servername git.<zone> 2>/dev/null | openssl x509 -noout -fingerprint -sha256
done                                                                        # identical, equals the announced fingerprint
echo | openssl s_client -connect git.<zone>:1965 -servername git.<zone> 2>/dev/null | openssl x509 -noout -fingerprint -sha256 -enddate
```

Update the front page with the fingerprint, keep the announcement page
up for 90 days.

## Rollback

Within 90 days the old key is still in the bundle (`_prev`): move it back
to `forge_tls_key`/`forge_tls_cert` and redeploy every POP in one window.
Users who already accepted the new certificate get a second prompt. After
90 days delete the `_prev` values and destroy offline copies.

## Compromise variant

Withdraw the suspect POP first (`drain-pop.md`), then steps 1 and 3
immediately, then the announcements (step 2) with an incident note, then
`rebuild-pop.md` for the suspect node and `rotate-ssh-host-key.md`
(the same node held both keys).
