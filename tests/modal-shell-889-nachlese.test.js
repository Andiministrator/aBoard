/* @vitest-environment happy-dom */

/**
 * F-889-Nachlese (Karte #465, Verify-Lauf Phase 2b, ADR-0115) — eigene
 * Testdatei statt Erweiterung von tests/modal.dom.test.js: Diese Datei
 * gehört mehreren Karten (F-752-Bestandsschutz), der Auftrag für #465
 * beschränkt sich ausdrücklich auf src/modal-shell.js + eine eigene
 * Testdatei (siehe Agent-Kontext der Karte).
 *
 * Befund des Verify-Laufs: Die Stapel-Regel in modal-shell.js#onKey
 * ("schliesse nur, wenn ich das oberste offene Overlay bin") setzt
 * stillschweigend voraus, dass die DOM-Reihenfolge die AKTUELLE
 * Öffnungsreihenfolge widerspiegelt. Das stimmte nicht: `createModal()`s
 * `open()` hängte das Overlay nur beim ALLERERSTEN Aufruf an
 * `document.body` — jedes weitere `open()` liess die (beim ersten Mal
 * gesetzte) DOM-Position unverändert. Wird eine Shell-Instanz einmal
 * geöffnet+geschlossen, DANACH das Karten-Modal zum ersten Mal geöffnet
 * (mountet hinter der Shell) und DANACH die Shell erneut geöffnet, zeigt
 * die DOM-Reihenfolge weiterhin "Shell vor Karten-Modal" — obwohl die
 * Shell zuletzt (und damit visuell zuoberst) geöffnet wurde. Ergebnis vor
 * dem Fix: modal-shell.js#onKey sagt der Shell "du bist nicht oberstes"
 * (Karten-Modal steht später im DOM), der F-752-Guard in
 * modal.js#onGlobalKey sagt dem Karten-Modal "ein anderes Overlay ist
 * offen" — beide Guards treten zurück, ESC schliesst GAR NICHTS mehr.
 *
 * Fix: `open()` hängt das Overlay jetzt bei JEDEM Aufruf an
 * `document.body` (nicht nur beim ersten) — `appendChild` auf einen
 * bereits angehängten Knoten verschiebt ihn per DOM-Spezifikation ans
 * Ende, stellt also die Reihenfolge "DOM-Position = Öffnungsreihenfolge"
 * bei jedem Reopen wieder her.
 *
 * Reichweite (Verify-Lauf, Frage 3): Über die UI ist dieser Zustand NICHT
 * erreichbar. Die einzigen zwei Shell-Konsumenten, die zusammen mit dem
 * Karten-Modal offen sein können (email-dialog.js, llm-pii.js), werden
 * AUSSCHLIESSLICH aus dem bereits geöffneten Karten-Modal heraus geöffnet
 * (modal.js:2538 bzw. das LLM-Tab, das als Teil des Karten-Modal-Bodys
 * gerendert wird, src/modal.js:2273) — ihr Overlay mountet dadurch IMMER
 * erst NACH dem des Karten-Modals, nie davor. Die sechs eigenständigen
 * Sidebar-Dialoge (admin-dialog.js, llm-keys-dialog.js,
 * notifications-dialog.js, clients-view.js, projects-view.js,
 * configdialog.js) waren zum Zeitpunkt der Messung nur über die Sidebar
 * erreichbar, die hinter dem voll-viewport-deckenden Overlay des
 * Karten-Modals (wie jedes `.modal-overlay`, position:fixed/inset:0) nicht
 * klickbar ist. [Stand Sprint 83, #703/#706: Der Dialog Benutzer-Einstellungen
 * öffnet seither vier Dialoge (KI-Schlüssel, Benachrichtigungen,
 * Agenten-Zugänge, Schleusen-Schlüssel) über sich — die Prämisse „nur über die
 * Sidebar“ gilt für diese nicht mehr; der Zustand bleibt trotzdem nicht aus
 * einem offenen Karten-Modal heraus erreichbar, weil die Benutzer-Einstellungen
 * selbst hinter dessen Overlay liegen.] Kein
 * globaler Tastatur-Shortcut umgeht das (grep 'config:open\|admin:open\|
 * llm-keys:open\|clients-view:open\|projects-view:open'
 * über src/*.js: einzige Aufrufer sind sidebar.js-Klicks). Kein einziger
 * der acht Konsumenten ist also sowohl eigenständig über die Sidebar ALS
 * AUCH von innerhalb des Karten-Modals erreichbar — der hier gefundene
 * Zustand ist damit strukturell so latent wie F-889 selbst: nur über den
 * EventBus erreichbar (dieser Test), nicht über echte Nutzer-Klicks. Der
 * Fix bleibt trotzdem geboten — dieselbe Begründung wie bei F-889 selbst
 * (Karte #465): der nächste Konsument, der eine Shell-Instanz unabhängig
 * VOM Karten-Modal UND von innerhalb öffnenbar macht, erbt sonst die
 * Sackgasse, ohne sie zu suchen.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

let boardModeValue = 'standalone'
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue
      if (key === 'userName') return 'TestUser'
      return ''
    }),
    set: vi.fn(),
  },
}))

import { EventBus }    from '../src/eventbus.js'
import { Modal }       from '../src/modal.js'
import { createModal } from '../src/modal-shell.js'

function makeCard(overrides = {}) {
  return {
    id: 'card-test-889', displayId: 89, title: 'F-889-Nachlese-Testkarte',
    description: '', agentContext: '', comments: [], attachments: [],
    links: [], activity: [], priority: 'mittel', labels: [], owner: '',
    complexity: '', effort: null, qa: false, critical: false, blocked: false,
    archived: false, columnId: 'col-backlog',
    ...overrides,
  }
}

/** Öffnet das Karten-Modal über den echten EventBus-Pfad, wie im Produktivcode. */
function openCardModal(card) {
  EventBus.emit('card:edit', card)
  /* Das Karten-Modal trägt zusätzlich die F-749-Marker-Klasse
     modal-overlay--blocks-sync (src/modal.js:2028) — DAS ist der stabile
     Selektor. `.modal-overlay` allein liefert in diesem Aufbau (Shell VOR
     dem Karten-Modal erzeugt) das FALSCHE Overlay, weil das erste
     `.modal-overlay` im DOM dann die Shell-Instanz ist, nicht das
     Karten-Modal (exakt die Falle, die der Verify-Lauf beim ersten
     Messversuch selbst benannt hat). */
  return document.querySelector('.modal-overlay--blocks-sync')
}

function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

beforeEach(() => {
  boardModeValue = 'standalone'
  resetModal()
})

afterEach(() => {
  resetModal()
})

describe('F-889-Nachlese: Sackgasse bei Shell-vor-Karten-Modal-Erstmontage (Verify-Lauf #465)', () => {
  it('ESC schliesst nach ESC genau einen der beiden Dialoge — den visuell oberen (die zuletzt geöffnete Shell-Instanz)', () => {
    /* 1) Shell-Instanz erzeugen, öffnen, wieder schliessen — mountet sie
       als ERSTES Kind von document.body. */
    const dialog = createModal({
      extraOverlayClass: 'test-889-overlay',
      extraPanelClass:   'test-889-panel',
      titleId:           'test-889-title',
      closeAriaLabel:    'Schließen',
    })
    dialog.open()
    dialog.close()

    /* 2) Karten-Modal ERSTMALS öffnen — mountet danach, also HINTER der
       (aktuell geschlossenen) Shell-Instanz. */
    const cardOverlay = openCardModal(makeCard())
    expect(cardOverlay, 'Karten-Modal-Overlay nicht gefunden').toBeTruthy()
    expect(cardOverlay.classList.contains('modal-overlay--open')).toBe(true)

    /* 3) Shell-Instanz ERNEUT öffnen — vor dem Fix bleibt sie an ihrer
       alten (zu frühen) DOM-Position; nach dem Fix wandert sie ans Ende. */
    dialog.open()
    expect(dialog.overlay.classList.contains('modal-overlay--open')).toBe(true)
    expect(cardOverlay.classList.contains('modal-overlay--open')).toBe(true)

    /* Beide Overlays sind jetzt gleichzeitig offen — der Stapel steht,
       exakt wie im Verify-Lauf gemessen. */
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))

    /* Die Shell-Instanz wurde zuletzt geöffnet und liegt damit visuell
       oben — sie schliesst. */
    expect(dialog.overlay.classList.contains('modal-overlay--open'),
      'Die visuell obere (zuletzt geöffnete) Shell-Instanz hätte schliessen müssen').toBe(false)
    /* Das Karten-Modal darunter bleibt offen. VOR dem Fix blieben HIER
       BEIDE Overlays offen (die Sackgasse: keiner der beiden Guards fühlt
       sich zuständig) — dieser Wert allein unterscheidet also nicht
       zwischen "Fix wirkt" und "Sackgasse besteht weiter"; erst zusammen
       mit der Zusicherung oben (Shell IST zu) ist der Fall eindeutig
       entschieden. */
    expect(cardOverlay.classList.contains('modal-overlay--open'),
      'Das Karten-Modal hätte offen bleiben müssen').toBe(true)

    dialog.close()
  })
})
