/* @vitest-environment happy-dom */

/**
 * Tests für `src/focus-trap.js` (#327, F-347/F-363).
 *
 * Der Sprint-54-Verify-Lauf hat festgestellt: Dieses Modul hatte **keinen
 * einzigen** eigenen Test, obwohl es als *gemeinsamer Helfer* für vier Dialoge
 * deklariert ist und in der Nacharbeit erheblich erweitert wurde
 * (Selektor-Vervollständigung, `visibility:hidden`, `inert`). Getestet war nur
 * indirekt über die Dialoge — also genau die Fälle, die diese Dialoge heute
 * enthalten, und keiner der Fälle, für die der Helfer gemacht ist.
 *
 * Bewusst NICHT gegatet: Der Helfer liegt in jedem Build (der Mass-Delete-Guard
 * in src/sync.js nutzt ihn im claude-code-Build), also müssen die Tests auch
 * in jedem Kontext laufen. Das ist zugleich die Absicherung gegen die
 * F-428-Klasse — verschwindet der Helfer aus einem Build, wird diese Datei rot.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { getFocusableElements, captureFocus, restoreFocus, focusInitial, trapTabKey }
  from '../src/focus-trap.js'

let box

beforeEach(() => {
  document.body.innerHTML = ''
  box = document.createElement('div')
  document.body.appendChild(box)
})
afterEach(() => { document.body.innerHTML = '' })

const setze = (html) => { box.innerHTML = html; return box }
const namen = (els) => els.map(e => e.getAttribute('data-n'))

describe('getFocusableElements — was dazugehört', () => {
  it('findet Formularelemente, Links und [tabindex]', () => {
    setze(`
      <input data-n="input">
      <textarea data-n="textarea"></textarea>
      <select data-n="select"></select>
      <button data-n="button"></button>
      <a href="#" data-n="a"></a>
      <span tabindex="0" data-n="tabindex"></span>
    `)
    expect(namen(getFocusableElements(box)))
      .toEqual(['input', 'textarea', 'select', 'button', 'a', 'tabindex'])
  })

  it('findet auch die Elemente, die kein heutiger Dialog benutzt', () => {
    /* Der Grund für diesen Test: Kein Konsument enthält so etwas — der Fehler
       wäre also nicht auslösbar und beim Review des NÄCHSTEN Dialogs nicht zu
       finden, weil er im Helfer steckt (Sprint-54-Frontend-Kritiker). */
    /* `style="display:inline"` bei audio/video ist kein Kunstgriff, sondern
       nötig: happy-dom gibt Medienelementen `display:none` als berechneten
       Stil, und der (korrekte) Sichtbarkeitsfilter würde sie sonst
       aussortieren — der Test prüfte dann den Filter statt den Selektor. */
    setze(`
      <details data-n="details"><summary data-n="summary">S</summary>Inhalt</details>
      <audio controls style="display:inline" data-n="audio"></audio>
      <video controls style="display:inline" data-n="video"></video>
      <div contenteditable="true" data-n="editable"></div>
      <iframe data-n="iframe"></iframe>
      <map><area href="#" data-n="area"></map>
    `)
    const gefunden = namen(getFocusableElements(box))
    for (const n of ['summary', 'audio', 'video', 'editable', 'iframe', 'area']) {
      expect(gefunden, `${n} fehlt im Selektor`).toContain(n)
    }
  })

  it('das <details>-Element SELBST gehört nicht dazu, nur sein <summary>', () => {
    /* In echten Browsern ist <details> nicht per Tab erreichbar und nicht
       programmatisch fokussierbar. Stünde es in der Liste, fokussierte der
       Tab-Umlauf dorthin ins Leere und der Fokus verliesse die Falle.
       happy-dom bildet das nicht ab — deshalb prüft dieser Test den
       SELEKTOR, nicht das Fokus-Verhalten (Sprint-54-Verify-Lauf). */
    setze('<details data-n="details"><summary data-n="summary">S</summary></details>')
    const gefunden = namen(getFocusableElements(box))
    expect(gefunden).toContain('summary')
    expect(gefunden).not.toContain('details')
  })

  it('contenteditable="false" zählt nicht', () => {
    setze('<div contenteditable="false" data-n="nein"></div><div contenteditable="true" data-n="ja"></div>')
    expect(namen(getFocusableElements(box))).toEqual(['ja'])
  })
})

describe('getFocusableElements — was ausgeschlossen bleibt', () => {
  it('disabled', () => {
    setze('<button disabled data-n="aus"></button><button data-n="an"></button>')
    expect(namen(getFocusableElements(box))).toEqual(['an'])
  })

  it('tabindex="-1"', () => {
    setze('<span tabindex="-1" data-n="aus"></span><span tabindex="0" data-n="an"></span>')
    expect(namen(getFocusableElements(box))).toEqual(['an'])
  })

  it('display:none (über offsetParent)', () => {
    setze('<button style="display:none" data-n="aus"></button><button data-n="an"></button>')
    expect(namen(getFocusableElements(box))).toEqual(['an'])
  })

  it('visibility:hidden — offsetParent ist dafür blind', () => {
    /* Ein so verstecktes Element behält sein Layout und damit einen
       offsetParent. Läge es in der Falle, verschwände der Fokus für den
       Nutzer scheinbar spurlos. */
    setze('<button style="visibility:hidden" data-n="aus"></button><button data-n="an"></button>')
    expect(namen(getFocusableElements(box))).toEqual(['an'])
  })

  it('inert — wirkt auf den ganzen Teilbaum', () => {
    /* inert entfernt die Fokussierbarkeit, ändert aber nichts am Layout. Ein
       inertes Element als „letztes fokussierbares" führt dazu, dass der
       Tab-Umlauf dorthin fokussieren will, der Browser das verweigert und der
       Fokus irgendwo anders landet — die Falle ist dort offen. */
    setze('<div inert><button data-n="aus"></button></div><button data-n="an"></button>')
    expect(namen(getFocusableElements(box))).toEqual(['an'])
  })

  it('leerer/fehlender Container stürzt nicht ab', () => {
    expect(getFocusableElements(null)).toEqual([])
    expect(getFocusableElements(document.createElement('div'))).toEqual([])
  })

  it('findet NIE alles-oder-nichts: mindestens ein Element bleibt übrig', () => {
    /* Absicherung gegen einen zu scharfen Filter. Ein Helfer, der versehentlich
       alles herausfiltert, wäre wirkungslos statt unvollständig — und das
       fiele in den Dialogen nicht auf, weil dort niemand Tab drückt. */
    setze('<button data-n="a"></button><input data-n="b">')
    expect(getFocusableElements(box).length).toBe(2)
  })
})

describe('captureFocus / restoreFocus', () => {
  it('gibt den Fokus an das gemerkte Element zurück', () => {
    setze('<button data-n="vorher"></button><input data-n="drin">')
    const vorher = box.querySelector('[data-n="vorher"]')
    vorher.focus()
    const gemerkt = captureFocus()
    box.querySelector('[data-n="drin"]').focus()
    restoreFocus(gemerkt)
    expect(document.activeElement).toBe(vorher)
  })

  it('ist ein No-op, wenn das Element aus dem DOM verschwunden ist', () => {
    setze('<button data-n="weg"></button>')
    const weg = box.querySelector('[data-n="weg"]')
    weg.focus()
    const gemerkt = captureFocus()
    weg.remove()
    expect(() => restoreFocus(gemerkt)).not.toThrow()
  })

  it('verträgt null', () => {
    expect(() => restoreFocus(null)).not.toThrow()
  })
})

describe('focusInitial', () => {
  it('bevorzugt das erste <input> — auch wenn ein Button davor steht', () => {
    /* Absicht, kein Zufall: Beim Öffnen eines Dialogs soll der Cursor im
       Eingabefeld stehen, nicht auf dem Abbrechen-Knopf, der im Markup oft
       zuerst kommt. (Meine erste Testfassung erwartete „erstes fokussierbares
       Element" und war damit falsch — der Code hat recht.) */
    setze('<button data-n="knopf"></button><input data-n="feld">')
    focusInitial(box)
    expect(document.activeElement.getAttribute('data-n')).toBe('feld')
  })

  it('ohne Input: der erste Button', () => {
    setze('<a href="#" data-n="link"></a><button data-n="knopf"></button>')
    focusInitial(box)
    expect(document.activeElement.getAttribute('data-n')).toBe('knopf')
  })

  it('ein ausdrücklicher Fallback schlägt den ersten Treffer, wenn nichts passt', () => {
    setze('<a href="#" data-n="link"></a>')
    const fallback = box.querySelector('[data-n="link"]')
    focusInitial(box, fallback)
    expect(document.activeElement).toBe(fallback)
  })

  it('tut nichts bei leerem Container', () => {
    expect(() => focusInitial(box)).not.toThrow()
  })
})

describe('trapTabKey', () => {
  const tab = (shift = false) => {
    const ev = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true, cancelable: true })
    return ev
  }

  it('Tab am LETZTEN Element springt zum ersten', () => {
    setze('<button data-n="erst"></button><button data-n="letzt"></button>')
    const letzt = box.querySelector('[data-n="letzt"]')
    letzt.focus()
    const ev = tab()
    trapTabKey(ev, box)
    expect(ev.defaultPrevented).toBe(true)
    expect(document.activeElement.getAttribute('data-n')).toBe('erst')
  })

  it('Shift+Tab am ERSTEN Element springt zum letzten', () => {
    setze('<button data-n="erst"></button><button data-n="letzt"></button>')
    box.querySelector('[data-n="erst"]').focus()
    const ev = tab(true)
    trapTabKey(ev, box)
    expect(ev.defaultPrevented).toBe(true)
    expect(document.activeElement.getAttribute('data-n')).toBe('letzt')
  })

  it('in der Mitte greift der Trap nicht ein', () => {
    setze('<button data-n="a"></button><button data-n="b"></button><button data-n="c"></button>')
    box.querySelector('[data-n="b"]').focus()
    const ev = tab()
    trapTabKey(ev, box)
    expect(ev.defaultPrevented).toBe(false)
  })

  it('andere Tasten werden nicht abgefangen', () => {
    setze('<button data-n="a"></button>')
    const ev = new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true })
    trapTabKey(ev, box)
    expect(ev.defaultPrevented).toBe(false)
  })
})
