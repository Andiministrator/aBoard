package api_test

// activities_read_test.go — Tests für den Lese-Endpunkt fürs Protokoll je
// Karte (ADR-0129 §5, Karte #554): GET /boards/{id}/activities.
//
// Abgedeckt:
//  - Karten-Filter (?card=...) liefert nur die Einträge dieser Karte
//  - Neueste zuerst
//  - ?limit begrenzt die Anzahl
//  - unbekanntes Board → 404
//  - ohne WithActivities → 503

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// getActivitiesReq ruft GET /boards/{board}/activities[?query] auf und
// dekodiert den Body als JSON-Array (leer bei Nicht-200).
func getActivitiesReq(t *testing.T, env *boardsTestEnv, board, query string) (int, []map[string]any) {
	t.Helper()
	url := env.srv.URL + "/boards/" + board + "/activities"
	if query != "" {
		url += "?" + query
	}
	resp := mustDo(t, opsReq(t, http.MethodGet, url, nil))
	defer resp.Body.Close()
	body := mustReadAll(t, resp.Body)
	var out []map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("Antwort nicht als JSON-Array dekodierbar: %v — %s", err, body)
		}
	}
	return resp.StatusCode, out
}

// TestActivitiesRead_FilterUndSortierung: ?card=k1 liefert NUR die
// Einträge von k1, neueste zuerst.
//
// GEGENPROBE (siehe Bericht): Ignoriert getActivities den ?card-Parameter
// (ListFiltered immer mit cardID=""), liefert die Anfrage 3 statt 2
// Einträge — der Test wird rot.
func TestActivitiesRead_FilterUndSortierung(t *testing.T) {
	env, _ := newOpsActivitiesEnv(t)
	legeOpsBoardAn(t, env)

	if code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`); code != http.StatusOK {
		t.Fatalf("erste Op: Status %d: %v", code, out)
	}
	if code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k2","field":"qa","value":true}}]`); code != http.StatusOK {
		t.Fatalf("zweite Op: Status %d: %v", code, out)
	}
	// Zweite Op auf k1, aber ein ANDERER op_type (card.move statt
	// card.field) — RecordOps zerlegt den Payload nicht in field/from/to
	// (das bleibt card.field-Bestandsmutationen vorbehalten), also
	// unterscheidbar nur über op_type/ts, nicht über field/value.
	if code, out := postOps(t, env, "b1", `[{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1","toColId":"col-erledigt"}}]`); code != http.StatusOK {
		t.Fatalf("dritte Op: Status %d: %v", code, out)
	}

	status, out := getActivitiesReq(t, env, "b1", "card=k1")
	if status != http.StatusOK {
		t.Fatalf("GET Status %d", status)
	}
	if len(out) != 2 {
		t.Fatalf("erwartet 2 Einträge für k1, erhalten %d: %v", len(out), out)
	}
	for _, e := range out {
		if e["card_id"] != "k1" {
			t.Errorf("Eintrag mit card_id %v im k1-Filter", e["card_id"])
		}
	}
	// Neueste zuerst: die dritte Op (card.move) steht vorn.
	if out[0]["op_type"] != "card.move" {
		t.Errorf("erster Eintrag: op_type=%v, erwartet card.move (neueste zuerst)", out[0]["op_type"])
	}
	if out[1]["op_type"] != "card.field" {
		t.Errorf("zweiter Eintrag: op_type=%v, erwartet card.field", out[1]["op_type"])
	}
}

// TestActivitiesRead_Limit prüft, dass ?limit die Trefferzahl begrenzt.
func TestActivitiesRead_Limit(t *testing.T) {
	env, _ := newOpsActivitiesEnv(t)
	legeOpsBoardAn(t, env)
	for i := 0; i < 5; i++ {
		if code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`); code != http.StatusOK {
			t.Fatalf("Op %d: Status %d: %v", i, code, out)
		}
	}
	status, out := getActivitiesReq(t, env, "b1", "limit=2")
	if status != http.StatusOK {
		t.Fatalf("GET Status %d", status)
	}
	if len(out) != 2 {
		t.Fatalf("erwartet 2 Einträge (limit=2), erhalten %d", len(out))
	}
}

// TestActivitiesRead_UnbekanntesBoard404: fehlendes Board → 404.
func TestActivitiesRead_UnbekanntesBoard404(t *testing.T) {
	env, _ := newOpsActivitiesEnv(t)
	status, _ := getActivitiesReq(t, env, "gibtsnicht", "")
	if status != http.StatusNotFound {
		t.Errorf("Status %d, erwartet 404", status)
	}
}

// TestActivitiesRead_OhneActivities503: kein WithActivities → 503 (analog
// WithFold/postOps).
func TestActivitiesRead_OhneActivities503(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).Register(mux) // bewusst kein WithActivities
	srv := httptest.NewServer(mux)
	defer srv.Close()
	env := &boardsTestEnv{srv: srv, db: db}

	create := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards", sampleBody("b1", "Ohne-Log")))
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create: Status %d", create.StatusCode)
	}

	status, _ := getActivitiesReq(t, env, "b1", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("Status %d, erwartet 503", status)
	}
}

// TestActivitiesRead_ACLHistorieNurFuerOwner (Kritiker Sprint 71): Die
// Einträge acl-set/acl-deleted tragen E-Mail und Rolle der Berechtigten.
// Die ACL-Liste darf nur der Owner lesen (F-261), also darf ein Viewer sie
// auch nicht über das Protokoll rekonstruieren. Der Owner sieht sie.
func TestActivitiesRead_ACLHistorieNurFuerOwner(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	act := storage.NewActivityRepo(db)
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).WithActivities(act).Register(mux)
	var h http.Handler = api.NewACLGuard(acl, storage.NewUserRepo(db), storage.NewBootstrapRepo(db)).Middleware(mux)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	if _, err := boards.Insert(ctx, "A", "Board A", "2", sampleBody("A", "Board A")); err != nil {
		t.Fatal(err)
	}
	for email, rolle := range map[string]string{"owner@example.com": storage.RoleOwner, "viewer@example.com": storage.RoleViewer} {
		if err := acl.Set(ctx, "A", email, rolle); err != nil {
			t.Fatal(err)
		}
	}
	if err := act.Record(ctx, "A", "owner@example.com", "acl-set", "role", "", "viewer", "viewer@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := act.Record(ctx, "A", "owner@example.com", "board-updated", "title", "", "Board A", ""); err != nil {
		t.Fatal(err)
	}

	lies := func(email string) []map[string]any {
		t.Helper()
		r, _ := http.NewRequest(http.MethodGet, srv.URL+"/boards/A/activities", nil)
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
		resp := mustDo(t, r)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: Status %d", email, resp.StatusCode)
		}
		var out []map[string]any
		_ = json.Unmarshal(mustReadAll(t, resp.Body), &out)
		return out
	}
	aclEintraege := func(out []map[string]any) int {
		n := 0
		for _, e := range out {
			if a, _ := e["action"].(string); len(a) > 4 && a[:4] == "acl-" {
				n++
			}
		}
		return n
	}
	if o := lies("owner@example.com"); aclEintraege(o) != 1 || len(o) != 2 {
		t.Errorf("Owner: %d Einträge, davon %d acl-* — erwartet 2 und 1", len(o), aclEintraege(o))
	}
	if v := lies("viewer@example.com"); aclEintraege(v) != 0 || len(v) != 1 {
		t.Errorf("Viewer: %d Einträge, davon %d acl-* — erwartet 1 und 0", len(v), aclEintraege(v))
	}
}
