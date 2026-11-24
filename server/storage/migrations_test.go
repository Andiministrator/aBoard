package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// hoechsteMigrationsversion liefert die Version der letzten Migrationsdatei.
// loadMigrations prüft dabei bereits die Lückenlosigkeit (1..n) — es genügt
// also, das letzte Element zu nehmen.
func hoechsteMigrationsversion(t *testing.T) int {
	t.Helper()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("keine Migrationen gefunden")
	}
	return ms[len(ms)-1].version
}

func TestRunMigrations_CreatesTables(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"boards", "attachments", "users", "acl", "activities"} {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("Tabelle %s nicht gefunden: %v", table, err)
		}
	}

	for _, idxName := range []string{"idx_attachments_card", "idx_acl_user", "idx_activities_board", "idx_activities_actor"} {
		var idx string
		err = db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type='index' AND name = ?", idxName,
		).Scan(&idx)
		if err != nil {
			t.Fatalf("Index %s nicht gefunden: %v", idxName, err)
		}
	}

	var userVersion int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	// Erwartung aus loadMigrations() statt fest kodiert: Die Zahl stand hier
	// als `13` und musste bei JEDER neuen Migration von Hand nachgezogen
	// werden — bei 0014 (F-425) prompt vergessen und der Test rot, obwohl
	// nichts kaputt war. Ein Test, der bei korrekter Arbeit fehlschlägt,
	// erzieht zum Wegschauen (Karte #355).
	if erwartet := hoechsteMigrationsversion(t); userVersion != erwartet {
		t.Fatalf("user_version = %d, erwartet %d", userVersion, erwartet)
	}

	// 0005 bringt server_config + server_config_audit + users.is_admin.
	// 0007 bringt mail_spam_senders + mail_spam_counter.
	// 0008 bringt mail_imap_state + mail_imap_processed.
	// 0009 bringt mail_autoreply_log.
	// 0011 bringt clients + projects.
	// 0012 bringt llm_conversations + llm_turns.
	// 0013 bringt ical_subscriptions.
	for _, table := range []string{"server_config", "server_config_audit", "llm_api_keys", "mail_spam_senders", "mail_spam_counter", "mail_imap_state", "mail_imap_processed", "mail_autoreply_log", "clients", "projects", "llm_conversations", "llm_turns", "ical_subscriptions"} {
		var name string
		err := db.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("Tabelle %s nicht gefunden: %v", table, err)
		}
	}
	var idx string
	if err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='index' AND name='server_config_audit_ts'",
	).Scan(&idx); err != nil {
		t.Fatalf("Index server_config_audit_ts nicht gefunden: %v", err)
	}
	// users.is_admin: erwartet als zusätzliche Spalte mit DEFAULT 0.
	var colName string
	var defaultVal sql.NullString
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(users)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(users): %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var typ string
		if err := rows.Scan(&cid, &colName, &typ, &notnull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if colName == "is_admin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("users.is_admin nicht gefunden (Migration 0005)")
	}
}

func TestRunMigrations_Idempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("erstes Open: %v", err)
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("zweite RunMigrations: %v", err)
	}
	db.Close()

	db2, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("zweites Open: %v", err)
	}
	defer db2.Close()

	var userVersion int
	if err := db2.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if erwartet := hoechsteMigrationsversion(t); userVersion != erwartet {
		t.Fatalf("user_version nach Re-Open = %d, erwartet %d", userVersion, erwartet)
	}
}

// F-191-Regression: zwei sequentielle Open() auf derselben DB-Datei dürfen
// die Migrationen nicht doppelt anwenden (CREATE TABLE ohne IF NOT EXISTS
// würde sonst beim zweiten Lauf abbrechen). Sichert ab, dass der
// BEGIN-IMMEDIATE-Pfad korrekt am bereits-angewendeten user_version vorbei-
// läuft. Echte Parallelität ist im Unit-Test nicht deterministisch
// reproduzierbar; der sequentielle Pfad deckt die Idempotenz-Logik ab.
// TestRunMigrations_WiederholterLauf prüft die IDEMPOTENZ: dreimal
// nacheinander migrieren darf nicht scheitern.
//
// ACHTUNG (Karte #385): Dieser Test hiess bis Sprint 54
// `TestRunMigrations_F191_KeinDoppelApply` und galt als die Absicherung von
// F-191. Das war er nie. F-191 beschreibt zwei Prozesse, die GLEICHZEITIG
// starten und beide user_version=0 lesen, bevor einer schreibt. Hier laufen
// die drei Durchgänge streng nacheinander — der zweite liest zwangsläufig
// die bereits geschriebene Version. Der Test wäre auch mit einer DEFERRED-
// Transaktion grün gewesen, also mit exakt dem Defekt, den er belegen
// sollte. Der echte Nebenläufigkeits-Test steht darunter.
func TestRunMigrations_WiederholterLauf(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	for i := 0; i < 3; i++ {
		db, err := Open(ctx, Config{DBPath: dbPath})
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := RunMigrations(ctx, db); err != nil {
			t.Fatalf("RunMigrations #%d: %v", i, err)
		}
		db.Close()
	}
}

// TestMigration0015_ChannelCheckAufNotificationLog sichert F-379 ab (Karte
// #411): die neue Migration 0015 rüstet den fehlenden channel-CHECK auf
// notification_log nach — auch für eine DB, die bereits auf Version 14 stand
// (also 0010 in der Fassung OHNE Check gelaufen sein könnte).
//
// GEGENPROBE (Karte #411): Entfernt man CHECK(channel IN (...)) aus
// 0015_notification_log_channel_check.sql, insertiert der letzte Schritt
// unten (bewusst ungültiger channel-Wert) klaglos durch — der Test wird rot.
func TestMigration0015_ChannelCheckAufNotificationLog(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	// Roh-DB auf Version 14 bringen — OHNE storage.Open (das würde sofort
	// alle Migrationen inkl. 0015 durchziehen). Simuliert eine DB, die vor
	// dieser Karte zuletzt migriert wurde.
	raw, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := journalModusAufWAL(ctx, raw); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > 14 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 14"); err != nil {
		t.Fatalf("user_version=14 setzen: %v", err)
	}

	// 0010_notifications.sql trägt den channel-CHECK im Repo HEUTE bereits
	// (noch am selben Tag nachgepflegt, siehe Kommentar in 0015). Der Replay
	// oben liefert also schon eine korrekte Tabelle — genau die DB, die
	// diese Migration eigentlich absichern soll (eine, bei der 0010 VOR dem
	// Fix lief), lässt sich aus dem heutigen Dateiinhalt nicht mehr
	// reproduzieren. Um die historische Lücke trotzdem testbar zu machen,
	// wird notification_log hier bewusst durch die damalige (fehlerhafte)
	// Fassung OHNE CHECK ersetzt — das ist exakt der Ausgangszustand, den
	// 0015 laut Kommentar dort adressiert.
	if _, err := raw.ExecContext(ctx, `DROP TABLE notification_log`); err != nil {
		t.Fatalf("notification_log droppen (Vorbereitung historischer Zustand): %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE notification_log (
		id              TEXT PRIMARY KEY,
		user_id         TEXT NOT NULL,
		event_type      TEXT NOT NULL,
		channel         TEXT NOT NULL,
		card_id         TEXT,
		board_id        TEXT,
		summary         TEXT,
		ts              TEXT NOT NULL,
		status          TEXT NOT NULL CHECK (status IN ('sent', 'failed', 'coalesced', 'throttled')),
		retry_count     INTEGER NOT NULL DEFAULT 0,
		error           TEXT,
		digested_at     TEXT
	)`); err != nil {
		t.Fatalf("notification_log ohne CHECK anlegen: %v", err)
	}

	// Bestandszeile MIT ungültigem channel — genau der Fall, den die
	// Migration beim Kopieren ausfiltern muss (kein CHECK im simulierten
	// historischen Zustand).
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO notification_log (id, user_id, event_type, channel, ts, status, retry_count)
		 VALUES ('bad-1', 'u1', 'card.commented', 'sms', '2020-01-01T00:00:00Z', 'sent', 0)`); err != nil {
		t.Fatalf("Bestandszeile mit ungültigem channel einfügen: %v", err)
	}
	// Bestandszeile MIT gültigem channel — muss die Migration überleben.
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO notification_log (id, user_id, event_type, channel, ts, status, retry_count)
		 VALUES ('good-1', 'u1', 'card.commented', 'email', '2020-01-01T00:00:00Z', 'sent', 0)`); err != nil {
		t.Fatalf("Bestandszeile mit gültigem channel einfügen: %v", err)
	}
	raw.Close()

	// Jetzt regulär öffnen — RunMigrations wendet 15 (und alles Weitere) an.
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open nach Vorab-Migration: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM notification_log WHERE id = 'bad-1'`).Scan(&count); err != nil {
		t.Fatalf("count bad-1: %v", err)
	}
	if count != 0 {
		t.Errorf("Zeile mit ungültigem channel ('bad-1') hätte beim Kopieren ausgefiltert werden müssen")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM notification_log WHERE id = 'good-1'`).Scan(&count); err != nil {
		t.Fatalf("count good-1: %v", err)
	}
	if count != 1 {
		t.Errorf("Zeile mit gültigem channel ('good-1') hätte die Migration überleben müssen")
	}

	// Der CHECK muss jetzt tatsächlich greifen — neue Inserts mit ungültigem
	// channel scheitern.
	_, err = db.ExecContext(ctx,
		`INSERT INTO notification_log (id, user_id, event_type, channel, ts, status, retry_count)
		 VALUES ('bad-2', 'u1', 'card.commented', 'sms', '2020-01-01T00:00:00Z', 'sent', 0)`)
	if err == nil {
		t.Fatalf("Insert mit ungültigem channel hätte am CHECK scheitern müssen")
	}
}

// TestMigration0016_ActivitiesOpSpalten sichert ADR-0129 §5 (Karte #554) ab:
// Migration 0016 ergänzt activities um card_id/op_type/actor_kind. Eine
// Bestandszeile (vor 0016 geschrieben, also ohne diese Spalten) muss den
// Umbau überleben und die Defaults (”, ”, 'mensch') bekommen; der neue
// CHECK auf actor_kind muss danach greifen.
//
// GEGENPROBE: Ersetzt man in 0016_activities_op.sql den CHECK auf actor_kind
// durch nichts (nur die Spalte ohne Constraint), insertiert der letzte
// Schritt unten (ungültiger actor_kind) klaglos durch — der Test wird rot.
func TestMigration0016_ActivitiesOpSpalten(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	// Roh-DB auf Version 15 bringen — OHNE storage.Open (das würde sofort
	// auch 0016 durchziehen). Simuliert eine DB, die vor dieser Karte zuletzt
	// migriert wurde.
	raw, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := journalModusAufWAL(ctx, raw); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > 15 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 15"); err != nil {
		t.Fatalf("user_version=15 setzen: %v", err)
	}

	// Bestandsdaten: ein Board (FK-Referenz) und eine Activity-Zeile in der
	// alten Spaltenform (0003, ohne card_id/op_type/actor_kind).
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 'Bestand', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("Bestands-Board einfügen: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO activities (id, board_id, actor, action, field, from_val, to_val, note, ts)
		 VALUES ('a-alt', 'b1', 'u@example.com', 'board-created', '', '', '', 'Bestand', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("Bestands-Activity einfügen: %v", err)
	}
	raw.Close()

	// Jetzt regulär öffnen — RunMigrations wendet 16 (und alles Weitere) an.
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open nach Vorab-Migration: %v", err)
	}
	defer db.Close()

	var cardID, opType, actorKind string
	if err := db.QueryRowContext(ctx,
		`SELECT card_id, op_type, actor_kind FROM activities WHERE id = 'a-alt'`,
	).Scan(&cardID, &opType, &actorKind); err != nil {
		t.Fatalf("Bestandszeile nach Migration lesen: %v", err)
	}
	if cardID != "" || opType != "" || actorKind != "mensch" {
		t.Errorf("Bestandszeile: card_id=%q op_type=%q actor_kind=%q, erwartet ''/''/mensch", cardID, opType, actorKind)
	}

	// Der CHECK muss jetzt greifen — ein ungültiger actor_kind scheitert.
	_, err = db.ExecContext(ctx,
		`INSERT INTO activities (id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind)
		 VALUES ('a-neu', 'b1', 'u@example.com', 'op', '', '', '', '', '2026-01-01T00:00:00Z', 'k1', 'card.field', 'roboter')`)
	if err == nil {
		t.Fatal("Insert mit ungültigem actor_kind hätte am CHECK scheitern müssen")
	}

	// Der Index für den Lese-Endpunkt (Protokoll je Karte) muss existieren.
	var idx string
	if err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_activities_card'",
	).Scan(&idx); err != nil {
		t.Fatalf("Index idx_activities_card nicht gefunden: %v", err)
	}
}

// TestMigration0019_ActivitiesTsFesteBreite (F-1157, Karte #582): Bestand
// in RFC3339Nano-Form wird auf "YYYY-MM-DDTHH:MM:SS.mmmZ" gebracht
// (auffüllen bzw. abschneiden), fremde Formen bleiben unverändert, die
// Indizes laufen danach aufsteigend über ts.
//
// GEGENPROBE (Karte #582): Ohne das UPDATE in 0019 bleiben die Werte in
// Altform stehen, ohne DROP/CREATE INDEX bleibt „ts DESC“ — beides rot.
func TestMigration0019_ActivitiesTsFesteBreite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	raw, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := journalModusAufWAL(ctx, raw); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > 18 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 18"); err != nil {
		t.Fatalf("user_version=18 setzen: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 'Bestand', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("Bestands-Board einfügen: %v", err)
	}
	faelle := map[string]string{
		"2020-01-01T00:00:00Z":            "2020-01-01T00:00:00.000Z",
		"2020-01-01T00:00:00.5Z":          "2020-01-01T00:00:00.500Z",
		"2020-01-01T00:00:00.123456789Z":  "2020-01-01T00:00:00.123Z",
		"2020-01-01T00:00:00.999999999Z":  "2020-01-01T00:00:00.999Z",
		"2020-01-01T00:00:00.123Z":        "2020-01-01T00:00:00.123Z",
		"2026-01-01":                      "2026-01-01",
		"2020-01-01T00:00:00+02:00":       "2020-01-01T00:00:00+02:00",
		"2020-01-01T00:00:00.xZ":          "2020-01-01T00:00:00.xZ",
		"2020-01-01T00:00:00.1234567890Z": "2020-01-01T00:00:00.1234567890Z",
	}
	for alt := range faelle {
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO activities (id, board_id, action, ts) VALUES (?, 'b1', 'board-updated', ?)`,
			"id-"+alt, alt); err != nil {
			t.Fatalf("Bestand %q einfügen: %v", alt, err)
		}
	}
	raw.Close()

	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open nach Vorab-Migration: %v", err)
	}
	defer db.Close()

	for alt, want := range faelle {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT ts FROM activities WHERE id = ?`, "id-"+alt).Scan(&got); err != nil {
			t.Fatalf("%q lesen: %v", alt, err)
		}
		if got != want {
			t.Errorf("ts %q → %q, erwartet %q", alt, got, want)
		}
	}
	for name, want := range map[string]string{
		"idx_activities_board": "CREATE INDEX idx_activities_board ON activities(board_id, ts)",
		"idx_activities_card":  "CREATE INDEX idx_activities_card ON activities(board_id, card_id, ts)",
		// Migration 0021 (F-1212) ersetzt den Teilindex aus 0020: Kappung nach rowid.
		"idx_activities_op_rowid": "CREATE INDEX idx_activities_op_rowid ON activities(board_id) WHERE action = 'op'",
	} {
		var got string
		if err := db.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&got); err != nil {
			t.Fatalf("Index %s: %v", name, err)
		}
		if got != want {
			t.Errorf("Index %s = %q, erwartet %q", name, got, want)
		}
	} // F-1212: Der Teilindex aus 0020 hat keinen Leser mehr und fällt mit 0021 weg.
	var alt int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_activities_op'").Scan(&alt); err != nil || alt != 0 {
		t.Errorf("idx_activities_op noch vorhanden (%d, %v) — Migration 0021 nicht gelaufen", alt, err)
	}
}

func TestOpen_PragmasGesetzt(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, erwartet wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, erwartet 1", foreignKeys)
	}
}

// F-190: foreign_keys / synchronous müssen auf jeder Pool-Connection greifen,
// nicht nur auf der ersten. Sichert die DSN-Pragma-Strategie ab.
func TestOpen_PragmasAufMehrerenConnections(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Mehrere dedizierte Connections aus dem Pool erzwingen.
	conns := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}

	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("Conn %d foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Fatalf("Conn %d foreign_keys = %d, erwartet 1 (F-190 Regression)", i, fk)
		}
		var sync int
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatalf("Conn %d synchronous: %v", i, err)
		}
		if sync != 1 { // NORMAL = 1
			t.Fatalf("Conn %d synchronous = %d, erwartet 1/NORMAL", i, sync)
		}
	}
}

// F-190: Cascading-Delete muss greifen — wenn foreign_keys auf einer
// Pool-Connection 0 ist, überleben attachments-Rows die boards-Löschung.
func TestOpen_CascadeDeleteAufMehrerenConnections(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 't', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO attachments (id, board_id, card_id, filename, mime, size, storage_path, created_at)
		 VALUES ('a1', 'b1', 'c1', 'f', 'text/plain', 0, 'p', 0)`); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}

	// Löschung auf einer frischen Pool-Connection (typisch bei parallelen Handlern).
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "DELETE FROM boards WHERE id = 'b1'"); err != nil {
		t.Fatalf("delete board: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM attachments WHERE board_id = 'b1'").Scan(&n); err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if n != 0 {
		t.Fatalf("attachments übrig: %d (F-190 Regression — Cascading-Delete greift nicht)", n)
	}
}

// TestRunMigrations_F191_ParallelerStart ist der Test, den F-191 verlangt:
// mehrere Prozesse migrieren GLEICHZEITIG dieselbe Datenbankdatei.
//
// Der Defekt (Sprint 21): `BeginTx(ctx, nil)` öffnet in SQLite eine
// DEFERRED-Transaktion. Die nimmt das Schreib-Lock erst beim ersten
// Schreibzugriff — beide Prozesse können also `PRAGMA user_version` als 0
// lesen, bevor einer schreibt, und beide wenden Migration 1 an. Die
// Migrationen benutzen `CREATE TABLE` ohne `IF NOT EXISTS`, der Verlierer
// scheitert hart und der Serverstart bricht ab.
//
// Der Fix (Karte #197): `BEGIN IMMEDIATE` auf einer gepinnten *sql.Conn.
// Das Schreib-Lock fällt sofort; die übrigen Aufrufer blockieren bis zum
// busy_timeout und lesen danach die fertige Version.
//
// WARUM HIER NICHT `Open` BENUTZT WIRD (Karte #385, zweiter Anlauf): Die
// erste Fassung dieses Tests rief `storage.Open` in allen Goroutinen — und
// war FLAKY. Sie scheiterte an `PRAGMA journal_mode = WAL`, das `Open` vor
// der Migration ausführt: Der Journal-Modus-Wechsel braucht ein exklusives
// Lock und liefert bei konkurrierendem Zugriff SQLITE_BUSY, ohne auf den
// busy_timeout zu warten. Das ist ein ECHTER, aber ANDERER Befund als F-191
// (festgehalten als F-597) — und er hat in diesem Test nichts zu suchen:
// Ein Test, der aus zwei Gründen rot werden kann, sagt nicht mehr, welcher
// vorliegt. Deshalb wird der Journal-Modus hier einmal seriell vorbereitet,
// und die Goroutinen greifen ausschliesslich `RunMigrations` an.
//
// GEGENGEPROBT: Mit "BEGIN" statt "BEGIN IMMEDIATE" scheitern die Läufe
// zuverlässig. Der beobachtete Fehler ist "database is locked (5)
// (SQLITE_BUSY)": Alle halten nach dem PRAGMA-Lesen ein SHARED-Lock, keiner
// kommt auf EXCLUSIVE hoch, der busy_timeout läuft ab — der klassische
// SQLite-Lock-Upgrade-Deadlock. Das doppelte Apply wäre der Ausgang bei
// genau zwei Läufen und günstigem Timing; ab drei gewinnt der Deadlock.
// Beide Wege enden im abgebrochenen Serverstart, beide fängt dieser Test.
//
// Der Punkt der Gegenprobe: Er KANN fehlschlagen. Sein Vorgänger
// (`TestRunMigrations_WiederholterLauf`, oben) konnte das nie.
func TestRunMigrations_F191_ParallelerStart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	// Vorbereitung: Datei anlegen und den Journal-Modus setzen — seriell, wie
	// es ein einzelner Erststart tut. NICHT migrieren; user_version bleibt 0.
	vorab, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("Vorbereitung sql.Open: %v", err)
	}
	if _, err := vorab.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("Vorbereitung journal_mode: %v", err)
	}
	var vorVersion int
	if err := vorab.QueryRowContext(ctx, "PRAGMA user_version").Scan(&vorVersion); err != nil {
		t.Fatalf("Vorbereitung user_version: %v", err)
	}
	if vorVersion != 0 {
		t.Fatalf("Vorbedingung verletzt: user_version = %d, erwartet 0", vorVersion)
	}
	vorab.Close()

	const gleichzeitig = 8

	// Jeder "Prozess" bekommt seine eigene *sql.DB auf dieselbe Datei —
	// getrennte Connection-Pools, wie bei getrennten Prozessen. Alle warten
	// an derselben Schranke, damit sie das Zeitfenster wirklich treffen.
	var start sync.WaitGroup
	var fertig sync.WaitGroup
	start.Add(1)
	fehler := make([]error, gleichzeitig)

	for i := 0; i < gleichzeitig; i++ {
		fertig.Add(1)
		go func(n int) {
			defer fertig.Done()
			db, err := sql.Open("sqlite", dsnFuer(dbPath))
			if err != nil {
				fehler[n] = fmt.Errorf("sql.Open: %w", err)
				start.Wait()
				return
			}
			defer db.Close()
			start.Wait() // Schranke: alle laufen gemeinsam los
			if err := RunMigrations(ctx, db); err != nil {
				fehler[n] = err
			}
		}(i)
	}
	start.Done()
	fertig.Wait()

	for i, err := range fehler {
		if err != nil {
			t.Errorf("paralleler Lauf %d scheiterte: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// Endzustand: user_version steht auf der höchsten Migration, und zwar
	// genau einmal angewendet. Ein doppeltes Apply hätte oben schon einen
	// Fehler erzeugt; hier wird geprüft, dass auch nichts ÜBERSPRUNGEN wurde,
	// weil ein Blockierter eine halb fertige Version gelesen hat.
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open zur Kontrolle: %v", err)
	}
	defer db.Close()

	erwartet := hoechsteMigrationsversion(t)
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version lesen: %v", err)
	}
	if version != erwartet {
		t.Errorf("user_version = %d, erwartet %d", version, erwartet)
	}

	// Und das Schema ist wirklich da — nicht nur die Versionsnummer.
	var tabellen int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='boards'").Scan(&tabellen); err != nil {
		t.Fatalf("sqlite_master lesen: %v", err)
	}
	if tabellen != 1 {
		t.Errorf("Tabelle boards %d-mal vorhanden, erwartet genau 1", tabellen)
	}
}

// F-1237: Ein älteres Binary darf auf einer DB mit höherer Schema-Version
// nicht stumm starten — nur mit bewusst gesetztem Notschalter.
func TestRunMigrations_NeueresSchemaBrichtAb(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	zukunft := hoechsteMigrationsversion(t) + 1
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", zukunft)); err != nil {
		t.Fatalf("user_version setzen: %v", err)
	}

	t.Setenv(SchemaNeuerZulassenEnv, "")
	err = RunMigrations(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "Schema-Version") {
		t.Fatalf("erwartet Abbruch wegen neuerer Schema-Version, bekommen: %v", err)
	}

	t.Setenv(SchemaNeuerZulassenEnv, "1")
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatalf("mit Notschalter erwartet Start, bekommen: %v", err)
	}
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != zukunft {
		t.Fatalf("user_version = %d, erwartet unverändert %d", v, zukunft)
	}
}
