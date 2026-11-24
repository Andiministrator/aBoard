package main

// Startprüfungen und Einrichtung der eingebauten Anmeldung (ADR-0148 §1, §3;
// Karte #816).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

const testMasterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func setzeOIDCUmgebung(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"ABOARD_ANMELDUNG":          "oidc",
		"ABOARD_OIDC_ISSUER":        "https://accounts.google.com",
		"ABOARD_OIDC_CLIENT_ID":     "client-1",
		"ABOARD_OIDC_CLIENT_SECRET": "geheim",
		"ABOARD_OIDC_ZUGELASSEN":    "anna@example.com",
		"ABOARD_SECRET_KEY":         testMasterKey,
		"ABOARD_TRUST_FORWARD_AUTH": "",
		"ABOARD_DEV_FAKE_USER":      "",
		"ABOARD_DEV_FAKE_EMAIL":     "",
		"ABOARD_BOOTSTRAP_OWNER":    "",
	} {
		t.Setenv(k, v)
	}
}

func TestAnmeldungKonfig816_GueltigeUmgebung(t *testing.T) {
	setzeOIDCUmgebung(t)
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("gültige Umgebung abgewiesen: %v", err)
	}
	t.Setenv("ABOARD_ANMELDUNG", "")
	for _, v := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_OIDC_ZUGELASSEN"} {
		t.Setenv(v, "")
	}
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("ohne OIDC abgewiesen: %v", err)
	}
}

// K2 F-A (Sprint-Abschluss 106–108): OIDC-Variablen ohne Schalter brechen den
// Start ab, statt still im auth-losen Einzelbetrieb zu laufen.
func TestAnmeldungKonfig_OIDCVariablenOhneSchalter(t *testing.T) {
	for _, v := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_OIDC_ZUGELASSEN"} {
		t.Run(v, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv("ABOARD_ANMELDUNG", "")
			for _, w := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_OIDC_ZUGELASSEN"} {
				if w != v {
					t.Setenv(w, "")
				}
			}
			err := checkAnmeldungKonfig()
			if err == nil || !strings.Contains(err.Error(), v) {
				t.Fatalf("%s gesetzt ohne ABOARD_ANMELDUNG=oidc: err=%v — erwartet Startabbruch mit Namen", v, err)
			}
		})
	}
}

func TestAnmeldungKonfig816_Startabbrueche(t *testing.T) {
	faelle := map[string][2]string{
		"unbekannter Wert":      {"ABOARD_ANMELDUNG", "ja"},
		"Forward-Auth zugleich": {"ABOARD_TRUST_FORWARD_AUTH", "1"},
		"Dev-Fake-User":         {"ABOARD_DEV_FAKE_USER", "dev"},
		"Dev-Fake-Email":        {"ABOARD_DEV_FAKE_EMAIL", "dev@example.com"},
		"Issuer fehlt":          {"ABOARD_OIDC_ISSUER", ""},
		"Issuer http":           {"ABOARD_OIDC_ISSUER", "http://accounts.google.com"},
		"Client-ID fehlt":       {"ABOARD_OIDC_CLIENT_ID", ""},
		"Client-Secret fehlt":   {"ABOARD_OIDC_CLIENT_SECRET", " "},
		"Secret-Key fehlt":      {"ABOARD_SECRET_KEY", ""},
		"Secret-Key ungültig":   {"ABOARD_SECRET_KEY", "kurz"},
		"Liste ungültig":        {"ABOARD_OIDC_ZUGELASSEN", "anna"},
	}
	for name, kv := range faelle {
		t.Run(name, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(kv[0], kv[1])
			if err := checkAnmeldungKonfig(); err == nil {
				t.Fatalf("%s=%q: Start nicht abgebrochen", kv[0], kv[1])
			}
		})
	}
}

// Verdrahtung: runPreflight ruft die Prüfung auf (Muster G9, F-1143).
func TestAnmeldungKonfig816_InRunPreflight(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv("ABOARD_OIDC_CLIENT_SECRET", "")
	err := runPreflight("127.0.0.1:0", "")
	if err == nil || !strings.Contains(err.Error(), "ABOARD_OIDC_CLIENT_SECRET") {
		t.Fatalf("runPreflight: %v — erwartet Abbruch wegen fehlendem Client-Secret", err)
	}
}

func TestAnmeldungBasis816_HttpsPflichtNurImOIDCBetrieb(t *testing.T) {
	setzeOIDCUmgebung(t)
	for basis, ok := range map[string]bool{"https://pm.example.de": true, "http://pm.example.de": false, "": false,
		"https://pm.example.de/board": false} { // Kritik A-5
		if err := pruefeAnmeldungBasis(basis); (err == nil) != ok {
			t.Errorf("basis %q: err=%v", basis, err)
		}
	}
	t.Setenv("ABOARD_ANMELDUNG", "")
	if err := pruefeAnmeldungBasis(""); err != nil {
		t.Fatalf("ohne OIDC: %v", err)
	}
}

// Ende-zu-Ende über anmeldungEinrichten: Routen angemeldet, Weiche mit der
// Liste verdrahtet. Wer aus der Liste gestrichen wird, ist nach dem Neustart
// trotz gültiger Sitzung draussen (401).
func TestAnmeldungEinrichten816_ListeGiltBeiJederAnfrage(t *testing.T) {
	setzeOIDCUmgebung(t)
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, abbrechen := context.WithCancel(context.Background())
	t.Cleanup(abbrechen)
	// Anbieter nicht erreichbar: die Einrichtung darf trotzdem gelingen.
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")

	baue := func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		opts, err := anmeldungEinrichten(ctx, db, mux, "https://board.example")
		if err != nil || len(opts) != 1 {
			t.Fatalf("anmeldungEinrichten: %v (%d Optionen)", err, len(opts))
		}
		return hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
	}
	h := baue()

	// /anmeldung/start ist angemeldet und antwortet 503 (Anbieter weg).
	rec := httptest.NewRecorder()
	// Mit Kürzel: ohne leitet Start seit #1074 zur Seite (Passwort ist ein
	// zweiter Weg).
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+api.AnmeldungStartPfad+"?anbieter=standard", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/anmeldung/start bei unerreichbarem Anbieter: %d, erwartet 503", rec.Code)
	}

	id, _, err := storage.NewSitzungRepo(db).Anlegen(context.Background(), "anna@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	anfrage := func() int {
		r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
		r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := anfrage(); c != http.StatusOK {
		t.Fatalf("zugelassene Sitzung: %d", c)
	}
	t.Setenv("ABOARD_OIDC_ZUGELASSEN", "bert@example.com")
	h = baue() // „Neustart“ mit geänderter Liste
	if c := anfrage(); c != http.StatusUnauthorized {
		t.Fatalf("gestrichene Adresse mit gültiger Sitzung: %d, erwartet 401", c)
	}
	// Kritik A-2: Der Neustart hat die Sitzung gelöscht — wieder zugelassen,
	// lebt der alte Cookie nicht auf.
	t.Setenv("ABOARD_OIDC_ZUGELASSEN", "anna@example.com")
	h = baue()
	if c := anfrage(); c != http.StatusUnauthorized {
		t.Fatalf("alte Sitzung nach Wiederaufnahme in die Liste: %d, erwartet 401", c)
	}
	// Bootstrap-Owner gilt als zugelassen.
	t.Setenv("ABOARD_OIDC_ZUGELASSEN", "bert@example.com")
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "Anna@Example.com")
	h = baue()
	id, _, err = storage.NewSitzungRepo(db).Anlegen(context.Background(), "anna@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := anfrage(); c != http.StatusOK {
		t.Fatalf("Bootstrap-Owner: %d", c)
	}
}

func TestAnmeldungEinrichten816_OhneOIDCNichts(t *testing.T) {
	t.Setenv("ABOARD_ANMELDUNG", "")
	mux := http.NewServeMux()
	opts, err := anmeldungEinrichten(context.Background(), nil, mux, "")
	if err != nil || opts != nil {
		t.Fatalf("ohne OIDC: opts=%v err=%v", opts, err)
	}
	for _, pfad := range []string{api.AnmeldungStartPfad, api.AnmeldungAbgemeldetPfad} {
		if _, muster := mux.Handler(httptest.NewRequest(http.MethodGet, pfad, nil)); muster != "" {
			t.Fatalf("ohne OIDC ist %s angemeldet (%q)", pfad, muster)
		}
	}
}

// run() startet keinen Server im Test; die Verdrahtung prüft deshalb der
// Quelltext (Muster TestOeffentlicheBasis_NurEineLesestelle).
func TestAnmeldung816_RunVerdrahtet(t *testing.T) {
	roh, err := mainQuelltext()
	if err != nil {
		t.Fatal(err)
	}
	for _, muss := range []string{
		"pruefeAnmeldungBasis(oeffentlicheBasis)",
		"anmeldungEinrichten(ctx, db, mux, oeffentlicheBasis, adminAPI)",
		"hauptHandler(mux, aclGuard.Middleware, agentZugang, mitAgentPfad, kettenOpts...)",
	} {
		if strings.Count(roh, muss) != 1 {
			t.Errorf("main.go enthält %q nicht genau einmal", muss)
		}
	}
}

// Kritik A-6: Der OIDC-Betrieb ist überall Mehrnutzer-Betrieb.
func TestAnmeldung816_OIDCIstMehrnutzer(t *testing.T) {
	setzeOIDCUmgebung(t)
	if !api.StrengeAnmeldung() {
		t.Fatal("OIDC-Betrieb gilt nicht als strenge Anmeldung")
	}
	t.Setenv("ABOARD_ANMELDUNG", "")
	if api.StrengeAnmeldung() {
		t.Fatal("Einzelbetrieb gilt als strenge Anmeldung")
	}
}

// Verify N-1 (Sprint-Abschluss 106–108): /anmeldung/abgemeldet ist im
// OIDC-Betrieb ohne Sitzung erreichbar (200, keine Weiterleitung) — über
// anmeldungEinrichten und die Weiche, nicht nur am Handler.
func TestAnmeldungEinrichten_AbgemeldetOhneSitzung(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, abbrechen := context.WithCancel(context.Background())
	t.Cleanup(abbrechen)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	opts, err := anmeldungEinrichten(ctx, db, mux, "https://board.example")
	if err != nil || len(opts) != 1 {
		t.Fatalf("anmeldungEinrichten: %v (%d Optionen)", err, len(opts))
	}
	h := hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+api.AnmeldungAbgemeldetPfad, nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" ||
		!strings.Contains(rec.Body.String(), api.AnmeldungSeitePfad+"?"+api.AnmeldungKontoWahlParam+"=1") {
		t.Fatalf("/anmeldung/abgemeldet: %d Location=%q — erwartet 200 mit Link zur Kontowahl", rec.Code, rec.Header().Get("Location"))
	}
}
