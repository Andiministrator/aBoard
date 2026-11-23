/* share-section.js — Board-Freigabe („Freigabe“) im Dialog Board-Einstellungen
 * (Karte #702; zuvor Sidebar-Sektion „Board teilen“, Karte #199, ADR-0059).
 *
 * Nur Server-Build (IS_SERVER_BUILD): im Solo-Template verschwindet das Modul
 * samt CSS-String per DCE (ADR-0064/0114). Der Dialog-Aufruf in
 * configdialog.js ist zusätzlich auf den Server-Modus beschränkt.
 *
 * WICHTIG: Freigaben greifen SOFORT am Server — der Rest des Dialogs hat
 * Speichern/Abbrechen, Abbrechen macht eine Freigabe nicht rückgängig. Die
 * Sektion sagt das ausdrücklich (share.sofort-hinweis).
 *
 * Rollen-Weiche: GET /boards/{id}/acl ist seit F-261 owner-only (sonst 403).
 * Die eigene Rolle liefert listKnownBoards() (GET /boards, Feld `role`).
 * Editor/Viewer sehen nur einen Satz und lösen KEINEN ACL-Abruf aus. Fehlt
 * `role` (Legacy-Bypass ohne Rollen) oder ist die Rolle nicht ermittelbar,
 * gilt das alte Verhalten: Liste laden, Fehler wie bisher anzeigen.
 *
 * Der Rollen-Check ist ein UX-Filter, keine Sicherheitsgrenze — der Server
 * prüft bei jedem Endpunkt selbst.
 */

import { I18n }          from './i18n.js';
import { StoragePlugin } from './storage-plugin.js';
import { SchleuseSchluesselDialog } from './schleuse-schluessel.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let ShareSection = null;
/* ADR-0075: test-only Zugriff auf buildShareContent. In Nicht-Server-Builds null. */
let __shareTest = null;

if (IS_SERVER_BUILD) {

let _stylesInjected = false;
function _ensureShareStyles() {
  if (_stylesInjected) return;
  _stylesInjected = true;
  const style = document.createElement('style');
  style.setAttribute('data-aboard', 'share-section');
  style.textContent = '.config-share-section{margin-bottom:14px}.share-sofort{margin:4px 0 8px;padding:6px 10px;font-size:12px;font-weight:600;color:var(--text);background:var(--surface-2);border-left:3px solid var(--accent,#0B5D73);border-radius:4px}.share-container{padding:0 0 8px}.share-muted{font-size:12px;line-height:1.4;color:var(--text-muted);font-style:italic;padding:4px 0}.share-rolle{font-size:13px;color:var(--text);padding:4px 0}.share-error{font-size:12px;color:var(--danger,#dc2626);padding:4px 0 6px}.share-error--hidden{display:none}.share-list{margin-bottom:8px}.share-acl-row{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:6px 0}.share-acl-info{display:flex;flex-direction:column;gap:1px;min-width:0;flex:1}.share-acl-email{font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.share-acl-role{font-size:11px;color:var(--text-muted);text-transform:capitalize}.share-remove-btn{flex-shrink:0;font-size:14px;line-height:1;padding:2px 7px;color:var(--text-muted);background:transparent;border:1px solid var(--border,#ddd);border-radius:4px;cursor:pointer}.share-remove-btn:hover{color:var(--danger,#dc2626);border-color:var(--danger,#dc2626)}.share-form{display:flex;gap:6px;flex-wrap:wrap;padding-top:6px;border-top:1px solid var(--border,#ddd)}.share-user-select{flex:1;min-width:0;font-size:13px;padding:4px 8px;border:1px solid var(--border);border-radius:var(--radius);background:var(--surface);color:var(--text);cursor:pointer}.share-user-select:focus{outline:none;border-color:var(--accent,#0B5D73)}.share-user-select:disabled{opacity:.6;cursor:not-allowed}.share-role-select{font-size:13px;padding:4px 6px;border:1px solid var(--border);border-radius:var(--radius);background:var(--surface);color:var(--text);cursor:pointer}.config-share-section .sls-sperre{display:block;padding:6px 0;cursor:default}';
  document.head.appendChild(style);
}

function el(tag, opts = {}) {
  const e = document.createElement(tag);
  if (opts.cls)  e.className = opts.cls;
  if (opts.text != null) e.textContent = opts.text;
  if (opts.type) e.setAttribute('type', opts.type);
  return e;
}

  function buildShareContent(boardId, rolle) {
    const container = el('div', { cls: 'share-container' });

    /* Lade-Zustand */
    const loadingRow = el('div', { cls: 'share-muted',
                                    text: I18n.t('share.loading') });
    container.appendChild(loadingRow);

    /* Fehlermeldung (initial verborgen) */
    const errorRow = el('div', { cls: 'share-error share-error--hidden' });
    container.appendChild(errorRow);

    /* ACL-Liste */
    const listEl = el('div', { cls: 'share-list' });
    container.appendChild(listEl);

    /* Formular: Bekannte-Nutzer-Picker + Rolle + Button.
       ADR-0075 (revidiert ADR-0066 §3): reiner Picker — KEIN Freitext-Invite.
       F-308: ohne Authelia-Selbstregistrierung (geschlossenes Team) führt ein
       Freitext-Invite an eine unbekannte Email ins Leere. Der Picker wird
       ausschließlich aus GET /known-users befüllt (loadKnownUsers). */
    const form = el('div', { cls: 'share-form' });
    const userSelect = el('select', { cls: 'share-user-select' });
    userSelect.setAttribute('aria-label', I18n.t('share.picker-label'));
    /* Platzhalter-Option (disabled+selected) — erzwingt bewusste Auswahl. */
    const placeholderOpt = document.createElement('option');
    placeholderOpt.value = '';
    placeholderOpt.textContent = I18n.t('share.picker-placeholder');
    placeholderOpt.disabled = true;
    placeholderOpt.selected = true;
    userSelect.appendChild(placeholderOpt);

    const roleSelect = el('select', { cls: 'share-role-select' });
    roleSelect.setAttribute('aria-label', I18n.t('share.section-label'));
    [
      ['owner',  I18n.t('share.role-owner')],
      ['editor', I18n.t('share.role-editor')],
      ['viewer', I18n.t('share.role-viewer')],
    ].forEach(([val, label]) => {
      const opt = document.createElement('option');
      opt.value = val;
      opt.textContent = label;
      if (val === 'viewer') opt.selected = true;
      roleSelect.appendChild(opt);
    });

    const addBtn = el('button', { cls: 'btn btn-sm btn-primary', text: I18n.t('share.add-btn'),
                                    type: 'button' });
    form.appendChild(userSelect);
    form.appendChild(roleSelect);
    form.appendChild(addBtn);
    container.appendChild(form);

    /* ADR-0075 §2 / F-308: den Edge-Case sichtbar benennen — extern
       provisionierte, aber nie eingeloggte Personen erscheinen erst nach
       ihrem Erst-Login in der wählbaren Menge. */
    const pickerHint = el('div', { cls: 'share-muted share-picker-hint',
                                   text: I18n.t('share.picker-hint') });
    container.appendChild(pickerHint);

    /* Plugin-Referenz für ACL-Calls */
    const plugin = StoragePlugin.getActivePlugin();

    /* ADR-0066 §3 (Karte #224): bekannte Nutzermenge — Set für O(1)-Lookup. */
    let knownEmailSet = new Set();
    /* ADR-0132: vom Betreiber erlaubte Domains (ABOARD_ACL_DOMAINS). null =
       noch nicht geladen — dann kein aktiv/inaktiv-Urteil über Domain-Einträge. */
    let erlaubteDomains = null;

    /* ADR-0132: Ein ACL-Schlüssel "@<domain>" ist eine Domain-Freigabe. */
    const istDomain = (email) => typeof email === 'string' && email.startsWith('@');
    /* Anzeigename eines ACL-Schlüssels — Domain-Einträge als „Alle mit @…“. */
    const anzeige = (email) => (istDomain(email) ? I18n.t('share.domain-entry', email.slice(1)) : email);
    /* F-1515: Trägt eine aktive Domain-Freigabe dieses Boards die Person weiter? */
    function domainTraegt(email) {
      if (istDomain(email) || !erlaubteDomains) return null;
      const d = email.slice(email.lastIndexOf('@') + 1).toLowerCase();
      return erlaubteDomains.has(d) && lastEntries.some(e => e.email === `@${d}`) ? d : null;
    }

    /* ADR-0132 §3: Eine Domain ist nie owner — die Option verschwindet nicht,
       sie wird gesperrt, damit die Liste ruhig bleibt. */
    function rolleFuerAuswahl() {
      const ownerOpt = roleSelect.querySelector('option[value="owner"]');
      const domain = istDomain(userSelect.value);
      if (ownerOpt) ownerOpt.disabled = domain;
      if (domain && roleSelect.value === 'owner') roleSelect.value = 'viewer';
    }
    userSelect.addEventListener('change', rolleFuerAuswahl);

    function showError(msg) {
      errorRow.textContent = msg;
      errorRow.classList.remove('share-error--hidden');
    }
    function clearError() {
      errorRow.textContent = '';
      errorRow.classList.add('share-error--hidden');
    }

    function rolLabel(role) {
      return I18n.t(`share.role-${role}`) || role;
    }

    /* ADR-0066 §3 (Karte #224): ACL-Eintrag mit aktiv/ausstehend-Status rendern.
       Status wird aus der knownEmailSet abgeleitet: in users → aktiv, sonst ausstehend.
       Falls knownEmailSet noch nicht geladen → kein Status-Badge (graceful). */
    function statusBadge(email) {
      if (istDomain(email)) {
        if (erlaubteDomains === null) return null;
        const status = erlaubteDomains.has(email.slice(1)) ? 'domain' : 'inaktiv';
        return el('span', { cls: `share-acl-status share-acl-status--${status}`,
                            text: I18n.t(`share.status-${status}`) });
      }
      if (knownEmailSet.size === 0) return null;
      const normalizedEmail = email.trim().toLowerCase();
      const status = knownEmailSet.has(normalizedEmail) ? 'aktiv' : 'ausstehend';
      const badge = el('span', {
        cls: `share-acl-status share-acl-status--${status}`,
        text: I18n.t(`share.status-${status}`),
      });
      return badge;
    }

    let lastEntries = [];
    /* F-1516: erst nach der ersten Antwort von aclList darf loadKnownUsers die
       Liste nachziehen — sonst stünde „Noch niemand hat Zugriff“ neben „Wird
       geladen…“. */
    let aclGeladen = false;
    function renderList(entries) {
      lastEntries = entries || [];
      listEl.textContent = '';
      if (!entries || entries.length === 0) {
        /* KO-12a/VO-6: aclList meldet bei 403 und bei Fehlern (5xx, Netz)
           ebenfalls [] — und der Owner steht selbst in der ACL, eine wirklich
           leere Liste gibt es also praktisch nicht. Immer neutral formulieren,
           nie „Noch niemand hat Zugriff“. */
        const none = el('div', { cls: 'share-muted', text: I18n.t('share.empty-unsicher') });
        listEl.appendChild(none);
        return;
      }
      /* Karte #352: letzter Owner darf serverseitig nicht entfernt werden
         (server/api/boards.go enforceOwnerRemains). UI spiegelt das: kein
         klickbarer `×`, dafür aria-disabled + Tooltip mit der Begründung.
         Wir RENDERN den Button weiter (visuelle Stabilität — Spaltenbreite
         bleibt konstant), aber er ist nicht aktivierbar. */
      const ownerCount = entries.filter(e => e.role === 'owner').length;
      entries.forEach(entry => {
        const row = el('div', { cls: 'share-acl-row' });

        const info = el('span', { cls: 'share-acl-info' });
        const emailSpan = el('span', { cls: 'share-acl-email',
          text: anzeige(entry.email) });
        const roleSpan  = el('span', { cls: 'share-acl-role',  text: rolLabel(entry.role) });
        info.appendChild(emailSpan);
        info.appendChild(roleSpan);
        /* ADR-0066 §3: aktiv/ausstehend-Marker */
        const badge = statusBadge(entry.email);
        if (badge) info.appendChild(badge);

        const isLastOwner = entry.role === 'owner' && ownerCount === 1;
        const removeBtn = el('button', { cls: 'btn btn-sm share-remove-btn',
                                          text: '×', type: 'button' });
        if (isLastOwner) {
          removeBtn.disabled = true;
          removeBtn.title = I18n.t('share.error-409');
          removeBtn.setAttribute('aria-label', I18n.t('share.error-409'));
        } else {
          removeBtn.title = I18n.t('share.remove-btn');
          removeBtn.setAttribute('aria-label', `${I18n.t('share.remove-btn')}: ${anzeige(entry.email)}`);
          removeBtn.addEventListener('click', async () => {
            /* F-1515: Bleibt die Person über eine Domain-Freigabe drin, sagt der
               Dialog das — sonst glaubt der Owner, sie sei draussen. */
            const d = domainTraegt(entry.email);
            const frage = d
              ? I18n.t('share.remove-confirm-domain', entry.email, d)
              : I18n.t('share.remove-confirm', anzeige(entry.email));
            if (!window.confirm(frage)) return;
            clearError();
            const r = await plugin.aclDelete?.(boardId, entry.email) ?? { ok: false, status: 0 };
            if (r.ok) {
              await reload();
            } else if (r.status === 409) {
              showError(I18n.t('share.error-409'));
            } else if (r.status === 403) {
              showError(I18n.t('share.error-403'));
            } else {
              showError(I18n.t('share.error-server'));
            }
          });
        }

        row.appendChild(info);
        row.appendChild(removeBtn);
        listEl.appendChild(row);
      });
    }

    /* ADR-0075 (revidiert ADR-0066 §3): bekannte Nutzermenge laden + Picker
       befüllen. Es gibt KEINEN Freitext-Fallback mehr (F-308) — ohne ladbare
       Menge bleibt der Picker leer + deaktiviert, der Hinweis erklärt es. */
    function disablePicker() {
      placeholderOpt.textContent = I18n.t('share.picker-empty');
      userSelect.disabled = true;
      addBtn.disabled = true;
    }
    async function loadKnownUsers() {
      try {
        const result = await plugin.knownUsers?.() ?? { ok: false, users: [] };
        if (!result.ok) { disablePicker(); return; }
        knownEmailSet = new Set(result.users.map(u => u.email.toLowerCase()));
        erlaubteDomains = new Set((result.domains ?? []).map(d => String(d).toLowerCase()));
        /* Picker-Optionen setzen (email als value, "Name (email)" als Label). */
        result.users.forEach(u => {
          const opt = document.createElement('option');
          opt.value = u.email;
          opt.textContent = u.name ? `${u.name} (${u.email})` : u.email;
          userSelect.appendChild(opt);
        });
        /* ADR-0132 §7: je erlaubter Domain eine Zeile „Alle mit @<domain>“ —
           wählbar ist nur, was der Betreiber freigegeben hat (kein Freitext). */
        erlaubteDomains.forEach(d => {
          const opt = document.createElement('option');
          opt.value = `@${d}`;
          opt.textContent = I18n.t('share.domain-option', d);
          userSelect.appendChild(opt);
        });
        /* F-308-Edge: niemand wählbar (noch kein Erst-Login) → deaktivieren. */
        if (result.users.length === 0 && erlaubteDomains.size === 0) disablePicker();
        /* Die ACL-Liste kann vor den Domains geladen sein — Marker nachziehen. */
        if (aclGeladen && (erlaubteDomains.size > 0 || lastEntries.some(e => istDomain(e.email)))) renderList(lastEntries);
      } catch {
        disablePicker();
      }
    }

    async function reload() {
      try {
        const entries = await plugin.aclList?.(boardId) ?? [];
        loadingRow.remove();
        aclGeladen = true;
        renderList(entries);
      } catch {
        loadingRow.textContent = I18n.t('share.error-server');
      }
    }

    addBtn.addEventListener('click', async () => {
      clearError();
      /* ADR-0075: Wert kommt ausschließlich aus dem Bekannte-Nutzer-Picker —
         keine Freitext-Eingabe, keine „unbekannt?"-Bestätigung mehr (F-308). */
      const email = userSelect.value;
      const role  = roleSelect.value;
      if (!email) { showError(I18n.t('share.error-no-selection')); return; }
      addBtn.disabled = true;
      const r = await plugin.aclSet?.(boardId, email, role) ?? { ok: false, status: 0 };
      addBtn.disabled = false;
      if (r.ok) {
        userSelect.selectedIndex = 0;
        rolleFuerAuswahl();
        await reload();
      } else if (r.status === 409) {
        showError(I18n.t('share.error-409'));
      } else if (r.status === 403) {
        showError(I18n.t('share.error-403'));
      } else if (r.status === 400 && istDomain(email)) {
        /* F-1515: Domain inzwischen nicht mehr erlaubt (oder Service-Board). */
        showError(I18n.t('share.error-400-domain'));
      } else {
        showError(I18n.t('share.error-server'));
      }
    });

    /* Initiales Laden: ACL-Liste + bekannte Nutzermenge parallel. */
    /* VO-7: Die ACL-Antwort als Promise am Container — aktualisieren() hält
       aria-busy, bis sie da ist (reload() wirft nie). */
    container.aclGeladen = reload();
    loadKnownUsers();

    return container;
  }

/* Eigene Rolle auf dem Board: 'owner' | 'editor' | 'viewer' | undefined.
   undefined = Legacy-Bypass ohne Rollen oder nicht ermittelbar → wie bisher. */
async function eigeneRolle(boardId) {
  try {
    const boards = await StoragePlugin.getActivePlugin().listKnownBoards?.();
    return (boards ?? []).find(b => b.id === boardId)?.role;
  } catch { return undefined; }
}

/* Baut die Sektion; `aktualisieren()` lädt den Inhalt neu (bei jedem Öffnen
   des Dialogs — das Board kann seit dem letzten Öffnen gewechselt haben). */
function build(boardId) {
  const section = el('section', { cls: 'config-share-section' });
  section.setAttribute('aria-labelledby', 'cfg-share-heading');
  const heading = el('div', { cls: 'config-separator', text: I18n.t('share.section-title') });
  heading.id = 'cfg-share-heading';
  heading.setAttribute('role', 'heading');
  heading.setAttribute('aria-level', '3');
  section.appendChild(heading);
  const hinweis = el('div', { cls: 'share-sofort', text: I18n.t('share.sofort-hinweis') });
  hinweis.setAttribute('role', 'note');
  section.appendChild(hinweis);
  /* KO-12c: Der Inhalt wechselt nach dem Laden (Liste / Rollenzeile) — als
     Live-Region melden und währenddessen als „beschäftigt“ kennzeichnen. */
  const body = el('div', { cls: 'share-body' });
  body.setAttribute('aria-live', 'polite');
  section.appendChild(body);
  _ensureShareStyles();

  let lauf = 0;
  async function aktualisieren(neueBoardId = boardId) {
    const mein = ++lauf;
    body.setAttribute('aria-busy', 'true');
    hinweis.hidden = false;
    body.textContent = '';
    body.appendChild(el('div', { cls: 'share-muted', text: I18n.t('share.loading') }));
    const rolle = await eigeneRolle(neueBoardId);
    if (mein !== lauf) return; /* neuerer Aufruf hat übernommen */
    body.textContent = '';
    /* Eigener Container je Lauf: ein verspäteter Sperre-Schalter eines
       überholten Laufs hängt dann an einem abgelösten Knoten (isConnected). */
    const inhalt = el('div', { cls: 'share-inhalt' });
    body.appendChild(inhalt);
    let fertig = Promise.resolve();
    if (rolle === 'editor' || rolle === 'viewer') {
      /* „wirkt sofort“ betrifft nur, wer Freigaben ändern kann (KO-12c). */
      hinweis.hidden = true;
      inhalt.appendChild(el('div', { cls: 'share-rolle',
        text: I18n.t('share.nur-rolle', I18n.t(`share.role-${rolle}`)) }));
    } else {
      const liste = buildShareContent(neueBoardId, rolle);
      inhalt.appendChild(liste);
      fertig = liste.aclGeladen ?? fertig;
    }
    /* Karte #684 (ADR-0134 §2): Schalter „für die Ausgangsschleuse sperren“. */
    SchleuseSchluesselDialog?.ergaenzeBoardSperre?.(inhalt, neueBoardId);
    /* VO-7: „beschäftigt“, bis auch die ACL-Liste geladen ist, nicht nur die Rolle. */
    await fertig;
    if (mein !== lauf) return;
    body.setAttribute('aria-busy', 'false');
  }
  return { el: section, aktualisieren };
}

ShareSection = { build };
__shareTest = { buildShareContent, build };

} /* Ende if (IS_SERVER_BUILD) */

export { ShareSection };
/* Nur für Tests (Server-Build-Kontext). null im Nicht-Server-Build. */
export const __testingShare__ = __shareTest;
