package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// Verify Session-QA (F-1306): Das Log zum Uhr-Gate von card.claim spricht nur
// beim Wechsel, nennt einen lesbaren Grund (nie „<nil>“) und die Abhilfe.
func TestFaltOptionen_ClaimUhrLogBeimWechsel(t *testing.T) {
	var zeilen []string
	altLog, altGestellt := claimUhrLog, uhr.Gestellt
	claimUhrLog = func(f string, a ...any) { zeilen = append(zeilen, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { claimUhrLog, uhr.Gestellt = altLog, altGestellt; claimUhrGesperrt.Store(false) })
	claimUhrGesperrt.Store(false)
	claim := []serverOp{{Type: "card.claim"}}

	uhr.Gestellt = func() (bool, error) { return false, nil }
	for i := 0; i < 3; i++ {
		if opt := faltOptionen(context.Background(), claim); !strings.Contains(opt, "uhrUnsicher") {
			t.Fatalf("uhrUnsicher fehlt: %q", opt)
		}
	}
	if len(zeilen) != 1 || strings.Contains(zeilen[0], "<nil>") || !strings.Contains(zeilen[0], uhr.PruefungEnvVar) ||
		!strings.Contains(zeilen[0], "nicht synchronisiert") {
		t.Fatalf("Log beim Sperren: %q", zeilen)
	}
	uhr.Gestellt = func() (bool, error) { return false, errors.New("adjtimex kaputt") }
	faltOptionen(context.Background(), claim)
	if len(zeilen) != 1 {
		t.Fatalf("erneutes Log ohne Wechsel: %q", zeilen)
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	faltOptionen(context.Background(), claim)
	faltOptionen(context.Background(), claim)
	if len(zeilen) != 2 || !strings.Contains(zeilen[1], "jetzt gestellt") {
		t.Fatalf("Log beim Freigeben: %q", zeilen)
	}
	// Verify Session-QA: der Lesefehler-Zweig beim Wechsel — Grund mit dem
	// Fehlertext, nicht „nicht synchronisiert“.
	uhr.Gestellt = func() (bool, error) { return false, errors.New("adjtimex kaputt") }
	faltOptionen(context.Background(), claim)
	if len(zeilen) != 3 || !strings.Contains(zeilen[2], "nicht lesbar (adjtimex kaputt)") {
		t.Fatalf("Log beim Sperren mit Lesefehler: %q", zeilen)
	}
}
