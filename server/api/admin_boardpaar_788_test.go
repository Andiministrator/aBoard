package api_test

// Karte #788 (ADR-0140 §4): Admin-Reiter „Board-Paar“ — koppeln, entkoppeln,
// Zugänge, Seite der Instanz. Nur Server-Admins; ein Board-Owner ohne
// Admin-Recht bekommt 403 (Katalog: „Board-Owner ohne Admin → 403“).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type boardpaarAdminEnv struct {
	srv  *httptest.Server
	paar *storage.BoardpaarRepo
	cfg  *storage.ConfigRepo
	db   *sql.DB
}

func newBoardpaarAdminEnv(t *testing.T) *boardpaarAdminEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, u := range [][]any{{"u-owner", "owner@firma.de", 0}, {"u-admin", "admin@firma.de", 1}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	for _, id := range []string{"b1", "b2", storage.ServiceBoardID} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
		if err := acl.Set(ctx, id, "owner@firma.de", storage.RoleOwner); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return "audit-" + string(rune('a'+n)) })
	paar := storage.NewBoardpaarRepo(db)
	adm := api.NewAdminAPI(cfg, storage.NewUserRepo(db), "test", time.Now())
	mux := http.NewServeMux()
	adm.Register(mux)
	adm.RegisterBoardpaar(mux, paar)
	srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
	t.Cleanup(srv.Close)
	return &boardpaarAdminEnv{srv: srv, paar: paar, cfg: cfg, db: db}
}

func (e *boardpaarAdminEnv) do(t *testing.T, method, path, email string, body any) (int, map[string]any, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Remote-Email", email)
	r.Header.Set("Remote-User", email)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func TestBoardpaarAdmin788_NurAdmin(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	wege := []struct {
		m, p string
		body any
	}{
		{http.MethodGet, "/admin/boardpaar", nil},
		{http.MethodPost, "/admin/boardpaar/innen", map[string]string{"board_id": "b1"}},
		{http.MethodDelete, "/admin/boardpaar/innen/b1", nil},
		{http.MethodPost, "/admin/boardpaar/aussen", map[string]string{"board_id": "b1", "innen_link_basis": "https://i.example/#board=x"}},
		{http.MethodDelete, "/admin/boardpaar/aussen/b1", nil},
		{http.MethodPost, "/admin/boardpaar/zugaenge", map[string]string{"typ": "abs", "name": "push"}},
		{http.MethodDelete, "/admin/boardpaar/zugaenge/x", nil},
		{http.MethodPut, "/admin/config/boardpaar.seite", map[string]any{"value": "innen"}},
		{http.MethodDelete, "/admin/config/boardpaar.seite", nil},
	}
	for _, w := range wege {
		// Owner aller Boards, aber kein Server-Admin.
		if code, _, raw := e.do(t, w.m, w.p, "owner@firma.de", w.body); code != http.StatusForbidden {
			t.Errorf("%s %s als Board-Owner: %d %s, erwartet 403", w.m, w.p, code, raw)
		}
	}
	ctx := context.Background()
	if l, _ := e.paar.InnenListe(ctx); len(l) != 0 {
		t.Errorf("Nicht-Admin hat gekoppelt: %+v", l)
	}
	if l, _ := e.paar.TokenListe(ctx); len(l) != 0 {
		t.Errorf("Nicht-Admin hat einen Zugang angelegt: %+v", l)
	}
	if s := api.BoardpaarSeite(ctx, e.cfg); s != api.BoardpaarSeiteAus {
		t.Errorf("Nicht-Admin hat die Seite gesetzt: %s", s)
	}
}

func TestBoardpaarAdmin788_Seite(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	if code, m, _ := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil); code != 200 || m["seite"] != "aus" {
		t.Fatalf("GET: %d %v", code, m)
	}
	for _, schlecht := range []map[string]any{{"value": "draussen"}, {"value": "innen", "is_secret": true}, {"value": "innen extern"}} {
		if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", "admin@firma.de", schlecht); code != http.StatusBadRequest {
			t.Errorf("PUT %v: %d %s, erwartet 400", schlecht, code, raw)
		}
	}
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", "admin@firma.de", map[string]any{"value": "  INNEN "}); code != 200 {
		t.Fatalf("PUT innen: %d %s", code, raw)
	}
	if v, err := e.cfg.Get(ctx, api.KonfigBoardpaarSeite); err != nil || v.Value != "innen" {
		t.Errorf("gespeichert: %+v, %v — erwartet Normalform innen", v, err)
	}
	if code, m, _ := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil); code != 200 || m["seite"] != "innen" {
		t.Errorf("GET nach PUT: %d %v", code, m)
	}
	if code, _, _ := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", "admin@firma.de", map[string]any{"value": ""}); code != 200 {
		t.Fatal("PUT leer")
	}
	if s := api.BoardpaarSeite(ctx, e.cfg); s != api.BoardpaarSeiteAus {
		t.Errorf("leer gespeichert, Seite %s, erwartet aus", s)
	}
	// Kritiker K-6: Löschen des Schlüssels heisst „aus“.
	if code, _, _ := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", "admin@firma.de", map[string]any{"value": "aussen"}); code != 200 {
		t.Fatal("PUT aussen")
	}
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/config/boardpaar.seite", "admin@firma.de", nil); code != 200 {
		t.Fatalf("DELETE: %d %s", code, raw)
	}
	if s := api.BoardpaarSeite(ctx, e.cfg); s != api.BoardpaarSeiteAus {
		t.Errorf("nach DELETE Seite %s, erwartet aus", s)
	}
}

func TestBoardpaarAdmin788_Koppeln(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	if code, _, _ := e.do(t, http.MethodPost, "/admin/boardpaar/innen", admin, map[string]string{"board_id": "gibtsnicht"}); code != http.StatusNotFound {
		t.Errorf("innen unbekannt: %d", code)
	}
	if code, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/innen", admin, map[string]string{"board_id": ""}); code != http.StatusBadRequest || !strings.Contains(raw, "board_id required") {
		t.Errorf("innen leer: %d %s", code, raw)
	}
	if code, _, _ := e.do(t, http.MethodPost, "/admin/boardpaar/innen", admin, map[string]string{"board_id": storage.ServiceBoardID}); code != http.StatusBadRequest {
		t.Errorf("innen Service-Board: %d", code)
	}
	if code, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/innen", admin, map[string]string{"board_id": "b1"}); code != 200 {
		t.Fatalf("innen b1: %d %s", code, raw)
	}
	if ok, _ := e.paar.InnenGekoppelt(ctx, "b1"); !ok {
		t.Error("b1 nicht gekoppelt")
	}
	if code, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", admin, map[string]string{"board_id": "b2", "innen_link_basis": "http://i.example/#board=x"}); code != http.StatusBadRequest || !strings.Contains(raw, "https") {
		t.Errorf("außen schlechte Adresse: %d %s", code, raw)
	}
	code, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", admin, map[string]string{"board_id": "b2", "innen_link_basis": "https://i.example#board=x"})
	if code != 200 || m["innen_link_basis"] != "https://i.example/#board=x" || m["von"] != "u-admin" {
		t.Fatalf("außen b2: %d %s", code, raw)
	}
	_, m, _ = e.do(t, http.MethodGet, "/admin/boardpaar", admin, nil)
	if len(m["innen"].([]any)) != 1 || len(m["aussen"].([]any)) != 1 {
		t.Errorf("Liste: %v", m)
	}
	for _, p := range []string{"/admin/boardpaar/innen/b1", "/admin/boardpaar/aussen/b2"} {
		if code, _, _ := e.do(t, http.MethodDelete, p, admin, nil); code != 200 {
			t.Errorf("DELETE %s: %d", p, code)
		}
		if code, _, _ := e.do(t, http.MethodDelete, p, admin, nil); code != http.StatusNotFound {
			t.Errorf("zweites DELETE %s: %d", p, code)
		}
	}
}

func TestBoardpaarAdmin788_Zugaenge(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	const admin = "admin@firma.de"
	if code, _, _ := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]string{"typ": "abk", "name": "x"}); code != http.StatusBadRequest {
		t.Errorf("falscher Typ: %d", code)
	}
	code, m, raw := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]string{"typ": "abs", "name": "push"})
	if code != http.StatusCreated {
		t.Fatalf("anlegen: %d %s", code, raw)
	}
	klartext, _ := m["token"].(string)
	if !strings.HasPrefix(klartext, "abs_") {
		t.Fatalf("Token %q ohne Präfix abs_", klartext)
	}
	zugang := m["zugang"].(map[string]any)
	if zugang["created_by"] != "u-admin" {
		t.Errorf("created_by %v", zugang["created_by"])
	}
	if code, _, _ := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]string{"typ": "abs", "name": "push"}); code != http.StatusConflict {
		t.Errorf("doppelter Name: %d", code)
	}
	if code, _, _ := e.do(t, http.MethodPost, "/admin/boardpaar/zugaenge", admin, map[string]string{"typ": "abe", "name": "push"}); code != http.StatusCreated {
		t.Errorf("gleicher Name, anderer Typ: %d", code)
	}
	_, _, liste := e.do(t, http.MethodGet, "/admin/boardpaar", admin, nil)
	if strings.Contains(liste, klartext) || strings.Contains(liste, "token_hash") {
		t.Errorf("Liste trägt ein Geheimnis: %s", liste)
	}
	id := zugang["id"].(string)
	if code, _, _ := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+id, admin, nil); code != 200 {
		t.Errorf("widerrufen: %d", code)
	}
	if code, _, _ := e.do(t, http.MethodDelete, "/admin/boardpaar/zugaenge/"+id, admin, nil); code != http.StatusNotFound {
		t.Errorf("zweites Widerrufen: %d", code)
	}
	if _, err := e.paar.TokenPruefe(context.Background(), storage.BoardpaarTypAusgang, klartext); err == nil {
		t.Error("widerrufenes Token gilt noch")
	}
}

// F-2021 (Sprint-89-Kritiker KS-6): Die Seite wechselt nur, wenn auf der
// bisherigen Seite kein Board gekoppelt ist — sonst 409, und Kopplung und
// Schatten bleiben unberührt. Nach dem Entkoppeln geht der Wechsel, und dann
// liegt kein Schatten mehr (TestBoardpaarAdmin2021_SeiteAusOhneSchatten).
func TestBoardpaarAdmin2021_SeiteAusOhneSchatten(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	admin := "admin@firma.de"
	seite := func(wert string) (int, string) {
		code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", admin, map[string]any{"value": wert})
		return code, raw
	}
	schatten := func() int {
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM schatten`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if code, raw := seite("aussen"); code != 200 {
		t.Fatalf("PUT aussen: %d %s", code, raw)
	}
	if code, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", admin,
		map[string]string{"board_id": "b1", "innen_link_basis": "https://i.example/#board=x"}); code != 200 && code != 201 {
		t.Fatalf("koppeln: %d %s", code, raw)
	}
	if _, err := e.db.Exec(`INSERT INTO schatten (board_id, schatten_id, art, vertraulich, bearbeiter, von_agent, geaendert_at)
		VALUES ('b1', 's1', 'karte', 0, 'x', 0, '2026-10-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, wert := range []string{"aus", "innen"} {
		if code, raw := seite(wert); code != http.StatusConflict || !strings.Contains(raw, "b1") {
			t.Errorf("PUT %s gekoppelt: %d %s, erwartet 409 mit Board b1", wert, code, raw)
		}
	}
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/config/boardpaar.seite", admin, nil); code != http.StatusConflict {
		t.Errorf("DELETE gekoppelt: %d %s, erwartet 409", code, raw)
	}
	if s := api.BoardpaarSeite(ctx, e.cfg); s != api.BoardpaarSeiteAussen || schatten() != 1 {
		t.Fatalf("nach Ablehnung: Seite %s, Schatten %d — erwartet aussen, 1", s, schatten())
	}
	if code, raw := seite("aussen"); code != 200 {
		t.Errorf("PUT gleiche Seite: %d %s, erwartet 200", code, raw)
	}
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/aussen/b1", admin, nil); code != 200 && code != 204 {
		t.Fatalf("entkoppeln: %d %s", code, raw)
	}
	if code, raw := seite("aus"); code != 200 {
		t.Fatalf("PUT aus nach Entkoppeln: %d %s", code, raw)
	}
	if n := schatten(); n != 0 {
		t.Errorf("Seite aus, aber %d Schatten", n)
	}
	// Innen: dieselbe Sperre.
	if code, raw := seite("innen"); code != 200 {
		t.Fatalf("PUT innen: %d %s", code, raw)
	}
	if err := e.paar.InnenKoppeln(ctx, "b2", "t"); err != nil {
		t.Fatal(err)
	}
	if code, raw := seite("aussen"); code != http.StatusConflict {
		t.Errorf("innen gekoppelt → aussen: %d %s, erwartet 409", code, raw)
	}
}

// Sitzungs-QA K-1: Ist die heutige Seite nicht lesbar, wird der Wechsel
// nicht blind erlaubt — 503, nichts geschrieben (fail-closed).
func TestBoardpaarAdmin2021_SeiteNichtLesbar503(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-admin', 'admin@firma.de', 'N', '2026-01-01', 1)`); err != nil {
		t.Fatal(err)
	}
	users := storage.NewUserRepo(db)
	kaputtDB, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = kaputtDB.Close()
	adm := api.NewAdminAPI(storage.NewConfigRepo(kaputtDB, nil, func() string { return "x" }), users, "test", time.Now())
	mux := http.NewServeMux()
	adm.Register(mux)
	adm.RegisterBoardpaar(mux, storage.NewBoardpaarRepo(db))
	srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
	t.Cleanup(srv.Close)
	e := &boardpaarAdminEnv{srv: srv}
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.seite", "admin@firma.de", map[string]any{"value": "innen"}); code != http.StatusServiceUnavailable {
		t.Errorf("PUT bei unlesbarer Seite: %d %s, erwartet 503", code, raw)
	}
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/config/boardpaar.seite", "admin@firma.de", nil); code != http.StatusServiceUnavailable {
		t.Errorf("DELETE bei unlesbarer Seite: %d %s, erwartet 503", code, raw)
	}
	_ = db.Close()
}
