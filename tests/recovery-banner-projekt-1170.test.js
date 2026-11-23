/**
 * Karte #1170: Hinweis „Nicht gespeicherte Änderungen der letzten Session“.
 * Im Projekt-Modus (Projektordner verbunden und freigegeben) wird nicht mehr
 * gefragt; der Ladepfad verwirft die Browser-Kopie still, sobald der Stand aus
 * dem Projektordner kam. Der Banner (Rückfall) hat dort nur „Alles klar“.
 * Geprüft wird der echte Quelltext aus src/main.js (kein Spiegel).
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

function bauen(fsaActive) {
  const code = schneide('function recoveryBannerNoetig(', '\n}\n')
    + schneide('function showRecoveryBanner(', '\n}\n');
  const Storage = { discardAutosave: vi.fn() };
  const State = { set: vi.fn() };
  const Sync = { isActive: () => fsaActive };
  // eslint-disable-next-line no-new-func
  const f = new Function('I18n', 'Storage', 'State', 'Sync',
    code + '\nreturn { recoveryBannerNoetig, showRecoveryBanner };')(I18n, Storage, State, Sync);
  return { ...f, Storage, State };
}

beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de'); });

it('Entscheidung: Banner nur ohne aktiven Projektordner', () => {
  const { recoveryBannerNoetig } = bauen(true);
  expect(recoveryBannerNoetig(true, { board: {} }, false)).toBe(true);
  expect(recoveryBannerNoetig(true, { board: {} }, true)).toBe(false);
  expect(recoveryBannerNoetig(false, { board: {} }, false)).toBe(false);
  expect(recoveryBannerNoetig(true, null, false)).toBe(false);
});

it('Ladepfad: aktiver Projektordner fragt nicht, Verzweigung nutzt recoveryBannerNoetig', () => {
  expect(quelle).toContain("const projektAktiv = Profile.has('feature.polling') && Sync.isActive();");
  expect(quelle).toContain('if (recoveryBannerNoetig(fromLocalStorage, state, projektAktiv)) {');
  /* Eigener Zweig: die Browser-Kopie wird nur verworfen, wenn der Ordner ein
     Board lieferte; sonst Hinweis, nie State.set(state) oder persistGated(state). */
  const zweig = quelle.slice(quelle.indexOf('} else if (fromLocalStorage && state && projektAktiv) {'));
  const ende = zweig.indexOf('} else if (state) {');
  expect(ende).toBeGreaterThan(0);
  const koerper = zweig.slice(0, ende);
  expect(koerper).toContain('if (state.board?.id) Storage.discardAutosave(state.board.id);');
  expect(koerper).toContain('showRecoveryBanner(state);');
  /* K-2: auf migrierten Boards zuerst der Fold, nicht die Projektion. */
  expect(koerper).toContain('if (await Sync.isMigrated()) {');
  expect(koerper.indexOf('Sync.projectorReconcile()')).toBeLessThan(koerper.indexOf('Sync.read()'));
  expect(koerper).not.toContain('State.set(state)');
  expect(koerper).not.toContain('persistGated');
});

it('Projekt-Modus-Banner: klarer Text, nur „Alles klar“, kein Laden', () => {
  const { showRecoveryBanner, Storage, State } = bauen(true);
  showRecoveryBanner({ board: { id: 'b1' } });
  const b = document.querySelector('.recovery-banner');
  expect(b.textContent).toContain('Projektordner konnte gerade nicht gelesen werden');
  expect(b.textContent).toContain('bleibt erhalten');
  const knoepfe = [...b.querySelectorAll('button')];
  expect(knoepfe.map(k => k.textContent)).toEqual(['Alles klar']);
  knoepfe[0].click();
  /* Kritik 20.11. K-6: im Rückfall ist die Browser-Kopie womöglich der einzige
     Stand — „Alles klar“ schließt nur, verwirft nichts. */
  expect(Storage.discardAutosave).not.toHaveBeenCalled();
  expect(State.set).not.toHaveBeenCalled();
  expect(document.querySelector('.recovery-banner')).toBeNull();
});

it('Standalone-Banner unverändert: Laden und Verwerfen', () => {
  const { showRecoveryBanner } = bauen(false);
  showRecoveryBanner({ board: { id: 'b1' } });
  const b = document.querySelector('.recovery-banner');
  expect(b.textContent).toContain('Nicht gespeicherte Änderungen');
  expect([...b.querySelectorAll('button')].map(k => k.textContent)).toEqual(['Laden', 'Verwerfen']);
});
