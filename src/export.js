/* Export-Modul — JSON-Export + KI-Kontext (→ ADR-0011) + Karten-Export (#292) */

import { I18n }     from './i18n.js';
import { Metrics }  from './metrics.js';
import { Markdown } from './markdown.js';
import { diffDays, todayISO } from './calendar/date-helpers.js';

function downloadText(filename, content, mimeType = 'text/plain;charset=utf-8') {
  const blob = new Blob([content], { type: mimeType });
  const url  = URL.createObjectURL(blob);
  const a    = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}

function toJSON(state) {
  const json = JSON.stringify({ version: state.version, board: state.board }, null, 2);
  const title = (state.board?.title || 'aboard').replace(/[^a-z0-9äöüß_-]/gi, '_');
  downloadText(`${title}.json`, json, 'application/json;charset=utf-8');
}

/* KI-Kontext-Export (→ ADR-0011) — F-52: Kommentare + qa eingeschlossen */
function buildKIKontext(state) {
  const board = state.board;
  const lines = [];
  const { columnDefs, sprints } = board;
  const isSwimLane = sprints.length > 1;

  lines.push(`# Board: ${board.title}`, '');

  /* ADR-0032: Verweise — Lookup-Tabelle (id → { card, sprint }) für O(1)-Zugriff */
  const cardIndex = new Map();
  sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => cardIndex.set(card.id, { card, sprint }));
  });

  const LINK_LABEL_DE = {
    'blocks':       'blockiert',
    'blocked-by':   'blockiert von',
    'depends-on':   'hängt ab von',
    'required-for': 'Voraussetzung für',
    'related':      'verwandt mit',
  };

  function renderLinksLine(card, currentSprint) {
    if (!Array.isArray(card.links) || card.links.length === 0) return null;
    const parts = [];
    /* Reihenfolge entsprechend Typen-Tabelle in ADR-0032 */
    const order = ['blocks', 'blocked-by', 'depends-on', 'required-for', 'related'];
    order.forEach(type => {
      const linksOfType = card.links.filter(l => l.type === type);
      linksOfType.forEach(link => {
        const tgt = cardIndex.get(link.targetId);
        if (!tgt) return; /* Verwaister Verweis — stillschweigend weglassen */
        const sprintSuffix = (tgt.sprint?.id !== currentSprint?.id && tgt.sprint?.id !== '__backlog__')
          ? ` (${tgt.sprint.title})`
          : '';
        const idStr = tgt.card.displayId != null ? `#${tgt.card.displayId}` : tgt.card.id.slice(0, 8);
        parts.push(`${LINK_LABEL_DE[type]} ${idStr}${sprintSuffix}`);
      });
    });
    return parts.length ? `Verweise: ${parts.join(', ')}` : null;
  }

  function renderCard(card, heading, out, sprintContext) {
    /* Hinweis: agentContext und activity werden hier bewusst nicht ausgegeben —
       agentContext ist ein internes Werkzeug (Claude-Arbeitsnotiz);
       activity ist ein PM-Audit-Log für Menschen, kein KI-Kontext (ADR-0037, F-110). */
    const idPrefix = card.displayId != null ? `#${card.displayId} ` : '';
    out.push(`${heading} ${idPrefix}${card.title}`);
    const colName = columnDefs.find(cd => cd.id === card.columnId)?.title ?? card.columnId ?? '?';
    const meta = [`Spalte: ${colName}`, `Priorität: ${card.priority}`];
    if (card.labels?.length) meta.push(`Labels: ${card.labels.join(', ')}`);
    if (card.milestone)      meta.push(`Milestone: ${card.milestone}`);
    if (card.session)        meta.push(`Session: ${card.session}`);
    if (card.owner)          meta.push(`Owner: ${card.owner}`);
    if (Array.isArray(card.assignees) && card.assignees.length) meta.push(`Assignees: ${card.assignees.join(', ')}`);
    if (card.complexity)     meta.push(`Komplexität: ${card.complexity}`);
    if (card.effort != null) meta.push(`Aufwand: ${card.effort}h`);
    if (card.qa)             meta.push('QA: ✓');
    if (card.critical)       meta.push('⚡ Kritisch');
    if (card.blocked)        meta.push('⊘ Blockiert');
    /* ADR-0034/F-140: Datumsfelder im KI-Export — Strings via I18n statt hartcodiert.
       Kalendertage statt Millisekunden-Differenz (DST-sicher, ADR-0103-Helfer). */
    if (card.dueDate) {
      const dueDiffDays = diffDays(todayISO(), card.dueDate);
      /* ADR-0138 §1: Uhrzeit hinter dem Datum */
      const due = card.dueTime ? `${card.dueDate} ${card.dueTime}` : card.dueDate;
      if (dueDiffDays !== null) {
        let rel;
        if (dueDiffDays < 0)        rel = `${I18n.t('date.ago-days', Math.abs(dueDiffDays))} ${I18n.t('date.overdue').toLowerCase()}`;
        else if (dueDiffDays === 0) rel = I18n.t('date.today');
        else                        rel = I18n.t('date.in-days', dueDiffDays);
        meta.push(`${I18n.t('date.due-prefix')} ${due} (${rel})`);
      } else {
        /* dueDate truthy aber nicht parsebar (sollte durch checkVersion/ADR-0034
           ausgeschlossen sein) — Datum trotzdem anzeigen, nur ohne Relativzeit. */
        meta.push(`${I18n.t('date.due-prefix')} ${due}`);
      }
    }
    if (card.startDate) meta.push(`${I18n.t('date.start-prefix')} ${card.startDate}${card.startTime ? ' ' + card.startTime : ''}`);
    /* ADR-0033: Anhang-Hinweis — Anzahl, keine Inhalte (Base64 würde den Export sprengen) */
    if (card.attachments?.length) meta.push(`Anhänge: ${card.attachments.length}`);
    out.push(meta.join(' | '));
    /* ADR-0032: Verweise als eigene Zeile (zwischen Meta und Description) */
    const linksLine = renderLinksLine(card, sprintContext);
    if (linksLine) out.push(linksLine);
    if (card.description?.trim()) {
      out.push('');
      out.push(card.description.trim());
    }
    if (card.comments?.length) {
      out.push('');
      out.push('**Kommentare:**');
      card.comments.forEach(c => {
        const who  = c.author ? ` ${c.author}` : '';
        const text = (c.text || '').replace(/\n/g, ' ');
        out.push(`- [${c.created}${who}] ${text}`);
      });
    }
    out.push('');
  }

  sprints.forEach(sprint => {
    if (isSwimLane) {
      let label;
      if (sprint.id === '__backlog__') {
        label = `## Backlog (Puffer — kein aktiver Sprint)`;
      } else {
        /* Karte #61: Velocity-Zusammenfassung im Sprint-Header (Meta-Karten ausgeschlossen) */
        /* F-1085: board mitgeben, damit isDoneColumnId() die echten columnDefs sieht */
        const m = Metrics.computeSprintMetrics(sprint, board);
        const velocityHint = m.hasEffortData
          ? ` — ${m.doneEffort}/${m.totalEffort}h erledigt (${m.doneCount}/${m.cardCount} Karten)`
          : ` — ${m.doneCount}/${m.cardCount} Karten`;
        label = `## Sprint: ${sprint.title}${velocityHint}`;
      }
      lines.push(label, '');

      /* ADR-0036: Sprint-Meta-Karten-Beschreibungen als semantischer Kontext */
      if (sprint.id !== '__backlog__') {
        const startCard = sprint.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'start');
        const endCard   = sprint.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'end');
        if (startCard?.description?.trim()) {
          lines.push(`### Sprint-Anweisungen (Start)`);
          lines.push('');
          lines.push(startCard.description.trim());
          lines.push('');
        }
        if (endCard?.description?.trim()) {
          lines.push(`### Sprint-Recap (Abschluss)`);
          lines.push('');
          lines.push(endCard.description.trim());
          lines.push('');
        }
      }
    }

    const colH  = isSwimLane ? '###' : '##';
    const cardH = isSwimLane ? '####' : '###';

    const activeColIds = new Set(columnDefs.map(cd => cd.id));

    columnDefs.forEach(colDef => {
      /* Karte #60: archivierte Karten + ADR-0036: Meta-Karten + ADR-0040: Templates vom KI-Kontext-Export ausschließen */
      const cards = sprint.cards.filter(c => c.columnId === colDef.id && !c.archived && c.kind !== 'sprint-meta' && !c.isTemplate);
      lines.push(`${colH} Spalte: ${colDef.title} (${cards.length} Karte${cards.length !== 1 ? 'n' : ''})`);
      lines.push('');

      if (cards.length === 0) {
        lines.push('*(leer)*', '');
      } else {
        cards.forEach(card => renderCard(card, cardH, lines, sprint));
      }
    });

    /* Karte #60: archivierte orphaned Karten + ADR-0036: Meta-Karten + ADR-0040: Templates ausschließen */
    const orphaned = sprint.cards.filter(c => !activeColIds.has(c.columnId) && !c.archived && c.kind !== 'sprint-meta' && !c.isTemplate);
    if (orphaned.length) {
      lines.push(`${colH} ⚠️ Orphaned Cards (${orphaned.length} — Spalte nicht mehr vorhanden)`);
      lines.push('');
      orphaned.forEach(card => {
        renderCard(card, cardH, lines, sprint);
        /* letzte Zeile durch Hinweis ersetzen */
        const safeColId = (card.columnId || '').replace(/[`*_[\]<>]/g, c => `\\${c}`);
        lines.splice(lines.lastIndexOf(''), 0, `*(Orphaned — columnId: \`${safeColId}\`)*`);
      });
    }
  });

  return lines.join('\n');
}

/* Im Projekt-Modus: ki-kontext.md direkt in den Projektordner schreiben */
async function toKIKontextFSA(state, dirHandle) {
  try {
    const fh = await dirHandle.getFileHandle('ki-kontext.md', { create: true });
    const w  = await fh.createWritable();
    await w.write(buildKIKontext(state));
    await w.close();
    return true;
  } catch { return false; }
}

/* Im Standalone-Modus: ki-kontext.md herunterladen */
function toKIKontextDownload(state) {
  downloadText('ki-kontext.md', buildKIKontext(state));
}

/* ────────────────────────────────────────────────────────────────────────
   Karten-Export (#292) — Markdown-Download, Print-Popup, mailto.

   Alle Modi (solo/claude-code/team/server). Server-SMTP-Pfad für die
   mailto-Aktion ist eigene Karte (#293) und wird hier NICHT vorgegriffen.
   Sicherheits-Modell: Markdown.render() liefert ein bereits sanitisiertes
   DOM (ADR-0089). Wir übernehmen dessen outerHTML 1:1 ins Print-Popup —
   keine zusätzliche XSS-Surface, kein document.write mit Roh-Strings.
   ──────────────────────────────────────────────────────────────────────── */

function slugifyTitle(title) {
  return String(title || '').normalize('NFKD').replace(/[̀-ͯ]/g, '').replace(/ß/g, 'ss')
    .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'karte';
}

function yamlEscape(v) {
  if (v == null) return '';
  const s = String(v);
  return /[:#\n\[\]{},&*!|>'"%@`]/.test(s) || /^\s|\s$/.test(s)
    ? '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"' : s;
}

function buildCardMarkdown(card, board) {
  const lines = ['---'];
  if (card.displayId != null) lines.push(`displayId: ${card.displayId}`);
  lines.push(`id: ${yamlEscape(card.id)}`);
  lines.push(`title: ${yamlEscape(card.title)}`);
  if (card.owner)          lines.push(`owner: ${yamlEscape(card.owner)}`);
  if (Array.isArray(card.assignees) && card.assignees.length) {
    lines.push(`assignees: [${card.assignees.map(yamlEscape).join(', ')}]`);
  }
  if (card.created)        lines.push(`created: ${yamlEscape(card.created)}`);
  if (card.updated)        lines.push(`updated: ${yamlEscape(card.updated)}`);
  if (card.complexity)     lines.push(`complexity: ${yamlEscape(card.complexity)}`);
  if (card.effort != null) lines.push(`effort: ${card.effort}`);
  if (card.dueDate)        lines.push(`dueDate: ${yamlEscape(card.dueDate)}`);
  if (card.startDate)      lines.push(`startDate: ${yamlEscape(card.startDate)}`);
  if (card.dueDate && card.dueTime)     lines.push(`dueTime: ${yamlEscape(card.dueTime)}`);
  if (card.startDate && card.startTime) lines.push(`startTime: ${yamlEscape(card.startTime)}`);
  if (Array.isArray(card.labels) && card.labels.length) {
    lines.push(`labels: [${card.labels.map(yamlEscape).join(', ')}]`);
  }
  if (board?.title) lines.push(`board: ${yamlEscape(board.title)}`);
  lines.push('---', '');
  /* H1 mit displayId-Präfix wie im KI-Kontext-Export */
  const prefix = card.displayId != null ? `#${card.displayId} — ` : '';
  lines.push(`# ${prefix}${card.title || ''}`, '');
  if (card.description?.trim()) {
    lines.push('## ' + I18n.t('card.export.section.description'));
    lines.push('');
    lines.push(card.description.trim());
    lines.push('');
  }
  if (Array.isArray(card.comments) && card.comments.length) {
    lines.push('## ' + I18n.t('card.export.section.comments'));
    lines.push('');
    card.comments.forEach(c => {
      const who  = c.author ? c.author : 'unbekannt';
      const when = c.created || '';
      lines.push(`### ${who} — ${when}`);
      lines.push('');
      lines.push((c.text || '').trim());
      lines.push('');
    });
  }
  return lines.join('\n');
}

const PRINT_CSS = '@page{margin:18mm}body{font:13pt/1.4 sans-serif;color:#000;background:#fff;margin:0}h1{font-size:20pt;margin:0 0 4mm}h2{font-size:14pt;margin:6mm 0 2mm;border-bottom:1px solid #ccc;padding-bottom:1mm}h3{font-size:12pt;margin:4mm 0 2mm;color:#444}.meta{font-size:10pt;color:#666;margin:0 0 6mm}.meta span{margin-right:4mm}.md-content code{background:#f4f4f4;padding:0 2pt}.md-content pre{background:#f4f4f4;padding:2mm;page-break-inside:avoid}.md-content table{border-collapse:collapse;margin:2mm 0}.md-content th,.md-content td{border:1px solid #999;padding:1mm 2mm}.md-content blockquote{border-left:3pt solid #999;padding-left:3mm;color:#555}.comment{margin:4mm 0;page-break-inside:avoid}.comment-head{font-weight:bold;color:#555}';

function buildPrintHtml(card, board) {
  const doc = document.implementation.createHTMLDocument(card.title || 'Karte');
  const head = doc.head, body = doc.body;
  const mk = (tag, text, cls) => {
    const el = doc.createElement(tag);
    if (text != null) el.textContent = text;
    if (cls) el.className = cls;
    return el;
  };
  if (!head.querySelector('title')) head.appendChild(mk('title', card.title || 'Karte'));
  head.appendChild(mk('style')).textContent = PRINT_CSS;
  const m = doc.createElement('meta'); m.setAttribute('charset', 'utf-8'); head.appendChild(m);

  const prefix = card.displayId != null ? `#${card.displayId} — ` : '';
  body.appendChild(mk('h1', `${prefix}${card.title || ''}`));

  const metaBits = [];
  if (card.owner)               metaBits.push(['owner', card.owner]);
  if (card.assignees?.length)   metaBits.push(['assignees', card.assignees.join(', ')]);
  if (card.complexity)          metaBits.push(['complexity', card.complexity]);
  if (card.effort != null)      metaBits.push(['effort', `${card.effort}h`]);
  if (card.dueDate)             metaBits.push(['due', card.dueTime ? `${card.dueDate} ${card.dueTime}` : card.dueDate]);
  if (card.labels?.length)      metaBits.push(['labels', card.labels.join(', ')]);
  if (board?.title)             metaBits.push(['board', board.title]);
  if (metaBits.length) {
    const p = mk('p', null, 'meta');
    metaBits.forEach(([k, v]) => {
      const sp = doc.createElement('span');
      sp.appendChild(mk('b', `${k}: `));
      sp.appendChild(doc.createTextNode(String(v)));
      p.appendChild(sp);
    });
    body.appendChild(p);
  }

  /* Markdown.render erzeugt sanitisiertes DOM (ADR-0089) — importNode, da
     Quell-Doc ein anderes Document ist. */
  if (card.description?.trim()) {
    body.appendChild(mk('h2', I18n.t('card.export.section.description')));
    body.appendChild(doc.importNode(Markdown.render(card.description), true));
  }
  if (card.comments?.length) {
    body.appendChild(mk('h2', I18n.t('card.export.section.comments')));
    card.comments.forEach(c => {
      const w = mk('div', null, 'comment');
      w.appendChild(mk('div', `${c.author || 'unbekannt'} — ${c.created || ''}`, 'comment-head'));
      w.appendChild(doc.importNode(Markdown.render(c.text || ''), true));
      body.appendChild(w);
    });
  }

  return '<!DOCTYPE html>\n' + doc.documentElement.outerHTML;
}

function downloadCardMarkdown(card, board) {
  const md   = buildCardMarkdown(card, board);
  const slug = slugifyTitle(card.title);
  const idTag = card.displayId != null ? `${card.displayId}-` : '';
  downloadText(`karte-${idTag}${slug}.md`, md, 'text/markdown;charset=utf-8');
}

function printCard(card, board) {
  /* WICHTIG: window.open im direkten Click-Handler-Pfad aufrufen, sonst
     blockieren Browser das Popup. Aufrufer (modal.js) erfüllt das. */
  const html = buildPrintHtml(card, board);
  /* F-340 (Sprint-39-Kritiker, akzeptiert): noopener würde das Print-Popup-
     Window-Handle nullen, damit funktionierte document.write nicht mehr.
     Hauptschutz ist ADR-0089-Markdown-Sanitierung im Hauptpfad; Refactor
     zu Blob-URL ist als Backlog-Folge möglich. */
  const w = window.open('', '_blank', 'width=820,height=900');
  if (!w) return false;   /* Popup-Blocker — Aufrufer kann Toast zeigen */
  w.document.open();
  w.document.write(html);
  w.document.close();
  /* nach Load: print() auslösen; close überlassen wir dem User */
  const trigger = () => { try { w.focus(); w.print(); } catch {} };
  if (w.document.readyState === 'complete') setTimeout(trigger, 50);
  else w.addEventListener('load', () => setTimeout(trigger, 50));
  return true;
}

function mailtoCard(card, board) {
  /* Alle Modi: mailto:-URL mit subject + Markdown-body. Server-SMTP (#293)
     wird hier bewusst NICHT vorgegriffen — eigene Karte. */
  const md      = buildCardMarkdown(card, board);
  const prefix  = card.displayId != null ? `#${card.displayId}: ` : '';
  const subject = `${prefix}${card.title || ''}`;
  /* mailto: hat Praxis-Limit ~2000 Zeichen (RFC nennt keine harte Grenze, aber
     viele Mail-Clients schneiden ab). Bei Überlänge: Body kürzen + Hinweis. */
  const MAX_BODY = 1800;
  let body = md;
  let truncated = false;
  if (body.length > MAX_BODY) {
    body = body.slice(0, MAX_BODY) + '\n\n…\n' + I18n.t('card.export.email-truncated');
    truncated = true;
  }
  const url = `mailto:?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
  /* window.location.href = url öffnet den Default-Mail-Client */
  try {
    window.location.href = url;
    return { ok: true, truncated };
  } catch {
    return { ok: false, truncated };
  }
}

export const Export = {
  toJSON, toKIKontextFSA, toKIKontextDownload,
  /* #292 Karten-Export */
  buildCardMarkdown, buildPrintHtml, slugifyTitle,
  downloadCardMarkdown, printCard, mailtoCard,
};
