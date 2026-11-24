package main

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

// agentTestServer baut den gemeinsamen Mux wie run() (Boards mit ACL,
// Activities und Fold, Events) und davor GENAU die Kette des Agenten-
// Listeners (agentHandler). Der ACLGuard steckt bewusst im Mux-Stapel des
// Menschen-Listeners, nicht hier — geprüft wird, was der Agent tatsächlich
// durchläuft.
func agentTestServer(t *testing.T) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"Agentenboard","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Agentenboard", "2", []byte(state)); err != nil {
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
	srv := httptest.NewServer(agentHandler(mux, api.NewAgentZugang(tokens)))
	t.Cleanup(srv.Close)
	return srv, db, klartext
}

func agentAnfrage(t *testing.T, srv *httptest.Server, methode, pfad, token, body string, kopf map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(methode, srv.URL+pfad, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func zaehle(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAgentKette_K1: Ein Agent ist kein Nutzer (ADR-0129 §3, K-1). Seine
// Anfragen auf einem sonst leeren Server legen keinen users-Eintrag, kein
// Board, keinen ACL-Eintrag und keinen Admin an — auch wenn er Identitäts-
// Header mitschickt und Trust an ist. Ops stempelt der Server als
// `agent:<name>`.
func TestAgentKette_K1(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	srv, db, token := agentTestServer(t)
	fremd := map[string]string{"Remote-User": "chef", "Remote-Email": "chef@firma.de"}

	if s := agentAnfrage(t, srv, "GET", "/boards/b1", token, "", fremd); s != 200 {
		t.Fatalf("GET eigenes Board: %d", s)
	}
	ops := `{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, ops, fremd); s != 200 {
		t.Fatalf("POST ops: %d", s)
	}
	if s := agentAnfrage(t, srv, "GET", "/boards", token, "", fremd); s != 403 {
		t.Fatalf("Board-Liste: %d, erwartet 403", s)
	}
	for q, want := range map[string]int{
		"SELECT COUNT(*) FROM users":                  0,
		"SELECT COUNT(*) FROM boards":                 1,
		"SELECT COUNT(*) FROM acl":                    0,
		"SELECT COUNT(*) FROM users WHERE is_admin=1": 0,
	} {
		if got := zaehle(t, db, q); got != want {
			t.Errorf("%s = %d, erwartet %d", q, got, want)
		}
	}
	var akteur, art, karte string
	if err := db.QueryRow(`SELECT actor, actor_kind, card_id FROM activities WHERE board_id='b1' AND action='op'`).Scan(&akteur, &art, &karte); err != nil {
		t.Fatal(err)
	}
	if akteur != "agent:projekt-x" || art != "agent" || karte != "k1" {
		t.Fatalf("Protokoll: actor=%q kind=%q card=%q, erwartet agent:projekt-x/agent/k1 (Identitäts-Header dürfen nicht wirken)", akteur, art, karte)
	}
}

// TestAgentKette_UrheberschaftImPayload (Kritiker Sprint 71, K-A2): Ein
// Agent kann keinen Kommentar unter fremdem Namen ablegen und weder
// comments noch activity einer Karte setzen oder leeren.
func TestAgentKette_UrheberschaftImPayload(t *testing.T) {
	srv, db, token := agentTestServer(t)
	kommentar := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","author":"chef@firma.de","text":"x","created":"2026-09-28"}}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, kommentar, nil); s != 200 {
		t.Fatalf("comment.add: %d", s)
	}
	var state string
	if err := db.QueryRow(`SELECT state_json FROM boards WHERE id='b1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(state, "chef@firma.de") || !strings.Contains(state, `"author":"agent:projekt-x"`) {
		t.Fatalf("Kommentar-Autor nicht überschrieben: %s", state)
	}
	for name, ops := range map[string]string{
		"card.field comments": `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":[]}}]`,
		"card.field activity": `[{"type":"card.field","payload":{"cardId":"k1","field":"activity","value":[]}}]`,
		// ADR-0130 §2 (Karte #604): card.fieldIf unterliegt denselben Regeln.
		"card.fieldIf comments": `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"comments","value":[],"expect":[]}}]`,
		"card.upsert comments":  `[{"type":"card.upsert","payload":{"sprintId":"s1","card":{"id":"k9","title":"n","comments":[]}}}]`,
		// Kritiker Sprint 71:
		"comment.delete":          `[{"type":"comment.delete","payload":{"cardId":"k1","commentId":"c1"}}]`,
		"comment.add ohne Objekt": `[{"type":"comment.add","payload":{"cardId":"k1","comment":["x"]}}]`,
		"board.field identities":  `[{"type":"board.field","payload":{"field":"identities","value":{}}}]`,
	} {
		if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, `{"ops":`+ops+`}`, nil); s != 400 {
			t.Errorf("%s: %d, erwartet 400", name, s)
		}
	}
}

// TestAgentKette_KommentarBearbeiten (ADR-0130 §4, Karte #601): Ein Agent
// darf seine eigenen Kommentare per comment.edit ändern, fremde nicht (422
// fremder-kommentar, geprüft gegen den Stand im Board), fehlende auch nicht
// (422 ziel-fehlt). Autor und die übrigen Kommentare bleiben unberührt.
func TestAgentKette_KommentarBearbeiten(t *testing.T) {
	srv, db, token := agentTestServer(t)
	eigen := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","text":"eigen","created":"2026-09-30"}}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, eigen, nil); s != 200 {
		t.Fatalf("comment.add: %d", s)
	}
	// Ein Mensch hat inzwischen einen Kommentar geschrieben (direkt im Stand,
	// der Menschen-Listener ist hier nicht aufgebaut).
	if _, err := db.Exec(`UPDATE boards SET state_json = replace(state_json, '"text":"eigen"}]', '"text":"eigen"},{"id":"c2","author":"chef@firma.de","text":"vom Chef","created":"2026-09-30"}]') WHERE id='b1'`); err != nil {
		t.Fatal(err)
	}
	stand := func() string {
		var st string
		if err := db.QueryRow(`SELECT state_json FROM boards WHERE id='b1'`).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if !strings.Contains(stand(), `"vom Chef"`) {
		t.Fatalf("Vorbedingung: fremder Kommentar fehlt: %s", stand())
	}

	for name, fall := range map[string]struct {
		ops  string
		want int
	}{
		"fremder Kommentar":       {`[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c2","text":"gekapert"}}]`, 422},
		"fehlender Kommentar":     {`[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c9","text":"x"}}]`, 422},
		"text keine Zeichenkette": {`[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":42}}]`, 400},
		"author im Payload":       {`[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"x","author":"chef@firma.de"}}]`, 400},
	} {
		if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, `{"ops":`+fall.ops+`}`, nil); s != fall.want {
			t.Errorf("%s: %d, erwartet %d", name, s, fall.want)
		}
	}
	if strings.Contains(stand(), "gekapert") {
		t.Fatalf("fremder Kommentar wurde geändert: %s", stand())
	}

	ok := `{"ops":[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"eigen, korrigiert"}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, ok, nil); s != 200 {
		t.Fatalf("comment.edit eigener Kommentar: %d", s)
	}
	st := stand()
	if !strings.Contains(st, `"text":"eigen, korrigiert"`) || !strings.Contains(st, `"author":"agent:projekt-x"`) || !strings.Contains(st, `"vom Chef"`) {
		t.Fatalf("Stand nach comment.edit: %s", st)
	}
}

// TestAgentKette_RateLimit: Agenten haben ihren eigenen Eimer mit 60 Batches
// pro Minute; die höhere Grenze der Menschen (Kritiker Sprint 71) gilt für
// sie nicht.
func TestAgentKette_RateLimit(t *testing.T) {
	srv, _, token := agentTestServer(t)
	for i := 0; i < 60; i++ {
		if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, `{"ops":[]}`, nil); s != 400 {
			t.Fatalf("Aufruf %d: %d", i, s)
		}
	}
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, `{"ops":[]}`, nil); s != 429 {
		t.Fatalf("61. Aufruf: %d, erwartet 429", s)
	}
}

// TestAgentKette_KeinLegacyBypass: Mit Trust AUS gibt es auf dem Menschen-
// Listener den auth-losen Einzelbetrieb. Auf dem Agenten-Listener nicht:
// ohne gültiges Token 401, auch mit Identitäts-Headern.
func TestAgentKette_KeinLegacyBypass(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	srv, _, _ := agentTestServer(t)
	for _, token := range []string{"", "abt_erfunden"} {
		if s := agentAnfrage(t, srv, "GET", "/boards/b1", token, "", map[string]string{"Remote-Email": "a@b.de"}); s != 401 {
			t.Errorf("Token %q: %d, erwartet 401", token, s)
		}
	}
}

func TestAgentListenerKonfig(t *testing.T) {
	faelle := []struct {
		tcp, sock, menschAddr, menschSock string
		netz                              string
		fehler                            bool
	}{
		{"", "", ":8080", "", "", false},
		{"127.0.0.1:8081", "", ":8080", "", "tcp", false},
		{"", "/run/a.sock", ":8080", "/run/m.sock", "unix", false},
		{"127.0.0.1:8081", "/run/a.sock", ":8080", "", "", true},
		{":8080", "", ":8080", "", "", true},
		{"", "/run/m.sock", ":8080", "/run/m.sock", "", true},
	}
	for _, f := range faelle {
		t.Setenv("ABOARD_AGENT_LISTEN", f.tcp)
		t.Setenv("ABOARD_AGENT_UNIX_SOCKET", f.sock)
		netz, _, err := agentListenerKonfig(f.menschAddr, f.menschSock)
		if (err != nil) != f.fehler || netz != f.netz {
			t.Errorf("%+v: netz=%q err=%v", f, netz, err)
		}
	}
}

// TestAgentListenerKonfig_WarntBeiOffenemBind (Kritiker Sprint 71, K-A8).
func TestAgentListenerKonfig_WarntBeiOffenemBind(t *testing.T) {
	var warnungen []string
	alt := agentListenerWarnung
	agentListenerWarnung = func(f string, a ...any) { warnungen = append(warnungen, f) }
	t.Cleanup(func() { agentListenerWarnung = alt })
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", "")
	for addr, warnt := range map[string]bool{"127.0.0.1:8081": false, "[::1]:8081": false, "0.0.0.0:8081": true, ":8081": true} {
		warnungen = nil
		t.Setenv("ABOARD_AGENT_LISTEN", addr)
		if _, _, err := agentListenerKonfig(":8080", ""); err != nil {
			t.Fatal(err)
		}
		if (len(warnungen) > 0) != warnt {
			t.Errorf("%s: Warnungen %v, erwartet Warnung=%v", addr, warnungen, warnt)
		}
	}
}

// TestStarteAgentListener_UnixSocket: Der echte Listener lauscht, setzt die
// Agenten-Kette davor und räumt den Socket beim Stoppen ab.
func TestStarteAgentListener_UnixSocket(t *testing.T) {
	// Unix-Socket-Pfade sind unter Linux auf 108 Byte begrenzt. t.TempDir()
	// kann darüber liegen, wenn TMPDIR tief verschachtelt ist (Verify
	// Sprint 71) — deshalb ein kurzes eigenes Verzeichnis.
	// Nicht nach /tmp (Projektregel, Kritiker Sprint 71): ins tmp/ der
	// Repo-Wurzel, das gitignoriert ist.
	basis, err := filepath.Abs(filepath.Join("..", "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(basis, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(basis, "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	if len(sock) >= 104 {
		t.Fatalf("Socket-Pfad %q ist %d Byte lang, zu lang für AF_UNIX — TMPDIR kürzen", sock, len(sock))
	}
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", sock)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boards/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	zugang := api.NewAgentZugang(storage.NewAgentTokenRepo(nil))
	fehler := make(chan error, 1)
	stop, err := starteAgentListener(mux, zugang, ":8080", "", socketModus{modus: socketModusVoreinstellung}, fehler)
	if err != nil || stop == nil {
		t.Fatalf("Start: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, Timeout: 2 * time.Second}
	resp, err := client.Get("http://agent/boards/b1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("ohne Token: %d, erwartet 401 (Agenten-Kette fehlt?)", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop(ctx)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("Socket nach Stop noch da: %v", err)
	}
}

// TestAgentZugang_IstVerdrahtet: main.go hängt OhneBearer vor den
// Menschen-Listener, startet den Agenten-Listener und registriert die
// Admin-Routen. Quelltext, Begründung wie in main_test.go.
//
// GRENZE: prüft nur, dass die Aufrufe im Code stehen (ohne Kommentare) —
// nicht ihre Reihenfolge in der Kette.
func TestAgentZugang_IstVerdrahtet(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	ohneKommentare := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(quelle), "")
	for _, muss := range []string{"api.OhneBearer(handler)", "starteAgentListener(", "RegisterAgentTokens(", `case "agent-token":`} {
		if !strings.Contains(ohneKommentare, muss) {
			t.Errorf("main.go enthält %q nicht", muss)
		}
	}
}

// TestEntferneAltenSocket_NurSockets (Kritiker Sprint 71): Zeigt der
// Socket-Pfad auf eine gewöhnliche Datei, bricht der Start ab, und die Datei
// bleibt stehen. Ein fehlender Pfad ist kein Fehler.
func TestEntferneAltenSocket_NurSockets(t *testing.T) {
	dir := t.TempDir()
	datei := filepath.Join(dir, "aboard.db")
	if err := os.WriteFile(datei, []byte("daten"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", datei)
	if stop, err := starteAgentListener(http.NewServeMux(), api.NewAgentZugang(storage.NewAgentTokenRepo(nil)), ":8080", "", socketModus{modus: socketModusVoreinstellung}, make(chan error, 1)); err == nil {
		if stop != nil {
			stop(context.Background())
		}
		t.Fatal("Start mit einer Nicht-Socket-Datei am Socket-Pfad gelang")
	}
	if _, err := os.Stat(datei); err != nil {
		t.Fatalf("Datei gelöscht: %v", err)
	}
	if err := entferneAltenSocket(filepath.Join(dir, "fehlt.sock")); err != nil {
		t.Fatalf("fehlender Pfad: %v", err)
	}
}

// TestAgentKette_Uebernahme (ADR-0130 §1, Karte #606): Ein Agent übernimmt
// eine Karte; der Halter ist sein Server-Stempel, nicht ein Payload-Wert. Eine
// Übernahme ERZWINGEN darf er nicht (400) — das bleibt Menschen vorbehalten.
// Die eigene gibt er frei.
func TestAgentKette_Uebernahme(t *testing.T) {
	srv, db, token := agentTestServer(t)
	claim := `{"ops":[{"type":"card.claim","payload":{"cardId":"k1","session":"s1"}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, claim, nil); s != 200 {
		t.Fatalf("card.claim: %d", s)
	}
	stand := func() string {
		var st string
		if err := db.QueryRow(`SELECT state_json FROM boards WHERE id='b1'`).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if !strings.Contains(stand(), `"by":"agent:projekt-x"`) {
		t.Fatalf("Halter nicht gestempelt: %s", stand())
	}
	for name, ops := range map[string]string{
		"erzwingen":            `[{"type":"card.release","payload":{"cardId":"k1","erzwingen":true}}]`,
		"erzwingen als Zahl 1": `[{"type":"card.release","payload":{"cardId":"k1","erzwingen":1}}]`,
		// Sitzungs-QA K-1: Go las ohne Beachtung der Schreibweise den LETZTEN
		// Treffer (false), der Fold exakt `erzwingen` (true).
		"erzwingen in zwei Schreibweisen": `[{"type":"card.release","payload":{"cardId":"k1","erzwingen":true,"ERZWINGEN":false}}]`,
		"field in zwei Schreibweisen":     `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","FIELD":"title","value":[]}}]`,
		"card-Schlüssel mit langem s":     `[{"type":"card.upsert","payload":{"sprintId":"s1","card":{"id":"k9","title":"n","comments":[],"commentſ":1}}}]`,
		// Sitzungs-QA K-3: ohne Sitzung wären zwei Sitzungen desselben Tokens derselbe Halter.
		"card.claim ohne session": `[{"type":"card.claim","payload":{"cardId":"k1"}}]`,
	} {
		if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, `{"ops":`+ops+`}`, nil); s != 400 {
			t.Errorf("%s: %d, erwartet 400", name, s)
		}
	}
	frei := `{"ops":[{"type":"card.release","payload":{"cardId":"k1","session":"s1","erzwingen":false}}]}`
	if s := agentAnfrage(t, srv, "POST", "/boards/b1/ops", token, frei, nil); s != 200 {
		t.Fatalf("eigene Freigabe: %d", s)
	}
	if strings.Contains(stand(), `"claim"`) {
		t.Fatalf("claim nach Freigabe: %s", stand())
	}
}
