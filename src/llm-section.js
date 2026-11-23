/* llm-section.js — Sektion „Konto“ (KI-Anbieter u. a.) in den Benutzer-Einstellungen
 * (Karte #703; zuvor eigene Sidebar-Sektion, Karte #298, ADR-0094 §Dialog).
 *
 * Die Schlüssel hängen an der PERSON (`llm_api_keys` je user_id), nicht am
 * Board — deshalb gehört die Sektion in den Dialog Benutzer-Einstellungen
 * (ADR-0065, Partition `user`). Der Knopf öffnet den bestehenden
 * LLMKeysDialog (Schlüssel, PII-Wahl).
 *
 * Im Firmenmodus (ADR-0133 §2) gibt es keine eigenen Schlüssel: die Sektion
 * nennt nur, was der Betreiber bereitstellt (Ziel, Modelle); der Knopf führt
 * dann nur noch zur PII-Wahl. Der Firmenmodus selbst wird nie im Browser
 * gesetzt (Umgebungsvariablen), die Administration zeigt ihn nur an.
 *
 * Karte #706: dieselbe Sektion trägt darunter die übrigen Personen-Einträge
 * des früheren Sidebar-Abschnitts „Konto“ — Benachrichtigungen,
 * Agenten-Zugänge und (nur bei eingeschalteter Ausgangsschleuse) Schleusen-
 * Schlüssel. Je ein Knopf öffnet den bestehenden Dialog; ein eigenes Modul
 * lohnt nicht, die Zeilen sind drei Aufrufe.
 *
 * Nur Server-Build (IS_SERVER_BUILD): im Solo-Template verschwindet das
 * Modul samt CSS-String per DCE (ADR-0064/0114).
 */

import { I18n }           from './i18n.js';
import { LLMKeysClient }  from './llm-keys-client.js';
import { LLMKeysDialog }  from './llm-keys-dialog.js';
import { NotificationsDialog }     from './notifications-dialog.js';
import { AgentZugaengeDialog }     from './agent-zugaenge-dialog.js';
import { SchleuseSchluesselDialog } from './schleuse-schluessel.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMSection = null;

if (IS_SERVER_BUILD) {

let _stylesInjected = false;
function _ensureStyles() {
  if (_stylesInjected) return;
  _stylesInjected = true;
  const style = document.createElement('style');
  style.setAttribute('data-aboard', 'llm-section');
  style.textContent = '.config-llm-section{margin-bottom:14px}.llm-section-text{font-size:12px;line-height:1.4;color:var(--text-muted);padding:4px 0 8px}.llm-konto{display:flex;flex-wrap:wrap;gap:6px;padding-top:8px}.llm-section-betreiber{font-size:13px;color:var(--text);padding:4px 0 8px}';
  document.head.appendChild(style);
}

function el(tag, opts = {}) {
  const e = document.createElement(tag);
  if (opts.cls)  e.className = opts.cls;
  if (opts.text != null) e.textContent = opts.text;
  if (opts.type) e.setAttribute('type', opts.type);
  return e;
}

/* Baut die Sektion; `aktualisieren()` lädt den Firmenmodus-Stand neu (bei
   jedem Öffnen des Dialogs). Ohne Antwort des Servers (Fehler, 503) zeigt
   sie den Normalfall — der Dialog meldet den Fehler dann selbst. */
function build() {
  const section = el('section', { cls: 'config-llm-section' });
  section.setAttribute('aria-labelledby', 'cfg-llm-heading');
  const heading = el('div', { cls: 'config-separator', text: I18n.t('konto.section-title') });
  heading.id = 'cfg-llm-heading';
  heading.setAttribute('role', 'heading');
  heading.setAttribute('aria-level', '3');
  section.appendChild(heading);
  const body = el('div', { cls: 'llm-section-body' });
  section.appendChild(body);
  _ensureStyles();

  /* Text und Knopf entstehen einmal; ein Firmenmodus-Treffer tauscht nur
     Text/Klasse/Beschriftung (F-1734: ein fokussierter Knopf bleibt im DOM). */
  const info = el('div');
  const knopf = el('button', { cls: 'btn btn-sm', type: 'button' });
  knopf.addEventListener('click', () => LLMKeysDialog?.open?.());
  body.append(info, knopf);

  let lauf = 0;
  function zeichne(firmenmodus) {
    if (firmenmodus) {
      const modelle = Array.isArray(firmenmodus.modelle) ? firmenmodus.modelle.join(', ') : '-';
      info.className = 'llm-section-betreiber';
      info.textContent = I18n.t('llm.section-betreiber', firmenmodus.ziel || '?', modelle);
    } else {
      info.className = 'llm-section-text';
      info.textContent = I18n.t('llm.section-intro');
    }
    knopf.textContent = I18n.t(firmenmodus ? 'llm.section-pii' : 'llm.section-verwalten');
  }

  async function aktualisieren() {
    const mein = ++lauf;
    zeichne(null); /* sofort bedienbar; Firmenmodus ersetzt die Anzeige nach dem Laden */
    const res = await LLMKeysClient.list();
    if (mein !== lauf) return; /* neuerer Aufruf hat übernommen */
    if (res?.ok && res.body?.firmenmodus) zeichne(res.body.firmenmodus);
  }

  /* Karte #706: Konto-Zeilen (früher Sidebar). Der Schleusen-Eintrag kommt
     erst, wenn GET /me `schleuse: "an"` meldet (ergaenzeMenue, dedupliziert). */
  const konto = el('div', { cls: 'llm-konto' });
  section.appendChild(konto);
  const zeile = (icon, key, dialog) => {
    const knopf = el('button', { cls: 'btn btn-sm', type: 'button', text: icon + ' ' + I18n.t(key) + '…' });
    knopf.addEventListener('click', () => dialog?.open?.());
    return knopf;
  };
  konto.appendChild(zeile('🔔', 'notif.menu', NotificationsDialog));
  konto.appendChild(zeile('🔑', 'agz.menu', AgentZugaengeDialog));
  const kontoErgaenzen = () => SchleuseSchluesselDialog?.ergaenzeMenue?.(konto,
    (text, onClick, o) => zeile(o?.icon || '', 'schleuse.menu', SchleuseSchluesselDialog));

  return { el: section, aktualisieren: () => { kontoErgaenzen(); return aktualisieren(); } };
}

LLMSection = { build };
}

export { LLMSection };
