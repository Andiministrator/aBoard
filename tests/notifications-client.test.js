/* notifications-client.test.js — Smoke-Tests für den REST-Wrapper (#305).
 *
 * Nur im Server-Build-Test-Kontext aktiv (vitest.config.server.js setzt
 * __SERVER_BUILD__=true). Im Standalone-Kontext überspringen wir — der
 * Client ist dort per DCE eliminiert (`NotificationsClient === null`).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

import { NotificationsClient } from '../src/notifications-client.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const describeOnServer = IS_SERVER_BUILD ? describe : describe.skip;

describeOnServer('NotificationsClient', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn();
  });

  it('eventTypes() GETs /users/me/notifications/event-types', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ event_types: [], channels: ['email', 'gotify'] }),
    });
    const res = await NotificationsClient.eventTypes();
    expect(res.ok).toBe(true);
    expect(globalThis.fetch).toHaveBeenCalledWith('/users/me/notifications/event-types');
  });

  it('listSubs() GETs /users/me/notifications/subscriptions', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ subscriptions: [] }),
    });
    const res = await NotificationsClient.listSubs();
    expect(res.ok).toBe(true);
    expect(globalThis.fetch).toHaveBeenCalledWith('/users/me/notifications/subscriptions');
  });

  it('setSub() PUTs with eventType + channel in path', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true, enabled: true }),
    });
    await NotificationsClient.setSub('card.commented', 'email', true);
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/users/me/notifications/subscriptions/card.commented/email',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ enabled: true }),
      })
    );
  });

  it('setConfig() PUTs URL + token in body', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    });
    await NotificationsClient.setConfig('gotify', { url: 'https://g', token: 't' });
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/users/me/notifications/config/gotify',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ url: 'https://g', token: 't' }),
      })
    );
  });

  it('test() POSTs to channel-specific endpoint', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    });
    await NotificationsClient.test('gotify');
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/users/me/notifications/test/gotify',
      expect.objectContaining({ method: 'POST' })
    );
  });

  it('emit() POSTs to /boards/{id}/notifications/events', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({ ok: true }),
    });
    const body = { event_type: 'card.commented', card_id: 'c1' };
    await NotificationsClient.emit('board-X', body);
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/boards/board-X/notifications/events',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(body),
      })
    );
  });

  it('returns ok=false on network failure', async () => {
    globalThis.fetch.mockRejectedValue(new Error('net down'));
    const res = await NotificationsClient.listSubs();
    expect(res.ok).toBe(false);
    expect(res.status).toBe(0);
  });

  it('parses non-JSON gracefully (body=null)', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => { throw new Error('not json'); },
    });
    const res = await NotificationsClient.listSubs();
    expect(res.ok).toBe(true);
    expect(res.body).toBeNull();
  });
});

describe('NotificationsClient (Standalone build)', () => {
  it('is null when not server build', () => {
    if (IS_SERVER_BUILD) {
      expect(NotificationsClient).not.toBeNull();
    } else {
      expect(NotificationsClient).toBeNull();
    }
  });
});
