package api_test

// security_hotfix_test.go — Negativ-Tests für den v1.20.1 Security-Hotfix
// (Karte #211): F-260 (Email-Normalisierung end-to-end), F-261 (aclList
// owner-only + Regression aclSet/aclDelete), F-262 (Shared-Secret-Middleware).
// Nutzt den ACL-Testharness aus acl_test.go (newACLTestEnv, seedBoard, do).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ── F-260: Email-Normalisierung über die volle HTTP-Kette ─────────────────

// TestF260_CaseInsensitive_BoardZugriff — ACL `owner@x.de`, Identitäts-Email
// `  Owner@X.de ` (Authelia-Casing-Varianz) → Zugriff MUSS gewährt werden.
// Vor dem Fix: 403 (Owner-Lockout).
func TestF260_CaseInsensitive_BoardZugriff(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "A")
	ctx := context.Background()
	if err := env.acl.Set(ctx, "A", "owner@x.de", storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}

	// GET /boards mit ungetrimmter/Upper-Email → 200 + Board sichtbar.
	resp := env.do(t, http.MethodGet, "/boards", "  Owner@X.de ", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards als ' Owner@X.de ' = %d, erwartet 200 (F-260 Owner-Lockout)", resp.StatusCode)
	}
	var list []map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &list)
	// ADR-0066: Erstkontakt legt zusätzlich ein persönliches Board an →
	// owned∪shared = {A (case-insensitiver ACL-Treffer), Personal-Board}.
	// Kern dieses Tests bleibt: A IST sichtbar (F-260, kein Owner-Lockout).
	foundA := false
	for _, e := range list {
		if e["id"] == "A" {
			foundA = true
		}
	}
	if len(list) != 2 || !foundA {
		t.Fatalf("Liste = %v, erwartet A + Personal-Board (case-insensitiver ACL-Treffer auf A)", list)
	}

	// GET /boards/A als owner mit Casing-Varianz → 200.
	r2 := env.do(t, http.MethodGet, "/boards/A", "OWNER@X.DE", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards/A als OWNER@X.DE = %d, erwartet 200", r2.StatusCode)
	}
}

// TestF260_aclSet_SchreibtNormalisiert — owner setzt ACL mit ungetrimmter
// Upper-Email; gespeichert wird die normalisierte Form, und ein Lookup mit
// noch anderem Casing trifft.
func TestF260_aclSet_SchreibtNormalisiert(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)

	body, _ := json.Marshal(map[string]string{"email": "  New.User@EXAMPLE.com ", "role": "editor"})
	resp := env.do(t, http.MethodPost, "/boards/B/acl", "owner@example.com", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner POST /acl = %d, erwartet 200", resp.StatusCode)
	}
	// Gespeichert in kanonischer Form?
	role, found, err := env.acl.GetRole(ctx, "B", "new.user@example.com")
	if err != nil || !found || role != storage.RoleEditor {
		t.Fatalf("ACL nicht normalisiert gespeichert: role=%q found=%v err=%v", role, found, err)
	}
	// Der frisch eingetragene Nutzer kommt mit eigener Casing-Varianz rein.
	r2 := env.do(t, http.MethodGet, "/boards/B", "NEW.USER@example.COM", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("neuer Editor GET /boards/B = %d, erwartet 200 (F-260)", r2.StatusCode)
	}
}

// ── F-261: aclList owner-only + Regression aclSet/aclDelete nicht-owner ────

// TestF261_aclList_NurOwner — viewer/editor 403, owner 200 (Kern-Fix).
func TestF261_aclList_NurOwner(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)
	_ = env.acl.Set(ctx, "B", "editor@example.com", storage.RoleEditor)

	for _, tc := range []struct {
		email string
		want  int
	}{
		{"viewer@example.com", http.StatusForbidden},
		{"editor@example.com", http.StatusForbidden},
		{"owner@example.com", http.StatusOK},
	} {
		resp := env.do(t, http.MethodGet, "/boards/B/acl", tc.email, nil)
		if resp.StatusCode != tc.want {
			resp.Body.Close()
			t.Fatalf("GET /boards/B/acl als %s = %d, erwartet %d (F-261)", tc.email, resp.StatusCode, tc.want)
		}
		resp.Body.Close()
	}
}

// TestF261_Regression_aclSetDelete_NichtOwner_403 — die bestehenden owner-
// Guards für aclSet/aclDelete dürfen durch den Hotfix nicht aufweichen.
func TestF261_Regression_aclSetDelete_NichtOwner_403(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	ctx := context.Background()
	_ = env.acl.Set(ctx, "B", "owner@example.com", storage.RoleOwner)
	_ = env.acl.Set(ctx, "B", "viewer@example.com", storage.RoleViewer)

	body, _ := json.Marshal(map[string]string{"email": "x@example.com", "role": "viewer"})
	if r := env.do(t, http.MethodPost, "/boards/B/acl", "viewer@example.com", body); r.StatusCode != http.StatusForbidden {
		r.Body.Close()
		t.Fatalf("viewer POST /acl = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if r := env.do(t, http.MethodDelete, "/boards/B/acl?email=owner@example.com", "viewer@example.com", nil); r.StatusCode != http.StatusForbidden {
		r.Body.Close()
		t.Fatalf("viewer DELETE /acl = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

// TestF261_Legacy_aclList_Erlaubt — Legacy-Bypass (Trust=AUS) muss exakt
// erhalten bleiben: ohne Identität kein owner-Konzept → GET /acl = 200.
func TestF261_Legacy_aclList_Erlaubt(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	env.seedBoard(t, "B")

	resp := env.do(t, http.MethodGet, "/boards/B/acl", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Legacy GET /acl = %d, erwartet 200 (Bypass unverändert)", resp.StatusCode)
	}
}

// ── F-262 (a): optionales Shared-Secret in der Forward-Auth-Middleware ────

// TestF262_Secret_FalschOderFehlend_401 — ist ABOARD_FORWARD_AUTH_SECRET
// gesetzt, wird ein Request ohne (oder mit falschem)
// X-Aboard-Forward-Auth-Secret mit 401 abgewiesen, der Handler läuft nicht.
// Bis Sprint 70 wurde nur die Identität verworfen und der Request lief
// anonym weiter — in den Legacy-Bypass (F-1124, ADR-0129 §2).
func TestF262_Secret_FalschOderFehlend_401(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")

	for _, fall := range []struct{ name, secret string }{
		{"ohne Secret-Header", ""},
		{"falsches Secret", "falsch"},
	} {
		t.Run(fall.name, func(t *testing.T) {
			cap := &captureHandler{}
			wrapped := api.WithForwardAuthIfEnabled(cap)
			req := httptest.NewRequest(http.MethodGet, "/boards", nil)
			req.Header.Set("Remote-Email", "spoof@firma.de")
			if fall.secret != "" {
				req.Header.Set("X-Aboard-Forward-Auth-Secret", fall.secret)
			}
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("Status = %d, erwartet 401 (F-1124)", rr.Code)
			}
			if cap.lastReq != nil {
				t.Fatalf("Handler lief trotz %s", fall.name)
			}
		})
	}

	// Korrektes Secret → Identität aktiv.
	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)
	req3 := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req3.Header.Set("Remote-Email", "real@firma.de")
	req3.Header.Set("X-Aboard-Forward-Auth-Secret", "geheim123")
	rr3 := httptest.NewRecorder()
	wrapped.ServeHTTP(rr3, req3)
	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil || id.Email != "real@firma.de" {
		t.Fatalf("Identität bei korrektem Secret = %+v, erwartet real@firma.de (F-262)", id)
	}
}

// TestF262_Secret_NichtGesetzt_BackwardCompat — ist die Env NICHT gesetzt,
// verhält sich die Middleware exakt wie vor dem Hotfix (kein Secret nötig).
func TestF262_Secret_NichtGesetzt_BackwardCompat(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	// ABOARD_FORWARD_AUTH_SECRET bewusst NICHT gesetzt.

	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-Email", "alice@example.com")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil || id.Email != "alice@example.com" {
		t.Fatalf("Identität ohne Secret-Env = %+v, erwartet alice@example.com (Backward-Compat)", id)
	}
}
