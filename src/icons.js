/* Gemeinsame SVG-Icons via DOM (kein innerHTML — ADR-0009) */

const NS = 'http://www.w3.org/2000/svg';

function svgIcon(paths, size = 13) {
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', size); svg.setAttribute('height', size);
  svg.setAttribute('fill', 'none'); svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2'); svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round'); svg.setAttribute('aria-hidden', 'true');
  paths.forEach(d => { const p = document.createElementNS(NS, 'path'); p.setAttribute('d', d); svg.appendChild(p); });
  return svg;
}

export function trashIcon(size = 13) {
  return svgIcon([
    'M3 6h18',
    'M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6',
    'M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2',
    'M10 11v6', 'M14 11v6',
  ], size);
}

/* Karte #154: Karten-Stapel-Icon für Sprint-Header-Counter (statt 🗂️-Emoji).
   Zwei versetzte Rechtecke, currentColor — passt sich an Counter-Textfarbe an. */
export function cardsIcon(size = 13) {
  return svgIcon([
    'M7 4h13v13',          /* hintere Karte: oben + rechts */
    'M4 7h13v13h-13z',     /* vordere Karte: vollständiges Rechteck */
  ], size);
}

/* ── Markdown-Toolbar-Icons ────────────────────────────────────── */

export function boldIcon(size = 14) {
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', size); svg.setAttribute('height', size);
  svg.setAttribute('fill', 'none'); svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2.5'); svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round'); svg.setAttribute('aria-hidden', 'true');
  /* B-Form: Stiel links, zwei Bögen nach rechts */
  const p1 = document.createElementNS(NS, 'path');
  p1.setAttribute('d', 'M6 4h8a4 4 0 0 1 0 8H6z');
  const p2 = document.createElementNS(NS, 'path');
  p2.setAttribute('d', 'M6 12h9a4 4 0 0 1 0 8H6z');
  svg.appendChild(p1); svg.appendChild(p2);
  return svg;
}

export function italicIcon(size = 14) {
  return svgIcon(['M19 4h-9', 'M14 20H5', 'M15 4 9 20'], size);
}

export function codeIcon(size = 14) {
  return svgIcon(['M16 18 22 12 16 6', 'M8 6 2 12 8 18'], size);
}

export function headingIcon(size = 14) {
  return svgIcon(['M4 12h16', 'M4 4v16', 'M20 4v16'], size);
}

export function listIcon(size = 14) {
  return svgIcon(['M8 6h13', 'M8 12h13', 'M8 18h13', 'M3 6h.01', 'M3 12h.01', 'M3 18h.01'], size);
}

export function orderedListIcon(size = 14) {
  return svgIcon(['M10 6h11', 'M10 12h11', 'M10 18h11', 'M4 6h1v4', 'M4 10H6', 'M6 18H4c0-1 2-2 2-3s-1-1.5-2-1'], size);
}

export function checklistIcon(size = 14) {
  return svgIcon(['M10 6h11', 'M10 12h11', 'M10 18h11', 'M4 6l1 1 2-2', 'M4 12l1 1 2-2', 'M4 18l1 1 2-2'], size);
}

export function linkIcon(size = 14) {
  return svgIcon([
    'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71',
    'M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71',
  ], size);
}

export function quoteIcon(size = 14) {
  return svgIcon([
    'M3 21c3 0 7-1 7-8V5c0-1.25-.756-2.017-2-2H4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2 1 0 1 0 1 1v1c0 1-1 2-2 2s-1 .008-1 1.031V20c0 1 0 1 1 1z',
    'M15 21c3 0 7-1 7-8V5c0-1.25-.757-2.017-2-2h-4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2h.75c0 2.25.25 4-2.75 4v3c0 1 0 1 1 1z',
  ], size);
}

export function hrIcon(size = 14) {
  return svgIcon(['M3 12h18'], size);
}

export function maximizeIcon(size = 14) {
  return svgIcon([
    'M8 3H5a2 2 0 0 0-2 2v3',
    'M21 8V5a2 2 0 0 0-2-2h-3',
    'M3 16v3a2 2 0 0 0 2 2h3',
    'M16 21h3a2 2 0 0 0 2-2v-3',
  ], size);
}

export function minimizeIcon(size = 14) {
  return svgIcon([
    'M8 3v3a2 2 0 0 1-2 2H3',
    'M21 8h-3a2 2 0 0 1-2-2V3',
    'M3 16h3a2 2 0 0 1 2 2v3',
    'M16 21v-3a2 2 0 0 1 2-2h3',
  ], size);
}

/* ── Attachment-MIME-Icons (ADR-0033) ─────────────────────────── */

export function attachmentIcon(size = 14) {
  return svgIcon([
    'M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48',
  ], size);
}

export function downloadIcon(size = 14) {
  return svgIcon([
    'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4',
    'M7 10l5 5 5-5',
    'M12 15V3',
  ], size);
}

export function imageIcon(size = 32) {
  const svg = svgIcon([
    'M3 5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
    'M21 15l-5-5L5 21',
  ], size);
  const c = document.createElementNS(NS, 'circle');
  c.setAttribute('cx', '8.5'); c.setAttribute('cy', '8.5'); c.setAttribute('r', '1.5');
  svg.appendChild(c);
  return svg;
}

export function pdfIcon(size = 32) {
  const svg = svgIcon([
    'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z',
    'M14 2v6h6',
  ], size);
  const txt = document.createElementNS(NS, 'text');
  txt.setAttribute('x', '12'); txt.setAttribute('y', '17');
  txt.setAttribute('text-anchor', 'middle');
  txt.setAttribute('font-size', '5'); txt.setAttribute('font-weight', '700');
  txt.setAttribute('fill', 'currentColor'); txt.setAttribute('stroke', 'none');
  txt.textContent = 'PDF';
  svg.appendChild(txt);
  return svg;
}

function fileWithLabel(label, size = 32) {
  const svg = svgIcon([
    'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z',
    'M14 2v6h6',
  ], size);
  const txt = document.createElementNS(NS, 'text');
  txt.setAttribute('x', '12'); txt.setAttribute('y', '17');
  txt.setAttribute('text-anchor', 'middle');
  txt.setAttribute('font-size', label.length > 3 ? '4' : '5'); txt.setAttribute('font-weight', '700');
  txt.setAttribute('fill', 'currentColor'); txt.setAttribute('stroke', 'none');
  txt.textContent = label;
  svg.appendChild(txt);
  return svg;
}

export function wordIcon(size = 32)     { return fileWithLabel('DOC',  size); }
export function excelIcon(size = 32)    { return fileWithLabel('XLS',  size); }
export function textIcon(size = 32)     { return fileWithLabel('TXT',  size); }
export function jsIcon(size = 32)       { return fileWithLabel('JS',   size); }
export function htmlIcon(size = 32)     { return fileWithLabel('HTML', size); }
export function jsonIcon(size = 32)     { return fileWithLabel('JSON', size); }
export function markdownIcon(size = 32) { return fileWithLabel('MD',   size); }
export function csvIcon(size = 32)      { return fileWithLabel('CSV',  size); }
export function archiveIcon(size = 32)  { return fileWithLabel('ZIP',  size); }
export function codeIcon32(size = 32)   { return fileWithLabel('CODE', size); }
export function genericFileIcon(size = 32) {
  return svgIcon([
    'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z',
    'M14 2v6h6',
  ], size);
}

/* MIME → Icon-Funktion. Bilder werden NICHT hier behandelt (Thumbnail aus data) */
export function mimeIconFor(mime, size = 32) {
  const m = (mime || '').toLowerCase();
  if (m === 'application/pdf') return pdfIcon(size);
  if (m === 'application/msword'
      || m === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document') return wordIcon(size);
  if (m === 'application/vnd.ms-excel'
      || m === 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet') return excelIcon(size);
  if (m === 'application/javascript' || m === 'text/javascript') return jsIcon(size);
  if (m === 'text/html')      return htmlIcon(size);
  if (m === 'application/json') return jsonIcon(size);
  if (m === 'text/markdown')  return markdownIcon(size);
  if (m === 'text/csv')       return csvIcon(size);
  if (m === 'application/zip' || m === 'application/x-tar' || m === 'application/gzip') return archiveIcon(size);
  if (m === 'text/plain')     return textIcon(size);
  if (m.startsWith('text/'))  return codeIcon32(size);
  return genericFileIcon(size);
}
