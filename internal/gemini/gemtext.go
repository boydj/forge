package gemini

import (
	"fmt"
	"strings"
)

// Page builds a gemtext document. All text passed in is treated as data:
// lines that would otherwise be interpreted as links, headings, lists,
// quotes or preformat toggles are neutralised by a leading space unless the
// caller uses the dedicated method for that line type.
type Page struct {
	b strings.Builder
}

// NewPage returns an empty page.
func NewPage() *Page { return &Page{} }

// String returns the document.
func (p *Page) String() string { return p.b.String() }

// Bytes returns the document as bytes.
func (p *Page) Bytes() []byte { return []byte(p.b.String()) }

// Len returns the current byte length.
func (p *Page) Len() int { return p.b.Len() }

// Heading writes a heading of level 1-3.
func (p *Page) Heading(level int, text string) {
	if level < 1 {
		level = 1
	}
	if level > 3 {
		level = 3
	}
	p.b.WriteString(strings.Repeat("#", level))
	p.b.WriteByte(' ')
	p.b.WriteString(oneLine(text))
	p.b.WriteByte('\n')
}

// Text writes one or more lines of plain text, escaping line-type prefixes.
func (p *Page) Text(text string) {
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		p.b.WriteString(EscapeLine(l))
		p.b.WriteByte('\n')
	}
}

// Textf writes a formatted single line of text.
func (p *Page) Textf(format string, args ...any) { p.Text(fmt.Sprintf(format, args...)) }

// Blank writes an empty line.
func (p *Page) Blank() { p.b.WriteByte('\n') }

// Link writes a link line. The label may be empty.
func (p *Page) Link(target, label string) {
	p.b.WriteString("=> ")
	p.b.WriteString(oneLine(strings.ReplaceAll(target, " ", "%20")))
	if label != "" {
		p.b.WriteByte(' ')
		p.b.WriteString(oneLine(label))
	}
	p.b.WriteByte('\n')
}

// Item writes a list item.
func (p *Page) Item(text string) {
	p.b.WriteString("* ")
	p.b.WriteString(oneLine(text))
	p.b.WriteByte('\n')
}

// Quote writes quoted lines.
func (p *Page) Quote(text string) {
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		p.b.WriteString("> ")
		p.b.WriteString(l)
		p.b.WriteByte('\n')
	}
}

// Pre writes a preformatted block. Lines inside that begin with ``` are
// prefixed with a space so they cannot terminate the block early.
func (p *Page) Pre(alt, body string) {
	p.b.WriteString("```")
	p.b.WriteString(oneLine(alt))
	p.b.WriteByte('\n')
	body = strings.ReplaceAll(body, "\r\n", "\n")
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "```") {
			p.b.WriteByte(' ')
		}
		p.b.WriteString(l)
		p.b.WriteByte('\n')
	}
	if strings.HasSuffix(body, "\n") {
		// Split produced a trailing empty element; drop the extra newline.
		s := p.b.String()
		p.b.Reset()
		p.b.WriteString(s[:len(s)-1])
	}
	p.b.WriteString("```\n")
}

// Raw appends already-formatted gemtext. Use only for trusted content.
func (p *Page) Raw(s string) {
	p.b.WriteString(s)
	if !strings.HasSuffix(s, "\n") {
		p.b.WriteByte('\n')
	}
}

// EscapeLine neutralises gemtext line-type prefixes in a single line of
// untrusted text by prefixing a space.
func EscapeLine(l string) string {
	l = strings.TrimRight(l, "\r")
	switch {
	case strings.HasPrefix(l, "=>"),
		strings.HasPrefix(l, "#"),
		strings.HasPrefix(l, "* "),
		strings.HasPrefix(l, ">"),
		strings.HasPrefix(l, "```"):
		return " " + l
	}
	return l
}

// oneLine removes CR and LF so a value cannot break out of its line.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", "", "\n", " ").Replace(s)
}
