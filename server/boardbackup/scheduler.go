package boardbackup

// scheduler.go — Periodische Backup-Goroutine (#303, ADR-0098).
//
// Cron-Strategie (Stdlib-only, ADR-0098):
//   Wir vermeiden eine externe Cron-Lib (siehe go.mod-Konvention: Stdlib-
//   only — vgl. server/mail/smtp.go-Header). Statt dessen:
//   - `time.Ticker` 1 × pro Minute wecken.
//   - In jedem Tick prüfen, ob das aktuelle Cron-Pattern matched.
//   - Match → Backup-Lauf über alle Boards, gefolgt von Rotation.
//
// Das ist robust genug für tägliche Backups (Worst-Case-Latenz: 60 s) und
// vermeidet eine neue Dependency. Bei feingranulareren Anforderungen kann
// die Cron-Erweiterung (`0 */15 * * *` etc.) später nachgezogen werden.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// SchedulerDeps bündelt die Abhängigkeiten der Goroutine.
type SchedulerDeps struct {
	Boards             *storage.BoardRepo
	ACL                *storage.AclRepo
	AttachmentsBaseDir string
	BackupBaseDir      string
	KeepDays           int
	CronPattern        string // 5-Felder-Form, z. B. "0 2 * * *"
	AboardVersion      string
	Logger             func(string, ...any)
	// Crypter verschlüsselt die nächtlichen Backups (#336). nil =
	// unverschlüsselt. Wird in main.go aus Config + ABOARD_SECRET_KEY
	// aufgelöst und beim Reload neu gesetzt.
	Crypter *Crypter
	// EncryptionRequired spiegelt `backup_board.encryption_enabled`.
	//
	// WARUM DAS FELD EXISTIERT (Sprint-58-Kritiker, K4): Ohne es hing die
	// Zusage „kein stiller Rückfall auf Klartext" allein daran, dass der
	// Aufrufer in `main.go` vorher `ResolveCrypter` ruft und den Scheduler
	// sonst gar nicht erst startet. Wer `SchedulerDeps{Crypter: nil}` baut,
	// während die Verschlüsselung eingeschaltet ist, bekam lautlos
	// Klartext-Archive — ohne Wächter im Paket und ohne Test.
	//
	// Das ist punktgenau die Klasse von F-699: ein Schritt, den nur der
	// Aufrufer tut. Der Helfer schuldet Vollständigkeit gegenüber seinem
	// Gegenstand, nicht gegenüber seinem heutigen Aufrufer.
	EncryptionRequired bool
	// NowFn ist injizierbar für Tests; nil → time.Now.
	NowFn func() time.Time
	// OnFailure ist ein optionaler Callback, der nach jedem fehlgeschlagenen
	// Board-Backup aufgerufen wird. Wird vom Notification-Subsystem (#305)
	// genutzt, um system.backup-failed-Events an Admins zu emittieren.
	// Niemals blockierend nutzen — wird im Backup-Worker-Pfad gerufen.
	OnFailure func(boardID, boardTitle string, err error)
}

// nowFunc liefert die aktuelle Zeit; Tests können ihn überschreiben.
var nowFunc = func() time.Time { return time.Now() }

// SetNowFunc setzt eine alternative Zeit-Quelle (für Tests).
func SetNowFunc(fn func() time.Time) {
	if fn != nil {
		nowFunc = fn
	}
}

// jetzt ist die Zeit-Quelle DIESER Scheduler-Instanz: `NowFn`, sonst die
// globale `nowFunc`.
//
// F-716 (d): Der Cron-Treffer las bis Sprint 61 `deps.NowFn`, der
// Dateizeitstempel in `runBackupAllBoards` dagegen die globale `nowFunc` —
// zwei Uhren für einen Lauf. Wer den Scheduler per `NowFn` auf eine feste
// Zeit stellte (der dokumentierte Weg für Test-Isolation pro Instanz),
// bekam einen Treffer um 02:00 und ein Archiv mit der echten Wanduhr im
// Namen. Beide Stellen fragen jetzt dieselbe Quelle.
func (d SchedulerDeps) jetzt() time.Time {
	if d.NowFn != nil {
		return d.NowFn()
	}
	return nowFunc()
}

// RunScheduler startet die periodische Backup-Goroutine und blockiert,
// bis ctx.Done() schließt. Wird üblicherweise in einer eigenen goroutine
// aus main.go gestartet.
func RunScheduler(ctx context.Context, deps SchedulerDeps) error {
	if deps.Boards == nil || deps.ACL == nil {
		return fmt.Errorf("scheduler: Boards/ACL nil")
	}
	if deps.BackupBaseDir == "" {
		return fmt.Errorf("scheduler: BackupBaseDir leer")
	}
	if deps.CronPattern == "" {
		deps.CronPattern = "0 2 * * *"
	}
	if deps.KeepDays <= 0 {
		deps.KeepDays = 7
	}
	if deps.Logger == nil {
		deps.Logger = log.Printf
	}

	deps.Logger("aboard-server: board-backup-scheduler gestartet (cron=%q, keep=%d, dir=%q)",
		deps.CronPattern, deps.KeepDays, deps.BackupBaseDir)

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Vermeide Doppel-Lauf im selben Minutenfenster (Edge-Case bei Clock-Jumps).
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			now := deps.jetzt()
			if !cronMatches(deps.CronPattern, now) {
				continue
			}
			// Truncate auf Minute — verhindert Doppel-Lauf innerhalb dieser Minute.
			minute := now.Truncate(time.Minute)
			if !lastRun.IsZero() && lastRun.Equal(minute) {
				continue
			}
			lastRun = minute
			_, _ = RunAllBoardsGuarded(ctx, deps)
		}
	}
}

// BackupAllBoardsOnce führt genau einen Sicherungslauf über alle Boards aus —
// dasselbe, was der Scheduler bei jedem Cron-Treffer tut, nur ohne Ticker.
//
// Exportiert seit Sprint 58 (F-699), weil der Lauf sonst NICHT PRÜFBAR ist:
// Er steckte in einer nicht exportierten Funktion hinter einem
// 60-Sekunden-Ticker, und genau darin ist das fehlende Umbenennen vier
// Sprints lang unbemerkt geblieben. Ein Pfad, den kein Test in vertretbarer
// Zeit erreicht, ist ein Pfad ohne Wächter.
func BackupAllBoardsOnce(ctx context.Context, deps SchedulerDeps) {
	_, _ = RunAllBoardsGuarded(ctx, deps)
}

// ErrAllLaeuft: es läuft bereits ein Sicherungslauf über alle Boards
// (Scheduler ODER Admin-Knopf). Der zweite Aufruf startet nichts (#735).
var ErrAllLaeuft = errors.New("boardbackup: ein Sicherungslauf über alle Boards läuft bereits")

// AllErgebnis ist das Ergebnis eines Laufs über alle Boards.
type AllErgebnis struct {
	Gesamt  int       `json:"total"`
	OK      int       `json:"ok"`
	Fehler  int       `json:"failed"`
	Start   time.Time `json:"started"`
	Ende    time.Time `json:"finished"`
	Meldung string    `json:"error,omitempty"` // Lauf als Ganzes gescheitert (Liste/Schlüssel) — deutscher Freitext, nur Rückfall
	// Code ist die maschinenlesbare Ursache zu Meldung ("list" | "key" |
	// "abort" | "panic"); der Client übersetzt sie in die Oberflächensprache
	// (K-8, Sprint 85) und zeigt Meldung nur, wenn er den Code nicht kennt.
	Code string `json:"code,omitempty"`
}

var (
	allMu      sync.Mutex
	allLaeuft  bool
	allLetztes *AllErgebnis
)

// AllStatus liefert, ob gerade ein Lauf aktiv ist, und das letzte Ergebnis
// (nil, solange seit Prozessstart keiner fertig wurde).
func AllStatus() (bool, *AllErgebnis) {
	allMu.Lock()
	defer allMu.Unlock()
	if allLetztes == nil {
		return allLaeuft, nil
	}
	k := *allLetztes
	return allLaeuft, &k
}

// ReserviereAll setzt die Sperre ("läuft"). Gelingt das nicht, läuft schon
// ein Lauf → false. Wer true bekommt, MUSS RunAllBoardsReserviert rufen.
func ReserviereAll() bool {
	allMu.Lock()
	defer allMu.Unlock()
	if allLaeuft {
		return false
	}
	allLaeuft = true
	return true
}

// RunAllBoardsReserviert führt den Lauf aus und gibt die Sperre am Ende frei.
// Voraussetzung: ReserviereAll() war true.
//
// Panik (K-7, Sprint 85): Sie wird HIER abgefangen und geloggt, der Lauf endet
// mit Code "panic" im Status. Ohne das beendete eine Panik in der Goroutine
// des Knopf-Laufs den ganzen Prozess (ein defer gibt zwar die Sperre frei,
// fängt aber nichts ab — der frühere Kommentar „auch bei Panik“ war nur für
// die Sperre wahr, nicht für den Prozess).
func RunAllBoardsReserviert(ctx context.Context, deps SchedulerDeps) (erg AllErgebnis) {
	erg = AllErgebnis{Start: deps.jetzt().UTC()}
	defer func() {
		if rec := recover(); rec != nil {
			/* Bewusst log.Printf, nicht deps.Logger: Der Logger kann selbst
			   die Panik ausgelöst haben. %v statt Stack — keine Pfade/Daten. */
			log.Printf("board-backup: PANIK im Lauf über alle Boards abgefangen: %v", rec)
			erg.Meldung = "Interner Fehler im Sicherungslauf (Panik) — Serverlog ansehen"
			erg.Code = "panic"
		}
		allMu.Lock()
		erg.Ende = deps.jetzt().UTC()
		k := erg
		allLetztes = &k
		allLaeuft = false
		allMu.Unlock()
	}()
	erg.Gesamt, erg.OK, erg.Fehler, erg.Meldung, erg.Code = runBackupAllBoards(ctx, deps)
	return erg
}

// RunAllBoardsGuarded: Sperre nehmen + laufen; ErrAllLaeuft, wenn belegt.
func RunAllBoardsGuarded(ctx context.Context, deps SchedulerDeps) (AllErgebnis, error) {
	if !ReserviereAll() {
		if deps.Logger != nil {
			deps.Logger("board-backup: Lauf über alle Boards übersprungen — es läuft bereits einer")
		}
		return AllErgebnis{}, ErrAllLaeuft
	}
	return RunAllBoardsReserviert(ctx, deps), nil
}

// runBackupAllBoards iteriert über alle Boards und schreibt ein Backup +
// Rotation pro Board. Fehler pro Board werden geloggt, brechen den Lauf
// aber nicht ab — andere Boards sollen nicht von einem defekten profitieren.
func runBackupAllBoards(ctx context.Context, deps SchedulerDeps) (gesamt, ok, fehler int, meldung, code string) {
	if ctx.Err() != nil { // K-5: schon beim Start abgebrochen (Herunterfahren oder Neukonfiguration)
		deps.Logger("board-backup: Lauf abgebrochen (Server fährt herunter oder Sicherung neu konfiguriert), bevor er begann")
		return 0, 0, 0, "Lauf abgebrochen", "abort"
	}
	boards, err := deps.Boards.List(ctx)
	if err != nil {
		deps.Logger("board-backup: list-boards: %v", err)
		return 0, 0, 0, "Board-Liste nicht lesbar", "list"
	}
	gesamt = len(boards)
	// K4: Verlangt die Konfiguration Verschlüsselung, aber es liegt kein
	// Crypter vor, wird NICHTS geschrieben. Ein unverschlüsseltes Backup
	// wäre eine stille Herabstufung des Schutzniveaus — genau das, was der
	// Operator mit dem Schalter ausschliessen wollte.
	if deps.EncryptionRequired && deps.Crypter == nil {
		deps.Logger("board-backup: encryption_enabled=true, aber kein Schlüssel — " +
			"KEIN Backup geschrieben (kein Rückfall auf Klartext)")
		for _, s := range boards {
			if deps.OnFailure != nil {
				deps.OnFailure(s.ID, s.Title, ErrEncryptionKeyMissing)
			}
		}
		return gesamt, 0, gesamt, "Verschlüsselung verlangt, aber kein Schlüssel", "key"
	}

	for _, s := range boards {
		/* K-5 (Sprint 85): Wird der Kontext abgebrochen, startet kein weiteres
		   Board mehr — sonst läuft der Lauf in geschlossene Datenbank und
		   erzeugt eine Fehlerflut. Keine Rotation und kein Verwaisten-Abräumen
		   nach einem Abbruch.
		   Zwei Auslöser, nicht einer (Verify Sprint 85, F-1823): Herunterfahren
		   UND der Reload der Sektion backup_board (main.go: boardBackupStop()
		   bricht den Scheduler-Kontext ab, z. B. beim Umschalten der
		   Verschlüsselung). Die Meldung nennt deshalb beide und die Zahl der
		   übersprungenen Boards; sie steht als Code "abort" im Status, den der
		   Admin-Dialog zeigt. Keine OnFailure je Board: beim Herunterfahren
		   ginge die Benachrichtigung in ein schliessendes System, und der
		   nächste Cron-Lauf holt die Boards nach. */
		if ctx.Err() != nil {
			deps.Logger("board-backup: Lauf abgebrochen (Server fährt herunter oder Sicherung neu konfiguriert) nach %d von %d Boards — %d nicht gesichert", ok+fehler, gesamt, gesamt-ok-fehler)
			return gesamt, ok, fehler, "Lauf abgebrochen", "abort"
		}
		ts := deps.jetzt().UTC()
		fn, err := ReservePendingPath(deps.BackupBaseDir, s.ID, ts)
		if err != nil {
			deps.Logger("board-backup: board=%s: Zwischenname reservieren: %v", s.ID, err)
			if deps.OnFailure != nil {
				deps.OnFailure(s.ID, s.Title, err)
			}
			fehler++
			continue
		}
		res, err := BackupBoard(ctx, Options{
			BoardID:            s.ID,
			Boards:             deps.Boards,
			ACL:                deps.ACL,
			AttachmentsBaseDir: deps.AttachmentsBaseDir,
			Out:                fn,
			AboardVersion:      deps.AboardVersion,
			Crypter:            deps.Crypter,
		})
		if err != nil {
			// Der per ReservePendingPath angelegte Platzhalter wird nie
			// beschrieben, wenn BackupBoard fehlschlägt — aufräumen, sonst
			// bleibt eine leere -pending-Leiche liegen (F-710-Nacharbeit).
			_ = os.Remove(fn)
			deps.Logger("board-backup: board=%s: %v", s.ID, err)
			if deps.OnFailure != nil {
				deps.OnFailure(s.ID, s.Title, err)
			}
			fehler++
			continue
		}
		// Bei Verschlüsselung schreibt BackupBoard unter fn+EncSuffix
		// (res.Path), der leere Platzhalter unter fn selbst bleibt sonst
		// als Leiche liegen (F-710-Nacharbeit).
		if res.Path != fn {
			_ = os.Remove(fn)
		}
		// F-699: Umbenennen auf den endgültigen Namen. `makeFilename`
		// schreibt bewusst unter `-pending`, damit eine abgebrochene
		// Sicherung nicht wie eine fertige aussieht; erst nach dem
		// erfolgreichen Lauf steht der SHA fest, der in den Namen gehört.
		// Dieser Schritt fehlte bis Sprint 58 — die nächtlichen Backups
		// hiessen dauerhaft `…-pending.tar.gz`, obwohl der Kommentar an
		// makeFilename das Gegenteil behauptete. Aufgefallen ist es erst,
		// als Karte #335 diese Liste dem Board-Eigentümer zeigte.
		endName := FormatFilename(s.ID, ts, res.SHA256)
		if res.Encrypted {
			endName += EncSuffix
		}
		endPfad := filepath.Join(deps.BackupBaseDir, s.ID, endName)
		if err := os.Rename(res.Path, endPfad); err != nil {
			// Nicht fatal: die Sicherung liegt vollständig auf der Platte,
			// nur unter dem Zwischennamen. Rotation und Liste erfassen sie
			// trotzdem (beide filtern über die Endung, nicht über den
			// Mittelteil).
			deps.Logger("board-backup: umbenennen board=%s: %v", s.ID, err)
		} else {
			res.Filename = endName
			res.Path = endPfad
		}

		deleted, rerr := Rotate(deps.BackupBaseDir, s.ID, deps.KeepDays)
		if rerr != nil {
			deps.Logger("board-backup: rotate board=%s: %v", s.ID, rerr)
		}
		deps.Logger("board-backup: board=%s file=%s size=%d sha=%s verschlüsselt=%t (rotated=%d)",
			s.ID, res.Filename, res.Size, shortSHA(res.SHA256), res.Encrypted, deleted)
		ok++
	}

	// F-1125: Backups gelöschter Boards nach Ablauf der Frist abräumen
	// (verwaist.go). Bewusst erst NACH einem vollständigen Lauf und nur mit
	// der Board-Liste, die gerade erfolgreich gelesen wurde — bricht der Lauf
	// oben ab (Liste unlesbar, Schlüssel fehlt), wird auch nichts abgeräumt.
	vorhanden := make(map[string]bool, len(boards))
	for _, s := range boards {
		vorhanden[s.ID] = true
	}
	raeumeVerwaisteAb(ctx, deps, vorhanden)
	return gesamt, ok, fehler, "", ""
}

// ReservePendingPath reserviert ATOMAR einen eindeutigen ZWISCHEN-Pfad für
// ein laufendes Board-Backup:
//
//	boardbackup-<boardId>-<YYYYMMDD-HHMMSS>-<zufall>-pending.tar.gz
//
// Der endgültige Name trägt statt `pending` die ersten acht Zeichen des
// SHA-256 (FormatFilename) — der steht erst fest, wenn das Archiv
// geschrieben ist. Das Umbenennen macht der Aufrufer; `ts` wird ihm deshalb
// hereingereicht statt hier erzeugt, damit beide Namen denselben Zeitstempel
// tragen (F-699: vorher rief diese Funktion `nowFunc()` selbst, und ein
// Aufrufer hätte für den Endnamen zwangsläufig eine zweite, minimal andere
// Zeit bekommen).
//
// WARUM ATOMAR (F-710, Sprint-58-Kritiker): Diese Funktion hiess vorher
// makeFilename und baute den Pfad rein aus Board-ID + Sekunden-Zeitstempel
// — ein reiner String-Bau ohne Reservierung. Der REST-Endpunkt
// (server/api/boardbackup.go) bildete unabhängig davon denselben Namen für
// dieselbe Formel. Trafen der nächtliche Scheduler-Lauf und ein
// "Jetzt sichern"-Klick — oder zwei Owner/Admins gleichzeitig, das
// Rate-Limit ist pro User — in derselben Sekunde für dasselbe Board
// zusammen, wählten beide denselben Pfad; BackupBoard öffnet ihn mit
// os.Create (truncate), der zweite Schreiber überschreibt den ersten
// mitten im Schreibvorgang. os.CreateTemp reserviert den Namen dagegen via
// O_EXCL beim Betriebssystem — kein zweiter Aufrufer kann denselben Pfad
// bekommen, selbst bei exakt gleicher Zeit, weil der von CreateTemp
// angehängte Zufalls-Suffix ihn eindeutig macht.
func ReservePendingPath(baseDir, boardID string, ts time.Time) (string, error) {
	dir := filepath.Join(baseDir, boardID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("boardbackup: Zielverzeichnis anlegen: %w", err)
	}
	pattern := "boardbackup-" + boardID + "-" + ts.UTC().Format("20060102-150405") + "-*-pending.tar.gz"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("boardbackup: Zwischenname reservieren: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("boardbackup: Zwischenname schliessen: %w", err)
	}
	return path, nil
}

// shortSHA gibt die ersten 8 Hex-Zeichen zurück (für Log-Ausgaben).
func shortSHA(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

// cronMatches prüft, ob das 5-Felder-Cron-Pattern auf die gegebene Zeit passt.
// Felder: minute, hour, day-of-month, month, day-of-week.
// Unterstützte Notationen:
//   - "*"   → jeder Wert
//   - "5"   → exakt 5
//   - "*/15" → alle 15 (modulo)
//   - "1,3,5" → Liste
//   - "1-5" → Bereich
//
// Bewusst minimal — wir brauchen für #303 hauptsächlich "0 2 * * *".
// Erweiterungen sind orthogonal nachrüstbar.
func cronMatches(pattern string, t time.Time) bool {
	fields := strings.Fields(pattern)
	if len(fields) != 5 {
		return false // unparsbar → niemals match (sicherer Default)
	}
	if !cronFieldMatches(fields[0], t.Minute(), 0, 59) {
		return false
	}
	if !cronFieldMatches(fields[1], t.Hour(), 0, 23) {
		return false
	}
	if !cronFieldMatches(fields[2], t.Day(), 1, 31) {
		return false
	}
	if !cronFieldMatches(fields[3], int(t.Month()), 1, 12) {
		return false
	}
	if !cronFieldMatches(fields[4], int(t.Weekday()), 0, 6) {
		return false
	}
	return true
}

// cronFieldMatches parsst ein einzelnes Cron-Feld und vergleicht es mit val.
func cronFieldMatches(field string, val, min, max int) bool {
	field = strings.TrimSpace(field)
	if field == "*" || field == "" {
		return true
	}
	// Step-Notation: */N oder a-b/N
	if strings.Contains(field, "/") {
		parts := strings.SplitN(field, "/", 2)
		step, err := strconv.Atoi(parts[1])
		if err != nil || step <= 0 {
			return false
		}
		// Range vor /
		rangeStart, rangeEnd := min, max
		if parts[0] != "*" {
			if strings.Contains(parts[0], "-") {
				rp := strings.SplitN(parts[0], "-", 2)
				if a, err := strconv.Atoi(rp[0]); err == nil {
					rangeStart = a
				}
				if b, err := strconv.Atoi(rp[1]); err == nil {
					rangeEnd = b
				}
			} else {
				if a, err := strconv.Atoi(parts[0]); err == nil {
					rangeStart = a
					rangeEnd = a
				}
			}
		}
		if val < rangeStart || val > rangeEnd {
			return false
		}
		return (val-rangeStart)%step == 0
	}
	// Listen-Notation: a,b,c
	if strings.Contains(field, ",") {
		for _, p := range strings.Split(field, ",") {
			if cronFieldMatches(p, val, min, max) {
				return true
			}
		}
		return false
	}
	// Range-Notation: a-b
	if strings.Contains(field, "-") {
		parts := strings.SplitN(field, "-", 2)
		a, errA := strconv.Atoi(parts[0])
		b, errB := strconv.Atoi(parts[1])
		if errA != nil || errB != nil {
			return false
		}
		return val >= a && val <= b
	}
	// Exakter Wert
	n, err := strconv.Atoi(field)
	if err != nil {
		return false
	}
	return val == n
}

// startSchedulerOnce ist ein Wrapper für main.go, der RunScheduler in einer
// goroutine startet und einen Cancel-Closure zurückgibt. Wird beim Reload
// genutzt: alter Scheduler stoppen, neuer mit aktualisierter Config starten.
func StartSchedulerOnce(parent context.Context, deps SchedulerDeps) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := RunScheduler(ctx, deps); err != nil {
			if deps.Logger != nil {
				deps.Logger("board-backup-scheduler beendet: %v", err)
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
