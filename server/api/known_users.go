package api

// known_users.go — GET /known-users (ADR-0066 §3, Karte #224).
//
// Liefert das globale bekannte-Nutzer-Verzeichnis: users ∪ DISTINCT acl.user_email.
// Basis für den Share-Picker im Frontend (Autocomplete + aktiv/ausstehend-Marker).
//
// Auth-Verhalten (spiegelt meHandler, ADR-0066 §3):
//   - Forward-Auth aktiv + nicht-leere Email → 200 + Liste.
//   - Forward-Auth aktiv, aber Email leer → 403 (email-geschlüsseltes Modell,
//     ohne Email keine Identität — analog acl.go:74-78).
//   - Kein Forward-Auth (Legacy-Bypass, Identität nil) → 200 + Liste
//     (im Legacy-Betrieb sind users/acl leer → i.d.R. leere Liste; konsistent
//     mit „Legacy = alles offen, keine Identität").
//
// Der Endpoint liegt AUSSERHALB /boards/* — der ACLGuard greift für Nicht-
// /boards-Pfade nicht (acl.go:68-71); dieser Handler prüft selbst.
//
// Response: {"users":[{"email":"…","name":"…","status":"aktiv|ausstehend"}],
//            "domains":["…"]}
// users sortiert nach email; domains = ABOARD_ACL_DOMAINS (ADR-0132).

import (
	"net/http"

	"github.com/Andiministrator/aBoard/server/storage"
)

// knownUsersHandler implementiert GET /known-users.
type knownUsersHandler struct {
	acl *storage.AclRepo
}

// RegisterKnownUsersHandler hängt GET /known-users an einen ServeMux.
func RegisterKnownUsersHandler(mux *http.ServeMux, acl *storage.AclRepo) {
	h := &knownUsersHandler{acl: acl}
	mux.Handle("GET /known-users", WithDefaults(http.HandlerFunc(h.handle)))
}

func (h *knownUsersHandler) handle(w http.ResponseWriter, r *http.Request) {
	id := IdentityFromContext(r.Context())

	if id != nil {
		// Forward-Auth aktiv — Email-Prüfung (ADR-0066 §3).
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
	}
	// Legacy-Bypass (id == nil): durchlassen, Liste ist ohnehin leer.

	subjects, err := h.acl.ListKnownSubjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "known-users: query failed", err)
		return
	}

	// Response-Typ für den JSON-Output.
	type userEntry struct {
		Email  string `json:"email"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	entries := make([]userEntry, 0, len(subjects))
	for _, s := range subjects {
		entries = append(entries, userEntry{
			Email:  s.Email,
			Name:   s.Name,
			Status: s.Status,
		})
	}

	// ADR-0132 §7: erlaubte Domains für den Picker („Alle mit @<domain>“).
	// Leer, solange der Betreiber ABOARD_ACL_DOMAINS nicht setzt.
	writeJSON(w, http.StatusOK, map[string]any{"users": entries, "domains": storage.ErlaubteACLDomains()})
}
