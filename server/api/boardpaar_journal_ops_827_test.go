package api_test

// Karte #827 (ADR-0141 §5.1, §7; Etappe E1b Teil 3): Spiegel-Schranke mit
// Rückweg und Journal am Ops-Weg, mit dem echten Fold — und der Schalter
// boardpaar.rueckweg (an/aus), die wartenden Spiegel im Schutzmodus und der
// Ganz-PUT, der nie journalisiert.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func newKopieEnv827(t *testing.T, rueckweg string) (*boardsTestEnv, *storage.ConfigRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	if rueckweg != "" {
		// Direkt in server_config: Der Admin-Weg nimmt „an“ bis E4 nicht an.
		if err := cfg.Set(ctx, api.KonfigBoardpaarRueckweg, rueckweg, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).WithFold(testMotor).
		MitBoardpaar(cfg, storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db)).Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)
	return env, cfg
}

// journal827 liest das Journal der Kopie, ohne abgeholt_at zu setzen.
func journal827(t *testing.T, env *boardsTestEnv) []storage.JournalAusgabe {
	t.Helper()
	abruf, err := storage.NewBoardRepo(env.db).JournalAbholen(context.Background(), kopie826, 0, 0, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	return abruf.Eintraege
}

func wartend827(t *testing.T, env *boardsTestEnv) map[string]bool {
	t.Helper()
	conn, err := env.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	w, err := storage.BoardpaarWartend(context.Background(), conn, kopie826)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// grund827: Das Protokoll trägt genau einen Eintrag „spiegel-geschuetzt“ zu
// dieser Karte, mit einem Grund, der text enthält.
func grund827(t *testing.T, out map[string]any, karte, text string) {
	t.Helper()
	spiegelZiel826(t, out, karte)
	e := out["protokoll"].([]any)[0].(map[string]any)
	if g, _ := e["grund"].(string); !strings.Contains(g, text) {
		t.Errorf("Grund %q, erwartet %q", g, text)
	}
}

func TestJournal827_RueckwegAnSchreibtJournal(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie826An(t, env)

	// Feld der Feldliste: angenommen, ein Eintrag „feld title“.
	s, out := postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"`+karte+`","field":"title","value":"außen"}}]`)
	if s != http.StatusOK {
		t.Fatalf("Feld: %d %v", s, out)
	}
	j := journal827(t, env)
	if len(j) != 1 || j[0].Art != "feld" || j[0].Stelle != "title" || string(j[0].Nachher) != `"außen"` ||
		j[0].Person != "anonym" || j[0].Akteur != "mensch" || j[0].Wartend {
		t.Fatalf("Journal nach Feld: %+v", j)
	}

	// Zweite Änderung derselben Stelle, noch nicht abgeholt: verschmolzen.
	if s, out := postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"`+karte+`","field":"title","value":"zweimal"}}]`); s != http.StatusOK {
		t.Fatalf("Feld 2: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 1 || string(j[0].Vorher) != `"Spiegel"` || string(j[0].Nachher) != `"zweimal"` {
		t.Fatalf("verschmolzen: %+v", j)
	}

	// Löschen eines Spiegels: 422 mit dem Grund des Rückwegs, kein Eintrag.
	s, out = postOps(t, env, kopie826, `[{"type":"card.delete","payload":{"cardId":"`+karte+`"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Löschen: %d %v", s, out)
	}
	grund827(t, out, karte, "cannot be carried back")
	if j := journal827(t, env); len(j) != 1 {
		t.Fatalf("Journal nach abgelehntem Löschen: %+v", j)
	}

	// Neue Karte in der markierten Spalte: angenommen, wird wartend.
	if s, out := postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`); s != http.StatusOK {
		t.Fatalf("Anlegen: %d %v", s, out)
	}
	if w := wartend827(t, env); !w["neu"] || len(w) != 1 {
		t.Fatalf("wartend: %v", w)
	}
	angelegt := 0
	for _, e := range journal827(t, env) {
		if e.Art == "angelegt" && e.Kennung == "neu" && e.Wartend {
			angelegt++
		}
	}
	if angelegt != 1 {
		t.Fatalf("angelegt-Einträge: %d (%+v)", angelegt, journal827(t, env))
	}
}

// F-2218: Der Eingang hat die Marke an eine wartende Karte gesetzt (E2 baut
// das; hier direkt im Stand nachgestellt), die Zeile in boardpaar_wartend
// steht noch. Eine Bearbeitung außen danach erfasst die Karte unter ihrer
// Marke — kein „entfernt“ für die Außen-ID.
func TestJournal827_MarkeAnWartenderKarte(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	legeKopie826An(t, env)
	if s, out := postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`); s != http.StatusOK {
		t.Fatalf("Anlegen: %d %v", s, out)
	}
	vorher := len(journal827(t, env))
	marke := strings.Repeat("N", 43)
	var state []byte
	if err := env.db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, kopie826).Scan(&state); err != nil {
		t.Fatal(err)
	}
	neu := strings.Replace(string(state), `"id":"neu",`, `"id":"neu","spiegel":{"id":"`+marke+`"},`, 1)
	if neu == string(state) {
		t.Fatalf("Karte „neu“ nicht im Stand gefunden: %s", state)
	}
	if _, err := env.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, neu, kopie826); err != nil {
		t.Fatal(err)
	}

	if s, out := postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"neu","field":"title","value":"Neu außen"}}]`); s != http.StatusOK {
		t.Fatalf("Bearbeiten nach Marke: %d %v", s, out)
	}
	j := journal827(t, env)
	if len(j) != vorher+1 {
		t.Fatalf("erwartet genau einen neuen Eintrag: %+v", j)
	}
	e := j[len(j)-1]
	if e.Art != "feld" || e.Kennung != marke || e.Wartend {
		t.Fatalf("Eintrag: %+v, erwartet feld unter der Marke", e)
	}
	for _, e := range j {
		if e.Art == "entfernt" {
			t.Fatalf("„entfernt“ im Journal: %+v", e)
		}
	}
}

// Rückweg wieder aus, Journal und wartende Karte bleiben: Die Schranke
// schützt Spiegel (2b) und wartende Karten, nichts kommt ins Journal.
func TestJournal827_RueckwegAusSchuetztWartende(t *testing.T) {
	env, cfg := newKopieEnv827(t, "an")
	karte := legeKopie826An(t, env)
	if s, out := postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`); s != http.StatusOK {
		t.Fatalf("Anlegen: %d %v", s, out)
	}
	if err := cfg.Set(context.Background(), api.KonfigBoardpaarRueckweg, "aus", false, "test"); err != nil {
		t.Fatal(err)
	}
	vorher := len(journal827(t, env))

	s, out := postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"neu","field":"title","value":"x"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("wartende Karte: %d %v", s, out)
	}
	grund827(t, out, "neu", "not yet arrived")

	s, out = postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"`+karte+`","field":"title","value":"x"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Spiegel: %d %v", s, out)
	}
	grund827(t, out, karte, "until the return path exists")

	// Eine eigene Lane bleibt frei.
	if s, out := postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}}]`); s != http.StatusOK {
		t.Fatalf("eigene Lane: %d %v", s, out)
	}
	if n := len(journal827(t, env)); n != vorher {
		t.Fatalf("Journal gewachsen: %d → %d", vorher, n)
	}
	if w := wartend827(t, env); !w["neu"] {
		t.Fatalf("wartend verloren: %v", w)
	}
}

// Der Ganz-PUT journalisiert nicht und lehnt deshalb auch mit Rückweg jede
// Änderung an einer wartenden Karte ab („Umsetzung 2c“ Entscheidung 2).
func TestJournal827_GanzPutLehntWartendeAb(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	legeKopie826An(t, env)
	if s, out := postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`); s != http.StatusOK {
		t.Fatalf("Anlegen: %d %v", s, out)
	}
	vorher := len(journal827(t, env))
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/"+kopie826, nil))
	raw := mustReadAll(t, resp.Body)
	resp.Body.Close()
	geaendert := strings.Replace(string(raw), `"title":"Neu"`, `"title":"per PUT"`, 1)
	if geaendert == string(raw) {
		t.Fatal("Karte „neu“ nicht im Stand")
	}
	req := opsReq(t, http.MethodPut, env.srv.URL+"/boards/"+kopie826, []byte(geaendert))
	req.Header.Set("If-Match", resp.Header.Get("ETag"))
	resp = mustDo(t, req)
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(text), "karte neu") {
		t.Fatalf("PUT: %d %s", resp.StatusCode, text)
	}
	if n := len(journal827(t, env)); n != vorher {
		t.Fatalf("PUT hat journalisiert: %d → %d", vorher, n)
	}
}

func TestJournal827_SchalterPruefung(t *testing.T) {
	for _, f := range []struct {
		ein, aus string
		ok       bool
	}{{"aus", "aus", true}, {"", "aus", true}, {" aus ", "aus", true}, {"an", "an", true}, {" an ", "an", true}, {"ja", "", false}} {
		got, err := api.PruefeBoardpaarRueckweg(api.KonfigBoardpaarRueckweg, f.ein)
		if (err == nil) != f.ok || got != f.aus {
			t.Errorf("%q: %q, %v", f.ein, got, err)
		}
	}
	// Lesen: fehlend, ungültig → aus; „an“ (direkt gesetzt) → an.
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" + fmt.Sprint(time.Now().UnixNano()) })
	for _, f := range []struct {
		wert string
		an   bool
	}{{"", false}, {"quatsch", false}, {"an", true}, {"aus", false}} {
		if f.wert != "" {
			if err := cfg.Set(ctx, api.KonfigBoardpaarRueckweg, f.wert, false, "t"); err != nil {
				t.Fatal(err)
			}
		}
		if an, err := api.BoardpaarRueckwegAnMitFehler(ctx, cfg); err != nil || an != f.an {
			t.Errorf("%q: %v, %v", f.wert, an, err)
		}
	}
}

// Seit E4 nimmt der Admin-Weg „an“ an (bis dahin abgelehnt, D1/PK2c-11), und
// das Umschalten gleicht die Rolle des Teams auf den Kopien an (D9): nur
// Subjekte aus boardpaar.team, nur auf Kopien, nur viewer ↔ editor, nur bei
// einem Wechsel.
func TestJournal827_AdminSchaltetTeamRolle(t *testing.T) {
	e := newBoardpaarAdminEnv(t)
	ctx := context.Background()
	setze := func(wert string) {
		t.Helper()
		if s, _, raw := e.do(t, http.MethodPut, "/admin/config/"+api.KonfigBoardpaarRueckweg, "admin@firma.de", map[string]any{"value": wert}); s != http.StatusOK {
			t.Fatalf("%q: %d %s", wert, s, raw)
		}
	}
	if s, _, raw := e.do(t, http.MethodPut, "/admin/config/"+api.KonfigBoardpaarTeam, "admin@firma.de", map[string]any{"value": "team@team.example, zwei@team.example"}); s != http.StatusOK {
		t.Fatalf("team: %d %s", s, raw)
	}
	acl := storage.NewAclRepo(e.db)
	k1 := "6f1c2c1e-0d4b-5e2a-9d5a-2b1f0c3a4e5d"
	frei := true // #836: freigegebene Kopien — die Rolle folgt hier allein dem Server-Schalter
	if _, err := e.paar.KopieAnlegenMit(ctx, k1, "K1", "https://i.example/#board=b", "skript:t", []string{"team@team.example", "zwei@team.example"}, &frei); err != nil {
		t.Fatal(err)
	}
	// Nicht im Team / kein Kopie-Board: bleiben unberührt.
	for _, z := range [][3]string{{k1, "fremd@firma.de", storage.RoleViewer}, {"b1", "team@team.example", storage.RoleViewer}} {
		if err := acl.Set(ctx, z[0], z[1], z[2]); err != nil {
			t.Fatal(err)
		}
	}
	rolle := func(board, wer string) string {
		t.Helper()
		ro, _, err := acl.GetRole(ctx, board, wer)
		if err != nil {
			t.Fatal(err)
		}
		return ro
	}
	pruefe := func(wann string, soll map[[2]string]string) {
		t.Helper()
		for k, v := range soll {
			if ist := rolle(k[0], k[1]); ist != v {
				t.Errorf("%s: %s/%s = %q, erwartet %q", wann, k[0], k[1], ist, v)
			}
		}
	}
	setze("an")
	if s, body, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil); s != http.StatusOK || body["rueckweg"] != "an" {
		t.Fatalf("liste nach an: %d %s", s, raw)
	}
	pruefe("an", map[[2]string]string{
		{k1, "team@team.example"}: storage.RoleEditor, {k1, "zwei@team.example"}: storage.RoleEditor,
		{k1, "fremd@firma.de"}: storage.RoleViewer, {"b1", "team@team.example"}: storage.RoleViewer,
		{k1, "admin@firma.de"}: storage.RoleOwner,
	})
	// Von Hand zurückgestuft, dann erneut „an“ gespeichert: kein Wechsel,
	// die Hand-Rolle bleibt.
	if err := acl.Set(ctx, k1, "zwei@team.example", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	setze("an")
	pruefe("an erneut", map[[2]string]string{{k1, "zwei@team.example"}: storage.RoleViewer, {k1, "team@team.example"}: storage.RoleEditor})
	// Eine Kopie, die bei „an“ entsteht, bekommt das Team gleich als editor.
	k2 := "0e5d8a2c-1111-5222-8333-44445555eeee"
	if _, err := e.paar.KopieAnlegenMit(ctx, k2, "K2", "https://i.example/#board=b", "skript:t", []string{"team@team.example"}, &frei); err != nil {
		t.Fatal(err)
	}
	pruefe("neue Kopie", map[[2]string]string{{k2, "team@team.example"}: storage.RoleEditor})
	setze("aus")
	if s, body, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil); s != http.StatusOK || body["rueckweg"] != "aus" {
		t.Fatalf("liste nach aus: %d %s", s, raw)
	}
	pruefe("aus", map[[2]string]string{
		{k1, "team@team.example"}: storage.RoleViewer, {k2, "team@team.example"}: storage.RoleViewer,
		{k1, "admin@firma.de"}: storage.RoleOwner, {"b1", "team@team.example"}: storage.RoleViewer,
	})
	// Löschen der Einstellung heißt „aus“: wieder an, dann löschen.
	setze("an")
	if s, _, raw := e.do(t, http.MethodDelete, "/admin/config/"+api.KonfigBoardpaarRueckweg, "admin@firma.de", nil); s != http.StatusOK {
		t.Fatalf("löschen: %d %s", s, raw)
	}
	pruefe("gelöscht", map[[2]string]string{{k1, "team@team.example"}: storage.RoleViewer, {k2, "team@team.example"}: storage.RoleViewer})
}

// F-2218 (Widerlegungsbedingung): die Schreibweisen des Browsers, wie
// diffToOps (src/diff.js) sie für eine Bearbeitung im Modal erzeugt —
// card.field je Feld samt Begleitfeldern updated und activity, comment.add,
// card.move mit Anker, card.upsert mit Anker. Die feine Schranke muss sie
// annehmen; das Journal erfasst nur die Feldliste, keine Begleitfelder.
func TestJournal827_BrowserSchreibweisen(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie826An(t, env)
	s, out := postOps(t, env, kopie826, `[
		{"type":"card.field","payload":{"cardId":"`+karte+`","field":"title","value":"Titel außen"}},
		{"type":"card.field","payload":{"cardId":"`+karte+`","field":"updated","value":"2026-10-13T20:00:00.000Z"}},
		{"type":"card.field","payload":{"cardId":"`+karte+`","field":"activity","value":[{"id":"a1","actor":"gerd@team.example","timestamp":"2026-10-13T20:00:00.000Z","type":"field","field":"title"}]}},
		{"type":"comment.add","payload":{"cardId":"`+karte+`","comment":{"id":"c1","author":"Gerd","text":"von außen","created":"2026-10-13"}}},
		{"type":"card.move","payload":{"cardId":"`+karte+`","toSprintId":"__backlog__","toColId":"col-ready","targetIndex":0,"anchor":null}},
		{"type":"card.upsert","payload":{"sprintId":"__backlog__","columnId":"col-ready","targetIndex":1,"anchor":"`+karte+`","card":{"id":"neu","title":"Neu","columnId":"col-ready","updated":"2026-10-13T20:00:00.000Z"}}}
	]`)
	if s != http.StatusOK {
		t.Fatalf("Browser-Batch: %d %v", s, out)
	}
	arten := map[string]int{}
	for _, e := range journal827(t, env) {
		arten[e.Art+" "+e.Stelle]++
		if e.Stelle == "updated" || e.Stelle == "activity" {
			t.Errorf("Begleitfeld im Journal: %+v", e)
		}
	}
	for _, soll := range []string{"feld title", "kommentar.add kommentar:c1", "verschoben ort", "angelegt "} {
		if arten[soll] != 1 {
			t.Errorf("%q: %d (alle: %v)", soll, arten[soll], arten)
		}
	}
	if w := wartend827(t, env); !w["neu"] {
		t.Errorf("neue Karte nicht wartend: %v", w)
	}
}

// F-2221 K-6: Ist boardpaar.rueckweg nicht lesbar (server_config auf einer
// geschlossenen Datenbank), lehnt der Ops-Weg jeden Batch auf einer Kopie mit
// 503 ab — auch einen, der bei „aus“ durchginge (eigene Lane) — und schreibt
// weder Board noch Journal.
func TestJournal827_SchalterUnlesbar503(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	kaputtDB, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = kaputtDB.Close()
	cfg := storage.NewConfigRepo(kaputtDB, nil, func() string { return "x" })
	if _, err := api.BoardpaarRueckwegAnMitFehler(ctx, cfg); err == nil {
		t.Fatal("Testaufbau: Schalter ist lesbar, der Test prüfte nichts")
	}
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).WithFold(testMotor).
		MitBoardpaar(cfg, storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db)).Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)
	karte := legeKopie826An(t, env)

	stand := func() (state string, journal int) {
		t.Helper()
		if err := db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, kopie826).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM boardpaar_journal`).Scan(&journal); err != nil {
			t.Fatal(err)
		}
		return state, journal
	}
	vorherState, vorherJournal := stand()
	for name, ops := range map[string]string{
		"eigene Lane": `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}}]`,
		"Spiegel":     `[{"type":"card.field","payload":{"cardId":"` + karte + `","field":"title","value":"außen"}}]`,
	} {
		if s, out := postOps(t, env, kopie826, ops); s != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %v, erwartet 503", name, s, out)
		}
	}
	if s, j := stand(); s != vorherState || j != vorherJournal {
		t.Fatalf("geschrieben trotz 503: Board gleich=%v, Journal %d → %d", s == vorherState, vorherJournal, j)
	}
}
