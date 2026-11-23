/* @vitest-environment happy-dom */

/**
 * F-447 (Karte #366) — Deep-Link-Auflösung.
 *
 * Das Format `#board=<id>&card=<ref>` wurde von drei Produzenten erzeugt und
 * von niemandem geparst; jeder Notification-, iCal- und Projects-View-Link
 * landete auf der Board-Wurzel. Die Tests halten fest, was der Router jetzt
 * versteht — und vor allem, was er bewusst NICHT anfasst (Kalender-Route).
 */

import { describe, it, expect } from 'vitest';
import { parseCardHash, findCardByRef } from '../src/deeplink.js';

const board = () => ({
  id: 'board-a',
  sprints: [
    { id: 's1', cards: [
      { id: 'uuid-eins', displayId: 1, title: 'Eins' },
      { id: 'uuid-zwei', displayId: 42, title: 'Zweiundvierzig' },
    ] },
    { id: '__backlog__', cards: [
      { id: 'uuid-drei', displayId: 7, title: 'Sieben', archived: true },
    ] },
  ],
});

describe('parseCardHash — Kurzform #card-<displayId>', () => {
  it('erkennt die historische Form (ADR-0028)', () => {
    expect(parseCardHash('#card-42')).toEqual({ boardId: null, cardRef: '42' });
  });

  it('nur Ziffern — #card-abc ist keine Kurzform', () => {
    expect(parseCardHash('#card-abc')).toBeNull();
    expect(parseCardHash('#card-')).toBeNull();
  });
});

describe('parseCardHash — Langform #board=…&card=…', () => {
  it('Board und Karte', () => {
    expect(parseCardHash('#board=board-b&card=42'))
      .toEqual({ boardId: 'board-b', cardRef: '42' });
  });

  it('Board ohne Karte (board-weite Events wie sprint.closed)', () => {
    expect(parseCardHash('#board=board-b'))
      .toEqual({ boardId: 'board-b', cardRef: '' });
  });

  it('UUID als card-Wert (email.go setzt die interne ID)', () => {
    expect(parseCardHash('#board=board-b&card=7e917786-5d12-44ca-ab16-dbc93c3ad118'))
      .toEqual({ boardId: 'board-b', cardRef: '7e917786-5d12-44ca-ab16-dbc93c3ad118' });
  });

  it('prozent-kodierte Werte werden dekodiert (F-377-Escape der Erzeuger)', () => {
    expect(parseCardHash('#board=a%26b%3Dc&card=1'))
      .toEqual({ boardId: 'a&b=c', cardRef: '1' });
  });

  it('Go-QueryEscape kodiert Leerzeichen als + — das muss zurückgedreht werden', () => {
    /* Genau hier liegt der Unterschied zwischen URLSearchParams und
       decodeURIComponent: letzteres liesse das + stehen. */
    expect(parseCardHash('#board=board+eins&card=1').boardId).toBe('board eins');
  });

  it('Reihenfolge der Parameter ist egal', () => {
    expect(parseCardHash('#card=42&board=board-b'))
      .toEqual({ boardId: 'board-b', cardRef: '42' });
  });

  it('leeres board= → kein Ziel (lieber nichts als ein Wechsel ins Nichts)', () => {
    expect(parseCardHash('#board=&card=42')).toBeNull();
  });
});

describe('parseCardHash — was NICHT gekapert werden darf', () => {
  it('Kalender-Route bleibt unangetastet (ADR-0103)', () => {
    expect(parseCardHash('#/calendar?view=month')).toBeNull();
    expect(parseCardHash('#/calendar?view=week&date=2026-08-17')).toBeNull();
  });

  it('leerer / fremder Hash', () => {
    expect(parseCardHash('')).toBeNull();
    expect(parseCardHash('#')).toBeNull();
    expect(parseCardHash('#irgendwas')).toBeNull();
    expect(parseCardHash('kein-hash')).toBeNull();
  });

  it('robust gegen Nicht-Strings', () => {
    expect(parseCardHash(null)).toBeNull();
    expect(parseCardHash(undefined)).toBeNull();
    expect(parseCardHash(42)).toBeNull();
  });

  it('ein Parameter, der nur „board" ENTHÄLT, zählt nicht', () => {
    expect(parseCardHash('#dashboard=x')).toBeNull();
  });
});

describe('findCardByRef — displayId ODER interne id', () => {
  it('findet per displayId (projects-view.js, ical.go)', () => {
    expect(findCardByRef(board(), '42').id).toBe('uuid-zwei');
  });

  it('findet per UUID (email.go)', () => {
    expect(findCardByRef(board(), 'uuid-eins').displayId).toBe(1);
  });

  it('findet über Swimlanes hinweg, auch im Backlog', () => {
    expect(findCardByRef(board(), '7').id).toBe('uuid-drei');
  });

  it('archivierte Karten werden gefunden — Archiv ist keine Löschung', () => {
    expect(findCardByRef(board(), 'uuid-drei').archived).toBe(true);
  });

  it('unbekannte Referenz → null (kein „irgendeine Karte")', () => {
    expect(findCardByRef(board(), '999')).toBeNull();
    expect(findCardByRef(board(), 'gibt-es-nicht')).toBeNull();
  });

  it('robust gegen kaputte Eingaben', () => {
    expect(findCardByRef(null, '1')).toBeNull();
    expect(findCardByRef({}, '1')).toBeNull();
    expect(findCardByRef({ sprints: null }, '1')).toBeNull();
    expect(findCardByRef(board(), '')).toBeNull();
    expect(findCardByRef(board(), null)).toBeNull();
    expect(findCardByRef({ sprints: [{ cards: null }, null] }, '1')).toBeNull();
  });

  it('numerische Referenz schlägt nicht versehentlich auf eine gleichnamige id an', () => {
    const b = { sprints: [{ cards: [
      { id: '42', displayId: 9 },
      { id: 'x', displayId: 42 },
    ] }] };
    /* displayId hat Vorrang — das ist der Fall der beiden Zahl-Produzenten. */
    expect(findCardByRef(b, '42').id).toBe('x');
  });
});
