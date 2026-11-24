package main

// Karte #778 (ADR-0140 §2): Der Schatten-Ausgang ist nur über den
// Agenten-Eingang mit einem abs_-Token erreichbar, nur bei boardpaar.seite =
// innen und nur für gekoppelte Boards. Das Token erreicht sonst nichts (403
// an beiden Ketten). Geprüft an der echten Kette aus hauptHandler im
// strengen Forward-Auth-Modus.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchattenAusgang778_Wege(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"Board-Titel-geheim","nextDisplayId":3,"columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "identities":[{"id":"u","kind":"user","label":"Gerd","email":"gerd@example.org"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[
	    {"id":"k1","displayId":1,"title":"Offen","columnId":"col-ready","owner":"Gerd","description":"Beschreibung-geheim","comments":[]},
	    {"id":"k2","displayId":2,"title":"Titel-geheim","columnId":"col-ready","confidential":true,"comments":[]}]}]}}`
	for _, id := range []string{"b1", "b2"} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(strings.ReplaceAll(state, `"b1"`, `"`+id+`"`))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := boards.Insert(ctx, "kaputt", "K", "2", []byte(`{"board":{"sprints":{}}}`)); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	acl := storage.NewAclRepo(db)
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	paar := storage.NewBoardpaarRepo(db)
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).WithFold(motor).Register(mux)
	api.NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	abs, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	abe, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "eingang", "test")
	if err != nil {
		t.Fatal(err)
	}
	altAbs, alt, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "alt", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := paar.TokenWiderrufen(ctx, alt.ID); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"b1", "kaputt"} {
		if err := paar.InnenKoppeln(ctx, b, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := paar.InnenKoppeln(ctx, storage.ServiceBoardID, "test"); err == nil {
		t.Fatal("Service-Board ließ sich koppeln")
	}
	agenten := storage.NewAgentTokenRepo(db)
	abt, _, err := agenten.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test")
	if err != nil {
		t.Fatal(err)
	}
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(agenten), true))
	t.Cleanup(srv.Close)

	pruefe := func(stufe string, faelle []struct {
		name, methode, pfad, token string
		want                       int
	}) {
		t.Helper()
		for _, f := range faelle {
			if s := agentAnfrage(t, srv, f.methode, f.pfad, f.token, "", nil); s != f.want {
				t.Errorf("%s / %s: %d, erwartet %d", stufe, f.name, s, f.want)
			}
		}
	}
	type fall = struct {
		name, methode, pfad, token string
		want                       int
	}

	// Seite „aus“ (nichts gesetzt): Den Weg gibt es nicht, auch nicht als
	// 401. Ohne abs_-Token entscheidet die Agenten-Kette (401) wie für jeden
	// anderen Pfad, bevor der Handler läuft.
	pruefe("aus", []fall{
		{"gekoppeltes Board", "GET", "/agent/schatten/b1", abs, 404},
		{"ohne Token", "GET", "/agent/schatten/b1", "", 401},
	})
	for _, wert := range []string{"aussen", "Innen", "innen "} {
		if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, wert, false, "test"); err != nil {
			t.Fatal(err)
		}
		want := 404
		if wert == "innen " { // getrimmt gültig
			want = 200
		}
		pruefe("seite="+wert, []fall{{"gekoppeltes Board", "GET", "/agent/schatten/b1", abs, want}})
	}
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "test"); err != nil {
		t.Fatal(err)
	}
	pruefe("innen", []fall{
		{"gekoppeltes Board", "GET", "/agent/schatten/b1", abs, 200},
		{"HEAD", "HEAD", "/agent/schatten/b1", abs, 200},
		{"nicht gekoppelt", "GET", "/agent/schatten/b2", abs, 404},
		{"unbekanntes Board", "GET", "/agent/schatten/gibtsnicht", abs, 404},
		{"Service-Board", "GET", "/agent/schatten/service", abs, 404},
		{"kaputter Stand", "GET", "/agent/schatten/kaputt", abs, 500},
		{"ohne Token", "GET", "/agent/schatten/b1", "", 401},
		{"widerrufenes Token", "GET", "/agent/schatten/b1", altAbs, 401},
		{"erfundenes abs_", "GET", "/agent/schatten/b1", "abs_erfunden", 401},
		// abs_ auf jedem anderen Weg: 403, an beiden Ketten.
		{"abs_ am Menschen-Pfad", "GET", "/schatten/b1", abs, 403},
		{"abs_ auf Board (Menschen)", "GET", "/boards/b1", abs, 403},
		{"abs_ auf Board (/agent/)", "GET", "/agent/boards/b1", abs, 403},
		{"abs_ Ops (/agent/)", "POST", "/agent/boards/b1/ops", abs, 403},
		{"abs_ PUT auf Schatten", "PUT", "/agent/schatten/b1", abs, 403},
		{"abs_ Unterpfad", "GET", "/agent/schatten/b1/x", abs, 403},
		{"abs_ auf Kalender-Ausgang", "GET", "/agent/ical/aussen/", abs, 403},
		// abe_ hat innen keinen Weg.
		{"abe_ auf Ausgang", "GET", "/agent/schatten/b1", abe, 403},
		{"abe_ am Menschen-Pfad", "GET", "/boards/b1", abe, 403},
		// Ein Agenten-Token kommt an den Ausgang nicht heran.
		{"Agenten-Token (/agent/)", "GET", "/agent/schatten/b1", abt, 403},
		{"Agenten-Token (Menschen)", "GET", "/schatten/b1", abt, 401},
	})

	// Inhalt: Hülle ohne Board-ID, vertraulicher Titel fehlt, keine
	// Beschreibung, keine E-Mail.
	req, _ := http.NewRequest("GET", srv.URL+"/agent/schatten/b1", nil)
	req.Header.Set("Authorization", "Bearer "+abs)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, geheim := range []string{"b1", "Board-Titel-geheim", "Titel-geheim", "Beschreibung-geheim", "gerd@"} {
		if strings.Contains(string(body), geheim) {
			t.Errorf("Antwort enthält %q: %s", geheim, body)
		}
	}
	var hülle map[string]json.RawMessage
	if err := json.Unmarshal(body, &hülle); err != nil {
		t.Fatal(err)
	}
	if len(hülle) != 3 || hülle["version"] == nil || hülle["stand"] == nil || hülle["schatten"] == nil {
		t.Errorf("Hülle: %s", body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: %q", resp.Header.Get("Cache-Control"))
	}
}
