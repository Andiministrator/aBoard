package api_test

// forwardauth_streng_test.go — strenger Modus der Forward-Auth-Middleware
// (ADR-0129 §2, F-1124). Prüfbefehl aus F-1124: Trust an, falsches Secret →
// 401; ohne Header → 401. Widerlegt wäre der Fix, wenn bei
// ABOARD_TRUST_FORWARD_AUTH=1 ein GET /boards ohne Header 200 liefert.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

// strengerRequest schickt einen Request an den Stack der ACL-Testumgebung
// (ForwardAuth → ACLGuard → mux, dieselbe Reihenfolge wie main.go) und
// setzt nur die Header, die der Fall braucht.
func strengerRequest(t *testing.T, env *aclTestEnv, method, path, email, secret string) int {
	t.Helper()
	req, err := http.NewRequest(method, env.srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		req.Header.Set("Remote-Email", email)
	}
	if secret != "" {
		req.Header.Set("X-Aboard-Forward-Auth-Secret", secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestStrengerModus_Stack — die drei Fälle aus F-1124 gegen den echten
// Middleware-Stack mit ACLGuard. Vor dem Fix liefen die ersten beiden in den
// Legacy-Bypass des Guards und bekamen 200.
func TestStrengerModus_Stack(t *testing.T) {
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")
	env := newACLTestEnv(t, true)
	defer env.srv.Close()
	env.seedBoard(t, "B")

	fälle := []struct {
		name, email, secret string
		want                int
	}{
		{"ohne Header", "", "", http.StatusUnauthorized},
		{"Identität ohne Secret", "spoof@firma.de", "", http.StatusUnauthorized},
		{"Identität mit falschem Secret", "spoof@firma.de", "falsch", http.StatusUnauthorized},
		{"nur Secret, keine Identität", "", "geheim123", http.StatusUnauthorized},
		{"Identität mit richtigem Secret", "alice@firma.de", "geheim123", http.StatusOK},
	}
	for _, f := range fälle {
		t.Run(f.name, func(t *testing.T) {
			for _, pfad := range []string{"/boards", "/boards/B"} {
				got := strengerRequest(t, env, http.MethodGet, pfad, f.email, f.secret)
				// Der letzte Fall ist ein Erstkontakt: alice bekommt ihr
				// persönliches Board, aber keine Rolle auf B → 403 dort.
				want := f.want
				if f.want == http.StatusOK && pfad == "/boards/B" {
					want = http.StatusForbidden
				}
				if got != want {
					t.Errorf("GET %s: Status = %d, erwartet %d", pfad, got, want)
				}
			}
		})
	}
}

// TestStrengerModus_OffenePfade — /healthz und der iCal-Feed bleiben ohne
// Identität erreichbar, aber nur lesend und nur exakt diese Pfade.
func TestStrengerModus_OffenePfade(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")

	var gesehen *api.ForwardAuthIdentity
	var gelaufen bool
	h := api.WithForwardAuthIfEnabled(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gelaufen = true
		gesehen = api.IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	fälle := []struct {
		method, pfad string
		want         int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodGet, "/ical/calendar.ics", http.StatusOK},
		{http.MethodHead, "/ical/calendar.ics", http.StatusOK},
		// Nur GET/HEAD: eine schreibende Methode kommt nicht vorbei.
		{http.MethodPost, "/healthz", http.StatusUnauthorized},
		{http.MethodDelete, "/ical/calendar.ics", http.StatusUnauthorized},
		// Nur exakt diese Pfade — keine Präfixe, keine Nachbarn.
		{http.MethodGet, "/healthz/", http.StatusUnauthorized},
		{http.MethodGet, "/healthzx", http.StatusUnauthorized},
		{http.MethodGet, "/ical/", http.StatusUnauthorized},
		{http.MethodGet, "/me/ical/subscriptions", http.StatusUnauthorized},
		{http.MethodGet, "/", http.StatusUnauthorized},
		// K-A9: kodierte Form eines offenen Pfads — der Mux routet sie in den
		// Catch-all, also darf sie nicht offen sein.
		{http.MethodGet, "/ical%2Fcalendar.ics", http.StatusUnauthorized},
		{http.MethodGet, "/%68ealthz", http.StatusUnauthorized},
	}
	for _, f := range fälle {
		t.Run(f.method+" "+f.pfad, func(t *testing.T) {
			gelaufen, gesehen = false, nil
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(f.method, f.pfad, nil))
			if rr.Code != f.want {
				t.Fatalf("Status = %d, erwartet %d", rr.Code, f.want)
			}
			if gelaufen != (f.want == http.StatusOK) {
				t.Fatalf("Handler gelaufen = %v, erwartet %v", gelaufen, !gelaufen)
			}
		})
	}

	// Auf einem offenen Pfad wird eine mitgeschickte Identität NICHT
	// übernommen — auch nicht mit richtigem Secret. Sonst wäre /healthz ein
	// Einstieg in den Erstkontakt-Pfad des ACLGuard (Fehlerklasse 95).
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Remote-Email", "alice@firma.de")
	req.Header.Set("X-Aboard-Forward-Auth-Secret", "geheim123")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gesehen != nil {
		t.Errorf("offener Pfad hat Identität übernommen: %+v", gesehen)
	}
}

// TestStrengerModus_TrustAus_Unveraendert — der auth-lose Einzelbetrieb
// bleibt, wie er war: ohne Header 200, keine Identität.
func TestStrengerModus_TrustAus_Unveraendert(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.srv.Close()
	env.seedBoard(t, "B")
	if got := strengerRequest(t, env, http.MethodGet, "/boards/B", "", ""); got != http.StatusOK {
		t.Fatalf("Trust aus, ohne Header: Status = %d, erwartet 200", got)
	}
}

// TestDevFakeHeaders_SchicktSecretMit — der Dev-Modus spielt den Proxy und
// muss deshalb das Secret mitschicken, sonst sperrt ihn der strenge Modus
// aus.
func TestDevFakeHeaders_SchicktSecretMit(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")
	t.Setenv("ABOARD_DEV_FAKE_USER", "devuser")
	t.Setenv("ABOARD_DEV_FAKE_EMAIL", "dev@example.com")

	cap := &captureHandler{}
	wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boards", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", rr.Code)
	}
	if id := api.IdentityFromContext(cap.lastReq.Context()); id == nil || id.Email != "dev@example.com" {
		t.Fatalf("Identität = %+v, erwartet dev@example.com", id)
	}
}

// TestDevFakeHeaders_UebernimmtKeineClientIdentitaet — K-A10: Bei leerem
// ABOARD_DEV_FAKE_EMAIL darf eine vom Client gesendete Remote-Email nicht
// durchrutschen; der Dev-Layer setzt das Secret ja selbst.
func TestDevFakeHeaders_UebernimmtKeineClientIdentitaet(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")
	t.Setenv("ABOARD_DEV_FAKE_USER", "dev")
	t.Setenv("ABOARD_DEV_FAKE_EMAIL", "")

	cap := &captureHandler{}
	wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))
	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-Email", "opfer@firma.de")
	wrapped.ServeHTTP(httptest.NewRecorder(), req)
	if cap.lastReq == nil {
		t.Fatal("Handler lief nicht")
	}
	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil || id.Email == "opfer@firma.de" || id.User != "dev" {
		t.Fatalf("Identität = %+v — Client-Header wurde übernommen (K-A10)", id)
	}
}

// TestForwardAuth_SecretMitZeilenumbruch — K-A5: Der Startcheck trimmt das
// Secret; die Laufzeit muss es genauso tun, sonst sperrt ein Umbruch aus einer
// Secret-Datei jeden Request aus.
func TestForwardAuth_SecretMitZeilenumbruch(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123\n")
	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)
	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-Email", "a@firma.de")
	req.Header.Set("X-Aboard-Forward-Auth-Secret", "geheim123")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200 — Secret zur Laufzeit nicht getrimmt", rr.Code)
	}
}

// TestACLGuard_OhneForwardAuth_TrustAn_401 — K-A11: Auch ein Stack, der den
// ACLGuard ohne die Forward-Auth-Middleware verwendet, lässt bei Trust an
// keinen Request ohne Identität durch. Offene Pfade bleiben offen.
func TestACLGuard_OhneForwardAuth_TrustAn_401(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	guard := api.NewACLGuard(nil, nil, nil)
	gelaufen := false
	h := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gelaufen = true
		w.WriteHeader(http.StatusOK)
	}))
	for _, pfad := range []string{"/boards/x", "/boards", "/me", "/admin/config"} {
		gelaufen = false
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, pfad, nil))
		if rr.Code != http.StatusUnauthorized || gelaufen {
			t.Errorf("GET %s ohne Identität: Status %d, Handler gelaufen=%v — erwartet 401", pfad, rr.Code, gelaufen)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("/healthz ohne Identität: Status %d, erwartet 200", rr.Code)
	}

	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boards/x", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("Trust aus: Status %d, erwartet 200 (auth-loser Einzelbetrieb)", rr.Code)
	}
}
