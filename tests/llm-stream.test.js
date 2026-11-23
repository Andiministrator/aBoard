/* llm-stream.test.js — Tests für den SSE-Decoder (#300 Etappe 3, ADR-0096 §6).
 *
 * Nur im Server-Build-Test-Kontext aktiv (LLMStream wird im Standalone-Kontext
 * via DCE auf null gesetzt — analog LLMTab). */

import { describe, it, expect, vi, afterEach } from 'vitest';

import { LLMStream } from '../src/llm-stream.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const describeOnServer = IS_SERVER_BUILD ? describe : describe.skip;

/* Stream-Mock-Helfer: SSE-Frames in einer ReadableStream-artigen Struktur. */
function makeStream(textChunks) {
  const enc = new TextEncoder();
  let i = 0;
  return {
    getReader: () => ({
      read: async () => {
        if (i >= textChunks.length) return { done: true, value: undefined };
        return { done: false, value: enc.encode(textChunks[i++]) };
      },
    }),
  };
}

function mockOkSSE(textChunks) {
  return { ok: true, status: 200, body: makeStream(textChunks) };
}

describeOnServer('LLMStream._parseEvent', () => {
  it('parst Standard-Event (event + data) zu {event, data}', () => {
    const ev = LLMStream._parseEvent('event: token\ndata: {"text":"hi"}');
    expect(ev).toEqual({ event: 'token', data: { text: 'hi' } });
  });

  it('default-event ist "message" wenn event:-Zeile fehlt', () => {
    const ev = LLMStream._parseEvent('data: {"k":1}');
    expect(ev.event).toBe('message');
    expect(ev.data).toEqual({ k: 1 });
  });

  it('Multi-Line-data: wird mit \\n verkettet (SSE-Spec §9.2)', () => {
    const ev = LLMStream._parseEvent('event: x\ndata: line1\ndata: line2');
    expect(ev.event).toBe('x');
    /* JSON-Parse schlägt fehl → fallback auf String */
    expect(ev.data).toBe('line1\nline2');
  });

  it('Kommentar-Zeilen (mit ":" am Anfang) werden ignoriert', () => {
    const ev = LLMStream._parseEvent(':heartbeat\nevent: done\ndata: {}');
    expect(ev).toEqual({ event: 'done', data: {} });
  });

  it('CRLF wird korrekt geschnitten', () => {
    const ev = LLMStream._parseEvent('event: token\r\ndata: {"text":"x"}');
    expect(ev).toEqual({ event: 'token', data: { text: 'x' } });
  });

  it('Kein data: → null', () => {
    expect(LLMStream._parseEvent('event: ping')).toBeNull();
  });
});

describeOnServer('LLMStream.streamLLM — Happy-Path', () => {
  afterEach(() => { vi.restoreAllMocks(); });

  it('dispatched token- und done-Events in Reihenfolge', async () => {
    const tokens = [];
    let doneData = null;
    globalThis.fetch = vi.fn(async () => mockOkSSE([
      'event: token\ndata: {"text":"Hello"}\n\n',
      'event: token\ndata: {"text":" world"}\n\n',
      'event: done\ndata: {"commentId":"c1","inputTokens":10,"outputTokens":2,"costEstimateUSD":0.001}\n\n',
    ]));
    await LLMStream.streamLLM({
      url: '/x', body: {},
      on: {
        token: (d) => tokens.push(d.text),
        done: (d) => { doneData = d; },
      },
    });
    expect(tokens).toEqual(['Hello', ' world']);
    expect(doneData).toEqual({ commentId: 'c1', inputTokens: 10, outputTokens: 2, costEstimateUSD: 0.001 });
  });

  it('Frames die über Chunk-Grenzen verteilt sind, werden korrekt zusammengesetzt', async () => {
    const tokens = [];
    globalThis.fetch = vi.fn(async () => mockOkSSE([
      'event: token\nda',           /* Chunk 1: data-Header abgeschnitten */
      'ta: {"text":"A"}\n\nevent: ', /* Chunk 2: Frame-Ende + nächster Frame-Beginn */
      'token\ndata: {"text":"B"}\n\n',
    ]));
    await LLMStream.streamLLM({
      url: '/x', body: {}, on: { token: (d) => tokens.push(d.text) },
    });
    expect(tokens).toEqual(['A', 'B']);
  });

  it('In-Stream-error-Event wird an on.error gegeben (kein throw)', async () => {
    let errData = null;
    globalThis.fetch = vi.fn(async () => mockOkSSE([
      'event: error\ndata: {"kind":"unreachable","message":"timeout"}\n\n',
    ]));
    await LLMStream.streamLLM({
      url: '/x', body: {}, on: { error: (d) => { errData = d; } },
    });
    expect(errData).toEqual({ kind: 'unreachable', message: 'timeout' });
  });

  it('tool-call-Events werden an on.toolCall gegeben (Etappe 4 vorbereitet)', async () => {
    const calls = [];
    globalThis.fetch = vi.fn(async () => mockOkSSE([
      'event: tool-call\ndata: {"id":"tc1","action":"set-field","field":"qa","value":true}\n\n',
    ]));
    await LLMStream.streamLLM({
      url: '/x', body: {}, on: { toolCall: (d) => calls.push(d) },
    });
    expect(calls).toEqual([{ id: 'tc1', action: 'set-field', field: 'qa', value: true }]);
  });
});

describeOnServer('LLMStream.streamLLM — Pre-Stream-Errors', () => {
  afterEach(() => { vi.restoreAllMocks(); });

  it('Pre-Stream-Error (HTTP 423 mit JSON-Body) wirft StreamPreError mit status+body', async () => {
    globalThis.fetch = vi.fn(async () => ({
      ok: false, status: 423,
      json: async () => ({ kind: 'locked', message: 'card locked',
                            holder: { user: 'bob' } }),
    }));
    await expect(LLMStream.streamLLM({ url: '/x', body: {}, on: {} }))
      .rejects.toThrow(LLMStream.StreamPreError);
    try {
      await LLMStream.streamLLM({ url: '/x', body: {}, on: {} });
    } catch (e) {
      expect(e.status).toBe(423);
      expect(e.body.kind).toBe('locked');
      expect(e.body.holder.user).toBe('bob');
    }
  });

  it('Pre-Stream-Error 400 context-budget liefert kind im body', async () => {
    globalThis.fetch = vi.fn(async () => ({
      ok: false, status: 400,
      json: async () => ({ kind: 'context-budget', message: '80% exceeded' }),
    }));
    try {
      await LLMStream.streamLLM({ url: '/x', body: {}, on: {} });
      expect.fail('should throw');
    } catch (e) {
      expect(e.name).toBe('StreamPreError');
      expect(e.status).toBe(400);
      expect(e.body.kind).toBe('context-budget');
    }
  });

  it('Pre-Stream-Error ohne JSON-Body → body=null, status korrekt', async () => {
    globalThis.fetch = vi.fn(async () => ({
      ok: false, status: 500,
      json: async () => { throw new Error('not json'); },
    }));
    try {
      await LLMStream.streamLLM({ url: '/x', body: {}, on: {} });
      expect.fail('should throw');
    } catch (e) {
      expect(e.status).toBe(500);
      expect(e.body).toBeNull();
    }
  });
});

describeOnServer('LLMStream.streamLLM — Abort', () => {
  afterEach(() => { vi.restoreAllMocks(); });

  it('fetch-AbortError wird durchgereicht', async () => {
    globalThis.fetch = vi.fn(async () => {
      const e = new Error('aborted');
      e.name = 'AbortError';
      throw e;
    });
    await expect(LLMStream.streamLLM({ url: '/x', body: {}, on: {} }))
      .rejects.toThrow(/abort/i);
  });

  it('Read-Abort (während Stream-Lesen) wird durchgereicht', async () => {
    globalThis.fetch = vi.fn(async () => ({
      ok: true, status: 200,
      body: {
        getReader: () => ({
          read: async () => {
            const e = new Error('aborted mid-stream');
            e.name = 'AbortError';
            throw e;
          },
        }),
      },
    }));
    await expect(LLMStream.streamLLM({ url: '/x', body: {}, on: {} }))
      .rejects.toThrow(/abort/i);
  });
});

describeOnServer('LLMStream.streamLLM — Request-Form', () => {
  afterEach(() => { vi.restoreAllMocks(); });

  it('schickt POST mit Content-Type application/json + Accept text/event-stream', async () => {
    let captured = null;
    globalThis.fetch = vi.fn(async (url, init) => {
      captured = { url, init };
      return mockOkSSE(['event: done\ndata: {}\n\n']);
    });
    await LLMStream.streamLLM({
      url: '/path', body: { foo: 'bar', conversationId: 'c1' }, on: {},
    });
    expect(captured.url).toBe('/path');
    expect(captured.init.method).toBe('POST');
    expect(captured.init.headers['Content-Type']).toBe('application/json');
    expect(captured.init.headers['Accept']).toBe('text/event-stream');
    expect(JSON.parse(captured.init.body)).toEqual({ foo: 'bar', conversationId: 'c1' });
  });
});
