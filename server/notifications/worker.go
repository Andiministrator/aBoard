package notifications

// worker.go — Notification-Worker.
//
// Konsumiert Events aus dem Bus, ermittelt pro Event die Empfänger (×Kanal),
// wendet Anti-Loop, Coalescence (5 Min) und Throttling (30/h/Kanal) an,
// versendet via Channel-Sender (Email/Gotify) mit Retry, und schreibt
// jeden Versuch ins notification_log.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// LaneBuffer — Kapazität der Sub-Queue EINES Empfängers (F-380, Karte #377).
// Läuft sie über, wird für diesen Empfänger verworfen; alle anderen sind
// davon nicht betroffen. 64 ist grosszügig gegenüber dem 30/h-Throttling,
// das ohnehin vorher greift.
const LaneBuffer = 64

// SubscriberSource liefert die UserIDs aller Empfänger eines Events.
// Implementierung in worker.go ist die Default-Version (kombiniert
// EnabledSubscribers aus dem Repo mit dem Default-Subscribed-Fallback).
type SubscriberSource interface {
	Subscribers(ctx context.Context, eventType, channel string) ([]string, error)
}

// UserResolver liefert E-Mail-Adresse + Anzeige-Name + Admin-Flag zu einer
// User-ID — Worker braucht das für E-Mail-Versand und Admin-Filterung.
type UserResolver interface {
	Resolve(ctx context.Context, userID string) (email, name string, isAdmin bool, err error)
}

// Sender ist die Channel-Abstraktion. Eine konkrete Implementierung pro
// Channel (Email/Gotify). SendOne darf nicht panicen; Fehler propagieren
// zum Retry-Loop.
type Sender interface {
	Channel() string
	SendOne(ctx context.Context, recipientUserID, recipientEmail, recipientName string, ev Event) error
}

// WorkerDeps bündelt die Abhängigkeiten des Workers.
type WorkerDeps struct {
	Bus           *Bus
	Subscriptions *storage.NotificationSubscriptionRepo
	Configs       *storage.NotificationConfigRepo
	Logs          *storage.NotificationLogRepo
	Users         UserResolver
	Senders       []Sender // Reihenfolge egal; Worker matched per Sender.Channel()
	Logger        func(string, ...interface{})
	// Now ist für Tests injizierbar (sonst time.Now).
	Now func() time.Time
}

// Worker konsumiert den Bus und versendet.
//
// F-380 (Karte #377): Der Versand läuft NICHT mehr synchron im Bus-Loop,
// sondern in einer Sub-Queue pro Empfänger („Lane"). Vorher blockierte ein
// einziger hängender SMTP-Server die gesamte Zustellung: VIER Sendeversuche
// à 15 s SendTimeout (60 s) plus die drei Retry-Delays (5 s + 30 s + 2 min =
// 155 s) ergeben rund 215 s je Empfänger, bei zehn Mention-Empfängern also
// gut 35 Minuten, in denen kein einziges anderes Event durchkommt — mit
// anschliessendem Drop bei vollem Bus (BusBuffer = 256).
//
// F-810 (Sprint 61): Hier stand „15 s SendTimeout plus die Retry-Delays …
// rund 155 s". Beide Hälften stimmten nicht. Erstens war 155 s die Summe der
// DELAYS allein — die vier Sendeversuche fehlten in der Rechnung. Zweitens
// band der SendTimeout den E-Mail-Kanal überhaupt nicht: `sendWithRetry`
// baute ihn zwar, `EmailSender.SendOne` reichte den Context aber nicht an
// `mailpkg.SendMail` weiter, das intern `context.Background()` setzte. Für
// E-Mail galt bis Sprint 61 nur noch die IDLE-Frist pro SMTP-Operation
// (`cfg.Timeout`, F-718) — ein Server, der langsam, aber stetig antwortet,
// konnte einen einzelnen Versuch beliebig lange strecken. Seit dem Fix
// (sendFunc mit Context, Default `mailpkg.SendMailContext`) deckt die Zahl
// oben auch den E-Mail-Kanal.
//
// Warum Lanes und kein allgemeiner Goroutine-Pool: der Bus ist FIFO und der
// Worker verarbeitete bisher streng sequentiell, ein Empfänger bekam seine
// Events also in Emit-Reihenfolge. Ein Pool bricht genau das. Eine Lane pro
// (Channel, Empfänger) erhält die Reihenfolge dort, wo sie beobachtbar ist,
// und isoliert Empfänger voneinander — mehr wurde nie zugesagt.
//
// Coalescence und Throttling wandern MIT in die Lane. Beide lesen das
// notification_log, das erst beim Send geschrieben wird; blieben sie im
// Bus-Loop, könnten zwei Events desselben Typs die Coalescence-Prüfung
// passieren, bevor das erste geloggt ist. In der Lane bleibt die Abfolge
// „prüfen → senden → loggen" pro Empfänger serialisiert, exakt wie vorher.
type Worker struct {
	deps WorkerDeps
	wg   sync.WaitGroup

	mu     sync.Mutex
	lanes  map[string]chan Event
	closed bool

	// inflight zählt eingereihte, noch nicht abgearbeitete Events. Grundlage
	// für WaitIdle() — ohne das könnte ein Test nach dem Einreihen nicht
	// feststellen, wann die Zustellung durch ist.
	inflight sync.WaitGroup
	dropped  atomic.Int64
}

// NewWorker erzeugt einen Worker.
func NewWorker(deps WorkerDeps) *Worker {
	if deps.Logger == nil {
		deps.Logger = func(string, ...interface{}) {}
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now() }
	}
	return &Worker{deps: deps, lanes: make(map[string]chan Event)}
}

// Run blockiert und konsumiert den Bus, bis der Channel geschlossen wird
// ODER ctx abgebrochen wird. Aufruf typischerweise in eigener Goroutine.
//
// Der `defer Stop()` ist nicht Kosmetik: die Lanes sind Goroutinen mit
// offenen Channels. Ohne Abbau bliebe pro Empfänger eine Goroutine hängen,
// wenn der Server heruntergefahren wird — die Ausstiegsfrage gehört zum
// Einbau, nicht zum ersten Vorfall (knowledge/topics/nebenlaeufigkeit-und-
// lesefenster.md §6). Abgedeckt sind damit: Normalende (Bus geschlossen),
// Abbruch (ctx) und Fehler; ein `kill -9` räumt ohnehin niemand auf.
func (w *Worker) Run(ctx context.Context) {
	if w.deps.Bus == nil {
		return
	}
	defer w.Stop()
	rx := w.deps.Bus.Receive()
	for {
		// Priorisierte, nicht-blockierende Vorabprüfung (F-376,
		// Sprint-42-Kritiker + Nacharbeit Sprint 61): Ohne sie entscheidet
		// die zweite, blockierende select unten bei GLEICHZEITIGER
		// Bereitschaft (ctx bereits Done UND rx hat gepufferte Events)
		// pseudo-zufällig zwischen beiden Fällen — ein bereits abgebrochener
		// Kontext würde dann gelegentlich noch ein weiteres Event
		// verarbeiten, bevor der Abbruch greift. Diese Prüfung macht den
		// Abbruchfall deterministisch UND ist die Stelle, an der gepufferte,
		// noch nicht verarbeitete Events gezählt und geloggt werden, statt
		// stillschweigend zu verschwinden.
		select {
		case <-ctx.Done():
			drainAndLog(rx, w.deps.Logger)
			return
		default:
		}
		select {
		case <-ctx.Done():
			drainAndLog(rx, w.deps.Logger)
			return
		case ev, ok := <-rx:
			if !ok {
				return
			}
			w.handle(ctx, ev)
		}
	}
}

// drainAndLog liest alle im Bus-Channel bereits gepufferten, aber noch
// nicht verarbeiteten Events NICHT-BLOCKIEREND ab und loggt ihre Anzahl,
// bevor der Worker sich beim ctx-Abbruch beendet.
//
// WARUM (F-376, Sprint-42-Kritiker): ADR-0107 §Konsequenzen akzeptiert
// In-Memory-Verlust bei Restart ausdrücklich als Trade-off — was fehlte,
// war die Beobachtbarkeit. Ohne diese Zeile sieht ein Operator im Log
// NICHT, dass beim Shutdown Events verworfen wurden; die einzige Spur wäre
// eine stille Lücke im notification_log.
//
// Bewusst NICHT versucht zu verarbeiten: main.go bindet den gesamten
// Worker-Shutdown an ein festes, EIGENES Zeitbudget (main.go:
// notifShutdownTimeout, F-729/Karte #509 — nicht dasselbe Budget wie der
// HTTP-Shutdown, siehe dortiger Kommentar); ein Versand jetzt würde dieses
// Budget zusätzlich beanspruchen — noch dazu OHNE die Lane-Isolation aus
// F-380, also mit dem Risiko, dass ein einziger hängender Empfänger den
// ganzen Rest wieder mitreisst.
func drainAndLog(rx <-chan Event, logger func(string, ...interface{})) int {
	n := 0
	for {
		select {
		case _, ok := <-rx:
			if !ok {
				return n
			}
			n++
		default:
			if n > 0 {
				logger("notifications: worker-shutdown — %d im Bus gepufferte Event(e) beim ctx-Abbruch verworfen (ADR-0107: In-Memory-Verlust akzeptiert)", n)
			}
			return n
		}
	}
}

// laneKey identifiziert die Sub-Queue eines Empfängers auf einem Kanal.
// Das NUL-Byte trennt sauber: es kommt weder in Channel-Namen noch in
// User-IDs vor, ein `:` könnte dagegen Teil einer ID sein.
func laneKey(channel, userID string) string { return channel + "\x00" + userID }

// enqueue reiht ein Event in die Lane des Empfängers ein und startet sie bei
// Bedarf. Läuft die Lane über, wird für DIESEN Empfänger verworfen — sichtbar
// im Log und im notification_log, nicht still (die Drop-Beobachtbarkeit war
// ausdrückliche Bedingung der Karte).
func (w *Worker) enqueue(ctx context.Context, sender Sender, userID string, ev Event) {
	channel := sender.Channel()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	key := laneKey(channel, userID)
	ch, ok := w.lanes[key]
	if !ok {
		ch = make(chan Event, LaneBuffer)
		w.lanes[key] = ch
		w.wg.Add(1)
		go w.laneLoop(ctx, sender, userID, ch)
	}

	// Der Send bleibt UNTER dem Lock (Sprint-52-Kritiker, K10). Vorher wurde
	// hier entsperrt und erst danach gesendet: läuft dazwischen `Stop()`
	// durch, das die Channels unter demselben Lock schliesst, ist der Send
	// ein `send on closed channel` — und damit ein Panic, den das `recover`
	// in deliverOne nicht abfängt (es sitzt eine Ebene tiefer). Heute nicht
	// auslösbar, weil `Stop()` nur aus `Run`s defer in derselben Goroutine
	// kommt; `Stop()` ist aber exportiert und im Kommentar ausdrücklich für
	// ein geordnetes Herunterfahren angeboten.
	//
	// Der Send ist nicht-blockierend (`default`-Zweig), das Lock wird also
	// nie länger als ein Channel-Send gehalten.
	w.inflight.Add(1)
	select {
	case ch <- ev:
		w.mu.Unlock()
		return
	default:
		w.mu.Unlock()
		w.inflight.Done()
		w.dropped.Add(1)
		w.deps.Logger("notifications: lane voll — event verworfen: user=%s channel=%s type=%s",
			userID, channel, ev.Type)
		/* Als failed protokollieren: das Schema kennt keinen eigenen
		   `dropped`-Status (CHECK-Constraint in 0010_notifications.sql), und
		   ein Migrations-Bump ist dafür unverhältnismässig. Der Error-Text
		   macht den Fall in der Log-Tabelle trotzdem eindeutig auffindbar. */
		w.logAttempt(ctx, userID, ev, channel, storage.NotifyStatusFailed, 0,
			"lane-queue voll — verworfen")
	}
}

// laneLoop arbeitet die Sub-Queue eines Empfängers ab — streng
// sequentiell, damit Reihenfolge, Coalescence und Throttling pro Empfänger
// so korrekt bleiben wie im alten synchronen Pfad.
func (w *Worker) laneLoop(ctx context.Context, sender Sender, userID string, ch <-chan Event) {
	defer w.wg.Done()
	for ev := range ch {
		if ctx.Err() != nil {
			// Abbruch: Restbestand nur noch abmelden, nicht mehr senden.
			// Ohne dieses Done() bliebe WaitIdle() für immer hängen.
			w.inflight.Done()
			continue
		}
		w.deliverOne(ctx, sender, userID, ev)
		w.inflight.Done()
	}
}

// deliverOne führt Coalescence-, Throttle-Prüfung und Versand für genau ein
// Event aus. Das `recover` ist Defense-in-depth: das Sender-Interface sagt
// zu, nicht zu panicen — trächte eine Implementierung es doch, riss sie
// vorher den ganzen Prozess mit, jetzt nur diese eine Zustellung.
func (w *Worker) deliverOne(ctx context.Context, sender Sender, userID string, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			w.deps.Logger("notifications: panic im Send-Pfad aufgefangen: user=%s channel=%s type=%s: %v",
				userID, sender.Channel(), ev.Type, r)
		}
	}()
	channel := sender.Channel()
	if w.shouldCoalesce(ctx, userID, ev, channel) {
		w.logAttempt(ctx, userID, ev, channel, storage.NotifyStatusCoalesced, 0, "")
		return
	}
	if w.isThrottled(ctx, userID, channel) {
		w.logAttempt(ctx, userID, ev, channel, storage.NotifyStatusThrottled, 0, "")
		return
	}
	w.sendWithRetry(ctx, sender, userID, ev)
}

// WaitIdle blockiert, bis alle eingereihten Events abgearbeitet sind.
//
// Ausschliesslich Testinfrastruktur (Karte #421, F-521) — kein Aufrufer im
// Produktivpfad, und das ist Absicht, nicht Versehen: der sichere
// Produktivweg ist Shutdown() unten (Stop()+Wait()+Timeout).
//
// NUR sicher, wenn nebenher NICHT weiter eingereiht wird — also in Tests und
// NACH `Stop()`. Grund: `sync.WaitGroup` panickt mit „WaitGroup is reused
// before previous Wait has returned", wenn ein `Add` läuft, während ein `Wait`
// auf einen bereits auf 0 gefallenen Zähler wartet. Genau das ist bei einem
// laufenden Worker möglich, weil der Bus-Loop jederzeit das nächste Event
// einreiht.
//
// Für ein geordnetes Herunterfahren also `Stop()` zuerst (schliesst die Lanes
// und lässt `enqueue` nichts mehr annehmen), dann `Wait()` — nicht
// `WaitIdle()`. Der frühere Kommentar bot die Methode für genau den Fall an,
// in dem sie unsicher ist (Sprint-52-Sitzungskritiker, V10; siehe F-521).
func (w *Worker) WaitIdle() { w.inflight.Wait() }

// Dropped liefert die Zahl der wegen voller Lane verworfenen Events —
// die Kennzahl zum Log-Eintrag, damit ein Drop nicht nur in einer Zeile
// steht, die niemand liest.
func (w *Worker) Dropped() int64 { return w.dropped.Load() }

// Stop schliesst alle Lanes und wartet auf ihr Ende. Idempotent.
// Nach Stop nimmt der Worker nichts mehr an.
func (w *Worker) Stop() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	for _, ch := range w.lanes {
		close(ch)
	}
	w.mu.Unlock()
	w.wg.Wait()
}

// handle verarbeitet ein einzelnes Event: ermittelt Empfänger, wendet
// Anti-Loop/Coalescence/Throttling an, schickt asynchron pro Empfänger.
func (w *Worker) handle(ctx context.Context, ev Event) {
	// Für jeden bekannten Channel die Subscriber bestimmen + senden.
	for _, sender := range w.deps.Senders {
		channel := sender.Channel()
		recipients, err := w.resolveRecipients(ctx, ev, channel)
		if err != nil {
			w.deps.Logger("notifications: resolve recipients failed: type=%s channel=%s err=%v", ev.Type, channel, err)
			continue
		}
		for _, userID := range recipients {
			// Anti-Loop: User-Aktionen lösen für den User selbst KEINE
			// Notification aus (Karte #305 §Anti-Loop). Bleibt hier im
			// Bus-Loop — reiner Feldvergleich, kein IO.
			if userID == ev.ActorUserID {
				continue
			}
			// F-380: ab hier übernimmt die Lane des Empfängers. Coalescence,
			// Throttling und Versand laufen dort (siehe deliverOne) — ein
			// hängender SMTP-Server verzögert damit nur die Zustellung an
			// SEINEN Empfänger, nicht den ganzen Bus.
			w.enqueue(ctx, sender, userID, ev)
		}
	}
}

// resolveRecipients liefert die UserIDs, die das Event auf diesem Channel
// erhalten sollen. Berücksichtigt:
//   - ExplicitRecipientUserIDs (z. B. Backup-Failed an Admins) — VORRANG.
//   - MentionedUserIDs (card.mentioned) — nur diese erhalten das Event.
//   - sonst: alle User mit (explizit enabled) ODER (kein expliziter Eintrag
//     UND DefaultSubscribed=true).
func (w *Worker) resolveRecipients(ctx context.Context, ev Event, channel string) ([]string, error) {
	// Spezialfall: explizite Recipients (Admin-only-Events o. ä.) —
	// ignoriert Subscription-Tabelle nicht ganz, aber überschreibt das
	// Default-Subscriber-Set. Pro User noch einmal die explizite Disable-
	// Möglichkeit prüfen (User kann Backup-Failed-Mail deaktivieren).
	if len(ev.ExplicitRecipientUserIDs) > 0 {
		var out []string
		for _, u := range ev.ExplicitRecipientUserIDs {
			ok, err := w.userOptedIn(ctx, u, ev.Type, channel)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, u)
			}
		}
		return out, nil
	}
	// Spezialfall card.mentioned: Fan-Out auf die in MentionedUserIDs
	// genannten User — Subscription gilt zusätzlich (User kann
	// Mention-Mails deaktivieren).
	if ev.Type == EventCardMentioned && len(ev.MentionedUserIDs) > 0 {
		var out []string
		for _, u := range ev.MentionedUserIDs {
			ok, err := w.userOptedIn(ctx, u, ev.Type, channel)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, u)
			}
		}
		return out, nil
	}
	// Default-Pfad: nur Empfänger, die das Event ausdrücklich abonniert
	// haben (oder per Default ON sind). Der Aufrufer (Mutations-Hook)
	// hat bereits die Karten-Empfänger eingegrenzt (z. B. nur Owner +
	// Assignees bei card.commented); wir tun hier KEIN board-weites
	// Fan-Out auf alle Mitglieder.
	explicit, err := w.deps.Subscriptions.EnabledSubscribers(ctx, ev.Type, channel)
	if err != nil {
		return nil, fmt.Errorf("subscribers: %w", err)
	}
	return explicit, nil
}

// userOptedIn prüft, ob ein User dieses (event_type, channel)-Tupel
// erhalten will. Berücksichtigt expliziten Eintrag UND Default.
func (w *Worker) userOptedIn(ctx context.Context, userID, eventType, channel string) (bool, error) {
	en, present, err := w.deps.Subscriptions.IsEnabled(ctx, userID, eventType, channel)
	if err != nil {
		return false, err
	}
	if present {
		return en, nil
	}
	return DefaultSubscribed(eventType, channel), nil
}

// shouldCoalesce prüft, ob in den letzten CoalescenceWindow Minuten ein
// gleichartiges Event zum selben (user, event_type, channel, card) bereits
// gelogged wurde. Nur Karten-Events werden coalesced — Events ohne CardID
// (z. B. system.backup-failed) immer als eigenständig behandelt.
func (w *Worker) shouldCoalesce(ctx context.Context, userID string, ev Event, channel string) bool {
	if !ev.HasCard() {
		return false
	}
	since := w.deps.Now().Add(-CoalescenceWindow)
	n, err := w.deps.Logs.RecentForCoalescence(ctx, userID, ev.Type, channel, ev.CardID, since)
	if err != nil {
		w.deps.Logger("notifications: coalesce count failed: %v", err)
		return false
	}
	return n > 0
}

// isThrottled prüft das 30/h/Kanal-Limit. Zählt sent-Einträge der letzten
// Stunde.
func (w *Worker) isThrottled(ctx context.Context, userID, channel string) bool {
	since := w.deps.Now().Add(-1 * time.Hour)
	n, err := w.deps.Logs.CountSentSince(ctx, userID, channel, since)
	if err != nil {
		w.deps.Logger("notifications: throttle count failed: %v", err)
		return false
	}
	return n >= HourlyChannelLimit
}

// sendWithRetry versucht den Versand bis zu len(RetryDelays)+1 Mal mit
// exponentiellem Backoff. Erfolg → status=sent. Letzter Fehlversuch →
// status=failed mit Error.
func (w *Worker) sendWithRetry(ctx context.Context, sender Sender, recipientUserID string, ev Event) {
	email, name, _, err := w.deps.Users.Resolve(ctx, recipientUserID)
	if err != nil || strings.TrimSpace(email) == "" {
		// Empfänger nicht auflösbar oder kein E-Mail-Profil. Loggen + Skip.
		errMsg := "user-resolve failed"
		if err != nil {
			errMsg = err.Error()
		}
		w.logAttempt(ctx, recipientUserID, ev, sender.Channel(), storage.NotifyStatusFailed, 0, errMsg)
		return
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		sendCtx, cancel := context.WithTimeout(ctx, SendTimeout)
		err := sender.SendOne(sendCtx, recipientUserID, email, name, ev)
		cancel()
		if err == nil {
			w.logAttempt(ctx, recipientUserID, ev, sender.Channel(), storage.NotifyStatusSent, attempt, "")
			return
		}
		lastErr = err
		if attempt >= len(RetryDelays) {
			break
		}
		// Backoff — nicht via time.Sleep blockierend, sondern via Timer +
		// ctx-Cancel-Abbruch.
		t := time.NewTimer(RetryDelays[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			w.logAttempt(ctx, recipientUserID, ev, sender.Channel(), storage.NotifyStatusFailed, attempt, ctx.Err().Error())
			return
		case <-t.C:
		}
	}
	w.logAttempt(ctx, recipientUserID, ev, sender.Channel(), storage.NotifyStatusFailed, len(RetryDelays), lastErr.Error())
}

// logAttempt schreibt einen Eintrag ins notification_log (best-effort —
// Fehler werden nur geloggt, der Send-Pfad geht weiter).
func (w *Worker) logAttempt(ctx context.Context, userID string, ev Event, channel, status string, retry int, errStr string) {
	summary := buildSummary(ev)
	entry := storage.NotificationLogEntry{
		UserID:     userID,
		EventType:  ev.Type,
		Channel:    channel,
		CardID:     ev.CardID,
		BoardID:    ev.BoardID,
		Summary:    summary,
		Status:     status,
		RetryCount: retry,
		Error:      errStr,
	}
	if _, err := w.deps.Logs.Insert(ctx, entry); err != nil {
		w.deps.Logger("notifications: log-insert failed: %v", err)
	}
}

// buildSummary baut einen kompakten Status-String für den Log-Eintrag
// (für Digest-Mails + Debug). Nicht der versendete Body — der wird in den
// Sendern gerendert.
func buildSummary(ev Event) string {
	switch ev.Type {
	case EventBackupFailed:
		if ev.BoardName != "" {
			return "Backup fehlgeschlagen: " + ev.BoardName
		}
		return "Backup fehlgeschlagen"
	case EventSubsystemOff:
		// Snippet trägt hier den Grund (welches Subsystem, welche
		// Fehlkonfiguration) — ohne ihn ist die Meldung unbrauchbar,
		// weil der Operator sonst nur weiss, DASS etwas aus ist.
		//
		// F-809: Für die DB-Spalte (notification_log.summary, vom Digest
		// weitergereicht) gilt trotzdem ein Limit — mit NotifLogSummaryMax,
		// NICHT EmailSnippetMax/GotifySnippetMax, denn die Mail-/Gotify-
		// Kürzung ist seit F-723 bewusst deaktiviert (der Grund IST die
		// Nachricht). Die Log-Spalte hat kein solches Bedürfnis nach
		// Vollständigkeit, wohl aber nach einer Obergrenze.
		if ev.Snippet != "" {
			// Präfix + Grund zusammen kürzen (nicht nur den Grund) — sonst
			// könnte "Subsystem aus: " + NotifLogSummaryMax-Runen die
			// Obergrenze der Spalte selbst wieder überschreiten.
			return truncate("Subsystem aus: "+ev.Snippet, NotifLogSummaryMax)
		}
		return "Subsystem wegen Fehlkonfiguration aus"
	case EventBoardAwaiting:
		return "Wartet auf User-Antwort: " + ev.BoardName
	default:
		if ev.CardTitle == "" {
			return HumanLabel(ev.Type)
		}
		return fmt.Sprintf("%s: #%d — %s", HumanLabel(ev.Type), ev.CardDisplayID, ev.CardTitle)
	}
}

// Wait wartet auf das Ende aller Lane-Goroutinen. Seit F-380 kein
// Platzhalter mehr: die Lanes sind genau der Goroutine-Stack, auf den hier
// gewartet wird. Sinnvoll erst NACH Stop() — vorher laufen die Lanes
// unbegrenzt weiter, weil ihre Channels offen sind.
func (w *Worker) Wait() {
	w.wg.Wait()
}

// Shutdown ist der Produktivpfad für den geordneten Ausstieg (Karte #421,
// F-521) — das Gegenstück zu WaitIdle(), das laut deren eigenem Kommentar
// dafür NICHT sicher ist. Shutdown ruft Stop() (schliesst die Lanes, wartet
// bereits intern) gefolgt von Wait() (dokumentierte Reihenfolge, s. o.) in
// einer eigenen Goroutine und meldet zusätzlich Dropped() als Laufzeit-
// Kennzahl — sonst hätte auch diese Methode nie einen Aufrufer ausserhalb
// von Tests.
//
// `timeout` ist die Obergrenze der Wartezeit. F-380 hat den Worker gegen
// einen hängenden SMTP-Server abgesichert, indem ein Empfänger die anderen
// nicht mehr blockiert — das deckt den laufenden Betrieb, NICHT den
// Prozess-Exit. Ohne Obergrenze würde ein einzelner hängender Empfänger
// (SendTimeout + RetryDelays, siehe Paket-Doku oben ~215 s worst case) den
// Shutdown genauso aufhalten, wie F-380 es für den laufenden Betrieb bereits
// verhindert hat — also dieselbe Klasse Fehler, nur eine Ebene höher.
//
// Rückgabe:
//   - true  — alle Lanes sind sauber beendet, bevor `timeout` ablief.
//   - false — `timeout` erreicht; Stop()/Wait() laufen im Hintergrund weiter
//     (best effort). Der Aufrufer darf den Prozess trotzdem beenden — ein
//     Timeout hier ist ein geloggter Kompromiss, kein Fehler, der den Exit
//     verhindern soll.
func (w *Worker) Shutdown(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		w.Stop() // schliesst die Lanes, wartet bereits intern (w.wg.Wait())
		w.Wait() // dokumentierte Reihenfolge (s. Kommentar oben) — nach
		// bereits erfolgtem Stop() ein schneller Re-Check auf denselben
		// (längst auf 0 gefallenen) Zähler.
		close(done)
	}()
	select {
	case <-done:
		if d := w.Dropped(); d > 0 {
			w.deps.Logger("notifications: shutdown — %d Event(e) wurden während der Laufzeit wegen voller Lane verworfen", d)
		}
		return true
	case <-time.After(timeout):
		w.deps.Logger("notifications: shutdown-timeout (%s) erreicht — Rest läuft im Hintergrund weiter", timeout)
		return false
	}
}
