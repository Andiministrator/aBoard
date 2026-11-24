package api

import (
	"encoding/json"
	"testing"
)

// Sprint-77-Kritiker: Der Idempotenz-Hash deckt den Akteur ab. Ohne ihn bekam
// ein anderer Schreiber mit gleicher id und gleicher Nutzlast „wiederholt“
// quittiert, für eine Op, die nie seine war.
func TestOpHash_AkteurGehoertDazu(t *testing.T) {
	op := serverOp{ClientID: "x-1", Type: "card.claim", Actor: "agent:a", Payload: json.RawMessage(`{"cardId":"k1","session":"s"}`)}
	ander := op
	ander.Actor = "agent:b"
	if opHash(op) == opHash(ander) {
		t.Fatal("gleicher Hash für verschiedene Akteure")
	}
	gleich := op
	gleich.Payload = json.RawMessage(`{ "cardId": "k1", "session": "s" }`)
	if opHash(op) != opHash(gleich) {
		t.Fatal("Leerraum in der Nutzlast ändert den Hash")
	}
}
