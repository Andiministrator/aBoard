/* llm-mutation-dialog.test.js — Tests für den Confirm-Dialog der
 * Karten-Mutations-Vorschläge (#300 Etappe 5, ADR-0096 §3+§4).
 *
 * Nur im Server-Build-Kontext aktiv. */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

import { LLMTab } from '../src/llm-tab.js';
import { State }  from '../src/state.js';
import { I18n }   from '../src/i18n.js';
import { Config } from '../src/config.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const describeOnServer = IS_SERVER_BUILD ? describe : describe.skip;

function makeBoardState() {
  return {
    version: 1,
    board: {
      id:    'board-1',
      title: 'Test',
      columnDefs: [
        { id: 'col-backlog',   title: 'Backlog' },
        { id: 'col-ready',     title: 'Ready' },
        { id: 'col-in-arbeit', title: 'In Arbeit' },
        { id: 'col-erledigt',  title: 'Erledigt' },
      ],
      sprints: [
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
        {
          id: 'sp-1', title: 'Sprint 1', kind: 'sprint',
          cards: [
            { id: 'card-A', displayId: 1, title: 'Karte A', description: 'A',
              columnId: 'col-ready', labels: [], comments: [], qa: false,
              critical: false, blocked: false, activity: [] },
          ],
        },
      ],
    },
  };
}

/* Mehrere Tests lassen den Dialog offen. Sein keydown-Listener haengt an
 * window (Capture, F-609) und ueberlebt das Leeren des DOM — er sammelt sich
 * ueber die Tests an (F-1793: im Shuffle meldete ein spaeter Escape zwölf
 * Listener auf einmal ab, der Zaehl-Test sah 12 statt 1). Escape schliesst
 * jeden noch offenen Dialog regulaer und meldet dabei den Listener ab. */
function schliesseOffeneDialoge() {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
}

describeOnServer('Mutations-Confirm-Dialog (#300 Etappe 5)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    Config.load();
    Config.set('userName', 'Andi');
    State.set(makeBoardState());
    if (LLMTab) LLMTab._resetCachesForTest();
  });

  afterEach(() => {
    schliesseOffeneDialoge();
    document.body.innerHTML = '';
    State.set(null);
  });

  it('build() rendert die Mutations-Opt-in-Checkbox (default OFF)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    const cb = panel.querySelector('#llm-allow-mutations');
    expect(cb).not.toBeNull();
    expect(cb.checked).toBe(false);
    expect(cb.type).toBe('checkbox');
  });

  it('Dialog rendert pro Vorschlag eine Zeile + zentral einen Schließen-Button', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'set-field',  allowed: true, args: { field: 'qa', value: true } },
      { id: 't2', name: 'add-label',  allowed: true, args: { label: 'urgent' } },
    ], { conversationId: null });
    const dialog = document.querySelector('.llm-mut-dialog');
    expect(dialog).not.toBeNull();
    const rows = dialog.querySelectorAll('.llm-mut-row');
    expect(rows.length).toBe(2);
    expect(dialog.querySelector('.llm-mut-f button')).not.toBeNull();
  });

  it('Server-blockierter Vorschlag wird ohne Apply-Button gerendert', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tx', name: 'card.delete', blocked: true, reason: 'L1-Blocklist' },
    ], {});
    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('blocked');
    expect(row.querySelector('.llm-mut-apply')).toBeNull();
    expect(row.textContent).toMatch(/L1-Blocklist/);
  });

  it('Pre-Apply-Validate blockt unbekanntes set-field-Feld (defense-in-depth)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'set-field', allowed: true, args: { field: 'owner', value: 'X' } },
    ], {});
    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('error');
    expect(row.textContent).toMatch(/owner/);
  });

  it('Apply mutiert State + hängt llm-mutation-applied an die Activity', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'set-field', allowed: true, args: { field: 'qa', value: true } },
    ], { conversationId: 'conv-99', turnId: 'turn-77' });

    const applyBtn = document.querySelector('.llm-mut-apply');
    expect(applyBtn).not.toBeNull();
    applyBtn.click();

    const card = State.get().board.sprints
      .find(s => s.id === 'sp-1').cards
      .find(c => c.id === 'card-A');
    expect(card.qa).toBe(true);
    const entry = card.activity.find(a => a.action === 'llm-mutation-applied');
    expect(entry).toBeTruthy();
    expect(entry.author).toBe('Andi');
    expect(entry.tool).toBe('set-field');
    expect(entry.meta.conversationId).toBe('conv-99');
    expect(entry.meta.turnId).toBe('turn-77');
    expect(entry.meta.toolCallId).toBe('t1');

    /* Row markiert sich als applied + deaktiviert Buttons */
    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('applied');
    expect(row.querySelector('.llm-mut-apply').disabled).toBe(true);
  });

  it('Discard hängt llm-mutation-discarded an die Activity (kein Karten-Mutation)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't9', name: 'add-comment', allowed: true, args: { text: 'Nope' } },
    ], { conversationId: 'conv-1', firmenmodus: false }); /* K2-4: Lauf ohne Firmenmodus */

    const discardBtn = document.querySelector('.llm-mut-discard');
    discardBtn.click();

    const card = State.get().board.sprints
      .find(s => s.id === 'sp-1').cards
      .find(c => c.id === 'card-A');
    /* Kein Kommentar angehängt (Discard ≠ Apply) */
    expect(card.comments.length).toBe(0);
    const entry = card.activity.find(a => a.action === 'llm-mutation-discarded');
    expect(entry).toBeTruthy();
    expect(entry.tool).toBe('add-comment');
    expect(entry.meta.toolCallId).toBe('t9');

    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('discarded');
  });

  it('move-Vorschlag mutiert columnId in-place (diff erzeugt später card.move)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'move', allowed: true, args: { toColumnId: 'col-erledigt' } },
    ], {});
    document.querySelector('.llm-mut-apply').click();
    const card = State.get().board.sprints
      .find(s => s.id === 'sp-1').cards
      .find(c => c.id === 'card-A');
    expect(card.columnId).toBe('col-erledigt');
  });

  it('KEIN „Apply All"-Button vorhanden (ADR-0096 §4 Safety)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'a', name: 'set-field', allowed: true, args: { field: 'qa', value: true } },
      { id: 'b', name: 'add-label', allowed: true, args: { label: 'foo' } },
    ], {});
    const buttons = [...document.querySelectorAll('.llm-mut-acts button, .llm-mut-f button')];
    const labels = buttons.map(b => b.textContent.toLowerCase());
    /* Nirgendwo darf „apply all" / „alle anwenden" / „all" auftauchen */
    for (const t of labels) {
      expect(t).not.toMatch(/all|alle/);
    }
  });

  it('Stale-Karte: Apply liefert false-state-Pfad (Karte nicht mehr im State)', () => {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'card-A' }, 'board-1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'set-field', allowed: true, args: { field: 'qa', value: true } },
    ], {});
    /* Karte zwischenzeitlich aus dem State entfernen */
    const s = State.get();
    s.board.sprints[1].cards = [];
    State.set(s);

    document.querySelector('.llm-mut-apply').click();
    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('error');
  });
});

/* ── F-411 / F-415: Mutations-Confirm-Dialog ─────────────────────────────
 *
 * Zwei Befunde aus dem Sprint-45-Kritiker, gebündelt in Karte #354.
 *
 * F-411: Der Lost-Update-Guard griff nur beim ÖFFNEN des Dialogs. `preview()`
 * wurde einmalig gegen den damaligen Karten-Stand berechnet; `pv.apply(card)`
 * lief beim Klick — Minuten später — blind auf den frischen State. Hatte
 * inzwischen jemand anderes dasselbe Feld geändert, war dessen Wert lautlos
 * weg.
 *
 * F-415: Der Dialog liess sich nur über den Schliessen-Knopf verlassen — kein
 * Escape, kein Backdrop-Klick. */
describeOnServer('LLM-Mutations-Dialog — Lost-Update-Guard und Schliesswege (#354)', () => {
  const KARTE = () => ({
    id: 'card-mut', displayId: 7, title: 'Demo', description: '',
    columnId: 'col-ready', labels: [], comments: [], activity: [],
    qa: false, critical: false, blocked: false, complexity: 'mittel', effort: 2,
  });

  /* Die Texte werden auf Deutsch geprueft; der Sprachzustand ist modulglobal
     und darf nicht von einem Vorgaengertest abhaengen (F-1793, Seed 3). */
  beforeEach(() => {
    I18n.setLang('de');
  });

  function stelleAuf(karte) {
    document.body.innerHTML = '';
    State.set({ board: { sprints: [{ id: 's1', cards: [karte] }] } });
    LLMTab._setStateForTest(JSON.parse(JSON.stringify(karte)), 'b1');
    const panel = document.createElement('div');
    document.body.appendChild(panel);
    return panel;
  }

  function karteAusState(id = 'card-mut') {
    return State.get().board.sprints[0].cards.find(c => c.id === id);
  }

  /* Gezielt per Klasse, NICHT über die Reihenfolge: im DOM steht „Verwerfen"
     VOR „Anwenden" (src/llm-tab.js:755-756). Ein `querySelector('button')`
     trifft also den Discard-Knopf — und ein Test, der versehentlich verwirft,
     ist grün, wenn er auf „nicht angewendet" prüft. */
  function anwendenKlicken() {
    document.querySelector('.llm-mut-apply').click();
  }

  afterEach(() => {
    schliesseOffeneDialoge();
    document.body.innerHTML = '';
    State.set(null);
  });

  it('wendet einen Vorschlag an, wenn sich die Ausgangslage nicht geändert hat', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});

    anwendenKlicken();
    expect(karteAusState().qa).toBe(true);
    expect(document.querySelector('.llm-mut-stale')).toBeNull();
  });

  it('F-411: wendet NICHT an, wenn sich der Ausgangswert zwischen Öffnen und Klick geändert hat', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], {});

    /* Genau das Fenster, um das es geht: Dialog steht offen, ein anderer
       Akteur ändert dasselbe Feld. */
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);

    anwendenKlicken();

    /* Der fremde Wert überlebt — das ist der ganze Punkt von F-411. */
    expect(karteAusState().complexity).toBe('einfach');
    /* Und der Nutzer erfährt davon: eigene Meldung, nicht der Fehler-Text. */
    const hinweis = document.querySelector('.llm-mut-stale');
    expect(hinweis).not.toBeNull();
    expect(hinweis.textContent).toContain('mittel');
    expect(hinweis.textContent).toContain('einfach');
  });

  it('F-620/F-621: der Stale-Ausgang ist sichtbar UND im Verlauf vermerkt', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], { firmenmodus: false }); /* K2-4: Lauf ohne Firmenmodus */
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);

    anwendenKlicken();

    /* F-620: eigene Klasse mit eigener Flaeche — nicht nur der ausgegraute
       discarded-Zustand, in dem der Text bei 60 % Deckkraft verschwindet. */
    const hinweis = document.querySelector('.llm-mut-stale');
    expect(hinweis).not.toBeNull();

    /* F-621: ADR-0096 §3 verlangt den Vermerk im Aktivitaets-Log. Ohne ihn
       laesst sich spaeter nicht unterscheiden, ob ein Mensch abgelehnt hat
       oder die Ausgangslage weggelaufen ist. */
    const eintraege = (karteAusState().activity || [])
      .filter(a => a.action === 'llm-mutation-discarded');
    expect(eintraege.length).toBe(1);
    expect(eintraege[0].meta?.verworfenWeil).toBe('stale-basis');
    expect(eintraege[0].note).toContain('mittel');
    expect(eintraege[0].note).toContain('einfach');
  });

  /* F-622(b): Rückweg nach der Stale-Ablehnung. Bis Sprint 69 sagte die
     Meldung „bitte neu bewerten", aber `_finalize('discarded')` hatte Apply-
     und Discard-Button endgültig deaktiviert — es gab keinen Weg zurück ausser
     Dialog schliessen und den KI-Tab neu zu öffnen. */
  it('F-622(b): der Retry-Knopf holt den aktuellen Stand und macht die Zeile wieder bewertbar', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], {});
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);

    anwendenKlicken(); // stale — Basis war 'mittel', ist jetzt 'einfach'

    const row = document.querySelector('.llm-mut-row');
    expect(row.dataset.state).toBe('discarded');
    const retryBtn = row.querySelector('.llm-mut-retry');
    expect(retryBtn, 'Retry-Knopf fehlt').not.toBeNull();

    retryBtn.click();

    /* Rot ohne den Fix: kein `.llm-mut-retry` im DOM, dieser Aufruf wirft. */
    expect(row.dataset.state).toBe('pending');
    expect(row.querySelector('.llm-mut-stale')).toBeNull();
    expect(row.querySelector('.llm-mut-retry')).toBeNull();
    expect(document.querySelector('.llm-mut-apply').disabled).toBe(false);
    /* Zusammenfassung zeigt jetzt die AKTUELLE Basis ('einfach'), nicht mehr
       die veraltete ('mittel') vom ersten Öffnen. */
    expect(row.querySelector('.llm-mut-summ').textContent).toContain('einfach');

    /* Jetzt klappt die Anwendung — die Basis stimmt jetzt überein. */
    anwendenKlicken();
    expect(karteAusState().complexity).toBe('hoch');
    expect(row.dataset.state).toBe('applied');
  });

  /* F-1106 (Sprint 69, Nacharbeit, A11y): War der verworfene Vorschlag der
     LETZTE offene, hatte `_finalize('discarded')` den Fokus schon auf
     „Schließen" gelegt, BEVOR der Retry-Knopf überhaupt im DOM stand — Tastatur-
     und Screenreader-Nutzer landeten also einen Tab-Stopp zu früh. */
  it('F-1106: war der verworfene Vorschlag der letzte offene, bekommt der Retry-Knopf den Fokus (nicht „Schließen")', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], {});
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);

    anwendenKlicken(); // stale, letzter (einziger) offene Vorschlag → openCount wird 0

    const retryBtn = document.querySelector('.llm-mut-retry');
    expect(retryBtn, 'Retry-Knopf fehlt').not.toBeNull();
    /* Rot ohne den Fix: document.activeElement ist der Schließen-Knopf,
       weil _finalize() ihn fokussiert hat, bevor retryBtn existierte. */
    expect(document.activeElement).toBe(retryBtn);
  });

  /* F-1113 (Verify-Lauf 2, V2-5): Der fokussierte Retry-Knopf entfernt sich
     beim Klick selbst aus dem DOM (siehe oben) — ohne einen neuen Fokus-Ziel
     faellt der Fokus auf `body`, und Tastatur-/Screenreader-Nutzer verlieren
     die Spur. */
  it('F-1113: nach dem Retry-Klick bekommt der wieder aktivierte Apply-Knopf den Fokus, nicht body', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], {});
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);

    anwendenKlicken(); // stale — letzter offene Vorschlag, Retry-Knopf bekommt den Fokus (F-1106)

    const retryBtn = document.querySelector('.llm-mut-retry');
    retryBtn.focus();
    retryBtn.click();

    /* Rot ohne den Fix: document.activeElement ist `body`, weil retryBtn sich
       im Klick-Handler selbst aus dem DOM entfernt hat. */
    expect(document.activeElement).toBe(document.querySelector('.llm-mut-apply'));
    expect(document.activeElement).not.toBe(document.body);
  });

  it('F-622(b): Retry auf eine inzwischen ganz verschwundene Karte belebt die Zeile NICHT wieder', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'hoch' } },
    ], {});
    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].complexity = 'einfach';
    State.set(fremd);
    anwendenKlicken();

    const row = document.querySelector('.llm-mut-row');
    const s = State.get();
    s.board.sprints[0].cards = [];
    State.set(s);

    const retry = row.querySelector('.llm-mut-retry');
    retry.focus();
    retry.click();

    /* F-1118: der Knopf entfernt sich selbst — der Fokus darf nicht auf body fallen. */
    expect(document.activeElement).not.toBe(document.body);
    expect(row.dataset.state).toBe('discarded'); // bleibt beim Stand vor dem Retry-Versuch
    expect(row.querySelector('.llm-mut-err')).not.toBeNull();
    expect(row.querySelector('.llm-mut-retry')).toBeNull(); // kein zweiter Versuch angeboten
  });

  /* F-622(b), zweite Hälfte: Stammt der Konflikt vom EIGENEN Dialog (zwei
     Vorschläge auf dasselbe Feld, der erste schon angewendet), ist „Jemand
     anderes war schneller" schlicht falsch. */
  it('F-622(b): ein Konflikt aus dem EIGENEN Dialog bekommt einen eigenen Text statt "Jemand anderes war schneller"', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
      { id: 'tc2', name: 'set-field', args: { field: 'qa', value: false } },
    ], { firmenmodus: false }); /* K2-4: Lauf ohne Firmenmodus */
    const rows = document.querySelectorAll('.llm-mut-row');
    const applyBtns = document.querySelectorAll('.llm-mut-apply');
    expect(rows.length).toBe(2);

    applyBtns[0].click(); // tc1: qa false -> true, wird angewendet
    expect(karteAusState().qa).toBe(true);
    expect(rows[0].dataset.state).toBe('applied');

    applyBtns[1].click(); // tc2: Basis war 'false', ist jetzt 'true' -> stale
    expect(rows[1].dataset.state).toBe('discarded');
    const hinweis = rows[1].querySelector('.llm-mut-stale');
    expect(hinweis).not.toBeNull();
    /* Rot ohne den Fix: der generische Text nennt "Jemand anderes war
       schneller", obwohl niemand Fremdes beteiligt war. */
    expect(hinweis.textContent).not.toMatch(/Jemand anderes war schneller/);
    expect(hinweis.textContent).toMatch(/anderer Vorschlag in diesem Dialog/);
    /* Trotzdem gibt es auch hier den Rückweg. */
    expect(rows[1].querySelector('.llm-mut-retry')).not.toBeNull();

    /* Und im Verlauf steht die richtige Herkunft, nicht 'stale-basis'. */
    const eintrag = (karteAusState().activity || [])
      .filter(a => a.action === 'llm-mutation-discarded')
      .find(a => a.meta?.toolCallId === 'tc2');
    expect(eintrag?.meta?.verworfenWeil).toBe('stale-basis-self');
  });

  it('F-411: eine Änderung an einem ANDEREN Feld blockiert den Vorschlag nicht', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});

    const fremd = JSON.parse(JSON.stringify(State.get()));
    fremd.board.sprints[0].cards[0].title = 'Anderer Titel';
    State.set(fremd);

    anwendenKlicken();

    expect(karteAusState().qa).toBe(true);
    expect(document.querySelector('.llm-mut-stale')).toBeNull();
  });

  it('F-622: der Dialog haelt den Modal-Vertrag (aria + Fokus)', () => {
    const panel = stelleAuf(KARTE());
    const vorher = document.createElement('button');
    document.body.appendChild(vorher);
    vorher.focus();

    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});

    /* role/aria-modal gehoeren ans OVERLAY, nicht an den inneren Kasten —
       sonst liest ein Screenreader den Hintergrund weiter mit. */
    const overlay = document.querySelector('.llm-mut-overlay');
    expect(overlay.getAttribute('role')).toBe('dialog');
    expect(overlay.getAttribute('aria-modal')).toBe('true');

    /* aria-labelledby muss auf eine existierende Ueberschrift zeigen. */
    const titelId = overlay.getAttribute('aria-labelledby');
    expect(titelId).toBeTruthy();
    expect(document.getElementById(titelId)).not.toBeNull();

    /* Der Fokus steht im Dialog, nicht mehr auf dem Knopf dahinter. */
    expect(overlay.contains(document.activeElement)).toBe(true);

    document.querySelector('.llm-mut-f button').click();

    /* Und kehrt zurueck, wohin er gehoert. */
    expect(document.activeElement).toBe(vorher);
    vorher.remove();
  });

  it('F-415: Escape schliesst den Dialog', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});
    expect(document.querySelector('.llm-mut-overlay')).not.toBeNull();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(document.querySelector('.llm-mut-overlay')).toBeNull();
  });

  it('F-415: Klick auf den Hintergrund schliesst, ein Klick IM Dialog nicht', () => {
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});

    /* Erst der Dialog-Inhalt: darf NICHT schliessen. Sonst verliert ein
       Fehlklick neben einem Knopf die ganze Liste. */
    document.querySelector('.llm-mut-dialog').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(document.querySelector('.llm-mut-overlay')).not.toBeNull();

    const overlay = document.querySelector('.llm-mut-overlay');
    overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(document.querySelector('.llm-mut-overlay')).toBeNull();
  });

  it('F-608: ein move auf eine inzwischen geloeschte Spalte wird abgelehnt', () => {
    /* Die Zweit-Auswertung beim Apply-Klick bekam zunaechst keine Spaltenliste.
       Damit war `_cols` in preview() null und der F-410-Check fiel ersatzlos
       aus — die Pruefung beim Klick war schwaecher als die beim Oeffnen,
       obwohl der Kommentar das Gegenteil versprach. */
    const panel = stelleAuf(KARTE());
    State.set({
      board: {
        columnDefs: [{ id: 'col-ready' }, { id: 'col-review' }],
        sprints: State.get().board.sprints,
      },
    });
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'move', args: { toColumnId: 'col-review' } },
    ], {});

    /* Waehrend der Dialog offen steht, verschwindet die Zielspalte. */
    const ohneReview = JSON.parse(JSON.stringify(State.get()));
    ohneReview.board.columnDefs = [{ id: 'col-ready' }];
    State.set(ohneReview);

    anwendenKlicken();

    expect(karteAusState().columnId).toBe('col-ready');
  });

  it('F-609: Escape schliesst den Dialog, ohne an das Karten-Modal durchzureichen', () => {
    /* Der Dialog lebt INNERHALB des Karten-Modals, das seinerseits auf Escape
       an document hoert. Ohne Capture-Phase + stopImmediatePropagation schloss
       ein Tastendruck beide — und die noch offenen Vorschlaege waren weg. */
    const panel = stelleAuf(KARTE());
    let modalHatGehoert = 0;
    /* Stellt src/modal.js nach: BUBBLE-Phase, und VOR dem Dialog registriert —
       genau die Lage, in der die Reihenfolge sonst gegen uns arbeitet. */
    const modalListener = () => { modalHatGehoert++; };
    document.addEventListener('keydown', modalListener);
    try {
      LLMTab._openMutationDialogForTest(panel, [
        { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
      ], {});

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

      expect(document.querySelector('.llm-mut-overlay')).toBeNull();
      expect(modalHatGehoert).toBe(0);
    } finally {
      document.removeEventListener('keydown', modalListener);
    }
  });

  it('F-415: der keydown-Listener wird beim Schliessen wieder abgemeldet', () => {
    /* Direkt gemessen, nicht über das Verhalten erschlossen: Ein erster
       Entwurf öffnete zwei Dialoge nacheinander und prüfte, dass Escape nicht
       beide schliesst. Der blieb GRÜN, auch mit ausgebautem
       removeEventListener — das `_geschlossen`-Flag macht den hängenden
       Listener wirkungslos, also von aussen unsichtbar. Der Test bewies
       nichts über den Abbau.

       Ein hängender Listener ist trotzdem ein Leck: Bei jedem Dialog kommt
       einer dazu, und alle laufen bei jedem Tastendruck mit. Beobachtbar ist
       das nur an document selbst. */
    const panel = stelleAuf(KARTE());
    /* An `window`, nicht an `document`: Der Listener liegt seit F-609 in der
       Capture-Phase an window (siehe Begründung dort). Ein Späher auf document
       sähe gar nichts mehr — und wäre damit ein Test, der grün bleibt, weil er
       am falschen Objekt misst. */
    const anAdd = vi.spyOn(window, 'addEventListener');
    const anRemove = vi.spyOn(window, 'removeEventListener');
    try {
      LLMTab._openMutationDialogForTest(panel, [
        { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
      ], {});
      const angemeldet = anAdd.mock.calls.filter(([typ]) => typ === 'keydown');
      expect(angemeldet.length).toBe(1);
      /* Capture-Phase — ohne die läuft der Listener nach dem des Karten-Modals
         und stopImmediatePropagation käme zu spät (F-609). */
      expect(angemeldet[0][2]).toBe(true);

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      expect(document.querySelector('.llm-mut-overlay')).toBeNull();

      const abgemeldet = anRemove.mock.calls.filter(([typ]) => typ === 'keydown');
      expect(abgemeldet.length).toBe(1);
      /* Dieselbe Funktionsreferenz — sonst meldet removeEventListener nichts
         ab und der Aufruf ist reine Dekoration. */
      expect(abgemeldet[0][1]).toBe(angemeldet[0][1]);
    } finally {
      anAdd.mockRestore();
      anRemove.mockRestore();
    }
  });

  it('F-645: ein ungueltiger WERT bekommt eine eigene Meldung, nicht "Ungueltige Argumente"', () => {
    /* F-412 fuehrte den Code `invalid-value` ein, die Anzeige-Verzweigung kannte
       ihn aber nicht — er fiel in den Default-Zweig und erschien unter der
       Ueberschrift "Ungueltige Argumente". Die Args waren jedoch strukturell in
       Ordnung; nur der Wert lag ausserhalb der Enum-Liste. Der informative Teil
       (der Hinweis mit Feld und erlaubten Werten) kam zwar an, unter einer
       falschen Ueberschrift.

       Rot, wenn der `invalid-value`-Zweig wieder entfaellt: dann steht dort
       "Ungueltige Argumente". Bis F-645 pruefte KEIN Test diesen
       Anzeige-Pfad — fuer keinen der sechs Fehlercodes (Sprint-55-Kritiker). */
    const panel = stelleAuf(KARTE());
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'complexity', value: 'sehr hoch' } },
    ], {});

    const fehler = document.querySelector('.llm-mut-err');
    expect(fehler).not.toBeNull();
    /* Sprachunabhaengig gegen I18n geprueft, nicht gegen eine feste
       Zeichenkette: Dieser Testkontext laeuft auf EN, andere auf DE — ein Test,
       der eine Sprache festschreibt, wuerde je nach Kontext rot und saehe aus
       wie ein echter Befund. */
    const erwartet = I18n.t('llm.mutation.invalid-value', '');
    const stamm    = erwartet.replace(/[: ]*$/, '');
    expect(fehler.textContent).toContain(stamm);
    expect(fehler.textContent).not.toContain(
      I18n.t('llm.mutation.invalid-args', '').replace(/[: ]*$/, ''));
    /* Der Hinweis nennt weiterhin das Feld — sonst waere die eigene Meldung
       zwar korrekter, aber weniger nuetzlich als die alte. */
    expect(fehler.textContent).toContain('complexity');
    /* F-646: Die Meldung erscheint erst nach dem Klick und moeglicherweise weit
       weg vom Fokus. Ohne Live-Region liest ein Screenreader sie nicht vor. */
    expect(fehler.getAttribute('role')).toBe('alert');

    /* Dialog ausdruecklich schliessen. Die gemeinsame `afterEach`-Bereinigung
       raeumt nur das DOM ab, ohne `_schliessen()` zu rufen — der Listener
       bliebe bis zum naechsten Escape haengen und wuerde in Folgetests
       mitzaehlen. Genau diese Unterlassung war der Grund, warum die Deckung
       des F-629-Guards ueberhaupt kollateral zustande kam. */
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
  });

  it('F-629: ein am Dialog vorbei abgeraeumtes Overlay verschluckt kein fremdes Escape', () => {
    /* Der Rueckweg aus F-609: Verschwindet das Overlay auf einem anderen Weg
       aus dem Dokument als ueber `_schliessen()` — etwa weil das umgebende
       Karten-Modal seinen Inhalt abraeumt —, bleibt der Capture-Listener an
       `window` haengen. Da er `stopImmediatePropagation` ruft, VERSCHLUCKT er
       dann fremde Escape-Tastendruecke, statt sie nur zu ignorieren.

       Dieser Test misst genau das und nichts anderes. Er stand vorher nur
       kollateral unter Deckung: Ein frueherer Testfall liess seinen Dialog
       offen, und erst die gemeinsame `afterEach`-Bereinigung stellte die Lage
       her. Isoliert ausgefuehrt (`-t "F-609"`) blieb die Suite auch mit
       ausgebautem Guard gruen — eine Deckung, die von der Testreihenfolge
       abhaengt, ist keine (Sprint-55-Verify-Lauf, ADR-0115).

       Trennscharf ist allein die Beobachtung am fremden Listener: Der Abbau
       per `removeEventListener` fiele auch ohne Guard an, weil `_schliessen()`
       ihn ohnehin ruft. Das Verschlucken faellt nur weg, wenn der Guard VOR
       `stopImmediatePropagation` greift. */
    const panel = stelleAuf(KARTE());
    /* Die EIGENE Listener-Referenz mitschneiden. Ein globaler Zaehler ueber
       `removeEventListener` waere reihenfolge-abhaengig: Laeuft vorher ein Test,
       der seinen Dialog offen laesst, meldet dessen Listener sich beim selben
       Escape ebenfalls ab (genau die Selbst-Abmeldung, die hier geprueft wird)
       — und der Zaehler steht auf 2 statt 1. Genau das ist beim Schreiben des
       F-645-Tests passiert: ein Test ueber Reihenfolgen-Abhaengigkeit, der
       selbst reihenfolge-abhaengig war. */
    const anAdd = vi.spyOn(window, 'addEventListener');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 'tc1', name: 'set-field', args: { field: 'qa', value: true } },
    ], {});
    const meinListener = anAdd.mock.calls.filter(([typ]) => typ === 'keydown').at(-1)[1];
    anAdd.mockRestore();

    const overlay = document.querySelector('.llm-mut-overlay');
    expect(overlay).not.toBeNull();
    /* Am Dialog vorbei aus dem Dokument nehmen — `_schliessen()` laeuft NICHT,
       der Listener bleibt also angemeldet. Genau die Lage aus dem Kommentar. */
    overlay.remove();

    let fremdHatGehoert = 0;
    const fremderListener = () => { fremdHatGehoert++; };
    document.addEventListener('keydown', fremderListener);
    const anRemove = vi.spyOn(window, 'removeEventListener');
    try {
      const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      document.dispatchEvent(ev);

      /* Der Kern: das fremde Escape kommt durch. Ohne den Guard ist das 0. */
      expect(fremdHatGehoert).toBe(1);
      /* Und es wird auch nicht stillschweigend entwertet. */
      expect(ev.defaultPrevented).toBe(false);
      /* Der Listener hat sich dabei selbst abgemeldet — sonst waere der
         naechste Tastendruck wieder betroffen. Gefiltert auf GENAU diese
         Funktionsreferenz (siehe oben), nicht auf die Anzahl aller
         keydown-Abmeldungen. */
      const abgemeldet = anRemove.mock.calls
        .filter(([typ, fn]) => typ === 'keydown' && fn === meinListener);
      expect(abgemeldet.length).toBe(1);

      /* Gegenprobe im Test selbst: ein zweiter Druck laeuft an keinem
         Dialog-Listener mehr vorbei. */
      const ev2 = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
      document.dispatchEvent(ev2);
      expect(fremdHatGehoert).toBe(2);
    } finally {
      anRemove.mockRestore();
      document.removeEventListener('keydown', fremderListener);
    }
  });
});
