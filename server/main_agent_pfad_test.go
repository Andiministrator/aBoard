package main

// Karte #616 (Nachtrag zu ADR-0129 K-10): Agenten erreichen den Server auch
// unter /agent/ am Menschen-Listener — ein Container, ein Port. Geprüft wird
// die echte Kette aus hauptHandler, im strengen Forward-Auth-Modus wie auf
// einem Hoster (Trust an, Secret gesetzt).

import (
	"bufio"
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

const pfadTestSecret = "geheim-616" // abzug:testwert

func pfadTestServer(t *testing.T, mitPfad bool) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	return pfadTestServerModus(t, mitPfad, true)
}

// pfadTestServerModus: streng = Forward-Auth mit Secret (Hoster), sonst
// ohne Forward-Auth (Legacy-Bypass).
func pfadTestServerModus(t *testing.T, mitPfad, streng bool) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	if streng {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
		t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	} else {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
		t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "")
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"Pfadboard","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Pfadboard", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	broker := api.NewEventBroker()
	api.NewBoardsAPI(boards).WithBroker(broker).WithACL(storage.NewAclRepo(db)).
		WithActivities(storage.NewActivityRepo(db)).WithFold(motor).Register(mux)
	api.NewEventsAPI(broker, boards).Register(mux)
	tokens := storage.NewAgentTokenRepo(db)
	klartext, _, err := tokens.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test")
	if err != nil {
		t.Fatal(err)
	}
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(tokens), mitPfad))
	t.Cleanup(srv.Close)
	return srv, db, klartext
}

var menschKopf = map[string]string{
	"Remote-User": "andi", "Remote-Email": "andi@example.org",
	"X-Aboard-Forward-Auth-Secret": pfadTestSecret, "Sec-Fetch-Site": "same-origin",
}

func TestAgentPfad_EinPortBeideEingaenge(t *testing.T) {
	srv, db, token := pfadTestServer(t, true)
	faelle := []struct {
		name, methode, pfad, token, body string
		kopf                             map[string]string
		want                             int
	}{
		{"Agent liest über /agent/", "GET", "/agent/boards/b1", token, "", nil, 200},
		{"Agent schreibt über /agent/", "POST", "/agent/boards/b1/ops", token,
			`{"ops":[{"type":"card.claim","payload":{"cardId":"k1","session":"s"}}]}`, nil, 200},
		// Identitäts-Header samt richtigem Secret zählen unter /agent/ nicht.
		{"/agent/ ohne Token, mit Menschen-Kopf", "GET", "/agent/boards/b1", "", "", menschKopf, 401},
		{"falsches Token", "GET", "/agent/boards/b1", "abt_erfunden", "", nil, 401},
		// Positivliste gilt wie am eigenen Listener.
		{"Admin über /agent/", "GET", "/agent/admin/agent-tokens", token, "", nil, 403},
		// Karte #699 (Kritiker KS-5): Namensliste der Board-Personen ist
		// Menschen vorbehalten — auch das Token des eigenen Boards kommt nicht hin.
		{"Mitglieder über /agent/", "GET", "/agent/boards/b1/mitglieder", token, "", nil, 403},
		{"fremdes Board über /agent/", "GET", "/agent/boards/b2", token, "", nil, 403},
		// `..` unter /agent/ fällt nicht in die Menschen-Kette durch.
		{"Punkt-Punkt unter /agent/", "GET", "/agent/boards/b1/../../../admin/agent-tokens", token, "", nil, 403},
		// Menschen-Pfade: Bearer weiter abgewiesen, Menschen unverändert.
		{"Bearer am Menschen-Pfad", "GET", "/boards/b1", token, "", nil, 401},
		{"Mensch am Menschen-Pfad", "GET", "/boards/b1", "", "", menschKopf, 200},
		{"Mensch ohne Secret", "GET", "/boards/b1", "", "", map[string]string{"Remote-User": "andi", "Remote-Email": "andi@example.org"}, 401},
	}
	for _, f := range faelle {
		if s := agentAnfrage(t, srv, f.methode, f.pfad, f.token, f.body, f.kopf); s != f.want {
			t.Errorf("%s: %d, erwartet %d", f.name, s, f.want)
		}
	}
	var state string
	if err := db.QueryRow(`SELECT state_json FROM boards WHERE id='b1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, `"by":"agent:projekt-x"`) {
		t.Fatalf("Schreiben über /agent/ nicht als Agent gestempelt: %s", state)
	}
}

// Ein kodiertes Präfix darf nicht die Menschen-Kette erreichen und nicht
// als Agent durchgehen: /%61gent/ ist nach dem Dekodieren /agent/, passt aber
// nicht zu RawPath — StripPrefix antwortet 404.
func TestAgentPfad_KodiertesPraefixFaelltZu(t *testing.T) {
	srv, _, token := pfadTestServer(t, true)
	req, _ := http.NewRequest("GET", srv.URL+"/%61gent/boards/b1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("kodiertes Präfix: 200")
	}
}

// roheAnfrage schickt die Request-Line wörtlich, so wie Caddy den rohen Pfad
// weiterreicht. Der Go-Client taugt dafür nicht: Einen Pfad mit `//` am Anfang
// schreibt er als absolute URL (`http://agent/…`), dann sähe der Server Host
// „agent“ und Pfad `/boards/…` — ein Messfehler des Tests, kein Serververhalten.
func roheAnfrage(t *testing.T, srv *httptest.Server, pfad string, mitToken bool, token string) int {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	kopf := "GET " + pfad + " HTTP/1.1\r\nHost: board.example.org\r\nConnection: close\r\n"
	if mitToken {
		kopf += "Authorization: Bearer " + token + "\r\n"
	}
	if _, err := conn.Write([]byte(kopf + "\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Abstimmung mit dem vorgeschalteten Proxy (2026-09-30): Der Proxy ordnet diese Pfade `handle /agent/*`
// zu (bereinigt, ohne Groß-/Kleinschreibung) und reicht sie ROH weiter. Hier
// dürfen sie weder in die Agenten- noch in die Menschen-Kette — sonst liefe
// eine Anfrage an der Anmeldung vorbei in eine Kette, die sie nicht erwartet.
// Offen war das vorher nicht (gemessen im Sprint-77-Abschluss und im Verify:
// 404 bzw. 307, auch im Legacy-Bypass), hing aber an der Routen-Tabelle.
// Erwartet: 400, bevor irgendeine Kette läuft — im strengen Modus und ohne
// Forward-Auth (Legacy-Bypass) gleichermaßen.
func TestAgentPfad_MehrdeutigerPfadAbgelehnt(t *testing.T) {
	pfade := []string{"/AGENT/boards/b1", "/Agent/boards/b1", "//agent/boards/b1", "/boards/../agent/boards/b1", "/%41GENT/boards/b1", "/agent"}
	for _, streng := range []bool{true, false} {
		for _, mitPfad := range []bool{true, false} {
			srv, _, token := pfadTestServerModus(t, mitPfad, streng)
			liste := pfade
			if !mitPfad {
				// Verify F-1273: bei ABOARD_AGENT_PFAD=aus gehört auch /agent/ selbst keiner Kette.
				liste = append(append([]string{}, pfade...), "/agent/boards/b1")
			}
			for _, p := range liste {
				for _, mitToken := range []bool{true, false} {
					if s := roheAnfrage(t, srv, p, mitToken, token); s != http.StatusBadRequest {
						t.Errorf("streng=%v pfad=%v %s (Token %v): %d, erwartet 400", streng, mitPfad, p, mitToken, s)
					}
				}
			}
		}
	}
	// Gegenstück: ein gewöhnlicher Menschen-Pfad bleibt unberührt (Legacy: 200).
	srv, _, _ := pfadTestServerModus(t, false, false)
	if s := roheAnfrage(t, srv, "/boards/b1", false, ""); s != http.StatusOK {
		t.Errorf("/boards/b1 im Legacy-Modus ohne Pfad-Eingang: %d, erwartet 200", s)
	}
	// Kein Treffer: /agentfoo ist kein Agenten-Pfad, auch nicht bei Caddy.
	if agentPfadMehrdeutig("/agentfoo/x") || agentPfadMehrdeutig("/boards/b1") {
		t.Error("Nicht-Agenten-Pfad als mehrdeutig erkannt")
	}
}

// SSE über den Pfad-Eingang: der Strom öffnet mit text/event-stream.
func TestAgentPfad_EventsUeberPfad(t *testing.T) {
	srv, _, token := pfadTestServer(t, true)
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/agent/boards/b1/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE über /agent/: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	_, _ = bufio.NewReader(resp.Body).Peek(1)
}

// Schalter aus: /agent/ gehört wieder der Menschen-Kette, und die weist das
// Token ab.
func TestAgentPfad_SchalterAus(t *testing.T) {
	srv, _, token := pfadTestServer(t, false)
	// Bis zum Sprint-77-Verify 401 (Menschen-Kette weist Bearer ab); seit F-1273
	// 400 mit „agent path is not available“ — abgelehnt bleibt es, jetzt mit Grund.
	if s := agentAnfrage(t, srv, "GET", "/agent/boards/b1", token, "", nil); s != http.StatusBadRequest {
		t.Fatalf("Schalter aus: %d, erwartet 400", s)
	}
}

func TestAgentPfadKonfig(t *testing.T) {
	for wert, want := range map[string]bool{"": true, "an": true, "aus": false} {
		t.Setenv(AgentPfadEnvVar, wert)
		got, err := agentPfadKonfig()
		if err != nil || got != want {
			t.Errorf("%q: %v %v, erwartet %v", wert, got, err, want)
		}
	}
	t.Setenv(AgentPfadEnvVar, "ja")
	if _, err := agentPfadKonfig(); err == nil {
		t.Fatal("ungültiger Wert ohne Fehler")
	}
}
