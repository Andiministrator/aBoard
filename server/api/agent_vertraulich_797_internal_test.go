package api

// Karte #797, Verify V-1: Die Zusage „der Wiederholungsschlüssel eines
// Aufrufs ohne ausdrückliches vertraulich:false bleibt bitgleich zum Stand vor
// #797“ wird hier erzwungen — gegen die alte Formel, nicht gegen sich selbst.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnlageOffenSumme797_OhneKennzeichenKeinBeitrag(t *testing.T) {
	ctx := context.Background()
	if got := anlageOffenSumme(ctx); got != nil {
		t.Fatalf("ohne Kennzeichen: %v, erwartet kein Beitrag (nil)", got)
	}
	if got := anlageOffenSumme(mitAnlageOffen(ctx)); len(got) != 1 || got[0] == "" {
		t.Fatalf("mit Kennzeichen: %v, erwartet genau einen Beitrag", got)
	}
}

// schluessel797: der abgeleitete Wiederholungsschlüssel (Aufruf ohne Kopf) für
// diese Eingaben; ok false = Antwort geschrieben.
func schluessel797(t *testing.T, a *BoardsAPI, ctx context.Context, werkzeug string, inhalt []string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	p := &AgentPrincipal{TokenID: "t1", Name: "assistent", BoardID: "b1"}
	e, ok := a.werkzeugIdemBeginn(w, r, p, "b1", werkzeug, inhalt...)
	if !ok {
		t.Fatalf("%s: Antwort %d %s", werkzeug, w.Code, w.Body.String())
	}
	return e.schluessel
}

func TestWiederholungsschluessel797_OhneAngabeBitgleichZumAltenStand(t *testing.T) {
	a := &BoardsAPI{}
	ctx := context.Background()

	// Karte: Formeln aus karte_anlegen vor #797 (Basis; mit neuen Feldern).
	karteBasis := []string{"Titel", "Beschreibung", "lane"}
	karteExtra := []string{"Titel", "Beschreibung", "lane", "Ready", "owner=Anna"}
	termin := []string{"Besprechung", "2026-10-20", "", "10:00", "11:00", "Raum 1", "Text"}
	for name, f := range map[string]struct {
		werkzeug string
		alt      []string
	}{
		"karte":       {"karte_anlegen", karteBasis},
		"karte-extra": {"karte_anlegen", karteExtra},
		"termin":      {"termin_anlegen", termin},
	} {
		// Der Aufruf, wie die Handler ihn heute bilden: alt + Beitrag.
		neu := append(append([]string{}, f.alt...), anlageOffenSumme(ctx)...)
		soll := schluessel797(t, a, ctx, f.werkzeug+"-alt-"+name, f.alt)
		ist := schluessel797(t, a, ctx, f.werkzeug+"-alt-"+name, neu)
		if soll != ist {
			t.Errorf("%s: Schlüssel ohne Angabe weicht vom alten Stand ab", name)
		}
		// Gegenseite: ausdrückliches false ist eine andere Anfrage.
		offen := append(append([]string{}, f.alt...), anlageOffenSumme(mitAnlageOffen(ctx))...)
		if schluessel797(t, a, ctx, f.werkzeug+"-alt-"+name, offen) == soll {
			t.Errorf("%s: vertraulich:false ändert den Schlüssel nicht", name)
		}
	}
}
