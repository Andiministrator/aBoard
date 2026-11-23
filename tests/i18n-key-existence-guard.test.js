/* @vitest-environment node */

/**
 * Quelltext-Wächter gegen fehlende i18n-Keys (F-750).
 *
 * `src/i18n.js#t(key)` fällt bei fehlendem Key auf den Key selbst zurück
 * (`dict[key] ?? EN[key] ?? key`) — ein TRUTHY String. Zwei Aufrufer
 * verliessen sich deshalb ohne Wirkung auf `I18n.t('x') || '…'`: der
 * Fallback greift nie, weil `I18n.t('x')` schon bei fehlendem Key etwas
 * Wahres liefert (nur eben den rohen Key statt eines Textes). Betroffen
 * waren `clients.loading` (clients-view.js, projects-view.js zweimal) und
 * `btn.delete` (notifications-dialog.js) — der Nutzer sah wörtlich
 * "clients.loading" bzw. "btn.delete" im UI. Beide Keys sind seit F-750 in
 * DE+EN ergänzt (src/i18n.js); dieser Test verhindert die Wiederkehr für
 * ALLE literalen `I18n.t('...')`-Aufrufe im Bestand, nicht nur die zwei
 * konkret gefundenen.
 *
 * Verfahren: STATISCHER Scan von src/**\/*.js nach `I18n.t(<literal>)`
 * (Quelltext, nicht Laufzeit-Traversal — dieselbe Fehlerklasse-32-Begründung
 * wie tests/i18n-double-call-guard.test.js: ein Pfad, der mit der aktuellen
 * Datenlage nicht durchlaufen wird, bleibt trotzdem geprüft). Für jeden
 * gefundenen Key wird der ECHTE `I18n.t()` mit lang='de' UND lang='en'
 * aufgerufen und geprüft, dass das Ergebnis NICHT der Key selbst ist — das
 * ist exakt der Bug-Mechanismus, kein Duplikat der Key-Tabelle.
 *
 * GRENZE — dynamische Präfixe (Konkatenation/Template-Interpolation) kann
 * dieser Wächter nicht auflösen und nimmt sie deshalb bewusst aus:
 *  - Konkatenation (`I18n.t('calendar.view.' + v)`,
 *    `I18n.t('chat-tail.kind.' + entry.kind)`): der Scan erkennt ein `+`
 *    direkt nach dem literalen Teil und überspringt den Treffer.
 *  - Template-Interpolation (`` I18n.t(`share.role-${role}`) ``,
 *    `` I18n.t(`history.field.${entry.field}`) ``,
 *    `` I18n.t(`identity.kind.${idn.kind}`) ``,
 *    `` I18n.t(`llm.preset.${k}`) ``, `` I18n.t(`llm.preset.system.${k}`) ``,
 *    `` I18n.t(`${delPrefix}.delete-title`) `` u.ä.): der Scan erkennt `${`
 *    im literalen Teil und überspringt den Treffer.
 *  - Reine Variablen (`I18n.t(labelKey)`, `I18n.t(f.labelKey)`, …) matcht
 *    das Literal-Muster ohnehin nicht — die Mehrheit der ~815 Aufrufe im
 *    Bestand fällt in diese Kategorie (siehe angepasste Schwelle in
 *    tests/i18n-double-call-guard.test.js, dort F-753(c) — die dort
 *    angepasste Schwelle behebt die dortige `>50`-vs-`~815`-Ungenauigkeit,
 *    nicht die hier behandelten fehlenden Keys).
 * Diese Fälle bleiben Laufzeit-/Review-Verantwortung des jeweiligen Moduls.
 *
 * ZWEITE GRENZE — ZWEI unabhängige Compile-Flag-Gates in src/i18n.js
 * selbst: `if (IS_SERVER_BUILD) { Object.assign(DE, {...}); ... }`
 * (server-only Keys: share.*, admin.menu, sidebar.clients, …) UND separat
 * `if (IS_PROJECTOR_BUILD) { Object.assign(DE, {...}); ... }` (ADR-0072,
 * projector-only Keys: compact.*, activity.*, chat-tail.*, doc.*,
 * sidebar.knowledge, …). Die Aufrufer dieser Keys stehen im Quelltext
 * selbst ausnahmslos hinter demselben jeweiligen Flag (verifiziert für
 * sidebar.js — Share-Dialog-Block Zeile 385–612), sind also im falschen
 * Kontext per DCE tot. Ein REIN textueller Scan sieht diese Gates nicht und
 * würde im falschen Kontext massenhaft falsch-positive "fehlende" Keys
 * melden. Da beide Blöcke Keys nur ADDIEREN (nie entfernen), ist NUR der
 * Kontext mit BEIDEN Flags true ('server+projektor', vitest.config.server.js)
 * die echte Vereinigungsmenge — der Auflösungs-Check läuft deshalb NUR dort
 * (`itServerProjektor`), während die Struktur-Selbsttests (Regex
 * funktioniert, Ausnahmeliste unauffällig) in allen vier Kontexten laufen. */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { I18n } from '../src/i18n.js'
import { itServerProjektor } from './helpers/build-flags.js'

const SRC = new URL('../src/', import.meta.url).pathname

/* Wie tests/i18n-double-call-guard.test.js#entferneKommentare — Block-
   Kommentare werden durch gleich viele '\n' ersetzt, damit Zeilennummern
   im bereinigten Text mit dem Original übereinstimmen. */
function entferneKommentare(quelle) {
  return quelle
    .replace(/\/\*[\s\S]*?\*\//g, (treffer) => '\n'.repeat((treffer.match(/\n/g) || []).length))
    .replace(/\/\/[^\n]*/g, '')
}

/* Fasst I18n.t(<quote>literal<quote>...) — Gruppe 2 ist der Key-Text,
   Gruppe 3 (falls vorhanden) ein '+' direkt nach dem literalen Teil
   (Konkatenation → dynamischer Präfix, siehe GRENZE oben). */
const LITERAL_CALL_MUSTER = /I18n\.t\(\s*(['"`])((?:(?!\1)[\s\S])*?)\1(\s*\+)?/g

function sammleLiteraleKeys(dateien) {
  const treffer = [] // { datei, zeile, key }
  for (const datei of dateien) {
    const roh = readFileSync(join(SRC, datei), 'utf8')
    const bereinigt = entferneKommentare(roh)
    let m
    LITERAL_CALL_MUSTER.lastIndex = 0
    while ((m = LITERAL_CALL_MUSTER.exec(bereinigt))) {
      const [, , key, konkatKonkatSuffix] = m
      if (konkatKonkatSuffix) continue        // 'foo.' + var  → dynamischer Präfix
      if (key.includes('${')) continue        // `foo.${var}` → Template-Interpolation
      const zeile = bereinigt.slice(0, m.index).split('\n').length
      treffer.push({ datei, zeile, key })
    }
  }
  return treffer
}

describe('Quelltext-Wächter: jeder literale I18n.t(...)-Key existiert (F-750)', () => {
  const dateien = readdirSync(SRC, { recursive: true })
    .map(p => String(p))
    .filter(f => f.endsWith('.js'))
    .filter(f => f !== 'i18n.js') // definiert die Keys, ruft sie nicht auf

  it('findet überhaupt literale I18n.t(...)-Aufrufe (Selbsttest gegen kaputte Regex)', () => {
    const treffer = sammleLiteraleKeys(dateien)
    expect(treffer.length).toBeGreaterThan(50)
  })

  it('Ausnahmeliste bleibt unauffällig: keiner der bekannten dynamischen Präfixe taucht als vollständiger Treffer auf', () => {
    /* Regressions-Beleg für die GRENZE oben: keiner dieser Präfixe darf als
       eigener (falscher) "Key" im Trefferset auftauchen — sonst hätte die
       Konkatenations-/Interpolations-Erkennung versagt. */
    const treffer = sammleLiteraleKeys(dateien)
    const keys = new Set(treffer.map(t => t.key))
    for (const praefix of ['calendar.view.', 'chat-tail.kind.', 'share.role-', 'share.status-', 'history.field.', 'identity.kind.', 'llm.preset.', 'llm.preset.system.']) {
      expect(keys.has(praefix), `dynamischer Präfix "${praefix}" wurde fälschlich als vollständiger Key erfasst`).toBe(false)
    }
  })

  itServerProjektor('jeder gefundene Key löst in DE UND EN zu etwas anderem als sich selbst auf (nur server+projektor-Kontext, siehe ZWEITE GRENZE oben)', () => {
    const treffer = sammleLiteraleKeys(dateien)
    const fehlend = []
    for (const { datei, zeile, key } of treffer) {
      for (const spracheL of ['de', 'en']) {
        I18n.setLang(spracheL)
        /* Manche Werte sind Funktionen, die Pflicht-Argumente erwarten
           (z.B. 'llm.send.estimate': (tokens, cost) => ...) — t() ruft sie
           beim Scan OHNE Argumente auf, das kann werfen. Ein Wurf beweist,
           dass der Key EXISTIERT (nur Argument-Bedarf, kein F-750-Fall) —
           anders als der stille Rückfall auf den Key selbst. */
        let wert
        try { wert = I18n.t(key) } catch { continue }
        if (wert === key) fehlend.push(`${datei}:${zeile}  '${key}' (${spracheL})`)
      }
    }
    I18n.setLang('de')
    expect(
      fehlend,
      `Fehlende i18n-Keys — I18n.t(key) fällt bei unbekanntem Key auf den Key\n` +
      `selbst zurück (truthy!), der Nutzer sieht den rohen Schlüssel im UI (F-750):\n  ${fehlend.join('\n  ')}`,
    ).toEqual([])
  })
})
