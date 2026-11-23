/* @vitest-environment node */

/**
 * F-439 (Karte #361): Jede injizierte CSS-Regel mit einer fest codierten
 * (nicht var()-basierten) Fläche (`background`/`background-color`) muss eine
 * eigene `color`-Deklaration tragen.
 *
 * Hintergrund: 17 Regeln in den per JS injizierten Stilen (`_ensureXStyles()`-
 * Slim-Pattern, #286) setzten eine annähernd weiße Fläche ohne eigene `color`
 * — sie erbten `var(--text)`, das im Dark-Theme fast identisch mit der Fläche
 * ist (Kontrast 1,01–1,14:1). Betroffen waren u.a. die LLM-Konversations-Turns,
 * alle vier Mutations-Zustände und sechs PII-Regeln (`src/llm-tab.js`,
 * `src/llm-pii.js`).
 *
 * `tests/css-vars.test.js` (F-436) fängt undefinierte `var()`-Referenzen ab.
 * Das hier ist die Schwester-Klasse: die referenzierte Variable existiert,
 * aber die Fläche bekommt trotzdem nie eine passende Textfarbe. Dieser Test
 * schliesst diese Lücke — im selben Stil wie css-vars.test.js.
 *
 * Bewusst NICHT geprüft (Fehlalarm-Vermeidung):
 *  - `background: transparent|none|inherit|initial|unset` — keine Fläche.
 *  - `background: var(...)` — folgt dem Theme, gepaart mit vererbtem
 *    `var(--text)`, das für genau dieses Flächenpaar gilt.
 *  - `color: <irgendein Wert>` (auch `inherit`) — Präsenz genügt, der Wert
 *    wird nicht geprüft (Wert-Sinnhaftigkeit ist Reviewer-Sache).
 *  - `rgba()`/`rgb()` mit alpha < 1 — Overlay-Backdrops (z.B. `rgba(0,0,0,.45)`
 *    hinter einem Dialog). Kein Text sitzt direkt auf dem Backdrop, der
 *    eigentliche Dialoginhalt hat eine eigene opake `var(--surface)`-Fläche.
 *    Empirisch verifiziert (2026-08-17): jede rgba/rgb-Fläche im gesamten
 *    `src/` folgt exakt diesem Muster (calendar-view.js, clients-view.js,
 *    llm-pii.js, llm-tab.js, modal.js, projects-view.js, sidebar.js).
 *  - Feste Fläche OHNE eigene `color`, aber im selben injizierten CSS-Block
 *    liegt eine Regel, die sowohl eine feste Fläche als auch eine eigene
 *    `color` setzt (F-463, siehe `ermittleAmbientFarbe()` weiter unten) — der
 *    Wurzel-Wrapper des Dialogs (`.admin-panel`, `.email-panel`, `.llm-panel`
 *    u.ä.) vererbt seine `color` DOM-seitig an alles darunter, auch ohne
 *    CSS-Selektor-Verschachtelung.
 *
 * Korpus (F-458/F-463, Karte #401): Der alte Extraktor matchte NUR
 * `<el>.textContent = '...';` — einfach gequotete Einzeiler-Direktzuweisungen.
 * Damit blieben vier von zwölf CSS-injizierenden Modulen unsichtbar:
 * admin-dialog.js, email-dialog.js, llm-keys-dialog.js (Zwischenvariable:
 * `const css = \`…\`; style.textContent = css;`) und notifications-dialog.js
 * (Template-Literal als Direktzuweisung). `extrahiereCssStrings()` deckt jetzt
 * beide Formen ab (siehe dortiger Kommentar für die genauen Grenzen der
 * Zwischenvariable-Auflösung) — der Korpus ist seither vollständig, nicht nur
 * "die meisten Dateien". Die Selbsttest-Schwelle unten reißt, sobald ein
 * Modul wieder aus dem Korpus fällt (siehe Kommentar dort).
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const SRC = new URL('../src/', import.meta.url).pathname

/* Ermittelt die Bezeichner aller `<style>`-Elemente in einer Quelldatei
   (F-623b, Karte #527): `const X = document.createElement('style')` (auch
   `let`/`var`). NAMENTLICHE Auflösung über die tatsächliche Erzeugung, keine
   Namenskonvention — im Korpus heißen diese Variablen uneinheitlich `style`,
   `st` oder `s` (grep-Befund Karte #527), eine feste Namensliste wäre also
   sowohl zu eng (verpasst neue Namen) als auch zu weit (ein generisches `s`
   wird anderswo im selben Modul für ganz andere DOM-Knoten wiederverwendet,
   siehe renderer.js `s.textContent = entry.snippet || ''` — dort aber ohne
   String-Literal auf der rechten Seite, siehe reDirekt unten). */
function ermittleStyleElementVariablen(quelle) {
  const namen = new Set()
  const re = /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*document\.createElement\(\s*['"]style['"]\s*\)/g
  let m
  while ((m = re.exec(quelle))) namen.add(m[1])
  return namen
}

/* Extrahiert injizierte CSS-Strings aus zwei Mustern, die im Slim-CSS-via-
   JS-Inject-Pattern (`_ensureXStyles()`, #286) vorkommen:

   1. Direktzuweisung:  <styleEl>.textContent = '...' | "..." | `...`;
      Template-Literale dürfen mehrzeilig sein — ' und " bleiben einzeilig
      (ein echter Zeilenumbruch darin wäre ohnehin kein gültiges JS).
      F-623b/Karte #527: Der Empfänger VOR `.textContent` muss ein per
      `ermittleStyleElementVariablen()` erkanntes `<style>`-Element sein.
      Vorher matchte dieses Muster JEDE `.textContent = '…'`-Zuweisung im
      gesamten `src/` — belegt durch `src/sync.js`, dessen Massenlösch-
      Warndialog (`msg.textContent = `Du bist dabei, …``, kein
      Style-Element) so als Pseudo-„CSS-Regel" durchrutschte. Die Einteilung
      passiert PRO DATEI: derselbe Bezeichner (z.B. `s`) kann in einer Datei
      sowohl das Style-Element als auch — in anderer Funktion — einen
      beliebigen anderen DOM-Knoten benennen; das ist unscharf, aber jede der
      zwölf bekannten CSS-injizierenden Dateien verwendet ihren Style-
      Bezeichner nachweislich nur genau einmal für `.textContent =`
      (nachgeprüft, Karte #527) — ein Modul, das das künftig ändert, reißt
      die Korpus-Selbsttests unten.

   2. Zwischenvariable:  const css = `…`; …  <el>.textContent = css;
      (F-458/F-463: admin-dialog.js, email-dialog.js, llm-keys-dialog.js und
      export.js/PRINT_CSS weisen so zu — vom alten Extraktor nie gesehen).
      Aufgelöst wird NUR, wenn der Bezeichner "css" enthält (Groß-/Klein-
      schreibung egal) — das ist die durchgängige Namenskonvention in diesem
      Repo für injizierte Stylesheets. Diese Einschränkung ist bewusst eng:
      ohne echtes Scoping würde eine reine Namenssuche über die ganze Datei
      sonst jede gleichnamige String-Deklaration aufgreifen (`label`, `msg`,
      `text`, `v`, `o`, …, siehe grep-Befund Karte #401) und beliebigen
      Nicht-CSS-Text als CSS fehlinterpretieren. Ein Modul, das künftig einen
      anderen Variablennamen für injiziertes CSS wählt, bleibt entsprechend
      unsichtbar — das ist eine bekannte Grenze, kein stiller Fehler: die
      Selbsttest-Schwelle unten reißt in genau diesem Fall. Bewusst OHNE die
      Style-Element-Einschränkung von Muster 1: export.js weist über
      `head.appendChild(mk('style')).textContent = PRINT_CSS;` zu — der
      Empfänger ist dort ein Funktionsaufruf, kein Bezeichner, die
      Style-Element-Erkennung von oben griffe hier gar nicht. */
function extrahiereCssStrings(quelle) {
  const treffer = []
  const styleVariablen = ermittleStyleElementVariablen(quelle)

  const reDirekt = /([A-Za-z_$][\w$]*)\.textContent\s*=\s*(?:'([^\n]*?)'|"([^\n]*?)"|`([\s\S]*?)`)\s*;/g
  let m
  while ((m = reDirekt.exec(quelle))) {
    if (!styleVariablen.has(m[1])) continue
    treffer.push(m[2] ?? m[3] ?? m[4])
  }

  const reZwischenvar = /\.textContent\s*=\s*([A-Za-z_$][\w$]*)\s*;/g
  while ((m = reZwischenvar.exec(quelle))) {
    const name = m[1]
    if (!/css/i.test(name)) continue
    const reDekl = new RegExp(
      `(?:const|let|var)\\s+${name}\\s*=\\s*(?:'([^\\n]*?)'|"([^\\n]*?)"|\`([\\s\\S]*?)\`)\\s*;`,
    )
    const dm = reDekl.exec(quelle)
    if (dm) treffer.push(dm[1] ?? dm[2] ?? dm[3])
  }

  return treffer
}

/* Klammer-basierter Mini-Parser: liefert { selector, body } je Regel, flacht
   @media- UND @supports-Blöcke rekursiv ab (deren innere Regeln zählen
   genauso mit — vorher wurde nur @media behandelt, F-463/Karte #401).
   F-454/Karte #527: überspringt jetzt auch CSS-Block-Kommentare VOR einer
   Regel — ohne das landete ein Kommentar direkt über einem Selektor (in
   style.css die Regel, nicht die Ausnahme) als Textbestandteil IM Selektor
   (z.B. Kommentartext + Zeilenumbruch + ".tab-badge" statt nur ".tab-badge"),
   was den Dark-Override-Abgleich per String-Vergleich stumm brechen liess.
   Die JS-injizierten CSS-Strings (F-439-Korpus oben) sind minifiziert und
   enthalten ohnehin keine Kommentare — für sie ändert sich nichts. */
function extrahiereRegeln(css) {
  const regeln = []
  function parseBlock(s, start) {
    let i = start
    while (i < s.length) {
      while (i < s.length && /\s/.test(s[i])) i++
      if (i < s.length && s[i] === '/' && s[i + 1] === '*') {
        const kommentarEnde = s.indexOf('*/', i + 2)
        i = kommentarEnde === -1 ? s.length : kommentarEnde + 2
        continue
      }
      if (i >= s.length) break
      const j = s.indexOf('{', i)
      if (j === -1) break
      const kopf = s.slice(i, j).trim()
      let tiefe = 1
      let k = j + 1
      while (k < s.length && tiefe > 0) {
        if (s[k] === '{') tiefe++
        else if (s[k] === '}') tiefe--
        k++
      }
      const body = s.slice(j + 1, k - 1)
      if (kopf.startsWith('@media') || kopf.startsWith('@supports')) parseBlock(body, 0)
      else regeln.push({ selector: kopf, body })
      i = k
    }
  }
  parseBlock(css, 0)
  return regeln
}

/* Splittet Deklarationen an `;` — aber NICHT innerhalb von Klammern, sonst
   zerfällt z.B. `background: url(data:image/svg+xml;base64,...)` an dem `;`
   im Data-URI in Bruchstücke (F-463/Karte #401: bislang nur latent, da kein
   injiziertes CSS aktuell einen Data-URI mit `;` enthält — trotzdem eine
   reale Parser-Lücke, kein hypothetisches Risiko). */
function parseDeclarations(body) {
  const props = {}
  const teile = []
  let tiefe = 0
  let start = 0
  for (let i = 0; i < body.length; i++) {
    const c = body[i]
    if (c === '(') tiefe++
    else if (c === ')') tiefe--
    else if (c === ';' && tiefe === 0) {
      teile.push(body.slice(start, i))
      start = i + 1
    }
  }
  teile.push(body.slice(start))
  for (const d of teile.map(x => x.trim()).filter(Boolean)) {
    const ci = d.indexOf(':')
    if (ci === -1) continue
    props[d.slice(0, ci).trim()] = d.slice(ci + 1).trim()
  }
  return props
}

const KEINE_FLAECHE = new Set(['transparent', 'none', 'inherit', 'initial', 'unset'])

/* rgba()/rgb() mit alpha < 1 = Overlay-Backdrop, keine text-tragende Fläche
   (siehe Kommentar oben). Fehlt alpha bei rgb() → als 1 (opak) gewertet. */
function istTranslucenterBackdrop(wert) {
  const m = wert.match(/^rgba?\(\s*\d+\s*,\s*\d+\s*,\s*\d+\s*(?:,\s*([\d.]+))?\s*\)$/)
  if (!m) return false
  const alpha = m[1] === undefined ? 1 : parseFloat(m[1])
  return alpha < 1
}

function istFestFlaeche(wert) {
  if (/^var\(/.test(wert)) return false
  if (KEINE_FLAECHE.has(wert)) return false
  if (istTranslucenterBackdrop(wert)) return false
  return true
}

/* Ambient-Farbe: Entlastung für feste Flächen ohne eigene `color`, wenn im
   selben injizierten Block ein WURZEL-CONTAINER seine `color` setzt. DOM-seitig
   hängt der ganze Dialog darunter, die Farbe wird also vererbt — auch ohne
   CSS-Selektor-Verschachtelung. Das ist die F-463-Fehlalarm-Falle: ohne diese
   Entlastung meldet der Wächter 7 Regeln in den drei Dialog-Modulen, die in
   Wahrheit korrekt eingefärbt sind.

   AMBIENT_WURZELN ist eine EXPLIZITE LISTE, keine Heuristik — und das ist der
   Kern von F-606. Die erste Fassung fragte `regeln.some(hat Fläche und color)`,
   also „irgendeine Regel im Block". Gemessen wirkten dadurch als Spender:
   in `llm-pii.js` u. a. `.pii-hit-redact` und `.pii-bulk button:hover`, in
   `llm-tab.js` u. a. `.llm-turn-u` und `.llm-mut-row[data-state="applied"]` —
   Blatt- und Zustandsregeln, keine Wrapper. Folge: JEDE feste Fläche ohne
   `color` war in genau den beiden Dateien entlastet, deren 17 Regeln F-439
   überhaupt ausgelöst haben. Der Wächter war dort schwächer als vor seiner
   „Verbesserung".

   Gemessen (Karte #401-Nacharbeit, drei Varianten über den ganzen Korpus):
     - `some()` (alt):            0 Funde  → entlastet alles, wertlos
     - namentliche Wurzelliste:   0 Funde  → entlastet genau die 7+2 Fehlalarme
     - gar keine Entlastung:      9 Funde  → 7 davon sind die F-463-Fehlalarme

   Wer ein neues `_ensureXStyles()`-Modul mit eigenem Wurzel-Wrapper anlegt,
   trägt ihn hier ein. Das ist Absicht: Ein Eintrag zwingt zu der Frage, ob der
   Selektor wirklich der Container ist, unter dem alles andere hängt. Eine
   Heuristik stellt diese Frage nie. */
const AMBIENT_WURZELN = new Set([
  'body',          // export.js: Druck-CSS, gedruckt wird ohnehin auf Weiss
  '.admin-panel',  // admin-dialog.js
  '.email-panel',  // email-dialog.js
  '.llm-panel',    // llm-keys-dialog.js
  '.agz-panel',    // agent-zugaenge-dialog.js (F-1456: feste Fläche #fff + color #222)
])

function ermittleAmbientFarbe(regeln) {
  return regeln.some(r => {
    if (!AMBIENT_WURZELN.has(r.selector.trim())) return false
    const props = parseDeclarations(r.body)
    const bg = props['background'] || props['background-color']
    return bg && istFestFlaeche(bg) && 'color' in props
  })
}

describe('CSS-Flächen: jede feste background-Farbe hat eine eigene color (F-439)', () => {
  /* F-623: REKURSIV. Die erste Fassung las nur das oberste Verzeichnis —
     `src/calendar/` und jedes künftige Unterverzeichnis blieben unsichtbar,
     während der Kopfkommentar behauptete, der Korpus sei „vollständig, nicht
     nur die meisten Dateien". Dieselbe Fehlerklasse wie F-458, eine Ebene
     tiefer: eine Zusage, die nur für den geprüften Ausschnitt gilt. */
  const dateien = readdirSync(SRC, { recursive: true })
    .map(p => String(p))
    .filter(f => f.endsWith('.js'))
  const funde = []
  /* Rohzahl extrahierter CSS-Regeln je Datei — UNGEFILTERT (auch Regeln ohne
     feste Fläche zählen mit). Grund: notifications-dialog.js z.B. liefert
     zwar CSS-Regeln, aber keine einzige mit fester Fläche (durchgehend
     var()/rgba() — siehe Kopfkommentar) und würde in einer nur auf `funde`
     basierenden Modul-Zählung als "kein Treffer" untergehen, obwohl der
     Extraktor sein CSS korrekt sieht. Für den Korpus-Vollständigkeits-
     Selbsttest unten zählt daher diese Rohzahl, nicht `funde`. */
  const regelnJeDatei = new Map()

  for (const datei of dateien) {
    const quelle = readFileSync(join(SRC, datei), 'utf8')
    for (const css of extrahiereCssStrings(quelle)) {
      const regeln = extrahiereRegeln(css)
      if (regeln.length > 0) {
        regelnJeDatei.set(datei, (regelnJeDatei.get(datei) || 0) + regeln.length)
      }
      const ambientFarbeVorhanden = ermittleAmbientFarbe(regeln)
      for (const regel of regeln) {
        const props = parseDeclarations(regel.body)
        const bg = props['background'] || props['background-color']
        if (!bg || !istFestFlaeche(bg)) continue
        funde.push({
          datei,
          selector: regel.selector,
          bg,
          hatColor: 'color' in props,
          abgedecktDurchVorfahre: !('color' in props) && ambientFarbeVorhanden,
        })
      }
    }
  }

  it('kennt überhaupt injizierte Fläche-Regeln (Selbsttest gegen kaputte Regex)', () => {
    /* Grobes Netz: Gesamtzahl der feste-Fläche-Funde (Stand Karte #401: 41).
       Schwelle mit Marge nach unten (>= 30), damit kleinere CSS-Edits nicht
       flackern, aber ein pauschaler Regex-Bruch (z.B. Zwischenvariable- oder
       Template-Literal-Erkennung kaputt) auffällt. Diese Zahl allein hätte
       den F-458-Fehler NICHT sicher gefangen — notifications-dialog.js trägt
       0 zu `funde` bei, siehe Test darunter für den präzisen Regressionstest
       genau dieses Fehlers. */
    expect(funde.length).toBeGreaterThanOrEqual(30)
  })

  it('behält die vier zuvor unsichtbaren Module im Korpus (F-458/F-463, Karte #401)', () => {
    /* Das ist der eigentliche Wächter gegen den Fehler dieser Karte: eine
       reine Regel-/Modul-ZAHL (egal wie hoch) reißt nicht zuverlässig, wenn
       GENAU eines von vielen Modulen verschwindet, solange die übrigen
       weiterwachsen. Deshalb hier explizit und namentlich je Modul geprüft —
       das reißt garantiert, sobald eines dieser vier wieder aus dem Korpus
       fällt, unabhängig davon, was der Rest des Korpus tut. Auf Rohzahl
       (nicht `funde`) geprüft, weil notifications-dialog.js keine einzige
       feste-Fläche-Regel hat (siehe Kommentar oben) — 0 `funde`-Einträge sind
       für dieses Modul der Normalzustand, nicht das Regressionssignal. */
    const erwartet = [
      'admin-dialog.js',
      'email-dialog.js',
      'llm-keys-dialog.js',
      'notifications-dialog.js',
    ]
    for (const datei of erwartet) {
      expect(
        regelnJeDatei.get(datei) || 0,
        `${datei} liefert keine extrahierte CSS-Regel mehr — Zwischenvariable-/` +
        `Template-Literal-Erkennung in extrahiereCssStrings() kaputt? (F-458/Karte #401)`,
      ).toBeGreaterThan(0)
    }
  })

  it('ignoriert .textContent-Zuweisungen an Nicht-<style>-Elemente mit CSS-artigem Text (F-623b)', () => {
    /* Regressionstest für den konkret belegten Fall: src/sync.js weist einem
       gewöhnlichen <p>-Element (nicht <style>) per Template-Literal einen
       Warntext zu, dessen `${…}`-Interpolation vom klammer-basierten
       Regel-Parser als CSS-Block-Start gelesen werden könnte, sobald der Text
       zusätzlich noch "background:" enthält (Gegenprobe unten). Ohne die
       Style-Element-Einschränkung in extrahiereCssStrings() (siehe deren
       Kopfkommentar) hätte diese Quelle einen Treffer geliefert. */
    const quelle = `
      function warnDialog(parts) {
        const msg = document.createElement('p');
        msg.textContent = \`Du bist dabei, \${parts.join(' und ')} zu löschen. {background:red;color:red} Das ist ungewöhnlich viel.\`;
      }
    `
    expect(extrahiereCssStrings(quelle)).toEqual([])
  })

  it('jede feste Fläche hat eine eigene color-Deklaration', () => {
    const fehlend = funde.filter(f => !f.hatColor && !f.abgedecktDurchVorfahre)
    const bericht = fehlend.map(f => `${f.selector}  (${f.datei}, background:${f.bg})`)
    expect(
      bericht,
      `Feste Fläche ohne eigene Textfarbe — erbt var(--text), das im Dark-Theme\n` +
      `auf hellen Flächen fast unlesbar ist (F-439). Entweder eine passende\n` +
      `color-Deklaration in derselben Regel ergänzen, oder — falls die Fläche\n` +
      `nachweislich nie Text trägt (z.B. Overlay-Backdrop) — istTranslucenterBackdrop()\n` +
      `bzw. KEINE_FLAECHE in tests/css-fill-color.test.js begründet erweitern:\n  ${bericht.join('\n  ')}`,
    ).toEqual([])
  })
})

/* ────────────────────────────────────────────────────────────────────────
 * F-454: WCAG-Kontrastrechnung für Regeln in src/style.css, die background/
 * background-color UND color in DERSELBEN Regel setzen.
 *
 * Der Wächter oben (F-439) prüft nur, OB eine color-Deklaration da ist — nicht,
 * ob sie genug Kontrast zur Fläche hat. Genau das ließ F-453 durchrutschen
 * (.llm-budget-warn, 3,46:1). Dieser Block schließt die Lücke NUR für
 * src/style.css — nicht für die JS-injizierten Overlay-Styles im Wächter oben.
 * Zwei getrennte Korpora, zwei getrennte Zusagen (F-623-Lehre: die Zusage
 * eines Wächters darf nicht breiter klingen als das, was er tatsächlich prüft).
 *
 * Formel (WCAG 2.x):
 *   L = 0,2126·R + 0,7152·G + 0,0722·B   (R/G/B linearisiert nach sRGB)
 *   Kontrast = (L_hell + 0,05) / (L_dunkel + 0,05)
 * AA für Fließtext verlangt 4,5:1. Große Schrift (≥ 18pt/24px, oder
 * ≥ 14pt/18,7px fett) käme mit 3:1 aus — diese Ausnahme wird hier NICHT
 * gemacht, weil keine der geprüften Regeln (Badges, Inputs, Banner) in diese
 * Größenklasse fällt (11–14px durchgehend).
 *
 * CSS-Variablen (var(--x)): WERDEN AUFGELÖST, getrennt für beide Themes
 * (Light: `:root, [data-theme="light"]`; Dark: `[data-theme="dark"]`). Eine
 * Regel muss in BEIDEN Themes bestehen — Auflösung schlägt bei undefinierten
 * Variablen fehl (kein stiller Fallback), das taucht dann als "unresolvable"
 * in `geprueft` gar nicht erst auf (siehe Selbsttest-Schwelle unten, die genau
 * deshalb komplett kaputte Auflösung fängt).
 *
 * Cascade-Näherung für Dark-Overrides: Existiert zu einem Basis-Selektor
 * `.foo` eine Regel `[data-theme="dark"] .foo { … }` (oder ihr `@media`-
 * Zwilling `:root:not([data-theme="light"]) .foo`), werden deren
 * Deklarationen für die Dark-Auswertung über die Basis-Deklarationen gelegt
 * (Vorbild: `.awaiting-banner`, `.tab-badge`). Das ist KEINE generische
 * CSS-Kaskade — andere Override-Muster (z.B. höhere Spezifität über mehrere
 * Klassen, Geschwister-Selektoren) erkennt dieser Wächter nicht.
 *
 * Vom Prüflauf ausgeschlossen sind NUR die reinen `:root`-Blöcke (Variablen-
 * Definition, sowie `:root:not([data-theme="light"]) …` als Dark-Override)
 * und `[data-theme="dark"] …`-Regeln — Letztere, weil sie wie oben
 * beschrieben separat auf ihren Basis-Selektor gefaltet und dort mitgeprüft
 * werden, nicht weil sie irrelevant wären. Eine `[data-theme="light"] …`-Regel
 * ist dagegen KEIN Fold-Ziel — sie ist eine eigenständige Light-Regel und
 * wird als solche (nur gegen die Light-Variablen) geprüft (F-1105: vorher
 * fiel sie unter denselben Ausschluss wie die Dark-Overrides und wurde in
 * gar keinem Theme geprüft — aktuell kein Treffer in `src/style.css`, aber
 * eine künftige Regel dieser Form wäre sonst lautlos durchgerutscht).
 *
 * Bewusst ausgenommen (Fehlalarm-Vermeidung, wie beim F-439-Wächter oben):
 *   - `transparent`/`none`/`inherit`/`initial`/`unset` als Fläche (KEINE_FLAECHE)
 *   - `color: inherit` — ohne volle Kaskade nicht auflösbar
 *   - rgba()/rgb() mit alpha < 1 als Fläche — der sichtbare Kontrast hängt vom
 *     (hier unbekannten) Untergrund ab, auf dem die transluzente Fläche liegt
 *   - `color-mix(...)` als Fläche ODER Textfarbe — dynamische Mischfarbe,
 *     ohne vollen CSS-Farbraum (oklab/srgb) nicht verlässlich in RGB
 *     aufzulösen. NAMENTLICHE Ausnahmeliste (KONTRAST_AUSNAHMEN), keine
 *     Heuristik — dieselbe Lehre wie AMBIENT_WURZELN oben (F-606): eine
 *     Bedingung, die „irgendwie unresolvable" pauschal durchwinkt, würde auch
 *     künftige echte Fehler verdecken.
 * ──────────────────────────────────────────────────────────────────────── */

const KONTRAST_AUSNAHMEN = new Map([
  ['.column--tinted .column-count', 'background ist color-mix() mit Laufzeit-Spaltenfarbe --col-tint (#666/#146/#148), nicht statisch auflösbar'],
  ['.attachment-drop-overlay', 'background ist color-mix() in srgb, nicht statisch auflösbar'],
])

function parseThemeVars(css, blockRe) {
  const vars = new Map()
  const block = css.match(blockRe)
  if (!block) return vars
  for (const m of block[1].matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    vars.set(m[1], m[2].trim())
  }
  return vars
}

function hexZuRgb(hex) {
  let h = hex.replace('#', '')
  if (h.length === 3) h = h.split('').map(c => c + c).join('')
  const num = parseInt(h, 16)
  return [(num >> 16) & 255, (num >> 8) & 255, num & 255]
}

function rgbFunktionZuRgb(wert) {
  const m = wert.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+))?\s*\)$/)
  if (!m) return null
  return { rgb: [Number(m[1]), Number(m[2]), Number(m[3])], alpha: m[4] === undefined ? 1 : parseFloat(m[4]) }
}

/* Löst einen Farbwert auf: hex, rgb()/rgba(), oder var(--x) (rekursiv gegen
   die übergebene Theme-Variablen-Map). Gibt { rgb, alpha } zurück, oder
   { unresolvable: true, grund } — NIE einen stillen Fallback (anders als der
   var()-Fallback selbst im echten CSS, siehe F-436). */
function loeseFarbeAuf(wert, themeVars) {
  /* `!important` gehört nicht zur Farbe (F-1466): ungekürzt las hexZuRgb aus
     „#fff !important“ eine falsche Farbe — .notif-btn-primary meldete 1,13:1. */
  const w = wert.replace(/\s*!important\s*$/i, '').trim()
  if (/color-mix\(/.test(w)) return { unresolvable: true, grund: 'color-mix()' }
  const varMatch = w.match(/^var\(\s*(--[a-z0-9-]+)/)
  if (varMatch) {
    const name = varMatch[1]
    if (!themeVars.has(name)) return { unresolvable: true, grund: `Variable ${name} im Theme nicht definiert` }
    return loeseFarbeAuf(themeVars.get(name), themeVars)
  }
  if (/^#/.test(w)) return { rgb: hexZuRgb(w), alpha: 1 }
  if (/^rgba?\(/.test(w)) {
    const r = rgbFunktionZuRgb(w)
    if (!r) return { unresolvable: true, grund: 'rgb()/rgba()-Syntax nicht erkannt' }
    return r
  }
  return { unresolvable: true, grund: `unbekanntes Farbformat: ${w}` }
}

function relativeLuminanz([r, g, b]) {
  const kanal = c => {
    const n = c / 255
    return n <= 0.03928 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4)
  }
  const [R, G, B] = [kanal(r), kanal(g), kanal(b)]
  return 0.2126 * R + 0.7152 * G + 0.0722 * B
}

function kontrastverhaeltnis(rgb1, rgb2) {
  const l1 = relativeLuminanz(rgb1)
  const l2 = relativeLuminanz(rgb2)
  const hell = Math.max(l1, l2)
  const dunkel = Math.min(l1, l2)
  return (hell + 0.05) / (dunkel + 0.05)
}

/* Analysiert einen CSS-Text und liefert { geprueft, fehlend } — ausgelagert
   aus dem Testkörper (F-1105), damit dieselbe Logik sowohl gegen die echte
   `src/style.css` als auch gegen ein synthetisches CSS-Fragment (Gegenprobe
   unten) laufen kann, statt sie für die Gegenprobe ein zweites Mal
   abzuschreiben. */
function ermittleKontrastBefunde(css, themeCss = css) {
  /* F-1456: `themeCss` liefert die Theme-Variablen — für JS-injiziertes CSS
     ist das `src/style.css`, nicht das injizierte Fragment selbst. */
  const lightVars = parseThemeVars(themeCss, /:root,\n\[data-theme="light"\] \{([\s\S]*?)\n\}/)
  const darkVars = parseThemeVars(themeCss, /\[data-theme="dark"\] \{([\s\S]*?)\n\}/)
  const regeln = extrahiereRegeln(css)

  /* Dark-Override-Regeln einsammeln: `[data-theme="dark"] X` und ihr
     `@media`-Zwilling `:root:not([data-theme="light"]) X` — siehe
     Kopfkommentar. Beide Schreibweisen landen unter demselben bereinigten
     Basis-Selektor X, spätere Deklarationen gewinnen (beide Formen tragen
     laut Konvention ohnehin dieselben Werte). */
  const darkUeberschreibungen = new Map()
  const DARK_PRAEFIXE = ['[data-theme="dark"] ', ':root:not([data-theme="light"]) ']
  for (const r of regeln) {
    const praefix = DARK_PRAEFIXE.find(p => r.selector.startsWith(p))
    if (!praefix) continue
    const basis = r.selector.slice(praefix.length)
    const bestehend = darkUeberschreibungen.get(basis) || {}
    darkUeberschreibungen.set(basis, { ...bestehend, ...parseDeclarations(r.body) })
  }

  const geprueft = []
  const fehlend = []

  for (const r of regeln) {
    /* F-1105: Theme-Variablen-Definitionsblöcke (`:root`, auch dessen
       `:root:not([data-theme="light"])`-Variante) UND `[data-theme="dark"]`-
       Regeln sind keine eigenständig zu prüfenden Regeln — Erstere sind reine
       Variablen-Blöcke, Letztere werden oben in `darkUeberschreibungen` auf
       ihren Basis-Selektor gefaltet und dort mitgeprüft. `[data-theme="light"]`-
       Regeln sind dagegen KEIN Fold-Ziel und werden bewusst NICHT
       ausgeschlossen — vorher fielen sie unter denselben pauschalen
       `^\[data-theme=`-Ausschluss wie die Dark-Overrides und wurden dadurch
       in gar keinem Theme geprüft. */
    if (/^:root\b/.test(r.selector)) continue
    if (r.selector === '[data-theme="dark"]' || r.selector.startsWith('[data-theme="dark"] ')) continue
    if (KONTRAST_AUSNAHMEN.has(r.selector)) continue

    const basisProps = parseDeclarations(r.body)
    if (!(basisProps['background'] || basisProps['background-color']) || !basisProps['color']) continue

    /* Eine `[data-theme="light"] X`-Regel gilt NUR im Light-Theme — sie wird
       nicht als Dark-Override gefaltet, ihr Dark-Wert ergibt sich aus der
       Basisregel `X` (falls vorhanden) bzw. deren eigenem Eintrag in
       `geprueft`. Gegen `dark` geprüft würde hier einen Light-Wert fälschlich
       als Dark-Wert bewerten. */
    const istLightOverride = r.selector.startsWith('[data-theme="light"] ')
    const themesZuPruefen  = istLightOverride
      ? [['light', lightVars]]
      : [['light', lightVars], ['dark', darkVars]]

    for (const [themeName, themeVars] of themesZuPruefen) {
      const override = themeName === 'dark' ? darkUeberschreibungen.get(r.selector) : null
      const props = override ? { ...basisProps, ...override } : basisProps
      const bgWert = props['background'] || props['background-color']
      const colorWert = props['color']
      if (!bgWert || !colorWert) continue
      if (KEINE_FLAECHE.has(bgWert.trim()) || colorWert.trim() === 'inherit') continue

      const bgFarbe = loeseFarbeAuf(bgWert, themeVars)
      const colorFarbe = loeseFarbeAuf(colorWert, themeVars)
      if (bgFarbe.unresolvable || colorFarbe.unresolvable) continue
      if (bgFarbe.alpha < 1) continue /* translucente Fläche, siehe Kopfkommentar */

      const ratio = kontrastverhaeltnis(bgFarbe.rgb, colorFarbe.rgb)
      geprueft.push({ selector: r.selector, theme: themeName, ratio })
      if (ratio < 4.5) {
        fehlend.push({
          selector: r.selector, theme: themeName,
          ratio: Math.round(ratio * 100) / 100, bg: bgWert, color: colorWert,
        })
      }
    }
  }

  return { geprueft, fehlend }
}

describe('CSS-Kontrast: background+color in derselben style.css-Regel erreicht AA 4,5:1 (F-454)', () => {
  const css = readFileSync(join(SRC, 'style.css'), 'utf8')
  const lightVars = parseThemeVars(css, /:root,\n\[data-theme="light"\] \{([\s\S]*?)\n\}/)
  const darkVars = parseThemeVars(css, /\[data-theme="dark"\] \{([\s\S]*?)\n\}/)

  it('kennt überhaupt Theme-Variablen (Selbsttest gegen kaputte Regex)', () => {
    expect(lightVars.size).toBeGreaterThan(10)
    expect(darkVars.size).toBeGreaterThan(10)
  })

  const { geprueft, fehlend } = ermittleKontrastBefunde(css)

  it('kennt überhaupt prüfbare Fläche+Textfarbe-Paare (Selbsttest gegen kaputte Regex)', () => {
    /* Stand Karte #527: 114 auflösbare Paare über beide Themes. Marge nach
       unten, damit kleinere style.css-Edits nicht flackern, aber ein
       pauschaler Regex-/Parser-Bruch auffällt. */
    expect(geprueft.length).toBeGreaterThanOrEqual(100)
  })

  it('jedes geprüfte Paar erreicht AA 4,5:1', () => {
    const bericht = fehlend.map(f => `${f.selector} [${f.theme}]  background:${f.bg} color:${f.color}  → ${f.ratio}:1`)
    expect(
      bericht,
      `Kontrast unter AA 4,5:1 (F-454) — die Farbe in src/style.css korrigieren,\n` +
      `NICHT die Schwelle senken. Bei var()-Paaren: die Variable im betroffenen\n` +
      `Theme-Block (:root/[data-theme="light"] bzw. [data-theme="dark"]) anpassen;\n` +
      `bei einer neuen, echt unauflösbaren Fläche (z.B. weitere color-mix()-Regel)\n` +
      `KONTRAST_AUSNAHMEN in diesem Testfile begründet erweitern:\n  ${bericht.join('\n  ')}`,
    ).toEqual([])
  })
})

/* ────────────────────────────────────────────────────────────────────────
 * F-1105 — Gegenprobe: eine `[data-theme="light"] X`-Regel mit schlechtem
 * Kontrast muss als Fehler auftauchen, statt lautlos übersprungen zu werden.
 * Rein synthetisches CSS (NICHT src/style.css) mit hartkodierten Hex-Farben,
 * damit kein Theme-Variablen-Block nötig ist. Vor dem Fix (Ausschluss-Regex
 * `^\[data-theme=` traf JEDE `[data-theme=…]`-Regel) wäre `fehlend` hier leer
 * geblieben — dieselbe Fläche/Farbe unter `.plain-selector` ohne Präfix wird
 * zur Kontrolle mitgeprüft und muss ebenfalls auffallen.
 * ──────────────────────────────────────────────────────────────────────── */
describe('CSS-Kontrast — [data-theme="light"]-Regel wird geprüft, nicht übersprungen (F-1105)', () => {
  const syntheticCss = `
[data-theme="light"] .f1105-schlecht-hell {
  background: #ffffff;
  color: #f5f5f5;
}
.f1105-schlecht-kontrolle {
  background: #ffffff;
  color: #f5f5f5;
}
[data-theme="dark"] .f1105-dark-override {
  background: #000000;
  color: #000005;
}
`

  it('erkennt den schlechten Kontrast der [data-theme="light"]-Regel', () => {
    const { fehlend } = ermittleKontrastBefunde(syntheticCss)
    const treffer = fehlend.find(f => f.selector === '[data-theme="light"] .f1105-schlecht-hell')
    expect(treffer).toBeTruthy()
    expect(treffer.theme).toBe('light')
    expect(treffer.ratio).toBeLessThan(4.5)
  })

  it('prüft die Light-Regel NUR gegen Light, nicht zusätzlich gegen Dark', () => {
    const { geprueft } = ermittleKontrastBefunde(syntheticCss)
    const treffer = geprueft.filter(g => g.selector === '[data-theme="light"] .f1105-schlecht-hell')
    expect(treffer.map(t => t.theme)).toEqual(['light'])
  })

  it('Kontrollregel ohne Präfix wird weiterhin erkannt (Regex nicht zu eng geworden)', () => {
    const { fehlend } = ermittleKontrastBefunde(syntheticCss)
    expect(fehlend.some(f => f.selector === '.f1105-schlecht-kontrolle')).toBe(true)
  })

  it('[data-theme="dark"]-Regel bleibt weiterhin vom eigenständigen Prüflauf ausgeschlossen (Fold-Ziel)', () => {
    const { geprueft } = ermittleKontrastBefunde(syntheticCss)
    expect(geprueft.some(g => g.selector === '[data-theme="dark"] .f1105-dark-override')).toBe(false)
  })
})

/* ────────────────────────────────────────────────────────────────────────
 * F-1456 (Sprint-80-Kritiker D-4): Kontrast auch für das JS-INJIZIERTE CSS.
 *
 * Der F-454-Block oben prüft ausdrücklich NUR `src/style.css`; Karte #637
 * verlangte, ihn für `.agz-panel`/den Admin-Dialog zu nutzen, und lieferte
 * stattdessen Regex-Spiegeltests auf den CSS-String (tests/anzeige-fehler-
 * boardliste-637.test.js) — die prüfen, dass `#fff` im String steht, nicht dass
 * der Text darauf lesbar ist. Dieser Block rechnet dieselbe WCAG-Formel
 * (ermittleKontrastBefunde) über ALLE injizierten Stile, mit den Theme-
 * Variablen aus style.css, und zusätzlich einen zweiten Fall, den die Regel-
 * für-Regel-Prüfung nicht sieht: eine Regel mit `color` OHNE eigene Fläche
 * sitzt auf der Fläche des Wurzel-Wrappers (AMBIENT_WURZELN) — dagegen wird die
 * Textfarbe gerechnet (z. B. `.agz-meldung--ok` auf `.agz-panel`).
 *
 * Grenzen (ehrlich): keine volle Kaskade (verschachtelte Flächen wie
 * `.agz-einmal` werden nur über ihre eigene Regel erfasst), `color-mix()`/
 * `rgba()` bleiben ausgenommen, und eine SICHTPRÜFUNG im Browser ersetzt das
 * nicht — gerechnet wird, nicht gesehen.
 * ──────────────────────────────────────────────────────────────────────── */

function ermittleAmbientBefunde(css, themeCss) {
  const lightVars = parseThemeVars(themeCss, /:root,\n\[data-theme="light"\] \{([\s\S]*?)\n\}/)
  const darkVars = parseThemeVars(themeCss, /\[data-theme="dark"\] \{([\s\S]*?)\n\}/)
  const regeln = extrahiereRegeln(css)
  const wurzel = regeln.find(r => AMBIENT_WURZELN.has(r.selector.trim()) && r.selector.trim() !== 'body')
  if (!wurzel) return { geprueft: [], fehlend: [] }
  const wp = parseDeclarations(wurzel.body)
  const wbg = wp['background'] || wp['background-color']
  if (!wbg || KEINE_FLAECHE.has(wbg.trim())) return { geprueft: [], fehlend: [] }
  const geprueft = []
  const fehlend = []
  for (const r of regeln) {
    if (r === wurzel || KONTRAST_AUSNAHMEN.has(r.selector)) continue
    const props = parseDeclarations(r.body)
    const farbe = props['color']
    if (!farbe || props['background'] || props['background-color']) continue
    if (farbe.trim() === 'inherit') continue
    for (const [theme, vars] of [['light', lightVars], ['dark', darkVars]]) {
      const bg = loeseFarbeAuf(wbg, vars)
      const fg = loeseFarbeAuf(farbe, vars)
      if (bg.unresolvable || fg.unresolvable || bg.alpha < 1) continue
      const ratio = kontrastverhaeltnis(bg.rgb, fg.rgb)
      geprueft.push({ selector: r.selector, theme, ratio })
      if (ratio < 4.5) fehlend.push({ selector: r.selector, theme, ratio: Math.round(ratio * 100) / 100, bg: wbg, color: farbe })
    }
  }
  return { geprueft, fehlend }
}

describe('CSS-Kontrast: JS-injizierte Stile (agz-panel, Admin-Dialog, Board-Übersicht …) erreichen AA 4,5:1 (F-1456)', () => {
  const styleCss = readFileSync(join(SRC, 'style.css'), 'utf8')
  const dateien = readdirSync(SRC, { recursive: true }).map(p => String(p)).filter(f => f.endsWith('.js'))
  const eigene = []
  const ambient = []
  const dateienMitBefund = new Set()
  /* Seit F-1466 ALLE Module: die frühere Namensliste (nur die drei Dialoge des
     Sprint-80-Befunds) liess vorbestehende Verstösse in sechs Modulen durch
     (14 Regeln, 16 Messzeilen Regel × Stil): var(--accent)+#fff im dunklen
     Stil 2,43:1, .notif-btn-primary, .llm-mut-retry, #888 auf der festen
     weissen .llm-panel-Fläche. Eine neue
     Datei ist damit ab dem ersten Commit gedeckt, statt erst, wenn jemand sie
     in eine Liste einträgt. */
  for (const datei of dateien) {
    const quelle = readFileSync(join(SRC, datei), 'utf8')
    for (const css of extrahiereCssStrings(quelle)) {
      const a = ermittleKontrastBefunde(css, styleCss)
      const b = ermittleAmbientBefunde(css, styleCss)
      if (a.geprueft.length || b.geprueft.length) dateienMitBefund.add(datei)
      eigene.push(...a.fehlend.map(f => ({ ...f, datei })))
      ambient.push(...b.fehlend.map(f => ({ ...f, datei })))
      eigene.geprueft = (eigene.geprueft || 0) + a.geprueft.length
      ambient.geprueft = (ambient.geprueft || 0) + b.geprueft.length
    }
  }
  const fmt = f => `${f.selector} [${f.theme}] (${f.datei}) background:${f.bg} color:${f.color} → ${f.ratio}:1`

  it('Selbsttest: rechnet überhaupt Paare, und agent-zugaenge-dialog.js/admin-dialog.js sind im Korpus', () => {
    expect(eigene.geprueft).toBeGreaterThanOrEqual(10)
    expect(ambient.geprueft).toBeGreaterThanOrEqual(8)
    expect(dateienMitBefund.has('agent-zugaenge-dialog.js')).toBe(true)
    expect(dateienMitBefund.has('admin-dialog.js')).toBe(true)
  })

  it('Regeln mit eigener Fläche UND color erreichen 4,5:1', () => {
    expect(eigene.map(fmt), `Kontrast unter AA 4,5:1 im injizierten CSS — Farbe korrigieren, nicht die Schwelle senken:\n  ${eigene.map(fmt).join('\n  ')}`).toEqual([])
  })

  it('Regeln mit color OHNE eigene Fläche erreichen 4,5:1 auf der Fläche des Wurzel-Wrappers', () => {
    expect(ambient.map(fmt), `Textfarbe auf der Wrapper-Fläche unter AA 4,5:1:\n  ${ambient.map(fmt).join('\n  ')}`).toEqual([])
  })

  it('Gegenprobe: ein synthetisches .agz-panel mit hellgrauem Text wird als Fehler erkannt', () => {
    const css = '.agz-panel{background:#fff;color:#222}.agz-meldung--ok{color:#bbbbbb}.agz-btn{background:#ffffff;color:#eeeeee}'
    expect(ermittleAmbientBefunde(css, styleCss).fehlend.map(f => f.selector)).toContain('.agz-meldung--ok')
    expect(ermittleKontrastBefunde(css, styleCss).fehlend.map(f => f.selector)).toContain('.agz-btn')
  })
})
