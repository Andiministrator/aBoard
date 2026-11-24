package llm

// firmenmodus_rechte_test.go — Kritiker K2-8 (Sprint 82): Hinweis im
// Start-Log, wenn die Schlüsseldatei offener ist als 0640. Testdateien liegen
// in t.TempDir(); die Rechte werden dort gesetzt und mit dem Verzeichnis
// verworfen.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerschluesselRechteWarnung_K28(t *testing.T) {
	pfad := filepath.Join(t.TempDir(), "schluessel")
	if err := os.WriteFile(pfad, []byte("sk-test-k28"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		rechte os.FileMode
		warnt  bool
	}{
		{0o600, false}, {0o400, false}, {0o640, false}, {0o440, false},
		{0o644, true}, {0o604, true}, {0o660, true}, {0o666, true}, {0o650, true},
	} {
		if err := os.Chmod(pfad, f.rechte); err != nil {
			t.Fatal(err)
		}
		w := ServerschluesselRechteWarnung(pfad)
		if (w != "") != f.warnt {
			t.Errorf("Rechte %04o: Warnung %q, erwartet warnt=%v", f.rechte, w, f.warnt)
		}
		if w != "" && (strings.Contains(w, "sk-test-k28") || !strings.Contains(w, ServerschluesselEnvVar)) {
			t.Errorf("Warnung nennt den Inhalt oder nicht die Variable: %q", w)
		}
	}
	if w := ServerschluesselRechteWarnung(""); w != "" {
		t.Errorf("leerer Pfad: %q", w)
	}
	if w := ServerschluesselRechteWarnung(filepath.Join(t.TempDir(), "fehlt")); w != "" {
		t.Errorf("fehlende Datei: %q (meldet FirmenmodusAusDatei)", w)
	}
}
