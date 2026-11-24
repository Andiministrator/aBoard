package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// purgeBeiseiteRoot sammelt Kartenverzeichnisse, die ein nachgeholter Purge beiseite
// geschoben hat (F-3030, Karte #1149). Führender Punkt: keine gültige Board-ID
// (safePathComponent), für die übrigen Methoden unsichtbar; eigenes Verzeichnis und
// nicht das Transfer-Staging, dessen Abgleich einen Eintrag als Transfer deuten würde.
//
// Aufbau: `.purge-beiseite/<unix>__<zufall>/<board>/<karte>/…`. Board und Karte bleiben
// Verzeichnisnamen (gültige IDs, je höchstens 128 Zeichen), der äußere Name hat feste
// Länge. Frühere Fassungen kodierten die IDs in einen Namen: roh war `__` mehrdeutig
// (F-3043), hex sprengte bei langen IDs NAME_MAX (F-3046) und las Rohnamen aus reinen
// Hex-Zeichen falsch (F-3047). Alles, was nicht genau diesem Aufbau entspricht, bleibt
// unangetastet.
const purgeBeiseiteRoot = ".purge-beiseite"

// purgeBeiseiteMaxAge — älter als das gilt ein Eintrag als Rest eines Absturzes
// zwischen Beiseiteschieben und zweiter Prüfung (oder eines gescheiterten
// Zurückschiebens). Solche Reste werden NICHT blind gelöscht (Sitzungs-Kritik
// 11.11., F-3038): Das Aufräumen fragt die Kartenlage — fehlt die Karte, wird
// gelöscht, lebt sie, wird zurückgeschoben, sonst bleibt der Rest liegen. Das
// Alter steht im äußeren Namen, nicht in der mtime: rename behält die alte mtime,
// ein gleichzeitiges Aufräumen hielte einen laufenden Eintrag sonst für alt (F-3039).
const purgeBeiseiteMaxAge = time.Hour

// beiseiteHuelleRe: äußerer Name `<unix>__<12 hex>`.
var beiseiteHuelleRe = regexp.MustCompile(`^([0-9]{1,18})__[0-9a-f]{12}$`)

// beiseiteIDRe: dieselben IDs wie die Anhang-Routen (api.safeIDRe) — strenger als
// safePathComponent; fremde Verzeichnisse im Rest bleiben liegen (F-3047).
var beiseiteIDRe = regexp.MustCompile(`^[a-zA-Z0-9\-_]{1,128}$`)

// KartenLageFn: verwaist=true, wenn die Karte im gespeicherten Stand fehlt (oder
// das Board nicht mehr existiert). Fehler → nichts anfassen.
type KartenLageFn func(boardId, cardId string) (verwaist bool, err error)

// LoescheKarteBestaetigt löscht das Anhang-Verzeichnis einer Karte, die der Aufrufer
// als fehlend geprüft hat — mit einer zweiten Prüfung NACH dem Wegnehmen (F-3030).
//
// Ablauf: Verzeichnis per rename beiseite schieben (atomar), dann nochVerwaist()
// fragen. true → das Beiseitegeschobene endgültig löschen. false oder Fehler →
// zurückschieben. Damit gibt es kein Fenster, in dem ein gleichzeitiges Schreiben die
// Karte zurückholt und ihre Dateien trotzdem gelöscht werden: Was vor der zweiten
// Prüfung geschrieben wird, sieht sie; was danach geschrieben wird, kommt nach der
// Löschentscheidung (wie ein Wiederherstellen nach dem Löschen). Eine Sperre der
// Datenbank geht nicht: Die Schreiber in BoardRepo lesen erst und schreiben dann in
// einer DEFERRED-Transaktion und scheitern unter WAL sofort, wenn jemand die
// Schreibsperre hält (knowledge §50).
//
// Rückgabe: geloescht=true, wenn gelöscht wurde oder es nichts zu löschen gab;
// false, wenn die Karte wieder im Stand steht (Verzeichnis zurückgeschoben).
//
// lage prüft beim Aufräumen alter Reste (raeumeBeiseiteAuf) deren Karten; nil →
// kein Aufräumen.
func (s *AttachmentStore) LoescheKarteBestaetigt(boardId, cardId string, nochVerwaist func() (bool, error), lage KartenLageFn) (bool, error) {
	if !safePathComponent(boardId) || !safePathComponent(cardId) {
		return false, ErrUnsafePathComponent
	}
	if istBoardAblage(cardId) {
		return false, ErrReservierteAblage
	}
	s.raeumeBeiseiteAuf(lage, false)
	ziel := s.cardDir(boardId, cardId)
	zufall := make([]byte, 6)
	if _, err := rand.Read(zufall); err != nil {
		return false, fmt.Errorf("attachments.LoescheKarteBestaetigt: %w", err)
	}
	huelle := filepath.Join(s.baseDir, purgeBeiseiteRoot,
		strconv.FormatInt(time.Now().Unix(), 10)+"__"+hex.EncodeToString(zufall))
	if err := os.MkdirAll(filepath.Join(huelle, boardId), 0o750); err != nil {
		return false, fmt.Errorf("attachments.LoescheKarteBestaetigt: %w", err)
	}
	beiseite := filepath.Join(huelle, boardId, cardId)
	if err := os.Rename(ziel, beiseite); err != nil {
		huelleEntfernen(huelle, boardId)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil // nichts da — idempotent wie DeleteCard
		}
		return false, fmt.Errorf("attachments.LoescheKarteBestaetigt: beiseite: %w", err)
	}
	verwaist, err := nochVerwaist()
	if err != nil || !verwaist {
		if rerr := s.schiebeZurueck(beiseite, ziel); rerr != nil {
			// F-3055: Belegte id beim Zurückschieben — die Karte lebt, die alte Datei
			// liegt sicher im Rest. Wie „Karte lebt“ antworten (409), nicht 500.
			if errors.Is(rerr, ErrPurgeRestKollision) {
				return false, err
			}
			return false, fmt.Errorf("attachments.LoescheKarteBestaetigt: zurück: %w (Prüfung: %v)", rerr, err)
		}
		huelleEntfernen(huelle, boardId)
		return false, err
	}
	if err := os.RemoveAll(huelle); err != nil {
		return true, fmt.Errorf("attachments.LoescheKarteBestaetigt: %w", err)
	}
	return true, nil
}

// huelleEntfernen räumt die leeren Hüllverzeichnisse weg (os.Remove: nur wenn leer).
func huelleEntfernen(huelle, boardId string) {
	_ = os.Remove(filepath.Join(huelle, boardId))
	_ = os.Remove(huelle)
}

// schiebeZurueck stellt ein beiseite geschobenes Kartenverzeichnis wieder her. Ist
// inzwischen ein neues entstanden (Upload in der Zwischenzeit), werden die Dateien
// einzeln zurückgeschoben. Belegt im neuen Verzeichnis schon eine Datei dieselbe
// Anhang-ID (oder denselben Namen), bleibt die alte Datei im Rest liegen und
// schiebeZurueck meldet ErrPurgeRestKollision: Weder geht die alte Datei verloren,
// noch entstehen zwei Dateien zu einer id (F-2081; Sprint-117-Kritiker, F-3051).
// Unter s.anlegen, damit ein gleichzeitiger Upload nicht zwischen Prüfung und
// Zurückschieben dieselbe id belegt.
func (s *AttachmentStore) schiebeZurueck(beiseite, ziel string) error {
	s.anlegen.Lock()
	defer s.anlegen.Unlock()
	if err := os.Rename(beiseite, ziel); err == nil {
		return nil
	}
	if err := os.MkdirAll(ziel, 0o750); err != nil {
		return err
	}
	eintraege, err := os.ReadDir(beiseite)
	if err != nil {
		return err
	}
	liegen := 0
	for _, e := range eintraege {
		belegt, err := zurueckBelegt(ziel, e.Name())
		if err != nil {
			return err
		}
		if belegt {
			liegen++
			continue
		}
		if err := os.Rename(filepath.Join(beiseite, e.Name()), filepath.Join(ziel, e.Name())); err != nil {
			return err
		}
	}
	if liegen > 0 {
		if s.ersteKollisionsMeldung(beiseite) {
			log.Printf("attachments: %d Datei(en) bleiben in %s liegen — Anhang-ID im Kartenverzeichnis inzwischen belegt (F-3051)", liegen, beiseite)
		}
		return fmt.Errorf("%w: %d Datei(en) in %s", ErrPurgeRestKollision, liegen, beiseite)
	}
	return os.RemoveAll(beiseite)
}

// ersteKollisionsMeldung: true nur beim ersten Mal je Rest und Prozess — ein
// Kollisions-Rest bleibt liegen, solange die Karte lebt, und würde sonst bei jedem
// Purge und Start erneut gemeldet (F-3054).
func (s *AttachmentStore) ersteKollisionsMeldung(beiseite string) bool {
	s.beiseiteMu.Lock()
	defer s.beiseiteMu.Unlock()
	if s.kollisionGemeldet[beiseite] {
		return false
	}
	if s.kollisionGemeldet == nil {
		s.kollisionGemeldet = map[string]bool{}
	}
	s.kollisionGemeldet[beiseite] = true
	return true
}

// vergissKollision: Der Rest ist aufgelöst, seine Meldungsmarke fällt weg — die
// Menge wächst sonst bis zum Prozessende (Sprint-117-Verify 2, F-3058).
func (s *AttachmentStore) vergissKollision(beiseite string) {
	s.beiseiteMu.Lock()
	defer s.beiseiteMu.Unlock()
	delete(s.kollisionGemeldet, beiseite)
}

// ErrPurgeRestKollision: beim Zurückschieben war eine Anhang-ID schon belegt; die
// betroffenen Dateien bleiben im Rest unter .purge-beiseite (F-3051).
var ErrPurgeRestKollision = errors.New("attachments: Anhang-ID beim Zurückschieben belegt")

// zurueckBelegt: Darf `name` nicht ins Kartenverzeichnis zurück? Belegt ist der
// gleiche Name und — für Anhänge `<attId>__<datei>` — jede id, die als Präfix vor
// einem `__` stehen kann. Weil auch ids `_` enthalten dürfen, wird jede solche
// Stelle geprüft: lieber eine Datei zu viel im Rest als zwei Dateien zu einer id.
// Teil-Dateien (.teil-…) haben keine id; sie gehen zurück, raeumeTeile räumt sie.
func zurueckBelegt(ziel, name string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(ziel, name)); err == nil {
		return true, nil
	}
	if strings.HasPrefix(name, teilPraefix) {
		return false, nil
	}
	for i := 1; i+2 <= len(name); i++ {
		if name[i:i+2] != "__" {
			continue
		}
		belegt, err := attIdBelegt(ziel, name[:i])
		if err != nil || belegt {
			return belegt, err
		}
	}
	return false, nil
}

// beiseiteRest liest eine Hülle: genau ein Board-Verzeichnis mit genau einem
// Karten-Verzeichnis, beide mit gültigen IDs. ok=false bei jedem anderen Aufbau.
func beiseiteRest(huelle string) (boardId, cardId string, ok bool) {
	boards, err := os.ReadDir(huelle)
	if err != nil || len(boards) != 1 || !boards[0].IsDir() || !beiseiteIDRe.MatchString(boards[0].Name()) {
		return "", "", false
	}
	karten, err := os.ReadDir(filepath.Join(huelle, boards[0].Name()))
	if err != nil || len(karten) != 1 || !karten[0].IsDir() || !beiseiteIDRe.MatchString(karten[0].Name()) {
		return "", "", false
	}
	return boards[0].Name(), karten[0].Name(), true
}

// purgeBeiseiteMaxProAufruf begrenzt, wie viele Reste ein einzelner Purge-Request
// bewertet (je Rest ein Stand-Lesen). Reste, die nie entscheidbar sind, kosten
// sonst jeden Purge erneut (Sprint-117-Kritiker, F-3052). Beim Start gilt keine
// Grenze.
const purgeBeiseiteMaxProAufruf = 20

// RaeumePurgeResteAuf löst beim Serverstart ALLE Reste in .purge-beiseite auf,
// unabhängig vom Alter: Vor dem ersten Request läuft kein Purge, also ist jeder
// Rest ein Absturz- oder Kollisionsrest (wie ReconcileStagedTransfers). Ohne diesen
// Lauf blieben die Dateien einer zurückgeholten Karte unsichtbar, bis irgendein
// Editor einen nachgeholten Purge auslöst (Sprint-117-Kritiker, F-3050).
// Nicht nach Alter → kein uhr-Gate (der Pi startet womöglich ohne gestellte Uhr).
func (s *AttachmentStore) RaeumePurgeResteAuf(lage KartenLageFn) {
	s.raeumeBeiseiteAuf(lage, true)
}

// raeumeBeiseiteAuf kümmert sich um Reste (Absturz zwischen Beiseiteschieben und
// zweiter Prüfung, gescheitertes Zurückschieben). Je Rest wird die Kartenlage
// gefragt: verwaist → löschen, Karte lebt → zurückschieben, Fehler oder
// unbekannter Aufbau → liegen lassen (F-3038). Aus einem Purge-Request
// (beimStart=false) nur Reste älter als purgeBeiseiteMaxAge, nur mit gestellter
// Uhr und höchstens purgeBeiseiteMaxProAufruf; beim Start (beimStart=true) alle.
//
// Kein zweites Nachfragen vor dem Löschen nötig: Die Dateien liegen schon beiseite,
// bevor lage() fragt — dieselbe Reihenfolge wie im Hauptweg (wegnehmen, prüfen,
// löschen); was danach geschrieben wird, kommt nach der Löschentscheidung.
func (s *AttachmentStore) raeumeBeiseiteAuf(lage KartenLageFn, beimStart bool) {
	if lage == nil {
		return
	}
	grenze := int64(math.MaxInt64)
	if !beimStart {
		// Löschen nach Alter: nur bei gestellter Uhr (knowledge §38, wie sweepStaleStaging).
		if !uhr.Erlaubt("attachment-purge-beiseite", nil) {
			return
		}
		grenze = time.Now().Add(-purgeBeiseiteMaxAge).Unix()
	}
	root := filepath.Join(s.baseDir, purgeBeiseiteRoot)
	eintraege, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var kandidaten []string
	for _, e := range eintraege {
		m := beiseiteHuelleRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		seit, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seit > grenze {
			continue
		}
		kandidaten = append(kandidaten, e.Name())
	}
	if !beimStart && len(kandidaten) > purgeBeiseiteMaxProAufruf {
		// Rundlauf (F-3054): Reste ohne Fortschritt (Kollision, unlesbare Lage)
		// belegten sonst dauerhaft das Kontingent, neuere kämen im Betrieb nie dran.
		s.beiseiteMu.Lock()
		ab := s.beiseiteVersatz % len(kandidaten)
		s.beiseiteVersatz = ab + purgeBeiseiteMaxProAufruf
		s.beiseiteMu.Unlock()
		auswahl := make([]string, 0, purgeBeiseiteMaxProAufruf)
		for i := 0; i < purgeBeiseiteMaxProAufruf; i++ {
			auswahl = append(auswahl, kandidaten[(ab+i)%len(kandidaten)])
		}
		kandidaten = auswahl
	}
	for _, name := range kandidaten {
		huelle := filepath.Join(root, name)
		boardId, cardId, ok := beiseiteRest(huelle)
		if !ok {
			// Leere Hüllen (Absturz zwischen MkdirAll und rename) räumen, ohne Inhalt
			// anzufassen: os.Remove entfernt nur leere Verzeichnisse (F-3048).
			leereHuelleEntfernen(huelle)
			continue
		}
		verwaist, err := lage(boardId, cardId)
		if err != nil {
			continue
		}
		beiseite := filepath.Join(huelle, boardId, cardId)
		if verwaist {
			if os.RemoveAll(huelle) == nil {
				s.vergissKollision(beiseite)
			}
		} else if s.schiebeZurueck(beiseite, s.cardDir(boardId, cardId)) == nil {
			huelleEntfernen(huelle, boardId)
			s.vergissKollision(beiseite)
		}
	}
}

// leereHuelleEntfernen entfernt eine Hülle nur, wenn sie leer ist oder nur leere
// Board-Verzeichnisse enthält. os.Remove scheitert an allem, was Inhalt hat — eine
// Datei kann hier nie verloren gehen (F-3048).
func leereHuelleEntfernen(huelle string) {
	eintraege, err := os.ReadDir(huelle)
	if err != nil {
		return
	}
	for _, e := range eintraege {
		_ = os.Remove(filepath.Join(huelle, e.Name()))
	}
	_ = os.Remove(huelle)
}
