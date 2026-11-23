/* agent-zugaenge-dialog.js — „Meine Agenten-Zugänge“ (Karte #608, ADR-0130 §6).
 *
 * Eine Person legt sich für ein Board, auf dem sie mindestens editor ist, ein
 * Token für ihre Claude-Sitzung an — sofern der Admin die Selbstbedienung für
 * dieses Board eingeschaltet hat. Der Agent handelt „für“ sie, seine Rolle ist
 * nie höher als ihre, und er verliert den Zugang, wenn sie ihn verliert.
 *
 * Karte #754 (ADR-0139 §5): Ein Zugang kann auch für mehrere Boards gelten —
 * eine Auswahl oder „alle diese Boards“ (Stand jetzt; später freigegebene
 * kommen nie von selbst dazu). Ein solcher Zugang arbeitet nur über die
 * Werkzeuge bzw. MCP, nicht über rohe Ops.
 *
 * Karte #799 (ADR-0140 §8): Auf dem externen Server eines Board-Paars sind
 * Anbieter und Beleg (Fundstelle des vertraglichen Trainingsausschlusses)
 * Pflicht; sonst optional. Das Feld dokumentiert nur, es prüft nichts.
 *
 * Das Token erscheint GENAU EINMAL, in einem schreibgeschützten Feld mit
 * Kopier-Knopf. Es wird nie geloggt und beim Schliessen aus dem DOM entfernt.
 *
 * IS_SERVER_BUILD-gegated → im Slim-/Claude-Code-Build per DCE eliminiert.
 */

import { I18n } from './i18n.js';
import { createModal } from './modal-shell.js';
import { Rueckmeldung } from './rueckmeldung.js';
import { boardpaarSeite } from './boardpaar.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let AgentZugaengeDialog = null;
let AgentZugaengeClient = null;

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

  AgentZugaengeClient = {
    /* → { tokens[], boards[{id,title,role}], gueltig_standard, gueltig_max } */
    liste() { return anfrage('GET', '/users/me/agent-tokens'); },
    /* → 201 { token, agent } — der Klartext nur in dieser Antwort */
    /* ziel: Board-ID (string) | { boards: [ids] } | { alle: true } (#754) */
    anlegen(ziel, name, role, gueltigTage, herkunft = {}) {
      const body = { name, role, gueltig_tage: gueltigTage };
      /* Karte #799: nur senden, was ausgefüllt ist (innen/aus: optional). */
      if (herkunft.anbieter) body.anbieter = herkunft.anbieter;
      if (herkunft.beleg) body.beleg = herkunft.beleg;
      if (typeof ziel === 'string') body.board = ziel;
      else if (ziel && ziel.alle) body.alle_boards = true;
      else body.boards = (ziel && ziel.boards) || [];
      return anfrage('POST', '/users/me/agent-tokens', body);
    },
    widerrufen(id) { return anfrage('DELETE', `/users/me/agent-tokens/${encodeURIComponent(id)}`); },
  };

  let _modal = null;
  let _bodyEl = null;
  /* Session-Kritiker Sprint 78, K-11: Das eben angelegte Token lebt bis zum
     Schliessen in dieser Variable — ein Neuladen der Liste (Widerruf, zweite
     Anlage) wischte es sonst aus dem DOM, bevor es kopiert war. `_lauf`
     zählt Öffnen/Schliessen: Ein Laden, das nach dem Schliessen fertig wird,
     rendert nichts mehr (sonst stünde das Token im geschlossenen Dialog). */
  let _einmal = null;
  let _lauf = 0;
  /* Erfolgsmeldung einer Aktion, die die Liste neu aufbaut (Anlegen,
     Widerruf) — überlebt den Neuaufbau, wird nach EINER Anzeige geleert. */
  let _angelegt = null;
  let _widerrufen = null;
  /* Sonderwerte der Board-Auswahl (#754) — keine gültige Board-ID (enthalten
     ein Leerzeichen, das eine ID nie hat). */
  const AUSWAHL = ' auswahl';
  const ALLE = ' alle';
  /* storage.ServiceBoardID (server/storage/seed_service.go). */
  const SERVICE_BOARD = 'service';

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.type) node.type = opts.type;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  /** Zustand eines Tokens für die Anzeige: aktiv | widerrufen | abgelaufen. */
  function zustand(t, jetzt = Date.now()) {
    if (t.revoked_at) return 'widerrufen';
    if (t.expires_at && Date.parse(t.expires_at) <= jetzt) return 'abgelaufen';
    return 'aktiv';
  }

  /* Karte #627: Rückmeldung jeder Aktion — Meldungszeile färben (busy/ok/err)
     und den Knopf kurz aufleuchten lassen. Vorher blieb u. a. das Kopieren
     bei verweigerter Zwischenablage stumm (das Feld wurde nur markiert). */
  function melde(ziel, art, text) {
    Rueckmeldung.meldeZeile(ziel, 'agz-meldung', art, text);
  }
  function blinke(btn, art) {
    Rueckmeldung.blinkeKnopf(btn, 'agz-btn', art);
  }

  function fehlertext(status, body) {
    /* Karte #799: Der Servertext zu Anbieter/Beleg ist deutsch — in der Oberfläche
       erscheint stattdessen ein übersetzter Text (Kritiker K-8). */
    /* Sprint-92-Kritiker KO-4: am Anfang verankert — der Server zitiert in
       Namensfehlern den Namen („name "beleg bot" …“), ein Teilwort träfe ihn. */
    if (status === 400 && typeof body?.error === 'string' && /^(?:anbieter|beleg):|^auf dem externen server/.test(body.error)) return I18n.t('agz.fehler-herkunft-server');
    if (status === 403) return I18n.t('agz.fehler-403');
    if (status === 409) return I18n.t('agz.fehler-409');
    if (status === 400) return I18n.t('agz.fehler-400');
    if (status === 429) return I18n.t('agz.fehler-429');
    return I18n.t('agz.fehler-server');
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'agz-overlay',
      extraPanelClass: 'agz-panel',
      titleId: 'agz-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      /* Beim Schliessen den Klartext aus dem DOM nehmen. */
      onRequestClose: () => close(),
    });
    _modal.setTitle(I18n.t('agz.titel'));
    _bodyEl = _modal.body;
  }

  function zeigeTokenEinmal(ziel, token, board) {
    const box = el('div', { cls: 'agz-einmal', attrs: { role: 'status' } });
    box.appendChild(el('p', { cls: 'agz-einmal-titel', text: I18n.t('agz.einmal-titel') }));
    const feld = el('input', { cls: 'agz-token', attrs: { readonly: '', 'aria-label': I18n.t('agz.einmal-titel'), spellcheck: 'false', autocomplete: 'off' } });
    feld.value = token;
    box.appendChild(feld);
    const kopieren = el('button', { cls: 'agz-btn', text: I18n.t('agz.btn-kopieren') });
    kopieren.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(feld.value);
        kopieren.textContent = I18n.t('agz.kopiert');
        blinke(kopieren, 'ok');
        melde(kopiert, 'ok', I18n.t('agz.kopiert'));
      } catch {
        feld.select();
        blinke(kopieren, 'err');
        melde(kopiert, 'err', I18n.t('agz.kopieren-fehler'));
      }
    });
    const kopiert = el('span', { cls: 'agz-meldung', attrs: { role: 'status' } });
    box.appendChild(kopieren);
    box.appendChild(kopiert);
    box.appendChild(el('p', { cls: 'agz-hinweis', text: I18n.t('agz.einmal-ablage', board) }));
    ziel.appendChild(box);
    feld.focus();
    feld.select();
  }

  /* Board-Spalte: ein Board, oder bei #754 „N Boards: …“ mit Titeln, soweit
     bekannt (die Liste kennt nur Boards mit eingeschalteter Selbstbedienung). */
  function boardText(t, titel) {
    if (!t.mehrere) return t.board_id;
    const namen = (t.boards || []).map(id => titel.get(id) || id);
    return I18n.t('agz.mehrere-boards', namen.length, namen.join(', '));
  }

  /* Karte #799: Anbieter/Beleg einer Zeile. Fehlt eine Angabe auf dem externen
     Server, steht ein deutlicher Hinweis statt einer leeren Zelle (Bestand
     wird nicht gesperrt — der Hinweis ist die ganze Folge). */
  function herkunftText(t, fehlt) {
    const teile = [t.anbieter, t.beleg].filter(Boolean);
    if (teile.length === 2) return teile.join(' — ');
    if (fehlt) return I18n.t('agz.herkunft-fehlt') + (teile.length ? ` (${teile[0]})` : '');
    return teile.length ? teile[0] : '—';
  }

  function renderTabelle(tokens, titel = new Map(), aussen = false) {
    if (!tokens.length) return el('p', { cls: 'agz-leer', text: I18n.t('agz.keine') });
    const table = el('table', { cls: 'agz-tabelle' });
    const kopf = el('tr');
    ['agz.col-name', 'agz.col-board', 'agz.col-rolle', 'agz.col-kennung', 'agz.col-herkunft', 'agz.col-ablauf', 'agz.col-status', 'agz.col-aktion']
      .forEach(k => kopf.appendChild(el('th', { text: I18n.t(k) })));
    const thead = el('thead'); thead.appendChild(kopf); table.appendChild(thead);
    const tbody = el('tbody');
    for (const t of tokens) {
      const tr = el('tr');
      const z = zustand(t);
      /* Hinweis nur für geltende Tokens, nicht für widerrufene oder abgelaufene (K-4). */
      const fehlt = aussen && z === 'aktiv' && !(t.anbieter && t.beleg);
      [t.name, boardText(t, titel), t.role, t.kennung ? `abt_${t.kennung}_…` : '—',
        /* Herkunftszelle: eigene Zelle mit Klasse statt Spaltenindex (K-3). */
        { text: herkunftText(t, fehlt), cls: 'agz-herkunft' + (fehlt ? ' agz-fehler' : '') },
        t.expires_at ? t.expires_at.slice(0, 10) : I18n.t('agz.unbefristet'), I18n.t('agz.status-' + z)]
        .forEach(v => {
          const td = el('td', { text: typeof v === 'object' ? v.text : v });
          if (typeof v === 'object' && v.cls) td.className = v.cls;
          tr.appendChild(td);
        });
      const aktion = el('td');
      if (z === 'aktiv') {
        const btn = el('button', { cls: 'agz-btn agz-btn--danger', text: I18n.t('agz.btn-widerrufen'),
          attrs: { 'aria-label': I18n.t('agz.aria-widerrufen', t.name) } });
        btn.addEventListener('click', async () => {
          const msg = aktion.querySelector('.agz-meldung') || aktion.appendChild(el('span', { cls: 'agz-meldung' }));
          if (!window.confirm(I18n.t('agz.confirm-widerrufen', t.name))) {
            melde(msg, 'busy', I18n.t('agz.abgebrochen'));
            return;
          }
          btn.disabled = true;
          melde(msg, 'busy', I18n.t('agz.arbeitet'));
          const r = await AgentZugaengeClient.widerrufen(t.id);
          if (r.ok) { _widerrufen = t.name; await refresh(); }
          else { btn.disabled = false; blinke(btn, 'err'); melde(msg, 'err', fehlertext(r.status)); }
        });
        aktion.appendChild(btn);
      }
      tr.appendChild(aktion);
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    return table;
  }

  function renderAnlage(boards, standard, max, aussen = false) {
    const sec = el('div', { cls: 'agz-anlage' });
    sec.appendChild(el('h3', { cls: 'agz-h', text: I18n.t('agz.neu-titel') }));
    if (!boards.length) {
      sec.appendChild(el('p', { cls: 'agz-hinweis', text: I18n.t('agz.keine-boards') }));
      return sec;
    }
    const form = el('div', { cls: 'agz-form' });
    const boardSel = el('select', { attrs: { 'aria-label': I18n.t('agz.col-board') } });
    for (const b of boards) boardSel.appendChild(Object.assign(el('option', { text: b.title || b.id }), { value: b.id }));
    /* #754: mehrere Boards — eine Auswahl oder alle (Stand jetzt). */
    const auswahl = el('fieldset', { cls: 'agz-auswahl', attrs: { hidden: '' } });
    auswahl.appendChild(el('legend', { text: I18n.t('agz.auswahl-legende') }));
    const haken = [];
    /* Kritiker #754, K-4: Das Service-Board (Mail-Eingang) nimmt der Server in
       keine Liste auf — sonst zeigte „alle (n)“ ein Board mehr, als angelegt
       wird. Für einen Zugang zu einem einzelnen Board bleibt es wählbar. */
    const mehrfach = boards.filter(b => b.id !== SERVICE_BOARD);
    if (mehrfach.length > 1) {
      boardSel.appendChild(Object.assign(el('option', { text: I18n.t('agz.opt-auswahl') }), { value: AUSWAHL }));
      boardSel.appendChild(Object.assign(el('option', { text: I18n.t('agz.opt-alle', mehrfach.length) }), { value: ALLE }));
      for (const b of mehrfach) {
        const lbl = el('label', { cls: 'agz-auswahl-zeile' });
        const chk = el('input', { type: 'checkbox' });
        chk.value = b.id;
        haken.push(chk);
        lbl.appendChild(chk);
        lbl.appendChild(document.createTextNode(' ' + (b.title || b.id)));
        auswahl.appendChild(lbl);
      }
    }
    const mehrHinweis = el('p', { cls: 'agz-hinweis', text: I18n.t('agz.mehrere-hinweis'), attrs: { hidden: '' } });
    const zeigeAuswahl = () => {
      const v = boardSel.value;
      if (v === AUSWAHL) auswahl.removeAttribute('hidden'); else auswahl.setAttribute('hidden', '');
      if (v === AUSWAHL || v === ALLE) mehrHinweis.removeAttribute('hidden'); else mehrHinweis.setAttribute('hidden', '');
    };
    boardSel.addEventListener('change', zeigeAuswahl);
    const name = el('input', { attrs: { placeholder: I18n.t('agz.name-platzhalter'), 'aria-label': I18n.t('agz.col-name'),
      pattern: '[a-z0-9][a-z0-9-]{0,62}', maxlength: '63', spellcheck: 'false', autocomplete: 'off' } });
    const rolle = el('select', { attrs: { 'aria-label': I18n.t('agz.col-rolle') } });
    for (const r of ['editor', 'viewer']) rolle.appendChild(Object.assign(el('option', { text: I18n.t('agz.rolle-' + r) }), { value: r }));
    const tage = el('input', { type: 'number', attrs: { min: '1', max: String(max), 'aria-label': I18n.t('agz.tage') } });
    tage.value = String(standard);
    /* Karte #799: Anbieter und Beleg — auf dem externen Server Pflicht. */
    const anbieter = el('input', { attrs: { placeholder: I18n.t('agz.anbieter'), 'aria-label': I18n.t('agz.anbieter'),
      maxlength: '80', autocomplete: 'off' } });
    const beleg = el('input', { attrs: { placeholder: I18n.t('agz.beleg'), 'aria-label': I18n.t('agz.beleg'),
      maxlength: '300', autocomplete: 'off' } });
    if (aussen) { anbieter.required = true; beleg.required = true; }
    const btn = el('button', { cls: 'agz-btn agz-btn--primary', text: I18n.t('agz.btn-anlegen') });
    const meldung = el('div', { cls: 'agz-meldung', attrs: { role: 'status' } });
    btn.addEventListener('click', async () => {
      const n = name.value.trim();
      if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(n)) {
        melde(meldung, 'err', I18n.t('agz.fehler-name'));
        blinke(btn, 'err');
        name.setAttribute('aria-invalid', 'true');
        name.focus();
        return;
      }
      name.removeAttribute('aria-invalid');
      let ziel = boardSel.value;
      if (ziel === ALLE) ziel = { alle: true };
      else if (ziel === AUSWAHL) {
        const ids = haken.filter(c => c.checked).map(c => c.value);
        if (!ids.length) {
          melde(meldung, 'err', I18n.t('agz.fehler-auswahl'));
          blinke(btn, 'err');
          return;
        }
        ziel = { boards: ids };
      }
      const herkunft = { anbieter: anbieter.value.trim(), beleg: beleg.value.trim() };
      if (aussen && (!herkunft.anbieter || !herkunft.beleg)) {
        melde(meldung, 'err', I18n.t('agz.fehler-herkunft'));
        blinke(btn, 'err');
        (herkunft.anbieter ? beleg : anbieter).focus();
        return;
      }
      btn.disabled = true;
      melde(meldung, 'busy', I18n.t('agz.arbeitet'));
      const lauf = _lauf;
      const r = await AgentZugaengeClient.anlegen(ziel, n, rolle.value, Number(tage.value) || 0, herkunft);
      /* Verify Sprint 78, V-2: inzwischen geschlossen → das Token nicht mehr
         zeigen und nicht merken (es existiert trotzdem; die Liste zeigt es,
         widerrufen lässt es sich dort). */
      if (lauf !== _lauf) return;
      btn.disabled = false;
      if (!r.ok || !r.body?.token) { blinke(btn, 'err'); melde(meldung, 'err', fehlertext(r.status, r.body)); return; }
      _angelegt = n;
      _einmal = { token: r.body.token, board: typeof ziel === 'string' ? ziel : 'aboard' };
      await refresh();
    });
    [boardSel, name, rolle, tage, anbieter, beleg, btn].forEach(x => form.appendChild(x));
    sec.appendChild(form);
    sec.appendChild(auswahl);
    sec.appendChild(mehrHinweis);
    sec.appendChild(el('label', { cls: 'agz-hinweis', text: I18n.t('agz.tage-hinweis', standard, max) }));
    sec.appendChild(el('p', { cls: 'agz-hinweis', text: I18n.t(aussen ? 'agz.herkunft-pflicht' : 'agz.herkunft-optional') }));
    sec.appendChild(meldung);
    return sec;
  }

  async function refresh() {
    const lauf = _lauf;
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'agz-hinweis', text: I18n.t('agz.laden') }));
    const [r, seite] = await Promise.all([AgentZugaengeClient.liste(), boardpaarSeite()]);
    if (lauf !== _lauf) return;   /* inzwischen geschlossen */
    const aussen = seite === 'aussen';
    _bodyEl.innerHTML = '';
    if (!r.ok) { _bodyEl.appendChild(el('p', { cls: 'agz-fehler', text: fehlertext(r.status) })); return; }
    if (_widerrufen || _angelegt) {
      const m = el('p', { cls: 'agz-meldung' });
      melde(m, 'ok', _widerrufen ? I18n.t('agz.msg-widerrufen', _widerrufen) : I18n.t('agz.msg-angelegt', _angelegt));
      _bodyEl.appendChild(m);
      _widerrufen = null;
      _angelegt = null;
    }
    _bodyEl.appendChild(el('p', { cls: 'agz-intro', text: I18n.t('agz.intro') }));
    const boards = Array.isArray(r.body?.boards) ? r.body.boards : [];
    const titel = new Map(boards.map(b => [b.id, b.title || b.id]));
    _bodyEl.appendChild(renderTabelle(Array.isArray(r.body?.tokens) ? r.body.tokens : [], titel, aussen));
    _bodyEl.appendChild(renderAnlage(boards,
      r.body?.gueltig_standard || 90, r.body?.gueltig_max || 365, aussen));
    if (_einmal) zeigeTokenEinmal(_bodyEl.querySelector('.agz-anlage') || _bodyEl, _einmal.token, _einmal.board);
  }

  function ensureStyles() {
    if (document.getElementById('agz-dialog-style')) return;
    const style = document.createElement('style');
    style.id = 'agz-dialog-style';
    style.textContent = `
.agz-overlay{z-index:1100;background:rgba(0,0,0,.5)}
.agz-panel .modal-header-row{background:#fff;color:#222;border-bottom:1px solid #eee}.agz-panel .modal-dialog-title{color:#222}.agz-panel .btn-modal-close{color:#444;opacity:1}
.agz-panel{background:#fff;color:#222;border-radius:8px;width:min(820px,94vw);max-height:88vh;box-shadow:0 12px 36px rgba(0,0,0,.3)}
.agz-intro,.agz-hinweis,.agz-leer{color:#555;font-size:.88rem;margin:0 0 10px;line-height:1.45}
.agz-tabelle{width:100%;border-collapse:collapse;font-size:.85rem;margin-bottom:16px}
.agz-tabelle th,.agz-tabelle td{border-bottom:1px solid #eee;padding:4px 6px;text-align:left}
.agz-herkunft{overflow-wrap:anywhere;min-width:8em}
.agz-h{font-size:.95rem;margin:12px 0 8px}
.agz-form{display:flex;gap:6px;flex-wrap:wrap;align-items:center;margin-bottom:6px}
.agz-form input,.agz-form select{padding:5px 7px;border:1px solid #ccc;border-radius:4px}
.agz-auswahl{border:1px solid #ddd;border-radius:4px;padding:6px 10px;margin:0 0 6px;max-height:180px;overflow:auto}
.agz-auswahl-zeile{display:block;font-size:.85rem;padding:2px 0}
.agz-btn{padding:5px 11px;border:1px solid #bbb;background:#f8f8f8;color:#222;cursor:pointer;border-radius:4px;font-size:.85rem}
.agz-btn--primary{background:#2563eb;color:#fff;border-color:#2563eb}
.agz-btn--danger{color:#c00;border-color:#f4baba;background:#fff5f5}
.agz-btn:disabled{opacity:.5;cursor:not-allowed}
.agz-fehler,.agz-meldung{color:#c00;font-size:.85rem}
.agz-meldung--busy{color:#555;font-style:italic}
.agz-meldung--ok{color:#1a7f37;font-weight:600}
.agz-meldung--err{color:#c00;font-weight:600}
.agz-btn--ok{background:#dafbe1;border-color:#1a7f37;color:#1a7f37}
.agz-btn--err{background:#ffebe9;border-color:#c00;color:#c00}
.agz-einmal{margin-top:12px;padding:10px;border:1px solid #f5b800;background:#fff8e1;color:#222;border-radius:6px}
.agz-einmal-titel{font-weight:600;margin:0 0 6px}
.agz-token{width:100%;font-family:monospace;padding:6px;margin-bottom:6px;box-sizing:border-box}`;
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
    _einmal = null;
    _angelegt = null;
    _widerrufen = null;
    if (_bodyEl) _bodyEl.innerHTML = '';
    _modal?.close();
  }

  AgentZugaengeDialog = { open, close, __testing__: { zustand, boardText, herkunftText } };
}

export { AgentZugaengeDialog, AgentZugaengeClient };
