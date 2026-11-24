package api_test

// HTTP-Ebene des Löschpfads (Karte #571). Die Tabellen-Vollständigkeit prüft
// storage/user_loeschung_test.go; hier geht es um Statuscodes, die 409-Liste
// und den Zugang nur für Admins.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func seedBoardMitOwner(t *testing.T, env *adminTestEnv, boardID, email string) {
	t.Helper()
	if _, err := env.db.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, '1', '{}', 1, 1)`, boardID, "Board "+boardID); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if _, err := env.db.Exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, 'owner', '2026-01-01')`, boardID, email); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
}

func TestAdmin_UserDelete(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@example.com", true)
	env.seedUser(t, "u-anna", "anna@example.com", false)
	env.seedUser(t, "u-carla", "carla@example.com", false)
	seedBoardMitOwner(t, env, "b-anna", "anna@example.com")

	// Kein Admin → 403, nichts passiert.
	resp := env.do(t, http.MethodDelete, "/admin/users/u-anna", "carla@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d, erwartet 403", resp.StatusCode)
	}

	// Ohne Übertragung → 409 mit Liste.
	resp = env.do(t, http.MethodDelete, "/admin/users/u-anna", "admin@example.com", nil)
	var konflikt struct {
		Boards []struct{ ID, Title string } `json:"boards"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&konflikt)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || len(konflikt.Boards) != 1 || konflikt.Boards[0].ID != "b-anna" || konflikt.Boards[0].Title != "Board b-anna" {
		t.Fatalf("ohne Übertragung: %d %+v, erwartet 409 mit b-anna", resp.StatusCode, konflikt)
	}

	// Übertragung an Unbekannte → 400.
	resp = env.do(t, http.MethodDelete, "/admin/users/u-anna", "admin@example.com",
		map[string]any{"uebertragen": map[string]string{"b-anna": "niemand@example.com"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unbekanntes Ziel: %d, erwartet 400", resp.StatusCode)
	}

	// Mit Übertragung → 200, Pseudonym in der Antwort.
	resp = env.do(t, http.MethodDelete, "/admin/users/u-anna", "admin@example.com",
		map[string]any{"uebertragen": map[string]string{"b-anna": "carla@example.com"}})
	var ok struct {
		OK        bool             `json:"ok"`
		Pseudonym string           `json:"pseudonym"`
		Gezaehlt  map[string]int64 `json:"gezaehlt"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !ok.OK || ok.Pseudonym == "" || ok.Gezaehlt["users"] != 1 {
		t.Fatalf("Löschung: %d %+v", resp.StatusCode, ok)
	}

	// Zweiter Versuch → 404.
	resp = env.do(t, http.MethodDelete, "/admin/users/u-anna", "admin@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("zweite Löschung: %d, erwartet 404", resp.StatusCode)
	}

	// Selbstlöschung → 409.
	resp = env.do(t, http.MethodDelete, "/admin/users/u-admin", "admin@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("Selbstlöschung: %d, erwartet 409", resp.StatusCode)
	}

	// Ungültiger Body → 400.
	r, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/admin/users/u-carla", strings.NewReader("{kaputt"))
	r.Header.Set("Remote-Email", "admin@example.com")
	r.Header.Set("Remote-User", "admin@example.com")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("kaputter Body: %d, erwartet 400", resp.StatusCode)
	}
}
