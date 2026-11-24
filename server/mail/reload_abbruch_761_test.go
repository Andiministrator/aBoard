package mail

// V-1 (Verify Sprint 86): Ein Reload/Shutdown bricht den Worker-Context ab,
// WÄHREND eine Mail bearbeitet wird. Die angefangene Mail muss trotzdem zu
// Ende laufen (Karte, MarkProcessed, Move nach Processed) — das Stopp-Signal
// gilt nur zwischen zwei UIDs.

import (
	"context"
	"testing"
)

type abbrechendeMailbox struct {
	*fakeMailbox
	cancel func()
}

func (a *abbrechendeMailbox) FetchRaw(uid uint32) ([]byte, error) {
	raw, err := a.fakeMailbox.FetchRaw(uid)
	a.cancel() // Reload: stop() → cancel() trifft mitten in die Verarbeitung
	return raw, err
}

func TestPollCycle_AbbruchMitteninMail_MailWirdZuEndeVerarbeitet(t *testing.T) {
	mb := newFakeMailbox(42)
	mb.addMessage(101, rawSimpleMail("<msg-v1@x>", "Neue Sache", "kunde@example.com", "Inhalt"))
	mb.addMessage(102, rawSimpleMail("<msg-v2@x>", "Zweite Sache", "kunde@example.com", "Inhalt"))
	deps, _, done := newWorkerTestDeps(t, mb)
	defer done()
	ctx, cancel := context.WithCancel(context.Background())
	am := &abbrechendeMailbox{fakeMailbox: mb, cancel: cancel}
	deps.DialFn = func(IMAPConfig) (mailbox, error) { return am, nil }
	if err := pollCycle(ctx, deps); err != nil {
		t.Fatalf("pollCycle: %v", err)
	}
	if got := mb.processed[101]; got != "Processed" {
		t.Errorf("UID 101 (angefangene Mail) liegt in %q, erwartet Processed", got)
	}
	seen, err := deps.Processed.Seen(context.Background(), "<msg-v1@x>")
	if err != nil || !seen {
		t.Errorf("angefangene Mail nicht als verarbeitet markiert (seen=%v err=%v)", seen, err)
	}
	// Das Stopp-Signal wirkt zwischen zwei UIDs: die zweite Mail bleibt liegen.
	if got, ok := mb.processed[102]; ok {
		t.Errorf("UID 102 wurde nach dem Stopp noch angefasst: %q", got)
	}
}
