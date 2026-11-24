package main

// Sprint-94-Kritik (ADR-0141 §2, §5.3): Nacharbeit am Rückweg innen.
//   - K-1: angelegt wird nie wegen des Orts verworfen — fehlt Lane oder
//     Spalte, geht die Karte an einen Ersatzort mit Hinweis-Kommentar. Ein
//     verworfenes angelegt hätte die wartende Karte außen beim nächsten
//     Hinweg still entfernt.
//   - K-2: Kennungen (ursprung, Kommentar-IDs) in der Form von Fold und
//     Hinweg, sonst 422.
//   - S-1: owner nur leer oder eine Identität innen.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

func anlageKritik94(op, aussenID, lane, spalte, owner string) map[string]any {
	return map[string]any{"op_id": op, "objekt": "karte", "id": schatten.AbgeleiteteID(aussenID), "art": "angelegt",
		"stelle": "", "ursprung": aussenID,
		"nachher": map[string]any{"lane": lane, "spalte": spalte, "nach": nil, "title": "Von außen " + op,
			"description": "", "priority": "mittel", "critical": false, "blocked": false, "owner": owner,
			"startDate": nil, "startTime": nil, "dueDate": nil, "dueTime": nil, "archived": false,
			"labels": []string{}, "assignees": []string{}, "vertraulich": false,
			"comments": []map[string]any{{"id": "c-" + op, "author": "Max", "text": "Erster"}}}}
}

func TestBoardpaarKritik94_AngelegtAnErsatzort(t *testing.T) {
	s := neueInnenSeite827(t)
	faelle := []struct {
		name, op, aussen, lane, spalte string
		wLane, wSpalte                 string
	}{
		{"Lane fehlt innen", "e-1", "aussen-e1", "lane-weg", "col-ready", "__backlog__", "col-ready"},
		{"Spalte fehlt innen", "e-2", "aussen-e2", "sprint-1", "spalte-weg", "sprint-1", "col-backlog"},
		{"beides fehlt", "e-3", "aussen-e3", "lane-weg", "spalte-weg", "__backlog__", "col-backlog"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			s.erwarte(t, anlageKritik94(f.op, f.aussen, f.lane, f.spalte, ""), "angewendet", "")
			lane, k := karte827(s.stand(t), schatten.AbgeleiteteID(f.aussen))
			if k == nil || lane != f.wLane || k["columnId"] != f.wSpalte || k["boardpaarUrsprung"] != f.aussen {
				t.Fatalf("Karte nicht am Ersatzort: %s %v", lane, k)
			}
			kom := kommentare827(k)
			if len(kom) != 2 || kom[0]["text"] != "Erster" || kom[1]["author"] != "Board-Paar" ||
				!strings.Contains(kom[1]["text"].(string), "Dieser Ort steht innen nicht zur Verfügung") {
				t.Fatalf("Hinweis-Kommentar fehlt: %v", kom)
			}
			// Wiederholung: schon angewendet, kein zweiter Hinweis.
			s.erwarte(t, anlageKritik94(f.op, f.aussen, f.lane, f.spalte, ""), "schon-angewendet", "")
		})
	}
	// Am richtigen Ort: kein Hinweis.
	s.erwarte(t, anlageKritik94("e-4", "aussen-e4", "sprint-1", "spalte-review", ""), "angewendet", "")
	if _, k := karte827(s.stand(t), schatten.AbgeleiteteID("aussen-e4")); len(kommentare827(k)) != 1 {
		t.Fatalf("Hinweis ohne Ersatzort: %v", kommentare827(k))
	}
}

func TestBoardpaarKritik94_OwnerNurIdentitaetInnen(t *testing.T) {
	s := neueInnenSeite827(t)
	s.erwarte(t, feld827("o-1", "k1", "owner", nil, "Claude Opus"), "verworfen", "bearbeiter-unbekannt")
	s.erwarte(t, feld827("o-2", "k1", "owner", nil, "Gerd Muster"), "angewendet", "")
	if _, k := karte827(s.stand(t), "k1"); k["owner"] != "Gerd Muster" {
		t.Fatalf("owner: %v", k["owner"])
	}
	s.erwarte(t, feld827("o-3", "k1", "owner", "Gerd Muster", ""), "angewendet", "")
	if _, k := karte827(s.stand(t), "k1"); k["owner"] != "" {
		t.Fatalf("owner leeren: %v", k["owner"])
	}
	s.erwarte(t, anlageKritik94("o-4", "aussen-o4", "sprint-1", "col-ready", "Claude Opus"), "angewendet", "")
	if _, k := karte827(s.stand(t), schatten.AbgeleiteteID("aussen-o4")); k["owner"] != "" {
		t.Fatalf("fremder owner bei der Anlage: %v", k["owner"])
	}
	s.erwarte(t, anlageKritik94("o-5", "aussen-o5", "sprint-1", "col-ready", "Andi"), "angewendet", "")
	if _, k := karte827(s.stand(t), schatten.AbgeleiteteID("aussen-o5")); k["owner"] != "Andi" {
		t.Fatalf("bekannter owner bei der Anlage: %v", k["owner"])
	}
}

func TestBoardpaarKritik94_KennungsformAmRueckweg(t *testing.T) {
	s := neueInnenSeite827(t)
	if code, m := s.anwenden(t, anlageKritik94("f-1", "a.b", "sprint-1", "col-ready", "")); code != 422 {
		t.Fatalf("ursprung a.b: %d %v", code, m)
	}
	falscherKommentar := anlageKritik94("f-2", "aussen-f2", "sprint-1", "col-ready", "")
	falscherKommentar["nachher"].(map[string]any)["comments"] = []map[string]any{{"id": "c.1", "author": "Max", "text": "x"}}
	if code, m := s.anwenden(t, falscherKommentar); code != 422 {
		t.Fatalf("Kommentar-ID c.1 bei der Anlage: %d %v", code, m)
	}
	if code, m := s.anwenden(t, map[string]any{"op_id": "f-3", "objekt": "karte", "id": "k1", "art": "kommentar.add",
		"stelle": "kommentar:c 2", "nachher": map[string]any{"id": "c 2", "author": "Max", "text": "x"}}); code != 422 {
		t.Fatalf("Kommentar-ID mit Leerzeichen: %d %v", code, m)
	}
	// Gegenprobe: gültige Form geht durch.
	s.erwarte(t, map[string]any{"op_id": "f-4", "objekt": "karte", "id": "k1", "art": "kommentar.add",
		"stelle": "kommentar:c-2", "nachher": map[string]any{"id": "c-2", "author": "Max", "text": "x"}}, "angewendet", "")
}

// umbauen827 ändert das Board innen von Hand (Testvorbereitung).
func umbauen827(t *testing.T, s *innen827, f func(board map[string]any)) {
	t.Helper()
	ctx := context.Background()
	b, err := s.boards.Get(ctx, "b-innen")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b.StateJSON, &st); err != nil {
		t.Fatal(err)
	}
	f(st["board"].(map[string]any))
	neu, _ := json.Marshal(st)
	if _, err := s.boards.Update(ctx, "b-innen", b.Title, b.Version, neu, b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

// Sitzungs-Kritik zum Sprint-94-Abschluss N-3 (F-2270): Eine innen
// archivierte Lane oder eine Service-Lane spiegelt der Hinweg nicht — die
// Anlage geht wie bei einer fehlenden Lane in den Backlog. Die Ersatzspalte
// ist col-backlog bzw. die erste mit gespiegelter Kennung. N-7 (F-2273): Der
// Hinweis nennt den Wunschort mit Titel, nie als rohe innere ID.
func TestBoardpaarSitzungskritik94_ErsatzortArchiviertServiceSpalte(t *testing.T) {
	s := neueInnenSeite827(t)
	umbauen827(t, s, func(b map[string]any) {
		b["sprints"] = append(b["sprints"].([]any),
			map[string]any{"id": "sprint-alt", "title": "Sprint Alt", "kind": "sprint", "archived": true, "cards": []any{}},
			map[string]any{"id": "svc", "title": "Service", "kind": "service", "cards": []any{}})
	})
	hinweis := func(t *testing.T, aussen string) (string, string) {
		t.Helper()
		lane, k := karte827(s.stand(t), schatten.AbgeleiteteID(aussen))
		kom := kommentare827(k)
		if k == nil || len(kom) != 2 || kom[1]["author"] != "Board-Paar" {
			t.Fatalf("%s: Karte oder Hinweis fehlt: %s %v", aussen, lane, kom)
		}
		return lane, kom[1]["text"].(string)
	}
	s.erwarte(t, anlageKritik94("g-1", "aussen-g1", "sprint-alt", "col-ready", ""), "angewendet", "")
	if lane, text := hinweis(t, "aussen-g1"); lane != "__backlog__" || !strings.Contains(text, "„Sprint Alt“ (archiviert) / „Ready“") ||
		strings.Contains(text, "sprint-alt") {
		t.Fatalf("archivierte Lane: %s %q", lane, text)
	}
	s.erwarte(t, anlageKritik94("g-2", "aussen-g2", "svc", "col-ready", ""), "angewendet", "")
	if lane, _ := hinweis(t, "aussen-g2"); lane != "__backlog__" {
		t.Fatalf("Service-Lane: %s", lane)
	}
	s.erwarte(t, anlageKritik94("g-3", "aussen-g3", "lane-weg", "spalte-weg", ""), "angewendet", "")
	if _, text := hinweis(t, "aussen-g3"); !strings.Contains(text, "eine Lane, die es innen nicht mehr gibt / eine Spalte, die es innen nicht mehr gibt") ||
		strings.Contains(text, "lane-weg") || strings.Contains(text, "spalte-weg") || !strings.Contains(text, "„Backlog / Backlog“") {
		t.Fatalf("fehlender Ort: %q", text)
	}
	// Ohne col-backlog: die erste Spalte mit gespiegelter Kennung, nicht x.1.
	umbauen827(t, s, func(b map[string]any) {
		b["columnDefs"] = []any{map[string]any{"id": "x.1", "title": "Kaputt"}, map[string]any{"id": "col-ready", "title": "Ready"}}
	})
	s.erwarte(t, anlageKritik94("g-4", "aussen-g4", "sprint-1", "spalte-weg", ""), "angewendet", "")
	if _, k := karte827(s.stand(t), schatten.AbgeleiteteID("aussen-g4")); k == nil || k["columnId"] != "col-ready" {
		t.Fatalf("Ersatzspalte: %v", k)
	}
}
