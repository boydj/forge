# Titan uploads

Titan is how you write to the forge: issues, comments, reviews, release
notes and assets, your SSH keys. It is the upload companion of Gemini,
on the same host and port (`titan://git.as215520.net/...`), authenticated
by the same client certificate. One upload per request; the forge answers
with a redirect to the page it changed.

## Rules

- **Certificate required**, and it must be registered
  ([Accounts and certificates](accounts.md)). Without one: status 60;
  unknown: 61; expired or revoked: 62.
- **Text uploads** (everything except release assets) must be
  `text/plain`, `text/gemini` or `text/markdown`, UTF-8, at most 256 KiB;
  the default type is `text/gemini`.
- **Release assets** take any type, at most 64 MiB.
- **Result**: `30` with the page to open on success; `50` with a reason
  when the body is too large, the type is wrong, a quota is hit or the
  repository is archived; `51` for a path you cannot see; `40` if the node
  cannot take writes at that moment (retry in a few seconds).
- A Gemini link never changes anything by itself. Actions that are not
  uploads (closing an issue, removing a collaborator, deleting) ask you to
  type a confirming word at a prompt.

## Lagrange

Create your identity for the whole site so it is offered for `titan://`
URLs. On any page that offers an action link (*open a new issue*,
*comment*, *review*, *merge*, ...): Page menu, Upload. Type or paste the
text, check that your identity is shown, send. The forge's `30` is
followed automatically. *Edit page with Titan* first fetches the current
text (Titan `;edit`) so you edit rather than retype; it works for issues,
comments and change descriptions.

## From a terminal

With gmid's `titan(1)`:

```
titan -C cert.pem -K key.pem -m text/plain \
  titan://git.as215520.net/~alice/proj/issues/new body.txt
```

`-C`/`-K` are your certificate and key in PEM; the body is read from a
file or stdin. Exit status 0 means the forge answered `2x` or `3x`. Any
Titan client that sends a client certificate works the same way.
