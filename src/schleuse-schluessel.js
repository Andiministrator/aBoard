/* schleuse-schluessel.js — Ausgangsschleuse für Personen (Karte #684, ADR-0134 §2).
 *
 * Drei Dinge, alle nur im Server-Build und nur bei eingeschalteter Schleuse:
 *  1. „Meine Schlüssel“: je freigegebenem Anbieter einen eigenen Schlüssel
 *     hinterlegen (mit Bestätigung „geschäftliches Konto“) oder löschen. Der
 *     Schlüssel geht nie an den Browser zurück — die Liste zeigt nur Anbieter
 *     und Datum.
 *  2. `ergaenzeMenue`: hängt den Menüpunkt an die Konto-Knopfreihe der Sektion
 *     in den Benutzer-Einstellungen (llm-section.js), wenn
 *     GET /me `schleuse: "an"` meldet.
 *  3. `ergaenzeBoardSperre`: Schalter „für die Ausgangsschleuse sperren“ in
 *     der Freigabe-Sektion im Dialog Board-Einstellungen
 *     (src/share-section.js). Fragt die Sperre nur an, wenn /me „an“ meldet
 *     (KC-8) — sonst gäbe es bei jedem Aufbau der Sektion einen
 *     404-Aufruf. Das /me-Ergebnis ist zwischengespeichert (`schleuseAn`).
 *
 * Getrennt vom KI-Chat (ADR-0133 §2): eigene Routen, eigene Tabelle;
 * src/llm-tab.js bleibt unberührt.
 *
 * IS_SERVER_BUILD-gegated → im Slim-/Claude-Code-Build per DCE eliminiert.
 */

import { I18n } from './i18n.js';
import { createModal } from './modal-shell.js';
import { Rueckmeldung } from './rueckmeldung.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let SchleuseSchluesselDialog = null;
let SchleuseClient = null;

if (IS_SERVER_BUILD) {
  async function anfrage(method, path, payload) {
    try {
      const res = await holen(path, {
        method,
        headers: payload === undefined ? {} : { 'Content-Type': 'application/json' },
        body: payload === undefined ? null : JSON.stringify(payload),
      });
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  SchleuseClient = {
    /* → { schleuse: 'an' } nur bei eingeschalteter Schleuse */
    me() { return anfrage('GET', '/me'); },
    /* → { anbieter: [{id, anbieter, bezeichnung, host, modelle, nutzbar, gruende}],
           schluessel: [{freigabe_id, anbieter, bezeichnung, erstellt_am, …}] } */
    liste() { return anfrage('GET', '/users/me/schleuse-schluessel'); },
    /* stand = { anbieter, basis_url } — so, wie der Dialog die Freigabe
       angezeigt hat. Hat der Admin sie inzwischen umgestellt, antwortet der
       Server 409 (KC-2). */
    setzen(id, key, stand) {
      return anfrage('PUT', `/users/me/schleuse-schluessel/${encodeURIComponent(id)}`,
        { key, geschaeftskonto_bestaetigt: true, anbieter: stand?.anbieter, basis_url: stand?.basis_url });
    },
    loeschen(id) { return anfrage('DELETE', `/users/me/schleuse-schluessel/${encodeURIComponent(id)}`); },
    /* → { gesperrt, darf_aendern } */
    sperre(boardId) { return anfrage('GET', `/boards/${encodeURIComponent(boardId)}/schleuse-sperre`); },
    setzeSperre(boardId, gesperrt) {
      return anfrage('PUT', `/boards/${encodeURIComponent(boardId)}/schleuse-sperre`, { gesperrt: !!gesperrt });
    },
  };

  let _modal = null;
  let _bodyEl = null;
  let _lauf = 0;
  let _meldung = '';

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.type) node.type = opts.type;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  function melde(ziel, art, text) {
    Rueckmeldung.meldeZeile(ziel, 'sls-meldung', art, text);
  }

  function fehlertext(status) {
    if (status === 503) return I18n.t('schleuse.fehler-503');
    if (status === 409) return I18n.t('schleuse.fehler-409');
    if (status === 429) return I18n.t('schleuse.fehler-429');
    return I18n.t('schleuse.fehler-server');
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'sls-overlay',
      extraPanelClass: 'sls-panel',
      titleId: 'sls-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      onRequestClose: () => close(),
    });
    _modal.setTitle(I18n.t('schleuse.titel'));
    _bodyEl = _modal.body;
  }

  function renderAnbieter(a, hinterlegt) {
    const name = a.bezeichnung ? `${a.bezeichnung} (${a.anbieter})` : a.anbieter;
    const box = el('div', { cls: 'sls-anbieter' });
    box.dataset.id = a.id;
    box.appendChild(el('h3', { cls: 'sls-h', text: `${name} — ${a.host}` }));
    if (Array.isArray(a.modelle) && a.modelle.length) {
      box.appendChild(el('p', { cls: 'sls-hinweis', text: a.modelle.join(', ') }));
    }
    box.appendChild(el('p', { cls: 'sls-stand', text: hinterlegt
      ? I18n.t('schleuse.hinterlegt', String(hinterlegt.erstellt_am || '').slice(0, 10))
      : I18n.t('schleuse.kein-schluessel') }));
    if (!a.nutzbar) box.appendChild(el('p', { cls: 'sls-hinweis', text: I18n.t('schleuse.nicht-nutzbar') }));

    const form = el('div', { cls: 'sls-form' });
    const key = el('input', { cls: 'sls-key', type: 'password',
      attrs: { 'aria-label': I18n.t('schleuse.schluessel'), placeholder: I18n.t('schleuse.schluessel'),
        autocomplete: 'off', spellcheck: 'false' } });
    const bestaetigt = el('input', { type: 'checkbox', cls: 'sls-bestaetigt' });
    const label = el('label', { cls: 'sls-label' });
    label.appendChild(bestaetigt);
    label.appendChild(document.createTextNode(' ' + I18n.t('schleuse.geschaeftskonto')));
    const speichern = el('button', { cls: 'sls-btn sls-btn--primary', text: I18n.t('schleuse.btn-speichern') });
    const msg = el('div', { cls: 'sls-meldung', attrs: { role: 'status' } });
    speichern.addEventListener('click', async () => {
      const k = key.value.trim();
      if (!k || /\s/.test(k)) { melde(msg, 'err', I18n.t('schleuse.fehler-leer')); key.focus(); return; }
      if (!bestaetigt.checked) { melde(msg, 'err', I18n.t('schleuse.fehler-bestaetigung')); bestaetigt.focus(); return; }
      speichern.disabled = true;
      melde(msg, 'busy', I18n.t('schleuse.arbeitet'));
      const lauf = _lauf;
      const r = await SchleuseClient.setzen(a.id, k, { anbieter: a.anbieter, basis_url: a.basis_url });
      if (lauf !== _lauf) return;
      key.value = '';
      speichern.disabled = false;
      if (r.status === 409) {
        /* Die Freigabe hat sich geändert oder ist zurückgezogen: neu laden,
           damit die Person sieht, wofür sie ihren Schlüssel einträgt. */
        _meldung = '';
        await refresh();
        const box = [...(_bodyEl?.querySelectorAll('.sls-anbieter') || [])].find(x => x.dataset.id === a.id);
        const neu = box?.querySelector('.sls-meldung');
        const ziel = neu || _bodyEl?.insertBefore(el('p', { cls: 'sls-meldung' }), _bodyEl.firstChild);
        if (ziel) melde(ziel, 'err', fehlertext(409));
        return;
      }
      if (!r.ok) { melde(msg, 'err', fehlertext(r.status)); return; }
      _meldung = I18n.t('schleuse.gespeichert');
      await refresh();
    });
    form.appendChild(key);
    form.appendChild(speichern);
    if (hinterlegt) {
      const loeschen = el('button', { cls: 'sls-btn sls-btn--danger', text: I18n.t('schleuse.btn-loeschen') });
      loeschen.addEventListener('click', async () => {
        if (!window.confirm(I18n.t('schleuse.confirm-loeschen', name))) return;
        loeschen.disabled = true;
        const lauf = _lauf;
        const r = await SchleuseClient.loeschen(a.id);
        if (lauf !== _lauf) return;
        loeschen.disabled = false;
        if (!r.ok) { melde(msg, 'err', fehlertext(r.status)); return; }
        _meldung = I18n.t('schleuse.geloescht');
        await refresh();
      });
      form.appendChild(loeschen);
    }
    box.appendChild(form);
    box.appendChild(label);
    box.appendChild(msg);
    return box;
  }

  async function refresh() {
    const lauf = _lauf;
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'sls-hinweis', text: I18n.t('schleuse.laden') }));
    const r = await SchleuseClient.liste();
    if (lauf !== _lauf) return;
    _bodyEl.innerHTML = '';
    if (!r.ok) { _bodyEl.appendChild(el('p', { cls: 'sls-fehler', text: fehlertext(r.status) })); return; }
    if (_meldung) {
      const m = el('p', { cls: 'sls-meldung' });
      melde(m, 'ok', _meldung);
      _bodyEl.appendChild(m);
      _meldung = '';
    }
    _bodyEl.appendChild(el('p', { cls: 'sls-intro', text: I18n.t('schleuse.intro') }));
    const anbieter = Array.isArray(r.body?.anbieter) ? r.body.anbieter : [];
    const schluessel = Array.isArray(r.body?.schluessel) ? r.body.schluessel : [];
    if (!anbieter.length) {
      _bodyEl.appendChild(el('p', { cls: 'sls-hinweis', text: I18n.t('schleuse.keine-anbieter') }));
      return;
    }
    for (const a of anbieter) {
      _bodyEl.appendChild(renderAnbieter(a, schluessel.find(s => s.freigabe_id === a.id)));
    }
  }

  function ensureStyles() {
    if (document.getElementById('sls-dialog-style')) return;
    const style = document.createElement('style');
    style.id = 'sls-dialog-style';
    style.textContent = `
.sls-overlay{z-index:1100;background:rgba(0,0,0,.5)}
.sls-panel .modal-header-row{background:#fff;color:#222;border-bottom:1px solid #eee}.sls-panel .modal-dialog-title{color:#222}.sls-panel .btn-modal-close{color:#444;opacity:1}
.sls-panel{background:#fff;color:#222;border-radius:8px;width:min(680px,94vw);max-height:88vh;box-shadow:0 12px 36px rgba(0,0,0,.3)}
.sls-intro,.sls-hinweis,.sls-stand{color:#555;font-size:.88rem;margin:0 0 8px;line-height:1.45}
.sls-anbieter{border-top:1px solid #eee;padding:10px 0}
.sls-h{font-size:.95rem;margin:0 0 6px}
.sls-form{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin-bottom:6px}
.sls-key{flex:1;min-width:200px;padding:5px 7px;border:1px solid #ccc;border-radius:4px;font-family:monospace}
.sls-label{font-size:.85rem;color:#333}
.sls-btn{padding:5px 11px;border:1px solid #bbb;background:#f8f8f8;color:#222;cursor:pointer;border-radius:4px;font-size:.85rem}
.sls-btn--primary{background:#2563eb;color:#fff;border-color:#2563eb}
.sls-btn--danger{color:#c00;border-color:#f4baba;background:#fff5f5}
.sls-btn:disabled{opacity:.5;cursor:not-allowed}
.sls-fehler,.sls-meldung{color:#c00;font-size:.85rem}
.sls-meldung--busy{color:#555;font-style:italic}
.sls-meldung--ok{color:#1a7f37;font-weight:600}
.sls-meldung--err{color:#c00;font-weight:600}
.sls-sperre{display:flex;gap:6px;align-items:center}`;
    document.head.appendChild(style);
  }

  function open() {
    ensureStyles();
    ensureModal();
    _modal.open();
    refresh().then(() => _modal.focusAfterRender?.());
  }

  function close() {
    _lauf++;
    _meldung = '';
    if (_bodyEl) _bodyEl.innerHTML = '';
    _modal?.close();
  }

  /* Meldet der Server die Schleuse als „an“? Der Schalter ändert sich nur mit
     einem Neustart des Servers; das Ergebnis wird deshalb für die Sitzung der
     Seite zwischengespeichert — Menü und Board-Sperre teilen sich EINEN
     /me-Aufruf. Ein Fehlschlag (Netz, 5xx) wird nicht gemerkt, der nächste
     Sidebar-Aufbau fragt erneut. */
  let _anVersprechen = null;
  function schleuseAn() {
    if (!_anVersprechen) {
      _anVersprechen = SchleuseClient.me().then((r) => {
        if (!r.ok) { _anVersprechen = null; return false; }
        return r.body?.schleuse === 'an';
      });
    }
    return _anVersprechen;
  }
  /* Für Tests: Zwischenspeicher leeren. */
  function vergissSchalter() { _anVersprechen = null; }

  /* Menüpunkt nur, wenn der Server die Schleuse als „an“ meldet. Läuft im
     Hintergrund, damit der Dialog nicht auf /me wartet. */
  async function ergaenzeMenue(section, makeRow, onClick = open) {
    const an = await schleuseAn();
    if (!an || !section?.isConnected) return;
    if (section.querySelector('.sls-menue')) return;
    const zeile = makeRow(I18n.t('schleuse.menu'), onClick, { icon: '🔐' });
    zeile.classList.add('sls-menue');
    section.appendChild(zeile);
  }

  /* Board-Sperre in der Freigabe-Sektion (ADR-0134 §2). Erst /me (KC-8):
     bei „aus“ kein Aufruf. 404 oder ein Fehler → nichts. Nicht-Owner sehen
     den Stand nur, wenn gesperrt. */
  async function ergaenzeBoardSperre(section, boardId) {
    if (!(await schleuseAn()) || !section?.isConnected) return;
    const r = await SchleuseClient.sperre(boardId);
    if (!r.ok || !r.body || !section?.isConnected) return;
    const { gesperrt, darf_aendern: darf } = r.body;
    const zeile = el('div', { cls: 'app-sidebar-row sls-sperre' });
    if (!darf) {
      if (!gesperrt) return;
      zeile.textContent = I18n.t('schleuse.sperre-gesperrt');
      section.appendChild(zeile);
      return;
    }
    ensureStyles();
    const cb = el('input', { type: 'checkbox', attrs: { 'aria-label': I18n.t('schleuse.sperre') } });
    cb.checked = !!gesperrt;
    const label = el('label');
    label.appendChild(cb);
    label.appendChild(document.createTextNode(' ' + I18n.t('schleuse.sperre')));
    label.title = I18n.t('schleuse.sperre-hinweis');
    const msg = el('span', { cls: 'sls-meldung', attrs: { role: 'status' } });
    cb.addEventListener('change', async () => {
      cb.disabled = true;
      const soll = cb.checked;
      const a = await SchleuseClient.setzeSperre(boardId, soll);
      cb.disabled = false;
      if (!a.ok) { cb.checked = !soll; melde(msg, 'err', I18n.t('schleuse.sperre-fehler')); return; }
      melde(msg, 'ok', '✓');
    });
    zeile.appendChild(label);
    zeile.appendChild(msg);
    section.appendChild(zeile);
  }

  SchleuseSchluesselDialog = { open, close, ergaenzeMenue, ergaenzeBoardSperre, vergissSchalter };
}

export { SchleuseSchluesselDialog, SchleuseClient };
