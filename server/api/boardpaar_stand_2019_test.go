package api

// F-2019 (Sprint-89-Kritiker KS-4, Karte #805): Ausgang innen und Eingang
// außen merken je gekoppeltem Board den letzten Lauf und seinen Fehlgrund;
// ein erfolgreicher Lauf löscht den Grund. Die Partnerlisten (Admin-Reiter,
// `boardpaar liste`) tragen beides.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaarStand2019_EingangAussen(t *testing.T) {
	_, mux, abe, db := eingangUmgebung779(t)
	paar := storage.NewBoardpaarRepo(db)
	ctx := context.Background()
	schicke := func(body string) int {
		req := httptest.NewRequest("PUT", "/schatten/b1", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+abe)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	stand := func() storage.BoardpaarAussen {
		l, err := paar.AussenListe(ctx)
		if err != nil || len(l) != 1 {
			t.Fatalf("Liste: %+v %v", l, err)
		}
		return l[0]
	}
	if s := stand(); s.LetzterEingang != "" || s.LetzterFehlgrund != "" {
		t.Fatalf("vor dem ersten Eingang: %+v", s)
	}
	if code := schicke(`{`); code != 422 {
		t.Fatalf("kaputter Body: %d", code)
	}
	if s := stand(); s.LetzterEingang == "" || !strings.HasPrefix(s.LetzterFehlgrund, "422: ") {
		t.Errorf("nach Ablehnung: %+v, erwartet Zeitpunkt und Grund „422: …“", s)
	}
	if code := schicke(`{"version":"1.0.0","schatten":[]}`); code != http.StatusOK {
		t.Fatalf("gültiger Lauf: %d", code)
	}
	if s := stand(); s.LetzterEingang == "" || s.LetzterFehlgrund != "" {
		t.Errorf("nach Erfolg: %+v, erwartet Zeitpunkt ohne Grund", s)
	}
}

func TestBoardpaarStand2019_AusgangInnen(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" })
	if err := cfg.Set(ctx, KonfigBoardpaarSeite, BoardpaarSeiteInnen, false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	if err := paar.InnenKoppeln(ctx, "b1", "test"); err != nil {
		t.Fatal(err)
	}
	abs, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	hole := func() int {
		req := httptest.NewRequest("GET", SchattenPfad+"b1", nil)
		req.Header.Set("Authorization", "Bearer "+abs)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	stand := func() storage.BoardpaarInnen {
		l, err := paar.InnenListe(ctx)
		if err != nil || len(l) != 1 {
			t.Fatalf("Liste: %+v %v", l, err)
		}
		return l[0]
	}
	if code := hole(); code != http.StatusOK {
		t.Fatalf("erster Abruf: %d", code)
	}
	if s := stand(); s.LetzteAbfrage == "" || s.LetzterFehlgrund != "" {
		t.Errorf("nach Erfolg: %+v", s)
	}
	if _, err := db.Exec(`UPDATE boards SET state_json = '{kaputt' WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	if code := hole(); code != http.StatusInternalServerError {
		t.Fatalf("kaputter Zustand: %d", code)
	}
	if s := stand(); !strings.Contains(s.LetzterFehlgrund, "nicht auswertbar") {
		t.Errorf("nach Fehlschlag: %+v, erwartet Grund „Board-Zustand nicht auswertbar …“", s)
	}
}

// F-2030: Die Board-Paar-Drossel zählt nach derselben Regel wie die
// Agenten-Bremse. Ein Client, der je Fehlversuch ein neues X-Forwarded-For
// anhängt, bekommt dadurch kein frisches Kontingent; X-Real-IP zählt nur
// von der Gegenstelle aus ABOARD_AGENT_PROXY.
func TestBoardpaarDrossel2030_Adressregel(t *testing.T) {
	_, mux, _, _ := eingangUmgebung779(t)
	versuch := func(i int, realIP string) int {
		req := httptest.NewRequest("PUT", "/schatten/b1", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer abe_falsch")
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i%250))
		if realIP != "" {
			req.Header.Set("X-Real-IP", realIP)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	gedrosselt := false
	for i := 0; i < 130 && !gedrosselt; i++ {
		gedrosselt = versuch(i, "") == http.StatusTooManyRequests
	}
	if !gedrosselt {
		t.Fatal("130 Fehlversuche mit wechselndem X-Forwarded-For: nie 429 — die Adresse kommt noch aus dem Header")
	}

	// Mit der Regel der Bremse und eingetragenem Proxy zählt X-Real-IP der
	// Gegenstelle: ein anderer Client hinter demselben Proxy ist frei.
	a, mux2, _, _ := eingangUmgebung779(t)
	z := NewAgentZugang(nil)
	if err := z.MitProxy("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	a.MitAdressregel(z.ClientAdresse)
	mux = mux2
	for i := 0; i < 130; i++ {
		versuch(i, "203.0.113.7")
	}
	if code := versuch(0, "203.0.113.7"); code != http.StatusTooManyRequests {
		t.Errorf("gesperrter Client hinter dem Proxy: %d, erwartet 429", code)
	}
	if code := versuch(0, "203.0.113.8"); code != http.StatusUnauthorized {
		t.Errorf("anderer Client hinter dem Proxy: %d, erwartet 401", code)
	}
}

// Sitzungs-QA K-3: der Auslöser von F-2019 selbst — mehr Einträge als die
// Betriebsgrenze. Der Ausgang antwortet 500, der Admin sieht den Grund.
func TestBoardpaarStand2019_AusgangZuViele(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var karten strings.Builder
	for i := 0; i <= schatten.EingangMaxSchatten; i++ {
		if i > 0 {
			karten.WriteByte(',')
		}
		fmt.Fprintf(&karten, `{"id":"%08x-0000-4000-8000-000000000000","title":"K%d","columnId":"col-backlog"}`, i, i)
	}
	state := `{"version":"2","board":{"id":"b1","sprints":[{"id":"__backlog__","kind":"backlog","cards":[` + karten.String() + `]}]}}`
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" })
	if err := cfg.Set(ctx, KonfigBoardpaarSeite, BoardpaarSeiteInnen, false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	if err := paar.InnenKoppeln(ctx, "b1", "test"); err != nil {
		t.Fatal(err)
	}
	abs, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	req := httptest.NewRequest("GET", SchattenPfad+"b1", nil)
	req.Header.Set("Authorization", "Bearer "+abs)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("über der Grenze: %d %.200s, erwartet 500", rec.Code, rec.Body.String())
	}
	l, err := paar.InnenListe(ctx)
	if err != nil || len(l) != 1 {
		t.Fatal(l, err)
	}
	soll := fmt.Sprintf("zu viele Einträge: %d, Grenze %d", schatten.EingangMaxSchatten+1, schatten.EingangMaxSchatten)
	if !strings.HasPrefix(l[0].LetzterFehlgrund, soll) {
		t.Errorf("Fehlgrund %q, erwartet Anfang %q", l[0].LetzterFehlgrund, soll)
	}
}

// Sitzungs-QA K-2: Der Stand wird auch gemerkt, wenn die Anfrage schon
// abgebrochen ist (Client weg, Frist abgelaufen) — und ein langer Grund
// wird gekürzt.
func TestBoardpaarStand2019_MerkenNachAbbruchUndGekuerzt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var gesehen error = context.Canceled
	merkeStand(ctx, "test", "b1", func(mctx context.Context) error {
		gesehen = mctx.Err()
		return nil
	})
	if gesehen != nil {
		t.Errorf("Merk-Kontext erbt den Abbruch der Anfrage: %v", gesehen)
	}

	_, _, _, db := eingangUmgebung779(t)
	paar := storage.NewBoardpaarRepo(db)
	if err := paar.AussenEingangMerken(context.Background(), "b1", strings.Repeat("ä", 400)); err != nil {
		t.Fatal(err)
	}
	l, _ := paar.AussenListe(context.Background())
	if n := len([]rune(l[0].LetzterFehlgrund)); n != 301 {
		t.Errorf("Fehlgrund hat %d Zeichen, erwartet 300 + „…“", n)
	}
}
