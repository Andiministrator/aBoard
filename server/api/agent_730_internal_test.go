package api

// Karte #730: Open WebUI stellt das Nutzer-JWT EINMAL pro Chat-Anfrage aus
// und nutzt es für alle Werkzeugaufrufe des Zugs. Lange Denk-Züge brauchen
// ein Höchstalter über 300 s — einstellbar über
// ABOARD_OWUI_JWT_MAX_AGE_SECONDS, exp gilt weiter.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestOWUIHoechstalter_Variable(t *testing.T) {
	t.Setenv(OWUIJWTMaxAgeEnv, "")
	if d, err := ladeOWUIHoechstalter(); err != nil || d != 300*time.Second {
		t.Fatalf("nicht gesetzt: %v %v", d, err)
	}
	if OWUIHoechstalterHinweis() != "" {
		t.Error("Standard braucht keinen Starthinweis")
	}
	for roh, soll := range map[string]time.Duration{"1800": 1800 * time.Second, " 60 ": 60 * time.Second, "3600": time.Hour} {
		t.Setenv(OWUIJWTMaxAgeEnv, roh)
		if d, err := ladeOWUIHoechstalter(); err != nil || d != soll {
			t.Errorf("%q: %v %v, erwartet %v", roh, d, err, soll)
		}
	}
	t.Setenv(OWUIJWTMaxAgeEnv, "1800")
	t.Setenv(OWUIJWTSecretFileEnv, "/irgendwo/geheim") // Delegation an: nur dann gibt es den Hinweis
	if h := OWUIHoechstalterHinweis(); !strings.Contains(h, "1800 s") || !strings.Contains(h, "FORWARD_USER_INFO_HEADER_JWT_EXPIRES_SECONDS") {
		t.Errorf("Starthinweis: %q", h)
	}
	for _, roh := range []string{"59", "3601", "0", "-300", "abc", "1.5", "300s", "9223372037", "18446744073709551616"} {
		t.Setenv(OWUIJWTMaxAgeEnv, roh)
		if _, err := ladeOWUIHoechstalter(); err == nil {
			t.Errorf("%q angenommen", roh)
		}
	}
}

// Ungültiger Wert bricht den Start ab (wie ein unbrauchbares Geheimnis) —
// aber nur, wenn die Delegation überhaupt eingeschaltet ist (K-2).
func TestOWUIHoechstalter_StartAbbruch(t *testing.T) {
	t.Setenv(OWUIJWTSecretFileEnv, "")
	t.Setenv(OWUIJWTMaxAgeEnv, "99999")
	if err := (&AgentZugang{}).MitOWUIDelegation(nil); err != nil {
		t.Fatalf("ohne Geheimnis darf das Höchstalter den Start nicht anhalten: %v", err)
	}
	t.Setenv(OWUIJWTMaxAgeEnv, "1800") // gültig — trotzdem kein Hinweis ohne Delegation
	if h := OWUIHoechstalterHinweis(); h != "" {
		t.Errorf("ohne Delegation kein Starthinweis: %q", h)
	}
	gut := filepath.Join(t.TempDir(), "geheim")
	_ = os.WriteFile(gut, owuiTestGeheimnis, 0o600)
	t.Setenv(OWUIJWTSecretFileEnv, gut)
	t.Setenv(OWUIJWTMaxAgeEnv, "99999")
	if err := (&AgentZugang{}).MitOWUIDelegation(nil); err == nil {
		t.Fatal("ungültiges Höchstalter angenommen")
	}
	t.Setenv(OWUIJWTMaxAgeEnv, "1800")
	z := &AgentZugang{}
	if err := z.MitOWUIDelegation(nil); err != nil || z.owuiHoechstalter != 1800*time.Second {
		t.Fatalf("1800: %v %v", z.owuiHoechstalter, err)
	}
}

func TestOWUIJWT_HoechstalterEinstellbar(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	kopf := map[string]any{"alg": "HS256", "typ": "JWT"}
	jwt := func(iat, exp int64) string {
		return owuiTestJWT(t, owuiTestGeheimnis, kopf, map[string]any{"iss": "open-webui", "email": "anna@firma.de", "iat": iat, "exp": exp})
	}
	// Ein Zug nach 1000 s Denkzeit, OWUI hat 1800 s Gültigkeit ausgestellt.
	lang := jwt(jetzt.Unix()-1000, jetzt.Unix()+800)
	_, _, err := pruefeOWUIJWTMitAlter(lang, owuiTestGeheimnis, jetzt, owuiHoechstalter)
	if err == nil || !strings.Contains(err.Error(), "zu alt") {
		t.Fatalf("Standard 300 s muss ablehnen: %v", err)
	}
	if !strings.Contains(err.Error(), "alter 1000 s, owui-gültigkeit 1800 s, höchstalter 300 s") {
		t.Errorf("Meldung ohne Zahlen: %v", err)
	}
	_, ablauf, err := pruefeOWUIJWTMitAlter(lang, owuiTestGeheimnis, jetzt, 1800*time.Second)
	if err != nil || !ablauf.Equal(jetzt.Add(800*time.Second)) {
		t.Fatalf("1800 s muss annehmen, Ablauf = exp: %v %v", ablauf, err)
	}
	// exp gilt weiter: OWUI auf 300 s, aBoard auf 1800 s → abgelaufen.
	kurz := jwt(jetzt.Unix()-1000, jetzt.Unix()-700)
	_, _, err = pruefeOWUIJWTMitAlter(kurz, owuiTestGeheimnis, jetzt, 1800*time.Second)
	if err == nil || !strings.Contains(err.Error(), "abgelaufen (alter 1000 s, owui-gültigkeit 300 s") {
		t.Fatalf("exp muss weiter gelten: %v", err)
	}
	// Höchstalter kappt ein weiteres exp weiterhin (K1-3).
	weit := jwt(jetzt.Unix()-10, jetzt.Unix()+7200)
	_, ablauf, err = pruefeOWUIJWTMitAlter(weit, owuiTestGeheimnis, jetzt, 1800*time.Second)
	if err != nil || !ablauf.Equal(jetzt.Add(1790*time.Second)) {
		t.Fatalf("Kappung auf iat+1800: %v %v", ablauf, err)
	}
}

// Grenzfenster: angenommen bis genau iat + Höchstalter + 60 s, eine Sekunde
// später nicht mehr (K-3). exp liegt weit dahinter, es greift das Höchstalter.
func TestOWUIJWT_GrenzfensterHoechstalter(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	kopf := map[string]any{"alg": "HS256", "typ": "JWT"}
	for alter, soll := range map[int64]bool{1860: true, 1861: false} {
		tok := owuiTestJWT(t, owuiTestGeheimnis, kopf, map[string]any{"iss": "open-webui", "email": "anna@firma.de",
			"iat": jetzt.Unix() - alter, "exp": jetzt.Unix() + 3600})
		_, _, err := pruefeOWUIJWTMitAlter(tok, owuiTestGeheimnis, jetzt, 1800*time.Second)
		if (err == nil) != soll {
			t.Errorf("Alter %d s: angenommen=%v, erwartet %v (%v)", alter, err == nil, soll, err)
		}
	}
}

// Die Ablehnungs-Logzeile nennt Zahlen, aber weder Adresse noch JWT (K-3).
func TestOWUIJWT_LogzeileOhneAdresse(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	tok := owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": "HS256", "typ": "JWT"},
		map[string]any{"iss": "open-webui", "email": "anna@firma.de", "iat": jetzt.Unix() - 1000, "exp": jetzt.Unix() - 700})
	var zeilen []string
	alt := owuiLog
	owuiLog = func(f string, a ...any) { zeilen = append(zeilen, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { owuiLog = alt })
	z := &AgentZugang{owuiGeheimnis: owuiTestGeheimnis, owuiACL: rollenStub{}, uhr: func() time.Time { return jetzt }}
	r := httptest.NewRequest(http.MethodGet, "/werkzeuge/boards", nil)
	if _, s, _ := z.delegierterPrincipal(r, &storage.AgentToken{ID: "t1", Name: "owui", Role: "editor"}, tok, 1); s != http.StatusUnauthorized {
		t.Fatalf("Status %d", s)
	}
	if len(zeilen) != 1 || !strings.Contains(zeilen[0], "alter 1000 s") {
		t.Fatalf("Logzeile: %q", zeilen)
	}
	if strings.Contains(zeilen[0], "anna") || strings.Contains(zeilen[0], tok) || strings.Contains(zeilen[0], strings.Split(tok, ".")[2]) {
		t.Errorf("Logzeile verrät Adresse oder JWT: %q", zeilen[0])
	}
}

// Der Zugang nutzt das eingestellte Höchstalter, nicht die Konstante.
func TestOWUIJWT_ZugangNutztHoechstalter(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	tok := owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": "HS256", "typ": "JWT"},
		map[string]any{"iss": "open-webui", "email": "anna@firma.de", "iat": jetzt.Unix() - 1000, "exp": jetzt.Unix() + 800})
	anfrage := func(alter time.Duration) int {
		z := &AgentZugang{owuiGeheimnis: owuiTestGeheimnis, owuiACL: rollenStub{}, uhr: func() time.Time { return jetzt }, owuiHoechstalter: alter}
		r := httptest.NewRequest(http.MethodGet, "/werkzeuge/boards", nil)
		_, status, _ := z.delegierterPrincipal(r, &storage.AgentToken{ID: "t1", Name: "owui", Role: "editor"}, tok, 1)
		return status
	}
	if s := anfrage(0); s != http.StatusUnauthorized {
		t.Errorf("Standard: Status %d, erwartet 401", s)
	}
	if s := anfrage(1800 * time.Second); s != 0 {
		t.Errorf("1800 s: Status %d, erwartet angenommen", s)
	}
}

type rollenStub struct{}

func (rollenStub) GetRole(context.Context, string, string) (string, bool, error) {
	return "", false, nil
}
