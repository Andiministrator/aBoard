package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

func runImport(args []string) error {
	return runImportMitArgs(envOrDefault("ABOARD_DB", defaultDBPath), args)
}

func runImportMitArgs(dbPath string, args []string) error {
	// KA-7: --findings-pruefung=aus ist der ausdrückliche Notfall-Schalter für
	// Dateien mit Finding-Verstößen aus der Zeit vor F-1572.
	pruefungAus := false
	var rest []string
	for _, a := range args {
		if a == "--findings-pruefung=aus" {
			pruefungAus = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 1 {
		// F-1934: Bisher zählte nur rest[0], alles Weitere fiel still weg.
		return fmt.Errorf("Verwendung: aboard-server import [--findings-pruefung=aus] <datei.json> (genau eine Datei, erhalten: %v)", rest)
	}
	return importFileOpt(context.Background(), dbPath, rest[0], pruefungAus)
}

// importFile liest eine aboard-sync.json-Datei und legt das Board in der SQLite-
// Datenbank an. Existiert das Board bereits (gleiche ID), wird es überschrieben
// (ifMatch=0 / Force-Update).
func importFile(ctx context.Context, dbPath, filePath string) error {
	return importFileOpt(ctx, dbPath, filePath, false)
}

// importFileOpt wie importFile; pruefungAus schaltet die Finding-Prüfung ab
// (Notfall-Weg, KA-7): Verstöße stehen dann als Warnung im Log.
func importFileOpt(ctx context.Context, dbPath, filePath string, pruefungAus bool) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("import: Datei lesen: %w", err)
	}

	// Minimales Parsen — state_json bleibt opak (ADR-0044).
	var envelope struct {
		// F-3070 (Sitzungs-Kritik 11.11.): von Hand angelegte Boards tragen die Version
		// auch als Zahl (`"version": 2`); siehe importVersion.
		Version json.RawMessage `json:"version"`
		Board   struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"board"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("import: JSON parsen: %w", err)
	}

	boardTitle := envelope.Board.Title
	if boardTitle == "" {
		return fmt.Errorf("import: board.title fehlt oder leer")
	}
	boardID := envelope.Board.ID
	if boardID == "" {
		boardID = uuid.New().String()
		log.Printf("import: board.id nicht gefunden — neue UUID generiert: %s", boardID)
		// UUID in state_json zurückschreiben damit DB-Schlüssel und JSON-Inhalt konsistent sind.
		raw, err = injectBoardID(raw, boardID)
		if err != nil {
			return fmt.Errorf("import: board.id einbetten: %w", err)
		}
	}
	version, err := importVersion(envelope.Version)
	if err != nil {
		return err
	}
	// F-3088/F-3092: Steht im Blob nicht schon ein nichtleerer Text (Zahl, fehlend, null, ""),
	// die Version als Text hineinschreiben. Sonst scheitern Leser mit `Version string`
	// (Backup-Restore, PUT) bzw. die Prüfung auf leere Version (restore.go, boards_ops.go).
	if !istVersionText(envelope.Version) {
		raw, err = setzeVersionText(raw, version)
		if err != nil {
			return fmt.Errorf("import: version normalisieren: %w", err)
		}
	}

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		return fmt.Errorf("import: DB öffnen: %w", err)
	}
	defer db.Close()

	repo := storage.NewBoardRepo(db)

	existing, err := repo.Get(ctx, boardID)
	// F-1572: Finding-Karten prüfen wie bei den Ops. Bei einem bestehenden
	// Board zählt nur, was gegenüber dem gespeicherten Stand neu verstösst;
	// ältere Sicherungen ohne Findings sind nicht betroffen.
	var gespeichert []byte
	if err == nil {
		gespeichert = existing.StateJSON
	}
	if v := storage.NeueFindingVerstoesse(gespeichert, raw); len(v) > 0 {
		if !pruefungAus {
			return fmt.Errorf("import: %s (Notfall: --findings-pruefung=aus)", storage.FindingVerstossMeldung(v))
		}
		log.Printf("import: WARNUNG — Finding-Prüfung abgeschaltet, %d Verstoß/Verstöße übergangen: %s",
			len(v), storage.FindingVerstossMeldung(v))
	}
	if err == nil {
		// Board existiert bereits — überschreiben (Force-Update, kein ETag-Check).
		// F-1685: Ein Import bringt einen vorhandenen Stand zurück wie die
		// Archiv-Rückspielung — Finding-Nummern bleiben, wie sie sind.
		if _, err := repo.Update(storage.MitFindingNummernBehalten(ctx), boardID, boardTitle, version, raw, 0); err != nil {
			return fmt.Errorf("import: Update: %w", err)
		}
		log.Printf("import: Board %q (%s) aktualisiert (updated_at=%d → neu)", boardTitle, boardID, existing.UpdatedAt)
		return nil
	}
	if !errors.Is(err, storage.ErrBoardNotFound) {
		return fmt.Errorf("import: Existenz-Check: %w", err)
	}

	// Neues Board anlegen.
	if _, err := repo.Insert(ctx, boardID, boardTitle, version, raw); err != nil {
		return fmt.Errorf("import: Insert: %w", err)
	}
	log.Printf("import: Board %q (%s) neu importiert", boardTitle, boardID)
	return nil
}

// injectBoardID schreibt boardID in board.id des JSON-Blobs zurück. Nötig wenn
// die Quell-Datei kein id-Feld hat — DB-Schlüssel und state_json müssen
// übereinstimmen damit das Frontend nach dem Load dieselbe ID sieht.
func injectBoardID(raw []byte, boardID string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(doc["board"], &board); err != nil {
		return nil, err
	}
	idJSON, err := json.Marshal(boardID)
	if err != nil {
		return nil, err
	}
	board["id"] = idJSON
	boardJSON, err := json.Marshal(board)
	if err != nil {
		return nil, err
	}
	doc["board"] = boardJSON
	return json.Marshal(doc)
}

// istVersionText: true, wenn roh ein nichtleerer JSON-Text ist.
func istVersionText(roh json.RawMessage) bool {
	var text string
	return len(roh) > 0 && roh[0] == '"' && json.Unmarshal(roh, &text) == nil && text != ""
}

// setzeVersionText schreibt `version` als JSON-Text in den Blob zurück (übrige Felder unverändert).
func setzeVersionText(raw []byte, version string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	v, err := json.Marshal(version)
	if err != nil {
		return nil, err
	}
	doc["version"] = v
	return json.Marshal(doc)
}

// importVersion liest `version` als Text; fehlt sie, gilt "2". Eine Zahl 1 oder 2 wird zu
// "1"/"2" (F-3070, wie versionAlsText im Browser, F-3069). Alles andere ist ein klarer Fehler
// statt „cannot unmarshal number“.
func importVersion(roh json.RawMessage) (string, error) {
	if len(roh) == 0 || string(roh) == "null" {
		return "2", nil
	}
	var text string
	if err := json.Unmarshal(roh, &text); err == nil {
		if text == "" {
			return "2", nil
		}
		return text, nil
	}
	switch string(roh) {
	case "1", "2":
		return string(roh), nil
	}
	return "", fmt.Errorf("import: version %s nicht lesbar (erwartet \"2\")", string(roh))
}
