package main

// main_preflight_test.go — F-1143 (G9, Sprint 71): belegt in ZWEI Stufen,
// dass der Startpfad checkForwardAuthSecret() erreicht:
//   1. runPreflight ruft checkForwardAuthSecret auf (Laufzeit, Tests unten),
//   2. run() ruft runPreflight auf (Quelltext, TestRun_RuftRunPreflightAuf).
// Stufe 1 allein verschöbe die Lücke nur eine Ebene nach oben: Wer den
// runPreflight-Aufruf aus run() entfernt, liesse die Laufzeit-Tests grün
// (Orchestrator-Nachtrag bei der Integration, Karte #555).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestRun_RuftRunPreflightAuf prüft den Aufrufer — Quelltext statt Laufzeit,
// weil run() den ganzen Server startet (Muster: main_test.go).
//
// GRENZE: Belegt nur, dass innerhalb von run() ein Aufruf steht, dessen
// Fehler zurückgegeben wird — nicht, dass er VOR dem ersten Listener liegt.
func TestRun_RuftRunPreflightAuf(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	ohneKommentare := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(quelle), "")
	start := strings.Index(ohneKommentare, "\nfunc run() error {")
	if start < 0 {
		t.Fatal("func run() error nicht gefunden")
	}
	rumpf := ohneKommentare[start+1:]
	if ende := strings.Index(rumpf, "\n}\n"); ende >= 0 {
		rumpf = rumpf[:ende]
	}
	if !regexp.MustCompile(`if err := runPreflight\([^)]*\); err != nil \{\s*return err`).MatchString(rumpf) {
		t.Fatal("run() ruft runPreflight(...) nicht mehr auf oder ignoriert den Fehler — " +
			"die Secret-Pflicht bei Trust=AN (F-1124) wäre ausser Kraft")
	}
}

// TestRunPreflight_RuftCheckForwardAuthSecretAuf — Trust=AN ohne Secret muss
// über runPreflight() denselben Fehler liefern wie checkForwardAuthSecret()
// direkt. listenAddr ist Loopback, damit ausschliesslich die Secret-Pflicht
// geprüft wird, nicht der Bind-Check (der hat seine eigenen Tests in
// forwardauth_bind_test.go).
func TestRunPreflight_RuftCheckForwardAuthSecretAuf(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "")

	err := runPreflight("127.0.0.1:8080", "")
	if err == nil {
		t.Fatal("runPreflight() bei Trust=AN ohne Secret = nil — checkForwardAuthSecret() wird nicht (mehr) aus run() erreicht (G9, F-1143)")
	}
	if !strings.Contains(err.Error(), "ABOARD_FORWARD_AUTH_SECRET") {
		t.Errorf("Fehlertext nennt die Variable nicht: %v", err)
	}
}

// TestRunPreflight_TrustMitSecret_OK — Gegenprobe zur Positivseite: Trust=AN
// MIT Secret und sicherem (Loopback-)Bind darf den Start nicht blockieren.
func TestRunPreflight_TrustMitSecret_OK(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")

	if err := runPreflight("127.0.0.1:8080", ""); err != nil {
		t.Fatalf("runPreflight() bei Trust=AN mit Secret + Loopback-Bind: %v, erwartet nil", err)
	}
}

// TestRunPreflight_TrustAus_OK — Einzelbetrieb (Trust=AUS): weder Bind- noch
// Secret-Check greifen, runPreflight muss durchlaufen.
func TestRunPreflight_TrustAus_OK(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "")

	if err := runPreflight(":8080", ""); err != nil {
		t.Fatalf("runPreflight() bei Trust=AUS: %v, erwartet nil", err)
	}
}
