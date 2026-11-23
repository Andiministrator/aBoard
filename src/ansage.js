/* Eine Live-Region für kurze Hinweise an Screenreader (F-2862, Sitzungs-Kritik
   02.11.2026 K-3). Screenreader sagen zuverlässig nur Änderungen an einer
   Region an, die schon vorher im DOM steht — ein frisch erzeugtes Toast mit
   role=status wird von manchen (VoiceOver) übergangen. Deshalb entsteht die
   Region beim Laden des Moduls, also vor dem ersten Hinweis, und bleibt
   dasselbe Element (wie in vorschlaege-aussen.js). Das sichtbare Toast ist
   aria-hidden, damit nichts doppelt angesagt wird. */
const region = document.createElement('div');
region.className = 'visually-hidden';
region.setAttribute('role', 'status');
document.body.appendChild(region);

/* Mehrere Ansagen im selben Takt (zwei Banner im selben Render) hängen sich
   aneinander; sonst überschreibt die zweite die erste, bevor ein Screenreader
   sie liest (F-2960). Der Takt endet mit dem nächsten Mikrotask. */
let imTakt = false;

export function ansagen(text) {
  /* Wieder einhängen, falls jemand den body geleert hat (Verify N-6). */
  if (!region.isConnected) document.body.appendChild(region);
  if (imTakt) { region.textContent = region.textContent.trimEnd() + ' ' + text; return; }
  imTakt = true;
  queueMicrotask(() => { imTakt = false; });
  /* Gleicher Text zweimal: ohne Änderung keine Ansage — geschütztes
     Leerzeichen anhängen, damit sich der Inhalt ändert (Verify N-5). */
  region.textContent = region.textContent === text ? text + ' ' : text;
}
