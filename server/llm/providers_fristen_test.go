package llm

import "testing"

// Karte #665 (ADR-0133 §3): Ein Stream wartet auf die Header, nicht auf das
// erste Wort. Liegt der Header-Deckel unter der Erstbyte-Frist, bricht ein
// Modell mit langer Denkphase ab, bevor die 300 s je gelten (vorher 180 s).
func TestAntwortHeaderDeckel_NichtUnterErstbyteFrist(t *testing.T) {
	if AntwortHeaderDeckel < streamErstbyteTimeout {
		t.Fatalf("AntwortHeaderDeckel %v < streamErstbyteTimeout %v", AntwortHeaderDeckel, streamErstbyteTimeout)
	}
	if AntwortHeaderDeckel < 300*1e9 {
		t.Fatalf("AntwortHeaderDeckel %v < 300 s (ADR-0133 §3)", AntwortHeaderDeckel)
	}
}
