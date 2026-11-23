/* @vitest-environment happy-dom */

/**
 * Karte #335 — Backup-Sektion im Board-Einstellungsdialog (Eigentümer-Sicht).
 *
 * ADR-0116: Das Modul ist server-build-gegated. In den drei Nicht-Server-
 * Kontexten existiert es im ausgelieferten Bundle nicht; die Tests laufen
 * dort trotzdem gegen die Quelldatei, weil `import` sie unabhängig vom
 * Vite-Define auflöst. Das ist beabsichtigt — die Logik ist buildunabhängig
 * korrekt, gegated ist nur ihr EINBAU (siehe build-invarianten.test.js für
 * die Grenze selbst).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { buildBackupSection, darfBackupSehen } from '../src/backup-section.js';
import { StoragePlugin } from '../src/storage-plugin.js';

/* Antwort-Fabrik für den fetch-Stub. */
function antwort(body, { ok = true, status = 200 } = {}) {
  return Promise.resolve({
    ok, status,
    json: () => Promise.resolve(body),
  });
}

function pluginStub({ user, acl }) {
  return {
    getCurrentUser: vi.fn(async () => user),
    aclList: vi.fn(async () => acl),
  };
}

describe('#335 — darfBackupSehen: wer die Sektion sieht', () => {
  const acl = [
    { email: 'chef@x.de', role: 'owner' },
    { email: 'gast@x.de', role: 'viewer' },
  ];

  it('Eigentümer laut ACL: ja', () => {
    expect(darfBackupSehen({ email: 'chef@x.de' }, acl)).toBe(true);
  });

  it('Nicht-Eigentümer (viewer): nein', () => {
    expect(darfBackupSehen({ email: 'gast@x.de' }, acl)).toBe(false);
  });

  it('Nutzer ohne ACL-Eintrag: nein', () => {
    expect(darfBackupSehen({ email: 'fremd@x.de' }, acl)).toBe(false);
  });

  it('Server-Admin ohne ACL-Eintrag: ja', () => {
    expect(darfBackupSehen({ email: 'admin@x.de', is_admin: true }, acl)).toBe(true);
  });

  it('Legacy-Bypass (keine Identität): ja — der Server lässt dort jeden durch', () => {
    expect(darfBackupSehen(null, [])).toBe(true);
    expect(darfBackupSehen({ email: null }, [])).toBe(true);
  });

  it('Gross-/Kleinschreibung und Leerzeichen der Email spielen keine Rolle', () => {
    // Der Server normalisiert Emails (storage.NormalizeEmail); täte die UI
    // das nicht, sähe ein Eigentümer mit „Chef@X.de" seine eigene Sektion
    // nicht — und niemand käme auf die Idee, dort zu suchen.
    expect(darfBackupSehen({ email: '  Chef@X.de ' }, acl)).toBe(true);
  });

  it('leere ACL macht aus einem angemeldeten Nicht-Admin keinen Eigentümer', () => {
    expect(darfBackupSehen({ email: 'chef@x.de' }, [])).toBe(false);
    expect(darfBackupSehen({ email: 'chef@x.de' }, null)).toBe(false);
  });
});

describe('#335 — Sektion baut sich auf und lädt die Liste', () => {
  let aktiv;

  beforeEach(() => {
    aktiv = vi.spyOn(StoragePlugin, 'getActivePlugin');
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.querySelectorAll('.config-backup-container').forEach(n => n.remove());
  });

  it('bleibt verborgen, wenn der Nutzer kein Eigentümer ist — und fragt gar nicht erst nach der Liste', async () => {
    aktiv.mockReturnValue(pluginStub({ user: { email: 'gast@x.de' }, acl: [{ email: 'chef@x.de', role: 'owner' }] }));
    const holen = vi.fn(() => antwort({ files: [] }));
    globalThis.fetch = holen;

    const { el, bereit } = buildBackupSection('b1');
    expect(await bereit).toBe(false);
    expect(el.hidden).toBe(true);
    // Kein Netzaufruf: eine verborgene Sektion, die trotzdem lädt, würde bei
    // jedem Dialog-Öffnen einen 403 provozieren.
    expect(holen).not.toHaveBeenCalled();
  });

  it('erscheint für den Eigentümer und listet die Backups', async () => {
    aktiv.mockReturnValue(pluginStub({ user: { email: 'chef@x.de' }, acl: [{ email: 'chef@x.de', role: 'owner' }] }));
    globalThis.fetch = vi.fn(() => antwort({
      files: [
        { filename: 'boardbackup-b1-20260820-020000-aaaaaaaa.tar.gz', size: 2048, mtime: '2026-08-20T02:00:00Z', encrypted: false },
        { filename: 'boardbackup-b1-20260819-020000-bbbbbbbb.tar.gz.enc', size: 5242880, mtime: '2026-08-19T02:00:00Z', encrypted: true },
      ],
    }));

    const { el, bereit } = buildBackupSection('b1');
    expect(await bereit).toBe(true);
    expect(el.hidden).toBe(false);

    const zeilen = el.querySelectorAll('.config-backup-table tbody tr');
    expect(zeilen.length).toBe(2);
    // Grössen werden lesbar formatiert, nicht als rohe Bytes gezeigt.
    expect(zeilen[0].children[1].textContent).toBe('2.0 KB');
    expect(zeilen[1].children[1].textContent).toBe('5.0 MB');
    // Nur das verschlüsselte Archiv trägt das Schloss.
    expect(zeilen[0].querySelector('.config-backup-badge')).toBeNull();
    expect(zeilen[1].querySelector('.config-backup-badge')).toBeTruthy();
    // Download-Link zeigt auf den Endpunkt des OFFENEN Boards.
    const dl = zeilen[1].querySelector('a[download]');
    expect(dl.getAttribute('href')).toContain('/boards/b1/backup/download/');
  });

  it('zeigt die Server-Meldung statt eines generischen Fehlers', async () => {
    // Der Fall, um den es geht: Verschlüsselung an, Schlüssel fehlt. Nur der
    // Betreiber kann das beheben — der Eigentümer muss erfahren, WAS los ist.
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    globalThis.fetch = vi.fn(() => antwort(
      { error: 'backup_board.encryption_enabled=true, aber ABOARD_SECRET_KEY ist nicht gesetzt' },
      { ok: false, status: 503 },
    ));

    const { el, bereit } = buildBackupSection('b1');
    await bereit;
    expect(el.querySelector('.config-backup-list').textContent)
      .toContain('ABOARD_SECRET_KEY');
  });

  it('„Jetzt sichern" ruft den richtigen Endpunkt und lädt die Liste neu', async () => {
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    const rufe = [];
    globalThis.fetch = vi.fn((url, opts) => {
      rufe.push({ url, method: (opts && opts.method) || 'GET' });
      return antwort({ files: [] });
    });

    const { el, bereit } = buildBackupSection('b1');
    await bereit;
    const vorher = rufe.length;

    el.querySelector('button.btn-primary').click();
    await vi.waitFor(() => expect(rufe.length).toBeGreaterThan(vorher + 1));

    expect(rufe.some(r => r.method === 'POST' && r.url === '/boards/b1/backup/now')).toBe(true);
    // Nach dem Sichern muss die Liste neu geladen werden, sonst sieht der
    // Nutzer sein frisches Backup nicht.
    expect(rufe.filter(r => r.url === '/boards/b1/backup/files').length).toBeGreaterThan(1);
  });

  it('Wiederherstellen schickt IMMER das offene Board als Ziel — nie ein fremdes', async () => {
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    let gesendet = null;
    globalThis.fetch = vi.fn((url, opts) => {
      if (url === '/backup/restore') { gesendet = opts.body; return antwort({}); }
      return antwort({ files: [] });
    });
    vi.stubGlobal('confirm', () => true);

    const { el, bereit } = buildBackupSection('b1');
    await bereit;

    const dateiInp = el.querySelector('input[type="file"]');
    Object.defineProperty(dateiInp, 'files', {
      value: [new File(['x'], 'sicherung.tar.gz.enc')],
      configurable: true,
    });
    el.querySelector('select').value = 'overwrite';
    el.querySelector('button.btn-danger').click();

    await vi.waitFor(() => expect(gesendet).not.toBeNull());
    expect(gesendet.get('mode')).toBe('overwrite');
    expect(gesendet.get('target_board_id')).toBe('b1');
  });

  it('Wiederherstellen ohne ausgewählte Datei sendet nichts', async () => {
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    const rufe = [];
    globalThis.fetch = vi.fn((url) => { rufe.push(url); return antwort({ files: [] }); });
    vi.stubGlobal('confirm', () => true);

    const { el, bereit } = buildBackupSection('b1');
    await bereit;
    el.querySelector('button.btn-danger').click();

    expect(rufe).not.toContain('/backup/restore');
    expect(el.querySelector('.config-backup-flash').textContent).toBeTruthy();
  });

  it('F-2876: flash ist eine dauerhaft eingehängte, anfangs leere Live-Region (Text kommt erst danach)', async () => {
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    globalThis.fetch = vi.fn(() => antwort({ files: [] }));
    const { el, bereit } = buildBackupSection('b1');
    const flash = el.querySelector('.config-backup-flash');
    expect(flash.getAttribute('role')).toBe('status');
    expect(flash.textContent).toBe('');
    await bereit;
    expect(el.querySelector('.config-backup-flash')).toBe(flash);
  });

  it('aktualisieren() lädt die Liste erneut — der Dialog ist ein Singleton', async () => {
    // Kritiker-Befund Sprint 58: `buildOverlay` in configdialog.js läuft genau
    // EINMAL. Ohne diesen Weg zeigt die Sektion bis zum nächsten Sprach- oder
    // Board-Wechsel den Stand vom ersten Öffnen — das nächtliche Backup fehlt,
    // obwohl der Hinweistext der Sektion es ankündigt.
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    let dateien = [];
    globalThis.fetch = vi.fn(() => antwort({ files: dateien }));

    const { el, bereit, aktualisieren } = buildBackupSection('b1');
    await bereit;
    expect(el.querySelector('.config-backup-table')).toBeNull(); /* leer */

    // Der Server legt inzwischen ein Backup an (nächtlich oder aus einem
    // anderen Tab) — ohne aktualisieren() bekäme die Sektion das nie mit.
    dateien = [{ filename: 'boardbackup-b1-20260826-020000-cccccccc.tar.gz',
                 size: 1024, mtime: '2026-08-26T02:00:00Z', encrypted: false }];
    aktualisieren();
    await vi.waitFor(() => expect(el.querySelector('.config-backup-table')).toBeTruthy());
    expect(el.querySelectorAll('.config-backup-table tbody tr').length).toBe(1);
  });

  it('aktualisieren() tut nichts, solange die Sektion verborgen ist', async () => {
    // Sonst würde jedes Öffnen des Dialogs bei einem Nicht-Eigentümer einen
    // 403 provozieren.
    aktiv.mockReturnValue(pluginStub({ user: { email: 'gast@x.de' }, acl: [] }));
    const holen = vi.fn(() => antwort({ files: [] }));
    globalThis.fetch = holen;

    const { bereit, aktualisieren } = buildBackupSection('b1');
    expect(await bereit).toBe(false);
    aktualisieren();
    expect(holen).not.toHaveBeenCalled();
  });

  it('überholte Ladevorgänge schreiben nicht mehr in die Liste', async () => {
    // Generations-Marke: Ohne sie gewinnt die zuletzt EINTREFFENDE Antwort
    // statt der zuletzt GESTELLTEN Anfrage. Der Nutzer sähe trotz
    // „Backup erstellt" den alten Stand.
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));

    const offen = [];
    globalThis.fetch = vi.fn(() => new Promise(res => { offen.push(res); }));

    const { el, aktualisieren } = buildBackupSection('b1');
    // Erster Lauf hängt (offen[0]); ein zweiter überholt ihn.
    await vi.waitFor(() => expect(offen.length).toBe(1));
    aktualisieren();
    await vi.waitFor(() => expect(offen.length).toBe(2));

    // Der NEUERE antwortet zuerst mit einer Datei …
    offen[1]({ ok: true, status: 200, json: () => Promise.resolve({
      files: [{ filename: 'neu.tar.gz', size: 10, mtime: 'x', encrypted: false }] }) });
    await vi.waitFor(() => expect(el.querySelectorAll('.config-backup-table tbody tr').length).toBe(1));

    // … danach trudelt die ALTE Antwort mit leerer Liste ein. Sie darf die
    // Anzeige nicht mehr anfassen.
    offen[0]({ ok: true, status: 200, json: () => Promise.resolve({ files: [] }) });
    await new Promise(r => setTimeout(r, 0));
    expect(el.querySelectorAll('.config-backup-table tbody tr').length).toBe(1);
  });

  it('Dateiauswahl akzeptiert auch verschlüsselte Archive (#336)', async () => {
    aktiv.mockReturnValue(pluginStub({ user: null, acl: [] }));
    globalThis.fetch = vi.fn(() => antwort({ files: [] }));
    const { el, bereit } = buildBackupSection('b1');
    await bereit;
    // Ohne `.enc` liesse der Dateidialog das eigene Backup nicht auswählen.
    expect(el.querySelector('input[type="file"]').accept).toContain('.enc');
  });
});
