package main

// Karte #834 (ADR-0142): Zugang „Sicherungen lesen“ (abb_) an der echten
// Kette aus hauptHandler — Schranke an beiden Ketten, nur verschlüsselte und
// fertige Archive, 404 für alles andere, Drossel, Erneuern auf jeder Seite.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
)

const kopie834 = "0e5d8a2c-1111-5222-8333-44445555e834"

const (
	enc834      = "boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc"
	klar834     = "boardbackup-b1-20261018-010203-abcdef11.tar.gz"
	pending834  = "boardbackup-b1-20261019-020000-123456789-pending.tar.gz.enc"
	kopieEnc834 = "boardbackup-" + kopie834 + "-20261019-010203-abcdef13.tar.gz.enc"
	inhalt834   = "ABBKENC2-geheim"
)

type sicherungenSeite struct {
	srv *httptest.Server
	db  *storage.BoardpaarRepo
	cfg *storage.ConfigRepo
	dir string
	abb string
	abs string
	abe string
	abr string
}

func neueSicherungenSeite(t *testing.T, seite string) *sicherungenSeite {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sicherungen")
	if err := cfg.Set(ctx, "backup_board.dir", dir, false, "test"); err != nil {
		t.Fatal(err)
	}
	boards := storage.NewBoardRepo(db)
	paar := storage.NewBoardpaarRepo(db)
	for _, id := range []string{"b1", "b3"} {
		if _, err := boards.Insert(ctx, id, "Geheimer Titel "+id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := paar.KopieAnlegen(ctx, kopie834, "Kopie-Titel", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	dateien := map[string][]string{
		"b1":     {enc834, klar834, pending834, "boardbackup-b3-20261019-010203-abcdef14.tar.gz.enc"},
		"b3":     {"boardbackup-b3-20261018-010203-abcdef15.tar.gz"}, // nur unverschlüsselt
		kopie834: {kopieEnc834},
		"bx":     {"boardbackup-bx-20261019-010203-abcdef16.tar.gz.enc"}, // Board gibt es nicht, kein Grabstein
		"bg":     {"boardbackup-bg-20261019-010203-abcdef17.tar.gz.enc"}, // Board gelöscht, Grabstein folgt
		"bl":     {},                                                     // leerer Ordner ohne Board: nichts abzuholen, fehlt in der Liste (F-2322)
	}
	for board, namen := range dateien {
		if err := os.MkdirAll(filepath.Join(dir, board), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range namen {
			if err := os.WriteFile(filepath.Join(dir, board, n), []byte(inhalt834), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := boardbackup.MarkiereGeloescht(dir, "bg", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	api.NewBoardpaarErneuernAPI(cfg, paar).Register(mux)
	api.NewBoardpaarKopieAPI(boards, cfg, paar, storage.NewSchattenRepo(db), nil).Register(mux)
	api.NewSicherungenAPI(boards, cfg, paar).Register(mux)
	api.NewBoardBackupAPI(boards, storage.NewAclRepo(db), storage.NewUserRepo(db), nil, cfg, t.TempDir(), "test").Register(mux)
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(storage.NewAgentTokenRepo(db)), true))
	t.Cleanup(srv.Close)
	s := &sicherungenSeite{srv: srv, db: paar, cfg: cfg, dir: dir}
	s.abb, _, _ = paar.TokenAnlegen(ctx, storage.BoardpaarTypSicherungen, "abholung", "t")
	s.abs, _, _ = paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	s.abe, _, _ = paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "ein", "t")
	s.abr, _, _ = paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	if s.abb == "" || s.abs == "" || s.abe == "" || s.abr == "" {
		t.Fatal("Token nicht angelegt")
	}
	return s
}

func (s *sicherungenSeite) anfrage(t *testing.T, methode, pfad, token string) (int, []byte) {
	t.Helper()
	return anfrage825(t, s.srv, methode, pfad, token, nil)
}

// Schranke: abb_ nur auf den zwei Wegen und nur lesend; an der Menschen-Kette
// überall 403; die anderen Typen erreichen /sicherungen nicht.
func TestSicherungen834_Schranke(t *testing.T) {
	s := neueSicherungenSeite(t, "aus")
	if st, b := s.anfrage(t, "GET", "/agent/sicherungen", s.abb); st != 200 {
		t.Fatalf("Liste: %d %s", st, b)
	}
	if st, _ := s.anfrage(t, "HEAD", "/agent/sicherungen", s.abb); st != 200 {
		t.Errorf("Liste HEAD: %d", st)
	}
	if st, b := s.anfrage(t, "GET", "/agent/sicherungen/b1/"+enc834, s.abb); st != 200 || string(b) != inhalt834 {
		t.Errorf("Archiv: %d %q", st, b)
	}
	for _, f := range []struct{ methode, pfad string }{
		{"POST", "/agent/sicherungen"},
		{"PUT", "/agent/sicherungen/b1/" + enc834},
		{"DELETE", "/agent/sicherungen/b1/" + enc834},
		{"POST", "/agent/sicherungen/b1/" + enc834},
		{"GET", "/agent/sicherungen/b1"},
		{"GET", "/agent/sicherungen/b1/a/b"},
		{"GET", "/agent/sicherungen/b1%2F" + enc834 + "/x"},
		{"GET", "/agent/boards/b1/backup/files"},
		{"GET", "/agent/boards/b1/backup/download/" + enc834},
		{"GET", "/agent/schatten/b1"},
		{"GET", "/agent/boardpaar/auftraege"},
		{"GET", "/agent/boards"},
		// Menschen-Kette: kein Weg, auch nicht der eigene.
		{"GET", "/sicherungen"},
		{"GET", "/sicherungen/b1/" + enc834},
		{"GET", "/boards/b1/backup/files"},
		{"GET", "/boards/b1/backup/download/" + enc834},
		{"POST", "/backup/restore"},
		{"GET", "/boards"},
	} {
		if st, b := s.anfrage(t, f.methode, f.pfad, s.abb); st != http.StatusForbidden {
			t.Errorf("abb %s %s: %d %s, erwartet 403", f.methode, f.pfad, st, b)
		}
	}
	for typ, tok := range map[string]string{"abs": s.abs, "abe": s.abe, "abr": s.abr} {
		for _, p := range []string{"/agent/sicherungen", "/agent/sicherungen/b1/" + enc834} {
			if st, b := s.anfrage(t, "GET", p, tok); st != http.StatusForbidden {
				t.Errorf("%s GET %s: %d %s, erwartet 403", typ, p, st, b)
			}
		}
	}
}

// Liste: nur verschlüsselte, fertige Archive; ohne Board-Titel; kopie nach
// boardpaar_aussen. Ein Board mit Datenbankzeile, aber ohne abholbare
// Sicherung steht mit `files: []` und ohne `fehler` in der Liste (F-2322);
// `angelegt` tragen genau die Boards mit Zeile.
func TestSicherungen834_ListeNurEnc(t *testing.T) {
	s := neueSicherungenSeite(t, "aussen")
	st, b := s.anfrage(t, "GET", "/agent/sicherungen", s.abb)
	if st != 200 {
		t.Fatalf("Liste: %d %s", st, b)
	}
	if strings.Contains(string(b), "Titel") || strings.Contains(string(b), "title") {
		t.Errorf("Liste trägt Board-Titel: %s", b)
	}
	var antwort struct {
		Boards []struct {
			BoardID     string `json:"board_id"`
			Kopie       bool   `json:"kopie"`
			BoardStatus string `json:"board_status"`
			Fehler      string `json:"fehler"`
			Angelegt    string `json:"angelegt"`
			Files       []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MTime    string `json:"mtime"`
			} `json:"files"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(b, &antwort); err != nil {
		t.Fatal(err)
	}
	gesehen := map[string]string{}
	for _, e := range antwort.Boards {
		var namen []string
		for _, f := range e.Files {
			namen = append(namen, f.Filename)
			if f.Size != int64(len(inhalt834)) || f.MTime == "" {
				t.Errorf("%s: Größe %d, Zeit %q", f.Filename, f.Size, f.MTime)
			}
		}
		gesehen[e.BoardID] = fmt.Sprintf("kopie=%v %s %s|%s", e.Kopie, e.BoardStatus, strings.Join(namen, ","), e.Fehler)
		if e.Files == nil {
			t.Errorf("%s: files fehlt oder ist null, erwartet []", e.BoardID)
		}
		mitZeile := e.BoardStatus == "aktiv"
		if mitZeile {
			if _, err := time.Parse(time.RFC3339, e.Angelegt); err != nil {
				t.Errorf("%s: angelegt %q ist keine RFC-3339-Zeit", e.BoardID, e.Angelegt)
			}
		} else if e.Angelegt != "" {
			t.Errorf("%s: angelegt %q bei Board ohne Datenbankzeile", e.BoardID, e.Angelegt)
		}
	}
	soll := map[string]string{
		"b1":     "kopie=false aktiv " + enc834 + "|",
		kopie834: "kopie=true aktiv " + kopieEnc834 + "|",
		// Nur unverschlüsselt, also nichts abholbar: steht trotzdem da,
		// ohne `fehler` (F-2322, Befund des Partnerprojekts F-996 K-1).
		"b3": "kopie=false aktiv |",
		// Archive ohne Board in der Datenbank bleiben abholbar (F-2305).
		"bx": "kopie=false ohne-board boardbackup-bx-20261019-010203-abcdef16.tar.gz.enc|",
		"bg": "kopie=false geloescht boardbackup-bg-20261019-010203-abcdef17.tar.gz.enc|",
	}
	if fmt.Sprint(gesehen) != fmt.Sprint(soll) {
		t.Errorf("Liste:\n %v\nerwartet\n %v", gesehen, soll)
	}
	// last_used_at gesetzt.
	liste, _ := s.db.TokenListe(context.Background())
	for _, z := range liste {
		if z.Typ == storage.BoardpaarTypSicherungen && z.LastUsedAt == "" {
			t.Error("last_used_at nicht gesetzt")
		}
	}
}

// Download: 404 für unverschlüsselt, unfertig, fremdes Board und Pfadtricks; HEAD ohne Inhalt.
func TestSicherungen834_Download404(t *testing.T) {
	s := neueSicherungenSeite(t, "innen")
	for _, p := range []string{
		"/agent/sicherungen/b1/" + klar834,
		"/agent/sicherungen/b1/" + pending834,
		"/agent/sicherungen/b1/boardbackup-b3-20261019-010203-abcdef14.tar.gz.enc", // fremdes Board im Ordner b1
		"/agent/sicherungen/" + kopie834 + "/" + enc834,                            // Datei von b1 unter der Kopie
		"/agent/sicherungen/b3/" + enc834,
		"/agent/sicherungen/b1/boardbackup-b1-20261019-010203-ffffffff.tar.gz.enc", // Datei fehlt
		"/agent/sicherungen/b1/nicht-erlaubt.enc",
	} {
		if st, b := s.anfrage(t, "GET", p, s.abb); st != http.StatusNotFound || strings.Contains(string(b), inhalt834) {
			t.Errorf("GET %s: %d %q, erwartet 404", p, st, b)
		}
	}
	// Archive ohne Board in der Datenbank sind abholbar, mit und ohne Grabstein.
	for _, p := range []string{
		"/agent/sicherungen/bx/boardbackup-bx-20261019-010203-abcdef16.tar.gz.enc",
		"/agent/sicherungen/bg/boardbackup-bg-20261019-010203-abcdef17.tar.gz.enc",
	} {
		if st, b := s.anfrage(t, "GET", p, s.abb); st != http.StatusOK || string(b) != inhalt834 {
			t.Errorf("GET %s: %d %q, erwartet 200 mit Inhalt", p, st, b)
		}
	}
	// Pfadtricks: nie 200, nie Inhalt.
	for _, p := range []string{
		"/agent/sicherungen/../sicherungen/b1/" + enc834,
		"/agent/sicherungen/b1/..%2F..%2Fb1%2F" + enc834,
		"/agent/sicherungen/%2e%2e/" + enc834,
		"/agent/sicherungen/b1/%2e%2e",
	} {
		if st, b := s.anfrage(t, "GET", p, s.abb); st == 200 || strings.Contains(string(b), inhalt834) {
			t.Errorf("GET %s: %d %q", p, st, b)
		}
	}
	req, _ := http.NewRequest("HEAD", s.srv.URL+"/agent/sicherungen/b1/"+enc834, nil)
	req.Header.Set("Authorization", "Bearer "+s.abb)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != int64(len(inhalt834)) {
		t.Errorf("HEAD: %d Länge %d", resp.StatusCode, resp.ContentLength)
	}
}

// Fehlversuche: 401, ab der Grenze 429; ein gültiges Token läuft nie in die
// Drossel. Widerrufen → 401.
func TestSicherungen834_Drossel(t *testing.T) {
	s := neueSicherungenSeite(t, "aus")
	if st, _ := s.anfrage(t, "GET", "/agent/sicherungen", storage.SicherungenPraefix+"falsch"); st != http.StatusUnauthorized {
		t.Fatalf("unbekanntes Token: %d, erwartet 401", st)
	}
	letzte := 0
	for i := 0; i < 130; i++ {
		letzte, _ = s.anfrage(t, "GET", "/agent/sicherungen/b1/"+enc834, storage.SicherungenPraefix+"falsch")
	}
	if letzte != http.StatusTooManyRequests {
		t.Errorf("nach 131 Fehlversuchen: %d, erwartet 429", letzte)
	}
	if st, _ := s.anfrage(t, "GET", "/agent/sicherungen", s.abb); st != 200 {
		t.Errorf("gültiges Token nach Drossel: %d, erwartet 200", st)
	}
	z, err := s.db.TokenAktivNachName(context.Background(), storage.BoardpaarTypSicherungen, "abholung")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.TokenWiderrufen(context.Background(), z.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.anfrage(t, "GET", "/agent/sicherungen", s.abb); st != http.StatusTooManyRequests && st != http.StatusUnauthorized {
		t.Errorf("widerrufen: %d, erwartet 401 (oder 429 unter der Drossel)", st)
	}
	s2 := neueSicherungenSeite(t, "aus")
	z2, _ := s2.db.TokenAktivNachName(context.Background(), storage.BoardpaarTypSicherungen, "abholung")
	_ = s2.db.TokenWiderrufen(context.Background(), z2.ID)
	if st, _ := s2.anfrage(t, "GET", "/agent/sicherungen", s2.abb); st != http.StatusUnauthorized {
		t.Errorf("widerrufen: %d, erwartet 401", st)
	}
}

// Erneuern: abb_ auf jeder Seite, auch „aus“; dort bleibt es für die
// anderen Typen bei 404.
func TestSicherungen834_ErneuernAufJederSeite(t *testing.T) {
	for _, seite := range []string{"aus", "innen", "aussen"} {
		s := neueSicherungenSeite(t, seite)
		st, b := s.anfrage(t, "POST", "/agent/boardpaar/erneuern", s.abb)
		var e struct {
			Token, Typ string
		}
		_ = json.Unmarshal(b, &e)
		if st != 200 || e.Typ != "abb" || !strings.HasPrefix(e.Token, storage.SicherungenPraefix) {
			t.Errorf("%s: abb erneuern: %d %s", seite, st, b)
			continue
		}
		if st, _ := s.anfrage(t, "GET", "/agent/sicherungen", e.Token); st != 200 {
			t.Errorf("%s: neues Token: %d", seite, st)
		}
	}
	s := neueSicherungenSeite(t, "aus")
	if st, _ := s.anfrage(t, "POST", "/agent/boardpaar/erneuern", s.abs); st != http.StatusNotFound {
		t.Errorf("aus: abs erneuern: %d, erwartet 404", st)
	}
}
