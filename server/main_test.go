package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/notifications"
)

// TestCurrentSchemaVersion_NichtVorhanden stellt sicher, dass die
// Schema-Versions-Probe bei nicht existierender DB 0 liefert UND keinerlei
// Dateien anlegt (F-239: kein storage.Open-Side-Effect mehr — vorher legte
// storage.Open Verzeichnis + DB + WAL/SHM am Restore-Ziel an).
func TestCurrentSchemaVersion_NichtVorhanden(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")
	dbPath := filepath.Join(dbDir, "aboard.db")

	if v := currentSchemaVersion(dbPath); v != 0 {
		t.Fatalf("currentSchemaVersion = %d, erwartet 0 (DB existiert nicht)", v)
	}

	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbDir} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("currentSchemaVersion hat %q angelegt — erwartet: kein Side-Effect", p)
		}
	}
}

// TestCurrentSchemaVersion_LiestVersion prüft, dass eine vorhandene DB ihre
// user_version read-only (ohne Migration) zurückgemeldet bekommt.
func TestCurrentSchemaVersion_LiestVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("DB anlegen: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatalf("user_version setzen: %v", err)
	}
	db.Close()

	if v := currentSchemaVersion(dbPath); v != 7 {
		t.Fatalf("currentSchemaVersion = %d, erwartet 7", v)
	}
}

// TestAboardVersion_HaengtAnBuildVersion nagelt Karte #347 fest.
//
// Sprint-53-Kritiker: Der gesamte Server-Anteil der Karte liess sich
// zurückbauen, ohne dass ein einziger Test rot wurde — nachgewiesen in einer
// Repo-Kopie. `aboardVersion` war vorher eine ZWEITE, nie gesetzte Variable;
// Backups und der Admin-Status meldeten deshalb dauerhaft "dev", obwohl der
// Build eine Version hatte. Genau dieser Rückfall soll auffallen.
//
// WARUM QUELLTEXT STATT LAUFZEITVERGLEICH (Sprint-53-Verify-Lauf):
// Die erste Fassung prüfte `aboardVersion != api.BuildVersion` zur Laufzeit.
// Das ist TAUTOLOGISCH: ohne `-X`-Ldflags — der Normalfall unter `go test` —
// stehen beide auf "dev". Der Verify-Lauf hat den alten Fehler exakt
// nachgebaut (`var aboardVersion = "dev"`, entkoppelt) und der Test blieb
// GRÜN. Ein Test, der den Bug nicht fangen kann, gegen den er antritt, ist
// schlimmer als keiner: er erzeugt Sicherheit, die es nicht gibt.
//
// Deshalb prüft er jetzt die QUELLE. Der Rückfall besteht ja gerade darin,
// die Kopplung im Code zu lösen — und genau die ist hier sichtbar.
func TestAboardVersion_HaengtAnBuildVersion(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	const erwartet = "var aboardVersion = api.BuildVersion"
	if !strings.Contains(string(quelle), erwartet) {
		t.Fatalf("main.go enthält %q nicht mehr.\n"+
			"aboardVersion muss aus api.BuildVersion abgeleitet bleiben — sonst melden "+
			"Backup-Metadaten und Admin-Status wieder einen anderen (meist veralteten) "+
			"Wert als /healthz, und zwar unbemerkt: Dockerfile und CI setzen "+
			"ausschliesslich api.BuildVersion.", erwartet)
	}
	// Zusätzlich die Laufzeit-Seite — schwach (siehe oben), aber sie fängt den
	// Fall ab, dass jemand die Zeile stehen lässt und woanders überschreibt.
	if aboardVersion != api.BuildVersion {
		t.Errorf("aboardVersion = %q, api.BuildVersion = %q — jemand überschreibt die Ableitung",
			aboardVersion, api.BuildVersion)
	}
}

// TestLLMUserAgent_OhneVersionsangabe sichert F-593 ab (Karte #394).
//
// Bis Sprint 54 lautete der User-Agent an externe LLM-Provider
// "aboard-server/"+aboardVersion. Solange aboardVersion auf "dev" festhing,
// blieb das folgenlos; seit Karte #347 ging die exakte Patch-Version an
// Anthropic, OpenAI, Google und Mistral.
//
// Der Test prüft BEWUSST die Quelltext-Stelle mit, nicht nur den Wert der
// Konstante. Grund: In der Testumgebung ist api.BuildVersion "dev" — ein
// reiner Wert-Test auf "enthält keine Versionsangabe" wäre auch mit der
// ALTEN Bildung grün gewesen ("aboard-server/dev" enthält keine Ziffern) und
// hätte die Regression damit nie gefangen. Genau die Sorte Test, die dieses
// Projekt sich in Sprint 53 wiederholt vorgehalten hat.
func TestLLMUserAgent_OhneVersionsangabe(t *testing.T) {
	// Teil 1 — Eigenschaft des Werts: keine Versionsangabe, kein Commit-Hash.
	if strings.ContainsAny(llmUserAgent, ".") {
		t.Errorf("llmUserAgent %q enthält einen Punkt — riecht nach Versionsangabe", llmUserAgent)
	}
	for _, verboten := range []string{"dev", api.BuildVersion, api.BuildCommit} {
		if verboten != "" && verboten != "1" && strings.Contains(llmUserAgent, verboten) {
			t.Errorf("llmUserAgent %q enthält %q — Build-Angaben gehören nicht an Dritte", llmUserAgent, verboten)
		}
	}

	// Teil 2 — Struktur: der Aufruf darf die Version nicht wieder einmischen.
	// Das ist der Teil, der die Regression tatsächlich fängt.
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	zeile := ""
	for _, z := range strings.Split(string(quelle), "\n") {
		if strings.Contains(z, "llm.RegisterDefaults(") {
			zeile = z
			break
		}
	}
	if zeile == "" {
		t.Fatal("Aufruf llm.RegisterDefaults( in main.go nicht gefunden — " +
			"der Test prüft eine Stelle, die es nicht mehr gibt, und wäre sonst still grün")
	}
	for _, verboten := range []string{"aboardVersion", "BuildVersion", "BuildCommit"} {
		if strings.Contains(zeile, verboten) {
			t.Errorf("RegisterDefaults-Aufruf mischt %q in den User-Agent: %s", verboten, strings.TrimSpace(zeile))
		}
	}
}

// TestNotifWorkerShutdown_IstVerdrahtetMitObergrenze nagelt Karte #421/F-521
// fest: main.go muss den Notification-Worker beim Prozess-Exit ordentlich
// herunterfahren. Ohne den `notifWorker.Shutdown(...)`-Aufruf kehrt run()
// zurück, während `Run()`s eigene Aufräum-Goroutine (ausgelöst durch
// ctx.Done(), siehe worker.go) noch auf offene Sende-Lanes wartet — eine
// Mail kann dann mitten im Versand abreissen, unbeobachtet.
//
// WARUM QUELLTEXT STATT LAUFZEITVERGLEICH (wie schon
// TestAboardVersion_HaengtAnBuildVersion und TestLLMUserAgent_... oben):
// Die drei neuen Tests in notifications/worker_test.go (TestWorker_Shutdown*)
// rufen Worker.Shutdown() selbst auf und prüfen damit die FUNKTION — nicht
// den AUFRUFER. Das ist exakt die Lücke, die F-521 ursprünglich beschreibt:
// "Der zugehörige Test bleibt grün, weil er die Funktion prüft, nie den
// Aufrufer." Wer `defer notifWorker.Shutdown(...)` aus main.go streicht,
// bekäme ohne diesen Test einen vollständig grünen restlichen Testlauf —
// Shutdown() selbst bliebe ja weiterhin korrekt, nur riefe sie niemand mehr.
// Deshalb liest dieser Test main.go als Quelltext, statt etwas zur Laufzeit
// zu vergleichen.
//
// GRENZE DIESES TESTS (Fehlerklasse 25 — ein Wächter ohne kommentierte
// Grenze wird für mehr gehalten, als er leistet): Er beweist NICHT, dass der
// Shutdown zur Laufzeit tatsächlich funktioniert, nicht, dass die Obergrenze
// sinnvoll dimensioniert ist, und nicht, dass der Aufruf an der richtigen
// Stelle im Kontrollfluss steht (z. B. wirklich vor Prozess-Exit läuft). Er
// belegt ausschliesslich, dass die Verdrahtung nicht STILLSCHWEIGEND entfernt
// wurde. Das Laufzeitverhalten von Shutdown() selbst (inkl. Timeout-Fall)
// deckt die notifications-Paket-Suite ab (TestWorker_Shutdown*).
func TestNotifWorkerShutdown_IstVerdrahtetMitObergrenze(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}

	/* Kommentarzeilen ausblenden, bevor gesucht wird (Sprint-59-Kritiker
	   [V] P3): Sonst hielte ein erklärender Kommentar, der den Aufruf
	   zitiert, den Wächter grün, während der Aufruf selbst weg ist. */
	ohneKommentare := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(quelle), "")

	/* `[^)]*` bricht an der ERSTEN schliessenden Klammer ab — bei
	   `Shutdown(time.Duration(0))` landete deshalb `time.Duration(0` im
	   Argument, und die „ist es 0?"-Prüfung darunter lief ins Leere. Der
	   Kritiker hat genau diesen Rückfall als durchrutschend gemeldet.
	   `.*?\)+\s*$` bis zum Zeilenende ist hier tragfähig, weil der Aufruf
	   auf einer eigenen Zeile steht. */
	re := regexp.MustCompile(`notifWorker\.Shutdown\(([^\n]*?)\)\s*(?:\{|$)`)
	match := re.FindStringSubmatch(ohneKommentare)
	if match == nil {
		t.Fatal("main.go ruft notifWorker.Shutdown(...) nicht mehr auf.\n" +
			"Ohne diesen Aufruf wartet der Prozess-Exit nicht mehr auf offene " +
			"Notification-Lanes (F-521/Karte #421): der Server kann beenden, " +
			"während eine Mail noch im Versand steckt, und der Worker reisst " +
			"seine Lane-Goroutinen unkontrolliert ab statt sie geordnet " +
			"abzuwickeln.")
	}

	// Mittelweg für die Obergrenzen-Prüfung: bewusst NICHT an den
	// Bezeichner "notifShutdownTimeout" gebunden — der darf beim nächsten
	// Umbenennen frei wechseln, ohne dass dieser Test bricht (das wäre reine
	// String-Klebrigkeit ohne Erkenntnisgewinn). Bestanden wird stattdessen
	// darauf, dass ÜBERHAUPT ein Argument übergeben wird und dass es nicht
	// das Literal "0" ist: Shutdown() ohne Argument (Kompilierfehler bei der
	// aktuellen Signatur, aber ein Refactor könnte das Argument optional
	// machen) und Shutdown(0) sind exakt der Rückfall, vor dem Worker.
	// Shutdown()s eigener Kommentar warnt — ein hängender SMTP-Server (F-380)
	// dürfte den Prozess-Exit dann wieder unbegrenzt aufhalten, statt nur die
	// eigene Lane.
	arg := strings.TrimSpace(match[1])
	/* Jede Schreibweise einer Null zählt als fehlende Obergrenze — `0`,
	   `time.Duration(0)`, `0*time.Second`. Die erste Fassung prüfte nur das
	   nackte Literal. */
	istNull := regexp.MustCompile(`^(0|time\.Duration\(\s*0\s*\)|0\s*\*\s*time\.\w+)$`)
	if arg == "" || istNull.MatchString(arg) {
		t.Fatalf("notifWorker.Shutdown(%s) hat keine erkennbare Obergrenze > 0 — "+
			"ohne eine echte Timeout-Dauer blockiert ein hängender SMTP-Server "+
			"(F-380) den Prozess-Exit wieder unbegrenzt, statt nur die eigene "+
			"Lane zu verzögern. Erwartet ein Argument wie notifShutdownTimeout.", arg)
	}
}

// TestBackupSchedulerAus_MeldetEsStattNurZuLoggen sichert den Phase-1-Fund aus
// Karte #420 (Nachtrag zu #336).
//
// Steht `backup_board.encryption_enabled=true`, fehlt aber der Master-Key,
// bleibt der Backup-Scheduler bewusst aus — ein unverschlüsseltes
// Ersatz-Backup wäre eine stille Herabstufung des Schutzniveaus. Der Haken
// war ein anderer: `OnFailure` (und damit `system.backup-failed`) hängt AM
// Scheduler. Läuft der nie, erfährt der Operator nie, dass ab diesem Moment
// überhaupt keine Sicherungen mehr entstehen — ausser aus einer Log-Zeile
// beim Serverstart, die niemand liest. „Keine Backups" ist der schlechteste
// Zustand, den ein Backup-System still einnehmen kann.
//
// WARUM QUELLTEXT (dieselbe Begründung wie bei den Tests oben, und hier
// besonders): Der Zweig wird nur beim Serverstart mit einer bestimmten
// Fehlkonfiguration betreten. Ihn zur Laufzeit zu erreichen hiesse, `run()`
// samt DB, Migrationen und Scheduler hochzufahren — dieses Repo tut das
// nirgends. Der Rückfall besteht ohnehin genau darin, den Aufruf wieder aus
// dem Zweig zu entfernen, und das ist im Quelltext sichtbar.
//
// GRENZE: Der Test belegt NICHT, dass die Meldung ankommt, dass die
// Empfängerliste stimmt oder dass der Text brauchbar ist. Er belegt, dass der
// Fehlerzweig nicht wieder auf blosses Loggen zurückfällt.
//
// Beim Gegenprüfen fiel auf, dass Go hier zur Hälfte mitschützt: Nimmt man
// nur den Aufruf heraus, wird `meldeSubsystemAus` „declared and not used" und
// der Build bricht. Der realistische Rückbau entfernt aber den Helfer gleich
// mit — und der baut wieder sauber durch. Genau diesen Fall fängt der Test
// (nachgemessen: mit entferntem Aufruf und stillgelegtem Helfer ist er rot,
// der Build grün). Das Verhalten von
// `meldeSubsystemAus` selbst ist über den Ereignistyp im notifications-Paket
// getestet (TestEventSubsystemOff_*, TestBuildSummary_SubsystemOff*).
func TestBackupSchedulerAus_MeldetEsStattNurZuLoggen(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	text := string(quelle)

	// Den Fehlerzweig isolieren: von der ResolveCrypter-Prüfung bis zum
	// nächsten `return`. Nur was DORT steht, zählt — ein `meldeSubsystemAus`
	// irgendwo sonst in der Datei würde diesen Zweig nicht retten.
	start := strings.Index(text, "if bbCryptErr != nil {")
	if start == -1 {
		t.Fatal("Der ResolveCrypter-Fehlerzweig in startBoardBackupScheduler wurde nicht gefunden.\n" +
			"Wurde er umgebaut? Dann prüfen, ob der Fall „Verschlüsselung an, Schlüssel fehlt\" " +
			"weiterhin eine Benachrichtigung auslöst und nicht nur eine Log-Zeile — sonst laufen " +
			"ab diesem Zustand stillschweigend gar keine Sicherungen mehr (Karte #420).")
	}
	ende := strings.Index(text[start:], "return func() {}")
	if ende == -1 {
		t.Fatal("Ende des ResolveCrypter-Fehlerzweigs nicht gefunden — Test an den Umbau anpassen")
	}
	zweig := text[start : start+ende]

	if !strings.Contains(zweig, "meldeSubsystemAus(") {
		t.Fatal("Der ResolveCrypter-Fehlerzweig meldet nicht mehr per meldeSubsystemAus(...).\n" +
			"Ohne diese Meldung fällt der Zustand „Verschlüsselung an, Schlüssel fehlt\" auf eine " +
			"blosse Log-Zeile zurück: Der Scheduler bleibt aus (richtig so), aber system.backup-failed " +
			"kann nicht feuern, weil OnFailure am nie gestarteten Scheduler hängt. Der Operator " +
			"erfährt dann nicht, dass überhaupt keine Sicherungen mehr entstehen (Karte #420).")
	}
	// Der Grund muss mit — eine Meldung „Subsystem aus" ohne das Warum lässt
	// den Operator genauso ratlos zurück wie gar keine.
	if !strings.Contains(zweig, "bbCryptErr") {
		t.Error("Die Meldung im Fehlerzweig reicht den zugrunde liegenden Fehler (bbCryptErr) nicht weiter — " +
			"der Operator erfährt dann nicht, WARUM das Subsystem aus ist.")
	}
}

// TestNotifVersandSchalter_IstVerdrahtet sichert die Auflage aus der
// Entscheidung des Maintainers vom 2026-08-19 (Karte #420): Der Versand ist standardmässig AUS,
// und der Schalter sitzt bei den SENDERN.
//
// WARUM DIESER TEST NEU GESCHRIEBEN WURDE: Die erste Fassung prüfte drei
// Zeichenketten in `main.go` (`DeliveryEnabled(`, `NewSuppressedSender(`, und
// die Abwesenheit eines Literals). Der Sprint-59-Kritiker hat sie ausgehebelt,
// indem er `notifSenders = wrapped` durch `_ = wrapped` ersetzte: Der Worker
// bekam die rohen Sender — also genau der Zustand, den der Wächter verhindern
// sollte — `go vet` blieb sauber, und der Test blieb GRÜN. Ein Wächter, der
// drei Zeichenketten prüft, prüft drei Zeichenketten, nicht die Zusage.
//
// Deshalb liegt die Verdrahtung jetzt in `notifications.BaueSender` und wird
// hier zur LAUFZEIT geprüft: „bei ausgeschaltetem Schalter enthält das
// Ergebnis keinen einzigen echten Sender." Das ist eine Behauptung über
// Werte, die ein Rückbau nicht überlebt.
//
// Der Quelltext-Anteil bleibt als zweite, schwächere Sicherung bestehen — er
// deckt ab, dass `main.go` die Funktion überhaupt noch benutzt. Beide
// zusammen tragen: der eine prüft die Semantik, der andere die Anwesenheit.
func TestNotifVersandSchalter_IstVerdrahtet(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	text := string(quelle)

	if !strings.Contains(text, "notifications.DeliveryEnabled(") {
		t.Fatal("main.go wertet notifications.DeliveryEnabled(...) nicht mehr aus — " +
			"der Versand-Schalter aus Karte #420 ist damit wirkungslos und es geht Post an " +
			"echte Empfänger, sobald SMTP konfiguriert ist.")
	}
	// Auf die ZUWEISUNG prüfen, nicht nur auf das Vorkommen: Der Rückfall,
	// den der Kritiker gebaut hat, bestand genau darin, das Ergebnis
	// wegzuwerfen.
	verdrahtet := regexp.MustCompile(`notifSenders\s*(:?)=\s*notifications\.BaueSender\(`)
	if !verdrahtet.MatchString(text) {
		t.Fatal("main.go weist das Ergebnis von notifications.BaueSender(...) nicht mehr " +
			"notifSenders zu. Der Worker bekäme dann die echten Sender, und bei " +
			"ausgeschaltetem Schalter ginge Post an echte Empfänger (Karte #420).")
	}
	if !strings.Contains(text, "Senders:       notifSenders") &&
		!strings.Contains(text, "Senders: notifSenders") {
		t.Fatal("Der Worker bekommt seine Sender nicht mehr aus notifSenders — " +
			"der Schalter ist umgangen (Karte #420).")
	}
	// Der Digest ist ein ZWEITER Sendepfad und ging bis zur Sprint-59-
	// Nacharbeit am Schalter vorbei ([S] P1). Er benutzt `SendDigest`, das
	// nicht Teil des Sender-Interface ist — die Umhüllung der Sender kann
	// ihn also gar nicht fassen.
	if !strings.Contains(text, "DeliveryEnabled: notifDelivery") {
		t.Fatal("Der Digest-Scheduler bekommt den Versand-Schalter nicht mehr übergeben. " +
			"Er sendet dann um 18:00 die gedrosselten Ereignisse an echte Empfänger, " +
			"obwohl der Versand aus ist — genau die Lücke, die der Sprint-59-Kritiker " +
			"als [S] P1 gefunden hat.")
	}
}

// TestBaueSender_UnterdruecktJedenEchtenSender ist der Laufzeit-Teil der
// Zusage aus Karte #420 — die Prüfung, die ein Rückbau nicht überlebt.
func TestBaueSender_UnterdruecktJedenEchtenSender(t *testing.T) {
	echt := []notifications.Sender{
		&testSender{kanal: notifications.ChannelEmail},
		&testSender{kanal: notifications.ChannelGotify},
	}

	aus := notifications.BaueSender(false, echt, nil)
	if len(aus) != len(echt) {
		t.Fatalf("BaueSender(false) liefert %d Sender, erwartet %d — die Kanalliste des "+
			"Workers muss dieselbe bleiben, sonst fehlt ein Kanal im Trockenbetrieb komplett",
			len(aus), len(echt))
	}
	for i, s := range aus {
		if s == echt[i] {
			t.Fatalf("BaueSender(false)[%d] ist der ECHTE Sender — bei ausgeschaltetem "+
				"Schalter ginge Post an echte Empfänger (Karte #420)", i)
		}
		if s.Channel() != echt[i].Channel() {
			t.Errorf("Kanal %d heisst %q statt %q — der Worker matcht Sender über den Namen",
				i, s.Channel(), echt[i].Channel())
		}
		if err := s.SendOne(context.Background(), "u", "wohin@example.com", "N",
			notifications.Event{Type: notifications.EventCardAssigned}); err != nil {
			t.Errorf("unterdrückter Sender %d meldet Fehler %v — er muss Erfolg melden, "+
				"sonst läuft der Worker in seinen Retry-Backoff", i, err)
		}
	}
	for i, s := range echt {
		if s.(*testSender).aufrufe != 0 {
			t.Errorf("echter Sender %d wurde %d× gerufen, erwartet 0", i, s.(*testSender).aufrufe)
		}
	}

	// Und die Gegenrichtung: eingeschaltet muss die Liste unverändert
	// durchgereicht werden, sonst wäre der Trockenbetrieb der Normalfall.
	an := notifications.BaueSender(true, echt, nil)
	for i := range an {
		if an[i] != echt[i] {
			t.Fatalf("BaueSender(true)[%d] ist nicht der echte Sender — eingeschaltet "+
				"muss unverändert zugestellt werden", i)
		}
	}
}

type testSender struct {
	kanal   string
	aufrufe int
}

func (t *testSender) Channel() string { return t.kanal }
func (t *testSender) SendOne(context.Context, string, string, string, notifications.Event) error {
	t.aufrufe++
	return nil
}

// TestNotifShutdownTimeout_DecktEinenSendversuchAb sichert Karte #509 (F-729)
// gegen den Rückfall ab, den der Sprint-59-Kritiker gefunden hat: Bis dahin
// war `notifShutdownTimeout` (damals: dieselbe Konstante wie der
// HTTP-Shutdown, `shutdownGracePeriod` = 10s) KLEINER als
// `notifications.SendTimeout` (15s). Ein Empfänger, dessen Sendeversuch
// GENAU beim Shutdown-Signal beginnt, konnte dann im besten Fall (kein
// Netzwerkproblem, keine Retries) nie fertig werden, bevor Worker.Shutdown()
// selbst schon aufgegeben hatte — "geordnetes Herunterfahren" hätte in
// diesem Fall STRUKTURELL nie true zurückgeben können.
//
// Der Test bindet sich bewusst an die echten Konstanten (nicht an einen
// nachgebauten Wert) — genau das ist die Eigenschaft, die geprüft werden
// soll: notifShutdownTimeout muss ECHT größer sein als SendTimeout, mit
// Marge für Stop()/Wait()/Logging danach, nicht nur größer-gleich.
func TestNotifShutdownTimeout_DecktEinenSendversuchAb(t *testing.T) {
	if notifShutdownTimeout <= notifications.SendTimeout {
		t.Fatalf("notifShutdownTimeout (%s) ist nicht größer als notifications.SendTimeout (%s) — "+
			"ein Sendeversuch, der genau beim Shutdown-Signal beginnt, passt dann selbst im "+
			"besten Fall nicht mehr ins Budget (F-729)", notifShutdownTimeout, notifications.SendTimeout)
	}
}

// TestShutdownBudget_DockerComposeDecktWorstCaseAb sichert die zweite Hälfte
// von Karte #509 (F-729) ab: Der Prozess-Exit braucht im Worst Case
// httpShutdownTimeout + notifShutdownTimeout (zwei SEQUENZIELLE Phasen,
// siehe Kommentar am Konstanten-Block in main.go) — ohne ein passendes
// `stop_grace_period` in docker-compose.yml killt Docker den Prozess nach
// seinem Default von 10s per SIGKILL, mitten im Notification-Worker-
// Shutdown. Genau das war der Zustand, den der Sprint-59-Kritiker fand: gar
// kein `stop_grace_period` gesetzt.
//
// WARUM QUELLTEXT (wie die anderen Tests in dieser Datei): docker-compose.yml
// ist Konfiguration, kein Code, den `go test` ausführen könnte — die einzige
// Prüfmöglichkeit ist, die Datei zu lesen und den gesetzten Wert gegen die
// tatsächlichen Konstanten zu rechnen. Ein rein händisch nachgetragener Wert
// (z. B. ein Kommentar "40s reichen") würde beim nächsten Anheben von
// notifShutdownTimeout lautlos veralten; dieser Test rechnet bei jedem Lauf
// neu nach.
func TestShutdownBudget_DockerComposeDecktWorstCaseAb(t *testing.T) {
	worstCase := httpShutdownTimeout + notifShutdownTimeout

	quelle, err := os.ReadFile(filepath.Join("..", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("docker-compose.yml nicht lesbar: %v", err)
	}

	// aboard-server-Service isolieren, damit ein stop_grace_period beim
	// (aktuell abgeschalteten) Backup-Service nicht fälschlich zählt.
	text := string(quelle)
	serviceStart := strings.Index(text, "aboard-server:")
	if serviceStart < 0 {
		t.Fatal("docker-compose.yml: Service \"aboard-server\" nicht gefunden")
	}
	serviceEnde := strings.Index(text[serviceStart:], "\n  aboard-backup:")
	var abschnitt string
	if serviceEnde < 0 {
		abschnitt = text[serviceStart:]
	} else {
		abschnitt = text[serviceStart : serviceStart+serviceEnde]
	}

	re := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*([0-9]+)s\s*$`)
	match := re.FindStringSubmatch(abschnitt)
	if match == nil {
		t.Fatalf("docker-compose.yml setzt für den aboard-server-Service kein "+
			"stop_grace_period (erwartetes Format 'Ns') — ohne dieses Feld killt Docker "+
			"den Prozess nach seinem Default von 10s, mitten im Notification-Worker-Shutdown "+
			"(Worst Case im Code: %s, F-729)", worstCase)
	}
	sekunden, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("stop_grace_period-Wert %q nicht als Ganzzahl-Sekunden lesbar: %v", match[1], err)
	}
	stopGrace := time.Duration(sekunden) * time.Second

	if stopGrace <= worstCase {
		t.Fatalf("stop_grace_period=%s deckt den Worst Case httpShutdownTimeout+notifShutdownTimeout=%s "+
			"nicht ab (keine Marge oder sogar kleiner) — Docker würde den Prozess genau in dem Fenster "+
			"per SIGKILL beenden, das der Code selbst für einen geordneten Shutdown vorsieht (F-729)",
			stopGrace, worstCase)
	}
}
