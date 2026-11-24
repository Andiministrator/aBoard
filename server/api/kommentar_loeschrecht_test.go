package api_test

// ADR-0136 (Karte #693, F-1596): Wer darf welchen Kommentar löschen — über
// den echten Menschen-Weg (Forward-Auth, ACL-Guard, Fold).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type envLoesch struct {
	srv      *httptest.Server
	boards   *storage.BoardRepo
	mu       sync.Mutex
	hinweise []api.KommentarGeloescht
}

// Board b1, Karte k1 mit einem Altkommentar ohne Stempel (alt1).
// anna, bert: editor · olga: owner · root: editor + Server-Admin.
func newEnvLoesch(t *testing.T) *envLoesch {
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
	state := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"Karte eins",
	    "columnId":"col-ready","comments":[{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01"}],
	    "activity":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de", 0}, {"u-bert", "bert@firma.de", 0},
		{"u-olga", "olga@firma.de", 0}, {"u-root", "root@firma.de", 1}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	for email, rolle := range map[string]string{"anna@firma.de": "editor", "bert@firma.de": "editor",
		"olga@firma.de": "owner", "root@firma.de": "editor"} {
		if err := acl.Set(ctx, "b1", email, rolle); err != nil {
			t.Fatal(err)
		}
	}
	e := &envLoesch{boards: boards}
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).WithUsers(users).WithFold(testMotor).
		WithKommentarLoeschHinweis(func(_ context.Context, g api.KommentarGeloescht) {
			e.mu.Lock()
			e.hinweise = append(e.hinweise, g)
			e.mu.Unlock()
		}).Register(mux)
	e.srv = httptest.NewServer(api.WithForwardAuthIfEnabled(api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *envLoesch) ops(t *testing.T, wer, ops string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/boards/b1/ops", strings.NewReader(`{"ops":`+ops+`}`))
	req.Header.Set("Remote-Email", wer)
	req.Header.Set("Remote-User", wer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// karte liest k1 aus dem gespeicherten Stand.
func (e *envLoesch) karte(t *testing.T) map[string]any {
	t.Helper()
	b, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &s); err != nil {
		t.Fatal(err)
	}
	return s.Board.Sprints[0].Cards[0]
}

func (e *envLoesch) kommentar(t *testing.T, id string) map[string]any {
	t.Helper()
	ks, _ := e.karte(t)["comments"].([]any)
	for _, k := range ks {
		if m, ok := k.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	return nil
}

func kommentarAdd(id, text string, extra string) string {
	return `[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"` + id + `","author":"X","text":"` + text +
		`","created":"2026-10-04"` + extra + `}}}]`
}

func kommentarDelete(id string) string {
	return `[{"type":"comment.delete","payload":{"cardId":"k1","commentId":"` + id + `"}}]`
}

func TestKommentarLoeschrecht_StempelUndEigenerKommentar(t *testing.T) {
	e := newEnvLoesch(t)
	// Ein mitgeschickter authorEmail (jede Schreibweise) zählt nicht.
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", `,"AuthorEmail":"bert@firma.de"`)); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	k := e.kommentar(t, "a1")
	if k["authorEmail"] != "anna@firma.de" || k["AuthorEmail"] != nil {
		t.Fatalf("Stempel: %v", k)
	}
	// Eigener Kommentar: löschen erlaubt, kein Verlaufseintrag, kein Hinweis.
	if code, b := e.ops(t, "anna@firma.de", kommentarDelete("a1")); code != 200 {
		t.Fatalf("eigenen löschen: %d %s", code, b)
	}
	if e.kommentar(t, "a1") != nil {
		t.Fatal("eigener Kommentar steht noch")
	}
	if act, _ := e.karte(t)["activity"].([]any); len(act) != 0 || len(e.hinweise) != 0 {
		t.Fatalf("eigene Löschung erzeugt Verlauf/Hinweis: %v %v", act, e.hinweise)
	}
}

func TestKommentarLoeschrecht_FremderKommentarNurOwnerOderAdmin(t *testing.T) {
	e := newEnvLoesch(t)
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	// Bert (editor) darf Annas Kommentar nicht löschen.
	code, b := e.ops(t, "bert@firma.de", kommentarDelete("a1"))
	if code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Fatalf("Bert löscht fremden: %d %s, erwartet 422 fremder-kommentar", code, b)
	}
	if e.kommentar(t, "a1") == nil {
		t.Fatal("Kommentar trotz Ablehnung weg")
	}
	// Altbestand ohne Stempel: auch Anna nicht.
	if code, b := e.ops(t, "anna@firma.de", kommentarDelete("alt1")); code != 422 {
		t.Fatalf("Altbestand durch editor: %d %s, erwartet 422", code, b)
	}
	// Olga (Board-Owner) darf — Verlaufseintrag und Hinweis an Anna.
	if code, b := e.ops(t, "olga@firma.de", kommentarDelete("a1")); code != 200 {
		t.Fatalf("Owner löscht: %d %s", code, b)
	}
	if e.kommentar(t, "a1") != nil {
		t.Fatal("Kommentar steht nach Owner-Löschung noch")
	}
	act, _ := e.karte(t)["activity"].([]any)
	if len(act) != 1 {
		t.Fatalf("Verlauf: %v", act)
	}
	v := act[0].(map[string]any)
	if v["action"] != "comment-deleted-by-owner" || v["author"] != "olga@firma.de" || v["from"] != "anna@firma.de" || v["to"] != "von Anna" {
		t.Fatalf("Verlaufseintrag: %v", v)
	}
	if len(e.hinweise) != 1 || e.hinweise[0].AutorEmail != "anna@firma.de" || e.hinweise[0].Loescher != "olga@firma.de" ||
		e.hinweise[0].CardTitle != "Karte eins" || e.hinweise[0].CardDisplayID != 1 {
		t.Fatalf("Hinweis: %+v", e.hinweise)
	}
	// Server-Admin (Rolle editor) darf den Altbestand löschen; Verlauf nennt
	// den Anzeigenamen, ein Hinweis entsteht mit leerem Autor (main.go
	// verwirft ihn).
	if code, b := e.ops(t, "root@firma.de", kommentarDelete("alt1")); code != 200 {
		t.Fatalf("Admin löscht Altbestand: %d %s", code, b)
	}
	act, _ = e.karte(t)["activity"].([]any)
	if len(act) != 2 || act[1].(map[string]any)["from"] != "Altbestand" {
		t.Fatalf("Verlauf Altbestand: %v", act)
	}
}

// Volltext-Wege: card.field comments und card.upsert dürfen weder fremde
// Kommentare entfernen noch die Urheberschaft umschreiben.
func TestKommentarLoeschrecht_VolltextWege(t *testing.T) {
	e := newEnvLoesch(t)
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	// Bert schreibt die Liste neu und macht sich zum Autor — Stempel bleibt Anna.
	feld := `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":[` +
		`{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01"},` +
		`{"id":"a1","author":"X","text":"von Anna","created":"2026-10-04","authorEmail":"bert@firma.de"},` +
		`{"id":"b1","author":"Bert","text":"neu","created":"2026-10-04","authorEmail":"anna@firma.de"}]}}]`
	if code, b := e.ops(t, "bert@firma.de", feld); code != 200 {
		t.Fatalf("card.field: %d %s", code, b)
	}
	if k := e.kommentar(t, "a1"); k["authorEmail"] != "anna@firma.de" {
		t.Fatalf("Urheberschaft umgeschrieben: %v", k)
	}
	if k := e.kommentar(t, "b1"); k["authorEmail"] != "bert@firma.de" {
		t.Fatalf("neuer Kommentar nicht auf den Schreiber gestempelt: %v", k)
	}
	// Bert lässt Annas Kommentar in card.field weg → 422.
	ohneA1 := `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":[` +
		`{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01"},` +
		`{"id":"b1","author":"Bert","text":"neu","created":"2026-10-04"}]}}]`
	if code, b := e.ops(t, "bert@firma.de", ohneA1); code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Fatalf("card.field ohne fremden: %d %s, erwartet 422", code, b)
	}
	// Dasselbe als card.upsert der ganzen Karte.
	karte := e.karte(t)
	var rest []any
	for _, k := range karte["comments"].([]any) {
		if k.(map[string]any)["id"] != "a1" {
			rest = append(rest, k)
		}
	}
	karte["comments"] = rest
	roh, _ := json.Marshal([]any{map[string]any{"type": "card.upsert", "payload": map[string]any{"card": karte}}})
	if code, b := e.ops(t, "bert@firma.de", string(roh)); code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Fatalf("card.upsert ohne fremden: %d %s, erwartet 422", code, b)
	}
	if e.kommentar(t, "a1") == nil {
		t.Fatal("Annas Kommentar trotz Ablehnung weg")
	}
}
