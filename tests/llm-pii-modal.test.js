/* llm-pii-modal.test.js — DOM-Tests für LLMPii.openPreviewModal (Karte #402, F-401).
 *
 * tests/llm-pii.test.js deckt detect/apply/buildPrompt + die Stale-Card-Warnung
 * (F-396) ab, aber KEINEN der fünf Modal-Kernpfade:
 *   1. Highlight-Render   — Funde als .pii-hit-Spans im Preview
 *   2. Klick-Action-Cycling — Klick auf einen Treffer zyklt die Aktion
 *   3. Bulk-Buttons        — "Alle X" wirkt auf ALLE Funde
 *   4. Token-Update        — die Token-Schätzung folgt den gewählten Aktionen
 *   5. _finish-Pfad        — was am Ende tatsächlich in { action, systemPrompt,
 *                            userPrompt } hinausgeht (Send vs. Cancel)
 *
 * Nur im Server-Build-Test-Kontext aktiv (DCE-Gate: __SERVER_BUILD__) — Nutzung
 * des zentralen Helfers aus ADR-0116 statt eigener typeof-Zeilen. */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { LLMPii } from '../src/llm-pii.js';
import { describeServer } from './helpers/build-flags.js';

beforeEach(() => {
  document.body.innerHTML = '';
});

function clickCancel(modal) {
  modal.querySelector('[data-act="cancel"]').dispatchEvent(new Event('click'));
}

function clickSend(modal) {
  modal.querySelector('[data-act="send"]').dispatchEvent(new Event('click'));
}

/* Klick auf einen Treffer-Span im Preview läuft über Event-Delegation auf
   dem Preview-Container (Zeile 612 in llm-pii.js) — braucht ein bubbelndes
   Event, sonst kommt der Klick beim Listener nie an. */
function clickHit(hitEl) {
  hitEl.dispatchEvent(new Event('click', { bubbles: true }));
}

/* ── Pfad 1: Highlight-Render ────────────────────────────────────────── */

describeServer('LLMPii.openPreviewModal — Highlight-Render (Pfad 1)', () => {
  it('rendert für jeden Fund einen .pii-hit-Span mit dem Fund-Text', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: 'Kontakt sys@firma.de',
      userPrompt: 'Antwort an user@karte.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    const hits = [...modal.querySelectorAll('#pii-preview .pii-hit')];
    expect(hits).toHaveLength(2);
    expect(hits.map((h) => h.textContent)).toEqual(['sys@firma.de', 'user@karte.de']);
    /* Default-Aktion ist 'redact' (DEFAULT_ACTION) — muss sich auch optisch
       als Highlight-Klasse zeigen, nicht nur im internen State. */
    for (const h of hits) expect(h.className).toContain('pii-hit-redact');
    clickCancel(modal);
    await p;
  });

  it('trennt System- und User-Funde per <hr> im Preview (Scope-Reihenfolge)', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: 'Kontakt sys@firma.de',
      userPrompt: 'Antwort an user@karte.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    const preview = modal.querySelector('#pii-preview');
    const nodes = [...preview.childNodes];
    const hrIdx = nodes.indexOf(preview.querySelector('hr'));
    const sysHit = [...preview.querySelectorAll('.pii-hit')].find((h) => h.textContent === 'sys@firma.de');
    const usrHit = [...preview.querySelectorAll('.pii-hit')].find((h) => h.textContent === 'user@karte.de');
    expect(nodes.indexOf(sysHit)).toBeLessThan(hrIdx);
    expect(nodes.indexOf(usrHit)).toBeGreaterThan(hrIdx);
    clickCancel(modal);
    await p;
  });
});

/* ── Pfad 2: Klick-Action-Cycling ────────────────────────────────────── */

describeServer('LLMPii.openPreviewModal — Klick-Action-Cycling (Pfad 2)', () => {
  it('Klick auf einen Treffer zyklt redact → placeholder → keep → redact', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Kontakt user@karte.de bitte.',
    });
    const modal = document.querySelector('.pii-modal');
    const hit = () => modal.querySelector('#pii-preview .pii-hit');

    expect(hit().className).toContain('pii-hit-redact'); /* Default */
    clickHit(hit());
    expect(hit().className).toContain('pii-hit-placeholder');
    clickHit(hit());
    expect(hit().className).not.toMatch(/pii-hit-(redact|placeholder)/); /* jetzt 'keep' */
    clickHit(hit());
    expect(hit().className).toContain('pii-hit-redact'); /* Zyklus geschlossen */

    clickCancel(modal);
    await p;
  });

  it('der Aktions-Button in der Fund-Liste synchronisiert sich mit dem Klick-Zyklus', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Kontakt user@karte.de bitte.',
    });
    const modal = document.querySelector('.pii-modal');
    const btn = (label) => [...modal.querySelectorAll('.pii-act button')].find((b) => b.textContent === label);

    expect(btn('Redact').className).toContain('pii-act-active');
    clickHit(modal.querySelector('#pii-preview .pii-hit'));
    expect(btn('Platzhalter').className).toContain('pii-act-active');
    expect(btn('Redact').className).not.toContain('pii-act-active');

    clickCancel(modal);
    await p;
  });
});

/* ── Pfad 3: Bulk-Buttons ────────────────────────────────────────────── */

describeServer('LLMPii.openPreviewModal — Bulk-Buttons (Pfad 3)', () => {
  const text = 'a@b.de, c@d.de und e@f.de';

  it('"Alle behalten" wirkt auf ALLE Funde, nicht nur den ersten', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: text });
    const modal = document.querySelector('.pii-modal');
    expect(modal.querySelectorAll('#pii-preview .pii-hit')).toHaveLength(3);

    modal.querySelector('[data-bulk="keep"]').dispatchEvent(new Event('click'));
    const hits = [...modal.querySelectorAll('#pii-preview .pii-hit')];
    expect(hits).toHaveLength(3);
    for (const h of hits) expect(h.className).not.toMatch(/pii-hit-(redact|placeholder)/);

    clickSend(modal);
    const r = await p;
    expect(r.userPrompt).toBe(text); /* unverändert, da alle 'keep' */
  });

  it('"Alle Platzhalter" wirkt auf ALLE Funde mit korrekten Typ-Indizes', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: text });
    const modal = document.querySelector('.pii-modal');

    modal.querySelector('[data-bulk="placeholder"]').dispatchEvent(new Event('click'));
    const hits = [...modal.querySelectorAll('#pii-preview .pii-hit')];
    expect(hits.every((h) => h.className.includes('pii-hit-placeholder'))).toBe(true);

    clickSend(modal);
    const r = await p;
    expect(r.userPrompt).toBe('[E-Mail #1], [E-Mail #2] und [E-Mail #3]');
  });
});

/* ── Pfad 4: Token-Update ────────────────────────────────────────────── */

describeServer('LLMPii.openPreviewModal — Token-Update (Pfad 4)', () => {
  const userPrompt = 'Kontakt a@b.de bitte.';
  /* Deterministischer Zähler statt _defaultEstimate (chars/4, ceil) — macht
     die erwarteten Werte im Test exakt nachrechenbar. */
  const estimateTokens = (s) => (s || '').length;

  it('Token-Anzeige spiegelt vorher/nachher gemäß aktuell gewählten Aktionen', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt, estimateTokens });
    const modal = document.querySelector('.pii-modal');
    const before = estimateTokens('\n' + userPrompt);
    const after = estimateTokens('\n' + userPrompt.replace('a@b.de', '<REDACTED>'));
    const delta = after - before;
    expect(modal.querySelector('#pii-tokens').textContent).toBe(
      `Token-Schätzung: vorher ${before} → nachher ${after} (${delta >= 0 ? '+' : ''}${delta})`,
    );
    clickCancel(modal);
    await p;
  });

  it('Token-Anzeige aktualisiert sich nach Bulk-Aktion (Delta wird neu berechnet)', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt, estimateTokens });
    const modal = document.querySelector('.pii-modal');
    modal.querySelector('[data-bulk="keep"]').dispatchEvent(new Event('click'));
    const before = estimateTokens('\n' + userPrompt);
    /* 'keep' lässt den Text unverändert → vorher === nachher, Delta 0 */
    expect(modal.querySelector('#pii-tokens').textContent).toBe(
      `Token-Schätzung: vorher ${before} → nachher ${before} (+0)`,
    );
    clickCancel(modal);
    await p;
  });
});

/* ── Pfad 5: _finish (Send-/Cancel-Ausgabe) ──────────────────────────── */

describeServer('LLMPii.openPreviewModal — _finish-Pfad (Pfad 5)', () => {
  it('Send wendet die AKTUELL gewählten Aktionen an, nicht den Rohtext', async () => {
    /* Bewusst 'placeholder' statt 'keep' als Ziel-Aktion: 'keep' liefert
       zufällig denselben Text wie der (fehlerhaft) unveränderte Rohtext —
       eine Gegenprobe, die apply() beim Send abklemmt, würde das nicht
       auffangen. '[E-Mail #1]' unterscheidet sich sowohl vom Rohtext als
       auch vom Default ('redact' → '<REDACTED>'). */
    const userPrompt = 'Kontakt a@b.de bitte.';
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt });
    const modal = document.querySelector('.pii-modal');
    clickHit(modal.querySelector('#pii-preview .pii-hit')); /* redact -> placeholder */

    clickSend(modal);
    const r = await p;
    expect(r.action).toBe('send');
    expect(r.userPrompt).toBe('Kontakt [E-Mail #1] bitte.');
  });

  it('Cancel liefert die unveränderten Rohdaten — der Aufrufer wertet sie nie aus', async () => {
    /* llm-tab.js prüft `r.action !== 'send'` und kehrt sofort zurück — der
       Rohtext bei Cancel wird also nie versendet. Dieser Test hält trotzdem
       fest, WAS _doFinish bei 'cancel' tatsächlich liefert: keine apply()
       -Anwendung (siehe llm-pii.js:669-673), anders als beim Send-Zweig. */
    const userPrompt = 'Kontakt a@b.de bitte.';
    const systemPrompt = 'Sys sys@firma.de';
    const p = LLMPii.openPreviewModal({ systemPrompt, userPrompt });
    const modal = document.querySelector('.pii-modal');
    clickCancel(modal);
    const r = await p;
    expect(r.action).toBe('cancel');
    expect(r.userPrompt).toBe(userPrompt);
    expect(r.systemPrompt).toBe(systemPrompt);
  });

  it('F-396-Frische: nach "Frischen Stand laden" sendet der zweite Klick den NEUEN Text mit den (zurückgesetzten) Default-Aktionen', async () => {
    /* Ergänzt die bestehenden Stale-Card-Tests in llm-pii.test.js um die
       Verbindung zum _finish-Ergebnis: nicht nur die Preview zeigt den
       frischen Text (dort bereits geprüft), auch das AUSGEHENDE
       userPrompt im Promise-Resolve muss der frische, korrekt maskierte
       Stand sein. */
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => 'Mail an c@d.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    clickSend(modal);
    await Promise.resolve();
    modal.querySelector('[data-stale-act="reload"]').dispatchEvent(new Event('click'));
    clickSend(modal);
    const r = await p;
    expect(r.action).toBe('send');
    expect(r.userPrompt).toBe('Mail an <REDACTED> senden.');
    expect(r.userPrompt).not.toContain('c@d.de');
  });
});

/* ── Pfad 6: Modal-Shell-Migration (#348, ADR-0118) ──────────────────────
 * Vorher hatte dieser Dialog ALS EINZIGER der acht Server-Einstellungs-
 * dialoge KEINEN Focus-Trap und KEINEN Close-Button. Diese Suite prüft die
 * neuen Zusagen — UND dass die absichtliche Abweichung von den sieben anderen
 * migrierten Dialogen hält: kein Klick-außerhalb-Schliessen (eine laufende
 * Redaktionsentscheidung darf nicht durch einen Fehlklick verworfen werden,
 * siehe Kopfkommentar in src/llm-pii.js). */

describeServer('LLMPii.openPreviewModal — Modal-Shell-Migration (#348)', () => {
  it('Panel trägt role/aria-modal/aria-labelledby auf den Titel-Header', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: '' });
    const modal = document.querySelector('.pii-modal');
    expect(modal.getAttribute('role')).toBe('dialog');
    expect(modal.getAttribute('aria-modal')).toBe('true');
    const labelId = modal.getAttribute('aria-labelledby');
    expect(labelId).toBeTruthy();
    expect(document.getElementById(labelId)?.textContent).toBe('PII-Vorschau vor Senden');
    clickCancel(modal);
    await p;
  });

  it('Overlay trägt die Modal-Shell-Basisklasse und öffnet mit aria-hidden=false', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: '' });
    const overlay = document.querySelector('.pii-overlay');
    expect(overlay.classList.contains('modal-overlay')).toBe(true);
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true);
    expect(overlay.getAttribute('aria-hidden')).toBe('false');
    clickCancel(document.querySelector('.pii-modal'));
    await p;
  });

  it('neuer Close-Button (✕) im Header schliesst wie Cancel — löst das Promise mit action:"cancel" auf (vorher gab es keinen)', async () => {
    const userPrompt = 'Kontakt a@b.de bitte.';
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt });
    const modal = document.querySelector('.pii-modal');
    const closeBtn = modal.querySelector('.btn-modal-close');
    expect(closeBtn).toBeTruthy();
    closeBtn.click();
    const r = await p;
    expect(r.action).toBe('cancel');
    expect(r.userPrompt).toBe(userPrompt);
  });

  it('ESC schliesst wie Cancel — läuft jetzt über die Modal-Shell statt eines eigenen keydown-Listeners', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: 'x' });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    const r = await p;
    expect(r.action).toBe('cancel');
  });

  it('Klick auf den Overlay-Hintergrund schliesst NICHT (bewusste Abweichung von den sieben anderen migrierten Dialogen)', async () => {
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: 'x' });
    const overlay = document.querySelector('.pii-overlay');
    overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    /* Dialog bleibt offen — kein Resolve, kein aria-hidden-Wechsel. */
    expect(overlay.getAttribute('aria-hidden')).toBe('false');
    expect(document.querySelector('.pii-modal')).toBeTruthy();
    clickCancel(document.querySelector('.pii-modal'));
    await p;
  });

  it('Focus-Trap: Tab am letzten fokussierbaren Element springt zum ersten (vorher NICHT vorhanden)', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: 'Kontakt sys@firma.de',
      userPrompt: 'Antwort an user@karte.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    const focusable = [...modal.querySelectorAll('button')]
      .filter((el) => !el.disabled && el.offsetParent !== null);
    expect(focusable.length).toBeGreaterThan(1);
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    last.focus();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(first);
    clickCancel(modal);
    await p;
  });

  it('Focus-Restore: schliesst der Dialog, kehrt der Fokus zum auslösenden Element zurück (vorher NICHT vorhanden)', async () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();

    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: 'x' });
    expect(document.activeElement).not.toBe(opener);
    clickCancel(document.querySelector('.pii-modal'));
    await p;
    expect(document.activeElement).toBe(opener);
  });

  it('Doppel-Klick auf Send bleibt reentrancy-sicher (Reentrancy-Guard überlebt die Migration)', async () => {
    /* F-751(a)-Kritiker-Fund: `expect(...).not.toThrow()` + `r.action === 'send'`
       prüfen den falschen Gürtel — beide Zusicherungen gelten AUCH ohne den
       `_done`-Guard in _doFinish (llm-pii.js:493f), weil (a) das zweite
       removeChild schon durch `if (modal.overlay.parentNode)` (:507)
       abgefangen wird und (b) ein Promise nur EINMAL resolved (der zweite
       resolve()-Aufruf ist ein stiller No-Op, kein Fehler). Zwei fremde
       Ursachen erfüllen die alte Zusicherung, ganz ohne den Guard.
       Gemessen: Guard entfernt → alle 19 Tests dieser Datei bleiben grün.
       Fix (Kritiker-Vorschlag, per Gegenprobe validiert): `getFreshUserPrompt`
       als Spy mit IDENTISCHEM Text übergeben (Hash passt → kein
       Stale-Warn-Abbruch, Send geht durch `_finish` → `_doFinish` bis zum
       Ende). `_finish` selbst hat einen EIGENEN `if (_done) return`-Guard
       (unverändert) — der greift aber nur, wenn `_doFinish` `_done` auch
       tatsächlich auf true setzt. Ohne diese Zuweisung ruft der zweite Klick
       `getFreshUserPrompt()` ein zweites Mal auf: `toHaveBeenCalledTimes(1)`
       unterscheidet die zwei Fälle, wo `.not.toThrow()` es nicht konnte. */
    const getFreshUserPrompt = vi.fn(() => 'x');
    const p = LLMPii.openPreviewModal({ systemPrompt: '', userPrompt: 'x', getFreshUserPrompt });
    const modal = document.querySelector('.pii-modal');
    const sendBtn = modal.querySelector('[data-act="send"]');
    expect(() => { sendBtn.dispatchEvent(new Event('click')); sendBtn.dispatchEvent(new Event('click')); }).not.toThrow();
    const r = await p;
    expect(r.action).toBe('send');
    expect(getFreshUserPrompt).toHaveBeenCalledTimes(1);
  });
});

/* ── F-758: Scroll-Position überlebt den Rebuild ─────────────────────────
 *
 * `.pii-preview` ist auf max-height:340px/overflow-y:auto begrenzt; jeder
 * Toggle-Klick baut den Knoten per innerHTML neu auf, und ein frisch befüllter
 * Knoten startet im Browser bei scrollTop 0. Wer in einem langen Prompt weit
 * unten eine Fundstelle abwählt, landete danach wieder ganz oben.
 *
 * happy-dom hat kein Layout: `scrollTop` ist dort eine schlichte Zahl, die von
 * `innerHTML = ''` NICHT zurückgesetzt wird. Ein Test, der einfach einen Wert
 * setzt und ihn danach wieder liest, wäre deshalb auch ohne den Fix grün — die
 * Fehlerklasse „Test, der nicht fehlschlagen kann". Stattdessen wird
 * `scrollTop` durch ein Accessor-Paar ersetzt, das jeden SCHREIBZUGRIFF
 * mitschreibt: die Zusage lautet „vor dem Rebuild lesen, danach zurückschreiben",
 * und genau das ist damit nachweisbar. */

function scrollSpion(el, stand) {
  let intern = stand;
  const geschrieben = [];
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => intern,
    set: (v) => { intern = v; geschrieben.push(v); },
  });
  return geschrieben;
}

describeServer('LLMPii.openPreviewModal — Scroll-Position beim Rebuild (F-758)', () => {
  it('schreibt die vorherige Scroll-Position von Preview UND Findings zurück', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Zeile mit a@b.de und noch eine mit c@d.de darin.',
    });
    const modal   = document.querySelector('.pii-modal');
    const preview = modal.querySelector('#pii-preview');
    const finds   = modal.querySelector('#pii-finds');

    const preSchreibungen  = scrollSpion(preview, 214);
    const findSchreibungen = scrollSpion(finds, 37);

    /* Toggle über einen Treffer-Klick — löst _renderAll() aus. */
    clickHit(modal.querySelector('#pii-preview .pii-hit'));

    expect(preSchreibungen).toContain(214);
    expect(findSchreibungen).toContain(37);

    clickCancel(document.querySelector('.pii-modal'));
    await p;
  });

  it('auch der Aktions-Button im Findings-Block (zweiter _renderAll-Pfad) erhält die Position', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Kontakt a@b.de bitte.',
    });
    const modal   = document.querySelector('.pii-modal');
    const preview = modal.querySelector('#pii-preview');
    const preSchreibungen = scrollSpion(preview, 99);

    modal.querySelector('#pii-finds .pii-act button').dispatchEvent(new Event('click', { bubbles: true }));

    expect(preSchreibungen).toContain(99);

    clickCancel(document.querySelector('.pii-modal'));
    await p;
  });
});
