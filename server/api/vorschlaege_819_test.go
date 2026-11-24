package api_test

// Karte #819: P3-Findings aus Sprint 91 zu den Vorschlägen (ADR-0140 §8).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func (e *env783) vorschlag819(t *testing.T, body string) map[string]any {
	t.Helper()
	if s, b := e.alsAgent(t, e.assistent, "POST", "/intern/b1/vorschlag", body); s != http.StatusOK {
		t.Fatalf("%s: %d %s", body, s, b)
	}
	l := e.liste783(t)
	return l[len(l)-1]
}

// TestVorschlag819_ListeSchreibtNicht (F-2096): Die Liste blendet einen
// abgelaufenen Vorschlag aus, löscht ihn aber nicht — das tut der Takt.
func TestVorschlag819_ListeSchreibtNicht(t *testing.T) {
	e := newEnv783(t)
	v := e.vorschlag819(t, `{"art":"kommentar","karte":"1","text":"alt"}`)
	if _, err := e.db.Exec(`UPDATE vorschlaege SET laeuft_ab = '2000-01-01T00:00:00Z' WHERE id = ?`, v["id"]); err != nil {
		t.Fatal(err)
	}
	if l := e.liste783(t); len(l) != 0 {
		t.Errorf("abgelaufener Vorschlag in der Liste: %v", l)
	}
	if n := e.offene(t); n != 1 {
		t.Errorf("Liste hat aufgeräumt: %d Zeilen, erwartet 1", n)
	}
	e.bapi.VorschlaegeAufraeumen(context.Background())
	if n := e.offene(t); n != 0 {
		t.Errorf("Takt-Aufräumen hat nicht gelöscht: %d", n)
	}
}

// TestVorschlag819_BearbeiterVorherNachher (F-2093): Die Anzeige nennt den
// heutigen Wert neben dem neuen, ein geleerter Owner erscheint als „(leer)“.
func TestVorschlag819_BearbeiterVorherNachher(t *testing.T) {
	e := newEnv783(t)
	v := e.vorschlag819(t, `{"art":"karte.bearbeiter","karte":"1","owner":"","beteiligte":["Vera"]}`)
	werte := map[string]string{}
	for _, f := range v["felder"].([]any) {
		m := f.(map[string]any)
		werte[m["schluessel"].(string)] = m["wert"].(string)
	}
	if werte["owner"] != "Anna → (leer)" {
		t.Errorf("owner = %q, erwartet „Anna → (leer)“", werte["owner"])
	}
	if werte["beteiligte"] != "(niemand) → Vera" {
		t.Errorf("beteiligte = %q, erwartet „(niemand) → Vera“", werte["beteiligte"])
	}
	// Unverändert: nur der neue Wert, kein Pfeil. Eigene Umgebung — die Liste
	// ordnet bei gleicher Sekunde nach zufälliger id.
	e = newEnv783(t)
	v = e.vorschlag819(t, `{"art":"karte.bearbeiter","karte":"1","owner":"Anna"}`)
	felder := v["felder"].([]any)
	if len(felder) != 1 || felder[0].(map[string]any)["wert"] != "Anna" {
		t.Errorf("unveränderter owner: %v, erwartet nur „Anna“", felder)
	}
}

// TestVorschlag819_UnklarerAusgangImHandler (F-2097): Scheitert die
// Ausführung mit einem nicht freigebbaren Status, sagt die Antwort des
// Handlers „offen“, nicht „nicht ausgeführt“. Auslöser: ein unlesbarer
// Board-Zustand (500) nach dem Entfernen des Vorschlags.
func TestVorschlag819_UnklarerAusgangImHandler(t *testing.T) {
	e := newEnv783(t)
	v := e.vorschlag819(t, `{"art":"kommentar","karte":"1","text":"hallo"}`)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = '{' WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	s, b := e.bestaetige(t, "anna@firma.de", v)
	if s != http.StatusInternalServerError {
		t.Fatalf("Status %d, erwartet 500: %s", s, b)
	}
	if !strings.Contains(string(b), "ist offen") || strings.Contains(string(b), "wurde nicht ausgeführt") {
		t.Errorf("Antwort nennt den unklaren Ausgang nicht: %s", b)
	}
}

// TestVorschlag819_AbgebrochenerBrowser (F-2090): Ist die Anfrage des
// Menschen schon abgebrochen, wenn die Ausführung läuft, wird trotzdem
// ausgeführt — der Vorschlag ist zu diesem Zeitpunkt bereits entfernt.
func TestVorschlag819_AbgebrochenerBrowser(t *testing.T) {
	e := newEnv783(t)
	v := e.vorschlag819(t, `{"art":"kommentar","karte":"1","text":"trotz Abbruch"}`)
	gespeichert, err := storage.NewVorschlagRepo(e.db).Hole(context.Background(), "b1", v["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, b := e.bapi.VorschlagAusfuehrenFuerTest(ctx, "b1", "anna@firma.de", gespeichert); s != http.StatusOK {
		t.Fatalf("Ausführung mit abgebrochener Anfrage: %d %s", s, b)
	}
	if k := e.karte783(t, "b1", "k1"); !strings.Contains(fmt.Sprint(k["comments"]), "trotz Abbruch") {
		t.Errorf("Kommentar fehlt: %v", k["comments"])
	}
}
