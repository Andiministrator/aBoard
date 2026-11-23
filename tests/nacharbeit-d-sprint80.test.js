/* @vitest-environment happy-dom */

/**
 * Sprint-80-Nacharbeit, Strang D (Frontend):
 *   F-1453  Boot ohne Server-Board: dauerhafter Hinweis (BootHinweis)
 *   F-1459  gemeinsame Rückmeldungs-Helfer (rueckmeldung.js), eine kurzeId()
 *   F-1463  „ID kopieren“-Knöpfe der Board-Übersicht nennen das Board (aria-label)
 * Die Plugin-Seite von F-1453 (Sperre, kein Request) steht in storage-plugin.test.js,
 * F-1450/1462/1443/1463-Backup in rueckmeldung-627, admin-backup-verschluesselung-626
 * und anzeige-fehler-boardliste-637.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: async () => [
        { id: 'b-eins', name: 'Eins', role: 'owner', updatedAt: 1 },
        { id: 'b-zwei', name: 'Zwei', role: 'owner', updatedAt: 2 },
      ],
    }),
  },
}));
vi.mock('../src/state.js', () => ({ State: { get: () => ({ board: { id: 'b-eins' } }) } }));

import { BootHinweis } from '../src/boot-hinweis.js';
import { Rueckmeldung } from '../src/rueckmeldung.js';
import { BoardsView } from '../src/boards-view.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));

describeServer('BootHinweis — dauerhafter Hinweis bei nicht ladbarer Boardliste (F-1453)', () => {
  beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de'); });
  afterEach(() => { document.body.innerHTML = ''; vi.useRealTimers(); });

  it('zeigt einen Banner (role=alert) mit „NICHT gespeichert“ und Neu-laden-Knopf', () => {
    BootHinweis.zeige();
    const b = document.querySelector('.server-boot-banner');
    expect(b).toBeTruthy();
    expect(b.getAttribute('role')).toBe('alert');
    expect(b.textContent).toContain('NICHT gespeichert');
    expect(b.querySelector('button').textContent).toBe(I18n.t('sync.boardliste-neu-laden'));
  });

  it('bleibt stehen (kein Auto-Ausblenden wie ein Toast) und wird nicht verdoppelt', () => {
    vi.useFakeTimers();
    BootHinweis.zeige();
    BootHinweis.zeige();
    vi.advanceTimersByTime(60_000);
    expect(document.querySelectorAll('.server-boot-banner')).toHaveLength(1);
  });

  it('entferne() nimmt ihn weg (nach erfolgreichem Wechsel auf ein Server-Board)', () => {
    BootHinweis.zeige();
    BootHinweis.entferne();
    expect(document.querySelector('.server-boot-banner')).toBeNull();
  });

  it('main.js verdrahtet es: Sperre + Banner im !boot.ok-Zweig, kein neues Server-Board', () => {
    const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    const zweig = q.slice(q.indexOf('if (boot.ok) {'), q.indexOf('State.markClean();', q.indexOf('if (boot.ok) {')));
    expect(zweig).toContain('sperreSpeichern');
    expect(zweig).toContain('BootHinweis?.zeige()');
    expect(zweig).not.toMatch(/createBoard|postBoard/);
  });
});

describeServer('Rueckmeldung — gemeinsame Helfer (F-1459)', () => {
  it('meldeZeile ersetzt nur den Zustands-Modifikator, übrige Klassen bleiben', () => {
    const z = document.createElement('div');
    z.className = 'agz-meldung extra agz-meldung--busy';
    Rueckmeldung.meldeZeile(z, 'agz-meldung', 'err', 'Fehler');
    expect(z.className).toBe('agz-meldung extra agz-meldung--err');
    expect(z.textContent).toBe('Fehler');
    expect(z.getAttribute('role')).toBe('alert');
    Rueckmeldung.meldeZeile(z, 'agz-meldung', 'ok', 'gut');
    expect(z.classList.contains('agz-meldung--err')).toBe(false);
    expect(z.getAttribute('role')).toBe('status');
    Rueckmeldung.meldeZeile(z, 'agz-meldung', 'err', 'x', 'status');
    expect(z.getAttribute('role')).toBe('status');
  });

  it('blinkeKnopf setzt die Klasse und nimmt sie nach 1,5 s zurück; null-sicher', () => {
    vi.useFakeTimers();
    const b = document.createElement('button');
    Rueckmeldung.blinkeKnopf(b, 'bv-btn', 'ok');
    expect(b.classList.contains('bv-btn--ok')).toBe(true);
    vi.advanceTimersByTime(1600);
    expect(b.classList.contains('bv-btn--ok')).toBe(false);
    expect(() => Rueckmeldung.blinkeKnopf(null, 'bv-btn', 'ok')).not.toThrow();
    vi.useRealTimers();
  });

  it('kurzeId kürzt nur lange IDs (> 12 Zeichen) auf 8 + „…“', () => {
    expect(Rueckmeldung.kurzeId('default')).toBe('default');
    expect(Rueckmeldung.kurzeId('123456789012')).toBe('123456789012');
    expect(Rueckmeldung.kurzeId('0123456789abcdef')).toBe('01234567…');
    expect(Rueckmeldung.kurzeId(12345)).toBe('12345');
  });

  it('die Dialoge bauen die Logik nicht mehr selbst (Quelltext-Probe)', () => {
    const lies = f => readFileSync(join(process.cwd(), 'src', f), 'utf8');
    expect(lies('admin-dialog.js')).not.toMatch(/slice\(0,\s*8\)\s*\+\s*'…'/);
    expect(lies('boards-view.js')).not.toMatch(/setTimeout\(\(\) => btn\.classList\.remove/);
    expect(lies('agent-zugaenge-dialog.js')).not.toMatch(/setTimeout\(\(\) => btn\.classList\.remove/);
    expect(lies('admin-dialog.js')).not.toMatch(/setTimeout\(\(\) => btn\.classList\.remove/);
  });
});

describeServer('BoardsView — Zugänglichkeit der Kopier-Knöpfe (F-1463)', () => {
  beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de'); });
  afterEach(() => { BoardsView?.close(); document.body.innerHTML = ''; });

  it('jeder „ID kopieren“-Knopf und jedes ID-Feld nennt sein Board im zugänglichen Namen', async () => {
    BoardsView.open();
    await ruhe();
    const knoepfe = [...document.querySelectorAll('.bv-panel .bv-btn')];
    expect(knoepfe).toHaveLength(2);
    const namen = knoepfe.map(k => k.getAttribute('aria-label'));
    expect(namen).toContain(I18n.t('boards-view.copy-aria', 'Eins'));
    expect(namen).toContain(I18n.t('boards-view.copy-aria', 'Zwei'));
    expect(new Set(namen).size).toBe(2);
    /* sichtbarer Text bleibt „ID kopieren“ (Label-in-Name: der Name enthält ihn) */
    knoepfe.forEach(k => expect(k.getAttribute('aria-label')).toContain(k.textContent));
    const felder = [...document.querySelectorAll('.bv-panel .bv-id')].map(f => f.getAttribute('aria-label'));
    expect(new Set(felder).size).toBe(2);
  });
});
