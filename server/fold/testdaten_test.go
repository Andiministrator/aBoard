package fold

// Testdaten für Messung und Nebenläufigkeit (aus dem Spike #546 übernommen): ein synthetisches Board mit
// realistisch gefüllten Karten und ein gemischter Op-Batch. Deterministisch
// (kein Zufall, keine Uhr), damit Läufe vergleichbar bleiben.

import (
	"encoding/json"
	"fmt"
	"strings"
)

var spalten = []string{"col-backlog", "col-ready", "col-in-arbeit", "col-erledigt"}

// synthBoard baut {version, board} mit nKarten Karten, verteilt auf den
// Backlog und vier Sprints. Jede Karte trägt die Felder eines echten Boards
// (Beschreibung, Kommentare, Aktivität, agentContext) — die Grösse pro Karte
// liegt damit in der Grössenordnung des echten aBoard-Boards (~6 KB/Karte
// dort, ~2 KB hier; das echte Board wird zusätzlich gemessen).
func synthBoard(praefix string, nKarten int) string {
	type obj = map[string]any
	sprints := []obj{}
	ids := []string{"__backlog__", "s1", "s2", "s3", "s4"}
	for i, id := range ids {
		kind := "sprint"
		if id == "__backlog__" {
			kind = "backlog"
		}
		sprints = append(sprints, obj{"id": id, "title": fmt.Sprintf("Sprint %d", i), "kind": kind, "archived": false, "cards": []obj{}})
	}
	text := strings.Repeat("Beschreibungstext mit Umlauten äöü und etwas Länge. ", 6)
	for i := 0; i < nKarten; i++ {
		sp := sprints[i%len(sprints)]
		karte := obj{
			"id": fmt.Sprintf("%s-k%04d", praefix, i), "displayId": i + 1,
			"title":       fmt.Sprintf("Karte %d", i),
			"description": text, "columnId": spalten[i%len(spalten)],
			"owner": "Claude", "assignees": []string{}, "labels": []string{"server", "spike"},
			"critical": false, "blocked": false, "qa": false, "archived": false,
			"complexity": "mittel", "effort": 2, "dueDate": nil, "startDate": nil,
			"links": []obj{}, "agentContext": "## Relevante Dateien\n- src/ops.js:738-1039\n",
			"comments": []obj{
				{"id": fmt.Sprintf("c%d-1", i), "author": "Andi", "text": "Erster Kommentar", "created": "2026-09-01"},
				{"id": fmt.Sprintf("c%d-2", i), "author": "Claude", "text": "Zweiter Kommentar, etwas länger als der erste.", "created": "2026-09-02"},
			},
			"activity": []obj{
				{"ts": "2026-09-01T10:00:00.000Z", "actor": "Andi", "type": "move", "from": "col-backlog", "to": "col-ready"},
				{"ts": "2026-09-02T10:00:00.000Z", "actor": "Claude", "type": "field", "field": "qa", "value": false},
				{"ts": "2026-09-03T10:00:00.000Z", "actor": "Claude", "type": "field", "field": "effort", "value": 2},
			},
		}
		sp["cards"] = append(sp["cards"].([]obj), karte)
	}
	b := obj{"version": "2", "board": obj{
		"id": praefix, "title": "Messboard " + praefix, "nextDisplayId": nKarten + 1,
		"columnDefs": []obj{
			{"id": "col-backlog", "title": "Backlog", "color": nil},
			{"id": "col-ready", "title": "Ready", "color": nil},
			{"id": "col-in-arbeit", "title": "In Arbeit", "color": nil},
			{"id": "col-erledigt", "title": "Erledigt", "color": nil},
		},
		"sprints": sprints, "awaitingUser": nil, "templates": []obj{},
	}}
	raw, err := json.Marshal(b)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// batch baut n Ops auf die gegebenen Karten-IDs: gemischt card.field,
// card.move, comment.add, card.upsert (neue Karte), card.delete. `ts` und
// `id` setzt hier der „Server" (ADR-0129 §4, K-5) — `tag` macht sie über
// Batches hinweg eindeutig.
func batch(tag string, kartenIDs []string, n int, basisMs int) string {
	type obj = map[string]any
	ops := make([]obj, 0, n)
	for i := 0; i < n; i++ {
		k := kartenIDs[(i*7)%len(kartenIDs)]
		var typ string
		var payload obj
		switch i % 10 {
		case 0, 1, 2, 3:
			felder := []string{"critical", "qa", "blocked", "title"}
			f := felder[i%4]
			var v any = true
			if f == "title" {
				v = "Neu " + tag
			}
			typ, payload = "card.field", obj{"cardId": k, "field": f, "value": v}
		case 4, 5:
			typ, payload = "card.move", obj{"cardId": k, "toSprintId": "s2", "toColId": "col-in-arbeit", "anchor": obj{"mode": "end"}}
		case 6, 7:
			typ, payload = "comment.add", obj{"cardId": k, "comment": obj{"id": fmt.Sprintf("%s-kom%d", tag, i), "author": "Agent", "text": "Kommentar aus dem Batch " + tag, "created": "2026-09-28"}}
		case 8:
			typ, payload = "card.upsert", obj{"sprintId": "s1", "columnId": "col-ready", "card": obj{
				"id": fmt.Sprintf("%s-neu%d", tag, i), "title": "Neue Karte " + tag, "description": "", "owner": "Claude",
				"assignees": []string{}, "labels": []string{}, "comments": []obj{}, "activity": []obj{}, "links": []obj{},
				"critical": false, "blocked": false, "qa": false, "archived": false, "complexity": "", "effort": nil,
				"dueDate": nil, "startDate": nil, "agentContext": "",
			}}
		case 9:
			typ, payload = "card.delete", obj{"cardId": k}
		}
		ms := basisMs + i
		ops = append(ops, obj{
			"id":            fmt.Sprintf("%s-op%03d", tag, i),
			"ts":            fmt.Sprintf("2026-09-28T12:%02d:%02d.%03dZ", (ms/60000)%60, (ms/1000)%60, ms%1000),
			"actor":         "Agent " + tag,
			"schemaVersion": 1,
			"type":          typ,
			"payload":       payload,
		})
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// kartenIDs liest alle Karten-IDs aus einem Snapshot-JSON.
func kartenIDs(snapshot string) []string {
	var s struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID string `json:"id"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(snapshot), &s); err != nil {
		panic(err)
	}
	var ids []string
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			ids = append(ids, c.ID)
		}
	}
	return ids
}
