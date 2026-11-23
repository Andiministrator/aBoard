/* Dringlichkeit einer Karte (Karten #1004 und #1006): EINE Regel für das
   Web-Board (Sortierung „nach Dringlichkeit“) und die Board-Leiste in Claude
   Code (Kasten „Dringend aus dem Backlog“). Kleinere Stufe = dringender:

     0  kritisch (`critical: true`)
     1  überfällig (`dueDate` vor heute)
     2  bald fällig (`dueDate` heute bis in DRINGLICH_BALD_TAGE Tagen)
     3  Priorität hoch (`priority: 'hoch'`)
     4  alles andere

   Bei erledigten Karten zählt die Fälligkeit nicht: Eine erledigte Karte mit
   vergangenem Datum ist nicht dringend. Rein: `heute` ('YYYY-MM-DD', Ortszeit)
   kommt vom Aufrufer. Die Leiste rechnet dieselbe Regel selbst (ein Mod darf
   nichts außerhalb seines Ordners importieren); beide Seiten prüfen gegen
   tests/testdata/dringlichkeit-vektoren.json, der Wächter
   tests/dringlichkeit-1004.test.js hält die Literal-Kopie im Leisten-Test gleich. */

export const DRINGLICH_BALD_TAGE = 3;
export const DRINGLICH_REST = 4;

function tagNr(s) {
  const m = typeof s === 'string' ? /^(\d{4})-(\d{2})-(\d{2})/.exec(s) : null;
  return m ? Date.UTC(+m[1], +m[2] - 1, +m[3]) / 864e5 : null;
}

export function dringlichkeit(card, heute, { erledigt = false } = {}) {
  if (!card || typeof card !== 'object') return DRINGLICH_REST;
  if (card.critical === true) return 0;
  if (!erledigt) {
    const f = tagNr(card.dueDate), h = tagNr(heute);
    if (f != null && h != null) {
      if (f < h) return 1;
      if (f - h <= DRINGLICH_BALD_TAGE) return 2;
    }
  }
  if (card.priority === 'hoch') return 3;
  return DRINGLICH_REST;
}

export function heuteOrtszeit(d = new Date()) {
  const z = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`;
}
