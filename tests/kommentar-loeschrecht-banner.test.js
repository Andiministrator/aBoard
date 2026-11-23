/**
 * ADR-0136 Nachtrag (Karte #693, F-1612): Lehnt der Server eine Änderung ab,
 * weil sie einen fremden Kommentar entfernt oder ändert, bietet der Banner
 * KEIN „Meine Fassung trotzdem speichern“ an — der Ganz-Board-PUT entfernte
 * den Kommentar sonst ohne Verlauf und Nachricht an den Autor.
 *
 * Geprüft wird der echte Quelltext von `zeigeAbgelehnt` aus src/main.js
 * (kein Spiegel): Die Funktion wird herausgeschnitten und mit
 * eingesetzten Abhängigkeiten ausgeführt.
 */
import { it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

function zeigeAbgelehntAusQuelle() {
  const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
  const start = q.indexOf('function zeigeAbgelehnt(plugin, result) {');
  expect(start).toBeGreaterThan(-1);
  const ende = q.indexOf('\n}\n', start);
  const quelle = q.slice(start, ende + 2);
  const banner = vi.fn();
  const behalteMeine = vi.fn();
  const zeigeFeldKonflikt = vi.fn();
  // eslint-disable-next-line no-new-func
  const fn = new Function('IS_SERVER_BUILD', 'I18n', 'showConflictBanner', 'behalteMeine', 'zeigeFeldKonflikt',
    quelle + '\nreturn zeigeAbgelehnt;')(true, I18n, banner, behalteMeine, zeigeFeldKonflikt);
  return { fn, banner, behalteMeine };
}

/* Die Meldungen stehen im Server-Block von i18n.js (DCE im slim Build). */
describeServer('zeigeAbgelehnt — fremder Kommentar (F-1612)', () => {
  it('eigene Meldung, nur ein Knopf, kein PUT-Angebot; „Verstanden“ holt den Server-Stand', () => {
    const { fn, banner, behalteMeine } = zeigeAbgelehntAusQuelle();
    const plugin = { holeJetzt: vi.fn() };
    fn(plugin, { abgelehnt: [{ art: 'fremder-kommentar', cardId: 'k1', displayId: 7, titel: 'Karte' }], meineFassung: {} });
    expect(banner).toHaveBeenCalledTimes(1);
    const [onMine, onServer, text, mineLabel, serverLabel] = banner.mock.calls[0];
    expect(onServer).toBeNull();
    expect(serverLabel).toBeNull();
    expect(mineLabel).toBe(I18n.t('ops.abgelehnt.ok'));
    expect(mineLabel).not.toBe(I18n.t('ops.abgelehnt.meine'));
    expect(text).toBe(I18n.t('ops.abgelehnt.fremder-kommentar', 7, 'Karte'));
    expect(text).toContain('#7');
    onMine();
    expect(plugin.holeJetzt).toHaveBeenCalledTimes(1);
    expect(behalteMeine).not.toHaveBeenCalled();
  });

  it('gemischt mit anderen Gründen: trotzdem kein PUT-Angebot', () => {
    const { fn, banner } = zeigeAbgelehntAusQuelle();
    fn({ holeJetzt() {} }, { abgelehnt: [{ art: 'ziel-fehlt', displayId: 1, titel: 'A' }, { art: 'schattenschluessel', displayId: 2, titel: 'B' }], meineFassung: {} });
    const [, onServer, text] = banner.mock.calls[0];
    expect(onServer).toBeNull();
    expect(text).toContain(I18n.t('ops.abgelehnt.rest'));
  });

  it('andere Gründe bleiben beim bisherigen Banner mit „Meine Fassung“', () => {
    const { fn, banner } = zeigeAbgelehntAusQuelle();
    fn({ holeJetzt() {} }, { abgelehnt: [{ art: 'ziel-fehlt', displayId: 1, titel: 'A' }], meineFassung: {} });
    expect(banner.mock.calls[0][3]).toBe(I18n.t('ops.abgelehnt.meine'));
  });

  it('Meldung gibt es auf Deutsch und Englisch', () => {
    for (const sprache of ['de', 'en']) {
      I18n.setLang?.(sprache);
      const t = I18n.t('ops.abgelehnt.fremder-kommentar', 3, 'X');
      expect(t).not.toBe('ops.abgelehnt.fremder-kommentar');
      expect(t).toContain('#3');
    }
    I18n.setLang?.('de');
  });
});

/* F-1621: Verlaufsschutz — dieselbe Regel wie beim fremden Kommentar. */
describeServer('zeigeAbgelehnt — Verlauf (F-1621)', () => {
  it('verlauf-gefaelscht/-geaendert: eigene Meldung, kein PUT-Angebot', () => {
    for (const art of ['verlauf-gefaelscht', 'verlauf-geaendert']) {
      const { fn, banner, behalteMeine } = zeigeAbgelehntAusQuelle();
      const plugin = { holeJetzt: vi.fn() };
      fn(plugin, { abgelehnt: [{ art, cardId: 'k1', displayId: 4, titel: 'V' }], meineFassung: {} });
      const [onMine, onServer, text, mineLabel] = banner.mock.calls[0];
      expect(onServer).toBeNull();
      expect(mineLabel).toBe(I18n.t('ops.abgelehnt.ok'));
      expect(text).toBe(I18n.t('ops.abgelehnt.verlauf', 4, 'V'));
      expect(text).not.toBe('ops.abgelehnt.verlauf');
      onMine();
      expect(plugin.holeJetzt).toHaveBeenCalledTimes(1);
      expect(behalteMeine).not.toHaveBeenCalled();
    }
  });
});

/* KF-6 (Sprint-82-Abschluss): Antwortkommentar „extern“ und doppelte Karten-id —
   der PUT lehnt beides ebenso ab, also kein „Meine Fassung trotzdem speichern“. */
describeServer('zeigeAbgelehnt — extern-kommentar, karte-doppelt (KF-6)', () => {
  it('eigene Meldung in DE und EN, kein PUT-Angebot', () => {
    for (const art of ['extern-kommentar', 'karte-doppelt']) {
      const { fn, banner, behalteMeine } = zeigeAbgelehntAusQuelle();
      fn({ holeJetzt: vi.fn() }, { abgelehnt: [{ art, cardId: 'k1', displayId: 5, titel: 'E' }], meineFassung: {} });
      const [, onServer, text, mineLabel] = banner.mock.calls[0];
      expect(onServer).toBeNull();
      expect(mineLabel).toBe(I18n.t('ops.abgelehnt.ok'));
      expect(text).toBe(I18n.t('ops.abgelehnt.' + art, 5, 'E'));
      expect(behalteMeine).not.toHaveBeenCalled();
      for (const sprache of ['de', 'en']) {
        I18n.setLang?.(sprache);
        expect(I18n.t('ops.abgelehnt.' + art, 5, 'E')).toContain('#5');
      }
      I18n.setLang?.('de');
    }
  });
});

/* F-1622: Lehnt der Ganz-Board-PUT ab, steht der Grund im Banner. */
describeServer('zeigeAbgewiesen — PUT abgelehnt (F-1622)', () => {
  it('nennt den Grund vom Server, ein Knopf, kein „erneut versuchen“', () => {
    const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    const start = q.indexOf('function zeigeAbgewiesen(abgewiesen) {');
    expect(start).toBeGreaterThan(-1);
    const quelle = q.slice(start, q.indexOf('\n}\n', start) + 2);
    const banner = vi.fn();
    // eslint-disable-next-line no-new-func
    const fn = new Function('I18n', 'showConflictBanner', quelle + '\nreturn zeigeAbgewiesen;')(I18n, banner);
    fn({ status: 422, grund: 'comments of other people would be removed' });
    const [, onServer, text, label] = banner.mock.calls[0];
    expect(onServer).toBeNull();
    expect(label).toBe(I18n.t('ops.abgelehnt.ok'));
    expect(text).toContain('comments of other people would be removed');
    expect(text).not.toBe(I18n.t('conflict.retry-failed'));
  });
});
