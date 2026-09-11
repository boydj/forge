# Getting started

Five steps from nothing to a pushed repository. Paths are on
`gemini://git.as215520.net/` unless they say otherwise.

## 1. Browse

Open `gemini://git.as215520.net/` in your client. The first time, the
client asks you to trust the server's certificate; compare it with the
fingerprints in [Service status](status.md) before accepting. Reading
needs no account.

## 2. Create your identity

Your account is a client certificate. Create one in your client for the
whole site, not for a single page, so it is offered for every path and for
Titan uploads too. In Lagrange: Identity menu, New Identity, scope
`gemini://git.as215520.net/`, any name, a long validity (an expired
certificate locks you out, see [Accounts and certificates](accounts.md)).

## 3. Register

Open `/account` with the certificate selected and choose *Create a new
account with this certificate*. Type a username: lowercase letters, digits
and hyphens, up to 32 characters, starting with a letter. That is all; the
certificate is now your login on this device.

To use the same account from another device, generate an enrolment code on
the first one (`/account/certs`, *generate a code to enrol another device*)
and enter it on the second (`/account`, *Add this certificate to an existing
account with an enrolment code*). Codes last 15 minutes and work once.

## 4. Add an SSH key

Pushes and clones of private repositories authenticate with an SSH key
tied to your account. Open `/account/keys` and paste one public key at
*paste a single key* (ed25519 preferred; also sk-ed25519, ecdsa, and RSA of
3072 bits or more). Do not reuse the key of your client certificate.

Register the server's host key while you are there: the page shows it,
and [Git over SSH](git.md) has the fingerprint and the DNS record to verify it against.

## 5. Create a repository and push

Open `/new` and type a name (lowercase letters, digits, `. _ -`). Then:

```
git clone git@git.as215520.net:you/name.git
cd name
# add files, commit
git push -u origin main
```

Or push an existing repository:

```
git remote add origin git@git.as215520.net:you/name.git
git push -u origin main
```

Your repository is at `/~you/name/`. A `README.md` or `README.gmi` at the
root renders on that page. The rest of this guide covers
[repositories](repositories.md), [issues](issues.md),
[changes](changes.md), [releases](releases.md) and [feeds](feeds.md).
