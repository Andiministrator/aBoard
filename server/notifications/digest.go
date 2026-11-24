package notifications

// digest.go — Tagesübersichts-Worker (18:00 lokal).
//
// Iteriert alle User mit pending-throttled-Einträgen, baut pro User eine
// Sammel-Mail mit den gedrosselten Events der letzten 24 h und markiert
// nach erfolgreichem Send die Log-Zeilen als digested.

import (
	"context"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// DigestDeps bündelt die Abhängigkeiten des Digest-Workers.
type DigestDeps struct {
	Logs  *storage.NotificationLogRepo
	Users UserResolver
	Email *EmailSender
	// DeliveryEnabled ist der Hauptschalter aus Karte #420 — hier ein
	// eigenes Feld und NICHT der `suppressedSender`, weil der Digest über
	// `SendDigest` geht, das gar nicht Teil des `Sender`-Interface ist.
	//
	// Der Sprint-59-Kritiker hat genau diese Lücke gefunden ([S] P1): Der
	// Worker bekam die umhüllten Sender, der Digest elf Zeilen später die
	// rohe Instanz. Und der Weg dorthin war nicht theoretisch, sondern der
	// Normalbetrieb — der `suppressedSender` meldet Erfolg, also schreibt
	// der Worker `status='sent'`, ab der 31. Zustellung pro Stunde greift
	// die Drosselung und schreibt `status='throttled'`, und genau die
	// sammelt der Digest um 18:00 ein und schickt sie hinaus.
	//
	// Die Lehre daran ist allgemeiner als der Fehler: Der Schalter sass
	// bei den SENDERN, und die Begründung dafür ist richtig — nur ist der
	// Digest kein Emitter, sondern ein ZWEITER SENDEPFAD. Wer einen
	// Schalter setzt, muss alle Ausgänge zählen, nicht alle Eingänge.
	DeliveryEnabled bool
	Logger          func(string, ...interface{})
	Now             func() time.Time
}

// notificationLogRetentionAge — Zeilen aus notification_log werden nach
// dieser Dauer entfernt (F-379, Karte #411). Analog zu mail_imap_processed
// (30 Tage, ImapProcessedRepo.EvictOlderThan in storage/imap_state.go) —
// dieselbe Klasse Tabelle (append-only Log), hier bislang ungelöst. Läuft im
// selben 18:00-Takt wie der Digest-Versand (loop() unten), statt einen
// zweiten Scheduler einzuführen: notification_log ist ohnehin die Tabelle,
// die dieser Worker schon anfässt (PendingDigest/MarkDigested).
const notificationLogRetentionAge = 30 * 24 * time.Hour

// DigestScheduler ist der einfache Stdlib-Scheduler: einmal pro Tag um
// DigestHour (lokale Zeit) wird RunOnce ausgeführt. Bewusst keine Cron-
// Library — die einzige Wiederholfrequenz ist „täglich um X Uhr"
// (entspricht boardbackup/scheduler.go-Stil).
type DigestScheduler struct {
	deps DigestDeps
}

// NewDigestScheduler legt einen Scheduler an.
func NewDigestScheduler(deps DigestDeps) *DigestScheduler {
	if deps.Logger == nil {
		deps.Logger = func(string, ...interface{}) {}
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now() }
	}
	return &DigestScheduler{deps: deps}
}

// Start spawnt eine Goroutine, die jeden Tag um DigestHour läuft. Liefert
// eine Cancel-Funktion. Idempotent — mehrfach starten ist erlaubt, aber
// jeder Aufruf erzeugt eine eigene Goroutine.
func (s *DigestScheduler) Start(ctx context.Context) func() {
	runCtx, cancel := context.WithCancel(ctx)
	go s.loop(runCtx)
	return cancel
}

func (s *DigestScheduler) loop(ctx context.Context) {
	// Initial-Cleanup beim Start — nicht erst beim ersten 18:00-Tick.
	//
	// Der Sprint-57-Kritiker hat die Luecke gemessen: Haengt die Eviction
	// ALLEIN am Tages-Tick, laeuft sie nie, solange der Prozess oefter neu
	// startet als einmal taeglich. Genau das ist der Normalfall dieses
	// Boards — jeder :latest-Deploy startet den Container neu, ein
	// Crash-Restart-Loop erst recht. notification_log waere dann exakt so
	// unbegrenzt gewachsen wie vor F-379, und das Finding stuende als
	// behoben da.
	//
	// Das damalige Vorbild (mail.RunWorker, Initial-Cleanup vor der
	// Polling-Schleife) machte es von Anfang an so; Karte #411 hat beim
	// Uebernehmen ausgerechnet den Teil weggelassen, der es robust macht.
	// Seit F-1192 räumt mail.RunWorker in der Schleife auf (evictWennFaellig);
	// hier genügt der Start plus der Tages-Tick, weil evictOld mit nicht
	// gestellter Uhr ohnehin spätestens um 18:00 erneut versucht wird.
	s.evictOld(ctx)

	for {
		next := nextDigestTime(s.deps.Now())
		wait := time.Until(next)
		if wait < 0 {
			wait = time.Minute
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.RunOnce(ctx)
			s.evictOld(ctx)
		}
	}
}

// evictOld räumt notification_log-Zeilen älter als
// notificationLogRetentionAge auf (F-379). Best-effort — ein Fehler stoppt
// den Scheduler nicht, analog zum imap-worker-Cleanup
// (mail.evictWennFaellig in server/mail/worker.go).
//
// F-1192: nur mit gestellter Uhr (server/uhr); sonst holt der nächste Lauf
// (spätestens 18:00) es nach.
func (s *DigestScheduler) evictOld(ctx context.Context) {
	if !uhr.Erlaubt("notifications-evict", s.deps.Logger) {
		return
	}
	n, err := s.deps.Logs.EvictOlderThan(ctx, notificationLogRetentionAge)
	if err != nil {
		s.deps.Logger("notifications-digest: evict fehlgeschlagen: %v", err)
		return
	}
	if n > 0 {
		s.deps.Logger("notifications-digest: %d alte notification_log-Einträge entfernt", n)
	}
}

// nextDigestTime liefert den nächsten 18:00-Lokal-Zeitpunkt nach `now`.
//
// F-373 (Sprint-42-Kritiker, behoben Sprint 61/Karte #445): Der
// „morgen"-Zweig baute den nächsten Zeitpunkt vorher per `t.Add(24 *
// time.Hour)` — eine Dauer-Arithmetik auf einem `time.Time` mit
// Zonen-Bezug. `Add` verschiebt um exakt 24 REALE Stunden, nicht um einen
// Kalendertag; an einem DST-Wechseltag hat der lokale Tag aber 23 oder 25
// Stunden. Ergebnis: Der berechnete „morgige 18:00"-Zeitpunkt trifft in
// Wahrheit 17:00 oder 19:00 Wanduhrzeit. Das allein wäre nur eine
// Stunde Versatz — der eigentliche Schaden entsteht beim NÄCHSTEN
// Schleifendurchlauf (`loop()` oben ruft diese Funktion bei jedem Tick neu
// auf): Trifft der Timer bei 17:00 Wanduhrzeit, liegt 18:00 desselben Tages
// noch in der Zukunft → derselbe Tag feuert ein zweites Mal. Trifft er bei
// 19:00, liegt 18:00 bereits in der Vergangenheit → der Lauf springt direkt
// auf übermorgen, der Tag fällt komplett aus.
//
// Der Fix ersetzt die Dauer-Arithmetik durch eine zweite `time.Date`-
// Konstruktion auf dem Kalendertag `now.Day()+1`. `time.Date` normalisiert
// überlaufende Feld-Werte (Go-Doku: „may be outside their usual ranges")
// UND ist zonen-bewusst — es berechnet die tatsächliche UTC-Absolutzeit für
// die angegebene ZIVILE Wanduhrzeit in `now.Location()`, unabhängig davon,
// welcher UTC-Offset an diesem Kalendertag gerade gilt. Ein DST-Wechsel
// zwischen heute 18:00 und morgen 18:00 verschiebt damit korrekt NUR die
// Distanz in Echtzeit-Stunden (23 oder 25), nicht mehr die Wanduhrzeit.
func nextDigestTime(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), DigestHour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, DigestHour, 0, 0, 0, now.Location())
	}
	return t
}

// RunOnce führt einen einzelnen Digest-Lauf aus (öffentlich für Test-Hook
// im Admin-Subsystem + Unit-Tests).
func (s *DigestScheduler) RunOnce(ctx context.Context) {
	users, err := s.deps.Logs.UsersWithPendingDigest(ctx)
	if err != nil {
		s.deps.Logger("notifications-digest: pending users: %v", err)
		return
	}
	now := s.deps.Now()
	for _, u := range users {
		if err := s.runOneUser(ctx, u, now); err != nil {
			s.deps.Logger("notifications-digest: user=%s err=%v", u, err)
		}
	}
}

func (s *DigestScheduler) runOneUser(ctx context.Context, userID string, now time.Time) error {
	entries, err := s.deps.Logs.PendingDigest(ctx, userID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	email, name, _, err := s.deps.Users.Resolve(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(email) == "" {
		s.deps.Logger("notifications-digest: skip user=%s (no email)", userID)
		return nil
	}
	items := make([]digestItem, 0, len(entries))
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ts, _ := time.Parse(time.RFC3339, e.TS)
		items = append(items, digestItem{
			EventType: e.EventType,
			Summary:   e.Summary,
			TS:        ts,
		})
		ids = append(ids, e.ID)
	}
	if !s.deps.DeliveryEnabled {
		// Nicht still: Ohne diese Zeile ist ein unterdrückter Digest von
		// einem ausgebliebenen nicht zu unterscheiden.
		s.deps.Logger("notifications-digest: VERSAND AUS (%s=off) — unterdrückt: %d Eintrag/Einträge an %s",
			DeliveryEnvVar, len(items), email)
		// Trotzdem als versandt markieren — bewusst, und aus demselben
		// Grund, aus dem der `suppressedSender` Erfolg meldet: Der
		// Trockenbetrieb soll sich verhalten wie der Echtbetrieb, nur ohne
		// Zustellung. Würde hier nicht markiert, sammelten sich die
		// Einträge unbegrenzt an, und der erste Lauf nach dem Einschalten
		// schickte eine Sammelmail über den gesamten Zeitraum davor — ein
		// schlechterer Zustand als der, den das Markieren kostet. Was
		// tatsächlich angefallen wäre, steht in der Zeile darüber und in
		// den `notification_log`-Einträgen.
		return s.deps.Logs.MarkDigested(ctx, ids)
	}
	if err := s.deps.Email.SendDigest(ctx, email, name, items, now); err != nil {
		return err
	}
	return s.deps.Logs.MarkDigested(ctx, ids)
}
