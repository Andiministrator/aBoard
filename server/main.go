package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/backup"
	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/notifications"
	"github.com/Andiministrator/aBoard/server/retention"
	"github.com/Andiministrator/aBoard/server/schleuse"
	servicepkg "github.com/Andiministrator/aBoard/server/serviceboard"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
	"github.com/Andiministrator/aBoard/server/web"

	"github.com/google/uuid"
)

const (
	defaultDBPath         = "/data/db/aboard.db"
	defaultAttachmentsDir = "/data/attachments"
	defaultListenAddr     = ":8080"
	defaultMaxAttachBytes = 50 * 1024 * 1024 // 50 MB

	// httpShutdownTimeout und notifShutdownTimeout sind ZWEI SEQUENZIELLE
	// Budgets, keine geteilte Obergrenze (F-729, Karte #509). Bis Sprint 67
	// behauptete ein Kommentar hier, der Notification-Worker-Shutdown binde
	// an "dieselbe Obergrenze wie den HTTP-Shutdown" — tatsächlich bekam
	// jede Phase ihre EIGENEN vollen 10 s, macht 20 s Worst-Case, ohne dass
	// irgendwo ein `stop_grace_period` gesetzt war (Docker-Default 10 s) —
	// der Prozess konnte also per SIGKILL mitten im Worker-Shutdown sterben.
	//
	// Fix: zwei benannte, bewusst UNGLEICHE Konstanten statt einer
	// wiederverwendeten. notifShutdownTimeout muss echt größer sein als
	// notifications.SendTimeout (15 s, siehe server/notifications/config.go)
	// — sonst passt nicht einmal ein einzelner, gerade erst gestarteter
	// Sendeversuch ins Budget und Shutdown() (worker.go) meldet auch im
	// Best-Case einen Timeout statt eines sauberen Endes.
	//
	// Worst-Case-Summe für den gesamten Prozess-Exit:
	// httpShutdownTimeout + notifShutdownTimeout = 10 s + 20 s = 30 s.
	// docker-compose.yml setzt `stop_grace_period: 40s` auf genau diese
	// Rechnung (30 s + Marge) — kleiner als die Summe würde das Problem nur
	// verschieben, nicht lösen.
	httpShutdownTimeout  = 10 * time.Second
	notifShutdownTimeout = 20 * time.Second
)

// aboardVersion ist die Version, die in Backup-Metadaten und im Admin-Status
// erscheint. Karte #347: leitet sich aus api.BuildVersion ab, statt eine eigene
// -ldflags-Variable zu sein.
//
// Vorher stand hier `var aboardVersion = "dev"` mit dem Hinweis, man könne sie
// per `-X main.aboardVersion=...` setzen — nur tat das niemand: Dockerfile und
// CI setzen ausschliesslich api.BuildVersion. Folge: jedes ausgelieferte
// Backup trug "dev" als Erzeuger-Version, und der Admin-Status-Tab zeigte
// dasselbe. Zwei Variablen für eine Tatsache sind eine Drift-Quelle; jetzt
// gibt es nur noch eine Quelle (api.BuildVersion, siehe server/api/health.go).
//
// ACHTUNG (Sprint-53-Kritiker): `-X main.aboardVersion=…` wirkt seit dieser
// Änderung NICHT mehr — der Linker kann nur Variablen mit konstantem
// Initialisierer setzen, und er meldet das nicht. Ein altes Build-Skript mit
// der Flag baut ab sofort still eine "dev"-Binary. Ausschliesslich
// `-X github.com/Andiministrator/aBoard/server/api.BuildVersion=…` setzen (so macht
// es server/Dockerfile). Die Initialisierungsreihenfolge ist dabei zuverlässig:
// `api` wird vor `main` initialisiert, und `-X` schreibt vor jedem Init.
var aboardVersion = api.BuildVersion

// llmUserAgent identifiziert den aBoard-Server gegenüber externen
// LLM-Providern. Bewusst OHNE Versionsangabe (Karte #394, F-593).
//
// Vorgeschichte: Hier stand "aboard-server/"+aboardVersion. Solange
// aboardVersion auf "dev" festhing (bis Karte #347), fiel das nicht auf —
// seither ginge die exakte Patch-Version dieses Servers an Anthropic,
// OpenAI, Google und Mistral, ohne dass das irgendwo dokumentiert wäre.
//
// Abgewogen (Claude Opus 5, Sprint 54, unter Freigabe des Maintainers, Entscheidungen
// dieser Grössenordnung selbst zu treffen): Der Diagnosewert beim Provider
// ist für uns gering — wir lesen dessen Telemetrie nicht, und für die eigene
// Fehlersuche steht die Version an fünf besseren Stellen (/healthz,
// Startlog, Sidebar-Fuss, Admin-Status, Backup-Metadaten). Dem steht
// gegenüber, dass eine exakte Patch-Version einem Dritten verrät, welche
// bekannten Fehler ein Server noch hat. Schlechter Tausch.
//
// Die "/1" ist eine Generationsangabe, keine Version, und folgt der
// Hausform von server/notifications/gotify.go ("aboard-notification/1").
// Sie ändert sich nur, wenn sich die Art der Nutzung grundlegend ändert.
const llmUserAgent = "aboard-server/1"

// defaultBoardSeed ist das kanonische, leere v2-Board, das auf einem frisch
// gestarteten Server (leere DB) angelegt wird (ADR-0054, Teil B / F-253).
//
// Bewusste, einzige Ausnahme von ADR-0044 (state_json opak): genau dieses eine
// Seed wird serverseitig erzeugt. Struktur 1:1 wie `Model.createBoard('Board')`
// im Frontend (src/model.js), nur mit fester id `default` statt UUID — so
// findet der Client „sein" Board über Reloads wieder (behebt L2) und
// `checkVersion(seed)` bleibt im Frontend ein valides Board (Test-abgesichert).
//
// ADR-0066/#225: Eine einzige Quelle der Wahrheit — sowohl dieses Default-
// Seed als auch der Personal-Board-Bootstrap (storage.BootstrapNewUser)
// erzeugen den State über storage.CanonicalEmptyBoardState (kein Drift
// zwischen zwei handgepflegten Seed-Kopien). Byte-Gleichheit zum bisherigen
// Literal ist via seed_test.go abgesichert.
var defaultBoardSeed = string(storage.CanonicalEmptyBoardState("default", "Board"))

func main() {
	// Karte #687 (Verify VS4-1): Kindprozess der PDF-Extraktion der
	// Ausgangsschleuse — kehrt in diesem Fall nie zurück. MUSS vor allem
	// anderen stehen: Der Kindprozess hat keine Konfiguration und soll nichts
	// öffnen, keinen Port, keine Datenbank.
	schleuse.PDFKindprozess()
	if len(os.Args) < 2 {
		if err := run(); err != nil {
			log.Fatalf("aboard-server: %v", err)
		}
		return
	}

	switch os.Args[1] {
	case "import":
		if err := runImport(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server import: %v", err)
		}
	case "backup":
		if err := runBackup(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server backup: %v", err)
		}
	case "restore":
		if err := runRestore(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server restore: %v", err)
		}
	case "agent-token":
		// ADR-0129 §3, Karte #553: Agenten-Tokens anlegen, auflisten,
		// widerrufen (main_agent.go).
		// Karte #608: `list --warn-days N` endet mit Exit 3, wenn ein aktives
		// Token bald abläuft — getrennt von echten Fehlern (Exit 1).
		if err := runAgentToken(os.Args[2:]); err != nil {
			if errors.Is(err, errAblaufWarnung) {
				fmt.Fprintf(os.Stderr, "aboard-server agent-token: %v\n", err)
				os.Exit(3)
			}
			log.Fatalf("aboard-server agent-token: %v", err)
		}
	case "acl":
		// F-1854: Board-Freigaben ohne HTTP-API setzen (main_acl.go).
		if err := runACL(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server acl: %v", err)
		}
	case "nutzer":
		// Karte #1052 (ADR-0148 §7): Konto auf andere Adresse umziehen (main_nutzer.go).
		if err := runNutzer(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server nutzer: %v", err)
		}
	case "boardpaar":
		// ADR-0140 §4, Karte #788: Board-Paar koppeln, entkoppeln, Zugänge
		// (main_boardpaar.go) — dasselbe wie der Admin-Reiter „Board-Paar“.
		if err := runBoardpaar(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server boardpaar: %v", err)
		}
	case "healthcheck":
		// ADR-0129 §5, Karte #542: rein lesender HTTP-Client gegen den
		// bereits laufenden Server (siehe healthcheck.go) — öffnet
		// bewusst NICHTS von dem, was run() sonst als Erstes tut (keine
		// DB, keine Migration, kein Listener).
		if err := runHealthcheck(os.Args[2:]); err != nil {
			log.Fatalf("aboard-server healthcheck: %v", err)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		// Kein bekannter Subcommand → Server starten (Rückwärtskompatibilität:
		// ein Startbefehl mit Zusatzwort soll einen Container nicht lahmlegen).
		// Ausnahme Tippfehler (F-1934, Kritiker K-1): `backp --out …` aus einem
		// Sicherungsskript startete sonst einen zweiten Server auf derselben DB.
		if err := pruefeUnbekanntenUnterbefehl(os.Args[1]); err != nil {
			log.Fatalf("aboard-server: %v", err)
		}
		log.Printf("aboard-server: Argumente %v sind kein Unterbefehl und werden ignoriert — Server startet", os.Args[1:])
		if err := run(); err != nil {
			log.Fatalf("aboard-server: %v", err)
		}
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `aboard-server — aBoard-Server-Binary

Subcommands:
  (kein Subcommand)           Server starten
  import [--findings-pruefung=aus] <datei.json>
                              aboard-sync.json in SQLite importieren
  backup --out <pfad>         Backup erstellen (.tar.gz mit DB + Attachments)
  restore [--dry-run|--force] <pfad.tar.gz>
                              Backup zurückspielen (Optionen VOR dem Pfad)
  healthcheck [--timeout <d>] GET /healthz gegen den laufenden Server prüfen
                              (nutzt ABOARD_UNIX_SOCKET/ABOARD_ADDR wie der
                              Server selbst; für den Docker-HEALTHCHECK)
  agent-token create --name <n> --board <id> [--role editor|viewer]
  agent-token list
  agent-token revoke --name <n>
                              Agenten-Tokens verwalten (ADR-0129 §3; --db wie
                              bei backup). Das Token erscheint nur bei create.
  acl orphans                 Boards ohne Owner auflisten
  acl adopt --email <adresse> <adresse> wird Owner aller Boards ohne Owner
  acl set --board <id> --email <adresse> --role owner|editor|viewer
                              Freigaben setzen (--db wie bei backup), z. B. nach
                              dem Umstieg auf den Mehrnutzer-Betrieb (F-1854)
  nutzer umziehen --von <alt> --nach <neu> [--dry-run]
                              Konto auf eine andere Adresse umziehen (Boards,
                              Rechte, Anmelde-Bindung; Sitzungen enden). Rückweg:
                              Adressen vertauschen. --db wie bei backup (#1052)
  boardpaar liste [--json]
  boardpaar seite [--setzen aus|innen|aussen]
  boardpaar koppeln --seite innen --board <id>
  boardpaar koppeln --seite aussen --board <id> --link https://<innen>/#board=<id>
  boardpaar entkoppeln --seite innen|aussen --board <id>
  boardpaar token-neu|token-widerrufen --typ abs|abe|abr|abb --name <n> [--darf-anlegen]
  boardpaar kopie-anlegen --board <id> --ziel <name> [--link https://<innen>/#board=<id>]
                              Board-Paar intern/extern (ADR-0140 §4; --db wie
                              bei backup). Das Token erscheint nur bei token-neu.

Backup-Optionen:
  --out <pfad>                Zieldatei (.tar.gz) oder Verzeichnis (Rolling-Modus)
  --db <pfad>                 DB-Pfad (Standard: ABOARD_DB / %s)
  --attachments <pfad>        Attachments-Verzeichnis (Standard: ABOARD_ATTACHMENTS_DIR / %s)
  --keep-daily <n>            Rolling: behalte n jüngste Tages-Archive (nur im Verzeichnis-Modus)
  --keep-weekly <m>           Rolling: behalte m zusätzliche Wochen-Archive (Montage)

Restore-Optionen (alle VOR dem Archiv-Pfad):
  --db <pfad>                 Ziel-DB-Pfad
  --attachments <pfad>        Ziel-Attachments-Verzeichnis
  --dry-run                   Nur anzeigen was passieren würde, nicht schreiben
  --force                     Existierende DB/Attachments überschreiben
  <pfad.tar.gz>               Archiv-Pfad (Pflicht, als letztes Argument)

Umgebungsvariablen:
  ABOARD_DB                   Pfad zur SQLite-Datenbank
  ABOARD_ATTACHMENTS_DIR      Pfad zum Attachments-Verzeichnis
  ABOARD_ADDR                 Lausch-Adresse des Servers (Standard: :8080)
  ABOARD_UNIX_SOCKET          Pfad zu einem Unix-Domain-Socket (überschreibt ABOARD_ADDR)
  ABOARD_UNIX_SOCKET_MODE     Modus der Unix-Sockets: 0600, 0660 (Standard) oder 0666
  ABOARD_UHR_PRUEFUNG         an (Standard): nach Frist nur löschen, wenn der Kernel die Uhr als
                              synchronisiert meldet; aus: ohne diese Prüfung (nur ohne Zeitdienst)
  ABOARD_TRUST_FORWARD_AUTH   Optionale Forward-Auth-Header-Trust-Schicht aktivieren (1 = an)
  ABOARD_AUTH_MODE            builtin: eingebaute Anmeldung (statt Forward-Auth); gleichbedeutend
                              ABOARD_ANMELDUNG=oidc. Dazu ABOARD_PUBLIC_BASE_URL (https), ABOARD_SECRET_KEY
                              und mindestens ein OIDC-Anbieter:
                                ABOARD_OIDC_ISSUER/_CLIENT_ID/_CLIENT_SECRET (Anbieter „standard“) und/oder
                                ABOARD_OIDC_PROVIDERS=google,authelia mit ABOARD_OIDC_<ID>_ISSUER,
                                _CLIENT_ID, _CLIENT_SECRET, _LABEL je Kürzel
  ABOARD_AUTH_METHODS         angebotene Anmeldewege (Kürzel der Anbieter); leer = alle eingerichteten
  ABOARD_OIDC_ALLOWED         Vorab-Liste (Adressen, @domain); gleichbedeutend ABOARD_OIDC_ZUGELASSEN
  ABOARD_BOOTSTRAP_OWNER      E-Mail des ersten Owners: Default-Board nur bei leerer DB;
                              Service-Board bei JEDEM Start, solange gesetzt.
  ABOARD_DEV_FAKE_USER        Dev-Modus: Remote-User-Header simulieren (nur mit ABOARD_TRUST_FORWARD_AUTH)
  ABOARD_DEV_FAKE_EMAIL       Dev-Modus: Remote-Email-Header simulieren
  ABOARD_DEV_FAKE_GROUPS      Dev-Modus: Remote-Groups-Header simulieren
  ABOARD_DEV_FAKE_NAME        Dev-Modus: Remote-Name-Header simulieren (Anzeigename)

Das ist eine Auswahl. Alle Umgebungsvariablen: docs/handbuch/server/konfiguration.md
`, defaultDBPath, defaultAttachmentsDir)
}

// runBackup verarbeitet den "backup"-Subcommand.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("out", "", "Zieldatei (.tar.gz) oder Verzeichnis für Rolling-Modus (Pflicht)")
	dbPath := fs.String("db", "", "Pfad zur SQLite-Datenbank (Standard: ABOARD_DB)")
	attachDir := fs.String("attachments", "", "Pfad zum Attachments-Verzeichnis (Standard: ABOARD_ATTACHMENTS_DIR)")
	keepDaily := fs.Int("keep-daily", 0, "Rolling: jüngste N Tages-Archive behalten")
	keepWeekly := fs.Int("keep-weekly", 0, "Rolling: N zusätzliche Wochen-Archive (Montage) behalten")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Verwendung: aboard-server backup --out <pfad> [Optionen]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := keineRestArgumente(fs, 0); err != nil {
		return err
	}

	if *out == "" {
		fs.Usage()
		return fmt.Errorf("--out ist erforderlich")
	}

	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	resolvedAttach := envOrDefault("ABOARD_ATTACHMENTS_DIR", defaultAttachmentsDir)
	if *attachDir != "" {
		resolvedAttach = *attachDir
	}

	archivePath, err := backup.Run(context.Background(), backup.Options{
		DBPath:         resolvedDB,
		AttachmentsDir: resolvedAttach,
		Out:            *out,
		AboardVersion:  aboardVersion,
		KeepDaily:      *keepDaily,
		KeepWeekly:     *keepWeekly,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Backup erstellt: %s\n", archivePath)
	return nil
}

// runRestore verarbeitet den "restore"-Subcommand.
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dbPath := fs.String("db", "", "Ziel-DB-Pfad (Standard: ABOARD_DB)")
	attachDir := fs.String("attachments", "", "Ziel-Attachments-Verzeichnis (Standard: ABOARD_ATTACHMENTS_DIR)")
	dryRun := fs.Bool("dry-run", false, "Zeigt an was passieren würde, schreibt nichts")
	force := fs.Bool("force", false, "Existierende DB/Attachments überschreiben")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Verwendung: aboard-server restore [Optionen] <pfad.tar.gz>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("Archiv-Pfad ist erforderlich")
	}
	// F-1934: Go `flag` hört am ersten Positionsargument auf. Ohne diese
	// Prüfung liefe `restore --force <archiv> --dry-run` als echtes
	// Zurückspielen — das `--dry-run` dahinter wäre still verloren.
	if err := keineRestArgumente(fs, 1); err != nil {
		return err
	}
	archivePath := fs.Arg(0)

	resolvedDB := envOrDefault("ABOARD_DB", defaultDBPath)
	if *dbPath != "" {
		resolvedDB = *dbPath
	}
	resolvedAttach := envOrDefault("ABOARD_ATTACHMENTS_DIR", defaultAttachmentsDir)
	if *attachDir != "" {
		resolvedAttach = *attachDir
	}

	// Schema-Version des laufenden Servers ermitteln (aus der aktuellen DB wenn vorhanden).
	maxSchemaVersion := currentSchemaVersion(resolvedDB)

	return backup.Restore(context.Background(), backup.RestoreOptions{
		ArchivePath:      archivePath,
		DBPath:           resolvedDB,
		AttachmentsDir:   resolvedAttach,
		DryRun:           *dryRun,
		Force:            *force,
		MaxSchemaVersion: maxSchemaVersion,
	})
}

// wissenServerOrdner: die Ordner, die wissen.dir nicht treffen darf
// (F-1887) — der Anhang-Ordner. Den Ordner der Datenbank bewusst NICHT
// (Verify V-1, F-1893): Liegt die DB direkt unter /data, sperrte er jeden
// Wissensordner unter /data, und dort liegt kein .md. Deshalb nimmt die
// Funktion den DB-Pfad gar nicht erst entgegen (F-1899, Kritiker
// Sprint-87-Abschluss K-3).
func wissenServerOrdner(attachDir string) []string {
	return []string{attachDir}
}

// currentSchemaVersion liest die Schema-Version (PRAGMA user_version) der
// Server-DB OHNE Seiteneffekte. Existiert die Datei nicht oder ist sie nicht
// lesbar, wird 0 zurückgegeben (= keine Prüfung) — beim Restore gegen eine
// leere Umgebung der richtige Default; die Migration läuft danach ohnehin.
//
// F-239: bewusst KEIN storage.Open hier. storage.Open würde Verzeichnis +
// DB + WAL/SHM anlegen und Migrationen auf einer evtl. älteren Bestands-DB
// ausführen — also genau das Restore-Ziel verunreinigen, das gleich danach
// aus dem Archiv überschrieben wird. Read-only öffnen, nichts schreiben,
// keine Datei materialisieren.
func currentSchemaVersion(dbPath string) int {
	if _, err := os.Stat(dbPath); err != nil {
		return 0 // DB existiert (noch) nicht → keine Prüfung, nichts anlegen
	}
	// query_only(1): die Verbindung verweigert jeden Schreibzugriff, also
	// keine WAL-Umschaltung, keine Migration, keine Sidecar-Dateien.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(2000)&_pragma=query_only(1)")
	if err != nil {
		return 0
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0
	}
	return v
}

// seedDefaultBoard legt auf einer leeren DB genau ein kanonisches Board mit
// fester id `default` an (ADR-0054, Teil B / F-253). Idempotent: existiert
// bereits mindestens ein Board, passiert nichts — ein bestehender Datenbestand
// wird nie angefasst. Behebt L1 (frischer Server war leer → jeder Client-Save
// traf ein nicht-existentes Board → 404/428).
//
// F-256 (Karte #199): falls ABOARD_BOOTSTRAP_OWNER gesetzt ist, wird der
// Ersteller als owner des neuen Default-Boards eingetragen. Nur beim NEU-
// Anlegen — bestehende Boards bleiben unberührt. Fehlertolerant: ein ACL-Set-
// Fehler stoppt den Start nicht (geloggt, Server läuft ohne ACL weiter).
func seedDefaultBoard(ctx context.Context, repo *storage.BoardRepo, aclRepo *storage.AclRepo) error {
	list, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("seedDefaultBoard: List: %w", err)
	}
	if len(list) > 0 {
		return nil // bestehender Bestand — nichts zu tun
	}
	if _, err := repo.Insert(ctx, "default", "Board", "2", []byte(defaultBoardSeed)); err != nil {
		return fmt.Errorf("seedDefaultBoard: Insert: %w", err)
	}
	log.Printf("aboard-server: leere DB erkannt — kanonisches Default-Board angelegt (id=default)")
	// F-256/F-263: Bootstrap-Owner aus Env-Var eintragen — durch dieselbe
	// zentrale Normalisierung wie der ACL-/Forward-Auth-Pfad (F-260), sonst
	// käme der Owner bei Casing-/Whitespace-Diff Env↔Authelia NIE auf sein
	// eigenes Board (Selbst-Lockout, nicht in-band reparierbar).
	if bootstrapOwner, gesetzt, ok := storage.BootstrapOwnerAusEnv(); gesetzt && aclRepo != nil {
		// F-263: ein leerer (nur Whitespace) oder offensichtlich kaputter
		// Wert darf NIE still einen unbrauchbaren ACL-Eintrag erzeugen. Klare
		// ERROR-Zeile statt stillem Schreiben — aber KEIN Server-Crash wegen
		// eines Bootstrap-Tippfehlers (Default-Board ohne ACL ist im
		// Legacy-Bypass nutzbar; mit Trust=AN merkt der Admin den 403 + sieht
		// diese Logzeile). F-1862: dieselbe Prüfung wie beim Service-Board.
		if !ok {
			log.Printf("aboard-server: FEHLER: ABOARD_BOOTSTRAP_OWNER=%q ist keine gültige Personenadresse "+
				"(nach Normalisierung) — KEIN Bootstrap-Owner-ACL-Eintrag angelegt. Setze eine gültige "+
				"Adresse und starte mit leerer DB neu (F-263).", bootstrapOwner)
		} else if aerr := aclRepo.Set(ctx, "default", bootstrapOwner, storage.RoleOwner); aerr != nil {
			log.Printf("aboard-server: Bootstrap-Owner ACL fehlgeschlagen (owner=%q): %v — Server läuft ohne ACL-Eintrag weiter", bootstrapOwner, aerr)
		} else {
			log.Printf("aboard-server: Bootstrap-Owner eingetragen: %q → owner auf Board 'default'", bootstrapOwner)
		}
	}
	return nil
}

// meldeBootstrapStand holt beim Start den Bootstrap-Admin nach (F-1855) und
// nennt Boards ohne Owner (F-1854). Beides nicht tödlich: Fehler werden
// geloggt, der Server läuft weiter.
func meldeBootstrapStand(ctx context.Context, db *sql.DB) {
	owner, gesetzt, ok := storage.BootstrapOwnerAusEnv()
	if gesetzt && !ok {
		// Verify W-4: gesetzt, aber ungültig, und noch kein Admin — sonst
		// schwiege der Start, und die Meldung käme erst bei der nächsten
		// Erstanmeldung (die bei bestehendem Konto nie kommt).
		if n, err := storage.NewUserRepo(db).CountAdmins(ctx); err == nil && n == 0 {
			log.Printf("aboard-server: FEHLER: ABOARD_BOOTSTRAP_OWNER=%q ist keine Personenadresse und es gibt "+
				"keinen Admin — niemand wird automatisch Admin. Wert berichtigen und neu starten (F-1855)", owner)
		}
	}
	if ok {
		if neu, err := storage.BootstrapAdminNachholen(ctx, db, owner); err != nil {
			log.Printf("aboard-server: Bootstrap-Admin nachholen fehlgeschlagen: %v", err)
		} else if neu {
			log.Printf("aboard-server: %q ist jetzt Admin (noch kein Admin vorhanden, ABOARD_BOOTSTRAP_OWNER) — F-1855", owner)
		}
	}
	// Im Einzelbetrieb hat kein Board einen Owner — der Hinweis gälte dort
	// bei jedem Start und hiesse nichts.
	if !api.StrengeAnmeldung() { // Kritik A-6: auch im OIDC-Betrieb melden
		return
	}
	ohne, err := storage.BoardsOhneOwner(ctx, db)
	if err != nil {
		log.Printf("aboard-server: Boards ohne Owner nicht prüfbar: %v", err)
		return
	}
	if len(ohne) > 0 {
		log.Printf("aboard-server: HINWEIS: %d Board(s) ohne Owner (z. B. %q) — im Mehrnutzer-Betrieb sieht sie "+
			"niemand. Zuteilen: aboard-server acl adopt --email <adresse> (F-1854)", len(ohne), ohne[0].ID)
	}
}

// runPreflight bündelt die Start-Voraussetzungen, die VOR jedem DB-/
// Netzwerk-Setup geprüft werden müssen: Bind-Sicherheit (ADR-0061 (c),
// F-262) und die Secret-Pflicht bei aktivem Forward-Auth-Trust (ADR-0129 §2,
// F-1124). Beide Checks brechen den Start per Fehler-Return ab — run()
// interpretiert das wie jeden anderen fatalen Startfehler.
//
// Eigene Funktion (G9, F-1143): checkForwardAuthSecret() war bis Sprint 71
// nur FÜR SICH getestet (forwardauth_bind_test.go) — kein Test bewies, dass
// run() sie überhaupt aufruft. Ein Refactor, der den Aufruf aus run()
// entfernt (Tippfehler beim Umbau, Merge-Konflikt, …), wäre unbemerkt
// geblieben, weil TestCheckForwardAuthSecret_* nur die Funktion selbst
// prüfen, nie den Startpfad. runPreflight macht die Verdrahtung selbst
// testbar, ohne den ganzen Server hochzufahren (main_preflight_test.go).
func runPreflight(listenAddr, unixSocket string) error {
	if err := checkForwardAuthBind(listenAddr, unixSocket, log.Printf); err != nil {
		return err
	}
	if err := checkForwardAuthSecret(); err != nil {
		return err
	}
	// ADR-0148 §1 (Karte #816): Schalter und Pflichtvariablen der
	// eingebauten Anmeldung, samt Ausschluss von Forward-Auth und Dev-Fake.
	if err := checkAnmeldungKonfig(); err != nil {
		return err
	}
	return nil
}

func run() error {
	// Karte #347: Als ALLERERSTES, noch vor jedem Check, der den Start abbrechen
	// kann. Wer im Container-Log nachsieht, warum ein Start scheiterte, braucht
	// zuerst die Antwort auf „welcher Build läuft hier überhaupt" — eine
	// Versionszeile, die erst beim erfolgreichen Lauschen erscheint, fehlt genau
	// im interessanten Fall. Der spätere „lauscht auf …"-Log nennt sie erneut,
	// damit auch ein abgeschnittenes Log-Fenster sie enthält.
	log.Printf("aboard-server %s: startet", api.VersionLine())

	dbPath := envOrDefault("ABOARD_DB", defaultDBPath)
	attachDir := envOrDefault("ABOARD_ATTACHMENTS_DIR", defaultAttachmentsDir)
	listenAddr := envOrDefault("ABOARD_ADDR", defaultListenAddr)
	unixSocket := os.Getenv("ABOARD_UNIX_SOCKET")

	// ADR-0061 (c) / F-262 / ADR-0129 §2 / F-1124: Fail-loud-Preflight VOR
	// allem anderen (Bind-Sicherheit + Secret-Pflicht bei Trust=AN). Siehe
	// runPreflight-Doku (G9, F-1143): eigene Funktion, damit ein Test die
	// Verdrahtung prüfen kann, ohne den ganzen Server zu starten.
	if err := runPreflight(listenAddr, unixSocket); err != nil {
		return err
	}

	// Karte #577: Modus der Unix-Sockets. Ein ungültiger Wert bricht hier ab,
	// bevor irgendetwas lauscht.
	sockModus, err := unixSocketModus()
	if err != nil {
		return err
	}

	// Karte #569 (ADR-0129 §5): Ausgangs-Schalter vor allem, was eine
	// Verbindung aufbauen könnte (Mail-Ingest, Notifications, LLM). Ein
	// ungültiger Wert bricht den Start ab, statt im Zweifel Ausgänge zuzulassen.
	politik, err := ausgang.AusUmgebung()
	if err != nil {
		return err
	}
	ausgang.Setze(politik)
	log.Printf("aboard-server: %s", politik.Beschreibung())

	// Karte #684 (ADR-0134 §1): Schalter der Ausgangsschleuse, Vorgabe aus.
	// Ein ungültiger Wert bricht den Start ab, bevor die Datenbank geöffnet
	// wird — im Zweifel kein Weg nach draussen.
	schleuseKonfig, err := schleuse.AusUmgebung()
	if err != nil {
		return err
	}
	log.Printf("aboard-server: %s", schleuseKonfig.Beschreibung())

	// F-1192: Frist-Löschungen nur mit gestellter Uhr (server/uhr). Ein
	// ungültiger Schalter hält den Start an; der Zustand steht im Start-Log.
	if err := uhr.PruefeKonfiguration(); err != nil {
		return err
	}
	log.Printf("aboard-server: %s", uhrZustand())

	// Karte #570: Basis-URL des OpenAI-kompatiblen Anbieters ebenfalls VOR dem
	// Öffnen der Datenbank prüfen — sonst liefen bei einem Tippfehler erst die
	// Migrationen, und dann bräche der Start ab (K-B7, Session-Kritiker).
	openAIBaseURL, err := llm.ValidateOpenAIBaseURL(os.Getenv("ABOARD_LLM_OPENAI_BASE_URL"))
	if err != nil {
		return err
	}
	// Karte #672 (ADR-0133 §7): Adresse der Open-WebUI-Instanz für den Knopf
	// „In Open WebUI weiterbearbeiten“. Ungültig → Startabbruch.
	owuiURL, err := api.ValidateOWUIURL(os.Getenv("ABOARD_OWUI_URL"))
	if err != nil {
		return err
	}
	openAIModelle, err := llm.ParseOpenAIModelle(os.Getenv("ABOARD_LLM_OPENAI_MODELLE"))
	if err != nil {
		return err
	}
	// Karte #665 (ADR-0133 §3): Kontextgrenze und Werkzeug-Schalter des
	// lokalen Modells — ebenfalls vor dem Öffnen der Datenbank.
	openAIKontext, err := llm.ParseOpenAIKontext(os.Getenv("ABOARD_LLM_OPENAI_KONTEXT"), openAIBaseURL)
	if err != nil {
		return err
	}
	openAIWerkzeugeAus, err := llm.ParseOpenAIWerkzeuge(os.Getenv("ABOARD_LLM_OPENAI_WERKZEUGE"))
	if err != nil {
		return err
	}
	llm.SetzeOpenAIKontext(openAIKontext)
	llm.SetzeOpenAIWerkzeugeAus(openAIWerkzeugeAus)
	if openAIKontext > 0 {
		log.Printf("aboard-server: LLM openai: Kontextgrenze %d Token (Budget-Tor an)", openAIKontext)
	}
	if openAIWerkzeugeAus {
		log.Printf("aboard-server: LLM openai: Werkzeuge aus (kein tools/tool_choice an das Gegenüber)")
	}
	// Karte #664 (ADR-0133 §2 + Nachtrag): Firmenmodus und PII-Vorgabe —
	// ebenfalls VOR dem Öffnen der Datenbank. Unlesbare/leere Schlüsseldatei,
	// leere Modellliste im Firmenmodus oder ein ungültiges ABOARD_LLM_PII
	// halten den Start an. Keine Meldung nennt den Schlüssel.
	llmFirmenmodus, llmPII, err := llmBetreiberVorgaben(openAIBaseURL, openAIModelle)
	if err != nil {
		return err
	}
	// Kritiker K2-8: zu offene Rechte an der Schlüsseldatei — nur Warnung.
	if llmFirmenmodus.Aktiv() {
		if w := llm.ServerschluesselRechteWarnung(os.Getenv(llm.ServerschluesselEnvVar)); w != "" {
			log.Print("aboard-server: " + w)
		}
	}
	// Karte #674 (ADR-0133 Nachtrag 2 A6): Obergrenze für Chat-Anfragen je
	// Person. Ungültiger Wert hält den Start an — auch ohne Firmenmodus, damit
	// ein Tippfehler nicht erst beim Scharfschalten auffällt.
	llmAnfragenProStunde, err := api.ParseLLMAnfragenProStunde(os.Getenv(api.LLMAnfragenEnvVar))
	if err != nil {
		return err
	}

	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		return err
	}
	defer db.Close()

	boardRepo := storage.NewBoardRepo(db)
	userRepo := storage.NewUserRepo(db)
	aclRepo := storage.NewAclRepo(db)
	bootstrapRepo := storage.NewBootstrapRepo(db) // ADR-0066/#225: Personal-Board-Bootstrap
	activityRepo := storage.NewActivityRepo(db)

	// #306/ADR-0101: Server-Administration Foundation. Master-Key kommt
	// per Env-Var (ABOARD_SECRET_KEY, 32 Bytes hex/base64); fehlt er, läuft
	// der ConfigStore im Read-Only-Modus für Secrets — Set(isSecret=true)
	// antwortet 503. Plain-Keys (z. B. Feature-Toggles) gehen weiter.
	// #298/ADR-0094: dieselbe Box wird für LLM-Keys wiederverwendet.
	secretBox, configBoxStatus := newSecretBox()
	if configBoxStatus != "" {
		log.Printf("aboard-server: %s", configBoxStatus)
	}
	configRepo := storage.NewConfigRepo(db, secretBox, uuidString)
	// ADR-0054 (F-253, L1): leere DB → kanonisches Default-Board anlegen,
	// bevor die API lauscht. Idempotent — Bestands-DBs bleiben unberührt.
	// F-256 (Karte #199): aclRepo für Bootstrap-Owner-Eintrag mitgeben.
	if err := seedDefaultBoard(ctx, boardRepo, aclRepo); err != nil {
		return err
	}
	// #294/ADR-0092: Service-Board-ENV-Bootstrap + Seed. One-shot beim Start;
	// idempotent (Bestands-Board bleibt unberührt). Reihenfolge: ENV→DB zuerst,
	// damit Seed() den möglicherweise gerade importierten Titel sieht.
	if err := servicepkg.BootstrapEnv(ctx, configRepo, log.Printf); err != nil {
		log.Printf("aboard-server: Service-Board-Env-Bootstrap fehlgeschlagen: %v", err)
	}
	serviceCfg := servicepkg.LoadConfig(ctx, configRepo)
	if err := servicepkg.Seed(ctx, boardRepo, aclRepo, serviceCfg, log.Printf); err != nil {
		// Ein Service-Board-Seed-Fehler ist nicht tödlich — Server soll auch
		// ohne Mail-Pipeline starten. Fehler loggen, weiterlaufen.
		log.Printf("aboard-server: Service-Board-Seed fehlgeschlagen: %v", err)
	}
	meldeBootstrapStand(ctx, db)
	spamRepo := storage.NewSpamRepo(db)
	broker := api.NewEventBroker()
	// Karte #200, ADR-0037: WithActivities hängt das server-seitige Activity-Log
	// ein. Im Legacy-Bypass (kein Forward-Auth) wird actor = '' geloggt — kein Crash.
	eventsAPI := api.NewEventsAPI(broker, boardRepo)
	attachStore := storage.NewAttachmentStore(attachDir)
	// F-200 (Karte #376): WithAttachments räumt beim Board-Delete auch das
	// Anhang-Verzeichnis ab — die Dateien liegen nur im Dateisystem, für sie
	// gibt es kein ON DELETE CASCADE.
	// Karte #552 (ADR-0129 §4): Fold am Server für POST /boards/{id}/ops.
	// Lädt das eingebettete ops.js-Bündel. Scheitert das, startet der Server
	// nicht — sonst fiele es erst beim ersten Agenten als 503 auf.
	foldMotor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		return fmt.Errorf("fold: %w", err)
	}
	boardsAPI := api.NewBoardsAPI(boardRepo).MitKalenderKonfig(configRepo).WithBroker(broker).WithACL(aclRepo).WithUsers(userRepo).
		WithActivities(activityRepo).WithAttachments(attachStore).WithFold(foldMotor).
		// Kritiker Sprint 70, K-A1: Grabstein ins Backup-Verzeichnis. Nur so
		// markierte Verzeichnisse räumt der Scheduler später ab — nie eines,
		// dessen Board bloss in der Datenbank fehlt (DB-Verlust, Rückspielung).
		// Das Verzeichnis wird pro Aufruf aus der Konfiguration gelesen, weil
		// backup_board.dir zur Laufzeit änderbar ist.
		WithNachDelete(func(ctx context.Context, boardID string) {
			dir := boardbackup.LoadConfig(ctx, configRepo).Dir
			if err := boardbackup.MarkiereGeloescht(dir, boardID, time.Now()); err != nil {
				log.Printf("boards.delete: Grabstein für Backups von Board %s nicht gesetzt — "+
					"sie werden NICHT abgeräumt: %v", boardID, err)
			}
		})
	attachAPI := api.NewAttachmentsAPI(attachStore, envInt64("ABOARD_ATTACHMENT_MAX_BYTES", defaultMaxAttachBytes)).WithBroker(broker).
		WithSpiegelUploadRecht(boardsAPI).  // F-2242: Spiegel und wartende Karten außen
		WithKommentarDateiRecht(boardsAPI). // KF-3: Dateien fremder Kommentare (ADR-0135/0136 Nachtrag)
		WithKartenLage(boardsAPI)           // F-3019/F-3020: nachgeholter Purge nur für verwaiste Karten

	// F-600 (Karte #398): Abgleich unterbrochener Cross-Board-Transfers. Der
	// Transfer ist eine zweiphasige Saga (DB-Transaktion, dann Anhang-Commit).
	// Stirbt der Prozess HART genau dazwischen, lebt die Karte laut Datenbank
	// bereits im Ziel-Board, ihre Anhänge liegen aber nur im unsichtbaren
	// Staging — für den Nutzer sieht das aus wie „Karte da, Anhänge weg".
	//
	// HIER und nicht später: der Abgleich läuft, BEVOR der HTTP-Server Anfragen
	// annimmt. Damit erübrigt sich die Frage, ob er einen laufenden Transfer
	// stören könnte — es läuft keiner. Ein Staging-Rest entsteht nur durch einen
	// Absturz, und auf einen Absturz folgt zwangsläufig ein Start.
	if b := attachStore.ReconcileStagedTransfers(func(cardId string) (string, error) {
		return storage.BoardFuerKarte(ctx, boardRepo, cardId)
	}); b.Liegengelassen > 0 {
		// F-618: Der Bericht wurde vorher verworfen. „Liegengelassen" ist der
		// einzige Ausgang, der einen Menschen braucht — die anderen beiden hat
		// der Abgleich selbst zu Ende gebracht. Deshalb hier eine eigene,
		// zusammenfassende Zeile: Wer nur die Startausgabe überfliegt, soll
		// sehen, dass etwas offen blieb, ohne die Einzelmeldungen zu suchen.
		log.Printf("aboard-server: Transfer-Abgleich: %d Staging-Rest(e) konnten nicht "+
			"zugeordnet werden und bleiben liegen — siehe die Zeilen darüber", b.Liegengelassen)
	}
	// F-3050 (Sprint-117-Kritiker): Reste nachgeholter Purges (.purge-beiseite) aus
	// einem Absturz zwischen Beiseiteschieben und zweiter Prüfung. Gleiche Begründung
	// wie oben: vor dem ersten Request läuft kein Purge, also gilt kein Alter. Lebt
	// die Karte, kommen ihre Dateien zurück; fehlt sie, gehen sie; sonst bleiben sie.
	attachStore.RaeumePurgeResteAuf(api.AufraeumLage(ctx, boardsAPI))

	mux := http.NewServeMux()
	// /healthz liegt im selben mux und damit (wenn aktiv) hinter dem
	// Forward-Auth-Wrapper — bleibt aber für Liveness-Checks ohne Auth-Header
	// erreichbar: Er steht auf der Ausnahmeliste des strengen Modus
	// (api.offenerPfad, ADR-0129 §2). Alles andere antwortet dort ohne
	// Identität mit 401.
	mux.Handle("/healthz", api.HealthHandler(db))
	boardsAPI.Register(mux)
	// Karte #671 (ADR-0133 §5/§7): Werkzeug-Endpunkte für Open WebUI
	// (/openapi.json, /werkzeuge/…). Nur über den Agenten-Eingang wirksam;
	// auf dem Menschen-Eingang antworten sie 404. Links „im Board öffnen“
	// aus ABOARD_PUBLIC_BASE_URL — ungültig: Warnung, keine Links (die
	// Variable gab es vorher schon ungeprüft für Mails; ein Start-Abbruch
	// träfe bestehende Deployments).
	// F-1559: Dieselbe Prüfung und derselbe Wert gelten für die Mail-Links
	// (notifBaseURL unten) — ungültig heisst dort ebenfalls: keine Links.
	oeffentlicheBasis := oeffentlicheBasisGeprueft(os.Getenv("ABOARD_PUBLIC_BASE_URL"), log.Printf)
	if err := pruefeAnmeldungBasis(oeffentlicheBasis); err != nil {
		return err
	}
	boardsAPI.WithOeffentlicheBasis(oeffentlicheBasis)
	// Karte #789 (ADR-0140 §7, §10 W-4): Board-Paar in den Agenten-Werkzeugen
	// (Schatten außen nur lesbar, Werkzeugtexte je Seite) und im Ops-Weg
	// (Agenten-Anlagen innen vertraulich). Seite je Anfrage aus server_config.
	boardsAPI.MitBoardpaar(configRepo, storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db))
	boardsAPI.RegisterWerkzeuge(mux)
	// Karte #783 (ADR-0140 §8): Rolle „interner Agent“ außen — fünf feste Wege
	// am Agenten-Eingang, Vorschläge, die ein Mensch im Board bestätigt, und
	// das Abräumen abgelaufener Vorschläge (24 h).
	boardsAPI.RegisterVorschlaege(mux)
	boardsAPI.StarteVorschlagAufraeumen(ctx)
	eventsAPI.Register(mux)
	attachAPI.Register(mux)
	// Karte #741: Vorschaubild je Board für die Kachelansicht (unter /boards/* → ACLGuard).
	api.NewBoardVorschauAPI(boardRepo, attachStore).Register(mux)
	// Karte #201, ADR-0059: /me-Endpoint — liefert Forward-Auth-Identität (Name/Email)
	// oder null-Felder im Legacy-Bypass. Außerhalb /boards/* → kein ACL-Guard.
	// Karte #684: /me meldet `schleuse: "an"` nur bei eingeschalteter
	// Ausgangsschleuse — sonst bleibt die Antwort unverändert.
	// Karte #739 (ADR-0137): Wissensquelle — Lesewege /wissen/* für Menschen
	// mit Konto dieser Instanz; /me nennt den Namen, wenn sie an und sichtbar
	// ist. Standard aus (wissen.enabled), Einstellungen im Admin-Reiter.
	// F-1887: wissen.dir darf den Anhang-Ordner nicht treffen (den
	// Sicherungsordner liest die Prüfung aus backup_board.*).
	wissenAPI := api.NewWissenAPI(configRepo, userRepo).MitServerOrdnern(wissenServerOrdner(attachDir)...)
	wissenAPI.Register(mux)
	api.RegisterMeHandler(mux, userRepo, api.MeWithOWUIURL(owuiURL), api.MeWithSchleuse(schleuseKonfig.An),
		api.MeWithWissen(wissenAPI.SichtbarFuer), api.MeWithBoardpaar(configRepo))
	// ADR-0066 §3, Karte #224: bekannte-Nutzer-Verzeichnis für Share-Picker.
	// Außerhalb /boards/* → ACLGuard greift nicht; Handler prüft selbst (Auth-Details
	// in known_users.go).
	api.RegisterKnownUsersHandler(mux, aclRepo)
	// #306/ADR-0101: /admin/*-Endpoints + requireAdmin-Middleware.
	// Außerhalb /boards/* → ACLGuard greift nicht; AdminAPI prüft is_admin selbst.
	adminAPI := api.NewAdminAPI(configRepo, userRepo, aboardVersion, time.Now())
	adminAPI.WithKonfigPruefung("wissen.", wissenAPI.PruefeKonfig)
	adminAPI.WithLoeschPruefung("wissen.", wissenAPI.PruefeLoeschen)
	adminAPI.Register(mux)
	adminAPI.Registry().RegisterTest("wissen", wissenAPI.WissenTest)
	adminAPI.Registry().RegisterReload("wissen", func(_ context.Context, _ map[string]string) error {
		wissenAPI.Vergiss()
		return nil
	})
	// Karte #553 (ADR-0129 §3): Agenten-Tokens. Der Zugang ist die Middleware
	// des Agenten-Listeners (main_agent.go); die Admin-Routen schliessen beim
	// Widerruf dessen offene Verbindungen.
	agentRepo := storage.NewAgentTokenRepo(db)
	agentZugang := api.NewAgentZugang(agentRepo)
	// Karte #608: Fehlversuch-Bremse — X-Real-IP nur vom konfigurierten Proxy.
	if err := agentZugang.MitProxy(os.Getenv(api.AgentProxyEnvVar)); err != nil {
		return err
	}
	// Karte #669 (ADR-0133 §4): delegierendes Dienst-Token für Open WebUI —
	// Geheimnis aus ABOARD_OWUI_JWT_SECRET_FILE, Rolle der Person aus der ACL.
	if err := agentZugang.MitOWUIDelegation(aclRepo); err != nil {
		return err
	}
	// Kritiker Sprint 82 Runde 2, KO-2: zu offene Rechte an der Geheimnis-
	// Datei — nur Warnung, wie beim Serverschlüssel (K2-8).
	if w := api.OWUIGeheimnisRechteWarnung(); w != "" {
		log.Print("aboard-server: " + w)
	}
	// Karte #730: abweichendes JWT-Höchstalter beim Start nennen.
	if h := api.OWUIHoechstalterHinweis(); h != "" {
		log.Print(h)
	}
	adminAPI.RegisterAgentTokens(mux, agentRepo, agentZugang)
	// Karte #684 (ADR-0134 §1, §2): Ausgangsschleuse. Der Admin-Status meldet
	// den Schalter immer; die Routen gibt es nur bei ABOARD_SCHLEUSE=an (sonst
	// 404). Schlüssel je Person nur mit Master-Key (sonst 503), mit derselben
	// Box wie llm_api_keys, aber in einer eigenen Tabelle — die Schleuse liest
	// llm_api_keys nie (ADR-0133 §2).
	var schleuseSchluessel *storage.SchleuseSchluesselRepo
	if secretBox != nil {
		schleuseSchluessel = storage.NewSchleuseSchluesselRepo(db, secretBox)
	}
	schleuseAPI := api.NewSchleuseAPI(schleuseKonfig, db, schleuseSchluessel, userRepo, aclRepo)
	// Karte #685 (ADR-0134 §3.1–§3.4): Aufträge mit verschlüsselter Zuordnung
	// (nur mit Master-Key) und das lokale Modell der zweiten
	// Pseudonymisierungsstufe — Firmenmodus mit eigener Basis-URL, sonst
	// keins (jeder Auszug bricht dann ab, ADR-0134 §1).
	var schleuseAuftraege *storage.SchleuseAuftragRepo
	if secretBox != nil {
		schleuseAuftraege = storage.NewSchleuseAuftragRepo(db, secretBox)
	}
	var schleuseModell schleuse.ModellAufruf
	if schleuseKonfig.An {
		pseudoModell, err := schleuse.WaehlePseudoModell(schleuseKonfig.PseudoModell, llmFirmenmodus.Modelle())
		if err != nil {
			return err
		}
		if aufruf := llm.SchleuseModellAufruf(llmFirmenmodus, openAIBaseURL, llmUserAgent, pseudoModell); aufruf != nil {
			schleuseModell = aufruf
			log.Printf("aboard-server: Ausgangsschleuse: zweite Pseudonymisierungsstufe über das lokale Modell %s (%s), Auszüge höchstens %d Zeichen",
				pseudoModell, openAIBaseURL, schleuseKonfig.GrenzeZeichen())
		} else {
			// F-1626: den eigentlichen Grund nennen — bei gesetzter, aber nicht
			// interner Basis-URL war „kein lokales Modell“ irreführend.
			grund := "kein Firmenmodus mit ABOARD_LLM_OPENAI_BASE_URL"
			if openAIBaseURL != "" {
				if err := llm.SchleuseBasisIntern(openAIBaseURL); err != nil {
					grund = err.Error()
				}
			}
			log.Printf("aboard-server: WARNUNG: Ausgangsschleuse an, aber keine zweite Pseudonymisierungsstufe (%s) — jeder Auszug bricht ab", grund)
		}
		if schleuseAuftraege != nil {
			if n, err := schleuseAuftraege.EntwuerfeAufraeumen(ctx, schleuseKonfig.AufbewahrungTage); err != nil {
				log.Printf("aboard-server: Ausgangsschleuse: alte Entwürfe nicht aufgeräumt: %v", err)
			} else if n > 0 {
				log.Printf("aboard-server: Ausgangsschleuse: %d Entwürfe nach %d Tagen ohne Änderung gelöscht", n, schleuseKonfig.AufbewahrungTage)
			}
		}
	}
	schleuseAPI.MitAuszug(schleuseAuftraege, schleuseModell)
	// Karte #687 (ADR-0134 §5): Dateien im Auszug — derselbe Anhangsspeicher,
	// nur lesend.
	schleuseAPI.MitAnhaengen(attachStore)
	// Karte #686 (ADR-0134 §3.4–§3.7): Freigabe, Hintergrundauftrag,
	// Antwortkommentar. Beim Start: unterbrochene Versände abschliessen,
	// freigegebene wieder anstossen (beides nur bei „an“ und mit Master-Key).
	schleuseAPI.MitVersand(boardRepo, broker, llmUserAgent)
	if schleuseKonfig.An && schleuseAuftraege != nil {
		schleuseAPI.VersandStart(ctx)
	}
	api.RegistriereSchleuse(mux, adminAPI, schleuseAPI)
	if schleuseKonfig.An {
		if secretBox == nil {
			log.Printf("aboard-server: WARNUNG: Ausgangsschleuse an, aber ABOARD_SECRET_KEY fehlt — Personen können keine Schlüssel hinterlegen (503)")
		}
		// Zweiter Riegel: freigegebene Hosts gegen ABOARD_AUSGAENGE_ERLAUBT.
		warnungen, err := schleuseAPI.Abgleich(ctx)
		if err != nil {
			return fmt.Errorf("ausgangsschleuse: abgleich mit der ausgangsliste: %w", err)
		}
		for _, w := range warnungen {
			log.Print(w)
		}
	}
	api.RegisterAgentTokensSelbst(mux, agentRepo, userRepo, agentZugang, configRepo) // Karte #608
	// #307/ADR-0102: /clients- und /projects-Endpoints. Server-weite Entities,
	// CRUD admin-only, Read viewer+; Karten-Projekt-Zuordnung lebt im Karten-JSON
	// (ADR-0044 — Server kennt boards.state_json nur als Blob).
	clientRepo := storage.NewClientRepo(db)
	projectRepo := storage.NewProjectRepo(db)
	clientsProjectsAPI := api.NewClientsProjectsAPI(clientRepo, projectRepo, userRepo, boardRepo, aclRepo)
	clientsProjectsAPI.Register(mux)
	// #308/ADR-0103 Etappe 5: GET /me/cards — Cross-Board-Kalender-Datenquelle
	// (alle terminierten Karten über alle Boards mit Lese-Zugriff). Außerhalb
	// /boards/* → ACLGuard greift nicht; Handler filtert per ListBoardsForUser.
	calendarAPI := api.NewCalendarAPI(boardRepo, aclRepo, userRepo)
	calendarAPI.Register(mux)
	// #308/ADR-0103 Etappe 6: iCal-Export. /me/ical/* (Token-Verwaltung,
	// Forward-Auth) + /ical/calendar.ics (token-authentifizierter Feed,
	// außerhalb /boards/* → kein ACLGuard). Token encrypted-at-rest via
	// secretBox; fehlt der Master-Key → tokens=nil → Endpoints 503.
	var icalTokens *storage.ICalTokenRepo
	if secretBox != nil {
		icalTokens = storage.NewICalTokenRepo(db, secretBox)
	}
	// Die Außenstelle (ADR-0138 §6, /ical/aussen/*, Token kalender-ausgang)
	// ist mit Karte #795 zurückgebaut (ADR-0140 §11). Die Tabelle
	// kalender_ausgang_tokens bleibt mit ihren widerrufenen Einträgen stehen.
	// Karte #782 (ADR-0140 §6): Schatten im Feed der Seite außen — die einzige
	// erlaubte Leserin der Schatten-Tabelle (Wächter in main_schatten_780_test.go).
	icalAPI := api.NewICalAPI(icalTokens, userRepo, boardRepo, aclRepo).MitKonfig(configRepo).
		MitBoardpaar(storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db)).
		MitAdressregel(agentZugang.ClientAdresse) // F-2109
	icalAPI.Register(mux)
	// Karte #778 (ADR-0140 §2): Schatten-Ausgang des Board-Paars, Seite
	// innen — GET /schatten/{board}, erreichbar nur über den Agenten-Eingang
	// mit einem abs_-Token.
	boardpaar := storage.NewBoardpaarRepo(db)
	api.NewSchattenAusgangAPI(boardRepo, configRepo, boardpaar).MitAdressregel(agentZugang.ClientAdresse).Register(mux)
	// Karte #779 (ADR-0140 §3): Schatten-Eingang, Seite außen —
	// PUT /schatten/{board}, nur über den Agenten-Eingang mit einem
	// abe_-Token. Schatten liegen in eigener Tabelle, nie im Board-Zustand.
	api.NewSchattenEingangAPI(configRepo, boardpaar, storage.NewSchattenRepo(db), broker).MitAdressregel(agentZugang.ClientAdresse).Register(mux)
	// Karte #796 (T-R12): POST /boardpaar/erneuern — das Push-Skript tauscht
	// sein abs_/abe_-Token gegen ein neues, nur über den Agenten-Eingang.
	api.NewBoardpaarErneuernAPI(configRepo, boardpaar).MitAdressregel(agentZugang.ClientAdresse).Register(mux)
	// Karte #825 (ADR-0141 Schritt 2a): externe Kopie anlegen und Hinweg v2 —
	// Aufträge/Ziele/Hinweg innen, Kopie anlegen/Eingang v2 außen, nur über
	// den Agenten-Eingang mit abs_, abr_ bzw. abe_.
	// Karte #834 (ADR-0142): Zugang „Sicherungen lesen“ — GET /sicherungen
	// und GET /sicherungen/{board}/{datei}, nur über den Agenten-Eingang mit
	// einem abb_-Token, auf jeder Seite der Instanz, nur verschlüsselte Archive.
	api.NewSicherungenAPI(boardRepo, configRepo, boardpaar).MitAdressregel(agentZugang.ClientAdresse).Register(mux)
	api.NewBoardpaarKopieAPI(boardRepo, configRepo, boardpaar, storage.NewSchattenRepo(db), broker).
		MitAdressregel(agentZugang.ClientAdresse).
		// Karte #827 (ADR-0141 §5.3): Rückweg innen über den Ops-Weg.
		MitRueckweg(boardsAPI).Register(mux)
	// Karte #781 (ADR-0140 §5, §6): Anzeige außen — GET /boards/{id}/schatten
	// für Board-Ansicht und Kalenderansicht im Browser (Menschen-Kette, Rolle
	// viewer oder mehr). Der dritte Leser der Schatten-Tabelle (Wächter in
	// main_schatten_780_test.go); auf aus/innen antwortet er 404.
	api.NewSchattenAnzeigeAPI(configRepo, boardpaar, storage.NewSchattenRepo(db)).Register(mux)
	// Karte #788 (ADR-0140 §4): Pflege von Partnerlisten und Zugängen, nur
	// Server-Admin (CLI: aboard-server boardpaar …).
	// Karte #831: Notweg „Kopie außen löschen“ hinterlässt dasselbe wie
	// DELETE /boards/{id} (Anhänge, Backup-Grabstein, Lösch-Ereignis).
	adminAPI.WithBoardGeloescht(boardsAPI.NachBoardGeloescht)
	adminAPI.WithBroker(broker) // F-2299: „bearbeitbar“ umstellen geht als schatten:aktualisiert hinaus
	adminAPI.WithBoardpaarBasis(oeffentlicheBasis).RegisterBoardpaar(mux, boardpaar)
	// #298/ADR-0094: /users/me/llm-keys/*-Endpoints. own-User-only (kein IDOR);
	// LLMKeyRepo nutzt dieselbe secretcrypto.Box wie #306-Secrets. Fehlt der
	// Master-Key → keys=nil → Handler antworten 503.
	//
	// Karte #570 (ADR-0129 §5): Basis-URL und Modellnamen des
	// OpenAI-kompatiblen Anbieters sind oben (vor dem DB-Open) geprüft. Nur
	// lesend im Admin-Status; die Oberfläche erfährt Ziel und Modelle über
	// GET /users/me/llm-keys (K-B1/K-B2).
	adminAPI.WithLLMOpenAIBaseURL(openAIBaseURL)
	// F-1546: Firmenmodus und PII-Vorgabe im Admin-Status — ohne Schlüssel.
	adminAPI.WithLLMVorgaben(llmFirmenmodus.Aktiv(), llmFirmenmodus.Modelle(), string(llmPII))
	llmRegistry := llm.NewRegistry()
	if llmFirmenmodus.Aktiv() {
		// Karte #664: Nur „openai“ — Anthropic/Google/Mistral nicht.
		llm.RegisterFirmenmodus(llmRegistry, llmUserAgent, openAIBaseURL)
		log.Printf("aboard-server: LLM-Firmenmodus aktiv: nur Anbieter %s, Serverschlüssel aus %s, Modelle %s, PII-Vorgabe %s — Nutzerschlüssel werden nicht benutzt",
			llm.FirmenmodusAnbieter, llm.ServerschluesselEnvVar, strings.Join(llmFirmenmodus.Modelle(), ","), llmPII)
		log.Printf("aboard-server: LLM-Firmenmodus: KI-Verläufe nur für ihren Ersteller sichtbar; höchstens %d Chat-Anfragen je Person und Stunde (%s)",
			llmAnfragenProStunde, api.LLMAnfragenEnvVar)
	} else {
		llm.RegisterDefaults(llmRegistry, llmUserAgent, openAIBaseURL)
		if llmPII != llm.PIINutzer {
			log.Printf("aboard-server: LLM-PII-Vorgabe des Betreibers: %s", llmPII)
		}
		// Kritiker Sprint 82 Runde 2, KO-3: „aus“ ohne Firmenmodus nicht still.
		if w := llm.PIIOhneFirmenmodusWarnung(llmPII, llmFirmenmodus.Aktiv()); w != "" {
			log.Print("aboard-server: " + w)
		}
	}
	if openAIBaseURL != "" {
		log.Printf("aboard-server: OpenAI-kompatibler Anbieter: Basis-URL %s", openAIBaseURL)
		// ausgang.Setze(politik) oben ist zu diesem Zeitpunkt bereits
		// gelaufen — Aktiv() liefert die tatsächlich geltende Politik, nicht
		// den Nullwert (der alles erlauben würde).
		for _, w := range openAIBasisWarnungen(openAIBaseURL, openAIModelle, ausgang.Aktiv(), llmFirmenmodus.Aktiv()) {
			log.Print(w)
		}
	}
	llmKeys := newLLMKeysRepoIfBoxed(db, configRepo)
	llmKeysAPI := api.NewLLMKeysAPI(llmKeys, userRepo, llmRegistry).WithOpenAIKompatibel(openAIBaseURL, openAIModelle).
		WithFirmenmodus(llmFirmenmodus, llmPII)
	llmKeysAPI.Register(mux)
	// #299/ADR-0095: POST /boards/{b}/cards/{c}/llm-send + GET /llm/pricing.
	// Liegt unter /boards/* → ACLGuard erzwingt editor+ (Sub-Resource-POST).
	// LockManager ist prozess-lokal (in-memory, Lauf-Kennung + Auffrischen, F-1538); ein
	// gemeinsamer LockManager über alle Routen, sonst kein Konflikt-Schutz.
	// Fehlt der Master-Key → keys=nil → handleSend antwortet 503.
	llmLocks := llm.NewLockManager(0)
	llmConvRepo := storage.NewLLMConversationRepo(db)
	// WithACL: nur für die Standort-Prüfung des Lese-Werkzeugs
	// fetch-related-card (#428/ADR-0120 §1a). Ohne dieses Repo wird das
	// Werkzeug nicht angeboten — die Routen-ACL macht weiterhin der ACLGuard.
	llmSendAPI := api.NewLLMSendAPI(boardRepo, userRepo, llmKeys, llmRegistry, llmLocks).
		WithConversations(llmConvRepo).
		WithACL(aclRepo).
		WithAttachments(attachStore).
		WithFirmenmodus(llmFirmenmodus, llmPII).
		WithAnfragenObergrenze(llmAnfragenProStunde)
	llmSendAPI.Register(mux)
	// #300/ADR-0096 Etappe 2: Conversation-CRUD-Endpoints unter /boards/{b}/cards/{c}/conversations.
	// Liegt unter /boards/* → ACLGuard enforct viewer+ (GET) bzw. editor+ (POST/DELETE);
	// Archive/Delete prüfen zusätzlich created_by-Match im Handler.
	// Karte #674: Im Firmenmodus sieht jede Person nur ihre eigenen Verläufe.
	llmConversationsAPI := api.NewLLMConversationsAPI(llmConvRepo, userRepo).WithFirmenmodus(llmFirmenmodus)
	llmConversationsAPI.Register(mux)
	// #293/ADR-0091: Karte als Mail versenden. Liegt unter /boards/* →
	// ACLGuard erzwingt viewer+ am Board. SMTP-Config kommt aus dem
	// ConfigStore (#306-Hülle wird in admin-dialog SMTP-Sektion befüllt).
	emailAPI := api.NewEmailAPI(boardRepo, userRepo, configRepo)
	emailAPI.Register(mux)

	// #296 — Cross-Board-Karten-Transfer (POST /boards/{b}/cards/{c}/transfer).
	// ACL: editor+ am Quell-Board (ACLGuard) + editor+ am Ziel-Board
	// (Handler-intern via aclRepo.GetRole). Atomic via BoardRepo.TransferCard
	// (BEGIN IMMEDIATE-Tx über beide Boards). Anhänge per Saga (Copy → DB →
	// Delete-Old, Rollback bei DB-Fehler) — siehe transfer.go Saga §1–§3.
	transferAPI := api.NewTransferAPI(boardRepo, userRepo, aclRepo).WithAttachments(attachStore)
	transferAPI.Register(mux)
	// #305/ADR-0107: Notifications-Subsystem — Event-Bus, Worker, Channels
	// (E-Mail + Gotify), Digest-Cron. Verschlüsselung für Gotify-Tokens läuft
	// über dieselbe secretcrypto.Box wie LLM-Keys/Config-Secrets. Fehlt der
	// Master-Key, bleibt der Bus aktiv (E-Mail-Channel funktioniert ohne
	// Secrets), aber Gotify-Set-Endpoints antworten 503.
	notifBox, _ := newSecretBox()
	notifSubsRepo := storage.NewNotificationSubscriptionRepo(db)
	notifConfigRepo := storage.NewNotificationConfigRepo(db, notifBox)
	notifLogRepo := storage.NewNotificationLogRepo(db, uuidString)
	notifBus := notifications.NewBus(log.Printf)
	// ADR-0136 §3: Hat ein Board-Owner oder Admin einen fremden Kommentar
	// gelöscht, erfährt es der Autor — nur er, nach seinen Einstellungen.
	// Ein Autor ohne Konto (oder ohne Stempel, Altbestand) bekommt nichts;
	// der Verlaufseintrag an der Karte steht trotzdem.
	boardsAPI.WithKommentarLoeschHinweis(kommentarLoeschHinweis(userRepo, boardRepo, aclRepo, notifBus.Emit))
	notifBaseURL := oeffentlicheBasis
	notifEmail := notifications.NewEmailSender(
		notifications.SMTPConfigFromConfigStore{Repo: configRepo},
		notifBaseURL,
		nil, /* mailpkg.SendMailContext */
	)
	notifGotify := notifications.NewGotifySender(notifConfigRepo, nil)
	// #420: Hauptschalter fuer den tatsaechlichen Versand. Default AUS
	// (Entscheid 2026-08-19) — die Kette laeuft vollstaendig durch,
	// nur der letzte Zoll wird unterdrueckt und protokolliert. Der Schalter
	// sitzt bewusst hier bei den SENDERN und nicht beim Ausloeser: sonst
	// gingen Mail-Ingest, Backup-Scheduler und jeder kuenftige
	// Server-Emitter daran vorbei.
	notifDelivery := notifications.DeliveryEnabled(os.Getenv(notifications.DeliveryEnvVar))
	// Die Umhuellung passiert in `BaueSender` und nicht hier: Als Funktion
	// ist die Zusage („bei ausgeschaltetem Schalter kein echter Sender in
	// der Liste") zur Laufzeit pruefbar. Die fruehere Inline-Schleife war
	// nur ueber einen Quelltext-Waechter abgesichert, und den hat der
	// Sprint-59-Kritiker ausgehebelt, ohne dass ein Test rot wurde.
	notifSenders := notifications.BaueSender(notifDelivery,
		[]notifications.Sender{notifEmail, notifGotify}, log.Printf)
	if !notifDelivery {
		// Nicht still: Wer den Schalter nicht kennt, sucht den Fehler sonst
		// in der Verdrahtung, waehrend die Verdrahtung stimmt.
		log.Printf("aboard-server: Notification-VERSAND IST AUS (%s nicht gesetzt) — "+
			"Events werden vollstaendig verarbeitet und protokolliert, aber nicht zugestellt. "+
			"Das gilt auch fuer die 18:00-Tagesuebersicht. Zum Einschalten: %s=on (siehe docs/handbuch/server/konfiguration.md)",
			notifications.DeliveryEnvVar, notifications.DeliveryEnvVar)
	} else {
		log.Printf("aboard-server: Notification-Versand ist AKTIV (%s=on) — Zustellung an echte Empfaenger",
			notifications.DeliveryEnvVar)
	}
	notifWorker := notifications.NewWorker(notifications.WorkerDeps{
		Bus:           notifBus,
		Subscriptions: notifSubsRepo,
		Configs:       notifConfigRepo,
		Logs:          notifLogRepo,
		Users:         notifications.UsersFromRepo{Repo: userRepo},
		Senders:       notifSenders,
		Logger:        log.Printf,
	})
	go notifWorker.Run(ctx)
	// Geordnetes Herunterfahren (F-521, Karte #421): `Run()`s eigener
	// `defer Stop()` läuft in einer ANDEREN Goroutine, ausgelöst durch
	// ctx.Done() — ohne dieses Defer hier würde run() (und damit der
	// Prozess) zurückkehren, während dort noch eine Mail unterwegs sein
	// kann. Shutdown() ruft Stop()+Wait() und bindet die Wartezeit an ein
	// EIGENES Budget (notifShutdownTimeout) — bewusst NICHT dieselbe
	// Obergrenze wie der HTTP-Shutdown weiter unten (httpShutdownTimeout),
	// sondern eine zweite, SEQUENZIELL nach dem HTTP-Shutdown ablaufende
	// Phase (F-729, Karte #509; Begründung + Worst-Case-Rechnung am
	// Konstanten-Block oben). Ein hängender SMTP-Server verzögert den Exit
	// damit höchstens um notifShutdownTimeout, nie unbegrenzt (F-380 bleibt
	// gültig: andere Empfänger blockieren dabei ohnehin nicht, siehe
	// worker.go).
	defer func() {
		if !notifWorker.Shutdown(notifShutdownTimeout) {
			log.Printf("aboard-server: notification-worker nicht innerhalb %s heruntergefahren — Rest laeuft im Hintergrund weiter", notifShutdownTimeout)
		}
	}()
	notifDigest := notifications.NewDigestScheduler(notifications.DigestDeps{
		Logs:  notifLogRepo,
		Users: notifications.UsersFromRepo{Repo: userRepo},
		Email: notifEmail,
		// #420 / Sprint-59-Kritiker [S] P1: Der Digest ist ein ZWEITER
		// Sendepfad und ging bis zur Nacharbeit am Schalter vorbei. Er
		// nutzt `SendDigest`, das nicht Teil des `Sender`-Interface ist —
		// die Umhuellung der Sender oben konnte ihn also gar nicht fassen.
		DeliveryEnabled: notifDelivery,
		Logger:          log.Printf,
	})
	notifDigestStop := notifDigest.Start(ctx)
	defer notifDigestStop()
	notifAPI := api.NewNotificationsAPI(notifSubsRepo, notifConfigRepo, notifLogRepo, userRepo, boardRepo, aclRepo, notifBus, notifGotify, notifEmail, notifDelivery)
	notifAPI.Register(mux)
	// Admin-Test-Hook: einen Test-Digest-Lauf auslösen.
	adminAPI.Registry().RegisterTest("notifications", func(testCtx context.Context, _ map[string]string) (any, error) {
		notifDigest.RunOnce(testCtx)
		return nil, nil
	})
	adminAPI.Registry().RegisterReload("notifications", func(_ context.Context, _ map[string]string) error {
		// Kein Reload nötig — Channel-Configs werden pro Send frisch geladen.
		return nil
	})
	// #303/ADR-0098: pro-Board-Backup (User-Feature, parallel zu server/backup).
	// REST-Endpoints + Scheduler. ACL: Owner ODER Admin.
	boardbackup.SetUUIDFunc(uuidString)
	boardBackupAPI := api.NewBoardBackupAPI(
		boardRepo, aclRepo, userRepo, activityRepo, configRepo, attachDir, aboardVersion,
	)
	// #336: Master-Key für Encryption-at-Rest durchreichen. Leer = keine
	// Verschlüsselung (dann greift ResolveCrypter und meldet, falls der
	// Operator sie eingeschaltet hat).
	boardBackupAPI.SetSecretKey(os.Getenv("ABOARD_SECRET_KEY"))
	// Karte #437 ([[F-709]]): Nach einer Rotation von ABOARD_SECRET_KEY liegt
	// hier der vorherige Wert. Er gilt NUR für den Restore — Backups werden
	// weiterhin ausschliesslich mit dem aktuellen Schlüssel geschrieben.
	boardBackupAPI.SetPreviousSecretKey(os.Getenv("ABOARD_SECRET_KEY_PREVIOUS"))
	boardBackupAPI.Register(mux)
	// Backup-Verzeichnis vorab anlegen — der Scheduler erwartet es.
	if err := api.EnsureBackupDir(configRepo, ctx); err != nil {
		log.Printf("aboard-server: board-backup-dir anlegen fehlgeschlagen: %v", err)
	}
	// Backup-Scheduler — startet eine Goroutine, kann via Reload neu
	// initialisiert werden.
	// meldeSubsystemAus schickt eine system.subsystem-off-Notification an alle
	// Server-Admins. Denselben Empfängerweg nutzt bereits OnFailure für
	// system.backup-failed (ListAdmins) — bewusst kein zweiter Mechanismus.
	//
	// Best effort und absichtlich nicht fatal: Ein Server, der wegen einer
	// fehlgeschlagenen Benachrichtigung nicht startet, wäre schlimmer als der
	// Zustand, den die Benachrichtigung meldet. Schlägt der Versand fehl,
	// bleibt die Log-Zeile — dieselbe Lage wie vor #420, nicht schlechter.
	// Der Zustand „falsch konfiguriert" ist DAUERHAFT, die Meldung ist
	// EREIGNISFÖRMIG (Sprint-59-Kritiker [K] P3). `startBoardBackupScheduler`
	// läuft bei jedem Prozessstart und zusätzlich bei jedem Admin-Reload;
	// Coalescence greift nicht, weil sie nur für Karten-Events gilt. Ein
	// Crash-Restart-Loop hätte damit eine Admin-Mail pro Neustart erzeugt,
	// bis nach 30 Stück die Drosselung greift — und die schiebt den Rest in
	// die 18:00-Übersicht, wo dieselbe Meldung noch einmal auftaucht.
	//
	// Das ist die entgegengesetzte Fehlerrichtung zu der, die F-719 beheben
	// sollte: aus „gar keine Meldung" würde „so viele, dass sie ignoriert
	// werden". Deshalb: einmal pro Subsystem — bis es wieder läuft.
	//
	// DER RÜCKWEG IST DER PUNKT (Phase-2b-Verify, [K] P2): Die erste Fassung
	// setzte das Flag und löschte es nie. `RegisterReload("backup_board")`
	// ruft `startBoardBackupScheduler` aber im SELBEN Prozess erneut auf —
	// ein Admin, der den Schlüssel nachträgt, repariert den Zustand ohne
	// Neustart. Eine zweite, unabhängige Störung danach wäre dann bis zum
	// nächsten Prozessende stumm geblieben: gemeldet wurde ja schon mal.
	// Ein Zustands-Flag ohne Rückweg ist genau die Klasse, die ADR-0115 zum
	// Anlass für diese Verify-Phase genommen hat — hier in der Nacharbeit an
	// einem Kritiker-Fund wieder eingebaut und vom Verify gefunden.
	//
	// `meldeSubsystemLaeuft` ist deshalb der Gegenpart und wird auf JEDEM
	// erfolgreichen Start gerufen, nicht nur nach einer Störung.
	subsystemDrossel := notifications.NewMeldungsDrossel()
	meldeSubsystemLaeuft := func(subsystem string) {
		if subsystemDrossel.Laeuft(subsystem) {
			log.Printf("aboard-server: %s laeuft wieder — kuenftige Stoerungen werden erneut gemeldet", subsystem)
		}
	}
	meldeSubsystemAus := func(subsystem, grund string) {
		if !subsystemDrossel.SollMelden(subsystem) {
			log.Printf("aboard-server: %s weiterhin aus (%s) — Meldung bereits verschickt", subsystem, grund)
			return
		}
		admins, aerr := userRepo.ListAdmins(ctx)
		if aerr != nil {
			log.Printf("aboard-server: notif subsystem-off: list admins: %v", aerr)
			return
		}
		if len(admins) == 0 {
			log.Printf("aboard-server: notif subsystem-off (%s): kein Admin hinterlegt — niemand wird benachrichtigt", subsystem)
			return
		}
		adminIDs := make([]string, 0, len(admins))
		for _, a := range admins {
			adminIDs = append(adminIDs, a.ID)
		}
		notifBus.Emit(notifications.Event{
			Type:                     notifications.EventSubsystemOff,
			Snippet:                  subsystem + " — " + grund,
			ExplicitRecipientUserIDs: adminIDs,
		})
	}

	// Empfänger-Ermittlung: Liste aller User mit is_admin=1. Geteilt von
	// Scheduler und Admin-Knopf „Alle Boards jetzt sichern" (#735).
	bbOnFailure := func(parent context.Context) func(boardID, boardTitle string, ferr error) {
		return func(boardID, boardTitle string, ferr error) {
			admins, aerr := userRepo.ListAdmins(parent)
			if aerr != nil {
				log.Printf("aboard-server: notif backup-failed: list admins: %v", aerr)
				return
			}
			adminIDs := make([]string, 0, len(admins))
			for _, a := range admins {
				adminIDs = append(adminIDs, a.ID)
			}
			notifBus.Emit(notifications.Event{
				Type:                     notifications.EventBackupFailed,
				BoardID:                  boardID,
				BoardName:                boardTitle,
				Snippet:                  ferr.Error(),
				ExplicitRecipientUserIDs: adminIDs,
			})
		}
	}
	boardBackupAPI.SetOnFailure(bbOnFailure(ctx))
	boardBackupAPI.SetBaseContext(ctx) // K-5: Knopf-Lauf endet mit dem Herunterfahren
	startBoardBackupScheduler := func(parent context.Context) func() {
		bbCfg := boardbackup.LoadConfig(parent, configRepo)
		if !bbCfg.Enabled {
			log.Printf("aboard-server: backup_board.enabled=false — Board-Backup-Scheduler deaktiviert")
			return func() {}
		}
		// #336: Crypter aus Config + Master-Key. Ein Fehler hier (Schalter an,
		// Schlüssel fehlt) darf den Scheduler NICHT unverschlüsselt
		// weiterlaufen lassen — er wird laut protokolliert und der Scheduler
		// bleibt aus, damit der Operator es merkt.
		bbCrypter, bbCryptErr := boardbackup.ResolveCrypter(bbCfg, os.Getenv("ABOARD_SECRET_KEY"))
		if bbCryptErr != nil {
			log.Printf("aboard-server: board-backup-Verschlüsselung nicht auflösbar — Scheduler bleibt AUS: %v", bbCryptErr)
			// #420: Ab hier entstehen KEINE Sicherungen mehr — und der
			// Weg, auf dem der Operator das sonst erführe, ist mit
			// abgeschaltet: `OnFailure` unten hängt am Scheduler, der
			// nie startet, also feuert system.backup-failed nie. Ohne
			// diese Meldung bleibt allein die Log-Zeile darüber, und
			// „keine Backups" ist der schlechteste Zustand, den ein
			// Backup-System still einnehmen kann.
			// Kurz und vorne das Handlungsrelevante (Sprint-59-Kritiker
			// [K] P2): Die frühere Fassung wiederholte, was der
			// eingebettete Fehler ohnehin sagt, und kam damit auf 328 Runen
			// — gekürzt auf das 200er-Limit fiel ausgerechnet „es werden
			// KEINE Sicherungen mehr erstellt" weg. (Die 328 meinen den
			// ALTEN Text; der hier ist genau deshalb kurz. Zwei Prüfläufe
			// haben die Zahl bestritten, nachdem sie den neuen gemessen
			// hatten.) Die Zusage selbst hängt nicht an der Zahl: Sie ist
			// in TestEmailBody_SubsystemOffKuerztDenGrundNicht verankert.
			meldeSubsystemAus("Board-Backup", fmt.Sprintf(
				"Es entstehen KEINE Sicherungen mehr. Grund: %v", bbCryptErr))
			return func() {}
		}
		// Ab hier ist die Konfiguration in Ordnung — den Meldungs-Merker
		// zurücksetzen, damit eine spätere Störung wieder gemeldet wird.
		meldeSubsystemLaeuft("Board-Backup")
		return boardbackup.StartSchedulerOnce(parent, boardbackup.SchedulerDeps{
			Crypter:            bbCrypter,
			EncryptionRequired: bbCfg.EncryptionEnabled,
			Boards:             boardRepo,
			ACL:                aclRepo,
			AttachmentsBaseDir: attachDir,
			BackupBaseDir:      bbCfg.Dir,
			KeepDays:           bbCfg.KeepDays,
			CronPattern:        bbCfg.Cron,
			AboardVersion:      aboardVersion,
			Logger:             log.Printf,
			// #305/ADR-0107: Backup-Failure → Notification an Admins.
			OnFailure: bbOnFailure(parent),
		})
	}
	boardBackupStop := startBoardBackupScheduler(ctx)
	defer func() { boardBackupStop() }()
	// Test-Hook: ein manuelles Backup des ersten Boards auslösen — bestätigt
	// Schreibrechte und Disk-Erreichbarkeit.
	adminAPI.Registry().RegisterTest("backup_board", func(testCtx context.Context, _ map[string]string) (any, error) {
		bbCfg := boardbackup.LoadConfig(testCtx, configRepo)
		if !bbCfg.Enabled {
			return nil, errors.New("backup_board.enabled=false")
		}
		boards, err := boardRepo.List(testCtx)
		if err != nil {
			return nil, fmt.Errorf("backup_board test: %w", err)
		}
		if len(boards) == 0 {
			return nil, errors.New("backup_board test: keine Boards vorhanden")
		}
		// Erstes Board nehmen — purely operational sanity check.
		first := boards[0]
		ts := time.Now().UTC()
		tmpPath := fmt.Sprintf("%s/%s/boardbackup-%s-%s-test.tar.gz",
			bbCfg.Dir, first.ID, first.ID, ts.Format("20060102-150405"))
		testCrypter, tcErr := boardbackup.ResolveCrypter(bbCfg, os.Getenv("ABOARD_SECRET_KEY"))
		if tcErr != nil {
			return nil, tcErr
		}
		res, err := boardbackup.BackupBoard(testCtx, boardbackup.Options{
			BoardID:            first.ID,
			Boards:             boardRepo,
			ACL:                aclRepo,
			AttachmentsBaseDir: attachDir,
			Out:                tmpPath,
			AboardVersion:      aboardVersion,
			Crypter:            testCrypter,
		})
		if err != nil {
			return nil, err
		}
		log.Printf("aboard-server: backup_board test-hook ok board=%s file=%s sha=%s",
			first.ID, res.Filename, res.SHA256[:8])
		// Karte #333: das Prüf-Ergebnis gehört in die Antwort, nicht nur ins Log.
		// Sprint-53-Kritiker zu Recht: Für backup_board gibt es im Admin-Dialog
		// (noch) KEINEN Test-Auslöser — der Backup-Tab arbeitet direkt gegen
		// /boards/{id}/backup/*. Das Ergebnis ist hier also für API-Konsumenten
		// und eine künftige UI-Anbindung da, nicht für einen heute klickbaren
		// Knopf. Der Hook selbst ist über POST /admin/config/backup_board/test
		// erreichbar.
		return map[string]any{
			"board_id": first.ID,
			"filename": res.Filename,
			"sha256":   res.SHA256,
		}, nil
	})
	adminAPI.Registry().RegisterReload("backup_board", func(_ context.Context, _ map[string]string) error {
		boardBackupStop()
		boardBackupStop = startBoardBackupScheduler(ctx)
		return nil
	})
	// ADR-0129 §5/Karte #572: periodischer Aufräumer für LLM-Verläufe
	// (llm_conversations/llm_turns, ABOARD_LLM_RETENTION_DAYS) und das
	// Audit-Log (server_config_audit, ABOARD_AUDIT_RETENTION_DAYS) — Pendant
	// zur Aktivitäten-Frist (Karte #554), die pro Schreibvorgang trimmt. Hier
	// genügt ein periodischer Lauf: Beide Tabellen wachsen langsam, und die
	// Frist ist tage-genau — ein Abräumen bei jedem Turn bzw. Audit-Eintrag
	// wäre Aufwand ohne Nutzen (server/retention/scheduler.go). Initial-Cleanup läuft kurz nach
	// dem Start (retention.DefaultErsterLaufNach, 10 min — F-1135: erst nach
	// dem NTP-Abgleich), nicht erst beim ersten Tick — sonst würde die
	// Eviction bei häufigen Neustarts (Deploys) nie laufen (dieselbe Lehre
	// wie beim Digest-Scheduler, Sprint-57-Kritiker).
	retentionScheduler := retention.New(retention.Deps{
		LLM:     llmConvRepo,
		Audit:   configRepo,
		OpIDs:   storage.NewOpIDRepo(db),
		Journal: storage.NewBoardRepo(db),
		Logger:  log.Printf,
	})
	retentionStop := retentionScheduler.Start(ctx)
	defer retentionStop()
	// #294/ADR-0092: POST /mail-spam-report — User-getriebener Spam-Workflow
	// am Service-Board. Liegt außerhalb /boards/* — der Handler prüft selbst
	// editor+ am Service-Board.
	spamAPI := api.NewSpamAPI(spamRepo, aclRepo, userRepo)
	spamAPI.Register(mux)
	// Bootstrap-Import der SMTP_*-Env-Vars in den ConfigStore (one-shot;
	// idempotent — Bestands-DB-Werte werden NICHT überschrieben).
	if err := mailpkg.BootstrapEnv(ctx, configRepo, log.Printf); err != nil {
		log.Printf("aboard-server: SMTP-Env-Bootstrap fehlgeschlagen: %v", err)
	}
	// SMTP-Test-Hook beim Admin-Subsystem registrieren (#306/ADR-0101 §4).
	// ADR-0138 §2: Instanz-Zeitzone der Uhrzeiten im Abo-Feed prüfen.
	adminAPI.Registry().RegisterTest("kalender", api.KalenderZeitzonenTest)
	adminAPI.Registry().RegisterTest("smtp", func(ctx context.Context, _ map[string]string) (any, error) {
		cfg, _ := mailpkg.LoadConfig(ctx, configRepo)
		return nil, mailpkg.TestConnection(cfg)
	})
	// Reload-Hook (ADR-0091 §5, Sprint-40-Kritiker [D]P2): noop, weil
	// SendMail die SMTP-Konfig pro Send neu lädt — es gibt keinen Client-
	// Singleton, der re-initialisiert werden müsste. Registrierung trotzdem
	// dafür, dass der `POST /admin/config/reload`-Aufruf für "smtp" eine
	// Bestätigung liefert (statt der Sektion im Result-Map zu fehlen).
	adminAPI.Registry().RegisterReload("smtp", func(_ context.Context, _ map[string]string) error {
		return nil
	})
	// #294/ADR-0092 §6: Service-Board-Test-Hook — "Service-Board re-erzeugen"
	// (idempotent: bestehende Karten bleiben). Lädt aktuelle Config aus dem
	// Store (Sektion-Test übergibt nur Plain-Werte, aber LoadConfig kennt
	// die Defaults und holt den Title bei Bedarf nach).
	adminAPI.Registry().RegisterTest("service-board", func(ctx context.Context, _ map[string]string) (any, error) {
		cfg := servicepkg.LoadConfig(ctx, configRepo)
		return nil, servicepkg.Seed(ctx, boardRepo, aclRepo, cfg, log.Printf)
	})
	// Reload-Hook: Re-Seed bei Config-Änderung (z. B. neuer Titel oder
	// ENABLED-Toggle). Idempotent + fehlertolerant — bestehende Karten
	// bleiben unangetastet. SpamDrop wird vom Mail-Ingest (#295) pro Polling-
	// Tick frisch geladen, daher hier kein Re-Init nötig.
	adminAPI.Registry().RegisterReload("service-board", func(ctx context.Context, _ map[string]string) error {
		cfg := servicepkg.LoadConfig(ctx, configRepo)
		return servicepkg.Seed(ctx, boardRepo, aclRepo, cfg, log.Printf)
	})
	// #295/ADR-0093: IMAP-Mail-Ingest-Worker. ENV→DB-Bootstrap, dann Worker-
	// Goroutine starten — nur wenn imap.enabled=true und Config valide ist.
	// Filter-Pipeline:
	//   - PreFilter: Eigenversand (smtp.from) — verhindert Loop, wenn unser
	//     eigener Versand per Mail-Forward zurückkommt.
	//   - SpamDetector: Blocklist (mail_spam_senders) + Rate-Limit (Default
	//     20/h pro Sender, konfigurierbar über imap.rate_limit_per_hour —
	//     F-370/ADR-0093 §7; 0 schaltet das Limit aus). Treffer → col-spam
	//     (oder Drop bei service-board.spam_drop=true).
	// Reload-Hook stoppt den alten Worker und startet einen neuen — Config-
	// Änderungen ohne Container-Restart.
	if err := mailpkg.BootstrapIMAPEnv(ctx, configRepo, log.Printf); err != nil {
		log.Printf("aboard-server: IMAP-Env-Bootstrap fehlgeschlagen: %v", err)
	}
	imapStateRepo := storage.NewImapStateRepo(db)
	imapProcessedRepo := storage.NewImapProcessedRepo(db)
	autoReplyLogRepo := storage.NewAutoReplyLogRepo(db) // #297: Lifetime-Cap-Tabelle
	startIMAPWorker := func(workerCtx context.Context) func() bool {
		imapCfg := mailpkg.LoadIMAPConfig(workerCtx, configRepo)
		if !imapCfg.Enabled {
			log.Printf("aboard-server: imap.enabled=false — Mail-Ingest deaktiviert")
			return func() bool { return true }
		}
		if err := imapCfg.Validate(); err != nil {
			log.Printf("aboard-server: IMAP-Config ungültig — Worker startet nicht: %v", err)
			return func() bool { return true }
		}
		smtpCfg, _ := mailpkg.LoadConfig(workerCtx, configRepo)
		svcCfg := servicepkg.LoadConfig(workerCtx, configRepo)
		autoCfg, _ := mailpkg.LoadAutoReplyConfig(workerCtx, configRepo)
		var autoReplyDeps *mailpkg.AutoReplyDeps
		if autoCfg.Enabled {
			autoReplyDeps = &mailpkg.AutoReplyDeps{
				SMTP:    smtpCfg,
				AutoCfg: autoCfg,
				LogRepo: autoReplyLogRepo,
				Logger:  log.Printf,
			}
		}
		deps := mailpkg.WorkerDeps{
			Config:    imapCfg,
			Boards:    boardRepo,
			State:     imapStateRepo,
			Processed: imapProcessedRepo,
			PreFilters: []mailpkg.IngestFilter{
				mailpkg.EigenversandFilter(smtpCfg.From),
			},
			SpamDetectors: []mailpkg.IngestFilter{
				mailpkg.BlocklistFilter(workerCtx, spamRepo),
				// F-370: Wert aus dem ConfigStore (imap.rate_limit_per_hour,
				// Default 20) statt Literal — ADR-0093 §7 führt ihn als
				// Konfigurations-Eigenschaft.
				mailpkg.RateLimitFilter(imapCfg.RateLimitPerHour),
			},
			ServiceBoardEnabled: svcCfg.Enabled,
			SpamDrop:            svcCfg.SpamDrop,
			Attachments:         attachStore, // F-358: Mail-Anhänge im Storage-Volume persistieren
			Logger:              log.Printf,
			AutoReply:           autoReplyDeps, // #297: opt-in Auto-Reply (nil = deaktiviert)
			// #305/ADR-0107: Service-New-Card → Notification-Event (Push an
			// abonnierte Admins/Service-Editoren). Bus ist im closure
			// referenziert, daher kein nil-Risiko.
			OnServiceCardCreated: func(boardID, cardID, cardTitle, sender string, displayID int) {
				notifBus.Emit(notifications.Event{
					Type:          notifications.EventServiceNewCard,
					BoardID:       boardID,
					BoardName:     "Service-Board",
					CardID:        cardID,
					CardDisplayID: displayID,
					CardTitle:     cardTitle,
					ActorEmail:    sender,
					Snippet:       cardTitle,
				})
			},
		}
		return starteMitWarten(workerCtx, func(runCtx context.Context) {
			if err := mailpkg.RunWorker(runCtx, deps); err != nil {
				log.Printf("aboard-server: imap-worker beendet mit Fehler: %v", err)
			}
		}, imapStopTimeout, log.Printf)
	}
	imapHalter := &workerHalter{}
	_ = imapHalter.Neustart(func() func() bool { return startIMAPWorker(ctx) })
	defer imapHalter.Stop()
	adminAPI.Registry().RegisterTest("imap", func(_ context.Context, _ map[string]string) (any, error) {
		cfg := mailpkg.LoadIMAPConfig(ctx, configRepo)
		return nil, mailpkg.TestIMAPConnection(cfg)
	})
	// F-1856/O-4: Neustart mit Warten auf den alten Worker, serialisiert (imapworker.go).
	adminAPI.Registry().RegisterReload("imap", func(_ context.Context, _ map[string]string) error {
		return imapReload(imapHalter,
			func() mailpkg.IMAPConfig { return mailpkg.LoadIMAPConfig(ctx, configRepo) },
			func() func() bool { return startIMAPWorker(ctx) })
	})
	// #297: Mail-Auto-Reply — ConfigStore-Sektion + Test-Hook (Template-Render mit Demo-Daten).
	adminAPI.Registry().RegisterTest("mail_auto_reply", func(ctx context.Context, _ map[string]string) (any, error) {
		autoCfg, err := mailpkg.LoadAutoReplyConfig(ctx, configRepo)
		if err != nil {
			return nil, err
		}
		// Template mit Demo-Daten rendern — kein realer SMTP-Send.
		// Karte #333 (AK13 aus #297): das Gerenderte geht zurück an den
		// Aufrufer, damit der Admin-Dialog Subject + Body zeigen kann statt
		// nur "Template gerendert".
		// Demo-Daten: der ROH-Betreff einer eingegangenen Mail und der daraus
		// abgeleitete Kartentitel sind zwei verschiedene Dinge (siehe
		// RenderAutoReplyPreview). Für die Vorschau werden beide bewusst so
		// gewählt, wie sie im Echtbetrieb zueinander stünden.
		subject, body, err := mailpkg.RenderAutoReplyPreview(
			autoCfg, 42, "Re: Drucker geht nicht", "Drucker geht nicht", "aBoard")
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"rendered_subject": subject,
			"rendered_body":    body,
		}, nil
	})
	adminAPI.Registry().RegisterReload("mail_auto_reply", func(_ context.Context, _ map[string]string) error {
		// Kein eigener State: Die Auto-Reply-Config wird beim Start des IMAP-Workers
		// gelesen (startIMAPWorker). Den Neustart erledigt der Hook „imap“ — der
		// Admin-Dialog ruft ihn beim Speichern gezielt (`reload?section=imap`, F-1856);
		// dieser Hook hier bestätigt nur die Sektion bei einem Reload aller Bereiche,
		// bei dem „imap“ im selben Lauf feuert.
		return nil
	})
	// ADR-0044/0051 (F-248): eingebettetes Server-Frontend unter "/".
	// Go-1.22-ServeMux: die obigen Muster sind spezifischer und behalten
	// Vorrang — die API wird durch den Catch-all nicht geschattet.
	mux.Handle("/", web.Handler())

	// ADR-0057 (Karte #73): optionale Forward-Auth-Middleware-Stack.
	// Reihenfolge: Dev-Fake-Inject (falls aktiv) → Forward-Auth-Read.
	// Ist ABOARD_TRUST_FORWARD_AUTH nicht gesetzt, sind beide Wrapper No-Ops
	// und das Verhalten ist identisch zu vorher (Default = auth-loser Modus).
	//
	// SICHERHEITSHINWEIS: ABOARD_TRUST_FORWARD_AUTH darf nur aktiviert werden,
	// wenn der Container ausschließlich intern gebunden ist (127.0.0.1 oder
	// Unix-Socket) — sonst Header-Spoofing möglich.
	// ADR-0059 (Karte #198): ACL-Enforcement direkt um den mux. Liegt in der
	// Kette NACH withForwardAuth (Identität muss im Context sein) und VOR dem
	// mux. Ohne Forward-Auth-Identität ist der Guard ein No-op (Legacy-Bypass,
	// ADR-0059 §2) — daher unbedingt verdrahtet, keine eigene Env-Var nötig.
	// Im strengen Modus (ABOARD_TRUST_FORWARD_AUTH=1, ADR-0129 §2, F-1124)
	// weist withForwardAuth Requests ohne Identität bereits selbst mit 401
	// ab, bevor sie hier ankommen — der Legacy-Bypass-Zweig im Guard bleibt
	// dann nur für Trust=AUS (auth-loser Einzelbetrieb) und die beiden
	// offenen Pfade relevant (/healthz, /ical/calendar.ics), die ohnehin
	// nicht unter /boards liegen.
	aclGuard := api.NewACLGuard(aclRepo, userRepo, bootstrapRepo)
	mitAgentPfad, err := agentPfadKonfig()
	if err != nil {
		return err
	}
	// ADR-0148 (Karten #1051, #816): eingebaute Anmeldung — Sitzungen,
	// Zulassung, /anmeldung/start und /anmeldung/rueckruf. Ausserhalb des
	// OIDC-Betriebs keine Option, die Kette bleibt wie bisher.
	// ADR-0149 §5.3 (Karte #1076): Einladungen über die Admin-API, nur im
	// eingebauten Anmeldebetrieb (Basis ist dort Pflicht und geprüft).
	if api.AnmeldungOIDCAktiv() {
		adminAPI.WithEinladungen(storage.NewEinladungRepo(db), oeffentlicheBasis)
	}
	kettenOpts, err := anmeldungEinrichten(ctx, db, mux, oeffentlicheBasis, adminAPI)
	if err != nil {
		return err
	}
	handler := hauptHandler(mux, aclGuard.Middleware, agentZugang, mitAgentPfad, kettenOpts...)
	if mitAgentPfad {
		log.Printf("aboard-server: Agenten-Zugang auch unter %s/ am Menschen-Listener (nur Bearer-Tokens, Positivliste; %s=aus schaltet ihn ab)", AgentPfadPraefix, AgentPfadEnvVar)
		// Session-Kritiker Sprint 78, K-4: Hinter einem Proxy ohne
		// ABOARD_AGENT_PROXY zählt die Fehlversuch-Bremse alle Clients unter der
		// Proxy-Adresse — zehn Fehlversuche irgendeines Clients sperrten dann
		// alle Alt-Tokens und Anfragen ohne bekannte Kennung.
		if os.Getenv(api.AgentProxyEnvVar) == "" {
			log.Printf("aboard-server: WARNUNG: %s ist leer — die Fehlversuch-Bremse des Agenten-Zugangs zählt je TCP-Gegenstelle. "+
				"Steht ein Proxy davor, zählen alle Clients gemeinsam unter seiner Adresse. Proxy eintragen (DNS-Name wie „proxy“), "+
				"und der Proxy setzt X-Real-IP.", api.AgentProxyEnvVar)
		}
	}

	// Timeouts gegen Slowloris-Varianten (F-197). Upload-lastige Endpoints (#74,
	// Sprint 22) bekommen ggf. eigene Handler-Timeouts via http.TimeoutHandler.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)

	stopAgent, err := starteAgentListener(mux, agentZugang, listenAddr, unixSocket, sockModus, serverErr)
	if err != nil {
		return err
	}

	// F-1181: Der Listener für Menschen (Unix-Socket oder TCP) startet in
	// starteMenschListener — wie der Agenten-Listener, damit auch sein
	// chmod-Fehlerpfad zur Laufzeit getestet ist. Scheitert der Start, wird
	// der schon laufende Agenten-Listener wieder gestoppt.
	if err := starteMenschListener(srv, listenAddr, unixSocket, sockModus,
		fmt.Sprintf("db=%s, attachments=%s", dbPath, attachDir), serverErr); err != nil {
		if stopAgent != nil {
			stopAgent(context.Background())
		}
		return err
	}

	select {
	case <-ctx.Done():
		log.Printf("aboard-server: Shutdown-Signal empfangen")
	case err := <-serverErr:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("aboard-server: erzwungener Shutdown: %v", err)
		_ = srv.Close()
	}
	if stopAgent != nil {
		stopAgent(shutdownCtx)
	}
	// Unix-Socket-Datei nach Shutdown aufräumen.
	if unixSocket != "" {
		_ = entferneAltenSocket(unixSocket)
	}
	return nil
}

// starteMenschListener bindet den Listener für Menschen und startet srv
// darauf (F-1181, aus run() gezogen). Mit unixSocket ein Unix-Domain-Socket
// (ADR-0057, Karte #73): eine übrig gebliebene Socket-Datei wird vorher
// entfernt (sonst EADDRINUSE nach einem Neustart), danach bekommt der Socket
// den Modus aus ABOARD_UNIX_SOCKET_MODE (Voreinstellung 0660, damit Caddy
// bzw. www-data in derselben Gruppe verbinden kann; #577). Ohne unixSocket
// TCP auf listenAddr, wie vor ADR-0057.
//
// Scheitert Binden oder chmod, ist nichts gestartet, und es bleibt keine
// Socket-Datei liegen: Go entfernt eine per net.Listen("unix") angelegte
// Datei beim Close selbst — der Fehlerpfad muss dieses Close nur erreichen.
// Das Ende von srv.Serve (nil nach Shutdown) meldet die Goroutine über
// serverErr.
func starteMenschListener(srv *http.Server, listenAddr, unixSocket string, sockModus socketModus, info string, serverErr chan<- error) error {
	netz, adresse, anzeige := "tcp", listenAddr, listenAddr
	if unixSocket != "" {
		netz, adresse, anzeige = "unix", unixSocket, "unix:"+unixSocket
		if err := entferneAltenSocket(unixSocket); err != nil {
			return err
		}
	}
	ln, err := net.Listen(netz, adresse)
	if err != nil {
		if netz == "unix" {
			return fmt.Errorf("unix-socket %q: %w", unixSocket, err)
		}
		return err
	}
	if netz == "unix" {
		if err := sockModus.anwenden(unixSocket); err != nil {
			_ = ln.Close()
			return err
		}
	}
	go func() {
		log.Printf("aboard-server %s: lauscht auf %s (%s) — Frontend unter /, API unter /boards, Health /healthz",
			api.VersionLine(), anzeige, info)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()
	return nil
}

// uhrZustand beschreibt fürs Start-Log, ob Frist-Löschungen laufen dürfen
// (F-1192). Eigene Funktion, damit der Text testbar ist.
func uhrZustand() string {
	if os.Getenv(uhr.PruefungEnvVar) == "aus" {
		return "Uhr-Prüfung aus (" + uhr.PruefungEnvVar + "=aus) — Frist-Löschungen laufen ohne Blick auf die Zeitsynchronisation"
	}
	ok, err := uhr.Gestellt()
	switch {
	case err != nil:
		return fmt.Sprintf("Systemuhr: Status nicht lesbar (%v) — Frist-Löschungen warten; ohne Zeitdienst %s=aus setzen", err, uhr.PruefungEnvVar)
	case !ok:
		return "Systemuhr noch nicht gestellt — Frist-Löschungen warten, bis ein Zeitdienst sie stellt"
	default:
		return "Systemuhr gestellt — Frist-Löschungen aktiv"
	}
}

// openAIBaseURLWarnung liefert die Warnzeile, wenn pol den Host der
// konfigurierten OpenAI-Basis-URL bei ABOARD_AUSGAENGE=aus sperrt — sonst ""
// (kein Log). Eigene Funktion (statt Inline in run()), damit Karte #570
// (ADR-0129 §5) testbar ist, ohne den ganzen Server zu starten
// (main_llm_baseurl_test.go). Bricht den Start NICHT ab: Der Ausgangs-
// Schalter ist die stärkere Regel — der Anbieter bleibt gesperrt, alles
// andere läuft, und das Log sagt, warum. Beide Variablen werden nur beim
// Start gelesen; eine nachgetragene Freigabe braucht einen Neustart.
func openAIBaseURLWarnung(baseURL string, pol *ausgang.Politik) string {
	// (Einzelprüfung; openAIBasisWarnungen sammelt alle Warnungen.)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		// Kann hier praktisch nicht passieren — baseURL kommt bereits durch
		// llm.ValidateOpenAIBaseURL. Defensiv: kein Crash, keine Warnung.
		return ""
	}
	if aerr := pol.PruefeURL(u.Host, u.Scheme); aerr != nil {
		return fmt.Sprintf("aboard-server: WARNUNG: OpenAI-kompatibler Anbieter über %s ist bei ABOARD_AUSGAENGE=aus nicht erreichbar (%v) — %s in ABOARD_AUSGAENGE_ERLAUBT aufnehmen",
			baseURL, aerr, u.Host)
	}
	return ""
}

// openAIBasisWarnungen sammelt die Start-Warnungen zur Basis-URL (Karte #570,
// Session-Kritiker K-B2/K-B6): gesperrt durch den Ausgangs-Schalter; Klartext
// (`http`) zu einem Rechner, der nicht dieser ist — der Bearer-Schlüssel der
// Nutzer ginge unverschlüsselt durchs Netz; kein Pfad (vLLM erwartet `/v1`);
// keine Modellliste (die Oberfläche böte sonst OpenAI-Modellnamen an, die das
// Gegenüber nicht kennt).
//
// Kritiker K2-8 (Sprint 82): Im Firmenmodus geht über diese Verbindung nicht
// der Schlüssel einzelner Nutzer, sondern der SERVERSCHLÜSSEL des Betreibers
// — mit jeder Chat-Anfrage aller Personen. Die Klartext-Warnung sagt dann
// das; der Start läuft trotzdem weiter (ein internes vLLM ohne TLS ist ein
// bewusster Betriebsfall).
func openAIBasisWarnungen(baseURL string, modelle []string, pol *ausgang.Politik, firmenmodus bool) []string {
	if baseURL == "" {
		return nil
	}
	var w []string
	if s := openAIBaseURLWarnung(baseURL, pol); s != "" {
		w = append(w, s)
	}
	if u, err := url.Parse(baseURL); err == nil && u.Scheme == "http" && !istLoopback(u.Hostname()) && firmenmodus {
		w = append(w, fmt.Sprintf("aboard-server: WARNUNG: Firmenmodus über http://%s — der SERVERSCHLÜSSEL (%s) geht mit jeder Chat-Anfrage aller Personen im Klartext durchs Netz (auch über einen HTTP_PROXY); wer mitliest, kann ihn auf Kosten des Betreibers nutzen. https verwenden oder die Strecke anders absichern (eigenes Netz, Tunnel)", u.Host, llm.ServerschluesselEnvVar))
	} else if err == nil && u.Scheme == "http" && !istLoopback(u.Hostname()) {
		w = append(w, fmt.Sprintf("aboard-server: WARNUNG: OpenAI-kompatibler Anbieter über http://%s — die LLM-Schlüssel der Nutzer gehen im Klartext durchs Netz (auch über einen HTTP_PROXY); https verwenden, wo möglich", u.Host))
	}
	if llm.OpenAIBasisOhnePfad(baseURL) {
		w = append(w, "aboard-server: WARNUNG: ABOARD_LLM_OPENAI_BASE_URL hat keinen Pfad — ein vLLM erwartet meist /v1 (z. B. http://llm.example:8000/v1), sonst antworten /models und /chat/completions mit 404")
	}
	if len(modelle) == 0 {
		w = append(w, "aboard-server: WARNUNG: ABOARD_LLM_OPENAI_MODELLE ist leer — die Oberfläche bietet dann OpenAI-Modellnamen an, die das Gegenüber vermutlich nicht kennt; die Modellnamen des Gegenübers eintragen (bei vLLM --served-model-name)")
	}
	return w
}

// llmBetreiberVorgaben liest Firmenmodus (ABOARD_LLM_SERVERSCHLUESSEL_FILE)
// und PII-Vorgabe (ABOARD_LLM_PII) — Karte #664, ADR-0133 §2. Eigene
// Funktion, damit ein Test die Startprüfungen ohne ganzen Server prüfen kann.
// Ohne beide Variablen: (nil, "nutzer", nil) — heutiges Verhalten.
func llmBetreiberVorgaben(openAIBaseURL string, openAIModelle []string) (*llm.Firmenmodus, llm.PIIVorgabe, error) {
	fm, err := llm.FirmenmodusAusDatei(os.Getenv(llm.ServerschluesselEnvVar), openAIModelle)
	if err != nil {
		return nil, "", err
	}
	pii, err := llm.ParsePIIVorgabe(os.Getenv(llm.PIIEnvVar), fm.Aktiv(), openAIBaseURL)
	if err != nil {
		return nil, "", err
	}
	return fm, pii, nil
}

func istLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// oeffentlicheBasisGeprueft prüft ABOARD_PUBLIC_BASE_URL einmal für alle
// Verbraucher (Werkzeug-Links und Mail-Links, F-1559). Ungültig: Warnung und
// leerer Wert (keine Links) — ein Start-Abbruch träfe bestehende Deployments,
// die den Wert vorher ungeprüft für Mails nutzten.
func oeffentlicheBasisGeprueft(roh string, logf func(string, ...any)) string {
	basis, err := api.ValidiereOeffentlicheBasis(roh)
	if err != nil {
		logf("aboard-server: WARNUNG: %v — Werkzeug-Antworten und Mails tragen keinen Board-Link", err)
		return ""
	}
	return basis
}

// unterbefehle sind die Wörter, die main() als ersten Parameter kennt.
var unterbefehle = []string{"import", "backup", "restore", "agent-token", "acl", "nutzer", "boardpaar", "healthcheck", "help"}

// pruefeUnbekanntenUnterbefehl lehnt ein erstes Argument ab, das einem
// bekannten Unterbefehl zum Verwechseln ähnlich ist (F-1934 K-1): höchstens
// zwei Bearbeitungsschritte, bei kurzen Unterbefehlen (`acl`, `help`) einer —
// sonst träfe schon „ab“ oder „run“. Andere Wörter bleiben erlaubt — der Server
// startet dann wie bisher, siehe main().
func pruefeUnbekanntenUnterbefehl(wort string) error {
	if strings.HasPrefix(wort, "-") {
		return nil
	}
	for _, u := range unterbefehle {
		grenze := 2
		if len(u) < 5 {
			grenze = 1
		}
		if editierAbstand(strings.ToLower(wort), u) <= grenze {
			return fmt.Errorf("unbekannter Unterbefehl %q — gemeint %q? (aboard-server help)", wort, u)
		}
	}
	return nil
}

// editierAbstand ist der Levenshtein-Abstand zweier Zeichenketten (Runen).
func editierAbstand(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	vor := make([]int, len(rb)+1)
	for j := range vor {
		vor[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		akt := make([]int, len(rb)+1)
		akt[0] = i
		for j := 1; j <= len(rb); j++ {
			kosten := 1
			if ra[i-1] == rb[j-1] {
				kosten = 0
			}
			akt[j] = min(vor[j]+1, akt[j-1]+1, vor[j-1]+kosten)
		}
		vor = akt
	}
	return vor[len(rb)]
}

// keineRestArgumente lehnt Positionsargumente jenseits der erwarteten ab
// (F-1934). Go `flag` hört am ersten Positionsargument auf; alles dahinter —
// auch `--dry-run` oder `--role viewer` — bliebe sonst ungelesen, und der
// Befehl liefe mit den Voreinstellungen statt mit dem, was dasteht.
func keineRestArgumente(fs *flag.FlagSet, erwartet int) error {
	if fs.NArg() <= erwartet {
		return nil
	}
	rest := fs.Args()[erwartet:]
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unerwartete Argumente %v — Optionen gehören vor die Positionsargumente, sie wurden nicht ausgewertet", rest)
		}
	}
	return fmt.Errorf("unerwartete Argumente: %v", rest)
}

func envOrDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// newSecretBox liest ABOARD_SECRET_KEY und liefert eine *secretcrypto.Box
// + Operator-Status-String (leer wenn alles OK, sonst Log-Zeile). Wird
// gemeinsam von ConfigRepo (#306) und LLMKeyRepo (#298) genutzt — derselbe
// Master-Key, kein zweiter.
//
// Fehlender/ungültiger Key → Box=nil, ConfigStore läuft im Read-Only-Modus
// für Secrets, LLM-Keys-Endpoints antworten 503.
func newSecretBox() (*secretcrypto.Box, string) {
	masterKey := os.Getenv("ABOARD_SECRET_KEY")
	if masterKey == "" {
		return nil, "ABOARD_SECRET_KEY nicht gesetzt — server_config-Secrets + LLM-Keys deaktiviert. Setze 32 Bytes hex oder base64, um Secrets/Keys persistieren zu können."
	}
	box, err := secretcrypto.New(masterKey)
	if err != nil {
		return nil, fmt.Sprintf("ABOARD_SECRET_KEY ungültig: %v — Secrets/LLM-Keys deaktiviert", err)
	}
	return box, ""
}

// newLLMKeysRepoIfBoxed liefert das LLMKeyRepo nur, wenn ein Master-Key
// konfiguriert ist. Sonst nil — der Handler antwortet dann 503.
// configRepo dient nur als Box-Träger; LLMKeyRepo nutzt secretBox direkt.
func newLLMKeysRepoIfBoxed(db *sql.DB, configRepo *storage.ConfigRepo) *storage.LLMKeyRepo {
	if configRepo == nil || !configRepo.SecretsEnabled() {
		return nil
	}
	// Re-import der Box aus der Env-Var (configRepo hält sie privat;
	// wir lesen sie hier erneut, statt eine Getter-Schnittstelle aufzumachen).
	box, _ := newSecretBox()
	if box == nil {
		return nil
	}
	return storage.NewLLMKeyRepo(db, box)
}

// uuidString liefert eine neue UUID als String — wrap, damit die uuid-Dep
// nicht in storage-Tests leaked (storage bleibt uuid-frei, Paket-Konvention
// in UserRepo/BootstrapRepo).
func uuidString() string { return uuid.NewString() }

// menschenHandler baut die Kette des Menschen-Listeners um den mux. Eigene
// Funktion, damit ein Test die Verdrahtung prüfen kann, nicht nur die
// einzelnen Middlewares (Sprint-72-Abschluss, S72-1).
//
// Identitätsquelle (ADR-0148 §1): Im OIDC-Betrieb (ABOARD_ANMELDUNG=oidc)
// sitzt die Sitzungsweiche an der Stelle von Forward-Auth; Forward-Auth und
// Dev-Fake werden dann gar nicht verdrahtet. Fehlt die Weiche in den Optionen,
// wird eine leere eingesetzt, die niemanden hereinlässt (fail-closed).
func menschenHandler(mux http.Handler, acl func(http.Handler) http.Handler, opts ...menschenOption) http.Handler {
	var k menschenKonfig
	for _, o := range opts {
		o(&k)
	}
	handler := acl(mux)
	if api.AnmeldungOIDCAktiv() {
		weiche := k.sitzung
		if weiche == nil {
			weiche = &api.Sitzungsweiche{}
		}
		handler = weiche.Middleware(handler)
	} else {
		handler = api.WithForwardAuthIfEnabled(handler)
		handler = api.WithDevFakeHeadersIfEnabled(handler)
	}
	// K-10 (Karte #553): Bearer-Tokens gehören nur auf den Agenten-Listener.
	handler = api.OhneBearer(handler)
	// Karte #778 (ADR-0140 §2): Board-Paar-Tokens (abs_/abe_) haben an der
	// Menschen-Kette keinen Weg — überall 403.
	handler = api.BoardpaarSchranke(handler, false)
	// S72-1: CSRF-Schutz als äußerste Schicht — ein fremder Browser-Request
	// wird abgewiesen, bevor Identität oder ACL überhaupt ausgewertet werden.
	return api.MitCSRFSchutz(handler)
}

// menschenKonfig: Teile der Menschen-Kette, die nur in einer Betriebsart
// gebraucht werden.
type menschenKonfig struct {
	sitzung *api.Sitzungsweiche
}

// menschenOption setzt einen Teil von menschenKonfig.
type menschenOption func(*menschenKonfig)

// mitSitzungsweiche hängt die Anmelde-Weiche des OIDC-Betriebs ein (wirkt
// nur bei ABOARD_ANMELDUNG=oidc).
func mitSitzungsweiche(w *api.Sitzungsweiche) menschenOption {
	return func(k *menschenKonfig) { k.sitzung = w }
}

// kommentarLoeschHinweis: Hat ein Board-Owner oder Admin einen fremden
// Kommentar gelöscht, erfährt es der Autor — nur er, nach seinen
// Einstellungen (ADR-0136 §3). Ein Autor ohne Konto (oder ohne Stempel,
// Altbestand) bekommt nichts; der Verlaufseintrag an der Karte steht
// trotzdem. F-1614: auch keiner, der am Board keine Rolle (mehr) hat — der
// Worker prüft bei ExplicitRecipientUserIDs nur das Abo, nicht die ACL, und
// die Meldung nennt Board, Karte und Textanfang.
func kommentarLoeschHinweis(users *storage.UserRepo, boards *storage.BoardRepo, acl *storage.AclRepo,
	emit func(notifications.Event)) func(context.Context, api.KommentarGeloescht) {
	return func(ctx context.Context, g api.KommentarGeloescht) {
		if g.AutorEmail == "" {
			return
		}
		email := storage.NormalizeEmail(g.AutorEmail)
		u, err := users.GetByEmail(ctx, email)
		if err != nil || u == nil {
			return
		}
		// Lesefehler = keine Rolle: im Zweifel keine Meldung.
		if _, found, err := acl.GetRole(ctx, g.BoardID, email); err != nil || !found {
			return
		}
		boardName := ""
		if b, err := boards.Get(ctx, g.BoardID); err == nil && b != nil {
			boardName = b.Title
		}
		emit(notifications.Event{
			Type:                     notifications.EventCommentDeleted,
			ActorEmail:               g.Loescher,
			BoardID:                  g.BoardID,
			BoardName:                boardName,
			CardID:                   g.CardID,
			CardDisplayID:            g.CardDisplayID,
			CardTitle:                g.CardTitle,
			Snippet:                  g.Textanfang,
			ExplicitRecipientUserIDs: []string{u.ID},
		})
	}
}
