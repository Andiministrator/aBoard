package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// ErrAttachmentNotFound wird von Open/Delete geliefert wenn die Datei fehlt.
var ErrAttachmentNotFound = errors.New("storage: attachment nicht gefunden")

// ErrAttachmentExists liefert Save, wenn unter der attId schon eine Datei
// liegt (F-2081). Die id stammt vom Client; ein zweiter Upload unter derselben
// id tauschte den Inhalt sonst ohne Spur aus, legte bei anderem Namen eine
// zweite Datei daneben oder löschte bei abgebrochener Übertragung das Original.
var ErrAttachmentExists = errors.New("storage: attachment-id bereits belegt")

// ErrUnsafePathComponent signalisiert eine ID, die nicht als einzelne
// Pfadkomponente taugt (F-200, Karte #376).
var ErrUnsafePathComponent = errors.New("storage: unsichere Pfadkomponente")

// safePathComponent setzt die Zusage durch, die im Kommentar von Save seit
// jeher stand, aber nirgends geprüft wurde: boardId/cardId/attId sind je EINE
// Pfadkomponente. Bis F-200 war das eine Bitte an die Aufrufer — und eine
// Zusage im Kommentar, die weiter reicht als der Code, verhindert genau die
// Nachfrage, die den Fehler gefunden hätte.
//
// Scharf wird das mit DeleteBoard: dort steht ein rekursives Löschen am Ende
// des Pfades. `boardId == ".."` würde damit das Elternverzeichnis des
// gesamten Anhang-Volumes mitnehmen.
func safePathComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) || strings.ContainsRune(s, 0) {
		return false
	}
	// Führender Punkt: Dotfiles/Dotdirs sind im Schema (#74) nicht vorgesehen
	// und wären im Verzeichnis-Listing der übrigen Methoden unsichtbar.
	return !strings.HasPrefix(s, ".")
}

// safeFilenameComponent neutralisiert einen Dateinamen, sodass er garantiert
// EINE Pfadkomponente bleibt (Sprint-52-Kritiker, S4). Analog zu
// `safeFilename()` in src/attachments.js und `attachmentSafeFilename()` in
// server/api — hier als letzte Instanz im Store selbst, damit die Zusage
// nicht davon abhängt, dass jeder künftige Aufrufer daran denkt.
//
// Leerer Rest → "datei": ein Name, der vollständig aus Separatoren bestand,
// ist kein gültiger Name, und ein leerer Anhang-Suffix würde die
// attId-Präfix-Suche in Open/Delete durcheinanderbringen.
func safeFilenameComponent(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == 0 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	// Nach dem Entfernen der Separatoren kann noch ".." übrig sein.
	if name == "" || name == "." || name == ".." {
		return "datei"
	}
	return name
}

// AttachmentStore speichert Anhänge unter baseDir/<boardId>/<cardId>/<attId>__<filename>.
// Spiegelt das FSA-Volume-Layout (Karte #74).
type AttachmentStore struct {
	baseDir string
	// anlegen macht Prüfen und Anlegen in Save zu einem Schritt (F-2081,
	// Kritik K-1): Zwei gleichzeitige Uploads mit derselben id und
	// verschiedenen Namen fänden sonst beide ein leeres Verzeichnis. Gehalten
	// wird sie nur bis zum Anlegen, nicht während der Übertragung.
	anlegen sync.Mutex
	// beiseiteMu schützt den Versatz, ab dem ein Purge-Request die Reste in
	// .purge-beiseite bewertet (Rundlauf statt immer die ältesten 20, F-3054), und
	// die Menge der schon gemeldeten Kollisions-Reste (Logzeile einmal je Rest und
	// Prozess, F-3054). Reiner Hinweis-Zustand: Verlust beim Neustart ist gewollt.
	beiseiteMu        sync.Mutex
	beiseiteVersatz   int
	kollisionGemeldet map[string]bool
}

// NewAttachmentStore erzeugt einen AttachmentStore; baseDir muss existieren oder
// wird beim ersten Save angelegt.
func NewAttachmentStore(baseDir string) *AttachmentStore {
	return &AttachmentStore{baseDir: baseDir}
}

// Save speichert die Anhangsdatei. boardId, cardId und attId müssen
// valide Pfadkomponenten sein (keine Slashes, keine Punkte am Anfang).
func (s *AttachmentStore) Save(boardId, cardId, attId, filename string, r io.Reader) error {
	if !safePathComponent(boardId) || !safePathComponent(cardId) || !safePathComponent(attId) {
		return ErrUnsafePathComponent
	}
	// Sprint-52-Kritiker (S4): `filename` war der einzige Parameter, der
	// ungeprüft in den Pfad ging — und der einzige mit Angreifer-Text darin.
	// Heute entschärfen beide Aufrufer ihn vorher (attachmentSafeFilename in
	// api, sanitizeAttachmentFilename in mail), aber genau darauf zu bauen
	// ist die Konstellation, die dieser Fix selbst als Ursache benennt: eine
	// Zusage im Kommentar, die weiter reicht als der Code. Anders als bei den
	// IDs wird hier nicht abgelehnt, sondern neutralisiert — ein Dateiname
	// ist Anzeigetext, kein Bezeichner, und ein Upload soll nicht daran
	// scheitern.
	filename = safeFilenameComponent(filename)
	dir := s.cardDir(boardId, cardId)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("attachments.Save mkdir: %w", err)
	}
	// F-2081: Belegt ist die id, nicht der Dateiname — ein zweiter Upload mit
	// anderem Namen hätte sonst eine zweite Datei <attId>__<neu> angelegt, und
	// Open/Delete griffen auf die erste in Sortierreihenfolge. O_EXCL deckt
	// den gleichen Namen auch dann, wenn zwei Uploads gleichzeitig hier ankommen.
	path := filepath.Join(dir, attId+"__"+filename)
	// Früh ablehnen, bevor Bytes fließen: belegte id → 409 ohne Upload.
	if err := s.idFrei(dir, attId); err != nil {
		return err
	}
	// Sprint-92-Kritiker KS-7: Erst in eine Teil-Datei schreiben, dann unter
	// der Sperre umbenennen. Bricht der Prozess mitten im Kopieren ab, bleibt
	// nur `.teil-…` liegen — der Präfix `<attId>__` bleibt frei, ein erneuter
	// Upload derselben id geht durch (vorher: dauerhaft 409 wegen Teil-Datei).
	teil := filepath.Join(dir, teilPraefix+attId+"-"+uuid.NewString())
	f, err := os.OpenFile(teil, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("attachments.Save create: %w", err)
	}
	defer os.Remove(teil) // nach erfolgreichem Umbenennen ein No-op
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync() // erst auf der Platte, dann umbenennen (Verify V-6)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("attachments.Save copy: %w", err)
	}
	return s.uebernehme(dir, attId, teil, path)
}

// teilPraefix kennzeichnet Teil-Dateien laufender Uploads. Kein Anhang:
// Open/Delete/attIdBelegt suchen nach `<attId>__`, StageCard überspringt sie.
const teilPraefix = ".teil-"

// teilMaxAlter: Teil-Dateien, die länger liegen, stammen aus einem harten
// Abbruch (Kill, Stromausfall), bei dem kein defer lief.
const teilMaxAlter = time.Hour

// raeumeTeile entfernt alte Teil-Dateien im Kartenverzeichnis. Best-effort.
func raeumeTeile(dir string) {
	// Wie sweepStaleStaging: nach Frist nur mit gestellter Uhr löschen —
	// ein Zeitsprung nach vorn träfe sonst einen laufenden Upload (Verify 2 W-1).
	if !uhr.Erlaubt("attachment-teil-sweep", nil) {
		return
	}
	eintraege, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	grenze := time.Now().Add(-teilMaxAlter)
	for _, e := range eintraege {
		if !strings.HasPrefix(e.Name(), teilPraefix) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(grenze) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// idFrei prüft unter der Sperre, ob die id frei ist, und räumt dabei alte
// Teil-Dateien weg (Verify V-6).
func (s *AttachmentStore) idFrei(dir, attId string) error {
	s.anlegen.Lock()
	defer s.anlegen.Unlock()
	raeumeTeile(dir)
	if belegt, err := attIdBelegt(dir, attId); err != nil {
		return fmt.Errorf("attachments.Save readdir: %w", err)
	} else if belegt {
		return ErrAttachmentExists
	}
	return nil
}

// uebernehme benennt die fertige Teil-Datei unter der Sperre um — erst nach
// erneuter Prüfung, ob die id inzwischen belegt ist (gleichzeitige Uploads,
// F-2081). Rename überschreibt nur denselben Namen, und den schließt die
// Prüfung davor aus.
func (s *AttachmentStore) uebernehme(dir, attId, teil, path string) error {
	s.anlegen.Lock()
	defer s.anlegen.Unlock()
	if belegt, err := attIdBelegt(dir, attId); err != nil {
		return fmt.Errorf("attachments.Save readdir: %w", err)
	} else if belegt {
		return ErrAttachmentExists
	}
	if err := os.Rename(teil, path); err != nil {
		return fmt.Errorf("attachments.Save rename: %w", err)
	}
	return nil
}

// attIdBelegt meldet, ob im Kartenverzeichnis schon eine Datei <attId>__*
// liegt — dieselbe Präfix-Regel, nach der Open und Delete suchen.
func attIdBelegt(dir, attId string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	prefix := attId + "__"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			return true, nil
		}
	}
	return false, nil
}

// Open sucht die Datei per attId-Präfix und liefert ReadSeeker + originalen Dateinamen.
// Der Aufrufer ist verantwortlich, den ReadCloser zu schließen.
func (s *AttachmentStore) Open(boardId, cardId, attId string) (io.ReadSeekCloser, string, error) {
	if !safePathComponent(boardId) || !safePathComponent(cardId) || !safePathComponent(attId) {
		return nil, "", ErrUnsafePathComponent
	}
	dir := s.cardDir(boardId, cardId)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrAttachmentNotFound
		}
		return nil, "", fmt.Errorf("attachments.Open readdir: %w", err)
	}
	prefix := attId + "__"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			filename := strings.TrimPrefix(e.Name(), prefix)
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, "", fmt.Errorf("attachments.Open open: %w", err)
			}
			return f, filename, nil
		}
	}
	return nil, "", ErrAttachmentNotFound
}

// Delete entfernt eine einzelne Anhangsdatei. Idempotent — fehlendes Attachment ist kein Fehler.
func (s *AttachmentStore) Delete(boardId, cardId, attId string) error {
	if !safePathComponent(boardId) || !safePathComponent(cardId) || !safePathComponent(attId) {
		return ErrUnsafePathComponent
	}
	dir := s.cardDir(boardId, cardId)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("attachments.Delete readdir: %w", err)
	}
	prefix := attId + "__"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("attachments.Delete remove: %w", err)
			}
			return nil
		}
	}
	return nil // nicht gefunden — idempotent
}

// DeleteCard entfernt das gesamte Card-Verzeichnis. Idempotent.
func (s *AttachmentStore) DeleteCard(boardId, cardId string) error {
	if !safePathComponent(boardId) || !safePathComponent(cardId) {
		return ErrUnsafePathComponent
	}
	if istBoardAblage(cardId) {
		return ErrReservierteAblage
	}
	dir := s.cardDir(boardId, cardId)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("attachments.DeleteCard: %w", err)
	}
	return nil
}

// DeleteBoard entfernt das gesamte Board-Verzeichnis samt aller Karten-
// Unterverzeichnisse (F-200, Karte #376). Idempotent — ein fehlendes
// Verzeichnis ist kein Fehler.
//
// Warum das gebraucht wird: `DELETE /boards/{id}` löschte bisher nur die
// DB-Zeile. Die Anhangsdateien liegen aber ausschliesslich im Dateisystem —
// es gibt keine `attachments`-Tabelle, das `ON DELETE CASCADE` der übrigen
// Tabellen läuft für sie also ins Leere. Verwaiste Verzeichnisse sammelten
// sich unbegrenzt an, und zwar mit Nutzerdaten darin.
//
// Der Pfad wird strikt aus baseDir + boardId zusammengesetzt und boardId
// vorher validiert. Board-Inhalt wird dabei NICHT gelesen: der Board-Blob ist
// für den Server opak (ADR-0044); die Verzeichnis-Löschung darf sich deshalb
// nur auf die ID stützen.
func (s *AttachmentStore) DeleteBoard(boardId string) error {
	if !safePathComponent(boardId) {
		return ErrUnsafePathComponent
	}
	dir := filepath.Join(s.baseDir, boardId)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("attachments.DeleteBoard: %w", err)
	}
	return nil
}

// ── Cross-Board-Transfer: zweiphasige Anhang-Übernahme (#296, F-390) ──
//
// transferStagingRoot ist das Sammelverzeichnis für Anhang-Kopien, die noch
// nicht committet sind. Es liegt unter baseDir — und zwar bewusst, denn
// os.Rename ist nur INNERHALB eines Dateisystems atomar (POSIX). Ein
// Staging-Verzeichnis in os.TempDir() könnte auf einem anderen Volume liegen
// und würde beim Rename mit EXDEV scheitern.
//
// Der führende Punkt ist kein Kosmetik-Detail: safePathComponent lehnt
// führende Punkte ab, also kann NIE ein Board mit dieser ID existieren. Das
// Staging-Verzeichnis kann damit prinzipiell nicht mit einem Board-Pfad
// kollidieren.
const transferStagingRoot = ".transfer-staging"

// stagingMaxAge — ab diesem Alter gilt ein Staging-Verzeichnis als Rest eines
// abgestürzten Transfers und wird beim nächsten Stage-Lauf entfernt. Grosszügig
// bemessen: ein laufender Transfer dauert Millisekunden, ein versehentliches
// Löschen wäre dagegen echter Datenverlust.
const stagingMaxAge = 24 * time.Hour

// stagingMinAge — jünger als das fasst der Abgleich ein Staging nicht an, weil
// es zu einem gerade laufenden Transfer eines Parallelprozesses gehören könnte
// (F-600-Nacharbeit). Grosszügig gegenüber der realen Dauer eines Transfers
// (Millisekunden bis wenige Sekunden bei grossen Anhängen) und immer noch
// belanglos gegenüber der Zeit, die zwischen einem Absturz und dem folgenden
// Neustart vergeht — ein echter Absturz-Rest ist beim nächsten Start immer
// älter als das.
//
// GRENZE, benannt statt behauptet (F-647, Sprint-55-Kritiker): Diese Zahl ist
// eine begründete ANNAHME, keine Messung — anders als bei F-597, wo die
// Bruchrate über 20 Runden gemessen wurde, bevor die Wartezeit festgelegt war.
// Das ist hier vertretbar, weil ADR-0033 §4 den prozess-übergreifenden Fall
// ohnehin nur eingrenzt und nicht löst, und weil der Betrieb laut
// docs/betrieb.md aus EINEM Container ohne Rolling-Replicas besteht: zwei
// gleichzeitig laufende Transfer-Prozesse gibt es dort nicht.
//
// Was die Zahl NICHT abdeckt: ein Transfer, dessen Fenster zwischen
// StageCardAttachments und Commit() über zwei Minuten hinausgezogen wird —
// etwa viele grosse Anhänge auf einem ausgelasteten Volume, kombiniert mit
// einem BEGIN IMMEDIATE, das mehrfach auf SQLITE_BUSY läuft. Ein parallel
// startender Prozess hielte das Staging dann für einen Absturz-Rest.
//
// Auslöser für eine Neubewertung, damit die Annahme nicht stillschweigend
// weitergilt: sobald mehr als eine Server-Instanz gleichzeitig läuft
// (Kubernetes, Blue/Green, Rolling Restart). Dann gehört die reale Dauer von
// StageCardAttachments→Commit gemessen und diese Konstante daran bemessen —
// oder das Staging bekommt eine Prozess-Kennung statt eines Zeitfensters.
const stagingMinAge = 2 * time.Minute

// stagingMetaSuffix benennt die Begleitdatei, die zu jedem Staging-Verzeichnis
// gehört: `.transfer-staging/<uuid>.meta.json` NEBEN `.transfer-staging/<uuid>/`.
//
// Warum daneben und nicht darin (F-600): Commit ist ein einzelnes os.Rename des
// gesamten Staging-Verzeichnisses an den Ziel-Pfad. Läge die Metadatei darin,
// wanderte sie mit und stünde dem Nutzer als Anhang-Datei in der Karte. Sie vor
// dem Rename zu löschen wäre die andere Möglichkeit, öffnet aber genau das
// Fenster wieder, das diese Datei schliessen soll: stirbt der Prozess zwischen
// Löschen und Rename, ist die Zuordnung weg und der Abgleich blind.
const stagingMetaSuffix = ".meta.json"

// stagingMeta hält fest, WOZU ein Staging-Verzeichnis gehört — die Angaben,
// die bis F-600 nur im Speicher standen (im StagedAttachments-Handle) und
// deshalb einen Prozess-Absturz nicht überlebten.
//
// Ohne sie kann ein Neustart nicht unterscheiden, ob ein Staging-Rest ein
// abgebrochener Versuch ist (wegwerfen) oder die zweite, nie ausgeführte
// Hälfte einer in der Datenbank bereits vollzogenen Verschiebung
// (fertigstellen). ReconcileStagedTransfers braucht genau diesen Unterschied.
type stagingMeta struct {
	SrcBoard string `json:"srcBoard"`
	DstBoard string `json:"dstBoard"`
	CardId   string `json:"cardId"`
	Staged   string `json:"staged"` // RFC 3339, rein informativ
}

// StagedAttachments ist das Handle auf eine vorbereitete, aber noch nicht
// sichtbare Anhang-Kopie (Saga-Schritt 1). Es wird entweder committet
// (Schritt 2, nach erfolgreicher DB-Transaktion) oder verworfen (Schritt 3).
//
// Ein nil-Handle ist gültig und bedeutet „nichts zu tun" (Karte ohne Anhänge);
// Commit und Abort sind auf nil-Receiver no-ops, damit der Aufrufer keine
// Fallunterscheidung braucht.
type StagedAttachments struct {
	store    *AttachmentStore
	stageDir string
	metaPath string
	dstBoard string
	cardId   string
}

// StageCardAttachments kopiert alle Anhänge einer Karte aus dem Quell-Board in
// ein kollisionsfreies Staging-Verzeichnis (#296 Saga-Schritt 1). Der
// Ziel-Pfad wird dabei NICHT angefasst.
//
// Warum zweiphasig (F-390): Die Vorgänger-Fassung kopierte direkt an den
// endgültigen Ziel-Pfad und verliess sich darauf, dass der Aufrufer bei einem
// DB-Fehler wieder aufräumt. Stürzt der Server zwischen Kopie und
// DB-Transaktion ab, kann er das nicht mehr — die Dateien blieben am Ziel-Pfad
// liegen. Beim nächsten Versuch schlug dann die Kollisionsprüfung an und der
// Endpoint antwortete dauerhaft mit 500: der Nutzer sass fest, bis jemand von
// Hand `rm -r` machte. Genau diese Sackgasse gibt es hier nicht mehr, weil ein
// abgestürzter Versuch nur ein Staging-Verzeichnis mit eigener UUID
// hinterlässt, das keinem folgenden Versuch im Weg steht (und nach
// stagingMaxAge selbst weggeräumt wird).
//
// Rückgabe (nil, nil): die Karte hat keine Anhänge — regulärer Fall.
// Original-Dateien im Quell-Board bleiben stehen; der Aufrufer löscht sie erst
// nach erfolgreichem Commit.
func (s *AttachmentStore) StageCardAttachments(srcBoardId, dstBoardId, cardId string) (*StagedAttachments, error) {
	if !safePathComponent(srcBoardId) || !safePathComponent(dstBoardId) || !safePathComponent(cardId) {
		return nil, ErrUnsafePathComponent
	}
	if istBoardAblage(cardId) {
		return nil, ErrReservierteAblage
	}

	// Gelegenheits-Aufräumen: Reste abgestürzter Transfers. Best-effort — ein
	// Fehler hier darf den laufenden Transfer nicht kippen.
	s.sweepStaleStaging()

	srcDir := s.cardDir(srcBoardId, cardId)
	srcEntries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Karte hat keine Anhänge
		}
		return nil, fmt.Errorf("attachments.StageCard readdir: %w", err)
	}

	// Vorab-Kollisionsprüfung: ein belegter Ziel-Pfad ist nach F-390 nur noch
	// als ECHTER Konflikt erklärbar (dort liegt eine Karte gleicher UUID) —
	// nicht mehr als Absturz-Rest. Wir prüfen trotzdem hier und nicht erst im
	// Commit, damit der Fehler VOR der DB-Transaktion auffällt.
	dstDir := s.cardDir(dstBoardId, cardId)
	if _, err := os.Stat(dstDir); err == nil {
		return nil, fmt.Errorf("attachments.StageCard: dst-Verzeichnis %s existiert bereits", dstDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("attachments.StageCard stat dst: %w", err)
	}

	stageDir := filepath.Join(s.baseDir, transferStagingRoot, uuid.NewString())
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return nil, fmt.Errorf("attachments.StageCard mkdir: %w", err)
	}

	for _, e := range srcEntries {
		if e.IsDir() {
			continue // wir kennen kein Verschachteln im Schema (#74)
		}
		if strings.HasPrefix(e.Name(), teilPraefix) {
			continue // Rest eines abgebrochenen Uploads, kein Anhang (Verify V-6)
		}
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(stageDir, e.Name())
		if err := copyFile(srcPath, dstPath); err != nil {
			_ = os.RemoveAll(stageDir)
			return nil, fmt.Errorf("attachments.StageCard copy %s: %w", e.Name(), err)
		}
	}

	// Metadatei ZULETZT — erst wenn alle Dateien kopiert sind. Ein Absturz
	// mitten im Kopieren hinterlässt damit ein Staging OHNE Metadatei, und der
	// Abgleich rührt es nicht an (siehe ReconcileStagedTransfers): unvollständig
	// kopierte Anhänge dürfen niemals fertiggestellt werden.
	metaPath := stageDir + stagingMetaSuffix
	metaRaw, err := json.Marshal(stagingMeta{
		SrcBoard: srcBoardId,
		DstBoard: dstBoardId,
		CardId:   cardId,
		Staged:   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, fmt.Errorf("attachments.StageCard meta marshal: %w", err)
	}
	if err := os.WriteFile(metaPath, metaRaw, 0o644); err != nil {
		_ = os.RemoveAll(stageDir)
		return nil, fmt.Errorf("attachments.StageCard meta schreiben: %w", err)
	}

	return &StagedAttachments{
		store:    s,
		stageDir: stageDir,
		metaPath: metaPath,
		dstBoard: dstBoardId,
		cardId:   cardId,
	}, nil
}

// Commit macht die gestagete Kopie am Ziel-Pfad sichtbar (Saga-Schritt 2).
// Erst hier entsteht überhaupt ein Verzeichnis unter dem Ziel-Board — der
// Übergang „unsichtbar → sichtbar" ist ein einzelnes os.Rename und damit
// atomar (gleiches Volume, siehe transferStagingRoot).
//
// Nach erfolgreichem Commit ist das Handle verbraucht; ein zweiter Aufruf ist
// ein no-op (idempotent).
func (st *StagedAttachments) Commit() error {
	if st == nil || st.stageDir == "" {
		return nil
	}
	// F-619: Pfadkomponenten prüfen, obwohl der reguläre Weg sie längst geprüft
	// hat. ADR-0033 sagt dazu: „die Prüfung im Store ist die letzte Instanz,
	// damit die Zusage nicht davon abhängt, dass jeder künftige Aufrufer daran
	// denkt." Der Reconcile-Pfad ist genau der Aufrufer, der nicht daran gedacht
	// hat — dort stammen `dstBoard`/`cardId` aus einer JSON-Datei auf der Platte,
	// und ein `"dstBoard": ".."` liesse os.Rename das Staging aus dem
	// Anhang-Volume herausschieben. Zwei Zeilen, die den Vertrag wieder ganz im
	// Store verankern statt in der Umsicht der Aufrufer.
	if !safePathComponent(st.dstBoard) || !safePathComponent(st.cardId) {
		return ErrUnsafePathComponent
	}
	dstDir := st.store.cardDir(st.dstBoard, st.cardId)
	if err := os.MkdirAll(filepath.Dir(dstDir), 0o755); err != nil {
		return fmt.Errorf("attachments.Commit mkdir board-dir: %w", err)
	}
	// Zweite Kollisionsprüfung: zwischen Stage und Commit lag die DB-Tx, in
	// der ein paralleler Transfer denselben Ziel-Pfad belegt haben könnte.
	// os.Rename würde ein leeres Zielverzeichnis stillschweigend ersetzen und
	// ein befülltes mit ENOTEMPTY ablehnen — beides wollen wir nicht raten.
	if _, err := os.Stat(dstDir); err == nil {
		return fmt.Errorf("attachments.Commit: dst-Verzeichnis %s existiert bereits", dstDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("attachments.Commit stat dst: %w", err)
	}
	if err := os.Rename(st.stageDir, dstDir); err != nil {
		return fmt.Errorf("attachments.Commit rename: %w", err)
	}
	// Metadatei NACH dem Rename entfernen. Andersherum entstünde für die Dauer
	// eines Systemaufrufs ein Staging ohne Zuordnung — genau der blinde Fleck,
	// den F-600 beschreibt. Bleibt sie wegen eines Filesystem-Fehlers liegen,
	// ist das harmlos: das zugehörige Verzeichnis existiert nicht mehr, und der
	// Abgleich behandelt eine Metadatei ohne Verzeichnis als Rest (siehe dort).
	if st.metaPath != "" {
		_ = os.Remove(st.metaPath)
		st.metaPath = ""
	}
	st.stageDir = ""
	return nil
}

// Abort verwirft die gestagete Kopie (Saga-Schritt 3, DB-Transaktion
// fehlgeschlagen). Idempotent und best-effort: bleibt das Verzeichnis wegen
// eines Filesystem-Fehlers liegen, räumt es der nächste sweepStaleStaging weg.
func (st *StagedAttachments) Abort() {
	if st == nil || st.stageDir == "" {
		return
	}
	_ = os.RemoveAll(st.stageDir)
	if st.metaPath != "" {
		_ = os.Remove(st.metaPath)
		st.metaPath = ""
	}
	st.stageDir = ""
}

// ── F-600: Abgleich nach hartem Absturz ──────────────────────────────────

// KartenOrtFn beantwortet die einzige Frage, die der Abgleich an die Datenbank
// hat: In welchem Board liegt diese Karte? Rückgabe ("", nil) bedeutet „in
// keinem" — die Karte existiert nicht (mehr).
//
// Als Funktion statt als Interface, weil der AttachmentStore bewusst nichts
// von der Datenbank weiss: er verwaltet ein Verzeichnis, sonst nichts. Der
// Aufrufer (server/main.go) verbindet beide Welten.
type KartenOrtFn func(cardId string) (boardId string, err error)

// AbgleichBericht fasst zusammen, was ein Abgleichslauf getan hat.
type AbgleichBericht struct {
	Fertiggestellt int // DB-Tx war durch, Commit fehlte → nachgeholt
	Verworfen      int // DB-Tx war nicht durch → Rest weggeräumt
	Liegengelassen int // nicht eindeutig → angefasst wurde nichts
}

// ReconcileStagedTransfers gleicht übrig gebliebene Staging-Verzeichnisse gegen
// den Datenbankstand ab und führt sie zu Ende ODER verwirft sie (F-600).
//
// DAS PROBLEM: Der Cross-Board-Transfer ist eine zweiphasige Saga — erst die
// DB-Transaktion, dann `staged.Commit()`. Gibt Commit einen FEHLER zurück,
// gibt es eine Logzeile mit beiden Pfaden. Stirbt der Prozess dagegen HART
// (OOM-Kill, SIGKILL, Node-Drain) genau dazwischen, wird diese Zeile nie
// erreicht. Der Zustand danach: Die Karte lebt laut Datenbank im Ziel-Board,
// ihre Anhänge liegen im Staging und unverändert am alten Quell-Pfad. Kein
// Datenverlust — aber für den Nutzer sieht es aus wie „Karte da, Anhänge weg",
// und sweepStaleStaging räumte die Staging-Kopie nach 24 h kommentarlos weg.
//
// DIE UNTERSCHEIDUNG, um die es geht: Ein Staging-Rest kann zweierlei sein —
// ein abgebrochener Versuch (die DB-Tx lief nie durch → wegwerfen) oder die
// nie ausgeführte zweite Hälfte einer vollzogenen Verschiebung (→ zu Ende
// führen). Die Metadatei nennt das Ziel-Board, die Datenbank sagt, wo die
// Karte tatsächlich liegt. Stimmen beide überein, war die Tx durch.
//
// WANN AUFRUFEN: beim Serverstart, BEVOR der HTTP-Server Anfragen annimmt.
// Damit erübrigt sich die Frage, ob der Abgleich einen laufenden Transfer
// stören könnte — es läuft keiner. Ein Staging-Rest entsteht nur durch einen
// Absturz, und auf einen Absturz folgt zwangsläufig ein Start.
//
// NICHT still, anders als sweepStaleStaging: Dieser Lauf stellt etwas FERTIG.
// Was er tut, gehört ins Log; was er nicht entscheiden kann, erst recht.
func (s *AttachmentStore) ReconcileStagedTransfers(ort KartenOrtFn) AbgleichBericht {
	var bericht AbgleichBericht
	if s == nil || ort == nil {
		return bericht
	}
	root := filepath.Join(s.baseDir, transferStagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		return bericht // kein Staging-Root = nie ein Transfer gelaufen
	}

	for _, e := range entries {
		if !e.IsDir() {
			// Eine Metadatei ohne zugehöriges Verzeichnis: das Verzeichnis ist
			// bereits weg (erfolgreicher Commit, dessen os.Remove der Metadatei
			// nicht mehr durchkam). Nichts fertigzustellen — aufräumen.
			if strings.HasSuffix(e.Name(), stagingMetaSuffix) {
				dir := strings.TrimSuffix(filepath.Join(root, e.Name()), stagingMetaSuffix)
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					_ = os.Remove(filepath.Join(root, e.Name()))
				}
			}
			continue
		}

		stageDir := filepath.Join(root, e.Name())
		metaPath := stageDir + stagingMetaSuffix

		// Alters-Fenster. Der Abgleich fasst nur an, was WEDER zu frisch NOCH
		// zu alt ist — beide Grenzen decken einen realen Fehlschlag ab:
		//
		//  ZU FRISCH: Die Prämisse „beim Start läuft kein Transfer" gilt nur
		//  prozessintern. Bei einem Rolling Restart oder einem Neustart mit noch
		//  laufendem Altprozess teilen sich zwei Prozesse dasselbe Volume —
		//  derselbe Fall, den F-597 für die Datenbank ausdrücklich annimmt
		//  (Docker-Restart-Schleife, Probe-Race). Ohne diese Untergrenze könnte
		//  der startende Prozess das Staging eines GERADE LAUFENDEN Transfers
		//  verwerfen, dessen DB-Transaktion noch offen ist: dessen Commit liefe
		//  danach ins Leere, und die Karte landete ohne Anhänge im Ziel.
		//
		//  ZU ALT: Ab stagingMaxAge gilt ein Rest per Definition als
		//  abgebrochener Versuch (siehe sweepStaleStaging). Ein Staging aus
		//  einem vor Wochen gescheiterten Versuch nachträglich zu committen
		//  kann Anhänge zurückbringen, die inzwischen bewusst gelöscht wurden —
		//  der Nutzer sähe ein Dokument wieder, das er entfernt hat.
		//
		// Beides führt NICHT zum Löschen: liegen lassen und melden ist hier
		// richtig, denn in beiden Fällen weiss der Abgleich zu wenig.
		if info, err := e.Info(); err == nil {
			alter := time.Since(info.ModTime())
			if alter < stagingMinAge {
				log.Printf("aboard-server: Transfer-Abgleich: Staging %s ist erst %s alt — "+
					"möglicherweise ein laufender Transfer eines Parallelprozesses, "+
					"bleibt unangetastet", e.Name(), alter.Round(time.Second))
				bericht.Liegengelassen++
				continue
			}
			if alter > stagingMaxAge {
				log.Printf("aboard-server: Transfer-Abgleich: Staging %s ist %s alt und damit "+
					"älter als %s — gilt als abgebrochener Versuch und wird nicht mehr "+
					"fertiggestellt; das Aufräumen übernimmt sweepStaleStaging",
					e.Name(), alter.Round(time.Hour), stagingMaxAge)
				bericht.Liegengelassen++
				continue
			}
		}

		raw, err := os.ReadFile(metaPath)
		if err != nil {
			// Keine Metadatei. Zwei Ursachen, beide gleich zu behandeln:
			// ein Staging aus der Zeit vor F-600, oder ein Absturz MITTEN im
			// Kopieren (die Metadatei wird bewusst zuletzt geschrieben).
			// Im zweiten Fall sind die Anhänge unvollständig — sie
			// fertigzustellen wäre schlimmer als sie liegen zu lassen. Also
			// weder das eine noch das andere: melden, der 24-h-Sweep räumt.
			log.Printf("aboard-server: Transfer-Abgleich: Staging %s ohne Metadatei — "+
				"nicht zuzuordnen, bleibt liegen (räumt sweepStaleStaging nach %v)",
				e.Name(), stagingMaxAge)
			bericht.Liegengelassen++
			continue
		}
		var meta stagingMeta
		if err := json.Unmarshal(raw, &meta); err != nil || meta.CardId == "" || meta.DstBoard == "" {
			// `err` kann nil sein: die Datei parst, aber cardId/dstBoard fehlen.
			// Dann endete die Meldung früher auf „bleibt liegen: <nil>" — eine
			// Diagnose, die dem Leser nichts sagt (F-618).
			grund := "cardId oder dstBoard fehlt"
			if err != nil {
				grund = err.Error()
			}
			log.Printf("aboard-server: Transfer-Abgleich: Staging %s hat eine unlesbare "+
				"Metadatei — bleibt liegen: %s", e.Name(), grund)
			bericht.Liegengelassen++
			continue
		}

		istBoard, err := ort(meta.CardId)
		if err != nil {
			log.Printf("aboard-server: Transfer-Abgleich: Karte %s nicht auffindbar, "+
				"Staging %s bleibt liegen: %v", meta.CardId, e.Name(), err)
			bericht.Liegengelassen++
			continue
		}

		if istBoard != meta.DstBoard {
			// Die DB-Transaktion lief nie durch (oder wurde zurückgedreht):
			// die Karte steht noch im Quell-Board oder gar nicht mehr. Der
			// Staging-Rest ist ein abgebrochener Versuch.
			_ = os.RemoveAll(stageDir)
			_ = os.Remove(metaPath)
			bericht.Verworfen++
			log.Printf("aboard-server: Transfer-Abgleich: Karte %s liegt in %q, nicht im "+
				"Ziel %q → abgebrochener Versuch, Staging %s verworfen",
				meta.CardId, istBoard, meta.DstBoard, e.Name())
			continue
		}

		// Die DB sagt: die Karte IST im Ziel-Board. Der Commit fehlt also nur
		// noch — genau der F-600-Fall.
		st := &StagedAttachments{
			store:    s,
			stageDir: stageDir,
			metaPath: metaPath,
			dstBoard: meta.DstBoard,
			cardId:   meta.CardId,
		}
		if err := st.Commit(); err != nil {
			// Commit prüft selbst auf ein belegtes Ziel-Verzeichnis. Schlägt er
			// fehl, wird NICHT gelöscht: die Anhänge sind die einzige Kopie am
			// Ziel, und raten ist hier Datenverlust.
			log.Printf("aboard-server: Transfer-Abgleich: Karte %s liegt im Ziel %q, aber "+
				"der nachgeholte Anhang-Commit scheiterte — Staging %s bleibt liegen, "+
				"Quell-Dateien unter %q ebenfalls: %v",
				meta.CardId, meta.DstBoard, e.Name(), meta.SrcBoard, err)
			bericht.Liegengelassen++
			continue
		}
		bericht.Fertiggestellt++

		// Quell-Dateien löschen — der reguläre Saga-Weg tut das direkt nach dem
		// Commit (server/api/transfer.go), und der Abgleich MUSS es genauso tun.
		// Ohne diesen Schritt ist der Transfer nur halb nachgeholt, mit zwei
		// Folgen, von denen die erste die schwerere ist:
		//
		//  1. Der Download-Handler prüft die ACL des Boards aus der URL, nicht
		//     ob die Karte dort noch liegt (der Board-Blob ist opak, ADR-0044).
		//     Bleiben die Dateien am Quell-Pfad liegen, liest sie jeder weiter,
		//     der das QUELL-Board sehen darf — obwohl die Karte längst in einem
		//     Board mit möglicherweise ganz anderer Berechtigung liegt.
		//  2. Ein späterer Rück-Transfer scheitert dauerhaft an der
		//     Kollisionsprüfung in StageCardAttachments ("dst-Verzeichnis
		//     existiert bereits") — genau die F-390-Sackgasse, an neuer Stelle.
		//
		// Best-effort wie im regulären Pfad: Der Transfer IST vollzogen, die
		// Karte zeigt auf die richtigen Pfade. Schlägt das Löschen fehl, bleibt
		// ein Platzverbrauch und das ACL-Problem — beides gehört ins Log, aber
		// keines rechtfertigt es, den gerade geretteten Commit zurückzudrehen.
		if meta.SrcBoard != "" {
			if err := s.DeleteCard(meta.SrcBoard, meta.CardId); err != nil {
				log.Printf("aboard-server: Transfer-Abgleich: Karte %s fertiggestellt, aber "+
					"die Quell-Anhänge unter %q liessen sich nicht löschen — sie bleiben "+
					"über das Quell-Board lesbar und blockieren einen Rück-Transfer: %v",
					meta.CardId, meta.SrcBoard, err)
			}
		}

		log.Printf("aboard-server: Transfer-Abgleich: Karte %s war bereits in %q, die "+
			"Anhänge nicht — unterbrochener Transfer aus %q nachträglich fertiggestellt",
			meta.CardId, meta.DstBoard, meta.SrcBoard)
	}

	// F-618: Aufräumen, was der Abgleich nicht entscheiden konnte. Ohne diesen
	// Aufruf läuft `sweepStaleStaging` nur aus `StageCardAttachments` — also
	// erst beim nächsten Transfer. Ein Rest, dessen Commit dauerhaft scheitert
	// (etwa weil das Ziel-Verzeichnis inzwischen belegt ist), erzeugte damit bei
	// JEDEM Start dieselbe Warnung. Der eigene Testkommentar formuliert das
	// Problem wörtlich: „Ein Wächter, der dauerhaft dasselbe meldet, wird
	// ignoriert." Der Sweep entfernt nur, was älter als stagingMaxAge ist —
	// genau die Reste, die der Abgleich oben schon als „zu alt" übersprungen hat.
	s.sweepStaleStaging()

	if bericht.Fertiggestellt+bericht.Verworfen+bericht.Liegengelassen > 0 {
		log.Printf("aboard-server: Transfer-Abgleich abgeschlossen: %d fertiggestellt, "+
			"%d verworfen, %d liegengelassen",
			bericht.Fertiggestellt, bericht.Verworfen, bericht.Liegengelassen)
	}
	return bericht
}

// sweepStaleStaging entfernt Staging-Verzeichnisse, die älter als
// stagingMaxAge sind — die Reste von Transfers, die zwischen Stage und Commit
// abgebrochen wurden (Server-Absturz). Ohne diesen Durchlauf wäre F-390 nur
// halb behoben: die Sackgasse wäre weg, der Platzverbrauch bliebe.
//
// Bewusst still: das Anhang-Volume ist kein Ort für harte Fehler auf einem
// Nebenpfad. Was hier scheitert, versucht der nächste Transfer erneut.
//
// UHR (Verify 2 Session-QA Sprint 74, F-1210): Das Alter kommt aus der
// Systemuhr. Startet der Pi nach einem Absturz mitten im Transfer mit
// vorgehender Uhr, hält der Abgleich einen frischen Rest für „zu alt“, und
// dieser Sweep löschte ihn — die Anhänge einer Karte, die laut Datenbank
// schon im Ziel liegt. Deshalb nur mit gestellter Uhr (server/uhr); sonst
// bleibt alles liegen, bis der nächste Start oder Transfer es mit richtiger
// Uhr entscheidet.
func (s *AttachmentStore) sweepStaleStaging() {
	if !uhr.Erlaubt("attachment-staging-sweep", nil) {
		return
	}
	root := filepath.Join(s.baseDir, transferStagingRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stagingMaxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			// Erfasst auch die Begleit-Metadateien (F-600): die liegen NEBEN
			// dem Verzeichnis und sind damit selbst Einträge dieser Schleife.
			// Eine Sonderbehandlung dafür wurde geschrieben und wieder
			// entfernt — die Gegenprobe zeigte, dass sie nie greift: beide
			// entstehen im selben Stage-Aufruf, ihre ModTimes liegen
			// Millisekunden auseinander, und der Cutoff ist 24 Stunden.
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// copyFile kopiert byte-für-byte. defer-Close + Sync vor Close für
// crash-safe-Schreiben (best-effort — bei Stromausfall mid-copy bleibt
// das alte src ohnehin unverändert, Saga-Rollback löscht dst).
func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}

func (s *AttachmentStore) cardDir(boardId, cardId string) string {
	return filepath.Join(s.baseDir, boardId, cardId)
}

// AttachmentDate liefert das heutige Datum im ISO-8601-Format — für Upload-Response.
func AttachmentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}

// ── Reservierte Board-Ablage (Karte #443, Server-Kritiker Sprint 61) ─────────
//
// Board-weite Anhänge (Hintergrundbild, Avatare) liegen im selben Store unter
// einem reservierten Bezeichner an der Stelle, an der sonst die Karten-id
// steht. Die API-Schicht lehnt ihn auf den Karten-Routen ab — aber das ist
// nicht die einzige Tür: `handleTransfer` reicht eine Karten-id aus dem Pfad
// ungeprüft an `StageCardAttachments` und `DeleteCard` weiter, VOR jeder
// Existenzprüfung. Über `POST /boards/{src}/cards/__board__/transfer` liess
// sich damit das komplette Board-Anhang-Verzeichnis kopieren; stünde je eine
// Karte mit dieser id im (für den Server opaken) Board-Blob, würde derselbe
// Weg Hintergrund und Avatare des Quell-Boards löschen.
//
// Eine Zusage, die nur an den Routen gilt, die ihr Autor im Blick hatte, ist
// keine Zusage. Deshalb steht die Ablehnung hier — an der Stelle, durch die
// jeder Karten-Zugriff muss. `Save`/`Open`/`Delete` bleiben ausgenommen: über
// sie läuft der legitime Board-Anhang-Pfad selbst.
const BoardAblageId = "__board__"

// ErrReservierteAblage — Karten-Operation auf der Board-Ablage versucht.
var ErrReservierteAblage = errors.New("attachments: reservierte Board-Ablage ist keine Karte")

func istBoardAblage(cardId string) bool { return cardId == BoardAblageId }
