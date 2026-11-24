package api

import "testing"

// Kritiker Sprint 97 D-1: Die Präfixprüfung darf bei Board „abc“ keine Datei
// des Boards „abc-def“ annehmen (ADR-0142 Nachtrag Punkt 4).
func TestKritik97_DateinameBindestrichPraefix(t *testing.T) {
	fremd := "boardbackup-abc-def-20261019-010203-abcdef.tar.gz.enc"
	if dateinameGehoertZuBoard("abc", fremd) {
		t.Errorf("Datei von abc-def gilt als Datei von abc")
	}
	if !dateinameGehoertZuBoard("abc-def", fremd) {
		t.Errorf("eigene Datei von abc-def abgewiesen")
	}
	if !dateinameGehoertZuBoard("abc", "boardbackup-abc-20261019-010203-abcdef.tar.gz") {
		t.Errorf("unverschlüsselte eigene Datei abgewiesen (Menschenweg)")
	}
}
