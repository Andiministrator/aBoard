package ausgang

import "testing"

// ADR-0147: Umgebung ABOARD_GOTIFY_INTERN_ERLAUBT (host oder host:port).
func TestAusUmgebung_GotifyIntern(t *testing.T) {
	t.Setenv("ABOARD_AUSGAENGE", "")
	t.Setenv("ABOARD_AUSGAENGE_ERLAUBT", "")
	t.Setenv(GotifyInternEnvVar, " Gotify.Internal , 127.0.0.1:8443,[::1]:8080,::1,[fd00::2]")
	p, err := AusUmgebung()
	if err != nil {
		t.Fatal(err)
	}
	ja := [][2]string{
		{"gotify.internal", ""}, {"GOTIFY.internal.", "443"}, {"127.0.0.1", "8443"},
		{"::1", "8080"}, {"[::1]", "8080"}, {"::1", "443"}, {"fd00::2", "443"},
	}
	for _, c := range ja {
		if !p.GotifyInternErlaubt(c[0], c[1]) {
			t.Errorf("%v sollte erlaubt sein", c)
		}
	}
	nein := [][2]string{
		{"gotify", "443"}, {"x.gotify.internal", "443"}, {"10.0.0.1", "443"}, {"", "443"},
		{"gotify.internal", "8443"}, {"127.0.0.1", "443"}, {"127.0.0.1", "9"}, {"::1", "9"},
	}
	for _, c := range nein {
		if p.GotifyInternErlaubt(c[0], c[1]) {
			t.Errorf("%v sollte nicht erlaubt sein", c)
		}
	}
	if !p.GotifyInternHost("127.0.0.1") || p.GotifyInternHost("10.0.0.1") {
		t.Error("GotifyInternHost")
	}
	if (*Politik)(nil).GotifyInternErlaubt("gotify.internal", "443") {
		t.Error("nil-Politik erlaubt etwas")
	}
}

func TestAusUmgebung_GotifyInternUngueltig(t *testing.T) {
	t.Setenv("ABOARD_AUSGAENGE", "")
	t.Setenv("ABOARD_AUSGAENGE_ERLAUBT", "")
	for _, wert := range []string{
		"https://gotify.internal", "*.internal", "a/b", "gotify.internal:", "gotify.internal:0",
		"gotify.internal:70000", "gotify.internal:abc", "[::1]:", "[::1", "::1:8080:x", "[::1]x:80",
		"１２７.0.0.1", "gotify.exämple", ":443",
	} {
		t.Setenv(GotifyInternEnvVar, wert)
		if _, err := AusUmgebung(); err == nil {
			t.Errorf("%q wurde angenommen", wert)
		}
	}
	t.Setenv(GotifyInternEnvVar, "")
	p, err := AusUmgebung()
	if err != nil || p.GotifyInternErlaubt("localhost", "443") {
		t.Errorf("leer: %v", err)
	}
}
