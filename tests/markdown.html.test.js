// Tests für HTML-Block-Rendering + Allowlist-Sanitierung in markdown.js
// → Karte #288, ADR-0089. Sicherheits-kritisch: XSS-Vektoren müssen alle weg.
import { Markdown } from '../src/markdown.js'

function render(text) {
  return Markdown.render(text)
}

describe('HTML-Block — Happy-Path (ADR-0089)', () => {
  it('<details>/<summary> wird als Element gerendert, nicht als Text', () => {
    const el = render('<details>\n<summary>Click</summary>\nHidden content\n</details>')
    const det = el.querySelector('details')
    expect(det).not.toBeNull()
    expect(det.querySelector('summary')?.textContent).toBe('Click')
  })

  it('<table> mit colspan/rowspan-Attributen bleibt erhalten', () => {
    const el = render('<table>\n<tr><th colspan="2">Head</th></tr>\n<tr><td>A</td><td>B</td></tr>\n</table>')
    const th = el.querySelector('th')
    expect(th?.getAttribute('colspan')).toBe('2')
    expect(el.querySelectorAll('td')).toHaveLength(2)
  })

  it('<kbd> als Block-Anfang triggert keinen htmlBlock (Inline-Tag, kein Block-Tag)', () => {
    /* <kbd> ist Inline-Allowlist, NICHT Block-Allowlist → bleibt Paragraph-Text.
       Folge-Karte (Inline-HTML) würde das verbessern. */
    const el = render('<kbd>Strg+C</kbd>')
    expect(el.querySelector('kbd')).toBeNull()
    expect(el.querySelector('p')).not.toBeNull()
  })

  it('Block-Tags am Anfang einer Zeile beenden den vorigen Absatz', () => {
    const el = render('Erster Absatz\n<div>HTML-Block</div>\nweiter im HTML')
    const p = el.querySelector('p')
    expect(p?.textContent).toBe('Erster Absatz')
    expect(el.querySelector('div')).not.toBeNull()
  })
})

describe('Sanitierung — XSS-Vektoren werden geblockt', () => {
  it('<script>-Tag wird komplett entfernt (Tag-Block-Allowlist verbietet ihn)', () => {
    /* <script> ist nicht in HTML_BLOCK_TAGS → der Block startet gar nicht,
       der Tokenizer liest <script> als Paragraph-Text (escaped via textContent). */
    const el = render('<script>alert(1)</script>')
    expect(el.querySelector('script')).toBeNull()
    expect(el.textContent).toContain('<script>')
  })

  it('<script> innerhalb eines erlaubten Block-Tags wird vom Sanitizer entfernt', () => {
    const el = render('<div>\nbefore <script>alert(1)</script> after\n</div>')
    expect(el.querySelector('script')).toBeNull()
    /* Inhalt darf erhalten bleiben (replaceWith textContent), aber kein Eval. */
    expect(el.querySelector('div')).not.toBeNull()
  })

  it('<iframe> wird zu Text herabgestuft', () => {
    const el = render('<div>\n<iframe src="evil"></iframe>\n</div>')
    expect(el.querySelector('iframe')).toBeNull()
    expect(el.querySelector('div')).not.toBeNull()
  })

  it('<svg> wird vollständig geblockt (foreignObject-Bypass-Risiko)', () => {
    const el = render('<div>\n<svg><foreignObject><script>x</script></foreignObject></svg>\n</div>')
    expect(el.querySelector('svg')).toBeNull()
    expect(el.querySelector('foreignobject')).toBeNull()
    expect(el.querySelector('script')).toBeNull()
  })

  it('onerror/onclick-Handler werden entfernt', () => {
    const el = render('<div onclick="alert(1)" onerror="bad()">\nx\n</div>')
    const div = el.querySelector('div')
    expect(div).not.toBeNull()
    expect(div.hasAttribute('onclick')).toBe(false)
    expect(div.hasAttribute('onerror')).toBe(false)
  })

  it('javascript: in href wird entfernt, Element bleibt aber als Container', () => {
    const el = render('<p>\n<a href="javascript:alert(1)">Click</a>\n</p>')
    const a = el.querySelector('a')
    expect(a).not.toBeNull()
    expect(a.hasAttribute('href')).toBe(false)
    expect(a.textContent).toBe('Click')
  })

  it('vbscript: + data:text/html in href werden entfernt', () => {
    const el1 = render('<p><a href="vbscript:msgbox(1)">x</a></p>')
    expect(el1.querySelector('a')?.hasAttribute('href')).toBe(false)
    const el2 = render('<p><a href="data:text/html,<script>x</script>">x</a></p>')
    expect(el2.querySelector('a')?.hasAttribute('href')).toBe(false)
  })

  it('http:/https:/mailto:/#fragment in href bleiben erhalten', () => {
    const el = render(
      '<p>\n<a href="https://example.org">A</a>\n<a href="mailto:x@y.z">B</a>\n<a href="#frag">C</a>\n</p>',
    )
    const links = el.querySelectorAll('a')
    expect(links[0]?.getAttribute('href')).toBe('https://example.org')
    expect(links[1]?.getAttribute('href')).toBe('mailto:x@y.z')
    expect(links[2]?.getAttribute('href')).toBe('#frag')
  })

  it('target=_blank erzwingt rel=noopener noreferrer', () => {
    const el = render('<p>\n<a href="https://example.org" target="_blank">x</a>\n</p>')
    const a = el.querySelector('a')
    expect(a?.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('img src=data:image/png ist erlaubt, data:image/svg+xml nicht', () => {
    const okSrc = 'data:image/png;base64,iVBORw0KG'
    const badSrc = 'data:image/svg+xml,<svg></svg>'
    const el1 = render(`<p><img src="${okSrc}"></p>`)
    expect(el1.querySelector('img')?.getAttribute('src')).toBe(okSrc)
    const el2 = render(`<p><img src="${badSrc}"></p>`)
    expect(el2.querySelector('img')?.hasAttribute('src')).toBe(false)
  })

  it('img src=javascript: wird entfernt', () => {
    const el = render('<p><img src="javascript:alert(1)"></p>')
    expect(el.querySelector('img')?.hasAttribute('src')).toBe(false)
  })

  it('Nicht-Allow-Attribute (z. B. style) werden entfernt', () => {
    const el = render('<div style="background:url(javascript:x)" data-foo="bar">\nx\n</div>')
    const div = el.querySelector('div')
    expect(div?.hasAttribute('style')).toBe(false)
    expect(div?.hasAttribute('data-foo')).toBe(false)
  })

  it('class/id/title/lang/dir bleiben universell erlaubt', () => {
    const el = render('<div class="x" id="y" title="t" lang="de" dir="ltr">\nq\n</div>')
    const div = el.querySelector('div')
    expect(div?.getAttribute('class')).toBe('x')
    expect(div?.getAttribute('id')).toBe('y')
    expect(div?.getAttribute('title')).toBe('t')
    expect(div?.getAttribute('lang')).toBe('de')
    expect(div?.getAttribute('dir')).toBe('ltr')
  })

  it('Mixed: erlaubte + verbotene Tags im selben Block', () => {
    const el = render('<section>\n<h2>Titel</h2>\n<form><input value="x"></form>\n<p>OK</p>\n</section>')
    expect(el.querySelector('section')).not.toBeNull()
    expect(el.querySelector('h2')?.textContent).toBe('Titel')
    expect(el.querySelector('form')).toBeNull()
    expect(el.querySelector('input')).toBeNull()
    expect(el.querySelector('p')?.textContent).toBe('OK')
  })
})

describe('Regression — bestehende Markdown-Konstrukte bleiben unverändert', () => {
  it('einfacher Absatz ohne HTML wird weiterhin als <p> gerendert', () => {
    const el = render('Hallo **Welt**')
    expect(el.querySelector('p')).not.toBeNull()
    expect(el.querySelector('strong')?.textContent).toBe('Welt')
  })

  it('Markdown-Tabelle (csv-Code-Block) bleibt unberührt', () => {
    const el = render('```csv\nA,B\n1,2\n```')
    expect(el.querySelector('table.md-table')).not.toBeNull()
  })

  it('Checkliste wird weiterhin gerendert', () => {
    const el = render('- [x] done\n- [ ] open')
    expect(el.querySelectorAll('input[type=checkbox]')).toHaveLength(2)
  })
})
