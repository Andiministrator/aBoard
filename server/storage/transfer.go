package storage

// transfer.go — Cross-Board-Karten-Transfer (#296, Sprint 43, ADR-0044/0050).
//
// User-Wunsch: eine Karte aus Board A in Board B verschieben (typischer Fall:
// Service-Board-Inbox-Karte → Projekt-Board). UUID bleibt erhalten (server-
// weit eindeutig), displayId wird im Ziel-Board neu vergeben.
//
// Atomicity: BEGIN IMMEDIATE-Tx über die boards-Tabelle. Beide Boards werden
// im selben Tx-Kontext gelesen, mutiert und upgedatet. Bei Fehler in einer
// der beiden Mutationen wird die Tx gerollt — kein partieller Zustand
// (weder Datenverlust noch Duplikat). Dieselbe Connection führt die ganze
// Tx aus (F-191-Pattern aus users.SetAdminFlagSelfSafe).
//
// Server-only-Feature: claude-code-Boards (Single-File) haben kein Konzept
// von „anderem Board" — diese Methode wird ausschließlich vom REST-Backend
// aufgerufen, NICHT vom Browser-Projektor.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// Transfer-spezifische Sentinel-Fehler. ErrBoardNotFound + ErrConflict
// werden aus boards.go wiederverwendet.
var (
	ErrCardNotFound      = errors.New("storage: card nicht gefunden")
	ErrInvalidColumn     = errors.New("storage: column-id im ziel-board nicht definiert")
	ErrTransferSameBoard = errors.New("storage: quell- und ziel-board sind identisch")
	ErrTransferMetaCard  = errors.New("storage: sprint-meta-karten können nicht transferiert werden")
)

// TransferCardParams beschreibt einen Cross-Board-Karten-Transfer.
type TransferCardParams struct {
	SrcBoardID  string
	DstBoardID  string
	CardID      string // UUID — bleibt erhalten
	DstColumnID string // muss in dst.board.columnDefs existieren
	Actor       string // Identität für Activity-Eintrag (z. B. Email)
}

// TransferCardResult trägt die im Ziel-Board vergebene neue displayId
// sowie die alte displayId aus dem Quell-Board (für UI-Toast „transferiert
// als #N"). Bei einer Finding-Karte (ADR-0133 §1, F-1542) zusätzlich die im
// Ziel neu vergebene und die alte Finding-Nummer; bei anderen Karten 0.
type TransferCardResult struct {
	NewDisplayID int
	OldDisplayID int
	NewFindingNr int
	OldFindingNr int
}

// TransferCard verschiebt eine Karte atomar zwischen zwei Boards.
//
// Ablauf (alles in EINER BEGIN IMMEDIATE-Tx, eine Connection):
//  1. Beide Boards via SELECT laden (state_json, updated_at).
//  2. Karte im Quell-Board lokalisieren + entfernen.
//  3. dst.board.nextDisplayId hochzählen → neue displayId für die Karte.
//  4. Karte-columnId → DstColumnID (gegen dst.board.columnDefs validiert).
//     Finding-Karte (F-1542): neue `finding.nr` aus dst.board.nextFindingNr,
//     die alte Nummer bleibt als Kommentar auffindbar (Suche) und steht im
//     Activity-Eintrag.
//  5. Activity-Eintrag "board-transferred" auf die transferierte Karte
//     (mit from/to-Info).
//  6. Karte in dst.board.sprints[__backlog__].cards anhängen.
//  7. Beide Boards via UPDATE schreiben (mit ifMatch-Schutz inline).
//  8. COMMIT — bei jedem Fehler ROLLBACK.
//
// Fehler:
//   - ErrTransferSameBoard          — src == dst
//   - ErrBoardNotFound              — eines der beiden Boards nicht da
//   - ErrCardNotFound               — Karte nicht in src
//   - ErrTransferMetaCard           — Karte ist sprint-meta
//   - ErrInvalidColumn              — DstColumnID nicht in dst.board.columnDefs
//   - ErrConflict                   — updated_at-Race nach Tx-Start (selten,
//     da BEGIN IMMEDIATE serialisiert; aber theoretisch möglich, wenn ein
//     anderer Caller exakt zwischen unserem SELECT und UPDATE schreibt; dann
//     ist die Tx ohnehin gerollt — Caller kann retryen)
//   - sonstige DB-Fehler              — durchgereicht
func (r *BoardRepo) TransferCard(ctx context.Context, p TransferCardParams) (*TransferCardResult, error) {
	if p.SrcBoardID == "" || p.DstBoardID == "" || p.CardID == "" || p.DstColumnID == "" {
		return nil, fmt.Errorf("storage.TransferCard: leere Pflichtfelder")
	}
	if p.SrcBoardID == p.DstBoardID {
		return nil, ErrTransferSameBoard
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage.TransferCard: conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("storage.TransferCard: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	srcState, srcTitle, srcVersion, srcTs, err := loadBoardState(ctx, conn, p.SrcBoardID)
	if err != nil {
		return nil, err
	}
	dstState, dstTitle, dstVersion, dstTs, err := loadBoardState(ctx, conn, p.DstBoardID)
	if err != nil {
		return nil, err
	}
	// ADR-0141 §5.1/§7 (Karte #826): Für die Spiegel-Schranke die Stände vor
	// dem Transfer festhalten (Prüfung unten, vor dem Schreiben).
	srcVorher, err := json.Marshal(srcState)
	if err != nil {
		return nil, fmt.Errorf("storage.TransferCard: marshal: %w", err)
	}
	dstVorher, err := json.Marshal(dstState)
	if err != nil {
		return nil, fmt.Errorf("storage.TransferCard: marshal: %w", err)
	}

	// 1. Karte im Quell-Board finden + entfernen
	card, oldDisplayID, err := extractCardFromState(srcState, p.CardID)
	if err != nil {
		return nil, err
	}

	// 2. dst-Spalte validieren
	if !columnExists(dstState, p.DstColumnID) {
		return nil, ErrInvalidColumn
	}

	// 3. neue displayId im Ziel-Board vergeben
	newDisplayID := nextDisplayID(dstState)

	// 4. Karte für Ziel-Board anpassen
	card["displayId"] = float64(newDisplayID)
	card["columnId"] = p.DstColumnID
	card["updated"] = time.Now().UTC().Format("2006-01-02")

	// 4a. Finding-Nummer neu vergeben (F-1542, ADR-0133 §1 „keine Doppel").
	// Die Nummer gehört zum Board, nicht zur Karte: übernähme der Transfer
	// sie, stünde sie im Ziel womöglich doppelt, und dessen Zähler liefe ihr
	// hinterher. Wie bei der displayId gilt deshalb: neue Nummer im Ziel. Die
	// alte darf aber nicht verschwinden — Commits, Kommentare und Notizen
	// verweisen auf sie. Sie landet als Kommentar auf der Karte, weil die
	// Board-Suche Kommentare durchsucht (`card.finding` ist ein geschlossenes
	// Feldset, ein Zusatzfeld lehnte der Fold ab), und im Activity-Eintrag.
	// Muss VOR dem Anhängen an dst laufen: die Karte trägt noch die alte
	// Nummer und zählte sonst beim Maximum des Ziels mit.
	oldFindingNr, newFindingNr := 0, 0
	fromInfo := map[string]any{
		"boardId":   p.SrcBoardID,
		"displayId": float64(oldDisplayID),
	}
	toInfo := map[string]any{
		"boardId":   p.DstBoardID,
		"displayId": float64(newDisplayID),
		"columnId":  p.DstColumnID,
	}
	if finding := findingOf(card); finding != nil {
		if nr, ok := ganzzahl(finding["nr"]); ok {
			oldFindingNr = nr
			fromInfo["findingNr"] = float64(nr)
		}
		newFindingNr = nextFindingNr(dstState)
		finding["nr"] = float64(newFindingNr)
		toInfo["findingNr"] = float64(newFindingNr)
		text := fmt.Sprintf("Übertragen aus Board „%s“; hier F-%d.", srcTitle, newFindingNr)
		if oldFindingNr > 0 {
			text = fmt.Sprintf("Übertragen aus Board „%s“, dort F-%d; hier F-%d.", srcTitle, oldFindingNr, newFindingNr)
		}
		existing, _ := card["comments"].([]any)
		card["comments"] = append(existing, map[string]any{
			"id":      uuid.NewString(),
			"author":  p.Actor,
			"text":    text,
			"created": time.Now().UTC().Format("2006-01-02"),
		})
	}

	// 4b. Ausgangsschleuse (Karte #686, Kritiker KS3-1/KS3-2): Die Aufträge
	// der Karte wandern mit — sonst verlöre der Antwortkommentar „extern“ im
	// Ziel seinen Beleg. Ein Kommentar „extern“, zu dem es im Quell-Board
	// keinen Auftrag gibt, ist nicht vom Server geschrieben (etwa über ein
	// per POST angelegtes Board eingeschleust): Er verliert im Ziel seine
	// Kennzeichnung und wird ein gewöhnlicher Kommentar.
	if err := schleuseBeimTransfer(ctx, conn, p.SrcBoardID, p.DstBoardID, p.CardID, card); err != nil {
		return nil, err
	}

	// 5. Activity-Eintrag anhängen
	appendActivity(card, map[string]any{
		"id":        uuid.NewString(),
		"actor":     p.Actor,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      "board-transferred",
		"from":      fromInfo,
		"to":        toInfo,
	})

	// 6. Karte in dst.__backlog__-Lane anhängen
	if err := appendCardToBacklog(dstState, card); err != nil {
		return nil, err
	}

	// 6a. Spiegel-Schranke (ADR-0141 §7): Transfer ist für Spiegel in beide
	// Richtungen gesperrt — eine Spiegelkarte verlässt die Kopie nicht, und in
	// die gespiegelte Backlog-Lane einer Kopie kommt keine fremde Karte.
	for _, b := range []struct {
		id     string
		vorher []byte
		state  map[string]any
	}{{p.SrcBoardID, srcVorher, srcState}, {p.DstBoardID, dstVorher, dstState}} {
		nachher, err := json.Marshal(b.state)
		if err != nil {
			return nil, fmt.Errorf("storage.TransferCard: marshal: %w", err)
		}
		if err := spiegelSchranke(ctx, conn, b.id, b.vorher, nachher); err != nil {
			return nil, err
		}
	}

	// 7. Beide Boards UPDATEn
	if err := updateBoardInTx(ctx, conn, p.SrcBoardID, srcTitle, srcVersion, srcState, srcTs); err != nil {
		return nil, err
	}
	if err := updateBoardInTx(ctx, conn, p.DstBoardID, dstTitle, dstVersion, dstState, dstTs); err != nil {
		return nil, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("storage.TransferCard: commit: %w", err)
	}
	committed = true
	return &TransferCardResult{
		NewDisplayID: newDisplayID,
		OldDisplayID: oldDisplayID,
		NewFindingNr: newFindingNr,
		OldFindingNr: oldFindingNr,
	}, nil
}

// HasNonSystemACLEntries meldet, ob im ACL-Bestand des Servers mindestens ein
// Eintrag für eine ECHTE Identität steht (F-391).
//
// Warum die Methode hier und nicht in acl.go steht: sie hat genau einen
// Aufrufer — den Ziel-Board-Check des Cross-Board-Transfers — und ist Teil
// dieses Features, nicht der allgemeinen ACL-Verwaltung.
//
// Warum „nicht-System" statt „irgendein Eintrag": serviceboard.Seed schreibt
// beim Start JEDER Instanz einen editor-Eintrag für den Mail-Worker
// (MailWorkerEmail), und das Service-Board ist per Default an. „Es gibt
// irgendeinen ACL-Eintrag" wäre damit in JEDEM Setup wahr — auch im
// dokumentierten Single-User-Default (docs/server-betrieb.md
// §Zustandsmatrix), in dem gar keine Identitäten existieren. Der Transfer
// wäre dort schlagartig tot. Ein Eintrag für eine echte Identität ist dagegen
// ein belastbares Signal dafür, dass auf diesem Server Boards geteilt werden.
func (r *AclRepo) HasNonSystemACLEntries(ctx context.Context) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM acl WHERE user_email <> ?)`
	var found int
	if err := r.db.QueryRowContext(ctx, q, MailWorkerEmail).Scan(&found); err != nil {
		return false, fmt.Errorf("acl.HasNonSystemACLEntries: %w", err)
	}
	return found != 0, nil
}

// loadBoardState liest Title/Version/State/UpdatedAt eines Boards innerhalb
// der laufenden Tx (über die übergebene Connection — wichtig, damit der
// READ unter dem BEGIN IMMEDIATE-Lock geschieht).
func loadBoardState(ctx context.Context, conn *sql.Conn, boardID string) (map[string]any, string, string, int64, error) {
	var title, version string
	var stateJSON []byte
	var updatedAt int64
	err := conn.QueryRowContext(ctx,
		`SELECT title, version, state_json, updated_at FROM boards WHERE id = ?`, boardID).
		Scan(&title, &version, &stateJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", 0, ErrBoardNotFound
	}
	if err != nil {
		return nil, "", "", 0, fmt.Errorf("storage.TransferCard: load %s: %w", boardID, err)
	}
	var state map[string]any
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return nil, "", "", 0, fmt.Errorf("storage.TransferCard: parse state_json (%s): %w", boardID, err)
	}
	return state, title, version, updatedAt, nil
}

// extractCardFromState durchsucht alle Sprints + Cards eines Boards nach
// der gegebenen UUID, entfernt die Karte aus ihrer Lane und liefert sie
// inklusive ihrer alten displayId zurück. Sprint-Meta-Karten sind tabu.
func extractCardFromState(state map[string]any, cardID string) (map[string]any, int, error) {
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return nil, 0, fmt.Errorf("storage.TransferCard: state.board fehlt")
	}
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		if spMap == nil {
			continue
		}
		cards, _ := spMap["cards"].([]any)
		for i, c := range cards {
			cMap, _ := c.(map[string]any)
			if cMap == nil {
				continue
			}
			if id, _ := cMap["id"].(string); id == cardID {
				if kind, _ := cMap["kind"].(string); kind == "sprint-meta" {
					return nil, 0, ErrTransferMetaCard
				}
				oldDisplay := 0
				if v, ok := cMap["displayId"].(float64); ok {
					oldDisplay = int(v)
				}
				// Karte aus der Lane entfernen (Slice-Splice).
				newCards := make([]any, 0, len(cards)-1)
				newCards = append(newCards, cards[:i]...)
				newCards = append(newCards, cards[i+1:]...)
				spMap["cards"] = newCards
				return cMap, oldDisplay, nil
			}
		}
	}
	return nil, 0, ErrCardNotFound
}

// columnExists prüft, ob columnId in board.columnDefs deklariert ist.
func columnExists(state map[string]any, columnID string) bool {
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return false
	}
	defs, _ := board["columnDefs"].([]any)
	for _, def := range defs {
		defMap, _ := def.(map[string]any)
		if defMap == nil {
			continue
		}
		if id, _ := defMap["id"].(string); id == columnID {
			return true
		}
	}
	return false
}

// nextDisplayID liest board.nextDisplayId, inkrementiert es und liefert
// den ursprünglichen Wert (Server-side äquivalent zum Browser-Pfad).
func nextDisplayID(state map[string]any) int {
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return 1
	}
	next := 1
	if v, ok := board["nextDisplayId"].(float64); ok {
		next = int(v)
	}
	board["nextDisplayId"] = float64(next + 1)
	return next
}

// findingOf liefert `card.finding`, wenn die Karte ein Finding ist
// (`kind: 'finding'` mit Objekt), sonst nil.
func findingOf(card map[string]any) map[string]any {
	if kind, _ := card["kind"].(string); kind != "finding" {
		return nil
	}
	finding, _ := card["finding"].(map[string]any)
	return finding
}

// ganzzahl liest eine JSON-Zahl als ganze Zahl ≥ 1 (Gegenstück zu
// `Number.isInteger` im Fold, eingeschränkt auf gültige Nummern).
func ganzzahl(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f < 1 || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// nextFindingNr ist das Gegenstück zu `nextFindingNr` in src/finding.js
// (F-1542): Zähler board.nextFindingNr, repariert, wenn er fehlt oder nicht
// über der höchsten vergebenen Nummer liegt. Liefert die vergebene Nummer und
// hebt den Zähler an.
func nextFindingNr(state map[string]any) int {
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return 1
	}
	hoechste := 0
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		if spMap == nil {
			continue
		}
		cards, _ := spMap["cards"].([]any)
		for _, c := range cards {
			cMap, _ := c.(map[string]any)
			if cMap == nil {
				continue
			}
			if f := findingOf(cMap); f != nil {
				if nr, ok := ganzzahl(f["nr"]); ok && nr > hoechste {
					hoechste = nr
				}
			}
		}
	}
	next, ok := ganzzahl(board["nextFindingNr"])
	if !ok || next <= hoechste {
		next = hoechste + 1
	}
	board["nextFindingNr"] = float64(next + 1)
	return next
}

// appendActivity hängt einen Activity-Eintrag an card.activity an
// (Idempotenz-egal — Caller stellt UUID-Einzigartigkeit sicher).
func appendActivity(card map[string]any, entry map[string]any) {
	existing, _ := card["activity"].([]any)
	card["activity"] = append(existing, entry)
}

// appendCardToBacklog hängt die Karte am Ende der __backlog__-Sprint-Lane
// des Ziel-Boards an. Wenn die Lane fehlt, ist das ein Schema-Bug — wir
// signalisieren es klar.
func appendCardToBacklog(state map[string]any, card map[string]any) error {
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return fmt.Errorf("storage.TransferCard: dst.state.board fehlt")
	}
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		if spMap == nil {
			continue
		}
		if id, _ := spMap["id"].(string); id == "__backlog__" {
			cards, _ := spMap["cards"].([]any)
			spMap["cards"] = append(cards, card)
			return nil
		}
	}
	return fmt.Errorf("storage.TransferCard: dst hat keine __backlog__-Lane")
}

// updateBoardInTx schreibt das mutierte State-Objekt zurück innerhalb der
// laufenden Tx. Inkrementiert updated_at strikt monoton (ETag-Stabilität,
// vgl. boards.Update). Wir verifizieren updated_at vorher == oldTs als
// Inline-If-Match-Schutz — bei Mismatch ErrConflict.
func updateBoardInTx(ctx context.Context, conn *sql.Conn, boardID, title, version string, state map[string]any, oldTs int64) error {
	newJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("storage.TransferCard: marshal %s: %w", boardID, err)
	}
	// Tx-internes If-Match: aktuelles updated_at muss noch oldTs sein. Da
	// BEGIN IMMEDIATE den Write-Lock hält, ist Drift fast ausgeschlossen —
	// der Check ist Defense-in-depth.
	var currentTs int64
	err = conn.QueryRowContext(ctx, `SELECT updated_at FROM boards WHERE id = ?`, boardID).Scan(&currentTs)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBoardNotFound
	}
	if err != nil {
		return fmt.Errorf("storage.TransferCard: precheck %s: %w", boardID, err)
	}
	if currentTs != oldTs {
		return ErrConflict
	}
	ts := nowMillis()
	if ts <= currentTs {
		ts = currentTs + 1
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE boards SET title = ?, version = ?, state_json = ?, updated_at = ? WHERE id = ?`,
		title, version, newJSON, ts, boardID); err != nil {
		return fmt.Errorf("storage.TransferCard: update %s: %w", boardID, err)
	}
	return nil
}

// ── F-600: wo liegt eine Karte? ──────────────────────────────────────────

// BoardFuerKarte sucht die Karte mit dieser ID in allen Boards und liefert die
// ID des Boards, in dem sie liegt. Rückgabe ("", nil) heisst: in keinem.
//
// WOZU: Der Anhang-Abgleich nach einem Absturz (F-600,
// AttachmentStore.ReconcileStagedTransfers) muss unterscheiden, ob die
// DB-Transaktion eines unterbrochenen Transfers durchgelaufen ist. Die einzige
// Auskunft, die er dafür braucht, ist diese.
//
// WARUM EIN LINEARER DURCHLAUF VERTRETBAR IST: Karten leben im `state_json`
// eines Boards, es gibt keine Karten-Tabelle zum Abfragen. Der Durchlauf lädt
// also jedes Board einmal. Das ist teuer — aber er läuft ausschliesslich beim
// Serverstart und ausschliesslich dann, wenn überhaupt ein Staging-Rest
// existiert. Im Normalbetrieb ist das nie der Fall: ein Rest entsteht nur,
// wenn der Prozess mitten in einem Transfer stirbt.
//
// Ein Board, dessen `state_json` sich nicht lesen lässt, wird ÜBERSPRUNGEN,
// nicht als Fehler gemeldet: Sonst könnte ein einzelnes kaputtes Board den
// Abgleich für alle anderen blockieren.
//
// ACHTUNG, das ist die unangenehme Stelle (F-611): Die Karte gilt dann als
// „nicht gefunden" — und der Aufrufer VERWIRFT daraufhin das Staging, er lässt
// es NICHT liegen. Ein früherer Kommentar behauptete hier das Gegenteil.
// Konkret heisst das: Ein einziges beschädigtes Board (oder ein transienter
// `Get`-Fehler) genügt, damit ein Transfer, dessen Datenbank-Transaktion
// durchgelaufen war, seine gestagete Kopie verliert. Die Quell-Dateien stehen
// dann noch, es geht also nichts sofort verloren — aber der automatische
// Reparaturweg ist weg, und das Log meldet „abgebrochener Versuch", also die
// falsche Diagnose.
//
// Warum es trotzdem so bleibt: Die Gegenrichtung („im Zweifel liegen lassen")
// verlangt, dass der Aufrufer „nicht gefunden" von „nicht nachschaubar"
// unterscheiden kann. Das wäre ein zweiter Rückgabewert und eine Änderung am
// Vertrag — sinnvoll, aber grösser als diese Nacharbeit. Bis dahin gilt: Der
// Fall ist selten (beschädigtes state_json), sein Schaden ist begrenzt (kein
// Datenverlust), und er ist hier benannt statt still.
func BoardFuerKarte(ctx context.Context, repo *BoardRepo, cardId string) (string, error) {
	if repo == nil || cardId == "" {
		return "", nil
	}
	summaries, err := repo.List(ctx)
	if err != nil {
		return "", fmt.Errorf("storage.BoardFuerKarte: boards auflisten: %w", err)
	}
	for _, sum := range summaries {
		b, err := repo.Get(ctx, sum.ID)
		if err != nil || b == nil {
			continue
		}
		var state map[string]any
		if err := json.Unmarshal(b.StateJSON, &state); err != nil {
			continue
		}
		board, _ := state["board"].(map[string]any)
		if board == nil {
			board = state
		}
		sprints, _ := board["sprints"].([]any)
		for _, sp := range sprints {
			spMap, _ := sp.(map[string]any)
			if spMap == nil {
				continue
			}
			cards, _ := spMap["cards"].([]any)
			for _, c := range cards {
				cMap, _ := c.(map[string]any)
				if cMap == nil {
					continue
				}
				if id, _ := cMap["id"].(string); id == cardId {
					return sum.ID, nil
				}
			}
		}
	}
	return "", nil
}

// schleuseBeimTransfer hängt die Schleusen-Aufträge der Karte an das
// Ziel-Board um und entfernt die Kennzeichnung „extern“ von Kommentaren, zu
// denen kein Auftrag gehört (Karte #686, KS3-1/KS3-2). Läuft in der
// Transaktion des Transfers.
func schleuseBeimTransfer(ctx context.Context, conn *sql.Conn, src, dst, cardID string, card map[string]any) error {
	rows, err := conn.QueryContext(ctx,
		`SELECT kommentar_id FROM schleuse_auftraege WHERE board_id = ? AND card_id = ? AND kommentar_id IS NOT NULL`, src, cardID)
	if err != nil {
		return fmt.Errorf("storage.TransferCard: schleuse lesen: %w", err)
	}
	belegt := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("storage.TransferCard: schleuse scan: %w", err)
		}
		belegt[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storage.TransferCard: schleuse rows: %w", err)
	}
	// Sitzungs-Kritiker SK-2: Ein Auftrag im Zustand „gesendet“ schreibt seinen
	// Kommentar, BEVOR er kommentar_id setzt. Läuft gerade einer, ist ein
	// Kommentar ohne Auftrags-Treffer womöglich genau dessen Antwort — dann
	// wird nichts entkennzeichnet. Ob er verbürgt ist, zeigt der Beleg.
	var laufend int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schleuse_auftraege WHERE board_id = ? AND card_id = ? AND zustand = 'gesendet'`, src, cardID).Scan(&laufend); err != nil {
		return fmt.Errorf("storage.TransferCard: schleuse zählen: %w", err)
	}
	if ks, ok := card["comments"].([]any); ok && laufend == 0 {
		for _, k := range ks {
			km, ok := k.(map[string]any)
			if !ok {
				continue
			}
			if art, _ := km["kind"].(string); art != "extern" {
				continue
			}
			if id, _ := km["id"].(string); !belegt[id] {
				delete(km, "kind")
				delete(km, "schleuse")
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schleuse_auftraege SET board_id = ? WHERE board_id = ? AND card_id = ?`, dst, src, cardID); err != nil {
		return fmt.Errorf("storage.TransferCard: schleuse umhängen: %w", err)
	}
	return nil
}
