/**
 * Zentrale Ableitung der Compile-Zeit-Flags für Tests (ADR-0116, F-434).
 *
 * Bis Sprint 53 leitete jede Testdatei `IS_SERVER_BUILD` selbst ab — 15 Kopien
 * derselben vier Zeilen, jede mit eigener Schreibweise (`describeOnServer`,
 * `isServerBuildCtx`, inline-`if`). Das ist der Nährboden von F-386: wenn die
 * Skip-Buchhaltung zwischen zwei Kontexten unerwartet aussieht, gibt es keinen
 * Ort, an dem man nachsieht, was eigentlich gegatet ist.
 *
 * Seit dem dritten und vierten Test-Kontext (`__PROJECTOR_BUILD__='false'`)
 * kommt eine zweite Achse dazu — dieselbe Duplikation ein zweites Mal wäre
 * absehbar unhaltbar. Deshalb hier einmal, benannt, mit den Kombinatoren
 * daneben.
 *
 * BEWUSST NICHT getan: die 15 bestehenden `IS_SERVER_BUILD`-Ableitungen in den
 * Testdateien umgebaut. Das gehört zu Karte #345 (F-386), die genau diese
 * Buchhaltung aufräumt — hier würde es den Diff der Build-Kontext-Arbeit
 * unlesbar machen. Neue Stellen nutzen diesen Helfer.
 */

import { describe, it } from 'vitest';

/* Die `typeof`-Prüfung ist nicht defensiv-überflüssig: Vitest ersetzt die
   Bezeichner per `define`, aber ein Kontext ohne die Definition (z. B. ein
   ad-hoc `npx vitest` ohne --config) würde sonst mit ReferenceError sterben
   statt mit einer sinnvollen Default-Annahme. */
const wahr = v => v === true || v === 'true';

export const IS_SERVER_BUILD =
  (typeof __SERVER_BUILD__ !== 'undefined') && wahr(__SERVER_BUILD__);

export const IS_PROJECTOR_BUILD =
  (typeof __PROJECTOR_BUILD__ !== 'undefined') && wahr(__PROJECTOR_BUILD__);

/**
 * Welches ausgelieferte Artefakt spiegelt der laufende Kontext?
 * Siehe ADR-0116 §Matrix. `server+projektor` ist die einzige Kombination
 * OHNE Artefakt — sie existiert als ADR-0072-Test-Invariante (die
 * Projektor-Schicht ist datenverlust-kritisch und wird zusätzlich unter
 * Server-Bedingungen voll getestet).
 */
export const BUILD_CONTEXT =
  IS_SERVER_BUILD && IS_PROJECTOR_BUILD ? 'server+projektor'
  : IS_SERVER_BUILD                     ? 'server-frontend'
  : IS_PROJECTOR_BUILD                  ? 'claude-code'
  :                                       'slim';

/* ── Kombinatoren ─────────────────────────────────────────────────────
   Nutzung: `describeProjektor('...', () => {...})` statt
   `(IS_PROJECTOR_BUILD ? describe : describe.skip)('...', ...)`.
   Die `.skip`-Variante (statt Weglassen) ist Absicht: die Suite bleibt in
   der Skip-Buchhaltung sichtbar, statt lautlos zu verschwinden — sonst
   sieht ein Kontext mit weniger Tests aus wie ein Kontext, in dem nichts
   fehlt (F-386-Klasse). */
/* Bewusst nur die tatsächlich benutzten Kombinatoren. Der Sprint-53-Kritiker
   hat vier ungenutzte Exporte gefunden — ein eigenes Namensschema ohne
   Anwendung ist ein Drift-Kandidat: die nächste Testdatei erfindet einen
   fünften Namen, weil der vierte nicht auffindbar war. Wer eine weitere
   Variante braucht, ergänzt sie hier MIT dem ersten Aufrufer. */
export const describeProjektor = IS_PROJECTOR_BUILD ? describe : describe.skip;
export const itProjektor       = IS_PROJECTOR_BUILD ? it : it.skip;
export const itOhneProjektor   = IS_PROJECTOR_BUILD ? it.skip : it;
export const describeServer    = IS_SERVER_BUILD    ? describe : describe.skip;
/* Erster Aufrufer: tests/i18n-key-existence-guard.test.js (F-750). Zwei
   UNABHÄNGIGE Gate-Blöcke in src/i18n.js (`if (IS_SERVER_BUILD) {...}` und
   `if (IS_PROJECTOR_BUILD) {...}`, ADR-0072) ADDIEREN Keys auf dieselbe
   Basis-Dict (nie entfernt) — die echte Vereinigungsmenge existiert deshalb
   nur, wenn BEIDE Flags true sind ('server+projektor' in BUILD_CONTEXT,
   vitest.config.server.js — die einzige Kombination ohne eigenes
   Artefakt, siehe ADR-0116). Ein einzelner `it` muss deshalb genau dort
   laufen, ohne die restliche Suite (Struktur-Selbsttests) zu skippen. */
export const itServerProjektor  = (IS_SERVER_BUILD && IS_PROJECTOR_BUILD) ? it : it.skip;
/* Erster Aufrufer: tests/i18n-slim-reachable-key-guard.test.js
   (Sprint-60-Kritiker-Nachlese). Gegenstück zu itServerProjektor: dort wird
   geprüft, dass server+projektor-Keys auflösen; hier wird geprüft, dass Keys,
   deren Aufrufer im Solo-/Dogfooding-Build ERREICHBAR sind (nicht hinter
   IS_SERVER_BUILD), nicht fälschlich NUR im Server-Block von src/i18n.js
   stehen. 'slim' ist der Kontext ohne beide Flags (vitest.config.slim.js,
   spiegelt aboard.template.html — das Artefakt mit dem harten Bundle-Limit). */
export const itSlim = (!IS_SERVER_BUILD && !IS_PROJECTOR_BUILD) ? it : it.skip;
/* Erster Aufrufer: tests/nachzuegler-451.test.js (F-144). Gegenstueck zu
   describeServer: Die Sidebar-Datei-Sektion existiert im Server-Build
   bauartbedingt nicht (Karte #207 blendet sie aus, und `Config.set('boardMode',
   'solo')` clampt im server-Kontext auf 'server'). Ein Test, der die
   Datei-Sektion braucht, kann dort nicht bestehen — er wird uebersprungen,
   statt den Kontext mit einer Bedingung zu verwaessern, die im Zielkontext
   nichts mehr prueft. */
export const describeOhneServer = IS_SERVER_BUILD ? describe.skip : describe;
/* Erster Aufrufer: tests/briefkasten-pruefung.test.js (Karte #524, F-1008).
   Für reine Werkzeug-/CLI-Tests ohne jeden Bezug zu __SERVER_BUILD__ oder
   __PROJECTOR_BUILD__ (sie rufen ein Node-Skript per execFileSync auf, das
   von keinem der beiden Flags abhängt) ist es reine Redundanz, in allen vier
   Kontexten identisch zu laufen — vier identische Läufe desselben git-
   Subprozess-Tests kosten Zeit, ohne zusätzlich etwas zu prüfen. `BUILD_CONTEXT
   === 'claude-code'` (== vitest.config.js, `bun run test:standalone`) ist die
   willkürliche, aber stabile Wahl für „genau einmal" — dieselbe Suite bleibt
   in der Skip-Buchhaltung der anderen drei Kontexte sichtbar (Klasse F-386),
   statt lautlos zu verschwinden.
   ⚠️ Nachgemessen, nicht angenommen: `tests/artefakt-kopplung.test.js` — das
   Vorbild für Werkzeug-Tests dieser Art — nutzt DIESEN Kombinator (noch)
   NICHT und läuft deshalb selbst weiterhin unnötig viermal. Das ist eine
   bestehende Redundanz, kein Vorbild für „hier ist Gaten überflüssig"; siehe
   den Hinweis dazu im Abschlussbericht von Karte #524. */
export const describeWerkzeug = (BUILD_CONTEXT === 'claude-code') ? describe : describe.skip;
