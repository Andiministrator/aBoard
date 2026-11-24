package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestForwardAuth_AbweisungWirdGeloggt — K-A5: Ohne VERBOSE schreibt eine
// Abweisung trotzdem eine Warnzeile, gedrosselt auf eine pro Minute, mit
// Grund, aber ohne Secret und ohne Identität.
func TestForwardAuth_AbweisungWirdGeloggt(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim123")
	t.Setenv("ABOARD_FORWARD_AUTH_VERBOSE", "")
	abweisungsLogZuletzt.Store(0)
	t.Cleanup(func() { abweisungsLogZuletzt.Store(0) })

	var buf bytes.Buffer
	altOut, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(altOut); log.SetFlags(altFlags) })

	h := withForwardAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/boards", nil)
		req.Header.Set("Remote-Email", "spion@firma.de")
		req.Header.Set("X-Aboard-Forward-Auth-Secret", "falsch-xyz")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	ausgabe := buf.String()
	if n := strings.Count(ausgabe, "401"); n != 1 {
		t.Fatalf("%d Warnzeilen für 5 Abweisungen, erwartet genau 1:\n%s", n, ausgabe)
	}
	if !strings.Contains(ausgabe, "Secret fehlt oder ist falsch") {
		t.Errorf("Grund fehlt in der Warnzeile: %s", ausgabe)
	}
	for _, geheim := range []string{"geheim123", "falsch-xyz", "spion@firma.de"} {
		if strings.Contains(ausgabe, geheim) {
			t.Errorf("Warnzeile enthält %q: %s", geheim, ausgabe)
		}
	}
}
