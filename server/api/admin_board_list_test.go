package api_test

// F-1439: GET /admin/boards listet ALLE Boards, auch ohne ACL-Eintrag.
// Ohne Anmeldung weist die Forward-Auth-Schicht mit 401 ab, Nicht-Admins
// bekommen von requireAdmin 403.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// F-1442: Seitenaufteilung — limit/offset, total, next_offset, Obergrenze.
func TestAdmin_BoardList_Seiten(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@example.com", true)
	for i := 1; i <= 5; i++ {
		if _, err := env.db.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, 'T', '1', '{}', ?, 1)`, fmt.Sprintf("b%d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	hole := func(query string) (int, map[string]any) {
		t.Helper()
		resp := env.do(t, http.MethodGet, "/admin/boards"+query, "admin@example.com", nil)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	ids := func(out map[string]any) string {
		var s []string
		for _, b := range out["boards"].([]any) {
			s = append(s, b.(map[string]any)["id"].(string))
		}
		return strings.Join(s, ",")
	}

	st, out := hole("?limit=2")
	if st != 200 || ids(out) != "b5,b4" || out["total"].(float64) != 5 || out["next_offset"].(float64) != 2 {
		t.Fatalf("Seite 1: %d %v", st, out)
	}
	st, out = hole("?limit=2&offset=2")
	if st != 200 || ids(out) != "b3,b2" || out["next_offset"].(float64) != 4 {
		t.Fatalf("Seite 2: %d %v", st, out)
	}
	st, out = hole("?limit=2&offset=4")
	if st != 200 || ids(out) != "b1" || out["next_offset"] != nil || out["total"].(float64) != 5 {
		t.Fatalf("Seite 3: %d %v", st, out)
	}
	st, out = hole("")
	if st != 200 || ids(out) != "b5,b4,b3,b2,b1" || out["total"].(float64) != 5 || out["next_offset"] != nil {
		t.Fatalf("Standard: %d %v", st, out)
	}
	if st, out = hole("?limit=999999"); st != 200 || len(out["boards"].([]any)) != 5 {
		t.Fatalf("limit gekappt: %d %v", st, out)
	}
	for _, q := range []string{"?limit=0", "?limit=-1", "?limit=x", "?offset=-3", "?offset=abc"} {
		if st, out = hole(q); st != http.StatusBadRequest {
			t.Errorf("%s: %d %v, erwartet 400", q, st, out)
		}
	}
}

func TestAdmin_BoardList(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@example.com", true)
	env.seedUser(t, "u-anna", "anna@example.com", false)
	seedBoardMitOwner(t, env, "b-anna", "anna@example.com")
	// Board ohne ACL (wie das Seed-Board `default`).
	if _, err := env.db.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('default', 'Seed', '1', '{}', 5, 1)`); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	resp := env.do(t, http.MethodGet, "/admin/boards", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ohne Anmeldung: %d, erwartet 401", resp.StatusCode)
	}

	resp = env.do(t, http.MethodGet, "/admin/boards", "anna@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d, erwartet 403", resp.StatusCode)
	}

	resp = env.do(t, http.MethodGet, "/admin/boards", "admin@example.com", nil)
	var body struct {
		Boards []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			UpdatedAt int64    `json:"updated_at"`
			HatACL    bool     `json:"hat_acl"`
			Owners    []string `json:"owners"`
		} `json:"boards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin: %d, erwartet 200", resp.StatusCode)
	}
	if len(body.Boards) != 2 {
		t.Fatalf("erwartet 2 Boards, bekam %d: %+v", len(body.Boards), body.Boards)
	}
	// Jüngst geändert zuerst: default (5) vor b-anna (1).
	if body.Boards[0].ID != "default" || body.Boards[0].HatACL || len(body.Boards[0].Owners) != 0 {
		t.Fatalf("default unerwartet: %+v", body.Boards[0])
	}
	if b := body.Boards[1]; b.ID != "b-anna" || !b.HatACL || len(b.Owners) != 1 || b.Owners[0] != "anna@example.com" {
		t.Fatalf("b-anna unerwartet: %+v", b)
	}
}
