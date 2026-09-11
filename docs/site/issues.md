# Issues

Every repository has an issue tracker at `/~user/repo/issues/`: open
issues, a link to closed ones, a feed, and *open a new issue*. Anyone who
can read the repository can open issues and comment; the author of an
issue or comment and the repository's writers can edit and close.

## Open an issue

Upload text with Titan to `titan://git.as215520.net/~user/repo/issues/new`
(see [Titan uploads](titan.md)). The first non-empty line is the title (a
leading `#` is stripped; at most 200 characters), the rest is the body,
plain text or gemtext, up to 256 KiB. The forge answers with the new
issue's page, `/~user/repo/issues/<n>`.

In Lagrange: open the *open a new issue* link, then Page menu, Upload;
type the text in the dialog with your identity selected and send.

## Comment

Upload to `titan://git.as215520.net/~user/repo/issues/<n>/comment`; the
whole body is the comment.

## Edit

`titan://git.as215520.net/~user/repo/issues/<n>/edit` replaces title and
body (same layout as when opening); a comment is edited at
`.../issues/<n>/comments/<id>/edit`. Lagrange's *Edit page with Titan*
fetches the current text into the editor first (Titan `;edit`), so you
change what is there rather than retype it.

## Close, reopen, delete a comment

These are confirmed at a prompt rather than uploaded: follow *close this
issue* (or *reopen this issue*) and type `close` or `reopen`, optionally
followed by a comment on the same line. *delete this comment* asks you to
type `delete`.

## Following issues

`/~user/repo/issues/feed` is a gemfeed of issues opened, closed, reopened
and commented; `/~user/repo/issues/atom.xml` is the same as Atom. See
[Feeds](feeds.md).
