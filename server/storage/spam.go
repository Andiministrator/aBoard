package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// spam.go — Server-side Mail-Spam-Blocklist + Drop-Counter (#294, ADR-0092).
//
// Wird vom Admin-API-Endpoint POST /mail-spam-report (api/spam.go) für den
// User-getriebenen Spam-Workflow geschrieben und vom IMAP-Mail-Ingest (#295)
// beim Polling gelesen. Beide Pfade laufen serverseitig.

// SpamRepo bündelt Lese-/Schreibzugriffe auf mail_spam_senders + mail_spam_counter.
type SpamRepo struct {
	db *sql.DB
}

func NewSpamRepo(db *sql.DB) *SpamRepo {
	return &SpamRepo{db: db}
}

// SpamSenderListed prüft, ob eine Email-Adresse auf der Blocklist steht.
// Email wird normalisiert (gleiche Form wie Add/Remove). Auf jedem IMAP-
// Polling-Tick (#295) eine Round-trip pro Sender — billig, da PK-Lookup.
func (r *SpamRepo) SpamSenderListed(ctx context.Context, email string) (bool, error) {
	email = NormalizeEmail(email)
	if email == "" {
		return false, nil
	}
	const q = `SELECT 1 FROM mail_spam_senders WHERE sender_email = ?`
	var one int
	err := r.db.QueryRowContext(ctx, q, email).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("spam.Listed: %w", err)
	}
	return true, nil
}

// AddSpamSender hängt einen Sender an die Blocklist (Upsert — schon gelistet
// ist kein Fehler, idempotent). addedBy darf leer sein (System-Bootstrap;
// Admin-Pfad reicht user_id).
func (r *SpamRepo) AddSpamSender(ctx context.Context, email, addedBy string) error {
	email = NormalizeEmail(email)
	if email == "" {
		return fmt.Errorf("spam.Add: sender_email darf nicht leer sein")
	}
	const q = `INSERT INTO mail_spam_senders (sender_email, added_at, added_by)
	            VALUES (?, ?, ?)
	            ON CONFLICT(sender_email) DO UPDATE SET added_at = excluded.added_at, added_by = excluded.added_by`
	var addedByCol sql.NullString
	if addedBy != "" {
		addedByCol = sql.NullString{String: addedBy, Valid: true}
	}
	if _, err := r.db.ExecContext(ctx, q, email, nowISO(), addedByCol); err != nil {
		return fmt.Errorf("spam.Add: %w", err)
	}
	return nil
}

// RemoveSpamSender entfernt einen Sender von der Blocklist. Idempotent —
// nicht-existenter Eintrag ist kein Fehler (Zielzustand "nicht gelistet"
// erreicht). Symmetrisch zum acl.Delete-Pattern.
func (r *SpamRepo) RemoveSpamSender(ctx context.Context, email string) error {
	email = NormalizeEmail(email)
	if email == "" {
		return fmt.Errorf("spam.Remove: sender_email darf nicht leer sein")
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM mail_spam_senders WHERE sender_email = ?`, email); err != nil {
		return fmt.Errorf("spam.Remove: %w", err)
	}
	return nil
}

// ListSpamSenders liefert die normalisierten Blocklist-Sender, sortiert
// alphabetisch. Für Admin-UI (#306 später) — im MVP nicht über die HTTP-API
// freigegeben (POST-only Mutation, kein GET).
func (r *SpamRepo) ListSpamSenders(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT sender_email FROM mail_spam_senders ORDER BY sender_email`)
	if err != nil {
		return nil, fmt.Errorf("spam.List: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, fmt.Errorf("spam.List scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// IncSpamCounter erhöht den Drop-Counter um 1. Wird vom IMAP-Mail-Ingest
// (#295) genutzt, wenn MAIL_SPAM_DROP=true UND der Sender auf der Blocklist
// steht — die Mail wird dann gar nicht zur Karte, nur diese Metrik bewegt
// sich.
func (r *SpamRepo) IncSpamCounter(ctx context.Context) error {
	const q = `UPDATE mail_spam_counter SET dropped_total = dropped_total + 1, updated_at = ? WHERE id = 1`
	if _, err := r.db.ExecContext(ctx, q, nowISO()); err != nil {
		return fmt.Errorf("spam.IncCounter: %w", err)
	}
	return nil
}

// SpamCounter liest den aktuellen Drop-Counter-Stand. Für Admin-UI / Status-
// Endpoint (#306).
func (r *SpamRepo) SpamCounter(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `SELECT dropped_total FROM mail_spam_counter WHERE id = 1`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("spam.Counter: %w", err)
	}
	return n, nil
}
