package api_test

// Karte #638 (F-1335, ADR-0130 §6 Nachtrag Sprint 80): Die Anlage eines
// Agenten-Zugangs über die Selbstbedienung verrät nicht, dass ein Name bei
// jemand anderem existiert. Agentennamen bleiben serverweit eindeutig — an
// `agent:<name>` hängen comment.edit-Eigentum, Übernahmen, Sperrbudget und
// Op-Hash; ein gleicher Name bei zwei Personen wäre ein Rechteübergang.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// verraeterischeWorte: Was eine 409 der Selbstbedienung NICHT sagen darf —
// jeder Hinweis auf eine andere Person oder auf ein aktives Token bestätigt
// dem Fragenden die fremde Existenz.
var verraeterischeWorte = []string{"another person", "bound", "active", "taken", "anderen person", "gehörte"}

func TestAgent638_NameNichtVerfuegbarVerraetNichts(t *testing.T) {
	e := newEnv608(t)
	ctx := context.Background()
	// Bert: editor auf b1 wie Anna, eigene Person.
	e.nutzerAnlegen(t, "u-bert", "bert@firma.de")
	if err := e.acl.Set(ctx, "b1", "bert@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	if s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"board":"b1","name":"claude"}`); s != http.StatusCreated {
		t.Fatalf("Anna legt claude an: %d %s", s, b)
	}

	// Bert will denselben Namen: 409, aber ohne Hinweis auf Anna.
	sFremd, bFremd := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "bert@firma.de", `{"board":"b1","name":"claude"}`)
	if sFremd != http.StatusConflict {
		t.Fatalf("fremder Name: %d %s", sFremd, bFremd)
	}
	klein := strings.ToLower(string(bFremd))
	for _, w := range verraeterischeWorte {
		if strings.Contains(klein, w) {
			t.Errorf("409 verrät fremde Existenz (%q): %s", w, bFremd)
		}
	}
	// Dieselbe Antwort, wenn der Name das EIGENE aktive Token hat — die
	// Antwort trennt die Fälle nicht.
	sEigen, bEigen := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"board":"b1","name":"claude"}`)
	if sEigen != sFremd || string(bEigen) != string(bFremd) {
		t.Errorf("eigener und fremder Fall unterscheidbar: %d %s ≠ %d %s", sEigen, bEigen, sFremd, bFremd)
	}
}

// Wer auf dem Board gar keinen Agenten anlegen darf, erfährt nichts über
// Namen: Die Rechteprüfung kommt vor der Namensprüfung (vor dem Fix kam
// eine 409 und bestätigte den Namen auch ohne Board-Rolle).
func TestAgent638_OhneRolleKeinNamensOrakel(t *testing.T) {
	e := newEnv608(t)
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	if s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"board":"b1","name":"geheim"}`); s != http.StatusCreated {
		t.Fatalf("Anna: %d %s", s, b)
	}
	// Carl: angemeldet, aber ohne Rolle auf b1.
	e.nutzerAnlegen(t, "u-carl", "carl@firma.de")
	sBelegt, bBelegt := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "carl@firma.de", `{"board":"b1","name":"geheim"}`)
	sFrei, bFrei := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "carl@firma.de", `{"board":"b1","name":"frei"}`)
	if sBelegt != http.StatusForbidden || sFrei != http.StatusForbidden || string(bBelegt) != string(bFrei) {
		t.Fatalf("Namens-Orakel ohne Rolle: belegt %d %s / frei %d %s", sBelegt, bBelegt, sFrei, bFrei)
	}
}

// Kein Rechteübergang: Weil Bert „claude“ nicht bekommt, kann kein Agent von
// Bert unter `agent:claude` stempeln — Annas Kommentar bleibt für Berts
// Agenten fremd (comment.edit → 422 fremder-kommentar).
func TestAgent638_FremderKommentarBleibtFremd(t *testing.T) {
	e := newEnv608(t)
	ctx := context.Background()
	e.nutzerAnlegen(t, "u-bert", "bert@firma.de")
	if err := e.acl.Set(ctx, "b1", "bert@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	token := func(wer, name string) string {
		t.Helper()
		s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", wer, `{"board":"b1","name":"`+name+`"}`)
		if s != http.StatusCreated {
			t.Fatalf("%s/%s: %d %s", wer, name, s, b)
		}
		i := strings.Index(string(b), `"token":"`)
		rest := string(b)[i+len(`"token":"`):]
		return rest[:strings.Index(rest, `"`)]
	}
	anna := token("anna@firma.de", "claude")
	if s, _ := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "bert@firma.de", `{"board":"b1","name":"claude"}`); s != http.StatusConflict {
		t.Fatalf("Bert bekommt Annas Namen: %d", s)
	}
	bert := token("bert@firma.de", "bert-claude")
	add := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","text":"von anna","created":"2026-10-03"}}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, add, "X-Aboard-Session", "s-anna"); s != http.StatusOK {
		t.Fatalf("Anna kommentiert: %d %s", s, b)
	}
	edit := `{"ops":[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"von bert"}}]}`
	s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", bert, edit, "X-Aboard-Session", "s-bert")
	if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "fremder-kommentar") {
		t.Fatalf("Bert bearbeitet Annas Kommentar: %d %s", s, b)
	}
}
