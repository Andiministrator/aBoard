/* @vitest-environment happy-dom */

/**
 * Karte #618 / F-1257 — Browser-Seite von „Übernahme aufheben“.
 *
 * 1. Das Modal schickt den Halter mit, den es ZEIGT (`erwartet`), nicht den,
 *    den der Server gerade kennt.
 * 2. Lehnt der Server ab (422 halter-geaendert), zeigt das Modal den aktuellen
 *    Halter bzw. blendet die Zeile aus, wenn keiner mehr hält.
 * 3. Das REST-Plugin liest den aktuellen Halter aus dem 422-Protokoll.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { IS_PROJECTOR_BUILD, IS_SERVER_BUILD } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn(key => (key === 'boardMode' ? 'project' : key === 'userName' ? 'Andi' : '')),
    set: vi.fn(),
  },
}));

import { EventBus } from '../src/eventbus.js';
import '../src/modal.js';
import { __testing__ } from '../src/storage-plugin.js';

const MIT_CLAIM_UI = IS_PROJECTOR_BUILD || IS_SERVER_BUILD;
const BIS = new Date(Date.now() + 3600_000).toISOString();

function karte(claim) {
  return {
    id: 'c618', displayId: 618, title: 'T', description: '', agentContext: '',
    comments: [], attachments: [], links: [], activity: [], priority: 'mittel',
    labels: [], owner: '', assignees: [], complexity: '', effort: null, qa: false,
    critical: false, blocked: false, archived: false, columnId: 'col-ready', claim,
  };
}

function reset() {
  EventBus.emit('config:language-changed');
  document.body.innerHTML = '';
}

describe.skipIf(!MIT_CLAIM_UI)('Modal: Aufheben mit dem angezeigten Halter', () => {
  it('freie Karte: die Übernahme-Zeile bleibt verborgen (updateMetaCardMode blendete sie ein)', () => {
    EventBus.emit('card:edit', karte(undefined));
    expect(document.querySelector('#modal-claim-row').style.display).toBe('none');
  });

  let gesendet;
  const fang = e => { gesendet = e; };
  beforeEach(() => { vi.useFakeTimers(); reset(); gesendet = null; EventBus.on('card:claim-release', fang); });
  afterEach(() => { EventBus.off?.('card:claim-release', fang); reset(); vi.useRealTimers(); });

  function oeffneUndKlicke(claim) {
    EventBus.emit('card:edit', karte(claim));
    const row = document.querySelector('#modal-claim-row');
    expect(row.style.display).toBe('flex');
    const grund = document.querySelector('#modal-claim-grund');
    grund.value = 'Sitzung hängt';
    grund.dispatchEvent(new Event('input'));
    document.querySelector('#modal-claim-aufheben').click();
    return row;
  }

  it('schickt by/session/since des angezeigten claim als `erwartet`', () => {
    oeffneUndKlicke({ by: 'agent:a', session: 'a', since: '2026-10-01T10:00:00.000Z', until: BIS });
    expect(gesendet).toMatchObject({
      cardId: 'c618', halter: 'agent:a', grund: 'Sitzung hängt',
      erwartet: { by: 'agent:a', session: 'a', since: '2026-10-01T10:00:00.000Z' },
    });
  });

  it('Ablehnung mit aktuellem Halter: die Zeile zeigt ihn, der Knopf ist wieder aus', () => {
    const row = oeffneUndKlicke({ by: 'agent:a', session: 'a', since: '2026-10-01T10:00:00.000Z', until: BIS });
    gesendet.fertig(false, { by: 'agent:b', session: 'b', since: '2026-10-01T12:00:00.000Z', until: BIS });
    expect(row.style.display).toBe('flex');
    expect(document.querySelector('#modal-claim-text').textContent).toContain('agent:b');
    expect(document.querySelector('#modal-claim-text').textContent).not.toContain('agent:a');
    expect(document.querySelector('#modal-claim-grund').value).toBe('');
    expect(document.querySelector('#modal-claim-aufheben').disabled).toBe(true);
    /* Der nächste Klick schickt den NEUEN Halter als Bedingung. */
    const grund = document.querySelector('#modal-claim-grund');
    grund.value = 'immer noch';
    grund.dispatchEvent(new Event('input'));
    document.querySelector('#modal-claim-aufheben').click();
    expect(gesendet.erwartet).toEqual({ by: 'agent:b', session: 'b', since: '2026-10-01T12:00:00.000Z' });
  });

  it('Ablehnung ohne Halter (null): die Zeile verschwindet', () => {
    const row = oeffneUndKlicke({ by: 'agent:a', session: 'a', since: '2026-10-01T10:00:00.000Z', until: BIS });
    gesendet.fertig(false, null);
    expect(row.style.display).toBe('none');
  });

  it('sonstiger Fehler (kein Halter-Befund): Anzeige bleibt, Grund bleibt, Knopf wieder an', () => {
    oeffneUndKlicke({ by: 'agent:a', session: 'a', since: '2026-10-01T10:00:00.000Z', until: BIS });
    gesendet.fertig(false);
    expect(document.querySelector('#modal-claim-text').textContent).toContain('agent:a');
    expect(document.querySelector('#modal-claim-aufheben').disabled).toBe(false);
  });
});

describe.skipIf(typeof __testing__.createRESTPlugin !== 'function')('REST-Plugin: hebeUebernahmeAuf liest halter-geaendert', () => {
  let spy;
  afterEach(() => spy?.mockRestore());

  async function mitAntwort(status, body) {
    spy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt) => {
      if (opt?.method === 'POST') return { ok: status < 300, status, json: async () => body, headers: new Headers() };
      return { ok: false, status: 404, json: async () => null, headers: new Headers() };
    });
    const plugin = __testing__.createRESTPlugin();
    await plugin.preload('b1').catch(() => {});
    return plugin.hebeUebernahmeAuf([{ type: 'card.release' }]);
  }

  it('422 mit aktuellem Halter → halterAktuell als claim-Objekt', async () => {
    const r = await mitAntwort(422, { protokoll: [{ art: 'halter-geaendert', index: 0, cardId: 'c', halter: 'agent:b', session: 'b', since: 'S', until: 'U' }] });
    expect(r).toEqual({ ok: false, status: 422, halterAktuell: { by: 'agent:b', session: 'b', since: 'S', until: 'U' } });
  });

  it('422 ohne Halter → halterAktuell null', async () => {
    const r = await mitAntwort(422, { protokoll: [{ art: 'halter-geaendert', index: 0, halter: null }] });
    expect(r.halterAktuell).toBeNull();
  });

  it('anderes 422 und 200: kein halterAktuell', async () => {
    expect('halterAktuell' in await mitAntwort(422, { protokoll: [{ art: 'ziel-fehlt', index: 0 }] })).toBe(false);
    expect(await mitAntwort(200, {})).toEqual({ ok: true, status: 200 });
  });
});
