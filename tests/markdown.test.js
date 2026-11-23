// Unit-Tests für markdown.js (→ Karte #90, ADR-0010, ADR-0031)
// Parser-Regression-Schutz: Tokenizer + Renderer für alle unterstützten Block- und Inline-Typen.
import { Markdown } from '../src/markdown.js'

/* ── Hilfsfunktionen ─────────────────────────────────────────────────── */

/** Rendert Markdown und gibt das Container-div zurück. */
function render(text, onToggle) {
  return Markdown.render(text, onToggle)
}

/** Gibt den ersten Element-Nachfahren eines bestimmten Tag-Namens zurück. */
function first(container, tag) {
  return container.querySelector(tag)
}

/* ══════════════════════════════════════════════════════════════════════
   1. Leerer / trivialer Input
══════════════════════════════════════════════════════════════════════ */

describe('Leerer Input', () => {
  it('leerer String → leeres div.md-content ohne Kind-Elemente', () => {
    const el = render('')
    expect(el.tagName).toBe('DIV')
    expect(el.className).toBe('md-content')
    expect(el.childElementCount).toBe(0)
  })

  it('nur Leerzeichen → leeres div.md-content', () => {
    const el = render('   \n  \n')
    expect(el.childElementCount).toBe(0)
  })

  it('null-Input → leeres div.md-content', () => {
    const el = render(null)
    expect(el.childElementCount).toBe(0)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   2. Überschriften
══════════════════════════════════════════════════════════════════════ */

describe('Überschriften', () => {
  it('# H1 → <h1 class="md-h1">', () => {
    const el = render('# Titel')
    const h1 = first(el, 'h1')
    expect(h1).not.toBeNull()
    expect(h1.className).toBe('md-h1')
    expect(h1.textContent).toBe('Titel')
  })

  it('## H2 → <h2 class="md-h2">', () => {
    const el = render('## Untertitel')
    const h2 = first(el, 'h2')
    expect(h2).not.toBeNull()
    expect(h2.textContent).toBe('Untertitel')
  })

  it('### H3 → <h3 class="md-h3">', () => {
    const el = render('### Abschnitt')
    const h3 = first(el, 'h3')
    expect(h3).not.toBeNull()
    expect(h3.className).toBe('md-h3')
  })

  it('Überschriften können Inline-Markup enthalten', () => {
    const el = render('# **Fett** und *kursiv*')
    const h1 = first(el, 'h1')
    expect(h1.querySelector('strong')).not.toBeNull()
    expect(h1.querySelector('em')).not.toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   3. Inline-Markup
══════════════════════════════════════════════════════════════════════ */

describe('Inline: fett', () => {
  it('**text** → <strong>', () => {
    const el = render('Das ist **fett** hier.')
    const strong = first(el, 'strong')
    expect(strong).not.toBeNull()
    expect(strong.textContent).toBe('fett')
  })
})

describe('Inline: kursiv', () => {
  it('*text* → <em>', () => {
    const el = render('Das ist *kursiv* hier.')
    const em = first(el, 'em')
    expect(em).not.toBeNull()
    expect(em.textContent).toBe('kursiv')
  })
})

describe('Inline: durchgestrichen', () => {
  it('~~text~~ → <del>', () => {
    const el = render('Das ist ~~durchgestrichen~~.')
    const del = first(el, 'del')
    expect(del).not.toBeNull()
    expect(del.textContent).toBe('durchgestrichen')
  })
})

describe('Inline: Code', () => {
  it('`code` → <code class="md-inline-code">', () => {
    const el = render('Verwende `const x = 1` hier.')
    const code = first(el, 'code')
    expect(code).not.toBeNull()
    expect(code.className).toBe('md-inline-code')
    expect(code.textContent).toBe('const x = 1')
  })
})

describe('Inline: Link', () => {
  it('[Text](https://example.com) → <a> mit href und target="_blank"', () => {
    const el = render('[Beispiel](https://example.com)')
    const a = first(el, 'a')
    expect(a).not.toBeNull()
    expect(a.textContent).toBe('Beispiel')
    // happy-dom normalisiert URLs und fügt trailing slash hinzu — startsWith prüfen
    expect(a.href).toMatch(/^https:\/\/example\.com/)
    expect(a.target).toBe('_blank')
    expect(a.rel).toContain('noopener')
  })

  it('Link mit http:// wird akzeptiert', () => {
    const el = render('[HTTP](http://example.com)')
    const a = first(el, 'a')
    // happy-dom normalisiert URLs — startsWith prüfen
    expect(a.href).toMatch(/^http:\/\/example\.com/)
  })

  it('Link ohne http/https → href="#" (Sicherheit — kein javascript: erlaubt)', () => {
    const el = render('[Gefährlich](javascript:alert(1))')
    const a = first(el, 'a')
    expect(a).not.toBeNull()
    // Der Parser setzt href auf '#' — happy-dom erweitert das zu absolutem URL
    expect(a.href).toContain('#')
    // Wichtig: kein javascript: im href
    expect(a.href).not.toContain('javascript:')
  })

  it('Mehrere Inline-Elemente in einer Zeile', () => {
    const el = render('**fett** und *kursiv* und `code`')
    const p = first(el, 'p')
    expect(p.querySelector('strong')).not.toBeNull()
    expect(p.querySelector('em')).not.toBeNull()
    expect(p.querySelector('code')).not.toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   4. Listen
══════════════════════════════════════════════════════════════════════ */

describe('Ungeordnete Liste', () => {
  it('- Einträge → <ul class="md-ul"> mit <li>', () => {
    const el = render('- Eins\n- Zwei\n- Drei')
    const ul = first(el, 'ul.md-ul')
    expect(ul).not.toBeNull()
    expect(ul.children.length).toBe(3)
    expect(ul.children[0].textContent).toBe('Eins')
    expect(ul.children[2].textContent).toBe('Drei')
  })

  it('* Einträge werden ebenfalls erkannt', () => {
    const el = render('* Alpha\n* Beta')
    const ul = first(el, 'ul.md-ul')
    expect(ul).not.toBeNull()
    expect(ul.children.length).toBe(2)
  })
})

describe('Geordnete Liste', () => {
  it('1. 2. 3. → <ol class="md-ol"> mit <li>', () => {
    const el = render('1. Erster\n2. Zweiter\n3. Dritter')
    const ol = first(el, 'ol.md-ol')
    expect(ol).not.toBeNull()
    expect(ol.children.length).toBe(3)
    expect(ol.children[1].textContent).toBe('Zweiter')
  })
})

/* ══════════════════════════════════════════════════════════════════════
   5. Checkliste
══════════════════════════════════════════════════════════════════════ */

describe('Checkliste', () => {
  it('- [ ] offen → Checkbox unchecked', () => {
    const el = render('- [ ] Offen')
    const ul = first(el, 'ul.md-checklist')
    expect(ul).not.toBeNull()
    const cb = ul.querySelector('input[type="checkbox"]')
    expect(cb).not.toBeNull()
    expect(cb.checked).toBe(false)
  })

  it('- [x] erledigt → Checkbox checked', () => {
    const el = render('- [x] Erledigt')
    const cb = first(el, 'input[type="checkbox"]')
    expect(cb.checked).toBe(true)
  })

  it('Gemischte Checkliste: offen + erledigt', () => {
    const el = render('- [ ] Aufgabe 1\n- [x] Aufgabe 2\n- [ ] Aufgabe 3')
    const cbs = el.querySelectorAll('input[type="checkbox"]')
    expect(cbs.length).toBe(3)
    expect(cbs[0].checked).toBe(false)
    expect(cbs[1].checked).toBe(true)
    expect(cbs[2].checked).toBe(false)
  })

  it('Ohne onCheckboxToggle → Checkboxes sind disabled', () => {
    const el = render('- [ ] Aufgabe')
    const cb = first(el, 'input[type="checkbox"]')
    expect(cb.disabled).toBe(true)
  })

  it('Mit onCheckboxToggle → Checkboxes sind nicht disabled', () => {
    const el = render('- [ ] Aufgabe', () => {})
    const cb = first(el, 'input[type="checkbox"]')
    expect(cb.disabled).toBe(false)
  })

  it('Erledigtes Item hat class "md-checklist-done" am Label-Span', () => {
    const el = render('- [x] Fertig')
    const span = el.querySelector('span.md-checklist-done')
    expect(span).not.toBeNull()
  })

  it('Offenes Item hat KEIN class "md-checklist-done"', () => {
    const el = render('- [ ] Noch offen')
    const span = el.querySelector('span.md-checklist-done')
    expect(span).toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   6. Zitat (Blockquote)
══════════════════════════════════════════════════════════════════════ */

describe('Blockquote', () => {
  it('> Zeile → <blockquote class="md-blockquote">', () => {
    const el = render('> Das ist ein Zitat.')
    const bq = first(el, 'blockquote.md-blockquote')
    expect(bq).not.toBeNull()
    expect(bq.textContent).toContain('Das ist ein Zitat.')
  })

  it('Mehrzeiliges Zitat → mehrere <p> in <blockquote>', () => {
    const el = render('> Zeile 1\n> Zeile 2')
    const bq = first(el, 'blockquote')
    const ps = bq.querySelectorAll('p')
    expect(ps.length).toBe(2)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   7. Trennlinie
══════════════════════════════════════════════════════════════════════ */

describe('Horizontale Trennlinie', () => {
  it('--- → <hr>', () => {
    const el = render('---')
    const hr = first(el, 'hr')
    expect(hr).not.toBeNull()
  })

  it('Längere Trennlinie ----- → <hr>', () => {
    const el = render('-----')
    expect(first(el, 'hr')).not.toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   8. Codeblock
══════════════════════════════════════════════════════════════════════ */

describe('Codeblock', () => {
  it('```js\\n...\\n``` → <pre class="md-pre"><code class="language-js">', () => {
    const el = render('```js\nconst x = 1;\n```')
    const pre = first(el, 'pre.md-pre')
    expect(pre).not.toBeNull()
    const code = pre.querySelector('code')
    expect(code).not.toBeNull()
    expect(code.className).toBe('language-js')
    expect(code.textContent).toBe('const x = 1;')
  })

  it('Codeblock ohne Sprache → <code> ohne className', () => {
    const el = render('```\nkein Sprachhinweis\n```')
    const code = el.querySelector('pre code')
    expect(code.className).toBe('')
  })

  it('Codeblock-Inhalt wird nicht als Markup interpretiert', () => {
    const el = render('```\n**kein fett** hier\n```')
    const code = el.querySelector('pre code')
    expect(code.querySelector('strong')).toBeNull()
    expect(code.textContent).toBe('**kein fett** hier')
  })
})

/* ══════════════════════════════════════════════════════════════════════
   9. CSV-Tabellen (ADR-0010-Erweiterung)
══════════════════════════════════════════════════════════════════════ */

describe('CSV-Tabellen', () => {
  const csvMarkdown = '```csv\nName,Wert,Status\nFeature A,100,fertig\nFeature B,50,offen\n```'

  it('```csv → <table class="md-table">', () => {
    const el = render(csvMarkdown)
    const table = first(el, 'table.md-table')
    expect(table).not.toBeNull()
  })

  it('Erste Zeile wird als <th>-Header gerendert', () => {
    const el = render(csvMarkdown)
    const ths = el.querySelectorAll('th')
    expect(ths.length).toBe(3)
    expect(ths[0].textContent).toBe('Name')
    expect(ths[1].textContent).toBe('Wert')
    expect(ths[2].textContent).toBe('Status')
  })

  it('Datenzeilen werden als <td> gerendert', () => {
    const el = render(csvMarkdown)
    const tds = el.querySelectorAll('td')
    expect(tds.length).toBe(6) // 2 Zeilen × 3 Spalten
    expect(tds[0].textContent).toBe('Feature A')
    expect(tds[3].textContent).toBe('Feature B')
  })

  it('CSV-Felder mit Leerzeichen werden getrimmt', () => {
    const el = render('```csv\nA , B , C\n1 , 2 , 3\n```')
    const ths = el.querySelectorAll('th')
    expect(ths[0].textContent).toBe('A')
    expect(ths[1].textContent).toBe('B')
  })

  it('CSV-Block erzeugt KEIN <pre> (kein normaler Codeblock)', () => {
    const el = render(csvMarkdown)
    expect(first(el, 'pre')).toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   10. Absatz
══════════════════════════════════════════════════════════════════════ */

describe('Absatz', () => {
  it('Normaler Text → <p>', () => {
    const el = render('Das ist ein normaler Satz.')
    const p = first(el, 'p')
    expect(p).not.toBeNull()
    expect(p.textContent).toBe('Das ist ein normaler Satz.')
  })

  it('Zwei Absätze (Leerzeile dazwischen) → zwei <p>', () => {
    const el = render('Erster Absatz.\n\nZweiter Absatz.')
    const ps = el.querySelectorAll('p')
    expect(ps.length).toBe(2)
  })

  it('Mehrere Zeilen ohne Leerzeile → ein <p> mit <br>', () => {
    const el = render('Zeile 1\nZeile 2\nZeile 3')
    const p = first(el, 'p')
    expect(p).not.toBeNull()
    const brs = p.querySelectorAll('br')
    expect(brs.length).toBe(2)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   11. Block-Wechsel (P → UL → P)
══════════════════════════════════════════════════════════════════════ */

describe('Block-Wechsel', () => {
  it('Absatz gefolgt von Liste → zwei separate Elemente', () => {
    const el = render('Einleitungstext\n\n- Punkt 1\n- Punkt 2\n\nAbschlusstext')
    const p = el.querySelectorAll('p')
    const ul = el.querySelectorAll('ul')
    expect(p.length).toBe(2)
    expect(ul.length).toBe(1)
  })

  it('Trennlinie zwischen zwei Absätzen', () => {
    const el = render('Oben\n\n---\n\nUnten')
    expect(first(el, 'hr')).not.toBeNull()
    expect(el.querySelectorAll('p').length).toBe(2)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   12. Sicherheit: XSS-Prävention (ADR-0010 Sicherheitsansatz)
══════════════════════════════════════════════════════════════════════ */

describe('XSS-Sicherheit', () => {
  it('<script>-Tag wird nicht ausgeführt — textContent bleibt literal', () => {
    const el = render('<script>alert(1)</script>')
    // Der Parser erzeugt DOM via createElement/textContent — kein script-Element
    expect(el.querySelector('script')).toBeNull()
    // Der Text kann als Textknoten erscheinen, aber kein ausführbares Script
  })

  it('<img onerror>-Angriff → kein img-Element im DOM', () => {
    const el = render('<img src=x onerror="alert(1)">')
    expect(el.querySelector('img')).toBeNull()
  })

  it('Inline-Code schützt Sonderzeichen: <b>-Tag bleibt als Text', () => {
    const el = render('Hier: `<b>test</b>`')
    const code = first(el, 'code')
    expect(code).not.toBeNull()
    expect(code.textContent).toBe('<b>test</b>')
    expect(code.querySelector('b')).toBeNull()
  })

  it('Codeblock schützt HTML-Markup', () => {
    const el = render('```\n<script>alert("xss")</script>\n```')
    const code = el.querySelector('pre code')
    expect(code.textContent).toContain('<script>')
    expect(el.querySelector('script')).toBeNull()
  })

  /* F-241 (Sprint 28): Karte #172 hat Markdown.render auf Comment-Eingaben
     erweitert. Diese Tests stellen sicher, dass typische XSS-Vektoren auch
     für die zweite Eingabe-Surface (Kommentare) wirkungslos bleiben. */
  it('F-241: javascript:-Link im Comment-Kontext wird neutralisiert', () => {
    const el = render('Klick: [hier](javascript:alert(1))')
    const a = el.querySelector('a')
    /* Der Parser erzeugt entweder gar kein href oder ein safe-Href. */
    if (a) {
      const href = a.getAttribute('href') || ''
      expect(href.toLowerCase().startsWith('javascript:')).toBe(false)
    }
  })

  it('F-241: data:-text/html-Link wird neutralisiert', () => {
    const el = render('[böse](data:text/html,<script>alert(1)</script>)')
    const a = el.querySelector('a')
    if (a) {
      const href = a.getAttribute('href') || ''
      expect(href.toLowerCase().startsWith('data:text/html')).toBe(false)
    }
  })

  it('F-241: on*-Attribut-Versuch im rohen HTML wird ignoriert', () => {
    const el = render('<div onclick="alert(1)">test</div>')
    /* Roh-HTML wird nicht als Element gerendert. */
    expect(el.querySelector('div[onclick]')).toBeNull()
  })

  it('F-241: <iframe>-Versuch erzeugt kein iframe-Element', () => {
    const el = render('<iframe src="evil.html"></iframe>')
    expect(el.querySelector('iframe')).toBeNull()
  })
})

/* ══════════════════════════════════════════════════════════════════════
   13. Tokenizer-API (Markdown.tokenizeBlocks)
══════════════════════════════════════════════════════════════════════ */

describe('tokenizeBlocks', () => {
  it('Überschrift wird als { type: "heading", level: 1 } tokenisiert', () => {
    const blocks = Markdown.tokenizeBlocks('# Titel')
    expect(blocks.length).toBe(1)
    expect(blocks[0].type).toBe('heading')
    expect(blocks[0].level).toBe(1)
    expect(blocks[0].text).toBe('Titel')
  })

  it('Checkliste wird als { type: "checklist" } tokenisiert', () => {
    const blocks = Markdown.tokenizeBlocks('- [x] Fertig\n- [ ] Offen')
    expect(blocks.length).toBe(1)
    expect(blocks[0].type).toBe('checklist')
    expect(blocks[0].items.length).toBe(2)
    expect(blocks[0].items[0].checked).toBe(true)
    expect(blocks[0].items[1].checked).toBe(false)
  })

  it('HR wird als { type: "hr" } tokenisiert', () => {
    const blocks = Markdown.tokenizeBlocks('---')
    expect(blocks[0].type).toBe('hr')
  })

  it('Leerzeilen werden übersprungen', () => {
    const blocks = Markdown.tokenizeBlocks('\n\n\nText\n\n')
    expect(blocks.length).toBe(1)
    expect(blocks[0].type).toBe('paragraph')
  })
})

/* ══════════════════════════════════════════════════════════════════════
   14. checklistStats
══════════════════════════════════════════════════════════════════════ */

describe('checklistStats', () => {
  it('Ohne Checkliste → null', () => {
    expect(Markdown.checklistStats('Normaler Text')).toBeNull()
  })

  it('null-Input → null', () => {
    expect(Markdown.checklistStats(null)).toBeNull()
  })

  it('2 von 3 erledigt → { total: 3, done: 2 }', () => {
    const stats = Markdown.checklistStats('- [x] A\n- [x] B\n- [ ] C')
    expect(stats).not.toBeNull()
    expect(stats.total).toBe(3)
    expect(stats.done).toBe(2)
  })

  it('Alle erledigt → done === total', () => {
    const stats = Markdown.checklistStats('- [x] A\n- [x] B')
    expect(stats.done).toBe(stats.total)
  })

  it('Groß-/Kleinschreibung bei [X] wird akzeptiert', () => {
    const stats = Markdown.checklistStats('- [X] Großes X\n- [ ] Klein')
    expect(stats.done).toBe(1)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   15. Edge-Cases
══════════════════════════════════════════════════════════════════════ */

describe('Edge-Cases', () => {
  it('Checkliste direkt nach Überschrift (kein Leerzeile dazwischen)', () => {
    const el = render('# Aufgaben\n- [ ] Eins\n- [x] Zwei')
    expect(first(el, 'h1')).not.toBeNull()
    expect(first(el, 'ul.md-checklist')).not.toBeNull()
  })

  it('Leerer Codeblock → <pre><code> mit leerem Inhalt', () => {
    const el = render('```\n```')
    const code = el.querySelector('pre code')
    expect(code).not.toBeNull()
    expect(code.textContent).toBe('')
  })

  it.skip('Verschachteltes Inline: **fett und *kursiv* drin** — Parser unterstützt keine Verschachtelung', () => {
    // BEKANNTE EINSCHRÄNKUNG: Der INLINE_RE ist nicht rekursiv.
    // **fett *kursiv* fett** wird nicht korrekt als strong{text+em+text} gerendert.
    // Stattdessen wird entweder gar nichts oder nur der äußere Token erkannt.
    const el = render('**fett *kursiv* text**')
    const strong = first(el, 'strong')
    expect(strong).not.toBeNull()
    const em = strong.querySelector('em')
    expect(em).not.toBeNull()
  })

  it('Link-Text darf Sonderzeichen enthalten', () => {
    const el = render('[Heise & Co.](https://heise.de)')
    const a = first(el, 'a')
    expect(a).not.toBeNull()
    expect(a.textContent).toBe('Heise & Co.')
  })

  it('render() gibt immer ein HTMLElement zurück (niemals null/string)', () => {
    const resultEmpty = render('')
    const resultText = render('Hallo')
    expect(resultEmpty instanceof HTMLElement).toBe(true)
    expect(resultText instanceof HTMLElement).toBe(true)
  })
})

/* ══════════════════════════════════════════════════════════════════════
   GFM-Pipe-Tabellen (#291)
══════════════════════════════════════════════════════════════════════ */

describe('GFM-Pipe-Tabellen (#291)', () => {
  it('einfache Tabelle: Header + 2 Zeilen', () => {
    const el = render('| A | B |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |')
    const table = el.querySelector('table.md-table')
    expect(table).not.toBeNull()
    expect(table.querySelectorAll('thead th')).toHaveLength(2)
    expect(table.querySelectorAll('tbody tr')).toHaveLength(2)
    const cells = table.querySelectorAll('tbody td')
    expect(cells[0].textContent).toBe('1')
    expect(cells[3].textContent).toBe('4')
  })

  it('Header allein (ohne Body) wird gerendert', () => {
    const el = render('| A | B |\n|---|---|')
    const table = el.querySelector('table.md-table')
    expect(table).not.toBeNull()
    expect(table.querySelectorAll('thead th')).toHaveLength(2)
    expect(table.querySelector('tbody')).toBeNull()
  })

  it('Alignment: :--- = left, ---: = right, :---: = center, --- = default', () => {
    const el = render('| L | C | R | D |\n|:---|:---:|---:|---|\n| a | b | c | d |')
    const ths = el.querySelectorAll('thead th')
    expect(ths[0].style.textAlign).toBe('left')
    expect(ths[1].style.textAlign).toBe('center')
    expect(ths[2].style.textAlign).toBe('right')
    expect(ths[3].style.textAlign).toBe('')
    const tds = el.querySelectorAll('tbody td')
    expect(tds[0].style.textAlign).toBe('left')
    expect(tds[1].style.textAlign).toBe('center')
    expect(tds[2].style.textAlign).toBe('right')
    expect(tds[3].style.textAlign).toBe('')
  })

  it('Inline-Markdown in Zellen wird geparst (bold, code, link)', () => {
    const el = render('| Stil | Code |\n|---|---|\n| **fett** | `x()` |\n| [Link](https://x) | normal |')
    expect(el.querySelector('tbody td strong')?.textContent).toBe('fett')
    expect(el.querySelector('tbody td code')?.textContent).toBe('x()')
    expect(el.querySelector('tbody td a')?.getAttribute('href')).toBe('https://x')
  })

  it('Tabelle ohne führende/trailing Pipes', () => {
    const el = render('A | B\n---|---\n1 | 2')
    const table = el.querySelector('table.md-table')
    expect(table).not.toBeNull()
    expect(table.querySelectorAll('thead th')).toHaveLength(2)
    expect(table.querySelector('tbody td').textContent).toBe('1')
  })

  it('Tabelle endet bei Leerzeile, Folgeblock wird separat', () => {
    const el = render('| A | B |\n|---|---|\n| 1 | 2 |\n\nNächster Absatz')
    expect(el.querySelector('table.md-table')).not.toBeNull()
    const ps = el.querySelectorAll('p')
    expect(ps[ps.length - 1].textContent).toBe('Nächster Absatz')
  })

  it('Tabelle direkt nach Absatz (kein Absatz-Suffix)', () => {
    const el = render('Voriger Absatz\n| A | B |\n|---|---|\n| 1 | 2 |')
    const p = el.querySelector('p')
    expect(p.textContent).toBe('Voriger Absatz')
    expect(el.querySelector('table.md-table')).not.toBeNull()
  })

  it('zu wenig Trennstriche (--) → keine Tabelle, bleibt Paragraph', () => {
    const el = render('| A | B |\n|--|--|\n| 1 | 2 |')
    expect(el.querySelector('table.md-table')).toBeNull()
    expect(el.querySelector('p')).not.toBeNull()
  })

  it('Mismatch Header-/Separator-Spaltenzahl → keine Tabelle', () => {
    const el = render('| A | B | C |\n|---|---|\n| 1 | 2 | 3 |')
    expect(el.querySelector('table.md-table')).toBeNull()
    expect(el.querySelector('p')).not.toBeNull()
  })

  it('Body-Zeile mit weniger Zellen wird mit Leer-Zellen aufgefüllt', () => {
    const el = render('| A | B | C |\n|---|---|---|\n| 1 | 2 |')
    const tds = el.querySelectorAll('tbody td')
    expect(tds).toHaveLength(3)
    expect(tds[2].textContent).toBe('')
  })

  it('Body-Zeile mit zu vielen Zellen wird gekürzt', () => {
    const el = render('| A | B |\n|---|---|\n| 1 | 2 | 3 |')
    const tds = el.querySelectorAll('tbody td')
    expect(tds).toHaveLength(2)
  })

  it('CSV-Code-Block-Tabelle bleibt funktional (Regression ADR-0010)', () => {
    const el = render('```csv\nA,B\n1,2\n```')
    expect(el.querySelector('table.md-table')).not.toBeNull()
  })
})
