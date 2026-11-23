/* modal-shell.js — Gemeinsame Verhaltens-/Struktur-Schicht für die
 * Server-Einstellungsdialoge (#348, ADR-0118).
 *
 * Entscheid vom 2026-08-19 (Karte #348): technisch zusammenführen,
 * Optik fast gleich lassen. Kein Redesign — die Dialoge bekommen eine
 * gemeinsame GRUNDLAGE statt sieben eigener Varianten; sichtbar ändern sich
 * nur Kleinigkeiten (Abstände, Kopfhöhen, Button-Größen).
 *
 * Bundle-Prinzip (siehe ADR-0118): `.modal-overlay` / `.modal-overlay--open` /
 * `.modal` / `.modal-header-row` / `.modal-dialog-title` / `.btn-modal-close` /
 * `.modal-body` sind Klassen aus src/style.css, die das Karten-Modal
 * (src/modal.js) ohnehin braucht — sie sind in JEDEM Build vorhanden, auch im
 * Slim-Template. Eine Shell, die auf diesen Klassen aufsetzt, kostet dem
 * Solo-Bundle KEIN einziges CSS-Byte. Nur die zusätzliche "Settings-Variante"
 * (oben ausgerichtetes, scrollbares Overlay statt zentriert, höherer
 * z-index) ist neues, minifiziertes CSS — und liegt hier, an EINER Stelle,
 * nicht dreimal in den Konsument-Dateien (Klasse-31-Fehlerklasse).
 *
 * Was diese Shell NICHT tut:
 *  - Sie färbt nichts um. Farben/Schriftgrößen der dialogspezifischen
 *    Inhalte (cp-*, notif-*, admin-*, llm-*, pii-*, email-*) bleiben in den
 *    jeweiligen Modulen — das ist ausdrücklich nicht Teil dieser Karte.
 *  - Sie kennt kein Tabs-/Sektionen-Konzept — das bleibt Sache des Aufrufers
 *    (siehe admin-dialog.js).
 *  - Sie ist NICHT hinter IS_SERVER_BUILD gegated (wie focus-trap.js): jeder
 *    Konsument entscheidet selbst, ob er server-only ist. Ein ungegateter
 *    Import dieses Moduls in einem DCE'ten Konsumenten verschwindet mit
 *    dessen Modul automatisch mit (F-428-Analogie, siehe focus-trap.js).
 */

import { focusInitial, trapTabKey, captureFocus, restoreFocus } from './focus-trap.js';

let _settingsStylesInjected = false;

/* F-889 (Karte #465): Welches `.modal-overlay--open` gerade OBEN im Stapel
   liegt (siehe ausführliche Begründung bei onKey unten). Modul-weite
   Hilfsfunktion + Cache, NICHT pro createModal()-Instanz, aus einem Grund,
   der sich erst beim Testen zeigte: Sind zwei Instanzen gleichzeitig offen,
   hängen BEIDE einen eigenen keydown-Listener an `document`. Der Browser
   liefert dasselbe Event-Objekt nacheinander an beide (Registrierungs-
   reihenfolge). Ruft die zuerst gefeuerte Instanz `close()` synchron auf
   (entfernt ihre eigene `--open`-Klasse), sähe die ZWEITE Instanz beim
   Nachfragen "ich bin jetzt die einzige offene" — und würde sich fälschlich
   ebenfalls schliessen (in der Gegenprobe nachgemessen: ohne den Cache
   schliesst der Settings-Tier-Test unten den Basis-Dialog mit). Der Cache
   friert das Ergebnis EINMAL PRO EVENT ein (WeakMap, Event-Objekt als
   Schlüssel) — wer auch immer zuerst nachfragt, entscheidet für alle
   Listener, die auf dasselbe Event reagieren; kein späterer Listener sieht
   einen bereits durch einen früheren Listener veränderten Zwischenzustand.
   Der Eintrag verschwindet mit dem Event-Objekt (kein manuelles Aufräumen
   nötig). */
const _obersterOverlayCache = new WeakMap();

function _istSettingsTier(el) {
  return el.classList.contains('modal-overlay--settings');
}

/* Exportiert seit dem Sprint-63-Abschluss: Das Karten-Modal (`src/modal.js`)
   nimmt an derselben `.modal-overlay--open`-Menge teil und muss dieselbe Frage
   nach derselben Regel beantworten. Zwei eigene Antworten auf „wer liegt
   oben?" waren genau der Defekt — beide Guards traten zurück, ESC schloss gar
   nichts. Verteidigung in der Tiefe heisst mehrere Prüfungen, EINE Wahrheit. */
export function ermittleOberstesOffenesOverlay(e) { return _ermittleOberstesOffenesOverlay(e); }

function _ermittleOberstesOffenesOverlay(e) {
  if (_obersterOverlayCache.has(e)) return _obersterOverlayCache.get(e);
  const offene = document.querySelectorAll('.modal-overlay--open');
  let oberstes = null;
  for (const el of offene) {
    /* Settings-Tier (z-index 11000) schlägt Basis-Tier (100) IMMER; bei
       gleichem Tier gewinnt die DOM-Reihenfolge — der spätere
       Geschwisterknoten liegt bei gleichem z-index visuell obenauf (reine
       CSS-Stapelregel). */
    if (oberstes === null || _istSettingsTier(el) || !_istSettingsTier(oberstes)) oberstes = el;
  }
  _obersterOverlayCache.set(e, oberstes);
  return oberstes;
}

/* Settings-Variante (#348, Familie B: bisher `.notif-overlay`/`.cp-overlay`):
 * oben ausgerichtet + scrollbar statt zentriert (viele Zeilen/Formulare),
 * höherer z-index als das Karten-Modal (11000 vs. 100 — Familie B lag schon
 * vorher auf 11000, siehe ADR-0118 zur Begründung). Panel-Breite als
 * gemeinsamer Mittelwert der bisherigen 520px/560px-Minima (#348-Vorarbeit:
 * das ist eine der ausdrücklich erlaubten "Kleinigkeiten"). */
function _ensureSettingsVariantStyles() {
  if (_settingsStylesInjected) return;
  _settingsStylesInjected = true;
  const style = document.createElement('style');
  style.setAttribute('data-aboard', 'modal-shell-settings-variant');
  style.textContent = '.modal-overlay--settings{z-index:11000;align-items:flex-start;justify-content:center;overflow-y:auto;padding:40px 16px}.modal--settings{min-width:540px;max-width:760px;width:100%}';
  document.head.appendChild(style);
}

/**
 * Baut Overlay + Panel + Header (Titel + Close-Button) + Body-Container nach
 * dem Karten-Modal-Muster (`.modal-overlay`/`.modal`/`.modal-header-row`/
 * `.modal-dialog-title`/`.btn-modal-close`/`.modal-body` — alle aus
 * src/style.css) und verdrahtet ESC, Klick-außerhalb, Focus-Trap und
 * Focus-Restore einheitlich (holt F-347/A11y für jeden Konsumenten nebenbei
 * mit, siehe focus-trap.js).
 *
 * @param {object} opts
 * @param {string} opts.extraOverlayClass  dialogspezifische Klasse auf dem
 *   Overlay (z.B. 'cp-overlay') — für evtl. weitere dialogspezifische
 *   CSS-Overrides UND als stabiler Selektor für Tests.
 * @param {string} opts.extraPanelClass    dialogspezifische Klasse auf dem
 *   Panel (z.B. 'cp-panel').
 * @param {string} [opts.extraBodyClass]   optionale dialogspezifische Klasse
 *   auf dem Body-Container (z.B. 'admin-body') — NUR für Bestandsschutz
 *   bestehender Tests/Selektoren gedacht, die den alten Klassennamen direkt
 *   abfragen (Familie A: admin-dialog.js, llm-keys-dialog.js). Neue
 *   Konsumenten sollten ohne auskommen und ausschliesslich gegen `.modal-body`
 *   selektieren.
 * @param {string} opts.titleId            id für <h2> + aria-labelledby.
 * @param {string} opts.closeAriaLabel     aria-label des Close-Buttons.
 * @param {'settings'|null} [opts.variant] 'settings' = oben ausgerichtetes,
 *   scrollbares Overlay mit z-index 11000 (bisherige Familie-B-Optik).
 *   Ohne Angabe: zentriertes Overlay wie das Karten-Modal (z-index 100).
 * @param {boolean} [opts.footer=false]    true = zusätzlicher Footer-Container
 *   (`.modal-actions`, aus style.css — das Karten-Modal nutzt dieselbe
 *   Klasse für seine Abbrechen/Speichern-Leiste) unterhalb des Body,
 *   zurückgegeben als `footer`. Für Dialoge mit eigener Button-Leiste
 *   ausserhalb des scrollbaren Bodys (z.B. llm-pii.js).
 * @param {boolean} [opts.closeOnOverlayClick=true] false = Klick auf den
 *   Overlay-Hintergrund schliesst NICHT. Für Dialoge, bei denen ein
 *   versehentlicher Klick eine in Arbeit befindliche Entscheidung verwerfen
 *   würde (llm-pii.js: PII-Redaktionsentscheidungen vor dem Senden).
 * @param {Function} [opts.onRequestClose] Wird — falls gesetzt — STATT der
 *   eingebauten `close()` aufgerufen, wenn ESC gedrückt, der Close-Button
 *   geklickt oder (bei `closeOnOverlayClick`) der Overlay-Hintergrund
 *   geklickt wird. Für Dialoge, deren "Schliessen" mehr ist als Sichtbarkeit
 *   umschalten — llm-pii.js ist kein persistentes Singleton wie die übrigen
 *   Einstellungsdialoge, sondern wird pro `openPreviewModal()`-Aufruf neu gebaut und
 *   bei Abschluss aus dem DOM entfernt; ein Promise muss dabei aufgelöst
 *   werden. Der Aufrufer bleibt dafür verantwortlich, irgendwann die
 *   eingebaute `close()` selbst aufzurufen (Fokus-Restore, Listener-Abbau).
 * @returns {{overlay:HTMLElement,panel:HTMLElement,header:HTMLElement,
 *   titleEl:HTMLElement,closeBtn:HTMLElement,body:HTMLElement,
 *   footer:HTMLElement|null,open:Function,close:Function,
 *   focusAfterRender:Function,setTitle:Function}}
 */
export function createModal(opts) {
  const {
    extraOverlayClass, extraPanelClass, extraBodyClass, titleId, closeAriaLabel, variant,
    footer = false, closeOnOverlayClick = true, onRequestClose,
  } = opts;

  if (variant === 'settings') _ensureSettingsVariantStyles();

  const overlay = document.createElement('div');
  overlay.className = variant === 'settings'
    ? `modal-overlay modal-overlay--settings ${extraOverlayClass}`
    : `modal-overlay ${extraOverlayClass}`;
  overlay.setAttribute('aria-hidden', 'true');

  const panel = document.createElement('div');
  panel.className = variant === 'settings'
    ? `modal modal--settings ${extraPanelClass}`
    : `modal ${extraPanelClass}`;
  panel.setAttribute('role', 'dialog');
  panel.setAttribute('aria-modal', 'true');
  panel.setAttribute('aria-labelledby', titleId);

  const header = document.createElement('div');
  header.className = 'modal-header-row';
  const titleEl = document.createElement('h2');
  titleEl.className = 'modal-dialog-title';
  titleEl.id = titleId;
  header.appendChild(titleEl);

  const closeBtn = document.createElement('button');
  closeBtn.className = 'btn-modal-close';
  closeBtn.type = 'button';
  closeBtn.textContent = '✕';
  closeBtn.setAttribute('aria-label', closeAriaLabel);
  header.appendChild(closeBtn);
  panel.appendChild(header);

  const body = document.createElement('div');
  body.className = extraBodyClass ? `modal-body ${extraBodyClass}` : 'modal-body';
  panel.appendChild(body);

  let footerEl = null;
  if (footer) {
    footerEl = document.createElement('div');
    footerEl.className = 'modal-actions';
    panel.appendChild(footerEl);
  }

  overlay.appendChild(panel);

  let prevFocus = null;
  let keyHandler = null;

  function close() {
    overlay.classList.remove('modal-overlay--open');
    overlay.setAttribute('aria-hidden', 'true');
    if (keyHandler) {
      document.removeEventListener('keydown', keyHandler);
      keyHandler = null;
    }
    restoreFocus(prevFocus);
    prevFocus = null;
  }

  /* Was ESC/Close-Button/Klick-außerhalb tatsächlich auslösen — per Default
     die eingebaute close(), überschreibbar via onRequestClose (siehe
     JSDoc). */
  const requestClose = typeof onRequestClose === 'function' ? onRequestClose : close;

  /* F-889 (Karte #465): Zwei gleichzeitig offene Shell-Instanzen (heute nur
     über den EventBus erreichbar — siehe configdialog.js#openPartition
     modalUser/modalBoard, ADR-0065 — nicht über die UI, deren Overlay den
     Viewport deckt) dürfen ESC nicht beide behandeln, sonst schliessen beide
     auf einen Tastendruck (jede Instanz hängt ihren eigenen Listener an
     `document`).
     Der wörtlich aus dem Karten-Modal übernommene Guard (F-752,
     modal.js#onGlobalKey: "schliesse nicht, wenn irgendein ANDERES Overlay
     offen ist") wäre hier ein Regress: Das Karten-Modal ist per Bauart IMMER
     das UNTERSTE Overlay (z-index 100, siehe style.css .modal-overlay) —
     ein reiner Existenz-Check trägt dort, weil "ein anderes Overlay ist
     offen" dort immer "und das liegt darüber" bedeutet. Diese Shell dagegen
     kann sowohl unter als auch ÜBER einem anderen Overlay liegen: die
     Settings-Variante liegt bei z-index 11000, der Default bei 100 wie das
     Karten-Modal (ADR-0118 §4). Ein Existenz-Check würde den oberen Dialog
     am Schliessen hindern, sobald irgendein zweiter (tieferer) Dialog offen
     ist — genau das Verhalten, das F-752 gerade verhindern sollte.
     Der Guard hier prüft deshalb STAPEL-ORDNUNG statt Existenz: "schliesse
     nur, wenn ich das oberste offene `.modal-overlay--open` bin" (Ermittlung
     + Cache-Begründung: `_ermittleOberstesOffenesOverlay` oben). Geprüft
     gegen alle acht Konsumenten von createModal() (grep 'createModal' in
     src/): email-dialog.js, llm-pii.js, admin-dialog.js, llm-keys-dialog.js,
     configdialog.js (Basis-Tier, z-index 100 — deckt auch den Fall zweier
     gleichzeitiger Instanzen ab, modalUser/modalBoard) sowie
     notifications-dialog.js, projects-view.js, clients-view.js
     (Settings-Tier, z-index 11000). Der Tier-Vergleich kennt keine
     Konsumenten-Liste — ein neunter Konsument (Basis oder Settings) ist
     automatisch mit abgedeckt. */
  function onKey(e) {
    if (e.key === 'Escape') {
      /* Erst prüfen, DANN abfangen (Kritiker-Befund Sprint 63): Ein
         nicht-oberster Dialog schluckte sonst die Vorgabeaktion von ESC
         (Vollbild verlassen, natives <dialog>, IME-Komposition abbrechen),
         obwohl er das Ereignis anschliessend unbehandelt weiterreicht. */
      if (_ermittleOberstesOffenesOverlay(e) !== overlay) return; /* F-889: ESC gehört dem oberen Dialog */
      e.preventDefault();
      requestClose();
      return;
    }
    trapTabKey(e, panel);
  }

  closeBtn.addEventListener('click', () => requestClose());
  if (closeOnOverlayClick) {
    overlay.addEventListener('click', (e) => { if (e.target === overlay) requestClose(); });
  }

  /** Öffnet den Dialog (mountet ihn beim ersten Aufruf, hängt ihn bei jedem
   * weiteren Aufruf ans Ende von `document.body` um). Initial-Focus NICHT
   * hier gesetzt — Inhalt ist beim Öffnen oft noch nicht gerendert
   * (async refresh()); Aufrufer ruft danach `focusAfterRender()`. */
  function open() {
    /* F-889-Nachlese (Verify-Lauf, Karte #465): `appendChild` auf einen
       bereits an DIESEN Parent angehängten Knoten verschiebt ihn per
       DOM-Spezifikation ans Ende — keine Duplizierung, keine Neuerzeugung.
       Bewusst OHNE den früheren `if (!overlay.parentNode)`-Guard: Die
       Stapel-Regel in `onKey`/`_ermittleOberstesOffenesOverlay` setzt
       voraus, dass die DOM-Reihenfolge die AKTUELLE Öffnungsreihenfolge
       widerspiegelt — nicht nur die des allerersten Öffnens. Ohne diese
       Zeile bleibt eine Instanz, die einmal geöffnet+geschlossen und erst
       NACH einem später erstmals geöffneten anderen Overlay erneut geöffnet
       wird, an ihrer alten (zu frühen) DOM-Position hängen: Guard und
       visuelle Stapelung fallen auseinander, ESC schliesst dann gar nichts
       mehr (gemessen: Shell einmal auf/zu, danach Karten-Modal erstmals
       geöffnet, danach Shell erneut geöffnet — vor dieser Zeile blieben
       nach ESC beide offen, siehe tests/modal-shell.test.js
       'F-889-Nachlese').
       Geprüft: Der Umzug passiert hier, WÄHREND das Overlay noch geschlossen
       ist (keine `--open`-Klasse, `aria-hidden` noch nicht auf 'false') —
       weder der Fokus-Pfad (captureFocus() darunter erfasst den AKTUELLEN
       `document.activeElement` der aufrufenden Seite, nicht irgendetwas in
       diesem noch unsichtbaren Overlay) noch der erzwungene Reflow für die
       F-754-Einblend-Animation (siehe `void overlay.offsetHeight` unten,
       läuft danach und liest den frischen, geschlossenen Ausgangszustand an
       der NEUEN Position) sind davon betroffen. Ein Umzug WÄHREND einer
       laufenden Schliessen-Transition (Reopen mitten in den 200ms von
       .modal-overlay) bleibt optisch unauffällig: `opacity`/`visibility`
       sind Eigenschaften des Elements selbst, nicht von der Position unter
       `body` abgeleitet — dieselbe Transition läuft an der neuen Position
       unverändert weiter. */
    document.body.appendChild(overlay);
    prevFocus = captureFocus();
    overlay.setAttribute('aria-hidden', 'false');
    /* F-754: Einblend-Animation — beim ALLERERSTEN Öffnen hatte der Browser
       den Ausgangszustand (opacity:0, visibility:hidden, scale(.94) —
       siehe style.css .modal-overlay) nie tatsächlich gerendert, wenn die
       '--open'-Klasse im SELBEN Tick gesetzt wird: die CSS-Transition
       greift nicht, der Dialog springt hart auf (ab dem zweiten Öffnen
       animiert er, weil der geschlossene Zustand dann schon einmal
       gerendert war).
       Karten-Modal (modal.js:2676) und Einstellungen (configdialog.js:1012)
       lösen das mit `requestAnimationFrame(() => classList.add(...))` —
       bewusst NICHT hier übernommen: das wäre asynchron (in diesem Repo per
       happy-dom über `setImmediate` abgebildet, in echten Browsern ein
       eigener Rendering-Zeitpunkt), und ALLE sieben Konsumenten dieser
       Shell rufen `focusAfterRender()` über eine bereits aufgelöste
       Promise-`.then()`-Kette auf (Microtask — sieh z.B.
       notifications-dialog.js:265, admin-dialog.js:846). Microtasks laufen
       vor jedem Makrotask/Rendering-Schritt vollständig durch — ein rAF
       hier würde also praktisch IMMER NACH `focusAfterRender()` feuern.
       Solange die '--open'-Klasse fehlt, ist `visibility` noch `hidden`
       (Basiszustand), und ein Element in einem `visibility:hidden`-Vorfahren
       ist per Fokus-Algorithmus NICHT fokussierbar — `focus()` wäre ein
       stiller No-Op. Das rAF-Muster 1:1 übernommen hätte also den
       Auto-Fokus alle sieben Dialoge bei JEDEM Öffnen gebrochen, nicht nur
       beim ersten (ein Bug schlimmer als der, den es beheben soll).
       Stattdessen: erzwungener Reflow (`offsetHeight` lesen) VOR dem
       Klassenwechsel — das committet den geschlossenen Zustand synchron,
       genau wie das rAF es täte, aber ohne jede Verzögerung. Derselbe
       visuelle Effekt, kein Nebenläufigkeits-Risiko. (Derselbe Fokus-Race
       besteht vermutlich bereits latent in modal.js/configdialog.js selbst
       — außerhalb des Umfangs dieser Karte, siehe Finding-Vorschlag.) */
    void overlay.offsetHeight;
    overlay.classList.add('modal-overlay--open');
    keyHandler = onKey;
    document.addEventListener('keydown', keyHandler);
  }

  /** Nach dem (ggf. asynchronen) Rendern des Inhalts aufrufen. */
  function focusAfterRender(fallback) {
    focusInitial(panel, fallback || closeBtn);
  }

  function setTitle(text) {
    titleEl.textContent = text;
  }

  return { overlay, panel, header, titleEl, closeBtn, body, footer: footerEl, open, close, focusAfterRender, setTitle };
}
