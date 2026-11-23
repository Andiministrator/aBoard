/* Zentraler Helfer für Testverzeichnisse (F-1312, Karte #646).

   REGEL
   Projektregel (Andi 2026-09-21): nichts nach /tmp, jedes Projekt hat sein
   eigenes tmp/. Tests legen ihre Wegwerf-Verzeichnisse deshalb NICHT mehr per
   `os.tmpdir()` an, sondern hier: `<repo>/tmp/testlauf/<praefix>XXXXXX`, eindeutig
   je Aufruf (mkdtemp). Das Elternverzeichnis wird bei Bedarf angelegt.
   Gegenstück in Python: framework/hooks/tests/_umgebung.py.

   FALLSTRICK (Fehlerklasse 105, knowledge/topics/claude-projekt-framework.md)
   Ein Verzeichnis INNERHALB des Repos lässt jede Aufwärtssuche (git, aber auch
   eigene Wurzelsuchen) im umgebenden aBoard-Repo landen. Deshalb setzt dieses
   Modul `GIT_CEILING_DIRECTORIES` auf `tmp/testlauf` — Kindprozesse erben
   `process.env`, git steigt dann nie in dieses Verzeichnis hinauf. Ein Test mit
   eigenem `git init` ist davon nicht betroffen (sein `.git` liegt darunter).
   Nur git-Befehle mit `cwd` im echten Repo (REPO_ROOT) bleiben unberührt. */
import { chmodSync, mkdirSync, mkdtempSync, readdirSync, rmSync, lstatSync, realpathSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_WURZEL = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

/** Absoluter Pfad `<repo>/tmp/testlauf`. */
export const TESTLAUF = join(REPO_WURZEL, 'tmp', 'testlauf');

function ceilingSetzen() {
  const alt = process.env.GIT_CEILING_DIRECTORIES;
  const liste = alt ? alt.split(':') : [];
  if (!liste.includes(TESTLAUF)) {
    process.env.GIT_CEILING_DIRECTORIES = [TESTLAUF, ...liste].join(':');
  }
}

const angelegt = [];

/* Rechte zurücksetzen, damit auch ein Test mit chmod 000 aufgeräumt werden kann.
   NUR Verzeichnisse (Sprint-81-Kritiker B-1): zum Löschen zählen allein die
   Rechte des Verzeichnisses. Eine Datei anzufassen hiesse, über einen Hardlink
   (z. B. aus einem lokalen `git clone`) die Rechte einer Datei AUSSERHALB von
   tmp/testlauf zu ändern — lstat erkennt Symlinks, aber keine Hardlinks. */
function entsperren(pfad) {
  try {
    const st = lstatSync(pfad);
    if (!st.isDirectory()) return;
    chmodSync(pfad, 0o700);
    for (const n of readdirSync(pfad)) entsperren(join(pfad, n));
  } catch { /* weg oder nicht lesbar: dann hilft auch rmSync mit force */ }
}

/** Entfernt alle von diesem Test-Worker angelegten Verzeichnisse (nur unter tmp/testlauf). */
export function testverzeichnisseAufraeumen() {
  while (angelegt.length) {
    const d = angelegt.pop();
    if (!istTestverzeichnis(d)) continue;
    entsperren(d);
    try { rmSync(d, { recursive: true, force: true }); } catch { /* beim nächsten Lauf sichtbar */ }
  }
}

/* Beim Import (also auf Datei-Ebene, nicht mitten im Test — vitest verbietet Hooks
   innerhalb von Tests) anmelden; je Testdatei ein frisches Modul. */
if (typeof globalThis.afterAll === 'function') globalThis.afterAll(testverzeichnisseAufraeumen);

/** Legt ein neues, eindeutiges Testverzeichnis an und liefert den Pfad.
    Aufräumen: am Ende der Testdatei automatisch (afterAll beim Import). */
export function neuesTestverzeichnis(praefix = 'test-') {
  mkdirSync(TESTLAUF, { recursive: true });
  ceilingSetzen();
  const d = mkdtempSync(join(TESTLAUF, praefix));
  angelegt.push(d);
  return d;
}

/* Der Pfad mit allen Symlinks aufgelöst — auch wenn sein Ende (noch) nicht
   existiert: der nächste existierende Vorfahr wird per realpath aufgelöst,
   der Rest angehängt. Sonst bliebe `<TESTLAUF>/link/neu` mit `link` nach
   aussen unaufgelöst (Verify Sprint 81 V-2). */
function echtPfad(p) {
  let kopf = resolve(p);
  const rest = [];
  for (;;) {
    try { return join(realpathSync(kopf), ...rest.reverse()); } catch { /* weiter hoch */ }
    const oben = dirname(kopf);
    if (oben === kopf) return resolve(p);
    rest.push(kopf.slice(oben.length).replace(/^\//, ''));
    kopf = oben;
  }
}

/** Liegt `pfad` unter tmp/testlauf? (Schutz vor Aufräum-Fehlgriffen.)
    Erst auflösen, dann vergleichen (Sprint-81-Kritiker B-2): ein Pfad wie
    `<TESTLAUF>/../..` beginnt mit dem Präfix und ist doch die Repo-Wurzel. */
export function istTestverzeichnis(pfad) {
  if (typeof pfad !== 'string' || pfad === '') return false;
  /* Zweiter Verify Sprint 81 W-1: `resolve()` faltet `..` rein lexikalisch,
     das Betriebssystem folgt aber erst dem Symlink — `<TESTLAUF>/lnk/../x`
     läge dann ausserhalb. Kein Aufrufer braucht `..`, also pauschal ablehnen. */
  if (pfad.split(/[\\/]/).includes('..')) return false;
  /* Verify Sprint 81 V-2: auch Symlinks IM Pfad auflösen — `<TESTLAUF>/link/sub`
     mit `link` nach aussen bestand sonst die Prüfung. Gibt es den Pfad (noch)
     nicht, genügt `resolve()`: dann ist auch nichts zu löschen. */
  return echtPfad(pfad).startsWith(echtPfad(TESTLAUF) + '/');
}
