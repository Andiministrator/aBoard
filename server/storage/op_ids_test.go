package storage

// op_ids_test.go — Idempotenz am Ops-Endpunkt (ADR-0130 §5, Karte #607).

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func zaehlerMitIDs(t *testing.T, ctx context.Context, repo *BoardRepo, ids ...string) (map[string]bool, error) {
	t.Helper()
	var gesehen map[string]bool
	schluessel := make([]OpSchluessel, len(ids))
	for i, id := range ids {
		schluessel[i] = OpSchluessel{ID: id, Hash: "h-" + id}
	}
	_, err := repo.ApplyOpsIdempotent(ctx, "b1", schluessel, func(state []byte, bekannt map[string]bool) ([]byte, error) {
		gesehen = bekannt
		neu := 0
		for _, id := range ids {
			if id == "" || !bekannt[id] {
				neu++
			}
		}
		if neu == 0 {
			return nil, nil
		}
		return erhoehe(state)
	}, nil)
	return gesehen, err
}

func TestApplyOpsIdempotent_WiederholungSchreibtNichts(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	legeZaehlerBoardAn(t, ctx, repo)

	if _, err := zaehlerMitIDs(t, ctx, repo, "op-1", "op-2"); err != nil {
		t.Fatal(err)
	}
	vorher, _ := repo.Get(ctx, "b1")

	bekannt, err := zaehlerMitIDs(t, ctx, repo, "op-1", "op-2")
	if !errors.Is(err, ErrAllesBekannt) {
		t.Fatalf("Wiederholung: err=%v, erwartet ErrAllesBekannt", err)
	}
	if !bekannt["op-1"] || !bekannt["op-2"] {
		t.Fatalf("bekannt=%v, erwartet beide", bekannt)
	}
	nachher, _ := repo.Get(ctx, "b1")
	if nachher.UpdatedAt != vorher.UpdatedAt || string(nachher.StateJSON) != string(vorher.StateJSON) {
		t.Fatal("Wiederholung hat geschrieben")
	}

	// Teilweise bekannt: nur op-3 ist neu, und falte sieht op-1 als bekannt.
	bekannt, err = zaehlerMitIDs(t, ctx, repo, "op-1", "op-3")
	if err != nil {
		t.Fatal(err)
	}
	if !bekannt["op-1"] || bekannt["op-3"] {
		t.Fatalf("bekannt=%v, erwartet nur op-1", bekannt)
	}
	// Ohne Schlüssel (leere id): immer neu.
	if _, err := zaehlerMitIDs(t, ctx, repo, ""); err != nil {
		t.Fatal(err)
	}
}

// Wird die Falt-Funktion abgelehnt, sind auch die ids nicht gemerkt — sonst
// verschluckte der Server die korrigierte Wiederholung.
func TestApplyOpsIdempotent_AblehnungMerktNichts(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	legeZaehlerBoardAn(t, ctx, repo)
	_, err := repo.ApplyOpsIdempotent(ctx, "b1", []OpSchluessel{{ID: "op-x", Hash: "h-op-x"}}, func([]byte, map[string]bool) ([]byte, error) {
		return nil, ErrOpsAbgelehnt
	}, nil)
	if !errors.Is(err, ErrOpsAbgelehnt) {
		t.Fatalf("err=%v", err)
	}
	bekannt, err := zaehlerMitIDs(t, ctx, repo, "op-x")
	if err != nil || bekannt["op-x"] {
		t.Fatalf("nach Ablehnung: bekannt=%v err=%v — id darf nicht gemerkt sein", bekannt, err)
	}
}

// K-3: dieselbe id mit anderem Hash ist ein Fehler, nicht eine Wiederholung.
func TestApplyOpsIdempotent_IDMitAnderemInhalt(t *testing.T) {
	ctx, repo := openOpsTestDB(t)
	legeZaehlerBoardAn(t, ctx, repo)
	if _, err := zaehlerMitIDs(t, ctx, repo, "op-1"); err != nil {
		t.Fatal(err)
	}
	vorher, _ := repo.Get(ctx, "b1")
	_, err := repo.ApplyOpsIdempotent(ctx, "b1", []OpSchluessel{{ID: "op-1", Hash: "anders"}},
		func(state []byte, _ map[string]bool) ([]byte, error) { return erhoehe(state) }, nil)
	var w *IDWiederverwendetError
	if !errors.As(err, &w) || w.ID != "op-1" {
		t.Fatalf("err=%v, erwartet IDWiederverwendetError für op-1", err)
	}
	if nachher, _ := repo.Get(ctx, "b1"); nachher.UpdatedAt != vorher.UpdatedAt {
		t.Fatal("trotz wiederverwendeter id geschrieben")
	}
}

// Verify V-4: Eine Zeile ohne Hash (leer) gilt als bekannt, ohne Hash-Vergleich.
func TestApplyOpsIdempotent_AltzeileOhneHash(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	legeZaehlerBoardAn(t, ctx, repo)
	if _, err := db.Exec(`INSERT INTO op_ids (board_id, client_id, hash, ts) VALUES ('b1', 'alt', '', '2026-09-30T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	_, err = repo.ApplyOpsIdempotent(ctx, "b1", []OpSchluessel{{ID: "alt", Hash: "irgendwas"}},
		func(state []byte, bekannt map[string]bool) ([]byte, error) {
			if !bekannt["alt"] {
				t.Fatal("Altzeile ohne Hash nicht als bekannt erkannt")
			}
			return nil, nil
		}, nil)
	if !errors.Is(err, ErrAllesBekannt) {
		t.Fatalf("err=%v, erwartet ErrAllesBekannt", err)
	}
}

func TestOpIDs_FristUndBoardLoeschung(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	legeZaehlerBoardAn(t, ctx, repo)
	if _, err := zaehlerMitIDs(t, ctx, repo, "jung"); err != nil {
		t.Fatal(err)
	}
	alt := time.Now().UTC().AddDate(0, 0, -(OpIDFristTage + 1)).Format(activityTSFormat)
	if _, err := db.Exec(`INSERT INTO op_ids (board_id, client_id, ts) VALUES ('b1', 'alt', ?)`, alt); err != nil {
		t.Fatal(err)
	}
	n, err := NewOpIDRepo(db).EvictExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("EvictExpired: n=%d err=%v, erwartet 1", n, err)
	}
	var rest int
	_ = db.QueryRow(`SELECT COUNT(*) FROM op_ids`).Scan(&rest)
	if rest != 1 {
		t.Fatalf("nach Frist %d übrig, erwartet 1 (jung)", rest)
	}
	if err := repo.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM op_ids`).Scan(&rest)
	if rest != 0 {
		t.Fatalf("nach Board-Löschung %d übrig, erwartet 0", rest)
	}
}
