import { Config, DARSTELLUNG } from './config.js';
import { Profile }            from './profile.js';
import { SettingsScope, SECTION_DIALOG_MAP, isSectionRelevant } from './settings-scope.js';
import { I18n }               from './i18n.js';
import { EventBus }           from './eventbus.js';
import { Sync }               from './sync.js';
import { State }              from './state.js';
import { addSwipeToDismiss }  from './swipe.js';
import { Modal }              from './modal.js';
/* #441 (ADR-0118): letzter der acht Einstellungsdialoge auf der Modal-Shell —
   siehe ADR-0118 §6 ("Offen: Migration der strukturellen Teile von
   configdialog.js"). Anders als bei den sieben zuvor migrierten Dialogen ist
   dieses Modul NICHT IS_SERVER_BUILD-gegated → der Import wirkt sich real auf
   das Slim-Template aus (dort verschwand modal-shell.js bisher per DCE mit
   jedem gegateten Konsumenten). */
import { createModal }        from './modal-shell.js';
/* Karte #450/#443: Board-Anhänge laufen über das Storage-Plugin, nicht mehr
   direkt über `attachments.js` — dieselbe Umgehung, die im Karten-Modal den
   REST-Weg umging, stand hier für den Board-Hintergrund. `verkleinereBild` ist
   eine reine Bildoperation ohne Transportweg und bleibt ein direkter Import.

   Stand seit Karte #443: Board-Anhänge nehmen den REST-Weg. Der fehlende
   Endpunkt existiert (`/boards/{id}/board-attachments`, ohne Karten-id), das
   REST-Backend meldet `boardAnhangImBoardState() === false`, und die
   Grössenprüfung läuft gegen das Limit dieses Endpunkts statt gegen den
   Board-State. Bis dahin stand hier das Gegenteil — richtig für #450,
   überholt seit #443 (Frontend-Kritiker Sprint 61).

   Zwei Dinge, die man beim Lesen auseinanderhalten muss:
   - `boardAnhangImBoardState()` beschreibt den TRANSPORTWEG und steuert die
     Grössenprüfung.
   - `boardBildVerkleinern()` beschreibt die ÜBERTRAGUNGSMENGE beim Anzeigen
     und steuert `verkleinereBild`. Ein Avatar wird mit 40 px Kante gezeigt;
     über REST lädt ihn jeder Betrachter, also wird weiter verkleinert,
     obwohl nichts mehr im State liegt. */
import { verkleinereBild } from './attachments.js';
import { StoragePlugin, createFsaAttachmentBackend } from './storage-plugin.js';

function anhangBackend() {
  return StoragePlugin.getActivePlugin()?.attachments?.() ?? createFsaAttachmentBackend();
}

import { buildAvatar, defaultUserIdentity } from './identity.js';
import { buildBackupSection } from './backup-section.js';
/* Karte #702: Freigabe-Sektion (Server-Build; sonst ShareSection === null → DCE). */
import { ShareSection } from './share-section.js';
/* Karte #703: KI-Anbieter-Sektion (Server-Build; sonst LLMSection === null → DCE). */
import { LLMSection } from './llm-section.js';

/* Löscht einen Board-Anhang und entfernt seinen Eintrag aus `board.attachments`
   — aber NUR, wenn das Löschen geglückt ist (F-871).
 *
 * Warum das eine eigene Funktion ist: Vier Stellen in diesem Modul ersetzen
 * oder entfernen einen Board-Anhang (Hintergrund, Avatar setzen, Avatar
 * entfernen, Identität zurücksetzen). Alle vier riefen
 * `deleteBoardAttachment` und filterten den Eintrag danach BEDINGUNGSLOS
 * weg. Schlug das Löschen fehl — die Funktion verschluckte jeden Fehler —,
 * blieb die Datei unreferenziert unter `<board>/__board__/` liegen, und es
 * gab keinen Weg mehr an sie heran: Die Purge-Route ist für die Board-Ablage
 * seit Karte #443 bewusst gesperrt, `DeleteBoard` greift erst beim Löschen
 * des ganzen Boards.
 *
 * Entscheid vom 2026-08-29 (Karte #460): kein neuer Aufräum-Endpunkt,
 * sondern der Fehler wird sichtbar. Der Eintrag bleibt stehen, der Nutzer
 * bekommt eine Meldung — Board-Zustand und Ablage bleiben damit konsistent,
 * und ein zweiter Versuch ist möglich.
 *
 * Was das NICHT leistet: Dateien, die heute schon verwaist sind, räumt es
 * nicht auf. Es verhindert nur neue.
 *
 * `ok === false` statt `ok !== true` ist Absicht: Ein Backend, das (noch)
 * nichts zurückgibt, verhält sich wie bisher — Eintrag weg. Andersherum
 * bliebe bei jedem geglückten Löschen ein Eintrag ohne Datei stehen, und ein
 * kaputtes Bild ist schlimmer als eine verwaiste Datei.
 *
 * @param {object} board  Board-Objekt (wird mutiert)
 * @param {object} att    der zu löschende Anhang
 * @param {object} [backend]  Anhang-Backend; Default ist das aktive Plugin
 * @returns {Promise<boolean>} true = gelöscht und Eintrag entfernt
 */
export async function entferneBoardAnhang(board, att, backend = anhangBackend()) {
  if (!att) return true;
  if ((await backend.deleteBoardAttachment(att)) === false) {
    alert(I18n.t('attachment.delete-failed'));
    return false;
  }
  board.attachments = (board.attachments || []).filter(a => a.id !== att.id);
  return true;
}

/* Compile-Time-Flag (Vite-Define, wie in config.js und storage-plugin.js):
   im Server-Frontend-Build auf true gesetzt. Bei IS_SERVER_BUILD=false
   eliminiert esbuild den Server-Bereich im Config-Dialog per DCE (Karte #203 / ADR-0064). */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Karte #248 / ADR-0067: __PROJECTOR_BUILD__=true im claude-code-Build
   (Vite-Define, Muster wie i18n.js/sync.js). Solo/Slim-Template + Server-
   Frontend = 'false'. */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

/* Karte #248: Der KI-Kontext-Export ist im claude-code-Build redundant
   (Claude liest aboard-sync.json direkt) → analog zum #222-Server-Gating
   verstecken. Bedingung = Projektor-Build ODER Server-Modus. Solo/Slim
   (beide Flags false) behält den Button (Nutzer ohne Claude-Integration). */
function kiContextHidden(isServer) {
  return IS_PROJECTOR_BUILD || SettingsScope.isServerHidden('ui.ki-context', isServer);
}

/* Einstellungs-Dialog (→ ADR-0018)
   Änderungen werden als Live-Vorschau sofort sichtbar,
   aber erst bei "Speichern" persistent — "Abbrechen" setzt zurück. */
export const ConfigDialog = (() => {
  /* ADR-0065/#441: Zwei Modal-Shell-Instanzen — user (Benutzer-Einstellungen) +
     board (Board-Einstellungen). Zwei Instanzen statt einer mit Umschaltung:
     die Partitionen zeigen dauerhaft unterschiedlichen Inhalt (siehe
     SECTION_DIALOG_MAP) — eine Umschaltung müsste den Inhalt bei jedem
     Wechsel ohnehin neu aufbauen, spart also nichts gegenüber zwei
     Singletons, verkompliziert aber das unabhängige Öffnen/Schliessen beider
     Partitionen. */
  let modalUser  = null;
  let modalBoard = null;
  /* Rückwärts-Kompatibilität: overlay zeigt jeweils auf das zuletzt geöffnete */
  let overlay  = null;
  let snapshot = null; /* Zustand beim Öffnen — für Abbrechen */
  /* ADR-0040: Render-Funktion für Templates-Liste — wird in buildOverlay gesetzt */
  let _renderTemplatesList = null;
  /* Karte #335 / Sprint-58-Kritiker: Der Board-Dialog ist ein Singleton —
     `buildOverlay` läuft genau einmal. Die Backup-Sektion muss deshalb bei
     JEDEM Öffnen zum Nachladen aufgefordert werden, sonst zeigt sie bis zum
     nächsten Sprach-/Board-Wechsel den Stand vom ersten Öffnen. */
  let _backupAktualisieren = null;
  /* Karte #702: Freigabe-Sektion nachladen (bei jedem Öffnen, Board kann gewechselt haben). */
  let _shareAktualisieren = null;
  /* Karte #703: KI-Anbieter-Sektion nachladen (Firmenmodus-Stand bei jedem Öffnen). */
  let _llmAktualisieren = null;
  /* Karte #123: Render-Funktion für Identitäten-Liste — wird in buildOverlay gesetzt */
  let _renderIdentitiesList = null;

  /* F-210: ObjectURL-Tracking für Identity-Avatar-Vorschauen im Config-Dialog.
     Bei jedem _renderIdentitiesList-Aufruf werden alte blob:-URLs freigegeben,
     bevor neue erzeugt werden — verhindert Akkumulation bei Texteingaben. */
  const _configObjectUrls = new Set();
  /* Die Objekt-URL der Hintergrund-Vorschau. Getrennt vom Satz oben, weil sie
     einen anderen Lebenszyklus hat — siehe `refreshBgPreview`. */
  let _bgThumbObjUrl = null;
  function _trackConfigUrl(url) {
    if (url && url.startsWith('blob:')) _configObjectUrls.add(url);
  }
  function _revokeConfigUrls() {
    _configObjectUrls.forEach(u => { try { URL.revokeObjectURL(u); } catch {} });
    _configObjectUrls.clear();
  }
  function _revokeBgThumbUrl() {
    if (_bgThumbObjUrl) { try { URL.revokeObjectURL(_bgThumbObjUrl); } catch {} }
    _bgThumbObjUrl = null;
  }

  function el(tag, attrs = {}) {
    const e = document.createElement(tag);
    Object.entries(attrs).forEach(([k, v]) => {
      if (k === 'cls') e.className = v;
      else if (k === 'text') e.textContent = v;
      else e.setAttribute(k, v);
    });
    return e;
  }

  /* Liest alle aktuellen Select-Werte aus dem Dialog */
  function readSelects() {
    const serverInterval = parseInt(overlay.querySelector('#cfg-server-interval')?.value ?? '', 10);
    return {
      /* Karte #150: Name-Feld wurde durch Identity-Liste ersetzt — Fallback aus Config */
      userName: overlay.querySelector('#cfg-userName')?.value ?? Config.get('userName'),
      theme:    overlay.querySelector('#cfg-theme')?.value,
      density:  overlay.querySelector('#cfg-density')?.value,
      ...Object.fromEntries(DARSTELLUNG.map(k => [k, overlay.querySelector(`#cfg-${k}`)?.value])),
      sortierung: overlay.querySelector('#cfg-sortierung')?.value,
      language: overlay.querySelector('#cfg-language')?.value,
      /* boardMode wird über Events gehandhabt — hier nicht speichern */
      attachmentMaxBytes:       readMB('#cfg-attachmentMax',  'attachmentMaxBytes'),
      attachmentBoardWarnBytes: readMB('#cfg-attachmentWarn', 'attachmentBoardWarnBytes'),
      attachmentBoardHardBytes: readMB('#cfg-attachmentHard', 'attachmentBoardHardBytes'),
      /* Server-Modus (Karte #78) */
      serverUrl:          overlay.querySelector('#cfg-server-url')?.value?.trim() ?? undefined,
      serverPollEnabled:  overlay.querySelector('#cfg-server-poll-enabled')?.checked ?? undefined,
      serverPollInterval: (!isNaN(serverInterval) && serverInterval >= 5 && serverInterval <= 60)
        ? serverInterval : undefined,
      /* Karte #290 (Chat-Tail, claude-code-only). Hinter IS_PROJECTOR_BUILD
         gegated, damit der slim Public-Distributable per DCE entfällt. */
      ...(IS_PROJECTOR_BUILD ? {
        chatTailEnabled: overlay.querySelector('#cfg-chat-tail-enabled')?.checked,
        chatTailLines:   (() => {
          const v = parseInt(overlay.querySelector('#cfg-chat-tail-lines')?.value ?? '', 10);
          return (!isNaN(v) && v >= 1 && v <= 100) ? v : undefined;
        })(),
      } : {}),
    };
  }

  /* MB-Eingabefeld → Bytes, mit Fallback auf bisherigen Wert.
     F-282: Bei !inp (Element fehlt — z. B. Benutzer-Dialog hat keine Attachment-Felder)
     wird undefined zurückgegeben statt Config.get(configKey), damit save() den Key
     überspringt und keine board-partitionierten Felder in den falschen Dialog schreibt.
     Der !isFinite-Zweig behält Config.get() — ungültige Nutzereingabe soll den
     bestehenden Wert NICHT überschreiben (bewusst). */
  function readMB(selector, configKey) {
    const inp = overlay.querySelector(selector);
    if (!inp) return undefined; /* F-282: Element fehlt → überspringen statt Wert zurückschreiben */
    const v = parseFloat(inp.value);
    if (!isFinite(v) || v <= 0) return Config.get(configKey);
    return Math.round(v * 1024 * 1024);
  }

  /* Wendet Werte als Live-Vorschau an (ohne localStorage) */
  function previewValues(values) {
    const root = document.documentElement;
    if (values.theme)    root.setAttribute('data-theme', values.theme);
    if (values.density)  root.setAttribute('data-density', values.density);
    for (const k of DARSTELLUNG) if (values[k]) root.setAttribute('data-' + k, values[k]);
    if (values.language) root.setAttribute('lang', values.language === 'de' ? 'de' : 'en');
  }

  function refreshBgPreview() {
    if (!overlay) return;
    const thumb = overlay.querySelector('#cfg-bg-thumb');
    if (!thumb) return;
    const rmBtn = overlay.querySelector('#cfg-bg-remove');
    const board = State.get()?.board;
    const id  = board?.background?.attachmentId;
    const att = id ? (board.attachments || []).find(a => a.id === id) : null;
    if (att) {
      anhangBackend().boardAttachmentUrl(att).then(url => {
        if (!url) return;
        /* Seit Karte #443 liefert das REST-Backend für Board-Anhänge eine
           frische `blob:`-URL statt einer `data:`-URL. Ohne Freigabe entstünde
           bei JEDEM Öffnen des Dialogs eine weitere, die bis zum Reload im
           Speicher bleibt (Frontend-Kritiker Sprint 61).

           Bewusst NICHT über `_trackConfigUrl`/`_revokeConfigUrls`: Dieser
           Satz wird vor jedem `_renderIdentitiesList` als Ganzes freigegeben —
           die Thumbnail-URL würde dabei mit weggeräumt, obwohl das Bild noch
           angezeigt wird, und der Hintergrund verschwände beim Tippen im
           Identitäten-Feld. Stattdessen dieselbe Vorgänger-Variable wie in
           `renderer.js` (`_bgObjUrl`): genau die eine URL, die ersetzt wird. */
        if (_bgThumbObjUrl && _bgThumbObjUrl !== url) {
          try { URL.revokeObjectURL(_bgThumbObjUrl); } catch {}
        }
        _bgThumbObjUrl = url.startsWith('blob:') ? url : null;
        thumb.style.backgroundImage = `url(${url})`;
        thumb.classList.add('config-bg-thumb--set');
        if (rmBtn) rmBtn.style.display = '';
      }).catch(() => {});
    } else {
      thumb.style.backgroundImage = '';
      thumb.classList.remove('config-bg-thumb--set');
      if (rmBtn) rmBtn.style.display = 'none';
    }
    const darRow = overlay.querySelector('#cfg-bg-dar-row');
    if (darRow) {
      darRow.style.display = att ? '' : 'none';
      const sel = darRow.querySelector('select');
      if (sel) sel.value = board?.background?.darstellung || 'getoent';
    }
    const h = overlay.querySelector('#cfg-bg-hint');
    if (h) h.style.display = Profile.has('feature.attachmentLimits') ? '' : 'none';
  }

  async function _setBg(board, bereitsVerkleinert) {
    const old = board.background?.attachmentId;
    const alterAnhang = old ? (board.attachments || []).find(x => x.id === old) : null;

    /* ERST HOCHLADEN, DANN LÖSCHEN (Sicherheits- und Server-Kritiker Sprint 61).
       Bis hierher stand das Löschen vorn, und das war harmlos, solange
       `uploadBoardFile` eine reine base64-Umwandlung war, die nie fehlschlug.
       Seit Karte #443 ist es ein echtes POST, das bei 413/500/Netzabbruch
       wirft — und der Aufrufer fing das mit einem leeren `catch`. Ergebnis
       im Fehlerfall: alter Hintergrund serverseitig gelöscht, Eintrag aus
       `board.attachments` entfernt, kein neuer da, keine Meldung. Der Nutzer
       verlor sein Bild, ohne dass irgendwo etwas stand.

       Der Fall ist nicht konstruiert: F-845 hält als bewusste Restgrenze
       fest, dass die Client-Prüfung gegen die 50 MB der Voreinstellung läuft,
       während der Betreiber `ABOARD_ATTACHMENT_MAX_BYTES` kleiner setzen darf.
       Genau dann kommt das 413 nach dem Klick.

       Eine Kollision gibt es bei der neuen Reihenfolge nicht: Der neue Anhang
       trägt eine frische UUID. */
    let neuerAnhang = null;
    if (bereitsVerkleinert) {
      /* Der Parameter heisst so, wie er gemeint ist: die Datei ist hier bereits verkleinert — der Aufrufer erledigt das, BEVOR
         er prueft (Karte #450, Kritiker-Befund).

         WARUM DIE REIHENFOLGE UMGEZOGEN IST: Bis Sprint 61 verkleinerte diese
         Funktion selbst, und `pickBoardBackground` prüfte davor mit der
         ORIGINALDATEI. Solange das Server-Profil kein `feature.attachmentLimits`
         hatte, war das folgenlos — `checkUpload` stieg dort sofort mit
         `{ok:true}` aus. Seit Karte #450 das Flag setzt, greift dort der
         Pro-Datei-Check (Default 2 MB) auf die unverkleinerte Datei: Ein
         normales Handy-Foto wurde mit „zu gross" abgelehnt, obwohl es
         verkleinert ~400 KB wiegt. Der Hintergrund war im Server-Modus damit
         nicht mehr setzbar — eine Regression, die derselbe Commit erzeugt hat,
         der den Upload-Pfad im Karten-Modal reparierte.

         Warum die Verkleinerung ueberhaupt: Im Server-Modus reist ein
         Board-Attachment als base64 IM BOARD-STATE mit — bei jedem Speichern
         und jedem Poll. Ein 4-MB-Foto riss dort die 10-MB-Grenze des
         Board-Endpunkts und der Nutzer bekam einen stillen 413. */
      neuerAnhang = await anhangBackend().uploadBoardFile(bereitsVerkleinert);
    }

    /* Ab hier ist nichts mehr zu erwarten, das wirft — erst jetzt wird der
       Bestand angefasst. */
    /* F-871: Scheitert das Löschen, bleibt der Eintrag stehen (der Nutzer
       bekommt die Meldung aus `entferneBoardAnhang`).

       ERSETZEN und ENTFERNEN gehen hier auseinander (Sprint-62-Kritiker, K1):

       Beim ERSETZEN wird der neue Hintergrund trotzdem gesetzt — der Upload
       ist geglückt, ihn zu verwerfen hiesse, den Fehler des einen Schritts auf
       den anderen zu übertragen. Der alte Eintrag bleibt in
       `board.attachments`, seine Datei existiert — Eintrag und Ablage stimmen
       also überein.

       WAS DABEI TROTZDEM ZURÜCKBLEIBT (Sprint-62-Kritiker, V6): Dieser
       Eintrag ist danach weder Hintergrund noch Avatar. Keine Oberfläche
       zeigt ihn, keine entfernt ihn, und `computeBoardBytes` zählt ihn
       dauerhaft mit. Ein zweiter Löschversuch ist nicht möglich, weil es
       keinen Knopf dafür gibt. Der frühere Kommentar nannte das „alles ist
       konsistent" — das stimmt für Eintrag und Datei, verschweigt aber, dass
       es keinen Rückweg gibt. Das ist der bewusst gewählte kleinere Schaden
       gegenüber der Alternative (siehe unten), aber es ist einer, und er
       gehört benannt statt beschönigt. Vollständig löst ihn erst eine
       Verwaltungsansicht für Board-Anhänge — es gibt keine.

       Beim ENTFERNEN gibt es keinen Upload, der etwas rechtfertigt. Würde
       `board.background = null` trotz fehlgeschlagenem Löschen laufen, wäre
       die Lage SCHLIMMER als vor der Karte: `refreshBgPreview` findet keinen
       Hintergrund mehr, blendet den Entfernen-Knopf aus, und `_setBg` ist über
       `if (board?.background)` nicht mehr erreichbar — der Nutzer hat keinen
       zweiten Versuch, während Eintrag UND Datei liegenbleiben. Genau der
       Zustand ohne Rückweg, den F-871 beheben sollte. Deshalb: kein Abräumen
       ohne geglücktes Löschen. */
    const geloescht = alterAnhang ? await entferneBoardAnhang(board, alterAnhang) : true;
    if (neuerAnhang) {
      if (!Array.isArray(board.attachments)) board.attachments = [];
      board.attachments.push(neuerAnhang);
      /* #727: Darstellung beim Bildwechsel behalten */
      const dar = board.background?.darstellung;
      board.background = dar ? { attachmentId: neuerAnhang.id, darstellung: dar } : { attachmentId: neuerAnhang.id };
    } else if (geloescht) {
      board.background = null;
    }
    State.set(State.get());
    EventBus.emit('board:background-changed');
    refreshBgPreview();
  }

  function pickBoardBackground() {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.accept = 'image/png,image/jpeg,image/gif,image/webp,image/avif';
    inp.onchange = async (e) => {
      const file = e.target.files?.[0];
      if (!file || !file.type || !file.type.startsWith('image/') || file.type === 'image/svg+xml') return;
      const board = State.get()?.board;
      if (!board) return;
      /* Erst verkleinern, dann pruefen (Karte #450): Geprueft werden muss, was
         tatsaechlich gespeichert wird — sonst lehnt der Vorcheck eine Datei ab,
         die in dieser Groesse nie ankommt.

         Verkleinert wird NUR, wenn die Datei im Board-State landet
         (Karte #450-Nacharbeit, Frontend-Kritiker Sprint 61). Die Auskunft
         gab es seit dem Umbau, wurde aber von niemandem gelesen — im
         `claude-code`-Profil ging der Hintergrund ins FSA-Volume und wurde
         trotzdem verlustbehaftet nach JPEG konvertiert (Alphakanal weg,
         Umbenennung auf `.jpg`), ohne dass das irgendwo Platz gespart hätte. */
      /* Karte #443: Die Verkleinerung hing bis hier am Transportweg-Schalter
         `boardAnhangImBoardState()`. Das war so lange richtig, wie „liegt im
         State" und „geht durch die Leitung" dasselbe bedeuteten. Seit
         Board-Anhänge per REST reisen, ist es das nicht mehr: Sie liegen NICHT
         im State und gehen trotzdem bei jedem Betrachter über das Netz.
         Deshalb ein eigener Schalter für eine eigene Frage. */
      const kleiner = anhangBackend().boardBildVerkleinern() ? await verkleinereBild(file) : file;
      const chk = anhangBackend().checkBoardUpload(kleiner, board);
      if (!chk.ok) { alert(chk.reason); return; }
      if (chk.warning) alert(chk.warning);
      /* Kein leerer catch mehr: Seit Karte #443 wirft der Upload bei 413/500/
         Netzabbruch. Ohne Meldung stand der Nutzer vor einem unveränderten
         Dialog und wusste nicht, ob etwas passiert ist. */
      try { await _setBg(board, kleiner); }
      catch { alert(I18n.t('attachment.write-failed')); }
    };
    inp.click();
  }

  async function removeBoardBackground() {
    const board = State.get()?.board;
    if (board?.background) await _setBg(board, null);
  }

  /* ── Identity-Bild-Upload (Karte #123) ────────────────────────── */
  function pickIdentityImage(identity) {
    const inp = document.createElement('input');
    inp.type = 'file';
    /* MIME-Whitelist — SVG bewusst ausgeschlossen (F-128, ADR-0033) */
    inp.accept = 'image/png,image/jpeg,image/gif,image/webp,image/avif';
    inp.onchange = async (e) => {
      const file = e.target.files?.[0];
      if (!file) return;
      /* Defensive Doppelprüfung — manche Browser ignorieren accept */
      if (file.type === 'image/svg+xml') return;
      if (!file.type.startsWith('image/')) return;
      const board = State.get()?.board;
      if (!board) return;
      /* Erst verkleinern, dann pruefen — siehe `pickBoardBackground` und
         `_setBg` (Karte #450). 512 px: Avatare werden mit maximal 40 px
         Kantenlaenge gezeigt (SIZE_MAP in identity.js), das laesst genug
         Reserve fuer hohe Pixeldichte. */
      const kleiner = anhangBackend().boardBildVerkleinern()
        ? await verkleinereBild(file, { maxKante: 512 })
        : file;
      const chk = anhangBackend().checkBoardUpload(kleiner, board);
      if (!chk.ok) { alert(chk.reason); return; }
      if (chk.warning) alert(chk.warning);
      try {
        /* Erst hochladen, dann den alten entfernen — dieselbe Umkehrung wie in
           `_setBg` und aus demselben Grund: Seit Karte #443 kann der Upload
           werfen, und dann wäre der alte Avatar bereits weg. Die Meldung im
           `catch` gab es hier schon; der Zustand war trotzdem inkonsistent
           (`identity.imageAttachmentId` zeigte auf einen gelöschten Anhang). */
        const alterAvatar = identity.imageAttachmentId
          ? (board.attachments || []).find(a => a.id === identity.imageAttachmentId)
          : null;
        /* `kleiner` statt `file`: Dieser Pfad landet über dasselbe
           `uploadBoardFile` im selben `board.attachments` und trägt dieselbe
           413-Falle wie der Hintergrund (Kritiker zu Karte #439) — ein 6-MB-Foto
           als Avatar reisst die 10-MB-Grenze des Board-Endpunkts genauso. Ein
           Guard, der nur einen von zwei Pfaden abdeckt, ist kein Guard.

           Der frühere Satz „und `checkUpload` oben lässt es im Server-Profil
           ungeprüft durch" ist seit Karte #450 falsch — das Server-Profil führt
           `feature.attachmentLimits`, die Prüfung greift dort. Genau deshalb
           muss sie die VERKLEINERTE Datei sehen. */
        const att = await anhangBackend().uploadBoardFile(kleiner);
        /* F-871: wie in `_setBg` — bleibt der alte Avatar liegen, wird das
           gemeldet, der neue aber trotzdem gesetzt. */
        if (alterAvatar) await entferneBoardAnhang(board, alterAvatar);
        if (!Array.isArray(board.attachments)) board.attachments = [];
        board.attachments.push(att);
        identity.imageAttachmentId = att.id;
        State.set({ ...State.get() });
        if (_renderIdentitiesList) _renderIdentitiesList();
      } catch {
        alert(I18n.t('attachment.write-failed'));
      }
    };
    inp.click();
  }

  async function removeIdentityImage(identity) {
    const board = State.get()?.board;
    if (!board || !identity.imageAttachmentId) return;
    const old = (board.attachments || []).find(a => a.id === identity.imageAttachmentId);
    /* F-871: Hier hängt mehr am Erfolg als beim Ersetzen — schlägt das Löschen
       fehl, bleibt auch die Verknüpfung stehen. Sonst zeigte `identity` auf
       nichts mehr, während Eintrag und Datei weiterexistieren, und der Nutzer
       hätte keinen Weg mehr, den Anhang loszuwerden. */
    if (old && !(await entferneBoardAnhang(board, old))) return;
    identity.imageAttachmentId = null;
    State.set({ ...State.get() });
    if (_renderIdentitiesList) _renderIdentitiesList();
  }

  async function resetIdentity(identity) {
    const board = State.get()?.board;
    if (!board) return;
    /* Bild ggf. löschen */
    if (identity.imageAttachmentId) {
      const old = (board.attachments || []).find(a => a.id === identity.imageAttachmentId);
      /* F-871: Anders als in `removeIdentityImage` bricht ein fehlgeschlagenes
         Löschen den Reset NICHT ab — der Nutzer will hier Label, Farbe und
         Symbol zurücksetzen, und die Meldung hat er bereits gesehen. Die
         Verknüpfung bleibt aber bestehen, solange die Datei da ist. */
      if (await entferneBoardAnhang(board, old)) identity.imageAttachmentId = null;
    }
    if (identity.kind === 'user') {
      /* User-Identity zurücksetzen: Label aus aktuellem config.userName ableiten,
         Farbe + Icon entsprechend. */
      const fresh = defaultUserIdentity(Config.get('userName') || '');
      identity.label = fresh.label;
      identity.color = fresh.color;
      identity.icon  = fresh.icon;
    }
    /* Karte #171: Claude-Identitäten werden nicht mehr im Settings-Dialog
       angezeigt — der Reset-Pfad ist daher unerreichbar. CLAUDE_DEFAULTS bleibt
       die statische Farb-/Symbol-Quelle in resolveIdentity. */
    State.set({ ...State.get() });
    if (_renderIdentitiesList) _renderIdentitiesList();
  }

  /* ADR-0065: Parametrisierter Renderer — partition = 'user' | 'board'.
     Sektions-Zugehörigkeit kommt aus SECTION_DIALOG_MAP (settings-scope.js, F-281 — ADR-0065 §1). */
  function buildOverlay(partition) {
    /* F-281: Sektions-Set aus der Single Source of Truth ableiten statt Inline-Lambda */
    const sectionSet = new Set(SECTION_DIALOG_MAP[partition] ?? []);
    const has = (s) => sectionSet.has(s);
    const titleKey = partition === 'user' ? 'config.title.user' : 'config.title.board';
    const titleId  = `config-dialog-title-${partition}`;
    const cancelFn = () => cancel(partition);

    /* #441 (ADR-0118 §6): Overlay/Panel/Header/Body + ESC/Klick-außerhalb/
       Focus-Trap/Initial-Focus/Focus-Restore kommen jetzt aus der
       Modal-Shell statt aus handgerolltem Code — dieselben Klassen, die
       dieser Dialog vorher schon von Hand erzeugt hat (`.modal-overlay`/
       `.modal`/`.modal-header-row`/`.modal-dialog-title`/`.btn-modal-close`/
       `.modal-body`). Kein `variant` (Default = zentriert, z-index 100 wie
       das Karten-Modal): Dieser Dialog gehörte schon vor #348 zur
       „Karten-Modal-Basis"-Familie, nicht zu Familie B/„settings"
       (ADR-0118, Einleitungstabelle) — der Umbau ändert daran nichts.
       `extraOverlayClass` trägt zusätzlich die F-749-Marker-Klasse
       `modal-overlay--blocks-sync` (Karten-Modal-Analogie, siehe main.js) —
       die Shell hängt den ganzen String unverändert an, sie erwartet dort
       keine einzelne Klasse. */
    const modal = createModal({
      extraOverlayClass: 'config-overlay modal-overlay--blocks-sync',
      extraPanelClass:   'config-dialog',
      titleId,
      closeAriaLabel: I18n.t('btn.close-modal'),
      footer: true,
      onRequestClose: cancelFn,
    });
    modal.setTitle(I18n.t(titleKey));
    modal.closeBtn.title = I18n.t('btn.close-modal'); /* Bestandsschutz: Tooltip gab es vorher */

    /* Scrollbarer Body — von der Shell bereits gebaut und in `panel` eingehängt. */
    const dialogBody = modal.body;

    function addRow(labelKey, configKey, options) {
      const row = el('div', { cls: 'config-row' });
      const lbl = el('label', { cls: 'config-label', text: I18n.t(labelKey) });
      const sel = el('select', { cls: 'config-select', id: `cfg-${configKey}` });
      lbl.setAttribute('for', `cfg-${configKey}`);
      options.forEach(([value, labelK]) => {
        sel.appendChild(el('option', { value, text: I18n.t(labelK) }));
      });
      sel.addEventListener('change', () => previewValues(readSelects()));
      row.appendChild(lbl);
      row.appendChild(sel);
      dialogBody.appendChild(row);
    }

    /* ADR-0065: Sektionen nur rendern wenn sie zur Partition gehören */

    /* ── Freigabe (Partition: board, ganz oben) ───────────────── */
    /* Karte #702: nur Server-Build UND Server-Modus; wirkt sofort am Server
       (kein Speichern/Abbrechen) — die Sektion weist selbst darauf hin. */
    if (IS_SERVER_BUILD && ShareSection && has('share')
        && isSectionRelevant('share', Config.get('boardMode'))) {
      const boardId = State.get()?.board?.id;
      if (boardId) {
        const freigabe = ShareSection.build(boardId);
        dialogBody.appendChild(freigabe.el);
        _shareAktualisieren = () => freigabe.aktualisieren(State.get()?.board?.id ?? boardId);
      }
    }

    /* ── Identitäten (Partition: user) */
    /* ADR-0080 (revidiert ADR-0078): Sektion im Server-Modus wieder sichtbar.
       Farbe/Icon sind lokale kosmetische Präferenzen ohne Forward-Auth-Bezug.
       Nur der Anzeigename (Label-Feld der user-default-Identity) wird im
       Server-Modus auf Feld-Ebene ausgeblendet (s. _renderIdentitiesList). */
    const isServerModeUser = Profile.has('feature.restStorage', Config.get('boardMode'));
    let identitiesList = null;
    if (has('identities') && isSectionRelevant('identities', Config.get('boardMode'))) {
      /* Karte #150: Identitäten-Section (Karte #123) — ganz oben, deckt User-Name/Farbe/Icon ab. */
      const identitiesSection = el('section', { cls: 'config-identities-section' });
      identitiesSection.setAttribute('aria-labelledby', 'cfg-identities-heading');
      const identitiesHeading = el('div', { cls: 'config-separator', id: 'cfg-identities-heading',
                                              text: I18n.t('identity.section-title') });
      identitiesSection.appendChild(identitiesHeading);
      identitiesList = el('div', { cls: 'config-identities-list', id: 'cfg-identities-list' });
      identitiesSection.appendChild(identitiesList);
      dialogBody.appendChild(identitiesSection);
    }

    /* ── Theme + Sprache (Partition: user) */
    if (has('theme')) {
      addRow('config.theme', 'theme', [
        ['system', 'config.theme.system'],
        ['light',  'config.theme.light'],
        ['dark',   'config.theme.dark'],
      ]);
      /* Karte #973: Kartendichte, Vorbild Planka (dort vier Stufen, hier zwei). */
      addRow('config.density', 'density', [
        ['normal',  'config.density.normal'],
        ['kompakt', 'config.density.kompakt'],
      ]);
      /* ADR-0153 (Karte #1183): Darstellung — Ecken, Schrift, Akzentfarbe */
      addRow('config.corners', 'corners', [['square', 'config.corners.square'], ['round', 'config.corners.round']]);
      addRow('config.font', 'font', ['system', 'serif', 'mono'].map(v => [v, 'config.font.' + v]));
      addRow('config.accent', 'accent', ['teal', 'blue', 'green', 'violet', 'graphite'].map(v => [v, 'config.accent.' + v]));
      addRow('config.sortierung', 'sortierung', [
        ['manuell',       'config.sortierung.manuell'],
        ['dringlichkeit', 'config.sortierung.dringlichkeit'],
      ]);
    }
    if (has('language')) {
      addRow('config.language', 'language', [
        ['en', 'config.lang.en'],
        ['de', 'config.lang.de'],
      ]);
    }

    /* ── KI-Anbieter (Partition: user) ─────────────────────────── */
    /* Karte #703: Schlüssel gehören der Person, nicht dem Board. Nur
       Server-Build UND Server-Modus; der Knopf öffnet den LLMKeysDialog. */
    if (IS_SERVER_BUILD && LLMSection && has('llm')
        && isSectionRelevant('llm', Config.get('boardMode'))) {
      const ki = LLMSection.build();
      dialogBody.appendChild(ki.el);
      _llmAktualisieren = ki.aktualisieren;
    }

    /* ── Modus-Sektion (Partition: board) ──────────────────────── */
    /* Karte #207: Mode-Switch-Bereich im Server-Modus ausblenden (widersprüchlich).
       modeContainer fasst Trenner + Zeile zusammen für einfaches hide/show. */
    let modeContainer = null;
    let modeSel = null;
    let statusVal = null;
    let projectArea = null;
    /* F-427: Status- und Deaktivieren-Zeile werden getrennt vom Container
       getoggelt — der Ordner-Button muss modus-unabhaengig erreichbar sein. */
    let statusRow = null;
    let disableRow = null;
    if (has('mode')) {
      modeContainer = el('div', { cls: 'config-mode-container' });
      const projectSep = el('div', { cls: 'config-separator', text: I18n.t('config.project-section') });
      modeContainer.appendChild(projectSep);

      const modeRow = el('div', { cls: 'config-row' });
      const modeLbl = el('label', { cls: 'config-label', text: I18n.t('config.board-mode'), for: 'cfg-boardMode' });
      modeSel = el('select', { cls: 'config-select', id: 'cfg-boardMode' });
      [
        ['solo',         'config.mode.solo'],
        ['team',         'config.mode.team'],
        ['claude-code',  'config.mode.claude-code'],
        ['server',       'config.mode.server'],
      ].forEach(([v, k]) => {
        const opt = el('option', { value: v, text: I18n.t(k) });
        /* Server-Option: Phase 2 — noch nicht implementiert */
        if (v === 'server') {
          opt.disabled = true;
          opt.title = I18n.t('config.profile-server-hint');
        }
        modeSel.appendChild(opt);
      });
      modeRow.appendChild(modeLbl);
      modeRow.appendChild(modeSel);
      modeContainer.appendChild(modeRow);
      dialogBody.appendChild(modeContainer);
    }

    /* ── Projektordner-Bereich (Partition: project) ─────────────── */
    if (has('project')) {
      /* Projekt-Bereich (nur sichtbar wenn project) */
      projectArea = el('div', { cls: 'config-project-area' });

      /* Status-Zeile */
      statusRow = el('div', { cls: 'config-row' });
      const statusLbl = el('span', { cls: 'config-label', text: I18n.t('config.project-status') });
      statusVal = el('span', { cls: 'config-project-status', id: 'cfg-project-status' });
      statusRow.appendChild(statusLbl);
      statusRow.appendChild(statusVal);
      projectArea.appendChild(statusRow);

      /* Ordner-Button */
      const dirRow = el('div', { cls: 'config-row' });
      const dirBtn = el('button', { cls: 'btn btn-sm', text: I18n.t('config.pick-dir'), type: 'button' });
      dirBtn.addEventListener('click', async () => {
        closeOverlay(partition);
        EventBus.emit('sync:pick-directory');
      });
      dirRow.appendChild(el('span', { cls: 'config-label', text: I18n.t('config.project-dir') }));
      dirRow.appendChild(dirBtn);
      projectArea.appendChild(dirRow);

      /* Deaktivieren-Button */
      disableRow = el('div', { cls: 'config-row' });
      const disableBtn = el('button', { cls: 'btn btn-sm btn-danger', text: I18n.t('config.disable-claude-code'), type: 'button' });
      disableBtn.addEventListener('click', () => {
        closeOverlay(partition);
        EventBus.emit('sync:disable');
      });
      disableRow.appendChild(el('div'));
      disableRow.appendChild(disableBtn);
      projectArea.appendChild(disableRow);

      dialogBody.appendChild(projectArea);
    }

    /* ── Chat-Tail (Karte #290) — nur claude-code-Projektor-Build ──
       Die Sektion zeigt einen Toggle und ein Lines-Feld. Beim Speichern
       übernimmt save() den Wert in Config UND ruft Sync.setChatTailEnabled
       auf, das den Hook-Marker via FSA schreibt/löscht. Im slim-Build
       (IS_PROJECTOR_BUILD=false) eliminiert esbuild diesen Block per DCE. */
    let chatTailArea = null;
    if (IS_PROJECTOR_BUILD && has('chat-tail')) {
      chatTailArea = el('div', { cls: 'config-chat-tail-area' });
      chatTailArea.appendChild(el('div', { cls: 'config-separator', text: I18n.t('chat-tail.section') }));

      const enRow = el('div', { cls: 'config-row' });
      const enLbl = el('label', { cls: 'config-label', text: I18n.t('chat-tail.enabled'), for: 'cfg-chat-tail-enabled' });
      const enChk = el('input', { type: 'checkbox', id: 'cfg-chat-tail-enabled' });
      enRow.appendChild(enLbl);
      enRow.appendChild(enChk);
      chatTailArea.appendChild(enRow);

      const linesRow = el('div', { cls: 'config-row' });
      const linesLbl = el('label', { cls: 'config-label', text: I18n.t('chat-tail.lines'), for: 'cfg-chat-tail-lines' });
      const linesInp = el('input', { type: 'number', cls: 'config-select', id: 'cfg-chat-tail-lines', min: '1', max: '100', step: '1' });
      linesRow.appendChild(linesLbl);
      linesRow.appendChild(linesInp);
      chatTailArea.appendChild(linesRow);

      const hint = el('p', { cls: 'config-bg-hint', text: I18n.t('chat-tail.hint') });
      chatTailArea.appendChild(hint);

      dialogBody.appendChild(chatTailArea);
    }

    /* ── Server-Bereich (Partition: server) ────────────────────── */
    /* Karte #203 / ADR-0064: Nur im Server-Build gebaut — IS_SERVER_BUILD=false → DCE.
       Im Nicht-Server-Build ist serverArea null; refreshProjectArea() und
       populateSelects() verwenden bereits defensive Zugriffe (?.style, querySelector-Guard). */
    let serverArea = null;
    if (IS_SERVER_BUILD && has('server')) {
      serverArea = el('div', { cls: 'config-server-area' });

      /* Erweitert-Sektion (zugeklappt, kein open-Attribut) — Karte #223 */
      const advDetails = el('details', { cls: 'config-advanced' });
      const advSummary = el('summary', { cls: 'config-advanced__summary', text: I18n.t('config.advanced-section') });
      advDetails.appendChild(advSummary);

      /* Server-URL */
      const urlRow = el('div', { cls: 'config-row' });
      const urlLbl = el('label', { cls: 'config-label', text: I18n.t('server.url'), for: 'cfg-server-url' });
      const urlInp = el('input', { type: 'text', cls: 'config-select', id: 'cfg-server-url' });
      urlInp.placeholder = I18n.t('server.url-placeholder');
      urlRow.appendChild(urlLbl);
      urlRow.appendChild(urlInp);
      advDetails.appendChild(urlRow);

      /* Polling ein/aus */
      const pollEnabledRow = el('div', { cls: 'config-row' });
      const pollEnabledLbl = el('label', { cls: 'config-label', text: I18n.t('server.poll-enabled'), for: 'cfg-server-poll-enabled' });
      const pollEnabledChk = el('input', { type: 'checkbox', id: 'cfg-server-poll-enabled' });
      pollEnabledRow.appendChild(pollEnabledLbl);
      pollEnabledRow.appendChild(pollEnabledChk);
      advDetails.appendChild(pollEnabledRow);

      /* Polling-Intervall */
      const intervalRow = el('div', { cls: 'config-row' });
      const intervalLbl = el('label', { cls: 'config-label', text: I18n.t('server.poll-interval'), for: 'cfg-server-interval' });
      const intervalInp = el('input', { type: 'number', cls: 'config-select', id: 'cfg-server-interval', min: '5', max: '60', step: '1' });
      intervalRow.appendChild(intervalLbl);
      intervalRow.appendChild(intervalInp);
      advDetails.appendChild(intervalRow);

      serverArea.appendChild(advDetails);
      /* #261: serverArea (= „Erweitert"-Sektion) wird NICHT hier eingehängt,
         sondern bewusst NACH der Templates-Sektion (vor der Gefahrenzone).
         Element-IDs sind position-unabhängig (querySelector im Overlay),
         daher kein Effekt auf refreshProjectArea/populateSelects. */
    }

    /* ── Anhänge-Limits (Partition: attachments) ── ADR-0033 ─────── */
    let attachArea = null;
    if (has('attachments')) {
      attachArea = el('div', { cls: 'config-attachments-area' });
      const attachSep  = el('div', { cls: 'config-separator', text: I18n.t('config.attachments-section') });
      attachArea.appendChild(attachSep);

      function addMBRow(labelKey, id, configKey, maxBytes) {
        const row = el('div', { cls: 'config-row' });
        const lbl = el('label', { cls: 'config-label', text: I18n.t(labelKey), for: id });
        const inp = el('input', { type: 'number', cls: 'config-select', id, min: '0.5', step: '0.5' });
        /* F-828: im Server-Build ist die Hartgrenze durch die PUT-Body-Grenze
           gedeckelt (Config.SERVER_ATTACHMENT_HARD_CAP_BYTES) — `max` ist hier
           UX (verhindert die Eingabe eines Werts, den `Config.set` ohnehin
           kappt), nicht die eigentliche Durchsetzung. */
        if (maxBytes) inp.setAttribute('max', String(maxBytes / (1024 * 1024)));
        inp.dataset.configKey = configKey;
        row.appendChild(lbl);
        row.appendChild(inp);
        attachArea.appendChild(row);
      }
      addMBRow('config.attachment-max',  'cfg-attachmentMax',  'attachmentMaxBytes');
      addMBRow('config.attachment-warn', 'cfg-attachmentWarn', 'attachmentBoardWarnBytes');
      addMBRow('config.attachment-hard', 'cfg-attachmentHard', 'attachmentBoardHardBytes',
        IS_SERVER_BUILD ? Config.SERVER_ATTACHMENT_HARD_CAP_BYTES : undefined);
      /* F-841: Warn-/Hart-Grenze rechnen gegen die KODIERTE (Base64-)Grösse,
         nicht gegen die Nutzdaten — die Labels sagen das jetzt selbst
         ("kodiert"/"encoded"), hier steht der Grund + Faktor dazu. Wieder-
         verwendete Hint-Klasse aus dem Hintergrundbild-Feld (rein optisch:
         kleine, gedämpfte Hinweiszeile — kein eigenes CSS nötig). */
      attachArea.appendChild(el('p', {
        cls: 'config-bg-hint', text: I18n.t('config.attachment-encoded-hint'),
      }));
      dialogBody.appendChild(attachArea);
    }

    /* ADR-0069: Attachment-Sichtbarkeit wird jetzt in refreshProjectArea() via
       isSectionRelevant('attachments', modeVal) gesteuert — kein separater Listener mehr.
       isSectionRelevant delegiert intern an Profile.has('feature.attachmentLimits') (F-269). */

    /* ── Board-Hintergrundbild (Partition: background) ──────────── */
    if (has('background')) {
      dialogBody.appendChild(el('div', { cls: 'config-separator', text: I18n.t('config.background-section') }));

      const bgRow = el('div', { cls: 'config-row' });
      const bgThumb = el('div', { cls: 'config-bg-thumb', id: 'cfg-bg-thumb' });
      const bgHint  = el('p',   { cls: 'config-bg-hint',  id: 'cfg-bg-hint', text: I18n.t('config.background-local-hint') });
      const bgPickBtn   = el('button', { cls: 'btn btn-sm', type: 'button', text: I18n.t('config.background-pick') });
      const bgRemoveBtn = el('button', { cls: 'btn btn-sm btn-danger', type: 'button', text: I18n.t('config.background-remove') });
      bgRemoveBtn.id = 'cfg-bg-remove';
      bgPickBtn.addEventListener('click',   () => pickBoardBackground());
      bgRemoveBtn.addEventListener('click', () => removeBoardBackground());
      const bgBtns = el('div', { cls: 'config-bg-buttons' });
      bgBtns.appendChild(bgPickBtn); bgBtns.appendChild(bgRemoveBtn);
      bgRow.appendChild(bgThumb); bgRow.appendChild(bgBtns);
      dialogBody.appendChild(bgRow);
      /* #727: Darstellung des Hintergrundbilds */
      const darRow = el('div', { cls: 'config-row', id: 'cfg-bg-dar-row' });
      const darLabel = el('label', { text: I18n.t('config.background-darstellung') });
      const darSel = document.createElement('select');
      darSel.id = 'cfg-bg-darstellung';
      darLabel.htmlFor = darSel.id;
      [['getoent', 'config.background-dar-getoent'], ['voll', 'config.background-dar-voll'], ['abgedunkelt', 'config.background-dar-dunkel']].forEach(([v, k]) => {
        const o = document.createElement('option'); o.value = v; o.textContent = I18n.t(k); darSel.appendChild(o);
      });
      darSel.addEventListener('change', () => {
        const b = State.get()?.board;
        if (!b?.background) return;
        b.background.darstellung = darSel.value;
        State.set(State.get());
        EventBus.emit('board:background-changed');
      });
      darRow.appendChild(darLabel); darRow.appendChild(darSel);
      dialogBody.appendChild(darRow);
      dialogBody.appendChild(bgHint);
    }

    /* ── Templates (Partition: templates) ── ADR-0040, Karte #65 ── */
    let templatesList = null;
    if (has('templates')) {
      const templatesSep = el('div', { cls: 'config-separator', text: I18n.t('template.section-title') });
      dialogBody.appendChild(templatesSep);

      templatesList = el('div', { cls: 'config-templates-list', id: 'cfg-templates-list' });
      dialogBody.appendChild(templatesList);

      _renderTemplatesList = function renderTemplatesListImpl() {
        while (templatesList.lastChild) templatesList.removeChild(templatesList.lastChild);
        const templates = State.get()?.board?.templates ?? [];
        if (templates.length === 0) {
          const empty = el('p', { cls: 'config-templates-empty', text: I18n.t('template.empty') });
          templatesList.appendChild(empty);
          return;
        }
        templates.forEach(tpl => {
          const row = el('div', { cls: 'config-template-row' });

          /* F-833: `.trim()` fehlte — ein Titel aus reinem Whitespace ist truthy
             und erzeugte sonst einen leeren Button statt „(ohne Titel)". Muster
             wie llm-pii.js:348 (bereits behoben, F-144). */
          const titleBtn = el('button', { cls: 'config-template-title btn-link', type: 'button',
                                           text: (tpl.title || '').trim() || '(ohne Titel)' });
          titleBtn.addEventListener('click', () => {
            /* Kein closeOverlay() — Modal öffnet sich über dem Config-Dialog */
            Modal.open({ ...tpl });
            /* Nach Modal-Schließen Liste neu aufbauen (template:update hat State bereits geändert) */
            const onClose = () => {
              if (_renderTemplatesList) _renderTemplatesList();
              EventBus.off('modal:closed', onClose);
            };
            EventBus.on('modal:closed', onClose);
          });

          const delBtn = el('button', { cls: 'btn btn-sm btn-danger config-template-delete', type: 'button' });
          delBtn.textContent = '×';
          delBtn.setAttribute('aria-label', I18n.t('template.delete'));
          delBtn.setAttribute('title', I18n.t('template.delete'));
          delBtn.addEventListener('click', () => {
            /* F-833: siehe Kommentar bei titleBtn oben — gleiche Whitespace-Lücke. */
            if (!confirm(I18n.t('template.delete-confirm', (tpl.title || '').trim() || '(ohne Titel)'))) return;
            EventBus.emit('template:delete', { templateId: tpl.id });
            if (_renderTemplatesList) _renderTemplatesList();
          });

          row.appendChild(titleBtn);
          row.appendChild(delBtn);
          templatesList.appendChild(row);
        });
      };
    }

    /* Karte #150: Identitäten-Section wurde nach ganz oben verschoben (Mount oberhalb von addRow theme). */

    /* Karte #152: Avatar-Preview inline neu zeichnen (statt voller _renderIdentitiesList).
       Hält den Fokus in Label-/Icon-/Color-Inputs bei jeder Eingabe. */
    function refreshAvatarPreview(previewSlot, idn) {
      const board = State.get()?.board;
      if (!board || !previewSlot) return;
      while (previewSlot.firstChild) previewSlot.removeChild(previewSlot.firstChild);
      const avatar = buildAvatar(idn, 'md', { board, trackUrl: _trackConfigUrl });
      if (avatar) previewSlot.appendChild(avatar);
    }

    _renderIdentitiesList = function renderIdentitiesListImpl() {
      if (!identitiesList) return; /* Partition 'board' hat keine Identitäten-Liste */
      /* F-210: Blob-URLs der vorherigen Render-Generation freigeben */
      _revokeConfigUrls();
      while (identitiesList.lastChild) identitiesList.removeChild(identitiesList.lastChild);
      const board = State.get()?.board;
      if (!board) return;
      if (!Array.isArray(board.identities)) board.identities = [];
      /* Karte #171: Nur User-Identitäten anzeigen. Claude-Identitäten sind statisch
         (CLAUDE_DEFAULTS) und werden in resolveIdentity on-the-fly synthetisiert. */
      const userIdentities = board.identities.filter(i => i.kind === 'user');
      userIdentities.forEach((idn, idx) => {
        const row = el('div', { cls: 'config-identity-row' });

        /* Avatar-Vorschau (32 px) */
        const previewSlot = el('div', { cls: 'config-identity-preview' });
        const avatar = buildAvatar(idn, 'md', { board, trackUrl: _trackConfigUrl });
        if (avatar) previewSlot.appendChild(avatar);
        row.appendChild(previewSlot);

        /* Felder-Block */
        const fields = el('div', { cls: 'config-identity-fields' });

        /* Kind-Label (read-only) */
        const kindLbl = el('div', { cls: 'config-identity-kind',
                                     text: I18n.t(`identity.kind.${idn.kind}`) });
        fields.appendChild(kindLbl);

        /* Label-Input (Anzeigename) — ADR-0080: im Server-Modus für user-default
           ausblenden, da der Anzeigename Forward-Auth-verwaltet und lokal
           wirkungslos ist. Farbe/Icon-Zeilen folgen unverändert. Nicht-user-
           default-Identitäten sowie alle Nicht-Server-Modi: Label stets zeigen. */
        const hideLabel = isServerModeUser && idn.id === 'user-default';
        if (!hideLabel) {
          const labelRow = el('div', { cls: 'config-identity-field' });
          const labelInpId = `cfg-identity-label-${idx}`;
          labelRow.appendChild(el('label', { cls: 'config-label', text: I18n.t('identity.label-input'),
                                              for: labelInpId }));
          const labelInp = el('input', { type: 'text', cls: 'config-select', id: labelInpId });
          labelInp.value = idn.label || '';
          labelInp.addEventListener('input', () => {
            idn.label = labelInp.value;
            /* Karte #150: User-Identity-Label setzt Config.userName direkt */
            if (idn.id === 'user-default') {
              Config.set('userName', labelInp.value);
            }
            State.set({ ...State.get() });
            /* Karte #152: Avatar inline neu zeichnen statt _renderIdentitiesList — Fokus bleibt */
            refreshAvatarPreview(previewSlot, idn);
          });
          labelRow.appendChild(labelInp);
          fields.appendChild(labelRow);
        }

        /* E-Mail-Feld (ADR-0108, Karte #339) — die Brücke von dieser Identity
           zum Server-Konto. Ohne sie bekommt der Träger keine
           Benachrichtigungen; das ist ein normaler Zustand und kein Fehler,
           deshalb steht es als optionales Feld da und nicht als Pflicht.

           Doppelt gegatet, und beide Gates haben verschiedene Aufgaben:
           `IS_SERVER_BUILD` wirft den Block im Slim- und claude-code-Bundle
           per DCE ganz heraus (dort gibt es keinen Server, also auch keine
           Empfänger — ohne dieses Gate trüge das Slim-Template rund 1,3 KB
           für ein Feld, das dort nie erscheinen kann). `isServerModeUser`
           entscheidet zur Laufzeit im Server-Build, ob dieser Nutzer gerade
           in einem Server-Board arbeitet. */
        if (IS_SERVER_BUILD && isServerModeUser) {
          const mailRow = el('div', { cls: 'config-identity-field' });
          const mailInpId = `cfg-identity-email-${idx}`;
          mailRow.appendChild(el('label', { cls: 'config-label', text: I18n.t('identity.email-input'),
                                             for: mailInpId }));
          const mailInp = el('input', { type: 'email', cls: 'config-select', id: mailInpId });
          mailInp.value = idn.email || '';
          mailInp.setAttribute('placeholder', I18n.t('identity.email-placeholder'));
          mailInp.setAttribute('aria-describedby', `${mailInpId}-hint`);
          mailInp.addEventListener('input', () => {
            idn.email = mailInp.value.trim();
            State.set({ ...State.get() });
          });
          mailRow.appendChild(mailInp);
          mailRow.appendChild(el('div', { cls: 'config-bg-hint', id: `${mailInpId}-hint`,
                                           text: I18n.t('identity.email-hint') }));
          fields.appendChild(mailRow);
        }

        /* Farb-Picker */
        const colorRow = el('div', { cls: 'config-identity-field' });
        const colorInpId = `cfg-identity-color-${idx}`;
        colorRow.appendChild(el('label', { cls: 'config-label', text: I18n.t('identity.color-picker'),
                                            for: colorInpId }));
        const colorInp = el('input', { type: 'color', cls: 'col-color-input', id: colorInpId });
        /* HEX-Validierung: <input type="color"> akzeptiert nur #rrggbb */
        const hexLike = typeof idn.color === 'string' && /^#[0-9a-f]{6}$/i.test(idn.color);
        colorInp.value = hexLike ? idn.color : '#0B5D73';
        colorInp.addEventListener('input', () => {
          idn.color = colorInp.value;
          State.set({ ...State.get() });
          /* Karte #152: Avatar inline neu zeichnen */
          refreshAvatarPreview(previewSlot, idn);
        });
        colorRow.appendChild(colorInp);
        fields.appendChild(colorRow);

        /* Icon-Input (max 2 Zeichen — Emoji oder Buchstabe) */
        const iconRow = el('div', { cls: 'config-identity-field' });
        const iconInpId = `cfg-identity-icon-${idx}`;
        iconRow.appendChild(el('label', { cls: 'config-label', text: I18n.t('identity.icon-input'),
                                            for: iconInpId }));
        const iconInp = el('input', { type: 'text', cls: 'config-select', id: iconInpId,
                                       maxlength: '2' });
        iconInp.value = idn.icon || '';
        iconInp.addEventListener('input', () => {
          idn.icon = iconInp.value || null;
          State.set({ ...State.get() });
          /* Karte #152: Avatar inline neu zeichnen */
          refreshAvatarPreview(previewSlot, idn);
        });
        iconRow.appendChild(iconInp);
        fields.appendChild(iconRow);

        row.appendChild(fields);

        /* Aktionen */
        const actions = el('div', { cls: 'config-identity-actions' });
        const uploadBtn = el('button', { cls: 'btn btn-sm', type: 'button',
                                          text: I18n.t('identity.image-upload') });
        uploadBtn.addEventListener('click', () => pickIdentityImage(idn));
        actions.appendChild(uploadBtn);

        if (idn.imageAttachmentId) {
          const removeBtn = el('button', { cls: 'btn btn-sm btn-danger', type: 'button',
                                            text: I18n.t('identity.image-remove') });
          removeBtn.addEventListener('click', () => removeIdentityImage(idn));
          actions.appendChild(removeBtn);
        }

        const resetBtn = el('button', { cls: 'btn btn-sm', type: 'button',
                                         text: I18n.t('identity.reset') });
        resetBtn.addEventListener('click', () => resetIdentity(idn));
        actions.appendChild(resetBtn);

        row.appendChild(actions);
        identitiesList.appendChild(row);
      });
    };

    /* #261: „Erweitert"-Sektion (Server-URL/Polling) ans Ende — nach
       Templates, vor der Gefahrenzone (die im Server-Modus ohnehin
       versteckt ist → „Erweitert" ist dort effektiv die letzte Sektion). */
    if (serverArea) dialogBody.appendChild(serverArea);

    /* ── Backup & Wiederherstellung (Partition: backup) ────────── */
    /* Karte #335: Board-Eigentümer kann sein Board hier sichern und
       zurückspielen. `has('backup')` ist nur im Server-Build gesetzt
       (settings-scope.js) — im slim- und claude-code-Bundle ist der ganze
       Zweig samt Modul-Import tot und wird von der DCE entfernt.

       Die Sektion hängt sich SOFORT ein (verborgen) und entscheidet
       asynchron über ihre Sichtbarkeit; ein synchroner Owner-Check gäbe es
       nicht, weil dafür die Board-ACL vom Server geholt werden muss. */
    if (IS_SERVER_BUILD && has('backup')
        && isSectionRelevant('backup', Config.get('boardMode'))) {
      const boardId = State.get()?.board?.id;
      if (boardId) {
        const backup = buildBackupSection(boardId);
        dialogBody.appendChild(backup.el);
        _backupAktualisieren = backup.aktualisieren;
      }
    }

    /* ── Gefahrenzone (Partition: danger) ─────────────────────── */
    /* Karte #207 / F-265: Im Server-Modus komplett ausblenden — Reset würde das
       geteilte Board für ALLE Mitglieder löschen. dangerContainer fasst
       Trenner + Zeile zusammen für einfaches hide/show. */
    let dangerContainer = null;
    if (has('danger')) {
      dangerContainer = el('div', { cls: 'config-danger-container' });
      const dangerSep = el('div', { cls: 'config-separator', text: I18n.t('config.danger-section') });
      dangerContainer.appendChild(dangerSep);

      const resetRow = el('div', { cls: 'config-row' });
      const resetBtn = el('button', { cls: 'btn btn-sm btn-danger', text: I18n.t('config.reset-board'), type: 'button' });
      resetBtn.addEventListener('click', () => {
        const confirmText = I18n.t('config.reset-confirm')
          + (State.isDirty() ? I18n.t('dirty.reset-confirm-suffix') : '');
        if (!confirm(confirmText)) return;
        closeOverlay(partition);
        EventBus.emit('board:reset');
      });
      resetRow.appendChild(el('div'));
      resetRow.appendChild(resetBtn);
      dangerContainer.appendChild(resetRow);
      dialogBody.appendChild(dangerContainer);
    }

    /* Kein `dialog.appendChild(dialogBody)` mehr nötig — `modal.body` ist
       bereits Teil des Panels (createModal() oben). */

    function refreshProjectArea() {
      const modeVal = modeSel?.value;
      const isServer = Profile.has('feature.restStorage', modeVal);
      /* ADR-0069 §6: Modus-Relevanz-Schicht — isSectionRelevant() aus settings-scope.js.
         Orthogonal zu serverScope: blendet modus-irrelevante Sektionen komplett aus,
         bevor serverScope-Filterung greift. F-269: Profile.has() ist die Single Source. */
      /* Project-Bereich: die volle Sektion (Status + Deaktivieren) ist nur für
         claude-code relevant (hat polling, kein restStorage).
         F-427/F-318: Der Ordner-Button ist zugleich der EINZIGE
         `sync:pick-directory`-Emitter und damit der einzige Weg von solo/team
         nach claude-code (der Modus-Select ist per F-83 disabled). Hing er an
         der Sektions-Relevanz, landete jedes frisch ausgerollte slim-Template
         in einer Sackgasse ohne UI-Ausweg. Der Aktivierungs-Einstieg ist
         deshalb von der Sektions-Relevanz entkoppelt und überall sichtbar, wo
         FSA überhaupt sinnvoll ist — also in allen Modi ausser Server
         (restStorage: Wahrheit liegt im Go-Server, Ordner-Wahl bedeutungslos). */
      /* F-431: In solo/team nur zeigen, wenn FSA überhaupt existiert. Ohne
         `showDirectoryPicker` (Firefox, Safari) liefert `Sync.pickDirectory`
         null und der Handler steigt still aus — der Knopf wäre im Erstkontakt
         eines ADR-0003-Doppelklick-Nutzers ein toter Knopf ohne Rückmeldung.
         In claude-code bleibt die Sektion unabhängig davon sichtbar (Status). */
      const projectRelevant = isSectionRelevant('project', modeVal);
      const canActivate     = !isServer && 'showDirectoryPicker' in window;
      if (projectArea)  projectArea.style.display  = (projectRelevant || canActivate) ? '' : 'none';
      if (statusRow)    statusRow.style.display    = projectRelevant ? '' : 'none';
      if (disableRow)   disableRow.style.display   = projectRelevant ? '' : 'none';
      /* Server-Bereich: sichtbar nur im Server-Modus — und nur wenn serverArea gebaut wurde
         (IS_SERVER_BUILD=false → serverArea ist null → kein Zugriff). */
      if (serverArea) serverArea.style.display = isSectionRelevant('server', modeVal) ? '' : 'none';
      /* Attachment-Limits: sichtbar nur für Solo + Team */
      if (attachArea) attachArea.style.display = isSectionRelevant('attachments', modeVal) ? '' : 'none';
    /* Karte #290: Chat-Tail-Sektion nur in claude-code (IS_PROJECTOR_BUILD
       garantiert zugleich, dass chatTailArea im slim-Build nie existiert). */
    if (chatTailArea) chatTailArea.style.display = isSectionRelevant('chat-tail', modeVal) ? '' : 'none';
      if (statusVal) {
        if (!isServer || !IS_SERVER_BUILD) {
          const name     = Sync.getDirectoryName();
          const active   = Sync.isActive();
          const needPerm = Sync.isPermissionNeeded();
          statusVal.textContent = active
            ? `${name} · ${I18n.t('sync.polling')}`
            : needPerm
              ? I18n.t('sync.permission-needed')
              : I18n.t('sync.no-dir');
        } else {
          statusVal.textContent = I18n.t('server.poll-active');
        }
      }
      /* ADR-0063: UI-Sichtbarkeit aus SettingsScope-Klassifikation ableiten.
         ADR-0059-Invariante: im Standalone-/Solo-Modus gibt isServerHidden() immer false zurück. */
      /* Mode-Switch: ui.board-mode → dangerous → im Server-Modus verstecken */
      if (modeContainer) modeContainer.style.display = SettingsScope.isServerHidden('ui.board-mode', isServer) ? 'none' : '';
      /* Gefahrenzone (Reset): ADR-0071 Achse 3 — nur im Single-File-Modell
         (Solo/Team) sinnvoll. Modus-Relevanz-Schicht (isSectionRelevant
         'danger') subsumiert das alte Server-Hide (F-265) UND blendet den
         Knopf zusätzlich in claude-code aus (Wahrheit = aboard-sync.*). */
      if (dangerContainer) dangerContainer.style.display = isSectionRelevant('danger', modeVal) ? '' : 'none';
      /* KI-Kontext-Button: im Server-Modus (Karte #222) ODER im claude-code-
         Projektor-Build (Karte #248) verstecken — siehe kiContextHidden().
         TDZ-Sicherheit: kiKontextBtn ist const und wird nach refreshProjectArea deklariert,
         aber diese Funktion wird erst nach vollständigem buildOverlay-Aufbau aufgerufen
         (via modeSel.dispatchEvent in populateSelects oder manuellem modeSel-change). */
      if (kiKontextBtn) kiKontextBtn.style.display = kiContextHidden(isServer) ? 'none' : '';
    }

    /* Karte #207: refreshProjectArea auch als change-Listener registrieren,
       damit Server-Modus-Filterung bei modeSel-Wechsel (z. B. via dispatchEvent)
       sofort greift. In der App-Nutzung ist modeSel disabled, aber für Tests
       und populateSelects() ist das Event entscheidend. */
    if (modeSel) {
      modeSel.addEventListener('change', refreshProjectArea);
      /* Select ist rein informativ — Modus-Wechsel über Buttons unten (F-83) */
      modeSel.disabled = true;
      modeSel.title = I18n.t('config.mode-readonly');
    }

    /* Aktionen — Footer-Container kommt jetzt aus der Shell (`footer:true`),
       dieselbe `.modal-actions`-Klasse wie zuvor von Hand gebaut. */
    const actions = modal.footer;
    const cancelBtn  = el('button', { cls: 'btn',             text: I18n.t('btn.cancel'),    type: 'button' });
    const saveBtn    = el('button', { cls: 'btn btn-primary', text: I18n.t('btn.save'),      type: 'button' });
    const kiKontextBtn = el('button', { cls: 'btn modal-actions-left', text: I18n.t('btn.ki-kontext'), type: 'button' });
    kiKontextBtn.title = I18n.t('btn.ki-kontext');
    kiKontextBtn.setAttribute('aria-label', I18n.t('btn.ki-kontext'));
    kiKontextBtn.addEventListener('click', () => {
      closeOverlay(partition);
      EventBus.emit('board:export-ki');
    });
    /* Karte #222/#248: KI-Kontext-Button im Server-Modus ODER im claude-code-
       Projektor-Build verstecken (kiContextHidden(); ADR-0063/ADR-0067).
       Sichtbarkeit wird hier direkt gesetzt — deckt BEIDE Partitionen (user + board) ab,
       da refreshProjectArea() nur in der board-Partition via modeSel-change aufgerufen wird.
       Profile.has() ohne Override: liefert den aktuellen Modus-Zustand beim Dialog-Aufbau.
       Für die board-Partition wird die Sichtbarkeit zusätzlich in refreshProjectArea()
       nachgezogen, damit sie bei modeSel-Wechseln (z. B. Solo→Server→Solo) korrekt folgt. */
    kiKontextBtn.style.display = kiContextHidden(Profile.has('feature.restStorage')) ? 'none' : '';
    const right = el('div', { cls: 'modal-actions-right' });
    right.appendChild(cancelBtn);
    right.appendChild(saveBtn);
    actions.appendChild(kiKontextBtn);
    actions.appendChild(right);

    /* ESC/Klick-außerhalb/Tab-Trap laufen jetzt über die Shell
       (onKey→onRequestClose=cancelFn, siehe createModal oben) — kein
       eigener keydown-/click-Handler mehr nötig (ADR-0118 §1/§3). */
    cancelBtn.addEventListener('click', cancelFn);
    saveBtn.addEventListener('click',   () => save(partition));

    return modal;
  }

  function populateSelects(ov) {
    /* ADR-0065: Defensive Zugriffe — nicht jede Sektion ist in jedem Dialog vorhanden */
    /* Karte #150: #cfg-userName-Feld entfernt — Identity-Liste deckt es ab */
    for (const k of ['theme', 'density', ...DARSTELLUNG, 'sortierung', 'language']) {
      const sel = ov.querySelector(`#cfg-${k}`);
      if (sel) sel.value = Config.get(k);
    }
    const modeSel = ov.querySelector('#cfg-boardMode');
    if (modeSel) {
      modeSel.value = Config.get('boardMode');
      /* refreshProjectArea aufrufen */
      modeSel.dispatchEvent(new Event('change'));
    }
    /* Server-Felder befüllen */
    const urlInp         = ov.querySelector('#cfg-server-url');
    const intervalInp    = ov.querySelector('#cfg-server-interval');
    const pollEnabledChk = ov.querySelector('#cfg-server-poll-enabled');
    if (urlInp)         urlInp.value         = Config.get('serverUrl') ?? '';
    if (intervalInp)    intervalInp.value    = Config.get('serverPollInterval') ?? 10;
    if (pollEnabledChk) pollEnabledChk.checked = Config.get('serverPollEnabled') !== false;
    /* Karte #290 (Chat-Tail): Werte aus Config übernehmen. IS_PROJECTOR_BUILD-
       gegated → im slim per DCE entfernt. */
    if (IS_PROJECTOR_BUILD) {
      const ctEnChk = ov.querySelector('#cfg-chat-tail-enabled');
      const ctLinesInp = ov.querySelector('#cfg-chat-tail-lines');
      if (ctEnChk)    ctEnChk.checked  = !!Config.get('chatTailEnabled');
      if (ctLinesInp) ctLinesInp.value = Config.get('chatTailLines') ?? 20;
    }
    /* ADR-0033: Bytes → MB für die Anzeige */
    ov.querySelectorAll('input[data-config-key]').forEach(inp => {
      const key = inp.dataset.configKey;
      const bytes = Config.get(key);
      if (typeof bytes === 'number') inp.value = (bytes / (1024 * 1024)).toFixed(1);
    });
    /* ADR-0040: Templates-Liste aktualisieren */
    if (_renderTemplatesList) _renderTemplatesList();
    /* Karte #123: Identitäten-Liste aktualisieren */
    if (_renderIdentitiesList) _renderIdentitiesList();
  }

  /* ── Öffnen / Schließen (ADR-0065: parametrisiert nach Partition) ── */

  /* ADR-0065: openPartition(partition) öffnet genau einen der zwei Dialoge.
     'user' → Benutzer-Einstellungen; 'board' → Board-Einstellungen. */
  function openPartition(partition) {
    snapshot = {
      theme:    Config.get('theme'),
      density:  Config.get('density'),
      ...Object.fromEntries(DARSTELLUNG.map(k => [k, Config.get(k)])),
      sortierung: Config.get('sortierung'),
      language: Config.get('language'),
    };

    let modal = partition === 'user' ? modalUser : modalBoard;
    if (!modal) {
      modal = buildOverlay(partition);
      document.body.appendChild(modal.overlay);
      if (partition === 'user') modalUser = modal;
      else modalBoard = modal;
    }
    overlay = modal.overlay; /* Rückwärts-Kompatibilität: readSelects() nutzt overlay */

    populateSelects(modal.overlay);
    refreshBgPreview();
    /* Backup-Liste bei jedem Öffnen nachladen (siehe _backupAktualisieren).
       Nur im Server-Build gesetzt; sonst null und der Aufruf entfällt. */
    if (_backupAktualisieren) _backupAktualisieren();
    /* KO-12b: Freigabe gehört nur zur board-Partition — die Benutzer-Einstellungen
       lösen keinen Abruf aus. */
    if (partition === 'board' && _shareAktualisieren) _shareAktualisieren();
    /* Karte #703: KI-Anbieter gehört nur zur user-Partition. */
    if (partition === 'user' && _llmAktualisieren) _llmAktualisieren();

    /* Sichtbarkeit + Fokus-Capture/-Trap/-Restore: jetzt Shell-Aufgabe
       (ADR-0118 §1). `modal.open()` setzt `--open` synchron per erzwungenem
       Reflow (F-754/F-756/F-808 — dieselbe Begründung wie zuvor hier direkt
       im Modul, jetzt einmal in modal-shell.js statt in jedem Konsumenten
       einzeln). `focusAfterRender()` läuft danach, weil der Fokus-Algorithmus
       ein Element in einem noch `visibility:hidden`-Vorfahren nicht
       akzeptiert (siehe modal-shell.js#open — derselbe Grund, der vorher
       hier stand). Anders als vorher NICHT hart auf `.btn-primary`: Die
       Shell-Reihenfolge (erstes Input, sonst erster Button — siehe
       focus-trap.js#focusInitial) ist dieselbe, die alle sieben bereits
       migrierten Dialoge verwenden. Die Identitäten-Sektion hat i.d.R. ein
       Eingabefeld, das dann zuerst den Fokus bekommt — kein Test hing am
       alten `.btn-primary`-Ziel (geprüft). */
    modal.open();
    modal.focusAfterRender();

    /* Swipe-to-Dismiss (F-95) — einmalig registrieren */
    if (!modal.overlay.dataset.swipe) {
      modal.overlay.dataset.swipe = '1';
      addSwipeToDismiss(modal.panel, () => cancel(partition));
    }
  }

  /* Rückwärts-Kompatibilität: EventBus 'config:open' öffnet Board-Dialog */
  function open() { openPartition('board'); }

  function save(partition) {
    const values = readSelects();
    /* F-282: undefined-Felder überspringen — readMB() gibt undefined zurück wenn das
       Element im aktuellen Dialog fehlt (z. B. Attachment-Felder im Benutzer-Dialog).
       Verhindert, dass board-partitionierte Werte zurückgeschrieben werden. */
    Object.entries(values).forEach(([k, v]) => { if (v !== undefined) Config.set(k, v); });

    /* Karte #1004: Sortierung ändert die Anordnung → Board neu zeichnen */
    if (values.sortierung && values.sortierung !== snapshot.sortierung) EventBus.emit('filter:changed');

    if (values.language && values.language !== snapshot.language) {
      I18n.setLang(values.language);
      EventBus.emit('config:language-changed');
    }

    /* Karte #290: Chat-Tail-Toggle/Lines wirken erst durch FSA-Marker-Write.
       IS_PROJECTOR_BUILD-gegated → DCE im slim. */
    if (IS_PROJECTOR_BUILD && values.chatTailEnabled !== undefined) {
      Sync.setChatTailEnabled(
        values.chatTailEnabled,
        values.chatTailLines ?? Config.get('chatTailLines'),
      ).catch(() => {});
    }

    closeOverlay(partition);
  }

  function cancel(partition) {
    /* Live-Vorschau rückgängig machen */
    if (snapshot) previewValues(snapshot);
    closeOverlay(partition);
  }

  function closeOverlay(partition) {
    const modal = partition === 'user' ? modalUser : modalBoard;
    if (!modal) return;
    /* F-107: Inline-Style aus Swipe-Dismiss entfernen — sonst bleibt translateY(100%) erhalten */
    modal.panel.style.transform = '';
    /* Sichtbarkeit + Fokus-Restore: jetzt Shell-Aufgabe (modal.close(),
       ADR-0118 §1) — der eigene `prevFocus`-Mechanismus entfällt komplett
       (captureFocus()/restoreFocus() aus focus-trap.js, wie bei den sieben
       bereits migrierten Dialogen). */
    modal.close();
    snapshot = null;
    /* Objekt-URLs des Dialogs freigeben — sonst bleibt pro Öffnen eine liegen
       (Frontend-Kritiker Sprint 61). Beim nächsten Öffnen baut
       `refreshBgPreview`/`_renderIdentitiesList` sie ohnehin neu.
       [[F-878]] (Karte #459, behoben): partitionsscharf statt unconditional
       für beide Partitionen. `_revokeBgThumbUrl()` gehört zur `background`-
       Sektion (nur `board`-Partition, siehe SECTION_DIALOG_MAP), `_revokeConfigUrls()`
       zur `identities`-Sektion (nur `user`-Partition — Avatare/Hintergrund-
       Vorschau erzeugen ihre Blob-URLs ausschliesslich in ihrer jeweiligen
       Sektion, siehe refreshBgPreview() bzw. buildAvatar()-Aufrufe in
       _renderIdentitiesList()/refreshAvatarPreview()). Bis hierher liefen
       beide Aufrufe für beide Partitionen — heute folgenlos, weil beide
       Dialoge modal sind und nie gleichzeitig offen; ein künftiger
       nicht-modaler Pfad hätte die Kopplung sonst geerbt, ohne dass jemand
       sie beim Schliessen des jeweils anderen Dialogs vermutet hätte.

       Die Fallunterscheidung spiegelt bewusst `openPartition` („'user' oder
       sonst board") statt zweimal auf Gleichheit zu prüfen: Sonst öffnete ein
       unerwarteter Wert den board-Dialog, gäbe beim Schliessen aber gar nichts
       frei — ein stiller Leck-Pfad genau in der Funktion, die gerade gegen
       einen solchen abgesichert wird. */
    if (partition === 'user') _revokeConfigUrls();
    else                      _revokeBgThumbUrl();
  }

  /* EventBus-Events: 'config:open' (Rückwärts-Kompatibilität, Board-Dialog)
     'config:open-board' → Board-Einstellungen
     'config:open-user'  → Benutzer-Einstellungen */
  EventBus.on('config:open',       () => openPartition('board'));
  EventBus.on('config:open-board', () => openPartition('board'));
  EventBus.on('config:open-user',  () => openPartition('user'));

  EventBus.on('config:language-changed', () => {
    /* Overlays neu bauen bei Sprachwechsel — Texte sonst veraltet.
       `modal.close()` VOR dem Entfernen: war der Dialog gerade offen, hängt
       sonst noch der Shell-eigene `document`-Keydown-Listener (siehe
       modal-shell.js#open) an der verworfenen Closure — ein Leck, das es im
       handgerollten Vorgänger nicht gab (der Listener sass direkt am
       `dialog`-Element und wurde mit dessen Entfernung automatisch mit
       GC'd). close() ist auch im bereits geschlossenen Zustand harmlos. */
    /* Objekt-URLs freigeben, BEVOR die Instanzen verworfen werden
       (Sprint-62-Kritiker, V3). `closeOverlay` ist seit Karte #459 der
       einzige Ort, an dem revoked wird — und er steigt bei `if (!modal)
       return;` sofort aus. Hier setzt der Handler `modalUser`/`modalBoard`
       auf `null`, also findet ein anschliessendes `closeOverlay` nichts mehr
       vor: Wer den Benutzer-Dialog mit Avataren offen hat, die Sprache
       umstellt und speichert, hinterlässt eine `blob:`-URL pro Bild für die
       Lebensdauer der Seite. Über die normale UI erreichbar, nicht latent. */
    _revokeConfigUrls();
    _revokeBgThumbUrl();
    if (modalUser)  { modalUser.close();  modalUser.overlay.remove();  modalUser  = null; }
    if (modalBoard) { modalBoard.close(); modalBoard.overlay.remove(); modalBoard = null; }
    overlay = null;
    _renderTemplatesList = null;
    _renderIdentitiesList = null;
    _backupAktualisieren = null;
    _shareAktualisieren = null;
    _llmAktualisieren = null;
  });

  EventBus.on('config:project-changed', () => {
    /* Board-Overlay neu bauen damit Projekt-Status aktuell ist */
    if (modalBoard) {
      /* Nur die `board`-Partition wird hier verworfen — also auch nur deren
         Objekt-URL freigeben (V3, dieselbe Lücke wie beim Sprachwechsel; die
         Partitionsschärfe stammt aus Karte #459/F-878). */
      _revokeBgThumbUrl();
      modalBoard.close(); /* siehe Begründung bei config:language-changed oben */
      modalBoard.overlay.remove();
      if (overlay === modalBoard.overlay) overlay = null;
      modalBoard = null;
      /* Board gewechselt → die alte Sektion gehörte zu einem anderen Board.
         Ohne dieses Zurücksetzen würde beim nächsten Öffnen die Liste des
         VORIGEN Boards nachgeladen. */
      _backupAktualisieren = null;
      _shareAktualisieren = null;
    }
  });

  return { open, openPartition };
})();
