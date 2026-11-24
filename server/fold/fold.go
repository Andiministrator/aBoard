// Paket fold führt den Op-Fold aus src/ops.js im Go-Server aus (ADR-0129 §4,
// Variante B; Karte #551, aus dem Spike #546 übernommen).
//
// Es gibt damit EINE Fold-Implementierung für Browser, Projektor und Server —
// die Drift zwischen ihnen kann nicht entstehen. src/ops.js wird dafür mit
// `bun run build:ops-bundle` zu ops.bundle.js gebündelt (goja kann kein ESM),
// hier per go:embed eingebettet und beim ersten Gebrauch EINMAL kompiliert.
//
// Datenaustausch Go <-> goja ausschliesslich als JSON-Text: Go reicht den
// Board-Blob und den Ops-Batch als Text hinein, JS parst, faltet und
// serialisiert. Kein goja-Objekt verlässt die Runtime.
//
// Nebenläufigkeit: Eine goja.Runtime darf nie von zwei Goroutinen zugleich
// benutzt werden. Der Motor hält deshalb einen begrenzten Pool (gepufferter
// Kanal); jede Runtime gehört zu jedem Zeitpunkt genau einem Aufruf. Die
// Spike-Gegenprobe mit einer geteilten Runtime lieferte 373 Race-Meldungen
// und falsche Ergebnisse.
package fold

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

//go:embed ops.bundle.js
var buendelQuelle string

// Die Brücke ist der einzige Einstieg von Go aus.
//
// Weiche „ohne Klon“ (ADR-0129 §4 Nachtrag, Punkt 2): fold() klont den
// Snapshot, damit der Aufrufer ihn unverändert behält. Am Server ist der
// Snapshot frisch geparst und wird danach verworfen — der Klon ist dort
// reine Kosten und Sperrzeit unter BEGIN IMMEDIATE (auf amd64 rund die
// Hälfte der Faltzeit am echten Board). clone() in ops.js prüft
// `typeof structuredClone` ZUR LAUFZEIT (goja kennt es nicht und nimmt sonst
// den JSON-Fallback). Eine Identitätsfunktion für die Dauer EINES Aufrufs
// schaltet den Klon ab, ohne ops.js zu ändern; danach wird der vorige
// Zustand wiederhergestellt. Abgesichert über die Fixtures: Ergebnis und
// Protokoll sind in beiden Modi byte-identisch (TestKonformitaetOhneKlon).
//
// Rückgaben werden mit NUL getrennt statt in ein JSON-Objekt verpackt, damit
// Go die (mehrere MB grosse) Projektion nicht ein zweites Mal parsen muss.
// JSON.stringify maskiert NUL in Zeichenketten als \u0000 — ein rohes NUL
// kommt in seiner Ausgabe nie vor.
//
// ⚠️ Die Quelle ist ein Go-Raw-String: Ein Backtick im JS (auch in einem
// Kommentar) beendet ihn und bricht den Build. Nur '…' oder "…" verwenden
// (Karte #748, einmal passiert).
const brueckeQuelle = `(function () {
  "use strict";
  function mitKlonModus(ohneKlon, fn) {
    if (!ohneKlon) return fn();
    var hatte = Object.prototype.hasOwnProperty.call(globalThis, "structuredClone");
    var alt = globalThis.structuredClone;
    globalThis.structuredClone = function (o) { return o; };
    try { return fn(); }
    finally { if (hatte) globalThis.structuredClone = alt; else delete globalThis.structuredClone; }
  }
  /* Beliebiger Export mit JSON-Argumenten — nur für die Konformitätstests.
     Liefert Rückgabe und die Argumente NACH dem Aufruf (fängt das befüllte
     Protokoll von fold und belegt die Reinheit des Snapshots). */
  globalThis.__rufe = function (name, argsJson, ohneKlon) {
    var fn = globalThis.AboardOps[name];
    if (typeof fn !== "function") throw new Error("unbekannte Funktion: " + name);
    var args = JSON.parse(argsJson);
    var r = mitKlonModus(ohneKlon, function () { return fn.apply(null, args); });
    return JSON.stringify(r === undefined ? null : r) + "\u0000" + JSON.stringify(args);
  };
  /* Vorprüfung eines Batches (Karte #552) — ohne Board, also VOR der
     Schreibsperre: Hülle (validateOp) und Payload-Vertrag (inspectOpPayload). */
  globalThis.__pruefe = function (opsJson) {
    var ops = JSON.parse(opsJson), O = globalThis.AboardOps;
    if (!Array.isArray(ops)) throw new Error("Ops sind kein Array");
    return JSON.stringify(ops.map(function (op) {
      var i = O.inspectOpPayload(op);
      return { gueltig: O.validateOp(op), fehlend: i.missing, unbekannt: i.unknown };
    }));
  };
  globalThis.__schemaVersion = function () { return globalThis.AboardOps.OP_SCHEMA_VERSION; };
  /* Server-Form: Snapshot + Ops-Batch (+ Optionen) rein, Projektion + Protokoll raus. */
  globalThis.__falte = function (snapJson, opsJson, optJson, ohneKlon) {
    var protokoll = [];
    var snap = JSON.parse(snapJson), ops = JSON.parse(opsJson);
    var opt = optJson ? JSON.parse(optJson) : undefined;
    var erg = mitKlonModus(ohneKlon, function () {
      return globalThis.AboardOps.fold(snap, ops, protokoll, opt);
    });
    return JSON.stringify(erg) + "\u0000" + JSON.stringify(protokoll);
  };
  /* Ziel-Prüfung für den Server (F-1146, Karte #558). fold() überspringt eine
     Op auf eine Karte oder Swimlane, die es nicht gibt, still — für den
     lokalen Projektor richtig (in der Inbox-Historie sind Ops auf später
     gelöschte Karten normal), für einen Agenten am Server aber der
     wahrscheinlichste Fehler: eine veraltete oder geratene ID, quittiert mit
     200. Deshalb hier, NICHT in ops.js (Maintainer-Entscheid 2026-09-28).

     Geprüft wird je Op gegen den Stand VOR ihr, also nach allen früheren Ops
     des Batches: Eine Karte, die eine frühere Op anlegt, zählt als vorhanden,
     eine, die eine frühere Op löscht, als fehlend. Dafür wird Op für Op
     gefaltet. Das ist nur dann dasselbe wie EIN fold() über den Batch, wenn
     die Array-Reihenfolge die Faltordnung ist; der Server stempelt dafür
     einen gemeinsamen ts und seq = Index. Anderes wird abgewiesen, statt
     still anders zu falten.

     Löschungen (card.delete, comment.delete, swimlane.delete) prüfen ihr
     Ziel bewusst NICHT: Eine Wiederholung nach einem Verbindungsabbruch
     bekäme sonst 422, obwohl der erste Versuch gewirkt hat. */
  function hatKarte(board, id) {
    var sp = board.sprints || [];
    for (var i = 0; i < sp.length; i++) {
      var cs = (sp[i] && sp[i].cards) || [];
      for (var j = 0; j < cs.length; j++) if (cs[j] && cs[j].id === id) return true;
    }
    return false;
  }
  /* Liefert die Karte oder null. */
  function karteVon(board, id) {
    var sp = board.sprints || [];
    for (var i = 0; i < sp.length; i++) {
      var cs = (sp[i] && sp[i].cards) || [];
      for (var j = 0; j < cs.length; j++) if (cs[j] && cs[j].id === id) return cs[j];
    }
    return null;
  }
  /* Liefert den Kommentar oder null. */
  function kommentarVon(board, cardId, commentId) {
    var sp = board.sprints || [];
    for (var i = 0; i < sp.length; i++) {
      var cs = (sp[i] && sp[i].cards) || [];
      for (var j = 0; j < cs.length; j++) {
        if (!cs[j] || cs[j].id !== cardId) continue;
        var ks = Array.isArray(cs[j].comments) ? cs[j].comments : [];
        for (var n = 0; n < ks.length; n++) if (ks[n] && ks[n].id === commentId) return ks[n];
        return null;
      }
    }
    return null;
  }
  function hatTermin(board, id) {
    var liste = Array.isArray(board.termine) ? board.termine : [];
    for (var i = 0; i < liste.length; i++) if (liste[i] && liste[i].id === id) return true;
    return false;
  }
  function hatSwimlane(board, id) {
    var sp = board.sprints || [];
    for (var i = 0; i < sp.length; i++) if (sp[i] && sp[i].id === id) return true;
    return false;
  }
  function fehlendeZiele(board, op) {
    var p = op.payload || {}, f = [];
    switch (op.type) {
      case "card.field":
      case "card.fieldIf":
      case "card.listAdd":
      case "card.listRemove":
      case "comment.add":
      case "card.claim":
      case "card.release":
        if (!hatKarte(board, p.cardId)) f.push({ cardId: p.cardId });
        break;
      case "comment.edit":
        /* ADR-0130 §4 (Karte #601): Bearbeiten ist keine Löschung — fehlt der
           Kommentar, hat ihn jemand inzwischen entfernt, und das soll der
           Schreiber erfahren, statt dass fold() es still überspringt. Eine
           Wiederholung nach Verbindungsabbruch trifft den Kommentar weiter
           an (idempotent: gleicher Text). */
        if (!hatKarte(board, p.cardId)) f.push({ cardId: p.cardId });
        else if (!kommentarVon(board, p.cardId, p.commentId)) f.push({ cardId: p.cardId, commentId: p.commentId });
        break;
      case "card.move":
      case "card.moveIf":
        if (!hatKarte(board, p.cardId)) f.push({ cardId: p.cardId });
        if (!hatSwimlane(board, p.toSprintId)) f.push({ sprintId: p.toSprintId });
        break;
      /* ADR-0141 §5.3 (Karte #827): Die bedingten Rückweg-Ops sind in ops.js
         auf ein fehlendes Ziel ein stilles No-op. Der Rückweg muss das als
         verworfen/karte-weg melden können, nicht als „angewendet“. */
      case "card.checkItemIf":
        if (!hatKarte(board, p.cardId)) f.push({ cardId: p.cardId });
        break;
      case "comment.editIf":
        if (!hatKarte(board, p.cardId)) f.push({ cardId: p.cardId });
        else if (!kommentarVon(board, p.cardId, p.commentId)) f.push({ cardId: p.cardId, commentId: p.commentId });
        break;
      case "termin.fieldIf":
        if (!hatTermin(board, p.terminId)) f.push({ terminId: p.terminId });
        break;
      case "card.upsert":
        /* Nur bei Neuanlage: fold() legt die Karte sonst still im Backlog ab
           statt in der genannten Swimlane. Auf eine bestehende Karte wirkt
           sprintId nicht (Position ändert nur card.move).
           Ohne sprintId ist das Ziel __backlog__. Fehlt auch das (Service-Board,
           seed_service.go), verwirft fold() die Karte und verbraucht dabei eine
           displayId (F-1152, Karte #561). */
        if (p.card && !hatKarte(board, p.card.id)) {
          if (p.sprintId != null && !hatSwimlane(board, p.sprintId) && hatSwimlane(board, "__backlog__")) {
            f.push({ cardId: p.card.id, sprintId: p.sprintId });
          } else if ((p.sprintId == null || !hatSwimlane(board, p.sprintId)) && !hatSwimlane(board, "__backlog__")) {
            f.push({ cardId: p.card.id, sprintId: p.sprintId != null ? p.sprintId : "__backlog__" });
          }
        }
        break;
      case "swimlane.move":
        if (!hatSwimlane(board, p.sprintId)) f.push({ sprintId: p.sprintId });
        break;
    }
    return f;
  }
  /* Agenten-Marke (ADR-0138 §6 „Umsetzung“, Karte #748, Zusage ans Partnerprojekt):
     Hat ein Agent die Frist einer Karte (Datum oder Uhrzeit) bzw. einen
     Termin ZULETZT geändert, trägt der Eintrag die Marke; ändert ein Mensch,
     fällt sie weg. Verwaltet allein hier, beim Falten am Server — was ein
     Schreiber in der Nutzlast mitschickt (jede Schreibweise), zählt nicht.
     Nicht in ops.js: Browser und lokaler Projektor kennen die Akteur-Art
     nicht, und die Marke gilt nur für den Feed „für außen“. */
  var KARTEN_MARKE = "fristVonAgent", TERMIN_MARKE = "vonAgent";
  /* Karte #778 (ADR-0140 §2): zweite Karten-Marke für den Titel — der
     Schatten-Ausgang kennzeichnet Titel, die zuletzt ein Agent geändert hat
     (Vertrag Push-Skript, Ziffer 9.2). Dieselbe Mechanik wie fristVonAgent, nur am Titel. */
  var TITEL_MARKE = "titelVonAgent";
  /* Karte #798 (ADR-0141 Nachtrag „#798 nach 2c“): Herkunftsmarke — die
     Karte hat ein Agent auf der Seite innen NEU angelegt. Anders als die
     beiden Marken oben fällt sie nie weg (Herkunft, nicht „zuletzt
     geändert“): Bestand bleibt, setzen kann sie nur die Neuanlage durch
     einen Agenten bei gesetzter Option anlageHerkunft. Was ein Schreiber
     mitschickt, zählt nicht. Grundlage für „Nach innen zurückholen“ und die
     Übersicht „von Agenten nach außen“. */
  var HERKUNFT_MARKE = "angelegtVonAgent";
  function titelForm(karte) {
    var v = (karte || {}).title;
    return v === undefined || v === null ? "" : JSON.stringify(v);
  }
  var FRIST_FELDER = ["startDate", "startTime", "dueDate", "dueTime"];
  function fristForm(karte) {
    karte = karte || {};
    var teile = [];
    for (var i = 0; i < FRIST_FELDER.length; i++) {
      var v = karte[FRIST_FELDER[i]];
      teile.push(v === undefined || v === null || v === "" ? "" : JSON.stringify(v));
    }
    return teile.join("\u0001");
  }
  /* Entfernt die Marke in jeder Schreibweise. */
  function ohneMarke(obj, name) {
    var klein = name.toLowerCase();
    for (var k in obj) {
      if (Object.prototype.hasOwnProperty.call(obj, k) && k.toLowerCase() === klein) delete obj[k];
    }
  }
  function markenZielKarte(op) {
    var p = op.payload || {};
    if (op.type === "card.upsert") return p.card && typeof p.card.id === "string" ? p.card.id : null;
    if (op.type === "card.delete") return null;
    return typeof p.cardId === "string" ? p.cardId : null;
  }
  /* Vor der Op: Frist und Marke der Zielkarte merken. Bestand und
     Nutzlast eines card.upsert werden auf dieselbe Form gebracht — Marke nur
     als true in genau dieser Schreibweise, sonst gar nicht. Sonst meldete
     der Fold eine abweichende Bestandsmarke (aus einem PUT, Import oder
     Restore) als entkernt (F-926), und JEDER Upsert der Karte fiele mit 422,
     ohne Rückweg (Kritiker #748, K-2). */
  function markeVorher(board, op) {
    var id = markenZielKarte(op);
    if (id === null) return null;
    var karte = karteVon(board, id);
    var vor = { cardId: id, frist: fristForm(karte), marke: !!(karte && karte[KARTEN_MARKE] === true),
      titel: titelForm(karte), titelMarke: !!(karte && karte[TITEL_MARKE] === true),
      herkunft: !!(karte && karte[HERKUNFT_MARKE] === true) };
    if (karte) {
      ohneMarke(karte, KARTEN_MARKE);
      if (vor.marke) karte[KARTEN_MARKE] = true;
      ohneMarke(karte, TITEL_MARKE);
      if (vor.titelMarke) karte[TITEL_MARKE] = true;
      ohneMarke(karte, HERKUNFT_MARKE);
      if (vor.herkunft) karte[HERKUNFT_MARKE] = true;
    }
    var pc = op.type === "card.upsert" ? op.payload.card : null;
    if (pc && typeof pc === "object") {
      ohneMarke(pc, KARTEN_MARKE);
      if (karte && vor.marke) pc[KARTEN_MARKE] = true;
      ohneMarke(pc, TITEL_MARKE);
      if (karte && vor.titelMarke) pc[TITEL_MARKE] = true;
      ohneMarke(pc, HERKUNFT_MARKE);
      if (karte && vor.herkunft) pc[HERKUNFT_MARKE] = true;
    }
    return vor;
  }
  /* herkunftNeu: Ergebnis von neuAnlage für diese Op, nur bei gesetzter
     Option anlageHerkunft (Agent, Seite innen) — sonst null (#798). */
  function markeNachher(board, op, vor, istAgent, herkunftNeu) {
    if (vor) {
      var karte = karteVon(board, vor.cardId);
      if (karte) {
        var marke = fristForm(karte) !== vor.frist ? istAgent : vor.marke;
        ohneMarke(karte, KARTEN_MARKE);
        if (marke) karte[KARTEN_MARKE] = true;
        var titelMarke = titelForm(karte) !== vor.titel ? istAgent : vor.titelMarke;
        ohneMarke(karte, TITEL_MARKE);
        if (titelMarke) karte[TITEL_MARKE] = true;
        var herkunft = vor.herkunft || !!(herkunftNeu && herkunftNeu.art === "karte" && herkunftNeu.id === vor.cardId);
        ohneMarke(karte, HERKUNFT_MARKE);
        if (herkunft) karte[HERKUNFT_MARKE] = true;
      }
    }
    if (op.type === "termin.upsert") {
      var t = (op.payload || {}).termin;
      var liste = Array.isArray(board.termine) ? board.termine : [];
      for (var i = 0; t && i < liste.length; i++) {
        if (liste[i] && liste[i].id === t.id) {
          ohneMarke(liste[i], TERMIN_MARKE);
          if (istAgent) liste[i][TERMIN_MARKE] = true;
        }
      }
    }
  }
  /* Karte #789 (ADR-0140 §10, Maintainer-Entscheid 10.10.2026): Karten und
     Termine, die ein AGENT auf der Seite innen NEU anlegt, sind vertraulich;
     freigeben (abschalten) darf nur ein Mensch — das deckt die Sperre aus
     #778 (vertraulich_sperre.go). Merkmal ist der Akteur, den diese Brücke
     sicher kennt, nicht die Nutzlast: Ein mitgeschicktes false, "" oder null
     ändert nichts. Nur bei Neuanlage — ein Upsert auf eine bestehende Karte
     bleibt unberührt (sonst schaltete jede Agenten-Bearbeitung ein). Die
     Karten-Fabrik setzt das Feld nie (Klasse 161); gesetzt wird es hier, am
     Ergebnis der Op. */
  function neuAnlage(board, op) {
    var p = op.payload || {};
    if (op.type === "card.upsert" && p.card && typeof p.card.id === "string") {
      return karteVon(board, p.card.id) ? null : { art: "karte", id: p.card.id };
    }
    if (op.type === "termin.upsert" && p.termin && typeof p.termin.id === "string") {
      var liste = Array.isArray(board.termine) ? board.termine : [];
      for (var i = 0; i < liste.length; i++) if (liste[i] && liste[i].id === p.termin.id) return null;
      return { art: "termin", id: p.termin.id };
    }
    return null;
  }
  /* Verify #789, V-1: Die im Batch neu angelegten Kennungen als Menge
     (je Art ein Objekt ohne Prototyp), und GENAU EIN Durchlauf über alle
     Karten und Termine des Boards, der für jede Kennung der Menge
     confidential = true setzt — O(Karten) je Op wie der Fold selbst. Vorher
     suchte karteVon je Neuanlage einzeln (Ops × Neuanlagen × Karten): 200
     Neuanlagen auf 3000 Karten rissen die Fold-Zeitgrenze. Bewusst KEINE
     Liste „welcher Op-Typ betrifft welche id“ (Klasse 186: ein vergessener
     Op-Typ wäre eine W-4-Lücke) — gesetzt wird am Ergebnis, für alle. */
  function neueMenge() {
    return { n: 0, karte: Object.create(null), termin: Object.create(null) };
  }
  function mengeAufnehmen(menge, ziel) {
    var m = ziel.art === "karte" ? menge.karte : menge.termin;
    if (m[ziel.id] !== true) {
      m[ziel.id] = true;
      menge.n++;
    }
  }
  function setzeVertraulichMenge(board, menge) {
    if (menge.n === 0) return;
    var sp = Array.isArray(board.sprints) ? board.sprints : [];
    for (var i = 0; i < sp.length; i++) {
      var cs = (sp[i] && Array.isArray(sp[i].cards)) ? sp[i].cards : [];
      for (var j = 0; j < cs.length; j++) {
        if (cs[j] && typeof cs[j].id === "string" && menge.karte[cs[j].id] === true) cs[j].confidential = true;
      }
    }
    var liste = Array.isArray(board.termine) ? board.termine : [];
    for (var t = 0; t < liste.length; t++) {
      if (liste[t] && typeof liste[t].id === "string" && menge.termin[liste[t].id] === true) liste[t].confidential = true;
    }
  }
  /* Karte #827: boardpaarUrsprung aus der Nutzlast an die gerade neu
     angelegte Karte bzw. den neuen Termin — nur eine Kennung (Zeichenkette
     aus [A-Za-z0-9_-], 1–64), sonst nichts. */
  function ursprungAus(ziel, op) {
    var p = op.payload || {};
    var quelle = ziel.art === "karte" ? p.card : p.termin;
    var u = quelle && quelle.boardpaarUrsprung;
    return typeof u === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(u) ? u : null;
  }
  function setzeUrsprung(board, ziel) {
    var u = ziel.ursprung;
    if (u === null) return;
    if (ziel.art === "karte") {
      var karte = karteVon(board, ziel.id);
      if (karte) karte.boardpaarUrsprung = u;
      return;
    }
    var liste = Array.isArray(board.termine) ? board.termine : [];
    for (var i = 0; i < liste.length; i++) if (liste[i] && liste[i].id === ziel.id) liste[i].boardpaarUrsprung = u;
  }
  globalThis.__falteMitZielen = function (snapJson, opsJson, optJson, ohneKlon) {
    var O = globalThis.AboardOps;
    var protokoll = [];
    var snap = JSON.parse(snapJson), ops = JSON.parse(opsJson);
    var opt = optJson ? JSON.parse(optJson) : undefined;
    /* Server-eigene Option, nicht für fold(): Agenten dürfen nur Kommentare
       bearbeiten, deren author ihr eigener Stempel ist (ADR-0130 §4). Geprüft
       gegen den Stand vor der Op, weil der Autor im Board steht, nicht in der Op. */
    var eigenerAutor = null;
    if (opt && typeof opt.eigenerAutor === "string") {
      eigenerAutor = opt.eigenerAutor;
      delete opt.eigenerAutor;
    }
    /* Server-eigene Option (ADR-0130 §1, Karte #606): Die Systemuhr ist nicht
       gestellt. Die ts der Ops stempelt der Server mit genau dieser Uhr — ob
       eine fremde Lease abgelaufen ist, lässt sich dann nicht entscheiden, und
       jedes neue Lease-Ende (until = ts + Dauer) wäre falsch. Deshalb meldet
       JEDES wirksame card.claim „uhr-unsicher“ (Server: 422) — die Übernahme
       einer fremden abgelaufenen Lease ebenso wie die einer freien Karte und
       die Erneuerung der eigenen (Sprint-77-Kritiker: vorher nur die erste;
       eine nachgehende Uhr stellte sonst eine Lease aus, die nach dem Stellen
       sofort abgelaufen war — ein Ausfall in Richtung Übernahme). Der Stand
       bleibt, wie er ist (Ausfall in Richtung Nichtstun). Eine fremde AKTIVE
       Lease trifft das nicht (die meldet ohnehin „beansprucht“). Die Regel
       steht EINMAL in ops.js (claimZustandFuerUebernahme); hier wird sie nur
       befragt. */
    /* Server-eigene Option (ADR-0136 §3): Verlaufseinträge, die direkt nach
       einer bestimmten Op an eine Karte gehören — der Vermerk „Kommentar von X
       gelöscht“. Je Op-id eine Liste {cardId, item}. Nicht als eigene Op:
       activity nimmt keine Mengen-Ops an, und ein card.field mit der ganzen
       Liste überschriebe frühere Ops desselben Batches. */
    /* Server-eigene Option (ADR-0141 §2/§5.3, Karte #827): Der Rückweg des
       Board-Paars legt eine außen angelegte Karte bzw. einen Termin innen an.
       Nur dann bleibt „boardpaarUrsprung“ (die Außen-ID) an der NEU
       angelegten Karte bzw. dem neuen Termin stehen — ops.js löscht das Feld
       bei jeder Neuanlage (VOM_FOLD_VERWALTET, TERMIN_VERWALTET), auf Bestand
       gewinnt ohnehin der Bestand. Wie anlageVertraulich ein Merkmal, das nur
       der Server setzt; Browser und lokaler Projektor kennen es nicht. */
    var boardpaarAnlage = !!(opt && opt.boardpaarAnlage === true);
    if (opt) delete opt.boardpaarAnlage;
    var verlauf = null;
    if (opt && opt.verlauf && typeof opt.verlauf === "object") {
      verlauf = opt.verlauf;
      delete opt.verlauf;
    }
    var uhrUnsicher = false;
    if (opt && opt.uhrUnsicher === true) uhrUnsicher = true;
    /* Server-eigene Option (Karte #748): Wer schreibt — "agent" oder
       "mensch". Ohne sie (lokale Aufrufer, Tests) bleibt die Agenten-Marke
       unangetastet. */
    var akteurArt = null;
    if (opt && (opt.akteurArt === "agent" || opt.akteurArt === "mensch")) akteurArt = opt.akteurArt;
    /* Server-eigene Option (Karte #789): Agenten-Anlagen werden vertraulich
       (neuAnlage oben). Setzt der Server nur auf der Seite innen. */
    var anlageVertraulich = !!(opt && opt.anlageVertraulich === true) && akteurArt === "agent";
    /* Server-eigene Option (Karte #798): Agenten-Neuanlagen bekommen die
       Herkunftsmarke. Setzt der Server für JEDEN Agenten auf der Seite innen,
       auch wenn W-4 zurückgenommen ist (dann ist anlageVertraulich gerade
       false — deshalb eine eigene Option). */
    var anlageHerkunft = !!(opt && opt.anlageHerkunft === true) && akteurArt === "agent";
    if (opt) {
      delete opt.uhrUnsicher;
      delete opt.akteurArt;
      delete opt.anlageVertraulich;
      delete opt.anlageHerkunft;
      /* Ohne weitere Optionen genau wie bisher: fold() ohne opt. */
      if (Object.keys(opt).length === 0) opt = undefined;
    }
    if (!Array.isArray(ops)) throw new Error("Ops sind kein Array");
    for (var i = 1; i < ops.length; i++) {
      if (ops[i].ts !== ops[0].ts || !(ops[i].seq > ops[i - 1].seq)) {
        throw new Error("Ops nicht in Faltordnung (gleicher ts, seq aufsteigend erwartet)");
      }
    }
    /* Karte #666 (ADR-0133 §1): Welche Op hat eine Karte NEU angelegt, und
       welche displayId (bei Findings: welche nr) hat der Fold vergeben? Die
       Antwort von POST /boards/{id}/ops nennt das je Op — sonst müsste der
       Schreiber das ganze Board nachladen, um seine neue Karte zu finden. */
    var angelegt = [];
    var erg = mitKlonModus(ohneKlon, function () {
      var stand = snap;
      var batchNeu = neueMenge(); /* Karte #789, K-1/V-1: im Batch neu angelegt (W-4) */
      for (var k = 0; k < ops.length; k++) {
        var board = stand && stand.board;
        var pu = ops[k].type === "card.upsert" ? (ops[k].payload || {}).card : null;
        var neuId = pu && typeof pu.id === "string" && board && Array.isArray(board.sprints) && !hatKarte(board, pu.id) ? pu.id : null;
        /* F-1152 (Karte #561): Ohne sprints-Array gibt es kein Ziel. Bisher
           wurde die Prüfung dann übersprungen, und fold() quittierte jede Op
           mit „nichts getan“. Jetzt zählt das Board als leer: Jede Op mit Ziel
           meldet ziel-fehlt. */
        if (board && !Array.isArray(board.sprints)) board = { sprints: [] };
        if (board) {
          var f = fehlendeZiele(board, ops[k]);
          for (var n = 0; n < f.length; n++) {
            var e = { art: "ziel-fehlt", op: ops[k].id, typ: ops[k].type };
            for (var s in f[n]) e[s] = f[n][s];
            protokoll.push(e);
          }
          if (uhrUnsicher && ops[k].type === "card.claim") {
            var pc = ops[k].payload || {};
            var karte = karteVon(board, pc.cardId);
            if (karte && O.claimZustandFuerUebernahme(karte, O.opZeitMs(ops[k]), ops[k].actor, pc.session) !== "fremd-aktiv") {
              var hc = karte.claim || {};
              protokoll.push({ art: "uhr-unsicher", op: ops[k].id, typ: ops[k].type, cardId: pc.cardId,
                halter: hc.by || null, session: hc.session || null, until: hc.until || null });
            }
          }
          if (eigenerAutor !== null && ops[k].type === "comment.edit") {
            var pk = ops[k].payload || {};
            var kom = kommentarVon(board, pk.cardId, pk.commentId);
            if (kom && kom.author !== eigenerAutor) {
              protokoll.push({ art: "fremder-kommentar", op: ops[k].id, typ: ops[k].type,
                cardId: pk.cardId, commentId: pk.commentId });
            }
          }
        }
        var vorMarke = akteurArt !== null && board ? markeVorher(board, ops[k]) : null;
        var neu = anlageVertraulich && board ? neuAnlage(board, ops[k]) : null;
        var herkunftNeu = anlageHerkunft && board ? neuAnlage(board, ops[k]) : null;
        /* Ursprung VOR dem Falten lesen: ohne Klon (Server) löscht ops.js das
           Feld bei der Neuanlage in der Nutzlast selbst. */
        var bpNeu = boardpaarAnlage && board ? neuAnlage(board, ops[k]) : null;
        if (bpNeu !== null) bpNeu.ursprung = ursprungAus(bpNeu, ops[k]);
        if (neu !== null) mengeAufnehmen(batchNeu, neu);
        stand = O.fold(stand, [ops[k]], protokoll, opt);
        if (akteurArt !== null && stand && stand.board) markeNachher(stand.board, ops[k], vorMarke, akteurArt === "agent", herkunftNeu);
        /* Kritiker #789, K-1: NACH JEDER Op alle in diesem Batch neu
           angelegten Einträge erneut vertraulich setzen, nicht nur die der
           anlegenden Op — sonst schaltete ein zweiter Upsert (oder ein
           card.field) derselben id im selben Batch ab, und die Sperre aus
           #778 sieht die Karte „vorher“ nicht. Je Op statt einmal am Ende:
           So sieht keine spätere Op des Batches (card.fieldIf mit expect,
           Verlaufseinträge, das Protokoll) einen Zwischenstand, in dem die
           Agenten-Anlage offen war. Gelöschte fehlen schlicht; wird dieselbe
           id im Batch neu angelegt, steht sie noch in der Liste. */
        if (stand && stand.board) setzeVertraulichMenge(stand.board, batchNeu);
        if (bpNeu !== null && stand && stand.board) setzeUrsprung(stand.board, bpNeu);
        if (verlauf && Object.prototype.hasOwnProperty.call(verlauf, ops[k].id)) {
          var vl = verlauf[ops[k].id] || [];
          for (var v = 0; v < vl.length; v++) {
            var vk = karteVon((stand && stand.board) || {}, vl[v].cardId);
            if (vk) vk.activity = (Array.isArray(vk.activity) ? vk.activity : []).concat([vl[v].item]);
          }
        }
        if (neuId !== null) {
          var nk = karteVon((stand && stand.board) || {}, neuId);
          if (nk) {
            var a = { op: ops[k].id, cardId: neuId, displayId: nk.displayId };
            if (nk.kind === "finding" && nk.finding && typeof nk.finding.nr === "number") a.nr = nk.finding.nr;
            angelegt.push(a);
          }
        }
      }
      return stand;
    });
    /* Dritter Teil nur hier: Falte (__falte) bleibt bei zwei Teilen. */
    return JSON.stringify(erg) + "\u0000" + JSON.stringify(protokoll) + "\u0000" + JSON.stringify(angelegt);
  };
})();`

// ErrZeitgrenze meldet, dass ein Aufruf die Zeitgrenze überschritten hat
// und per rt.Interrupt abgebrochen wurde (ADR-0129 §4 Nachtrag, Punkt 6).
var ErrZeitgrenze = errors.New("fold: Zeitgrenze überschritten")

// maxAufruftiefe begrenzt die JS-Aufruftiefe. ops.js rekursiert nicht tief;
// die Grenze schützt davor, dass eine unerwartete Rekursion den Go-Stack
// bis zum Prozessabbruch wachsen lässt, statt als Fehler zurückzukommen.
const maxAufruftiefe = 4096

var (
	programmeEinmal sync.Once
	buendelProgramm *goja.Program
	brueckeProgramm *goja.Program
	programmFehler  error
)

// programme liefert die EINMAL kompilierten Programme. goja.Program ist an
// keine Runtime gebunden und darf nebenläufig benutzt werden.
func programme() (*goja.Program, *goja.Program, error) {
	programmeEinmal.Do(func() {
		// strict=true: die Quelle ist ein ES-Modul, also strikt.
		buendelProgramm, programmFehler = goja.Compile("ops.bundle.js", buendelQuelle, true)
		if programmFehler != nil {
			return
		}
		brueckeProgramm, programmFehler = goja.Compile("bruecke.js", brueckeQuelle, true)
	})
	return buendelProgramm, brueckeProgramm, programmFehler
}

// laeufer kapselt EINE goja.Runtime. Nicht goroutinensicher — der Motor
// sorgt dafür, dass immer nur ein Aufruf sie hält.
type laeufer struct {
	rt     *goja.Runtime
	rufe   goja.Callable
	falte  goja.Callable
	zielen goja.Callable
	pruefe goja.Callable
}

func neuerLaeufer() (*laeufer, error) {
	bp, brp, err := programme()
	if err != nil {
		return nil, fmt.Errorf("fold: kompilieren: %w", err)
	}
	rt := goja.New()
	rt.SetMaxCallStackSize(maxAufruftiefe)
	if _, err := rt.RunProgram(bp); err != nil {
		return nil, fmt.Errorf("fold: Bündel laden: %w", err)
	}
	if _, err := rt.RunProgram(brp); err != nil {
		return nil, fmt.Errorf("fold: Brücke laden: %w", err)
	}
	rufe, ok1 := goja.AssertFunction(rt.Get("__rufe"))
	falte, ok2 := goja.AssertFunction(rt.Get("__falte"))
	pruefe, ok3 := goja.AssertFunction(rt.Get("__pruefe"))
	zielen, ok4 := goja.AssertFunction(rt.Get("__falteMitZielen"))
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errors.New("fold: Brücke nicht gefunden")
	}
	return &laeufer{rt: rt, rufe: rufe, falte: falte, zielen: zielen, pruefe: pruefe}, nil
}

// Optionen steuern den Motor. Nullwerte bedeuten die Voreinstellung.
type Optionen struct {
	// PoolGroesse ist die Zahl der Runtimes. Voreinstellung 2: BEGIN
	// IMMEDIATE serialisiert die Schreiber ohnehin für die ganze Datenbank,
	// mehr Runtimes kosten nur Speicher (je rund 160 KB).
	PoolGroesse int
	// Zeitgrenze je Aufruf. Voreinstellung 2 s — das Zehnfache der
	// Zielgrenze von 200 ms für 50 Ops. Sie fängt Endlosschleifen und
	// pathologische Eingaben, nicht normale Last.
	Zeitgrenze time.Duration
	// MitKlon schaltet den Snapshot-Klon in fold() wieder ein. Am Server nie
	// nötig (siehe Brücke); vorhanden für Tests und Messungen.
	MitKlon bool
}

const (
	standardPoolGroesse = 2
	standardZeitgrenze  = 2 * time.Second
)

// Motor faltet Op-Batches mit einem begrenzten Pool von Runtimes.
type Motor struct {
	frei       chan *laeufer
	zeitgrenze time.Duration
	ohneKlon   bool
	schema     int64
}

// Neu erzeugt den Motor und füllt den Pool vorab — die Kaltstartkosten
// (Kompilieren 0,8 ms, je Runtime 0,15 ms auf amd64) fallen beim
// Serverstart an, nicht in der ersten Anfrage. Ein Fehler hier heisst, dass
// das eingebettete Bündel nicht lädt: Der Server soll dann nicht starten.
func Neu(opt Optionen) (*Motor, error) {
	n := opt.PoolGroesse
	if n <= 0 {
		n = standardPoolGroesse
	}
	grenze := opt.Zeitgrenze
	if grenze <= 0 {
		grenze = standardZeitgrenze
	}
	m := &Motor{frei: make(chan *laeufer, n), zeitgrenze: grenze, ohneKlon: !opt.MitKlon}
	for i := 0; i < n; i++ {
		l, err := neuerLaeufer()
		if err != nil {
			return nil, err
		}
		if i == 0 {
			v, err := l.rt.RunString("__schemaVersion()")
			if err != nil {
				return nil, fmt.Errorf("fold: OP_SCHEMA_VERSION lesen: %w", err)
			}
			m.schema = v.ToInteger()
		}
		m.frei <- l
	}
	return m, nil
}

// SchemaVersion ist OP_SCHEMA_VERSION aus dem eingebetteten ops.js. Der
// Server stempelt sie auf jede Op, statt sie in Go zu wiederholen — sonst
// liefe eine Anhebung in ops.js am Server vorbei, und validateOp verwürfe
// jede Server-Op als Poison.
func (m *Motor) SchemaVersion() int64 { return m.schema }

// Pruefung ist das Ergebnis der Vorprüfung EINER Op.
type Pruefung struct {
	// Gueltig ist validateOp(op): Hülle, Op-Typ, schemaVersion, Pflichtfelder.
	Gueltig bool `json:"gueltig"`
	// Fehlend nennt fehlende Pflicht-Payload-Felder.
	Fehlend []string `json:"fehlend"`
	// Unbekannt nennt Payload-Schlüssel ausserhalb von OP_PAYLOAD_SPEC, je
	// mit dem vermutlich gemeinten Namen (leer, wenn keiner nahe liegt).
	Unbekannt []struct {
		Key   string  `json:"key"`
		Meant *string `json:"meant"`
	} `json:"unbekannt"`
}

// Pruefe prüft einen Ops-Batch (JSON-Array) ohne Board: validateOp und
// inspectOpPayload aus ops.js, je Op ein Eintrag in derselben Reihenfolge.
// Gedacht für die Annahme VOR der Schreibsperre.
func (m *Motor) Pruefe(ctx context.Context, opsJSON string) ([]Pruefung, error) {
	var aus string
	err := m.mitLaeufer(ctx, func(l *laeufer) error {
		v, err := l.pruefe(goja.Undefined(), l.rt.ToValue(opsJSON))
		if err != nil {
			return err
		}
		aus = v.String()
		return nil
	})
	if err != nil {
		return nil, err
	}
	var p []Pruefung
	if err := json.Unmarshal([]byte(aus), &p); err != nil {
		return nil, fmt.Errorf("fold: Prüfergebnis: %w", err)
	}
	return p, nil
}

// Ergebnis eines Folds, beides als JSON-Text.
type Ergebnis struct {
	// Projektion ist der gefaltete Snapshot (dieselbe Form wie der Eingang).
	Projektion string
	// Protokoll ist das JSON-Array, das fold() befüllt (Meldungen zu
	// unbekannten Schlüsseln, fehlenden Zielen usw.).
	Protokoll string
	// Angelegt ist ein JSON-Array: je Op, die eine Karte NEU angelegt hat,
	// die vergebene displayId und bei Findings die nr (Karte #666, ADR-0133
	// §1). Nur FalteMitZielen füllt es; bei Falte bleibt es leer. Als Text
	// statt als Slice, damit Ergebnis vergleichbar bleibt (==, fold_test.go).
	Angelegt string
}

// AngelegteKarten liest Angelegt; leer ergibt nil.
func (e Ergebnis) AngelegteKarten() ([]Angelegt, error) {
	if e.Angelegt == "" {
		return nil, nil
	}
	var a []Angelegt
	if err := json.Unmarshal([]byte(e.Angelegt), &a); err != nil {
		return nil, fmt.Errorf("fold: angelegte Karten: %w", err)
	}
	return a, nil
}

// Angelegt: eine vom Fold neu angelegte Karte. Op ist die Server-id der Op.
type Angelegt struct {
	Op        string `json:"op"`
	CardID    string `json:"cardId"`
	DisplayID int    `json:"displayId"`
	// Nr ist die Finding-Nummer (nur bei kind 'finding').
	Nr *int `json:"nr,omitempty"`
}

// Falte wendet einen Ops-Batch (JSON-Array) auf einen Snapshot (JSON-Objekt
// mit `board`) an. optionenJSON darf leer sein; sonst wird es als viertes
// Argument an fold() gereicht (z. B. `{"feldMassstab": [...]}`).
//
// Falte prüft die Ops NICHT vorab — fold() überspringt ungültige Ops still,
// wie im Projektor. Wer einen Batch annimmt, validiert vorher.
//
// Wartet ctx länger auf eine freie Runtime, als er gilt, kommt ctx.Err()
// zurück. Ein laufender Fold wird durch ctx nicht abgebrochen, nur durch
// die Zeitgrenze.
func (m *Motor) Falte(ctx context.Context, snapshotJSON, opsJSON, optionenJSON string) (Ergebnis, error) {
	return m.falteUeber(ctx, func(l *laeufer) goja.Callable { return l.falte }, snapshotJSON, opsJSON, optionenJSON)
}

// FalteMitZielen faltet wie Falte und meldet zusätzlich jede Op, deren Ziel
// (Karte bzw. Swimlane) im Stand vor ihr fehlt, als Protokolleintrag
// `art: "ziel-fehlt"` (F-1146, Karte #558). Löschungen sind ausgenommen, damit
// eine Wiederholung nach einem Verbindungsabbruch nicht scheitert.
//
// Anders als Falte verlangt es die Ops in Faltordnung: gleicher ts, seq
// aufsteigend in Array-Reihenfolge — so stempelt der Server. Sonst kommt ein
// Fehler zurück. Projektion und übriges Protokoll sind dieselben wie bei
// Falte (TestFalteMitZielenGleichFalte).
func (m *Motor) FalteMitZielen(ctx context.Context, snapshotJSON, opsJSON, optionenJSON string) (Ergebnis, error) {
	return m.falteUeber(ctx, func(l *laeufer) goja.Callable { return l.zielen }, snapshotJSON, opsJSON, optionenJSON)
}

func (m *Motor) falteUeber(ctx context.Context, fn func(*laeufer) goja.Callable, snapshotJSON, opsJSON, optionenJSON string) (Ergebnis, error) {
	var opt goja.Value = goja.Undefined()
	var aus string
	err := m.mitLaeufer(ctx, func(l *laeufer) error {
		if optionenJSON != "" {
			opt = l.rt.ToValue(optionenJSON)
		}
		v, err := fn(l)(goja.Undefined(), l.rt.ToValue(snapshotJSON), l.rt.ToValue(opsJSON), opt, l.rt.ToValue(m.ohneKlon))
		if err != nil {
			return err
		}
		aus = v.String()
		return nil
	})
	if err != nil {
		return Ergebnis{}, err
	}
	projektion, protokoll, ok := strings.Cut(aus, "\x00")
	if !ok {
		return Ergebnis{}, errors.New("fold: Brücke lieferte keine Trennmarke")
	}
	erg := Ergebnis{Projektion: projektion, Protokoll: protokoll}
	// Karte #666: FalteMitZielen liefert einen dritten Teil (angelegte Karten).
	if p, angelegt, drei := strings.Cut(protokoll, "\x00"); drei {
		erg.Protokoll, erg.Angelegt = p, angelegt
	}
	return erg, nil
}

// rufe ruft einen beliebigen Export von ops.js mit JSON-Argumenten (Array)
// auf und liefert Rückgabe und Argumente-danach. Nur für die
// Konformitätstests; ohneKlon wie im Motor.
func (m *Motor) rufe(ctx context.Context, name, argsJSON string, ohneKlon bool) (ergebnis, argsNach string, err error) {
	var aus string
	err = m.mitLaeufer(ctx, func(l *laeufer) error {
		v, err := l.rufe(goja.Undefined(), l.rt.ToValue(name), l.rt.ToValue(argsJSON), l.rt.ToValue(ohneKlon))
		if err != nil {
			return err
		}
		aus = v.String()
		return nil
	})
	if err != nil {
		return "", "", err
	}
	ergebnis, argsNach, ok := strings.Cut(aus, "\x00")
	if !ok {
		return "", "", errors.New("fold: Brücke lieferte keine Trennmarke")
	}
	return ergebnis, argsNach, nil
}

// mitLaeufer holt eine Runtime, führt fn unter der Zeitgrenze aus und legt
// danach eine vertrauenswürdige Runtime zurück.
//
// Nach JEDEM Fehler und nach jedem ausgelösten Interrupt wird die Runtime
// ersetzt, nicht zurückgelegt: Eine Exception kann globalen Zustand halb
// geändert hinterlassen (etwa die Klon-Weiche, falls das finally nicht
// lief), und ein Interrupt, der knapp nach dem Ende des Aufrufs feuert,
// bliebe gesetzt und bräche den NÄCHSTEN Aufruf ab.
func (m *Motor) mitLaeufer(ctx context.Context, fn func(*laeufer) error) error {
	var l *laeufer
	select {
	case l = <-m.frei:
	case <-ctx.Done():
		return ctx.Err()
	}

	uhr := time.AfterFunc(m.zeitgrenze, func() { l.rt.Interrupt(ErrZeitgrenze) })
	err := fn(l)
	gefeuert := !uhr.Stop()

	if err == nil && !gefeuert {
		m.frei <- l
		return nil
	}
	m.frei <- m.ersatz(l)

	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return ErrZeitgrenze
	}
	if err != nil {
		return fmt.Errorf("fold: %w", err)
	}
	// Uhr feuerte nach dem Ende von fn: Ergebnis ist vollständig und gültig.
	return nil
}

// ersatz liefert eine frische Runtime für eine verbrauchte. Scheitert das
// (praktisch nur, wenn schon das Kompilieren scheiterte — dann wäre Neu
// bereits gescheitert), wird die alte nach ClearInterrupt weiterbenutzt:
// Ein schrumpfender Pool liefe sonst irgendwann leer, und jeder spätere
// Aufruf bliebe für immer in der Warteschlange hängen.
func (m *Motor) ersatz(alt *laeufer) *laeufer {
	if neu, err := neuerLaeufer(); err == nil {
		return neu
	}
	alt.rt.ClearInterrupt()
	return alt
}
