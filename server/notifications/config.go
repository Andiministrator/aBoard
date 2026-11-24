package notifications

// config.go — Konstanten + Konfigurations-Struktur für den Notification-
// Subsystem.

import "time"

// CoalescenceWindow — Sliding-Window pro (user, event_type, channel, card_id).
// Innerhalb des Windows wird ein erneutes Event zur selben (Sub, Card)
// nicht erneut versendet (status: coalesced).
const CoalescenceWindow = 5 * time.Minute

// HourlyChannelLimit — harte 30/h pro (User, Kanal). Darüber: Event landet
// im Log als status=throttled und wird vom Digest-Worker um 18:00 in eine
// Sammel-Mail/Sammel-Gotify-Notification überführt.
const HourlyChannelLimit = 30

// DigestHour — Uhrzeit für die Tagesübersicht (lokale Server-Time).
const DigestHour = 18

// BusBuffer — Kapazität der internen Event-Queue. Bei Überlauf blockiert
// Emit kurz (channel-send), aber niemals länger als 50ms (siehe bus.go) —
// danach Drop mit log.Printf-Warnung statt Mutationspfad zu bremsen.
const BusBuffer = 256

// SendTimeout — pro Channel (SMTP-Connect+Send oder HTTP-POST).
const SendTimeout = 15 * time.Second

// RetryDelays — exponentielles Backoff bei Send-Fehler.
// Spec aus #305 agentContext: 5s / 30s / 2min.
var RetryDelays = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
}

// MaxBodySnippetLength — Längenlimit für die im Notification-Body
// gezeigten Auszüge.
const (
	EmailSnippetMax  = 200
	GotifySnippetMax = 80
)

// NotifLogSummaryMax — Längenlimit für `notification_log.summary` (F-809).
// Eigene, grosszügigere Konstante statt EmailSnippetMax/GotifySnippetMax:
// Bei `system.subsystem-off` IST der Grund die Nachricht, und für Mail/
// Gotify soll er ungekürzt bleiben (F-723). Diese Konstante bindet nur die
// DB-Spalte (Log/Digest) — SQLite-`TEXT` ist unbegrenzt, ein Limit ist hier
// reine Log-Hygiene, kein Sicherheitsthema (server-interner Text).
const NotifLogSummaryMax = 1000
