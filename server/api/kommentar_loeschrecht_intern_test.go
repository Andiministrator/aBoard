package api

// ADR-0136 (Karte #693, F-1611): vergleicheKommentare direkt — die Fälle, die
// über HTTP schwer herzustellen sind (unlesbarer Stand, Agenten, kaputter
// Altbestand).

import "testing"

func standMit(kommentare string) []byte {
	return []byte(`{"board":{"id":"b1","sprints":[{"id":"s1","cards":[{"id":"k1","title":"T","displayId":1,"comments":` + kommentare + `}]}]}}`)
}

const fremderKom = `{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":"hallo"}`

func TestVergleicheKommentare_UnlesbarIstAbgelehnt(t *testing.T) {
	editor := kommentarSchreiber{email: "bert@firma.de"}
	owner := kommentarSchreiber{email: "olga@firma.de", darfFremde: true}
	gut := standMit(`[` + fremderKom + `]`)
	kaputt := []byte(`{"board":{"id":"b1","sprints":"x"}}`)
	if v, _ := vergleicheKommentare(gut, kaputt, editor, "b1"); len(v) != 1 || v[0].art != "stand-unlesbar" {
		t.Errorf("nachher unlesbar: %v", v)
	}
	if v, _ := vergleicheKommentare(gut, kaputt, owner, "b1"); len(v) != 1 || v[0].art != "stand-unlesbar" {
		t.Errorf("nachher unlesbar (Owner): %v", v)
	}
	if v, _ := vergleicheKommentare(kaputt, gut, editor, "b1"); len(v) != 1 || v[0].art != "stand-unlesbar" {
		t.Errorf("vorher unlesbar, editor: %v", v)
	}
	// Owner/Admin darf einen kaputten Stand ersetzen — sonst bliebe das
	// Board für immer gesperrt.
	if v, _ := vergleicheKommentare(kaputt, gut, owner, "b1"); len(v) != 0 {
		t.Errorf("vorher unlesbar, Owner: %v", v)
	}
}

func TestVergleicheKommentare_KaputterAltbestandBleibtSchreibbar(t *testing.T) {
	editor := kommentarSchreiber{email: "bert@firma.de"}
	kaputt := standMit(`["x"]`)
	// Unverändert: andere Felder der Karte dürfen sich ändern.
	nachher := []byte(`{"board":{"id":"b1","sprints":[{"id":"s1","cards":[{"id":"k1","title":"neu","displayId":1,"comments":["x"]}]}]}}`)
	if v, _ := vergleicheKommentare(kaputt, nachher, editor, "b1"); len(v) != 0 {
		t.Errorf("unveränderter kaputter Bestand: %v", v)
	}
	if v, _ := vergleicheKommentare(kaputt, standMit(`[]`), editor, "b1"); len(v) != 1 || v[0].art != "stand-unlesbar" {
		t.Errorf("kaputter Bestand geändert durch editor: %v", v)
	}
}

func TestVergleicheKommentare_Agent(t *testing.T) {
	agent := kommentarSchreiber{agentAkteur: "agent:bot"}
	eigener := `{"id":"g1","author":"agent:bot","text":"a"}`
	vorher := standMit(`[` + fremderKom + `,` + eigener + `]`)
	// Eigenen bearbeiten: ok.
	if v, _ := vergleicheKommentare(vorher, standMit(`[`+fremderKom+`,{"id":"g1","author":"agent:bot","text":"b"}]`), agent, "b1"); len(v) != 0 {
		t.Errorf("Agent bearbeitet eigenen: %v", v)
	}
	// Fremden entfernen oder bearbeiten: nie.
	if v, _ := vergleicheKommentare(vorher, standMit(`[`+eigener+`]`), agent, "b1"); len(v) != 1 {
		t.Errorf("Agent entfernt fremden: %v", v)
	}
	// Neuer Kommentar unter Menschennamen: abgelehnt.
	if v, _ := vergleicheKommentare(vorher, standMit(`[`+fremderKom+`,`+eigener+`,{"id":"g2","author":"agent:bot","authorEmail":"anna@firma.de","text":"x"}]`), agent, "b1"); len(v) != 1 {
		t.Errorf("Agent legt Kommentar mit fremdem Stempel an: %v", v)
	}
	// Legacy-Bypass ohne Identität: keine Prüfung.
	if v, _ := vergleicheKommentare(vorher, standMit(`[]`), kommentarSchreiber{}, "b1"); len(v) != 0 {
		t.Errorf("Legacy-Bypass: %v", v)
	}
}

func TestVergleicheKommentare_KopieUndNeu(t *testing.T) {
	bert := kommentarSchreiber{email: "bert@firma.de"}
	vorher := standMit(`[` + fremderKom + `]`)
	// Unveränderte Kopie eines fremden Kommentars an eine neue Karte: ok.
	kopie := []byte(`{"board":{"id":"b1","sprints":[{"id":"s1","cards":[{"id":"k1","comments":[` + fremderKom + `]},{"id":"k2","comments":[` + fremderKom + `]}]}]}}`)
	if v, _ := vergleicheKommentare(vorher, kopie, bert, "b1"); len(v) != 0 {
		t.Errorf("Kopie: %v", v)
	}
	// Veränderte „Kopie“ unter Annas Namen: abgelehnt.
	falsch := []byte(`{"board":{"id":"b1","sprints":[{"id":"s1","cards":[{"id":"k1","comments":[` + fremderKom + `]},{"id":"k2","comments":[{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":"untergeschoben"}]}]}]}}`)
	if v, _ := vergleicheKommentare(vorher, falsch, bert, "b1"); len(v) != 1 || v[0].art != "fremder-kommentar" {
		t.Errorf("untergeschobener Text: %v", v)
	}
}

func TestIstAlteKIAntwortVon(t *testing.T) {
	for autor, want := range map[string]bool{
		"🤖 anthropic/claude (im Auftrag von anna@firma.de)": true,
		"🤖 anthropic/claude (im Auftrag von ANNA@firma.de)": true,
		"🤖 anthropic/claude (im Auftrag von bert@firma.de)": false,
		"anthropic/claude (im Auftrag von anna@firma.de)":   false,
		"🤖 anthropic/claude":                                false,
	} {
		if got := istAlteKIAntwortVon(autor, "anna@firma.de"); got != want {
			t.Errorf("%q: %v, erwartet %v", autor, got, want)
		}
	}
}

// F-1629: Ergänzt ein Client einem fremden Kommentar leere Felder
// (attachments: [], null), ist das keine Änderung. Ein nicht leeres Feld
// bleibt eine.
func TestVergleicheKommentare_LeereFelderSindKeineAenderung(t *testing.T) {
	editor := kommentarSchreiber{email: "bert@firma.de"}
	vorher := standMit(`[` + fremderKom + `]`)
	for _, f := range []struct {
		nachher string
		erlaubt bool
	}{
		{`{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":"hallo","attachments":[]}`, true},
		{`{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":"hallo","edited":null,"meta":{}}`, true},
		{`{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":"hallo","attachments":[{"id":"x"}]}`, false},
		{`{"id":"c1","author":"Anna","authorEmail":"anna@firma.de","text":""}`, false},
	} {
		v, _ := vergleicheKommentare(vorher, standMit(`[`+f.nachher+`]`), editor, "b1")
		if (len(v) == 0) != f.erlaubt {
			t.Errorf("%s: %v, erlaubt erwartet %v", f.nachher, v, f.erlaubt)
		}
	}
}
