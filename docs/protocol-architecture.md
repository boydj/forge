# Protocol architecture

```
                     HUMAN INTERFACE

               Gemini             Titan
                READ              WRITE
                 |                  |
                 +--------+---------+
                          |
                    TLS identity
                          |
                       account
                          ^
                          |
                       SSH key
                          |
                 +--------+---------+
                 |                  |
             Git fetch          Git push
                 |                  |
                 +------ SSH -------+

             Subscriptions ---- Gemfeeds
             Notifications ---- Misfin (later, adapter only)
```

## Gemini

Port 1965, TLS 1.2+ with SNI, one request per connection. Requests are absolute
URLs of at most 1024 bytes. Responses are `<status> <meta>\r\n` followed by a
body for 2x. The forge uses:

| Status | Use |
| --- | --- |
| 10 | INPUT for short fields (username, search, enrolment code) |
| 20 | `text/gemini; charset=utf-8` pages, raw blobs with detected MIME |
| 30/31 | after Titan writes and for canonical paths |
| 40/41 | transient failures, drain |
| 42 | CGI-equivalent: subprocess failure |
| 44 | slow down (rate limits) |
| 50/51 | bad request / not found |
| 59 | malformed request |
| 60/61/62 | certificate required / not authorised / invalid |

Pages are gemtext. User-supplied text is rendered so that it cannot forge
link, heading or preformat lines unless the page intends it.

## Titan

Same port and TLS. Request line `titan://host/path;size=N;mime=TYPE;token=T\r\n`
followed by exactly N bytes. The forge requires a client certificate for every
Titan request, enforces per-path size limits and MIME allowlists, writes bodies
to a temporary file under quota, applies the change in one database
transaction, and answers `30 gemini://host/<result path>`.

## Git over SSH

Public-key authentication only. The only accepted commands are
`git-upload-pack '<repo>'` and `git-receive-pack '<repo>'`. Protocol v2 is
advertised via `GIT_PROTOCOL`. No PTY, no shell, no forwarding, no subsystems.

## Gemfeeds

`text/gemini` pages whose first heading is the feed title and whose link lines
begin with an ISO date. Served at `/feed`, `/~user/feed`, `/~user/repo/feed`,
`/~user/repo/issues/feed`, `/~user/repo/releases/feed`.
