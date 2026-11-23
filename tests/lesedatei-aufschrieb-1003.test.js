/**
 * F-2750 (Karte #1003): Wächter gegen ein vergessenes Hochzählen von
 * LESEDATEI_AUFSCHRIEB (src/lesedatei.js).
 *
 * F-2739 trägt nur, wenn jede Änderung am Aufschrieb den Stand hochzählt — sonst
 * erreicht sie vorhandene Lesedateien erst bei der nächsten Änderung der
 * Projektion. Der Test hält einen Fingerabdruck des Aufschriebs (ab
 * `isoMitOffset` bis Dateiende: lesedateiZeit, LESEDATEI_VOLL_MAX,
 * LESEDATEI_SCHLANK_FELDER, baueLesedatei, lesedateiText) samt Stand fest.
 * Kommentare und Leerraum zählen nicht mit.
 *
 * Wird er rot: Ändert die Änderung, was in der Lesedatei steht, dann
 * LESEDATEI_AUFSCHRIEB hochzählen und beide Werte unten nachtragen. Ändert sie
 * nichts am Inhalt (reine Umformung), nur den Fingerabdruck nachtragen.
 */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { join } from 'node:path';
import { LESEDATEI_AUFSCHRIEB } from '../src/lesedatei.js';

const STAND = { aufschrieb: 3, fingerabdruck: '29a1308deb799bef4a85393e366a44f6494bd9408055a0e4b365c2d0924f1a38' };

function fingerabdruck(quelle) {
  const ab = quelle.indexOf('export function isoMitOffset');
  if (ab < 0) throw new Error('Anker „export function isoMitOffset“ fehlt in src/lesedatei.js');
  const teil = quelle.slice(ab).replace(/\/\*[\s\S]*?\*\//g, '').replace(/\s+/g, ' ').trim();
  return createHash('sha256').update(teil).digest('hex');
}

describe('F-2750 — LESEDATEI_AUFSCHRIEB folgt dem Aufschrieb', () => {
  const quelle = readFileSync(join(process.cwd(), 'src/lesedatei.js'), 'utf8');

  it('Fingerabdruck und Stand sind gemeinsam festgehalten', () => {
    expect({ aufschrieb: LESEDATEI_AUFSCHRIEB, fingerabdruck: fingerabdruck(quelle) }).toEqual(STAND);
  });

  it('Gegenprobe: eine Änderung am Aufschrieb ändert den Fingerabdruck, ein Kommentar nicht', () => {
    const geaendert = quelle.replace("'effort', 'complexity'", "'effort', 'complexity', 'labels'");
    expect(geaendert).not.toBe(quelle);
    expect(fingerabdruck(geaendert)).not.toBe(STAND.fingerabdruck);
    const kommentar = quelle.replace('export function baueLesedatei', '/* nur ein Kommentar */\nexport function baueLesedatei');
    expect(fingerabdruck(kommentar)).toBe(fingerabdruck(quelle));
  });
});
