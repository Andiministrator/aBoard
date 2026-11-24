package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		prefix, _, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %s missing _-separator", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %s: invalid version prefix: %w", e.Name(), err)
		}
		raw, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration version gap: erwartet %d, gefunden %d (%s)", i+1, m.version, m.name)
		}
	}
	return out, nil
}

// RunMigrations führt fehlende Migrations transaktional aus und setzt
// PRAGMA user_version auf die zuletzt angewendete Version. Idempotent.
//
// F-191: Versions-Check und Anwendung laufen in EINER Transaktion, die mit
// BEGIN IMMEDIATE eröffnet wird — der Write-Lock wird damit SOFORT erworben,
// nicht erst beim ersten Write (Go/SQLite-Default = DEFERRED). Ein parallel
// startender Prozess (Docker-Restart-Loop, K8s-Probe-Race, parallele CLI)
// blockiert dadurch auf dem Lock (busy_timeout aus dem DSN) und liest nach
// dem Commit das bereits hochgezählte user_version — statt user_version=0
// doppelt zu lesen und Migration 1 ein zweites Mal anzuwenden (CREATE TABLE
// ohne IF NOT EXISTS → Abbruch). Begründung: ADR-0059 §3, F-191.
//
// Eine gepinnte *sql.Conn ist nötig, weil BEGIN/COMMIT/ROLLBACK auf
// derselben physischen Connection laufen müssen; database/sql kann
// BEGIN IMMEDIATE nicht über sql.TxOptions ausdrücken.
//
// BEWUSST OHNE `IF NOT EXISTS` in den Migrationsdateien (Karte #385, dritter
// Punkt der Definition of Done — geprüft und verworfen):
//
// Als zweiter Gürtel klingt es harmlos, ist aber ein Sicherheitsnetz, das
// Fehler VERDECKT statt sie aufzufangen. Läuft eine Migration ein zweites
// Mal, ist das per Definition ein Defekt im Versions-Tracking — die einzige
// Instanz, die entscheidet, was schon angewendet wurde. Mit `IF NOT EXISTS`
// liefe der zweite Durchgang scheinbar durch: die CREATE-Statements wären
// stumm, die NICHT idempotenten Anweisungen derselben Datei (ALTER TABLE,
// INSERT von Startdaten, UPDATE) aber nicht — die scheiterten weiterhin oder,
// schlimmer, verdoppelten Zeilen. Ergebnis wäre ein halb migriertes Schema,
// das sich für vollständig migriert hält.
//
// Der harte Abbruch ist die bessere Antwort: er passiert beim Start, ist
// laut, und der Zustand auf der Platte ist dank der Transaktion unverändert.
// Die Nebenläufigkeit ist eine Sperren-Frage und wird oben als solche gelöst.
// SchemaNeuerZulassenEnv ist der Notschalter für F-1237: auf "1" gesetzt,
// startet der Server auch auf einer DB mit höherer Schema-Version (mit Warnung).
const SchemaNeuerZulassenEnv = "ABOARD_SCHEMA_NEUER_ZULASSEN"

func RunMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate: begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Eigener Context: Rollback muss auch bei abgebrochenem ctx greifen.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("migrate: read user_version: %w", err)
	}
	// F-1237: Eine DB, die eine NEUERE Version migriert hat, als dieses
	// Binary kennt, ist ein Rollback ohne Backup (docs/handbuch/server/container.md,
	// Updates: die alte Version allein ist kein Rückweg). Ohne diese Prüfung
	// startete das ältere Binary stumm auf einem Schema, das es nicht kennt —
	// bei einer Migration, die Spalten umbenennt, mit Laufzeitfehlern mitten
	// im Betrieb statt einem klaren Abbruch beim Start. Notschalter für den
	// Fall, dass kein Backup existiert und der Betreiber es bewusst riskiert.
	if hoechste := migrations[len(migrations)-1].version; current > hoechste {
		if os.Getenv(SchemaNeuerZulassenEnv) != "1" {
			return fmt.Errorf("migrate: Datenbank hat Schema-Version %d, dieses Programm kennt nur bis %d — "+
				"vermutlich ein Rollback ohne Backup; Backup der passenden Version zurückspielen "+
				"(docs/handbuch/server/container.md, Abschnitt Updates) oder bewusst %s=1 setzen", current, hoechste, SchemaNeuerZulassenEnv)
		}
		log.Printf("WARNUNG: Datenbank hat Schema-Version %d, dieses Programm kennt nur bis %d — "+
			"Start trotzdem, weil %s=1 gesetzt ist", current, hoechste, SchemaNeuerZulassenEnv)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		// PRAGMA user_version schreibt in den DB-Header und ist Teil der
		// Transaktion — wird bei Rollback mit zurückgenommen.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			return fmt.Errorf("migration %s: set user_version: %w", m.name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	committed = true
	return nil
}
