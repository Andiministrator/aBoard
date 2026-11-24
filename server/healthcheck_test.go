package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// gesundheitsHandler baut einen minimalen /healthz-Handler, der EXAKT die
// Antwortform von server/api/health.go nachbildet ({"ok": true/false, ...}) —
// ohne die echte DB-Anbindung, denn checkHealth prüft nur den JSON-Body.
func gesundheitsHandler(ok bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "db": "up"})
	})
}

// TestCheckHealth_TCP_Erfolg prüft den Erfolgsfall über echtes TCP
// (httptest.Server) — der Kanal, den ABOARD_ADDR ohne ABOARD_UNIX_SOCKET
// auswählt.
func TestCheckHealth_TCP_Erfolg(t *testing.T) {
	srv := httptest.NewServer(gesundheitsHandler(true))
	defer srv.Close()

	// srv.Listener.Addr() liefert "127.0.0.1:PORT" — bereits ein gültiger
	// Host:Port, normalizeHealthHost lässt ihn unverändert.
	addr := srv.Listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := checkHealth(ctx, "", addr); err != nil {
		t.Fatalf("checkHealth (TCP, ok=true) = %v, erwartet nil", err)
	}
}

// TestCheckHealth_TCP_OkFalse prüft, dass ein "ok":false im Body als Fehler
// gilt — auch wenn der HTTP-Status technisch verarbeitbar war (hier: 503,
// wie es server/api/health.go bei DB-Ausfall liefert).
func TestCheckHealth_TCP_OkFalse(t *testing.T) {
	srv := httptest.NewServer(gesundheitsHandler(false))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := checkHealth(ctx, "", addr)
	if err == nil {
		t.Fatal("checkHealth (TCP, ok=false) = nil, erwartet Fehler")
	}
}

// TestCheckHealth_TCP_NichtErreichbar prüft den Fall "kein Server lauscht" —
// ein geschlossener Listener liefert eine Adresse, unter der garantiert
// niemand antwortet.
func TestCheckHealth_TCP_NichtErreichbar(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listener für Adressreservierung: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // Adresse bleibt kurzzeitig frei, aber es antwortet niemand mehr

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := checkHealth(ctx, "", addr); err == nil {
		t.Fatal("checkHealth (TCP, nicht erreichbar) = nil, erwartet Fehler")
	}
}

// TestCheckHealth_UnixSocket_Erfolg prüft den Kanal, den ABOARD_UNIX_SOCKET
// auswählt — ein echter Unix-Domain-Socket in t.TempDir() (kein /tmp, siehe
// Auftragsrahmen: t.TempDir() ist für Go-Tests ausdrücklich erlaubt).
func TestCheckHealth_UnixSocket_Erfolg(t *testing.T) {
	sockPath := kurzerSocketPfad(t, "aboard.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("unix-Listener: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: gesundheitsHandler(true)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := checkHealth(ctx, sockPath, ""); err != nil {
		t.Fatalf("checkHealth (unix, ok=true) = %v, erwartet nil", err)
	}
}

// TestCheckHealth_UnixSocket_OkFalse spiegelt TestCheckHealth_TCP_OkFalse für
// den Socket-Kanal.
func TestCheckHealth_UnixSocket_OkFalse(t *testing.T) {
	sockPath := kurzerSocketPfad(t, "aboard.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("unix-Listener: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: gesundheitsHandler(false)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := checkHealth(ctx, sockPath, ""); err == nil {
		t.Fatal("checkHealth (unix, ok=false) = nil, erwartet Fehler")
	}
}

// TestCheckHealth_UnixSocket_NichtErreichbar prüft einen Socket-Pfad, unter
// dem nichts lauscht (Datei existiert nicht einmal).
func TestCheckHealth_UnixSocket_NichtErreichbar(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "existiert-nicht.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := checkHealth(ctx, sockPath, ""); err == nil {
		t.Fatal("checkHealth (unix, nicht erreichbar) = nil, erwartet Fehler")
	}
}

// TestNormalizeHealthHost nagelt die Host-Normalisierung fest: leerer/
// Wildcard-Host → 127.0.0.1, sonst unverändert.
func TestNormalizeHealthHost(t *testing.T) {
	cases := map[string]string{
		":8080":            "127.0.0.1:8080",
		"0.0.0.0:8080":     "127.0.0.1:8080",
		"127.0.0.1:9090":   "127.0.0.1:9090",
		"example.test:443": "example.test:443",
	}
	for in, want := range cases {
		if got := normalizeHealthHost(in); got != want {
			t.Errorf("normalizeHealthHost(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// TestHealthcheckTarget_UnixHatVorrang prüft, dass healthcheckTarget()
// dieselbe Vorrang-Regel liest wie run() in main.go: ABOARD_UNIX_SOCKET
// überschreibt ABOARD_ADDR (Doku in printUsage()), es wird aber nichts
// entschieden — beide Werte kommen unverändert zurück, die Entscheidung
// trifft checkHealth/healthClient.
func TestHealthcheckTarget_UnixHatVorrang(t *testing.T) {
	t.Setenv("ABOARD_UNIX_SOCKET", "/run/aboard/aboard.sock")
	t.Setenv("ABOARD_ADDR", ":9999")

	unixSocket, tcpAddr := healthcheckTarget()
	if unixSocket != "/run/aboard/aboard.sock" {
		t.Errorf("unixSocket = %q, erwartet /run/aboard/aboard.sock", unixSocket)
	}
	if tcpAddr != ":9999" {
		t.Errorf("tcpAddr = %q, erwartet :9999 (ABOARD_ADDR bleibt trotzdem gelesen)", tcpAddr)
	}
}

// TestHealthcheckTarget_Default prüft den unkonfigurierten Fall: kein Socket,
// ABOARD_ADDR fällt auf defaultListenAddr zurück.
func TestHealthcheckTarget_Default(t *testing.T) {
	t.Setenv("ABOARD_UNIX_SOCKET", "")
	t.Setenv("ABOARD_ADDR", "")

	unixSocket, tcpAddr := healthcheckTarget()
	if unixSocket != "" {
		t.Errorf("unixSocket = %q, erwartet leer", unixSocket)
	}
	if tcpAddr != defaultListenAddr {
		t.Errorf("tcpAddr = %q, erwartet defaultListenAddr %q", tcpAddr, defaultListenAddr)
	}
}

// TestCheckHealth_OkTrueAberFalscherStatus — ok:true hinter einem Nicht-200
// ist kein gesunder Server (Orchestrator-Nacharbeit zu #542).
func TestCheckHealth_OkTrueAberFalscherStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	if err := checkHealth(context.Background(), "", srv.Listener.Addr().String()); err == nil {
		t.Fatal("Status 500 mit ok:true wurde als gesund gewertet")
	}
}

// kurzerSocketPfad liefert einen RELATIVEN Socket-Pfad in einem frischen
// Testverzeichnis (Kritiker Sprint 70, K-A16). Linux begrenzt Pfade von
// Unix-Sockets auf 108 Byte; liegt TMPDIR im Projekt-tmp/ (Projektregel:
// nichts nach /tmp), sprengt der absolute Pfad aus t.TempDir() diese Grenze,
// und bind scheitert mit „invalid argument“. Das Arbeitsverzeichnis wechselt
// nur für diesen Test (t.Chdir stellt es danach zurück).
func kurzerSocketPfad(t *testing.T, name string) string {
	t.Helper()
	t.Chdir(t.TempDir())
	return name
}
