package main

// Karte #796 (T-R12): POST /agent/boardpaar/erneuern an der echten Kette aus
// hauptHandler. Nur über den Agenten-Eingang, nur mit dem Typ der Seite
// (abs_ innen, abe_ außen), bei „aus“ gar nicht; das Token erreicht damit
// nichts weiter, und an der Menschen-Kette prallt es an der Schranke ab.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

func TestBoardpaarErneuern796_Wege(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	paar := storage.NewBoardpaarRepo(db)
	if err := paar.InnenKoppeln(ctx, "b1", "test"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	api.NewSchattenEingangAPI(cfg, paar, storage.NewSchattenRepo(db), nil).Register(mux)
	api.NewBoardpaarErneuernAPI(cfg, paar).Register(mux)
	agenten := storage.NewAgentTokenRepo(db)
	abt, _, err := agenten.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test")
	if err != nil {
		t.Fatal(err)
	}
	abs, _, _ := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	abe, _, _ := paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "eingang", "test")
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(agenten), true))
	t.Cleanup(srv.Close)

	type fall = struct {
		name, methode, pfad, token string
		want                       int
	}
	pruefe := func(stufe string, faelle []fall) {
		t.Helper()
		for _, f := range faelle {
			if s := agentAnfrage(t, srv, f.methode, f.pfad, f.token, "", nil); s != f.want {
				t.Errorf("%s / %s: %d, erwartet %d", stufe, f.name, s, f.want)
			}
		}
	}
	const weg = "/agent/boardpaar/erneuern"

	// Seite „aus“: den Weg gibt es nicht.
	pruefe("aus", []fall{
		{"abs_", "POST", weg, abs, 404},
		{"abe_", "POST", weg, abe, 404},
	})
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "test"); err != nil {
		t.Fatal(err)
	}
	pruefe("innen, ohne Erneuerung", []fall{
		{"abe_ hat innen keinen Erneuern-Weg", "POST", weg, abe, 401},
		{"erfundenes abs_", "POST", weg, "abs_erfunden", 401},
		{"ohne Token", "POST", weg, "", 401},
		// Ein Agenten-Token geht durch die Agenten-Middleware, und die kennt
		// den Weg nicht: 403, der Handler läuft gar nicht.
		{"Agenten-Token", "POST", weg, abt, 403},
		{"abs_ GET", "GET", weg, abs, 403},
		{"abs_ Unterpfad", "POST", weg + "/x", abs, 403},
		{"abs_ am Menschen-Pfad", "POST", "/boardpaar/erneuern", abs, 403},
	})

	// Erneuern: neues Token, altes gilt in der Übergangszeit, Wiederholung
	// liefert dasselbe; das neue öffnet den Ausgang.
	erneuere := func(token string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+weg, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if cc := resp.Header.Get("Cache-Control"); resp.StatusCode == 200 && cc != "no-store" {
			t.Errorf("Cache-Control = %q, erwartet no-store", cc)
		}
		body, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		return resp.StatusCode, m
	}
	status, erste := erneuere(abs)
	if status != 200 {
		t.Fatalf("Erneuern: %d %v", status, erste)
	}
	neu, _ := erste["token"].(string)
	if !strings.HasPrefix(neu, "abs_") || neu == abs || erste["name"] != "push" || erste["typ"] != "abs" || erste["wiederholt"] != false || erste["alt_gilt_bis"] == "" {
		t.Fatalf("Antwort: %v", erste)
	}
	status, zweite := erneuere(abs)
	if status != 200 || zweite["token"] != neu || zweite["wiederholt"] != true {
		t.Errorf("Wiederholung: %d %v", status, zweite)
	}
	pruefe("nach Erneuerung", []fall{
		{"neues Token am Ausgang", "GET", "/agent/schatten/b1", neu, 200},
		{"altes Token in der Übergangszeit", "GET", "/agent/schatten/b1", abs, 200},
		{"neues Token am Menschen-Pfad", "GET", "/boards/b1", neu, 403},
	})
	// Protokoll: Nachfolger trägt die Quelladresse.
	liste, _ := paar.TokenListe(ctx)
	gefunden := false
	for _, z := range liste {
		if z.ErneuertAus != "" {
			gefunden = true
			if z.ErneuertVon == "" {
				t.Errorf("Nachfolger ohne Adresse: %+v", z)
			}
		}
	}
	if !gefunden {
		t.Error("kein erneuerter Zugang in der Liste")
	}

	// Verify F2: Vorgänger in der Übergangszeit bei ungestellter Uhr → 503
	// am Ausgang, nicht 401; das aktuelle Token geht weiter durch.
	altUhr := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = altUhr })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	pruefe("ungestellte Uhr", []fall{
		{"Vorgänger am Ausgang", "GET", "/agent/schatten/b1", abs, 503},
		{"aktuelles Token am Ausgang", "GET", "/agent/schatten/b1", neu, 200},
	})
	// Verify 2, F-B: der Header sagt dem Skript, wann es wiederkommen soll.
	pruefeRetryAfter := func(methode, pfad, token string) {
		t.Helper()
		req, _ := http.NewRequest(methode, srv.URL+pfad, strings.NewReader(`{"version":"1","schatten":[]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "300" {
			t.Errorf("%s %s: %d, Retry-After %q; erwartet 503 mit Retry-After 300", methode, pfad, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
	pruefeRetryAfter("GET", "/agent/schatten/b1", abs)
	uhr.Gestellt = altUhr

	// Außen: abe_ erneuert, abs_ nicht.
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "aussen", false, "test"); err != nil {
		t.Fatal(err)
	}
	pruefe("aussen", []fall{{"abs_ hat außen keinen Erneuern-Weg", "POST", weg, neu, 401}})
	if status, m := erneuere(abe); status != 200 || !strings.HasPrefix(m["token"].(string), "abe_") {
		t.Errorf("abe_ erneuern: %d %v", status, m)
	}
	// Verify 2, F-B: auch der Eingang (PUT) antwortet dem Vorgänger bei
	// ungestellter Uhr 503 mit Retry-After — vor dem Lesen des Bodys.
	uhr.Gestellt = func() (bool, error) { return false, nil }
	pruefeRetryAfter("PUT", "/agent/schatten/b1", abe)
	uhr.Gestellt = altUhr
}

// erneuernTestServer: Seite innen, ein abs_-Zugang, die echte Kette.
func erneuernTestServer(t *testing.T) (*httptest.Server, *storage.BoardpaarRepo, string, *sql.DB) {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	mux := http.NewServeMux()
	api.NewBoardpaarErneuernAPI(cfg, paar).Register(mux)
	abs, _, _ := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(storage.NewAgentTokenRepo(db)), true))
	t.Cleanup(srv.Close)
	return srv, paar, abs, db
}

// Kritiker V-1: Die gespeicherte Adresse folgt der Proxy-Regel — ein
// X-Forwarded-For vom Client landet nicht im Nachweis.
func TestBoardpaarErneuern796_AdresseOhneXForwardedFor(t *testing.T) {
	srv, paar, abs, _ := erneuernTestServer(t)
	kopf := map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.10"}
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", abs, "", kopf); s != 200 {
		t.Fatalf("Erneuern: %d", s)
	}
	liste, _ := paar.TokenListe(context.Background())
	for _, z := range liste {
		if z.ErneuertAus != "" && z.ErneuertVon != "127.0.0.1" {
			t.Errorf("erneuert_von = %q, erwartet die TCP-Gegenstelle 127.0.0.1", z.ErneuertVon)
		}
	}
}

// Kritiker V-2: eigenes Kontingent von 30 Fehlversuchen je Stunde, danach
// 429; ein gültiges Token zählt nicht und kommt auch danach nicht durch die
// Drossel ins Stolpern.
func TestBoardpaarErneuern796_Drossel(t *testing.T) {
	srv, _, abs, _ := erneuernTestServer(t)
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", abs, "", nil); s != 200 {
		t.Fatalf("Erneuern: %d", s)
	}
	for i := 1; i <= 30; i++ {
		if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", "abs_erfunden", "", nil); s != 401 {
			t.Fatalf("Fehlversuch %d: %d, erwartet 401", i, s)
		}
	}
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", "abs_erfunden", "", nil); s != 429 {
		t.Errorf("31. Fehlversuch: %d, erwartet 429", s)
	}
}

// Kritiker V-3: Ein kodierter Schrägstrich ist nicht der Erneuern-Weg.
func TestBoardpaarErneuern796_KodierterPfad(t *testing.T) {
	srv, _, abs, _ := erneuernTestServer(t)
	for _, pfad := range []string{"/agent/boardpaar%2Ferneuern", "/agent/boardpaar/erneuern/", "/agent//boardpaar/erneuern"} {
		if s := agentAnfrage(t, srv, "POST", pfad, abs, "", nil); s != 403 {
			t.Errorf("%s: %d, erwartet 403", pfad, s)
		}
	}
}

// Kritiker K-1: ungestellte Uhr → 503 mit Retry-After, nichts geschrieben.
// Kritiker F-B: eine Wiederholung von anderer Adresse steht als WARNUNG im Log.
func TestBoardpaarErneuern796_UhrUndWarnung(t *testing.T) {
	srv, paar, abs, db := erneuernTestServer(t)
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	req, _ := http.NewRequest("POST", srv.URL+"/agent/boardpaar/erneuern", nil)
	req.Header.Set("Authorization", "Bearer "+abs)
	resp, err := http.DefaultClient.Do(req)
	uhr.Gestellt = alt
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Errorf("ungestellte Uhr: %d, Retry-After %q; erwartet 503 mit Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if liste, _ := paar.TokenListe(context.Background()); len(liste) != 1 {
		t.Errorf("bei ungestellter Uhr %d Zugänge, erwartet 1", len(liste))
	}

	var puffer bytes.Buffer
	log.SetOutput(&puffer)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", abs, "", nil); s != 200 {
		t.Fatalf("Erneuern: %d", s)
	}
	if strings.Contains(puffer.String(), "WARNUNG") {
		t.Errorf("Warnung schon bei der ersten Erneuerung: %s", puffer.String())
	}
	// Dieselbe Adresse wiederholt: keine Warnung.
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", abs, "", nil); s != 200 {
		t.Fatalf("Wiederholung: %d", s)
	}
	if strings.Contains(puffer.String(), "WARNUNG") {
		t.Errorf("Warnung bei Wiederholung von derselben Adresse: %s", puffer.String())
	}
	// Andere Adresse: im Nachweis steht die erste; hier von Hand umgestellt,
	// weil der Test nur eine Gegenstelle hat.
	if _, err := db.Exec(`UPDATE boardpaar_tokens SET erneuert_von = '192.0.2.1' WHERE erneuert_aus IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if s := agentAnfrage(t, srv, "POST", "/agent/boardpaar/erneuern", abs, "", nil); s != 200 {
		t.Fatalf("Wiederholung: %d", s)
	}
	if !strings.Contains(puffer.String(), "WARNUNG") || !strings.Contains(puffer.String(), "192.0.2.1") {
		t.Errorf("keine Warnung bei Wiederholung von anderer Adresse: %s", puffer.String())
	}
}

// Verify F5: `boardpaar liste` nennt einen Vorgänger nach der Übergangszeit
// „abgelöst“, wie der Admin-Reiter.
func TestBoardpaarErneuern796_ListeText(t *testing.T) {
	vorbei := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	kommt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if s := zugangText(storage.BoardpaarToken{GiltBis: vorbei}); !strings.HasPrefix(s, "abgelöst") {
		t.Errorf("abgelaufener Vorgänger: %q", s)
	}
	if s := zugangText(storage.BoardpaarToken{GiltBis: kommt}); !strings.HasPrefix(s, "erneuert, gilt bis") {
		t.Errorf("Vorgänger in der Übergangszeit: %q", s)
	}
	if s := zugangText(storage.BoardpaarToken{ErneuertAus: "x", ErneuertVon: "192.0.2.7", CreatedAt: "t"}); !strings.Contains(s, "erneuert t von 192.0.2.7") {
		t.Errorf("Nachfolger: %q", s)
	}
}
