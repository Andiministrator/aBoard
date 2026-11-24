package api_test

// HTTP-Ebene der Nutzerliste (Karte #575, Folge zu #571). Die
// Zähl-Definition selbst prüft storage/users_test.go; hier geht es um
// Statuscodes, Sortierung und den Zugang nur für Admins.
// seedBoardMitOwner stammt aus admin_user_loeschung_test.go (selbes Package).

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAdmin_UserList(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@example.com", true)
	env.seedUser(t, "u-anna", "anna@example.com", false)
	seedBoardMitOwner(t, env, "b-anna", "anna@example.com")

	// Kein Admin → 403.
	resp := env.do(t, http.MethodGet, "/admin/users", "anna@example.com", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d, erwartet 403", resp.StatusCode)
	}

	resp = env.do(t, http.MethodGet, "/admin/users", "admin@example.com", nil)
	var body struct {
		Users []struct {
			ID                string `json:"id"`
			Email             string `json:"email"`
			IsAdmin           bool   `json:"is_admin"`
			BoardsOwner       int    `json:"boards_owner"`
			BoardsEinzelOwner int    `json:"boards_einzel_owner"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Liste: %d, erwartet 200", resp.StatusCode)
	}
	if len(body.Users) != 2 {
		t.Fatalf("erwartet 2 Nutzer, bekam %d", len(body.Users))
	}
	// Sortiert nach Email: admin vor anna.
	if body.Users[0].Email != "admin@example.com" || body.Users[1].Email != "anna@example.com" {
		t.Fatalf("Sortierung unerwartet: %+v", body.Users)
	}
	if !body.Users[0].IsAdmin {
		t.Fatalf("admin.is_admin sollte true sein")
	}
	if body.Users[1].BoardsOwner != 1 || body.Users[1].BoardsEinzelOwner != 1 {
		t.Fatalf("anna unerwartet: %+v", body.Users[1])
	}
}
