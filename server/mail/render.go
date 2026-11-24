// Package mail — Server-side Mail-Versand (#293, ADR-0091).
//
// render.go: Mini-Markdown-Renderer mit STRENGER Allow-Liste (kein raw HTML,
// nur eine feste Token-Liste — siehe ADR-0091 §2). Bewusst restriktiver als
// der Client-Renderer (ADR-0089), weil Mail-Clients (Outlook/Gmail/Mobile)
// eine breitere XSS-Angriffsfläche haben.

package mail

import (
	"fmt"
	"html"
	"strings"
)

// RenderHTML übersetzt Markdown-Source in eine restriktive HTML-Allow-Liste:
// Headings (h2/h3), Bold/Italic, Code (inline + Block), Lists (ul/ol),
// Blockquotes, hr, Paragraphs, Links (https?://). Alles andere wird als
// Text escaped.
//
// **Kein Pass-Through von raw HTML** — `<script>` etc. wird als Text gezeigt.
// Links werden gegen `https?://`-Schema gefiltert.
func RenderHTML(src string) string {
	var out strings.Builder
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) {
		l := lines[i]
		trimmed := strings.TrimRight(l, "\r")

		// Code-Block ```…```
		if strings.HasPrefix(trimmed, "```") {
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(strings.TrimRight(lines[j], "\r"), "```") {
				j++
			}
			body := ""
			if j > i+1 {
				body = strings.Join(lines[i+1:j], "\n")
			}
			out.WriteString("<pre><code>")
			out.WriteString(html.EscapeString(body))
			out.WriteString("</code></pre>\n")
			i = j + 1
			continue
		}

		// Heading
		if strings.HasPrefix(trimmed, "# ") {
			out.WriteString("<h2>")
			out.WriteString(renderInline(strings.TrimPrefix(trimmed, "# ")))
			out.WriteString("</h2>\n")
			i++
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			out.WriteString("<h3>")
			out.WriteString(renderInline(strings.TrimPrefix(trimmed, "## ")))
			out.WriteString("</h3>\n")
			i++
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			out.WriteString("<h4>")
			out.WriteString(renderInline(strings.TrimPrefix(trimmed, "### ")))
			out.WriteString("</h4>\n")
			i++
			continue
		}

		// Horizontal rule
		if trimmed == "---" || trimmed == "***" {
			out.WriteString("<hr>\n")
			i++
			continue
		}

		// Blockquote
		if strings.HasPrefix(trimmed, "> ") {
			j := i
			var bq strings.Builder
			for j < len(lines) && strings.HasPrefix(strings.TrimRight(lines[j], "\r"), "> ") {
				if bq.Len() > 0 {
					bq.WriteString(" ")
				}
				bq.WriteString(strings.TrimPrefix(strings.TrimRight(lines[j], "\r"), "> "))
				j++
			}
			out.WriteString("<blockquote>")
			out.WriteString(renderInline(bq.String()))
			out.WriteString("</blockquote>\n")
			i = j
			continue
		}

		// Unordered list
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			out.WriteString("<ul>")
			for i < len(lines) {
				t := strings.TrimRight(lines[i], "\r")
				if strings.HasPrefix(t, "- ") {
					out.WriteString("<li>")
					out.WriteString(renderInline(strings.TrimPrefix(t, "- ")))
					out.WriteString("</li>")
					i++
				} else if strings.HasPrefix(t, "* ") {
					out.WriteString("<li>")
					out.WriteString(renderInline(strings.TrimPrefix(t, "* ")))
					out.WriteString("</li>")
					i++
				} else {
					break
				}
			}
			out.WriteString("</ul>\n")
			continue
		}

		// Ordered list (any digit followed by ". ")
		if isOrderedListLine(trimmed) {
			out.WriteString("<ol>")
			for i < len(lines) {
				t := strings.TrimRight(lines[i], "\r")
				if isOrderedListLine(t) {
					content := stripOrderedListPrefix(t)
					out.WriteString("<li>")
					out.WriteString(renderInline(content))
					out.WriteString("</li>")
					i++
				} else {
					break
				}
			}
			out.WriteString("</ol>\n")
			continue
		}

		// Empty line → keep paragraph break.
		if trimmed == "" {
			i++
			continue
		}

		// Paragraph: collect consecutive non-empty, non-special lines.
		var p strings.Builder
		for i < len(lines) {
			t := strings.TrimRight(lines[i], "\r")
			if t == "" || isBlockStart(t) {
				break
			}
			if p.Len() > 0 {
				p.WriteString(" ")
			}
			p.WriteString(t)
			i++
		}
		if p.Len() > 0 {
			out.WriteString("<p>")
			out.WriteString(renderInline(p.String()))
			out.WriteString("</p>\n")
		}
	}
	return out.String()
}

// renderInline übersetzt Inline-Markup (**bold**, *italic*, `code`, [link](url))
// und escaped alles andere. Ungültige Konstrukte werden als Text gezeigt.
//
// Sprint-40-Kritiker-Fix [V]P3: Tiefenlimit gegen pathologischen User-Input
// (`**...**...**`-Spam → Stack-Overflow). 32 ist großzügig genug für realen
// Markup-Gebrauch, knapp genug um adversarial Inputs zu kappen.
func renderInline(s string) string {
	return renderInlineDepth(s, 0)
}

const renderInlineMaxDepth = 32

func renderInlineDepth(s string, depth int) string {
	if depth >= renderInlineMaxDepth {
		return html.EscapeString(s)
	}
	// Wir gehen den String Zeichenweise durch und behandeln Markup-Tokens.
	var out strings.Builder
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		c := runes[i]
		switch c {
		case '`':
			// Inline-Code bis zum nächsten Backtick.
			end := indexRune(runes, '`', i+1)
			if end > i {
				out.WriteString("<code>")
				out.WriteString(html.EscapeString(string(runes[i+1 : end])))
				out.WriteString("</code>")
				i = end + 1
				continue
			}
		case '*':
			// **bold** oder *italic*
			if i+1 < len(runes) && runes[i+1] == '*' {
				end := indexSeq(runes, "**", i+2)
				if end > i+1 {
					out.WriteString("<strong>")
					out.WriteString(renderInlineDepth(string(runes[i+2:end]), depth+1))
					out.WriteString("</strong>")
					i = end + 2
					continue
				}
			} else {
				end := indexRune(runes, '*', i+1)
				if end > i {
					out.WriteString("<em>")
					out.WriteString(renderInlineDepth(string(runes[i+1:end]), depth+1))
					out.WriteString("</em>")
					i = end + 1
					continue
				}
			}
		case '[':
			// [label](url) — Sprint-40-Kritiker-Fix [K]P3: balancierten
			// Klammer-Counter statt naivem First-`)` (sonst bricht
			// Wikipedia-URL `Foo_(bar)`).
			closeBracket := indexRune(runes, ']', i+1)
			if closeBracket > i && closeBracket+1 < len(runes) && runes[closeBracket+1] == '(' {
				closeParen := findBalancedParen(runes, closeBracket+2)
				if closeParen > closeBracket+1 {
					label := string(runes[i+1 : closeBracket])
					url := string(runes[closeBracket+2 : closeParen])
					if isAllowedURL(url) {
						out.WriteString(`<a href="`)
						out.WriteString(html.EscapeString(url))
						out.WriteString(`">`)
						out.WriteString(renderInlineDepth(label, depth+1))
						out.WriteString("</a>")
						i = closeParen + 1
						continue
					}
				}
			}
		}
		out.WriteString(html.EscapeString(string(c)))
		i++
	}
	return out.String()
}

// findBalancedParen sucht das schließende `)` ab `from`, das die Klammer
// vor `from` schließt — innerhalb verschachtelter `(...)` korrekt skippend.
// Wikipedia-URL-Pattern: `Foo_(bar)` darf das innere `)` nicht als URL-Ende
// gewertet werden, weil die `(` davor ungeschlossen ist.
func findBalancedParen(rs []rune, from int) int {
	depth := 0
	for i := from; i < len(rs); i++ {
		if rs[i] == '(' {
			depth++
		} else if rs[i] == ')' {
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

func indexRune(rs []rune, target rune, from int) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == target {
			return i
		}
	}
	return -1
}

func indexSeq(rs []rune, seq string, from int) int {
	target := []rune(seq)
	for i := from; i+len(target) <= len(rs); i++ {
		match := true
		for j, t := range target {
			if rs[i+j] != t {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func isOrderedListLine(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if i == 0 {
			return false
		}
		if i+1 < len(s) && s[i] == '.' && s[i+1] == ' ' {
			return true
		}
		return false
	}
	return false
}

func stripOrderedListPrefix(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if i+1 < len(s) && s[i] == '.' && s[i+1] == ' ' {
			return s[i+2:]
		}
		break
	}
	return s
}

func isBlockStart(line string) bool {
	switch {
	case strings.HasPrefix(line, "# "):
	case strings.HasPrefix(line, "## "):
	case strings.HasPrefix(line, "### "):
	case strings.HasPrefix(line, "- "):
	case strings.HasPrefix(line, "* "):
	case strings.HasPrefix(line, "> "):
	case strings.HasPrefix(line, "```"):
	case line == "---" || line == "***":
	default:
		if isOrderedListLine(line) {
			return true
		}
		return false
	}
	return true
}

// isAllowedURL filtert Linkziele gegen das erlaubte Schema. javascript:/data:
// werden bewusst NICHT erlaubt — Defense-in-depth gegen Mail-Client-XSS.
func isAllowedURL(u string) bool {
	lc := strings.ToLower(u)
	return strings.HasPrefix(lc, "http://") || strings.HasPrefix(lc, "https://") || strings.HasPrefix(lc, "mailto:")
}

// BuildHTMLBody umwickelt den gerenderten Markdown-Inhalt mit minimalem
// Mail-tauglichem Wrapper.
func BuildHTMLBody(title, headerHTML, contentMarkdown string) string {
	return fmt.Sprintf(
		`<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head>`+
			`<body>%s%s</body></html>`,
		html.EscapeString(title),
		headerHTML,
		RenderHTML(contentMarkdown),
	)
}
