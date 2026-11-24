package api_test

// agent_v1_test.go — Nacharbeit Verify-Lauf V1 (Sprint 82) an den
// Werkzeug-Endpunkten für Open WebUI:
//
//   - V1-3: `sicherheitshinweis` steht in den JSON-Antworten von boards,
//     board_lesen und karte_zeigen als ERSTES Feld, vor dem Fremdtext.
//   - V1-4: Wurde die Karte des ersten Aufrufs innerhalb des Fensters
//     gelöscht oder archiviert, legt die erneute Anlage neu an.
//   - Wiederholungsschutz bei parallelen Aufrufen: 12 gleichzeitige
//     karte_anlegen mit demselben Inhalt ergeben genau eine Karte.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestAgentV1_SicherheitshinweisZuerst(t *testing.T) {
	e := newEnv671(t, true)
	for _, pfad := range []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/werkzeuge/boards/b1/karten/1?format=json"} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "GET", pfad, "")
		if s != http.StatusOK {
			t.Fatalf("%s: %d %s", pfad, s, b)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(b)), `{"sicherheitshinweis":`) {
			t.Errorf("%s: Hinweis nicht als erstes Feld: %.120s", pfad, b)
		}
	}
}

// aendereKarte ändert die Karte mit dem Titel titel in Board b1 direkt im
// gespeicherten Stand: loeschen=true entfernt sie, sonst wird sie archiviert.
func (e *env671) aendereKarte(t *testing.T, titel string, loeschen bool) {
	t.Helper()
	ctx := context.Background()
	b, err := e.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b.StateJSON, &st); err != nil {
		t.Fatal(err)
	}
	gefunden := false
	for _, sp := range st["board"].(map[string]any)["sprints"].([]any) {
		spm := sp.(map[string]any)
		karten, _ := spm["cards"].([]any)
		var rest []any
		for _, k := range karten {
			km := k.(map[string]any)
			if km["title"] == titel {
				gefunden = true
				if loeschen {
					continue
				}
				km["archived"] = true
			}
			rest = append(rest, km)
		}
		if rest == nil {
			rest = []any{}
		}
		spm["cards"] = rest
	}
	if !gefunden {
		t.Fatalf("Karte %q nicht gefunden", titel)
	}
	roh, _ := json.Marshal(st)
	if _, err := e.boards.Update(ctx, "b1", b.Title, b.Version, roh, 0); err != nil {
		t.Fatal(err)
	}
}

func TestAgentV1_NeuanlageNachLoeschenOderArchivieren(t *testing.T) {
	for _, fall := range []struct {
		name     string
		loeschen bool
	}{{"geloescht", true}, {"archiviert", false}} {
		t.Run(fall.name, func(t *testing.T) {
			e := newEnv671(t, true)
			body := `{"titel":"Messestand buchen","beschreibung":"Halle 3"}`
			s1, b1 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
			if s1 != http.StatusOK {
				t.Fatalf("erste Anlage: %d %s", s1, b1)
			}
			e.aendereKarte(t, "Messestand buchen", fall.loeschen)
			s2, b2 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
			m2 := json671(t, b2)
			if s2 != http.StatusOK || m2["wiederholt"] != nil || m2["displayId"] == json671(t, b1)["displayId"] {
				t.Fatalf("erneute Anlage nach %s: %d %s — erwartet neue Karte", fall.name, s2, b2)
			}
			erwartet := 1
			if !fall.loeschen {
				erwartet = 2 // die archivierte bleibt im Stand
			}
			if n := e.zaehle(t, "b1", `"title":"Messestand buchen"`); n != erwartet {
				t.Fatalf("%d Karten im Stand, erwartet %d", n, erwartet)
			}
			// Die Wiederholung DIESES Aufrufs ist wieder eine Wiederholung.
			s3, b3 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
			if m3 := json671(t, b3); s3 != http.StatusOK || m3["wiederholt"] != true || m3["displayId"] != m2["displayId"] {
				t.Fatalf("Wiederholung der Neuanlage: %d %s", s3, b3)
			}
			if n := e.zaehle(t, "b1", `"title":"Messestand buchen"`); n != erwartet {
				t.Fatalf("nach der Wiederholung %d Karten, erwartet %d", n, erwartet)
			}
		})
	}
}

func TestAgentV1_ParalleleAnlageEineKarte(t *testing.T) {
	e := newEnv671(t, true)
	body := `{"titel":"Parallel angelegt","beschreibung":"x"}`
	const n = 12
	var wg sync.WaitGroup
	status := make([]int, n)
	koerper := make([][]byte, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status[i], koerper[i] = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
		}(i)
	}
	wg.Wait()
	neu, ids := 0, map[any]bool{}
	for i := 0; i < n; i++ {
		if status[i] != http.StatusOK {
			t.Fatalf("Aufruf %d: %d %s", i, status[i], koerper[i])
		}
		m := json671(t, koerper[i])
		ids[m["displayId"]] = true
		if m["wiederholt"] == nil {
			neu++
		}
	}
	if neu != 1 || len(ids) != 1 {
		t.Errorf("%d Aufrufe ohne „wiederholt“, %d verschiedene Nummern — erwartet je 1", neu, len(ids))
	}
	if c := e.zaehle(t, "b1", `"title":"Parallel angelegt"`); c != 1 {
		t.Fatalf("%d Karten statt einer", c)
	}
}
