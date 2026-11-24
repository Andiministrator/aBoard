package api

// F-1632: Eine Panik in der Arbeit des Auszugs beendet nicht den Server —
// die Antwort ist 500 (vor dem ersten Lebenszeichen) bzw. trägt status 500
// im Objekt (danach), und das Log nennt die Panik ohne ihren Text.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAntwortMitLebenszeichen_PanikWirdFehler(t *testing.T) {
	for _, f := range []struct {
		name     string
		warten   time.Duration
		panikMit any
	}{
		{"vor dem Lebenszeichen, Laufzeitfehler", 0, nil},
		{"nach dem Lebenszeichen, Text mit Auszug", 60 * time.Millisecond, "geheimer Auszugstext"},
	} {
		var mu sync.Mutex
		var logs []string
		logf := func(format string, a ...any) {
			mu.Lock()
			logs = append(logs, fmt.Sprintf(format, a...))
			mu.Unlock()
		}
		w := httptest.NewRecorder()
		antwortMitLebenszeichen(w, 20*time.Millisecond, logf, func() (int, map[string]any) {
			time.Sleep(f.warten)
			if f.panikMit != nil {
				panic(f.panikMit)
			}
			var m map[string]int
			m["x"] = 1 // Laufzeitfehler: Zuweisung an nil-Map
			return http.StatusCreated, nil
		})
		var body map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(w.Body.String())), &body); err != nil {
			t.Fatalf("%s: Antwort kein JSON: %q", f.name, w.Body.String())
		}
		status := w.Code
		if s, ok := body["status"].(float64); ok {
			status = int(s)
		}
		if status != http.StatusInternalServerError || body["grund"] != "intern" {
			t.Errorf("%s: HTTP %d, Objekt %v — erwartet 500/intern", f.name, w.Code, body)
		}
		alle := strings.Join(logs, "\n")
		if !strings.Contains(alle, "Panik im Auszug") || strings.Contains(alle, "geheimer Auszugstext") {
			t.Errorf("%s: Log %q", f.name, alle)
		}
	}
}
