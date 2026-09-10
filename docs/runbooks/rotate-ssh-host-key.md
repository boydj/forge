# Rotate the SSH host key

**Purpose**: replace the ed25519 host key presented on port 22 by every
POP, and the `SSHFP` records that let `VerifyHostKeyDNS` clients accept it
without a prompt.

**Preconditions**: DNSSEC-signed zone (Cloudflare, `manage_dnssec` or DS
published by hand); all POPs deployable in one window; announcement
channel. The server presents **one** host key (no OpenSSH-style overlap of
two `HostKey`s, `docs/status.md` technical debt), so every client without
DNS verification sees "REMOTE HOST IDENTIFICATION HAS CHANGED" once.

**Duration**: DNS TTL lead (publish the new SSHFP at least one TTL ahead)
+ ~10 min per POP.

**Triggers**: node compromise (do it together with
`rotate-tls-certificate.md`; the same node held both), key material lost
from the bundle, policy.

## 1. Generate and stage

```sh
scripts/secrets gen-hostkey git.<zone>
#   prints: ssh_host_key YAML, the .pub line, and "git.<zone> IN SSHFP 4 2 <sha256hex>"
scripts/secrets edit infra/secrets/dev.enc.yaml     # paste as ssh_host_key_next; keep ssh_host_key as is
git commit -am "secrets: staged next SSH host key"
```

Fingerprint to announce: `ssh-keygen -lf` of the `.pub` line (`SHA256:...`).

## 2. Publish both SSHFP records ahead of time

```sh
cd infra/opentofu/environments/dev
$EDITOR dev.auto.tfvars
#   sshfp = [
#     { algorithm = 4, type = 2, fingerprint = "<OLD sha256 hex>" },
#     { algorithm = 4, type = 2, fingerprint = "<NEW sha256 hex>" },
#   ]
eval "$(../../../../scripts/secrets env ../../../secrets/dev.enc.yaml)"
tofu plan && tofu apply
unset VULTR_API_KEY CLOUDFLARE_API_TOKEN; cd -
dig +dnssec SSHFP git.<zone> | grep -E 'SSHFP|flags'          # both records, "ad" flag from a validating resolver
```

Announce the new `SHA256:` fingerprint on the front page / account keys
page and in the project repository, with the instruction for users
without DNS verification: `ssh-keygen -R git.<zone>` and compare against
the published fingerprint on next connect.

## 3. Switch all POPs in one window

```sh
scripts/secrets edit infra/secrets/dev.enc.yaml
#   ssh_host_key_next -> ssh_host_key; old one -> ssh_host_key_prev
git commit -am "secrets: rotate SSH host key"
make build
for pop in ewr1 <others>; do
  ssh -p 2200 deploy@$pop.nodes.<zone> 'sudo -u forge forge admin pop drain; sudo bgp-announce withdraw' 2>/dev/null
  scripts/deploy $pop --binary bin/forge
  ssh -p 2200 deploy@$pop.nodes.<zone> 'journalctl -u forge -n 20 --no-pager | grep "ssh host key"'   # fingerprint=SHA256:<new>, sshfp=...
  ssh -p 2200 deploy@$pop.nodes.<zone> 'sudo bgp-announce announce; sudo -u forge forge admin pop undrain'
done
```

## 4. Remove the old SSHFP record

After the switch (and one TTL), drop the old entry from `sshfp` in
`dev.auto.tfvars`, `tofu apply`.

## Verify

```sh
for pop in ewr1 <others>; do ssh-keyscan -p 22 -t ed25519 $pop.nodes.<zone> 2>/dev/null | ssh-keygen -lf -; done   # identical, = announced
ssh -o VerifyHostKeyDNS=yes -o UserKnownHostsFile=/dev/null -T git@git.<zone>      # no prompt with a validating resolver; forge's "no shell" reply
```

## Rollback

`ssh_host_key_prev` back to `ssh_host_key`, redeploy every POP in one
window, keep both SSHFP records until you are done. Users who already
updated `known_hosts` get a second warning.

## Notes

- Only ed25519 is accepted by `sshd.LoadOrCreateHostKey`; do not try to
  stage an RSA or ECDSA key.
- Dual host-key overlap (T-36): set `ssh.previous_host_key_file` in `forge.toml` to the old key for the overlap period; both keys are offered. Without it the warning for
  non-DNS clients is unavoidable.
