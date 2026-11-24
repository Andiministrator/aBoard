package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func newTestConfigRepo(t *testing.T, withBox bool) (*ConfigRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var box *secretcrypto.Box
	if withBox {
		k := make([]byte, secretcrypto.KeySize)
		_, _ = rand.Read(k)
		b, err := secretcrypto.New(hex.EncodeToString(k))
		if err != nil {
			t.Fatalf("New box: %v", err)
		}
		box = b
	}
	var ctr atomic.Int64
	repo := NewConfigRepo(db, box, func() string {
		return "audit-" + strconv.FormatInt(ctr.Add(1), 10)
	})
	return repo, func() { db.Close() }
}

func TestConfigRepo_SetGetPlain(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	if err := repo.Set(ctx, "smtp.host", "mail.example.org", false, "user-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, err := repo.Get(ctx, "smtp.host")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v.Value != "mail.example.org" || v.IsSecret {
		t.Errorf("erwartet plain 'mail.example.org', got %+v", v)
	}
	if v.UpdatedBy != "user-1" {
		t.Errorf("UpdatedBy = %q", v.UpdatedBy)
	}
}

func TestConfigRepo_SetGetSecret(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	const secret = "s3cr3t-passwort" // abzug:testwert
	if err := repo.Set(ctx, "smtp.pass", secret, true, "user-1"); err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	v, err := repo.Get(ctx, "smtp.pass")
	if err != nil {
		t.Fatalf("Get secret: %v", err)
	}
	if !v.IsSecret {
		t.Error("erwartet IsSecret=true")
	}
	if v.Value != secret {
		t.Errorf("Get-Roundtrip-Secret: %q != %q", v.Value, secret)
	}
}

func TestConfigRepo_List_RedactedSecrets(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	_ = repo.Set(ctx, "smtp.host", "mail.example.org", false, "")
	_ = repo.Set(ctx, "smtp.pass", "geheim", true, "")
	_ = repo.Set(ctx, "imap.host", "imap.example.org", false, "")

	entries, err := repo.List(ctx, "smtp")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List(smtp): %d Einträge, erwartet 2", len(entries))
	}
	for _, e := range entries {
		if e.Key == "smtp.pass" {
			if e.Value != "" {
				t.Errorf("Secret-Klartext im List-Output: %q", e.Value)
			}
			if !e.IsSet || !e.IsSecret {
				t.Errorf("smtp.pass IsSet=%v IsSecret=%v", e.IsSet, e.IsSecret)
			}
		}
		if e.Key == "smtp.host" && e.Value != "mail.example.org" {
			t.Errorf("Plain-Wert verloren: %q", e.Value)
		}
	}

	all, _ := repo.List(ctx, "")
	if len(all) != 3 {
		t.Errorf("List(all): %d, erwartet 3", len(all))
	}
}

func TestConfigRepo_Update_PreservesAudit(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	_ = repo.Set(ctx, "smtp.host", "v1", false, "user-1")
	_ = repo.Set(ctx, "smtp.host", "v2", false, "user-2")

	v, _ := repo.Get(ctx, "smtp.host")
	if v.Value != "v2" || v.UpdatedBy != "user-2" {
		t.Errorf("nach Update: %+v", v)
	}

	audit, err := repo.Audit(ctx, "", 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if len(audit) != 2 {
		t.Fatalf("Audit-Einträge: %d, erwartet 2", len(audit))
	}
	// DESC-Sortierung: erster Eintrag ist der jüngste.
	if audit[0].NewValueRedacted != "v2" || audit[1].NewValueRedacted != "v1" {
		t.Errorf("Audit-Sortierung falsch: %+v", audit)
	}
	if audit[0].OldValueRedacted != "v1" {
		t.Errorf("Audit-Diff verloren: old=%q", audit[0].OldValueRedacted)
	}
}

func TestConfigRepo_Audit_RedactsSecretValues(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	_ = repo.Set(ctx, "smtp.pass", "alter-pass", true, "u1")
	_ = repo.Set(ctx, "smtp.pass", "neuer-pass", true, "u2")

	audit, _ := repo.Audit(ctx, "", 0)
	for _, e := range audit {
		if e.NewValueRedacted != "***" {
			t.Errorf("Secret-Audit nicht redacted: new=%q", e.NewValueRedacted)
		}
		// old kann leer sein (erste Mutation) oder '***' (Update). Klartext
		// darf NIE auftauchen.
		if e.OldValueRedacted != "" && e.OldValueRedacted != "***" {
			t.Errorf("Secret-Audit-Old leakt: %q", e.OldValueRedacted)
		}
	}
}

func TestConfigRepo_Delete_Idempotent(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	_ = repo.Set(ctx, "smtp.host", "v", false, "u")
	if err := repo.Delete(ctx, "smtp.host", "u"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, "smtp.host", "u"); err != nil {
		t.Fatalf("Delete (zweites Mal): %v", err)
	}
	_, err := repo.Get(ctx, "smtp.host")
	if !errors.Is(err, ErrConfigKeyNotFound) {
		t.Errorf("erwartet ErrConfigKeyNotFound, got %v", err)
	}

	// Nur EIN Audit-Eintrag (zweites Delete ist No-op, kein Audit).
	audit, _ := repo.Audit(ctx, "", 0)
	deleteCount := 0
	for _, e := range audit {
		if e.Action == AuditActionDelete {
			deleteCount++
		}
	}
	if deleteCount != 1 {
		t.Errorf("delete-audit-count = %d, erwartet 1", deleteCount)
	}
}

func TestConfigRepo_SecretsUnavailable_WithoutBox(t *testing.T) {
	repo, done := newTestConfigRepo(t, false)
	defer done()
	ctx := context.Background()

	if repo.SecretsEnabled() {
		t.Error("SecretsEnabled erwartet false")
	}

	// Plain-Set funktioniert.
	if err := repo.Set(ctx, "imap.host", "imap.x", false, "u"); err != nil {
		t.Fatalf("plain Set: %v", err)
	}

	// Secret-Set scheitert sauber.
	err := repo.Set(ctx, "imap.pass", "geheim", true, "u")
	if !errors.Is(err, ErrSecretsUnavailable) {
		t.Errorf("erwartet ErrSecretsUnavailable, got %v", err)
	}
}

func TestConfigRepo_AppendAudit_GenericActions(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	if err := repo.AppendAudit(ctx, "users/u9", "u1", AuditActionAdminGrant, "", "is_admin=true"); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	audit, _ := repo.Audit(ctx, "", 0)
	if len(audit) != 1 || audit[0].Action != AuditActionAdminGrant {
		t.Errorf("Audit fehlt oder hat falsche Action: %+v", audit)
	}
}

// #306/Sprint-40-Kritiker-Fix [K]P1: Self-Lockout-Race-Schutz.
// SetAdminFlagSelfSafe muss verhindern, dass der letzte Admin entzogen wird —
// auch wenn der Aufrufer NICHT der eigene User ist (Cross-Race).
func TestUserRepo_SetAdminFlagSelfSafe_BlocksLastAdminRevoke(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1','a@x.de','A','t',1)`); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepo(db)
	if err := users.SetAdminFlagSelfSafe(ctx, "u1", false); !errors.Is(err, ErrLastAdminLockout) {
		t.Errorf("erwartet ErrLastAdminLockout, got %v", err)
	}
	// Status unverändert.
	u, _ := users.GetByEmail(ctx, "a@x.de")
	if !u.IsAdmin {
		t.Error("u1 sollte noch Admin sein")
	}
}

func TestUserRepo_SetAdminFlagSelfSafe_OKWennAndererBleibt(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, _ := Open(ctx, Config{DBPath: dbPath})
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1','a@x.de','A','t',1)`)
	_, _ = db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u2','b@x.de','B','t',1)`)
	users := NewUserRepo(db)
	if err := users.SetAdminFlagSelfSafe(ctx, "u1", false); err != nil {
		t.Fatalf("Revoke mit 2 Admins: %v", err)
	}
	count, _ := users.CountAdmins(ctx)
	if count != 1 {
		t.Errorf("nach Revoke: %d Admins, erwartet 1", count)
	}
}

func TestUserRepo_SetAdminFlagSelfSafe_GrantOK(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, _ := Open(ctx, Config{DBPath: dbPath})
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1','a@x.de','A','t',1)`)
	_, _ = db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u2','b@x.de','B','t',0)`)
	users := NewUserRepo(db)
	if err := users.SetAdminFlagSelfSafe(ctx, "u2", true); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	count, _ := users.CountAdmins(ctx)
	if count != 2 {
		t.Errorf("nach Grant: %d, erwartet 2", count)
	}
}

func TestConfigRepo_Audit_PaginationSinceLimit(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = repo.Set(ctx, "smtp.host", "v"+strconv.Itoa(i), false, "u")
	}
	audit, _ := repo.Audit(ctx, "", 2)
	if len(audit) != 2 {
		t.Errorf("Limit nicht beachtet: %d", len(audit))
	}
}

// ── ADR-0129 §5/Karte #572: Frist für das Audit-Log ──────────────────────

// TestConfigRepo_EvictAuditExpired_RemovesOldKeepsYoung ist das
// Grundmuster (analog TestActivityRepo_FristLoeschtAltesBehaeltJunges):
// alte Zeilen weg, junge bleiben.
//
// GEGENPROBE: Entfernt man in EvictAuditExpired() den `substr(ts,1,19) < ?`-
// Filter (z. B. ersetzt ihn durch `1=1`), löscht der Aufruf auch den jungen
// Eintrag — der Test wird rot.
func TestConfigRepo_EvictAuditExpired_RemovesOldKeepsYoung(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(auditRetentionDaysEnvVar, "10")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	timeNow = func() time.Time { return base.AddDate(0, 0, -20) }
	if err := repo.AppendAudit(ctx, "smtp.host", "u1", AuditActionSet, "", "alt"); err != nil {
		t.Fatalf("AppendAudit alt: %v", err)
	}

	timeNow = func() time.Time { return base.AddDate(0, 0, -2) }
	if err := repo.AppendAudit(ctx, "smtp.host", "u1", AuditActionSet, "", "jung"); err != nil {
		t.Fatalf("AppendAudit jung: %v", err)
	}

	timeNow = func() time.Time { return base }
	n, err := repo.EvictAuditExpired(ctx)
	if err != nil {
		t.Fatalf("EvictAuditExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("EvictAuditExpired meldet %d, erwartet 1", n)
	}

	entries, err := repo.Audit(ctx, "", 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	werte := map[string]bool{}
	for _, e := range entries {
		werte[e.NewValueRedacted] = true
	}
	if werte["alt"] {
		t.Error("alter Audit-Eintrag (20 Tage) hätte bei Frist 10 Tage gelöscht werden müssen")
	}
	if !werte["jung"] {
		t.Error("junger Audit-Eintrag (2 Tage) hätte bei Frist 10 Tage bleiben müssen")
	}
}

// TestConfigRepo_EvictAuditExpired_TriggerFormat belegt, dass der Trim auch
// mit dem ts-Format des SQL-Triggers aus Migration 0018
// (agent_tokens_mit_board_geloescht, strftime('%Y-%m-%dT%H:%M:%fZ','now') —
// FESTE Breite mit 3-stelligen Millisekunden) korrekt vergleicht, nicht nur
// mit dem Go-seitigen nowISO()-Format (RFC3339Nano, VARIABLE Breite). Der
// Trigger selbst lässt sich hier ohne echte Board-Löschung nicht auslösen —
// die Zeile wird deshalb direkt im Trigger-Format eingefügt, wie es der
// echte Trigger schreiben würde.
//
// GEGENPROBE: Ersetzt man in EvictAuditExpired() `substr(ts,1,19) < ?` durch
// einen vollen String-Vergleich `ts < ?` gegen einen mit nowISO()-Format
// (RFC3339Nano) erzeugten Cutoff, bleibt die alte Trigger-Zeile stehen —
// dieser Test wird rot.
func TestConfigRepo_EvictAuditExpired_TriggerFormat(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	altTrigger := base.AddDate(0, 0, -20).Format("2006-01-02T15:04:05.000") + "Z"
	jungTrigger := base.AddDate(0, 0, -2).Format("2006-01-02T15:04:05.000") + "Z"

	insert := func(id, ts string) {
		if _, err := repo.db.ExecContext(ctx,
			`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
			 VALUES (?, 'smtp.host', '', 'set', NULL, 'x', ?)`, // Trigger-ts-Format, aber eine befristete Aktion: die Trigger-Aktion selbst ist seit K-B3 ausgenommen (TestConfigRepo_EvictAuditExpired_AusnahmenBleiben)
			id, ts); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("trig-alt", altTrigger)
	insert("trig-jung", jungTrigger)

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(auditRetentionDaysEnvVar, "10")
	timeNow = func() time.Time { return base }

	if _, err := repo.EvictAuditExpired(ctx); err != nil {
		t.Fatalf("EvictAuditExpired: %v", err)
	}

	var altCount, jungCount int
	if err := repo.db.QueryRowContext(ctx, `SELECT count(*) FROM server_config_audit WHERE id = 'trig-alt'`).Scan(&altCount); err != nil {
		t.Fatalf("count trig-alt: %v", err)
	}
	if err := repo.db.QueryRowContext(ctx, `SELECT count(*) FROM server_config_audit WHERE id = 'trig-jung'`).Scan(&jungCount); err != nil {
		t.Fatalf("count trig-jung: %v", err)
	}
	if altCount != 0 {
		t.Error("trig-alt (Trigger-Format, 20 Tage) hätte gelöscht werden müssen")
	}
	if jungCount != 1 {
		t.Error("trig-jung (Trigger-Format, 2 Tage) hätte bleiben müssen")
	}
}

// TestConfigRepo_EvictAuditExpired_KeineFraktionsUnschaerfe belegt denselben
// String-Vergleichs-Fallstrick wie TestActivityRepo_Frist_KeineFraktionsUnschaerfe
// (activities_test.go), hier für server_config_audit: nowISO() (RFC3339Nano)
// lässt Nachkommastellen weg, wenn sie exakt Null sind. Ein Eintrag 0,5 s
// NACH dem Cutoff, aber in derselben Sekunde, trägt einen Sekundenbruchteil
// im ts und darf NICHT gelöscht werden.
//
// GEGENPROBE (belegt): Ersetzt man in EvictAuditExpired() den Vergleich
// `substr(ts, 1, 19) < ?` durch einen vollen String-Vergleich `ts < ?`
// gegen einen Cutoff mit angehängtem "Z" (kein voller RFC3339Nano-Cutoff
// nötig, "Z" allein genügt für den Beweis: '.' < 'Z' in ASCII), wird dieser
// Test rot — die anderen beiden Audit-Retention-Tests oben bleiben dabei
// GRÜN, weil ihre alt/jung-Paare um volle Tage auseinanderliegen und die
// Grenzfall-Sekunde nie treffen. Erst dieser Test prüft den Fall, den
// activities.go als Grund für substr(...) dokumentiert.
func TestConfigRepo_EvictAuditExpired_KeineFraktionsUnschaerfe(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(auditRetentionDaysEnvVar, "10")

	cutoffSecond := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// 0,5 s NACH dem Cutoff, aber innerhalb derselben Sekunde — trägt einen
	// Sekundenbruchteil im ts (nowISO()/RFC3339Nano).
	entryTime := cutoffSecond.Add(500 * time.Millisecond)

	timeNow = func() time.Time { return entryTime }
	if err := repo.AppendAudit(ctx, "smtp.host", "u1", AuditActionSet, "", "grenzfall"); err != nil {
		t.Fatalf("AppendAudit grenzfall: %v", err)
	}

	// Löst den Trim mit Cutoff = cutoffSecond aus (Frist 10 Tage vorher).
	timeNow = func() time.Time { return cutoffSecond.AddDate(0, 0, 10) }
	if _, err := repo.EvictAuditExpired(ctx); err != nil {
		t.Fatalf("EvictAuditExpired: %v", err)
	}

	entries, err := repo.Audit(ctx, "", 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.NewValueRedacted == "grenzfall" {
			found = true
		}
	}
	if !found {
		t.Error("Eintrag 0,5s NACH dem Cutoff (aber in derselben Sekunde) wurde fälschlich gelöscht — Sekunden-Kürzung fehlt im Vergleich")
	}
}

// TestAuditRetentionDays_WarntNurEinmal (analog
// TestActivityRetentionDays_WarntNurEinmal/TestLLMRetentionDays_WarntNurEinmal,
// F-1148).
func TestAuditRetentionDays_WarntNurEinmal(t *testing.T) {
	auditRetentionWarnungEinmal = sync.Once{}
	defer func() { auditRetentionWarnungEinmal = sync.Once{} }()

	t.Setenv(auditRetentionDaysEnvVar, "abc")
	for i := 0; i < 3; i++ {
		if got := auditRetentionDays(); got != AuditRetentionDaysDefault {
			t.Errorf("Lauf %d: auditRetentionDays() = %d, erwartet Voreinstellung %d", i, got, AuditRetentionDaysDefault)
		}
	}
}

// TestAuditRetentionDays_UngueltigeWerte deckt die Randfälle ab, analog
// TestLLMRetentionDays_UngueltigeWerte.
func TestAuditRetentionDays_UngueltigeWerte(t *testing.T) {
	auditRetentionWarnungEinmal = sync.Once{}
	defer func() { auditRetentionWarnungEinmal = sync.Once{} }()

	if got := auditRetentionDays(); got != AuditRetentionDaysDefault {
		t.Errorf("ohne Env-Var: %d, erwartet %d", got, AuditRetentionDaysDefault)
	}
	for _, raw := range []string{"0", "-5", "nicht-numerisch"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(auditRetentionDaysEnvVar, raw)
			if got := auditRetentionDays(); got != AuditRetentionDaysDefault {
				t.Errorf("%s: %d, erwartet Voreinstellung %d", raw, got, AuditRetentionDaysDefault)
			}
		})
	}
	t.Setenv(auditRetentionDaysEnvVar, "99")
	if got := auditRetentionDays(); got != 99 {
		t.Errorf("gültiger Wert 99 wird nicht übernommen, got %d", got)
	}
}
