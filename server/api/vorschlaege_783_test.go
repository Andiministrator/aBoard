package api_test

// Karte #783 (ADR-0140 §8, Vertrag Push-Skript 3a/9.6, Tests T4/T13/T14):
// Rolle „interner Agent“ auf dem externen Server.
//
//   - Positivliste: Ein intern-Token erreicht genau die fünf /intern/…-Wege
//     seines Boards, sonst nichts — keine rohen Wege, keine Werkzeuge, kein
//     MCP, kein fremdes Board, keine Vorschlags-Liste (Stellen 1, 2, 4).
//   - Sofort wirksam sind nur Spalte und Priorität einer bestehenden
//     externen Karte; die 31. Änderung in der Stunde wird gebremst (T14).
//   - Alles andere wird ein Vorschlag: unsichtbar für Agenten, sichtbar und
//     bestätigbar nur für Editoren in einer Menschen-Sitzung (T13), höchstens
//     10 offene je Token. Bestätigen führt unter dem Agenten aus (Autor
//     agent:<name>, keine authorEmail, actor_kind agent) und nennt den
//     Bestätiger im Protokoll; Ablehnen löscht hart (T4, T13).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const schatten783 = "SCHATTEN-783"

type env783 struct {
	db                     *sql.DB
	boards                 *storage.BoardRepo
	cfg                    *storage.ConfigRepo
	tokens                 *storage.AgentTokenRepo
	agent                  *httptest.Server
	mensch                 *httptest.Server
	assistent, assistentB2 string // intern-Tokens für b1 (gekoppelt) und b2 (nicht gekoppelt)
	projekt                string // editor-Token für b1
	frisch                 string // weiteres intern-Token für b1 (alsAgentNeu)
	bapi                   *api.BoardsAPI
}

func newEnv783(t *testing.T) *env783 { t.Helper(); return newEnv783Mit(t, true) }

// newEnv783Mit: trust = false baut die Menschen-Kette ohne Forward-Auth
// (Legacy-Bypass) — der Schalter wird beim Verdrahten gelesen, nicht je Anfrage.
func newEnv783Mit(t *testing.T, trust bool) *env783 {
	t.Helper()
	if trust {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	} else {
		t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	for id, titel := range map[string]string{"b1": "Extern", "b2": "Anderes"} {
		if _, err := boards.Insert(ctx, id, titel, "2", []byte(board671(id, titel))); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range [][2]string{{"u-anna", "anna@firma.de"}, {"u-vera", "vera@firma.de"}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u[0], u[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, z := range [][3]string{{"b1", "anna@firma.de", "editor"}, {"b1", "vera@firma.de", "viewer"}, {"b2", "anna@firma.de", "editor"}} {
		if err := acl.Set(ctx, z[0], z[1], z[2]); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, api.BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	bp := storage.NewBoardpaarRepo(db)
	sr := storage.NewSchattenRepo(db)
	if _, err := bp.AussenKoppeln(ctx, "b1", "https://innen.example/#board=3f2a9c1e-7b4d-4e8a-9c0f-1a2b3c4d5e6f", "admin"); err != nil {
		t.Fatal(err)
	}
	nr, titel := 12, schatten783
	if _, err := sr.Ersetze(ctx, "b1", []schatten.Eingang{{SchattenID: sid782("A", 1), Art: schatten.ArtKarte, Nr: &nr, Titel: &titel}}); err != nil {
		t.Fatal(err)
	}
	repo := storage.NewAgentTokenRepo(db)
	anlegen := func(a storage.AgentTokenAnlage) string {
		k, _, err := repo.Anlegen(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	e := &env783{db: db, boards: boards, cfg: cfg, tokens: repo,
		assistent:   anlegen(storage.AgentTokenAnlage{Name: "assistent", BoardID: "b1", Role: storage.AgentRolleIntern, CreatedBy: "cli"}),
		assistentB2: anlegen(storage.AgentTokenAnlage{Name: "assistent-b2", BoardID: "b2", Role: storage.AgentRolleIntern, CreatedBy: "cli"}),
		projekt:     anlegen(storage.AgentTokenAnlage{Name: "projekt", BoardID: "b1", Role: storage.AgentRolleEditor, CreatedBy: "cli"}),
	}
	mux := http.NewServeMux()
	bapi := api.NewBoardsAPI(boards).MitKalenderKonfig(cfg).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).
		WithFold(testMotor).WithUsers(users).MitBoardpaar(cfg, bp, sr)
	bapi.Register(mux)
	bapi.RegisterWerkzeuge(mux)
	bapi.RegisterVorschlaege(mux)
	e.bapi = bapi
	e.agent = httptest.NewServer(api.NewAgentZugang(repo).Middleware(mux))
	t.Cleanup(e.agent.Close)
	var h http.Handler = api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)
	e.mensch = httptest.NewServer(api.OhneBearer(api.WithForwardAuthIfEnabled(h)))
	t.Cleanup(e.mensch.Close)
	return e
}

func rufe783(t *testing.T, basis, methode, pfad, body string, kopf map[string]string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(methode, basis+pfad, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env783) alsAgent(t *testing.T, token, methode, pfad, body string) (int, []byte) {
	t.Helper()
	return rufe783(t, e.agent.URL, methode, pfad, body, map[string]string{"Authorization": "Bearer " + token})
}

func (e *env783) alsMensch(t *testing.T, email, methode, pfad, body string) (int, []byte) {
	t.Helper()
	kopf := map[string]string{}
	if email != "" {
		kopf["Remote-Email"], kopf["Remote-User"] = email, email
	}
	return rufe783(t, e.mensch.URL, methode, pfad, body, kopf)
}

// karte783: die Karte mit id aus dem Board-Zustand (nil = gibt es nicht).
func (e *env783) karte783(t *testing.T, board, id string) map[string]any {
	t.Helper()
	b, err := e.boards.Get(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	var stand struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &stand); err != nil {
		t.Fatal(err)
	}
	for _, s := range stand.Board.Sprints {
		for _, k := range s.Cards {
			if k["id"] == id {
				return k
			}
		}
	}
	return nil
}

func (e *env783) offene(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM vorschlaege`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// liste783: die Vorschläge des Boards, wie Anna sie sieht.
func (e *env783) liste783(t *testing.T) []map[string]any {
	t.Helper()
	s, b := e.alsMensch(t, "anna@firma.de", "GET", "/boards/b1/vorschlaege", "")
	if s != http.StatusOK {
		t.Fatalf("Liste: %d %s", s, b)
	}
	var out struct {
		Vorschlaege []map[string]any `json:"vorschlaege"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out.Vorschlaege
}

func (e *env783) bestaetige(t *testing.T, email string, v map[string]any) (int, []byte) {
	t.Helper()
	return e.alsMensch(t, email, "POST", "/boards/b1/vorschlaege/"+v["id"].(string)+"/bestaetigen", `{"hash":"`+v["hash"].(string)+`"}`)
}

func TestVorschlag783_Positivliste(t *testing.T) {
	e := newEnv783(t)
	for _, f := range []struct{ methode, pfad, muss string }{
		{"GET", "/intern/b1/board", schatten783},
		{"GET", "/intern/b1/karte/1", "Offene Karte"},
		{"GET", "/intern/b1/karte/i-12", schatten783},
		{"GET", "/intern/b1/suche?q=Offene", "Offene Karte"},
	} {
		s, b := e.alsAgent(t, e.assistent, f.methode, f.pfad, "")
		if s != http.StatusOK || !strings.Contains(string(b), f.muss) {
			t.Errorf("%s %s: %d %.300s", f.methode, f.pfad, s, b)
		}
		if strings.Contains(string(b), "innen.example") {
			t.Errorf("%s %s: Link nach innen: %.300s", f.methode, f.pfad, b)
		}
	}
	// Alles andere: 403 — rohe Wege, Werkzeuge, MCP, fremdes Board, die
	// Vorschlags-Liste und der Bestätigungsweg (T13), andere Methoden.
	for _, f := range [][3]string{
		{"GET", "/boards/b1", ""}, {"GET", "/boards/b1/events", ""}, {"GET", "/boards/b1/ich", ""},
		{"POST", "/boards/b1/ops", `{"ops":[]}`}, {"GET", "/werkzeuge/boards/b1", ""}, {"GET", "/werkzeuge/boards", ""},
		{"GET", "/werkzeuge/karten?q=Offene", ""}, {"GET", "/openapi.json", ""},
		{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"GET", "/intern/b2/board", ""}, {"HEAD", "/intern/b1/board", ""}, {"GET", "/intern/b1/vorschlag", ""},
		{"POST", "/intern/b1/board", "{}"}, {"GET", "/intern/b1/karte/1/felder", ""},
		{"GET", "/boards/b1/vorschlaege", ""}, {"POST", "/boards/b1/vorschlaege/x/bestaetigen", `{"hash":"x"}`},
		{"DELETE", "/boards/b1/vorschlaege/x", ""},
	} {
		if s, b := e.alsAgent(t, e.assistent, f[0], f[1], f[2]); s != http.StatusForbidden {
			t.Errorf("intern %s %s: %d (erwartet 403) %.200s", f[0], f[1], s, b)
		}
	}
	// Die Wege gehören nur der Rolle intern: ein editor-Token und ein Mensch → 403.
	if s, b := e.alsAgent(t, e.projekt, "GET", "/intern/b1/board", ""); s != http.StatusForbidden {
		t.Errorf("editor-Token auf /intern: %d %.200s", s, b)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "GET", "/intern/b1/board", ""); s != http.StatusForbidden {
		t.Errorf("Mensch auf /intern: %d %.200s", s, b)
	}
	// Suche: q höchstens 100 Zeichen.
	if s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/suche?q="+strings.Repeat("a", 101), ""); s != http.StatusBadRequest {
		t.Errorf("q mit 101 Zeichen: %d %.200s", s, b)
	}
	// Nicht gekoppeltes Board: der Weg ist zu, auch für das eigene Token.
	if s, b := e.alsAgent(t, e.assistentB2, "GET", "/intern/b2/board", ""); s != http.StatusForbidden || !strings.Contains(string(b), "nicht gekoppelt") {
		t.Errorf("nicht gekoppelt: %d %.200s", s, b)
	}
	// Seite innen: die Rolle gibt es nicht.
	if err := e.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, api.BoardpaarSeiteInnen, false, "test"); err != nil {
		t.Fatal(err)
	}
	if s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/board", ""); s != http.StatusForbidden || !strings.Contains(string(b), "externen Server") {
		t.Errorf("Seite innen: %d %.200s", s, b)
	}
}

func TestVorschlag783_FelderSofortUndBremse(t *testing.T) {
	e := newEnv783(t)
	s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/karte/1/felder", `{"spalte":"Ready","prioritaet":"hoch"}`)
	if s != http.StatusOK {
		t.Fatalf("felder: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k1"); k["columnId"] != "col-ready" || k["priority"] != "hoch" {
		t.Fatalf("Karte nach felder: %v", k)
	}
	var akteur, art string
	if err := e.db.QueryRow(`SELECT actor, actor_kind FROM activities WHERE board_id = 'b1' AND card_id = 'k1' AND op_type = 'card.move'`).Scan(&akteur, &art); err != nil || akteur != "agent:assistent" || art != "agent" {
		t.Errorf("Protokoll: %q %q %v", akteur, art, err)
	}
	// Nur spalte und prioritaet; keine Schatten; keine Bearbeiter.
	for _, f := range [][2]string{
		{"/intern/b1/karte/1/felder", `{"owner":"Anna"}`},
		{"/intern/b1/karte/1/felder", `{"titel":"neu"}`},
		{"/intern/b1/karte/1/felder", `{}`},
		{"/intern/b1/karte/i-12/felder", `{"spalte":"Ready"}`},
		{"/intern/b1/karte/1/felder", `{"prioritaet":"dringend"}`},
		{"/intern/b1/karte/4/felder", `{"spalte":"Ready"}`}, // Sprint-Meta-Karte
	} {
		if s, b := e.alsAgent(t, e.assistent, "POST", f[0], f[1]); s != http.StatusBadRequest {
			t.Errorf("%s %s: %d (erwartet 400) %.200s", f[0], f[1], s, b)
		}
	}
	// Bremse: 2 schon verbraucht; noch 28 Änderungen (14 × 2), dann 429 (T14).
	for i := 0; i < 14; i++ {
		body := `{"spalte":"Backlog","prioritaet":"niedrig"}`
		if i%2 == 1 {
			body = `{"spalte":"Ready","prioritaet":"hoch"}`
		}
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/karte/1/felder", body); s != http.StatusOK {
			t.Fatalf("Änderung %d: %d %s", i, s, b)
		}
	}
	vorher := e.karte783(t, "b1", "k1")["columnId"]
	s, b, kopf := rufe783Kopf(t, e.agent.URL, "POST", "/intern/b1/karte/1/felder", `{"spalte":"In Arbeit"}`, map[string]string{"Authorization": "Bearer " + e.assistent})
	if s != http.StatusTooManyRequests || kopf.Get("Retry-After") == "" {
		t.Fatalf("31. Änderung: %d Retry-After=%q %s", s, kopf.Get("Retry-After"), b)
	}
	if k := e.karte783(t, "b1", "k1"); k["columnId"] != vorher {
		t.Errorf("gebremste Änderung hat geschrieben: %v", k)
	}
	// Lesen bleibt.
	if s, _ := e.alsAgent(t, e.assistent, "GET", "/intern/b1/board", ""); s != http.StatusOK {
		t.Errorf("Lesen nach der Bremse: %d", s)
	}
}

func TestVorschlag783_KommentarBestaetigen(t *testing.T) {
	e := newEnv783(t)
	s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"Bitte bis Freitag","fuer":"Anna"}`)
	if s != http.StatusOK || strings.Contains(string(b), "vorschlagId") {
		t.Fatalf("vorschlag: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k1"); len(k["comments"].([]any)) != 0 {
		t.Fatalf("Vorschlag wirkte vor der Bestätigung: %v", k["comments"])
	}
	// Sichtbar nur für Editoren in einer Menschen-Sitzung (T13).
	if s, b := e.alsMensch(t, "vera@firma.de", "GET", "/boards/b1/vorschlaege", ""); s != http.StatusForbidden {
		t.Errorf("Viewer sieht Vorschläge: %d %.200s", s, b)
	}
	if s, b := e.alsAgent(t, e.projekt, "GET", "/boards/b1/vorschlaege", ""); s != http.StatusForbidden {
		t.Errorf("editor-Token sieht Vorschläge: %d %.200s", s, b)
	}
	liste := e.liste783(t)
	if len(liste) != 1 {
		t.Fatalf("Liste: %v", liste)
	}
	v := liste[0]
	if v["art"] != "kommentar" || v["agent"] != "agent:assistent" || v["fuer"] != "Anna" || v["karte"].(map[string]any)["displayId"] != float64(1) {
		t.Errorf("Eintrag: %v", v)
	}
	if f := v["felder"].([]any); len(f) != 1 || f[0].(map[string]any)["wert"] != "Bitte bis Freitag" {
		t.Errorf("Felder: %v", f)
	}
	// Bestätigen: falscher Hash 409, Viewer 403, editor-Token am Menschen-Eingang 401,
	// über den Agenten-Eingang 403 (oben) — nichts geschrieben.
	if s, b := e.alsMensch(t, "anna@firma.de", "POST", "/boards/b1/vorschlaege/"+v["id"].(string)+"/bestaetigen", `{"hash":"falsch"}`); s != http.StatusConflict {
		t.Errorf("falscher Hash: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "vera@firma.de", v); s != http.StatusForbidden {
		t.Errorf("Viewer bestätigt: %d %s", s, b)
	}
	if s, b := rufe783(t, e.mensch.URL, "POST", "/boards/b1/vorschlaege/"+v["id"].(string)+"/bestaetigen", `{"hash":"`+v["hash"].(string)+`"}`,
		map[string]string{"Authorization": "Bearer " + e.projekt}); s != http.StatusUnauthorized {
		t.Errorf("Token am Menschen-Eingang: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k1"); len(k["comments"].([]any)) != 0 {
		t.Fatalf("Kommentar ohne Bestätigung: %v", k["comments"])
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusOK {
		t.Fatalf("bestätigen: %d %s", s, b)
	}
	kommentare := e.karte783(t, "b1", "k1")["comments"].([]any)
	if len(kommentare) != 1 {
		t.Fatalf("Kommentare: %v", kommentare)
	}
	c := kommentare[0].(map[string]any)
	if c["author"] != "agent:assistent" || c["text"] != "Bitte bis Freitag" {
		t.Errorf("Kommentar: %v", c)
	}
	if mail, da := c["authorEmail"]; da && mail != "" {
		t.Errorf("authorEmail des Bestätigers am Agenten-Kommentar: %v", c)
	}
	var akteur, art string
	if err := e.db.QueryRow(`SELECT actor, actor_kind FROM activities WHERE board_id = 'b1' AND op_type = 'comment.add'`).Scan(&akteur, &art); err != nil || akteur != "agent:assistent" || art != "agent" {
		t.Errorf("Protokoll der Op: %q %q %v", akteur, art, err)
	}
	var notiz string
	var bestaetiger string
	if err := e.db.QueryRow(`SELECT actor, note FROM activities WHERE board_id = 'b1' AND action = 'vorschlag-bestaetigt'`).Scan(&bestaetiger, &notiz); err != nil || bestaetiger != "anna@firma.de" {
		t.Errorf("Protokoll des Bestätigers: %q %q %v", bestaetiger, notiz, err)
	}
	// Verify V-7: die Adresse steht im actor (den die Nutzerlöschung pseudonymisiert), nicht in der Notiz.
	if strings.Contains(notiz, "@") {
		t.Errorf("Adresse in der Notiz: %q", notiz)
	}
	if strings.Contains(notiz, "Bitte bis Freitag") {
		t.Errorf("Protokoll trägt die Nutzlast: %q", notiz)
	}
	if n := e.offene(t); n != 0 {
		t.Errorf("nach dem Bestätigen noch %d Vorschläge in der Tabelle", n)
	}
	// Ein zweites Bestätigen findet nichts mehr.
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusNotFound {
		t.Errorf("zweites Bestätigen: %d %s", s, b)
	}
}

func TestVorschlag783_AblehnenGrenzeWiederholung(t *testing.T) {
	e := newEnv783(t)
	for i := 0; i < 10; i++ {
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", fmt.Sprintf(`{"art":"kommentar","karte":"1","text":"Nr %d"}`, i)); s != http.StatusOK {
			t.Fatalf("Vorschlag %d: %d %s", i, s, b)
		}
	}
	// Dieselbe Anfrage noch einmal: kein neuer Vorschlag.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"Nr 3"}`); s != http.StatusOK || !strings.Contains(string(b), `"wiederholt":true`) {
		t.Errorf("Wiederholung: %d %s", s, b)
	}
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"elf"}`); s != http.StatusTooManyRequests {
		t.Fatalf("11. Vorschlag: %d %s", s, b)
	}
	liste := e.liste783(t)
	if len(liste) != 10 {
		t.Fatalf("Liste: %d", len(liste))
	}
	if s, b := e.alsMensch(t, "vera@firma.de", "DELETE", "/boards/b1/vorschlaege/"+liste[0]["id"].(string), ""); s != http.StatusForbidden {
		t.Errorf("Viewer lehnt ab: %d %s", s, b)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "DELETE", "/boards/b1/vorschlaege/"+liste[0]["id"].(string), ""); s != http.StatusOK {
		t.Fatalf("ablehnen: %d %s", s, b)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM vorschlaege WHERE id = ?`, liste[0]["id"]).Scan(&n); err != nil || n != 0 {
		t.Errorf("abgelehnter Vorschlag noch in der Tabelle: %d %v", n, err)
	}
	// Ein Platz ist frei.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"elf"}`); s != http.StatusOK {
		t.Errorf("nach dem Ablehnen: %d %s", s, b)
	}
	// Nicht vorschlagbar: Arten außerhalb der Liste und Felder außerhalb der Art (T14).
	for _, body := range []string{
		`{"art":"anhang","karte":"1"}`, `{"art":"umstufen","karte":"1"}`, `{"art":"termin.anlegen","titel":"x"}`,
		`{"art":"karte.anlegen","titel":"x","labels":["a"]}`, `{"art":"karte.anlegen","titel":"x","owner":"Anna"}`,
		`{"art":"karte.anlegen","titel":"x","swimlane":"Sprint 1"}`, `{"art":"kommentar","karte":"i-12","text":"x"}`,
		`{"art":"kommentar","text":"ohne Karte"}`, `{"art":"karte.anlegen","titel":"Siehe https://beispiel.de"}`,
		`{"art":"karte.anlegen","titel":"Rückruf kunde@firma.de"}`,
		`{"art":"karte.anlegen","titel":"` + strings.Repeat("x", 201) + `"}`,
		`{"art":"kommentar","karte":"1","text":"` + strings.Repeat("x", 4001) + `"}`,
		`{"art":"kommentar","karte":"1","text":"x","fuer":"` + strings.Repeat("x", 81) + `"}`,
	} {
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", body); s != http.StatusBadRequest {
			t.Errorf("%.80s: %d (erwartet 400) %.200s", body, s, b)
		}
	}
}

func TestVorschlag783_KarteAnlegenTextLoeschen(t *testing.T) {
	e := newEnv783(t)
	// karte.anlegen mit Spalte und Frist.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.anlegen","titel":"Messestand planen","beschreibung":"Fläche klären","spalte":"Ready","faellig":"2026-11-02"}`); s != http.StatusOK {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	v := e.liste783(t)[0]
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusOK {
		t.Fatalf("bestätigen: %d %s", s, b)
	}
	s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/suche?q=Messestand", "")
	if s != http.StatusOK || !strings.Contains(string(b), "Messestand planen") {
		t.Fatalf("neue Karte nicht gefunden: %d %s", s, b)
	}
	var akteur string
	if err := e.db.QueryRow(`SELECT actor FROM activities WHERE board_id = 'b1' AND op_type = 'card.upsert'`).Scan(&akteur); err != nil || akteur != "agent:assistent" {
		t.Errorf("Anlage nicht unter dem Agenten: %q %v", akteur, err)
	}

	// karte.text: ändert ein Mensch die Karte vor der Bestätigung, schreibt die
	// Bestätigung nichts, und der Vorschlag bleibt (409).
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.text","karte":"5","titel":"Im Sprint (neu)"}`); s != http.StatusOK {
		t.Fatalf("karte.text: %d %s", s, b)
	}
	v = e.liste783(t)[0]
	if s, b := e.alsMensch(t, "anna@firma.de", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"k5","field":"title","value":"Von Anna"}}]}`); s != http.StatusOK {
		t.Fatalf("Anna ändert: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusConflict {
		t.Fatalf("Bestätigen nach Änderung: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k5"); k["title"] != "Von Anna" {
		t.Errorf("Titel überschrieben: %v", k["title"])
	}
	if l := e.liste783(t); len(l) != 1 || l[0]["id"] != v["id"] {
		t.Errorf("Vorschlag nicht wiederhergestellt: %v", l)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "DELETE", "/boards/b1/vorschlaege/"+v["id"].(string), ""); s != http.StatusOK {
		t.Fatalf("ablehnen: %d %s", s, b)
	}
	// karte.text ohne Zwischenänderung: wirkt.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.text","karte":"5","beschreibung":"Neu beschrieben"}`); s != http.StatusOK {
		t.Fatalf("karte.text 2: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", e.liste783(t)[0]); s != http.StatusOK {
		t.Fatalf("bestätigen text: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k5"); k["description"] != "Neu beschrieben" || k["title"] != "Von Anna" {
		t.Errorf("karte.text: %v", k)
	}

	// karte.loeschen: Änderung dazwischen → nichts gelöscht; sonst weg.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.loeschen","karte":"1"}`); s != http.StatusOK {
		t.Fatalf("loeschen: %d %s", s, b)
	}
	v = e.liste783(t)[0]
	if s, b := e.alsMensch(t, "anna@firma.de", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"priority","value":"hoch"}}]}`); s != http.StatusOK {
		t.Fatalf("Anna ändert k1: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusConflict {
		t.Fatalf("Löschen nach Änderung: %d %s", s, b)
	}
	if e.karte783(t, "b1", "k1") == nil {
		t.Fatal("Karte trotz Änderung gelöscht")
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "DELETE", "/boards/b1/vorschlaege/"+v["id"].(string), ""); s != http.StatusOK {
		t.Fatalf("ablehnen: %d %s", s, b)
	}
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.loeschen","karte":"1"}`); s != http.StatusOK {
		t.Fatalf("loeschen 2: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", e.liste783(t)[0]); s != http.StatusOK {
		t.Fatalf("bestätigen löschen: %d %s", s, b)
	}
	if e.karte783(t, "b1", "k1") != nil {
		t.Error("Karte nicht gelöscht")
	}
}

func TestVorschlag783_WiderrufenesTokenUndBearbeiter(t *testing.T) {
	e := newEnv783(t)
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"karte.bearbeiter","karte":"5","owner":"Anna","beteiligte":["Vera"]}`); s != http.StatusOK {
		t.Fatalf("bearbeiter: %d %s", s, b)
	}
	v := e.liste783(t)[0]
	if v["zugangAktiv"] != true {
		t.Errorf("zugangAktiv vor dem Widerruf: %v", v["zugangAktiv"])
	}
	if _, err := e.tokens.Revoke(context.Background(), "assistent", "test"); err != nil {
		t.Fatal(err)
	}
	if w := e.liste783(t)[0]; w["zugangAktiv"] != false {
		t.Errorf("zugangAktiv nach dem Widerruf: %v (Verify V-2)", w["zugangAktiv"])
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusConflict || !strings.Contains(string(b), "widerrufen") {
		t.Fatalf("Bestätigen nach Widerruf: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k5"); k["owner"] == "Anna" {
		t.Errorf("Bearbeiter trotz Widerruf gesetzt: %v", k)
	}
	if n := e.offene(t); n != 1 {
		t.Errorf("Vorschlag nach abgelehnter Bestätigung weg: %d", n)
	}
}

// TestVorschlag783_InternTokenNurAussen: Die Admin-API legt ein intern-Token
// nur auf der Seite außen an, und nur als Projekt-Token (ADR-0140 §8).
func TestVorschlag783_InternTokenNurAussen(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","title":"B"}}`)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-admin", "admin@firma.de", 1}, {"u-normal", "normal@firma.de", 0}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	repo := storage.NewAgentTokenRepo(db)
	mux := http.NewServeMux()
	adm := api.NewAdminAPI(cfg, storage.NewUserRepo(db), "test", time.Now())
	adm.RegisterAgentTokens(mux, repo, api.NewAgentZugang(repo))
	srv := httptest.NewServer(api.WithForwardAuthIfEnabled(mux))
	t.Cleanup(srv.Close)
	anlegen := func(body string) (int, []byte) {
		return rufe783(t, srv.URL, "POST", "/admin/agent-tokens", body, map[string]string{"Remote-Email": "admin@firma.de", "Remote-User": "admin@firma.de"})
	}
	body := `{"name":"assistent","board":"b1","role":"intern"}`
	for _, seite := range []string{"", api.BoardpaarSeiteInnen} {
		if seite != "" {
			if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
				t.Fatal(err)
			}
		}
		if s, b := anlegen(body); s != http.StatusBadRequest {
			t.Fatalf("Seite %q: %d %s", seite, s, b)
		}
	}
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, api.BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	if s, b := anlegen(`{"name":"assistent","board":"b1","role":"intern","user":"normal@firma.de"}`); s != http.StatusBadRequest {
		t.Errorf("intern mit Person: %d %s", s, b)
	}
	if s, b := anlegen(`{"name":"assistent","boards":["b1"],"role":"intern","user":"normal@firma.de"}`); s != http.StatusBadRequest {
		t.Errorf("intern für mehrere Boards: %d %s", s, b)
	}
	// Karte #799: außen sind Anbieter und Beleg Pflicht, auch für intern.
	if s, b := anlegen(body); s != http.StatusBadRequest {
		t.Fatalf("Seite außen ohne Anbieter/Beleg: %d %s", s, b)
	}
	if s, b := anlegen(strings.TrimSuffix(strings.TrimSpace(body), "}") + `,"anbieter":"Beispiel-Anbieter","beleg":"vertraege/vertrag.md"}`); s != http.StatusCreated {
		t.Fatalf("Seite außen: %d %s", s, b)
	}
}

// TestVorschlag783_BestaetigenZaehltBeimMenschen: Die Ausführung eines
// bestätigten Vorschlags läuft unter dem Agenten, zählt aber im Eimer des
// Menschen (ADR-0140 §8) — auch wenn der Agent seinen ausgeschöpft hat.
func TestVorschlag783_BestaetigenZaehltBeimMenschen(t *testing.T) {
	e := newEnv783(t)
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"Hallo"}`); s != http.StatusOK {
		t.Fatalf("vorschlag: %d %s", s, b)
	}
	e.bapi.SetzeAgentenOpsGrenzeFuerTest(1)
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/karte/1/felder", `{"spalte":"Ready"}`); s != http.StatusOK {
		t.Fatalf("erste Änderung: %d %s", s, b)
	}
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/karte/1/felder", `{"spalte":"Backlog"}`); s != http.StatusTooManyRequests {
		t.Fatalf("Eimer des Agenten nicht leer: %d %s", s, b)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", e.liste783(t)[0]); s != http.StatusOK {
		t.Fatalf("Bestätigen bei leerem Agenten-Eimer: %d %s", s, b)
	}
}

// TestVorschlag783_KeineWerkzeuge: werkzeugDarf schliesst die Rolle intern
// selbst aus — an Role und an tokenRolle (Stelle 2, Klasse 183).
func TestVorschlag783_KeineWerkzeuge(t *testing.T) {
	for _, nurToken := range []bool{false, true} {
		for _, f := range []struct {
			methode string
			segs    []string
		}{
			{"POST", []string{"mcp"}}, {"GET", []string{"openapi.json"}}, {"GET", []string{"werkzeuge", "boards"}},
			{"GET", []string{"werkzeuge", "boards", "b1"}}, {"GET", []string{"werkzeuge", "karten"}},
			{"POST", []string{"werkzeuge", "boards", "b1", "karten"}},
		} {
			if api.InternWerkzeugDarfFuerTest(f.methode, f.segs, nurToken) {
				t.Errorf("werkzeugDarf(intern, nurTokenRolle=%v) %s %v = true", nurToken, f.methode, f.segs)
			}
		}
	}
}

// TestVorschlag783_UnsichtbareZeichen (Kritik K-A1): Was der Mensch beim
// Bestätigen nicht sieht, wird nicht angenommen — in jedem Textfeld.
func TestVorschlag783_UnsichtbareZeichen(t *testing.T) {
	e := newEnv783(t)
	vektoren := map[string]string{
		"ZWSP": "\u200b", "RLO": "\u202e", "Tag-Block": "\U000E0049\U000E0067", "Zeilentrenner": "\u2028",
		"Variation Selector": "\ufe0f", "privat": "\ue000", "BOM": "\ufeff", "Soft Hyphen": "\u00ad",
		// Verify V-1: ZWJ/ZWNJ zwischen Buchstaben (Bits verstecken), Verify V-4: Füllzeichen.
		"ZWJ zwischen Buchstaben": "a\u200db", "ZWNJ": "\u200c", "Braille leer": "\u2800",
		"Hangul-Füller": "\u3164", "CGJ": "\u034f",
	}
	pruefeUnsichtbar783(t, e, vektoren)
	if n := e.offene(t); n != 0 {
		t.Fatalf("%d Vorschläge mit unsichtbaren Zeichen angenommen", n)
	}
	weiterUnsichtbar783(t, e)
}

// TestVorschlag783_UnsichtbareSelektoren (Sprint-91-Kritiker K-S1): eigene
// Umgebung, weil die Lese-Drossel (120 je Minute) für beide Tabellen nicht reicht.
func TestVorschlag783_UnsichtbareSelektoren(t *testing.T) {
	e := newEnv783(t)
	pruefeUnsichtbar783(t, e, map[string]string{
		"Mongolisch FVS1": "\u180b", "Mongolisch FVS2": "\u180c", "Mongolisch FVS3": "\u180d",
		"Mongolisch FVS4": "\u180f", "VS1": "\ufe00", "VS17": "\U000E0100",
	})
	if n := e.offene(t); n != 0 {
		t.Fatalf("%d Vorschläge mit unsichtbaren Zeichen angenommen", n)
	}
}

// pruefeUnsichtbar783: jeder Vektor in jedem Textfeld muss mit 400 abgelehnt werden.
func pruefeUnsichtbar783(t *testing.T, e *env783, vektoren map[string]string) {
	t.Helper()
	for name, z := range vektoren {
		// Die Vektoren stehen als Escape im Quelltext; dekodiert ein Werkzeug sie
		// beim Schreiben, prüft der Test nichts mehr (Memory „Escape in
		// Werkzeugparametern“) — deshalb hier die Probe, dass es echte Zeichen sind.
		if len(z) < 2 || (z[0] < 0x80 && !strings.ContainsAny(z, "\u200d\u200c")) {
			t.Fatalf("Vektor %s ist kein Mehrbyte-Zeichen: %q", name, z)
		}
		zj, _ := json.Marshal("Bitte" + z + " prüfen")
		for _, body := range []string{
			`{"art":"kommentar","karte":"1","text":` + string(zj) + `}`,
			`{"art":"karte.anlegen","titel":"Neu","beschreibung":` + string(zj) + `}`,
			`{"art":"karte.anlegen","titel":` + string(zj) + `}`,
			`{"art":"kommentar","karte":"1","text":"ok","fuer":` + string(zj) + `}`,
			`{"art":"karte.bearbeiter","karte":"5","owner":` + string(zj) + `}`,
			`{"art":"karte.bearbeiter","karte":"5","beteiligte":[` + string(zj) + `]}`,
			`{"art":"karte.text","karte":"5","beschreibung":` + string(zj) + `}`,
		} {
			if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", body); s != http.StatusBadRequest {
				t.Errorf("%s in %.60s: %d %.200s", name, body, s, b)
			}
		}
	}
}

// weiterUnsichtbar783: der zweite Teil von TestVorschlag783_UnsichtbareZeichen.
func weiterUnsichtbar783(t *testing.T, e *env783) {
	t.Helper()
	// Erlaubt: Zeilenumbruch und Tab in mehrzeiligen Feldern, geschütztes Leerzeichen, Umlaute, Emoji.
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"Zeile 1\nZeile 2\tmit Tab, Grüße\u00a0🙂"}`); s != http.StatusOK {
		t.Errorf("gewöhnlicher Text abgelehnt: %d %s", s, b)
	}
	// Verify V-1: gewöhnliche Emoji mit VS16, Keycap und ZWJ-Folgen sind erlaubt.
	for _, emoji := range []string{"\u26a0\ufe0f", "\u2764\ufe0f", "1\ufe0f\u20e3", "\U0001F468\u200d\U0001F469\u200d\U0001F467",
		"\U0001F44D\U0001F3FD", "\U0001F1E9\U0001F1EA"} {
		if strings.IndexFunc(emoji, func(r rune) bool { return r > 0x2000 }) < 0 {
			t.Fatalf("Emoji-Vektor ist nicht dekodiert: %q", emoji)
		}
		zj, _ := json.Marshal("Achtung " + emoji + " bitte")
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"2","text":`+string(zj)+`}`); s != http.StatusOK {
			t.Errorf("Emoji %q abgelehnt: %d %s", emoji, s, b)
		}
	}
	// Verify 2 W-1: kein Bit-Kanal über VS16 nach Ziffern ohne Keycap, ZWJ zwischen
	// Nicht-Bildzeichen, lange ZWJ-Ketten oder viele VS16.
	kette := strings.Repeat("\U0001F600\u200d", 8) + "\U0001F600"
	for name, text := range map[string]string{
		"VS16 nach Ziffer": "2\ufe0f024", "ZWJ zwischen Symbolen": "°\u200d°", "ZWJ-Kette": kette,
		"fünf VS16": strings.Repeat("\u26a0\ufe0f", 5),
	} {
		if strings.IndexFunc(text, func(r rune) bool { return r == 0x200d || r == 0xfe0f }) < 0 {
			t.Fatalf("Vektor %s nicht dekodiert", name)
		}
		zj, _ := json.Marshal(text)
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":`+string(zj)+`}`); s != http.StatusBadRequest {
			t.Errorf("%s angenommen: %d %s", name, s, b)
		}
	}
	// Die Meldung nennt Codepunkt und Stelle (Verify V-1).
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"ab\u200bc"}`); s != http.StatusBadRequest || !strings.Contains(string(b), "U+200B an Stelle 3") {
		t.Errorf("Meldung: %d %s", s, b)
	}
	// Externe Bearbeiter gibt es nicht als Vorschlag (K-A7).
	for _, body := range []string{
		`{"art":"karte.bearbeiter","karte":"5","owner":"external:kunde@x.de"}`,
		`{"art":"karte.bearbeiter","karte":"5","beteiligte":["Anna","kunde@x.de"]}`,
		`{"art":"karte.bearbeiter","karte":"5","beteiligte":["EXTERNAL:x"]}`,
	} {
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %.200s", body, s, b)
		}
	}
}

// TestVorschlag783_NumerischeKartenID (Kritik K-A2): Hat eine Karte die id
// „5“ und gibt es daneben Karte #5, trifft die Bestätigung nie die falsche.
func TestVorschlag783_NumerischeKartenID(t *testing.T) {
	e := newEnv783(t)
	if s, b := e.alsMensch(t, "anna@firma.de", "POST", "/boards/b1/ops",
		`{"ops":[{"type":"card.upsert","payload":{"card":{"id":"5","title":"Ziffern-id","columnId":"col-backlog","comments":[]},"sprintId":"__backlog__","columnId":"col-backlog"}}]}`); s != http.StatusOK {
		t.Fatalf("Karte mit id 5: %d %s", s, b)
	}
	k := e.karte783(t, "b1", "5")
	nr := fmt.Sprint(k["displayId"])
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"`+nr+`","text":"an Ziffern-id"}`); s != http.StatusOK {
		t.Fatalf("vorschlag: %d %s", s, b)
	}
	v := e.liste783(t)[0]
	if v["karteFehlt"] != true {
		t.Errorf("Liste müsste die Karte als nicht eindeutig melden: %v", v)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusConflict {
		t.Fatalf("Bestätigen: %d %s", s, b)
	}
	if c := e.karte783(t, "b1", "k5")["comments"].([]any); len(c) != 0 {
		t.Errorf("Kommentar an der falschen Karte #5: %v", c)
	}
}

// TestVorschlag783_GleicherVorschlagZweimal (Kritik K-A3): Ein zweiter,
// gleichlautender Vorschlag nach der ersten Bestätigung wirkt — der
// Wiederholungsschutz der Werkzeuge schluckt ihn nicht.
func TestVorschlag783_GleicherVorschlagZweimal(t *testing.T) {
	e := newEnv783(t)
	for i := 0; i < 2; i++ {
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"Erinnerung"}`); s != http.StatusOK || strings.Contains(string(b), "wiederholt") {
			t.Fatalf("Vorschlag %d: %d %s", i, s, b)
		}
		if s, b := e.bestaetige(t, "anna@firma.de", e.liste783(t)[0]); s != http.StatusOK {
			t.Fatalf("Bestätigen %d: %d %s", i, s, b)
		}
	}
	if c := e.karte783(t, "b1", "k1")["comments"].([]any); len(c) != 2 {
		t.Errorf("zwei bestätigte Kommentare erwartet: %v", c)
	}
}

// TestVorschlag783_ProtokollNurFuerEditoren (Kritik K-A4): Einträge zu
// Vorschlägen sehen weder Viewer noch Agenten; der Eintrag des Agenten trägt
// actor_kind agent.
func TestVorschlag783_ProtokollNurFuerEditoren(t *testing.T) {
	e := newEnv783(t)
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"x"}`); s != http.StatusOK {
		t.Fatalf("vorschlag: %d %s", s, b)
	}
	var art string
	if err := e.db.QueryRow(`SELECT actor_kind FROM activities WHERE action = 'vorschlag-angelegt'`).Scan(&art); err != nil || art != "agent" {
		t.Errorf("actor_kind: %q %v", art, err)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "GET", "/boards/b1/activities", ""); s != http.StatusOK || !strings.Contains(string(b), "vorschlag-angelegt") {
		t.Errorf("Editor sieht den Eintrag nicht: %d %.300s", s, b)
	}
	if s, b := e.alsMensch(t, "vera@firma.de", "GET", "/boards/b1/activities", ""); s != http.StatusOK || strings.Contains(string(b), "vorschlag") {
		t.Errorf("Viewer sieht Vorschlags-Einträge: %d %.300s", s, b)
	}
	if s, b := e.alsAgent(t, e.projekt, "GET", "/werkzeuge/boards/b1/verlauf", ""); s != http.StatusOK || strings.Contains(string(b), "vorschlag") {
		t.Errorf("Agent sieht Vorschlags-Einträge: %d %.300s", s, b)
	}
}

// TestVorschlag783_Luecken (Kritik K-B6, K-A11): archivieren, Bearbeiter,
// Ablauf, fremdes Board, Seite innen, Rennen, Formfehler, archivierte Karte.
func TestVorschlag783_Luecken(t *testing.T) {
	e := newEnv783(t)
	vorschlag := func(body string) map[string]any {
		t.Helper()
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", body); s != http.StatusOK {
			t.Fatalf("%s: %d %s", body, s, b)
		}
		l := e.liste783(t)
		return l[len(l)-1]
	}
	// karte.archivieren und karte.bearbeiter wirken nach Bestätigung.
	if s, b := e.bestaetige(t, "anna@firma.de", vorschlag(`{"art":"karte.archivieren","karte":"2"}`)); s != http.StatusOK {
		t.Fatalf("archivieren: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k2"); k["archived"] != true {
		t.Errorf("nicht archiviert: %v", k)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", vorschlag(`{"art":"karte.bearbeiter","karte":"5","owner":"Anna","beteiligte":["Vera"]}`)); s != http.StatusOK {
		t.Fatalf("bearbeiter: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k5"); k["owner"] != "Anna" || fmt.Sprint(k["assignees"]) != "[Vera]" {
		t.Errorf("bearbeiter: %v %v", k["owner"], k["assignees"])
	}
	// Abgelaufen: 410, und das Aufräumen löscht ihn.
	v := vorschlag(`{"art":"kommentar","karte":"1","text":"alt"}`)
	if _, err := e.db.Exec(`UPDATE vorschlaege SET laeuft_ab = '2000-01-01T00:00:00Z' WHERE id = ?`, v["id"]); err != nil {
		t.Fatal(err)
	}
	if s, b := e.bestaetige(t, "anna@firma.de", v); s != http.StatusGone {
		t.Fatalf("abgelaufen: %d %s", s, b)
	}
	if n := e.offene(t); n != 0 {
		t.Errorf("abgelaufener Vorschlag nicht aufgeräumt: %d", n)
	}
	var notiz string
	if err := e.db.QueryRow(`SELECT note FROM activities WHERE action = 'vorschlag-abgelaufen'`).Scan(&notiz); err != nil || strings.Contains(notiz, "alt") {
		t.Errorf("Protokoll Ablauf: %q %v", notiz, err)
	}
	// Vorschlag von b1 über b2 bestätigen/ablehnen: 404 (Anna ist auf b2 editor).
	v = vorschlag(`{"art":"kommentar","karte":"1","text":"b1"}`)
	if s, b := e.alsMensch(t, "anna@firma.de", "POST", "/boards/b2/vorschlaege/"+v["id"].(string)+"/bestaetigen", `{"hash":"`+v["hash"].(string)+`"}`); s != http.StatusNotFound {
		t.Errorf("fremdes Board bestätigen: %d %s", s, b)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "DELETE", "/boards/b2/vorschlaege/"+v["id"].(string), ""); s != http.StatusNotFound {
		t.Errorf("fremdes Board ablehnen: %d %s", s, b)
	}
	// Rennen: sechs gleichzeitige Bestätigungen, genau eine wirkt.
	statusse := make(chan int, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := e.bestaetige(t, "anna@firma.de", v)
			statusse <- s
		}()
	}
	wg.Wait()
	close(statusse)
	ok := 0
	for s := range statusse {
		if s == http.StatusOK {
			ok++
		}
	}
	if c := e.karte783(t, "b1", "k1")["comments"].([]any); ok != 1 || len(c) != 1 {
		t.Errorf("Rennen: %d erfolgreich, %d Kommentare", ok, len(c))
	}
	// Gleichzeitige Anlagen: nie 500.
	var anlage sync.WaitGroup
	fehler := make(chan string, 12)
	for i := 0; i < 12; i++ {
		anlage.Add(1)
		go func(i int) {
			defer anlage.Done()
			s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", fmt.Sprintf(`{"art":"kommentar","karte":"1","text":"p%d"}`, i))
			if s != http.StatusOK && s != http.StatusTooManyRequests {
				fehler <- fmt.Sprintf("%d %s", s, b)
			}
		}(i)
	}
	anlage.Wait()
	close(fehler)
	for f := range fehler {
		t.Errorf("parallele Anlage: %s", f)
	}
	// Formfehler und archivierte Karte (K-B6), Felder auf archivierter Karte (K-A11 M6).
	for _, body := range []string{
		`{"art":"karte.anlegen","titel":"x","faelligUhrzeit":"10:00"}`,
		`{"art":"karte.anlegen","titel":"x","prioritaet":"hoch"}`,
		`{"art":"karte.anlegen","titel":"x","spalte":"Gibt es nicht"}`,
		`{"art":"karte.anlegen","titel":"x","faellig":"2026-13-40"}`,
		`{"art":"kommentar","karte":"3","text":"an archivierte Karte"}`,
	} {
		if s, b := e.alsAgent(t, e.assistentB2, "POST", "/intern/b1/vorschlag", body); s != http.StatusForbidden {
			t.Errorf("fremdes Token %s: %d %.200s", body, s, b)
		}
		if s, b := e.alsAgentNeu(t, body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %.200s", body, s, b)
		}
	}
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/karte/3/felder", `{"spalte":"Backlog"}`); s != http.StatusBadRequest {
		t.Errorf("felder auf archivierter Karte: %d %s", s, b)
	}
	// Seite innen: die Menschen-Wege gibt es nicht (404).
	if err := e.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, api.BoardpaarSeiteInnen, false, "test"); err != nil {
		t.Fatal(err)
	}
	if s, b := e.alsMensch(t, "anna@firma.de", "GET", "/boards/b1/vorschlaege", ""); s != http.StatusNotFound {
		t.Errorf("Seite innen: %d %s", s, b)
	}
}

// alsAgentNeu: Vorschlag mit einem frischen intern-Token für b1 (die Grenze
// von 10 des Tokens assistent ist in TestVorschlag783_Luecken schon erreicht).
func (e *env783) alsAgentNeu(t *testing.T, body string) (int, []byte) {
	t.Helper()
	if e.frisch == "" {
		k, _, err := e.tokens.Anlegen(context.Background(), storage.AgentTokenAnlage{Name: "assistent-frisch", BoardID: "b1", Role: storage.AgentRolleIntern, CreatedBy: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		e.frisch = k
	}
	return e.alsAgent(t, e.frisch, "POST", "/intern/b1/vorschlag", body)
}

// TestVorschlag783_OhneAnmeldung: Ohne Forward-Auth (Trust aus, Legacy) sind
// die Menschen-Wege zu (403) — bestätigt wird nur in einer Sitzung.
func TestVorschlag783_OhneAnmeldung(t *testing.T) {
	mit := newEnv783(t)
	if s, b := mit.alsAgent(t, mit.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"x"}`); s != http.StatusOK {
		t.Fatalf("vorschlag: %d %s", s, b)
	}
	v := mit.liste783(t)[0]
	// Dieselbe Datenbank wäre ideal; eine zweite Umgebung ohne Trust mit
	// eigenem Vorschlag tut es auch (die Kette prüft nur, ob jemand angemeldet ist).
	e := newEnv783Mit(t, false)
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"x"}`); s != http.StatusOK {
		t.Fatalf("vorschlag ohne Trust: %d %s", s, b)
	}
	var vid string
	if err := e.db.QueryRow(`SELECT id FROM vorschlaege`).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	v = map[string]any{"id": vid, "hash": v["hash"]}
	for _, f := range [][3]string{
		{"GET", "/boards/b1/vorschlaege", ""},
		{"POST", "/boards/b1/vorschlaege/" + v["id"].(string) + "/bestaetigen", `{"hash":"` + v["hash"].(string) + `"}`},
		{"DELETE", "/boards/b1/vorschlaege/" + v["id"].(string), ""},
	} {
		if s, b := e.alsMensch(t, "", f[0], f[1], f[2]); s != http.StatusForbidden {
			t.Errorf("%s %s ohne Anmeldung: %d %s", f[0], f[1], s, b)
		}
	}
	if n := e.offene(t); n != 1 {
		t.Errorf("Vorschlag ohne Anmeldung verändert: %d", n)
	}
}

func rufe783Kopf(t *testing.T, basis, methode, pfad, body string, kopf map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(methode, basis+pfad, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// TestVorschlag783_FehlversucheGebremst (Kritik K-A6): Auch abgelehnte
// Anfragen an …/vorschlag und …/felder zählen gegen die Anfragen-Grenze —
// sonst lädt jede fehlerhafte Anfrage ungebremst das ganze Board.
func TestVorschlag783_FehlversucheGebremst(t *testing.T) {
	e := newEnv783(t)
	e.bapi.SetzeWerkzeugLeseGrenzeFuerTest(3)
	for i := 0; i < 3; i++ {
		if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"kommentar","karte":"999","text":"x"}`); s != http.StatusNotFound {
			t.Fatalf("Fehlversuch %d: %d %s", i, s, b)
		}
	}
	for _, f := range [][2]string{{"/intern/b1/vorschlag", `{"art":"kommentar","karte":"1","text":"x"}`}, {"/intern/b1/karte/1/felder", `{"spalte":"Ready"}`}} {
		if s, b := e.alsAgent(t, e.assistent, "POST", f[0], f[1]); s != http.StatusTooManyRequests {
			t.Errorf("%s nach drei Fehlversuchen: %d %s", f[0], s, b)
		}
	}
}

// F-2078: Der Hinweis boardpaar.hinweis in board_lesen unterscheidet die Rolle.
// Außen (Rolle editor) bleibt „Neue Karten gehören nach innen“; die Rolle
// intern liest über /intern/{board}/board einen eigenen Text (Vorschläge).
func TestVorschlag783_BoardpaarHinweisJeRolle(t *testing.T) {
	e := newEnv783(t)
	hinweis := func(s int, b []byte) string {
		t.Helper()
		if s != http.StatusOK {
			t.Fatalf("Board lesen: %d %.300s", s, b)
		}
		var m struct {
			Board struct {
				Boardpaar struct {
					Hinweis string `json:"hinweis"`
				} `json:"boardpaar"`
			} `json:"board"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m.Board.Boardpaar.Hinweis
	}
	aussen := hinweis(e.alsAgent(t, e.projekt, "GET", "/werkzeuge/boards/b1", ""))
	if !strings.Contains(aussen, "Neue Karten gehören nach innen") || strings.Contains(aussen, "interne Agent") {
		t.Errorf("Rolle außen: %q", aussen)
	}
	// K-2: Rolle und Gegenstück bleiben in der Kopie erhalten.
	sInt, bInt := e.alsAgent(t, e.assistent, "GET", "/intern/b1/board", "")
	var rg struct {
		Board struct {
			Boardpaar struct {
				Rolle       string `json:"rolle"`
				Gegenstueck string `json:"gegenstueck"`
			} `json:"boardpaar"`
		} `json:"board"`
	}
	if err := json.Unmarshal(bInt, &rg); err != nil || sInt != http.StatusOK ||
		rg.Board.Boardpaar.Rolle != "extern" || rg.Board.Boardpaar.Gegenstueck != "intern" {
		t.Errorf("Rolle/Gegenstück in der Kopie: %d %+v (%v)", sInt, rg, err)
	}
	intern := hinweis(sInt, bInt)
	if !strings.Contains(intern, "Spalte und Priorität") || !strings.Contains(intern, "sofort") {
		t.Errorf("Rolle intern ohne „Spalte und Priorität … sofort“: %q", intern)
	}
	if !strings.Contains(intern, "interne Agent") || !strings.Contains(intern, "Vorschlag") ||
		strings.Contains(intern, "gehören nach innen") {
		t.Errorf("Rolle intern: %q", intern)
	}
}

// K-4 (#808): Die Rolle intern bekommt keine Texte, die zu Werkzeugen schicken,
// die sie nicht hat (boards, boardId).
func TestVorschlag783_TexteOhneFremdeWerkzeuge(t *testing.T) {
	e := newEnv783(t)
	// (a) Der Standardtext bleibt außen; für die Rolle intern gilt der eigene
	// (verbotenMeldungFuer, Test im internen Paket). Fremde Boards fängt die
	// Positivliste schon vorher mit 403 ab.
	// (a) W-1: Der Assistent ruft einen Werkzeug-Pfad auf — 403 ohne Hinweis auf boards.
	if s, b := e.alsAgent(t, e.assistent, "GET", "/werkzeuge/boards", ""); s != http.StatusForbidden ||
		strings.Contains(string(b), "boards prüfen") || !strings.Contains(string(b), "/agent/intern/") {
		t.Errorf("Rolle intern auf /werkzeuge/boards: %d %.300s", s, b)
	}
	// Außen bleibt der bisherige Text.
	if s, b := e.alsAgent(t, e.projekt, "GET", "/werkzeuge/boards/b2", ""); s != http.StatusForbidden || !strings.Contains(string(b), "Mit boards prüfen") {
		t.Errorf("außen falsches Board: %d %.300s", s, b)
	}
	// (b) gekürzte Suche: kein „boardId“.
	ctx := context.Background()
	var menge []schatten.Eingang
	for i := 1; i <= 150; i++ {
		titel, nr := fmt.Sprintf("Karte Schatten %03d mit einem recht langen Titel zum Füllen", i), 100+i
		menge = append(menge, schatten.Eingang{SchattenID: "S" + fmt.Sprintf("%042d", i), Art: schatten.ArtKarte, Nr: &nr, Titel: &titel})
	}
	if _, err := storage.NewSchattenRepo(e.db).Ersetze(ctx, "b1", menge); err != nil {
		t.Fatal(err)
	}
	s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/suche?q=Karte", "")
	var m struct {
		InternGekuerzt bool   `json:"internGekuerzt"`
		InternHinweis  string `json:"internHinweis"`
	}
	if err := json.Unmarshal(b, &m); err != nil || s != http.StatusOK || !m.InternGekuerzt ||
		strings.Contains(m.InternHinweis, "boardId") || !strings.Contains(m.InternHinweis, "genaueren Suchbegriff") {
		t.Errorf("gekürzte Suche intern: %d %.400s", s, b)
	}
	s, b = e.alsAgent(t, e.projekt, "GET", "/werkzeuge/karten?q=Karte", "")
	if err := json.Unmarshal(b, &m); err != nil || s != http.StatusOK || !strings.Contains(m.InternHinweis, "Mit boardId") {
		t.Errorf("gekürzte Suche außen: %d %.400s", s, b)
	}
}

// W-4 (#808): Die Fehlermeldung bei unbekannter art nennt alle Vorschlagsarten
// des Codes (aus vorschlagFelder gebildet, sortiert).
func TestVorschlag783_UnbekannteArtNenntAlleArten(t *testing.T) {
	e := newEnv783(t)
	s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", `{"art":"gibt.es.nicht"}`)
	want := "karte.anlegen, karte.archivieren, karte.bearbeiter, karte.loeschen, karte.text, kommentar"
	if s != http.StatusBadRequest || !strings.Contains(string(b), want) {
		t.Errorf("unbekannte art: %d %.400s", s, b)
	}
}
