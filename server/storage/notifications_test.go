package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func newNotifTestStack(t *testing.T) (
	*NotificationSubscriptionRepo,
	*NotificationConfigRepo,
	*NotificationLogRepo,
	func(),
) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	var id int
	newID := func() string {
		id++
		return "log-" + time.Now().UTC().Format("150405.000000") + "-" + hex.EncodeToString([]byte{byte(id)})
	}
	return NewNotificationSubscriptionRepo(db),
		NewNotificationConfigRepo(db, box),
		NewNotificationLogRepo(db, newID),
		func() { db.Close() }
}

func TestNotifSubscription_SetAndList(t *testing.T) {
	subs, _, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	if err := subs.Set(ctx, "u1", "card.commented", "email", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := subs.Set(ctx, "u1", "card.commented", "gotify", false); err != nil {
		t.Fatalf("Set2: %v", err)
	}
	list, err := subs.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list len=%d, want 2", len(list))
	}
}

func TestNotifSubscription_UpsertOverwrites(t *testing.T) {
	subs, _, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	_ = subs.Set(ctx, "u1", "card.commented", "email", true)
	_ = subs.Set(ctx, "u1", "card.commented", "email", false)
	en, present, err := subs.IsEnabled(ctx, "u1", "card.commented", "email")
	if err != nil {
		t.Fatalf("IsEnabled: %v", err)
	}
	if !present || en {
		t.Errorf("IsEnabled: present=%v en=%v, want present=true en=false", present, en)
	}
}

func TestNotifSubscription_EnabledSubscribers(t *testing.T) {
	subs, _, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	_ = subs.Set(ctx, "u1", "card.commented", "email", true)
	_ = subs.Set(ctx, "u2", "card.commented", "email", true)
	_ = subs.Set(ctx, "u3", "card.commented", "email", false)
	got, err := subs.EnabledSubscribers(ctx, "card.commented", "email")
	if err != nil {
		t.Fatalf("Enabled: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("enabled=%v, want 2 users", got)
	}
}

func TestNotifConfig_GotifyRoundtrip(t *testing.T) {
	_, cfgs, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	if err := cfgs.SetGotify(ctx, "u1", GotifyConfig{URL: "https://gotify.example/", Token: "secret-token"}); err != nil {
		t.Fatalf("SetGotify: %v", err)
	}
	got, err := cfgs.GetGotify(ctx, "u1")
	if err != nil {
		t.Fatalf("GetGotify: %v", err)
	}
	if got.URL != "https://gotify.example/" || got.Token != "secret-token" {
		t.Errorf("Roundtrip: %+v", got)
	}
}

func TestNotifConfig_Has(t *testing.T) {
	_, cfgs, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	ok, _ := cfgs.Has(ctx, "u1", "gotify")
	if ok {
		t.Error("Has: should be false before Set")
	}
	_ = cfgs.SetGotify(ctx, "u1", GotifyConfig{URL: "https://g.example", Token: "t"})
	ok, _ = cfgs.Has(ctx, "u1", "gotify")
	if !ok {
		t.Error("Has: should be true after Set")
	}
}

func TestNotifConfig_GotifyNotFound(t *testing.T) {
	_, cfgs, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	_, err := cfgs.GetGotify(ctx, "u-nope")
	if !errors.Is(err, ErrNotificationConfigNotFound) {
		t.Errorf("err=%v, want ErrNotificationConfigNotFound", err)
	}
}

func TestNotifConfig_Delete(t *testing.T) {
	_, cfgs, _, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	_ = cfgs.SetGotify(ctx, "u1", GotifyConfig{URL: "https://g", Token: "t"})
	if err := cfgs.Delete(ctx, "u1", "gotify"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, _ := cfgs.Has(ctx, "u1", "gotify")
	if ok {
		t.Error("Has: should be false after Delete")
	}
	// Delete idempotent
	if err := cfgs.Delete(ctx, "u1", "gotify"); err != nil {
		t.Errorf("Delete idempotent: %v", err)
	}
}

func TestNotifLog_InsertAndCount(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := logs.Insert(ctx, NotificationLogEntry{
			UserID:    "u1",
			EventType: "card.commented",
			Channel:   "email",
			Status:    NotifyStatusSent,
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	n, err := logs.CountSentSince(ctx, "u1", "email", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("CountSentSince: %v", err)
	}
	if n != 5 {
		t.Errorf("count=%d, want 5", n)
	}
}

func TestNotifLog_CoalescenceCount(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := logs.Insert(ctx, NotificationLogEntry{
			UserID:    "u1",
			EventType: "card.commented",
			Channel:   "email",
			CardID:    "card-A",
			Status:    NotifyStatusSent,
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	// Anderes Card-ID darf nicht zählen.
	_, _ = logs.Insert(ctx, NotificationLogEntry{
		UserID:    "u1",
		EventType: "card.commented",
		Channel:   "email",
		CardID:    "card-B",
		Status:    NotifyStatusSent,
	})
	n, err := logs.RecentForCoalescence(ctx, "u1", "card.commented", "email", "card-A", time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("RecentForCoalescence: %v", err)
	}
	if n != 3 {
		t.Errorf("coalesce-count=%d, want 3", n)
	}
}

func TestNotifLog_PendingDigest_MarkDigested(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := logs.Insert(ctx, NotificationLogEntry{
			UserID:    "u1",
			EventType: "card.commented",
			Channel:   "email",
			Status:    NotifyStatusThrottled,
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		ids = append(ids, id)
	}
	pend, err := logs.PendingDigest(ctx, "u1")
	if err != nil {
		t.Fatalf("PendingDigest: %v", err)
	}
	if len(pend) != 3 {
		t.Errorf("pending=%d, want 3", len(pend))
	}
	// Mark digested
	if err := logs.MarkDigested(ctx, ids); err != nil {
		t.Fatalf("MarkDigested: %v", err)
	}
	pend2, _ := logs.PendingDigest(ctx, "u1")
	if len(pend2) != 0 {
		t.Errorf("pending after digest=%d, want 0", len(pend2))
	}
}

func TestNotifLog_UsersWithPendingDigest(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()
	_, _ = logs.Insert(ctx, NotificationLogEntry{UserID: "u1", EventType: "card.commented", Channel: "email", Status: NotifyStatusThrottled})
	_, _ = logs.Insert(ctx, NotificationLogEntry{UserID: "u2", EventType: "card.commented", Channel: "email", Status: NotifyStatusThrottled})
	_, _ = logs.Insert(ctx, NotificationLogEntry{UserID: "u3", EventType: "card.commented", Channel: "email", Status: NotifyStatusSent})
	users, err := logs.UsersWithPendingDigest(ctx)
	if err != nil {
		t.Fatalf("UsersWithPendingDigest: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("pending users=%v, want 2", users)
	}
}

// TestNotifLog_EvictOlderThan sichert F-379 ab (Karte #411): alte Zeilen
// verschwinden, junge bleiben — ein Test, der nur das Löschen prüft, wäre
// auch mit einem blinden `DELETE FROM notification_log` grün gewesen.
func TestNotifLog_EvictOlderThan(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()

	oldID, err := logs.Insert(ctx, NotificationLogEntry{UserID: "u1", EventType: "card.commented", Channel: "email", Status: NotifyStatusSent})
	if err != nil {
		t.Fatalf("insert old: %v", err)
	}
	youngID, err := logs.Insert(ctx, NotificationLogEntry{UserID: "u1", EventType: "card.commented", Channel: "email", Status: NotifyStatusSent})
	if err != nil {
		t.Fatalf("insert young: %v", err)
	}

	// oldID über das 30-Tage-Fenster hinaus zurückdatieren; youngID bleibt auf
	// "jetzt" (per Insert bereits über nowISO() gesetzt).
	oldTs := time.Now().UTC().Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := logs.db.ExecContext(ctx, `UPDATE notification_log SET ts = ? WHERE id = ?`, oldTs, oldID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := logs.EvictOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("EvictOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("Evict-Anzahl = %d, erwartet 1", n)
	}

	var count int
	if err := logs.db.QueryRowContext(ctx, `SELECT count(*) FROM notification_log WHERE id = ?`, oldID).Scan(&count); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if count != 0 {
		t.Errorf("alte Zeile (id=%s) sollte nach Evict weg sein", oldID)
	}
	if err := logs.db.QueryRowContext(ctx, `SELECT count(*) FROM notification_log WHERE id = ?`, youngID).Scan(&count); err != nil {
		t.Fatalf("count young: %v", err)
	}
	if count != 1 {
		t.Errorf("junge Zeile (id=%s) sollte erhalten bleiben", youngID)
	}
}

// TestNotifLog_EvictOlderThan_NutztFixierteUhr sichert F-686c ab: EvictOlderThan
// muss den Cutoff aus `timeNow()` berechnen, nicht aus dem echten `time.Now()`.
//
// Konstruktion (bewusst so und nicht mit einer "normalen" Vergangenheits-
// Zeile, die unter BEIDEN Uhren als alt durchginge): `timeNow` wird auf ein
// festes Datum weit in der Zukunft (Jahr 2200) fixiert. Die Zeile bekommt
// einen ts-Wert im Jahr 2100 — das liegt klar VOR der fixierten Uhr (2200)
// und damit weit ausserhalb des 30-Tage-Fensters (sollte evicted werden),
// aber NACH der echten Systemzeit (liegt in der Zukunft, also NICHT älter
// als ein Cutoff, der aus dem echten `time.Now()` berechnet würde — bliebe
// also stehen). Nur mit dem Fix (Cutoff aus `timeNow()`) wird die Zeile
// gelöscht; mit dem ungefixten `time.Now()` bleibt sie stehen, weil 2100 in
// der Zukunft der echten Systemuhr liegt.
func TestNotifLog_EvictOlderThan_NutztFixierteUhr(t *testing.T) {
	_, _, logs, done := newNotifTestStack(t)
	defer done()
	ctx := context.Background()

	fixiertesJetzt := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
	orig := timeNow
	timeNow = func() time.Time { return fixiertesJetzt }
	defer func() { timeNow = orig }()

	id, err := logs.Insert(ctx, NotificationLogEntry{UserID: "u1", EventType: "card.commented", Channel: "email", Status: NotifyStatusSent})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	altTs := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := logs.db.ExecContext(ctx, `UPDATE notification_log SET ts = ? WHERE id = ?`, altTs, id); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := logs.EvictOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("EvictOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("Evict-Anzahl = %d, erwartet 1 — EvictOlderThan nutzt offenbar nicht die fixierte Uhr (F-686c)", n)
	}
}
