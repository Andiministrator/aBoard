/**
 * Kalender-Datums-Helfer (ADR-0103, Karte #308, Etappe 1).
 *
 * Reine, seiteneffektfreie Funktionen für die Kalender-Ansicht: Monatslängen,
 * Schaltjahre, ISO-8601-Kalenderwoche, Wochenstart (Mo), Monats-Grid.
 *
 * KEIN externes Date-Lib (Bundle-Argument ADR-0103/ADR-0110) — `Intl.DateTimeFormat`
 * übernimmt die lokalisierte Darstellung im Renderer (Etappe 2+), hier liegt nur
 * die kalendarische Logik.
 *
 * ── Zeitzonen-Doktrin (wichtig, siehe ADR-0034-Gotcha) ──────────────────────
 * aBoard-Datumsfelder (`dueDate`/`startDate`) sind zeitzonenlose Kalenderdaten
 * im Format `YYYY-MM-DD`. Die *Arithmetik* hier (addDays/diffDays/Wochen/Grid)
 * rechnet bewusst durchgängig in **UTC-Zivil-Datum** (`Date.UTC`): beide Enden
 * derselben Zeitzone → es gibt keinen Offset, der über eine DST-Grenze springen
 * könnte. Das ist DST-immun und unabhängig von der Zeitzone des Test-Runners.
 *
 * Die in der Karte gewarnte Falle ist `new Date('2026-03-29')` — das parst als
 * UTC-Mitternacht und liefert via `.getDate()` in Lokalzeit ein Off-by-one.
 * Diese Falle wird hier vermieden, indem ISO-Strings NIE über `new Date(string)`
 * geparst werden, sondern komponentenweise (`parseISO`).
 *
 * Der EINZIGE Ort, an dem Lokalzeit zählt, ist „welcher Tag ist heute?" — dafür
 * `todayISO()`, das eine Date-Instanz in Lokalzeit formatiert. Local- und
 * UTC-Pfad werden nie vermischt.
 *
 * Monats-Konvention: alle öffentlichen Funktionen nutzen `month` als 1–12
 * (passend zum ISO-String), NICHT die 0-basierte JS-Konvention.
 */

const DAY_MS = 86400000;

function pad2(n) {
  return String(n).padStart(2, '0');
}

/** Schaltjahr nach gregorianischer Regel. */
export function isLeapYear(year) {
  return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
}

/**
 * Anzahl Tage im Monat.
 * @param {number} year
 * @param {number} month 1–12
 */
export function daysInMonth(year, month) {
  const lengths = [31, isLeapYear(year) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return lengths[month - 1];
}

/**
 * `YYYY-MM-DD` → `{year, month, day}` (month 1–12) oder `null` bei Ungültigkeit.
 * Streng: prüft Bereich + reale Existenz (z. B. 2026-02-30 → null).
 */
export function parseISO(iso) {
  if (typeof iso !== 'string') return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!m) return null;
  const year = Number(m[1]), month = Number(m[2]), day = Number(m[3]);
  if (month < 1 || month > 12) return null;
  if (day < 1 || day > daysInMonth(year, month)) return null;
  return { year, month, day };
}

/** `{year, month, day}` → `YYYY-MM-DD`. month 1–12. */
export function toISO(year, month, day) {
  return `${String(year).padStart(4, '0')}-${pad2(month)}-${pad2(day)}`;
}

/** UTC-Zivil-Millisekunden eines ISO-Datums (interne Arithmetik-Basis). */
function isoToUTC(iso) {
  const p = parseISO(iso);
  if (!p) return null;
  return Date.UTC(p.year, p.month - 1, p.day);
}

/** UTC-Millisekunden → ISO-String. */
function utcToISO(ms) {
  const d = new Date(ms);
  return toISO(d.getUTCFullYear(), d.getUTCMonth() + 1, d.getUTCDate());
}

/**
 * Datum um `delta` Tage verschieben (delta kann negativ sein).
 * Roll-over über Monats-/Jahres-/Schaltjahresgrenzen korrekt.
 * @returns {string|null} ISO oder null bei ungültiger Eingabe
 */
export function addDays(iso, delta) {
  const base = isoToUTC(iso);
  if (base === null) return null;
  return utcToISO(base + delta * DAY_MS);
}

/**
 * Differenz in ganzen Tagen: `isoB - isoA`.
 * @returns {number|null}
 */
export function diffDays(isoA, isoB) {
  const a = isoToUTC(isoA), b = isoToUTC(isoB);
  if (a === null || b === null) return null;
  return Math.round((b - a) / DAY_MS);
}

/**
 * Wochentag, Montag-basiert: Mo=0 … So=6 (deutsche/ISO-Konvention).
 * @returns {number|null}
 */
export function weekdayMonday(iso) {
  const ms = isoToUTC(iso);
  if (ms === null) return null;
  return (new Date(ms).getUTCDay() + 6) % 7;
}

/** Wochenende (Sa/So)? */
export function isWeekend(iso) {
  const wd = weekdayMonday(iso);
  return wd === 5 || wd === 6;
}

/**
 * Montag der Woche, die `iso` enthält.
 * @returns {string|null}
 */
export function startOfWeek(iso) {
  const wd = weekdayMonday(iso);
  if (wd === null) return null;
  return addDays(iso, -wd);
}

/**
 * Die 7 Tage (Mo…So) der Woche, die `iso` enthält.
 * @returns {string[]}
 */
export function weekDays(iso) {
  const mon = startOfWeek(iso);
  if (mon === null) return [];
  return Array.from({ length: 7 }, (_, i) => addDays(mon, i));
}

/**
 * ISO-8601-Kalenderwoche eines Datums.
 * KW 1 ist die Woche, die den ersten Donnerstag des Jahres enthält.
 * Das ISO-Wochen-Jahr kann vom Kalenderjahr abweichen (z. B. 2021-01-01 → KW 53/2020).
 *
 * Rein in UTC gerechnet (kein DST-Einfluss auf die Wochen-Nummerierung).
 * @returns {{week:number, year:number}|null}
 */
export function getISOWeek(iso) {
  const p = parseISO(iso);
  if (!p) return null;
  // Donnerstag der laufenden Woche bestimmt KW + Wochen-Jahr.
  const d = new Date(Date.UTC(p.year, p.month - 1, p.day));
  const dayNum = (d.getUTCDay() + 6) % 7; // Mo=0…So=6
  d.setUTCDate(d.getUTCDate() - dayNum + 3); // Donnerstag dieser Woche
  const weekYear = d.getUTCFullYear();
  // Donnerstag der KW 1 (Woche mit dem 4. Januar).
  const firstThu = new Date(Date.UTC(weekYear, 0, 4));
  const firstDayNum = (firstThu.getUTCDay() + 6) % 7;
  firstThu.setUTCDate(firstThu.getUTCDate() - firstDayNum + 3);
  const week = 1 + Math.round((d.getTime() - firstThu.getTime()) / (7 * DAY_MS));
  return { week, year: weekYear };
}

/**
 * Monats-Grid: alle Tage vom Montag der ersten Woche bis zum Sonntag der
 * letzten Woche des Monats — immer volle Mo–So-Wochen (28/35/42 Zellen).
 * Geeignet als Datenquelle für das 7-Spalten-Monats-CSS-Grid (Etappe 2).
 * @param {number} year
 * @param {number} month 1–12
 * @returns {string[]} ISO-Daten in chronologischer Reihenfolge
 */
export function monthGrid(year, month) {
  const firstIso = toISO(year, month, 1);
  const lastIso = toISO(year, month, daysInMonth(year, month));
  const grid = [];
  let cur = startOfWeek(firstIso);
  // ISO-Strings sind lexikografisch == chronologisch → direkter Vergleich.
  while (cur <= lastIso || grid.length % 7 !== 0) {
    grid.push(cur);
    cur = addDays(cur, 1);
    if (grid.length > 42) break; // Sicherheitsnetz (nie erreicht)
  }
  return grid;
}

/** Gehört `iso` zum Monat `year`/`month` (1–12)? */
export function isInMonth(iso, year, month) {
  const p = parseISO(iso);
  return !!p && p.year === year && p.month === month;
}

/**
 * Heutiges Datum als ISO-String in **Lokalzeit** (einziger lokalzeit-abhängiger
 * Helfer — siehe Doktrin oben). Default `new Date()`; für deterministische
 * Tests eine feste Date-Instanz übergeben.
 * @param {Date} [now]
 */
export function todayISO(now = new Date()) {
  return toISO(now.getFullYear(), now.getMonth() + 1, now.getDate());
}

/**
 * Gibt eine Range (`startDate`→`dueDate`) als inklusive ISO-Tagesliste zurück.
 * Nur `dueDate` (kein/ungültiges start) → einzelner Tag. Vertauschte Reihenfolge
 * wird toleriert (start > due → als Einzeltag `due` behandelt).
 * @returns {string[]}
 */
export function dateRangeDays(startDate, dueDate) {
  if (!parseISO(dueDate)) return [];
  if (!parseISO(startDate)) return [dueDate];
  const span = diffDays(startDate, dueDate);
  if (span < 0) return [dueDate];
  return Array.from({ length: span + 1 }, (_, i) => addDays(startDate, i));
}

/**
 * ADR-0138 §1: Uhrzeit einer Karte (`startTime`/`dueTime`), `"HH:MM"` im
 * 24-Stunden-Format. Ortszeit ohne Zeitzone — der Browser rechnet nie um
 * (§2), deshalb genügt die Formprüfung.
 */
export function istUhrzeit(s) {
  return typeof s === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
}
