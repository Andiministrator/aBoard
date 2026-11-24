package mail

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestEigenversandFilter(t *testing.T) {
	f := EigenversandFilter("aboard@example.org")
	cases := []struct {
		from string
		drop bool
	}{
		{"aboard@example.org", true},
		{"ABOARD@example.org", true}, // ParseAddress liefert lowercase via ParseMail
		{"other@example.org", false},
		{"", false},
	}
	for _, c := range cases {
		// Wir testen die Filter-Funktion direkt; ParseMail-lowercase wird im
		// processor.go gemacht. Hier setzen wir From manuell auf lowercase.
		from := c.from
		if from != "" {
			from = stringsToLower(from)
		}
		p := &ParsedMail{From: from}
		_, drop := f(p)
		if drop != c.drop {
			t.Errorf("Eigenversand(%q): drop=%v, want %v", c.from, drop, c.drop)
		}
	}

	// Leerer smtpFrom → Filter ist no-op
	f2 := EigenversandFilter("")
	if _, drop := f2(&ParsedMail{From: "any@x.de"}); drop {
		t.Errorf("Leerer smtpFrom sollte nicht droppen")
	}
}

// stringsToLower lokal — Test-Helper, vermeidet strings-Import-Aufblähen.
func stringsToLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestBlocklistFilter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	repo := storage.NewSpamRepo(db)
	_ = repo.AddSpamSender(ctx, "spammer@bad.example", "test")

	f := BlocklistFilter(ctx, repo)

	if _, drop := f(&ParsedMail{From: "spammer@bad.example"}); !drop {
		t.Errorf("blockierter Sender sollte droppen")
	}
	if _, drop := f(&ParsedMail{From: "ok@good.example"}); drop {
		t.Errorf("nicht-gelisteter Sender sollte nicht droppen")
	}
	if _, drop := f(&ParsedMail{From: ""}); drop {
		t.Errorf("leere From sollte nicht droppen")
	}
}

func TestRateLimitFilter(t *testing.T) {
	// 3 pro Stunde, deterministische Zeit
	max := 3
	rl := &rateLimiter{
		max:    max,
		window: time.Hour,
		seen:   map[string][]time.Time{},
	}
	base := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	rl.nowFn = func() time.Time { return base }

	p := &ParsedMail{From: "spammy@x.de"}
	for i := 0; i < max; i++ {
		if _, drop := rl.check(p); drop {
			t.Fatalf("Iteration %d: drop zu früh", i)
		}
	}
	// 4. Mail innerhalb des Fensters → drop
	if _, drop := rl.check(p); !drop {
		t.Errorf("4. Mail sollte droppen (Limit überschritten)")
	}
	// Verschiedener Sender → unabhängig
	if _, drop := rl.check(&ParsedMail{From: "other@x.de"}); drop {
		t.Errorf("anderer Sender sollte unabhängig sein")
	}
	// Nach 1h+1s passiert wieder
	rl.nowFn = func() time.Time { return base.Add(time.Hour + time.Second) }
	if _, drop := rl.check(p); drop {
		t.Errorf("nach Fenster-Ende sollte wieder passieren")
	}
}

func TestRateLimitFilter_Disabled(t *testing.T) {
	f := RateLimitFilter(0) // disabled
	for i := 0; i < 100; i++ {
		if _, drop := f(&ParsedMail{From: "x@y.de"}); drop {
			t.Fatalf("max=0 sollte alle passieren lassen")
		}
	}
}

// F-1578: Absender, deren Mails alle aus dem Fenster gefallen sind, werden
// geräumt; die Grenze für aktive Absender bleibt.
func TestRateLimitFilter_F1578_RaeumtAbgelaufeneAbsender(t *testing.T) {
	rl := &rateLimiter{max: 2, window: time.Hour, seen: map[string][]time.Time{}}
	jetzt := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	rl.nowFn = func() time.Time { return jetzt }

	for i := 0; i < 1000; i++ {
		rl.check(&ParsedMail{From: fmt.Sprintf("fremd-%d@x.de", i)})
	}
	if len(rl.seen) < 900 {
		t.Fatalf("Vorbedingung: %d Absender", len(rl.seen))
	}
	jetzt = jetzt.Add(2 * time.Hour)
	for i := 0; i < 2*rateLimitRaeumAlle; i++ {
		rl.check(&ParsedMail{From: "aktiv@x.de"})
	}
	if n := len(rl.seen); n != 1 {
		t.Fatalf("%d Absender nach Ablauf, erwartet 1 (nur aktiv@x.de)", n)
	}
	if _, drop := rl.check(&ParsedMail{From: "aktiv@x.de"}); !drop {
		t.Fatal("Grenze wirkt nach dem Räumen nicht mehr")
	}
}
