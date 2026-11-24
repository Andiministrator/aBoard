package api_test

// Kritiker Sprint 82, Runde 2, KO-4: Bei doppelter displayId nahmen
// karte_zeigen, kommentieren und verschieben still die erste Karte. Jetzt
// 400 mit den Karten-ids (wie findeSwimlane bei mehrdeutigem Namen); über
// die id ist jede der beiden Karten weiter erreichbar.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// doppelteNummer gibt Karte k5 in Board b1 die Nummer 1 — dieselbe wie k1.
func (e *env671) doppelteNummer(t *testing.T) {
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
	gesetzt := false
	for _, sp := range st["board"].(map[string]any)["sprints"].([]any) {
		karten, _ := sp.(map[string]any)["cards"].([]any)
		for _, k := range karten {
			if km := k.(map[string]any); km["id"] == "k5" {
				km["displayId"] = 1
				gesetzt = true
			}
		}
	}
	if !gesetzt {
		t.Fatal("Karte k5 nicht gefunden")
	}
	roh, _ := json.Marshal(st)
	if _, err := e.boards.Update(ctx, "b1", b.Title, b.Version, roh, 0); err != nil {
		t.Fatal(err)
	}
}

func TestWerkzeugeKO4_DoppelteNummerMehrdeutig(t *testing.T) {
	e := newEnv671(t, true)
	e.doppelteNummer(t)
	vorher := e.stand(t, "b1")
	for _, fall := range []struct{ name, methode, pfad, body string }{
		{"karte_zeigen", "GET", "/werkzeuge/boards/b1/karten/1?format=json", ""},
		{"karte_zeigen #", "GET", "/werkzeuge/boards/b1/karten/%231?format=json", ""},
		{"kommentieren", "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"An welche Karte?"}`},
		{"verschieben", "POST", "/werkzeuge/boards/b1/karten/1/verschieben", `{"spalte":"Ready"}`},
	} {
		s, b := e.rufe(t, e.token, "anna@firma.de", fall.methode, fall.pfad, fall.body)
		if s != http.StatusBadRequest {
			t.Errorf("%s: Status %d, erwartet 400 bei doppelter Nummer: %s", fall.name, s, b)
			continue
		}
		fehler, _ := json671(t, b)["error"].(string)
		if !strings.Contains(fehler, "k1") || !strings.Contains(fehler, "k5") || !strings.Contains(fehler, "id") {
			t.Errorf("%s: Fehlertext nennt nicht beide Karten-ids: %q", fall.name, fehler)
		}
	}
	if nachher := e.stand(t, "b1"); nachher != vorher {
		t.Error("bei mehrdeutiger Nummer wurde trotzdem geschrieben")
	}
	// Über die id ist jede Karte eindeutig erreichbar.
	for _, id := range []string{"k1", "k5"} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/"+id+"?format=json", "")
		if s != http.StatusOK {
			t.Errorf("karte_zeigen %s: Status %d: %s", id, s, b)
		}
	}
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/k5/kommentare", `{"text":"Diese hier"}`)
	if s != http.StatusOK {
		t.Fatalf("kommentieren k5: Status %d: %s", s, b)
	}
	if !strings.Contains(e.stand(t, "b1"), "Diese hier") {
		t.Error("Kommentar an k5 nicht im Stand")
	}
	// Eine eindeutige Nummer bleibt unverändert nutzbar.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/6?format=json", ""); s != http.StatusOK {
		t.Errorf("karte_zeigen 6: Status %d: %s", s, b)
	}
	// Unbekannte Nummer: weiter 404.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/99?format=json", ""); s != http.StatusNotFound {
		t.Errorf("karte_zeigen 99: Status %d, erwartet 404: %s", s, b)
	}
}
