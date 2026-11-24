package api_test

// F-3019/F-3020 (#1143): Der nachgeholte Karten-Purge (`?nurVerwaist=1`) darf nur
// eine Karte treffen, die im gespeicherten Stand fehlt. Steht sie dort noch
// (Löschen nie am Server angekommen, Karte wiederhergestellt), antwortet der
// Server 409 und lässt alle Dateien liegen — unabhängig davon, welchen Stand
// der Client für einen Beweis hielt (knowledge §56).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestF3019_NurVerwaist_KarteBestehtNoch_409(t *testing.T) {
	e := newEnvDatei(t)
	code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1?nurVerwaist=1", "olga@firma.de", "")
	if code != http.StatusConflict {
		t.Fatalf("nachgeholter Purge einer bestehenden Karte: %d %s, erwartet 409", code, b)
	}
	for _, id := range []string{"att-anna", "att-karte", "att-bert"} {
		if !e.dateiDa(id) {
			t.Fatalf("Datei %s der lebenden Karte gelöscht", id)
		}
	}
}

func TestF3019_NurVerwaist_KarteWeg_LoeschtAlles(t *testing.T) {
	e := newEnvDatei(t)
	store := storage.NewAttachmentStore(e.attachDir)
	if err := store.Save("b1", "k-weg", "att-weg", "weg.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k-weg?nurVerwaist=1", "bert@firma.de", "")
	if code != http.StatusNoContent {
		t.Fatalf("nachgeholter Purge einer verwaisten Karte: %d %s, erwartet 204", code, b)
	}
	if e.dateiDa("att-weg") {
		t.Fatal("Datei der verwaisten Karte nicht gelöscht")
	}
	if !e.dateiDa("att-karte") {
		t.Fatal("Datei einer anderen Karte mitgelöscht")
	}
}

// Ohne den Parameter bleibt der Sofort-Purge, wie er ist: Der Browser leert die
// Ablage, BEVOR er die Karte aus dem Stand nimmt.
func TestF3019_OhneParameter_SofortPurgeUnveraendert(t *testing.T) {
	e := newEnvDatei(t)
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1", "olga@firma.de", ""); code != http.StatusNoContent {
		t.Fatalf("Sofort-Purge: %d %s", code, b)
	}
	if e.dateiDa("att-karte") {
		t.Fatal("Sofort-Purge hat nicht gelöscht")
	}
}

// Fail-closed ohne Prüfung, bei fehlendem Board und unlesbarem Stand.
func TestF3019_NurVerwaist_FailClosed(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "kaputt", "K", "2", []byte(`{"version":"2"}`)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := storage.NewAttachmentStore(dir)
	for _, bid := range []string{"kaputt", "fehlt"} {
		if err := store.Save(bid, "k1", "att-1", "a.txt", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	boardsAPI := api.NewBoardsAPI(boards)
	faelle := []struct {
		name     string
		lage     bool
		board    string
		erwartet int
	}{
		{"ohne Prüfung", false, "kaputt", http.StatusConflict},
		{"Stand unlesbar", true, "kaputt", http.StatusConflict},
		{"Board fehlt", true, "fehlt", http.StatusNotFound},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			a := api.NewAttachmentsAPI(store, 1<<20)
			if f.lage {
				a = a.WithKartenLage(boardsAPI)
			}
			mux := http.NewServeMux()
			a.Register(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			req, _ := http.NewRequest("DELETE", srv.URL+"/boards/"+f.board+"/attachments/k1?nurVerwaist=1", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != f.erwartet {
				t.Fatalf("Status %d, erwartet %d", resp.StatusCode, f.erwartet)
			}
			if _, _, err := store.Open(f.board, "k1", "att-1"); err != nil {
				t.Fatalf("Datei trotz Ablehnung gelöscht: %v", err)
			}
		})
	}
}

// F-3013 (#1141, ADR-0136 Nachtrag 2026-11-11): Ist die Karte gelöscht, entfernt der
// nachgeholte Rest-Purge auch die Dateien fremder Kommentare — das Ergebnis hängt
// nicht mehr davon ab, ob Leeren oder Speichern zuerst ankam.
func TestF3013_RestNachLoeschenEntferntGeschuetzte(t *testing.T) {
	e := newEnvDatei(t)
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1", "bert@firma.de", ""); code != http.StatusOK {
		t.Fatalf("Sofort-Purge: %d %s", code, b)
	}
	if !e.dateiDa("att-anna") {
		t.Fatal("Sofort-Purge hat die geschützte Datei an der lebenden Karte entfernt")
	}
	leer := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,"columnDefs":[{"id":"col-ready","title":"Ready"}],"sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[]}]}}`
	if _, err := e.boards.Update(context.Background(), "b1", "B", "2", []byte(leer), 0); err != nil {
		t.Fatal(err)
	}
	if code, b := e.anfrage(t, "DELETE", "/boards/b1/attachments/k1?nurVerwaist=1", "bert@firma.de", ""); code != http.StatusNoContent {
		t.Fatalf("Rest-Purge nach dem Löschen: %d %s, erwartet 204", code, b)
	}
	if e.dateiDa("att-anna") {
		t.Fatal("geschützte Datei der gelöschten Karte blieb liegen")
	}
}

// F-3030 (#1149): Holt ein gleichzeitiges Schreiben die Karte zwischen erster und
// zweiter Prüfung zurück, bleiben ihre Dateien und die Antwort ist 409.
type lageFolge struct {
	antworten []bool
	fehler    []error // je Aufruf; nil-Einträge oder fehlend = kein Fehler
}

func (l *lageFolge) KarteBesteht(context.Context, string, string) (bool, error) {
	a := l.antworten[0]
	var e error
	if len(l.fehler) > 0 {
		e, l.fehler = l.fehler[0], l.fehler[1:]
	}
	if len(l.antworten) > 1 {
		l.antworten = l.antworten[1:]
	}
	return a, e
}

func TestF3030_KarteKommtZwischenPruefungenZurueck_409(t *testing.T) {
	for _, f := range []struct {
		name     string
		folge    []bool
		erwartet int
		bleibt   bool
	}{
		{"zurückgeholt", []bool{false, true}, http.StatusConflict, true},
		{"bleibt weg", []bool{false, false}, http.StatusNoContent, false},
	} {
		t.Run(f.name, func(t *testing.T) {
			store := storage.NewAttachmentStore(t.TempDir())
			if err := store.Save("b1", "k1", "att-1", "a.txt", strings.NewReader("x")); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			api.NewAttachmentsAPI(store, 1<<20).WithKartenLage(&lageFolge{antworten: f.folge}).Register(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			req, _ := http.NewRequest("DELETE", srv.URL+"/boards/b1/attachments/k1?nurVerwaist=1", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != f.erwartet {
				t.Fatalf("Status %d, erwartet %d", resp.StatusCode, f.erwartet)
			}
			_, _, err = store.Open("b1", "k1", "att-1")
			if (err == nil) != f.bleibt {
				t.Fatalf("Datei da=%v, erwartet %v", err == nil, f.bleibt)
			}
		})
	}
}

// F-3042: Verschwindet das Board zwischen erster und zweiter Prüfung → 404, Dateien
// zurückgeschoben (gelöscht werden sie mit dem Board).
func TestF3042_BoardVerschwindetZwischenPruefungen_404(t *testing.T) {
	store := storage.NewAttachmentStore(t.TempDir())
	if err := store.Save("b1", "k1", "att-1", "a.txt", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	lage := &lageFolge{antworten: []bool{false, false}, fehler: []error{nil, storage.ErrBoardNotFound}}
	api.NewAttachmentsAPI(store, 1<<20).WithKartenLage(lage).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest("DELETE", srv.URL+"/boards/b1/attachments/k1?nurVerwaist=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Status %d, erwartet 404", resp.StatusCode)
	}
	if _, _, err := store.Open("b1", "k1", "att-1"); err != nil {
		t.Fatalf("Datei nicht zurückgeschoben: %v", err)
	}
}
