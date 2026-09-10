# 0005. Human identity: TLS client certificates; Git identity: SSH keys

Date: 2026-09-10
Status: accepted

## Context

Gemini clients support self-signed client certificates natively. The forge must
avoid passwords, and must support multiple devices, loss and revocation.

## Decision

- An account is a row in the database. It has zero or more **certificates**
  (SHA-256 fingerprint of the DER certificate, label, created, expires,
  revoked) and zero or more **SSH keys** (OpenSSH authorized_keys line, label,
  created, revoked).
- Any registered, non-revoked certificate authenticates as the account for
  Gemini and Titan. Any registered, non-revoked SSH key authenticates as the
  account for Git.
- Registration: a request to `/account` with an unknown certificate offers a
  username INPUT (status 10) and creates the account bound to that certificate.
  Additional certificates are added by an already-authenticated session
  generating a one-time enrolment code (INPUT flow) and presenting it from the
  new device; SSH keys are added by Titan-uploading `authorized_keys` text to
  `/account/keys`.
- Recovery: any remaining valid credential (certificate or SSH key via a
  signed challenge through `forge admin`) can enrol a new certificate; an
  operator can enrol one via `forge admin user recover`.
- The same private key is never used for both a TLS identity and an SSH key.
- Status codes: 60 when identity is required, 61 when not authorised, 62 when
  the certificate is expired or revoked.

## Consequences

- No passwords, no email, no OAuth.
- Certificate expiry is the client's choice; the forge records `NotAfter` and
  refuses expired certificates (62), which pushes users to enrol replacements.
- Revocation is immediate: fingerprint lookup happens on every request.
