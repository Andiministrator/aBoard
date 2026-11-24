package schatten

// Sprint-94-Kritik K-2/K-4 (ADR-0141 §7): Neue Kommentare und neue Karten
// außen müssen den Rückweg überstehen — eine ID außerhalb von
// GueltigeKennung oder Anhänge an einem neuen Kommentar verschwänden nach dem
// Bestätigen außen still.

import (
	"strings"
	"testing"
)

func TestSchrankeRueckwegKritik94_NeueKommentareUndKarten(t *testing.T) {
	a := sk("a", "KA", `"comments":[{"id":"ka","author":"Anna","text":"a","created":"c","herkunft":"aussen"}]`)
	termin := `[{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}]`
	v := kopie827(a, "", "", termin)
	neuerKommentar := func(c string) []byte {
		return kopie827(strings.Replace(a, `"herkunft":"aussen"}`, `"herkunft":"aussen"},`+c, 1), "", "", termin)
	}
	gesperrt := map[string]struct {
		n  []byte
		id string
	}{
		"neuer Kommentar mit Anhang":             {neuerKommentar(`{"id":"k2","author":"B","text":"x","created":"d","attachments":["anh-1"]}`), "a"},
		"neuer Kommentar mit ungültiger ID":      {neuerKommentar(`{"id":"k.2","author":"B","text":"x","created":"d"}`), "a"},
		"neue Karte mit ungültiger ID":           {kopie827(a+`,{"id":"n.1","title":"N","columnId":"col-ready"}`, "", "", termin), "n.1"},
		"neue Karte mit Anhang am Kommentar":     {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","comments":[{"id":"c1","text":"x","attachments":["anh"]}]}`, "", "", termin), "n1"},
		"neue Karte mit ungültiger Kommentar-ID": {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","comments":[{"id":"c 1","text":"x"}]}`, "", "", termin), "n1"},
		// Verify F-V4: Anhänge und Verweise trägt die Journal-Form nicht.
		"neue Karte mit Anhang":  {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","attachments":[{"id":"x"}]}`, "", "", termin), "n1"},
		"neue Karte mit Verweis": {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","links":[{"type":"related","targetId":"a"}]}`, "", "", termin), "n1"},
		// Verify 2 F-V6: keine lesbare Liste gilt vorsichtshalber als belegt.
		"neue Karte, links als Text":     {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","links":"x"}`, "", "", termin), "n1"},
		"neue Karte, attachments Objekt": {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","attachments":{}}`, "", "", termin), "n1"},
	}
	for name, f := range gesperrt {
		got := verstoesse827(t, v, f.n, nil)
		gefunden := false
		for _, o := range got {
			if o.Art == "karte" && o.ID == f.id {
				gefunden = true
			}
		}
		if !gefunden {
			t.Errorf("%s: Verstoß karte %s erwartet, bekam %v", name, f.id, got)
		}
	}
	erlaubt := map[string][]byte{
		"neuer Kommentar ohne Anhang":       neuerKommentar(`{"id":"k2","author":"B","text":"x","created":"d","attachments":[]}`),
		"neuer Kommentar, attachments null": neuerKommentar(`{"id":"k2","author":"B","text":"x","created":"d","attachments":null}`),
		"neue Karte mit Kommentar":          kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","comments":[{"id":"c1","text":"x"}]}`, "", "", termin),
		"neue Karte, leere Listen":          kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","attachments":[],"links":[]}`, "", "", termin),
		"neue Karte, links null":            kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","links":null}`, "", "", termin),
	}
	for name, n := range erlaubt {
		if got := verstoesse827(t, v, n, nil); len(got) != 0 {
			t.Errorf("%s: erlaubt, bekam %v", name, got)
		}
	}
	// Ein schon bestehender Kommentar mit Anhang bleibt unberührt erlaubt
	// (die Regel gilt nur für neue).
	mitAnhang := sk("a", "KA", `"comments":[{"id":"ka","author":"Anna","text":"a","created":"c","herkunft":"aussen","attachments":["alt"]}]`)
	v2 := kopie827(mitAnhang, "", "", termin)
	if got := verstoesse827(t, v2, kopie827(strings.Replace(mitAnhang, `"title":"T"`, `"title":"T2"`, 1), "", "", termin), nil); len(got) != 0 {
		t.Errorf("bestehender Kommentar mit Anhang: %v", got)
	}
}

// F-2267 (Sitzungs-Kritik N-1): Ein neuer Termin außen mit ungültiger ID ging
// durch die Schranke, wurde als `angelegt` journalisiert und hielt am
// Anwenden-Weg das Skript an (422). F-2269 (N-2): Ein Kommentar ohne
// Zeichenketten-ID übersprang die Prüfung und verschwand beim nächsten Hinweg.
func TestSchrankeRueckwegSitzungskritik94_TerminIDsUndKommentareOhneID(t *testing.T) {
	a := sk("a", "KA", `"comments":[{"id":"ka","author":"Anna","text":"a","created":"c","herkunft":"aussen"}]`)
	termin := `{"id":"t-a","titel":"T","datum":"2026-10-20","spiegel":{"id":"KT"}}`
	v := kopie827(a, "", "", "["+termin+"]")
	mitTermin := func(neu string) []byte { return kopie827(a, "", "", "["+termin+","+neu+"]") }
	neuerKommentar := func(c string) []byte {
		return kopie827(strings.Replace(a, `"herkunft":"aussen"}`, `"herkunft":"aussen"},`+c, 1), "", "", "["+termin+"]")
	}
	enthaelt := func(got []SpiegelOrt, art, id string) bool {
		for _, o := range got {
			if o.Art == art && o.ID == id {
				return true
			}
		}
		return false
	}
	for name, f := range map[string]struct {
		n       []byte
		art, id string
	}{
		"neuer Termin mit Punkt und Leerzeichen": {mitTermin(`{"id":"t.1 x","titel":"N","datum":"2026-10-21"}`), "termin", "t.1 x"},
		"neuer Termin mit 65 Zeichen":            {mitTermin(`{"id":"` + strings.Repeat("t", 65) + `","titel":"N","datum":"2026-10-21"}`), "termin", strings.Repeat("t", 65)},
		"neuer Kommentar ohne id":                {neuerKommentar(`{"author":"B","text":"x","created":"d"}`), "karte", "a"},
		"neuer Kommentar mit Zahl als id":        {neuerKommentar(`{"id":7,"author":"B","text":"x","created":"d"}`), "karte", "a"},
		"neuer Kommentar mit leerer id":          {neuerKommentar(`{"id":"","author":"B","text":"x","created":"d"}`), "karte", "a"},
		"neue Karte mit Kommentar ohne id":       {kopie827(a+`,{"id":"n1","title":"N","columnId":"col-ready","comments":[{"text":"x"}]}`, "", "", "["+termin+"]"), "karte", "n1"},
	} {
		if got := verstoesse827(t, v, f.n, nil); !enthaelt(got, f.art, f.id) {
			t.Errorf("%s: Verstoß %s %s erwartet, bekam %v", name, f.art, f.id, got)
		}
	}
	if got := verstoesse827(t, v, mitTermin(`{"id":"t-neu_1","titel":"N","datum":"2026-10-21"}`), nil); len(got) != 0 {
		t.Errorf("neuer Termin mit gültiger ID: erlaubt, bekam %v", got)
	}
	// Bestand bleibt frei: ein Termin mit ungültiger ID, der schon vorher
	// außen stand (eigener Termin, nie journalisiert), und ein schon
	// vorhandener Kommentar ohne id, solange er unverändert bleibt.
	alt := `{"id":"alt x","titel":"Eigen","datum":"2026-10-22"}`
	vAlt := kopie827(a, "", "", "["+termin+","+alt+"]")
	nAlt := kopie827(strings.Replace(a, `"title":"T"`, `"title":"T2"`, 1), "", "", "["+termin+","+alt+"]")
	if got := verstoesse827(t, vAlt, nAlt, nil); len(got) != 0 {
		t.Errorf("bestehender Termin mit ungültiger ID: frei, bekam %v", got)
	}
	ohneID := sk("a", "KA", `"comments":[{"author":"Alt","text":"a","created":"c"}]`)
	vO := kopie827(ohneID, "", "", "["+termin+"]")
	nO := kopie827(strings.Replace(ohneID, `"title":"T"`, `"title":"T2"`, 1), "", "", "["+termin+"]")
	if got := verstoesse827(t, vO, nO, nil); len(got) != 0 {
		t.Errorf("bestehender Kommentar ohne id, unverändert: frei, bekam %v", got)
	}
}
