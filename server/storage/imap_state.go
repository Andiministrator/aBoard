package storage

// imap_state.go — Worker-State für den IMAP-Mail-Ingest (#295, ADR-0093).
//
// Zwei Sub-Repos, beide ohne Verschlüsselung (kein Klartext-Secret hier — nur
// Mail-Bookkeeping: UIDs und Message-IDs):
//   - ImapStateRepo: pro Folder last_uid + uid_validity + last_poll_at.
//     Bei UIDVALIDITY-Wechsel wird last_uid auf 0 zurückgesetzt (Provider-
//     Migration / Re-Mount der Mailbox).
//   - ImapProcessedRepo: Set verarbeiteter Message-IDs, schützt gegen
//     Doppelverarbeitung im MOVE-Race. Eviction > 30 Tage in der Poll-Schleife
//     des Workers, täglich und nur mit gestellter Uhr (F-1192).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// --- Folder-State ---------------------------------------------------------

// ImapFolderState bündelt die letzte gesehene UID + UIDVALIDITY + Poll-Zeit.
type ImapFolderState struct {
	Folder      string
	LastUID     uint32
	UIDValidity uint32
	LastPollAt  string // ISO-8601, leer wenn noch nie gepollt
}

// ImapStateRepo verwaltet mail_imap_state (eine Zeile pro Folder).
type ImapStateRepo struct {
	db *sql.DB
}

// NewImapStateRepo legt ein Repo an.
func NewImapStateRepo(db *sql.DB) *ImapStateRepo {
	return &ImapStateRepo{db: db}
}

// Get liefert den State eines Folders. Existiert noch keiner, kommt ein
// leerer State (LastUID=0, UIDValidity=0, LastPollAt="") und kein Fehler —
// der Worker behandelt den Erststart genau so wie nach UIDVALIDITY-Reset.
func (r *ImapStateRepo) Get(ctx context.Context, folder string) (ImapFolderState, error) {
	var s ImapFolderState
	s.Folder = folder
	var lastPoll sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT last_uid, uid_validity, last_poll_at
		 FROM mail_imap_state WHERE folder = ?`, folder).Scan(&s.LastUID, &s.UIDValidity, &lastPoll)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("imap-state.get: %w", err)
	}
	if lastPoll.Valid {
		s.LastPollAt = lastPoll.String
	}
	return s, nil
}

// Set persistiert den State. Idempotent via UPSERT.
func (r *ImapStateRepo) Set(ctx context.Context, s ImapFolderState) error {
	if s.Folder == "" {
		return fmt.Errorf("imap-state.set: folder leer")
	}
	now := timeNow().UTC().Format(time.RFC3339Nano)
	if s.LastPollAt == "" {
		s.LastPollAt = now
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO mail_imap_state (folder, last_uid, uid_validity, last_poll_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   last_uid = excluded.last_uid,
		   uid_validity = excluded.uid_validity,
		   last_poll_at = excluded.last_poll_at`,
		s.Folder, s.LastUID, s.UIDValidity, s.LastPollAt)
	if err != nil {
		return fmt.Errorf("imap-state.set: %w", err)
	}
	return nil
}

// ResetUID setzt last_uid eines Folders auf 0 — für UIDVALIDITY-Wechsel.
// uid_validity wird mit dem neuen Wert überschrieben. last_poll_at bleibt.
func (r *ImapStateRepo) ResetUID(ctx context.Context, folder string, newUIDValidity uint32) error {
	now := timeNow().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO mail_imap_state (folder, last_uid, uid_validity, last_poll_at)
		 VALUES (?, 0, ?, ?)
		 ON CONFLICT(folder) DO UPDATE SET
		   last_uid = 0,
		   uid_validity = excluded.uid_validity,
		   last_poll_at = excluded.last_poll_at`,
		folder, newUIDValidity, now)
	if err != nil {
		return fmt.Errorf("imap-state.reset-uid: %w", err)
	}
	return nil
}

// --- Processed-Message-IDs ------------------------------------------------

// ImapProcessedRepo verwaltet mail_imap_processed (idempotenz-set).
type ImapProcessedRepo struct {
	db *sql.DB
}

// NewImapProcessedRepo legt ein Repo an.
func NewImapProcessedRepo(db *sql.DB) *ImapProcessedRepo {
	return &ImapProcessedRepo{db: db}
}

// Seen prüft, ob eine Message-ID schon verarbeitet wurde.
// messageID wird wörtlich (inkl. <>) verwendet — Aufrufer normalisiert nicht.
func (r *ImapProcessedRepo) Seen(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		// Mail ohne Message-ID — kann nicht idempotent erkannt werden.
		// Worker behandelt das per anderem Pfad (UID + Mail-Datum + Sender);
		// hier liefern wir "nicht gesehen" und überlassen die Entscheidung dem
		// Worker.
		return false, nil
	}
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM mail_imap_processed WHERE message_id = ?`, messageID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("imap-processed.seen: %w", err)
	}
	return true, nil
}

// MarkProcessed fügt eine Message-ID hinzu. Idempotent — doppeltes Insert ist
// kein Fehler (UPSERT).
func (r *ImapProcessedRepo) MarkProcessed(ctx context.Context, messageID string) error {
	if messageID == "" {
		return nil // leere Message-ID nicht persistieren
	}
	now := timeNow().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO mail_imap_processed (message_id, processed_at)
		 VALUES (?, ?)
		 ON CONFLICT(message_id) DO NOTHING`,
		messageID, now)
	if err != nil {
		return fmt.Errorf("imap-processed.mark: %w", err)
	}
	return nil
}

// EvictOlderThan löscht Einträge älter als die angegebene Dauer.
// Uhr über `timeNow` (users.go), wie NotificationLogRepo.EvictOlderThan — sonst
// lässt sich die Grenze im Test nicht fixieren (Nachzug zu F-686c, Sprint 69).
// Vom Worker mit 30*24h aufgerufen (ADR-0093 §12), seit F-1192 in der
// Poll-Schleife (mail.evictWennFaellig) statt beim Start.
func (r *ImapProcessedRepo) EvictOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := timeNow().UTC().Add(-age).Format(time.RFC3339Nano)
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM mail_imap_processed WHERE processed_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("imap-processed.evict: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
