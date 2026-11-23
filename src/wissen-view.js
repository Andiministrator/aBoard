/* wissen-view.js — Lesesicht auf die gemeinsame Wissensquelle (Karte #739,
 * ADR-0137 §3).
 *
 * Seitenleiste → „Wissen“ (Name aus /me) → dieser Dialog: Übersicht nach
 * Ordnern, Suche, Seite. aBoard liest nur; geschrieben wird im Repo
 * (optional verlinkt: „Im Repo bearbeiten“).
 *
 * Seiten rendert der Markdown-Renderer im Modus `sicher` (ADR-0137 §6):
 * Beim Anzeigen lädt nichts, rohes HTML erscheint als Text. Relative Links
 * kommen als `a.md-seitenlink` an; dieser Dialog löst sie gegen die aktuelle
 * Seite auf und öffnet die Zielseite. Links untereinander sind in den
 * eingebundenen Wissensseiten nicht geregelt (08.10.) — deshalb tolerant: Prozent-Kodierung, fehlendes
 * `.md`, Anker und zuletzt ein eindeutiger Dateiname irgendwo im Ordner.
 * Keine Ordnernamen fest eingebaut.
 *
 * Gesamtes Modul hinter IS_SERVER_BUILD → im Slim/Projektor-Build per DCE weg.
 */

import { I18n } from './i18n.js';
import { Markdown } from './markdown.js';
import { createModal } from './modal-shell.js';
import { ansagen } from './ansage.js'; /* F-2876 */
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let WissenView = null;

/* Pfad-Auflösung ist rein und wird direkt getestet.
   basis: Pfad der aktuellen Seite (NFC). ziel: data-seite des Links.
   seiten: Set der Pfade im Index. Liefert den Indexpfad oder null. */
function loeseSeitenlink(basis, ziel, seiten) {
  let z = String(ziel || '').split('#')[0].split('?')[0].trim();
  if (!z) return null;
  try { z = decodeURIComponent(z); } catch { /* roh lassen */ }
  z = z.normalize('NFC');
  const teile = [];
  const start = z.startsWith('/') ? [] : String(basis || '').split('/').slice(0, -1);
  for (const t of [...start, ...z.split('/')]) {
    if (t === '' || t === '.') continue;
    if (t === '..') {
      if (!teile.length) return null; /* über die Wurzel hinaus */
      teile.pop();
    } else teile.push(t);
  }
  const p = teile.join('/');
  if (seiten.has(p)) return p;
  if (!z.endsWith('/') && seiten.has(p + '.md')) return p + '.md';
  /* Zuletzt: derselbe Dateiname, wenn er im ganzen Ordner genau einmal
     vorkommt (Ordnerstruktur im Umbau, Links zeigen auf alte Orte). Nur für
     ausdrückliche `.md`-Ziele — ein Ordnerlink (`karten/`) oder ein Name
     ohne Endung soll nicht still irgendeine gleichnamige Seite öffnen
     (Kritiker #739 K-5). */
  if (!/\.md$/i.test(z)) return null;
  const name = teile[teile.length - 1].toLowerCase();
  const treffer = [...seiten].filter(s => s.split('/').pop().toLowerCase() === name);
  return treffer.length === 1 ? treffer[0] : null;
}

if (IS_SERVER_BUILD) {
  let _stylesInjected = false;
  const ensureStyles = () => {
    if (_stylesInjected) return;
    _stylesInjected = true;
    const style = document.createElement('style');
    style.setAttribute('data-aboard', 'wissen');
    style.textContent = '.wv-panel{width:min(860px,100%)}.wv-kopf{display:flex;gap:8px;align-items:center;margin-bottom:12px}.wv-suche{flex:1;padding:7px 9px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;font-size:14px}.wv-zurueck{padding:6px 10px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:13px;white-space:nowrap}.wv-ordner{margin:14px 0 4px;font-size:13px;font-weight:600;color:var(--text-muted)}.wv-liste{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:2px}.wv-eintrag{display:block;width:100%;text-align:left;background:none;border:none;border-radius:4px;padding:6px 8px;font-size:14px;color:inherit;cursor:pointer}.wv-eintrag:hover,.wv-eintrag:focus-visible{background:var(--btn-bg-hover);outline:none}.wv-pfad{display:block;font-size:11px;color:var(--text-muted)}.wv-schnipsel{display:block;font-size:12px;color:var(--text-muted);margin-top:2px}.wv-hinweis{color:var(--text-muted);font-style:italic;font-size:13px;margin:8px 0}.wv-fuss{display:flex;justify-content:space-between;gap:12px;margin-top:16px;padding-top:8px;border-top:1px solid var(--border);font-size:12px;color:var(--text-muted)}.wv-fuss a{color:inherit}.wv-inhalt .md-seitenlink{text-decoration:underline dotted}';
    document.head.appendChild(style);
  };

  let _modal = null;
  let _kopf = null;
  let _suche = null;
  let _zurueck = null;
  let _inhalt = null;
  let _fuss = null;
  let _index = null;      /* Antwort von /wissen/index */
  let _seiten = new Set();
  let _lauf = 0;          /* verwirft späte Antworten nach Ansichtswechsel */
  let _suchTimer = null;
  let _zeigtTreffer = false; /* Verify #739 V-4: nur Treffer weichen der Übersicht */

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  async function holeJSON(pfad) {
    try {
      const res = await holen(pfad);
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  function fehlerText(status) {
    if (status === 404) return I18n.t('wissen.nicht-gefunden');
    if (status === 413) return I18n.t('wissen.zu-gross');
    if (status === 429) return I18n.t('wissen.zu-viele');
    if (status === 403) return I18n.t('wissen.kein-zugriff');
    return I18n.t('wissen.fehler');
  }

  function hinweis(text) {
    _inhalt.textContent = '';
    _inhalt.appendChild(el('p', 'wv-hinweis', text));
  }

  function setzeFuss(bearbeiten) {
    _fuss.textContent = '';
    const st = _index?.stand;
    _fuss.appendChild(el('span', '', st ? I18n.t('wissen.stand', st.commit || '', st.datum || '') : ''));
    /* Der Server liefert nur https-Links (wissen.bearbeiten_url geprüft);
       hier noch einmal, weil das href aus einer Antwort stammt. */
    if (bearbeiten && /^https:\/\//i.test(bearbeiten)) {
      const a = el('a', '', I18n.t('wissen.bearbeiten'));
      a.href = bearbeiten;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      _fuss.appendChild(a);
    }
  }

  function eintrag(seite, unterzeile) {
    const li = el('li');
    const b = el('button', 'wv-eintrag', seite.titel || seite.pfad);
    b.type = 'button';
    b.title = seite.pfad;
    if (unterzeile) b.appendChild(el('span', 'wv-pfad', seite.pfad));
    if (seite.schnipsel) b.appendChild(el('span', 'wv-schnipsel', seite.schnipsel));
    b.addEventListener('click', () => zeigeSeite(seite.pfad));
    li.appendChild(b);
    return li;
  }

  function zeigeUebersicht() {
    _lauf++;
    _zeigtTreffer = false;
    _zurueck.hidden = true;
    setzeFuss(null);
    const seiten = _index?.seiten || [];
    if (!seiten.length) { hinweis(I18n.t('wissen.leer')); return; }
    _inhalt.textContent = '';
    /* Gruppiert nach erstem Ordner — welche Ordner es gibt, bestimmt der
       Bestand, nicht der Code. */
    const gruppen = new Map();
    for (const s of seiten) {
      const i = s.pfad.indexOf('/');
      const ordner = i < 0 ? '' : s.pfad.slice(0, i);
      if (!gruppen.has(ordner)) gruppen.set(ordner, []);
      gruppen.get(ordner).push(s);
    }
    for (const [ordner, liste] of gruppen) {
      if (ordner) _inhalt.appendChild(el('h3', 'wv-ordner', ordner));
      const ul = el('ul', 'wv-liste');
      liste.forEach(s => ul.appendChild(eintrag(s, false)));
      _inhalt.appendChild(ul);
    }
    if (_index.gekappt) _inhalt.appendChild(el('p', 'wv-hinweis', I18n.t('wissen.index-gekappt')));
  }

  async function zeigeSeite(pfad) {
    /* Kritiker #739 K-4a: eine noch wartende Suche überschriebe sonst die
       Seite, die gerade geöffnet wird. */
    clearTimeout(_suchTimer);
    const lauf = ++_lauf;
    _zeigtTreffer = false;
    _zurueck.hidden = false;
    hinweis(I18n.t('wissen.laden'));
    const r = await holeJSON('/wissen/seite?pfad=' + encodeURIComponent(pfad));
    if (lauf !== _lauf) return;
    if (!r.ok || typeof r.body?.inhalt !== 'string') {
      setzeFuss(null);
      hinweis(fehlerText(r.status));
      return;
    }
    _inhalt.textContent = '';
    const md = Markdown.render(r.body.inhalt, null, { sicher: true });
    md.addEventListener('click', (e) => {
      const a = e.target.closest?.('a.md-seitenlink');
      if (!a) return;
      e.preventDefault();
      const ziel = loeseSeitenlink(r.body.pfad, a.dataset.seite, _seiten);
      if (ziel) { zeigeSeite(ziel); return; }
      /* Kritiker #739 K-4d: die gelesene Seite bleibt stehen. */
      _inhalt.querySelector('.wv-linkhinweis')?.remove();
      const p = el('p', 'wv-hinweis wv-linkhinweis', I18n.t('wissen.link-tot', a.dataset.seite));
      /* F-2876: frisch erzeugt → über die dauerhafte Live-Region ansagen. */
      p.setAttribute('aria-hidden', 'true');
      ansagen(p.textContent);
      _inhalt.prepend(p);
    });
    _inhalt.appendChild(md);
    setzeFuss(r.body.bearbeiten);
    _modal.body.scrollTop = 0;
    /* K-4e: der geklickte Eintrag ist weg — Fokus auf den Seiteninhalt. */
    _inhalt.focus({ preventScroll: true });
  }

  async function suche(q) {
    const lauf = ++_lauf;
    /* Schon beim Absenden: Leert jemand das Feld, während die Antwort (oder
       ein Fehler) noch aussteht, gilt sie als verworfen (Verify #739 W-2). */
    _zeigtTreffer = true;
    _zurueck.hidden = false;
    const r = await holeJSON('/wissen/suche?q=' + encodeURIComponent(q));
    if (lauf !== _lauf) return;
    setzeFuss(null);
    if (!r.ok) { hinweis(fehlerText(r.status)); return; }
    const treffer = Array.isArray(r.body?.treffer) ? r.body.treffer : [];
    if (!treffer.length) { hinweis(I18n.t('wissen.keine-treffer')); return; }
    _inhalt.textContent = '';
    const ul = el('ul', 'wv-liste');
    treffer.forEach(t => ul.appendChild(eintrag(t, true)));
    _inhalt.appendChild(ul);
    if (r.body.gekappt) _inhalt.appendChild(el('p', 'wv-hinweis', I18n.t('wissen.gekappt')));
  }

  function onSucheingabe() {
    clearTimeout(_suchTimer);
    /* Gezählt wie am Server: nach NFC (K-4b). Unter zwei Zeichen weichen
       stehende Treffer wieder der Übersicht; eine gerade gelesene Seite
       bleibt (Verify V-4). */
    const q = _suche.value.trim();
    if ([...q.normalize('NFC')].length < 2) {
      if (_index && _zeigtTreffer) zeigeUebersicht();
      return;
    }
    _suchTimer = setTimeout(() => suche(q), 300);
  }

  function ensureModal() {
    if (_modal && _modal.overlay.isConnected) return;
    ensureStyles();
    _modal = createModal({
      extraPanelClass: 'wv-panel',
      titleId: 'wv-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
    });
    _kopf = el('div', 'wv-kopf');
    _suche = el('input', 'wv-suche');
    _suche.type = 'search';
    _suche.placeholder = I18n.t('wissen.suche');
    _suche.setAttribute('aria-label', I18n.t('wissen.suche'));
    _suche.maxLength = 200;
    _suche.addEventListener('input', onSucheingabe);
    _zurueck = el('button', 'wv-zurueck', I18n.t('wissen.zurueck'));
    _zurueck.type = 'button';
    _zurueck.addEventListener('click', () => {
      clearTimeout(_suchTimer);
      _suche.value = '';
      zeigeUebersicht();
    });
    _kopf.appendChild(_suche);
    _kopf.appendChild(_zurueck);
    _inhalt = el('div', 'wv-inhalt');
    _inhalt.tabIndex = -1;
    _fuss = el('div', 'wv-fuss');
    _modal.body.appendChild(_kopf);
    _modal.body.appendChild(_inhalt);
    _modal.body.appendChild(_fuss);
  }

  async function open(name) {
    ensureModal();
    _modal.setTitle(name || I18n.t('sidebar.wissen'));
    _suche.value = '';
    _zurueck.hidden = true;
    _fuss.textContent = '';
    hinweis(I18n.t('wissen.laden'));
    _modal.open();
    const lauf = ++_lauf;
    const r = await holeJSON('/wissen/index');
    if (lauf !== _lauf) return;
    if (!r.ok || !Array.isArray(r.body?.seiten)) {
      _index = null;
      _seiten = new Set();
      hinweis(fehlerText(r.status === 404 ? 0 : r.status));
    } else {
      _index = r.body;
      _seiten = new Set(_index.seiten.map(s => s.pfad));
      if (_index.name) _modal.setTitle(_index.name);
      zeigeUebersicht();
    }
    _modal.focusAfterRender(_suche);
  }

  function close() {
    clearTimeout(_suchTimer);
    _lauf++;
    _modal?.close();
  }

  WissenView = { open, close };
}

export { WissenView };
export const __testingWissen__ = { loeseSeitenlink };
