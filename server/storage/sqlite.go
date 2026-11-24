package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Config bündelt Pfad-Optionen für die DB.
type Config struct {
	// DBPath ist der Dateipfad der SQLite-Datenbank.
	// Default in main.go: /data/db/aboard.db (ENV ABOARD_DB überschreibt).
	DBPath string
}

// dsnFuer baut den Verbindungsstring. Ausgelagert, damit Tests, die den
// Serverstart nachstellen, exakt dieselben Pragmas benutzen wie der Betrieb —
// eine nachgebaute Kopie im Test würde sonst irgendwann abweichen und genau
// das nicht mehr prüfen, wofür sie da ist (Karte #385/#355).
func dsnFuer(pfad string) string {
	return pfad +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
}

// Open öffnet die SQLite-Datenbank, setzt die Pragmas (WAL, synchronous,
// foreign_keys) und führt ausstehende Migrationen aus. Das Verzeichnis
// wird angelegt, falls es noch nicht existiert.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("storage: leerer DBPath")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("storage: DB-Verzeichnis: %w", err)
	}

	// Pro-Connection-Pragmas werden als DSN-Parameter gesetzt. Sonst hätten neue
	// Connections aus dem Pool (SetMaxOpenConns) abweichende Defaults — speziell
	// foreign_keys=0, was ON DELETE CASCADE auf attachments still aushebelt.
	db, err := sql.Open("sqlite", dsnFuer(cfg.DBPath))
	if err != nil {
		return nil, fmt.Errorf("storage: sql.Open: %w", err)
	}

	// WAL ist persistent in der DB-Datei verankert — einmaliges Setzen reicht.
	// Verbindungs-Pool klein halten: WAL erlaubt parallele Reader, SQLite mag
	// keine vielen gleichzeitigen Writer.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := journalModusAufWAL(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}

	if err := RunMigrations(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return db, nil
}

// walVersuche/walWartezeit begrenzen die Wiederholungen beim Setzen des
// Journal-Modus. Zehn Versuche à 50 ms = eine halbe Sekunde Geduld. Das
// Zeitfenster, um das es geht, ist die Dauer eines einzigen
// `PRAGMA journal_mode = WAL` auf einer leeren Datei — Mikrosekunden. Wer
// nach einer halben Sekunde immer noch nicht durchkommt, hat ein anderes
// Problem als einen Startwettlauf, und das soll er als Fehler sehen.
const (
	walVersuche  = 10
	walWartezeit = 50 * time.Millisecond
)

// journalModusAufWAL stellt sicher, dass die Datenbank im WAL-Modus läuft.
//
// F-597: `PRAGMA journal_mode = WAL` braucht ein exklusives Lock und liefert
// bei konkurrierendem Zugriff SQLITE_BUSY — **ohne** auf den busy_timeout(5000)
// aus dem DSN zu warten; der Busy-Handler greift bei diesem PRAGMA nicht.
// Beim parallelen Erststart auf einer frischen Datei (Docker-Restart-Schleife,
// Probe-Race — genau die Szenarien, die F-191 motivieren) brach `Open` deshalb
// ab, BEVOR `RunMigrations` überhaupt gerufen wurde. Der F-191-Fix in der
// Migrations-Transaktion greift dort per Konstruktion nicht: er sitzt hinter
// der Stelle, an der der Start scheitert.
//
// Der Fix hat zwei Teile — und nur einer davon trägt. Das auseinanderzuhalten
// ist wichtig, weil die naheliegende Erklärung falsch ist:
//
//  1. WIEDERHOLEN — das ist der eigentliche Fix. Von mehreren gleichzeitigen
//     Startern gewinnt genau einer den Modus-Wechsel; die übrigen bekommen
//     SQLITE_BUSY, warten kurz und sehen beim nächsten Durchlauf bereits WAL.
//     Sie kehren dann über den Lesepfad zurück, ohne selbst geschrieben zu
//     haben. Der Regressionstest hängt an dieser Eigenschaft.
//  2. ERST LESEN — eine Kostenoptimierung, KEIN Schutz gegen den Wettlauf.
//     Naheliegend wäre die Annahme, der Lesepfad vermeide das exklusive Lock
//     im Normalfall. Gemessen (Karte #395, Sprint 55) stimmt das nicht:
//     steht WAL bereits, kommt auch das SCHREIBENDE PRAGMA unter einem
//     fremden RESERVED-Lock ohne Fehler durch — es findet ja kein Wechsel
//     statt und braucht deshalb gar kein Lock. Der Lesepfad spart nur den
//     teureren Aufruf (gemessen 12 µs statt 810 µs). Wer ihn entfernt, macht
//     den Start langsamer, nicht kaputt.
//
// Zu Punkt 2 gibt es bewusst KEINEN Test: die Eigenschaft ist von aussen
// nicht beobachtbar. Ein Versuch über `query_only(1)` schlug fehl, weil
// SQLite den Journal-Modus-Wechsel auch auf einer query_only-Verbindung
// zulässt (ebenfalls gemessen) — der Test wäre grün geblieben, auch mit
// vollständig ausgebautem Lesepfad. Ein solcher Test hätte nur Sicherheit
// vorgetäuscht.
//
// Der Rückgabewert von `PRAGMA journal_mode = WAL` wird ausgewertet, nicht
// verworfen: SQLite meldet den TATSÄCHLICH gesetzten Modus zurück und kann
// die Umstellung ohne Fehler ablehnen (etwa auf einer :memory:-Datenbank,
// die dauerhaft "memory" bleibt). Ein `ExecContext`, das diese Zeile wegwirft,
// hielte eine abgelehnte Umstellung für einen Erfolg.
func journalModusAufWAL(ctx context.Context, db *sql.DB) error {
	var letzter error
	for versuch := 0; versuch < walVersuche; versuch++ {
		var modus string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&modus); err != nil {
			return fmt.Errorf("storage: pragma journal_mode lesen: %w", err)
		}
		if strings.EqualFold(modus, "wal") {
			return nil
		}

		var gesetzt string
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&gesetzt)
		switch {
		case err == nil && strings.EqualFold(gesetzt, "wal"):
			return nil
		case err == nil:
			// Kein Fehler, aber auch kein WAL: SQLite hat die Umstellung
			// abgelehnt. Wiederholen hilft hier nicht — sofort melden.
			return fmt.Errorf("storage: pragma journal_mode: blieb %q statt wal", gesetzt)
		default:
			// F-624: NUR bei einem Lock-Konflikt wiederholen. Auf einem
			// schreibgeschützten Volume drehte die Schleife sonst zehnmal, wartete
			// eine halbe Sekunde und meldete dann „nach 10 Versuchen: … readonly"
			// — was den Betreiber auf eine Lock-Konkurrenz schickt statt auf die
			// Berechtigungen. Ein Fehler, der sich durch Warten nicht ändert,
			// gehört sofort gemeldet.
			if !istLockKonflikt(err) {
				return fmt.Errorf("storage: pragma journal_mode: %w", err)
			}
			letzter = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("storage: pragma journal_mode: %w", ctx.Err())
		case <-time.After(walWartezeit):
		}
	}
	return fmt.Errorf("storage: pragma journal_mode nach %d Versuchen: %w", walVersuche, letzter)
}

// istLockKonflikt erkennt die Fehler, bei denen Warten sinnvoll ist:
// SQLITE_BUSY (5) und SQLITE_LOCKED (6). Alles andere — fehlende
// Berechtigung, defekte Datei, volle Platte — wird durch Wiederholen nicht
// besser und soll den Aufrufer sofort erreichen (F-624).
//
// Geprüft wird auf dem Fehlertext, nicht auf einem Fehlercode: modernc.org/sqlite
// exportiert seinen Fehlertyp nicht in einer Form, die sich ohne Bindung an
// interne Pakete auswerten liesse. Der Text ist stabil („database is locked",
// „database table is locked") und wird von der Bibliothek selbst gebildet.
func istLockKonflikt(err error) bool {
	if err == nil {
		return false
	}
	txt := strings.ToLower(err.Error())
	return strings.Contains(txt, "is locked") || strings.Contains(txt, "sqlite_busy")
}
