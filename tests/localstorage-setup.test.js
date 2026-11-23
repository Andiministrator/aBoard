/* @vitest-environment happy-dom */
/**
 * Setup-Wächter (F-1766): tests/helpers/localstorage-setup.js setzt den Stub vor
 * JEDEM Test frisch — auch wenn der Vorgänger ein Wurf-Objekt oder undefined
 * hinterlassen hat (z. B. bei rotem expect vor der Rücknahme).
 * Die Tests hängen bewusst aneinander (Reihenfolge innerhalb der Datei).
 */
import { it, expect } from 'vitest'

it('A: hinterlässt ein Wurf-Objekt, ohne es zurückzunehmen', () => {
  globalThis.localStorage = { getItem() { throw new Error('gesperrt') }, setItem() { throw new Error('gesperrt') } }
})

it('B: bekommt trotzdem einen heilen, leeren Stub', () => {
  expect(localStorage.length).toBe(0)
  localStorage.setItem('x', '1')
  expect(localStorage.getItem('x')).toBe('1')
})

it('C: Vorgänger hinterlässt Daten und undefined', () => {
  localStorage.setItem('rest', '1')
  globalThis.localStorage = undefined
})

it('D: heil und ohne Reste des Vorgängers', () => {
  expect(localStorage.getItem('rest')).toBeNull()
  expect(localStorage.length).toBe(0)
})
