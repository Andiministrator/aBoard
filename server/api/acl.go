package api

// acl.go — ACL-Enforcement-Middleware (Karte #198, ADR-0059 §1/§2).
//
// Sitzt in der Kette NACH withForwardAuth: liest die Identität aus dem
// Request-Context und erzwingt rollenbasierten Zugriff auf die /boards-API.
//
//   - Keine Identität im Context → No-op, Request unverändert durchgelassen.
//     Das ist der Legacy-Bypass (ADR-0059 §2): ohne ABOARD_TRUST_FORWARD_AUTH
//     exakt heutiges Verhalten (auth-loser Einzelbetrieb). Im strengen Modus
//     (ABOARD_TRUST_FORWARD_AUTH=1, ADR-0129 §2, F-1124) kommt hier für
//     /boards* praktisch keine leere Identität mehr an: withForwardAuth weist
//     genau diesen Fall bereits mit 401 ab. Der Bypass-Zweig bleibt trotzdem
//     bestehen — er greift weiterhin bei Trust AUS und auf den beiden
//     offenen Pfaden (/healthz, /ical/calendar.ics), die aber ohnehin nicht
//     unter /boards liegen.
//   - Identität vorhanden → Open-to-authenticated (ADR-0066, revidiert
//     ADR-0059 §1): jede nicht-leere, normalisierte Remote-Email ist ein
//     vollwertiger Nutzer. Bei ECHTEM Erstkontakt (keine users-Row) wird
//     atomar users-Row + persönliches Board + owner-ACL provisioniert
//     (BootstrapNewUser). Danach Rollenprüfung pro Board (owned ∪ shared
//     bleibt — nur das pauschale len(boards)==0→403-Gate entfällt).
//     Remote-Email leer → weiterhin 403 (email-geschlüsseltes Modell).
//
// Nur /boards* wird erzwungen — inklusive Subressourcen: der SSE-Stream
// (GET /boards/{id}/events) und Attachments (/boards/{id}/attachments/...)
// liegen unter /boards und werden mit-erfasst (GET = viewer+, mutierend =
// editor+). Frontend (/) und Health (/healthz) bleiben außen vor: die
// SPA-Shell trägt keine Daten, und Health-/Caddy-Checks senden keine
// Remote-*-Header (→ Identität nil → Bypass), bleiben also erreichbar.

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/google/uuid"
)

// Rollen-Rang für Mindest-Rollen-Prüfungen: viewer < editor < owner.
var roleRank = map[string]int{
	storage.RoleViewer: 1,
	storage.RoleEditor: 2,
	storage.RoleOwner:  3,
}

// ACLGuard kapselt die ACL-Enforcement-Middleware. acl/users stammen aus
// dem storage-Paket (#197). Der Guard ist autorisierungs-agnostisch, solange
// keine Identität im Context liegt — die Aktivierung hängt allein an der
// Forward-Auth-Schicht (ADR-0059 §2), nicht an einer eigenen Env-Var.
type ACLGuard struct {
	acl       *storage.AclRepo
	users     *storage.UserRepo
	bootstrap *storage.BootstrapRepo
}

func NewACLGuard(acl *storage.AclRepo, users *storage.UserRepo, bootstrap *storage.BootstrapRepo) *ACLGuard {
	return &ACLGuard{acl: acl, users: users, bootstrap: bootstrap}
}

// Middleware wickelt next ein. Reihenfolge in main.go: NACH
// WithForwardAuthIfEnabled (Identität muss bereits im Context liegen).
func (g *ACLGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := IdentityFromContext(r.Context())
		if identity == nil {
			// Kritiker Sprint 70, K-A11: Der Guard verlässt sich nicht darauf,
			// dass vor ihm withForwardAuth sitzt. Ist Trust an, hat ein
			// Request ohne Identität hier nichts verloren — ausser auf den
			// offenen Pfaden. Das schützt jede künftige Kette (etwa den
			// Agenten-Listener aus ADR-0129 §3), die den Guard ohne die
			// Forward-Auth-Middleware wiederverwendet.
			// ADR-0148 §1 (Karte #1051): Im OIDC-Betrieb gilt dasselbe — der
			// Legacy-Bypass darf dort nie erreichbar sein, auch wenn eine
			// Kette die Sitzungsweiche vergisst.
			if strengeAnmeldung() && !ohneAnmeldungErlaubt(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized: identity required")
				return
			}
			// Legacy-Bypass (ADR-0059 §2): nur noch im auth-losen
			// Einzelbetrieb (weder Forward-Auth noch OIDC) und auf den offenen
			// Pfaden.
			next.ServeHTTP(w, r)
			return
		}

		// Nur die /boards-API wird erzwungen (siehe Datei-Header).
		segs := pathSegments(r.URL.Path)
		if len(segs) == 0 || segs[0] != "boards" {
			next.ServeHTTP(w, r)
			return
		}

		// F-260: zentrale Email-Normalisierung (TrimSpace+ToLower). Muss
		// exakt der Form entsprechen, in der ACL-Einträge gespeichert sind,
		// sonst Casing-Mismatch Authelia↔ACL → Owner-Lockout.
		email := storage.NormalizeEmail(identity.Email)
		if email == "" {
			// Forward-Auth aktiv, aber kein Remote-Email → ACL ist
			// email-basiert (ADR-0059 §1), ohne Email keine Autorisierung.
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}

		ctx := r.Context()

		// ADR-0066 (revidiert ADR-0059 §1): Open-to-authenticated. Das
		// pauschale len(boards)==0→403-Gate entfällt — eine nicht-leere,
		// normalisierte Email genügt. Bei ECHTEM Erstkontakt (keine
		// users-Row) wird atomar ein persönliches Board samt owner-ACL
		// provisioniert: BootstrapNewUser ist nebenläufigkeitssicher
		// (BEGIN IMMEDIATE, In-Tx-Re-Check) und resurrektiert NICHT, wenn
		// der Nutzer sein Board später löscht (Trigger = users-Row neu,
		// nicht „0 Boards"). Der GetByEmail-Vorabcheck spart bei
		// Bestandsnutzern den Tx-Overhead; die Korrektheit unter
		// parallelen Erst-Requests garantiert der In-Tx-Check.
		anzeige := identity.AnzeigeName()
		if u, gerr := g.users.GetByEmail(ctx, email); errors.Is(gerr, storage.ErrUserNotFound) {
			if _, berr := g.bootstrap.BootstrapNewUser(ctx, uuid.NewString(), uuid.NewString(), email, anzeige); berr != nil {
				writeError(w, http.StatusInternalServerError, "user provisioning failed", berr)
				return
			}
		} else if gerr != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", gerr)
			return
		} else if anzeige != "" && u.Name != anzeige {
			// Karte #577: Bestandsnamen nachziehen. Bis v1.42.0 landete hier
			// Remote-User — hinter oauth2-proxy/Google die numerische
			// Konto-ID. Geschrieben wird nur bei Abweichung, also einmal pro
			// Namenswechsel, nicht bei jedem Poll. Ein Fehler kostet nur die
			// Anzeige; der Request läuft weiter.
			if serr := g.users.SetName(ctx, u.ID, anzeige); serr != nil {
				log.Printf("aboard-server: users.name für %s nicht nachgezogen: %v", u.ID, serr)
			}
		}

		// Collection-Routen (/boards exakt): GET = Liste (im Handler nach
		// ACL gefiltert), POST = Create (Handler vergibt owner-ACL). Der
		// globale ACL-Nachweis oben genügt hier.
		if len(segs) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// Board-bezogene Route: /boards/{id} oder tiefer.
		boardID := segs[1]
		role, found, err := g.acl.GetRole(ctx, boardID, email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl check failed", err)
			return
		}
		if !found {
			// Authentifiziert + global zugriffsberechtigt, aber kein
			// ACL-Eintrag für DIESES Board (ADR-0059 §1: 403, nicht 404).
			writeError(w, http.StatusForbidden, "forbidden: no access to board")
			return
		}

		if !roleAllows(role, r.Method, len(segs) > 2) {
			writeError(w, http.StatusForbidden, "forbidden: insufficient role")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// roleAllows entscheidet, ob role die Methode auf der Route ausführen darf.
//
// deeper == false → /boards/{id} selbst:
//
//	GET/HEAD → viewer+ · PUT/PATCH → editor+ · DELETE → owner · sonst verboten
//
// deeper == true  → Subressource (z. B. /boards/{id}/attachments/...):
//
//	GET/HEAD → viewer+ · mutierend → editor+
//
// (Subressourcen-Semantik bewusst grob — feinere Attachment-Rollen sind
// Sache von #74/Folgekarten, nicht #198.)
func roleAllows(role, method string, deeper bool) bool {
	rank := roleRank[role]
	if rank == 0 {
		return false // unbekannte Rolle — fail-closed
	}
	switch method {
	case http.MethodGet, http.MethodHead:
		return rank >= roleRank[storage.RoleViewer]
	}
	if deeper {
		// Jede Mutation an einer Subressource: editor+.
		return rank >= roleRank[storage.RoleEditor]
	}
	switch method {
	case http.MethodPut, http.MethodPatch, http.MethodPost:
		return rank >= roleRank[storage.RoleEditor]
	case http.MethodDelete:
		return rank >= roleRank[storage.RoleOwner]
	}
	return false
}

// pathSegments zerlegt einen URL-Pfad in nicht-leere Segmente.
// "/boards/default/attachments" → ["boards","default","attachments"].
func pathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
