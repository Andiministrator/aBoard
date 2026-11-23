/* backup-section.js — Backup & Wiederherstellung für den Board-Eigentümer
 * (Karte #335, ADR-0098; Verschlüsselungs-Anzeige aus Karte #336).
 *
 * ABGRENZUNG ZU src/admin-dialog.js::renderBackup:
 * Der Admin-Dialog bedient einen SERVER-Admin, der ein beliebiges Board über
 * eine eingetippte Board-ID sichert. Diese Sektion bedient den EIGENTÜMER des
 * gerade geöffneten Boards — es gibt deshalb kein Board-ID-Feld und keinen
 * „Laden"-Knopf: das Board steht fest, die Liste lädt beim Öffnen.
 *
 * Karte #303 hatte die Oberfläche für Board-Eigentümer spezifiziert; umgesetzt
 * worden war sie damals nur im Admin-Dialog. Die REST-ACL war von Anfang an
 * owner-aware (`requireBoardAccess`: Owner ODER Server-Admin) — es fehlte nur
 * der Zugang.
 *
 * SICHTBARKEIT ist zweistufig:
 *   1. Modus/Build — `isSectionRelevant('backup', …)` in settings-scope.js:
 *      nur Server-Build und Server-Modus. Ausserhalb wird dieses Modul nicht
 *      einmal importiert (DCE).
 *   2. Person — asynchron: Eigentümer des Boards ODER Server-Admin. Bis die
 *      Antwort da ist, bleibt die Sektion verborgen; sie erscheint also
 *      nachträglich statt kurz aufzublitzen und wieder zu verschwinden.
 *
 * Der Personen-Check ist ein UX-Filter, KEINE Sicherheitsgrenze. Die
 * verbindliche Prüfung macht der Server bei jedem einzelnen Endpunkt. Wer
 * die Sektion per DevTools einblendet, bekommt vom Server 403.
 */

import { I18n }          from './i18n.js';
import { EventBus }      from './eventbus.js';
import { StoragePlugin } from './storage-plugin.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

/* CSS für diese Sektion wird zur Laufzeit injiziert statt in src/style.css
   abgelegt (Muster wie `_ensureDocViewerStyles` in sidebar.js). Grund: Diese
   Datei ist server-build-gegated und fällt im slim- und claude-code-Bundle der
   DCE zum Opfer — Regeln in style.css täten das NICHT und würden beide
   Distributables um totes CSS vergrössern. Die verwendeten Variablen sind
   gegen src/style.css geprüft (--surface-2, --border, --text-muted, --radius);
   ein Tippfehler bliebe hier sonst unsichtbar, weil CSS-Fallbacks ihn
   verdecken. */
let _backupStylesInjected = false;
function _ensureBackupStyles() {
  if (_backupStylesInjected) return;
  _backupStylesInjected = true;
  const style = document.createElement('style');
  style.setAttribute('data-aboard', 'backup-section');
  style.textContent = '.config-backup-container{margin-top:8px}.config-backup-actions{display:flex;gap:8px;margin:8px 0}.config-backup-flash{font-size:13px;color:var(--text-muted);min-height:18px;margin-bottom:6px}.config-backup-list{overflow-x:auto}.config-backup-table{width:100%;border-collapse:collapse;font-size:13px}.config-backup-table th,.config-backup-table td{text-align:left;padding:5px 8px;border-bottom:1px solid var(--border);vertical-align:middle}.config-backup-table th{color:var(--text-muted);font-weight:600}.config-backup-table td:first-child{word-break:break-all}.config-backup-badge{display:inline-block;background:var(--surface-2);border:1px solid var(--border);border-radius:var(--radius);padding:0 6px;font-size:11px;white-space:nowrap}.config-subsection-title{font-weight:600;font-size:14px;margin:14px 0 6px}.config-hint{font-size:11px;color:var(--text-muted);font-style:italic}';
  document.head.appendChild(style);
}

/* Kleiner DOM-Helfer, gleiche Signatur wie in admin-dialog.js/configdialog.js. */
function el(tag, attrs = {}) {
  const node = document.createElement(tag);
  if (attrs.cls) node.className = attrs.cls;
  if (attrs.text != null) node.textContent = attrs.text;
  if (attrs.id) node.id = attrs.id;
  if (attrs.type) node.type = attrs.type;
  return node;
}

/* Bytes lesbar machen. Exportiert, weil `src/admin-dialog.js` dieselbe
   Backup-Liste rendert: Sprint-58-Kritiker hat gemessen, dass die Drift
   zwischen beiden Oberflächen schon eingetreten war — dort standen rohe
   Bytes und kein Verschlüsselungs-Hinweis, obwohl beide dieselbe
   REST-Antwort lesen. Ein gemeinsamer Helfer ist billiger als die Zusage,
   zwei Stellen synchron zu halten. */
export function formatGroesse(bytes) {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n < 0) return '—';
  if (n < 1024) return `${n} B`;
  const einheiten = ['KB', 'MB', 'GB'];
  let wert = n / 1024;
  let i = 0;
  while (wert >= 1024 && i < einheiten.length - 1) { wert /= 1024; i++; }
  return `${wert.toFixed(wert < 10 ? 1 : 0)} ${einheiten[i]}`;
}

/**
 * Kennzeichnung für ein verschlüsseltes Archiv. Exportiert aus demselben
 * Grund wie `formatGroesse` — der Admin-Dialog zeigt dieselbe Liste.
 *
 * Das Symbol steht bewusst NICHT allein: der Text „verschlüsselt" trägt die
 * Aussage, das Emoji ist Verzierung, das `title` nur Zusatz (F-116/F-231/F-338
 * — Symbole ohne Textalternative).
 *
 * @returns {HTMLElement}
 */
export function verschluesseltBadge() {
  const badge = el('span', {
    cls: 'config-backup-badge', text: I18n.t('config.backup-encrypted'),
  });
  badge.title = I18n.t('config.backup-encrypted-title');
  return badge;
}

/**
 * Entscheidet, ob die Sektion für den aktuellen Nutzer sichtbar ist.
 *
 * Drei Wege zu „ja":
 *   - Server-Admin (`is_admin`) — darf ohnehin jedes Board sichern.
 *   - Eigentümer laut Board-ACL.
 *   - Legacy-Bypass: der Server läuft ohne Forward-Auth, es gibt gar keine
 *     Identität. Dann ist der einzige Nutzer zugleich Eigentümer; der Server
 *     lässt in diesem Modus jeden durch (`requireBoardAccess` gibt bei
 *     fehlender Identität `true` zurück). Die Sektion auszublenden würde hier
 *     eine Funktion verstecken, die tatsächlich benutzbar ist.
 *
 * Exportiert, weil genau diese Ableitung die Stelle ist, an der sich UI und
 * Server-ACL auseinanderentwickeln könnten — sie gehört einzeln testbar.
 *
 * @param {{email: string|null, is_admin?: boolean}|null} user
 * @param {Array<{email: string, role: string}>} aclEintraege
 * @returns {boolean}
 */
export function darfBackupSehen(user, aclEintraege) {
  if (!user || !user.email) return true;  /* Legacy-Bypass, siehe oben */
  if (user.is_admin) return true;
  const meine = String(user.email).trim().toLowerCase();
  return (aclEintraege || []).some(e =>
    String(e && e.email || '').trim().toLowerCase() === meine && e.role === 'owner');
}

/**
 * Baut die Sektion. Sie wird SOFORT (verborgen) zurückgegeben, damit der
 * Aufrufer sie synchron einhängen kann; der Sichtbarkeits- und Ladevorgang
 * läuft asynchron nach.
 *
 * @param {string} boardId  Board-UUID des aktuell geöffneten Boards
 * @returns {{el: HTMLElement, bereit: Promise<boolean>}} `bereit` erfüllt sich
 *   mit der Sichtbarkeits-Entscheidung — für Tests und für Aufrufer, die auf
 *   den fertigen Zustand warten wollen.
 */
export function buildBackupSection(boardId) {
  _ensureBackupStyles();
  const container = el('div', { cls: 'config-backup-container' });
  container.hidden = true;

  container.appendChild(el('div', {
    cls: 'config-separator', text: I18n.t('config.backup-section'),
  }));
  container.appendChild(el('p', {
    cls: 'config-hint', text: I18n.t('config.backup-hint'),
  }));

  const flash = el('div', { cls: 'config-backup-flash' });
  flash.setAttribute('role', 'status');   /* Meldungen erreichen Screenreader */
  flash.setAttribute('aria-live', 'polite');

  const aktionen = el('div', { cls: 'config-backup-actions' });
  const jetztBtn = el('button', {
    cls: 'btn btn-sm btn-primary', type: 'button', text: I18n.t('admin.backup.btn-now'),
  });
  aktionen.appendChild(jetztBtn);

  const listeHost = el('div', { cls: 'config-backup-list' });

  container.appendChild(aktionen);
  container.appendChild(flash);
  container.appendChild(listeHost);

  const basis = `/boards/${encodeURIComponent(boardId)}/backup`;

  /* Generations-Marke gegen überlappende Ladevorgänge.
     `container.hidden = false` fällt VOR dem ersten `listeLaden()`, die Knöpfe
     sind also schon bedienbar, während die erste Liste noch unterwegs ist. Wer
     in diesem Fenster „Jetzt sichern" drückt (oder zweimal schnell löscht),
     startet einen zweiten Lauf auf denselben `listeHost`. Ohne Marke gewinnt
     die zuletzt EINTREFFENDE Antwort statt der zuletzt GESTELLTEN Anfrage:
     Bei Netz-Jitter überschreibt die alte Antwort die neue, und der Nutzer
     sieht trotz „Backup erstellt" seine frische Datei nicht.
     Muster aus knowledge/topics/nebenlaeufigkeit-und-lesefenster.md §11. */
  let ladeGeneration = 0;

  /* Fehler-Antworten des Servers tragen einen sprechenden Text (etwa
     „Backup ist verschlüsselt, ABOARD_SECRET_KEY nicht konfiguriert"). Den
     zu zeigen ist der Punkt: ein generisches „Fehler" liesse den Eigentümer
     im Dunkeln über eine Lage, die nur der Betreiber beheben kann. */
  async function fehlertext(res) {
    if (res.status === 429) return I18n.t('admin.error-429');
    try {
      const b = await res.json();
      if (b && b.error) return String(b.error);
    } catch { /* kein JSON — Standardtext */ }
    return I18n.t('admin.error-server');
  }

  async function listeLaden() {
    const meine = ++ladeGeneration;
    /* Nach JEDEM await prüfen, ob inzwischen ein neuerer Lauf gestartet ist —
       dann gehört die Anzeige ihm, und dieser Lauf tritt wortlos ab. */
    const ueberholt = () => meine !== ladeGeneration;

    listeHost.innerHTML = '';
    listeHost.appendChild(el('p', { cls: 'config-hint', text: I18n.t('admin.loading') }));
    let res;
    try {
      res = await holen(`${basis}/files`);
    } catch {
      if (ueberholt()) return;
      listeHost.innerHTML = '';
      listeHost.appendChild(el('p', { cls: 'config-hint', text: I18n.t('admin.error-server') }));
      return;
    }
    if (ueberholt()) return;
    listeHost.innerHTML = '';
    if (!res.ok) {
      const txt = await fehlertext(res);
      if (ueberholt()) return;
      listeHost.appendChild(el('p', { cls: 'config-hint', text: txt }));
      return;
    }
    const body  = await res.json().catch(() => ({}));
    if (ueberholt()) return;
    const files = (body && body.files) || [];
    if (files.length === 0) {
      listeHost.appendChild(el('p', { cls: 'config-hint', text: I18n.t('admin.backup.empty') }));
      return;
    }

    const tabelle = el('table', { cls: 'config-backup-table' });
    const thead = el('thead');
    const kopfZeile = el('tr');
    ['admin.backup.col-file', 'admin.backup.col-size', 'admin.backup.col-mtime', 'admin.backup.col-actions']
      .forEach(k => kopfZeile.appendChild(el('th', { text: I18n.t(k) })));
    thead.appendChild(kopfZeile);
    tabelle.appendChild(thead);

    const tbody = el('tbody');
    files.forEach(f => {
      const tr = el('tr');

      const nameZelle = el('td');
      nameZelle.appendChild(el('span', { text: f.filename }));
      /* #336: Verschlüsselte Archive kennzeichnen. Ohne diesen Hinweis lädt
         jemand ein `.tar.gz.enc` herunter, versucht `tar -tzf` und hält es
         für beschädigt. */
      if (f.encrypted) {
        nameZelle.appendChild(document.createTextNode(' '));
        nameZelle.appendChild(verschluesseltBadge());
      }
      tr.appendChild(nameZelle);

      tr.appendChild(el('td', { text: formatGroesse(f.size) }));
      tr.appendChild(el('td', { text: f.mtime }));

      const aktionsZelle = el('td');
      const dl = el('a', { cls: 'btn btn-sm', text: I18n.t('admin.backup.btn-download') });
      dl.href = `${basis}/download/${encodeURIComponent(f.filename)}`;
      dl.setAttribute('download', f.filename);
      aktionsZelle.appendChild(dl);

      const del = el('button', {
        cls: 'btn btn-sm btn-danger', type: 'button', text: I18n.t('admin.backup.btn-delete'),
      });
      del.addEventListener('click', async () => {
        if (!confirm(I18n.t('admin.backup.confirm-delete'))) return;
        del.disabled = true;
        let rr;
        try {
          rr = await holen(`${basis}/files/${encodeURIComponent(f.filename)}`, { method: 'DELETE' });
        } catch {
          del.disabled = false;
          flash.textContent = I18n.t('admin.error-server');
          return;
        }
        del.disabled = false;
        if (rr.ok) {
          flash.textContent = I18n.t('admin.backup.msg-deleted');
          listeLaden();
        } else {
          flash.textContent = await fehlertext(rr);
        }
      });
      aktionsZelle.appendChild(del);
      tr.appendChild(aktionsZelle);
      tbody.appendChild(tr);
    });
    tabelle.appendChild(tbody);
    listeHost.appendChild(tabelle);
  }

  jetztBtn.addEventListener('click', async () => {
    jetztBtn.disabled = true;
    flash.textContent = I18n.t('admin.loading');
    let res;
    try {
      res = await holen(`${basis}/now`, { method: 'POST' });
    } catch {
      jetztBtn.disabled = false;
      flash.textContent = I18n.t('admin.error-server');
      return;
    }
    jetztBtn.disabled = false;
    if (res.ok) {
      flash.textContent = I18n.t('admin.backup.msg-ok');
      listeLaden();
    } else {
      flash.textContent = await fehlertext(res);
    }
  });

  /* ── Wiederherstellen ───────────────────────────────────────────── */

  container.appendChild(el('div', {
    cls: 'config-subsection-title', text: I18n.t('admin.backup.upload.title'),
  }));

  const dateiRow = el('div', { cls: 'config-row' });
  const dateiLbl = el('label', { cls: 'config-label', text: I18n.t('admin.backup.upload.file') });
  const dateiInp = el('input', { cls: 'config-select', type: 'file', id: 'cfg-backup-file' });
  /* #336: `.enc` gehört in die Auswahl — sonst kann der Eigentümer sein
     eigenes verschlüsseltes Archiv im Dateidialog nicht auswählen. */
  dateiInp.accept = '.tar.gz,.gz,.enc';
  dateiLbl.setAttribute('for', 'cfg-backup-file');
  dateiRow.appendChild(dateiLbl);
  dateiRow.appendChild(dateiInp);
  container.appendChild(dateiRow);

  const modusRow = el('div', { cls: 'config-row' });
  const modusLbl = el('label', { cls: 'config-label', text: I18n.t('admin.backup.upload.mode') });
  const modusSel = el('select', { cls: 'config-select', id: 'cfg-backup-mode' });
  modusLbl.setAttribute('for', 'cfg-backup-mode');
  const optUeber = el('option', { text: I18n.t('admin.backup.upload.mode-over') });
  optUeber.value = 'overwrite';
  const optNeu = el('option', { text: I18n.t('admin.backup.upload.mode-new') });
  optNeu.value = 'as-new';
  modusSel.appendChild(optUeber);
  modusSel.appendChild(optNeu);
  modusRow.appendChild(modusLbl);
  modusRow.appendChild(modusSel);
  container.appendChild(modusRow);

  const restoreAktionen = el('div', { cls: 'config-backup-actions' });
  const restoreBtn = el('button', {
    cls: 'btn btn-sm btn-danger', type: 'button', text: I18n.t('admin.backup.upload.btn'),
  });
  restoreAktionen.appendChild(restoreBtn);
  container.appendChild(restoreAktionen);

  restoreBtn.addEventListener('click', async () => {
    const datei = dateiInp.files && dateiInp.files[0];
    if (!datei) { flash.textContent = I18n.t('config.backup-no-file'); return; }
    /* Der Überschreiben-Modus ist nicht umkehrbar — deshalb ein eigener,
       deutlicherer Text als beim Anlegen eines neuen Boards. */
    const frage = modusSel.value === 'overwrite'
      ? I18n.t('config.backup-confirm-overwrite')
      : I18n.t('admin.backup.confirm-restore');
    if (!confirm(frage)) return;

    restoreBtn.disabled = true;
    flash.textContent = I18n.t('admin.loading');
    const fd = new FormData();
    fd.append('file', datei);
    fd.append('mode', modusSel.value);
    /* Ziel ist IMMER das offene Board — der Eigentümer kann von hier aus
       kein fremdes Board überschreiben. Das unterscheidet diese Sektion vom
       Admin-Dialog, wo die Ziel-ID ein Freitextfeld ist. */
    if (modusSel.value === 'overwrite') fd.append('target_board_id', boardId);

    /* Karte #726: Das Ergebnis geht zusätzlich als Ereignis hinaus. main.js
       übernimmt nach „Überschreiben“ sofort den Server-Stand und meldet
       Erfolg oder Fehler als Toast, falls der Dialog inzwischen zu ist. */
    const modus = modusSel.value;
    let res;
    try {
      res = await holen('/backup/restore', { method: 'POST', body: fd });
    } catch {
      restoreBtn.disabled = false;
      flash.textContent = I18n.t('admin.error-server');
      EventBus.emit('backup:restore-failed', { text: flash.textContent });
      return;
    }
    restoreBtn.disabled = false;
    if (res.ok) {
      flash.textContent = I18n.t('admin.backup.msg-restored');
      EventBus.emit('backup:restored', { boardId, mode: modus });
      listeLaden();
    } else {
      flash.textContent = await fehlertext(res);
      EventBus.emit('backup:restore-failed', { text: flash.textContent });
    }
  });

  /* ── Sichtbarkeit asynchron entscheiden ─────────────────────────── */

  const bereit = (async () => {
    let user = null;
    let acl  = [];
    try {
      const plugin = StoragePlugin.getActivePlugin();
      user = await plugin.getCurrentUser?.() ?? null;
      acl  = await plugin.aclList?.(boardId) ?? [];
    } catch {
      /* Lookup gescheitert — Sektion verborgen lassen. Lieber eine fehlende
         Sektion als eine, die bei jedem Klick 403 liefert. */
      return false;
    }
    if (!darfBackupSehen(user, acl)) return false;
    container.hidden = false;
    await listeLaden();
    return true;
  })();

  /* Muss der Aufrufer bei jedem Öffnen des Dialogs rufen.
     Grund: Der Board-Einstellungsdialog ist ein Singleton — `buildOverlay`
     und damit diese Funktion laufen genau EINMAL, beim ersten Öffnen
     (configdialog.js, `openPartition`). Ohne diesen Aufruf zeigt die Sektion
     bis zum nächsten Sprach- oder Board-Wechsel den Stand von damals: das
     nächtliche Backup fehlt, das ein Kollege oder der Server inzwischen
     angelegt hat — obwohl der Hinweistext der Sektion genau das verspricht.
     Der Admin-Dialog hat das Problem nicht, weil er seinen Rumpf bei jedem
     Tab-Wechsel neu baut; beim Spiegeln hierher ist der Unterschied
     untergegangen. */
  function aktualisieren() {
    if (container.hidden) return;   /* nicht sichtbar → nichts nachzuladen */
    listeLaden();
  }

  return { el: container, bereit, aktualisieren };
}
