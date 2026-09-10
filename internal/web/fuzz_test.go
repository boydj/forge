package web

import (
	"os"
	"strings"
	"testing"

	"as215520.net/forge/internal/store"
)

// strict enables invariants that document open findings in
// docs/security-review.md; set FORGE_FUZZ_STRICT=1 after applying the
// corresponding patches to make them part of the fuzz contract.
var strict = os.Getenv("FORGE_FUZZ_STRICT") == "1"

// fenceStats walks gemtext and reports whether fences are balanced plus
// every non-preformatted line, for line-type checks.
func fenceStats(text string) (balanced bool, lines []string) {
	in := false
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.HasPrefix(l, "```") {
			in = !in
			continue
		}
		if !in {
			lines = append(lines, l)
		}
	}
	return !in, lines
}

var mdSeeds = []string{
	"# Title\n\nSome *text* with a [link](gemini://example.org/) and ![img](a.png).\n",
	"```go\nfmt.Println(\"```\")\n```\n",
	"    ```\n",
	"    code\n\tmore\n",
	"[x](#top) [y](javascript:alert(1)) [z](/account/keys/add?ssh-ed25519%20AAAA)\n",
	"> quote\n- item\n1. one\n---\n#### deep\n",
	"~~~\nfence\n```\n",
	"=> gemini://evil/ not a link in markdown\n",
	"[](a)\n",
	"**bold** __x__ `code`\n",
	strings.Repeat("[a](b)", 500),
	strings.Repeat("*", 5000),
	"",
}

func FuzzMarkdownToGemtext(f *testing.F) {
	for _, s := range mdSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		out := MarkdownToGemtext(md, "/~o/r/tree/main/")
		balanced, lines := fenceStats(out)
		if strict && !balanced {
			// SR-12: an indented code line that starts with ``` emits an
			// unbalanced fence and swallows the rest of the page.
			t.Fatalf("unbalanced fences for %q:\n%s", md, out)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "#") && !(strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "## ") || strings.HasPrefix(l, "### ")) {
				t.Fatalf("heading deeper than 3 or malformed: %q", l)
			}
			if strings.HasPrefix(l, "=>") {
				rest := strings.TrimPrefix(l, "=> ")
				target, _, _ := strings.Cut(rest, " ")
				for _, r := range target {
					if r < 0x21 || r == 0x7f {
						t.Fatalf("whitespace/control in link target: %q", l)
					}
				}
				if strict && target == "" {
					// SR-13: links with a dropped target render "=>  label".
					t.Fatalf("empty link target: %q", l)
				}
				if strings.HasPrefix(strings.ToLower(target), "javascript:") || strings.HasPrefix(strings.ToLower(target), "data:") {
					t.Fatalf("dangerous scheme survived: %q", l)
				}
			}
		}
		if len(out) > 32*len(md)+64 {
			t.Fatalf("output blow-up: %d bytes from %d", len(out), len(md))
		}
	})
}

func FuzzUserGemtext(f *testing.F) {
	for _, s := range []string{
		"hello\n=> gemini://example.org/ link\n# heading\n```\n=> inside\n```\n",
		"=> /account/keys/add?ssh-ed25519%20AAAA click\n",
		"=> titan://example.org/x up\n",
		"=>javascript:x\n",
		"=> \n",
		"```\nunterminated",
		"###### deep\n#\n",
		"* item\n> quote\n",
		"\r\n=> gemini://a/ b\r\n",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		out := userGemtext(text)
		balanced, lines := fenceStats(out)
		if !balanced {
			t.Fatalf("unbalanced fences:\n%s", out)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "=>") && !strings.HasSuffix(l, " [user link]") {
				t.Fatalf("unmarked link line: %q", l)
			}
			if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "### ") {
				t.Fatalf("page-level heading from user text: %q", l)
			}
			if strings.HasPrefix(l, "=> ") {
				target, _, _ := strings.Cut(strings.TrimPrefix(l, "=> "), " ")
				for _, r := range target {
					if r < 0x21 || r == 0x7f {
						t.Fatalf("whitespace/control in link target: %q", l)
					}
				}
			}
		}
		if len(out) > 2*len(text)+64 && len(text) > 0 {
			// Each line gains at most a prefix, " [user link]" and a fence.
			if len(out) > 16*len(text)+64 {
				t.Fatalf("output blow-up: %d bytes from %d", len(out), len(text))
			}
		}
	})
}

func FuzzRenderAnchored(f *testing.F) {
	for _, s := range []string{
		"@ src/main.go:12\nlooks wrong\n",
		"@ ../../../../account/keys/add?ssh-ed25519%20AAAA:1 v1\n",
		"@ a?b#c v2\n",
		"@ x:1 v99999999999999999999\n",
		"```\n@ inside:1\n```\n",
		"@ \n",
		"",
	} {
		f.Add(s)
	}
	h := &Handler{}
	rc := &repoCtx{base: "/~o/r"}
	ch := &store.Change{Number: 7, Version: 2}
	f.Fuzz(func(t *testing.T, body string) {
		out := h.renderAnchored(rc, ch, body, 1)
		balanced, lines := fenceStats(out)
		if !balanced {
			t.Fatalf("unbalanced fences:\n%s", out)
		}
		for _, l := range lines {
			if !strings.HasPrefix(l, "=>") {
				continue
			}
			if strings.HasSuffix(l, " [user link]") {
				continue
			}
			target, _, _ := strings.Cut(strings.TrimPrefix(l, "=> "), " ")
			if !strings.HasPrefix(target, "/~o/r/changes/7/v") {
				t.Fatalf("anchor link outside the change: %q", l)
			}
			if strict {
				// SR-06: the anchor path is not validated or escaped, so it
				// can climb out of the change URL or carry a query.
				for _, seg := range strings.Split(target, "/") {
					if seg == ".." || seg == "." {
						t.Fatalf("anchor link climbs out of the change URL: %q", l)
					}
				}
				if strings.ContainsAny(target, "?#%") {
					t.Fatalf("anchor link carries query/fragment: %q", l)
				}
			}
		}
	})
}

func FuzzRebaseGemtextLinks(f *testing.F) {
	f.Add("=> a.gmi label\n=> gemini://x/ y\n=> #frag\n=>\n=> javascript:x y\nplain\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, gmi string) {
		out := rebaseGemtextLinks(gmi, "/~o/r/tree/main/")
		in := strings.ReplaceAll(gmi, "\r\n", "\n")
		if strings.Count(out, "\n") != strings.Count(in, "\n") {
			t.Fatalf("line count changed: %d -> %d", strings.Count(in, "\n"), strings.Count(out, "\n"))
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "=>") {
				target, _, _ := strings.Cut(strings.TrimPrefix(l, "=> "), " ")
				if target == "" || strings.HasPrefix(strings.ToLower(target), "javascript:") {
					t.Fatalf("bad rebased link: %q", l)
				}
			}
		}
	})
}
