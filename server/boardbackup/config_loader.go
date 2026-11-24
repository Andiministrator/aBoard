package boardbackup

// config_loader.go — Brücke ConfigRepo (#306) ↔ boardbackup-Config.
//
// Keys in der server_config-Tabelle (alle plain, keine Secrets):
//   backup_board.cron           (default "0 2 * * *")
//   backup_board.dir            (default "/data/backups/boards")
//   backup_board.keep_days      (default 7)
//   backup_board.max_upload_mb  (default 500)
//   backup_board.enabled        (default "true")
//   backup_board.encryption_enabled     (default "false")   — #336, ADR-0098 §10
//   backup_board.encryption_key_source  (default "master_key")

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Config bündelt die runtime-veränderlichen Backup-Einstellungen.
type Config struct {
	Cron        string
	Dir         string
	KeepDays    int
	MaxUploadMB int
	Enabled     bool
	// EncryptionEnabled schaltet Encryption-at-Rest für NEUE Backups ein
	// (#336). Bestehende unverschlüsselte Archive bleiben lesbar; das Flag
	// wirkt nur auf die Schreibseite.
	EncryptionEnabled bool
	// EncryptionKeySource benennt die Schlüsselquelle. Gültig ist derzeit
	// ausschliesslich KeySourceMaster — siehe die Konstante.
	EncryptionKeySource string
}

// DefaultConfig liefert die Default-Werte (#303, ADR-0098).
func DefaultConfig() Config {
	return Config{
		Cron:                "0 2 * * *",
		Dir:                 "/data/backups/boards",
		KeepDays:            7,
		MaxUploadMB:         500,
		Enabled:             true,
		EncryptionEnabled:   false,
		EncryptionKeySource: KeySourceMaster,
	}
}

// LoadConfig liest die Sektion `backup_board.*` aus dem ConfigStore.
// Fehlende Keys → Defaults. Wert leer-String wird wie „nicht gesetzt"
// behandelt (Default).
func LoadConfig(ctx context.Context, cfg *storage.ConfigRepo) Config {
	c := DefaultConfig()
	if cfg == nil {
		return c
	}
	get := func(key string) string {
		v, err := cfg.Get(ctx, key)
		if err != nil || v == nil {
			return ""
		}
		return v.Value
	}
	if v := get("backup_board.cron"); v != "" {
		c.Cron = v
	}
	if v := get("backup_board.dir"); v != "" {
		c.Dir = v
	}
	if v := get("backup_board.keep_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.KeepDays = n
		}
	}
	if v := get("backup_board.max_upload_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxUploadMB = n
		}
	}
	if v := get("backup_board.enabled"); v == "false" {
		c.Enabled = false
	}
	if v := get("backup_board.encryption_enabled"); v == "true" {
		c.EncryptionEnabled = true
	}
	if v := get("backup_board.encryption_key_source"); v != "" {
		c.EncryptionKeySource = v
	}
	return c
}

// KeySourceMaster ist der einzige unterstützte Wert für
// `backup_board.encryption_key_source`: der Server-Master-Key
// ABOARD_SECRET_KEY, mit dem auch die LLM-API-Keys und die
// server_config-Secrets verschlüsselt sind.
//
// WARUM NUR EINER (Entscheid des Maintainers, 2026-08-19): Ein backup-spezifischer
// Zweitschlüssel wäre ein zweites Schlüsselmanagement — eine zweite Sache,
// die gesichert, rotiert und bei einem Serverumzug mitgenommen werden muss.
// Der Preis dieser Entscheidung ist ausdrücklich akzeptiert und gehört in
// die Betriebsdoku, nicht nur in diesen Kommentar: **Geht ABOARD_SECRET_KEY
// verloren, sind die Backups UND die gespeicherten API-Keys unlesbar.**
//
// Der Config-Key existiert trotzdem, statt die Quelle fest zu verdrahten:
// So ist die Entscheidung an der Stelle sichtbar, an der jemand sie ändern
// wollte, und ein abweichender Wert wird laut abgelehnt statt still ignoriert.
const KeySourceMaster = "master_key"

// ErrUnknownKeySource meldet einen nicht unterstützten Wert in
// `backup_board.encryption_key_source`.
var ErrUnknownKeySource = errors.New(
	"backup_board.encryption_key_source: unbekannter Wert — unterstützt ist nur " + KeySourceMaster)

// ResolveCrypter entscheidet für die SCHREIBSEITE, ob und womit verschlüsselt
// wird. Rückgabe (nil, nil) heisst „unverschlüsselt schreiben".
//
// Fehlt bei eingeschalteter Verschlüsselung der Schlüssel, ist das ein
// FEHLER und kein Grund, ersatzweise unverschlüsselt zu schreiben: eine
// stille Herabstufung des Schutzniveaus wäre genau das, was der Operator
// mit dem Schalter ausschliessen wollte.
func ResolveCrypter(cfg Config, masterKey string) (*Crypter, error) {
	if !cfg.EncryptionEnabled {
		return nil, nil
	}
	src := cfg.EncryptionKeySource
	if src == "" {
		src = KeySourceMaster
	}
	if src != KeySourceMaster {
		return nil, fmt.Errorf("%w (gesetzt: %q)", ErrUnknownKeySource, src)
	}
	if masterKey == "" {
		return nil, ErrEncryptionKeyMissing
	}
	return NewCrypter(masterKey)
}
