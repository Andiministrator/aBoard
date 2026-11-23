/* llm-pii.js — PII-Detektor + Preview-Modal-Helfer (#301, ADR-0097).
 *
 * IS_SERVER_BUILD-gegated → im Slim/Claude-Code-Build per DCE eliminiert.
 *
 * Verträge:
 *  - detect(text)                          → Findings[] (positionssortiert, validierte
 *                                            IBAN/Luhn/Steuer-ID-Treffer)
 *  - apply(text, findings, actionsById)    → maskierter Text + Statistik
 *  - DEFAULT_ACTION                        → 'redact' (Sicherheits-Default)
 *
 * Finding: { id, type, match, start, end, valid }
 *   id      — stabile ID (type + start) für Modal-Referenz
 *   type    — 'email'|'phone'|'iban'|'bic'|'creditcard'|'taxid-de'|'vatid'|'birthdate'|'address'
 *   match   — gefundener Substring
 *   start   — Position in text
 *   end     — Position in text (exklusiv)
 *   valid   — true wenn Validierung (Mod97/Luhn/Prüfziffer) bestanden ODER nicht anwendbar
 *
 * actionsById: { [id]: 'keep' | 'redact' | 'placeholder' }
 *   Fehlende Einträge → DEFAULT_ACTION = 'redact'.
 *
 * Platzhalter sind deterministisch: identischer Treffer-Text → identischer Index.
 * Beispiel: zweimal "hallo@x.de" → beidemal "[E-Mail #1]".
 */

/* #348/ADR-0118: Overlay/Panel/Header/Body/Footer + Focus-Trap kommen aus
   der gemeinsamen Modal-Shell — ADDIERT Focus-Trap/Focus-Restore, die dieser
   Dialog vorher NICHT hatte (im Gegensatz zu den uebrigen
   Einstellungsdialogen war PII-Preview der einzige ganz ohne).

   Sprint-60-Kritiker: Hier stand „den anderen acht Dialogen" — die Zahl war
   falsch (es sind sieben andere von acht) und trug nichts zur Aussage bei.
   Zahlen, die nur schmuecken, driften; die Bestandszahl gehoert in ADR-0118,
   wo sie gegen die Aufzaehlung darunter prueffaehig ist. z-index bewusst NICHT auf die
   'settings'-Variante (11000) oder Familie A (1100) migriert — siehe
   ADR-0118 §z-index für die Begründung (PII stapelt nur je über dem
   Karten-Modal, nie über Familie A/B). `closeOnOverlayClick:false` +
   `onRequestClose`, weil dieser Dialog KEIN persistentes Singleton ist wie
   die uebrigen: er wird pro Aufruf neu gebaut, löst ein Promise auf und
   entfernt sich danach selbst aus dem DOM — ein versehentlicher Klick auf
   den Hintergrund darf laufende Redaktionsentscheidungen nicht verwerfen. */
import { createModal } from './modal-shell.js';
import { I18n } from './i18n.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMPii = null;

if (IS_SERVER_BUILD) {

  const DEFAULT_ACTION = 'redact';

  /* Pattern-Definitionen.
     - rx muss `g`-Flag haben (matchAll).
     - validate(match): true → Treffer behalten; false → verwerfen.
       Default true (kein Validator).
     - placeholderLabel: angezeigter Typ-Name im Platzhalter. */
  const PATTERNS = [
    {
      type: 'email',
      placeholderLabel: 'E-Mail',
      rx: /[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}/gi,
    },
    {
      type: 'iban',
      placeholderLabel: 'IBAN',
      rx: /\b[A-Z]{2}\d{2}(?:[ ]?\d{4}){2,7}[ ]?\d{1,4}\b/g,
      validate: (m) => _checkMod97(m.replace(/\s+/g, '')),
    },
    {
      type: 'creditcard',
      placeholderLabel: 'Kreditkarte',
      /* Kritiker-Fix S-P3 (#301 Kritiker): vorher `(?:\d[ -]?){13,19}` matched
         JEDE Einzelziffer mit optionalem Trenner → bei Logfiles mit
         "1 2 3 4 5 …" hunderte Treffer + Luhn-Random-Hits. Verschärft:
         erfordert Vier-Stellen-Blöcke (echtes KK-Format: 4-4-4-4 oder
         4-6-5 etc.) bzw. eine zusammenhängende 13–19-Ziffern-Sequenz ohne
         Trenner. Bewusster Verzicht auf Beibehaltung einzeln-getrennter
         Ziffern — diese sind in der Praxis (Mail/Notiz/Karte) kein
         KK-Format. */
      rx: /\b(?:\d{13,19}|\d{4}[ -]\d{4}[ -]\d{4}[ -]\d{1,7}|\d{4}[ -]\d{6}[ -]\d{5})\b/g,
      validate: (m) => _checkLuhn(m.replace(/[ -]/g, '')),
    },
    {
      type: 'bic',
      placeholderLabel: 'BIC',
      rx: /\b[A-Z]{6}[A-Z0-9]{2}(?:[A-Z0-9]{3})?\b/g,
    },
    {
      type: 'vatid',
      placeholderLabel: 'USt-IdNr',
      rx: /\b[A-Z]{2}\d{8,12}\b/g,
    },
    {
      type: 'taxid-de',
      placeholderLabel: 'Steuer-ID',
      rx: /\b\d{11}\b/g,
      validate: (m) => _checkTaxIdDE(m),
    },
    {
      type: 'phone',
      placeholderLabel: 'Telefon',
      /* DE-Vorwahl ODER intl-Variante, mind. 7 Ziffern total. */
      rx: /(?:\+49|0049|0)[\s\-/]?(?:\d[\s\-/]?){6,14}\d/g,
    },
    {
      type: 'birthdate',
      placeholderLabel: 'Geburtsdatum',
      /* DD.MM.YYYY (DE) ODER YYYY-MM-DD (ISO). */
      rx: /\b(?:\d{1,2}\.\d{1,2}\.(?:19|20)\d{2}|(?:19|20)\d{2}-\d{2}-\d{2})\b/g,
      validate: (m) => _checkBirthdatePlausible(m),
    },
    {
      type: 'address',
      placeholderLabel: 'Adresse',
      /* PLZ + Stadtname-Heuristik. Bewusst over-match-tolerant — als
         "möglich" gekennzeichnet, User-Aktion entscheidet. */
      rx: /\b\d{5}\s+[A-ZÄÖÜ][a-zäöüß-]{2,}(?:\s[A-ZÄÖÜ][a-zäöüß-]+)?\b/g,
    },
  ];

  /* ── Validatoren ──────────────────────────────────────────────────── */

  /** IBAN Mod97 (ISO 13616). Buchstaben → Zahlen (A=10..Z=35). */
  function _checkMod97(iban) {
    if (iban.length < 15 || iban.length > 34) return false;
    const rearranged = iban.slice(4) + iban.slice(0, 4);
    let num = '';
    for (const ch of rearranged) {
      if (ch >= '0' && ch <= '9') num += ch;
      else if (ch >= 'A' && ch <= 'Z') num += (ch.charCodeAt(0) - 55).toString();
      else return false;
    }
    /* BigInt-frei: schrittweises mod 97 über 9-Ziffer-Blöcke. */
    let rem = 0;
    for (let i = 0; i < num.length; i += 9) {
      const chunk = String(rem) + num.slice(i, i + 9);
      rem = Number(chunk) % 97;
    }
    return rem === 1;
  }

  /** Luhn-Prüfsumme. */
  function _checkLuhn(digits) {
    if (digits.length < 13 || digits.length > 19) return false;
    let sum = 0;
    let dbl = false;
    for (let i = digits.length - 1; i >= 0; i--) {
      let d = digits.charCodeAt(i) - 48;
      if (d < 0 || d > 9) return false;
      if (dbl) {
        d *= 2;
        if (d > 9) d -= 9;
      }
      sum += d;
      dbl = !dbl;
    }
    return sum % 10 === 0;
  }

  /** Deutsche Steuer-ID (11 Ziffern, Prüfziffer nach BMF-Algorithmus).
   *  Aus den ersten 10 Ziffern wird die 11. berechnet (mod 11/10-Verfahren). */
  function _checkTaxIdDE(s) {
    if (!/^\d{11}$/.test(s)) return false;
    /* Die ersten 10 Ziffern dürfen nur eine Ziffer doppelt oder dreifach
       enthalten — vereinfachte Regel, die häufige Falsch-Treffer wegfiltert. */
    const first10 = s.slice(0, 10);
    const counts = {};
    for (const ch of first10) counts[ch] = (counts[ch] || 0) + 1;
    const repeats = Object.values(counts).filter((n) => n >= 2).length;
    if (repeats !== 1) return false;
    const maxRepeat = Math.max(...Object.values(counts));
    if (maxRepeat > 3) return false;

    /* Prüfziffer-Algorithmus (ISO 7064 Mod 11/10). */
    let product = 10;
    for (let i = 0; i < 10; i++) {
      const d = s.charCodeAt(i) - 48;
      let sum = (d + product) % 10;
      if (sum === 0) sum = 10;
      product = (sum * 2) % 11;
    }
    const check = (11 - product) % 10;
    return check === (s.charCodeAt(10) - 48);
  }

  /** Geburtsdatum-Plausibilität: zwischen 1900-01-01 und heute. */
  function _checkBirthdatePlausible(s) {
    let y, m, d;
    if (s.includes('.')) {
      const [dd, mm, yyyy] = s.split('.');
      y = Number(yyyy); m = Number(mm); d = Number(dd);
    } else {
      const [yyyy, mm, dd] = s.split('-');
      y = Number(yyyy); m = Number(mm); d = Number(dd);
    }
    if (y < 1900) return false;
    const now = new Date();
    if (y > now.getFullYear()) return false;
    if (m < 1 || m > 12) return false;
    if (d < 1 || d > 31) return false;
    return true;
  }

  /* ── Detect ───────────────────────────────────────────────────────── */

  /** Findet alle PII-Treffer im Text. Reihenfolge: positionssortiert,
   *  überlappende Treffer werden zugunsten des längeren Matches verworfen. */
  function detect(text) {
    if (!text || typeof text !== 'string') return [];
    const raw = [];
    for (const def of PATTERNS) {
      def.rx.lastIndex = 0;
      let m;
      while ((m = def.rx.exec(text)) !== null) {
        const match = m[0];
        const valid = def.validate ? def.validate(match) : true;
        if (!valid) continue;
        raw.push({
          type: def.type,
          match,
          start: m.index,
          end: m.index + match.length,
          valid: true,
        });
        if (m.index === def.rx.lastIndex) def.rx.lastIndex++;
      }
    }
    /* Überlappungen auflösen: bei Konflikt das längere Match behalten,
       bei Gleichstand das frühere (deterministisch). */
    raw.sort((a, b) => a.start - b.start || (b.end - b.start) - (a.end - a.start));
    const out = [];
    for (const f of raw) {
      const last = out[out.length - 1];
      if (last && f.start < last.end) {
        /* Überlappt — last ist länger oder gleich lang (Sortierung), drop f. */
        continue;
      }
      out.push(f);
    }
    /* Stabile IDs vergeben (für Modal-Referenz). */
    return out.map((f) => ({ ...f, id: `${f.type}-${f.start}` }));
  }

  /* ── Apply ────────────────────────────────────────────────────────── */

  /** Wendet Aktionen auf Treffer an. Platzhalter sind deterministisch:
   *  gleicher Match-Text → gleicher Index (über alle Typen gemeinsam,
   *  pro Typ-Counter separat).
   *
   *  Rückgabe:
   *  {
   *    text:        maskierter Text,
   *    counts:      { kept, redacted, placeholdered },
   *    placeholderMap: { [match]: '[Label #N]' }
   *  }
   */
  function apply(text, findings, actionsById) {
    if (!text || !findings || findings.length === 0) {
      return { text: text || '', counts: { kept: 0, redacted: 0, placeholdered: 0 }, placeholderMap: {} };
    }
    const acts = actionsById || {};
    const placeholderMap = {};
    const perTypeIndex = {}; /* type → nächste freie Nummer */
    /* Findings müssen positionssortiert sein (detect liefert das schon). */
    const sorted = [...findings].sort((a, b) => a.start - b.start);
    let out = '';
    let cur = 0;
    let kept = 0, redacted = 0, placeholdered = 0;

    for (const f of sorted) {
      if (f.start < cur) continue; /* Überlappung — sollte detect schon dropen */
      out += text.slice(cur, f.start);
      const action = acts[f.id] || DEFAULT_ACTION;
      if (action === 'keep') {
        out += f.match;
        kept++;
      } else if (action === 'placeholder') {
        const key = `${f.type}::${f.match}`;
        let ph = placeholderMap[key];
        if (!ph) {
          perTypeIndex[f.type] = (perTypeIndex[f.type] || 0) + 1;
          const label = _labelFor(f.type);
          ph = `[${label} #${perTypeIndex[f.type]}]`;
          placeholderMap[key] = ph;
        }
        out += ph;
        placeholdered++;
      } else {
        /* 'redact' (default) */
        out += '<REDACTED>';
        redacted++;
      }
      cur = f.end;
    }
    out += text.slice(cur);
    return { text: out, counts: { kept, redacted, placeholdered }, placeholderMap };
  }

  function _labelFor(type) {
    for (const def of PATTERNS) if (def.type === type) return def.placeholderLabel;
    return type;
  }

  /* ── Stale-Card-Hash (F-396) ──────────────────────────────────────────
     Deterministischer 32-Bit-Hash (FNV-1a) zur Erkennung, ob sich der
     Karten-Inhalt seit dem Öffnen des Preview-Modals geändert hat. Kein
     Kryptografie-Zweck — Kollisionen sind für "hat sich der Text seit dem
     letzten Snapshot geändert?" bei Karten-Prompt-Länge praktisch
     irrelevant. */
  function hashText(s) {
    const str = s || '';
    let h = 0x811c9dc5;
    for (let i = 0; i < str.length; i++) {
      h ^= str.charCodeAt(i);
      h = Math.imul(h, 0x01000193);
    }
    return (h >>> 0).toString(16);
  }

  /* ── Server-äquivalenter Prompt-Builder ──────────────────────────────
     Spiegelt server/llm/context.go BuildPrompt. Determinismus-Pflicht:
     gleiche Eingabe → gleicher Output. Damit der vom User im Modal
     gesehene Prompt bit-identisch das ist, was als userPromptOverride
     an den Server geht (= was zum Provider rausgeht). */
  const ALLOWED_FIELDS = new Set([
    'description', 'comments', 'links', 'agentContext', 'activity',
  ]);
  const MAX_REF_SNIPPET = 200;

  function _truncateSnippet(s) {
    if (!s) return '';
    const chars = Array.from(s); /* code-point-safe */
    if (chars.length <= MAX_REF_SNIPPET) return s;
    return chars.slice(0, MAX_REF_SNIPPET - 1).join('') + '…';
  }

  /** Baut den User-Prompt für eine Karte — server-äquivalent.
   *  refsByID: { [cardId]: { displayId, title, snippet } } — vom Aufrufer
   *  vorberechnet aus dem Board-State. */
  function buildPrompt(card, includeFields, refsByID) {
    if (!card) return '';
    const sel = new Set();
    for (const f of (includeFields || [])) if (ALLOWED_FIELDS.has(f)) sel.add(f);

    const out = [];
    const title = (card.title || '').trim() || '(ohne Titel)';
    const displayId = Number(card.displayId) || 0;
    out.push(displayId > 0 ? `# Karte #${displayId}: ${title}` : `# Karte: ${title}`);
    out.push(''); /* Leerzeile nach Header */

    if (sel.has('description')) {
      const d = (card.description || '').trim();
      if (d) {
        out.push('## Beschreibung');
        out.push('');
        out.push(d.replace(/\n+$/, ''));
        out.push('');
      }
    }

    if (sel.has('comments') && Array.isArray(card.comments) && card.comments.length) {
      out.push('## Kommentare');
      out.push('');
      for (const c of card.comments) {
        const text = (c && c.text || '').trim();
        if (!text) continue;
        const date = c.created || '';
        const author = c.author || '';
        let head = '- [';
        head += date;
        if (author) head += (date ? ' ' : '') + author;
        head += ']: ';
        out.push(head + text.replace(/\n/g, '\n  '));
      }
      out.push('');
    }

    if (sel.has('links') && Array.isArray(card.links) && card.links.length) {
      const rows = card.links
        .filter((l) => l && l.type && l.targetId)
        .map((l) => ({ type: l.type, targetId: l.targetId }))
        .sort((a, b) => a.type === b.type
          ? a.targetId.localeCompare(b.targetId)
          : a.type.localeCompare(b.type));
      if (rows.length) {
        out.push('## Verweise');
        out.push('');
        for (const r of rows) {
          const ref = (refsByID && refsByID[r.targetId]) || {};
          const refTitle = (ref.title || '').trim() || '(unbekannte Karte)';
          let line = `- ${r.type}: `;
          if (ref.displayId > 0) line += `#${ref.displayId} `;
          line += `"${refTitle}"`;
          const snip = (ref.snippet || '').trim();
          if (snip) line += ` — ${_truncateSnippet(snip)}`;
          out.push(line);
        }
        out.push('');
      }
    }

    if (sel.has('agentContext')) {
      const a = (card.agentContext || '').trim();
      if (a) {
        out.push('## Agent-Kontext');
        out.push('');
        out.push(a.replace(/\n+$/, ''));
        out.push('');
      }
    }

    if (sel.has('activity') && Array.isArray(card.activity) && card.activity.length) {
      out.push('## Aktivität');
      out.push('');
      for (const a of card.activity) {
        const action = a && a.action || '';
        if (!action) continue;
        const ts = a.ts || '';
        const actor = a.actor || '';
        let line = '- [';
        line += ts;
        if (actor) line += (ts ? ' ' : '') + actor;
        line += '] ' + action;
        if (a.from || a.to) line += `: ${a.from || ''} → ${a.to || ''}`;
        out.push(line);
      }
      out.push('');
    }

    /* Trailing newline-Normalisierung wie Server: rtrim + ein '\n' */
    return out.join('\n').replace(/\n+$/, '') + '\n';
  }

  /* ── Preview-Modal ───────────────────────────────────────────────────
     Öffnet ein modales Overlay mit hervorgehobenem Prompt + Aktionen pro
     Fund. Promise-basiert; resolved zu { action: 'send'|'cancel',
     systemPrompt, userPrompt } — oder zu null bei Abbruch.

     Stil-Inject ist lazy + idempotent (Slim-Pattern). */

  let _piiStylesInjected = false;
  function _ensurePiiStyles() {
    if (_piiStylesInjected || typeof document === 'undefined') return;
    _piiStylesInjected = true;
    const st = document.createElement('style');
    /* #348/ADR-0118: Overlay-Positionierung/-Hintergrund, Panel-Grundfarbe/
       -Radius, Header- und Footer-Struktur kommen jetzt aus der Modal-Shell
       (`.modal-overlay`/`.modal`/`.modal-header-row`/`.modal-actions`, alle
       aus src/style.css). Nur die z-index-Abweichung (1000, siehe ADR-0118)
       und die PII-spezifische Panel-Größe bleiben als Override. `.pii-f`
       ist entfallen — `.modal-actions` aus style.css übernimmt Padding/
       Border/Ausrichtung der Fuß-Buttonleiste (justify-content:flex-end
       ersetzt space-between, da PII nur Cancel+Send rechtsbündig zeigt). */
    st.textContent = '.pii-overlay{z-index:1000}.pii-modal{max-width:880px;max-height:88vh;width:92vw}.pii-modal .modal-actions{justify-content:flex-end}.pii-count{font-size:13px;color:var(--text-muted)}.pii-warn{padding:8px 10px;background:#fff8e1;color:#6b4a00;border:1px solid #ffcc02;border-radius:4px;font-size:12px;margin-bottom:10px;line-height:1.5}.pii-warn-att{background:#ffe0b2;color:#8a3400;border-color:#ff9800;font-weight:600}.pii-bulk{display:flex;gap:8px;margin-bottom:10px;flex-wrap:wrap}.pii-bulk button{padding:4px 10px;font-size:12px;background:var(--surface);border:1px solid var(--border);border-radius:4px;cursor:pointer}.pii-bulk button:hover{background:#f5f5f5;color:#333}.pii-tokens{font-size:12px;color:var(--text-muted);margin-bottom:10px}.pii-preview{font-family:ui-monospace,Consolas,monospace;font-size:12px;line-height:1.5;white-space:pre-wrap;background:#fafafa;color:#333;border:1px solid var(--border);border-radius:4px;padding:10px;max-height:340px;overflow-y:auto;word-wrap:break-word}.pii-hit{background:#fff3a0;color:#5c4a00;padding:1px 3px;border-radius:2px;border:1px dashed #d4a700;cursor:pointer}.pii-hit-redact{background:#ffd4d4;color:#8b0000;border-color:#c33;text-decoration:line-through}.pii-hit-placeholder{background:#d4f0ff;color:#0d47a1;border-color:#1976d2;font-style:italic}.pii-finds{margin-top:14px;display:flex;flex-direction:column;gap:6px;font-size:13px}.pii-find{display:flex;align-items:center;gap:8px;padding:6px 8px;background:#fafafa;color:#333;border:1px solid var(--border);border-radius:4px}.pii-find-meta{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.pii-find-type{font-weight:600;font-size:12px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.04em;margin-right:6px}.pii-find-match{font-family:ui-monospace,Consolas,monospace;font-size:12px}.pii-act{display:flex;gap:4px}.pii-act button{padding:3px 8px;font-size:12px;background:var(--surface);border:1px solid var(--border);border-radius:4px;cursor:pointer}.pii-act button.pii-act-active{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.pii-stale-warn{padding:10px 16px;background:#fff3e0;color:#8a3400;border-top:1px solid #ff9800;font-size:13px;line-height:1.5}.pii-stale-acts{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap}.pii-stale-acts button{padding:5px 10px;font-size:12px;border-radius:4px;cursor:pointer;background:#fff3e0;border:1px solid #ff9800;color:#8a3400}.pii-cancel{background:var(--surface);border:1px solid var(--border);padding:8px 14px;font-size:14px;border-radius:4px;cursor:pointer}.pii-send{background:var(--accent-btn);color:#fff;border:none;padding:8px 14px;font-size:14px;border-radius:4px;cursor:pointer}';
    document.head.appendChild(st);
  }

  /** Öffnet Preview-Modal. Returns Promise<{action,systemPrompt,userPrompt}|null>.
   *
   *  getFreshUserPrompt (optional, F-396): synchroner Callback, der den
   *  aktuell im (gepollten) State liegenden, server-äquivalent serialisierten
   *  userPrompt liefert (oder null, wenn die Karte nicht mehr auffindbar ist).
   *  Wird bei jedem "Senden"-Klick erneut aufgerufen — das Modal kann
   *  minutenlang offen bleiben (Treffer prüfen/redacten), während eine
   *  andere Person die Karte per Op-Inbox editiert. Weicht der frische Hash
   *  vom Hash beim Öffnen ab, wird NICHT hart blockiert (Fix-Richtung
   *  Karte #378) — stattdessen erscheint die Stale-Warn-Fläche mit der Wahl
   *  "Frischen Stand laden" (Modal-Inhalt wird ersetzt + neu geprüft) oder
   *  "Trotzdem senden" (bewusst mit dem bereits geprüften alten Stand). */
  /* anhangAnzahl (#429, ADR-0121): Zahl der Bild-Anhaenge, die mit diesem
     Send rausgehen. > 0 blendet einen zweiten Warnblock ein — die Grenze des
     Filters bei Bildern gehoert genau HIERHER, an die Stelle, an der jemand
     sich auf ihn verlaesst. Der Hinweis an der Auswahl im KI-Tab ist die
     erste Haelfte; ohne diese zweite steht im Moment der Entscheidung nichts. */
  function openPreviewModal({ systemPrompt, userPrompt, estimateTokens, getFreshUserPrompt, anhangAnzahl }) {
    if (typeof document === 'undefined') return Promise.resolve(null);
    _ensurePiiStyles();

    /* curUserPrompt ist mutable — "Frischen Stand laden" ersetzt ihn. */
    let curUserPrompt = userPrompt || '';
    let openHash = hashText(curUserPrompt);

    /* Detect auf Gesamttext, aber Offsets in zwei Bereichen tracken. */
    const sysFinds = detect(systemPrompt).map((f) => ({ ...f, _scope: 'system' }));
    let usrFinds = detect(curUserPrompt).map((f) => ({ ...f, _scope: 'user', id: `u-${f.id}` }));
    let findings = [...sysFinds, ...usrFinds];

    /* actionsById state */
    const actions = {};
    for (const f of findings) actions[f.id] = DEFAULT_ACTION;

    return new Promise((resolve) => {
      /* Reentrancy-Guard + Finish-Logik vorab deklariert (Funktionsdeklarationen
         sind gehoisted) — `onRequestClose` unten referenziert `_finish`,
         ausgeführt wird sie aber erst nach vollständigem Modal-Aufbau, wenn der
         Nutzer tatsächlich interagiert. */
      let _done = false;
      function _doFinish(action) {
        /* Kritiker-Fix K-P1: Reentrancy-Guard. Doppelklick Send oder
           ESC-direkt-nach-Send würde sonst removeChild() ein zweites Mal
           aufrufen → NotFoundError. Auch Promise-Resolve nur einmal. */
        if (_done) return;
        _done = true;
        let finalSys = systemPrompt || '';
        let finalUsr = curUserPrompt;
        try {
          if (action === 'send') {
            finalSys = apply(systemPrompt || '', _scopeFinds('system'), actions).text;
            finalUsr = apply(curUserPrompt, _scopeFinds('user'), actions).text;
          }
        } finally {
          /* modal.close(): Listener-Abbau (ESC) + Fokus-Restore — vorher
             manuell nachgebaut, jetzt Shell-Aufgabe (#348). Danach entfernt
             sich der Dialog wie bisher komplett aus dem DOM (kein
             persistentes Singleton, siehe Kopfkommentar). */
          modal.close();
          if (modal.overlay.parentNode) modal.overlay.parentNode.removeChild(modal.overlay);
          resolve({ action, systemPrompt: finalSys, userPrompt: finalUsr });
        }
      }

      /* F-396: Vor dem finalen Send erneut prüfen, ob die Karte seit dem
         Öffnen des Modals (bzw. seit dem letzten "Frischen Stand laden")
         geändert wurde. Bei Abweichung KEIN hartes Blockieren — die
         Stale-Warn-Fläche zeigt die Wahl, der Klick auf "Senden" selbst
         finalisiert in diesem Fall noch nicht. */
      function _finish(action) {
        if (_done) return;
        if (action === 'send' && typeof getFreshUserPrompt === 'function') {
          const freshText = getFreshUserPrompt();
          /* Sprint-52-Kritiker (D2): `null` heisst „Karte nicht mehr
             auffindbar" — im Mehrbenutzerbetrieb also gelöscht. Die frühere
             Fassung prüfte nur `typeof === 'string'` und liess den Send in
             diesem Fall UNGEPRÜFT durch: das LLM arbeitete gegen eine Karte,
             die es nicht mehr gibt, ohne jede Warnung. Die stärkste Form der
             Änderung darf nicht die einzige sein, die durchrutscht. */
          if (freshText === null || freshText === undefined) {
            _kartenVerlustAnzeigen();
            return;
          }
          if (typeof freshText === 'string' && hashText(freshText) !== openHash) {
            if (staleEl) staleEl.style.display = '';
            return;
          }
        }
        _doFinish(action);
      }

      const modal = createModal({
        extraOverlayClass: 'pii-overlay',
        extraPanelClass: 'pii-modal',
        titleId: 'pii-dialog-title',
        /* Sprint-60-Kritiker: war als einziger Shell-Konsument hartcodiert.
           Der uebrige Dialogtext ist zwar ebenfalls deutsch (Bestand), aber ein
           aria-label ist das, was ein Screenreader-Nutzer statt des Symbols
           hoert — das aus dem Woerterbuch zu nehmen kostet nichts. */
        closeAriaLabel: I18n.t('btn.close-modal'),
        footer: true,
        /* Kein Klick-außerhalb-Schliessen — ein Fehlklick darf eine laufende
           Redaktionsentscheidung nicht verwerfen (siehe Kopfkommentar). ESC
           und der Close-Button laufen stattdessen über _finish('cancel'),
           damit das Promise garantiert aufgelöst wird. */
        closeOnOverlayClick: false,
        onRequestClose: () => _finish('cancel'),
      });
      modal.setTitle('PII-Vorschau vor Senden');
      const countBadge = document.createElement('span');
      countBadge.className = 'pii-count';
      countBadge.id = 'pii-count';
      countBadge.textContent = `${findings.length} potenzielle Treffer`;
      modal.header.insertBefore(countBadge, modal.closeBtn);

      modal.body.innerHTML = `
          <div class="pii-warn">
            <strong>Automatischer Filter erkennt KEINE Personennamen</strong>, keine freitext-Adressen
            und keine indirekten Identifier ("der Mitarbeiter aus Abteilung Y") — Regex deckt nur strukturierte
            Identifier ab. Verantwortliche Prüfung beim Senden bleibt beim User.
          </div>
          <div class="pii-bulk">
            <button data-bulk="redact" type="button">Alle redacten</button>
            <button data-bulk="placeholder" type="button">Alle Platzhalter</button>
            <button data-bulk="keep" type="button">Alle behalten</button>
          </div>
          <div class="pii-tokens" id="pii-tokens"></div>
          <div class="pii-preview" id="pii-preview"></div>
          <div class="pii-finds" id="pii-finds"></div>
      `;

      /* Anhang-Warnung als erstes Element — per DOM statt im Template, damit
         der Text nicht durch innerHTML geht (er ist zwar aus dem eigenen
         Woerterbuch, aber ein Template ist die Stelle, an der so etwas
         irgendwann doch aus fremder Quelle kommt). */
      if (Number(anhangAnzahl) > 0) {
        const attWarn = document.createElement('div');
        attWarn.className = 'pii-warn pii-warn-att';
        attWarn.id = 'pii-att-warn';
        attWarn.textContent = I18n.t('pii.attachments-blind', Number(anhangAnzahl));
        modal.body.insertBefore(attWarn, modal.body.firstChild);
      }

      const staleEl = document.createElement('div');
      staleEl.className = 'pii-stale-warn';
      staleEl.id = 'pii-stale-warn';
      staleEl.style.display = 'none';
      staleEl.setAttribute('role', 'alert');
      staleEl.innerHTML = `
          <strong>Karte wurde inzwischen geändert.</strong> Der geprüfte Text oben basiert nicht
          mehr auf dem aktuellen Stand der Karte.
          <div class="pii-stale-acts">
            <button type="button" data-stale-act="reload">Frischen Stand laden</button>
            <button type="button" data-stale-act="anyway">Trotzdem senden (alter Stand)</button>
          </div>
      `;
      modal.panel.insertBefore(staleEl, modal.footer);

      modal.footer.innerHTML = `
          <button class="pii-cancel" type="button" data-act="cancel">Abbrechen</button>
          <button class="pii-send"   type="button" data-act="send">Senden</button>
      `;

      modal.open();

      const previewEl = modal.body.querySelector('#pii-preview');
      const findsEl   = modal.body.querySelector('#pii-finds');
      const tokensEl  = modal.body.querySelector('#pii-tokens');

      function _scopeFinds(scope) {
        return findings.filter((f) => f._scope === scope);
      }

      function _renderHighlighted(text, finds) {
        /* finds sind über `id` referenzierbar — wir setzen span-tags */
        const sorted = [...finds].sort((a, b) => a.start - b.start);
        const out = [];
        let cur = 0;
        for (const f of sorted) {
          if (f.start < cur) continue;
          out.push(document.createTextNode(text.slice(cur, f.start)));
          const sp = document.createElement('span');
          const act = actions[f.id];
          sp.className = 'pii-hit' + (act === 'redact' ? ' pii-hit-redact'
            : act === 'placeholder' ? ' pii-hit-placeholder' : '');
          sp.dataset.fid = f.id;
          sp.title = `${_labelFor(f.type)} — Klick wechselt zwischen behalten / redact / Platzhalter`;
          sp.textContent = f.match;
          out.push(sp);
          cur = f.end;
        }
        out.push(document.createTextNode(text.slice(cur)));
        return out;
      }

      /* F-403 (Karte #433) — GEMESSEN statt geraten, bevor umgebaut wurde.
         Der Sprint-44-Kritiker-Befund unterstellte spürbares Ruckeln bei 200
         Findings, weil ein Toggle-Klick previewEl+findsEl komplett neu
         aufbaut statt nur den betroffenen Teilbaum zu patchen. Benchmark
         (happy-dom, tendenziell LANGSAMER als ein echter Browser, da ohne
         V8-DOM-Fastpath): 20x _renderAll() pro Finding-Zahl, Mittelwert:
           n=5   Findings → ~1,2 ms/Aufruf
           n=20  Findings → ~2,3 ms/Aufruf
           n=50  Findings → ~4,5 ms/Aufruf
           n=200 Findings → ~16,5 ms/Aufruf
         Selbst der 200er-Extremfall (aBoard-Kartentexte erreichen das in der
         Praxis nicht — 200 strukturierte PII-Identifier in einer Karte wären
         ein Ausreisser, kein Normalfall) bleibt unter der ~100-ms-Schwelle,
         ab der Menschen eine UI-Reaktion als spürbar verzögert wahrnehmen —
         und das im langsameren Test-DOM. Ein gezielter Diff-Rebuild (nur die
         geänderte Zeile patchen) hätte hier gegen einen echten Nutzen von
         effektiv null zusätzliche Komplexität UND ein neues Risiko am
         F-396-Kartenschnappschuss eingetauscht (openHash/curUserPrompt
         dürfen sich beim Rendern nicht verschieben — ein Diff-Pfad ist genau
         die Art Änderung, die das versehentlich täte). Deshalb bewusst KEIN
         Umbau: das volle innerHTML-Neuaufbauen bleibt, dokumentiert statt
         "übersehen". Separat davon offen (nicht Teil dieser Messung): bei
         sehr langen Prompt-Texten kann `.pii-preview` (max-height:340px,
         overflow-y:auto) beim Rebuild seine Scroll-Position verlieren — das
         ist ein UX-Nit unabhängig von der Finding-Zahl, siehe Kartenbericht. */
      function _renderAll() {
        /* F-758: Scroll-Position beider Blöcke über den Rebuild retten.
           `.pii-preview` ist auf max-height:340px/overflow-y:auto begrenzt;
           jeder Toggle-Klick baut den Knoten per innerHTML neu auf und der
           neue startet bei scrollTop 0. Wer in einem langen Prompt weit unten
           eine Fundstelle abwählt, landete danach wieder ganz oben. Das hängt
           an der TEXTLÄNGE, nicht an der Finding-Zahl — die F-403-Messung
           oben (Rechenkosten) widerlegt es also nicht.
           Rein visuell: der F-396-Schnappschuss (openHash/curUserPrompt) wird
           nicht angefasst. */
        const preScroll  = previewEl.scrollTop;
        const findScroll = findsEl.scrollTop;

        /* Preview-Block: System + User, optisch getrennt. */
        previewEl.innerHTML = '';
        const sysH = document.createElement('div');
        sysH.style.fontWeight = '600';
        sysH.style.fontSize = '11px';
        sysH.style.color = 'var(--text-muted)';
        sysH.style.marginBottom = '4px';
        sysH.textContent = 'SYSTEM-PROMPT';
        previewEl.appendChild(sysH);
        for (const n of _renderHighlighted(systemPrompt || '', _scopeFinds('system'))) {
          previewEl.appendChild(n);
        }
        previewEl.appendChild(document.createElement('hr'));
        const usrH = document.createElement('div');
        usrH.style.fontWeight = '600';
        usrH.style.fontSize = '11px';
        usrH.style.color = 'var(--text-muted)';
        usrH.style.margin = '8px 0 4px';
        usrH.textContent = 'USER-PROMPT (Karte)';
        previewEl.appendChild(usrH);
        for (const n of _renderHighlighted(curUserPrompt, _scopeFinds('user'))) {
          previewEl.appendChild(n);
        }

        /* Pro-Treffer-Aktionen */
        findsEl.innerHTML = '';
        for (const f of findings) {
          const row = document.createElement('div');
          row.className = 'pii-find';
          const meta = document.createElement('div');
          meta.className = 'pii-find-meta';
          const t = document.createElement('span');
          t.className = 'pii-find-type';
          t.textContent = _labelFor(f.type);
          const m = document.createElement('span');
          m.className = 'pii-find-match';
          m.textContent = f.match;
          meta.appendChild(t);
          meta.appendChild(m);
          row.appendChild(meta);
          const act = document.createElement('div');
          act.className = 'pii-act';
          for (const a of ['keep', 'redact', 'placeholder']) {
            const b = document.createElement('button');
            b.type = 'button';
            b.textContent = a === 'keep' ? 'Behalten' : a === 'redact' ? 'Redact' : 'Platzhalter';
            if (actions[f.id] === a) b.classList.add('pii-act-active');
            b.addEventListener('click', () => {
              actions[f.id] = a;
              _renderAll();
              _updateTokens();
            });
            act.appendChild(b);
          }
          row.appendChild(act);
          findsEl.appendChild(row);
        }

        /* F-758: nach dem Neuaufbau zurückschreiben. Der Browser klemmt einen
           zu grossen Wert selbst auf die neue Scroll-Höhe — es kann also nicht
           „daneben" scrollen, wenn der Inhalt kürzer geworden ist. */
        previewEl.scrollTop = preScroll;
        findsEl.scrollTop   = findScroll;

        _updateTokens();
      }

      function _updateTokens() {
        if (!tokensEl) return;
        const sysApplied = apply(systemPrompt || '', _scopeFinds('system'), actions);
        const usrApplied = apply(curUserPrompt, _scopeFinds('user'), actions);
        const before = (estimateTokens || _defaultEstimate)((systemPrompt || '') + '\n' + curUserPrompt);
        const after  = (estimateTokens || _defaultEstimate)(sysApplied.text + '\n' + usrApplied.text);
        const delta = after - before;
        tokensEl.textContent = `Token-Schätzung: vorher ${before} → nachher ${after} (${delta >= 0 ? '+' : ''}${delta})`;
      }

      function _defaultEstimate(s) { return Math.ceil((s || '').length / 4); }

      /* Klick auf Treffer im Preview → Action cyclen. */
      previewEl.addEventListener('click', (ev) => {
        const sp = ev.target.closest('.pii-hit');
        if (!sp) return;
        const fid = sp.dataset.fid;
        const cur = actions[fid] || DEFAULT_ACTION;
        actions[fid] = cur === 'keep' ? 'redact' : cur === 'redact' ? 'placeholder' : 'keep';
        _renderAll();
      });

      /* Bulk-Buttons */
      for (const btn of modal.body.querySelectorAll('.pii-bulk button')) {
        btn.addEventListener('click', () => {
          const target = btn.dataset.bulk;
          for (const f of findings) actions[f.id] = target;
          _renderAll();
        });
      }

      /* F-396: Karte hat sich seit dem Öffnen des Modals geändert — neuen
         Stand übernehmen. Findings/Actions für den User-Scope werden neu
         aufgebaut (Treffer-Positionen können sich verschoben haben); der
         System-Prompt ist davon nie betroffen (kartenunabhängig, User-Text).
         Neue Finding-IDs starten bei DEFAULT_ACTION, bereits gesetzte
         Aktionen für unveränderte IDs bleiben erhalten. */
      function _refreshFromFreshCard(freshText) {
        curUserPrompt = freshText;
        openHash = hashText(curUserPrompt);
        usrFinds = detect(curUserPrompt).map((f) => ({ ...f, _scope: 'user', id: `u-${f.id}` }));
        /* Sprint-52-Kritiker (S3): Die Finding-IDs sind POSITIONAL
           (`typ-offset`). Behielte man die bisherigen Aktionen für den
           User-Scope, erbte ein neuer Treffer die Entscheidung eines alten,
           der zufällig an derselben Stelle stand. Konkret: Nutzer setzt
           `email-120` bewusst auf `keep` (die eigene Adresse); im frischen
           Text steht an Offset 120 eine FREMDE Adresse — sie ginge
           unredigiert ans LLM, ohne dass der Nutzer sie je gesehen hat.
           Der Sicherheits-Default gilt deshalb für den ganzen neu erkannten
           User-Scope neu. System-Scope bleibt unberührt: der systemPrompt
           ist kartenunabhängig und hat sich nicht geändert. */
        for (const f of usrFinds) actions[f.id] = DEFAULT_ACTION;
        findings = [...sysFinds, ...usrFinds];
        for (const f of sysFinds) if (!(f.id in actions)) actions[f.id] = DEFAULT_ACTION;
        if (staleEl) staleEl.style.display = 'none';
        _renderAll();
      }

      /* Karte ist verschwunden: dieselbe Warnfläche, aber „Frischen Stand
         laden" wäre hier eine tote Schaltfläche — es gibt keinen frischen
         Stand mehr. Deshalb wird sie ausgeblendet und der Text angepasst. */
      function _kartenVerlustAnzeigen() {
        if (!staleEl) return;
        const acts = staleEl.querySelector('.pii-stale-acts');
        const reload = staleEl.querySelector('[data-stale-act="reload"]');
        if (reload) reload.style.display = 'none';
        const kopf = staleEl.querySelector('strong');
        if (kopf) kopf.textContent = 'Karte existiert nicht mehr.';
        staleEl.dataset.grund = 'geloescht';
        if (acts) acts.style.display = '';
        staleEl.style.display = '';
      }

      if (staleEl) {
        const reloadBtn = staleEl.querySelector('[data-stale-act="reload"]');
        const anywayBtn = staleEl.querySelector('[data-stale-act="anyway"]');
        if (reloadBtn) reloadBtn.addEventListener('click', () => {
          if (typeof getFreshUserPrompt !== 'function') return;
          const freshText = getFreshUserPrompt();
          if (typeof freshText === 'string') { _refreshFromFreshCard(freshText); return; }
          /* Verify-Lauf zur Sprint-52-Nacharbeit: Der `null`-Fall war nur im
             Send-Pfad behandelt. Wird die Karte gelöscht, WÄHREND die
             Stale-Fläche schon sichtbar ist, tat dieser Klick nichts — die
             Fläche blieb in der „bearbeitet"-Darstellung stecken, ohne
             Ausweg ausser dem Schliessen des Modals. */
          _kartenVerlustAnzeigen();
        });
        if (anywayBtn) anywayBtn.addEventListener('click', () => {
          if (staleEl) staleEl.style.display = 'none';
          /* Bewusste User-Entscheidung: mit dem bereits geprüften, ggf.
             veralteten Stand senden — Stale-Check hier bewusst umgangen. */
          _doFinish('send');
        });
      }

      modal.footer.querySelector('[data-act="send"]').addEventListener('click', () => _finish('send'));
      modal.footer.querySelector('[data-act="cancel"]').addEventListener('click', () => _finish('cancel'));

      /* ESC/Tab-Trap laufen jetzt über die Modal-Shell (onKey→onRequestClose
         bzw. trapTabKey) — kein eigener keydown-Listener mehr (#348). */
      _renderAll();
      modal.focusAfterRender();
    });
  }

  /* ── Modul-Exports ────────────────────────────────────────────────── */

  LLMPii = {
    detect,
    apply,
    buildPrompt,
    openPreviewModal,
    hashText,
    DEFAULT_ACTION,
    /* Test-/Diagnose-Hooks (nicht im normalen Verbrauch): */
    /* Die erlaubten Kontext-Felder als Liste. Für den Drift-Wächter aus F-400:
       Der Go-Test iteriert `AllowedFields` dynamisch und merkt deshalb sofort,
       wenn die Fixture ein neues Feld nicht abdeckt. Der JS-Test hatte die
       Liste dupliziert und wäre genau dann blind gewesen (Kritiker-Lauf
       2026-09-17). Eine duplizierte Liste in einem Drift-Wächter ist der
       Fehler, gegen den er gebaut ist. */
    _allowedFields: [...ALLOWED_FIELDS],
    _patterns: PATTERNS,
    _checkMod97,
    _checkLuhn,
    _checkTaxIdDE,
  };
}

export { LLMPii };
