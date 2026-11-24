package api

// Karte #743 (ADR-0138 §4): Ein Vorschlags-Token legt keine Termine an und
// löscht keine. Über HTTP (agent_670_test.go) fällt eine termin.*-Op schon an
// zwei Stellen: am Op-Typ und an der Liste erlaubter Payload-Schlüssel. Die
// Gegenprobe zur Typ-Zeile blieb dort grün, weil die Schlüssel-Liste ebenfalls
// ablehnt. Dieser Test hält die Typ-Zeile selbst fest: Die Ablehnung nennt den
// Op-Typ, nicht erst einen Schlüssel.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestVorschlag743_TermineAmTypAbgelehnt(t *testing.T) {
	agent := &AgentPrincipal{Name: "recherche", BoardID: "b1", Role: storage.AgentRolleVorschlag}
	for typ, payload := range map[string]string{
		"termin.upsert": `{"termin":{"id":"t1","titel":"Fremd","datum":"2026-10-08"}}`,
		"termin.delete": `{"terminId":"t1"}`,
	} {
		neu, grund := vorschlagPayload(typ, json.RawMessage(payload), agent, "", "2026-10-07T10:00:00.000Z")
		if neu != nil || !strings.Contains(grund, "may only create new cards") || !strings.Contains(grund, typ) {
			t.Errorf("%s: grund %q, erwartet Ablehnung am Op-Typ", typ, grund)
		}
	}
}
