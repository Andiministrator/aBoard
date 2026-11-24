package api_test

// Karte #831 (ADR-0141 Nachtrag, F-2232): Notweg „Kopie außen löschen“ —
// DELETE /admin/boardpaar/kopien/{board}. Nur Server-Admins, nur außen, nur
// Kopien (gekoppelt mit kopie = 1 oder außen entkoppelt mit Marken), ohne
// Board-Rolle owner. Gerüst wie admin_boardpaar_788_test.go, dazu der
// Nach-Lösch-Schritt (WithBoardGeloescht).

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

const kopie831ID = "0b8f3a52-6c1e-4f7a-9d2b-3e4c5a6b7c8d"
const kopie831Basis = "https://innen.example/#board=innen-1"

type kopieLoeschEnv struct {
	*boardpaarAdminEnv
	geloescht []string
	broker    *api.EventBroker // F-2299: Ereignisweg der Admin-API
}

func newKopieLoeschEnv(t *testing.T) *kopieLoeschEnv {
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
	for _, id := range []string{"b1", "b2", storage.ServiceBoardID} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return "audit-" + string(rune('a'+n)) })
	paar := storage.NewBoardpaarRepo(db)
	env := &kopieLoeschEnv{broker: api.NewEventBroker()}
	adm := api.NewAdminAPI(cfg, storage.NewUserRepo(db), "test", time.Now()).
		WithBroker(env.broker).
		WithBoardGeloescht(func(_ context.Context, id string) { env.geloescht = append(env.geloescht, id) })
	mux := http.NewServeMux()
	adm.Register(mux)
	adm.RegisterBoardpaar(mux, paar)
	srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
	t.Cleanup(srv.Close)
	env.boardpaarAdminEnv = &boardpaarAdminEnv{srv: srv, paar: paar, cfg: cfg, db: db}
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "aussen", false, "t"); err != nil {
		t.Fatal(err)
	}
	return env
}

// kopieMitAnhang legt die Kopie an und hängt Journal, wartende Zeile und
// Schatten daran — alles, was mit dem Board verschwinden muss.
func kopieMitAnhang(t *testing.T, db *sql.DB, paar *storage.BoardpaarRepo) {
	t.Helper()
	ctx := context.Background()
	if _, err := paar.KopieAnlegen(ctx, kopie831ID, "Kopie", kopie831Basis, "abe:test", nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boardpaar_journal (board_id, objekt, kennung, art, zeit) VALUES ('` + kopie831ID + `', 'karte', 'k1', 'angelegt', '2026-10-14T10:00:00Z')`,
		`INSERT INTO boardpaar_wartend (board_id, aussen_id, objekt, seq_angelegt) VALUES ('` + kopie831ID + `', 'k1', 'karte', 1)`,
		`INSERT INTO schatten (board_id, schatten_id, art, vertraulich, bearbeiter, von_agent, geaendert_at)
		 VALUES ('` + kopie831ID + `', 's1', 'karte', 0, '[]', 0, '2026-10-14T10:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func zaehle(t *testing.T, db *sql.DB, tabelle, spalte string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM `+tabelle+` WHERE `+spalte+` = ?`, kopie831ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKopieLoeschen831_AdminOhneOwnerRolle(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	kopieMitAnhang(t, e.db, e.paar)
	// Der Admin ist owner nur über KopieAnlegen geworden — Rolle wegnehmen,
	// damit der Test belegt, dass der Notweg sie nicht verlangt.
	if _, err := e.db.Exec(`DELETE FROM acl WHERE board_id = ?`, kopie831ID); err != nil {
		t.Fatal(err)
	}
	pfad := "/admin/boardpaar/kopien/" + kopie831ID

	// Kein Admin → 403, Board bleibt.
	if s, _, raw := e.do(t, http.MethodDelete, pfad, "owner@firma.de", nil); s != http.StatusForbidden {
		t.Errorf("Nicht-Admin: %d %s", s, raw)
	}
	if zaehle(t, e.db, "boards", "id") != 1 {
		t.Fatal("Board nach 403 weg")
	}

	// Liste nennt die gekoppelte Kopie nicht unter kopien_entkoppelt.
	_, liste, _ := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil)
	if f, ok := liste["kopien_entkoppelt"].([]any); !ok || len(f) != 0 {
		t.Errorf("kopien_entkoppelt vor dem Entkoppeln: %v", liste["kopien_entkoppelt"])
	}

	s, m, raw := e.do(t, http.MethodDelete, pfad, "admin@firma.de", nil)
	if s != http.StatusOK || m["entkoppelt"] != false {
		t.Fatalf("Admin: %d %s", s, raw)
	}
	for _, tc := range []struct{ tabelle, spalte string }{
		{"boards", "id"}, {"boardpaar_aussen", "aussen_board_id"}, {"boardpaar_journal", "board_id"},
		{"boardpaar_wartend", "board_id"}, {"schatten", "board_id"}, {"acl", "board_id"},
	} {
		if n := zaehle(t, e.db, tc.tabelle, tc.spalte); n != 0 {
			t.Errorf("%s: %d Zeilen nach dem Löschen", tc.tabelle, n)
		}
	}
	if len(e.geloescht) != 1 || e.geloescht[0] != kopie831ID {
		t.Errorf("Nach-Lösch-Schritt: %v", e.geloescht)
	}
	// Zweimal: 404.
	if s, _, raw := e.do(t, http.MethodDelete, pfad, "admin@firma.de", nil); s != http.StatusNotFound {
		t.Errorf("zweimal: %d %s", s, raw)
	}

	// Ein neuer Auftrag legt die Kopie frisch an (keine Wiederholung, keine
	// Wiederaufnahme).
	erg, err := e.paar.KopieAnlegen(ctx, kopie831ID, "Kopie neu", kopie831Basis, "abe:test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if erg.Wiederholt || erg.Wiederaufgenommen || erg.Titel != "Kopie neu" {
		t.Errorf("nach dem Löschen nicht frisch angelegt: %+v", erg)
	}
	if n := zaehle(t, e.db, "boardpaar_journal", "board_id"); n != 0 {
		t.Errorf("Journal der neuen Kopie: %d", n)
	}
}

func TestKopieLoeschen831_EntkoppelteKopie(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	kopieMitAnhang(t, e.db, e.paar)
	// Marken setzen (die frische Kopie ist kanonisch leer) und außen entkoppeln.
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`,
		`{"version":"2","board":{"id":"`+kopie831ID+`","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}]}}`,
		kopie831ID); err != nil {
		t.Fatal(err)
	}
	if err := e.paar.AussenEntkoppeln(ctx, kopie831ID); err != nil {
		t.Fatal(err)
	}
	_, liste, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil)
	f, _ := liste["kopien_entkoppelt"].([]any)
	if len(f) != 1 || f[0].(map[string]any)["board_id"] != kopie831ID || f[0].(map[string]any)["titel"] != "Kopie" {
		t.Fatalf("kopien_entkoppelt: %s", raw)
	}
	s, m, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+kopie831ID, "admin@firma.de", nil)
	if s != http.StatusOK || m["entkoppelt"] != true {
		t.Fatalf("entkoppelte Kopie: %d %s", s, raw)
	}
	if zaehle(t, e.db, "boards", "id") != 0 || zaehle(t, e.db, "boardpaar_journal", "board_id") != 0 ||
		zaehle(t, e.db, "boardpaar_wartend", "board_id") != 0 {
		t.Error("Board, Journal oder Wartend nach dem Löschen noch da")
	}
}

func TestKopieLoeschen831_NurKopienNurAussen(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	const admin = "admin@firma.de"
	// Stufe-1-Board (kopie = 0), normales Board, Service-Board: 409.
	if _, err := e.paar.AussenKoppeln(ctx, "b1", "https://innen.example/#board=x", "t"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b1", "b2", storage.ServiceBoardID} {
		if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+id, admin, nil); s != http.StatusConflict {
			t.Errorf("%s: %d %s", id, s, raw)
		}
	}
	// Ein Board mit unlesbarem Zustand und ohne Kopplung ist keine Kopie.
	if _, err := e.db.Exec(`UPDATE boards SET state_json = '{"spiegel": kaputt' WHERE id = 'b2'`); err != nil {
		t.Fatal(err)
	}
	if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/b2", admin, nil); s != http.StatusConflict {
		t.Errorf("unlesbar: %d %s", s, raw)
	}
	if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/gibtsnicht", admin, nil); s != http.StatusNotFound {
		t.Errorf("unbekannt: %d %s", s, raw)
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM boards WHERE id IN ('b1', 'b2', ?)`, storage.ServiceBoardID).Scan(&n); err != nil || n != 3 {
		t.Errorf("Boards nach Ablehnung: %d %v", n, err)
	}

	// Seite innen (und aus): abgelehnt, auch für eine echte Kopie.
	kopieMitAnhang(t, e.db, e.paar)
	for _, seite := range []string{"innen", "aus"} {
		if err := e.cfg.Set(ctx, api.KonfigBoardpaarSeite, seite, false, "t"); err != nil {
			t.Fatal(err)
		}
		if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+kopie831ID, admin, nil); s != http.StatusConflict {
			t.Errorf("Seite %s: %d %s", seite, s, raw)
		}
	}
	if zaehle(t, e.db, "boards", "id") != 1 {
		t.Error("Kopie auf Seite innen/aus gelöscht")
	}
	if len(e.geloescht) != 0 {
		t.Errorf("Nach-Lösch-Schritt bei Ablehnung: %v", e.geloescht)
	}
}

// Nacharbeit #831, Kritik K-1: Ein gewöhnliches Board, an das der Owner per
// Ganz-PUT Spiegel-Marken setzt (oder die Sicherung einer früheren Kopie
// zurückspielt), ist keine entkoppelte Kopie — nicht in der Liste, 409 beim
// Notweg. Entschieden wird an der Herkunft (Migration 0042).
func TestKopieLoeschen831_MarkenOhneHerkunft(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	boards := storage.NewBoardRepo(e.db)
	b, err := boards.Get(ctx, "b2")
	if err != nil {
		t.Fatal(err)
	}
	markiert := `{"version":"2","board":{"id":"b2","title":"B","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}]}}`
	if _, err := boards.Update(ctx, "b2", "B", "2", []byte(markiert), b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := e.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'b2'`).Scan(&state); err != nil || !strings.Contains(state, `"spiegel"`) {
		t.Fatalf("Vorbedingung: Marken nicht angekommen (%v): %s", err, state)
	}
	_, liste, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil)
	if f, ok := liste["kopien_entkoppelt"].([]any); !ok || len(f) != 0 {
		t.Errorf("kopien_entkoppelt nennt ein Board mit selbst gesetzten Marken: %s", raw)
	}
	if s, _, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/b2", "admin@firma.de", nil); s != http.StatusConflict {
		t.Errorf("Notweg auf Board mit Marken ohne Herkunft: %d %s, erwartet 409", s, raw)
	}
	if zaehle := func() int {
		var n int
		_ = e.db.QueryRow(`SELECT count(*) FROM boards WHERE id = 'b2'`).Scan(&n)
		return n
	}(); zaehle != 1 {
		t.Error("Board b2 gelöscht")
	}
}

// Sitzungs-Verify V-1: Eine außen entkoppelte Kopie ohne Spiegel-Marken lässt
// sich im Admin-Weg nicht von Hand koppeln — 409, die Herkunft entscheidet.
func TestKopieLoeschen831_KoppelnVonHandMitHerkunft(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	if _, err := e.paar.KopieAnlegen(ctx, kopie831ID, "Kopie", kopie831Basis, "abe:test", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.paar.AussenEntkoppeln(ctx, kopie831ID); err != nil {
		t.Fatal(err)
	}
	s, _, raw := e.do(t, http.MethodPost, "/admin/boardpaar/aussen", "admin@firma.de",
		map[string]string{"board_id": kopie831ID, "innen_link_basis": kopie831Basis})
	if s != http.StatusConflict {
		t.Errorf("entkoppelte Kopie von Hand gekoppelt: %d %s, erwartet 409", s, raw)
	}
	if g, err := e.paar.AussenGekoppelt(ctx, kopie831ID); err != nil || g != nil {
		t.Errorf("nach Ablehnung gekoppelt: %+v %v", g, err)
	}
}
