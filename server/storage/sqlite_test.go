package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOpen_F597_WartetAufFremdesSchreibLock ist der eigentliche Regressionstest
// zu F-597 — der deterministische. Er stellt den Wettlauf her, statt auf ihn zu
// hoffen: ein fremder Schreiber hält ein RESERVED-Lock auf der Datei, während
// `Open` den Journal-Modus von "delete" auf WAL wechseln will.
//
// WARUM NICHT DER PARALLEL-TEST WEITER UNTEN: Der ist realitätsnah, aber als
// Wächter untauglich. Gemessen (Karte #395, alter Code, 20 Runden à 8
// gleichzeitige `Open`): **1 Bruch auf 160 Aufrufe**. Ein Test, der die
// Regression in weniger als einem Prozent der Fälle sieht, ist im Alltag ein
// Zufallsgenerator — genau die Flakiness, die schon den ersten Anlauf des
// F-191-Tests unbrauchbar machte. Er bleibt als Realitätsprobe erhalten, aber
// die Zusage „ohne Fix rot" trägt nur dieser Test hier.
//
// GEGENGEPROBT (Karte #395, Sprint 55), dreimal wiederholt, dreimal gleich:
//
//   - Alter Code (`db.ExecContext(ctx, "PRAGMA journal_mode = WAL")`):
//     `Open` scheitert nach ~300 µs mit
//     "storage: pragma journal_mode: database is locked (5) (SQLITE_BUSY)".
//   - Mit `journalModusAufWAL`: `Open` gelingt nach ~160 ms — es wartet das
//     Lock ab — und der Endzustand ist journal_mode = "wal".
//
// Die ~300 µs sind zugleich der Beleg für die Kernaussage des Findings: der
// `busy_timeout(5000)` aus dem DSN greift bei diesem PRAGMA NICHT. Griffe er,
// hätte der alte Code die 120 ms abgewartet statt sofort aufzugeben.
func TestOpen_F597_WartetAufFremdesSchreibLock(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	// Datei anlegen und im DELETE-Modus belassen (SQLite-Default). Damit steht
	// `Open` vor einem echten Modus-WECHSEL — dem Schritt, der das exklusive
	// Lock braucht. Auf einer Datei, die schon WAL trägt, gäbe es nichts zu
	// sperren, und der Test prüfte nichts.
	vorab, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("Vorbereitung sql.Open: %v", err)
	}
	if _, err := vorab.ExecContext(ctx, "CREATE TABLE platzhalter (a INTEGER)"); err != nil {
		t.Fatalf("Vorbereitung CREATE TABLE: %v", err)
	}
	var vorModus string
	if err := vorab.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&vorModus); err != nil {
		t.Fatalf("Vorbereitung journal_mode lesen: %v", err)
	}
	if strings.EqualFold(vorModus, "wal") {
		t.Fatalf("Vorbedingung verletzt: journal_mode ist bereits %q — "+
			"dann gibt es keinen Wechsel und nichts zu sperren", vorModus)
	}
	vorab.Close()

	// Der fremde Schreiber. `BEGIN IMMEDIATE` nimmt das RESERVED-Lock sofort;
	// der Modus-Wechsel kommt damit nicht auf EXCLUSIVE hoch.
	sperre, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("Sperre sql.Open: %v", err)
	}
	defer sperre.Close()
	conn, err := sperre.Conn(ctx)
	if err != nil {
		t.Fatalf("Sperre Conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("Sperre BEGIN IMMEDIATE: %v", err)
	}

	// Haltedauer: deutlich länger als der eine Versuch, den der alte Code
	// unternahm (~300 µs), und deutlich kürzer als die Geduld des Fixes
	// (walVersuche * walWartezeit = 500 ms). Genau dazwischen trennt der Test.
	const haltedauer = 120 * time.Millisecond
	freigegeben := make(chan error, 1)
	go func() {
		time.Sleep(haltedauer)
		_, err := conn.ExecContext(ctx, "COMMIT")
		conn.Close()
		freigegeben <- err
	}()

	beginn := time.Now()
	db, err := Open(ctx, Config{DBPath: dbPath})
	dauer := time.Since(beginn)
	if err != nil {
		t.Fatalf("Open scheiterte nach %v am fremden Schreib-Lock: %v", dauer, err)
	}
	defer db.Close()

	if err := <-freigegeben; err != nil {
		t.Fatalf("Sperre COMMIT: %v", err)
	}

	// Nicht nur „kein Fehler": der Modus muss auch wirklich gewechselt haben.
	// Ein Fix, der den Fehler bloss verschluckt, wäre sonst ebenfalls grün.
	var modus string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&modus); err != nil {
		t.Fatalf("journal_mode lesen: %v", err)
	}
	if !strings.EqualFold(modus, "wal") {
		t.Fatalf("journal_mode = %q, erwartet wal — der Fehler wurde verschluckt, "+
			"nicht behoben", modus)
	}

	// Und er hat tatsächlich gewartet, statt das Lock zu umgehen. Ohne diese
	// Schranke wäre der Test auch mit einem „Fix" grün, der den Modus-Wechsel
	// bei Konflikt einfach überspringt und WAL erst später zufällig vorfindet.
	if dauer < haltedauer {
		t.Fatalf("Open kehrte nach %v zurück, das Lock fiel erst nach %v — "+
			"der Wechsel kann nicht abgewartet worden sein", dauer, haltedauer)
	}
}

// TestOpen_F597_ParallelerErststart ist der Test, den F-597 verlangt: mehrere
// Prozesse rufen GLEICHZEITIG `Open` auf einer noch nicht existierenden
// Datenbankdatei — Docker-Restart-Schleife, Probe-Race.
//
// Der Defekt: `Open` führte `PRAGMA journal_mode = WAL` unbedingt aus, bevor
// `RunMigrations` lief. Dieses PRAGMA braucht ein exklusives Lock und liefert
// bei konkurrierendem Zugriff SQLITE_BUSY, OHNE auf den busy_timeout(5000)
// aus dem DSN zu warten — der Busy-Handler greift bei diesem PRAGMA nicht.
// Der Serverstart brach mit "storage: pragma journal_mode: database is locked
// (5) (SQLITE_BUSY)" ab, und zwar an einer Stelle VOR der Migration: der
// F-191-Fix (`BEGIN IMMEDIATE` in der Migrations-Transaktion) greift dort per
// Konstruktion nicht.
//
// ABGRENZUNG ZU TestRunMigrations_F191_ParallelerStart: Jener Test bereitet
// den Journal-Modus bewusst SERIELL vor und greift nur `RunMigrations` an,
// damit er nicht aus zwei Gründen rot werden kann. Dieser Test hier ist das
// Gegenstück — er lässt genau den Schritt zu, den der andere ausschliesst,
// und deckt damit die Lücke, die durch jene Isolation entstanden ist.
//
// GEGENGEPROBT (Karte #395, Sprint 55): Mit der alten Fassung
//
//	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil { … }
//
// anstelle von `journalModusAufWAL` scheitert dieser Test zuverlässig; die
// Fehlermeldung ist wörtlich "storage: pragma journal_mode: database is
// locked (5) (SQLITE_BUSY)". Der Punkt der Gegenprobe: Er KANN fehlschlagen.
//
// F-625 (Sprint-55-Kritiker): Dieser Test ist als WÄCHTER wirkungslos und
// bleibt trotzdem — als Realitätsprobe. Die Gegenprobe belegt es: mit dem alten
// `db.ExecContext`-Einzeiler war nur `TestOpen_F597_WartetAufFremdesSchreibLock`
// zuverlässig rot, dieser hier blieb grün. Wer eine Regression sucht, prüft
// gegen jenen Test, nicht gegen diesen. Was er leistet: Er fährt den echten
// Ablauf (acht gleichzeitige `Open` auf einer frischen Datei) und würde eine
// grobe Verschlechterung — etwa eine Sperre, die den Start ganz blockiert —
// dann doch bemerken. Deterministisch machen liesse er sich nur, indem die
// Schranke unmittelbar vor das PRAGMA wandert; das hiesse, `Open` für den Test
// aufzubrechen, und dafür ist der Ertrag zu klein.
func TestOpen_F597_ParallelerErststart(t *testing.T) {
	ctx := context.Background()
	// Bewusst KEINE Vorbereitung: die Datei darf noch nicht existieren. Genau
	// der Erststart ist der verwundbare Fall — sobald WAL einmal in der Datei
	// verankert ist, liest jeder weitere Start ihn nur noch.
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	const gleichzeitig = 8

	var start sync.WaitGroup
	var fertig sync.WaitGroup
	start.Add(1)
	fehler := make([]error, gleichzeitig)
	dbs := make([]*sql.DB, gleichzeitig)

	for i := 0; i < gleichzeitig; i++ {
		fertig.Add(1)
		go func(n int) {
			defer fertig.Done()
			start.Wait() // Schranke: alle laufen gemeinsam los
			db, err := Open(ctx, Config{DBPath: dbPath})
			if err != nil {
				fehler[n] = err
				return
			}
			dbs[n] = db
		}(i)
	}
	start.Done()
	fertig.Wait()

	for _, db := range dbs {
		if db != nil {
			defer db.Close()
		}
	}

	for i, err := range fehler {
		if err != nil {
			t.Errorf("paralleler Erststart %d scheiterte: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// Endzustand: der Journal-Modus steht wirklich auf WAL. Ohne diese Prüfung
	// wäre der Test auch mit einem "Fix" grün, der das PRAGMA einfach
	// weglässt — dann liefe der Server dauerhaft im DELETE-Journal und
	// verlöre die parallelen Reader, für die WAL überhaupt gesetzt wird.
	kontrolle, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("Kontrolle sql.Open: %v", err)
	}
	defer kontrolle.Close()
	var modus string
	if err := kontrolle.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&modus); err != nil {
		t.Fatalf("Kontrolle journal_mode: %v", err)
	}
	if !strings.EqualFold(modus, "wal") {
		t.Fatalf("journal_mode = %q, erwartet wal", modus)
	}

	// Und die Migration ist genau einmal durchgelaufen: user_version steht auf
	// der höchsten Migration. Ein abgebrochener Starter hätte oben schon einen
	// Fehler erzeugt; hier wird geprüft, dass auch nichts übersprungen wurde.
	erwartet := hoechsteMigrationsversion(t)
	var version int
	if err := kontrolle.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("Kontrolle user_version: %v", err)
	}
	if version != erwartet {
		t.Fatalf("user_version = %d, erwartet %d", version, erwartet)
	}
}

// TestJournalModusAufWAL_MeldetAbgelehnteUmstellung belegt den Zweig, den ein
// `ExecContext` verschluckt hätte: SQLite kann die Umstellung ohne Fehler
// ABLEHNEN und den alten Modus zurückgeben. Eine :memory:-Datenbank bleibt
// dauerhaft auf "memory".
//
// Ohne diesen Zweig hielte der Serverstart eine abgelehnte Umstellung für
// einen Erfolg und liefe ohne WAL weiter — still, und genau so lange, bis
// jemand sich über fehlende parallele Reader wundert.
func TestJournalModusAufWAL_MeldetAbgelehnteUmstellung(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open :memory:: %v", err)
	}
	defer db.Close()

	err = journalModusAufWAL(ctx, db)
	if err == nil {
		t.Fatal("kein Fehler bei abgelehnter Umstellung — die Rückgabe des " +
			"PRAGMA wird nicht ausgewertet")
	}
	if !strings.Contains(err.Error(), "statt wal") {
		t.Fatalf("Fehlermeldung nennt den tatsächlichen Modus nicht: %v", err)
	}
	// Die Meldung muss den vorgefundenen Modus wörtlich nennen — sonst steht
	// der Betreiber vor "ging nicht" ohne zu wissen, was stattdessen gilt.
	if !strings.Contains(err.Error(), "memory") {
		t.Fatalf("Fehlermeldung nennt %q nicht: %v", "memory", err)
	}
}

// TestJournalModusAufWAL_MeldetNichtWiederholbaresSofort deckt F-624 ab: Die
// Wiederholungsschleife ist für Lock-Konflikte da. Ein Fehler, der sich durch
// Warten nicht ändert — fehlende Berechtigung, defekte Datei, volle Platte —,
// soll den Aufrufer sofort erreichen.
//
// Ohne diese Unterscheidung drehte die Schleife auf einem schreibgeschützten
// Volume zehnmal, wartete eine halbe Sekunde und meldete dann „nach 10
// Versuchen: … readonly". Das schickt den Betreiber auf die falsche Fährte:
// Er sucht eine Lock-Konkurrenz, während die Datei schlicht nicht schreibbar
// ist.
//
// Gemessen wird an der DAUER, nicht nur am Fehler: Ein Test, der bloss
// „Fehler kam zurück" prüft, wäre mit und ohne Fix grün.
func TestJournalModusAufWAL_MeldetNichtWiederholbaresSofort(t *testing.T) {
	ctx := context.Background()
	verzeichnis := t.TempDir()
	dbPath := filepath.Join(verzeichnis, "aboard.db")

	// Datei im DELETE-Modus anlegen, dann schreibgeschützt machen — der
	// Modus-Wechsel scheitert dann an der Berechtigung, nicht an einem Lock.
	vorab, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("Vorbereitung: %v", err)
	}
	if _, err := vorab.ExecContext(ctx, "CREATE TABLE platzhalter (a INTEGER)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	vorab.Close()
	if err := os.Chmod(verzeichnis, 0o555); err != nil {
		t.Fatalf("Chmod Verzeichnis: %v", err)
	}
	if err := os.Chmod(dbPath, 0o444); err != nil {
		t.Fatalf("Chmod Datei: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(verzeichnis, 0o755)
		_ = os.Chmod(dbPath, 0o644)
	})

	db, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	beginn := time.Now()
	err = journalModusAufWAL(ctx, db)
	dauer := time.Since(beginn)

	if err == nil {
		t.Skip("Umgebung erlaubt den Modus-Wechsel trotz Schreibschutz — der Test " +
			"kann hier nichts aussagen (z. B. als root)")
	}
	maximal := walVersuche * walWartezeit
	if dauer >= maximal {
		t.Errorf("journalModusAufWAL brauchte %v (Schleife bis %v ausgereizt) — ein "+
			"nicht wiederholbarer Fehler wurde wie ein Lock-Konflikt behandelt: %v",
			dauer, maximal, err)
	}
}
