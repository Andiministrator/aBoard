package api_test

// KF-3 (Sprint-82-Abschluss, Runde 2): Dateien an einem fremden Kommentar
// dürfen nur sein Autor und Owner/Admin entfernen oder umhängen — im Stand
// (Ops, PUT) wie in der Ablage (DELETE). Niemand hängt eine Datei unter einen
// fremden Kommentar.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type envDatei struct {
	srv       *httptest.Server
	attachDir string
	boards    *storage.BoardRepo
}

func newEnvDatei(t *testing.T) *envDatei {
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
	    "columnId":"col-ready",
	    "comments":[{"id":"c-anna","author":"Anna","authorEmail":"anna@firma.de","text":"Beleg","created":"2026-10-01"},
	                {"id":"c-bert","author":"Bert","authorEmail":"bert@firma.de","text":"Meins","created":"2026-10-01"}],
	    "attachments":[{"id":"att-anna","filename":"beleg.txt","commentId":"c-anna"},
	                   {"id":"att-karte","filename":"karte.txt"},
	                   {"id":"att-bert","filename":"bert.txt","commentId":"c-bert"}],
	    "activity":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de", 0}, {"u-bert", "bert@firma.de", 0}, {"u-olga", "olga@firma.de", 0}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	for email, rolle := range map[string]string{"anna@firma.de": "editor", "bert@firma.de": "editor", "olga@firma.de": "owner"} {
		if err := acl.Set(ctx, "b1", email, rolle); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	store := storage.NewAttachmentStore(dir)
	for _, id := range []string{"att-anna", "att-karte", "att-bert"} {
		if err := store.Save("b1", "k1", id, id+".txt", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	boardsAPI := api.NewBoardsAPI(boards).WithACL(acl).WithUsers(users).WithFold(testMotor)
	boardsAPI.Register(mux)
	api.NewAttachmentsAPI(store, 1<<20).WithKommentarDateiRecht(boardsAPI).WithKartenLage(boardsAPI).Register(mux)
	e := &envDatei{attachDir: dir, boards: boards}
	e.srv = httptest.NewServer(api.WithForwardAuthIfEnabled(api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *envDatei) anfrage(t *testing.T, methode, pfad, wer, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(methode, e.srv.URL+pfad, r)
	req.Header.Set("Remote-Email", wer)
	req.Header.Set("Remote-User", wer)
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

func (e *envDatei) dateiDa(id string) bool {
	treffer, _ := filepath.Glob(filepath.Join(e.attachDir, "*", "*", id+"__*"))
	if len(treffer) == 0 {
		treffer, _ = filepath.Glob(filepath.Join(e.attachDir, "**", id+"__*"))
	}
	if len(treffer) > 0 {
		_, err := os.Stat(treffer[0])
		return err == nil
	}
	return false
}

func TestKF3_DeleteDateiFremderKommentar(t *testing.T) {
	e := newEnvDatei(t)
	if !e.dateiDa("att-anna") {
		t.Fatal("Testaufbau: Datei att-anna nicht gefunden")
	}
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1/att-anna", "bert@firma.de", ""); code != http.StatusForbidden {
		t.Fatalf("Bert löscht Annas Kommentar-Datei: %d %s, erwartet 403", code, b)
	}
	if !e.dateiDa("att-anna") {
		t.Fatal("Datei trotz 403 weg")
	}
	// Eigene Kommentar-Datei und Karten-Datei: frei.
	for _, id := range []string{"att-bert", "att-karte"} {
		if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1/"+id, "bert@firma.de", ""); code != http.StatusNoContent {
			t.Fatalf("Bert löscht %s: %d %s", id, code, b)
		}
	}
	// Autorin selbst und Owner: erlaubt.
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1/att-anna", "anna@firma.de", ""); code != http.StatusNoContent {
		t.Fatalf("Anna löscht eigene: %d %s", code, b)
	}
}

func TestKF3_OwnerDarfFremdeDateiLoeschen(t *testing.T) {
	e := newEnvDatei(t)
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1/att-anna", "olga@firma.de", ""); code != http.StatusNoContent {
		t.Fatalf("Owner: %d %s", code, b)
	}
	if e.dateiDa("att-anna") {
		t.Fatal("Datei nach Owner-Löschung noch da")
	}
}

func TestKF3_PurgeLaesstFremdeKommentarDateiStehen(t *testing.T) {
	e := newEnvDatei(t)
	// F-3013 (#1141): 200 mit der Anzahl der stehen gebliebenen Dateien statt 204.
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1", "bert@firma.de", ""); code != http.StatusOK || !strings.Contains(b, `"geschuetztGeblieben":1`) {
		t.Fatalf("Purge: %d %s, erwartet 200 mit geschuetztGeblieben 1", code, b)
	}
	if !e.dateiDa("att-anna") {
		t.Fatal("Purge hat Annas Kommentar-Datei entfernt")
	}
	if e.dateiDa("att-karte") || e.dateiDa("att-bert") {
		t.Fatal("Purge hat die übrigen Dateien nicht entfernt")
	}
}

func TestKF3_StandEntfernenUmhaengenAnheften(t *testing.T) {
	e := newEnvDatei(t)
	ohneAnna := `[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[{"id":"att-karte","filename":"karte.txt"},{"id":"att-bert","filename":"bert.txt","commentId":"c-bert"}]}}]`
	if code, b := e.anfrage(t, "POST", "/boards/b1/ops", "bert@firma.de", `{"ops":`+ohneAnna+`}`); code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Fatalf("Entfernen aus dem Stand: %d %s, erwartet 422 fremder-kommentar", code, b)
	}
	umgehaengt := `[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[{"id":"att-anna","filename":"beleg.txt","commentId":"c-bert"},{"id":"att-karte","filename":"karte.txt"},{"id":"att-bert","filename":"bert.txt","commentId":"c-bert"}]}}]`
	if code, b := e.anfrage(t, "POST", "/boards/b1/ops", "bert@firma.de", `{"ops":`+umgehaengt+`}`); code != 422 {
		t.Fatalf("Umhängen: %d %s, erwartet 422", code, b)
	}
	angeheftet := `[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[{"id":"att-anna","filename":"beleg.txt","commentId":"c-anna"},{"id":"att-karte","filename":"karte.txt","commentId":"c-anna"},{"id":"att-bert","filename":"bert.txt","commentId":"c-bert"}]}}]`
	if code, b := e.anfrage(t, "POST", "/boards/b1/ops", "bert@firma.de", `{"ops":`+angeheftet+`}`); code != 422 {
		t.Fatalf("Anheften unter fremden Kommentar: %d %s, erwartet 422", code, b)
	}
	// Eigene Kommentar-Datei aus dem Stand nehmen: frei.
	ohneBert := `[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[{"id":"att-anna","filename":"beleg.txt","commentId":"c-anna"},{"id":"att-karte","filename":"karte.txt"}]}}]`
	if code, b := e.anfrage(t, "POST", "/boards/b1/ops", "bert@firma.de", `{"ops":`+ohneBert+`}`); code != 200 {
		t.Fatalf("Eigene Datei aus dem Stand: %d %s", code, b)
	}
	// Owner darf Annas Datei aus dem Stand nehmen.
	nurKarte := `[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[{"id":"att-karte","filename":"karte.txt"}]}}]`
	if code, b := e.anfrage(t, "POST", "/boards/b1/ops", "olga@firma.de", `{"ops":`+nurKarte+`}`); code != 200 {
		t.Fatalf("Owner: %d %s", code, b)
	}
}

// SR-1 (Sitzungs-Kritiker): Der PUT meldet per X-Aboard-Stand-Geaendert, wenn
// der Server den Stand verändert hat (hier: neuer Kommentar gestempelt) — und
// nur dann.
func TestSR1_PutMeldetVeraendertenStand(t *testing.T) {
	e := newEnvDatei(t)
	b, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	etag := ""
	{
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/boards/b1", nil)
		req.Header.Set("Remote-Email", "bert@firma.de")
		req.Header.Set("Remote-User", "bert@firma.de")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		etag = resp.Header.Get("ETag")
	}
	put := func(body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/boards/b1", strings.NewReader(body))
		req.Header.Set("If-Match", etag)
		req.Header.Set("Remote-Email", "bert@firma.de")
		req.Header.Set("Remote-User", "bert@firma.de")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		etag = resp.Header.Get("ETag")
		return resp
	}
	resp := put(string(b.StateJSON))
	if resp.StatusCode != 200 || resp.Header.Get("X-Aboard-Stand-Geaendert") != "" {
		t.Fatalf("unveränderter Stand: %d, Kopfzeile %q", resp.StatusCode, resp.Header.Get("X-Aboard-Stand-Geaendert"))
	}
	neu := strings.Replace(string(b.StateJSON), `"comments":[`, `"comments":[{"id":"c-neu","author":"Bert","text":"neu","created":"2026-10-05"},`, 1)
	resp = put(neu)
	if resp.StatusCode != 200 || resp.Header.Get("X-Aboard-Stand-Geaendert") != "1" {
		t.Fatalf("gestempelter Kommentar: %d, Kopfzeile %q, erwartet 1", resp.StatusCode, resp.Header.Get("X-Aboard-Stand-Geaendert"))
	}
}
