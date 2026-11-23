/* Attachment-Storage (→ ADR-0033)

   Zwei Backends, ein Interface:
   - Projekt-Modus (FSA): Binärdateien liegen in attachments/<card.id>/<attachment.id>__<filename>
   - Standalone-Modus:    Base64 inline im attachment.data-Feld

   Beide Pfade liefern dieselben Attachment-Metadaten (id, filename, mime, size, created).
   Nur im Standalone-Modus enthält das Objekt zusätzlich `data` (Base64). */

import { Config }  from './config.js';
import { Profile } from './profile.js';
import { Sync }    from './sync.js';
import { State }   from './state.js';
import { I18n }    from './i18n.js';

const ATTACHMENTS_DIR = 'attachments';

/* ── Helfer ────────────────────────────────────────────────────────── */

/* Filename-Escape: Pfadtraversierungs-Zeichen + Null-Byte neutralisieren,
   Windows-reservierte Namen mit Suffix entschärfen (F-129).
   Hinweis: das Encoding bleibt verlustarm — `__` ist der Separator zum UUID-Präfix
   (siehe ADR-0033), darum hier nicht angefasst. */
const WIN_RESERVED = /^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\..*)?$/i;
export function safeFilename(name) {
  let n = (name || 'datei').replace(/[/\\\0]/g, '_');
  if (WIN_RESERVED.test(n)) n = `_${n}`;
  return n;
}

/* F-1088: Mail-Ingest-Altbestand aus der Zeit vor Sprint 69 (F-358) trägt
   card.attachments-Einträge als reine id-Strings statt der sonst üblichen
   Objekte {id, filename, mime, size}. Seit F-855 bestimmt der SERVER beim
   Senden Typ und Größe eines solchen Eintrags aus der Datei selbst — im
   Karten-Zustand steht der Alteintrag aber unverändert als nackter String,
   und jeder Frontend-Leser, der `.id`/`.mime`/`.filename`/`.size` erwartet,
   sieht bei einem String nur `undefined`.

   normalisiereAnhang liest EINEN Eintrag und macht daraus immer ein Objekt
   mit mindestens `id` — ein String wird zu `{ id: s }`, ein Objekt bleibt
   unverändert (auch mit allen zusätzlichen Feldern). Das ist bewusst NUR eine
   Lese-Normalisierung: Es wird nichts zurückgeschrieben, keine Migration.
   Ohne `mime`/`filename`/`size` bleiben diese Felder undefined — Aufrufer, die
   das nicht vertragen (z. B. eine Typ-Prüfung), müssen selbst absichern; siehe
   `_bildAnhaenge` in llm-tab.js und `renderAttachmentEntry` in modal.js. */
export function normalisiereAnhang(a) {
  if (typeof a === 'string') return a ? { id: a } : null;
  return a;
}

/* Bytes lesbar formatieren */
export function humanSize(bytes) {
  if (bytes == null || isNaN(bytes)) return '–';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return I18n.t('attachment.size-kb', (bytes / 1024).toFixed(0));
  return I18n.t('attachment.size-mb', (bytes / (1024 * 1024)).toFixed(1));
}

/* Base64-String → Byte-Anzahl (Base64-Länge × 3/4 minus Padding) */
function base64ByteLength(b64) {
  if (!b64) return 0;
  const padding = b64.endsWith('==') ? 2 : b64.endsWith('=') ? 1 : 0;
  return Math.floor((b64.length * 3) / 4) - padding;
}

/* Summe aller eingebetteten Base64-Daten im Board (in Bytes) */
/* Was ein einzelner Anhang im Board-State wiegt.
 *
 * `thumb` zählt mit (Karte #467, beim Abhängigkeits-Check des
 * Sprint-63-Abschlusses gefunden): Das Cover-Thumbnail liegt als base64 am
 * Anhang-Objekt und reist damit bei jedem PUT und jedem Poll mit — genau wie
 * `data`. Es NICHT zu zählen wäre derselbe Defekt wie [[F-835]], nur an der
 * Nachbarstelle: Die Prüfung, die vor dem 413 schützen soll, sähe einen Teil
 * des Bodys nicht.
 *
 * WO DIE ZAHL WIRKLICH GATET — die erste Fassung dieses Kommentars hat sich
 * hier vertan (Kritiker-Befund Sprint 63): Sie nannte den Server-Modus als
 * schärfsten Fall. Dort wird der Wert für diese Entscheidung aber gar nicht
 * gelesen: `createRESTAttachmentBackend` reicht bei beiden Prüfungen
 * `imBoardState: false` durch, und `checkUpload` steigt oben aus, BEVOR
 * `computeBoardBytes` läuft. Übrig bleibt dort die Anzeige (`boardSizeLabel`).
 *
 * Gatend ist die Zahl im Solo- und Team-Profil — dort liegen `data` UND
 * `thumb` im State, und die Summe entscheidet über die Warn- und Hartgrenze.
 * Für den `claude-code`-Modus gilt sie gar nicht (kein
 * `feature.attachmentLimits`); dort ist der Effekt ein anderer, aber auch
 * nicht harmlos: `aboard-sync.json` ist versioniert, jedes Thumbnail steht
 * also dauerhaft in der Historie.
 *
 * Die Zeile bleibt in jedem Fall richtig — die Anzeige soll die Wahrheit
 * sagen, und die Grenzen sollen zählen, was tatsächlich im Body steht. Nur
 * die Begründung musste stimmen: Eine Erklärung, die etwas anderes behauptet
 * als der Code tut, ist genau der Mangel, den [[F-835]] anprangert. */
function attachmentStateBytes(a) {
  if (!a) return 0;
  return (a.data ? base64ByteLength(a.data) : 0)
       + (a.thumb ? base64ByteLength(a.thumb) : 0);
}

export function computeBoardBytes(board) {
  let total = 0;
  /* Karten-Anhänge */
  (board?.sprints || []).forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      (card.attachments || []).forEach(a => { total += attachmentStateBytes(a); });
    });
  });
  /* Karte #114: Board-Anhänge (Hintergrundbild) */
  (board?.attachments || []).forEach(a => { total += attachmentStateBytes(a); });
  return total;
}

/* ── Limit-Checks ──────────────────────────────────────────────────── */

/* base64 kodiert 3 Bytes in 4 Zeichen. Wer gegen eine Uebertragungs- oder
   Speichergrenze prueft, muss diesen Aufschlag mitrechnen — der State traegt
   den kodierten String, nicht die Rohdaten. */
export const BASE64_FAKTOR = 4 / 3;

/* Vor dem Upload prüfen: Pro-Datei + Board-Hartlimit (im Standalone-Modus).
   Liefert { ok, reason?, warning? } — bei `warning` darf der Upload trotzdem laufen.

   `opts.imBoardState` (Karte #450): Die Board-Grössenprüfung ist nur sinnvoll,
   wenn die Datei tatsächlich IM BOARD-STATE landet und damit den PUT-Body bzw.
   die localStorage-Quota belastet. Seit dem Umbau auf `Storage.plugin.attachments()`
   reisen Karten-Anhänge im Server-Modus per Multipart an einen eigenen Endpunkt
   mit eigenem Limit — sie tragen zum State nichts bei. Die Board-Prüfung dort
   trotzdem laufen zu lassen, hiesse ein volles Board für Uploads zu sperren,
   die es gar nicht voller machen. Board-Anhänge (Hintergrundbild) liegen
   dagegen weiterhin als base64 im State (Phase 1) — dort gilt sie unverändert.
   Default `true`: wer nichts angibt, bekommt das strengere Verhalten. */
export function checkUpload(file, board, opts = {}) {
  const imBoardState = opts.imBoardState !== false;
  /* Attachment-Limits gelten nur bei Profilen mit `feature.attachmentLimits`.
     Seit Karte #450 sind das Solo, Team UND Server — im Server-Modus reist ein
     Anhang als base64 im Board-State und geht gegen die PUT-Body-Grenze; der
     fruehere Zusatz „(Solo + Team)" ist seither falsch. `claude-code` bleibt
     aussen vor (FSA-Volume, keine Grenze). */
  if (!Profile.has('feature.attachmentLimits')) return { ok: true };

  /* `opts.maxBytes` (Karte #450-Nacharbeit): Das Pro-Datei-Limit hängt am
     ZIEL, nicht am Board. `attachmentMaxBytes` (2 MB) ist für den
     base64-im-State-Weg gedacht; ein REST-Karten-Anhang geht an einen
     Endpunkt mit eigenem, viel höherem Limit. Wer den strengeren Wert dort
     anwendet, lehnt Dateien ab, die der Server klaglos nähme. */
  const max  = Number.isFinite(opts.maxBytes) ? opts.maxBytes : Config.get('attachmentMaxBytes');
  const warn = Config.get('attachmentBoardWarnBytes');
  const hard = Config.get('attachmentBoardHardBytes');

  if (file.size > max) {
    return { ok: false, reason: I18n.t('attachment.too-large', file.name, humanSize(max)) };
  }
  /* Datei landet nicht im State → die Board-Grössenprüfung ist gegenstandslos.
     Das Pro-Datei-Limit oben gilt weiter: es schützt vor dem 413 des
     Attachment-Endpunkts, den der Nutzer sonst erst nach dem Hochladen sähe. */
  if (!imBoardState) return { ok: true };

  const currentBytes = computeBoardBytes(board);
  /* Gegen die TRANSPORTGROESSE pruefen, nicht gegen die Nutzdaten (Karte #450).
     `computeBoardBytes` liefert die decodierte Groesse — im State steht aber der
     base64-String, und der ist 4/3 so lang. Genau diese Differenz entscheidet:
     Ein Board mit 8 MB Nutzdaten ergibt einen PUT-Body von rund 10,6 MB und
     scheitert an `MaxBodyBytes = 10 MB` (server/api/middleware.go), waehrend die
     alte Rechnung „8 MB < 10 MB, alles gut" sagte. Die Pruefung haette also
     genau den Fall durchgelassen, fuer den es sie gibt.
     Im Solo-Modus gilt derselbe Faktor gegen die localStorage-Quota; die
     Rechnung war dort ebenso zu optimistisch, nur ohne Server-Fehlermeldung.
     Der JSON-Overhead (Feldnamen, Anfuehrungszeichen, Karten, Kommentare,
     Aktivitaetslogs) kommt obendrauf und ist bewusst NICHT eingerechnet.
     F-828-Korrektur (Karte #454): Die fruehere Begruendung an dieser Stelle —
     „der Puffer zwischen Warn- und Hartgrenze faengt ihn" — war falsch. Dieser
     Puffer liegt UNTERHALB der Hartgrenze und schuetzt gegen deren
     Ueberschreiten nichts. Aufgefangen wird der Overhead seither dadurch, dass
     die Hartgrenze im Server-Build 2 MB unter `MaxBodyBytes` liegt
     (`SERVER_ATTACHMENT_HARD_CAP_BYTES` in src/config.js). Auch das ist eine
     Heuristik und keine Garantie — ein Board mit sehr vielen Kommentaren kann
     die Marge aufbrauchen. */
  const projected    = Math.round((currentBytes + file.size) * BASE64_FAKTOR);
  if (projected > hard) {
    return { ok: false, reason: I18n.t('attachment.board-hard', humanSize(projected), humanSize(hard)) };
  }
  if (projected > warn) {
    return { ok: true, warning: I18n.t('attachment.board-warn', humanSize(projected), humanSize(warn)) };
  }
  return { ok: true };
}

/* ── FSA-Helpers ───────────────────────────────────────────────────── */

async function getAttachmentsDir(create = true) {
  const dirHandle = Sync.getDirectoryHandle();
  if (!dirHandle) return null;
  try {
    return await dirHandle.getDirectoryHandle(ATTACHMENTS_DIR, { create });
  } catch { return null; }
}

async function getCardDir(id, create = true) {
  const root = await getAttachmentsDir(create);
  if (!root) return null;
  try { return await root.getDirectoryHandle(id, { create }); } catch { return null; }
}

function attachmentFilename(att) {
  return `${att.id}__${safeFilename(att.filename)}`;
}

/* Datei aus FSA-Ordner als File-Objekt holen — null bei Fehler */
async function fsaGetFile(cardId, att) {
  const dir = await getCardDir(cardId, false);
  if (!dir) return null;
  try {
    const fh = await dir.getFileHandle(attachmentFilename(att));
    return await fh.getFile();
  } catch { return null; }
}

async function fsaWriteFile(cardId, att, blob) {
  const dir = await getCardDir(cardId, true);
  if (!dir) return false;
  try {
    const fh = await dir.getFileHandle(attachmentFilename(att), { create: true });
    const w  = await fh.createWritable();
    await w.write(blob);
    await w.close();
    return true;
  } catch { return false; }
}

/* Wie `getCardDir(id, false)`, aber es unterscheidet „gibt es nicht" von
   „komme nicht dran" — und genau darauf kommt es beim Löschen an.
   `getCardDir` wirft beide Fälle in dasselbe `null`: kein Volume-Handle
   (Sync inaktiv), entzogene FSA-Berechtigung (`NotAllowedError`) und ein
   schlicht nicht vorhandenes Verzeichnis (`NotFoundError`) sehen für den
   Aufrufer gleich aus. Für Lesepfade ist das richtig (kein Bild ist kein
   Bild); für den Löschpfad ist es der Unterschied zwischen „erledigt" und
   „nicht angefasst" (Sprint-62-Kritiker, K1).

   Rückgabe `{ dir, fehlt }`: `dir` ist das Verzeichnis oder `null`; `fehlt`
   sagt, ob das `null` bedeutet „existiert nachweislich nicht". */
async function getCardDirZumLoeschen(cardId) {
  const dirHandle = Sync.getDirectoryHandle();
  /* Kein Volume-Handle heisst NICHT „nichts zu löschen" — es heisst, dass wir
     gar nicht nachsehen können. */
  if (!dirHandle) return { dir: null, fehlt: false };
  try {
    const root = await dirHandle.getDirectoryHandle(ATTACHMENTS_DIR, { create: false });
    return { dir: await root.getDirectoryHandle(cardId, { create: false }), fehlt: false };
  } catch (e) {
    return { dir: null, fehlt: e?.name === 'NotFoundError' };
  }
}

/* Rückgabe: `true`, wenn die Datei danach sicher weg ist — einschliesslich
   „war schon weg" (Verzeichnis oder Datei nachweislich nicht vorhanden).
   `false`, wenn das Löschen an etwas anderem gescheitert ist ODER wir nicht
   nachsehen konnten; dann existiert die Datei möglicherweise noch, und der
   Aufrufer darf ihren Eintrag NICHT aus dem Board entfernen, sonst bleibt sie
   unreferenziert liegen (F-871).

   Die erste Fassung gab bei jedem `null` aus `getCardDir` ein `true` zurück
   und behauptete im Kommentar „kein Verzeichnis = war schon weg". Das stimmte
   für einen der drei Fälle, die zu `null` führen — bei entzogener
   FSA-Berechtigung meldete sie Erfolg, während die Datei liegenblieb. Genau
   der Zustand, den F-871 ausschliessen sollte, nur in der anderen Hälfte
   (Sprint-62-Kritiker, K1).

   Der Karten-Pfad ignoriert die Rückgabe weiterhin — er löscht ganze
   Verzeichnisse und hat den Fall nicht. */
async function fsaDeleteFile(cardId, att) {
  const { dir, fehlt } = await getCardDirZumLoeschen(cardId);
  if (!dir) return fehlt;
  try { await dir.removeEntry(attachmentFilename(att)); }
  catch (e) { return e?.name === 'NotFoundError'; }
  return true;
}

/* Den ganzen Card-Ordner entfernen — beim Karten-Löschen */
export async function purgeCardAttachments(cardId) {
  /* F-194: FSA-Verzeichnis-Bereinigung nur bei Profilen mit FSA-Volume (claude-code) */
  if (!Profile.has('feature.fsaVolumeAttachments')) return;
  const root = await getAttachmentsDir(false);
  if (!root) return;
  try { await root.removeEntry(cardId, { recursive: true }); } catch {}
}

/* Karte #114: Board-Attachments — fester Unterordner-Name "__board__" */
const BOARD_ATTACH_DIR = '__board__';
const fsaGetBoardFile    = (att)       => fsaGetFile(BOARD_ATTACH_DIR, att);
const fsaWriteBoardFile  = (att, blob) => fsaWriteFile(BOARD_ATTACH_DIR, att, blob);
const fsaDeleteBoardFile = (att)       => fsaDeleteFile(BOARD_ATTACH_DIR, att);

/* ── Base64-Helpers (Standalone) ───────────────────────────────────── */

function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload  = () => {
      const result = reader.result || '';
      const idx = result.indexOf(',');
      resolve(idx >= 0 ? result.slice(idx + 1) : result);
    };
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

function base64ToBlob(b64, mime) {
  const bin = atob(b64);
  const len = bin.length;
  const bytes = new Uint8Array(len);
  for (let i = 0; i < len; i++) bytes[i] = bin.charCodeAt(i);
  return new Blob([bytes], { type: mime || 'application/octet-stream' });
}

/* ── Public API ────────────────────────────────────────────────────── */

/* Liefert das fertige Attachment-Objekt für die Persistenz (im Card).
   Im Projekt-Modus wird die Binärdatei in den FSA-Ordner geschrieben.
   Im Standalone-Modus liegt der Base64-String im `data`-Feld.
   Wirft bei Schreibfehler. */
export async function uploadFile(file, card) {
  const att = {
    id:       crypto.randomUUID(),
    filename: file.name || 'datei',
    mime:     file.type || 'application/octet-stream',
    size:     file.size,
    created:  new Date().toISOString().slice(0, 10),
  };

  /* F-194: FSA-Volume nur in claude-code; server lädt via REST (createRESTAttachmentBackend, #74) */
  const isFsaVolume = Profile.has('feature.fsaVolumeAttachments');
  if (isFsaVolume) {
    const ok = await fsaWriteFile(card.id, att, file);
    if (!ok) throw new Error('fsa-write-failed');
    return att;
  }

  att.data = await fileToBase64(file);
  return att;
}

/* Liefert ein Blob für Vorschau/Download — egal in welchem Modus */
export async function readAttachment(card, att) {
  if (att.data) return base64ToBlob(att.data, att.mime);
  if (Profile.has('feature.fsaVolumeAttachments')) {
    return await fsaGetFile(card.id, att);
  }
  return null;
}

/* RFC-6838-vereinfacht: type/subtype, jeweils aus ALPHANUM + .+-
   Verhindert CSS-Injection bei `url(data:${mime};base64,...)` (F-162). */
const MIME_RE = /^[A-Za-z0-9.+\-]+\/[A-Za-z0-9.+\-]+$/;
export function safeMime(mime) {
  return (typeof mime === 'string' && MIME_RE.test(mime)) ? mime : 'application/octet-stream';
}

/* F-1107 (Nebenbefund der Server-Nacharbeit zu F-855/#538): SVG-Ausschluss
   (F-128 — SVG kann Skripte enthalten, wird deshalb nie als <img> gerendert
   oder als Cover angeboten) prüfte an vier Stellen im Frontend
   (`src/modal.js` zweimal, `src/renderer.js` zweimal) bislang exakt den
   String `'image/svg+xml'`. Ein Bestandsanhang mit `image/svg`,
   `image/svg-xml` oder Groß-/Kleinschreibungs-Varianten (`image/SVG+XML`)
   lief am Vergleich vorbei und wurde trotzdem als Bild angeboten. Diese
   Prüfung ist bewusst weiter als der exakte MIME-Typ — jedes `image/*`,
   das den Teilstring `svg` enthält (ohne Rücksicht auf Groß-/Kleinschreibung),
   gilt als SVG. Transportfrei (reine String-Prüfung, kein I/O) — steht daher
   auch in der Positivliste `tests/anhang-transportweg-450.test.js`. */
export function istSvgMime(mime) {
  return typeof mime === 'string' && /^image\//i.test(mime) && /svg/i.test(mime);
}

/* Anzeigbare URL für ein Attachment.
   Achtung: Aufrufer ist verantwortlich, ObjectURL nach Gebrauch zu revoken. */
export async function attachmentUrl(card, att) {
  if (att.data) {
    return `data:${safeMime(att.mime)};base64,${att.data}`;
  }
  const blob = await readAttachment(card, att);
  return blob ? URL.createObjectURL(blob) : null;
}

/* Einzelnes Attachment entfernen — Persistenz (FSA-Datei) + Card-Mutation */
export async function deleteAttachment(card, att) {
  if (Profile.has('feature.fsaVolumeAttachments')) {
    await fsaDeleteFile(card.id, att);
  }
}

/* Download-Trigger über ein freies <a download>-Element */
export async function downloadAttachment(card, att) {
  const blob = await readAttachment(card, att);
  if (!blob) return false;
  const url = URL.createObjectURL(blob);
  const a   = document.createElement('a');
  a.href = url;
  a.download = att.filename || 'datei';
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  return true;
}

/* Board-Gesamtgröße als formatiertes Label — für UI-Hinweis im Anhänge-Tab */
export function boardSizeLabel() {
  const state = State.get();
  return humanSize(computeBoardBytes(state?.board));
}

/* ── Board-Attachment API (Karte #114) ─────────────────────────────── */

/* Zielkantenlänge und Qualität für Hintergrundbilder (Karte #439).
   1920 px reicht für jedes Display, auf dem ein Board sinnvoll bedienbar ist —
   das Bild liegt ohnehin mit `opacity:.15` hinter dem Inhalt. JPEG-Qualität
   0.82 ist der übliche Punkt, an dem Artefakte bei Fotos noch nicht auffallen.
   Aus 4 MB werden damit typischerweise 200–400 KB. */
export const BG_MAX_KANTE = 1920;
/* Formate, die immer animiert sein KÖNNEN und pauschal ausgenommen bleiben.
   PNG und WebP stehen bewusst NICHT hier: Beide tragen als Standbild denselben
   MIME-Typ wie in ihrer animierten Form (APNG bzw. animiertes WebP), und sie
   pauschal auszunehmen würde den häufigen Fall (ein grosses statisches Bild)
   wegen eines Randfalls verschlechtern. Beide werden stattdessen an ihrem
   Container-Chunk erkannt — siehe `istAnimiert` und `ALS_ANIMIERT_PRUEFBAR`.
   JPEG fehlt, weil es keine Animation tragen kann. */
const ANIMIERBARE_FORMATE = new Set(['image/gif', 'image/apng']);

/* WARUM `image/webp` SEIT SPRINT 63 NICHT MEHR IN DIESER LISTE STEHT
 * (Kritiker-Befund zum Sprint-63-Abschluss, `[K]` P1):
 *
 * Bis Karte #466 war WebP ausschliesslich ein EINGABE-Format, und die pauschale
 * Ausnahme kostete nichts ausser der Verkleinerung eines grossen statischen
 * WebP. Seit #466 ist WebP das AUSGABE-Format dieser Funktion für Bilder mit
 * Transparenz — und damit erzeugte `verkleinereBild` Dateien, die sie selbst
 * nie wieder anfasst.
 *
 * Das nahm Karte #467 genau die Bilder weg, für die sie gebaut wurde: Ein
 * freigestelltes PNG wird im Solo-Profil beim Anhängen zu WebP (#466); wird es
 * danach als Cover gesetzt, läuft `erzeugeThumbnail` erneut durch
 * `verkleinereBild` — und stieg hier mit dem Original aus. Kein Thumbnail, und
 * die Karte lud weiter das 1920-px-Bild nach. Fehlerklasse 57: zwei Fixes,
 * einzeln richtig, zusammen ein Loch.
 *
 * Die Lösung ist dieselbe wie bei PNG/APNG: nicht das Format pauschal
 * ausnehmen, sondern die Animation ERKENNEN. Ein animiertes WebP trägt einen
 * `ANIM`-Chunk im RIFF-Container; ein statisches nicht. Damit wird ein
 * statisches WebP wie jedes andere Rasterbild behandelt, und die ursprüngliche
 * Sorge („eine Canvas-Umwandlung nimmt nur den ersten Frame") bleibt für den
 * Fall gedeckt, für den sie gilt. */
const ALS_ANIMIERT_PRUEFBAR = new Set(['image/png', 'image/webp']);

/* Erkennt ein animiertes PNG am `acTL`-Chunk.
 *
 * Die APNG-Spezifikation verlangt, dass `acTL` VOR dem ersten `IDAT` steht —
 * es genügt also, den Kopf zu lesen statt die ganze Datei. 64 KB sind
 * grosszügig: `acTL` folgt üblicherweise direkt auf `IHDR`, also innerhalb der
 * ersten 100 Bytes; die Reserve deckt Dateien mit umfangreichen Metadaten
 * (ICC-Profil, EXIF) davor ab.
 *
 * Im Zweifel `false`: Ein nicht erkanntes APNG wird verkleinert und verliert
 * seine Animation — ärgerlich, aber der Nutzer sieht sofort, dass etwas fehlt.
 * Ein fälschlich als animiert eingestuftes Standbild bliebe dagegen still zu
 * gross und liefe in genau das 413, gegen das die Verkleinerung antritt.
 */
/* Sucht eine Vier-Byte-Marke im Dateikopf. Kein TextDecoder — der stolpert
   über Binärdaten. */
function enthaeltMarke(bytes, marke) {
  for (let i = 0; i + 3 < bytes.length; i++) {
    if (bytes[i] === marke[0] && bytes[i + 1] === marke[1] &&
        bytes[i + 2] === marke[2] && bytes[i + 3] === marke[3]) return true;
  }
  return false;
}

const MARKE_ACTL = [0x61, 0x63, 0x54, 0x4C];   /* 'acTL' — APNG */
const MARKE_ANIM = [0x41, 0x4E, 0x49, 0x4D];   /* 'ANIM' — animiertes WebP */

/* Erkennt ein animiertes Bild an seinem Container-Chunk.
 *
 * PNG: `acTL` (siehe Begründung oben). WebP: `ANIM` im RIFF-Container — ein
 * animiertes WebP führt ihn direkt nach `VP8X`, ein statisches hat ihn gar
 * nicht. Beide stehen weit vorne, das 64-KB-Fenster reicht grosszügig.
 *
 * Seit Sprint 63 gilt das auch für WebP, weil `verkleinereBild` es selbst
 * ausgibt und ein pauschaler Ausschluss dem Cover-Thumbnail den Boden entzog
 * (Begründung bei `ALS_ANIMIERT_PRUEFBAR`). */
async function istAnimiert(file) {
  try {
    const kopf = new Uint8Array(await file.slice(0, 65536).arrayBuffer());
    if (file.type === 'image/webp') return enthaeltMarke(kopf, MARKE_ANIM);
    return enthaeltMarke(kopf, MARKE_ACTL);
  } catch {
    return false;
  }
}
export const BG_QUALITAET = 0.82;

/* ── Transparenz-Erhalt beim Verkleinern (Karte #466, F-821-Rest) ─────
 *
 * Bis Sprint 63 schrieb `verkleinereBild` IMMER `image/jpeg` zurück. JPEG
 * kennt keinen Alphakanal: Ein freigestelltes Logo, eine Grafik mit
 * transparentem Rand oder ein Screenshot mit Fensterschatten verlor seine
 * Transparenz — still, nicht umkehrbar, und das Original wird nicht
 * aufbewahrt.
 *
 * WARUM DIE ENTSCHEIDUNG AN DEN PIXELN HÄNGT UND NICHT AM FORMAT: Die
 * naheliegende Regel „PNG bleibt PNG" (so stand sie als Empfehlung auf der
 * Karte) ist an einer Stelle teuer. Gemessen mit Pillow an skalierten
 * 1920-px-Bildern, JPEG q=0.82 gegen PNG (optimize) und WebP q=0.82:
 *
 *   freigestelltes Logo (Alpha)      JPEG   92 KB   PNG    87 KB   WebP  42 KB
 *   UI-Screenshot + Schatten (Alpha) JPEG  162 KB   PNG    32 KB   WebP  19 KB
 *   freigestelltes FOTO (Alpha)      JPEG  447 KB   PNG  3294 KB   WebP 404 KB
 *   Foto, opak                       JPEG  767 KB   PNG  5240 KB   WebP 797 KB
 *
 * Bei Grafiken ist PNG billiger als JPEG — bei einem freigestellten Foto aber
 * das 7,4-fache. Als base64 im Board-State (Faktor 4/3) sind das 4,4 MB aus
 * einem Bild, und genau davor schützt die Verkleinerung (die 10-MB-Grenze des
 * Board-Endpunkts, F-845). Die Formatregel hätte den Verlustfall behoben und
 * dafür den Fall wieder aufgerissen, für den die Funktion gebaut wurde.
 *
 * WebP löst beides: Es trägt einen Alphakanal und liegt in JEDEM gemessenen
 * Fall bei oder unter der JPEG-Grösse. Es ist deshalb das Zielformat, wenn
 * Transparenz vorhanden ist — und nur dann. Ein opakes PNG (Screenshot ohne
 * Rand) geht weiterhin den JPEG-Weg; dort gibt es nichts zu erhalten.
 *
 * Die EINE Wahrheit dieser Entscheidung ist „hat das gezeichnete Bild
 * Pixel mit a < 255?". Das Quellformat ist nur ein Vorfilter, der die Messung
 * dort spart, wo sie nicht nötig ist (JPEG kann kein Alpha tragen). */

/* Formate, die einen Alphakanal tragen KÖNNEN — nur bei ihnen wird gemessen. */
const ALPHA_FAEHIGE_FORMATE = new Set(['image/png', 'image/webp', 'image/avif']);
/* Zielformat für Bilder MIT Transparenz (Begründung oben). */
const ALPHA_ZIEL_TYP = 'image/webp';
/* Dateiendung je Ausgabeformat — der Name muss zum Inhalt passen. */
const ENDUNG_JE_TYP = /*#__PURE__*/ Object.freeze({
  'image/jpeg': '.jpg', 'image/webp': '.webp', 'image/png': '.png', 'image/avif': '.avif',
});

/* Findet mindestens ein nicht vollständig deckendes Pixel in RGBA-Daten.
 *
 * Rein und ohne Canvas — damit die Regel mit echten Pixelwerten prüfbar ist.
 * happy-dom liefert weder `drawImage` noch `getImageData` (beide sind dort
 * `null`), ein Test kann die Transparenz also nicht aus einer echten
 * Bilddatei gewinnen; er kann aber diese Funktion mit echten Alpha-Werten
 * füttern. Genau dafür ist sie exportiert.
 *
 * @param {Uint8ClampedArray|number[]} daten RGBA-Quadrupel
 * @returns {boolean}
 */
export function hatTransparentePixel(daten) {
  if (!daten || typeof daten.length !== 'number') return false;
  for (let i = 3; i < daten.length; i += 4) {
    if (daten[i] < 255) return true;
  }
  return false;
}

/* Das Ausgabeformat für ein bereits auf den Canvas gezeichnetes Bild.
 *
 * Im Zweifel wird Transparenz ANGENOMMEN: Kann die Umgebung die Pixel nicht
 * lesen (kein `getImageData`, oder es wirft), fällt die Entscheidung auf das
 * alphafähige Format. Der Preis dafür ist im schlechtesten Fall eine grössere
 * Datei — der Preis der Gegenrichtung wäre ein stiller, nicht umkehrbarer
 * Datenverlust. */
function ermittleZielTyp(quellTyp, ctx, cv) {
  if (!ALPHA_FAEHIGE_FORMATE.has(quellTyp)) return 'image/jpeg';
  if (!ctx || typeof ctx.getImageData !== 'function') return ALPHA_ZIEL_TYP;
  try {
    const bild = ctx.getImageData(0, 0, cv.width, cv.height);
    return hatTransparentePixel(bild && bild.data) ? ALPHA_ZIEL_TYP : 'image/jpeg';
  } catch {
    return ALPHA_ZIEL_TYP;
  }
}

/* Verkleinert ein Bild auf `maxKante`, wenn es grösser ist.
 *
 * WARUM DAS NICHT NUR KOSMETIK IST (Karte #439): Im Server-Modus landet ein
 * Board-Attachment als base64 IM BOARD-STATE — es geht bei jedem Speichern per
 * PUT und bei jedem Poll komplett über die Leitung. Ein 4-MB-Foto wird als
 * base64 zu 5,3 MB; zusammen mit dem übrigen State reisst das die 10-MB-Grenze
 * des Board-Endpunkts, und der Nutzer bekommt einen stillen 413 statt eines
 * Hintergrundbildes. Genau so ist der Fehler auf dem produktiven Server
 * aufgetreten.
 *
 * Die Verkleinerung ist der robusteste Punkt für den Eingriff: Sie wirkt
 * unabhängig davon, welche Grenze zuerst greift (Go-Handler, Reverse-Proxy,
 * localStorage-Quota im Solo-Modus) und macht den State auch für alle anderen
 * Modi klein. Ein höheres Limit wäre ein Pflaster gewesen — der State würde
 * weiterhin bei jedem Poll mehrere Megabyte übertragen.
 *
 * Gibt die Originaldatei unverändert zurück, wenn sie klein genug ist, wenn
 * das Format nicht rastert (SVG ist ohnehin ausgeschlossen, F-128) oder wenn
 * die Umwandlung scheitert — ein misslungenes Verkleinern darf den Upload
 * nicht verhindern, sondern höchstens gross lassen.
 *
 * AUSGABEFORMAT (seit Karte #466): `image/jpeg` wie bisher — ausser das Bild
 * hat transparente Pixel, dann `image/webp` (bzw. das, was der Browser daraus
 * macht, solange es Alpha trägt). Die Begründung samt Messwerten steht bei
 * `ermittleZielTyp` weiter oben. Ein WebP, das hier ENTSTEHT, läuft bei einem
 * späteren Durchlauf normal durch die Verkleinerung — genau deshalb steht WebP
 * seit dem Sprint-63-Abschluss nicht mehr pauschal in `ANIMIERBARE_FORMATE`
 * (sonst bekäme ein solches Bild nie ein Cover-Thumbnail).
 *
 * @param {File|Blob} file
 * @param {{maxKante?: number, qualitaet?: number}} [opts]
 * @returns {Promise<File|Blob>}
 */
export async function verkleinereBild(file, opts = {}) {
  const maxKante  = opts.maxKante  ?? BG_MAX_KANTE;
  const qualitaet = opts.qualitaet ?? BG_QUALITAET;
  if (!file || typeof file.type !== 'string' || !file.type.startsWith('image/')) return file;
  /* Animierbare Rasterformate bleiben unangetastet — eine Canvas-Umwandlung
     nimmt nur den ersten Frame und wirft die Animation weg. Lieber gross und
     richtig als klein und kaputt.

     GIF steht pauschal in der Liste, weil dort jede Datei animiert sein kann
     und es keinen billigen Standbild-Nachweis gibt. PNG und WebP stehen NICHT
     darin: Beide verstecken ihre animierte Form hinter demselben MIME-Typ,
     werden aber an ihrem Container-Chunk erkannt (`istAnimiert`, Zeile
     darunter) und sonst wie jedes andere Rasterbild verkleinert.

     Bei WebP war das bis Sprint 63 anders — es stand pauschal in der Liste.
     Das ging schief, sobald `verkleinereBild` WebP selbst AUSGIBT (Karte
     #466): Die Funktion erzeugte Dateien, die sie nie wieder anfasste, und
     nahm damit dem Cover-Thumbnail (Karte #467) genau die Bilder weg, für die
     es gebaut wurde. Die Erkennung per Chunk deckt die ursprüngliche Sorge
     („eine Canvas-Umwandlung nimmt nur den ersten Frame") weiterhin ab, ohne
     den Standbild-Fall mitzubestrafen — ein unnötig grosses Bild ist
     ärgerlich, ein stillschweigend zerstörtes nicht wiederherstellbar. */
  if (ANIMIERBARE_FORMATE.has(file.type) || file.type === 'image/svg+xml') return file;
  if (ALS_ANIMIERT_PRUEFBAR.has(file.type) && await istAnimiert(file)) return file;
  if (typeof createImageBitmap !== 'function' || typeof document === 'undefined') return file;

  let bitmap = null;
  try {
    bitmap = await createImageBitmap(file);
    const gross = Math.max(bitmap.width, bitmap.height);
    if (!gross || gross <= maxKante) return file;

    const faktor = maxKante / gross;
    const cv = document.createElement('canvas');
    cv.width  = Math.max(1, Math.round(bitmap.width  * faktor));
    cv.height = Math.max(1, Math.round(bitmap.height * faktor));
    const ctx = cv.getContext('2d');
    if (!ctx) return file;
    ctx.drawImage(bitmap, 0, 0, cv.width, cv.height);

    /* Zielformat NACH `drawImage` bestimmen — vorher stehen die Pixel, an
       denen die Entscheidung hängt, noch gar nicht auf dem Canvas (Karte
       #466). */
    const zielTyp = ermittleZielTyp(file.type, ctx, cv);

    const blob = await new Promise(res => {
      if (typeof cv.toBlob !== 'function') { res(null); return; }
      cv.toBlob(res, zielTyp, qualitaet);
    });
    /* Nur übernehmen, wenn es wirklich kleiner wurde. Bei einem bereits stark
       komprimierten Bild kann die Umwandlung grösser ausfallen — dann wäre der
       „Fix" eine Verschlechterung. */
    if (!blob || blob.size >= file.size) return file;

    /* Was der Browser WIRKLICH geliefert hat, nicht was bestellt war: Die
       HTML-Spezifikation erlaubt ihm, ein nicht unterstütztes Zielformat
       still durch PNG zu ersetzen (ältere Safari-Versionen können WebP nicht
       kodieren). Für uns ist das unschädlich — PNG trägt Alpha ebenfalls.
       Unzulässig wäre nur, dass ein alphafähiges Ziel als JPEG zurückkommt;
       dann ist die Transparenz weg, und wir behalten lieber das Original.
       Dieses Sicherheitsnetz ist der Grund, warum die Zusage „Alpha geht nie
       verloren" nicht von der Formatunterstützung des Browsers abhängt. */
    const istTyp = blob.type || zielTyp;
    if (zielTyp !== 'image/jpeg' && !ALPHA_FAEHIGE_FORMATE.has(istTyp)) return file;

    const endung = ENDUNG_JE_TYP[istTyp] ?? '.jpg';
    const name = String(file.name || 'hintergrund').replace(/\.[^.]*$/, '') + endung;
    return new File([blob], name, { type: istTyp });
  } catch {
    return file;
  } finally {
    if (bitmap && typeof bitmap.close === 'function') { try { bitmap.close(); } catch {} }
  }
}

/* ── Cover-Thumbnail (Karte #467) ─────────────────────────────────────
 *
 * DAS PROBLEM (Beobachtung aus dem Betrieb, 2026-08-29): „Wenn eine Karte ein Bild als
 * Cover-Bild hat, dauert es ganz schön lang, bis das angezeigt wird." Es gab
 * kein Thumbnail — die Karte zeigte das Bild in voller Grösse: bis zu 1920 px
 * gespeicherte Kante auf rund 264 px Anzeigebreite (`--col-width: 280px`),
 * also gut 7× in der Breite und 50× in der Fläche. Im FSA-Volume liegt sogar
 * das unveränderte Original (ein Handyfoto sind 4000 px). Dazu kam der
 * Anzeigeweg: `.card--has-cover` steht sofort, das Bild wird asynchron
 * nachgeholt — im Server-Modus mit einem HTTP-Request pro Cover.
 *
 * DIE GRÖSSE — gemessen, nicht geschätzt (Pillow, Foto-Cover 4000×3000, auf
 * die jeweilige Kante skaliert; base64 = Rohgrösse × 4/3):
 *
 *              roh      base64    20 Cover-Karten
 *   280 px    8,0 KB   10,7 KB      0,21 MB      (JPEG q=0.70)
 *   420 px   21,4 KB   28,6 KB      0,56 MB      (JPEG q=0.70)
 *   560 px   43,6 KB   58,2 KB      1,14 MB      (JPEG q=0.70)
 *  1920 px  900,1 KB 1200,1 KB     23,44 MB      (JPEG q=0.82 — heutiger Stand)
 *
 * Die Karte schlug 560 px vor (2× Pixeldichte auf 280 px Spaltenbreite).
 * Gewählt sind **420 px**: Das ist immer noch das 1,6-fache der Anzeigebreite
 * — auf einem Bild, das per `background-size: cover` beschnitten und
 * abgedunkelt hinter Text liegt, ist der Unterschied zu 560 px nicht zu
 * sehen —, kostet aber nur die Hälfte. Gegenüber dem heutigen Zustand ist es
 * der Faktor 42.
 *
 * WARUM DAS THUMBNAIL AM ANHANG HÄNGT UND NICHT EIN ZWEITER ANHANG IST: Ein
 * zweiter Anhang stünde sichtbar in der Anhangsliste, und im Server-Modus
 * bliebe es bei einem Request pro Cover — nur mit kleinerer Nutzlast. Als
 * Feld am Anhang-Objekt liegt das Thumbnail dagegen im Board-State, den der
 * Renderer ohnehin schon hat: Das Cover erscheint dann **synchron mit der
 * Karte**, ohne Request und ohne FSA-Lesevorgang. Genau das war der
 * beobachtete Schmerz.
 *
 * Das Original wird dabei NICHT angefasst — das Thumbnail kommt zusätzlich.
 * Die naheliegende Abkürzung „einfach `BG_MAX_KANTE` senken" wäre die
 * Datenveränderung, die [[F-821]] und Karte #466 gerade zurückgedrängt haben. */

/* Zielkante und Qualität des Cover-Thumbnails (Begründung oben). */
export const THUMB_MAX_KANTE = 420;
/* Bewusst NICHT exportiert (QA-Befund Sprint 63): Es gibt keinen Abnehmer
   ausserhalb dieses Moduls, und ein Export ohne Abnehmer ist öffentliche
   Fläche, die niemand prüft. `THUMB_MAX_KANTE` ist exportiert, weil ein Test
   die Zielkante zusichert — die Qualität sichert keiner zu. */
const THUMB_QUALITAET = 0.7;

/* Erzeugt aus einem Anhang-Blob das Thumbnail-Paar für die Karte.
 *
 * Gibt `null` zurück, wenn kein Thumbnail entstehen kann oder soll — der
 * Aufrufer lässt das Feld dann weg, und der Renderer fällt auf das Original
 * zurück. Das ist der Normalfall für alles, was `verkleinereBild` bewusst
 * unangetastet lässt: Nicht-Bilder, SVG, animierte Formate (GIF/WebP/APNG —
 * ein Thumbnail daraus wäre ein Standbild) und jeden Fehlerfall. Auch ein
 * Bild, das bereits kleiner als die Zielkante ist, bekommt keins: Dann gibt
 * `verkleinereBild` dieselbe Datei zurück, und ein „Thumbnail", das eine
 * Kopie des Originals ist, kostet nur State.
 *
 * @param {Blob|File|null} blob Der Originalanhang
 * @returns {Promise<{thumb: string, thumbMime: string}|null>}
 */
export async function erzeugeThumbnail(blob) {
  if (!blob || typeof blob.type !== 'string' || !blob.type.startsWith('image/')) return null;
  try {
    const klein = await verkleinereBild(blob, {
      maxKante:  THUMB_MAX_KANTE,
      qualitaet: THUMB_QUALITAET,
    });
    /* Identität heisst: nicht verkleinert (zu klein, animiert, Fehler). Dann
       gibt es nichts zu speichern, was das Original nicht schon wäre. */
    if (!klein || klein === blob) return null;
    return { thumb: await fileToBase64(klein), thumbMime: klein.type || 'image/jpeg' };
  } catch {
    return null;
  }
}

/* Erzeugt das Thumbnail für ein frisch gesetztes Cover und schreibt es an den
 * Anhang — mit allen Abhängigkeiten als Parameter.
 *
 * WARUM MIT INJIZIERTEN ABHÄNGIGKEITEN UND NICHT DIREKT IN `main.js`: Die
 * Handler dort sind nicht exportiert und deshalb nicht aufrufbar; das Repo
 * behilft sich an solchen Stellen sonst mit „Spiegel-Tests", die eine Kopie
 * der Logik prüfen. Genau die sind nach [[F-478]] wertlos — sie bleiben grün,
 * wenn der Fix im Produktivcode verschwindet. Die eigentliche Schwierigkeit
 * liegt ohnehin hier und nicht in der Verdrahtung: Zwischen dem Lesen des
 * Originals und dem Schreiben des Ergebnisses liegt ein `await`, und der
 * Board-Zustand kann sich in dieser Zeit geändert haben.
 *
 * @param {string} cardId
 * @param {string} attachmentId
 * @param {{findeKarte: (id:string)=>object|null,
 *          leseBlob: (karte:object, att:object)=>Promise<Blob|null>,
 *          mutiere: (fn:(karte:object)=>void)=>void}} deps
 * @returns {Promise<boolean>} ob ein Thumbnail geschrieben wurde
 */
export async function reicheCoverThumbnailNach(cardId, attachmentId, deps) {
  try {
    const karte = deps.findeKarte(cardId);
    if (!karte) return false;
    const att = (karte.attachments || []).find(a => a && a.id === attachmentId);
    /* Schon vorhanden → nichts zu tun (dasselbe Cover erneut gesetzt). */
    if (!att || att.thumb) return false;

    const mini = await erzeugeThumbnail(await deps.leseBlob(karte, att));
    if (!mini) return false;

    let geschrieben = false;
    deps.mutiere(frisch => {
      /* NEU nachsehen statt die Referenz von oben zu benutzen: Während des
         Verkleinerns kann die Karte gelöscht, ein anderes Cover gesetzt oder
         der Anhang entfernt worden sein. Ein Schreibvorgang auf den alten
         Stand würde ein Thumbnail an ein Bild hängen, das gar nicht mehr das
         Cover ist — sichtbar als falsches Vorschaubild. */
      if (!frisch || frisch.coverAttachmentId !== attachmentId) return;
      const ziel = (frisch.attachments || []).find(a => a && a.id === attachmentId);
      if (!ziel || ziel.thumb) return;

      /* Grössenprüfung VOR dem Schreiben (Kritiker-Befund Sprint 63): Dieser
         Weg legt rund 29 KB base64 im Board-State ab, ohne dass der Nutzer
         eine Datei hochlädt — `checkUpload` läuft nur auf dem Upload-Pfad und
         sieht ihn nie. Ohne die Prüfung wächst ein Board allein dadurch, dass
         jemand bei vorhandenen Anhängen das Cover durchprobiert, und der
         nächste reguläre Upload wird mit „Board zu gross" abgelehnt, obwohl
         nichts hinzugefügt wurde.
         Geprüft wird nur die HARTE Grenze und nur, wenn das Profil überhaupt
         Limits kennt: Ein Thumbnail ist eine Bequemlichkeit, kein Auftrag des
         Nutzers — es darf ein Board nicht über die Grenze schieben, aber auch
         keine Warnung erzeugen, auf die niemand reagieren kann. */
      if (deps.passtNochRein && !deps.passtNochRein(mini.thumb)) return;

      ziel.thumb     = mini.thumb;
      ziel.thumbMime = mini.thumbMime;
      geschrieben = true;
    });
    return geschrieben;
  } catch {
    /* Still: Ein Board ohne Thumbnail funktioniert vollständig, nur langsamer. */
    return false;
  }
}

/* Entfernt das Thumbnail eines Anhangs, der nicht mehr Cover ist.
 *
 * Ohne diesen Schritt bleibt es für immer stehen (Kritiker-Befund Sprint 63):
 * Wer bei fünf Bildern das Cover durchprobiert und sich für keins entscheidet,
 * hinterlässt fünf verwaiste Thumbnails im Board-State — in keiner Oberfläche
 * sichtbar, aber von `computeBoardBytes` gezählt und bei jedem PUT
 * mitgeschickt. Nur `card:attachment-remove` räumte bisher auf, und das auch
 * nur, weil dort der ganze Anhang verschwindet.
 *
 * @returns {boolean} ob etwas entfernt wurde
 */
export function entferneThumbnail(karte, attachmentId) {
  if (!karte || !attachmentId) return false;
  const att = (karte.attachments || []).find(a => a && a.id === attachmentId);
  if (!att || (att.thumb === undefined && att.thumbMime === undefined)) return false;
  delete att.thumb;
  delete att.thumbMime;
  return true;
}

/* Die anzeigbare URL eines Cover-Thumbnails — synchron, ohne Request.
 *
 * Das ist der Grund für die ganze Übung: Der Renderer bekommt die URL im
 * selben Tick, in dem er die Karte baut. Gibt `null`, wenn kein Thumbnail
 * vorliegt (Bestandskarten, nicht verkleinerbare Bilder) — der Aufrufer
 * nimmt dann den bisherigen asynchronen Weg über `attachmentUrl`.
 *
 * `data:` statt `blob:` ist hier bewusst: Eine Objekt-URL müsste wieder
 * freigegeben werden, und der Renderer führt dafür eine eigene Buchhaltung
 * (`_trackRendererUrl`). Bei rund 29 KB base64 ist die data-URL unkritisch —
 * die Grenze, an der Karte #439 scheiterte, lag bei mehreren Megabyte in
 * einer CSS Custom Property. */
/* Zeichenvorrat einer base64-Nutzlast. Die Prüfung ist bewusst grob — sie
   soll nicht dekodieren, sondern nur verhindern, dass offensichtlicher Unsinn
   ungeprüft in eine CSS-Deklaration wandert (Kritiker-Befund Sprint 63). */
const BASE64_RE = /^[A-Za-z0-9+/]+={0,2}$/;

/* Formate, die als Thumbnail zulässig sind.
 *
 * SVG ist ausgeschlossen — dieselbe Regel, die seit F-128 für `att.mime` gilt.
 * Sie galt für das neue Feld zunächst NICHT: Der Renderer schliesst SVG-Cover
 * über `coverAtt.mime` aus, ein Board mit `mime: 'image/png'` und
 * `thumbMime: 'image/svg+xml'` wäre also durchgerutscht. `safeMime` prüft nur
 * die FORM eines MIME-Typs, nicht seinen Inhalt — das ist kein Ersatz für eine
 * Positivliste. Kein XSS (in CSS referenziertes SVG führt keine Skripte aus),
 * aber eine Regel, die für das eine Feld gilt und für das andere nicht, ist
 * genau die Art Asymmetrie, aus der später eine Lücke wird. */
const THUMB_ERLAUBTE_TYPEN = new Set(['image/jpeg', 'image/webp', 'image/png', 'image/avif']);

export function thumbnailUrl(att) {
  if (!att || typeof att.thumb !== 'string' || !att.thumb) return null;
  /* Nutzlast grob prüfen, bevor sie in eine CSS-`url()`-Angabe geht. Ein
     abgeschnittener oder von Hand editierter Wert ergibt sonst ein Bild, das
     nie lädt — und `.card--has-cover` setzt weisse Schrift auf hellen Grund,
     die Karte wäre unlesbar (dieselbe Falle, die der asynchrone Pfad seit der
     #450-Nacharbeit abfängt; CSS-Hintergrundbilder haben kein `onerror`). */
  if (!BASE64_RE.test(att.thumb)) return null;
  const typ = att.thumbMime || 'image/jpeg';
  if (!THUMB_ERLAUBTE_TYPEN.has(typ)) return null;
  /* Escaping HIER, nicht beim Aufrufer: Ein zweiter Konsument der Funktion
     erbt den Schutz sonst nicht. */
  return `data:${safeMime(typ)};base64,${att.thumb}`.replace(/"/g, '%22');
}

/* Eine Objekt-URL für ein Board-Attachment — IMMER `blob:`, nie `data:`.
 *
 * Der Unterschied ist nicht kosmetisch (Karte #439): `applyBoardBackground`
 * schreibt die URL in eine **CSS Custom Property**, und ein mehrere Megabyte
 * langer `url(data:…)`-Wert wird dort vom Browser stillschweigend verworfen —
 * `setProperty` meldet keinen Fehler, die Variable bleibt leer, und
 * `background-image: var(…)` fällt auf `none` zurück. Gemessen auf dem
 * produktiven Server: Klasse `board--has-bg` gesetzt, Variable `''`,
 * berechneter `::before`-Hintergrund `none`.
 *
 * Dieselbe data-URL funktioniert als DIREKTES `style.backgroundImage`
 * anstandslos — deshalb zeigte die Vorschau im Einstellungsdialog das Bild,
 * während der Hintergrund leer blieb. Wer diesen Unterschied nicht kennt,
 * sucht den Fehler beim Laden des Bildes, wo er nicht ist.
 *
 * Der Aufrufer MUSS die URL mit `URL.revokeObjectURL` freigeben.
 */
export async function boardAttachmentObjectUrl(att) {
  if (att?.data) {
    try {
      /* `base64ToBlob` statt einer zweiten Decodier-Schleife: Die gab es hier
         zuerst, und ein Kritiker hat sie zu Recht als Duplikat gemeldet — zwei
         Stellen, die dasselbe tun, müssen bei jeder künftigen Anpassung
         synchron gehalten werden. */
      return URL.createObjectURL(base64ToBlob(att.data, safeMime(att.mime)));
    } catch {
      return null;
    }
  }
  if (Profile.has('feature.fsaVolumeAttachments')) {
    const blob = await fsaGetBoardFile(att);
    return blob ? URL.createObjectURL(blob) : null;
  }
  return null;
}
export async function uploadBoardFile(file) {
  const att = { id: crypto.randomUUID(), filename: file.name || 'datei',
                mime: file.type || 'application/octet-stream', size: file.size,
                created: new Date().toISOString().slice(0, 10) };
  if (Profile.has('feature.fsaVolumeAttachments')) {
    if (!await fsaWriteBoardFile(att, file)) throw new Error('fsa-write-failed');
    return att;
  }
  att.data = await fileToBase64(file);
  return att;
}

export async function boardAttachmentUrl(att) {
  if (att.data) return `data:${safeMime(att.mime)};base64,${att.data}`;
  if (Profile.has('feature.fsaVolumeAttachments')) {
    const blob = await fsaGetBoardFile(att);
    return blob ? URL.createObjectURL(blob) : null;
  }
  return null;
}

/* F-871: liefert `true`, wenn der Anhang danach weg ist. Ohne FSA-Volume
   liegt der Anhang als base64 im Board-State — dort gibt es keine Datei, das
   Entfernen des Eintrags durch den Aufrufer IST das Löschen. */
export async function deleteBoardAttachment(att) {
  if (Profile.has('feature.fsaVolumeAttachments')) return await fsaDeleteBoardFile(att);
  return true;
}
