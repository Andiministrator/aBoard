package api

// notifications.go — REST-Endpunkte für /users/me/notifications/* + den
// Client-Trigger /boards/{id}/notifications/events (#305, ADR-0107).
//
// ACL: own-User-only für /users/me/* (kein IDOR). Der Client-Trigger
// /boards/{id}/notifications/events läuft unter dem ACLGuard
// (Board-editor+ nötig — die POST-Methode passiert die Editor-Schranke);
// der Server validiert die in der Payload genannten Empfänger NICHT per
// Karten-State (Server-State ist opak, ADR-0044) — er übernimmt
// `recipient_user_ids`/`mentioned_user_ids` 1:1 aus der Payload. Das
// Frontend, das das Board interpretiert, entscheidet wer benachrichtigt
// wird. `actor_user_id` wird IGNORIERT und immer aus dem authentifizierten
// Caller gesetzt — sonst Anti-Loop-Bypass (Kritiker-Lauf Sprint 42 [S]P2).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/notifications"
	"github.com/Andiministrator/aBoard/server/storage"
)

// NotificationsAPI bündelt alle Notification-bezogenen REST-Handler.
//
// Felder können nil sein:
//   - configs == nil → Schreib-Endpunkte (Set Config) antworten 503.
//   - bus == nil     → Trigger-Endpunkt antwortet 503.
//   - acl  == nil    → recipient_scope wird ignoriert (Resolver kein-op).
//
// maxRecipientEmails begrenzt, wie viele Adressen ein einzelner Emit aufloesen
// darf. 50 ist grosszuegig gegenueber jedem realen Board (Owner plus Assignees
// plus Erwaehnte) und klein genug, dass ein Emit keine unbegrenzte Zahl
// Datenbankabfragen ausloest. Ueberzaehlige werden abgeschnitten, nicht
// abgelehnt: Ein 4xx wuerde die Karten-Mutation im Frontend als gescheitert
// erscheinen lassen, obwohl sie gelungen ist.
const maxRecipientEmails = 50

type NotificationsAPI struct {
	subscriptions *storage.NotificationSubscriptionRepo
	configs       *storage.NotificationConfigRepo
	logs          *storage.NotificationLogRepo
	users         *storage.UserRepo
	bus           notifications.Emitter
	gotifyTest    *notifications.GotifySender
	emailTest     *notifications.EmailSender
	boards        *storage.BoardRepo
	acl           *storage.AclRepo
	limiter       *rateLimiter
	// deliveryEnabled ist der Zustand des Versand-Hauptschalters, EINMAL
	// beim Start ausgewertet — genau wie die Sender-Verdrahtung in main.go
	// (Sprint-59-Kritiker [V] P3). Die erste Fassung las die Umgebung bei
	// jeder Anfrage neu und begründete das mit „dieselbe reine Funktion,
	// keine zweite Wahrheit". Das stimmte für die Funktion, nicht für den
	// ZEITPUNKT: Die UI konnte damit einen Zustand melden, der von der
	// tatsächlichen Verdrahtung abweicht. Ein Wert, einmal gelesen, ein
	// Konstruktor-Argument.
	deliveryEnabled bool
}

// NewNotificationsAPI legt die API-Struktur an.
func NewNotificationsAPI(
	subs *storage.NotificationSubscriptionRepo,
	cfgs *storage.NotificationConfigRepo,
	logs *storage.NotificationLogRepo,
	users *storage.UserRepo,
	boards *storage.BoardRepo,
	acl *storage.AclRepo,
	bus notifications.Emitter,
	gotifyTest *notifications.GotifySender,
	emailTest *notifications.EmailSender,
	deliveryEnabled bool,
) *NotificationsAPI {
	return &NotificationsAPI{
		subscriptions:   subs,
		configs:         cfgs,
		logs:            logs,
		users:           users,
		boards:          boards,
		acl:             acl,
		bus:             bus,
		gotifyTest:      gotifyTest,
		emailTest:       emailTest,
		deliveryEnabled: deliveryEnabled,
		limiter:         newRateLimiter(60, time.Hour), // 60 Mutationen/h pro User
	}
}

// Register hängt alle Routen an mux.
func (a *NotificationsAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /users/me/notifications/event-types", a.protect(a.handleEventTypes, false))
	mux.Handle("GET /users/me/notifications/subscriptions", a.protect(a.handleListSubs, false))
	mux.Handle("PUT /users/me/notifications/subscriptions/{eventType}/{channel}", a.protect(a.handleSetSub, true))
	mux.Handle("GET /users/me/notifications/config/{channel}", a.protect(a.handleGetConfig, false))
	mux.Handle("PUT /users/me/notifications/config/{channel}", a.protect(a.handleSetConfig, true))
	mux.Handle("DELETE /users/me/notifications/config/{channel}", a.protect(a.handleDeleteConfig, true))
	mux.Handle("POST /users/me/notifications/test/{channel}", a.protect(a.handleTestSend, true))
	// Client-Trigger: liegt unter /boards/{id}/ → ACLGuard erzwingt
	// editor+ am Board. Der Handler übernimmt recipients aus dem Body, ohne
	// Karten-State zu prüfen (ADR-0044). `actor_user_id` wird IGNORIERT
	// und durch den authentifizierten Caller ersetzt — Anti-Loop-Bypass
	// wäre sonst trivial (Kritiker-Lauf Sprint 42 [S]P2).
	// Rate-Limit gegen authenticated-user-DoS: identisch zum protect-Limiter
	// (60/h pro User) — siehe handleEmitEvent.
	mux.Handle("POST /boards/{id}/notifications/events", WithDefaults(http.HandlerFunc(a.handleEmitEvent)))
}

// protect prüft Forward-Auth + lädt User-Row, übergibt userID an Handler.
func (a *NotificationsAPI) protect(h func(w http.ResponseWriter, r *http.Request, userID string), rateLimit bool) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			writeError(w, http.StatusForbidden, "forbidden: notifications require authentication")
			return
		}
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		u, err := a.users.GetByEmail(r.Context(), email)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		if rateLimit {
			if !a.limiter.allow(u.ID) {
				writeError(w, http.StatusTooManyRequests, "rate limit: 60 mutations per hour per user")
				return
			}
		}
		h(w, r, u.ID)
	}))
}

// handleEventTypes liefert die kanonische Event-Typ-Liste — das Frontend
// rendert daraus die Settings-UI, ohne die Strings zu duplizieren.
func (a *NotificationsAPI) handleEventTypes(w http.ResponseWriter, r *http.Request, _ string) {
	type item struct {
		Type          string `json:"type"`
		Label         string `json:"label"`
		DefaultEmail  bool   `json:"default_email"`
		DefaultGotify bool   `json:"default_gotify"`
	}
	out := make([]item, 0, len(notifications.AllEventTypes))
	for _, t := range notifications.AllEventTypes {
		out = append(out, item{
			Type:          t,
			Label:         notifications.HumanLabel(t),
			DefaultEmail:  notifications.DefaultSubscribed(t, notifications.ChannelEmail),
			DefaultGotify: notifications.DefaultSubscribed(t, notifications.ChannelGotify),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"event_types": out,
		"channels":    notifications.AllChannels,
		// #420: Damit die Einstellungs-UI sagen kann, dass der Versand
		// zentral aus ist. Ohne dieses Feld sieht der Nutzer eine
		// vollstaendig konfigurierbare Oberflaeche, die nichts zustellt —
		// und sucht den Fehler bei sich.
		//
		// Derselbe Wert, den main.go bei der Sender-Verdrahtung benutzt
		// hat — als Konstruktor-Argument durchgereicht statt hier erneut
		// aus der Umgebung gelesen. Sonst gäbe es zwei LESEZEITPUNKTE für
		// denselben Schalter, und die UI könnte einen Zustand melden, der
		// von der tatsächlichen Verdrahtung abweicht.
		"delivery_enabled": a.deliveryEnabled,
	})
}

// handleListSubs liefert die expliziten + Default-aufgelösten Subscription-
// Einträge dieses Users. Das Frontend bekommt die effektive Sicht, kein
// Wissen über Defaults nötig.
func (a *NotificationsAPI) handleListSubs(w http.ResponseWriter, r *http.Request, userID string) {
	explicit, err := a.subscriptions.List(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "subs list failed", err)
		return
	}
	// Map: (event_type, channel) → enabled
	explicitMap := make(map[string]bool, len(explicit))
	for _, s := range explicit {
		explicitMap[s.EventType+"|"+s.Channel] = s.Enabled
	}
	type effective struct {
		EventType string `json:"event_type"`
		Channel   string `json:"channel"`
		Enabled   bool   `json:"enabled"`
		Explicit  bool   `json:"explicit"`
	}
	out := make([]effective, 0, len(notifications.AllEventTypes)*len(notifications.AllChannels))
	for _, t := range notifications.AllEventTypes {
		for _, ch := range notifications.AllChannels {
			key := t + "|" + ch
			if v, ok := explicitMap[key]; ok {
				out = append(out, effective{EventType: t, Channel: ch, Enabled: v, Explicit: true})
			} else {
				out = append(out, effective{EventType: t, Channel: ch, Enabled: notifications.DefaultSubscribed(t, ch), Explicit: false})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// handleSetSub schreibt einen expliziten Eintrag.
func (a *NotificationsAPI) handleSetSub(w http.ResponseWriter, r *http.Request, userID string) {
	eventType := r.PathValue("eventType")
	channel := r.PathValue("channel")
	if !isKnownEventType(eventType) {
		writeError(w, http.StatusBadRequest, "unknown event_type")
		return
	}
	if !isKnownChannel(channel) {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := a.subscriptions.Set(r.Context(), userID, eventType, channel, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "subs set failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
}

// handleGetConfig liefert nur „is_set" — der Token wird NIE ausgeliefert.
// Für Gotify zusätzlich die URL (kein Geheimnis).
func (a *NotificationsAPI) handleGetConfig(w http.ResponseWriter, r *http.Request, userID string) {
	channel := r.PathValue("channel")
	if !isKnownChannel(channel) {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	if a.configs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"channel": channel, "is_set": false, "secrets_enabled": false})
		return
	}
	has, err := a.configs.Has(r.Context(), userID, channel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config get failed", err)
		return
	}
	resp := map[string]any{
		"channel":         channel,
		"is_set":          has,
		"secrets_enabled": a.configs.SecretsEnabled(),
	}
	if has && channel == notifications.ChannelGotify {
		// URL ausliefern (kein Geheimnis), Token NICHT.
		cfg, err := a.configs.GetGotify(r.Context(), userID)
		if err == nil && cfg != nil {
			resp["url"] = cfg.URL
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetConfig schreibt eine Channel-Config. Für Gotify: URL + Token
// im Body, beides Pflicht.
func (a *NotificationsAPI) handleSetConfig(w http.ResponseWriter, r *http.Request, userID string) {
	channel := r.PathValue("channel")
	if !isKnownChannel(channel) {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	if a.configs == nil || !a.configs.SecretsEnabled() {
		writeError(w, http.StatusServiceUnavailable, "secrets disabled: configure ABOARD_SECRET_KEY")
		return
	}
	switch channel {
	case notifications.ChannelGotify:
		r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
		var body struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		body.URL = strings.TrimSpace(body.URL)
		body.Token = strings.TrimSpace(body.Token)
		if err := notifications.ValidateGotifyURL(body.URL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid url: "+err.Error())
			return
		}
		if body.Token == "" {
			writeError(w, http.StatusBadRequest, "token required")
			return
		}
		if err := a.configs.SetGotify(r.Context(), userID, storage.GotifyConfig{URL: body.URL, Token: body.Token}); err != nil {
			writeError(w, http.StatusInternalServerError, "config set failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case notifications.ChannelEmail:
		// E-Mail braucht keine User-Config — Adresse stammt aus users.email.
		writeError(w, http.StatusBadRequest, "email channel has no per-user config (uses identity email)")
	default:
		writeError(w, http.StatusBadRequest, "unknown channel")
	}
}

// handleDeleteConfig entfernt eine Channel-Config.
func (a *NotificationsAPI) handleDeleteConfig(w http.ResponseWriter, r *http.Request, userID string) {
	channel := r.PathValue("channel")
	if !isKnownChannel(channel) {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	if a.configs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := a.configs.Delete(r.Context(), userID, channel); err != nil {
		writeError(w, http.StatusInternalServerError, "config delete failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTestSend versendet eine synthetische Test-Notification — ohne in
// notification_log zu schreiben. Für den „Test"-Button im UI.
func (a *NotificationsAPI) handleTestSend(w http.ResponseWriter, r *http.Request, userID string) {
	channel := r.PathValue("channel")
	if !isKnownChannel(channel) {
		writeError(w, http.StatusBadRequest, "unknown channel")
		return
	}
	switch channel {
	case notifications.ChannelGotify:
		if a.configs == nil || !a.configs.SecretsEnabled() {
			writeError(w, http.StatusServiceUnavailable, "secrets disabled")
			return
		}
		cfg, err := a.configs.GetGotify(r.Context(), userID)
		if errors.Is(err, storage.ErrNotificationConfigNotFound) {
			writeError(w, http.StatusNotFound, "no gotify config stored")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "config read failed", err)
			return
		}
		if a.gotifyTest == nil {
			writeError(w, http.StatusServiceUnavailable, "gotify sender not configured")
			return
		}
		if err := a.gotifyTest.TestConnection(r.Context(), *cfg); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case notifications.ChannelEmail:
		if a.emailTest == nil {
			writeError(w, http.StatusServiceUnavailable, "email sender not configured")
			return
		}
		// User-Mail aus User-Row holen.
		u, err := a.users.GetByID(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		if strings.TrimSpace(u.Email) == "" {
			writeError(w, http.StatusBadRequest, "no email address in user profile")
			return
		}
		ev := notifications.Event{
			Type:       "service.new-card", // benigner Test-Event
			BoardName:  "aBoard-Test",
			ActorEmail: "noreply@aboard.local",
			Snippet:    "Dies ist ein Verbindungstest. Wenn du diese Nachricht siehst, ist dein E-Mail-Kanal korrekt konfiguriert.",
		}
		if err := a.emailTest.SendOne(r.Context(), userID, u.Email, u.Name, ev); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusBadRequest, "unknown channel")
	}
}

// handleEmitEvent ist der Client-Trigger — der Browser ruft das nach
// jeder Karten-Mutation auf, die eine Notification auslösen soll. Body:
//
//	{
//	  "event_type":          "card.commented",
//	  "actor_user_id":       "<uuid>",                // optional, default: caller
//	  "card_id":             "<uuid>",
//	  "card_display_id":     287,
//	  "card_title":          "...",
//	  "board_id":            "<from URL>",
//	  "board_name":          "...",
//	  "snippet":             "kommentar-text (gekürzt)",
//	  "recipient_user_ids":  ["...", "..."],
//	  "mentioned_user_ids":  ["..."]                  // nur für card.mentioned
//	}
//
// Der Server-State bleibt opak — er validiert die Empfänger NICHT gegen
// die Board-Membership (ADR-0044). Die Karten-Logik im Frontend ist die
// Quelle der Wahrheit, wer für ein Event Empfänger ist.
func (a *NotificationsAPI) handleEmitEvent(w http.ResponseWriter, r *http.Request) {
	if a.bus == nil {
		writeError(w, http.StatusServiceUnavailable, "notifications bus not configured")
		return
	}
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	// Caller-Identity ist Pflicht — Anti-Loop und Audit hängen daran.
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeError(w, http.StatusForbidden, "forbidden: notifications require authentication")
		return
	}
	callerUser, err := a.users.GetByEmail(r.Context(), storage.NormalizeEmail(id.Email))
	if errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user lookup failed", err)
		return
	}
	// Rate-Limit (Kritiker-Lauf Sprint 42 [S]P2): authenticated-user-DoS
	// gegen Bus + DB. Identisches Limit zum protect-Limiter.
	if !a.limiter.allow(callerUser.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 60 emits per hour per user")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var body struct {
		EventType        string   `json:"event_type"`
		CardID           string   `json:"card_id"`
		CardDisplayID    int      `json:"card_display_id"`
		CardTitle        string   `json:"card_title"`
		BoardName        string   `json:"board_name"`
		Snippet          string   `json:"snippet"`
		RecipientUserIDs []string `json:"recipient_user_ids"`
		MentionedUserIDs []string `json:"mentioned_user_ids"`
		// RecipientEmails ist der ADR-0108-Weg fuer die card.*-Events
		// (Karte #339/#420): Das Frontend kennt die Karte und loest
		// Owner/Assignee-Label ueber `board.identities[].email` in
		// Mailadressen auf; der Server mappt sie via users.GetByEmail auf
		// UserIDs. Damit bleibt der Server-State opak (ADR-0044) — er
		// bekommt fertige Adressen und muss die Kartenstruktur nicht
		// interpretieren.
		//
		// Unbekannte Adressen werden STILL verworfen, nicht als 4xx
		// gemeldet: Eine Fehlermeldung „diese Adresse kennen wir nicht"
		// waere ein Existenz-Orakel ueber die Nutzertabelle, und der
		// Aufrufer koennte ohnehin nichts damit anfangen — eine Identity
		// ohne hinterlegten Account ist ein normaler Zustand.
		RecipientEmails []string `json:"recipient_emails"`
		// RecipientScope expandiert die Empfänger-Liste server-seitig.
		// Aktuell unterstützt: "board-members-viewer-plus" — alle Board-
		// Mitglieder (ACL viewer/editor/owner) außer dem Caller. Für Events
		// ohne karten-individuelle Empfänger (board.awaiting-user,
		// sprint.closed). Frontend hat im aktuellen Datenmodell kein
		// Owner↔UserID-Mapping (siehe ADR-0108) — dieser Scope schließt
		// die zwei ACL-basierten Events sauber ab. Für card.*-Events bleibt
		// das Mapping in Folgekarte #339.
		RecipientScope string `json:"recipient_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// Client-Trigger dürfen NICHT jeden Katalog-Typ auslösen (Sprint-59-
	// Kritiker [S] P2). `isKnownEventType` prüft gegen `AllEventTypes` —
	// darin stehen auch die beiden `system.*`-Typen, die ausschliesslich
	// server-intern entstehen (Backup-Fehlschlag, abgeschaltetes Subsystem).
	// Beide sind per Default auf BEIDEN Kanälen abonniert und tragen die
	// höchste Gotify-Dringlichkeit; niemand hat ein Abo, das er abbestellen
	// könnte. Zusammen mit `recipient_emails` liess sich daraus adressierte
	// Post mit frei wählbarem Text an jedes bekannte Konto erzeugen — die
	// Adressen liefert `/known-users` an jeden Angemeldeten, und ein eigenes
	// Board für den ACL-Guard legt `POST /boards` an.
	//
	// Der Code, der das ausnutzbar machte, ist NICHT neu: Vor
	// `recipient_emails` musste ein Aufrufer UUIDs kennen, die der Server
	// keinem Nicht-Admin ausliefert. Diese Unwissenheit war der faktische
	// Schutz. Der neue Vertrag ersetzt den unerratbaren Schlüssel durch
	// einen enumerierbaren — dieselbe Umkehrung wie bei F-702 im Vorsprint
	// (knowledge/topics/claude-projekt-framework.md, Fehlerklasse 27).
	if !isClientTriggerableEventType(body.EventType) {
		writeError(w, http.StatusBadRequest, "unknown event_type")
		return
	}
	// Recipient-Scope-Expansion (ADR-0108-Brücke): falls gesetzt und gültig,
	// fügt der Server eine ACL-basierte Liste in ExplicitRecipientUserIDs.
	// Bestehende RecipientUserIDs werden gemerged (Duplikate entfernt).
	// `recipient_emails` gehört NUR zu den karten-bezogenen Ereignissen
	// (Phase-2b-Verify, [S] P1). Der erste Fix zu F-722 hat die
	// `system.*`-Typen ausgesperrt und `board.awaiting-user`/`sprint.closed`
	// in der Allowlist gelassen — richtig, denn für die beiden gibt es einen
	// legitimen Weg. Nur ist es NICHT dieser: Sie sind für den ACL-basierten
	// `recipient_scope` gebaut (Karte #338), bei dem der SERVER die
	// Empfänger aus der Board-Mitgliedschaft expandiert. Mit frei gewählten
	// Adressen tragen sie dieselbe Angriffsklasse wie die system.*-Typen —
	// `board.awaiting-user` ist auf beiden Kanälen unbedingt abonniert und
	// hat Gotify-Dringlichkeit 7, und ein Frontend-Aufrufer existiert für
	// keinen von beiden.
	//
	// Die Trennung ist damit sauber: Wer eine KARTE meint, nennt Adressen;
	// wer ein BOARD meint, nennt einen Scope und lässt den Server auflösen.
	// `recipient_emails` gehört NUR zu den karten-bezogenen Ereignissen
	// (Phase-2b-Verify, [S] P1). Der erste Fix zu F-722 hat die
	// `system.*`-Typen ausgesperrt und `board.awaiting-user`/`sprint.closed`
	// in der Allowlist gelassen — richtig, denn für die beiden gibt es einen
	// legitimen Weg. Nur ist es NICHT dieser: Sie sind für den ACL-basierten
	// `recipient_scope` gebaut (Karte #338), bei dem der SERVER die
	// Empfänger aus der Board-Mitgliedschaft expandiert. Mit frei gewählten
	// Adressen tragen sie dieselbe Angriffsklasse wie die system.*-Typen —
	// `board.awaiting-user` ist auf beiden Kanälen unbedingt abonniert und
	// hat Gotify-Dringlichkeit 7, und ein Frontend-Aufrufer existiert für
	// keinen von beiden.
	//
	// Die Trennung ist damit sauber: Wer eine KARTE meint, nennt Adressen;
	// wer ein BOARD meint, nennt einen Scope und lässt den Server auflösen.
	//
	// **WARUM `recipient_user_ids` hier BEWUSST nicht mitgesperrt ist**
	// (zweiter Phase-2b-Verify): Die Asymmetrie ist echt und wurde gemeldet.
	// Zwei Gründe sprechen dagegen, sie hier zu schliessen.
	//
	//  1. **Nicht ausnutzbar.** Der Server liefert User-UUIDs an keinen
	//     Nicht-Admin aus — `/me` gibt `{name, email, is_admin}`,
	//     `/known-users` gibt Adressen ohne ID. Ein Aufrufer kann sie nicht
	//     kennen. Genau diese Unwissenheit war schon vor ADR-0108 der
	//     faktische Schutz hinter der ADR-0044-Entscheidung, Empfänger nicht
	//     gegen die Board-Mitgliedschaft zu validieren. `recipient_emails`
	//     war der Unterschied: Adressen sind über `/known-users`
	//     enumerierbar, UUIDs nicht.
	//  2. **Es ist dokumentiertes, getestetes Verhalten aus Karte #338.**
	//     `TestEmit_RecipientScope_MergesWithExplicit` prüft ausdrücklich,
	//     dass Scope und explizite UserIDs gemergt werden — inklusive eines
	//     Empfängers auf einem ANDEREN Board. Eine Sperre hier bricht dieses
	//     Feature; ein erster Versuch in genau dieser Nacharbeit hat den
	//     Test prompt rot gemacht. Ein bewusstes Feature stillschweigend zu
	//     brechen, weil ein Prüflauf eine Asymmetrie meldet, wäre der
	//     schlechtere Fehler.
	//
	// Die Verteidigung hängt damit an einer Nebenbedingung ausserhalb dieser
	// Datei: dass kein Endpunkt UserIDs ausliefert. **Wer je einen baut —
	// Admin-Export, Aktivitäts-Ansicht, Debug-Route —, macht diesen Pfad
	// scharf, ohne hier etwas zu ändern.** Als [[F-738]] im Backlog
	// festgehalten, damit die Bedingung benannt ist statt vergessen.
	//
	// Seit Sprint 61 ist sie zusätzlich GEPRÜFT statt nur behauptet:
	// `TestF738_KeinEndpunktLiefertUserIDsAus` (api/known_users_test.go)
	// hält fest, dass `/known-users` — damals der einzige Endpunkt, der fremde
	// Nutzer aufzählt — keine User-ID ausliefert. Seit Karte #699 zählt auch
	// `/boards/{id}/mitglieder` fremde Nutzer auf (nur Namen); dieselbe Wache
	// steht dort als `TestMitglieder699_F738_KeineUserIDs`. Fällt einer der
	// beiden, ist die Begründung oben hinfällig und die Prüfung gehört hierher.
	// Wer einen weiteren aufzählenden Endpunkt baut, ergänzt die Wache.
	if len(body.RecipientEmails) > 0 && !istKartenEreignis(body.EventType) {
		writeError(w, http.StatusBadRequest,
			"recipient_emails is only valid for card.* events; use recipient_scope")
		return
	}
	recipients := body.RecipientUserIDs
	if len(body.RecipientEmails) > 0 {
		recipients = mergeUserIDs(recipients, a.resolveEmails(r.Context(), body.RecipientEmails, callerUser.ID))
	}
	if body.RecipientScope != "" {
		expanded, scopeErr := a.resolveScope(r.Context(), body.RecipientScope, boardID, callerUser.ID)
		if scopeErr != nil {
			writeError(w, http.StatusBadRequest, scopeErr.Error())
			return
		}
		recipients = mergeUserIDs(recipients, expanded)
	}
	// Kritiker-Lauf Sprint 42 [S]P2: actor wird IMMER aus dem
	// authentifizierten Caller gesetzt — kein Override durch Body, sonst
	// kann ein Editor andere User mit gespoofter "actor"-Identität spammen
	// und/oder den Anti-Loop-Filter umgehen.
	ev := notifications.Event{
		Type:                     body.EventType,
		ActorUserID:              callerUser.ID,
		ActorEmail:               callerUser.Email,
		BoardID:                  boardID,
		BoardName:                body.BoardName,
		CardID:                   body.CardID,
		CardDisplayID:            body.CardDisplayID,
		CardTitle:                body.CardTitle,
		Snippet:                  body.Snippet,
		ExplicitRecipientUserIDs: recipients,
		MentionedUserIDs:         body.MentionedUserIDs,
	}
	a.bus.Emit(ev)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// resolveEmails uebersetzt Mailadressen in UserIDs (ADR-0108 Option A).
//
// Drei Eigenschaften, die alle drei aus einem konkreten Missbrauchsweg folgen:
//
//   - Der Caller wird IMMER herausgefiltert. Niemand bekommt Post ueber die
//     eigene Aktion (Auflage aus Entscheid des Maintainers vom 2026-08-19). Der Worker
//     filtert zwar ebenfalls gegen ActorUserID, aber sich auf eine
//     Absicherung zwei Schichten weiter zu verlassen heisst, sie beim
//     naechsten Umbau zu verlieren.
//   - Unbekannte Adressen fallen still heraus (siehe Feld-Kommentar oben).
//   - Die Liste ist begrenzt. Ohne Grenze koennte ein Aufrufer mit einem
//     einzigen Emit eine Datenbankabfrage pro Adresse ausloesen; das
//     60/h-Limit bremst die Anzahl der Emits, nicht ihre Groesse.
func (a *NotificationsAPI) resolveEmails(ctx context.Context, emails []string, callerUserID string) []string {
	/* Erst normalisieren und entdoppeln, DANN kappen (Sprint-59-Kritiker
	   [K] P3). Vorher lief das Kappen zuerst — Duplikate, Leerstrings und
	   Adressen, die sich nur in der Schreibweise unterschieden, verbrauchten
	   also Kontingent. Fehlschlag: Eine Karte, bei der dieselbe Person Owner
	   UND Assignee UND erwähnt ist, erzeugt Dopplungen; standen davon 50 in
	   der Liste, fiel der echte Empfänger dahinter still heraus. */
	eindeutig := make([]string, 0, len(emails))
	gesehen := make(map[string]bool, len(emails))
	for _, raw := range emails {
		mail := storage.NormalizeEmail(raw)
		if mail == "" || gesehen[mail] {
			continue
		}
		gesehen[mail] = true
		eindeutig = append(eindeutig, mail)
	}
	if len(eindeutig) > maxRecipientEmails {
		eindeutig = eindeutig[:maxRecipientEmails]
	}
	out := make([]string, 0, len(eindeutig))
	for _, mail := range eindeutig {
		u, err := a.users.GetByEmail(ctx, mail)
		if err != nil || u == nil {
			continue
		}
		if u.ID == callerUserID {
			continue
		}
		out = append(out, u.ID)
	}
	return out
}

// resolveScope expandiert ein recipient_scope-Token in eine UserID-Liste.
// Aktuell unterstützt:
//
//   - "board-members-viewer-plus": alle ACL-Einträge des Boards (viewer/
//     editor/owner) außer dem Caller. Emails, die in der `users`-Tabelle
//     keine Entsprechung haben (z. B. ausstehende Invites), werden still
//     übersprungen — sie bekommen die Notification nicht, weil sie sich
//     noch nie eingeloggt haben.
//
// Bei unbekanntem Scope: Fehler. Bei a.acl == nil: leere Liste (operator-
// kontrollierter Legacy-Bypass).
func (a *NotificationsAPI) resolveScope(ctx context.Context, scope, boardID, callerUserID string) ([]string, error) {
	if scope != "board-members-viewer-plus" {
		return nil, errors.New("unknown recipient_scope")
	}
	if a.acl == nil || a.users == nil {
		return nil, nil
	}
	entries, err := a.acl.ListByBoard(ctx, boardID)
	if err != nil {
		return nil, errors.New("acl lookup failed")
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !storage.ValidRole(e.Role) {
			continue
		}
		// ADR-0132 §6: ein Domain-Eintrag ist keine Adresse — Benachrichtigungen
		// gehen nur an persönlich eingetragene Nutzer.
		if storage.IstDomainSubjekt(e.UserEmail) {
			continue
		}
		u, uerr := a.users.GetByEmail(ctx, storage.NormalizeEmail(e.UserEmail))
		if errors.Is(uerr, storage.ErrUserNotFound) {
			continue
		}
		if uerr != nil {
			continue
		}
		if u.ID == callerUserID {
			continue
		}
		out = append(out, u.ID)
	}
	return out, nil
}

// mergeUserIDs vereinigt zwei UserID-Listen (Reihenfolge erste-Liste-zuerst,
// dann ergänzt). Duplikate werden entfernt.
func mergeUserIDs(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, u := range a {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, u := range b {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

// isKnownEventType prüft gegen notifications.AllEventTypes.
// isClientTriggerableEventType grenzt die Typen ein, die ein BROWSER auslösen
// darf. Bewusst eine eigene Liste und keine Ableitung mit Ausnahmen: Wer
// künftig einen server-internen Ereignistyp ergänzt, soll ihn hier NICHT
// automatisch freigeschaltet bekommen. Die Voreinstellung ist damit „nicht
// vom Client auslösbar" — die Richtung, in der ein Vergessen billig ist.
//
// Ein abgewiesener Typ bekommt dieselbe Antwort wie ein unbekannter
// (400 „unknown event_type"): Der Aufrufer soll aus der Fehlermeldung nicht
// lernen, dass es den Typ gibt und er nur nicht darf.
func isClientTriggerableEventType(t string) bool {
	switch t {
	case notifications.EventCardAssigned,
		notifications.EventCardCommented,
		notifications.EventCardMentioned,
		notifications.EventCardMoved,
		notifications.EventCardBlocked,
		notifications.EventBoardAwaiting,
		notifications.EventSprintClosed:
		return true
	default:
		// system.backup-failed, system.subsystem-off, service.new-card und
		// card.comment-deleted (ADR-0136) entstehen ausschliesslich
		// server-intern.
		return false
	}
}

// istKartenEreignis — die fünf Typen, die sich auf eine einzelne Karte
// beziehen und deren Empfänger das Frontend aus Owner/Assignees/Erwähnungen
// ableitet (ADR-0108). Nur für sie ist `recipient_emails` der vorgesehene
// Weg; alles andere hat entweder einen Server-Resolver oder entsteht
// server-intern.
func istKartenEreignis(t string) bool {
	switch t {
	case notifications.EventCardAssigned,
		notifications.EventCardCommented,
		notifications.EventCardMentioned,
		notifications.EventCardMoved,
		notifications.EventCardBlocked:
		return true
	default:
		return false
	}
}

func isKnownEventType(t string) bool {
	for _, k := range notifications.AllEventTypes {
		if k == t {
			return true
		}
	}
	return false
}

// isKnownChannel prüft gegen notifications.AllChannels.
func isKnownChannel(c string) bool {
	for _, k := range notifications.AllChannels {
		if k == c {
			return true
		}
	}
	return false
}
