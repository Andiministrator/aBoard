package storage

// Karte #783 (ADR-0140 §8, Migration 0038): Speicher der Vorschläge und die
// Rolle intern in agent_tokens.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func vorschlagTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"b1", "b2"} {
		if _, err := NewBoardRepo(db).Insert(context.Background(), id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","title":"B"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func neuerVorschlag(id, board, token string) *Vorschlag {
	return &Vorschlag{ID: id, BoardID: board, TokenID: token, TokenName: "assistent", Art: VorschlagKommentar, KarteID: "k1",
		Nutzlast: `{"text":"x"}`, Hash: "h-" + id}
}

func TestVorschlaege783_GrenzeAblaufLoeschen(t *testing.T) {
	ctx := context.Background()
	db := vorschlagTestDB(t)
	repo := NewVorschlagRepo(db)
	alt := timeNow
	t.Cleanup(func() { timeNow = alt })
	jetzt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return jetzt }

	for i := 0; i < VorschlaegeJeTokenMax; i++ {
		if _, err := repo.Anlegen(ctx, neuerVorschlag("v"+string(rune('a'+i)), "b1", "t1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Anlegen(ctx, neuerVorschlag("zuviel", "b1", "t1")); !errors.Is(err, ErrVorschlagGrenze) {
		t.Fatalf("11. Vorschlag: %v", err)
	}
	// Die Grenze gilt je Token, nicht je Board.
	if _, err := repo.Anlegen(ctx, neuerVorschlag("anderes-token", "b1", "t2")); err != nil {
		t.Fatalf("anderes Token: %v", err)
	}
	if l, err := repo.Offene(ctx, "b1"); err != nil || len(l) != VorschlaegeJeTokenMax+1 {
		t.Fatalf("Offene b1: %d %v", len(l), err)
	}
	if l, _ := repo.Offene(ctx, "b2"); len(l) != 0 {
		t.Errorf("Offene b2: %d", len(l))
	}
	if _, err := repo.Hole(ctx, "b2", "va"); !errors.Is(err, ErrVorschlagUnbekannt) {
		t.Errorf("Hole über fremdes Board: %v", err)
	}

	// 24 h später: abgelaufen — nicht mehr offen, zählt nicht mehr, wird gelöscht.
	timeNow = func() time.Time { return jetzt.Add(VorschlagGueltig) }
	if l, _ := repo.Offene(ctx, "b1"); len(l) != 0 {
		t.Errorf("nach 24 h noch offen: %d", len(l))
	}
	v, err := repo.Hole(ctx, "b1", "va")
	if err != nil || !v.Abgelaufen() {
		t.Fatalf("Hole abgelaufen: %v %v", v, err)
	}
	if _, err := repo.Anlegen(ctx, neuerVorschlag("neu", "b1", "t1")); err != nil {
		t.Fatalf("Abgelaufene dürfen nicht mitzählen, Anlegen scheiterte: %v", err)
	}
	weg, err := repo.AbgelaufeneLoeschen(ctx)
	if err != nil || len(weg) != VorschlaegeJeTokenMax+1 {
		t.Fatalf("AbgelaufeneLoeschen: %d %v", len(weg), err)
	}
	if weg[0].Nutzlast != "" {
		t.Errorf("AbgelaufeneLoeschen liefert die Nutzlast: %q", weg[0].Nutzlast)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vorschlaege`).Scan(&n); err != nil || n != 1 {
		t.Errorf("nach dem Aufräumen: %d Zeilen (erwartet 1, der neue) %v", n, err)
	}

	// Entfernen: genau einmal; Wiederherstellen bringt ihn unverändert zurück.
	neu, _ := repo.Hole(ctx, "b1", "neu")
	if err := repo.Entfernen(ctx, "b1", "neu"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Entfernen(ctx, "b1", "neu"); !errors.Is(err, ErrVorschlagUnbekannt) {
		t.Errorf("zweites Entfernen: %v", err)
	}
	if err := repo.Wiederherstellen(ctx, neu); err != nil {
		t.Fatal(err)
	}
	if zurueck, err := repo.Hole(ctx, "b1", "neu"); err != nil || *zurueck != *neu {
		t.Errorf("Wiederherstellen: %+v %v", zurueck, err)
	}

	// Board gelöscht → seine Vorschläge mit.
	if err := NewBoardRepo(db).Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vorschlaege`).Scan(&n); err != nil || n != 0 {
		t.Errorf("nach Board-Löschung: %d %v", n, err)
	}
}

// TestVorschlaege783_RolleInternNurProjektToken: Anlegen und CHECK der
// Migration 0038 lassen intern nur als Projekt-Token für ein Board zu.
func TestVorschlaege783_RolleInternNurProjektToken(t *testing.T) {
	ctx := context.Background()
	db := vorschlagTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := NewAclRepo(db).Set(ctx, "b1", "anna@firma.de", RoleEditor); err != nil {
		t.Fatal(err)
	}
	repo := NewAgentTokenRepo(db)
	for name, a := range map[string]AgentTokenAnlage{
		"mit-person":   {Name: "g1", BoardID: "b1", Role: AgentRolleIntern, UserID: "u1"},
		"delegierend":  {Name: "g2", Role: AgentRolleIntern, Delegierend: true},
		"mehrere":      {Name: "g3", Role: AgentRolleIntern, UserID: "u1", BoardIDs: []string{"b1"}},
		"ohne-board":   {Name: "g4", Role: AgentRolleIntern},
		"service":      {Name: "g5", BoardID: ServiceBoardID, Role: AgentRolleIntern},
		"selbstbedien": {Name: "g6", BoardID: "b1", Role: AgentRolleIntern, UserID: "u1", Selbstbedienung: true},
	} {
		if _, _, err := repo.Anlegen(ctx, a); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("%s: %v (erwartet ErrAgentEingabe)", name, err)
		}
	}
	if _, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "assistent", BoardID: "b1", Role: AgentRolleIntern, CreatedBy: "cli"}); err != nil || tok.Role != AgentRolleIntern {
		t.Fatalf("Projekt-Token intern: %v %v", tok, err)
	}
	// Der CHECK hält dasselbe in der Tabelle, auch an Anlegen vorbei.
	_, err := db.Exec(`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, user_id)
		VALUES ('x', 'x', 'b1', 'intern', 'hash-x', '2026-01-01', 'u1')`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("CHECK intern mit Person: %v", err)
	}
}

// tabellenText: alle Zeilen einer Abfrage als Text (zum Vergleich vorher/nachher).
func tabellenText(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	spalten, _ := rows.Columns()
	var sb strings.Builder
	for rows.Next() {
		werte := make([]any, len(spalten))
		ziele := make([]any, len(spalten))
		for i := range werte {
			ziele[i] = &werte[i]
		}
		if err := rows.Scan(ziele...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%v\n", werte)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// TestVorschlaege783_Migration38MitBestand: 0038 baut agent_tokens neu auf —
// auf einer Datenbank mit Schema 37 und Tokens aller Formen bleibt jede Zeile,
// jede Board-Liste, jeder Index und jeder Trigger gleich, und die Trigger
// feuern danach weiter (Kritik K-B6; Vorlage von Kritiker B).
func TestVorschlaege783_Migration38MitBestand(t *testing.T) {
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
		if m.version > 37 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 37"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b1", "b2"} {
		if _, err := NewBoardRepo(db).Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","title":"B"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	ins := `INSERT INTO agent_tokens (id,name,board_id,role,token_hash,created_at,created_by,revoked_at,user_id,expires_at,kennung,delegierend,mehrere)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, z := range [][]any{
		{"t1", "editor1", "b1", "editor", "h1", "2026-01-01", "andi", nil, nil, nil, nil, 0, 0},
		{"t2", "viewer1", "b2", "viewer", "h2", "2026-01-02", "andi", "2026-02-01", nil, nil, "k2", 0, 0},
		{"t3", "vorschlag1", "b1", "vorschlag", "h3", "2026-01-03", "x", nil, nil, "2027-01-01", "k3", 0, 0},
		{"t4", "deleg", nil, "editor", "h4", "2026-01-04", "x", nil, nil, nil, nil, 1, 0},
		{"t5", "person", nil, "editor", "h5", "2026-01-05", "x", nil, "u1", nil, "k5", 0, 1},
	} {
		if _, err := db.Exec(ins, z...); err != nil {
			t.Fatalf("%v: %v", z[0], err)
		}
	}
	if _, err := db.Exec("INSERT INTO agent_token_boards (token_id, board_id) VALUES ('t5','b1'),('t5','b2')"); err != nil {
		t.Fatal(err)
	}
	// Explizite Spalten: Migration 0039 (Karte #799) hängt anbieter/beleg an, das
	// soll den Vergleich „0038 verändert keine Zeile“ nicht stören.
	const tokens = "SELECT id,name,board_id,role,token_hash,created_at,created_by,revoked_at,user_id,expires_at,kennung,delegierend,mehrere FROM agent_tokens ORDER BY id"
	const listen = "SELECT * FROM agent_token_boards ORDER BY 1,2"
	const schema = "SELECT name, sql FROM sqlite_master WHERE tbl_name IN ('agent_tokens','agent_token_boards') AND type IN ('index','trigger') ORDER BY name"
	vorher := []string{tabellenText(t, db, tokens), tabellenText(t, db, listen), tabellenText(t, db, schema), tabellenText(t, db, "SELECT COUNT(*) FROM server_config_audit")}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	nachher := []string{tabellenText(t, db, tokens), tabellenText(t, db, listen), tabellenText(t, db, schema), tabellenText(t, db, "SELECT COUNT(*) FROM server_config_audit")}
	for i, was := range []string{"agent_tokens", "agent_token_boards", "Indizes/Trigger", "Audit"} {
		if vorher[i] != nachher[i] {
			t.Errorf("%s verändert:\n%s\nvs\n%s", was, vorher[i], nachher[i])
		}
	}
	if v := tabellenText(t, db, "PRAGMA user_version"); v != fmt.Sprintf("[%d]\n", alle[len(alle)-1].version) {
		t.Errorf("user_version: %q", v)
	}
	if v := tabellenText(t, db, "PRAGMA foreign_key_check"); v != "" {
		t.Errorf("foreign_key_check: %q", v)
	}
	// Die Trigger feuern weiter: Board b1 löschen → Audit für das aktive Token
	// t1, das aktive vorschlag-Token t3 und t5s Board-Liste.
	if _, err := db.Exec("DELETE FROM boards WHERE id='b1'"); err != nil {
		t.Fatal(err)
	}
	var audit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE action IN ('agent-token-revoke-board-deleted','agent-token-board-removed')`).Scan(&audit); err != nil || audit != 3 {
		t.Errorf("Audit-Einträge nach Board-Löschung: %d (erwartet 3) %v", audit, err)
	}
}

// TestVorschlaege783_AnlegenParallelUndDoppelt: 30 parallele Anlagen eines
// Tokens ergeben genau 10 Vorschläge, der Rest „Grenze“ — kein „database is
// locked“ (Kritik K-B1, gemessen vorher 2–13 Fehler). Gleicher Hash →
// ErrVorschlagDoppelt mit dem vorhandenen.
func TestVorschlaege783_AnlegenParallelUndDoppelt(t *testing.T) {
	ctx := context.Background()
	db := vorschlagTestDB(t)
	repo := NewVorschlagRepo(db)
	var mu sync.Mutex
	ok, grenze := 0, 0
	var andere []error
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.Anlegen(ctx, neuerVorschlag(fmt.Sprintf("p%02d", i), "b1", "t1"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrVorschlagGrenze):
				grenze++
			default:
				andere = append(andere, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != VorschlaegeJeTokenMax || grenze != 30-VorschlaegeJeTokenMax || len(andere) > 0 {
		t.Fatalf("parallel: ok=%d grenze=%d andere=%v", ok, grenze, andere)
	}
	// Doppelt: anderes Token, zweimal derselbe Hash.
	erst := neuerVorschlag("d1", "b1", "t9")
	if _, err := repo.Anlegen(ctx, erst); err != nil {
		t.Fatal(err)
	}
	zweit := neuerVorschlag("d2", "b1", "t9")
	zweit.Hash = erst.Hash
	alt, err := repo.Anlegen(ctx, zweit)
	if !errors.Is(err, ErrVorschlagDoppelt) || alt == nil || alt.ID != "d1" {
		t.Fatalf("Doppel: %v %+v", err, alt)
	}
	// Derselbe Hash eines ANDEREN Tokens ist kein Doppel.
	fremd := neuerVorschlag("d3", "b1", "t8")
	fremd.Hash = erst.Hash
	if _, err := repo.Anlegen(ctx, fremd); err != nil {
		t.Fatalf("gleicher Hash, anderes Token: %v", err)
	}
	// Wiederherstellen eines Vorschlags, den es noch gibt: kein Fehler; eine
	// verletzte Regel (Art außerhalb des CHECK) dagegen schon (K-B10).
	if err := NewVorschlagRepo(db).Wiederherstellen(ctx, erst); err != nil {
		t.Errorf("Wiederherstellen vorhanden: %v", err)
	}
	kaputt := *erst
	kaputt.ID, kaputt.Art = "kaputt", "anhang"
	if err := NewVorschlagRepo(db).Wiederherstellen(ctx, &kaputt); err == nil {
		t.Error("Wiederherstellen mit ungültiger Art: kein Fehler")
	}
}
