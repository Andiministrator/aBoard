/**
 * Kritiker Sprint 88 (F-1965): Das bei F-1945 im Modal behobene `|| ''` steckte
 * weiter in den Kopierpfaden — aus `complexity: 0` (oder `false`) einer Karte
 * wurde beim Anlegen einer Vorlage oder beim Spawn einer Wiederholung `''`.
 * Getestet sind die beiden Pfade mit eigener Funktion (Model.createTemplate,
 * spawnRecurringCards); die Event-Handler in main.js (Vorlage anwenden,
 * template:create/update) folgen demselben Muster ohne eigenen Test.
 */

import { describe, it, expect } from 'vitest'
import { Model } from '../src/model.js'
import { spawnRecurringCards } from '../src/recurrence.js'

describe('F-1965 — complexity 0/false überlebt die Kopierpfade', () => {
  it.each([[0], [false], ['M'], ['']])('Model.createTemplate übernimmt complexity %j', (wert) => {
    expect(Model.createTemplate({ title: 'V', complexity: wert }).complexity).toBe(wert)
  })

  it('fehlende complexity bleibt wie bisher leer', () => {
    expect(Model.createTemplate({ title: 'V' }).complexity).toBe('')
  })

  it.each([[0], [false]])('spawnRecurringCards übernimmt complexity %j in die Instanz', (wert) => {
    const vorlage = {
      id: 'v1', displayId: 1, title: 'Täglich', description: '', labels: [], owner: '',
      complexity: wert, effort: null, columnId: 'col-ready', comments: [],
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    }
    const board = {
      nextDisplayId: 2,
      columnDefs: [{ id: 'col-ready', title: 'Ready' }, { id: 'col-erledigt', title: 'Erledigt' }],
      sprints: [{ id: 's1', title: 'S', cards: [vorlage] }],
    }
    spawnRecurringCards(board, '2026-05-13')
    const instanz = board.sprints[0].cards.find(c => c.id !== 'v1')
    expect(instanz).toBeDefined()
    expect(instanz.complexity).toBe(wert)
  })
})
