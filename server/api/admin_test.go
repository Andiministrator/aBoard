package api_test

// admin_test.go — /admin/*-API (Karte #306, ADR-0101).
//
// Deckt requireAdmin (403 ohne Identität / nicht-Admin / kein User-Row),
// Config-Get/Set/Delete-Roundtrip, Audit-Redaktion für Secrets, Rate-Limit
// (30/h), Self-Lockout-Schutz beim Admin-Flag-Wechsel.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

type adminTestEnv struct {
	srv   *httptest.Server
	db    *sql.DB
	users *storage.UserRepo
	cfg   *storage.ConfigRepo
	admin *api.AdminAPI
}

func newAdminTestEnv(t *testing.T, withCryptoBox bool) *adminTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	users := storage.NewUserRepo(db)

	var box *secretcrypto.Box
	if withCryptoBox {
		k := make([]byte, secretcrypto.KeySize)
		_, _ = rand.Read(k)
		b, err := secretcrypto.New(hex.EncodeToString(k))
		if err != nil {
			t.Fatalf("box: %v", err)
		}
		box = b
	}
	var ctr atomic.Int64
	cfg := storage.NewConfigRepo(db, box, func() string {
		return "audit-" + strconv.FormatInt(ctr.Add(1), 10)
	})
	admin := api.NewAdminAPI(cfg, users, "test", time.Now())

	mux := http.NewServeMux()
	admin.Register(mux)

	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)

	return &adminTestEnv{
		srv:   httptest.NewServer(h),
		db:    db,
		users: users,
		cfg:   cfg,
		admin: admin,
	}
}

func (e *adminTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

func (e *adminTestEnv) seedUser(t *testing.T, id, email string, isAdmin bool) {
	t.Helper()
	flag := 0
	if isAdmin {
		flag = 1
	}
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, ?)`,
		id, storage.NormalizeEmail(email), "N", "2026-01-01", flag); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
}

func (e *adminTestEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// Seit ADR-0129 §2 (F-1124) weist schon die Forward-Auth-Middleware einen
// Request ohne Identität mit 401 ab; die 403 von requireAdmin ist dahinter
// die zweite Linie und über diesen Stack nicht mehr erreichbar.
func TestAdmin_RequireAdmin_401WithoutIdentity(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()

	resp := env.do(t, http.MethodGet, "/admin/config", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("erwartet 401, got %d", resp.StatusCode)
	}
}

func TestAdmin_RequireAdmin_403ForNonAdmin(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-normal", "user@x.de", false)

	resp := env.do(t, http.MethodGet, "/admin/config", "user@x.de", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("erwartet 403 für Nicht-Admin, got %d", resp.StatusCode)
	}
}

func TestAdmin_ConfigRoundtrip_Plain(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	// Set
	resp := env.do(t, http.MethodPut, "/admin/config/smtp.host", "admin@x.de",
		map[string]any{"value": "mail.example.org", "is_secret": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Set Status: %d", resp.StatusCode)
	}

	// List
	resp = env.do(t, http.MethodGet, "/admin/config/smtp", "admin@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("List Status: %d", resp.StatusCode)
	}
	var body struct {
		Entries []struct {
			Key      string `json:"key"`
			Value    string `json:"value"`
			IsSecret bool   `json:"is_secret"`
			IsSet    bool   `json:"is_set"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 1 || body.Entries[0].Value != "mail.example.org" {
		t.Errorf("List unerwartet: %+v", body)
	}

	// Delete
	resp = env.do(t, http.MethodDelete, "/admin/config/smtp.host", "admin@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Delete Status: %d", resp.StatusCode)
	}
}

func TestAdmin_ConfigSet_Secret_RedactedInList(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	resp := env.do(t, http.MethodPut, "/admin/config/smtp.pass", "admin@x.de",
		map[string]any{"value": "geheim123", "is_secret": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Set Status: %d", resp.StatusCode)
	}

	resp = env.do(t, http.MethodGet, "/admin/config", "admin@x.de", nil)
	var body struct {
		Entries []struct {
			Key      string `json:"key"`
			Value    string `json:"value"`
			IsSecret bool   `json:"is_secret"`
			IsSet    bool   `json:"is_set"`
		} `json:"entries"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 1 {
		t.Fatalf("Entries: %+v", body.Entries)
	}
	e := body.Entries[0]
	if !e.IsSecret || !e.IsSet {
		t.Errorf("Flags falsch: %+v", e)
	}
	if e.Value != "" {
		t.Errorf("SECRET LEAK: value=%q", e.Value)
	}
}

func TestAdmin_ConfigSet_SecretWithoutBox_503(t *testing.T) {
	env := newAdminTestEnv(t, false) // KEINE Crypto-Box
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	resp := env.do(t, http.MethodPut, "/admin/config/smtp.pass", "admin@x.de",
		map[string]any{"value": "geheim", "is_secret": true})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("erwartet 503, got %d", resp.StatusCode)
	}
}

func TestAdmin_AuditLog_SecretRedacted(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	_ = env.do(t, http.MethodPut, "/admin/config/smtp.pass", "admin@x.de",
		map[string]any{"value": "geheim123", "is_secret": true})

	resp := env.do(t, http.MethodGet, "/admin/audit", "admin@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Audit Status: %d", resp.StatusCode)
	}
	var body struct {
		Entries []struct {
			Action           string `json:"action"`
			NewValueRedacted string `json:"new_value_redacted"`
		} `json:"entries"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 1 {
		t.Fatalf("Audit-Einträge: %+v", body.Entries)
	}
	if body.Entries[0].NewValueRedacted != "***" {
		t.Errorf("Secret leakt in Audit: %q", body.Entries[0].NewValueRedacted)
	}
}

func TestAdmin_SelfLockout_RevokeLastAdmin_409(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	// Versuch, sich selbst als letzten Admin zu degradieren → 409.
	resp := env.do(t, http.MethodPatch, "/admin/users/u-admin/admin", "admin@x.de",
		map[string]any{"is_admin": false})
	if resp.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("erwartet 409, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestAdmin_SelfLockout_OkWennAndererAdminBleibt(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-a", "a@x.de", true)
	env.seedUser(t, "u-b", "b@x.de", true)

	// a entzieht sich → OK, weil b noch Admin bleibt.
	resp := env.do(t, http.MethodPatch, "/admin/users/u-a/admin", "a@x.de",
		map[string]any{"is_admin": false})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Self-Revoke mit zweitem Admin → %d", resp.StatusCode)
	}
}

func TestAdmin_RateLimit_30PerHour(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	// 30 PUTs sollen klappen, der 31. → 429.
	for i := 0; i < 30; i++ {
		resp := env.do(t, http.MethodPut, "/admin/config/k", "admin@x.de",
			map[string]any{"value": "v" + strconv.Itoa(i), "is_secret": false})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Lauf %d: erwartet 200, got %d", i, resp.StatusCode)
		}
	}
	resp := env.do(t, http.MethodPut, "/admin/config/k", "admin@x.de",
		map[string]any{"value": "vN", "is_secret": false})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("31. Mutation: erwartet 429, got %d", resp.StatusCode)
	}
}

func TestAdmin_Status_LiefertVersionUndAdminCount(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)
	env.seedUser(t, "u-2", "two@x.de", true)

	resp := env.do(t, http.MethodGet, "/admin/status", "admin@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status: %d", resp.StatusCode)
	}
	var body struct {
		Version        string `json:"version"`
		AdminCount     int    `json:"admin_count"`
		SecretsEnabled bool   `json:"secrets_enabled"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Version != "test" {
		t.Errorf("Version: %q", body.Version)
	}
	if body.AdminCount != 2 {
		t.Errorf("AdminCount: %d", body.AdminCount)
	}
	if !body.SecretsEnabled {
		t.Errorf("SecretsEnabled erwartet true")
	}
}

func TestAdmin_SectionTest_501WhenUnregistered(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("erwartet 501 für nicht-registrierte Sektion, got %d", resp.StatusCode)
	}
}

func TestAdmin_SectionTest_RunsRegisteredHook(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	called := false
	env.admin.Registry().RegisterTest("smtp", func(ctx context.Context, cfg map[string]string) (any, error) {
		called = true
		return nil, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("erwartet 200, got %d", resp.StatusCode)
	}
	if !called {
		t.Errorf("Test-Hook nicht aufgerufen")
	}
}

// AK_test_endpoint: POST /admin/config/mail_auto_reply/test → 200,
// wenn ein Test-Hook für "mail_auto_reply" registriert ist.
func TestAdmin_MailAutoReplyTest_ReturnsOKWhenRegistered(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	var capturedCfg map[string]string
	env.admin.Registry().RegisterTest("mail_auto_reply", func(ctx context.Context, cfg map[string]string) (any, error) {
		capturedCfg = cfg
		return nil, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/mail_auto_reply/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("AK_test_endpoint: erwartet 200, got %d", resp.StatusCode)
	}
	// Hook wurde aufgerufen (capturedCfg nicht mehr nil)
	if capturedCfg == nil {
		t.Error("AK_test_endpoint: mail_auto_reply-Test-Hook nicht aufgerufen")
	}
}

// AK_test_endpoint_payload (Karte #333): Liefert der Hook ein Ergebnis, steht
// es unter "result" in der Antwort — und `ok:true` bleibt daneben stehen, damit
// Aufrufer, die nur darauf sehen, nicht brechen.
func TestAdmin_SectionTest_ReturnsHookResult(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	env.admin.Registry().RegisterTest("mail_auto_reply", func(ctx context.Context, cfg map[string]string) (any, error) {
		return map[string]any{
			"rendered_subject": "[aBoard #42] Re: Beispiel-Karte",
			"rendered_body":    "Guten Tag,\n\nIhre Anfrage ist eingegangen.",
		}, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/mail_auto_reply/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erwartet 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok fehlt oder ist nicht true: %v", body["ok"])
	}
	result, _ := body["result"].(map[string]any)
	if result == nil {
		t.Fatalf("result fehlt in der Antwort: %v", body)
	}
	if got := result["rendered_subject"]; got != "[aBoard #42] Re: Beispiel-Karte" {
		t.Errorf("rendered_subject = %v", got)
	}
	if got, _ := result["rendered_body"].(string); got == "" {
		t.Error("rendered_body ist leer")
	}
}

// Ohne Hook-Ergebnis bleibt die Antwort wie vor Karte #333 — kein leeres
// "result"-Feld, auf das ein Aufrufer hereinfallen könnte.
func TestAdmin_SectionTest_OmitsResultWhenHookReturnsNil(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	env.admin.Registry().RegisterTest("smtp", func(ctx context.Context, cfg map[string]string) (any, error) {
		return nil, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erwartet 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	if _, da := body["result"]; da {
		t.Errorf("result darf ohne Hook-Ergebnis nicht gesetzt sein: %v", body)
	}
}

// Sprint-53-Kritiker: `result != nil` prüft nur das Interface. Ein Hook mit
// `var m map[string]any; return m, nil` liefert ein NICHT-nil Interface mit
// nil-Wert dahinter — die Antwort enthielt dann `"result": null`, obwohl
// OmitsResultWhenHookReturnsNil genau das ausschliessen sollte.
func TestAdmin_SectionTest_TypisiertesNilLiefertKeinResult(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	env.admin.Registry().RegisterTest("smtp", func(ctx context.Context, cfg map[string]string) (any, error) {
		var m map[string]any // typisiertes nil
		return m, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erwartet 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	if _, da := body["result"]; da {
		t.Errorf("typisiertes nil darf kein result-Feld erzeugen: %v", body)
	}
}

// Ein nicht serialisierbares Ergebnis ergab bisher HTTP 200 mit KOMPLETT
// leerem Body: writeJSON setzt den Status vor dem Encode, und json.Encoder
// schreibt bei Marshal-Fehler gar nichts. Der Client las "Test ok" ohne
// Ergebnis — der Fehler blieb undiagnostiziert.
func TestAdmin_SectionTest_NichtSerialisierbaresErgebnis(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	env.admin.Registry().RegisterTest("smtp", func(ctx context.Context, cfg map[string]string) (any, error) {
		return map[string]any{"kanal": make(chan int)}, nil
	})

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("erwartet 500, got %d — ein leerer 200er wäre schlimmer als ein ehrlicher Fehler", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Antwort nicht dekodierbar (leerer Body?): %v", err)
	}
	if body["ok"] != false {
		t.Errorf("ok = %v, erwartet false", body["ok"])
	}
}

// Liefert ein Hook Ergebnis UND Fehler, gehörte das Ergebnis bisher zu den
// stillen Verlusten: der 422-Zweig schrieb nur den Fehlertext. Ein Test, der
// "Host erreicht, TLS ok, AUTH abgelehnt" meldet, ist deutlich nützlicher.
func TestAdmin_SectionTest_ErgebnisAuchImFehlerfall(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	env.admin.Registry().RegisterTest("smtp", func(ctx context.Context, cfg map[string]string) (any, error) {
		return map[string]any{"reached_host": true, "tls": "ok"}, errors.New("AUTH abgelehnt")
	})

	resp := env.do(t, http.MethodPost, "/admin/config/smtp/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("erwartet 422, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	if body["error"] != "AUTH abgelehnt" {
		t.Errorf("error = %v", body["error"])
	}
	result, _ := body["result"].(map[string]any)
	if result == nil || result["tls"] != "ok" {
		t.Errorf("Diagnosedaten im Fehlerfall verloren: %v", body)
	}
}

// AK_test_endpoint_501: Ohne registrierten Hook → 501.
func TestAdmin_MailAutoReplyTest_Returns501WhenUnregistered(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	resp := env.do(t, http.MethodPost, "/admin/config/mail_auto_reply/test", "admin@x.de", map[string]any{})
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("AK_test_endpoint_501: erwartet 501, got %d", resp.StatusCode)
	}
}

// K-B8 (Session-Kritiker Sprint 72): llm_openai_base_url erscheint nur, wenn
// eine Basis gesetzt ist, und dann genau mit ihr.
func TestAdmin_Status_OpenAIBasisNurWennGesetzt(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)

	lies := func() map[string]any {
		resp := env.do(t, http.MethodGet, "/admin/status", "admin@x.de", nil)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body
	}
	if _, ok := lies()["llm_openai_base_url"]; ok {
		t.Error("ohne Basis darf das Feld nicht erscheinen")
	}
	env.admin.WithLLMOpenAIBaseURL("http://llm.example:8000/v1")
	if v := lies()["llm_openai_base_url"]; v != "http://llm.example:8000/v1" {
		t.Errorf("llm_openai_base_url = %v", v)
	}
}

// Session-Kritiker Sprint 74 (F-1204): /admin/status nennt den Uhr-Zustand,
// damit ein dauerhaft ruhendes Löschen nicht nur in einer rotierten
// Log-Zeile steht.
// GEGENPROBE: Ohne body["uhr"] in handleStatus fehlt das Feld — rot.
func TestAdmin_Status_UhrZustand(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return false, errors.New("operation not permitted") }

	resp := env.do(t, http.MethodGet, "/admin/status", "admin@x.de", nil)
	defer resp.Body.Close()
	var body struct {
		Uhr map[string]any `json:"uhr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Uhr["pruefung"] != "an" || body.Uhr["gestellt"] != false || body.Uhr["fehler"] != "operation not permitted" {
		t.Fatalf("uhr = %v, erwartet pruefung an, gestellt false, fehler gesetzt", body.Uhr)
	}
}

// F-1546: /admin/status nennt Firmenmodus, erlaubte Modelle und PII-Vorgabe —
// aber weder Schlüssel noch Schlüssellänge. Der Test übergibt der API einen
// Schlüssel gar nicht erst; er hält fest, dass die Antwort außer den
// vereinbarten Feldern nichts Schlüsselartiges trägt.
// GEGENPROBE: Ohne die Felder in handleStatus fehlt llm_firmenmodus — rot.
func TestAdmin_Status_FirmenmodusUndPII(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)
	lies := func() map[string]any {
		resp := env.do(t, http.MethodGet, "/admin/status", "admin@x.de", nil)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body
	}
	if _, ok := lies()["llm_firmenmodus"]; ok {
		t.Error("ohne Vorgaben darf das Feld nicht erscheinen")
	}
	env.admin.WithLLMVorgaben(true, []string{"qwen3-32b"}, "aus")
	body := lies()
	if body["llm_firmenmodus"] != true || body["llm_pii"] != "aus" || fmt.Sprint(body["llm_modelle"]) != "[qwen3-32b]" {
		t.Fatalf("Status: %v", body)
	}
	// Karte #703: Ziel des Firmenmodus (nur Host); ohne Basis-URL der Standard.
	if body["llm_ziel"] != "api.openai.com" {
		t.Errorf("llm_ziel = %v, erwartet api.openai.com", body["llm_ziel"])
	}
	env.admin.WithLLMOpenAIBaseURL("https://llm.firma.example:8443/v1")
	if z := lies()["llm_ziel"]; z != "llm.firma.example:8443" {
		t.Errorf("llm_ziel = %v, erwartet nur den Host", z)
	}
	env.admin.WithLLMOpenAIBaseURL("")
	for k := range body {
		if strings.Contains(strings.ToLower(k), "schluessel") || strings.Contains(strings.ToLower(k), "key") || strings.Contains(strings.ToLower(k), "laenge") {
			t.Errorf("Status trägt das Feld %q", k)
		}
	}
	env.admin.WithLLMVorgaben(false, nil, "nutzer")
	body = lies()
	if _, ok := body["llm_ziel"]; ok {
		t.Error("ohne Firmenmodus darf llm_ziel nicht erscheinen")
	}
	if body["llm_firmenmodus"] != false || body["llm_pii"] != "nutzer" || body["llm_modelle"] != nil {
		t.Fatalf("ohne Firmenmodus: %v", body)
	}
}

// ADR-0138 §2, Kritiker 07.10., K-4: Eine unbekannte Kalender-Zeitzone wird
// beim Speichern abgelehnt — sonst rechnete der Feed still in UTC. Leer heisst
// Standard (Europe/Berlin) und bleibt erlaubt.
func TestAdmin_ConfigSet_KalenderZeitzoneGeprueft(t *testing.T) {
	env := newAdminTestEnv(t, true)
	defer env.close()
	env.seedUser(t, "u-admin", "admin@x.de", true)
	for _, f := range []struct {
		wert   string
		status int
	}{{"Mars/Olympus", http.StatusBadRequest}, {"Local", http.StatusBadRequest}, {"Europe/Berlin", http.StatusOK}, {"", http.StatusOK}} {
		resp := env.do(t, http.MethodPut, "/admin/config/kalender.zeitzone", "admin@x.de",
			map[string]any{"value": f.wert, "is_secret": false})
		resp.Body.Close()
		if resp.StatusCode != f.status {
			t.Errorf("%q: status=%d, erwartet %d", f.wert, resp.StatusCode, f.status)
		}
	}
	// Andere Schlüssel bleiben ungeprüft.
	resp := env.do(t, http.MethodPut, "/admin/config/kalender.sonstwas", "admin@x.de",
		map[string]any{"value": "Mars/Olympus", "is_secret": false})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("anderer Schlüssel: status=%d", resp.StatusCode)
	}
}
