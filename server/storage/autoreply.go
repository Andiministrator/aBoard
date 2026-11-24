package storage

// autoreply.go — Lifetime-Cap-Tabelle für Mail-Auto-Reply (#297).
//
// AutoReplyLogRepo kapselt mail_autoreply_log: pro (sender_email, card_id) wird
// genau eine Auto-Reply protokolliert. Doppelter Insert wird via UNIQUE-Constraint
// lautlos absorbiert — rowsAffected==0 signalisiert dem Caller, dass bereits
// gesendet wurde.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AutoReplyLogRepo verwaltet mail_autoreply_log (Lifetime-Cap).
type AutoReplyLogRepo struct {
	db *sql.DB
}

// NewAutoReplyLogRepo legt ein Repo an.
func NewAutoReplyLogRepo(db *sql.DB) *AutoReplyLogRepo {
	return &AutoReplyLogRepo{db: db}
}

// TryRecord versucht einen neuen Log-Eintrag für (senderEmail, cardID) zu schreiben.
// Liefert true, wenn der Eintrag NEU angelegt wurde (Auto-Reply darf gesendet werden).
// Liefert false, wenn bereits ein Eintrag existiert (UNIQUE-Konflikt, kein Send).
//
// rowsAffected==0 bei DO NOTHING → kein zweiter Send möglich, auch nicht nach
// Neustart des Servers (persistente Tabelle).
func (r *AutoReplyLogRepo) TryRecord(ctx context.Context, senderEmail, cardID string) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO mail_autoreply_log (sender_email, card_id, sent_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(sender_email, card_id) DO NOTHING`,
		senderEmail, cardID, now)
	if err != nil {
		return false, fmt.Errorf("autoreply-log.try-record: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
