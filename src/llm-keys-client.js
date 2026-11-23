/* llm-keys-client.js — REST-Wrapper für /users/me/llm-keys/* (#298, ADR-0094).
 *
 * IS_SERVER_BUILD-gegated → im Slim/Claude-Code-Build per DCE eliminiert.
 * Same-Origin (Forward-Auth-Cookies via Browser); strukturierte
 * { ok, status, body }-Antworten, kein throw.
 */

import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMKeysClient = null;

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

  LLMKeysClient = {
    /* GET /users/me/llm-keys → { providers, keys[{provider, created_at, last_used_at}] } */
    list() { return getJSON('/users/me/llm-keys'); },
    /* POST /users/me/llm-keys/{provider} { key } — Server testet vor Speichern */
    set(provider, key) {
      return sendJSON('POST', `/users/me/llm-keys/${encodeURIComponent(provider)}`, { key });
    },
    /* DELETE /users/me/llm-keys/{provider} */
    delete(provider) {
      return sendJSON('DELETE', `/users/me/llm-keys/${encodeURIComponent(provider)}`, undefined);
    },
    /* POST /users/me/llm-keys/{provider}/test — testet den GESPEICHERTEN Key */
    test(provider) {
      return sendJSON('POST', `/users/me/llm-keys/${encodeURIComponent(provider)}/test`, {});
    },
  };
}

export { LLMKeysClient };
