package api

import (
	"net/http"
)

// MaxBodyBytes begrenzt die Größe eingehender JSON-Bodies. Phase 1: 10 MB —
// deutlich über dem 250-KB-Bundle-Budget, deutlich unter Attachment-Größen
// (Attachments laufen separat über multipart in Karte #74).
const MaxBodyBytes = 10 * 1024 * 1024

// withSecurityHeaders setzt die Defense-in-depth-Header, die der Health-
// Handler in Sprint 21 inline trug (F-195). Ab Karte #71 sind sie zentral,
// damit alle Handler die Defaults erben.
//
// Bewusst NICHT gesetzt:
//   - HSTS: das ist Sache des Reverse-Proxies (ADR-0044 §"Auth")
//   - X-Frame-Options: durch CSP frame-ancestors ersetzt (s. u.)
//   - sonstige CSP: für die JSON-API irrelevant; die Frontend-Auslieferung ist
//     separater Pfad (server/web, setzt dort ebenfalls frame-ancestors)
//
// Gesetzt wird `Content-Security-Policy: frame-ancestors 'self'` (ADR-0133 §7,
// Karte #672): Seit der Open-WebUI-Anbindung gibt es einen Embedding-Fall, den
// es zu verhindern gilt — niemand soll die Board-Oberfläche heimlich in eine
// fremde Seite einbetten. Auf JSON-Antworten ist die Direktive wirkungslos,
// aber gefahrlos; eine Quelle für beide Pfade ist FrameAncestorsPolicy.
// FrameAncestorsPolicy ist der CSP-Wert gegen fremdes Einbetten (ADR-0133 §7).
const FrameAncestorsPolicy = "frame-ancestors 'self'"

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", FrameAncestorsPolicy)
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit kappt Request-Bodies auf MaxBodyBytes. Größere Bodies werden
// vom Decoder als Fehler bemerkt (http.MaxBytesError) und vom Handler als
// 413 Payload Too Large beantwortet.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// WithDefaults kombiniert die Standard-Middleware-Stack für alle REST-
// Handler. Reihenfolge: Body-Limit zuerst (closest to handler) — damit der
// Security-Header-Wrap auch im Fehlerpfad noch greift.
func WithDefaults(h http.Handler) http.Handler {
	return withSecurityHeaders(withBodyLimit(h))
}

// WithSecurityHeaders setzt nur die Security-Header — ohne Body-Limit.
// Für Attachment-Upload-Routen, die ihr eigenes MaxBytesReader setzen (#74).
func WithSecurityHeaders(h http.Handler) http.Handler {
	return withSecurityHeaders(h)
}
