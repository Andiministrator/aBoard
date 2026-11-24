package storage

// llm_keys.go — LLM-API-Key-Storage (#298, ADR-0094).
//
// Speichert pro (user_id, provider) genau einen Key, AES-256-GCM-verschlüsselt
// via secretcrypto.Box (Reuse aus #306/ADR-0101). Klartext-Lesen ist nur
// für serverseitige Send-Calls vorgesehen (Send-Pfad #299) — Frontend-GET
// liefert ausschließlich die Provider-Liste, nie den Key.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

// ErrLLMKeyNotFound signalisiert, dass kein Key für (user_id, provider)
// existiert.
var ErrLLMKeyNotFound = errors.New("storage: llm api key nicht gefunden")

// LLMKeyMeta beschreibt einen Key, OHNE den Klartext. Sicher zur Auslieferung
// ans Frontend.
type LLMKeyMeta struct {
	Provider   string `json:"provider"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// LLMKeyRepo verwaltet llm_api_keys. box darf NICHT nil sein — anders als
// ConfigRepo lebt llm_keys nur mit Verschlüsselung (Klartext-Pfad existiert
// nicht). Der Aufrufer (main.go) bringt das Repo nur auf, wenn ein
// Master-Key konfiguriert ist; sonst antwortet der Handler 503.
type LLMKeyRepo struct {
	db  *sql.DB
	box *secretcrypto.Box
}

// NewLLMKeyRepo legt ein Repo an. box muss != nil sein.
func NewLLMKeyRepo(db *sql.DB, box *secretcrypto.Box) *LLMKeyRepo {
	return &LLMKeyRepo{db: db, box: box}
}

// List liefert die Meta-Liste aller Keys eines Users (NIE den Klartext).
func (r *LLMKeyRepo) List(ctx context.Context, userID string) ([]LLMKeyMeta, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT provider, created_at, coalesce(last_used_at, '')
		 FROM llm_api_keys WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("llm-keys.list: %w", err)
	}
	defer rows.Close()
	var out []LLMKeyMeta
	for rows.Next() {
		var m LLMKeyMeta
		if err := rows.Scan(&m.Provider, &m.CreatedAt, &m.LastUsedAt); err != nil {
			return nil, fmt.Errorf("llm-keys.list scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Has prüft ohne Decrypt, ob ein Key für (user_id, provider) existiert.
// Schneller als Get + Decrypt.
func (r *LLMKeyRepo) Has(ctx context.Context, userID, provider string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM llm_api_keys WHERE user_id = ? AND provider = ?`,
		userID, provider).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("llm-keys.has: %w", err)
	}
	return true, nil
}

// Get liest den Klartext-Key. Nur für serverseitige Send-Calls (#299).
// ErrLLMKeyNotFound wenn nichts gespeichert; sonstige Fehler propagieren.
//
// Sicherheits-Hinweis: Aufrufer DÜRFEN den Klartext NICHT loggen oder ans
// Frontend ausliefern (außer in einer Sanitized-Form von secretcrypto).
func (r *LLMKeyRepo) Get(ctx context.Context, userID, provider string) (string, error) {
	var enc string
	err := r.db.QueryRowContext(ctx,
		`SELECT key_encrypted FROM llm_api_keys WHERE user_id = ? AND provider = ?`,
		userID, provider).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrLLMKeyNotFound
	}
	if err != nil {
		return "", fmt.Errorf("llm-keys.get: %w", err)
	}
	plain, err := r.box.OpenString(enc)
	if err != nil {
		return "", fmt.Errorf("llm-keys.get decrypt: %w", err)
	}
	return plain, nil
}

// Set legt einen Key an oder ersetzt einen bestehenden. Verschlüsselt
// vor dem Schreiben; created_at bleibt beim Update erhalten.
func (r *LLMKeyRepo) Set(ctx context.Context, userID, provider, key string) error {
	enc, err := r.box.SealString(key)
	if err != nil {
		return fmt.Errorf("llm-keys.set seal: %w", err)
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("llm-keys.set conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("llm-keys.set begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Existiert bereits? → UPDATE, created_at bleibt.
	var exists bool
	if err := conn.QueryRowContext(ctx,
		`SELECT 1 FROM llm_api_keys WHERE user_id = ? AND provider = ?`,
		userID, provider).Scan(new(int)); err == nil {
		exists = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("llm-keys.set lookup: %w", err)
	}

	ts := nowISO()
	if exists {
		if _, err := conn.ExecContext(ctx,
			`UPDATE llm_api_keys SET key_encrypted = ?, last_used_at = NULL
			 WHERE user_id = ? AND provider = ?`,
			enc, userID, provider); err != nil {
			return fmt.Errorf("llm-keys.set update: %w", err)
		}
	} else {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO llm_api_keys (user_id, provider, key_encrypted, created_at)
			 VALUES (?, ?, ?, ?)`,
			userID, provider, enc, ts); err != nil {
			return fmt.Errorf("llm-keys.set insert: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("llm-keys.set commit: %w", err)
	}
	committed = true
	return nil
}

// Delete entfernt den Key (idempotent — kein Fehler wenn nicht existiert).
func (r *LLMKeyRepo) Delete(ctx context.Context, userID, provider string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM llm_api_keys WHERE user_id = ? AND provider = ?`,
		userID, provider); err != nil {
		return fmt.Errorf("llm-keys.delete: %w", err)
	}
	return nil
}

// MarkUsed aktualisiert last_used_at — wird beim erfolgreichen TestKey
// und beim Send (#299) gerufen. Best-effort: Fehler werden vom Aufrufer
// optional ignoriert (Send-Pfad soll nicht wegen eines fehlgeschlagenen
// Timestamps-Updates scheitern).
func (r *LLMKeyRepo) MarkUsed(ctx context.Context, userID, provider string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE llm_api_keys SET last_used_at = ? WHERE user_id = ? AND provider = ?`,
		nowISO(), userID, provider)
	if err != nil {
		return fmt.Errorf("llm-keys.mark-used: %w", err)
	}
	return nil
}
