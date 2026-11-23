/* @vitest-environment happy-dom */

/**
 * V-8 (Verify Sprint 86): AdminClient.reload() auf fetch-Ebene — mit Sektion
 * geht `?section=<name>` (URL-kodiert) an den Server, ohne Argument bleibt die
 * URL unverändert (alle Hooks). Die Dialog-Tests mocken AdminClient und sehen
 * die URL deshalb nie.
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let AdminClient = null;
describeServer('AdminClient.reload (V-8)', () => {
  afterEach(() => { delete globalThis.fetch; });
  const lade = async () => { ({ AdminClient } = await import('../src/admin-client.js')); };
  const mock = () => {
    const spy = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ results: {} }) }));
    globalThis.fetch = spy;
    return spy;
  };

  it('reload("imap") → POST /admin/config/reload?section=imap', async () => {
    await lade();
    const spy = mock();
    await AdminClient.reload('imap');
    const [url, opt] = spy.mock.calls[0];
    expect(String(url)).toMatch(/\/admin\/config\/reload\?section=imap$/);
    expect(opt.method).toBe('POST');
  });

  it('reload() ohne Argument → /admin/config/reload ohne Parameter', async () => {
    await lade();
    const spy = mock();
    await AdminClient.reload();
    const [url, opt] = spy.mock.calls[0];
    expect(String(url)).toMatch(/\/admin\/config\/reload$/);
    expect(opt.method).toBe('POST');
  });

  it('Sektionsname wird URL-kodiert', async () => {
    await lade();
    const spy = mock();
    await AdminClient.reload('a b&c');
    expect(String(spy.mock.calls[0][0])).toMatch(/\?section=a%20b%26c$/);
  });
});
