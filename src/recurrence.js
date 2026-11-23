/* Wiederholungs-Termine (ADR-0041, F-169).
   Eigenes Modul seit Sprint 69: main.js exportiert nichts (Import würde die
   EventBus-Registrierungen auslösen), die Tests prüften deshalb eine
   „wortgleich gehaltene“ Kopie — und nichts hielt sie wortgleich. Jetzt
   importieren main.js und die Tests dieselbe Funktion.

   F-1103 (Sprint 69, Nacharbeit): `spawnRecurringCards` zog aus demselben
   Grund nach — main.js importiert sie jetzt von hier, statt eine zweite,
   „wortgleich gehaltene“ Kopie in den Tests zu pflegen. */
import { Model } from './model.js';

/**
 * Berechnet das nächste Spawn-Datum ausgehend von `lastSpawned` + Intervall.
 * Gibt einen ISO-8601-String (YYYY-MM-DD) zurück.
 *
 * Monatsende-Edge-Case: 31. Januar + 1 Monat → wird auf letzten gültigen Tag
 * des Zielmonats geklemmt (z. B. 28./29. Februar).
 *
 * F-169 (Karte #525): optionaler dritter Parameter `day` — Semantik von der
 * UI übernommen (`src/modal.js` `#modal-recurrence-day`, Bereich 0-6 bei
 * `weekly`, 1-31 bei `monthly`):
 *   - `weekly` + `day` (Wochentag, JS-Konvention `Date#getUTCDay()`:
 *     0 = Sonntag … 6 = Samstag): nächster Termin ist der nächste `day`,
 *     der STRIKT NACH `lastSpawned` liegt — fällt `lastSpawned` selbst auf
 *     diesen Wochentag, sind das +7 Tage (kein Sofort-Spawn am selben Tag).
 *   - `monthly` + `day` (Tag im Monat, 1-31): dieser Tag im Monat, der auf
 *     den Monat von `lastSpawned` folgt. Hat der Zielmonat weniger Tage
 *     (z. B. `day=31` im Februar), wird auf den Monatsletzten geklemmt —
 *     dieselbe Klemm-Logik wie im `day`-losen Zweig unten, nur mit dem
 *     Ziel-`day` statt dem ORIGINAL-Tag als Ausgangswert.
 *   - Ohne `day` (oder `day == null`): unverändert +7 Tage / +1 Monat wie
 *     bisher — bestehendes Verhalten für Templates ohne Tagesfeld bleibt
 *     identisch.
 * Kein Nachholen übersprungener Termine: diese Funktion berechnet immer nur
 * GENAU den nächsten Termin ab `lastSpawned`, nie eine Liste überfälliger
 * Termine — das Aufrufer-seitige Verhalten „höchstens ein Spawn pro Prüfung"
 * (spawnRecurringCards() unten) bleibt dadurch unverändert erhalten.
 */
export function nextSpawnDate(lastSpawned, interval, day) {
  const d = new Date(lastSpawned + 'T12:00:00Z');
  const hasDay = day != null && !isNaN(day);
  if (interval === 'daily') {
    d.setUTCDate(d.getUTCDate() + 1);
  } else if (interval === 'weekly') {
    if (hasDay) {
      const current = d.getUTCDay();
      let diffTage = (day - current + 7) % 7;
      if (diffTage === 0) diffTage = 7; /* strikt NACH lastSpawned, kein Tag-0-Treffer */
      d.setUTCDate(d.getUTCDate() + diffTage);
    } else {
      d.setUTCDate(d.getUTCDate() + 7);
    }
  } else if (interval === 'monthly') {
    if (hasDay) {
      /* Erst auf den 1. des Zielmonats springen (vermeidet Overflow-
         Artefakte von setUTCMonth() bei einem hohen Ausgangstag), dann die
         tatsächliche Monatslänge ermitteln und `day` daran klemmen. */
      d.setUTCDate(1);
      d.setUTCMonth(d.getUTCMonth() + 1);
      const letzterTagZielmonat = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0)).getUTCDate();
      d.setUTCDate(Math.min(day, letzterTagZielmonat));
    } else {
      const origDay = d.getUTCDate();
      d.setUTCMonth(d.getUTCMonth() + 1);
      /* Edge-Case: wenn Monat überlaufen hat (z. B. 31. Jan → März), auf letzten
         Tag des Zielmonats klemmen */
      if (d.getUTCDate() !== origDay) {
        d.setUTCDate(0); /* 0 = letzter Tag des vorherigen Monats */
      }
    }
  }
  return d.toISOString().slice(0, 10);
}

/**
 * Erzeugt beim Board-Load Spawn-Instanzen für fällige Template-Karten.
 * Deterministisch über lastSpawned-Anchor — idempotent bei mehrfachem Aufruf am selben Tag.
 *
 * Mutiert board direkt (analog zu anderen migrate*-Funktionen).
 * Gibt board zurück.
 */
export function spawnRecurringCards(board, today) {
  if (!board?.sprints) return board;

  /* col-ready ermitteln — erste Spalte mit dieser ID oder Fallback auf col-backlog */
  const hasColReady = board.columnDefs?.some(c => c.id === 'col-ready');
  const spawnColId  = hasColReady ? 'col-ready' : 'col-backlog';

  /* Wir iterieren über einen Snapshot der Sprints (kein direktes push während Iteration) */
  const sprintsSnapshot = [...board.sprints];

  for (const sprint of sprintsSnapshot) {
    /* Backlog-Sprint erhält keine Spawn-Instanzen (col-ready existiert dort nicht sinnvoll) */
    if (sprint.id === '__backlog__') continue;

    const cardsSnapshot = [...sprint.cards];
    for (const card of cardsSnapshot) {
      /* ADR-0036: Sprint-Meta-Karten niemals als Recurrence-Templates behandeln */
      if (card.kind === 'sprint-meta') continue;
      if (!card.recurrence) continue;

      const { interval, lastSpawned, day } = card.recurrence;
      if (!interval || !lastSpawned) continue;

      /* ADR-0041: Kein Spawn, wenn das Template in `col-erledigt` liegt (der
         Nutzer hat die Vorlage abgeschaltet). Bewusst an der id festgemacht,
         nicht am Titel: So hat es Sprint 61 entschieden (F-830), und eine
         Titel-Erkennung stoppte Vorlagen in fremden Spalten, die zufällig
         „Fertig“ oder „Done“ heissen. Sprint 69 hatte das kurz auf die
         Titel-Semantik umgestellt (F-1103) und nach zwei Prüfläufen wieder
         zurückgenommen (F-1117). */
      if (card.columnId === 'col-erledigt') continue;

      /* F-169: `day` fliesst nur bei weekly/monthly in die Berechnung ein —
         nextSpawnDate() ignoriert ihn bei daily ohnehin, wird hier aber nicht
         extra gefiltert, um die Zuständigkeit nicht doppelt zu pflegen. */
      const spawnOn = nextSpawnDate(lastSpawned, interval, day);
      if (spawnOn > today) continue; /* noch nicht fällig */

      /* Spawn-Instanz anlegen */
      const displayId = Model.assignDisplayId(board);
      const instance  = Model.createCard(card.title, spawnColId, displayId);
      instance.description  = card.description  || '';
      instance.labels       = Array.isArray(card.labels) ? [...card.labels] : [];
      instance.owner        = card.owner        || '';
      instance.complexity   = card.complexity   ?? '';
      instance.effort       = card.effort       ?? null;
      instance.dueDate      = spawnOn;            /* Fälligkeitsdatum = Spawn-Datum */
      /* ADR-0138 §1: die Uhrzeit der Vorlage bleibt, nur das Datum ist neu.
         startTime nicht — die Instanz hat kein startDate, die Uhrzeit hinge
         an keinem Tag und fiele beim nächsten Laden ohnehin weg. */
      instance.dueTime      = card.dueTime      ?? null;
      instance.recurrence   = null;               /* Instanzen sind keine Templates */
      /* ADR-0140 §10 (Karte #778): Eine vertrauliche Vorlage erzeugt
         vertrauliche Instanzen — sonst ginge derselbe Titel über jede
         Instanz als Schatten hinaus. Nur wenn die Vorlage den Schlüssel
         trägt; nie als Standardwert (Feld-Maßstab, Klasse 161). */
      if (Object.prototype.hasOwnProperty.call(card, 'confidential')) instance.confidential = card.confidential;

      sprint.cards.push(instance);

      /* lastSpawned auf heute aktualisieren — verhindert doppelten Spawn */
      card.recurrence = { ...card.recurrence, lastSpawned: today };
    }
  }

  return board;
}
