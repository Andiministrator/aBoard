/**
 * Unit-Tests: search.js — Volltextsuche (Karte #56)
 * Abdeckung: matches(), highlightSegments(), clear(), isActive(), getQuery()
 */

/* @vitest-environment node */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { Search } from '../src/search.js'

// Search ist ein Singleton-Modul — nach jedem Test zurücksetzen
beforeEach(() => {
  Search.clear()
})

// ─────────────────────────────────────────────────────────────────────────────
// 1) Grundzustand
// ─────────────────────────────────────────────────────────────────────────────

describe('Search — Grundzustand', () => {
  it('initial ist isActive() false', () => {
    expect(Search.isActive()).toBe(false)
  })

  it('initial ist getQuery() leer', () => {
    expect(Search.getQuery()).toBe('')
  })

  it('nach clear() ist isActive() false', () => {
    Search.clear() // einmal extra
    expect(Search.isActive()).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2) matches() — leere Query
// ─────────────────────────────────────────────────────────────────────────────

describe('Search.matches() — leere Query', () => {
  it('leere Query: jede Karte matched', () => {
    const card = { title: 'Irgendeine Karte', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 3) matches() — Trefferfelder
// ─────────────────────────────────────────────────────────────────────────────

describe('Search.matches() — Trefferfelder', () => {
  // Debounce umgehen: _applyQuery ist intern, also setQuery sofort + flush via clear+direktes Test
  // Wir testen matches() direkt nach clear() + setQuery() mit Fake-Timer

  beforeEach(() => {
    // Debounce-Timer durch Fake-Timers ersetzen damit setQuery synchron wirkt
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    Search.clear()
  })

  function setQuerySync(q) {
    Search.setQuery(q)
    vi.runAllTimers()
  }

  it('Treffer in title (case-insensitiv)', () => {
    setQuerySync('Fehler')
    const card = { title: 'Kritischer Fehler gefunden', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })

  it('kein Treffer wenn title nicht passt und andere Felder leer', () => {
    setQuerySync('xyz123notexisting')
    const card = { title: 'Normale Karte', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(false)
  })

  it('Treffer in description', () => {
    setQuerySync('implementier')
    const card = { title: 'Karte', description: 'Zu implementieren: REST-API', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })

  it('Treffer in comments[].text', () => {
    setQuerySync('review')
    const card = {
      title: 'Feature',
      description: '',
      labels: [],
      owner: '',
      comments: [{ text: 'Needs review before merge', author: 'Andi' }]
    }
    expect(Search.matches(card)).toBe(true)
  })

  it('Treffer in labels[]', () => {
    setQuerySync('bugfix')
    const card = { title: 'Karte', description: '', labels: ['bugfix', 'sprint-1'], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })

  it('Treffer in owner', () => {
    setQuerySync('claude')
    const card = { title: 'Karte', description: '', labels: [], owner: 'Claude Sonnet' }
    expect(Search.matches(card)).toBe(true)
  })

  it('keine Treffer wenn Karte alle Felder leer hat', () => {
    setQuerySync('test')
    const card = { title: '', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(false)
  })

  it('Teilstring-Match (kein Exact-Match erforderlich)', () => {
    setQuerySync('ber')
    const card = { title: 'Überarbeiten', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })

  it('Case-insensitiv: Großbuchstaben in Query treffen Kleinbuchstaben im Titel', () => {
    setQuerySync('SUCHE')
    const card = { title: 'suche funktioniert', description: '', labels: [], owner: '' }
    expect(Search.matches(card)).toBe(true)
  })

  it('fehlende optionale Felder (keine comments, keine labels) werfen keinen Fehler', () => {
    setQuerySync('test')
    const card = { title: 'Testkarte' }
    expect(() => Search.matches(card)).not.toThrow()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 4) highlightSegments()
// ─────────────────────────────────────────────────────────────────────────────

describe('Search.highlightSegments()', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    Search.clear()
  })

  function setQuerySync(q) {
    Search.setQuery(q)
    vi.runAllTimers()
  }

  it('leere Query: gibt einzelnes non-highlight-Segment zurück', () => {
    // query ist leer nach clear()
    const segs = Search.highlightSegments('Hallo Welt')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toEqual({ text: 'Hallo Welt', highlight: false })
  })

  it('Treffer am Anfang des Strings', () => {
    setQuerySync('Hall')
    const segs = Search.highlightSegments('Hallo Welt')
    expect(segs[0]).toMatchObject({ text: 'Hall', highlight: true })
    expect(segs[1]).toMatchObject({ text: 'o Welt', highlight: false })
  })

  it('Treffer am Ende des Strings', () => {
    setQuerySync('Welt')
    const segs = Search.highlightSegments('Hallo Welt')
    expect(segs[0]).toMatchObject({ text: 'Hallo ', highlight: false })
    expect(segs[1]).toMatchObject({ text: 'Welt', highlight: true })
  })

  it('Treffer in der Mitte des Strings', () => {
    setQuerySync('llo')
    const segs = Search.highlightSegments('Hallo Welt')
    expect(segs).toHaveLength(3)
    expect(segs[0]).toMatchObject({ text: 'Ha', highlight: false })
    expect(segs[1]).toMatchObject({ text: 'llo', highlight: true })
    expect(segs[2]).toMatchObject({ text: ' Welt', highlight: false })
  })

  it('mehrere Treffer im selben String', () => {
    setQuerySync('a')
    const segs = Search.highlightSegments('banana')
    const highlighted = segs.filter(s => s.highlight)
    expect(highlighted).toHaveLength(3) // b[a]n[a]n[a]
  })

  it('kein Treffer: gibt einzelnes non-highlight-Segment zurück', () => {
    setQuerySync('xyz999')
    const segs = Search.highlightSegments('Hallo Welt')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ text: 'Hallo Welt', highlight: false })
  })

  it('Case-insensitiv: highlight-Segment enthält Originalschreibweise', () => {
    setQuerySync('hallo')
    const segs = Search.highlightSegments('HALLO Welt')
    expect(segs[0]).toMatchObject({ text: 'HALLO', highlight: true })
  })

  it('leerer Text: gibt einzelnes Segment zurück', () => {
    setQuerySync('test')
    const segs = Search.highlightSegments('')
    expect(segs).toHaveLength(1)
    expect(segs[0].text).toBe('')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5) onChange-Listener
// ─────────────────────────────────────────────────────────────────────────────

describe('Search.onChange()', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    Search.clear()
  })

  it('Listener wird bei Query-Änderung aufgerufen', () => {
    const spy = vi.fn()
    Search.onChange(spy)
    Search.setQuery('test')
    vi.runAllTimers()
    expect(spy).toHaveBeenCalledWith('test')
  })

  it('clear() ruft Listener mit leerem String auf', () => {
    const spy = vi.fn()
    Search.onChange(spy)
    Search.setQuery('abc')
    vi.runAllTimers()
    Search.clear()
    expect(spy).toHaveBeenLastCalledWith('')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 6) Debounce-Verhalten
// ─────────────────────────────────────────────────────────────────────────────

describe('Search — Debounce', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    Search.clear()
  })

  it('Query ist erst nach ~150 ms aktiv', () => {
    Search.setQuery('abc')
    // Vor Ablauf: noch nicht aktiv
    expect(Search.isActive()).toBe(false)
    vi.advanceTimersByTime(151)
    expect(Search.isActive()).toBe(true)
    expect(Search.getQuery()).toBe('abc')
  })

  it('Mehrfache Eingaben innerhalb 150 ms ergeben nur einen Aufruf', () => {
    const spy = vi.fn()
    Search.onChange(spy)
    Search.setQuery('a')
    Search.setQuery('ab')
    Search.setQuery('abc')
    vi.runAllTimers()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy).toHaveBeenCalledWith('abc')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// F-145 — Abmeldeweg für onChange
// ─────────────────────────────────────────────────────────────────────────────

describe('Search — off() (F-145)', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers(); Search.clear() })

  it('abgemeldeter Listener wird nicht mehr gerufen', () => {
    const bleibt = vi.fn()
    const geht   = vi.fn()
    Search.onChange(bleibt)
    Search.onChange(geht)

    expect(Search.off(geht)).toBe(true)

    Search.setQuery('abc')
    vi.runAllTimers()

    expect(bleibt).toHaveBeenCalledTimes(1)
    expect(geht).not.toHaveBeenCalled()

    Search.off(bleibt)
  })

  it('off() mit unbekannter Funktion meldet false und lässt die Liste unberührt', () => {
    const listener = vi.fn()
    Search.onChange(listener)

    expect(Search.off(() => {})).toBe(false)

    Search.setQuery('x')
    vi.runAllTimers()
    expect(listener).toHaveBeenCalledTimes(1)

    Search.off(listener)
  })
})
