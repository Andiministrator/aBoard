package api

// schleuse_kommentar_intern_test.go — Der Antwortkommentar „extern“ der
// Ausgangsschleuse am Ergebnis (Karte #686, ADR-0134 §3.6): unveränderlich
// für alle, von Clients nicht anlegbar, löschbar nur mit Verlaufseintrag.

import (
	"testing"
	"time"
)

const externKom = `{"id":"c9","author":"🌐 anthropic/m (extern, im Auftrag von anna@firma.de)","authorEmail":"anna@firma.de","text":"Antwort","kind":"extern","schleuse":{"auftragId":"a1"}}`

func TestExternKommentar_Unveraenderlich(t *testing.T) {
	vorher := standMit(`[` + externKom + `]`)
	anna := kommentarSchreiber{email: "anna@firma.de"}
	owner := kommentarSchreiber{email: "olga@firma.de", darfFremde: true}
	for name, nachher := range map[string]string{
		"Text":          `{"id":"c9","author":"🌐 anthropic/m (extern, im Auftrag von anna@firma.de)","authorEmail":"anna@firma.de","text":"Geändert","kind":"extern","schleuse":{"auftragId":"a1"}}`,
		"Art entfernt":  `{"id":"c9","author":"🌐 anthropic/m (extern, im Auftrag von anna@firma.de)","authorEmail":"anna@firma.de","text":"Antwort","schleuse":{"auftragId":"a1"}}`,
		"Beleg ersetzt": `{"id":"c9","author":"🌐 anthropic/m (extern, im Auftrag von anna@firma.de)","authorEmail":"anna@firma.de","text":"Antwort","kind":"extern","schleuse":{"auftragId":"a2"}}`,
	} {
		for wer, s := range map[string]kommentarSchreiber{"Autorin": anna, "Owner": owner} {
			v, _ := vergleicheKommentare(vorher, standMit(`[`+nachher+`]`), s, "b1")
			if len(v) != 1 || v[0].art != "extern-kommentar" {
				t.Errorf("%s durch %s: %v", name, wer, v)
			}
		}
	}
	// Unverändert zurückgeschrieben (Browser speichert die ganze Karte) ist erlaubt.
	if v, _ := vergleicheKommentare(vorher, vorher, anna, "b1"); len(v) != 0 {
		t.Errorf("unverändert: %v", v)
	}
}

func TestExternKommentar_NichtAnlegbar(t *testing.T) {
	anna := kommentarSchreiber{email: "anna@firma.de"}
	owner := kommentarSchreiber{email: "olga@firma.de", darfFremde: true}
	for wer, s := range map[string]kommentarSchreiber{"Autorin": anna, "Owner": owner} {
		v, _ := vergleicheKommentare(standMit(`[]`), standMit(`[`+externKom+`]`), s, "b1")
		if len(v) != 1 || v[0].art != "extern-kommentar" {
			t.Errorf("%s legt externen Kommentar an: %v", wer, v)
		}
	}
	// Ein gewöhnlicher Kommentar zu einem bestehenden „extern“ wandeln geht auch nicht.
	normal := `{"id":"c9","author":"Anna","authorEmail":"anna@firma.de","text":"Antwort"}`
	v, _ := vergleicheKommentare(standMit(`[`+normal+`]`), standMit(`[`+externKom+`]`), anna, "b1")
	if len(v) != 1 || v[0].art != "extern-kommentar" {
		t.Errorf("zu extern gewandelt: %v", v)
	}
	// Schatten-Schreibweise des Art-Schlüssels fällt auf.
	schatten := `{"id":"c8","author":"Anna","authorEmail":"anna@firma.de","text":"x","Kind":"extern"}`
	v, _ = vergleicheKommentare(standMit(`[]`), standMit(`[`+schatten+`]`), anna, "b1")
	if len(v) != 1 || v[0].art != "schattenschluessel" {
		t.Errorf("Schattenschlüssel: %v", v)
	}
}

func TestExternKommentar_LoeschenMitVerlauf(t *testing.T) {
	vorher := standMit(`[` + externKom + `]`)
	nachher := standMit(`[]`)
	// Die Autorin darf ihren Antwortkommentar löschen — mit Verlaufseintrag,
	// ohne Benachrichtigung an sich selbst.
	v, g := vergleicheKommentare(vorher, nachher, kommentarSchreiber{email: "anna@firma.de"}, "b1")
	if len(v) != 0 || len(g) != 1 || !g[0].Extern || !g[0].Eigen {
		t.Fatalf("Autorin löscht: %v %+v", v, g)
	}
	if vl := kommentarVerlauf(g, "op1", time.Now()); vl["op1"][0]["item"].(map[string]any)["action"] != "schleuse-kommentar-geloescht" {
		t.Errorf("Verlaufsaktion: %v", vl)
	}
	// Owner: wie jeder fremde Kommentar, aber mit der Schleusen-Aktion.
	v, g = vergleicheKommentare(vorher, nachher, kommentarSchreiber{email: "olga@firma.de", darfFremde: true}, "b1")
	if len(v) != 0 || len(g) != 1 || !g[0].Extern || g[0].Eigen {
		t.Errorf("Owner löscht: %v %+v", v, g)
	}
	// Andere Editoren nicht.
	if v, _ := vergleicheKommentare(vorher, nachher, kommentarSchreiber{email: "bert@firma.de"}, "b1"); len(v) != 1 {
		t.Errorf("fremder Editor löscht: %v", v)
	}
}

// Kritiker KS3-3: Eine Kopie an eine ANDERE Karte ist keine erlaubte Kopie —
// auch nicht für die Autorin; an derselben Karte bleibt sie unverändert.
func TestExternKommentar_KeineKopieAnAndereKarte(t *testing.T) {
	zwei := func(k1, k2 string) []byte {
		return []byte(`{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","title":"T","displayId":1,"comments":` + k1 + `},{"id":"k2","title":"U","displayId":2,"comments":` + k2 + `}]}]}}`)
	}
	vorher := zwei(`[`+externKom+`]`, `[]`)
	for wer, s := range map[string]kommentarSchreiber{"Editor": {email: "bert@firma.de"}, "Autorin": {email: "anna@firma.de"}, "Owner": {email: "olga@firma.de", darfFremde: true}} {
		v, _ := vergleicheKommentare(vorher, zwei(`[`+externKom+`]`, `[`+externKom+`]`), s, "b1")
		if len(v) != 1 || v[0].art != "extern-kommentar" || v[0].cardID != "k2" {
			t.Errorf("%s kopiert an andere Karte: %v", wer, v)
		}
		v, _ = vergleicheKommentare(vorher, zwei(`[`+externKom+`,`+externKom+`]`, `[]`), s, "b1")
		if len(v) != 1 || v[0].art != "extern-kommentar" {
			t.Errorf("%s verdoppelt an derselben Karte: %v", wer, v)
		}
	}
}

// Kritiker KS3-4: Der Verlaufseintrag zum Löschen trägt keinen Textanfang.
func TestExternKommentar_VerlaufOhneText(t *testing.T) {
	_, g := vergleicheKommentare(standMit(`[`+externKom+`]`), standMit(`[]`), kommentarSchreiber{email: "anna@firma.de"}, "b1")
	item := kommentarVerlauf(g, "op1", time.Now())["op1"][0]["item"].(map[string]any)
	if item["to"] != "" {
		t.Errorf("Textanfang im Verlauf: %v", item["to"])
	}
}
