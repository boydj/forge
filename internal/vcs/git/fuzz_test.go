package git

import (
	"os"
	"path"
	"strings"
	"testing"
)

// strict enables invariants that document open findings in
// docs/security-review.md; set FORGE_FUZZ_STRICT=1 after applying the
// corresponding patches to make them part of the fuzz contract.
var strict = os.Getenv("FORGE_FUZZ_STRICT") == "1"

// FuzzCheckPath: an accepted tree path is relative, canonical and has no
// traversal component, so it can be appended to a "<rev>:" spec or passed
// after "--" (I-19, T-23).
func FuzzCheckPath(f *testing.F) {
	for _, s := range []string{
		"", "a", "a/b", "a/../b", "../a", "..", "/a", "-a", "a/", "a//b", "./a", "a/.", "a\x00b", "a\nb", ".git/config",
		"a\\..\\b", "a/./b", "...", ".a", "a b", strings.Repeat("a/", 100),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		clean, err := checkPath(p)
		if err != nil {
			if clean != "" {
				t.Fatalf("error with value %q", clean)
			}
			return
		}
		if clean == "" {
			return
		}
		if strings.ContainsRune(clean, 0) || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "-") {
			t.Fatalf("accepted %q -> %q", p, clean)
		}
		for _, seg := range strings.Split(clean, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("accepted %q -> %q with segment %q", p, clean, seg)
			}
		}
		if path.Clean(clean) != clean {
			t.Fatalf("non-canonical result %q from %q", clean, p)
		}
		if strict {
			for _, r := range clean {
				if r < 0x20 || r == 0x7f {
					// SR-08: a newline in a path reaches `git cat-file --batch`
					// as a second request line.
					t.Fatalf("accepted control character in %q", p)
				}
			}
		}
	})
}

// FuzzCheckRefName: an accepted ref or revision name cannot be mistaken for
// an option, a range, a reflog selector or a pathspec by git.
func FuzzCheckRefName(f *testing.F) {
	for _, s := range []string{
		"main", "refs/heads/main", "HEAD", "-x", "--all", "a..b", "a...b", "a^{tree}", "a~1", "a@{1}", "@", "a:b",
		"a b", "a.lock", "a.", "/a", "a/", "a//b", "a\\b", "a*", "a?", "a[b]", "a\x7f", "a\nb", ".a", "a/.b",
		"0123456789012345678901234567890123456789", "", strings.Repeat("a", 1025),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if err := checkRefName(name); err != nil {
			return
		}
		switch {
		case name == "" || len(name) > 1024, name == "@",
			strings.HasPrefix(name, "-"), strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"),
			strings.Contains(name, ".."), strings.Contains(name, "@{"), strings.Contains(name, "//"),
			strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, "."),
			strings.ContainsAny(name, " ~^:?*[\\\x00\n\r\t"):
			t.Fatalf("accepted %q", name)
		}
		for _, r := range name {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("accepted control character in %q", name)
			}
		}
		// A branch name is a ref name that is not already fully qualified.
		if err := checkBranchName(name); err == nil && (strings.HasPrefix(name, "refs/") || len(name) > 255) {
			t.Fatalf("checkBranchName accepted %q", name)
		}
	})
}
