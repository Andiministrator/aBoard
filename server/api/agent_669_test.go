package api_test

// Karte #669 (ADR-0133 §4): Open WebUI handelt mit einem delegierenden
// Dienst-Token im Namen der angemeldeten Person — über die echten Wege
// (Agenten-Zugang, Ops, Protokoll) und mit Gegenproben für jeden Abwehrfall.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

var geheimnis669 = []byte("owui-und-aboard-teilen-dieses-geheimnis-allein")

type env669 struct {
	agent, ohneDelegation *httptest.Server
	acl                   *storage.AclRepo
	boards                *storage.BoardRepo
	db                    *sql.DB
	repo                  *storage.AgentTokenRepo
	token                 string
	tokenID               string
}

func newEnv669(t *testing.T) *env669 {
	t.Helper()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	datei := filepath.Join(t.TempDir(), "owui-jwt-secret")
	if err := os.WriteFile(datei, append(append([]byte{}, geheimnis669...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	for _, id := range []string{"b1", "b2", "b3", storage.ServiceBoardID} {
		state := `{"version":"2","board":{"id":"` + id + `","title":"B","nextDisplayId":2,
		  "columnDefs":[{"id":"col-ready","title":"Ready"}],
		  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]}]}]}}`
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(state)); err != nil && id != storage.ServiceBoardID {
			t.Fatal(err)
		}
	}
	for _, e := range [][3]string{
		{"b1", "anna@firma.de", "editor"},
		{"b2", "anna@firma.de", "owner"},
		{"b1", "cora@extern.de", "viewer"},
		{"b1", "@firma.de", "editor"}, // Domain-Freigabe (ADR-0132)
		{storage.ServiceBoardID, "anna@firma.de", "owner"},
	} {
		if err := acl.Set(ctx, e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
	repo := storage.NewAgentTokenRepo(db)
	klar, tok, err := repo.Anlegen(ctx, storage.AgentTokenAnlage{Name: "open-webui", Role: storage.AgentRolleEditor, CreatedBy: "cli", Delegierend: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	broker := api.NewEventBroker()
	bapi := api.NewBoardsAPI(boards).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).WithFold(testMotor).WithBroker(broker)
	bapi.Register(mux)
	// Kritiker Sprint 82, K1-1: Ein delegierendes Token handelt nur über die
	// Werkzeuge — die Tests prüfen die Schutzaussagen deshalb dort.
	bapi.RegisterWerkzeuge(mux)
	api.NewEventsAPI(broker, boards).Register(mux)

	t.Setenv(api.OWUIJWTSecretFileEnv, datei)
	zugang := api.NewAgentZugang(repo)
	if err := zugang.MitOWUIDelegation(acl); err != nil {
		t.Fatal(err)
	}
	e := &env669{acl: acl, boards: boards, db: db, repo: repo, token: klar, tokenID: tok.ID}
	e.agent = httptest.NewServer(zugang.Middleware(mux))
	t.Cleanup(e.agent.Close)
	// Gegenstück: derselbe Server ohne konfigurierte Delegation.
	t.Setenv(api.OWUIJWTSecretFileEnv, "")
	ohne := api.NewAgentZugang(repo)
	if err := ohne.MitOWUIDelegation(acl); err != nil {
		t.Fatal(err)
	}
	e.ohneDelegation = httptest.NewServer(ohne.Middleware(mux))
	t.Cleanup(e.ohneDelegation.Close)
	return e
}

func jwt669(t *testing.T, g []byte, kopf map[string]any, email string, iat, exp time.Time) string {
	t.Helper()
	k, _ := json.Marshal(kopf)
	i, _ := json.Marshal(map[string]any{"sub": "owui-id", "email": email, "name": "N", "role": "user",
		"iss": "open-webui", "iat": iat.Unix(), "exp": exp.Unix()})
	basis := base64.RawURLEncoding.EncodeToString(k) + "." + base64.RawURLEncoding.EncodeToString(i)
	if g == nil {
		return basis + "."
	}
	mac := hmac.New(sha256.New, g)
	mac.Write([]byte(basis))
	return basis + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func frisch669(t *testing.T, email string) string {
	jetzt := time.Now()
	return jwt669(t, geheimnis669, map[string]any{"alg": "HS256", "typ": "JWT"}, email, jetzt, jetzt.Add(300*time.Second))
}

func (e *env669) anfrage(t *testing.T, srv *httptest.Server, methode, pfad, body string, kopf ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(methode, srv.URL+pfad, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(kopf); i += 2 {
		req.Header.Add(kopf[i], kopf[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func ich669(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("ich: %s", b)
	}
	return m
}

// rolle669: die wirksame Rolle aus der Antwort von board_lesen.
func rolle669(t *testing.T, b []byte) any {
	t.Helper()
	board, _ := ich669(t, b)["board"].(map[string]any)
	return board["rolle"]
}

// Kritiker Sprint 82, K1-1: Die Schutzaussagen von #669 gelten über die
// Werkzeuge — die rohe Agenten-API erreicht ein delegierendes Token nicht mehr.
func TestAgent669_PersonAusJWTRolleAusACL(t *testing.T) {
	e := newEnv669(t)
	jwtAnna := frisch669(t, "Anna@Firma.de")

	// Gültig: Person aus dem JWT; die Klartext-Kopfzeile behauptet jemand
	// anderen und wird nicht ausgewertet.
	s, b := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards", "", "X-OpenWebUI-User-Jwt", jwtAnna,
		"X-OpenWebUI-User-Email", "chef@firma.de")
	if s != http.StatusOK || ich669(t, b)["person"] != "anna@firma.de" {
		t.Fatalf("anna boards: %d %s", s, b)
	}
	if s, b = e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", jwtAnna,
		"X-OpenWebUI-User-Email", "chef@firma.de"); s != http.StatusOK || rolle669(t, b) != "editor" {
		t.Fatalf("anna b1: %d %s", s, b)
	}
	// Board aus dem Pfad; owner wird auf editor gekappt.
	if s, b = e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b2", "", "X-OpenWebUI-User-Jwt", jwtAnna); s != http.StatusOK || rolle669(t, b) != "editor" {
		t.Fatalf("anna b2 (owner → editor): %d %s", s, b)
	}
	// Anderes Board im Pfad, auf dem die Person nichts hat → 403; ebenso ein
	// Board, das es nicht gibt (gleiche Antwort, kein Orakel).
	for _, pfad := range []string{"/werkzeuge/boards/b3", "/werkzeuge/boards/gibt-es-nicht"} {
		if s, _ := e.anfrage(t, e.agent, "GET", pfad, "", "X-OpenWebUI-User-Jwt", jwtAnna); s != http.StatusForbidden {
			t.Fatalf("anna %s: %d", pfad, s)
		}
	}
	// Service-Board nie über OWUI, auch nicht für dessen Owner.
	if s, _ := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/"+storage.ServiceBoardID, "", "X-OpenWebUI-User-Jwt", jwtAnna); s != http.StatusForbidden {
		t.Fatalf("service-board: %d", s)
	}
	// Domain-Freigabe zählt: bert hat keinen eigenen Eintrag.
	jwtBert := frisch669(t, "bert@firma.de")
	if s, b = e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", jwtBert); s != http.StatusOK || rolle669(t, b) != "editor" {
		t.Fatalf("bert per Domain: %d %s", s, b)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b2", "", "X-OpenWebUI-User-Jwt", jwtBert); s != http.StatusForbidden {
		t.Fatalf("bert b2: %d", s)
	}
	// Person ganz ohne ACL-Rolle (fremde Domain) → 403.
	if s, _ := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", frisch669(t, "dora@anders.de")); s != http.StatusForbidden {
		t.Fatalf("dora: %d", s)
	}
	// viewer liest, schreibt nicht.
	jwtCora := frisch669(t, "cora@extern.de")
	if s, b := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", jwtCora); s != http.StatusOK || rolle669(t, b) != "viewer" {
		t.Fatalf("cora liest: %d %s", s, b)
	}
	if s, _ := e.anfrage(t, e.agent, "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"hallo"}`, "X-OpenWebUI-User-Jwt", jwtCora); s != http.StatusForbidden {
		t.Fatalf("cora schreibt: %d", s)
	}
	// Entzieht der Owner die Domain-Freigabe, ist bert sofort draussen.
	if err := e.acl.Delete(context.Background(), "b1", "@firma.de"); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", jwtBert); s != http.StatusForbidden {
		t.Fatalf("bert nach Entzug: %d", s)
	}
}

func TestAgent669_StempelWiePersonenToken(t *testing.T) {
	e := newEnv669(t)
	jwtAnna := frisch669(t, "anna@firma.de")
	if s, b := e.anfrage(t, e.agent, "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"hallo"}`,
		"X-OpenWebUI-User-Jwt", jwtAnna, "X-OpenWebUI-User-Email", "chef@firma.de"); s != http.StatusOK {
		t.Fatalf("kommentieren: %d %s", s, b)
	}
	b1, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if st := string(b1.StateJSON); !strings.Contains(st, `"author":"agent:open-webui"`) || !strings.Contains(st, `"onBehalfOf":"anna@firma.de"`) || strings.Contains(st, "chef@firma.de") {
		t.Fatalf("Kommentar-Stempel: %s", st)
	}
	var fuer, akteur string
	if err := e.db.QueryRow(`SELECT on_behalf_of, actor FROM activities WHERE board_id = 'b1' AND op_type = 'comment.add'`).Scan(&fuer, &akteur); err != nil {
		t.Fatal(err)
	}
	if fuer != "anna@firma.de" || akteur != "agent:open-webui" {
		t.Fatalf("Protokoll: on_behalf_of=%q actor=%q", fuer, akteur)
	}
	// Ops, deren Erlaubnis am Akteur hängt (comment.edit, card.claim,
	// card.release), gibt es für ein delegierendes Token zweifach nicht: Kein
	// Werkzeug erzeugt sie, und der rohe Ops-Weg ist gesperrt (K1-1, siehe
	// TestAgent669_NurWerkzeuge). Die Sperre in agentPayload selbst prüft
	// TestAgentK1_AkteurOpsGesperrt (agent_k1_internal_test.go).
	for _, op := range []string{
		`{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"umgeschrieben"}}`,
		`{"type":"card.claim","payload":{"cardId":"k1","session":"s1"}}`,
		`{"type":"card.release","payload":{"cardId":"k1"}}`,
	} {
		if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", `{"ops":[`+op+`]}`, "X-OpenWebUI-User-Jwt", frisch669(t, "bert@firma.de")); s != http.StatusForbidden {
			t.Fatalf("%s: %d %s", op, s, b)
		}
	}
	b1, _ = e.boards.Get(context.Background(), "b1")
	if strings.Contains(string(b1.StateJSON), "umgeschrieben") || strings.Contains(string(b1.StateJSON), `"claim"`) {
		t.Fatalf("abgelehnte Op hat gewirkt: %s", b1.StateJSON)
	}
}

func TestAgent669_Abwehr(t *testing.T) {
	e := newEnv669(t)
	jetzt := time.Now()
	hs := map[string]any{"alg": "HS256", "typ": "JWT"}
	faelle := map[string][]string{
		"ohne-jwt":          nil,
		"nur-klartext":      {"X-OpenWebUI-User-Email", "anna@firma.de", "X-OpenWebUI-User-Name", "Anna", "X-OpenWebUI-User-Id", "1", "X-OpenWebUI-User-Role", "admin"},
		"alg-none":          {"X-OpenWebUI-User-Jwt", jwt669(t, nil, map[string]any{"alg": "none", "typ": "JWT"}, "anna@firma.de", jetzt, jetzt.Add(300*time.Second))},
		"falsche-signatur":  {"X-OpenWebUI-User-Jwt", jwt669(t, []byte("ein-ganz-anderes-geheimnis-mit-genug-zeichen"), hs, "anna@firma.de", jetzt, jetzt.Add(300*time.Second))},
		"abgelaufen":        {"X-OpenWebUI-User-Jwt", jwt669(t, geheimnis669, hs, "anna@firma.de", jetzt.Add(-100*time.Second), jetzt.Add(-61*time.Second))},
		"zu-alt":            {"X-OpenWebUI-User-Jwt", jwt669(t, geheimnis669, hs, "anna@firma.de", jetzt.Add(-400*time.Second), jetzt.Add(time.Hour))},
		"domain-als-person": {"X-OpenWebUI-User-Jwt", jwt669(t, geheimnis669, hs, "@firma.de", jetzt, jetzt.Add(300*time.Second))},
		"zwei-jwt":          {"X-OpenWebUI-User-Jwt", frisch669(t, "cora@extern.de"), "X-OpenWebUI-User-Jwt", frisch669(t, "anna@firma.de")},
	}
	for name, kopf := range faelle {
		if s, b := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", kopf...); s != http.StatusUnauthorized {
			t.Errorf("%s: %d %s (erwartet 401)", name, s, b)
		}
	}
	// Gültiges JWT, aber der Server hat kein Geheimnis → 401 (Ausfall zu).
	if s, _ := e.anfrage(t, e.ohneDelegation, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", frisch669(t, "anna@firma.de")); s != http.StatusUnauthorized {
		t.Fatalf("ohne Delegation: %d", s)
	}
	// Widerrufen → 401 trotz gültigem JWT.
	if _, err := e.repo.Revoke(context.Background(), "open-webui", "cli"); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/werkzeuge/boards/b1", "", "X-OpenWebUI-User-Jwt", frisch669(t, "anna@firma.de")); s != http.StatusUnauthorized {
		t.Fatalf("widerrufen: %d", s)
	}
}

// Kein Handler hinter dem Agenten-Zugang sieht eine OWUI-Nutzer-Kopfzeile —
// weder das JWT noch die Klartext-Rückfälle, auch nicht bei Projekt-Tokens.
func TestAgent669_HandlerSiehtKeineOWUIKopfzeilen(t *testing.T) {
	e := newEnv669(t)
	var gesehen []string
	zugang := api.NewAgentZugang(e.repo)
	t.Setenv(api.OWUIJWTSecretFileEnv, "")
	if err := zugang.MitOWUIDelegation(e.acl); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	klar, _, err := e.repo.Anlegen(ctx, storage.AgentTokenAnlage{Name: "projekt", BoardID: "b1", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(zugang.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-openwebui-user-") {
				gesehen = append(gesehen, k)
			}
		}
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/boards/b1", nil)
	req.Header.Set("Authorization", "Bearer "+klar)
	req.Header.Set("X-OpenWebUI-User-Email", "chef@firma.de")
	req.Header.Set("X-OpenWebUI-User-Jwt", frisch669(t, "chef@firma.de"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(gesehen) != 0 {
		t.Fatalf("Status %d, gesehene Kopfzeilen %v", resp.StatusCode, gesehen)
	}
}

// Kritiker Sprint 82, K1-1: Ein delegierendes Token erreicht NUR die Werkzeuge.
// Belegt waren mit Dienst-Token und gültigem JWT `card.delete`,
// `swimlane.delete` und `board.field title=…` über POST /boards/{id}/ops —
// jetzt 403, das Board bleibt unverändert. Ebenso der Ereignisstrom, das
// Board-Blob, /ich und die Anhänge (Open WebUI braucht keinen davon).
func TestAgent669_NurWerkzeuge(t *testing.T) {
	e := newEnv669(t)
	jwtAnna := frisch669(t, "anna@firma.de") // editor auf b1
	vorher, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]string{
		"card.delete":     `{"type":"card.delete","payload":{"cardId":"k1"}}`,
		"swimlane.delete": `{"type":"swimlane.delete","payload":{"sprintId":"s1"}}`,
		"board.field":     `{"type":"board.field","payload":{"field":"title","value":"Übernommen"}}`,
	} {
		s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", `{"ops":[`+op+`]}`, "X-OpenWebUI-User-Jwt", jwtAnna)
		if s != http.StatusForbidden || !strings.Contains(string(b), "tool endpoints") {
			t.Errorf("%s: %d %s (erwartet 403)", name, s, b)
		}
	}
	nachher, _ := e.boards.Get(context.Background(), "b1")
	if string(nachher.StateJSON) != string(vorher.StateJSON) {
		t.Fatalf("abgelehnte Op hat gewirkt: %s", nachher.StateJSON)
	}
	for _, f := range [][2]string{
		{"GET", "/boards/b1"}, {"HEAD", "/boards/b1"}, {"GET", "/boards/b1/events"}, {"GET", "/boards/b1/ich"},
		{"GET", "/boards/b1/attachments/k1/a1"}, {"POST", "/boards/b1/attachments/k1"},
		{"DELETE", "/boards/b1/attachments/k1/a1"}, {"GET", "/boards/b1/board-attachments/a1"},
		{"POST", "/boards/b1/board-attachments"}, {"DELETE", "/boards/b1/board-attachments/a1"},
		{"GET", "/boards"}, {"GET", "/me"},
	} {
		if s, _ := e.anfrage(t, e.agent, f[0], f[1], "", "X-OpenWebUI-User-Jwt", jwtAnna); s != http.StatusForbidden {
			t.Errorf("%s %s: %d (erwartet 403)", f[0], f[1], s)
		}
	}
	// Auch ohne JWT nur 403 — der Weg existiert für dieses Token nicht, die
	// Antwort hängt nicht an der Person.
	if s, _ := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", `{"ops":[]}`); s != http.StatusForbidden {
		t.Fatalf("ohne JWT: %d", s)
	}
	// Gegenstück: dieselbe Person über das Werkzeug — erlaubt.
	if s, b := e.anfrage(t, e.agent, "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"über das Werkzeug"}`, "X-OpenWebUI-User-Jwt", jwtAnna); s != http.StatusOK {
		t.Fatalf("Werkzeug: %d %s", s, b)
	}
}
