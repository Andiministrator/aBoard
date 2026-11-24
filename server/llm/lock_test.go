package llm

import (
	"sync"
	"testing"
	"time"
)

func TestLockManager_AcquireRelease_Roundtrip(t *testing.T) {
	m := NewLockManager(0)
	lauf, ok, _ := m.Acquire("card-1", "user-a", "anthropic", "claude-opus-4-7")
	if !ok || lauf == "" {
		t.Fatalf("erster Acquire muss erfolgreich sein und eine Lauf-Kennung liefern")
	}
	if _, found := m.Holder("card-1"); !found {
		t.Fatalf("Holder direkt nach Acquire fehlt")
	}
	m.Release("card-1", lauf)
	if _, found := m.Holder("card-1"); found {
		t.Fatalf("nach Release darf kein Holder mehr da sein")
	}
}

func TestLockManager_SecondAcquireBlocked(t *testing.T) {
	m := NewLockManager(0)
	if _, ok, _ := m.Acquire("card-1", "user-a", "anthropic", "claude-opus-4-7"); !ok {
		t.Fatal("erster Acquire muss klappen")
	}
	_, ok, holder := m.Acquire("card-1", "user-b", "openai", "gpt-4o")
	if ok {
		t.Fatalf("zweiter Acquire darf NICHT erfolgreich sein")
	}
	if holder.User != "user-a" || holder.Provider != "anthropic" {
		t.Errorf("Holder falsch: %+v", holder)
	}
}

func TestLockManager_StaleAutoCleanup(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	clock := time.Unix(1_700_000_000, 0).UTC()
	m.SetClock(func() time.Time { return clock })
	if _, ok, _ := m.Acquire("card-1", "user-a", "anthropic", "opus"); !ok {
		t.Fatal("erster Acquire muss klappen")
	}
	// Clock 91 s nach vorne → Stale.
	clock = clock.Add(91 * time.Second)
	_, ok, holder := m.Acquire("card-1", "user-b", "openai", "gpt-4o")
	if !ok {
		t.Fatalf("nach Stale-TTL muss zweiter Acquire klappen, holder=%+v", holder)
	}
	if holder.User != "user-b" {
		t.Errorf("neuer Holder soll user-b sein, got %+v", holder)
	}
}

func TestLockManager_HolderRespectsTTL(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	clock := time.Unix(1_700_000_000, 0).UTC()
	m.SetClock(func() time.Time { return clock })
	_, _, _ = m.Acquire("c", "u", "p", "m")
	if _, ok := m.Holder("c"); !ok {
		t.Fatal("Holder direkt nach Acquire muss zu finden sein")
	}
	clock = clock.Add(91 * time.Second)
	if _, ok := m.Holder("c"); ok {
		t.Fatal("Holder muss nach TTL als nicht-gehalten gelten")
	}
}

func TestLockManager_ReleaseFremdeKennung_NoOp(t *testing.T) {
	m := NewLockManager(0)
	lauf, _, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	m.Release("c", "user-a") // Nutzer-ID statt Kennung: wirkt nicht
	m.Release("c", "")
	m.Release("c", lauf+"x")
	if _, ok := m.Holder("c"); !ok {
		t.Fatal("Release mit fremder/leerer Kennung darf nichts ändern")
	}
	m.Release("c", lauf)
	if _, ok := m.Holder("c"); ok {
		t.Fatal("Release mit eigener Kennung muss freigeben")
	}
}

func TestLockManager_Concurrency(t *testing.T) {
	m := NewLockManager(0)
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	succ := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, ok, _ := m.Acquire("hot-card", "user-x", "p", "m")
			succ <- ok
		}()
	}
	wg.Wait()
	close(succ)
	count := 0
	for ok := range succ {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Errorf("erwartet genau 1 Erfolg unter parallelem Acquire, got %d", count)
	}
}

// testUhr ist eine goroutine-sichere Test-Clock (Halten() liest sie aus
// einer eigenen Goroutine; eine nackte Variable wäre ein Data-Race).
type testUhr struct {
	mu sync.Mutex
	t  time.Time
}

func neueTestUhr() *testUhr { return &testUhr{t: time.Unix(1_700_000_000, 0).UTC()} }

func (u *testUhr) jetzt() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.t
}

func (u *testUhr) vor(d time.Duration) {
	u.mu.Lock()
	u.t = u.t.Add(d)
	u.mu.Unlock()
}

// F-1538 (a): Zwei Läufe DESSELBEN Nutzers auf derselben Karte. Der erste
// lebt länger als die TTL und frischt auf — der zweite Acquire muss
// scheitern, auch nach weit über 90 s.
func TestLockManager_F1538_AuffrischenHaeltUeberTTL(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	uhr := neueTestUhr()
	m.SetClock(uhr.jetzt)
	lauf1, ok, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	if !ok {
		t.Fatal("erster Acquire muss klappen")
	}
	// 5 × 60 s = 300 s Laufzeit (Erstbyte-Frist), alle 60 s aufgefrischt.
	for i := 0; i < 5; i++ {
		uhr.vor(60 * time.Second)
		if !m.Refresh("c", lauf1) {
			t.Fatalf("Refresh #%d des lebenden Laufs scheitert", i+1)
		}
		if _, ok2, holder := m.Acquire("c", "user-a", "anthropic", "opus"); ok2 {
			t.Fatalf("zweiter Lauf desselben Nutzers bekam die Sperre nach %d s, holder=%+v", (i+1)*60, holder)
		}
	}
	if h, ok := m.Holder("c"); !ok || !h.Since.Equal(time.Unix(1_700_000_000, 0).UTC()) {
		t.Errorf("Holder nach Auffrischen: %+v ok=%v — Since muss der Lauf-Start bleiben", h, ok)
	}
}

// F-1538 (a, Takt): Halten() frischt aus eigener Goroutine auf, ohne dass
// der Lauf etwas tut (Wartezeit bis zum ersten Token liefert keine Chunks).
func TestLockManager_F1538_HaltenFrischtImTaktAuf(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	uhr := neueTestUhr()
	m.SetClock(uhr.jetzt)
	lauf, _, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	stop := m.halten("c", lauf, time.Millisecond)
	defer stop()
	for schritt := 1; schritt <= 4; schritt++ {
		uhr.vor(60 * time.Second)
		ziel := uhr.jetzt()
		frist := time.Now().Add(2 * time.Second)
		for {
			m.mu.Lock()
			z := m.locks["c"].zuletzt
			m.mu.Unlock()
			if z.Equal(ziel) {
				break
			}
			if time.Now().After(frist) {
				t.Fatalf("Schritt %d: Halten hat nicht aufgefrischt (zuletzt=%v, Uhr=%v)", schritt, z, ziel)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// 240 s nach Start: ohne Halten wäre die Sperre längst verwaist.
	if _, ok, _ := m.Acquire("c", "user-b", "openai", "gpt"); ok {
		t.Fatal("Sperre trotz Halten nach 240 s übernommen")
	}
	stop()
	stop() // idempotent
}

// F-1538 (b): Der alte Lauf darf beim Ende nicht die Sperre eines neuen
// Laufs desselben Nutzers löschen.
func TestLockManager_F1538_AlterLaufLoeschtNeueSperreNicht(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	uhr := neueTestUhr()
	m.SetClock(uhr.jetzt)
	lauf1, _, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	uhr.vor(91 * time.Second) // Lauf 1 hat nicht aufgefrischt → verwaist
	lauf2, ok, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	if !ok || lauf2 == lauf1 {
		t.Fatalf("Übernahme der verwaisten Sperre: ok=%v lauf1=%q lauf2=%q", ok, lauf1, lauf2)
	}
	m.Release("c", lauf1) // defer des alten Laufs
	if m.Refresh("c", lauf1) {
		t.Error("Refresh mit verdrängter Kennung darf nicht wirken")
	}
	if _, ok := m.Holder("c"); !ok {
		t.Fatal("Release des alten Laufs hat die Sperre des neuen gelöscht")
	}
	if _, ok3, _ := m.Acquire("c", "user-a", "anthropic", "opus"); ok3 {
		t.Fatal("dritter Lauf bekam die Sperre, obwohl Lauf 2 sie hält")
	}
}

// F-1538 (c): Ein abgestürzter Lauf (kein Auffrischen, kein Release) gibt
// die Karte nach TTL wieder frei.
func TestLockManager_F1538_AbgestuerzterLaufWirdFrei(t *testing.T) {
	m := NewLockManager(90 * time.Second)
	uhr := neueTestUhr()
	m.SetClock(uhr.jetzt)
	lauf1, _, _ := m.Acquire("c", "user-a", "anthropic", "opus")
	uhr.vor(60 * time.Second)
	m.Refresh("c", lauf1) // lebte eine Weile …
	uhr.vor(89 * time.Second)
	if _, ok, _ := m.Acquire("c", "user-b", "openai", "gpt"); ok {
		t.Fatal("89 s nach letzter Auffrischung darf die Sperre noch nicht frei sein")
	}
	uhr.vor(2 * time.Second) // … dann nichts mehr: 91 s seit Auffrischung
	if _, ok, h := m.Acquire("c", "user-b", "openai", "gpt"); !ok || h.User != "user-b" {
		t.Fatalf("abgestürzter Lauf hält die Karte über TTL hinaus: ok=%v h=%+v", ok, h)
	}
}
