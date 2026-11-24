package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ── Test-Hilfsmittel ─────────────────────────────────────────────────

// fakeSender ist ein Sender-Stub, der jeden Send in einer Slice protokolliert.
// Wenn FailUntil > 0 ist, schlägt SendOne die ersten N Aufrufe fehl.
type fakeSender struct {
	channel string
	mu      sync.Mutex
	sends   []sendCall
	fail    atomic.Int32 // FailUntil-Counter
}

type sendCall struct {
	userID string
	email  string
	event  Event
}

func (f *fakeSender) Channel() string { return f.channel }

func (f *fakeSender) SendOne(ctx context.Context, userID, email, name string, ev Event) error {
	if remaining := f.fail.Load(); remaining > 0 {
		f.fail.Add(-1)
		return ErrFake
	}
	f.mu.Lock()
	f.sends = append(f.sends, sendCall{userID: userID, email: email, event: ev})
	f.mu.Unlock()
	return nil
}

func (f *fakeSender) Sends() []sendCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]sendCall, len(f.sends))
	copy(cp, f.sends)
	return cp
}

// ErrFake ist ein injizierter Sende-Fehler für Tests.
var ErrFake = &fakeError{msg: "fake send error"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

// fakeUserResolver liefert deterministisch Werte für die TestUser-IDs.
type fakeUserResolver struct {
	users map[string]fakeUser
}

type fakeUser struct {
	email string
	name  string
	admin bool
}

func (f fakeUserResolver) Resolve(ctx context.Context, userID string) (string, string, bool, error) {
	u, ok := f.users[userID]
	if !ok {
		return "", "", false, nil
	}
	return u.email, u.name, u.admin, nil
}

// newTestWorker baut Bus + Repos + Worker mit einem injizierten now() und
// einem fakeSender. Liefert auch das Storage-DB-Handle für direkte Inspektion.
func newTestWorker(t *testing.T, now func() time.Time) (*Bus, *Worker, *fakeSender, *storage.NotificationLogRepo, *storage.NotificationSubscriptionRepo) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))

	idGen := func() string {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	subs := storage.NewNotificationSubscriptionRepo(db)
	cfgs := storage.NewNotificationConfigRepo(db, box)
	logs := storage.NewNotificationLogRepo(db, idGen)

	bus := NewBus(t.Logf)
	sender := &fakeSender{channel: ChannelEmail}
	users := fakeUserResolver{users: map[string]fakeUser{
		"u-alice": {email: "alice@example.com", name: "Alice"},
		"u-bob":   {email: "bob@example.com", name: "Bob"},
		"u-carol": {email: "carol@example.com", name: "Carol", admin: true},
	}}
	worker := NewWorker(WorkerDeps{
		Bus:           bus,
		Subscriptions: subs,
		Configs:       cfgs,
		Logs:          logs,
		Users:         users,
		Senders:       []Sender{sender},
		Logger:        t.Logf,
		Now:           now,
	})
	// F-380: Die Lanes sind Goroutinen mit offenen Channels. Ohne Stop()
	// laufen sie über das Testende hinaus — und zwar gegen eine DB, die
	// t.Cleanup gerade schliesst. Stop() steht VOR db.Close() in der
	// Cleanup-Reihenfolge (LIFO: zuletzt registriert läuft zuerst).
	t.Cleanup(worker.Stop)
	return bus, worker, sender, logs, subs
}

// drainBus konsumiert alle anstehenden Events synchron. Wir umgehen Run() +
// Goroutine — handle() ist nicht-exportiert, daher emittieren wir und ziehen
// danach den channel selbst.
//
// F-380 (Karte #377): Seit der Lane-Umstellung reiht `handle` nur noch ein,
// der Versand läuft in der Empfänger-Goroutine. Nach jedem Event wird deshalb
// auf `WaitIdle()` gewartet — pro Event, nicht am Ende: die bestehenden
// Coalescence-/Throttle-Tests setzen voraus, dass Event N vollständig
// geloggt ist, bevor Event N+1 geprüft wird. Genau diese Abfolge hatte der
// alte synchrone Pfad, und sie bleibt damit erhalten.
func drainBus(t *testing.T, bus *Bus, worker *Worker, count int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < count; i++ {
		select {
		case ev := <-bus.Receive():
			worker.handle(ctx, ev)
			worker.WaitIdle()
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("drainBus: timeout waiting for event %d", i)
		}
	}
}

// ── Tests ────────────────────────────────────────────────────────────

func TestWorker_AntiLoop_SkipsActor(t *testing.T) {
	bus, worker, sender, _, subs := newTestWorker(t, nil)
	ctx := context.Background()
	_ = subs.Set(ctx, "u-alice", EventCardCommented, ChannelEmail, true)
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice", // selber Kommentar
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-alice"},
	})
	drainBus(t, bus, worker, 1)
	if got := len(sender.Sends()); got != 0 {
		t.Errorf("Anti-Loop versagt: %d Sends, want 0", got)
	}
}

func TestWorker_DeliversToSubscriber(t *testing.T) {
	bus, worker, sender, _, subs := newTestWorker(t, nil)
	ctx := context.Background()
	_ = subs.Set(ctx, "u-bob", EventCardCommented, ChannelEmail, true)
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	drainBus(t, bus, worker, 1)
	sends := sender.Sends()
	if len(sends) != 1 {
		t.Fatalf("Sends=%d, want 1", len(sends))
	}
	if sends[0].userID != "u-bob" {
		t.Errorf("recipient=%q, want u-bob", sends[0].userID)
	}
}

func TestWorker_CoalescesWithinWindow(t *testing.T) {
	// Worker.Now muss konsistent mit der Realzeit sein, weil storage.nowISO
	// im Log-Insert echte Wall-Clock-Time nutzt. Daher: time.Now() einfrieren.
	fixedNow := time.Now()
	bus, worker, sender, _, subs := newTestWorker(t, func() time.Time { return fixedNow })
	ctx := context.Background()
	_ = subs.Set(ctx, "u-bob", EventCardCommented, ChannelEmail, true)
	// Erstes Event → soll senden + Log-Eintrag schreiben.
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	drainBus(t, bus, worker, 1)
	// Zweites Event direkt danach (kein Now-Wechsel) → coalesced.
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	drainBus(t, bus, worker, 1)
	if got := len(sender.Sends()); got != 1 {
		t.Errorf("Coalescence versagt: %d Sends, want 1", got)
	}
}

func TestWorker_ThrottlesAt30PerHour(t *testing.T) {
	// Worker.Now muss konsistent mit der Realzeit sein, weil storage.nowISO
	// im Log-Insert echte Wall-Clock-Time nutzt. Daher: time.Now() einfrieren.
	fixedNow := time.Now()
	bus, worker, sender, _, subs := newTestWorker(t, func() time.Time { return fixedNow })
	ctx := context.Background()
	_ = subs.Set(ctx, "u-bob", EventCardCommented, ChannelEmail, true)
	// 35 verschiedene Karten — keine Coalescence aktiv, nur Throttling.
	for i := 0; i < 35; i++ {
		bus.Emit(Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-alice",
			CardID:                   "card-" + hex.EncodeToString([]byte{byte(i)}),
			ExplicitRecipientUserIDs: []string{"u-bob"},
		})
	}
	drainBus(t, bus, worker, 35)
	sends := sender.Sends()
	if got := len(sends); got != HourlyChannelLimit {
		t.Errorf("Throttling: %d Sends, want %d", got, HourlyChannelLimit)
	}
}

func TestWorker_DefaultSubscribed_Fallback(t *testing.T) {
	bus, worker, sender, _, _ := newTestWorker(t, nil)
	// KEINE expliziten Subscriptions — fallt auf Default zurück.
	// EventCardCommented = Default ON für Email → u-bob bekommt.
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	drainBus(t, bus, worker, 1)
	if got := len(sender.Sends()); got != 1 {
		t.Errorf("Default-Subscribed-Fallback: %d Sends, want 1", got)
	}
}

func TestWorker_DefaultSubscribed_OptOut(t *testing.T) {
	bus, worker, sender, _, subs := newTestWorker(t, nil)
	ctx := context.Background()
	_ = subs.Set(ctx, "u-bob", EventCardCommented, ChannelEmail, false)
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-alice",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	drainBus(t, bus, worker, 1)
	if got := len(sender.Sends()); got != 0 {
		t.Errorf("Explicit-Opt-Out missed: %d Sends, want 0", got)
	}
}

func TestWorker_MentionedFanOut(t *testing.T) {
	bus, worker, sender, _, _ := newTestWorker(t, nil)
	bus.Emit(Event{
		Type:             EventCardMentioned,
		ActorUserID:      "u-alice",
		CardID:           "card-1",
		MentionedUserIDs: []string{"u-bob", "u-carol"},
	})
	drainBus(t, bus, worker, 1)
	// Card.mentioned = Default ON für Email → beide u-bob + u-carol.
	sends := sender.Sends()
	if got := len(sends); got != 2 {
		t.Fatalf("Mention-FanOut: %d Sends, want 2", got)
	}
}

func TestWorker_LogsCoalescedEvent(t *testing.T) {
	// Worker.Now muss konsistent mit der Realzeit sein, weil storage.nowISO
	// im Log-Insert echte Wall-Clock-Time nutzt. Daher: time.Now() einfrieren.
	fixedNow := time.Now()
	bus, worker, _, logs, subs := newTestWorker(t, func() time.Time { return fixedNow })
	ctx := context.Background()
	_ = subs.Set(ctx, "u-bob", EventCardCommented, ChannelEmail, true)
	// 2 Events zur selben Karte.
	for i := 0; i < 2; i++ {
		bus.Emit(Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-alice",
			CardID:                   "card-1",
			ExplicitRecipientUserIDs: []string{"u-bob"},
		})
	}
	drainBus(t, bus, worker, 2)
	// Erstes ist sent, zweites ist coalesced — beide werden gelogged.
	sentCount, _ := logs.CountSentSince(ctx, "u-bob", ChannelEmail, fixedNow.Add(-time.Hour))
	if sentCount != 1 {
		t.Errorf("sent=%d, want 1", sentCount)
	}
}

func TestEvent_HasCard(t *testing.T) {
	if (Event{CardID: "x"}).HasCard() != true {
		t.Error("HasCard mit CardID: should be true")
	}
	if (Event{}).HasCard() != false {
		t.Error("HasCard ohne CardID: should be false")
	}
}

func TestDefaultSubscribed_Tabellenkonform(t *testing.T) {
	// ADR-0107 §2 — Default-Subscribed-Tabelle.
	cases := []struct {
		event, channel string
		want           bool
	}{
		{EventCardAssigned, ChannelEmail, true},
		{EventCardCommented, ChannelEmail, true},
		{EventCardMentioned, ChannelEmail, true},
		{EventCardMoved, ChannelEmail, false}, // Default off email
		{EventCardMoved, ChannelGotify, true}, // Default on gotify
		{EventCardBlocked, ChannelEmail, true},
		{EventBoardAwaiting, ChannelEmail, true},
		{EventServiceNewCard, ChannelEmail, false},
		{EventSprintClosed, ChannelEmail, false},
		{EventBackupFailed, ChannelEmail, true},
	}
	for _, tc := range cases {
		if got := DefaultSubscribed(tc.event, tc.channel); got != tc.want {
			t.Errorf("DefaultSubscribed(%s, %s) = %v, want %v", tc.event, tc.channel, got, tc.want)
		}
	}
}

func TestValidateGotifyURL(t *testing.T) {
	if err := ValidateGotifyURL("https://gotify.example/"); err != nil {
		t.Errorf("https-url should pass: %v", err)
	}
	if err := ValidateGotifyURL("http://gotify.example"); err == nil {
		t.Error("http should fail")
	}
	if err := ValidateGotifyURL(""); err == nil {
		t.Error("empty should fail")
	}
	if err := ValidateGotifyURL("https://"); err == nil {
		t.Error("no host should fail")
	}
}

func TestGotifyPriority_AllEvents(t *testing.T) {
	// Sanity: keine Event-Type liefert 0.
	for _, et := range AllEventTypes {
		if p := GotifyPriority(et); p == 0 {
			t.Errorf("Priority for %s = 0", et)
		}
	}
	if got := GotifyPriority(EventBackupFailed); got != 8 {
		t.Errorf("BackupFailed prio=%d, want 8", got)
	}
	if got := GotifyPriority(EventBoardAwaiting); got != 7 {
		t.Errorf("Awaiting prio=%d, want 7", got)
	}
}

func TestBus_DropsOnFull(t *testing.T) {
	bus := NewBus(nil)
	// Bus.Receive() konsumiert nichts → Queue füllt sich.
	for i := 0; i < BusBuffer; i++ {
		bus.Emit(Event{Type: EventCardCommented})
	}
	// Nächster Emit blockiert 50ms und dropt. Sollte schnell zurückkommen.
	start := time.Now()
	bus.Emit(Event{Type: EventCardCommented})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("Bus.Emit took too long when full: %v", elapsed)
	}
}

// ── F-380 (Karte #377): Empfänger-Isolation ──────────────────────────
//
// Der Defekt: `sendWithRetry` lief synchron im Bus-Loop. Ein hängender
// SMTP-Server kostete pro Empfänger SendTimeout (15 s) + RetryDelays
// (5 s + 30 s + 2 min) ≈ 155 s, in denen KEIN anderes Event durchkam.

// blockingSender blockiert für bestimmte Empfänger, bis freigegeben wird.
// Alle anderen Empfänger werden sofort bedient.
type blockingSender struct {
	channel  string
	blockFor map[string]chan struct{} // userID → Freigabe-Signal
	mu       sync.Mutex
	sends    []string // userIDs in Zustellreihenfolge
}

func (b *blockingSender) Channel() string { return b.channel }

func (b *blockingSender) SendOne(ctx context.Context, userID, email, name string, ev Event) error {
	if gate, ok := b.blockFor[userID]; ok {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	b.sends = append(b.sends, userID)
	b.mu.Unlock()
	return nil
}

func (b *blockingSender) Sends() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sends...)
}

// newWorkerWithSender wie newTestWorker, aber mit injiziertem Sender.
func newWorkerWithSender(t *testing.T, sender Sender) (*Bus, *Worker, *storage.NotificationSubscriptionRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	idGen := func() string {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	subs := storage.NewNotificationSubscriptionRepo(db)
	bus := NewBus(t.Logf)
	worker := NewWorker(WorkerDeps{
		Bus:           bus,
		Subscriptions: subs,
		Configs:       storage.NewNotificationConfigRepo(db, box),
		Logs:          storage.NewNotificationLogRepo(db, idGen),
		Users: fakeUserResolver{users: map[string]fakeUser{
			"u-alice": {email: "alice@example.com", name: "Alice"},
			"u-bob":   {email: "bob@example.com", name: "Bob"},
			"u-carol": {email: "carol@example.com", name: "Carol"},
		}},
		Senders: []Sender{sender},
		Logger:  t.Logf,
	})
	t.Cleanup(worker.Stop)
	return bus, worker, subs
}

func TestWorker_HaengenderEmpfaengerBlockiertDieAnderenNicht(t *testing.T) {
	gate := make(chan struct{})
	sender := &blockingSender{
		channel:  ChannelEmail,
		blockFor: map[string]chan struct{}{"u-alice": gate},
	}
	bus, worker, _ := newWorkerWithSender(t, sender)
	ctx := context.Background()

	// Ein Event an alle drei — Alice hängt (ihr SMTP antwortet nicht).
	ev := Event{
		Type:                     EventCardMentioned,
		ActorUserID:              "u-zed",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-alice", "u-bob", "u-carol"},
	}
	bus.Emit(ev)
	select {
	case got := <-bus.Receive():
		worker.handle(ctx, got)
	case <-time.After(time.Second):
		t.Fatal("kein Event im Bus")
	}

	// Bob und Carol müssen durchkommen, OHNE dass Alice freigegeben wird.
	// Vor dem Fix wäre hier nichts passiert: handle() hing im SendOne für
	// Alice fest und kam nie zu den beiden anderen.
	deadline := time.After(3 * time.Second)
	for {
		got := sender.Sends()
		if len(got) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Bob/Carol nicht zugestellt, solange Alice haengt: %v", got)
		case <-time.After(5 * time.Millisecond):
		}
	}
	zugestellt := sender.Sends()
	for _, u := range []string{"u-bob", "u-carol"} {
		found := false
		for _, s := range zugestellt {
			if s == u {
				found = true
			}
		}
		if !found {
			t.Errorf("%s fehlt in %v", u, zugestellt)
		}
	}
	for _, s := range zugestellt {
		if s == "u-alice" {
			t.Error("Alice zugestellt, obwohl ihr Sender noch haengt")
		}
	}

	// Freigeben → Alice kommt nach, und der Worker wird sauber leer.
	close(gate)
	worker.WaitIdle()
	if len(sender.Sends()) != 3 {
		t.Errorf("nach Freigabe: %v, want alle drei", sender.Sends())
	}
}

func TestWorker_ReihenfolgeProEmpfaengerBleibtErhalten(t *testing.T) {
	// Der Bus ist FIFO und der Worker lieferte bisher streng sequentiell —
	// ein Empfänger bekam seine Events in Emit-Reihenfolge. Ein allgemeiner
	// Goroutine-Pool hätte das gebrochen; die Lane pro Empfänger nicht.
	sender := &orderSender{channel: ChannelEmail}
	bus, worker, _ := newWorkerWithSender(t, sender)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		bus.Emit(Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-zed",
			CardID:                   fmt.Sprintf("card-%d", i), // eigene Karte → keine Coalescence
			CardTitle:                fmt.Sprintf("T%d", i),
			ExplicitRecipientUserIDs: []string{"u-bob"},
		})
	}
	for i := 0; i < 5; i++ {
		worker.handle(ctx, <-bus.Receive())
	}
	worker.WaitIdle()

	got := sender.Titles()
	want := []string{"T0", "T1", "T2", "T3", "T4"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Reihenfolge verletzt: got %v, want %v", got, want)
		}
	}
}

// orderSender protokolliert die CardTitle in Zustellreihenfolge und
// verzögert leicht, damit eine parallele Abarbeitung auffallen würde.
type orderSender struct {
	channel string
	mu      sync.Mutex
	titles  []string
}

func (o *orderSender) Channel() string { return o.channel }

func (o *orderSender) SendOne(ctx context.Context, userID, email, name string, ev Event) error {
	time.Sleep(time.Millisecond)
	o.mu.Lock()
	o.titles = append(o.titles, ev.CardTitle)
	o.mu.Unlock()
	return nil
}

func (o *orderSender) Titles() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.titles...)
}

func TestWorker_VolleLaneVerwirftSichtbar(t *testing.T) {
	// Drop-Beobachtbarkeit: eine überlaufende Lane darf nicht still
	// verwerfen — Zähler UND notification_log-Eintrag müssen es zeigen.
	gate := make(chan struct{})
	sender := &blockingSender{
		channel:  ChannelEmail,
		blockFor: map[string]chan struct{}{"u-alice": gate},
	}
	_, worker, _ := newWorkerWithSender(t, sender)
	ctx := context.Background()

	// LaneBuffer+2 Events an Alice: eines steckt im SendOne fest, LaneBuffer
	// passen in die Queue, der Rest wird verworfen.
	for i := 0; i < LaneBuffer+3; i++ {
		worker.handle(ctx, Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-zed",
			CardID:                   fmt.Sprintf("card-%d", i),
			ExplicitRecipientUserIDs: []string{"u-alice"},
		})
	}
	if worker.Dropped() == 0 {
		t.Error("kein Drop gezaehlt, obwohl die Lane ueberlaufen musste")
	}
	close(gate)
	worker.WaitIdle()
}

func TestWorker_StopIstIdempotentUndBeendetLanes(t *testing.T) {
	sender := &orderSender{channel: ChannelEmail}
	bus, worker, _ := newWorkerWithSender(t, sender)
	ctx := context.Background()
	bus.Emit(Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-zed",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	worker.handle(ctx, <-bus.Receive())
	worker.WaitIdle()

	worker.Stop()
	worker.Stop() // idempotent — zweiter Aufruf darf nicht auf einen
	worker.Wait() // geschlossenen Channel schliessen (Panic) oder haengen

	// Nach Stop nimmt der Worker nichts mehr an (kein Send auf geschlossenen
	// Channel, kein Panic).
	worker.handle(ctx, Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-zed",
		CardID:                   "card-2",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	if got := len(sender.Titles()); got != 1 {
		t.Errorf("nach Stop noch zugestellt: %d Sends, want 1", got)
	}
}

// TestWorker_EnqueueNachStopKeinPanic fährt enqueue und Stop nebenläufig
// (Sprint-52-Kritiker, K10: vor dem Fix wurde das Lock vor dem Channel-Send
// freigegeben; lief Stop() dazwischen durch, war der Send ein
// `send on closed channel` — ein Panic, den das recover in deliverOne nicht
// abfängt, weil es eine Ebene tiefer sitzt).
//
// EHRLICH VERMESSEN: Mit testweise zurückgebautem Fix bleibt dieser Test
// grün — das Fenster zwischen Unlock und Send ist zu schmal, um es über
// Goroutine-Scheduling zuverlässig zu treffen, und `-race` findet es auch
// nicht (ein Send auf einen geschlossenen Channel ist kein Daten-Race).
// Der Test sichert damit die Abwesenheit von Deadlocks und offensichtlichen
// Regressionen im Stop-Pfad, NICHT den K10-Fix selbst. Dessen Korrektheit
// liegt in der Struktur: Send und close(ch) stehen jetzt unter demselben
// Mutex, das Fenster existiert nicht mehr.
func TestWorker_EnqueueNachStopKeinPanic(t *testing.T) {
	sender := &orderSender{channel: ChannelEmail}
	bus, worker, _ := newWorkerWithSender(t, sender)
	ctx := context.Background()

	ev := Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-zed",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	}
	bus.Emit(ev)
	worker.handle(ctx, <-bus.Receive())
	worker.WaitIdle()

	// Stop und enqueue nebenläufig — ohne den Fix wäre das ein Panic-Fenster.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); worker.Stop() }()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			worker.handle(ctx, Event{
				Type:                     EventCardCommented,
				ActorUserID:              "u-zed",
				CardID:                   fmt.Sprintf("card-%d", i),
				ExplicitRecipientUserIDs: []string{"u-bob"},
			})
		}
	}()
	wg.Wait() // ein Panic hier riesse den Testprozess mit
	worker.WaitIdle()
}

// ── Shutdown() — Produktivpfad für #421/F-521 ─────────────────────────

// capturingLogger sammelt Logger-Aufrufe threadsicher — für Assertions auf
// den Inhalt (t.Logf allein lässt sich nicht auswerten).
type capturingLogger struct {
	mu   sync.Mutex
	logs []string
}

func (c *capturingLogger) logf(format string, args ...interface{}) {
	c.mu.Lock()
	c.logs = append(c.logs, fmt.Sprintf(format, args...))
	c.mu.Unlock()
}

func (c *capturingLogger) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// newWorkerWithLogger wie newWorkerWithSender, aber mit injizierbarem
// Logger — für Assertions auf Shutdown()s Log-Ausgabe (Dropped()-Metrik,
// Timeout-Meldung).
func newWorkerWithLogger(t *testing.T, sender Sender, logger *capturingLogger) *Worker {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	idGen := func() string {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	worker := NewWorker(WorkerDeps{
		Bus:           NewBus(t.Logf),
		Subscriptions: storage.NewNotificationSubscriptionRepo(db),
		Configs:       storage.NewNotificationConfigRepo(db, box),
		Logs:          storage.NewNotificationLogRepo(db, idGen),
		Users: fakeUserResolver{users: map[string]fakeUser{
			"u-alice": {email: "alice@example.com", name: "Alice"},
			"u-bob":   {email: "bob@example.com", name: "Bob"},
		}},
		Senders: []Sender{sender},
		Logger:  logger.logf,
	})
	return worker
}

// TestWorker_RunLoggtVerworfeneEventsBeiCtxAbbruch (F-376, Sprint-42-Kritiker;
// Nacharbeit Sprint 61, Karte #445) — Events, die beim ctx-Abbruch bereits im
// Bus-Channel gepuffert, aber noch nicht verarbeitet sind, dürfen nicht
// spurlos verschwinden. Run() muss sie zählen und loggen (ADR-0107 akzeptiert
// den In-Memory-Verlust selbst, aber ohne Log-Zeile ist er unbeobachtbar).
//
// Deterministisch statt statistisch (Klasse 11 in
// knowledge/topics/claude-projekt-framework.md): ctx wird VOR Run()
// abgebrochen, nicht nebenläufig dazu — sonst würde ein Go-`select` mit zwei
// gleichzeitig bereiten Fällen (ctx.Done() UND gepuffertes Event) pseudo-
// zufällig zwischen beiden entscheiden, und der Test wäre nur manchmal rot.
func TestWorker_RunLoggtVerworfeneEventsBeiCtxAbbruch(t *testing.T) {
	sender := &orderSender{channel: ChannelEmail}
	logger := &capturingLogger{}
	worker := newWorkerWithLogger(t, sender, logger)

	for i := 0; i < 3; i++ {
		worker.deps.Bus.Emit(Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-zed",
			CardID:                   fmt.Sprintf("card-%d", i),
			ExplicitRecipientUserIDs: []string{"u-bob"},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // VOR Run() abgebrochen — Run() darf die drei Events NICHT mehr verarbeiten.

	worker.Run(ctx) // synchron: Run() muss wegen der Vorabprüfung sofort zurückkehren.

	if !logger.contains("3 im Bus gepufferte Event(e)") {
		t.Fatalf("erwartete Log-Zeile über 3 verworfene Events fehlt; Logs: %v", logger.logs)
	}
	if got := sender.Titles(); len(got) != 0 {
		t.Fatalf("Sender hat %d Event(e) zugestellt bekommen — bei bereits abgebrochenem ctx dürfen es 0 sein: %v", len(got), got)
	}
}

// TestDrainAndLog_ZaehltNurWennVorhanden — Gegenprobe zur Log-Bedingung:
// ein leerer Channel darf KEINE Zeile erzeugen (sonst wäre jeder saubere
// Shutdown mit einer irreführenden "0 Events verworfen"-Meldung im Log).
func TestDrainAndLog_ZaehltNurWennVorhanden(t *testing.T) {
	logger := &capturingLogger{}
	ch := make(chan Event, 4)
	n := drainAndLog(ch, logger.logf)
	if n != 0 {
		t.Fatalf("erwartet 0, bekommen %d", n)
	}
	if len(logger.logs) != 0 {
		t.Fatalf("leerer Channel darf keine Log-Zeile erzeugen, bekommen: %v", logger.logs)
	}
}

// TestWorker_ShutdownOhneRestbestandBeendetSofort — Normalfall: keine
// hängenden Sends, Shutdown() meldet true, deutlich unterhalb des Timeouts.
func TestWorker_ShutdownOhneRestbestandBeendetSofort(t *testing.T) {
	sender := &orderSender{channel: ChannelEmail}
	logger := &capturingLogger{}
	worker := newWorkerWithLogger(t, sender, logger)
	ctx := context.Background()

	worker.handle(ctx, Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-zed",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-bob"},
	})
	worker.WaitIdle()

	start := time.Now()
	ok := worker.Shutdown(5 * time.Second)
	elapsed := time.Since(start)
	if !ok {
		t.Fatal("Shutdown meldete Timeout, obwohl kein Restbestand offen war")
	}
	if elapsed > time.Second {
		t.Errorf("Shutdown ohne Restbestand dauerte %s — sollte sofort zurückkehren", elapsed)
	}
	// Kein Drop in diesem Test → keine Dropped-Metrik-Zeile.
	if logger.contains("shutdown —") {
		t.Error("Shutdown loggte eine Dropped-Metrik, obwohl Dropped() == 0 war")
	}
}

// TestWorker_ShutdownMeldetDroppedAlsMetrik — Fall 1 für Dropped(): eine
// übergelaufene Lane muss beim Shutdown als Kennzahl im Log auftauchen, nicht
// nur im Einzel-Drop-Log-Eintrag von enqueue().
func TestWorker_ShutdownMeldetDroppedAlsMetrik(t *testing.T) {
	gate := make(chan struct{})
	sender := &blockingSender{
		channel:  ChannelEmail,
		blockFor: map[string]chan struct{}{"u-alice": gate},
	}
	logger := &capturingLogger{}
	worker := newWorkerWithLogger(t, sender, logger)
	ctx := context.Background()

	// LaneBuffer+3 Events: eines blockiert in SendOne, der Rest läuft in die
	// Lane, der Überschuss wird verworfen (siehe TestWorker_VolleLaneVerwirftSichtbar).
	for i := 0; i < LaneBuffer+3; i++ {
		worker.handle(ctx, Event{
			Type:                     EventCardCommented,
			ActorUserID:              "u-zed",
			CardID:                   fmt.Sprintf("card-%d", i),
			ExplicitRecipientUserIDs: []string{"u-alice"},
		})
	}
	if worker.Dropped() == 0 {
		t.Fatal("kein Drop gezählt — Testaufbau prüft die falsche Sache")
	}
	close(gate) // Rest darf durch, Shutdown soll sauber (true) enden

	ok := worker.Shutdown(5 * time.Second)
	if !ok {
		t.Fatal("Shutdown meldete Timeout — sollte nach close(gate) sauber enden")
	}
	if !logger.contains("shutdown —") {
		t.Error("Shutdown hat die Dropped()-Metrik nicht geloggt")
	}
}

// TestWorker_ShutdownTimeoutBeiHaengendemEmpfaenger — Gegenprobe zur
// F-380-Auflage: ein Empfänger, der nie antwortet, darf Shutdown() nicht
// unbegrenzt aufhalten. Erwartet false innerhalb des übergebenen Timeouts,
// deutlich unter dem worst-case (~155 s, SendTimeout+RetryDelays).
func TestWorker_ShutdownTimeoutBeiHaengendemEmpfaenger(t *testing.T) {
	gate := make(chan struct{}) // wird in diesem Test absichtlich NICHT vor Shutdown() geschlossen
	sender := &blockingSender{
		channel:  ChannelEmail,
		blockFor: map[string]chan struct{}{"u-alice": gate},
	}
	logger := &capturingLogger{}
	worker := newWorkerWithLogger(t, sender, logger)
	ctx := context.Background()

	worker.handle(ctx, Event{
		Type:                     EventCardCommented,
		ActorUserID:              "u-zed",
		CardID:                   "card-1",
		ExplicitRecipientUserIDs: []string{"u-alice"},
	})
	// Kurz warten, bis der Sub-Queue-Worker den Event tatsächlich abgeholt
	// hat und in SendOne auf `gate` hängt — sonst könnte das Event noch
	// unversendet in der Lane liegen, wenn Shutdown() startet (auch das wäre
	// blockierend, aber wir wollen gezielt den hängenden Sende-Fall prüfen).
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	ok := worker.Shutdown(80 * time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("Shutdown meldete Erfolg, obwohl der Empfänger nie freigegeben wurde")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Shutdown-Timeout griff nicht rechtzeitig: elapsed=%s, timeout war 80ms", elapsed)
	}
	if !logger.contains("shutdown-timeout") {
		t.Error("Shutdown hat den Timeout-Fall nicht geloggt")
	}

	// Aufräumen: Empfänger freigeben und auf das Ende der Lane warten, damit
	// kein Logger-Aufruf aus einer Hintergrund-Goroutine nach Testende
	// erfolgt (t.Logf nach Testende panict).
	close(gate)
	worker.Wait()
}
