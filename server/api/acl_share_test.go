package api_test

// acl_share_test.go — Tests für REST GET/POST/DELETE /boards/{id}/acl (#199, ADR-0059).
// Nutzt den ACL-Testharness aus acl_test.go (newACLTestEnv, seedBoard, do).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// ── GET /boards/{id}/acl ──────────────────────────────────────────────

func TestACLShare_GET_ListeEintraege(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)

	resp := env.do(t, http.MethodGet, "/boards/B/acl", "owner@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards/B/acl = %d, erwartet 200", resp.StatusCode)
	}
	var entries []map[string]any
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &entries); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Einträge = %d, erwartet 2", len(entries))
	}
}

func TestACLShare_GET_BoardNichtVorhanden_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	// Damit der globale ACL-Check (Invite-only) nicht blockiert:
	env.seedBoard(t, "X")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "X", "user@example.com", storage.RoleOwner)

	// Die ACL-Middleware gibt 403 zurück für Boards ohne ACL-Eintrag (ADR-0059 §1):
	// ein authentifizierter User mit globalem Zugriff, aber ohne ACL auf NICHTDA → 403.
	resp := env.do(t, http.MethodGet, "/boards/NICHTDA/acl", "user@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET nicht-existentes Board = %d, erwartet 403 (ACL-Middleware blockiert)", resp.StatusCode)
	}
}

// TestACLShare_GET_BoardNichtVorhanden_404_Legacy: Im Legacy-Modus kommt der Handler zu Wort,
// der dann 404 zurückgibt.
func TestACLShare_GET_BoardNichtVorhanden_404_Legacy(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()

	resp := env.do(t, http.MethodGet, "/boards/NICHTDA/acl", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Legacy GET nicht-existentes Board = %d, erwartet 404", resp.StatusCode)
	}
}

// F-261 (v1.20.1-Hotfix): aclList ist jetzt owner-only. Ein als viewer/editor
// eingeladener Account darf die komplette Mitglieder-/Email-Liste NICHT mehr
// lesen (PII-/Topologie-Leak). Vorher gab dieser Test fälschlich 200 zurück —
// er kodierte das unsichere Verhalten. Jetzt: Negativ-Test, viewer/editor → 403.
func TestACLShare_GET_ViewerEditor_403_F261(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)
	_ = env.acl.Set(ctx, "B", "editor@example.com", storage.RoleEditor)

	// viewer → 403 (Email-Harvesting verhindert)
	resp := env.do(t, http.MethodGet, "/boards/B/acl", "viewer@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer GET /acl = %d, erwartet 403 (F-261 owner-only)", resp.StatusCode)
	}

	// editor → ebenfalls 403 (nur owner darf)
	resp2 := env.do(t, http.MethodGet, "/boards/B/acl", "editor@example.com", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("editor GET /acl = %d, erwartet 403 (F-261 owner-only)", resp2.StatusCode)
	}

	// owner → 200 (Regression: der berechtigte Pfad bleibt offen)
	resp3 := env.do(t, http.MethodGet, "/boards/B/acl", "owner@example.com", nil)
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("owner GET /acl = %d, erwartet 200", resp3.StatusCode)
	}
}

// ── POST /boards/{id}/acl ─────────────────────────────────────────────

func TestACLShare_POST_OwnerDarf(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)

	body, _ := json.Marshal(map[string]string{"email": "neu@example.com", "role": "editor"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "owner@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner POST /acl = %d, erwartet 200", resp.StatusCode)
	}
	// Eintrag tatsächlich gesetzt?
	role, found, err := env.acl.GetRole(ctx, "B", "neu@example.com")
	if err != nil || !found || role != storage.RoleEditor {
		t.Fatalf("ACL nicht gesetzt: role=%q found=%v err=%v", role, found, err)
	}
}

func TestACLShare_POST_EditorWird403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "editor@example.com", storage.RoleEditor)

	body, _ := json.Marshal(map[string]string{"email": "neu@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "editor@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("editor POST /acl = %d, erwartet 403", resp.StatusCode)
	}
}

func TestACLShare_POST_ViewerWird403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)

	body, _ := json.Marshal(map[string]string{"email": "neu@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "viewer@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer POST /acl = %d, erwartet 403", resp.StatusCode)
	}
}

func TestACLShare_POST_UngueltigeRolle_400(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)

	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "role": "superadmin"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "owner@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ungültige Rolle = %d, erwartet 400", resp.StatusCode)
	}
}

func TestACLShare_POST_BoardNichtVorhanden_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "X")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "X", "owner@example.com", storage.RoleOwner)

	// ACL-Middleware blockiert mit 403 (kein ACL-Eintrag für NICHTDA).
	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/NICHTDA/acl", "owner@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("nicht-existentes Board POST /acl = %d, erwartet 403 (ACL-Middleware)", resp.StatusCode)
	}
}

func TestACLShare_POST_BoardNichtVorhanden_404_Legacy(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()

	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/NICHTDA/acl", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Legacy nicht-existentes Board POST /acl = %d, erwartet 404", resp.StatusCode)
	}
}

func TestACLShare_POST_LetzterOwnerDemotion_409(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)

	// Versuch: letzten owner auf viewer downgraden → 409
	body, _ := json.Marshal(map[string]string{"email": "owner@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "owner@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("Last-Owner-Demotion POST /acl = %d, erwartet 409", resp.StatusCode)
	}
}

// ── DELETE /boards/{id}/acl?email=... ────────────────────────────────

func TestACLShare_DELETE_OwnerEntferntAnderen(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)

	resp := env.do(t, http.MethodDelete, "/boards/B/acl?email=viewer@example.com", "owner@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("owner DELETE /acl = %d, erwartet 204", resp.StatusCode)
	}
	// Eintrag weg?
	_, found, _ := env.acl.GetRole(ctx, "B", "viewer@example.com")
	if found {
		t.Fatalf("viewer-Eintrag nach DELETE noch vorhanden")
	}
}

func TestACLShare_DELETE_EditorWird403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "editor@example.com", storage.RoleEditor)

	resp := env.do(t, http.MethodDelete, "/boards/B/acl?email=editor@example.com", "editor@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("editor DELETE /acl = %d, erwartet 403", resp.StatusCode)
	}
}

func TestACLShare_DELETE_ViewerWird403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)

	resp := env.do(t, http.MethodDelete, "/boards/B/acl?email=viewer@example.com", "viewer@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer DELETE /acl = %d, erwartet 403", resp.StatusCode)
	}
}

func TestACLShare_DELETE_LetzterOwner_409(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)

	resp := env.do(t, http.MethodDelete, "/boards/B/acl?email=owner@example.com", "owner@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("Last-Owner DELETE /acl = %d, erwartet 409", resp.StatusCode)
	}
}

func TestACLShare_DELETE_BoardNichtVorhanden_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "X")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "X", "owner@example.com", storage.RoleOwner)

	// ACL-Middleware blockiert mit 403 (kein ACL-Eintrag für NICHTDA).
	resp := env.do(t, http.MethodDelete, "/boards/NICHTDA/acl?email=owner@example.com", "owner@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("nicht-existentes Board DELETE /acl = %d, erwartet 403 (ACL-Middleware)", resp.StatusCode)
	}
}

func TestACLShare_DELETE_BoardNichtVorhanden_404_Legacy(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()

	resp := env.do(t, http.MethodDelete, "/boards/NICHTDA/acl?email=owner@example.com", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Legacy nicht-existentes Board DELETE /acl = %d, erwartet 404", resp.StatusCode)
	}
}

// ── Legacy-Modus (trust=false) erlaubt Mutationen ─────────────────────

func TestACLShare_Legacy_MutationErlaubt(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	env.seedBoard(t, "B")

	// POST ohne Forward-Auth-Identität → Legacy-Bypass → erlaubt
	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "" /* kein email-Header */, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Legacy POST /acl = %d, erwartet 200 (Bypass)", resp.StatusCode)
	}

	// DELETE im Legacy-Modus
	resp2 := env.do(t, http.MethodDelete, "/boards/B/acl?email=x@example.com", "", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("Legacy DELETE /acl = %d, erwartet 204 (Bypass)", resp2.StatusCode)
	}
}

// ── GET ohne Forward-Auth (Legacy) ────────────────────────────────────

func TestACLShare_Legacy_GET_Erlaubt(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	env.seedBoard(t, "B")

	resp := env.do(t, http.MethodGet, "/boards/B/acl", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Legacy GET /acl = %d, erwartet 200", resp.StatusCode)
	}
}
