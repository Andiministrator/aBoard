package mail

// imap_config.go — IMAP-Worker-Config + ConfigStore-/Env-Bootstrap (#295, ADR-0093).
//
// Strukturell analog zu config_loader.go (SMTP, ADR-0091): LoadIMAPConfig holt
// die imap.*-Keys aus dem ConfigStore, BootstrapIMAPEnv importiert IMAP_*-
// Env-Vars beim Server-Start in den Store (one-shot, idempotent, DB wins).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// IMAPConfig bündelt die IMAP-Worker-Parameter. Werte stammen aus dem
// ConfigStore (#306/ADR-0101: imap.host/port/user/pass/tls/folder/...).
type IMAPConfig struct {
	Host                string
	Port                int
	User                string
	Pass                string
	TLS                 bool // true = direkt TLS (993), false = STARTTLS auf Plain-Port
	Folder              string
	ProcessedFolder     string
	ErrorFolder         string
	PollingInterval     time.Duration
	MaxMailSizeMB       int
	MaxAttachmentSizeMB int
	Enabled             bool
	DialTimeout         time.Duration
	// RateLimitPerHour begrenzt eingehende Mails pro Absender-Adresse
	// (Spam-Schutz, ADR-0093 §7). 0 = Limit aus. Default 20.
	//
	// F-370: Der Wert stand als Literal `RateLimitFilter(20)` in main.go,
	// obwohl ADR-0093 §7 ihn als Konfigurations-Eigenschaft führt. Ein
	// Betreiber mit einem regen Absender (Ticket-Gateway, Monitoring) konnte
	// ihn nur durch einen Neubau ändern.
	RateLimitPerHour int
}

// ErrIMAPNotConfigured signalisiert dass keine IMAP-Konfiguration vorhanden
// oder unvollständig ist. Worker startet dann nicht (Enabled=false-Path).
var ErrIMAPNotConfigured = errors.New("mail: imap not configured")

// Defaults gemäß ADR-0093 §8.
const (
	defaultIMAPPort                  = 993
	defaultIMAPTLS                   = true
	defaultIMAPFolder                = "INBOX"
	defaultIMAPProcessedFolder       = "Processed"
	defaultIMAPErrorFolder           = "Errors"
	defaultIMAPPollingIntervalSec    = 600
	defaultIMAPMinPollingIntervalSec = 60
	defaultIMAPMaxMailSizeMB         = 25
	defaultIMAPMaxAttachmentSizeMB   = 10
	defaultIMAPEnabled               = false
	defaultIMAPRateLimitPerHour      = 20 // ADR-0093 §7
)

// LoadIMAPConfig liest die imap.*-Keys aus dem ConfigStore. Fehlende Keys →
// Default-Werte. Boolean-Parsing tolerant ("true"/"1"/"yes" → true).
//
// Validate() wird hier NICHT aufgerufen — der Aufrufer (Worker-Start)
// entscheidet, ob Enabled=false-Pfad reicht oder eine Validierung Pflicht ist.
func LoadIMAPConfig(ctx context.Context, cfg *storage.ConfigRepo) IMAPConfig {
	c := IMAPConfig{
		Port:                defaultIMAPPort,
		TLS:                 defaultIMAPTLS,
		Folder:              defaultIMAPFolder,
		ProcessedFolder:     defaultIMAPProcessedFolder,
		ErrorFolder:         defaultIMAPErrorFolder,
		PollingInterval:     time.Duration(defaultIMAPPollingIntervalSec) * time.Second,
		MaxMailSizeMB:       defaultIMAPMaxMailSizeMB,
		MaxAttachmentSizeMB: defaultIMAPMaxAttachmentSizeMB,
		Enabled:             defaultIMAPEnabled,
		DialTimeout:         15 * time.Second,
		RateLimitPerHour:    defaultIMAPRateLimitPerHour,
	}
	if cfg == nil {
		return c
	}
	get := func(key string) string {
		v, err := cfg.Get(ctx, key)
		if err != nil {
			return ""
		}
		return v.Value
	}
	if v := get("imap.host"); v != "" {
		c.Host = v
	}
	if v := get("imap.port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			c.Port = n
		}
	}
	if v := get("imap.user"); v != "" {
		c.User = v
	}
	if v := get("imap.pass"); v != "" {
		c.Pass = v
	}
	if v := get("imap.tls"); v != "" {
		c.TLS = parseIMAPBool(v, defaultIMAPTLS)
	}
	if v := get("imap.folder"); v != "" {
		c.Folder = v
	}
	if v := get("imap.processed_folder"); v != "" {
		c.ProcessedFolder = v
	}
	if v := get("imap.error_folder"); v != "" {
		c.ErrorFolder = v
	}
	if v := get("imap.polling_interval_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= defaultIMAPMinPollingIntervalSec {
			c.PollingInterval = time.Duration(n) * time.Second
		} else if err == nil && n > 0 {
			// Clamp auf Minimum (ADR-0093 §2): Provider-Rate-Limit-Schutz
			c.PollingInterval = time.Duration(defaultIMAPMinPollingIntervalSec) * time.Second
		}
	}
	if v := get("imap.max_mail_size_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxMailSizeMB = n
		}
	}
	if v := get("imap.max_attachment_size_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxAttachmentSizeMB = n
		}
	}
	if v := get("imap.enabled"); v != "" {
		c.Enabled = parseIMAPBool(v, defaultIMAPEnabled)
	}
	// F-370/ADR-0093 §7: 0 ist ein gültiger Wert und schaltet das Limit AUS
	// (so liest es RateLimitFilter). Negative Werte werden verworfen — sie
	// hätten keine sinnvolle Lesart, und der Default ist die sicherere Wahl
	// als „Limit versehentlich aus".
	if v := get("imap.rate_limit_per_hour"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.RateLimitPerHour = n
		}
	}
	return c
}

// Validate prüft die Pflichtfelder für einen Worker-Start. Bei Enabled=false
// reicht der Default-Pfad; bei Enabled=true muss Host/User/Pass gesetzt sein.
//
// Sprint-40-Pattern: Auth ist Pflicht — kein Anonymous-IMAP (Privacy + Sec).
func (c IMAPConfig) Validate() error {
	if !c.Enabled {
		return nil // Worker startet einfach nicht — kein Fehler
	}
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("%w: imap.host fehlt", ErrIMAPNotConfigured)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("%w: imap.port ungültig: %d", ErrIMAPNotConfigured, c.Port)
	}
	if strings.TrimSpace(c.User) == "" || strings.TrimSpace(c.Pass) == "" {
		return fmt.Errorf("%w: imap.user/imap.pass sind Pflicht", ErrIMAPNotConfigured)
	}
	if strings.TrimSpace(c.Folder) == "" {
		return fmt.Errorf("%w: imap.folder fehlt", ErrIMAPNotConfigured)
	}
	return nil
}

// MaxMailBytes liefert das Mail-Größenlimit in Bytes.
func (c IMAPConfig) MaxMailBytes() int64 {
	return int64(c.MaxMailSizeMB) * 1024 * 1024
}

// MaxAttachmentBytes liefert das Anhang-Größenlimit in Bytes.
func (c IMAPConfig) MaxAttachmentBytes() int64 {
	return int64(c.MaxAttachmentSizeMB) * 1024 * 1024
}

// BootstrapIMAPEnv importiert IMAP_*-Env-Vars in den ConfigStore, sobald die
// jeweiligen Keys noch nicht existieren. Idempotent — bei nächstem Start
// passiert nichts mehr (DB wins, ADR-0091-Pattern).
//
// Mapping:
//
//	IMAP_HOST                    → imap.host                  (plain)
//	IMAP_PORT                    → imap.port                  (plain)
//	IMAP_USER                    → imap.user                  (plain)
//	IMAP_PASS                    → imap.pass                  (SECRET, secretcrypto.Box)
//	IMAP_TLS                     → imap.tls                   (plain)
//	IMAP_FOLDER                  → imap.folder                (plain)
//	IMAP_PROCESSED_FOLDER        → imap.processed_folder      (plain)
//	IMAP_ERROR_FOLDER            → imap.error_folder          (plain)
//	IMAP_POLL_INTERVAL_SEC       → imap.polling_interval_sec  (plain)
//	IMAP_MAX_MAIL_SIZE_MB        → imap.max_mail_size_mb      (plain)
//	IMAP_MAX_ATTACHMENT_SIZE_MB  → imap.max_attachment_size_mb (plain)
//	IMAP_ENABLED                 → imap.enabled               (plain)
//	IMAP_RATE_LIMIT_PER_HOUR     → imap.rate_limit_per_hour   (plain, F-370)
func BootstrapIMAPEnv(ctx context.Context, cfg *storage.ConfigRepo, log func(string, ...any)) error {
	if cfg == nil {
		return nil
	}
	mappings := []struct {
		env    string
		key    string
		secret bool
	}{
		{"IMAP_HOST", "imap.host", false},
		{"IMAP_PORT", "imap.port", false},
		{"IMAP_USER", "imap.user", false},
		{"IMAP_PASS", "imap.pass", true},
		{"IMAP_TLS", "imap.tls", false},
		{"IMAP_FOLDER", "imap.folder", false},
		{"IMAP_PROCESSED_FOLDER", "imap.processed_folder", false},
		{"IMAP_ERROR_FOLDER", "imap.error_folder", false},
		{"IMAP_POLL_INTERVAL_SEC", "imap.polling_interval_sec", false},
		{"IMAP_MAX_MAIL_SIZE_MB", "imap.max_mail_size_mb", false},
		{"IMAP_MAX_ATTACHMENT_SIZE_MB", "imap.max_attachment_size_mb", false},
		{"IMAP_ENABLED", "imap.enabled", false},
		{"IMAP_RATE_LIMIT_PER_HOUR", "imap.rate_limit_per_hour", false},
	}
	for _, m := range mappings {
		v := os.Getenv(m.env)
		if v == "" {
			continue
		}
		if existing, err := cfg.Get(ctx, m.key); err == nil && existing.Value != "" {
			continue
		}
		if m.secret && !cfg.SecretsEnabled() {
			log("aboard-server: %s gesetzt, aber ABOARD_SECRET_KEY fehlt — Secret nicht importiert", m.env)
			continue
		}
		if err := cfg.Set(ctx, m.key, v, m.secret, ""); err != nil {
			return fmt.Errorf("imap-bootstrap %s: %w", m.key, err)
		}
	}
	return nil
}

// parseIMAPBool tolerant — "true"/"1"/"yes" → true, "false"/"0"/"no" → false,
// alles andere → fallback.
func parseIMAPBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}
