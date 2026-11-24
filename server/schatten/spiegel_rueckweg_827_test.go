package schatten

// Karte #827 (ADR-0141 §7, D4/D7): Spiegel-Schranke mit Rückweg und der
// Schutz wartender Spiegel im Schutzmodus.

import (
	"strings"
	"testing"
)

func verstoesse827(t *testing.T, vorher, nachher []byte, wartend map[string]bool) []SpiegelOrt {
	t.Helper()
	v, unlesbar := SpiegelVerstoesseRueckweg(vorher, nachher, wartend)
	if unlesbar {
		t.Fatal("unlesbar")
	}
	return v
}

func TestSchrankeRueckweg827_Erlaubt(t *testing.T) {
	a := sk("a", "KA", `"comments":[{"id":"k1","author":"Anna","text":"alt","created":"c","herkunft":"aussen"}]`)
	v := kopie827(a+","+sk("b", "KB", ""), "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`)
	faelle := map[string][]byte{
		"Titel, Labels, Termin":                kopie827(strings.Replace(strings.Replace(a, `"title":"T"`, `"title":"Neu"`, 1), `"labels":[]`, `"labels":["x"]`, 1)+","+sk("b", "KB", ""), "", "", `[{"id":"t-a","titel":"T2","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"Begleitfelder":                        kopie827(strings.Replace(a, `"labels":[]`, `"labels":[],"activity":[{"x":1}],"updated":"2026-10-13","fristVonAgent":true,"claim":{"by":"x"}`, 1)+","+sk("b", "KB", ""), "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"},"vonAgent":true}]`),
		"vertraulich ein":                      kopie827(strings.Replace(a, `"labels":[]`, `"labels":[],"confidential":true`, 1)+","+sk("b", "KB", ""), "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"umsortiert und in Backlog/Ready":      kopie827(sk("b", "KB", ""), strings.Replace(a, "col-backlog", "col-ready", 1), "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"Kommentar außen bearbeitet und neuer": kopie827(strings.Replace(a, `"text":"alt","created":"c","herkunft":"aussen"}`, `"text":"neu","created":"c","herkunft":"aussen","edited":"e"},{"id":"k2","author":"B","text":"x","created":"d"}`, 1)+","+sk("b", "KB", ""), "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"neue Karte und neuer Termin":          kopie827(a+","+sk("b", "KB", "")+`,{"id":"n1","title":"N","columnId":"col-ready"}`, "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}},{"id":"t-n","titel":"N","datum":"2026-10-21"}]`),
	}
	for name, n := range faelle {
		if got := verstoesse827(t, v, n, nil); len(got) != 0 {
			t.Errorf("%s: erlaubt, bekam %v", name, got)
		}
	}
}

func TestSchrankeRueckweg827_Gesperrt(t *testing.T) {
	a := sk("a", "KA", `"comments":[{"id":"ki","author":"Innen","text":"i","created":"c","herkunft":"innen"},{"id":"ka","author":"Anna","text":"a","created":"c","herkunft":"aussen"}]`)
	b := sk("b", "KB", `"confidential":true`)
	termin := `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`
	v := kopie827(a+","+b, "", "", termin)
	faelle := map[string]struct {
		n   []byte
		art string
		id  string
	}{
		"gelöscht":               {kopie827(b, "", "", termin), "karte", "a"},
		"in unmarkierte Lane":    {kopie827(b, "", a, termin), "karte", "a"},
		"in Spalte ohne Marke":   {kopie827(strings.Replace(a, "col-backlog", "x", 1)+","+b, "", "", termin), "karte", "a"},
		"vertraulich aus":        {kopie827(a+","+strings.Replace(b, `"confidential":true`, `"confidential":false`, 1), "", "", termin), "karte", "b"},
		"Marke geändert":         {kopie827(strings.Replace(a, `"id":"KA"`, `"id":"KZ"`, 1)+","+b, "", "", termin), "karte", "a"},
		"Verweis gesetzt":        {kopie827(strings.Replace(a, `"labels":[]`, `"labels":[],"links":[{"type":"related","targetId":"b"}]`, 1)+","+b, "", "", termin), "karte", "a"},
		"Agent-Kontext":          {kopie827(strings.Replace(a, `"labels":[]`, `"labels":[],"agentContext":"x"`, 1)+","+b, "", "", termin), "karte", "a"},
		"innerer Kommentar":      {kopie827(strings.Replace(a, `"text":"i"`, `"text":"geändert"`, 1)+","+b, "", "", termin), "karte", "a"},
		"Kommentar gelöscht":     {kopie827(strings.Replace(a, `,{"id":"ka","author":"Anna","text":"a","created":"c","herkunft":"aussen"}`, "", 1)+","+b, "", "", termin), "karte", "a"},
		"Autor eines Kommentars": {kopie827(strings.Replace(a, `"author":"Anna"`, `"author":"Mallory"`, 1)+","+b, "", "", termin), "karte", "a"},
		"neue Karte ohne Spalte": {kopie827(a+","+b+`,{"id":"n1","title":"N","columnId":"x"}`, "", "", termin), "karte", "n1"},
		"Termin gelöscht":        {kopie827(a+","+b, "", "", "[]"), "termin", "t-a"},
		"Termin-Herkunft":        {kopie827(a+","+b, "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"},"erstelltVon":"x"}]`), "termin", "t-a"},
		"Ursprung gesetzt":       {kopie827(strings.Replace(a, `"labels":[]`, `"labels":[],"boardpaarUrsprung":"x"`, 1)+","+b, "", "", termin), "karte", "a"},
	}
	for name, f := range faelle {
		got := verstoesse827(t, v, f.n, nil)
		gefunden := false
		for _, o := range got {
			if o.Art == f.art && o.ID == f.id {
				gefunden = true
			}
		}
		if !gefunden {
			t.Errorf("%s: Verstoß %s %s erwartet, bekam %v", name, f.art, f.id, got)
		}
	}
	// Lanes und Spalten bleiben gesperrt wie in 2b.
	n := []byte(strings.Replace(string(v), `"title":"L"`, `"title":"L2"`, 1))
	if got := verstoesse827(t, v, n, nil); len(got) != 1 || got[0].Art != "lane" {
		t.Errorf("Lane umbenannt: %v", got)
	}
}

func TestSchrankeRueckweg827_Wartend(t *testing.T) {
	w := `{"id":"w1","title":"W","description":"","columnId":"col-backlog","comments":[]}`
	v := kopie827(w, "", "", "")
	wartend := map[string]bool{"w1": true}
	if got := verstoesse827(t, v, kopie827(strings.Replace(w, `"title":"W"`, `"title":"W2"`, 1), "", "", ""), wartend); len(got) != 0 {
		t.Errorf("wartende Karte bearbeiten erlaubt: %v", got)
	}
	if got := verstoesse827(t, v, kopie827("", "", w, ""), wartend); len(got) != 1 {
		t.Errorf("wartende Karte in unmarkierte Lane gesperrt: %v", got)
	}
	if got := verstoesse827(t, v, kopie827("", "", "", ""), wartend); len(got) != 1 {
		t.Errorf("wartende Karte löschen gesperrt: %v", got)
	}
	// Schutzmodus: jede Änderung an einer wartenden Karte ist ein Verstoß.
	if got, _ := WartendVerstoesse(v, kopie827(strings.Replace(w, `"title":"W"`, `"title":"W2"`, 1), "", "", ""), wartend); len(got) != 1 {
		t.Errorf("Schutzmodus: %v", got)
	}
	if got, _ := WartendVerstoesse(v, v, wartend); len(got) != 0 {
		t.Errorf("unverändert: %v", got)
	}
	if got, _ := WartendVerstoesse(v, kopie827("", "", "", ""), nil); len(got) != 0 {
		t.Errorf("ohne wartende nichts: %v", got)
	}
}

func TestSchrankeRueckweg827_AltkarteFrei(t *testing.T) {
	alt := `{"id":"alt-1","title":"Alt","description":"","columnId":"col-backlog","comments":[]}`
	v := kopie827(alt, "", "", "")
	for name, n := range map[string][]byte{
		"geändert": kopie827(strings.Replace(alt, `"Alt"`, `"Alt2"`, 1), "", "", ""),
		"gelöscht": kopie827("", "", "", ""),
	} {
		if got := verstoesse827(t, v, n, nil); len(got) != 0 {
			t.Errorf("%s: Altkarte ohne Marke gehört außen, bekam %v", name, got)
		}
	}
}

// Sitzungs-Kritik A-5: Marken setzt nur der Eingang.
func TestSchrankeRueckweg827_NeueOderDoppelteMarke(t *testing.T) {
	a := sk("a", "KA", "")
	v := kopie827(a, "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`)
	faelle := map[string][]byte{
		"neue Karte mit Marke":   kopie827(a+","+sk("f", "KF", ""), "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"Marke kopiert davor":    kopie827(sk("f", "KA", "")+","+a, "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`),
		"neuer Termin mit Marke": kopie827(a, "", "", `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}},{"id":"t-f","titel":"F","datum":"2026-10-21","spiegel":{"id":"KF"}}]`),
	}
	for name, n := range faelle {
		if got := verstoesse827(t, v, n, nil); len(got) == 0 {
			t.Errorf("%s: Verstoß erwartet", name)
		}
	}
}

// Sitzungs-Kritik B-2: Schutzmodus für wartende Karten auch bei Lane, Spalte
// und für wartende Termine.
func TestSchrankeRueckweg827_WartendSchutzOrtUndTermin(t *testing.T) {
	w := `{"id":"w1","title":"W","description":"","columnId":"col-backlog","comments":[]}`
	tw := `[{"id":"tw","titel":"T","datum":"2026-10-20"}]`
	v := kopie827(w, "", "", tw)
	wartend := map[string]bool{"w1": true, "tw": true}
	for name, n := range map[string][]byte{
		"andere Spalte":   kopie827(strings.Replace(w, "col-backlog", "col-ready", 1), "", "", tw),
		"andere Lane":     kopie827("", w, "", tw),
		"Termin geändert": kopie827(w, "", "", `[{"id":"tw","titel":"T2","datum":"2026-10-20"}]`),
	} {
		if got, _ := WartendVerstoesse(v, n, wartend); len(got) != 1 {
			t.Errorf("%s: ein Verstoß erwartet, bekam %v", name, got)
		}
	}
}

// Verify V-2: zwei Termine mit derselben Marke.
func TestSchrankeRueckweg827_DoppelteTerminMarke(t *testing.T) {
	a := sk("a", "KA", "")
	tm := `{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}`
	v := kopie827(a, "", "", "["+tm+"]")
	n := kopie827(a, "", "", "["+tm+`,{"id":"t-b","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`)
	if got := verstoesse827(t, v, n, nil); len(got) == 0 {
		t.Fatal("doppelte Termin-Marke muss ein Verstoß sein")
	}
}

// Karte #863 (A-3): Zwei Karten mit derselben Marke — b.karten (nach Marke) hält nur eine; beide Verzeichnisse
// müssen trotzdem gesperrt sein (nicht fail-open). Eigene Karte ohne Marke außerhalb markierter Lanes bleibt frei.
func TestKartenVerzeichnisGesperrt863_DoppelteMarke(t *testing.T) {
	st := kopie827(sk("a", "KA", "")+","+sk("b", "KA", ""), "", sk("e", "", ""), "")
	for _, id := range []string{"a", "b"} {
		if g, unl := KartenVerzeichnisGesperrt(st, nil, id); !g || unl {
			t.Errorf("Karte %s bei doppelter Marke: gesperrt=%v unlesbar=%v, erwartet gesperrt", id, g, unl)
		}
	}
	if g, _ := KartenVerzeichnisGesperrt(st, nil, "unbekannt"); g {
		t.Error("unbekannte Karte darf nicht gesperrt sein")
	}
}
