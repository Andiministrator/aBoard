// Modus `sicher` des Markdown-Renderers (Karte #739, ADR-0137 §6).
// Zusage: Beim Anzeigen von Wissensseiten lädt NICHTS — kein Bild auf
// keinem der beiden Wege (Markdown-Inline, HTML-Block), kein Anhang, und
// rohes HTML erscheint nur als Text.
import { Markdown } from '../src/markdown.js'

const sicher = (text) => Markdown.render(text, null, { sicher: true })

/* Alles, was beim Einfügen ins DOM eine Ressource anfordern oder Code
   ausführen könnte. */
function ladendeElemente(el) {
  return el.querySelectorAll('img, picture, source, video, audio, iframe, object, embed, link, script, style, svg, [src], [srcset], [style], [class]:not([class=""]):not(.md-seitenlink):not([class^="md-"]), [id]')
}

describe('Modus sicher — Bilder', () => {
  it.each([
    ['extern mit Datenabfluss', '![alt-ext](https://x.example/?d=geheim)'],
    ['gleiche Herkunft', '![alt-same](/boards/b1/attachments/k/a)'],
    ['relativ', '![alt-rel](bild.png)'],
    ['protokoll-relativ', '![alt-pr](//x.example/a.png)'],
    ['Anhang', '![alt-att](attachment://123e4567-e89b-12d3-a456-426614174000)'],
    ['data:', '![alt-data](data:image/png;base64,AAAA)'],
  ])('%s → nur Alt-Text', (_, md) => {
    const el = sicher(md)
    expect(ladendeElemente(el).length).toBe(0)
    expect(el.querySelector('a')).toBeNull()
    expect(el.textContent).toMatch(/^alt-/)
  })

  it('Bild in Checkliste, Überschrift und Zitat → Text', () => {
    const el = sicher('# ![kopf](https://x/a.png)\n\n- [ ] ![haken](https://x/b.png)\n\n> ![zitat](/c.png)')
    expect(ladendeElemente(el).length).toBe(0)
    expect(el.textContent).toContain('kopf')
    expect(el.textContent).toContain('haken')
    expect(el.textContent).toContain('zitat')
  })

  it('Bild in Tabellenzelle und Liste → Text', () => {
    const el = sicher('| a |\n|---|\n| ![zelle](https://x/y) |\n\n- ![liste](/a.png)')
    expect(ladendeElemente(el).length).toBe(0)
    expect(el.textContent).toContain('zelle')
    expect(el.textContent).toContain('liste')
  })

  it('Gegenstück: ohne sicher lädt ein Bild gleicher Herkunft', () => {
    const el = Markdown.render('![x](/a.png)')
    expect(el.querySelector('img')).not.toBeNull()
  })
})

describe('Modus sicher — rohes HTML', () => {
  it.each([
    ['<img>', '<p><img src="https://x.example/p.gif"></p>'],
    ['<a class>', '<div><a class="btn" href="https://x.example">k</a></div>'],
    ['<img same-origin>', '<div><img src="/a.png" alt="a"></div>'],
    ['onerror', '<div><img src=x onerror="alert(1)"></div>'],
  ])('%s → als Text, kein Element', (_, html) => {
    const el = sicher(html)
    expect(ladendeElemente(el).length).toBe(0)
    expect(el.querySelector('a')).toBeNull()
    expect(el.textContent).toContain('<')
  })

  it('Gegenstück: ohne sicher wird HTML als Element eingefügt', () => {
    const el = Markdown.render('<div><a class="btn" href="https://x.example">k</a></div>')
    expect(el.querySelector('a.btn')).not.toBeNull()
  })
})

describe('Modus sicher — Links', () => {
  it('https → Link mit noopener noreferrer, neues Fenster', () => {
    const a = sicher('[doku](https://forge.example/a)').querySelector('a')
    expect(a.getAttribute('href')).toBe('https://forge.example/a')
    expect(a.rel).toBe('noopener noreferrer')
    expect(a.target).toBe('_blank')
  })

  it.each([
    'http://x.example/a', 'mailto:a@b.de', 'javascript:alert(1)', 'JaVaScRiPt:alert(1)',
    '//x.example/a', 'data:text/html,x', '#anker',
  ])('%s → nur Text', (ziel) => {
    const el = sicher(`[text](${ziel})`)
    expect(el.querySelector('a')).toBeNull()
    /* `javascript:alert(1)` endet für den Parser an der ersten `)` — der
       Rest bleibt Text. Entscheidend: kein Link. */
    expect(el.textContent).toMatch(/^text\)?$/)
  })

  it.each([
    ['datei.md', 'datei.md'],
    ['../Datenschutz & Consent/cookies.md', '../Datenschutz & Consent/cookies.md'],
    ['unter/seite.md#abschnitt', 'unter/seite.md#abschnitt'],
    ['/aboard/start.md', '/aboard/start.md'],
  ])('relativ %s → Seitenlink', (ziel, seite) => {
    const a = sicher(`[text](${ziel})`).querySelector('a')
    expect(a.className).toBe('md-seitenlink')
    expect(a.dataset.seite).toBe(seite)
    expect(a.getAttribute('href')).toBe('#')
    expect(a.target).toBe('')
  })

  it('Gegenstück: ohne sicher wird ein relativer Link zu #, ohne Seitenlink', () => {
    const a = Markdown.render('[t](datei.md)').querySelector('a')
    expect(a.getAttribute('href')).toBe('#')
    expect(a.className).toBe('')
  })
})

describe('Modus sicher — Zustand', () => {
  it('der nächste normale Aufruf ist wieder normal', () => {
    sicher('![a](/a.png)')
    expect(Markdown.render('![a](/a.png)').querySelector('img')).not.toBeNull()
  })

  it('auch nach einem Fehler im Rendern', () => {
    const orig = document.createElement
    let n = 0
    document.createElement = function (tag) {
      if (++n === 3) throw new Error('kaputt')
      return orig.call(document, tag)
    }
    try {
      expect(() => sicher('# a\n\nb\n\nc')).toThrow('kaputt')
    } finally {
      document.createElement = orig
    }
    expect(Markdown.render('<div><a class="btn" href="https://x">k</a></div>').querySelector('a.btn')).not.toBeNull()
  })

  it('direkter renderBlock-Aufruf (modal.js) nach abgebrochenem sicher-Rendern ist normal', () => {
    const orig = document.createElement
    let n = 0
    document.createElement = function (tag) {
      if (++n === 3) throw new Error('kaputt')
      return orig.call(document, tag)
    }
    try {
      expect(() => sicher('# a\n\nb\n\nc')).toThrow('kaputt')
    } finally {
      document.createElement = orig
    }
    const ziel = document.createElement('div')
    const [block] = Markdown.tokenizeBlocks('<div><a class="btn" href="https://x">k</a></div>')
    Markdown.renderBlock(block, ziel)
    expect(ziel.querySelector('a.btn')).not.toBeNull()
  })

  it('Struktur bleibt: Überschrift, Liste, Code, Tabelle', () => {
    const el = sicher('# Titel\n\n- a\n- b\n\n```\ncode\n```\n\n| x |\n|---|\n| y |')
    expect(el.querySelector('h1').textContent).toBe('Titel')
    expect(el.querySelectorAll('li').length).toBe(2)
    expect(el.querySelector('pre code').textContent).toBe('code')
    expect(el.querySelector('td').textContent).toBe('y')
  })

  it('Checkliste nicht bedienbar', () => {
    const cb = sicher('- [ ] offen').querySelector('input')
    expect(cb.disabled).toBe(true)
  })
})

/* F-1864 (vorbestehend, beide Modi): `[![alt](bild)](ziel)` wurde zu einem
   Link auf die BILDadresse mit zerbrochenem Text. Jetzt: Link aufs Ziel,
   Alt-Text als Linktext, kein <img> in keinem Modus. */
describe('F-1864 Badge-Muster', () => {
  const BADGE = '[![Status](https://x.example/p.gif)](https://y.example/)'

  it('Link zeigt auf das Ziel, nicht auf das Bild', () => {
    const a = Markdown.render(BADGE).querySelector('a')
    expect(a.getAttribute('href')).toBe('https://y.example/')
    expect(a.textContent).toBe('Status')
  })

  it('Normalmodus: kein <img>, kein Rest-Markdown im Text', () => {
    const el = Markdown.render('vor ' + BADGE + ' nach')
    expect(el.querySelector('img')).toBeNull()
    expect(el.textContent).toBe('vor Status nach')
  })

  it('Modus sicher: Link aufs Ziel, kein <img>', () => {
    const el = sicher(BADGE)
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('a').getAttribute('href')).toBe('https://y.example/')
  })

  it('unsicheres Ziel wird nicht verlinkt (beide Modi)', () => {
    const roh = '[![x](https://x.example/p.gif)](javascript:alert(1))'
    expect(Markdown.render(roh).querySelector('a').getAttribute('href')).toBe('#')
    expect(sicher(roh).querySelector('a')).toBeNull()
  })

  it('leerer Alt-Text: die Zieladresse ist der Linktext', () => {
    const a = Markdown.render('[![](https://x.example/p.gif)](https://y.example/)').querySelector('a')
    expect(a.textContent).toBe('https://y.example/')
  })
})
