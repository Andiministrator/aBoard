package main

// Kritiker #779, K-4: Der Ausgang innen hält die Grenze des Eingangs außen
// (5000 Schatten je Board) selbst ein — ganz oder Fehler, nie eine gekappte
// Liste (außen würden die übrigen sonst gelöscht).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchattenAusgang779_GrenzeWieEingang(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	paar := storage.NewBoardpaarRepo(db)
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "test"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{schatten.EingangMaxSchatten, schatten.EingangMaxSchatten + 1} {
		karten := make([]string, n)
		for i := range karten {
			karten[i] = fmt.Sprintf(`{"id":"k%d","displayId":%d,"title":"Karte"}`, i, i+1)
		}
		id := fmt.Sprintf("b%d", n)
		state := `{"version":"2","board":{"id":"` + id + `","sprints":[{"id":"s","cards":[` + strings.Join(karten, ",") + `]}]}}`
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(state)); err != nil {
			t.Fatal(err)
		}
		if err := paar.InnenKoppeln(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	abs, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	agenten := storage.NewAgentTokenRepo(db)
	srv := httptest.NewServer(hauptHandler(mux, func(h http.Handler) http.Handler { return h }, api.NewAgentZugang(agenten), true))
	t.Cleanup(srv.Close)
	if s := agentAnfrage(t, srv, "GET", fmt.Sprintf("/agent/schatten/b%d", schatten.EingangMaxSchatten), abs, "", nil); s != 200 {
		t.Errorf("%d Schatten: %d, erwartet 200", schatten.EingangMaxSchatten, s)
	}
	if s := agentAnfrage(t, srv, "GET", fmt.Sprintf("/agent/schatten/b%d", schatten.EingangMaxSchatten+1), abs, "", nil); s != 500 {
		t.Errorf("%d Schatten: %d, erwartet 500", schatten.EingangMaxSchatten+1, s)
	}
}
