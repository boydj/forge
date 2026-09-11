# Feeds

Everything that happens on the forge is published as gemfeeds, the
subscription format of Gemini (a gemtext page whose dated links are the
entries), each with an Atom twin for readers that only speak Atom.
Subscribe in your client (Lagrange: Bookmarks, Subscribe to page) or point
a feed reader at the Atom URL.

| What | Gemfeed | Atom |
| --- | --- | --- |
| The whole forge | `/feed` | `/atom.xml` |
| A user | `/~user/feed` | `/~user/atom.xml` |
| A repository | `/~user/repo/feed` | `/~user/repo/atom.xml` |
| Its issues | `/~user/repo/issues/feed` | `/~user/repo/issues/atom.xml` |
| Its changes | `/~user/repo/changes/feed` | `/~user/repo/changes/atom.xml` |
| One change | `/~user/repo/changes/<n>/feed` | |
| Its releases | `/~user/repo/releases/feed` | `/~user/repo/releases/atom.xml` |
| Service incidents | `/status/feed` | `/status/atom.xml` |

Entries read as sentences ("alice pushed 2 commits to main in alice/proj:
...", "bob opened issue #1: ...") and link to the page concerned. Feeds
show the newest 60 events. Events of private repositories appear only for
viewers who can see the repository, so subscribe with your certificate
selected if you want them.

Gemfeed readers collapse several events on the same page (an issue opened,
commented and closed) into one entry showing the newest; the Atom twin
keeps them separate.
