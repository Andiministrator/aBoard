package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalServiceBoardState_FixedColumns(t *testing.T) {
	got := string(CanonicalServiceBoardState("service", "Service"))
	// Pflicht-Spalten aus ADR-0092 §3 — Reihenfolge und Titel.
	for _, want := range []string{
		`"id":"col-inbox","title":"Inbox"`,
		`"id":"col-in-bearbeitung","title":"In Bearbeitung"`,
		`"id":"col-wartet","title":"Wartet"`,
		`"id":"col-erledigt","title":"Erledigt"`,
		`"id":"col-spam","title":"Spam"`,
		`"kind":"service"`,
		`"id":"__service__"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Service-Board-Seed enthält %q nicht; got=%s", want, got)
		}
	}
}

func TestCanonicalServiceBoardState_InjectionSafe(t *testing.T) {
	// Bösartige Eingaben müssen als JSON-String escaped landen, nicht das
	// Schema brechen. Analog seed.go-Schutz.
	got := string(CanonicalServiceBoardState("id-x", `a"b\c`))
	if !strings.Contains(got, `"title":"a\"b\\c"`) {
		t.Errorf("Title-Escape fehlt; got=%s", got)
	}
}

func TestCanonicalServiceBoardState_IsValidJSON(t *testing.T) {
	got := CanonicalServiceBoardState("service", "Service")
	if !json.Valid(got) {
		t.Errorf("Seed ist kein valides JSON: %s", got)
	}
}
