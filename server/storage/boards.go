package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/schatten"
	"time"
)

// Board ist die persistente Repräsentation eines Boards. state_json bleibt
// opak (ADR-0044): das gesamte Frontend-State-Objekt {version, board} wird
// als JSON-Blob abgelegt; Sprints/Cards/Comments leben darin verschachtelt.
type Board struct {
	ID        string
	Title     string
	Version   string
	StateJSON []byte
	UpdatedAt int64 // Unix-ms, Optimistic-Concurrency-Token
	CreatedAt int64 // Unix-ms
	// VertraulichBewahrt: nur aus Update — Kennungen der Karten und Termine,
	// deren Schalter „vertraulich“ der Schreiber abschalten wollte und die
	// Update vertraulich gelassen hat (ADR-0140 §10, Karte #778). Der PUT
	// meldet sie im Header X-Aboard-Vertraulich-Bewahrt.
	VertraulichBewahrt []string
}

// Summary trägt die Felder, die für Listenansichten reichen (z. B. Switch-
// Popover). State_json wird bewusst nicht geliefert — bei vielen Boards
// würde das die Antwort aufblähen.
type Summary struct {
	ID        string
	Title     string
	UpdatedAt int64
	CreatedAt int64 // Unix-ms; die Sicherungs-Liste meldet es als `angelegt` (F-2322)
	// Awaiting: board.awaitingUser als JSON-Objekt, nil ohne Eintrag (ADR-0156,
	// Karte #1197). Kommt aus der von Triggern gepflegten Spalte awaiting_json,
	// nicht aus state_json — die Liste parst keinen Stand.
	Awaiting []byte
}

var (
	ErrBoardNotFound = errors.New("storage: board nicht gefunden")
	// ErrConflict signalisiert eine If-Match-Verletzung beim Update.
	ErrConflict = errors.New("storage: optimistic-concurrency-konflikt")
)

// BoardRepo bündelt die CRUD-Operationen auf der boards-Tabelle.
type BoardRepo struct {
	db *sql.DB
}

func NewBoardRepo(db *sql.DB) *BoardRepo {
	return &BoardRepo{db: db}
}

// nowMillis liefert die aktuelle Zeit als Unix-Millisekunden. Als Variable,
// damit Tests sie monoton hochzählen können.
var nowMillis = func() int64 {
	return time.Now().UnixMilli()
}

// List liefert alle Boards, sortiert nach updated_at absteigend.
func (r *BoardRepo) List(ctx context.Context) ([]Summary, error) {
	const q = `SELECT id, title, updated_at, created_at, awaiting_json FROM boards ORDER BY updated_at DESC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("boards.List: %w", err)
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var aw sql.NullString
		if err := rows.Scan(&s.ID, &s.Title, &s.UpdatedAt, &s.CreatedAt, &aw); err != nil {
			return nil, fmt.Errorf("boards.List scan: %w", err)
		}
		// Kritik 24.11. A-5: ein leeres Objekt heißt „niemand wartet“ — wie fehlend.
		if aw.Valid && aw.String != "" && aw.String != "{}" {
			s.Awaiting = []byte(aw.String)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("boards.List rows: %w", err)
	}
	return out, nil
}

// Get holt ein Board oder gibt ErrBoardNotFound zurück.
func (r *BoardRepo) Get(ctx context.Context, id string) (*Board, error) {
	const q = `SELECT id, title, version, state_json, updated_at, created_at
	            FROM boards WHERE id = ?`
	b := &Board{}
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&b.ID, &b.Title, &b.Version, &b.StateJSON, &b.UpdatedAt, &b.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("boards.Get: %w", err)
	}
	return b, nil
}

// SchleuseGesperrt liest die Board-Sperre der Ausgangsschleuse (ADR-0134 §2).
func (r *BoardRepo) SchleuseGesperrt(ctx context.Context, id string) (bool, error) {
	return SchleuseBoardSperre(ctx, r.db, id)
}

// SetzeSchleuseSperre stellt die Board-Sperre (mit Audit). Die Rückspielung
// als neues Board übernimmt damit die Sperre des gesicherten Boards (F-1583);
// Insert kennt die Spalte bewusst nicht.
func (r *BoardRepo) SetzeSchleuseSperre(ctx context.Context, id string, gesperrt bool, userID string) error {
	return SetzeSchleuseBoardSperre(ctx, r.db, id, gesperrt, userID)
}

// Existiert meldet, ob es ein Board mit dieser id gibt — ohne den State zu
// lesen und ohne Schreibsperre (WAL: Leser warten nicht auf Schreiber). Der
// Ops-Endpunkt fragt das VOR BEGIN IMMEDIATE, damit ein Batch auf ein
// fehlendes Board die datenbankweite Sperre gar nicht erst nimmt
// (Session-Kritiker nach Sprint 73, F-1186).
func (r *BoardRepo) Existiert(ctx context.Context, id string) (bool, error) {
	var eins int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, id).Scan(&eins)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("boards.Existiert: %w", err)
	}
	return true, nil
}

// Insert legt ein neues Board an. ID + Title + Version müssen gesetzt sein;
// updated_at + created_at werden serverseitig gesetzt.
func (r *BoardRepo) Insert(ctx context.Context, id, title, version string, stateJSON []byte) (*Board, error) {
	if id == "" || title == "" || version == "" {
		return nil, fmt.Errorf("boards.Insert: id/title/version dürfen nicht leer sein")
	}
	if len(stateJSON) == 0 {
		return nil, fmt.Errorf("boards.Insert: state_json darf nicht leer sein")
	}
	/* ADR-0141 §5.1 (Karte #826): Import, Restore als neues Board und „Kopie
	   als neues Board“ legen nie eine Kopie an — die Marken `spiegel` fallen
	   weg (eine Kopie entsteht nur über KopieAnlegen). */
	stateJSON = schatten.OhneSpiegelMarken(stateJSON)
	ts := nowMillis()
	const q = `INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
	            VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q, id, title, version, stateJSON, ts, ts); err != nil {
		return nil, fmt.Errorf("boards.Insert: %w", err)
	}
	return &Board{
		ID: id, Title: title, Version: version,
		StateJSON: stateJSON, UpdatedAt: ts, CreatedAt: ts,
	}, nil
}

// Update führt einen Optimistic-Concurrency-geschützten Update durch.
//
// ifMatch == 0:  keine If-Match-Prüfung (Force-Update, in Phase 1 nicht
//
//	vom API-Handler genutzt — Reserviert für Migrationen/CLI).
//
// ifMatch != 0:  Update gelingt nur, wenn das aktuelle updated_at gleich ist;
//
//	bei Mismatch wird ErrConflict zurückgegeben (das aktuelle
//	Board ist über *Board zurückzulesen via Get()).
func (r *BoardRepo) Update(ctx context.Context, id, title, version string, stateJSON []byte, ifMatch int64) (*Board, error) {
	if id == "" || title == "" || version == "" {
		return nil, fmt.Errorf("boards.Update: id/title/version dürfen nicht leer sein")
	}
	if len(stateJSON) == 0 {
		return nil, fmt.Errorf("boards.Update: state_json darf nicht leer sein")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("boards.Update begin: %w", err)
	}
	defer tx.Rollback()

	var currentTs int64
	var createdAt int64
	var currentJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT updated_at, created_at, state_json FROM boards WHERE id = ?`, id).
		Scan(&currentTs, &createdAt, &currentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("boards.Update select: %w", err)
	}

	if ifMatch != 0 && ifMatch != currentTs {
		return nil, ErrConflict
	}

	/* F-1544: board.nextFindingNr nie absenken — sonst kehrt die Nummer eines
	   gelöschten höchsten Findings zurück (finding_zaehler.go). */
	stateJSON, err = findingZaehlerBewahren(currentJSON, stateJSON, !findingNummernBehalten(ctx))
	if err != nil {
		return nil, fmt.Errorf("boards.Update: %w", err)
	}
	/* ADR-0140 §10 (Karte #778): „vertraulich“ schaltet kein Ganz-Board-
	   Schreiben ab, auch kein Restore (vertraulich_bewahren.go). */
	stateJSON, bewahrt, err := vertraulichBewahren(currentJSON, stateJSON)
	if err != nil {
		return nil, fmt.Errorf("boards.Update: %w", err)
	}
	/* ADR-0141 §7 (Karte #826): Spiegel einer externen Kopie sind bis 2c nur
	   lesbar — auch für den Ganz-PUT und das Restore (spiegel_schranke.go). */
	if err := spiegelSchranke(ctx, tx, id, currentJSON, stateJSON); err != nil {
		return nil, err
	}

	/* updated_at strikt > currentTs — verhindert ETag-Stagnation bei schnellen Updates */
	ts := max(nowMillis(), currentTs+1)

	const q = `UPDATE boards SET title = ?, version = ?, state_json = ?, updated_at = ?
	            WHERE id = ?`
	if _, err := tx.ExecContext(ctx, q, title, version, stateJSON, ts, id); err != nil {
		return nil, fmt.Errorf("boards.Update exec: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("boards.Update commit: %w", err)
	}

	return &Board{
		ID: id, Title: title, Version: version,
		StateJSON: stateJSON, UpdatedAt: ts, CreatedAt: createdAt,
		VertraulichBewahrt: bewahrt,
	}, nil
}

// OffeneAenderungenError: Delete hat nicht gelöscht, weil das Board
// unbestätigte Journal-Einträge trägt (Kritiker Sprint 97 K-1).
type OffeneAenderungenError struct{ Anzahl int }

func (e *OffeneAenderungenError) Error() string {
	return fmt.Sprintf("boards.Delete: %d offene Änderungen im Journal — nur über „Kopie außen löschen“", e.Anzahl)
}

// Delete löscht das Board. Attachments werden per FK ON DELETE CASCADE
// mitgelöscht; das attachments-Volume muss separat aufgeräumt werden
// (Phase 1: durch #74-Attachment-Endpoint vorgegeben, hier nicht relevant).
//
// Aufträge der Ausgangsschleuse (schleuse_auftraege, ADR-0134 §3–§4) gehen
// in DERSELBEN Transaktion mit (Kritiker KS-6): Sie haben keinen
// Fremdschlüssel auf boards und hielten sonst Auszug und verschlüsselte
// Zuordnung eines Boards fest, das es nicht mehr gibt. Erst das Board, dann
// die Aufträge — die Schreibsperre der Transaktion hält eine gleichzeitige
// Auftragsanlage auf, und SchleuseAuftragRepo.AnlegenGeprueft legt für ein
// gelöschtes Board keine Zeile mehr an. Keine Frist-Löschung (kein uhr-Gate):
// gelöscht wird per ID, weil das Board weg ist.
//
// Kritiker Sprint 97 K-1: Trägt das Board unbestätigte Journal-Einträge (eine
// Kopie außen mit Änderungen, die das Skript noch nicht abgeholt hat), löscht
// Delete nicht, sondern liefert *OffeneAenderungenError — in derselben
// Transaktion gezählt. Gelöscht wird so ein Board nur über den Admin-Notweg
// „Kopie außen löschen“ (KopieLoeschen), der die Zahl vorher nennt (F-2240).
func (r *BoardRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("boards.Delete begin: %w", err)
	}
	defer tx.Rollback()
	// Prüfen und Löschen in EINEM Statement (Sitzungs-QA 20.10.): erst zählen,
	// dann löschen scheitert in einer DEFERRED-Transaktion unter WAL sofort mit
	// „database is locked“, sobald dazwischen ein anderer Schreiber committet
	// (knowledge/topics/nebenlaeufigkeit-und-lesefenster.md §50). Ein
	// schreibendes Statement nimmt die Sperre am Anfang und wartet über
	// busy_timeout; danach hält die Transaktion sie, und das Deuten von „0
	// Zeilen“ liest einen Stand, den niemand mehr ändert.
	res, err := tx.ExecContext(ctx, `DELETE FROM boards WHERE id = ? AND NOT EXISTS
		(SELECT 1 FROM boardpaar_journal j WHERE j.board_id = ? AND j.bestaetigt_at IS NULL)`, id, id)
	if err != nil {
		return fmt.Errorf("boards.Delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("boards.Delete affected: %w", err)
	}
	if n == 0 {
		var offen int
		if err := tx.QueryRowContext(ctx, "SELECT "+fmt.Sprintf(offeneAenderungenSQL, "?"), id).Scan(&offen); err != nil {
			return fmt.Errorf("boards.Delete offene aenderungen: %w", err)
		}
		if offen > 0 {
			return &OffeneAenderungenError{Anzahl: offen}
		}
		return ErrBoardNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schleuse_auftraege WHERE board_id = ?`, id); err != nil {
		return fmt.Errorf("boards.Delete schleuse_auftraege: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("boards.Delete commit: %w", err)
	}
	return nil
}
