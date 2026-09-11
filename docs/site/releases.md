# Releases

A release is a tag with notes and, optionally, downloadable files
(assets). Releases are listed at `/~user/repo/releases/` with a feed;
publishing needs write access.

## Publish

1. Push the tag: `git tag v1.0 && git push origin v1.0`.
2. Upload the notes with Titan to
   `titan://git.as215520.net/~user/repo/releases/new`. First line: the tag
   name. Second line: the title. Remaining lines: the notes (gemtext or
   plain text, up to 256 KiB).

The release page `/~user/repo/releases/v1.0` links the tagged commit and
the files at that tag. Change title and notes by uploading to the release
page itself (`titan://.../releases/v1.0`, same layout without the tag
line).

## Assets

Upload a file with Titan to
`titan://git.as215520.net/~user/repo/releases/v1.0/assets/<name>`, with its
MIME type (`;mime=application/gzip` for a tarball, for instance). One
asset is at most 64 MiB. Downloads are the same path over Gemini, served
with the type you gave. *delete <name>* on the release page removes an
asset after you type `delete`; *delete this release* removes the release
and its assets (the tag stays).

## Following releases

`/~user/repo/releases/feed` (gemfeed) and `/~user/repo/releases/atom.xml`
list each release as it is published. See [Feeds](feeds.md).
