/* Karte #847: Die umbrochene zweite Reiterzeile des Admin-Dialogs wurde abgeschnitten.
   Ursache: `.admin-tabbar` war Flex-Kind des Panels (column, max-height, overflow hidden) und
   hatte `overflow-x: auto` — das macht sie auch in y zum Scroll-Container (min-height 0),
   sodass sie schrumpfte. Fix: kein overflow an der Leiste, flex-shrink: 0.
   happy-dom rechnet kein Layout, deshalb prüft der Test die Stil-Eigenschaften. */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const js = readFileSync(join(process.cwd(), 'src', 'admin-dialog.js'), 'utf8');
const regel = (sel) => {
  const m = js.match(new RegExp('^' + sel.replace('.', '\\.') + ' \\{([^}]*)\\}', 'm'));
  return m ? m[1] : null;
};

describe('Admin-Reiterleiste bleibt vollständig sichtbar (#847)', () => {
  const bar = regel('.admin-tabbar');
  const body = regel('.admin-body');

  it('Regeln gefunden', () => {
    expect(bar).not.toBeNull();
    expect(body).not.toBeNull();
  });
  it('Leiste umbricht und schrumpft nicht', () => {
    expect(bar).toMatch(/flex-wrap:\s*wrap/);
    expect(bar).toMatch(/flex-shrink:\s*0/);
  });
  it('Leiste ist kein Scroll-Container (overflow würde in y abschneiden)', () => {
    expect(bar).not.toMatch(/overflow/);
  });
  it('Der Body scrollt weiter', () => {
    expect(body).toMatch(/overflow:\s*auto/);
  });
});
