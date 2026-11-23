/* Karte #728: Das injizierte Admin-CSS nutzt nur Design-Variablen, die es gibt.
   Liest beide Quelltexte (src/admin-dialog.js, src/style.css) und prüft:
   1. jede verwendete var(--x) ist in style.css oder im Admin-CSS selbst definiert,
   2. keine festen Farbwerte außerhalb der erlaubten Ausnahme (--admin-err),
   3. kein Fallback in var(…, …) (verdeckt Tippfehler, F-436). */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const SRC = join(process.cwd(), 'src');

const js = readFileSync(join(SRC, 'admin-dialog.js'), 'utf8');
const themeCss = readFileSync(join(SRC, 'style.css'), 'utf8');
const a = js.indexOf('const css = `\n') + 'const css = `\n'.length;
const adminCss = js.slice(a, js.indexOf('`;\n    const style = document.createElement', a));

/* K-2 (Sprint 85): nur Definitionen in Deklarationsposition, Kommentare vorher entfernt —
   sonst zählt ein Kommentar-Text „--x:“ als Definition. */
const definiert = (css) => new Set([...css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/[{;]\s*(--[a-z0-9-]+)\s*:/gi)].map(m => m[1]));

describe('Admin-Dialog nutzt die Design-Variablen (#728)', () => {
  it('K-2 Selbsttest: Kommentar-Text „--x:“ ist keine Definition, echte Deklaration schon', () => {
    expect(definiert('/* Ausnahme `--x:` */ .a { color: var(--x); }').has('--x')).toBe(false);
    expect(definiert('.a { --x: red; }').has('--x')).toBe(true);
    expect(definiert(':root {\n --x: red;\n --y: blue; }').has('--y')).toBe(true);
  });

  it('Admin-CSS wurde gefunden', () => {
    expect(adminCss.length).toBeGreaterThan(1000);
    expect(adminCss).toContain('.admin-panel');
  });

  it('jede verwendete var(--x) ist definiert', () => {
    const bekannt = new Set([...definiert(themeCss), ...definiert(adminCss)]);
    const benutzt = [...adminCss.matchAll(/var\(\s*(--[a-z0-9-]+)/gi)].map(m => m[1]);
    expect(benutzt.length).toBeGreaterThan(20);
    const fehlend = [...new Set(benutzt)].filter(n => !bekannt.has(n));
    expect(fehlend).toEqual([]);
  });

  it('kein Fallback in var()', () => {
    expect(adminCss.match(/var\([^)]*,/g)).toBeNull();
  });

  it('keine festen Farben außer der --admin-err-Definition', () => {
    const ohneKommentar = adminCss.replace(/\/\*[\s\S]*?\*\//g, '');
    const ohneAusnahme = ohneKommentar.split('\n').filter(z => !z.includes('--admin-err:')).join('\n');
    expect(ohneAusnahme.match(/#[0-9a-f]{3,8}\b|rgba?\(/gi)).toBeNull();
  });

  it('Knöpfe tragen die globalen Klassen .btn / .btn-primary', () => {
    expect(js).toContain("cls: 'btn btn-primary admin-btn admin-btn--primary'");
    expect(js).toContain("cls: 'btn admin-btn'");
  });
});
