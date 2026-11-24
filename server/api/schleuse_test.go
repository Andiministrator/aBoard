package api_test

// schleuse_test.go — Routen der Ausgangsschleuse, Grundlage S1 (Karte #684,
// ADR-0134 §1, §2, §4).
//
// Der Aufbau spiegelt main.go: Admin-Status immer, Routen nur bei „an“,
// ACLGuard vor /boards/*, Forward-Auth als äusserste Schicht.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	slAdmin  = "admin@example.com"
	slAnna   = "anna@example.com"
	slBert   = "bert@example.com"
	slBoard  = "board-sl"
	slAnnaID = "u-anna"
)

type schleuseEnv struct {
	srv  *httptest.Server
	db   *sql.DB
	sl   *api.SchleuseAPI
	box  *secretcrypto.Box
	logs []string
	mu   sync.Mutex
}

func neueSchleuseEnv(t *testing.T, an, mitBox bool) *schleuseEnv {
	t.Helper()
	return neueSchleuseEnvMit(t, schleuse.Konfig{An: an, AufbewahrungTage: 30}, mitBox)
}

// neueSchleuseEnvMit nimmt die ganze Konfiguration (Karte #685: Größengrenze).
func neueSchleuseEnvMit(t *testing.T, cfg schleuse.Konfig, mitBox bool) *schleuseEnv {
	t.Helper()
	an := cfg.An
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	for _, u := range [][3]any{{"u-admin", slAdmin, 1}, {slAnnaID, slAnna, 0}, {"u-bert", slBert, 0}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u[0], u[1], u[2]); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, 'B', '1', '{}', 1, 1)`, slBoard); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if err := acl.Set(ctx, slBoard, slAnna, storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := acl.Set(ctx, slBoard, slBert, storage.RoleEditor); err != nil {
		t.Fatal(err)
	}
	var box *secretcrypto.Box
	if mitBox {
		k := make([]byte, secretcrypto.KeySize)
		_, _ = rand.Read(k)
		box, err = secretcrypto.New(hex.EncodeToString(k))
		if err != nil {
			t.Fatal(err)
		}
	}
	cfgRepo := storage.NewConfigRepo(db, box, func() string { return fmt.Sprint(time.Now().UnixNano()) })
	admin := api.NewAdminAPI(cfgRepo, users, "test", time.Now())
	var schluessel *storage.SchleuseSchluesselRepo
	if box != nil {
		schluessel = storage.NewSchleuseSchluesselRepo(db, box)
	}
	e := &schleuseEnv{db: db}
	e.box = box
	e.sl = api.NewSchleuseAPI(cfg, db, schluessel, users, acl).
		MitLog(func(f string, a ...any) {
			e.mu.Lock()
			e.logs = append(e.logs, fmt.Sprintf(f, a...))
			e.mu.Unlock()
		})
	mux := http.NewServeMux()
	admin.Register(mux)
	api.RegisterMeHandler(mux, users, api.MeWithSchleuse(an))
	api.RegistriereSchleuse(mux, admin, e.sl)
	h := api.WithForwardAuthIfEnabled(api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux))
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *schleuseEnv) do(t *testing.T, method, path, email string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, rdr)
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
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{"_roh": string(raw)}
	}
	return resp.StatusCode, m
}

// anlegen legt eine Freigabe als Admin an und liefert ihre ID.
func (e *schleuseEnv) anlegen(t *testing.T, body map[string]any) string {
	t.Helper()
	code, m := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAdmin, body)
	if code != http.StatusCreated {
		t.Fatalf("anlegen: %d %v", code, m)
	}
	return m["freigabe"].(map[string]any)["id"].(string)
}

var slAnthropic = map[string]any{
	"anbieter": "anthropic", "modelle": []string{"claude-x"}, "vertragsform_bestaetigt": true,
}

// slStand: der Stand, den der Schlüsseldialog für slAnthropic anzeigt (KC-2).
var slStand = map[string]any{"anbieter": "anthropic", "basis_url": "https://api.anthropic.com"}

// mitStand ergänzt einen Schlüssel-Body um den Stand der Freigabe.
func mitStand(b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range slStand {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ADR-0134 §1: Bei „aus“ gibt es keine Route (404), /me meldet nichts, der
// Admin-Status meldet „aus“.
func TestSchleuse_Aus_KeineRouten(t *testing.T) {
	e := neueSchleuseEnv(t, false, true)
	for _, r := range [][3]string{
		{http.MethodGet, "/admin/schleuse/freigaben", slAdmin},
		{http.MethodPost, "/admin/schleuse/freigaben", slAdmin},
		{http.MethodPut, "/admin/schleuse/freigaben/x", slAdmin},
		{http.MethodDelete, "/admin/schleuse/freigaben/x", slAdmin},
		{http.MethodGet, "/users/me/schleuse-schluessel", slAnna},
		{http.MethodPut, "/users/me/schleuse-schluessel/x", slAnna},
		{http.MethodDelete, "/users/me/schleuse-schluessel/x", slAnna},
		{http.MethodGet, "/boards/" + slBoard + "/schleuse-sperre", slAnna},
		{http.MethodPut, "/boards/" + slBoard + "/schleuse-sperre", slAnna},
	} {
		var body any
		if r[0] == http.MethodPost || r[0] == http.MethodPut {
			body = map[string]any{}
		}
		if code, m := e.do(t, r[0], r[1], r[2], body); code != http.StatusNotFound {
			t.Errorf("%s %s bei aus: %d, erwartet 404 (%v)", r[0], r[1], code, m)
		}
	}
	_, me := e.do(t, http.MethodGet, "/me", slAnna, nil)
	if _, da := me["schleuse"]; da {
		t.Errorf("/me meldet die Schleuse bei aus: %v", me)
	}
	_, st := e.do(t, http.MethodGet, "/admin/status", slAdmin, nil)
	if st["schleuse"] != "aus" {
		t.Errorf("Status schleuse = %v, erwartet aus", st["schleuse"])
	}
	if _, da := st["schleuse_anbieter_freigegeben"]; da {
		t.Errorf("Status nennt bei aus Anbieterzahlen: %v", st)
	}
}

// Admin-Freigabeliste: nur Admins, CRUD, Status zählt freigegeben/nutzbar,
// Abgleich mit der Ausgangsliste (ADR-0134 §1, zweiter Riegel).
func TestSchleuse_Freigabeliste_AdminUndAbgleich(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true, "api.anthropic.com")))
	e := neueSchleuseEnv(t, true, true)

	if code, _ := e.do(t, http.MethodGet, "/admin/schleuse/freigaben", slAnna, nil); code != http.StatusForbidden {
		t.Errorf("Nicht-Admin liest Freigabeliste: %d, erwartet 403", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAnna, slAnthropic); code != http.StatusForbidden {
		t.Errorf("Nicht-Admin legt an: %d, erwartet 403", code)
	}
	for _, kaputt := range []map[string]any{
		{"anbieter": "openai"},
		{"anbieter": "openai-kompatibel"}, // Basis-URL Pflicht
		{"anbieter": "mistral", "basis_url": "http://api.mistral.ai/v1"},
		{"anbieter": "mistral", "basis_url": "https://u:p@api.mistral.ai"},
		{"anbieter": "mistral", "modelle": []string{"a b"}},
		{"anbieter": "mistral", "unbekannt": true},
	} {
		if code, m := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAdmin, kaputt); code != http.StatusBadRequest {
			t.Errorf("ungültige Eingabe %v: %d %v, erwartet 400", kaputt, code, m)
		}
	}

	idA := e.anlegen(t, slAnthropic) // Host api.anthropic.com steht auf der Liste
	e.anlegen(t, map[string]any{"anbieter": "openai-kompatibel", "basis_url": "https://llm.example.net/v1/",
		"modelle": []string{"m1"}, "vertragsform_bestaetigt": true})
	e.anlegen(t, map[string]any{"anbieter": "mistral", "modelle": []string{"m"}}) // Vertragsform fehlt
	if code, _ := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAdmin, slAnthropic); code != http.StatusConflict {
		t.Errorf("doppelter Eintrag: %d, erwartet 409", code)
	}

	// Abgleich: nur der freigegebene, aber gesperrte Host warnt.
	e.mu.Lock()
	logs := strings.Join(e.logs, "\n")
	e.mu.Unlock()
	if !strings.Contains(logs, "llm.example.net") || strings.Contains(logs, "api.anthropic.com in ABOARD") || strings.Contains(logs, "api.mistral.ai") {
		t.Errorf("Abgleich-Warnungen unerwartet:\n%s", logs)
	}

	_, liste := e.do(t, http.MethodGet, "/admin/schleuse/freigaben", slAdmin, nil)
	if liste["ausgaenge"] != "aus" {
		t.Errorf("ausgaenge = %v", liste["ausgaenge"])
	}
	gruende := map[string]string{}
	for _, f := range liste["freigaben"].([]any) {
		m := f.(map[string]any)
		gruende[m["anbieter"].(string)] = fmt.Sprint(m["nutzbar"], m["gruende"])
		if m["anbieter"] == "openai-kompatibel" && m["basis_url"] != "https://llm.example.net/v1" {
			t.Errorf("Basis-URL nicht bereinigt: %v", m["basis_url"])
		}
		if m["anbieter"] == "anthropic" && (m["vertragsform_bestaetigt_von"] != "u-admin" || m["vertragsform_bestaetigt_am"] == "") {
			t.Errorf("Bestätigung ohne Admin/Zeitpunkt: %v", m)
		}
	}
	want := map[string]string{
		"anthropic":         "true []",
		"openai-kompatibel": "false [ausgang-gesperrt]",
		"mistral":           "false [vertragsform-fehlt ausgang-gesperrt]",
	}
	for k, v := range want {
		if gruende[k] != v {
			t.Errorf("%s: %q, erwartet %q", k, gruende[k], v)
		}
	}

	_, st := e.do(t, http.MethodGet, "/admin/status", slAdmin, nil)
	if st["schleuse"] != "an" || st["schleuse_anbieter_freigegeben"] != float64(2) || st["schleuse_anbieter_nutzbar"] != float64(1) || st["schleuse_aufbewahrung_tage"] != float64(30) {
		t.Errorf("Status: %v", st)
	}

	// Ändern: deaktivieren → nicht mehr freigegeben; Löschen.
	upd := map[string]any{"anbieter": "anthropic", "modelle": []string{"claude-x"}, "vertragsform_bestaetigt": true, "aktiv": false, "vier_augen": false}
	if code, m := e.do(t, http.MethodPut, "/admin/schleuse/freigaben/"+idA, slAdmin, upd); code != http.StatusOK || m["freigabe"].(map[string]any)["freigegeben"] != false {
		t.Errorf("ändern: %d %v", code, m)
	}
	if code, _ := e.do(t, http.MethodDelete, "/admin/schleuse/freigaben/"+idA, slAdmin, nil); code != http.StatusOK {
		t.Errorf("löschen: %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, "/admin/schleuse/freigaben/"+idA, slAdmin, nil); code != http.StatusNotFound {
		t.Errorf("zweites Löschen: %d, erwartet 404", code)
	}
}

// Schlüssel je Person: Bestätigung „geschäftliches Konto“ Pflicht, nur für
// freigegebene Anbieter, die Liste trägt nie den Schlüssel, /me meldet „an“.
func TestSchleuse_SchluesselJePerson(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	e := neueSchleuseEnv(t, true, true)
	id := e.anlegen(t, slAnthropic)
	idOhne := e.anlegen(t, map[string]any{"anbieter": "mistral", "modelle": []string{"m"}})

	_, me := e.do(t, http.MethodGet, "/me", slAnna, nil)
	if me["schleuse"] != "an" {
		t.Errorf("/me schleuse = %v, erwartet an", me["schleuse"])
	}

	const geheim = "sk-ant-GEHEIM-1234567890"
	pfad := "/users/me/schleuse-schluessel/" + id
	for _, b := range []map[string]any{
		mitStand(map[string]any{"key": geheim}),
		mitStand(map[string]any{"key": geheim, "geschaeftskonto_bestaetigt": false}),
		mitStand(map[string]any{"key": "", "geschaeftskonto_bestaetigt": true}),
		mitStand(map[string]any{"key": "a b", "geschaeftskonto_bestaetigt": true}),
		{"key": geheim, "geschaeftskonto_bestaetigt": true}, // ohne Stand (KC-2)
		{"key": geheim, "geschaeftskonto_bestaetigt": true, "anbieter": "anthropic"},
	} {
		if code, _ := e.do(t, http.MethodPut, pfad, slAnna, b); code != http.StatusBadRequest {
			t.Errorf("Body %v: %d, erwartet 400", b, code)
		}
	}
	if code, _ := e.do(t, http.MethodPut, "/users/me/schleuse-schluessel/"+idOhne, slAnna,
		mitStand(map[string]any{"key": geheim, "geschaeftskonto_bestaetigt": true, "anbieter": "mistral", "basis_url": "https://api.mistral.ai/v1"})); code != http.StatusConflict {
		t.Errorf("Schlüssel für nicht freigegebenen Anbieter: %d, erwartet 409", code)
	}
	if code, _ := e.do(t, http.MethodPut, "/users/me/schleuse-schluessel/gibts-nicht", slAnna,
		mitStand(map[string]any{"key": geheim, "geschaeftskonto_bestaetigt": true})); code != http.StatusNotFound {
		t.Errorf("unbekannte Freigabe: %d, erwartet 404", code)
	}
	if code, m := e.do(t, http.MethodPut, pfad, slAnna, mitStand(map[string]any{"key": geheim, "geschaeftskonto_bestaetigt": true})); code != http.StatusOK {
		t.Fatalf("setzen: %d %v", code, m)
	}

	code, liste := e.do(t, http.MethodGet, "/users/me/schleuse-schluessel", slAnna, nil)
	roh, _ := json.Marshal(liste)
	if code != http.StatusOK || strings.Contains(string(roh), "GEHEIM") || strings.Contains(string(roh), "key_encrypted") {
		t.Fatalf("Liste: %d %s", code, roh)
	}
	if n := len(liste["schluessel"].([]any)); n != 1 {
		t.Errorf("Annas Schlüssel: %d, erwartet 1", n)
	}
	if n := len(liste["anbieter"].([]any)); n != 1 {
		t.Errorf("angebotene Anbieter: %d, erwartet 1 (nur freigegebene)", n)
	} else if a := liste["anbieter"].([]any)[0].(map[string]any); a["basis_url"] != "https://api.anthropic.com" || a["anbieter"] != "anthropic" {
		t.Errorf("Liste trägt den Stand der Freigabe nicht (KC-2): %v", a)
	}
	_, bert := e.do(t, http.MethodGet, "/users/me/schleuse-schluessel", slBert, nil)
	if n := len(bert["schluessel"].([]any)); n != 0 {
		t.Errorf("Bert sieht %d Schlüssel, erwartet 0", n)
	}
	// In der Datenbank liegt der Schlüssel nur verschlüsselt.
	var enc string
	if err := e.db.QueryRow(`SELECT key_encrypted FROM schleuse_schluessel WHERE user_id = ?`, slAnnaID).Scan(&enc); err != nil || strings.Contains(enc, "GEHEIM") {
		t.Errorf("key_encrypted: %q %v", enc, err)
	}
	if code, _ := e.do(t, http.MethodDelete, pfad, slAnna, nil); code != http.StatusOK {
		t.Errorf("löschen: %d", code)
	}
	_, liste = e.do(t, http.MethodGet, "/users/me/schleuse-schluessel", slAnna, nil)
	if n := len(liste["schluessel"].([]any)); n != 0 {
		t.Errorf("nach Löschen %d Schlüssel", n)
	}
}

// Ohne Master-Key antworten die Schlüssel-Routen 503.
func TestSchleuse_SchluesselOhneMasterKey503(t *testing.T) {
	e := neueSchleuseEnv(t, true, false)
	id := e.anlegen(t, slAnthropic)
	if code, _ := e.do(t, http.MethodGet, "/users/me/schleuse-schluessel", slAnna, nil); code != http.StatusServiceUnavailable {
		t.Errorf("GET ohne Master-Key: %d, erwartet 503", code)
	}
	if code, _ := e.do(t, http.MethodPut, "/users/me/schleuse-schluessel/"+id, slAnna,
		mitStand(map[string]any{"key": "k", "geschaeftskonto_bestaetigt": true})); code != http.StatusServiceUnavailable {
		t.Errorf("PUT ohne Master-Key: %d, erwartet 503", code)
	}
}

// Board-Sperre: lesen ab viewer, stellen nur der Owner.
func TestSchleuse_BoardSperre_NurOwner(t *testing.T) {
	e := neueSchleuseEnv(t, true, true)
	pfad := "/boards/" + slBoard + "/schleuse-sperre"
	_, m := e.do(t, http.MethodGet, pfad, slBert, nil)
	if m["gesperrt"] != false || m["darf_aendern"] != false {
		t.Errorf("Editor liest: %v", m)
	}
	if code, _ := e.do(t, http.MethodPut, pfad, slBert, map[string]any{"gesperrt": true}); code != http.StatusForbidden {
		t.Errorf("Editor sperrt: %d, erwartet 403", code)
	}
	if code, _ := e.do(t, http.MethodPut, pfad, slAnna, map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("ohne gesperrt: %d, erwartet 400", code)
	}
	if code, m := e.do(t, http.MethodPut, pfad, slAnna, map[string]any{"gesperrt": true}); code != http.StatusOK {
		t.Fatalf("Owner sperrt: %d %v", code, m)
	}
	_, m = e.do(t, http.MethodGet, pfad, slAnna, nil)
	if m["gesperrt"] != true || m["darf_aendern"] != true {
		t.Errorf("Owner liest: %v", m)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE key = ? AND user_id = ? AND action = ?`,
		"schleuse-board-sperre:"+slBoard, slAnnaID, storage.AuditActionSchleuseBoardSperre).Scan(&n)
	if n != 1 {
		t.Errorf("Audit-Eintrag der Sperre: %d, erwartet 1", n)
	}
}

// ADR-0133 §2: Die Schleuse liest llm_api_keys nicht — am Quelltext der
// Routen-Datei geprüft (die Verhaltensprobe steht in storage/schleuse_test.go).
func TestSchleuse_RoutenKennenLLMKeysNicht(t *testing.T) {
	roh, err := os.ReadFile("schleuse.go")
	if err != nil {
		t.Fatal(err)
	}
	// Der Kopfkommentar nennt die Abgrenzung; geprüft wird der Code danach.
	code := string(roh)
	if i := strings.Index(code, "\nimport ("); i > 0 {
		code = code[i:]
	}
	for _, verboten := range []string{"llm_api_keys", "LLMKeyRepo", "LLMKeysAPI"} {
		if strings.Contains(code, verboten) {
			t.Errorf("api/schleuse.go nennt %s", verboten)
		}
	}
}

// KC-1: Interne Ziele lehnt schon die Route ab (400), nicht erst S3.
func TestSchleuse_Freigabe_InterneBasisAbgelehnt(t *testing.T) {
	e := neueSchleuseEnv(t, true, true)
	for _, basis := range []string{"https://127.0.0.1/v1", "https://localhost/v1", "https://169.254.169.254/latest",
		"https://[::1]/", "https://10.0.0.5:8443/", "https://0x7f.1/", "https://2130706433/"} {
		code, m := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAdmin,
			map[string]any{"anbieter": "openai-kompatibel", "basis_url": basis, "modelle": []string{"m"}})
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %v, erwartet 400", basis, code, m)
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM schleuse_freigaben`).Scan(&n)
	if n != 0 {
		t.Errorf("%d Freigabe(n) mit internem Ziel angelegt", n)
	}
}

// KC-4: Dieselbe Basis in anderer Schreibweise ist ein Doppel (409).
func TestSchleuse_Freigabe_NormalformGegenDoppel(t *testing.T) {
	e := neueSchleuseEnv(t, true, true)
	e.anlegen(t, map[string]any{"anbieter": "openai-kompatibel", "basis_url": "https://llm.example.net/v1", "modelle": []string{"m"}})
	for _, b := range []string{"https://LLM.example.net/v1/", "https://llm.example.net:443/v1", "https://llm.example.net./v1//"} {
		if code, m := e.do(t, http.MethodPost, "/admin/schleuse/freigaben", slAdmin,
			map[string]any{"anbieter": "openai-kompatibel", "basis_url": b, "modelle": []string{"m"}}); code != http.StatusConflict {
			t.Errorf("%s: %d %v, erwartet 409", b, code, m)
		}
	}
}

// KC-6: Der PUT ersetzt den ganzen Eintrag — fehlt aktiv oder
// vertragsform_bestaetigt, ist das 400, nicht still „aktiv“ bzw. „Bestätigung
// weg“.
func TestSchleuse_FreigabeAendern_PflichtfelderBeimPUT(t *testing.T) {
	e := neueSchleuseEnv(t, true, true)
	id := e.anlegen(t, map[string]any{"anbieter": "anthropic", "modelle": []string{"m"}, "vertragsform_bestaetigt": true, "aktiv": false, "vier_augen": true})
	pfad := "/admin/schleuse/freigaben/" + id
	for _, b := range []map[string]any{
		{"anbieter": "anthropic", "modelle": []string{"m"}, "vertragsform_bestaetigt": true, "vier_augen": true},
		{"anbieter": "anthropic", "modelle": []string{"m"}, "aktiv": false, "vier_augen": true},
		{"anbieter": "anthropic", "modelle": []string{"m"}, "aktiv": false, "vertragsform_bestaetigt": true}, // Verify VS-1
		{"anbieter": "anthropic", "modelle": []string{"m"}},
	} {
		if code, m := e.do(t, http.MethodPut, pfad, slAdmin, b); code != http.StatusBadRequest {
			t.Errorf("PUT %v: %d %v, erwartet 400", b, code, m)
		}
	}
	var aktiv, vertrag, vier int
	_ = e.db.QueryRow(`SELECT aktiv, vertragsform_bestaetigt, vier_augen FROM schleuse_freigaben WHERE id = ?`, id).Scan(&aktiv, &vertrag, &vier)
	if aktiv != 0 || vertrag != 1 || vier != 1 {
		t.Errorf("Eintrag verändert: aktiv=%d vertragsform=%d vier_augen=%d, erwartet 0/1/1", aktiv, vertrag, vier)
	}
	if code, m := e.do(t, http.MethodPut, pfad, slAdmin, map[string]any{"anbieter": "anthropic", "modelle": []string{"m"},
		"vertragsform_bestaetigt": true, "aktiv": true, "vier_augen": true}); code != http.StatusOK {
		t.Errorf("vollständiger PUT: %d %v", code, m)
	}
}

// KC-2: Der Admin stellt die Basis-URL um, während der Schlüsseldialog offen
// ist. Der PUT mit dem alten Stand bekommt 409 und schreibt nichts.
func TestSchleuse_SchluesselSetzen_VeralteterStand409(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	e := neueSchleuseEnv(t, true, true)
	id := e.anlegen(t, map[string]any{"anbieter": "openai-kompatibel", "basis_url": "https://a.example.net/v1",
		"modelle": []string{"m"}, "vertragsform_bestaetigt": true})
	_, liste := e.do(t, http.MethodGet, "/users/me/schleuse-schluessel", slAnna, nil)
	a := liste["anbieter"].([]any)[0].(map[string]any)
	alterStand := map[string]any{"anbieter": a["anbieter"], "basis_url": a["basis_url"]}

	if code, m := e.do(t, http.MethodPut, "/admin/schleuse/freigaben/"+id, slAdmin, map[string]any{"anbieter": "openai-kompatibel",
		"basis_url": "https://b.example.net/v1", "modelle": []string{"m"}, "vertragsform_bestaetigt": true, "aktiv": true, "vier_augen": false}); code != http.StatusOK {
		t.Fatalf("ändern: %d %v", code, m)
	}
	body := map[string]any{"key": "sk-x", "geschaeftskonto_bestaetigt": true}
	for k, v := range alterStand {
		body[k] = v
	}
	if code, m := e.do(t, http.MethodPut, "/users/me/schleuse-schluessel/"+id, slAnna, body); code != http.StatusConflict {
		t.Errorf("alter Stand: %d %v, erwartet 409", code, m)
	}
	var n int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM schleuse_schluessel`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d Schlüssel trotz veraltetem Stand geschrieben", n)
	}
	body["basis_url"] = "https://b.example.net/v1"
	if code, m := e.do(t, http.MethodPut, "/users/me/schleuse-schluessel/"+id, slAnna, body); code != http.StatusOK {
		t.Errorf("aktueller Stand: %d %v", code, m)
	}
}

// KC-7: Sperren darf nur ein persönlich eingetragener Owner. Eine Domain-
// Freigabe (ADR-0132, höchstens editor) sperrt nicht; wer gar nicht Mitglied
// ist, bekommt 403 — lesend wie schreibend.
func TestSchleuse_BoardSperre_DomainEditorUndNichtMitglied(t *testing.T) {
	t.Setenv("ABOARD_ACL_DOMAINS", "firma.de")
	e := neueSchleuseEnv(t, true, true)
	for _, u := range [][2]string{{"u-carla", "carla@firma.de"}, {"u-dora", "dora@example.com"}} {
		if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.NewAclRepo(e.db).Set(context.Background(), slBoard, "@firma.de", storage.RoleEditor); err != nil {
		t.Fatalf("Domain-Eintrag: %v", err)
	}
	pfad := "/boards/" + slBoard + "/schleuse-sperre"
	if code, m := e.do(t, http.MethodGet, pfad, "carla@firma.de", nil); code != http.StatusOK || m["darf_aendern"] != false {
		t.Errorf("Domain-Editor liest: %d %v, erwartet 200 ohne darf_aendern", code, m)
	}
	if code, m := e.do(t, http.MethodPut, pfad, "carla@firma.de", map[string]any{"gesperrt": true}); code != http.StatusForbidden {
		t.Errorf("Domain-Editor sperrt: %d %v, erwartet 403", code, m)
	}
	if code, m := e.do(t, http.MethodGet, pfad, "dora@example.com", nil); code != http.StatusForbidden {
		t.Errorf("Nicht-Mitglied liest: %d %v, erwartet 403", code, m)
	}
	if code, m := e.do(t, http.MethodPut, pfad, "dora@example.com", map[string]any{"gesperrt": true}); code != http.StatusForbidden {
		t.Errorf("Nicht-Mitglied sperrt: %d %v, erwartet 403", code, m)
	}
	if gesperrt, _ := storage.SchleuseBoardSperre(context.Background(), e.db, slBoard); gesperrt {
		t.Errorf("Board ist gesperrt")
	}
}
