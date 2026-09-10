# Protocol research: Gemini, Titan, Gemfeeds, Misfin, clients, TLS, testing

Research date: 2026-09-10. Sources were fetched live; Gemini-only sources (`gemini://`) were
retrieved with `openssl s_client` and are cited by their `gemini://` URL. Where a claim comes from
reading an implementation's source rather than a spec, the file is named.

Contents

1. Gemini protocol (spec v0.24.1)
2. Gemtext (spec v0.24.1)
3. Titan
4. Gemfeeds (the "subscription" companion spec)
5. Misfin
6. Client landscape and interoperability quirks
7. Existing Gemini forge / git-browsing projects
8. Server-side TLS practice
9. Testing tools for CI
10. Implications for the forge

---

## 1. Gemini protocol

Canonical source: https://geminiprotocol.net/docs/protocol-specification.gmi (document version
0.24.1, CC0). The old single "speculative specification" was split into a protocol spec and a
separate gemtext spec (https://geminiprotocol.net/docs/gemtext-specification.gmi). The protocol
spec explicitly defers TLS certificate policy to a best-practices document, and the best-practices
page (https://geminiprotocol.net/docs/best-practices.gmi) currently says nothing about server
certificates; the TLS tutorial (https://geminiprotocol.net/docs/tls-tutorial.gmi) has its TOFU
section marked "Coming soon". Section 8 below fills that gap from other sources.

### 1.1 Request

```
request = absolute-URI CRLF
```

Normative rules (all quoted or closely paraphrased from the spec):

- "the URI MUST NOT exceed 1024 bytes, and a server MUST reject requests where the URI exceeds
  this limit."
- "A server MUST reject a request with a userinfo portion."
- "Clients MUST NOT send a fragment as part of the request, and a server MUST reject such
  requests as well."
- "a empty path component and a path component of "/" are equivalent and servers MUST support
  both without sending a redirection" (clients SHOULD add the trailing `/`, servers MUST cope
  without it).
- Default port 1965. "the use of an IP address in the authority section SHOULD NOT be used."
- Rejecting a malformed request is status 59.

### 1.2 Response header

```
reply    = input / success / redirect / tempfail / permfail / auth
input    = "1" DIGIT SP prompt        CRLF
success  = "2" DIGIT SP mimetype      CRLF body
redirect = "3" DIGIT SP URI-reference CRLF
tempfail = "4" DIGIT [SP errormsg]    CRLF
permfail = "5" DIGIT [SP errormsg]    CRLF
auth     = "6" DIGIT [SP errormsg]    CRLF
prompt   = 1*(SP / VCHAR)
mimetype = type "/" subtype *(";" parameter)
errormsg = 1*(SP / VCHAR)
```

- Header "MUST be UTF-8 encoded text and MUST NOT begin with the Byte Order Mark U+FEFF".
- META is mandatory for 1x/2x/3x and optional for 4x/5x/6x. In 0.24.1 there is **no explicit
  META length limit** in the BNF (the pre-split v0.16 spec said 1024 bytes). Keep META at or
  under 1024 bytes for compatibility with older parsers.
- "Servers MUST NOT send status codes that are not defined." Clients "MUST reject any status code
  less than '10' and greater than '69'" and SHOULD treat undefined codes in range by first digit.
- Body: "raw content, text or binary... There is no support for compression, chunking or any
  other kind of content or transfer encoding. The server closes the connection after the final
  byte, there is no 'end of response' signal."
- Text bodies "SHOULD NOT begin with an encoding of the Byte Order Mark"; clients SHOULD ignore
  one if present in text/gemini.
- Line endings: text media may use CRLF or bare LF (not bare CR), consistently; clients MUST
  accept both.

### 1.3 Status codes

| Code | Name | META | Notes |
|---|---|---|---|
| 10 | Input | prompt text (required) | Client MUST prompt; resend same URI with input as query, spaces as `%20`, newlines `%0A` (servers SHOULD accept `%0A` and `%0D%0A`). If the URI already had a query, the client MUST replace it. |
| 11 | Sensitive input | prompt | As 10 but input not echoed (passwords). |
| 20 | Success | MIME type with optional params | Only defined 2x code. |
| 30 | Temporary redirect | absolute or relative URI-reference | Client keeps using the original URI in future. Empty META is not valid. |
| 31 | Permanent redirect | URI-reference | Client SHOULD use the new location from then on. |
| 40 | Temporary failure | optional message | Unspecified; retry may succeed. |
| 41 | Server unavailable | optional | Overload / maintenance (cf. HTTP 503). |
| 42 | CGI error | optional | Dynamic content process died or timed out. |
| 43 | Proxy error | optional | Proxy could not complete a transaction with the remote host. |
| 44 | Slow down | optional | Client "SHOULD use an exponential back off, where subsequent delays between requests are doubled until this status no longer returned." (0.24.1 does **not** define META as a number of seconds.) |
| 50 | Permanent failure | optional | General. |
| 51 | Not found | optional | "It may exist in the future, it may not." |
| 52 | Gone | optional | Aggregators "should stop requesting the resource". |
| 53 | Proxy request refused | optional | Domain not served here and no proxying. |
| 59 | Bad request | optional | Malformed request or violated request constraints. |
| 60 | Client certificate required | optional message | See 1.6. |
| 61 | Certificate not authorised | optional | "The problem is not with the certificate itself, which may be authorised for other resources." |
| 62 | Certificate not valid | optional | Dates outside validity, invalid signature, or X.509 violation, "with no consideration of the particular requested resource". |

Redirect rules: clients "MUST limit the number of redirections they follow to 5"; when redirecting
a request that had a query string "the client MUST NOT apply the query string to the new location";
servers SHOULD NOT include fragments in redirects. Best practices: "Refrain from using redirects
frivolously"; cross-protocol redirects are "very heavily discouraged".

### 1.4 MIME, charset, lang

- Clients "MUST support MIME types of text/gemini with a character set of UTF-8, and text/plain,
  with a character set of either US-ASCII... or UTF-8."
- "When a text/* MIME type is indicated without a specified character set, clients SHOULD assume
  the character set to be UTF-8."
- "Client MUST deal with MIME parameters that are not understood by simply ignoring them."
- Gemtext spec: `charset` inherited from RFC 2046, default UTF-8; `lang` is the only text/gemini
  specific parameter, value is a comma-separated list of BCP 47 tags, and "if multiple tags (and
  hence a comma) are used, the whole value MUST be enclosed in quotation marks", e.g.
  `text/gemini; lang="en,fr"`. "Parameters other than "charset" and "lang" are undefined and
  clients MUST ignore any such paramters."
- Best practices: "For new content, please, please, please just use UTF-8"; servers are strongly
  encouraged to map `.gmi`/`.gemini` to text/gemini and serve `index.gmi` for directories.

### 1.5 TLS

- "Servers and clients MUST support TLS 1.2 or higher." The spec warns that under TLS 1.2 "the
  server name and the client certificate (if used) [are sent] in the clear"; a client "SHOULD
  warn the user when a client certifiate will be transmitted via TLS 1.2".
- SNI: "Client and server implementations MUST support TLS SNI... and clients MUST include
  hostname information when making requests for URLs where the authority section is a hostname."
  For IP-address authorities clients "SHOULD omit SNI information rather than setting it to an
  empty value."
- close_notify: "Gemini servers MUST use the TLS close_notify implementation to close the
  connection. Clients SHOULD NOT close a connection by default, but MAY in case the content
  exceeds constraints set by the user. Both clients and servers SHOULD handle the case when the
  TLS close_notify mechanism is not used... A client SHOULD notify the user of such a case."
  (gmid's `gg` client warns when a server omits close_notify, so this is observable in CI.)
- Server cert validation: "Clients are strongly RECOMMENDED to use a Trust on First Use or
  'TOFU' certificate-pinning system, which does not reject self-signed certificates as invalid".
  The stored record is "fingerprint and expiry date... associated with the server's hostname and
  port"; a mismatch "but the previous certificate's expiry date has not passed, this is
  considered potential evidence of a Man-In-The-Middle attack." The spec does not say what the
  fingerprint is computed over (whole certificate vs public key); clients differ, see section 8.

### 1.6 Client certificates (status 60/61/62)

Quoting the spec for status 60:

> "The content requires a client certificate. The client MUST provide a certificate in order to
> access the content and SHOULD NOT repeat the request without one. The scope of a certificate
> generated in response to this status code should be limited to the host and port from which the
> status code was received and the path of the URL in the original request plus all paths below
> it. A server MAY require a different certificate for a different path on the same host and
> port. A server SHOULD allow the same certificate to be used for any content along the given
> path."

> "Clients MUST NOT automatically generate a client certificate and use it to repeat the request
> without the active involvement of the user. Clients MUST NOT use a client certificate generated
> in response to this status code for a request to a different host, a different port, or a path
> on the same host and port which is above the path of the URL in the original request unless
> directed to do so by the user."

For all 6x: "servers SHOULD include such information [why the certificate was required or
rejected], and clients SHOULD display it to the user."

Consequences: identity in Gemini is the client certificate itself (self-signed, no CA); the only
stable identifier is a fingerprint of the certificate or of its public key. A 60 is a UI event
in every client (a human chooses or creates an identity); there is no silent retry.

---

## 2. Gemtext

Source: https://geminiprotocol.net/docs/gemtext-specification.gmi (v0.24.1). "Each line belongs
to exactly one type, and it is possible to unambiguously determine this type by inspecting the
first three characters of the line." Documents are parsed in a single pass with one state bit
(normal vs preformatted).

| Type | Syntax | Rules |
|---|---|---|
| Text | anything else | Wrapped by client per line; consecutive lines are not merged. |
| Link | `=>[<ws>]<URL>[<ws><label>]` | `ws` is one or more spaces/tabs; URL required and percent-encoded per RFC 3986; label optional; relative URLs resolved against the document URL. |
| Preformat toggle | ` ``` ` as the first three characters, "no leading whitespace" | Toggle lines are not rendered. Text after the opening ` ``` ` "MAY be interpreted by the client as 'alt text'" (recommended for ASCII art and for source code, to name the language); text after the closing toggle "MUST be ignored". Inside preformat mode, whitespace must be preserved verbatim so code can be copy-pasted. |
| Heading | `#`, `##`, `###` + optional whitespace + text | Semantic structure (TOC, feed titles); the first `#` heading is the document title for feed/index tools. |
| List item | `* ` (asterisk, space) + text | Single level only. |
| Quote | `>` + text | Quoted material. |

Line endings: CRLF or LF per the protocol spec.

---

## 3. Titan

### 3.1 The specification

Titan is Alex Schroeder's write protocol for Gemini, documented on the Transjovian wiki
(reachable over Gemini only; the HTTPS host returns 404):

- Spec: gemini://transjovian.org/titan/The%20Titan%20Specification
- Authentication & Authorisation: gemini://transjovian.org/titan/Authentication%20&%20Authorisation
- History (2020 mailing-list mail, Sean Conner's original proposal): gemini://transjovian.org/titan/Titan%20history
- Edit-link proposal: gemini://transjovian.org/titan/Edit%20Link
- gmid's `titan(1)` man page cites the spec as gemini://transjovian.org/titan/page/The%20Titan%20Specification, but that `/page/` path returned 51 in this pass; the wiki now serves it without `/page/`.

Request line, quoted from the spec:

```
titan://transjovian.org/test/raw/testing;size=123;mime=plain/text;token=hello\r\n
```

"That is, the target URL, followed by one, two, or three parameters:
* a mandatory filesize in bytes so that the server knows when you're done
* an optional MIME type to identify what kind of data you are sending (the default is text/gemini)
* an optional token to authenticate if the server allows anonymous uploads (on a wiki, for example)

After the Titan URL and the carriage return and linefeed the client sends the data and the server
replies with a regular Gemini status response."

Parameter rules:

- "Parameters are not query parameters! There is no question mark after the URL. Parameters are
  separated from the rest of the URL and each other using a semicolon and they come as key/value
  pairs." They are RFC 3986 path-segment parameters, and "the parameters need to go between the
  path and the query parameters since they are specified only for the last path segment", e.g.
  `titan://example.org/raw/Test;token=hello;mime=plain/text;size=10?username=Alex`.
- Exactly three parameter names are defined: `token`, `mime`, `size`. "No other parameters are
  used."
- **No ordering is mandated.** The spec's own examples use both `size;mime;token` and
  `token;mime;size`. Lagrange emits `;mime=...;size=...[;token=...]`
  (skyjake/lagrange `src/gmrequest.c`, `composeTitanRequest_GmRequest_`), gmid's `titan` appends
  size then mime/token, Phoebe accepts any order.
- "An interactive client following a Titan link or ending up at a Titan link following a
  redirect must ignore all existing parameters, query the user and make the Titan request using
  the one, two or three parameters provided by the user." So a link like
  `=> titan://host/x;mime=text/gemini` cannot pre-set parameters for Lagrange.
- MIME default: "If no MIME type is provided, the server should assume text/gemini." A server
  "may reject your upload if it restricts the MIME types it accepts, and it may ignore or
  translate MIME types it gets."
- Size: "The server should reject the upload if it exceeds the intended size."

Transaction and responses (quoted):

- Validation before reading the body: "the token is invalid, the size exceeds the internal limit,
  the filename doesn't match the constraints, the MIME type is not allowed, a client certificate
  is required, and so on. In this case, the server responds with an error line and closes the
  socket, for example: '50 A token is required to upload a file\r\n'." "Client should check for
  a server response when the connection is closed."
- After the body: "the server completes the transaction with a regular Gemini response before
  closing the connection. It could simply report success: '20 text/gemini; charset=UTF-8\r\n
  Upload succeeded.\n', or it could redirect to the updated resource:
  '30 titan://transjovian.org/test/testing\r\n', or it could report an error... Any Gemini
  response is possible."
- Redirects: "The Titan request must be made to that last Gemini URL in that redirect chain...
  Making a Titan request to a URL that would have resulted in a redirect using Gemini (URL A) is
  a mistake and ought to be reported as an error."
- Creation: attempting a Titan write to a resource that does not yet exist is allowed; the prior
  Gemini read may have returned 20 with empty content or 51.
- Deletion: "When the client wants to delete a resource, it uses the Titan protocol to send zero
  bytes of content (size=0)."
- Authorisation vs authentication: "The Titan protocol only does authorization: you bear a token
  (the password)... The alternative to using the token is to identify users. A server that would
  like to identify its users... would use client certificates to establish identity and act
  accordingly. That is, authentication is already part of Gemini and as such applies just as much
  to Titan."
- TLS: identical to Gemini; Lagrange's help states "When it comes to TLS, Titan is equivalent to
  Gemini, so the same server and client certificates can be used with both." Same port 1965 is
  conventional (GmCapsule: "Both [Gemini and Titan] are accepted via the same TCP port").

Edit-link proposal (implemented by Lagrange 1.18+ and kulak/gemini): `titan://host/path;edit\r\n`
with no body returns the raw, editable representation of the resource; the client then uploads to
the same URL without `;edit`. Servers may lock on `;edit` and must use a timeout; a locked page
answers 40.

### 3.2 How real implementations behave

**Lagrange** (client; `src/gmrequest.c`, `src/gmcerts.c`, `res/about/version.gmi`, help.gmi):

- Builds `<url>;mime=<m>;size=<n>[;token=<urlencoded token>]` and sends the payload after CRLF.
  Text typed in the dialog is sent as `text/plain` ("It will be sent to the server using
  'text/plain' as the media type"); dropped files use a detected or user-set type.
- Identity selection: `identityForUrl_GmCerts` does a case-insensitive string-prefix match of the
  request URL (default port stripped) against each identity's "use URLs"; "Fallback: Titan URLs
  use the Gemini identities, if not otherwise specified" by rewriting `titan://` to `gemini://`
  and retrying the lookup. Changelog: "Default Titan upload identity was sometimes chosen
  incorrectly; should match the active Gemini identity."
- "The redirect limit (5) only applies to Gemini, not Titan." Redirects after an upload are
  followed; a redirect **to** a `titan://` URL reopens the upload dialog ("Whenever you try to
  open a 'titan://' URL... via a redirect, a dialog will open").
- "Titan: Don't send requests with an empty path."
- Supports `;edit` ("the client will receive the latest version of the resource in a format
  suitable for editing").

**Phoebe** (Alex Schroeder's reference wiki server, Perl; `lib/App/Phoebe.pm` from
https://fastapi.metacpan.org/source/App::Phoebe, release App-Phoebe-4.07):

- Path must match `^(?:/(space))?(?:/raw|/page|/file)?/([^/;=&]+(?:;\w+=[^;=&]+)+)` — i.e. at
  least one `;key=value` on the **last** segment; parameters are split on `[;=&]` and URL-decoded,
  so `&` also works as a separator (a leftover from Sean Conner's proposal).
- `size` must be all digits and at most `wiki_page_size_limit`, else `59`.
- Token: if the wiki has tokens configured and none is sent, `59 Uploads require a token`; wrong
  token also `59`. (Phoebe uses 59 for every validation failure, not 50/60/61.)
- MIME: `$params->{mime} || "text/plain"` — Phoebe's default is text/plain, contradicting the
  spec's text/gemini default. text/plain and text/gemini are always allowed; other types only if
  configured.
- Body handling: reads until exactly `size` bytes; if more arrive, `59 Received more than the
  promised N bytes`; text that is not valid UTF-8 gets `59`; `size=0` deletes the page or file.
- Success: `30 gemini://host/page/<id>` (redirect to the Gemini view of the saved page).
- Processing timeout 10 s (alarm) after the body has arrived.

**GmCapsule** (skyjake's Python server; https://gmi.skyjake.fi/gmcapsule/): Titan and Gemini on
the same port; CGI scripts receive the body on stdin and `TITAN_TOKEN` and `REMOTE_IDENT` (client
cert hash) in the environment; the script writes a normal Gemini response.

**kulak/gemini** (Go server library, "Titan protocol tested with Lagrange browser";
`request.go`): splits the whole URL path on `;`, first element is the path, remaining elements are
parameters; `edit` must be the only parameter; unknown parameters are ignored; `size` parsed as
int64; `ReadTitanPayload` does `io.ReadFull` for exactly `size` bytes.

**gmid `titan(1)`** (Omar Polo, in the gmid distribution since 2023-07): `titan [-C cert] [-K key]
[-m mime] [-t token] url [file]`, reads stdin if no file, "alters the passed url to include the
parameter for the file size as well as the MIME and the token if -m or -t are given", exits 0 when
the response is 2x/3x, 2 otherwise, "doesn't perform TOFU". Note: the gmid **server** does not
serve Titan (no mention in gmid.8/gmid.conf.5); Agate, Molly Brown and gemserv READMEs also have no
Titan support.

### 3.3 Known Titan-capable software

Clients: Lagrange (desktop, mobile, TUI variant), Elpher via the `gemini-write` package
(`e` to edit, `C-c C-c` to save, tokens via `elpher-gemini-tokens`;
https://github.com/emacsmirror/gemini-write), gmid's `titan` CLI, Alex Schroeder's Perl `titan`
CLI (App::Phoebe, `--token`, `--mime`, `--cert_file`, `--key_file`, multi-file upload to a URL
ending in `/`), Rust `trotter` crate/`trot` CLI (1.0.2, 2026-09-02, `Actor::upload` with
`Titan { content, mimetype, token }`), Rust `titanite` (client+server library).
gmni, gemget, Amfora, Kristall, Bombadillo, diohsc have **no** Titan support.

Servers: Phoebe, GmCapsule, Atlas (C#), Bunkum (C#), Maple (C++), kulak/gemini (Go), titanit/
titanite (Rust). Sources: https://geminiprotocol.net/software/ and
https://raw.githubusercontent.com/kr1sp1n/awesome-gemini/main/README.md.

### 3.4 Ambiguities we must decide

1. **Parameter placement.** Spec: on the last path segment, before `?`. kulak splits the whole
   path on `;`. Decision: parse `;key=value` pairs only from the final segment; reject a `;` in
   earlier segments with 59, and never use `;` in our own resource paths.
2. **Parameter order and duplicates.** Accept any order; reject duplicate keys and unknown keys
   (spec: "No other parameters are used") except `edit`.
3. **Default MIME.** Spec says text/gemini; Phoebe defaults to text/plain; Lagrange sends
   text/plain for typed text. Decision: treat a missing `mime` as text/gemini per spec, and treat
   text/plain as text/gemini for gemtext-typed resources (issues, comments), as the spec allows.
4. **Request-line length.** Titan does not state one. Gemini's 1024-byte rule applies to the URI
   and clients assume it; enforce 1024 bytes on the request line including parameters.
5. **Early rejection.** When we reply before the body (60/61/50/59), the client may still be
   writing; we must send the header, send close_notify, and drain/discard input for a bounded
   time to avoid RST-induced client errors that hide our status line. Lagrange expects "Client
   should check for a server response when the connection is closed."
6. **Response after upload.** `30 gemini://...` (Phoebe style) is the most interoperable: Lagrange
   follows it and displays the resource. Do **not** redirect to a `titan://` URL (Lagrange would
   reopen the upload dialog). For API-ish endpoints a `20 text/gemini` body with the result is
   also fine.
7. **size=0 is delete.** We should require `size=0` deletes to be explicit only on resources where
   deletion makes sense, and reply 50/61 elsewhere; an empty file upload is otherwise
   indistinguishable.
8. **Identity scoping.** Lagrange maps a `titan://host/p` request to the identity used for
   `gemini://host/p`. Our 60 challenges must be issued on Gemini URLs whose path prefix covers
   the Titan endpoints, so the same identity is offered automatically.
9. **Clients ignore parameters in links.** We cannot pre-fill token/mime through link URLs; the
   token is typed by the user. Prefer client-certificate authorisation and make tokens optional.
10. **`;edit`.** Optional; supported by Lagrange. Worth serving for issue/wiki-like resources as
    a "load current text into the editor" path.

---

## 4. Gemfeeds (subscription companion spec)

Source: https://geminiprotocol.net/docs/companion/subscription.gmi ("Subscribing to Gemini pages").
It defines how to read a text/gemini page as if it were an Atom feed.

Quoted rules:

- Feed `id` and `link`: the URL the document was fetched from.
- Title: "The contents of the first header line in the document beginning with a single # serves
  as the feed's required 'title' element." Authors are told to use self-describing titles.
- Subtitle: "If a header line beginning with ## occurs in the document after the first line
  beginning with a single # but before any non-empty, non-header lines, its contents may serve as
  the feed's optional 'subtitle' element."
- Entries: "Each link line where the URL is followed by a label whose first 10 characters
  correspond to a date in ISO 8601 format (i.e. YYYY-MM-DD) represents a single entry. Link lines
  which do not meet this criteria are ignored."
- Entry id and link: the link URL (so one entry per URL; a second link to the same URL is the
  same entry).
- Entry updated: "noon UTC on the day indicated by the 10 character date stamp".
- Entry title: "what remains of the corresponding link line's label after discarding the first
  whitespace-separated component", with optional sanitisation of separators such as
  "1965-03-23 - Gemini 3 launch successful!".
- Feed updated: max of entry updated, or fetch time if there are no entries.
- Ordering: not mandated; the example is newest-first. Count: no limit; the spec targets "ten or
  twenty hand-picked gemlogs" updating "every few days" and says a full Atom feed may be published
  alongside.

How clients actually detect entries:

- **Lagrange** (`src/feeds.c`): regex
  `^=>\s*([^\s]+)\s+([0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9])([^0-9].*)` — the date must be
  followed by a non-digit character and at least one more character, so `=> url 2026-09-10` with
  no title is **not** an entry. Entries on the same day get one-second offsets to preserve page
  order. Subscriptions are bookmarks tagged `.subscribed`; refresh interval is user-selectable (was
  hard-coded 4 h); up to 10 feeds are fetched concurrently; non-heading entries whose discovery
  time is older than `maxAge_Visited` (6 x 30 days, `src/visited.c`) are dropped from the local
  cache. Lagrange also has a non-standard "heading" subscription mode that treats `#` headings as
  entries.
- **Amfora** (`subscriptions/subscriptions.go`, README, CHANGELOG): does **not** implement this
  spec. It supports Atom/RSS/JSON Feed via gofeed, recognised only when the media type is
  `application/atom+xml`, `application/rss+xml` or `application/json+feed`, or the filename is
  `atom.xml`, `feed.xml`, `feed.json` or ends in `.atom`/`.rss`/`.xml`; otherwise a subscription
  is a "page" subscription that only records a SHA-256 hash and reports "changed".
- **Antenna / CAPCOM-style aggregators** follow the companion spec; they were not re-verified in
  this pass.

Practical rule for the forge: every activity page must start with a single `#` title, optionally
one `##` subtitle, then link lines of the form `=> <url> YYYY-MM-DD - <title>` (newest first,
one link per event URL, non-digit after the date). Serve a parallel Atom feed at a `.xml`/`.atom`
path with `application/atom+xml` for Amfora and generic readers.

---

## 5. Misfin

Status as of 2026-09:

- Misfin(B) is the last spec from the original author "Lem" (gemini://misfin.org/specification.gmi,
  "updated 11 may 2023"; GitHub mirror https://github.com/JCLemme/misfin last pushed 2023-05-17).
  Transport: TLS on TCP 1958; request `misfin://<MAILBOX>@<HOSTNAME><SPACE><MESSAGE>\r\n`, whole
  request <= 2048 bytes, message is gemtext; sender identity is a self-signed client cert with
  `USER_ID` = mailbox, `CN` = display name, `SAN` = hostname; response `20 <sha256 fingerprint of
  recipient cert>`; status families 2x/3x/4x(40-45)/5x(50-53,59)/6x(60-64).
- Misfin(C) is a **community proposal, draft 9 (2024-02-06, Martin Keegan)**, gemini://satch.xyz/
  misfin/proposed-misfin-c-9th-draft.gmi. It is "deliberately incompatible with Misfin(B)" and
  "in no sense endorsed by Lem, the Misfin maintainer" who "is currently absent". Request becomes
  `misfin://<MAILBOX>@<HOSTNAME><TAB><CONTENT-LENGTH>\r\n<MESSAGE>`; first line <= 1024 bytes;
  message <= 16384 bytes with three metadata lines (senders, recipients, timestamps) before the
  gemtext body; 20 META is the lower-case hex SHA-256 of the recipient's public key.
- Implementations listed on gemini://satch.xyz/misfin/ (page maintained by satch): clients Skylab,
  Lagrange >= 1.18 (send only, "does not maintain a local Misfin mailbox"), cipres, misfinmail,
  misfin-shell; servers clseibold's misfin-server (Go), cipres (Python), Estampa, vertx-misfin-
  server (Java), ratatoskr; Titlani (https://github.com/alanbato/titlani, Python, Misfin(C), last
  push 2026-02-20). The reference implementation is marked "No Active development". The interop
  report was last updated 2024-02-06. A Nov 2025 blog post
  (https://blog.woodpeckersnest.space/2025/11/30/roughnecks/misfin-server-gemini-inboxes/) shows a
  single-operator deployment still choosing between server implementations.
- Identity verification model (Lagrange 1.18 post, gemini://skyjake.fi/gemlog/2024/09/lagrange-1.18.gmi):
  "servers are expected to verify the identity of unknown senders by checking if the sender's
  certificate matches the one provided by their server". A forge that sends Misfin mail therefore
  must also run a Misfin server on its hostname (port 1958) that presents the sender's certificate.

Viability for forge notifications: low today. Two incompatible wire formats (B vs C), a handful
of hobbyist servers, and nearly zero user mailboxes; recipients need a Misfin address, and the
forge would need to operate a Misfin server for sender verification.

**Recommendation: defer.** Keep the notification layer pluggable (per-user "notification
address" that is initially a gemfeed URL only), and revisit Misfin(C) once the draft stabilises
and Lagrange or another mainstream client can *receive*. Nothing in the data model should
preclude adding `misfin_address` to a user profile later.

---

## 6. Client landscape and interoperability quirks

| Client | Recency | Client certs | Server-cert model | Titan | Feeds |
|---|---|---|---|---|---|
| Lagrange (skyjake) | v1.21.1, 2026-09-03; active | Full identity UI; URL-prefix scope; import PEM; temporary identities; default expiry year 9999 | TOFU on **public key** (SPKI) + port, plus CA trust; expired certs rejected (1-hour exception) | Yes, incl. `;edit` | Gemfeed regex (section 4) |
| Amfora (makew0rld) | v1.11.0, 2025-07-14 | Config-file `[auth.certs]`/`[auth.keys]` keyed by domain, optionally path (CHANGELOG #115); no in-app generation | TOFU on SPKI SHA-256 + expiry (`client/tofu.go`, "Better than cert.Raw, see #7") | No | Atom/RSS/JSON + page-change only |
| gmni / gmnlm (sircmpwn) | "This project is complete"; gmnisrv "no longer maintained" | `-E cert:key` | TOFU per DeVault's design (whole-cert fingerprint stored in known_hosts with notAfter); `-j always|once|fail` | No | No |
| Kristall (MasterQ32) | last release V0.4 2022-09; repo pushed 2026-09-08 | "Supports client certificates" | "TOFU and CA TLS handling" | No | Not documented |
| Elpher (Emacs, manual v3.7.0) | active | Throwaway vs persistent certs; used "for all subsequent gemini requests involving URLs begining with the URL for which the certificate was created", forgotten on a non-matching URL; `elpher-certificate-map` auto-activates by URL prefix | **No verification by default** (`elpher-gemini-TLS-cert-checks` nil) | Via `gemini-write` | No |
| Bombadillo | site current | Not documented (treat as read-only) | "TOFU-style certificate system" | No | No |
| diohsc (Haskell) | Hackage current | Yes | TOFU and/or trusted CAs, "TLS resuming, 0RTT" | No | No |

Known client-certificate pitfalls:

1. **Scope is a string prefix, not a path-segment prefix.** Lagrange (`isUsedOn_GmIdentity`:
   `startsWithCase_String(url, used)`) and Elpher match textual prefixes. An identity activated on
   `gemini://forge.example/alice` will also be sent to `gemini://forge.example/alice-bob`. Issue
   60 on directory-style URLs ending in `/` and keep all authenticated routes beneath a common
   prefix (e.g. `/~alice/` or `/settings/`).
2. **Default port normalisation.** Lagrange strips `:1965` before matching; Amfora keys TOFU and
   certs by `domain` plus a `:port` suffix only for non-default ports. Always emit canonical URLs
   without `:1965`.
3. **Clients never auto-retry a 60.** The user must pick an identity; the client then repeats the
   *same* URL. The 60 META text is the only guidance the user sees; write it as an instruction
   ("Select or create an identity to sign in; it will be linked to your account").
4. **Cross-scheme reuse.** Lagrange reuses the Gemini identity for the matching `titan://` URL;
   Elpher's map is scheme-literal. Keep `gemini://` and `titan://` paths identical for a resource.
5. **Certificates are long-lived and self-signed.** Lagrange creates identities that expire in
   9999; Amfora's docs suggest `-days 1825`. Do not reject certificates for long validity, absent
   CN, or self-signature; do reject expired/not-yet-valid ones with 62 as the spec says.
6. **What to key accounts on.** Clients show a SHA-256 fingerprint "of the full certificate or
   just the public key" (Lagrange changelog). Key accounts on the SHA-256 of the DER
   SubjectPublicKeyInfo so a user can re-issue a certificate with the same key (Lagrange's own
   server-side policy) and allow several certificates per account.
7. **TLS 1.2 leaks the client certificate.** Clients "SHOULD warn" under 1.2; run TLS 1.3 where
   the peer supports it and accept 1.2 only for compliance.
8. **0-RTT.** diohsc advertises TLS early data; never treat 0-RTT data as an authenticated
   request (disable early data server-side).
9. **Elpher performs no server-cert validation by default**; nothing to do server-side, but it
   means TOFU warnings will not protect those users on rotation.

---

## 7. Existing Gemini forge / git-browsing projects

| Project | What it is | State | Lessons |
|---|---|---|---|
| Gemigit (https://github.com/RealMelkor/Gemigit; gemini://gmi.rmf-dev.com) | "A self-hosted gemini git service" in Go/SQLite/MySQL; private/public repos, groups, LDAP, TOTP 2FA, "Option to use token authentication when doing git operations", git served over **HTTP and SSH**, stateless mode for load balancing. ISC. | Last push 2024-08-11; 120 commits | The only real Gemini-native forge. Writes happen through Gemini input prompts and git over HTTP/SSH, not Titan. Auth mechanism details were not verified in this pass. |
| git-gemini-forge (https://git.average.name/AverageHelper/git-gemini-forge; gemini://git.average.name) | Rust read-only Gemini front-end to a Forgejo/Gitea/GitLab API | "under active construction, and is missing basic features", "Viewing source files is not implemented yet" | Respects robots.txt; author: "proxying external git forges that you do not own is very rude when done without permission". |
| gemini-git-browser (https://github.com/masalachai/gemini-git-browser; gemini://git.ritesh.ch) | Rust, browse repos/trees/files; converts README.md to gemtext | Last push 2021-07 | Markdown-to-gemtext conversion is expected by users; namespace-based repo layout. |
| gmnigit (https://git.sr.ht/~kornellapacz/gmnigit) | Go static generator "inspired by stagit": log, tree, refs; `-max-commits` | Small | Regenerate in `post-receive`; cap history rendering. |
| git.gmi (https://git.sr.ht/~fkfd/git.gmi) | Python CGI (pygit2) under Jetforce: summary, tree, blob, log; "feature-complete" v0.4.0 | Frozen | "Don't forget the trailing slash" because relative links break otherwise; CGI chosen for licensing reasons. |
| gemini-git (https://git.sr.ht/~hugo_wetterberg/gemini-git) | "serves up your public sourcehut repositories over gemini" | Not fetchable (502) | Sourcehut itself has no Gemini mirror; this is third-party. |
| Titan-based wikis (Phoebe, GmCapsule capsules) | The only widely deployed Titan writers | Active | Titan is proven for wiki-style text editing with tokens + client certs, which maps well onto issues/comments/wiki pages. |

None of these use Titan for forge writes and none rely on client certificates for accounts (except
possibly Gemigit); a Gemini + Titan + client-cert forge is new ground, so interoperability testing
against Lagrange is the critical path.

---

## 8. Server-side TLS practice

Facts:

- The spec requires TLS >= 1.2, SNI, close_notify, and recommends TOFU (section 1.5).
- Drew DeVault's "TOFU recommendations for Gemini" (https://drewdevault.com/blog/Gemini-TOFU/):
  "you should use a self-signed certificate, and you should not use a certificate signed by one
  of the mainstream certificate authorities." His client algorithm stores hostname, fingerprint
  algorithm, hex fingerprint of the certificate, and the notAfter timestamp; a mismatch before
  expiry is UNTRUSTED with no in-app override; expired records are disregarded.
- Lagrange since 1.6 (gemini://skyjake.fi/gemlog/2021/07/lagrange-1.6.gmi): "fingerprints... are
  now generated based on public keys. This way, a server is allowed to renew their certificate
  without losing trust, as long as the key pair is not changed. Also, the server port number is
  included when matching fingerprints". "Expired certificates are always untrusted, and an error
  page is shown" (1-hour temporary exception). Lagrange also accepts CA-signed certs ("verified
  using TOFU and trusted root CAs").
- Amfora pins SHA-256 of `RawSubjectPublicKeyInfo` and the expiry; gmni/Bombadillo-era clients
  pin the whole certificate.
- Server defaults in the ecosystem: Agate auto-generates self-signed certs expiring
  `4096-01-01` ("For Gemini it is recommended by the specification to use self signed
  certificates because Gemini uses the TOFU... principle"); gmnisrv generates certs valid for
  `INT32_MAX` seconds ("68 years"); Lagrange client identities default to year 9999.

Implications:

1. **Use one long-lived self-signed certificate per hostname** (10+ years or effectively
   non-expiring), with the hostname in SAN (gmid's `gg` and Go clients verify names). Do not use
   Let's Encrypt for `gemini://` — 60-90 day renewals would trigger warnings in whole-cert TOFU
   clients on every renewal, and default LE tooling rotates keys.
2. **Never rotate the key pair casually.** Key rotation is a trust reset for every client. If it
   must happen, schedule it at the old cert's notAfter, because TOFU clients treat expired
   records as "unknown host" rather than "mismatch"; publish the new fingerprint out of band
   (site news, DNS TXT, Fediverse) beforehand. Cert-only renewal with the same key is invisible to
   Lagrange/Amfora but still a warning in gmni-style clients.
3. **Anycast / multiple POPs:** every POP must present the byte-identical certificate and key.
   Same key with different certs would satisfy SPKI-pinning clients but not whole-cert-pinning
   ones. Pin per port as well: run Gemini and Titan on the same 1965 listener, or, if Titan gets
   its own port, use the same certificate there.
4. **Client-cert handling on the server:** request (but do not require) a client certificate on
   every handshake with no CA verification (accept self-signed, any CN, absent chain); validate
   only the validity window (62 on failure) and compute the SPKI SHA-256 for identity. Requiring a
   cert at handshake level would break plain reads.
5. **TLS 1.3 preferred, 1.2 allowed**; send close_notify after every response, including Titan
   early rejections; set an idle/handshake timeout and a Titan body timeout (Phoebe uses 10 s of
   processing time; the Titan spec warns about slow-loris style uploads).

---

## 9. Testing tools for CI

Command-line clients (all script-friendly):

| Tool | Gemini | Titan | Client cert | Cert validation | Notes |
|---|---|---|---|---|---|
| `gmni` (C, BearSSL; https://sr.ht/~sircmpwn/gmni/) | yes | no | `-E cert.pem:key.pem` | `-j always|once|fail` TOFU modes | `-I` prints only `status meta`; `-i` prints header then body; `-L` follow redirects; `-d input` answers a 10/11 prompt; `-N` treats 10 as success; non-2x exit status equals the response status. Project is "complete" (unmaintained). |
| `gemget` (Go; https://github.com/makew0rld/gemget, v1.9.0 2023) | yes | no | `--cert`, `--key` | `-i/--insecure` skips checks | `--header` prints `Header: <status> <meta>`; `-o -` to stdout; `-r N` redirect limit. |
| `gg` (from gmid; https://github.com/omar-polo/gmid) | yes | no | `-C cert -K key` | name verification only (`-N` disables), `-H sni`, `-2`/`-3` force TLS version, `-T seconds` timeout | Warns when a server omits close_notify - useful for a conformance check. |
| `titan` (from gmid) | no | yes | `-C cert -K key` | none | `titan -m text/gemini -t TOKEN titan://host/path file` (stdin if no file); adds `;size=` itself; exit 0 on 2x/3x, 2 otherwise, 1 on error. Best CI choice for Titan. |
| `titan` (Perl, App::Phoebe) | no | yes | `--cert_file`, `--key_file` | none | `--token`, `--mime`; multiple files to a URL ending in `/`. |
| `trot` (Rust trotter 1.0.2, `cargo install --features cli trotter`) | yes | yes | `.cert_file/.key_file` | OpenSSL | Library API `Actor::upload(url, Titan{content,mimetype,token})`. |
| `openssl s_client` | yes | yes | `-cert -key` | `-verify` optional | See scripts below. |

Libraries for writing our own harness: Go `git.sr.ht/~sotirisp/go-gemini` (v0.3.0, 2025-04, has
`certificate` and `tofu` packages; docs hidden on pkg.go.dev due to license), Go `kulak/gemini`
(server-side Titan parsing, 2024-10), Rust `titanite` (Titan client+server, 2025-02). For a
minimal harness, raw `crypto/tls` / `rustls` with a 1024-byte request line is enough.

Scripting a Gemini request with OpenSSL (used to fetch every `gemini://` source in this document):

```sh
printf 'gemini://%s/%s\r\n' "$HOST" "$PATH_" \
  | openssl s_client -quiet -nocommands -connect "$HOST:1965" -servername "$HOST" 2>/dev/null
```

Scripting a Titan upload with OpenSSL:

```sh
f=issue.gmi
{ printf 'titan://%s/%s;size=%d;mime=text/gemini\r\n' "$HOST" "$PATH_" "$(stat -c%s "$f")"; cat "$f"; } \
  | openssl s_client -quiet -nocommands -connect "$HOST:1965" -servername "$HOST" \
      -cert client.pem -key client.key 2>/dev/null
```

Caveats verified against OpenSSL 3.0.13: `-quiet` implies `-ign_eof` (needed so s_client waits for
the server's response after stdin closes); `-nocommands` is required because s_client otherwise
interprets lines beginning with `Q`, `R`, `k`, etc. as interactive commands, which will corrupt
binary or gemtext bodies. Use `-tls1_2` / `-tls1_3` to test both versions, and check for
`close_notify` by confirming s_client exits without `read:errno=104`.

Generating a test identity (Amfora's documented recipe):

```sh
openssl req -new -subj "/CN=ci-user" -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -days 1825 -nodes -out client.pem -keyout client.key
```

Suggested CI matrix: (a) `gmni -j always -I` for status/META assertions on every route,
(b) `gg -3` and `gg -2` for TLS-version coverage and close_notify warnings, (c) gmid `titan` with
and without `-C` to assert 60/61/20/30 paths and `size=0` deletes, (d) an OpenSSL script that
sends a wrong `size` (both short and long) and a 1025-byte request line to assert 59, (e) a
gemfeed validator that applies Lagrange's regex to the activity pages, (f) periodic manual
testing in Lagrange (desktop + `clagrange` TUI), the reference client for Titan and identities.

---

## 10. Implications for the forge

Design constraints derived from the above:

1. **Request parsing:** absolute URI + CRLF, reject >1024 bytes, userinfo, or fragment with 59;
   treat empty path as `/`; canonical URLs without `:1965`; require SNI to select the vhost.
2. **Responses:** `NN META\r\n`; META <= 1024 bytes; `text/gemini; charset=utf-8; lang=en` on
   gemtext; only defined status codes; always close with close_notify; never use compression or
   chunking; bare LF line endings are fine.
3. **Rate limiting:** use 44 (exponential back-off expected; META is free text in 0.24.1, so the
   message should say how long to wait in words rather than assume clients parse a number).
4. **Identity = client certificate.** Account key is SHA-256 of the SPKI; allow multiple certs per
   account; accept self-signed/long-lived/no-CN certs; 62 only for validity-window or malformed
   certs; 61 for "known cert, not allowed here"; 60 with instructional META on a `/`-terminated
   URL that is a prefix of every authenticated route, because clients scope identities by string
   prefix and never retry automatically.
5. **Sign-up flow:** a 60 on e.g. `gemini://forge/account/` -> user picks/creates identity ->
   forge sees an unknown SPKI -> 10 prompt for a username (one input per round trip, <= ~900
   bytes of query) -> account created. Passwords are unnecessary; 11 exists if we ever need a
   secret.
6. **Titan endpoints:** same host/port/path as the Gemini view (`titan://forge/<same path>`),
   parameters parsed only from the last path segment (`size` mandatory, `mime` default
   text/gemini, `token` optional, `edit` alone), reject unknown/duplicate parameters and >1024-byte
   lines with 59, per-resource size limits announced in the 59 message, read exactly `size`
   bytes, then respond `30 gemini://forge/<resource>` (never to a `titan://` URL) or a `20`
   gemtext receipt. `size=0` = delete only where documented. Early rejections (60/61/50/59) must
   send close_notify and drain input briefly.
7. **Auth for Titan:** rely on the client certificate (Lagrange automatically reuses the Gemini
   identity for the matching Titan URL); make `token` optional and use it only for secondary
   purposes (e.g. a per-repo "anyone with the link can comment" token), since clients discard
   parameters embedded in links and require the user to type the token.
8. **Editing UX:** support `;edit` so Lagrange can load current text for issues/comments/wiki
   pages; keep issue/comment bodies in gemtext; accept `text/plain` uploads as gemtext.
9. **Activity feeds:** every "subscribe-able" page (repo activity, issue, user, global) is a
   gemfeed: single `#` title, optional `##` subtitle, newest-first link lines
   `=> <event-url> YYYY-MM-DD - <title>` with a unique URL per event and a non-digit character
   after the date (Lagrange's regex). Also serve `atom.xml` with `application/atom+xml` for Amfora
   and generic readers. Keep feeds reasonably short (Lagrange caches, but pages are refetched in
   full every refresh) - 50-100 entries is ample.
10. **Notifications:** gemfeeds now; Misfin deferred (spec fork B/C, few servers, no receiving
    mainstream client). Keep a per-user notification-address field extensible.
11. **Server TLS:** one long-lived self-signed cert per hostname with SAN, key never rotated
    casually, byte-identical cert/key on every POP for anycast, same cert on the Titan port if
    separate, TLS 1.3 preferred with 1.2 fallback, no 0-RTT, request-but-not-require client certs
    with no CA validation.
12. **Content rendering:** README.md -> gemtext conversion, trailing-slash-consistent directory
    URLs so relative links resolve, preformatted blocks with the language as alt text for source
    and diffs (Lagrange/Amfora highlight from alt text), and cap history/diff sizes per page.
13. **Git transport:** none of the Gemini projects push over Gemini/Titan; git over SSH remains
    the write path for repositories, with the SSH key registered through a Titan upload or an
    input prompt after client-cert login.
14. **Test tooling:** gmni, gmid's `gg`/`titan`, and OpenSSL scripts cover Gemini and Titan in CI;
    Lagrange is the manual reference client and the only one that exercises the full Titan +
    identity + `;edit` + gemfeed path.

### Source index

- Gemini protocol spec v0.24.1: https://geminiprotocol.net/docs/protocol-specification.gmi
- Gemtext spec v0.24.1: https://geminiprotocol.net/docs/gemtext-specification.gmi
- Subscription companion spec: https://geminiprotocol.net/docs/companion/subscription.gmi
- Best practices: https://geminiprotocol.net/docs/best-practices.gmi ; TLS tutorial (incomplete): https://geminiprotocol.net/docs/tls-tutorial.gmi
- Titan spec / auth / history / edit link: gemini://transjovian.org/titan/ (pages listed in 3.1)
- Lagrange source and docs: https://github.com/skyjake/lagrange (`src/gmrequest.c`, `src/gmcerts.c`, `src/feeds.c`, `src/visited.c`, `res/about/version.gmi`, `res/about/help.gmi`); gemlogs gemini://skyjake.fi/gemlog/2021/07/lagrange-1.6.gmi and gemini://skyjake.fi/gemlog/2024/09/lagrange-1.18.gmi
- GmCapsule: https://gmi.skyjake.fi/gmcapsule/
- Phoebe: https://fastapi.metacpan.org/source/App::Phoebe (App-Phoebe-4.07)
- kulak/gemini: https://github.com/kulak/gemini ; titanite: https://github.com/YGGverse/titanite ; trotter: https://docs.rs/trotter/latest/trotter/
- gmid (`gg`, `titan`): https://github.com/omar-polo/gmid (gg.1, titan.1, ChangeLog)
- gmni: https://sr.ht/~sircmpwn/gmni/ (doc/gmni.scd) ; gmnisrv: https://git.sr.ht/~sircmpwn/gmnisrv
- gemget: https://github.com/makew0rld/gemget
- Amfora: https://github.com/makew0rld/amfora (README, CHANGELOG, `subscriptions/subscriptions.go`, `client/tofu.go`), wiki https://github.com/makew0rld/amfora/wiki/Client-Certificates
- Kristall: https://github.com/MasterQ32/kristall ; Elpher manual: https://elpa.nongnu.org/nongnu-devel/doc/elpher.html ; gemini-write: https://github.com/emacsmirror/gemini-write
- Bombadillo: https://bombadillo.colorfield.space/ ; diohsc: https://hackage.haskell.org/package/diohsc
- Misfin: gemini://misfin.org/ , gemini://satch.xyz/misfin/ (incl. proposed-misfin-c-9th-draft.gmi, misfin-interop.gmi), https://github.com/JCLemme/misfin , https://github.com/alanbato/titlani , https://blog.woodpeckersnest.space/2025/11/30/roughnecks/misfin-server-gemini-inboxes/
- Forge projects: https://github.com/RealMelkor/Gemigit , https://git.average.name/AverageHelper/git-gemini-forge , https://github.com/masalachai/gemini-git-browser , https://git.sr.ht/~kornellapacz/gmnigit , https://git.sr.ht/~fkfd/git.gmi , https://git.sr.ht/~hugo_wetterberg/gemini-git
- TOFU: https://drewdevault.com/blog/Gemini-TOFU/ ; Agate README: https://github.com/mbrubeck/agate ; software list: https://geminiprotocol.net/software/ ; awesome-gemini: https://github.com/kr1sp1n/awesome-gemini
