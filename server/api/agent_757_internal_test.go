package api

// Karte #757: Einzelprüfungen des Bestätigungscodes, die über HTTP nicht
// erreichbar sind — die Person beim selben Token (delegierend: Person aus dem
// JWT) und die Stand-Kennung.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestLoeschCode757_BindungAnPerson(t *testing.T) {
	geheimnis := []byte(strings.Repeat("g", 32))
	jetzt := time.Now()
	z := &loeschZiel{art: "karte", objektID: "k1", stand: standKennung(json.RawMessage(`{"id":"k1"}`))}
	anna := &AgentPrincipal{TokenID: "t1", OnBehalfOf: "anna@firma.de", Delegiert: true}
	code, _, err := bildeLoeschCode(geheimnis, anna, "b1", z, jetzt)
	if err != nil {
		t.Fatal(err)
	}
	if _, f := pruefeLoeschCode(geheimnis, code, anna, "b1", z, jetzt); f != nil {
		t.Fatalf("eigener Code: %v", f.meldung)
	}
	// Groß-/Kleinschreibung der Adresse ist dieselbe Person.
	annaGross := *anna
	annaGross.OnBehalfOf = "Anna@Firma.de"
	if _, f := pruefeLoeschCode(geheimnis, code, &annaGross, "b1", z, jetzt); f != nil {
		t.Errorf("gleiche Person, andere Schreibweise: %v", f.meldung)
	}
	for name, p := range map[string]*AgentPrincipal{
		"fremde Person, gleiches Token": {TokenID: "t1", OnBehalfOf: "bert@firma.de", Delegiert: true},
		"Projekt-Token ohne Person":     {TokenID: "t1"},
		"anderes Token":                 {TokenID: "t2", OnBehalfOf: "anna@firma.de", Delegiert: true},
	} {
		if _, f := pruefeLoeschCode(geheimnis, code, p, "b1", z, jetzt); f == nil || f.status != http.StatusForbidden {
			t.Errorf("%s: %+v", name, f)
		}
	}
	if _, f := pruefeLoeschCode([]byte(strings.Repeat("h", 32)), code, anna, "b1", z, jetzt); f == nil || f.status != http.StatusForbidden {
		t.Errorf("anderes Geheimnis: %+v", f)
	}
	// Ablauf genau an der Grenze: ungültig.
	if _, f := pruefeLoeschCode(geheimnis, code, anna, "b1", z, jetzt.Add(loeschCodeGueltig)); f == nil || f.status != http.StatusGone {
		t.Errorf("an der Grenze: %+v", f)
	}
}

func TestStandKennung757(t *testing.T) {
	a := standKennung(json.RawMessage(`{"a":1,"b":{"c":[1,2]}}`))
	if b := standKennung(json.RawMessage(`{"b":{"c":[1,2]},"a":1}`)); a != b {
		t.Error("Schlüsselreihenfolge ändert die Kennung")
	}
	for _, anders := range []string{`{"a":1,"b":{"c":[2,1]}}`, `{"a":1.0,"b":{"c":[1,2]}}`, `{"a":1,"b":{"c":[1,2]},"d":null}`} {
		if standKennung(json.RawMessage(anders)) == a {
			t.Errorf("%s ergibt dieselbe Kennung", anders)
		}
	}
}

func TestAgentRohSperre757(t *testing.T) {
	for feld, gesperrt := range map[string]bool{"title": true, "columnDefs": true, "background": true, "templates": true, "attachments": false} {
		grund := agentRohSperre("board.field", json.RawMessage(`{"field":"`+feld+`","value":null}`))
		if (grund != "") != gesperrt {
			t.Errorf("%s: %q", feld, grund)
		}
	}
	if agentRohSperre("card.field", json.RawMessage(`{"field":"title","value":"x","cardId":"k1"}`)) != "" {
		t.Error("card.field title gesperrt")
	}
}

// Die Positivliste allein (ohne Handler): Im Matrix-Test verdeckt die
// Owner-Prüfung des Handlers eine fehlende Bedingung hier (Gegenprobe 757,
// Lehre aus #753 — jede Hürde braucht ihren eigenen Test). Geprüft wird mit
// einer Person, die owner sein KÖNNTE: nur die Token-Art entscheidet.
func TestWerkzeugDarf757_StrukturNurMitPerson(t *testing.T) {
	segs := func(pfad string) []string {
		s, ok := agentSegmente(pfad)
		if !ok {
			t.Fatal(pfad)
		}
		return s
	}
	for _, weg := range []struct{ methode, pfad string }{
		{http.MethodPost, "/werkzeuge/boards/b1/aendern"},
		{http.MethodGet, "/werkzeuge/boards/b1/spalten/col-leer/loeschen"},
		{http.MethodPost, "/werkzeuge/boards/b1/spalten/col-leer/loeschen"},
	} {
		for name, p := range map[string]*AgentPrincipal{
			"Projekt-Token":     {TokenID: "t", BoardID: "b1", Role: "editor", tokenRolle: "editor"},
			"delegierend owner": {TokenID: "t", BoardID: "b1", Role: "editor", tokenRolle: "editor", OnBehalfOf: "anna@firma.de", Delegiert: true},
			"Person viewer":     {TokenID: "t", BoardID: "b1", Role: "viewer", tokenRolle: "viewer", OnBehalfOf: "anna@firma.de"},
		} {
			if werkzeugDarf(p, weg.methode, segs(weg.pfad)) {
				t.Errorf("%s %s: %s darf", weg.methode, weg.pfad, name)
			}
		}
		person := &AgentPrincipal{TokenID: "t", BoardID: "b1", Role: "editor", tokenRolle: "editor", OnBehalfOf: "anna@firma.de"}
		if !werkzeugDarf(person, weg.methode, segs(weg.pfad)) {
			t.Errorf("%s %s: Personen-Token editor darf nicht", weg.methode, weg.pfad)
		}
	}
	// Die übrigen Lösch-Paare: Projekt-Token ja, delegierend nie, viewer nie.
	for _, art := range []string{"karten/1", "swimlanes/s1", "termine/t-1", "anhaenge/a-1"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			s := segs("/werkzeuge/boards/b1/" + art + "/loeschen")
			if !werkzeugDarf(&AgentPrincipal{BoardID: "b1", Role: "editor", tokenRolle: "editor"}, m, s) ||
				werkzeugDarf(&AgentPrincipal{BoardID: "b1", Role: "editor", tokenRolle: "editor", OnBehalfOf: "a@b", Delegiert: true}, m, s) ||
				werkzeugDarf(&AgentPrincipal{BoardID: "b1", Role: "viewer", tokenRolle: "viewer"}, m, s) {
				t.Errorf("%s %s", m, art)
			}
		}
	}
}

// Verify #757 V-2/V-3: Freigegeben wird nur nach einer Ablehnung vor dem COMMIT.
func TestLoeschCodeFreigebbar757(t *testing.T) {
	if loeschCodeFreigebbar(nil) {
		t.Error("ohne Antwort (Quittung unlesbar) freigegeben")
	}
	for status, soll := range map[int]bool{400: true, 404: true, 409: true, 413: true, 422: true, 429: true,
		500: false, 503: false, 200: false, 0: false} {
		if ist := loeschCodeFreigebbar(&pufferAntwort{status: status}); ist != soll {
			t.Errorf("Status %d: %v", status, ist)
		}
	}
}

// Abschluss-QA #757 Q-1: die Verdrahtung im Fehlerzweig von werkzeugLoeschen
// mit dem echten Code-Speicher — nach 5xx oder ohne Antwort bleibt der Code
// verbraucht, nach einer Ablehnung vor dem COMMIT gilt er wieder.
func TestLoeschNichtGeschrieben757(t *testing.T) {
	db, err := storage.Open(t.Context(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	codes := storage.NewLoeschCodeRepo(db)
	ablauf := time.Now().Add(loeschCodeGueltig)
	for i, fall := range []struct {
		puf  *pufferAntwort
		frei bool
	}{
		{&pufferAntwort{kopf: http.Header{}, status: http.StatusInternalServerError}, false},
		{&pufferAntwort{kopf: http.Header{}, status: http.StatusServiceUnavailable}, false},
		{nil, false},
		{&pufferAntwort{kopf: http.Header{}, status: http.StatusConflict}, true},
		{&pufferAntwort{kopf: http.Header{}, status: http.StatusUnprocessableEntity}, true},
	} {
		zufall := "z" + strconv.Itoa(i)
		if err := codes.Verbrauche(t.Context(), zufall, ablauf); err != nil {
			t.Fatal(err)
		}
		puffer := &pufferAntwort{kopf: http.Header{}, status: http.StatusInternalServerError}
		loeschNichtGeschrieben(t.Context(), httptest.NewRecorder(), codes, zufall, &loeschKarte, puffer, fall.puf)
		err := codes.Verbrauche(t.Context(), zufall, ablauf)
		if frei := err == nil; frei != fall.frei {
			t.Errorf("Fall %d: wieder frei = %v, erwartet %v (%v)", i, frei, fall.frei, err)
		}
	}
}
