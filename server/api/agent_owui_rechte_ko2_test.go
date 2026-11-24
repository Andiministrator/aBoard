package api_test

// Kritiker Sprint 82, Runde 2, KO-2: ABOARD_OWUI_JWT_SECRET_FILE wurde ohne
// Rechteprüfung gelesen, anders als der LLM-Serverschlüssel (K2-8). Wer das
// Geheimnis lesen kann, signiert gültige OWUI-JWTs für jede Person. Jetzt
// warnt der Start bei Rechten offener als 0640 (kein Abbruch).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestOWUIGeheimnisRechteWarnungKO2(t *testing.T) {
	datei := filepath.Join(t.TempDir(), "owui-jwt-secret")
	if err := os.WriteFile(datei, []byte(strings.Repeat("g", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(api.OWUIJWTSecretFileEnv, "")
	if w := api.OWUIGeheimnisRechteWarnung(); w != "" {
		t.Errorf("ohne Variable: Warnung %q, erwartet keine", w)
	}
	t.Setenv(api.OWUIJWTSecretFileEnv, datei)
	for _, fall := range []struct {
		rechte os.FileMode
		warnen bool
	}{{0o600, false}, {0o640, false}, {0o400, false}, {0o644, true}, {0o660, true}, {0o604, true}, {0o666, true}} {
		if err := os.Chmod(datei, fall.rechte); err != nil {
			t.Fatal(err)
		}
		w := api.OWUIGeheimnisRechteWarnung()
		if fall.warnen && (w == "" || !strings.Contains(w, api.OWUIJWTSecretFileEnv) || !strings.Contains(w, "0640")) {
			t.Errorf("Rechte %04o: Warnung %q, erwartet Warnsatz mit %s und 0640", fall.rechte, w, api.OWUIJWTSecretFileEnv)
		}
		if !fall.warnen && w != "" {
			t.Errorf("Rechte %04o: Warnung %q, erwartet keine", fall.rechte, w)
		}
		if strings.Contains(w, "gggg") {
			t.Errorf("Warnung nennt das Geheimnis: %q", w)
		}
	}
	// Die Datei liegt im eigenen Testverzeichnis (t.TempDir) und wird mit ihm entfernt.
	_ = os.Chmod(datei, 0o600)
}
