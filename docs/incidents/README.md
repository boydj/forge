# Incidents

Incident reports live here, one file per incident, and feed the status
page. The status page at `/status/` lists the ten newest; `/status/feed`
is a gemfeed and `/status/atom.xml` an Atom feed of all of them, so anyone
can subscribe to incidents the way they subscribe to a repository. Writing
an incident is a git push: there is no other system to update.

## File format

Name the file `YYYY-MM-DD-slug.md` (gemtext `.gmi` works too). The date is
when the incident started; the slug is a few words in kebab case. Files
that do not match that pattern, this README included, are not incidents.

The first heading in the file is the title shown on the status page and in
the feeds; keep it short and factual ("Leader failover", "Disk full on
sgp1"). The body is free-form. A useful shape:

```markdown
# Leader failover

**Status:** resolved
**Started:** 2026-09-11 14:02 UTC
**Resolved:** 2026-09-11 14:19 UTC
**Impact:** pushes and Titan writes failed for 17 minutes; reads unaffected.

## Timeline

- 14:02 ewr1 health drains: replication check failing.
- ...

## Cause

## What changes
```

Update the file as the incident evolves; the page always shows the current
text, and the status page banner is computed live from the fleet, not from
these files. The runbook for the human side is
`runbooks/incident-checklist.md`.
