package api_test

// F-2303 (Karte #836, ADR-0141 Nachtrag): Der Werkzeug-Hinweis einer Kopie
// folgt dem wirksamen Rückweg je Board — „Bearbeiten außen ist erlaubt“ nur
// bei boardpaar.rueckweg = an UND bearbeitbar = 1. Zwei Kopien auf einem
// Server, eine frei, eine gesperrt; dazu der Hinweis des internen Agenten
// (werkzeugBoardpaarFuer).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestBearbeitbar836_WerkzeugHinweisJeKopie(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	ctx := context.Background()
	if _, err := e.bp.AussenKoppeln(ctx, "b2", innenBasis789, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`,
		`UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 0 WHERE aussen_board_id = 'b2'`,
	} {
		if _, err := e.neu.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	x := e.neu
	s, b := x.rufe(t, x.token, "anna@firma.de", "GET", "/werkzeuge/boards", "")
	if s != http.StatusOK {
		t.Fatalf("boards: %d %.300s", s, b)
	}
	var liste struct {
		Boards []struct {
			BoardID   string `json:"boardId"`
			Boardpaar *struct {
				Hinweis string `json:"hinweis"`
				Kopie   bool   `json:"kopie"`
			} `json:"boardpaar"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(b, &liste); err != nil {
		t.Fatal(err)
	}
	hinweis := map[string]string{}
	for _, bd := range liste.Boards {
		if bd.Boardpaar != nil && bd.Boardpaar.Kopie {
			hinweis[bd.BoardID] = bd.Boardpaar.Hinweis
		}
	}
	if h := hinweis["b1"]; !strings.Contains(h, "Bearbeiten außen ist erlaubt") || strings.Contains(h, "nicht erlaubt") {
		t.Errorf("freie Kopie b1: %q", h)
	}
	if h := hinweis["b2"]; !strings.Contains(h, "Bearbeiten außen ist nicht erlaubt") || strings.Contains(h, "Bearbeiten außen ist erlaubt") {
		t.Errorf("gesperrte Kopie b2: %q", h)
	}
}

// Der interne Agent (Rolle intern) auf einer Kopie bei Server-Schalter an:
// freigegeben → „Bearbeiten außen ist erlaubt“, gesperrt → „nicht erlaubt“.
func TestBearbeitbar836_InternerAgentHinweisJeKopie(t *testing.T) {
	e := newEnv783(t)
	ctx := context.Background()
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	hinweis := func() string {
		t.Helper()
		s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/board", "")
		var m struct {
			Board struct {
				Boardpaar struct {
					Hinweis string `json:"hinweis"`
				} `json:"boardpaar"`
			} `json:"board"`
		}
		if err := json.Unmarshal(b, &m); err != nil || s != http.StatusOK {
			t.Fatalf("%d %.300s (%v)", s, b, err)
		}
		return m.Board.Boardpaar.Hinweis
	}
	for _, f := range []struct {
		wert int
		soll string
		nie  string
	}{{0, "Bearbeiten außen ist nicht erlaubt", "Bearbeiten außen ist erlaubt"}, {1, "Bearbeiten außen ist erlaubt", "nicht erlaubt"}} {
		if _, err := e.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = ? WHERE aussen_board_id = 'b1'`, f.wert); err != nil {
			t.Fatal(err)
		}
		if h := hinweis(); !strings.Contains(h, f.soll) || strings.Contains(h, f.nie) {
			t.Errorf("bearbeitbar %d: %q", f.wert, h)
		}
	}
}
