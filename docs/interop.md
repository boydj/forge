# Interoperability with third-party clients

`scripts/interop` exercises a forge with real Gemini/Titan clients and feed
parsers (not our own test client) and prints a pass/fail table. It starts a
scratch instance on high ports by itself, or tests a running forge when given
a URL. Every check whose client is missing is `SKIP`ped, so the script is safe
in CI with any subset of the tools installed; it exits 1 only on `FAIL`.

```
scripts/interop                        # scratch forge on 19965/12222 (INTEROP_PORT, INTEROP_SSH_PORT)
scripts/interop gemini://host:1965     # a running forge; INTEROP_REPO (alice/proj) must exist and be public
INTEROP_KEEP=1 INTEROP_DIR=/tmp/fi scripts/interop   # keep certificates, feeds, serve.log for inspection
```

Keep `INTEROP_DIR` short: the hook socket lives under the data directory and
Unix socket paths are limited to about 108 bytes (see deviation 4).

## Tools obtained (no root required)

| Tool | Version | How |
| --- | --- | --- |
| `gg`, `titan` (gmid) | `gg 2.1.1-current`, commit `06255c9` (2026-08-01) | `git clone https://github.com/omar-polo/gmid`. No `tls.h`/`event2` headers on the host, so: build libevent 2.1.12-stable from its release tarball with `./configure --prefix=$HOME/.local/libevent --disable-openssl`, then in gmid `CFLAGS=-I$HOME/.local/libevent/include LDFLAGS="-L$HOME/.local/libevent/lib -Wl,-rpath,$HOME/.local/libevent/lib" ./configure` (gmid bundles a libtls shim over OpenSSL 3, `compat/libtls`), then `make gg titan` (only those two targets; the server itself is not needed). Installed to `~/.local/bin`. |
| `gemget` | v1.9.0 | `GOBIN=~/.local/bin go install github.com/makeworld-the-better-one/gemget@latest` (the `makew0rld` alias fails with a module-path mismatch). Supports `--cert/--key`, follows `31`, refuses URLs over 1024 bytes locally. |
| `ignition-gemini` | 1.0.0 (Python) | `pip install --user ignition-gemini`. Its lazy import of `cryptography.hazmat.primitives.serialization` is broken with current `cryptography`; the script imports that module first. TOFU state is redirected with `ignition.set_default_hosts_file()` into the work dir (by default it writes `.known_hosts` into the current directory). |
| `feedparser` | 6.0.14 | `pip install --user feedparser`; used on the Atom twins (bozo flag). |
| `openssl` | 3.0.13 | system; TLS version, `close_notify`, SAN, SNI probes. |
| `python3` | 3.11 | raw TLS probes for request lines no client will send, and the Titan early-rejection timing checks. These are ours, not third party. |
| gmni | - | not attempted (needs BearSSL). |
| Amfora, Lagrange | - | interactive only; see the manual checklist below. |

`gg -N` and `gemget -i` disable certificate checks (self-signed scratch
certificate); `titan(1)` never verifies the server (`titan.c:335`).

## Results

Self-hosted run on 2026-09-10 (`pass=78 fail=2 skip=0 info=8`; the URL-mode
run against a separately started instance gives the same two failures). The
two `FAIL` rows are deviations 1 and 2 below and stay red until fixed.

```
STAT | CHECK                                          | CLIENT    | DETAIL
-----+------------------------------------------------+-----------+----------------------------------------
PASS | front page 20 text/gemini                      | gg        | 20 text/gemini; charset=utf-8; lang=en
PASS | front page body is gemtext                     | gg        | # forge
PASS | repo page 20                                   | gg        | 20 text/gemini; charset=utf-8; lang=en
PASS | repo page lists clone URL                      | gg        | git clone ssh://git@localhost:12232/alice/proj.git
PASS | tree page 20                                   | gg        | 20 text/gemini; charset=utf-8; lang=en
PASS | raw blob MIME image/png                        | gg        | 20 image/png
PASS | raw blob MIME text/markdown                    | gg        | 20 text/markdown; charset=utf-8
PASS | 31 for missing trailing slash                  | gg        | 31 /~alice/proj/
PASS | 51 for unknown path                            | gg        | 51 not found
PASS | 60 on /account without cert                    | gg        | 60 Create or select a client certificate to identify yourself
PASS | empty path gemini://host accepted              | gg        | 20 text/gemini; charset=utf-8; lang=en
INFO | 1100-byte URL                                  | gg        | client refuses locally ("iri too long"); server checked raw below
INFO | 31 redirect                                    | gg        | gg does not follow redirects (by design); gemget/ignition checked
PASS | front page 20 text/gemini                      | gemget    | Header: 20 text/gemini; charset=utf-8; lang=en
PASS | 31 redirect followed to 20                     | gemget    | Header: 31 /~alice/proj/ Header: 20 text/gemini; charset=utf-8; lang=en 
PASS | raw blob bytes intact (png magic)              | gemget    | 89504e47
PASS | 51 for unknown path (non-zero exit)            | gemget    | Header: 51 not found rc=1
PASS | 60 on /account without cert (hint --cert)      | gemget    | Header: 60 Create or select a client certificate to identify yourself Error: gemin
INFO | 1100-byte URL                                  | gemget    | client refuses locally ("url is too long"); server checked raw below
PASS | front page 20 (SuccessResponse)                | ignition  | 20 text/gemini; charset=utf-8; lang=en SuccessResponse
PASS | 31 -> RedirectResponse                         | ignition  | 31 /~alice/proj/ RedirectResponse
PASS | 51 -> PermFailureResponse                      | ignition  | 51 not found PermFailureResponse
PASS | 60 -> ClientCertRequiredResponse               | ignition  | 60 Create or select a client certificate to identify yourself ClientCertRequiredRe
PASS | 1100-byte URL -> 59                            | ignition  | 59 malformed request PermFailureResponse
PASS | 1024-byte URL accepted (51, not 59)            | raw       | TLSv1.3|51 not found|body=0|eof
PASS | 1025-byte URL -> 59                            | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | 1100-byte URL -> 59                            | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | request with LF only (no CR) -> 59             | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | bare CR inside request line -> 59              | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | URL with userinfo -> 59                        | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | URL with fragment -> 59                        | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | empty request line -> 59                       | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | relative URL -> 59                             | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | http:// scheme -> 59                           | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | dot-dot path -> 59                             | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | empty path gemini://host -> 20                 | raw       | TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=344|eof
PASS | bytes after CRLF are ignored                   | raw       | TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=344|eof
INFO | raw space in URL                               | raw       | 51 not found (strict servers answer 59)
INFO | URL host not ours (example.org)                | raw       | 20 text/gemini; charset=utf-8; lang=en (no 53; any host is served)
PASS | TLS 1.3 negotiated                             | raw       | TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=344|eof
PASS | clean EOF after response (close_notify)        | raw       | TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=344|eof
PASS | openssl: TLSv1.3, close_notify from server     | openssl   |     Protocol  : TLSv1.3 <<< TLS 1.3, Alert [length 0002], warning close_notify 
PASS | openssl: session ends with 'closed'            | openssl   | closed
PASS | certificate SAN contains hostname              | openssl   | X509v3SubjectAlternativeName:DNS:localhost
PASS | no SNI still served (20)                       | openssl   | 20 text/gemini; charset=utf-8; lang=en
INFO | other SNI (other.example)                      | openssl   | 20 text/gemini; charset=utf-8; lang=en (server ignores SNI)
INFO | TLS 1.2 offered                                | openssl   |  Protocol : TLSv1.2 (minimum per docs/tls.md)
PASS | IPv6 literal host [::1]                        | gg        | 20 text/gemini; charset=utf-8; lang=en
PASS | IPv6 literal host [::1]                        | gemget    | Header: 20 text/gemini; charset=utf-8; lang=en
PASS | unregistered cert: /account -> 20 register page | raw       | TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=298|eof
FAIL | /account? -> 10 INPUT for username             | raw       | got: TLSv1.3|20 text/gemini; charset=utf-8; lang=en|body=298|eof (want /\|10 /)
PASS | unregistered cert: /account offers registration | gemget    | Header: 20 text/gemini; charset=utf-8; lang=en This certificate (28f3ef8c0c) is n
PASS | /account?<name> -> 30 /account, then 20 Account page | gemget    | Header: 30 /account Header: 20 text/gemini; charset=utf-8; lang=en # Account
PASS | /new -> 10 INPUT (gemget stops, asks user)     | gemget    | Header: 10 Repository name (lowercase letters, digits, . _ -) Error: This URL need
PASS | /new?<name> -> 30 to new repo                  | gemget    | Header: 30 /~bob40194/interop-901154/ Header: 20 text/gemini; charset=utf-8; lang=
PASS | registered cert sees account page              | gg        | # Account: bob40194
PASS | second user registers (erin)                   | raw       | TLSv1.3|30 /account|body=0|eof
PASS | Titan upload new issue -> 30 (exit 0)          | titan     | /~alice/proj/issues/1 rc=0
PASS | uploaded issue renders with title              | gg        | # #1 Interop issue 901154
PASS | Titan comment from stdin -> 30                 | titan     | /~alice/proj/issues/1 rc=0
PASS | comment appears on issue page                  | gg        | A comment via titan.
PASS | Titan edit upload -> 30                        | titan     | /~alice/proj/issues/1 rc=0
PASS | edited title rendered                          | gg        | # #1 Interop issue 901154 (edited)
PASS | Titan without cert -> 60 (titan exit 2)        | titan     | titan: server error: a client certificate identifies you; register at /account rc=
PASS | Titan unregistered cert -> 61                  | titan     | titan: server error: this certificate is not registered; visit /account to registe
INFO | Titan ;edit via gmid titan                     | titan     | titan(1) always appends ;size=N -> server answers 59 (client has no ;edit support)
PASS | Titan no cert, size=50MB, 5-byte body -> immediate 60 | raw       | 0.00s 60 a client certificate identifies you; register at /account 
PASS | Titan unregistered cert -> immediate 61        | raw       | 0.00s 61 this certificate is not registered; visit /account to register 
PASS | Titan size over global cap -> immediate 59     | raw       | 0.00s 59 upload too large 
PASS | Titan size over endpoint limit -> immediate 50 | raw       | 0.00s 50 body too large (limit 262144 bytes) 
PASS | Titan without size -> 59                       | raw       | 0.00s 59 malformed request 
PASS | Titan duplicate size param -> 59               | raw       | 0.00s 59 malformed request 
PASS | Titan disallowed MIME -> 50                    | raw       | 0.00s 50 text/plain or text/gemini expected 
PASS | Titan request line LF only -> 59               | raw       | TLSv1.3|59 malformed request|body=0|eof
PASS | Titan raw upload -> 30 /issues/N               | raw       | 0.00s 30 /~alice/proj/issues/2 
PASS | Titan ;edit -> 20 text/plain with title+body   | raw       | 0.00s 20 text/plain; charset=utf-8 body=b'Raw titan issue\n\n\n'
PASS | Titan ;edit without cert -> 60                 | raw       | 0.00s 60 a client certificate identifies you; register at /account 
PASS | Titan ;edit with size -> 59                    | raw       | 0.00s 59 malformed request 
PASS | Titan edit upload by non-author -> 61          | raw       | 0.00s 61 this certificate is not registered; visit /account to register 
FAIL | Titan ;edit by other registered user -> 61 (docs/titan.md) | raw       | got: 0.00s 20 text/plain; charset=utf-8 body=b'Raw titan issue\n\n\n' 
PASS | Titan edit upload by other registered user -> 61 | raw       | 0.00s 61 not permitted 
PASS | gemfeed /feed: title + dated entries (subscription spec) | gemfeed   | title='# forge activity' entries=10 baddated=0 rc=0
PASS | gemfeed /feed: entries use '- ' after date     | gemfeed   | 0
PASS | gemfeed /~alice/proj/feed valid                | gemfeed   | title='# alice/proj activity' entries=5 baddated=0 rc=0
PASS | gemfeed served as text/gemini                  | gg        | 20 text/gemini; charset=utf-8; lang=en
PASS | Atom /atom.xml: well-formed, feedparser bozo=0 | atom      | wellformed entries=10 id=gemini://localhost:19975/ feedparser: bozo=0 version=atom
PASS | Atom /~alice/proj/atom.xml valid               | atom      | wellformed entries=5 id=gemini://localhost:19975/~alice/proj/ feedparser: bozo=0 v
PASS | Atom served as application/atom+xml            | gg        | 20 application/atom+xml; charset=utf-8

target: gemini://localhost:19975   pass=78 fail=2 skip=0 info=8
tools: gg titan gemget openssl python3 feedparser ignition
```

Client-side behaviour worth knowing:

- `gg` does not follow redirects and refuses a URL over 1024 bytes before
  sending it; `gemget` follows `31` (up to 5) and also refuses long URLs;
  `ignition` returns a `RedirectResponse` and leaves following to the caller.
- INPUT (`10`): neither `gg` nor `gemget` resends with a query. `gemget`
  exits 1 with "This URL needs input, you should make the request again
  manually"; appending `?value` by hand works for `/new?name` and
  `/account?name`.
- `titan(1)` always appends `;size=N`, so it cannot issue `;edit` (server
  answers `59`, correctly). Lagrange is the only client tested that does.
- Early Titan rejections (`60`/`61`/`59`/`50` before the body) are answered
  in 0.00 s with the body unread. Because the server then closes with the
  body still arriving, Linux sends RST; Python's ssl still returns the status
  line, but `titan(1)` exits `bad fd` (`titan.c:121`, `POLLERR` handled
  before `POLLIN`) when its body write races the close. The script retries
  and counts these as an `INFO` row. A server-side mitigation is possible
  (deviation 5).

## Deviations from the specification or from our documentation

1. **Registration link is a no-op** (`FAIL`). `docs/titan.md` and the
   register page itself offer `=> /account? Create a new account with this
   certificate`, which should answer `10` asking for a username. It answers
   `20` with the same register page. `internal/web/account.go:53` reads
   `req.Query()`, which is `""` for both `/account` and `/account?`
   (`url.Parse` records a bare trailing `?` as `URL.ForceQuery`, not in
   `RawQuery`), so the `q == "?"` branch at `account.go:62` is dead.
   Real clients (Lagrange, gemget, ignition) drop or keep the `?` exactly as
   typed; either way the user loops. Registration only works by typing
   `/account?name` by hand. Fix: at `account.go:54` use
   `if q == "" && !req.URL.ForceQuery {` (the embedded `*gemini.Request`
   exposes `URL`), or move the prompt to a distinct path such as
   `/account/new` and link to that. Add a test that `/account?` with an
   unregistered certificate returns `10`.

2. **`;edit` ignores authorisation** (`FAIL`). `docs/titan.md` says
   `/issues/<n>/edit;edit` and `/comments/<id>/edit;edit` are for the "issue
   author or writer"; `internal/web/issues.go:259-262` and `:286-289` return
   the editable text to any registered reader. The upload half is checked
   (`61 not permitted` from `EditIssue`/`EditComment`). Nothing leaks on a
   public repository, but the doc and the handler disagree. Fix: guard both
   `if req.Titan.Edit` branches with
   `if !(is.AuthorID == u.ID || rc.acc.CanWrite()) { gemini.Error(req.w, 61, "not permitted"); return }`
   (comment: `c.AuthorID`), or change the doc to say the read is open to
   readers.

3. **Any host, port or SNI is served** (`INFO`). `gemini://example.org/`,
   `gemini://localhost:1965/` (wrong port) and any SNI name all get `20`.
   The Gemini spec asks servers to answer `53 proxy request refused` for
   hosts they do not serve; `StatusProxyRequestRefused` exists in
   `internal/gemini/status.go:24` but nothing uses it, and
   `docs/protocol-architecture.md` records no decision. Low risk (no
   proxying happens), but it means a misdirected request is silently served
   under the wrong absolute URLs in feeds. Proposed fix: in the web handler
   entry (`internal/web/handler.go`, where `request` is built), compare
   `req.URL.Hostname()` case-insensitively with `Config.Hostname` (plus
   node names / IP literals used by the health checker) and answer `53`
   otherwise; keep it behind a config knob if replicas are addressed by IP.

4. **Startup fails opaquely on long data paths** (found while setting up).
   With `data_dir` deeper than ~95 bytes `forge serve` dies with
   `forge: hook socket: listen unix .../hook.sock: bind: invalid argument`
   (`cmd/forge/ssh.go:64`, `internal/hooks/hooks.go:176`): `sun_path` is
   108 bytes. `admin init` accepts the path without complaint. Fix: in
   `internal/config.(*Config).Validate` (or `hooks.Listen`) reject
   `len(HookSocket()) >= 104` with a message naming the limit, and/or add a
   `[ssh] hook_socket` override so the socket can live in `/run`.

5. **RST after early Titan rejection** (`INFO`, see above).
   `internal/gemini/server.go:281` does `CloseWrite()` and then the deferred
   `Close()` while unread body bytes may still be in flight, which turns the
   FIN into an RST on Linux. Clients that write before they read (gmid
   `titan`, and probably other simple uploaders) can lose the status line.
   Proposed mitigation: for Titan requests rejected before the body, read
   and discard up to `min(size, 64 KiB)` (or until a short deadline) before
   closing, or `SetLinger`-style delay; the spec-required behaviour
   (status line + `close_notify`) is already correct.

6. **Raw space in the request line is accepted** (`INFO`).
   `gemini://host/a b` yields `51` rather than `59`;
   `internal/gemini/request.go:84-89` rejects controls and `\r\n` but lets
   `url.Parse` accept the space. Strict servers and the spec's "URL" wording
   imply `59`. One-line fix: add `' '` to the control check at
   `request.go:88`.

7. **Feed nits** (no status change). The forge-wide `/feed` and `/~user/feed`
   include `user.cert.add` and `user.key.add` events whose links are
   `/account` and `/account/keys` (`internal/forge/identity.go:140`,
   `internal/forge/sshkeys.go:105`); for any other reader those links answer
   `60`, and because the gemfeed entry id is the link, all "certificate
   added" events collapse into one entry per feed. Consider linking them to
   `/~user/` or excluding both kinds from feeds in
   `internal/web/feeds.go:21,35`. `feedparser` reports `bozo=0`,
   `version=atom10` for every Atom twin; gemfeed pages satisfy the
   subscription companion spec (`# title`, `=> path YYYY-MM-DD - subject`).

Behaviour confirmed correct: `20`/`31`/`51`/`59`/`60`/`61` as documented;
1024-byte URL accepted and 1025 refused; LF-only, bare CR, userinfo,
fragment, `..`, relative and `http://` request lines all `59`; bytes after
the CRLF ignored; empty path treated as `/`; TLS 1.3 negotiated, TLS 1.2
still accepted (documented minimum in `docs/tls.md`); `close_notify` sent
(`openssl s_client` shows `closed`); SAN carries the hostname; IPv6
listener works with a `[::1]` literal; Titan limits (`size` cap, endpoint
limit, MIME, duplicate parameter, missing `size`, `;edit` with `size`) are
enforced before the body is read; raw blob MIME types are right and bytes
are intact through `gemget`.

## Manual checklist: Lagrange

Lagrange cannot run headless, so these are done by hand (Lagrange 1.18 or
newer for "Edit page with Titan"). Use `INTEROP_KEEP=1 scripts/interop` and
point Lagrange at `gemini://localhost:19965/` while it runs, or at a real
deployment.

1. Open the front page; confirm it renders, the feed link and the
   repository list are present, and the TOFU prompt names the expected
   certificate fingerprint (compare with `serve.log`, `tls identity`).
2. Follow `alice/proj` -> files -> `README.md` and `logo.png`: the raw image
   opens inline (`image/png`), the Markdown blob opens as text.
3. Type `gemini://localhost:19965/~alice/proj` without the slash: Lagrange
   follows the `31` to `/~alice/proj/` and the address bar shows the slash.
4. Open `/account`: expect the `60` page ("Create or select a client
   certificate"). Identity -> New identity; choose **use for the entire
   domain** (not "this page only"), so it is also sent for `titan://` URLs.
5. Reload `/account`: expect the register page. Click "Create a new account
   with this certificate". **Expected**: an input prompt for a username.
   **Currently**: the same page again (deviation 1); type
   `gemini://localhost:19965/account?yourname` by hand to continue, then
   confirm `/account` shows "Account: yourname".
6. `/new`: an input prompt appears; enter a name; the new repository page
   opens (`30`).
7. On `/~alice/proj/issues/new` use the page menu -> "Upload" (Titan). Type
   a title line, a blank line and a body; upload as `text/plain`. Expect
   `30` to `/~alice/proj/issues/N`, followed automatically, showing the
   issue. Repeat with a `.gmi` file chosen from disk.
8. On the issue page, "Edit page with Titan" on the
   `.../issues/N/edit` URL: Lagrange fetches `;edit` and shows
   `title\n\nbody` in the editor; change the title, upload; the page shows
   the new title. Do the same for one of your comments
   (`.../comments/<id>/edit`).
9. Switch to a different identity (or none) and repeat 7: expect the `61`
   ("not registered") or `60` error to be shown as an error page, not a hang
   (deviation 5 does not affect Lagrange in testing so far, but note it if
   the dialog reports "connection reset").
10. Close an issue: follow the close link, type `close` at the prompt;
    reopen with `reopen`. Delete a comment with `delete`. Confirm a plain
    click without typing changes nothing.
11. Feeds: open `/feed`, bookmark it and tick "Subscribe to feed"; entries
    appear under Feeds with dates and the subject text (the ` - ` separator
    must not be visible). Do the same for `/~alice/proj/feed` and for
    `/~alice/proj/atom.xml` (Lagrange also accepts Atom). Trigger a new
    event (open an issue) and use Feeds -> Refresh: the new entry appears.
12. Identities -> your identity -> "Use on this page" is unnecessary; verify
    that after restarting Lagrange the identity is still offered on
    `titan://` URLs (domain-wide scope was set in step 4).

Amfora: same reads; it does not implement gemfeed subscriptions, so subscribe
to `atom.xml` only; no Titan support.
