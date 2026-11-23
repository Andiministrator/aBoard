// Tests für die Schranke gegen externe Bild-Ziele (F-856 Teil 2, ADR-0122).
//
// Der Kern der Zusage: Beim Rendern wird KEIN Bild von einem fremden Host
// geladen — egal über welchen der beiden Wege es in den Text kam
// (Markdown-Bild-Inline `![](…)` oder HTML-Passthrough `<img src>`), und egal
// welcher Aufrufer rendert. Ein Test, der nur einen der beiden Wege prüft,
// deckt die Hälfte ab, in der der Fehler dann wiederkommt.
import { beforeEach, afterEach } from 'vitest'
import { Markdown } from '../src/markdown.js'

function render(text) {
  return Markdown.render(text)
}

/** Alle tatsächlich gesetzten img-Quellen im gerenderten Baum. */
function bildQuellen(el) {
  return Array.from(el.querySelectorAll('img'))
    .map(i => i.getAttribute('src'))
    .filter(Boolean)
}

describe('Externe Bild-Ziele — Markdown-Weg', () => {
  it('![](https://fremd/…) lädt nicht, sondern zeigt einen Platzhalter', () => {
    const el = render('![Diagramm](https://fremder-host.example/x.png?d=geheim)')
    expect(bildQuellen(el)).toEqual([])
    const platz = el.querySelector('a.md-ext-img')
    expect(platz).not.toBeNull()
    // Der Host ist die Information, an der der Nutzer entscheidet.
    expect(platz.textContent).toContain('fremder-host.example')
  })

  it('das Ziel bleibt am Platzhalter erhalten — nichts geht verloren', () => {
    const url = 'https://fremder-host.example/x.png?d=geheim'
    const platz = render(`![](${url})`).querySelector('a.md-ext-img')
    expect(platz.dataset.externalSrc).toBe(url)
    expect(platz.title).toBe(url)
  })

  it('ein Klick lädt das Bild bewusst nach — die Entscheidung liegt beim Betrachter', () => {
    const url = 'https://fremder-host.example/x.png'
    const el = render(`![Alt-Text](${url})`)
    el.querySelector('a.md-ext-img').click()
    const img = el.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.getAttribute('src')).toBe(url)
    expect(img.getAttribute('alt')).toBe('Alt-Text')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
  })

  it('http: (ohne TLS) wird genauso behandelt wie https:', () => {
    const el = render('![](http://fremder-host.example/x.png)')
    expect(bildQuellen(el)).toEqual([])
    expect(el.querySelector('a.md-ext-img')).not.toBeNull()
  })
})

// Hinweis: `<img>` ist ein INLINE-Tag — ein nacktes <img> in einer eigenen
// Zeile wird vom Block-Tokenizer nicht als HTML-Block erkannt. Die Tests
// wrappen es deshalb in <p>, so wie es in echtem Karten-HTML auch steht.
describe('Externe Bild-Ziele — HTML-Weg (der zweite Aufrufer derselben Lücke)', () => {
  it('<img src="https://fremd/…"> lädt nicht, sondern wird zum Platzhalter', () => {
    const el = render('<p><img src="https://fremder-host.example/pixel.gif" alt="x"></p>')
    expect(bildQuellen(el)).toEqual([])
    expect(el.querySelector('a.md-ext-img')).not.toBeNull()
  })

  it('protokoll-relatives //host/x — von isSafeUrl durchgelassen, hier gefangen', () => {
    const el = render('<p><img src="//fremder-host.example/pixel.gif"></p>')
    expect(bildQuellen(el)).toEqual([])
    expect(el.querySelector('a.md-ext-img')).not.toBeNull()
  })
})

describe('Externe Bild-Ziele — was weiterhin lädt', () => {
  it('data:image bleibt unangetastet — eingebettet, kein Netzwerkzugriff', () => {
    const daten = 'data:image/png;base64,iVBORw0KGgo='
    const el = render(`![](${daten})`)
    // Der Markdown-Weg setzt data: nie als src (Bestandsverhalten), aber er
    // darf auch keinen Platzhalter erzeugen — es ist kein externes Ziel.
    expect(el.querySelector('a.md-ext-img')).toBeNull()
  })

  it('data:image über den HTML-Weg lädt weiter', () => {
    const el = render('<p><img src="data:image/png;base64,iVBORw0KGgo="></p>')
    expect(bildQuellen(el)).toEqual(['data:image/png;base64,iVBORw0KGgo='])
    expect(el.querySelector('a.md-ext-img')).toBeNull()
  })

  it('attachment:// bleibt der Anhang-Pfad und wird kein Platzhalter', () => {
    const el = render('![](attachment://11111111-2222-3333-4444-555555555555)')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    const img = el.querySelector('img')
    expect(img?.dataset.attachmentId).toBe('11111111-2222-3333-4444-555555555555')
  })

  it('relative Pfade sind kein fremder Host', () => {
    const el = render('<p><img src="/assets/logo.png"></p>')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    expect(bildQuellen(el)).toEqual(['/assets/logo.png'])
  })

  it('gleicher Origin lädt weiter — der eigene Server ist nicht „extern"', () => {
    const el = render(`<p><img src="${location.origin}/assets/logo.png"></p>`)
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    expect(bildQuellen(el).length).toBe(1)
  })
})

describe('Externe Bild-Ziele — die Zusage gilt für JEDEN Aufrufer', () => {
  // Der eigentliche Punkt der globalen Lösung: Es gibt keinen Renderpfad, der
  // die Schranke umgeht, weil sie nicht am Aufrufer hängt. Geprüft am
  // Textinhalt, wie er aus den drei Quellen kommt, die F-856 nennt.
  it.each([
    ['LLM-Antworttext (Stream)', 'Hier das Ergebnis:\n\n![](https://fremder-host.example/a?d=1)'],
    ['persistierter Kommentar',  '![](https://fremder-host.example/b?d=2)'],
    ['Mail-Ingest-Karteninhalt', '<p>Guten Tag<img src="https://fremder-host.example/c?d=3" width="1" height="1"></p>'],
  ])('%s lädt nicht von selbst', (_name, text) => {
    const el = render(text)
    expect(bildQuellen(el)).toEqual([])
    expect(el.querySelector('a.md-ext-img')).not.toBeNull()
  })
})


describe('Externe Bild-Ziele — der Platzhalter überlebt die Export-Serialisierung', () => {
  // `src/export.js` serialisiert das gerenderte DOM mit `outerHTML` in ein
  // neues Fenster. Event-Listener gehen dabei verloren. Ein <button> wäre dort
  // tot und verspräche „Klicken zum Laden", ohne etwas zu tun — vor der
  // Schranke stand im Ausdruck immerhin das Bild. Ein <a href> bleibt auch
  // ohne Handler benutzbar.
  it('der Platzhalter ist ein Link auf das Ziel, nicht nur ein Button', () => {
    const url = 'https://fremder-host.example/x.png'
    const el = render(`![](${url})`)
    const platz = el.querySelector('a.md-ext-img')
    expect(platz).not.toBeNull()
    expect(platz.tagName).toBe('A')
    expect(platz.getAttribute('href')).toBe(url)
    expect(platz.getAttribute('rel')).toContain('noopener')
  })

  it('nach der Serialisierung bleibt das Ziel erreichbar', () => {
    const url = 'https://fremder-host.example/x.png'
    const html = render(`![](${url})`).outerHTML
    expect(html).toContain(`href="${url}"`)
    // Und kein <img src> — der Sinn der Schranke bleibt auch im Export erhalten.
    expect(html).not.toContain(`<img src="${url}"`)
  })

  it('in der App navigiert der Klick NICHT, sondern lädt das Bild', () => {
    const url = 'https://fremder-host.example/x.png'
    const el = render(`![Alt](${url})`)
    const platz = el.querySelector('a.md-ext-img')
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true })
    platz.dispatchEvent(ev)
    expect(ev.defaultPrevented, 'ohne preventDefault navigiert der Klick weg').toBe(true)
    expect(el.querySelector('img')?.getAttribute('src')).toBe(url)
  })
})

describe('Externe Bild-Ziele — die Schranke hängt am HOST, nicht am Protokoll', () => {
  // Der Anlassfall: `aboard.html` ist zum Doppelklick gedacht, läuft also unter
  // `file://`. Dort ergibt `new URL('//fremd/x', 'file:///…')` das Protokoll
  // `file:` mit Host `fremd` — ein Protokoll-Test auf http/https lässt das
  // durch, obwohl das Ziel auf einem fremden Rechner liegt.
  //
  // Der Test ruft `istExternesBildZiel` nicht direkt auf (die Funktion ist
  // modul-privat) — er stellt die Basis um, unter der der Renderer arbeitet.
  let baseEl
  beforeEach(() => {
    baseEl = document.createElement('base')
    baseEl.href = 'file:///home/nutzer/aboard.html'
    document.head.appendChild(baseEl)
  })
  afterEach(() => baseEl?.remove())

  it('//fremder-host/x wird unter file://-Basis als extern erkannt', () => {
    expect(document.baseURI.startsWith('file:'),
      'Vorbedingung verletzt: die Basis ist nicht file:, der Test prüft dann nichts').toBe(true)
    const el = render('<p><img src="//fremder-host.example/pixel.gif"></p>')
    expect(bildQuellen(el)).toEqual([])
    expect(el.querySelector('a.md-ext-img')).not.toBeNull()
  })

  it('ein lokaler Pfad bleibt unter file://-Basis unangetastet', () => {
    const el = render('<p><img src="/home/nutzer/bild.png"></p>')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    expect(bildQuellen(el)).toEqual(['/home/nutzer/bild.png'])
  })

  it('attachment:// bleibt lokal — die UUID darf nicht als fremder Host gelten', () => {
    // Ohne den expliziten Schema-Ausschluss parst `new URL` die UUID als host,
    // und ausgerechnet der Anhang-Pfad würde zum Platzhalter.
    const el = render('![](attachment://11111111-2222-3333-4444-555555555555)')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    expect(el.querySelector('img')?.dataset.attachmentId)
      .toBe('11111111-2222-3333-4444-555555555555')
  })
})

describe('Externe Bild-Ziele — kein anklickbarer Link auf ein nicht-ladbares Schema', () => {
  /* Der Befund entstand aus ZWEI Fixes, die einzeln richtig waren: der
     Umstellung der Schranke vom Protokoll auf den Host, und dem Wechsel des
     Platzhalters von <button> zu <a href>. `new URL` parst die Autorität jedes
     Schemas mit `//` — `javascript://evil/x` hatte damit einen Host, galt als
     „externes Bild-Ziel" und landete mitsamt Schema im href.

     WAS HIER GEPRÜFT WIRD UND WAS NICHT — die erste Fassung dieser Gruppe
     prüfte nichts, und das ist lehrreich genug, um es hinzuschreiben:

     Es gibt jetzt zwei Schranken hintereinander. `istExternesBildZiel` lehnt
     nicht-ladbare Schemata ab (dann entsteht gar kein Platzhalter), und
     `externesBildPlatzhalter` setzt den href nur bei `http(s)`/protokoll-
     relativ. Die erste macht die zweite unerreichbar. Eine Zusicherung der
     Form „falls ein Platzhalter da ist, hat er href='#'" ist deshalb VAKUUM
     WAHR: Sie läuft nie. Beide Gegenproben — jede Schranke einzeln zurück-
     gebaut — blieben grün, weil jeweils die andere den Fall abfing.

     Geprüft wird deshalb hier die WIRKSAME Schranke, ohne `if`. Die zweite
     ist bewusste Tiefenverteidigung für den Fall, dass jemand die erste
     lockert — sie ist per Konstruktion nicht erreichbar und wird nicht
     getestet, sondern im Code begründet. Ein Test, der so tut, als prüfe er
     sie, wäre schlechter als keiner.

     NACHTRAG SPRINT 62 (Kritiker, V2): Es sind inzwischen DREI Schranken —
     und der Absatz darüber hat für zwei davon nicht mehr gestimmt.

     Der Klick-Handler setzte `img.src = src` mit dem ROHEN Wert; ein Ziel,
     dessen `href` defensiv auf `'#'` stand, lud beim Klick trotzdem. Die
     erste Korrektur gab ihm dieselbe Regex wie dem `href` — und die
     Begründung, das sei „aus demselben Grund nicht testbar".

     **Das war falsch, und ein Kritiker hat es gemessen.** Schranke 1
     entscheidet auf dem GEPARSTEN Protokoll (`new URL(...).protocol`), die
     Regex auf dem ROHEN String. Für `https:/host/x.png` (ein Schrägstrich)
     fällt beides auseinander: Der Parser sagt https, die Regex sagt nein.
     Der Fall erreicht also beide hinteren Schranken — sie waren nie
     unerreichbar, und eine Zusicherung darüber wäre nie vakuum wahr gewesen.
     Sichtbar wurde es erst als Schaden: eine tote Attrappe, deren Klick
     nichts tat, wo vorher das Bild lud.

     Behoben, indem alle drei Schranken dieselbe Quelle benutzen
     (`darfGeladenWerden`, geparstes Protokoll). Die Gruppe unten prüft
     genau diese Randfälle — sie ist der Test, den es laut voriger Fassung
     nicht geben konnte. */
  it.each([
    ['javascript', 'javascript://evil.example/%0aalert(1)'],
    ['vbscript',   'vbscript://evil.example/x'],
    ['view-source','view-source://evil.example/x'],
    ['unbekannt',  'foo://evil.example/x'],
  ])('%s:// erzeugt weder Bild noch Platzhalter-Link', (_name, url) => {
    const el = render(`![x](${url})`)
    expect(bildQuellen(el), 'als Bild geladen').toEqual([])
    expect(el.querySelector('a.md-ext-img'),
      'ein Platzhalter entsteht — dann traegt sein href das Schema, und im '
      + 'Export ist der Klick nicht mehr durch preventDefault entschaerft').toBeNull()
    for (const a of el.querySelectorAll('a')) {
      expect(a.getAttribute('href')).not.toMatch(/^(javascript|vbscript|view-source|foo):/i)
    }
  })

  it('http(s) und protokoll-relativ bleiben klickbar — sonst wäre der Fix ein Rückschritt', () => {
    for (const url of ['https://fremd.example/a.png', 'http://fremd.example/a.png']) {
      const platz = render(`![](${url})`).querySelector('a.md-ext-img')
      expect(platz, `kein Platzhalter fuer ${url}`).not.toBeNull()
      expect(platz.getAttribute('href')).toBe(url)
    }
    const rel = render('<p><img src="//fremd.example/a.png"></p>').querySelector('a.md-ext-img')
    expect(rel, 'protokoll-relativ muss weiterhin ein Platzhalter sein').not.toBeNull()
    expect(rel.getAttribute('href')).toBe('//fremd.example/a.png')
  })
})

/* Sprint-62-Kritiker (K2/V2): Die Schreibweisen, an denen roher String und
   geparstes Protokoll auseinanderfallen. Der WHATWG-Parser — und jeder
   Browser — normalisiert sie zu gültigen https-URLs; eine Regex auf dem
   Rohtext tut das nicht.

   Was hier zugesichert wird: Wenn ein Platzhalter entsteht (Schranke 1 hat
   das Ziel also als externes Bild eingestuft), dann ist er auch BENUTZBAR —
   `href` trägt das Ziel, der Klick lädt es. Alles andere wäre eine Attrappe:
   ein Bedienelement, das „Klicken zum Laden" verspricht und nichts tut. */
describe('Externe Bild-Ziele — Platzhalter sind benutzbar, nicht nur da', () => {
  it.each([
    ['ein Schrägstrich',  'https:/fremd.example/a.png'],
    ['gar kein Schrägstrich', 'https:fremd.example/a.png'],
    ['zwei, der Normalfall',  'https://fremd.example/a.png'],
  ])('%s — Platzhalter mit ladbarem href, Klick zeigt das Bild', (_name, url) => {
    const el = render(`![x](${url})`)
    const platz = el.querySelector('a.md-ext-img')
    expect(platz, `kein Platzhalter fuer ${url} — dann prueft der Rest nichts`).not.toBeNull()

    expect(platz.getAttribute('href'),
      `der href steht auf '#', obwohl das Ziel ladbar ist — der Platzhalter ist `
      + `eine tote Attrappe (und im Export ein Link ins Leere)`).toBe(url)

    expect(bildQuellen(el), 'vor dem Klick darf nichts geladen sein').toEqual([])
    platz.click()
    expect(bildQuellen(el), 'nach dem Klick fehlt das Bild — der Klick lief ins Leere')
      .toEqual([url])
  })

  it('ein nicht ladbares Schema bleibt aussen vor — die Lockerung geht nicht zu weit', () => {
    /* Gegenrichtung, ohne die der Test oben durch „immer erlauben" erfuellbar
       waere. `javascript://evil/x` hat fuer `new URL` einen Host, wird aber
       schon von Schranke 1 abgelehnt: kein Platzhalter, kein Bild, kein Link
       mit diesem Schema. */
    const el = render('![x](javascript://evil.example/%0aalert(1))')
    expect(el.querySelector('a.md-ext-img')).toBeNull()
    expect(bildQuellen(el)).toEqual([])
    for (const a of el.querySelectorAll('a')) {
      expect(a.getAttribute('href')).not.toMatch(/^javascript:/i)
    }
  })
})
