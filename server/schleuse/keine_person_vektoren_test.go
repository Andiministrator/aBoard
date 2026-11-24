package schleuse

// keine_person_vektoren_test.go — gemeinsame Testvektoren für die Regel
// „keine Person“ (Kritiker KO-9): dieselbe Datei prüft der Browser-Spiegel
// istKeinePerson in src/bearbeiter-kandidaten.js
// (tests/keine-person-vektoren.test.js). Driftet eine Seite, wird hier oder
// dort ein Test rot.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKeinePerson_GemeinsameVektoren(t *testing.T) {
	roh, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "keine-person-vektoren.json"))
	if err != nil {
		t.Fatalf("Vektoren nicht lesbar: %v", err)
	}
	var vektoren []struct {
		Name        string `json:"name"`
		KeinePerson bool   `json:"keinePerson"`
	}
	if err := json.Unmarshal(roh, &vektoren); err != nil {
		t.Fatalf("Vektoren kein JSON: %v", err)
	}
	if len(vektoren) < 10 {
		t.Fatalf("nur %d Vektoren — Datei verstümmelt?", len(vektoren))
	}
	for _, v := range vektoren {
		if got := keinePerson(v.Name); got != v.KeinePerson {
			t.Errorf("keinePerson(%q) = %v, erwartet %v", v.Name, got, v.KeinePerson)
		}
	}
}
