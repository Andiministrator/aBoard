package mail

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// --- Fake-Mailbox ---------------------------------------------------------

// fakeMailbox simuliert eine IMAP-Mailbox in-memory. UIDs werden monoton
// vergeben, FetchRaw liefert die hinterlegten Bytes, UidMove verschiebt die
// UID in eine Ziel-Folder-Map (sichtbar fürs Test).
type fakeMailbox struct {
	mu          sync.Mutex
	uidValidity uint32
	folder      string
	inbox       map[uint32][]byte // UID → rohes Mail-Bytes (UNSEEN)
	processed   map[uint32]string // UID → ZielFolder (gemovte UIDs)
	moveErr     error
	fetchErr    error
	searchErr   error
	loggedOut   bool
}

func newFakeMailbox(uidValidity uint32) *fakeMailbox {
	return &fakeMailbox{
		uidValidity: uidValidity,
		inbox:       map[uint32][]byte{},
		processed:   map[uint32]string{},
	}
}

func (f *fakeMailbox) addMessage(uid uint32, raw []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbox[uid] = raw
}

func (f *fakeMailbox) Capabilities() (map[string]bool, error) {
	return map[string]bool{"IMAP4rev1": true, "MOVE": true}, nil
}

func (f *fakeMailbox) SelectFolder(folder string) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folder = folder
	return f.uidValidity, nil
}

func (f *fakeMailbox) UidSearchUnseen() ([]uint32, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	uids := make([]uint32, 0, len(f.inbox))
	for uid := range f.inbox {
		uids = append(uids, uid)
	}
	// Deterministisch sortieren — Tests sind sonst flaky
	for i := 1; i < len(uids); i++ {
		for j := i; j > 0 && uids[j-1] > uids[j]; j-- {
			uids[j-1], uids[j] = uids[j], uids[j-1]
		}
	}
	return uids, nil
}

func (f *fakeMailbox) FetchRaw(uid uint32) ([]byte, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.inbox[uid]
	if !ok {
		return nil, fmt.Errorf("uid %d not found", uid)
	}
	return raw, nil
}

func (f *fakeMailbox) UidMove(uid uint32, dst string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.inbox, uid)
	f.processed[uid] = dst
	return nil
}

func (f *fakeMailbox) Logout() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedOut = true
	return nil
}

// --- Helfer ---------------------------------------------------------------

func newWorkerTestDeps(t *testing.T, mb *fakeMailbox) (WorkerDeps, *storage.BoardRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	state := storage.NewImapStateRepo(db)
	processed := storage.NewImapProcessedRepo(db)
	seedServiceBoardForTest(t, boards)

	deps := WorkerDeps{
		Config: IMAPConfig{
			Enabled:             true,
			Host:                "imap.example.org",
			Port:                993,
			User:                "u",
			Pass:                "p",
			Folder:              "INBOX",
			ProcessedFolder:     "Processed",
			ErrorFolder:         "Errors",
			PollingInterval:     1 * time.Second,
			MaxMailSizeMB:       25,
			MaxAttachmentSizeMB: 10,
		},
		Boards:              boards,
		State:               state,
		Processed:           processed,
		ServiceBoardEnabled: true,
		Logger:              func(string, ...any) {},
		DialFn:              func(IMAPConfig) (mailbox, error) { return mb, nil },
	}
	cleanup := func() { db.Close() }
	return deps, boards, cleanup
}

// rawSimpleMail erzeugt eine minimale RFC-5322-Mail.
func rawSimpleMail(messageID, subject, from, body string) []byte {
	return []byte(fmt.Sprintf(
		"From: %s\r\nTo: ingest@aboard.local\r\nSubject: %s\r\nMessage-ID: %s\r\nDate: Tue, 21 May 2026 10:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, subject, messageID, body))
}

// --- Tests ----------------------------------------------------------------

func TestPollCycle_ServiceCardForNoMatch(t *testing.T) {
	mb := newFakeMailbox(42)
	mb.addMessage(101, rawSimpleMail("<msg-a@x>", "Neue Sache", "kunde@example.com", "Inhalt"))

	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx := context.Background()

	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle: %v", err)
	}
	if mb.processed[101] != "Processed" {
		t.Errorf("UID 101 sollte in Processed sein, ist %q", mb.processed[101])
	}
	st, err := deps.State.Get(ctx, "INBOX")
	if err != nil {
		t.Fatalf("State.Get: %v", err)
	}
	if st.LastUID != 101 {
		t.Errorf("LastUID = %d, erwartet 101", st.LastUID)
	}
	if st.UIDValidity != 42 {
		t.Errorf("UIDValidity = %d, erwartet 42", st.UIDValidity)
	}
	seen, err := deps.Processed.Seen(ctx, "<msg-a@x>")
	if err != nil || !seen {
		t.Errorf("Message-ID sollte als processed markiert sein (seen=%v err=%v)", seen, err)
	}
}

func TestPollCycle_DuplicateMessageIDOnlyMoves(t *testing.T) {
	mb := newFakeMailbox(7)
	mb.addMessage(50, rawSimpleMail("<dup-1@x>", "Test", "k@x", "Hi"))

	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx := context.Background()

	// Erster Durchlauf — normale Verarbeitung
	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle 1: %v", err)
	}

	// Zweite UID mit derselben Message-ID — soll nur movt werden, keine zweite Karte
	mb.addMessage(60, rawSimpleMail("<dup-1@x>", "Test", "k@x", "Hi"))
	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle 2: %v", err)
	}

	if mb.processed[60] != "Processed" {
		t.Errorf("UID 60 sollte in Processed sein, ist %q", mb.processed[60])
	}

	// Service-Board hat genau eine Karte (die erste); die zweite Mail wurde nicht
	// re-ingested. Wir zählen "displayId" im JSON — pro Karte genau einmal.
	b, err := deps.Boards.Get(ctx, storage.ServiceBoardID)
	if err != nil {
		t.Fatalf("Boards.Get: %v", err)
	}
	if cnt := strings.Count(string(b.StateJSON), `"displayId":`); cnt != 1 {
		t.Errorf("Karten-Anzahl im Service-Board = %d, erwartet 1 (kein Re-Ingest)", cnt)
	}
}

func TestPollCycle_UIDValidityResetClearsLastUID(t *testing.T) {
	mb := newFakeMailbox(100)
	mb.addMessage(5, rawSimpleMail("<a@x>", "S", "k@x", "B"))

	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx := context.Background()

	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle 1: %v", err)
	}

	// Simuliere UIDVALIDITY-Wechsel (Provider-Re-Index)
	mb.uidValidity = 200
	mb.addMessage(1, rawSimpleMail("<b@x>", "S2", "k@x", "B2"))

	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle 2: %v", err)
	}

	st, err := deps.State.Get(ctx, "INBOX")
	if err != nil {
		t.Fatalf("State.Get: %v", err)
	}
	if st.UIDValidity != 200 {
		t.Errorf("UIDValidity nach Reset = %d, erwartet 200", st.UIDValidity)
	}
}

func TestPollCycle_FetchErrorMovesToError(t *testing.T) {
	mb := newFakeMailbox(1)
	mb.addMessage(99, rawSimpleMail("<f@x>", "S", "k@x", "B"))
	mb.fetchErr = errors.New("simulierter Fetch-Fehler")

	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx := context.Background()

	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle: %v", err) // pollCycle propagiert nicht — processOne-Fehler wird zu Move-to-Error
	}
	if mb.processed[99] != "Errors" {
		t.Errorf("UID 99 sollte in Errors sein, ist %q", mb.processed[99])
	}
}

func TestRunWorker_ShutdownOnCancel(t *testing.T) {
	mb := newFakeMailbox(1)
	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	deps.Config.PollingInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = RunWorker(ctx, deps)
		close(doneCh)
	}()

	// Kurz laufen lassen, dann Cancel
	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWorker hat nach Cancel nicht innerhalb 2s zurückgegeben")
	}
}

func TestRunWorker_DisabledShortCircuits(t *testing.T) {
	mb := newFakeMailbox(1)
	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	deps.Config.Enabled = false

	if err := RunWorker(context.Background(), deps); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if mb.folder != "" {
		t.Errorf("SelectFolder hätte nicht aufgerufen werden dürfen, folder=%q", mb.folder)
	}
}

func TestNextBackoff_DoublesUntilMax(t *testing.T) {
	got := initialBackoff
	for i := 0; i < 20; i++ {
		got = nextBackoff(got)
	}
	if got != maxBackoff {
		t.Errorf("Backoff hat maxBackoff nicht erreicht: %v", got)
	}
}

func TestPollCycle_TicketMatchAddsComment(t *testing.T) {
	mb := newFakeMailbox(1)
	deps, repo, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx := context.Background()

	// Service-Board enthält nach Seed bereits keine Karten — eine Karte anlegen,
	// die nach CreateCardInServiceBoard displayId=1 bekommt.
	if _, _, err := CreateCardInServiceBoard(ctx, repo, NewServiceCard{
		Title:       "Vorhandene Karte",
		Description: "Original",
		SenderEmail: "init@x",
		MessageID:   "<init@x>",
	}); err != nil {
		t.Fatalf("Setup-Karte: %v", err)
	}

	// Mail mit Ticket-Tag [aBoard #1] → Match auf die eben angelegte Karte
	mb.addMessage(7, rawSimpleMail("<reply@x>", "Re: [aBoard #1] Nachfrage", "k@x", "Mein Kommentar"))

	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle: %v", err)
	}
	if mb.processed[7] != "Processed" {
		t.Errorf("UID 7 sollte in Processed sein, ist %q", mb.processed[7])
	}
	b2, err := repo.Get(ctx, storage.ServiceBoardID)
	if err != nil {
		t.Fatalf("Boards.Get nach Poll: %v", err)
	}
	if !strings.Contains(string(b2.StateJSON), "Mein Kommentar") {
		t.Errorf("Kommentar nicht in Service-Board-State gefunden")
	}
}

// TestEvictWennFaellig (F-1192): Das Aufräumen wartet auf eine gestellte Uhr
// und wird beim nächsten Poll nachgeholt; danach erst wieder nach 24 h.
// GEGENPROBE: Ohne uhr.Erlaubt in evictWennFaellig läuft der erste Aufruf
// trotz nicht gestellter Uhr — rot.
func TestEvictWennFaellig(t *testing.T) {
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	laeufe := 0
	evict := func(context.Context) (int64, error) { laeufe++; return 0, nil }
	ctx := context.Background()
	t0 := time.Now()
	logf := func(string, ...any) {}

	uhr.Gestellt = func() (bool, error) { return false, nil }
	f := evictWennFaellig(ctx, logf, evict, time.Time{}, t0)
	if laeufe != 0 || !f.IsZero() {
		t.Fatalf("nicht gestellte Uhr: %d Läufe, fällig %v — erwartet 0 und weiter fällig", laeufe, f)
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	f = evictWennFaellig(ctx, logf, evict, f, t0.Add(time.Minute))
	if laeufe != 1 {
		t.Fatalf("gestellte Uhr: %d Läufe, erwartet 1", laeufe)
	}
	f = evictWennFaellig(ctx, logf, evict, f, t0.Add(time.Hour))
	if laeufe != 1 {
		t.Fatalf("vor Ablauf von 24 h erneut gelaufen (%d)", laeufe)
	}
	evictWennFaellig(ctx, logf, evict, f, t0.Add(25*time.Hour))
	if laeufe != 2 {
		t.Fatalf("nach 25 h nicht erneut gelaufen (%d)", laeufe)
	}
}

// TestEvictWennFaellig_FehlerBleibtFaellig (Session-Kritiker Sprint 74,
// F-1202, F-1215): Ein Fehler ist kein erledigter Lauf — nach 15 min wird es
// erneut versucht, nicht erst nach 24 h, aber auch nicht bei jedem Poll.
// GEGENPROBE: Liefert evictWennFaellig nach einem Fehler jetzt+24h, läuft der
// Aufruf nach 16 min nicht — rot; liefert es `faellig`, läuft schon der nach
// 1 min — ebenfalls rot.
func TestEvictWennFaellig_FehlerBleibtFaellig(t *testing.T) {
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return true, nil }
	laeufe := 0
	evict := func(context.Context) (int64, error) { laeufe++; return 0, errors.New("database is locked") }
	t0 := time.Now()
	logf := func(string, ...any) {}
	f := evictWennFaellig(context.Background(), logf, evict, time.Time{}, t0)
	f = evictWennFaellig(context.Background(), logf, evict, f, t0.Add(time.Minute))
	if laeufe != 1 {
		t.Fatalf("%d Läufe nach 1 min, erwartet 1 — ein dauerhafter Fehler darf nicht jeden Poll treffen", laeufe)
	}
	evictWennFaellig(context.Background(), logf, evict, f, t0.Add(16*time.Minute))
	if laeufe != 2 {
		t.Fatalf("%d Läufe nach 16 min, erwartet 2 — nach einem Fehler nicht erst in 24 h", laeufe)
	}
}
