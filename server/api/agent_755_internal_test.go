package api

// Karte #755: Übersetzung der Fold-Ablehnungen der Schreib-Werkzeuge. Der Fall
// „ein Mensch ändert zwischen Lesen und Schreiben“ (card.fieldIf →
// feld-geaendert) ist über die echte Kette nicht herstellbar — der stand wird
// vorher geprüft, die Lücke dazwischen ist Mikrosekunden breit. Hier deshalb
// die Antwort, die postOps in diesem Fall liefert.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ablehnung755(protokoll string) *pufferAntwort {
	p := &pufferAntwort{kopf: http.Header{}, status: http.StatusUnprocessableEntity}
	p.body = *bytes.NewBufferString(`{"error":"ops would not apply as intended; nothing written","protokoll":` + protokoll + `}`)
	return p
}

func TestAgent755_FeldGeaendertUebersetzt(t *testing.T) {
	k := &wkKarte{DisplayID: 7}
	for _, fall := range []struct {
		protokoll string
		status    int
		satz      string
	}{
		{`[{"art":"feld-geaendert","feld":"title"}]`, http.StatusConflict, "seit dem Lesen geändert"},
		{`[{"art":"fremder-kommentar"}]`, http.StatusForbidden, "selbst geschrieben"},
		{`[{"art":"ziel-fehlt","cardId":"x"}]`, http.StatusNotFound, "inzwischen gelöscht"},
		{`[{"art":"meta-archivierung"}]`, http.StatusUnprocessableEntity, "abgelehnt"},
	} {
		w := httptest.NewRecorder()
		werkzeugSchreibFehler(w, ablehnung755(fall.protokoll), k)
		if w.Code != fall.status || !strings.Contains(w.Body.String(), fall.satz) {
			t.Errorf("%s: %d %s", fall.protokoll, w.Code, w.Body.String())
		}
	}
	// werkzeugOpsFehler (Werkzeuge ohne eigene Übersetzung) nennt den Grund ebenfalls.
	w := httptest.NewRecorder()
	werkzeugOpsFehler(w, ablehnung755(`[{"art":"feld-geaendert"}]`))
	if !strings.Contains(w.Body.String(), "seit dem Lesen geändert") {
		t.Errorf("werkzeugOpsFehler: %s", w.Body.String())
	}
}
