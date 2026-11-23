import { EventBus }  from './eventbus.js';
import { Markdown }   from './markdown.js';
import { I18n }       from './i18n.js';
import { Filter, vonAgentNachAussen } from './filter.js';
import { Search }     from './search.js';
import { Sync }       from './sync.js';
import { COMPACT_OP_THRESHOLD } from './inbox.js';
import { Storage, istMetaKarteLeer } from './storage.js';
import { State }      from './state.js';
import { Config }     from './config.js';
import { cardsIcon }             from './icons.js';
import logoSvg        from '../assets/aBoard-icon.svg?raw';
import { DragDrop }   from './dragdrop.js';
import { openBlockers, isDoneColumn } from './links.js';
import { Metrics }    from './metrics.js';
import { Model }      from './model.js';
import { FINDING_TYPEN, FINDING_PRIORITAETEN, FINDING_STATUS } from './finding.js';
/* Karte #450-Nacharbeit (Frontend-Kritiker Sprint 61): Anhänge laufen über
   das Storage-Plugin. Der direkte Import hier war die zweite Umgehung, die
   der erste Umbau übersehen hat — und die einzige mit sofort sichtbarer
   Wirkung: Ein Karten-Cover, das per REST auf dem Server liegt, hat kein
   `att.data`, und `attachments.js attachmentUrl` liefert dafür `null`. */
import { createFsaAttachmentBackend } from './storage-plugin.js';
/* Karte #467: Das Cover-Thumbnail ist eine reine Datenoperation am
   Anhang-Objekt und geht am Storage-Plugin vorbei — es liegt im Board-State,
   den der Renderer ohnehin hat, und braucht deshalb kein Backend. */
import { thumbnailUrl, istSvgMime } from './attachments.js';
import { BulkSelect } from './bulk-select.js';
import { StoragePlugin } from './storage-plugin.js';
import { resolveIdentity, buildAvatar, colorForLabel, initialOf, getContrastColor, faerbeLabel } from './identity.js';

/* Siehe Begründung beim Import oben. Fallback = dasselbe Backend, das
   Standalone- und Projekt-Plugin liefern. */
function anhangBackend() {
  return StoragePlugin.getActivePlugin()?.attachments?.() ?? createFsaAttachmentBackend();
}
import { Profile }       from './profile.js';
import { kandidatenSofort, ladeKandidaten, claudeGruppe, istClaudeKennung, CLAUDE_MODELLE } from './bearbeiter-kandidaten.js'; /* Karte #698, nur gegatet benutzt; CLAUDE_MODELLE ungegatet (#817) */
import { eigeneAwaitingSchluessel, awaitingOderNull } from './awaiting.js';
import { BoardMenu, dialogOffen, fremderFokus } from './board-menu.js';
import { claimZustand, istVertraulich } from './ops.js';   /* Karte #606/#778 — nur in gegateten Zweigen benutzt */
import { SchattenAussen } from './schatten-aussen.js';      /* Karte #825 — nur gegatet benutzt */
import { spiegelGebunden, rueckwegAn, ohneGebundene, markeWirkt } from './boardpaar.js'; /* Karte #827 E4, #831 — nur gegatet benutzt */
import { diffDays, todayISO } from './calendar/date-helpers.js';
import { dringlichkeit, heuteOrtszeit } from './dringlichkeit.js'; /* Karte #1004 */
import { ansagen } from './ansage.js'; /* F-2876: frisch erzeugte Banner über die dauerhafte Live-Region ansagen */

/* ADR-0072: Projektor-Build-Flag (Muster wie IS_SERVER_BUILD/ADR-0064).
   false → slim Public-Distributable: das Kompaktierungs-Banner (ADR-0071
   Achse 2D) entfällt per DCE; damit wird die letzte inbox.js-Importkante
   im Renderer tot → ops/inbox/diff tree-shaken (slim < 263000 B, ADR-0077). */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

/* #328: Header-Avatar (Karte #201/ADR-0059) ist server-only. Gate hier,
   damit esbuild den Block im slim Distributable per DCE eliminiert. */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');
/* Karte #826: Abzeichen der Spiegel (Karte und Lane-Kopf), nur gegatet benutzt. */
const SPIEGEL_ABZEICHEN_STIL = 'color:var(--text-muted);border:1px dashed var(--border);background:transparent';

export const Renderer = (() => {
  /* ── Swimlane-Collapse-State (Session) ───────────────────────────── */
  const collapsedSprints = new Set();
  let swimlaneInitialized = false;
  /* ADR-0117: welche archivierten Swimlanes schon einmal automatisch
     eingeklappt wurden. Ohne diesen Merker wuerde jeder Re-Render das
     Aufklappen des Nutzers sofort rueckgaengig machen. */
  const _archivEingeklappt = new Set();

  /* ── Spalten-Collapse-State (persistiert via Config, Karte #151) ──── */
  const collapsedColumns = new Set();
  let _collapsedColumnsLoaded = false;
  function loadCollapsedColumnsFromConfig() {
    if (_collapsedColumnsLoaded) return;
    const fromConfig = Config.get('collapsedColumns');
    if (Array.isArray(fromConfig)) fromConfig.forEach(k => collapsedColumns.add(k));
    _collapsedColumnsLoaded = true;
  }
  function persistCollapsedColumns() {
    Config.set('collapsedColumns', [...collapsedColumns]);
  }
  /* F-214: Bei Spalten-/Sprint-Löschung Stale-Keys aufräumen.
     matchFn(key) → true ⇒ Key entfernen. Persistiert nur wenn was geändert wurde. */
  function cleanupCollapsedColumns(matchFn) {
    let changed = false;
    for (const key of [...collapsedColumns]) {
      if (matchFn(key)) {
        collapsedColumns.delete(key);
        changed = true;
      }
    }
    if (changed) persistCollapsedColumns();
    return changed;
  }
  /* F-214, zweite Haelfte: `cleanupCollapsedColumns` haengt an den beiden
     lokalen Handlern `col:delete`/`sprint:delete`. Schluessel, die auf einem
     ANDEREN Weg verwaisen, raeumt niemand weg — eine Spalte, die ein fremder
     Akteur per Op loescht, ein Board-Import, oder schlicht Altbestand aus der
     Zeit vor dem Handler-Fix. Fuer die waechst der Config-Eintrag weiter
     monoton, genau wie das Finding es beschreibt.

     WARUM DAS HIER NICHT EINFACH „alles wegwerfen, was nicht ins Board passt"
     heisst: Die Config kennt das Board nicht. F-227 haelt ausdruecklich fest,
     dass `collapsedColumns` nur `sprintId:colId` speichert und ueber Boards
     hinweg gilt (deshalb purgt `board:reset` sie komplett). Ein Schluessel mit
     unbekannter sprintId ist deshalb NICHT beweisbar verwaist — er kann
     genauso gut zu einem anderen Board desselben Geraets gehoeren. Ihn zu
     loeschen waere kein Aufraeumen, sondern Verlust fremder UI-Einstellungen.

     Entfernt wird also nur, was nachweislich zu DIESEM Board gehoert und
     trotzdem ins Leere zeigt: sprintId existiert hier, colId nicht (mehr).
     Die verbleibende Luecke — verwaiste Schluessel geloeschter Sprints — ist
     mit dieser Datenstruktur nicht entscheidbar und bleibt bewusst offen;
     eine offen benannte Luecke ist besser als eine behauptete Abdeckung. Sie
     zu schliessen braucht eine Board-Zuordnung im Schluessel selbst (dann
     waere auch F-227 erledigt). */
  function pruneCollapsedColumns(board) {
    const sprintIds = new Set((board?.sprints || []).map(sp => sp?.id).filter(Boolean));
    const colIds    = new Set((board?.columnDefs || []).map(cd => cd?.id).filter(Boolean));
    /* Leeres/halb geladenes Board: nichts entscheiden, sonst raeumt ein
       Zwischenzustand echte Einstellungen weg. */
    if (sprintIds.size === 0 || colIds.size === 0) return false;
    return cleanupCollapsedColumns(key => {
      const trenner = key.indexOf(':');
      /* Formwidriger Schluessel (kein `sprintId:colId`) — kann von keiner
         Spalte stammen, also gefahrlos weg. */
      if (trenner < 1) return true;
      const sprintId = key.slice(0, trenner);
      const colId    = key.slice(trenner + 1);
      if (!sprintIds.has(sprintId)) return false;  /* fremdes Board — nicht anfassen */
      return !colIds.has(colId);
    });
  }
  /* F-223 (Karte #162): Beim Board-Swap (Server-Modus Phase 2) Module-Singletons
     zurücksetzen, sonst gelten die Collapse-Annahmen des vorherigen Boards weiter.
     collapsedColumns wird zusätzlich aus Config neu geladen — die Config ist
     Board-übergreifend, der Reload sortiert relevante Keys aktiv ein. */
  function resetSwimlaneState() {
    collapsedSprints.clear();
    _archivEingeklappt.clear();
    swimlaneInitialized = false;
    collapsedColumns.clear();
    _collapsedColumnsLoaded = false;
    loadCollapsedColumnsFromConfig();
  }

  /* ── ObjectURL-Tracking (F-210) ─────────────────────────────────── */
  /* Verhindert Blob-URL-Leaks bei wiederholten renderBoard-Aufrufen.
     Alle erzeugten blob:-URLs werden hier gesammelt und beim nächsten
     renderBoard-Aufruf via URL.revokeObjectURL freigegeben. */
  const _rendererObjectUrls = new Set();
  /* F-238: monoton steigender Render-Token. Jeder renderBoard-Aufruf erhöht
     ihn (nach _revokeRendererUrls). Async Cover-Callbacks vergleichen ihren
     Snapshot dagegen und brechen bei veralteter Generation ab. */
  let _renderGeneration = 0;
  function _trackRendererUrl(url) {
    if (url && url.startsWith('blob:')) _rendererObjectUrls.add(url);
  }
  function _revokeRendererUrls() {
    _rendererObjectUrls.forEach(u => { try { URL.revokeObjectURL(u); } catch {} });
    _rendererObjectUrls.clear();
  }

  /* F-217 (Karte #162): Swimlane-Kind-Default zentralisiert.
     Backlog-Swimlane ist immer 'backlog', alles andere default 'sprint'. */
  function swimlaneKindOf(sprint) {
    return sprint.kind || (sprint.id === '__backlog__' ? 'backlog' : 'sprint');
  }

  /* ── Spaltenfarbe-Palette — Logo-kohärent (Karte #122, überarbeitet #146) ── */
  const COLUMN_COLOR_PALETTE = [
    '#0B5D73', /* Teal      (Logo, --accent)     */
    '#2db5d0', /* Teal hell (--accent dark var)  */
    '#E9C85D', /* Gelb      (Logo, prio-high)    */
    '#63D2B0', /* Grün      (Logo, prio-low)     */
    '#dc2626', /* Rot       (passend zu Delete)  */
    '#7c3aed', /* Violett                        */
    '#ea580c', /* Orange                         */
    '#64748b', /* Slate     (neutraler Grau)     */
  ];

  /* ── Hilfsmethoden ───────────────────────────────────────────────── */

  function el(tag, cls) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    return e;
  }

  /* Sprint-102-Kritik F-2675: Symbol-Abzeichen (↑, ●●○, 💬 3, Datum) tragen ihren
     Klartext als Tooltip und als Namen; role="img", weil ein aria-label auf einem
     rollenlosen span nicht verlässlich angesagt wird (wie identity.js buildAvatar). */
  function benenne(node, text) {
    node.title = text;
    node.setAttribute('role', 'img');
    node.setAttribute('aria-label', text);
  }

  function clear(node) {
    while (node.lastChild) node.removeChild(node.lastChild);
  }


  /* ── Sprint-Meta-Sonderkarte (ADR-0036) ─────────────────────────── */

  function renderSprintMetaCard(card) {
    const cardEl = el('div', 'card card--sprint-meta');
    cardEl.dataset.id = card.id;
    cardEl.setAttribute('tabindex', '0');
    cardEl.setAttribute('role', 'button');
    cardEl.setAttribute('aria-label', card.title);
    cardEl.setAttribute('draggable', 'false');

    /* Titel (fest — kein Edit-Icon) */
    const title = el('div', 'card-title');
    const titleText = el('span', 'card-title-text');
    titleText.textContent = card.title;
    title.appendChild(titleText);
    cardEl.appendChild(title);

    /* Nur Kommentar-Count-Badge als Footer (wenn Kommentare vorhanden) */
    const commentCount = Array.isArray(card.comments) ? card.comments.length : 0;
    if (commentCount > 0) {
      const footer = el('div', 'card-footer');
      const cmtBadge = el('span', 'card-comment-badge');
      cmtBadge.textContent = `💬 ${commentCount}`;
      footer.appendChild(cmtBadge);
      cardEl.appendChild(footer);
    }

    /* Klick öffnet Modal (nur Beschreibung + Kommentare editierbar) */
    cardEl.addEventListener('click', () => EventBus.emit('card:edit', card));
    cardEl.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); EventBus.emit('card:edit', card); }
    });
    return cardEl;
  }

  /* F-2710: „kompakt“ kürzt den Titel auf zwei Zeilen (line-clamp), „normal“ bricht um.
     Der volle Text steht deshalb nur dann als Tooltip, wenn der Titel wirklich abgeschnitten ist;
     gemessen wird beim Darüberfahren, nicht beim Rendern (Layout-Messung je Karte wäre teuer). */
  const _titelTooltipContainer = new WeakSet();
  function onTitelPointerOver(e) {
    /* F-2748: Tastaturnutzer bekommen den vollen Titel nicht über `title` (erscheint nie bei Fokus,
       und j/k setzen keinen DOM-Fokus), sondern über die kompakt-Regel in style.css, die die Kürzung
       bei markierter oder fokussierter Karte aufhebt. */
    const t = e.target.closest?.('.card-title-text');
    if (!t) return;
    const gekuerzt = t.scrollHeight > t.clientHeight + 1 || t.scrollWidth > t.clientWidth + 1;
    if (gekuerzt) t.title = t.textContent; else t.removeAttribute('title');
  }

  /* ── Karte ───────────────────────────────────────────────────────── */

  /* colDef: die Spalten-Definition, in der `card` gerade gerendert wird (von
     renderColumn durchgereicht). F-141: wird für die Erledigt-Erkennung des
     Datum-Badges gebraucht — direkt vom Aufrufer statt aus State.get() zu
     lesen, sonst wäre der Badge in Kontexten ohne aktuell gesetztes globales
     State (z. B. Tests, die renderBoard() mit einem lokalen state-Objekt statt
     über State.set() aufrufen) unzuverlässig falsch-negativ. */
  function renderCard(card, colDef = null, sprintId = null) {
    /* ADR-0036: Sprint-Meta-Sonderkarten haben eigenes eingeschränktes Rendering */
    if (card.kind === 'sprint-meta') return renderSprintMetaCard(card);

    /* Karte #156: searchDimmed entfällt — bei aktiver Suche werden nicht-matchende
       Karten in renderColumn schon herausgefiltert. Filter behält sein Dimming. */
    const filterDimmed = Filter.isActive() && !Filter.matches(card);
    let cardClass = 'card';
    if (filterDimmed) cardClass += ' card--filtered';
    /* Karte #60: archivierte Karten gedämpft darstellen (nur sichtbar wenn showArchived aktiv) */
    if (card.archived) cardClass += ' card--archived';
    /* ADR-0141 §8 (Karte #826): Spiegelkarte einer externen Kopie — mit
       Abzeichen. Karte #827 E4: An das interne Board gebunden ist auch eine
       Karte ohne Marke in einer gespiegelten Lane (außen angelegt, wartet auf
       ihre Marke). Gebundene Karten sind nicht auswählbar (Massenaktionen
       mischten Erlaubtes und Abgelehntes); bei Rückweg aus sind sie nur lesbar
       und nicht ziehbar, bei Rückweg an ziehbar zwischen gespiegelten Spalten
       gespiegelter Lanes (dragdrop.js) und im Modal bearbeitbar (modal.js). */
    /* F-2233 (#831): Auf einer außen entkoppelten Kopie wirkt die Marke nicht. */
    const istSpiegel = IS_SERVER_BUILD && markeWirkt(card);
    const gebunden = IS_SERVER_BUILD && spiegelGebunden(card, sprintId);
    if (IS_SERVER_BUILD && istSpiegel) cardClass += ' card--spiegel';
    if (IS_SERVER_BUILD && gebunden) cardClass += ' card--gebunden' + (rueckwegAn() ? '' : ' card--nur-lesen');
    const cardEl = el('div', cardClass);
    cardEl.dataset.id = card.id;
    cardEl.setAttribute('tabindex', '0');
    cardEl.setAttribute('role', 'button');
    cardEl.setAttribute('aria-label', card.title);

    /* Karte #173: Cover-Bild als abgedunkelter Karten-Hintergrund (statt
       separater Block oberhalb). SVG bleibt blockiert (F-128); URL nur dann
       in background-image setzen, wenn Schema sicher ist (blob:/data:image/),
       und " im URL escapen — sonst CSS-Injection via background-image url()
       (F-162-Pattern). */
    if (card.coverAttachmentId) {
      const coverAtt = Array.isArray(card.attachments)
        ? card.attachments.find(a => a.id === card.coverAttachmentId)
        : null;
      /* F-1107: istSvgMime() statt exaktem String-Vergleich (erkennt auch
         `image/svg`, `image/svg-xml`, Groß-/Kleinschreibungs-Varianten). */
      const isCoverableImage = coverAtt?.mime?.startsWith('image/') && !istSvgMime(coverAtt.mime);
      if (isCoverableImage) {
        cardEl.classList.add('card--has-cover');
        /* Karte #467: Liegt ein Thumbnail am Anhang, ist das Cover SYNCHRON
           da — kein `await`, kein HTTP-Request, kein FSA-Lesevorgang. Genau
           das war die Beobachtung aus dem Betrieb („dauert ganz schön lang, bis das
           angezeigt wird"): Die Klasse stand sofort, das Bild kam hinterher,
           und zwar in bis zu 1920 px auf rund 264 px Anzeigebreite.
           Der asynchrone Weg darunter bleibt vollständig erhalten — für
           Bestandskarten ohne Thumbnail, für Bilder, aus denen keins
           entstehen kann (animierte Formate, bereits kleine Bilder), und
           damit ein fehlendes Thumbnail nie wie ein kaputtes aussieht. */
        /* `thumbnailUrl` escapt selbst und liefert `null`, sobald Nutzlast
           oder MIME-Typ nicht taugen — dann greift der asynchrone Weg unten,
           statt eine Karte mit weisser Schrift auf hellem Grund
           zurückzulassen. */
        const thumbUrl = thumbnailUrl(coverAtt);
        if (thumbUrl) {
          cardEl.style.backgroundImage =
            `linear-gradient(rgba(0,0,0,0.55), rgba(0,0,0,0.55)), url("${thumbUrl}")`;
        }
        const coverGen = _renderGeneration;
        /* KEIN `return` an dieser Stelle: Der Cover-Block sitzt mitten im
           Aufbau der Karte — ein früher Ausstieg hier liesse Titel, Badges
           und Labels weg. Der asynchrone Zweig wird deshalb übersprungen,
           nicht die Funktion verlassen. */
        if (!thumbUrl) anhangBackend().attachmentUrl(card, coverAtt).then(url => {
          if (!url) {
            /* Karte #450-Nacharbeit: Die Klasse steht schon, das Bild kommt
               nicht. `.card--has-cover` setzt `color:#f5f5f5` und
               `min-height:110px` (src/style.css) — ohne Bild also weisse
               Schrift auf hellem Grund, die Karte ist unlesbar. Vorher konnte
               das praktisch nicht passieren, weil im Server-Modus jeder Anhang
               `att.data` trug; seit dem REST-Umbau ist `null` ein normaler
               Ausgang (Anhang gelöscht, Server nicht erreichbar, 404). */
            if (coverGen === _renderGeneration && cardEl.isConnected) {
              cardEl.classList.remove('card--has-cover');
            }
            return;
          }
          /* F-238: stale Render-Generation oder abgehängter Knoten → die
             eben erzeugte blob:-URL sofort freigeben (sonst Leak, weil
             _trackRendererUrl unten übersprungen würde) und abbrechen.
             Schützt zugleich vor leerem Cover: _revokeRendererUrls der
             Folge-Generation kassiert dann nur eigene, frische URLs. */
          const usable = coverGen === _renderGeneration && cardEl.isConnected
            && /^(blob:|data:image\/)/.test(url);
          if (!usable) {
            if (url.startsWith('blob:')) { try { URL.revokeObjectURL(url); } catch {} }
            return;
          }
          _trackRendererUrl(url);
          const safeUrl = url.replace(/"/g, '%22');
          cardEl.style.backgroundImage =
            `linear-gradient(rgba(0,0,0,0.55), rgba(0,0,0,0.55)), url("${safeUrl}")`;
        }).catch(() => {});
      }
    }

    const title = el('div', 'card-title');
    const titleText = el('span', 'card-title-text');
    /* Such-Highlighting im Titel: DOM-Aufbau via createElement (ADR-0009) */
    if (Search.isActive() && Search.matches(card)) {
      titleText.appendChild(Search.buildHighlightedText(card.title));
    } else {
      titleText.textContent = card.title;
    }
    title.appendChild(titleText);
    if (card.displayId != null) {
      const idBadge = document.createElement('a');
      idBadge.className = 'card-display-id';
      idBadge.textContent = `#${card.displayId}`;
      idBadge.href = `#card-${card.displayId}`;
      idBadge.addEventListener('click', e => e.stopPropagation());
      title.appendChild(idBadge);
    }
    /* ADR-0133 §1: Finding-Abzeichen „F-<nr> P<n>" */
    if (card.kind === 'finding' && card.finding) {
      const fBadge = el('span', 'card-finding-id');
      fBadge.textContent = `F-${card.finding.nr ?? '?'} ${card.finding.prioritaet ?? ''}`.trim();
      title.appendChild(fBadge);
    }
    cardEl.appendChild(title);

    if (card.labels?.length) {
      const labels = el('div', 'card-labels');
      card.labels.forEach(lbl => {
        const span = el('span', 'label');
        span.textContent = lbl;
        faerbeLabel(span, lbl);
        labels.appendChild(span);
      });
      cardEl.appendChild(labels);
    }

    /* ADR-0032 Karte #54: board für den blocked-by-Verweis weiter unten. */
    const board = State.get()?.board;

    /* ADR-0034/F-140: Datum-Badge-Logik — Kalendertage statt Millisekunden-
       Differenz (DST-sicher, ADR-0103-Helfer aus date-helpers.js).
       F-141: Erledigt-Spalte per Titel-Semantik erkennen (isDoneColumn/F-250)
       statt hartkodiert auf `col-erledigt` zu prüfen — sonst zeigt eine
       umbenannte oder manuell angelegte Erledigt-Spalte den Badge fälschlich
       weiter an. Der übergebene `colDef` (von renderColumn, die tatsächliche
       Spalte dieser Karte) ist die primäre Quelle; Fallback auf State.get()
       nur defensiv, falls renderCard je ohne colDef aufgerufen wird. */
    const cardColDef = colDef ?? board?.columnDefs?.find(cd => cd.id === card.columnId);
    let dateBadgeClass = null;
    let dateBadgeText  = null;
    let dateBadgeTitle = null;
    if (card.dueDate && !isDoneColumn(cardColDef)) {
      const dueDiffDays = diffDays(todayISO(), card.dueDate);
      /* ADR-0138 §1: Uhrzeit hinter dem Datum; die Überfällig-/Bald-Regel
         bleibt tagesgenau (ADR-0034). */
      const due = card.dueTime ? `${card.dueDate} ${card.dueTime}` : card.dueDate;
      let rel = '';
      dateBadgeClass = 'card-date-badge';
      /* dueDate truthy aber nicht parsebar — Badge neutral mit Rohwert statt zu werfen. */
      dateBadgeText  = `📅 ${due}`;
      if (dueDiffDays !== null) {
        /* Karte #972: kurz „30. Okt.“ (Jahr nur, wenn nicht das laufende), Uhrzeit nur
           wenn gesetzt; volles Datum und Restzeit im Tooltip, Dringlichkeit über Farbe UND
           Zeichen (⚠ überfällig, ⏳ bald) — Farbe allein reicht nicht (F-2674, WCAG 1.4.1). */
        dateBadgeText = (dueDiffDays < 0 ? '⚠ ' : dueDiffDays <= 3 ? '⏳ ' : '📅 ') + new Intl.DateTimeFormat(I18n.getLang() === 'de' ? 'de-DE' : 'en-US', {
          day: 'numeric', month: 'short', year: card.dueDate.slice(0, 4) === todayISO().slice(0, 4) ? undefined : 'numeric',
        }).format(new Date(card.dueDate + 'T00:00')) + (card.dueTime ? ' ' + card.dueTime : '');
        if (dueDiffDays < 0) {
          dateBadgeClass += ' card-date-badge--overdue';
          rel = I18n.t('date.overdue');
        } else {
          if (dueDiffDays <= 3) dateBadgeClass += ' card-date-badge--soon';
          rel = dueDiffDays === 0 ? I18n.t('date.today') : I18n.t('date.in-days', dueDiffDays);
        }
      }
      dateBadgeTitle = `${I18n.t('card.due-date')}: ${due}${rel && ` (${rel})`}`;
    }

    const stats = Markdown.checklistStats(card.description);
    const linkCount = Array.isArray(card.links) ? card.links.length : 0;

    /* ADR-0032 Karte #54: linkBlocked computed — offene blocked-by-Verweise
       (board bereits oben für den Datum-Badge/F-141 geholt) */
    const blockerCards = (board && linkCount > 0) ? openBlockers(board, card) : [];
    const isLinkBlockedComputed = blockerCards.length > 0;

    /* F-338: assignees gehoeren in showFooter, sonst wird der weiter unten
       gebaute Assignee-Cluster (inkl. des dortigen a11y-Fallbacks) bei einer
       Karte, die ausschliesslich Assignees traegt, NIE gerendert — der Footer
       existiert dann gar nicht erst. Ein a11y-Fix an einem Element, das der
       Renderer nie ausgibt, ist keiner. */
    /* Karte #606 (ADR-0130 §1): Übernahme-Badge. Nur in den Builds, in denen
       eine Übernahme entstehen kann (Projektor-Inbox, Server) — im slim
       Template gibt es keinen Weg dorthin, der Zweig fällt per DCE weg. Der
       Zustand kommt aus derselben Regel wie im Fold (claimZustand), „jetzt“
       ist hier die Uhr des Betrachters; als Betrachter zählt niemand (kein
       Halter), deshalb der Platzhalter-Akteur. */
    let claimAnzeige = null;
    if (IS_PROJECTOR_BUILD || IS_SERVER_BUILD) {
      if (card.claim) claimAnzeige = claimZustand(card, Date.now(), '\u0000betrachter', null);
    }
    const zeigeClaim = claimAnzeige === 'fremd-aktiv' || claimAnzeige === 'fremd-abgelaufen';
    /* ADR-0140 §10 (Karte #778): Schloss für „vertraulich“ — nur im Server-Build. */
    const zeigeVertraulich = IS_SERVER_BUILD && istVertraulich(card);
    const spiegelAnhaenge = IS_SERVER_BUILD && istSpiegel ? (Number(card.spiegel.anhaenge) || 0) : 0;
    /* Karte #970: Zähler für Kommentare, Anhänge und Beschreibung (Vorbild Trello); 0 bleibt unsichtbar */
    const zaehler = [
      ['💬', card.comments?.length, 'tab.comments'],
      ['📎', card.attachments?.length, 'card.attachments'],
      ['≡', card.description?.trim() ? -1 : 0, 'card.export.section.description'],
    ].filter(z => z[1]);
    const showFooter = zaehler.length > 0 || istSpiegel || zeigeVertraulich || zeigeClaim || stats || card.qa || card.critical || card.blocked || isLinkBlockedComputed || card.owner || card.priority === 'hoch' || card.priority === 'niedrig' || card.complexity || card.effort != null || linkCount > 0 || dateBadgeClass != null || card.recurrence || (Array.isArray(card.assignees) && card.assignees.length > 0);
    if (showFooter) {
      const footer = el('div', 'card-footer');

      if (IS_SERVER_BUILD && istSpiegel) {
        /* Inline statt style.css: das slim Template soll kein totes CSS tragen.
           Kritik K-8: ohne Hintergrund, wie die Schatten (F-2027) — --text-muted
           auf --surface/--surface-2 hält so 4,9–5,0:1; mit grauer Füllung
           fiel es auf --surface-2 unter 4,5:1. */
        const b = el('span', 'card-flag-badge card-spiegel-badge');
        b.style.cssText = SPIEGEL_ABZEICHEN_STIL;
        b.textContent = I18n.t('card.spiegel');
        b.title = I18n.t(rueckwegAn() ? 'card.spiegel.hint.rueckweg' : 'card.spiegel.hint');
        footer.appendChild(b);
        if (spiegelAnhaenge > 0) {
          const a = el('span', 'card-flag-badge card-spiegel-anhaenge');
          a.style.cssText = b.style.cssText;
          a.textContent = I18n.t('card.spiegel.anhaenge', spiegelAnhaenge);
          a.title = I18n.t('card.spiegel.anhaenge.hint', spiegelAnhaenge);
          footer.appendChild(a);
        }
      }

      if (IS_SERVER_BUILD && zeigeVertraulich) {
        const b = el('span', 'card-flag-badge card-vertraulich-badge');
        b.textContent = '🔒';
        b.title = I18n.t('card.confidential.hint');
        b.setAttribute('aria-label', I18n.t('card.confidential'));
        footer.appendChild(b);
      }

      /* Karte #972: Priorität als Symbol, Klartext in title/aria-label */
      if (card.priority === 'hoch' || card.priority === 'niedrig') {
        const hoch = card.priority === 'hoch';
        const b = el('span', `card-prio-badge card-prio-badge--${hoch ? 'high' : 'low'}`);
        b.textContent = hoch ? '↑' : '↓';
        benenne(b, `${I18n.t('card.priority')}: ${I18n.t(hoch ? 'card.prio.high' : 'card.prio.low')}`);
        footer.appendChild(b);
      }

      if (stats) {
        const cl = el('span', 'card-checklist-summary');
        cl.textContent = `${stats.done}/${stats.total}`;
        footer.appendChild(cl);
      }

      if (IS_PROJECTOR_BUILD || IS_SERVER_BUILD) {
        if (zeigeClaim) {
          const aktiv = claimAnzeige === 'fremd-aktiv';
          const b = el('span', 'card-flag-badge card-claim-badge');
          /* Inline statt style.css: das slim Template soll kein totes CSS tragen. */
          b.style.cssText = aktiv
            ? 'color:#2563eb;background:rgba(37,99,235,.12)'
            : 'color:var(--text-muted);background:rgba(128,128,128,.14)';
          const wer = card.claim.by || '?';
          const zeit = new Date(card.claim.until).toLocaleString();
          b.textContent = aktiv ? I18n.t('claim.badge', wer) : I18n.t('claim.badge.verwaist');
          b.title = aktiv ? I18n.t('claim.titel', wer, zeit) : I18n.t('claim.titel.verwaist', wer, zeit);
          footer.appendChild(b);
        }
      }
      if (card.critical) {
        const b = el('span', 'card-flag-badge card-flag-badge--critical');
        b.textContent = I18n.t('card.critical');
        footer.appendChild(b);
      }
      if (card.blocked) {
        const b = el('span', 'card-flag-badge card-flag-badge--blocked');
        b.textContent = I18n.t('card.blocked');
        footer.appendChild(b);
      }
      /* ADR-0032 Karte #54: computed linkBlocked-Badge — unabhängig vom manuellen blocked-Flag */
      if (isLinkBlockedComputed) {
        const ids = blockerCards
          .map(c => c.displayId != null ? `#${c.displayId}` : c.title)
          .join(', ');
        const lb = el('span', 'card-link-blocked-badge');
        lb.textContent = I18n.t('link.blocked-badge');
        lb.title = I18n.t('link.blocked-tooltip', ids);
        footer.appendChild(lb);
      }
      if (card.owner) {
        const isClaudeOwner = card.owner.toLowerCase().startsWith('claude');
        const badge = el('span', isClaudeOwner ? 'card-owner-badge card-owner-badge--claude' : 'card-owner-badge card-owner-badge--user');
        /* F-2707: Der Name steht im Tooltip, weil „kompakt“ das Label ausblendet. */
        badge.title = `${I18n.t('card.owner')}: ${card.owner}`;
        /* Karte #123: Avatar voranstellen (Identity-System) */
        const identity = board ? resolveIdentity(card.owner, board) : null;
        if (identity) {
          const avatar = buildAvatar(identity, 'sm', { board, trackUrl: _trackRendererUrl });
          if (avatar) { badge.appendChild(avatar); badge.classList.add('card-owner-badge--avatar'); }
        }
        const labelEl = el('span', 'card-owner-badge-label');
        labelEl.textContent = card.owner;
        badge.appendChild(labelEl);
        footer.appendChild(badge);
      }
      /* #302: Assignees-Cluster — kleine Avatare nach dem Owner-Badge.
         Max 3 sichtbar, Rest als „+N"-Overflow-Badge. */
      if (Array.isArray(card.assignees) && card.assignees.length) {
        const cluster = el('span', 'card-assignees-cluster');
        cluster.title = I18n.t('card.assignees') + ': ' + card.assignees.join(', ');
        const MAX = 3;
        const shown = card.assignees.slice(0, MAX);
        shown.forEach(name => {
          const ident = board ? resolveIdentity(name, board) : null;
          if (ident) {
            const a = buildAvatar(ident, 'xs', { board, trackUrl: _trackRendererUrl });
            if (a) { a.classList.add('card-assignee-avatar'); cluster.appendChild(a); }
          } else {
            /* F-338: title allein ist fuer Screenreader nicht verlaesslich —
               echte buildAvatar-Avatare (identity.js) setzen role="img" +
               aria-label, das zieht dieser Fallback nach (title bleibt als
               Hover-Tooltip stehen). */
            const fallback = el('span', 'card-assignee-avatar card-assignee-avatar--unknown');
            const anzeigeName = name || '?';
            fallback.textContent = anzeigeName.slice(0, 1).toUpperCase();
            fallback.setAttribute('role', 'img');
            fallback.setAttribute('aria-label', anzeigeName);
            fallback.title = anzeigeName;
            cluster.appendChild(fallback);
          }
        });
        if (card.assignees.length > MAX) {
          const more = el('span', 'card-assignee-overflow');
          more.textContent = '+' + (card.assignees.length - MAX);
          cluster.appendChild(more);
        }
        footer.appendChild(cluster);
      }
      if (card.qa) {
        const qa = el('span', 'card-qa-badge');
        qa.textContent = '✓ QA';
        qa.title = I18n.t('card.qa');
        footer.appendChild(qa);
      }
      if (card.complexity) {
        const cx = el('span', `card-complexity-badge card-complexity-badge--${card.complexity}`);
        const label = { einfach: I18n.t('card.complexity.simple'), mittel: I18n.t('card.complexity.medium'), hoch: I18n.t('card.complexity.high') }[card.complexity] || card.complexity;
        /* Karte #972: Stufe als ●○○ / ●●○ / ●●●, Klartext in title/aria-label */
        const stufe = { einfach: 1, mittel: 2, hoch: 3 }[card.complexity];
        cx.textContent = stufe ? '●●●○○○'.substr(3 - stufe, 3) : label;
        benenne(cx, `${I18n.t('card.complexity')}: ${label}`);
        footer.appendChild(cx);
      }
      if (card.effort != null) {
        const ef = el('span', 'card-effort-badge');
        ef.textContent = `${card.effort}h`;
        ef.title = I18n.t('card.effort');
        footer.appendChild(ef);
      }
      /* ADR-0032: Karten-Verweise als Footer-Badge */
      if (linkCount > 0) {
        const lb = el('span', 'card-link-badge');
        lb.textContent = `⛓ ${linkCount}`;
        lb.title = I18n.t('tab.links');
        footer.appendChild(lb);
      }
      /* n = -1: Beschreibung vorhanden, ohne Zahl */
      for (const [sym, n, key] of zaehler) {
        const zb = el('span', 'card-count-badge');
        zb.textContent = n < 0 ? sym : `${sym} ${n}`;
        benenne(zb, n < 0 ? I18n.t(key) : `${I18n.t(key)}: ${n}`);
        footer.appendChild(zb);
      }
      /* ADR-0034: Fälligkeitsdatum-Badge */
      if (dateBadgeClass != null) {
        const db = el('span', dateBadgeClass);
        db.textContent = dateBadgeText;
        /* F-2679: Name beginnt mit dem sichtbaren Text (WCAG 2.5.3 Label in Name) */
        benenne(db, `${dateBadgeText} — ${dateBadgeTitle}`);
        footer.appendChild(db);
      }
      /* ADR-0041: Recurrence-Badge — nur auf Template-Karten (mit recurrence-Feld).
         F-167: reines Emoji ohne Textalternative ist für Screenreader stumm —
         zusätzlich zum title (Hover-Tooltip) ein aria-label setzen. */
      if (card.recurrence) {
        const intervalKey = `recurrence.${card.recurrence.interval}`;
        const recurrenceLabel = I18n.t('recurrence.badge-title', I18n.t(intervalKey));
        const rb = el('span', 'card-recurrence-badge');
        rb.textContent = '🔁';
        rb.title = recurrenceLabel;
        rb.setAttribute('aria-label', recurrenceLabel);
        footer.appendChild(rb);
      }

      cardEl.appendChild(footer);
    }

    /* Karte #63: Bulk-Selektion — Ctrl/Cmd-Klick toggelt, Shift-Klick Range, normaler Klick öffnet Modal */
    cardEl.addEventListener('click', (e) => {
      if (e.ctrlKey || e.metaKey) {
        /* Ctrl/Cmd-Klick: Toggle-Selektion, kein Modal */
        e.preventDefault();
        /* Kritik K-5 (Karte #826): Spiegelkarten nicht auswählbar — der Server
           wiese eine Massenaktion auf sie ab (Ablehnungsmeldung, lokale
           Abweichung bis zum nächsten Stand). */
        if (IS_SERVER_BUILD && gebunden) return;
        BulkSelect.toggle(card.id);
        return;
      }
      if (e.shiftKey) {
        /* Shift-Klick: Range-Selektion basierend auf sichtbaren Nicht-Meta-Karten */
        e.preventDefault();
        if (IS_SERVER_BUILD && gebunden) return;
        const visibleIds = [...document.querySelectorAll('.card:not(.card--sprint-meta):not(.card--gebunden)')]
          .filter(el => el.offsetParent !== null || el.closest('[style*="display: none"]') === null)
          .map(el => el.dataset.id)
          .filter(Boolean);
        BulkSelect.selectRange(card.id, visibleIds);
        return;
      }
      /* Normaler Klick: Modal öffnen (bestehend) */
      EventBus.emit('card:edit', card);
    });
    cardEl.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); EventBus.emit('card:edit', card); }
    });
    return cardEl;
  }

  /* ── Karte-Hinzufügen-Bereich ────────────────────────────────────── */

  /* footer + addBtn werden übergeben damit Cancel inline restoren kann — kein Re-Render nötig (F-39).
     ADR-0040: optionaler template-Parameter — wird übergeben wenn ein Template gewählt wurde. */
  function renderCardAddForm(sprintId, colId, footer, addBtn, template = null, alsFinding = false) {
    const wrap = el('div', 'card-add-form');

    const input = el('input');
    input.type = 'text';
    input.className = 'card-add-input';
    input.placeholder = I18n.t('card.add-placeholder');
    wrap.appendChild(input);

    const actions = el('div', 'card-add-actions');
    const confirmBtn = el('button', 'btn btn-primary btn-sm');
    confirmBtn.textContent = I18n.t('btn.add-card-confirm');
    const cancelBtn = el('button', 'btn btn-sm');
    cancelBtn.textContent = I18n.t('btn.cancel');
    /* ADR-0133 §1: dieselbe Eingabe als Finding-Karte anlegen */
    const findingBtn = el('button', 'btn btn-sm btn-new-finding');
    findingBtn.textContent = '🔎 ' + I18n.t('finding.new');
    /* F-1548: Aus dem Vorlagen-Dropdown als „Finding“ gewählt — dann legt
       auch Enter ein Finding an, und der Karten-Knopf entfällt (derselbe
       `card:quick-add`-Weg wie der Finding-Knopf, kein zweiter). */
    if (!alsFinding) actions.appendChild(confirmBtn);
    actions.appendChild(findingBtn);
    actions.appendChild(cancelBtn);
    wrap.appendChild(actions);

    function restore() {
      clear(footer);
      footer.appendChild(addBtn);
    }

    function commit(finding = alsFinding) {
      const title = input.value.trim();
      if (title) {
        /* ADR-0040: Template-Daten mitsenden wenn Template gewählt */
        EventBus.emit('card:quick-add', { sprintId, colId, title, template, finding });
      } else restore();
    }

    input.addEventListener('keydown', e => {
      if (e.key === 'Enter')  { e.preventDefault(); commit(); }
      if (e.key === 'Escape') restore();
    });
    confirmBtn.addEventListener('click', () => commit());
    findingBtn.addEventListener('click', () => commit(true));
    cancelBtn.addEventListener('click', restore);

    return wrap;
  }

  /* ── Template-Dropdown (ADR-0040, Karte #65) ────────────────────── */

  /** Öffnet ein kleines Dropdown unter dem „+ Karte"-Button mit „Leer" + Template-Namen.
   *  Wählt der Nutzer „Leer": normales Add-Form.
   *  Wählt er ein Template: Add-Form mit vorausgefüllten Feldern über `card:quick-add-from-template`. */
  function openTemplateDropdown(sprintId, colId, footer, addBtn, templates) {
    /* Bestehenden Dropdown entfernen (falls doppelt geklickt) */
    footer.querySelector('.template-dropdown')?.remove();

    const dropdown = el('div', 'template-dropdown');

    function choose(template, alsFinding = false) {
      dropdown.remove();
      clear(footer);
      const form = renderCardAddForm(sprintId, colId, footer, addBtn, template, alsFinding);
      footer.appendChild(form);
      form.querySelector('input').focus();
      if (template) {
        form.querySelector('.card-add-input').value = template.title;
      }
    }

    /* Option: Leer */
    const emptyItem = el('button', 'template-dropdown-item');
    emptyItem.textContent = I18n.t('template.new-empty');
    emptyItem.type = 'button';
    emptyItem.addEventListener('click', () => choose(null));
    dropdown.appendChild(emptyItem);

    /* F-1548 (ADR-0133 §1): Finding als eigener Eintrag neben „Leer“. */
    const findingItem = el('button', 'template-dropdown-item template-dropdown-finding');
    findingItem.textContent = '🔎 ' + I18n.t('finding.new');
    findingItem.type = 'button';
    findingItem.addEventListener('click', () => choose(null, true));
    dropdown.appendChild(findingItem);

    /* Optionen: Templates */
    templates.forEach(tpl => {
      const item = el('button', 'template-dropdown-item');
      /* F-833: `.trim()` fehlte — ein Titel aus reinem Whitespace ist truthy
         und erzeugte sonst einen leeren Dropdown-Eintrag statt des Platzhalters. */
      item.textContent = (tpl.title || '').trim() || I18n.t('template.choose');
      item.type = 'button';
      item.addEventListener('click', () => choose(tpl));
      dropdown.appendChild(item);
    });

    footer.appendChild(dropdown);

    /* Klick außerhalb → Dropdown schließen */
    function onOutside(e) {
      if (!footer.contains(e.target)) {
        dropdown.remove();
        document.removeEventListener('mousedown', onOutside, true);
      }
    }
    document.addEventListener('mousedown', onOutside, true);

    /* Escape → Dropdown schließen ohne Aktion */
    function onKey(e) {
      if (e.key === 'Escape') {
        dropdown.remove();
        document.removeEventListener('keydown', onKey, true);
        document.removeEventListener('mousedown', onOutside, true);
      }
    }
    document.addEventListener('keydown', onKey, true);
  }

  /* ── Spalte ──────────────────────────────────────────────────────── */

  /* F-250: „Erledigt"-Spalte per Titel-Semantik erkennen statt fester id
     `col-erledigt`. Manuell angelegte Spalten bekommen via createColumnDef()
     eine UUID-id — Kompaktansicht/Clear-Always müssen am Titel hängen, nicht
     an der id (analog F-242).
     F-127: `isDoneColumn`/`DONE_COLUMN_NAMES` standen bis Sprint 61 hier als
     modul-lokale Kopie. Seither liegen sie in links.js (dorthin gehörte die
     Erkennung ohnehin, weil die Blocker-Freigabe sie braucht) und werden von
     hier importiert — eine Quelle statt zweier, die auseinanderlaufen können. */

  /* F-242: Sprint-Meta-Sortierung (📋 oben / 🏁 unten) hing hartcodiert an
     der id `col-backlog`. Eine manuell angelegte „Backlog"-Spalte bekommt
     via createColumnDef() eine UUID-id → Sortierung griff nicht. Analog zu
     isDoneColumn/F-250 per Titel-Semantik erkennen. BACKLOG_NAMES = Single Source. */
  const BACKLOG_NAMES = ['backlog', 'ideen', 'parkplatz'];
  function isBacklogColumn(colDef) {
    return BACKLOG_NAMES.includes((colDef?.title ?? '').trim().toLowerCase());
  }

  /* Karte #1004: Darstellung nach Dringlichkeit — stabil, Meta-Karten behalten
     ihren Platz (nur die übrigen Karten wandern durch ihre Plätze). Rein: das
     Eingabefeld und der gespeicherte Stand bleiben unberührt. */
  function sortiereNachDringlichkeit(karten, heute, erledigt) {
    const plaetze = [], rest = [];
    karten.forEach((c, i) => { if (c.kind !== 'sprint-meta') { plaetze.push(i); rest.push({ c, i, s: dringlichkeit(c, heute, { erledigt }) }); } });
    rest.sort((a, b) => a.s - b.s || a.i - b.i);
    const aus = karten.slice();
    plaetze.forEach((p, k) => { aus[p] = rest[k].c; });
    return aus;
  }

  function renderColumn(colDef, cards, sprintId, heute = heuteOrtszeit()) {
    /* Karte #60: archivierte Karten nur rendern wenn showArchived aktiv */
    const showArchived = Filter.isShowingArchived();
    const archivedCount = cards.filter(c => c.archived).length;
    let visibleCards    = showArchived ? cards : cards.filter(c => !c.archived);
    /* Karte #156: bei aktiver Suche Karten ohne Match komplett ausblenden (statt dimmen) */
    if (Search.isActive()) {
      visibleCards = visibleCards.filter(c => Search.matches(c));
    }

    const nachDringlichkeit = Config.get('sortierung') === 'dringlichkeit';
    if (nachDringlichkeit) visibleCards = sortiereNachDringlichkeit(visibleCards, heute, isDoneColumn(colDef));

    const colEl = el('div', 'column');
    colEl.dataset.id = colDef.id;
    colEl.dataset.sprintId = sprintId;
    if (isDoneColumn(colDef)) colEl.classList.add('is-done');  /* F-250: CSS hängt an .is-done, nicht an fester id */
    /* ADR-0141 §7/§8, F-2142 (Karte #826): Auf einer externen Kopie gehören
       gespiegelte Lanes und Spalten dem internen Board. In einer gespiegelten
       Lane legt niemand an und räumt niemand ab; eine gespiegelte Spalte
       wird nicht umbenannt, gefärbt, gelöscht, geleert oder verschoben — der
       Server wiese jede dieser Änderungen ab (Spiegel-Schranke). */
    const laneGespiegelt = IS_SERVER_BUILD &&
      markeWirkt((State.get()?.board?.sprints ?? []).find(s => s.id === sprintId));
    const spalteGespiegelt = IS_SERVER_BUILD && markeWirkt(colDef);
    if (IS_SERVER_BUILD && laneGespiegelt) colEl.classList.add('column--spiegel');
    if (IS_SERVER_BUILD && spalteGespiegelt) colEl.classList.add('column--struktur-spiegel');
    /* Karte #827 E4 (ADR-0141 §7): Bei Rückweg an nimmt eine gespiegelte
       Spalte in einer gespiegelten Lane Karten an — neue und hineingezogene
       werden wartend und gehen nach innen. Anderswohin darf eine gebundene
       Karte nicht (der Server wiese es ab). */
    const rueckwegZiel = IS_SERVER_BUILD && laneGespiegelt && spalteGespiegelt && rueckwegAn();
    if (IS_SERVER_BUILD && rueckwegZiel) colEl.classList.add('column--rueckweg-ziel');

    /* Collapse-Key pro Sprint + Spalte getrennt */
    const collapseKey = `${sprintId}:${colDef.id}`;
    const isCollapsed = collapsedColumns.has(collapseKey);
    if (isCollapsed) colEl.classList.add('column--collapsed');

    /* Header */
    const header = el('div', 'column-header');
    if (nachDringlichkeit) { header.title = I18n.t('col.sort-dringlich'); header.classList.add('column-header--sort'); } /* F-2763: Symbol per CSS */

    /* Toggle-Button (▼/▶) — vor dem Titel */
    const toggleBtn = el('button', 'column-toggle');
    toggleBtn.textContent = isCollapsed ? '▶' : '▼';
    toggleBtn.setAttribute('aria-label', isCollapsed ? I18n.t('col.expand') : I18n.t('col.collapse'));
    toggleBtn.addEventListener('click', e => {
      e.stopPropagation(); /* Title-Rename nicht auslösen */
      const nowCollapsed = collapsedColumns.has(collapseKey);
      const cardsEl = colEl.querySelector('.column-cards');
      const footer  = colEl.querySelector('.column-footer');
      if (nowCollapsed) {
        collapsedColumns.delete(collapseKey);
        colEl.classList.remove('column--collapsed');
        if (cardsEl) cardsEl.style.display = '';
        if (footer)  footer.style.display  = '';
        toggleBtn.textContent = '▼';
        toggleBtn.setAttribute('aria-label', I18n.t('col.collapse'));
      } else {
        collapsedColumns.add(collapseKey);
        colEl.classList.add('column--collapsed');
        if (cardsEl) cardsEl.style.display = 'none';
        if (footer)  footer.style.display  = 'none';
        toggleBtn.textContent = '▶';
        toggleBtn.setAttribute('aria-label', I18n.t('col.expand'));
      }
      persistCollapsedColumns();  /* Karte #151 */
    });
    header.appendChild(toggleBtn);

    const titleEl = el('span', 'column-title');
    titleEl.textContent = colDef.title;
    /* F-2225: „Karten gehen nach innen“ gilt nur im Rückweg-Ziel — in einer
       eigenen Lane bleibt eine gespiegelte Spalte nur strukturell gesperrt. */
    titleEl.title = I18n.t(IS_SERVER_BUILD && spalteGespiegelt ? (rueckwegZiel ? 'lane.spiegel.hint.rueckweg' : 'lane.spiegel.hint') : 'col.rename-tip');

    /* Karte #146: Counter ist gleichzeitig Color-Button (öffnet Farb-Popover).

       F-155 (entschieden Sprint 61): Der Zähler zeigt `visibleCards.length` —
       also GENAU die Liste, die unten gerendert wird. Sprint-Meta-Karten
       werden bewusst NICHT herausgerechnet: sie stehen sichtbar in der Spalte
       (die 📋-Start-Karte liegt im aktiven Sprint in „Erledigt"), und ein
       Zähler, der 1 sagt, während zwei Karten untereinander liegen, ist
       schlechter als der gemeldete Symptomfall. Die Stellen, die Meta-Karten
       ausschliessen — Velocity (`computeSprintMetrics`), der Archivier-Dialog
       (ADR-0117 §6) und `archivableCount` weiter unten —, MESSEN Arbeit; der
       Spalten-Zähler beschriftet dagegen einen Stapel. Invariante deshalb:
       Zähler == Anzahl der `.card`-Elemente in dieser Spalte (durch einen Test
       in renderer.dom.test.js festgenagelt). */
    const count = el('button', 'column-count');
    count.type = 'button';
    count.textContent = visibleCards.length;
    count.title = I18n.t('col.color.label');
    count.setAttribute('aria-label', I18n.t('col.color.label'));

    const deleteBtn = el('button', 'btn-col-delete');
    /* Karte #165: × → 🗑 (Emoji-Papierkorb für alle echten Delete-Buttons).
       × bleibt für Close-/Clear-Aktionen reserviert.
       Karte #174 / F-231: Emoji-Glyph in aria-hidden-Span — Screen-Reader
       liest nur das aria-label, nicht den Emoji-Default-Namen doppelt. */
    const deleteGlyph = document.createElement('span');
    deleteGlyph.textContent = '🗑';
    deleteGlyph.setAttribute('aria-hidden', 'true');
    deleteBtn.appendChild(deleteGlyph);
    deleteBtn.title = I18n.t('col.delete-title');
    deleteBtn.setAttribute('aria-label', I18n.t('col.delete-label', colDef.title));

    const clearBtn = el('button', 'btn-col-clear');
    /* Karte #174: 🧹 (Besen) statt trashIcon-SVG — visuelle Verwechslung mit
       btn-col-delete (🗑) wegnehmen. Semantik: Besen = Aufräumen, Mülleimer
       = Wegwerfen. Span mit aria-hidden umhüllt das Emoji, damit Screen-
       Reader nur das aria-label vorlesen (gleichzeitig F-231-Pattern). */
    const clearGlyph = document.createElement('span');
    clearGlyph.textContent = '🧹';
    clearGlyph.setAttribute('aria-hidden', 'true');
    clearBtn.appendChild(clearGlyph);
    clearBtn.title = I18n.t('col.clear-title');
    clearBtn.setAttribute('aria-label', I18n.t('col.clear-label', colDef.title));
    clearBtn.style.display = cards.length ? '' : 'none'; /* alle Karten zählen (inkl. Archiv) */

    /* Erledigt-Spalten: Papierkorb immer sichtbar (nicht nur auf Hover) */
    if (isDoneColumn(colDef)) {
      clearBtn.classList.add('btn-col-clear--always');
    }

    /* Karte #60: Archiv-Toggle — nur anzeigen wenn archivierte Karten vorhanden.
       Karte #287 (F-325): Glyph 👁 statt 📦, damit der Sichtbarkeits-Toggle
       nicht mehr mit der Massen-Archiv-Sammel-Aktion (📦, #279) optisch
       kollidiert. Aria-Label-Klarheit über `filter.archive-toggle-label`. */
    let archiveToggleBtn = null;
    if (archivedCount > 0) {
      archiveToggleBtn = el('button', showArchived ? 'btn-col-archive-toggle btn-col-archive-toggle--active' : 'btn-col-archive-toggle');
      archiveToggleBtn.title = I18n.t('filter.archive-toggle-label');
      archiveToggleBtn.setAttribute('aria-label', I18n.t('filter.archive-toggle-label'));
      archiveToggleBtn.setAttribute('aria-pressed', String(showArchived));
      archiveToggleBtn.textContent = `👁 ${archivedCount}`;
      archiveToggleBtn.addEventListener('click', e => {
        e.stopPropagation();
        Filter.set({ showArchived: !Filter.isShowingArchived() });
        EventBus.emit('filter:changed');
      });
    }

    /* Karte #279: Massen-Archiv — nur in Erledigt-Spalten, wenn ≥1 nicht-
       archivierte Nicht-Meta-Karte vorhanden ist. Sprint-Meta-Karten
       (kind:'sprint-meta') werden NIE archiviert (ADR-0036; vermeidet die
       F-153-Klasse für diese Aktion). Einzel-Toggle existiert bereits im
       Modal — dies ist die Spalten-Sammelaktion. */
    let archiveDoneBtn = null;
    const archivableCount = cards.filter(
      c => !c.archived && c.kind !== 'sprint-meta',
    ).length;
    if (isDoneColumn(colDef) && archivableCount > 0) {
      archiveDoneBtn = el('button', 'btn-col-archive-all');
      const archGlyph = document.createElement('span');
      archGlyph.textContent = '📦';
      archGlyph.setAttribute('aria-hidden', 'true');
      archiveDoneBtn.appendChild(archGlyph);
      archiveDoneBtn.title = I18n.t('col.archive-done-title');
      archiveDoneBtn.setAttribute('aria-label', I18n.t('col.archive-done-label', colDef.title));
      archiveDoneBtn.addEventListener('click', e => {
        e.stopPropagation();
        if (confirm(I18n.t('col.archive-done-confirm', archivableCount, colDef.title))) {
          /* F-NEU-2: sprintId mitgeben — der Handler scopt auf genau diesen
             Sprint, damit die bestätigte Zahl der tatsächlichen Wirkung
             entspricht (vorher sprint-übergreifend → Confirm-Mismatch). */
          EventBus.emit('col:archive-done', { colId: colDef.id, sprintId });
        }
      });
    }

    /* Karte #146: Farb-Tint auf Spalten-Element anwenden — Counter selbst öffnet das Popover */
    if (colDef.color) {
      colEl.style.setProperty('--col-tint', colDef.color);
      colEl.classList.add('column--tinted');
    }

    /* Farb-Popover öffnen */
    let colorPopover = null;

    function closeColorPopover() {
      colorPopover?.remove();
      colorPopover = null;
    }

    function openColorPopover() {
      closeColorPopover();

      const pop = el('div', 'col-color-popover');
      pop.setAttribute('role', 'dialog');
      pop.setAttribute('aria-modal', 'true');
      pop.setAttribute('aria-label', I18n.t('col.color.aria-palette'));

      /* Farbpalette */
      const paletteEl = el('div', 'col-color-palette');
      paletteEl.setAttribute('role', 'radiogroup');
      paletteEl.setAttribute('aria-label', I18n.t('col.color.aria-palette'));

      const swatchBtns = COLUMN_COLOR_PALETTE.map((hex, idx) => {
        const btn = el('button', 'col-color-swatch-btn');
        btn.style.background = hex;
        btn.setAttribute('aria-label', hex);
        btn.setAttribute('role', 'radio');
        btn.setAttribute('aria-checked', String(colDef.color === hex));
        btn.dataset.hex = hex;
        if (colDef.color === hex) btn.classList.add('col-color-swatch-btn--selected');
        btn.addEventListener('click', () => {
          EventBus.emit('col:color', { colId: colDef.id, color: hex });
          closeColorPopover();
        });
        return btn;
      });
      swatchBtns.forEach(b => paletteEl.appendChild(b));
      pop.appendChild(paletteEl);

      /* Freie Farb-Eingabe */
      const freeRow = el('div', 'col-color-free-row');
      const freeLabel = el('label', 'col-color-free-label');
      freeLabel.textContent = I18n.t('col.color.label');
      const colorInput = el('input');
      colorInput.type = 'color';
      colorInput.className = 'col-color-input';
      colorInput.value = colDef.color ?? '#0B5D73';
      /* Karte #153: input = Live-Preview (Native-Picker noch offen), change = finalisieren + schließen. */
      colorInput.addEventListener('input', () => {
        EventBus.emit('col:color', { colId: colDef.id, color: colorInput.value });
      });
      colorInput.addEventListener('change', () => {
        EventBus.emit('col:color', { colId: colDef.id, color: colorInput.value });
        closeColorPopover();
      });
      freeLabel.appendChild(colorInput);
      freeRow.appendChild(freeLabel);
      pop.appendChild(freeRow);

      /* Reset-Button */
      const resetBtn = el('button', 'btn btn-sm col-color-reset');
      resetBtn.type = 'button';
      resetBtn.textContent = I18n.t('col.color.reset');
      resetBtn.addEventListener('click', () => {
        EventBus.emit('col:color', { colId: colDef.id, color: null });
        closeColorPopover();
      });
      pop.appendChild(resetBtn);

      /* Tastatur-Navigation in Palette: ArrowLeft/Right, Escape */
      pop.addEventListener('keydown', e => {
        if (e.key === 'Escape') { closeColorPopover(); count.focus(); return; }
        if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
          const focused = pop.querySelector('.col-color-swatch-btn:focus');
          if (!focused) return;
          const idx = swatchBtns.indexOf(focused);
          const next = e.key === 'ArrowRight'
            ? swatchBtns[(idx + 1) % swatchBtns.length]
            : swatchBtns[(idx - 1 + swatchBtns.length) % swatchBtns.length];
          next.focus();
          e.preventDefault();
        }
      });

      /* Karte #146: Positionierung am Counter-Button (vorher colorBtn) */
      const rect = count.getBoundingClientRect();
      pop.style.top   = `${rect.bottom + 6}px`;
      pop.style.right = `${document.documentElement.clientWidth - rect.right}px`;
      document.body.appendChild(pop);
      requestAnimationFrame(() => pop.classList.add('popover--open'));
      colorPopover = pop;

      /* Fokus auf ersten Swatch oder aktiven */
      const activeSwatch = swatchBtns.find(b => b.classList.contains('col-color-swatch-btn--selected')) ?? swatchBtns[0];
      activeSwatch?.focus();

      /* Klick außerhalb schließt */
      setTimeout(() => {
        document.addEventListener('pointerdown', function onOutside(e) {
          if (colorPopover && !colorPopover.contains(e.target) && e.target !== count) {
            closeColorPopover();
          } else if (colorPopover) {
            document.addEventListener('pointerdown', onOutside, { once: true });
          }
        }, { once: true });
      }, 0);
    }

    count.addEventListener('click', e => {
      e.stopPropagation();
      if (colorPopover) { closeColorPopover(); } else { openColorPopover(); }
    });

    /* Titel + Zähler gruppiert (flex:1) — Action-Buttons rechts davon */
    const titleGroup = el('div', 'col-title-group');
    titleGroup.appendChild(titleEl);
    titleGroup.appendChild(count);

    const headerRight = el('div', 'col-header-right');
    if (archiveToggleBtn) headerRight.appendChild(archiveToggleBtn);
    const strukturFrei = !(IS_SERVER_BUILD && spalteGespiegelt);
    const laneFrei     = !(IS_SERVER_BUILD && laneGespiegelt);
    if (archiveDoneBtn && laneFrei) headerRight.appendChild(archiveDoneBtn);
    /* „Spalte leeren“ wirkt über alle Lanes — auf einer gespiegelten Spalte
       träfe es Spiegelkarten auch dann, wenn dieser Kopf in einer eigenen Lane steht. */
    if (strukturFrei && laneFrei) headerRight.appendChild(clearBtn);
    if (strukturFrei) headerRight.appendChild(deleteBtn);
    if (!strukturFrei) {
      count.disabled = true;
      count.removeAttribute('title');
      count.removeAttribute('aria-label');
    }

    header.appendChild(titleGroup);
    header.appendChild(headerRight);
    if (nachDringlichkeit) { const sr = el('span', 'visually-hidden'); sr.textContent = I18n.t('col.sort-dringlich'); header.appendChild(sr); } /* F-2862: Zustand für Screenreader */
    colEl.appendChild(header);

    titleEl.addEventListener('click', () => {
      if (!strukturFrei) return;
      const input = el('input');
      input.className = 'column-title-input';
      input.value = colDef.title;
      titleGroup.replaceChild(input, titleEl); /* in titleGroup ersetzen */
      input.focus();
      input.select();

      let saved = false;
      function save() {
        if (saved) return;
        saved = true;
        EventBus.emit('col:rename', { colId: colDef.id, title: input.value.trim() || colDef.title });
      }
      input.addEventListener('keydown', e => {
        if (e.key === 'Enter')  { e.preventDefault(); save(); }
        if (e.key === 'Escape') { saved = true; EventBus.emit('col:rename', { colId: colDef.id, title: colDef.title }); }
      });
      input.addEventListener('blur', save);
    });

    clearBtn.addEventListener('click', () => {
      if (confirm(I18n.t('col.clear-confirm', colDef.title))) {
        EventBus.emit('col:clear', { colId: colDef.id });
      }
    });

    deleteBtn.addEventListener('click', () => {
      if (confirm(I18n.t('col.delete-all-confirm', colDef.title))) {
        EventBus.emit('col:delete', { colId: colDef.id });
      }
    });

    /* Karten-Liste — archivierte Karten nur wenn showArchived aktiv */
    const cardsEl = el('div', 'column-cards');
    visibleCards.forEach(card => cardsEl.appendChild(renderCard(card, colDef, sprintId)));
    /* Karte #825 (ADR-0141 §8): Auf einer externen Kopie stehen die Schatten
       interner Karten am Ende ihrer Spalte — eigene Klasse, nie `.card`
       (Zähler, Ziehen, Modal, Mehrfachauswahl, j/k und FLIP sehen sie nicht). */
    if (IS_SERVER_BUILD && SchattenAussen.kopie()) {
      const sprint = (State.get()?.board?.sprints ?? []).find(s => s.id === sprintId);
      for (const e of SchattenAussen.inSpalte(sprint, colDef)) cardsEl.appendChild(SchattenAussen.karte(e));
    }
    if (isCollapsed) cardsEl.style.display = 'none';
    colEl.appendChild(cardsEl);

    /* Footer */
    const footer = el('div', 'column-footer');
    const addBtn = el('button', 'btn-add-card');
    addBtn.textContent = I18n.t('btn.add-card');

    addBtn.addEventListener('click', () => {
      /* ADR-0040: Dropdown anzeigen wenn Templates vorhanden, sonst Default-Verhalten */
      const templates = State.get()?.board?.templates ?? [];
      if (templates.length > 0) {
        openTemplateDropdown(sprintId, colDef.id, footer, addBtn, templates);
      } else {
        clear(footer);
        const form = renderCardAddForm(sprintId, colDef.id, footer, addBtn);
        footer.appendChild(form);
        form.querySelector('input').focus();
      }
    });

    /* Ohne Knopf findet auch der Shortcut `n` (shortcuts.js) hier keinen —
       er nimmt den ersten Knopf einer eigenen Lane oder tut nichts. Karte #827
       E4: Bei Rückweg an gibt es den Knopf auch in gespiegelten Spalten einer
       gespiegelten Lane (die neue Karte wartet auf ihre Marke). */
    if (laneFrei || (IS_SERVER_BUILD && rueckwegZiel)) footer.appendChild(addBtn);
    if (isCollapsed) footer.style.display = 'none';
    colEl.appendChild(footer);

    return colEl;
  }

  /* ── Sprint-Header ───────────────────────────────────────────────── */

  /* columnDefs: die Spalten des gerenderten Boards, von renderBoard
     durchgereicht. Bewusst als Parameter statt ueber `State.get()` — derselbe
     Grund wie bei `renderCard(card, colDef)` (F-141): Tests rufen
     `renderBoard({board})` mit einem lokalen Objekt auf, ohne `State.set()`.
     Ein Griff nach dem globalen State liefert dort nichts und die
     Erledigt-Erkennung waere still falsch. */
  function renderSprintHeader(sprint, columnDefs = null, board = null) {
    const header = el('div', 'sprint-header');
    /* ADR-0046: Swimlane-Kind ('sprint' | 'lane' | 'backlog'). Migration garantiert das Feld,
       Fallback hier nur defensiv für Test-Fixtures, die kind manuell weglassen. */
    const kind = swimlaneKindOf(sprint);
    if (kind === 'lane')    header.classList.add('sprint-header--lane');
    /* ADR-0092: Service-Board-Header ohne Sprint-Mechanik (kein Velocity,
       kein Sprint↔Lane-Toggle, kein Delete-Button) — Server-Wahrheit. */
    if (kind === 'service') header.classList.add('sprint-header--service');

    /* Karte #175 (D): Sprint-Aktivierungs-Sweep. Wenn die Sprint-Start-
       Karte einen sprint-activate-Activity-Eintrag innerhalb der letzten
       4 s hat, läuft beim Render einmalig ein Highlight über den Header. */
    if (kind === 'sprint' && Array.isArray(sprint.cards)) {
      const startCard = sprint.cards.find(c => c.metaType === 'start');
      const lastActivate = (startCard?.activity || [])
        .filter(a => a.type === 'sprint-activate')
        .map(a => Date.parse(a.timestamp))
        .filter(t => !isNaN(t))
        .sort((a, b) => b - a)[0];
      /* F-240: unter prefers-reduced-motion gar nicht erst die Sweep-Klasse
         setzen — CSS reduziert die Animation auf 0.001 ms, die Klasse blieb
         aber 720 ms hängen (Stil-Drift). */
      if (lastActivate && Date.now() - lastActivate < 4000 && !prefersReducedMotion()) {
        pulseClass(header, 'sprint-header--just-activated', 720);
      }
    }

    /* Toggle-Button zum Ein-/Ausklappen */
    const toggleBtn = el('button', 'sprint-toggle');
    const isCollapsed = collapsedSprints.has(sprint.id);
    toggleBtn.textContent = isCollapsed ? '▶' : '▼';
    toggleBtn.setAttribute('aria-label', I18n.t(isCollapsed ? 'sprint.expand' : 'sprint.collapse'));
    toggleBtn.addEventListener('click', () => {
      const lane = toggleBtn.closest('.swimlane');
      const cols = lane?.querySelector('.swimlane-columns');
      if (!cols) return;
      if (collapsedSprints.has(sprint.id)) {
        collapsedSprints.delete(sprint.id);
        cols.style.display = '';
        toggleBtn.textContent = '▼';
        toggleBtn.setAttribute('aria-label', I18n.t('sprint.collapse'));
      } else {
        collapsedSprints.add(sprint.id);
        cols.style.display = 'none';
        toggleBtn.textContent = '▶';
        toggleBtn.setAttribute('aria-label', I18n.t('sprint.expand'));
      }
    });
    header.appendChild(toggleBtn);

    const titleEl = el('span', 'sprint-title');
    titleEl.textContent = sprint.title;

    /* F-2142 (Karte #826, ADR-0141 §4/§8): Gespiegelte Lane einer externen
       Kopie — schlanker Kopf wie beim Service-Board: kein Ziehgriff, kein
       Umbenennen, Löschen, Umschalten der Art, Archivieren, keine Velocity.
       Aufbau und Sprint-Mechanik gehören dem internen Board. */
    if (IS_SERVER_BUILD && markeWirkt(sprint)) {
      header.classList.add('sprint-header--spiegel');
      const laneHinweis = rueckwegAn() ? 'lane.spiegel.hint.rueckweg' : 'lane.spiegel.hint';
      titleEl.title = I18n.t(laneHinweis);
      header.appendChild(titleEl);
      const b = el('span', 'card-flag-badge sprint-spiegel-badge');
      b.style.cssText = SPIEGEL_ABZEICHEN_STIL;
      b.textContent = I18n.t('card.spiegel');
      b.title = I18n.t(laneHinweis);
      header.appendChild(b);
      if (sprint.archived) {
        const pille = el('span', 'sprint-archived-pill');
        pille.textContent = I18n.t('sprint.archived-pill');
        header.appendChild(pille);
      }
      return header;
    }

    if (sprint.id !== '__backlog__') {
      /* ADR-0092: Service-Board-Header bekommt 📬-Icon zur Erkennung, aber
         keine Rename-/Delete-/Kind-Toggle-Kontrollen (Server-Wahrheit, nicht
         über die UI veränderbar). Frühe Rückgabe via separater Pfad. */
      if (kind === 'service') {
        const icon = el('span', 'sprint-service-icon');
        icon.textContent = '📬';
        icon.setAttribute('role', 'img');
        icon.setAttribute('aria-label', I18n.t('service.board.icon-label'));
        header.appendChild(icon);
        titleEl.title = sprint.title;
        header.appendChild(titleEl);
        return header;
      }
      /* #326: Drag-Handle für Swimlane-Reordering — nur kind:sprint|lane, nie __backlog__
         (oben ausgeschlossen) und nie kind:service (oben mit return). DragDrop-Logik
         in src/dragdrop.js (onPointerDown sucht .sprint-drag-handle). */
      const dragHandle = el('span', 'sprint-drag-handle');
      dragHandle.textContent = '⋮⋮';
      dragHandle.setAttribute('role', 'button');
      dragHandle.setAttribute('aria-label', I18n.t('sprint.drag-handle'));
      dragHandle.setAttribute('title', I18n.t('sprint.drag-handle'));
      dragHandle.setAttribute('tabindex', '0');
      /* F-388 (Karte #344): Bis Sprint 54 war dieses Element ein Knopf, der
         nichts tat — `role="button"` + `tabindex="0"` ohne Keydown-Handler.
         Wer mit der Tastatur navigiert, landete darauf, der Screenreader
         meldete „Schaltfläche", und keine Taste löste etwas aus. Exakt das
         F-337-Muster aus Sprint 39.

         Pfeil hoch/runter verschiebt jetzt direkt eine Position. Bewusst
         KEIN Positions-Wähler-Dialog (die Karte liess beides offen): Der
         Maus-Weg verschiebt auch schrittweise, und ein zweiter Mechanismus
         mit eigener Bedienlogik wäre mehr zu lernen, nicht weniger. */
      dragHandle.setAttribute('aria-describedby', 'swimlane-move-hint');
      dragHandle.addEventListener('keydown', (e) => {
        if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
        /* preventDefault NUR im Handler — sonst wäre das Seiten-Scrollen mit
           den Pfeiltasten global blockiert (Hinweis im agentContext). */
        e.preventDefault();
        verschiebeSwimlanePerTaste(sprint, e.key === 'ArrowUp' ? -1 : 1);
      });
      header.appendChild(dragHandle);
      /* Punkt 5 (Karte #126): title zeigt vollen Namen für Truncation-Tooltip.
       * Rename-Hinweis wandert in ein visuell verstecktes Span (aria-describedby). */
      titleEl.title = sprint.title;
      const renameHintId = `rename-hint-${sprint.id}`;
      const renameHint = el('span', 'visually-hidden');
      renameHint.id = renameHintId;
      renameHint.textContent = I18n.t('col.rename-tip');
      header.appendChild(renameHint);
      titleEl.setAttribute('aria-describedby', renameHintId);
      titleEl.setAttribute('tabindex', '0');
      titleEl.setAttribute('role', 'button');
      titleEl.addEventListener('keydown', e => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); titleEl.click(); }
      });
      titleEl.addEventListener('click', () => {
        const input = el('input');
        input.className = 'sprint-title-input';
        input.value = sprint.title;
        header.replaceChild(input, titleEl);
        input.focus();
        input.select();

        let saved = false;
        function save() {
          if (saved) return;
          saved = true;
          EventBus.emit('sprint:rename', { sprintId: sprint.id, title: input.value.trim() || sprint.title });
        }
        input.addEventListener('keydown', e => {
          if (e.key === 'Enter')  { e.preventDefault(); save(); }
          if (e.key === 'Escape') { saved = true; EventBus.emit('sprint:rename', { sprintId: sprint.id, title: sprint.title }); }
        });
        input.addEventListener('blur', save);
      });

      const deleteBtn = el('button', 'btn-sprint-delete');
      /* Karte #165: × → 🗑 (konsistent zu btn-col-delete und Modal-card-delete-SVG-Trash).
         Karte #174 / F-231: Emoji im aria-hidden-Span — Doppel-Vorlesen vermeiden. */
      const sprintDelGlyph = document.createElement('span');
      sprintDelGlyph.textContent = '🗑';
      sprintDelGlyph.setAttribute('aria-hidden', 'true');
      deleteBtn.appendChild(sprintDelGlyph);
      /* Karte #157: bei kind=lane eigene Strings — sonst steht „Sprint XYZ löschen" für Lanes.
         F-218 (Karte #162): defensiver — explizite Bedingung statt String-Default.
         Backlog-Swimlanes haben kein delete-Button (Header rendered nur den Titel),
         daher hier nur 'sprint' / 'lane' relevant. Unbekannte kinds → warn + default. */
      let delPrefix;
      if (kind === 'lane') delPrefix = 'lane';
      else if (kind === 'sprint') delPrefix = 'sprint';
      else {
        console.warn(`Unbekannter swimlane.kind="${kind}" für Sprint ${sprint.id}, fallback 'sprint'`);
        delPrefix = 'sprint';
      }
      deleteBtn.title = I18n.t(`${delPrefix}.delete-title`);
      deleteBtn.setAttribute('aria-label', I18n.t(`${delPrefix}.delete-title`));
      deleteBtn.addEventListener('click', () => {
        const n = sprint.cards.filter(c => c.kind !== 'sprint-meta').length;
        const msg = n > 0
          ? I18n.t(`${delPrefix}.delete-confirm`, sprint.title, n)
          : I18n.t(`${delPrefix}.delete-confirm-empty`, sprint.title);
        if (confirm(msg)) EventBus.emit('sprint:delete', { sprintId: sprint.id });
      });
      header.appendChild(titleEl);

      /* ADR-0046 / Karte #145: Sprint↔Lane-Toggle als Icon-Button (statt natives Select). */
      const kindBtn = el('button', 'btn-sprint-kind');
      kindBtn.type = 'button';
      const isLane = kind === 'lane';
      kindBtn.textContent = isLane ? '🛣️' : '⏱️';
      kindBtn.dataset.kind = isLane ? 'lane' : 'sprint';
      const tipKey = isLane ? 'swimlane.toggle.to-sprint' : 'swimlane.toggle.to-lane';
      kindBtn.title = I18n.t(tipKey);
      kindBtn.setAttribute('aria-label', I18n.t(tipKey));
      kindBtn.addEventListener('click', () => {
        const next = kindBtn.dataset.kind === 'lane' ? 'sprint' : 'lane';
        /* F-216, zweite Haelfte: Der Bestaetigungstext sagte pauschal
           „Meta-Karten bleiben als Historie". Das stimmt nur fuer Meta-Karten
           MIT Inhalt: Der Cleanup-Pass in `migrateSprintMeta` entfernt beim
           naechsten Board-Load genau die leeren aus Lanes (F-364 hat den Test
           dafuer von `comments.length` auf `istMetaKarteLeer` erweitert, aber
           leere Karten fallen weiterhin weg). Fuer einen frisch angelegten
           Sprint — dessen Meta-Karten typischerweise leer sind — versprach der
           Dialog also das Gegenteil dessen, was passiert, und beim
           Zurueckschalten entstehen neue Karten mit neuer displayId.
           Deshalb hier dieselbe Frage stellen, die der Cleanup-Pass
           beantwortet, und den Text danach waehlen. */
        if (next === 'lane') {
          const metaKarten = sprint.cards.filter(c => c.kind === 'sprint-meta');
          const bleibtEtwas = metaKarten.some(c => !istMetaKarteLeer(c));
          const key = bleibtEtwas
            ? 'swimlane.toggle.confirm-to-lane'
            : 'swimlane.toggle.confirm-to-lane-leer';
          if (!confirm(I18n.t(key))) return;
        }
        /* F-216: lane→sprint ohne vorhandene Meta-Karten → Recovery-Confirm.
           Neue Meta-Karten bekommen eine neue displayId; frühere Recap-Inhalte
           gelöschter Meta-Karten sind nicht wiederherstellbar. */
        if (next === 'sprint') {
          const hasMeta = sprint.cards.some(c => c.kind === 'sprint-meta');
          if (!hasMeta && !confirm(I18n.t('swimlane.toggle.confirm-to-sprint-no-meta'))) return;
        }
        EventBus.emit('sprint:set-kind', { sprintId: sprint.id, kind: next });
      });
      header.appendChild(kindBtn);

      /* ADR-0117 §5 (Karte #346): Archiv-Umschalter. Der Ausschluss von
         __backlog__ und kind:'service' ergaebe sich hier zwar schon aus der
         Struktur (beide kehren vorher zurueck), wird aber ausdruecklich ueber
         denselben Test gefahren wie der Event-Handler — sonst sind es zwei
         Guards, die nur zufaellig dasselbe sagen. */
      if (Model.darfArchiviertWerden(sprint)) {
      const archivBtn = el('button', 'btn-sprint-archive');
      const istArchiviert = !!sprint.archived;
      archivBtn.type = 'button';
      const archivGlyph = document.createElement('span');
      archivGlyph.textContent = '📦';
      archivGlyph.setAttribute('aria-hidden', 'true');
      archivBtn.appendChild(archivGlyph);
      const archivKey = istArchiviert ? 'sprint.unarchive' : 'sprint.archive';
      archivBtn.title = I18n.t(archivKey);
      archivBtn.setAttribute('aria-label', I18n.t(archivKey));
      archivBtn.setAttribute('aria-pressed', istArchiviert ? 'true' : 'false');
      archivBtn.addEventListener('click', () => {
        if (!istArchiviert) {
          /* ADR-0117 §6: Kein Verbot, aber auch keine Ueberraschung. Meta-Karten
             zaehlen nicht mit — sie sind Buchhaltung, keine offene Arbeit.

             Die Erledigt-Erkennung laeuft ueber `isDoneColumn` (Titel-Semantik),
             NICHT ueber einen Vergleich mit der id `col-erledigt`. Die erste
             Fassung dieser Zeile tat genau das — und wiederholte damit im selben
             Sprint den Fehler, den Karte #405 (F-141) zwei Commits vorher an der
             Datums-Badge beseitigt hatte: eine manuell angelegte oder umbenannte
             Erledigt-Spalte traegt eine UUID-id, und der Vergleich haette sie
             als „noch offen" gezaehlt. Der Dialog haette dann vor Karten
             gewarnt, die die App an drei anderen Stellen als erledigt fuehrt
             (Sprint-56-Kritiker, F-654). */
          const spaltenNachId = new Map(
            (columnDefs || State.get()?.board?.columnDefs || []).map(cd => [cd.id, cd]));
          const offen = sprint.cards.filter(c =>
            c.kind !== 'sprint-meta' && !c.archived
            && !isDoneColumn(spaltenNachId.get(c.columnId))).length;
          if (offen > 0 && !confirm(I18n.t('sprint.archive-confirm-offen', sprint.title, offen))) return;
        }
        EventBus.emit('sprint:set-archived', { sprintId: sprint.id, archived: !istArchiviert });
      });
      header.appendChild(archivBtn);

      /* Pille: macht im eingeblendeten Zustand sichtbar, WARUM diese Swimlane
         eingeklappt ist — sonst sieht sie aus wie jede andere zugeklappte. */
      if (istArchiviert) {
        const pille = el('span', 'sprint-archived-pill');
        pille.textContent = I18n.t('sprint.archived-pill');
        header.appendChild(pille);
      }
      }

      header.appendChild(deleteBtn);
    } else {
      header.appendChild(titleEl);
    }

    /* Velocity-Widget — nur für echte Sprints (ADR-0046: Lanes/Backlog ohne Velocity) */
    if (kind === 'sprint') {
      /* F-1085: board mitgeben, damit isDoneColumnId() die echten columnDefs sieht */
      const metrics = Metrics.computeSprintMetrics(sprint, board);
      const velocityEl = buildVelocityWidget(metrics);
      if (velocityEl) header.appendChild(velocityEl);
    }

    return header;
  }

  /**
   * Baut das Velocity-Widget für den Sprint-Header.
   * Zeigt immer Karten-Counter mit Icon; bei Effort-Daten zusätzlich h-Anzeige.
   * Sparkline wurde durch die Progress-Linie (buildSprintProgressLine) ersetzt
   * um den 100 %-Bug bei ungeschätzten offenen Karten zu vermeiden (Karte #126).
   */
  function buildVelocityWidget(metrics) {
    const wrap = el('div', 'sprint-velocity');

    /* Karten-Counter mit Icon — Karte #154: SVG-Icon (currentColor) statt 🗂️-Emoji, damit es farblich
       zum Rest des Header-Counters passt (var(--text-muted)). */
    const counter = el('span', 'sprint-velocity-counter');
    counter.setAttribute('aria-label',
      I18n.t('metrics.cards-aria', metrics.doneCount, metrics.cardCount));
    counter.setAttribute('title',
      I18n.t('metrics.cards', metrics.doneCount, metrics.cardCount));

    const iconEl = el('span', 'sprint-velocity-icon');
    iconEl.appendChild(cardsIcon(12));
    iconEl.setAttribute('aria-hidden', 'true');

    const numEl = el('span', 'sprint-velocity-num');
    numEl.textContent = `${metrics.doneCount}/${metrics.cardCount}`;

    counter.appendChild(iconEl);
    counter.appendChild(numEl);
    wrap.appendChild(counter);

    if (!metrics.hasEffortData) return wrap;

    /* Effort-Anzeige */
    const effortEl = el('span', 'sprint-velocity-effort');
    effortEl.textContent = I18n.t('metrics.effort-done', metrics.doneEffort, metrics.totalEffort);
    wrap.appendChild(effortEl);

    return wrap;
  }

  /**
   * Baut die Sprint-Progress-Linie (dünner Balken unter dem Sprint-Header).
   * Wird als Geschwister-Element zum Header in der Swimlane gemounted — NICHT
   * innerhalb von .sprint-header, damit sie beim Collapse-Toggle nicht mitblendet.
   *
   * Ersetzt die Sparkline als primärer Fortschritts-Indikator (Karte #126):
   * Basis ist cardCount (alle Karten), nicht nur geschätzte — damit kein falsches
   * 100 % wenn geschätzte Karten alle erledigt aber ungeschätzte noch offen sind.
   */
  function buildSprintProgressLine(metrics) {
    const line = el('div', 'sprint-progress-line');
    const fill = el('div', 'sprint-progress-fill');
    const ratio = metrics.cardCount > 0
      ? (metrics.doneCount / metrics.cardCount) * 100
      : 0;
    fill.style.width = `${ratio.toFixed(2)}%`;
    line.appendChild(fill);
    return line;
  }

  /* ── Spalte-Hinzufügen — Popover ────────────────────────────────── */

  let colPopover = null;

  function buildPopover(placeholder, ariaLabel, onCommit, onClose) {
    const pop = el('div', 'col-add-popover');
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-modal', 'true');
    pop.setAttribute('aria-label', ariaLabel);

    const input = el('input');
    input.type = 'text';
    input.className = 'col-add-popover-input';
    input.placeholder = placeholder;

    const row = el('div', 'col-add-popover-row');
    const okBtn = el('button', 'btn btn-sm btn-primary');
    okBtn.type = 'button';
    okBtn.textContent = I18n.t('btn.add-card-confirm');
    const noBtn = el('button', 'btn btn-sm');
    noBtn.type = 'button';
    noBtn.textContent = I18n.t('btn.cancel');
    row.appendChild(okBtn);
    row.appendChild(noBtn);
    pop.appendChild(input);
    pop.appendChild(row);

    /* Focus-Trap (F-50) */
    pop.addEventListener('keydown', e => {
      if (e.key !== 'Tab') return;
      const focusable = [input, okBtn, noBtn];
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    });

    function commit() {
      const title = input.value.trim();
      onClose();
      if (title) onCommit(title);
    }

    input.addEventListener('keydown', e => {
      if (e.key === 'Enter')  { e.preventDefault(); commit(); }
      if (e.key === 'Escape') onClose();
    });
    okBtn.addEventListener('click', commit);
    noBtn.addEventListener('click', onClose);

    return { pop, input };
  }

  function openColPopover(anchorBtn) {
    closeColPopover();

    const { pop, input } = buildPopover(
      I18n.t('col.add-placeholder'), I18n.t('col.add'),
      title => EventBus.emit('col:quick-add', { title }),
      closeColPopover,
    );

    const rect = anchorBtn.getBoundingClientRect();
    pop.style.top   = `${rect.bottom + 6}px`;
    pop.style.right = `${document.documentElement.clientWidth - rect.right}px`;
    document.body.appendChild(pop);
    /* Einblend-Animation: Klasse im nächsten Frame setzen */
    requestAnimationFrame(() => pop.classList.add('popover--open'));
    colPopover = pop;
    input.focus();

    setTimeout(() => document.addEventListener('pointerdown', onOutside, { once: true }), 0);
    function onOutside(e) {
      if (colPopover && !colPopover.contains(e.target) && e.target !== anchorBtn) {
        closeColPopover();
      } else if (colPopover) {
        document.addEventListener('pointerdown', onOutside, { once: true });
      }
    }
  }

  function closeColPopover() {
    colPopover?.remove();
    colPopover = null;
  }

  /* ── Overflow-Menü (mobile) ─────────────────────────────────────── */

  let overflowMenu = null;
  let overflowOutsideHandler = null;
  let mobileFilterVisible = false;

  function openOverflowMenu(anchorBtn) {
    if (overflowMenu) { closeOverflowMenu(); return; }
    const menu = el('div', 'overflow-menu');

    function item(text, onClick) {
      const btn = el('button', 'overflow-menu-item');
      btn.textContent = text;
      /* click statt pointerdown — feuert erst nach pointerup, kein Konflikt mit Outside-Handler */
      btn.addEventListener('click', () => { closeOverflowMenu(); onClick(); });
      return btn;
    }

    menu.appendChild(item(I18n.t('col.add'),         () => openColPopover(anchorBtn)));
    menu.appendChild(item(I18n.t('btn.add-sprint'),  () => openSprintPopover(anchorBtn)));
    /* Karte #176: Open/Export/Config liegen jetzt in der App-Sidebar
       (Logo-Klick öffnet sie). Mobile-Overflow zeigt die als Shortcut zur
       Sidebar — ein Klick weniger als Logo + Eintrag. */
    menu.appendChild(item(I18n.t('sidebar.open'),    () => EventBus.emit('sidebar:open')));

    /* Trennlinie + Filter-Toggle */
    const sep = el('div', 'overflow-menu-sep');
    menu.appendChild(sep);
    menu.appendChild(item(
      I18n.t(mobileFilterVisible ? 'filter.hide' : 'filter.show'),
      () => {
        mobileFilterVisible = !mobileFilterVisible;
        const fb = document.querySelector('#board-header .header-filter');
        if (fb) fb.classList.toggle('filter-visible', mobileFilterVisible);
      }
    ));

    document.body.appendChild(menu);
    overflowMenu = menu;

    const rect = anchorBtn.getBoundingClientRect();
    menu.style.top   = `${rect.bottom + 4}px`;
    menu.style.right = `${window.innerWidth - rect.right}px`;
    /* Einblend-Animation: Klasse im nächsten Frame setzen */
    requestAnimationFrame(() => menu.classList.add('popover--open'));

    /* Keyboard-Navigation (F-94) */
    menu.addEventListener('keydown', (e) => {
      const items = [...menu.querySelectorAll('.overflow-menu-item')];
      const idx   = items.indexOf(document.activeElement);
      if (e.key === 'ArrowDown') { e.preventDefault(); items[(idx + 1) % items.length]?.focus(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); items[(idx - 1 + items.length) % items.length]?.focus(); }
      else if (e.key === 'Home')   { e.preventDefault(); items[0]?.focus(); }
      else if (e.key === 'End')    { e.preventDefault(); items[items.length - 1]?.focus(); }
      else if (e.key === 'Escape') { closeOverflowMenu(); anchorBtn.focus(); }
      else if (e.key === 'Tab')    { e.preventDefault(); closeOverflowMenu(); anchorBtn.focus(); }
    });
    menu.querySelector('.overflow-menu-item')?.focus();

    /* Outside-Click: nur schließen wenn Klick AUSSERHALB des Menüs */
    overflowOutsideHandler = (e) => {
      if (overflowMenu && !overflowMenu.contains(e.target)) closeOverflowMenu();
    };
    setTimeout(() => document.addEventListener('pointerdown', overflowOutsideHandler), 0);
  }

  function closeOverflowMenu() {
    overflowMenu?.remove();
    overflowMenu = null;
    if (overflowOutsideHandler) {
      document.removeEventListener('pointerdown', overflowOutsideHandler);
      overflowOutsideHandler = null;
    }
  }

  /* ── Sprint-Hinzufügen — Popover ────────────────────────────────── */

  let sprintPopover = null;

  function openSprintPopover(anchorBtn) {
    closeSprintPopover();

    /* ADR-0046: Mini-Select im Popover, um zwischen Sprint und Lane zu wählen. */
    const kindRef = { value: 'sprint' };
    const { pop, input } = buildPopover(
      I18n.t('sprint.add-placeholder'), I18n.t('btn.add-sprint'),
      title => EventBus.emit('sprint:add', { title, kind: kindRef.value }),
      closeSprintPopover,
    );

    /* Kind-Auswahl oberhalb der Action-Row einfügen */
    const kindRow = el('div', 'col-add-popover-row sprint-kind-row');
    const kindSel = el('select', 'sprint-kind-select');
    kindSel.setAttribute('aria-label', I18n.t('swimlane.kind.label'));
    const optSprint = document.createElement('option');
    optSprint.value = 'sprint';
    optSprint.textContent = I18n.t('swimlane.add.sprint');
    const optLane = document.createElement('option');
    optLane.value = 'lane';
    optLane.textContent = I18n.t('swimlane.add.lane');
    kindSel.appendChild(optSprint);
    kindSel.appendChild(optLane);
    kindSel.addEventListener('change', () => { kindRef.value = kindSel.value; });
    kindRow.appendChild(kindSel);
    /* Vor der bestehenden OK/Abbrechen-Row einfügen */
    const actionRow = pop.querySelector('.col-add-popover-row');
    pop.insertBefore(kindRow, actionRow);

    const rect = anchorBtn.getBoundingClientRect();
    pop.style.top   = `${rect.bottom + 6}px`;
    pop.style.right = `${document.documentElement.clientWidth - rect.right}px`;
    document.body.appendChild(pop);
    /* Einblend-Animation: Klasse im nächsten Frame setzen */
    requestAnimationFrame(() => pop.classList.add('popover--open'));
    sprintPopover = pop;
    input.focus();

    setTimeout(() => document.addEventListener('pointerdown', onOutside, { once: true }), 0);
    function onOutside(e) {
      if (sprintPopover && !sprintPopover.contains(e.target) && e.target !== anchorBtn) {
        closeSprintPopover();
      } else if (sprintPopover) {
        document.addEventListener('pointerdown', onOutside, { once: true });
      }
    }
  }

  function closeSprintPopover() {
    sprintPopover?.remove();
    sprintPopover = null;
  }

  /* ── Header ──────────────────────────────────────────────────────── */

  function renderHeader(board) {
    closeOverflowMenu();
    const header = document.getElementById('board-header');

    /* Karte #56-Followup: Re-Render würde sonst Fokus + Caret aus dem
       Such-Input verlieren — User-Tippen reißt nach 150ms Debounce ab. */
    const ae = document.activeElement;
    const searchHadFocus = ae?.id === 'search-input';
    const searchSelStart = searchHadFocus ? ae.selectionStart : null;
    const searchSelEnd   = searchHadFocus ? ae.selectionEnd   : null;

    /* F-1732: ein laufendes Inline-Umbenennen übersteht den Neuaufbau — Wert und
       Auswahl merken, das Feld nach dem Neubau wieder öffnen. */
    const ri = document.getElementById('board-title-input');
    /* F-1737: nur ein noch offenes Feld desselben Boards wird wiederhergestellt;
       ein per Enter/Blur/Esc bereits beendetes (dataset.zu) oder fremdes
       (Board-Wechsel) wird verworfen. */
    const rename = ri && !ri.dataset.zu && ri.dataset.boardId === board.id && [ri.value, ri.selectionStart, ri.selectionEnd];
    if (ri) ri.onblur = null; /* blur beim Entfernen speichert nicht */

    clear(header);

    const logo = document.createElement('span');
    logo.className = 'board-logo';
    logo.innerHTML = logoSvg; /* Build-Asset via Vite ?raw — kein Nutzerdatum, ADR-0009-Ausnahme (F-71) */
    /* Karte #176: Logo öffnet die App-Sidebar (Hamburger-Geste auf Mobile,
       Schnellzugriff auf Desktop). */
    logo.setAttribute('role', 'button');
    logo.setAttribute('tabindex', '0');
    logo.setAttribute('aria-label', I18n.t('sidebar.open'));
    logo.title = I18n.t('sidebar.open');
    logo.addEventListener('click', () => EventBus.emit('sidebar:open'));
    logo.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        EventBus.emit('sidebar:open');
      }
    });
    header.appendChild(logo);

    /* Karte #704: Titel = Knopf mit Board-Umschalter (Klick), Stift zum
       Umbenennen daneben (board-menu.js). Das Inline-Umbenennen bleibt hier. */
    const title = BoardMenu.build(board, { onRename: startRename });
    header.appendChild(title);
    if (rename) startRename(title, rename);

    function startRename(wrapEl, alt) {
      const input = el('input');
      input.id = 'board-title-input';
      input.value = alt ? alt[0] : board.title;
      input.dataset.boardId = board.id;
      header.replaceChild(input, wrapEl);
      input.focus();
      alt ? input.setSelectionRange(alt[1], alt[2]) : input.select();

      let saved = false;
      /* F-1737: vor dem Emit als beendet markieren — der Emit rendert synchron neu. */
      function beenden(title) {
        saved = true;
        input.dataset.zu = '1';
        EventBus.emit('board:title-change', { title });
      }
      function save() {
        if (!saved) beenden(input.value.trim() || board.title);
      }
      input.addEventListener('keydown', e => {
        if (e.key === 'Enter')  { e.preventDefault(); save(); }
        if (e.key === 'Escape') { beenden(board.title); }
      });
      input.onblur = save;
    }

    /* Karte #176/#704: Settings, Öffnen, Export liegen in der Sidebar (Logo-Klick
       öffnet sie), der Board-Switch im Titel-Menü. Hier verbleiben nur die
       board-bezogenen Quick-Actions (Add-Col, Add-Sprint, Save). */
    const actions = el('div', 'header-actions');

    const addColBtn = el('button', 'btn btn-icon hide-mobile');
    addColBtn.title = I18n.t('col.add');
    addColBtn.setAttribute('aria-label', I18n.t('col.add'));
    addColBtn.textContent = '＋';
    addColBtn.addEventListener('click', () => openColPopover(addColBtn));

    const addSprintBtn = el('button', 'btn btn-icon hide-mobile');
    addSprintBtn.title = I18n.t('btn.add-sprint');
    addSprintBtn.setAttribute('aria-label', I18n.t('btn.add-sprint'));
    addSprintBtn.textContent = '⊞';
    addSprintBtn.addEventListener('click', () => openSprintPopover(addSprintBtn));

    const saveBtn = el('button', 'btn btn-primary');
    saveBtn.id = 'btn-save';
    saveBtn.textContent = I18n.t('btn.save');
    saveBtn.addEventListener('click', () => EventBus.emit('board:save'));

    const moreBtn = el('button', 'btn btn-icon show-mobile');
    moreBtn.textContent = '⋯';
    moreBtn.title = I18n.t('btn.more');
    moreBtn.setAttribute('aria-label', I18n.t('btn.more'));
    moreBtn.addEventListener('click', () => openOverflowMenu(moreBtn));

    /* ── Filter-Controls (inline, zwischen Titel und Buttons) ───── */
    const filterBar = el('div', 'header-filter');

    const owners = new Set();
    const labels = new Set();
    board.sprints.forEach(s => s.cards.forEach(c => {
      if (c.owner) owners.add(c.owner);
      c.labels?.forEach(l => labels.add(l));
    }));
    const f = Filter.get();

    function filterSelect(labelKey, current, options, onChange) {
      const sel = el('select', current ? 'filter-select filter-select--active' : 'filter-select');
      sel.title = I18n.t(labelKey);
      const allOpt = document.createElement('option');
      allOpt.value = '';
      allOpt.textContent = `${I18n.t(labelKey)}: ${I18n.t('filter.all')}`;
      sel.appendChild(allOpt);
      options.forEach(o => {
        const opt = document.createElement('option');
        opt.value = o;
        opt.textContent = o;
        if (o === current) opt.selected = true;
        sel.appendChild(opt);
      });
      sel.addEventListener('change', () => { onChange(sel.value); EventBus.emit('filter:changed'); });
      return sel;
    }

    function flagBtn(flag, symbol, titleKey, zusatz = '') {
      const btn = el('button', f.flags.has(flag) ? 'btn btn-sm filter-flag filter-flag--active' : 'btn btn-sm filter-flag');
      btn.textContent = symbol;
      btn.title = I18n.t(titleKey);
      btn.setAttribute('aria-label', zusatz ? `${I18n.t(titleKey)}: ${zusatz}` : I18n.t(titleKey)); /* zusatz: Zähler im zugänglichen Namen (F-2409) */
      btn.setAttribute('aria-pressed', String(f.flags.has(flag)));
      btn.addEventListener('click', () => { Filter.set({ flagToggle: flag }); EventBus.emit('filter:changed'); });
      return btn;
    }

    if (owners.size > 1) filterBar.appendChild(filterSelect('filter.owner', f.owner, [...owners].sort(), v => Filter.set({ owner: v })));
    if (labels.size)     filterBar.appendChild(filterSelect('filter.label', f.label, [...labels].sort(), v => Filter.set({ label: v })));
    /* ADR-0133 §1: Finding-Filter nur auf Boards mit Findings; Wortschatz aus finding.js */
    if (board.sprints.some(s => s.cards.some(c => c.kind === 'finding'))) {
      [['typ', FINDING_TYPEN], ['prioritaet', FINDING_PRIORITAETEN], ['status', FINDING_STATUS]].forEach(([k, liste]) =>
        filterBar.appendChild(filterSelect('finding.' + (k === 'prioritaet' ? 'prio' : k), f.finding[k] || '', liste,
          v => Filter.set({ finding: [k, v] }))));
    }
    filterBar.appendChild(flagBtn('critical', '⚡', 'filter.critical'));
    filterBar.appendChild(flagBtn('blocked',  '⊘',  'filter.blocked'));
    if (IS_SERVER_BUILD) {
      /* Karte #798: Übersicht „von Agenten nach außen“ — nur, wenn es solche
         Karten gibt (die Marke setzt der Server nur innen) oder der Filter an ist. */
      const n = board.sprints.reduce((summe, s) => summe + s.cards.filter(c => !c.archived && vonAgentNachAussen(c)).length, 0);
      if (n || f.flags.has('vonAgent')) filterBar.appendChild(flagBtn('vonAgent', '🤖 ' + n, 'filter.vonAgent', String(n)));
    }

    if (Filter.isActive()) {
      const clearBtn = el('button', 'btn btn-sm filter-clear');
      clearBtn.textContent = '×';
      clearBtn.title = I18n.t('filter.clear');
      clearBtn.setAttribute('aria-label', I18n.t('filter.clear'));
      clearBtn.addEventListener('click', () => { Filter.clear(); EventBus.emit('filter:changed'); });
      filterBar.appendChild(clearBtn);
    }

    /* ── Such-Leiste (Karte #56) ─────────────────────────────────── */
    const searchBar = el('div', 'header-search');

    const searchInput = el('input', 'search-input');
    searchInput.type = 'text';
    searchInput.id = 'search-input';
    searchInput.placeholder = I18n.t('search.placeholder');
    searchInput.setAttribute('aria-label', I18n.t('search.aria-label'));
    searchInput.value = Search.getQuery();
    searchInput.addEventListener('input', () => {
      Search.setQuery(searchInput.value);
    });
    /* Escape leert die Suche und gibt Fokus frei */
    searchInput.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        Search.clear();
        searchInput.value = '';
        searchInput.blur();
      }
    });
    searchBar.appendChild(searchInput);

    /* Karte #56-Followup: Fokus + Caret aus altem Such-Input wiederherstellen. */
    if (searchHadFocus) {
      requestAnimationFrame(() => {
        searchInput.focus();
        try { searchInput.setSelectionRange(searchSelStart, searchSelEnd); } catch {}
      });
    }

    if (Search.isActive()) {
      const searchClearBtn = el('button', 'btn btn-sm search-clear');
      searchClearBtn.textContent = '×';
      searchClearBtn.title = I18n.t('search.clear');
      searchClearBtn.setAttribute('aria-label', I18n.t('search.clear'));
      searchClearBtn.addEventListener('click', () => {
        Search.clear();
        EventBus.emit('search:changed');
      });
      searchBar.appendChild(searchClearBtn);
    }

    actions.appendChild(addColBtn);
    actions.appendChild(addSprintBtn);
    actions.appendChild(moreBtn);
    actions.appendChild(saveBtn);
    header.appendChild(filterBar);
    header.appendChild(searchBar);
    header.appendChild(actions);

    /* ── Header-Avatar (Karte #201, ADR-0059) ─────────────────────────
       Kompakter Initialen-Kreis (30px) rechts in der Kopfzeile.
       Nur sichtbar wenn Forward-Auth AN und ein identifizierter User vorhanden.
       Legacy-Bypass (kein User): kein Avatar, kein Crash.
       Async/fire-and-forget — Header ist schon komplett, Avatar kommt nach.
       #328: IS_SERVER_BUILD-Gate → DCE im slim Distributable. */
    if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) {
      const plugin = StoragePlugin.getActivePlugin();
      if (typeof plugin?.getCurrentUser === 'function') {
        plugin.getCurrentUser().then(user => {
          if (!user || (user.name == null && user.email == null)) return;
          const label = user.name || user.email;
          const color = colorForLabel(label);
          const initial = initialOf(label);
          const textColor = getContrastColor(color);

          const avatarEl = document.createElement('span');
          avatarEl.className = 'board-header-avatar';
          avatarEl.style.background = color;
          /* Name als Hover-Tooltip — kein sichtbarer Name-Text im DOM */
          avatarEl.title = user.name || user.email;
          avatarEl.setAttribute('aria-label', user.name || user.email);
          avatarEl.setAttribute('aria-hidden', 'false');

          const glyph = document.createElement('span');
          glyph.style.color = textColor;
          glyph.textContent = initial;
          glyph.setAttribute('aria-hidden', 'true');
          avatarEl.appendChild(glyph);

          /* Sicherheitscheck: Header noch im DOM (kein Re-Render dazwischen) */
          if (header.isConnected) header.appendChild(avatarEl);
        }).catch(() => { /* Netzwerkfehler → kein Avatar, kein Crash */ });
      }
    }
  }

  /* ── Board ───────────────────────────────────────────────────────── */

  /* ── FLIP-Animation für Karten-Drop + Auto-Reload (Karte #97) ───── */

  function prefersReducedMotion() {
    return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  }

  /* F-240: setTimeout-Handles für kosmetische Klassen-Cleanups (Sprint-Sweep,
     Drop-Pulse, Erledigt-Glow) zentral tracken. Ein Re-Render verwirft die
     alten DOM-Knoten — die noch laufenden Cleanup-Timer würden sonst auf
     detachte Knoten feuern. clearAnimTimers() (am Render-Anfang) bricht sie ab. */
  const pendingAnimTimers = new Set();
  /* Pulse-Highlight-Helper (#328): Klasse setzen, nach ms entfernen via
     trackedTimeout (Re-Render-resistent). */
  function pulseClass(el, cls, ms) {
    el.classList.add(cls);
    trackedTimeout(() => el.classList.remove(cls), ms);
  }

  function trackedTimeout(fn, ms) {
    const id = setTimeout(() => { pendingAnimTimers.delete(id); fn(); }, ms);
    pendingAnimTimers.add(id);
    return id;
  }
  function clearAnimTimers() {
    pendingAnimTimers.forEach(clearTimeout);
    pendingAnimTimers.clear();
  }

  /* Generischer FLIP-Snapshot: liest data-Attribut (id|sprintId) und liefert
     id→DOMRect-Map. Wird für .card und .swimlane gleichermaßen genutzt. */
  function snapshotPositions(selector, keyAttr) {
    const rects = new Map();
    document.querySelectorAll(selector).forEach(el => {
      const key = el.dataset[keyAttr];
      if (key) rects.set(key, el.getBoundingClientRect());
    });
    return rects;
  }

  /* Einzel-Pass der FLIP-Animation. onMatched? wird pro animiertem Element
     aufgerufen — Karten-Pass nutzt das für Pulse-/Erledigt-Highlights. */
  function flipPass(selector, keyAttr, rectsMap, onMatched) {
    if (!rectsMap || rectsMap.size === 0) return;
    document.querySelectorAll(selector).forEach(el => {
      const key = el.dataset[keyAttr];
      const before = rectsMap.get(key);
      if (!before) return;
      const after = el.getBoundingClientRect();
      const dx = before.left - after.left;
      const dy = before.top  - after.top;
      if (dx === 0 && dy === 0) return;
      el.style.transform = `translate(${dx}px, ${dy}px)`;
      el.style.transition = 'none';
      requestAnimationFrame(() => {
        el.style.transform = '';
        el.style.transition = 'transform 200ms ease-out';
        el.addEventListener('transitionend', () => {
          el.style.transition = '';
        }, { once: true });
      });
      onMatched?.(el, key);
    });
  }

  function flipAnimate(flipData) {
    if (!flipData) return;
    if (prefersReducedMotion()) return;
    const { rects, droppedCardId, laneRects } = flipData;
    flipPass('.card', 'id', rects, (cardEl, cardId) => {
      if (cardId !== droppedCardId) return;
      /* Pulse-Highlight für die gedropte Karte */
      pulseClass(cardEl, 'card--just-dropped', 440);
      /* Karte #175 (A): zusätzlicher Erledigt-Glow, wenn die Karte in
         einer „Erledigt"-Spalte landet. Erkennung über die Spalten-ID
         des umschließenden .column-Elements. */
      const colId = (cardEl.closest('.column')?.dataset.id || '').toLowerCase();
      if (colId.includes('erledigt') || colId.includes('done')) {
        pulseClass(cardEl, 'card--landed-erledigt', 640);
      }
    });
    /* #326: Swimlane-FLIP parallel zur Karten-FLIP. */
    flipPass('.swimlane', 'sprintId', laneRects);
  }

  /* ── awaitingUser-Banner (Karte #115) ───────────────────────────────── */

  /* Modul-State für Tab-Titel-Originalwert und Live-Update-Timer */
  let _originalTitle   = null;
  let _awaitingTimer   = null;

  /** Relative Zeitanzeige: ISO-Zeitstempel → deutsches Kurzformat */
  function relativeTime(iso) {
    const diffMs  = Date.now() - new Date(iso).getTime();
    const diffMin = Math.round(diffMs / 60_000);
    if (diffMin < 2)  return 'gerade eben';
    if (diffMin < 60) return `vor ${diffMin} min`;
    const diffH = Math.round(diffMin / 60);
    if (diffH < 24)   return `vor ${diffH} h`;
    const diffD = Math.round(diffH / 24);
    return `vor ${diffD} Tag${diffD === 1 ? '' : 'en'}`;
  }

  /** Die Person dieses Browsers: die Einstellung `userName`. Kein Rückfall
      auf die erste Nutzer-Identität (Session-Kritiker Sprint 78, O-2): Am
      Server setzt niemand `userName` automatisch, und auf einem Board mit
      mehreren Personen sähe Person B sonst die Frage von A und löschte sie.
      Den Einzelplatz-Fall deckt `eigeneAwaitingSchluessel` ab. */
  function eigenePerson() {
    const personen = [(Config.get('userName') || '').trim()];
    /* Verify Sprint 78, V-1: Am Server setzt niemand `userName`, und das Feld
       ist dort ausgeblendet — ohne diese Ergänzung erschiene der Banner am
       Server nie. Die Anmelde-Identität (Name und E-Mail aus
       getCurrentUser) zählt deshalb mit; sie wird einmal geholt, danach der
       Banner neu gezeichnet. */
    if (imServerModus()) {
      if (_serverIdentitaet) personen.push(_serverIdentitaet.name || '', _serverIdentitaet.email || '');
      else ladeServerIdentitaet();
    }
    return personen.filter(Boolean);
  }

  let _serverIdentitaet = null;
  let _identitaetLaedt = false;
  let _identitaetVersuch = 0;
  function ladeServerIdentitaet() {
    if (!IS_SERVER_BUILD || _identitaetLaedt) return;
    /* Verify Sprint 78, W-1: Bleibt die Identität leer (Legacy-Bypass,
       /me gestört), nicht bei jeder Zeichnung neu fragen — höchstens einmal
       je Minute. */
    if (Date.now() - _identitaetVersuch < 60_000) return;
    _identitaetVersuch = Date.now();
    const plugin = StoragePlugin.getActivePlugin();
    if (typeof plugin?.getCurrentUser !== 'function') return;
    _identitaetLaedt = true;
    plugin.getCurrentUser().then(u => {
      const id = { name: u?.name || '', email: u?.email || '' };
      /* Nur eine echte Identität merken — eine leere Antwort (Legacy-Bypass,
         noch nicht angemeldet) darf beim nächsten Zeichnen neu gefragt werden. */
      if (!id.name && !id.email) { _identitaetLaedt = false; return; }
      _serverIdentitaet = id;
      const b = State.get()?.board;
      if (b) renderAwaitingBanner(b);
    }).catch(() => { _identitaetLaedt = false; });
  }

  /* Server-Modus: keine Einzelplatz-Regel für awaitingUser (s. awaiting.js). */
  function imServerModus() {
    return IS_SERVER_BUILD && Profile.has('feature.restStorage');
  }

  /** Banner-Element erzeugen oder aktualisieren */
  function renderAwaitingBanner(board) {
    const existing = document.getElementById('awaiting-user-banner');
    /* ADR-0130 §7 (Karte #609): der Banner zeigt NUR den Eintrag der eigenen
       Person; die Fragen an andere Personen bleiben ihnen vorbehalten. */
    /* F-1336 (Sprint-81-Kritiker A-1): mit einem Eintrag je Chat kann es
       mehrere eigene geben. Gezeigt wird die JÜNGSTE Frage (since, dann
       Schlüssel — deterministisch), dazu die Zahl der übrigen. */
    const eigeneSchluessel = eigeneAwaitingSchluessel(board, eigenePerson(), { server: imServerModus() });
    const auSchluessel = [...eigeneSchluessel].sort((a, b) => {
      const sa = board.awaitingUser[a].since, sb = board.awaitingUser[b].since;
      return sa < sb ? 1 : sa > sb ? -1 : (a < b ? -1 : a > b ? 1 : 0);
    })[0];
    const au = auSchluessel === undefined ? null : board.awaitingUser[auSchluessel];
    const weitere = Math.max(0, eigeneSchluessel.length - 1);

    /* Timer in jedem Fall neu aufsetzen */
    if (_awaitingTimer) { clearInterval(_awaitingTimer); _awaitingTimer = null; }

    if (!au) {
      /* Flag nicht gesetzt — Banner entfernen, Titel wiederherstellen */
      existing?.remove();
      if (_originalTitle !== null) {
        document.title = _originalTitle;
        _originalTitle = null;
      }
      return;
    }

    /* Tab-Titel: Original sichern (nur beim ersten Setzen) und Prefix setzen.
       F-171: Dirty-Prefix ('* ') und vorhandenes ⚠️-Prefix vor dem Sichern entfernen,
       damit beim Clear der korrekte saubere Titel wiederhergestellt wird. */
    if (_originalTitle === null) {
      _originalTitle = document.title.replace(/^⚠️\s*/, '').replace(/^\*\s*/, '');
    }
    document.title = '⚠️ ' + _originalTitle;

    /* Snippet: erste 60 Zeichen der Frage */
    const snippet = au.question.length > 60 ? au.question.slice(0, 57) + '…' : au.question;

    function updateBannerTime(bannerEl) {
      const timeEl = bannerEl.querySelector('.awaiting-banner-time');
      if (timeEl) timeEl.textContent = relativeTime(au.since);
    }

    function updateBannerFrage(bannerEl) {
      const q = bannerEl.querySelector('.awaiting-banner-question');
      if (q) q.textContent = snippet;
      const mehr = bannerEl.querySelector('.awaiting-banner-weitere');
      if (mehr) mehr.textContent = weitere ? I18n.t('awaiting.weitere', weitere) : '';
    }

    if (existing) {
      /* Banner bereits vorhanden — Frage, Zahl der übrigen Chats und Zeit
         aktualisieren (A-1: vorher blieb der Text des ersten Eintrags stehen,
         auch wenn inzwischen ein anderer Chat wartete). */
      updateBannerFrage(existing);
      updateBannerTime(existing);
    } else {
      /* Neuen Banner erzeugen */
      const banner = el('div', 'awaiting-banner');
      banner.id = 'awaiting-user-banner';
      /* F-2876: kein role=status am frisch erzeugten Banner (wird oft nicht
         angesagt) — die Ansage läuft über die Live-Region in ansage.js. Das
         Banner bleibt les- und bedienbar (Knopf), daher kein aria-hidden. */
      ansagen(I18n.t('awaiting.prefix') + snippet + I18n.t('awaiting.suffix'));

      const text = el('span', 'awaiting-banner-text');
      const timeEl = el('span', 'awaiting-banner-time');
      timeEl.textContent = relativeTime(au.since);
      text.appendChild(document.createTextNode(I18n.t('awaiting.prefix')));
      const q = el('em', 'awaiting-banner-question');
      q.textContent = snippet;
      text.appendChild(q);
      text.appendChild(document.createTextNode(I18n.t('awaiting.suffix')));
      text.appendChild(timeEl);
      const mehr = el('span', 'awaiting-banner-weitere');
      mehr.textContent = weitere ? I18n.t('awaiting.weitere', weitere) : '';
      text.appendChild(mehr);

      const clearBtn = el('button', 'awaiting-banner-btn');
      clearBtn.type = 'button';
      clearBtn.textContent = I18n.t('awaiting.cta');
      clearBtn.addEventListener('click', () => {
        const st = State.get();
        if (st?.board) {
          /* Nur den eigenen Schlüssel — die Einträge anderer Personen bleiben
             (ADR-0130 §7). Der Diff macht daraus eine board.awaiting-Op. */
          const eigene = eigeneAwaitingSchluessel(st.board, eigenePerson(), { server: imServerModus() });
          if (eigene.length) {
            const rest = { ...st.board.awaitingUser };
            for (const k of eigene) delete rest[k];
            st.board.awaitingUser = awaitingOderNull(rest);
          }
          EventBus.emit('state:changed', st); /* State mitgeben — sonst autoPersist undefined */
        }
      });

      banner.appendChild(text);
      banner.appendChild(clearBtn);

      /* Banner zwischen Header und board-columns einfügen */
      const header = document.getElementById('board-header');
      if (header?.parentNode) {
        header.parentNode.insertBefore(banner, header.nextSibling);
      } else {
        document.body.insertBefore(banner, document.body.firstChild);
      }
    }

    /* Live-Update: Zeitanzeige jede Minute aktualisieren */
    const bannerEl = document.getElementById('awaiting-user-banner');
    if (bannerEl) {
      _awaitingTimer = setInterval(() => {
        const current = document.getElementById('awaiting-user-banner');
        if (current) updateBannerTime(current);
        else { clearInterval(_awaitingTimer); _awaitingTimer = null; }
      }, 60_000);
    }
  }

  /* ── Inbox-Kompaktierungs-Banner (ADR-0071 Achse 2D — Karte #230) ───────
     Rein informative Lese-UI: ab COMPACT_OP_THRESHOLD valider Inbox-Ops
     (Sync.getInboxOpCount(), bei jedem projectorReconcile aktualisiert)
     erscheint ein Banner. Der Button hängt NUR einen append-only
     `maintenance.compact`-Marker an (Sync.requestCompaction) — der Browser
     fasst `aboard-sync.base.json` NIE an; die eigentliche Kompaktierung/
     Rotation macht ausschließlich der headless-Projektor. Schrumpft die
     Inbox unter die Schwelle (Projektor hat kompaktiert), verschwindet das
     Banner beim nächsten Render von selbst. Im Server-/Solo-Modus bleibt
     getInboxOpCount() 0 → kein Banner. */
  function renderCompactionBanner() {
    if (!IS_PROJECTOR_BUILD) {
      /* ADR-0072: slim Build hat keine Projektor-Schicht → kein Banner.
         Macht COMPACT_OP_THRESHOLD tot → inbox.js wird tree-geshaked. */
      document.getElementById('compaction-banner')?.remove();
      return;
    }
    const existing = document.getElementById('compaction-banner');
    /* Defensiv: ein rein informatives Banner darf den Render-Pfad NIE
       killen, falls Sync unerwartet geformt ist (z. B. Server-Modus-Stub). */
    const n = typeof Sync.getInboxOpCount === 'function' ? Sync.getInboxOpCount() : 0;

    if (!Number.isFinite(n) || n < COMPACT_OP_THRESHOLD) {
      existing?.remove();
      return;
    }

    /* Karte #337: Bloat-Info fuer das optionale Warning-Element ermitteln. */
    const bloatInfo = typeof Sync.getInboxBloatInfo === 'function'
      ? Sync.getInboxBloatInfo() : null;
    const showBloat = !!(bloatInfo && bloatInfo.bloat);
    const bloatDays = showBloat
      ? Math.round(Math.max(bloatInfo.daysSinceBase, bloatInfo.oldestOpDays))
      : 0;

    if (existing) {
      /* Banner steht schon — Op-Zahl aktualisieren + Bloat-Warnung sync. */
      const t = existing.querySelector('.compaction-banner-text');
      if (t) t.textContent = I18n.t('compact.banner', n);
      /* Bloat-Warning-Element: erzeugen falls neu, entfernen falls nicht mehr */
      let bloatEl = existing.querySelector('.compaction-bloat-text');
      if (showBloat && !bloatEl) {
        bloatEl = el('span', 'awaiting-banner-text compaction-bloat-text');
        bloatEl.style.cssText = 'display:block;margin-top:.25rem;font-weight:600;color:#c0392b;';
        /* Vor dem Button einfuegen */
        const btn = existing.querySelector('.awaiting-banner-btn');
        existing.insertBefore(bloatEl, btn || null);
      }
      if (bloatEl) {
        if (showBloat) {
          bloatEl.textContent = I18n.t('compact.bloat', bloatDays);
        } else {
          bloatEl.remove();
        }
      }
      return;
    }

    const banner = el('div', 'awaiting-banner');
    banner.id = 'compaction-banner';
    ansagen(I18n.t('compact.banner', n)); /* F-2876, s. o. */

    const text = el('span', 'awaiting-banner-text compaction-banner-text');
    text.textContent = I18n.t('compact.banner', n);

    const btn = el('button', 'awaiting-banner-btn');
    btn.type = 'button';
    btn.textContent = I18n.t('compact.cta');
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      const ok = typeof Sync.requestCompaction === 'function'
        && await Sync.requestCompaction(Config.get('userName') || '');
      if (ok) {
        btn.textContent = I18n.t('compact.queued');
      } else {
        /* nicht migriert / inaktiv (Banner sollte dann gar nicht stehen) —
           Button zurücksetzen, damit der User es erneut versuchen kann. */
        btn.disabled = false;
      }
    });

    banner.appendChild(text);
    /* Karte #337: Bloat-Warnung beim erstmaligen Banner-Aufbau einhaengen. */
    if (showBloat) {
      const bloatEl = el('span', 'awaiting-banner-text compaction-bloat-text');
      bloatEl.style.cssText = 'display:block;margin-top:.25rem;font-weight:600;color:#c0392b;';
      bloatEl.textContent = I18n.t('compact.bloat', bloatDays);
      banner.appendChild(bloatEl);
    }
    banner.appendChild(btn);

    const header = document.getElementById('board-header');
    if (header?.parentNode) {
      header.parentNode.insertBefore(banner, header.nextSibling);
    } else {
      document.body.insertBefore(banner, document.body.firstChild);
    }
  }

  /* ── Aktivitäts-Panel (ADR-0084 Variante A — Karte #289) ────────────────
     Rein lesendes Drawer-Panel, das den ADR-0067-Op-/ADR-0037-Activity-Strom
     sichtbar macht. Quellen: board-weiter Activity-Tail, Op-Inbox-Count,
     awaitingUser (kompakt), Handoff-Notiz aus der 📋-Start-Karte des aktiven
     Sprints. Keine eigene Polling-Schicht — hängt am bestehenden
     onExternalUpdate-Render-Pfad (renderBoard, neben den anderen Bannern).
     Hinter IS_PROJECTOR_BUILD-Guard, damit es im slim Build per DCE
     herausfällt (ADR-0088). CSS via JS-Inject (Muster #286/F-320), aus
     dem gleichen DCE-Grund. */

  /* Karte #290 + #329: Chat-Tail-Footer + Activity-Tail-Tab. Modul-State. */
  let _chatTailEntries        = [];
  let _chatTailDrawerOpen     = false;
  let _chatTailStylesInjected = false;
  let _chatTailListenerBound  = false;
  /* Aktiver Footer-Tab: 'chat' (nur claude-code) oder 'activity' (alle Modi,
     IS_PROJECTOR_BUILD). Default 'activity', weil der Tab auch ohne Chat-Tail
     sichtbar ist. */
  let _footerActiveTab        = 'activity';

  /* Sammelt alle Activity-Einträge board-weit, sortiert nach ts absteigend,
     liefert die jüngsten `limit` Stück mit Karten-Referenz. */
  function _collectActivityTail(board, limit = 20) {
    const entries = [];
    for (const s of (board?.sprints || [])) {
      for (const c of (s.cards || [])) {
        for (const a of (c.activity || [])) {
          entries.push({
            ts: a.timestamp || '',
            actor: a.author || '',
            action: a.action || '',
            field: a.field || '',
            from: a.from || '',
            to: a.to || '',
            cardDisplayId: c.displayId,
            cardTitle: c.title || '',
          });
        }
      }
    }
    entries.sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0));
    return entries.slice(0, limit);
  }

  /* Format-Helfer: "owner-changed: A → B" → kurzer Lesetext */
  function _formatActivityEntry(entry) {
    const refSpan = document.createElement('span');
    refSpan.className = 'card-ref';
    refSpan.textContent = ` #${entry.cardDisplayId}`;

    const line = document.createElement('span');
    const actor = document.createElement('span');
    actor.className = 'actor';
    actor.textContent = entry.actor || I18n.t('activity.actor-unknown');
    line.appendChild(actor);
    line.appendChild(document.createTextNode(' '));

    let verb;
    if (entry.action === 'owner-changed') {
      verb = I18n.t('activity.action.owner', entry.from || '∅', entry.to || '∅');
    } else if (entry.action === 'column-moved') {
      verb = I18n.t('activity.action.column', entry.to || '∅');
    } else if (entry.action === 'field-updated') {
      verb = I18n.t('activity.action.field', entry.field || '', entry.to || '∅');
    } else if (entry.action === 'sprint-activate') {
      verb = I18n.t('activity.action.sprint-activate');
    } else {
      verb = entry.action || '';
    }
    line.appendChild(document.createTextNode(verb));
    line.appendChild(refSpan);

    const when = document.createElement('span');
    when.className = 'when';
    when.textContent = entry.ts ? relativeTime(entry.ts) : '';
    line.appendChild(when);
    return line;
  }

  /* ── Chat-Tail-Footer mit Activity-Tail-Tab (Karte #290 + #329) ───────
     Footer am unteren Browserrand mit zwei Tabs:
       - "💬 Chat" (claude-code-only, chatTailEnabled): Hook-getriebene Live-Linien
       - "📋 Verlauf" (IS_PROJECTOR_BUILD, alle Modi, activityTailEnabled): board.activity

     Sichtbarkeits-Logik:
       - Im slim-Build (!IS_PROJECTOR_BUILD): Footer komplett entfernen (DCE).
       - Ansonsten: Footer sichtbar wenn chatTailEnabled (claude-code) ODER
         activityTailEnabled (jeder Modus). Tab-Auswahl merkt sich _footerActiveTab.

     Slim-DCE: !IS_PROJECTOR_BUILD → komplette Entfernung inkl. Listener-Detach. */

  function _ensureChatTailStyles() {
    if (_chatTailStylesInjected) return;
    _chatTailStylesInjected = true;
    const s = document.createElement('style');
    s.setAttribute('data-aboard', 'chat-tail');
    s.textContent = '.chat-tail-footer{position:fixed;left:0;right:0;bottom:0;z-index:1500;background:var(--surface);border-top:1px solid var(--border);box-shadow:0 -2px 8px rgba(0,0,0,.08);font-size:12px;color:var(--text);max-height:36vh;display:flex;flex-direction:column}.chat-tail-footer__head{display:flex;align-items:center;gap:6px;padding:4px 10px;cursor:pointer;user-select:none;border-bottom:1px solid transparent}.chat-tail-footer__head:hover{background:var(--btn-bg-hover)}.chat-tail-footer__head .chev{display:inline-block;transition:transform 120ms;color:var(--text-muted)}.chat-tail-footer--open .chat-tail-footer__head .chev{transform:rotate(90deg)}.chat-tail-footer--open .chat-tail-footer__head{border-bottom-color:var(--border)}.chat-tail-footer__tabs{display:flex;gap:4px;align-items:center}.chat-tail-footer__tab{background:none;border:none;padding:2px 8px;border-radius:var(--radius);cursor:pointer;font-size:11px;color:var(--text-muted);font-weight:600}.chat-tail-footer__tab:hover{background:var(--btn-bg-hover);color:var(--text)}.chat-tail-footer__tab--active{background:var(--btn-bg-hover);color:var(--text)}.chat-tail-footer__title{font-weight:600;color:var(--text-muted);font-size:11px;text-transform:uppercase;letter-spacing:.04em}.chat-tail-footer__latest{flex:1;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-size:12px}.chat-tail-footer--open .chat-tail-footer__latest{display:none}.chat-tail-footer__body{display:none;overflow-y:auto;padding:4px 10px 6px;max-height:32vh}.chat-tail-footer--open .chat-tail-footer__body{display:block}.chat-tail-footer__entry{display:flex;gap:6px;align-items:baseline;padding:2px 0;border-bottom:1px dotted var(--border)}.chat-tail-footer__entry:last-child{border-bottom:none}.chat-tail-footer__kind{flex:0 0 auto;font-size:13px;line-height:1}.chat-tail-footer__when{flex:0 0 auto;color:var(--text-muted);font-size:11px;font-variant-numeric:tabular-nums;min-width:3em}.chat-tail-footer__snip{flex:1;white-space:pre-wrap;word-break:break-word}.chat-tail-footer__empty{color:var(--text-muted);font-style:italic;font-size:12px;padding:4px 0}.chat-tail-footer__act-entry{padding:2px 0;border-bottom:1px dotted var(--border);color:var(--text)}.chat-tail-footer__act-entry:last-child{border-bottom:none}.chat-tail-footer__act-entry .actor{font-weight:600}.chat-tail-footer__act-entry .when{color:var(--text-muted);font-size:11px;margin-left:6px}.chat-tail-footer__act-entry .card-ref{color:var(--text-muted);font-size:11px}';
    document.head.appendChild(s);
  }

  function _formatChatTailLatest(entry) {
    if (!entry) return I18n.t('chat-tail.empty');
    const sym = I18n.t('chat-tail.kind.' + entry.kind) || '·';
    return sym + ' ' + (entry.snippet || '');
  }

  /* Rendert den Body des aktiven Tabs (chat oder activity) in den Footer. */
  function _renderChatTailBody(footer, board) {
    const body = footer.querySelector('.chat-tail-footer__body');
    const latest = footer.querySelector('.chat-tail-footer__latest');
    if (!body) return;
    body.textContent = '';

    if (_footerActiveTab === 'chat') {
      /* Chat-Tab: Hook-getriebene Einträge aus _chatTailEntries */
      if (latest) latest.textContent = _formatChatTailLatest(_chatTailEntries[_chatTailEntries.length - 1]);
      if (_chatTailEntries.length === 0) {
        const empty = el('div', 'chat-tail-footer__empty');
        empty.textContent = I18n.t('chat-tail.empty');
        body.appendChild(empty);
        return;
      }
      _chatTailEntries.forEach(entry => {
        const row = el('div', 'chat-tail-footer__entry');
        const k = el('span', 'chat-tail-footer__kind');
        k.textContent = I18n.t('chat-tail.kind.' + entry.kind) || '·';
        const w = el('span', 'chat-tail-footer__when');
        w.textContent = entry.ts ? relativeTime(entry.ts) : '';
        const s = el('span', 'chat-tail-footer__snip');
        s.textContent = entry.snippet || '';
        row.appendChild(k);
        row.appendChild(w);
        row.appendChild(s);
        body.appendChild(row);
      });
      /* Autoscroll auf den jüngsten Eintrag (unten) — analog zu Chat-UIs. */
      body.scrollTop = body.scrollHeight;
    } else {
      /* Activity-Tab: board.activity-Tail (board wird bei jedem renderChatTailFooter-
         Aufruf mitgegeben; Read-Only, kein Live-Listener nötig). */
      if (latest) latest.textContent = I18n.t('activity.tail');
      const limit = Number(Config.get('activityTailLines')) || 20;
      const tail = _collectActivityTail(board, limit);
      if (tail.length === 0) {
        const empty = el('div', 'chat-tail-footer__empty');
        empty.textContent = I18n.t('activity.empty');
        body.appendChild(empty);
        return;
      }
      tail.forEach(entry => {
        const row = el('div', 'chat-tail-footer__act-entry');
        row.appendChild(_formatActivityEntry(entry));
        body.appendChild(row);
      });
    }
  }

  function renderChatTailFooter(board) {
    if (!IS_PROJECTOR_BUILD) {
      /* Slim-Build: kompletten Footer entfernen + Listener vergessen. esbuild
         eliminiert den ganzen Funktionsrumpf, der frühe Return-Pfad reicht. */
      document.getElementById('chat-tail-footer')?.remove();
      return;
    }

    const mode              = Config.get('boardMode');
    const chatEnabled       = !!Config.get('chatTailEnabled') && mode === 'claude-code';
    const activityEnabled   = !!Config.get('activityTailEnabled');

    /* F-330 (Sprint-39-Kritiker, behoben Sprint 61/#445): Der Chat-Tail-
       Listener ist Chat-spezifisch — sobald chatEnabled auf false wechselt
       (Mode-Wechsel weg von claude-code, oder chatTailEnabled ausgeschaltet),
       muss er gelöst werden. Vorher blieb er nach dem einmaligen Binden
       (_chatTailListenerBound) für die gesamte Modul-Lebensdauer registriert
       — null-safe (der Callback griff nur auf einen ggf. fehlenden
       #chat-tail-footer zu, kein Crash), aber ein Listener ohne Zweck, UND
       ein späteres Wieder-Einschalten band nie erneut (Once-Only-Falle).
       Muss VOR den beiden Return-Pfaden unten stehen, sonst würde das
       Lösen bei "Footer komplett weg" (nächster Block) übersprungen. */
    if (!chatEnabled && _chatTailListenerBound && typeof Sync.setChatTailListener === 'function') {
      Sync.setChatTailListener(null);
      _chatTailListenerBound = false;
    }

    /* Footer nur sichtbar wenn mindestens ein Tab aktiv ist. */
    if (!chatEnabled && !activityEnabled) {
      document.getElementById('chat-tail-footer')?.remove();
      return;
    }

    /* Falls der aktuell aktive Tab nicht mehr verfügbar ist → auf den anderen
       wechseln, der noch verfügbar ist. */
    if (_footerActiveTab === 'chat' && !chatEnabled) _footerActiveTab = 'activity';
    if (_footerActiveTab === 'activity' && !activityEnabled) _footerActiveTab = 'chat';

    _ensureChatTailStyles();

    /* Chat-Tail-Listener einmalig binden — nur wenn Chat-Tab aktiv (chatEnabled).
       Der Listener ist Chat-spezifisch und wird nicht für den Activity-Tab benötigt.
       Wandert mit dem Modul-Singleton mit. Sync ist leerer DCE-Stub im slim. */
    if (chatEnabled && !_chatTailListenerBound && typeof Sync.setChatTailListener === 'function') {
      _chatTailListenerBound = true;
      Sync.setChatTailListener(entries => {
        _chatTailEntries = entries || [];
        const f = document.getElementById('chat-tail-footer');
        if (f) _renderChatTailBody(f, board);
      });
      if (typeof Sync.getChatTail === 'function') {
        _chatTailEntries = Sync.getChatTail() || [];
      }
    }

    let footer = document.getElementById('chat-tail-footer');
    if (!footer) {
      /* Neuer Footer: Drawer-State auf zu zurücksetzen — verhindert, dass
         ein alter Modul-Singleton-Wert einen frisch erstellten Footer
         fälschlicherweise aufklappt (Relevanz: Tests + Re-Mount nach Mode-Wechsel). */
      _chatTailDrawerOpen = false;
      footer = el('div', 'chat-tail-footer');
      footer.id = 'chat-tail-footer';
      footer.setAttribute('role', 'region');
      footer.setAttribute('aria-label', I18n.t('footer.title'));

      /* F-337 (Sprint-39-Kritiker): Head als role="button" + tabindex
         + aria-expanded, damit Screenreader/Keyboard-Nutzer den Drawer
         öffnen können. Enter/Space lösen den Toggle aus. */
      const head = el('div', 'chat-tail-footer__head');
      head.setAttribute('role', 'button');
      head.setAttribute('tabindex', '0');
      head.setAttribute('aria-expanded', 'false');
      const chev = el('span', 'chev');
      chev.textContent = '▶';
      chev.setAttribute('aria-hidden', 'true');

      /* Tab-Leiste in der Kopfzeile — nur sichtbar wenn beide Tabs verfügbar */
      const tabs = el('div', 'chat-tail-footer__tabs');
      tabs.setAttribute('data-board', 'footer-tabs');
      head.appendChild(chev);
      head.appendChild(tabs);

      const latest = el('span', 'chat-tail-footer__latest');
      head.appendChild(latest);
      const toggle = () => {
        _chatTailDrawerOpen = !_chatTailDrawerOpen;
        footer.classList.toggle('chat-tail-footer--open', _chatTailDrawerOpen);
        head.setAttribute('aria-expanded', String(_chatTailDrawerOpen));
      };
      head.addEventListener('click', e => {
        /* Tab-Klick soll den Drawer NICHT schließen — stoppen */
        if (e.target.classList.contains('chat-tail-footer__tab')) return;
        toggle();
      });
      head.addEventListener('keydown', e => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); }
      });
      footer.appendChild(head);

      const body = el('div', 'chat-tail-footer__body');
      footer.appendChild(body);
      document.body.appendChild(footer);
    }

    /* Tab-Leiste aktualisieren */
    const tabs = footer.querySelector('[data-board="footer-tabs"]');
    if (tabs) {
      tabs.textContent = '';
      if (chatEnabled) {
        const btn = el('button', 'chat-tail-footer__tab');
        btn.type = 'button';
        btn.textContent = I18n.t('chat-tail.tab');
        btn.dataset.tab = 'chat';
        if (_footerActiveTab === 'chat') btn.classList.add('chat-tail-footer__tab--active');
        btn.addEventListener('click', e => {
          e.stopPropagation();
          _footerActiveTab = 'chat';
          footer.querySelectorAll('.chat-tail-footer__tab').forEach(
            b => b.classList.toggle('chat-tail-footer__tab--active', b.dataset.tab === 'chat'),
          );
          _renderChatTailBody(footer, board);
        });
        tabs.appendChild(btn);
      }
      if (activityEnabled) {
        const btn = el('button', 'chat-tail-footer__tab');
        btn.type = 'button';
        btn.textContent = I18n.t('activity.tab');
        btn.dataset.tab = 'activity';
        if (_footerActiveTab === 'activity') btn.classList.add('chat-tail-footer__tab--active');
        btn.addEventListener('click', e => {
          e.stopPropagation();
          _footerActiveTab = 'activity';
          footer.querySelectorAll('.chat-tail-footer__tab').forEach(
            b => b.classList.toggle('chat-tail-footer__tab--active', b.dataset.tab === 'activity'),
          );
          _renderChatTailBody(footer, board);
        });
        tabs.appendChild(btn);
      }
    }

    footer.classList.toggle('chat-tail-footer--open', _chatTailDrawerOpen);
    _renderChatTailBody(footer, board);
  }

  /* ── Swimlane-Verschieben per Tastatur (F-388, Karte #344) ─────────── */

  /* Nach dem Verschieben rendert das Board neu und der alte DOM-Knoten samt
     Fokus ist weg. Ohne Wiederherstellung wäre nach EINEM Schritt Schluss —
     der Nutzer müsste sich zurück-tabben, um erneut zu drücken. Das ist der
     Unterschied zwischen „Taste funktioniert" und „bedienbar". */
  let _fokusSwimlaneHandle = null;

  /* Eine gemeinsame, visuell versteckte Region für die Reorder-Meldungen.
     `polite`, nicht `assertive`: die Meldung soll den Screenreader-Nutzer
     nicht mitten im Satz unterbrechen (Hinweis im agentContext). */
  function swimlaneLiveRegion() {
    let liveEl = document.getElementById('swimlane-move-live');
    if (!liveEl) {
      liveEl = el('div', 'visually-hidden');
      liveEl.id = 'swimlane-move-live';
      liveEl.setAttribute('aria-live', 'polite');
      liveEl.setAttribute('aria-atomic', 'true');
      document.body.appendChild(liveEl);
    }
    return liveEl;
  }

  /* Der Hinweistext, auf den jedes Handle per aria-describedby zeigt — einmal
     im Dokument, nicht pro Swimlane. */
  function swimlaneMoveHint() {
    let hint = document.getElementById('swimlane-move-hint');
    if (!hint) {
      hint = el('span', 'visually-hidden');
      hint.id = 'swimlane-move-hint';
      document.body.appendChild(hint);
    }
    hint.textContent = I18n.t('sprint.move-hint');
    return hint;
  }

  function melde(text) { swimlaneLiveRegion().textContent = text; }

  /**
   * Verschiebt eine Swimlane um `richtung` Positionen (-1 hoch, +1 runter).
   *
   * Die Liste der verschiebbaren Swimlanes wird aus dem DOM gelesen, mit
   * derselben Regel wie `dropSwimlane` in src/dragdrop.js: alle `.swimlane`
   * in DOM-Reihenfolge ohne `__backlog__`. Bewusst dieselbe Quelle — eine
   * eigene Rechnung über `state.board.sprints` könnte abweichen, und zwei
   * Wege mit unterschiedlicher Index-Semantik auf demselben Event wären eine
   * Fehlerquelle, die niemand vermutet.
   */
  function verschiebeSwimlanePerTaste(sprint, richtung) {
    const lanes = [...document.querySelectorAll('.swimlane')]
      .map(l => l.dataset.sprintId)
      .filter(id => id && id !== '__backlog__');
    const von = lanes.indexOf(sprint.id);
    if (von === -1) return;
    const nach = von + richtung;

    /* An den Rändern nichts tun, aber sagen warum — ein stummer Tastendruck
       ist für Screenreader-Nutzer nicht von einem kaputten Knopf zu
       unterscheiden. */
    if (nach < 0)             { melde(I18n.t('sprint.move-first', sprint.title)); return; }
    if (nach >= lanes.length) { melde(I18n.t('sprint.move-last',  sprint.title)); return; }

    /* FLIP-Snapshot wie beim Maus-Drag, damit die Umsortierung sanft läuft
       statt zu springen. Bei `prefers-reduced-motion` bleibt es aus. */
    DragDrop.snapshotSwimlanesForKeyboardMove?.();

    _fokusSwimlaneHandle = sprint.id;
    /* Positionen in der Meldung sind 1-basiert — die Zählung ist für Menschen,
       nicht für den Index. */
    melde(I18n.t('sprint.moved', sprint.title, von + 1, nach + 1, lanes.length));
    /* ADR-0117: `lanes` sind die SICHTBAREN Swimlanes. `nach` ist damit eine
       Position in der sichtbaren Reihenfolge, nicht in board.sprints — sobald
       archivierte ausgeblendet sind, laufen die beiden auseinander. Deshalb
       zusaetzlich die id der Swimlane mitgeben, an deren Stelle es gehen soll;
       der Handler rechnet daraus die Board-Position aus. */
    EventBus.emit('swimlane:moved', { sprintId: sprint.id, targetIndex: nach, ankerSprintId: lanes[nach] });
  }

  /* Nach dem Re-Render den Fokus zurück auf das Handle der verschobenen
     Swimlane. Aus renderBoard heraus aufgerufen. */
  function restoreSwimlaneFokus() {
    if (!_fokusSwimlaneHandle) return;
    const id = _fokusSwimlaneHandle;
    _fokusSwimlaneHandle = null;
    const lane = document.querySelector(`.swimlane[data-sprint-id="${CSS.escape(id)}"]`);
    const handle = lane?.querySelector('.sprint-drag-handle');
    if (handle) { try { handle.focus({ preventScroll: true }); } catch { handle.focus(); } }
  }

  /* Karte #114: Board-Hintergrundbild */
  let _bgObjUrl = null;
  /* F-164 (Karte #386): Generations-Marke, gleiche Rolle wie
     `_renderGeneration` beim Karten-Cover. `boardAttachmentObjectUrl` löst
     asynchron auf; ohne Marke entscheidet die REIHENFOLGE DER ANTWORTEN,
     welcher Hintergrund am Ende steht, nicht die Reihenfolge der Aufrufe.

     Der Schaden war doppelt. Bei zwei schnell aufeinander folgenden Aufrufen
     (Board-Wechsel, Re-Render) und einer Antwort in umgekehrter Reihenfolge
     galt der späte, veraltete Callback als der gültige: Er sah `_bgObjUrl`
     mit der FRISCHEN URL des anderen Aufrufs belegt, hielt sie für die alte
     — und gab damit genau das Bild frei, das gerade angezeigt wurde (das
     `blob:` bricht sichtbar weg). Anschliessend setzte er sein eigenes,
     veraltetes Bild.

     Und im Frühausstieg oben (Hintergrund entfernt) blieb ein bereits
     laufender Callback unbeeindruckt: Er setzte das Bild danach wieder,
     obwohl es gerade abgeschaltet wurde. Deshalb zählt die Marke auch
     dort hoch — nicht nur im Bild-Zweig. */
  let _bgGeneration = 0;
  /* Welches Attachment die aktuell angezeigte URL erzeugt hat (Kritiker zu
     Karte #439). Ohne diesen Vergleich decodiert JEDER Re-Render das Bild neu:
     `applyBoardBackground` hängt an `state:changed`, und das feuert bei jeder
     Kartenbewegung, jedem Kommentar und — in den Polling-Profilen — bei jedem
     erfolgreichen Poll. Vorher war das eine Template-Konkatenation, seit der
     Umstellung auf Blob-URLs sind es `atob` plus eine Byte-Schleife über das
     ganze Bild. Der Fix gegen den unsichtbaren Hintergrund hätte sich sonst
     als wiederkehrende Last durch die Hintertür wieder eingekauft. */
  let _bgAttId = null;

  function applyBoardBackground(board) {
    const app = document.getElementById('app') || document.body;
    const id  = board?.background?.attachmentId;
    const att = id ? (board.attachments || []).find(a => a.id === id) : null;
    /* Jeder Aufruf entwertet die noch laufenden — auch der Abschalt-Fall
       und der Kurzschluss unten. */
    const gen = ++_bgGeneration;
    /* #727: Darstellung VOR den Frühausstiegen (Bild bleibt gleich, Modus wechselt).
       Unbekannt/fehlend = getönt (keine Klasse). */
    const dar = att ? board.background.darstellung : null;
    app.classList.toggle('board--bg-voll', dar === 'voll');
    app.classList.toggle('board--bg-dunkel', dar === 'abgedunkelt');
    /* Unverändertes Bild, URL steht schon: nichts neu zu decodieren. Bewusst
       gegen `_bgObjUrl` UND `_bgAttId` geprüft — ein gesetztes Id ohne URL
       wäre ein halber Zustand, aus dem es sonst keinen Rückweg gäbe.

       DIESER AUSSTIEG STEHT NACH DER GENERATIONSMARKE, NICHT DAVOR. Die erste
       Fassung kehrte davor zurück — und hat damit den F-164-Schutz ausgehebelt:
       Bild B steht, jemand wählt A (Anfrage läuft), wählt vor der Antwort
       wieder B. Der B-Render fand alles unverändert und stieg aus, ohne die
       Marke zu erhöhen; als A danach antwortete, galt es noch als aktuell und
       überschrieb das angezeigte B. Gefunden hat es der bestehende
       F-164-Wettlauf-Test — er ist genau dafür da. */
    if (att && _bgAttId === att.id && _bgObjUrl) return;
    /* kein SVG (F-128; F-1107: istSvgMime() statt exaktem String-Vergleich) */
    if (!att?.mime?.startsWith('image/') || istSvgMime(att.mime)) {
      app.style.removeProperty('--board-bg-image');
      app.classList.remove('board--has-bg');
      if (_bgObjUrl) { URL.revokeObjectURL(_bgObjUrl); _bgObjUrl = null; }
      _bgAttId = null;
      return;
    }
    /* IMMER eine Blob-URL, nie eine data-URL (Karte #439): Der Wert landet
       unten in einer CSS Custom Property, und dort verwirft der Browser einen
       mehrere Megabyte langen `url(data:…)` stillschweigend — `setProperty`
       meldet nichts, die Variable bleibt leer, `background-image: var(…)`
       fällt auf `none` zurück. Die Vorschau im Einstellungsdialog setzt
       `backgroundImage` direkt und war deshalb nie betroffen; genau diese
       Asymmetrie hat den Fehler auf dem Server so schwer auffindbar gemacht.
       Die Freigabe-Logik unten (_bgObjUrl, _bgGeneration, F-164) greift
       unverändert — sie war ohnehin auf blob: ausgelegt. */
    anhangBackend().boardAttachmentObjectUrl(att).then(url => {
      if (!url) return;
      if (gen !== _bgGeneration) {
        /* Überholt. Die eigene URL freigeben — sie wurde nie angezeigt und
           würde sonst nie mehr angefasst (das ist der Leak aus F-164).
           `_bgObjUrl` bleibt unberührt: die gehört dem Gewinner. */
        if (url.startsWith('blob:')) { try { URL.revokeObjectURL(url); } catch {} }
        return;
      }
      if (_bgObjUrl && _bgObjUrl !== url) { URL.revokeObjectURL(_bgObjUrl); _bgObjUrl = null; }
      if (url.startsWith('blob:')) _bgObjUrl = url;
      _bgAttId = att.id;
      app.style.setProperty('--board-bg-image', `url(${url})`);
      app.classList.add('board--has-bg');
    }).catch(() => {});
  }

  /* ── Bulk-Action-Bar (Karte #63) ─────────────────────────────── */

  let _bulkActionBar = null;

  /** Ist das Bulk-Menü offen? Die Aufbau-Listener hängen vor dem Umschalt-
      Listener am Button und laufen beim Schließ-Klick bei noch offenem Menü —
      dort darf nichts aufgebaut/fokussiert werden (VO-1). */
  function istOffen(menu) {
    return menu.classList.contains('bulk-dropdown-menu--open');
  }

  /** Gruppenkopf/Platzhalter im Bulk-Menü: im role="menu" nur mit role="presentation"
      zulässig (wie board-menu.js, Sitzungs-QA Sprint 85). */
  function bulkKopf(menu, text, css = 'font-size:11px;color:var(--text-muted);padding:4px 14px 2px;pointer-events:none;') {
    const k = el('div', 'bulk-dropdown-item');
    k.setAttribute('role', 'presentation');
    k.textContent = text;
    k.style.cssText = css;
    return menu.appendChild(k);
  }

  /** Menüeintrag-Button; im Rollenmenü mit role="menuitem" (F-1790). */
  function bulkItem(menu) {
    const b = el('button', 'bulk-dropdown-item');
    b.type = 'button';
    if (menu.getAttribute('role') === 'menu') b.setAttribute('role', 'menuitem');
    return b;
  }

  /** Hilfsfunktion: ein Dropdown-Panel innerhalb der Action-Bar */
  function buildBulkDropdown(btnText, buildItems, mitFeld = false) {
    const wrap = el('div', 'bulk-dropdown');
    const btn = el('button', 'btn btn-sm');
    btn.type = 'button';
    btn.textContent = btnText;

    const menu = el('div', 'bulk-dropdown-menu');
    /* F-1790: Rolle am Menü. Mit Eingabefeld (Label hinzufügen) ist role="menu"
       ungültig (Kinder müssten menuitem sein) → Gruppe im Disclosure-Muster: nur
       aria-expanded, kein aria-haspopup (dessen Wert müsste zur Popup-Rolle passen). */
    menu.setAttribute('role', mitFeld ? 'group' : 'menu');
    menu.setAttribute('aria-label', btnText);
    if (!mitFeld) btn.setAttribute('aria-haspopup', 'menu');
    btn.setAttribute('aria-expanded', 'false');
    /* Geschlossen = weder sichtbar noch fokussierbar/bedienbar (VO-1):
       `inert` hier, `visibility:hidden` in der CSS. */
    menu.setAttribute('inert', '');
    /* Erst einhängen, dann die Einträge bauen: die Aufrufer holen sich den
       Auslöser-Button per `menu.previousElementSibling` — am losen Menü ist
       das null, die Bulk-Menüs blieben sonst leer (gefunden bei KO-5). */
    wrap.appendChild(btn);
    wrap.appendChild(menu);
    buildItems(menu, () => closeDropdown());

    let open = false;

    function openDropdown() {
      open = true;
      btn.setAttribute('aria-expanded', 'true');
      menu.removeAttribute('inert');
      menu.classList.add('bulk-dropdown-menu--open');
      /* Aussenklick schließt — der Listener lebt, solange das Menü offen ist
         (nicht `once`: ein Klick ins Menü darf ihn nicht verbrauchen, F-1729). */
      document.addEventListener('pointerdown', onOutside);
      document.addEventListener('keydown', onEsc, true);
    }
    function closeDropdown(fokusZurueck = true) {
      open = false;
      btn.setAttribute('aria-expanded', 'false');
      document.removeEventListener('pointerdown', onOutside);
      document.removeEventListener('keydown', onEsc, true);
      /* Fokus im Menü würde sonst im unsichtbaren Feld stehen bleiben (VO-1);
         beim Tab-Verlassen (F-1769) wandert der Fokus schon selbst weiter. */
      if (fokusZurueck && menu.contains(document.activeElement)) btn.focus();
      menu.setAttribute('inert', '');
      menu.classList.remove('bulk-dropdown-menu--open');
    }
    function onOutside(e) {
      if (!wrap.contains(e.target)) {
        closeDropdown();
      }
    }

    btn.addEventListener('click', () => {
      if (open) closeDropdown(); else openDropdown();
    });
    /* F-1769/F-1785: Escape schließt und gibt dem Auslöser den Fokus — im Capture
       auf Dokumentebene, solange offen: der Fokus steht oft auf body (Klick auf eine
       Überschrift, Safari/Firefox auf macOS), dann erreichte ein Handler am `wrap`
       Escape nie und der globale löschte die Auswahl (Klasse 149). */
    function onEsc(e) {
      if (e.key === 'Escape' && !dialogOffen() && !fremderFokus(wrap)) { e.preventDefault(); e.stopPropagation(); closeDropdown(false); btn.focus(); }
    }
    /* Auswahl leer (Escape bei geschlossenem Menü, Boardwechsel): die Leiste
       verschwindet, ein offenes Menü mit Einträgen des alten Boards mit ihr (F-1786). */
    EventBus.on('bulk:changed', ({ count: n }) => { if (!n && open) closeDropdown(false); });
    /* F-1773: Pfeiltasten wie im Board-Menü (Umlauf); Pfeil runter am Auslöser
       öffnet und springt auf den ersten Punkt. Das Label-Feld zählt als Punkt —
       Pfeile bewegen in einem einzeiligen Feld nichts, nichts geht verloren. */
    wrap.addEventListener('keydown', e => {
      const d = e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : 0;
      if (!d || (!open && !(d > 0 && e.target === btn))) return;
      e.preventDefault();
      if (!open) btn.click();
      const p = [...menu.querySelectorAll('input,button')];
      const i = p.indexOf(document.activeElement);
      p[i < 0 ? (d > 0 ? 0 : p.length - 1) : (i + d + p.length) % p.length]?.focus();
    });
    /* Fokus verlässt das Menü-Umfeld (Tab): schließen, ohne ihn zurückzuziehen. */
    wrap.addEventListener('focusout', e => {
      if (open && e.relatedTarget && !wrap.contains(e.relatedTarget)) closeDropdown(false);
    });

    return wrap;
  }

  /** Baut die Action-Bar und hängt sie an document.body */
  function renderBulkActionBar() {
    /* Vorhandene entfernen */
    _bulkActionBar?.remove();
    _bulkActionBar = null;

    const bar = el('div', 'bulk-action-bar bulk-action-bar--hidden');
    bar.id = 'bulk-action-bar';
    bar.setAttribute('role', 'toolbar');
    bar.setAttribute('aria-label', 'Bulk-Aktionen');

    /* Zähler-Label */
    const countEl = el('span', 'bulk-action-count');
    countEl.id = 'bulk-action-count';
    bar.appendChild(countEl);

    const sep1 = el('div', 'bulk-action-sep');
    bar.appendChild(sep1);

    /* ── Verschieben-Dropdown ─── */
    const moveDropdown = buildBulkDropdown(I18n.t('bulk.move'), (menu, close) => {
      /* Optionen aus aktuellem Board-State dynamisch befüllen */
      menu.addEventListener('mousedown', () => {}); /* Fokus behalten */
      /* Inhalt wird beim Öffnen dynamisch erzeugt — deshalb mouseover/focus-Trick nutzen */
      bulkKopf(menu, '…', 'color:var(--text-muted)');

      /* Beim Öffnen des Menus Inhalt aufbauen */
      menu.addEventListener('click', () => {});
      /* Rebuild trigger: wenn das Parent-Button geklickt wird */
      const parentBtn = menu.previousElementSibling;
      if (parentBtn) {
        parentBtn.addEventListener('click', () => {
          if (istOffen(menu)) return; /* Schließ-Klick: nichts aufbauen (VO-1) */
          clear(menu);
          const state = State.get();
          if (!state?.board) return;

          /* Spalten */
          bulkKopf(menu, '── Spalten');

          state.board.columnDefs.forEach(col => {
            const item = bulkItem(menu);
            item.textContent = col.title;
            item.addEventListener('click', () => {
              close();
              EventBus.emit('bulk:move', { toColId: col.id });
            });
            menu.appendChild(item);
          });

          /* Sprints (Swimlane-Modus) — wenn mehr als 1 Sprint vorhanden */
          /* Opus-Kritik O-1 (Karte #826): gespiegelte Lanes einer externen Kopie
             sind kein Ziel — der Server wiese eine eigene Karte dort ab. */
          const realSprints = state.board.sprints.filter(s => s.id !== '__backlog__' && !(IS_SERVER_BUILD && markeWirkt(s)));
          if (realSprints.length > 0) {
            bulkKopf(menu, '── Sprints');
            realSprints.forEach(sprint => {
              const item = bulkItem(menu);
              item.textContent = sprint.title;
              item.addEventListener('click', () => {
                close();
                EventBus.emit('bulk:move', { toSprintId: sprint.id });
              });
              menu.appendChild(item);
            });
          }
        });
      }
    });
    bar.appendChild(moveDropdown);

    /* ── Owner-Dropdown ─── */
    const ownerDropdown = buildBulkDropdown(I18n.t('bulk.owner'), (menu, close) => {
      const parentBtn = menu.previousElementSibling;
      let lauf = 0;
      if (parentBtn) {
        parentBtn.addEventListener('click', () => {
          if (istOffen(menu)) return; /* Schließ-Klick: nichts aufbauen (VO-1) */
          const state = State.get();
          const mein = ++lauf; /* F-1763: nur die Antwort des jüngsten Öffnens zählt */
          const serverModus = IS_SERVER_BUILD && Profile.has('feature.restStorage');
          const ownerItem = (o) => {
            const item = bulkItem(menu);
            item.textContent = o;
            item.addEventListener('click', () => {
              close();
              EventBus.emit('bulk:set-owner', { owner: o });
            });
            return item;
          };
          /* Baut das ganze Menü neu auf (sortiert). `nachgeladen` = Namen, die
             ladeKandidaten() nachliefert. */
          const baue = (nachgeladen = []) => {
            clear(menu);
            const owners = new Set(['']);
            state?.board?.sprints?.forEach(s => s.cards.forEach(c => { if (c.owner) owners.add(c.owner); }));
            let claude = [];
            /* Standard-Optionen. Karte #698: Im Server-Modus sind es die
               Personen des Boards statt der festen Claude-Liste (bestehende
               Owner stehen oben schon drin und bleiben wählbar). Claude-Modelle
               folgen als eigene Gruppe nach den Personen — nur auf Boards, die
               sie schon benutzen (KO-1). Rest wie bisher. */
            if (IS_SERVER_BUILD && serverModus) {
              kandidatenSofort(state?.board).forEach(o => owners.add(o));
              nachgeladen.forEach(o => owners.add(o));
              claude = claudeGruppe(state?.board);
              /* Bestehende Claude-Owner („Claude Opus 5“) gehören in die Gruppe. */
              [...owners].filter(istClaudeKennung).forEach(o => { if (!claude.includes(o)) claude.push(o); });
              claude.forEach(o => owners.delete(o));
            } else {
              ['', ...CLAUDE_MODELLE].forEach(o => owners.add(o));
            }

            const noOwnerItem = bulkItem(menu);
            noOwnerItem.textContent = I18n.t('bulk.no-owner');
            noOwnerItem.addEventListener('click', () => {
              close();
              EventBus.emit('bulk:set-owner', { owner: '' });
            });
            menu.appendChild(noOwnerItem);
            [...owners].filter(o => o !== '').sort().forEach(o => menu.appendChild(ownerItem(o)));
            /* Gruppenkopf + i18n-Schlüssel nur im Server-Build (VO-9): hinter
               IS_SERVER_BUILD fällt der Zweig im Solo-Template per DCE heraus. */
            if (IS_SERVER_BUILD && claude.length) {
              bulkKopf(menu, '── ' + I18n.t('card.owner.gruppe-claude')).dataset.claudeKopf = '';
              claude.sort().forEach(o => menu.appendChild(ownerItem(o)));
            }
          };
          baue();
          if (serverModus) {
            ladeKandidaten(state?.board).then(namen => {
              /* Nur ins offene Menü (F-1730); ein geschlossenes Menü bleibt unberührt. */
              if (!menu.isConnected || !istOffen(menu)) return;
              /* F-1763: ältere Antwort oder inzwischen anderes Board → verwerfen. */
              if (mein !== lauf || State.get()?.board?.id !== state?.board?.id) return;
              /* F-1787: immer dieselben Einträge, egal wo der Fokus steht — also stets
                 neu aufbauen. Ein fokussierter Eintrag bleibt als derselbe Knoten stehen
                 (F-1730: Fokus/laufender Klick gehen nicht verloren). Entfällt er, geht der
                 Fokus auf den Auslöser — nicht auf den ersten Eintrag, das ist „kein Owner“,
                 und das gemeinte Enter leerte sonst den Owner aller Karten. */
              const f = menu.contains(document.activeElement) ? document.activeElement : null;
              baue(namen);
              if (f) {
                const neu = [...menu.children].find(k => k.textContent === f.textContent && k.tagName === 'BUTTON');
                if (neu) neu.replaceWith(f); else parentBtn.focus();
                if (neu) f.focus();
              }
            }).catch(() => {});
          }
        });
      }
    });
    bar.appendChild(ownerDropdown);

    /* ── Label-hinzufügen-Dropdown ─── */
    const labelAddDropdown = buildBulkDropdown(I18n.t('bulk.label-add'), (menu, close) => {
      const parentBtn = menu.previousElementSibling;
      if (parentBtn) {
        parentBtn.addEventListener('click', () => {
          if (istOffen(menu)) return; /* Schließ-Klick: nichts aufbauen (VO-1) */
          clear(menu);
          /* Texteingabe für neues Label */
          const inp = el('input', 'bulk-dropdown-input');
          inp.type = 'text';
          inp.placeholder = I18n.t('bulk.label-placeholder');
          inp.addEventListener('keydown', e => {
            if (e.key === 'Enter') {
              e.preventDefault();
              const val = inp.value.trim();
              if (val) {
                close();
                EventBus.emit('bulk:label-add', { label: val });
              }
            }
          });
          menu.appendChild(inp);

          /* Vorhandene Labels als schnelle Auswahl */
          const state = State.get();
          const labels = new Set();
          state?.board?.sprints?.forEach(s => s.cards.forEach(c => c.labels?.forEach(l => labels.add(l))));
          [...labels].sort().forEach(lbl => {
            const item = bulkItem(menu);
            item.textContent = lbl;
            item.addEventListener('click', () => {
              close();
              EventBus.emit('bulk:label-add', { label: lbl });
            });
            menu.appendChild(item);
          });

          requestAnimationFrame(() => { if (istOffen(menu)) inp.focus(); });
        });
      }
    }, true);
    bar.appendChild(labelAddDropdown);

    /* ── Label-entfernen-Dropdown ─── */
    const labelRemoveDropdown = buildBulkDropdown(I18n.t('bulk.label-remove'), (menu, close) => {
      const parentBtn = menu.previousElementSibling;
      if (parentBtn) {
        parentBtn.addEventListener('click', () => {
          if (istOffen(menu)) return; /* Schließ-Klick: nichts aufbauen (VO-1) */
          clear(menu);
          /* Alle Labels der selektierten Karten anzeigen */
          const state = State.get();
          /* Verify V-2 (Karte #827): ohne gebundene Karten — die Aktion lässt sie aus (main.js bulkAuswahl). */
          const selected = IS_SERVER_BUILD ? ohneGebundene(BulkSelect.getSelected()).frei : BulkSelect.getSelected();
          const labels = new Set();
          state?.board?.sprints?.forEach(s => s.cards.forEach(c => {
            if (selected.has(c.id)) c.labels?.forEach(l => labels.add(l));
          }));
          if (labels.size === 0) {
            bulkKopf(menu, '–', 'color:var(--text-muted)');
            return;
          }
          [...labels].sort().forEach(lbl => {
            const item = bulkItem(menu);
            item.textContent = lbl;
            item.addEventListener('click', () => {
              close();
              EventBus.emit('bulk:label-remove', { label: lbl });
            });
            menu.appendChild(item);
          });
        });
      }
    });
    bar.appendChild(labelRemoveDropdown);

    /* ── Separator ─── */
    const sep2 = el('div', 'bulk-action-sep');
    bar.appendChild(sep2);

    /* ── Löschen-Button ─── */
    const deleteBtn = el('button', 'btn btn-sm');
    deleteBtn.type = 'button';
    deleteBtn.style.color = '#c0392b';
    deleteBtn.textContent = I18n.t('bulk.delete');
    deleteBtn.addEventListener('click', () => {
      /* Verify V-2 (Karte #827): gezählt wird, was wirklich gelöscht wird. */
      const n = IS_SERVER_BUILD ? ohneGebundene(BulkSelect.getSelected()).frei.size : BulkSelect.count();
      if (confirm(I18n.t('bulk.delete-confirm', n))) {
        EventBus.emit('bulk:delete');
      }
    });
    bar.appendChild(deleteBtn);

    /* ── Trennlinie + Schließen-Button ─── */
    const sep3 = el('div', 'bulk-action-sep');
    bar.appendChild(sep3);

    const clearBtn = el('button', 'btn btn-sm');
    clearBtn.type = 'button';
    clearBtn.textContent = '×';
    clearBtn.title = I18n.t('bulk.clear-selection');
    clearBtn.setAttribute('aria-label', I18n.t('bulk.clear-selection'));
    clearBtn.addEventListener('click', () => BulkSelect.clearAll());
    bar.appendChild(clearBtn);

    /* ARIA-Live-Region (visuell versteckt) */
    let liveEl = document.getElementById('bulk-select-live');
    if (!liveEl) {
      liveEl = el('div', '');
      liveEl.id = 'bulk-select-live';
      liveEl.setAttribute('aria-live', 'polite');
      liveEl.setAttribute('aria-atomic', 'true');
      document.body.appendChild(liveEl);
    }

    document.body.appendChild(bar);
    _bulkActionBar = bar;

    /* Update-Listener */
    function updateBar({ count: n }) {
      if (n > 0) {
        bar.classList.remove('bulk-action-bar--hidden');
        countEl.textContent = I18n.t('bulk.selected-count', n);
      } else {
        bar.classList.add('bulk-action-bar--hidden');
        countEl.textContent = '';
      }
    }

    EventBus.on('bulk:changed', updateBar);

    /* Initialer Zustand */
    updateBar({ count: BulkSelect.count() });
  }

  /* Bulk-Action-Bar einmalig initialisieren (nach erstem Board-Render) */
  let _bulkBarInitialized = false;

  /* Karte #1044 (F-2804): Tageswechsel bei offenem Board — Datum je Render merken;
     ein Timer bis Mitternacht und visibilitychange zeichnen neu, wenn es nicht mehr stimmt. */
  let _heuteStand = null, _letzterStand = null, _tagWaechter = false;
  function tagPruefen() {
    if (_heuteStand && Config.get('sortierung') === 'dringlichkeit' && heuteOrtszeit() !== _heuteStand) {
      renderBoard(_letzterStand);
    }
  }
  function tagTimer() {
    const n = new Date();
    setTimeout(() => { tagPruefen(); tagTimer(); },
      new Date(n.getFullYear(), n.getMonth(), n.getDate() + 1, 0, 0, 1) - n);
  }
  function tagWaechterStarten() {
    if (_tagWaechter) return;
    _tagWaechter = true;
    document.addEventListener('visibilitychange', () => { if (!document.hidden) tagPruefen(); });
    tagTimer();
  }

  function renderBoard(state) {
    if (!state?.board) return;

    /* Karte #151: Collapse-Zustand aus Config rehydrieren (einmalig nach Config.load) */
    loadCollapsedColumnsFromConfig();
    /* F-214: verwaiste Schluessel dieses Boards wegraeumen (persistiert nur,
       wenn sich wirklich etwas geaendert hat — siehe cleanupCollapsedColumns). */
    pruneCollapsedColumns(state.board);

    /* F-388: Ziel von aria-describedby der Drag-Handles. Muss existieren,
       BEVOR die Handles gerendert werden — ein describedby auf eine fehlende
       ID liest der Screenreader stillschweigend als „keine Beschreibung",
       der Hinweis wäre also weg, ohne dass es auffällt. */
    swimlaneMoveHint();

    /* F-210: Blob-URLs der vorherigen Render-Generation freigeben */
    _revokeRendererUrls();
    /* F-238: neue Generation — in-flight Cover-Callbacks der alten erkennen */
    _renderGeneration++;

    /* Karte #63: Bulk-Action-Bar einmalig aufbauen */
    if (!_bulkBarInitialized) {
      _bulkBarInitialized = true;
      renderBulkActionBar();
    }

    /* FLIP: Snapshot aus DragDrop abholen (einmaliger Verbrauch) */
    const flipData = DragDrop.takePendingFlip();

    /* Karte #97: bei jedem Re-Render ohne DragDrop-FLIP einen Allgemein-Snapshot
       vorher anlegen — Sync-Updates / state-Mutationen animieren sich dann sanft. */
    const autoFlipRects = !flipData ? snapshotPositions('.card', 'id') : null;
    /* #326: Swimlane-Auto-FLIP — analog autoFlipRects für Karten. */
    const autoFlipLanes = !flipData ? snapshotPositions('.swimlane', 'sprintId') : null;

    applyBoardBackground(state.board);
    renderHeader(state.board);
    renderAwaitingBanner(state.board);
    renderCompactionBanner();
    renderChatTailFooter(state.board);

    const container = document.getElementById('board-columns');
    clear(container);
    /* F-2710: Titel-Tooltip nur bei gekürztem Titel (ein Listener am Container, nicht je Karte) */
    if (!_titelTooltipContainer.has(container)) {
      _titelTooltipContainer.add(container);
      container.addEventListener('pointerover', onTitelPointerOver);
    }
    /* F-240: pendende Cleanup-Timer der nun verworfenen Knoten abbrechen */
    clearAnimTimers();

    const { columnDefs, sprints } = state.board;
    const isFlat = sprints.length <= 1;
    const heute = heuteOrtszeit();  /* Karte #1004: einmal je Render */
    _heuteStand = heute; _letzterStand = state; tagWaechterStarten();

    container.classList.toggle('swimlane-mode', !isFlat);

    if (isFlat) {
      const backlog = sprints[0] ?? { id: '__backlog__', cards: [] };
      columnDefs.forEach(colDef => {
        const cards = backlog.cards.filter(c => c.columnId === colDef.id);
        container.appendChild(renderColumn(colDef, cards, backlog.id, heute));
      });
    } else {
      /* Beim ersten Swimlane-Render: alle außer dem ersten Sprint einklappen */
      if (!swimlaneInitialized) {
        sprints.forEach((sprint, idx) => {
          if (idx > 0) collapsedSprints.add(sprint.id);
        });
        swimlaneInitialized = true;
      }
      /* ADR-0117: Eine eingeblendete archivierte Swimlane startet eingeklappt.
         Sonst waere „Archiv anzeigen" bei vielen archivierten Sprints eine
         Karten-Lawine — und der Schalter damit unbrauchbar fuer seinen
         eigentlichen Zweck. Aufklappen bleibt jederzeit moeglich; das `add`
         greift nur, solange der Nutzer sie in dieser Sitzung nicht selbst
         aufgeklappt hat (deshalb ein Merker statt eines Zwangs bei jedem
         Render — ein Zwang wuerde das Aufklappen sofort wieder zunichte
         machen). */
      sprints.forEach(sprint => {
        if (sprint.archived) {
          if (!_archivEingeklappt.has(sprint.id)) {
            collapsedSprints.add(sprint.id);
            _archivEingeklappt.add(sprint.id);
          }
        } else {
          /* Zurueckgeholt → Merker loeschen, sonst startet ein spaeter erneut
             archivierter Sprint in derselben Sitzung NICHT eingeklappt und die
             Zusage aus ADR-0117 §2 gilt nur beim ersten Mal. Der Merker wurde
             ausser bei `resetSwimlaneState` (Board-Wechsel/Reload) nie geleert
             — bei vielen Archivierungen baute sich so ueber eine Sitzung genau
             die Karten-Lawine wieder auf, die die Archivierung verhindern soll
             (Sprint-56-Kritiker, F-655). */
          _archivEingeklappt.delete(sprint.id);
        }
      });

      sprints.forEach(sprint => {
        /* ADR-0117: archivierte Swimlanes nur zeigen, wenn der Archiv-Filter
           aktiv ist — derselbe Schalter wie fuer archivierte Karten. */
        if (sprint.archived && !Filter.isShowingArchived()) return;
        /* Karte #156: Swimlane ohne matchende Karten bei aktiver Suche komplett überspringen */
        if (Search.isActive()) {
          const hasMatch = sprint.cards.some(c => !c.archived && c.kind !== 'sprint-meta' && Search.matches(c));
          /* Kritik Sprint 93 K-1: Gespiegelte Lanes einer externen Kopie
             (Marke `spiegel`, ADR-0141 §4) tragen nur Schatten, keine Karten —
             sie bleiben stehen, die Schatten filtert die Suche selbst. */
          if (!hasMatch && !(IS_SERVER_BUILD && markeWirkt(sprint))) return;
        }
        const lane = el('div', 'swimlane');
        lane.dataset.sprintId = sprint.id;
        lane.appendChild(renderSprintHeader(sprint, columnDefs, state.board));
        /* Punkt 2 (Karte #126): Progress-Linie als Geschwister zum Header,
           NICHT innerhalb von .sprint-header — bleibt sichtbar wenn Sprint kollabiert ist.
           ADR-0046: Progress-Linie nur für echte Sprints — Lanes/Backlog haben keine Sprint-Fortschritts-Metrik. */
        const swimlaneKind = swimlaneKindOf(sprint);
        /* Kritik Sprint 93 D-2: Die Kopie hat keine Sprint-Mechanik (ADR-0141 §4). */
        if (swimlaneKind === 'sprint' && !(IS_SERVER_BUILD && markeWirkt(sprint))) {
          /* F-1085: board mitgeben, damit isDoneColumnId() die echten columnDefs sieht */
          const metrics = Metrics.computeSprintMetrics(sprint, state.board);
          lane.appendChild(buildSprintProgressLine(metrics));
        }
        const cols = el('div', 'swimlane-columns');
        if (collapsedSprints.has(sprint.id)) cols.style.display = 'none';
        columnDefs.forEach(colDef => {
          let cards = sprint.cards.filter(c => c.columnId === colDef.id);
          /* Karte #177: in der Backlog-Spalte eines Sprints stehen die Meta-
             Karten an festen Rändern — 📋 Sprint-Start oben, 🏁 Sprint-
             Abschluss unten. Normale Karten behalten ihre relative Reihenfolge. */
          if (swimlaneKind === 'sprint' && isBacklogColumn(colDef)) {
            cards = sortMetaCardsBacklog(cards);
          }
          cols.appendChild(renderColumn(colDef, cards, sprint.id, heute));
        });
        lane.appendChild(cols);
        container.appendChild(lane);
      });
    }

    /* FLIP anwenden (nach vollständigem DOM-Rebuild) */
    if (flipData) {
      requestAnimationFrame(() => flipAnimate(flipData));
    } else if ((autoFlipRects && autoFlipRects.size > 0) || (autoFlipLanes && autoFlipLanes.size > 0)) {
      requestAnimationFrame(() => flipAnimate({
        rects: autoFlipRects || new Map(),
        laneRects: autoFlipLanes || new Map(),
        droppedCardId: null,
      }));
    }

    /* Karte #63: Bulk-Select-DOM-Klassen nach Re-Render synchronisieren */
    BulkSelect.onBoardRender();

    /* F-388 (Karte #344): Fokus zurück auf das Drag-Handle der eben per
       Tastatur verschobenen Swimlane. Muss NACH dem DOM-Neubau laufen — der
       alte Knoten samt Fokus existiert dann nicht mehr. Ohne das wäre nach
       einem Tastendruck Schluss, weil der Fokus auf <body> landet. */
    restoreSwimlaneFokus();
  }

  EventBus.on('board:background-changed', () => {
    const b = State.get()?.board;
    if (b) applyBoardBackground(b);
  });

  /* F-223 (Karte #201): Board-Swap → Module-Singletons zurücksetzen.
     Listener auf 'board:loaded' — main.js emittiert dieses Event nach
     jedem Board-Wechsel (sync:switch-to-board, server:new-board,
     standalone:switch-to-file). Kapselt den Reset zentral im Renderer
     statt extern über resetSwimlaneState() aus main.js aufzurufen.
     Backward-Kompatibel: resetSwimlaneState() bleibt exportiert für
     direkte Aufrufe und Tests. */
  EventBus.on('board:loaded', () => {
    resetSwimlaneState();
  });

  /* Karte #177: Sprint-Backlog-Sortierung — 📋 Sprint-Start oben,
     🏁 Sprint-Abschluss unten, normale Karten dazwischen (stable).
     Exportiert für Tests. */
  function sortMetaCardsBacklog(cards) {
    const start = cards.filter(c => c.metaType === 'start');
    const end   = cards.filter(c => c.metaType === 'end');
    const rest  = cards.filter(c => c.metaType !== 'start' && c.metaType !== 'end');
    return [...start, ...rest, ...end];
  }

  return { renderBoard, cleanupCollapsedColumns, pruneCollapsedColumns, resetSwimlaneState, sortMetaCardsBacklog };
})();
