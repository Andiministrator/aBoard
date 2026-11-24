package storage

// finding_zaehler_1685_test.go — eine NEUE Finding-Karte mit einer Nummer
// unter dem Zähler bekommt beim Ganz-Board-Schreiben die nächste freie Nummer
// (F-1685, Kritiker R2 KF-8). Bestehende Karten bleiben unangetastet.

import (
	"context"
	"encoding/json"
	"testing"
)

func f1685Karte(id string, nr int) string {
	b, _ := json.Marshal(map[string]any{"id": id, "title": id, "columnId": "col-backlog", "kind": "finding",
		"finding": map[string]any{"nr": nr, "typ": "V", "prioritaet": "P3", "status": "offen"}})
	return string(b)
}

func f1685Stand(zaehler string, karten ...string) []byte {
	z := ""
	if zaehler != "" {
		z = `"nextFindingNr":` + zaehler + `,`
	}
	k := ""
	for i, x := range karten {
		if i > 0 {
			k += ","
		}
		k += x
	}
	return []byte(`{"version":"1","board":{"id":"b","title":"B",` + z + `"sprints":[{"id":"s","cards":[` + k + `]}]}}`)
}

func f1685Nummern(t *testing.T, state []byte) map[string]int {
	t.Helper()
	var s struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID      string `json:"id"`
					Finding struct {
						Nr int `json:"nr"`
					} `json:"finding"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(state, &s); err != nil {
		t.Fatal(err)
	}
	aus := map[string]int{}
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			aus[c.ID] = c.Finding.Nr
		}
	}
	return aus
}

func TestUpdate_F1685_NeueKarteUnterDemZaehlerWirdUmnummeriert(t *testing.T) {
	ctx := context.Background()
	// Gespeichert: F-3 und F-9; F-7 war vergeben und ist gelöscht → Zähler 10.
	repo := zaehlerRepo(t, string(f1685Stand("10", f1685Karte("k3", 3), f1685Karte("k9", 9))))
	// Browser legt per PUT eine neue Karte mit der alten Nummer 7 an, eine zweite mit 12.
	b, err := repo.Update(ctx, "b", "B", "1",
		f1685Stand("8", f1685Karte("k3", 3), f1685Karte("k9", 9), f1685Karte("neu", 7), f1685Karte("neu2", 12)), 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	nr := f1685Nummern(t, b.StateJSON)
	if nr["neu"] != 10 {
		t.Errorf("neue Karte mit Nummer 7: jetzt %d, erwartet 10 (nächste freie)", nr["neu"])
	}
	if nr["neu2"] != 12 || nr["k3"] != 3 || nr["k9"] != 9 {
		t.Errorf("andere Nummern verändert: %v", nr)
	}
	if got := findingZaehler(b.StateJSON); got != 13 {
		t.Errorf("nextFindingNr = %d, erwartet 13", got)
	}
}

func TestUpdate_F1685_ZaehlerFehltAltwertAusHoechsterNummer(t *testing.T) {
	ctx := context.Background()
	// Gespeichert ohne Zähler: höchste Nummer 4 → frei ab 5.
	repo := zaehlerRepo(t, string(f1685Stand("", f1685Karte("k4", 4))))
	b, err := repo.Update(ctx, "b", "B", "1", f1685Stand("", f1685Karte("k4", 4), f1685Karte("neu", 2)), 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if nr := f1685Nummern(t, b.StateJSON); nr["neu"] != 5 || nr["k4"] != 4 {
		t.Errorf("Nummern: %v, erwartet neu=5, k4=4", nr)
	}
}

func TestUpdate_F1685_BestandBleibtByteGleich(t *testing.T) {
	ctx := context.Background()
	stand := f1685Stand("10", f1685Karte("k3", 3), f1685Karte("k9", 9))
	repo := zaehlerRepo(t, string(stand))
	b, err := repo.Update(ctx, "b", "B", "1", stand, 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if string(b.StateJSON) != string(stand) {
		t.Errorf("unveränderter Stand umgeschrieben:\n%s", b.StateJSON)
	}
}

// PF-1 (Prüflauf F-1685): Mehrere neue Karten bei veraltetem Zähler — die
// Umnummerierung überspringt Nummern, die andere Karten behalten.
func TestUpdate_F1685_MehrereNeueKartenOhneDoppelnummer(t *testing.T) {
	ctx := context.Background()
	repo := zaehlerRepo(t, string(f1685Stand("9", f1685Karte("a", 8))))
	b, err := repo.Update(ctx, "b", "B", "1",
		f1685Stand("7", f1685Karte("a", 8), f1685Karte("n7", 7), f1685Karte("n9", 9), f1685Karte("n5", 5), f1685Karte("n10", 10)), 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	nr := f1685Nummern(t, b.StateJSON)
	gesehen := map[int]string{}
	for id, n := range nr {
		if andere, da := gesehen[n]; da {
			t.Fatalf("Doppelnummer F-%d: %s und %s (%v)", n, id, andere, nr)
		}
		gesehen[n] = id
	}
	if nr["a"] != 8 || nr["n9"] != 9 || nr["n10"] != 10 {
		t.Errorf("bleibende Nummern verändert: %v", nr)
	}
	if nr["n7"] != 11 || nr["n5"] != 12 {
		t.Errorf("Umnummerierung: %v, erwartet n7=11, n5=12 (9 und 10 belegt)", nr)
	}
	if got := findingZaehler(b.StateJSON); got != 13 {
		t.Errorf("nextFindingNr = %d, erwartet 13", got)
	}
}
