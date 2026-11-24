package api

// spam.go — Server-side Mail-Spam-Blocklist-Mutation (#294 Etappe 3, ADR-0092 §5).
//
// POST /mail-spam-report
//   { "senderEmail": "spammer@example.com" }                 → Sender auf Blocklist
//   { "senderEmail": "spammer@example.com", "undo": true }   → Sender entfernen
//
// Liegt außerhalb /boards/* → ACLGuard greift NICHT; statt dessen prüft der
// Handler selbst, ob der Caller einen identifizierten User trägt UND
// editor+ am Service-Board hat. Bewusst NICHT admin-gegated: die Aktion
// gehört zum Service-Board-User-Workflow ("🚫 Als Spam markieren" am
// Karten-Modal, ADR-0092 §5), nicht zum Admin-Bereich.
//
// Rate-Limit: 30 Mutationen/Minute pro User — gegen versehentliche Bulk-
// Klick-Floods + DoS-Defense-in-depth.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// SpamAPI bündelt die Abhängigkeiten für /mail-spam-report. Erhält Refs auf
// SpamRepo (Blocklist), AclRepo (Berechtigungsprüfung am Service-Board) und
// UserRepo (Identitäts-Auflösung).
type SpamAPI struct {
	spam    *storage.SpamRepo
	acl     *storage.AclRepo
	users   *storage.UserRepo
	limiter *rateLimiter
}

func NewSpamAPI(spam *storage.SpamRepo, acl *storage.AclRepo, users *storage.UserRepo) *SpamAPI {
	return &SpamAPI{
		spam:    spam,
		acl:     acl,
		users:   users,
		limiter: newRateLimiter(30, time.Minute),
	}
}

// Register hängt die Route an. Liegt außerhalb /boards/* — ACLGuard greift
// nicht; eigene Identitäts-/Editor-Prüfung im Handler.
func (a *SpamAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /mail-spam-report", WithDefaults(http.HandlerFunc(a.handleReport)))
}

type spamReportRequest struct {
	SenderEmail string `json:"senderEmail"`
	Undo        bool   `json:"undo,omitempty"`
}

func (a *SpamAPI) handleReport(w http.ResponseWriter, r *http.Request) {
	if a.spam == nil {
		writeError(w, http.StatusServiceUnavailable, "spam repo not initialised")
		return
	}

	id := IdentityFromContext(r.Context())
	if id == nil {
		writeError(w, http.StatusForbidden, "forbidden: spam-report requires authentication")
		return
	}
	callerEmail := storage.NormalizeEmail(id.Email)
	if callerEmail == "" {
		writeError(w, http.StatusForbidden, "forbidden: no identity email")
		return
	}
	u, err := a.users.GetByEmail(r.Context(), callerEmail)
	if errors.Is(err, storage.ErrUserNotFound) {
		writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user lookup failed", err)
		return
	}

	// editor+ am Service-Board nötig — ein viewer darf nicht in eine
	// Server-weite Blocklist schreiben.
	if a.acl != nil {
		role, found, aerr := a.acl.GetRole(r.Context(), storage.ServiceBoardID, callerEmail)
		if aerr != nil {
			writeError(w, http.StatusInternalServerError, "acl lookup failed", aerr)
			return
		}
		if !found || (role != storage.RoleOwner && role != storage.RoleEditor) {
			writeError(w, http.StatusForbidden, "forbidden: editor+ on service-board required")
			return
		}
	}

	if !a.limiter.allow(u.ID) {
		writeError(w, http.StatusTooManyRequests, "rate limit: 30 spam-reports per minute per user")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body spamReportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// Sender-Adress-Validierung — keine syntaxlosen Müll-Strings auf die
	// Blocklist. Wir nutzen mail.ParseAddress (RFC 5322), das ist im
	// EmailAPI schon präzedent (email.go).
	addr, err := mail.ParseAddress(body.SenderEmail)
	if err != nil || addr.Address == "" {
		writeError(w, http.StatusBadRequest, "invalid senderEmail")
		return
	}
	sender := storage.NormalizeEmail(addr.Address)

	if body.Undo {
		if err := a.spam.RemoveSpamSender(r.Context(), sender); err != nil {
			writeError(w, http.StatusInternalServerError, "spam remove failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sender": sender, "listed": false})
		return
	}
	if err := a.spam.AddSpamSender(r.Context(), sender, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "spam add failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sender": sender, "listed": true})
}

// Compile-time check: SpamAPI implementiert die Standard-Register-Schnittstelle
// (defensiv, dient auch als Doku).
var _ interface {
	Register(*http.ServeMux)
} = (*SpamAPI)(nil)
