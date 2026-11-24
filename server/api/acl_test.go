package api_test

// acl_test.go — ACL-Enforcement (#198, ADR-0059 §1/§2).

import (
	"bytes"
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

type aclTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	users  *storage.UserRepo
}

func (e *aclTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

// newACLTestEnv baut die volle Kette ForwardAuth → ACLGuard → mux.
// trust=true setzt ABOARD_TRUST_FORWARD_AUTH (Remote-*-Header werden gelesen);
// trust=false = Legacy (Header ignoriert, Bypass).
func newACLTestEnv(t *testing.T, trust bool) *aclTestEnv {
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
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)

	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)

	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)

	return &aclTestEnv{
		srv: httptest.NewServer(h), db: db,
		boards: boards, acl: acl, users: users,
	}
}

func (e *aclTestEnv) seedBoard(t *testing.T, id string) {
	t.Helper()
	if _, err := e.boards.Insert(context.Background(), id, "Board "+id, "2", sampleBody(id, "Board "+id)); err != nil {
		t.Fatalf("seed board %s: %v", id, err)
	}
}

// do baut einen Request mit optionaler Forward-Auth-Identität (Remote-*).
func (e *aclTestEnv) do(t *testing.T, method, path, email string, body []byte) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	return mustDo(t, r)
}

// ── Legacy-Bypass (ADR-0059 §2) ───────────────────────────────────────────

func TestACL_LegacyBypass_OhneForwardAuth(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	env.seedBoard(t, "A")

	// Header gesetzt, aber Trust AUS → Identität nil → Bypass, kein 403.
	resp := env.do(t, http.MethodGet, "/boards", "ghost@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Legacy GET /boards = %d, erwartet 200 (Bypass)", resp.StatusCode)
	}
	// Liste unfiltered: A ist sichtbar.
	var list []map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &list)
	if len(list) != 1 {
		t.Fatalf("Legacy-Liste len=%d, erwartet 1 (unfiltered)", len(list))
	}
}

// ── ADR-0066: Open-to-authenticated — Erstkontakt → Personal-Board ────────
//
// Ersetzt den alten Invite-only-Test (keine ACL → 403, kein User-Row).
// ADR-0066 revidiert ADR-0059 §1: ein brandneuer Authelia-Nutzer ohne JEDE
// ACL bekommt KEIN 403 mehr, sondern beim Erstkontakt atomar users-Row +
// persönliches Board (owner) — und sieht ab da genau dieses eine Board.

func TestACL_Erstkontakt_PersonalBoardBootstrap(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A") // fremdes Board, NICHT mit nobody geteilt

	resp := env.do(t, http.MethodGet, "/boards", "nobody@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards Erstkontakt = %d, erwartet 200 (kein Invite-Gate mehr)", resp.StatusCode)
	}
	var list []map[string]any
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &list); err != nil {
		t.Fatalf("json: %v", err)
	}
	// Genau das persönliche Board — NICHT das fremde Board A.
	if len(list) != 1 {
		t.Fatalf("Liste len=%d, erwartet 1 (nur Personal-Board, owned∪shared)", len(list))
	}
	if list[0]["id"] == "A" {
		t.Fatalf("Erstnutzer sieht fremdes Board A — ACL-Filter verletzt")
	}
	personalID, _ := list[0]["id"].(string)

	ctx := context.Background()
	// users-Row materialisiert.
	if _, err := env.users.GetByEmail(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("users-Row nicht angelegt (Bootstrap verletzt): %v", err)
	}
	// owner-ACL auf dem persönlichen Board.
	role, found, err := env.acl.GetRole(ctx, personalID, "nobody@example.com")
	if err != nil || !found || role != storage.RoleOwner {
		t.Fatalf("Personal-Board-ACL = (%q,%v,%v), erwartet owner", role, found, err)
	}
	// Board ist abrufbar + kanonisch leer (ADR-0052).
	b, err := env.boards.Get(ctx, personalID)
	if err != nil {
		t.Fatalf("Personal-Board nicht abrufbar: %v", err)
	}
	if string(b.StateJSON) != string(storage.CanonicalEmptyBoardState(personalID, "Board")) {
		t.Fatalf("Personal-Board-State weicht vom kanonischen leeren Board ab:\n%s", b.StateJSON)
	}
}

// Kein Resurrect (ADR-0066 §1): löscht der Nutzer sein Personal-Board, wird
// es beim nächsten Request NICHT neu angelegt (Trigger = users-Row neu).
func TestACL_KeinResurrectNachLoeschung(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	ctx := context.Background()

	// Erstkontakt → Personal-Board.
	r1 := env.do(t, http.MethodGet, "/boards", "u@example.com", nil)
	r1.Body.Close()
	var l1 []map[string]any
	resp := env.do(t, http.MethodGet, "/boards", "u@example.com", nil)
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &l1)
	resp.Body.Close()
	if len(l1) != 1 {
		t.Fatalf("nach Erstkontakt len=%d, erwartet 1", len(l1))
	}
	personalID := l1[0]["id"].(string)

	// Nutzer löscht sein Board (DELETE als owner).
	dresp := env.do(t, http.MethodDelete, "/boards/"+personalID, "u@example.com", nil)
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent && dresp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE Personal-Board = %d", dresp.StatusCode)
	}

	// Nächster Request: KEIN neues Board (users-Row existiert weiter).
	resp2 := env.do(t, http.MethodGet, "/boards", "u@example.com", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET nach Lösch = %d, erwartet 200", resp2.StatusCode)
	}
	var l2 []map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp2.Body), &l2)
	if len(l2) != 0 {
		t.Fatalf("Board resurrektiert (len=%d) — Trigger ist users-Row neu, nicht 0 Boards", len(l2))
	}
	if _, err := env.users.GetByEmail(ctx, "u@example.com"); err != nil {
		t.Fatalf("users-Row verschwand bei Board-Lösch: %v", err)
	}
}

func TestACL_ForwardAuthOhneEmail_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A")

	r, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/boards", nil)
	r.Header.Set("Remote-User", "user-ohne-email") // kein Remote-Email
	resp := mustDo(t, r)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Identität ohne Email = %d, erwartet 403", resp.StatusCode)
	}
}

// ── Liste = owned ∪ shared (ADR-0066 §2) ──────────────────────────────────
//
// Geteiltes Board A (viewer) ∪ persönliches Bootstrap-Board (owner). Das
// fremde Board B bleibt unsichtbar (kein ACL-Eintrag). Ersetzt die alte
// „nur Board A"-Erwartung (vor ADR-0066 gab es kein Personal-Board).

func TestACL_ListeOwnedUnionShared(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A")
	env.seedBoard(t, "B")
	ctx := context.Background()
	if err := env.acl.Set(ctx, "A", "u@example.com", storage.RoleViewer); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}

	resp := env.do(t, http.MethodGet, "/boards", "u@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards = %d, erwartet 200", resp.StatusCode)
	}
	var list []map[string]any
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &list); err != nil {
		t.Fatalf("json: %v", err)
	}
	// Erwartet: A (shared, viewer) + persönliches Board (owner) = 2.
	if len(list) != 2 {
		t.Fatalf("Liste len=%d, erwartet 2 (A shared ∪ Personal-Board owned): %v", len(list), list)
	}
	ids := map[string]bool{}
	for _, e := range list {
		ids[e["id"].(string)] = true
	}
	if !ids["A"] {
		t.Fatalf("Board A (geteilt) fehlt in der Liste: %v", list)
	}
	if ids["B"] {
		t.Fatalf("Fremdes Board B sichtbar — ACL-Filter verletzt: %v", list)
	}
	// Genau ein eigenes (owner) Board, und das ist NICHT A (auf A nur viewer).
	owned, err := env.acl.ListBoardsWithRoleForUser(ctx, "u@example.com")
	if err != nil {
		t.Fatalf("ListBoardsWithRoleForUser: %v", err)
	}
	var ownerBoards []string
	for _, e := range owned {
		if e.Role == storage.RoleOwner {
			ownerBoards = append(ownerBoards, e.BoardID)
		}
	}
	if len(ownerBoards) != 1 || ownerBoards[0] == "A" {
		t.Fatalf("owner-Boards = %v, erwartet genau 1 (Personal-Board, nicht A)", ownerBoards)
	}
	// users-Row materialisiert.
	if _, err := env.users.GetByEmail(ctx, "u@example.com"); err != nil {
		t.Fatalf("users-Row nicht materialisiert: %v", err)
	}
}

// ── Rollen-Enforcement auf /boards/{id} ───────────────────────────────────

func TestACL_RollenEnforcement(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "A", "v@example.com", storage.RoleViewer)
	_ = env.acl.Set(ctx, "A", "e@example.com", storage.RoleEditor)
	_ = env.acl.Set(ctx, "A", "o@example.com", storage.RoleOwner)

	// viewer: GET ok, PUT/DELETE 403.
	if r := env.do(t, http.MethodGet, "/boards/A", "v@example.com", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET = %d, erwartet 200", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if r := env.do(t, http.MethodPut, "/boards/A", "v@example.com", sampleBody("A", "x")); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if r := env.do(t, http.MethodDelete, "/boards/A", "v@example.com", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer DELETE = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// editor: PUT von ACL erlaubt → Handler verlangt If-Match → 428
	// (NICHT 403). Beweist: ACL hat durchgelassen.
	if r := env.do(t, http.MethodPut, "/boards/A", "e@example.com", sampleBody("A", "x")); r.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("editor PUT = %d, erwartet 428 (ACL erlaubt, Handler verlangt If-Match)", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if r := env.do(t, http.MethodDelete, "/boards/A", "e@example.com", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("editor DELETE = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// owner: DELETE erfolgreich (204).
	if r := env.do(t, http.MethodDelete, "/boards/A", "o@example.com", nil); r.StatusCode != http.StatusNoContent {
		t.Fatalf("owner DELETE = %d, erwartet 204", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

func TestACL_FremdesBoard_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A")
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "A", "u@example.com", storage.RoleOwner)

	// Hat ACL auf A (globaler Zugriff ok), aber nicht auf B → 403.
	r := env.do(t, http.MethodGet, "/boards/B", "u@example.com", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("GET fremdes Board B = %d, erwartet 403", r.StatusCode)
	}
}

// ── Create → Ersteller wird owner (ADR-0059 §1) ───────────────────────────

func TestACL_Create_ErstellerWirdOwner(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "Seed")
	ctx := context.Background()
	// User braucht globalen Zugriff (Invite-only) um POST zu dürfen.
	_ = env.acl.Set(ctx, "Seed", "c@example.com", storage.RoleViewer)

	resp := env.do(t, http.MethodPost, "/boards", "c@example.com", sampleBody("NEU", "Neu"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /boards = %d, erwartet 201", resp.StatusCode)
	}
	role, found, err := env.acl.GetRole(ctx, "NEU", "c@example.com")
	if err != nil || !found || role != storage.RoleOwner {
		t.Fatalf("Ersteller nicht owner: role=%q found=%v err=%v", role, found, err)
	}
	// Folge-Zugriff auf das neue Board als owner klappt.
	if r := env.do(t, http.MethodGet, "/boards/NEU", "c@example.com", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards/NEU als owner = %d, erwartet 200", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

// F-264 (#244): Der kompensierende Rollback-Delete im create-Handler darf
// niemals ein FREMDES, bereits bestehendes Board hart löschen. Verifizierte
// Semantik: BoardRepo.Insert ist ein schlichtes `INSERT INTO` → eine
// id-Kollision liefert eine UNIQUE-Verletzung, die der Handler VOR dem
// Owner-Grant/Delete-Block als 409 abfängt (isUniqueViolation). Der
// Delete-Pfad ist damit bei Kollision strukturell unerreichbar. Dieser Test
// nagelt die Invariante fest: kollidiert ein ANDERER Nutzer auf eine
// bestehende id, bleiben Board UND dessen Owner-ACL unversehrt. Bricht ein
// künftiges Refactor das (z. B. `INSERT OR REPLACE`), schlägt er fehl.
func TestBoardsCreate_DuplicateID_DifferentUser_KeinFremdLoesch(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	ctx := context.Background()

	// Nutzer A legt Board "X" an → 201, A wird owner.
	if r := env.do(t, http.MethodPost, "/boards", "a@example.com", sampleBody("X", "A-Board")); r.StatusCode != http.StatusCreated {
		t.Fatalf("A POST /boards = %d, erwartet 201", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if role, found, err := env.acl.GetRole(ctx, "X", "a@example.com"); err != nil || !found || role != storage.RoleOwner {
		t.Fatalf("A nicht owner von X: role=%q found=%v err=%v", role, found, err)
	}

	// Nutzer B kollidiert auf dieselbe id "X" → muss 409 sein (UNIQUE →
	// isUniqueViolation, VOR dem Owner-Grant/Rollback-Delete-Block).
	if r := env.do(t, http.MethodPost, "/boards", "b@example.com", sampleBody("X", "B-Versuch")); r.StatusCode != http.StatusConflict {
		t.Fatalf("B kollidierender POST = %d, erwartet 409", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// F-264-Kern: A's Board lebt unverändert + A ist weiterhin owner
	// (kein kompensierender Delete hat das Fremd-Board genukt).
	r := env.do(t, http.MethodGet, "/boards/X", "a@example.com", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards/X als A = %d, erwartet 200 (Fremd-Board überlebt Kollision)", r.StatusCode)
	}
	body := mustReadAll(t, r.Body)
	if !bytes.Contains(body, []byte("A-Board")) {
		t.Fatalf("X-Inhalt nicht mehr A's Board: %s", body)
	}
	if role, found, err := env.acl.GetRole(ctx, "X", "a@example.com"); err != nil || !found || role != storage.RoleOwner {
		t.Fatalf("A's Owner-ACL nach Kollision verloren: role=%q found=%v err=%v", role, found, err)
	}
	if _, found, _ := env.acl.GetRole(ctx, "X", "b@example.com"); found {
		t.Fatalf("B hat unerwartet eine Rolle auf X erhalten")
	}
}
