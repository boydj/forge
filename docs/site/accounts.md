# Accounts and certificates

## Your certificate is your account

The forge identifies you by the public key of the client certificate your
Gemini client presents. There is no password, no e-mail and no recovery
form. Anything that changes state on the forge needs the certificate, and
the same certificate is used for Titan uploads.

Practical consequences:

- **Scope it to the whole site** (`gemini://git.as215520.net/`) when you
  create it, so your client offers it everywhere, including `titan://` URLs.
- **Choose a long validity.** An expired certificate is refused on every
  page, including `/account`, so you could not even enrol a replacement
  with it. Lagrange's default (year 9999) is fine.
- **Re-issuing with the same key keeps the account.** A new key is a new
  identity; enrol it as another device instead.
- **Back it up.** Losing the only certificate of an account means losing
  the account, unless another device is enrolled.

## Several devices

Each account may have several certificates, one per device, each labelled
and revocable on its own.

1. On a device that already has the account: `/account/certs`, *generate a
   code to enrol another device*. The code is 16 hex digits, valid for 15
   minutes, usable once.
2. On the new device, with its own fresh certificate selected: `/account`,
   *Add this certificate to an existing account with an enrolment code*,
   and type the code at the prompt.

Revoke a device at `/account/certs` (*revoke*); revocation is immediate on
every point of presence. You cannot revoke the certificate you are
currently using.

## SSH keys

`/account/keys` lists your keys with their fingerprints. Add one at *paste a
single key* (the prompt takes one `authorized_keys` line), or upload a whole
`authorized_keys` file with Titan to `/account/keys` (`text/plain`, one key
per line). Accepted: ed25519, sk-ed25519, ecdsa, and RSA of at least 3072
bits. Remove a key with its *remove this key* link.

The SSH login name is ignored; clone URLs use `git@`. Which key you offer
decides who you are.

## Profile

`/account/profile` sets a display name and a short bio, typed as
`display name | bio` (use `-` to clear). Your public page is `/~you/`.

## Registration rules

Usernames match `[a-z][a-z0-9-]{0,31}` and are permanent. The name is shown
in URLs (`/~you/`) and in activity feeds.
