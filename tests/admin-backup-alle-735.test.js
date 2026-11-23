/**
 * Karte #735: Backup-Reiter — Hinweis unter der Board-Auswahl, Knopf
 * „Alle Boards jetzt sichern“ (Feld leer) bzw. „Jetzt sichern“ (Board
 * gewählt), Ergebnis, Sperre während des Laufs, 409.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { IS_SERVER_BUILD } from './helpers/build-flags.js';
import { I18n } from '../src/i18n.js';

const describeServer = IS_SERVER_BUILD ? describe : describe.skip;
let AdminDialog = null;
if (IS_SERVER_BUILD) ({ AdminDialog } = await import('../src/admin-dialog.js'));

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function oeffne() {
  AdminDialog.open();
  await ruhe(); await ruhe();
  document.querySelector('.admin-tab[data-section="backup"]').click();
  await ruhe(); await ruhe();
  return document.querySelector('.admin-body');
}
const antwort = (status, body) => ({ ok: status >= 200 && status < 300, status, json: async () => body });

describeServer('Backup-Reiter: Alle Boards sichern (Karte #735)', () => {
  let fetchSpy;
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; vi.useRealTimers(); });

  const jetztBtn = b => b.querySelectorAll('.admin-actions')[0].querySelectorAll('.admin-btn')[1];
  const flash = b => [...b.querySelectorAll(':scope > .admin-flash')][0];

  /* F-1829: Der Reiter fragt beim Aufbau einmal den Lauf-Status ab. Die Mocks
     liefern deshalb „läuft“ erst NACH dem Start-POST; davor ist der Server
     ruhig (sonst würde jeder Aufbau einen Lauf wieder aufnehmen). */
  function mockServer({ startStatus = 202, status = { running: false, last: { total: 3, ok: 3, failed: 0 } }, vorab = null } = {}) {
    let gestartet = false;
    fetchSpy = vi.fn(async (url, opt) => {
      if (String(url).includes('/admin/backup/all-now')) { gestartet = true; return antwort(startStatus, startStatus === 409 ? { error: 'x' } : { started: true }); }
      if (String(url).includes('/admin/backup/all-status')) return antwort(200, vorab ? vorab : (gestartet || !status.running ? status : { running: false, last: null }));
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
  }

  it('Hinweis unter der Board-Auswahl nennt die automatische Sicherung aller Boards', async () => {
    mockServer();
    const b = await oeffne();
    const hinweise = [...b.querySelectorAll('.admin-hint')].map(h => h.textContent);
    expect(hinweise.some(t => /täglich/.test(t) && /alle Boards/.test(t) && /einzeln/.test(t))).toBe(true);
  });

  it('Beschriftung folgt dem Feld live: leer / getippt / wieder geleert', async () => {
    mockServer();
    const b = await oeffne();
    const feld = b.querySelector('input.admin-form-input');
    expect(jetztBtn(b).textContent).toBe('Alle Boards jetzt sichern');
    feld.value = 'board-1'; feld.dispatchEvent(new Event('input'));
    expect(jetztBtn(b).textContent).toBe('Jetzt sichern');
    feld.value = '  '; feld.dispatchEvent(new Event('input'));
    expect(jetztBtn(b).textContent).toBe('Alle Boards jetzt sichern');
  });

  it('Feld leer: Klick startet den Alle-Boards-Lauf und zeigt das Ergebnis', async () => {
    mockServer();
    const b = await oeffne();
    const btn = jetztBtn(b);
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    const posts = fetchSpy.mock.calls.filter(c => String(c[0]).includes('/admin/backup/all-now'));
    expect(posts).toHaveLength(1);
    expect(posts[0][1].method).toBe('POST');
    expect(flash(b).textContent).toMatch(/3 Boards gesichert, 0 Fehler/);
    expect(flash(b).classList.contains('admin-flash--ok')).toBe(true);
    expect(btn.disabled).toBe(false);
  });

  it('Knopf ist während des Laufs gesperrt, zweiter Klick startet nichts', async () => {
    mockServer({ status: { running: true, last: null } });
    const b = await oeffne();
    const btn = jetztBtn(b);
    btn.click();
    await ruhe(); await ruhe();
    expect(btn.disabled).toBe(true);
    btn.click(); btn.disabled = false; btn.click(); // auch erzwungen: Zustandsflag sperrt
    await ruhe(); await ruhe();
    const posts = fetchSpy.mock.calls.filter(c => String(c[0]).includes('/admin/backup/all-now'));
    expect(posts).toHaveLength(1);
  });

  it('Fehler: Teilfehler wird mit Anzahl gemeldet (rot)', async () => {
    mockServer({ status: { running: false, last: { total: 3, ok: 2, failed: 1 } } });
    const b = await oeffne();
    jetztBtn(b).click();
    await ruhe(); await ruhe(); await ruhe();
    expect(flash(b).textContent).toMatch(/2 gesichert, 1 Fehler/);
    expect(flash(b).classList.contains('admin-flash--err')).toBe(true);
  });

  it('409: verständlicher Text, Knopf wieder frei', async () => {
    mockServer({ startStatus: 409 });
    const b = await oeffne();
    const btn = jetztBtn(b);
    btn.click();
    await ruhe(); await ruhe();
    expect(flash(b).textContent).toMatch(/bereits eine Sicherung über alle Boards/);
    expect(btn.disabled).toBe(false);
  });

  it('Board gewählt: „Jetzt sichern“ bleibt der Einzel-Endpunkt, kein Alle-Lauf', async () => {
    mockServer();
    const b = await oeffne();
    b.querySelector('input.admin-form-input').value = 'board-1';
    b.querySelector('input.admin-form-input').dispatchEvent(new Event('input'));
    jetztBtn(b).click();
    await ruhe(); await ruhe();
    expect(fetchSpy.mock.calls.some(c => String(c[0]).includes('/boards/board-1/backup/now'))).toBe(true);
    expect(fetchSpy.mock.calls.some(c => String(c[0]).includes('/admin/backup/all-now'))).toBe(false);
  });

  /* K-1 (Sprint 85): die Status-Abfrage darf nach Schließen / Reiterwechsel nicht weiterlaufen. */
  const statusAnfragen = () => fetchSpy.mock.calls.filter(c => String(c[0]).includes('/admin/backup/all-status')).length;

  it('K-1: nach AdminDialog.close() keine weiteren Status-Abfragen', async () => {
    mockServer({ status: { running: true, last: null } });
    const b = await oeffne();
    vi.useFakeTimers();
    jetztBtn(b).click();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1600);
    const vorher = statusAnfragen();
    expect(vorher).toBeGreaterThan(0);
    AdminDialog.close();
    await vi.advanceTimersByTimeAsync(1600); // laufender Schlaf endet, Schleife prüft und bricht ab
    const nach = statusAnfragen();
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusAnfragen()).toBe(nach);
    expect(nach).toBeLessThanOrEqual(vorher + 1);
  });

  it('K-1: nach Reiterwechsel keine weiteren Status-Abfragen', async () => {
    mockServer({ status: { running: true, last: null } });
    const b = await oeffne();
    vi.useFakeTimers();
    jetztBtn(b).click();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1600);
    document.querySelector('.admin-tab[data-section="status"]').click();
    await vi.advanceTimersByTimeAsync(1600);
    const nach = statusAnfragen();
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusAnfragen()).toBe(nach);
  });

  /* F-1826 (Verify Sprint 85): Schließen, während ein await offen ist — die
     späte Antwort darf nicht mehr in die (ausgehängte) Meldung schreiben. */
  function verzoegerterServer(pfad, body, status = 200) {
    let loese;
    const offen = new Promise(r => { loese = () => r(antwort(status, body)); });
    let gestartet = false;
    fetchSpy = vi.fn(async url => {
      if (String(url).includes(pfad)) { gestartet = true; return offen; }
      if (String(url).includes('/admin/backup/all-status')) return antwort(200, { running: gestartet, last: null });
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
    return () => loese();
  }

  it('F-1826: Schließen während des Start-POST — späte 409 schreibt nichts mehr', async () => {
    const loese = verzoegerterServer('/admin/backup/all-now', { error: 'x' }, 409);
    const b = await oeffne();
    const f = flash(b);
    jetztBtn(b).click();
    await ruhe();
    const vorher = f.textContent;
    AdminDialog.close();
    loese();
    await ruhe(); await ruhe(); await ruhe();
    expect(f.textContent).toBe(vorher);
  });

  it('F-1826: Schließen während der Status-Abfrage — spätes Ergebnis schreibt nichts mehr', async () => {
    let loese;
    const offen = new Promise(r => { loese = () => r(antwort(200, { running: false, last: { total: 2, ok: 2, failed: 0 } })); });
    fetchSpy = vi.fn(async url => {
      if (String(url).includes('/admin/backup/all-now')) return antwort(202, { started: true });
      if (String(url).includes('/admin/backup/all-status')) return offen;
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
    const b = await oeffne();
    const f = flash(b);
    jetztBtn(b).click();
    await ruhe(); await ruhe(); await ruhe();
    expect(fetchSpy.mock.calls.some(c => String(c[0]).includes('/admin/backup/all-status'))).toBe(true);
    const vorher = f.textContent;
    AdminDialog.close();
    loese();
    await ruhe(); await ruhe(); await ruhe();
    expect(f.textContent).toBe(vorher);
    expect(f.textContent).not.toMatch(/2 Boards gesichert/);
  });

  /* F-1827 (Kritiker Sprint 85): Restore über den Admin-Dialog meldet sein
     Ergebnis als Ereignis wie die Board-Einstellungen (#726) — sonst übernimmt
     main.js den wiederhergestellten Stand des offenen Boards nicht. */
  /* F-1833 (Verify Sprint 85): trennende Fälle — Antwort-board_id ≠ Zielfeld,
     Modus „als neu“, Moduswechsel während des Uploads. */
  async function adminRestore({ antwortId, ziel, modus, umschaltenAuf }) {
    const { EventBus } = await import('../src/eventbus.js');
    const ereignisse = [];
    const mit = p => ereignisse.push(p);
    EventBus.on('backup:restored', mit);
    vi.stubGlobal('confirm', () => true);
    let loese;
    const offen = new Promise(r => { loese = () => r(antwort(200, { board_id: antwortId, mode: modus })); });
    fetchSpy = vi.fn(async url => {
      if (String(url).includes('/backup/restore')) return offen;
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
    try {
      const b = await oeffne();
      const datei = b.querySelector('input[type="file"]');
      Object.defineProperty(datei, 'files', { value: [new File(['x'], 'b.tar.gz')], configurable: true });
      const auswahl = [...b.querySelectorAll('select.admin-form-input')].find(x => [...x.options].some(o => o.value === 'as-new'));
      auswahl.value = modus;
      [...b.querySelectorAll('input.admin-form-input[type="text"]')].pop().value = ziel;
      [...b.querySelectorAll('.admin-btn')].find(k => k.textContent === I18n.t('admin.backup.upload.btn')).click();
      await ruhe();
      if (umschaltenAuf) auswahl.value = umschaltenAuf;   /* Upload läuft noch */
      loese();
      await ruhe(); await ruhe(); await ruhe();
      return ereignisse;
    } finally {
      EventBus.off?.('backup:restored', mit);
      vi.unstubAllGlobals();
    }
  }

  it('F-1827: Admin-Restore sendet backup:restored mit der board_id der Server-Antwort', async () => {
    expect(await adminRestore({ antwortId: 'board-9', ziel: 'board-7', modus: 'overwrite' }))
      .toEqual([{ boardId: 'board-9', mode: 'overwrite' }]);
  });

  it('F-1827: Modus „als neu“ wird als as-new gemeldet (Empfänger übernimmt dann nichts)', async () => {
    expect(await adminRestore({ antwortId: 'board-neu', ziel: '', modus: 'as-new' }))
      .toEqual([{ boardId: 'board-neu', mode: 'as-new' }]);
  });

  it('F-1833: Moduswechsel während des Uploads ändert den gemeldeten Modus nicht', async () => {
    expect(await adminRestore({ antwortId: 'board-7', ziel: 'board-7', modus: 'overwrite', umschaltenAuf: 'as-new' }))
      .toEqual([{ boardId: 'board-7', mode: 'overwrite' }]);
  });

  /* F-1829: laufender Alle-Boards-Lauf nach Reiterwechsel wieder sichtbar. */
  it('F-1829: läuft beim Aufbau schon ein Lauf, ist der Knopf gesperrt und das Ergebnis erscheint am Ende', async () => {
    const laufend = { running: true, last: null };
    mockServer({ vorab: laufend });
    const b = await oeffne();
    const btn = jetztBtn(b);
    expect(btn.disabled).toBe(true);
    expect(flash(b).textContent).toMatch(/läuft|Sicherung/i);
    expect(flash(b).classList.contains('admin-flash--busy')).toBe(true);
    /* Server wird fertig → nächster Poll liefert das Ergebnis */
    fetchSpy.mockImplementation(async url => String(url).includes('/admin/backup/all-status')
      ? antwort(200, { running: false, last: { total: 4, ok: 4, failed: 0 } }) : antwort(200, { files: [] }));
    await new Promise(r => setTimeout(r, 1700));   /* echter Takt von 1,5 s */
    expect(flash(b).textContent).toMatch(/4 Boards gesichert, 0 Fehler/);
    expect(btn.disabled).toBe(false);
  });

  it('F-1829: zweimal Reiter wechseln → nur EIN Polling, Ergebnis in den aktuellen Reiter', async () => {
    mockServer({ vorab: { running: true, last: null } });
    await oeffne();
    const tab = id => document.querySelector(`.admin-tab[data-section="${id}"]`).click();
    vi.useFakeTimers();
    tab('status'); await vi.advanceTimersByTimeAsync(0);
    tab('backup'); await vi.advanceTimersByTimeAsync(0);
    tab('status'); await vi.advanceTimersByTimeAsync(0);
    tab('backup'); await vi.advanceTimersByTimeAsync(0);
    /* Die vier Aufbauten haben je einmal gefragt; alte Polls enden nach ihrem Schlaf. */
    await vi.advanceTimersByTimeAsync(3100);
    const vorher = statusAnfragen();
    await vi.advanceTimersByTimeAsync(6000);
    /* 6 s bei 1,5 s Takt → höchstens 4 Abfragen eines einzigen Polls (+1 Toleranz) */
    expect(statusAnfragen() - vorher).toBeLessThanOrEqual(5);
    expect(statusAnfragen() - vorher).toBeGreaterThanOrEqual(3);
  });

  it('F-1829: Reiter verlassen, während der wiederaufgenommene Poll offen ist → keine Abfragen mehr', async () => {
    mockServer({ vorab: { running: true, last: null } });
    await oeffne();
    vi.useFakeTimers();
    document.querySelector('.admin-tab[data-section="status"]').click();
    await vi.advanceTimersByTimeAsync(3100);
    const nach = statusAnfragen();
    await vi.advanceTimersByTimeAsync(10000);
    expect(statusAnfragen()).toBe(nach);
  });

  /* O-9: wiederaufgenommenes Polling endet bei „kein Lauf mehr bekannt“ und bei 401/403. */
  it('O-9: Wiederaufnahme, Server kennt keinen Lauf mehr (running:false ohne last) → Ende mit Meldung', async () => {
    let n = 0;
    fetchSpy = vi.fn(async url => {
      if (String(url).includes('/admin/backup/all-status')) {
        return antwort(200, ++n <= 2 ? { running: true, last: null } : { running: false, last: null });
      }
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
    const b = await oeffne();
    const btn = jetztBtn(b);
    expect(btn.disabled).toBe(true);
    await new Promise(r => setTimeout(r, 1700));
    expect(btn.disabled).toBe(false);
    expect(flash(b).classList.contains('admin-flash--err')).toBe(true);
    expect(flash(b).textContent).toMatch(/nicht mehr bekannt/);
  });

  it('O-9: Wiederaufnahme, dann 403 → Ende statt weiter „läuft“', async () => {
    let n = 0;
    fetchSpy = vi.fn(async url => {
      if (String(url).includes('/admin/backup/all-status')) {
        return ++n <= 2 ? antwort(200, { running: true, last: null }) : antwort(403, { error: 'x' });
      }
      return antwort(200, { files: [] });
    });
    globalThis.fetch = fetchSpy;
    const b = await oeffne();
    const btn = jetztBtn(b);
    expect(btn.disabled).toBe(true);
    await new Promise(r => setTimeout(r, 1700));
    expect(btn.disabled).toBe(false);
    expect(flash(b).classList.contains('admin-flash--err')).toBe(true);
  });

  /* K-8: Fehlercode des Servers wird in der Oberflächensprache angezeigt, nicht der deutsche Freitext. */
  it.each([
    ['list', 'de', /Board-Liste ist nicht lesbar/], ['list', 'en', /board list cannot be read/],
    ['key', 'de', /kein Schlüssel/], ['key', 'en', /there is no key/],
    ['abort', 'en', /shutting down/], ['panic', 'en', /internal error/],
  ])('K-8: Code %s in %s', async (code, lang, muster) => {
    mockServer({ status: { running: false, last: { total: 1, ok: 0, failed: 0, code, error: 'Board-Liste nicht lesbar (deutscher Freitext)' } } });
    I18n.setLang(lang);
    const b = await oeffne();
    jetztBtn(b).click();
    await ruhe(); await ruhe(); await ruhe();
    const t = flash(b).textContent;
    expect(t).toMatch(muster);
    expect(t).not.toContain('deutscher Freitext');
  });

  it('K-8: unbekannter Code → Freitext als Rückfall', async () => {
    mockServer({ status: { running: false, last: { total: 1, ok: 0, failed: 0, code: 'neu', error: 'Irgendwas' } } });
    const b = await oeffne();
    jetztBtn(b).click();
    await ruhe(); await ruhe(); await ruhe();
    expect(flash(b).textContent).toContain('Irgendwas');
  });
});
