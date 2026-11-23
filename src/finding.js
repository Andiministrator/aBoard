/* Findings als fester Kartentyp — Wortschatz und Wertprüfung (ADR-0133 §1, Karte #666)
 *
 * Eine Finding-Karte trägt `kind: 'finding'` und ihre Fachfelder gesammelt
 * unter `card.finding = { nr, typ, prioritaet, beleg, lehre, status }`. Der
 * Befund selbst ist Titel plus Beschreibung der Karte — kein eigenes Feld.
 *
 * REINES Modul ohne Importe: `src/ops.js` (Fold in Browser, Projektor und —
 * gebündelt — am Server) prüft damit, und die Oberfläche (#667) kann dieselben
 * Listen für ihre Auswahlfelder nehmen. Ein Aufschrieb, keine Kopien
 * (knowledge `nebenlaeufigkeit-und-lesefenster.md` §30).
 *
 * Der Wortschatz ist der von `docs/findings.md` (Kopf, „geschlossenes
 * Vokabular"). Zwei Einträge tragen dort einen Zusatz — `eingeplant (Sprint N)`
 * und `teilweise behoben (…)`. Hier gibt es den Zusatz NICHT, weder im Status
 * noch als eigenes Feld:
 *   - den Sprint zeigt die Karte selbst — sie liegt in dessen Swimlane;
 *   - was teilweise behoben ist, gehört in Beschreibung oder Kommentar. Das
 *     sagt der Kasten in `docs/findings.md` für jede Nuance: „Die Nuance gehört
 *     in die Beschreibungsspalte, nicht in den Status."
 * Ein zusätzliches Freitextfeld wäre eine zweite Stelle für dieselbe Aussage
 * und für schwächere Modelle ein weiterer Ort, an dem sie falsch landet.
 */

export const FINDING_KIND = 'finding';

export const FINDING_TYPEN = /*#__PURE__*/ Object.freeze(['S', 'K', 'B', 'V', 'D']);
export const FINDING_PRIORITAETEN = /*#__PURE__*/ Object.freeze(['P0', 'P1', 'P2', 'P3']);
export const FINDING_STATUS = /*#__PURE__*/ Object.freeze([
  'offen', 'sofort', 'eingeplant', 'backlog', 'teilweise behoben',
  'behoben', 'akzeptiert', 'behandelt', 'aufgelöst', 'hinfällig',
]);
/* Die ersten fünf zählen in docs/findings.md als offen, die übrigen als erledigt. */
export const FINDING_STATUS_OFFEN = /*#__PURE__*/ Object.freeze(FINDING_STATUS.slice(0, 5));

/* Alle Schlüssel von `card.finding`. `nr` vergibt der Fold (nie der Schreiber). */
export const FINDING_FELDER = /*#__PURE__*/ Object.freeze(['nr', 'typ', 'prioritaet', 'beleg', 'lehre', 'status']);

/**
 * Nächste Finding-Nummer aus `board.nextFindingNr` — vergibt sie und hebt den
 * Zähler an (ADR-0133 §1). Eine Fassung für Fold (`src/ops.js`) und Anlegen im
 * Browser (`src/main.js`, `card:quick-add`): `ops.js` fehlt im slim Template,
 * die Oberfläche braucht die Vergabe dort trotzdem (F-1547). Das Gegenstück am
 * Server für den Kartentransfer ist `nextFindingNr` in
 * `server/storage/transfer.go`.
 *
 * Muster wie `nextDisplayId` in `src/ops.js`, mit einem Unterschied: Der
 * Zähler wird nicht nur repariert, wenn er fehlt, sondern auch, wenn er nicht
 * über der höchsten vergebenen Nummer liegt. Die Zusage ist „keine Doppel“ —
 * ein Board, dessen Zähler aus einem älteren Stand stammt (Rückspielung, ein
 * Schreiber, der das Feld nicht kennt), vergäbe sonst eine Nummer ein zweites
 * Mal. Boards ohne `nextFindingNr` beginnen bei max(nr) + 1, ohne Findings
 * bei 1. Gelöschte Findings hinterlassen eine Lücke, solange der Zähler lebt;
 * ihre Nummer kehrt nur zurück, wenn der Zähler verloren geht UND es die
 * höchste war — dagegen sperrt der Server das Absenken beim Ganz-Board-
 * Schreiben (F-1544, `server/storage/finding_zaehler.go`).
 *
 * Deterministisch: liest und schreibt nur `board`.
 *
 * @param {object} board
 * @returns {number} die vergebene Nummer
 */
export function nextFindingNr(board) {
  let max = 0;
  for (const s of board.sprints || []) for (const c of s.cards || []) {
    const nr = c && c.kind === FINDING_KIND && c.finding ? c.finding.nr : null;
    if (Number.isInteger(nr) && nr > max) max = nr;
  }
  if (!Number.isInteger(board.nextFindingNr) || board.nextFindingNr <= max) board.nextFindingNr = max + 1;
  return board.nextFindingNr++;
}

const WORTSCHATZ = {
  typ: FINDING_TYPEN,
  prioritaet: FINDING_PRIORITAETEN,
  status: FINDING_STATUS,
};

/**
 * Ist `wert` ein gültiges `card.finding`? Geprüft wird nur der Inhalt, nicht
 * die Nummer — ob `nr` passt, hängt am Bestand und entscheidet `src/ops.js`.
 *
 * - `typ`, `prioritaet`, `status`: Pflicht, genau ein Wort aus dem Wortschatz
 *   (Gross-/Kleinschreibung zählt — es ist ein geschlossener Wortschatz).
 * - `beleg`, `lehre`: Zeichenkette, oder fehlt/`null`.
 * - jeder andere Schlüssel: abgelehnt. Ein vertipptes `prio` wäre sonst ein
 *   Finding ohne Priorität, das niemand bemerkt.
 *
 * REIN: keine Zeit, kein Zufall, keine Ausgabe.
 *
 * @param {*} wert
 * @returns {null|{feld:string, text:string, unbekannt?:string}}
 *   `null` = gültig. Sonst das betroffene Feld und ein Satz Begründung;
 *   `unbekannt` trägt den fremden Schlüssel (für einen Namensvorschlag).
 */
export function findingFehler(wert) {
  if (wert == null) {
    return { feld: 'finding', text: 'Eine Finding-Karte braucht `finding` — ein Objekt'
      + ' { typ, prioritaet, status, beleg?, lehre? }.' };
  }
  if (typeof wert !== 'object' || Array.isArray(wert)) {
    return { feld: 'finding', text: `\`finding\` ist ein Objekt, nicht ${JSON.stringify(wert)}.` };
  }
  for (const k of Object.keys(wert)) {
    if (!FINDING_FELDER.includes(k)) {
      return { feld: `finding.${k}`, unbekannt: k,
        text: `\`finding.${k}\` gibt es nicht. Erlaubt sind: ${FINDING_FELDER.join(', ')}.` };
    }
  }
  for (const feld of ['typ', 'prioritaet', 'status']) {
    const v = wert[feld];
    const erlaubt = WORTSCHATZ[feld];
    if (v == null) {
      return { feld: `finding.${feld}`, text: `\`finding.${feld}\` fehlt. Erlaubt: ${erlaubt.join(', ')}.` };
    }
    if (!erlaubt.includes(v)) {
      let text = `\`finding.${feld}\` ist ${JSON.stringify(v)} — erlaubt: ${erlaubt.join(', ')}.`;
      /* Der häufigste Fehlgriff: den Zusatz aus docs/findings.md mitschreiben. */
      const wort = typeof v === 'string' ? erlaubt.find(w => v.startsWith(w + ' ')) : null;
      if (feld === 'status' && wort) {
        text += ` Ohne Zusatz schreiben („${wort}“); den Sprint zeigt die Swimlane der Karte,`
             + ' Einzelheiten gehören in Beschreibung oder Kommentar.';
      }
      return { feld: `finding.${feld}`, text };
    }
  }
  for (const feld of ['beleg', 'lehre']) {
    const v = wert[feld];
    if (v != null && typeof v !== 'string') {
      return { feld: `finding.${feld}`, text: `\`finding.${feld}\` ist Text (Zeichenkette) oder fehlt.` };
    }
  }
  return null;
}
