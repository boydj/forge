# Replication (v1)

How forge nodes share repositories and metadata. Implements ADR 0011 (single
writer per repository; replicas pull) and ADR 0003 (metadata in SQLite,
replicated at the application layer). Code: `internal/repl`,
`internal/store/repl.go`, migration `0002_replication.sql`.

## Model

- Every repository has one **leader** node (`repositories.leader_node`).
  Only the leader accepts pushes and Titan writes for it; every node serves
  reads from its local copy.
- **Global metadata** (users, certificates, SSH keys) has one **metadata
  leader** (`cluster.metadata_leader`; default: the alphabetically first node
  name among this node and its peers). Only it creates or changes those
  rows.
- Replication is **pull-based**: a worker on every node polls every peer
  each `cluster.sync_interval` (default 10 s). Leaders additionally send a
  best-effort `notify` after a push so replicas fetch within milliseconds
  rather than waiting for the next poll.
- No consensus, no automatic failover. A lost leader stops writes for the
  repositories it leads until an operator moves leadership; reads continue.

## Control plane

Every node runs a small HTTP server on `cluster.control_listen`, an address
on the WireGuard mesh (`fda5:bc65:9bb1:1::<n>`) or loopback, never a public
interface. HTTP is acceptable here because the network is private, every
request is authenticated, and git's smart-HTTP transport gives us
replication fetches with no new code in git.

Authentication: `Authorization: Bearer <cluster secret>` compared in
constant time, plus `X-Forge-Node: <name>`; the name must be in
`cluster.peers`. Wrong secret answers 401, unknown node 403. The secret comes
from `cluster.secret_file` (`forge admin cluster init`). WireGuard already
authenticates nodes at the transport (threat model T-33); the secret keeps
stray processes on the mesh out. Per-message signatures and nonces (T-41)
are future work.

| Endpoint | Served by | Purpose |
| --- | --- | --- |
| `GET /v1/status` | every node | node name, version, metadata leader, repositories led, last local event id, time |
| `GET /v1/events?after=<id>&limit=<n>` | every node | events **authored on this node** with id > after, oldest first (max 1000) |
| `GET /v1/repos` | every node | every repository record this node knows, including soft-deleted ones |
| `GET /v1/repos/{id}/metadata` | leader of `{id}` | snapshot of the repository's collaborators, issues, changes, change versions, comments, reviews, releases, release assets |
| `GET /v1/repos/{id}/state` | every node | this node's refs for the repository and its replica bookkeeping (used by `move-leader`) |
| `GET /v1/repos/{id}/assets/{tag}/{name}` | leader of `{id}` | one release asset file, streamed with `Content-Length` and `X-Forge-SHA256` (see Release asset files) |
| `GET /v1/users` | metadata leader | snapshot of users, certificates, ssh_keys |
| `POST /v1/notify {repo_id}` | every node | ask the worker to sync one repository now |
| `POST /v1/forward` | leader | run a forwarded Titan write (see Forwarding) |
| `GET /v1/git/{owner}/{name}.git/info/refs?service=git-upload-pack`, `POST .../git-upload-pack` | leader | git smart HTTP, read only |

The git endpoints spawn `git upload-pack --stateless-rpc [--advertise-refs]`
exactly like `git http-backend`, with the client's `Git-Protocol` header
passed as `GIT_PROTOCOL` (protocol v2), gzip request bodies accepted, and
the hardened environment from `internal/vcs/git` (fsck on transfer, no
hooks, no user config). `git-receive-pack` is refused: pushes never travel
over the control plane.

Replicas fetch with an ordinary `git fetch --prune --no-tags
+refs/heads/*:refs/heads/* +refs/tags/*:refs/tags/*` against
`http://<leader control addr>/v1/git/<owner>/<name>.git`. The
`Authorization` and `X-Forge-Node` headers are injected through
`http.extraHeader` in `GIT_CONFIG_*` environment entries (not on the command
line, so the secret is not visible in `ps`), and `protocol.http.allow=always`
is added on top of the backend's `protocol.allow=never`. Refs are only ever
fetched from the repository's current leader.

## Data flow of one sync cycle

For a node N, each cycle (`Node.SyncOnce`) does, in order:

1. **Global metadata.** If N is not the metadata leader: `GET /v1/users`
   from it and upsert users, certificates and SSH keys by id. Users are
   never deleted locally (repositories reference them); certificates and
   keys absent from the snapshot are deleted.
2. For each peer P, sorted by name:
   1. `GET /v1/status`; record P in `nodes` (address, version, last seen).
   2. **Repository records.** `GET /v1/repos`; for each record decide
      whether P is authoritative (see below) and upsert the full row by id,
      including `leader_node`, `updated_at`, `pushed_at`, `deleted_at`.
   3. **Events.** With cursor `c = repl_cursors[P]`, page through
      `GET /v1/events?after=c` and insert each event with `node = P`,
      `origin_id = <P's id>` and P's timestamp, advancing the cursor in the
      same transaction. Events already present (unique on `(node,
      origin_id)`) are ignored. If an event names a repository N does not
      have yet, step 2 is repeated first so the reference is kept.
   4. **Per repository** led by P and not deleted: fetch git refs when the
      record's `pushed_at` moved, an event mentioned the repository, a
      notify named it, or the last attempt failed; pull and apply the
      metadata snapshot when `updated_at` moved, an event mentioned the
      repository, or the last attempt failed; then reconcile the release
      asset files with the replicated `release_assets` rows (every cycle;
      see Release asset files). Record the outcome in `repo_replicas`
      (status `ok`/`error`, detail, `last_synced_at`, `last_event_id` =
      cursor, `leader_updated_at`, `leader_pushed_at`).
   5. Set `forge_replica_lag_events{leader=P}` = P's last event id minus
      the cursor.
3. Set `forge_leader_repositories`.

A `notify` (or `forge admin repl resync`) runs the same steps for one
repository only, skipping the change checks so a fetch always happens.

### Which repository record wins

A record served by P is applied when P leads the repository, or when N's own
record names P as leader (so the handoff written by the old leader during
`move-leader` is honoured). Records for repositories N leads are never
overwritten by a peer. Repository ids are assigned by the creating leader;
a replica never inserts rows for repositories it does not lead, so ids never
collide (the forge layer refuses such writes with `ErrNotLeader`).

### Release asset files

Asset files live outside SQLite (`<data>/assets/<owner>/<repo>/<tag>/<name>`,
`forge.AssetPath`; the replication node builds the same path from
`repl.Options.AssetsDir`, which defaults to the `assets` sibling of
`ReposDir`). Rows in `release_assets` carry the size and sha256 of each
file, so replicas can reconcile files against rows without any extra
bookkeeping. After the per-repository step above (`Node.syncAssets`):

- Every `release_assets` row of the repository (joined with its release for
  the tag) is checked against the local file. A missing file, a non-regular
  file or a size mismatch triggers a download; after a metadata pull (or a
  `resync`) every present file is additionally hashed and re-fetched on a
  sha256 mismatch. Between metadata changes only existence and size are
  checked, so a same-length corruption is caught by the next metadata change
  or `resync`, not by every 10 s poll (hashing every asset each cycle would
  be too costly).
- Downloads come from the repository's leader, one at a time, via
  `GET /v1/repos/{id}/assets/{tag}/{name}`. The leader validates the tag
  (no space, `/`, `\`, `.`/`..`, at most 128 bytes) and the name
  (`forge.assetNameRe`), requires a matching `release_assets` row for that
  repository before touching the filesystem, and only then opens the
  record's path; a file on disk without a row is never served (404). The
  client refuses records larger than 1 GiB, requires `Content-Length` equal
  to the record's size and `X-Forge-SHA256` equal to its hash, streams into
  a temporary file under `<data>/tmp` (same filesystem), verifies length and
  sha256, sets mode 0640 and renames into place. A partial or wrong
  download never replaces a file.
- Files under `<assets>/<owner>/<repo>/` that no row names are removed, and
  tag directories left empty are deleted (bounded to that one repository
  directory, two fixed levels, symlinks are not followed).
- A per-file failure (leader has no file, hash mismatch, transfer error) is
  recorded in `repo_replicas.detail` as `assets: <tag>/<name>: ...` with
  status `error`; the other files of the repository are still processed,
  the git refs and metadata rows already applied stay, and the next cycle
  retries. `/v1/repos/{id}/state` therefore reports `error` while any
  asset is missing, and `move-leader` refuses until the target has them.

Note that `forge.RemoveAsset` and `forge.DeleteRelease` neither stamp the
repository's `updated_at` nor append an event, so replicas notice a
removal only at the next change to that repository (or `resync`); until
then the replica keeps serving the stale file. `AddAsset` emits an event,
so additions replicate promptly.

### Per-origin cursors

Event ids are per-node autoincrement, so "how far am I" only makes sense per
origin. `repl_cursors(node, last_origin_id)` holds one cursor per peer;
`events.origin_id` keeps the origin id on every replicated row. Feeds and
the activity log query the `events` table as before and therefore show local
and replicated events together, ordered by local insertion (arrival order
across origins, id order within one origin).

## What replicates, what does not

| Data | Owner | Mechanism |
| --- | --- | --- |
| Git refs and objects | repository leader | smart-HTTP fetch, mirror of `refs/heads/*` and `refs/tags/*` |
| Repository records (incl. soft deletes, leader) | repository leader | full-row upsert from `/v1/repos` |
| Collaborators, issues, changes, change versions, comments, reviews, releases, release assets | repository leader | whole-repository snapshot, applied atomically (delete + insert by leader ids) |
| Events | authoring node | append-only pull with per-origin cursor |
| Users, certificates, SSH keys | metadata leader | snapshot upsert every cycle |
| Release asset **files** | repository leader | per-file fetch over the control plane, verified against the row's size and sha256; files without a row removed (see Release asset files) |
| Tokens (enrolment codes), settings, `nodes`, `repo_replicas` | local | never replicated |

## Consistency

- **Eventual and monotone.** A replica converges to the leader's state
  within one sync interval plus transfer time under normal conditions; it
  never moves backwards because every step is keyed by leader-assigned ids
  and applied in leader order (events) or as a whole snapshot (rows).
- **Idempotent.** Re-running any step is harmless: event insertion ignores
  duplicates, record and row upserts are keyed by id, git fetch is a mirror
  update, cursors only advance. An interrupted fetch leaves refs untouched
  (git updates refs only after a verified pack) and the next cycle resumes.
- **Last writer wins by the leader.** There is exactly one writer per row
  set (the repository leader for repository data, the metadata leader for
  identities), so conflicts cannot arise; `updated_at` is copied, not
  compared. Local edits of replicated rows are lost at the next sync by
  design.
- **Cross-table ordering.** Within one cycle records precede events and
  identities precede everything, so references resolve; an event whose
  repository or user is nevertheless unknown (metadata leader down, race
  with a purge) is stored with a NULL reference rather than blocking the log.
  A metadata snapshot whose author is not yet known locally fails as a
  whole and is retried next cycle.
- **Timestamps have one-second resolution** (`store.Now`). The change checks
  therefore also key on events and notifies; two pushes inside the same
  second are still fetched because each push emits an event.

## Failure modes

| Situation | Effect | Recovery |
| --- | --- | --- |
| Repository leader down | reads served from every replica; pushes to replicas are refused with the leader's name; Titan writes on replicas fail with "not the leader" (or forwarding error) | wait, or `move-leader` from the leader once it is back; automatic failover is not in v1 |
| Metadata leader down | registrations and key changes fail everywhere else; existing identities keep working from local copies | wait; there is no metadata leader election in v1 |
| Replica behind | stale reads on that POP; `forge_replica_lag_events{leader}` > 0, `repo_replicas.status` = `error` with the cause in `detail` | it retries every interval; `forge admin repl status` shows it, `resync` forces a full pass |
| Sync interrupted (restart, timeout) | partial cycle; nothing corrupt: cursors and refs only move after success | next cycle repeats the same work |
| Peer serves events at or below our cursor (rebuilt node with the same name) | that peer's sync is aborted with an error; nothing applied | rebuild replicas from the leader, or reset the cursor row for that origin |
| Wrong secret or unknown node name | 401 / 403, logged on the server as `control auth failed` | fix `cluster.peers` / `secret_file` |
| Leader's repository files missing | fetch fails, replica row `error` | restore the leader's repository; replicas keep their last good copy |
| Leader's asset file missing or its size differs from the row | leader answers 404 / 500 for that asset; replica row `error` with `assets: <tag>/<name>: ...`; other assets, refs and rows of the repository are still synced | restore the file on the leader (or remove the asset); the replica retries every cycle |
| Asset download interrupted, short, or hash mismatch | temporary file discarded, existing local copy untouched, row `error` | retried next cycle; persistent mismatch means the leader's file no longer matches its record |
| Asset record larger than 1 GiB | refused before any transfer, row `error` | the size bound is `repl.maxAssetBytes`; split the file or raise the bound |
| Replica's asset copy truncated or deleted | repaired by the next cycle (existence and size are checked every cycle) | nothing to do |
| Replica's asset copy corrupted at the same length | served stale until the next metadata change or `forge admin repl resync` | run `resync` |

Replicas keep serving their last good copy through all of the above.

## Operator procedures

All run against the local database of the node you are on; `move-leader`
and `resync` also talk to peers over the control plane.

- `forge admin repl status` (`repl.Status`): one line per `(repository,
  node)` with status, detail, last sync time, and the cursor into the
  leader's log. Compare with `forge_replica_lag_events` in Prometheus.
- `forge admin repl resync <repo id>` (`Node.Resync`): on a replica, marks
  the row `resync` and immediately fetches refs and re-applies the metadata
  snapshot from the leader. Refused on the leader.
- `forge admin repl move-leader <repo id> <node>` (`Node.MoveLeader`):
  **run on the current leader.** It refuses unless the target's refs equal
  the leader's and the target reports `replica_status = ok` for the
  leader's current `updated_at`; then it sets `leader_node`, appends an
  admin event and notifies peers. The change propagates because every
  replica accepts a record from the node it currently records as leader
  (the old one); the new leader starts accepting writes as soon as it has
  pulled the record (one interval, or on notify). Drain writes first: a
  push that lands between the ref comparison and the switch would stay on
  the old leader and be overwritten by the new leader's first push. If it
  fails with "not fully synced", run `resync` on the target and retry.
- Adding a node: give it the secret and a `cluster.peers` entry on every
  node (and every node in its own list), start it; its worker pulls every
  peer's repositories and history from scratch. Leadership of existing
  repositories does not change.
- Removing a node: move leadership of everything it leads, remove it from
  every peer list, then delete its `nodes` and `repo_replicas` rows if you
  care about tidy status output.

## Forwarding

A Titan write for a repository this node does not lead can be forwarded
(`repl.Forwarder`, implemented by `*repl.Node`): the replica posts the
original request (path, mime, body, the client's certificate DER) to the
leader's `/v1/forward`; the leader re-derives the identity from the
certificate, re-runs authorisation and the write through its normal Titan
handler (`repl.Handler`, implemented in `internal/web`), and returns the
Gemini status and meta line, which the replica relays unchanged. The leader
logs the originating node. A compromised replica can therefore only replay
what a real client sent it (threat model T-37). Push forwarding is not in
v1: `git push` to a replica is refused with a message naming the leader.

## Future work

- Push forwarding (proxy the SSH exec stream to the leader) or a
  leader-specific push hostname.
- Automatic failover with a signed leader manifest per repository (T-34,
  T-38) and verification of fetched refs against it.
- Metadata leader election, or a proper global metadata store, so that
  registrations survive the loss of one node.
- Per-message signatures with nonces on the control plane (T-41).
- Incremental metadata (row-level changes carried in events) instead of
  whole-repository snapshots, once repositories are large enough for the
  snapshot to matter.
- Stamp `updated_at` / emit an event on asset and release removal so that
  replicas drop the files promptly; remove asset files of soft-deleted
  repositories on replicas.
- Cursor reset and node rebuild tooling.
