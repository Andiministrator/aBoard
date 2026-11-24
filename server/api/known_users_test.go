package api_test

// known_users_test.go — Tests für GET /known-users (ADR-0066 §3, Karte #224).

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// knownUsersTestEnv bündelt Server + Repos für die /known-users-Tests.
type knownUsersTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	acl    *storage.AclRepo
	users  *storage.UserRepo
	boards *storage.BoardRepo
}

func (e *knownUsersTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

// newKnownUsersTestEnv baut: ForwardAuth → ACLGuard → mux (mit /known-users registriert).
func newKnownUsersTestEnv(t *testing.T, trust bool) *knownUsersTestEnv {
	t.Helper()
	if trust {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	} else {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	aclRepo := storage.NewAclRepo(db)
	usersRepo := storage.NewUserRepo(db)
	boardsRepo := storage.NewBoardRepo(db)
	bootstrapRepo := storage.NewBootstrapRepo(db)

	mux := http.NewServeMux()
	// BoardsAPI für den ACLGuard-Middleware-Pfad.
	api.NewBoardsAPI(boardsRepo).WithACL(aclRepo).Register(mux)
	api.RegisterKnownUsersHandler(mux, aclRepo)

	guard := api.NewACLGuard(aclRepo, usersRepo, bootstrapRepo)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)

	return &knownUsersTestEnv{
		srv:    httptest.NewServer(h),
		db:     db,
		acl:    aclRepo,
		users:  usersRepo,
		boards: boardsRepo,
	}
}

// seedBoard legt ein Test-Board mit der gegebenen ID an (FK-Voraussetzung für acl.Set).
func (e *knownUsersTestEnv) seedBoard(t *testing.T, id string) {
	t.Helper()
	if _, err := e.boards.Insert(context.Background(), id, "Board "+id, "2", sampleBody(id, "Board "+id)); err != nil {
		t.Fatalf("seedBoard %s: %v", id, err)
	}
}

// doKU sendet GET /known-users mit optionaler Forward-Auth-Email.
func (e *knownUsersTestEnv) doKU(t *testing.T, email string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, e.srv.URL+"/known-users", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	return mustDo(t, r)
}

// ── Hilfsfunktion: JSON-Decode ───────────────────────────────────────────────

type knownUsersResp struct {
	Users []struct {
		Email  string `json:"email"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"users"`
}

func decodeKUResp(t *testing.T, resp *http.Response) knownUsersResp {
	t.Helper()
	var r knownUsersResp
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &r); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	return r
}

// ── Forward-Auth aktiv: aktiv + ausstehend gemischt ──────────────────────────

func TestKnownUsers_ForwardAuth_AktivUndAusstehend(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()

	ctx := context.Background()
	// Board anlegen (FK-Voraussetzung für acl.Set).
	env.seedBoard(t, "board-x")
	// Nutzer, der sich eingeloggt hat → users-Row vorhanden.
	_, err := env.users.Upsert(ctx, "uid-1", "aktiv@example.com", "Aktiver User")
	if err != nil {
		t.Fatalf("Upsert aktiv: %v", err)
	}
	// Vorab-geteilte Email: nur in acl, nie eingeloggt.
	if err := env.acl.Set(ctx, "board-x", "ausstehend@example.com", storage.RoleViewer); err != nil {
		t.Fatalf("acl.Set ausstehend: %v", err)
	}
	// Aktiver User auch in acl (für ein anderes Board).
	if err := env.acl.Set(ctx, "board-x", "aktiv@example.com", storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set aktiv: %v", err)
	}

	resp := env.doKU(t, "aktiv@example.com")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /known-users = %d, erwartet 200", resp.StatusCode)
	}
	r := decodeKUResp(t, resp)
	if len(r.Users) != 2 {
		t.Fatalf("Users-Anzahl = %d, erwartet 2", len(r.Users))
	}
	// Sortierung nach email → aktiv@example.com zuerst.
	if r.Users[0].Email != "aktiv@example.com" {
		t.Errorf("Users[0].Email = %q, erwartet aktiv@example.com", r.Users[0].Email)
	}
	if r.Users[0].Status != "aktiv" {
		t.Errorf("Users[0].Status = %q, erwartet aktiv", r.Users[0].Status)
	}
	if r.Users[0].Name != "Aktiver User" {
		t.Errorf("Users[0].Name = %q, erwartet 'Aktiver User'", r.Users[0].Name)
	}
	if r.Users[1].Email != "ausstehend@example.com" {
		t.Errorf("Users[1].Email = %q, erwartet ausstehend@example.com", r.Users[1].Email)
	}
	if r.Users[1].Status != "ausstehend" {
		t.Errorf("Users[1].Status = %q, erwartet ausstehend", r.Users[1].Status)
	}
	if r.Users[1].Name != "" {
		t.Errorf("Users[1].Name = %q, erwartet leer (nie eingeloggt)", r.Users[1].Name)
	}
}

// ── Forward-Auth aktiv: leere Email → 403 ───────────────────────────────────

func TestKnownUsers_ForwardAuth_LeereEmail_403(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()

	// Request mit gesetztem Remote-User aber leerem Remote-Email.
	r, err := http.NewRequest(http.MethodGet, env.srv.URL+"/known-users", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	r.Header.Set("Remote-User", "jemand")
	// Remote-Email bewusst nicht gesetzt → nach withForwardAuth: id.Email = ""
	resp := mustDo(t, r)
	defer resp.Body.Close()
	// Bei leerem Remote-Email: id ist nil (withForwardAuth setzt id nur wenn
	// user != "" || email != ""). Da Remote-User gesetzt ist, wird id gesetzt,
	// aber Email ist "". Der Handler prüft NormalizeEmail(id.Email) == "" → 403.
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /known-users mit leerem Email = %d, erwartet 403", resp.StatusCode)
	}
}

// ── Legacy-Bypass: kein Forward-Auth → 200 + leere Liste ────────────────────

func TestKnownUsers_Legacy_200_LeersteListe(t *testing.T) {
	env := newKnownUsersTestEnv(t, false)
	defer env.close()

	resp := env.doKU(t, "") // kein Email-Header
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Legacy GET /known-users = %d, erwartet 200", resp.StatusCode)
	}
	r := decodeKUResp(t, resp)
	if len(r.Users) != 0 {
		t.Fatalf("Legacy users-Länge = %d, erwartet 0 (leere DB)", len(r.Users))
	}
}

// ── Nur users-Row, kein acl-Eintrag: trotzdem sichtbar ──────────────────────

func TestKnownUsers_NurUsersOhneACL_Sichtbar(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()

	ctx := context.Background()
	_, err := env.users.Upsert(ctx, "uid-2", "solo@example.com", "Solo User")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Keine acl-Einträge.

	resp := env.doKU(t, "solo@example.com")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /known-users = %d, erwartet 200", resp.StatusCode)
	}
	r := decodeKUResp(t, resp)
	if len(r.Users) != 1 {
		t.Fatalf("Users-Anzahl = %d, erwartet 1", len(r.Users))
	}
	if r.Users[0].Status != "aktiv" {
		t.Errorf("Status = %q, erwartet aktiv", r.Users[0].Status)
	}
}

// ── Duplikat-Freiheit: gleiche Email in mehreren acl-Zeilen → nur einmal ─────

func TestKnownUsers_DuplikatFrei(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()

	ctx := context.Background()
	// Boards anlegen (FK-Voraussetzung für acl.Set).
	env.seedBoard(t, "board-a")
	env.seedBoard(t, "board-b")
	// Selbe Email in zwei Board-ACLs → muss nur einmal erscheinen.
	if err := env.acl.Set(ctx, "board-a", "shared@example.com", storage.RoleViewer); err != nil {
		t.Fatalf("acl.Set board-a: %v", err)
	}
	if err := env.acl.Set(ctx, "board-b", "shared@example.com", storage.RoleEditor); err != nil {
		t.Fatalf("acl.Set board-b: %v", err)
	}

	resp := env.doKU(t, "shared@example.com")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /known-users = %d, erwartet 200", resp.StatusCode)
	}
	r := decodeKUResp(t, resp)
	if len(r.Users) != 1 {
		t.Fatalf("Users-Anzahl = %d, erwartet 1 (kein Duplikat)", len(r.Users))
	}
}

// ── F-738: die Nebenbedingung, an der eine Sicherheitsaussage hängt ────────

// `api/notifications.go` begründet ausführlich, warum `recipient_user_ids`
// NICHT gegen die Board-Mitgliedschaft geprüft wird: Ein Aufrufer kann
// fremde User-UUIDs gar nicht kennen, weil kein Endpunkt sie ausliefert.
// Das ist eine Bedingung über den REST der API — sie stand bisher nur als
// Kommentar da und war von nichts gedeckt. Baut jemand einen Endpunkt, der
// UUIDs herausgibt (Admin-Export, Aktivitäts-Ansicht, Debug-Route), wird
// jener Pfad scharf, OHNE dass in notifications.go eine Zeile fällt.
//
// Dieser Test macht die Bedingung an der Stelle prüfbar, an der sie zählt:
// `/known-users` ist der einzige Endpunkt, der FREMDE Nutzer aufzählt. (Bei
// `/me` wäre die eigene ID unkritisch — damit liesse sich nur man selbst
// adressieren.) Fällt der Test, ist F-738 keine theoretische Asymmetrie
// mehr — dann gehört `recipient_user_ids` gegen die Board-ACL geprüft,
// bevor der neue Endpunkt ausgeliefert wird.
func TestF738_KeinEndpunktLiefertUserIDsAus(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()

	ctx := context.Background()
	env.seedBoard(t, "board-f738")
	const geheimeID = "uid-9f3c1b7e-nur-intern"
	if _, err := env.users.Upsert(ctx, geheimeID, "wer@example.com", "Wer Auch Immer"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := env.acl.Set(ctx, "board-f738", "wer@example.com", storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}
	// Ein zweiter, fremder Nutzer — genau der Fall, dessen UUID ein
	// Angreifer bräuchte, um ihm eine Benachrichtigung unterzuschieben.
	const fremdeID = "uid-4a2d8c50-fremd"
	if _, err := env.users.Upsert(ctx, fremdeID, "fremd@example.com", "Fremder"); err != nil {
		t.Fatalf("Upsert fremd: %v", err)
	}
	if err := env.acl.Set(ctx, "board-f738", "fremd@example.com", storage.RoleViewer); err != nil {
		t.Fatalf("acl.Set fremd: %v", err)
	}

	resp := env.doKU(t, "wer@example.com")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /known-users = %d, erwartet 200", resp.StatusCode)
	}
	koerper := string(mustReadAll(t, resp.Body))
	for _, id := range []string{geheimeID, fremdeID} {
		if strings.Contains(koerper, id) {
			t.Errorf("GET /known-users enthält die User-ID %q — damit ist die "+
				"Begründung in api/notifications.go hinfällig und "+
				"`recipient_user_ids` muss gegen die Board-ACL geprüft "+
				"werden (F-738).\nAntwort: %s", id, koerper)
		}
	}
}
