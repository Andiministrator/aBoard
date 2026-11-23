import { EventBus }          from './eventbus.js';
import { Shortcuts }         from './shortcuts.js';
import { Markdown }           from './markdown.js';
import { Config }             from './config.js';
import { Profile }            from './profile.js';
import { I18n }               from './i18n.js';
import {
  trashIcon, boldIcon, italicIcon, codeIcon, headingIcon,
  listIcon, orderedListIcon, checklistIcon, linkIcon, quoteIcon,
  hrIcon, maximizeIcon, minimizeIcon,
  downloadIcon, mimeIconFor,
} from './icons.js';
import { addSwipeToDismiss }  from './swipe.js';
import { ermittleOberstesOffenesOverlay } from './modal-shell.js';
import { ansagen } from './ansage.js'; /* F-2876 */
import { State }              from './state.js';
import { LINK_TYPES, TYPE_KEYS, groupLinksByType, isDoneColumnId } from './links.js';
/* Karte #450-Nacharbeit: nur noch die Transport-freien Helfer direkt —
   `verkleinereBild` ist eine reine Bildoperation, `BASE64_FAKTOR` eine
   Konstante. Alles, was einen Speicherort berührt, geht über
   `anhangBackend()`. Der frühere Namespace-Import machte den Unterschied
   unsichtbar: `Attachments.irgendwas` sah überall gleich harmlos aus. */
import { verkleinereBild, BASE64_FAKTOR, normalisiereAnhang, istSvgMime } from './attachments.js';
import { resolveIdentity, buildAvatar, nameForEmail, faerbeLabel } from './identity.js';
import { kandidatenSofort, ladeKandidaten, claudeGruppe, istClaudeKennung, CLAUDE_MODELLE } from './bearbeiter-kandidaten.js'; /* Karte #698, nur gegatet benutzt; CLAUDE_MODELLE ungegatet (#817) */
import { Export }                       from './export.js';
/* #293/ADR-0091: EmailDialog ist im Nicht-Server-Build null und alle
   Konsumenten-Sites in dieser Datei sind hinter IS_SERVER_BUILD-Gates → DCE. */
import { EmailDialog }                  from './email-dialog.js';
import { StoragePlugin, createFsaAttachmentBackend } from './storage-plugin.js';
/* #307/ADR-0102: Projekt-Dropdown im Karten-Modal — server-only.
   ClientsProjectsClient + AdminClient sind im Nicht-Server-Build null,
   alle Konsumenten hier sind hinter IS_SERVER_BUILD-Gates → DCE. */
import { ClientsProjectsClient }        from './clients-projects-client.js';
import { AdminClient }                  from './admin-client.js';
/* #299/ADR-0095: KI-Tab im Karten-Modal — server-only. LLMTab ist im
   Nicht-Server-Build null; alle Konsumenten hier sind IS_SERVER_BUILD-gated → DCE. */
import { LLMTab }                       from './llm-tab.js';
/* Karte #685 (ADR-0134 §3): „Extern bearbeiten“ — im Nicht-Server-Build null,
   alle Konsumenten hier IS_SERVER_BUILD-gated → DCE. */
import { SchleuseAuszug }                from './schleuse-auszug.js';
import { parseCardHash }                from './deeplink.js'; /* F-457/K3 */
import { FINDING_TYPEN, FINDING_PRIORITAETEN, FINDING_STATUS } from './finding.js'; /* ADR-0133 §1 */
import { claimZustand, istVertraulich } from './ops.js';      /* Karte #606/#778, nur gegatet benutzt */
import { boardpaarSeite, spiegelModus, spiegelGebunden } from './boardpaar.js'; /* Karte #778/#827, nur gegatet benutzt */
import { holen, Sitzung } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

/* ── Anhang-Backend (Karte #450) ──────────────────────────────────────
   Anhänge laufen über das Storage-Plugin, nicht mehr direkt über
   `attachments.js`. Der Unterschied ist nicht kosmetisch: Im Server-Modus
   liefert das Plugin `createRESTAttachmentBackend`, das die Datei per
   Multipart an `POST /boards/{id}/attachments/{cardId}` schickt — einen
   eigenen Endpunkt mit eigenem, höherem Limit. Der direkte Import ging daran
   vorbei und legte die Datei als base64 in den Board-State, wo sie beim
   nächsten PUT gegen `MaxBodyBytes` lief. Genau das war die gemeldete „Speichern
   fehlgeschlagen" (F-798: das REST-Backend war vollständig implementiert und
   hatte im gesamten Frontend keinen einzigen Aufrufer).

   ZUM `??`-FALLBACK (korrigiert Sprint 65, F-853): Er ist DEFENSIV UND HEUTE
   UNERREICHBAR — die frühere Begründung „deckt den Zustand vor `selectPlugin`
   ab (früher Render, Tests)" beschrieb einen Zeitpunkt, den es nicht gibt.
   `getActivePlugin()` endet unbedingt auf `return selectPlugin(mode)` und
   liefert damit immer ein Plugin; dass jedes Plugin ein `attachments()` hat,
   ist der Vertrag aus ADR-0044 und wird in `tests/storage-plugin.test.js`
   §„Storage-Plugin — attachments()" für alle drei Varianten einzeln
   zugesichert. Beide `?.` können also nicht greifen.

   Der Zweig bleibt trotzdem stehen: Er kostet nichts und fängt eine künftige
   Plugin-Variante ab, die `attachments()` vergisst — dann liefert er exakt
   dasselbe Backend, das Standalone und Projekt ohnehin liefern, statt an einem
   `undefined` zu sterben. Mitgeprüft und NICHT eingetreten ist der Fall, der
   ihn gefährlich gemacht hätte: im Server-Modus ist er nicht erreichbar, das
   REST-Backend wird also nicht still umgangen. Wer ihn entfernt, verliert nur
   diese Absicherung — nicht Verhalten. */
function anhangBackend() {
  return StoragePlugin.getActivePlugin()?.attachments?.() ?? createFsaAttachmentBackend();
}

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');
/* Karte #606: „Übernahme aufheben“ gibt es nur, wo eine Übernahme entstehen
   kann (Projektor-Inbox, Server) — im slim Template fällt der Block per DCE weg. */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');


/* Karten-Edit-Modal mit Typora-Edit-Modus (→ ADR-0016, ADR-0029)
   Vorher drei Modi (View/Typora/Quelltext, ADR-0017) — abgelöst durch ADR-0029.

   Tab-System (#47): Description / Kommentare / Agent-Kontext.
   Agent-Kontext-Tab nur bei boardMode=project + Claude-Owner sichtbar.

   Pane-Generalisierung: statt einer globalen `rawText`-Variable gibt es
   eine `panes`-Map { key → { text } }. `getPane(key)` / `setPane(key, val)`
   ersetzen direkten Zugriff. `commitTyporaBlock` und `openNewBlockEditor`
   bekommen den Pane-Key als Parameter. */

export const Modal = (() => {
  let overlay         = null;

  /* Karte #606: die Übernahme-Zeile aus `card.claim` befüllen. Eigene Funktion
     seit Karte #618: Lehnt der Server das Aufheben ab, weil inzwischen ein
     anderer hält (halter-geaendert), zeigt das Modal den aktuellen Halter —
     ohne den Rest der Karte neu zu laden. */
  function fuelleClaimZeile(card) {
    const claimRow = overlay && overlay.querySelector('#modal-claim-row');
    if (!claimRow) return;
    const z = card.claim ? claimZustand(card, Date.now(), '\u0000betrachter', null) : 'frei';
    const zeigen = z === 'fremd-aktiv' || z === 'fremd-abgelaufen';
    claimRow.style.display = zeigen ? 'flex' : 'none';
    overlay.querySelector('#modal-claim-grund').value = '';
    overlay.querySelector('#modal-claim-aufheben').disabled = true;
    if (zeigen) {
      const c = card.claim;
      const wer = (c.by || '?') + (c.session ? I18n.t('claim.sitzung', c.session) : '');
      const zeit = new Date(c.until).toLocaleString();
      overlay.querySelector('#modal-claim-text').textContent = z === 'fremd-aktiv'
        ? I18n.t('claim.titel', wer, zeit) : I18n.t('claim.titel.verwaist', wer, zeit);
    }
  }
  let currentCard     = null;
  let pendingComments = [];     /* im Modal neu hinzugefügte Kommentare */
  /* Karte #678 (ADR-0135): für den NÄCHSTEN Kommentar gewählte Dateien.
     Nur im Server-Build befüllt; hochgeladen wird erst beim Hinzufügen. */
  let kommentarDateien = [];

  /* ── Pane-State (generalisiert für desc + agent) ─────────────── */

  /* Enthält den autoritativen Rohtext je Pane */
  const panes = {
    desc:  { text: '' },
    agent: { text: '' },
  };

  function getPane(key)       { return panes[key]?.text ?? ''; }
  function setPane(key, val)  { if (panes[key]) panes[key].text = val; }

  /* Aktiver Tab: 'desc' | 'comments' | 'agent' | 'links' | 'attachments' */
  let activeTab = 'desc';
  /* ADR-0076: Edit-Einstieg des desc-Pane. Default Lese-Modus; per
     „Bearbeiten"-Button umschaltbar. Flüchtig — Reset bei jeder
     Karten-Öffnung. Betrifft NUR desc; agent bleibt ADR-0029. */
  let descEditActive = false;
  /* ADR-0141 §7/§8 (Karte #826): Spiegelkarte einer externen Kopie — bis zum
     Rückweg (2c) nur lesbar. Gesetzt in open(), gilt bis zum nächsten open(). */
  let nurLesen = false;
  /* Karte #827 E4 (ADR-0141 §7): gebundene Karte bei Rückweg an —
     bearbeitbar, was der Rückweg nach innen trägt; gesperrt bleiben Löschen,
     Anhänge, Verweise, „vertraulich aus“, Agent-Kontext, KI und die Projekt-
     und Agentenfelder (qa, Komplexität, Aufwand, Wiederholung, Projekt, …). */
  let rueckwegSperre = false;

  /* ObjectURLs, die beim Schließen des Modals freigegeben werden müssen */
  const activeObjectUrls = new Set();
  function trackObjectUrl(url) { if (url && url.startsWith('blob:')) activeObjectUrls.add(url); }
  function revokeTrackedUrls() {
    activeObjectUrls.forEach(u => { try { URL.revokeObjectURL(u); } catch {} });
    activeObjectUrls.clear();
  }

  /* ── DOM-Helfer ────────────────────────────────────────────────── */

  function el(tag, attrs = {}) {
    const e = document.createElement(tag);
    Object.entries(attrs).forEach(([k, v]) => {
      if (k === 'cls') e.className = v;
      else if (k === 'text') e.textContent = v;
      else e.setAttribute(k, v);
    });
    return e;
  }

  /* ── Markdown-Toolbar ─────────────────────────────────────────── */

  /** Aktive Textarea finden (bearbeiteter Block oder neuer Block) */
  function findActiveTextarea() {
    return overlay?.querySelector('.typora-editing .typora-textarea')
        || overlay?.querySelector('.typora-new');
  }

  /** Modifikator auf aktive Textarea anwenden — KEIN Re-Render (Fokus bleibt erhalten) */
  function applyToActiveTextarea(modifier) {
    const ta = findActiveTextarea();
    if (!ta) return;
    const s   = ta.selectionStart;
    const e   = ta.selectionEnd;
    const sel = ta.value.slice(s, e);
    const { newText, newStart, newEnd } = modifier(ta.value, s, e, sel);
    ta.value = newText;
    ta.setSelectionRange(newStart, newEnd);
    ta.focus();
    /* Auto-Resize */
    ta.style.height = 'auto';
    ta.style.height = ta.scrollHeight + 'px';
    /* Toolbar-Aktiv-Zustand nach Änderung aktualisieren */
    updateToolbarState(ta);
    /* Kein renderActivePane() — das Textarea bleibt aktiv, Blur-Handler rendert später */
  }

  /**
   * Toggle-fähiger Wrap-Modifier: entfernt Marker wenn Selektion bereits gewrappt ist,
   * fügt sie sonst ein.
   *
   * Fall A: Selektion umfasst die Marker selbst (z. B. "**wort**" markiert)
   * Fall B: Selektion liegt zwischen den Markern (z. B. nur "wort" markiert, aber umgeben)
   * Sonst: Wrap (Toggle On)
   */
  function wrapModifier(before, after) {
    return (value, s, e, sel) => {
      /* Fall A: Selektion beginnt und endet mit dem Marker */
      if (sel.startsWith(before) && sel.endsWith(after) && sel.length >= before.length + after.length) {
        const inner = sel.slice(before.length, sel.length - after.length);
        const newText = value.slice(0, s) + inner + value.slice(e);
        return { newText, newStart: s, newEnd: s + inner.length };
      }

      /* Fall B: Marker direkt vor und nach der Selektion im Gesamttext */
      const preMarker  = value.slice(s - before.length, s);
      const postMarker = value.slice(e, e + after.length);

      /* Italic-Sonderfall: sicherstellen dass es nicht Teil eines Bold-Markers ist */
      const isSingleStar = before === '*' && after === '*';
      if (isSingleStar) {
        const preDouble  = value.slice(s - 2, s);
        const postDouble = value.slice(e, e + 2);
        /* Wenn doppelte Sternchen direkt angrenzen → kein Toggle-Off, wäre Bold-Marker */
        if (preDouble === '**' && postDouble === '**') {
          /* Wrap On statt Toggle-Off um Bold nicht zu beschädigen */
          const placeholder = sel || 'Text';
          const newText = value.slice(0, s) + before + placeholder + after + value.slice(e);
          return {
            newText,
            newStart: sel ? s + before.length : s + before.length,
            newEnd:   sel ? e + before.length : s + before.length + placeholder.length,
          };
        }
      }

      if (preMarker === before && postMarker === after) {
        const newText = value.slice(0, s - before.length) + sel + value.slice(e + after.length);
        return {
          newText,
          newStart: s - before.length,
          newEnd:   s - before.length + sel.length,
        };
      }

      /* Kein Match → Wrap (Toggle On) */
      const placeholder = sel || 'Text';
      const newText = value.slice(0, s) + before + placeholder + after + value.slice(e);
      return {
        newText,
        newStart: sel ? s + before.length : s + before.length,
        newEnd:   sel ? e + before.length : s + before.length + placeholder.length,
      };
    };
  }

  /* ── Toolbar-Aktiv-Indikator ──────────────────────────────────── */

  /** Prüft ob Selektion in textarea von given Markern umschlossen ist */
  function isWrappedWith(value, s, e, before, after) {
    const sel = value.slice(s, e);
    /* Fall A: Selektion umfasst Marker */
    if (sel.startsWith(before) && sel.endsWith(after) && sel.length >= before.length + after.length) {
      return true;
    }
    /* Fall B: Marker direkt außerhalb der Selektion */
    const preMarker  = value.slice(s - before.length, s);
    const postMarker = value.slice(e, e + after.length);
    if (preMarker === before && postMarker === after) {
      /* Italic-Sonderfall: kein Bold-Marker */
      if (before === '*' && after === '*') {
        const preDouble  = value.slice(s - 2, s);
        const postDouble = value.slice(e, e + 2);
        if (preDouble === '**' && postDouble === '**') return false;
      }
      return true;
    }
    return false;
  }

  /** Aktiv-Zustand der Bold/Italic/Code-Buttons anhand aktueller Selektion setzen */
  function updateToolbarState(ta) {
    if (!overlay) return;
    const value = ta.value;
    const s     = ta.selectionStart;
    const e     = ta.selectionEnd;

    const checks = [
      { sel: '[data-toolbar-action="bold"]',   before: '**', after: '**' },
      { sel: '[data-toolbar-action="italic"]', before: '*',  after: '*'  },
      { sel: '[data-toolbar-action="code"]',   before: '`',  after: '`'  },
    ];

    checks.forEach(({ sel: btnSel, before, after }) => {
      overlay.querySelectorAll(btnSel).forEach(btn => {
        /* Italic: nicht aktiv wenn Bold-Marker direkt angrenzt */
        let active = isWrappedWith(value, s, e, before, after);
        if (before === '*' && after === '*' && active) {
          const preDouble  = value.slice(s - 2, s);
          const postDouble = value.slice(e, e + 2);
          if (preDouble === '**' && postDouble === '**') active = false;
        }
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
        btn.classList.toggle('toolbar-btn-active', active);
      });
    });
  }

  /** Prefix an jeder selektierten Zeile einfügen */
  function prefixLinesModifier(prefix) {
    return (value, s, e, sel) => {
      /* Bereich auf volle Zeilen ausdehnen */
      const lineStart = value.lastIndexOf('\n', s - 1) + 1;
      const lineEnd   = e === value.length ? e : (value.indexOf('\n', e - 1) + 1 || value.length);
      const region    = value.slice(lineStart, lineEnd);
      const newRegion = region.split('\n').map(l => prefix + l).join('\n');
      const newText   = value.slice(0, lineStart) + newRegion + value.slice(lineEnd);
      return {
        newText,
        newStart: lineStart,
        newEnd:   lineStart + newRegion.length,
      };
    };
  }

  /** Überschriften-Prefix (ersetzt ggf. bereits vorhandenen) */
  function headingModifier(level) {
    const prefix = '#'.repeat(level) + ' ';
    return (value, s, e) => {
      const lineStart = value.lastIndexOf('\n', s - 1) + 1;
      const lineEnd   = (value.indexOf('\n', s)) === -1 ? value.length : value.indexOf('\n', s);
      const line      = value.slice(lineStart, lineEnd);
      /* Bestehende Heading-Marker entfernen */
      const stripped  = line.replace(/^#{1,6}\s/, '');
      const newLine   = prefix + stripped;
      const newText   = value.slice(0, lineStart) + newLine + value.slice(lineEnd);
      return {
        newText,
        newStart: lineStart + prefix.length,
        newEnd:   lineStart + newLine.length,
      };
    };
  }

  /** Link einfügen: [selektion](url) oder [text](url) */
  function linkModifier(value, s, e, sel) {
    const label   = sel || 'Text';
    const insert  = `[${label}](url)`;
    const newText = value.slice(0, s) + insert + value.slice(e);
    /* Cursor auf "url" positionieren */
    const urlStart = s + label.length + 3;
    const urlEnd   = urlStart + 3;
    return { newText, newStart: urlStart, newEnd: urlEnd };
  }

  /** Trennlinie auf eigene Zeile einfügen */
  function hrModifier(value, s) {
    const before = value.slice(0, s);
    const after  = value.slice(s);
    /* Sicherstellen, dass --- auf eigener Zeile steht */
    const prefix = before.length && !before.endsWith('\n') ? '\n' : '';
    const suffix = after.length  && !after.startsWith('\n') ? '\n' : '';
    const insert = prefix + '---' + suffix;
    const newText = before + insert + after;
    const newPos  = s + insert.length;
    return { newText, newStart: newPos, newEnd: newPos };
  }

  /* F-112 (behoben Sprint 61/#445): buildToolbar() registrierte bis hierhin
     bei JEDEM Aufruf einen eigenen `document`-click-Listener, um das gerade
     gebaute Heading-Dropdown-Menü zu schliessen — ohne jedes Cleanup.
     buildToolbar() läuft zweimal pro Modal-Öffnung (desc- + agent-Pane) und
     bei jedem Rebuild (u. a. Sprachwechsel, EventBus `config:language-
     changed` invalidiert den Overlay-Cache) erneut — die Listener
     akkumulierten unbegrenzt, jeder mit einer Closure auf ein ggf. längst
     aus dem DOM entferntes `headingMenu`.
     Fix: EIN einziger, modul-weiter Listener (Guard analog zum
     `_chatTailListenerBound`-Muster aus F-330/renderer.js), der beim Klick
     JEDES offene `.toolbar-dropdown-menu` schliesst — unabhängig davon,
     wie viele Toolbars seither gebaut wurden. */
  let _toolbarDropdownDocListenerBound = false;
  function _ensureToolbarDropdownDocListener() {
    if (_toolbarDropdownDocListenerBound) return;
    _toolbarDropdownDocListenerBound = true;
    document.addEventListener('click', () => {
      document.querySelectorAll('.toolbar-dropdown-menu.open').forEach(m => m.classList.remove('open'));
    });
  }

  /** Toolbar-Button erzeugen */
  function toolbarBtn(icon, labelKey) {
    const btn = el('button', { type: 'button', cls: 'toolbar-btn' });
    btn.setAttribute('aria-label', I18n.t(labelKey));
    btn.setAttribute('title', I18n.t(labelKey));
    btn.appendChild(icon);
    return btn;
  }

  /** Toolbar bauen und zurückgeben.
   *  Maximize-Button sitzt rechtsbündig in der Toolbar (Karte #111).
   *  paneKey + section werden für setMaximized() in den Click-Handler eingebunden. */
  function buildToolbar(paneKey, section) {
    const bar = el('div', { cls: 'desc-toolbar' });

    /* Fett */
    const btnBold = toolbarBtn(boldIcon(14), 'toolbar.bold');
    btnBold.dataset.toolbarAction = 'bold';
    btnBold.setAttribute('aria-pressed', 'false');
    btnBold.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnBold.addEventListener('click', () => applyToActiveTextarea(wrapModifier('**', '**')));

    /* Kursiv */
    const btnItalic = toolbarBtn(italicIcon(14), 'toolbar.italic');
    btnItalic.dataset.toolbarAction = 'italic';
    btnItalic.setAttribute('aria-pressed', 'false');
    btnItalic.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnItalic.addEventListener('click', () => applyToActiveTextarea(wrapModifier('*', '*')));

    /* Inline-Code */
    const btnCode = toolbarBtn(codeIcon(14), 'toolbar.code');
    btnCode.dataset.toolbarAction = 'code';
    btnCode.setAttribute('aria-pressed', 'false');
    btnCode.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnCode.addEventListener('click', () => applyToActiveTextarea(wrapModifier('`', '`')));

    /* Überschriften-Dropdown */
    const headingWrap = el('div', { cls: 'toolbar-dropdown-wrap' });
    const btnHeading  = toolbarBtn(headingIcon(14), 'toolbar.heading');
    btnHeading.classList.add('toolbar-btn-dropdown');
    const headingMenu = el('div', { cls: 'toolbar-dropdown-menu' });
    /* F-116: title allein ist keine verlaessliche Textalternative (wird je nach
       Screenreader/Einstellung vorgelesen oder nicht) — zusaetzlich aria-label,
       title bleibt als Tooltip stehen. */
    const btnH2 = el('button', { type: 'button', cls: 'toolbar-dropdown-item', text: 'H2' });
    btnH2.setAttribute('title', I18n.t('toolbar.heading.h2'));
    btnH2.setAttribute('aria-label', I18n.t('toolbar.heading.h2'));
    const btnH3 = el('button', { type: 'button', cls: 'toolbar-dropdown-item', text: 'H3' });
    btnH3.setAttribute('title', I18n.t('toolbar.heading.h3'));
    btnH3.setAttribute('aria-label', I18n.t('toolbar.heading.h3'));
    headingMenu.appendChild(btnH2);
    headingMenu.appendChild(btnH3);
    headingWrap.appendChild(btnHeading);
    headingWrap.appendChild(headingMenu);

    btnHeading.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnHeading.addEventListener('click', (e) => {
      e.stopPropagation();
      headingMenu.classList.toggle('open');
    });
    btnH2.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnH2.addEventListener('click', () => { applyToActiveTextarea(headingModifier(2)); headingMenu.classList.remove('open'); });
    btnH3.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnH3.addEventListener('click', () => { applyToActiveTextarea(headingModifier(3)); headingMenu.classList.remove('open'); });

    /* F-113: Pfeil-Tasten-Navigation IM Menü — die Tab-Bar des Modals hat sie
       längst, dieses Dropdown bisher nicht; per Tastatur war es nur über den
       normalen Tab-Umlauf erreichbar. Muster nach WAI-ARIA „Menu Button":
       ↓/↑ am Auslöser öffnet und setzt den Fokus auf den ersten/letzten
       Eintrag, im Menü wandern ↓/↑ zyklisch, Pos1/Ende springen an die
       Ränder, Esc schliesst und gibt den Fokus an den Auslöser zurück, Tab
       schliesst und lässt den Umlauf weiterlaufen.
       Der Fokuswechsel auf die Menü-Einträge ist ungefährlich, obwohl die
       Toolbar sonst überall Fokus-Diebstahl verhindert: `mousedown`-
       preventDefault betrifft nur die Maus, und `applyToActiveTextarea()`
       liest selectionStart/End frisch vom Textarea — das seine Selektion auch
       ohne Fokus behält — und fokussiert es am Ende ohnehin wieder.
       `.open` MUSS vor `focus()` gesetzt sein: ohne die Klasse steht das Menü
       auf `display:none` (style.css:1480) und ein Element darin ist nicht
       fokussierbar. */
    const headingItems = [btnH2, btnH3];
    function focusHeadingItem(i) {
      headingMenu.classList.add('open');
      headingItems[(i + headingItems.length) % headingItems.length].focus();
    }
    btnHeading.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown')    { e.preventDefault(); focusHeadingItem(0); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); focusHeadingItem(-1); }
    });
    headingMenu.addEventListener('keydown', (e) => {
      const idx = Math.max(0, headingItems.indexOf(document.activeElement));
      if (e.key === 'ArrowDown')     { e.preventDefault(); focusHeadingItem(idx + 1); }
      else if (e.key === 'ArrowUp')  { e.preventDefault(); focusHeadingItem(idx - 1); }
      else if (e.key === 'Home')     { e.preventDefault(); focusHeadingItem(0); }
      else if (e.key === 'End')      { e.preventDefault(); focusHeadingItem(-1); }
      else if (e.key === 'Escape')   {
        /* stopPropagation: sonst schliesst derselbe Esc auch das Karten-Modal */
        e.preventDefault(); e.stopPropagation();
        headingMenu.classList.remove('open');
        btnHeading.focus();
      } else if (e.key === 'Tab')    { headingMenu.classList.remove('open'); }
    });
    _ensureToolbarDropdownDocListener();

    /* Liste */
    const btnList = toolbarBtn(listIcon(14), 'toolbar.list');
    btnList.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnList.addEventListener('click', () => applyToActiveTextarea(prefixLinesModifier('- ')));

    /* Geordnete Liste */
    const btnOL = toolbarBtn(orderedListIcon(14), 'toolbar.ordered-list');
    btnOL.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnOL.addEventListener('click', () => applyToActiveTextarea(prefixLinesModifier('1. ')));

    /* Checkliste */
    const btnCL = toolbarBtn(checklistIcon(14), 'toolbar.checklist');
    btnCL.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnCL.addEventListener('click', () => applyToActiveTextarea(prefixLinesModifier('- [ ] ')));

    /* Link */
    const btnLink = toolbarBtn(linkIcon(14), 'toolbar.link');
    btnLink.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnLink.addEventListener('click', () => applyToActiveTextarea(linkModifier));

    /* Zitat */
    const btnQuote = toolbarBtn(quoteIcon(14), 'toolbar.quote');
    btnQuote.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnQuote.addEventListener('click', () => applyToActiveTextarea(prefixLinesModifier('> ')));

    /* Trennlinie */
    const btnHr = toolbarBtn(hrIcon(14), 'toolbar.hr');
    btnHr.addEventListener('mousedown', e => e.preventDefault()); /* Fokus-Diebstahl verhindern */
    btnHr.addEventListener('click', () => applyToActiveTextarea(hrModifier));

    bar.appendChild(btnBold);
    bar.appendChild(btnItalic);
    bar.appendChild(btnCode);
    bar.appendChild(headingWrap);
    bar.appendChild(btnList);
    bar.appendChild(btnOL);
    bar.appendChild(btnCL);
    bar.appendChild(btnLink);
    bar.appendChild(btnQuote);
    bar.appendChild(btnHr);

    /* Maximize-Button rechts in der Toolbar (Karte #111) */
    const maxBtn = el('button', { type: 'button', cls: 'toolbar-btn btn-maximize' });
    maxBtn.setAttribute('aria-label', I18n.t('toolbar.maximize'));
    maxBtn.setAttribute('title', I18n.t('toolbar.maximize'));
    maxBtn.appendChild(maximizeIcon(14));
    maxBtn.addEventListener('mousedown', e => e.preventDefault());
    maxBtn.addEventListener('click', () => {
      setMaximized(section, maxBtn, maximizedPaneKey !== paneKey, paneKey);
    });
    bar.appendChild(maxBtn);

    /* #267: Lese⇄Edit-Schalter ganz links in der Toolbar — nur desc-Pane
       (agent-Pane ist ADR-0029 click-to-edit, kein Modus-Toggle). Außerhalb
       von #desc-pane → echter Schalter zwischen Render-Ansicht und Editor.
       CSS blendet im Lese-Modus (.modal-desc-section ohne .desc-section-
       editing) die Markdown-Format-Buttons aus → Read-View zeigt nur
       „Bearbeiten“ + Maximize. */
    if (paneKey === 'desc') {
      bar.insertBefore(buildDescEditToggle(section), bar.firstChild);
    }

    return { bar, maxBtn };
  }

  /* ── Maximize-State ────────────────────────────────────────────── */

  /* F-108: Ein Key statt boolean — unterscheidet welche Pane maximiert ist */
  let maximizedPaneKey = null; /* 'desc' | 'agent' | null */

  function setMaximized(section, maxBtn, state, paneKey = null) {
    maximizedPaneKey = state ? paneKey : null;
    /* F-173: Body-Klasse setzt `.modal { transform: none }` per CSS — sonst
       erzeugt der Modal-Transform einen Containing-Block für `position: fixed`
       und die maximierte Section bleibt am Modal kleben statt am Viewport. */
    document.body.classList.toggle('maximized-pane-active', state);
    if (state) {
      section.classList.add('desc-maximized');
      /* Icon tauschen */
      while (maxBtn.firstChild) maxBtn.removeChild(maxBtn.firstChild);
      maxBtn.appendChild(minimizeIcon(14));
      maxBtn.setAttribute('aria-label', I18n.t('toolbar.minimize'));
      maxBtn.setAttribute('title', I18n.t('toolbar.minimize'));
    } else {
      section.classList.remove('desc-maximized');
      while (maxBtn.firstChild) maxBtn.removeChild(maxBtn.firstChild);
      maxBtn.appendChild(maximizeIcon(14));
      maxBtn.setAttribute('aria-label', I18n.t('toolbar.maximize'));
      maxBtn.setAttribute('title', I18n.t('toolbar.maximize'));
    }
  }

  function field(labelText, child, forId) {
    const wrap = el('div', { cls: 'modal-field' });
    const lbl  = el('label', { text: labelText });
    if (forId) lbl.setAttribute('for', forId);
    wrap.appendChild(lbl);
    wrap.appendChild(child);
    return wrap;
  }

  /* ── Kommentare ────────────────────────────────────────────────── */

  function renderComment(comment) {
    const item   = el('div', { cls: 'comment-item' });
    const meta   = el('div', { cls: 'comment-meta' });
    /* Karte #123: Avatar links neben dem Autor-Tag */
    const board    = State.get()?.board;
    const identity = board ? resolveIdentity(comment.author || '', board) : null;
    if (identity) {
      const avatar = buildAvatar(identity, 'sm', { board, trackUrl: trackObjectUrl });
      if (avatar) meta.appendChild(avatar);
    }
    /* Karte #608 (ADR-0130 §6): Agenten-Kommentar „für“ eine Person — der
       Server setzt `onBehalfOf`, verantwortlich bleibt die Person. */
    const fuer = comment.onBehalfOf ? nameForEmail(comment.onBehalfOf, board) : '';
    const author = el('span', { cls: 'comment-author-tag',
                                  text: (comment.author || '—') + (fuer ? ' ' + I18n.t('comment.fuer', fuer) : '') });
    if (fuer) author.title = comment.onBehalfOf;
    const date   = el('span', { cls: 'comment-date', text: comment.created });
    /* Karte #172: Kommentar-Body als Markdown — analog Karten-Beschreibung. */
    const text   = el('div',  { cls: 'comment-text' });
    if (comment.text) {
      text.appendChild(Markdown.render(comment.text));
    }
    meta.appendChild(author);
    meta.appendChild(date);
    /* Karte #639 (F-1264): Nur der SERVER stempelt `edited` bei einer
       Bearbeitung; der Fold übernimmt lediglich ein mitgegebenes `p.edited`
       (src/ops.js, `edited` ist ein zulässiger Payload-Schlüssel von
       comment.edit) — lokal entsteht „bearbeitet“ also nur, wenn der
       Schreiber es selbst mitgibt, und ist dort nicht verbürgt. Hier nur
       sichtbar machen, Zeit im Tooltip (F-1455). */
    if (comment.edited) {
      meta.appendChild(el('span', { cls: 'comment-date comment-edited',
                                    text: '· ' + I18n.t('comment.edited'),
                                    title: String(comment.edited) }));
    }
    item.appendChild(meta);
    item.appendChild(text);
    /* Karte #686 (ADR-0134 §3.6): Antwort eines externen Modells — Kennzeichnung,
       Beleg und „weicht vom Original ab“ liefert die Schleuse. */
    if (IS_SERVER_BUILD && comment.kind === 'extern' && SchleuseAuszug) {
      item.classList.add('comment-item--extern');
      item.appendChild(SchleuseAuszug.externBeleg(comment, State.get()?.board?.id ?? null, currentCard?.id ?? null));
    }
    /* Karte #678 (ADR-0135): Dateien des Kommentars darunter. Sie stehen als
       gewöhnliche Karten-Anhänge mit `commentId` in `card.attachments` —
       gleiche Ablage, gleiches Limit, gleiche Rechte wie die Karte. */
    if (IS_SERVER_BUILD) {
      const dateien = (currentCard?.attachments || [])
        .map(normalisiereAnhang)
        .filter(a => a && a.commentId && a.commentId === comment.id);
      if (dateien.length) item.appendChild(renderKommentarAnhaenge(dateien));
    }
    return item;
  }

  /* Karte #678: kompakte Liste unter einem Kommentar — Symbol, Name, Grösse;
     ein Klick lädt herunter. Bewusst ohne Vorschaubild: dessen ObjectURL
     hinge an `revokeTrackedUrls()` des Anhänge-Tabs. Löschen geht über den
     Anhänge-Tab, dort steht die Datei ebenfalls. */
  function renderKommentarAnhaenge(dateien) {
    ensureKommentarAnhangStyles();
    const box = el('div', { cls: 'comment-attachments' });
    dateien.forEach(att => {
      const name = att.filename || att.id;
      const btn = el('button', { type: 'button', cls: 'comment-attachment' });
      btn.setAttribute('title', I18n.t('comment.anhang-laden', name));
      btn.appendChild(mimeIconFor(att.mime, 14));
      btn.appendChild(el('span', { cls: 'attachment-name', text: name }));
      btn.appendChild(el('span', { cls: 'attachment-size', text: anhangBackend().humanSize(att.size) }));
      btn.addEventListener('click', async () => {
        const ok = await anhangBackend().downloadAttachment(currentCard, att);
        if (!ok) alert(I18n.t('attachment.read-failed'));
      });
      box.appendChild(btn);
    });
    return box;
  }

  /* Karte #678: die für den nächsten Kommentar gewählten Dateien anzeigen,
     je mit „nicht anhängen“. */
  function renderKommentarDateiAuswahl() {
    /* Gate auch hier: Die Funktion ruft sich im „×“-Handler selbst auf und
       gälte dem Minifier sonst als benutzt — gemessen +458 B im Template. */
    if (!IS_SERVER_BUILD) return;
    const box = overlay?.querySelector('#comment-files');
    if (!box) return;
    while (box.lastChild) box.removeChild(box.lastChild);
    kommentarDateien.forEach((datei, i) => {
      const chip = el('span', { cls: 'comment-file' });
      chip.appendChild(el('span', { cls: 'attachment-name', text: datei.name }));
      const weg = el('button', { type: 'button', cls: 'comment-file-weg', text: '×' });
      weg.setAttribute('aria-label', I18n.t('comment.datei-entfernen', datei.name));
      weg.addEventListener('click', () => {
        kommentarDateien.splice(i, 1);
        renderKommentarDateiAuswahl();
      });
      chip.appendChild(weg);
      box.appendChild(chip);
    });
  }

  /* Server-only CSS per JS (nicht style.css), damit es im Solo-/Template-
     Bundle per DCE wegfällt. */
  function ensureKommentarAnhangStyles() {
    if (document.getElementById('aboard-kommentar-anhang-styles')) return;
    const st = document.createElement('style');
    st.id = 'aboard-kommentar-anhang-styles';
    st.textContent = '.comment-attachments,.comment-files{display:flex;flex-wrap:wrap;gap:4px;margin-top:4px}.comment-attachment,.comment-file{display:inline-flex;align-items:center;gap:4px;max-width:100%;padding:2px 6px;font-size:12px;border:1px solid var(--border);border-radius:var(--radius);background:var(--surface);color:inherit}.comment-attachment{cursor:pointer}.comment-attachment .attachment-name,.comment-file .attachment-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:220px}.comment-file-weg{border:none;background:none;cursor:pointer;color:inherit;padding:0 2px}';
    document.head.appendChild(st);
  }

  function renderComments(list, comments) {
    while (list.lastChild) list.removeChild(list.lastChild);
    (comments || []).forEach(c => list.appendChild(renderComment(c)));
    if (!(comments?.length)) {
      const empty = el('p', { cls: 'comment-empty', text: '–' });
      list.appendChild(empty);
    }
    /* F-146: Badge-Update lebt IM Render-Pfad, nicht beim Aufrufer — analog
       renderAttachmentsList/renderLinksList. Vorher riefen beide Aufrufer
       (addComment, buildOverlay) updateCommentBadge() separat auf; ein
       künftiger dritter Aufrufer hätte das leicht vergessen können. */
    updateCommentBadge();
  }

  /* #856: Eingabefeld wächst mit dem Text bis ~40 % der Viewport-Höhe,
     danach scrollt es. Ohne Text (nach Absenden) zurück auf Ausgangshöhe. */
  function kommentarEingabeAnpassen(ta) {
    if (!ta) return;
    if (!ta.value) { ta.style.height = ''; ta.style.overflowY = ''; return; }
    /* Verstecktes Panel (anderer Tab aktiv): nicht messen — scrollHeight wäre 0 und der Entwurf auf die
       Mindesthöhe abgeschnitten. switchTab('comments') misst beim Sichtbarwerden neu (Karte #863, A-2). */
    const panel = ta.closest?.('.tab-panel');
    if (panel && !panel.classList.contains('tab-panel-active')) return;
    ta.style.height = 'auto';
    const grenze = Math.floor((window.innerHeight || 800) * 0.4);
    /* Rahmenzugabe: scrollHeight enthält den Rahmen nicht, das Feld ist border-box — sonst Innenraum 2 px zu klein → Scrollbalken. */
    let rahmen = (ta.offsetHeight || 0) - (ta.clientHeight || 0);
    try {
      const cs = window.getComputedStyle(ta);
      rahmen = Math.max(rahmen, (parseFloat(cs.borderTopWidth) || 0) + (parseFloat(cs.borderBottomWidth) || 0));
    } catch { /* ohne Computed-Style bleibt offset-client */ }
    const h = Math.min(ta.scrollHeight + Math.max(0, rahmen), grenze);
    ta.style.height = h + 'px';
    /* Kein Inline-overflow-y: Der CSS-Grundwert „auto“ regiert, ein Scrollbalken erscheint erst bei Überlauf. Ein Inline-
       „hidden“ überstimmte ihn und ließ nach einer Fensterverkleinerung den Entwurf ohne Scrollbalken abgeschnitten. */
  }

  /* #856: Klasse am Modal, solange der Kommentar-Tab aktiv ist (volle Höhe). */
  function kommentarModusSetzen(an) {
    const m = overlay?.querySelector('.modal');
    if (m) m.classList.toggle('modal--kommentare', !!an);
  }

  function addComment(list, textEl) {
    const text = textEl.value.trim();
    /* Karte #678: Ein Kommentar darf auch nur aus Dateien bestehen. */
    const dateien = IS_SERVER_BUILD ? kommentarDateien : [];
    if (!text && !dateien.length) return;
    const comment = {
      id:      crypto.randomUUID(),
      author:  Config.get('userName') || 'User',
      text,
      created: new Date().toISOString().slice(0, 10),
    };
    pendingComments.push(comment);
    /* Live-Anzeige: bestehende + neue Kommentare zeigen.
       Badge-Update passiert INNERHALB von renderComments (F-146). */
    const all = [...(currentCard?.comments ?? []), ...pendingComments];
    renderComments(list, all);
    textEl.value = '';
    kommentarEingabeAnpassen(textEl);
    list.scrollTop = list.scrollHeight;
    /* Karte #678 (ADR-0135): Die Dateien laufen über denselben Upload wie
       die Karten-Anhänge (Limit, SVG-Sperre, Serialisierung F-130) und
       tragen zusätzlich die `commentId`. Der Kommentar selbst bleibt —
       wie jeder neue Kommentar — bis zum Speichern in `pendingComments`;
       das Schliessen speichert automatisch (closeWithAutosave). */
    if (IS_SERVER_BUILD && dateien.length) {
      kommentarDateien = [];
      renderKommentarDateiAuswahl();
      const zielKartenId = currentCard?.id;
      const laeufe = dateien.map(datei => triggerUpload(datei, comment.id));
      /* KB-7: Ein Kommentar OHNE Text, dessen Dateien alle scheitern, wäre
         ein leerer Kommentar — er wird wieder entfernt. Mit Text bleibt er. */
      if (!text) {
        Promise.all(laeufe).then((ergebnisse) => {
          if (ergebnisse.every(att => !att)) entferneLeerenDateiKommentar(comment.id, zielKartenId);
        });
      }
    }
  }

  /* KB-7 (Karte #678): einen reinen Datei-Kommentar entfernen, dessen
     Uploads alle gescheitert sind. Die Uploads laufen NACH dem Anlegen und
     können dauern — inzwischen kann der Kommentar schon gespeichert sein
     (Speichern, Schliessen mit Autosave, Checkbox-Toggle). Deshalb zwei Wege:
       - noch in `pendingComments` → nur dort heraus, nichts ist gesendet;
       - schon im State → über den normalen Weg (`card:updated` ohne ihn;
         der Diff macht daraus `comment.delete`).
     Weder noch (Abbrechen verwirft `pendingComments`) → nichts zu tun. */
  function entferneLeerenDateiKommentar(kommentarId, kartenId) {
    if (!IS_SERVER_BUILD) return;
    const offen = currentCard && currentCard.id === kartenId;
    const i = offen ? pendingComments.findIndex(c => c.id === kommentarId) : -1;
    if (i >= 0) {
      pendingComments.splice(i, 1);
    } else {
      const basis = findCardById(kartenId)?.card;
      if (!basis || !(basis.comments || []).some(c => c.id === kommentarId)) return;
      EventBus.emit('card:updated', {
        ...basis,
        comments: basis.comments.filter(c => c.id !== kommentarId),
        updated:  new Date().toISOString().slice(0, 10),
      });
      if (offen) currentCard = { ...currentCard, comments: (currentCard.comments || []).filter(c => c.id !== kommentarId) };
    }
    if (offen) {
      const liste = overlay?.querySelector('#comments-list');
      if (liste) renderComments(liste, [...(currentCard.comments ?? []), ...pendingComments]);
    }
  }

  /* Generischer Tab-Badge — zeigt Count vor Tab-Label wenn >= 1, sonst nichts.
     Kann nach dem Bau von overlay (buildOverlay) aufgerufen werden.
     Karte #97: Generalisiert für Comments / Attachments / Links. */
  function setTabBadge(tabId, count, labelKey) {
    if (!overlay) return;
    const tabBtn = overlay.querySelector('#' + tabId);
    if (!tabBtn) return;
    /* Vorhandenes Badge entfernen (Idempotenz) */
    const existingBadge = tabBtn.querySelector('.tab-badge');
    if (existingBadge) tabBtn.removeChild(existingBadge);
    if (count >= 1) {
      const badge = document.createElement('span');
      badge.className = 'tab-badge';
      badge.textContent = String(count);
      badge.setAttribute('aria-hidden', 'true');
      tabBtn.insertBefore(badge, tabBtn.firstChild);
      tabBtn.setAttribute('aria-label', `${I18n.t(labelKey)} (${count})`);
    } else {
      tabBtn.removeAttribute('aria-label');
    }
  }

  /* ── Verlauf-Tab (ADR-0037) ───────────────────────────────────── */

  /* Aktion als lesbaren Label-String übersetzen.
     F-245 (Hot-Fix Sprint-28-Folge): Schema-Drift zwischen ADR-0037
     (author/action/from/to/field) und ADR-0047 + späterem
     Sprint-Lifecycle-Code (actor/type/note). Wir tolerieren beide:
     entry.action ?? entry.type, plus note als Fallback-Label. */
  function activityLabel(entry) {
    const action = entry.action ?? entry.type;
    if (action === 'owner-changed') {
      return I18n.t('history.action.owner-changed') +
             (entry.from ? ` „${entry.from}"` : '') +
             ' → ' + (entry.to ? `„${entry.to}"` : '–');
    }
    if (action === 'project-changed') {
      return I18n.t('history.action.project-changed') +
             (entry.from ? ` „${entry.from}"` : '') +
             ' → ' + (entry.to ? `„${entry.to}"` : '–');
    }
    if (action === 'column-moved') {
      return I18n.t('history.action.column-moved') +
             (entry.from ? ` „${entry.from}"` : '') +
             ' → ' + (entry.to ? `„${entry.to}"` : '–');
    }
    if (action === 'field-updated') {
      const fieldLabel = I18n.t(`history.field.${entry.field}`) || entry.field;
      return I18n.t('history.action.field-updated') +
             ` ${fieldLabel}: ${entry.from ?? '–'} → ${entry.to ?? '–'}`;
    }
    /* #299/ADR-0095 Etappe 4: LLM-Send + Discard.
       Server-Schema: action="llm-sent", to="<provider>/<model>", meta={...}.
       Meta-Zeile (Tokens + Cost + Kontext) rendert renderActivityEntry. */
    if (action === 'llm-sent') {
      return I18n.t('history.action.llm-sent') +
             (entry.to ? ` „${entry.to}"` : '');
    }
    /* ADR-0136: Ein Board-Owner hat einen fremden Kommentar gelöscht.
       Server-Schema: from = Autor (E-Mail oder Anzeigename), to = Textanfang. */
    if (action === 'comment-deleted-by-owner') {
      return I18n.t('history.action.comment-deleted-by-owner', entry.from || '–') +
             (entry.to ? ` „${entry.to}"` : '');
    }
    if (action === 'llm-response-discarded') {
      return I18n.t('history.action.llm-response-discarded') +
             (entry.to ? ` „${entry.to}"` : '');
    }
    /* #300 Etappe 5 (ADR-0096 §3+§4): Mutations-Vorschläge.
       entry.tool = "set-field"|"move"|"add-comment"|"extend-description"|"add-label",
       entry.note = Kurz-Summary (z. B. "qa: false → true"). */
    if (action === 'llm-mutation-applied') {
      return I18n.t('history.action.llm-mutation-applied') +
             (entry.tool ? ` ${entry.tool}` : '') +
             (entry.note ? ` — ${entry.note}` : '');
    }
    if (action === 'llm-mutation-discarded') {
      return I18n.t('history.action.llm-mutation-discarded') +
             (entry.tool ? ` ${entry.tool}` : '') +
             (entry.note ? ` — ${entry.note}` : '');
    }
    /* Karte #686 (ADR-0134 §3.7): Protokoll der Ausgangsschleuse —
       to = "<anbieter>/<modell>", note = Grund bei Fehlschlag. */
    if (IS_SERVER_BUILD && typeof action === 'string' && action.startsWith('schleuse-')) {
      if (action === 'schleuse-kommentar-geloescht') {
        return I18n.t('history.action.schleuse-kommentar-geloescht', entry.from || '–') + (entry.to ? ` „${entry.to}"` : '');
      }
      return I18n.t(`history.action.${action}`) + (entry.to ? ` „${entry.to}"` : '') + (entry.note ? ` — ${entry.note}` : '');
    }
    /* KF-V1 (Sprint-82-Abschluss): Beteiligte geändert (src/activity.js:
       added/removed) und Karte von einem anderen Board übernommen (Server,
       transfer.go: from/to = { boardId, displayId, … }). */
    if (action === 'assignees-changed') {
      const liste = l => (Array.isArray(l) ? l : []).join(', ');
      return I18n.t('history.action.assignees-changed') +
             (entry.added?.length ? ` +${liste(entry.added)}` : '') +
             (entry.removed?.length ? ` −${liste(entry.removed)}` : '');
    }
    if (action === 'board-transferred') {
      const nr = x => (x && typeof x === 'object' && x.displayId != null) ? `#${x.displayId}` : '–';
      return I18n.t('history.action.board-transferred') + ` ${nr(entry.from)} → ${nr(entry.to)}`;
    }
    /* F-1555/#670: Vorschlags-Zugang legt eine Karte an; die Aktion heisst
       `vorschlag`, der Hinweistext (note) kommt vom Server. */
    if (action === 'vorschlag') {
      return I18n.t('history.action.vorschlag') + (entry.note ? ` ${entry.note}` : '');
    }
    /* Lifecycle-Events (create/move/complete/sprint-activate/...) tragen
       eine freie note. Wenn keine note vorhanden, fällt das auf den Action-
       Namen zurück, statt undefined zu zeigen. */
    if (entry.note) return `${action ?? '–'}: ${entry.note}`;
    return action ?? '–';
  }

  /* Einzelnen Verlauf-Eintrag als DOM-Element rendern */
  function renderActivityEntry(entry) {
    const item   = el('div', { cls: 'activity-entry' });
    const meta   = el('div', { cls: 'activity-meta' });
    /* F-245: Author kann unter entry.author (ADR-0037) oder entry.actor
       (ADR-0047 + neuere Lifecycle-Einträge) liegen. */
    const authorName = entry.author ?? entry.actor ?? '';
    /* Karte #123: Avatar (xs/20px) links neben dem Autor */
    const board    = State.get()?.board;
    const identity = board ? resolveIdentity(authorName, board) : null;
    if (identity) {
      const avatar = buildAvatar(identity, 'xs', { board, trackUrl: trackObjectUrl });
      if (avatar) meta.appendChild(avatar);
    }
    const author = el('span', { cls: 'activity-author-tag', text: authorName || '—' });
    /* #299/ADR-0095 Etappe 4: Server-Activity-Schema nutzt `ts` (ISO-Timestamp),
       Frontend-Schema nutzt `timestamp`. Beide tolerieren. */
    const tsField = entry.timestamp ?? entry.ts ?? '';
    const dateStr = tsField ? tsField.slice(0, 10) : '?';
    const time    = el('span', { cls: 'activity-date', text: dateStr });
    const label   = el('div',  { cls: 'activity-label', text: activityLabel(entry) });
    meta.appendChild(author);
    meta.appendChild(time);
    item.appendChild(meta);
    item.appendChild(label);
    /* #299/ADR-0095 Etappe 4: llm-sent-Meta-Zeile (Tokens + Cost + Kontext-Note).
       Backend setzt entry.meta = { inputTokens, outputTokens, costEstimateUSD, contextNote }.
       Bei llm-response-discarded wird die Meta-Zeile bewusst übersprungen — die
       eigentlichen Token-/Kosten-Zahlen stehen am Original-llm-sent-Eintrag. */
    const action = entry.action ?? entry.type;
    if ((action === 'llm-sent' || (IS_SERVER_BUILD && action === 'schleuse-beantwortet')) && entry.meta && typeof entry.meta === 'object') {
      const m = entry.meta;
      const metaLine = el('div', { cls: 'activity-llm-meta',
        text: I18n.t('history.llm.meta',
                     m.inputTokens || 0, m.outputTokens || 0,
                     m.costEstimateUSD || 0, m.contextNote || '') });
      item.appendChild(metaLine);
    }
    return item;
  }

  /* Verlauf-Liste rendern — neueste zuerst, max. BATCH Einträge sichtbar */
  const HISTORY_BATCH = 20;
  function renderHistoryList(list, activity) {
    while (list.lastChild) list.removeChild(list.lastChild);
    const entries = Array.isArray(activity) ? [...activity].reverse() : [];
    if (entries.length === 0) {
      list.appendChild(el('p', { cls: 'activity-empty', text: I18n.t('history.empty') }));
      return;
    }
    const visible = entries.slice(0, HISTORY_BATCH);
    visible.forEach(e => list.appendChild(renderActivityEntry(e)));

    /* „Ältere laden"-Button wenn mehr als HISTORY_BATCH Einträge */
    if (entries.length > HISTORY_BATCH) {
      const loadMore = el('button', { cls: 'btn btn-sm activity-load-more',
                                       text: I18n.t('history.load-more', entries.length - HISTORY_BATCH),
                                       type: 'button' });
      loadMore.addEventListener('click', () => {
        loadMore.remove();
        entries.slice(HISTORY_BATCH).forEach(e => list.appendChild(renderActivityEntry(e)));
      });
      list.appendChild(loadMore);
    }
  }

  /* Kommentar-Badge zählt bestehende + im Modal hinzugefügte Kommentare. */
  function updateCommentBadge() {
    const count = (currentCard?.comments?.length || 0) + pendingComments.length;
    setTabBadge('tab-comments', count, 'tab.comments');
  }

  function updateAttachmentsBadge() {
    const count = currentCard?.attachments?.length || 0;
    setTabBadge('tab-attachments', count, 'tab.attachments');
  }

  function updateLinksBadge() {
    const count = currentCard?.links?.length || 0;
    setTabBadge('tab-links', count, 'tab.links');
  }

  /* ── Karten-Verweise (ADR-0032) ────────────────────────────────── */

  /* Lookup über alle Sprints — gibt { card, sprint } oder null */
  function findCardById(cardId) {
    const board = State.get()?.board;
    if (!board?.sprints) return null;
    for (const sprint of board.sprints) {
      const card = sprint.cards?.find(c => c.id === cardId);
      if (card) return { card, sprint };
    }
    return null;
  }

  /* Liefert den Spalten-Titel für eine Karte (oder leeren String) */
  function columnTitleFor(card) {
    const board = State.get()?.board;
    if (!board || !card) return '';
    const col = board.columnDefs?.find(cd => cd.id === card.columnId);
    return col?.title || '';
  }

  /* Verweis-Eintrag als DOM bauen */
  function renderLinkEntry(link) {
    const entry = el('div', { cls: 'link-entry' });
    entry.dataset.targetId = link.targetId;
    entry.dataset.type     = link.type;

    const target = findCardById(link.targetId);

    /* Typ-Icon */
    const icon = el('span', { cls: `link-icon link-icon--${link.type}` });
    icon.textContent = link.type === 'blocks' || link.type === 'blocked-by' ? '⊘'
                     : link.type === 'depends-on' || link.type === 'required-for' ? '↳'
                     : '↔';
    entry.appendChild(icon);

    if (target) {
      const a = el('a', { cls: 'link-target', href: `#card-${target.card.displayId ?? ''}` });
      const idStr = target.card.displayId != null ? `#${target.card.displayId} ` : '';
      a.textContent = `${idStr}${target.card.title}`;
      /* Klick: Modal-Close würde sonst die ungespeicherten Edits verwerfen — wir
         erlauben den Hash-Sprung trotzdem, weil der Nutzer den Verweis explizit klickt.
         Save-Action vor dem Folgen: nicht notwendig, da Hash-Wechsel die aktuelle Karte
         schließt und die Ziel-Karte öffnet — Save-vs-Discard-Verhalten analog zu close(). */
      a.addEventListener('click', (e) => {
        e.preventDefault();
        /* Aktive Edits committen + speichern, dann Ziel-Karte öffnen */
        if (currentCard && target.card?.id !== currentCard.id) {
          /* Wechsel-Verhalten: erst aktuelle Karte speichern (gleiches Muster wie saveAndClose) */
          try { saveAndClose(); } catch {}
          EventBus.emit('card:edit', target.card);
        }
      });
      entry.appendChild(a);

      const badge = el('span', { cls: 'link-col-badge' });
      badge.textContent = columnTitleFor(target.card) || '–';
      entry.appendChild(badge);
    } else {
      const dead = el('span', { cls: 'link-target link-target--dead' });
      dead.textContent = I18n.t('link.target-deleted');
      entry.appendChild(dead);
    }

    /* Remove-Button */
    const removeBtn = el('button', { type: 'button', cls: 'link-remove-btn' });
    removeBtn.setAttribute('aria-label', I18n.t('link.remove'));
    removeBtn.setAttribute('title', I18n.t('link.remove'));
    removeBtn.textContent = '×';
    removeBtn.addEventListener('click', () => {
      const isBlocker = link.type === 'blocks' || link.type === 'blocked-by';
      const targetTitle = target?.card?.title || I18n.t('link.unknown-target');
      if (isBlocker && !confirm(I18n.t('link.confirm-remove-blocker', targetTitle))) return;
      EventBus.emit('card:link-remove', {
        sourceId: currentCard.id, targetId: link.targetId, type: link.type,
      });
      /* Lokale Sicht: Karte neu aus State holen, Liste neu rendern */
      const refreshed = findCardById(currentCard.id);
      if (refreshed) currentCard = { ...currentCard, links: refreshed.card.links };
      renderLinksList();
    });
    /* Kritik K-2: Entfernen änderte den Rückverweis auf der Spiegelkarte. */
    if (!(IS_SERVER_BUILD && spiegelGebunden(target?.card, target?.sprint?.id))) entry.appendChild(removeBtn);

    return entry;
  }

  /* Komplette Links-Liste neu rendern */
  function renderLinksList() {
    if (!overlay) return;
    const list = overlay.querySelector('#links-list');
    if (!list) return;
    while (list.lastChild) list.removeChild(list.lastChild);

    if (!currentCard || !Array.isArray(currentCard.links) || currentCard.links.length === 0) {
      const empty = el('p', { cls: 'link-empty', text: I18n.t('link.empty') });
      list.appendChild(empty);
    } else {
      /* Nach Typ gruppieren — Reihenfolge laut LINK_TYPES */
      const groups = groupLinksByType(currentCard);
      LINK_TYPES.forEach(type => {
        const links = groups[type];
        if (!links.length) return;
        const group = el('div', { cls: 'link-group' });
        const heading = el('h4', { cls: 'link-group-heading', text: I18n.t(TYPE_KEYS[type]) });
        group.appendChild(heading);
        links.forEach(l => group.appendChild(renderLinkEntry(l)));
        list.appendChild(group);
      });
    }
    /* Karte #97: Tab-Badge mit aktueller Verknüpfungs-Anzahl.
       F-146: Update steht jetzt am ENDE der Render-Funktion — einheitlich
       mit renderAttachmentsList/renderComments (vorher hier am Anfang). */
    updateLinksBadge();
  }

  /* Picker: Karte aus dem aktuellen Board wählen + Typ festlegen */
  function openLinkPicker() {
    if (!overlay || !currentCard) return;
    /* Bestehenden Picker entfernen */
    overlay.querySelector('.link-picker')?.remove();

    const picker = el('div', { cls: 'link-picker' });

    /* Suchfeld */
    const search = el('input', { type: 'text', cls: 'link-picker-search',
                                  placeholder: I18n.t('link.picker.placeholder') });
    picker.appendChild(search);

    /* Typ-Wahl */
    const typeWrap = el('div', { cls: 'link-picker-type-row' });
    const typeLabel = el('label', { text: I18n.t('link.picker.type-label'),
                                     cls: 'link-picker-type-label' });
    const typeSel = el('select', { cls: 'link-picker-type-select' });
    LINK_TYPES.forEach(t => {
      typeSel.appendChild(el('option', { value: t, text: I18n.t(TYPE_KEYS[t]) }));
    });
    typeWrap.appendChild(typeLabel);
    typeWrap.appendChild(typeSel);
    picker.appendChild(typeWrap);

    /* Ergebnisliste */
    const results = el('div', { cls: 'link-picker-results' });
    picker.appendChild(results);

    /* Action-Row */
    const actionRow = el('div', { cls: 'link-picker-actions' });
    const cancelBtn = el('button', { type: 'button', cls: 'btn btn-sm',
                                      text: I18n.t('link.picker.cancel') });
    cancelBtn.addEventListener('click', () => picker.remove());
    actionRow.appendChild(cancelBtn);
    picker.appendChild(actionRow);

    /* Kandidatenliste aufbauen — alle Karten außer self.
       F-125: Ausgeschlossen wird nicht mehr jede bereits irgendwie verlinkte
       Karte, sondern nur die Kombination (Zielkarte, gewählter Typ) — genau
       das, was `Links.addLink()` als Dublette abweist (`hasLink` prüft
       targetId UND type). Vorher konnte man über die Oberfläche keinen
       zweiten Verweistyp auf dieselbe Karte legen, obwohl das Datenmodell ihn
       trägt (z. B. „related" ergänzen, wenn schon ein „blocked-by" steht) —
       man musste den bestehenden Verweis erst löschen. Weil der Typ im Picker
       umgeschaltet werden kann, gehört die Prüfung in renderResults() und
       nicht in die einmalig gebaute Kandidatenliste. */
    const board = State.get()?.board;
    const existingLinks = currentCard.links || [];
    const candidates = [];
    board?.sprints?.forEach(sprint => {
      (sprint.cards || []).forEach(card => {
        if (card.id === currentCard.id) return;
        /* Kritik K-2 (Karte #826): Ein Verweis ist doppelt persistiert (ADR-0032)
           und änderte die Spiegelkarte mit — der Server wiese das ab. */
        if (IS_SERVER_BUILD && spiegelGebunden(card, sprint.id)) return;
        candidates.push({ card, sprint });
      });
    });

    function renderResults() {
      while (results.lastChild) results.removeChild(results.lastChild);
      const q = search.value.trim().toLowerCase();
      const chosenType = typeSel.value;
      const filtered = candidates.filter(({ card }) => {
        if (existingLinks.some(l => l.targetId === card.id && l.type === chosenType)) return false;
        if (!q) return true;
        const hay = `${card.displayId ?? ''} ${card.title}`.toLowerCase();
        return hay.includes(q);
      });
      filtered.slice(0, 30).forEach(({ card, sprint }) => {
        const row = el('button', { type: 'button', cls: 'link-picker-row' });
        const idStr = card.displayId != null ? `#${card.displayId} ` : '';
        row.textContent = `${idStr}${card.title}`;
        const sprintTag = el('span', { cls: 'link-picker-sprint-tag',
                                        text: sprint?.id === '__backlog__' ? 'Backlog' : (sprint?.title || '') });
        row.appendChild(sprintTag);
        row.addEventListener('click', () => {
          EventBus.emit('card:link-add', {
            sourceId: currentCard.id, targetId: card.id, type: typeSel.value,
          });
          /* Lokale Sicht aktualisieren */
          const refreshed = findCardById(currentCard.id);
          if (refreshed) currentCard = { ...currentCard, links: refreshed.card.links };
          picker.remove();
          renderLinksList();
        });
        results.appendChild(row);
      });
      if (filtered.length === 0) {
        const empty = el('p', { cls: 'link-picker-empty', text: '–' });
        results.appendChild(empty);
      }
    }

    search.addEventListener('input', renderResults);
    /* F-125: Typwechsel ändert die Ausschlussliste → Ergebnisse neu aufbauen. */
    typeSel.addEventListener('change', renderResults);
    search.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.stopPropagation(); picker.remove(); }
    });
    renderResults();

    /* Picker direkt unter den Button hängen */
    const addBtn = overlay.querySelector('#link-add-btn');
    addBtn?.parentNode?.insertBefore(picker, addBtn.nextSibling);
    search.focus();
  }

  /* ── Anhänge (ADR-0033) ────────────────────────────────────────── */

  /* Groessen-Hinweis im Fuss des Anhaenge-Tabs, ab >50 % der Warn-Schwelle.
     Sichtbar ueberall dort, wo die Grenze auch WIRKT — das Kriterium ist
     `feature.attachmentLimits`, nicht `!feature.polling` (Karte #450,
     Kritiker-Befund).

     Vorher stand hier `if (Profile.has('feature.polling')) return;`, was
     `server` UND `claude-code` ausschloss. Seit dem Server-Profil die Limits
     gelten, hiesse das: Der Nutzer laeuft dort in eine harte Ablehnung, ohne
     je gesehen zu haben, wie voll das Board ist — Grenze ohne Anzeige. In
     `claude-code` ist es umgekehrt richtig, den Hinweis wegzulassen: dort
     liegen Anhaenge im FSA-Volume und es gibt gar keine Grenze.

     GERECHNET WIRD MIT DEM BASE64-AUFSCHLAG, wie in `checkUpload`. Sonst
     zeigte der Fuss „8,0 MB" und die Ablehnung meldete „10,7 MB ≥ 10,0 MB" —
     zwei Zahlen fuer dieselbe Sache, die sich nicht in Einklang bringen
     lassen. Beide Stellen beschreiben jetzt die Uebertragungsgroesse. */
  function updateBoardSizeInfo() {
    if (!overlay) return;
    const info = overlay.querySelector('#attachments-board-size');
    if (!info) return;
    info.style.display = 'none';
    info.textContent = '';
    if (!Profile.has('feature.attachmentLimits')) return;
    const state = State.get();
    const used  = Math.round(anhangBackend().computeBoardBytes(state?.board) * BASE64_FAKTOR);
    const warn  = Config.get('attachmentBoardWarnBytes');
    if (used >= warn / 2) {
      info.style.display = '';
      info.textContent = I18n.t('attachment.board-size', anhangBackend().humanSize(used));
    }
  }

  /* Thumbnail oder MIME-Icon für ein Attachment.
     F-128: SVG bewusst NICHT als <img>-Thumbnail rendern — SVG kann
     <script>, <foreignObject> oder externe Referenzen enthalten (ADR-0033).
     F-1107: `istSvgMime()` statt exaktem String-Vergleich — erkennt auch
     `image/svg`, `image/svg-xml` und Groß-/Kleinschreibungs-Varianten aus
     Mail-Ingest-Altbestand. */
  async function buildAttachmentPreview(att) {
    const wrapper = el('div', { cls: 'attachment-preview' });
    const isRenderableImage = att.mime?.startsWith('image/') && !istSvgMime(att.mime);
    if (isRenderableImage) {
      const img = el('img', { cls: 'attachment-thumb' });
      img.alt = att.filename || '';
      const url = await anhangBackend().attachmentUrl(currentCard, att);
      /* F-131: Wurde das Modal während des await geschlossen, ObjectURL sofort revoken */
      if (url) {
        if (!overlay?.isConnected) {
          if (url.startsWith('blob:')) { try { URL.revokeObjectURL(url); } catch {} }
        } else {
          img.src = url;
          trackObjectUrl(url);
        }
      }
      wrapper.appendChild(img);
    } else {
      wrapper.appendChild(mimeIconFor(att.mime, 32));
    }
    return wrapper;
  }

  function renderAttachmentEntry(attRoh) {
    /* F-1088: Mail-Ingest-Altbestand vor Sprint 69 steht als reiner id-String
       in card.attachments (statt {id, filename, mime, size}) — ohne
       Normalisierung liefe jedes att.xxx unten auf undefined, der Eintrag
       zeigte „–" und Download/Cover griffen ins Leere (kein att.id). */
    const att = normalisiereAnhang(attRoh);
    /* F-1102: ein leerer String liefert null — dieser Eintrag wird
       übersprungen statt die ganze Liste mit einem TypeError abzubrechen. */
    if (!att) return null;
    const entry = el('div', { cls: 'attachment-entry' });
    entry.dataset.attachmentId = att.id;

    /* Cover-Markierung: Eintrag mit Klasse versehen wenn aktives Cover */
    const isCover = currentCard?.coverAttachmentId === att.id;
    if (isCover) entry.classList.add('attachment-entry--cover');

    /* Preview-Slot — wird async befüllt */
    const previewSlot = el('div', { cls: 'attachment-preview-slot' });
    entry.appendChild(previewSlot);
    buildAttachmentPreview(att).then(preview => {
      if (previewSlot.isConnected) previewSlot.appendChild(preview);
    });

    /* Meta-Spalte: Dateiname + Größe */
    const meta = el('div', { cls: 'attachment-meta' });
    /* F-1088: Altbestand ohne filename zeigt die id statt „–" — ein
       Anhang ohne jeden lesbaren Namen wäre im Anhänge-Tab nicht auffindbar. */
    const name = el('span', { cls: 'attachment-name', text: att.filename || att.id });
    name.setAttribute('title', att.filename || att.id || '');
    const size = el('span', { cls: 'attachment-size', text: anhangBackend().humanSize(att.size) });
    meta.appendChild(name);
    meta.appendChild(size);
    /* Cover-Badge in der Meta-Spalte */
    if (isCover) {
      const coverBadge = el('span', { cls: 'attachment-cover-badge', text: I18n.t('attachment.is-cover') });
      meta.appendChild(coverBadge);
    }
    entry.appendChild(meta);

    /* Aktion: Download */
    const dlBtn = el('button', { type: 'button', cls: 'attachment-btn attachment-download-btn' });
    dlBtn.setAttribute('aria-label', I18n.t('attachment.download'));
    dlBtn.setAttribute('title', I18n.t('attachment.download'));
    dlBtn.appendChild(downloadIcon(14));
    dlBtn.addEventListener('click', async () => {
      const ok = await anhangBackend().downloadAttachment(currentCard, att);
      if (!ok) alert(I18n.t('attachment.read-failed'));
    });
    entry.appendChild(dlBtn);

    /* Aktion: Als Cover setzen / Cover entfernen — nur für Bilder, nicht SVG
       (F-128; F-1107: istSvgMime() statt exaktem String-Vergleich) */
    const isCoverableImage = att.mime?.startsWith('image/') && !istSvgMime(att.mime);
    if (isCoverableImage) {
      const coverBtn = el('button', { type: 'button', cls: isCover ? 'attachment-btn attachment-cover-btn attachment-cover-btn--active' : 'attachment-btn attachment-cover-btn' });
      const coverLabel = isCover ? I18n.t('attachment.remove-cover') : I18n.t('attachment.set-cover');
      coverBtn.setAttribute('aria-label', coverLabel);
      coverBtn.setAttribute('title', coverLabel);
      coverBtn.textContent = isCover ? '🖼✕' : '🖼';
      coverBtn.addEventListener('click', () => {
        const newCoverId = isCover ? null : att.id;
        EventBus.emit('card:cover-set', { cardId: currentCard.id, attachmentId: newCoverId });
        const refreshed = findCardById(currentCard.id);
        if (refreshed) currentCard = { ...currentCard, coverAttachmentId: refreshed.card.coverAttachmentId };
        renderAttachmentsList();
      });
      entry.appendChild(coverBtn);
    }

    /* Aktion: Löschen */
    const delBtn = el('button', { type: 'button', cls: 'attachment-btn attachment-delete-btn' });
    delBtn.setAttribute('aria-label', I18n.t('attachment.delete'));
    delBtn.setAttribute('title', I18n.t('attachment.delete'));
    delBtn.appendChild(trashIcon(13));
    delBtn.addEventListener('click', async () => {
      if (!confirm(I18n.t('attachment.delete-confirm', att.filename || '–'))) return;
      const geloescht = await anhangBackend().deleteAttachment(currentCard, att);
      /* F-2978 (#1131): Server hat nicht gelöscht (401/5xx/Netz) → Anhang bleibt in der Karte.
         Nur der Server-Plugin liefert false; FSA/Standalone liefern undefined (wie bisher). */
      if (IS_SERVER_BUILD && geloescht === 'verboten') { EventBus.emit('hinweis', Sitzung.text('anhangLoeschen403')); return; } /* F-2990 */
      if (IS_SERVER_BUILD && geloescht === false) { EventBus.emit('hinweis', Sitzung.text('anhangLoeschenFehler')); return; }
      EventBus.emit('card:attachment-remove', { cardId: currentCard.id, attachmentId: att.id });
      const refreshed = findCardById(currentCard.id);
      if (refreshed) currentCard = { ...currentCard, attachments: refreshed.card.attachments, coverAttachmentId: refreshed.card.coverAttachmentId };
      renderAttachmentsList();
      /* Karte #678: Hing die Datei an einem Kommentar, dort ebenfalls weg. */
      if (IS_SERVER_BUILD && att.commentId) {
        const liste = overlay?.querySelector('#comments-list');
        if (liste) renderComments(liste, [...(currentCard.comments ?? []), ...pendingComments]);
      }
    });
    entry.appendChild(delBtn);

    return entry;
  }

  function renderAttachmentsList() {
    if (!overlay) return;
    const list = overlay.querySelector('#attachments-list');
    if (!list) return;
    /* Vor Re-Render alle laufenden ObjectURLs freigeben — neue werden beim Rendern getrackt */
    revokeTrackedUrls();
    while (list.lastChild) list.removeChild(list.lastChild);

    const items = currentCard?.attachments || [];
    if (items.length === 0) {
      const empty = el('p', { cls: 'attachment-empty', text: I18n.t('attachment.empty') });
      list.appendChild(empty);
    } else {
      /* F-1102: normalisiereAnhang liefert für '' null — renderAttachmentEntry
         gibt dafür null zurück statt zu werfen; solche Einträge überspringen,
         die restlichen Anhänge bleiben sichtbar. */
      items.forEach(att => {
        const entry = renderAttachmentEntry(att);
        if (entry) list.appendChild(entry);
      });
    }
    updateBoardSizeInfo();
    /* Karte #97: Tab-Badge mit aktueller Anhangs-Anzahl */
    updateAttachmentsBadge();
  }

  /* F-130 (Karte #376): Upload-Serialisierung.

     `checkUpload` liest die Board-Größe, `uploadFile` schreibt sie — dazwischen
     liegt ein `await`. Zwei gleichzeitig laufende Upload-Pfade (Datei-Dialog
     feuert `forEach` OHNE await, dazu Paste und Drop) prüfen deshalb beide
     gegen denselben Schnappschuss und kommen beide durch, obwohl ihre Summe
     das Hartlimit reisst. Ein klassisches Check-Then-Act.

     Die Prüfung „gegen den aktuellen Stand" ist ohne Serialisierung nicht
     herstellbar: aktuell ist der Stand erst, wenn der vorherige Upload den
     State fortgeschrieben hat. Deshalb reiht sich jeder Aufruf in eine
     Promise-Kette ein; Prüfung und Schreiben eines Uploads laufen damit
     atomar gegenüber den anderen.

     Die Kette darf NIE in den rejected-Zustand geraten — sonst blockiert ein
     einzelner Fehler alle folgenden Uploads bis zum Modal-Neuaufbau (die
     Klasse „Gate ohne Rückweg", knowledge/topics/nebenlaeufigkeit-und-
     lesefenster.md §6). `uploadQueue` wird deshalb ausschliesslich mit einer
     bereits abgefangenen Promise fortgeschrieben.

     ZIELKARTE BEIM EINREIHEN BINDEN (Sprint-52-Kritiker, K1): Die erste
     Fassung dieses Fixes las `currentCard` erst zur Ausführungszeit — und
     `uploadQueue` überlebt als Modul-Zustand jedes Modal-Öffnen. Wer drei
     Dateien wählte, das Modal schloss und Karte B öffnete, hängte die
     restlichen Dateien an **Karte B**; wer nur schloss, verlor sie ohne
     Alert und ohne Log. Vorher konnte das nicht passieren, weil die alte
     `async function` `currentCard` synchron vor dem ersten `await` las — die
     Serialisierung hat diese Eigenschaft versehentlich mitgenommen.

     Die Bindung ist die stärkere Antwort als ein Abbruch bei Abweichung: der
     Nutzer hat den Upload für Karte A gestartet, also gehört er an Karte A —
     auch wenn er inzwischen woanders hinschaut. */
  let uploadQueue = Promise.resolve();

  /* F-829: `triggerUpload`/`uploadOne` liefern das TATSAECHLICH erzeugte
     Attachment zurueck (oder `null` bei Ablehnung/Fehler) — Aufrufer, die
     wissen müssen, welches Attachment gerade entstanden ist (Paste-Pfad, s.u.),
     dürfen sich nicht mehr auf "das letzte Element der Anhangsliste" verlassen. */
  function triggerUpload(file, kommentarId) {
    /* Kritik K-4: Auf einer Spiegelkarte lädt nichts hoch (die Bytes landeten
       im Backend, das Kartenop wiese der Server ab — verwaiste Datei). */
    if (nurLesen || rueckwegSperre) return Promise.resolve(null);
    /* SYNCHRON binden, bevor irgendein await dazwischenkommt. */
    const zielKarte = currentCard;
    const lauf = uploadQueue.then(() => uploadOne(file, zielKarte, kommentarId));
    /* Fortgeschrieben wird die abgefangene Variante; der Aufrufer bekommt
       `lauf` und kann darauf warten. uploadOne wirft ohnehin nicht — der
       catch ist die Absicherung gegen künftige Änderungen dort. */
    uploadQueue = lauf.catch(() => null);
    return lauf;
  }

  async function uploadOne(file, zielKarte, kommentarId) {
    if (!zielKarte || !file) return null;

    /* VOR der Pruefung verkleinern (Karte #450, Meldung aus dem Betrieb 2026-08-28).
       Zwei Gruende fuer diese Reihenfolge:

       (1) Geprueft werden muss, was TATSAECHLICH gespeichert wird. Ein
           4-MB-Foto, das nach der Verkleinerung 400 KB wiegt, darf nicht am
           Pro-Datei-Limit scheitern — sonst lehnt der Vorcheck etwas ab, das
           gar nicht in dieser Groesse ankommt.
       (2) Im Server-Modus reist ein Karten-Anhang als base64 IM BOARD-STATE
           (Profil `server` hat kein `feature.fsaVolumeAttachments`, F-194) und
           geht bei jedem Speichern per PUT komplett ueber die Leitung, gegen
           `MaxBodyBytes = 10 MB` in server/api/middleware.go. Genau daran sind
           Bild-Uploads eines Nutzers gescheitert: „Speichern fehlgeschlagen", nach dem
           Reload war der Anhang weg.

       `verkleinereBild` (Karte #439) ist bewusst tolerant: Nicht-Bilder,
       animierte Formate, SVG und jeder Fehlerfall geben die Originaldatei
       unveraendert zurueck — ein misslungenes Verkleinern darf den Upload
       nicht verhindern, sondern hoechstens gross lassen. Der Aufruf ist
       deshalb an dieser Stelle bedingungslos richtig.

       Das ersetzt NICHT den REST-Weg aus Punkt 1 der Karte (F-798: das
       Attachment-Plugin ist unbenutzter Code) — es macht den Zustand nur
       unabhaengig davon tragfaehig, welche Grenze zuerst greift.

       NUR WO DIE DATEI IM STATE LANDET (Kritiker-Befund zur ersten Fassung):
       Die erste Fassung rief `verkleinereBild` bedingungslos. Im
       `claude-code`-Profil liegen Anhaenge aber als Dateien im FSA-Volume
       (`feature.fsaVolumeAttachments`) — dort gibt es weder eine
       PUT-Body-Grenze noch eine Quota, die Verkleinerung waere reiner Verlust
       ohne Gegenwert. Und sie ist nicht umkehrbar: `verkleinereBild`
       konvertiert nach JPEG und benennt auf `.jpg` um, ein PNG verliert dabei
       seinen Alphakanal (JPEG hat keinen). Fuer ein Beweisfoto oder einen
       gescannten Beleg an einer Karte ist das eine stille Datenveraenderung.
       Das Kriterium ist deshalb nicht „hat Limits", sondern „landet im
       Board-State" — genau dort zahlt die Verkleinerung sich aus. */
    /* Karte #450: Die Frage „landet die Datei im Board-State?" beantwortet
       jetzt das Backend, nicht mehr eine Profile-Abfrage hier. Der bisherige
       Ausdruck `!Profile.has('feature.fsaVolumeAttachments')` war richtig,
       solange es genau zwei Wege gab (FSA-Volume oder State). Seit der
       Server-Modus per REST hochlädt, gibt es einen dritten — und der hätte
       hier weiterhin „ja, im State" geantwortet und jedes Foto verlustbehaftet
       nach JPEG konvertiert, obwohl es gar nicht mehr in den State geht. */
    const backend = anhangBackend();
    const imStateGespeichert = backend.kartenAnhangImBoardState();
    const datei = imStateGespeichert ? await verkleinereBild(file) : file;

    /* State FRISCH lesen — nicht vor dem Einreihen in die Kette, sondern
       jetzt, nachdem alle vorherigen Uploads durch sind. */
    const state = State.get();
    const check = backend.checkUpload(datei, state?.board);
    if (!check.ok) {
      alert(check.reason);
      return null; /* F-829: kein Attachment entstanden — Aufrufer darf nichts referenzieren */
    }
    if (check.warning) alert(check.warning);

    let att;
    try {
      att = await backend.uploadFile(datei, zielKarte);
    } catch {
      alert(I18n.t('attachment.write-failed'));
      return null; /* F-829: FSA-Schreibfehler — ebenfalls kein Attachment */
    }
    /* Karte #678 (ADR-0135): Kommentar-Datei = Karten-Anhang mit
       `commentId`. Der Server kennt das Feld nicht; es lebt nur in
       `card.attachments` und lässt `comment.delete` die Datei mitnehmen. */
    if (IS_SERVER_BUILD && kommentarId) att = { ...att, commentId: kommentarId };
    EventBus.emit('card:attachment-add', { cardId: zielKarte.id, attachment: att });
    /* UI nur anfassen, wenn das Modal noch dieselbe Karte zeigt. Sonst
       würde der Nachzügler die Anhangsliste der GERADE offenen Karte mit
       den Daten der alten überschreiben. */
    if (currentCard && currentCard.id === zielKarte.id) {
      const refreshed = findCardById(zielKarte.id);
      if (refreshed) currentCard = { ...currentCard, attachments: refreshed.card.attachments };
      renderAttachmentsList();
      if (IS_SERVER_BUILD && kommentarId) {
        const liste = overlay?.querySelector('#comments-list');
        if (liste) renderComments(liste, [...(currentCard.comments ?? []), ...pendingComments]);
      }
    }
    return att; /* F-829: das tatsächlich erzeugte Attachment — nicht "das letzte der Liste" */
  }

  /* ── Typora-Ansicht-Rendering ──────────────────────────────────── */

  /* Pane-ID → Pane-Key-Zuordnung */
  function paneKeyFromContainer(container) {
    return container.id === 'agent-pane' ? 'agent' : 'desc';
  }

  /* ADR-0076: Umschalt-Button für den desc-Pane (Lese ⇄ Edit).
     Muster wie die Markdown-Toolbar-Buttons: mousedown→preventDefault
     verhindert Fokus-/Blur-Diebstahl vom aktiven Block (sonst Re-Render-
     Race), click führt die Aktion aus (auch keyboard-erreichbar). */
  /* ADR-0076 / #267 (Nutzer-Meldung): Modus-Schalter Lese ⇄ Edit. Lebt in
     der `.desc-toolbar` (descSection), NICHT in `#desc-pane` — der Button
     ist ein Schalter ZWISCHEN gerenderter Nur-Lese-Ansicht und WYSIWYG-
     Editor, nicht ein Element IM Editor. Wird einmal pro Modal gebaut;
     `syncDescToggle` hält Label/State an `descEditActive` synchron. */
  function buildDescEditToggle(section) {
    /* Beschriftung und Zustand setzt syncDescToggle — open() rendert den
       Pane immer (renderDescPane), bevor das Modal sichtbar ist. */
    const btn = el('button', { type: 'button', cls: 'desc-edit-toggle' });
    btn.addEventListener('mousedown', e => e.preventDefault());
    btn.addEventListener('click', () => {
      const pane = section.querySelector('#desc-pane');
      if (descEditActive && pane) {
        /* Edit verlassen: offenen Block UND offene Neu-Block-Textarea
           sauber committen (K-2: mousedown→preventDefault unterdrückt das
           blur des Neu-Blocks → ohne diesen Commit Datenverlust). */
        const active = pane.querySelector('.typora-editing');
        if (active) commitTyporaBlock(active, 'desc');
        commitNewBlock(pane, 'desc');
      }
      descEditActive = !descEditActive;
      renderPane('desc');
    });
    return btn;
  }

  /* Hält den (einmal gebauten) Toolbar-Toggle + die Read/Edit-Klasse der
     Section an `descEditActive` synchron. Aus renderTyporaView(desc)
     aufgerufen → deckt auch den Reset-Pfad (descEditActive=false beim
     Modal-Open) ab, da darauf stets renderPane folgt. */
  function syncDescToggle(section) {
    if (!section) return;
    section.classList.toggle('desc-section-editing', descEditActive);
    const t = section.querySelector('.desc-edit-toggle');
    if (!t) return;
    t.textContent = I18n.t(descEditActive ? 'md.edit-done' : 'md.edit-toggle');
    /* #817: im Lese-Modus nur ein Stift — Name und Tooltip sagen, was er tut. */
    t.title = I18n.t(descEditActive ? 'md.edit-done' : 'md.edit-hint');
    t.setAttribute('aria-label', t.title);
    t.classList.toggle('desc-edit-toggle--active', descEditActive);
  }

  function renderTyporaView(container) {
    const key = paneKeyFromContainer(container);
    const text = getPane(key);
    while (container.lastChild) container.removeChild(container.lastChild);

    /* ADR-0076: agent-Pane bleibt ADR-0029 (immer Klick-zu-Edit). Der
       desc-Pane ist Lese-Modus, solange descEditActive false ist — dann
       KEINE Block-/Placeholder-Klick-Aktivierung. Checklisten-Häkchen
       bleiben unabhängig davon interaktiv (Callback in Markdown.renderBlock). */
    const interactive = key !== 'desc' || descEditActive;
    if (key === 'desc') {
      container.classList.toggle('desc-pane--readonly', !descEditActive);
      /* #267: Toggle NICHT mehr in den Editor (#desc-pane) hängen — er
         lebt stabil in der .desc-toolbar; hier nur Zustand angleichen. */
      syncDescToggle(container.closest('.modal-desc-section'));
    }

    /* ADR-0076 §Nachtrag (#817): Aus dem Lese-Modus führt auch ein Klick
       auf den leeren Platzhalter bzw. ein Doppelklick auf einen Absatz in
       den Schreibmodus. Derselbe Weg wie beim Knopf: activateTyporaBlock
       committet einen offenen Block vorher (im Lese-Modus gibt es keinen). */
    const bearbeite = idx => {
      if (nurLesen || (rueckwegSperre && key === 'agent')) return;
      if (!interactive) { descEditActive = true; renderPane(key); }
      activateTyporaBlock(container, idx);
    };

    if (!text.trim()) {
      /* F-111 Variante A: Agent-Tab zeigt strukturierte ADR-0030-Vorlage als Placeholder.
         Desc-Tab zeigt einfaches „Klicken zum Schreiben…". */
      if (key === 'agent') {
        const placeholder = el('pre', {
          cls: 'md-empty typora-placeholder agent-placeholder',
        });
        placeholder.style.whiteSpace = 'pre-line';
        placeholder.textContent = I18n.t('agent-context.placeholder');
        /* Verify V-1 (Karte #826): auch der Platzhalter öffnet auf einer Spiegelkarte keinen Editor. */
        placeholder.addEventListener('click', () => { if (!nurLesen && !rueckwegSperre) activateTyporaBlock(container, null); });
        container.appendChild(placeholder);
      } else {
        const placeholder = el('p', {
          cls:  'md-empty typora-placeholder',
          text: I18n.t('md.click-write'),
        });
        placeholder.addEventListener('click', () => bearbeite(null));
        container.appendChild(placeholder);
      }
      return;
    }

    const blocks = Markdown.tokenizeBlocks(text);

    blocks.forEach((block, idx) => {
      const wrapper = el('div', { cls: 'typora-block' });
      wrapper.dataset.blockIdx = idx;

      const renderedEl = document.createElement('div');
      renderedEl.className = 'typora-rendered';
      Markdown.renderBlock(block, renderedEl, (lineIndex, checked) => {
        toggleChecklistLine(container, lineIndex, checked);
        renderPane(key);
      });
      wrapper.appendChild(renderedEl);

      /* Lese-Modus: Doppelklick statt Klick; Links und Häkchen bleiben
         per Einfachklick bedienbar und öffnen keinen Editor. */
      wrapper.addEventListener(interactive ? 'click' : 'dblclick', (e) => {
        /* Nacharbeit #817 K-4: auch Ziele IN einem Link (<a><strong>…). */
        if (e.target.closest?.('a, input')) return;
        if (wrapper.classList.contains('typora-editing')) return;
        bearbeite(idx);
      });

      container.appendChild(wrapper);
    });
  }

  /* lines/blocks werden intern immer frisch aus dem Pane-Text berechnet (F-62) */
  function activateTyporaBlock(container, blockIdx) {
    const key = paneKeyFromContainer(container);
    const active = container.querySelector('.typora-editing');
    if (active) commitTyporaBlock(active, key);

    if (blockIdx === null) {
      openNewBlockEditor(container, key);
      return;
    }

    const wrapper = container.querySelector(`[data-block-idx="${blockIdx}"]`);
    if (!wrapper) return;

    const text   = getPane(key);
    const lines  = text.split('\n');
    const blocks = Markdown.tokenizeBlocks(text);
    const block  = blocks[blockIdx];
    if (!block) return;
    const blockRaw = lines.slice(block.start, block.end).join('\n');

    wrapper.classList.add('typora-editing');
    while (wrapper.lastChild) wrapper.removeChild(wrapper.lastChild);

    const ta = el('textarea', { cls: 'typora-textarea' });
    ta.value = blockRaw;
    wrapper.appendChild(ta);

    ta.style.height = 'auto';
    ta.style.height = ta.scrollHeight + 'px';
    ta.addEventListener('input', () => {
      ta.style.height = 'auto';
      ta.style.height = ta.scrollHeight + 'px';
    });

    ta.addEventListener('blur', (e) => {
      /* Wandert der Fokus in die Toolbar, NICHT committen (Karte #450,
         Kritiker-Befund zu F-113).

         Warum: `commitTyporaBlock` + `renderPane` bauen den Pane neu auf, und
         damit verschwindet das `.typora-editing`-Element. `applyToActiveTextarea`
         findet danach kein Textarea mehr (`findActiveTextarea`) und steigt
         wortlos aus — die Toolbar-Aktion verpufft, oder sie trifft ueber den
         `.typora-new`-Fallback den falschen Block.

         Fuer die MAUS war das nie ein Problem: jeder Toolbar-Knopf hat
         `mousedown → preventDefault`, der Fokus bleibt im Textarea. Der
         TASTATUR-Weg hat diesen Schutz nicht — weder der normale Tab-Umlauf
         noch die mit F-113 ergaenzte Pfeil-Navigation im Ueberschriften-Menue.
         Der Kritiker hat gemessen: Maus setzt `## Ueberschrift`, Tastatur
         aendert nichts. Die Pfeil-Navigation hat den Weg dorthin nur sichtbar
         gemacht; kaputt war er schon vorher.

         `relatedTarget` ist das Element, das den Fokus BEKOMMT (in happy-dom
         nachgemessen: bei programmatischem `focus()` korrekt gesetzt). Faellt
         der Fokus ins Leere (`null`, z. B. Klick auf den Hintergrund), gilt der
         Normalfall und der Block wird committet. */
      const zielInToolbar = e?.relatedTarget?.closest?.('.desc-toolbar');
      if (zielInToolbar) return;
      commitTyporaBlock(wrapper, key);
      renderPane(key);
    });

    ta.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { ta.blur(); return; }
      /* Ctrl/Cmd+B → Fett, Ctrl/Cmd+I → Kursiv */
      if ((e.ctrlKey || e.metaKey) && e.key === 'b') {
        e.preventDefault();
        applyToActiveTextarea(wrapModifier('**', '**'));
      }
      if ((e.ctrlKey || e.metaKey) && e.key === 'i') {
        e.preventDefault();
        applyToActiveTextarea(wrapModifier('*', '*'));
      }
    });

    /* Toolbar-Aktiv-Indikator bei Cursor-/Selektion-Bewegung aktualisieren */
    ta.addEventListener('keyup',   () => updateToolbarState(ta));
    ta.addEventListener('mouseup', () => updateToolbarState(ta));
    /* selectionchange: gefiltert auf aktives Textarea */
    const onSelectionChange = () => {
      if (document.activeElement === ta) updateToolbarState(ta);
    };
    document.addEventListener('selectionchange', onSelectionChange);
    ta.addEventListener('blur', () => {
      document.removeEventListener('selectionchange', onSelectionChange);
    }, { once: true });

    ta.focus();
    ta.setSelectionRange(ta.value.length, ta.value.length);
    updateToolbarState(ta);
  }

  function commitTyporaBlock(wrapper, key) {
    if (!wrapper.classList.contains('typora-editing')) return;
    const ta = wrapper.querySelector('.typora-textarea');
    if (!ta) return;
    const blockIdx = parseInt(wrapper.dataset.blockIdx, 10);
    const text   = getPane(key);
    const lines  = text.split('\n');
    const blocks = Markdown.tokenizeBlocks(text);
    const block  = blocks[blockIdx];
    if (!block) return;
    const newBlock = ta.value.split('\n');
    const newLines = [...lines];
    newLines.splice(block.start, block.end - block.start, ...newBlock);
    setPane(key, newLines.join('\n'));
  }

  /* K-2 (F-307-Re-Kritiker): Eine offene Neu-Block-Textarea (`.typora-new`)
     wird NUR per blur committet. Der ADR-0076-„Fertig"-Toggle unterdrückt
     mit mousedown→preventDefault genau dieses blur; auch collectRawText
     (Speichern) committete bisher nur `.typora-editing`. Ohne diesen
     gemeinsamen Commit-Pfad ginge frisch getippter Text verloren. Rein
     additiv (committet höchstens MEHR Text, nie weniger). */
  function commitNewBlock(container, key) {
    const ta = container?.querySelector('.typora-new');
    if (!ta) return;
    const text = ta.value.trim();
    if (text) {
      const current = getPane(key);
      setPane(key, current ? current + '\n\n' + ta.value : ta.value);
    }
  }

  function openNewBlockEditor(container, key) {
    const ta = el('textarea', { cls: 'typora-textarea typora-new' });
    ta.placeholder = I18n.t('md.new-block-placeholder');
    /* Nacharbeit #817 O-1: Im Schreibmodus der leeren Beschreibung nur das
       Eingabefeld — „Klicken zum Schreiben…“ darüber wäre ein zweiter
       Platzhalter. Der Agent-Pane behält seine ADR-0030-Vorlage als Leitfaden. */
    if (key === 'desc') container.querySelector('.typora-placeholder')?.remove();
    container.appendChild(ta);
    ta.focus();
    ta.addEventListener('blur', () => {
      commitNewBlock(container, key);
      renderPane(key);
    });
  }

  /* ── Pane-Rendering ────────────────────────────────────────────── */

  /** Eine einzelne Pane anhand ihres Keys neu rendern */
  function renderPane(key) {
    const id = key === 'agent' ? 'agent-pane' : 'desc-pane';
    const container = overlay?.querySelector(`#${id}`);
    if (!container) return;
    renderTyporaView(container);
  }

  /** Aktive Pane ermitteln und neu rendern (wird von Toolbar-Aktionen aufgerufen) */
  function renderActivePane() {
    renderPane(activeTab === 'agent' ? 'agent' : 'desc');
  }

  /* Rückwärtskompatible Funktion — von toggleChecklistLine (desc-Tab) genutzt */
  function renderDescPane() {
    renderPane('desc');
  }

  /* ── Checkliste toggeln ────────────────────────────────────────── */

  function toggleChecklistLine(container, lineIndex, checked) {
    const key = paneKeyFromContainer(container);
    const lines = getPane(key).split('\n');
    if (lineIndex < 0 || lineIndex >= lines.length) return;
    lines[lineIndex] = checked
      ? lines[lineIndex].replace(/^(- \[) \]/, '$1x]')
      : lines[lineIndex].replace(/^(- \[)x\]/i, '$1 ]');
    setPane(key, lines.join('\n'));
    /* Checkbox-Toggle sofort persistieren incl. pendingComments — gehen sonst bei Abbruch verloren (F-41) */
    if (currentCard && key === 'desc') {
      /* Aktuelle Links aus State holen — link-Events laufen unabhängig vom Modal */
      const freshSelf = findCardById(currentCard.id);
      const currentLinks = Array.isArray(freshSelf?.card?.links)
        ? freshSelf.card.links
        : (Array.isArray(currentCard.links) ? currentCard.links : []);
      /* F-918: Auch dieser Pfad speichert OHNE bewussten Speichern-Klick (ein
         Checkbox-Haken in der Beschreibung genügt) und ersetzte die Karte im
         State bislang durch `{ ...currentCard, … }` — also durch den Stand vom
         ÖFFNEN des Modals. Eine fremde Änderung, die seither eingetroffen ist
         (Spalte, qa, Kommentar), war damit weg. Dieselbe Behandlung wie in
         saveAndClose(): frischer Stand als Basis, darüber nur, was der Nutzer
         wirklich geändert hat. `description` ist das nach dem Toggle
         zwangsläufig, `agentContext` nur, wenn er ihn auch angefasst hat.

         `currentCard` wird bewusst NICHT auf `basis` gesetzt, sondern nur in
         genau den Feldern nachgeführt, die dieser Pfad geschrieben hat: Es ist
         der Bezugspunkt für „was hat der Nutzer im FORMULAR geändert"
         (isDirty(), nurTatsaechlichGeaenderte()). Würde man den frischen Stand
         hineinkopieren, sähe jedes Formularfeld, das fremd geändert wurde,
         beim nächsten Schliessen wie eine Nutzer-Änderung aus — und der Fix
         hätte die Rückrollung nur nach hinten verschoben. */
      const basis = freshSelf?.card ?? currentCard;
      const patch = nurTatsaechlichGeaenderte({
        description:  getPane('desc'),
        agentContext: getPane('agent'),
        comments:     [...(currentCard.comments ?? []), ...pendingComments],
        links:        currentLinks,
      }, basis);
      EventBus.emit('card:updated', {
        ...basis,
        ...patch,
        updated: new Date().toISOString().slice(0, 10),
      });
      currentCard     = { ...currentCard, ...patch };
      pendingComments = [];
    }
  }

  /* ── Claude-Owner-Prüfung ──────────────────────────────────────── */

  /** Prüft ob ein Owner-Wert zur Claude-Familie gehört */
  function isClaudeOwner(owner) {
    return /^Claude(\s|$)/i.test(owner || '');
  }

  /** Prüft ob der Agent-Kontext-Tab sichtbar sein soll.
   *  #302: Auch sichtbar wenn ein Assignee Claude-Bezug hat (ADR-0030 erweitert). */
  function agentTabVisible(owner, assignees) {
    if (!Profile.has('feature.agentContext')) return false;
    if (isClaudeOwner(owner)) return true;
    return Array.isArray(assignees) && assignees.some(isClaudeOwner);
  }

  /* ── Sprint-Meta-Karten-Modus ─────────────────────────────────── */

  /** Schaltet das Modal in den eingeschränkten Meta-Karten-Modus:
   *  - Nur Beschreibungs-Tab und Kommentare-Tab sichtbar
   *  - Titel-Feld read-only (Titel ist fix)
   *  - Meta-Felder (Priorität, Owner, Komplexität, etc.) read-only / ausgeblendet
   *  - Delete-Button und Archiv-Button ausblenden
   *
   * F-160: Bei isMetaCard=false werden tab-attachments/tab-links/tab-history
   * explizit wieder eingeblendet — sonst bleiben sie ausgeblendet, wenn vorher
   * eine Meta-Karte geöffnet war. tab-agent bleibt Sache von updateTabVisibility
   * (abhängig von Owner + boardMode).
   */
  function updateMetaCardMode(isMetaCard) {
    if (!overlay) return;

    const metaSpecificTabs = ['tab-attachments', 'tab-links', 'tab-history'];
    metaSpecificTabs.forEach(tabId => {
      const tabEl = overlay.querySelector(`#${tabId}`);
      if (tabEl) tabEl.style.display = isMetaCard ? 'none' : '';
    });
    if (isMetaCard) {
      const tabAgent = overlay.querySelector('#tab-agent');
      if (tabAgent) tabAgent.style.display = 'none';
      /* #299: KI-Tab auf Meta-Karten irrelevant — explizit ausblenden. */
      const tabLLM = overlay.querySelector('#tab-llm');
      if (tabLLM) tabLLM.style.display = 'none';
    }

    /* Titel-Feld read-only */
    const titleInput = overlay.querySelector('#modal-title');
    if (titleInput) titleInput.readOnly = isMetaCard;

    /* Meta-Felder ausblenden — Meta-Rows im Desc-Tab */
    const metaRows = overlay.querySelectorAll('.modal-meta-row');
    /* Karte #618: Zeilen mit eigener Sichtbarkeitsregel (Übernahme, Projekt)
       nur für Meta-Karten ausblenden, nie einblenden. Diese Funktion läuft in
       open() NACH deren Befüllung; `''` machte die Übernahme-Zeile bei jeder
       normalen Karte sichtbar — leeres Grund-Feld, grauer Knopf, auch ohne
       Übernahme (Sprint 77, #606). */
    metaRows.forEach(row => {
      if (isMetaCard) row.style.display = 'none';
      else if (row.id !== 'modal-claim-row' && row.id !== 'modal-meta-row-project') row.style.display = '';
    });
    /* ADR-0041: Recurrence-Zeile ausblenden für Meta-Karten (wird durch .modal-meta-row bereits erfasst) */

    /* Labels-Feld ausblenden */
    const labelsField = overlay.querySelector('#modal-labels')?.closest('.modal-field');
    if (labelsField) labelsField.style.display = isMetaCard ? 'none' : '';

    /* Flag-Row und Termine-Sektion (#817: sonst bliebe ein leerer Kopf) ausblenden */
    zeigeFlagsUndTermine(!isMetaCard);

    /* Delete- und Archiv-Button ausblenden */
    const deleteBtn = overlay.querySelector('.btn-card-delete');
    if (deleteBtn) deleteBtn.style.display = isMetaCard ? 'none' : '';
    const archiveBtn = overlay.querySelector('#modal-archive-btn');
    if (archiveBtn && isMetaCard) archiveBtn.style.display = 'none';
  }

  /* Karte #817: Flag-Pillen und Termine-Sektion gemeinsam ein-/ausblenden
     (Meta- und Template-Karten). */
  function zeigeFlagsUndTermine(an) {
    for (const s of ['.flag-row', '#modal-termine']) overlay.querySelector(s).style.display = an ? '' : 'none';
  }

  /* ── Template-Modus (ADR-0040, Karte #65) ─────────────────────── */

  /** Schaltet das Modal in den eingeschränkten Template-Bearbeitungs-Modus:
   *  - Tabs Anhänge, Verknüpfungen, Verlauf, Kommentare ausblenden (irrelevant für Skelette)
   *  - Priorität-/Datum-/Flag-Zeilen ausblenden (nicht in Templates gespeichert)
   *  - „Als Template speichern"-Button ausblenden (schon ein Template)
   *  - Archiv-Button ausblenden
   *  Bei isTemplateCard=false: alle Tabs wieder einblenden (Normalzustand).
   */
  function updateTemplateMode(isTemplateCard) {
    if (!overlay) return;

    const hiddenTabs = ['tab-attachments', 'tab-links', 'tab-history', 'tab-comments'];
    hiddenTabs.forEach(tabId => {
      const tabEl = overlay.querySelector(`#${tabId}`);
      if (tabEl) tabEl.style.display = isTemplateCard ? 'none' : '';
    });
    /* #299: KI-Tab in Templates ausblenden — Send braucht eine real-existierende Karte. */
    if (isTemplateCard) {
      const tabLLM = overlay.querySelector('#tab-llm');
      if (tabLLM) tabLLM.style.display = 'none';
    }

    /* Datumsfelder, Recurrence und Flags sind in Templates nicht sinnvoll
       (Karte #817: Datum und Recurrence stecken in der Termine-Sektion) */
    zeigeFlagsUndTermine(!isTemplateCard);

    /* Archiv-Button: bei Templates immer ausblenden; bei Normalkarten bestehende Logik nicht stören */
    if (isTemplateCard) {
      const archiveBtn = overlay.querySelector('#modal-archive-btn');
      if (archiveBtn) archiveBtn.style.display = 'none';
    }

    /* „Als Template speichern"-Button: bei Template selbst ausblenden */
    const saveAsTplBtn = overlay.querySelector('#modal-save-as-template-btn');
    if (saveAsTplBtn) saveAsTplBtn.style.display = isTemplateCard ? 'none' : '';
  }

  /* #294/ADR-0092: Service-Board-Modus — Spam-Button (un)sichtbar machen und
     je nach Karten-Spalte umlabeln. „🚫 Als Spam markieren" außerhalb col-spam,
     „Kein Spam" innerhalb col-spam. Nur in Lanes mit kind:service. */
  function updateServiceBoardMode(card) {
    if (!overlay) return;
    const btn = overlay.querySelector('#modal-spam-btn');
    if (!btn) return;
    const found = card && findCardById(card.id);
    const isService = found?.sprint?.kind === 'service';
    if (!isService) { btn.style.display = 'none'; return; }
    btn.style.display = '';
    const inSpam = card.columnId === 'col-spam';
    btn.textContent = inSpam ? I18n.t('card.action.no-spam') : I18n.t('card.action.spam');
    btn.title = btn.textContent;
  }

  /* #296: Transfer-Button-Sichtbarkeit — nur Server-Modus + Nicht-Meta. */
  function updateTransferBtnVisibility(card) {
    if (!overlay) return;
    const btn = overlay.querySelector('#modal-transfer-btn');
    if (!btn) return;
    const isMeta = card?.kind === 'sprint-meta';
    const inServerMode = Profile.has('feature.restStorage');
    btn.style.display = (!isMeta && inServerMode) ? '' : 'none';
  }

  /* #672/ADR-0133 §7: Knopf „In Open WebUI weiterbearbeiten“. Die Adresse der
     Open-WebUI-Instanz kommt als `owui_url` aus GET /me (Server-Umgebungsvariable
     ABOARD_OWUI_URL, leer = Funktion aus) und wird je Seitenladung einmal
     geholt. Alles hinter IS_SERVER_BUILD → im Solo-Template per DCE entfernt. */
  let owuiUrl = null; /* null = noch nicht geholt, '' = nicht konfiguriert */
  async function holeOwuiUrl() {
    if (owuiUrl !== null) return owuiUrl;
    const plugin = StoragePlugin.getActivePlugin();
    if (!plugin || plugin.kind !== 'rest') return '';
    let me = null;
    try { me = await plugin.getCurrentUser?.(); } catch { /* Knopf bleibt aus */ }
    const u = typeof me?.owui_url === 'string' ? me.owui_url : '';
    /* Nur ein Erfolg wird gemerkt; ein Netzfehler liefert null-Felder (=''),
       soll beim nächsten Öffnen aber erneut versucht werden. */
    if (me && 'owui_url' in me) owuiUrl = u;
    return u;
  }

  function updateOwuiBtnVisibility(card) {
    if (!overlay) return;
    const btn = overlay.querySelector('#modal-owui-btn');
    if (!btn) return;
    btn.style.display = 'none';
    if (card?.kind === 'sprint-meta' || !Profile.has('feature.restStorage')) return;
    holeOwuiUrl().then(u => {
      /* Das Modal kann inzwischen geschlossen/neu aufgebaut sein. */
      const b = overlay?.querySelector('#modal-owui-btn');
      if (b && u) b.style.display = '';
    });
  }

  /* Baut die Open-WebUI-Adresse: nur Kartennummer, Titel und Boardname — KEIN
     Karteninhalt (stünde sonst in Browser-Verlauf und Proxy-Logs); den holt das
     Modell über das Werkzeug karte_zeigen. `submit=false` ist Pflicht, sonst
     schickt Open WebUI den Text sofort ab. */
  function baueOwuiUrl(basis, card, boardTitel, boardId) {
    const titel = String(card?.title ?? '').slice(0, 200);
    const nr = card?.displayId != null ? `#${card.displayId}` : `(${card?.id ?? ''})`;
    const q = I18n.t('card.owui.prompt', nr, titel, String(boardTitel ?? '').slice(0, 200), String(boardId ?? '').slice(0, 100));
    return `${basis.replace(/\/+$/, '')}/?submit=false&q=${encodeURIComponent(q)}`;
  }

  /* #685 (ADR-0134 §3): Knopf „Extern bearbeiten“. Nur im Server-Modus
     (Profile feature.restStorage); den Rest entscheidet SchleuseAuszug. */
  function updateSchleuseBtn(card) {
    if (!overlay || !SchleuseAuszug) return;
    const btn = overlay.querySelector('#modal-schleuse-btn');
    if (!btn) return;
    if (!Profile.has('feature.restStorage')) { btn.style.display = 'none'; return; }
    SchleuseAuszug.aktualisiereKnopf(btn, card, State.get()?.board?.id ?? null);
  }

  function openSchleuseAuszug() {
    if (!currentCard || !SchleuseAuszug) return;
    const card = currentCard;
    SchleuseAuszug.open(State.get()?.board?.id ?? null, card, () => updateSchleuseBtn(card));
  }

  function openInOpenWebUI() {
    if (!currentCard || !owuiUrl) return;
    const url = baueOwuiUrl(owuiUrl, currentCard, State.get()?.board?.title ?? '', State.get()?.board?.id ?? '');
    /* Direkt im Klick-Pfad öffnen (Popup-Blocker); noopener: Open WebUI
       bekommt keinen Zugriff auf dieses Fenster. */
    window.open(url, '_blank', 'noopener,noreferrer');
  }

  /* #296: CSS für Transfer-Picker lazily injizieren. JS-Inject statt style.css,
     damit der slim-Build (IS_SERVER_BUILD=false) das CSS via DCE eliminiert. */
  let _transferStylesInjected = false;
  function _ensureTransferStyles() {
    if (_transferStylesInjected) return;
    _transferStylesInjected = true;
    const st = document.createElement('style');
    st.setAttribute('data-aboard', 'card-transfer');
    st.textContent = '.modal-transfer-overlay{position:absolute;inset:0;z-index:10;background:rgba(0,0,0,.45);display:flex;align-items:center;justify-content:center;padding:20px}.modal-transfer-box{background:var(--surface);border-radius:var(--radius);box-shadow:0 8px 32px rgba(0,0,0,.25);padding:18px 20px;width:min(420px,100%);display:flex;flex-direction:column;gap:8px}.modal-transfer-box h3{margin:0 0 8px;font-size:15px}.modal-transfer-box label{font-size:12px;color:var(--text-muted);margin-top:6px}.modal-transfer-box select{padding:6px 8px;font-size:13px}.modal-transfer-msg{color:var(--danger,#c33);font-size:12px;min-height:1em}.modal-transfer-btnrow{display:flex;justify-content:flex-end;gap:8px;margin-top:10px}';
    document.head.appendChild(st);
  }

  /* #296: Picker-Overlay öffnen — Board-Liste (ACL-gefiltert) + Spalten-Mapping,
     Submit ruft StoragePlugin.transferCard(); bei Erfolg Modal schließen +
     EventBus-Hinweis (Board-Wechsel oder Reload bleibt dem Aufrufer überlassen). */
  async function openTransferPicker() {
    if (!currentCard || !overlay) return;
    const plugin = StoragePlugin.getActivePlugin();
    if (!plugin || plugin.kind !== 'rest') return;

    _ensureTransferStyles();
    const cardId = currentCard.id;
    const currentBoardId = State.get()?.board?.id ?? null;
    /* Sprint-54-Kritiker: Die Karte, für die dieser Picker geöffnet wurde.
       Der Submit-Handler unten ruft nach einem `await` ein `close()`, das
       bedingungslos die AKTUELLE Modal-Instanz schliesst — auch wenn das
       inzwischen eine andere Karte ist. Der Vergleich unten verhindert das. */
    const pickerCardId = cardId;

    const wrap = el('div', { cls: 'modal-transfer-overlay' });
    wrap.setAttribute('role', 'dialog');
    wrap.setAttribute('aria-modal', 'true');
    const box = el('div', { cls: 'modal-transfer-box' });
    box.appendChild(el('h3', { text: I18n.t('card.transfer.title') }));

    const boardLabel = el('label', { text: I18n.t('card.transfer.target-board'),
                                      for: 'transfer-board-sel' });
    const boardSel = el('select', { id: 'transfer-board-sel' });
    boardSel.appendChild(el('option', { value: '', text: I18n.t('card.transfer.loading') }));
    boardSel.disabled = true;
    box.appendChild(boardLabel);
    box.appendChild(boardSel);

    const colLabel = el('label', { text: I18n.t('card.transfer.target-column'),
                                    for: 'transfer-col-sel' });
    const colSel = el('select', { id: 'transfer-col-sel' });
    colSel.disabled = true;
    box.appendChild(colLabel);
    box.appendChild(colSel);

    const msg = el('div', { cls: 'modal-transfer-msg' });
    box.appendChild(msg);

    const btnRow = el('div', { cls: 'modal-transfer-btnrow' });
    const cancelB = el('button', { cls: 'btn', type: 'button',
                                    text: I18n.t('card.transfer.cancel') });
    const okB = el('button', { cls: 'btn btn-primary', type: 'button',
                                text: I18n.t('card.transfer.submit') });
    okB.disabled = true;
    btnRow.appendChild(cancelB);
    btnRow.appendChild(okB);
    box.appendChild(btnRow);
    wrap.appendChild(box);
    overlay.appendChild(wrap);

    function closePicker() { wrap.remove(); }
    cancelB.addEventListener('click', closePicker);
    wrap.addEventListener('click', (e) => { if (e.target === wrap) closePicker(); });

    /* Boards laden + ACL-Filter: nur owner/editor, ohne current Board. */
    let boards = [];
    try { boards = await plugin.listKnownBoards(); } catch { boards = []; }
    const targets = (boards || []).filter(b =>
      b.id !== currentBoardId
      && (b.role === 'owner' || b.role === 'editor' || !b.role) /* Legacy-Bypass: kein role-Feld */
    );
    boardSel.innerHTML = '';
    if (targets.length === 0) {
      boardSel.appendChild(el('option', { value: '',
                                           text: I18n.t('card.transfer.no-targets') }));
      boardSel.disabled = true;
      return;
    }
    boardSel.disabled = false;
    boardSel.appendChild(el('option', { value: '', text: '—' }));
    targets.forEach(b => {
      boardSel.appendChild(el('option', { value: b.id,
                                           text: b.title || b.name || b.id }));
    });

    /* Beim Board-Wechsel: Spalten holen + Default-Match auf Titel. */
    const currentColTitle = (State.get()?.board?.columnDefs || [])
      .find(c => c.id === currentCard.columnId)?.title || '';
    /* F-389: Generations-Marke gegen out-of-order-Antworten. Jeder Wechsel
       zählt hoch; eine Antwort zählt nur, wenn seither kein weiterer Wechsel
       stattfand. Ohne das zeigt die Folge A→B→A die Spalten des zuletzt
       ANTWORTENDEN Boards statt des zuletzt GEWÄHLTEN — und der Nutzer
       transferiert in eine Spalte, die er nie ausgewählt hat.
       Die zusätzliche Prüfung auf boardSel.value ist kein Doppelt-Gemoppel:
       sie fängt den Fall, dass der Nutzer zwischenzeitlich auf den
       Leer-Eintrag „—" zurückgeht — dann läuft kein neuer Request, die
       Generation bleibt stehen, und die alte Antwort dürfte trotzdem nicht
       mehr wirken. */
    let colGen = 0;
    boardSel.addEventListener('change', async () => {
      const gen = ++colGen;
      colSel.innerHTML = '';
      colSel.disabled = true;
      okB.disabled = true;
      msg.textContent = '';
      const id = boardSel.value;
      if (!id) return;
      colSel.appendChild(el('option', { value: '',
                                         text: I18n.t('card.transfer.loading-columns') }));
      const r = await plugin.peekBoardColumns(id);
      if (gen !== colGen || boardSel.value !== id) return; /* veraltete Antwort */
      colSel.innerHTML = '';
      if (!r.ok || !Array.isArray(r.columnDefs) || r.columnDefs.length === 0) {
        msg.textContent = I18n.t('card.transfer.fail');
        return;
      }
      r.columnDefs.forEach(c => {
        colSel.appendChild(el('option', { value: c.id, text: c.title }));
      });
      /* Default: Titel-Match, sonst erste Spalte. */
      const match = r.columnDefs.find(c => c.title === currentColTitle);
      colSel.value = match ? match.id : r.columnDefs[0].id;
      colSel.disabled = false;
      okB.disabled = false;
    });

    okB.addEventListener('click', async () => {
      const toBoardId = boardSel.value;
      const toColumnId = colSel.value;
      if (!toBoardId || !toColumnId) return;
      okB.disabled = true;
      cancelB.disabled = true;
      msg.textContent = '';
      const res = await plugin.transferCard(cardId, toBoardId, toColumnId);
      /* Zeigt das Modal noch auf die Karte, für die dieser Transfer lief?
         Sonst NICHT schliessen (Sprint-54-Frontend-Kritiker, P2).

         Fehlschlag-Szenario ohne diese Prüfung: Nutzer öffnet Karte A,
         startet den Transfer, schliesst das Modal mit Escape (der globale
         Escape-Handler kennt das verschachtelte Transfer-Overlay nicht) und
         öffnet Karte B. Die Antwort für A kommt an und ruft `close()` — das
         schliesst Karte B ungefragt, gibt über `revokeTrackedUrls()` deren
         Anhang-URLs frei (Vorschaubilder brechen weg) und setzt den Fokus
         auf das Element, das VOR Karte A aktiv war.

         F-389 deckte das nicht ab: dort ging es um die Dropdown-Anzeige
         während der Auswahl, hier um den Erfolgspfad nach dem Absenden. */
      const nochDieselbeKarte = currentCard && currentCard.id === pickerCardId;
      if (res.ok) {
        closePicker();
        if (nochDieselbeKarte) close();
        /* Board-Refresh: aktueller View bleibt; transferierte Karte ist weg.
           EventBus-Signal optional — Polling holt den State binnen 10 s. */
        EventBus.emit('card:transferred', { cardId, toBoardId, toColumnId,
                                             newDisplayId: res.body?.newDisplayId });
        return;
      }
      /* Auch der Fehlerpfad: Ist das Modal weitergezogen, hängt dieser Picker
         gar nicht mehr im Dokument — dann wäre die Meldung unsichtbar und die
         Knöpfe wieder aktiv zu schalten wirkungslos. Sauberer abräumen. */
      if (!nochDieselbeKarte) { closePicker(); return; }
      msg.textContent = (res.status === 403)
        ? I18n.t('card.transfer.fail-forbidden')
        : I18n.t('card.transfer.fail');
      okB.disabled = false;
      cancelB.disabled = false;
    });
  }

  /* #294/ADR-0092: Toggle Karte ↔ col-spam + Server-Blocklist-Mutation.
     Sender-Email aus card.senderEmail (von #295-Mail-Ingest gesetzt) oder
     interaktiv per prompt() erfragt. Server-Build-only. */
  async function toggleSpamAndClose() {
    if (!currentCard) return;
    const found = findCardById(currentCard.id);
    if (found?.sprint?.kind !== 'service') return;
    const inSpam = currentCard.columnId === 'col-spam';
    const undo   = inSpam;
    let sender = currentCard.senderEmail || '';
    if (!sender) {
      sender = (prompt(I18n.t('card.action.spam-prompt'), '') || '').trim();
      if (!sender) return;
    }
    try {
      const res = await (IS_SERVER_BUILD ? holen : fetch)('/mail-spam-report', { /* F-2977: im Standalone-Template bleibt es fetch (Größe) */
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ senderEmail: sender, undo }),
      });
      if (!res.ok) { alert(I18n.t('card.action.spam-fail')); return; }
    } catch { alert(I18n.t('card.action.spam-fail')); return; }
    const base = found?.card ?? currentCard;

    /* F-915: dieselbe Begründung/Lösung wie bei archiveAndClose() (siehe
       dort, auch für die Sonderbehandlung des leeren Titels) —
       toggleSpamAndClose() schliesst das Modal ebenfalls (close() unten) und
       baute sein card:updated bislang nur aus dem State (`base`), ohne
       offene Formular-Änderungen zu berücksichtigen. Jetzt dieselbe
       computeFormPatch()-Basis.

       F-918: … und dieselbe Reduktion auf die tatsächlich geänderten Felder
       wie dort. `base` wird bewusst NICHT auf frischeBasis() umgestellt: Es
       stammt aus `found`, das VOR dem `await fetch(...)` gelesen wurde, und
       damit aus derselben Aufruf-Runde wie die Service-Lane-Prüfung
       darüber. Beide dürfen nicht auf verschiedene Stände schauen — die
       Karte könnte während des Netzaufrufs die Swimlane gewechselt haben,
       und dann hätte der Toggle auf einer Lane gerechnet, die er nie
       geprüft hat. Die fremde Änderung, um die es F-918 geht, ist in `found`
       ohnehin schon enthalten: gelesen wird es beim Klick, nicht beim
       Öffnen des Modals. */
    const patch = nurTatsaechlichGeaenderte(computeFormPatch(), base);
    if ('title' in patch && !patch.title) delete patch.title;

    EventBus.emit('card:updated', {
      ...base,
      ...patch,
      columnId: undo ? 'col-inbox' : 'col-spam',
      updated:  new Date().toISOString().slice(0, 10),
    });
    close();
  }

  /* ── Tab-Sichtbarkeit aktualisieren ───────────────────────────── */

  function updateTabVisibility(owner) {
    if (!overlay) return;
    /* Karte #817: Jede Änderung an Owner/Beteiligten läuft hier durch. */
    zeigeAssigneeChips();
    const tabAgent = overlay.querySelector('#tab-agent');
    if (!tabAgent) return;
    const aSel = overlay.querySelector('#modal-assignees');
    const assignees = aSel ? [...aSel.selectedOptions].map(o => o.value) : [];
    const visible = agentTabVisible(owner, assignees);
    tabAgent.style.display = visible ? '' : 'none';

    /* Falls Agent-Tab aktiv war und nun ausgeblendet wird → Fallback auf desc */
    if (!visible && activeTab === 'agent') {
      switchTab('desc');
    }
    /* Falls Agent-Tab nun sichtbar UND war nicht sichtbar → kein Auto-Switch */
  }

  /* #299/ADR-0095: KI-Tab sichtbar nur im Server-Modus + ≥1 hinterlegter
     Provider-Key. async wegen Keys-Lookup; Cache in LLMTab vermeidet
     wiederholte Round-Trips. Im Slim-Build per DCE komplett entfernt. */
  async function updateLLMTabVisibility() {
    if (!IS_SERVER_BUILD || !overlay || !LLMTab) return;
    const tabLLM = overlay.querySelector('#tab-llm');
    if (!tabLLM) return;
    const inServerMode = Profile.has('feature.restStorage');
    if (!inServerMode) {
      tabLLM.style.display = 'none';
      if (activeTab === 'llm') switchTab('desc');
      return;
    }
    let hasKey = false;
    try { hasKey = await LLMTab.hasAnyKey(); } catch { hasKey = false; }
    /* Karte #826: Auf einer Spiegelkarte schreibt die KI nichts — Tab weg
       (Karte #827: auch bei Rückweg an). */
    const zeigen = hasKey && !nurLesen && !rueckwegSperre;
    tabLLM.style.display = zeigen ? '' : 'none';
    if (!zeigen && activeTab === 'llm') switchTab('desc');
  }

  /* ── Tab-Wechsel ───────────────────────────────────────────────── */

  function switchTab(tabKey) {
    if (!overlay) return;

    /* Aktiven Typora-Block des bisherigen Tab committen */
    const prevPaneKey = activeTab === 'agent' ? 'agent' : 'desc';
    const prevContainer = overlay.querySelector(prevPaneKey === 'agent' ? '#agent-pane' : '#desc-pane');
    if (prevContainer) {
      const editingWrapper = prevContainer.querySelector('.typora-editing');
      if (editingWrapper) commitTyporaBlock(editingWrapper, prevPaneKey);
    }

    activeTab = tabKey;

    /* Tab-Buttons aktualisieren */
    overlay.querySelectorAll('.tab-btn').forEach(btn => {
      const isActive = btn.dataset.tab === tabKey;
      btn.classList.toggle('tab-active', isActive);
      btn.setAttribute('aria-selected', isActive ? 'true' : 'false');
    });

    /* Tab-Panels umschalten */
    overlay.querySelectorAll('.tab-panel').forEach(panel => {
      const isActive = panel.dataset.tabPanel === tabKey;
      panel.classList.toggle('tab-panel-active', isActive);
    });

    /* #856: Kommentar-Tab = volle Modal-Höhe, neuester Kommentar sichtbar */
    kommentarModusSetzen(tabKey === 'comments');
    if (tabKey === 'comments') {
      kommentarEingabeAnpassen(overlay.querySelector('#comment-input'));
      const kl = overlay.querySelector('#comments-list');
      if (kl) kl.scrollTop = kl.scrollHeight;
    }

    /* Pane neu rendern wenn desc oder agent */
    if (tabKey === 'desc' || tabKey === 'agent') {
      renderPane(tabKey);
    }
    /* Links-Tab: Liste neu aufbauen (externe Änderungen möglich) */
    if (tabKey === 'links') {
      /* Karte frisch aus State holen (externe Sync-Updates könnten links geändert haben) */
      const fresh = findCardById(currentCard?.id);
      if (fresh) currentCard = { ...currentCard, links: fresh.card.links };
      renderLinksList();
    }
    /* Attachments-Tab: Liste neu aufbauen (externe Änderungen möglich) */
    if (tabKey === 'attachments') {
      const fresh = findCardById(currentCard?.id);
      if (fresh) currentCard = { ...currentCard, attachments: fresh.card.attachments };
      renderAttachmentsList();
    }
    if (tabKey === 'links' || tabKey === 'attachments') sperreNachNeuaufbau();
    /* Verlauf-Tab: Liste neu aufbauen (ADR-0037) */
    if (tabKey === 'history') {
      const fresh = findCardById(currentCard?.id);
      const activity = fresh?.card?.activity ?? currentCard?.activity ?? [];
      const histList = overlay.querySelector('#history-list');
      if (histList) renderHistoryList(histList, activity);
    }
    /* KI-Tab: Composer + Preview frisch rendern (#299/ADR-0095).
       Beim Verlassen des Tabs Polling stoppen — sonst läuft der Timer beim
       Wechsel zu desc/comments weiter und verbraucht unnötig Netzwerk. */
    if (IS_SERVER_BUILD && LLMTab) {
      if (tabKey === 'llm' && currentCard) {
        const boardId = State.get()?.board?.id ?? null;
        LLMTab.render(currentCard, boardId);
      } else {
        LLMTab.stopPolling();
      }
    }

    /* Maximierungs-Zustand zurücksetzen falls Pane wechselt (F-108: korrekter Key) */
    if (maximizedPaneKey !== null) {
      const sectionId = maximizedPaneKey === 'agent' ? '#tab-panel-agent .modal-desc-section' : '#tab-panel-desc .modal-desc-section';
      const section = overlay.querySelector(sectionId);
      const maxBtnSel = maximizedPaneKey === 'agent' ? '#tab-panel-agent .btn-maximize' : '#tab-panel-desc .btn-maximize';
      const maxBtn  = overlay.querySelector(maxBtnSel);
      if (section && maxBtn) setMaximized(section, maxBtn, false);
    }
  }

  /* ── Overlay bauen ─────────────────────────────────────────────── */

  function buildOverlay() {
    /* F-749: eigene Marker-Klasse, damit main.js#onExternalUpdate gezielt
       NUR das Karten-Modal (und den Config-Dialog) trifft — seit #348 teilen
       sich alle acht Settings-Dialoge die Basisklasse `.modal-overlay`
       (modal-shell.js), ein reiner `.modal-overlay[aria-hidden="false"]`-
       Selektor würde sie seither versehentlich mitfangen. */
    const ov = el('div', { cls: 'modal-overlay modal-overlay--blocks-sync' });
    /* Kritik K-1 (Karte #826): Sperre in der Capture-Phase. Sie fängt Klicks
       und Änderungen an Bedienelementen ab, die nach open() neu entstanden
       und deshalb nicht gesperrt sind — der Server wiese ihre Ops ohnehin ab. */
    if (IS_SERVER_BUILD) {
      const sperre = e => {
        if (!nurLesen && !rueckwegSperre) return;
        const z = e.target?.closest?.('button, input, textarea, select');
        if (!z || z.matches(sperrFrei())) return;
        e.preventDefault();
        e.stopImmediatePropagation();
      };
      ov.addEventListener('click', sperre, true);
      ov.addEventListener('change', sperre, true);
    }
    ov.setAttribute('aria-hidden', 'true');

    const modal = el('div', { cls: 'modal' });
    modal.setAttribute('role', 'dialog');
    modal.setAttribute('aria-modal', 'true');
    modal.setAttribute('aria-labelledby', 'modal-title'); /* F-109: ID existiert als #modal-title */

    /* Titelzeile: editierbarer Titel + Direktlink-ID + Schließen-Kreuz (ADR-0028, #82) */
    const modalHeader = el('div', { cls: 'modal-header-row' });
    const titleLeft   = el('div', { cls: 'modal-title-left' });
    const titleInput  = el('input', { type: 'text', id: 'modal-title', cls: 'modal-title-header', autocomplete: 'off' });
    titleInput.setAttribute('aria-label', I18n.t('card.title'));
    const cardIdLink  = el('a', { cls: 'modal-card-id', id: 'modal-card-id', href: '#' });
    /* Direktklick verhindert Re-Open (würde ungespeicherte Edits verwerfen) — Rechtsklick „Link kopieren" bleibt */
    cardIdLink.addEventListener('click', e => e.preventDefault());
    titleLeft.appendChild(titleInput);
    titleLeft.appendChild(cardIdLink);

    /* Schließen-Kreuz oben rechts (#82) */
    const closeBtn = el('button', { cls: 'btn-modal-close', type: 'button', text: '×' });
    closeBtn.setAttribute('aria-label', I18n.t('btn.close-modal'));
    closeBtn.setAttribute('title', I18n.t('btn.close-modal'));

    /* Papierkorb — wird später in den Footer verschoben (#82) */
    const deleteBtn   = el('button', { cls: 'btn-card-delete', type: 'button' });
    deleteBtn.setAttribute('aria-label', I18n.t('btn.delete-card'));
    deleteBtn.setAttribute('title', I18n.t('btn.delete-card'));
    deleteBtn.appendChild(trashIcon(14));

    modalHeader.appendChild(titleLeft);
    modalHeader.appendChild(closeBtn);
    modal.appendChild(modalHeader);

    /* Scrollbarer Body (alles zwischen Header und Footer) */
    const modalBody = el('div', { cls: 'modal-body' });

    /* ── Tab-Bar ───────────────────────────────────────────────── */
    const tabBar = el('div', { cls: 'modal-tab-bar' });
    tabBar.setAttribute('role', 'tablist');
    tabBar.setAttribute('aria-label', I18n.t('card.edit-title'));

    function makeTabBtn(tabKey, labelKey, id) {
      const btn = el('button', {
        cls:  'tab-btn',
        type: 'button',
        id,
      });
      btn.setAttribute('role', 'tab');
      btn.setAttribute('aria-selected', 'false');
      btn.setAttribute('aria-controls', `tab-panel-${tabKey}`);
      btn.dataset.tab = tabKey;
      /* Tab-Label als Text-Span (ermöglicht Badge-Kind-Element davor) */
      const labelSpan = el('span', { text: I18n.t(labelKey) });
      btn.appendChild(labelSpan);
      btn.addEventListener('click', () => switchTab(tabKey));
      return btn;
    }

    const tabDesc        = makeTabBtn('desc',        'tab.description',   'tab-desc');
    const tabComments    = makeTabBtn('comments',    'tab.comments',      'tab-comments');
    const tabAttachments = makeTabBtn('attachments', 'tab.attachments',   'tab-attachments');
    const tabAgent       = makeTabBtn('agent',       'tab.agent-context', 'tab-agent');
    const tabLinks       = makeTabBtn('links',       'tab.links',         'tab-links');
    const tabHistory     = makeTabBtn('history',     'tab.history',       'tab-history');

    tabBar.appendChild(tabDesc);
    tabBar.appendChild(tabComments);
    tabBar.appendChild(tabAttachments);
    tabBar.appendChild(tabAgent);
    tabBar.appendChild(tabLinks);
    tabBar.appendChild(tabHistory);

    /* ADR-0133 §1: Finding-Tab — nur bei kind:'finding' (open() blendet ein) */
    const tabFinding = makeTabBtn('finding', 'tab.finding', 'tab-finding');
    tabFinding.style.display = 'none';
    tabBar.appendChild(tabFinding);

    /* #299/ADR-0095: KI-Tab — nur im Server-Build vorhanden (Slim-DCE). */
    if (IS_SERVER_BUILD) {
      const tabLLM = makeTabBtn('llm', 'llm.tab.title', 'tab-llm');
      tabLLM.style.display = 'none'; /* Sichtbarkeit per updateLLMTabVisibility() */
      tabBar.appendChild(tabLLM);
    }

    /* Arrow-Key-Navigation zwischen Tabs */
    tabBar.addEventListener('keydown', (e) => {
      const tabs = [...tabBar.querySelectorAll('.tab-btn:not([style*="display: none"])')];
      const idx  = tabs.indexOf(document.activeElement);
      if (idx === -1) return;
      if (e.key === 'ArrowRight' || e.key === 'ArrowLeft' || e.key === 'Home' || e.key === 'End') {
        e.preventDefault();
        let next;
        if (e.key === 'ArrowRight') next = (idx + 1) % tabs.length;
        else if (e.key === 'ArrowLeft') next = (idx - 1 + tabs.length) % tabs.length;
        else if (e.key === 'Home') next = 0;
        else next = tabs.length - 1;
        tabs[next].focus();
        switchTab(tabs[next].dataset.tab);
      }
    });

    modalBody.appendChild(tabBar);

    /* ── Tab-Panels ────────────────────────────────────────────── */

    /* Panel: Beschreibung */
    const panelDesc = el('div', { cls: 'tab-panel tab-panel-active', id: 'tab-panel-desc' });
    panelDesc.setAttribute('role', 'tabpanel');
    panelDesc.setAttribute('aria-labelledby', 'tab-desc');
    panelDesc.dataset.tabPanel = 'desc';

    const descSection = el('div', { cls: 'modal-desc-section' });

    /* Markdown-Toolbar inkl. Maximize-Button (Karte #111) */
    const { bar: toolbar, maxBtn } = buildToolbar('desc', descSection);

    const descPane = el('div', { cls: 'desc-pane', id: 'desc-pane' });

    /* Esc im maximierten Zustand abfangen — Modal nicht schließen */
    descSection.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && maximizedPaneKey === 'desc') {
        e.stopPropagation();
        setMaximized(descSection, maxBtn, false);
      }
    }, true);

    descSection.appendChild(toolbar);
    descSection.appendChild(descPane);
    panelDesc.appendChild(descSection);
    /* Meta-Felder + Flags werden weiter unten gebaut und in panelDesc gehängt (Karte #96).
       Bis dahin: Tab-Panels erst nach modalBody-Aufbau einhängen. */
    modalBody.appendChild(panelDesc);

    /* Panel: Kommentare */
    const panelComments = el('div', { cls: 'tab-panel', id: 'tab-panel-comments' });
    panelComments.setAttribute('role', 'tabpanel');
    panelComments.setAttribute('aria-labelledby', 'tab-comments');
    panelComments.dataset.tabPanel = 'comments';

    const commentsSection = el('div', { cls: 'comments-section' });
    const commentsList    = el('div', { cls: 'comments-list', id: 'comments-list' });

    const commentForm  = el('div', { cls: 'comment-form' });
    const commentInput = el('textarea', { cls: 'comment-input', id: 'comment-input',
                                           placeholder: I18n.t('comment.placeholder') });
    const addBtn       = el('button', { cls: 'btn btn-sm btn-primary',
                                         text: I18n.t('comment.add'), type: 'button' });

    commentForm.appendChild(commentInput);
    commentForm.appendChild(addBtn);
    /* Karte #678 (ADR-0135): Dateien an den nächsten Kommentar — nur im
       Server-Build; Vorlage ist der Upload-Knopf des Anhänge-Tabs. Zweite
       Zeile des Formular-Rasters (Auswahl | Knopf), damit „Hinzufügen“ der
       erste Knopf bleibt. */
    if (IS_SERVER_BUILD) {
      const dateiInput = el('input', { type: 'file', id: 'comment-upload', cls: 'comment-upload-input' });
      dateiInput.multiple = true;
      dateiInput.style.display = 'none';
      dateiInput.addEventListener('change', () => {
        kommentarDateien.push(...(dateiInput.files || []));
        dateiInput.value = '';
        renderKommentarDateiAuswahl();
      });
      const dateiBtn = el('button', { type: 'button', cls: 'btn btn-sm comment-upload-btn',
                                      text: I18n.t('comment.dateien') });
      dateiBtn.addEventListener('click', () => dateiInput.click());
      ensureKommentarAnhangStyles();
      commentForm.appendChild(el('div', { cls: 'comment-files', id: 'comment-files' }));
      commentForm.appendChild(dateiInput);
      commentForm.appendChild(dateiBtn);
    }

    addBtn.addEventListener('click', () => addComment(commentsList, commentInput));
    commentInput.addEventListener('input', () => kommentarEingabeAnpassen(commentInput));
    commentInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        addComment(commentsList, commentInput);
      }
    });

    commentsSection.appendChild(commentsList);
    commentsSection.appendChild(commentForm);
    panelComments.appendChild(commentsSection);
    modalBody.appendChild(panelComments);

    /* Panel: Anhänge (ADR-0033) */
    const panelAttachments = el('div', { cls: 'tab-panel', id: 'tab-panel-attachments' });
    panelAttachments.setAttribute('role', 'tabpanel');
    panelAttachments.setAttribute('aria-labelledby', 'tab-attachments');
    panelAttachments.dataset.tabPanel = 'attachments';

    const attachmentsSection = el('div', { cls: 'attachments-section' });
    const attachmentsList    = el('div', { cls: 'attachments-list', id: 'attachments-list' });
    const attachmentsActions = el('div', { cls: 'attachments-actions' });
    const uploadInput = el('input', { type: 'file', id: 'attachments-upload', cls: 'attachments-upload-input' });
    uploadInput.style.display = 'none';
    uploadInput.addEventListener('change', () => {
      const files = [...(uploadInput.files || [])];
      files.forEach(f => triggerUpload(f));
      uploadInput.value = '';
    });
    const uploadBtn = el('button', { type: 'button', cls: 'btn btn-sm', text: I18n.t('attachment.upload') });
    uploadBtn.addEventListener('click', () => uploadInput.click());

    const sizeInfo = el('p', { cls: 'attachments-board-size', id: 'attachments-board-size' });

    attachmentsActions.appendChild(uploadBtn);
    attachmentsSection.appendChild(attachmentsList);
    attachmentsSection.appendChild(attachmentsActions);
    attachmentsSection.appendChild(uploadInput);
    attachmentsSection.appendChild(sizeInfo);
    panelAttachments.appendChild(attachmentsSection);
    modalBody.appendChild(panelAttachments);

    /* Panel: Agent-Kontext */
    const panelAgent = el('div', { cls: 'tab-panel', id: 'tab-panel-agent' });
    panelAgent.setAttribute('role', 'tabpanel');
    panelAgent.setAttribute('aria-labelledby', 'tab-agent');
    panelAgent.dataset.tabPanel = 'agent';

    const agentSection = el('div', { cls: 'modal-desc-section' });

    /* Toolbar für Agent-Tab inkl. eigenem Maximize-Button (Karte #111) */
    const { bar: agentToolbar, maxBtn: maxBtnAgent } = buildToolbar('agent', agentSection);

    const agentPane = el('div', { cls: 'desc-pane', id: 'agent-pane' });

    agentSection.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && maximizedPaneKey === 'agent') {
        e.stopPropagation();
        setMaximized(agentSection, maxBtnAgent, false);
      }
    }, true);

    agentSection.appendChild(agentToolbar);
    agentSection.appendChild(agentPane);
    panelAgent.appendChild(agentSection);
    modalBody.appendChild(panelAgent);

    /* Panel: Verknüpfungen (ADR-0032) */
    const panelLinks = el('div', { cls: 'tab-panel', id: 'tab-panel-links' });
    panelLinks.setAttribute('role', 'tabpanel');
    panelLinks.setAttribute('aria-labelledby', 'tab-links');
    panelLinks.dataset.tabPanel = 'links';

    const linksSection = el('div', { cls: 'links-section' });
    const linksList    = el('div', { cls: 'tab-links-list', id: 'links-list' });
    const linksAddBtn  = el('button', { cls: 'btn btn-sm', type: 'button', text: I18n.t('link.add') });
    linksAddBtn.id = 'link-add-btn';
    linksAddBtn.addEventListener('click', () => openLinkPicker());

    linksSection.appendChild(linksList);
    linksSection.appendChild(linksAddBtn);
    panelLinks.appendChild(linksSection);
    modalBody.appendChild(panelLinks);

    /* Panel: Verlauf (ADR-0037) */
    const panelHistory = el('div', { cls: 'tab-panel', id: 'tab-panel-history' });
    panelHistory.setAttribute('role', 'tabpanel');
    panelHistory.setAttribute('aria-labelledby', 'tab-history');
    panelHistory.dataset.tabPanel = 'history';

    const historySection = el('div', { cls: 'history-section' });
    const historyList    = el('div', { cls: 'history-list', id: 'history-list' });
    historySection.appendChild(historyList);
    panelHistory.appendChild(historySection);
    modalBody.appendChild(panelHistory);

    /* Panel: Finding (ADR-0133 §1) — feste Felder, Auswahllisten statt Freitext */
    const panelFinding = el('div', { cls: 'tab-panel', id: 'tab-panel-finding' });
    panelFinding.setAttribute('role', 'tabpanel');
    panelFinding.setAttribute('aria-labelledby', 'tab-finding');
    panelFinding.dataset.tabPanel = 'finding';
    [['typ', FINDING_TYPEN, v => I18n.t('finding.typ.' + v)],
     ['prioritaet', FINDING_PRIORITAETEN, v => v],
     ['status', FINDING_STATUS, v => v]].forEach(([k, liste, text]) => {
      const sel = el('select', { id: 'modal-finding-' + k });
      liste.forEach(v => sel.appendChild(el('option', { value: v, text: text(v) })));
      panelFinding.appendChild(field(I18n.t('finding.' + (k === 'prioritaet' ? 'prio' : k)), sel, 'modal-finding-' + k));
    });
    ['beleg', 'lehre'].forEach(k => {
      const ta = el('textarea', { id: 'modal-finding-' + k, rows: '3' });
      panelFinding.appendChild(field(I18n.t('finding.' + k), ta, 'modal-finding-' + k));
    });
    modalBody.appendChild(panelFinding);

    /* Panel: KI-Tab (#299/ADR-0095) — nur Server-Build (Slim-DCE). */
    if (IS_SERVER_BUILD && LLMTab) {
      modalBody.appendChild(LLMTab.build());
    }

    /* ── Meta-Felder (immer sichtbar, unterhalb der Tabs) ───────── */

    const prioritySel = el('select', { id: 'modal-priority' });
    [['niedrig', 'card.prio.low'], ['mittel', 'card.prio.medium'], ['hoch', 'card.prio.high']].forEach(([v, key]) => {
      prioritySel.appendChild(el('option', { value: v, text: I18n.t(key) }));
    });

    /* Owner-Dropdown */
    const ownerSel = el('select', { id: 'modal-owner' });
    fuelleFesteOwnerOptionen(ownerSel);

    /* Owner-Wechsel: Tab-Sichtbarkeit live aktualisieren */
    ownerSel.addEventListener('change', () => {
      updateTabVisibility(ownerSel.value);
    });

    /* #302: Assignees — Multi-Select für zusätzlich Beteiligte.
       Owner bleibt Single-String + Spawn-Quelle (ADR-0043).
       Items werden beim Open dynamisch befüllt (Identities + Claude-Modelle).
       Karte #817: Das Multi-Select ist nur noch unsichtbarer Datenträger; zu
       sehen sind Chips (zeigeAssigneeChips). Alle Lese-, Vergleichs- und
       Fremdwertpfade (F-1964) laufen unverändert über dieses Feld. */
    const assigneesSel = el('select', { id: 'modal-assignees', multiple: true, hidden: '' });
    assigneesSel.addEventListener('change', () => updateTabVisibility(ownerSel.value));

    /* Karte #817: Owner als Chip mit Stern (verantwortlich), daneben die
       Beteiligten als Chips, „+“ fügt hinzu. Zwei Felder, eine Zeile. */
    const ownerChip = el('span', { cls: 'label', title: I18n.t('card.owner') });
    ownerChip.append('★', ownerSel);
    const assigneeBox = el('div', { cls: 'card-labels', id: 'modal-assignee-chips' });
    const assigneePlus = el('select', { cls: 'chip-add', 'aria-label': I18n.t('card.assignees') });
    assigneePlus.addEventListener('change', () => {
      const o = [...assigneesSel.options].find(x => x.value === assigneePlus.value);
      if (o) { o.selected = true; meldeAenderung(assigneesSel); }
    });
    assigneeBox.append(ownerChip, assigneePlus);

    /* Karte #817: Labels als Chips. #modal-labels bleibt unsichtbarer
       Datenträger im bisherigen Komma-Format (Lesepfade, F-1964-Normalform). */
    const labelsInput = el('input', { type: 'text', id: 'modal-labels', hidden: '' });
    const labelBox = el('div', { cls: 'card-labels', id: 'modal-label-chips' });
    /* Nacharbeit #817 K-4: beschriftet über <label for> (field() unten). */
    const labelNeu = el('input', { type: 'text', cls: 'chip-add', id: 'modal-label-neu', placeholder: I18n.t('card.label-add') });
    const labelUebernehmen = () => {
      if (labelNeu.value.trim()) setzeLabels(formLabels());
      labelNeu.value = '';
    };
    labelNeu.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); labelUebernehmen(); } });
    labelNeu.addEventListener('blur', labelUebernehmen);
    labelBox.appendChild(labelNeu);

    /* Komplexität-Dropdown */
    const complexitySel = el('select', { id: 'modal-complexity' });
    [['', 'card.complexity.none'], ['einfach', 'card.complexity.simple'],
     ['mittel', 'card.complexity.medium'], ['hoch', 'card.complexity.high']].forEach(([v, key]) => {
      complexitySel.appendChild(el('option', { value: v, text: I18n.t(key) }));
    });

    /* Aufwand-Zahlenfeld */
    const effortInput = el('input', { type: 'number', id: 'modal-effort', min: '0', step: '0.5',
                                       placeholder: I18n.t('card.effort.placeholder') });
    /* Karte #774: Wer tippt, ersetzt einen fremden Altwert bewusst. */
    effortInput.addEventListener('input', () => {
      if (effortInput.dataset.altwert === undefined) return;
      delete effortInput.dataset.altwert;
      effortInput.placeholder = I18n.t('card.effort.placeholder');
      zeigeEffortHinweis(null);
    });

    /* Karte #96: Meta-Felder + Flags gehören in den Beschreibungs-Tab, nicht in jeden Tab.
       Damit sind sie nur sichtbar wenn Tab "Beschreibung" aktiv ist; Tabs Kommentare/
       Anhänge/Agent/Verknüpfungen bleiben aufgeräumt. */
    /* Karte #817: eine Zeile „Bearbeiter“ (Owner-Chip + Beteiligte). */
    const metaRow = el('div', { cls: 'modal-meta-row' });
    const bearbeiterFeld = field(I18n.t('card.bearbeiter'), assigneeBox, 'modal-owner');
    bearbeiterFeld.appendChild(assigneesSel);
    metaRow.appendChild(bearbeiterFeld);
    panelDesc.appendChild(metaRow);

    /* Karte #606 (ADR-0130 §1): Übernahme — wer hält die Karte, bis wann.
       Aufheben ist ein bewusster Eingriff in die Arbeit einer anderen Sitzung:
       nur mit Grund, der als Kommentar an der Karte stehen bleibt. Befüllt in
       open(); unsichtbar, solange die Karte frei ist. CSS inline (slim). */
    if (IS_PROJECTOR_BUILD || IS_SERVER_BUILD) {
      const claimRow = el('div', { cls: 'modal-meta-row', id: 'modal-claim-row' });
      claimRow.style.cssText = 'display:none;align-items:center;gap:8px;flex-wrap:wrap';
      const claimText = el('span', { id: 'modal-claim-text' });
      claimText.style.cssText = 'flex:1 1 100%;font-size:13px';
      const claimGrund = el('input', { type: 'text', id: 'modal-claim-grund', placeholder: I18n.t('claim.grund') });
      claimGrund.style.cssText = 'flex:1 1 12em;min-width:0';
      const claimBtn = el('button', { type: 'button', id: 'modal-claim-aufheben', cls: 'btn btn-secondary', text: I18n.t('claim.aufheben') });
      claimBtn.disabled = true;
      /* Sitzungs-QA K-6: Solange ein Aufheben läuft, bleibt der Knopf aus —
         sonst gab Weitertippen ihn frei, und ein zweiter Klick schickte einen
         zweiten Batch mit einem zweiten Grund-Kommentar. */
      let claimLaeuft = false;
      claimGrund.addEventListener('input', () => { claimBtn.disabled = claimLaeuft || !claimGrund.value.trim(); });
      claimBtn.addEventListener('click', () => {
        const grund = claimGrund.value.trim();
        const karte = currentCard;
        if (claimLaeuft || !grund || !karte?.claim) return;
        claimLaeuft = true;
        claimBtn.disabled = true;
        /* Karte #618 (F-1257): der angezeigte Halter geht als Bedingung mit —
           aufgehoben wird nur, wenn der Server genau ihn noch kennt. */
        const c = karte.claim;
        EventBus.emit('card:claim-release', {
          cardId: karte.id, halter: c.by || '?', grund,
          erwartet: { by: c.by ?? null, session: c.session ?? null, since: c.since ?? null },
          /* Erst nach bestätigtem Erfolg ausblenden (knowledge §41).
             `aktuell` (nur bei halter-geaendert): der Halter, den der Server
             jetzt kennt — ein Objekt, oder null für „keiner mehr“. */
          fertig: (ok, aktuell) => {
            claimLaeuft = false;
            if (currentCard !== karte) return;
            if (ok) { delete karte.claim; claimRow.style.display = 'none'; claimGrund.value = ''; }
            else if (aktuell !== undefined) {
              if (aktuell) karte.claim = aktuell; else delete karte.claim;
              fuelleClaimZeile(karte);
            } else claimBtn.disabled = !claimGrund.value.trim();
          },
        });
      });
      claimRow.appendChild(claimText);
      claimRow.appendChild(claimGrund);
      claimRow.appendChild(claimBtn);
      panelDesc.appendChild(claimRow);
    }

    /* #307/ADR-0102: Projekt-Dropdown — nur im Server-Build vorhanden.
       Sichtbarkeit zusätzlich vom projects_enabled-Toggle abhängig (in open()
       gesetzt). IS_SERVER_BUILD=false → kompletter Block per DCE eliminiert. */
    let projectSel = null;
    let projectRow = null;
    if (IS_SERVER_BUILD) {
      projectSel = el('select', { id: 'modal-project' });
      projectRow = el('div', { cls: 'modal-meta-row', id: 'modal-meta-row-project' });
      projectRow.appendChild(field(I18n.t('card.project'), projectSel, 'modal-project'));
      projectRow.style.display = 'none'; /* erst sichtbar, wenn Toggle + Projekte vorhanden */
      panelDesc.appendChild(projectRow);
    }

    /* Karte #817: Priorität, Komplexität, Aufwand in einer Zeile. */
    const metaRow2 = el('div', { cls: 'modal-meta-row' });
    metaRow2.appendChild(field(I18n.t('card.priority'),   prioritySel,   'modal-priority'));
    metaRow2.appendChild(field(I18n.t('card.complexity'), complexitySel, 'modal-complexity'));
    const effortFeld = field(I18n.t('card.effort'),     effortInput,   'modal-effort');
    /* F-1967: sichtbarer Hinweis auf einen fremden Altwert (Platzhalter allein
       ist kontrastarm und für Screenreader unzuverlässig). */
    const effortHinweis = el('div', { id: 'modal-effort-hint' });
    effortHinweis.hidden = true;
    effortHinweis.style.cssText = 'font-size:.8em;opacity:.85;margin-top:2px';
    effortFeld.appendChild(effortHinweis);
    metaRow2.appendChild(effortFeld);
    panelDesc.appendChild(metaRow2);

    /* ADR-0034: Datumsfelder */
    const dueDateInput   = el('input', { type: 'date', id: 'modal-due-date' });
    const startDateInput = el('input', { type: 'date', id: 'modal-start-date' });
    /* ADR-0138 §1: je Datum eine optionale Uhrzeit, nur bei gesetztem Datum
       bedienbar (eine Uhrzeit ohne Datum verwirft der Ladepfad ohnehin). */
    /* F-1892 (Sprint-86-Kritiker K-B6a): Beide Felder hießen nur „Uhrzeit“ —
       ein Screenreader unterschied Frist und Start nicht. */
    const dueTimeInput   = el('input', { type: 'time', id: 'modal-due-time',   'aria-label': `${I18n.t('card.due-date')}: ${I18n.t('card.time')}` });
    const startTimeInput = el('input', { type: 'time', id: 'modal-start-time', 'aria-label': `${I18n.t('card.start-date')}: ${I18n.t('card.time')}` });
    const datumMitZeit = (d, t) => {
      const w = el('div', { cls: 'modal-date-time' });
      w.appendChild(d); w.appendChild(t);
      d.addEventListener('input', () => { t.disabled = !d.value; });
      return w;
    };
    const metaRow3 = el('div', { cls: 'modal-meta-row', id: 'modal-meta-row-dates' });
    metaRow3.appendChild(field(I18n.t('card.due-date'),   datumMitZeit(dueDateInput,   dueTimeInput),   'modal-due-date'));
    metaRow3.appendChild(field(I18n.t('card.start-date'), datumMitZeit(startDateInput, startTimeInput), 'modal-start-date'));

    /* Karte #817: Termine (Datum, Uhrzeit, Wiederholung) in einer
       einklappbaren Sektion. Eingeklappt heißt nur verborgen — die Felder
       bleiben im DOM. Der Kopf fasst zusammen, was gesetzt ist. */
    const termine = el('details', { cls: 'modal-termine', id: 'modal-termine' });
    const termineKopf = el('summary');
    /* Aufklappen merken, pro Gerät (UI-Zustand, kein Board-Feld). Der
       Klick feuert vor dem Umschalten: der neue Zustand ist !open. */
    termineKopf.addEventListener('click', () => { try { localStorage.setItem(TERMINE_KEY, termine.open ? '0' : '1'); } catch {} });
    termine.addEventListener('change', zeigeTermine);
    termine.append(termineKopf, metaRow3);
    panelDesc.appendChild(termine);

    /* ADR-0041: Recurrence-Editor */
    const recurrenceRow = el('div', { cls: 'modal-meta-row modal-recurrence-row', id: 'modal-recurrence-row' });
    const recurrenceSel = el('select', { id: 'modal-recurrence-interval' });
    [['', 'recurrence.off'], ['daily', 'recurrence.daily'],
     ['weekly', 'recurrence.weekly'], ['monthly', 'recurrence.monthly']].forEach(([v, key]) => {
      recurrenceSel.appendChild(el('option', { value: v, text: I18n.t(key) }));
    });
    recurrenceRow.appendChild(field(I18n.t('recurrence.title'), recurrenceSel, 'modal-recurrence-interval'));

    /* Optionales Tages-Feld — Sichtbarkeit abhängig vom gewählten Intervall */
    const recurrenceDayWrap = el('div', { cls: 'modal-field', id: 'modal-recurrence-day-wrap' });
    recurrenceDayWrap.style.display = 'none';
    const recurrenceDayInput = el('input', { type: 'number', id: 'modal-recurrence-day', min: '0', max: '31', step: '1' });
    const recurrenceDayLabel = el('label', { text: I18n.t('recurrence.day-of-week'), for: 'modal-recurrence-day' });
    recurrenceDayWrap.appendChild(recurrenceDayLabel);
    recurrenceDayWrap.appendChild(recurrenceDayInput);
    recurrenceRow.appendChild(recurrenceDayWrap);

    function updateRecurrenceDayVisibility() {
      const interval = recurrenceSel.value;
      recurrenceDayWrap.style.display = (interval === 'weekly' || interval === 'monthly') ? '' : 'none';
      recurrenceDayLabel.textContent = interval === 'monthly'
        ? I18n.t('recurrence.day-of-month')
        : I18n.t('recurrence.day-of-week');
      /* max-Anpassung: weekly = 0-6, monthly = 1-31 */
      recurrenceDayInput.min  = interval === 'monthly' ? '1' : '0';
      recurrenceDayInput.max  = interval === 'monthly' ? '31' : '6';
    }
    recurrenceSel.addEventListener('change', updateRecurrenceDayVisibility);
    termine.appendChild(recurrenceRow);

    const labelsFeld = field(I18n.t('card.labels'), labelBox, 'modal-label-neu');
    labelsFeld.appendChild(labelsInput);
    panelDesc.appendChild(labelsFeld);

    /* Flags: QA · Kritisch · Blockiert — Karte #817: als Umschalt-Pillen
       (CSS .flag-row) oben im Details-Tab, vor der Beschreibung. */
    const flagRow = el('div', { cls: 'flag-row' });
    function addFlag(id, key) {
      const cb  = el('input', { type: 'checkbox', id, cls: 'qa-checkbox' });
      const lbl = el('label', { cls: 'label', text: I18n.t(key), for: id });
      flagRow.appendChild(cb);
      flagRow.appendChild(lbl);
    }
    addFlag('modal-qa',       'card.qa');
    addFlag('modal-critical', 'card.critical');
    addFlag('modal-blocked',  'card.blocked');
    if (IS_SERVER_BUILD) {
      /* ADR-0140 §10 (Karte #778): Schalter „vertraulich“ — der Titel geht
         dann nicht als Schatten nach außen. Sichtbar nur auf der Seite
         „innen“ eines Board-Paars (oder wenn die Karte ihn schon trägt,
         damit ein Mensch ihn wieder abschalten kann); open() entscheidet. */
      addFlag('modal-confidential', 'card.confidential');
      flagRow.querySelector('#modal-confidential').title = I18n.t('card.confidential.hint');
      flagRow.querySelector('label[for="modal-confidential"]').title = I18n.t('card.confidential.hint');
      /* Karte #798: „Nach innen zurückholen“ — für Karten, die ein Agent innen
         offen angelegt hat (Marke angelegtVonAgent). Ein Klick: Schalter an,
         über den normalen Speicherweg sichern (Konfliktschutz, Diff → card.field).
         Sichtbar nur zusammen mit dem Schalter (zeigeVertraulichSchalter). */
      const zurueck = el('button', { cls: 'btn btn-sm', type: 'button', id: 'modal-zurueckholen', text: I18n.t('card.zurueckholen') });
      zurueck.title = I18n.t('card.zurueckholen.hint');
      zurueck.hidden = true;
      zurueck.addEventListener('click', () => {
        const cb = overlay?.querySelector('#modal-confidential');
        if (!cb || cb.hidden) return;
        cb.checked = true;
        saveAndClose();
      });
      flagRow.appendChild(zurueck);
    }
    panelDesc.prepend(flagRow);

    modal.appendChild(modalBody);

    /* Aktionen — immer unten sichtbar; Papierkorb links (#82), Cancel/Save rechts */
    const actions   = el('div', { cls: 'modal-actions' });
    /* Papierkorb-Slot links + Archivieren-Button (Karte #60) */
    const actionsLeft = el('div', { cls: 'modal-actions-left' });
    actionsLeft.appendChild(deleteBtn);

    /* Karte #60: Archivieren-Button — nur in col-erledigt sichtbar */
    const archiveBtn = el('button', { cls: 'btn btn-sm btn-archive', type: 'button' });
    archiveBtn.id = 'modal-archive-btn';
    actionsLeft.appendChild(archiveBtn);

    /* ADR-0040: „Als Template speichern"-Button (Karte #65) */
    const saveAsTplBtn = el('button', { cls: 'btn btn-sm', type: 'button',
                                         text: I18n.t('template.save-as'), id: 'modal-save-as-template-btn' });
    saveAsTplBtn.title = I18n.t('template.save-as');
    actionsLeft.appendChild(saveAsTplBtn);

    /* #294/ADR-0092: Spam-Button (nur kind:service-Board + Server-Build).
       Toggle „🚫 Als Spam markieren" ↔ „Kein Spam" je nach Karten-Spalte. */
    let spamBtn = null;
    if (IS_SERVER_BUILD) {
      spamBtn = el('button', { cls: 'btn btn-sm', type: 'button', id: 'modal-spam-btn' });
      spamBtn.style.display = 'none';
      actionsLeft.appendChild(spamBtn);
    }

    /* #296: Cross-Board-Transfer-Button (nur Server-Build + Server-Modus +
       Nicht-Meta-Karte). Klick öffnet Picker-Overlay über dem Modal. */
    let transferBtn = null;
    if (IS_SERVER_BUILD) {
      transferBtn = el('button', { cls: 'btn btn-sm', type: 'button',
                                    text: I18n.t('card.transfer.action'),
                                    id: 'modal-transfer-btn' });
      transferBtn.style.display = 'none';
      actionsLeft.appendChild(transferBtn);
    }

    /* #672/ADR-0133 §7: „In Open WebUI weiterbearbeiten“ — nur Server-Build;
       sichtbar erst, wenn /me eine ABOARD_OWUI_URL meldet (updateOwuiBtnVisibility). */
    let owuiBtn = null;
    if (IS_SERVER_BUILD) {
      owuiBtn = el('button', { cls: 'btn btn-sm', type: 'button',
                               text: I18n.t('card.owui.action'),
                               id: 'modal-owui-btn' });
      owuiBtn.title = I18n.t('card.owui.title');
      owuiBtn.style.display = 'none';
      actionsLeft.appendChild(owuiBtn);
    }

    /* Karte #685 (ADR-0134 §3): „Extern bearbeiten“ — nur Server-Build;
       sichtbar erst, wenn die Schleuse an ist und die Person einen nutzbaren
       eigenen Anbieter oder schon einen Entwurf hat (SchleuseAuszug). */
    let schleuseBtn = null;
    if (IS_SERVER_BUILD && SchleuseAuszug) {
      schleuseBtn = el('button', { cls: 'btn btn-sm', type: 'button', id: 'modal-schleuse-btn' });
      schleuseBtn.style.display = 'none';
      actionsLeft.appendChild(schleuseBtn);
    }

    /* Karte #292: Export-Submenü (Drucken / Markdown / E-Mail) */
    const exportWrap = el('div', { cls: 'modal-export-wrap' });
    const exportBtn  = el('button', { cls: 'btn btn-sm', type: 'button',
                                       text: I18n.t('card.export.menu'), id: 'modal-export-btn' });
    exportBtn.title = I18n.t('card.export.menu');
    const exportMenu = el('div', { cls: 'modal-export-menu' });
    exportMenu.style.display = 'none';
    const btnPrint = el('button', { cls: 'btn btn-sm', type: 'button',
                                     text: I18n.t('card.export.print'), id: 'modal-export-print' });
    const btnMd    = el('button', { cls: 'btn btn-sm', type: 'button',
                                     text: I18n.t('card.export.md'), id: 'modal-export-md' });
    const btnEmail = el('button', { cls: 'btn btn-sm', type: 'button',
                                     text: I18n.t('card.export.email'), id: 'modal-export-email' });
    exportMenu.appendChild(btnPrint);
    exportMenu.appendChild(btnMd);
    exportMenu.appendChild(btnEmail);
    exportWrap.appendChild(exportBtn);
    exportWrap.appendChild(exportMenu);
    actionsLeft.appendChild(exportWrap);

    const right     = el('div', { cls: 'modal-actions-right' });
    const cancelBtn = el('button', { cls: 'btn btn-modal-cancel', text: I18n.t('btn.cancel'), type: 'button' });
    const saveBtn   = el('button', { cls: 'btn btn-primary', text: I18n.t('btn.save-card'), type: 'button' });
    right.appendChild(cancelBtn);
    right.appendChild(saveBtn);
    actions.appendChild(actionsLeft);
    actions.appendChild(right);
    modal.appendChild(actions);

    ov.appendChild(modal);

    /* Focus-Trap */
    modal.addEventListener('keydown', (e) => {
      if (e.key !== 'Tab') return;
      const focusable = [...modal.querySelectorAll('input,textarea,select,button')].filter(f => !f.disabled && f.offsetParent !== null);
      const first = focusable[0];
      const last  = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    });

    /* Karte #463: Backdrop-Klick ist ein AMBIGER Schliessweg (Fehlklick
       genauso denkbar wie gewolltes Schliessen) — deshalb Auto-Save statt
       stillem Verwerfen, siehe closeWithAutosave(). */
    ov.addEventListener('click', (e) => { if (e.target === ov) closeWithAutosave(); });
    titleInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter')  { e.preventDefault(); saveAndClose(); }
      /* Karte #463: ESC im Titelfeld ist derselbe ambige Fall wie Backdrop/
         X-Knopf — Auto-Save statt Verwerfen. */
      if (e.key === 'Escape') closeWithAutosave();
    });
    saveBtn.addEventListener('click',   saveAndClose);
    /* Karte #463: "Abbrechen" bleibt bewusst die einzige EXPLIZITE
       Verwerfen-Aktion im Modal — reines close() ohne Speichern. Würde auch
       dieser Button auto-speichern, gäbe es im ganzen Modal keinen Weg mehr,
       eine Bearbeitung bewusst zu verwerfen. Siehe closeWithAutosave(). */
    cancelBtn.addEventListener('click', close);
    /* Karte #463: X-Knopf ist wie Backdrop/ESC ein ambiger Schliessweg. */
    closeBtn.addEventListener('click',  closeWithAutosave);
    deleteBtn.addEventListener('click', deleteAndClose);
    /* Karte #60: Archivieren/Aus Archiv holen */
    archiveBtn.addEventListener('click', archiveAndClose);
    /* #294/ADR-0092: Spam-Button (nur Server-Build) */
    if (IS_SERVER_BUILD && spamBtn) spamBtn.addEventListener('click', toggleSpamAndClose);
    /* #296: Transfer-Button (nur Server-Build) */
    if (IS_SERVER_BUILD && transferBtn) transferBtn.addEventListener('click', openTransferPicker);
    /* #672: Open-WebUI-Knopf (nur Server-Build) */
    if (IS_SERVER_BUILD && owuiBtn) owuiBtn.addEventListener('click', openInOpenWebUI);
    /* #685: „Extern bearbeiten“ (nur Server-Build) */
    if (IS_SERVER_BUILD && schleuseBtn) schleuseBtn.addEventListener('click', openSchleuseAuszug);
    /* ADR-0040: Als Template speichern (Karte #65) */
    saveAsTplBtn.addEventListener('click', saveAsTemplate);

    /* Karte #292: Export-Submenü-Handler */
    function closeExportMenu() { exportMenu.style.display = 'none'; }
    function toggleExportMenu() {
      exportMenu.style.display = (exportMenu.style.display === 'none') ? 'flex' : 'none';
    }
    exportBtn.addEventListener('click', (e) => { e.stopPropagation(); toggleExportMenu(); });
    /* Klick außerhalb des Menüs → schließen */
    modal.addEventListener('click', (e) => {
      if (!exportWrap.contains(e.target)) closeExportMenu();
    });
    /* WICHTIG: Submenü-Buttons synchron im Click-Pfad rufen — sonst blockiert
       der Browser window.open für printCard. */
    btnPrint.addEventListener('click', () => {
      closeExportMenu();
      if (!currentCard) return;
      const ok = Export.printCard(currentCard, State.get()?.board ?? null);
      if (!ok) {
        try { alert(I18n.t('card.export.popup-blocked')); } catch {}
      }
    });
    btnMd.addEventListener('click', () => {
      closeExportMenu();
      if (!currentCard) return;
      Export.downloadCardMarkdown(currentCard, State.get()?.board ?? null);
    });
    btnEmail.addEventListener('click', () => {
      closeExportMenu();
      if (!currentCard) return;
      /* #293/ADR-0091: Server-Modus + IS_SERVER_BUILD → eigener Dialog mit
         SMTP-Versand; sonst Fallback auf mailto: (Karte #292). */
      if (IS_SERVER_BUILD && Profile.has('feature.restStorage') && EmailDialog) {
        const board = State.get()?.board ?? null;
        const sendFn = (cardId, payload) => StoragePlugin.getActivePlugin().sendCardEmail(cardId, payload);
        EmailDialog.open(currentCard, board, sendFn);
        return;
      }
      Export.mailtoCard(currentCard, State.get()?.board ?? null);
    });

    /* ── Clipboard-Paste (Karte #50, ADR-0033) ────────────────────── */
    /* Nur aktiv wenn eine Typora-Textarea fokussiert ist (Edit-Mode).
       Sonst: Standardverhalten (Strg-V in Textfeldern, Selects usw.). */
    modal.addEventListener('paste', async (e) => {
      const ta = findActiveTextarea();
      if (!ta) return; /* kein Edit-Mode aktiv → normales Paste */

      const items = e.clipboardData?.items;
      if (!items) return;

      const imageItems = [...items].filter(i => i.kind === 'file' && i.type.startsWith('image/'));
      if (imageItems.length === 0) return; /* nur Text im Clipboard → unverändert */

      e.preventDefault(); /* Bild-Bytes nicht als Text einfügen */

      for (const item of imageItems) {
        const file = item.getAsFile();
        if (!file) continue;

        /* Upload-Pfad — wie Manual-Upload.
           F-829: `triggerUpload` liefert das TATSAECHLICH erzeugte Attachment
           zurück (oder `null` bei Ablehnung/Fehler). Vorher griff dieser Code
           ungeprüft "das letzte Attachment der Liste" — lehnte `checkUpload`
           den Upload ab (Pro-Datei-/Hartlimit), legte `uploadOne` gar kein
           Attachment an, und der Code schrieb stattdessen den Verweis auf den
           zuletzt vorhandenen, UNBETEILIGTEN Anhang in den Text. */
        const att = await triggerUpload(file);
        if (!att) continue; /* Upload abgelehnt oder fehlgeschlagen — kein Verweis einfügen */

        const insert = `![${att.filename}](attachment://${att.id})`;
        const s = ta.selectionStart ?? ta.value.length;
        const before = ta.value.slice(0, s);
        const after  = ta.value.slice(ta.selectionEnd ?? s);
        ta.value = before + insert + after;
        ta.setSelectionRange(s + insert.length, s + insert.length);
        ta.focus();
        /* Textarea-Höhe anpassen (Typora auto-resize) */
        ta.dispatchEvent(new Event('input'));
      }
    });

    /* ── Drag & Drop (Karte #50, ADR-0033) ───────────────────────── */
    /* Drop-Zone: gesamtes .modal-Element.
       Overlay wird nur bei Files-Drag angezeigt; normaler Text-Drag bleibt unberührt. */
    let dropOverlay = null;
    let dragCounter = 0; /* Zähler für verschachtelte dragenter/dragleave */

    function getOrCreateDropOverlay() {
      if (!dropOverlay) {
        dropOverlay = el('div', { cls: 'attachment-drop-overlay',
                                   text: I18n.t('attachment.drop-overlay') });
        /* F-136/F-2876: Screen-Reader-Hinweis bei Drag-Aktivität über die
           dauerhafte Live-Region (showDropOverlay); das Overlay selbst ist
           nur Optik. */
        dropOverlay.setAttribute('aria-hidden', 'true');
        modal.appendChild(dropOverlay);
      }
      return dropOverlay;
    }

    function showDropOverlay() {
      const o = getOrCreateDropOverlay();
      if (!o.dataset.angesagt) ansagen(I18n.t('attachment.drop-overlay'));
      o.dataset.angesagt = '1';
      o.style.display = '';
    }
    function hideDropOverlay() { if (dropOverlay) { dropOverlay.style.display = 'none'; delete dropOverlay.dataset.angesagt; } }

    modal.addEventListener('dragenter', (e) => {
      if (![...(e.dataTransfer?.types ?? [])].includes('Files')) return;
      if (nurLesen || rueckwegSperre) return; /* Karte #826/#827: kein Ablege-Hinweis auf gebundenen Karten */
      e.preventDefault();
      dragCounter++;
      showDropOverlay();
    });

    modal.addEventListener('dragover', (e) => {
      if (![...(e.dataTransfer?.types ?? [])].includes('Files')) return;
      e.preventDefault(); /* notwendig damit drop-Event feuert */
      e.dataTransfer.dropEffect = 'copy';
    });

    modal.addEventListener('dragleave', (e) => {
      if (![...(e.dataTransfer?.types ?? [])].includes('Files')) return;
      dragCounter--;
      if (dragCounter <= 0) {
        dragCounter = 0;
        hideDropOverlay();
      }
    });

    modal.addEventListener('drop', async (e) => {
      e.preventDefault();
      dragCounter = 0;
      hideDropOverlay();

      const files = [...(e.dataTransfer?.files ?? [])];
      for (const file of files) {
        await triggerUpload(file);
      }
    });

    return ov;
  }

  /* ── Öffnen / Schließen ────────────────────────────────────────── */

  /* Feste Owner-Optionen (Solo-/Projekt-Betrieb, ADR-0043). Eigene Funktion,
     weil fuelleBearbeiter() das Select im Server-Modus leert: open() baut sie
     beim Wechsel zurück in den Nicht-Server-Pfad wieder auf (KO-6). */
  function fuelleFesteOwnerOptionen(ownerSel) {
    ownerSel.innerHTML = '';
    delete ownerSel.dataset.bearbeiterServer;
    ownerSel.appendChild(el('option', { value: '', text: I18n.t('card.owner.unassigned') }));
    const userOpt = el('option', { value: Config.get('userName') || 'User',
                                    text:  Config.get('userName') || 'User' });
    userOpt.dataset.user = '1'; /* Marker zum dynamischen Update in open() */
    ownerSel.appendChild(userOpt);
    /* Karte #817: dieselbe Liste wie die Assignee-Kandidaten (eine Quelle). */
    CLAUDE_MODELLE.forEach(v => {
      ownerSel.appendChild(el('option', { value: v, text: v === 'Claude' ? I18n.t('card.owner.claude-any') : v }));
    });
  }

  let prevFocus = null;

  /* Karte #698: Owner-Dropdown und Assignee-Auswahl im Server-Modus.
     `namen` = Personen des Boards (bearbeiter-kandidaten.js). Werte, die schon
     auf der Karte stehen (Claude-Modell, „User“, external:<mail>, ehemalige
     Person), bleiben als Option erhalten — sonst verlöre das Speichern sie
     still. `behalteAuswahl`: beim nachträglichen Einspielen gilt die aktuelle
     Auswahl im DOM (Nutzer kann schon geklickt haben), nicht der Kartenstand. */
  let bearbeiterGen = 0;
  let projektGen = 0;
  /** Q-3: Wert eines Formularfelds als Vergleichstext (Mehrfachauswahl: alle gewählten). */
  function feldAnzeigeWert(f) {
    return f.multiple ? JSON.stringify([...f.options].filter(o => o.selected).map(o => o.value).sort()) : String(f.value);
  }

  /* ── Karte #817: Chips und Termine-Kopf ─────────────────────────
     Die Chips sind nur eine Ansicht. Datenträger bleiben #modal-labels
     (Komma-Format) und #modal-assignees (Mehrfachauswahl); Zeichnen fasst
     sie nie an, nur eine Bedienung schreibt — und meldet das wie eine
     Eingabe, damit der „berührt“-Merker (Q-3) greift. */
  const TERMINE_KEY = 'aboard.modal.termine';
  const meldeAenderung = f => f.dispatchEvent(new Event('change', { bubbles: true }));
  const leseLabels = () => (overlay?.querySelector('#modal-labels')?.value || '').split(',').map(s => s.trim()).filter(Boolean);

  const normLabels = liste => [...new Set(liste.map(s => s.trim()).filter(Boolean))];

  /* Nacharbeit #817 K-1: Was im Label-Eingabefeld steht, aber noch nicht per
     Enter/Verlassen übernommen ist, zählt mit — sonst sähen isDirty() und
     jedes Speichern (ESC, X, Knopf, Vorlage) es nicht, und ESC verlöre den
     halb getippten Text (das alte Komma-Textfeld speicherte ihn). Nur lesend:
     das Eingabefeld und der Datenträger bleiben unangetastet. */
  const formLabels = () => {
    const neu = overlay?.querySelector('#modal-label-neu')?.value || '';
    return neu.trim() ? normLabels([...leseLabels(), ...neu.split(',')]) : leseLabels();
  };

  function setzeLabels(liste) {
    const f = overlay.querySelector('#modal-labels');
    f.value = normLabels(liste).join(', ');
    meldeAenderung(f);
    zeigeLabelChips();
  }

  /** Chips vor das letzte Kind der Box (das „+“-Bedienelement) zeichnen. */
  function zeichneChips(box, texte, weg, farbig) {
    box.querySelectorAll('.chip').forEach(c => c.remove());
    texte.forEach((t, i) => {
      const c = el('span', { cls: 'label chip', text: t });
      if (farbig) faerbeLabel(c, t); /* Karte #971: Label-Chips wie auf der Karte */
      const x = el('button', { type: 'button', cls: 'chip-x', text: '×', 'aria-label': `${I18n.t('config.background-remove')}: ${t}` });
      /* Nacharbeit #817 K-3: Neuzeichnen entfernt den geklickten Knopf — der
         Fokus fiele auf BODY. Er geht an den Nachbar-Chip, sonst ans „+“. */
      x.addEventListener('click', () => {
        weg(i);
        const xs = box.querySelectorAll('.chip-x');
        (xs[Math.min(i, xs.length - 1)] || box.lastChild).focus();
      });
      c.appendChild(x);
      box.insertBefore(c, box.lastChild);
    });
    sperreNachNeuaufbau();
  }

  function zeigeLabelChips() {
    const liste = leseLabels();
    zeichneChips(overlay.querySelector('#modal-label-chips'), liste, i => setzeLabels(liste.filter((_, j) => j !== i)), true);
  }

  function zeigeAssigneeChips() {
    const sel = overlay.querySelector('#modal-assignees');
    const box = overlay.querySelector('#modal-assignee-chips');
    const alle = [...sel.options];
    const an = alle.filter(o => o.selected);
    zeichneChips(box, an.map(o => o.text), i => { an[i].selected = false; meldeAenderung(sel); });
    const plus = box.lastChild;
    plus.innerHTML = '<option value="">+';
    alle.forEach(o => { if (!o.selected) plus.appendChild(el('option', { value: o.value, text: o.text })); });
  }

  /** Kopf der Termine-Sektion: „Termine · Fällig am 14.10. 09:00 · Wöchentlich“.
      Rückgabe: Anzahl gesetzter Angaben. */
  function zeigeTermine() {
    const wert = id => overlay.querySelector('#modal-' + id).value;
    /* Datum als „14.10.“ — ohne Date-Objekt, also ohne Zeitzonen-Verschiebung. */
    const teile = ['due', 'start'].filter(k => wert(k + '-date')).map(k =>
      `${I18n.t(`card.${k}-date`)} ${wert(k + '-date').slice(5).split('-').reverse().join('.')}. ${wert(k + '-time')}`.trim());
    const r = overlay.querySelector('#modal-recurrence-interval');
    if (r.value) teile.push(r.selectedOptions[0].text);
    overlay.querySelector('#modal-termine > summary').textContent = [I18n.t('card.termine'), ...teile].join(' · ');
    return teile.length;
  }

  function fuelleBearbeiter(card, namen, behalteAuswahl = false) {
    const ownerSel = overlay?.querySelector('#modal-owner');
    const assigneesSel = overlay?.querySelector('#modal-assignees');
    const kartenAssignees = Array.isArray(card.assignees) ? card.assignees : [];
    const ownerWert = behalteAuswahl && ownerSel ? ownerSel.value : (card.owner || '');
    const gewaehlt = behalteAuswahl && assigneesSel
      ? [...assigneesSel.selectedOptions].map(o => o.value)
      : kartenAssignees;
    /* KO-1: Claude-Modelle als eigene Gruppe NACH den Personen — nur, wenn das
       Board sie schon benutzt. Kartenwerte, die in keiner Gruppe stehen
       (ehemalige Person, „User“, external:<mail>), hängen ungruppiert an. */
    const claude = [...claudeGruppe(State.get()?.board)];
    /* VO-10: Wie im Bulk-Menü gehört jeder Kartenwert mit Claude-Kennung
       („Claude Opus 5 (1M context)“) in die Claude-Gruppe, nicht ungruppiert. */
    [card.owner, ownerWert, ...kartenAssignees, ...gewaehlt].forEach(w => {
      if (w && istClaudeKennung(w) && !claude.includes(w)) claude.push(w);
    });
    const gruppiert = (w) => namen.includes(w) || claude.includes(w);
    const gruppeEinhaengen = (sel, werteClaude, markiert) => {
      if (!werteClaude.length) return;
      const g = document.createElement('optgroup');
      g.label = I18n.t('card.owner.gruppe-claude');
      werteClaude.forEach(w => {
        const opt = el('option', { value: w, text: w });
        if (markiert?.(w)) opt.selected = true;
        g.appendChild(opt);
      });
      sel.appendChild(g);
    };
    if (ownerSel) {
      const extra = [];
      [card.owner, ownerWert].forEach(w => { if (w && !gruppiert(w) && !extra.includes(w)) extra.push(w); });
      ownerSel.innerHTML = '';
      ownerSel.dataset.bearbeiterServer = '1';
      ownerSel.appendChild(el('option', { value: '', text: I18n.t('card.owner.unassigned') }));
      namen.forEach(w => ownerSel.appendChild(el('option', { value: w, text: w })));
      gruppeEinhaengen(ownerSel, claude.filter(w => !namen.includes(w)));
      extra.forEach(w => ownerSel.appendChild(el('option', { value: w, text: w })));
      ownerSel.value = ownerWert;
    }
    if (assigneesSel) {
      const extra = [];
      [...kartenAssignees, ...gewaehlt].forEach(w => { if (w && !gruppiert(w) && !extra.includes(w)) extra.push(w); });
      assigneesSel.innerHTML = '';
      namen.forEach(w => {
        const opt = el('option', { value: w, text: w });
        if (gewaehlt.includes(w)) opt.selected = true;
        assigneesSel.appendChild(opt);
      });
      gruppeEinhaengen(assigneesSel, claude.filter(w => !namen.includes(w)), w => gewaehlt.includes(w));
      extra.forEach(w => {
        const opt = el('option', { value: w, text: w });
        if (gewaehlt.includes(w)) opt.selected = true;
        assigneesSel.appendChild(opt);
      });
    }
    if (ownerSel) updateTabVisibility(ownerSel.value);
  }

  function open(card) {
    /* ADR-0032: links als Array sicherstellen (Migration für ältere Karten) */
    if (!Array.isArray(card.links)) card.links = [];
    /* ADR-0033: attachments als Array sicherstellen */
    if (!Array.isArray(card.attachments)) card.attachments = [];
    /* ADR-0037: activity als Array sicherstellen */
    if (!Array.isArray(card.activity)) card.activity = [];
    currentCard = card;
    setPane('desc',  card.description  || '');
    setPane('agent', card.agentContext || '');
    /* ADR-0076: Beschreibung öffnet immer im Lese-Modus. */
    descEditActive = false;

    /* Karte #826: Sperren des Lesemodus der vorigen Karte lösen, BEVOR die
       übrigen Modi dieses open() ihre eigenen Sperren setzen — und den Modus
       gleich hier neu bestimmen, nicht erst weiter unten: wirft open()
       dazwischen, bliebe sonst der Wert der vorigen Karte stehen (Kritik K-6). */
    const modus = IS_SERVER_BUILD ? spiegelModus(card) : null;
    nurLesen = modus === 'lesen';
    rueckwegSperre = modus === 'rueckweg';
    if (IS_SERVER_BUILD) setzeNurLesen(false);

    if (!overlay) {
      overlay = buildOverlay();
      /* F-1964/M-4: Wer ein Feld bedient, hat es berührt — dann gilt seine Eingabe
         auch, wenn sie der Formular-Normalform des Altwerts gleicht. */
      /* Q-3: Text-, Label- und Assignee-Felder gelten nur als berührt, wenn der
         Formularwert vom beim Öffnen ANGEZEIGTEN Wert abweicht (Tippen und
         Zurücksetzen ist Nettoänderung null). Datums-/Uhrzeitfelder behalten den
         Ereignis-Merker: ein Fremddatum wird als '' angezeigt und wäre sonst
         nicht entfernbar. */
      const merkeBeruehrt = e => {
        const t = e.target;
        if (!t?.dataset || !t.id?.startsWith('modal-')) return;
        if (t.dataset.angezeigt !== undefined) {
          if (feldAnzeigeWert(t) !== t.dataset.angezeigt) t.dataset.beruehrt = '1'; else delete t.dataset.beruehrt;
        } else t.dataset.beruehrt = '1';
      };
      overlay.addEventListener('input', merkeBeruehrt);
      overlay.addEventListener('change', merkeBeruehrt);
      document.addEventListener('keydown', onGlobalKey);
      /* Karte #463: beforeunload ist kein Auto-Save-Kandidat — beim Tab-
         Schliessen kann nicht mehr asynchron gespeichert werden. Dort bleibt
         die Browser-eigene Warnung der richtige Mechanismus, siehe
         onBeforeUnload(). Registrierung analog zu onGlobalKey: einmalig,
         beim ersten Öffnen des Overlays. */
      window.addEventListener('beforeunload', onBeforeUnload);
    }

    /* Bei JEDEM Öffnen ans DOM-Ende hängen, nicht nur beim ersten
       (Kritiker-Befund Sprint 63 — die andere Hälfte von F-914):
       `appendChild` verschiebt einen bereits eingehängten Knoten, erzeugt also
       kein Duplikat. Die Modal-Shell tut das seit Karte #465; hier fehlte es,
       und die Invariante „DOM-Reihenfolge = aktuelle Öffnungsreihenfolge" galt
       damit nur zur Hälfte — sie entscheidet aber bei gleichem z-index (100
       für beide) darüber, wer visuell oben liegt UND wer ESC bekommt.
       Der reparierte Fall: Ein Shell-Dialog wird über die Sidebar geöffnet,
       DANN das Karten-Modal zum ersten Mal (per `j`/`Enter` — Shell-Dialoge
       setzen keinen Shortcut-Kontext, die Tastatur erreicht das Board also
       weiterhin). Das Karten-Modal stand dann HINTER dem Shell-Overlay im DOM;
       der Stapel-Guard der Shell trat zurück („ich bin nicht oberstes") und
       der Existenz-Guard hier ebenfalls („ein anderes Overlay ist offen") —
       ESC schloss gar nichts. */
    document.body.appendChild(overlay);

    const cardIdLink = overlay.querySelector('#modal-card-id');
    if (cardIdLink) {
      if (card.displayId != null) {
        cardIdLink.textContent = `#${card.displayId}`;
        cardIdLink.setAttribute('href', `#card-${card.displayId}`);
        cardIdLink.style.display = '';
        try { history.replaceState(null, '', `#card-${card.displayId}`); } catch {}
      } else {
        cardIdLink.style.display = 'none';
      }
    }

    for (const id of Object.values(BERUEHRT_FELDER)) delete overlay.querySelector(id)?.dataset.beruehrt;
    overlay.querySelector('#modal-title').value      = card.title;
    /* Verify V-2 (F-1947): wie complexity — „normal“, „P1“, „high“ aus Konsumenten-Boards. */
    /* Kritiker Sprint 88 (F-1963): `''` zeigt „mittel“ (vergleichsWert hält es beim
       Schliessen als unverändert), 0/false sind Fremdwerte — kein `||`. */
    setzeMitFremdwert(overlay.querySelector('#modal-priority'), card.priority == null || card.priority === '' ? 'mittel' : card.priority, BEKANNTE_PRIORITAET);
    overlay.querySelector('#modal-labels').value     = (Array.isArray(card.labels) ? card.labels : []).join(', ');
    /* Nacharbeit #817 K-2: kein Resttext der vorigen Karte (Blur → Label). */
    overlay.querySelector('#modal-label-neu').value = '';
    zeigeLabelChips();
    fuelleSchaetzfelder(card);
    /* ADR-0034: Datumsfelder befüllen (null → leerer String für date-Input) */
    overlay.querySelector('#modal-due-date').value   = card.dueDate   || '';
    overlay.querySelector('#modal-start-date').value = card.startDate || '';
    /* ADR-0138 §1: Uhrzeiten; ohne Datum gesperrt */
    for (const [z, d, wert] of [['#modal-due-time', card.dueDate, card.dueTime], ['#modal-start-time', card.startDate, card.startTime]]) {
      const t = overlay.querySelector(z);
      t.value = wert || '';
      t.disabled = !d;
    }

    /* ADR-0041: Recurrence-Editor befüllen */
    const recurrIntervalEl = overlay.querySelector('#modal-recurrence-interval');
    const recurrDayEl      = overlay.querySelector('#modal-recurrence-day');
    if (recurrIntervalEl) {
      const r = card.recurrence;
      setzeMitFremdwert(recurrIntervalEl, r?.interval ?? '', BEKANNTE_INTERVALLE);
      if (recurrDayEl) recurrDayEl.value = r?.day != null ? r.day : '';
      /* Tages-Feld-Sichtbarkeit initial setzen — Karte #817: über den
         change-Handler des Auswahlfelds (updateRecurrenceDayVisibility) statt
         einer zweiten Kopie derselben Regel. Das Ereignis steigt nicht auf,
         erreicht also den „berührt“-Merker am Overlay nicht. */
      recurrIntervalEl.dispatchEvent(new Event('change'));
    }

    /* Karte #817: Termine eingeklappt, wenn nichts gesetzt ist; sonst offen,
       solange der Nutzer sie auf diesem Gerät nicht zugeklappt hat. */
    let termineZu = null;
    try { termineZu = localStorage.getItem(TERMINE_KEY) === '0'; } catch {}
    overlay.querySelector('#modal-termine').open = !!zeigeTermine() && !termineZu;

    /* Karte #698: Im Server-Modus füllt fuelleBearbeiter() Owner und Assignees
       aus den Personen des Boards; die festen Claude-/User-Optionen entfallen.
       Überall sonst bleibt es wie bisher (ADR-0043 Check 1). */
    const bearbeiterAusServer = IS_SERVER_BUILD && Profile.has('feature.restStorage');
    if (!bearbeiterAusServer) {
      /* KO-6: Hat ein früheres Öffnen im Server-Modus das Select geleert,
         kommen die festen Optionen jetzt zurück — sonst ginge card.owner
         beim Speichern verloren. */
      const sel0 = overlay.querySelector('#modal-owner');
      if (sel0?.dataset.bearbeiterServer) fuelleFesteOwnerOptionen(sel0);
      /* User-Option aktuell halten (userName kann sich in Settings ändern) */
      const userOpt = overlay.querySelector('#modal-owner option[data-user]');
      if (userOpt) {
        const name = Config.get('userName') || 'User';
        userOpt.value = name;
        userOpt.textContent = name;
      }
    }
    const ownerSel = overlay.querySelector('#modal-owner');
    if (ownerSel && !bearbeiterAusServer) {
      /* VO-12: Ein Kartenowner außerhalb der festen Liste („Anna“ aus dem
         Server-Modus, „Claude Opus 5“) bleibt als Option erhalten — sonst
         verlöre ihn das Speichern still. Frühere Zusatzoption erst entfernen. */
      ownerSel.querySelectorAll('option[data-owner-extra]').forEach(o => o.remove());
      const ownerText = card.owner ? String(card.owner) : '';
      if (ownerText && ![...ownerSel.options].some(o => o.value === ownerText)) {
        const extra = el('option', { value: ownerText, text: ownerText });
        extra.dataset.ownerExtra = '1';
        ownerSel.appendChild(extra);
      }
      ownerSel.value = ownerText;
    }

    /* Karte #606: Übernahme anzeigen (Regel wie im Fold, „jetzt“ = Betrachter-Uhr). */
    if (IS_PROJECTOR_BUILD || IS_SERVER_BUILD) fuelleClaimZeile(card);

    if (bearbeiterAusServer) {
      fuelleBearbeiter(card, kandidatenSofort(State.get()?.board));
      const gen = ++bearbeiterGen;
      ladeKandidaten(State.get()?.board).then(namen => {
        /* Nur einspielen, wenn dieselbe Karte noch offen ist und die Auswahl
           zwischenzeitlich nicht neu aufgebaut wurde. */
        if (gen === bearbeiterGen && currentCard?.id === card.id) fuelleBearbeiter(card, namen, true);
      }).catch(() => { /* Rückfall steht schon im Dropdown */ });
    } else {
      /* VO-4: Auch hier die Generation hochzählen — eine späte Server-Antwort
         einer früheren Öffnung darf dieses Modal nicht überschreiben. */
      ++bearbeiterGen;
      /* #302: Assignees — Optionen dynamisch befüllen + Selektion setzen */
      const assigneesSel = overlay.querySelector('#modal-assignees');
      if (assigneesSel) {
        const userName = Config.get('userName') || 'User';
        const candidates = [userName, ...CLAUDE_MODELLE];
        /* Karten-bestehende Assignees mit aufnehmen (z. B. external:email) */
        const existing = Array.isArray(card.assignees) ? card.assignees : [];
        existing.forEach(a => { if (!candidates.includes(a)) candidates.push(a); });
        assigneesSel.innerHTML = '';
        candidates.forEach(name => {
          const opt = el('option', { value: name, text: name });
          if (existing.includes(name)) opt.selected = true;
          assigneesSel.appendChild(opt);
        });
      }
    }

    /* #307/ADR-0102: Projekt-Dropdown befüllen — server-only. Sichtbarkeit
       hängt von Server-Config (projects_enabled). Bei Clients-aktiv werden
       Projekte nach Client gruppiert (<optgroup>). IS_SERVER_BUILD=false →
       gesamter Block per DCE eliminiert. */
    if (IS_SERVER_BUILD) {
      const isServerMode = Profile.has('feature.restStorage');
      const pRow = overlay.querySelector('#modal-meta-row-project');
      const pSel = overlay.querySelector('#modal-project');
      if (pRow && pSel) {
        if (!isServerMode) {
          /* W-3: Profilwechsel — Befüllung der vorigen Karte verwerfen, nichts Altes schreiben. */
          delete pSel.dataset.fertig;
          ++projektGen;
          pRow.style.display = 'none';
        } else {
          /* M-2: Solange die Befüllung nicht fertig ist, liefert computeFormPatch
             die project_id der Karte (nicht den Wert einer früheren Öffnung). */
          delete pSel.dataset.fertig;
          pSel.innerHTML = '';
          const gen = ++projektGen;
          /* Server-Modus → Toggle + Projekte parallel laden, dann befüllen. */
          (async () => {
            const [cfgRes, projRes, clientRes] = await Promise.all([
              AdminClient.listConfig('clients-projects'),
              ClientsProjectsClient.listProjects(),
              ClientsProjectsClient.listClients(),
            ]);
            const cfgMap = {};
            /* Array.isArray statt `body && body.entries || []` (Sprint-54-Verify):
               Die alte Fassung wertet bei `body = []` zu `[].entries` aus — der
               eingebauten Array-METHODE, die truthy ist. Der `|| []`-Zweig greift
               dann nicht, und `.forEach` läuft auf eine Funktion: TypeError. Der
               Server liefert normalerweise `{entries:[…]}`, aber ein leeres Array
               (oder ein geänderter Endpunkt) genügt, um die Kartenansicht zu
               zerlegen. Dasselbe Muster lag an sieben Stellen. */
            (Array.isArray(cfgRes.body?.entries) ? cfgRes.body.entries : []).forEach(e => { cfgMap[e.key] = e; });
            const isBool = v => v === 'true' || v === '1';
            const projectsEnabled = isBool(cfgMap['clients-projects.projects_enabled']?.value);
            const clientsEnabled  = isBool(cfgMap['clients-projects.clients_enabled']?.value);
            /* Q-1: Generationszähler — auch dieselbe Karte, erneut geöffnet, verwirft die alte Antwort. */
            if (gen !== projektGen || currentCard?.id !== card.id) return;
            if (!projectsEnabled) { pRow.style.display = 'none'; return; }
            pRow.style.display = '';
            pSel.innerHTML = '';
            /* "Kein Projekt" als erste Option */
            const noneOpt = el('option', { value: '', text: I18n.t('card.project-none') });
            pSel.appendChild(noneOpt);
            const projects = (projRes.ok && Array.isArray(projRes.body))
              ? projRes.body.filter(p => p.active !== false) : [];
            if (clientsEnabled && Array.isArray(clientRes.body)) {
              /* Gruppiert per <optgroup> nach Client. */
              const clientById = new Map(clientRes.body.map(c => [c.id, c]));
              const grouped = new Map();
              for (const p of projects) {
                const key = p.client_id || '';
                if (!grouped.has(key)) grouped.set(key, []);
                grouped.get(key).push(p);
              }
              for (const [clientId, list] of grouped.entries()) {
                const label = clientId
                  ? (clientById.get(clientId)?.name || I18n.t('projects.group-no-client'))
                  : I18n.t('projects.group-no-client');
                const og = el('optgroup');
                og.setAttribute('label', label);
                list.forEach(p => {
                  const opt = el('option', { value: p.id, text: p.name });
                  og.appendChild(opt);
                });
                pSel.appendChild(og);
              }
            } else {
              /* Flache Liste. */
              projects.forEach(p => {
                const opt = el('option', { value: p.id, text: p.name });
                pSel.appendChild(opt);
              });
            }
            /* M-2: Eine project_id, die unter den (aktiven) Projekten fehlt, bleibt
               als Fremdoption erhalten — sonst löschte Öffnen+Schliessen sie. */
            const pid = card.project_id == null ? '' : String(card.project_id);
            if (pid && ![...pSel.options].some(o => o.value === pid)) {
              /* Q-7: Name aus der UNGEFILTERTEN Liste; inaktiv oder unbekannt eigens benannt. */
              const bekannt = (projRes.ok && Array.isArray(projRes.body)) ? projRes.body.find(p => String(p.id) === pid) : null;
              const fremd = el('option', { value: pid, text: bekannt
                ? I18n.t('card.project-inaktiv', bekannt.name || pid)
                : I18n.t('card.project-unbekannt', pid) });
              pSel.appendChild(fremd);
            }
            pSel.value = pid;
            pSel.dataset.fertig = '1';
          })();
        }
      }
    }

    const qaCb = overlay.querySelector('#modal-qa');
    if (qaCb) qaCb.checked = !!card.qa;
    const critCb = overlay.querySelector('#modal-critical');
    if (critCb) critCb.checked = !!card.critical;
    const blockedCb = overlay.querySelector('#modal-blocked');
    if (blockedCb) blockedCb.checked = !!card.blocked;
    if (IS_SERVER_BUILD) zeigeVertraulichSchalter(card);

    /* Karte #60: Archivieren-Button — Text und Sichtbarkeit aktualisieren */
    const archBtn = overlay.querySelector('#modal-archive-btn');
    if (archBtn) {
      /* F-830: Titel-Semantik statt Id-Hartvergleich (Links.isDoneColumnId) —
         sonst erscheint der Archivieren-Button auf einer umbenannten
         Erledigt-Spalte nie. */
      const inErledigt = isDoneColumnId(State.get()?.board, card.columnId);
      archBtn.style.display = inErledigt ? '' : 'none';
      archBtn.textContent = card.archived ? I18n.t('card.unarchive') : I18n.t('card.archive');
      archBtn.title = card.archived ? I18n.t('card.unarchive') : I18n.t('card.archive');
    }

    /* Q-3: Anzeigewert beim Öffnen merken (Vergleichsbasis des „berührt“-Merkers). */
    for (const id of ['#modal-title', '#modal-labels', '#modal-assignees']) {
      const f = overlay.querySelector(id);
      if (f) f.dataset.angezeigt = feldAnzeigeWert(f);
    }

    pendingComments = [];
    /* Karte #678: Dateiauswahl gehört zur zuvor offenen Karte. */
    if (IS_SERVER_BUILD) { kommentarDateien = []; renderKommentarDateiAuswahl(); }
    const list = overlay.querySelector('#comments-list');
    /* Badge wird innerhalb von renderComments aktualisiert (F-146).
       F-770(b): …aber nur, wenn es die Liste überhaupt gibt. Fehlt
       `#comments-list`, blieb der Badge der zuvor geöffneten Karte stehen —
       die F-146-Zusage „lebt IM Render-Pfad" hatte sich seit Karte #433 eine
       Bedingung eingekauft, die das alte Muster nicht hatte. Der else-Zweig
       stellt den unbedingten Badge-Stand wieder her, ohne den Render-Pfad
       aufzubrechen. */
    if (list) renderComments(list, card.comments);
    else updateCommentBadge();

    /* Tab-Sichtbarkeit anhand Owner + boardMode neu auswerten */
    updateTabVisibility(card.owner || '');

    /* ADR-0133 §1: Finding-Tab ein-/ausblenden und befüllen */
    const istFinding = card.kind === 'finding' && !!card.finding;
    overlay.querySelector('#tab-finding').style.display = istFinding ? '' : 'none';
    if (istFinding) {
      for (const k of ['typ', 'prioritaet', 'status', 'beleg', 'lehre']) {
        overlay.querySelector('#modal-finding-' + k).value = card.finding[k] ?? '';
      }
    }

    /* #299/ADR-0095: KI-Tab-Sichtbarkeit asynchron evaluieren (Keys-Cache). */
    if (IS_SERVER_BUILD) updateLLMTabVisibility();

    /* ADR-0036: Sprint-Meta-Karten — nur desc + comments sichtbar;
       Anhänge / Verknüpfungen / Verlauf / Agent-Kontext ausblenden.
       Außerdem: Titel nicht editierbar, Meta-Felder read-only. */
    const isMetaCard = card.kind === 'sprint-meta';
    updateMetaCardMode(isMetaCard);

    /* ADR-0040: Template-Bearbeitung — Tabs Anhänge/Verknüpfungen/Verlauf/Kommentare ausblenden.
       Nur für Nicht-Meta-Karten aufrufen — Meta-Karten haben eigenen eingeschränkten Modus.
       updateTemplateMode läuft auch für normale Karten (isTemplate=false) um zuvor
       gesetzten Template-Modus zurückzusetzen. */
    const isTemplateCard = !!card.isTemplate;
    if (!isMetaCard) {
      updateTemplateMode(isTemplateCard);
    }

    /* #294/ADR-0092: Spam-Aktion im Service-Board — Sichtbarkeit + Label
       je nach Karten-Spalte aktualisieren. Außerhalb kind:service immer
       ausgeblendet. Nur im Server-Build aktiv (DCE-friendly). */
    if (IS_SERVER_BUILD) updateServiceBoardMode(card);

    /* #296: Cross-Board-Transfer-Sichtbarkeit — nur Server-Modus + Nicht-Meta. */
    if (IS_SERVER_BUILD) updateTransferBtnVisibility(card);

    /* #672: Open-WebUI-Knopf — nur mit gesetzter ABOARD_OWUI_URL (aus /me). */
    if (IS_SERVER_BUILD) updateOwuiBtnVisibility(card);

    /* #685: „Extern bearbeiten“ — nur bei Schleuse „an“ und eigenem Anbieter
       oder vorhandenem Entwurf; die Markierung „Entwurf vorhanden“ sieht nur
       die Person selbst. */
    if (IS_SERVER_BUILD) updateSchleuseBtn(card);

    /* Immer mit desc-Tab starten (auch nach erneutem Öffnen) */
    activeTab = 'desc';
    kommentarModusSetzen(false);
    kommentarEingabeAnpassen(overlay.querySelector('#comment-input'));
    overlay.querySelectorAll('.tab-btn').forEach(btn => {
      const isDesc = btn.dataset.tab === 'desc';
      btn.classList.toggle('tab-active', isDesc);
      btn.setAttribute('aria-selected', isDesc ? 'true' : 'false');
    });
    overlay.querySelectorAll('.tab-panel').forEach(panel => {
      panel.classList.toggle('tab-panel-active', panel.dataset.tabPanel === 'desc');
    });

    overlay.setAttribute('aria-hidden', 'false');
    /* Einblend-Animation: der geschlossene Zustand (opacity:0, visibility:hidden,
       scale(.94) — style.css `.modal-overlay`) muss committet sein, bevor die
       '--open'-Klasse fällt, sonst greift die CSS-Transition beim allerersten
       Öffnen nicht.
       F-756: Das erledigte bis Sprint 61 ein `requestAnimationFrame`. Der
       Initialfokus weiter unten (`#modal-title`.focus()) läuft aber SYNCHRON
       in derselben Funktion — also garantiert VOR dem rAF-Callback. Damit
       traf `focus()` ein Element, dessen Vorfahre noch `visibility:hidden`
       war; ein solches Element ist per Fokus-Algorithmus nicht fokussierbar,
       der Aufruf war im echten Browser ein stiller No-Op (happy-dom zeigt das
       nicht: dort kaskadiert `visibility` nicht). Fix wie in modal-shell.js
       (F-754): erzwungener Reflow committet den geschlossenen Zustand
       synchron — gleicher visueller Effekt, ohne Nebenläufigkeit. */
    void overlay.offsetHeight;
    overlay.classList.add('modal-overlay--open');

    /* ADR-0033, Karte #50: Attachment-Resolver für Markdown-Inline-Bilder setzen.
       Resolver sucht nur in currentCard.attachments (keine Cross-Card-Auflösung).
       UUID-Validierung erfolgt im Markdown-Modul.
       Karte #117: vor renderDescPane() — sonst werden Inline-Bilder beim Re-Open
       ohne src gerendert, da der Resolver noch null/aus vorigem Open ist. */
    Markdown.setAttachmentResolver((id) => {
      const att = currentCard?.attachments?.find(a => a.id === id);
      if (!att) return Promise.resolve(null);
      return anhangBackend().attachmentUrl(currentCard, att);
    });

    renderDescPane();
    /* ADR-0032: Verknüpfungen-Liste vorausrendern (Tab könnte angeklickt werden) */
    renderLinksList();
    /* ADR-0033: Anhänge-Liste ebenfalls vorausrendern */
    renderAttachmentsList();
    /* ADR-0037: Verlauf-Liste vorausrendern */
    const histListEl = overlay.querySelector('#history-list');
    if (histListEl) renderHistoryList(histListEl, card.activity);

    /* Swipe-to-Dismiss nur einmal registrieren */
    if (!overlay.dataset.swipe) {
      overlay.dataset.swipe = '1';
      const modal = overlay.querySelector('.modal');
      /* Karte #463: Swipe-to-Dismiss ist derselbe ambige Fall wie Backdrop/
         X-Knopf/ESC (keine explizite Verwerfen-Handlung wie "Abbrechen") —
         war in der Kartenbeschreibung nicht extra aufgeführt, hätte sonst
         aber dieselbe Lücke unbehoben gelassen. */
      if (modal) addSwipeToDismiss(modal, closeWithAutosave);
    }

    if (IS_SERVER_BUILD && (nurLesen || rueckwegSperre)) setzeNurLesen(true);

    prevFocus = document.activeElement;
    overlay.querySelector('#modal-title').focus();
  }

  /* ADR-0141 §7/§8 (Karte #826): Lesemodus für Spiegelkarten. Sperrt alle
     Eingaben und Knöpfe des Modals ausser Tabs, Schliessen, Abbrechen und
     Export, und zeigt oben im Body ein Hinweisband. Gesperrt wird nur, was
     noch nicht gesperrt war — die Marke `data-nur-lesen` hält fest, was
     dieser Modus gesperrt hat, damit das Freigeben (beim nächsten open())
     keine Sperre anderer Modi (Meta-Karte, Rechte) aufhebt. */
  const NUR_LESEN_FREI = '.tab-btn, .btn-modal-close, .btn-modal-cancel, .modal-export-wrap *, .btn-maximize';

  /* Karte #827 E4: frei bei Rückweg an — eine Liste der erlaubten Elemente,
     kein Ausschluss: Ein neues Bedienelement bleibt gesperrt, bis es hier
     steht (Lehre 265). Erlaubt ist, was die feine Schranke des Servers
     annimmt (server/schatten/spiegel_rueckweg.go): die Feldliste (Titel,
     Beschreibung samt Checkliste, Priorität, kritisch, blockiert, Owner,
     Datum und Uhrzeit, archivieren), Labels, Bearbeiter, Kommentare anfügen
     (ohne Datei), „vertraulich“ einschalten (nur, wenn es beim Öffnen aus war)
     und die Übernahme aufheben (Begleitfeld). Die Kommentarliste kennt kein
     Bearbeiten und kein Löschen. */
  const RUECKWEG_FREI = NUR_LESEN_FREI + ', #modal-title, #modal-priority, #modal-critical, #modal-blocked, #modal-owner, ' +
    '#modal-assignees, #modal-assignee-chips *, #modal-labels, #modal-label-neu, #modal-label-chips *, ' +
    '#modal-due-date, #modal-due-time, #modal-start-date, #modal-start-time, .btn-archive, #modal-claim-aufheben, #modal-claim-grund, ' +
    '#tab-panel-desc .modal-desc-section *, #comment-input, .comment-form > .btn-primary, .activity-load-more, ' +
    '.modal-actions-right .btn-primary, #modal-confidential[data-anfang="0"]';

  function sperrFrei() { return rueckwegSperre ? RUECKWEG_FREI : NUR_LESEN_FREI; }

  /* Kritik K-1: Teile des Modals werden nach open() neu gebaut (Tab-Wechsel
     zu Verknüpfungen/Anhängen, Chips nach dem Nachladen der Bearbeiter) —
     danach erneut sperren. Die Klick-Sperre in buildOverlay() hält auch, wenn
     ein Neuaufbau hier nicht vorbeikommt. */
  function sperreNachNeuaufbau() {
    if (IS_SERVER_BUILD && (nurLesen || rueckwegSperre) && currentCard) setzeNurLesen(true);
  }

  function setzeNurLesen(an) {
    if (!overlay) return;
    const body = overlay.querySelector('.modal-body');
    overlay.querySelector('.modal-nur-lesen-band')?.remove();
    overlay.querySelectorAll('.anhang-sperr-hinweis').forEach(e => e.remove());
    overlay.querySelectorAll('[data-nur-lesen]').forEach(e => {
      e.disabled = false;
      e.removeAttribute('data-nur-lesen');
    });
    if (!an) return;
    const frei = sperrFrei();
    overlay.querySelectorAll('input, textarea, select, button').forEach(e => {
      if (e.disabled || e.matches(frei)) return;
      e.disabled = true;
      e.setAttribute('data-nur-lesen', '');
    });
    if (body) {
      const band = document.createElement('div');
      band.className = 'modal-nur-lesen-band';
      band.setAttribute('role', 'note');
      band.style.cssText = 'margin:0 0 10px;padding:8px 12px;border:1px dashed var(--border);border-radius:6px;background:var(--surface-2);color:var(--text);font-size:13px';
      if (IS_SERVER_BUILD) band.textContent = I18n.t(rueckwegSperre ? 'card.spiegel.rueckweg' : 'card.spiegel.nur-lesen');
      body.insertBefore(band, body.firstChild);
    }
    /* Karte #862: Upload-Knöpfe sind oben schon gesperrt — hier sagt ein Hinweis
       an den Stellen selbst, warum. Der Server lehnt Uploads an diese Karten auf jeder Kopie ab
       (422, F-2242/F-2374), mit und ohne Rückweg. Das CSS kommt per <style> aus diesem gegateten
       Zweig statt aus style.css, damit es im Slim-Template nicht als toter Code liegt (F-2410/V-4). */
    if (IS_SERVER_BUILD) {
      if (!document.getElementById('anhang-sperr-hinweis-stil')) {
        const st = document.createElement('style');
        st.id = 'anhang-sperr-hinweis-stil';
        st.textContent = '.anhang-sperr-hinweis{grid-column:1/-1;margin:4px 0;font-size:12px;color:var(--text-muted)}';
        document.head.appendChild(st);
      }
      ['.attachments-actions', '.comment-form'].forEach(sel => {
        const ziel = overlay.querySelector(sel);
        if (!ziel) return;
        const h = document.createElement('p');
        h.className = 'anhang-sperr-hinweis';
        h.setAttribute('role', 'note');
        h.textContent = I18n.t('attachment.spiegel-gesperrt');
        ziel.appendChild(h);
      });
    }
  }

  function close() {
    if (!overlay) return;
    /* Shortcut-Kontext zurück auf Board setzen (ADR-0035) */
    Shortcuts.setContext('board');
    /* Maximize beim Schließen immer zurücksetzen (F-108: korrekter Key) */
    if (maximizedPaneKey !== null) {
      const sectionId = maximizedPaneKey === 'agent' ? '#tab-panel-agent .modal-desc-section' : '#tab-panel-desc .modal-desc-section';
      const maxBtnSel = maximizedPaneKey === 'agent' ? '#tab-panel-agent .btn-maximize' : '#tab-panel-desc .btn-maximize';
      const section = overlay.querySelector(sectionId);
      const maxBtn  = overlay.querySelector(maxBtnSel);
      if (section && maxBtn) setMaximized(section, maxBtn, false);
    }
    /* F-107: Inline-Style aus Swipe-Dismiss entfernen — sonst bleibt translateY(100%) erhalten */
    const modal = overlay.querySelector('.modal');
    if (modal) modal.style.transform = '';
    kommentarModusSetzen(false);
    overlay.classList.remove('modal-overlay--open');
    overlay.setAttribute('aria-hidden', 'true');
    revokeTrackedUrls(); /* ADR-0033: ObjectURLs der Attachment-Thumbnails freigeben */
    /* ADR-0033, Karte #50: Resolver freigeben damit kein veralteter currentCard-Closure hängt */
    Markdown.setAttachmentResolver(null);
    /* #299/ADR-0095 Etappe 4: Pending-Lock-Polling stoppen (sonst läuft der
       Timer nach Modal-Close weiter und zieht weiter GET /llm-pending-Calls). */
    if (IS_SERVER_BUILD && LLMTab) LLMTab.stopPolling();
    /* Nacharbeit #817 K-2: Resttext leeren, BEVOR der Fokus wandert — sonst
       übernähme das Blur ihn nach dem Schliessen (Speichern hat ihn über
       formLabels() schon erfasst, Abbrechen verwirft ihn bewusst). */
    const labelNeu = overlay.querySelector('#modal-label-neu');
    if (labelNeu) labelNeu.value = '';
    currentCard = null;
    prevFocus?.focus();
    prevFocus = null;
    /* ADR-0028: Hash entfernen ohne hashchange-Event auszulösen.
       F-457 (Sprint-50-Kritiker, K3): Die Prüfung stand auf dem Präfix
       `#card-` und übersah damit die von Karte #366 eingeführte Langform
       `#board=…&card=…` — der Hash blieb stehen. Folge: Nutzer öffnet den
       Deep-Link, schliesst die Karte, wechselt per Sidebar das Board, lädt
       neu — und `handleCardHash()` wechselt ihn ungefragt wieder weg.
       `parseCardHash` erkennt beide Formen. Die Karten-Referenz ist die
       Bedingung: ein reiner `#board=…`-Link ohne `card=` hat dieses Modal
       nicht geöffnet und bleibt deshalb stehen — er ist der teilbare Link auf
       das Board und soll das Schliessen einer Karte überleben (F-462). */
    if (parseCardHash(location.hash)?.cardRef) {
      try { history.replaceState(null, '', location.pathname + location.search); } catch {}
    }
    /* ADR-0040: Template-Liste im Config-Dialog nach Schließen aktualisieren */
    EventBus.emit('modal:closed');
  }

  function collectRawText() {
    /* Aktiven Block committen — prüft beide Panes */
    const descContainer  = overlay?.querySelector('#desc-pane');
    const agentContainer = overlay?.querySelector('#agent-pane');
    if (descContainer) {
      const active = descContainer.querySelector('.typora-editing');
      if (active) commitTyporaBlock(active, 'desc');
      commitNewBlock(descContainer, 'desc');   /* K-2: Neu-Block beim Speichern nicht verlieren */
    }
    if (agentContainer) {
      const active = agentContainer.querySelector('.typora-editing');
      if (active) commitTyporaBlock(active, 'agent');
      commitNewBlock(agentContainer, 'agent'); /* K-2: dito agent-Pane */
    }
    return { desc: getPane('desc'), agent: getPane('agent') };
  }

  /* F-919 (Sprint-63-Kritiker, Karte #525): Nicht-mutierende Zwillinge von
     commitTyporaBlock()/commitNewBlock() für reine Lesepfade. `isDirty()`
     rief bislang collectRawText() auf, um "was steht gerade im Formular"
     zu ermitteln — und beging dabei denselben Commit wie ein echtes
     Speichern: `panes[key].text` wurde überschrieben, obwohl `isDirty()`
     nur eine Frage beantworten soll, keine Antwort persistieren. Betroffen
     war auch `onBeforeUnload()`, das bei JEDEM Entladeversuch (auch bei
     abgebrochener Navigation, "Auf der Seite bleiben") einen gerade aktiv
     bearbeiteten Typora-Block in den Pane-State committete, ohne dass je
     gespeichert wurde — das Formular zeigte danach weiter die offene
     Editier-Textarea, aber `panes[key].text` hatte deren Inhalt bereits
     übernommen. Sichtbarer Schaden war nicht bekannt, aber eine Prüf-
     funktion mit Schreib-Nebenwirkung ist eine Falle für den nächsten
     Aufrufer.
     Diese Funktionen spiegeln die Commit-Logik 1:1, LESEN aber nur — sie
     rufen weder `setPane()` noch `commitTyporaBlock()`/`commitNewBlock()`
     auf. Änderungen an einer der beiden Original-Funktionen (Block-
     Tokenisierung, Neu-Block-Verkettung) müssen hier nachgezogen werden;
     ein Spiegel-Test dafür wäre selbst wieder Duplikation, deshalb bewusst
     nur ein Kommentar-Verweis statt eines zweiten Testpaars. */
  function peekPaneText(key) {
    const containerId = key === 'agent' ? '#agent-pane' : '#desc-pane';
    const container = overlay?.querySelector(containerId);
    let text = getPane(key);
    if (!container) return text;

    /* Wie commitTyporaBlock(): aktiven Block gegen die aktuellen Block-
       grenzen splicen — nur ohne setPane() am Ende. */
    const editing = container.querySelector('.typora-editing');
    if (editing) {
      const ta = editing.querySelector('.typora-textarea');
      const blockIdx = parseInt(editing.dataset.blockIdx, 10);
      if (ta && !isNaN(blockIdx)) {
        const lines  = text.split('\n');
        const blocks = Markdown.tokenizeBlocks(text);
        const block  = blocks[blockIdx];
        if (block) {
          const newBlockLines = ta.value.split('\n');
          const newLines = [...lines];
          newLines.splice(block.start, block.end - block.start, ...newBlockLines);
          text = newLines.join('\n');
        }
      }
    }

    /* Wie commitNewBlock(): offene Neu-Block-Textarea anhängen — nur ohne
       setPane() am Ende. */
    const neu = container.querySelector('.typora-new');
    if (neu) {
      const val = neu.value.trim();
      if (val) text = text ? text + '\n\n' + neu.value : neu.value;
    }

    return text;
  }

  function peekRawText() {
    return { desc: peekPaneText('desc'), agent: peekPaneText('agent') };
  }

  /* Karte #463: Formularfelder zu einem Update-Patch zusammenfassen — OHNE
     zu speichern oder zu schliessen. Bis Karte #463 stand diese Ableitung
     nur inline in saveAndClose(); jetzt ist sie die gemeinsame Grundlage für
     saveAndClose() (Persistieren beim Speichern-Knopf) UND isDirty()
     (Dirty-Check für Auto-Save-beim-Schliessen und den beforeunload-Guard) —
     eine Quelle für "was steht gerade im Formular", damit beide Pfade nicht
     auseinanderlaufen können. Committet dabei aktive Typora-Blöcke (wie
     vorher schon in saveAndClose) — ein zweiter Aufruf direkt danach (z. B.
     saveAndClose() nach isDirty()) ist ein No-Op, weil kein
     `.typora-editing`-Element mehr existiert. */
  /* F-919: `commit` steuert, ob der Rohtext-Schritt schreibend
     (collectRawText(), Default — echtes Speichern) oder lesend
     (peekRawText(), von isDirty() genutzt) erfolgt. Alle übrigen Felder
     dieser Funktion lesen ohnehin nur aus dem DOM, ohne Nebenwirkung. */
  function computeFormPatch({ commit = true } = {}) {
    if (!currentCard) return null;

    const { desc, agent } = commit ? collectRawText() : peekRawText();

    /* ADR-0036: Sprint-Meta-Karten — nur Beschreibung + Kommentare speichern;
       alle anderen Felder (title, priority, owner, qa, etc.) bleiben unverändert. */
    if (currentCard.kind === 'sprint-meta') {
      return {
        description: desc,
        comments:   [...(currentCard.comments ?? []), ...pendingComments],
      };
    }

    /* ADR-0040: Template-Bearbeitung — Skelett-Felder */
    if (currentCard.isTemplate) {
      const title = overlay.querySelector('#modal-title').value.trim();
      const labels = formLabels();
      const owner      = overlay.querySelector('#modal-owner')?.value ?? '';
      const complexity = leseComplexity();
      const effort     = leseEffort();
      return { title, description: desc, labels, owner, complexity, effort };
    }

    const title = overlay.querySelector('#modal-title').value.trim();
    const labels = formLabels();

    /* ADR-0032: links werden über separate card:link-add/remove-Events live persistiert.
       Beim Save die aktuelle Sicht aus dem State übernehmen, damit kein veralteter Stand
       die zwischenzeitlich gesetzten Verweise überschreibt. */
    const freshSelf = findCardById(currentCard.id);
    const currentLinks = Array.isArray(freshSelf?.card?.links)
      ? freshSelf.card.links
      : (Array.isArray(currentCard.links) ? currentCard.links : []);
    /* ADR-0033: attachments analog — live-persistiert über card:attachment-add/remove */
    const currentAttachments = Array.isArray(freshSelf?.card?.attachments)
      ? freshSelf.card.attachments
      : (Array.isArray(currentCard.attachments) ? currentCard.attachments : []);

    /* ADR-0041: Recurrence aus Modal lesen */
    const recurrIntervalVal = overlay.querySelector('#modal-recurrence-interval')?.value ?? '';
    let recurrenceVal = null;
    if (recurrIntervalVal === FREMD) {
      /* Verify V-2: unbekanntes Intervall — die Wiederholung bleibt, wie sie ist. */
      recurrenceVal = currentCard.recurrence ?? null;
    } else if (recurrIntervalVal && recurrIntervalVal === currentCard.recurrence?.interval &&
               leseTagNummer() === tagNummer(currentCard.recurrence?.day)) {
      /* Kritiker Sprint 88 (F-1964): unverändert — die Wiederholung bleibt, wie
         sie ist. Neu gebaut fehlten Zusatzschlüssel, `day: "1"` wurde zu 1, und
         ein fehlendes lastSpawned wurde „gestern“: schon das Ansehen aktivierte
         so eine ruhende Wiederholung (recurrence.js überspringt sie ohne). */
      recurrenceVal = currentCard.recurrence;
    } else if (recurrIntervalVal) {
      const dayRaw = overlay.querySelector('#modal-recurrence-day')?.value;
      const dayNum = dayRaw !== '' && dayRaw != null ? parseInt(dayRaw, 10) : undefined;
      /* lastSpawned: wenn bereits vorhanden, beibehalten; sonst yesterday (= sofortiger Spawn beim nächsten Load) */
      const existingLastSpawned = currentCard.recurrence?.lastSpawned;
      const today = new Date().toISOString().slice(0, 10);
      /* lastSpawned auf gestern setzen wenn frisch aktiviert, damit Spawn beim nächsten Load ausgelöst wird */
      const prevDay = new Date(today + 'T12:00:00Z');
      prevDay.setUTCDate(prevDay.getUTCDate() - 1);
      const yesterday = prevDay.toISOString().slice(0, 10);
      recurrenceVal = {
        interval:    recurrIntervalVal,
        lastSpawned: existingLastSpawned ?? yesterday,
      };
      if (dayNum != null && !isNaN(dayNum)) recurrenceVal.day = dayNum;
    }

    /* #307/ADR-0102: project_id aus Modal lesen — nur im Server-Build;
       im Standalone bleibt der bestehende Wert erhalten (kann theoretisch
       per JSON-Import gesetzt sein). IS_SERVER_BUILD=false → DCE't den
       projectSel-Lookup zu undefined; fallback auf currentCard.project_id. */
    let projectIdVal = currentCard.project_id ?? null;
    if (IS_SERVER_BUILD) {
      const pSel = overlay.querySelector('#modal-project');
      if (pSel && pSel.dataset.fertig === '1') projectIdVal = pSel.value || null;
    }

    return {
      title,
      description:  desc,
      agentContext: agent,
      comments:    [...(currentCard.comments ?? []), ...pendingComments],
      qa:          !!overlay.querySelector('#modal-qa')?.checked,
      critical:    !!overlay.querySelector('#modal-critical')?.checked,
      blocked:     !!overlay.querySelector('#modal-blocked')?.checked,
      /* Karte #778: `confidential` nur, wenn der Schalter bedient wurde — ein
         ständig mitgeschicktes false entschärfte bei veraltetem Stand still
         (Vorprüfung P-8/P-17). Sonst trägt die frische Basis den Wert. */
      ...(IS_SERVER_BUILD ? vertraulichPatch() : {}),
      owner:       overlay.querySelector('#modal-owner')?.value ?? '',
      /* #302: Assignees aus Multi-Select sammeln */
      assignees:   (() => {
        const sel = overlay.querySelector('#modal-assignees');
        return sel ? [...sel.selectedOptions].map(o => o.value) : (currentCard.assignees || []);
      })(),
      priority:    leseMitFremdwert(overlay.querySelector('#modal-priority')),
      complexity:  leseComplexity(),
      effort:      leseEffort(),
      /* ADR-0034: Datumsfelder — leerer String wird als null gespeichert */
      dueDate:     overlay.querySelector('#modal-due-date')?.value   || null,
      startDate:   overlay.querySelector('#modal-start-date')?.value || null,
      /* ADR-0138 §1: Uhrzeit nur mit Datum — wer das Datum leert, leert sie mit. */
      dueTime:     (overlay.querySelector('#modal-due-date')?.value   && overlay.querySelector('#modal-due-time')?.value)   || null,
      startTime:   (overlay.querySelector('#modal-start-date')?.value && overlay.querySelector('#modal-start-time')?.value) || null,
      /* ADR-0041: Wiederkehrend */
      recurrence:  recurrenceVal,
      /* #307/ADR-0102: Projekt-Zuordnung (server-only befüllt) */
      project_id:  projectIdVal,
      labels,
      links:       currentLinks,
      attachments: currentAttachments,
      /* ADR-0133 §1: Finding-Felder; `nr` bleibt, unveränderte Texte bleiben roh
         (fehlend ≠ ''), damit isDirty() nichts Falsches meldet. */
      ...(currentCard.kind === 'finding' && currentCard.finding ? { finding: (() => {
        const f = { ...currentCard.finding };
        for (const k of ['typ', 'prioritaet', 'status', 'beleg', 'lehre']) {
          const v = overlay.querySelector('#modal-finding-' + k).value;
          if (v !== (f[k] ?? '')) f[k] = v;
        }
        return f;
      })() } : {}),
    };
  }

  function saveAndClose() {
    if (!currentCard) return;
    if (nurLesen) { close(); return; }

    /* F-918: `rohPatch` ist der volle Formularstand (Basis der Titel-
       Validierung unten — die prüft, was IM FELD steht, nicht ob es geändert
       wurde), `patch` nur der geänderte Teil davon; `basis` ist der FRISCHE
       Stand aus dem State statt des Öffnungs-Schnappschusses `currentCard`.
       Ausführliche Begründung samt Restfällen bei nurTatsaechlichGeaenderte(). */
    const rohPatch = computeFormPatch();
    const basis    = frischeBasis();
    const patch    = nurTatsaechlichGeaenderte(rohPatch, basis);

    /* ADR-0036: Sprint-Meta-Karten — nur Beschreibung + Kommentare speichern;
       alle anderen Felder (title, priority, owner, qa, etc.) bleiben unverändert. */
    if (currentCard.kind === 'sprint-meta') {
      EventBus.emit('card:updated', {
        ...basis,
        ...patch,
        updated: new Date().toISOString().slice(0, 10),
      });
      close();
      return;
    }

    /* ADR-0040: Template-Bearbeitung — Skelett-Felder speichern über template:update-Event.
       `basis` fällt hier immer auf `currentCard` zurück (Templates liegen nicht
       in board.sprints, siehe frischeBasis()) — der Merge ist damit
       wertgleich zum vorherigen `{ ...currentCard, ...rohPatch }`. */
    if (currentCard.isTemplate) {
      if (!rohPatch.title) return;
      EventBus.emit('template:update', { ...basis, ...patch });
      close();
      return;
    }

    if (!rohPatch.title) return;

    EventBus.emit('card:updated', {
      ...basis,
      ...patch,
      updated: new Date().toISOString().slice(0, 10),
    });
    close();
  }

  /* Karte #463 / Verify-Lauf (Sprint 63, ADR-0115 Phase 2b): Felder, die
     NICHT im Formular stehen, sondern live über eigene Events persistiert
     werden (card:link-add/-remove, card:attachment-add/-remove).
     computeFormPatch() liefert sie trotzdem mit — saveAndClose() muss den
     aktuellen Stand mitschreiben —, aber sie im Dirty-Check zu vergleichen
     hiesse, `freshSelf` gegen sich selbst zu vergleichen (immer
     "unverändert"). Ein KÜNFTIGES Feld, das ebenfalls live statt übers
     Formular persistiert wird, fällt NICHT automatisch hier hinein — wer ein
     solches Feld ergänzt, trägt es hier bewusst ein (siehe isDirty()-
     Kommentar: alles, was hier NICHT steht, wird verglichen). */
  const LIVE_PERSISTED_PATCH_KEYS = new Set(['links', 'attachments']);

  /* Default-Werte je Feld — dieselben "leer"-Werte, mit denen open() das
     Formular aus der Karte befüllt (siehe dort). Nötig, damit z. B. eine
     Karte ohne `labels`-Feld (undefined) nicht fälschlich als "dirty" gegen
     das vom Formular gelieferte `[]` erscheint. Ein Feld, das HIER fehlt,
     fällt NICHT aus dem Dirty-Check heraus (das war der Verify-Lauf-Befund
     zu `labels`) — es wird nur ohne Default-Auffüllung roh verglichen, im
     Zweifel also EHER zu oft als "dirty" erkannt (führt zu einem
     überflüssigen, aber harmlosen Auto-Save), nie stillschweigend
     übersprungen (das wäre Datenverlust). */
  const PATCH_FIELD_DEFAULTS = {
    title: '', description: '', agentContext: '', comments: [], owner: '',
    priority: 'mittel', complexity: '', effort: null, dueDate: null,
    startDate: null, dueTime: null, startTime: null, recurrence: null, project_id: null, labels: [], assignees: [],
    qa: false, critical: false, blocked: false,
  };

  /* Karte #463: Prüft, ob das Formular vom zuletzt gespeicherten Stand
     (currentCard) abweicht. Grundlage für closeWithAutosave() UND
     onBeforeUnload() — eine Quelle für "gibt es etwas zu verlieren".

     Verify-Lauf (Sprint 63): Die erste Fassung zählte die zu vergleichenden
     Felder je Kartentyp einzeln in drei if-Ketten auf — genau die
     Fehlerklasse "Aufzählung, die an mehreren Stellen driftet": `labels`
     stand in computeFormPatch() UND im Formular, fehlte aber in der
     Aufzählung des Normal-Zweigs und liess Label-Only-Änderungen beim
     Schliessen wortlos verschwinden (Test "Nur-Labels-Änderung..." weiter
     unten in dieser Datei belegt das, rot vor dieser Fassung).

     Jetzt: EIN generischer Loop über die Schlüssel, die computeFormPatch()
     TATSÄCHLICH zurückgibt — kartentyp-unabhängig, weil die drei Zweige dort
     ohnehin unterschiedliche Schlüsselmengen liefern (sprint-meta z. B. nur
     `description`+`comments`). Ein künftiges Feld in computeFormPatch()
     taucht automatisch HIER auf, statt zusätzlich in einer zweiten,
     unabhängig zu pflegenden Liste nachgetragen werden zu müssen — einzige
     Ausnahme ist LIVE_PERSISTED_PATCH_KEYS (siehe dort). */
  /* Wie `JSON.stringify`, aber mit sortierten Objekt-Schlüsseln.
     Kritiker-Befund Sprint 63, nachgemessen: `recurrence` ist das einzige
     Feld, das `computeFormPatch()` als FRISCH GEBAUTES Objekt zurückgibt —
     in der Reihenfolge `{interval, lastSpawned, day}`. Ein Board-Import, der
     es als `{"interval":…,"day":…,"lastSpawned":…}` mitbringt, ergab damit
     bei jedem Öffnen `isDirty() === true`: Jedes Schliessen über
     Backdrop/ESC/X schrieb eine Op, einen Aktivitätseintrag und einen
     Sync-Vorgang — fürs blosse Ansehen der Karte. Gemessen: `card:updated`
     wurde 1× emittiert, obwohl nichts geändert war.
     Array-Reihenfolge bleibt unangetastet: Dort IST die Reihenfolge Teil des
     Werts (Kommentare, Labels), und ein Sortieren würde echte Änderungen
     verstecken. */
  function stabilJson(wert) {
    return JSON.stringify(wert, (_schluessel, v) => {
      if (v && typeof v === 'object' && !Array.isArray(v)) {
        return Object.keys(v).sort().reduce((akk, k) => { akk[k] = v[k]; return akk; }, {});
      }
      return v;
    });
  }

  /* ── Karte #778 (ADR-0140 §10): Schalter „vertraulich“ ─────────────── */
  function vertraulichPatch() {
    const cb = overlay?.querySelector('#modal-confidential');
    if (!cb || cb.hidden || cb.dataset.anfang === undefined) return {};
    const an = !!cb.checked;
    return an === (cb.dataset.anfang === '1') ? {} : { confidential: an };
  }

  function zeigeVertraulichSchalter(card) {
    const cb  = overlay.querySelector('#modal-confidential');
    const lbl = overlay.querySelector('label[for="modal-confidential"]');
    if (!cb || !lbl) return;
    const an = istVertraulich(card);
    cb.checked = an;
    cb.dataset.anfang = an ? '1' : '0';
    /* Das Overlay wird wiederverwendet: Die Antwort auf die Seitenfrage darf
       nur die Karte treffen, für die sie gestellt wurde (Verify-2 #778, W2-1). */
    const fuerKarte = String(card?.id ?? '');
    cb.dataset.karte = fuerKarte;
    const zurueck = overlay.querySelector('#modal-zurueckholen');
    const setzeSichtbar = sichtbar => {
      cb.hidden = !sichtbar; lbl.hidden = !sichtbar;
      if (zurueck) zurueck.hidden = !(sichtbar && !an && card?.angelegtVonAgent === true);
    };
    if (zurueck) zurueck.hidden = true;
    /* Karte #827 E4: Außen bei Rückweg an darf man eine gebundene Karte
       vertraulich machen (nie zurück, ADR-0141 §7) — der Schalter ist da;
       steht er schon an, sperrt ihn RUECKWEG_FREI. */
    setzeSichtbar(an || (IS_SERVER_BUILD && rueckwegSperre));
    if (an || (IS_SERVER_BUILD && rueckwegSperre)) return;
    boardpaarSeite().then(seite => {
      if (overlay?.querySelector('#modal-confidential') === cb && cb.dataset.karte === fuerKarte) setzeSichtbar(seite === 'innen');
    });
  }

  function isDirty() {
    if (!overlay || !currentCard) return false;
    /* F-919: nicht-committierend — isDirty() ist eine Prüfung, kein
       Speichervorgang. Der Schliessen-Pfad (closeWithAutosave() →
       saveAndClose()) ruft computeFormPatch() bei tatsächlicher Dirty-Lage
       danach ERNEUT auf, diesmal mit dem Default `commit: true` — das
       eigentliche Speichern committet also weiterhin vollständig. */
    const patch = computeFormPatch({ commit: false });
    if (!patch) return false;

    for (const key of Object.keys(patch)) {
      if (LIVE_PERSISTED_PATCH_KEYS.has(key)) continue;
      if (feldGeaendert(key, patch[key])) return true;
    }
    return false;
  }
  /* #307/ADR-0102, zur obigen Schleife: Im Standalone-Build ist `project_id`
     kein echtes Formularfeld (kein #modal-project-Select) — computeFormPatch()
     liefert dort unverändert `currentCard.project_id` zurück (siehe dort).
     `patchVal === cardVal` gilt für diesen Schlüssel also automatisch immer,
     ohne dass die Schleife das Feld gesondert behandeln müsste — im
     Server-Build wird es dagegen echt aus dem Select gelesen und verglichen. */

  /* ── F-918 / Karte #482: Auto-Save darf keine FREMDE Änderung zurückrollen ──

     Der Defekt (Kritiker-Lauf zum Sprint-63-Abschluss): Jeder speichernde
     Schliessweg emittierte `{ ...currentCard, ...patch }`, und main.js ersetzt
     die Karte im State VOLLSTÄNDIG (`found.sprint.cards[found.idx] =
     updatedCard`). `currentCard` ist aber der Stand BEIM ÖFFNEN des Modals;
     einen Guard, der das Sync-Polling bei offenem Modal anhält, gibt es nicht.
     Bis Karte #463 löste das nur der bewusste Klick auf „Speichern" aus — seit
     dem Auto-Save beim Schliessen zusätzlich Backdrop-Fehlklick, ESC und
     Swipe. Szenario: A öffnet Karte #123, B verschiebt sie nach col-erledigt,
     setzt qa und hängt einen Kommentar an, A tippt ein Zeichen und klickt
     daneben → columnId und qa fallen zurück, B's Kommentar ist weg. Die
     #463-Begründung „versehentlich gespeichert ist reversibel" trägt für die
     EIGENE Eingabe; für die dabei still zurückgerollte fremde Änderung nicht —
     und das Board hat kein Undo.

     Die Regel lautet deshalb NICHT „frisch gewinnt": Ändert der Nutzer den
     Titel im Modal, muss SEIN Titel gewinnen, nicht der zwischenzeitlich
     gepollte. Stattdessen:

         Basis ist der FRISCHE Stand aus dem State.
         Darübergelegt wird nur, was der Nutzer TATSÄCHLICH im Formular
         geändert hat.

     Die dafür nötige Unterscheidung „geändert vs. bloss mitgeliefert" liefert
     `computeFormPatch()` nicht — es gibt für jedes Feld einen Wert zurück,
     auch für unangetastete. Die Unterscheidung existiert aber bereits: Es ist
     exakt der Feldvergleich, den `isDirty()` fährt (Formularwert gegen
     `currentCard`, also gegen den Öffnungs-Schnappschuss), nur eben pro Feld
     statt als ein Gesamt-Boolean. `nurTatsaechlichGeaenderte()` unten ist
     dieselbe Schleife mit demselben `stabilJson`/`PATCH_FIELD_DEFAULTS`-
     Regelwerk — bewusst dieselbe Quelle, damit „was gilt als geändert?" nicht
     an zwei Stellen auseinanderlaufen kann (genau die Drift-Fehlerklasse, die
     der Verify-Lauf zu #463 bei `labels` gefunden hat).

     WAS DAS NICHT LÖST — der benannte Restfall: Ändern zwei Personen
     DASSELBE Feld gleichzeitig, gewinnt der Schliessende. Das ist
     Last-Write-Wins auf FELD-Ebene statt bisher auf KARTEN-Ebene, also eine
     echte Verkleinerung des Schadens (statt der ganzen Karte geht nur noch das
     eine wirklich umkämpfte Feld verloren), aber keine Auflösung: Eine echte
     Auflösung bräuchte Feld-Versionen oder einen Konflikt-Dialog — beides
     hat das Board nicht, und beides wäre eine eigene Entscheidung (ADR), keine
     Nebenwirkung eines Bugfixes. Der Fall ist auch der unwahrscheinlichere:
     ein Feld, das beide gleichzeitig anfassen, gegen „irgendein anderes Feld
     derselben Karte".

     ZWEITER RESTFALL, bewusst: `close()` schreibt nichts zurück ins Formular.
     Bleibt das Modal nach einer fremden Änderung offen, zeigt es weiter die
     alten Werte an — der Nutzer SIEHT die fremde Änderung nicht, sie geht nur
     nicht mehr verloren. Ein Live-Nachführen des offenen Formulars wäre die
     nächste Ausbaustufe und würde ihrerseits die Frage aufwerfen, was mit
     halbfertigen Eingaben im nachgeführten Feld passiert.

     Anmerkung zu den bereits vorhandenen Teil-Nachführungen (`links`,
     `attachments`, `coverAttachmentId`): Die bleiben, wo sie sind. Sie decken
     einen ANDEREN Fall ab — Felder, die das Modal SELBST über eigene Events
     live persistiert, während es offen ist; dort muss `currentCard` mitlaufen,
     damit die Anzeige stimmt. Der Fix hier deckt sie zusätzlich mit ab
     (`LIVE_PERSISTED_PATCH_KEYS` wird unverändert durchgereicht, weil
     `computeFormPatch()` diese beiden ohnehin schon frisch aus dem State
     liest), macht sie aber nicht überflüssig. */

  /** Frischer Stand der offenen Karte aus dem State — Basis jedes
   *  card:updated. Fällt auf `currentCard` zurück, wenn die Karte im State
   *  nicht (mehr) auffindbar ist: Templates (ADR-0040) liegen gar nicht in
   *  `board.sprints`, und eine zwischenzeitlich gelöschte Karte darf nicht in
   *  ein leeres Objekt münden. */
  function frischeBasis() {
    if (!currentCard) return null;
    return findCardById(currentCard.id)?.card ?? currentCard;
  }

  /** Reduziert einen `computeFormPatch()`-Rohpatch auf die Felder, die der
   *  Nutzer im Formular tatsächlich gegenüber dem Öffnungs-Schnappschuss
   *  geändert hat. Alles andere bleibt weg, damit der frische Stand darunter
   *  stehen bleibt statt überschrieben zu werden.
   *
   *  `comments` ist dabei KEIN Formularfeld, sondern eine Anhänge-Liste:
   *  `computeFormPatch()` baut sie als `[...currentCard.comments,
   *  ...pendingComments]` — der Vergleich „geändert?" wäre hier also
   *  gleichbedeutend mit „gibt es unabgeschickte Kommentare?", und ein
   *  Übernehmen des Rohwerts würde die fremden Kommentare abschneiden, die
   *  seit dem Öffnen dazugekommen sind. Deshalb wird die Liste stattdessen auf
   *  der FRISCHEN Basis neu zusammengesetzt. (Kommentare lassen sich im Modal
   *  nur anhängen, nicht löschen oder bearbeiten — sonst trüge dieses Anhängen
   *  nicht.) */
  /* Karte #774 / F-1939: Schätzwerte aus fremden Skalen (S/M/L, "30 min" …)
     stehen in Konsumenten-Boards massenhaft. Das Auswahlfeld kannte sie nicht,
     das Zahlenfeld zeigte sie leer — und weil isDirty() Formular gegen Karte
     vergleicht, löschte schon Öffnen und Schliessen die Schätzung. Jetzt: der
     fremde complexity-Wert steht als eigene Option im Feld, ein fremder
     effort-Wert bleibt als Altwert am Feld hängen, bis jemand etwas tippt. */
  const FREMD = '__fremd__';
  const BEKANNTE_KOMPLEXITAET = new Set(['', 'einfach', 'mittel', 'hoch']);
  const BEKANNTE_PRIORITAET   = new Set(['niedrig', 'mittel', 'hoch']);
  const BEKANNTE_INTERVALLE   = new Set(['', 'daily', 'weekly', 'monthly']);

  function fuelleSchaetzfelder(card) {
    const sel = overlay.querySelector('#modal-complexity');
    /* Kritiker M-1: Auch Zahlen (1/2/3 in linux, framework), 0 und Wahrheitswerte
       sind fremde Werte. Ein <select> kennt nur Text — der Rohwert bleibt deshalb
       wie bei effort als JSON am Feld und kommt zurück, solange die Fremd-Option
       gewählt ist. Kein `|| ''`: das machte aus 0 einen leeren Wert. */
    setzeMitFremdwert(sel, card.complexity ?? '', BEKANNTE_KOMPLEXITAET);

    const eff = overlay.querySelector('#modal-effort');
    if (card.effort != null && typeof card.effort !== 'number') {
      eff.value = '';
      eff.dataset.altwert = JSON.stringify(card.effort);
      eff.placeholder = I18n.t('card.effort.fremd', String(card.effort));
      zeigeEffortHinweis(card.effort === '' ? null : String(card.effort));
    } else {
      delete eff.dataset.altwert;
      eff.value = card.effort != null ? card.effort : '';
      eff.placeholder = I18n.t('card.effort.placeholder');
      zeigeEffortHinweis(null);
    }
  }

  /* F-1967: Hinweis „Bisheriger Wert“ am Aufwandsfeld, per aria-describedby
     verbunden; ohne Fremdwert weg (das Overlay wird wiederverwendet). */
  function zeigeEffortHinweis(text) {
    const eff  = overlay?.querySelector('#modal-effort');
    const hint = overlay?.querySelector('#modal-effort-hint');
    if (!eff || !hint) return;
    if (text == null) {
      hint.hidden = true;
      hint.textContent = '';
      eff.removeAttribute('aria-describedby');
    } else {
      hint.textContent = I18n.t('card.effort.hinweis', text);
      hint.hidden = false;
      eff.setAttribute('aria-describedby', 'modal-effort-hint');
    }
  }

  function leseComplexity() {
    return leseMitFremdwert(overlay?.querySelector('#modal-complexity')) ?? '';
  }

  /* Verify V-2/V-3 (F-1947): ein Auswahlfeld, das einen fremden Wert nicht als
     Option kennt, zeigt ihn leer — und der Autosave schreibt dann leer zurück.
     setzeMitFremdwert hängt für einen fremden Wert (jeder Typ ausser einem
     bekannten Text) eine Option FREMD an und hält den Rohwert als JSON am
     Feld; leseMitFremdwert gibt ihn mit Typ zurück, solange sie gewählt ist.
     Ohne Rohwert (nicht erreichbar, Härtung V-3) liefert es '' statt FREMD. */
  function setzeMitFremdwert(sel, wert, bekannt) {
    if (!sel) return;
    sel.querySelectorAll('option[data-fremd]').forEach(o => o.remove());
    delete sel.dataset.altwert;
    if (typeof wert === 'string' && bekannt.has(wert)) { sel.value = wert; return; }
    const opt = el('option', { value: FREMD, text: I18n.t('card.fremdwert', String(wert)) });
    opt.dataset.fremd = '1';
    sel.appendChild(opt);
    sel.dataset.altwert = JSON.stringify(wert);
    sel.value = FREMD;
  }

  function tagNummer(roh) {
    const n = roh !== '' && roh != null ? parseInt(roh, 10) : undefined;
    return n != null && !isNaN(n) ? n : undefined;
  }

  function leseTagNummer() {
    return tagNummer(overlay?.querySelector('#modal-recurrence-day')?.value);
  }

  function leseMitFremdwert(sel) {
    if (!sel) return undefined;
    if (sel.value !== FREMD) return sel.value;
    return sel.dataset.altwert !== undefined ? JSON.parse(sel.dataset.altwert) : '';
  }

  function leseEffort() {
    const eff = overlay?.querySelector('#modal-effort');
    const v = eff?.value;
    if ((v === '' || v == null) && eff?.dataset.altwert !== undefined) return JSON.parse(eff.dataset.altwert);
    return v !== '' && v != null ? parseFloat(v) : null;
  }

  /* Kritiker Sprint 88 (F-1963): Eine leere Priorität `''` (Bestand, u. a. als
     Spur des alten F-1947-Verlusts) zeigt das Modal als „mittel“. Für den
     Vergleich sind beide gleich — sonst schrieb schon das blosse Ansehen
     „mittel“ in 82 Karten des eigenen Boards. Gemeinsam für isDirty() und
     nurTatsaechlichGeaenderte(), damit beide dasselbe für „geändert“ halten. */
  function vergleichsWert(key, wert) {
    return key === 'priority' && wert === '' ? 'mittel' : wert;
  }

  /* F-1964 (Altwert-Prinzip wie effort/complexity, #774): Was das Formular für
     ein UNBERÜHRTES Feld liefert, ist nicht immer der Rohwert der Karte
     (Labels mit Komma, Datum im Fremdformat, Titel mit Leerraum, `qa: "true"`,
     doppelte Assignees …). Ein Feld gilt als unverändert, wenn das Formular
     entweder den Rohwert oder dessen Formular-Normalform liefert — dann bleibt
     beim Speichern der Rohwert der Karte stehen. Was der Nutzer selbst
     eingibt, weicht von beiden ab und wird gespeichert. */
  const ISO_DATUM = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$/;
  const ISO_ZEIT  = /^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d(\.\d+)?)?$/;
  const nurDatum  = v => (typeof v === 'string' && ISO_DATUM.test(v) ? v : null);

  function formularNormalform(key, karte) {
    const v = karte?.[key];
    switch (key) {
      case 'title':    return String(v ?? '').trim();
      case 'labels':   return (Array.isArray(v) ? v : []).join(', ').split(',').map(x => x.trim()).filter(Boolean);
      case 'dueDate': case 'startDate': return nurDatum(v);
      case 'dueTime': case 'startTime': {
        const d = nurDatum(karte[key === 'dueTime' ? 'dueDate' : 'startDate']);
        return d && typeof v === 'string' && ISO_ZEIT.test(v) ? v : null;
      }
      case 'qa': case 'critical': case 'blocked': return !!v;
      case 'owner':    return String(v ?? '');
      case 'project_id': return v == null || v === '' ? null : String(v);
      case 'assignees': return [...new Set((Array.isArray(v) ? v : []).map(String))];
      default:         return v;
    }
  }

  /* Formularfelder, deren Berührung gemerkt wird (Schlüssel im Patch → Selektor). */
  const BERUEHRT_FELDER = {
    title: '#modal-title', labels: '#modal-labels', dueDate: '#modal-due-date', startDate: '#modal-start-date',
    dueTime: '#modal-due-time', startTime: '#modal-start-time', qa: '#modal-qa', critical: '#modal-critical',
    blocked: '#modal-blocked', assignees: '#modal-assignees', owner: '#modal-owner', project_id: '#modal-project',
  };
  const beruehrt = key => !!BERUEHRT_FELDER[key] && overlay?.querySelector(BERUEHRT_FELDER[key])?.dataset.beruehrt === '1';

  const alsMenge = a => (Array.isArray(a) ? [...new Set(a.map(String))].sort() : a);

  /** true, wenn der Patch-Wert vom Öffnungsstand der Karte abweicht. */
  function feldGeaendert(key, patchRoh) {
    const def      = PATCH_FIELD_DEFAULTS[key];
    const patchVal = vergleichsWert(key, patchRoh ?? def);
    const cardVal  = vergleichsWert(key, currentCard?.[key] ?? def);
    if (stabilJson(patchVal) === stabilJson(cardVal)) return false;
    if (beruehrt(key)) return true;
    const norm = vergleichsWert(key, formularNormalform(key, currentCard) ?? def);
    if (stabilJson(patchVal) === stabilJson(norm)) return false;
    /* Assignees: die Auswahlliste kennt keine Reihenfolge. */
    return key === 'assignees' ? stabilJson(alsMenge(patchVal)) !== stabilJson(alsMenge(cardVal)) : true;
  }

  function nurTatsaechlichGeaenderte(rohPatch, basis) {
    const patch = {};
    if (!rohPatch) return patch;
    for (const key of Object.keys(rohPatch)) {
      if (LIVE_PERSISTED_PATCH_KEYS.has(key)) { patch[key] = rohPatch[key]; continue; }
      if (key === 'comments') continue;
      if (feldGeaendert(key, rohPatch[key])) patch[key] = rohPatch[key];
    }
    /* ADR-0138 §1 / M-3: Wer das Datum leert, leert die Uhrzeit mit — auch eine
       fremde, die das Formular nicht anzeigt. */
    for (const [d, t] of [['dueDate', 'dueTime'], ['startDate', 'startTime']]) {
      if (d in patch && patch[d] == null && currentCard?.[t] != null) patch[t] = null;
    }
    if ('comments' in rohPatch && pendingComments.length > 0) {
      patch.comments = [...(basis?.comments ?? []), ...pendingComments];
    }
    return patch;
  }

  /* Karte #463 (F-752-Folgekarte): Vier Schliesswege umgingen bislang jede
     Speicherung — X-Knopf, Backdrop-Klick, ESC (Titelfeld UND onGlobalKey),
     dazu Swipe-to-Dismiss auf Mobil. Team-Entscheid: Auto-Save statt
     Rückfrage-Dialog — Konsistenz mit dem Rest des Boards (Drag&Drop, Flags,
     Spalten-Wechsel speichern auch sofort), und die Fehlerrichtung
     "versehentlich gespeichert" ist reversibel, "versehentlich verworfen"
     nicht (bei einem Board ohne Undo). Der Abbrechen-Button bleibt bewusst
     aussen vor (siehe cancelBtn-Listener) — er ist die einzige EXPLIZITE
     Verwerfen-Aktion im Modal. */
  function closeWithAutosave() {
    if (!overlay) return;
    if (!currentCard || !isDirty()) { close(); return; }
    saveAndClose();
    /* saveAndClose() bricht ohne zu speichern UND ohne zu schliessen ab,
       wenn der Titel leer ist (Validierung dort). Das darf einen ambigen
       Schliessweg (Backdrop/X/ESC/Swipe) nicht wirkungslos machen — der
       Nutzer stünde sonst vor einem Modal, das auf keinen dieser Wege mehr
       reagiert. Für diesen einen Fall bleibt es bei der alten Verwerfen-
       Semantik: hart schliessen, ohne zu speichern. Erkennung: close()
       setzt currentCard = null; ist es nach saveAndClose() noch gesetzt,
       hat der Titel-Guard gegriffen und close() wurde nicht erreicht. */
    if (currentCard) close();
  }

  /* Karte #463: beforeunload warnt NUR, wenn tatsächlich etwas ungespeichert
     ist — ein beforeunload, das immer warnt, ist eine Plage und wird von
     Nutzern weggeklickt bzw. von Browsern zunehmend ignoriert. Ist das Modal
     gar nicht offen, ist currentCard null und isDirty() liefert false —
     kein Extra-Guard auf `modal-overlay--open` nötig. Asynchrones Speichern
     ist beim Tab-Schliessen nicht mehr möglich, deshalb hier bewusst KEIN
     Auto-Save wie bei closeWithAutosave() — die Browser-eigene "Änderungen
     verwerfen?"-Warnung ist der einzig verbliebene Mechanismus. */
  function onBeforeUnload(e) {
    if (!isDirty()) return;
    e.preventDefault();
    e.returnValue = '';
  }

  function deleteAndClose() {
    if (!currentCard) return;
    if (!confirm(I18n.t('card.delete-confirm', currentCard.title))) return;
    EventBus.emit('card:deleted', currentCard.id);
    close();
  }

  /* Karte #60: Archiv-Toggle — archived-Flag umschalten und speichern.
     F-158/ADR-0036: Sprint-Meta-Karten dürfen nicht archiviert werden — der
     Archiv-Button wird für sie zwar per updateMetaCardMode() versteckt, aber
     ein Guard, der nur in der UI sitzt, ist keiner (analog main.js zum
     Swimlane-Guard, ADR-0117 §5): ein programmatischer Aufruf (Test, Skript,
     künftiger Shortcut) würde sonst archived:true auf einer Meta-Karte
     setzen. Dieselbe Prüfung wie an den beiden bereits geschlossenen Pfaden
     `col:clear` (F-153) und `col:archive-done` (F-364) in main.js — kein
     dritter Guard-Stil.

     Der Guard greift NUR in Richtung „archivieren" (Sprint-60-Kritiker):
     `archiveAndClose` ist ein Toggle, und eine bedingungslose Sperre haette
     den Rueckweg mitzugemauert. Eine Meta-Karte kann `archived:true` auch
     ohne diesen Pfad tragen — aus einem importierten Board oder aus einer
     `card.field`-Op (ADR-0067; `CARD_FIELD_BLOCKLIST` in ops.js kennt nur
     `id`/`displayId`), und `renderer.js` filtert archivierte Karten ohne
     Meta-Ausnahme aus der Spalte: die Sprint-Abschluss-Karte samt Recap
     waere unsichtbar. Zurueckgeholt wird sie von der zweiten Haelfte des
     Fixes — `migrateSprintMeta` in storage.js raeumt das Flag beim naechsten
     Board-Load weg. Dieser Guard haelt hier nur den Weg offen, statt ihn zu
     verriegeln; ueber die UI ist er ohnehin nicht erreichbar, weil
     `updateMetaCardMode()` den Button bei Meta-Karten auf `display:none`
     setzt. Das ist Defense-in-depth, keine Bedienfunktion (Verify-Lauf V-6:
     die erste Fassung dieses Kommentars behauptete, der Rueckweg liefe ueber
     den Button — er lief nie ueber ihn).

     Geprueft wird `base`, nicht `currentCard` (Verify-Lauf V-5): Der Toggle
     rechnet mit `base.archived`, und `base` ist der frische Stand aus dem
     State. Waere `currentCard` inzwischen veraltet — Modal offen, waehrend
     `migrateSprintMeta` die Karte heilt —, haette der Guard auf `true`
     geprueft und der Toggle auf `false` gerechnet: Ergebnis `archived:true`
     auf einer Meta-Karte, also genau der Zustand, den ADR-0036 ausschliesst.
     Guard und Mutation muessen dasselbe Objekt lesen. */
  function archiveAndClose() {
    if (!currentCard) return;
    const freshSelf = findCardById(currentCard.id);
    const base = freshSelf?.card ?? currentCard;
    if (base.kind === 'sprint-meta' && !base.archived) return;

    /* F-915 (Nebenbefund aus Karte #463, Orchestrator-Entscheid "Weg 1"):
       Archivieren ist keine verwerfende Aktion — das ist deleteAndClose()
       (dort ist der Verlust der Bearbeitung Absicht). archiveAndClose()
       schliesst das Modal (close() unten) und zählt damit zu den
       Schliesswegen, für die seit #463 die Zusage gilt: "Schliessen
       verliert nichts". Bislang baute diese Funktion ihr card:updated NUR
       aus dem State (`base`) — kein einziges Formularfeld floss ein, eine
       offene Titel-/Beschreibungs-Änderung ging beim Klick auf Archivieren
       wortlos verloren. Jetzt dieselbe computeFormPatch()-Basis wie
       saveAndClose()/closeWithAutosave(): das Formular wird mitgespeichert,
       nicht verworfen — samt der noch nicht abgeschickten Kommentare, die
       computeFormPatch() ohnehin mitführt.

       ZUM LEEREN TITEL VERHÄLT SICH DIESE FUNKTION BEWUSST ANDERS ALS IHRE
       BEIDEN NACHBARN — das ist keine Nachlässigkeit, sondern die einzige
       Wahl, die hier keinen neuen Schaden anrichtet:
         - saveAndClose() BRICHT AB (`if (!patch.title) return;`) und
           schliesst nicht — der Nutzer soll den Titel korrigieren.
         - closeWithAutosave() VERWIRFT dann alles und schliesst hart, weil
           ein Schliessweg, der auf nichts mehr reagiert, schlimmer wäre.
         - Hier wird nur der EINE ungültige Patch-Schlüssel verworfen, der
           Rest gespeichert, und archiviert wird trotzdem.
       Abbrechen wäre falsch, weil der Nutzer auf „Archivieren" geklickt hat
       und nicht auf „Speichern" — eine Zustandsänderung an einer leeren
       Titelzeile scheitern zu lassen, wäre unerklärlich. Alles zu verwerfen
       wäre ebenfalls falsch, weil genau das der Befund F-915 ist. Bleibt:
       den Titel stehen lassen, wie er war.

       F-918: `base` war hier schon der frische Stand (das war der V-5-Befund
       oben), der PATCH aber nicht — er trug alle Formularfelder mit den
       Werten vom Öffnen und hat den frischen Stand darunter wieder
       plattgemacht. Archivieren ist ein Schliessweg wie jeder andere, also
       gilt hier dieselbe Reduktion auf die tatsächlich geänderten Felder.
       Der Titel-Guard bleibt und trifft jetzt nur noch den Fall, dass der
       Nutzer den Titel im Formular selbst geleert hat — ein unangetasteter
       Titel steht gar nicht mehr im Patch. */
    const patch = nurTatsaechlichGeaenderte(computeFormPatch(), base);
    if ('title' in patch && !patch.title) delete patch.title;

    EventBus.emit('card:updated', {
      ...base,
      ...patch,
      archived: !base.archived,
      updated:  new Date().toISOString().slice(0, 10),
    });
    close();
  }

  /* ADR-0040: Aktuelle Karte als Template speichern — übernimmt Skelett-Felder (Karte #65) */
  function saveAsTemplate() {
    if (!currentCard) return;
    const { desc } = collectRawText();
    const title       = overlay?.querySelector('#modal-title')?.value?.trim() || currentCard.title;
    const labels = formLabels();
    const owner       = overlay?.querySelector('#modal-owner')?.value       ?? currentCard.owner ?? '';
    const complexity  = overlay?.querySelector('#modal-complexity') ? leseComplexity() : (currentCard.complexity ?? '');
    const effort      = leseEffort();

    EventBus.emit('template:save', {
      title, description: desc, labels, owner, complexity, effort,
    });
    /* Toast anzeigen */
    EventBus.emit('template:saved-toast', title);
  }

  function onGlobalKey(e) {
    if (e.key !== 'Escape' || !overlay || !overlay.classList.contains('modal-overlay--open')) return;
    /* F-752: Öffnet aus dem Karten-Modal heraus ein weiterer Dialog (PII-
       Preview, E-Mail — beide über modal-shell.js/createModal(), das
       dieselbe Klasse `modal-overlay--open` verwendet, seit ADR-0118 auch
       Familie A/B), hängt an document EIN WEITERER keydown-Listener, der
       ESC ebenfalls unconditional behandelt. Registrierungsreihenfolge
       (dieser Listener zuerst, siehe open() oben) heisst: dieser Handler
       liefe IMMER zuerst und würde das Karten-Modal ungefragt mitschliessen
       — close() speichert nicht, ungespeicherte Titel-/Beschreibungs-
       änderungen wären weg. `stopPropagation()`/`stopImmediatePropagation()`
       im oberen Dialog kämen zu spät (dieser Listener hätte schon
       gefeuert), also der Guard hier: Ist irgend ein ANDERES Overlay mit
       derselben Klasse gerade offen, gehört der ESC-Druck dem oberen
       Dialog — das Karten-Modal bleibt unangetastet. Absichtlich per
       Identitätsvergleich (`el !== overlay`) statt einer Klassen-
       Ausnahmeliste: neue Konsumenten von modal-shell.js brauchen dann
       keine Anpassung hier. */
    /* Kritiker-Befund Sprint 63: Hier stand eine EXISTENZ-Prüfung („schliesse
       nicht, wenn irgendein ANDERES Overlay offen ist"). Sie trug, solange das
       Karten-Modal immer das unterste Overlay war — genau das steht als
       Begründung darüber. Seit Karte #465 beantwortet die Modal-Shell dieselbe
       Frage über eine STAPEL-Ordnung, und die beiden Antworten konnten
       auseinanderfallen: Wird ein Sidebar-Dialog geöffnet und DANN eine Karte
       (per `j`/`Enter` — Shell-Dialoge setzen keinen Shortcut-Kontext, die
       Tastatur erreicht das Board also weiterhin), liegt das Karten-Modal
       oben. Die Shell trat dann zurück („ich bin nicht oberstes") und diese
       Prüfung ebenfalls („ein anderes Overlay ist offen") — ESC schloss GAR
       NICHTS, in beide Richtungen eine Sackgasse.
       Jetzt fragt auch das Karten-Modal die Stapel-Ordnung, und zwar über
       DIESELBE Funktion wie die Shell: mehrere Prüfungen, EINE Wahrheit
       (Fehlerklasse 65). Am F-752-Fall ändert sich nichts — ein Dialog, der
       aus dem offenen Karten-Modal heraus aufgeht, steht später im DOM und
       gewinnt weiterhin. */
    if (ermittleOberstesOffenesOverlay(e) !== overlay) return;
    /* Karte #463: seit hier Auto-Save statt stillem Verwerfen — der Guard
       oben bleibt unverändert nötig (er schützt weiterhin davor, dass ESC
       den falschen Dialog trifft), nur das Schliessen selbst speichert jetzt
       ungesicherte Änderungen statt sie zu verwerfen. */
    closeWithAutosave();
  }

  EventBus.on('card:edit', open);

  /* Overlay-Cache invalidieren damit nächster Öffnen mit neuer Sprache rebuilt (F-67) */
  EventBus.on('config:language-changed', () => {
    if (overlay) { overlay.remove(); overlay = null; }
  });

  return { open, close };
})();
