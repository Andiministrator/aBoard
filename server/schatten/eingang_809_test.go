package schatten

// Karte #809, Kritiker T-4: Eine doppelte schattenId wird auch dann gemeldet,
// wenn das erste Vorkommen selbst fehlerhaft ist.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func eintrag809(id, titel string) string {
	return fmt.Sprintf(`{"schattenId":%q,"art":"karte","nr":1,"titel":%q,"vertraulich":false,"spalte":"","prioritaet":"",`+
		`"startDatum":null,"startZeit":null,"endeDatum":null,"endeZeit":null,"bearbeiter":[],"vonAgent":false}`, id, titel)
}

func TestEingang809_DoppelteKennungNachFehlerhaftemErsten(t *testing.T) {
	body := `{"version":"1.0.0","schatten":[` + eintrag809(sid(1), "Привет") + `,` + eintrag809(sid(1), "Gut") + `]}`
	_, err := LiesEingang([]byte(body), "1.0.0")
	var f *EingangFehler
	if !errors.As(err, &f) {
		t.Fatalf("kein EingangFehler: %v", err)
	}
	if len(f.Fehler) != 2 {
		t.Fatalf("erwartet zwei Einzelfehler (Index 0 fehlerhaft, Index 1 doppelt), ist %+v", f.Fehler)
	}
	if f.Fehler[1].Index != 1 || !strings.Contains(f.Fehler[1].Grund, "schattenId wie schatten[0]") {
		t.Errorf("das Doppel an Index 1 wurde nicht gemeldet: %+v", f.Fehler[1])
	}
}

// Der Rückbau ändert nichts am Normalfall: zwei gültige Einträge mit
// verschiedenen Kennungen gehen durch.
func TestEingang809_VerschiedeneKennungenBleibenGueltig(t *testing.T) {
	body := `{"version":"1.0.0","schatten":[` + eintrag809(sid(1), "Eins") + `,` + eintrag809(sid(2), "Zwei") + `]}`
	if m, err := LiesEingang([]byte(body), "1.0.0"); err != nil || len(m) != 2 {
		t.Fatalf("%v %d", err, len(m))
	}
}
