# Titan

Titan is the write half of the human interface: the same TLS listener and
port as Gemini (1965), the same client certificate as identity, one upload
per connection. Research and the choices it informed are in
`docs/research/protocols.md` section 3; the code is
`internal/gemini/request.go` (parsing), `internal/gemini/server.go`
(limits) and `internal/web/account.go`, `issues.go` (endpoints).

## Protocol as implemented

Request line, then the body:

```
titan://host/path;size=N[;mime=TYPE][;token=T]\r\n<exactly N bytes>
titan://host/path;edit\r\n                                (no body)
```

| Rule | Behaviour |
| --- | --- |
| Parameters | parsed only from the last path segment, after the first `;`; any order; `size`, `mime`, `token`, `edit` only; a duplicate or unknown key is `59 malformed request`; a `;` inside the unescaped path is `59` |
| `size` | required unless `;edit`; digits only, at most 15; `edit` together with `size` is `59` |
| `mime` | default `text/gemini`; at most 255 bytes |
| `token` | parsed (at most 255 bytes) and currently ignored; see "Tokens" |
| Request line | at most 2048 bytes (Gemini's is 1024); userinfo, fragment, `..` segments and `//` are `59` |
| Global size cap | `size > limits.max_titan_bytes` (default 64 MiB) is `59 upload too large` before any body is read |
| Body timeout | 120 s to deliver the body (server constant, not configurable) |
| Identity | no certificate: `60`; unregistered certificate: `61`; revoked/expired: `62`; disabled account: `61`. All decided before the body is read |
| Text bodies | `readTitanText`: per-endpoint limit (`50 body too large (limit N bytes)`), MIME must be `text/plain`, `text/gemini` or `text/markdown` (parameters after `;` ignored) else `50 text/plain or text/gemini expected`; fewer bytes than `size`: `59 short body`; invalid UTF-8 or NUL: `59 body must be UTF-8 text`; CRLF normalised to LF |
| Result | `30 <gemini path>` of the affected resource; `;edit` answers `20 text/plain` with the editable text |
| Close | the server writes the status line, then `close_notify`, for early rejections as well |

`size=0` is not treated as a delete anywhere. Non-text uploads (release
assets, any MIME type) are bounded by `limits.max_asset_bytes`.

## Endpoints

"Who" is evaluated with the repository's access rules: *reader* is anyone
who can see the repository (everyone for public ones), *writer* is the owner,
a collaborator with `write`/`admin`, or a forge admin.

| Path | Method | Who | Body / prompt | Limit | Result |
| --- | --- | --- | --- | --- | --- |
| `/account/keys` | Titan | registered user | `authorized_keys` text, one key per line (ed25519, sk-ed25519, ecdsa, rsa >= 3072) | 64 KiB | `30 /account/keys` |
| `/~o/r/issues/new` | Titan | reader; repo not archived | first non-empty line is the title (leading `#` stripped, <= 200 chars), rest is the body | `max_text_bytes` (256 KiB) | `30 /~o/r/issues/<n>` |
| `/~o/r/issues/<n>/comment` | Titan | reader; not archived | comment text | `max_text_bytes` | `30 /~o/r/issues/<n>` |
| `/~o/r/issues/<n>/edit` | Titan; `;edit` | issue author or writer | title line + body; `;edit` returns `title\n\nbody` | `max_text_bytes` | `30 /~o/r/issues/<n>` |
| `/~o/r/issues/<n>/comments/<id>/edit` | Titan; `;edit` | comment author or writer | body; `;edit` returns the body | `max_text_bytes` | `30 /~o/r/issues/<n>` |
| `/~o/r/issues/<n>/comments/<id>/delete` | Titan (body ignored) or Gemini INPUT | comment author or writer | Gemini: type `delete` | - | `30 /~o/r/issues/<n>` |
| `/~o/r/issues/<n>/close`, `/reopen` | Gemini INPUT | issue author or writer | type `close` / `reopen`, optionally followed by a comment | - | `30 /~o/r/issues/<n>` |
| `/new` | Gemini INPUT | registered user | repository name | quota | `30 /~u/<name>/` |
| `/account?` | Gemini INPUT | unregistered certificate | username | - | `30 /account` |
| `/account/enrol` | Gemini INPUT (`11`) | unregistered certificate | enrolment code | - | `30 /account` |
| `/account/profile` | Gemini INPUT | registered user | `display name \| bio` | 64 / 512 chars | `30 /account` |
| `/account/keys/add` | Gemini INPUT | registered user | one public key | - | `30 /account/keys` |
| `/~o/r/changes/*`, `/~o/r/releases/*`, `/~o/r/settings/*` | Titan | - | **planned (M3)**; currently `51` | - | - |

Writes are only accepted on the repository's leader node; elsewhere the
domain layer returns `ErrNotLeader` -> `40 writes are not accepted on this
node right now` (single-node deployments are always the leader).

## The consent rule

A Gemini request is a link click or a typed URL, and clients prefetch and
retry freely, so **no Gemini request changes state unless the user typed a
confirmation into an INPUT prompt** (close/reopen with the literal word,
`delete` for a comment, a name for `/new` and `/account?`). Everything
else that writes goes through Titan, where the client has to build a body
deliberately. This is threat-model invariant T-19.

`/account/keys/remove/<fingerprint>` and `/account/certs/revoke/<spki>`
follow the rule too: both ask the user to type `remove` or `revoke` at an
INPUT prompt behind an action token, and the certificate in use cannot be
revoked from its own session.

## `;edit`

`titan://host/<resource>;edit` with no body returns the editable
representation as `20 text/plain; charset=utf-8`; the client (Lagrange
1.18+, "Edit page with Titan") shows it in the editor and uploads the
result to the same URL without `;edit`. Supported on issue `edit` and
comment `edit` paths; elsewhere the request is routed like an upload and
usually answers `51`.

## Error statuses

| Status | When |
| --- | --- |
| `30` | success; META is the gemini path to show |
| `40` | node is not the leader; identity lookup failed; internal error |
| `41` | free disk below `limits.min_free_bytes` (`insufficient storage`) |
| `50` | body too large for the endpoint, MIME not allowed, quota exceeded, already exists, archived repository, title too long |
| `51` | unknown path, or a repository/issue the identity may not see |
| `59` | malformed request line, request line too long, `size` over the global cap, short body, non-UTF-8 body |
| `60` | no client certificate |
| `61` | certificate not registered, account disabled, or action not permitted (`not permitted`) |
| `62` | certificate expired or revoked |

## Tokens: deviation from the threat model

`docs/threat-model.md` (T-19, T-21) proposed that every Titan write carry a
per-user, per-purpose, 10-minute `token=` issued on the Gemini page that
offers the action. The implementation authorises on the **client certificate
only**; `token=` is accepted syntactically and ignored.

Rationale:

- The threat the token addressed is a client auto-submitting a write. Titan
  uploads are explicit client actions: the specification requires a client
  following a `titan://` link to discard link parameters and ask the user
  for body, size, MIME and token. There is no link-click write path.
- Clients drop parameters from links, so a page-issued token would have to
  be typed by hand into every upload dialog, which is the main cost of
  Titan UX for no security gain over the certificate.
- Identity and revocation are already per request (SPKI lookup on every
  connection); a stolen certificate would also steal any token flow.

The `tokens` table and `Store.CreateToken/ConsumeToken` exist and are used
for certificate enrolment codes, so per-action tokens can be added for
destructive operations (delete repository, transfer ownership) if abuse
appears. Recorded in `docs/status.md` as technical debt; revisit at M3
when repository settings gain destructive endpoints.

## Client notes

- **Lagrange**: create the identity for the whole site so it is offered for
  `titan://` URLs (the identity is chosen by matching the equivalent
  `gemini://` URL prefix). Page menu -> "Upload" opens the Titan dialog on
  the current URL; typed text is sent as `text/plain`; files use their
  detected type. The `30` after an upload is followed and the resource is
  shown. "Edit page with Titan" performs `;edit` first.
- **gmid's `titan(1)`**: `titan -C client.crt -K client.key -m text/plain titan://host/~o/r/issues/new body.txt`
  (reads stdin without a file; exit 0 on `2x`/`3x`; no TOFU checks).
- **openssl**: see `docs/development.md` for tested one-liners
  (`-quiet -nocommands`).
- **Elpher** with `gemini-write`, Alex Schroeder's Perl `titan`, and the
  Rust `trot` CLI also work in principle (not tested here). Amfora, gmni,
  Kristall, Bombadillo have no Titan support.

### Action tokens (query-driven writes)

A Gemini INPUT answer arrives as the URL query, and a link can pre-fill a
query. Typed confirmation alone therefore proves nothing. Every
query-driven state change (`/new`, `/account/register`, `/account/enrol`,
key removal, certificate revocation, issue close/reopen, comment and
release deletion, repository settings) is served only under
`<path>/_/<token>`, where the token is an HMAC of the acting identity
(account id, or certificate key before registration), the path and the
UTC day, keyed with a per-node or cluster secret. A request without the
token is redirected to the tokenised path with the query dropped, which
forces the client to show the INPUT prompt; a request with a wrong token is
redirected to the plain path. Tokens are valid for two days and never
appear in page content, so a third party cannot construct one.

The token is a **suffix** of the resource's own path, not a `/_/<token>/`
prefix at the root as in v0.1.1 and earlier. Clients scope an identity to a
URL prefix, so a token path outside that prefix was requested without the
certificate the token is bound to: it could never verify, the server
redirected back to the plain path, and that re-issued a token. Certificate
enrolment from an identity scoped to `/account` looped until it hit the
client's redirect limit. Keeping the token under the resource keeps the
certificate attached to it.
