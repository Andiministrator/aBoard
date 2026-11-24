package api

import (
	"encoding/json"
	"testing"
)

// Verify Sprint 78, V-6 (F-1318): Die Sitzung im Protokoll liest den exakten
// Schlüssel `session` — wie der Fold, nicht per Struct-Unmarshal.
func TestSitzungDerOp_ExakterSchluessel(t *testing.T) {
	op := func(typ, payload string) serverOp { return serverOp{Type: typ, Payload: json.RawMessage(payload)} }
	faelle := []struct {
		header string
		op     serverOp
		soll   string
	}{
		{"", op("card.claim", `{"cardId":"k","session":"y"}`), "y"},
		{"", op("card.claim", `{"cardId":"k","SESSION":"x"}`), ""},
		{"", op("card.release", `{"cardId":"k","Session":"x"}`), ""},
		{"h", op("card.claim", `{"cardId":"k","session":"y"}`), "h"},
		{"", op("card.field", `{"cardId":"k","session":"y"}`), ""},
		{"", op("card.claim", `kaputt`), ""},
	}
	for i, f := range faelle {
		if ist := sitzungDerOp(f.header, f.op); ist != f.soll {
			t.Errorf("Fall %d: %q, erwartet %q", i, ist, f.soll)
		}
	}
}
