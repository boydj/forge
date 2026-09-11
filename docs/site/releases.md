# Releases

1. Push a tag: `git tag v1.0 && git push origin v1.0`.
2. At `/~user/repo/releases/`, follow *publish a release* and upload the
   notes: first line the tag name, second line the title, then the notes.

The release page links the tagged commit and the files at that tag. To
attach a file, upload it with Titan to
`titan://git.as215520.net/~user/repo/releases/v1.0/assets/<filename>`
with its type (for example `;mime=application/gzip`); it is then
downloadable from the release page. Files up to 64 MiB.

Follow releases at `/~user/repo/releases/feed`.
