package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func openOpsTestDB(t *testing.T) (context.Context, *BoardRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, NewBoardRepo(db)
}

type zaehlerBlob struct {
	Version string `json:"version"`
	Board   struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Zaehler int    `json:"zaehler"`
	} `json:"board"`
}

func legeZaehlerBoardAn(t *testing.T, ctx context.Context, repo *BoardRepo) *Board {
	t.Helper()
	b, err := repo.Insert(ctx, "b1", "Zähler", "2", []byte(`{"version":"2","board":{"id":"b1","title":"Zähler","zaehler":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func erhoehe(state []byte) ([]byte, error) {
	var z zaehlerBlob
	if err := json.Unmarshal(state, &z); err != nil {
		return nil, err
	}
	z.Board.Zaehler++
	return json.Marshal(z)
}

func TestApplyOps_SchreibtUndUebernimmtTitel(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	alt := legeZaehlerBoardAn(t, ctx, repo)
	b, err := repo.ApplyOps(ctx, "b1", func(state []byte) ([]byte, error) {
		return []byte(`{"version":"2","board":{"id":"b1","title":"Neuer Titel","zaehler":7}}`), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.UpdatedAt <= alt.UpdatedAt || b.Title != "Neuer Titel" {
		t.Fatalf("Rückgabe: updated %d→%d, Titel %q", alt.UpdatedAt, b.UpdatedAt, b.Title)
	}
	gelesen, _ := repo.Get(ctx, "b1")
	if gelesen.Title != "Neuer Titel" || gelesen.UpdatedAt != b.UpdatedAt || string(gelesen.StateJSON) != string(b.StateJSON) {
		t.Fatalf("gespeichert: %+v", gelesen)
	}
}

// TestApplyOps_FehlerRolltZurueck: Ablehnung der Falt-Funktion und eine
// verletzte Hülle schreiben nichts, auch nicht updated_at.
func TestApplyOps_FehlerRolltZurueck(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	alt := legeZaehlerBoardAn(t, ctx, repo)
	faelle := map[string]func([]byte) ([]byte, error){
		"abgelehnt":  func([]byte) ([]byte, error) { return nil, fmt.Errorf("%w: wirkungslos", ErrOpsAbgelehnt) },
		"fremde id":  func([]byte) ([]byte, error) { return []byte(`{"version":"2","board":{"id":"b2","title":"x"}}`), nil },
		"titel leer": func([]byte) ([]byte, error) { return []byte(`{"version":"2","board":{"id":"b1","title":""}}`), nil },
		"kein json":  func([]byte) ([]byte, error) { return []byte(`{`), nil },
	}
	for name, fn := range faelle {
		_, err := repo.ApplyOps(ctx, "b1", fn, nil)
		if err == nil {
			t.Errorf("%s: kein Fehler", name)
		}
		if name == "abgelehnt" && !errors.Is(err, ErrOpsAbgelehnt) {
			t.Errorf("abgelehnt: ErrOpsAbgelehnt nicht durchgereicht: %v", err)
		}
		jetzt, _ := repo.Get(ctx, "b1")
		if jetzt.UpdatedAt != alt.UpdatedAt || string(jetzt.StateJSON) != string(alt.StateJSON) {
			t.Fatalf("%s: Board trotz Fehler verändert", name)
		}
	}
	// Die Verbindung ist nach den Rollbacks wieder frei: ein normaler Lauf geht.
	if _, err := repo.ApplyOps(ctx, "b1", erhoehe, nil); err != nil {
		t.Fatalf("nach Rollbacks: %v", err)
	}
}

func TestApplyOps_BoardFehlt(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	aufgerufen := false
	_, err := repo.ApplyOps(ctx, "gibtsnicht", func(s []byte) ([]byte, error) { aufgerufen = true; return s, nil }, nil)
	if !errors.Is(err, ErrBoardNotFound) || aufgerufen {
		t.Fatalf("err=%v aufgerufen=%v", err, aufgerufen)
	}
}

// TestApplyOps_NebenlaeufigKeinVerlorenerSchreibvorgang: 16 Schreiber
// erhöhen gleichzeitig je 5-mal einen Zähler im Blob. Mit BEGIN IMMEDIATE
// sieht jeder den Stand seines Vorgängers → Endstand genau 80, kein Fehler.
// Gegenprobe (gemessen): mit plain BEGIN lesen zwei Schreiber denselben
// Stand; der zweite scheitert beim Hochstufen der Sperre mit SQLITE_BUSY
// (die busy-Wartezeit greift dort nicht), der Test wird rot.
func TestApplyOps_NebenlaeufigKeinVerlorenerSchreibvorgang(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	legeZaehlerBoardAn(t, ctx, repo)
	const schreiber, runden = 16, 5
	var wg sync.WaitGroup
	fehler := make(chan error, schreiber*runden)
	for i := 0; i < schreiber; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < runden; k++ {
				if _, err := repo.ApplyOps(ctx, "b1", erhoehe, nil); err != nil {
					fehler <- err
				}
			}
		}()
	}
	wg.Wait()
	close(fehler)
	for err := range fehler {
		t.Fatal(err)
	}
	b, _ := repo.Get(ctx, "b1")
	var z zaehlerBlob
	_ = json.Unmarshal(b.StateJSON, &z)
	if z.Board.Zaehler != schreiber*runden {
		t.Fatalf("Zähler %d, erwartet %d — Schreibvorgänge verloren", z.Board.Zaehler, schreiber*runden)
	}
}
