package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Karte #1197, ADR-0156: boards.awaiting_json ist eine von Triggern gepflegte
// Kopie von state_json.board.awaitingUser (nur Objekte), und List liefert sie.

func awZustand(id, aw string) []byte {
	return []byte(`{"version":"2","board":{"id":"` + id + `","title":"T","awaitingUser":` + aw + `}}`)
}

func awSpalte(t *testing.T, db *sql.DB, id string) (string, bool) {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT awaiting_json FROM boards WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String, v.Valid
}

func awOeffne(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func awGleich(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("Spalte kein JSON: %q", got)
	}
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("awaiting_json = %s, erwartet %s", gb, wb)
	}
}

func TestAwaitingSpalte_InsertUpdateUndRoheWege(t *testing.T) {
	ctx := context.Background()
	db := awOeffne(t)
	r := NewBoardRepo(db)
	eintrag := `{"Andi":{"question":"Frage?","since":"2026-11-23T10:00:00Z"}}`

	b, err := r.Insert(ctx, "b1", "T", "2", awZustand("b1", eintrag))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := awSpalte(t, db, "b1"); !ok {
		t.Fatal("nach Insert: Spalte NULL")
	} else {
		awGleich(t, v, eintrag)
	}

	// Update auf null leert die Spalte.
	if b, err = r.Update(ctx, "b1", "T", "2", awZustand("b1", "null"), b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, ok := awSpalte(t, db, "b1"); ok {
		t.Error("nach Update auf null: Spalte nicht NULL")
	}

	// Ein Schreibweg an BoardRepo vorbei (wie transfer.go, schatten.go) wird
	// ebenfalls erfasst — das ist der Grund für den Trigger.
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, awZustand("b1", eintrag), "b1"); err != nil {
		t.Fatal(err)
	}
	if v, ok := awSpalte(t, db, "b1"); !ok {
		t.Error("nach rohem UPDATE: Spalte NULL")
	} else {
		awGleich(t, v, eintrag)
	}

	// Kein Objekt (alte Fehlform: Zeichenkette, Liste) → NULL.
	for _, aw := range []string{`"text"`, `[1,2]`, `42`} {
		if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, awZustand("b1", aw), "b1"); err != nil {
			t.Fatal(err)
		}
		if v, ok := awSpalte(t, db, "b1"); ok {
			t.Errorf("awaitingUser %s: Spalte %q, erwartet NULL", aw, v)
		}
	}
}

// Ein unlesbarer Stand darf das Schreiben nie abbrechen (Trigger fängt ihn ab).
func TestAwaitingSpalte_KaputterStandBrichtNichtAb(t *testing.T) {
	db := awOeffne(t)
	ts := int64(1)
	for _, s := range []any{[]byte("kein json"), "auch {kein", []byte{0xff, 0xfe}} {
		if _, err := db.Exec(`INSERT OR REPLACE INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('k', 'T', '2', ?, ?, ?)`, s, ts, ts); err != nil {
			t.Fatalf("Insert mit %q bricht ab: %v", s, err)
		}
		if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'k'`, s); err != nil {
			t.Fatalf("Update mit %q bricht ab: %v", s, err)
		}
		if v, ok := awSpalte(t, db, "k"); ok {
			t.Errorf("%q: Spalte %q, erwartet NULL", s, v)
		}
		ts++
	}
}

func TestAwaitingSpalte_ListLiefertFeld(t *testing.T) {
	ctx := context.Background()
	db := awOeffne(t)
	r := NewBoardRepo(db)
	eintrag := `{"Andi":{"question":"Q","since":"2026-11-23T10:00:00Z"}}`
	if _, err := r.Insert(ctx, "mit", "T", "2", awZustand("mit", eintrag)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Insert(ctx, "ohne", "T", "2", awZustand("ohne", "null")); err != nil {
		t.Fatal(err)
	}
	// Kritik 24.11. A-5: leeres Objekt = niemand wartet, wird nicht ausgeliefert.
	if _, err := r.Insert(ctx, "leer", "T", "2", awZustand("leer", "{}")); err != nil {
		t.Fatal(err)
	}
	l, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gesehen := 0
	for _, s := range l {
		switch s.ID {
		case "mit":
			gesehen++
			awGleich(t, string(s.Awaiting), eintrag)
		case "ohne", "leer":
			gesehen++
			if s.Awaiting != nil {
				t.Errorf("ohne: Awaiting %q, erwartet nil", s.Awaiting)
			}
		}
	}
	if gesehen != 3 {
		t.Fatalf("List: %d von 3 Boards", gesehen)
	}
}

// Migration 0048 füllt den Bestand nach, und beide Trigger existieren nach
// allen Migrationen (wer boards neu aufbaut, verliert sie sonst still).
func TestMigration0048_RueckfuellungUndTrigger(t *testing.T) {
	voll := awOeffne(t)
	for _, name := range []string{"boards_awaiting_nach_insert", "boards_awaiting_nach_update"} {
		var n int
		if err := voll.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("Trigger %s fehlt nach allen Migrationen", name)
		}
	}
	// Zustand vor 0048: Schema bis 0047, Bestand einsetzen.
	db := schemaBis(t, 47)
	eintrag := `{"Andi":{"question":"Q","since":"2026-11-23T10:00:00Z"}}`
	ins := func(id string, state any) {
		mustExec(t, db, `INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, 'T', '2', ?, 1, 1)`, id, state)
	}
	ins("m", awZustand("m", eintrag)) // als BLOB wie im Betrieb
	ins("o", awZustand("o", "null"))
	ins("x", []byte("kaputt"))
	if err := RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if v, ok := awSpalte(t, db, "m"); !ok {
		t.Error("m: nicht nachgefüllt")
	} else {
		awGleich(t, v, eintrag)
	}
	for _, id := range []string{"o", "x"} {
		if v, ok := awSpalte(t, db, id); ok {
			t.Errorf("%s: Spalte %q, erwartet NULL", id, v)
		}
	}
}
