package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Karte #569: Ein ungültiger ABOARD_AUSGAENGE-Wert hält den Start an, bevor
// der Server irgendetwas anlegt oder eine Verbindung aufbauen könnte — im
// Zweifel keine Ausgänge statt „wird schon an sein“. Pfade und Adresse zeigen
// ins Test-Verzeichnis; fehlt die Prüfung in run(), startet der Server, und
// die Zeitgrenze macht den Test rot, statt ihn hängen zu lassen.
func TestRun_UngueltigerAusgangsSchalterStopptStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ABOARD_DB", filepath.Join(dir, "aboard.db"))
	t.Setenv("ABOARD_ATTACHMENTS_DIR", filepath.Join(dir, "anhaenge"))
	t.Setenv("ABOARD_ADDR", "127.0.0.1:0")
	t.Setenv("ABOARD_UNIX_SOCKET", "")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_AGENT_LISTEN", "")
	t.Setenv("ABOARD_AGENT_UNIX_SOCKET", "")
	t.Setenv("ABOARD_AUSGAENGE", "vielleicht")

	fertig := make(chan error, 1)
	go func() { fertig <- run() }()
	select {
	case err := <-fertig:
		if err == nil || !strings.Contains(err.Error(), "ABOARD_AUSGAENGE") {
			t.Fatalf("run() = %v, erwartet Fehler zu ABOARD_AUSGAENGE", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() startet trotz ungültigem ABOARD_AUSGAENGE")
	}
}
