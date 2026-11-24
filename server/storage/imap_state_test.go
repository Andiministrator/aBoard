package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newImapTestRepos(t *testing.T) (*ImapStateRepo, *ImapProcessedRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return NewImapStateRepo(db), NewImapProcessedRepo(db), func() { db.Close() }
}

func TestImapState_GetEmpty(t *testing.T) {
	state, _, done := newImapTestRepos(t)
	defer done()
	s, err := state.Get(context.Background(), "INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.LastUID != 0 || s.UIDValidity != 0 || s.LastPollAt != "" {
		t.Errorf("Empty-State erwartet, got %+v", s)
	}
}

func TestImapState_SetAndGet(t *testing.T) {
	state, _, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	in := ImapFolderState{Folder: "INBOX", LastUID: 42, UIDValidity: 1234, LastPollAt: "2026-05-21T12:00:00Z"}
	if err := state.Set(ctx, in); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := state.Get(ctx, "INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastUID != in.LastUID || got.UIDValidity != in.UIDValidity || got.LastPollAt != in.LastPollAt {
		t.Errorf("Mismatch: got %+v, want %+v", got, in)
	}
}

func TestImapState_Upsert(t *testing.T) {
	state, _, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	_ = state.Set(ctx, ImapFolderState{Folder: "INBOX", LastUID: 1, UIDValidity: 100, LastPollAt: "2026-05-21T10:00:00Z"})
	_ = state.Set(ctx, ImapFolderState{Folder: "INBOX", LastUID: 5, UIDValidity: 100, LastPollAt: "2026-05-21T11:00:00Z"})
	got, _ := state.Get(ctx, "INBOX")
	if got.LastUID != 5 {
		t.Errorf("Upsert: LastUID=%d, erwartet 5", got.LastUID)
	}
}

func TestImapState_ResetUID(t *testing.T) {
	state, _, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	_ = state.Set(ctx, ImapFolderState{Folder: "INBOX", LastUID: 42, UIDValidity: 100, LastPollAt: "2026-05-21T10:00:00Z"})
	if err := state.ResetUID(ctx, "INBOX", 999); err != nil {
		t.Fatalf("ResetUID: %v", err)
	}
	got, _ := state.Get(ctx, "INBOX")
	if got.LastUID != 0 {
		t.Errorf("ResetUID: LastUID=%d, erwartet 0", got.LastUID)
	}
	if got.UIDValidity != 999 {
		t.Errorf("ResetUID: UIDValidity=%d, erwartet 999", got.UIDValidity)
	}
}

func TestImapProcessed_SeenMark(t *testing.T) {
	_, proc, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	msgID := "<abc123@example.org>"
	ok, _ := proc.Seen(ctx, msgID)
	if ok {
		t.Errorf("vor Mark sollte Seen()=false sein")
	}
	if err := proc.MarkProcessed(ctx, msgID); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	ok, _ = proc.Seen(ctx, msgID)
	if !ok {
		t.Errorf("nach Mark sollte Seen()=true sein")
	}
}

func TestImapProcessed_MarkIdempotent(t *testing.T) {
	_, proc, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	msgID := "<dup@example.org>"
	if err := proc.MarkProcessed(ctx, msgID); err != nil {
		t.Fatalf("Mark 1: %v", err)
	}
	// Zweiter Aufruf darf kein Fehler werfen (ON CONFLICT DO NOTHING).
	if err := proc.MarkProcessed(ctx, msgID); err != nil {
		t.Fatalf("Mark 2 (idempotent): %v", err)
	}
}

func TestImapProcessed_MarkEmptyMessageIDIsNoop(t *testing.T) {
	_, proc, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	if err := proc.MarkProcessed(context.Background(), ""); err != nil {
		t.Fatalf("MarkProcessed(\"\"): %v", err)
	}
	ok, _ := proc.Seen(ctx, "")
	if ok {
		t.Errorf("Seen(\"\") sollte false sein")
	}
}

func TestImapProcessed_EvictOlderThan(t *testing.T) {
	_, proc, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()
	// 3 Einträge anlegen, einen davon "alt" backdating
	_ = proc.MarkProcessed(ctx, "<a@x>")
	_ = proc.MarkProcessed(ctx, "<b@x>")
	_ = proc.MarkProcessed(ctx, "<c@x>")
	// b@x backdate über das Eviction-Fenster
	oldTs := time.Now().UTC().Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := proc.db.ExecContext(ctx, `UPDATE mail_imap_processed SET processed_at = ? WHERE message_id = ?`, oldTs, "<b@x>"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	n, err := proc.EvictOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if n != 1 {
		t.Errorf("Evict-Anzahl: %d, erwartet 1", n)
	}
	ok, _ := proc.Seen(ctx, "<b@x>")
	if ok {
		t.Errorf("<b@x> sollte nach Evict weg sein")
	}
	ok, _ = proc.Seen(ctx, "<a@x>")
	if !ok {
		t.Errorf("<a@x> sollte erhalten bleiben")
	}
}

// Nachzug zu F-686c (Sprint 69): EvictOlderThan muss dieselbe fixierbare Uhr
// nutzen wie NotificationLogRepo. Der Eintrag liegt vor der fixierten Uhr
// (2200), aber nach der echten Systemzeit — mit time.Now() bliebe er stehen.
func TestImapProcessed_EvictOlderThan_NutztFixierteUhr(t *testing.T) {
	_, proc, done := newImapTestRepos(t)
	defer done()
	ctx := context.Background()

	orig := timeNow
	timeNow = func() time.Time { return time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC) }
	defer func() { timeNow = orig }()

	if err := proc.MarkProcessed(ctx, "<alt@example.org>"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	altTs := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := proc.db.ExecContext(ctx,
		`UPDATE mail_imap_processed SET processed_at = ? WHERE message_id = ?`, altTs, "<alt@example.org>"); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	n, err := proc.EvictOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("EvictOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("Evict-Anzahl = %d, erwartet 1 — EvictOlderThan nutzt nicht die fixierte Uhr", n)
	}
}
