package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// Build-Metadaten. Alle drei werden im Dockerfile per
// `-ldflags "-X github.com/Andiministrator/aBoard/server/api.<Name>=<Wert>"` gesetzt
// (siehe server/Dockerfile und die Build-Pipeline); ohne ldflags bleiben
// die Defaults stehen — dann läuft eine lokal gebaute Binary.
//
// Karte #347: Diese drei Variablen sind die EINZIGE Quelle für Versionsangaben
// im Server. main.aboardVersion leitet sich davon ab (war vorher eine zweite,
// nie gesetzte Variable — Backup-Metadaten und der Admin-Status-Tab meldeten
// deshalb dauerhaft "dev", obwohl der Build eine Version hatte).
var (
	BuildVersion = "dev"
	BuildCommit  = "unknown"
	BuildDate    = "unknown"
)

// VersionInfo liefert die Build-Metadaten als Map — für /healthz, den
// Admin-Status und den Startlog dieselbe Quelle.
func VersionInfo() map[string]string {
	return map[string]string{
		"version":    BuildVersion,
		"commit":     BuildCommit,
		"build_date": BuildDate,
	}
}

// VersionLine ist die menschenlesbare Einzeiler-Form, z. B.
// "v1.34.1 (commit 81e9e1d, build 2026-08-18T08:30:00Z)".
func VersionLine() string {
	return BuildVersion + " (commit " + BuildCommit + ", build " + BuildDate + ")"
}

// HealthHandler liefert einen JSON-Statusbericht: { ok, db, version }.
// "db" ist "up", wenn ein SELECT 1 die Verbindung bestätigt, sonst "down".
//
// HEAD ist ausdrücklich erlaubt (F-196): Docker `HEALTHCHECK`, K8s
// `livenessProbe.httpGet` und die Upstream-Prüfungen von Caddy/Traefik senden
// teils HEAD statt GET. Ein 405 darauf sieht für den Orchestrator wie ein
// kranker Container aus, obwohl der Server gesund ist. Der Body-Aufbau bleibt
// identisch — `net/http` verwirft ihn bei HEAD von selbst, und der
// Status-Code (200 vs. 503) ist genau das, was ein Healthcheck auswertet.
func HealthHandler(db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		// Karte #347: commit + build_date zusätzlich, damit die Drift-Erkennung
		// (ADR-0085 / #270) ohne Registry-Abfrage auskommt — /healthz ist
		// unauthentifiziert erreichbar und sagt jetzt, WELCHER Build läuft.
		// Bewusst hier statt in einem eigenen /version-Endpoint: ein zweiter
		// Endpoint mit demselben Inhalt wäre eine Drift-Quelle mehr.
		resp := map[string]any{
			"ok":         true,
			"db":         "up",
			"version":    BuildVersion,
			"commit":     BuildCommit,
			"build_date": BuildDate,
		}
		status := http.StatusOK

		var probe int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&probe); err != nil || probe != 1 {
			resp["ok"] = false
			resp["db"] = "down"
			status = http.StatusServiceUnavailable
		}

		// Defense-in-depth-Header — werden ab #71 von einer Middleware geliefert,
		// damit alle REST-Handler dieselben Defaults erben.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
