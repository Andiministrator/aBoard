package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestFoldMotor_IstVerdrahtet nagelt Karte #552 fest: main.go erzeugt den
// Fold-Motor und hängt ihn an die BoardsAPI. Ohne WithFold antwortet
// POST /boards/{id}/ops mit 503 — und die Tests in api/boards_ops_test.go
// blieben grün, weil sie den Motor selbst anhängen. Das ist die Lücke aus
// F-1143 G9: Funktion geprüft, Aufrufer nicht.
//
// Quelltext statt Laufzeit, aus demselben Grund wie die Tests in
// main_test.go: run() startet den ganzen Server.
//
// GRENZE: Belegt nur, dass beide Aufrufe im Code stehen (ohne Kommentare) —
// nicht, dass WithFold an DIESER BoardsAPI hängt oder vor Register läuft.
func TestFoldMotor_IstVerdrahtet(t *testing.T) {
	quelle, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("main.go nicht lesbar: %v", err)
	}
	ohneKommentare := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(quelle), "")
	for _, muss := range []string{"fold.Neu(", ".WithFold("} {
		if !strings.Contains(ohneKommentare, muss) {
			t.Errorf("main.go enthält %q nicht mehr — POST /boards/{id}/ops wäre ohne Fold-Motor (503)", muss)
		}
	}
}
