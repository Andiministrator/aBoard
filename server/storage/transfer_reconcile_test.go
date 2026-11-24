package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// Die Tests zu F-600 stellen den Fall nach, den die vorhandene Fehlerbehandlung
// NICHT erreicht: Der Prozess stirbt hart (OOM-Kill, SIGKILL, Node-Drain)
// zwischen der erfolgreichen DB-Transaktion und `staged.Commit()`.
//
// „Absturz" heisst hier: Saga §1 läuft (StageCardAttachments), danach wird das
// Handle weggeworfen — weder Commit noch Abort. Genau das hinterlässt ein
// Prozessende.
//
// Die Datenbank wird über eine Attrappe vertreten (kartenOrt), weil der
// Abgleich von ihr nur eine einzige Auskunft braucht: in welchem Board die
// Karte liegt. Die echte Umsetzung dieser Auskunft ist `BoardFuerKarte`.

// altern setzt Staging-Verzeichnis und Metadatei auf ein Alter, das im
// Abgleichs-Fenster liegt: älter als stagingMinAge (sonst gilt der Rest als
// möglicherweise laufender Transfer eines Parallelprozesses), jünger als
// stagingMaxAge (sonst gilt er als abgebrochener Versuch).
//
// Das ist keine Test-Kosmetik, sondern die realistische Lage: Zwischen dem
// Absturz und dem Neustart, bei dem der Abgleich läuft, vergeht immer Zeit.
// Ein Test, der ein sekundenaltes Staging vorlegt, prüft einen Zustand, den
// es im Betrieb nicht gibt.
func altern(t *testing.T, baseDir string) {
	t.Helper()
	root := filepath.Join(baseDir, transferStagingRoot)
	eintraege, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("Staging-Root lesen: %v", err)
	}
	zeit := time.Now().Add(-(stagingMinAge + time.Minute))
	for _, e := range eintraege {
		pfad := filepath.Join(root, e.Name())
		if err := os.Chtimes(pfad, zeit, zeit); err != nil {
			t.Fatalf("Chtimes %s: %v", e.Name(), err)
		}
	}
}

// kartenOrt baut eine KartenOrtFn aus einer festen Zuordnung.
func kartenOrt(zuordnung map[string]string) KartenOrtFn {
	return func(cardId string) (string, error) {
		return zuordnung[cardId], nil
	}
}

// TestReconcile_F600_KarteImZiel_StelltFertig — der Kernfall: die DB-Tx ist
// durch (Karte liegt laut Datenbank im Ziel-Board), der Anhang-Commit fehlt.
// Der Abgleich muss das Staging FERTIGSTELLEN, nicht wegräumen.
//
// Ohne den Abgleich sah der Nutzer: Karte im Ziel-Board, Anhänge weg. Die
// Dateien lagen noch am Quell-Pfad, aber das wusste niemand — und
// sweepStaleStaging räumte die Staging-Kopie nach 24 h kommentarlos weg.
func TestReconcile_F600_KarteImZiel_StelltFertig(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__rechnung.pdf")
	dstDir := filepath.Join(baseDir, "dst-board", "card-uuid")

	// Saga §1, danach „stürzt der Server ab": Handle wird verworfen.
	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Fatalf("Vorbedingung verletzt: Ziel-Pfad existiert schon nach Saga §1")
	}

	// Neustart. Die Datenbank sagt: die Karte IST im Ziel-Board.
	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		"card-uuid": "dst-board",
	}))

	if bericht.Fertiggestellt != 1 {
		t.Errorf("Fertiggestellt = %d, erwartet 1 (Bericht: %+v)", bericht.Fertiggestellt, bericht)
	}
	if bericht.Verworfen != 0 {
		t.Errorf("Verworfen = %d, erwartet 0 — der Abgleich hat weggeräumt statt fertigzustellen "+
			"(Bericht: %+v)", bericht.Verworfen, bericht)
	}

	// Der eigentliche Nachweis: die Anhänge sind am Ziel-Pfad SICHTBAR.
	got, err := os.ReadFile(filepath.Join(dstDir, "att1__rechnung.pdf"))
	if err != nil {
		t.Fatalf("Anhang am Ziel-Pfad fehlt — genau der F-600-Zustand: %v", err)
	}
	if string(got) != "inhalt-att1__rechnung.pdf" {
		t.Errorf("Inhalt am Ziel: %q", string(got))
	}

	// Und das Staging ist restlos weg, samt Metadatei — sonst meldete der
	// nächste Start denselben Rest erneut.
	rest, _ := os.ReadDir(filepath.Join(baseDir, transferStagingRoot))
	if len(rest) != 0 {
		namen := make([]string, 0, len(rest))
		for _, e := range rest {
			namen = append(namen, e.Name())
		}
		t.Errorf("Staging-Root nicht leer nach dem Fertigstellen: %v", namen)
	}
}

// TestReconcile_F600_KarteNichtImZiel_Verwirft ist die Gegenrichtung: Die
// DB-Transaktion lief NICHT durch — die Karte steht noch im Quell-Board.
// Der Staging-Rest ist ein abgebrochener Versuch und gehört weggeräumt.
//
// Dieser Test ist der Grund, warum der Abgleich überhaupt eine Metadatei
// braucht: Ohne die Auskunft „wohin sollte diese Karte?" sind beide Fälle von
// aussen ununterscheidbar, und der Abgleich müsste raten.
func TestReconcile_F600_KarteNichtImZiel_Verwirft(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__rechnung.pdf")
	dstDir := filepath.Join(baseDir, "dst-board", "card-uuid")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)

	// Die Datenbank sagt: die Karte ist noch im QUELL-Board.
	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		"card-uuid": "src-board",
	}))

	if bericht.Verworfen != 1 {
		t.Errorf("Verworfen = %d, erwartet 1 (Bericht: %+v)", bericht.Verworfen, bericht)
	}
	if bericht.Fertiggestellt != 0 {
		t.Errorf("Fertiggestellt = %d, erwartet 0 — ein abgebrochener Versuch wurde "+
			"fertiggestellt (Bericht: %+v)", bericht.Fertiggestellt, bericht)
	}
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Errorf("Ziel-Pfad %s existiert — ein nie vollzogener Transfer wurde sichtbar gemacht", dstDir)
	}

	// Die Original-Dateien im Quell-Board bleiben unangetastet: sie sind nach
	// dem abgebrochenen Versuch die einzige Kopie.
	if _, err := os.Stat(filepath.Join(baseDir, "src-board", "card-uuid", "att1__rechnung.pdf")); err != nil {
		t.Errorf("Quell-Anhang wurde mit weggeräumt — das wäre echter Datenverlust: %v", err)
	}

	rest, _ := os.ReadDir(filepath.Join(baseDir, transferStagingRoot))
	if len(rest) != 0 {
		t.Errorf("Staging-Rest nicht weggeräumt: %d Einträge", len(rest))
	}
}

// TestReconcile_F600_KarteVerschwunden_Verwirft: die Karte existiert in keinem
// Board mehr (zwischenzeitlich gelöscht). Das ist ebenfalls „nicht im Ziel" —
// es gibt nichts fertigzustellen.
func TestReconcile_F600_KarteVerschwunden_Verwirft(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{}))
	if bericht.Verworfen != 1 {
		t.Errorf("Verworfen = %d, erwartet 1 (Bericht: %+v)", bericht.Verworfen, bericht)
	}
}

// TestReconcile_F600_StagingOhneMetadatei_BleibtLiegen deckt den Fall ab, der
// am gefährlichsten wäre, wenn man ihn falsch behandelt: ein Staging ohne
// Metadatei.
//
// Zwei Ursachen, beide gleich zu behandeln — ein Rest aus der Zeit vor F-600,
// oder ein Absturz MITTEN im Kopieren (die Metadatei wird bewusst zuletzt
// geschrieben). Im zweiten Fall sind die Anhänge unvollständig; sie
// fertigzustellen wäre schlimmer als sie liegen zu lassen, weil unvollständige
// Dateien dann als vollständige am Ziel erschienen.
//
// Der Abgleich darf also weder fertigstellen noch löschen: nur melden. Das
// Wegräumen übernimmt der 24-h-Sweep, dessen Zweck genau das ist.
func TestReconcile_F600_StagingOhneMetadatei_BleibtLiegen(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)

	// Metadatei entfernen — so sieht ein Vor-F-600-Staging aus, und so sieht
	// ein Absturz mitten im Kopieren aus.
	root := filepath.Join(baseDir, transferStagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("Staging-Root lesen: %v", err)
	}
	var stageDirName string
	for _, e := range entries {
		if e.IsDir() {
			stageDirName = e.Name()
		} else {
			if err := os.Remove(filepath.Join(root, e.Name())); err != nil {
				t.Fatalf("Metadatei entfernen: %v", err)
			}
		}
	}
	if stageDirName == "" {
		t.Fatal("kein Staging-Verzeichnis gefunden")
	}

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		// Selbst wenn die Karte im Ziel läge: ohne Metadatei weiss der Abgleich
		// das nicht und darf nicht raten.
		"card-uuid": "dst-board",
	}))

	if bericht.Liegengelassen != 1 {
		t.Errorf("Liegengelassen = %d, erwartet 1 (Bericht: %+v)", bericht.Liegengelassen, bericht)
	}
	if bericht.Fertiggestellt != 0 || bericht.Verworfen != 0 {
		t.Errorf("Der Abgleich hat ohne Zuordnung gehandelt statt zu melden: %+v", bericht)
	}
	if _, err := os.Stat(filepath.Join(root, stageDirName)); err != nil {
		t.Errorf("Staging wurde angefasst, obwohl es nicht zuzuordnen war: %v", err)
	}
}

// TestReconcile_F600_MetadateiOhneVerzeichnis_WirdAufgeraeumt: eine Metadatei,
// deren Verzeichnis nicht mehr da ist. Das entsteht, wenn nach erfolgreichem
// Rename das os.Remove der Metadatei nicht mehr durchkam.
//
// Nichts fertigzustellen, nichts zu retten — aber ohne Aufräumen meldete der
// Abgleich sie bei JEDEM Start erneut als „nicht zuzuordnen". Ein Wächter, der
// dauerhaft dasselbe meldet, wird ignoriert.
func TestReconcile_F600_MetadateiOhneVerzeichnis_WirdAufgeraeumt(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	root := filepath.Join(baseDir, transferStagingRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	waise := filepath.Join(root, "abc-123"+stagingMetaSuffix)
	if err := os.WriteFile(waise, []byte(`{"srcBoard":"a","dstBoard":"b","cardId":"c"}`), 0o644); err != nil {
		t.Fatalf("Waise schreiben: %v", err)
	}

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{"c": "b"}))

	if bericht.Fertiggestellt != 0 || bericht.Verworfen != 0 || bericht.Liegengelassen != 0 {
		t.Errorf("Eine Waisen-Metadatei wurde als Vorgang gezählt: %+v", bericht)
	}
	if _, err := os.Stat(waise); !os.IsNotExist(err) {
		t.Errorf("Waisen-Metadatei nicht aufgeräumt (err=%v)", err)
	}
}

// TestSweepStaleStaging_LaesstKeineMetadateiZurueck: nach dem 24-h-Sweep darf
// im Staging-Root nichts übrig bleiben — weder Verzeichnis noch Begleitdatei.
//
// Der Testname sagt bewusst „lässt nichts zurück" und nicht „nimmt die
// Metadatei mit": Für das Mitnehmen gab es zunächst eine eigene Zeile im Sweep.
// Die Gegenprobe (Zeile ausgebaut) blieb GRÜN und entlarvte sie als redundant —
// die Metadatei liegt neben dem Verzeichnis und ist damit selbst ein Eintrag,
// den die Schleife ohnehin erfasst. Die Zeile wurde daraufhin entfernt, statt
// den Test so lange umzubauen, bis er sie rechtfertigt.
//
// Was hier bleibt, ist die Zusage, die wirklich zählt und die rot wird, wenn
// jemand den Sweep bricht: nach dem Lauf ist der Staging-Root leer.
func TestSweepStaleStaging_LaesstKeineMetadateiZurueck(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)

	root := filepath.Join(baseDir, transferStagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("Staging-Root lesen: %v", err)
	}
	// Vorbedingung festhalten: es liegen wirklich BEIDE dort. Ohne diese
	// Prüfung wäre der Test auch grün, wenn StageCardAttachments die Metadatei
	// gar nicht mehr schriebe.
	if len(entries) != 2 {
		t.Fatalf("erwartet Verzeichnis + Metadatei, gefunden %d Einträge", len(entries))
	}

	altZeit := time.Now().Add(-2 * stagingMaxAge)
	for _, e := range entries {
		pfad := filepath.Join(root, e.Name())
		if err := os.Chtimes(pfad, altZeit, altZeit); err != nil {
			t.Fatalf("Chtimes %s: %v", e.Name(), err)
		}
	}

	store.sweepStaleStaging()

	rest, _ := os.ReadDir(root)
	if len(rest) != 0 {
		namen := make([]string, 0, len(rest))
		for _, e := range rest {
			namen = append(namen, e.Name())
		}
		t.Errorf("Sweep liess %v stehen — erwartet: Staging-Root leer", namen)
	}
}

// ── Nacharbeit zum Sprint-55-Kritiker ────────────────────────────────────

// TestReconcile_F600_FertigstellenLoeschtQuellAnhaenge deckt den schwersten
// Befund des Kritiker-Laufs ab: Der Abgleich stellte den Transfer fertig, liess
// die Quell-Dateien aber liegen — anders als der reguläre Saga-Weg, der sie
// direkt nach dem Commit löscht.
//
// WARUM DAS MEHR IST ALS PLATZVERBRAUCH: Der Download-Handler prüft die ACL des
// Boards aus der URL, nicht ob die Karte dort noch liegt (der Board-Blob ist
// opak, ADR-0044). Bleiben die Dateien am Quell-Pfad, liest sie jeder weiter,
// der das QUELL-Board sehen darf — obwohl die Karte inzwischen in einem Board
// mit ganz anderer Berechtigung liegt. Zweitens scheitert ein späterer
// Rück-Transfer dauerhaft an der Kollisionsprüfung: die F-390-Sackgasse,
// wiedereingeführt an neuer Stelle.
func TestReconcile_F600_FertigstellenLoeschtQuellAnhaenge(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__gehalt.pdf")
	srcDatei := filepath.Join(baseDir, "src-board", "card-uuid", "att1__gehalt.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	altern(t, baseDir)

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		"card-uuid": "dst-board",
	}))
	if bericht.Fertiggestellt != 1 {
		t.Fatalf("Fertiggestellt = %d, erwartet 1 (%+v)", bericht.Fertiggestellt, bericht)
	}

	// Am Ziel sichtbar …
	if _, err := os.Stat(filepath.Join(baseDir, "dst-board", "card-uuid", "att1__gehalt.pdf")); err != nil {
		t.Fatalf("Anhang am Ziel fehlt: %v", err)
	}
	// … und an der Quelle WEG.
	if _, err := os.Stat(srcDatei); !os.IsNotExist(err) {
		t.Errorf("Quell-Anhang %s existiert noch (err=%v) — er bleibt damit über das "+
			"Quell-Board lesbar und blockiert einen Rück-Transfer", srcDatei, err)
	}

	// Die Probe aufs Exempel: Der Rück-Transfer muss jetzt gehen.
	seedCardAttachments(t, baseDir, "dst-board", "card-uuid", "att1__gehalt.pdf")
	if _, err := store.StageCardAttachments("dst-board", "src-board", "card-uuid"); err != nil {
		t.Errorf("Rück-Transfer blockiert — genau die F-390-Sackgasse: %v", err)
	}
}

// TestReconcile_F600_ZuFrischesStagingBleibtUnangetastet: Die Prämisse „beim
// Start läuft kein Transfer" gilt nur PROZESSINTERN. Bei einem Rolling Restart
// teilen sich zwei Prozesse dasselbe Volume — derselbe Fall, den F-597 für die
// Datenbank ausdrücklich annimmt.
//
// Fehlschlag ohne diesen Guard: Prozess A ist in Saga §1, die DB-Transaktion
// läuft noch. Prozess B startet, sieht die Karte noch im Quell-Board und
// verwirft das Staging eines LAUFENDEN Transfers. A's Commit läuft danach ins
// Leere, die Karte landet ohne Anhänge im Ziel.
func TestReconcile_F600_ZuFrischesStagingBleibtUnangetastet(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	// KEIN altern() — das Staging ist sekundenalt, wie bei einem laufenden Transfer.

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		"card-uuid": "src-board", // DB-Tx noch nicht durch
	}))

	if bericht.Verworfen != 0 {
		t.Errorf("Verworfen = %d — ein womöglich laufender Transfer wurde weggeräumt (%+v)",
			bericht.Verworfen, bericht)
	}
	if bericht.Liegengelassen != 1 {
		t.Errorf("Liegengelassen = %d, erwartet 1 (%+v)", bericht.Liegengelassen, bericht)
	}
	rest, _ := os.ReadDir(filepath.Join(baseDir, transferStagingRoot))
	if len(rest) != 2 {
		t.Errorf("Staging-Verzeichnis + Metadatei sollten unangetastet sein, gefunden %d Einträge", len(rest))
	}
}

// TestReconcile_F600_ZuAltesStagingWirdNichtFertiggestellt: Ab stagingMaxAge
// gilt ein Rest als abgebrochener Versuch (so definiert es sweepStaleStaging).
//
// Fehlschlag ohne diesen Guard: Ein Transfer bricht ab, die DB-Tx lief nie
// durch. Der Nutzer wiederholt ihn erfolgreich und löscht danach im Ziel-Board
// einen Anhang. Wochen später ein Neustart → das alte Staging wird ans Ziel
// gerenamed, und ein bewusst gelöschtes Dokument ist wieder da.
func TestReconcile_F600_ZuAltesStagingWirdNichtFertiggestellt(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__alt.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	root := filepath.Join(baseDir, transferStagingRoot)
	eintraege, _ := os.ReadDir(root)
	uralt := time.Now().Add(-30 * 24 * time.Hour)
	for _, e := range eintraege {
		pfad := filepath.Join(root, e.Name())
		if err := os.Chtimes(pfad, uralt, uralt); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{
		"card-uuid": "dst-board", // Karte IST im Ziel — trotzdem nicht fertigstellen
	}))

	if bericht.Fertiggestellt != 0 {
		t.Errorf("Fertiggestellt = %d — ein %v altes Staging wurde committet (%+v)",
			bericht.Fertiggestellt, 30*24*time.Hour, bericht)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "dst-board", "card-uuid")); !os.IsNotExist(err) {
		t.Errorf("Ziel-Verzeichnis wurde angelegt — gelöschte Anhänge könnten so zurückkehren")
	}
}

// TestCommit_LehntUnsicherePfadkomponentenAb schliesst F-619: `Commit()` ist
// die letzte Instanz vor `os.Rename`, und der Reconcile-Pfad ist der erste
// Aufrufer, dessen Pfadkomponenten nicht durch `safePathComponent` gelaufen
// sind — sie stammen dort aus einer JSON-Datei auf der Platte.
//
// Fehlschlag ohne die Prüfung: Eine Metadatei mit `"dstBoard": ".."` führt zu
// `os.Rename(stageDir, baseDir/../<cardId>)` — das Staging wandert aus dem
// Anhang-Volume heraus. Kein erreichbarer Angriffspfad (StageCardAttachments
// würde ein solches Board ablehnen), aber ADR-0033 verankert die Zusage
// ausdrücklich im Store und nicht in der Umsicht der Aufrufer.
//
// WICHTIG AM AUFBAU, und beim ersten Anlauf falsch gemacht: Das
// Staging-Verzeichnis muss WIRKLICH EXISTIEREN. Eine erste Fassung übergab
// einen erfundenen Pfad — dann scheitert `os.Rename` ohnehin an ENOENT, und die
// Gegenprobe (Prüfung ausgebaut) blieb grün. Der Test hätte gemessen, dass ein
// nicht existierendes Verzeichnis sich nicht umbenennen lässt, und nichts über
// F-619 ausgesagt. Deshalb zusätzlich `errors.Is(err, ErrUnsafePathComponent)`
// statt nur „irgendein Fehler".
func TestCommit_LehntUnsicherePfadkomponentenAb(t *testing.T) {
	faelle := []struct{ name, board, card string }{
		{"Board mit ..", "..", "card-uuid"},
		{"Karte mit ..", "dst-board", ".."},
		{"Board mit Schrägstrich", "a/b", "card-uuid"},
		{"Board mit führendem Punkt", ".transfer-staging", "card-uuid"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			baseDir := t.TempDir()
			store := NewAttachmentStore(baseDir)

			// Echtes Staging mit Inhalt — ohne die Prüfung WÜRDE das Rename
			// gelingen, und genau das soll der Test verhindern.
			stageDir := filepath.Join(baseDir, transferStagingRoot, "echtes-staging")
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(stageDir, "att1__x.pdf"), []byte("inhalt"), 0o644); err != nil {
				t.Fatalf("Datei anlegen: %v", err)
			}

			st := &StagedAttachments{
				store:    store,
				stageDir: stageDir,
				dstBoard: f.board,
				cardId:   f.card,
			}
			err := st.Commit()
			if err == nil {
				t.Fatalf("Commit akzeptierte board=%q card=%q", f.board, f.card)
			}
			if !errors.Is(err, ErrUnsafePathComponent) {
				t.Fatalf("Fehler war %v, erwartet ErrUnsafePathComponent — Commit ist an "+
					"etwas anderem gescheitert als an der Pfadprüfung", err)
			}
			if _, err := os.Stat(filepath.Join(stageDir, "att1__x.pdf")); err != nil {
				t.Errorf("Staging wurde trotz Ablehnung verschoben: %v", err)
			}
		})
	}
}

// TestReconcile_F618_RaeumtAlteResteWeg: Der Abgleich stösst am Ende
// `sweepStaleStaging` an. Ohne diesen Aufruf läuft der Sweep nur aus
// `StageCardAttachments` — also erst beim nächsten Transfer. Ein zu alter Rest
// wurde deshalb bei JEDEM Start erneut als „bleibt liegen" gemeldet, ohne dass
// je etwas passierte.
//
// Der eigene Testkommentar weiter oben formuliert das Problem wörtlich: „Ein
// Wächter, der dauerhaft dasselbe meldet, wird ignoriert."
//
// GEGENGEPROBT (Sprint-55-Nacharbeit): Ohne den `s.sweepStaleStaging()`-Aufruf
// am Ende von ReconcileStagedTransfers bleibt der Rest liegen und der Test ist
// rot.
func TestReconcile_F618_RaeumtAlteResteWeg(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")

	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	root := filepath.Join(baseDir, transferStagingRoot)
	eintraege, _ := os.ReadDir(root)
	uralt := time.Now().Add(-2 * stagingMaxAge)
	for _, e := range eintraege {
		pfad := filepath.Join(root, e.Name())
		if err := os.Chtimes(pfad, uralt, uralt); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	// Erster Lauf: zu alt zum Fertigstellen → liegengelassen, aber am Ende
	// weggeräumt.
	bericht := store.ReconcileStagedTransfers(kartenOrt(map[string]string{"card-uuid": "dst-board"}))
	if bericht.Liegengelassen != 1 {
		t.Fatalf("Liegengelassen = %d, erwartet 1 (%+v)", bericht.Liegengelassen, bericht)
	}

	rest, _ := os.ReadDir(root)
	if len(rest) != 0 {
		namen := make([]string, 0, len(rest))
		for _, e := range rest {
			namen = append(namen, e.Name())
		}
		t.Fatalf("der zu alte Rest liegt noch da: %v — beim nächsten Start meldet der "+
			"Abgleich dasselbe erneut", namen)
	}

	// Zweiter Lauf: nichts mehr zu melden.
	zweiter := store.ReconcileStagedTransfers(kartenOrt(map[string]string{"card-uuid": "dst-board"}))
	if zweiter.Liegengelassen != 0 {
		t.Errorf("zweiter Lauf meldet erneut %d liegengelassen", zweiter.Liegengelassen)
	}
}

// TestSweepStaleStaging_WartetAufGestellteUhr (Verify 2 Session-QA Sprint 74,
// F-1210): Mit nicht gestellter Uhr bleibt ein scheinbar alter Staging-Rest
// liegen — er könnte die Anhänge einer Karte tragen, die laut Datenbank schon
// im Ziel liegt.
// GEGENPROBE: Ohne uhr.Erlaubt in sweepStaleStaging ist der Rest weg — rot.
func TestSweepStaleStaging_WartetAufGestellteUhr(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src-board", "card-uuid", "att1__x.pdf")
	if _, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	root := filepath.Join(baseDir, transferStagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		t.Fatalf("Vorbedingung: Staging-Rest erwartet (%v)", err)
	}
	altZeit := time.Now().Add(-2 * stagingMaxAge)
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(root, e.Name()), altZeit, altZeit); err != nil {
			t.Fatal(err)
		}
	}
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })

	uhr.Gestellt = func() (bool, error) { return false, nil }
	store.sweepStaleStaging()
	if rest, _ := os.ReadDir(root); len(rest) != len(entries) {
		t.Fatalf("nicht gestellte Uhr: %d von %d Einträgen übrig", len(rest), len(entries))
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	store.sweepStaleStaging()
	if rest, _ := os.ReadDir(root); len(rest) != 0 {
		t.Fatalf("gestellte Uhr: %d Einträge übrig, erwartet 0", len(rest))
	}
}
