/* @vitest-environment happy-dom */

/**
 * Karte #608 (ADR-0130 §6): Ein Agenten-Kommentar trägt `onBehalfOf` (vom
 * Server gestempelt). Das Karten-Modal zeigt „agent:x (für <Name>)“ — der
 * Name kommt aus den Identitäten, sonst aus dem Teil vor dem @.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn(k => (k === 'userName' ? 'Andi' : '')), set: vi.fn() },
}));

import { EventBus } from '../src/eventbus.js';
import '../src/modal.js';

const karte = comments => ({
  id: 'c1', displayId: 1, title: 'T', description: '', agentContext: '', comments,
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '', assignees: [],
  complexity: '', effort: null, qa: false, critical: false, blocked: false, archived: false, columnId: 'col-ready',
});

describe('Kommentar „für“ eine Person (Karte #608)', () => {
  beforeEach(() => { vi.useFakeTimers(); EventBus.emit('config:language-changed'); document.body.innerHTML = ''; });
  afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = ''; vi.useRealTimers(); });

  it('mit onBehalfOf: Autor plus „(für …)“, E-Mail im title', () => {
    EventBus.emit('card:edit', karte([
      { id: 'k1', author: 'agent:annas-claude', onBehalfOf: 'anna@firma.de', text: 'hallo', created: '2026-10-01' },
      { id: 'k2', author: 'Andi', text: 'selbst', created: '2026-10-01' },
    ]));
    const tags = [...document.querySelectorAll('.comment-author-tag')].map(t => [t.textContent, t.title]);
    expect(tags).toContainEqual([expect.stringMatching(/^agent:annas-claude \((für|for) anna\)$/), 'anna@firma.de']);
    expect(tags.map(t => t[0])).toContain('Andi');
  });
});
