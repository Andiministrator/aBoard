package notifications

// event.go — Event-Typ + Konstanten + Default-Subscription-Tabelle.
//
// Eine Event-Instanz wandert aus den Emit-Quellen (Server-intern oder
// REST-getriggert) in den Bus, vom Bus über die Worker-Goroutine in die
// (User × Channel)-Liste und dort in die jeweiligen Sender (email/gotify).

import "time"

// Event-Typen — kanonische Strings. Auch im Frontend bekannt (i18n + Settings).
const (
	EventCardAssigned   = "card.assigned"
	EventCardCommented  = "card.commented"
	EventCardMentioned  = "card.mentioned"
	EventCardMoved      = "card.moved"
	EventCardBlocked    = "card.blocked"
	EventBoardAwaiting  = "board.awaiting-user"
	EventServiceNewCard = "service.new-card"
	EventSprintClosed   = "sprint.closed"
	EventBackupFailed   = "system.backup-failed"
	// EventSubsystemOff meldet, dass ein Subsystem wegen FEHLKONFIGURATION
	// gar nicht erst angelaufen ist — im Unterschied zu EventBackupFailed,
	// das einen fehlgeschlagenen LAUF meldet.
	//
	// Der Anlass (Sprint-58-Nachtrag zu Karte #336): Steht
	// `backup_board.encryption_enabled=true`, fehlt aber der Master-Key,
	// bleibt der Backup-Scheduler bewusst aus — ein unverschlüsseltes
	// Ersatz-Backup wäre eine stille Herabstufung des Schutzniveaus. Nur
	// hängt `OnFailure` am Scheduler: Läuft der nie, feuert auch
	// system.backup-failed nie, obwohl ab diesem Moment ÜBERHAUPT KEINE
	// Sicherungen mehr entstehen. Der Operator erfuhr es einzig aus einer
	// Log-Zeile beim Serverstart.
	//
	// „Keine Backups" ist der schlechteste Zustand, den ein Backup-System
	// still einnehmen kann. Bewusst allgemein benannt: Der Fall „Subsystem
	// aus, weil falsch konfiguriert" ist nicht backup-spezifisch, und der
	// nächste Kandidat soll keinen zweiten Ereignistyp brauchen.
	EventSubsystemOff = "system.subsystem-off"
	// EventCommentDeleted: Ein Board-Owner oder Server-Admin hat einen
	// Kommentar gelöscht, den jemand anderes geschrieben hat (ADR-0136 §3).
	// Empfänger ist allein der Autor (ExplicitRecipientUserIDs).
	EventCommentDeleted = "card.comment-deleted"
)

// AllEventTypes ist die kanonische Liste — wird vom REST-Endpoint
// /notifications/event-types geliefert (Frontend rendert die Settings-UI
// daraus, ohne die Strings zu duplizieren).
var AllEventTypes = []string{
	EventCardAssigned,
	EventCardCommented,
	EventCardMentioned,
	EventCardMoved,
	EventCardBlocked,
	EventBoardAwaiting,
	EventServiceNewCard,
	EventSprintClosed,
	EventBackupFailed,
	EventSubsystemOff,
	EventCommentDeleted,
}

// Channel-Konstanten.
const (
	ChannelEmail  = "email"
	ChannelGotify = "gotify"
)

// AllChannels ist die kanonische Channel-Liste.
var AllChannels = []string{ChannelEmail, ChannelGotify}

// Event ist die wandernde Nutzlast. Felder bewusst flach — der Worker
// extrahiert was er braucht.
type Event struct {
	// Type ist eines aus den Event…-Konstanten.
	Type string
	// ActorUserID ist die User-ID des Auslösers — der Worker filtert
	// Subscriptions, deren Empfänger == Actor sind (Anti-Loop: User wird
	// nicht für eigene Aktionen benachrichtigt).
	ActorUserID string
	// ActorEmail — für Render-Templates ("von <name> verschoben").
	// Optional; Worker fällt zurück auf user_id wenn leer.
	ActorEmail string
	// BoardID + BoardName — für Render-Templates + Deep-Link.
	BoardID   string
	BoardName string
	// CardID + CardDisplayID + CardTitle — für die meisten Events.
	// Bei Events ohne Karte (system.backup-failed, board.awaiting-user)
	// leer.
	CardID        string
	CardDisplayID int
	CardTitle     string
	// Snippet — kurzer Body-Auszug (~200 Zeichen), z. B. Kommentar-Text
	// oder Verschiebe-Beschreibung. Render-Pfad kürzt auf channel-Limit.
	Snippet string
	// MentionedUserIDs — bei card.mentioned die explizit gemeinten User.
	// Worker beschränkt das Fan-Out auf diese (überschreibt das übliche
	// Subscriber-Lookup). Leer bei anderen Event-Typen.
	MentionedUserIDs []string
	// ExplicitRecipientUserIDs — bei system.backup-failed o. ä. die Liste
	// der Admin-User. Worker nutzt sie statt EnabledSubscribers, weil
	// Admin-only-Events ein anderes Subscriber-Modell haben.
	ExplicitRecipientUserIDs []string
	// TS — Erzeugungszeitpunkt (Bus setzt automatisch wenn leer).
	TS time.Time
	// Extra — kanal-spezifische Override-Felder (z. B. Gotify-Priority
	// pro Event-Typ wird aus Type abgeleitet, kein Override-Override hier).
	// Heute leer — Reservefeld.
	Extra map[string]string
}

// HasCard zeigt, ob das Event sich auf eine spezifische Karte bezieht
// (für Coalescence-Bucket-Bildung).
func (e Event) HasCard() bool { return e.CardID != "" }

// DefaultSubscribed liefert den Default-Wert, wenn KEIN expliziter Eintrag
// in notification_subscriptions vorhanden ist. Entspricht der Tabelle aus
// ADR-0107 §2.
//
// Logik: Karten-Events des Empfängers (assigned/commented/mentioned/moved/
// blocked) und board.awaiting-user sind per Default ON. Service-Board-Neue-
// Karte und Sprint-Closed sind OFF (eher Operator-Interesse). Backup-Failed
// nur für Admins ON — der Aufrufer filtert die Empfänger-Liste vorher
// (über ExplicitRecipientUserIDs).
func DefaultSubscribed(eventType, channel string) bool {
	switch eventType {
	case EventCardAssigned, EventCardCommented, EventCardMentioned,
		EventCardBlocked, EventBoardAwaiting, EventBackupFailed,
		EventSubsystemOff, EventCommentDeleted:
		return true
	case EventCardMoved:
		// Auf E-Mail per Default OFF (sonst zu laut), auf Gotify ON
		// (Tracker pulsiert leise — Mobile-Sichtbarkeit).
		return channel == ChannelGotify
	case EventServiceNewCard, EventSprintClosed:
		return false
	default:
		return false
	}
}

// GotifyPriority mappt Event-Typen auf Gotify-Priority (siehe Karte #305
// agentContext + ADR-0107 §5). Höhere Werte = laut/aufdringlich.
func GotifyPriority(eventType string) int {
	switch eventType {
	case EventBackupFailed, EventSubsystemOff:
		return 8
	case EventBoardAwaiting, EventCardMentioned:
		return 7
	case EventCardAssigned, EventCardCommented, EventCardBlocked, EventCommentDeleted:
		return 5
	case EventCardMoved, EventSprintClosed, EventServiceNewCard:
		return 3
	default:
		return 5
	}
}

// HumanLabel liefert die deutsche Bezeichnung für ein Event — für E-Mail-
// Subject + Settings-UI. Hier zentralisiert, damit Frontend (i18n) und
// Backend (Render) nicht driften.
func HumanLabel(eventType string) string {
	switch eventType {
	case EventCardAssigned:
		return "Karte zugewiesen"
	case EventCardCommented:
		return "Neuer Kommentar"
	case EventCardMentioned:
		return "Erwähnt in Kommentar"
	case EventCardMoved:
		return "Karte verschoben"
	case EventCardBlocked:
		return "Karte blockiert"
	case EventSubsystemOff:
		return "Subsystem wegen Fehlkonfiguration aus"
	case EventBoardAwaiting:
		return "Claude wartet auf Antwort"
	case EventServiceNewCard:
		return "Neue Karte im Service-Board"
	case EventSprintClosed:
		return "Sprint abgeschlossen"
	case EventBackupFailed:
		return "Backup fehlgeschlagen"
	case EventCommentDeleted:
		return "Eigener Kommentar gelöscht"
	default:
		return eventType
	}
}
