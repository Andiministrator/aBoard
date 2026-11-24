package api

// F-2944 (Kritik Abschluss Sprint 111): Das Serverlog nennt bei bestätigter
// frischer Anmeldung den Weg (auth_time oder iat-Ersatzweg) und den Anbieter.

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFrisch2944_LogNenntWegUndAnbieter(t *testing.T) {
	k, c := pmMitBindung(t)
	k.uhr = time.Now().UTC().Truncate(time.Second)
	k.ke.ReauthFallback = true

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	for _, f := range []struct {
		name   string
		claims map[string]any
		weg    string
	}{
		{"iat-Weg", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix()}, "Weg ok_ohne_auth_time"},
		{"auth_time-Weg", map[string]any{"sub": "sub-opfer", "iat": k.uhr.Unix(), "auth_time": k.uhr.Unix()}, "Weg ok)"},
	} {
		buf.Reset()
		ac, q := k.frischStart(t, "google", frischLoesen, "google", c)
		k.rueckruf(t, k.google, "google", q, "l-"+uuid.NewString(), f.claims, ac, c)
		got := buf.String()
		if !strings.Contains(got, "bestätigt") || !strings.Contains(got, `"google"`) || !strings.Contains(got, f.weg) {
			t.Errorf("%s: Log ohne Anbieter/Weg %q: %s", f.name, f.weg, got)
		}
	}
}
