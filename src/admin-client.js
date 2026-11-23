/* admin-client.js — REST-Wrapper für /admin/*-Endpoints (#306, ADR-0101).
 *
 * Wird ausschließlich vom admin-dialog.js verwendet. Beide Module sind hinter
 * IS_SERVER_BUILD gegated → im Slim-/Claude-Code-Build per DCE eliminiert.
 *
 * Konventionen (analog storage-plugin.js):
 *   - Same-Origin-Requests; Auth läuft über die Caddy-Forward-Auth-Cookies.
 *   - Fehler-Antworten werfen NIE — Caller bekommt strukturiertes Ergebnis
 *     (ok, status, body) und entscheidet selbst über UI-Reaktion.
 */

import { Sitzung } from './sitzung-abgelaufen.js'; /* F-2830, null außerhalb des Server-Builds */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Modul-Inhalt nur im Server-Build vorhanden. Im Slim/Claude-Code-Build
   ist `AdminClient` null und alle Import-Sites laufen ihrerseits hinter
   IS_SERVER_BUILD-Gates (DCE'd). */
let AdminClient = null;

if (IS_SERVER_BUILD) {
  /* baseUrl wird zentral in storage-plugin gepflegt. Wir lesen denselben
     Wert aus der Profile-Konfig, damit Test-Server (httptest.URL) und
     Production identisch funktionieren. */
  const baseUrl = () => {
    /* Storage-Plugin liefert window.location.origin als Default. Hier
       reicht same-origin — der Server hostet Frontend + /admin/* selbst. */
    return '';
  };

  async function getJSON(path) {
    try {
      const res = await fetch(`${baseUrl()}${path}`);
      Sitzung?.meldeAntwort(res.status); /* F-2830 */
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  async function sendJSON(method, path, payload) {
    try {
      const res = await fetch(`${baseUrl()}${path}`, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: payload === undefined ? null : JSON.stringify(payload),
      });
      Sitzung?.meldeAntwort(res.status); /* F-2830 */
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  AdminClient = {
    /* GET /admin/config[/{section}] → { entries: [...], secrets_enabled } */
    listConfig(section) {
      const path = section ? `/admin/config/${encodeURIComponent(section)}` : '/admin/config';
      return getJSON(path);
    },
    /* PUT /admin/config/{key} { value, is_secret } */
    setConfig(key, value, isSecret) {
      return sendJSON('PUT', `/admin/config/${encodeURIComponent(key)}`, {
        value, is_secret: !!isSecret,
      });
    },
    /* DELETE /admin/config/{key} */
    deleteConfig(key) {
      return sendJSON('DELETE', `/admin/config/${encodeURIComponent(key)}`, undefined);
    },
    /* GET /admin/status */
    status() { return getJSON('/admin/status'); },
    /* GET /admin/audit?since=&limit= */
    audit({ since = '', limit = 100 } = {}) {
      const qs = new URLSearchParams();
      if (since) qs.set('since', since);
      if (limit) qs.set('limit', String(limit));
      const path = `/admin/audit${qs.toString() ? '?' + qs.toString() : ''}`;
      return getJSON(path);
    },
    /* POST /admin/config/{section}/test */
    testSection(section) {
      return sendJSON('POST', `/admin/config/${encodeURIComponent(section)}/test`, {});
    },
    /* Karte #735: Sicherungslauf über ALLE Boards (nur Admin).
       POST /admin/backup/all-now → 202 { started } | 409 (läuft schon) |
       403 | 503. Der Lauf ist asynchron; Fortschritt per
       GET /admin/backup/all-status → { running, last: { total, ok, failed,
       error? } | null }. */
    startAllBackup() { return sendJSON('POST', '/admin/backup/all-now'); },
    allBackupStatus() { return getJSON('/admin/backup/all-status'); },
    /* POST /admin/config/reload — ohne Argument ALLE Reload-Hooks; mit `section`
       nur der dieser Sektion (?section=…, O-3: unbekannt → 400). */
    reload(section) {
      return sendJSON('POST', '/admin/config/reload' + (section ? `?section=${encodeURIComponent(section)}` : ''), {});
    },
    /* PATCH /admin/users/{id}/admin { is_admin } */
    setAdminFlag(userId, isAdmin) {
      return sendJSON('PATCH', `/admin/users/${encodeURIComponent(userId)}/admin`, {
        is_admin: !!isAdmin,
      });
    },
    /* GET /admin/users → { users: [{id, email, name, is_admin, created,
       boards_owner, boards_einzel_owner}, …] } (Karte #575) */
    listUsers() { return getJSON('/admin/users'); },
    /* Karte #1078 (ADR-0149 §6): Konten, Einladungen. 404 = keine eingebaute
       Anmeldung (der Reiter blendet den Teil dann aus). */
    listKonten() { return getJSON('/admin/konten'); },
    /* POST /admin/einladungen { emails } → { ergebnisse: [{email, konto_id, link,
       ablauf, erneut, mail, fehler}] } — der Link steht NUR in dieser Antwort. */
    einladen(emails) { return sendJSON('POST', '/admin/einladungen', { emails }); },
    /* DELETE /admin/einladungen/{id} → 204 | 404 | 409 (schon eingelöst) */
    widerrufeEinladung(id) { return sendJSON('DELETE', `/admin/einladungen/${encodeURIComponent(id)}`, undefined); },
    /* POST /admin/konten/{id}/{bestaetigen|ablehnen|sperren|entsperren|zuruecksetzen} */
    kontoAktion(id, aktion) {
      return sendJSON('POST', `/admin/konten/${encodeURIComponent(id)}/${encodeURIComponent(aktion)}`, undefined);
    },
    /* DELETE /admin/users/{id} { uebertragen? } (Karte #575, Folge zu #571).
       uebertragen ist optional — leer/undefined → kein Body, wie bisher.
       Bei 409 „einziger Owner" liefert der Server die Board-Liste im Body
       (`{error, boards}`); sendJSON parst den Body UNABHÄNGIG vom Status
       (s. o.), der Aufrufer bekommt `res.body.boards` also ohnehin durch —
       kein Extra-Handling hier nötig. */
    /* Karte #608 (ADR-0130 §6): Agenten-Zugänge. Die Liste trägt nie ein
       Token, nur Name, Board, Person, Kennung und Ablauf. */
    listAgentTokens() { return getJSON('/admin/agent-tokens'); },
    /* Karte #670 (ADR-0133 §6): Projekt-Zugang anlegen — { name, board, role,
       gueltig_tage } → { token, agent }. Der Klartext steht NUR in dieser
       Antwort. */
    createAgentToken(body) { return sendJSON('POST', '/admin/agent-tokens', body); },
    /* Seit Server-Nacharbeit A (F-1434): der Pfad nimmt bevorzugt die Token-id,
       die Namensform bleibt gültig (bei mehreren aktiven Tokens gleichen
       Namens antwortet der Server 409). Der Aufrufer gibt `t.id || t.name`. */
    revokeAgentToken(idOderName) {
      return sendJSON('DELETE', `/admin/agent-tokens/${encodeURIComponent(idOderName)}`, undefined);
    },
    /* Karte #788 (ADR-0140 §4): Board-Paar — Seite, Partnerlisten innen und
       außen, Zugänge abs_/abe_. Die Liste trägt nie ein Token; der Klartext
       steht NUR in der Antwort von createBoardpaarZugang ({ token, zugang }).
       Die Seite setzt setConfig('boardpaar.seite', …). */
    getBoardpaar() { return getJSON('/admin/boardpaar'); },
    koppleBoardpaar(seite, boardId, innenLinkBasis) {
      const body = seite === 'aussen' ? { board_id: boardId, innen_link_basis: innenLinkBasis } : { board_id: boardId };
      return sendJSON('POST', `/admin/boardpaar/${seite === 'aussen' ? 'aussen' : 'innen'}`, body);
    },
    entkoppleBoardpaar(seite, boardId) {
      return sendJSON('DELETE', `/admin/boardpaar/${seite === 'aussen' ? 'aussen' : 'innen'}/${encodeURIComponent(boardId)}`, undefined);
    },
    createBoardpaarZugang(typ, name, darfAnlegen = false) {
      return sendJSON('POST', '/admin/boardpaar/zugaenge', { typ, name, darf_anlegen: !!darfAnlegen });
    },
    /* F-2268: boardpaar.team speichern. bisher = der zuletzt gelesene Wert;
       entzöge das Speichern Zugriff und fehlt bestaetigt, antwortet der Server
       409 mit { bestaetigung_noetig, weg, zeilen } und schreibt nichts. */
    setBoardpaarTeam(team, bisher, bestaetigt = false) {
      return sendJSON('PUT', '/admin/boardpaar/team', { team, bisher, bestaetigt: !!bestaetigt });
    },
    /* #825 (ADR-0141 §3): Auftrag „Externe Kopie anlegen“ (innen). */
    createBoardpaarAuftrag(boardId, ziel, innenLinkBasis) {
      const body = { board_id: boardId, ziel };
      if (innenLinkBasis) body.innen_link_basis = innenLinkBasis;
      return sendJSON('POST', '/admin/boardpaar/auftraege', body);
    },
    zieheBoardpaarAuftragZurueck(id) {
      return sendJSON('DELETE', `/admin/boardpaar/auftraege/${encodeURIComponent(id)}`, undefined);
    },
    /* #831 (ADR-0141 Nachtrag): Notweg „Kopie außen löschen“ — nur außen,
       nur Kopien (auch außen entkoppelte), ohne Board-Rolle owner. */
    loescheBoardpaarKopie(boardId) {
      return sendJSON('DELETE', `/admin/boardpaar/kopien/${encodeURIComponent(boardId)}`, undefined);
    },
    /* #836 (ADR-0141 Nachtrag): Schalter „bearbeitbar“ einer Kopie (nur
       außen, nur Kopien). */
    setBoardpaarBearbeitbar(boardId, bearbeitbar) {
      return sendJSON('PUT', `/admin/boardpaar/kopien/${encodeURIComponent(boardId)}/bearbeitbar`, { bearbeitbar: !!bearbeitbar });
    },
    revokeBoardpaarZugang(id) {
      return sendJSON('DELETE', `/admin/boardpaar/zugaenge/${encodeURIComponent(id)}`, undefined);
    },
    /* #848: abgeschlossenen Zugang (widerrufen, oder abgelöst nach der
       Übergangszeit) aus der Tabelle entfernen; 409 = gilt noch, 503 = Uhr
       des Servers nicht gestellt. Und alle abgeschlossenen auf einmal
       → { entfernt, uebersprungen }. */
    entferneBoardpaarZugang(id) {
      return sendJSON('DELETE', `/admin/boardpaar/zugaenge/${encodeURIComponent(id)}/zeile`, undefined);
    },
    raeumeBoardpaarZugaengeAuf() {
      return sendJSON('POST', '/admin/boardpaar/zugaenge/aufraeumen', {});
    },
    revokeAgentTokensOfUser(userId) {
      return sendJSON('POST', `/admin/users/${encodeURIComponent(userId)}/agent-tokens/revoke`, {});
    },
    /* Karte #684 (ADR-0134 §2): Freigabeliste der Ausgangsschleuse. Bei
       ABOARD_SCHLEUSE=aus antwortet der Server 404 (keine Routen).
       → { freigaben: [{id, anbieter, bezeichnung, basis_url, host, modelle,
       vertragsform_bestaetigt, vier_augen, aktiv, freigegeben, nutzbar,
       gruende[]}], anbieter_arten[], ausgaenge: 'an'|'aus', aufbewahrung_tage } */
    listSchleuseFreigaben() { return getJSON('/admin/schleuse/freigaben'); },
    createSchleuseFreigabe(body) { return sendJSON('POST', '/admin/schleuse/freigaben', body); },
    updateSchleuseFreigabe(id, body) {
      return sendJSON('PUT', `/admin/schleuse/freigaben/${encodeURIComponent(id)}`, body);
    },
    deleteSchleuseFreigabe(id) {
      return sendJSON('DELETE', `/admin/schleuse/freigaben/${encodeURIComponent(id)}`, undefined);
    },
    /* → { boards: [{id, title, an}] } — alle Boards mit Schalterstand */
    listAgentSelbstbedienung() { return getJSON('/admin/agent-selbstbedienung'); },
    setAgentSelbstbedienung(boardId, an) {
      return sendJSON('PUT', `/admin/boards/${encodeURIComponent(boardId)}/agent-selbstbedienung`, { an: !!an });
    },
    /* F-1439 → { boards: [{id, title, updated_at, hat_acl, owners[]}] } — ALLE
       Boards, auch ohne ACL (Admin-only; ältere Server: 404). */
    /* F-1442: Der Server liefert seitenweise (`limit` Standard 500/max. 1000,
       `offset`; Antwort mit `total` und — solange es weitere gibt — `next_offset`).
       Alle Seiten nachladen, mit Obergrenze gegen Endlosschleifen; ein Server
       ohne `next_offset` (älter) liefert wie bisher die eine Antwort. Scheitert
       eine Folgeseite, kommt dieser Fehler zurück (lieber sichtbar unvollständig
       als still abgeschnitten). */
    async listAllBoards() {
      const MAX_SEITEN = 50;
      let r = await getJSON('/admin/boards');
      if (!r.ok || !Array.isArray(r.body?.boards)) return r;
      const boards = [...r.body.boards];
      let next = r.body.next_offset;
      /* F-1477 (a): meldet der Server „mehr“, aber mit einem Offset, der nicht
         vorwärts zeigt, wird nicht weitergeblättert (sonst Endlosschleife) — die
         Liste ist dann aber unvollständig, auch ohne `total`. */
      let rueckwaerts = false;
      for (let i = 1; i < MAX_SEITEN && Number.isInteger(next) && next > 0; i++) {
        const seite = await getJSON(`/admin/boards?offset=${next}`);
        if (!seite.ok || !Array.isArray(seite.body?.boards)) return seite;
        boards.push(...seite.body.boards);
        const weiter = seite.body.next_offset;
        if (Number.isInteger(weiter) && weiter > 0 && weiter <= next) rueckwaerts = true;
        next = Number.isInteger(weiter) && weiter > next ? weiter : undefined;
      }
      /* Verify Sprint 80, V-4: Die Seiten sind kein Schnappschuss — wird ein Board
         während des Ladens geändert, rückt es nach vorn (Sortierung jüngst zuerst)
         und erscheint doppelt bzw. fehlt. Doppelte fallen weg; fehlt etwas gegenüber
         `total` oder greift die Seitengrenze, heisst die Liste `unvollstaendig` —
         sichtbar unvollständig statt still abgeschnitten. */
      const gesehen = new Set();
      /* F-1477 (b): nur Boards MIT id entdoppeln — ohne id gibt es nichts zu
         vergleichen, und `undefined` als Schlüssel würfe jedes weitere weg. */
      const eindeutig = boards.filter(b => b && (b.id == null || (!gesehen.has(b.id) && gesehen.add(b.id))));
      const total = r.body.total;
      const unvollstaendig = Number.isInteger(next) && next > 0
        || rueckwaerts
        || (Number.isInteger(total) && eindeutig.length < total);
      return { ok: true, status: r.status, body: { ...r.body, boards: eindeutig, next_offset: undefined, unvollstaendig } };
    },
    deleteUser(userId, uebertragen) {
      const payload = uebertragen && Object.keys(uebertragen).length > 0
        ? { uebertragen } : undefined;
      return sendJSON('DELETE', `/admin/users/${encodeURIComponent(userId)}`, payload);
    },
  };
}

export { AdminClient };
