package retention

// scheduler.go — periodischer Aufräumer für LLM-Verläufe (llm_conversations/
// llm_turns) und das Audit-Log (server_config_audit), ADR-0129 §5 A-8,
// Karte #572.
//
// Muster wie notifications.DigestScheduler (Aufräumen beim Start, danach
// Tick). mail.RunWorker tat es bis F-1192 genauso; seitdem räumt er in der
// Poll-Schleife auf, sobald es fällig ist und die Uhr steht
// (mail.evictWennFaellig) — weil ein einmaliger Lauf vor der Schleife bei
// falscher Uhr bis zum nächsten Neustart ausfiele. Alle drei belegen dieselbe
// Lehre (Sprint-57-Kritiker, dokumentiert in notifications/digest.go):
// Hängt die Eviction ALLEIN am periodischen Tick, läuft sie nie, solange
// der Prozess öfter neu startet als das Intervall — der Normalfall bei
// :latest-Deploys und erst recht bei einem Crash-Restart-Loop. Deshalb
// räumt Start() früh auf, nicht erst beim ersten Tick — seit F-1135 nach
// einer kurzen Startverzögerung (DefaultErsterLaufNach), damit eine beim
// Hochfahren falsch gehende Uhr vorher von NTP gestellt wird.
//
// Anders als beim Digest (fester Wanduhr-Zeitpunkt 18:00, weil an
// demselben Takt der Mail-Versand hängt) hängt hier kein zweiter Vorgang
// an der Uhrzeit — ein einfacher Intervall-Ticker genügt (Vorbild:
// mail.RunWorker mit PollingInterval statt einer Wanduhr-Berechnung).
//
// Warum ein EIGENER periodischer Scheduler statt Trim-je-Schreibvorgang
// (wie bei activities.Record/RecordOps): AppendTurn/AppendTurnPair und die
// Audit-Inserts laufen sehr viel häufiger als eine Frist-Prüfung nötig
// hätte — die Fristen sind tage-granular, ein Trim bei jedem Turn wäre
// unnötige Last ohne erkennbaren Nutzen.

import (
	"context"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// DefaultInterval — wie oft EvictExpired/EvictAuditExpired periodisch
// laufen. Die Fristen selbst (ABOARD_LLM_RETENTION_DAYS/
// ABOARD_AUDIT_RETENTION_DAYS) sind tage-granular — ein täglicher Takt
// reicht, ein kürzerer brächte keinen erkennbaren Vorteil.
const DefaultInterval = 24 * time.Hour

// DefaultErsterLaufNach — Wartezeit vom Start bis zum ersten Aufräumlauf
// (F-1135, Karte #581). Ein Pi ohne gepufferte Uhr startet mit falscher
// Zeit, bis NTP sie stellt; eine vorgehende Uhr ließe LLM-Verläufe und Audit
// zu früh löschen. Zehn Minuten reichen für den NTP-Abgleich nach dem
// Hochfahren mit großem Abstand. Der Preis: Startet der Prozess öfter neu
// als alle zehn Minuten, räumt er nicht auf — für eine tage-genaue Frist
// unerheblich, und ein solcher Neustart-Kreislauf fällt ohnehin auf.
const DefaultErsterLaufNach = 10 * time.Minute

// Deps bündelt die Abhängigkeiten des Retention-Schedulers. LLM und Audit
// dürfen unabhängig voneinander nil sein (z. B. in Tests, die nur eine der
// beiden Tabellen prüfen) — RunOnce überspringt einen nil-Repo dann still.
type Deps struct {
	LLM   *storage.LLMConversationRepo
	Audit *storage.ConfigRepo
	// OpIDs: Idempotenz-Schlüssel des Ops-Endpunkts (ADR-0130 §5, Karte #607).
	OpIDs *storage.OpIDRepo
	// Journal: bestätigte Einträge des Board-Paar-Journals (ADR-0141 §5.2,
	// Karte #827) — nach 30 Tagen weg.
	Journal  *storage.BoardRepo
	Interval time.Duration // 0 → DefaultInterval
	// ErsterLaufNach: Wartezeit bis zum ersten Lauf; 0 → DefaultErsterLaufNach.
	ErsterLaufNach time.Duration
	Logger         func(string, ...any) // nil → no-op
}

// Scheduler räumt periodisch llm_conversations/llm_turns
// (LLMConversationRepo.EvictExpired) und server_config_audit
// (ConfigRepo.EvictAuditExpired) auf.
type Scheduler struct {
	deps Deps
}

// New legt einen Scheduler an.
func New(deps Deps) *Scheduler {
	if deps.Interval <= 0 {
		deps.Interval = DefaultInterval
	}
	if deps.ErsterLaufNach <= 0 {
		deps.ErsterLaufNach = DefaultErsterLaufNach
	}
	if deps.Logger == nil {
		deps.Logger = func(string, ...any) {}
	}
	return &Scheduler{deps: deps}
}

// Start spawnt eine Goroutine, die nach deps.ErsterLaufNach einmal aufräumt
// (RunOnce) und danach alle deps.Interval erneut, bis ctx endet. Der erste
// Lauf hängt nicht am Intervall (Lehre des Digest-Schedulers), aber auch
// nicht mehr am Startzeitpunkt selbst (F-1135: erst nach dem NTP-Abgleich).
// Liefert eine Cancel-Funktion. Idempotent — mehrfach starten ist erlaubt, aber jeder
// Aufruf erzeugt eine eigene Goroutine (analog DigestScheduler.Start).
func (s *Scheduler) Start(ctx context.Context) func() {
	runCtx, cancel := context.WithCancel(ctx)
	go s.loop(runCtx)
	return cancel
}

func (s *Scheduler) loop(ctx context.Context) {
	erster := time.NewTimer(s.deps.ErsterLaufNach)
	select {
	case <-ctx.Done():
		erster.Stop()
		return
	case <-erster.C:
	}
	s.RunOnce(ctx)

	ticker := time.NewTicker(s.deps.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce führt einen einzelnen Aufräum-Durchlauf aus — öffentlich für
// einen künftigen Admin-Test-Hook und für Unit-Tests (analog
// DigestScheduler.RunOnce). Best-effort: Ein Fehler in einem der beiden
// Repos stoppt den anderen nicht und stoppt den Scheduler nicht.
//
// F-1192: Nur mit gestellter Uhr (server/uhr) — sonst löscht dieser Lauf
// nichts und das Log sagt es; der nächste Lauf versucht es wieder.
func (s *Scheduler) RunOnce(ctx context.Context) {
	if !uhr.Erlaubt("retention", s.deps.Logger) {
		return
	}
	if s.deps.LLM != nil {
		n, err := s.deps.LLM.EvictExpired(ctx)
		if err != nil {
			s.deps.Logger("retention: llm-evict fehlgeschlagen: %v", err)
		} else if n > 0 {
			s.deps.Logger("retention: %d alte LLM-Conversation(s) entfernt", n)
		}
	}
	if s.deps.OpIDs != nil {
		n, err := s.deps.OpIDs.EvictExpired(ctx)
		if err != nil {
			s.deps.Logger("retention: op-ids-evict fehlgeschlagen: %v", err)
		} else if n > 0 {
			s.deps.Logger("retention: %d alte Op-ids entfernt", n)
		}
	}
	if s.deps.Journal != nil {
		n, err := s.deps.Journal.JournalAufraeumen(ctx, time.Now())
		if err != nil {
			s.deps.Logger("retention: boardpaar-journal fehlgeschlagen: %v", err)
		} else if n > 0 {
			s.deps.Logger("retention: %d Board-Paar-Journal-Einträge entfernt (bestätigt oder Reste entkoppelter Kopien)", n)
		}
	}
	if s.deps.Audit != nil {
		n, err := s.deps.Audit.EvictAuditExpired(ctx)
		if err != nil {
			s.deps.Logger("retention: audit-evict fehlgeschlagen: %v", err)
		} else if n > 0 {
			s.deps.Logger("retention: %d alte Audit-Eintraege entfernt", n)
		}
	}
}
