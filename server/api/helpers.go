package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// writeJSON serialisiert payload als JSON-Response. Content-Type wird hier
// gesetzt; X-Content-Type-Options + Cache-Control liefert die Middleware.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError gibt einen einfachen Fehler im JSON-Format zurück. Bewusst
// schlicht — keine Stack-Traces oder interne Pfade an den Client.
//
// F-252: `err` ist optional (variadic, damit die ~300 Bestandsaufrufer ohne
// Änderung weiterkompilieren) und landet — falls gesetzt — NUR im Server-Log,
// nie in der HTTP-Antwort. Vorher verschwand der Roh-Fehler an den meisten
// 500er-Stellen spurlos: der Client bekam "create failed", und im Log stand
// nichts Weiterführendes. Aufrufer mit einem echten zugrundeliegenden Fehler
// übergeben ihn als viertes Argument; reine Validierungs-/Berechtigungsfehler
// (kein Go-error dahinter, z. B. "missing board id") bleiben dreiargumentig.
//
// Nur err[0] wird ausgewertet — mehr als ein Fehler ist nie sinnvoll, die
// Variadic-Form dient ausschliesslich der Rückwärtskompatibilität.
//
// Log-Format folgt der im Server etablierten Konvention (log.Printf mit
// "aboard-server: "-Präfix, siehe z. B. boards.go, main.go, worker.go).
func writeError(w http.ResponseWriter, status int, msg string, err ...error) {
	if len(err) > 0 && err[0] != nil {
		// redactForLog, NICHT %v: ein Fehlerwert ist unkontrollierter Text
		// aus fremden Bibliotheken. Der Sprint-57-Kritiker hat gezeigt, dass
		// hier sonst der Google-API-Key im Klartext landet (er steht als
		// ?key=… in der URL, und *url.Error traegt die volle URL). Siehe
		// logredact.go fuer die drei abgedeckten Klassen und ihre Grenzen.
		log.Printf("aboard-server: %d %s: %s", status, msg, redactForLog(err[0]))
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// etag formatiert einen updated_at-Wert als Strong-ETag (RFC 7232).
// Beispiel: 1715703123456 → `"1715703123456"` (mit Quotes als ETag-Format).
func etag(updatedAt int64) string {
	return `"` + strconv.FormatInt(updatedAt, 10) + `"`
}

// parseIfMatch parst den If-Match-Header und liefert den darin steckenden
// updated_at-Wert. Akzeptiert sowohl `"123"` (gequotete Form, RFC 7232) als
// auch `123` (lax). Mehrere ETags oder `*` werden in Phase 1 abgelehnt.
//
// Leerer Header → (0, nil): kein If-Match gesetzt; Aufrufer entscheidet,
// ob das ein Fehler ist (PUT verlangt es; Force-Update wäre 0).
func parseIfMatch(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if raw == "*" {
		return 0, errors.New(`If-Match "*" not supported in phase 1`)
	}
	// Mehrere ETags trennt RFC 7232 mit Komma — wir akzeptieren nur einen.
	if strings.Contains(raw, ",") {
		return 0, errors.New("If-Match accepts a single ETag in phase 1")
	}
	if strings.HasPrefix(raw, `W/`) {
		return 0, errors.New("weak ETags not supported")
	}
	val := strings.TrimPrefix(strings.TrimSuffix(raw, `"`), `"`)
	v, err := strconv.ParseInt(val, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("If-Match malformed: %q", raw)
	}
	return v, nil
}

// readEnvelope dekodiert den Request-Body in den Envelope und gibt parallel
// die Rohbytes zurück. Die Rohbytes landen 1:1 als state_json — der Server
// validiert nur die Top-Level-Felder (version, board.id, board.title) und
// hält den Rest opak (ADR-0044).
//
// Body-Limit greift via withBodyLimit-Middleware: zu große Bodies werden
// hier mit http.MaxBytesError sichtbar und als 413 nach oben gereicht.
func readEnvelope(r *http.Request) (raw []byte, env boardEnvelope, err error) {
	if r.Body == nil {
		return nil, env, errors.New("empty body")
	}
	raw, err = io.ReadAll(r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return nil, env, fmt.Errorf("request body exceeds %d bytes", mb.Limit)
		}
		return nil, env, fmt.Errorf("read body: %w", err)
	}
	if len(raw) == 0 {
		return nil, env, errors.New("empty body")
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, env, fmt.Errorf("invalid json: %w", err)
	}
	return raw, env, nil
}

// isUniqueViolation erkennt UNIQUE-/PRIMARY-Constraint-Verletzungen aus
// modernc/sqlite anhand des Error-Texts. Schmaler, aber für unsere zwei
// betroffenen Pfade (POST /boards mit existierender id) ausreichend.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "primary key")
}
