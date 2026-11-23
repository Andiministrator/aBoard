/**
 * Karte #1175 / F-3101: Ordner bekannt, Freigabe fehlt (`Sync.isPermissionNeeded()`,
 * auch „hängend“) → kein Recovery-Hinweis mit „Laden“; die Browser-Kopie bleibt
 * unangetastet. Dazu Verhaltenstests für den Ladeweg im Projekt-Modus
 * (Verify V-2 aus #1170): Fold schlägt Projektion; unlesbarer Ordner → Hinweis,
 * State unverändert, Kopie bleibt.
 *
 * Echt geprüft: die Quelltext-Funktionen `recoveryBannerNoetig`,
 * `uebernehmeOrdnerStand`, `showRecoveryBanner` aus src/main.js (per new Function
 * mit Mocks für Sync/State/Storage). Der Bootstrap selbst (Top-Level-await in
 * main.js) ist nicht importierbar; dessen Verzweigung wird nur per Quelltext-
 * Muster geprüft.
 */
import { it, expect, vi, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { I18n } from '../src/i18n.js';

const quelle = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');

function schneide(ab, bis) {
  const start = quelle.indexOf(ab);
  expect(start).toBeGreaterThan(-1);
  return quelle.slice(start, quelle.indexOf(bis, start) + bis.length);
}

function bauen({ aktiv = true, migriert = false, gefaltet = null, raw = null, wirft = false } = {}) {
  const code = schneide('function recoveryBannerNoetig(', '\n}\n')
    + schneide('async function uebernehmeOrdnerStand(', '\n}\n')
    + schneide('function showRecoveryBanner(', '\n}\n');
  const READ_ERROR = Symbol('READ_ERROR');
  const Storage = { discardAutosave: vi.fn(), checkVersion: vi.fn(x => x) };
  const aktuell = { v: null };
  const State = {
    set: vi.fn(x => { aktuell.v = x; }), markClean: vi.fn(), get: () => aktuell.v,
  };
  const Sync = {
    isActive: () => aktiv,
    isMigrated: async () => migriert,
    projectorReconcile: async () => { if (wirft) throw new Error('x'); return gefaltet; },
    read: async () => { if (wirft) throw new Error('x'); return raw === 'FEHLER' ? READ_ERROR : raw; },
    READ_ERROR, markAdopted: vi.fn(),
  };
  const StoragePlugin = { getActivePlugin: () => ({ parseAndMigrate: r => JSON.parse(r) }) };
  // eslint-disable-next-line no-new-func
  const f = new Function('I18n', 'Storage', 'State', 'Sync', 'StoragePlugin',
    code + '\nreturn { recoveryBannerNoetig, uebernehmeOrdnerStand, showRecoveryBanner };')(
    I18n, Storage, State, Sync, StoragePlugin);
  return { ...f, Storage, State, Sync };
}

beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de'); });

it('F-3101: Freigabe fehlt → kein Recovery-Banner (auch ohne aktiven Handle)', () => {
  const { recoveryBannerNoetig } = bauen({ aktiv: false });
  expect(recoveryBannerNoetig(true, { board: {} }, false, true)).toBe(false);
  expect(recoveryBannerNoetig(true, { board: {} }, false, false)).toBe(true);
  expect(recoveryBannerNoetig(true, { board: {} }, false)).toBe(true);
});

it('F-3101: Bootstrap-Verzweigung fängt „Freigabe fehlt“ vor dem Banner ab, ohne Laden/Verwerfen', () => {
  expect(quelle).toContain("const freigabeFehlt = Profile.has('feature.polling') && Sync.isPermissionNeeded();");
  const start = quelle.indexOf('if (fromLocalStorage && state && freigabeFehlt && !projektAktiv) {');
  expect(start).toBeGreaterThan(-1);
  const zweig = quelle.slice(start, quelle.indexOf('} else if (recoveryBannerNoetig(', start));
  expect(zweig).not.toContain('State.set');
  expect(zweig).not.toContain('markAdopted');
  expect(zweig).not.toContain('discardAutosave');
  expect(zweig).not.toContain('showRecoveryBanner');
  expect(quelle).toContain('recoveryBannerNoetig(fromLocalStorage, state, projektAktiv, freigabeFehlt)');
});

it('Ladeweg: Fold liefert anderes Board als die Projektion → State bekommt den Fold', async () => {
  const fold = { board: { id: 'b1', titel: 'Fold' } };
  const proj = { board: { id: 'b1', titel: 'Projektion' } };
  const t = bauen({ migriert: true, gefaltet: fold, raw: JSON.stringify(proj) });
  await t.uebernehmeOrdnerStand({ board: { id: 'b1', titel: 'Kopie' } });
  expect(t.State.set).toHaveBeenCalledTimes(1);
  expect(t.State.set.mock.calls[0][0].board.titel).toBe('Fold');
  expect(t.Sync.markAdopted).toHaveBeenCalled();
  expect(t.Storage.discardAutosave).toHaveBeenCalledWith('b1');
  expect(document.querySelector('.recovery-banner')).toBeNull();
});

it('Ladeweg: Ordner unlesbar → Hinweis, State unverändert, Kopie nicht verworfen', async () => {
  for (const opt of [{ raw: 'FEHLER' }, { raw: null }, { wirft: true }, { migriert: true, gefaltet: null, raw: 'FEHLER' }]) {
    document.body.innerHTML = '';
    const t = bauen(opt);
    await t.uebernehmeOrdnerStand({ board: { id: 'b1' } });
    expect(t.State.set).not.toHaveBeenCalled();
    expect(t.Sync.markAdopted).not.toHaveBeenCalled();
    expect(t.Storage.discardAutosave).not.toHaveBeenCalled();
    expect(document.querySelector('.recovery-banner')).not.toBeNull();
    expect([...document.querySelectorAll('.recovery-banner button')].map(k => k.textContent)).toEqual(['Alles klar']);
  }
});
