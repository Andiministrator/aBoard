/* llm-stream.js — SSE-Decoder für LLM-Stream-Endpoint (#300, ADR-0096 §6).
 *
 * Browser-EventSource unterstützt kein POST mit Body — daher `fetch` +
 * `ReadableStream` + manueller SSE-Parser. Strikt `__SERVER_BUILD__`-gegated:
 * im Slim/Claude-Code-Build per DCE eliminiert (Stream-Endpoint existiert
 * dort nicht).
 *
 * Public API:
 *   await streamLLM({ url, body, signal, on: { token, toolCall, fetch, done, error } })
 *
 * Verhalten:
 *   - Pre-Stream-Fehler (HTTP-Status != 2xx mit JSON-Body, z. B. 423 Lock,
 *     400 context-budget, 412 no-key): wirft das geparsete JSON-Objekt
 *     `{kind, message, ...}` als Error-Subklasse `StreamPreError` mit
 *     `.status` und `.body`. Der Caller unterscheidet Lock/Budget/usw.
 *   - In-Stream-Fehler (event:error nach Stream-Start): ruft `on.error(data)`,
 *     beendet die Schleife. Throw NICHT — der Caller weiß über `on.error`.
 *   - Cancel: AbortController via `signal`. `fetch` wirft DOMException
 *     `AbortError` — Caller sieht das, wir geben den Fehler durch.
 *
 * SSE-Spec-Kurzfassung:
 *   - Events sind durch Leerzeile (\n\n) getrennt.
 *   - Innerhalb eines Events: `event: <typ>` und ein-oder-mehrere `data: <line>`
 *     Zeilen. Mehrere `data:`-Zeilen werden mit \n verkettet (Spec §9.2).
 *   - Wir verarbeiten unsere Typen: token, tool-call, fetch, done, error.
 *
 * `fetch` (#428, ADR-0120 §6) meldet einen serverseitig beantworteten Abruf
 * einer verknüpften Karte: {id, cardId?, label?, title?, ok, reason?}. Anders
 * als `tool-call` ist das KEIN Vorschlag zum Bestätigen — der Abruf ist
 * bereits geschehen; das Ereignis ist die Sichtbarmachung.
 */

import { Sitzung } from './sitzung-abgelaufen.js'; /* F-2830, null außerhalb des Server-Builds */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMStream = null;

if (IS_SERVER_BUILD) {
  class StreamPreError extends Error {
    constructor(status, body) {
      super(body?.message || `pre-stream error ${status}`);
      this.name = 'StreamPreError';
      this.status = status;
      this.body = body || null;
    }
  }

  function _parseEvent(chunk) {
    let event = 'message';
    const dataLines = [];
    for (const raw of chunk.split('\n')) {
      const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw;
      if (line === '' || line.startsWith(':')) continue;
      const colon = line.indexOf(':');
      const field = colon >= 0 ? line.slice(0, colon) : line;
      let value = colon >= 0 ? line.slice(colon + 1) : '';
      if (value.startsWith(' ')) value = value.slice(1);
      if (field === 'event') event = value;
      else if (field === 'data') dataLines.push(value);
    }
    if (dataLines.length === 0) return null;
    const dataStr = dataLines.join('\n');
    let data = null;
    try { data = JSON.parse(dataStr); } catch { data = dataStr; }
    return { event, data };
  }

  async function streamLLM({ url, body, signal, on }) {
    const handlers = on || {};
    let res;
    try {
      res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'text/event-stream' },
        body: JSON.stringify(body || {}),
        signal,
      });
    } catch (e) {
      // Netzwerk-/Abort-Fehler vor Header-Empfang
      throw e;
    }
    Sitzung?.meldeAntwort(res.status); /* F-2830 */
    if (!res.ok) {
      // Pre-Stream-Fehler — Server hat sich entschieden, KEINEN Stream zu
      // schicken. Body ist JSON (Lock-Holder, context-budget, invalid-key, ...).
      let parsed = null;
      try { parsed = await res.json(); } catch {}
      throw new StreamPreError(res.status, parsed);
    }
    if (!res.body || !res.body.getReader) {
      // Test-Mocks ohne ReadableStream-Body: nichts zu streamen, sauber zurück.
      return;
    }
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    while (true) {
      let chunk;
      try {
        chunk = await reader.read();
      } catch (e) {
        // Abort während des Reads → propagieren
        throw e;
      }
      if (chunk.done) break;
      buf += dec.decode(chunk.value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const raw = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const ev = _parseEvent(raw);
        if (!ev) continue;
        if (ev.event === 'token' && handlers.token) handlers.token(ev.data);
        else if (ev.event === 'tool-call' && handlers.toolCall) handlers.toolCall(ev.data);
        else if (ev.event === 'fetch' && handlers.fetch) handlers.fetch(ev.data);
        else if (ev.event === 'done' && handlers.done) handlers.done(ev.data);
        else if (ev.event === 'error' && handlers.error) handlers.error(ev.data);
      }
    }
  }

  LLMStream = {
    streamLLM,
    StreamPreError,
    /* Test-Hook: SSE-Frame-Parser einzeln prüfbar */
    _parseEvent,
  };
}

export { LLMStream };
