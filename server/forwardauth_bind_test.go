package main

import (
	"strings"
	"testing"
)

// TestIsLoopbackBind — Heuristik für sicheren vs. unsicheren TCP-Bind (F-262).
func TestIsLoopbackBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":   true,
		"127.0.0.1":        true,
		"127.5.5.5:8080":   true, // 127.0.0.0/8 komplett Loopback
		"localhost:8080":   true,
		"[::1]:8080":       true,
		"::1":              true,
		":8080":            false, // alle Interfaces
		"":                 false,
		"0.0.0.0:8080":     false,
		"[::]:8080":        false,
		"192.168.1.10:443": false, // konkrete LAN-IP → konservativ unsicher
	}
	for in, want := range cases {
		if got := isLoopbackBind(in); got != want {
			t.Errorf("isLoopbackBind(%q) = %v, erwartet %v", in, got, want)
		}
	}
}

// TestForwardAuthBindDecision — die reine Kern-Entscheidung (F-262 (c)).
func TestForwardAuthBindDecision(t *testing.T) {
	type tc struct {
		name   string
		trust  bool
		addr   string
		unix   string
		optOut bool
		want   forwardAuthBindOutcome
	}
	for _, c := range []tc{
		{"Trust AUS → OK egal welcher Bind", false, ":8080", "", false, bindOK},
		{"Trust AN + Loopback → OK", true, "127.0.0.1:8080", "", false, bindOK},
		{"Trust AN + Unix-Socket → OK", true, ":8080", "/run/aboard.sock", false, bindOK},
		{"Trust AN + :8080 ohne Opt-out → REFUSE", true, ":8080", "", false, bindRefuse},
		{"Trust AN + 0.0.0.0 ohne Opt-out → REFUSE", true, "0.0.0.0:8080", "", false, bindRefuse},
		{"Trust AN + :8080 + Opt-out → WARN", true, ":8080", "", true, bindWarn},
		{"Trust AN + LAN-IP ohne Opt-out → REFUSE", true, "192.168.1.5:8080", "", false, bindRefuse},
	} {
		if got := forwardAuthBindDecision(c.trust, c.addr, c.unix, c.optOut); got != c.want {
			t.Errorf("%s: forwardAuthBindDecision = %d, erwartet %d", c.name, got, c.want)
		}
	}
}

// TestCheckForwardAuthBind_Refuse — Trust=AN + :8080 ohne Opt-out: die
// Prozess-Glue liefert einen Fehler (run() bricht ab).
func TestCheckForwardAuthBind_Refuse(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_INSECURE_BIND", "")

	err := checkForwardAuthBind(":8080", "", func(string, ...any) {})
	if err == nil {
		t.Fatalf("checkForwardAuthBind(:8080) = nil, erwartet Fehler (F-262 Refuse)")
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("Fehlermeldung nennt keine Abhilfe (127.0.0.1): %q", err.Error())
	}
}

// TestCheckForwardAuthBind_OptOut_WarntStattAbbruch.
func TestCheckForwardAuthBind_OptOut_WarntStattAbbruch(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_INSECURE_BIND", "1")

	var warned string
	err := checkForwardAuthBind(":8080", "", func(f string, v ...any) { warned = f })
	if err != nil {
		t.Fatalf("checkForwardAuthBind mit Opt-out = %v, erwartet nil (nur Warnung)", err)
	}
	if warned == "" || !strings.Contains(warned, "WARNUNG") {
		t.Errorf("erwartete laute Warnung, bekam: %q", warned)
	}
}

// TestCheckForwardAuthBind_TrustAus_KeinFehler — Default-Verhalten unverändert.
func TestCheckForwardAuthBind_TrustAus_KeinFehler(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")

	if err := checkForwardAuthBind(":8080", "", func(string, ...any) {}); err != nil {
		t.Fatalf("Trust=AUS + :8080 = %v, erwartet nil (ADR-0057/0059 unverändert)", err)
	}
}

// TestCheckForwardAuthBind_LoopbackOK — Trust=AN + 127.0.0.1 → kein Fehler.
func TestCheckForwardAuthBind_LoopbackOK(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")

	if err := checkForwardAuthBind("127.0.0.1:8080", "", func(string, ...any) {}); err != nil {
		t.Fatalf("Trust=AN + 127.0.0.1 = %v, erwartet nil", err)
	}
}

// ── ADR-0129 §2 / F-1124: Secret-Pflicht bei aktivem Trust ────────────

func TestForwardAuthSecretFehlt(t *testing.T) {
	fälle := []struct {
		trust  bool
		secret string
		want   bool
	}{
		{false, "", false}, // Einzelbetrieb: kein Secret nötig
		{false, "geheim", false},
		{true, "", true},      // Trust ohne Secret → Abbruch
		{true, "   \t", true}, // nur Leerraum zählt als fehlend
		{true, "geheim", false},
	}
	for _, f := range fälle {
		if got := forwardAuthSecretFehlt(f.trust, f.secret); got != f.want {
			t.Errorf("forwardAuthSecretFehlt(%v, %q) = %v, erwartet %v", f.trust, f.secret, got, f.want)
		}
	}
}

func TestCheckForwardAuthSecret_TrustOhneSecret_Abbruch(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "")
	err := checkForwardAuthSecret()
	if err == nil {
		t.Fatal("Trust an ohne Secret: kein Fehler, erwartet Startabbruch")
	}
	if !strings.Contains(err.Error(), "ABOARD_FORWARD_AUTH_SECRET") {
		t.Errorf("Fehlertext nennt die Variable nicht: %v", err)
	}
}

func TestCheckForwardAuthSecret_TrustMitSecret_OK(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")
	if err := checkForwardAuthSecret(); err != nil {
		t.Fatalf("Trust an mit Secret: %v", err)
	}
}

func TestCheckForwardAuthSecret_TrustAus_OK(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "")
	if err := checkForwardAuthSecret(); err != nil {
		t.Fatalf("Trust aus ohne Secret: %v", err)
	}
}
