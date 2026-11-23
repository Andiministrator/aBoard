/**
 * Karte #1137 / F-2991 (Kritiker Sprint 114 K-5), ADR-0148 §5.
 *
 * Quelltext-Wächter: Jedes rohe `fetch(` in `src/**` muss entweder
 *  (a) `holen(` sein (Fetch-Hülle aus sitzung-abgelaufen.js, meldet selbst), oder
 *  (b) in den nächsten MELDE_FENSTER Zeilen `meldeAntwort(` aufrufen (Altmuster,
 *      „fetch + Sitzung?.meldeAntwort von Hand“), oder
 *  (c) in AUSNAHMEN stehen — mit Datei, Erkennungsmerkmal (Teilstring der Zeile,
 *      keine Zeilennummer, die driftet) und Begründung.
 * Ein neues rohes fetch ohne Meldung macht den Test rot: ein 401 würde dann am
 * Band „Sitzung abgelaufen“ vorbeilaufen (F-2977).
 *
 * Zusätzlich zwei Quelltext-Wächter auf src/main.js (nicht importierbar: lädt mit
 * Seiteneffekten): Unterdrücken des Toasts in `sync:switch-to-board` bei
 * abgelaufener Sitzung und Purge-Meldung beim Karten-Löschen. Die Meldelogik selbst
 * ist in sitzung-fetch-huelle-1130 bzw. anhang-loeschhinweise-1136 verhaltensgetestet.
 *
 * Reiner Quelltext-Test, build-unabhängig (läuft in allen vier Kontexten).
 */
import { it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

const MELDE_FENSTER = 12; /* llm-stream: fetch in try/catch, Meldung 10 Zeilen später */

/* Jede Ausnahme: Datei, Erkennungsmerkmal (Teilstring der fetch-Zeile), Begründung. */
export const AUSNAHMEN = [
  { datei: 'src/sitzung-abgelaufen.js', merkmal: ': (...a) => fetch(...a);',
    grund: 'Die Hülle `holen` selbst; im Standalone-Build ist sie schlicht fetch ohne Serverbezug.' },
  { datei: 'src/sitzung-abgelaufen.js', merkmal: 'fetch(ANMELDUNG_ENDE',
    grund: 'Abmelden: die Antwort wird selbst ausgewertet, ein 401 ist dort kein „Sitzung abgelaufen“, es folgt ohnehin der Seitenwechsel.' },
  { datei: 'src/schatten-aussen.js', merkmal: "'/healthz'",
    grund: 'no-cors-Probe gegen fremden Ursprung: Antwort opaque, Status nicht lesbar, nicht unser Server.' },
  { datei: 'src/storage-plugin.js', merkmal: 'fetch(`data:',
    grund: 'data:-URL (lokale Base64-Umwandlung), kein Netzwerkzugriff.' },
  { datei: 'src/storage-plugin.js', merkmal: '.status; } catch { return null; }',
    grund: '/me-Probe von Sitzung.setzeProbe: liefert nur den Status an die Sitzungslogik, die ihn selbst auswertet.' },
  { datei: 'src/storage-plugin.js', merkmal: 'const res = await fetch(`${baseUrl()}/me`, { cache',
    grund: '/me-Abruf von Sitzung.setzeBetriebsartAbruf (klaereBetriebsart wertet den 401 selbst aus).' },
];

function dateien(dir) {
  const aus = [];
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) aus.push(...dateien(p));
    else if (p.endsWith('.js')) aus.push(p);
  }
  return aus;
}

const FETCH_AUFRUF = /(^|[^\w$.])(?:(?:window|globalThis|self)\.)?fetch\(/;

/** Zeilen ohne Kommentarzeilen; `fetch(` nur als Aufruf (kein `.fetch(`-Handler, kein `'fetch'`). */
export function findeUngemeldete(datei, quelle, ausnahmen = AUSNAHMEN) {
  const zeilen = quelle.split('\n');
  const befunde = [];
  let imBlock = false;
  zeilen.forEach((z, i) => {
    const t = z.trim();
    if (imBlock) { if (t.includes('*/')) imBlock = false; return; }
    if (t.startsWith('/*')) { if (!t.includes('*/')) imBlock = true; return; }
    if (t.startsWith('//') || t.startsWith('*')) return;
    const code = z.replace(/\/\*.*?\*\//g, '').replace(/\/\/.*$/, '');
    if (!FETCH_AUFRUF.test(code)) return;
    if (/\bholen\(/.test(code)) return;
    /* F-3014: Das Fenster endet vor dem nächsten fetch — sonst gälte die Meldung
       eines Nachbar-fetch auch für dieses. */
    const naechstesFetch = zeilen.findIndex((n, j) => j > i && FETCH_AUFRUF.test(n));
    const bis = Math.min(i + MELDE_FENSTER, naechstesFetch === -1 ? Infinity : naechstesFetch);
    const fenster = zeilen.slice(i, bis).join('\n');
    if (/meldeAntwort\(/.test(fenster)) return;
    if (ausnahmen.some(a => a.datei === datei && z.includes(a.merkmal))) return;
    befunde.push(`${datei}:${i + 1}: ${t}`);
  });
  return befunde;
}

const quelle = p => readFileSync(join(process.cwd(), p), 'utf8');

it('F-2991: kein rohes fetch( in src/ ohne Meldung an die Sitzung oder begründete Ausnahme', () => {
  const befunde = dateien(join(process.cwd(), 'src')).flatMap(p => {
    const rel = p.slice(process.cwd().length + 1);
    return findeUngemeldete(rel, readFileSync(p, 'utf8'));
  });
  expect(befunde, 'Neues fetch ohne Sitzung.meldeAntwort: `holen(...)` nehmen oder begründete Ausnahme eintragen').toEqual([]);
});

it('F-2991: jede Ausnahme trifft noch genau eine Zeile (keine Karteileichen) und hat eine Begründung', () => {
  for (const a of AUSNAHMEN) {
    expect(a.grund.length, a.merkmal).toBeGreaterThan(20);
    const treffer = quelle(a.datei).split('\n').filter(z => z.includes(a.merkmal));
    expect(treffer.length, `${a.datei}: ${a.merkmal}`).toBe(1);
  }
});

it('F-2991 Gegenprobe: ein neues rohes fetch in einem Server-Modul macht den Wächter rot', () => {
  const quell = "async function neu() {\n  const res = await fetch('/x');\n  return res.json();\n}\n";
  expect(findeUngemeldete('src/neu.js', quell)).toHaveLength(1);
  expect(findeUngemeldete('src/neu.js', "const r = await window.fetch('/x');")).toHaveLength(1);
  expect(findeUngemeldete('src/neu.js', "const r = await globalThis.fetch('/x');")).toHaveLength(1);
  /* grün: Hülle, Handmeldung, Kommentar */
  expect(findeUngemeldete('src/neu.js', "const r = await holen('/x');")).toEqual([]);
  expect(findeUngemeldete('src/neu.js', "const r = await fetch('/x');\nSitzung?.meldeAntwort(r.status);")).toEqual([]);
  expect(findeUngemeldete('src/neu.js', "// ruft fetch(...) auf\n/* fetch(x)\n fetch(y) */")).toEqual([]);
  /* F-3014: die Meldung eines folgenden fetch gilt nicht für das vorige */
  expect(findeUngemeldete('src/neu.js', "await fetch('/a');\nconst r = await fetch('/b');\nSitzung.meldeAntwort(r.status);")).toHaveLength(1);
  /* Meldung weit hinter dem Fenster zählt nicht */
  expect(findeUngemeldete('src/neu.js', "await fetch('/x');\n" + '\n'.repeat(MELDE_FENSTER) + 'Sitzung.meldeAntwort(1);')).toHaveLength(1);
});

it('main.js: sync:switch-to-board unterdrückt den Toast nur bei abgelaufener Sitzung im Server-Build (F-2977)', () => {
  const q = quelle('src/main.js');
  const start = q.indexOf("EventBus.on('sync:switch-to-board'");
  expect(start).toBeGreaterThan(-1);
  const ende = q.indexOf('\nEventBus.on(', start + 10);
  const block = q.slice(start, ende === -1 ? undefined : ende);
  /* Der Fehler-Toast hängt genau an dieser Bedingung … */
  expect(block).toMatch(/if \(!\(IS_SERVER_BUILD && result\.sitzungAbgelaufen\)\) showExportToast\(I18n\.t\(key\)\);/);
  /* … und es gibt keinen zweiten, ungeschützten Toast mit dem Schlüssel. */
  expect(block.match(/showExportToast\(I18n\.t\(key\)\)/g)).toHaveLength(1);
});

it('main.js: Karten-Löschen meldet das Purge-Ergebnis über Sitzung.anhangPurgeMelden (F-2990)', () => {
  const q = quelle('src/main.js');
  expect(q).toMatch(/const anhangPurgeMelden = IS_SERVER_BUILD \? e => Sitzung\.anhangPurgeMelden\(e\) : null;/);
  expect(q).toMatch(/purgeCardAttachments\(cardId\)\.then\(e => anhangPurgeMelden\(\[e\]\)\)/);
});
