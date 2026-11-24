package storage

// Tests zu fristen.go und den Ausnahmen der Audit-Frist (Session-Kritiker
// Sprint 72, K-B1 und K-B3).

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"
)

// K-B1: Ein riesiger Fristwert meinte „nie löschen“ und löschte alles.
func TestFrist_RiesigerWertLoeschtNichts(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()
	llm := NewLLMConversationRepo(repo.db)

	riesig := strconv.FormatInt(int64(^uint64(0)>>1), 10) // MaxInt64
	t.Setenv(auditRetentionDaysEnvVar, riesig)
	t.Setenv(llmRetentionDaysEnvVar, riesig)
	t.Setenv(activityRetentionDaysEnvVar, riesig)

	if err := repo.AppendAudit(ctx, "smtp.host", "u1", AuditActionSet, "", "frisch"); err != nil {
		t.Fatal(err)
	}
	jetzt := nowISO()
	if _, err := repo.db.Exec(`INSERT INTO llm_conversations (id, board_id, card_id, provider, model, created_by, created_at) VALUES ('c1','b','k','anthropic','m','a@x.de',?)`, jetzt); err != nil {
		t.Fatal(err)
	}

	if n, err := repo.EvictAuditExpired(ctx); err != nil || n != 0 {
		t.Errorf("Audit: n=%d err=%v, erwartet 0 gelöscht", n, err)
	}
	if n, err := llm.EvictExpired(ctx); err != nil || n != 0 {
		t.Errorf("LLM: n=%d err=%v, erwartet 0 gelöscht", n, err)
	}
	for name, got := range map[string]int{
		"activity": activityRetentionDays(), "llm": llmRetentionDays(), "audit": auditRetentionDays(),
	} {
		if got > FristTageMax {
			t.Errorf("%s: Frist %d über der Obergrenze %d", name, got, FristTageMax)
		}
	}
}

// Grenzwerte: 36500 gilt, 36501 fällt auf die Voreinstellung zurück.
func TestFrist_Obergrenze(t *testing.T) {
	t.Setenv(auditRetentionDaysEnvVar, "36500")
	if got := auditRetentionDays(); got != 36500 {
		t.Errorf("36500: %d", got)
	}
	t.Setenv(auditRetentionDaysEnvVar, "36501")
	if got := auditRetentionDays(); got != AuditRetentionDaysDefault {
		t.Errorf("36501: %d, erwartet Voreinstellung", got)
	}
}

// Der Stichtag-Schutz: liegt der Stichtag nicht vor „jetzt“, wird nichts
// gelöscht — unabhängig davon, wie der Wert zustande kam.
func TestFristStichtag_NurVorJetzt(t *testing.T) {
	if _, ok := fristStichtag(0); ok {
		t.Error("Frist 0 Tage ergab einen Stichtag — erwartet ok=false")
	}
	if _, ok := fristStichtag(-5); ok {
		t.Error("negative Frist ergab einen Stichtag — erwartet ok=false")
	}
	if s, ok := fristStichtag(1); !ok || len(s) != 19 {
		t.Errorf("Frist 1 Tag: %q ok=%v", s, ok)
	}
}

// K-B3: Einträge, die einen noch geltenden Zustand belegen, überleben die
// Frist; Konfigurationsänderungen nicht; der Lauf protokolliert sich selbst.
func TestConfigRepo_EvictAuditExpired_AusnahmenBleiben(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()
	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(auditRetentionDaysEnvVar, "10")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return base.AddDate(0, 0, -20) }
	if err := repo.AppendAudit(ctx, "smtp.host", "u1", AuditActionSet, "", "alt"); err != nil {
		t.Fatal(err)
	}
	for _, a := range AuditAktionenOhneFrist {
		if err := repo.AppendAudit(ctx, "k", "u1", a, "", "bleibt"); err != nil {
			t.Fatal(err)
		}
	}
	timeNow = func() time.Time { return base }
	n, err := repo.EvictAuditExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, erwartet genau 1 gelöscht (die Konfig-Änderung)", n, err)
	}
	zaehle := func(q string, args ...any) int {
		var c int
		if err := repo.db.QueryRow(q, args...).Scan(&c); err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		return c
	}
	for _, a := range AuditAktionenOhneFrist {
		if zaehle(`SELECT COUNT(*) FROM server_config_audit WHERE action = ?`, a) != 1 {
			t.Errorf("Ausnahme %q wurde gelöscht", a)
		}
	}
	if zaehle(`SELECT COUNT(*) FROM server_config_audit WHERE action = ? AND new_value_redacted LIKE 'geloescht=1 %'`, AuditActionEvict) != 1 {
		t.Error("Aufräumlauf hat keinen eigenen Audit-Eintrag hinterlassen")
	}
}

// F-1582: Die Audit-Einträge der Ausgangsschleuse fallen nicht unter die
// Audit-Frist — sie belegen einen geltenden Zustand (Freigabe, Board-Sperre).
func TestEvictAuditExpired_SchleuseOhneFrist(t *testing.T) {
	repo, done := newTestConfigRepo(t, true)
	defer done()
	ctx := context.Background()
	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(auditRetentionDaysEnvVar, "10")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return base.AddDate(0, 0, -20) }
	aktionen := []string{
		AuditActionSchleuseFreigabeAnlegen, AuditActionSchleuseFreigabeAendern,
		AuditActionSchleuseFreigabeLoeschen, AuditActionSchleuseBoardSperre,
	}
	for _, a := range aktionen {
		if err := repo.AppendAudit(ctx, "schleuse", "u1", a, "", "bleibt"); err != nil {
			t.Fatal(err)
		}
	}
	timeNow = func() time.Time { return base }
	if n, err := repo.EvictAuditExpired(ctx); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, erwartet 0 gelöscht", n, err)
	}
}
