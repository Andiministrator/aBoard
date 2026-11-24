package api

// Karte #669 (ADR-0133 §4): Prüfung des OWUI-Nutzer-JWT mit gestellter Uhr.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var owuiTestGeheimnis = []byte("ein-test-geheimnis-mit-mindestens-32-zeichen")

func owuiTestJWT(t *testing.T, geheimnis []byte, kopf, inhalt map[string]any) string {
	t.Helper()
	k, _ := json.Marshal(kopf)
	i, _ := json.Marshal(inhalt)
	basis := base64.RawURLEncoding.EncodeToString(k) + "." + base64.RawURLEncoding.EncodeToString(i)
	mac := hmac.New(sha256.New, geheimnis)
	mac.Write([]byte(basis))
	return basis + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestOWUIJWT_Pruefung(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	kopf := map[string]any{"alg": "HS256", "typ": "JWT"}
	inhalt := func(aenderung map[string]any) map[string]any {
		m := map[string]any{"sub": "id-1", "email": "Anna@Firma.de", "name": "Anna", "role": "user",
			"iss": "open-webui", "iat": jetzt.Unix() - 10, "exp": jetzt.Unix() + 290}
		for k, v := range aenderung {
			if v == nil {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
		return m
	}

	gut := owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(nil))
	email, ablauf, err := pruefeOWUIJWT(gut, owuiTestGeheimnis, jetzt)
	if err != nil || email != "anna@firma.de" || !ablauf.Equal(jetzt.Add(290*time.Second)) {
		t.Fatalf("gültiges JWT: %q %v %v", email, ablauf, err)
	}
	// Grenzen der Fristen: genau 360 s alt und exp 59 s vorbei gehen noch.
	grenze := owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iat": jetzt.Unix() - 360, "exp": jetzt.Unix() - 59}))
	if _, _, err := pruefeOWUIJWT(grenze, owuiTestGeheimnis, jetzt); err != nil {
		t.Fatalf("an der Grenze: %v", err)
	}

	// alg=none: unsigniert, leere Signatur — und auch mit „Signatur“.
	ohneAlg := func(alg string) string {
		k, _ := json.Marshal(map[string]any{"alg": alg, "typ": "JWT"})
		i, _ := json.Marshal(inhalt(nil))
		return base64.RawURLEncoding.EncodeToString(k) + "." + base64.RawURLEncoding.EncodeToString(i) + "."
	}
	// Signatur über den echten Inhalt, aber alg im Kopf vertauscht.
	mitAlg := func(alg string) string {
		return owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": alg, "typ": "JWT"}, inhalt(nil))
	}
	teile := strings.Split(gut, ".")
	faelle := map[string]string{
		"alg-none-leer":      ohneAlg("none"),
		"alg-none-mit-sig":   ohneAlg("none") + teile[2],
		"alg-klein":          mitAlg("hs256"),
		"alg-HS384":          mitAlg("HS384"),
		"alg-RS256":          mitAlg("RS256"),
		"alg-fehlt":          owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"typ": "JWT"}, inhalt(nil)),
		"typ-falsch":         owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": "HS256", "typ": "JWE"}, inhalt(nil)),
		"crit":               owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": "HS256", "crit": []string{"x"}}, inhalt(nil)),
		"falsches-geheimnis": owuiTestJWT(t, []byte("ein-anderes-geheimnis-mit-mindestens-32-z"), kopf, inhalt(nil)),
		"inhalt-getauscht":   teile[0] + "." + strings.Split(owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"email": "admin@firma.de"})), ".")[1] + "." + teile[2],
		"abgelaufen":         owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iat": jetzt.Unix() - 100, "exp": jetzt.Unix() - 61})),
		"zu-alt":             owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iat": jetzt.Unix() - 361, "exp": jetzt.Unix() + 3600})),
		"iat-zukunft":        owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iat": jetzt.Unix() + 61, "exp": jetzt.Unix() + 361})),
		"exp-fehlt":          owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"exp": nil})),
		"iat-fehlt":          owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iat": nil})),
		"exp-text":           owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"exp": "morgen"})),
		"iss-falsch":         owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iss": "jemand"})),
		"iss-fehlt":          owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"iss": nil})),
		"email-fehlt":        owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"email": nil})),
		"email-domain":       owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"email": "@firma.de"})),
		"email-ohne-at":      owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"email": "anna"})),
		"email-leerzeichen":  owuiTestJWT(t, owuiTestGeheimnis, kopf, inhalt(map[string]any{"email": "anna @firma.de"})),
		"zwei-teile":         teile[0] + "." + teile[1],
		"vier-teile":         gut + ".x",
		"sig-kein-base64":    teile[0] + "." + teile[1] + ".!!!",
		"leer":               "",
		"zu-lang":            gut + strings.Repeat("A", owuiJWTMaxLaenge),
	}
	for name, tok := range faelle {
		if _, _, err := pruefeOWUIJWT(tok, owuiTestGeheimnis, jetzt); !errors.Is(err, errOWUIJWT) {
			t.Errorf("%s: angenommen (err = %v)", name, err)
		}
	}
	// Ohne Geheimnis nimmt die Prüfung nichts an — auch kein „leer signiertes“.
	leerSigniert := owuiTestJWT(t, nil, kopf, inhalt(nil))
	if _, _, err := pruefeOWUIJWT(leerSigniert, nil, jetzt); err == nil {
		t.Error("ohne Geheimnis angenommen")
	}
}

func TestOWUIGeheimnisDatei(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(OWUIJWTSecretFileEnv, "")
	if g, err := ladeOWUIGeheimnis(); g != nil || err != nil {
		t.Fatalf("nicht gesetzt: %q %v", g, err)
	}
	kurz := filepath.Join(dir, "kurz")
	_ = os.WriteFile(kurz, []byte("zu-kurz\n"), 0o600)
	t.Setenv(OWUIJWTSecretFileEnv, kurz)
	if _, err := ladeOWUIGeheimnis(); err == nil {
		t.Fatal("zu kurzes Geheimnis angenommen")
	}
	t.Setenv(OWUIJWTSecretFileEnv, filepath.Join(dir, "fehlt"))
	if _, err := ladeOWUIGeheimnis(); err == nil {
		t.Fatal("fehlende Datei angenommen")
	}
	gut := filepath.Join(dir, "gut")
	_ = os.WriteFile(gut, append(append([]byte{}, owuiTestGeheimnis...), '\n'), 0o600)
	t.Setenv(OWUIJWTSecretFileEnv, gut)
	if g, err := ladeOWUIGeheimnis(); err != nil || string(g) != string(owuiTestGeheimnis) {
		t.Fatalf("gut: %q %v", g, err)
	}
}

func TestOWUIHeaderEntfernen(t *testing.T) {
	r, _ := http.NewRequest("GET", "/boards/b1", nil)
	r.Header.Set("X-OpenWebUI-User-Jwt", "a.b.c")
	r.Header.Set("X-OpenWebUI-User-Email", "admin@firma.de")
	r.Header.Set("X-OpenWebUI-User-Name", "Admin")
	r.Header.Set("X-OpenWebUI-User-Id", "1")
	r.Header.Set("X-OpenWebUI-User-Role", "admin")
	r.Header["x-openwebui-user-email"] = []string{"klein@firma.de"} // nicht kanonisch eingeschleust
	jwt, n := owuiHeaderEntfernen(r)
	if jwt != "a.b.c" || n != 1 {
		t.Fatalf("jwt=%q n=%d", jwt, n)
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-openwebui-user-") {
			t.Errorf("Kopfzeile %q nicht entfernt", k)
		}
	}
}
