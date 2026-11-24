package api

import (
	"bytes"
	"strings"
	"testing"
)

// Karte #778 (Vorprüfung P-4): Kennungen im Header nur in sicherer Form,
// höchstens 20, die Anzahl zählt alle.
func TestVertraulichBewahrtHeader778(t *testing.T) {
	if h := vertraulichBewahrtHeader(nil); h != "" {
		t.Fatalf("leer: %q", h)
	}
	if h := vertraulichBewahrtHeader([]string{"k1", "a,b", "x\ny", "t-2_3"}); h != "4; ids=k1,t-2_3" {
		t.Fatalf("Filter: %q", h)
	}
	var viele []string
	for i := 0; i < 30; i++ {
		viele = append(viele, "k"+strings.Repeat("x", i%3+1))
	}
	h := vertraulichBewahrtHeader(viele)
	if !strings.HasPrefix(h, "30; ids=") || strings.Count(h, ",") != 19 {
		t.Fatalf("Kappung: %q", h)
	}
}

// Kritiker #778, K-3: Ein unlesbarer Stand gilt als Verstoß (Ausfall in
// Richtung Nichtstun); ohne vertraulichen Bestand wird nicht geprüft.
func TestVertraulichUnlesbar778(t *testing.T) {
	vorher := []byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k","confidential":true}]}]}}`)
	if _, unlesbar := vertraulichAbgeschaltet(vorher, []byte(`{"board":`)); !unlesbar {
		t.Fatal("unlesbarer Stand nicht gemeldet")
	}
	if v, unlesbar := vertraulichAbgeschaltet([]byte(`{"board":{"sprints":[]}}`), []byte(`kaputt`)); unlesbar || v != nil {
		t.Fatal("ohne Bestand geprüft")
	}
	// Escapter Schlüssel (K-8): der Vorfilter lässt ihn nicht durch.
	esc := []byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k","\u0063onfidential":true}]}]}}`)
	if bytes.Contains(esc, []byte("confidential")) {
		t.Fatal("Testfall trägt den Schlüssel im Klartext — kein Escape")
	}
	ohne := []byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k"}]}]}}`)
	if v, _ := vertraulichAbgeschaltet(esc, ohne); len(v) != 1 {
		t.Fatalf("escapter Schlüssel: %v", v)
	}
}

// Verify #778, V-1: Kein „confidential“ im ganzen Board, nur ein Termin an
// einer fehlenden Karte — der Termin ist wirksam vertraulich, ein Agent darf
// karteId nicht lösen.
func TestVertraulichOhneSchalterImBoard778(t *testing.T) {
	vorher := []byte(`{"board":{"sprints":[{"id":"s","cards":[]}],"termine":[{"id":"t","titel":"x","datum":"2026-10-01","karteId":"weg"}]}}`)
	nachher := []byte(`{"board":{"sprints":[{"id":"s","cards":[]}],"termine":[{"id":"t","titel":"x","datum":"2026-10-01"}]}}`)
	if v, _ := vertraulichAbgeschaltet(vorher, nachher); len(v) != 1 || v[0].id != "t" {
		t.Fatalf("Termin an fehlender Karte: %v", v)
	}
}
