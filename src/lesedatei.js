/* Lesedatei aboard-sync.offen.json (ADR-0145, Karten #981 und #984)

   Für Leser mit Größengrenze (Board-Seitenleiste: `$.fs.read` liest höchstens
   4 MiB): nur die offenen Karten, in derselben Hülle `board` wie die
   Projektion. Ein Aufschrieb für beide Projektoren (ADR-0145 Nachtrag #984):
   den CLI-Projektor (tools/aboard-projektor.mjs) und den Browser-Projektor
   (src/sync.js, nur im Projektor-Build). Rein: kein Dateizugriff, keine Uhr —
   `jetzt` und die Angaben zur Projektion kommen vom Aufrufer. */

export const LESEDATEI_FILE = 'aboard-sync.offen.json';

/* F-2739: Stand des Aufschriebs (`baueLesedatei`). Hochzählen bei jeder
   Änderung an baueLesedatei, sonst erreicht sie vorhandene Lesedateien erst
   bei der nächsten Änderung der Projektion. Steht als `aufschrieb` in der
   Datei; `lesedateiAktuell` vergleicht ihn. Leser müssen das Feld nicht
   kennen (`format` und `version` bleiben). */
export const LESEDATEI_AUFSCHRIEB = 3;
/* F-2750: tests/lesedatei-aufschrieb-1003.test.js hält einen Fingerabdruck des
   Aufschriebs (isoMitOffset bis Dateiende, ohne Kommentare) samt diesem Wert fest
   und wird rot, wenn der Code sich ändert und der Wert nicht. */

/* Kritik B-1 (29.10.): Darf der CLI-Projektor die Lesedatei nicht schreiben
   (F-2691), ersetzt er eine vorhandene durch diesen Platzhalter ohne Karten.
   Der Browser-Projektor erneuert eine gesperrte Datei nicht; Leser sehen kein
   `board` und nehmen die Projektion. */
export function lesedateiSperre(grund) {
  return JSON.stringify({ format: 'aboard-lesedatei', version: 1, gesperrt: grund }) + '\n';
}

export function lesedateiGesperrt(text) {
  try { return !!JSON.parse(text)?.gesperrt; } catch { return false; }
}

/* F-2702: EINE Regel für beide Projektoren (CLI und Browser): Eine vorhandene
   Lesedatei wird nicht neu geschrieben, wenn sie zur selben Projektion gehört
   (`projektion.sha256` gleich) — sonst wechselt `erzeugt` bei jedem Persist, und
   eine versionierte Lesedatei stünde im Arbeitsbaum dauerhaft als geändert.
   Ohne lesbaren Vermerk (Platzhalter, altes Format, kaputt) gilt sie als neu zu
   schreiben. F-2739: Dasselbe gilt, wenn ihr `aufschrieb` fehlt oder kleiner
   ist als der heutige LESEDATEI_AUFSCHRIEB — eine Änderung an baueLesedatei
   erreicht so auch Lesedateien zu unveränderter Projektion. F-2749: Ein
   GRÖSSERER `aufschrieb` gilt als aktuell — CLI- und Browser-Projektor laufen
   beim Konsumenten oft auf verschiedenen Ständen; ein älterer Stand stuft die
   Datei eines neueren nicht zurück, sonst schrieben sie sich gegenseitig neu. */
export function lesedateiAktuell(text, sha256) {
  try {
    const f = JSON.parse(text);
    return !!f && f.format === 'aboard-lesedatei' && f.version === 1 && !f.gesperrt
      && Number.isInteger(f.aufschrieb) && f.aufschrieb >= LESEDATEI_AUFSCHRIEB
      && !!f.board && typeof f.projektion?.sha256 === 'string' && f.projektion.sha256 === sha256;
  } catch { return false; }
}

export function isoMitOffset(d) {
  const z = (n, l = 2) => String(n).padStart(l, '0');
  const off = -d.getTimezoneOffset();
  const vz = off >= 0 ? '+' : '-';
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`
    + `T${z(d.getHours())}:${z(d.getMinutes())}:${z(d.getSeconds())}.${z(d.getMilliseconds(), 3)}`
    + `${vz}${z(Math.floor(Math.abs(off) / 60))}:${z(Math.abs(off) % 60)}`;
}

/* ADR-0145 §4: reines Datum bleibt Datum (eine erfundene Uhrzeit wäre
   falsch), ein Wert mit Uhrzeit wird Ortszeit mit Offset, Undeutbares fällt
   weg (undefined → JSON.stringify lässt das Feld aus). */
export function lesedateiZeit(wert) {
  if (typeof wert !== 'string') return undefined;
  if (/^\d{4}-\d{2}-\d{2}$/.test(wert)) return wert;
  if (!/^\d{4}-\d{2}-\d{2}T/.test(wert)) return undefined;
  const ms = Date.parse(wert);
  return Number.isFinite(ms) ? isoMitOffset(new Date(ms)) : undefined;
}

export const LESEDATEI_VOLL_MAX = 3 * 1024 * 1024;   /* Reserve zur 4-MiB-Grenze des Mods */
/* F-2787: `claim` gehört dazu. Die Leiste liest bei Boards über 4 MiB nur diese
   Kurzfassung; ohne Sperre sähe sie weder Halter noch „freigeben“. */
export const LESEDATEI_SCHLANK_FELDER = ['id', 'displayId', 'title', 'kind', 'owner', 'assignees',
  'priority', 'critical', 'blocked', 'qa', 'columnId', 'updated', 'dueDate', 'effort', 'complexity',
  'claim'];

export function baueLesedatei(board, inhalt, projektionInfo, jetzt) {
  const defs = Array.isArray(board.columnDefs) ? board.columnDefs : [];
  /* Dieselbe Regel wie die Seitenleiste: ID oder Titel, weil manche Boards
     eigene Spalten-IDs führen. */
  const erledigt = new Set(['col-erledigt', ...defs
    .filter(d => d && String(d.title ?? '').trim().toLowerCase() === 'erledigt')
    .map(d => String(d.id))]);
  const alle = new Map();
  for (const s of board.sprints || []) {
    for (const c of (s && s.cards) || []) if (c && typeof c.id === 'string') alle.set(c.id, { c, s });
  }
  const sprints = [];
  const drin = new Set();
  for (const s of board.sprints || []) {
    if (!s || typeof s !== 'object' || s.archived === true) continue;
    const karten = ((s.cards) || []).filter(c => c && typeof c === 'object');
    const offen = karten.filter(c => c.archived !== true && !erledigt.has(c.columnId));
    const zahlen = {
      erledigt: karten.filter(c => erledigt.has(c.columnId)).length,
      gesamt: karten.filter(c => c.archived !== true || erledigt.has(c.columnId)).length,
    };
    const cards = offen.map(c => {
      const k = inhalt === 'voll'
        ? { ...c }
        : Object.fromEntries(LESEDATEI_SCHLANK_FELDER.filter(f => f in c).map(f => [f, c[f]]));
      if ('updated' in k) k.updated = lesedateiZeit(c.updated);
      drin.add(c.id);
      return k;
    });
    sprints.push({ id: s.id, title: s.title, kind: s.kind, zahlen, cards });
  }
  /* F-2789: Die Leiste (Mod) bestimmt Akteur und Bearbeiter-Auswahl aus den
     Identitäten des Boards. Nur `kind` und `label` (Datensparsamkeit: weder
     E-Mail noch Bild noch Farbe). */
  const identities = (Array.isArray(board.identities) ? board.identities : [])
    .filter(i => i && typeof i === 'object' && typeof i.label === 'string')
    .map(i => ({ kind: i.kind, label: i.label }));
  const verweisZiele = {};
  for (const sp of sprints) for (const k of sp.cards) {
    const quelle = alle.get(k.id)?.c;
    for (const l of (quelle && Array.isArray(quelle.links)) ? quelle.links : []) {
      const ziel = l && alle.get(l.targetId);
      if (!ziel || drin.has(l.targetId)) continue;
      verweisZiele[l.targetId] = { displayId: ziel.c.displayId, title: ziel.c.title,
        columnId: ziel.c.columnId, sprintId: ziel.s.id };
    }
  }
  return {
    format: 'aboard-lesedatei', version: 1, aufschrieb: LESEDATEI_AUFSCHRIEB,
    erzeugt: isoMitOffset(jetzt), inhalt,
    projektion: projektionInfo,
    board: { id: board.id, title: board.title, columnDefs: defs, identities, sprints, verweisZiele },
  };
}

/* ADR-0145 §3: die volle Fassung, solange sie unter LESEDATEI_VOLL_MAX
   bleibt, sonst die schlanke. Liefert den Dateitext samt Schlusszeilenumbruch
   und welche Fassung es wurde (der CLI-Projektor meldet den Wechsel). */
export function lesedateiText(board, projektionInfo, jetzt) {
  const laenge = t => new TextEncoder().encode(t).length;
  const voll = JSON.stringify(baueLesedatei(board, 'voll', projektionInfo, jetzt)) + '\n';
  if (laenge(voll) <= LESEDATEI_VOLL_MAX) return { text: voll, inhalt: 'voll', vollBytes: laenge(voll) };
  return { text: JSON.stringify(baueLesedatei(board, 'schlank', projektionInfo, jetzt)) + '\n',
    inhalt: 'schlank', vollBytes: laenge(voll) };
}
