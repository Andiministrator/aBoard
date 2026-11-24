package mail

// imap_config_test.go — Tests für LoadIMAPConfig, soweit sie ein
// konfigurierbares Verhalten belegen (F-370).

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func imapTestConfigRepo(t *testing.T) (*storage.ConfigRepo, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var n int
	idFn := func() string { n++; return fmt.Sprintf("test-id-%d", n) }
	return storage.NewConfigRepo(db, nil, idFn), ctx
}

// F-370: ADR-0093 §7 führt das Rate-Limit als Konfigurations-Eigenschaft;
// im Code stand es als Literal `RateLimitFilter(20)` in main.go. Der Test
// hält beide Richtungen fest — den Default (der Betreiber, der nichts
// einstellt, bekommt weiterhin 20) und den Override.
func TestLoadIMAPConfig_RateLimitDefault(t *testing.T) {
	cfg, ctx := imapTestConfigRepo(t)
	c := LoadIMAPConfig(ctx, cfg)
	if c.RateLimitPerHour != 20 {
		t.Errorf("RateLimitPerHour = %d, erwartet 20 (ADR-0093 §8-Default)", c.RateLimitPerHour)
	}
}

func TestLoadIMAPConfig_RateLimitOverride(t *testing.T) {
	cfg, ctx := imapTestConfigRepo(t)
	if err := cfg.Set(ctx, "imap.rate_limit_per_hour", "5", false, ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c := LoadIMAPConfig(ctx, cfg)
	if c.RateLimitPerHour != 5 {
		t.Errorf("RateLimitPerHour = %d, erwartet 5 — der Store-Wert wird nicht gelesen", c.RateLimitPerHour)
	}
}

// 0 ist ein gültiger Wert und schaltet das Limit aus (so liest es
// RateLimitFilter). Ohne den expliziten Fall würde eine `n > 0`-Prüfung die
// Null still auf den Default zurückdrehen — der Betreiber hätte das Limit
// abgeschaltet und es liefe weiter.
func TestLoadIMAPConfig_RateLimitNullSchaltetAus(t *testing.T) {
	cfg, ctx := imapTestConfigRepo(t)
	if err := cfg.Set(ctx, "imap.rate_limit_per_hour", "0", false, ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c := LoadIMAPConfig(ctx, cfg)
	if c.RateLimitPerHour != 0 {
		t.Errorf("RateLimitPerHour = %d, erwartet 0 (Limit aus)", c.RateLimitPerHour)
	}
	// Gegenprobe auf der Filter-Seite: 0 lässt alles durch.
	f := RateLimitFilter(c.RateLimitPerHour)
	for i := 0; i < 100; i++ {
		if _, skip := f(&ParsedMail{From: "viel@example.com"}); skip {
			t.Fatalf("Filter hat bei Limit 0 in Runde %d verworfen", i)
		}
	}
}

// Unsinnige Werte dürfen das Limit nicht abschalten — der Default ist die
// sicherere Wahl als „Spam-Schutz versehentlich aus".
func TestLoadIMAPConfig_RateLimitUngueltigBleibtDefault(t *testing.T) {
	for _, wert := range []string{"-1", "viele", "20x"} {
		cfg, ctx := imapTestConfigRepo(t)
		if err := cfg.Set(ctx, "imap.rate_limit_per_hour", wert, false, ""); err != nil {
			t.Fatalf("Set(%q): %v", wert, err)
		}
		if c := LoadIMAPConfig(ctx, cfg); c.RateLimitPerHour != 20 {
			t.Errorf("Wert %q → RateLimitPerHour = %d, erwartet Default 20", wert, c.RateLimitPerHour)
		}
	}
}
