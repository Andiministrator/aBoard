package storage

// Verify zur Sitzungs-QA 20.10. (F-2313-Nacharbeit, Klasse §50 in
// knowledge/topics/nebenlaeufigkeit-und-lesefenster.md): Delete zählt offene
// Journal-Einträge. Zählen und dann Löschen in einer DEFERRED-Transaktion
// scheitert in WAL sofort mit „database is locked“, sobald dazwischen ein
// anderer Schreiber committet — busy_timeout greift nicht. Gelöscht wird
// deshalb in EINEM Statement. Prüfstein: parallele Löschungen neben laufenden
// Schreibern, jede Löschung gelingt, kein dritter Ausgang.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestBoardsDelete_ParallelNebenSchreibern(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	const n = 30
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("b%02d", i)
		if _, err := repo.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Insert(ctx, "laerm", "L", "2", []byte(`{"version":"2","board":{"id":"laerm","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}

	stopp := make(chan struct{})
	var schreiber sync.WaitGroup
	for w := 0; w < 4; w++ {
		schreiber.Add(1)
		go func() {
			defer schreiber.Done()
			for k := 0; ; k++ {
				select {
				case <-stopp:
					return
				default:
				}
				_, _ = db.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = 'laerm'`, k)
			}
		}()
	}

	var wg sync.WaitGroup
	fehler := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := repo.Delete(ctx, id); err != nil {
				fehler <- fmt.Errorf("%s: %w", id, err)
			}
		}(fmt.Sprintf("b%02d", i))
	}
	wg.Wait()
	close(stopp)
	schreiber.Wait()
	close(fehler)
	for err := range fehler {
		t.Errorf("Delete neben Schreibern gescheitert: %v", err)
	}
	// Gelöscht heißt weg — ein Delete, das ohne Fehler nichts löscht, fiele sonst durch.
	rest, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].ID != "laerm" {
		t.Errorf("nach den Löschungen noch %d Boards da, erwartet nur laerm", len(rest))
	}
}

// Dasselbe mit offenem Journal: jede parallele Löschung meldet die offenen
// Änderungen (409), keine 404, keine „database is locked“, und das Board bleibt.
func TestBoardsDelete_ParallelMitOffenemJournal(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	if _, err := repo.Insert(ctx, "kopie", "K", "2", []byte(`{"version":"2","board":{"id":"kopie","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO boardpaar_journal (board_id, objekt, kennung, art, zeit)
		VALUES ('kopie', 'karte', 'k1', 'geaendert', '2026-10-20T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	falsch := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.Delete(ctx, "kopie")
			var offen *OffeneAenderungenError
			if !errors.As(err, &offen) || offen.Anzahl != 1 {
				falsch <- err
			}
		}()
	}
	wg.Wait()
	close(falsch)
	for err := range falsch {
		t.Errorf("parallele Löschung mit offenem Journal: %v, erwartet OffeneAenderungenError{1}", err)
	}
	if _, err := repo.Get(ctx, "kopie"); err != nil {
		t.Errorf("Board trotz offenem Journal weg: %v", err)
	}
}
