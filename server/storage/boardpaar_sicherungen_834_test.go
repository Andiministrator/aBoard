package storage

// Karte #834 (ADR-0142): Migration 0043, Typ abb (Sicherungen lesen).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Rundweg der Migration: Bestand aus 0040 (alle drei Typen, Erneuerungs-
// Spalten, Anlege-Recht) übersteht 0043 unverändert; abb ist erlaubt, das
// Anlege-Recht bleibt bei abe, der Teilindex lebt.
func TestBoardpaar834_MigrationRundweg(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsnFuer(filepath.Join(t.TempDir(), "a.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	alle, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range alle {
		if m.version > 42 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 42"); err != nil {
		t.Fatal(err)
	}
	// Vor 0043 kennt der CHECK abb nicht (sonst prüfte der Rundweg nichts).
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t0','abb','vorher','h0','x')`); err == nil {
		t.Fatal("abb schon vor 0043 angenommen — Ausgangsstand falsch")
	}
	for _, q := range []string{
		`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at,created_by,last_used_at,gilt_bis,nachfolger_id,nachfolger_enc,erneuert_aus,erneuert_von,darf_anlegen)
		 VALUES ('t1','abe','kopien','h1','2026-01-01','andi','2026-01-02','2026-01-03','t2','enc','t0','192.0.2.1',1)`,
		`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at,revoked_at) VALUES ('t2','abs','push','h2','2026-01-01','2026-01-05')`,
		`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t3','abr','rueck','h3','2026-01-01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v < 43 {
		t.Fatalf("user_version %d (%v), erwartet ≥ 43", v, err)
	}
	var typ, name, enc, aus, von, gilt string
	var darf int
	if err := db.QueryRow(`SELECT typ,name,nachfolger_enc,erneuert_aus,erneuert_von,gilt_bis,darf_anlegen FROM boardpaar_tokens WHERE id='t1'`).
		Scan(&typ, &name, &enc, &aus, &von, &gilt, &darf); err != nil {
		t.Fatal(err)
	}
	if typ != "abe" || name != "kopien" || enc != "enc" || aus != "t0" || von != "192.0.2.1" || gilt != "2026-01-03" || darf != 1 {
		t.Errorf("abe nach 0043: %s %s %s %s %s %s darf=%d", typ, name, enc, aus, von, gilt, darf)
	}
	var widerrufen string
	if err := db.QueryRow(`SELECT revoked_at FROM boardpaar_tokens WHERE id='t2'`).Scan(&widerrufen); err != nil || widerrufen != "2026-01-05" {
		t.Errorf("abs nach 0043: %q (%v)", widerrufen, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM boardpaar_tokens`).Scan(&n); err != nil || n != 3 {
		t.Errorf("Bestand nach 0043: %d Zeilen (%v), erwartet 3", n, err)
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t4','abb','abholung','h4','x')`); err != nil {
		t.Errorf("abb abgelehnt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at,darf_anlegen) VALUES ('t5','abb','x','h5','x',1)`); err == nil {
		t.Error("abb mit darf_anlegen angenommen")
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t6','abx','x','h6','x')`); err == nil {
		t.Error("unbekannter Typ angenommen")
	}
	if _, err := db.Exec(`INSERT INTO boardpaar_tokens (id,typ,name,token_hash,created_at) VALUES ('t7','abb','abholung','h7','x')`); err == nil {
		t.Error("doppelter aktiver Name angenommen — Teilindex fehlt")
	}
}

func TestBoardpaar834_TypAbb(t *testing.T) {
	ctx := context.Background()
	r := NewBoardpaarRepo(kopieTestDB(t))
	abb, z, err := r.TokenAnlegen(ctx, BoardpaarTypSicherungen, "abholung", "admin")
	if err != nil || !strings.HasPrefix(abb, SicherungenPraefix) || z.Typ != "abb" {
		t.Fatalf("abb: %q %+v %v", abb, z, err)
	}
	if BoardpaarTypAusPraefix(abb) != BoardpaarTypSicherungen {
		t.Error("BoardpaarTypAusPraefix(abb_…)")
	}
	if _, _, err := r.TokenAnlegenMit(ctx, BoardpaarTypSicherungen, "mit-recht", "admin", true); !errors.Is(err, ErrBoardpaarAnlegeRecht) {
		t.Errorf("abb mit Anlege-Recht: %v", err)
	}
	// Ein abb_ öffnet keinen anderen Typ und umgekehrt.
	if _, err := r.TokenPruefe(ctx, BoardpaarTypEingang, abb); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("abb als abe geprüft: %v", err)
	}
	if g, err := r.TokenPruefe(ctx, BoardpaarTypSicherungen, abb); err != nil || g.Name != "abholung" {
		t.Errorf("TokenPruefe: %+v %v", g, err)
	}
	e, err := r.TokenErneuern(ctx, BoardpaarTypSicherungen, abb, "192.0.2.7")
	if err != nil || !strings.HasPrefix(e.Token, SicherungenPraefix) {
		t.Fatalf("abb erneuern: %+v %v", e, err)
	}
	akt, err := r.TokenAktivNachName(ctx, BoardpaarTypSicherungen, "abholung")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.TokenWiderrufen(ctx, akt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TokenPruefe(ctx, BoardpaarTypSicherungen, e.Token); !errors.Is(err, ErrBoardpaarTokenUnbekannt) {
		t.Errorf("widerrufenes abb gilt noch: %v", err)
	}
}
