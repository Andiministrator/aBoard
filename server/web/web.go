// Package web liefert das eingebettete Server-Frontend aus (ADR-0044/0051,
// behebt F-248). Der Build-Output von `bun run build:server-frontend` wird
// nach server/web/dist/ kopiert und via //go:embed ins Single-Binary
// gebacken. dist/index.html ist KEIN Platzhalter, sondern das zuletzt gebaute
// index.html; die dazugehörigen dist/assets/ sind nicht eingecheckt. Ein
// lokales `go build` ohne vorheriges `bun run build:server` liefert deshalb
// ein Frontend ohne Skripte (F-1858). Der Docker-Build baut beides frisch.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// all: bindet auch Dateien mit `_`/`.`-Präfix ein. Compile-Zeit-Constraint:
// dist/ muss ≥1 Datei enthalten — daher das eingecheckte dist/index.html.
//
//go:embed all:dist
var distFS embed.FS

// Handler liefert das eingebettete Frontend. Registriert in main.go an "/".
// Spezifischere Muster (/healthz, GET /boards…) gewinnen via
// Go-1.22-ServeMux-Precedence — die API bleibt ungeschattet. Existiert die
// angefragte Datei nicht im Embed-FS, wird index.html geliefert
// (SPA-Deep-Link, Status 200).
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// dist ist zur Compile-Zeit eingebettet — nur bei Build-Defekt erreichbar.
		panic("web: eingebettetes dist nicht lesbar: " + err.Error())
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("web: dist/index.html fehlt im Embed-FS: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// ADR-0133 §7 (Karte #672): niemand bettet die Oberfläche in eine
		// fremde Seite ein. Dieselbe Zeichenfolge wie api.FrameAncestorsPolicy
		// (web importiert api nicht, um keine Abhängigkeit zu schaffen).
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		name := r.URL.Path
		if name == "" || name == "/" {
			name = "index.html"
		} else {
			name = name[1:] // führenden Slash für fs.FS-Pfad entfernen
		}
		if f, err := sub.Open(name); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}
