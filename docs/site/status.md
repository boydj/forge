# Service status

`/status/` shows whether the forge is healthy: one line per point of
presence, each component, and the most recent incident reports. Subscribe
to incidents at `/status/feed`; maintenance and key rotations are
announced there.

The service is anycast: you reach the nearest point of presence and all
of them serve everything.

## Fingerprints to trust

The same on every point of presence; they change only by announced
rotation.

TLS certificate (`gemini://` and `titan://`, port 1965), valid to
2035-12-31:

```
2A:B6:07:B3:E0:1A:86:44:B2:CC:E5:09:6B:27:E6:EA:3C:45:71:B9:A2:CC:ED:EF:6C:67:FE:35:AA:52:24:6C
```

SSH host key (git, port 22, ed25519; also published as an SSHFP record):

```
SHA256:0pNfPYIMlNWpteYvSZp5AHaXiLtnl8roTxOzsUTrJOg
```

If your client reports a mismatch, do not accept it; check the incident
feed.
