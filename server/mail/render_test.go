package mail

import (
	"strings"
	"testing"
)

func TestRender_HeadingsAndParagraph(t *testing.T) {
	got := RenderHTML("# Hi\n\nAbsatz")
	if !strings.Contains(got, "<h2>Hi</h2>") {
		t.Errorf("h2 fehlt: %q", got)
	}
	if !strings.Contains(got, "<p>Absatz</p>") {
		t.Errorf("Absatz fehlt: %q", got)
	}
}

func TestRender_NoRawHTML(t *testing.T) {
	got := RenderHTML(`<script>alert('xss')</script>`)
	if strings.Contains(got, "<script>") {
		t.Errorf("raw <script> pass-through: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("script-Tag wurde nicht escaped: %q", got)
	}
}

func TestRender_BoldItalicCode(t *testing.T) {
	cases := map[string]string{
		"**fett**": "<strong>fett</strong>",
		"*kursiv*": "<em>kursiv</em>",
		"`inline`": "<code>inline</code>",
	}
	for in, want := range cases {
		got := RenderHTML(in)
		if !strings.Contains(got, want) {
			t.Errorf("Input %q: erwartet %q, got %q", in, want, got)
		}
	}
}

func TestRender_CodeBlock(t *testing.T) {
	src := "```\nx := 1\n```"
	got := RenderHTML(src)
	if !strings.Contains(got, "<pre><code>x := 1") {
		t.Errorf("Code-Block fehlt: %q", got)
	}
}

func TestRender_Lists(t *testing.T) {
	src := "- a\n- b\n- c"
	got := RenderHTML(src)
	if !strings.Contains(got, "<ul>") || !strings.Contains(got, "<li>a</li>") {
		t.Errorf("ul/li fehlt: %q", got)
	}

	src2 := "1. eins\n2. zwei"
	got = RenderHTML(src2)
	if !strings.Contains(got, "<ol>") || !strings.Contains(got, "<li>eins</li>") {
		t.Errorf("ol/li fehlt: %q", got)
	}
}

func TestRender_Link_AllowedScheme(t *testing.T) {
	got := RenderHTML("[click](https://example.org)")
	if !strings.Contains(got, `<a href="https://example.org">click</a>`) {
		t.Errorf("https-Link fehlt: %q", got)
	}
}

func TestRender_Link_RejectsJavaScriptScheme(t *testing.T) {
	got := RenderHTML("[click](javascript:alert(1))")
	// Wichtig: kein href-Attribut mit javascript:. Der Text-Fallback darf
	// den Schema-String als Text enthalten (das ist sicher — nur als HTML-
	// Attribut wäre es gefährlich).
	if strings.Contains(got, `href="javascript`) {
		t.Errorf("javascript:-Scheme als href: %q", got)
	}
	if strings.Contains(got, "<a ") {
		t.Errorf("Unsicherer Link wurde als <a> gerendert: %q", got)
	}
}

func TestRender_Link_RejectsDataScheme(t *testing.T) {
	got := RenderHTML("[x](data:text/html,<script>1</script>)")
	if strings.Contains(got, `href="data`) {
		t.Errorf("data:-Scheme als href: %q", got)
	}
	if strings.Contains(got, "<a ") {
		t.Errorf("Unsicherer Link wurde als <a> gerendert: %q", got)
	}
	// Doppelschutz: das eingebettete <script> muss escaped sein.
	if strings.Contains(got, "<script>") {
		t.Errorf("script-Tag pass-through: %q", got)
	}
}

func TestRender_HorizontalRule(t *testing.T) {
	got := RenderHTML("Hallo\n\n---\n\nWelt")
	if !strings.Contains(got, "<hr>") {
		t.Errorf("hr fehlt: %q", got)
	}
}

func TestRender_Blockquote(t *testing.T) {
	got := RenderHTML("> Zitat hier")
	if !strings.Contains(got, "<blockquote>Zitat hier</blockquote>") {
		t.Errorf("blockquote fehlt: %q", got)
	}
}

func TestRender_EscapesHTMLInPlainText(t *testing.T) {
	got := RenderHTML(`5 < 10 & 10 > 3`)
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") || !strings.Contains(got, "&gt;") {
		t.Errorf("HTML-Entities fehlen: %q", got)
	}
}

func TestRender_NestedBoldItalic(t *testing.T) {
	got := RenderHTML("**bold *with* italic**")
	if !strings.Contains(got, "<strong>") || !strings.Contains(got, "<em>") {
		t.Errorf("nested fehlt: %q", got)
	}
}

func TestRender_RealisticCardBody(t *testing.T) {
	src := `# Karte #42

**Status:** In Arbeit

- Sub-task 1
- Sub-task 2

Mehr Info auf [GitHub](https://github.com/example/repo).

` + "```\ncode here\n```"
	got := RenderHTML(src)
	for _, want := range []string{"<h2>Karte #42</h2>", "<strong>Status:</strong>", "<ul>",
		"<li>Sub-task 1</li>", `<a href="https://github.com/example/repo">GitHub</a>`,
		"<pre><code>code here"} {
		if !strings.Contains(got, want) {
			t.Errorf("realistic body: erwartet %q in %q", want, got)
		}
	}
}

func TestBuildHTMLBody_WrapsAndEscapesTitle(t *testing.T) {
	html := BuildHTMLBody(`<title-attack>`, "<h1>OK</h1>", "Hallo")
	if strings.Contains(html, "<title-attack>") {
		t.Errorf("Title-Escape fehlt: %s", html)
	}
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Errorf("Wrapper fehlt")
	}
}
