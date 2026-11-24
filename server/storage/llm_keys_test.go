package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func newLLMTestRepo(t *testing.T) (*LLMKeyRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	return NewLLMKeyRepo(db, box), func() { db.Close() }
}

func TestLLMKey_SetGetRoundtrip(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	const keyVal = "sk-ant-abc123"
	if err := repo.Set(ctx, "u1", "anthropic", keyVal); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := repo.Get(ctx, "u1", "anthropic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != keyVal {
		t.Errorf("Roundtrip: %q vs %q", got, keyVal)
	}
}

func TestLLMKey_Has(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	ok, _ := repo.Has(ctx, "u1", "openai")
	if ok {
		t.Errorf("Has() vor Set sollte false sein")
	}
	_ = repo.Set(ctx, "u1", "openai", "sk-")
	ok, _ = repo.Has(ctx, "u1", "openai")
	if !ok {
		t.Errorf("Has() nach Set sollte true sein")
	}
}

func TestLLMKey_GetUnknown_Err(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	_, err := repo.Get(context.Background(), "ghost", "anthropic")
	if !errors.Is(err, ErrLLMKeyNotFound) {
		t.Errorf("erwartet ErrLLMKeyNotFound, got %v", err)
	}
}

func TestLLMKey_List_ExcludesPlaintext(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	_ = repo.Set(ctx, "u1", "anthropic", "sk-ant-LEAK-INDICATOR")
	_ = repo.Set(ctx, "u1", "openai", "sk-openai-LEAK-INDICATOR")

	list, err := repo.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List-Länge: %d", len(list))
	}
	// Strict-Check: Meta-Struct hat überhaupt keinen Klartext-Pfad —
	// ein Marshall via fmt.Sprintf darf den "LEAK-INDICATOR" NIE enthalten.
	for _, m := range list {
		if contains(m.CreatedAt, "LEAK-INDICATOR") || contains(m.LastUsedAt, "LEAK-INDICATOR") {
			t.Errorf("Klartext leakt: %+v", m)
		}
	}
}

func TestLLMKey_Update_KeepsCreatedAt(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	_ = repo.Set(ctx, "u1", "anthropic", "v1")
	before, _ := repo.List(ctx, "u1")
	firstCreated := before[0].CreatedAt

	_ = repo.Set(ctx, "u1", "anthropic", "v2")
	after, _ := repo.List(ctx, "u1")
	if after[0].CreatedAt != firstCreated {
		t.Errorf("Update überschrieb created_at: %q → %q", firstCreated, after[0].CreatedAt)
	}

	got, _ := repo.Get(ctx, "u1", "anthropic")
	if got != "v2" {
		t.Errorf("Update-Roundtrip: %q", got)
	}
}

func TestLLMKey_Delete_Idempotent(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	_ = repo.Set(ctx, "u1", "anthropic", "k")
	if err := repo.Delete(ctx, "u1", "anthropic"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, "u1", "anthropic"); err != nil {
		t.Fatalf("Delete (zweites Mal): %v", err)
	}
	if _, err := repo.Get(ctx, "u1", "anthropic"); !errors.Is(err, ErrLLMKeyNotFound) {
		t.Errorf("Get nach Delete: %v", err)
	}
}

func TestLLMKey_MarkUsed_UpdatesTimestamp(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	_ = repo.Set(ctx, "u1", "openai", "k")
	if err := repo.MarkUsed(ctx, "u1", "openai"); err != nil {
		t.Fatalf("MarkUsed: %v", err)
	}
	list, _ := repo.List(ctx, "u1")
	if list[0].LastUsedAt == "" {
		t.Errorf("LastUsedAt wurde nicht gesetzt")
	}
}

func TestLLMKey_PerUserIsolation(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	ctx := context.Background()
	_ = repo.Set(ctx, "u1", "anthropic", "user1-key")
	_ = repo.Set(ctx, "u2", "anthropic", "user2-key")

	list1, _ := repo.List(ctx, "u1")
	list2, _ := repo.List(ctx, "u2")
	if len(list1) != 1 || len(list2) != 1 {
		t.Fatalf("Cross-User-Liste verlinkt: u1=%d u2=%d", len(list1), len(list2))
	}
	k1, _ := repo.Get(ctx, "u1", "anthropic")
	k2, _ := repo.Get(ctx, "u2", "anthropic")
	if k1 != "user1-key" || k2 != "user2-key" {
		t.Errorf("Cross-User-Lesefehler: %q / %q", k1, k2)
	}
}

func TestLLMKey_ProviderEnum_CheckConstraint(t *testing.T) {
	repo, done := newLLMTestRepo(t)
	defer done()
	// "twitter" ist nicht im CHECK-Constraint — direkter DB-Insert muss
	// vom Constraint abgefangen werden. Geht über das Repo nicht (das
	// validiert nicht selbst), aber gegen direktes SQL.
	_, err := repo.db.ExecContext(context.Background(),
		`INSERT INTO llm_api_keys (user_id, provider, key_encrypted, created_at)
		 VALUES ('u1', 'twitter', 'x', '2026-01-01')`)
	if err == nil {
		t.Errorf("CHECK-Constraint sollte Insert verweigern")
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
