package storage

// server_config.go — Hybrid-Env/DB-Configstore + Audit-Log (Karte #306,
// ADR-0101).
//
// Persistiert runtime-veränderliche Server-Konfiguration. Bootstrap-kritische
// und statisch-geheime Werte (DB-Pfad, Listen-Addr, Master-Encryption-Key)
// bleiben Env-Var-only — diese Schicht deckt die Subsysteme ab, die ohne
// Container-Restart pflegbar sein müssen (SMTP/IMAP/Backup/Service-Board/
// Feature-Toggles).
//
// Section-Konvention: Keys haben dotted Form `<section>.<feldname>`
// (z. B. `smtp.host`, `smtp.port`, `backup.cron`). Die Section ist reines
// Naming, kein Schema-Element — Aufzählung pro Sektion läuft via
// `key LIKE 'smtp.%'`. Das hält das Schema minimal und macht hierarchische
// Erweiterungen (z. B. `smtp.tls.cert_path`) ohne Migration möglich.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

// ConfigValue ist ein gelesener Eintrag aus server_config. Für Secrets ist
// Plain bereits dekryptiert (nur wenn Box != nil im Repo) — Aufrufer mit
// View-Anspruch (z. B. der GET-/admin/config-Handler) lesen via List() in
// die redacted Form um.
type ConfigValue struct {
	Key       string
	Value     string // dekryptierter Plaintext (auch für Secrets)
	IsSecret  bool
	UpdatedBy string // user_id; leer bei Bootstrap-Loader
	UpdatedAt string // ISO-8601
}

// ConfigEntry ist die redaktierte, an Admins ausgelieferte Form. Für Secrets
// ist Value leer und IsSet zeigt nur an, OB ein Wert hinterlegt ist.
type ConfigEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	IsSecret  bool   `json:"is_secret"`
	IsSet     bool   `json:"is_set"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// AuditEntry beschreibt eine Mutations-Spur. Werte sind bereits redacted
// (für is_secret-Keys: '***').
type AuditEntry struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	UserID           string `json:"user_id"`
	Action           string `json:"action"`
	OldValueRedacted string `json:"old_value_redacted,omitempty"`
	NewValueRedacted string `json:"new_value_redacted,omitempty"`
	TS               string `json:"ts"`
}

// Bekannte Audit-Actions. Andere Werte werden NICHT validiert (forward-
// kompatibel: Folge-Karten dürfen neue Actions vergeben).
const (
	AuditActionSet         = "set"
	AuditActionDelete      = "delete"
	AuditActionReload      = "reload"
	AuditActionAdminGrant  = "admin-grant"
	AuditActionAdminRevoke = "admin-revoke"
)

const redactedSecretValue = "***"

// ErrConfigKeyNotFound signalisiert, dass kein Eintrag zum Key existiert.
var ErrConfigKeyNotFound = errors.New("storage: config-Key nicht gefunden")

// ErrKonfigGeheimwert: Eine Folge (KonfigFolge) oder KonfigKlartextTx braucht
// den Klartext eines Eintrags, der als Geheimwert gespeichert ist und nicht
// entschlüsselt werden kann (Sitzungs-Kritik zum Sprint-94-Abschluss N-6,
// F-2272). Vorher bekam die Folge "" — ein Altbestand umging so den Entzug.
var ErrKonfigGeheimwert = errors.New("storage: Einstellung ist als Geheimwert gespeichert und nicht lesbar")

// ErrSecretsUnavailable signalisiert, dass das Repo ohne Crypto-Box läuft
// (kein ABOARD_SECRET_KEY) und ein is_secret-Schreibvorgang verlangt wird.
// Der ConfigStore bleibt nutzbar für Plain-Keys — Secrets brauchen Setup.
var ErrSecretsUnavailable = errors.New("storage: keine Master-Encryption-Key konfiguriert (ABOARD_SECRET_KEY)")

// ConfigRepo verwaltet server_config + server_config_audit.
type ConfigRepo struct {
	db    *sql.DB
	box   *secretcrypto.Box // nil → Secrets nicht verfügbar
	newID func() string
}

// NewConfigRepo legt ein Repo an. box darf nil sein (Read-Only-Modus für
// Secrets); newID erzeugt UUIDs für Audit-IDs (Aufrufer-Konvention, wie
// im UserRepo).
func NewConfigRepo(db *sql.DB, box *secretcrypto.Box, newID func() string) *ConfigRepo {
	return &ConfigRepo{db: db, box: box, newID: newID}
}

// SecretsEnabled zeigt an, ob ein Master-Key konfiguriert ist.
func (r *ConfigRepo) SecretsEnabled() bool { return r.box != nil }

// Get liest einen Konfig-Eintrag. Für Secrets wird automatisch dekryptiert
// — Aufrufer mit View-Verantwortung (Admin-GET) verwenden List() statt.
//
// ErrConfigKeyNotFound wenn der Key nicht existiert.
// ErrSecretsUnavailable wenn der Eintrag is_secret=1 ist, das Repo aber
// keine Crypto-Box hat.
func (r *ConfigRepo) Get(ctx context.Context, key string) (*ConfigValue, error) {
	const q = `
		SELECT key, value_plain, value_encrypted, is_secret,
		       coalesce(updated_by, ''), updated_at
		FROM server_config WHERE key = ?`
	var (
		v      ConfigValue
		plain  sql.NullString
		encB64 sql.NullString
		secInt int
	)
	err := r.db.QueryRowContext(ctx, q, key).Scan(&v.Key, &plain, &encB64, &secInt, &v.UpdatedBy, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConfigKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("config-get %q: %w", key, err)
	}
	v.IsSecret = secInt == 1
	if v.IsSecret {
		if r.box == nil {
			return nil, ErrSecretsUnavailable
		}
		pt, derr := r.box.OpenString(encB64.String)
		if derr != nil {
			return nil, fmt.Errorf("config-get %q: dekrypt: %w", key, derr)
		}
		v.Value = pt
	} else {
		v.Value = plain.String
	}
	return &v, nil
}

// List liefert die redaktierte View aller Keys, optional gefiltert nach
// Section-Prefix (Empty → alles). Secrets werden NIE im Klartext
// ausgeliefert — nur IsSet=true zeigt, dass ein Wert hinterlegt ist.
func (r *ConfigRepo) List(ctx context.Context, section string) ([]ConfigEntry, error) {
	rows, err := r.db.QueryContext(ctx, listQuery(section), listArgs(section)...)
	if err != nil {
		return nil, fmt.Errorf("config-list: %w", err)
	}
	defer rows.Close()
	var out []ConfigEntry
	for rows.Next() {
		var (
			e      ConfigEntry
			plain  sql.NullString
			encB64 sql.NullString
			updBy  sql.NullString
			secInt int
		)
		if err := rows.Scan(&e.Key, &plain, &encB64, &secInt, &updBy, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("config-list scan: %w", err)
		}
		e.IsSecret = secInt == 1
		e.UpdatedBy = updBy.String
		if e.IsSecret {
			e.IsSet = encB64.Valid && encB64.String != ""
			// Value bleibt leer — Klartext NIE ausliefern.
		} else {
			e.IsSet = plain.Valid
			e.Value = plain.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func listQuery(section string) string {
	const base = `SELECT key, value_plain, value_encrypted, is_secret, updated_by, updated_at FROM server_config`
	if section == "" {
		return base + ` ORDER BY key`
	}
	return base + ` WHERE key = ? OR key LIKE ? ORDER BY key`
}

func listArgs(section string) []any {
	if section == "" {
		return nil
	}
	// Exact-Match `smtp` ODER Prefix `smtp.*` — Sektion ohne Suffix lädt
	// auch flat-Keys, falls je benutzt.
	return []any{section, section + ".%"}
}

// Set schreibt einen Eintrag (Insert oder Update) und legt einen Audit-
// Eintrag an. Beides in EINER Transaktion (BEGIN IMMEDIATE — analog
// Bootstrap-Pattern, F-191).
//
// userID darf leer sein (Bootstrap-Loader oder Reload-Pfad). Bei is_secret
// ohne konfigurierte Box → ErrSecretsUnavailable, ohne Schreibversuch.
func (r *ConfigRepo) Set(ctx context.Context, key, value string, isSecret bool, userID string) error {
	return r.SetMitFolge(ctx, key, value, isSecret, userID, nil)
}

// KonfigFolge läuft beim Setzen oder Löschen einer Einstellung in derselben
// Transaktion, vor dem COMMIT (Karte #827 E4: Team-Rolle mit dem Schalter
// boardpaar.rueckweg). alt ist der bisherige Klartext ("" ohne Eintrag oder
// bei einem Geheimwert), neu der neue ("" beim Löschen). Ein Fehler rollt die
// Einstellung mit zurück.
type KonfigFolge func(ctx context.Context, conn *sql.Conn, alt, neu string) error

// KonfigKlartextTx liest den Klartext einer Einstellung auf der Verbindung
// einer laufenden Transaktion (Karte #827 E4). Ohne Eintrag "". Ein Geheimwert
// ist ErrKonfigGeheimwert (F-2272: früher "", das las sich wie „nicht gesetzt“).
func KonfigKlartextTx(ctx context.Context, conn zeilenAbfrager, key string) (string, error) {
	var v sql.NullString
	var geheim sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT value_plain, is_secret FROM server_config WHERE key = ?`, key).Scan(&v, &geheim)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("config-lesen %s: %w", key, err)
	}
	if geheim.Int64 == 1 {
		return "", fmt.Errorf("config-lesen %s: %w", key, ErrKonfigGeheimwert)
	}
	return v.String, nil
}

// SetMitFolge ist Set mit einer Folge in derselben Transaktion (folge darf nil
// sein).
func (r *ConfigRepo) SetMitFolge(ctx context.Context, key, value string, isSecret bool, userID string, folge KonfigFolge) error {
	if isSecret && r.box == nil {
		return ErrSecretsUnavailable
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("config-set conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("config-set begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Bestehenden Eintrag lesen (für Audit-Diff + Insert-vs-Update).
	var (
		oldPlain  sql.NullString
		oldEncB64 sql.NullString
		oldSecret sql.NullInt64
	)
	qerr := conn.QueryRowContext(ctx,
		`SELECT value_plain, value_encrypted, is_secret FROM server_config WHERE key = ?`, key,
	).Scan(&oldPlain, &oldEncB64, &oldSecret)
	exists := qerr == nil
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return fmt.Errorf("config-set lookup: %w", qerr)
	}

	var encB64 sql.NullString
	var plain sql.NullString
	if isSecret {
		ct, serr := r.box.SealString(value)
		if serr != nil {
			return fmt.Errorf("config-set seal: %w", serr)
		}
		encB64 = sql.NullString{String: ct, Valid: true}
	} else {
		plain = sql.NullString{String: value, Valid: true}
	}

	ts := nowISO()
	updBy := sql.NullString{String: userID, Valid: userID != ""}

	if exists {
		if _, err := conn.ExecContext(ctx,
			`UPDATE server_config
			    SET value_plain = ?, value_encrypted = ?, is_secret = ?, updated_by = ?, updated_at = ?
			  WHERE key = ?`,
			plain, encB64, boolToInt(isSecret), updBy, ts, key,
		); err != nil {
			return fmt.Errorf("config-set update: %w", err)
		}
	} else {
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO server_config (key, value_plain, value_encrypted, is_secret, updated_by, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			key, plain, encB64, boolToInt(isSecret), updBy, ts,
		); err != nil {
			return fmt.Errorf("config-set insert: %w", err)
		}
	}

	// Audit-Eintrag — Werte für is_secret-Keys redacted.
	oldRed := redactValue(oldPlain, oldSecret.Int64 == 1, exists)
	newRed := redactValue(plain, isSecret, true)
	if err := r.insertAuditTx(ctx, conn, key, userID, AuditActionSet, oldRed, newRed, ts); err != nil {
		return err
	}
	if folge != nil {
		alt, aerr := r.klartextFuerFolge(oldPlain, oldEncB64, oldSecret.Int64 == 1)
		if aerr != nil {
			return fmt.Errorf("config-set folge %s: %w", key, aerr)
		}
		// F-2272: der neue Wert im Klartext, auch wenn er geheim gespeichert wird.
		if err := folge(ctx, conn, alt, value); err != nil {
			return fmt.Errorf("config-set folge: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("config-set commit: %w", err)
	}
	committed = true
	return nil
}

// Delete entfernt einen Eintrag und logged Audit. Kein Fehler, wenn der
// Key nicht existiert (idempotent).
func (r *ConfigRepo) Delete(ctx context.Context, key, userID string) error {
	return r.DeleteMitFolge(ctx, key, userID, nil)
}

// DeleteMitFolge ist Delete mit einer Folge in derselben Transaktion; sie läuft
// nur, wenn es einen Eintrag gab (folge darf nil sein).
func (r *ConfigRepo) DeleteMitFolge(ctx context.Context, key, userID string, folge KonfigFolge) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("config-delete conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("config-delete begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var (
		oldPlain  sql.NullString
		oldEncB64 sql.NullString
		oldSecret sql.NullInt64
	)
	qerr := conn.QueryRowContext(ctx,
		`SELECT value_plain, value_encrypted, is_secret FROM server_config WHERE key = ?`, key,
	).Scan(&oldPlain, &oldEncB64, &oldSecret)
	if errors.Is(qerr, sql.ErrNoRows) {
		// Idempotent — committen, kein Audit (es gab nichts zu mutieren).
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("config-delete commit (noop): %w", err)
		}
		committed = true
		return nil
	}
	if qerr != nil {
		return fmt.Errorf("config-delete lookup: %w", qerr)
	}

	if _, err := conn.ExecContext(ctx, `DELETE FROM server_config WHERE key = ?`, key); err != nil {
		return fmt.Errorf("config-delete: %w", err)
	}

	oldRed := redactValue(oldPlain, oldSecret.Int64 == 1, true)
	if err := r.insertAuditTx(ctx, conn, key, userID, AuditActionDelete, oldRed, "", nowISO()); err != nil {
		return err
	}
	if folge != nil {
		alt, aerr := r.klartextFuerFolge(oldPlain, oldEncB64, oldSecret.Int64 == 1)
		if aerr != nil {
			return fmt.Errorf("config-delete folge %s: %w", key, aerr)
		}
		if err := folge(ctx, conn, alt, ""); err != nil {
			return fmt.Errorf("config-delete folge: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("config-delete commit: %w", err)
	}
	committed = true
	return nil
}

// klartextFuerFolge: der bisherige Wert für eine Folge — bei einem Geheimwert
// entschlüsselt; geht das nicht (kein Schlüssel, kaputter Wert), bricht der
// Schreibvorgang ab, statt der Folge "" zu geben (F-2272).
func (r *ConfigRepo) klartextFuerFolge(plain, encB64 sql.NullString, geheim bool) (string, error) {
	if !geheim {
		return plain.String, nil
	}
	if r.box == nil {
		return "", ErrKonfigGeheimwert
	}
	pt, err := r.box.OpenString(encB64.String)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrKonfigGeheimwert, err)
	}
	return pt, nil
}

// Audit liest den paginierten Audit-Log. since="" → ohne Untergrenze;
// limit<=0 → 100 (sicherer Default).
func (r *ConfigRepo) Audit(ctx context.Context, since string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	q := `SELECT id, key, user_id, action, coalesce(old_value_redacted,''), coalesce(new_value_redacted,''), ts
	      FROM server_config_audit`
	args := []any{}
	if since != "" {
		q += ` WHERE ts > ?`
		args = append(args, since)
	}
	q += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("config-audit query: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Key, &e.UserID, &e.Action, &e.OldValueRedacted, &e.NewValueRedacted, &e.TS); err != nil {
			return nil, fmt.Errorf("config-audit scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendAudit hängt manuell einen Audit-Eintrag an (z. B. für reload oder
// admin-grant/revoke aus anderen Subsystemen). action und Werte werden
// 1:1 übernommen — der Aufrufer ist für Redaktion verantwortlich.
func (r *ConfigRepo) AppendAudit(ctx context.Context, key, userID, action, oldRedacted, newRedacted string) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("audit-append conn: %w", err)
	}
	defer conn.Close()
	return r.insertAuditTx(ctx, conn, key, userID, action, oldRedacted, newRedacted, nowISO())
}

func (r *ConfigRepo) insertAuditTx(ctx context.Context, conn *sql.Conn, key, userID, action, oldRed, newRed, ts string) error {
	_, err := conn.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.newID(), key, userID, action, nullableString(oldRed), nullableString(newRed), ts,
	)
	if err != nil {
		return fmt.Errorf("audit-insert: %w", err)
	}
	return nil
}

// redactValue liefert den für den Audit-Log sichtbaren Wert. Für is_secret-
// Keys IMMER '***' (auch leer-vorher-leer-nachher → '***', weil das
// Vorhandensein eines Secret-Eintrags an sich schon Info ist; der Diff
// "war gesetzt" vs "ist gesetzt" bleibt aber via action sichtbar).
func redactValue(v sql.NullString, isSecret bool, exists bool) string {
	if !exists {
		return ""
	}
	if isSecret {
		return redactedSecretValue
	}
	return v.String
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AuditRetentionDaysDefault ist die Voreinstellung für die Frist des
// Audit-Logs (server_config_audit), wenn ABOARD_AUDIT_RETENTION_DAYS fehlt
// oder ungültig ist (ADR-0129 §5, Karte #572).
//
// LÄNGER als Aktivitäten- und LLM-Frist (je 365 Tage, s. activities.go bzw.
// llm_conversations.go): server_config_audit ist der NACHWEIS, wer wann
// Server-Konfiguration, Agenten-Tokens (K-13, ADR-0129 §3) oder Admin-
// Rechte geändert hat — ein Jahr wäre für eine nachträgliche Prüfung ("wer
// hat diesen Agenten-Token vor 14 Monaten angelegt, und war das
// gerechtfertigt?") zu knapp, gerade weil das Audit genau für den Fall da
// ist, dass diese Frage erst spät gestellt wird. 730 Tage (2 Jahre)
// orientieren sich an gängigen Aufbewahrungsfristen für Prüf-/Nachweis-
// protokolle, ohne eine bestimmte Rechtsordnung zu behaupten — für einen Team-Betrieb ist
// keine externe Compliance-Vorgabe bekannt (ADR-0129 §5 nennt nur
// "einstellbare Fristen", keine Zahl). Einstellbar per Env-Var, falls sich
// das ändert.
const AuditRetentionDaysDefault = 730

// auditRetentionDaysEnvVar ist der Name der überschreibenden Env-Var.
const auditRetentionDaysEnvVar = "ABOARD_AUDIT_RETENTION_DAYS"

// auditRetentionWarnungEinmal verhindert Log-Flut analog
// activities.go/retentionWarnungEinmal (F-1148).
var auditRetentionWarnungEinmal sync.Once

// auditRetentionDays liest die Frist (in Tagen) aus der Umgebung. Fehlender
// oder ungültiger Wert (keine ganze Zahl, <= 0) fällt auf die Voreinstellung
// zurück — bei ungültigem (aber gesetztem) Wert mit einer einmaligen
// Log-Warnung, analog activityRetentionDays()/llmRetentionDays().
func auditRetentionDays() int {
	raw := os.Getenv(auditRetentionDaysEnvVar)
	if raw == "" {
		return AuditRetentionDaysDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !fristGueltig(n) {
		auditRetentionWarnungEinmal.Do(func() {
			log.Printf("aboard-server: %s=%q ungültig (erwartet ganze Zahl 1–%d), verwende Voreinstellung %d Tage",
				auditRetentionDaysEnvVar, raw, FristTageMax, AuditRetentionDaysDefault)
		})
		return AuditRetentionDaysDefault
	}
	return n
}

// AuditAktionenOhneFrist sind Audit-Aktionen, die einen NOCH GELTENDEN
// Zustand belegen und deshalb von der Frist ausgenommen sind (Session-
// Kritiker Sprint 72, K-B3): wer einen Admin ernannt hat (users führt kein
// granted_by), wer ein Agenten-Token ohne Ablaufdatum angelegt oder
// widerrufen hat, und welche User-ID hinter einem Pseudonym steckte
// (Nutzer-Löschung, Karte #571) — und seit F-1582 die Einträge der
// Ausgangsschleuse (Freigabeliste, Board-Sperre, ADR-0134): wer wann welchen
// Anbieter freigegeben oder ein Board gesperrt hat, belegt einen geltenden
// Zustand. Diese Einträge sind selten; das Audit
// wächst durch die Ausnahme nicht nennenswert. Abgeräumt werden nur
// Konfigurationsänderungen, Reloads und die Aufräum-Einträge selbst.
var AuditAktionenOhneFrist = []string{
	AuditActionAdminGrant, AuditActionAdminRevoke,
	"agent-token-create", "agent-token-revoke", "agent-token-revoke-board-deleted",
	AuditActionUserDelete, AuditActionUserUmzug,
	AuditActionSchleuseFreigabeAnlegen, AuditActionSchleuseFreigabeAendern,
	AuditActionSchleuseFreigabeLoeschen, AuditActionSchleuseBoardSperre,
}

// AuditActionEvict protokolliert einen Aufräumlauf der Audit-Frist.
const AuditActionEvict = "audit-evict"

// EvictAuditExpired löscht server_config_audit-Zeilen älter als die
// konfigurierte Frist (auditRetentionDays, ADR-0129 §5, Karte #572) — außer
// den Aktionen in AuditAktionenOhneFrist. Die Tabelle hat vier Schreiber:
// die Admin-Config-Audit-Einträge dieses Repos (insertAuditTx/AppendAudit),
// das Agenten-Token-Audit (storage/agent_tokens.go, agentAudit), den
// Board-Löschung-Trigger aus Migration 0018 (agent_tokens_mit_board_geloescht)
// und die Nutzer-Löschung (storage/user_loeschung.go). Ein Lauf, der etwas
// löscht, hinterlässt selbst einen Eintrag (AuditActionEvict). Dieser Eintrag
// ist bewusst NICHT ausgenommen: Er belegt einen vergangenen Vorgang, keinen
// geltenden Zustand, und läuft nach derselben Frist ab (Verify Sprint 72, V-3).
//
// ADR-0101 §5 nannte das Audit „append-only, wächst unbegrenzt“; das gilt
// seit Karte #572 nur noch für die ausgenommenen Aktionen (Vermerk in
// ADR-0101 und ADR-0129 §5).
//
// GEMISCHTE ts-FORMATE: Go-seitige Einträge tragen nowISO() (RFC3339Nano,
// variable Breite — Null-Nachkommastellen werden weggelassen), der Trigger
// aus Migration 0018 schreibt dagegen
// strftime('%Y-%m-%dT%H:%M:%fZ','now') (feste 3-stellige Millisekunden).
// Beide Formate stimmen in den ersten 19 Zeichen überein
// ("YYYY-MM-DDTHH:MM:SS"), erst danach weichen sie ab — der hier verwendete
// substr(ts,1,19)-Vergleich (Sekunden-Genauigkeit, analog activities.go
// trim() — dort ausführlich hergeleitet, warum ein voller String-Vergleich
// bei variabler Breite falsch sortiert) ist deshalb für BEIDE Formate
// korrekt, ein voller String-Vergleich wäre es nicht.
//
// Der bestehende Index server_config_audit_ts (Migration 0005, reines `ts`)
// deckt einen Vergleich auf substr(ts,1,19) nicht ab — SQLite nutzt einen
// Index nur für Ausdrücke, die er selbst indiziert (hier keine Ausdrucks-
// Spalte). Bewusst KEIN neuer Ausdrucks-Index: Anders als bei activities
// (F-1157: dort seit Migration 0019 fester Zeitstempel und Index) wächst diese
// Tabelle nur mit Admin-Config-Änderungen und Agenten-Token-Verwaltung, nicht
// mit jedem Op — die Zeilenzahl bleibt um Größenordnungen kleiner.
func (r *ConfigRepo) EvictAuditExpired(ctx context.Context) (int64, error) {
	tage := auditRetentionDays()
	cutoff, ok := fristStichtag(tage)
	if !ok {
		return 0, nil
	}
	args := []any{cutoff}
	platz := make([]string, len(AuditAktionenOhneFrist))
	for i, a := range AuditAktionenOhneFrist {
		platz[i] = "?"
		args = append(args, a)
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM server_config_audit WHERE substr(ts, 1, 19) < ? AND action NOT IN (`+strings.Join(platz, ",")+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("config-audit.evict: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := r.AppendAudit(ctx, "audit", "", AuditActionEvict, "", fmt.Sprintf("geloescht=%d frist_tage=%d", n, tage)); err != nil {
			return n, fmt.Errorf("config-audit.evict: eigener eintrag: %w", err)
		}
	}
	return n, nil
}
