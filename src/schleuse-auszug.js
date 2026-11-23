/* schleuse-auszug.js — „Extern bearbeiten“: Auszug und Durchsicht der
 * Ausgangsschleuse (Karte #685, ADR-0134 §3.1–§3.4 — Teil S2).
 *
 * Drei Dinge, alle nur im Server-Build und nur bei eingeschalteter Schleuse:
 *  1. `aktualisiereKnopf`: Der Knopf „Extern bearbeiten“ im Karten-Modal ist
 *     nur sichtbar, wenn GET /me `schleuse: "an"` meldet UND die Person
 *     einen nutzbaren eigenen Anbieter hat und die Karte bearbeiten darf —
 *     oder schon einen Entwurf an der Karte hat. Dann trägt er die
 *     Markierung „Entwurf vorhanden“ (nur für die Person selbst: Ein Entwurf
 *     ist privat, er ist noch nirgends hingegangen).
 *  2. Auszug erstellen: Anbieter/Modell, Teile (Beschreibung, einzelne
 *     Kommentare, eigene Aufgabe). Den Text der Teile liest der SERVER aus
 *     dem gespeicherten Board — der Browser schickt nur die Auswahl.
 *  3. Durchsicht: Der pseudonymisierte Auszug mit markierten Platzhaltern
 *     (Art, Quelle fest/Modell — die Originale bleiben auf dem Server), der
 *     Text ist editierbar, Speichern berechnet den Hash neu.
 *  4. Freigabe und Versand (Karte #686, S3): „Freigeben und senden“ bindet
 *     die Freigabe an den Hash des GESPEICHERTEN Stands (ungespeicherte
 *     Änderungen sperren den Knopf). Bei Vier-Augen sieht eine zweite Person
 *     mit Bearbeitungsrechten den Auftrag unter „Wartet auf deine zweite
 *     Freigabe“. Der Server sendet im Hintergrund; der Dialog fragt den
 *     Stand nach, solange etwas unterwegs ist.
 *  5. Antwortkommentar „extern“ (externBeleg): Kennzeichnung, aufklappbarer
 *     gesendeter Auszug und der Hinweis „weicht vom Original ab“, wenn der
 *     Kommentar nicht mehr dem entspricht, was der Server geschrieben hat.
 *  6. Dateien (Karte #687, S4): Anhänge der Karte wählen. Text und PDF liest
 *     der SERVER aus und pseudonymisiert sie mit dem Rest; Bilder gehen nie
 *     pseudonymisiert hinaus — in der Durchsicht je Bild eine eigene
 *     Freigabe mit Warnhinweis. Die Freigaben zählen wie Textänderungen:
 *     ungespeichert sperren sie „Freigeben und senden“.
 *
 * Getrennt vom KI-Chat (ADR-0133 §2): eigene Routen; src/llm-tab.js bleibt
 * unberührt.
 *
 * IS_SERVER_BUILD-gegated → im Slim-/Claude-Code-Build per DCE eliminiert,
 * samt CSS (_ensureStyles) und i18n-Schlüsseln.
 */

import { I18n } from './i18n.js';
import { createModal } from './modal-shell.js';
import { Rueckmeldung } from './rueckmeldung.js';
import { humanSize } from './attachments.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let SchleuseAuszug = null;
let SchleuseAuszugClient = null;

if (IS_SERVER_BUILD) {
  /* Antwort lesen. Rechnet das lokale Modell länger, schickt der Server
     sofort 200 und Leerzeichen als Lebenszeichen (ADR-0133 §3); den
     eigentlichen Status trägt dann das Objekt im Feld `status`. */
  async function anfrage(method, path, payload) {
    try {
      const res = await holen(path, {
        method,
        headers: payload === undefined ? {} : { 'Content-Type': 'application/json' },
        body: payload === undefined ? null : JSON.stringify(payload),
      });
      const body = await res.json().catch(() => null);
      const status = typeof body?.status === 'number' ? body.status : res.status;
      return { ok: status >= 200 && status < 300, status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  const basis = (boardId, cardId) =>
    `/boards/${encodeURIComponent(boardId)}/cards/${encodeURIComponent(cardId)}/schleuse-auftraege`;

  SchleuseAuszugClient = {
    me() { return anfrage('GET', '/me'); },
    /* → { anbieter: [{id, anbieter, bezeichnung, modelle, nutzbar}], schluessel: [{freigabe_id}] } */
    schluessel() { return anfrage('GET', '/users/me/schleuse-schluessel'); },
    /* → { auftraege, gesperrt, darf_erstellen, lokales_modell, grenze_zeichen } */
    liste(boardId, cardId) { return anfrage('GET', basis(boardId, cardId)); },
    erstellen(boardId, cardId, auswahl) { return anfrage('POST', basis(boardId, cardId), auswahl); },
    lesen(boardId, cardId, id) { return anfrage('GET', `${basis(boardId, cardId)}/${encodeURIComponent(id)}`); },
    /* bilderFrei: vollständige Liste der Bild-ids, die mitgehen sollen
       (Karte #687); weggelassen = Freigaben unverändert. */
    speichern(boardId, cardId, id, text, basisSha256, bilderFrei) {
      const body = { text, basis_sha256: basisSha256 };
      if (Array.isArray(bilderFrei)) body.bilder_freigegeben = bilderFrei;
      return anfrage('PUT', `${basis(boardId, cardId)}/${encodeURIComponent(id)}`, body);
    },
    /* Löschen über /users/me (F-1619): geht auch nach Rückstufung auf viewer
       oder Entzug des Zugangs — der eigene Entwurf soll nie hängen bleiben. */
    loeschen(id) { return anfrage('DELETE', `/users/me/schleuse-auftraege/${encodeURIComponent(id)}`); },
    /* Karte #686: Freigabe (eigener Auftrag) und zweite Freigabe (fremder,
       Vier-Augen) — beide an den Hash gebunden, den die Person gesehen hat. */
    freigeben(boardId, cardId, id, sha256) {
      return anfrage('POST', `${basis(boardId, cardId)}/${encodeURIComponent(id)}/freigabe`, { sha256 });
    },
    zweitfreigeben(boardId, cardId, id, sha256) {
      return anfrage('POST', `${basis(boardId, cardId)}/${encodeURIComponent(id)}/zweitfreigabe`, { sha256 });
    },
    beleg(boardId, cardId, id) {
      return anfrage('GET', `/boards/${encodeURIComponent(boardId)}/cards/${encodeURIComponent(cardId)}/schleuse-belege/${encodeURIComponent(id)}`);
    },
  };

  const PLATZHALTER = /\[[A-Z]+_[0-9]+\]/g;
  const GRUENDE = new Set(['board-gesperrt', 'kein-lokales-modell', 'lokales-modell-unerreichbar',
    'lokales-modell-fehler', 'zu-gross', 'auszug-leer', 'kommentar-unbekannt', 'kein-schluessel',
    'anbieter-nicht-nutzbar', 'modell-nicht-erlaubt', 'zu-viele-entwuerfe', 'zu-viele-auszuege',
    'keine-rolle', 'veraltet', 'kein-entwurf', 'karte-unbekannt', 'kein-master-key', 'anbieter-unbekannt',
    'zu-viele-auftraege', 'selbstfreigabe', 'keine-zweitfreigabe', 'kein-versand',
    /* Karte #687: Dateien */
    'datei-unbekannt', 'datei-art', 'datei-zu-gross', 'datei-unlesbar', 'pdf-ohne-text', 'datei-fehlt',
    'zu-viele-dateien', 'zu-viele-bilder', 'keine-bilder', 'kein-anhangspeicher', 'bild-unbekannt']);
  /* Gründe aus dem Versand (fehler_grund), die einen lesbaren Text haben;
     andere erscheinen als Code. */
  const VERSAND_GRUENDE = new Set(['board-gesperrt', 'zu-viele-auftraege', 'unterbrochen', 'nicht-erreichbar',
    'abgelehnt', 'zeitueberschreitung', 'schluessel-abgelehnt', 'karte-fehlt', 'interne-adresse', 'umleitung',
    /* Sitzungs-Kritiker SK-6: alle Gründe, die Server und Versand setzen */
    'keine-rolle', 'kein-schluessel', 'anbieter-nicht-nutzbar', 'modell-nicht-erlaubt', 'hash-abweichung',
    'kommentar-fehlt', 'gekuerzt', 'anbieter-fehler', 'ausgang-gesperrt', 'zu-lang', 'abgebrochen',
    'anbieter-unbekannt', 'intern', 'fehler',
    /* Karte #687: Dateien beim Versand */
    'datei-veraendert', 'datei-fehlt', 'keine-bilder', 'kein-anhangspeicher', 'datei-unlesbar', 'datei-zu-gross']);
  const grundText = (g) => VERSAND_GRUENDE.has(g) ? I18n.t(`schleuse.versand.grund.${g}`) : String(g || '?');

  /* Anhänge der Karte als { id, filename, mime, size, thumb } — auch die
     Alt-Einträge aus dem Mail-Ingest (reine id-Strings). */
  function anhaenge(card) {
    return (Array.isArray(card?.attachments) ? card.attachments : [])
      .map(a => typeof a === 'string' ? { id: a } : a)
      .filter(a => a && typeof a.id === 'string' && a.id);
  }
  function anhangName(card, id) {
    const a = anhaenge(card).find(x => x.id === id);
    return a?.filename || id;
  }

  function fehlertext(r) {
    const g = r?.body?.grund;
    /* Betrifft der Fehler eine Datei, steht ihr Name dabei (Karte #687). */
    const datei = r?.body?.datei_id && _ctx?.card ? ` (${anhangName(_ctx.card, r.body.datei_id)})` : '';
    if (GRUENDE.has(g)) return I18n.t(`schleuse.auszug.fehler.${g}`) + datei;
    if (r?.status === 403) return I18n.t('schleuse.auszug.fehler.keine-rolle');
    return I18n.t('schleuse.fehler-server');
  }

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.type) node.type = opts.type;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  function melde(ziel, art, text) {
    Rueckmeldung.meldeZeile(ziel, 'sla-meldung', art, text);
  }

  /* Eigene, nutzbare Anbieter mit hinterlegtem Schlüssel. */
  function eigeneAnbieter(body) {
    const anbieter = Array.isArray(body?.anbieter) ? body.anbieter : [];
    const schluessel = new Set((Array.isArray(body?.schluessel) ? body.schluessel : []).map(s => s.freigabe_id));
    return anbieter.filter(a => a.nutzbar && schluessel.has(a.id) && Array.isArray(a.modelle) && a.modelle.length);
  }

  const offen = (a) => a.zustand === 'entwurf' || a.zustand === 'freigegeben';
  const kurzHash = (h) => String(h || '').slice(0, 12) + '…';
  const zeit = (iso) => String(iso || '').slice(0, 16).replace('T', ' ');
  /* Unterwegs: freigegeben und auf nichts mehr wartend, oder gesendet. */
  const unterwegs = (a) => a.zustand === 'gesendet' ||
    (a.zustand === 'freigegeben' && (!a.vier_augen || !!a.zweit_freigegeben_von));
  function zustandText(a) {
    if (a.zustand === 'freigegeben') {
      return I18n.t(a.vier_augen && !a.zweit_freigegeben_von ? 'schleuse.versand.zustand.wartet-zweit' : 'schleuse.versand.zustand.wird-gesendet');
    }
    if (a.zustand === 'fehlgeschlagen') return I18n.t('schleuse.versand.zustand.fehlgeschlagen', grundText(a.fehler_grund));
    if (a.zustand === 'gesendet' || a.zustand === 'beantwortet') return I18n.t(`schleuse.versand.zustand.${a.zustand}`);
    return '';
  }

  /* GET /me einmal je Seitenladung (der Schalter ändert sich nur mit einem
     Neustart des Servers); ein Fehlschlag wird nicht gemerkt. */
  let _anVersprechen = null;
  function schleuseAn() {
    if (!_anVersprechen) {
      _anVersprechen = SchleuseAuszugClient.me().then((r) => {
        if (!r.ok) { _anVersprechen = null; return false; }
        return r.body?.schleuse === 'an';
      });
    }
    return _anVersprechen;
  }
  function vergissSchalter() { _anVersprechen = null; }

  /* ── Knopf im Karten-Modal ─────────────────────────────────────────── */

  let _knopfLauf = 0;
  async function aktualisiereKnopf(btn, card, boardId) {
    if (!btn) return;
    const lauf = ++_knopfLauf;
    btn.style.display = 'none';
    btn.classList.remove('sla-knopf--entwurf');
    if (!card?.id || card.kind === 'sprint-meta' || !boardId) return;
    if (!(await schleuseAn())) return;
    const [k, l] = await Promise.all([SchleuseAuszugClient.schluessel(), SchleuseAuszugClient.liste(boardId, card.id)]);
    if (lauf !== _knopfLauf || !btn.isConnected || !l.ok) return;
    const alle = Array.isArray(l.body?.auftraege) ? l.body.auftraege : [];
    const entwuerfe = alle.filter(a => a.zustand === 'entwurf');
    const wartet = alle.some(unterwegs);
    const zweit = (Array.isArray(l.body?.zur_freigabe) ? l.body.zur_freigabe : []).length;
    const kannNeu = !!l.body?.darf_erstellen && k.ok && eigeneAnbieter(k.body).length > 0;
    if (!entwuerfe.length && !wartet && !zweit && !kannNeu) return;
    ensureStyles();
    btn.textContent = wartet ? I18n.t('schleuse.versand.knopf-wartet')
      : entwuerfe.length ? I18n.t('schleuse.auszug.knopf-entwurf', entwuerfe.length)
      : I18n.t('schleuse.auszug.knopf');
    btn.title = I18n.t('schleuse.auszug.knopf-titel');
    if (entwuerfe.length || wartet || zweit) btn.classList.add('sla-knopf--entwurf');
    btn.style.display = '';
  }

  /* ── Dialog ────────────────────────────────────────────────────────── */

  let _modal = null;
  let _bodyEl = null;
  let _lauf = 0;
  let _ctx = null; /* { boardId, card, onAenderung } */

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'sla-overlay',
      extraPanelClass: 'sla-panel',
      titleId: 'sla-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
      onRequestClose: () => close(),
    });
    _modal.setTitle(I18n.t('schleuse.auszug.titel'));
    _bodyEl = _modal.body;
  }

  function leeren() { if (_bodyEl) _bodyEl.innerHTML = ''; }

  async function renderStart(meldung) {
    const lauf = _lauf;
    leeren();
    _bodyEl.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.laden') }));
    const { boardId, card } = _ctx;
    const [k, l] = await Promise.all([SchleuseAuszugClient.schluessel(), SchleuseAuszugClient.liste(boardId, card.id)]);
    if (lauf !== _lauf) return;
    leeren();
    if (!l.ok) { _bodyEl.appendChild(el('p', { cls: 'sla-fehler', text: fehlertext(l) })); return; }
    if (meldung) {
      const m = el('p', { cls: 'sla-meldung' });
      melde(m, 'ok', meldung);
      _bodyEl.appendChild(m);
    }
    _bodyEl.appendChild(el('p', { cls: 'sla-intro', text: I18n.t('schleuse.auszug.intro') }));

    const alle = Array.isArray(l.body?.auftraege) ? l.body.auftraege : [];
    const auftraege = alle.filter(offen);
    if (auftraege.length) {
      _bodyEl.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.auszug.entwuerfe') }));
      for (const a of auftraege) _bodyEl.appendChild(renderEntwurfZeile(a));
    }
    /* Karte #686: Gesendete Aufträge der Person und fremde, die auf ihre
       zweite Freigabe warten. */
    const gesendet = alle.filter(a => !offen(a));
    if (gesendet.length) {
      _bodyEl.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.versand.auftraege') }));
      for (const a of gesendet) _bodyEl.appendChild(renderAuftragZeile(a));
    }
    const zur = Array.isArray(l.body?.zur_freigabe) ? l.body.zur_freigabe : [];
    if (zur.length) {
      _bodyEl.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.versand.zur-freigabe') }));
      for (const z of zur) _bodyEl.appendChild(renderZweitZeile(z));
    }
    planeNachfrage(alle);

    const anbieter = k.ok ? eigeneAnbieter(k.body) : [];
    let hinweis = '';
    if (l.body?.gesperrt) hinweis = I18n.t('schleuse.auszug.gesperrt');
    else if (!l.body?.lokales_modell) hinweis = I18n.t('schleuse.auszug.kein-modell');
    else if (!l.body?.darf_erstellen) hinweis = I18n.t('schleuse.auszug.keine-rolle');
    else if (!anbieter.length) hinweis = I18n.t('schleuse.auszug.kein-anbieter');
    if (hinweis) { _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark', text: hinweis })); return; }
    _bodyEl.appendChild(renderFormular(anbieter, l.body?.grenze_zeichen));
  }

  /* Solange ein eigener Auftrag unterwegs ist, fragt der Dialog alle 4 s
     nach — nur, solange er die Startansicht zeigt (_lauf). */
  let _nachfrage = null;
  function planeNachfrage(alle) {
    clearTimeout(_nachfrage);
    if (!alle.some(unterwegs)) return;
    const lauf = _lauf;
    _nachfrage = setTimeout(() => {
      if (lauf !== _lauf || !_ctx) return;
      /* Kritiker KS3-6: Wer im Formular für den nächsten Auszug schreibt
         oder auswählt, verlöre das beim Neuaufbau — dann später nachfragen. */
      /* Nur das Aufgabenfeld zählt: Den Fokus setzt der Dialog beim Öffnen
         selbst ins Formular — daran gemessen fragte er nie wieder nach. */
      const aufgabe = _bodyEl?.querySelector('.sla-form .sla-aufgabe');
      if (aufgabe && (aufgabe.value || document.activeElement === aufgabe)) {
        planeNachfrage(alle);
        return;
      }
      _ctx.onAenderung?.();
      renderStart();
    }, 4000);
  }

  function renderEntwurfZeile(a) {
    const zeile = el('div', { cls: 'sla-entwurf' });
    zeile.dataset.id = a.id;
    zeile.dataset.zustand = a.zustand;
    let text = I18n.t('schleuse.auszug.entwurf-zeile', zeit(a.geaendert_am), a.anbieter, a.modell, kurzHash(a.sha256));
    if (a.zustand === 'freigegeben') text += ' · ' + zustandText(a);
    else if (a.fehler_grund) text += ' · ' + I18n.t('schleuse.versand.aufgehoben', grundText(a.fehler_grund));
    zeile.appendChild(el('span', { cls: 'sla-entwurf-text', text }));
    const oeffnen = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.auszug.btn-oeffnen') });
    oeffnen.addEventListener('click', () => renderDurchsicht(a));
    const loeschen = el('button', { cls: 'sla-btn sla-btn--danger', type: 'button', text: I18n.t('schleuse.auszug.btn-loeschen') });
    loeschen.addEventListener('click', () => loescheEntwurf(a, loeschen));
    zeile.appendChild(oeffnen);
    zeile.appendChild(loeschen);
    return zeile;
  }

  async function loescheEntwurf(a, knopf) {
    if (!window.confirm(I18n.t('schleuse.auszug.confirm-loeschen'))) return;
    knopf.disabled = true;
    const lauf = _lauf;
    const r = await SchleuseAuszugClient.loeschen(a.id);
    if (lauf !== _lauf) return;
    knopf.disabled = false;
    if (!r.ok) {
      const m = el('p', { cls: 'sla-meldung' });
      melde(m, 'err', fehlertext(r));
      _bodyEl.insertBefore(m, _bodyEl.firstChild);
      return;
    }
    _ctx.onAenderung?.();
    renderStart(I18n.t('schleuse.auszug.geloescht'));
  }

  function renderAuftragZeile(a) {
    const zeile = el('div', { cls: 'sla-entwurf sla-auftrag' });
    zeile.dataset.id = a.id;
    zeile.dataset.zustand = a.zustand;
    zeile.appendChild(el('span', { cls: 'sla-entwurf-text', text: I18n.t('schleuse.versand.auftrag-zeile',
      zeit(a.gesendet_am || a.geaendert_am), a.anbieter, a.modell, zustandText(a)) }));
    const ansehen = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.versand.btn-ansehen') });
    ansehen.addEventListener('click', () => renderErgebnis(a));
    zeile.appendChild(ansehen);
    return zeile;
  }

  function renderErgebnis(a) {
    _lauf++;
    leeren();
    _bodyEl.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.versand.auftrag-zeile',
      zeit(a.gesendet_am || a.geaendert_am), a.anbieter, a.modell, zustandText(a)) }));
    _bodyEl.appendChild(el('p', { cls: 'sla-hash', text: I18n.t('schleuse.auszug.hash', a.sha256 || '') }));
    if (a.input_tokens || a.output_tokens || a.kosten_usd) {
      _bodyEl.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.versand.kosten',
        a.input_tokens || 0, a.output_tokens || 0, Number(a.kosten_usd || 0).toFixed(4)) }));
    }
    if (Array.isArray(a.veraenderte_platzhalter) && a.veraenderte_platzhalter.length) {
      _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark sla-veraendert',
        text: I18n.t('schleuse.versand.veraendert', a.veraenderte_platzhalter.join(', ')) }));
    }
    _bodyEl.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.versand.gesendeter-auszug') }));
    const auszug = el('div', { cls: 'sla-vorschau' });
    fuelleVorschau(auszug, a.text || '', a.ersetzungen);
    _bodyEl.appendChild(auszug);
    const gesendeteBilder = (Array.isArray(a.bilder) ? a.bilder : []).filter(b => b.freigegeben);
    if (gesendeteBilder.length) {
      _bodyEl.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.dateien.bilder-gesendet', gesendeteBilder.length) }));
      for (const b of gesendeteBilder) _bodyEl.appendChild(bildZeile(b, null));
    }
    if (a.antwort_text) {
      _bodyEl.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.versand.antwort') }));
      _bodyEl.appendChild(el('div', { cls: 'sla-vorschau sla-antwort', text: a.antwort_text }));
    }
    const zurueck = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.auszug.btn-zurueck') });
    zurueck.addEventListener('click', () => { _lauf++; renderStart(); });
    _bodyEl.appendChild(zurueck);
  }

  function renderZweitZeile(z) {
    const zeile = el('div', { cls: 'sla-entwurf sla-zweit' });
    zeile.dataset.id = z.id;
    zeile.appendChild(el('span', { cls: 'sla-entwurf-text', text: I18n.t('schleuse.versand.zweit-zeile',
      z.antragsteller || '?', z.anbieter, z.modell, kurzHash(z.sha256)) }));
    const durchsehen = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.versand.btn-durchsehen') });
    durchsehen.addEventListener('click', () => renderZweit(z));
    zeile.appendChild(durchsehen);
    return zeile;
  }

  /* Zweite Freigabe: lesen, nicht ändern — geändert wird nur von der
     antragstellenden Person (dann ist die Freigabe ohnehin weg). */
  function renderZweit(z) {
    _lauf++;
    leeren();
    _bodyEl.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.versand.zweit-titel', z.antragsteller || '?') }));
    _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark', text: I18n.t('schleuse.versand.zweit-hinweis') }));
    const vorschau = el('div', { cls: 'sla-vorschau' });
    fuelleVorschau(vorschau, z.text || '', z.ersetzungen);
    _bodyEl.appendChild(vorschau);
    /* Karte #687: Die zweite Person gibt die Bilder mit frei — sie sieht,
       welche mitgehen. */
    const freiBilder = (Array.isArray(z.bilder) ? z.bilder : []).filter(b => b.freigegeben);
    if (freiBilder.length) {
      _bodyEl.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.dateien.bilder-gehen-mit', freiBilder.length) }));
      _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark sla-bild-warnung', text: I18n.t('schleuse.dateien.bild-warnung') }));
      for (const b of freiBilder) _bodyEl.appendChild(bildZeile(b, null, z.id));
    }
    _bodyEl.appendChild(el('p', { cls: 'sla-hash', text: I18n.t('schleuse.auszug.hash', z.sha256 || '') }));
    _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-verantwortung', text: I18n.t('schleuse.versand.verantwortung') }));
    const msg = el('div', { cls: 'sla-meldung', attrs: { role: 'status' } });
    const knoepfe = el('div', { cls: 'sla-knoepfe' });
    const freigeben = el('button', { cls: 'sla-btn sla-btn--primary sla-zweit-freigeben', type: 'button', text: I18n.t('schleuse.versand.btn-zweit') });
    freigeben.addEventListener('click', async () => {
      if (!window.confirm(I18n.t('schleuse.versand.zweit-confirm', kurzHash(z.sha256)))) return;
      freigeben.disabled = true;
      melde(msg, 'busy', I18n.t('schleuse.arbeitet'));
      const lauf = _lauf;
      const r = await SchleuseAuszugClient.zweitfreigeben(_ctx.boardId, _ctx.card.id, z.id, z.sha256);
      if (lauf !== _lauf) return;
      freigeben.disabled = false;
      if (!r.ok) { melde(msg, 'err', fehlertext(r)); return; }
      _ctx.onAenderung?.();
      renderStart(I18n.t('schleuse.versand.zweit-ok'));
    });
    const zurueck = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.auszug.btn-zurueck') });
    zurueck.addEventListener('click', () => { _lauf++; renderStart(); });
    knoepfe.appendChild(freigeben);
    knoepfe.appendChild(zurueck);
    _bodyEl.appendChild(knoepfe);
    _bodyEl.appendChild(msg);
  }

  function renderFormular(anbieter, grenze) {
    const { card } = _ctx;
    const form = el('div', { cls: 'sla-form' });
    form.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.auszug.neu') }));
    form.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.auszug.gespeicherter-stand') }));

    const anbieterSel = el('select', { cls: 'sla-anbieter', attrs: { 'aria-label': I18n.t('schleuse.auszug.anbieter') } });
    for (const a of anbieter) {
      anbieterSel.appendChild(el('option', { text: a.bezeichnung ? `${a.bezeichnung} (${a.anbieter})` : a.anbieter,
        attrs: { value: a.id } }));
    }
    const modellSel = el('select', { cls: 'sla-modell', attrs: { 'aria-label': I18n.t('schleuse.auszug.modell') } });
    const fuelleModelle = () => {
      modellSel.innerHTML = '';
      const a = anbieter.find(x => x.id === anbieterSel.value) || anbieter[0];
      for (const m of a?.modelle || []) modellSel.appendChild(el('option', { text: m, attrs: { value: m } }));
    };
    anbieterSel.addEventListener('change', fuelleModelle);
    fuelleModelle();
    const zeile = el('div', { cls: 'sla-zeile' });
    zeile.appendChild(labelUm(I18n.t('schleuse.auszug.anbieter'), anbieterSel));
    zeile.appendChild(labelUm(I18n.t('schleuse.auszug.modell'), modellSel));
    form.appendChild(zeile);

    form.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.auszug.teile') }));
    const beschreibung = el('input', { type: 'checkbox', cls: 'sla-teil-beschreibung' });
    const hatBeschreibung = !!String(card.description || '').trim();
    beschreibung.checked = hatBeschreibung;
    beschreibung.disabled = !hatBeschreibung;
    form.appendChild(checkboxZeile(beschreibung, I18n.t('schleuse.auszug.beschreibung')));
    const kommentarBoxen = [];
    for (const k of Array.isArray(card.comments) ? card.comments : []) {
      if (!k?.id || !String(k.text || '').trim()) continue;
      const cb = el('input', { type: 'checkbox', cls: 'sla-teil-kommentar' });
      cb.dataset.id = k.id;
      kommentarBoxen.push(cb);
      const anfang = String(k.text).replace(/\s+/g, ' ').slice(0, 80);
      form.appendChild(checkboxZeile(cb, I18n.t('schleuse.auszug.kommentar', k.author || '?', anfang)));
    }
    /* Karte #687 (ADR-0134 §5): Dateien der Karte. Die Art bestimmt der
       Server am Inhalt; der Hinweis hier richtet sich nach der Angabe der
       Karte und dient nur der Orientierung. */
    const dateiBoxen = [];
    const liste = anhaenge(card);
    if (liste.length) {
      form.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.dateien.titel') }));
      for (const a of liste) {
        const cb = el('input', { type: 'checkbox', cls: 'sla-teil-datei' });
        cb.dataset.id = a.id;
        dateiBoxen.push(cb);
        const mime = String(a.mime || '');
        const art = mime.startsWith('image/') ? 'bild' : (mime === 'application/pdf' || mime.startsWith('text/')) ? 'text' : 'sonst';
        const zeile = checkboxZeile(cb, `${a.filename || a.id}${a.size ? ' · ' + humanSize(a.size) : ''}`);
        zeile.appendChild(el('span', { cls: `sla-datei-art sla-datei-art--${art}`, text: ' ' + I18n.t(`schleuse.dateien.art-${art}`) }));
        form.appendChild(zeile);
      }
    }
    const aufgabe = el('textarea', { cls: 'sla-aufgabe', attrs: { rows: '3', 'aria-label': I18n.t('schleuse.auszug.aufgabe'),
      placeholder: I18n.t('schleuse.auszug.aufgabe') } });
    form.appendChild(labelUm(I18n.t('schleuse.auszug.aufgabe'), aufgabe));
    if (grenze) form.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.auszug.grenze', grenze) }));

    const erstellen = el('button', { cls: 'sla-btn sla-btn--primary', type: 'button', text: I18n.t('schleuse.auszug.btn-erstellen') });
    const msg = el('div', { cls: 'sla-meldung', attrs: { role: 'status' } });
    erstellen.addEventListener('click', async () => {
      const auswahl = {
        freigabe_id: anbieterSel.value,
        modell: modellSel.value,
        beschreibung: beschreibung.checked,
        kommentare: kommentarBoxen.filter(cb => cb.checked).map(cb => cb.dataset.id),
        aufgabe: aufgabe.value,
        dateien: dateiBoxen.filter(cb => cb.checked).map(cb => cb.dataset.id),
      };
      if (!auswahl.beschreibung && !auswahl.kommentare.length && !auswahl.aufgabe.trim() && !auswahl.dateien.length) {
        melde(msg, 'err', I18n.t('schleuse.auszug.fehler.auszug-leer'));
        return;
      }
      erstellen.disabled = true;
      melde(msg, 'busy', I18n.t('schleuse.auszug.arbeitet'));
      const lauf = _lauf;
      const r = await SchleuseAuszugClient.erstellen(_ctx.boardId, _ctx.card.id, auswahl);
      if (lauf !== _lauf) return;
      erstellen.disabled = false;
      if (!r.ok || !r.body?.auftrag) { melde(msg, 'err', fehlertext(r)); return; }
      _ctx.onAenderung?.();
      const hinweise = [I18n.t('schleuse.auszug.erstellt', (r.body.auftrag.ersetzungen || []).length)];
      if (r.body.entschaerft > 0) hinweise.push(I18n.t('schleuse.auszug.entschaerft', r.body.entschaerft));
      renderDurchsicht(r.body.auftrag, hinweise.join(' '));
    });
    form.appendChild(erstellen);
    form.appendChild(msg);
    return form;
  }

  function labelUm(text, feld) {
    const l = el('label', { cls: 'sla-feld' });
    l.appendChild(el('span', { cls: 'sla-label', text }));
    l.appendChild(feld);
    return l;
  }

  function checkboxZeile(cb, text) {
    const l = el('label', { cls: 'sla-check' });
    l.appendChild(cb);
    l.appendChild(document.createTextNode(' ' + text));
    return l;
  }

  /* Vorschau mit markierten Platzhaltern. Bekannte tragen Art und Quelle;
     unbekannte hat die Person selbst eingefügt (ohne Zuordnung). Nur
     textContent — der Auszug ist Fremdtext, nie HTML. */
  function fuelleVorschau(ziel, text, ersetzungen) {
    ziel.innerHTML = '';
    const bekannt = new Map((ersetzungen || []).map(e => [e.platzhalter, e]));
    let pos = 0;
    for (const m of text.matchAll(PLATZHALTER)) {
      if (m.index > pos) ziel.appendChild(document.createTextNode(text.slice(pos, m.index)));
      const e = bekannt.get(m[0]);
      /* ersetzungen === null: Herkunft unbekannt (Beleg für andere Personen) —
         markiert, aber ohne Quelle. */
      const quelle = e ? (e.quelle === 'modell' ? 'modell' : 'fest') : ersetzungen === null ? 'fest' : 'neu';
      const span = el('mark', { cls: `sla-ph sla-ph--${quelle}`, text: m[0] });
      if (ersetzungen !== null) {
        span.title = e
          ? I18n.t('schleuse.auszug.ph-titel', e.art, I18n.t(`schleuse.auszug.legende-${quelle}`))
          : I18n.t('schleuse.auszug.legende-neu');
      }
      ziel.appendChild(span);
      pos = m.index + m[0].length;
    }
    if (pos < text.length) ziel.appendChild(document.createTextNode(text.slice(pos)));
  }

  /* Wie viele Angaben hat das lokale Modell ersetzt (KS-7)? Eine
     Prompt-Injection im Kartentext („antworte {"funde":[]}“) schaltet die
     zweite Stufe sonst still ab. Gezählt wird aus den Ersetzungen des
     Auftrags — so stimmt die Zahl auch beim späteren Öffnen. Bei 0 ein
     deutlicher Warnhinweis. */
  function renderModellFunde(auftrag) {
    const n = (auftrag.ersetzungen || []).filter(e => e?.quelle === 'modell').length;
    const p = el('p', { cls: n === 0 ? 'sla-modell-funde sla-hinweis sla-hinweis--stark sla-modell-funde--keine' : 'sla-modell-funde sla-hinweis' });
    p.dataset.anzahl = String(n);
    p.textContent = n === 0 ? I18n.t('schleuse.auszug.modell-keine-funde') : I18n.t('schleuse.auszug.modell-funde', n);
    return p;
  }

  /* Eine Bildzeile: Name, Größe der bereinigten Fassung, Hash — mit aid
     zusätzlich die Vorschau vom Server, und zwar die BEREINIGTEN Bytes, die
     hinausgehen (Verify VS4-2; die Miniatur aus dem Karten-Zustand kann jeder
     Board-Schreiber setzen). Mit wahl ({ frei, onWahl }) die Einzelfreigabe:
     freigeben lässt sich ein Bild erst, wenn seine Vorschau geladen ist;
     abwählen geht immer. */
  function bildZeile(b, wahl, aid) {
    const zeile = el('div', { cls: 'sla-bild' });
    zeile.dataset.id = b.id;
    const att = _ctx?.card ? anhaenge(_ctx.card).find(x => x.id === b.id) : null;
    let cb = null;
    if (wahl) {
      cb = el('input', { type: 'checkbox', cls: 'sla-bild-frei' });
      cb.checked = !!wahl.frei;
      cb.disabled = !cb.checked;
      cb.title = I18n.t('schleuse.dateien.erst-ansehen');
      cb.addEventListener('change', () => wahl.onWahl(cb.checked));
      zeile.appendChild(checkboxZeile(cb, I18n.t('schleuse.dateien.bild-mitsenden')));
    }
    if (aid && _ctx) {
      const img = el('img', { cls: 'sla-bild-vorschau', attrs: { alt: '',
        src: `${basis(_ctx.boardId, _ctx.card.id)}/${encodeURIComponent(aid)}/bilder/${encodeURIComponent(b.id)}` } });
      img.addEventListener('load', () => { if (cb) { cb.disabled = false; cb.title = ''; } });
      img.addEventListener('error', () => {
        img.replaceWith(el('span', { cls: 'sla-hinweis sla-hinweis--stark sla-bild-fehlt', text: I18n.t('schleuse.dateien.vorschau-fehlt') }));
        /* Sitzungs-Kritiker SK4-6: Auch ein schon gespeichert freigegebenes
           Bild ohne Vorschau wird abgewählt und gesperrt — die Auswahl gilt
           dann als ungespeichert, „Freigeben und senden“ ist zu, bis die
           Person ohne das Bild speichert. */
        if (cb) {
          const war = cb.checked;
          cb.checked = false;
          cb.disabled = true;
          if (war) wahl.onWahl(false);
        }
      });
      zeile.appendChild(img);
    }
    const info = el('span', { cls: 'sla-bild-info', text: I18n.t('schleuse.dateien.bild-zeile',
      att?.filename || b.id, humanSize(b.bytes || 0), kurzHash(b.sha256)) });
    info.title = b.sha256 || '';
    zeile.appendChild(info);
    return zeile;
  }

  function renderDurchsicht(auftrag, meldung) {
    _lauf++;
    leeren();
    let stand = auftrag;
    const kopf = el('div', { cls: 'sla-kopf' });
    kopf.appendChild(el('h3', { cls: 'sla-h', text: I18n.t('schleuse.auszug.durchsicht') }));
    kopf.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark', text: I18n.t('schleuse.auszug.durchsicht-hinweis') }));
    const legende = el('p', { cls: 'sla-legende' });
    for (const q of ['fest', 'modell', 'neu']) {
      legende.appendChild(el('mark', { cls: `sla-ph sla-ph--${q}`, text: I18n.t(`schleuse.auszug.legende-${q}`) }));
      legende.appendChild(document.createTextNode(' '));
    }
    kopf.appendChild(legende);
    kopf.appendChild(renderModellFunde(stand));
    _bodyEl.appendChild(kopf);
    const msg = el('div', { cls: 'sla-meldung', attrs: { role: 'status' } });
    if (meldung) melde(msg, 'ok', meldung);
    _bodyEl.appendChild(msg);

    const vorschau = el('div', { cls: 'sla-vorschau', attrs: { 'aria-live': 'polite' } });
    const feld = el('textarea', { cls: 'sla-text', attrs: { rows: '12', 'aria-label': I18n.t('schleuse.auszug.text'), spellcheck: 'false' } });
    feld.value = stand.text || '';
    const hash = el('p', { cls: 'sla-hash' });
    const zeigeHash = () => {
      hash.textContent = I18n.t('schleuse.auszug.hash', kurzHash(stand.sha256));
      hash.title = stand.sha256 || '';
      if (ungespeichert()) {
        hash.appendChild(el('span', { cls: 'sla-ungespeichert', text: ' — ' + I18n.t('schleuse.auszug.ungespeichert') }));
      }
    };
    let sendenStand = () => {};
    /* Karte #687: Einzelfreigabe der Bilder — Auswahl im Dialog, gespeichert
       erst mit „Speichern“ (dann neuer Hash, wie bei einer Textänderung). */
    let bilderWahl = new Set((stand.bilder || []).filter(b => b.freigegeben).map(b => b.id));
    const bilderGespeichert = () => new Set((stand.bilder || []).filter(b => b.freigegeben).map(b => b.id));
    const bilderGeaendert = () => {
      const g = bilderGespeichert();
      return g.size !== bilderWahl.size || [...g].some(id => !bilderWahl.has(id));
    };
    const ungespeichert = () => feld.value !== stand.text || bilderGeaendert();
    const aktualisiere = () => { fuelleVorschau(vorschau, feld.value, stand.ersetzungen); zeigeHash(); sendenStand(); };
    feld.addEventListener('input', aktualisiere);
    aktualisiere();
    _bodyEl.appendChild(vorschau);
    _bodyEl.appendChild(labelUm(I18n.t('schleuse.auszug.text'), feld));
    const bilderBox = el('div', { cls: 'sla-bilder' });
    const zeigeBilder = () => {
      bilderBox.innerHTML = '';
      const bilder = Array.isArray(stand.bilder) ? stand.bilder : [];
      if (!bilder.length) return;
      bilderBox.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.dateien.bilder-titel') }));
      bilderBox.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark sla-bild-warnung', text: I18n.t('schleuse.dateien.bild-warnung') }));
      for (const b of bilder) bilderBox.appendChild(bildZeile(b, {
        frei: bilderWahl.has(b.id),
        onWahl: (an) => { if (an) bilderWahl.add(b.id); else bilderWahl.delete(b.id); aktualisiere(); },
      }, stand.id));
    };
    zeigeBilder();
    _bodyEl.appendChild(bilderBox);
    _bodyEl.appendChild(hash);

    const knoepfe = el('div', { cls: 'sla-knoepfe' });
    const speichern = el('button', { cls: 'sla-btn sla-btn--primary', type: 'button', text: I18n.t('schleuse.auszug.btn-speichern') });
    speichern.addEventListener('click', async () => {
      speichern.disabled = true;
      melde(msg, 'busy', I18n.t('schleuse.arbeitet'));
      const lauf = _lauf;
      const bilderFrei = (stand.bilder || []).length ? [...bilderWahl] : undefined;
      const r = await SchleuseAuszugClient.speichern(_ctx.boardId, _ctx.card.id, stand.id, feld.value, stand.sha256, bilderFrei);
      if (lauf !== _lauf) return;
      speichern.disabled = false;
      if (!r.ok || !r.body?.auftrag) { melde(msg, 'err', fehlertext(r)); return; }
      stand = r.body.auftrag;
      feld.value = stand.text;
      bilderWahl = bilderGespeichert();
      zeigeBilder();
      aktualisiere();
      melde(msg, 'ok', I18n.t('schleuse.auszug.gespeichert'));
    });
    const loeschen = el('button', { cls: 'sla-btn sla-btn--danger', type: 'button', text: I18n.t('schleuse.auszug.btn-loeschen') });
    loeschen.addEventListener('click', () => loescheEntwurf(stand, loeschen));
    const zurueck = el('button', { cls: 'sla-btn', type: 'button', text: I18n.t('schleuse.auszug.btn-zurueck') });
    zurueck.addEventListener('click', () => { _lauf++; renderStart(); });
    /* Karte #686 (ADR-0134 §3.4): Freigegeben wird der GESPEICHERTE Stand,
       gebunden an seinen Hash — mit ungespeicherten Änderungen bleibt der
       Knopf gesperrt. Ein schon freigegebener Entwurf wartet (Vier-Augen
       oder Versand); eine Änderung hebt die Freigabe auf. */
    const senden = el('button', { cls: 'sla-btn sla-btn--primary sla-senden', type: 'button', text: I18n.t('schleuse.auszug.btn-senden') });
    sendenStand = () => {
      const offenGeblieben = ungespeichert();
      senden.disabled = offenGeblieben || stand.zustand !== 'entwurf';
      senden.title = offenGeblieben ? I18n.t('schleuse.versand.erst-speichern') : '';
    };
    sendenStand();
    senden.addEventListener('click', async () => {
      if (ungespeichert() || stand.zustand !== 'entwurf') return;
      const nBilder = (stand.bilder || []).filter(b => b.freigegeben).length;
      const frage = I18n.t('schleuse.versand.confirm', kurzHash(stand.sha256)) +
        (nBilder ? '\n\n' + I18n.t('schleuse.dateien.confirm-bilder', nBilder) : '');
      if (!window.confirm(frage)) return;
      senden.disabled = true;
      melde(msg, 'busy', I18n.t('schleuse.arbeitet'));
      const lauf = _lauf;
      const r = await SchleuseAuszugClient.freigeben(_ctx.boardId, _ctx.card.id, stand.id, stand.sha256);
      if (lauf !== _lauf) return;
      if (!r.ok || !r.body?.auftrag) { sendenStand(); melde(msg, 'err', fehlertext(r)); return; }
      _ctx.onAenderung?.();
      renderStart(I18n.t(r.body.auftrag.vier_augen ? 'schleuse.versand.wartet-zweit-meldung' : 'schleuse.versand.unterwegs-meldung'));
    });
    knoepfe.appendChild(speichern);
    knoepfe.appendChild(zurueck);
    knoepfe.appendChild(loeschen);
    knoepfe.appendChild(senden);
    _bodyEl.appendChild(knoepfe);
    _bodyEl.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark sla-verantwortung', text: I18n.t('schleuse.versand.verantwortung') }));
    if (stand.zustand === 'freigegeben') {
      _bodyEl.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.versand.schon-freigegeben') + ' (' + zustandText(stand) + ')' }));
    }
  }

  /* ── Antwortkommentar „extern“ ─────────────────────────────────────── */

  /* Beleg je Auftrag, solange der Kommentartext gleich bleibt — das
     Karten-Modal rendert Kommentare oft neu. */
  const _belege = new Map(); /* auftragId → { text, antwort } */

  function renderBelegInhalt(ziel, r) {
    ziel.innerHTML = '';
    const b = r?.ok ? r.body?.beleg : null;
    if (!b) { ziel.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.extern.kein-beleg') })); return; }
    const frei = [b.freigegeben_von, b.zweit_freigegeben_von].filter(Boolean).join(' + ') || '?';
    ziel.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.extern.beleg-zeile', b.absender || '?', frei, zeit(b.gesendet_am), b.sha256 || '') }));
    if (b.input_tokens || b.output_tokens || b.kosten_usd) {
      ziel.appendChild(el('p', { cls: 'sla-hinweis', text: I18n.t('schleuse.versand.kosten',
        b.input_tokens || 0, b.output_tokens || 0, Number(b.kosten_usd || 0).toFixed(4)) }));
    }
    if (Array.isArray(b.veraenderte_platzhalter) && b.veraenderte_platzhalter.length) {
      ziel.appendChild(el('p', { cls: 'sla-hinweis sla-hinweis--stark sla-veraendert',
        text: I18n.t('schleuse.versand.veraendert', b.veraenderte_platzhalter.join(', ')) }));
    }
    ziel.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.versand.gesendeter-auszug') }));
    const auszug = el('div', { cls: 'sla-vorschau' });
    fuelleVorschau(auszug, b.auszug || '', null);
    ziel.appendChild(auszug);
    /* Karte #687: gesendete Bilder mit dem Hash ihrer bereinigten Fassung. */
    const bilder = Array.isArray(b.bilder) ? b.bilder : [];
    if (bilder.length) {
      ziel.appendChild(el('p', { cls: 'sla-label', text: I18n.t('schleuse.dateien.bilder-gesendet', bilder.length) }));
      for (const bild of bilder) ziel.appendChild(bildZeile(bild, null));
    }
  }

  /* Element unter einem Kommentar mit kind „extern“: Kennzeichnung (aus dem
     Kommentar), „weicht vom Original ab“ (vom Server, verglichen mit dem
     Hash, den er beim Schreiben festgehalten hat) und der aufklappbare
     gesendete Auszug. Alles textContent — Auszug und Antwort sind Fremdtext. */
  function externBeleg(comment, boardId, cardId) {
    ensureStyles();
    const s = comment?.schleuse && typeof comment.schleuse === 'object' ? comment.schleuse : {};
    const box = el('div', { cls: 'sla-extern' });
    box.appendChild(el('div', { cls: 'sla-extern-marke', text: I18n.t('schleuse.extern.marke',
      s.anbieter || '?', s.modell || '?', zeit(s.gesendetAm || comment?.created), comment?.authorEmail || '?') }));
    const warnung = el('div', { cls: 'sla-hinweis sla-hinweis--stark sla-extern-abweichend', text: I18n.t('schleuse.extern.abweichend') });
    warnung.hidden = true;
    /* Kritiker KS3-1: Die Kennzeichnung oben steht im Kommentar selbst und
       beweist nichts. Erst der Beleg des Servers — derselbe Kommentar an
       dieser Karte — macht sie verbürgt. Ohne ihn: sichtbar „nicht
       verifiziert“. */
    /* Sitzungs-Kritiker SK-3: Ausgangszustand ist „wird geprüft“, nicht
       „verbürgt“ — ein hängender Abruf zeigt so nie eine stille Bestätigung. */
    const unbelegt = el('div', { cls: 'sla-hinweis sla-extern-unbelegt sla-extern-pruefe', text: I18n.t('schleuse.extern.pruefe') });
    box.appendChild(unbelegt);
    box.appendChild(warnung);
    const knopf = el('button', { cls: 'sla-btn sla-extern-knopf', type: 'button', text: I18n.t('schleuse.extern.beleg-zeigen') });
    const inhalt = el('div', { cls: 'sla-extern-beleg' });
    inhalt.hidden = true;
    box.appendChild(knopf);
    box.appendChild(inhalt);
    const id = typeof s.auftragId === 'string' ? s.auftragId : '';
    /* Gemerkt wird nur ein bestätigter Beleg (Verify VS3-1): Ein Netzfehler
       oder ein Abruf im kurzen Abstand zwischen Kommentar und Abschluss des
       Auftrags darf einen echten Kommentar nicht bis zum Neuladen als „nicht
       verifiziert“ stehen lassen — dann wird nach 5 s einmal nachgefragt. */
    const bestaetigt = (r) => {
      const b = r?.ok ? r.body?.beleg : null;
      return !!(b && b.kommentar_vorhanden && b.kommentar_id === comment?.id);
    };
    /* Gemerkt: ein bestätigter Beleg dauerhaft, ein Nicht-Treffer 60 s
       (SK-3: sonst fragte jeder Neuaufbau des Modals erneut). Die einmalige
       Nachfrage nach 5 s umgeht den Merker (erzwingen). */
    const laden = async (erzwingen) => {
      if (!id || !boardId || !cardId) return { ok: false };
      const c = _belege.get(id);
      if (!erzwingen && c && c.text === comment.text && (c.bis === 0 || c.bis > Date.now())) return c.antwort;
      const r = await SchleuseAuszugClient.beleg(boardId, cardId, id);
      _belege.set(id, { text: comment.text, antwort: r, bis: bestaetigt(r) ? 0 : Date.now() + 60000 });
      return r;
    };
    const zeige = (r) => {
      unbelegt.classList.remove('sla-extern-pruefe');
      unbelegt.classList.add('sla-hinweis--stark');
      unbelegt.textContent = I18n.t('schleuse.extern.nicht-verifiziert');
      unbelegt.hidden = bestaetigt(r);
      warnung.hidden = !(bestaetigt(r) && r.body.beleg.abweichend);
    };
    laden(false).then((r) => {
      zeige(r);
      if (!bestaetigt(r) && id && boardId && cardId) {
        setTimeout(() => { if (box.isConnected) laden(true).then(zeige); }, 5000);
      }
    });
    knopf.addEventListener('click', async () => {
      if (!inhalt.hidden) {
        inhalt.hidden = true;
        knopf.textContent = I18n.t('schleuse.extern.beleg-zeigen');
        return;
      }
      knopf.disabled = true;
      const r = await laden();
      knopf.disabled = false;
      renderBelegInhalt(inhalt, r);
      inhalt.hidden = false;
      knopf.textContent = I18n.t('schleuse.extern.beleg-verbergen');
    });
    return box;
  }

  function ensureStyles() {
    if (document.getElementById('sla-dialog-style')) return;
    const style = document.createElement('style');
    style.id = 'sla-dialog-style';
    style.textContent = `
.sla-panel .modal-header-row{background:#fff;color:#222;border-bottom:1px solid #eee}.sla-panel .modal-dialog-title{color:#222}.sla-panel .btn-modal-close{color:#444;opacity:1}
.sla-panel{background:#fff;color:#222;border-radius:8px;box-shadow:0 12px 36px rgba(0,0,0,.3)}
.sla-intro,.sla-hinweis{color:#555;font-size:.88rem;margin:0 0 8px;line-height:1.45}
.sla-hinweis--stark{color:#7a4b00;background:#fff8e6;border:1px solid #f0d9a8;border-radius:4px;padding:6px 8px}
.sla-h{font-size:.95rem;margin:12px 0 6px}
.sla-entwurf{display:flex;gap:6px;align-items:center;border-top:1px solid #eee;padding:6px 0;font-size:.85rem}.sla-entwurf-text{flex:1;font-family:monospace}
.sla-form{border-top:1px solid #eee;padding-top:4px}
.sla-zeile{display:flex;gap:10px;flex-wrap:wrap}
.sla-feld{display:flex;flex-direction:column;gap:3px;margin:6px 0;flex:1;min-width:180px}
.sla-label{font-size:.85rem;color:#333;font-weight:600;margin:8px 0 4px}
.sla-check{display:block;font-size:.85rem;margin:3px 0;color:#333}
.sla-aufgabe,.sla-text{width:100%;box-sizing:border-box;padding:6px 8px;border:1px solid #ccc;border-radius:4px;font:inherit;font-size:.88rem}
.sla-text{font-family:monospace}
.sla-vorschau{white-space:pre-wrap;border:1px solid #ddd;border-radius:4px;padding:8px;max-height:40vh;overflow:auto;background:#fafafa;color:#222;font-size:.88rem;line-height:1.5}
.sla-ph{border-radius:3px;padding:0 2px;font-family:monospace;font-size:.85em}
.sla-ph--fest{background:#dbeafe;color:#1e3a8a}.sla-ph--modell{background:#ede9fe;color:#4c1d95}.sla-ph--neu{background:#fef3c7;color:#78350f}
.sla-legende{font-size:.8rem;margin:0 0 6px}
.sla-hash{font-family:monospace;font-size:.8rem;color:#444;margin:6px 0}.sla-ungespeichert{color:#b45309;font-family:inherit}
.sla-knoepfe{display:flex;gap:6px;flex-wrap:wrap;margin:8px 0}
.sla-btn{padding:5px 11px;border:1px solid #bbb;background:#f8f8f8;color:#222;cursor:pointer;border-radius:4px;font-size:.85rem}
.sla-btn--primary{background:#2563eb;color:#fff;border-color:#2563eb}
.sla-btn--danger{color:#c00;border-color:#f4baba;background:#fff5f5}
.sla-btn:disabled{opacity:.5;cursor:not-allowed}
.sla-fehler{color:#c00;font-size:.85rem}
.sla-meldung{font-size:.85rem;min-height:1em;margin:4px 0}
.sla-meldung--busy{color:#555;font-style:italic}.sla-meldung--ok{color:#1a7f37;font-weight:600}.sla-meldung--err{color:#c00;font-weight:600}
.sla-knopf--entwurf{border-color:#7c3aed;color:#5b21b6}
.sla-antwort{white-space:pre-wrap}
.sla-extern{margin-top:4px;font-size:12px}.sla-extern-marke{color:#5b21b6;font-weight:600;margin-bottom:2px}
.sla-extern .sla-btn{font-size:12px;padding:2px 8px}.sla-extern-beleg{margin-top:4px}
.sla-datei-art{font-size:.8rem;color:#666}.sla-datei-art--bild{color:#7a4b00}
.sla-bild{display:flex;gap:8px;align-items:center;border-top:1px solid #eee;padding:4px 0;font-size:.85rem}
.sla-bild .sla-check{margin:0;font-weight:600;color:#7a4b00}.sla-bild-info{font-family:monospace;font-size:.8rem;color:#444}
.sla-bild{flex-wrap:wrap}.sla-bild-vorschau{max-width:100%;max-height:320px;border:1px solid #ddd;border-radius:3px;display:block}`;
    document.head.appendChild(style);
  }

  /* boardId/card: die geöffnete Karte; onAenderung: nach Anlegen/Löschen
     (das Karten-Modal aktualisiert dann seinen Knopf). */
  function open(boardId, card, onAenderung) {
    if (!boardId || !card?.id) return;
    _ctx = { boardId, card, onAenderung };
    ensureStyles();
    ensureModal();
    _lauf++;
    _modal.open();
    renderStart().then(() => _modal.focusAfterRender?.());
  }

  function close() {
    _lauf++;
    clearTimeout(_nachfrage);
    leeren();
    _modal?.close();
    _ctx = null;
  }

  SchleuseAuszug = { open, close, aktualisiereKnopf, vergissSchalter, externBeleg };
}

export { SchleuseAuszug, SchleuseAuszugClient };
