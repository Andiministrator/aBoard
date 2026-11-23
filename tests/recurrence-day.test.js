/**
 * Unit-Tests: Wiederkehrende Karten mit Tagesfeld (F-169, Karte #525)
 *
 * `tests/recurrence.test.js` deckt bereits die day-lose Datumsarithmetik
 * (`nextSpawnDate()`/`spawnRecurringCards()` ohne `day`) ab.
 *
 * F-1103 (Sprint 69, Nacharbeit): `spawnRecurringCards` war hier bis dahin
 * eine „wortgleich gehaltene“ Kopie der Produktionslogik aus src/main.js —
 * nichts hielt sie tatsächlich wortgleich, und der Hardcode
 * `columnId === 'col-erledigt'` stand in Kopie und Produktionsfunktion
 * gleichermassen (F-1103). `spawnRecurringCards` lebt seither wie `nextSpawnDate` in
 * `src/recurrence.js` und wird hier importiert statt nachgebaut.
 *
 * Feldnamen/Kodierung, gegen den echten UI-Code geprüft (src/modal.js,
 * `#modal-recurrence-day` + `updateRecurrenceDayVisibility()`):
 *   - `recurrence.day` bei `weekly`: Wochentag 0-6, JS-Konvention
 *     `Date#getUTCDay()` → 0 = Sonntag, 1 = Montag, … 6 = Samstag.
 *   - `recurrence.day` bei `monthly`: Tag im Monat, 1-31.
 */

import { describe, it, expect } from 'vitest'

// nextSpawnDate/spawnRecurringCards: die ECHTEN Funktionen aus src/recurrence.js
// (Sprint 69 / F-1103), keine Kopie.
import { nextSpawnDate, spawnRecurringCards } from '../src/recurrence.js'

function makeBoard(cards = []) {
  return {
    id:            'board-recurrence-day-test',
    title:         'Recurrence-Day-Test',
    nextDisplayId: 1,
    columnDefs: [
      { id: 'col-backlog',   title: 'Backlog' },
      { id: 'col-ready',     title: 'Ready' },
      { id: 'col-in-arbeit', title: 'In Arbeit' },
      { id: 'col-erledigt',  title: 'Erledigt' },
    ],
    sprints: [
      { id: 's1', title: 'Sprint 1', cards },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ],
  }
}

function makeTemplateCard(overrides = {}) {
  return {
    id:           crypto.randomUUID(),
    displayId:    1,
    title:        'Wiederkehrende Test-Karte',
    description:  '',
    labels:       [],
    comments:     [],
    qa:           false,
    critical:     false,
    blocked:      false,
    owner:        '',
    complexity:   '',
    effort:       null,
    columnId:     'col-ready',
    recurrence:   null,
    ...overrides,
  }
}

// ─────────────────────────────────────────────────────────────────────────
// nextSpawnDate() mit `day` — weekly (Wochentag, 0=Sonntag)
// ─────────────────────────────────────────────────────────────────────────

describe('nextSpawnDate() — weekly mit day (F-169)', () => {
  it('nächster Ziel-Wochentag STRIKT NACH lastSpawned (Mi → nächster Fr)', () => {
    // 2026-05-06 ist ein Mittwoch (getUTCDay()===3); day=5 ist Freitag.
    expect(nextSpawnDate('2026-05-06', 'weekly', 5)).toBe('2026-05-08')
  })

  it('Ziel-Wochentag = Wochentag von lastSpawned → +7 Tage (kein Sofort-Treffer am selben Tag)', () => {
    // 2026-05-06 ist Mittwoch (3), day=3 ebenfalls Mittwoch.
    expect(nextSpawnDate('2026-05-06', 'weekly', 3)).toBe('2026-05-13')
  })

  it('Ziel-Wochentag liegt VOR dem Wochentag von lastSpawned → nächste Woche', () => {
    // 2026-05-06 = Mittwoch (3), day=1 = Montag → nächster Montag ist der 11.
    expect(nextSpawnDate('2026-05-06', 'weekly', 1)).toBe('2026-05-11')
  })

  it('ohne day bleibt das bisherige Verhalten (+7 Tage) unverändert', () => {
    expect(nextSpawnDate('2026-05-06', 'weekly', undefined)).toBe('2026-05-13')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// nextSpawnDate() mit `day` — monthly (Tag im Monat, 1-31)
// ─────────────────────────────────────────────────────────────────────────

describe('nextSpawnDate() — monthly mit day (F-169)', () => {
  it('Ziel-Tag im Folgemonat von lastSpawned', () => {
    expect(nextSpawnDate('2026-04-13', 'monthly', 20)).toBe('2026-05-20')
  })

  it('Monatsende-Klemmung: day=31 in einem Zielmonat mit nur 28 Tagen (Februar, kein Schaltjahr)', () => {
    expect(nextSpawnDate('2026-01-15', 'monthly', 31)).toBe('2026-02-28')
  })

  it('Schaltjahr: day=29 im Zielmonat Februar EINES Schaltjahres trifft exakt (kein Klemmen nötig)', () => {
    expect(nextSpawnDate('2028-01-15', 'monthly', 29)).toBe('2028-02-29')
  })

  it('Schaltjahr: day=30 im Zielmonat Februar eines Schaltjahres klemmt auf den 29.', () => {
    expect(nextSpawnDate('2028-01-15', 'monthly', 30)).toBe('2028-02-29')
  })

  it('Jahreswechsel: Zielmonat Dezember → Januar des Folgejahres', () => {
    expect(nextSpawnDate('2025-12-15', 'monthly', 10)).toBe('2026-01-10')
  })

  it('ohne day bleibt das bisherige Verhalten (+1 Monat, Monatsende-Klemmung) unverändert', () => {
    expect(nextSpawnDate('2026-01-31', 'monthly', undefined)).toBe('2026-02-28')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// spawnRecurringCards() — kein Nachholen übersprungener Termine
// ─────────────────────────────────────────────────────────────────────────

describe('spawnRecurringCards() — kein Nachholen (F-169)', () => {
  it('erzeugt bei mehreren überfälligen Terminen trotzdem nur EINE Spawn-Instanz pro Prüfung', () => {
    // lastSpawned liegt Wochen zurück, day trifft auf denselben Wochentag —
    // ohne Nachhol-Logik darf trotzdem nur eine Karte entstehen.
    const template = makeTemplateCard({
      recurrence: { interval: 'weekly', lastSpawned: '2026-03-04', day: 3 }, // Mittwoch
    })
    const board = makeBoard([template])

    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    const spawned = sprint.cards.filter(c => c.id !== template.id)
    expect(spawned.length).toBe(1)
    // lastSpawned wird auf den PRÜFTAG gesetzt, nicht auf den errechneten Termin —
    // bestehendes Verhalten, hier nur gegen die day-Variante nachgemessen.
    const updatedTemplate = sprint.cards.find(c => c.id === template.id)
    expect(updatedTemplate.recurrence.lastSpawned).toBe('2026-05-13')
  })

  it('monthly mit day: Klemmung wirkt auch im echten Spawn-Durchlauf (dueDate der Instanz)', () => {
    const template = makeTemplateCard({
      recurrence: { interval: 'monthly', lastSpawned: '2026-01-15', day: 31 },
    })
    const board = makeBoard([template])

    spawnRecurringCards(board, '2026-02-28')

    const sprint = board.sprints.find(s => s.id === 's1')
    const spawned = sprint.cards.find(c => c.id !== template.id)
    expect(spawned).toBeTruthy()
    expect(spawned.dueDate).toBe('2026-02-28')
  })
})
