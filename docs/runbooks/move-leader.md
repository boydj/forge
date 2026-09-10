# Move repository leadership to another node

**Purpose**: transfer the single writer role for one repository from its
current leader to a replica (planned migration, or before decommissioning
a node). Reads are served everywhere; only the leader accepts pushes and
Titan writes.

**Preconditions**: `cluster.enabled = true` on both nodes, they are peers
of each other, the target reports the repository as `ok` in
`forge admin pop status`, and **you are on the current leader** (the
command refuses elsewhere). If the leader is dead, see `rebuild-pop.md`
("leader lost"): `move-leader` cannot run without it in v1.

**Duration**: 1-2 min per repository; writes unavailable for that window.

**Triggers**: node decommission, rebalancing, planned maintenance longer
than users tolerate, `rebuild-pop.md`.

## Steps

```sh
# on the current leader #
sudo -u forge forge admin repo list | grep '<owner>/<name>'          # confirm LEADER column = this node
sudo -u forge forge admin pop status | grep '<owner>/<name>'         # target row: STATUS ok, recent LAST SYNC

# 1. stop writes landing on the old leader during the switch.
#    There is no per-repository write freeze; the practical options are:
#    a) tell the owner not to push for two minutes, or
#    b) take the whole POP out of rotation (drain-pop.md) so anycast traffic goes elsewhere.
#       Pushes to a non-leader are refused with the leader's name, so nothing is silently lost.

# 2. make sure the target is fully caught up (idempotent, safe to repeat)
#    on the TARGET:   sudo -u forge forge admin repo resync <owner>/<name>

# 3. move (run on the LEADER)
sudo -u forge forge admin repo move-leader <owner>/<name> <target-node>
```

`move-leader` compares refs on both sides and the target's replica status
for the leader's current `updated_at`; on mismatch it returns
`not fully synced`: run `repo resync` on the target and retry. On success
it sets `leader_node`, appends an admin event, and notifies peers; the new
leader accepts writes as soon as it has pulled the record (one
`sync_interval`, 10 s, or immediately on notify).

## Verify

```sh
# on both nodes, within ~10 s
sudo -u forge forge admin repo list | grep '<owner>/<name>'          # LEADER = target on both
# from a client: a push to git.<zone> lands on the new leader; a push that hits the old one is refused with
#   "not the leader of this repository; leader is <target>"
sudo -u forge forge admin pop status | grep '<owner>/<name>'         # old leader now has a replica row for it
```

## Rollback

Run the same command on the new leader in the other direction:

```sh
sudo -u forge forge admin repo move-leader <owner>/<name> <old-node>
```

## Notes

- Global metadata (users, certificates, SSH keys) has its own leader
  (`cluster.metadata_leader`, default alphabetically first node); it is
  not moved per repository and there is no command to move it. Changing it
  means editing `forge.toml` on every node and restarting. **PLANNED**.
- Release asset files are not replicated in v1; move them by hand
  (`<data_dir>/assets/<owner>/<repo>/`) if the repository has releases.
