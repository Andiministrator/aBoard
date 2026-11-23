/* @vitest-environment node */

/**
 * Quelltext-Wächter gegen den `I18n.t(key)(arg)`-Doppelaufruf-Bug (F-748,
 * gefunden während Karte #348).
 *
 * `src/i18n.js` definiert `function t(key, ...args) { … return typeof val
 * === 'function' ? val(...args) : val; }`. Ein Aufruf OHNE Argumente wie
 * `I18n.t('some.key')` ruft eine hinterlegte Übersetzungsfunktion also
 * SOFORT mit leerer Argumentliste auf und gibt einen fertigen **String**
 * zurück — kein aufrufbares Objekt. `I18n.t('some.key')(arg)` versucht,
 * diesen String erneut als Funktion aufzurufen und wirft
 * `TypeError: ... is not a function`.
 *
 * Vier DATEIEN, FÜNF Fundstellen im Bestand trugen genau dieses Muster
 * (F-753(c)-Kritiker-Fund korrigiert eine frühere Untertreibung hier — am
 * Parent-Commit 3d96506 nachgezählt: `src/admin-dialog.js:139`,
 * `src/clients-view.js:88` und `:94`, `src/llm-keys-dialog.js:151`,
 * `src/projects-view.js:136`) — alle mit Karte #348 behoben auf
 * `I18n.t(key, arg)`. Dieser Test verhindert die Wiederkehr, weil er das
 * Muster STATISCH im Quelltext sucht, nicht zur Laufzeit — das ist wichtig
 * für Stellen, die (wie der `admin.placeholder-card`-Zweig in
 * admin-dialog.js) mit der aktuellen Datenlage gar nicht durchlaufen werden
 * und deshalb per Laufzeit-Test nicht fassbar sind (Fehlerklasse 32 in der
 * Umkehrung: kein Aufrufer-Test kann einen Pfad prüfen, der nicht betreten
 * wird — nur ein Quelltext-Wächter kann das).
 *
 * GRENZE (bewusst, siehe Kopfkommentare von tests/css-fill-color.test.js für
 * dasselbe Muster): Der Wächter erkennt nur die DIREKTE Schreibweise
 * `I18n.t(...)(...)`. Eine Umgehung über eine Zwischenvariable —
 * `const f = I18n.t(k); f(x);` — bleibt für ihn unsichtbar, weil das ein
 * allgemeines Aufruf-Nachverfolgungsproblem wäre (dieselbe Grenze wie bei
 * `extrahiereCssStrings()` dort). Im Bestand kommt diese Umgehung nirgends
 * vor (nachgeprüft) — sollte sie auftauchen, ist sie ein Refactoring-Anlass,
 * kein Fall für diesen Test.
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const SRC = new URL('../src/', import.meta.url).pathname

/* Kommentare ausblenden, BEVOR nach dem Muster gesucht wird — sonst hält ein
   zitierender Kommentar (wie der in diesem Test selbst oder in
   admin-dialog.js) den Wächter grün, obwohl der Text das Muster nennt.
   Einfache Line- und Block-Kommentar-Entfernung reicht hier — mit einer
   PRÄZISIERUNG (F-753(c)-Kritiker-Fund, hier nachgemessen): für `/*` stimmt
   die frühere Behauptung "kein String im Bestand enthält das in einer
   Weise, die die Entfernung verfälscht" uneingeschränkt. Für `//` NICHT
   wörtlich — String-Literale mit `://` (URL-Präfixe) werden von der naiven
   naive Zeilenkommentar-Regex (siehe entferneKommentare() unten) durchaus
   als Kommentar-Start fehlgedeutet und der Zeilenrest verschluckt. Nachgezählt (nicht die
   Kommentar-Vorkommen, nur ECHTE Code-Strings): 8 Zeilen — src/icons.js:3,
   src/metrics.js:160, src/notifications-dialog.js:141, src/markdown.js
   (zweimal, :61/:63), src/modal.js:2384, src/i18n.js (zweimal, die beiden
   'notif.gotify-url-required'-Werte). Für DIESEN Test bleibt es folgenlos,
   weil auf keiner dieser Zeilen nach der URL noch ein `I18n.t(...)(...)`-
   oder `I18n.t(` -Aufruf folgt, den die Trunkierung verschlucken könnte —
   die Muster-Suche selbst ist ohnehin nur ein Substring-Scan, kein Parser,
   und reagiert nur auf das, was NACH der Trunkierung noch dasteht.

   Block-Kommentare werden NICHT durch den leeren String ersetzt, sondern
   durch ebenso viele '\n' wie sie selbst enthielten (Orchestrator-Fund,
   Karte #348): Ein mehrzeiliger Block-Kommentar VOR einer Fundstelle
   verschiebt sonst jede Zeilennummer danach nach oben — bei email-dialog.js
   waren es zehn Zeilen Differenz, bei einer Datei mit ausgedehnteren
   Kopfkommentaren (llm-pii.js) wären es über hundert. Die gemeldete
   Fundstellen-Zeile ist der einzige Zweck der Ausgabe; eine falsche Zahl
   sieht aus wie eine Quelltext-Zeile und ist doch nur eine Zeile im
   bereinigten Zwischenprodukt, das niemand sonst sieht (Fehlerklasse-17-
   Verwandte: eine Zahl gehört nur zusammen mit dem sie erzeugenden Schritt
   gelesen). Zeilenkommentare (`//…`) brauchen das nicht — ihr Muster
   (`[^\n]*`) verschluckt den abschliessenden Zeilenumbruch ohnehin nie. */
function entferneKommentare(quelle) {
  return quelle
    .replace(/\/\*[\s\S]*?\*\//g, (treffer) => '\n'.repeat((treffer.match(/\n/g) || []).length))
    .replace(/\/\/[^\n]*/g, '')
}

/* `I18n.t(<beliebiges, ohne schließende Klammer davor>)(` — ein `)` direkt
   gefolgt von `(` ist im Kontext von `I18n.t(...)` ausschliesslich der
   Doppelaufruf; ein legitimer zweiter Aufruf mit dem echten Argument sieht
   syntaktisch anders aus (`I18n.t('key', arg)`, ein Komma statt `)(`). */
const DOPPELAUFRUF_MUSTER = /I18n\.t\([^)]*\)\(/g

describe('Quelltext-Wächter: kein I18n.t(key)(arg)-Doppelaufruf (F-748)', () => {
  const dateien = readdirSync(SRC, { recursive: true })
    .map(p => String(p))
    .filter(f => f.endsWith('.js'))

  it('kennt überhaupt I18n.t(...)-Aufrufe (Selbsttest gegen kaputte Regex)', () => {
    /* F-753(c)-Kritiker-Fund: `toBeGreaterThan(50)` gegen tatsächlich ~815
       Treffer ist keine Schwelle, sondern ein Freifahrtschein — ein
       pauschaler Regex-Bruch, der 90 % der Fundstellen verschluckt, bliebe
       unbemerkt. Vorbild tests/css-fill-color.test.js:268 (>=30 gegen 41,
       F-458): Schwelle mit MARGE nach unten, damit kleinere Edits nicht
       flackern, aber ein Bruch der Größenordnung auffällt. Gemessen (Stand
       dieser Karte, entferneKommentare()-bereinigt): 817. */
    let treffer = 0
    for (const datei of dateien) {
      const quelle = entferneKommentare(readFileSync(join(SRC, datei), 'utf8'))
      treffer += (quelle.match(/I18n\.t\(/g) || []).length
    }
    expect(treffer).toBeGreaterThanOrEqual(600)
  })

  it('behält die sechs aufrufstärksten Module im Korpus (Modul-Liste analog F-458)', () => {
    /* Reine Gesamtzahl allein reißt nicht zuverlässig, wenn GENAU ein
       großes Modul aus dem Korpus fällt (z.B. eine kaputte Import-Kette
       oder ein versehentlich ausgeschlossenes Verzeichnis), solange andere
       Module weiterwachsen — dieselbe Fehlerklasse wie F-458
       (tests/css-fill-color.test.js:281). Deshalb zusätzlich NAMENTLICH je
       Modul geprüft, mit einer groben, deutlich unter dem gemessenen Stand
       liegenden Untergrenze (renderer.js 143, modal.js 122,
       admin-dialog.js 100, llm-tab.js 84, sidebar.js 62,
       configdialog.js 59). */
    const erwartetMindestens = {
      'renderer.js':      50,
      'modal.js':          50,
      'admin-dialog.js':   40,
      'llm-tab.js':        30,
      'sidebar.js':        20,
      'configdialog.js':   20,
    }
    for (const [datei, minimum] of Object.entries(erwartetMindestens)) {
      const quelle = entferneKommentare(readFileSync(join(SRC, datei), 'utf8'))
      const treffer = (quelle.match(/I18n\.t\(/g) || []).length
      expect(
        treffer,
        `${datei} liefert nur noch ${treffer} I18n.t(...)-Treffer (erwartet ≥ ${minimum}) — ` +
        `Regex-/Kommentar-Erkennung kaputt oder Modul aus dem Korpus gefallen? (F-753(c))`,
      ).toBeGreaterThanOrEqual(minimum)
    }
  })

  it('entferneKommentare() zählt Zeilen deckungsgleich mit dem Original (Orchestrator-Gegenprobe)', () => {
    /* Regressionstest für den Fund des Orchestrators: ein mehrzeiliger
       Block-Kommentar VOR der Fundstelle darf die gemeldete Zeile nicht
       verschieben. Zeile 1 und 2 sind der Kommentar (3 Zeilen im Quelltext,
       da er über zwei '\n' läuft), Zeile 4 trägt das Muster — GENAU wie in
       der echten Datei muss die im bereinigten Text gezählte Zeile mit der
       Zeile im ROHEN Quelltext übereinstimmen. */
    const quelle = [
      '/* Kommentarzeile eins',
      '   Kommentarzeile zwei */',
      '',
      "I18n.t('x')(y);",
    ].join('\n')
    const bereinigt = entferneKommentare(quelle)
    DOPPELAUFRUF_MUSTER.lastIndex = 0
    const m = DOPPELAUFRUF_MUSTER.exec(bereinigt)
    expect(m).not.toBeNull()
    const zeileImBereinigten = bereinigt.slice(0, m.index).split('\n').length
    const zeileImRoh = quelle.slice(0, quelle.indexOf("I18n.t('x')(y)")).split('\n').length
    expect(zeileImBereinigten).toBe(zeileImRoh)
    expect(zeileImBereinigten).toBe(4)
  })

  it('kein src/**/*.js ruft I18n.t(...) unmittelbar ein zweites Mal auf', () => {
    const fundstellen = []
    for (const datei of dateien) {
      const roh = readFileSync(join(SRC, datei), 'utf8')
      const bereinigt = entferneKommentare(roh)
      let m
      DOPPELAUFRUF_MUSTER.lastIndex = 0
      while ((m = DOPPELAUFRUF_MUSTER.exec(bereinigt))) {
        const biszumTreffer = bereinigt.slice(0, m.index)
        const zeile = biszumTreffer.split('\n').length
        fundstellen.push(`${datei}:${zeile}  ${m[0]}`)
      }
    }
    expect(
      fundstellen,
      `I18n.t(key)(arg)-Doppelaufruf gefunden — I18n.t(key) OHNE Argumente ruft\n` +
      `eine hinterlegte Übersetzungsfunktion bereits auf und liefert einen String;\n` +
      `der zweite Aufruf wirft TypeError. Richtig: I18n.t(key, arg). (F-748):\n  ${fundstellen.join('\n  ')}`,
    ).toEqual([])
  })
})
