package web

import (
	"regexp"
	"strings"

	"as215520.net/forge/internal/gemini"
)

var (
	mdLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	mdInline  = regexp.MustCompile("(\\*\\*|__|`)(.+?)(\\*\\*|__|`)")
)

// MarkdownToGemtext converts a practical subset of Markdown to gemtext:
// headings, fenced code, lists, quotes, paragraphs (joined), and links,
// which are hoisted to their own link lines after the paragraph. Relative
// link targets are resolved against base.
func MarkdownToGemtext(md, base string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	var out strings.Builder
	var para []string
	var links [][2]string
	inFence := false
	fenceAlt := ""
	flush := func() {
		if len(para) > 0 {
			text := strings.Join(para, " ")
			out.WriteString(gemini.EscapeLine(stripInline(text)))
			out.WriteByte('\n')
			para = nil
		}
		for _, l := range links {
			out.WriteString("=> " + l[1] + " " + l[0] + "\n")
		}
		links = nil
	}
	extract := func(line string) string {
		line = mdImageRe.ReplaceAllStringFunc(line, func(m string) string {
			sm := mdImageRe.FindStringSubmatch(m)
			links = append(links, [2]string{"image: " + sm[1], resolveLink(sm[2], base)})
			return sm[1]
		})
		return mdLinkRe.ReplaceAllStringFunc(line, func(m string) string {
			sm := mdLinkRe.FindStringSubmatch(m)
			label := sm[1]
			if label == "" {
				label = sm[2]
			}
			links = append(links, [2]string{label, resolveLink(sm[2], base)})
			return label
		})
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			if inFence {
				out.WriteString("```\n")
				inFence = false
			} else {
				flush()
				fenceAlt = strings.TrimSpace(strings.TrimLeft(line, "`~"))
				out.WriteString("```" + strings.ReplaceAll(fenceAlt, "\n", "") + "\n")
				inFence = true
			}
			continue
		}
		if inFence {
			if strings.HasPrefix(line, "```") {
				out.WriteByte(' ')
			}
			out.WriteString(line + "\n")
			continue
		}
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			flush()
			out.WriteByte('\n')
		case strings.HasPrefix(trim, "#"):
			flush()
			level := 0
			for level < len(trim) && trim[level] == '#' {
				level++
			}
			text := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(trim[level:]), "#"))
			if level > 3 {
				level = 3
			}
			out.WriteString(strings.Repeat("#", level) + " " + stripInline(extract(text)) + "\n")
			flush()
		case strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") || strings.HasPrefix(trim, "+ "):
			flush()
			out.WriteString("* " + stripInline(extract(trim[2:])) + "\n")
			flush()
		case strings.HasPrefix(trim, "> "):
			flush()
			out.WriteString("> " + stripInline(extract(trim[2:])) + "\n")
			flush()
		case strings.HasPrefix(trim, "    ") || strings.HasPrefix(line, "\t"):
			flush()
			out.WriteString("```\n" + strings.TrimPrefix(strings.TrimPrefix(line, "    "), "\t") + "\n```\n")
		case trim == "---" || trim == "***" || trim == "___":
			flush()
		case regexp.MustCompile(`^\d+\. `).MatchString(trim):
			flush()
			out.WriteString("* " + stripInline(extract(trim)) + "\n")
			flush()
		default:
			para = append(para, extract(trim))
		}
	}
	flush()
	if inFence {
		out.WriteString("```\n")
	}
	return out.String()
}

func stripInline(s string) string {
	return mdInline.ReplaceAllString(s, "$2")
}

func resolveLink(target, base string) string {
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "/") {
		return sanitizeURL(target)
	}
	target = strings.TrimPrefix(target, "./")
	if strings.HasPrefix(target, "#") {
		return ""
	}
	return sanitizeURL(base + target)
}

func sanitizeURL(u string) string {
	u = strings.Map(func(r rune) rune {
		if r < 0x21 || r == 0x7f {
			return -1
		}
		return r
	}, u)
	if strings.HasPrefix(strings.ToLower(u), "javascript:") || strings.HasPrefix(strings.ToLower(u), "data:") {
		return ""
	}
	return u
}

// rebaseGemtextLinks rewrites relative link lines in a gemtext document so
// they resolve against base (the directory of the file being shown).
func rebaseGemtextLinks(gmi, base string) string {
	lines := strings.Split(strings.ReplaceAll(gmi, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "=>") {
			continue
		}
		rest := strings.TrimSpace(l[2:])
		target, label, _ := strings.Cut(rest, " ")
		target = resolveLink(target, base)
		if target == "" {
			lines[i] = " " + l
			continue
		}
		lines[i] = strings.TrimRight("=> "+target+" "+strings.TrimSpace(label), " ")
	}
	return strings.Join(lines, "\n")
}
