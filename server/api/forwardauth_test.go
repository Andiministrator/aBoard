package api_test

// forwardauth_test.go — Tests für die optionale Forward-Auth-Middleware
// (Karte #73, ADR-0057).

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

// captureHandler ist ein Test-Handler, der den letzten Request aufzeichnet.
type captureHandler struct {
	lastReq *http.Request
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.lastReq = r
	w.WriteHeader(http.StatusOK)
}

// ── WithForwardAuthIfEnabled — Flag aus ──────────────────────────────────────

// TestForwardAuth_FlagAus_KeinVerhalten stellt sicher, dass ohne
// ABOARD_TRUST_FORWARD_AUTH identisches Verhalten gilt: kein Context-Key,
// keine Verarbeitung der Remote-*-Header.
func TestForwardAuth_FlagAus_KeinVerhalten(t *testing.T) {
	// ABOARD_TRUST_FORWARD_AUTH ist in Tests per Default nicht gesetzt.
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")

	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-User", "testuser")
	req.Header.Set("Remote-Email", "test@example.com")

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", rr.Code)
	}

	// Context-Key muss leer sein — Middleware ist inaktiv.
	if id := api.IdentityFromContext(cap.lastReq.Context()); id != nil {
		t.Errorf("IdentityFromContext = %+v, erwartet nil (Flag deaktiviert)", id)
	}
}

// ── WithForwardAuthIfEnabled — Flag an ──────────────────────────────────────

// TestForwardAuth_FlagAn_HeaderVorhanden prüft den Hauptpfad:
// Flag gesetzt + Remote-User/Email → Identität im Context.
func TestForwardAuth_FlagAn_HeaderVorhanden(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")

	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-User", "alice")
	req.Header.Set("Remote-Email", "alice@example.com")
	req.Header.Set("Remote-Groups", "admins,users")

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", rr.Code)
	}

	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil {
		t.Fatal("IdentityFromContext = nil, erwartet Identität")
	}
	if id.User != "alice" {
		t.Errorf("User = %q, erwartet alice", id.User)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("Email = %q, erwartet alice@example.com", id.Email)
	}
	if id.Groups != "admins,users" {
		t.Errorf("Groups = %q, erwartet admins,users", id.Groups)
	}
}

// TestForwardAuth_FlagAn_KeineHeader_401 — strenger Modus (ADR-0129 §2,
// F-1124): Flag gesetzt, aber kein Remote-User/Email-Header → 401, der
// Handler läuft nicht. Bis Sprint 70 kam der Request hier ohne Identität
// durch und landete im Legacy-Bypass des ACLGuard.
func TestForwardAuth_FlagAn_KeineHeader_401(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")

	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	// Kein Remote-User, kein Remote-Email gesetzt.

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("Status = %d, erwartet 401 (strenger Modus, F-1124)", rr.Code)
	}
	if cap.lastReq != nil {
		t.Errorf("Handler lief trotz fehlender Identität")
	}
}

// TestForwardAuth_FlagAn_NurEmail prüft dass auch nur Email ohne User
// ausreicht um eine Identität zu setzen (Authelia kann User leer lassen
// wenn nur OIDC-Email bekannt ist).
func TestForwardAuth_FlagAn_NurEmail(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")

	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	req.Header.Set("Remote-Email", "bob@example.com")
	// Kein Remote-User.

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil {
		t.Fatal("IdentityFromContext = nil, erwartet Identität bei Email-only")
	}
	if id.Email != "bob@example.com" {
		t.Errorf("Email = %q, erwartet bob@example.com", id.Email)
	}
	if id.User != "" {
		t.Errorf("User = %q, erwartet leer", id.User)
	}
}

// ── Dev-Fake-Header ──────────────────────────────────────────────────────────

// TestDevFakeHeaders_FlagAus_KeinInject prüft dass ohne ABOARD_TRUST_FORWARD_AUTH
// kein Dev-Fake-Inject stattfindet — auch wenn ABOARD_DEV_FAKE_USER gesetzt ist.
func TestDevFakeHeaders_FlagAus_KeinInject(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_DEV_FAKE_USER", "devuser")

	cap := &captureHandler{}
	// Gesamter Stack: erst Dev-Fake, dann Forward-Auth.
	wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	// Kein Inject ohne Trust-Flag → kein Context-Eintrag.
	if id := api.IdentityFromContext(cap.lastReq.Context()); id != nil {
		t.Errorf("IdentityFromContext = %+v, erwartet nil (Trust-Flag aus)", id)
	}
}

// TestDevFakeHeaders_BeideFlags_Inject prüft das Zusammenspiel:
// ABOARD_TRUST_FORWARD_AUTH + ABOARD_DEV_FAKE_USER → Dev-Header werden
// injiziert, Forward-Auth-Middleware liest sie und setzt Identität.
func TestDevFakeHeaders_BeideFlags_Inject(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_DEV_FAKE_USER", "devuser")
	t.Setenv("ABOARD_DEV_FAKE_EMAIL", "dev@example.com")
	t.Setenv("ABOARD_DEV_FAKE_GROUPS", "dev-group")

	cap := &captureHandler{}
	// Stack: Dev-Fake-Inject → Forward-Auth-Read → Handler.
	wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", rr.Code)
	}

	id := api.IdentityFromContext(cap.lastReq.Context())
	if id == nil {
		t.Fatal("IdentityFromContext = nil, erwartet Dev-Identität")
	}
	if id.User != "devuser" {
		t.Errorf("User = %q, erwartet devuser", id.User)
	}
	if id.Email != "dev@example.com" {
		t.Errorf("Email = %q, erwartet dev@example.com", id.Email)
	}
	if id.Groups != "dev-group" {
		t.Errorf("Groups = %q, erwartet dev-group", id.Groups)
	}
}

// TestDevFakeHeaders_TrustAn_KeinFakeUser_KeinInject prüft dass ohne
// ABOARD_DEV_FAKE_USER kein Inject stattfindet — auch wenn Trust-Flag gesetzt.
func TestDevFakeHeaders_TrustAn_KeinFakeUser_KeinInject(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_DEV_FAKE_USER", "")

	cap := &captureHandler{}
	wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))

	req := httptest.NewRequest(http.MethodGet, "/boards", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	// Kein FakeUser → kein Inject → kein Header → 401 im strengen Modus
	// (F-1124). Der Handler darf nicht laufen.
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, erwartet 401 (kein FakeUser, keine Header)", rr.Code)
	}
	if cap.lastReq != nil {
		t.Errorf("Handler lief ohne Identität")
	}
}

// ── IdentityFromContext — Null-Sicherheit ────────────────────────────────────

// TestIdentityFromContext_LeerContext prüft dass IdentityFromContext(ctx)
// nil zurückgibt wenn keine Identität gesetzt wurde — kein Panic.
func TestIdentityFromContext_LeerContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if id := api.IdentityFromContext(req.Context()); id != nil {
		t.Errorf("IdentityFromContext auf leerem Context = %+v, erwartet nil", id)
	}
}

// ── F-255: kein Log-Rauschen pro Request ─────────────────────────────────────

// logMitschnitt lenkt den Standard-Logger in einen Puffer um und gibt ihn
// samt Aufräum-Hook zurück. Go-`log` hat keine Level — die Umlenkung ist der
// einzige Weg, „diese Zeile darf nicht entstehen" zu prüfen.
func logMitschnitt(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	altOut := log.Writer()
	altFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(altOut)
		log.SetFlags(altFlags)
	})
	return &buf
}

// F-255: Hinter Authelia trägt JEDER Request die Remote-*-Header — Polling
// alle ~10 s pro Tab plus jedes CRUD. Eine Log-Zeile pro Request ist damit
// Dauer-Rauschen, das sich im Betrieb nicht abstellen liess.
func TestForwardAuth_KeinLogProRequest(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_VERBOSE", "")

	buf := logMitschnitt(t)
	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)
	buf.Reset() // die einmalige Start-Zeile interessiert hier nicht

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/boards", nil)
		req.Header.Set("Remote-User", "alice")
		req.Header.Set("Remote-Email", "alice@example.com")
		wrapped.ServeHTTP(httptest.NewRecorder(), req)
	}

	if strings.Contains(buf.String(), "forward-auth: user=") {
		t.Errorf("Identitäts-Zeile pro Request geschrieben, obwohl "+
			"ABOARD_FORWARD_AUTH_VERBOSE nicht gesetzt ist (F-255).\nLog:\n%s", buf.String())
	}
	// Die Identität muss trotzdem ankommen — der Fix darf nur das Log
	// abschalten, nicht die Middleware.
	if id := api.IdentityFromContext(cap.lastReq.Context()); id == nil || id.User != "alice" {
		t.Errorf("Identität fehlt trotz gesetzter Header: %+v", id)
	}
}

// Gegenrichtung: Wer hinsehen will, bekommt die Zeile per Env zurück.
func TestForwardAuth_VerboseSchaltetLogEin(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_VERBOSE", "1")

	buf := logMitschnitt(t)
	cap := &captureHandler{}
	wrapped := api.WithForwardAuthIfEnabled(cap)
	buf.Reset()

	req := httptest.NewRequest(http.MethodGet, "/boards/x", nil)
	req.Header.Set("Remote-User", "bob")
	req.Header.Set("Remote-Email", "bob@example.com")
	wrapped.ServeHTTP(httptest.NewRecorder(), req)

	ausgabe := buf.String()
	for _, teil := range []string{"forward-auth: user=", `"bob"`, "bob@example.com", "/boards/x"} {
		if !strings.Contains(ausgabe, teil) {
			t.Errorf("Verbose-Log enthält %q nicht.\nLog:\n%s", teil, ausgabe)
		}
	}
}

// Die einmalige Start-Zeile ersetzt die Request-Zeile als Antwort auf
// „läuft die Forward-Auth überhaupt?" — ohne sie wäre der Fix eine reine
// Verschlechterung der Beobachtbarkeit.
func TestForwardAuth_StartZeileNenntDenSchalter(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_VERBOSE", "")

	buf := logMitschnitt(t)
	api.WithForwardAuthIfEnabled(&captureHandler{})

	ausgabe := buf.String()
	if !strings.Contains(ausgabe, "forward-auth aktiv") {
		t.Errorf("keine Start-Zeile beim Aktivieren.\nLog:\n%s", ausgabe)
	}
	if !strings.Contains(ausgabe, "ABOARD_FORWARD_AUTH_VERBOSE") {
		t.Errorf("Start-Zeile nennt den Verbose-Schalter nicht.\nLog:\n%s", ausgabe)
	}
}

// TestEnvSchalterAn deckt F-832 (c) ab: In forwardauth.go standen zwei
// Ein/Aus-Konventionen zwei Zeilen auseinander. forwardAuthVerbose() las
// "0"/"false"/"off" als AUS, ABOARD_TRUST_FORWARD_AUTH prüfte auf `!= ""` —
// dort schaltete `=0` also EIN. Beim Trust-Schalter ist das ein
// Sicherheitsbefund: Wer ihn auf 0 setzt und "aus" meint, bekam eine
// Instanz, die Identitäts-Header aus dem Netz vertraut.
func TestEnvSchalterAn(t *testing.T) {
	faelle := []struct {
		wert string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"  off  ", false},
		{"no", false},
		{"1", true},
		{"true", true},
		{"ja", true},
		{"irgendwas", true},
	}
	for _, f := range faelle {
		t.Run("wert="+f.wert, func(t *testing.T) {
			t.Setenv("ABOARD_TEST_SCHALTER", f.wert)
			if got := api.EnvSchalterAn("ABOARD_TEST_SCHALTER"); got != f.want {
				t.Errorf("EnvSchalterAn(%q) = %v, erwartet %v", f.wert, got, f.want)
			}
		})
	}
}

// TestTrustForwardAuthEnabled_NullIstAus ist der eigentliche F-832-(c)-Beleg:
// vor dem Fix war "0" hier EIN.
func TestTrustForwardAuthEnabled_NullIstAus(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "0")
	if api.TrustForwardAuthEnabled() {
		t.Error("ABOARD_TRUST_FORWARD_AUTH=0 muss AUS bedeuten — sonst vertraut der Server Identitäts-Headern, obwohl der Operator sie abgeschaltet glaubt")
	}
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	if !api.TrustForwardAuthEnabled() {
		t.Error("ABOARD_TRUST_FORWARD_AUTH=1 muss AN bedeuten")
	}
}

// TestDevFakeUser_FalsyNameBleibtAktiv deckt den Kritiker-Befund aus
// Sprint 61 ab: ABOARD_DEV_FAKE_USER ist ein WERT (der Fake-Username), kein
// Ein/Aus-Schalter. Wäre er über EnvSchalterAn geprüft, deaktivierte ein
// Entwickler mit dem Platzhalter-Namen "no" oder "off" den Dev-Layer lautlos.
func TestDevFakeUser_FalsyNameBleibtAktiv(t *testing.T) {
	for _, name := range []string{"no", "off", "false", "0"} {
		t.Run("name="+name, func(t *testing.T) {
			t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
			t.Setenv("ABOARD_DEV_FAKE_USER", name)

			var gesehen *api.ForwardAuthIdentity
			h := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(
				http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					gesehen = api.IdentityFromContext(r.Context())
				})))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/boards", nil))

			if gesehen == nil {
				t.Fatalf("Fake-Username %q hat den Dev-Layer abgeschaltet — er ist ein Wert, kein Schalter", name)
			}
			if gesehen.User != name {
				t.Errorf("Remote-User = %q, erwartet %q", gesehen.User, name)
			}
		})
	}
}

// TestDevFakeHeaders_Name (F-1178, Karte #584): ABOARD_DEV_FAKE_NAME landet
// als Remote-Name in der Identität und gewinnt im Anzeigenamen (#577). Ohne
// die Variable bleibt ein vom Client gesendeter Remote-Name draussen (K-A10).
//
// GEGENPROBE (Karte #584): Ohne das Setzen von Remote-Name in
// injectDevFakeHeaders ist Name leer, der Test wird rot.
func TestDevFakeHeaders_Name(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_DEV_FAKE_USER", "123456789012345678901")
	t.Setenv("ABOARD_DEV_FAKE_EMAIL", "dev@example.com")

	for _, fall := range []struct{ env, soll, anzeige string }{
		{"Andi Dev", "Andi Dev", "Andi Dev"},
		{"", "", "dev@example.com"},
	} {
		t.Setenv("ABOARD_DEV_FAKE_NAME", fall.env)
		cap := &captureHandler{}
		wrapped := api.WithDevFakeHeadersIfEnabled(api.WithForwardAuthIfEnabled(cap))
		req := httptest.NewRequest(http.MethodGet, "/boards", nil)
		req.Header.Set("Remote-Name", "vom Client")
		wrapped.ServeHTTP(httptest.NewRecorder(), req)

		id := api.IdentityFromContext(cap.lastReq.Context())
		if id == nil {
			t.Fatal("IdentityFromContext = nil, erwartet Dev-Identität")
		}
		if id.Name != fall.soll {
			t.Errorf("ABOARD_DEV_FAKE_NAME=%q: Name = %q, erwartet %q", fall.env, id.Name, fall.soll)
		}
		if got := id.AnzeigeName(); got != fall.anzeige {
			t.Errorf("ABOARD_DEV_FAKE_NAME=%q: AnzeigeName = %q, erwartet %q", fall.env, got, fall.anzeige)
		}
	}
}
