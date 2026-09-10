package gemini

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Fuzz targets for the wire-facing parsers and the gemtext builder. They
// check crash-freedom plus the invariants the security review relies on:
// a parsed path never contains traversal, META never carries a control
// character, and text passed through Page.Text can never start a gemtext
// line type. See docs/security-review.md.

func FuzzParseRequestLine(f *testing.F) {
	for _, s := range []string{
		"gemini://example.org/",
		"gemini://example.org/~alice/repo/tree/main/a%20b",
		"gemini://example.org/../x",
		"gemini://example.org/%2e%2e/x",
		"gemini://example.org/a/..",
		"gemini://example.org/a//b",
		"gemini://example.org/a/%00",
		"gemini://example.org/a%0d%0ab",
		"gemini://example.org/x?q=%3F",
		"titan://example.org/~a/r/issues/new;size=10;mime=text/plain",
		"titan://example.org/~a/r/issues/1/edit;edit",
		"titan://example.org/~a/r/x;size=1;size=2",
		"titan://example.org/~a/r/x;token=abc;size=0",
		"titan://example.org/~a/r/x%3Bsize=1;size=1",
		"titan://example.org/x;size=99999999999999999999",
		"titan://example.org/x;size=1;edit",
		"TITAN://example.org/x;size=1;mime=text%2Fgemini",
		"gemini://user@example.org/",
		"gemini://example.org/#frag",
		"gemini://example.org/\x7f",
		"http://example.org/",
		"gemini://[::1]:1965/",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		u, tp, err := parseRequestLine(line)
		if err != nil {
			if u != nil || tp != nil {
				t.Fatalf("error with non-nil result: %v", err)
			}
			return
		}
		if u == nil {
			t.Fatal("nil URL without error")
		}
		for _, c := range line {
			if c < 0x20 || c == 0x7f {
				t.Fatalf("accepted control character %q", line)
			}
		}
		if len(line) > MaxTitanRequestBytes || (u.Scheme == "gemini" && len(line) > MaxRequestBytes) {
			t.Fatalf("accepted over-long line (%d bytes)", len(line))
		}
		if u.Scheme != "gemini" && u.Scheme != "titan" {
			t.Fatalf("accepted scheme %q", u.Scheme)
		}
		if u.Host == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
			t.Fatalf("accepted authority/fragment: %q", line)
		}
		p := u.Path
		if strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") || strings.Contains(p, "//") || strings.ContainsAny(p, "\x00\r\n") {
			t.Fatalf("accepted traversal-looking path %q from %q", p, line)
		}
		if (u.Scheme == "titan") != (tp != nil) {
			t.Fatalf("titan params mismatch: scheme=%s params=%v", u.Scheme, tp)
		}
		if tp != nil {
			if tp.Size < 0 || tp.Size >= 1_000_000_000_000_000 {
				t.Fatalf("size out of range: %d", tp.Size)
			}
			if tp.Edit && tp.Size != 0 {
				t.Fatalf("edit with size: %+v", tp)
			}
			if tp.MIME == "" || len(tp.MIME) > 255 || len(tp.Token) > 255 {
				t.Fatalf("bad params: %+v", tp)
			}
			if strings.Contains(p, ";") {
				t.Fatalf("titan path still carries parameters: %q", p)
			}
		}
	})
}

func FuzzSanitizeMeta(f *testing.F) {
	for _, s := range []string{
		"text/gemini; charset=utf-8",
		"a\r\nb",
		"x\x00y",
		strings.Repeat("é", 700),
		strings.Repeat("a", MaxMetaBytes+10),
		"\xff\xfe",
		"tab\there",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, meta string) {
		out := SanitizeMeta(meta)
		if len(out) > MaxMetaBytes {
			t.Fatalf("META too long: %d", len(out))
		}
		for i := 0; i < len(out); i++ {
			if out[i] < 0x20 || out[i] == 0x7f {
				t.Fatalf("control byte %#x in META %q", out[i], out)
			}
		}
		if !utf8.ValidString(out) {
			t.Fatalf("META is not valid UTF-8: %q", out)
		}
		if again := SanitizeMeta(out); again != out {
			t.Fatalf("not idempotent: %q -> %q", out, again)
		}
	})
}

// prefixes are the gemtext line types user text must never start.
var prefixes = []string{"=>", "#", "* ", ">", "```"}

func FuzzPage(f *testing.F) {
	for _, s := range []string{
		"plain", "=> gemini://evil/ click", "# heading", "* item", "> quote", "```", "a\r\nb\n```\nc", "\r```", " =>", "",
	} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		var p Page
		p.Text(a)
		for _, l := range strings.Split(strings.TrimSuffix(p.String(), "\n"), "\n") {
			for _, pre := range prefixes {
				if strings.HasPrefix(l, pre) {
					t.Fatalf("Text produced line type %q: %q", pre, l)
				}
			}
		}
		if e := EscapeLine(a); EscapeLine(e) != e {
			t.Fatalf("EscapeLine not idempotent on %q", a)
		}

		p = Page{}
		p.Pre(a, b)
		lines := strings.Split(strings.TrimSuffix(p.String(), "\n"), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "```") || lines[len(lines)-1] != "```" {
			t.Fatalf("Pre framing broken: %q", p.String())
		}
		fences := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "```") {
				fences++
			}
		}
		if fences != 2 {
			t.Fatalf("Pre body toggled the fence (%d fence lines): %q", fences, p.String())
		}

		p = Page{}
		p.Link(a, b)
		if s := p.String(); strings.Count(s, "\n") != 1 || !strings.HasPrefix(s, "=> ") {
			t.Fatalf("Link is not one link line: %q", s)
		}
		p = Page{}
		p.Heading(2, a)
		if s := p.String(); strings.Count(s, "\n") != 1 || !strings.HasPrefix(s, "## ") {
			t.Fatalf("Heading is not one heading line: %q", s)
		}
		p = Page{}
		p.Item(a)
		if s := p.String(); strings.Count(s, "\n") != 1 || !strings.HasPrefix(s, "* ") {
			t.Fatalf("Item is not one item line: %q", s)
		}
		p = Page{}
		p.Quote(a)
		for _, l := range strings.Split(strings.TrimSuffix(p.String(), "\n"), "\n") {
			if !strings.HasPrefix(l, "> ") {
				t.Fatalf("Quote line without prefix: %q", l)
			}
		}
	})
}
