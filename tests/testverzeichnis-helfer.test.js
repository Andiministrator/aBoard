/* Sprint-81-Kritiker B-1/B-2: der Aufräumpfad von tests/helpers/testverzeichnis.js
   ist Löschcode — er darf nichts ausserhalb von tmp/testlauf anfassen.

   B-1: `entsperren()` setzte jede DATEI auf 0600; über einen Hardlink (lokaler
   `git clone`) traf das eine Datei ausserhalb. Jetzt nur Verzeichnisse.
   B-2: `istTestverzeichnis()` verglich die rohe Zeichenkette — `<TESTLAUF>/../..`
   (die Repo-Wurzel) bestand die Prüfung. Jetzt erst auflösen. */
import { describe, it, expect } from 'vitest';
import { chmodSync, linkSync, mkdtempSync, rmSync, statSync, writeFileSync, mkdirSync, symlinkSync } from 'node:fs';
import { join } from 'node:path';
import {
  neuesTestverzeichnis, testverzeichnisseAufraeumen, istTestverzeichnis, TESTLAUF,
} from './helpers/testverzeichnis.js';

describe('Testverzeichnis-Helfer: Aufräumen bleibt in tmp/testlauf (Kritiker B-1, B-2)', () => {
  it('B-2: ein Pfad mit .. aus tmp/testlauf heraus zählt nicht als Testverzeichnis', () => {
    expect(istTestverzeichnis(TESTLAUF + '/../..')).toBe(false);
    expect(istTestverzeichnis(TESTLAUF + '/x/../../y')).toBe(false);
    expect(istTestverzeichnis(TESTLAUF + '-x/a')).toBe(false);
    expect(istTestverzeichnis(TESTLAUF)).toBe(false);
    expect(istTestverzeichnis('')).toBe(false);
    expect(istTestverzeichnis(TESTLAUF + '/abc')).toBe(true);
  });

  it('V-2: ein Symlink IM Pfad nach aussen zählt nicht als Testverzeichnis', () => {
    /* Das Aussen-Ziel ist ein eigener Wegwerf-Ordner unter <repo>/tmp/ —
       ausserhalb von tmp/testlauf, aber nie echter Projektbestand. */
    /* Frischer Klon ohne tmp/ (öffentlicher Abzug, #1180): Elternordner erst anlegen. */
    mkdirSync(join(TESTLAUF, '..'), { recursive: true });
    const aussen = mkdtempSync(join(TESTLAUF, '..', 'v2-aussen-'));
    const d = neuesTestverzeichnis('v2-');
    try {
      mkdirSync(join(aussen, 'sub'));
      symlinkSync(aussen, join(d, 'link'));
      expect(istTestverzeichnis(join(d, 'link'))).toBe(false);
      expect(istTestverzeichnis(join(d, 'link', 'sub'))).toBe(false);
      expect(istTestverzeichnis(join(d, 'link', 'gibt-es-nicht'))).toBe(false);
      expect(istTestverzeichnis(join(d, 'echt'))).toBe(true);
      /* W-1: `..` hinter einem Symlink — lexikalisch „drinnen“, real draussen. */
      expect(istTestverzeichnis(d + '/link/../x')).toBe(false);
      testverzeichnisseAufraeumen();
      expect(statSync(join(aussen, 'sub')).isDirectory()).toBe(true);
    } finally {
      rmSync(aussen, { recursive: true, force: true });
    }
  });

  it('B-1: Aufräumen ändert die Rechte einer per Hardlink verknüpften Datei NICHT', () => {
    /* Ziel bewusst NICHT über den Helfer angelegt: es steht für eine Datei
       ausserhalb des Aufräumbereichs (eigene Verwaltung, unten entfernt). */
    mkdirSync(TESTLAUF, { recursive: true });
    const draussen = mkdtempSync(join(TESTLAUF, 'b1-draussen-'));
    const ziel = join(draussen, 'ziel.txt');
    try {
      writeFileSync(ziel, 'x');
      chmodSync(ziel, 0o444);
      const d = neuesTestverzeichnis('b1-');
      linkSync(ziel, join(d, 'link.txt'));
      testverzeichnisseAufraeumen();
      expect((statSync(ziel).mode & 0o777).toString(8)).toBe('444');
    } finally {
      chmodSync(ziel, 0o644);
      rmSync(draussen, { recursive: true, force: true });
    }
  });

  it('Gegenprobe: ein Verzeichnis mit chmod 000 wird trotzdem aufgeräumt', () => {
    const d = neuesTestverzeichnis('b1-gesperrt-');
    const innen = join(d, 'innen');
    mkdirSync(innen);
    writeFileSync(join(innen, 'f'), 'x');
    chmodSync(innen, 0o000);
    testverzeichnisseAufraeumen();
    expect(() => statSync(d)).toThrow();
  });
});
