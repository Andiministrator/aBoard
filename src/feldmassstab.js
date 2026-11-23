/* Feld-Massstab für per Op neu angelegte Karten (F-925, F-1046).
 *
 * WOZU: Beide Projektoren (headless `tools/aboard-projektor.mjs`, Browser
 * `src/sync.js`) melden eine `card.upsert`-Neuanlage, der Felder fehlen. Den
 * Vergleich macht `fold` (`fehlendeMassstabFelder` in `src/ops.js`); WOGEGEN
 * verglichen wird, steht hier.
 *
 * WARUM DIE FABRIK UND NICHT DER BESTAND (F-1046, Fehlerklasse 88a): Bis
 * Sprint 68 war der Massstab die Schnittmenge der Felder aller Karten in der
 * base. Nach der nächsten Kompaktierung stand die beanstandete Karte selbst in
 * der base — und der Massstab des Folgelaufs hatte ihre Lücken übernommen.
 * Gemessen vom Sprint-67-Kritiker: die zweite `{id,title}`-Neuanlage wurde nur
 * noch mit den harmlosen Feldern gemeldet, `description, qa, labels, comments`
 * fehlten in der Meldung. Die Karten-Fabrik (`Model.createCard`) kann der
 * beurteilte Bestand nicht beschreiben; sie ist die Stelle, an der jede
 * legitime Karte entsteht.
 *
 * ABGELEITET, NICHT AUFGESCHRIEBEN: Beide Teilmengen werden bei jedem Aufruf
 * aus dem Code gemessen, der sie bestimmt — die Feldliste aus
 * `Model.createCard`, die „ergänzt"-Menge aus einem Probelauf von
 * `normalisiereProjektion` (ADR-0127) über eine nackte Karte. Eine
 * festgeschriebene Liste wäre die Widerlegungsbedingung von F-1046 in
 * Reinform: Bekommt die Fabrik ein Feld, veraltet die Liste, ohne dass es
 * jemand merkt. So kann sie das per Konstruktion nicht.
 *
 * NICHT IN `src/ops.js`: Der Fold ist rein und importiert nichts (ADR-0067 §2,
 * DCE). Und er füllt nichts auf — das bräche die Round-Trip-Invariante
 * (ADR-0079, Fehlerklasse 87). Hier wird nur der Massstab gebaut; gemeldet
 * wird im Aufrufer.
 *
 * KOSTEN: eine Karte anlegen und ein Ein-Karten-Board normalisieren — im
 * Browser einmal je Poll (15 s). Bewusst ohne Cache: ein Cache wäre der Ort,
 * an dem der Massstab doch wieder veraltet (und die Tests, die die Fabrik
 * austauschen, bräuchten einen Rücksetz-Haken). */

import { Model } from './model.js';
import { normalisiereProjektion } from './storage.js';
import { VOM_FOLD_BEI_NEUANLAGE_GESETZT } from './ops.js';

/**
 * @returns {{felder: string[], ergaenzt: string[]}}
 *   `felder`   — alle Felder, die eine Neuanlage tragen sollte (Fabrik minus
 *                dem, was der Fold selbst setzt: `displayId`, `columnId`)
 *   `ergaenzt` — die Teilmenge davon, die `normalisiereProjektion` bei jedem
 *                Lauf beider Projektoren nachträgt; fehlen sie in der Op, steht
 *                trotzdem ein Wert in der Projektion
 */
export function kartenFeldMassstab() {
  const fabrik = Model.createCard('');
  const felder = Object.keys(fabrik).filter(k => !VOM_FOLD_BEI_NEUANLAGE_GESETZT.includes(k));

  /* Probe: eine Karte, die nur trägt, was jede Neuanlage nach dem Fold hat
     (`id`, `title`, dazu die beiden Fold-Felder). Was danach zusätzlich auf
     ihr steht, hat die Normalisierung ergänzt. */
  const nackt = { id: 'feldmassstab-probe', title: '', displayId: 1, columnId: 'col-backlog' };
  const probe = normalisiereProjektion({
    version: '2',
    board: {
      id: 'feldmassstab-probe', title: '', nextDisplayId: 2,
      columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
      sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [nackt] }],
    },
  });
  const normalisiert = probe?.board?.sprints?.[0]?.cards?.find(c => c && c.id === nackt.id);
  const ergaenzt = normalisiert
    ? felder.filter(k => !(k in nackt) && k in normalisiert)
    : [];
  return { felder, ergaenzt };
}

/**
 * Teilt die fehlenden Felder einer Neuanlage in die, die die Normalisierung
 * nachträgt, und die, die dauerhaft fehlen. Nur die zweite Gruppe ist ein
 * Befund: seit ADR-0127 normalisieren beide Projektoren die Projektion, die
 * erste Gruppe steht dort also ohnehin.
 *
 * @param {string[]} fehlen
 * @param {{ergaenzt: string[]}} massstab  Ergebnis von `kartenFeldMassstab()`
 * @returns {{ergaenzt: string[], dauerhaft: string[]}}
 */
export function teileFehlendeFelder(fehlen, massstab) {
  const ergaenztMenge = new Set(massstab?.ergaenzt || []);
  const liste = Array.isArray(fehlen) ? fehlen : [];
  return {
    ergaenzt:  liste.filter(k => ergaenztMenge.has(k)),
    dauerhaft: liste.filter(k => !ergaenztMenge.has(k)),
  };
}
