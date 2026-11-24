package retention

// scheduler_test.go — Tests für den Retention-Scheduler (ADR-0129 §5,
// Karte #572). Analog notifications/digest_test.go (dieselbe Lehre: Start()
// muss SOFORT aufräumen, nicht erst beim ersten Tick).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

func newSchedulerTestRepos(t *testing.T) (*storage.LLMConversationRepo, *storage.ConfigRepo, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	idGen := func() string { return "audit-id" }
	return storage.NewLLMConversationRepo(db), storage.NewConfigRepo(db, nil, idGen), db
}

// backdateAudit setzt ts einer server_config_audit-Zeile per Direkt-SQL
// zurück — ConfigRepo.AppendAudit schreibt immer mit "jetzt"
// (storage.nowISO(), paketintern, ohne Now-Injektion vom Aufrufer aus).
// Diese Zeit-Injektion für die Frist selbst ist bereits ausführlich in
// storage/server_config_test.go abgedeckt (timeNow-Fixierung); hier auf
// Scheduler-Ebene reicht Direkt-SQL als Testaufbau.
func backdateAudit(t *testing.T, db *sql.DB, newValue, ts string) {
	t.Helper()
	res, err := db.Exec(`UPDATE server_config_audit SET ts = ? WHERE new_value_redacted = ?`, ts, newValue)
	if err != nil {
		t.Fatalf("backdateAudit: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("backdateAudit: %d Zeilen getroffen, erwartet 1", n)
	}
}

// TestScheduler_RunOnce_RaeumtBeideTabellenAuf belegt, dass RunOnce
// tatsächlich BEIDE Repos anfässt — nicht nur eins. Ein LLM-Verlauf, der
// die Frist überschritten hat, UND ein Audit-Eintrag, der das ebenfalls
// tut, müssen nach einem einzigen RunOnce beide weg sein.
//
// GEGENPROBE: Entfernt man in RunOnce() den `s.deps.Audit.EvictAuditExpired`-
// Aufruf (z. B. auskommentiert), bleibt der alte Audit-Eintrag stehen — der
// Test wird an der Audit-Prüfung rot, obwohl die LLM-Prüfung weiter grün
// bleibt (zeigt: die beiden Aufrufe sind unabhängig, ein Test allein hätte
// den fehlenden zweiten Aufruf nicht gefangen).
func TestScheduler_RunOnce_RaeumtBeideTabellenAuf(t *testing.T) {
	llmRepo, auditRepo, db := newSchedulerTestRepos(t)
	ctx := context.Background()

	t.Setenv("ABOARD_LLM_RETENTION_DAYS", "10")
	t.Setenv("ABOARD_AUDIT_RETENTION_DAYS", "10")

	old := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339Nano)

	if _, err := llmRepo.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-alt", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := llmRepo.AppendTurn(ctx, storage.AppendTurnParams{
		ID: "t-alt", ConversationID: "conv-alt", Role: "user", Content: "x", TS: old,
	}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := auditRepo.AppendAudit(ctx, "smtp.host", "u1", storage.AuditActionSet, "", "alt"); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	// AppendAudit schreibt mit nowISO() (also "jetzt") — für den Test muss
	// die Zeile zusätzlich künstlich zurückdatiert werden; ein Zeit-Mock
	// existiert im retention-Paket bewusst nicht (die Scheduler-Deps kennen
	// keine Now-Injektion, die Fristen selbst leben in storage — siehe
	// dortige Tests für die Uhr-Injektion). Direktes Backdaten via SQL
	// bleibt hier zulässig: es ist Testaufbau, kein Produktionscode.
	backdateAudit(t, db, "alt", old)

	s := New(Deps{LLM: llmRepo, Audit: auditRepo, Logger: t.Logf})
	s.RunOnce(ctx)

	if _, err := llmRepo.GetConversation(ctx, "conv-alt"); err == nil {
		t.Error("conv-alt haette per LLM-Retention geloescht sein muessen")
	}

	entries, err := auditRepo.Audit(ctx, "", 0)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	for _, e := range entries {
		if e.NewValueRedacted == "alt" {
			t.Error("alter Audit-Eintrag haette per Audit-Retention geloescht sein muessen")
		}
	}
}

// TestScheduler_Start_RaeumtSofortAuf (analog
// notifications.TestDigestScheduler_StartRaeumtSofortAuf): Start() muss
// sofort aufräumen, nicht erst beim ersten Tick des Intervalls — sonst
// läuft die Eviction bei einem Prozess, der öfter neu startet als das
// Intervall (Normalfall bei Deploys), nie.
//
// Seit F-1135 nach der Startverzögerung, nicht mehr beim Start selbst — siehe
// TestScheduler_Start_WartetAufErstenLauf.
//
// GEGENPROBE: Entfernt man in loop() den ersten `s.RunOnce(ctx)`-Aufruf VOR
// der Ticker-Schleife, bleibt der alte Eintrag über die 3-Sekunden-Deadline
// hinaus stehen (das Default-Intervall ist 24h) — der Test schlägt fehl.
func TestScheduler_Start_RaeumtSofortAuf(t *testing.T) {
	llmRepo, _, _ := newSchedulerTestRepos(t)
	ctx := context.Background()
	t.Setenv("ABOARD_LLM_RETENTION_DAYS", "10")

	old := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339Nano)
	if _, err := llmRepo.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-alt", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := llmRepo.AppendTurn(ctx, storage.AppendTurnParams{
		ID: "t-alt", ConversationID: "conv-alt", Role: "user", Content: "x", TS: old,
	}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	// Default-Intervall 24h — der Tick greift in diesem Test nicht. Die
	// Startverzögerung (F-1135) ist auf 1 ms gestellt: Geprüft wird, dass der
	// erste Lauf NICHT am Intervall hängt.
	s := New(Deps{LLM: llmRepo, Logger: t.Logf, ErsterLaufNach: time.Millisecond})
	cancel := s.Start(ctx)
	defer cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := llmRepo.GetConversation(ctx, "conv-alt"); err != nil {
			return // Start hat aufgeräumt.
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("conv-alt lebt noch — Start() hat nicht sofort aufgeräumt, die Eviction haengt wieder allein am Tick")
}

// TestScheduler_RunOnce_NilRepoWirdUebersprungen belegt, dass ein nil-Repo
// (z. B. wenn nur eine der beiden Tabellen geprüft werden soll) nicht
// paniert.
func TestScheduler_RunOnce_NilRepoWirdUebersprungen(t *testing.T) {
	s := New(Deps{Logger: t.Logf})
	s.RunOnce(context.Background()) // darf nicht paniken
}

// TestScheduler_Start_WartetAufErstenLauf (F-1135, Karte #581): Vor Ablauf
// der Startverzögerung wird nichts gelöscht — eine beim Hochfahren noch
// falsch gehende Uhr soll erst von NTP gestellt werden.
//
// GEGENPROBE (Karte #581): Ruft loop() RunOnce wieder vor dem Timer auf,
// ist conv-alt nach 300 ms weg und der Test wird rot.
func TestScheduler_Start_WartetAufErstenLauf(t *testing.T) {
	llmRepo, _, _ := newSchedulerTestRepos(t)
	ctx := context.Background()
	t.Setenv("ABOARD_LLM_RETENTION_DAYS", "10")

	old := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339Nano)
	if _, err := llmRepo.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-alt", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := llmRepo.AppendTurn(ctx, storage.AppendTurnParams{
		ID: "t-alt", ConversationID: "conv-alt", Role: "user", Content: "x", TS: old,
	}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	s := New(Deps{LLM: llmRepo, Logger: t.Logf, ErsterLaufNach: time.Hour})
	cancel := s.Start(ctx)
	defer cancel()
	time.Sleep(300 * time.Millisecond)
	if _, err := llmRepo.GetConversation(ctx, "conv-alt"); err != nil {
		t.Fatalf("conv-alt vor Ablauf der Startverzögerung gelöscht: %v", err)
	}
}

// TestNew_ErsterLaufNachVoreinstellung: 0 heisst Voreinstellung, nicht „sofort“.
func TestNew_ErsterLaufNachVoreinstellung(t *testing.T) {
	if got := New(Deps{}).deps.ErsterLaufNach; got != DefaultErsterLaufNach {
		t.Fatalf("ErsterLaufNach = %v, erwartet %v", got, DefaultErsterLaufNach)
	}
}

// TestScheduler_RunOnce_WartetAufGestellteUhr (F-1192): Mit nicht gestellter
// Systemuhr löscht der Lauf nichts; ist sie gestellt, holt der nächste es nach.
//
// GEGENPROBE (F-1192): Ohne die uhr.Erlaubt-Prüfung am Anfang von RunOnce ist
// conv-alt schon beim ersten Lauf weg, der Test wird rot.
func TestScheduler_RunOnce_WartetAufGestellteUhr(t *testing.T) {
	llmRepo, _, _ := newSchedulerTestRepos(t)
	ctx := context.Background()
	t.Setenv("ABOARD_LLM_RETENTION_DAYS", "10")
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })

	old := time.Now().UTC().AddDate(0, 0, -20).Format(time.RFC3339Nano)
	if _, err := llmRepo.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-alt", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := llmRepo.AppendTurn(ctx, storage.AppendTurnParams{
		ID: "t-alt", ConversationID: "conv-alt", Role: "user", Content: "x", TS: old,
	}); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{LLM: llmRepo, Logger: t.Logf})

	uhr.Gestellt = func() (bool, error) { return false, nil }
	s.RunOnce(ctx)
	if _, err := llmRepo.GetConversation(ctx, "conv-alt"); err != nil {
		t.Fatal("conv-alt mit nicht gestellter Uhr gelöscht")
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	s.RunOnce(ctx)
	if _, err := llmRepo.GetConversation(ctx, "conv-alt"); err == nil {
		t.Fatal("conv-alt nach dem Stellen der Uhr nicht gelöscht")
	}
}
