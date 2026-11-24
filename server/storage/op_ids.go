package storage

// op_ids.go — Idempotenz am Ops-Endpunkt (ADR-0130 §5, Karte #607).
//
// Gelesen und geschrieben wird INNERHALB der Schreibsperre von ApplyOps
// (bekannteOpIDs/merkeOpIDs auf derselben Verbindung), damit „schon
// angenommen?“ und „jetzt angenommen“ nicht auseinanderfallen können. Nur das
// Aufräumen nach Frist läuft eigenständig (retention, Uhr-Gate F-1192).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// OpIDFristTage — so lange bleibt eine Client-id bekannt. Eine Wiederholung
// kommt in Sekunden bis Stunden (Ausgang nach Netzausfall, ADR-0130 §5);
// 30 Tage decken auch einen Rechner, der eine Weile offline war.
const OpIDFristTage = 30

// MaxClientIDLaenge begrenzt, was als Idempotenz-Schlüssel angenommen wird.
const MaxClientIDLaenge = 128

// OpSchluessel ist der Idempotenz-Schlüssel einer Op: die Client-id und ein
// Hash über Akteur, Typ und Nutzlast (K-3, Akteur seit F-1260). Leere ID = Op
// ohne Schlüssel.
type OpSchluessel struct {
	ID   string
	Hash string
}

// IDWiederverwendetError: Eine bekannte Client-id kam mit anderem Inhalt.
type IDWiederverwendetError struct{ ID string }

func (e *IDWiederverwendetError) Error() string {
	return "storage: client-id " + e.ID + " wurde schon für eine andere Op verwendet"
}

// OpIDRepo räumt die Tabelle op_ids nach Frist auf.
type OpIDRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewOpIDRepo legt das Repo an.
func NewOpIDRepo(db *sql.DB) *OpIDRepo {
	return &OpIDRepo{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// EvictExpired löscht Client-ids, die älter als OpIDFristTage sind. Nach
// Alter — der Aufrufer (retention.Scheduler.RunOnce) prüft vorher die Uhr.
func (r *OpIDRepo) EvictExpired(ctx context.Context) (int64, error) {
	grenze := r.now().AddDate(0, 0, -OpIDFristTage).Format(activityTSFormat)
	res, err := r.db.ExecContext(ctx, `DELETE FROM op_ids WHERE ts < ?`, grenze)
	if err != nil {
		return 0, fmt.Errorf("op_ids.EvictExpired: %w", err)
	}
	return res.RowsAffected()
}

// OpIDHash: der gespeicherte Hash einer schon angenommenen Client-id (ok =
// angenommen). Für den Rückweg des Board-Paars (ADR-0141 §5.3): Ist
// „<Op-ID>“ bzw. „<Op-ID>-k“ gemerkt, entscheidet der Hash zwischen
// Wiederholung und wiederverwendeter Op-ID. Liest ohne Sperre.
func (r *BoardRepo) OpIDHash(ctx context.Context, boardID, clientID string) (string, bool, error) {
	m, err := bekannteOpIDs(ctx, r.db, boardID, []string{clientID})
	if err != nil {
		return "", false, err
	}
	h, ok := m[clientID]
	return h, ok, nil
}

// bekannteOpIDs liefert zu den ids, die für das Board schon angenommen
// wurden, den gespeicherten Hash.
func bekannteOpIDs(ctx context.Context, ex interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, boardID string, ids []string) (map[string]string, error) {
	bekannt := map[string]string{}
	for start := 0; start < len(ids); start += 200 {
		ende := min(start+200, len(ids))
		teil := ids[start:ende]
		args := make([]any, 0, len(teil)+1)
		args = append(args, boardID)
		for _, id := range teil {
			args = append(args, id)
		}
		q := `SELECT client_id, hash FROM op_ids WHERE board_id = ? AND client_id IN (?` + strings.Repeat(",?", len(teil)-1) + `)`
		rows, err := ex.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("op_ids: lesen: %w", err)
		}
		for rows.Next() {
			var id, hash string
			if err := rows.Scan(&id, &hash); err != nil {
				rows.Close()
				return nil, fmt.Errorf("op_ids: scan: %w", err)
			}
			bekannt[id] = hash
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return bekannt, nil
}

// merkeOpIDs trägt die angenommenen ids ein. INSERT OR IGNORE: eine id, die
// zweimal im selben Batch steht, ist kein Fehler.
func merkeOpIDs(ctx context.Context, ex Ausfuehrer, boardID string, schluessel []OpSchluessel, jetzt time.Time) error {
	ts := jetzt.UTC().Format(activityTSFormat)
	for _, k := range schluessel {
		if _, err := ex.ExecContext(ctx, `INSERT OR IGNORE INTO op_ids (board_id, client_id, hash, ts) VALUES (?, ?, ?, ?)`, boardID, k.ID, k.Hash, ts); err != nil {
			return fmt.Errorf("op_ids: schreiben: %w", err)
		}
	}
	return nil
}
