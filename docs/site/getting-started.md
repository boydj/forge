# Getting started

## 1. Create your identity

In your Gemini client, create a client certificate for the whole site,
`gemini://git.as215520.net/`, with a long validity. In Lagrange: Identity
menu, New Identity. This certificate is your account: keep it, and enrol
a second device (below) rather than relying on one copy.

When the client asks you to trust the server, the fingerprints are on the
[status page](status.md).

## 2. Register

Open `/account` with the certificate selected and choose *Create a new
account with this certificate*. Type a username (lowercase letters,
digits, hyphens). Done.

## 3. Add an SSH key

Open `/account/keys` and paste your public key at *paste a single key*
(ed25519 recommended). The page also shows the server's host key so you
can verify it when ssh asks.

## 4. Create a repository and push

Open `/new`, type a name, then:

```
git clone git@git.as215520.net:you/name.git
cd name
# add files, commit
git push -u origin main
```

Your repository is at `/~you/name/`. A `README.md` or `README.gmi` renders
on that page.

## Writing to the forge

Anything you write (an issue, a comment, a review, release notes) is a
Titan upload: on the page that offers the action, use your client's
upload (Lagrange: Page menu, Upload), type the text with your identity
selected, send. The forge then shows you the result. A few actions
(closing an issue, deleting something) instead ask you to type a
confirming word at a prompt.

## A second device

On a device that has the account: `/account/certs`, *generate a code to
enrol another device*. On the new device, with its own certificate:
`/account`, *Add this certificate to an existing account*, and enter the
code (valid 15 minutes). Devices are listed and can be revoked at
`/account/certs`.
