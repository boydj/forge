package gemini

import (
	"strings"
	"testing"
)

func TestParseRequestLine(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		scheme string
		path   string
		size   int64
		mime   string
		token  string
	}{
		{"gemini://example.org/", true, "gemini", "/", 0, "", ""},
		{"gemini://example.org", true, "gemini", "", 0, "", ""},
		{"gemini://example.org/~alice/repo/tree/main/src?x=1", true, "gemini", "/~alice/repo/tree/main/src", 0, "", ""},
		{"GEMINI://example.org/", true, "gemini", "/", 0, "", ""},
		{"gemini://example.org/a/../b", false, "", "", 0, "", ""},
		{"gemini://example.org/a//b", false, "", "", 0, "", ""},
		{"gemini://user@example.org/", false, "", "", 0, "", ""},
		{"gemini://example.org/#frag", false, "", "", 0, "", ""},
		{"http://example.org/", false, "", "", 0, "", ""},
		{"/relative", false, "", "", 0, "", ""},
		{"gemini://example.org/\x00", false, "", "", 0, "", ""},
		{"titan://example.org/~a/r/issues/new;size=12;mime=text/plain;token=abc", true, "titan", "/~a/r/issues/new", 12, "text/plain", "abc"},
		{"titan://example.org/~a/r/issues/new;mime=text/plain;size=5", true, "titan", "/~a/r/issues/new", 5, "text/plain", ""},
		{"titan://example.org/~a/r/issues/new;size=5", true, "titan", "/~a/r/issues/new", 5, "text/gemini", ""},
		{"titan://example.org/~a/r/issues/new", false, "", "", 0, "", ""},
		{"titan://example.org/~a/r/issues/new;size=x", false, "", "", 0, "", ""},
		{"titan://example.org/~a/r/issues/new;size=5;size=6", false, "", "", 0, "", ""},
		{"titan://example.org/~a/r;size=5/../new", false, "", "", 0, "", ""},
		{"titan://example.org/~a/r/issues/new;size=5;evil=1", false, "", "", 0, "", ""},
		{"titan://example.org/x;size=1;mime=image%2Fpng", true, "titan", "/x", 1, "image/png", ""},
		{"titan://example.org/~a/r/issues/3;edit", true, "titan", "/~a/r/issues/3", 0, "text/gemini", ""},
		{"titan://example.org/~a/r/issues/3;edit;size=4", false, "", "", 0, "", ""},
	}
	for _, c := range cases {
		u, tp, err := parseRequestLine(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v err=%v", c.in, c.ok, err)
			continue
		}
		if !c.ok {
			continue
		}
		if u.Scheme != c.scheme || u.Path != c.path {
			t.Errorf("%q: got %s %q", c.in, u.Scheme, u.Path)
		}
		if c.scheme == "titan" {
			if tp == nil || tp.Size != c.size || tp.MIME != c.mime || tp.Token != c.token {
				t.Errorf("%q: titan params %+v", c.in, tp)
			}
		} else if tp != nil {
			t.Errorf("%q: unexpected titan params", c.in)
		}
	}
	long := "gemini://example.org/" + strings.Repeat("a", 1100)
	if _, _, err := parseRequestLine(long); err != ErrRequestTooLong {
		t.Errorf("long request: %v", err)
	}
}

func TestSanitizeMeta(t *testing.T) {
	if got := SanitizeMeta("ok\r\n20 text/gemini\r\n"); got != "ok20 text/gemini" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", 600)
	got := SanitizeMeta(long)
	if len(got) > MaxMetaBytes || !strings.HasSuffix(got, "é") {
		t.Errorf("truncation broke utf-8: len=%d", len(got))
	}
}

func TestPageEscaping(t *testing.T) {
	p := NewPage()
	p.Heading(1, "Title\nwith newline")
	p.Text("=> gemini://evil/ click me\n# not a heading\n* item\n> quote\n```pre")
	p.Link("gemini://x/a b", "label\r\n=> injected")
	p.Pre("code", "line1\n```\nline3\n")
	want := "# Title with newline\n" +
		" => gemini://evil/ click me\n # not a heading\n * item\n > quote\n ```pre\n" +
		"=> gemini://x/a%20b label => injected\n" +
		"```code\nline1\n ```\nline3\n```\n"
	if p.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", p.String(), want)
	}
}

func TestResponseWriter(t *testing.T) {
	var sb strings.Builder
	rw := newResponseWriter(&sb)
	if _, err := rw.Write([]byte("x")); err == nil {
		t.Error("write before header should fail")
	}
	if err := rw.Header(20, "text/gemini"); err != nil {
		t.Fatal(err)
	}
	if err := rw.Header(20, "again"); err == nil {
		t.Error("second header should fail")
	}
	_, _ = rw.Write([]byte("body"))
	_ = rw.flush()
	if sb.String() != "20 text/gemini\r\nbody" {
		t.Errorf("got %q", sb.String())
	}
	var sb2 strings.Builder
	rw2 := newResponseWriter(&sb2)
	_ = rw2.Header(51, "nope")
	if _, err := rw2.Write([]byte("x")); err == nil {
		t.Error("body on 51 should fail")
	}
}
