package api_test

// acl_domain_test.go — Board-Freigabe für eine E-Mail-Domain über die REST-Kette
// ForwardAuth → ACLGuard → Handler (ADR-0132, Karte #653).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func postACL(t *testing.T, env *aclTestEnv, board, caller, email, role string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "role": role})
	resp := env.do(t, http.MethodPost, "/boards/"+board+"/acl", caller, body)
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestACLDomain_POST_NurMitBetreiberListe(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "TEAM")
	_ = env.acl.Set(context.Background(), "TEAM", "owner@firma.de", storage.RoleOwner)

	// Gegenprobe: ohne ABOARD_ACL_DOMAINS bleibt alles beim Alten.
	t.Setenv(storage.ACLDomainsEnv, "")
	if got := postACL(t, env, "TEAM", "owner@firma.de", "@firma.de", "viewer"); got != http.StatusBadRequest {
		t.Fatalf("POST @firma.de ohne Liste = %d, erwartet 400", got)
	}

	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	if got := postACL(t, env, "TEAM", "owner@firma.de", "@firma.de", "owner"); got != http.StatusBadRequest {
		t.Fatalf("POST @firma.de als owner = %d, erwartet 400", got)
	}
	if got := postACL(t, env, "TEAM", "owner@firma.de", "@andere.de", "viewer"); got != http.StatusBadRequest {
		t.Fatalf("POST nicht erlaubte Domain = %d, erwartet 400", got)
	}
	if got := postACL(t, env, "TEAM", "owner@firma.de", "@firma.de", "editor"); got != http.StatusOK {
		t.Fatalf("POST @firma.de editor = %d, erwartet 200", got)
	}
	// Nur der Owner darf — auch Domain-Mitglieder mit editor nicht.
	if got := postACL(t, env, "TEAM", "kollege@firma.de", "@firma.de", "viewer"); got != http.StatusForbidden {
		t.Fatalf("POST durch Domain-editor = %d, erwartet 403", got)
	}
}

func TestACLDomain_NeueSiehtFreigegebenesBoard(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "TEAM")
	ctx := context.Background()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	_ = env.acl.Set(ctx, "TEAM", "owner@firma.de", storage.RoleOwner)
	if err := env.acl.Set(ctx, "TEAM", "@firma.de", storage.RoleViewer); err != nil {
		t.Fatalf("Set @firma.de: %v", err)
	}

	// Erstkontakt: eigenes Board entsteht (ADR-0066), TEAM ist zusätzlich sichtbar.
	resp := env.do(t, http.MethodGet, "/boards", "neu@firma.de", nil)
	var list []map[string]any
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &list); err != nil {
		t.Fatalf("json: %v", err)
	}
	resp.Body.Close()
	ids := map[string]bool{}
	for _, e := range list {
		ids[e["id"].(string)] = true
	}
	if !ids["TEAM"] || len(list) != 2 {
		t.Fatalf("Liste für neu@firma.de = %v, erwartet TEAM + eigenes Board", list)
	}

	if r := env.do(t, http.MethodGet, "/boards/TEAM", "neu@firma.de", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards/TEAM als Domain-viewer = %d, erwartet 200", r.StatusCode)
	} else {
		r.Body.Close()
	}
	// viewer darf nicht schreiben.
	if r := env.do(t, http.MethodPut, "/boards/TEAM", "neu@firma.de", []byte(`{}`)); r.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT /boards/TEAM als Domain-viewer = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}
	// Fremde Domain: kein Zugriff.
	if r := env.do(t, http.MethodGet, "/boards/TEAM", "x@andere.de", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /boards/TEAM als fremde Domain = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// Betreiber schaltet ab → sofort 403, ohne Zeilen zu löschen.
	t.Setenv(storage.ACLDomainsEnv, "")
	if r := env.do(t, http.MethodGet, "/boards/TEAM", "neu@firma.de", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /boards/TEAM nach Abschalten = %d, erwartet 403", r.StatusCode)
	} else {
		r.Body.Close()
	}
}

func TestKnownUsers_LiefertErlaubteDomains(t *testing.T) {
	env := newKnownUsersTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "B")
	t.Setenv(storage.ACLDomainsEnv, "firma.de, x.org")
	_ = env.acl.Set(context.Background(), "B", "@firma.de", storage.RoleViewer)

	resp := env.doKU(t, "a@firma.de")
	defer resp.Body.Close()
	var body struct {
		Users   []map[string]any `json:"users"`
		Domains []string         `json:"domains"`
	}
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Domains) != 2 || body.Domains[0] != "firma.de" || body.Domains[1] != "x.org" {
		t.Fatalf("domains = %v, erwartet [firma.de x.org]", body.Domains)
	}
	for _, u := range body.Users {
		if u["email"] == "@firma.de" {
			t.Fatalf("Domain-Eintrag als Person im Verzeichnis: %v", body.Users)
		}
	}

	t.Setenv(storage.ACLDomainsEnv, "")
	resp2 := env.doKU(t, "a@firma.de")
	defer resp2.Body.Close()
	body.Domains = nil
	_ = json.Unmarshal(mustReadAll(t, resp2.Body), &body)
	if body.Domains == nil || len(body.Domains) != 0 {
		t.Fatalf("ohne Liste: domains = %#v, erwartet leeres Array (nicht null)", body.Domains)
	}
}

// F-1511 (Kritiker Sprint 82, K-1): Eine Identität "@<domain>" ist ein
// Domain-Schlüssel, keine Person — der Server weist sie ab, bevor der
// Erstkontakt ("@firma.de", owner) anlegen kann.
func TestACLDomain_IdentitaetMitAtAmAnfang(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	for _, ident := range []string{"@firma.de", " @Firma.DE "} {
		resp := env.do(t, http.MethodGet, "/boards", ident, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("GET /boards als %q = %d, erwartet 403", ident, resp.StatusCode)
		}
	}
	if _, err := env.users.GetByEmail(context.Background(), "@firma.de"); err == nil {
		t.Fatal("users-Zeile für @firma.de angelegt")
	}
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM acl WHERE user_email = '@firma.de'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("acl-Zeilen für @firma.de: %d %v", n, err)
	}
}

// F-1514 (Kritiker Sprint 82, K-4): Am Service-Board keine Domain-Freigabe.
func TestACLDomain_ServiceBoardAusgenommen(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	ctx := context.Background()
	if _, err := env.boards.Get(ctx, storage.ServiceBoardID); err != nil {
		env.seedBoard(t, storage.ServiceBoardID)
	}
	if err := env.acl.Set(ctx, storage.ServiceBoardID, "admin@firma.de", storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"email": "@firma.de", "role": "viewer"})
	resp := env.do(t, http.MethodPost, "/boards/"+storage.ServiceBoardID+"/acl", "admin@firma.de", body)
	defer resp.Body.Close()
	text := string(mustReadAll(t, resp.Body))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(text, "Service-Board") {
		t.Fatalf("POST @firma.de am Service-Board = %d %q, erwartet 400 mit Service-Board-Meldung", resp.StatusCode, text)
	}
}

// F-1530 (Session-QA Sprint 82): Kartenmail und LLM-Senden sind POSTs auf
// Unterressourcen — der ACLGuard verlangt editor+. Ein Domain-Betrachter
// kommt nicht durch, ein Domain-Editor schon (bis zum Handler).
func TestACLDomain_KartenmailUndLLMNurAbEditor(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	env.seedBoard(t, "TEAM")
	env.seedBoard(t, "TE")
	ctx := context.Background()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	_ = env.acl.Set(ctx, "TEAM", "owner@firma.de", storage.RoleOwner)
	_ = env.acl.Set(ctx, "TE", "owner@firma.de", storage.RoleOwner)
	if err := env.acl.Set(ctx, "TEAM", "@firma.de", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := env.acl.Set(ctx, "TE", "@firma.de", storage.RoleEditor); err != nil {
		t.Fatal(err)
	}
	for _, pfad := range []string{"/cards/c1/email", "/cards/c1/llm-send"} {
		r := env.do(t, http.MethodPost, "/boards/TEAM"+pfad, "neu@firma.de", []byte(`{}`))
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("POST %s als Domain-Betrachter = %d, erwartet 403", pfad, r.StatusCode)
		}
		r = env.do(t, http.MethodPost, "/boards/TE"+pfad, "neu@firma.de", []byte(`{}`))
		r.Body.Close()
		if r.StatusCode == http.StatusForbidden {
			t.Fatalf("POST %s als Domain-Editor = 403, erwartet Durchlass am Guard", pfad)
		}
	}
}
