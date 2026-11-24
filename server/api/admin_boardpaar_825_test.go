package api_test

// Karte #825 (ADR-0141 §3): Admin-Weg „Externe Kopie anlegen“, Ziele und
// Aufträge in der Liste, Zugang abr und Recht darf_anlegen, Einstellung
// boardpaar.team. Gerüst aus admin_boardpaar_788_test.go.

import (
	"context"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaarAdmin825_AuftragUndZugaenge(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"

	// Nur Admins, wie alle Board-Paar-Wege.
	for _, w := range []struct {
		m, p string
		body any
	}{
		{http.MethodPost, "/admin/boardpaar/auftraege", map[string]string{"board_id": "b1", "ziel": "tg"}},
		{http.MethodDelete, "/admin/boardpaar/auftraege/x", nil},
	} {
		if s, _, raw := e.do(t, w.m, w.p, "owner@firma.de", w.body); s != http.StatusForbidden {
			t.Errorf("%s %s als Owner: %d %s", w.m, w.p, s, raw)
		}
	}

	// Seite aus: kein Auftrag.
	if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/auftraege", admin,
		map[string]string{"board_id": "b1", "ziel": "tg", "innen_link_basis": "https://i.example/#board=b1"}); s != http.StatusConflict {
		t.Errorf("Seite aus: %d %s", s, raw)
	}
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "t"); err != nil {
		t.Fatal(err)
	}
	// Ohne Adresse und ohne ABOARD_PUBLIC_BASE_URL: 400 mit Grund.
	if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/auftraege", admin,
		map[string]string{"board_id": "b1", "ziel": "tg"}); s != http.StatusBadRequest {
		t.Errorf("ohne Adresse: %d %s", s, raw)
	}
	s, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/auftraege", admin,
		map[string]string{"board_id": "b1", "ziel": "tg", "innen_link_basis": "https://i.example/#board=b1"})
	if s != http.StatusCreated || m["status"] != "offen" {
		t.Fatalf("Auftrag: %d %s", s, raw)
	}
	id, _ := m["id"].(string)
	if ok, _ := e.paar.InnenGekoppelt(ctx, "b1"); !ok {
		t.Error("Auftrag hat b1 nicht gekoppelt")
	}
	if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/auftraege", admin,
		map[string]string{"board_id": "b1", "ziel": "tg", "innen_link_basis": "https://i.example/#board=b1"}); s != http.StatusConflict {
		t.Errorf("zweiter offener Auftrag: %d %s", s, raw)
	}
	for name, body := range map[string]map[string]string{
		"Service-Board":    {"board_id": storage.ServiceBoardID, "ziel": "tg", "innen_link_basis": "https://i.example/#board=s"},
		"unbekannt":        {"board_id": "nein", "ziel": "tg", "innen_link_basis": "https://i.example/#board=n"},
		"Ziel ungültig":    {"board_id": "b2", "ziel": "a b", "innen_link_basis": "https://i.example/#board=b2"},
		"Adresse ungültig": {"board_id": "b2", "ziel": "tg", "innen_link_basis": "http://i.example/#board=b2"},
	} {
		if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/auftraege", admin, body); s < 400 || s >= 500 {
			t.Errorf("%s: %d %s", name, s, raw)
		}
	}
	_, liste, _ := e.do(t, http.MethodGet, "/admin/boardpaar", admin, nil)
	if a, _ := liste["auftraege"].([]any); len(a) != 1 {
		t.Errorf("Liste ohne Auftrag: %v", liste)
	}
	if _, ok := liste["ziele"]; !ok {
		t.Error("Liste ohne ziele")
	}
	if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/auftraege/"+id, admin, nil); s != http.StatusOK {
		t.Errorf("zurückziehen: %d %s", s, raw)
	}
	if s, _, _ := e.do(t, http.MethodDelete, "/admin/boardpaar/auftraege/"+id, admin, nil); s != http.StatusNotFound {
		t.Errorf("zweimal zurückziehen: %d", s)
	}

	// Zugänge: abr anlegbar; darf_anlegen nur für abe.
	if s, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]any{"typ": "abr", "name": "rueck"}); s != http.StatusCreated ||
		len(m["token"].(string)) < 10 || m["token"].(string)[:4] != "abr_" {
		t.Errorf("abr: %d %s", s, raw)
	}
	if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]any{"typ": "abs", "name": "x", "darf_anlegen": true}); s != http.StatusBadRequest {
		t.Errorf("abs mit Recht: %d %s", s, raw)
	}
	if s, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]any{"typ": "abe", "name": "kopien", "darf_anlegen": true}); s != http.StatusCreated ||
		m["zugang"].(map[string]any)["darf_anlegen"] != true {
		t.Errorf("abe mit Recht: %d %s", s, raw)
	}
}

func TestBoardpaarAdmin825_Team(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	t.Setenv(storage.ACLDomainsEnv, "team.example")
	const admin = "admin@firma.de"
	if s, _, raw := e.do(t, http.MethodPut, "/admin/config/"+api.KonfigBoardpaarTeam, admin,
		map[string]any{"value": "A@team.example; @team.example"}); s != http.StatusOK {
		t.Fatalf("Team: %d %s", s, raw)
	}
	if v, err := e.cfg.Get(context.Background(), api.KonfigBoardpaarTeam); err != nil || v.Value != "a@team.example,@team.example" {
		t.Errorf("Normalform: %+v %v", v, err)
	}
	for _, schlecht := range []string{"kein-at", "@fremd.example"} {
		if s, _, raw := e.do(t, http.MethodPut, "/admin/config/"+api.KonfigBoardpaarTeam, admin,
			map[string]any{"value": schlecht}); s != http.StatusBadRequest {
			t.Errorf("%q: %d %s", schlecht, s, raw)
		}
	}
	_, liste, _ := e.do(t, http.MethodGet, "/admin/boardpaar", admin, nil)
	if liste["team"] != "a@team.example,@team.example" {
		t.Errorf("Liste: team = %v", liste["team"])
	}
}

// Verify #825 V-1: Ein Board mit Spiegel-Marken lässt sich im Admin-Weg nicht
// von Hand außen koppeln — 409 mit Grund.
func TestBoardpaarAdmin825_KopieNichtVonHand(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	// Direkt per SQL: Insert entfernt Spiegel-Marken (ADR-0141 §5.1, #826) —
	// hier steht eine außen entkoppelte Kopie, deren Marken geblieben sind.
	if _, err := storage.NewBoardRepo(e.db).Insert(ctx, "b3", "K", "2", storage.CanonicalEmptyBoardState("b3", "K")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE boards SET state_json = ? WHERE id = 'b3'`,
		`{"version":"2","board":{"id":"b3","title":"K","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}]}}`); err != nil {
		t.Fatal(err)
	}
	s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", "admin@firma.de",
		map[string]string{"board_id": "b3", "innen_link_basis": "https://i.example/#board=x"})
	if s != http.StatusConflict {
		t.Errorf("Kopie von Hand: %d %s", s, raw)
	}
	if s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", "admin@firma.de",
		map[string]string{"board_id": "b1", "innen_link_basis": "https://i.example/#board=x"}); s != http.StatusOK {
		t.Errorf("gewöhnliches Board: %d %s", s, raw)
	}
}
