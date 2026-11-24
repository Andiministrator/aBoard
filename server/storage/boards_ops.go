package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrOpsAbgelehnt markiert einen Fehler der Falt-Funktion, den der Aufrufer
// ausdrücklich als Ablehnung gemeint hat (z. B. „Batch wirkt nicht wie
// beabsichtigt"). ApplyOps reicht ihn unverändert durch; nichts ist
// geschrieben. Andere Fehler der Falt-Funktion sind Serverfehler.
var ErrOpsAbgelehnt = errors.New("storage: ops abgelehnt")

// ErrHuelleVerletzt: Das gefaltete Board verletzt die Hülle (id, title,
// version). Ursache ist der Batch des Clients (z. B. board.field title ""),
// kein Serverfehler — der Aufrufer antwortet mit 4xx (Kritiker Sprint 71, K-A7).
var ErrHuelleVerletzt = errors.New("storage: gefaltetes board verletzt die hülle")

// Ausfuehrer ist, was ein Schritt im Schreibvorgang von ApplyOps bekommt: die
// Verbindung, auf der die Transaktion läuft. Mehr als Ausführen darf er nicht
// — kein COMMIT, kein ROLLBACK, keine eigene Transaktion.
type Ausfuehrer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ErrDBBelegt: Die Schreibsperre war innerhalb von busy_timeout nicht zu
// bekommen. Vorübergehend — der Aufrufer antwortet mit 503.
var ErrDBBelegt = errors.New("storage: datenbank belegt")

// ApplyOps faltet einen Op-Batch unter Schreibsperre auf den Board-Blob
// (ADR-0129 §4, K-5; Karte #552).
//
// Ablauf auf EINER gepinnten Verbindung: BEGIN IMMEDIATE → Blob lesen →
// falte(blob) → neuen Blob schreiben (updated_at strikt grösser) → COMMIT.
// BEGIN IMMEDIATE nimmt die Schreibsperre für die ganze Datenbank VOR dem
// Lesen. Damit sieht jeder Batch den Stand seines Vorgängers, und die
// displayId-Vergabe in fold() (nextDisplayId) bleibt eindeutig, auch wenn
// mehrere Schreiber gleichzeitig Karten anlegen. Ein optimistisches Retry
// gibt es bewusst nicht. BEGIN IMMEDIATE lässt sich über sql.TxOptions nicht
// ausdrücken, deshalb die Conn (Muster: BootstrapNewUser, TransferCard).
//
// falte darf nichts ausserhalb der Transaktion bewirken; ihre Laufzeit ist
// Sperrzeit für alle Schreiber. Liefert sie einen Fehler, wird
// zurückgerollt. Der neue Blob muss dieselbe board.id tragen; title und
// version werden aus ihm übernommen (board.field kann den Titel ändern).
//
// imSchreibvorgang (optional, nil = keiner) läuft nach dem UPDATE und vor dem
// COMMIT auf derselben Verbindung. Dort schreibt der Ops-Endpunkt das
// Protokoll je Op (F-1156): Board und Protokoll werden gemeinsam wirksam oder
// gar nicht. Liefert er einen Fehler, wird zurückgerollt — dann ist auch das
// Board nicht geschrieben.
func (r *BoardRepo) ApplyOps(ctx context.Context, id string, falte func(stateJSON []byte) ([]byte, error), imSchreibvorgang func(ctx context.Context, ex Ausfuehrer, b *Board) error) (*Board, error) {
	return r.ApplyOpsIdempotent(ctx, id, nil,
		func(state []byte, _ map[string]bool) ([]byte, error) { return falte(state) }, imSchreibvorgang)
}

// ErrAllesBekannt: Jede Op des Batches trug eine Client-id, die der Server
// schon angenommen hat — eine Wiederholung. Nichts geschrieben; der Aufrufer
// quittiert mit 200 (ADR-0130 §5, Karte #607).
var ErrAllesBekannt = errors.New("storage: alle Ops bereits angenommen")

// ApplyOpsIdempotent ist ApplyOps mit Idempotenz-Schlüsseln (ADR-0130 §5,
// Karte #607). clientIDs sind die Client-ids der Ops (leere Einträge = ohne
// Schlüssel). Unter derselben Schreibsperre wird ermittelt, welche davon
// schon angenommen sind; falte bekommt diese Menge und faltet nur den Rest.
// Gibt falte (nil, nil) zurück, gab es nichts Neues: zurückgerollt,
// ErrAllesBekannt. Nach dem UPDATE werden die übrigen ids eingetragen — in
// derselben Transaktion, damit „angenommen“ und „gemerkt“ nicht
// auseinanderfallen (Lehre §36: nichts Wesentliches nach dem COMMIT).
func (r *BoardRepo) ApplyOpsIdempotent(ctx context.Context, id string, schluessel []OpSchluessel, falte func(stateJSON []byte, bekannt map[string]bool) ([]byte, error), imSchreibvorgang func(ctx context.Context, ex Ausfuehrer, b *Board) error) (*Board, error) {
	if id == "" {
		return nil, fmt.Errorf("boards.ApplyOps: id darf nicht leer sein")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("boards.ApplyOps: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Eigener Kontext: Ist ctx abgelaufen, muss das ROLLBACK trotzdem
			// laufen, sonst bliebe die Verbindung mit offener Sperre im Pool.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var state []byte
	var currentTs, createdAt int64
	err = conn.QueryRowContext(ctx, `SELECT state_json, updated_at, created_at FROM boards WHERE id = ?`, id).
		Scan(&state, &currentTs, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: lesen: %w", err)
	}

	var mitID []OpSchluessel
	var ids []string
	for _, k := range schluessel {
		if k.ID != "" {
			mitID = append(mitID, k)
			ids = append(ids, k.ID)
		}
	}
	bekannt := map[string]bool{}
	if len(ids) > 0 {
		gespeichert, err := bekannteOpIDs(ctx, conn, id, ids)
		if err != nil {
			return nil, fmt.Errorf("boards.ApplyOps: %w", err)
		}
		// Sitzungs-QA K-3: Eine bekannte id mit anderem Inhalt ist kein
		// Wiederholen. Ohne diese Prüfung quittierte der Server die zweite
		// Op still als „schon angenommen“ und verwarf ihren Inhalt.
		for _, k := range mitID {
			if h, ok := gespeichert[k.ID]; ok {
				if h != "" && k.Hash != "" && h != k.Hash {
					return nil, &IDWiederverwendetError{ID: k.ID}
				}
				bekannt[k.ID] = true
			}
		}
	}

	neu, err := falte(state, bekannt)
	if err != nil {
		return nil, err
	}
	if neu == nil {
		return nil, ErrAllesBekannt
	}

	var env struct {
		Version string `json:"version"`
		Board   struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"board"`
	}
	if err := json.Unmarshal(neu, &env); err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: gefalteter Blob ist kein JSON: %w", err)
	}
	if env.Board.ID != id || env.Board.Title == "" || env.Version == "" {
		return nil, fmt.Errorf("%w (id=%q, title leer=%v, version leer=%v)",
			ErrHuelleVerletzt, env.Board.ID, env.Board.Title == "", env.Version == "")
	}

	ts := max(nowMillis(), currentTs+1)
	if _, err := conn.ExecContext(ctx,
		`UPDATE boards SET title = ?, version = ?, state_json = ?, updated_at = ? WHERE id = ?`,
		env.Board.Title, env.Version, neu, ts, id); err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: schreiben: %w", err)
	}
	b := &Board{
		ID: id, Title: env.Board.Title, Version: env.Version,
		StateJSON: neu, UpdatedAt: ts, CreatedAt: createdAt,
	}
	var neueIDs []OpSchluessel
	for _, k := range mitID {
		if !bekannt[k.ID] {
			neueIDs = append(neueIDs, k)
		}
	}
	if err := merkeOpIDs(ctx, conn, id, neueIDs, time.UnixMilli(ts)); err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: %w", err)
	}
	if imSchreibvorgang != nil {
		if err := imSchreibvorgang(ctx, conn, b); err != nil {
			return nil, fmt.Errorf("boards.ApplyOps: im Schreibvorgang: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("boards.ApplyOps: commit: %w", err)
	}
	committed = true
	return b, nil
}
