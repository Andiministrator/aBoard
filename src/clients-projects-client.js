/* clients-projects-client.js — REST-Wrapper für /clients und /projects
 * (#307, ADR-0102). Server-only; im Slim/Claude-Code-Build per DCE eliminiert.
 *
 * Konventionen: identisch zu admin-client.js / notifications-client.js:
 *   - Same-Origin-Requests; Auth läuft über Caddy-Forward-Auth-Cookies.
 *   - Fehler werfen NIE — Caller bekommt { ok, status, body } und entscheidet
 *     selbst über UI-Reaktion (Toast/Inline-Fehler).
 *
 * Endpoints (ADR-0102 §REST-API):
 *   GET    /clients                          viewer+
 *   POST   /clients                          admin
 *   PATCH  /clients/{id}                     admin
 *   DELETE /clients/{id}[?hard=true]         admin (Soft-/Hard-Delete)
 *   GET    /clients/{id}/projects            viewer+
 *   GET    /projects[?client_id=]            viewer+
 *   POST   /projects                         admin
 *   PATCH  /projects/{id}                    admin
 *   DELETE /projects/{id}[?hard=true]        admin
 *   GET    /projects/{id}/cards              viewer+ (board-übergreifend)
 */

import { Sitzung } from './sitzung-abgelaufen.js'; /* F-2830, null außerhalb des Server-Builds */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let ClientsProjectsClient = null;

if (IS_SERVER_BUILD) {
  const baseUrl = () => '';

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

  async function sendEmpty(method, path) {
    try {
      const res = await fetch(`${baseUrl()}${path}`, { method });
      Sitzung?.meldeAntwort(res.status); /* F-2830 */
      return { ok: res.ok, status: res.status, body: null };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  ClientsProjectsClient = {
    listClients({ includeInactive = false } = {}) {
      const qs = includeInactive ? '?include=inactive' : '';
      return getJSON(`/clients${qs}`);
    },
    createClient({ name, description, contact }) {
      return sendJSON('POST', '/clients', { name, description, contact });
    },
    updateClient(id, patch) {
      return sendJSON('PATCH', `/clients/${encodeURIComponent(id)}`, patch);
    },
    deleteClient(id, { hard = false } = {}) {
      const qs = hard ? '?hard=true' : '';
      return sendEmpty('DELETE', `/clients/${encodeURIComponent(id)}${qs}`);
    },
    listProjectsForClient(id) {
      return getJSON(`/clients/${encodeURIComponent(id)}/projects`);
    },
    listProjects({ clientId, includeInactive = false } = {}) {
      const qs = new URLSearchParams();
      if (clientId) qs.set('client_id', clientId);
      if (includeInactive) qs.set('include', 'inactive');
      const tail = qs.toString();
      return getJSON(`/projects${tail ? '?' + tail : ''}`);
    },
    createProject({ name, description, clientId }) {
      return sendJSON('POST', '/projects', {
        name, description, client_id: clientId || '',
      });
    },
    updateProject(id, patch) {
      const body = { ...patch };
      if (Object.prototype.hasOwnProperty.call(patch, 'clientId')) {
        body.client_id = patch.clientId || '';
        delete body.clientId;
      }
      return sendJSON('PATCH', `/projects/${encodeURIComponent(id)}`, body);
    },
    deleteProject(id, { hard = false } = {}) {
      const qs = hard ? '?hard=true' : '';
      return sendEmpty('DELETE', `/projects/${encodeURIComponent(id)}${qs}`);
    },
    listCardsForProject(id) {
      return getJSON(`/projects/${encodeURIComponent(id)}/cards`);
    },
    /* #308 Etappe 5: Cross-Board-Kalender-Datenquelle (GET /me/cards). Liefert
       alle terminierten Karten über alle Boards mit Lese-Zugriff. Optionaler
       Range-Filter "<from>~<to>" (YYYY-MM-DD). */
    listMyCards({ dueDateRange } = {}) {
      const qs = dueDateRange ? `?dueDateRange=${encodeURIComponent(dueDateRange)}` : '';
      return getJSON(`/me/cards${qs}`);
    },
    /* Karte #743 (ADR-0138 §4): Termine ohne Karte über alle Boards mit
       Leserecht (GET /me/termine), gleicher Bereichsfilter wie listMyCards. */
    listMyTermine({ dueDateRange } = {}) {
      const qs = dueDateRange ? `?dueDateRange=${encodeURIComponent(dueDateRange)}` : '';
      return getJSON(`/me/termine${qs}`);
    },
    /* #308 Etappe 6: iCal-Subscription-Verwaltung. */
    listICalSubs() {
      return getJSON('/me/ical/subscriptions');
    },
    /* Abos „für außen“ (#748) gibt es seit Karte #795 nicht mehr. */
    createICalSub({ boardId } = {}) {
      return sendJSON('POST', '/me/ical/subscriptions', { board_id: boardId || '' });
    },
    deleteICalSub(id) {
      return sendEmpty('DELETE', `/me/ical/subscriptions/${encodeURIComponent(id)}`);
    },
  };
}

export { ClientsProjectsClient };
