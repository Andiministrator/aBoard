package mail

// worker.go — IMAP-Polling-Worker für #295 (ADR-0093 Etappe 4).
//
// Architektur:
//   - RunWorker(ctx, deps) ist die blockierende Goroutine-Hauptschleife.
//   - pollCycle öffnet eine IMAP-Verbindung, holt UNSEEN-UIDs, verarbeitet
//     jede über IngestMessage und verschiebt sie ans Ziel-Folder. Eine
//     Verbindung pro Cycle (kein IDLE im MVP, ADR-0093 §2).
//   - mailbox ist eine schmale Interface-Abstraktion — Production-Pfad nutzt
//     imapMailbox (go-imap-Wrapper), Tests können einen Fake einhängen.
//
// Backoff:
//   - Bei Connect-/Login-Fehlern Exponential-Backoff (1s/4s/16s/60s, dann
//     fest auf PollingInterval).
//   - Pro erfolgreichem Cycle wird Backoff zurückgesetzt.
//
// Idempotenz:
//   - ImapProcessedRepo.Seen prüft Message-ID vor IngestMessage — Doppel-Move-
//     Races werden lautlos zum ProcessedFolder verschoben, ohne erneute
//     Karten-/Kommentar-Erzeugung.
//   - UIDVALIDITY-Wechsel setzt last_uid auf 0 — die alte Mailbox-Snapshot ist
//     vom Provider verworfen worden, kein UID-Vergleich mehr sinnvoll.

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// WorkerDeps bündelt alle Abhängigkeiten des IMAP-Workers. Wird vom main-
// Init-Pfad zusammengebaut (server/main.go) und einmal an RunWorker übergeben.
type WorkerDeps struct {
	Config              IMAPConfig
	Boards              *storage.BoardRepo
	State               *storage.ImapStateRepo
	Processed           *storage.ImapProcessedRepo
	PreFilters          []IngestFilter
	SpamDetectors       []IngestFilter
	ServiceBoardEnabled bool
	SpamDrop            bool
	// Attachments persistiert Mail-Anhänge im Storage-Volume (F-358).
	// Wenn nil, werden Anhänge nicht abgelegt — Karten/Kommentare erhalten
	// dann KEINE attachment-IDs. Production-Pfad setzt das auf den
	// geteilten *storage.AttachmentStore.
	Attachments AttachmentSaver
	// AutoReply für opt-in Auto-Reply-Mail (#297). nil = deaktiviert.
	// Fehler beim Versand brechen den Ingest-Flow nicht ab.
	AutoReply *AutoReplyDeps
	// BoardName für das Auto-Reply-Template ({{.BoardName}}). Leer = "aBoard".
	BoardName string
	Logger    func(string, ...any)
	// DialFn ist überschreibbar für Tests. Default = dialMailbox.
	DialFn func(IMAPConfig) (mailbox, error)
	// OnServiceCardCreated — optionaler Notification-Hook nach erfolgreichem
	// Service-Board-Karten-Anlegen. Wird vom Notification-Subsystem (#305)
	// genutzt um service.new-card-Events zu emittieren. nil = aus.
	OnServiceCardCreated func(boardID, cardID, cardTitle, senderEmail string, displayID int)
}

// mailbox abstrahiert die IMAP-Operationen, die der Worker braucht.
// Production-Implementierung: imapMailbox. Tests können einen Fake einhängen.
type mailbox interface {
	Capabilities() (map[string]bool, error)
	SelectFolder(folder string) (uidValidity uint32, err error)
	UidSearchUnseen() ([]uint32, error)
	FetchRaw(uid uint32) ([]byte, error)
	UidMove(uid uint32, destFolder string) error
	Logout() error
}

// initialBackoff + Backoff-Sequenz für transient errors.
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 60 * time.Second
)

// processedEvictAge — der Worker räumt mail_imap_processed-Einträge älter
// als das auf (ADR-0093 §12).
const processedEvictAge = 30 * 24 * time.Hour

// processedEvictIntervall — Abstand zwischen zwei Aufräumläufen. Bis F-1192
// lief das Aufräumen nur einmal beim Worker-Start; seitdem wartet es auf eine
// gestellte Uhr und wird deshalb in der Poll-Schleife nachgeholt.
const processedEvictIntervall = 24 * time.Hour

// processedEvictFehlerAbstand — Wartezeit nach einem gescheiterten
// Aufräumlauf (F-1202, F-1215): kurz genug für einen transienten Lock, lang
// genug, dass ein dauerhafter Fehler Log und Schreibsperre nicht flutet.
const processedEvictFehlerAbstand = 15 * time.Minute

// RunWorker läuft in einer Goroutine bis ctx abgebrochen wird. Liefert beim
// Cancel nil (sauberer Shutdown). Fehler werden via Logger ausgegeben — wird
// nie zurückgegeben, weil der Worker resilient pollen soll (Polling-Worker
// stoppt nicht bei transient errors).
func RunWorker(ctx context.Context, deps WorkerDeps) error {
	if deps.Logger == nil {
		deps.Logger = func(string, ...any) {}
	}
	if deps.DialFn == nil {
		deps.DialFn = dialMailbox
	}
	if !deps.Config.Enabled {
		deps.Logger("imap-worker: deaktiviert (imap.enabled=false) — kein Start")
		return nil
	}
	if err := deps.Config.Validate(); err != nil {
		deps.Logger("imap-worker: Config-Validierung fehlgeschlagen — kein Start: %v", err)
		return nil
	}

	deps.Logger("imap-worker: gestartet — host=%s folder=%s polling=%v",
		deps.Config.Host, deps.Config.Folder, deps.Config.PollingInterval)

	// Aufräumen vor dem ersten Poll und danach täglich (evictWennFaellig).
	evict := func(ctx context.Context) (int64, error) {
		return deps.Processed.EvictOlderThan(ctx, processedEvictAge)
	}
	var naechsterEvict time.Time
	backoff := initialBackoff
	for ctx.Err() == nil {
		naechsterEvict = evictWennFaellig(ctx, deps.Logger, evict, naechsterEvict, time.Now())
		err := pollCycle(ctx, deps)
		if err != nil {
			deps.Logger("imap-worker: poll-cycle-Fehler (backoff %v): %v", backoff, err)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = initialBackoff
		if !sleepCtx(ctx, deps.Config.PollingInterval) {
			return nil
		}
	}
	return nil
}

// evictWennFaellig räumt mail_imap_processed auf, wenn es fällig ist
// (faellig null oder erreicht) und die Uhr gestellt ist (F-1192, server/uhr),
// und liefert den nächsten Fälligkeitszeitpunkt. Mit nicht gestellter Uhr
// bleibt es fällig und wird beim nächsten Poll erneut versucht, nach einem
// Fehler in processedEvictFehlerAbstand — vorher lief
// es nur einmal beim Start, also womöglich vor dem Uhrenabgleich. `jetzt`
// trägt die monotone Uhr von time.Now, der Abstand übersteht also einen
// Uhrsprung.
func evictWennFaellig(ctx context.Context, logf func(string, ...any),
	evict func(context.Context) (int64, error), faellig, jetzt time.Time) time.Time {
	if !faellig.IsZero() && jetzt.Before(faellig) {
		return faellig
	}
	if !uhr.Erlaubt("imap-evict", logf) {
		return faellig
	}
	n, err := evict(ctx)
	if err != nil {
		// Session-Kritiker Sprint 74 (F-1202): ein Fehler ist kein erledigter
		// Lauf — bald erneut versuchen, nicht erst in 24 h. Aber nicht bei jedem
		// Poll (Verify 2, F-1215): Ein dauerhafter Fehler wäre sonst im Backoff
		// alle paar Sekunden ein DELETE unter der Schreibsperre plus Log-Zeile.
		logf("imap-worker: processed-evict fehlgeschlagen, nächster Versuch in %s: %v", processedEvictFehlerAbstand, err)
		return jetzt.Add(processedEvictFehlerAbstand)
	}
	if n > 0 {
		logf("imap-worker: %d alte Message-IDs aus mail_imap_processed entfernt", n)
	}
	return jetzt.Add(processedEvictIntervall)
}

// sleepCtx schläft delay oder bricht bei ctx.Done() ab.
// Liefert true wenn die Pause normal verstrichen ist, false wenn abgebrochen.
func sleepCtx(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextBackoff verdoppelt den Backoff bis maxBackoff. Sequenz: 1s, 2s, 4s, …, 60s.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

// mailVerarbeitungsFrist begrenzt die Verarbeitung EINER Mail (Fetch→Ingest→
// MarkProcessed→Move), unabhängig vom Stopp-Signal des Workers (V-1).
const mailVerarbeitungsFrist = 2 * time.Minute

// pollCycle: eine Verbindung, alle UNSEEN-UIDs, dann Disconnect.
// Fehler werden weitergegeben — RunWorker entscheidet über Backoff.
func pollCycle(ctx context.Context, deps WorkerDeps) error {
	mb, err := deps.DialFn(deps.Config)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = mb.Logout() }()

	uidValidity, err := mb.SelectFolder(deps.Config.Folder)
	if err != nil {
		return fmt.Errorf("select %q: %w", deps.Config.Folder, err)
	}

	state, err := deps.State.Get(ctx, deps.Config.Folder)
	if err != nil {
		return fmt.Errorf("state.get: %w", err)
	}
	if state.UIDValidity != 0 && state.UIDValidity != uidValidity {
		deps.Logger("imap-worker: UIDVALIDITY-Wechsel %d→%d für %s — last_uid auf 0", state.UIDValidity, uidValidity, deps.Config.Folder)
		if err := deps.State.ResetUID(ctx, deps.Config.Folder, uidValidity); err != nil {
			return fmt.Errorf("state.reset-uid: %w", err)
		}
		state.LastUID = 0
	}
	state.UIDValidity = uidValidity

	uids, err := mb.UidSearchUnseen()
	if err != nil {
		return fmt.Errorf("uid-search: %w", err)
	}
	if len(uids) == 0 {
		// Kein neues Mail — State trotzdem aktualisieren (last_poll_at)
		state.LastPollAt = time.Now().UTC().Format(time.RFC3339Nano)
		return deps.State.Set(ctx, state)
	}

	for _, uid := range uids {
		if ctx.Err() != nil {
			break
		}
		if err := processOne(ctx, deps, mb, uid); err != nil {
			deps.Logger("imap-worker: uid=%d Verarbeitungsfehler: %v", uid, err)
			// Move-to-Error-Folder, damit die Mail nicht im nächsten Poll erneut auftaucht
			if moveErr := mb.UidMove(uid, deps.Config.ErrorFolder); moveErr != nil {
				deps.Logger("imap-worker: uid=%d konnte nicht in %s verschoben werden: %v", uid, deps.Config.ErrorFolder, moveErr)
			}
		}
		if uid > state.LastUID {
			state.LastUID = uid
		}
	}

	state.LastPollAt = time.Now().UTC().Format(time.RFC3339Nano)
	// V-1: Auch nach einem Stopp mitten im Lauf den Stand schreiben, sonst gehen
	// last_uid der schon verarbeiteten Mails verloren (kurze eigene Frist).
	setCtx, cancelSet := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelSet()
	if err := deps.State.Set(setCtx, state); err != nil {
		return fmt.Errorf("state.set: %w", err)
	}
	return nil
}

// processOne verarbeitet eine einzelne UID. Fehler bedeuten: Move-to-Error.
// Erfolg bedeutet: Move-to-Processed + MarkProcessed.
func processOne(ctx context.Context, deps WorkerDeps, mb mailbox, uid uint32) error {
	// V-1: Das Stopp-Signal (Reload/Shutdown) gilt nur ZWISCHEN zwei UIDs
	// (pollCycle). Eine angefangene Mail wird zu Ende gebracht — mit eigener
	// Frist, losgelöst vom Abbruch. Sonst scheitert BeginTx mit "context
	// canceled", die Mail landet im ErrorFolder und ist nie eine Karte.
	ctx, cancelMail := context.WithTimeout(context.WithoutCancel(ctx), mailVerarbeitungsFrist)
	defer cancelMail()

	raw, err := mb.FetchRaw(uid)
	if err != nil {
		return fmt.Errorf("fetch uid=%d: %w", uid, err)
	}

	parsed, err := ParseMail(bytes.NewReader(raw), deps.Config.MaxMailBytes(), deps.Config.MaxAttachmentBytes())
	if err != nil {
		// Bei Parse-Fehler: Mail in ErrorFolder. ParseFailed ist tolerant zurückgegeben,
		// damit der Worker weiterläuft.
		return fmt.Errorf("parse uid=%d: %w", uid, err)
	}

	// Idempotenz: schon mal verarbeitet? Dann nur Move ohne Re-Ingest.
	if parsed.MessageID != "" {
		seen, err := deps.Processed.Seen(ctx, parsed.MessageID)
		if err != nil {
			deps.Logger("imap-worker: processed.seen uid=%d: %v (Re-Process erlaubt)", uid, err)
		} else if seen {
			deps.Logger("imap-worker: uid=%d msg-id=%s bereits verarbeitet — nur Move", uid, parsed.MessageID)
			return mb.UidMove(uid, deps.Config.ProcessedFolder)
		}
	}

	opts := IngestOptions{
		PreFilters:           deps.PreFilters,
		SpamDetectors:        deps.SpamDetectors,
		ServiceBoardEnabled:  deps.ServiceBoardEnabled,
		SpamDrop:             deps.SpamDrop,
		Attachments:          deps.Attachments, // F-358
		AutoReply:            deps.AutoReply,   // #297
		BoardName:            deps.BoardName,
		OnServiceCardCreated: deps.OnServiceCardCreated, // #305
	}
	res, ingestErr := IngestMessage(ctx, deps.Boards, parsed, opts)
	if ingestErr != nil && !errors.Is(ingestErr, ErrNoServiceBoard) {
		// Harter Ingest-Fehler — Mail bleibt UNGESEEN-markiert nicht (Server hat
		// die UID schon gesehen); wir verschieben sie nach Error, damit der
		// nächste Poll sie nicht erneut anfasst.
		return fmt.Errorf("ingest uid=%d: %w", uid, ingestErr)
	}

	// MarkProcessed auch bei dropped/skipped — sonst landet die Mail bei UIDVALIDITY-
	// Reset erneut in der Pipeline.
	if parsed.MessageID != "" {
		if err := deps.Processed.MarkProcessed(ctx, parsed.MessageID); err != nil {
			deps.Logger("imap-worker: mark-processed uid=%d msg-id=%s: %v", uid, parsed.MessageID, err)
		}
	}

	if res != nil {
		deps.Logger("imap-worker: uid=%d action=%s display-id=%d reason=%q",
			uid, res.Action, res.DisplayID, res.DropReason)
	}

	return mb.UidMove(uid, deps.Config.ProcessedFolder)
}

// --- IMAP-Wrapper (Production) -------------------------------------------

// imapMailbox ist die Default-Implementierung der mailbox-Interface — wraps
// go-imap *client.Client.
type imapMailbox struct {
	c *client.Client
}

// dialMailbox stellt eine TLS-/STARTTLS-Verbindung her, login'ed mit User+Pass.
// Wird als WorkerDeps.DialFn-Default verwendet.
func dialMailbox(cfg IMAPConfig) (mailbox, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 15 * time.Second
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	// Karte #569: Ausgangs-Schalter. go-imap wählt selbst — deshalb hier vorab
	// prüfen, bevor DialWithDialer(TLS) überhaupt aufgerufen wird.
	if err := ausgang.Aktiv().Pruefe(addr); err != nil {
		return nil, err
	}

	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var c *client.Client
	var err error
	if cfg.TLS {
		c, err = client.DialWithDialerTLS(dialer, addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("dial-tls: %w", err)
		}
	} else {
		c, err = client.DialWithDialer(dialer, addr)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		// STARTTLS opportunistisch, wenn der Server es kann
		if ok, _ := c.SupportStartTLS(); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				_ = c.Logout()
				return nil, fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if err := c.Login(cfg.User, cfg.Pass); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("login: %w", err)
	}
	return &imapMailbox{c: c}, nil
}

func (m *imapMailbox) Capabilities() (map[string]bool, error) {
	return m.c.Capability()
}

func (m *imapMailbox) SelectFolder(folder string) (uint32, error) {
	mb, err := m.c.Select(folder, false)
	if err != nil {
		return 0, err
	}
	return mb.UidValidity, nil
}

func (m *imapMailbox) UidSearchUnseen() ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	return m.c.UidSearch(criteria)
}

func (m *imapMailbox) FetchRaw(uid uint32) ([]byte, error) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{section.FetchItem()}

	msgCh := make(chan *imap.Message, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- m.c.UidFetch(seqset, items, msgCh) }()

	var raw []byte
	for msg := range msgCh {
		if msg == nil {
			continue
		}
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		buf, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read body uid=%d: %w", uid, err)
		}
		raw = buf
	}
	if err := <-errCh; err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("uid=%d: kein Body geliefert", uid)
	}
	return raw, nil
}

func (m *imapMailbox) UidMove(uid uint32, dst string) error {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	// go-imap fällt bei fehlender MOVE-Extension automatisch auf
	// Copy+Store(\Deleted)+Expunge zurück — kein Capability-Check nötig.
	return m.c.UidMove(seqset, dst)
}

func (m *imapMailbox) Logout() error {
	if m.c == nil {
		return nil
	}
	return m.c.Logout()
}

// TestIMAPConnection prüft Dial + Login + SELECT INBOX gegen die konfigurierte
// IMAP-Verbindung, OHNE Mails zu pollen. Wird vom Admin-Test-Hook
// (POST /admin/config/imap/test) und beim CLI-Debug genutzt.
//
// Anti-Spam: Test-Klicks führen keinen Pull aus.
func TestIMAPConnection(cfg IMAPConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	mb, err := dialMailbox(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mb.Logout() }()
	if _, err := mb.SelectFolder(cfg.Folder); err != nil {
		return fmt.Errorf("select %q: %w", cfg.Folder, err)
	}
	return nil
}
