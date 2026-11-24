package api

// csrf.go — Schutz gegen Cross-Site Request Forgery (Sprint-72-Abschluss,
// Kritiker S72-1).
//
// Der Menschen-Listener authentisiert über das Sitzungs-Cookie des
// vorgeschalteten Proxys (Forward-Auth). Ohne diesen Schutz konnte eine
// fremde Seite, die ein angemeldeter Nutzer öffnet, schreibende Requests
// auslösen — etwa ein Formular-POST mit text/plain auf /boards/{id}/ops, der
// seit Sprint 72 der normale Schreibweg des Browsers ist.
//
// Umgesetzt mit http.CrossOriginProtection (Go ≥ 1.25): Schreibende Methoden
// werden abgewiesen, wenn der Browser sie als fremd kennzeichnet
// (Sec-Fetch-Site: cross-site oder same-site) oder — bei älteren Browsern —
// der Host im Origin-Header nicht zum Host der Anfrage passt. GET, HEAD und
// OPTIONS bleiben erlaubt. Requests ohne diese Header (curl, Agenten,
// Werkzeuge) sind keine Browser-Requests und laufen durch; sie tragen kein
// fremdes Cookie mit sich.
//
// Auch „same-site“ wird abgewiesen: Unter einer gemeinsamen Domain
// (board.firma.de neben wiki.firma.de) ist eine andere Anwendung derselben
// Site keine vertrauenswürdige Quelle.

import "net/http"

// MitCSRFSchutz umhüllt den Handler des Menschen-Listeners.
func MitCSRFSchutz(next http.Handler) http.Handler {
	schutz := http.NewCrossOriginProtection()
	schutz.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "forbidden: cross-origin request rejected")
	}))
	return schutz.Handler(next)
}
