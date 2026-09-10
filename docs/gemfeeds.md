# Gemfeeds

Activity is published as gemfeeds, `text/gemini` pages that feed readers
parse according to the Gemini subscription companion specification
(`gemini://geminiprotocol.net/docs/companion/subscription.gmi`), with an Atom
twin next to each for readers that only speak Atom. Feeds are views over the
`events` table (`docs/operations.md`, "Schema"); code is
`internal/web/feeds.go`.

## Paths

| Gemfeed | Atom twin | Contents |
| --- | --- | --- |
| `/feed` | `/atom.xml` | forge-wide activity |
| `/~user/feed` | `/~user/atom.xml` | everything the user did |
| `/~user/repo/feed` | `/~user/repo/atom.xml` | all events of the repository |
| `/~user/repo/issues/feed` | `/~user/repo/issues/atom.xml` | `issue.open`, `issue.close`, `issue.reopen`, `comment` |
| `/~user/repo/releases/feed` | `/~user/repo/releases/atom.xml` | **planned (M3)**; `/releases/` is a placeholder page today |

Events belonging to private repositories are only included when the viewer
(by client certificate) is the owner, a collaborator or an admin; deleted
repositories' events are hidden. Each feed returns the newest 60 events.

## Format produced

```
20 text/gemini; charset=utf-8; lang=en
# alice/proj activity
## Gemfeed from git.example.net
=> /~alice/proj/ home

=> /~alice/proj/commit/3f2c... 2026-09-10 - alice pushed 1 commit to main in alice/proj: return one
=> /~alice/proj/log/main 2026-09-09 - alice created branch main in alice/proj (2 commits)
=> /~alice/proj/refs 2026-09-09 - alice tagged v1.0 in alice/proj
=> /~alice/proj/issues/1 2026-09-09 - bob opened issue #1: crash on start
```

- First heading = feed title (`<title> activity`, or `<owner>/<repo> issues`
  for filtered feeds); the `##` line is the subtitle; the `home` link is not
  an entry (no date).
- One entry per link line: `=> <path> YYYY-MM-DD - <subject>`. The ` - `
  separator guarantees a non-digit after the date (Lagrange's parser
  requires one) and is stripped by readers, leaving the subject as the
  entry title.
- Dates are UTC; readers set the entry time to noon UTC of that day.
- Links are root-relative paths; readers resolve them against the feed URL.
- No entries: the page says `No activity yet.` and remains a valid,
  subscribable feed.

Per the companion spec the entry **id is the link URL**, so several events
pointing at the same page (an issue opened, commented, closed) collapse into
one entry in gemfeed readers, showing the newest. The Atom twin keeps them
distinct.

## Atom twins

`application/atom+xml; charset=utf-8`, Atom 1.0: feed `id`/`link` are the
absolute `gemini://` URL of the home page, `updated` is the newest event
time (or now), each entry has `title` = subject, `id` =
`gemini://host/path#event-<n>`, `updated` in RFC 3339, `link` = absolute
`gemini://` URL and `author/name` when a user caused the event.

## Entry title conventions

Subjects are one line, plain text, start with the actor and read as a
sentence, so they stand alone in an aggregator:

| Kind | Subject | Links to |
| --- | --- | --- |
| `repo.create` | `alice created alice/proj` | `/~alice/proj/` |
| `repo.update`, `repo.archive` | `alice updated alice/proj` | `/~alice/proj/` |
| `repo.delete` | `alice deleted alice/proj` | `/~alice/` |
| `push` (branch created) | `alice created branch main in alice/proj (3 commits)` | `/~alice/proj/log/main` |
| `push` (branch updated) | `alice pushed 2 commits to main in alice/proj: <subject of head commit>` | `/~alice/proj/commit/<id>` |
| `push` (branch/tag deleted) | `alice deleted branch x in alice/proj` | `/~alice/proj/refs` |
| `push` (tag) | `alice tagged v1.0 in alice/proj` | `/~alice/proj/refs` |
| `issue.open` | `bob opened issue #4: <title>` | `/~alice/proj/issues/4` |
| `issue.close`, `issue.reopen`, `issue.edit` | `bob closed issue #4: <title>` | the issue |
| `comment` | `bob commented on issue #4: <title>` | the issue |
| `user.create` | `new user bob` | `/~bob/` |
| `user.key.add`, `user.cert.add` | `SSH key added`, `certificate added` | `/account/keys`, `/account` |

`change.*`, `review`, `release`, `release.asset`, `admin` and
`replica.status` kinds are defined in `internal/store/events.go` and will be
emitted by M3/M5 features. User-supplied text in subjects (issue titles) is
already one-line and escaped by the gemtext writer, so it cannot break the
link line.

## Client notes

- **Lagrange**: bookmark the feed page and tick "Subscribe to feed"; it
  refetches on its own schedule and lists new entries under Feeds. Lagrange
  keeps only entries newer than its visited-history window (about six
  months), which our 60-entry pages are well inside.
- **Amfora**: does not implement the gemfeed companion spec; subscribe to
  the `atom.xml` twin (`application/atom+xml` is recognised by media type
  and by the `atom.xml` name). Subscribing to a gemfeed page in Amfora only
  reports "page changed".
- **Aggregators** (Antenna, CAPCOM-style) accept the gemfeed pages as is.
- No `Last-Modified`/conditional fetch exists in Gemini; feeds are small on
  purpose.
