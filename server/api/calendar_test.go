package api_test

// calendar_test.go — Tests für GET /me/cards (Karte #308 Etappe 5, ADR-0103).
//
// Deckt: ACL-Filter (User sieht nur Karten von Boards mit Zugriff), dueDate-
// Pflicht (Karten ohne Termin fehlen), sprint-meta/archived-Ausschluss,
// dueDateRange-Filter inkl. Range-Karten-Überlappung, 400 bei kaputter Range,
// 403 ohne Identity-Email.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type calTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	closer func()
}

func newCalEnv(t *testing.T) *calTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	users := storage.NewUserRepo(db)
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)

	mux := http.NewServeMux()
	api.NewCalendarAPI(boards, acl, users).Register(mux)
	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)

	srv := httptest.NewServer(h)
	return &calTestEnv{
		srv:    srv,
		db:     db,
		boards: boards,
		acl:    acl,
		closer: func() { srv.Close(); _ = db.Close() },
	}
}

// seedCalBoard legt ein Board mit terminierten Karten an. owner bekommt owner-ACL.
func (e *calTestEnv) seedCalBoard(t *testing.T, boardID, ownerEmail string, stateJSON string) {
	t.Helper()
	if _, err := e.boards.Insert(context.Background(), boardID, "B-"+boardID, "2", []byte(stateJSON)); err != nil {
		t.Fatalf("boards.Insert %s: %v", boardID, err)
	}
	if ownerEmail != "" {
		if err := e.acl.Set(context.Background(), boardID, ownerEmail, storage.RoleOwner); err != nil {
			t.Fatalf("acl.Set %s: %v", boardID, err)
		}
	}
}

func (e *calTestEnv) get(t *testing.T, path, email string) (*http.Response, []map[string]any) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out []map[string]any
	_ = json.Unmarshal(body, &out)
	return resp, out
}

const calBoardA = `{"version":"2","board":{"id":"a","title":"A","sprints":[
	{"id":"sp1","title":"S","kind":"sprint","cards":[
		{"id":"a1","displayId":1,"title":"Termin","columnId":"col-ready","dueDate":"2026-06-15","owner":"andi"},
		{"id":"a2","displayId":2,"title":"Ohne Termin","columnId":"col-ready"},
		{"id":"a3","displayId":3,"title":"Archiviert","columnId":"col-erledigt","dueDate":"2026-06-16","archived":true},
		{"id":"meta1","displayId":4,"title":"Sprint-Start","columnId":"col-erledigt","dueDate":"2026-06-15","kind":"sprint-meta"},
		{"id":"a4","displayId":5,"title":"Range","columnId":"col-ready","startDate":"2026-06-28","dueDate":"2026-07-03"}
	]}
]}}`

const calBoardB = `{"version":"2","board":{"id":"b","title":"B","sprints":[
	{"id":"sp1","title":"S","kind":"sprint","cards":[
		{"id":"b1","displayId":1,"title":"Fremd","columnId":"col-ready","dueDate":"2026-06-15"}
	]}
]}}`

// ── ACL: nur Boards mit Zugriff ───────────────────────────────────────────

func TestMeCards_ACL_NurEigeneBoards(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", calBoardA)
	env.seedCalBoard(t, "b", "other@example.com", calBoardB)

	resp, cards := env.get(t, "/me/cards", "andi@example.com")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, erwartet 200", resp.StatusCode)
	}
	// andi sieht nur Board a: a1 (Termin) + a4 (Range). a2 ohne Termin,
	// a3 archiviert, meta1 sprint-meta → raus. b1 fremdes Board → raus.
	if len(cards) != 2 {
		t.Fatalf("len=%d, erwartet 2: %+v", len(cards), cards)
	}
	for _, c := range cards {
		if c["board_id"] != "a" {
			t.Errorf("fremdes Board durchgesickert: %v", c["board_id"])
		}
	}
}

func TestMeCards_OhneTerminUndMetaUndArchivRaus(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", calBoardA)
	_, cards := env.get(t, "/me/cards", "andi@example.com")
	ids := map[string]bool{}
	for _, c := range cards {
		ids[c["card_id"].(string)] = true
	}
	if ids["a2"] || ids["a3"] || ids["meta1"] {
		t.Errorf("ausgeschlossene Karte enthalten: %+v", ids)
	}
	if !ids["a1"] || !ids["a4"] {
		t.Errorf("erwartete Karte fehlt: %+v", ids)
	}
}

// ── dueDateRange-Filter ───────────────────────────────────────────────────

func TestMeCards_RangeFilter(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", calBoardA)

	// Nur Juni: a1 (15.06) drin, a4-Range (28.06–03.07) überlappt Juni → drin.
	_, cards := env.get(t, "/me/cards?dueDateRange=2026-06-01~2026-06-30", "andi@example.com")
	if len(cards) != 2 {
		t.Fatalf("Juni: len=%d, erwartet 2: %+v", len(cards), cards)
	}

	// Nur erste Junihälfte: nur a1, a4-Range startet 28.06 → raus.
	_, cards = env.get(t, "/me/cards?dueDateRange=2026-06-01~2026-06-10", "andi@example.com")
	if len(cards) != 0 {
		t.Fatalf("01–10.06: len=%d, erwartet 0", len(cards))
	}

	// Range, in der a4 sicher liegt, a1 raus.
	_, cards = env.get(t, "/me/cards?dueDateRange=2026-07-01~2026-07-31", "andi@example.com")
	if len(cards) != 1 || cards[0]["card_id"] != "a4" {
		t.Fatalf("Juli: erwartet nur a4, bekam %+v", cards)
	}
}

func TestMeCards_KaputteRange400(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", calBoardA)
	resp, _ := env.get(t, "/me/cards?dueDateRange=2026-06-01_2026-06-30", "andi@example.com")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("kaputte Range: status=%d, erwartet 400", resp.StatusCode)
	}
	resp, _ = env.get(t, "/me/cards?dueDateRange=2026-6-1~2026-06-30", "andi@example.com")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("nicht-ISO: status=%d, erwartet 400", resp.StatusCode)
	}
}

func TestMeCards_OhneEmail403(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", calBoardA)
	// Forward-Auth an, aber kein Remote-Email → 403.
	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/me/cards", nil)
	r.Header.Set("Remote-User", "x")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("ohne Email: status=%d, erwartet 403", resp.StatusCode)
	}
}

// ADR-0138 §1: /me/cards reicht gültige Uhrzeiten an die Kalenderansicht
// durch; eine Uhrzeit ohne zugehöriges Datum oder in falscher Form nicht.
func TestMeCards_Uhrzeiten(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", `{"version":"2","board":{"id":"a","title":"A","sprints":[
	{"id":"sp1","title":"S","kind":"sprint","cards":[
		{"id":"z1","displayId":1,"title":"Mit","columnId":"col-ready","startDate":"2026-10-08","startTime":"10:00","dueDate":"2026-10-08","dueTime":"11:30"},
		{"id":"z2","displayId":2,"title":"Verwaist","columnId":"col-ready","dueDate":"2026-10-09","startTime":"08:00","dueTime":"25:00"}
	]}
]}}`)
	_, cards := env.get(t, "/me/cards", "andi@example.com")
	byID := map[string]map[string]any{}
	for _, c := range cards {
		byID[c["card_id"].(string)] = c
	}
	if z := byID["z1"]; z["startTime"] != "10:00" || z["dueTime"] != "11:30" {
		t.Errorf("z1: Uhrzeiten fehlen: %+v", z)
	}
	if z := byID["z2"]; z["startTime"] != nil || z["dueTime"] != nil {
		t.Errorf("z2: verwaiste/ungültige Uhrzeit durchgereicht: %+v", z)
	}
}
