/* ADR-0130 §7 (Karte #609): `board.awaitingUser` je Person.

   Form: `{ <Person>: { question, since, askedBy } }` oder `null` (niemand
   wartet). Bis Sprint 77 war es EIN Eintrag `{ question, since, askedBy }` für
   das ganze Board — zwei Sitzungen überschrieben sich den Banner.

   Geschrieben wird die Abbildung NUR über die Op `board.awaiting` (setzt genau
   einen Schlüssel), nie über `board.field`: das ersetzte das ganze Feld, und
   zwei gleichzeitige Fragen verlören eine (Kritiker K-1, dieselbe LWW-Falle
   wie bei den Listenfeldern, eine Ebene höher).

   Dieses Modul ist rein (keine Uhr, kein Zufall, keine Umgebung) und liegt in
   allen Builds — Fold, Diff, Migration und Renderer nutzen dieselbe Prüfung
   statt je einer Kopie. */

export const AWAITING_PERSON_MAX = 200;
/* Session-Kritiker Sprint 78, O-7: Obergrenzen des Eintrags — sonst legte ein
   Schreiber beliebig große Einträge ab. Der Stop-Hook kürzt die Frage auf 200. */
export const AWAITING_FRAGE_MAX = 2000;
export const AWAITING_FELD_MAX = 200;

/* Prototyp-Schlüssel: als Personenname würde `map[person] = …` den Prototyp
   der Abbildung setzen statt eines Eintrags (dieselbe Klasse wie F-940). */
const PROTOTYP = new Set(['__proto__', 'constructor', 'prototype']);

/* F-1336 (Karte #645, ADR-0130 §7 Nachtrag): Einträge JE SITZUNG. Zwei Chats
   derselben Person teilten sich sonst einen Eintrag — die zweite Frage wurde
   nicht gesetzt, ein Prompt löschte die Frage der anderen Sitzung. Trägt die
   Op ein `session`, liegt der Eintrag unter `<Person> · <Sitzung>` und nennt
   `person` und `session` selbst; zugeordnet wird über `eintrag.person`, nicht
   über den Schlüssel. Ohne `session` bleibt alles wie bisher (Schlüssel =
   Person) — alte Hooks und der Browser schreiben so weiter. */
export const AWAITING_SITZUNG_TRENNER = ' · ';
/* Dasselbe Muster wie `sitzung_gueltig` in framework/hooks/_common.py: die
   Kennung kommt aus den Hook-Daten (`session_id`) bzw. `ABOARD_SESSION`. */
const SITZUNG_MUSTER = /^[A-Za-z0-9._:@-]{1,200}$/;

export function awaitingSitzungGueltig(session) {
  return typeof session === 'string' && SITZUNG_MUSTER.test(session);
}

export function awaitingSitzungsSchluessel(person, session) {
  return person + AWAITING_SITZUNG_TRENNER + session;
}

/* Sprint-81-Kritiker A-3: höchstens so viele Sitzungs-Einträge je Person.
   Ein Chat, der fragt und dann endet (oder per /clear die Kennung wechselt),
   räumt seinen Eintrag nie selbst ab; ohne Grenze wüchse die Abbildung, und
   am Server genügte ein Agent-Token mit frei gewählten Kennungen. */
export const AWAITING_SITZUNGEN_MAX = 10;

/* Kappt die Sitzungs-Einträge von `person` auf AWAITING_SITZUNGEN_MAX; es
   fallen die ältesten weg (`since`, dann Schlüssel — deterministisch, der
   Fold läuft in Browser und Server gleich). Verändert `abbildung`. */
export function kappeAwaitingSitzungen(abbildung, person, schonen = null) {
  /* Verify Sprint 81 V-5: den gerade geschriebenen Eintrag nie verwerfen —
     `since` bringt der Schreiber mit; bei abweichender Uhr wäre er sonst
     „der älteste“ und verschwände sofort, ohne je angezeigt zu werden. */
  const eigene = Object.keys(abbildung)
    .filter(k => k !== schonen && abbildung[k] && abbildung[k].session !== undefined && abbildung[k].person === person)
    .sort((a, b) => {
      const sa = abbildung[a].since, sb = abbildung[b].since;
      return sa < sb ? -1 : sa > sb ? 1 : (a < b ? -1 : a > b ? 1 : 0);
    });
  const platz = AWAITING_SITZUNGEN_MAX - (schonen !== null && abbildung[schonen] ? 1 : 0);
  for (let i = 0; i < eigene.length - platz; i++) delete abbildung[eigene[i]];
}

export function awaitingPersonGueltig(person) {
  return typeof person === 'string'
    && person.trim() !== ''
    && [...person].length <= AWAITING_PERSON_MAX
    && !PROTOTYP.has(person)
    /* Sprint-81-Kritiker A-4: ein Name mit dem Sitzungs-Trenner fiele auf den
       Schlüssel eines Sitzungs-Eintrags (`Nutzer · s1` ohne Sitzung = `Nutzer` mit
       Sitzung `s1`) und überschriebe oder löschte ihn. */
    && !person.includes(AWAITING_SITZUNG_TRENNER);
}

/* Ein Eintrag: question und since nicht leer, askedBy eine Zeichenkette —
   dieselbe Prüfung wie die alte Einzelform (Karte #115). */
export function istAwaitingEintrag(e) {
  return !!e && typeof e === 'object' && !Array.isArray(e)
    && typeof e.question === 'string' && e.question.length > 0 && [...e.question].length <= AWAITING_FRAGE_MAX
    && typeof e.since === 'string' && e.since.length > 0 && [...e.since].length <= AWAITING_FELD_MAX
    && typeof e.askedBy === 'string' && [...e.askedBy].length <= AWAITING_FELD_MAX
    /* F-1336: `person`/`session` stempelt der Fold bei Sitzungs-Einträgen —
       optional, aber wenn da, dann gültig und nur gemeinsam. */
    && (e.session === undefined
      ? e.person === undefined
      : awaitingSitzungGueltig(e.session) && awaitingPersonGueltig(e.person));
}

/* Schlüssel und Eintrag passen zusammen: ein Sitzungs-Eintrag liegt genau
   unter `<Person> · <Sitzung>`, alles andere unter einem gültigen Personennamen.
   So kann niemand einen Eintrag unter fremdem Schlüssel ablegen, der beim
   Zuordnen über `eintrag.person` als jemand anderes zählte. */
function schluesselPasst(k, e) {
  if (e.session !== undefined) return k === awaitingSitzungsSchluessel(e.person, e.session);
  return awaitingPersonGueltig(k);
}

/* Die Person eines Eintrags (F-1336): bei Sitzungs-Einträgen das Feld, sonst
   der Schlüssel. */
export function awaitingPersonVon(k, e) {
  return e && typeof e === 'object' && typeof e.person === 'string' && e.session !== undefined ? e.person : k;
}

/* Die alte Einzelform hat `question` als Zeichenkette auf oberster Ebene; in
   der Abbildung wäre `question` ein Personenname und sein Wert ein Objekt. */
export function istAlteAwaitingForm(v) {
  return !!v && typeof v === 'object' && !Array.isArray(v) && typeof v.question === 'string';
}

/* Die Person, der ein Bestand ohne Namen zugeschrieben wird: die erste
   Nutzer-Identität des Boards, sonst „User" (der Vorgabename des Browsers,
   `defaultUserIdentity`). Deterministisch und ohne Config — der headless
   Projektor kennt keine Browser-Einstellung, und ein anderes Ergebnis dort als
   im Browser wäre ein Dauer-Fehlalarm des Anomalie-Wächters (F-812). */
export function standardPerson(board) {
  const ids = Array.isArray(board?.identities) ? board.identities : [];
  for (const i of ids) {
    if (i && i.kind === 'user' && typeof i.label === 'string' && awaitingPersonGueltig(i.label.trim())) {
      return i.label.trim();
    }
  }
  return 'User';
}

/* Der Wert als Abbildung Person → Eintrag. Liest die alte Einzelform als
   Eintrag der Standardperson (ADR-0130 §7: „der bisherige Board-Owner"); was
   keine gültige Form hat, fällt weg. Liefert immer ein neues Objekt. */
export function awaitingAlsAbbildung(v, board) {
  const out = {};
  if (!v || typeof v !== 'object' || Array.isArray(v)) return out;
  if (istAlteAwaitingForm(v)) {
    if (istAwaitingEintrag(v)) out[standardPerson(board)] = v;
    return out;
  }
  for (const k of Object.keys(v)) {
    if (!PROTOTYP.has(k) && istAwaitingEintrag(v[k]) && schluesselPasst(k, v[k])) out[k] = v[k];
  }
  return out;
}

/* Leere Abbildung heisst `null` — die Form, die das Board-Skelett und der
   Server-Seed tragen. */
export function awaitingOderNull(abbildung) {
  return Object.keys(abbildung).length ? abbildung : null;
}

/* Der Schlüssel, der zu `person` gehört — ohne Rücksicht auf Gross-/
   Kleinschreibung und Randleerraum (wie `emailsForLabels`: eine verpasste
   Zuordnung ist hier harmloser als eine falsche). `null`, wenn keiner passt. */
export function awaitingSchluesselFuer(v, person) {
  const ziel = String(person ?? '').trim().toLowerCase();
  if (!ziel || !v || typeof v !== 'object' || Array.isArray(v) || istAlteAwaitingForm(v)) return null;
  for (const k of Object.keys(v)) {
    if (String(awaitingPersonVon(k, v[k])).trim().toLowerCase() === ziel) return k;
  }
  return null;
}

/* Die Schlüssel der EIGENEN Einträge (Browser-Banner, „Ich habe geantwortet“).

   - Alle Schlüssel, die zu `person` passen (ohne Rücksicht auf Gross-/
     Kleinschreibung und Randleerraum) — zwei Sitzungen, die gleichzeitig
     „Nutzer“ und „nutzer“ schrieben, erzeugen zwei Schlüssel; beide gehören
     derselben Person, und „Ich habe geantwortet“ muss beide löschen
     (Session-Kritiker Sprint 78, O-3).
   - Auf einem LOKALEN Board (kein Server-Modus) mit höchstens EINER
     Nutzer-Identität zusätzlich JEDER Eintrag: Dort gibt es niemand anderen.
     Am Server gilt das nie — Identitäten pflegt man dort von Hand, ein Board
     mit vielen Mitgliedern trägt oft nur die eine Standard-Identität „User“. Die Hooks schreiben ohne
     ABOARD_PERSON unter der Standardperson (oft „User“), der Browser heisst
     „Nutzer“; und nach einer Umbenennung der Identität bliebe ein Eintrag unter
     dem alten Namen sonst für niemanden „eigen“ (O-4).
   - Eine leere `person` (Browser ohne `userName`) passt zu nichts — auf einem
     Board mit mehreren Personen zeigt er dann keinen Banner, statt die Frage
     der ersten Identität als eigene zu nehmen und beim Klick zu löschen (O-2).

   Liefert die Schlüssel in der Reihenfolge der Abbildung; leer = keiner. */
export function eigeneAwaitingSchluessel(board, person, { server = false } = {}) {
  const v = board?.awaitingUser;
  if (!v || typeof v !== 'object' || Array.isArray(v) || istAlteAwaitingForm(v)) return [];
  /* `person` darf eine Liste sein (Server: Einstellung, Anmelde-Name, E-Mail). */
  const ziele = (Array.isArray(person) ? person : [person])
    .map(x => String(x ?? '').trim().toLowerCase()).filter(Boolean);
  const nutzer = (Array.isArray(board?.identities) ? board.identities : []).filter(i => i && i.kind === 'user');
  const alle = Object.keys(v).filter(k => istAwaitingEintrag(v[k]));
  if (!server && nutzer.length <= 1) return alle;
  /* F-1336: zugeordnet über die Person des Eintrags — die Sitzungs-Einträge
     derselben Person gehören ihr alle. */
  return alle.filter(k => ziele.includes(String(awaitingPersonVon(k, v[k])).trim().toLowerCase()));
}
