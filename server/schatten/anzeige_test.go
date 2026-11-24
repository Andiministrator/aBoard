package schatten

import "testing"

func TestAnzeigeTitel_Tabelle(t *testing.T) {
	leer, blank, echt := "", " \t ", "Angebot Müller"
	fälle := []struct {
		name        string
		vertraulich bool
		titel       *string
		want        string
		wantEcht    bool
	}{
		{"vertraulich mit Titel", true, &echt, InterneKarteTitel, false},
		{"Titel fehlt", false, nil, InterneKarteTitel, false},
		{"leerer Titel", false, &leer, InterneKarteTitel, false},
		{"nur Leerraum", false, &blank, InterneKarteTitel, false},
		{"echter Titel", false, &echt, echt, true},
	}
	for _, f := range fälle {
		got, ok := AnzeigeTitel(f.vertraulich, f.titel)
		if got != f.want || ok != f.wantEcht {
			t.Errorf("%s: (%q, %v), erwartet (%q, %v)", f.name, got, ok, f.want, f.wantEcht)
		}
	}
}
