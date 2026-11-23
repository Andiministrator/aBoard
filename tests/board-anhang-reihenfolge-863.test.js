/**
 * Karte #443, Kritiker-Nacharbeit — die Reihenfolge beim ERSETZEN eines
 * Board-Anhangs, und die Freigabe seiner Objekt-URL ([[F-863]], [[F-864]]).
 *
 * WAS DIESE DATEI IST — und was sie nicht ist: ein STRUKTUR-Wächter auf dem
 * Quelltext von `src/configdialog.js`, kein Verhaltenstest. Der Grund ist
 * nicht Bequemlichkeit: `_setBg`, `pickIdentityImage`, `refreshBgPreview` und
 * `closeOverlay` sind modul-private Closures; das Modul exportiert nur
 * `{ open, openPartition }`. Ein Verhaltenstest müsste den Dialog öffnen, ein
 * `<input type=file>` mit einem echten File bestücken und dessen `onchange`
 * auslösen — viel Gerüst für eine Invariante, die aus einer einzigen
 * Reihenfolge besteht.
 *
 * Der Verify-Lauf zum Sprint-61-Abschluss hat zu Recht angemerkt, dass die
 * beiden Fixes ohne diese Datei GAR KEINE Abdeckung hätten: „behoben" wäre
 * allein durch Codelesen gedeckt gewesen, und eine Gegenprobe wäre unmöglich.
 * Was hier steht, fängt genau die Regression, die real passieren kann —
 * jemand dreht die Reihenfolge zurück, weil sie „aufräumen vor dem Anlegen"
 * intuitiver findet.
 *
 * Grenzen, damit niemand mehr hineinliest, als drinsteht:
 *
 *  - Ein Struktur-Wächter prüft die Anordnung im Quelltext, nicht das
 *    Laufzeitverhalten. Wer die Funktionen umbenennt oder umbaut, muss ihn
 *    mitziehen — dann aber bewusst.
 *  - Der ANKER (`indexOf('function name(')`) sucht im ungestrippten Text;
 *    Kommentare werden erst danach entfernt. Nennt künftig ein Kommentar die
 *    Zeichenfolge `function _setBg(` VOR der echten Definition, ankert der
 *    Wächter im Kommentar. Heute tritt der Fall nicht ein (geprüft), aber es
 *    ist dieselbe Familie wie der Fehler, den dieser Kopf beschreibt — nur an
 *    der Anker- statt an der Treffer-Stelle.
 */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const quelle = readFileSync(join(process.cwd(), 'src/configdialog.js'), 'utf8')

/** Schneidet den Rumpf einer Funktion heraus (bis zur nächsten auf gleicher Ebene).
 *
 *  OHNE KOMMENTARE — und das ist keine Kosmetik: Die erste Fassung dieses
 *  Wächters mass die Position von `uploadBoardFile` im ERKLÄRTEXT von `_setBg`
 *  (der Kommentar dort nennt die Funktion, um zu begründen, warum die
 *  Reihenfolge umgestellt wurde). Der Treffer lag damit immer ganz vorn, die
 *  Zusicherung war immer erfüllt, und die Gegenprobe — Reihenfolge im Code
 *  zurückdrehen — blieb GRÜN. Ein Wächter, der den Kommentar prüft statt den
 *  Code, bewacht nichts. Gefunden hat das erst die Gegenprobe, nicht das
 *  Schreiben des Tests. */
function funktionsRumpf(name) {
  const start = quelle.indexOf(`function ${name}(`)
  expect(start, `Funktion ${name} nicht gefunden — der Wächter prüft dann nichts`).toBeGreaterThan(-1)
  const rest = quelle.slice(start + 1)
  /* Ende des Rumpfes: die nächste Funktion auf gleicher Ebene — ODER eines der
     Konstrukte, mit denen das Modul ausklingt. Ohne die zweite Hälfte lief der
     „Rumpf" der LETZTEN Funktion (`closeOverlay`) bis `})();` am Dateiende und
     umfasste 45 statt 19 Zeilen, samt der `EventBus.on`-Handler. Ein Wächter,
     der dort etwas sucht, findet es auch, wenn es längst woanders steht — und
     genau das hat die Gegenprobe des Kritiker-Laufs gezeigt: Die Revoke-Aufrufe
     liessen sich aus `closeOverlay` heraus in einen Event-Handler verschieben,
     ohne dass ein Test rot wurde. */
  const naechste = rest.search(/\n  (?:async )?function |\n  EventBus\.on\(|\n  return \{|\n\}\)\(\);/)
  const rumpf = naechste < 0 ? rest : rest.slice(0, naechste)
  return rumpf
    .replace(/\/\*[\s\S]*?\*\//g, ' ')   /* Blockkommentare */
    .replace(/^\s*\/\/.*$/gm, ' ')        /* Zeilenkommentare */
}

/* Selbsttest des Helfers: Bliebe ein Kommentar stehen, würden die Zusicherungen
   unten wieder die Prosa messen — der Fall, der oben beschrieben ist. */
describe('der Wächter misst Code, nicht Kommentare', () => {
  it('funktionsRumpf entfernt Kommentare', () => {
    const rumpf = funktionsRumpf('_setBg')
    expect(rumpf).not.toContain('Bis hierher stand das Löschen vorn')
    expect(rumpf, 'ohne Code-Inhalt prüfen die Tests unten nichts').toContain('uploadBoardFile')
  })
})

describe('F-863 — beim Ersetzen wird erst hochgeladen, dann gelöscht', () => {
  // Der Fehler: Der alte Anhang war serverseitig gelöscht und aus
  // `board.attachments` entfernt, BEVOR der Upload lief. Seit Karte #443
  // wirft der Upload (413/500/Netz) — dann war das alte Bild weg, das neue
  // nie da, und der leere `catch` schluckte die Meldung.
  /* Karte #460 (F-871): Der Löschaufruf heisst hier seit Sprint 62
     `entferneBoardAnhang` — die gemeinsame Hilfsfunktion, die den Eintrag nur
     bei geglücktem Löschen aus `board.attachments` nimmt. `deleteBoardAttachment`
     steht jetzt eine Ebene tiefer, in ihr. Der Wächter musste deshalb den Namen
     mitziehen; die Invariante ist unverändert: erst hochladen, dann löschen. */
  it.each([
    ['_setBg', 'uploadBoardFile', 'entferneBoardAnhang'],
    ['pickIdentityImage', 'uploadBoardFile', 'entferneBoardAnhang'],
  ])('%s lädt hoch, bevor es löscht', (fn, zuerst, danach) => {
    const rumpf = funktionsRumpf(fn)
    const iUpload = rumpf.indexOf(zuerst)
    const iDelete = rumpf.indexOf(danach)
    expect(iUpload, `${zuerst} kommt in ${fn} nicht vor`).toBeGreaterThan(-1)
    expect(iDelete, `${danach} kommt in ${fn} nicht vor`).toBeGreaterThan(-1)
    expect(iUpload,
      `In ${fn} steht ${danach} vor ${zuerst}. Schlägt der Upload fehl, ist der `
      + `alte Anhang bereits weg — genau F-863.`).toBeLessThan(iDelete)
  })

  it('pickBoardBackground meldet einen fehlgeschlagenen Upload, statt ihn zu schlucken', () => {
    /* Der Zusammenhang zum `_setBg`-Aufruf gehört in die Zusicherung, nicht
       nur „irgendwo im Rumpf steht ein catch mit alert". Die erste Fassung war
       beides zugleich, und beides ist im Kritiker-Lauf belegt worden:
       FALSCH-GRÜN — der Schutz um `_setBg` liess sich entfernen, solange ein
       unbeteiligtes `try/catch` im selben Rumpf stehenblieb; und FALSCH-ROT —
       `catch {` → `catch (e) {` ist verhaltensgleich (und nötig, sobald jemand
       den Fehler loggen will), machte den Test aber rot. Ein Wächter, der
       beides tut, wird beim nächsten Mal gelockert statt verstanden. */
    const rumpf = funktionsRumpf('pickBoardBackground')
    expect(rumpf,
      'der try/catch um den _setBg-Aufruf fehlt — dann laeuft der Fehlerfall aus '
      + 'F-863 wieder stumm durch').toMatch(/_setBg\([^)]*\)\s*;?\s*\}\s*catch\s*(\([^)]*\))?\s*\{\s*alert\(/)
  })
})

describe('F-864 — die Objekt-URL der Hintergrund-Vorschau wird freigegeben', () => {
  it('refreshBgPreview gibt die vorherige URL frei, bevor es eine neue setzt', () => {
    const rumpf = funktionsRumpf('refreshBgPreview')
    expect(rumpf, 'kein revokeObjectURL — jedes Öffnen des Dialogs leckt eine blob:-URL')
      .toMatch(/revokeObjectURL\(_bgThumbObjUrl\)/)
    const iRevoke = rumpf.indexOf('revokeObjectURL')
    const iSet = rumpf.indexOf('thumb.style.backgroundImage')
    expect(iRevoke, 'die alte URL wird erst nach dem Setzen freigegeben').toBeLessThan(iSet)
  })

  /* Karte #459 (F-878): Der Name dieses Tests lautete bis Sprint 62
     „closeOverlay gibt beide Objekt-URL-Sätze frei" — und beschrieb damit
     nach dem Fix genau das Verhalten, das als Fehler behoben wurde. Wer ihn
     las, musste die Kopplung für zugesichert halten. Der Wächter selbst ist
     unverändert richtig: Er sichert zu, dass BEIDE Freigaben in der Funktion
     vorkommen — sonst leckt eine Partition gar nicht mehr —, und sagt
     bewusst nichts darüber, unter welcher Bedingung sie laufen.

     Was er NICHT kann: strukturelle Bedingtheit erkennen. Ein `if (false)`
     käme hier durch. Das ist keine Lücke, die dieser Test schliessen sollte
     (ein Quelltext-Wächter, der die genaue `if`-Form prüft, bricht bei jeder
     Umformulierung) — dafür gibt es den Verhaltenstest in
     `tests/config-dialog-partition-scoped-revoke-459.test.js`, der beide
     Partitionen gleichzeitig öffnet und misst, welche URL freigegeben wird. */
  it('closeOverlay räumt beide Objekt-URL-Sätze auf — je in seiner Partition (F-878)', () => {
    const rumpf = funktionsRumpf('closeOverlay')
    expect(rumpf).toContain('_revokeBgThumbUrl()')
    expect(rumpf).toContain('_revokeConfigUrls()')
  })
})
