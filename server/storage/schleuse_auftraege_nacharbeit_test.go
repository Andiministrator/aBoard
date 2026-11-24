package storage

// schleuse_auftraege_nacharbeit_test.go — Nacharbeit zu den Kritiker-Befunden
// über S2 (Karte #685): KS-5 Obergrenze in einem Schreibvorgang, KS-6
// Board-Löschung räumt Aufträge ab, KS-11 freigegebene zählen mit.

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func auftragsBoard(t *testing.T, r *SchleuseAuftragRepo, id string) {
	t.Helper()
	if _, err := NewBoardRepo(r.db).Insert(context.Background(), id, "T", "1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

// KS-5/KS-11: AnlegenGeprueft lässt höchstens hoechst offene Aufträge zu —
// freigegebene zählen mit, gesendete nicht. Gesperrtes oder gelöschtes
// Board: keine Zeile.
func TestSchleuseAuftrag_AnlegenGeprueft(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	ctx := context.Background()
	auftragsBoard(t, r, "b1")
	a, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-a", "eins"), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'freigegeben', freigabe_sha256 = text_sha256 WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.ZaehleEntwuerfe(ctx, "u-a"); n != 1 {
		t.Errorf("freigegeben zählt nicht mit: %d", n)
	}
	if _, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-a", "zwei"), nil, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-a", "drei"), nil, 2); !errors.Is(err, ErrSchleuseZuVieleEntwuerfe) {
		t.Errorf("dritter Auftrag: %v", err)
	}
	// Andere Person: eigene Grenze.
	if _, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-b", "b"), nil, 2); err != nil {
		t.Errorf("andere Person: %v", err)
	}
	// Gesendet zählt nicht mehr.
	if _, err := db.Exec(`UPDATE schleuse_auftraege SET zustand = 'gesendet' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-a", "drei"), nil, 2); err != nil {
		t.Errorf("nach Senden: %v", err)
	}
	if err := SetzeSchleuseBoardSperre(ctx, db, "b1", true, "u-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AnlegenGeprueft(ctx, neuerAuftrag("u-c", "x"), nil, 2); !errors.Is(err, ErrSchleuseBoardGesperrt) {
		t.Errorf("gesperrtes Board: %v", err)
	}
	b := neuerAuftrag("u-c", "x")
	b.BoardID = "gibt-es-nicht"
	if _, err := r.AnlegenGeprueft(ctx, b, nil, 2); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("fehlendes Board: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM schleuse_auftraege WHERE user_id = 'u-c'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d Zeilen für u-c", n)
	}
}

// KS-5: Parallele Anlagen sehen nie gemeinsam den letzten freien Platz.
func TestSchleuseAuftrag_AnlegenGeprueftParallel(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	auftragsBoard(t, r, "b1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, voll, sonst := 0, 0, 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.AnlegenGeprueft(context.Background(), neuerAuftrag("u-a", "x"), nil, 5)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrSchleuseZuVieleEntwuerfe):
				voll++
			default:
				sonst++
				t.Errorf("unerwartet: %v", err)
			}
		}()
	}
	wg.Wait()
	if n, _ := r.ZaehleEntwuerfe(context.Background(), "u-a"); n != 5 || ok != 5 || voll != 25 || sonst != 0 {
		t.Errorf("offen=%d ok=%d voll=%d sonst=%d", n, ok, voll, sonst)
	}
}

// KS-6: Die Board-Löschung nimmt die Aufträge des Boards mit, andere bleiben.
func TestBoardDelete_RaeumtSchleuseAuftraegeAb(t *testing.T) {
	db := loeschDB(t)
	r := NewSchleuseAuftragRepo(db, schleuseBox(t))
	ctx := context.Background()
	auftragsBoard(t, r, "b1")
	auftragsBoard(t, r, "b2")
	weg, _ := r.AnlegenGeprueft(ctx, neuerAuftrag("u-a", "weg"), map[string]string{"[PERSON_1]": "Anna"}, 20)
	b := neuerAuftrag("u-a", "bleibt")
	b.BoardID = "b2"
	bleibt, _ := r.AnlegenGeprueft(ctx, b, nil, 20)
	if err := NewBoardRepo(db).Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, weg.ID, "u-a"); !errors.Is(err, ErrSchleuseAuftragNichtGefunden) {
		t.Errorf("Auftrag des gelöschten Boards noch da: %v", err)
	}
	if _, err := r.Get(ctx, bleibt.ID, "u-a"); err != nil {
		t.Errorf("Auftrag eines anderen Boards weg: %v", err)
	}
	if err := NewBoardRepo(db).Delete(ctx, "b1"); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("zweite Löschung: %v", err)
	}
}
