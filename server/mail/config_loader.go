package mail

// config_loader.go — Brücke zwischen ConfigRepo (#306) und SMTP-Config-Struct.
//
// LoadConfig holt die smtp.*-Keys aus dem ConfigStore in eine Config-Instanz.
// BootstrapEnv importiert fehlende Keys aus Env-Vars (one-shot beim Server-
// Start) — Env wirkt nur als Initial-Default. Danach: DB wins.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// LoadConfig liest die SMTP-Konfiguration aus dem ConfigStore.
// Fehlende Pflichtfelder ergeben eine Config mit leeren Werten — Validate()
// (in smtp.go) wirft dann ErrSMTPNotConfigured.
func LoadConfig(ctx context.Context, cfg *storage.ConfigRepo) (Config, error) {
	c := Config{Port: 587, STARTTLS: true, Timeout: 15 * time.Second}
	get := func(key string) (string, error) {
		v, err := cfg.Get(ctx, key)
		if err != nil {
			return "", nil // Key fehlt → leer
		}
		return v.Value, nil
	}
	if v, _ := get("smtp.host"); v != "" {
		c.Host = v
	}
	if v, _ := get("smtp.port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Port = n
		}
	}
	if v, _ := get("smtp.user"); v != "" {
		c.User = v
	}
	if v, _ := get("smtp.pass"); v != "" {
		c.Pass = v
	}
	if v, _ := get("smtp.from"); v != "" {
		c.From = v
	}
	if v, _ := get("smtp.from_name"); v != "" {
		c.FromName = v
	}
	if v, _ := get("smtp.starttls"); v == "false" {
		c.STARTTLS = false
	}
	return c, nil
}

// BootstrapEnv importiert SMTP_*-Env-Vars in den ConfigStore, sobald die
// jeweiligen Keys noch nicht existieren. Idempotent — bei nächstem Start
// passiert nichts mehr.
//
// Mapping:
//
//	SMTP_HOST      → smtp.host (plain)
//	SMTP_PORT      → smtp.port (plain)
//	SMTP_USER      → smtp.user (plain)
//	SMTP_PASS      → smtp.pass (SECRET, wird via secretcrypto verschlüsselt)
//	SMTP_FROM      → smtp.from (plain)
//	SMTP_FROM_NAME → smtp.from_name (plain)
//	SMTP_STARTTLS  → smtp.starttls (plain)
//
// Auf Servern mit Pre-#298-Bestand (Master-Key fehlt) wird SMTP_PASS
// übersprungen — die Bootstrap-Routine emittiert dann eine Warnung.
func BootstrapEnv(ctx context.Context, cfg *storage.ConfigRepo, log func(string, ...any)) error {
	if cfg == nil {
		return nil
	}
	mappings := []struct {
		env    string
		key    string
		secret bool
	}{
		{"SMTP_HOST", "smtp.host", false},
		{"SMTP_PORT", "smtp.port", false},
		{"SMTP_USER", "smtp.user", false},
		{"SMTP_PASS", "smtp.pass", true},
		{"SMTP_FROM", "smtp.from", false},
		{"SMTP_FROM_NAME", "smtp.from_name", false},
		{"SMTP_STARTTLS", "smtp.starttls", false},
	}
	for _, m := range mappings {
		v := os.Getenv(m.env)
		if v == "" {
			continue
		}
		// Existiert der Key schon? → nicht überschreiben (DB wins).
		if existing, err := cfg.Get(ctx, m.key); err == nil && existing.Value != "" {
			continue
		}
		if m.secret && !cfg.SecretsEnabled() {
			log("aboard-server: %s gesetzt, aber ABOARD_SECRET_KEY fehlt — Secret nicht importiert", m.env)
			continue
		}
		if err := cfg.Set(ctx, m.key, v, m.secret, ""); err != nil {
			return fmt.Errorf("mail-bootstrap %s: %w", m.key, err)
		}
	}
	return nil
}
