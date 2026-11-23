/* admin-dialog.js — Server-Administration UI (#306, ADR-0101).
 *
 * Separater Dialog (NICHT Settings) mit Sektion-Tabs in fester Reihenfolge.
 * Im MVP (#306) sind nur "System-Status" und "Audit-Log" funktionsfähig;
 * SMTP/IMAP/Auto-Reply/Service-Board/Backup/User-Verwaltung sind leere
 * Hülle-Tabs mit Hinweis auf die Folge-Karte.
 *
 * Gesamtes Modul hinter IS_SERVER_BUILD gegated → im Slim/Claude-Code-Build
 * per DCE eliminiert (kein Bundle-Impact, keine i18n-Key-Leaks).
 *
 * Stilistisch lehnt sich der Dialog an buildShareContent (share-section.js) an: einfache DOM-
 * Konstruktion, kein neues Framework, Markup mit denselben Sidebar-Klassen.
 */

import { I18n } from './i18n.js';
import { AdminClient } from './admin-client.js';
import { EventBus } from './eventbus.js';
import { ClientsProjectsClient } from './clients-projects-client.js';
/* #348/ADR-0118: Overlay/Panel/Header/Body + ESC/Klick-außerhalb/Focus-Trap
   kommen aus der gemeinsamen Modal-Shell — kein eigener buildShell()/onKey()
   mehr. `extraBodyClass:'admin-body'` erhält den Bestandsschutz für die
   umfangreiche Tabs-Test-Suite (admin-dialog.test.js selektiert direkt
   gegen `.admin-body`). */
import { createModal } from './modal-shell.js';
import { Rueckmeldung } from './rueckmeldung.js';
/* Gemeinsame Darstellungs-Helfer der Backup-Liste — beide Oberflächen (dieser
   Admin-Dialog und die Eigentümer-Sektion im Board-Einstellungsdialog) lesen
   dieselbe REST-Antwort und sollen sie gleich zeigen. Beide Module sind
   IS_SERVER_BUILD-gegated, der Import zieht also nichts ins Solo-Bundle. */
import { formatGroesse, verschluesseltBadge } from './backup-section.js';
/* Für „eigenes Konto nicht löschbar anbieten" (Karte #575) — analog
   backup-section.js: StoragePlugin.getActivePlugin().getCurrentUser()
   liefert im Standalone-/Solo-Fall synchron {name:null,email:null} (kein
   Netz), im Server-Modus die Forward-Auth-Identität samt is_admin. */
import { StoragePlugin } from './storage-plugin.js';
import { vergissBoardpaarSeite, boardpaarSeite } from './boardpaar.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Im Nicht-Server-Build ist AdminDialog null + alle Konsumenten-Sites
   gegated → DCE'd vom Bundle. */
let AdminDialog = null;

if (IS_SERVER_BUILD) {

  /* Section-Definition. Reihenfolge ist UI-relevant; cardId benennt die
     Folge-Karte (oder null = MVP-Sektion + im Code befüllt). */
  const SECTIONS = [
    { id: 'status',    labelKey: 'admin.tab.status',    cardId: null }, /* #306 (MVP) */
    { id: 'audit',     labelKey: 'admin.tab.audit',     cardId: null }, /* #306 (MVP) */
    { id: 'smtp',      labelKey: 'admin.tab.smtp',      cardId: '#293' },
    { id: 'imap',      labelKey: 'admin.tab.imap',      cardId: '#295' },
    { id: 'autoreply', labelKey: 'admin.tab.autoreply', cardId: '#297' },
    { id: 'service',   labelKey: 'admin.tab.service',   cardId: null }, /* #294 (umgesetzt) */
    { id: 'backup',    labelKey: 'admin.tab.backup',    cardId: null }, /* #303 (umgesetzt) */
    { id: 'clients-projects', labelKey: 'admin.tab.clients-projects', cardId: null }, /* #307/ADR-0102 (umgesetzt) */
    { id: 'kalender',  labelKey: 'admin.tab.kalender',  cardId: null }, /* #742/ADR-0138 §2 */
    { id: 'wissen',    labelKey: 'admin.tab.wissen',    cardId: null }, /* #739/ADR-0137 §2 */
    { id: 'boardpaar', labelKey: 'admin.tab.boardpaar', cardId: null }, /* #788/ADR-0140 §4 */
    { id: 'users',     labelKey: 'admin.tab.users',     cardId: null }, /* #575 (umgesetzt) */
    { id: 'agents',    labelKey: 'admin.tab.agents',    cardId: null }, /* #608 (ADR-0130 §6) */
    /* Karte #684 (ADR-0134): nur sichtbar, wenn /admin/status `schleuse: "an"`
       meldet — zeigeSchleuseTab() hängt den Reiter dann an (dynamisch). */
    { id: 'schleuse',  labelKey: 'admin.tab.schleuse',  cardId: null, dynamisch: true },
  ];

  let _modal = null;
  let _bodyEl = null;
  let _activeSection = 'status';
  /* #575: Erfolgsmeldung einer Löschung überlebt den sofortigen Reload der
     Liste (renderUsers() leert _bodyEl komplett) — als Variable statt als
     DOM-Knoten im gerade entfernten Bestätigungs-Panel transportiert, sonst
     verschwindet "Gelöscht. Pseudonym: …" im selben Tick, in dem es
     erscheint. Wird nach EINER Anzeige zurückgesetzt. */
  let _usersFlashMsg = '';
  /* V-1 (Verify Sprint 72): Nummer des jüngsten renderUsers()-Laufs — ein
     älterer, der nach schnellem Weg-und-zurück-Wechseln später fertig wird,
     rendert nicht mehr. */
  let _usersLauf = 0;

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  /* Karte #627: EINE Stelle für die Rückmeldung jeder Aktion. Vorher setzte
     jeder Handler `flash.textContent` von Hand, und manche (Backup „Jetzt
     sichern“ bei leerer Board-ID) gar nichts — der Klick blieb unsichtbar.
     `meldung` färbt die Zeile (busy/ok/err) und setzt role (err → alert,
     sonst status), `markiereKnopf` lässt den Knopf kurz aufleuchten,
     `starte`/`beende` klammern eine laufende Aktion. */
  function meldung(flash, art, text, rolle) {
    flash.classList.add('admin-flash');
    Rueckmeldung.meldeZeile(flash, 'admin-flash', art, text, rolle);
  }
  function markiereKnopf(btn, art) {
    Rueckmeldung.blinkeKnopf(btn, 'admin-btn', art);
  }
  function starte(btn, flash) {
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    meldung(flash, 'busy', I18n.t('admin.working'));
  }
  function beende(btn, flash, art, text) {
    btn.disabled = false;
    btn.removeAttribute('aria-busy');
    meldung(flash, art, text);
    markiereKnopf(btn, art);
  }
  /* Inline-Rückmeldung neben einem Schalter/Knopf in einer Tabellenzeile.
     Hat die Zelle noch kein Meldungselement, entsteht es hier. */
  function zeilenMeldung(span, art, text) {
    span.textContent = text;
    span.className = 'admin-row-msg admin-row-msg--' + art + (art === 'err' ? ' admin-users-row-error' : '');
    span.setAttribute('role', art === 'err' ? 'alert' : 'status');
  }
  /* F-2019: letzter Abruf (innen) bzw. Eingang (außen) einer Kopplung. Ein
     Fehlgrund steht rot in der Zelle — sonst sähe der Admin nicht, dass ein
     Board auf dem alten Stand hängt. */
  function abgleichZelle(art, e) {
    const zeit = art === 'innen' ? e.letzte_abfrage : e.letzter_eingang;
    const cell = el('td');
    if (!zeit) {
      cell.textContent = I18n.t('admin.boardpaar.abgleich-nie-' + art);
      return cell;
    }
    const wann = zeit.slice(0, 16).replace('T', ' ');
    if (e.letzter_fehlgrund) {
      const span = el('span', { cls: 'admin-row-msg admin-row-msg--err',
        text: I18n.t('admin.boardpaar.abgleich-fehler', wann, e.letzter_fehlgrund) });
      cell.appendChild(span);
    } else {
      cell.textContent = I18n.t('admin.boardpaar.abgleich-ok', wann);
    }
    return cell;
  }
  /* Backup-Aktionen: 404/503 haben eigene, verständliche Texte. */
  function backupFehler(status, grund) {
    if (status === 404) return I18n.t('admin.backup.err-not-found');
    /* F-1438: 503 hat mehrere Ursachen (Secret-Key fehlt/ungültig, Backup
       abgeschaltet …) — den Grund nennt der Server im Feld `error`. */
    if (status === 503) {
      return grund ? I18n.t('admin.backup.err-503', grund) : I18n.t('admin.backup.err-503-neutral');
    }
    return serverFehler(status);
  }
  /* Fehlertext aus einer Antwort: bei 503 zuerst den Grund aus dem Body lesen. */
  async function backupFehlerAus(r) {
    let grund = '';
    if (r && r.status === 503) {
      try { const b = await r.json?.(); grund = (b && typeof b.error === 'string') ? b.error : ''; }
      catch { grund = ''; }
    }
    return backupFehler(r ? r.status : 0, grund);
  }
  /* Server-Status → lesbarer Fehlertext (429 hat einen eigenen). */
  function serverFehler(status) {
    if (status === 429) return I18n.t('admin.error-429');
    if (status === 403) return I18n.t('admin.error-403');
    return I18n.t('admin.error-server');
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'admin-overlay',
      extraPanelClass: 'admin-panel',
      extraBodyClass: 'admin-body',
      titleId: 'admin-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      /* Karte #670: über close(), damit ein angezeigtes Agenten-Token beim
         Schliessen aus dem DOM verschwindet (Esc, Overlay, ✕). */
      onRequestClose: () => close(),
    });
    _modal.setTitle(I18n.t('admin.title'));
    _bodyEl = _modal.body;

    /* Tab-Bar: zwischen Header und Body eingehängt — kein Shell-Konzept,
       bleibt beim Sektionswechsel stehen (nur `_bodyEl` wird neu gerendert). */
    const tabbar = el('div', { cls: 'admin-tabbar' });
    SECTIONS.forEach(s => {
      if (!s.dynamisch) tabbar.appendChild(neuerTab(s));
    });
    _modal.panel.insertBefore(tabbar, _bodyEl);
  }

  function neuerTab(s) {
    const tab = el('button', { cls: 'admin-tab', text: I18n.t(s.labelKey) });
    tab.dataset.section = s.id;
    tab.addEventListener('click', () => selectSection(s.id));
    return tab;
  }

  /* Karte #684: Reiter „Ausgangsschleuse“ nur bei eingeschalteter Schleuse —
     bei ABOARD_SCHLEUSE=aus zeigt die Oberfläche nichts davon (ADR-0134 §1). */
  function zeigeSchleuseTab(an) {
    const bar = _modal?.panel.querySelector('.admin-tabbar');
    if (!bar) return;
    const da = bar.querySelector('.admin-tab[data-section="schleuse"]');
    if (an && !da) bar.appendChild(neuerTab(SECTIONS.find(s => s.id === 'schleuse')));
    if (!an && da) da.remove();
  }

  function selectSection(id) {
    if (id !== 'boardpaar') _boardpaarAufraeumHinweis = null; /* Verify A-1 */
    _activeSection = id;
    _kontenLauf++; /* #1078: eine laufende Einladung/Aktion gehört zum verlassenen Reiter */
    /* Active-Tab markieren */
    _modal.panel.querySelectorAll('.admin-tab').forEach(t => {
      t.classList.toggle('admin-tab--active', t.dataset.section === id);
    });
    return renderBody(); /* #327: open() braucht das Promise für Initial-Focus */
  }

  async function renderBody() {
    if (!_bodyEl) return;
    _bodyEl.innerHTML = '';
    const section = SECTIONS.find(s => s.id === _activeSection);
    if (!section) return;

    if (section.id === 'status') {
      await renderStatus();
    } else if (section.id === 'audit') {
      await renderAudit();
    } else if (section.id === 'smtp') {
      /* #293/ADR-0091: SMTP-Sektion ist mit dieser Karte funktional. */
      await renderSMTP();
    } else if (section.id === 'service') {
      /* #294/ADR-0092: Service-Board-Sektion. */
      await renderService();
    } else if (section.id === 'imap') {
      /* #295/ADR-0093: IMAP-Mail-Ingest-Sektion. */
      await renderIMAP();
    } else if (section.id === 'autoreply') {
      /* #297: Mail-Auto-Reply-Sektion. */
      await renderAutoReply();
    } else if (section.id === 'backup') {
      /* #303/ADR-0098: Board-Backup-Sektion. */
      await renderBackup();
    } else if (section.id === 'clients-projects') {
      /* #307/ADR-0102: Clients-+Projekte-Sektion (Toggles + Default-Picker). */
      await renderClientsProjects();
    } else if (section.id === 'kalender') {
      /* #742/ADR-0138 §2: Instanz-Zeitzone der Uhrzeiten im Abo-Feed. */
      await renderKalender();
    } else if (section.id === 'boardpaar') {
      /* #788/ADR-0140 §4: Board-Paar — Seite, Partnerlisten, Zugänge. */
      await renderBoardpaar();
    } else if (section.id === 'wissen') {
      /* #739/ADR-0137 §2: Wissensquelle — Schalter, Ordner, Sichtbarkeit. */
      await renderWissen();
    } else if (section.id === 'agents') {
      await renderAgents();
    } else if (section.id === 'schleuse') {
      /* Karte #684 (ADR-0134 §2): Freigabeliste der Ausgangsschleuse. */
      await renderSchleuse();
    } else if (section.id === 'users') {
      /* #575, Folge zu #571: Nutzerliste + Admin-Umschalter + Löschen. */
      await renderUsers();
    } else if (section.cardId) {
      /* F-748: derzeit ohne Laufzeit-Testabdeckung — mit der aktuellen
         SECTIONS-Tabelle hat jede Sektion mit gesetztem cardId (smtp/imap/
         autoreply) einen eigenen `else if`-Zweig weiter oben und erreicht
         diesen Pfad nie; er greift erst, sobald eine künftige Stub-Sektion
         einen cardId OHNE eigenen Renderer bekommt. Der Quelltext-Wächter
         (tests/i18n-double-call-guard.test.js) deckt die Zeile trotzdem ab,
         weil er das Muster statisch sucht statt den Pfad zu betreten. */
      _bodyEl.appendChild(el('p', { cls: 'admin-placeholder',
        text: I18n.t('admin.placeholder-card', section.cardId) }));
    } else {
      _bodyEl.appendChild(el('p', { cls: 'admin-placeholder',
        text: I18n.t('admin.placeholder-future') }));
    }
  }

  /* #293/ADR-0091: SMTP-Sektion mit Feldern + Test-Button. */
  const SMTP_FIELDS = [
    { key: 'smtp.host',      labelKey: 'admin.smtp.host',      secret: false, placeholder: 'mail.example.org' },
    { key: 'smtp.port',      labelKey: 'admin.smtp.port',      secret: false, placeholder: '587' },
    { key: 'smtp.user',      labelKey: 'admin.smtp.user',      secret: false, placeholder: '' },
    { key: 'smtp.pass',      labelKey: 'admin.smtp.pass',      secret: true,  placeholder: '••••••••' },
    { key: 'smtp.from',      labelKey: 'admin.smtp.from',      secret: false, placeholder: 'aboard@example.org' },
    { key: 'smtp.from_name', labelKey: 'admin.smtp.from_name', secret: false, placeholder: 'aBoard' },
    { key: 'smtp.starttls',  labelKey: 'admin.smtp.starttls',  secret: false, placeholder: 'true' },
  ];

  async function renderSMTP() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('smtp');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const map = {};
    (Array.isArray(res.body?.entries) ? res.body.entries : []).forEach(e => { map[e.key] = e; });

    const form = el('div', { cls: 'admin-smtp-form' });
    SMTP_FIELDS.forEach(f => {
      const row = el('div', { cls: 'admin-form-row' });
      const label = el('label', { cls: 'admin-form-label', text: I18n.t(f.labelKey) });
      const input = el('input', { cls: 'admin-form-input' });
      input.type = f.secret ? 'password' : 'text';
      input.placeholder = f.placeholder;
      const existing = map[f.key];
      if (existing && !f.secret) input.value = existing.value || '';
      if (f.secret && existing && existing.is_set) input.placeholder = '••••• (gespeichert; leer = unverändert)';
      input.dataset.key = f.key;
      input.dataset.secret = String(f.secret);
      row.appendChild(label);
      row.appendChild(input);
      form.appendChild(row);
    });
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.btn-test-section') });
    const flash = el('div', { cls: 'admin-flash' });

    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      const inputs = form.querySelectorAll('input.admin-form-input');
      let failed = false;
      let fehlerStatus = 0;
      for (const i of inputs) {
        const key = i.dataset.key;
        const secret = i.dataset.secret === 'true';
        const val = i.value;
        if (secret && val === '') continue; /* leer = unverändert lassen */
        const r = await AdminClient.setConfig(key, val, secret);
        if (!r.ok) { failed = true; fehlerStatus = r.status; break; }
      }
      if (failed) { beende(saveBtn, flash, 'err', serverFehler(fehlerStatus)); return; }
      /* O-8: der Mail-Worker liest diese Werte beim Start → Neustart der Sektion „imap“ */
      await speichernMeldung(saveBtn, flash);
    });

    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      const r = await AdminClient.testSection('smtp');
      if (r.ok) beende(testBtn, flash, 'ok', I18n.t('admin.msg-test-ok'));
      else beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.msg-test-fail'));
    });

    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint',
      text: I18n.t('admin.smtp.hint') }));
  }

  /* #294/ADR-0092: Service-Board-Sektion — analog renderSMTP, kompakter,
     ohne secret-Felder. Test-Button löst serverseitiges Re-Seed aus
     (idempotent — bestehende Karten bleiben). */
  const SERVICE_FIELDS = [
    { key: 'service-board.enabled',   labelKey: 'admin.service.enabled',   placeholder: 'true' },
    { key: 'service-board.title',     labelKey: 'admin.service.title',     placeholder: 'Service' },
    { key: 'service-board.spam_drop', labelKey: 'admin.service.spam-drop', placeholder: 'false' },
  ];

  async function renderService() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('service-board');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const map = {};
    (Array.isArray(res.body?.entries) ? res.body.entries : []).forEach(e => { map[e.key] = e; });

    const form = el('div', { cls: 'admin-smtp-form' });
    SERVICE_FIELDS.forEach(f => {
      const row = el('div', { cls: 'admin-form-row' });
      const label = el('label', { cls: 'admin-form-label', text: I18n.t(f.labelKey) });
      const input = el('input', { cls: 'admin-form-input' });
      input.type = 'text';
      input.placeholder = f.placeholder;
      const existing = map[f.key];
      if (existing) input.value = existing.value || '';
      input.dataset.key = f.key;
      row.appendChild(label);
      row.appendChild(input);
      form.appendChild(row);
    });
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.service.btn-reseed') });
    const flash = el('div', { cls: 'admin-flash' });

    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      let failed = false;
      let fehlerStatus = 0;
      for (const i of form.querySelectorAll('input.admin-form-input')) {
        const r = await AdminClient.setConfig(i.dataset.key, i.value, false);
        if (!r.ok) { failed = true; fehlerStatus = r.status; break; }
      }
      if (failed) { beende(saveBtn, flash, 'err', serverFehler(fehlerStatus)); return; }
      /* O-8: der Mail-Worker liest diese Werte beim Start → Neustart der Sektion „imap“ */
      await speichernMeldung(saveBtn, flash, 'service-board');
    });

    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      const r = await AdminClient.testSection('service-board');
      if (r.ok) beende(testBtn, flash, 'ok', I18n.t('admin.service.msg-reseeded'));
      else beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.msg-test-fail'));
    });

    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.service.hint') }));
  }

  /* #739/ADR-0137 §2: Wissensquelle. Schalter als Häkchen und Sichtbarkeit
     als Auswahl statt „true/false"-Textfeldern (einfach bedienbar). Der
     Server prüft jeden Wert beim Speichern (dir nur unter /data/,
     bearbeiten_url nur https mit {pfad}) und nennt bei 400 den Grund.
     Gespeichert wird der Schalter ZULETZT: Wer Ordner und Häkchen in einem
     Zug setzt, schaltet nicht ein, bevor der Ordner angenommen ist. */
  const WISSEN_TEXTFELDER = [
    { key: 'wissen.dir',            labelKey: 'admin.wissen.dir',            placeholder: '/data/wissen' },
    { key: 'wissen.name',           labelKey: 'admin.wissen.name',           placeholder: 'Wissen' },
    { key: 'wissen.bearbeiten_url', labelKey: 'admin.wissen.bearbeiten_url', placeholder: 'https://forge.example/org/repo/_edit/main/{pfad}' },
  ];

  async function renderWissen() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('wissen');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const map = {};
    (Array.isArray(res.body?.entries) ? res.body.entries : []).forEach(e => { map[e.key] = e.value || ''; });

    const form = el('div', { cls: 'admin-smtp-form' });
    const zeile = (id, labelKey, feld) => {
      const row = el('div', { cls: 'admin-form-row' });
      feld.id = id;
      row.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t(labelKey), attrs: { for: id } }));
      row.appendChild(feld);
      form.appendChild(row);
    };
    const an = el('input');
    an.type = 'checkbox';
    an.checked = map['wissen.enabled'] === 'true';
    zeile('admin-wissen-enabled', 'admin.wissen.enabled', an);
    const felder = WISSEN_TEXTFELDER.map(f => {
      const input = el('input', { cls: 'admin-form-input' });
      input.type = 'text';
      input.placeholder = f.placeholder;
      input.value = map[f.key] || '';
      input.dataset.key = f.key;
      zeile('admin-' + f.key.replace('.', '-'), f.labelKey, input);
      return input;
    });
    const sichtbar = el('select', { cls: 'admin-form-input' });
    for (const [wert, key] of [['alle', 'admin.wissen.sichtbar-alle'], ['admins', 'admin.wissen.sichtbar-admins'],
      ['gruppe', 'admin.wissen.sichtbar-gruppe']]) {
      const o = el('option', { text: I18n.t(key) });
      o.value = wert;
      sichtbar.appendChild(o);
    }
    /* F-1863: „gruppe:<name>“ — Name aus Remote-Groups des Anmeldedienstes. */
    const gespeichert = map['wissen.sichtbar'] || '';
    const gruppe = el('input', { cls: 'admin-form-input' });
    gruppe.type = 'text';
    gruppe.placeholder = 'wissen';
    gruppe.value = gespeichert.startsWith('gruppe:') ? gespeichert.slice('gruppe:'.length) : '';
    sichtbar.value = gespeichert === 'admins' ? 'admins' : gespeichert.startsWith('gruppe:') ? 'gruppe' : 'alle';
    zeile('admin-wissen-sichtbar', 'admin.wissen.sichtbar', sichtbar);
    zeile('admin-wissen-gruppe', 'admin.wissen.gruppe', gruppe);
    const gruppeZeile = gruppe.closest('.admin-form-row') || gruppe.parentElement;
    /* S-1: `.admin-form-row { display: contents }` schlägt [hidden] — deshalb
       zusätzlich display setzen (Test: tests/admin-dialog.test.js, Block Wissen). */
    const zeigeGruppe = () => {
      const aus = sichtbar.value !== 'gruppe';
      gruppeZeile.hidden = aus;
      gruppeZeile.style.display = aus ? 'none' : '';
    };
    sichtbar.addEventListener('change', zeigeGruppe);
    zeigeGruppe();
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.wissen.btn-pruefen') });
    const flash = el('div', { cls: 'admin-flash' });
    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      const werte = felder.map(i => [i.dataset.key, i.value.trim()]);
      /* Leerer Gruppenname → „gruppe:“; der Server lehnt das ab und die
         Meldung nennt das Feld (kein stilles Zurückfallen auf „alle“). */
      werte.push(['wissen.sichtbar', sichtbar.value === 'gruppe' ? 'gruppe:' + gruppe.value.trim() : sichtbar.value]);
      /* Einschalten zuletzt (erst muss der Ordner angenommen sein),
         Ausschalten zuerst: Den Ordner leeren darf nur, wer schon aus ist,
         und bei verschwundenem Mount soll Ausschalten nie an einem anderen
         Feld scheitern (Verify #739 V-1/V-2). */
      if (an.checked) werte.push(['wissen.enabled', 'true']);
      else werte.unshift(['wissen.enabled', 'false']);
      /* Kritiker #739 K-6: Gespeichert wird Wert für Wert. Scheitert einer,
         sind die davor schon wirksam — die Meldung nennt sie, statt so zu
         tun, als sei nichts passiert. */
      const geschafft = [];
      for (const [key, value] of werte) {
        const r = await AdminClient.setConfig(key, value, false);
        if (!r.ok) {
          const grund = (r.status === 400 && r.body?.error) || serverFehler(r.status);
          beende(saveBtn, flash, 'err', geschafft.length ? I18n.t('admin.wissen.teil', grund, geschafft.join(', ')) : grund);
          return;
        }
        geschafft.push(key);
      }
      beende(saveBtn, flash, 'ok', I18n.t('admin.msg-saved'));
    });
    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      const r = await AdminClient.testSection('wissen');
      const e = r.body?.result;
      if (r.ok && e) {
        const st = e.stand ? [e.stand.commit, e.stand.datum].filter(Boolean).join(' ') : '';
        beende(testBtn, flash, 'ok', I18n.t('admin.wissen.msg-ok', e.seiten, st));
      } else beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.msg-test-fail'));
    });
    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.wissen.hint') }));
  }

  /* #742/ADR-0138 §2: Kalender-Sektion — nur die Instanz-Zeitzone. Uhrzeiten
     an Karten sind Ortszeit dieser Zone; nur der Abo-Feed rechnet nach UTC um.
     „Prüfen" löst den gespeicherten Namen auf dem Server auf: ein Tippfehler
     hiesse sonst still „Feed in UTC". */
  async function renderKalender() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('kalender');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const eintraege = Array.isArray(res.body?.entries) ? res.body.entries : [];
    const eintrag = eintraege.find(e => e.key === 'kalender.zeitzone');

    const form = el('div', { cls: 'admin-smtp-form' });
    const row = el('div', { cls: 'admin-form-row' });
    const input = el('input', { cls: 'admin-form-input', attrs: { id: 'admin-kalender-zeitzone' } });
    input.type = 'text';
    input.placeholder = 'Europe/Berlin';
    input.value = eintrag?.value || '';
    row.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.kalender.zeitzone'), attrs: { for: 'admin-kalender-zeitzone' } }));
    row.appendChild(input);
    form.appendChild(row);
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.kalender.btn-pruefen') });
    const flash = el('div', { cls: 'admin-flash' });
    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      const r = await AdminClient.setConfig('kalender.zeitzone', input.value.trim(), false);
      if (r.ok) beende(saveBtn, flash, 'ok', I18n.t('admin.msg-saved'));
      /* 400 = unbekannte Zone (K-4): die Server-Meldung nennt den Namen. */
      else beende(saveBtn, flash, 'err', (r.status === 400 && r.body?.error) || serverFehler(r.status));
    });
    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      const r = await AdminClient.testSection('kalender');
      const e = r.body?.result;
      if (r.ok && e) beende(testBtn, flash, 'ok', I18n.t('admin.kalender.msg-ok', e.zeitzone, e.ortszeit));
      else beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.msg-test-fail'));
    });
    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.kalender.hint') }));
  }

  /* Karte #788 (ADR-0140 §4): Board-Paar intern/extern — Seite der Instanz,
     Partnerlisten innen und außen, Zugänge abs_/abe_. Koppeln gibt Titel nach
     außen frei und ist deshalb Sache der Server-Administration. Der
     Klartext eines neuen Zugangs steht GENAU EINMAL im Dialog (wie #748). */
  let _boardpaarEinmal = null;
  /* #848: Ergebnis von „Abgeschlossene entfernen“, wenn etwas übersprungen
     wurde — überlebt den Neuaufbau des Reiters genau einmal. */
  let _boardpaarAufraeumHinweis = null;
  /* Verify A-3: dieselbe Form, die der Server (time.RFC3339Nano) liest. */
  const RFC3339 = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/;
  let _boardpaarLauf = 0;
  let _boardpaarMeldung = null; /* #831: Erfolg des Notwegs über das Neuzeichnen hinweg */

  /* #831 (ADR-0141 Nachtrag, F-2232): Knopf „Kopie außen löschen“ mit
     Tipp-Bestätigung wie beim Löschen einer Person — erst wenn der Name
     (ohne Namen: die Board-ID) exakt eingegeben ist, wird der Knopf aktiv.
     Kein natives confirm(): Ein Klick darf hier nichts endgültig löschen. */
  function kopieLoeschKnopf(boardId, titelText, cell, msg, neu, offene) {
    /* Kritik #831 K-2: Titel mit Rand-Leerzeichen oder anderer Unicode-Form
       wären sonst nie bestätigbar — beide Seiten trimmen und NFC-normalisieren;
       die Board-ID gilt immer als Bestätigung. */
    const norm = (t) => String(t ?? '').trim().normalize('NFC');
    const name = norm(titelText) || boardId;
    const bestaetigt = (eingabe) => { const e = norm(eingabe); return e !== '' && (e === name || e === boardId); };
    const anzeige = titelText ? `${titelText} (${boardId})` : boardId;
    const knopf = el('button', { cls: 'btn admin-btn admin-boardpaar-kopie-loeschen', text: I18n.t('admin.boardpaar.btn-kopie-loeschen'),
      attrs: { 'aria-label': I18n.t('admin.boardpaar.aria-kopie-loeschen', anzeige) } });
    knopf.addEventListener('click', () => {
      knopf.disabled = true;
      const panel = el('div', { cls: 'admin-users-confirm admin-boardpaar-kopie-confirm' });
      panel.appendChild(el('p', { cls: 'admin-users-confirm-warning', text: I18n.t('admin.boardpaar.kopie-loeschen-warnung') }));
      /* F-2240: Wie viele noch nicht übertragene Änderungen mit der Kopie
         verloren gehen, steht VOR dem Löschen da (Zahl aus der Liste). */
      if (Number.isFinite(offene) && offene > 0) {
        panel.appendChild(el('p', { cls: 'admin-users-confirm-warning admin-boardpaar-kopie-offene',
          text: I18n.t('admin.boardpaar.kopie-loeschen-offene', offene), attrs: { role: 'alert' } }));
      }
      const feldId = 'admin-boardpaar-kopie-del-' + String(boardId).replace(/[^A-Za-z0-9_-]/g, '_');
      panel.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.boardpaar.kopie-loeschen-label', name), attrs: { for: feldId } }));
      const feld = el('input', { cls: 'admin-form-input', attrs: { id: feldId, autocomplete: 'off' } });
      feld.type = 'text';
      panel.appendChild(feld);
      const leiste = el('div', { cls: 'admin-actions' });
      const ok = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.boardpaar.kopie-loeschen-ok') });
      ok.disabled = true;
      const ab = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.boardpaar.kopie-loeschen-abbrechen') });
      leiste.appendChild(ok);
      leiste.appendChild(ab);
      panel.appendChild(leiste);
      feld.addEventListener('input', () => { ok.disabled = !bestaetigt(feld.value); });
      ab.addEventListener('click', () => {
        panel.remove();
        knopf.disabled = false;
        zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
        knopf.focus();
      });
      ok.addEventListener('click', async () => {
        if (ok.disabled || !bestaetigt(feld.value)) return;
        ok.disabled = true;
        ab.disabled = true;
        zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
        const r = await AdminClient.loescheBoardpaarKopie(boardId);
        if (r.ok) {
          _boardpaarMeldung = I18n.t('admin.boardpaar.kopie-geloescht', anzeige);
          const verworfen = r.body && r.body.offene_aenderungen;
          if (Number.isFinite(verworfen) && verworfen > 0) {
            _boardpaarMeldung += ' ' + I18n.t('admin.boardpaar.kopie-geloescht-offene', verworfen);
          }
          await neu();
          return;
        }
        if (_activeSection !== 'boardpaar' || !ok.isConnected) return;
        ab.disabled = false;
        ok.disabled = !bestaetigt(feld.value);
        markiereKnopf(ok, 'err');
        zeilenMeldung(msg, 'err', (r.status === 409 && I18n.t('admin.boardpaar.err-keine-kopie')) ||
          (r.status === 404 && I18n.t('admin.boardpaar.err-404')) || serverFehler(r.status));
      });
      cell.insertBefore(panel, msg);
      feld.focus();
    });
    return knopf;
  }

  /* Karte #836 (ADR-0141 Nachtrag): Schalter „bearbeitbar“ je Kopie — nur
     außen, nur Kopien; Stufe-1-Zeilen zeigen „—“. Wirksam nur zusammen mit
     „Bearbeiten außen erlauben“; das sagt der Hinweis unter der Tabelle. Ein
     Fehlschlag stellt das Häkchen zurück. */
  function bearbeitbarZelle(e, seite, anzeige) {
    const td = el('td');
    if (!e.kopie) { td.textContent = '—'; return td; }
    const cb = el('input', { cls: 'admin-boardpaar-bearbeitbar',
      attrs: { type: 'checkbox', 'aria-label': I18n.t('admin.boardpaar.aria-bearbeitbar', anzeige) } });
    cb.checked = e.bearbeitbar === true;
    cb.disabled = seite !== 'aussen';
    const msg = el('span', { cls: 'admin-row-msg' });
    cb.addEventListener('change', async () => {
      const an = cb.checked;
      cb.disabled = true;
      zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
      const r = await AdminClient.setBoardpaarBearbeitbar(e.board_id, an);
      if (!cb.isConnected) return;
      cb.disabled = false;
      if (!r.ok) {
        cb.checked = !an;
        /* Die Meldung des Servers ist deutsch und nennt den Grund (keine
           Kopie, nicht außen, nicht gekoppelt). */
        zeilenMeldung(msg, 'err', r.body?.error || serverFehler(r.status));
        return;
      }
      e.bearbeitbar = an;
      zeilenMeldung(msg, 'ok', I18n.t('admin.msg-saved'));
    });
    td.appendChild(cb);
    td.appendChild(msg);
    return td;
  }

  async function renderBoardpaar() {
    const lauf = _boardpaarLauf;
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const [res, boardsRes] = await Promise.all([
      AdminClient.getBoardpaar(),
      Promise.resolve().then(() => AdminClient.listAllBoards?.()).catch(() => null),
    ]);
    if (lauf !== _boardpaarLauf || _activeSection !== 'boardpaar') return;
    _bodyEl.innerHTML = '';
    /* Sprint-94-Kritik F-2 (wie K-B7 im Users-Reiter): Die Erfolgsmeldung des
       Notwegs steht VOR einem möglichen Ladefehler und bekommt den Fokus —
       sonst wüsste der Admin nach einem 503 nicht, ob die unumkehrbare
       Löschung lief, und die Meldung tauchte später ohne Bezug auf. */
    if (_boardpaarMeldung) {
      const f = el('div', { cls: 'admin-flash', attrs: { role: 'status', tabindex: '-1' } });
      meldung(f, 'ok', _boardpaarMeldung, 'status');
      _bodyEl.appendChild(f);
      _boardpaarMeldung = null;
      f.focus();
    }
    /* F-2329/Verify A-1: Ergebnis von „Abgeschlossene entfernen“ — genau
       einmal, auch vor einem Ladefehler (wie die Meldung oben); close() und
       ein Reiterwechsel verwerfen es. */
    if (_boardpaarAufraeumHinweis) {
      _bodyEl.appendChild(el('p', { cls: 'admin-hint admin-aufraeum-hinweis', text: _boardpaarAufraeumHinweis, attrs: { role: 'status' } }));
      _boardpaarAufraeumHinweis = null;
    }
    if (!res.ok) { renderError(res.status); return; }
    const d = res.body || {};
    const seite = d.seite || 'aus';
    const boards = (boardsRes && boardsRes.ok && Array.isArray(boardsRes.body?.boards))
      ? boardsRes.body.boards.filter(b => b && b.id && b.id !== 'service') : [];
    const titel = new Map(boards.map(b => [b.id, b.title]));
    const boardText = id => titel.get(id) ? `${titel.get(id)} (${id})` : id;
    const neu = async () => {
      if (_activeSection !== 'boardpaar') return;
      _bodyEl.innerHTML = '';
      await renderBoardpaar();
    };

    /* Seite der Instanz */
    const row = el('div', { cls: 'admin-form-row' });
    const sel = el('select', { cls: 'admin-form-input', attrs: { id: 'admin-boardpaar-seite' } });
    for (const s of ['aus', 'innen', 'aussen']) {
      const o = el('option', { text: I18n.t('admin.boardpaar.seite-' + s) });
      o.value = s;
      if (s === seite) o.selected = true;
      sel.appendChild(o);
    }
    row.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.boardpaar.seite'), attrs: { for: 'admin-boardpaar-seite' } }));
    row.appendChild(sel);
    _bodyEl.appendChild(row);
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const flash = el('div', { cls: 'admin-flash' });
    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      const r = await AdminClient.setConfig('boardpaar.seite', sel.value, false);
      if (!r.ok) {
        /* F-2021: 409 = auf der bisherigen Seite sind noch Boards gekoppelt. */
        const text = (r.status === 400 && r.body?.error) ||
          (r.status === 409 && I18n.t('admin.boardpaar.seite-gekoppelt')) || serverFehler(r.status);
        beende(saveBtn, flash, 'err', text);
        return;
      }
      /* Dieser Browser fragt /me neu; andere sehen es nach dem Neuladen. */
      vergissBoardpaarSeite();
      await neu();
    });
    const actions = el('div', { cls: 'admin-actions' });
    actions.appendChild(saveBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.seite-hint') }));
    /* Sprint-90-Kritik F-F / Verify F5: alle Zeiten des Reiters (Abgleich,
       Kopplung, Zugänge) kommen als UTC vom Server — Hinweis einmal, immer. */
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.zeiten-utc') }));

    /* Partnerlisten innen und außen */
    for (const art of ['innen', 'aussen']) {
      const liste = Array.isArray(d[art]) ? d[art] : [];
      _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.' + art + '-title') }));
      if (seite !== art) {
        _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.warn-seite', I18n.t('admin.boardpaar.seite-' + seite)) }));
      }
      if (liste.length === 0) {
        _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.boardpaar.empty') }));
      } else {
        /* #836: außen eine Spalte „Bearbeitbar“ (nur Kopien haben den Schalter). */
        const spalten = ['admin.boardpaar.col-board', ...(art === 'aussen' ? ['admin.boardpaar.col-link'] : []),
          'admin.boardpaar.col-seit', 'admin.boardpaar.col-abgleich',
          ...(art === 'aussen' ? ['admin.boardpaar.col-bearbeitbar'] : []), 'admin.agents.col-actions'];
        const table = el('table', { cls: 'admin-audit-table' });
        const trh = el('tr');
        spalten.forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
        const thead = el('thead'); thead.appendChild(trh); table.appendChild(thead);
        const tbody = el('tbody');
        for (const e of liste) {
          const tr = el('tr');
          /* #825: Kopien (ADR-0141) von Stufe-1-Kopplungen unterscheiden. */
          const name = boardText(e.board_id) + (e.kopie ? ' — ' + I18n.t('admin.boardpaar.kopie') : '');
          [name, ...(art === 'aussen' ? [e.innen_link_basis] : []), (e.seit || '').slice(0, 10)]
            .forEach(v => tr.appendChild(el('td', { text: v })));
          tr.appendChild(abgleichZelle(art, e));
          if (art === 'aussen') tr.appendChild(bearbeitbarZelle(e, seite, boardText(e.board_id)));
          const cell = el('td');
          const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.boardpaar.btn-entkoppeln'),
            attrs: { 'aria-label': I18n.t('admin.boardpaar.aria-entkoppeln', boardText(e.board_id)) } });
          const msg = el('span', { cls: 'admin-row-msg' });
          btn.addEventListener('click', async () => {
            /* F-2229 (#831): Auf einer Kopie bleiben die Spiegel mit vollem
               Inhalt stehen — eigener Text mit Verweis auf den Notweg. */
            const schluessel = art === 'aussen' && e.kopie ? 'admin.boardpaar.confirm-aussen-kopie' : 'admin.boardpaar.confirm-' + art;
            if (!confirm(I18n.t(schluessel, boardText(e.board_id)))) {
              zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
              return;
            }
            btn.disabled = true;
            zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
            const r = await AdminClient.entkoppleBoardpaar(art, e.board_id);
            if (r.ok) { await neu(); return; }
            btn.disabled = false;
            markiereKnopf(btn, 'err');
            zeilenMeldung(msg, 'err', r.body?.error || serverFehler(r.status));
          });
          cell.appendChild(btn);
          /* #831 (F-2232): Notweg „Kopie außen löschen“ — nur außen, nur Kopien. */
          if (art === 'aussen' && e.kopie && seite === 'aussen') {
            cell.appendChild(kopieLoeschKnopf(e.board_id, titel.get(e.board_id), cell, msg, neu, e.offene_aenderungen));
          }
          cell.appendChild(msg);
          tr.appendChild(cell);
          tbody.appendChild(tr);
        }
        table.appendChild(tbody);
        _bodyEl.appendChild(table);
      }
      if (art === 'aussen' && seite === 'aussen' && liste.some(e => e.kopie)) {
        _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.bearbeitbar-hint') }));
      }
      /* #831: außen entkoppelte Kopien (Herkunft, keine Kopplung) — sonst bliebe
         nach einem Fehlgriff „entkoppeln“ kein Knopf zum Löschen. */
      const frei = Array.isArray(d.kopien_entkoppelt) ? d.kopien_entkoppelt : [];
      if (art === 'aussen' && seite === 'aussen' && frei.length > 0) {
        _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.entkoppelt-title') }));
        _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.entkoppelt-hint') }));
        const t2 = el('table', { cls: 'admin-audit-table admin-boardpaar-entkoppelt' });
        const kopf = el('tr');
        ['admin.boardpaar.col-board', 'admin.agents.col-actions'].forEach(k => kopf.appendChild(el('th', { text: I18n.t(k) })));
        const th2 = el('thead'); th2.appendChild(kopf); t2.appendChild(th2);
        const tb2 = el('tbody');
        for (const k of frei) {
          const tr = el('tr');
          tr.appendChild(el('td', { text: k.titel ? `${k.titel} (${k.board_id})` : k.board_id }));
          const cell = el('td');
          const msg = el('span', { cls: 'admin-row-msg' });
          cell.appendChild(kopieLoeschKnopf(k.board_id, k.titel, cell, msg, neu, k.offene_aenderungen));
          cell.appendChild(msg);
          tr.appendChild(cell);
          tb2.appendChild(tr);
        }
        t2.appendChild(tb2);
        _bodyEl.appendChild(t2);
      }
      /* Neues Board koppeln: Auswahl aus allen Boards dieses Servers (ohne
         Service-Board); ohne Liste ein Feld für die Board-ID. */
      const form = el('div', { cls: 'admin-agents-new' });
      const gekoppelt = new Set(liste.map(e => e.board_id));
      let wahl;
      if (boards.length > 0) {
        wahl = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-board') } });
        const leer = el('option', { text: I18n.t('admin.boardpaar.board-pick') });
        leer.value = '';
        wahl.appendChild(leer);
        boards.filter(b => !gekoppelt.has(b.id)).forEach(b => {
          const o = el('option', { text: boardText(b.id) });
          o.value = b.id;
          wahl.appendChild(o);
        });
      } else {
        wahl = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-board'), placeholder: 'Board-ID' } });
      }
      form.appendChild(wahl);
      let link = null;
      if (art === 'aussen') {
        link = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-link'),
          placeholder: I18n.t('admin.boardpaar.link-placeholder'), maxlength: '512' } });
        link.type = 'url';
        form.appendChild(link);
      }
      const btn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.boardpaar.btn-koppeln') });
      const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
      btn.addEventListener('click', async () => {
        const id = (wahl.value || '').trim();
        if (!id) { markiereKnopf(btn, 'err'); zeilenMeldung(msg, 'err', I18n.t('admin.boardpaar.board-pick')); return; }
        btn.disabled = true;
        zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
        const r = await AdminClient.koppleBoardpaar(art, id, link ? link.value.trim() : undefined);
        if (r.ok) { await neu(); return; }
        if (_activeSection !== 'boardpaar' || !btn.isConnected) return;
        btn.disabled = false;
        markiereKnopf(btn, 'err');
        zeilenMeldung(msg, 'err', (r.status === 400 && r.body?.error) || (r.status === 404 && I18n.t('admin.boardpaar.err-404')) || serverFehler(r.status));
      });
      form.appendChild(btn);
      form.appendChild(msg);
      _bodyEl.appendChild(form);
    }

    /* #825 (ADR-0141 §3): innen „Externe Kopie anlegen“, außen das Team. */
    if (seite === 'innen') renderBoardpaarKopien(d, boards, boardText, neu);
    if (seite === 'aussen') renderBoardpaarTeam(d);

    /* Zugänge abs_/abe_/abr_/abb_ (abb_: Sicherungen lesen, ADR-0142 — gilt auf jeder Seite) */
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.zugaenge-title') }));
    /* #820: Das Einmal-Token steht unter der Überschrift seines Abschnitts,
       nicht oben im Reiter — dort lag es außer Sicht (Befund aus dem Betrieb, 13.10.). */
    if (_boardpaarEinmal) {
      zeigeAgentTokenEinmal(_boardpaarEinmal, _bodyEl, 'admin.boardpaar.once-hint')
        .scrollIntoView?.({ block: 'nearest' });
      _boardpaarEinmal = null;
    }
    const zugaenge = Array.isArray(d.zugaenge) ? d.zugaenge : [];
    if (zugaenge.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.boardpaar.zugaenge-empty') }));
    } else {
      const table = el('table', { cls: 'admin-audit-table' });
      const trh = el('tr');
      ['admin.boardpaar.col-typ', 'admin.agents.col-name', 'admin.kalender.col-created', 'admin.kalender.col-used', 'admin.boardpaar.col-erneuert', 'admin.agents.col-status', 'admin.agents.col-actions']
        .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
      const thead = el('thead'); thead.appendChild(trh); table.appendChild(thead);
      const tbody = el('tbody');
      const jetzt = Date.now();
      let abgeschlossen = 0;
      for (const z of zugaenge) {
        const tr = el('tr');
        /* Karte #796: Erneuerung durch das Skript — wann und von wo; ein
           erneuertes Token gilt noch bis zum Ende seiner Übergangszeit,
           danach ist es abgelöst (Sprint-90-Kritik F-E/D-1): kein Knopf mehr,
           sein Widerruf liesse das aktuelle Token unberührt. */
        const zeit = (iso) => (iso || '').slice(0, 16).replace('T', ' ');
        const erneuert = z.erneuert_aus ? I18n.t('admin.boardpaar.erneuert-von', zeit(z.created_at), z.erneuert_von || '?') : '—';
        /* F-2324: ein unlesbares gilt_bis ist NICHT abgelöst — der Server
           wertet es beim Entfernen als „gilt noch“ (409). Die Zeile behält
           ihren Widerruf-Knopf; widerrufen lässt sie sich dann entfernen. */
        const bisMs = RFC3339.test(z.gilt_bis || '') ? Date.parse(z.gilt_bis) : NaN; /* Verify A-3: Date.parse ist toleranter als Go */
        const abgeloest = !z.revoked_at && !!z.gilt_bis && Number.isFinite(bisMs) && bisMs <= jetzt;
        const status = z.revoked_at ? I18n.t('admin.agents.status-revoked')
          : abgeloest ? I18n.t('admin.boardpaar.status-abgeloest')
          : z.gilt_bis ? I18n.t('admin.boardpaar.status-erneuert', zeit(z.gilt_bis))
          : I18n.t('admin.agents.status-active');
        const typText = z.darf_anlegen ? I18n.t('admin.boardpaar.typ-mit-recht', z.typ + '_') : z.typ + '_';
        [typText, z.name, (z.created_at || '').slice(0, 10), z.last_used_at ? zeit(z.last_used_at) : '—', erneuert, status]
          .forEach(v => tr.appendChild(el('td', { text: v })));
        const cell = el('td');
        /* #848: Abgeschlossene Zeilen (widerrufen, oder abgelöst nach der
           Übergangszeit) lassen sich entfernen — der Server prüft selbst
           und lehnt einen geltenden Zugang mit 409 ab. */
        if (z.revoked_at || abgeloest) {
          abgeschlossen++;
          const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.boardpaar.btn-entfernen'),
            attrs: { 'aria-label': I18n.t('admin.boardpaar.aria-entfernen', z.name) } });
          const msg = el('span', { cls: 'admin-row-msg' });
          btn.addEventListener('click', async () => {
            if (!confirm(I18n.t('admin.boardpaar.confirm-entfernen', z.typ + '_ ' + z.name))) {
              zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
              return;
            }
            btn.disabled = true;
            zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
            const r = await AdminClient.entferneBoardpaarZugang(z.id);
            if (r.ok || r.status === 404) { await neu(); return; }
            btn.disabled = false;
            markiereKnopf(btn, 'err');
            zeilenMeldung(msg, 'err', r.status === 409 ? I18n.t('admin.boardpaar.entfernen-gilt-noch')
              : r.status === 503 ? I18n.t('admin.boardpaar.entfernen-uhr') : serverFehler(r.status));
          });
          cell.appendChild(btn);
          cell.appendChild(msg);
        }
        if (!z.revoked_at && !abgeloest) {
          const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.agents.btn-revoke'),
            attrs: { 'aria-label': I18n.t('admin.agents.aria-revoke', z.name) } });
          const msg = el('span', { cls: 'admin-row-msg' });
          btn.addEventListener('click', async () => {
            if (!confirm(I18n.t('admin.boardpaar.confirm-revoke', z.typ + '_ ' + z.name))) {
              zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
              return;
            }
            btn.disabled = true;
            zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
            const r = await AdminClient.revokeBoardpaarZugang(z.id);
            if (r.ok) { await neu(); return; }
            btn.disabled = false;
            markiereKnopf(btn, 'err');
            zeilenMeldung(msg, 'err', serverFehler(r.status));
          });
          cell.appendChild(btn);
          cell.appendChild(msg);
        }
        tr.appendChild(cell);
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      _bodyEl.appendChild(table);
      /* #848: alle abgeschlossenen auf einmal — mit jeder wöchentlichen
         Erneuerung kommt eine abgelöste Zeile dazu. */
      if (abgeschlossen > 0) {
        const zeile = el('div', { cls: 'admin-agents-new' });
        const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.boardpaar.btn-aufraeumen', abgeschlossen) });
        const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
        btn.addEventListener('click', async () => {
          if (!confirm(I18n.t('admin.boardpaar.confirm-aufraeumen', abgeschlossen))) {
            zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
            return;
          }
          btn.disabled = true;
          zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
          const zug = _boardpaarLauf;
          const r = await AdminClient.raeumeBoardpaarZugaengeAuf();
          /* Verify A-1: Dialog zu oder Reiter gewechselt, während die Antwort
             unterwegs war → kein Merker, sonst erschiene er später ohne Bezug. */
          if (zug !== _boardpaarLauf || _activeSection !== 'boardpaar' || !btn.isConnected) return;
          if (r.ok) {
            if (r.body?.uebersprungen) {
              _boardpaarAufraeumHinweis = I18n.t('admin.boardpaar.aufgeraeumt', r.body.entfernt || 0, r.body.uebersprungen);
            }
            await neu();
            return;
          }
          /* F-2329: Ein Abbruch mitten im Aufräumen kann schon Zeilen entfernt
             haben — neu laden, damit die Tabelle den wahren Stand zeigt, und
             den Fehler über den Neuaufbau hinweg nennen. */
          _boardpaarAufraeumHinweis = serverFehler(r.status);
          await neu();
        });
        zeile.appendChild(btn);
        zeile.appendChild(msg);
        _bodyEl.appendChild(zeile);
      }
    }
    const form = el('div', { cls: 'admin-agents-new' });
    const typ = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-typ') } });
    for (const t of ['abs', 'abe', 'abr', 'abb']) {
      const o = el('option', { text: I18n.t('admin.boardpaar.typ-' + t) });
      o.value = t;
      if ((seite === 'aussen') === (t === 'abe') && t !== 'abr' && t !== 'abb') o.selected = true;
      typ.appendChild(o);
    }
    const name = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.col-name'), placeholder: 'team-schatten', maxlength: '64' } });
    /* #825: „darf Kopien anlegen“ gibt es nur für abe_ (ADR-0141 §3, VF-6). */
    const rechtLabel = el('label', { cls: 'admin-checkbox-label' });
    const recht = el('input', { attrs: { type: 'checkbox' } });
    rechtLabel.appendChild(recht);
    rechtLabel.appendChild(document.createTextNode(' ' + I18n.t('admin.boardpaar.darf-anlegen')));
    const rechtZeigen = () => { rechtLabel.hidden = typ.value !== 'abe'; if (rechtLabel.hidden) recht.checked = false; };
    typ.addEventListener('change', rechtZeigen);
    rechtZeigen();
    const btn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.boardpaar.btn-zugang') });
    const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
      const zug = _boardpaarLauf;
      const r = await AdminClient.createBoardpaarZugang(typ.value, name.value.trim(), recht.checked);
      /* Wie #748 K-4: den Klartext VOR der Abschnittsprüfung merken; ist der
         Dialog inzwischen zu, verfällt er (V-2). */
      if (r.ok && r.body?.token && zug === _boardpaarLauf) _boardpaarEinmal = r.body.token;
      if (_activeSection !== 'boardpaar' || !btn.isConnected) return;
      if (!r.ok || !r.body?.token) {
        btn.disabled = false;
        markiereKnopf(btn, 'err');
        zeilenMeldung(msg, 'err', r.body?.error || serverFehler(r.status));
        return;
      }
      await neu();
    });
    [typ, name, rechtLabel, btn, msg].forEach(x => form.appendChild(x));
    _bodyEl.appendChild(form);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.hint') }));
  }

  /* #825 (ADR-0141 §3.2): Aufträge „Externe Kopie anlegen“ — innen. Das Skript
     holt sie ab, legt die Kopie außen an und meldet „erledigt“. */
  function renderBoardpaarKopien(d, boards, boardText, neu) {
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.kopien-title') }));
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.kopien-hint') }));
    const auftraege = Array.isArray(d.auftraege) ? d.auftraege : [];
    if (auftraege.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.boardpaar.auftraege-empty') }));
    } else {
      const table = el('table', { cls: 'admin-audit-table' });
      const trh = el('tr');
      ['admin.boardpaar.col-board', 'admin.boardpaar.ziel', 'admin.boardpaar.col-status', 'admin.agents.col-actions']
        .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
      const thead = el('thead'); thead.appendChild(trh); table.appendChild(thead);
      const tbody = el('tbody');
      const zeit = (iso) => (iso || '').slice(0, 16).replace('T', ' ');
      for (const a of auftraege) {
        const tr = el('tr');
        const status = a.status === 'erledigt' ? I18n.t('admin.boardpaar.auftrag-erledigt', zeit(a.erledigt_at))
          : I18n.t('admin.boardpaar.auftrag-offen', zeit(a.angelegt_at));
        [a.titel ? `${a.titel} (${a.board_id})` : boardText(a.board_id), a.ziel, status]
          .forEach(v => tr.appendChild(el('td', { text: v })));
        const cell = el('td');
        if (a.status === 'offen') {
          const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.boardpaar.btn-zurueckziehen') });
          const msg = el('span', { cls: 'admin-row-msg' });
          btn.addEventListener('click', async () => {
            btn.disabled = true;
            zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
            const r = await AdminClient.zieheBoardpaarAuftragZurueck(a.id);
            if (r.ok) { await neu(); return; }
            btn.disabled = false;
            markiereKnopf(btn, 'err');
            zeilenMeldung(msg, 'err', r.body?.error || serverFehler(r.status));
          });
          cell.appendChild(btn);
          cell.appendChild(msg);
        }
        tr.appendChild(cell);
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      _bodyEl.appendChild(table);
    }
    const form = el('div', { cls: 'admin-agents-new' });
    let wahl;
    if (boards.length > 0) {
      wahl = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-board') } });
      const leer = el('option', { text: I18n.t('admin.boardpaar.board-pick') });
      leer.value = '';
      wahl.appendChild(leer);
      boards.forEach(b => { const o = el('option', { text: boardText(b.id) }); o.value = b.id; wahl.appendChild(o); });
    } else {
      wahl = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.col-board'), placeholder: 'Board-ID' } });
    }
    form.appendChild(wahl);
    const ziele = Array.isArray(d.ziele) ? d.ziele : [];
    let ziel;
    if (ziele.length > 0) {
      ziel = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.ziel') } });
      ziele.forEach(z => { const o = el('option', { text: z.name }); o.value = z.name; ziel.appendChild(o); });
    } else {
      ziel = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.ziel'), placeholder: 'team-extern', maxlength: '64' } });
    }
    form.appendChild(ziel);
    let link = null;
    if (!d.link_basis_vorgabe) {
      link = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.boardpaar.link-pflicht'),
        placeholder: I18n.t('admin.boardpaar.link-placeholder'), maxlength: '512' } });
      link.type = 'url';
      form.appendChild(link);
    }
    const btn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.boardpaar.btn-kopie') });
    const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
    btn.addEventListener('click', async () => {
      const id = (wahl.value || '').trim();
      if (!id) { markiereKnopf(btn, 'err'); zeilenMeldung(msg, 'err', I18n.t('admin.boardpaar.board-pick')); return; }
      btn.disabled = true;
      zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
      const r = await AdminClient.createBoardpaarAuftrag(id, (ziel.value || '').trim(), link ? link.value.trim() : '');
      if (r.ok) { await neu(); return; }
      if (_activeSection !== 'boardpaar' || !btn.isConnected) return;
      btn.disabled = false;
      markiereKnopf(btn, 'err');
      zeilenMeldung(msg, 'err', (r.status === 409 && I18n.t('admin.boardpaar.err-auftrag-offen')) ||
        (r.status === 404 && I18n.t('admin.boardpaar.err-404')) || r.body?.error || serverFehler(r.status));
    });
    form.appendChild(btn);
    form.appendChild(msg);
    _bodyEl.appendChild(form);
    if (ziele.length === 0) _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.ziele-keine') }));
  }

  /* #825 (ADR-0141 §3.4, W-C): das feste Board-Paar-Team — außen. */
  function renderBoardpaarTeam(d) {
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.team-title') }));
    const row = el('div', { cls: 'admin-form-row' });
    const feld = el('input', { cls: 'admin-form-input', attrs: { id: 'admin-boardpaar-team', placeholder: 'person@firma.example, @firma.example' } });
    feld.value = d.team || '';
    row.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.boardpaar.team-label'), attrs: { for: 'admin-boardpaar-team' } }));
    row.appendChild(feld);
    _bodyEl.appendChild(row);
    const btn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const flash = el('div', { cls: 'admin-flash' });
    /* F-2268: Wer aus dem Team fällt, verliert sofort Leser-/Bearbeiter-Rechte
       auf allen Kopien. Der erste Aufruf liefert dann nur die Vorschau (409);
       gespeichert wird erst nach Bestätigung. `bisher` ist der gelesene Wert —
       hat ihn inzwischen jemand geändert, lehnt der Server ab. */
    let bisher = d.team || '';
    btn.addEventListener('click', async () => {
      starte(btn, flash);
      let r = await AdminClient.setBoardpaarTeam(feld.value, bisher, false);
      /* Sitzungs-QA #835 K-2: Reiter inzwischen gewechselt → keine Rückfrage
         über einem fremden Reiter, nichts speichern. */
      if (_activeSection !== 'boardpaar' || !btn.isConnected) return;
      if (r.status === 409 && r.body?.bestaetigung_noetig) {
        const weg = (r.body.weg || []).join(', ');
        if (!confirm(I18n.t('admin.boardpaar.team-confirm-entzug', weg, r.body.zeilen))) {
          beende(btn, flash, 'err', I18n.t('admin.boardpaar.team-abgebrochen'));
          return;
        }
        r = await AdminClient.setBoardpaarTeam(feld.value, bisher, true);
      }
      if (!btn.isConnected) return;
      if (r.status === 409 && r.body?.veraltet) { beende(btn, flash, 'err', I18n.t('admin.boardpaar.team-veraltet')); return; }
      if (!r.ok) { beende(btn, flash, 'err', ((r.status === 400 || r.status === 409) && r.body?.error) || serverFehler(r.status)); return; }
      bisher = r.body?.team ?? feld.value;
      feld.value = bisher;
      beende(btn, flash, 'ok', r.body?.zeilen > 0
        ? I18n.t('admin.boardpaar.team-gespeichert-entzogen', r.body.zeilen) : I18n.t('admin.msg-saved'));
    });
    const actions = el('div', { cls: 'admin-actions' });
    actions.appendChild(btn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.team-hint') }));
    renderBoardpaarRueckweg(d);
  }

  /* Karte #827 E4 (ADR-0141 §7, Plan D1/D9): Rückweg außen — ein Schalter.
     An: Spiegelkarten sind außen bearbeitbar, Änderungen gehen nach innen,
     das Team wird auf allen Kopien Bearbeiter. Aus: nur lesen (Stufe 2b),
     das Team wird wieder Leser. Der Server gleicht die Rollen in derselben
     Transaktion an. */
  function renderBoardpaarRueckweg(d) {
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.boardpaar.rueckweg-title') }));
    const row = el('div', { cls: 'admin-form-row' });
    const cb = el('input', { attrs: { type: 'checkbox', id: 'admin-boardpaar-rueckweg' } });
    cb.checked = d.rueckweg === 'an';
    row.appendChild(cb);
    row.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.boardpaar.rueckweg-label'), attrs: { for: 'admin-boardpaar-rueckweg' } }));
    _bodyEl.appendChild(row);
    const flash = el('div', { cls: 'admin-flash' });
    cb.addEventListener('change', async () => {
      const an = cb.checked;
      starte(cb, flash);
      const r = await AdminClient.setConfig('boardpaar.rueckweg', an ? 'an' : 'aus', false);
      if (!cb.isConnected) return;
      if (!r.ok) {
        cb.checked = !an;
        beende(cb, flash, 'err', (r.status === 400 && r.body?.error) || serverFehler(r.status));
        return;
      }
      beende(cb, flash, 'ok', I18n.t('admin.msg-saved'));
      /* Dieser Browser zeichnet gleich um (boardpaar:rueckweg); andere nach dem Neuladen. */
      vergissBoardpaarSeite();
      boardpaarSeite();
    });
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.boardpaar.rueckweg-hint') }));
  }

  /* #295/ADR-0093: IMAP-Sektion — Polling-Worker-Konfig + Test-Button.
     Felder analog SMTP-Pattern (host/port/user/pass/tls + Folder-Trio +
     Poll-Intervall + Größenlimits). pass ist secret, der Rest plain. */
  const IMAP_FIELDS = [
    { key: 'imap.enabled',                   labelKey: 'admin.imap.enabled',                  secret: false, placeholder: 'true' },
    { key: 'imap.host',                      labelKey: 'admin.imap.host',                     secret: false, placeholder: 'imap.example.org' },
    { key: 'imap.port',                      labelKey: 'admin.imap.port',                     secret: false, placeholder: '993' },
    { key: 'imap.user',                      labelKey: 'admin.imap.user',                     secret: false, placeholder: 'ingest@aboard.example.org' },
    { key: 'imap.pass',                      labelKey: 'admin.imap.pass',                     secret: true,  placeholder: '••••••••' },
    { key: 'imap.tls',                       labelKey: 'admin.imap.tls',                      secret: false, placeholder: 'true' },
    { key: 'imap.folder',                    labelKey: 'admin.imap.folder',                   secret: false, placeholder: 'INBOX' },
    { key: 'imap.processed_folder',          labelKey: 'admin.imap.processed_folder',         secret: false, placeholder: 'Processed' },
    { key: 'imap.error_folder',              labelKey: 'admin.imap.error_folder',             secret: false, placeholder: 'Errors' },
    { key: 'imap.polling_interval_sec',      labelKey: 'admin.imap.polling_interval_sec',     secret: false, placeholder: '600' },
    { key: 'imap.max_mail_size_mb',          labelKey: 'admin.imap.max_mail_size_mb',         secret: false, placeholder: '25' },
    { key: 'imap.max_attachment_size_mb',    labelKey: 'admin.imap.max_attachment_size_mb',   secret: false, placeholder: '10' },
    /* F-370: Server-Seite existiert seit Karte #451 (IMAPConfig.RateLimitPerHour,
       BootstrapIMAPEnv, main.go) — hier nur die fehlende Admin-UI. `0` schaltet
       das Limit aus, negative/unparsbare Werte fallen server-seitig auf den
       Default zurück; das steht im Label, nicht nur im Kommentar hier, weil
       die UI der einzige Ort ist, an dem ein Admin es liest. */
    { key: 'imap.rate_limit_per_hour',       labelKey: 'admin.imap.rate_limit_per_hour',      secret: false, placeholder: '20' },
  ];

  /* F-1856 (Karte #760): Der IMAP-Arbeitsprozess liest SMTP-, Service-Board- und
     Auto-Reply-Konfiguration nur beim Start. `POST /admin/config/reload?section=imap`
     führt NUR den Hook „imap“ aus (O-3: ein Reload aller Hooks bräche u. a. einen
     laufenden Board-Backup-Lauf ab) und startet den Worker neu. Gemeldet wird das
     Ergebnis der Sektion „imap“, nicht HTTP 200 (Muster F-1462), in drei Zuständen
     (O-5): „ok“ (läuft neu), „disabled“ (Mail-Eingang aus — nichts zu starten),
     sonst Fehler mit dem Text des Servers (z. B. ungültige Config). */
  async function speichernMeldung(saveBtn, flash, vorabSektion) {
    /* V-5: Der Service-Board-Reiter braucht zuerst den Re-Seed seiner eigenen
       Sektion (Hook „service-board“), danach den Mail-Worker-Neustart. */
    if (vorabSektion) {
      let rv;
      try { rv = await AdminClient.reload(vorabSektion); } catch { rv = null; }
      const ev = rv && rv.ok && rv.body && rv.body.results ? rv.body.results[vorabSektion] : undefined;
      if (ev !== 'ok') {
        return beende(saveBtn, flash, 'err', typeof ev === 'string' && ev.startsWith('error: ')
          ? I18n.t('admin.mail.msg-reload-error', ev.slice('error: '.length))
          : I18n.t('admin.mail.msg-reload-fail'));
      }
    }
    let rl;
    try { rl = await AdminClient.reload('imap'); } catch { rl = null; }
    const erg = rl && rl.ok && rl.body && rl.body.results ? rl.body.results.imap : undefined;
    if (erg === 'ok') return beende(saveBtn, flash, 'ok', I18n.t('admin.mail.msg-saved-reloaded'));
    if (erg === 'disabled') return beende(saveBtn, flash, 'ok', I18n.t('admin.mail.msg-saved-off'));
    if (typeof erg === 'string' && erg.startsWith('error: ')) {
      return beende(saveBtn, flash, 'err', I18n.t('admin.mail.msg-reload-error', erg.slice('error: '.length)));
    }
    return beende(saveBtn, flash, 'err', I18n.t('admin.mail.msg-reload-fail'));
  }

  async function renderIMAP() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('imap');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const map = {};
    (Array.isArray(res.body?.entries) ? res.body.entries : []).forEach(e => { map[e.key] = e; });

    const form = el('div', { cls: 'admin-smtp-form' });
    IMAP_FIELDS.forEach(f => {
      const row = el('div', { cls: 'admin-form-row' });
      const label = el('label', { cls: 'admin-form-label', text: I18n.t(f.labelKey) });
      const input = el('input', { cls: 'admin-form-input' });
      input.type = f.secret ? 'password' : 'text';
      input.placeholder = f.placeholder;
      const existing = map[f.key];
      if (existing && !f.secret) input.value = existing.value || '';
      if (f.secret && existing && existing.is_set) input.placeholder = '••••• (gespeichert; leer = unverändert)';
      input.dataset.key = f.key;
      input.dataset.secret = String(f.secret);
      row.appendChild(label);
      row.appendChild(input);
      form.appendChild(row);
    });
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.btn-test-section') });
    const flash = el('div', { cls: 'admin-flash' });

    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      let failed = false;
      let fehlerStatus = 0;
      for (const i of form.querySelectorAll('input.admin-form-input')) {
        const secret = i.dataset.secret === 'true';
        const val = i.value;
        if (secret && val === '') continue; /* leer = unverändert */
        const r = await AdminClient.setConfig(i.dataset.key, val, secret);
        if (!r.ok) { failed = true; fehlerStatus = r.status; break; }
      }
      if (failed) { beende(saveBtn, flash, 'err', serverFehler(fehlerStatus)); return; }
      await speichernMeldung(saveBtn, flash);
    });

    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      const r = await AdminClient.testSection('imap');
      if (r.ok) beende(testBtn, flash, 'ok', I18n.t('admin.imap.msg-test-ok'));
      else beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.imap.msg-test-fail'));
    });

    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.imap.hint') }));
  }

  /* #297: Mail-Auto-Reply-Sektion.
     Felder: Enabled-Toggle, Template-Textarea, From, Reply-To.
     Test-Button rendert das Template mit Demo-Daten (kein realer Send). */
  const AUTOREPLY_FIELDS = [
    { key: 'mail_auto_reply.enabled',   labelKey: 'admin.mail_auto_reply.enabled.label',   secret: false, placeholder: 'false', type: 'text' },
    { key: 'mail_auto_reply.from',      labelKey: 'admin.mail_auto_reply.from.label',       secret: false, placeholder: '', type: 'text' },
    { key: 'mail_auto_reply.reply_to',  labelKey: 'admin.mail_auto_reply.reply_to.label',   secret: false, placeholder: '', type: 'text' },
  ];

  async function renderAutoReply() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('mail_auto_reply');
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const map = {};
    (Array.isArray(res.body?.entries) ? res.body.entries : []).forEach(e => { map[e.key] = e; });

    const title = el('h3', { cls: 'admin-section-title', text: I18n.t('admin.mail_auto_reply.title') });
    _bodyEl.appendChild(title);

    const form = el('div', { cls: 'admin-smtp-form' });
    AUTOREPLY_FIELDS.forEach(f => {
      const row = el('div', { cls: 'admin-form-row' });
      const label = el('label', { cls: 'admin-form-label', text: I18n.t(f.labelKey) });
      const input = el('input', { cls: 'admin-form-input' });
      input.type = 'text';
      input.placeholder = f.placeholder;
      const existing = map[f.key];
      if (existing) input.value = existing.value || '';
      input.dataset.key = f.key;
      row.appendChild(label);
      row.appendChild(input);
      form.appendChild(row);
    });

    /* Template-Textarea — separate Zeile (volle Breite) */
    const tmplLabel = el('label', { cls: 'admin-form-label', text: I18n.t('admin.mail_auto_reply.template.label') });
    const tmplArea = el('textarea', { cls: 'admin-form-input admin-form-textarea',
      attrs: { rows: '10', placeholder: I18n.t('admin.mail_auto_reply.template.placeholder'),
                'data-key': 'mail_auto_reply.template' } });
    const tmplExisting = map['mail_auto_reply.template'];
    if (tmplExisting) tmplArea.value = tmplExisting.value || '';
    const tmplRow = el('div', { cls: 'admin-form-row' });
    tmplRow.appendChild(tmplLabel);
    tmplRow.appendChild(tmplArea);
    form.appendChild(tmplRow);
    _bodyEl.appendChild(form);

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const testBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.mail_auto_reply.test.label') });
    const flash = el('div', { cls: 'admin-flash' });
    const preview = el('pre', { cls: 'admin-autoreply-preview', attrs: { style: 'display:none' } });

    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      let failed = false;
      let fehlerStatus = 0;
      for (const i of form.querySelectorAll('[data-key]')) {
        const key = i.dataset.key;
        const val = i.tagName === 'TEXTAREA' ? i.value : i.value;
        const r = await AdminClient.setConfig(key, val, false);
        if (!r.ok) { failed = true; fehlerStatus = r.status; break; }
      }
      if (failed) { beende(saveBtn, flash, 'err', serverFehler(fehlerStatus)); return; }
      await speichernMeldung(saveBtn, flash);
    });

    testBtn.addEventListener('click', async () => {
      starte(testBtn, flash);
      preview.style.display = 'none';
      const r = await AdminClient.testSection('mail_auto_reply');
      if (r.ok) {
        beende(testBtn, flash, 'ok', I18n.t('admin.mail_auto_reply.msg-test-ok'));
        /* Karte #333: Der Test-Endpoint liefert das Hook-Ergebnis unter
           `result` — bewusst als eigener Namensraum und nicht flach neben
           `ok`/`error`, damit ein Hook die Statusaussage der Antwort nicht
           überschreiben kann. Bis #333 gab der Endpoint nur `{ok:true}` zurück
           und diese Vorschau blieb immer leer (AK13 aus #297, halbumgesetzt). */
        const ergebnis = r.body && r.body.result;
        if (ergebnis && (ergebnis.rendered_subject || ergebnis.rendered_body)) {
          preview.textContent = `Subject: ${ergebnis.rendered_subject || ''}\n\n${ergebnis.rendered_body || ''}`;
          preview.style.display = 'block';
        }
      } else {
        beende(testBtn, flash, 'err', (r.body && r.body.error) || I18n.t('admin.mail_auto_reply.msg-test-fail'));
      }
    });

    actions.appendChild(saveBtn);
    actions.appendChild(testBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(preview);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.mail_auto_reply.help') }));
  }

  /* #303/ADR-0098: Board-Backup-Sektion. Admin (oder Owner via Server-
     seitiger ACL) gibt eine Board-ID an, sieht die Backup-Liste, kann manuelles
     Backup auslösen, Files herunterladen/löschen, und Backup hochladen +
     wiederherstellen. */
  /* Karte #626 (F-1406): Schalter für `backup_board.encryption_enabled`.
     Vorher gab es ihn nur per Hand-`PUT /admin/config/…`; wer ihn vergaß,
     bekam still Klartext-Backups (bei manchen Betreibern ist Verschlüsselung Pflicht).

     „Schlüssel gesetzt ja/nein“ kommt aus `secrets_enabled` der
     /admin/config-Antwort — das ist genau „ABOARD_SECRET_KEY gesetzt UND
     gültig“ (dieselbe Prüfung wie bei der Backup-Verschlüsselung:
     secretcrypto.NewAEAD). Der Schlüssel selbst verlässt den Server nie, die
     Antwort trägt nur den Wahrheitswert.

     Ohne Schlüssel lässt sich der Schalter nicht EINschalten: Der Server
     liesse den Scheduler dann aus (main.go: ResolveCrypter-Fehler → „keine
     Sicherungen mehr“) — ein Klick, der die Backups abschaltet, wäre der
     schlimmere Fehler als ein fehlender Schalter. Ausschalten bleibt immer
     möglich. Nach dem Schreiben läuft der Reload, damit der Scheduler den
     neuen Stand sofort liest und nicht erst beim nächsten Neustart. */
  let _backupLauf = 0;
  async function fuelleVerschluesselung(host, lauf) {
    host.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listConfig('backup_board');
    if (lauf !== _backupLauf || _activeSection !== 'backup' || !host.isConnected) return;
    host.innerHTML = '';
    if (!res.ok) {
      host.appendChild(el('p', { cls: 'admin-error', attrs: { role: 'alert' }, text: I18n.t('admin.backup.enc.unreadable') }));
      return;
    }
    const eintraege = Array.isArray(res.body?.entries) ? res.body.entries : [];
    const eintrag = eintraege.find(e => e.key === 'backup_board.encryption_enabled');
    let an = !!eintrag && (eintrag.value === 'true');
    const schluessel = !!(res.body && res.body.secrets_enabled);

    const box = el('div', { cls: 'admin-enc-box' });
    const label = el('label', { cls: 'admin-enc-switch' });
    const chk = el('input', { attrs: { type: 'checkbox' } });
    label.appendChild(chk);
    label.appendChild(document.createTextNode(I18n.t('admin.backup.enc.label')));
    box.appendChild(label);
    const keyZeile = el('p', {
      cls: 'admin-enc-key' + (schluessel ? '' : ' admin-enc-key--missing'),
      text: I18n.t(schluessel ? 'admin.backup.enc.key-yes' : 'admin.backup.enc.key-no'),
    });
    box.appendChild(keyZeile);
    const warn = el('p', { cls: 'admin-enc-warn', attrs: { role: 'status' } });
    box.appendChild(warn);
    const flash = el('div', { cls: 'admin-flash' });
    box.appendChild(flash);

    function zeige() {
      chk.checked = an;
      /* Ohne Schlüssel nicht einschaltbar; ein bereits gesetztes „an“ bleibt
         ausschaltbar (sonst gäbe es keinen Rückweg aus dem Fehlzustand). */
      chk.disabled = !schluessel && !an;
      box.classList.toggle('admin-enc-box--warn', !an || !schluessel);
      if (!an) warn.textContent = I18n.t(schluessel ? 'admin.backup.enc.warn-off' : 'admin.backup.enc.warn-off-nokey');
      else if (!schluessel) warn.textContent = I18n.t('admin.backup.enc.warn-on-nokey');
      else warn.textContent = '';
    }
    zeige();

    chk.addEventListener('change', async () => {
      const soll = chk.checked;
      chk.disabled = true;
      meldung(flash, 'busy', I18n.t('admin.working'));
      const r = await AdminClient.setConfig('backup_board.encryption_enabled', soll ? 'true' : 'false', false);
      if (!r.ok) {
        an = !soll;
        zeige();
        meldung(flash, 'err', serverFehler(r.status));
        return;
      }
      an = soll;
      /* F-1462: Gewertet wird NUR die Sektion `backup_board`: HTTP 200 allein
         zählt nicht, ihr Ergebnis steht in `results.backup_board` („error: …“).
         V-4: Der Reload läuft gezielt für diese Sektion (`?section=backup_board`)
         und lädt keine fremden Bereiche (Mail-Worker, Service-Board, Wissen) mit.
         Fehlt das Feld (älterer Server), gilt allein der HTTP-Status. */
      const rl = await AdminClient.reload('backup_board');
      const bb = rl && rl.body && rl.body.results ? rl.body.results.backup_board : undefined;
      const bbOk = bb === undefined || bb === 'ok';
      zeige();
      if (rl && rl.ok && bbOk) meldung(flash, 'ok', I18n.t(soll ? 'admin.backup.enc.msg-on' : 'admin.backup.enc.msg-off'));
      else meldung(flash, 'err', I18n.t('admin.backup.enc.msg-reload-fail'));
    });
    host.appendChild(box);
  }

  async function renderBackup() {
    const lauf = ++_backupLauf;
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.backup.intro') }));
    const encHost = el('div', { cls: 'admin-enc-host' });
    _bodyEl.appendChild(encHost);
    fuelleVerschluesselung(encHost, lauf);

    /* Board-ID-Picker */
    const pickerRow = el('div', { cls: 'admin-form-row' });
    pickerRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.backup.board-id'), attrs: { for: 'admin-backup-board-id' } }));
    const boardInput = el('input', { cls: 'admin-form-input', attrs: { id: 'admin-backup-board-id' } });
    boardInput.type = 'text';
    boardInput.placeholder = I18n.t('admin.backup.board-id-placeholder');
    pickerRow.appendChild(boardInput);
    _bodyEl.appendChild(pickerRow);
    /* Karte #735: Was passiert ohne Auswahl? Die tägliche Sicherung erfasst
       alle Boards; die Auswahl dient der Einzelansicht/-sicherung. */
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.backup.board-hint') }));

    const actions = el('div', { cls: 'admin-actions' });
    const loadBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.backup.btn-load') });
    const nowBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.backup.btn-now') });
    actions.appendChild(loadBtn);
    actions.appendChild(nowBtn);
    _bodyEl.appendChild(actions);

    const flash = el('div', { cls: 'admin-flash' });
    _bodyEl.appendChild(flash);

    const listHost = el('div');
    _bodyEl.appendChild(listHost);

    /* Karte #735: Beschriftung des Sichern-Knopfs folgt dem Feld live —
       leer = „Alle Boards jetzt sichern“, sonst „Jetzt sichern“. */
    let alleLaeuft = false;
    function beschrifteJetzt() {
      nowBtn.textContent = I18n.t(boardInput.value.trim() ? 'admin.backup.btn-now' : 'admin.backup.btn-now-all');
    }
    boardInput.addEventListener('input', beschrifteJetzt);
    beschrifteJetzt();

    /* Karte #627: Pflichtfeld leer → sichtbarer Hinweis statt stillem
       Abbruch (ein Nutzer hielt „Jetzt sichern“ ohne Board-ID für erledigt). */
    function boardIdFehlt(btn) {
      boardInput.setAttribute('aria-invalid', 'true');
      meldung(flash, 'err', I18n.t('admin.backup.err-board-id'));
      markiereKnopf(btn, 'err');
      boardInput.focus();
    }
    boardInput.addEventListener('input', () => boardInput.removeAttribute('aria-invalid'));

    /* #624: Auswahl statt Freitext — Quelle ist GET /admin/boards (alle
       Boards, F-1439), Rückfall GET /boards (ACL-gefiltert; dort fehlen
       Boards ohne ACL-Eintrag wie das Seed-Board `default`, sie bleiben
       über „Andere ID…“ bzw. das Freitextfeld erreichbar). Das Freitextfeld
       bleibt die einzige Wahrheit; die Auswahl setzt nur seinen Wert. */
    const ANDERE = '__andere__';
    (async () => {
      let boards = [];
      let unvollstaendig = false;
      /* F-1439: Admin-Endpunkt mit ALLEN Boards (auch ohne ACL); Rückfall auf
         die ACL-gefilterte Liste, wenn er fehlt (404, älterer Server) oder
         scheitert. */
      try {
        const r = await AdminClient.listAllBoards?.();
        if (r && r.ok && Array.isArray(r.body?.boards)) {
          boards = r.body.boards.map(b => ({ id: b.id, name: b.title, owners: b.owners || [] }));
          unvollstaendig = !!r.body.unvollstaendig;
        }
      } catch { boards = []; }
      if (boards.length === 0) {
        try { boards = await StoragePlugin.getActivePlugin().listKnownBoards?.() || []; }
        catch { boards = []; }
      }
      if (lauf !== _backupLauf || !Array.isArray(boards) || boards.length === 0) return;
      /* F-1463: sichtbares Label per for/id am select (statt doppeltem aria-label). */
      const sel = el('select', { cls: 'admin-form-input', attrs: { id: 'admin-backup-board-pick' } });
      const leer = el('option', { text: I18n.t('admin.backup.board-pick') });
      leer.value = '';
      sel.appendChild(leer);
      const ids = new Set();
      boards.forEach(b => {
        ids.add(b.id);
        const kurz = Rueckmeldung.kurzeId(b.id);
        const o = el('option', { text: `${b.name || b.title || b.id} (${kurz})` });
        o.value = b.id;
        sel.appendChild(o);
      });
      if (unvollstaendig) {
        /* V-4: nicht still abschneiden — der Rest bleibt über „Andere ID…“ erreichbar. */
        const hinweis = el('option', { text: I18n.t('admin.backup.board-list-incomplete') });
        hinweis.value = '';
        hinweis.disabled = true;
        sel.appendChild(hinweis);
      }
      const andere = el('option', { text: I18n.t('admin.backup.board-other') });
      andere.value = ANDERE;
      sel.appendChild(andere);
      sel.addEventListener('change', () => {
        boardInput.removeAttribute('aria-invalid');
        if (sel.value === ANDERE) { boardInput.value = ''; boardInput.focus(); }
        else boardInput.value = sel.value;
        beschrifteJetzt();
      });
      boardInput.addEventListener('input', () => {
        const v = boardInput.value.trim();
        sel.value = ids.has(v) ? v : (v ? ANDERE : '');
      });
      const selRow = el('div', { cls: 'admin-form-row' });
      selRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.backup.board-pick'), attrs: { for: 'admin-backup-board-pick' } }));
      selRow.appendChild(sel);
      pickerRow.parentNode?.insertBefore(selRow, pickerRow);
    })();

    async function refreshList() {
      const id = boardInput.value.trim();
      if (!id) { listHost.innerHTML = ''; return { ok: true }; }
      listHost.innerHTML = '';
      listHost.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
      const r = await holen(`/boards/${encodeURIComponent(id)}/backup/files`);
      listHost.innerHTML = '';
      /* F-1450: Ladefehler an den Aufrufer melden — vorher rief dieser Zweig
         renderError (Text am Dialogende) und `loadBtn` meldete trotzdem
         „Liste geladen“ in Grün. Der Text kommt wie bei Sichern/Löschen aus
         backupFehlerAus (404, 503 mit Grund aus der Antwort), und steht im
         Listenbereich, nicht am Ende des Dialogs. */
      if (!r.ok) {
        const text = await backupFehlerAus(r);
        listHost.appendChild(el('p', { cls: 'admin-error', text, attrs: { role: 'alert' } }));
        return { ok: false, text };
      }
      const body = await r.json().catch(() => ({}));
      const files = (body && body.files) || [];
      if (files.length === 0) {
        listHost.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.backup.empty') }));
        return { ok: true };
      }
      const table = el('table', { cls: 'admin-audit-table' });
      const thead = el('thead');
      const trh = el('tr');
      ['admin.backup.col-file','admin.backup.col-size','admin.backup.col-mtime','admin.backup.col-actions']
        .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
      thead.appendChild(trh);
      table.appendChild(thead);
      const tbody = el('tbody');
      files.forEach(f => {
        const tr = el('tr');
        /* #336/Sprint-58-Kritiker: Diese Liste und die Eigentümer-Sektion in
           `backup-section.js` lesen dieselbe REST-Antwort. Bis der Kritiker es
           gemessen hat, zeigte nur die neue Sektion den Verschlüsselungs-
           Hinweis und lesbare Grössen — ein Server-Admin sah hier rohe Bytes
           und keinen Hinweis darauf, dass ein Archiv gar nicht mit `tar`
           zu öffnen ist. Beide Helfer kommen jetzt aus einer Quelle. */
        const nameZelle = el('td');
        nameZelle.appendChild(el('span', { text: f.filename }));
        if (f.encrypted) {
          nameZelle.appendChild(document.createTextNode(' '));
          nameZelle.appendChild(verschluesseltBadge());
        }
        tr.appendChild(nameZelle);
        tr.appendChild(el('td', { text: formatGroesse(f.size) }));
        tr.appendChild(el('td', { text: f.mtime }));
        const actCell = el('td');
        const dl = el('a', { cls: 'btn admin-btn', text: I18n.t('admin.backup.btn-download') });
        dl.href = `/boards/${encodeURIComponent(id)}/backup/download/${encodeURIComponent(f.filename)}`;
        dl.setAttribute('download', f.filename);
        actCell.appendChild(dl);
        const del = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.backup.btn-delete') });
        del.addEventListener('click', async () => {
          if (!confirm(I18n.t('admin.backup.confirm-delete'))) return;
          starte(del, flash);
          let rr;
          try {
            rr = await holen(`/boards/${encodeURIComponent(id)}/backup/files/${encodeURIComponent(f.filename)}`,
              { method: 'DELETE' });
          } catch { rr = { ok: false, status: 0 }; }
          del.disabled = false;
          del.removeAttribute('aria-busy');
          if (rr.ok) { meldung(flash, 'ok', I18n.t('admin.backup.msg-deleted')); markiereKnopf(del, 'ok'); refreshList(); }
          else { meldung(flash, 'err', await backupFehlerAus(rr)); markiereKnopf(del, 'err'); }
        });
        actCell.appendChild(del);
        tr.appendChild(actCell);
        tbody.appendChild(tr);
      });
      table.appendChild(tbody);
      listHost.appendChild(table);
      return { ok: true };
    }

    loadBtn.addEventListener('click', async () => {
      if (!boardInput.value.trim()) { boardIdFehlt(loadBtn); return; }
      starte(loadBtn, flash);
      try {
        const erg = await refreshList();
        if (erg && erg.ok === false) beende(loadBtn, flash, 'err', erg.text);
        else beende(loadBtn, flash, 'ok', I18n.t('admin.backup.msg-loaded'));
      } catch {
        beende(loadBtn, flash, 'err', I18n.t('admin.error-server'));
      }
    });
    /* Karte #735: Lauf über alle Boards. Der Server antwortet asynchron (202),
       weil ein Lauf das 30-s-Antwortlimit überschreiten kann; hier wird der
       Status abgefragt, bis er fertig ist. Der Knopf bleibt bis dahin gesperrt
       (kein Doppelklick); ein paralleler Scheduler-/Admin-Lauf wird serverseitig
       mit 409 abgewiesen. */
    async function sichereAlle() {
      if (alleLaeuft) return;
      alleLaeuft = true;
      starte(nowBtn, flash);
      const fertig = (art, text) => {
        alleLaeuft = false;
        beende(nowBtn, flash, art, text);
      };
      /* K-1 (Sprint 85): Abbruch bei Reiterwechsel, Schließen oder ausgehängtem
         Knopf — nach JEDEM await, auch nach dem Start (Verify Sprint 85,
         F-1826), sonst schreiben 409-/Fehlerzweig in ein ausgehängtes DOM. */
      const abgebrochen = () => lauf !== _backupLauf || _activeSection !== 'backup' || !nowBtn.isConnected;
      let r;
      try { r = await AdminClient.startAllBackup(); } catch { r = { ok: false, status: 0 }; }
      if (abgebrochen()) { alleLaeuft = false; return; }
      if (r.status === 409) { fertig('err', I18n.t('admin.backup.all-running')); return; }
      if (!r.ok) {
        const grund = r.status === 503 && r.body && typeof r.body.error === 'string' ? r.body.error : '';
        fertig('err', r.status === 503 ? backupFehler(503, grund) : serverFehler(r.status));
        return;
      }
      meldung(flash, 'busy', I18n.t('admin.backup.all-busy'));
      await pollenAlle(abgebrochen, fertig);
    }
    /* F-1829: Status abfragen, bis der Lauf fertig ist. Gemeinsam für den
       eigenen Start und die Wiederaufnahme nach Reiterwechsel. Läuft je Aufbau
       der Sektion höchstens einmal (alleLaeuft) und bricht ab, sobald `lauf`
       veraltet ist — ein neuer Aufbau übernimmt dann selbst. */
    async function pollenAlle(abgebrochen, fertig, wiederaufnahme = false) {
      for (let i = 0; i < 2400; i++) {
        /* K-1: vor UND nach jedem await, sonst pollt der Browser bis zu ~1 h
           weiter und schreibt in ein ausgehängtes DOM. */
        if (abgebrochen()) { alleLaeuft = false; return; }
        let st;
        try { st = await AdminClient.allBackupStatus(); } catch { st = { ok: false }; }
        if (abgebrochen()) { alleLaeuft = false; return; }
        /* O-9: Anmeldung/Berechtigung weg → Ende statt bis zu 1 h „läuft“. */
        if (st && (st.status === 401 || st.status === 403)) { fertig('err', serverFehler(st.status)); return; }
        /* O-9: Wiederaufnahme, aber der Server kennt keinen Lauf mehr
           (`running:false` ohne `last`, z. B. nach Neustart) → Ende. */
        if (wiederaufnahme && st && st.ok && st.body && st.body.running === false && !st.body.last) {
          fertig('err', I18n.t('admin.backup.all-gone'));
          return;
        }
        if (st && st.ok && st.body && st.body.running === false && st.body.last) {
          const l = st.body.last;
          /* K-8 (Sprint 85): Der Server liefert einen Code; der Text kommt aus i18n
             (DE/EN). Der deutsche Freitext `error` ist nur Rückfall für unbekannte Codes. */
          const ALLE_FEHLER = {
            list: 'admin.backup.all-fail-list', key: 'admin.backup.all-fail-key',
            abort: 'admin.backup.all-fail-abort', panic: 'admin.backup.all-fail-panic',
          };
          if (l.code && ALLE_FEHLER[l.code]) fertig('err', I18n.t(ALLE_FEHLER[l.code]));
          else if (l.error) fertig('err', I18n.t('admin.backup.all-fail', l.error));
          else if (l.failed > 0) fertig('err', I18n.t('admin.backup.all-partial', l.ok, l.failed));
          else fertig('ok', I18n.t('admin.backup.all-ok', l.ok));
          return;
        }
        await new Promise(res => setTimeout(res, 1500));
      }
      fertig('err', I18n.t('admin.backup.all-timeout'));
    }
    /* F-1829: Beim Aufbau einmal fragen, ob schon ein Alle-Boards-Lauf läuft
       (Reiterwechsel/Dialog neu geöffnet) — dann Knopf sperren und weiter pollen. */
    (async () => {
      let st;
      try { st = await AdminClient.allBackupStatus(); } catch { return; }
      const abgebrochen = () => lauf !== _backupLauf || _activeSection !== 'backup' || !nowBtn.isConnected;
      if (abgebrochen() || alleLaeuft) return;
      if (!(st && st.ok && st.body && st.body.running === true)) return;
      alleLaeuft = true;
      nowBtn.disabled = true;
      nowBtn.setAttribute('aria-busy', 'true');
      meldung(flash, 'busy', I18n.t('admin.backup.all-busy'));
      const fertig = (art, text) => { alleLaeuft = false; beende(nowBtn, flash, art, text); };
      await pollenAlle(abgebrochen, fertig, true);
    })();
    nowBtn.addEventListener('click', async () => {
      const id = boardInput.value.trim();
      if (!id) { await sichereAlle(); return; }
      starte(nowBtn, flash);
      let r;
      try { r = await holen(`/boards/${encodeURIComponent(id)}/backup/now`, { method: 'POST' }); }
      catch { r = { ok: false, status: 0 }; }
      if (r.ok) { beende(nowBtn, flash, 'ok', I18n.t('admin.backup.msg-ok')); refreshList(); }
      else beende(nowBtn, flash, 'err', await backupFehlerAus(r));
    });

    /* Upload-Sektion */
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.backup.upload.title') }));
    const upForm = el('div', { cls: 'admin-smtp-form' });
    const fileRow = el('div', { cls: 'admin-form-row' });
    fileRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.backup.upload.file') }));
    const fileInput = el('input', { cls: 'admin-form-input' });
    fileInput.type = 'file';
    /* #336: `.enc` gehört dazu, seit Backups verschlüsselt abgelegt werden
       können. Ohne die Erweiterung liesse der Dateidialog ein
       `boardbackup-….tar.gz.enc` gar nicht erst auswählen — der Restore-Pfad
       kann es längst. */
    fileInput.accept = '.tar.gz,.gz,.enc';
    fileRow.appendChild(fileInput);
    upForm.appendChild(fileRow);

    const modeRow = el('div', { cls: 'admin-form-row' });
    modeRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.backup.upload.mode') }));
    const modeSelect = el('select', { cls: 'admin-form-input' });
    const optOver = el('option', { text: I18n.t('admin.backup.upload.mode-over') });
    optOver.value = 'overwrite';
    const optNew = el('option', { text: I18n.t('admin.backup.upload.mode-new') });
    optNew.value = 'as-new';
    modeSelect.appendChild(optOver);
    modeSelect.appendChild(optNew);
    modeRow.appendChild(modeSelect);
    upForm.appendChild(modeRow);

    const targetRow = el('div', { cls: 'admin-form-row' });
    targetRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.backup.upload.target') }));
    const targetInput = el('input', { cls: 'admin-form-input' });
    targetInput.type = 'text';
    targetRow.appendChild(targetInput);
    upForm.appendChild(targetRow);
    _bodyEl.appendChild(upForm);

    const upActions = el('div', { cls: 'admin-actions' });
    const upBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.backup.upload.btn') });
    upActions.appendChild(upBtn);
    _bodyEl.appendChild(upActions);
    upBtn.addEventListener('click', async () => {
      const f = fileInput.files && fileInput.files[0];
      if (!f) { meldung(flash, 'err', I18n.t('admin.backup.err-no-file')); markiereKnopf(upBtn, 'err'); return; }
      if (!confirm(I18n.t('admin.backup.confirm-restore'))) {
        meldung(flash, 'busy', I18n.t('admin.backup.msg-cancelled'));
        return;
      }
      starte(upBtn, flash);
      const fd = new FormData();
      fd.append('file', f);
      fd.append('mode', modeSelect.value);
      if (targetInput.value.trim()) fd.append('target_board_id', targetInput.value.trim());
      /* Modus VOR dem await festhalten (Verify Sprint 85, F-1833): Das
         Auswahlfeld bleibt während des Uploads bedienbar. */
      const modus = modeSelect.value;
      let r;
      try { r = await holen('/backup/restore', { method: 'POST', body: fd }); }
      catch { r = { ok: false, status: 0, json: async () => ({}) }; }
      /* Restore-Abschluss wie in den Board-Einstellungen (Karte #726; Kritiker
         Sprint 85, F-1827): Überschreibt der Admin das gerade offene Board,
         übernimmt main.js sofort den Server-Stand und verwirft ein geplantes
         Speichern — sonst landeten ungespeicherte Änderungen ohne Rückfrage
         auf dem wiederhergestellten Board. Ob es das offene Board ist, prüft
         der Empfänger; Ziel ist die vom Server gemeldete board_id. */
      if (r.ok) {
        const b = await r.json().catch(() => ({}));
        beende(upBtn, flash, 'ok', I18n.t('admin.backup.msg-restored'));
        const boardId = (b && typeof b.board_id === 'string' && b.board_id) || targetInput.value.trim();
        EventBus.emit('backup:restored', { boardId, mode: modus });
      }
      else if (r.status === 413) beende(upBtn, flash, 'err', I18n.t('admin.backup.err-too-large'));
      else { const b = await r.json().catch(() => ({})); beende(upBtn, flash, 'err', (b && b.error) || backupFehler(r.status)); }
    });

    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.backup.help') }));
  }

  /* #307/ADR-0102: Clients-+Projekte-Sektion mit zwei Toggles +
     Default-Projekt-Picker. Config-Keys (Section "clients-projects"):
       - clients-projects.projects_enabled (bool als "true"/"false")
       - clients-projects.clients_enabled  (bool, hängt von projects_enabled ab)
       - clients-projects.default_project_id (UUID-String oder leer)
     Client-Toggle ist disabled, solange projects_enabled=false.
     Server-Validierung (Doppelgürtel) ist Folge-Karte — UI verhindert
     den invaliden Zustand. */
  async function renderClientsProjects() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const cfgRes = await AdminClient.listConfig('clients-projects');
    const projRes = await ClientsProjectsClient.listProjects();
    _bodyEl.innerHTML = '';
    if (!cfgRes.ok) { renderError(cfgRes.status); return; }

    const cfgMap = {};
    (Array.isArray(cfgRes.body?.entries) ? cfgRes.body.entries : []).forEach(e => { cfgMap[e.key] = e; });
    const isBoolTrue = v => v === 'true' || v === '1';

    const initialProjEn = isBoolTrue(cfgMap['clients-projects.projects_enabled']?.value);
    const initialClientEn = isBoolTrue(cfgMap['clients-projects.clients_enabled']?.value);
    const initialDefault = cfgMap['clients-projects.default_project_id']?.value || '';

    const form = el('div', { cls: 'admin-cp-form' });

    /* Toggle Projekt-Liste */
    const projRow = el('div', { cls: 'admin-form-row' });
    projRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.cp.projects-enabled') }));
    const projChk = el('input', { cls: 'admin-form-input', attrs: { type: 'checkbox' } });
    projChk.checked = initialProjEn;
    projRow.appendChild(projChk);
    form.appendChild(projRow);

    /* Toggle Client-Liste — disabled wenn Projekt-Liste aus */
    const clientRow = el('div', { cls: 'admin-form-row' });
    clientRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.cp.clients-enabled') }));
    const clientChk = el('input', { cls: 'admin-form-input', attrs: { type: 'checkbox' } });
    clientChk.checked = initialClientEn && initialProjEn;
    clientChk.disabled = !initialProjEn;
    clientRow.appendChild(clientChk);
    form.appendChild(clientRow);

    const clientHint = el('p', { cls: 'admin-hint admin-cp-disabled-hint',
      text: I18n.t('admin.cp.clients-disabled-hint') });
    clientHint.style.display = initialProjEn ? 'none' : '';
    form.appendChild(clientHint);

    /* Default-Projekt-Picker */
    const defRow = el('div', { cls: 'admin-form-row' });
    defRow.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.cp.default-project') }));
    const defSel = el('select', { cls: 'admin-form-input' });
    const noneOpt = el('option', { text: I18n.t('admin.cp.default-project-none') });
    noneOpt.value = '';
    defSel.appendChild(noneOpt);
    if (projRes.ok && Array.isArray(projRes.body)) {
      projRes.body.filter(p => p.active !== false).forEach(p => {
        const opt = el('option', { text: p.name });
        opt.value = p.id;
        if (p.id === initialDefault) opt.selected = true;
        defSel.appendChild(opt);
      });
    }
    defRow.appendChild(defSel);
    form.appendChild(defRow);

    _bodyEl.appendChild(form);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.cp.default-project-hint') }));

    /* Toggle-Verkettung: Projekt-Liste deaktivieren → Client-Toggle auto-aus + disabled */
    projChk.addEventListener('change', () => {
      if (projChk.checked) {
        clientChk.disabled = false;
        clientHint.style.display = 'none';
      } else {
        clientChk.disabled = true;
        clientChk.checked = false;
        clientHint.style.display = '';
      }
    });

    const actions = el('div', { cls: 'admin-actions' });
    const saveBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.btn-save') });
    const flash = el('div', { cls: 'admin-flash' });

    saveBtn.addEventListener('click', async () => {
      starte(saveBtn, flash);
      const writes = [
        ['clients-projects.projects_enabled', projChk.checked ? 'true' : 'false'],
        ['clients-projects.clients_enabled',  clientChk.checked ? 'true' : 'false'],
        ['clients-projects.default_project_id', defSel.value],
      ];
      let failed = false;
      let fehlerStatus = 0;
      for (const [key, val] of writes) {
        const r = await AdminClient.setConfig(key, val, false);
        if (!r.ok) { failed = true; fehlerStatus = r.status; break; }
      }
      if (failed) beende(saveBtn, flash, 'err', serverFehler(fehlerStatus));
      else beende(saveBtn, flash, 'ok', I18n.t('admin.msg-saved'));
    });

    actions.appendChild(saveBtn);
    _bodyEl.appendChild(actions);
    _bodyEl.appendChild(flash);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.cp.hint') }));
  }

  /* #575 (Folge zu #571): Nutzer-Tab — Tabelle, Admin-Umschalter,
     Löschen mit Tipp-Bestätigung + Board-Übertragung bei 409. */
  async function renderUsers() {
    const lauf = ++_usersLauf;
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    /* Eigene Identität für "Konto nicht selbst löschbar anbieten" — der
       Server lehnt Selbstlöschung ohnehin mit 409 ab (ErrSelbstloeschung),
       das hier ist nur die UI-Vorwegnahme. Scheitert der Lookup, bleibt die
       Zeile bedienbar — der Server bleibt die Wahrheit. */
    let me = null;
    try { me = await StoragePlugin.getActivePlugin().getCurrentUser?.(); } catch { /* s.o. */ }
    const meEmail = (me && me.email) ? String(me.email).trim().toLowerCase() : '';

    /* Karte #1078: Konten parallel zur Nutzerliste laden. */
    const [res, kontenRes] = await Promise.all([AdminClient.listUsers(), ladeKonten()]);
    /* K-B8 (Session-Kritiker Sprint 72): Während der awaits kann der Admin
       den Reiter gewechselt haben — dann gehört _bodyEl einer anderen
       Sektion, und die Tabelle darf dort nicht landen. */
    if (lauf !== _usersLauf || _activeSection !== 'users' || !_bodyEl) return;
    /* #1078: Schon eingeblendete Einladungslinks (nur im DOM) in den neuen Aufbau
       hinüberretten — sonst verschwänden sie beim Löschen eines Nutzers ohne Hinweis. */
    const alteLinks = _bodyEl.querySelector('.admin-konten-links');
    if (alteLinks) alteLinks.remove();
    _bodyEl.innerHTML = '';
    /* K-B7: Die Erfolgsmeldung einer Löschung erscheint VOR einem möglichen
       Ladefehler — sonst ginge das Pseudonym verloren und tauchte später
       ohne Bezug wieder auf. */
    if (_usersFlashMsg) {
      _bodyEl.appendChild(el('div', { cls: 'admin-flash admin-users-flash', text: _usersFlashMsg, attrs: { role: 'status', tabindex: '-1' } }));
      _usersFlashMsg = '';
    }
    /* Karte #1078 (ADR-0149 §6): Einladen/Konten über der Nutzerliste; fehlt der
       Endpunkt (404, keine eingebaute Anmeldung), entfällt nur dieser Teil. */
    /* Nutzerliste in eigenem Behälter, damit sie nach einer Konten-Aktion allein
       nachgeladen werden kann (ohne Konten-Teil und Linkbox neu zu bauen). */
    const nutzerHost = el('div', { cls: 'admin-users-host' });
    async function ladeNutzer() {
      const r = await AdminClient.listUsers();
      if (lauf !== _usersLauf || _activeSection !== 'users' || !nutzerHost.isConnected) return;
      zeichneNutzer(r);
    }
    if (kontenTeilDa(kontenRes)) _bodyEl.appendChild(baueKontenTeil(kontenRes, meEmail, alteLinks, ladeNutzer));
    _bodyEl.appendChild(nutzerHost);
    zeichneNutzer(res);

    function zeichneNutzer(res) {
    nutzerHost.innerHTML = '';
    if (!res.ok) { nutzerHost.appendChild(fehlerAbsatz(res.status)); return; }
    const users = (res.body && Array.isArray(res.body.users)) ? res.body.users : [];

    if (users.length === 0) {
      nutzerHost.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.users.empty') }));
      return;
    }

    const table = el('table', { cls: 'admin-audit-table admin-users-table' });
    const thead = el('thead');
    const trh = el('tr');
    ['admin.users.col-email', 'admin.users.col-name', 'admin.users.col-admin',
      'admin.users.col-boards-owner', 'admin.users.col-boards-einzel', 'admin.users.col-actions']
      .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
    thead.appendChild(trh);
    table.appendChild(thead);
    const tbody = el('tbody');

    users.forEach(u => {
      const istEigen = meEmail !== '' && String(u.email || '').trim().toLowerCase() === meEmail;
      const tr = el('tr');
      tr.appendChild(el('td', { text: u.email || '' }));
      tr.appendChild(el('td', { text: u.name || '' }));

      /* Admin-Umschalter — direkt beim Umlegen geschrieben (kein separater
         Speichern-Button, wie schon bei den Config-Toggles). */
      const adminCell = el('td');
      const adminChk = el('input', { attrs: { type: 'checkbox', 'aria-label': I18n.t('admin.users.aria-admin', u.email || '') } });
      adminChk.checked = !!u.is_admin;
      const adminErr = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
      adminChk.addEventListener('change', async () => {
        const naechster = adminChk.checked;
        /* K-B10: Volle Server-Administration nicht per Fehlklick vergeben —
           Rückfrage wie bei den anderen heiklen Admin-Aktionen. Entziehen
           braucht keine: der Server schützt den letzten Admin. */
        if (naechster && !confirm(I18n.t('admin.users.confirm-grant', u.email || ''))) {
          adminChk.checked = false;
          zeilenMeldung(adminErr, 'busy', I18n.t('admin.cancelled'));
          return;
        }
        adminChk.disabled = true;
        zeilenMeldung(adminErr, 'busy', I18n.t('admin.working'));
        const r = await AdminClient.setAdminFlag(u.id, naechster);
        adminChk.disabled = false;
        if (!r.ok) {
          adminChk.checked = !naechster; /* zurücknehmen */
          zeilenMeldung(adminErr, 'err', r.status === 409
            ? I18n.t('admin.users.error-admin-toggle-last-admin')
            : serverFehler(r.status));
        } else {
          zeilenMeldung(adminErr, 'ok', I18n.t('admin.msg-done'));
        }
      });
      adminCell.appendChild(adminChk);
      adminCell.appendChild(adminErr);
      tr.appendChild(adminCell);

      tr.appendChild(el('td', { text: String(u.boards_owner ?? 0) }));
      tr.appendChild(el('td', { text: String(u.boards_einzel_owner ?? 0) }));

      const actionsCell = el('td');
      if (istEigen) {
        actionsCell.appendChild(el('span', { cls: 'admin-hint', text: I18n.t('admin.users.self-badge') }));
      } else {
        const delBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.users.btn-delete'), attrs: { 'aria-label': I18n.t('admin.users.aria-delete', u.email || '') } });
        delBtn.addEventListener('click', () => oeffneLoeschBestaetigung(u, actionsCell, delBtn, users));
        actionsCell.appendChild(delBtn);
      }
      tr.appendChild(actionsCell);
      tbody.appendChild(tr);
    });

    table.appendChild(tbody);
    nutzerHost.appendChild(table);
    }
  }

  /* Karte #1078 (ADR-0149 §6): Konten und Einladungen im Reiter „Benutzer“.
     Kein zweiter Reiter: renderUsers() hängt diesen Teil über die bisherige
     Nutzerliste. Alles Server-Dynamische (auch `gemeldete_email`/`gemeldeter_name`,
     ungeprüfter Text vom Anbieter) wird NUR über textContent gesetzt.
     404 auf /admin/konten = keine eingebaute Anmeldung → der Teil entfällt. */
  let _kontenLauf = 0;
  const kt = (key, ...args) => I18n.t('admin.konten.' + key, ...args);
  const KONTEN_API = { bestaetigen: 'bestaetigen', ablehnen: 'ablehnen', sperren: 'sperren',
    entsperren: 'entsperren', zuruecksetzen: 'zuruecksetzen', neuerlink: 'zuruecksetzen' };

  function ladeKonten() {
    if (typeof AdminClient.listKonten !== 'function') return Promise.resolve(null);
    return Promise.resolve().then(() => AdminClient.listKonten()).catch(() => null);
  }
  /* Teil sichtbar? Nicht bei fehlendem Endpunkt (404, kein Antwortformat). */
  function kontenTeilDa(res) {
    if (!res || res.status === 404) return false;
    return !(res.ok && !Array.isArray(res.body?.konten));
  }
  const kleinMail = (s) => String(s || '').trim().toLowerCase();
  const zeitKurz = (iso) => String(iso || '').slice(0, 16).replace('T', ' ');
  function kontoZustandText(z) {
    return KONTEN_ZUSTAENDE.includes(z) ? kt('z-' + z) : String(z || '');
  }
  const KONTEN_ZUSTAENDE = ['invited', 'pending', 'active', 'blocked'];
  /* Anzeige des Anmeldewegs (Kommentar F-2880): '' → Strich, `standard` ist nur
     eine Altbeschriftung (PruefenFuer bindet je Person) → „bisheriger Anbieter“. */
  function kontoWegText(k) {
    const w = k.weg || '';
    if (!w) return kt('weg-keiner');
    if (w === 'password') return kt(k.hat_zwei_faktor ? 'weg-password-2fa' : 'weg-password');
    if (w === 'standard') return kt('weg-standard');
    return String(w);
  }

  function baueKontenTeil(startRes, meEmail, uebernommeneLinks, ladeNutzer) {
    const lauf = ++_kontenLauf;
    const host = el('section', { cls: 'admin-konten', attrs: { 'aria-labelledby': 'admin-konten-title' } });
    host.appendChild(el('h3', { cls: 'admin-section-title', text: kt('title'), attrs: { id: 'admin-konten-title' } }));
    host.appendChild(el('p', { cls: 'admin-hint admin-konten-intro', text: kt('intro') }));

    const flash = el('div', { cls: 'admin-flash admin-konten-flash', attrs: { role: 'status', tabindex: '-1' } });

    /* Einladen */
    const form = el('div', { cls: 'admin-form-row admin-konten-form' });
    form.appendChild(el('label', { cls: 'admin-form-label', text: kt('mail-label'), attrs: { for: 'admin-konten-mails' } }));
    const area = el('textarea', { cls: 'admin-form-input admin-form-textarea',
      attrs: { id: 'admin-konten-mails', rows: '3', autocomplete: 'off', spellcheck: 'false' } });
    const einladenBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: kt('btn-einladen') });
    form.appendChild(area);
    form.appendChild(einladenBtn);
    host.appendChild(form);
    host.appendChild(flash);

    /* Einmal angezeigte Links — nur im DOM, nie in einer Variablen oder im Speicher des Browsers. */
    const linkHost = el('div', { cls: 'admin-konten-links-host' });
    host.appendChild(linkHost);
    /* Bei einem Neuaufbau des Reiters (z. B. nach dem Löschen eines Nutzers) übernommene Linkbox. */
    if (uebernommeneLinks) linkHost.appendChild(uebernommeneLinks);
    const listHost = el('div', { cls: 'admin-konten-liste' });
    host.appendChild(listHost);

    function linkBox() {
      let box = linkHost.querySelector('.admin-konten-links');
      if (box) return box;
      box = el('div', { cls: 'admin-enc-box admin-konten-links', attrs: { role: 'status' } });
      box.appendChild(el('p', { cls: 'admin-form-label', text: kt('ergebnis-title') }));
      box.appendChild(el('p', { cls: 'admin-enc-key', text: kt('links-hint') }));
      box.appendChild(el('div', { cls: 'admin-konten-links-rows' }));
      const aus = el('button', { cls: 'btn admin-btn', text: kt('btn-links-ausblenden') });
      aus.addEventListener('click', () => {
        const f = box.closest('.admin-konten')?.querySelector('.admin-konten-flash') || flash;
        box.remove(); f.focus();
      });
      box.appendChild(aus);
      linkHost.appendChild(box);
      return box;
    }
    function entferneLinkZeile(email) {
      const box = linkHost.querySelector('.admin-konten-links');
      if (!box) return;
      box.querySelectorAll('.admin-konten-link').forEach(r => { if (r.dataset.email === kleinMail(email)) r.remove(); });
      if (!box.querySelector('.admin-konten-link')) box.remove();
    }
    /* items: { email, link?, ablauf?, erneut?, mail?, fehler? } */
    function zeigeLinks(items) {
      if (!items.length) return null;
      const rows = linkBox().querySelector('.admin-konten-links-rows');
      let erstes = null;
      items.forEach(it => {
        rows.querySelectorAll('.admin-konten-link').forEach(r => { if (r.dataset.email === kleinMail(it.email)) r.remove(); });
        const row = el('div', { cls: 'admin-konten-link' });
        row.dataset.email = kleinMail(it.email);
        if (it.fehler || !it.link) {
          const f = it.fehler || 'intern';
          const key = ['adresse', 'hat_zugang', 'schon_eingeloest', 'belegt', 'intern'].includes(f) ? f : 'intern';
          row.classList.add('admin-konten-link--fehler');
          row.appendChild(el('span', { cls: 'admin-row-msg admin-row-msg--err', text: `${it.email}: ${kt('fehler-' + key)}` }));
          rows.appendChild(row);
          return;
        }
        const id = 'admin-konten-link-' + (rows.children.length + 1) + '-' + Math.random().toString(36).slice(2, 7);
        row.appendChild(el('label', { cls: 'admin-form-label', text: kt('link-label', it.email), attrs: { for: id } }));
        const feld = el('input', { cls: 'admin-form-input admin-agents-token',
          attrs: { id, readonly: '', spellcheck: 'false', autocomplete: 'off' } });
        feld.value = it.link;
        const kopieren = el('button', { cls: 'btn admin-btn', text: kt('btn-copy'),
          attrs: { 'aria-label': I18n.t('admin.konten.aria', kt('btn-copy'), it.email) } });
        const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
        kopieren.addEventListener('click', async () => {
          try {
            await navigator.clipboard.writeText(feld.value);
            markiereKnopf(kopieren, 'ok');
            zeilenMeldung(msg, 'ok', kt('copied'));
          } catch {
            feld.select();
            markiereKnopf(kopieren, 'err');
            zeilenMeldung(msg, 'err', kt('copy-failed'));
          }
        });
        row.appendChild(feld);
        row.appendChild(kopieren);
        row.appendChild(msg);
        const info = [];
        if (it.ablauf) info.push(kt('gueltig-bis', zeitKurz(it.ablauf)));
        if (it.erneut) info.push(kt('erneut'));
        if (it.mail && ['gesendet', 'nicht_eingerichtet', 'fehlgeschlagen'].includes(it.mail)) info.push(kt('mail-' + it.mail));
        if (info.length) row.appendChild(el('div', { cls: 'admin-hint admin-konten-link-info', text: info.join(' · ') }));
        rows.appendChild(row);
        if (!erstes) erstes = feld;
      });
      return erstes;
    }

    /* Aktion auf ein Konto; true = Liste wurde neu gezeichnet (Panel/Knopf sind weg). */
    async function fuehreAus(k, key) {
      meldung(flash, 'busy', I18n.t('admin.working'));
      const r = key === 'widerrufen'
        ? await AdminClient.widerrufeEinladung(k.id)
        : await AdminClient.kontoAktion(k.id, KONTEN_API[key]);
      if (lauf !== _kontenLauf) return false;
      if (r.ok) {
        const body = r.body || {};
        let fokus = null;
        let text;
        if (key === 'widerrufen') { text = kt('msg-widerrufen', k.email); entferneLinkZeile(k.email); }
        else if (key === 'bestaetigen') text = kt('msg-bestaetigt', k.email);
        else if (key === 'ablehnen') { text = kt('msg-abgelehnt', k.email); entferneLinkZeile(k.email); }
        else if (key === 'sperren') text = kt('msg-gesperrt', k.email);
        else if (key === 'entsperren') {
          text = kt('msg-entsperrt', k.email, kontoZustandText(body.zustand));
          if (body.zustand === 'pending') text += ' ' + kt('msg-entsperrt-bestaetigen');
        } else {
          text = kt(key === 'neuerlink' ? 'msg-neuerlink' : 'msg-zurueckgesetzt', k.email);
          fokus = zeigeLinks([{ email: k.email, link: body.link, ablauf: body.ablauf, mail: body.mail }]);
        }
        meldung(flash, 'ok', text);
        /* Bestätigen/Sperren/… ändern auch die Nutzerliste darunter. */
        await Promise.all([ladeListe(), ladeNutzer ? ladeNutzer() : null]);
        if (lauf !== _kontenLauf) return true;
        /* Nach „Entsperren“ landet die Person bei „wartet“: Bestätigen gleich anbieten. */
        if (key === 'entsperren' && body.zustand === 'pending') {
          fokus = [...listHost.querySelectorAll('button[data-aktion="bestaetigen"]')].find(b => b.dataset.konto === k.id) || null;
        }
        (fokus || flash).focus();
        return true;
      }
      if (r.status === 503) { meldung(flash, 'err', kt('err-503')); flash.focus(); return false; }
      let txt;
      if (r.status === 409) {
        txt = key === 'ablehnen' ? kt('err-409-ablehnen')
          : key === 'bestaetigen' ? kt('err-409-bestaetigen')
          : kt(key === 'widerrufen' ? 'err-409-widerrufen' : 'err-409');
      }
      else if (r.status === 404) txt = kt('err-404');
      else txt = serverFehler(r.status);
      meldung(flash, 'err', txt);
      const neu = r.status === 409 || r.status === 404;
      if (neu) await ladeListe();
      if (lauf !== _kontenLauf) return neu;
      flash.focus();
      return neu;
    }

    /* Gefährliche Aktion: Bestätigungs-Panel wie bei „Nutzer löschen“, kein confirm(). */
    function oeffnePanel(cell, trigger, k, key) {
      trigger.disabled = true;
      const panel = el('div', { cls: 'admin-users-confirm admin-konten-confirm' });
      panel.appendChild(el('p', { cls: 'admin-users-confirm-warning', text: kt('confirm-' + key + (key === 'widerrufen' && k.hat_zugang ? '-zugang' : '')) }));
      const aktionen = el('div', { cls: 'admin-actions' });
      const ok = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: kt('btn-' + key),
        attrs: { 'aria-label': I18n.t('admin.konten.aria', kt('btn-' + key), k.email) } });
      const abbrechen = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.users.btn-cancel') });
      aktionen.appendChild(ok);
      aktionen.appendChild(abbrechen);
      panel.appendChild(aktionen);
      ok.addEventListener('click', async () => {
        ok.disabled = true; abbrechen.disabled = true;
        const neu = await fuehreAus(k, key);
        if (!neu && panel.isConnected) { ok.disabled = false; abbrechen.disabled = false; }
      });
      abbrechen.addEventListener('click', () => {
        panel.remove();
        trigger.disabled = false;
        meldung(flash, 'busy', I18n.t('admin.cancelled'));
        trigger.focus();
      });
      cell.appendChild(panel);
      abbrechen.focus();
    }

    function zeichneListe(res) {
      listHost.innerHTML = '';
      if (!res || !res.ok) {
        listHost.appendChild(el('p', { cls: 'admin-row-msg admin-row-msg--err', text: res && res.status === 503 ? kt('err-503') : kt('laden-fehler'), attrs: { role: 'alert' } }));
        return;
      }
      const konten = res.body.konten;
      if (konten.length === 0) {
        listHost.appendChild(el('p', { cls: 'admin-empty', text: kt('empty') }));
        return;
      }
      const table = el('table', { cls: 'admin-audit-table admin-users-table admin-konten-table' });
      const thead = el('thead');
      const trh = el('tr');
      ['col-email', 'col-zustand', 'col-weg', 'col-link', 'col-actions'].forEach(c => trh.appendChild(el('th', { text: kt(c) })));
      thead.appendChild(trh);
      table.appendChild(thead);
      const tbody = el('tbody');
      konten.forEach(k => {
        const tr = el('tr');
        tr.dataset.konto = k.id;
        const mailCell = el('td');
        mailCell.appendChild(el('div', { cls: 'admin-konten-email', text: k.email || '' }));
        if (k.gemeldete_email || k.gemeldeter_name) {
          const g = el('div', { cls: 'admin-konten-gemeldet', text: kt('gemeldet', k.gemeldeter_name, k.gemeldete_email) });
          const abweichend = !!k.gemeldete_email && kleinMail(k.gemeldete_email) !== kleinMail(k.email);
          /* F-2961: Flag vom Server statt Zusatztext in der Adresse; Grund wird genannt. */
          const unbestaetigt = !!k.gemeldete_email && k.gemeldete_email_unbestaetigt === true;
          if (abweichend || unbestaetigt) g.classList.add('admin-konten-gemeldet--abweichend');
          if (abweichend) g.appendChild(el('strong', { cls: 'admin-konten-abweichung', text: ' ' + kt('abweichung') }));
          if (unbestaetigt) g.appendChild(el('strong', { cls: 'admin-konten-unbestaetigt', text: ' ' + kt('unbestaetigt') }));
          mailCell.appendChild(g);
        }
        tr.appendChild(mailCell);

        const zCell = el('td');
        zCell.appendChild(el('span', { cls: 'admin-konten-zustand admin-konten-zustand--' + String(k.zustand || '').replace(/[^a-z]/g, ''), text: kontoZustandText(k.zustand) }));
        tr.appendChild(zCell);
        tr.appendChild(el('td', { text: kontoWegText(k) }));

        const lCell = el('td');
        if (k.zustand === 'invited') {
          if (k.link_abgelaufen) lCell.appendChild(el('span', { cls: 'admin-row-msg admin-row-msg--err', text: kt('link-abgelaufen') }));
          else if (k.link_ablauf) lCell.appendChild(el('span', { text: kt('gueltig-bis', zeitKurz(k.link_ablauf)) }));
        }
        tr.appendChild(lCell);

        const aCell = el('td');
        const eigen = meEmail !== '' && kleinMail(k.email) === meEmail;
        const knopf = (key, gefahr) => {
          const b = el('button', { cls: 'btn admin-btn admin-konten-btn', text: kt('btn-' + key),
            attrs: { 'aria-label': I18n.t('admin.konten.aria', kt('btn-' + key), k.email), 'data-aktion': key } });
          b.dataset.konto = k.id;
          b.addEventListener('click', () => {
            if (gefahr) oeffnePanel(aCell, b, k, key);
            else { b.disabled = true; fuehreAus(k, key).then(neu => { if (!neu && b.isConnected) b.disabled = false; }); }
          });
          aCell.appendChild(b);
        };
        const z = k.zustand;
        /* F-2921/F-2930/F-2931: „Neuer Link“ (mit Rückfrage, setzt erneut zurück),
           wenn das Konto zurückgesetzt wurde oder die Person eine Nutzerzeile hat.
           „Widerrufen“ fehlt nur bei Rücksetz-Link UND Nutzerzeile (widerrufbar:
           false): Das Löschen ließe die Person ohne Konto zurück. Sonst beide. */
        if (z === 'invited') {
          if (k.neuer_link && !eigen) knopf('neuerlink', true);
          if (k.widerrufbar !== false) knopf('widerrufen', true);
        }
        if (z === 'pending') { knopf('bestaetigen', false); knopf('ablehnen', true); }
        if (z === 'blocked') knopf('entsperren', false);
        if (!eigen) {
          if (z === 'active' || z === 'pending') knopf('sperren', true);
          /* Frisch eingeladene Konten nicht zurücksetzen: der Server lehnt das mit 409 ab
             (server/storage/konten_einloesen.go); Weg ist Widerrufen oder erneut einladen.
             Zurückgesetzte (k.neuer_link) bekommen oben „Neuer Link“. */
          if (z !== 'invited') knopf('zuruecksetzen', true);
        } else {
          aCell.appendChild(el('span', { cls: 'admin-hint', text: I18n.t('admin.users.self-badge') }));
        }
        tr.appendChild(aCell);
        tbody.appendChild(tr);
      });
      table.appendChild(tbody);
      listHost.appendChild(table);
    }

    async function ladeListe() {
      const r = await ladeKonten();
      if (lauf !== _kontenLauf) return;
      zeichneListe(r);
    }

    einladenBtn.addEventListener('click', async () => {
      const adressen = area.value.split(/[\s,;]+/).map(s => s.trim()).filter(Boolean);
      if (adressen.length === 0) {
        meldung(flash, 'err', kt('einladen-leer'));
        area.focus();
        return;
      }
      starte(einladenBtn, flash);
      const r = await AdminClient.einladen(adressen);
      if (lauf !== _kontenLauf) return;
      if (!r.ok) {
        beende(einladenBtn, flash, 'err', r.status === 400 ? kt('einladen-400') : r.status === 503 ? kt('err-503') : serverFehler(r.status));
        return;
      }
      const erg = Array.isArray(r.body?.ergebnisse) ? r.body.ergebnisse : [];
      /* Nur fehlgeschlagene Adressen bleiben im Feld stehen (zum Korrigieren). */
      area.value = erg.filter(e => e.fehler || !e.link).map(e => e.email).join('\n');
      beende(einladenBtn, flash, 'ok', I18n.t('admin.msg-done'));
      const erstes = zeigeLinks(erg);
      await ladeListe();
      if (lauf !== _kontenLauf) return;
      (erstes || flash).focus();
    });

    zeichneListe(startRes);
    return host;
  }

  /* Karte #608 (ADR-0130 §6): Agenten-Zugänge — alle Tokens (nie ein
     Klartext), Widerruf je Token und „alle einer Person“, dazu der
     Selbstbedienungs-Schalter je Board (ab Werk aus). */
  let _agentsLauf = 0;
  /* Meldung einer Aktion, die die Liste neu aufbaut (Widerruf) — überlebt den
     Neuaufbau wie `_usersFlashMsg`, sonst verschwände die Bestätigung mit dem
     Knopf, an dem sie hing. Wird nach EINER Anzeige zurückgesetzt. */
  let _agentsFlashMsg = '';
  /* Karte #670: Klartext eines eben angelegten Tokens — für GENAU EINE Anzeige
     im nächsten Aufbau der Liste, danach null. close() und jeder spätere
     Aufbau (Widerruf, Abschnittswechsel) lassen ihn fallen. */
  let _agentsEinmal = null;
  async function renderAgents() {
    const lauf = ++_agentsLauf;
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    /* F-1443: Besitzer je Board aus GET /admin/boards (`owners[]`). Fehlt der
       Endpunkt (älterer Server, 404) oder scheitert er, bleibt die Liste ohne
       Besitzer wie bisher. */
    const [tok, sb, alle, seite] = await Promise.all([
      AdminClient.listAgentTokens(),
      AdminClient.listAgentSelbstbedienung(),
      Promise.resolve().then(() => AdminClient.listAllBoards?.()).catch(() => null),
      /* Karte #799: Auf der Seite außen sind Anbieter und Beleg Pflicht. */
      boardpaarSeite(),
    ]);
    const aussen = seite === 'aussen';
    const besitzerJeBoard = new Map();
    if (alle && alle.ok && Array.isArray(alle.body?.boards)) {
      for (const ab of alle.body.boards) {
        if (ab && ab.id != null && Array.isArray(ab.owners) && ab.owners.length) besitzerJeBoard.set(ab.id, ab.owners);
      }
    }
    if (lauf !== _agentsLauf || _activeSection !== 'agents' || !_bodyEl) return;
    _bodyEl.innerHTML = '';
    if (_agentsFlashMsg) {
      _bodyEl.appendChild(el('div', { cls: 'admin-flash admin-flash--ok', text: _agentsFlashMsg, attrs: { role: 'status' } }));
      _agentsFlashMsg = '';
    }
    if (_agentsEinmal) {
      /* Sitzungs-QA 13.10. (QA-2): wie #820 — das Formular steht weiter
         unten; ohne Scroll bliebe das Einmal-Token nach dem Neuaufbau außer Sicht. */
      zeigeAgentTokenEinmal(_agentsEinmal).scrollIntoView?.({ block: 'nearest' });
      _agentsEinmal = null;
    }
    if (!tok.ok) { renderError(tok.status); return; }
    const tokens = Array.isArray(tok.body) ? tok.body : [];
    /* #754: Titel für die Board-Liste eines Tokens über mehrere Boards
       (die Schalter-Übersicht kennt alle Boards). */
    const titelJeBoard = new Map(((sb.ok && Array.isArray(sb.body?.boards)) ? sb.body.boards : [])
      .map(b => [b.id, b.title || b.id]));
    const jetzt = Date.now();

    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.agents.tokens-title') }));
    if (tokens.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.agents.empty') }));
    } else {
      const table = el('table', { cls: 'admin-audit-table admin-agents-table' });
      const trh = el('tr');
      ['admin.agents.col-name', 'admin.agents.col-board', 'admin.agents.col-person', 'admin.agents.col-role',
        'admin.agents.col-kennung', 'admin.agents.col-herkunft', 'admin.agents.col-expires', 'admin.agents.col-status', 'admin.agents.col-actions']
        .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
      const thead = el('thead'); thead.appendChild(trh); table.appendChild(thead);
      const tbody = el('tbody');
      const personen = new Map();
      for (const t of tokens) {
        const status = t.revoked_at ? 'revoked' : (t.expires_at && Date.parse(t.expires_at) <= jetzt ? 'expired' : 'active');
        const tr = el('tr');
        /* Karte #799: Hinweis nur für geltende Tokens (aktiv), nicht für widerrufene
           oder abgelaufene (Kritiker K-4) — am schon berechneten Status. */
        const herkunftFehlt = aussen && status === 'active' && !(t.anbieter && t.beleg);
        /* F-1551 (#669): Ein delegierendes Token hat weder Board noch Person —
           beides kommt je Anfrage aus Open WebUI (JWT) bzw. dem Pfad. Leere
           Zellen sähen nach einem Datenfehler aus. */
        [t.name,
          t.delegierend ? I18n.t('admin.agents.delegierend-board')
            : t.mehrere ? I18n.t('admin.agents.mehrere-boards', (t.boards || []).length,
              (t.boards || []).map(id => titelJeBoard.get(id) || id).join(', '))
            : t.board_id,
          t.delegierend ? I18n.t('admin.agents.delegierend-person') : (t.user_email || I18n.t('admin.agents.projekt')),
          t.role,
          t.kennung ? `abt_${t.kennung}_…` : '—',
          /* Herkunftszelle: eigene Zelle mit Klasse statt Spaltenindex (Kritiker K-3). */
          { text: herkunftText(t, herkunftFehlt), cls: 'admin-agents-herkunft' + (herkunftFehlt ? ' admin-agents-herkunft-fehlt' : '') },
          t.expires_at ? t.expires_at.slice(0, 10) : I18n.t('admin.agents.unbefristet'),
          I18n.t('admin.agents.status-' + status)].forEach(v => {
          const td = el('td', { text: typeof v === 'object' ? v.text : v });
          if (typeof v === 'object' && v.cls) td.className = v.cls;
          tr.appendChild(td);
        });
        const cell = el('td');
        if (status === 'active') {
          if (t.user_id) personen.set(t.user_id, t.user_email || t.user_id);
          const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.agents.btn-revoke'),
            attrs: { 'aria-label': I18n.t('admin.agents.aria-revoke', t.name) } });
          btn.addEventListener('click', async () => {
            const msg = cell.querySelector('.admin-row-msg') || cell.appendChild(el('span', { cls: 'admin-row-msg' }));
            if (!confirm(I18n.t('admin.agents.confirm-revoke', t.name))) {
              zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
              return;
            }
            btn.disabled = true;
            zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
            const r = await AdminClient.revokeAgentToken(t.id || t.name);
            if (r.ok) {
              _agentsFlashMsg = I18n.t('admin.agents.msg-revoked', t.name);
              _bodyEl.innerHTML = '';
              await renderAgents();
              return;
            }
            btn.disabled = false;
            markiereKnopf(btn, 'err');
            zeilenMeldung(msg, 'err', serverFehler(r.status));
          });
          cell.appendChild(btn);
        }
        tr.appendChild(cell);
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      _bodyEl.appendChild(table);

      /* Austrittsweg: alle aktiven Tokens einer Person auf einmal. */
      for (const [userId, email] of personen) {
        const btn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.agents.btn-revoke-person', email) });
        const msg = el('span', { cls: 'admin-row-msg' });
        btn.addEventListener('click', async () => {
          if (!confirm(I18n.t('admin.agents.confirm-revoke-person', email))) {
            zeilenMeldung(msg, 'busy', I18n.t('admin.cancelled'));
            return;
          }
          btn.disabled = true;
          zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
          const r = await AdminClient.revokeAgentTokensOfUser(userId);
          if (r.ok) {
            _agentsFlashMsg = I18n.t('admin.agents.msg-revoked-person', email);
            _bodyEl.innerHTML = '';
            await renderAgents();
            return;
          }
          btn.disabled = false;
          markiereKnopf(btn, 'err');
          zeilenMeldung(msg, 'err', serverFehler(r.status));
        });
        _bodyEl.appendChild(btn);
        _bodyEl.appendChild(msg);
      }
    }

    const boards = (sb.ok && Array.isArray(sb.body?.boards)) ? sb.body.boards : [];
    if (boards.length) renderAgentAnlage(boards, lauf, aussen);

    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.agents.self-title') }));
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.agents.self-hint') }));
    for (const b of boards) {
      const lbl = el('label', { cls: 'admin-agents-switch' });
      const chk = el('input', { attrs: { type: 'checkbox', 'aria-label': I18n.t('admin.agents.aria-self', b.title || b.id) } });
      chk.checked = !!b.an;
      const err = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
      chk.addEventListener('change', async () => {
        const soll = chk.checked;
        chk.disabled = true;
        zeilenMeldung(err, 'busy', I18n.t('admin.working'));
        const r = await AdminClient.setAgentSelbstbedienung(b.id, soll);
        chk.disabled = false;
        if (!r.ok) { chk.checked = !soll; zeilenMeldung(err, 'err', serverFehler(r.status)); }
        else zeilenMeldung(err, 'ok', I18n.t(soll ? 'admin.agents.msg-self-on' : 'admin.agents.msg-self-off'));
      });
      /* F-1396: Boards gleichen Titels unterscheidbar (Titel + gekürzte ID),
         Zustand als Text „an/aus“ statt nur am Kästchen ablesbar. */
      const zustand = el('span', { cls: 'admin-agents-state', attrs: { 'aria-hidden': 'true' } });
      const setzeZustand = () => {
        zustand.textContent = I18n.t(chk.checked ? 'admin.agents.state-on' : 'admin.agents.state-off');
        zustand.classList.toggle('admin-agents-state--on', chk.checked);
      };
      setzeZustand();
      chk.addEventListener('change', setzeZustand);
      const kurzId = Rueckmeldung.kurzeId(b.id);
      lbl.appendChild(chk);
      lbl.appendChild(zustand);
      lbl.appendChild(document.createTextNode(' ' + (b.title || b.id) + ' '));
      lbl.appendChild(el('span', { cls: 'admin-agents-id', text: `(${kurzId})` }));
      const besitzer = besitzerJeBoard.get(b.id);
      if (besitzer) {
        lbl.appendChild(document.createTextNode(' '));
        lbl.appendChild(el('span', { cls: 'admin-agents-owner', text: I18n.t('admin.agents.owners', besitzer.join(', ')) }));
      }
      lbl.appendChild(err);
      _bodyEl.appendChild(lbl);
      _bodyEl.appendChild(el('br'));
    }
  }

  /* Karte #670 (ADR-0133 §6): Projekt-Zugang anlegen — ohne Person, Rolle
     editor, viewer oder vorschlag. Tokens MIT Person legt die Person selbst an
     (Selbstbedienung) oder der Admin über die API/CLI. */
  const AGENT_NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
  /* Karte #799: Anbieter/Beleg einer Zeile; fehlt eine Angabe auf außen, steht
     ein deutlicher Hinweis (Bestand wird nicht gesperrt). */
  function herkunftText(t, fehlt) {
    const teile = [t.anbieter, t.beleg].filter(Boolean);
    if (teile.length === 2) return teile.join(' — ');
    if (fehlt) return I18n.t('admin.agents.herkunft-fehlt') + (teile.length ? ` (${teile[0]})` : '');
    return teile.length ? teile[0] : '—';
  }

  function renderAgentAnlage(boards, lauf, aussen = false) {
    _bodyEl.appendChild(el('h3', { cls: 'admin-section-title', text: I18n.t('admin.agents.new-title') }));
    const form = el('div', { cls: 'admin-agents-new' });
    const name = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.col-name'),
      placeholder: I18n.t('admin.agents.name-placeholder'), maxlength: '63', spellcheck: 'false', autocomplete: 'off' } });
    const boardSel = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.col-board') } });
    for (const b of boards) {
      boardSel.appendChild(Object.assign(el('option', { text: `${b.title || b.id} (${Rueckmeldung.kurzeId(b.id)})` }), { value: b.id }));
    }
    const rolle = el('select', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.col-role') } });
    /* Karte #783: „intern“ legt der Server nur auf der Seite außen an (sonst 400 mit Grund). */
    for (const r of ['editor', 'viewer', 'vorschlag', 'intern']) {
      rolle.appendChild(Object.assign(el('option', { text: I18n.t('admin.agents.role-' + r) }), { value: r }));
    }
    const tage = el('input', { cls: 'admin-form-input', attrs: { type: 'number', min: '1', max: '365', 'aria-label': I18n.t('admin.agents.days') } });
    tage.value = '90';
    /* Karte #799: Anbieter und Beleg des Trainingsausschlusses — auf außen Pflicht. */
    const anbieter = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.anbieter'),
      placeholder: I18n.t('admin.agents.anbieter'), maxlength: '80', autocomplete: 'off' } });
    const beleg = el('input', { cls: 'admin-form-input', attrs: { 'aria-label': I18n.t('admin.agents.beleg'),
      placeholder: I18n.t('admin.agents.beleg'), maxlength: '300', autocomplete: 'off' } });
    if (aussen) { anbieter.required = true; beleg.required = true; }
    const btn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.agents.btn-create') });
    const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
    btn.addEventListener('click', async () => {
      const n = name.value.trim();
      if (!AGENT_NAME.test(n)) {
        name.setAttribute('aria-invalid', 'true');
        markiereKnopf(btn, 'err');
        zeilenMeldung(msg, 'err', I18n.t('admin.agents.err-name'));
        name.focus();
        return;
      }
      name.removeAttribute('aria-invalid');
      const a = anbieter.value.trim();
      const b = beleg.value.trim();
      if (aussen && (!a || !b)) {
        markiereKnopf(btn, 'err');
        zeilenMeldung(msg, 'err', I18n.t('admin.agents.fehler-herkunft'));
        (a ? beleg : anbieter).focus();
        return;
      }
      btn.disabled = true;
      zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
      const body = { name: n, board: boardSel.value, role: rolle.value, gueltig_tage: Number(tage.value) || 0 };
      /* Nur senden, was ausgefüllt ist (innen/aus: optional). */
      if (a) body.anbieter = a;
      if (b) body.beleg = b;
      const r = await AdminClient.createAgentToken(body);
      /* Inzwischen geschlossen oder neu aufgebaut: das Token nicht mehr zeigen
         (es existiert trotzdem und steht in der Liste; widerrufen geht dort). */
      if (lauf !== _agentsLauf || _activeSection !== 'agents' || !_bodyEl) return;
      btn.disabled = false;
      if (!r.ok || !r.body?.token) {
        markiereKnopf(btn, 'err');
        /* Karte #799 (F-2117): Der Servertext zu Anbieter/Beleg ist deutsch -
           übersetzt anzeigen, wie in „Meine Agenten-Zugänge“ (K-8). */
        /* Sprint-92-Kritiker KO-4: am Anfang verankert (Namensfehler zitieren den Namen). */
        const herkunftFehler = r.status === 400 && typeof r.body?.error === 'string' && /^(?:anbieter|beleg):|^auf dem externen server/.test(r.body.error);
        zeilenMeldung(msg, 'err', herkunftFehler ? I18n.t('admin.agents.fehler-herkunft-server') : (r.body?.error || serverFehler(r.status)));
        return;
      }
      _agentsEinmal = r.body.token;
      _agentsFlashMsg = I18n.t('admin.agents.msg-created', n);
      _bodyEl.innerHTML = '';
      await renderAgents();
    });
    [name, boardSel, rolle, tage, anbieter, beleg, btn, msg].forEach(x => form.appendChild(x));
    _bodyEl.appendChild(form);
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.agents.new-hint') }));
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t(aussen ? 'admin.agents.herkunft-pflicht' : 'admin.agents.herkunft-optional') }));
  }

  function zeigeAgentTokenEinmal(token, ziel = _bodyEl, hinweisKey = 'admin.agents.once-hint') {
    const box = el('div', { cls: 'admin-enc-box admin-agents-once', attrs: { role: 'status' } });
    box.appendChild(el('p', { cls: 'admin-form-label', text: I18n.t('admin.agents.once-title') }));
    const feld = el('input', { cls: 'admin-form-input admin-agents-token',
      attrs: { readonly: '', 'aria-label': I18n.t('admin.agents.once-title'), spellcheck: 'false', autocomplete: 'off' } });
    feld.value = token;
    const kopieren = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.agents.btn-copy') });
    const msg = el('span', { cls: 'admin-row-msg', attrs: { role: 'status' } });
    kopieren.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(feld.value);
        markiereKnopf(kopieren, 'ok');
        zeilenMeldung(msg, 'ok', I18n.t('admin.agents.copied'));
      } catch {
        feld.select();
        markiereKnopf(kopieren, 'err');
        zeilenMeldung(msg, 'err', I18n.t('admin.agents.copy-failed'));
      }
    });
    [feld, kopieren, msg].forEach(x => box.appendChild(x));
    box.appendChild(el('p', { cls: 'admin-enc-key', text: I18n.t(hinweisKey) }));
    ziel.appendChild(box);
    return box;
  }

  /* Kleiner Hinweis „Abgebrochen“ in der Aktionszelle, nachdem das
     Bestätigungs-Panel geschlossen wurde. */
  function abbruchHinweis(cell) {
    return cell.querySelector('.admin-row-msg') || cell.appendChild(el('span', { cls: 'admin-row-msg' }));
  }

  /* Baut die Lösch-Bestätigung EINER Zeile: Tipp-Bestätigung per exakter
     E-Mail, bei 409 mit Board-Liste eine Übertragungs-Auswahl je Board,
     erst danach ist der Bestätigen-Knopf aktiv. `alleNutzer` liefert die
     Ziel-Auswahl der Übertragung (alle außer dem zu löschenden Nutzer). */
  function oeffneLoeschBestaetigung(nutzer, cell, trigger, alleNutzer) {
    trigger.disabled = true;
    const panel = el('div', { cls: 'admin-users-confirm' });
    panel.appendChild(el('p', { cls: 'admin-users-confirm-warning', text: I18n.t('admin.users.confirm-warning') }));
    /* K-B11: Label per id verknüpft; der Platzhalter nennt nicht die
       Ziel-Adresse (sonst wird die Bestätigung zum Abschreiben). */
    const idBasis = 'admin-users-del-' + String(nutzer.id || '').replace(/[^A-Za-z0-9_-]/g, '_');
    panel.appendChild(el('label', { cls: 'admin-form-label', text: I18n.t('admin.users.confirm-email-label', nutzer.email || ''), attrs: { for: idBasis + '-email' } }));
    const emailInput = el('input', { cls: 'admin-form-input', attrs: { id: idBasis + '-email', autocomplete: 'off' } });
    emailInput.type = 'text';
    emailInput.placeholder = I18n.t('admin.users.confirm-email-placeholder');
    panel.appendChild(emailInput);

    const transferHost = el('div', { cls: 'admin-users-transfer' });
    panel.appendChild(transferHost);

    const panelActions = el('div', { cls: 'admin-actions' });
    const confirmBtn = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.users.btn-confirm-delete') });
    confirmBtn.disabled = true;
    const cancelBtn = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.users.btn-cancel') });
    panelActions.appendChild(confirmBtn);
    panelActions.appendChild(cancelBtn);
    panel.appendChild(panelActions);

    const flash = el('div', { cls: 'admin-flash', attrs: { role: 'status' } });
    panel.appendChild(flash);

    /* boardId → <select>-Element der Übertragungsauswahl. */
    let auswahl = {};

    function auswertenAktivierung() {
      const emailPasst = emailInput.value === nutzer.email;
      const alleZieleGewaehlt = Object.values(auswahl).every(sel => sel.value !== '');
      confirmBtn.disabled = !(emailPasst && alleZieleGewaehlt);
    }
    emailInput.addEventListener('input', auswertenAktivierung);

    function renderTransfer(boards) {
      transferHost.innerHTML = '';
      auswahl = {};
      if (!boards || boards.length === 0) return;
      transferHost.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.users.transfer-intro') }));
      boards.forEach((b, i) => {
        const row = el('div', { cls: 'admin-form-row' });
        const selId = idBasis + '-ziel-' + i;
        row.appendChild(el('label', { cls: 'admin-form-label', text: b.title || b.id, attrs: { for: selId } }));
        const sel = el('select', { cls: 'admin-form-input', attrs: { id: selId } });
        const leer = el('option', { text: I18n.t('admin.users.transfer-select-placeholder') });
        leer.value = '';
        sel.appendChild(leer);
        alleNutzer.filter(cand => cand.id !== nutzer.id).forEach(cand => {
          const opt = el('option', { text: cand.email });
          opt.value = cand.email;
          sel.appendChild(opt);
        });
        sel.addEventListener('change', auswertenAktivierung);
        row.appendChild(sel);
        transferHost.appendChild(row);
        auswahl[b.id] = sel;
      });
      auswertenAktivierung();
    }

    confirmBtn.addEventListener('click', async () => {
      if (confirmBtn.disabled) return;
      confirmBtn.disabled = true;
      cancelBtn.disabled = true;
      meldung(flash, 'busy', I18n.t('admin.working'));
      const uebertragen = {};
      Object.entries(auswahl).forEach(([boardId, sel]) => { uebertragen[boardId] = sel.value; });
      const r = await AdminClient.deleteUser(nutzer.id, uebertragen);
      if (r.ok) {
        /* Panel + flash verschwinden gleich im Reload — Meldung geht über
           _usersFlashMsg, nicht über das lokale `flash`-Element (s. o.). */
        _usersFlashMsg = I18n.t('admin.users.msg-deleted', (r.body && r.body.pseudonym) || '');
        await renderUsers(); /* Liste neu laden — Zeile ist danach weg */
        /* S72-9: Der Fokus lag auf dem jetzt entfernten Knopf — auf die
           Meldung setzen, statt ihn ins Leere fallen zu lassen. */
        _bodyEl?.querySelector('.admin-users-flash')?.focus();
        return;
      }
      if (r.status === 409 && r.body && Array.isArray(r.body.boards) && r.body.boards.length > 0) {
        renderTransfer(r.body.boards);
        meldung(flash, 'err', I18n.t('admin.users.msg-transfer-needed'), 'status');
      } else {
        /* K-B9: eigene Texte statt der englischen Servermeldung. */
        const k = { 400: 'admin.users.error-delete-400', 404: 'admin.users.error-delete-404',
          409: 'admin.users.error-delete-409', 429: 'admin.error-429' }[r.status];
        meldung(flash, 'err', I18n.t(k || 'admin.error-server'), 'status');
        markiereKnopf(confirmBtn, 'err');
      }
      cancelBtn.disabled = false;
      auswertenAktivierung();
    });

    cancelBtn.addEventListener('click', () => {
      panel.remove();
      trigger.disabled = false;
      zeilenMeldung(abbruchHinweis(cell), 'busy', I18n.t('admin.cancelled'));
      trigger.focus(); /* S72-9: zurück zum auslösenden Knopf */
    });

    cell.appendChild(panel);
    emailInput.focus(); /* K-B11: Fokus dorthin, wo es weitergeht */
  }

  function renderError(status) { _bodyEl.appendChild(fehlerAbsatz(status)); }
  function fehlerAbsatz(status) {
    let msg;
    if (status === 403) msg = I18n.t('admin.error-403');
    else if (status === 429) msg = I18n.t('admin.error-429');
    else if (status === 503) msg = I18n.t('admin.error-503');
    else msg = I18n.t('admin.error-server');
    return el('p', { cls: 'admin-error', text: msg });
  }

  async function renderStatus() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.status();
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const data = res.body || {};
    const list = el('dl', { cls: 'admin-status-list' });
    function row(labelKey, value, opts = {}) {
      list.appendChild(el('dt', { text: I18n.t(labelKey) }));
      const dd = el('dd', { text: String(value) });
      if (opts.warn) dd.classList.add('admin-status-value--warn');
      list.appendChild(dd);
    }
    row('admin.status.version', data.version || '-');
    row('admin.status.uptime', formatUptime(data.uptime_sec || 0));
    row('admin.status.admin-count', data.admin_count ?? 0);
    row('admin.status.secrets',
      data.secrets_enabled ? I18n.t('admin.status.secrets-yes') : I18n.t('admin.status.secrets-no'));
    // Karte #570 (ADR-0129 §5): nur wenn der Server eine abweichende Basis
    // meldet (server/api/admin.go handleStatus lässt das Feld sonst weg) —
    // rein informativ, keine Bearbeitung hier (kein server_config-Weg für LLM).
    if (data.llm_openai_base_url) {
      row('admin.status.llm-openai-base-url', data.llm_openai_base_url);
    }
    /* Karte #703: Firmenmodus der KI nur ANZEIGEN — gesetzt wird er per
       Umgebungsvariable, nie im Browser. Der Server meldet Ziel (Host),
       Modelle und PII-Vorgabe, nie den Schlüssel. Ein aktueller Server sendet
       `llm_firmenmodus` immer (die PII-Vorgabe ist mindestens `nutzer`); fehlt
       das Feld, ist es ein älterer Server — dann keine Zeile. */
    if (typeof data.llm_firmenmodus === 'boolean') {
      if (data.llm_firmenmodus) {
        const modelle = Array.isArray(data.llm_modelle) ? data.llm_modelle.join(', ') : '-';
        row('admin.status.llm-firmenmodus', I18n.t('admin.status.llm-firmenmodus-an', modelle));
        if (data.llm_ziel) row('admin.status.llm-ziel', data.llm_ziel);
      } else {
        row('admin.status.llm-firmenmodus', I18n.t('admin.status.llm-firmenmodus-aus'));
      }
    }
    if (data.llm_pii) {
      row('admin.status.llm-pii', I18n.t(`admin.status.llm-pii-${data.llm_pii}`));
    }
    /* F-1213: Karte #570-Nachbarschaft — server/api/admin.go handleStatus
       liefert seit F-1204 `uhr: { pruefung: "an"|"aus", gestellt: bool,
       fehler?: string }` (server/uhr, ADR: F-1192). Ein dauerhaft ruhendes
       Frist-Löschen (Systemuhr nicht gestellt) stand bis hierhin nur in
       einer Log-Zeile, die längst rotiert sein kann. Fehlt das Feld (älterer
       Server ohne F-1204), wird nichts gerendert — kein Absturz auf
       `data.uhr` (K-1213-1). */
    const uhr = data.uhr;
    if (uhr && typeof uhr === 'object') {
      let text; let warn = false;
      if (uhr.fehler) {
        /* Kernel-Status nicht lesbar (z. B. adjtimex ohne Rechte) — heisst
           serverseitig ebenfalls "nicht gestellt": es wird nicht gelöscht,
           bis die Prüfung wieder lesbar ist. */
        text = I18n.t('admin.status.uhr-error', uhr.fehler);
        warn = true;
      } else if (uhr.pruefung === 'aus') {
        text = I18n.t('admin.status.uhr-off');
      } else if (uhr.gestellt) {
        text = I18n.t('admin.status.uhr-set');
      } else {
        text = I18n.t('admin.status.uhr-unset');
        warn = true;
      }
      row('admin.status.uhr', text, { warn });
    }
    /* Karte #684 (ADR-0134): Schalter der Ausgangsschleuse, bei „an“ samt
       Zahl der freigegebenen/nutzbaren Anbieter und der Aufbewahrung. Fehlt
       das Feld (älterer Server), keine Zeile. */
    if (data.schleuse === 'an') {
      const f = data.schleuse_anbieter_freigegeben ?? 0;
      const n = data.schleuse_anbieter_nutzbar ?? 0;
      row('admin.status.schleuse', I18n.t('admin.status.schleuse-an', f, n), { warn: f > n });
      row('admin.status.schleuse-tage', I18n.t('admin.status.schleuse-tage-wert', data.schleuse_aufbewahrung_tage ?? 30));
    } else if (data.schleuse === 'aus') {
      row('admin.status.schleuse', I18n.t('admin.status.schleuse-aus'));
    }
    zeigeSchleuseTab(data.schleuse === 'an');
    _bodyEl.appendChild(list);
  }

  /* ── Karte #684 (ADR-0134 §2): Freigabeliste der Ausgangsschleuse ─────
     Tabelle aller Einträge mit Nutzbarkeit (der Server berechnet sie gegen
     die Ausgangsliste) und ein Formular zum Anlegen/Bearbeiten. */
  let _schleuseBearbeiten = null;
  let _schleuseMeldung = '';

  async function renderSchleuse() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.listSchleuseFreigaben();
    if (_activeSection !== 'schleuse') return;
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const data = res.body || {};
    const freigaben = Array.isArray(data.freigaben) ? data.freigaben : [];
    _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.schleuse.intro') }));
    if (data.ausgaenge === 'aus') {
      _bodyEl.appendChild(el('p', { cls: 'admin-hint', text: I18n.t('admin.schleuse.ausgaenge-aus') }));
    }
    const flash = el('div', { cls: 'admin-flash', attrs: { role: 'status' } });
    if (_schleuseMeldung) { meldung(flash, 'ok', _schleuseMeldung); _schleuseMeldung = ''; }
    _bodyEl.appendChild(flash);
    if (!freigaben.length) {
      _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.schleuse.leer') }));
    } else {
      const table = el('table', { cls: 'admin-audit-table admin-schleuse-table' });
      const trh = el('tr');
      ['admin.schleuse.col-anbieter', 'admin.schleuse.col-host', 'admin.schleuse.col-modelle',
        'admin.schleuse.col-vertrag', 'admin.schleuse.col-vier', 'admin.schleuse.col-status', 'admin.schleuse.col-aktion']
        .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
      const thead = el('thead'); thead.appendChild(trh); table.appendChild(thead);
      const tbody = el('tbody');
      const jn = (b) => I18n.t(b ? 'admin.schleuse.ja' : 'admin.schleuse.nein');
      for (const f of freigaben) {
        const tr = el('tr');
        tr.dataset.id = f.id;
        const name = f.bezeichnung ? `${f.bezeichnung} (${f.anbieter})` : f.anbieter;
        [name, f.host || '', (f.modelle || []).join(', '), jn(f.vertragsform_bestaetigt), jn(f.vier_augen)]
          .forEach(v => tr.appendChild(el('td', { text: v })));
        const status = f.nutzbar
          ? I18n.t('admin.schleuse.nutzbar')
          : (f.gruende || []).map(g => I18n.t(`admin.schleuse.grund-${g}`)).join(', ');
        const td = el('td');
        td.appendChild(el('span', { text: status, cls: f.nutzbar ? '' : 'admin-status-value--warn' }));
        tr.appendChild(td);
        const aktion = el('td');
        const bearb = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.schleuse.btn-bearbeiten') });
        bearb.addEventListener('click', () => { _schleuseBearbeiten = f; renderBody(); });
        const loe = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.schleuse.btn-loeschen') });
        const msg = el('span', { cls: 'admin-row-msg' });
        loe.addEventListener('click', async () => {
          if (!window.confirm(I18n.t('admin.schleuse.confirm-loeschen', name))) return;
          loe.disabled = true;
          zeilenMeldung(msg, 'busy', I18n.t('admin.working'));
          const r = await AdminClient.deleteSchleuseFreigabe(f.id);
          if (r.ok) {
            if (_schleuseBearbeiten?.id === f.id) _schleuseBearbeiten = null;
            _schleuseMeldung = I18n.t('admin.schleuse.geloescht');
            renderBody();
            return;
          }
          loe.disabled = false;
          zeilenMeldung(msg, 'err', serverFehler(r.status));
        });
        aktion.appendChild(bearb); aktion.appendChild(loe); aktion.appendChild(msg);
        tr.appendChild(aktion);
        tbody.appendChild(tr);
      }
      table.appendChild(tbody);
      _bodyEl.appendChild(table);
    }
    _bodyEl.appendChild(renderSchleuseForm(Array.isArray(data.anbieter_arten) && data.anbieter_arten.length
      ? data.anbieter_arten : ['openai-kompatibel', 'anthropic', 'mistral', 'google']));
  }

  function renderSchleuseForm(arten) {
    const f = _schleuseBearbeiten;
    const box = el('div', { cls: 'admin-schleuse-form' });
    box.appendChild(el('h3', { cls: 'admin-section-title',
      text: I18n.t(f ? 'admin.schleuse.form-bearbeiten' : 'admin.schleuse.form-neu') }));
    const form = el('div', { cls: 'admin-smtp-form' });
    function feld(labelKey, input) {
      const id = 'admin-schleuse-' + labelKey.split('.').pop();
      input.id = id;
      const lbl = el('label', { cls: 'admin-form-label', text: I18n.t(labelKey), attrs: { for: id } });
      form.appendChild(lbl);
      form.appendChild(input);
      return input;
    }
    const art = el('select', { cls: 'admin-form-input' });
    arten.forEach(a => { const o = el('option', { text: a }); o.value = a; art.appendChild(o); });
    art.value = f?.anbieter || arten[0];
    feld('admin.schleuse.col-anbieter', art);
    const bez = feld('admin.schleuse.bezeichnung', el('input', { cls: 'admin-form-input', attrs: { maxlength: '120' } }));
    bez.value = f?.bezeichnung || '';
    const basis = feld('admin.schleuse.basis-url', el('input', { cls: 'admin-form-input',
      attrs: { placeholder: 'https://…', spellcheck: 'false', autocomplete: 'off' } }));
    basis.value = f?.basis_url || '';
    const modelle = feld('admin.schleuse.modelle', el('input', { cls: 'admin-form-input', attrs: { spellcheck: 'false' } }));
    modelle.value = (f?.modelle || []).join(', ');
    function haken(labelKey, an) {
      const cb = el('input', { attrs: { type: 'checkbox' } });
      cb.checked = !!an;
      return feld(labelKey, cb);
    }
    const vertrag = haken('admin.schleuse.vertragsform', f?.vertragsform_bestaetigt);
    const vier = haken('admin.schleuse.vier-augen', f?.vier_augen);
    const aktiv = haken('admin.schleuse.aktiv', f ? f.aktiv : true);
    box.appendChild(form);
    const actions = el('div', { cls: 'admin-actions' });
    const speichern = el('button', { cls: 'btn btn-primary admin-btn admin-btn--primary', text: I18n.t('admin.schleuse.btn-speichern') });
    const flash = el('div', { cls: 'admin-flash', attrs: { role: 'status' } });
    speichern.addEventListener('click', async () => {
      const body = {
        anbieter: art.value,
        bezeichnung: bez.value.trim(),
        basis_url: basis.value.trim(),
        modelle: modelle.value.split(',').map(x => x.trim()).filter(Boolean),
        vertragsform_bestaetigt: vertrag.checked,
        vier_augen: vier.checked,
        aktiv: aktiv.checked,
      };
      starte(speichern, flash);
      const r = f ? await AdminClient.updateSchleuseFreigabe(f.id, body)
                  : await AdminClient.createSchleuseFreigabe(body);
      if (r.ok) {
        _schleuseBearbeiten = null;
        _schleuseMeldung = I18n.t('admin.schleuse.gespeichert');
        renderBody();
        return;
      }
      const text = r.status === 400 ? I18n.t('admin.schleuse.fehler-400', r.body?.error || '')
        : r.status === 409 ? I18n.t('admin.schleuse.fehler-409') : serverFehler(r.status);
      beende(speichern, flash, 'err', text);
    });
    actions.appendChild(speichern);
    if (f) {
      const neu = el('button', { cls: 'btn admin-btn', text: I18n.t('admin.schleuse.btn-neu') });
      neu.addEventListener('click', () => { _schleuseBearbeiten = null; renderBody(); });
      actions.appendChild(neu);
    }
    box.appendChild(actions);
    box.appendChild(flash);
    return box;
  }

  async function renderAudit() {
    _bodyEl.appendChild(el('p', { cls: 'admin-loading', text: I18n.t('admin.loading') }));
    const res = await AdminClient.audit({ limit: 100 });
    _bodyEl.innerHTML = '';
    if (!res.ok) { renderError(res.status); return; }
    const entries = (res.body && res.body.entries) || [];
    if (entries.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'admin-empty', text: I18n.t('admin.audit.empty') }));
      return;
    }
    /* #1109: eigene Klasse admin-audit-log — der Umbruch gilt nur hier, nicht
       für die übrigen Tabellen mit admin-audit-table (Kritik 10.11., F-A). */
    const table = el('table', { cls: 'admin-audit-table admin-audit-log' });
    const thead = el('thead');
    const trh = el('tr');
    ['admin.audit.ts','admin.audit.action','admin.audit.key','admin.audit.user','admin.audit.value']
      .forEach(k => trh.appendChild(el('th', { text: I18n.t(k) })));
    thead.appendChild(trh);
    table.appendChild(thead);
    const tbody = el('tbody');
    entries.forEach(e => {
      const tr = el('tr');
      /* #1109 (F-2942): Zeit kurz in Ortszeit, die volle Angabe im title —
         die Nanosekunden nahmen der Spalte „Value“ den Platz. */
      tr.appendChild(el('td', { cls: 'admin-audit-zeit', text: auditZeit(e.ts), attrs: e.ts ? { title: e.ts } : {} }));
      tr.appendChild(el('td', { text: e.action || '' }));
      tr.appendChild(el('td', { text: e.key || '' }));
      tr.appendChild(el('td', { text: e.user_id || '' }));
      const v = e.new_value_redacted || e.old_value_redacted || '';
      tr.appendChild(el('td', { cls: 'admin-audit-wert', text: v }));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    _bodyEl.appendChild(table);
  }

  /* #1109: RFC3339 → „YYYY-MM-DD HH:MM:SS“ in Ortszeit; Unlesbares bleibt roh. */
  function auditZeit(ts) {
    const ms = Date.parse(ts || '');
    if (!Number.isFinite(ms)) return ts || '';
    const d = new Date(ms);
    const z = (n) => String(n).padStart(2, '0');
    /* F-2946: Zone ausweisen (Ortszeit mit Abstand zu UTC, z. B. +01:00). */
    const off = -d.getTimezoneOffset();
    const abs = Math.abs(off);
    const zone = `${off < 0 ? '-' : '+'}${z(Math.floor(abs / 60))}:${z(abs % 60)}`;
    return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())} ${z(d.getHours())}:${z(d.getMinutes())}:${z(d.getSeconds())} ${zone}`;
  }

  function formatUptime(sec) {
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d) return `${d}d ${h}h ${m}m`;
    if (h) return `${h}h ${m}m`;
    return `${m}m`;
  }

  function ensureStyles() {
    if (document.getElementById('admin-dialog-style')) return;
    const css = `
/* Karte #728: Nur Design-Variablen aus src/style.css (--surface, --text,
   --border, --accent, …) — keine festen Farben, damit Hell/Dunkel greift.
   Einzige Ausnahme --admin-err: das Theme kennt keine Fehlerfarbe, und das
   feste #dc2626 aus .btn-danger erreicht auf dunklem Grund kein AA 4,5:1;
   deshalb hell #b91c1c (AA auf --surface) und dunkel #f87171 (AA auf dunkel).
   Knöpfe tragen zusätzlich .btn/.btn-primary aus style.css (Markup). */
.admin-panel { --admin-err: #b91c1c; }
[data-theme="dark"] .admin-panel { --admin-err: #f87171; }
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) .admin-panel { --admin-err: #f87171; }
}
.admin-overlay { z-index: 1100; }
.admin-panel { color: var(--text); width: min(900px, 92vw); max-height: 88vh; }
.admin-tabbar { display: flex; gap: 4px; padding: 8px 12px 0; border-bottom: 1px solid var(--border); flex-wrap: wrap; flex-shrink: 0; }
.admin-tab { background: transparent; border: 1px solid transparent; padding: 6px 10px; border-radius: 6px 6px 0 0; cursor: pointer; font-size: 0.9rem; color: var(--text-muted); }
.admin-tab:hover { color: var(--text); }
.admin-tab--active { background: var(--surface-2); border-color: var(--border); border-bottom-color: var(--surface-2); color: var(--text); }
.admin-body { padding: 16px; overflow: auto; }
.admin-placeholder { color: var(--text-muted); font-style: italic; }
.admin-status-list { display: grid; grid-template-columns: max-content 1fr; gap: 6px 16px; margin: 0; }
.admin-status-list dt { color: var(--text-muted); font-weight: 600; }
.admin-status-list dd { margin: 0; }
/* F-1213: Uhr-Zustand "nicht gestellt"/Fehler hervorheben — bewusst KEINE
   neue Farbe (Kontrast-Wächter F-454), sondern das bestehende Gelb-Paar aus
   src/style.css (--prio-high-bg/--prio-high-fg), das dort bereits AA 4,5:1
   erfüllt und hell/dunkel führt. */
.admin-status-value--warn { color: var(--prio-high-fg); background: var(--prio-high-bg); padding: 1px 6px; border-radius: 4px; font-weight: 600; display: inline-block; }
.admin-audit-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
.admin-audit-table th, .admin-audit-table td { padding: 4px 8px; border-bottom: 1px solid var(--border); text-align: left; vertical-align: top; }
.admin-audit-log td.admin-audit-wert { overflow-wrap: anywhere; }
.admin-audit-log td.admin-audit-zeit { white-space: nowrap; }
.admin-error { color: var(--admin-err); }
.admin-empty, .admin-loading { color: var(--text-muted); }
.admin-smtp-form { display: grid; grid-template-columns: max-content 1fr; gap: 8px 16px; margin-bottom: 12px; }
.admin-form-row { display: contents; }
.admin-form-label { padding: 6px 0; color: var(--text-muted); font-weight: 600; }
.admin-form-input { padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--surface-2); color: var(--text); font-family: monospace; font-size: 0.9rem; }
.admin-form-input:focus { outline: 2px solid var(--accent); outline-offset: -1px; border-color: transparent; }
.admin-actions { display: flex; gap: 8px; margin-bottom: 8px; }
.admin-flash { font-size: 0.85rem; min-height: 1em; padding: 4px 0; }
.admin-flash--busy { color: var(--text-muted); font-style: italic; }
.admin-flash--ok { color: var(--prio-low-fg); font-weight: 600; }
.admin-flash--err { color: var(--admin-err); font-weight: 600; }
.admin-btn--ok { background: var(--prio-low-bg); border-color: var(--prio-low-fg); color: var(--prio-low-fg); }
.admin-btn--err { background: var(--surface-2); border-color: var(--admin-err); color: var(--admin-err); }
.admin-enc-box { margin: 0 0 14px; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface-2); }
.admin-enc-box--warn { border-color: var(--prio-high-fg); background: var(--prio-high-bg); color: var(--prio-high-fg); }
.admin-enc-switch { display: flex; gap: 8px; align-items: center; font-weight: 600; }
.admin-enc-key { margin: 6px 0 0; font-size: 0.85rem; }
.admin-enc-key--missing { color: var(--admin-err); font-weight: 600; }
.admin-enc-box--warn .admin-enc-key--missing { color: var(--prio-high-fg); }
.admin-enc-warn { margin: 6px 0 0; font-size: 0.85rem; color: var(--prio-high-fg); font-weight: 600; }
.admin-hint { margin-top: 16px; padding: 8px 10px; background: var(--accent-dim); border-left: 3px solid var(--accent); font-size: 0.85rem; color: var(--text); }
.admin-form-textarea { resize: vertical; font-family: monospace; font-size: 0.85rem; }
.admin-autoreply-preview { margin-top: 8px; padding: 8px; background: var(--surface-2); border: 1px solid var(--border); border-radius: 6px; font-size: 0.82rem; white-space: pre-wrap; max-height: 200px; overflow: auto; }
.admin-section-title { font-size: 1rem; margin: 0 0 12px; color: var(--text); }
.admin-users-table td { vertical-align: middle; }
.admin-users-row-error { color: var(--admin-err); font-size: 0.78rem; margin-left: 6px; }
.admin-agents-switch { display: inline-flex; align-items: center; gap: 6px; }
.admin-agents-switch input[type=checkbox] { accent-color: var(--accent-btn); width: 16px; height: 16px; }
.admin-agents-state { min-width: 2.2em; padding: 1px 6px; border-radius: 4px; border: 1px solid var(--border); background: var(--btn-bg); color: var(--text); font-size: 0.78rem; font-weight: 600; text-align: center; }
.admin-agents-state--on { border-color: var(--prio-low-fg); background: var(--prio-low-bg); color: var(--prio-low-fg); }
.admin-agents-id { color: var(--text-muted); font-family: monospace; font-size: 0.8rem; }
.admin-agents-herkunft-fehlt { color: var(--prio-high-fg); font-weight: 600; }
.admin-agents-herkunft { overflow-wrap: anywhere; min-width: 8em; }
.admin-agents-new { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 4px; }
.admin-agents-token { width: 100%; box-sizing: border-box; margin-bottom: 6px; }
.admin-row-msg { font-size: 0.78rem; margin-left: 6px; }
.admin-row-msg--ok { color: var(--prio-low-fg); font-weight: 600; }
.admin-row-msg--err { color: var(--admin-err); font-weight: 600; }
.admin-row-msg--busy { color: var(--text-muted); font-style: italic; }
.admin-users-confirm { margin-top: 8px; padding: 10px; background: var(--surface-2); border: 1px solid var(--admin-err); border-radius: var(--radius); min-width: 260px; }
.admin-users-confirm-warning { margin: 0 0 8px; color: var(--admin-err); font-size: 0.85rem; }
.admin-users-transfer { margin-top: 8px; }
.admin-konten { margin-bottom: 20px; }
.admin-konten-form { display: flex; flex-direction: column; gap: 6px; align-items: flex-start; }
.admin-konten-form textarea { width: 100%; box-sizing: border-box; }
.admin-konten-flash:empty { display: none; }
.admin-konten-links { margin: 10px 0; }
.admin-konten-link { margin: 8px 0; }
.admin-konten-link .admin-agents-token { width: 70%; max-width: 100%; margin-right: 6px; }
.admin-konten-link-info { margin-top: 4px; }
.admin-konten-gemeldet { margin-top: 2px; font-size: 0.8rem; color: var(--text-muted); overflow-wrap: anywhere; }
.admin-konten-gemeldet--abweichend { color: var(--admin-err); font-weight: 600; border-left: 3px solid var(--admin-err); padding-left: 6px; }
.admin-konten-zustand { padding: 1px 6px; border-radius: 4px; border: 1px solid var(--border); font-size: 0.78rem; font-weight: 600; }
.admin-konten-zustand--pending { border-color: var(--prio-high-fg); color: var(--prio-high-fg); }
.admin-konten-zustand--blocked { border-color: var(--admin-err); color: var(--admin-err); }
.admin-konten-zustand--active { border-color: var(--prio-low-fg); color: var(--prio-low-fg); }
.admin-konten-btn { margin: 0 4px 4px 0; }
`;
    const style = document.createElement('style');
    style.id = 'admin-dialog-style';
    style.textContent = css;
    document.head.appendChild(style);
  }

  function open() {
    ensureStyles();
    ensureModal();
    _activeSection = 'status';
    _modal.open();
    /* #327: Initial-Focus erst NACH dem (asynchronen) Sektions-Rendering —
       vorher existieren die Formularfelder der Sektion noch nicht im DOM. */
    Promise.resolve(selectSection('status')).then(() => _modal.focusAfterRender());
  }

  function close() {
    /* Karte #670: Ein angezeigtes Agenten-Token geht mit dem Dialog — aus dem
       DOM und aus dem Speicher; ein noch laufendes Anlegen zeigt es nicht mehr. */
    _agentsEinmal = null;
    _agentsLauf++;
    _kontenLauf++; /* #1078: eingeblendete Einladungslinks gehen mit dem Dialog */
    _boardpaarEinmal = null; /* #788, wie V-2 */
    _boardpaarMeldung = null; /* #831 */
    _boardpaarAufraeumHinweis = null; /* Verify A-1 */
    _boardpaarLauf++;
    _backupLauf++; /* K-1: laufende Status-Abfrage der Sicherungs-Sektion beenden */
    _schleuseBearbeiten = null;
    if (_bodyEl) _bodyEl.innerHTML = '';
    _modal?.close();
  }

  AdminDialog = { open, close };
}

export { AdminDialog };
