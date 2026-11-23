/**
 * Uhrzeit an Karten (ADR-0138 §1, Karte #742).
 *
 * - Ladeprüfung: gültige Uhrzeiten bleiben, Form-Fehler und Uhrzeiten ohne
 *   Datum werden null (storage.js migrateDates).
 * - Kalender: Uhrzeit am Eintrag, Sortierung je Tag (ganztags zuerst).
 * - Exporte, Wiederholung.
 * - Nachweis D-9: Die Schreibwege, die die Felder nicht kennen (Modal-Diff als
 *   card.field, Drag-and-Drop als card.move, card.upsert aus dem eigenen
 *   Zustand), lassen startTime/dueTime stehen. Genau so verhält sich eine
 *   ältere Fassung, die die Felder nicht kennt: Sie trägt sie als unbekannte
 *   Schlüssel mit, weil keiner dieser Wege Felder aufzählt.
 */
import { describe, it, expect } from 'vitest';
import { Storage } from '../src/storage.js';
import { Model } from '../src/model.js';
import { Export } from '../src/export.js';
import { spawnRecurringCards } from '../src/recurrence.js';
import { cardsByDay, uhrzeitAmTag, upcomingByDue, shiftCardDates } from '../src/calendar/collect.js';
import { istUhrzeit } from '../src/calendar/date-helpers.js';
import { fold } from '../src/ops.js';
import { diffToOps } from '../src/diff.js';
import { describeProjektor } from './helpers/build-flags.js';

const clone = (x) => JSON.parse(JSON.stringify(x));

function board(cards) {
  return {
    version: '2',
    board: {
      id: 'b1', title: 'B', nextDisplayId: 50, templates: [], attachments: [], background: null,
      columnDefs: [
        { id: 'col-backlog', title: 'Backlog' }, { id: 'col-ready', title: 'Ready' },
        { id: 'col-in-arbeit', title: 'In Arbeit' }, { id: 'col-erledigt', title: 'Erledigt' },
      ],
      sprints: [{ id: 's1', title: 'Sprint 1', kind: 'sprint', cards }],
    },
  };
}

function karte(id, felder = {}) {
  return { ...Model.createCard(`Karte ${id}`, 'col-ready', Number(id.replace(/\D/g, '')) || 1), id, ...felder };
}

describe('istUhrzeit', () => {
  it('nimmt HH:MM im 24-Stunden-Format', () => {
    for (const s of ['00:00', '09:30', '19:59', '23:59']) expect(istUhrzeit(s)).toBe(true);
  });
  it('lehnt alles andere ab', () => {
    for (const s of ['', '9:30', '24:00', '23:60', '12:5', null, undefined, 930, '2026-10-08T10:00']) {
      expect(istUhrzeit(s)).toBe(false);
    }
  });
});

describe('Model.createCard — Uhrzeit-Felder NICHT in der Fabrik (ADR-0138 §1, F-925)', () => {
  /* Die Fabrik ist der Feld-Massstab des Projektors. Stünden die optionalen
     Uhrzeit-Felder darin, meldete er jede Neuanlage eines älteren Schreibers
     als unvollständig. Fehlend heisst „keine Uhrzeit“. */
  it('createCard trägt weder dueTime noch startTime', () => {
    const c = Model.createCard('x', 'col-backlog', 1);
    expect('dueTime' in c).toBe(false);
    expect('startTime' in c).toBe(false);
  });
});

describe('Storage.checkVersion — Uhrzeiten beim Laden (ADR-0138 §1)', () => {
  /* checkVersion legt in einer Sprint-Swimlane die Meta-Karten an — die
     eigenen Karten deshalb über die id holen, nicht über die Position. */
  const lade = (cards) => {
    const alle = Storage.checkVersion(board(cards)).board.sprints[0].cards;
    return cards.map(k => alle.find(x => x.id === k.id));
  };

  it('gültige Uhrzeiten mit Datum bleiben', () => {
    const [c] = lade([karte('c1', { startDate: '2026-10-08', startTime: '10:00', dueDate: '2026-10-08', dueTime: '11:30' })]);
    expect([c.startTime, c.dueTime]).toEqual(['10:00', '11:30']);
    /* Das Datum bleibt reines YYYY-MM-DD — es wird NIE um die Uhrzeit erweitert. */
    expect([c.startDate, c.dueDate]).toEqual(['2026-10-08', '2026-10-08']);
  });

  it('Uhrzeit ohne zugehöriges Datum → null', () => {
    const [c] = lade([karte('c1', { dueDate: '2026-10-08', startTime: '10:00', dueTime: null })]);
    expect(c.startTime).toBeNull();
    const [d] = lade([karte('c2', { dueDate: null, dueTime: '17:00' })]);
    expect(d.dueTime).toBeNull();
  });

  it('Uhrzeit in falscher Form → null', () => {
    for (const falsch of ['9:30', '24:00', '10:00:00', 1000, 'mittags']) {
      const [c] = lade([karte('c1', { dueDate: '2026-10-08', dueTime: falsch })]);
      expect(c.dueTime).toBeNull();
    }
  });

  it('ungültiges Datum nimmt seine Uhrzeit mit', () => {
    const [c] = lade([karte('c1', { dueDate: '2026-02-31', dueTime: '10:00' })]);
    expect(c.dueDate).toBeNull();
    expect(c.dueTime).toBeNull();
  });

  it('Karte ohne die Felder bekommt keine dazu (alte Karten bleiben unverändert)', () => {
    const roh = { id: 'c1', displayId: 1, title: 'alt', columnId: 'col-ready', dueDate: '2026-10-08' };
    const [c] = lade([roh]);
    expect('dueTime' in c).toBe(false);
    expect('startTime' in c).toBe(false);
  });
});

describe('Kalender — Uhrzeit am Tag (ADR-0138 §1)', () => {
  it('ein Tag mit Beginn und Ende → von–bis', () => {
    const c = { startDate: '2026-10-08', startTime: '10:00', dueDate: '2026-10-08', dueTime: '11:30' };
    expect(uhrzeitAmTag(c, '2026-10-08')).toEqual({ label: '10:00–11:30', sort: '10:00' });
  });

  it('Ende vor Beginn → nur der Beginn (wie im Feed)', () => {
    const c = { startDate: '2026-10-08', startTime: '10:00', dueDate: '2026-10-08', dueTime: '09:00' };
    expect(uhrzeitAmTag(c, '2026-10-08').label).toBe('10:00');
  });

  it('mehrtägig: Beginn am ersten, Frist am letzten Tag, dazwischen ganztags', () => {
    const c = { startDate: '2026-10-08', startTime: '09:00', dueDate: '2026-10-10', dueTime: '12:00' };
    expect(uhrzeitAmTag(c, '2026-10-08').label).toBe('09:00');
    expect(uhrzeitAmTag(c, '2026-10-09').label).toBeNull();
    expect(uhrzeitAmTag(c, '2026-10-10').label).toBe('12:00');
  });

  it('verwaiste oder kaputte Uhrzeit zeigt nichts', () => {
    expect(uhrzeitAmTag({ dueDate: '2026-10-08', startTime: '10:00' }, '2026-10-08').label).toBeNull();
    expect(uhrzeitAmTag({ dueDate: '2026-10-08', dueTime: '25:00' }, '2026-10-08').label).toBeNull();
  });

  it('cardsByDay: ganztags zuerst, dann nach Uhrzeit, sonst Board-Reihenfolge', () => {
    const e = (id, dueTime) => ({ card: { id, dueDate: '2026-10-08', dueTime }, sprintId: 's1' });
    const tag = cardsByDay([e('a', '14:00'), e('b', null), e('c', '09:00'), e('d', null), e('f', '09:00')]).get('2026-10-08');
    expect(tag.map(x => x.card.id)).toEqual(['b', 'd', 'c', 'f', 'a']);
  });

  it('upcomingByDue: bei gleichem Tag nach Uhrzeit, ganztags zuerst', () => {
    const e = (id, displayId, dueTime) => ({ card: { id, displayId, dueDate: '2026-10-08', dueTime }, sprintId: 's1' });
    const liste = upcomingByDue([e('a', 1, '14:00'), e('b', 2, null), e('c', 3, '09:00')], '2026-10-01');
    expect(liste.map(x => x.card.id)).toEqual(['b', 'c', 'a']);
  });

  it('Drag-and-Drop verschiebt nur Daten — die Uhrzeiten bleiben beim Spread erhalten', () => {
    const c = { id: 'x', startDate: '2026-10-08', startTime: '10:00', dueDate: '2026-10-08', dueTime: '11:00' };
    const neu = { ...c, ...shiftCardDates(c, '2026-10-08', '2026-10-12') };
    expect(neu).toMatchObject({ startDate: '2026-10-12', dueDate: '2026-10-12', startTime: '10:00', dueTime: '11:00' });
  });
});

describe('Exporte — Uhrzeit hinter dem Datum (ADR-0138 §1)', () => {
  const c = karte('c7', { dueDate: '2026-10-08', dueTime: '17:00', startDate: '2026-10-08', startTime: '09:00' });

  it('Karten-Markdown', () => {
    const md = Export.buildCardMarkdown(c, board([c]).board);
    expect(md).toContain('dueTime: "17:00"');
    expect(md).toContain('startTime: "09:00"');
  });

  it('ohne Uhrzeit unverändert', () => {
    const ohne = karte('c8', { dueDate: '2026-10-08' });
    expect(Export.buildCardMarkdown(ohne, board([ohne]).board)).not.toContain('Time:');
  });
});

describe('Wiederkehrende Karten — Instanz übernimmt die Uhrzeit (ADR-0138 §1)', () => {
  it('dueTime der Vorlage, Datum neu', () => {
    const vorlage = karte('c9', { dueTime: '08:30', recurrence: { interval: 'daily', lastSpawned: '2026-10-06' } });
    const st = board([vorlage]);
    spawnRecurringCards(st.board, '2026-10-07');
    const inst = st.board.sprints[0].cards.find(x => x.id !== 'c9');
    expect(inst.dueDate).toBe('2026-10-07');
    expect(inst.dueTime).toBe('08:30');
  });
});

/* ── Nachweis D-9 (Kritiker 07.10., ADR-0138 §1) ──────────────────────────
   Die drei Schreibwege, die Felder NICHT aufzählen, über Diff und Fold — der
   Weg, auf dem die Änderung tatsächlich ins Board kommt. */
describeProjektor('D-9: Schreibwege ohne Kenntnis der Uhrzeit-Felder entfernen sie nicht', () => {
  const mitZeit = () => karte('c1', { startDate: '2026-10-08', startTime: '10:00', dueDate: '2026-10-08', dueTime: '11:30' });
  const zeiten = (st) => {
    const c = st.board.sprints[0].cards.find(x => x.id === 'c1');
    return [c.startTime, c.dueTime];
  };

  it('Modal-Diff (nur der geänderte Titel) → card.field, Uhrzeiten bleiben', () => {
    const prev = board([mitZeit()]);
    const next = clone(prev);
    next.board.sprints[0].cards[0].title = 'neu';
    const ops = diffToOps(prev, next, 'test');
    expect(ops.map(o => o.type)).toEqual(['card.field']);
    expect(ops[0].payload.field).toBe('title');
    expect(zeiten(fold(prev, ops))).toEqual(['10:00', '11:30']);
  });

  it('Drag-and-Drop in eine andere Spalte → card.move, Uhrzeiten bleiben', () => {
    const prev = board([mitZeit()]);
    const next = clone(prev);
    next.board.sprints[0].cards[0].columnId = 'col-in-arbeit';
    const ops = diffToOps(prev, next, 'test');
    expect(ops.map(o => o.type)).toContain('card.move');
    expect(ops.some(o => o.type === 'card.upsert')).toBe(false);
    expect(zeiten(fold(prev, ops))).toEqual(['10:00', '11:30']);
  });

  it('card.upsert aus dem eigenen, geladenen Zustand trägt die Felder mit', () => {
    /* Eine Fassung lädt das Board (checkVersion) und schreibt die Karte als
       Ganzes zurück — die Felder müssen den Ladepfad überleben. */
    const geladen = Storage.checkVersion(board([mitZeit()]));
    const ohne = clone(geladen);
    ohne.board.sprints[0].cards = ohne.board.sprints[0].cards.filter(x => x.id !== 'c1');
    const ops = diffToOps(ohne, geladen, 'test');
    expect(ops.map(o => o.type)).toEqual(['card.upsert']);
    expect(zeiten(fold(ohne, ops))).toEqual(['10:00', '11:30']);
  });
});
