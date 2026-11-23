/* Karte #813: Fußzeile der Karte bricht um, Abzeichen brechen in sich nicht um.
   happy-dom wendet CSS nicht an — der Test liest src/style.css als Text. */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const css = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8');

function regel(selektor) {
  const esc = selektor.replace(/[.>]/g, m => '\\' + m);
  const m = css.match(new RegExp('(?:^|\\n)' + esc + '\\s*\\{([^}]*)\\}'));
  return m ? m[1] : null;
}

describe('#813 Karten-Fußzeile', () => {
  it('.card-footer ist Flex mit flex-wrap: wrap', () => {
    const r = regel('.card-footer');
    expect(r).not.toBeNull();
    expect(r).toMatch(/display:\s*flex/);
    expect(r).toMatch(/flex-wrap:\s*wrap/);
  });

  it('.card-footer hat einen Zeilenabstand (gap)', () => {
    expect(regel('.card-footer')).toMatch(/gap:/);
  });

  it('Abzeichen in der Fußzeile: nowrap, schrumpfen nicht, Ausreißer mit Ellipse', () => {
    const block = css.match(/(\.card-footer > [^{]+)\{([^}]*)\}/);
    expect(block).not.toBeNull();
    expect(block[2]).toMatch(/white-space:\s*nowrap/);
    expect(block[2]).toMatch(/flex-shrink:\s*0/);
    expect(block[2]).toMatch(/max-width:\s*100%/);
    expect(block[2]).toMatch(/text-overflow:\s*ellipsis/);
    for (const k of ['card-checklist-summary', 'card-prio-badge', 'card-flag-badge', 'card-effort-badge',
      'card-date-badge', 'card-link-badge', 'card-link-blocked-badge', 'card-comment-badge',
      'card-qa-badge', 'card-complexity-badge', 'card-count-badge']) {
      expect(block[1], k).toContain(`.card-footer > .${k}`);
    }
  });

  it('Abzeichen außerhalb der Liste kürzen sich selbst (Sprint-92-Kritiker KO-6)', () => {
    /* Bewusst nicht in der gemeinsamen Regel: card-owner-badge (eigene max-width
       mit Ellipse), card-assignees-cluster (Avatare, fest breit) und
       card-recurrence-badge (ein Emoji). Für den Owner wird die eigene Kürzung
       hier festgehalten, damit die Ausnahme belegt bleibt. */
    const bloecke = [...css.matchAll(/\.card-owner-badge\s*\{([^}]*)\}/g)].map(m => m[1]);
    expect(bloecke.some(b => /max-width:\s*\d+px/.test(b) && /text-overflow:\s*ellipsis/.test(b))).toBe(true);
  });

  it('keine spätere .card-footer-Regel nimmt den Umbruch zurück', () => {
    for (const m of css.matchAll(/(^|\})\s*\.card-footer\s*\{([^}]*)\}/g)) {
      expect(m[2]).not.toMatch(/flex-wrap:\s*nowrap/);
    }
  });

  it('Footer-Kinder werden nicht per margin-left:auto verschoben', () => {
    expect(css).not.toMatch(/\.card-footer[^{]*\{[^}]*margin-left:\s*auto/);
  });

  it('Cover- und Erledigt-Variante bleiben unverändert', () => {
    expect(css).toMatch(/\.card--has-cover \.card-footer\s*\{\s*color:\s*#f5f5f5;\s*\}/);
    expect(css).toMatch(/\.column\.is-done \.card-footer\s*\{\s*display:\s*none;\s*\}/);
  });
});
