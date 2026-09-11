# Service status

`/status/` shows the service as the node you reached sees it: a one-line
verdict (all systems operational, degraded, major outage), each component
(Gemini and Titan, Git over SSH, the metadata database, storage,
replication), how many points of presence announce the anycast service,
and one line per point of presence with its state, health, replication lag
and uptime. It is computed live from the nodes themselves.

Below it are the most recent **incident reports**, written by the
operator, with `/status/feed` (gemfeed) and `/status/atom.xml` (Atom) to
subscribe to them. Planned maintenance and key rotations are announced
there too.

`/status` (no trailing slash) is the terse health probe of one node: `ok`
with its name, version and uptime, or an error status when it is
unhealthy.

## What to pin

Both server identities are the same on every point of presence and change
only by announced rotation.

**TLS certificate** for `gemini://` and `titan://` on port 1965, valid to
2035-12-31:

```
SHA-256 of the certificate:
2A:B6:07:B3:E0:1A:86:44:B2:CC:E5:09:6B:27:E6:EA:3C:45:71:B9:A2:CC:ED:EF:6C:67:FE:35:AA:52:24:6C
SHA-256 of the public key (what Lagrange and Amfora pin):
44a32527595eb183fcacf15e34954dd6e028021974b33f0b0d7d0a6e1c7921ee
```

**SSH host key** for git on port 22 (ed25519), also in DNS as an SSHFP
record:

```
SHA256:0pNfPYIMlNWpteYvSZp5AHaXiLtnl8roTxOzsUTrJOg
```

If your client reports a mismatch with these values, do not accept it;
check the status page from another network and the incident feed.

## Reaching one point of presence

`git.as215520.net` is anycast: you talk to the nearest point of presence
and every one of them serves everything. Each node is also reachable by
its own name, `<pop>.nodes.as215520.net`, which is useful when you want to
compare what two nodes show.
