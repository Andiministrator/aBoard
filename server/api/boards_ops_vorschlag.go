package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Vorschlags-Zugang (ADR-0133 §6, Karte #670).
//
// Ein Token mit der Rolle `vorschlag` gehört der Recherche-Instanz. Sie liest
// fremde Webseiten; alles, was sie schickt, kann von dort gesteuert sein. Sie
// darf deshalb genau eines: NEUE Karten anlegen. Der Server baut jede solche
// Karte selbst — aus Titel, Beschreibung und Labels der Anfrage —, vergibt die
// Karten-id, legt sie in die Swimlane „Vorschläge“ (Spalte Backlog) und legt
// diese Swimlane beim ersten Vorschlag selbst an. Was die Anfrage sonst über
// Ziel und Felder sagt, zählt nicht oder führt zur Ablehnung.
//
// Die Lenkung steht hier in Go und nicht in ops.js: Sie betrifft nur diesen
// einen Zugang, und der Fold soll für alle Schreiber dieselbe Regel bleiben.
// Was der Fold daraus macht, prüft postOps danach gegen sein Ergebnis
// (`angelegt`): Jede Vorschlags-Op muss eine Karte NEU angelegt haben, sonst
// wird nichts geschrieben.

const (
	// VorschlagSwimlaneID ist die feste id der Swimlane „Vorschläge“. Gefunden
	// wird sie über diese id, nicht über den Titel — ein Mensch darf sie
	// umbenennen, ohne dass daneben eine zweite entsteht.
	VorschlagSwimlaneID = "lane-vorschlaege"
	// vorschlagSwimlaneTitel: Titel bei der Anlage.
	vorschlagSwimlaneTitel = "Vorschläge"
	// vorschlagSpalte: Zielspalte (ADR-0133 §6). Fehlt sie im Board, nimmt
	// der Server die erste vorhandene — der Fold klemmte sonst und lehnte ab.
	vorschlagSpalte = "col-backlog"

	// MaxVorschlaegeJeBatch: Ein Batch eines Vorschlags-Tokens trägt höchstens
	// so viele Karten (statt MaxOpsJeBatch). Zusammen mit der Obergrenze der
	// Swimlane begrenzt das, wie schnell eine gesteuerte Instanz das Board
	// zuschütten kann.
	MaxVorschlaegeJeBatch = 10
	// MaxVorschlaegeInSwimlane: Liegen so viele Karten in „Vorschläge“, lehnt
	// der Server weitere ab, bis Menschen aufräumen. Ohne diese Grenze füllte
	// eine Schleife das Board bis MaxBodyBytes — danach könnte auch kein
	// Mensch mehr speichern.
	MaxVorschlaegeInSwimlane = 200

	maxVorschlagTitel        = 300  // Zeichen
	maxVorschlagBeschreibung = 8000 // Zeichen
	maxVorschlagLabels       = 10
	maxVorschlagLabel        = 40 // Zeichen je Label
)

// vorschlagNamensraum: Namensraum der abgeleiteten Karten-ids (UUIDv5).
var vorschlagNamensraum = uuid.MustParse("6f1d3c52-9a0e-4c47-8f43-0b7c2e5d8a61")

var (
	errVorschlagArchiviert = errors.New("vorschlag: die swimlane „Vorschläge“ ist archiviert")
	errVorschlagVoll       = errors.New("vorschlag: die swimlane „Vorschläge“ ist voll")
	errVorschlagNichtNeu   = errors.New("vorschlag: keine neue karte")
)

// istVorschlag: Der Principal ist ein Vorschlags-Token.
func istVorschlag(agent *AgentPrincipal) bool {
	return agent != nil && agent.Role == storage.AgentRolleVorschlag
}

// vorschlagKartenFelder: Was die Anfrage in `card` mitgeben darf. Alles andere
// führt zur Ablehnung — still verworfen hielte die Instanz es für gesetzt.
//
// Bewusst NICHT erlaubt (je mit Grund):
//   - owner, assignees: Benennen Personen, und der Browser leitet daraus
//     „Meine Karten“ und Benachrichtigungen ab.
//   - dueDate, startDate, dueTime, startTime, recurrence: Fristen erzeugen Erinnerungen und
//     Kalendereinträge; wiederkehrende Karten erzeugen weitere Karten.
//   - links: Verweise ändern die Sicht ANDERER Karten (Symmetrie-Pflicht) und
//     verraten bei Erfolg/Misserfolg, welche Karten-ids es gibt.
//   - comments, activity: Urheberschaft — die setzt nur der Server.
//   - attachments, coverAttachmentId: Anhänge laufen über eigene Endpunkte,
//     die dieses Token nicht erreicht.
//   - agentContext: Kontext für Sub-Agents; aus fremden Webseiten wäre das
//     eine Anweisung an den nächsten Agenten, der die Karte bearbeitet.
//   - kind, finding, metaType: keine Findings, keine Sprint-Meta-Karten.
//   - critical, blocked, qa, archived, status, priority, complexity, effort,
//     project_id, milestone, session, checklist: Planung ist Sache der
//     Menschen, die den Vorschlag übernehmen.
//   - id, displayId, columnId: vergibt der Server bzw. der Fold. `id` wird
//     angenommen (card.upsert verlangt sie überall sonst), aber nicht benutzt.
var vorschlagKartenFelder = map[string]bool{"id": true, "title": true, "description": true, "labels": true}

// vorschlagPayloadFelder: Schlüssel der Payload selbst. sprintId, columnId,
// targetIndex und anchor werden angenommen und überschrieben — das Ziel
// bestimmt allein der Server.
var vorschlagPayloadFelder = map[string]bool{"card": true, "sprintId": true, "columnId": true, "targetIndex": true, "anchor": true}

// vorschlagPayload baut aus der Anfrage die Payload, die gefaltet wird, oder
// nennt den Grund der Ablehnung. clientID ist die `id` der Op (leer = keine);
// ts die Eingangszeit des Batches.
//
// Die Karten-id vergibt der Server: mit Client-id abgeleitet aus Token und
// Client-id (eine Wiederholung trifft dieselbe Karte und wird über die
// Idempotenz als „wiederholt“ quittiert), sonst zufällig. Eine Instanz kann
// damit keine id einer bestehenden Karte treffen; dass die Karte wirklich neu
// ist, prüft postOps zusätzlich am Ergebnis des Folds.
func vorschlagPayload(typ string, payload json.RawMessage, agent *AgentPrincipal, clientID, ts string) (json.RawMessage, string) {
	// Nur card.upsert. Ausdrücklich auch KEINE Termine (termin.upsert,
	// termin.delete; Karte #743, ADR-0138 §4): Ein Termin geht über den
	// Abo-Feed nach außen und in fremde Kalender — aus fremden Webseiten
	// angelegt wäre das ein Abflussweg, gleicher Grund wie bei Fristen
	// (dueDate/startDate oben in vorschlagKartenFelder).
	if typ != "card.upsert" {
		return nil, "a suggestion token may only create new cards (card.upsert); " + typ + " is not allowed"
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil || p == nil {
		return nil, "payload must be an object"
	}
	if k := schreibweisenDoppel(p); k != "" {
		return nil, "payload key " + k + " appears twice in different case"
	}
	for k := range p {
		if !vorschlagPayloadFelder[k] {
			return nil, "payload key " + k + " is not allowed for suggestions"
		}
	}
	var karte map[string]json.RawMessage
	if json.Unmarshal(p["card"], &karte) != nil || karte == nil {
		return nil, "card must be an object"
	}
	if k := schreibweisenDoppel(karte); k != "" {
		return nil, "card key " + k + " appears twice in different case"
	}
	for k := range karte {
		if !vorschlagKartenFelder[k] {
			return nil, "card field " + k + " is not allowed for suggestions (allowed: title, description, labels)"
		}
	}
	var titel string
	if json.Unmarshal(karte["title"], &titel) != nil || strings.TrimSpace(titel) == "" {
		return nil, "card.title must be a non-empty string"
	}
	titel = strings.TrimSpace(titel)
	if utf8.RuneCountInString(titel) > maxVorschlagTitel || hatSteuerzeichen(titel, false) {
		return nil, fmt.Sprintf("card.title must be at most %d characters without control characters", maxVorschlagTitel)
	}
	beschreibung := ""
	if roh, ok := karte["description"]; ok && strings.TrimSpace(string(roh)) != "null" {
		if json.Unmarshal(roh, &beschreibung) != nil {
			return nil, "card.description must be a string"
		}
		if utf8.RuneCountInString(beschreibung) > maxVorschlagBeschreibung || hatSteuerzeichen(beschreibung, true) {
			return nil, fmt.Sprintf("card.description must be at most %d characters without control characters (line breaks and tabs are fine)", maxVorschlagBeschreibung)
		}
	}
	labels := []string{}
	if roh, ok := karte["labels"]; ok && strings.TrimSpace(string(roh)) != "null" {
		var roheLabels []string
		if json.Unmarshal(roh, &roheLabels) != nil {
			return nil, "card.labels must be an array of strings"
		}
		if len(roheLabels) > maxVorschlagLabels {
			return nil, fmt.Sprintf("card.labels may have at most %d entries", maxVorschlagLabels)
		}
		for _, l := range roheLabels {
			l = strings.TrimSpace(l)
			if l == "" || utf8.RuneCountInString(l) > maxVorschlagLabel || hatSteuerzeichen(l, false) {
				return nil, fmt.Sprintf("each label must be a non-empty string of at most %d characters without control characters", maxVorschlagLabel)
			}
			labels = append(labels, l)
		}
	}

	kartenID := uuid.NewString()
	if clientID != "" {
		kartenID = uuid.NewSHA1(vorschlagNamensraum, []byte(agent.TokenID+"\x00"+clientID)).String()
	}
	datum := ts
	if len(datum) >= 10 {
		datum = datum[:10]
	}
	// Felder wie Model.createCard (src/model.js), mit leeren Werten für alles,
	// was der Vorschlag nicht setzen darf. Der Verlaufseintrag nennt den
	// Zugang, damit Menschen die Herkunft am Kartenverlauf sehen — gesetzt vom
	// Server, nie von der Instanz.
	neu := map[string]any{
		"card": map[string]any{
			"id": kartenID, "title": titel, "description": beschreibung,
			"status": "offen", "priority": "mittel", "labels": labels,
			"milestone": nil, "session": nil, "comments": []any{},
			"qa": false, "critical": false, "blocked": false,
			"owner": "", "assignees": []any{}, "project_id": nil,
			"complexity": "", "effort": nil, "agentContext": "",
			"links": []any{}, "attachments": []any{}, "coverAttachmentId": nil,
			"dueDate": nil, "startDate": nil, "recurrence": nil, "archived": false,
			"activity": []any{map[string]any{
				"id": uuid.NewString(), "author": agent.Akteur(), "action": "vorschlag",
				"note": "angelegt über den Vorschlags-Zugang „" + agent.Name + "“", "timestamp": ts,
			}},
			"columnId": vorschlagSpalte, "created": datum, "updated": datum,
		},
		"sprintId": VorschlagSwimlaneID,
		"columnId": vorschlagSpalte,
	}
	aus, err := json.Marshal(neu)
	if err != nil {
		return nil, "suggestion could not be encoded"
	}
	return aus, ""
}

// hatSteuerzeichen: Steuerzeichen (C0, DEL, C1). mitUmbruch lässt Zeilenumbruch
// und Tabulator zu (Beschreibung).
func hatSteuerzeichen(s string, mitUmbruch bool) bool {
	for _, c := range s {
		if mitUmbruch && (c == '\n' || c == '\r' || c == '\t') {
			continue
		}
		if c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0) {
			return true
		}
	}
	return false
}

// vorschlagVorbereiten läuft unter der Schreibsperre, mit dem Stand, auf den
// gefaltet wird. Es
//   - findet die Swimlane „Vorschläge“ über ihre id; fehlt sie, stellt es eine
//     swimlane.upsert-Op voran, die sie anlegt (kind lane);
//   - lehnt ab, wenn sie archiviert ist (errVorschlagArchiviert) — Menschen
//     haben die Vorschläge damit weggeräumt; still in eine unsichtbare
//     Swimlane zu schreiben hiesse, dass niemand sie sieht, und sie
//     zurückzuholen, ihre Entscheidung zu übergehen;
//   - lehnt ab, wenn danach mehr als MaxVorschlaegeInSwimlane Karten darin
//     lägen (errVorschlagVoll);
//   - setzt die Zielspalte auf col-backlog oder, fehlt die, die erste Spalte.
//
// Die Seqs werden neu durchnummeriert (fold verlangt aufsteigende seq bei
// gleichem ts); id, ts und Akteur bleiben.
func vorschlagVorbereiten(state []byte, ops []serverOp) ([]serverOp, error) {
	var stand struct {
		Board map[string]json.RawMessage `json:"board"`
	}
	if err := json.Unmarshal(state, &stand); err != nil {
		return nil, fmt.Errorf("vorschlag: stand: %w", err)
	}
	// Exakte Schlüssel wie im Fold (Sitzungs-QA K-1): Ein Struct-Unmarshal nähme
	// auch "ARCHIVED" oder "ID" — dann sähe Go eine andere Swimlane als der Fold.
	var spalten []map[string]json.RawMessage
	_ = json.Unmarshal(stand.Board["columnDefs"], &spalten)
	spalte := vorschlagSpalte
	if len(spalten) > 0 {
		gefunden := false
		for _, s := range spalten {
			if zeichenketteVon(s["id"]) == vorschlagSpalte {
				gefunden = true
				break
			}
		}
		if !gefunden {
			spalte = zeichenketteVon(spalten[0]["id"])
		}
	}
	var swimlanes []map[string]json.RawMessage
	_ = json.Unmarshal(stand.Board["sprints"], &swimlanes)
	vorhanden, belegt := false, 0
	for _, s := range swimlanes {
		if zeichenketteVon(s["id"]) != VorschlagSwimlaneID {
			continue
		}
		vorhanden = true
		if jsWahr(s["archived"]) {
			return nil, errVorschlagArchiviert
		}
		var karten []json.RawMessage
		_ = json.Unmarshal(s["cards"], &karten)
		belegt = len(karten)
		break
	}
	neue := 0
	for _, op := range ops {
		if op.Type == "card.upsert" {
			neue++
		}
	}
	if belegt+neue > MaxVorschlaegeInSwimlane {
		return nil, errVorschlagVoll
	}

	aus := make([]serverOp, 0, len(ops)+1)
	if !vorhanden && len(ops) > 0 {
		lane, _ := json.Marshal(map[string]any{"sprint": map[string]any{
			"id": VorschlagSwimlaneID, "title": vorschlagSwimlaneTitel, "kind": "lane"}})
		aus = append(aus, serverOp{ID: uuid.NewString(), TS: ops[0].TS, Actor: ops[0].Actor,
			SchemaVersion: ops[0].SchemaVersion, Type: "swimlane.upsert", Payload: lane})
	}
	for _, op := range ops {
		if op.Type == "card.upsert" && spalte != vorschlagSpalte {
			var p map[string]json.RawMessage
			if err := json.Unmarshal(op.Payload, &p); err != nil {
				return nil, fmt.Errorf("vorschlag: payload: %w", err)
			}
			var karte map[string]json.RawMessage
			if err := json.Unmarshal(p["card"], &karte); err != nil {
				return nil, fmt.Errorf("vorschlag: karte: %w", err)
			}
			karte["columnId"], _ = json.Marshal(spalte)
			p["card"], _ = json.Marshal(karte)
			p["columnId"], _ = json.Marshal(spalte)
			op.Payload, _ = json.Marshal(p)
		}
		aus = append(aus, op)
	}
	for i := range aus {
		aus[i].Seq = i
	}
	return aus, nil
}

// zeichenketteVon liest eine JSON-Zeichenkette, sonst "".
func zeichenketteVon(roh json.RawMessage) string {
	var s string
	_ = json.Unmarshal(roh, &s)
	return s
}

// jsWahr: Wahrheitswert wie in JavaScript (der Browser prüft `sprint.archived`
// so) — fehlend, null, false, 0 und "" sind falsch, alles andere wahr.
func jsWahr(roh json.RawMessage) bool {
	switch t := strings.TrimSpace(string(roh)); t {
	case "", "null", "false", `""`:
		return false
	default:
		var n float64
		if json.Unmarshal(roh, &n) == nil {
			return n != 0
		}
		return true
	}
}
