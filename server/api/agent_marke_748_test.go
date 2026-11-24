package api_test

// Karte #748: Agenten-Marke an Fristen (fristVonAgent) und Terminen
// (vonAgent) — über die echten Wege: Ops am Mensch- und Agenten-Eingang,
// Fold, Admin-API hinter derselben Kette wie am Server (OhneBearer,
// Anmeldung, ACL). Die Marke bleibt nach dem Rückbau der Außenstelle (#795)
// bestehen: Schatten (#778, von_agent) und #782 lesen sie.
//
// Bis #795 hiess die Datei kalender_aussen_748_test.go und prüfte zusätzlich
// das Abo „für außen“ und den Zugang kalender-ausgang; diese Teile sind mit
// der Außenstelle entfernt. Der Bestand-Fall steht in ical_bestand_795_test.go.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

type env748 struct {
	mensch, agent *httptest.Server
	db            *sql.DB
	boards        *storage.BoardRepo
	acl           *storage.AclRepo
	tokens        *storage.ICalTokenRepo
}

func newEnv748(t *testing.T) *env748 {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"Kunden","nextDisplayId":3,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[
	    {"id":"k1","displayId":1,"title":"Angebot Müller","columnId":"col-ready","dueDate":"2026-10-20","description":"Geheim: Preis 5000","comments":[]},
	    {"id":"k2","displayId":2,"title":"Ohne Frist","columnId":"col-ready","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Kunden", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de", 0}, {"u-admin", "admin@firma.de", 1}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	for email, rolle := range map[string]string{"anna@firma.de": "editor", "admin@firma.de": "owner"} {
		if err := acl.Set(ctx, "b1", email, rolle); err != nil {
			t.Fatal(err)
		}
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	tokens := storage.NewICalTokenRepo(db, box)
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })

	repo := storage.NewAgentTokenRepo(db)
	zugang := api.NewAgentZugang(repo)
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).WithFold(testMotor).Register(mux)
	adm := api.NewAdminAPI(cfg, users, "test", time.Now())
	adm.Register(mux)
	adm.RegisterAgentTokens(mux, repo, zugang)
	api.NewICalAPI(tokens, users, boards, acl).MitKonfig(cfg).Register(mux)

	// Wie menschenHandler (server/main.go) ohne CSRF-Schicht.
	var h http.Handler = api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)
	h = api.WithForwardAuthIfEnabled(h)
	h = api.OhneBearer(h)
	// Wie agentHandler (server/main_agent.go), ohne die Board-Paar-Schranke.
	ag := zugang.Middleware(mux)
	e := &env748{db: db, boards: boards, acl: acl, tokens: tokens}
	e.mensch = httptest.NewServer(h)
	t.Cleanup(e.mensch.Close)
	e.agent = httptest.NewServer(ag)
	t.Cleanup(e.agent.Close)
	return e
}

// anfrage: wer = E-Mail (Menschen-Eingang) oder Bearer-Token (beginnt mit
// abt_ — dann als Authorization, an beiden Eingängen).
func (e *env748) anfrage(t *testing.T, srv *httptest.Server, methode, pfad, wer, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(methode, srv.URL+pfad, rdr)
	if strings.HasPrefix(wer, "abt_") || strings.HasPrefix(wer, "abk_") { // abk_: Bestand-Test #795
		req.Header.Set("Authorization", "Bearer "+wer)
	} else if wer != "" {
		req.Header.Set("Remote-Email", wer)
		req.Header.Set("Remote-User", wer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env748) agentToken(t *testing.T) string {
	t.Helper()
	s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de",
		`{"name":"assistent","board":"b1","role":"editor","user":"anna@firma.de"}`)
	if s != http.StatusCreated {
		t.Fatalf("Agenten-Token: %d %s", s, b)
	}
	var out struct{ Token string }
	_ = json.Unmarshal([]byte(b), &out)
	return out.Token
}

func (e *env748) stand(t *testing.T) string {
	t.Helper()
	b, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	return string(b.StateJSON)
}

func (e *env748) karte(t *testing.T, id string) map[string]any {
	t.Helper()
	var s struct {
		Board struct {
			Sprints []struct{ Cards []map[string]any }
		}
	}
	if err := json.Unmarshal([]byte(e.stand(t)), &s); err != nil {
		t.Fatal(err)
	}
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			if c["id"] == id {
				return c
			}
		}
	}
	return nil
}

func (e *env748) ops(t *testing.T, srv *httptest.Server, wer, ops string) {
	t.Helper()
	if s, b := e.anfrage(t, srv, "POST", "/boards/b1/ops", wer, `{"ops":[`+ops+`]}`); s != http.StatusOK {
		t.Fatalf("ops %s: %d %s", ops, s, b)
	}
}

// markeAnKarte: Schlüssel, die (in irgendeiner Schreibweise) die Marke sind.
func markeAnKarte(c map[string]any) []string {
	var k []string
	for x := range c {
		if strings.EqualFold(x, "fristVonAgent") {
			k = append(k, fmt.Sprintf("%s=%v", x, c[x]))
		}
	}
	return k
}

// --- Agenten-Marke an Karten ----------------------------------------------

func TestKalender748_MarkeAnKarten(t *testing.T) {
	e := newEnv748(t)
	assistent := e.agentToken(t)
	pruefe := func(schritt string, soll bool) {
		t.Helper()
		got := markeAnKarte(e.karte(t, "k1"))
		if soll && (len(got) != 1 || got[0] != "fristVonAgent=true") || !soll && len(got) != 0 {
			t.Fatalf("%s: Marke %v, erwartet %v", schritt, got, soll)
		}
	}
	pruefe("Ausgangslage (Frist vom Menschen)", false)

	// Agent ändert etwas anderes als die Frist → keine Marke.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Angebot Müller GmbH"}}`)
	pruefe("Agent ändert Titel", false)
	// Agent verschiebt die Frist → Marke.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"dueDate","value":"2026-10-21"}}`)
	pruefe("Agent ändert dueDate", true)
	// Mensch ändert etwas anderes → Marke bleibt.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Angebot"}}`)
	pruefe("Mensch ändert Titel", true)
	// Mensch schickt die ganze Karte ohne Marke (Volltext-Weg des Browsers):
	// kein 422 wegen Entkernung, Marke bleibt, weil die Frist gleich ist.
	k := e.karte(t, "k1")
	delete(k, "fristVonAgent")
	k["description"] = "neu"
	roh, _ := json.Marshal(k)
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.upsert","payload":{"card":`+string(roh)+`}}`)
	pruefe("Mensch-Upsert ohne Marke, Frist gleich", true)
	// Der Agent kann die Marke nicht selbst wegnehmen — in keiner Schreibweise.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"fristVonAgent","value":false}}`)
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"FRISTVONAGENT","value":false}}`)
	pruefe("Agent setzt Marke selbst", true)
	// Mensch ändert die Uhrzeit → Marke weg.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"dueTime","value":"14:00"}}`)
	pruefe("Mensch ändert dueTime", false)
	// Ein Mensch kann sie nicht setzen (die Marke sagt etwas über Agenten).
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"fristVonAgent","value":true}}`)
	pruefe("Mensch setzt Marke", false)

	// Neue Karte vom Agenten mit Frist → Marke; ohne Frist → keine.
	e.ops(t, e.agent, assistent, `{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k3","title":"Rückruf","dueDate":"2026-10-22","fristVonAgent":false}}}`)
	if got := markeAnKarte(e.karte(t, "k3")); len(got) != 1 || got[0] != "fristVonAgent=true" {
		t.Fatalf("neue Agenten-Karte mit Frist: %v", got)
	}
	e.ops(t, e.agent, assistent, `{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k4","title":"Notiz"}}}`)
	if got := markeAnKarte(e.karte(t, "k4")); len(got) != 0 {
		t.Fatalf("neue Agenten-Karte ohne Frist: %v", got)
	}
	// Agent setzt eine Frist an einer Karte, die keine hatte.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k2","field":"startDate","value":"2026-10-23"}}`)
	if got := markeAnKarte(e.karte(t, "k2")); len(got) != 1 {
		t.Fatalf("Agent setzt startDate: %v", got)
	}
}

// --- Agenten-Marke an Terminen --------------------------------------------

func TestKalender748_MarkeAnTerminen(t *testing.T) {
	e := newEnv748(t)
	assistent := e.agentToken(t)
	marke := func(id string) any {
		t.Helper()
		tm := terminMitID743(t, e.stand(t), id)
		if tm == nil {
			t.Fatalf("Termin %s fehlt", id)
		}
		for k, v := range tm {
			if strings.EqualFold(k, "vonAgent") && k != "vonAgent" {
				t.Fatalf("Marke in fremder Schreibweise: %s", k)
			}
			_ = v
		}
		return tm["vonAgent"]
	}
	// Mensch legt an, auch mit gefälschter Marke → keine Marke.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"termin.upsert","payload":{"termin":{"id":"t1","titel":"Kunde Meier","datum":"2026-10-24","vonAgent":true}}}`)
	if m := marke("t1"); m != nil {
		t.Fatalf("Menschen-Termin: Marke %v", m)
	}
	// Agent ändert den Menschen-Termin → Marke (zuletzt geändert, nicht angelegt).
	e.ops(t, e.agent, assistent, `{"type":"termin.upsert","payload":{"termin":{"id":"t1","titel":"Kunde Meier verschoben","datum":"2026-10-25","VONAGENT":false}}}`)
	if m := marke("t1"); m != true {
		t.Fatalf("nach Agenten-Änderung: Marke %v", m)
	}
	if tm := terminMitID743(t, e.stand(t), "t1"); tm["erstelltVon"] != nil {
		t.Fatalf("Herkunft eines Menschen-Termins darf sich nicht ändern: %v", tm)
	}
	// Mensch ändert zurück → Marke weg.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"termin.upsert","payload":{"termin":{"id":"t1","titel":"Kunde Meier","datum":"2026-10-25","vonAgent":true}}}`)
	if m := marke("t1"); m != nil {
		t.Fatalf("nach Menschen-Änderung: Marke %v", m)
	}
}

// Kritiker #748, K-2: Eine Bestandsmarke in anderer Form (aus PUT, Import,
// Restore) darf Upserts nicht mit 422 „entkernung“ scheitern lassen — der
// nächste Fold normalisiert sie.
func TestKalender748_AbweichendeBestandsmarke(t *testing.T) {
	for _, bestand := range []string{`"fristVonAgent":false`, `"FristVonAgent":true`, `"fristVonAgent":"ja"`} {
		e := newEnv748(t)
		roh := strings.Replace(e.stand(t), `"title":"Ohne Frist",`, `"title":"Ohne Frist",`+bestand+`,`, 1)
		if roh == e.stand(t) {
			t.Fatal("Bestand nicht präpariert")
		}
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, roh); err != nil {
			t.Fatal(err)
		}
		k := e.karte(t, "k2")
		k["description"] = "geändert"
		ganz, _ := json.Marshal(k)
		e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.upsert","payload":{"card":`+string(ganz)+`}}`)
		// Marke gilt nur als true in genau dieser Schreibweise; alles andere
		// (auch FristVonAgent:true, nur per PUT eines Menschen erzeugbar)
		// normalisiert der Fold zu „keine Marke“.
		if got := markeAnKarte(e.karte(t, "k2")); len(got) != 0 {
			t.Fatalf("Bestand %s: Marke danach %v", bestand, got)
		}
	}
}
