package storage

// notifications.go — Notification-Subsystem-Storage (#305, ADR-0107).
//
// Drei Repos:
//   - NotificationSubscriptionRepo  — pro (user_id, event_type, channel) ein
//                                     enabled-Flag.
//   - NotificationConfigRepo        — pro (user_id, channel) kanal-spezifische
//                                     Config (Gotify-URL + verschlüsseltes
//                                     Token). E-Mail braucht keinen Eintrag —
//                                     Adresse stammt aus users.email.
//   - NotificationLogRepo           — append-only Audit-/Throttle-Log.
//
// Verschlüsselung läuft über secretcrypto.Box (ABOARD_SECRET_KEY) — identisch
// zu llm_api_keys + server_config-Secrets. Fehlt der Master-Key, akzeptiert
// ConfigRepo keine Schreibvorgänge (Read-Only-Modus für Secrets).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

// ErrNotificationConfigNotFound signalisiert, dass keine Config für
// (user_id, channel) existiert.
var ErrNotificationConfigNotFound = errors.New("storage: notification-config nicht gefunden")

// NotificationStatus sind die kanonischen Log-Status-Werte.
const (
	NotifyStatusSent      = "sent"
	NotifyStatusFailed    = "failed"
	NotifyStatusCoalesced = "coalesced"
	NotifyStatusThrottled = "throttled"
)

// NotificationChannel-Konstanten — Schema validiert auch.
const (
	NotifyChannelEmail  = "email"
	NotifyChannelGotify = "gotify"
)

// ── Subscriptions ────────────────────────────────────────────────────

// NotificationSubscription beschreibt einen pro-User-pro-EventType-pro-Channel
// enabled/disabled-Eintrag.
type NotificationSubscription struct {
	UserID    string `json:"user_id"`
	EventType string `json:"event_type"`
	Channel   string `json:"channel"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

// NotificationSubscriptionRepo verwaltet notification_subscriptions.
type NotificationSubscriptionRepo struct {
	db *sql.DB
}

// NewNotificationSubscriptionRepo legt ein Repo an.
func NewNotificationSubscriptionRepo(db *sql.DB) *NotificationSubscriptionRepo {
	return &NotificationSubscriptionRepo{db: db}
}

// List liefert alle expliziten Einträge eines Users. Default-Werte (was nicht
// explizit gesetzt wurde) liegen NICHT im Repo — der Aufrufer kennt sie
// via notifications.DefaultSubscribed(eventType, channel).
func (r *NotificationSubscriptionRepo) List(ctx context.Context, userID string) ([]NotificationSubscription, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id, event_type, channel, enabled, updated_at
		 FROM notification_subscriptions WHERE user_id = ?
		 ORDER BY event_type, channel`, userID)
	if err != nil {
		return nil, fmt.Errorf("notif-subs.list: %w", err)
	}
	defer rows.Close()
	var out []NotificationSubscription
	for rows.Next() {
		var s NotificationSubscription
		var enabledInt int
		if err := rows.Scan(&s.UserID, &s.EventType, &s.Channel, &enabledInt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("notif-subs.list scan: %w", err)
		}
		s.Enabled = enabledInt != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// Set legt einen Eintrag an oder überschreibt einen bestehenden.
func (r *NotificationSubscriptionRepo) Set(ctx context.Context, userID, eventType, channel string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO notification_subscriptions (user_id, event_type, channel, enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, event_type, channel) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, eventType, channel, en, nowISO()); err != nil {
		return fmt.Errorf("notif-subs.set: %w", err)
	}
	return nil
}

// EnabledSubscribers liefert die User-IDs, deren Subscription für
// (eventType, channel) aktiv ist. Nutzt die explizite Tabelle — Default-
// Behandlung übernimmt der Aufrufer (siehe notifications.SubscriberLookup).
func (r *NotificationSubscriptionRepo) EnabledSubscribers(ctx context.Context, eventType, channel string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id FROM notification_subscriptions
		 WHERE event_type = ? AND channel = ? AND enabled = 1`,
		eventType, channel)
	if err != nil {
		return nil, fmt.Errorf("notif-subs.enabled: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("notif-subs.enabled scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// IsEnabled prüft die explizite Subscription für (user, event_type, channel).
// `present=false` bedeutet: kein expliziter Eintrag → Default greift.
func (r *NotificationSubscriptionRepo) IsEnabled(ctx context.Context, userID, eventType, channel string) (enabled, present bool, err error) {
	var en int
	err = r.db.QueryRowContext(ctx,
		`SELECT enabled FROM notification_subscriptions
		 WHERE user_id = ? AND event_type = ? AND channel = ?`,
		userID, eventType, channel).Scan(&en)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("notif-subs.is-enabled: %w", err)
	}
	return en != 0, true, nil
}

// ── Config (pro Channel) ─────────────────────────────────────────────

// GotifyConfig ist die für Channel="gotify" verschlüsselt persistierte Form.
// Das Token wird NIE an Frontend ausgeliefert (Write-only-Vertrag, ADR-0107 §6).
type GotifyConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// NotificationConfigRepo verwaltet notification_config. Schreibt nur, wenn
// box != nil — Frontend sieht via Has() ob ein Eintrag existiert.
type NotificationConfigRepo struct {
	db  *sql.DB
	box *secretcrypto.Box
}

// NewNotificationConfigRepo legt ein Repo an. box darf nil sein (Read-only
// für Secrets) — Set() liefert dann ErrSecretsUnavailable.
func NewNotificationConfigRepo(db *sql.DB, box *secretcrypto.Box) *NotificationConfigRepo {
	return &NotificationConfigRepo{db: db, box: box}
}

// SecretsEnabled zeigt an, ob ein Master-Key konfiguriert ist.
func (r *NotificationConfigRepo) SecretsEnabled() bool { return r.box != nil }

// Has prüft ohne Decrypt, ob ein Config-Eintrag existiert. Schneller Test
// für den GET-Pfad — Frontend bekommt nur „is_set: true/false".
func (r *NotificationConfigRepo) Has(ctx context.Context, userID, channel string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM notification_config WHERE user_id = ? AND channel = ?`,
		userID, channel).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notif-config.has: %w", err)
	}
	return true, nil
}

// GetGotify liest + dekryptiert eine Gotify-Config. Nur für serverseitige
// Send-Calls — NIE an Frontend ausliefern.
func (r *NotificationConfigRepo) GetGotify(ctx context.Context, userID string) (*GotifyConfig, error) {
	if r.box == nil {
		return nil, ErrSecretsUnavailable
	}
	var enc string
	err := r.db.QueryRowContext(ctx,
		`SELECT config_encrypted FROM notification_config WHERE user_id = ? AND channel = ?`,
		userID, NotifyChannelGotify).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotificationConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("notif-config.get-gotify: %w", err)
	}
	plain, err := r.box.OpenString(enc)
	if err != nil {
		return nil, fmt.Errorf("notif-config.get-gotify decrypt: %w", err)
	}
	var cfg GotifyConfig
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		return nil, fmt.Errorf("notif-config.get-gotify unmarshal: %w", err)
	}
	return &cfg, nil
}

// SetGotify ersetzt die Gotify-Config (Token + URL). Anwendbar nur wenn
// SecretsEnabled() == true.
func (r *NotificationConfigRepo) SetGotify(ctx context.Context, userID string, cfg GotifyConfig) error {
	if r.box == nil {
		return ErrSecretsUnavailable
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("notif-config.set-gotify marshal: %w", err)
	}
	enc, err := r.box.SealString(string(raw))
	if err != nil {
		return fmt.Errorf("notif-config.set-gotify seal: %w", err)
	}
	now := nowISO()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO notification_config (user_id, channel, config_encrypted, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, channel) DO UPDATE SET config_encrypted = excluded.config_encrypted, updated_at = excluded.updated_at`,
		userID, NotifyChannelGotify, enc, now, now); err != nil {
		return fmt.Errorf("notif-config.set-gotify: %w", err)
	}
	return nil
}

// Delete entfernt eine Channel-Config (idempotent).
func (r *NotificationConfigRepo) Delete(ctx context.Context, userID, channel string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM notification_config WHERE user_id = ? AND channel = ?`,
		userID, channel); err != nil {
		return fmt.Errorf("notif-config.delete: %w", err)
	}
	return nil
}

// ── Log (append-only) ────────────────────────────────────────────────

// NotificationLogEntry ist eine Zeile des append-only Audit-Logs.
type NotificationLogEntry struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	EventType  string `json:"event_type"`
	Channel    string `json:"channel"`
	CardID     string `json:"card_id,omitempty"`
	BoardID    string `json:"board_id,omitempty"`
	Summary    string `json:"summary,omitempty"`
	TS         string `json:"ts"`
	Status     string `json:"status"`
	RetryCount int    `json:"retry_count"`
	Error      string `json:"error,omitempty"`
	DigestedAt string `json:"digested_at,omitempty"`
}

// NotificationLogRepo verwaltet notification_log.
type NotificationLogRepo struct {
	db    *sql.DB
	newID func() string
}

// NewNotificationLogRepo legt ein Repo an. newID erzeugt UUIDs (Aufrufer-
// Konvention wie ConfigRepo).
func NewNotificationLogRepo(db *sql.DB, newID func() string) *NotificationLogRepo {
	return &NotificationLogRepo{db: db, newID: newID}
}

// Insert legt einen Log-Eintrag an und liefert die generierte ID.
func (r *NotificationLogRepo) Insert(ctx context.Context, e NotificationLogEntry) (string, error) {
	if e.ID == "" {
		e.ID = r.newID()
	}
	if e.TS == "" {
		e.TS = nowISO()
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO notification_log
		 (id, user_id, event_type, channel, card_id, board_id, summary, ts, status, retry_count, error, digested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.UserID, e.EventType, e.Channel, nullStr(e.CardID), nullStr(e.BoardID),
		nullStr(e.Summary), e.TS, e.Status, e.RetryCount, nullStr(e.Error), nullStr(e.DigestedAt)); err != nil {
		return "", fmt.Errorf("notif-log.insert: %w", err)
	}
	return e.ID, nil
}

// CountSentSince zählt, wie viele `sent`-Einträge ein User über einen Channel
// seit `since` produziert hat — für das 30/h-Throttling-Limit.
func (r *NotificationLogRepo) CountSentSince(ctx context.Context, userID, channel string, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM notification_log
		 WHERE user_id = ? AND channel = ? AND status = ? AND ts >= ?`,
		userID, channel, NotifyStatusSent, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("notif-log.count-sent: %w", err)
	}
	return n, nil
}

// RecentForCoalescence liefert die jüngsten Logeinträge für ein
// (user, event_type, channel, card_id)-Tupel innerhalb des Coalescence-Windows.
// Wird der Worker an direkt anschließenden Events zum selben Tupel innerhalb
// 5 Min anwenden → coalesced-Status statt erneutem Versand.
func (r *NotificationLogRepo) RecentForCoalescence(ctx context.Context, userID, eventType, channel, cardID string, since time.Time) (int, error) {
	q := `SELECT count(*) FROM notification_log
	      WHERE user_id = ? AND event_type = ? AND channel = ? AND ts >= ?`
	args := []interface{}{userID, eventType, channel, since.UTC().Format(time.RFC3339)}
	if cardID != "" {
		q += " AND card_id = ?"
		args = append(args, cardID)
	}
	q += " AND status IN ('sent', 'coalesced')"
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("notif-log.coalesce-count: %w", err)
	}
	return n, nil
}

// PendingDigest liefert Throttled-Einträge eines Users, die noch nicht in
// eine Digest-Mail geflossen sind — Worker für 18:00-Tagesübersicht.
func (r *NotificationLogRepo) PendingDigest(ctx context.Context, userID string) ([]NotificationLogEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, event_type, channel, coalesce(card_id, ''), coalesce(board_id, ''),
		        coalesce(summary, ''), ts, status, retry_count, coalesce(error, ''), coalesce(digested_at, '')
		 FROM notification_log
		 WHERE user_id = ? AND status = ? AND digested_at IS NULL
		 ORDER BY ts ASC`,
		userID, NotifyStatusThrottled)
	if err != nil {
		return nil, fmt.Errorf("notif-log.pending-digest: %w", err)
	}
	defer rows.Close()
	var out []NotificationLogEntry
	for rows.Next() {
		var e NotificationLogEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.EventType, &e.Channel, &e.CardID, &e.BoardID,
			&e.Summary, &e.TS, &e.Status, &e.RetryCount, &e.Error, &e.DigestedAt); err != nil {
			return nil, fmt.Errorf("notif-log.pending-digest scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkDigested setzt digested_at auf NOW() für alle gegebenen IDs (in einem
// Statement). Wird vom Digest-Worker aufgerufen, nachdem die Sammel-Mail
// versandt ist.
func (r *NotificationLogRepo) MarkDigested(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// SQLite hat ein 999er-Parameter-Limit; in Batches splitten falls je
	// nötig (heute reicht ein Stmt — pro User selten >100 throttled/24h).
	now := nowISO()
	q := "UPDATE notification_log SET digested_at = ? WHERE id IN ("
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, now)
	for i, id := range ids {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, id)
	}
	q += ")"
	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("notif-log.mark-digested: %w", err)
	}
	return nil
}

// UsersWithPendingDigest liefert die User-IDs, die noch unvergebene
// throttled-Einträge haben — Digest-Worker iteriert sie.
func (r *NotificationLogRepo) UsersWithPendingDigest(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM notification_log
		 WHERE status = ? AND digested_at IS NULL`,
		NotifyStatusThrottled)
	if err != nil {
		return nil, fmt.Errorf("notif-log.pending-users: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("notif-log.pending-users scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// EvictOlderThan löscht notification_log-Einträge, deren `ts` älter als die
// angegebene Dauer ist (F-379, Karte #411). Analog zu
// ImapProcessedRepo.EvictOlderThan (imap_state.go) — dieselbe Klasse Tabelle
// (append-only Log ohne eigenen Aufräum-Pfad), hier bislang ungelöst. Aufruf
// im selben 18:00-Takt wie der Digest-Versand (DigestScheduler.loop), statt
// einen zweiten Scheduler einzuführen.
func (r *NotificationLogRepo) EvictOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	// RFC3339Nano (nicht RFC3339) wie imap_state.go: ts wird über nowISO() mit
	// Nanosekunden-Genauigkeit geschrieben (users.go). Ein Cutoff ohne
	// Nanosekunden würde bei String-Vergleich in der exakt selben Sekunde
	// fälschlich "kleiner" sortieren ('.' < 'Z' in ASCII) und frische Zeilen
	// mit-evicten.
	//
	// F-686c: timeNow() statt direktem time.Now() — dieselbe Uhr, mit der auch
	// nowISO() die ts-Spalte beim Insert füllt. Tests können sie fixieren.
	cutoff := timeNow().UTC().Add(-age).Format(time.RFC3339Nano)
	res, err := r.db.ExecContext(ctx, `DELETE FROM notification_log WHERE ts < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("notif-log.evict: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// nullStr ist ein kleiner Helper, der leere Strings als SQL-NULL persistiert.
// Hält die Tabelle kompakt und unterscheidet „leer" von „nicht gesetzt".
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
