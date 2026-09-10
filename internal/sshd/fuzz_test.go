package sshd

import (
	"strings"
	"testing"
)

// FuzzParseCommand checks that the exec-line grammar never panics, that an
// accepted command yields exactly one validated owner and repository name
// (I-2, I-10), and that accepted names round-trip through the parser.
func FuzzParseCommand(f *testing.F) {
	for _, s := range []string{
		"git-upload-pack 'alice/repo.git'",
		"git-receive-pack '~alice/repo'",
		"git upload-pack /alice/repo",
		"git receive-pack alice/repo.git",
		"git-upload-pack 'alice/../etc'",
		"git-upload-pack '../alice/repo'",
		"git-upload-pack '~/alice/repo'",
		"git-upload-pack 'alice/repo.git.git'",
		"git-upload-pack '-alice/repo'",
		"git-upload-pack '--help'",
		"git-upload-pack 'alice/repo'; id",
		"git-upload-pack \"alice/repo\"",
		"git-upload-pack 'alice/repo' extra",
		"git-upload-archive 'alice/repo'",
		"git-upload-pack 'alice/repo.lock'",
		"git-upload-pack 'Alice/Repo'",
		"git-upload-pack ''",
		"git-upload-pack '",
		"",
		strings.Repeat("a", 300),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		cmd, err := ParseCommand(line)
		if err != nil {
			if cmd.Owner != "" || cmd.Repo != "" {
				t.Fatalf("error with non-empty command: %+v", cmd)
			}
			return
		}
		if cmd.Op != OpRead && cmd.Op != OpWrite {
			t.Fatalf("bad op %v", cmd.Op)
		}
		for _, name := range []string{cmd.Owner, cmd.Repo} {
			if name == "" || strings.ContainsAny(name, "/\\ '\"\x00~") || strings.Contains(name, "..") ||
				strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") {
				t.Fatalf("accepted unsafe name %q from %q", name, line)
			}
		}
		if !ownerRe.MatchString(cmd.Owner) || !repoRe.MatchString(cmd.Repo) || strings.HasSuffix(cmd.Repo, ".git") {
			t.Fatalf("accepted name outside grammar: %+v from %q", cmd, line)
		}
		if len(line) > maxCommandLen {
			t.Fatalf("accepted over-long line")
		}
		for _, form := range []string{
			"git-upload-pack '" + cmd.Path() + ".git'",
			"git-receive-pack " + cmd.Path(),
			"git upload-pack '~" + cmd.Path() + "'",
		} {
			again, err := ParseCommand(form)
			if err != nil || again.Owner != cmd.Owner || again.Repo != cmd.Repo {
				t.Fatalf("round trip %q failed: %+v %v", form, again, err)
			}
		}
	})
}
