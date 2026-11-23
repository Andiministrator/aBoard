/* Deep-Link-Auflösung (F-447, Karte #366) — reine Funktionen, kein DOM.
 *
 * ## Warum es zwei Formate gibt (und beide bleiben)
 *
 * `#card-<displayId>` ist der historische Direktlink innerhalb des offenen
 * Boards (ADR-0028). Er kann nicht mehr: `displayId` ist nur **pro Board**
 * eindeutig.
 *
 * Genau daran scheiterte alles, was board-übergreifend verlinkt — und das sind
 * alle drei Produzenten im Server-Modus:
 *
 *   | Produzent                     | verlinkt                              |
 *   |-------------------------------|---------------------------------------|
 *   | `src/projects-view.js`        | Karten mehrerer Boards in einer Tabelle |
 *   | `server/api/ical.go`          | Kalender über Boards hinweg (ADR-0103) |
 *   | `server/notifications/email.go` | Karte eines Boards an einen Empfänger, der ein anderes offen hat (ADR-0107) |
 *
 * Sie erzeugen `#board=<id>&card=<ref>` — ein Format, das bis F-447 **niemand
 * geparst hat**. Jeder dieser Links landete auf der Board-Wurzel.
 *
 * Der naheliegende Fix („alle Produzenten auf `#card-<displayId>` umstellen")
 * wäre schlimmer als der Defekt: Eine Benachrichtigung über Karte #42 von
 * Board B würde dann Karte #42 von Board A öffnen — falsche Karte statt keiner
 * Karte. Deshalb wird der Router erweitert, nicht die Produzenten verkürzt.
 *
 * ## Warum `card=` beides sein darf
 *
 * Die drei Produzenten sind sich uneins: `projects-view.js` und `ical.go`
 * setzen die `displayId`, `email.go` die interne UUID. Statt sie auf einen
 * Wert zu zwingen, akzeptiert die Auflösung **beides**. Der Grund ist nicht
 * Bequemlichkeit: Benachrichtigungs-Mails sind längst verschickt und liegen in
 * Postfächern. Ein Vereinheitlichen der Produzenten würde die bereits
 * versendeten Links **nicht** reparieren — Toleranz beim Lesen tut es.
 */

/**
 * Zerlegt einen Location-Hash in ein Deep-Link-Ziel.
 *
 * Erkannt werden:
 *   - `#card-42`                  → { boardId: null, cardRef: '42' }
 *   - `#board=<id>&card=<ref>`    → { boardId: '<id>', cardRef: '<ref>' }
 *   - `#board=<id>`               → { boardId: '<id>', cardRef: '' }
 *
 * Alles andere (insbesondere der Kalender-Deep-Link `#/calendar?…`, ADR-0103)
 * liefert `null` und wird von seinem eigenen Router behandelt.
 *
 * Die Werte werden mit `URLSearchParams` dekodiert — bewusst nicht mit
 * `decodeURIComponent`: Go's `url.QueryEscape` (der Erzeuger in `email.go`,
 * F-377) kodiert das Leerzeichen als `+`. `URLSearchParams` dreht das korrekt
 * zurück, `decodeURIComponent` würde ein `+` im Ergebnis stehen lassen.
 *
 * @param {string} hash  z. B. `location.hash`
 * @returns {{boardId: string|null, cardRef: string}|null}
 */
export function parseCardHash(hash) {
  const h = typeof hash === 'string' ? hash : '';
  if (!h.startsWith('#')) return null;

  const kurz = /^#card-(\d+)$/.exec(h);
  if (kurz) return { boardId: null, cardRef: kurz[1] };

  /* Ohne `board=` ist es keiner unserer Links. Die Vorabprüfung hält
     `#/calendar?view=month` von URLSearchParams fern, das daraus sonst
     klaglos einen Parameter `/calendar?view` bastelt. */
  if (!/(^|[#&])board=/.test(h)) return null;
  let params;
  try { params = new URLSearchParams(h.slice(1)); } catch { return null; }
  const boardId = params.get('board');
  if (!boardId) return null;
  return { boardId, cardRef: params.get('card') || '' };
}

/**
 * Termin-Ziel eines Deep-Links `#board=<id>&termin=<termin-id>` (ADR-0138 §4,
 * Karte #743) — das Gegenstück zu `card=`. Liefert die Termin-id oder `''`.
 * Eigene Funktion statt eines Felds in `parseCardHash`: Termine gibt es nur
 * im Server-Build, und nur dort ruft `src/main.js` sie auf — im Solo-Template
 * fällt sie per Tree-Shaking weg. `parseCardHash` liefert für einen
 * Termin-Link `cardRef: ''`, öffnet also keine Karte.
 *
 * @param {string} hash  z. B. `location.hash`
 * @returns {string}
 */
export function parseTerminRef(hash) {
  const h = typeof hash === 'string' ? hash : '';
  if (!h.startsWith('#') || !/(^|[#&])termin=/.test(h)) return '';
  try { return new URLSearchParams(h.slice(1)).get('termin') || ''; } catch { return ''; }
}

/**
 * Sucht eine Karte im Board — per `displayId` ODER per interner `id`.
 *
 * Rein numerische Referenzen werden **zuerst** als `displayId` gedeutet, weil
 * das der Fall der beiden Frontend-/iCal-Produzenten ist; schlägt das fehl,
 * wird dieselbe Zeichenkette noch als `id` probiert. Eine UUID ist nie rein
 * numerisch, die beiden Räume überschneiden sich also nicht.
 *
 * Archivierte Karten werden bewusst **mitgefunden**: Ein Link auf eine
 * archivierte Karte soll sie öffnen, nicht ins Leere laufen — das Archiv ist
 * eine Sichtbarkeitsentscheidung der Spaltenansicht, keine Löschung.
 *
 * @param {object} board  `state.board`
 * @param {string} ref    `displayId` als Zeichenkette oder Karten-`id`
 * @returns {object|null} die Karte oder null
 */
export function findCardByRef(board, ref) {
  const sprints = board && Array.isArray(board.sprints) ? board.sprints : null;
  if (!sprints || typeof ref !== 'string' || ref === '') return null;
  const alsZahl = /^\d+$/.test(ref) ? parseInt(ref, 10) : null;
  if (alsZahl !== null) {
    for (const s of sprints) {
      const treffer = (s && s.cards || []).find(c => c && c.displayId === alsZahl);
      if (treffer) return treffer;
    }
  }
  for (const s of sprints) {
    const treffer = (s && s.cards || []).find(c => c && c.id === ref);
    if (treffer) return treffer;
  }
  return null;
}
