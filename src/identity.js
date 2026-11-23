/* Identity-System (Karte #123) — Owner-/Autor-Avatare für Karten, Kommentare
   und Activity-Log. Daten liegen in board.identities. Abgrenzung: NICHT zu
   verwechseln mit Profile.has() (Deployment-Profil, ADR-0044). */

/* Karte #450-Nacharbeit: über das Storage-Plugin, nicht direkt. Heute noch
   folgenlos — Board-Anhänge (und damit Avatar-Bilder) tragen weiterhin
   `att.data` —, aber genau diese Annahme hebt Karte #443 auf. Dann fände der
   direkte Weg das Bild nicht mehr. */
import { StoragePlugin, createFsaAttachmentBackend } from './storage-plugin.js';

function anhangBackend() {
  return StoragePlugin.getActivePlugin()?.attachments?.() ?? createFsaAttachmentBackend();
}

/* Karte #158: Claude-Defaults — einheitliche Coral-Farbe, Unterscheidung per Icon. */
export const CLAUDE_DEFAULTS = {
  'claude-opus':   { color: '#cc785c', icon: '✦', label: 'Claude Opus'   },
  'claude-sonnet': { color: '#cc785c', icon: '✧', label: 'Claude Sonnet' },
  'claude-haiku':  { color: '#cc785c', icon: '❋', label: 'Claude Haiku'  },
  /* Karte #1172 (ADR-0026 Nachtrag 2026-11-19): vierte Familie. */
  'claude-fable':  { color: '#cc785c', icon: '✶', label: 'Claude Fable'  },
};

const USER_COLOR_PALETTE = [
  '#2563eb', '#0891b2', '#16a34a', '#ca8a04', '#ea580c',
  '#dc2626', '#9333ea', '#db2777', '#64748b',
];

/** djb2-Hash — stabil über Sessions. */
export function hashString(s) {
  let h = 5381;
  const str = String(s ?? '');
  for (let i = 0; i < str.length; i++) h = ((h << 5) + h + str.charCodeAt(i)) | 0;
  return Math.abs(h);
}

export function colorForLabel(label) {
  return USER_COLOR_PALETTE[hashString(label) % USER_COLOR_PALETTE.length];
}

/** Karte #971: Labelfarbe fest aus dem Text — Farbton in 45°-Schritten (8 Töne),
    Sättigung/Helligkeit je Theme über --lb/--lf (style.css, AA in beiden Themes).
    F-2677: djb2 % 8 hängt nur an der Quersumme der Zeichencodes („P1“/„P9“, „ux“/„xu“
    gleich) — deshalb multiplikativ mischen und die obersten 3 Bit nehmen. */
export function labelTon(text) {
  return (Math.imul(hashString(text), 0x9E3779B1) >>> 29) * 45;
}

export function faerbeLabel(node, text) {
  node.classList.add('lc');
  node.style.setProperty('--h', labelTon(text));
}

export function initialOf(label) {
  const s = String(label ?? '').trim();
  if (!s) return '?';
  return [...s][0].toUpperCase();
}

function parseHex(hex) {
  let h = String(hex || '').trim().replace(/^#/, '');
  if (h.length === 3) h = h.split('').map(c => c + c).join('');
  if (h.length !== 6) return { r: 124, g: 58, b: 237 };
  return {
    r: parseInt(h.slice(0, 2), 16) || 0,
    g: parseInt(h.slice(2, 4), 16) || 0,
    b: parseInt(h.slice(4, 6), 16) || 0,
  };
}

function relativeLuminance({ r, g, b }) {
  const lin = (v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

export function contrastRatio(a, b) {
  const lA = relativeLuminance(parseHex(a));
  const lB = relativeLuminance(parseHex(b));
  const [hi, lo] = lA > lB ? [lA, lB] : [lB, lA];
  return (hi + 0.05) / (lo + 0.05);
}

/** Beste Textfarbe (#fff oder #000) für einen Hintergrund. */
export function getContrastColor(bgHex) {
  const cW = contrastRatio(bgHex, '#ffffff');
  const cB = contrastRatio(bgHex, '#000000');
  if (cW >= 4.5) return '#ffffff';
  if (cB >= 4.5) return '#000000';
  return cW >= cB ? '#ffffff' : '#000000';
}

export function defaultClaudeIdentities() {
  return Object.entries(CLAUDE_DEFAULTS).map(([kind, d]) => ({
    id: `identity-${kind}`, kind, label: d.label, color: d.color, icon: d.icon,
    imageAttachmentId: null,
  }));
}

export function defaultUserIdentity(userName) {
  const label = (typeof userName === 'string' && userName.trim()) ? userName.trim() : 'User';
  return {
    id: 'user-default', kind: 'user', label,
    color: colorForLabel(label), icon: initialOf(label), imageAttachmentId: null,
    /* ADR-0108: optionale Brücke Identity → Server-User. Leer heisst „nicht
       zugeordnet" — solche Identities bekommen keine Benachrichtigungen und
       werden dabei still übersprungen (bewusste Akzeptanz der ADR). */
    email: '',
  };
}

/* ADR-0108 / Karte #339: Mailadressen der Empfänger einer Karte.

   Der Weg führt bewusst über `identity.email` und nicht über den Owner-String
   direkt: `owner`/`assignees` sind freie Label (`"Nutzer"`, `"Claude Opus"`) und
   taugen nicht als Schlüssel. Der Server bekommt am Ende fertige Adressen und
   muss die Kartenstruktur nicht interpretieren (ADR-0044).

   `resolveIdentity` wird hier NICHT verwendet: Es erzeugt für unbekannte
   Labels eine On-the-fly-Identity, die per Konstruktion keine Adresse hat —
   und für Claude-Labels eine aus `CLAUDE_DEFAULTS`, die ebenfalls keine hat.
   Gesucht sind nur gepflegte Einträge aus `board.identities`, und zwar
   ausschliesslich `kind: 'user'`: ein Modell bekommt keine Post.

   Der Vergleich läuft case-insensitiv und getrimmt über das Label. Das ist
   grosszügiger als der exakte Abgleich in `resolveIdentity` — hier ist eine
   verpasste Zuordnung (keine Benachrichtigung) der harmlosere Fehler als eine
   falsche (Post an den Falschen), und Gross-/Kleinschreibung eines Namens
   ändert nicht, wer gemeint ist. */
/** Karte #608: Anzeigename zu einer E-Mail — das Label der Identität mit
    dieser E-Mail, sonst der Teil vor dem @. Für „Claude (für Nutzer)“ an
    Agenten-Kommentaren (`comment.onBehalfOf` stempelt der Server). */
export function nameForEmail(email, board) {
  const e = (typeof email === 'string') ? email.trim().toLowerCase() : '';
  if (!e) return '';
  const identities = Array.isArray(board?.identities) ? board.identities : [];
  const treffer = identities.find(i => typeof i.email === 'string' && i.email.trim().toLowerCase() === e);
  return treffer?.label || e.split('@')[0];
}

export function emailsForLabels(labels, board) {
  const identities = Array.isArray(board?.identities) ? board.identities : [];
  const nachLabel = new Map();
  for (const i of identities) {
    if (i?.kind !== 'user') continue;
    const mail = (typeof i.email === 'string') ? i.email.trim() : '';
    if (!mail) continue;
    const key = String(i.label ?? '').trim().toLowerCase();
    if (key && !nachLabel.has(key)) nachLabel.set(key, mail);
  }
  const out = [];
  const gesehen = new Set();
  for (const l of (Array.isArray(labels) ? labels : [])) {
    const key = String(l ?? '').trim().toLowerCase();
    if (!key) continue;
    const mail = nachLabel.get(key);
    if (mail && !gesehen.has(mail.toLowerCase())) {
      gesehen.add(mail.toLowerCase());
      out.push(mail);
    }
  }
  return out;
}

/** Owner-String → Identity (oder null). Reihenfolge:
    Opus/Sonnet/Haiku-Substring (case-insensitive) → generisches Claude → user-Label-Match
    → on-the-fly user-Identity. */
export function resolveIdentity(owner, board) {
  const o = (typeof owner === 'string') ? owner : '';
  if (!o) return null;
  const identities = Array.isArray(board?.identities) ? board.identities : [];

  const ensure = (kind) => {
    const f = identities.find(i => i.kind === kind);
    if (f) return f;
    const d = CLAUDE_DEFAULTS[kind];
    return d ? { id: `identity-${kind}`, kind, label: d.label, color: d.color,
                 icon: d.icon, imageAttachmentId: null } : null;
  };

  const low = o.toLowerCase();
  if (low.includes('opus'))   return ensure('claude-opus');
  if (low.includes('sonnet')) return ensure('claude-sonnet');
  if (low.includes('haiku'))  return ensure('claude-haiku');
  if (low.includes('fable'))  return ensure('claude-fable');
  if (/^claude\b/i.test(o) || low === 'claude') return ensure('claude-sonnet');

  const userMatch = identities.find(i => i.kind === 'user' && i.label === o);
  if (userMatch) return userMatch;

  const hash = hashString(o);
  return {
    id: `user-onthefly-${hash}`, kind: 'user', label: o,
    color: colorForLabel(o), icon: initialOf(o), imageAttachmentId: null,
    _onTheFly: true,
  };
}

const SIZE_MAP = { xs: 20, sm: 24, md: 32, lg: 40 };

/** DOM-Avatar (Kreis) für eine Identity. Bild-Variante (imageAttachmentId)
    überdeckt den Glyph; sonst Icon/Initiale in Kontrast-Textfarbe. */
export function buildAvatar(identity, size = 'sm', options = {}) {
  if (!identity) return null;
  const px = (typeof size === 'number' && size > 0)
    ? Math.round(size) : (SIZE_MAP[size] ?? SIZE_MAP.sm);
  const root = document.createElement('span');
  root.className = 'identity-avatar';
  root.setAttribute('role', 'img');
  root.setAttribute('aria-label', identity.label || '');
  root.title = identity.label || '';
  root.style.width  = `${px}px`;
  root.style.height = `${px}px`;
  root.style.background = identity.color || '#7c3aed';
  root.dataset.identityKind = identity.kind || '';

  const { board, trackUrl } = options || {};

  if (identity.imageAttachmentId && board) {
    const att = (board.attachments || []).find(a => a.id === identity.imageAttachmentId);
    if (att) {
      root.classList.add('identity-avatar--image');
      Promise.resolve(anhangBackend().boardAttachmentUrl(att)).then(url => {
        if (!url) {
          /* Klasse zurücknehmen, sonst bleibt ein leerer Avatar-Kreis stehen,
             wo die Initialen hingehören (dieselbe Klasse wie beim Karten-Cover,
             Karte #450-Nacharbeit). */
          root.classList.remove('identity-avatar--image');
          return;
        }
        if (typeof trackUrl === 'function' && url.startsWith('blob:')) trackUrl(url);
        root.style.backgroundImage = `url("${url}")`;
        root.style.backgroundSize = 'cover';
        root.style.backgroundPosition = 'center';
      }).catch(() => {});
      return root;
    }
  }

  const glyph = document.createElement('span');
  glyph.className = 'identity-avatar-glyph';
  glyph.textContent = identity.icon || initialOf(identity.label);
  glyph.style.color = getContrastColor(identity.color || '#7c3aed');
  glyph.style.fontSize = `${Math.max(10, Math.floor(px * 0.55))}px`;
  glyph.setAttribute('aria-hidden', 'true');
  root.appendChild(glyph);
  return root;
}
