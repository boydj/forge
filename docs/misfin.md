# Misfin (notifications)

Misfin is the Gemini-family mail protocol. It was evaluated as the channel
for forge notifications (mentions, review requests, issue activity) in M0.
Decision: **deferred to M12**; notifications are gemfeeds until then, and
the event model is built so that a Misfin sender can be added without
touching the data model. Research: `docs/research/protocols.md` section 5.

## Evaluation summary

| Aspect | Finding (2026-09) |
| --- | --- |
| Specification | two incompatible wire formats. **Misfin(B)** (the original author, last updated 2023-05): TLS on 1958, `misfin://mailbox@host<SP><gemtext message>\r\n`, whole request <= 2048 bytes, response `20 <sha256 of recipient cert>`. **Misfin(C)** (community draft 9, 2024-02): `misfin://mailbox@host<TAB><length>\r\n<message>`, message <= 16 KiB with sender/recipient/timestamp metadata lines; explicitly incompatible with B and not endorsed by the original maintainer, who is absent |
| Sender identity | a self-signed client certificate whose fields carry mailbox, display name and hostname. Receiving servers verify an unknown sender by connecting back to the sender's hostname on 1958 and comparing certificates, so **a forge that sends mail must also run a Misfin server** on `git.<zone>` presenting the sender certificate |
| Client support | Lagrange >= 1.18 can *send* (no mailbox); Skylab, cipres, misfinmail, misfin-shell are niche; no mainstream client *receives* |
| Servers | a handful of hobbyist implementations (clseibold's Go server, cipres, Estampa, Titlani for C, ...); the reference implementation is marked not actively developed; interop report last updated 2024-02 |
| Users | nearly no one has a Misfin mailbox; recipients would need one at a third-party server |
| Fit | message size limits suit one-line notifications with a `gemini://` link; the identity model (self-signed certificate) matches ours |

Conclusion: the protocol is viable in principle and aligned with the
project's values, but the ecosystem cannot deliver a notification today.
Building it now would mean running a second TLS service on every POP for
a channel with no readers.

## Notification design (adapter, ready for Misfin)

Everything that could become a notification is already an **event**
(`events` table, `internal/store/events.go`): kind, repository, actor,
one-line subject, gemini path, JSON payload, node, time. Feeds are views
over events; notifications will be too.

Planned pieces (M12), none of which exist yet:

```
events (append-only)  -->  notifier worker  -->  Notifier interface  -->  gemfeed (per-user, exists today)
                            (leader only,                              -->  misfin (M12)
                             cursor in settings)                       -->  others later (email is out of scope)
```

- **Subscriptions**: per-user rules stored in a new table
  (`notifications`: user, kind filter, repository filter, address kind,
  address). Defaults: activity on repositories you own or collaborate on,
  issues you opened, changes you review, mentions of `@name` in bodies.
- **`Notifier` interface** in a new `internal/notify` package:
  `Deliver(ctx, to Address, n Notification) error` where `Notification` is
  the event plus a rendered subject and absolute `gemini://` link. The
  gemfeed notifier is a no-op (the per-user feed already exists and
  subscribers poll it); the Misfin notifier composes the message from the
  same fields.
- **Address field** on the account: `misfin_address` (`mailbox@host`),
  edited via an INPUT prompt on `/account/profile`, empty by default.
  Nothing in the schema forbids adding this column later; ADR 0003's
  "additive migrations" rule applies.
- **Worker**: runs on the leader of the event's repository, keeps its
  cursor (`last_event_id`) in `settings`, retries with backoff, never
  blocks a write, and records delivery failures as `admin` events for the
  operator.
- **Misfin server side** (required for sender verification): a small
  listener on 1958 sharing `internal/gemini`'s TLS setup and the service
  certificate, answering only the verification handshake and, optionally,
  accepting replies into the issue thread (a reply becomes a comment,
  subject to the same limits as a Titan upload). Which fork (B or C) to
  implement is decided at M12 from the interop report at that time; the
  event and notifier layers are the same for both.

## What users get now

- Per-user, per-repository and per-issue-tracker gemfeeds with Atom twins
  (`docs/gemfeeds.md`) that any feed reader can poll.
- Security-relevant account events (`user.cert.add`, `user.key.add`) appear
  in the user's own feed so a compromise is visible.

## Decision record

Deferred to M12. Revisit when either the Misfin(C) draft is finalised and
adopted by at least one mainstream client that receives mail, or the B/C
split resolves; re-run the client and server survey from
`docs/research/protocols.md` section 5 first.
