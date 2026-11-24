// Package serviceboard bündelt Bootstrap-Logik und Konfigurations-Loader für
// das eine Service-Board pro Server-Instanz (#294, ADR-0092). Strukturell
// analog zu server/mail (Config aus ConfigStore + Env-Bootstrap), nur ohne
// SMTP-Client — das Service-Board ist reine Storage-Konvention.
//
// Lebenszeit:
//   - Beim Server-Start: BootstrapEnv() importiert ENV-Defaults in den
//     ConfigStore (one-shot, idempotent), dann Seed() legt das Service-Board
//     an, sofern ENABLED=true und es noch nicht existiert.
//   - Zur Laufzeit: LoadConfig() liest die aktuellen service-board.*-Keys.
//     Mutationen laufen über die Admin-API (Sektions-Tab #294 Etappe 3b).
package serviceboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Config trägt die runtime-relevante Service-Board-Konfiguration. Wird beim
// Bootstrap und beim Admin-Reload-Hook gelesen. Die Felder spiegeln die
// ADR-0092 §4 ENV-Variablen.
type Config struct {
	Enabled  bool   // SERVICE_BOARD_ENABLED (default true)
	Title    string // SERVICE_BOARD_TITLE  (default "Service")
	SpamDrop bool   // MAIL_SPAM_DROP       (default false)
}

// Default-Werte aus ADR-0092 §4. Werden auch genutzt, wenn der ConfigStore
// keinen Eintrag liefert — Validate ist hier kein Pflichtschritt (anders als
// SMTP), weil leere Defaults zu einem funktionsfähigen Zustand führen.
const (
	defaultEnabled  = true
	DefaultTitle    = "Service"
	defaultSpamDrop = false
)

// Bekannte ConfigStore-Keys (Section "service-board"). Konvention konsistent
// zu smtp.* / backup.* / autoreply.* (ADR-0101 §3 dotted-Section-Naming).
const (
	KeyEnabled  = "service-board.enabled"
	KeyTitle    = "service-board.title"
	KeySpamDrop = "service-board.spam_drop"
	Section     = "service-board"
)

// LoadConfig liest die service-board.*-Keys aus dem ConfigStore in eine
// Config-Instanz. Fehlende Keys → Default. Boolean-Parsing tolerant
// ("true"/"1" → true, alles andere → false).
func LoadConfig(ctx context.Context, cfg *storage.ConfigRepo) Config {
	c := Config{Enabled: defaultEnabled, Title: DefaultTitle, SpamDrop: defaultSpamDrop}
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
	if v := get(KeyEnabled); v != "" {
		c.Enabled = parseBool(v, defaultEnabled)
	}
	if v := get(KeyTitle); v != "" {
		c.Title = v
	}
	if v := get(KeySpamDrop); v != "" {
		c.SpamDrop = parseBool(v, defaultSpamDrop)
	}
	return c
}

// BootstrapEnv importiert SERVICE_BOARD_*-/MAIL_SPAM_DROP-Env-Vars in den
// ConfigStore, sofern die jeweiligen Keys noch nicht existieren. Idempotent.
// One-shot beim Server-Start (analog mail.BootstrapEnv).
//
// Mapping:
//
//	SERVICE_BOARD_ENABLED → service-board.enabled    (plain)
//	SERVICE_BOARD_TITLE   → service-board.title      (plain)
//	MAIL_SPAM_DROP        → service-board.spam_drop  (plain)
func BootstrapEnv(ctx context.Context, cfg *storage.ConfigRepo, log func(string, ...any)) error {
	if cfg == nil {
		return nil
	}
	mappings := []struct {
		env string
		key string
	}{
		{"SERVICE_BOARD_ENABLED", KeyEnabled},
		{"SERVICE_BOARD_TITLE", KeyTitle},
		{"MAIL_SPAM_DROP", KeySpamDrop},
	}
	for _, m := range mappings {
		v := os.Getenv(m.env)
		if v == "" {
			continue
		}
		if existing, err := cfg.Get(ctx, m.key); err == nil && existing.Value != "" {
			continue
		}
		if err := cfg.Set(ctx, m.key, v, false, ""); err != nil {
			return fmt.Errorf("service-board bootstrap %s: %w", m.key, err)
		}
	}
	return nil
}

// Seed legt das eine Service-Board an, sofern Enabled=true und es noch nicht
// existiert (ADR-0092 §4). Idempotent: ein bereits existierendes Board mit
// id=`service` wird NICHT angefasst (auch nicht der Titel — User-Edits
// bleiben erhalten; Titel-Resync läuft nur beim Re-Test via Admin-UI).
//
// ACL-Bootstrap:
//   - Mail-Worker-System-Identität (storage.MailWorkerEmail) → editor
//     (implizite Schreibrechte für #295 Mail-Ingest + Spam-Pfade).
//   - ABOARD_BOOTSTRAP_OWNER (falls gesetzt) → owner, damit der Admin
//     beim ersten Start sofort verwalten kann. Bestehender owner-Eintrag
//     bleibt unangetastet (idempotent über Upsert).
//
// Fehlertolerant: ACL-Set-Fehler stoppen den Boot nicht (geloggt; Board
// existiert dann ggf. ohne ACL und ist im Legacy-Bypass nutzbar — Admin
// sieht beim Trust=AN-Setup einen 403 und repariert via Share-UX).
func Seed(ctx context.Context, boardRepo *storage.BoardRepo, aclRepo *storage.AclRepo, cfg Config, log func(string, ...any)) error {
	if !cfg.Enabled {
		log("aboard-server: service-board.enabled=false — kein Service-Board angelegt")
		return nil
	}
	title := cfg.Title
	if title == "" {
		title = DefaultTitle
	}
	existing, err := boardRepo.Get(ctx, storage.ServiceBoardID)
	if err != nil && !errors.Is(err, storage.ErrBoardNotFound) {
		return fmt.Errorf("seedServiceBoard: %w", err)
	}
	if existing == nil {
		state := storage.CanonicalServiceBoardState(storage.ServiceBoardID, title)
		if _, err := boardRepo.Insert(ctx, storage.ServiceBoardID, title, "2", state); err != nil {
			return fmt.Errorf("seedServiceBoard: Insert: %w", err)
		}
		log("aboard-server: Service-Board angelegt (id=%s, title=%q, kind=service)", storage.ServiceBoardID, title)
	}

	if aclRepo == nil {
		return nil
	}
	// Mail-Worker als editor — System-Identität, NICHT über Share-UX
	// editierbar (keine Owner-Rolle, kein Admin-Privileg).
	if aerr := aclRepo.Set(ctx, storage.ServiceBoardID, storage.MailWorkerEmail, storage.RoleEditor); aerr != nil {
		log("aboard-server: Service-Board ACL für mail-worker fehlgeschlagen: %v", aerr)
	}
	// Bootstrap-Owner als owner — gleiche Quelle wie seedDefaultBoard
	// (F-256), damit der erste Admin auch das Service-Board verwaltet.
	// F-1862: dieselbe Plausibilitätsprüfung wie beim Default-Board —
	// bis dahin landete jeder Tippfehler als Owner im ACL.
	if owner, gesetzt, ok := storage.BootstrapOwnerAusEnv(); gesetzt && !ok {
		log("aboard-server: FEHLER: ABOARD_BOOTSTRAP_OWNER=%q ist keine gültige Personenadresse — kein Owner-Eintrag am Service-Board", owner)
	} else if ok {
		if aerr := aclRepo.Set(ctx, storage.ServiceBoardID, owner, storage.RoleOwner); aerr != nil {
			log("aboard-server: Service-Board ACL für bootstrap-owner fehlgeschlagen (%q): %v", owner, aerr)
		}
	}
	return nil
}

func parseBool(v string, fallback bool) bool {
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return fallback
}
