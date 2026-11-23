/* notifications-triggers.js — die fünf card.*-Events (Karte #339/#420, ADR-0107 §1).
 *
 * IS_SERVER_BUILD-gegated → im Slim- und claude-code-Build per DCE eliminiert.
 *
 * WARUM DIESES MODUL UND NICHT AUFRUFE IN modal.js/dragdrop.js
 *
 * Der agentContext von Karte #339 nannte drei getrennte Trigger-Stellen im
 * Modal (Owner-Toggle, Kommentar-Submit, blocked-Toggle). Die gibt es so
 * nicht: `modal.js` sammelt beim Schliessen ALLE Felder ein und feuert EIN
 * `card:updated` mit dem Gesamtzustand. Wer dort einzeln einhakte, müsste den
 * alten Wert selbst mitschleppen — an drei Stellen, jede mit eigener
 * Gelegenheit, ihn falsch zu erwischen.
 *
 * Der Handler in `main.js` hat dagegen beides nebeneinander liegen: die alte
 * Karte aus dem State und die neue aus dem Modal. Genau daraus lassen sich
 * die fünf Ereignisse ableiten. Dieses Modul ist deshalb eine reine
 * Diff-Funktion plus ein dünner Versand-Teil.
 *
 * WAS HIER BEWUSST NICHT PASSIERT
 *
 * - Kein `await` im Mutationspfad. `emit` läuft fire-and-forget mit
 *   `.catch()`; keine Kartenänderung darf daran scheitern, dass ein
 *   Benachrichtigungs-Endpunkt langsam oder tot ist.
 * - Keine Entscheidung darüber, OB zugestellt wird. Das ist Sache des
 *   Servers (Abonnements) und des Hauptschalters aus Karte #420. Hier wird
 *   nur gemeldet, was passiert ist.
 * - Kein Filtern des Auslösers. Der Server filtert den Caller aus
 *   `recipient_emails` (`resolveEmails`), und dort gehört es hin: Er weiss,
 *   WER die Anfrage stellt, das Frontend kennt nur einen Anzeigenamen.
 */

import { NotificationsClient } from './notifications-client.js';
import { emailsForLabels } from './identity.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Ereignis-Typen — müssen mit `server/notifications/event.go` übereinstimmen. */
export const EVENT_ASSIGNED  = 'card.assigned';
export const EVENT_COMMENTED = 'card.commented';
export const EVENT_MENTIONED = 'card.mentioned';
export const EVENT_MOVED     = 'card.moved';
export const EVENT_BLOCKED   = 'card.blocked';

/* Der Server kürzt ohnehin auf das Kanal-Limit (200 Zeichen E-Mail, 80
   Gotify). Hier wird trotzdem gekürzt, damit ein langer Kommentar nicht
   sinnlos über die Leitung geht — und damit der Body unter dem 16-KB-Limit
   von `handleEmitEvent` bleibt. */
const SNIPPET_MAX = 200;

function kurz(text) {
  const t = String(text ?? '').replace(/\s+/g, ' ').trim();
  return t.length <= SNIPPET_MAX ? t : t.slice(0, SNIPPET_MAX - 1) + '…';
}

/* Erwähnungen: `@name` im Kommentartext.
 *
 * Der agentContext warnt zu Recht: „Ein Regex, der zu gierig ist, verschickt
 * Post an Unbeteiligte — hier ist ein zu enger Treffer der harmlosere
 * Fehler." Deshalb wird NICHT versucht, mehrteilige Namen zu raten. Gematcht
 * wird ein zusammenhängendes Wort nach `@`, und getroffen wird nur, wer als
 * `kind: 'user'`-Identity mit gepflegter Adresse im Board steht — ein
 * `@irgendwas` ohne Entsprechung läuft ins Leere statt an jemanden.
 *
 * Die Zeichenklasse enthält Umlaute und Bindestrich, weil deutsche Namen die
 * enthalten; sie enthält KEIN Leerzeichen, weil sonst der halbe Satz nach dem
 * `@` als Name gälte. `\S@` davor schliesst Mailadressen aus (`a@b.de` ist
 * keine Erwähnung von `b`). */
const MENTION_RE = /(^|[^\w@äöüÄÖÜß])@([\wäöüÄÖÜß][\wäöüÄÖÜß.-]*)/gu;

export function extractMentions(text) {
  const out = [];
  const gesehen = new Set();
  for (const m of String(text ?? '').matchAll(MENTION_RE)) {
    /* Ein Satzpunkt direkt hinter dem Namen gehört nicht dazu. */
    const name = m[2].replace(/[.\-]+$/, '');
    const key = name.toLowerCase();
    if (name && !gesehen.has(key)) { gesehen.add(key); out.push(name); }
  }
  return out;
}

/* Die Empfänger einer Karte: Owner plus Assignees. */
function beteiligte(card) {
  const l = [];
  if (typeof card?.owner === 'string' && card.owner.trim()) l.push(card.owner.trim());
  for (const a of (Array.isArray(card?.assignees) ? card.assignees : [])) {
    if (typeof a === 'string' && a.trim()) l.push(a.trim());
  }
  return l;
}

/**
 * Leitet aus (alt, neu) die fälligen Ereignisse ab. REIN — kein Netzzugriff,
 * kein State, keine Uhr. Genau deshalb ist die Ableitung testbar, ohne einen
 * Server oder ein DOM zu brauchen.
 *
 * @param {object|null} alt  - Kartenzustand VOR der Änderung
 * @param {object} neu       - Kartenzustand NACH der Änderung
 * @param {object} board     - für `board.identities[].email`
 * @param {{neueKommentare?: Array}} [opts] - im Modal hinzugefügte Kommentare;
 *        wird nichts übergeben, leitet die Funktion sie aus der Differenz der
 *        `comments`-Arrays ab.
 * @returns {Array<{event_type: string, recipient_emails: string[], snippet: string}>}
 */
export function ereignisseFuerAenderung(alt, neu, board, opts = {}) {
  if (!neu) return [];
  const events = [];
  const mailsVon = (labels) => emailsForLabels(labels, board);

  /* ── card.assigned — nur an NEU Hinzugekommene ──────────────────────
     Wer schon vorher zugewiesen war, hat seine Benachrichtigung damals
     bekommen; ihn bei jeder weiteren Änderung erneut anzuschreiben wäre
     genau das Rauschen, an dem solche Systeme sterben. */
  const vorher = new Set(beteiligte(alt).map(x => x.toLowerCase()));
  const dazu = beteiligte(neu).filter(x => !vorher.has(x.toLowerCase()));
  if (dazu.length) {
    const mails = mailsVon(dazu);
    if (mails.length) {
      events.push({ event_type: EVENT_ASSIGNED, recipient_emails: mails, snippet: '' });
    }
  }

  /* ── card.commented / card.mentioned ────────────────────────────────
     Beide hängen an denselben neuen Kommentaren. Erwähnte bekommen ihr
     eigenes, dringlicheres Ereignis (Gotify-Priorität 7 statt 5) und werden
     aus der Kommentar-Liste herausgenommen — sonst bekäme dieselbe Person
     zwei Meldungen über denselben Text. */
  const neueKommentare = Array.isArray(opts.neueKommentare)
    ? opts.neueKommentare
    : diffKommentare(alt, neu);
  if (neueKommentare.length) {
    const text = neueKommentare.map(k => k?.text ?? '').join(' ');
    const erwaehnt = mailsVon(extractMentions(text));
    const erwaehntSet = new Set(erwaehnt.map(m => m.toLowerCase()));
    if (erwaehnt.length) {
      events.push({ event_type: EVENT_MENTIONED, recipient_emails: erwaehnt, snippet: kurz(text) });
    }
    const rest = mailsVon(beteiligte(neu)).filter(m => !erwaehntSet.has(m.toLowerCase()));
    if (rest.length) {
      events.push({ event_type: EVENT_COMMENTED, recipient_emails: rest, snippet: kurz(text) });
    }
  }

  /* ── card.blocked — nur die Flanke false→true ───────────────────────
     Das Entsperren ist keine schlechte Nachricht und braucht keine
     Benachrichtigung; ein Toggle, der bei jedem Speichern feuerte, wäre
     zudem laut ohne Anlass. */
  if (!alt?.blocked && neu.blocked) {
    const mails = mailsVon(beteiligte(neu));
    if (mails.length) {
      events.push({ event_type: EVENT_BLOCKED, recipient_emails: mails, snippet: '' });
    }
  }

  return events;
}

/* Neue Kommentare aus der Differenz der Arrays — Fallback, wenn der Aufrufer
   sie nicht mitgibt. Über die IDs, nicht über die Länge: Ein gelöschter und
   ein hinzugefügter Kommentar im selben Speichervorgang liessen die Länge
   gleich und der Vergleich schlüge fehl. */
function diffKommentare(alt, neu) {
  const alteIds = new Set((Array.isArray(alt?.comments) ? alt.comments : []).map(k => k?.id));
  return (Array.isArray(neu?.comments) ? neu.comments : []).filter(k => k && !alteIds.has(k.id));
}

/**
 * Das Verschiebe-Ereignis. Getrennt von `ereignisseFuerAenderung`, weil es
 * aus einem anderen Pfad kommt (Drag-and-Drop, nicht Modal-Speichern).
 *
 * Nur Spalten- oder Swimlane-Wechsel zählen. Ein Umsortieren INNERHALB
 * derselben Spalte ist keine Zustandsänderung, über die jemand Post bekommen
 * möchte — und beim Aufräumen einer Spalte wären es sonst zehn Mails.
 */
export function ereignisFuerVerschieben(alt, neu, board) {
  if (!alt || !neu) return null;
  const spalteAnders = alt.columnId !== neu.columnId;
  const laneAnders   = alt.sprintId !== neu.sprintId;
  if (!spalteAnders && !laneAnders) return null;
  const mails = emailsForLabels(beteiligte(neu), board);
  if (!mails.length) return null;
  return {
    event_type: EVENT_MOVED,
    recipient_emails: mails,
    snippet: kurz(spalteAnders ? `Neue Spalte: ${neu.columnTitle ?? neu.columnId}` : 'Andere Swimlane'),
  };
}

/**
 * Schickt die abgeleiteten Ereignisse. Fire-and-forget.
 *
 * `NotificationsClient` wirft nie (es liefert `{ok,status,body}`), aber ein
 * `.catch()` steht trotzdem da: Ein künftiger Umbau des Clients darf keinen
 * unbehandelten Promise-Fehler in den Mutationspfad tragen.
 */
export function sendeEreignisse(boardId, board, card, events) {
  if (!IS_SERVER_BUILD || !NotificationsClient || !boardId) return;
  for (const ev of (events || [])) {
    if (!ev || !ev.recipient_emails?.length) continue;
    try {
      Promise.resolve(NotificationsClient.emit(boardId, {
        event_type:       ev.event_type,
        card_id:          card?.id ?? '',
        card_display_id:  Number.isFinite(card?.displayId) ? card.displayId : 0,
        card_title:       String(card?.title ?? ''),
        board_name:       String(board?.title ?? ''),
        snippet:          ev.snippet ?? '',
        recipient_emails: ev.recipient_emails,
      })).catch(err => console.warn('[notifications] emit fehlgeschlagen:', ev.event_type, err));
    } catch (err) {
      console.warn('[notifications] emit fehlgeschlagen:', ev.event_type, err);
    }
  }
}
