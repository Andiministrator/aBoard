package storage

// Karte #836 (ADR-0141 Nachtrag): Migration 0044, Schalter „bearbeitbar“ je
// Kopie-Board.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/google/uuid"
)

// (d) Rundweg der Migration: Eine bestehende Kopie und ein Stufe-1-Board aus
// dem Stand 43 bekommen bearbeitbar = 0 (fail-closed, Nachtrag Punkt 2); der
// CHECK lässt nur 0 und 1 zu.
func TestBoardpaar836_MigrationVoreinstellung0(t *testing.T) {
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
		if m.version > 43 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 43"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boards (id,title,version,state_json,updated_at,created_at) VALUES ('k','K','2','{}',1,1), ('s','S','2','{}',1,1)`,
		`INSERT INTO boardpaar_aussen (aussen_board_id,innen_link_basis,seit,von,kopie) VALUES ('k','https://i.example/#board=a','x','t',1)`,
		`INSERT INTO boardpaar_aussen (aussen_board_id,innen_link_basis,seit,von,kopie) VALUES ('s','https://i.example/#board=b','x','t',0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v < 44 {
		t.Fatalf("user_version %d (%v), erwartet ≥ 44", v, err)
	}
	for _, id := range []string{"k", "s"} {
		var b int
		if err := db.QueryRow(`SELECT bearbeitbar FROM boardpaar_aussen WHERE aussen_board_id = ?`, id).Scan(&b); err != nil || b != 0 {
			t.Errorf("%s: bearbeitbar %d (%v), erwartet 0", id, b, err)
		}
	}
	if _, err := db.Exec(`UPDATE boardpaar_aussen SET bearbeitbar = 2 WHERE aussen_board_id = 'k'`); err == nil {
		t.Error("bearbeitbar = 2 angenommen")
	}
	r := NewBoardpaarRepo(db)
	if e, err := r.AussenGekoppelt(ctx, "k"); err != nil || e == nil || !e.Kopie || e.Bearbeitbar {
		t.Errorf("Kopie nach 0044: %+v %v", e, err)
	}
	if ok, err := NewBoardRepo(db).KopieBearbeitbar(ctx, "k"); err != nil || ok {
		t.Errorf("KopieBearbeitbar vor dem Setzen: %v %v", ok, err)
	}
	if err := r.KopieBearbeitbarSetzen(ctx, "k", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := NewBoardRepo(db).KopieBearbeitbar(ctx, "k"); err != nil || !ok {
		t.Errorf("KopieBearbeitbar nach dem Setzen: %v %v", ok, err)
	}
	if err := r.KopieBearbeitbarSetzen(ctx, "s", true); !errors.Is(err, ErrBoardpaarKeineKopie) {
		t.Errorf("Stufe 1: %v, erwartet ErrBoardpaarKeineKopie", err)
	}
	if err := r.KopieBearbeitbarSetzen(ctx, "fehlt", true); !errors.Is(err, ErrBoardpaarNichtGekoppelt) {
		t.Errorf("unbekannt: %v, erwartet ErrBoardpaarNichtGekoppelt", err)
	}
	// Ein Stufe-1-Board gilt nie als bearbeitbar, auch mit 1 in der Spalte.
	if _, err := db.Exec(`UPDATE boardpaar_aussen SET bearbeitbar = 1 WHERE aussen_board_id = 's'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := NewBoardRepo(db).KopieBearbeitbar(ctx, "s"); err != nil || ok {
		t.Errorf("Stufe 1 mit 1: %v %v", ok, err)
	}
}

// Ein Lesefehler ist „nicht bearbeitbar“ mit Fehler (fail-closed): Der
// Ops-Weg schreibt dann nichts.
func TestBoardpaar836_LesefehlerIstNichtBearbeitbar(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if ok, err := NewBoardRepo(db).KopieBearbeitbar(ctx, "k"); err == nil || ok {
		t.Errorf("geschlossene DB: %v %v, erwartet false mit Fehler", ok, err)
	}
}

// Nachtrag Punkt 6: Die Rolle des Board-Paar-Teams folgt je Kopie dem
// wirksamen Rückweg — editor nur bei boardpaar.rueckweg = an UND
// bearbeitbar = 1, sonst viewer. Geprüft an allen drei Setzwegen (Anlegen,
// Hinweg, Admin) und mit zwei Kopien nebeneinander.
func TestBoardpaar836_TeamRolleJeKopie(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	cfg := NewConfigRepo(db, nil, uuid.NewString)
	sr := NewSchattenRepo(db)
	acl := NewAclRepo(db)
	if err := cfg.Set(ctx, KonfigBoardpaarTeamSchluessel, "team@team.example", false, "t"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(ctx, KonfigBoardpaarRueckwegSchluessel, "an", false, "t"); err != nil {
		t.Fatal(err)
	}
	rolle := func(board string) string {
		t.Helper()
		ro, ok, err := acl.GetRole(ctx, board, "team@team.example")
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", board, ok, err)
		}
		return ro
	}
	hinweg := func(board string, wert bool) {
		t.Helper()
		if _, err := sr.ErsetzeKopieFreigabe(ctx, board, &wert, func(state []byte, _ *schatten.Schutz) ([]byte, []string, error) {
			return state, nil, nil
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	frei, zu := true, false
	eins := "0e5d8a2c-1111-5222-8333-44445555a836"
	zwei := "0e5d8a2c-1111-5222-8333-44445555b836"

	// Anlegen: an + 0 → viewer, an + 1 → editor; ohne Feld → 0 → viewer.
	if _, err := r.KopieAnlegenTeamAusKonfigMit(ctx, eins, "K1", "https://i.example/#board=a", "s", &zu); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieAnlegenTeamAusKonfigMit(ctx, zwei, "K2", "https://i.example/#board=b", "s", &frei); err != nil {
		t.Fatal(err)
	}
	if rolle(eins) != RoleViewer || rolle(zwei) != RoleEditor {
		t.Fatalf("nach dem Anlegen: eins %s (erwartet viewer), zwei %s (erwartet editor)", rolle(eins), rolle(zwei))
	}
	ohne := "0e5d8a2c-1111-5222-8333-44445555c836"
	if _, err := r.KopieAnlegenTeamAusKonfigMit(ctx, ohne, "K3", "https://i.example/#board=c", "s", nil); err != nil {
		t.Fatal(err)
	}
	if rolle(ohne) != RoleViewer {
		t.Errorf("ohne Feld: %s, erwartet viewer", rolle(ohne))
	}

	// Hinweg: eins 0 → 1 → 0; zwei bleibt unberührt.
	hinweg(eins, true)
	if rolle(eins) != RoleEditor || rolle(zwei) != RoleEditor {
		t.Errorf("Hinweg 0→1: eins %s, zwei %s", rolle(eins), rolle(zwei))
	}
	hinweg(eins, false)
	if rolle(eins) != RoleViewer || rolle(zwei) != RoleEditor {
		t.Errorf("Hinweg 1→0: eins %s, zwei %s (zwei muss editor bleiben)", rolle(eins), rolle(zwei))
	}

	// Stand nach der Migration: Team ist noch editor, bearbeitbar 0. Der
	// erste Hinweg mit dem Feld (auch ohne Wechsel) macht es zum Leser.
	if _, err := db.Exec(`UPDATE acl SET role = ? WHERE user_email = 'team@team.example' AND board_id = ?`, RoleEditor, eins); err != nil {
		t.Fatal(err)
	}
	hinweg(eins, false)
	if rolle(eins) != RoleViewer {
		t.Errorf("Hinweg 0→0 nach der Migration: %s, erwartet viewer", rolle(eins))
	}

	// Admin-Weg: zwei 1 → 0 → 1; eins bleibt unberührt.
	if err := r.KopieBearbeitbarSetzen(ctx, zwei, false); err != nil {
		t.Fatal(err)
	}
	if rolle(zwei) != RoleViewer || rolle(eins) != RoleViewer {
		t.Errorf("Admin 1→0: zwei %s, eins %s", rolle(zwei), rolle(eins))
	}
	if err := r.KopieBearbeitbarSetzen(ctx, zwei, true); err != nil {
		t.Fatal(err)
	}
	if rolle(zwei) != RoleEditor || rolle(eins) != RoleViewer {
		t.Errorf("Admin 0→1: zwei %s, eins %s", rolle(zwei), rolle(eins))
	}

	// Server-Schalter aus: Freigabe ändert die Rolle nicht (Team bleibt Leser).
	if err := cfg.Set(ctx, KonfigBoardpaarRueckwegSchluessel, "aus", false, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE acl SET role = ? WHERE user_email = 'team@team.example' AND board_id = ?`, RoleViewer, zwei); err != nil {
		t.Fatal(err)
	}
	if err := r.KopieBearbeitbarSetzen(ctx, eins, true); err != nil {
		t.Fatal(err)
	}
	if rolle(eins) != RoleViewer {
		t.Errorf("aus + 1: %s, erwartet viewer", rolle(eins))
	}

	// Angleichen über alle Kopien (Schalter-Folge aus → an): nur die
	// freigegebenen werden editor (eins, zwei), ohne bleibt viewer.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := BoardpaarTeamRolleTx(ctx, conn, "", []string{"team@team.example"}, true); err != nil {
		t.Fatal(err)
	}
	if rolle(eins) != RoleEditor || rolle(zwei) != RoleEditor || rolle(ohne) != RoleViewer {
		t.Errorf("Folge an: eins %s, zwei %s, ohne %s", rolle(eins), rolle(zwei), rolle(ohne))
	}
	// Und eine Kopie, die vorher (etwa von Hand) editor war, aber 0 hat, wird
	// beim Angleichen Leser.
	if _, err := db.Exec(`UPDATE acl SET role = ? WHERE user_email = 'team@team.example' AND board_id = ?`, RoleEditor, ohne); err != nil {
		t.Fatal(err)
	}
	if _, err := BoardpaarTeamRolleTx(ctx, conn, "", []string{"team@team.example"}, true); err != nil {
		t.Fatal(err)
	}
	if rolle(ohne) != RoleViewer {
		t.Errorf("Folge an, Kopie ohne Freigabe: %s, erwartet viewer", rolle(ohne))
	}
}
