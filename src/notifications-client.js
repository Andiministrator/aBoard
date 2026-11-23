/* notifications-client.js — REST-Wrapper für /users/me/notifications/* und
 * /boards/{id}/notifications/events (#305, ADR-0107).
 *
 * IS_SERVER_BUILD-gegated → in Slim/Claude-Code-Builds per DCE eliminiert.
 * Same-Origin (Forward-Auth-Cookies via Browser); strukturierte
 * { ok, status, body }-Antworten, kein throw.
 */

import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let NotificationsClient = null;

if (IS_SERVER_BUILD) {
  async function getJSON(path) {
    try {
      const res = await holen(path);
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }
  async function sendJSON(method, path, payload) {
    try {
      const res = await holen(path, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: payload === undefined ? null : JSON.stringify(payload),
      });
      const body = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  NotificationsClient = {
    /* GET /users/me/notifications/event-types → { event_types: [...], channels: [...] } */
    eventTypes() { return getJSON('/users/me/notifications/event-types'); },
    /* GET /users/me/notifications/subscriptions → effektive Liste (incl. Defaults) */
    listSubs() { return getJSON('/users/me/notifications/subscriptions'); },
    /* PUT /users/me/notifications/subscriptions/{eventType}/{channel} { enabled } */
    setSub(eventType, channel, enabled) {
      return sendJSON(
        'PUT',
        `/users/me/notifications/subscriptions/${encodeURIComponent(eventType)}/${encodeURIComponent(channel)}`,
        { enabled }
      );
    },
    /* GET /users/me/notifications/config/{channel} → { channel, is_set, url? } */
    getConfig(channel) {
      return getJSON(`/users/me/notifications/config/${encodeURIComponent(channel)}`);
    },
    /* PUT /users/me/notifications/config/{channel} { url, token } */
    setConfig(channel, body) {
      return sendJSON('PUT', `/users/me/notifications/config/${encodeURIComponent(channel)}`, body);
    },
    /* DELETE /users/me/notifications/config/{channel} */
    deleteConfig(channel) {
      return sendJSON('DELETE', `/users/me/notifications/config/${encodeURIComponent(channel)}`, undefined);
    },
    /* POST /users/me/notifications/test/{channel} — Test-Send */
    test(channel) {
      return sendJSON('POST', `/users/me/notifications/test/${encodeURIComponent(channel)}`, {});
    },
    /* POST /boards/{id}/notifications/events {event_type, card_id, …} — Client-Trigger */
    emit(boardId, body) {
      return sendJSON('POST', `/boards/${encodeURIComponent(boardId)}/notifications/events`, body);
    },
  };
}

export { NotificationsClient };
