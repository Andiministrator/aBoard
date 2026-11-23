/* llm-pii.test.js — Tests für den PII-Detektor (#301, ADR-0097).
 *
 * Nur im Server-Build-Test-Kontext aktiv (DCE-Gate: __SERVER_BUILD__).
 * Im Standalone-Kontext liefert llm-pii.js `LLMPii === null` → describe.skip. */

import { describe, it, expect, beforeEach } from 'vitest';
import { LLMPii } from '../src/llm-pii.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const describeOnServer = IS_SERVER_BUILD ? describe : describe.skip;

describeOnServer('LLMPii.detect — E-Mail', () => {
  it('findet einfache Adressen', () => {
    const f = LLMPii.detect('Bitte an hallo@example.com schreiben.');
    expect(f).toHaveLength(1);
    expect(f[0].type).toBe('email');
    expect(f[0].match).toBe('hallo@example.com');
  });

  it('findet mehrere Adressen mit Sonderzeichen', () => {
    const f = LLMPii.detect('a.b+test@sub.example.co.uk und x_y-z@foo.de');
    expect(f).toHaveLength(2);
    expect(f.map((x) => x.match)).toEqual([
      'a.b+test@sub.example.co.uk',
      'x_y-z@foo.de',
    ]);
  });

  it('ignoriert nicht-E-Mail-Inhalt', () => {
    const f = LLMPii.detect('Text ohne @ aber mit example.com nichts.');
    expect(f.filter((x) => x.type === 'email')).toEqual([]);
  });
});

describeOnServer('LLMPii.detect — IBAN (Mod97)', () => {
  it('findet gültige DE-IBAN', () => {
    /* Test-IBAN aus DE-Bundesbank-Beispiel: DE89 3704 0044 0532 0130 00 */
    const f = LLMPii.detect('Konto: DE89 3704 0044 0532 0130 00 — Verwendungszweck …');
    const ibans = f.filter((x) => x.type === 'iban');
    expect(ibans).toHaveLength(1);
    expect(ibans[0].match).toBe('DE89 3704 0044 0532 0130 00');
  });

  it('verwirft IBAN mit falscher Prüfsumme', () => {
    const f = LLMPii.detect('Fake: DE00 0000 0000 0000 0000 00');
    expect(f.filter((x) => x.type === 'iban')).toEqual([]);
  });

  it('Mod97 direkt: gültig vs. ungültig', () => {
    expect(LLMPii._checkMod97('DE89370400440532013000')).toBe(true);
    expect(LLMPii._checkMod97('DE89370400440532013001')).toBe(false);
    expect(LLMPii._checkMod97('AT611904300234573201')).toBe(true);
  });
});

describeOnServer('LLMPii.detect — Kreditkarte (Luhn)', () => {
  it('findet gültige Test-Visa', () => {
    /* 4111 1111 1111 1111 ist Visa-Test-Nummer, Luhn-gültig */
    const f = LLMPii.detect('Karte 4111 1111 1111 1111 bitte');
    const cc = f.filter((x) => x.type === 'creditcard');
    expect(cc).toHaveLength(1);
    expect(cc[0].match.replace(/\s/g, '')).toBe('4111111111111111');
  });

  it('verwirft ungültige Luhn', () => {
    const f = LLMPii.detect('Karte 4111 1111 1111 1112 bitte');
    expect(f.filter((x) => x.type === 'creditcard')).toEqual([]);
  });

  it('Luhn direkt', () => {
    expect(LLMPii._checkLuhn('4111111111111111')).toBe(true);
    expect(LLMPii._checkLuhn('4111111111111112')).toBe(false);
    expect(LLMPii._checkLuhn('5500000000000004')).toBe(true);
    expect(LLMPii._checkLuhn('123')).toBe(false); /* zu kurz */
  });

  it('keine Massen-False-Positives auf Logfile-Auszug (Kritiker S-P3)', () => {
    /* Vorher: "1 2 3 4 5 …".repeat(2000) → ~270 vermeintliche KK durch Luhn-Random-Hits.
       Nachher: 0 KK-Funde, da kein Vier-Stellen-Block + keine 13+-Ziffern-Sequenz. */
    const noise = '1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 '.repeat(200);
    const f = LLMPii.detect(noise);
    expect(f.filter((x) => x.type === 'creditcard')).toEqual([]);
  });

  it('findet KK mit Vier-Stellen-Blöcken', () => {
    const f = LLMPii.detect('Karte 4111-1111-1111-1111 ablegen');
    const cc = f.filter((x) => x.type === 'creditcard');
    expect(cc).toHaveLength(1);
  });
});

describeOnServer('LLMPii.detect — Steuer-ID DE', () => {
  it('akzeptiert valides Beispiel (BZSt-Stil: 9 dreifach, Prüfziffer 9)', () => {
    /* 6592997048|9 — erste 10 erfüllen die Wiederholungs-Regel (9 ×3),
       letzte Ziffer ist die ISO-7064-Mod-11/10-Prüfziffer. */
    expect(LLMPii._checkTaxIdDE('65929970489')).toBe(true);
  });

  it('verwirft 11 zufällige Ziffern (alle unique)', () => {
    expect(LLMPii._checkTaxIdDE('12345678901')).toBe(false);
  });

  it('verwirft falsche Prüfziffer', () => {
    expect(LLMPii._checkTaxIdDE('65929970480')).toBe(false);
  });

  it('findet im Text', () => {
    const f = LLMPii.detect('Steuer-ID: 65929970489 für die Anmeldung.');
    const tax = f.filter((x) => x.type === 'taxid-de');
    expect(tax).toHaveLength(1);
  });
});

describeOnServer('LLMPii.detect — Telefon', () => {
  it('findet DE-Nummer mit 0', () => {
    const f = LLMPii.detect('Anruf unter 0151 12345678 bitte.');
    expect(f.filter((x) => x.type === 'phone').length).toBeGreaterThan(0);
  });

  it('findet intl +49', () => {
    const f = LLMPii.detect('+49 30 12345678 — Berlin');
    expect(f.filter((x) => x.type === 'phone').length).toBeGreaterThan(0);
  });
});

describeOnServer('LLMPii.detect — Geburtsdatum', () => {
  it('findet DE-Format DD.MM.YYYY', () => {
    const f = LLMPii.detect('geboren 03.07.1985 in Berlin');
    expect(f.filter((x) => x.type === 'birthdate')).toHaveLength(1);
  });

  it('findet ISO YYYY-MM-DD', () => {
    const f = LLMPii.detect('DOB 1985-07-03 entered');
    expect(f.filter((x) => x.type === 'birthdate')).toHaveLength(1);
  });

  it('verwirft 1899-12-31 (unplausibel früh)', () => {
    /* Regex matched nicht (19|20)\d{2} für 1899 - nur ein Sanity-Check, ohne expected Match. */
    const f = LLMPii.detect('Stamm 1899-12-31 — Notiz');
    expect(f.filter((x) => x.type === 'birthdate')).toEqual([]);
  });
});

describeOnServer('LLMPii.detect — Überlappung + Sortierung', () => {
  it('liefert positionssortiert', () => {
    const f = LLMPii.detect('zweite a@b.de erste vor 0151 1234567 dritte');
    /* Sortierung nach start. */
    for (let i = 1; i < f.length; i++) {
      expect(f[i].start).toBeGreaterThanOrEqual(f[i - 1].start);
    }
  });

  it('IDs sind stabil (type-start)', () => {
    const f = LLMPii.detect('a@b.de');
    expect(f[0].id).toBe(`email-${f[0].start}`);
  });
});

describeOnServer('LLMPii.apply — Default-Aktion redact', () => {
  it('redactet alles ohne actionsById', () => {
    const text = 'Mail an a@b.de senden.';
    const f = LLMPii.detect(text);
    const out = LLMPii.apply(text, f, {});
    expect(out.text).toBe('Mail an <REDACTED> senden.');
    expect(out.counts.redacted).toBe(1);
    expect(out.counts.kept).toBe(0);
    expect(out.counts.placeholdered).toBe(0);
  });

  it('keep belässt unverändert', () => {
    const text = 'Mail an a@b.de senden.';
    const f = LLMPii.detect(text);
    const out = LLMPii.apply(text, f, { [f[0].id]: 'keep' });
    expect(out.text).toBe(text);
    expect(out.counts.kept).toBe(1);
  });
});

describeOnServer('LLMPii.apply — Platzhalter-Determinismus', () => {
  it('gleicher Match → gleicher Index', () => {
    const text = 'an a@b.de und nochmal a@b.de schicken';
    const f = LLMPii.detect(text);
    expect(f).toHaveLength(2);
    const acts = { [f[0].id]: 'placeholder', [f[1].id]: 'placeholder' };
    const out = LLMPii.apply(text, f, acts);
    expect(out.text).toBe('an [E-Mail #1] und nochmal [E-Mail #1] schicken');
    expect(out.counts.placeholdered).toBe(2);
  });

  it('verschiedene Matches → fortlaufende Indizes pro Typ', () => {
    const text = 'a@b.de und x@y.de';
    const f = LLMPii.detect(text);
    const acts = { [f[0].id]: 'placeholder', [f[1].id]: 'placeholder' };
    const out = LLMPii.apply(text, f, acts);
    expect(out.text).toBe('[E-Mail #1] und [E-Mail #2]');
  });

  it('verschiedene Typen → eigene Counter', () => {
    const text = 'Mail a@b.de Kreditkarte 4111 1111 1111 1111';
    const f = LLMPii.detect(text);
    const acts = {};
    for (const x of f) acts[x.id] = 'placeholder';
    const out = LLMPii.apply(text, f, acts);
    expect(out.text).toContain('[E-Mail #1]');
    expect(out.text).toContain('[Kreditkarte #1]');
  });
});

describeOnServer('LLMPii.buildPrompt — server-äquivalente Serialisierung', () => {
  const card = {
    displayId: 42,
    title: 'Demo',
    description: 'erste Zeile\nzweite Zeile',
    comments: [
      { author: 'a', text: 'x', created: '2026-05-25' },
      { author: 'b', text: 'mehr\nzeilen', created: '2026-05-25' },
    ],
    links: [{ type: 'depends-on', targetId: 'tgt-1' }],
    agentContext: 'Kontext',
    activity: [{ ts: '2026-05-25T10:00:00Z', actor: 'a', action: 'card.move', from: 'col-ready', to: 'col-in-arbeit' }],
  };
  const refs = { 'tgt-1': { displayId: 7, title: 'Linked Card', snippet: 'kurz' } };

  it('Header mit #displayId + Titel', () => {
    const p = LLMPii.buildPrompt({ ...card, comments: [], links: [], activity: [] }, ['description'], refs);
    expect(p.startsWith('# Karte #42: Demo')).toBe(true);
  });

  it('Reihenfolge fest: description → comments → links → agentContext → activity', () => {
    const p = LLMPii.buildPrompt(card, ['description', 'comments', 'links', 'agentContext', 'activity'], refs);
    const iDesc = p.indexOf('## Beschreibung');
    const iCmt = p.indexOf('## Kommentare');
    const iLnk = p.indexOf('## Verweise');
    const iAg = p.indexOf('## Agent-Kontext');
    const iAct = p.indexOf('## Aktivität');
    expect(iDesc).toBeGreaterThanOrEqual(0);
    expect(iCmt).toBeGreaterThan(iDesc);
    expect(iLnk).toBeGreaterThan(iCmt);
    expect(iAg).toBeGreaterThan(iLnk);
    expect(iAct).toBeGreaterThan(iAg);
  });

  it('unbekannte Felder werden ignoriert', () => {
    const p = LLMPii.buildPrompt(card, ['description', 'bogus-field'], refs);
    expect(p).toContain('## Beschreibung');
    expect(p).not.toContain('bogus');
  });

  it('Kommentare mit Multi-line werden eingerückt', () => {
    const p = LLMPii.buildPrompt(card, ['comments'], refs);
    expect(p).toContain('mehr\n  zeilen');
  });

  it('Verweise: nach (type, targetId) sortiert', () => {
    const c = {
      ...card, comments: [], activity: [],
      links: [
        { type: 'related', targetId: 'b' },
        { type: 'depends-on', targetId: 'a' },
        { type: 'related', targetId: 'a' },
      ],
    };
    const r = {
      a: { displayId: 1, title: 'A' },
      b: { displayId: 2, title: 'B' },
    };
    const p = LLMPii.buildPrompt(c, ['links'], r);
    const iDepA = p.indexOf('depends-on: #1 "A"');
    const iRelA = p.indexOf('related: #1 "A"');
    const iRelB = p.indexOf('related: #2 "B"');
    expect(iDepA).toBeGreaterThan(0);
    expect(iRelA).toBeGreaterThan(iDepA);
    expect(iRelB).toBeGreaterThan(iRelA);
  });

  it('leere Karte → nur Header + trailing newline', () => {
    const p = LLMPii.buildPrompt({ title: 'T' }, ['description'], {});
    expect(p).toBe('# Karte: T\n');
  });
});

describeOnServer('LLMPii.hashText', () => {
  it('gleicher Text → gleicher Hash', () => {
    expect(LLMPii.hashText('abc')).toBe(LLMPii.hashText('abc'));
  });
  it('unterschiedlicher Text → unterschiedlicher Hash', () => {
    expect(LLMPii.hashText('abc')).not.toBe(LLMPii.hashText('abd'));
  });
  it('leer/null sicher', () => {
    expect(LLMPii.hashText('')).toBe(LLMPii.hashText(null));
  });
});

describeOnServer('LLMPii.openPreviewModal — Stale-Card-Warnung (F-396, Karte #378)', () => {
  /* Diese Tests treiben das DOM-Modal direkt (statt über LLMTab._onSend),
     damit die Stale-Warn-Fläche unabhängig vom Send-Pfad geprüft wird. */
  beforeEach(() => {
    document.body.innerHTML = '';
  });

  function clickSend(modal) {
    modal.querySelector('[data-act="send"]').dispatchEvent(new Event('click'));
  }

  it('unveränderte Karte → kein Warnungs-Element sichtbar, Send finalisiert direkt', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => 'Mail an a@b.de senden.', /* identisch */
    });
    const modal = document.querySelector('.pii-modal');
    const staleEl = modal.querySelector('#pii-stale-warn');
    expect(staleEl.style.display).toBe('none');
    clickSend(modal);
    const r = await p;
    expect(staleEl.style.display).toBe('none'); /* kein Fehlalarm */
    expect(r.action).toBe('send');
    expect(document.querySelector('.pii-overlay')).toBeFalsy(); /* Modal geschlossen */
  });

  /* ── Verify-Lauf zur Sprint-52-Nacharbeit: die Luecken, die dort
        auffielen — F-516 und F-517 waren behoben, aber ungetestet. ── */

  it('F-516: nach "Frischen Stand laden" erben positionsgleiche Treffer NICHT die alte Aktion', async () => {
    /* Die Finding-IDs sind positional (`typ-offset`). Setzt der Nutzer einen
       Treffer bewusst auf `keep` und steht im frischen Text an derselben
       Stelle eine ANDERE Adresse, ginge sie sonst unredigiert ans LLM —
       ohne dass der Nutzer sie je gesehen hat. */
    const alt  = 'Mail an aaa@b.de senden.';
    const neu  = 'Mail an zzz@b.de senden.';   /* gleiche Laenge → gleicher Offset */
    let frisch = alt;
    const p = LLMPii.openPreviewModal({
      systemPrompt: '', userPrompt: alt,
      getFreshUserPrompt: () => frisch,
    });
    const modal = document.querySelector('.pii-modal');

    /* Treffer auf `keep` stellen (die eigene Adresse, bewusst unredigiert). */
    /* Die Aktions-Buttons tragen keine data-Attribute; Reihenfolge ist
       keep / redact / placeholder — „Behalten" ist der erste. */
    const keepBtn = modal.querySelector('.pii-act button:first-child');
    expect(keepBtn.textContent).toBe('Behalten');
    keepBtn.dispatchEvent(new Event('click'));
    /* Belegen, dass der Klick gewirkt hat — sonst prueft der Test nichts. */
    expect(modal.querySelector('.pii-act button:first-child').className)
      .toContain('pii-act-active');

    frisch = neu;                       /* Karte wird extern geaendert */
    clickSend(modal);
    await Promise.resolve();
    modal.querySelector('[data-stale-act="reload"]').dispatchEvent(new Event('click'));

    clickSend(modal);
    const r = await p;
    /* Die NEUE Adresse darf nicht im Klartext rausgehen. */
    expect(r.userPrompt).not.toContain('zzz@b.de');
  });

  it('F-517: geloeschte Karte → Warnung statt blindem Send', async () => {
    let resolved = false;
    const p = LLMPii.openPreviewModal({
      systemPrompt: '', userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => null,     /* Karte existiert nicht mehr */
    });
    p.then(() => { resolved = true; });
    const modal = document.querySelector('.pii-modal');
    const staleEl = modal.querySelector('#pii-stale-warn');

    clickSend(modal);
    await Promise.resolve();

    expect(staleEl.style.display).toBe('');
    expect(staleEl.dataset.grund).toBe('geloescht');
    expect(resolved).toBe(false);        /* kein blinder Send gegen eine Karte, die es nicht gibt */
    /* "Frischen Stand laden" ist hier sinnlos und daher ausgeblendet */
    expect(modal.querySelector('[data-stale-act="reload"]').style.display).toBe('none');

    modal.querySelector('[data-act="cancel"]').dispatchEvent(new Event('click'));
    await p;
  });

  it('F-517: Loeschung WAEHREND die Stale-Flaeche schon offen ist → Reload meldet das', async () => {
    /* Der Fund des Verify-Laufs: der null-Fall war nur im Send-Pfad
       behandelt. Klickte der Nutzer danach auf "Frischen Stand laden",
       passierte gar nichts — die Flaeche blieb in der alten Darstellung
       stecken, ohne Ausweg ausser Modal schliessen. */
    let frisch = 'Mail an GEAENDERT@b.de senden.';
    const p = LLMPii.openPreviewModal({
      systemPrompt: '', userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => frisch,
    });
    const modal = document.querySelector('.pii-modal');
    const staleEl = modal.querySelector('#pii-stale-warn');

    clickSend(modal);
    await Promise.resolve();
    expect(staleEl.style.display).toBe('');      /* "bearbeitet"-Fall */
    expect(staleEl.dataset.grund).toBeUndefined();

    frisch = null;                                /* jetzt wird geloescht */
    modal.querySelector('[data-stale-act="reload"]').dispatchEvent(new Event('click'));

    expect(staleEl.dataset.grund).toBe('geloescht');
    expect(modal.querySelector('[data-stale-act="reload"]').style.display).toBe('none');

    modal.querySelector('[data-act="cancel"]').dispatchEvent(new Event('click'));
    await p;
  });

  it('Karte ändert sich bei offenem Modal → Warnung erscheint, Send finalisiert NICHT sofort', async () => {
    let resolved = false;
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => 'Mail an ANDERE-ADRESSE@b.de senden.', /* geändert */
    });
    p.then(() => { resolved = true; });
    const modal = document.querySelector('.pii-modal');
    const staleEl = modal.querySelector('#pii-stale-warn');
    expect(staleEl.style.display).toBe('none');
    clickSend(modal);
    await Promise.resolve(); /* Microtask-Tick, falls fälschlich resolved */
    expect(staleEl.style.display).toBe('');
    expect(resolved).toBe(false); /* kein hartes Blockieren, aber auch kein Silent-Send */
    /* Modal bleibt offen — aufräumen für nächsten Test */
    modal.querySelector('[data-act="cancel"]').dispatchEvent(new Event('click'));
    await p;
  });

  it('"Trotzdem senden" nach Warnung → sendet den bereits geprüften alten Stand', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => 'Mail an ANDERE-ADRESSE@b.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    clickSend(modal);
    await Promise.resolve();
    modal.querySelector('[data-stale-act="anyway"]').dispatchEvent(new Event('click'));
    const r = await p;
    expect(r.action).toBe('send');
    /* redact ist Default-Aktion — a@b.de (aus dem ALTEN Stand) wurde ersetzt */
    expect(r.userPrompt).toBe('Mail an <REDACTED> senden.');
  });

  it('"Frischen Stand laden" → Preview aktualisiert sich, zweiter Send-Klick sendet den NEUEN Stand', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
      getFreshUserPrompt: () => 'Mail an c@d.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    const staleEl = modal.querySelector('#pii-stale-warn');
    clickSend(modal);
    await Promise.resolve();
    expect(staleEl.style.display).toBe('');
    modal.querySelector('[data-stale-act="reload"]').dispatchEvent(new Event('click'));
    expect(staleEl.style.display).toBe('none');
    /* Preview zeigt jetzt den frischen Text */
    expect(modal.querySelector('#pii-preview').textContent).toContain('c@d.de');
    expect(modal.querySelector('#pii-preview').textContent).not.toContain('a@b.de');
    /* Zweiter Send-Klick: getFreshUserPrompt liefert weiterhin den gleichen
       (jetzt übernommenen) Stand → kein erneuter Mismatch. */
    clickSend(modal);
    const r = await p;
    expect(r.action).toBe('send');
    expect(r.userPrompt).toBe('Mail an <REDACTED> senden.');
  });

  it('ohne getFreshUserPrompt-Callback → kein Stale-Check, Send finalisiert wie zuvor', async () => {
    const p = LLMPii.openPreviewModal({
      systemPrompt: '',
      userPrompt: 'Mail an a@b.de senden.',
    });
    const modal = document.querySelector('.pii-modal');
    clickSend(modal);
    const r = await p;
    expect(r.action).toBe('send');
  });
});

describeOnServer('LLMPii.apply — gemischte Aktionen', () => {
  it('keep + redact + placeholder kombiniert', () => {
    const text = 'a@b.de und c@d.de und e@f.de';
    const f = LLMPii.detect(text);
    expect(f).toHaveLength(3);
    const acts = {
      [f[0].id]: 'keep',
      [f[1].id]: 'redact',
      [f[2].id]: 'placeholder',
    };
    const out = LLMPii.apply(text, f, acts);
    expect(out.text).toBe('a@b.de und <REDACTED> und [E-Mail #1]');
    expect(out.counts.kept).toBe(1);
    expect(out.counts.redacted).toBe(1);
    expect(out.counts.placeholdered).toBe(1);
  });

  it('leere Findings → unveränderter Text', () => {
    const out = LLMPii.apply('reiner Text ohne PII', [], {});
    expect(out.text).toBe('reiner Text ohne PII');
    expect(out.counts).toEqual({ kept: 0, redacted: 0, placeholdered: 0 });
  });

  it('null-/leere Eingaben sicher', () => {
    expect(LLMPii.detect(null)).toEqual([]);
    expect(LLMPii.detect('')).toEqual([]);
    expect(LLMPii.apply(null, [], {}).text).toBe('');
  });
});
