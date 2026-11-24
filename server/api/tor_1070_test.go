package api

// Karte #1070 (ADR-0149 §1, §5.1, §5.8): Tor, Ausnahme-Pfade, CSRF auf den
// POST-Wegen, /me im builtin-Betrieb. Testdaten nur mit example-Adressen.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

type fakeKonten struct {
	zustand map[string]string
	fehler  error
	n       atomic.Int32
}

func (f *fakeKonten) Zustand(_ context.Context, email string) (string, error) {
	f.n.Add(1)
	if f.fehler != nil {
		return "", f.fehler
	}
	return f.zustand[email], nil
}

func testTor(t *testing.T, liste string, k *fakeKonten) *Tor {
	t.Helper()
	z, err := NeueZulassung(liste, "")
	if err != nil {
		t.Fatal(err)
	}
	return NeuesTor(z, k)
}

func TestTor1070_Entscheidung(t *testing.T) {
	k := &fakeKonten{zustand: map[string]string{
		"aktiv@example.com":      storage.KontoAktiv,
		"gesperrt@example.com":   storage.KontoGesperrt,
		"eingeladen@example.com": storage.KontoEingeladen,
		"wartet@example.com":     storage.KontoWartet,
		"anna@example.com":       storage.KontoGesperrt, // auf der Liste UND gesperrt
	}}
	tor := testTor(t, "anna@example.com, liste@example.com, @firma.example", k)
	for adr, want := range map[string]bool{
		"liste@example.com":      true,
		" Liste@Example.com ":    true,
		"jemand@firma.example":   true,
		"aktiv@example.com":      true,
		"gesperrt@example.com":   false,
		"anna@example.com":       false, // Sperre schlägt Liste
		"eingeladen@example.com": false,
		"wartet@example.com":     false,
		"fremd@example.com":      false,
		"@firma.example":         false,
		"":                       false,
	} {
		if got, err := tor.Erlaubt(context.Background(), adr); err != nil || got != want {
			t.Errorf("Erlaubt(%q) = %v, %v; erwartet %v", adr, got, err, want)
		}
	}
	// Ohne Konten-Speicher gilt nur die Liste; ohne Tor niemand.
	ohne := testTor(t, "liste@example.com", nil)
	ohne.Konten = nil
	if ok, _ := ohne.Erlaubt(context.Background(), "liste@example.com"); !ok {
		t.Errorf("Liste ohne Konten-Speicher abgewiesen")
	}
	var nichts *Tor
	if ok, _ := nichts.Erlaubt(context.Background(), "liste@example.com"); ok {
		t.Errorf("nil-Tor lässt herein")
	}
}

func TestTor1070_FehlerIstFehler(t *testing.T) {
	tor := testTor(t, "liste@example.com", &fakeKonten{fehler: errors.New("platte voll")})
	// Auch eine gelistete Adresse: Ohne lesbaren Kontozustand wüsste das Tor
	// nicht, ob sie gesperrt ist.
	if ok, err := tor.Erlaubt(context.Background(), "liste@example.com"); ok || err == nil {
		t.Fatalf("Fehler im Speicher: ok=%v err=%v, erwartet Fehler", ok, err)
	}
}

func TestTor1070_CacheUndVergessen(t *testing.T) {
	k := &fakeKonten{zustand: map[string]string{"aktiv@example.com": storage.KontoAktiv}}
	tor := testTor(t, "", k)
	jetzt := time.Unix(1000, 0)
	tor.jetzt = func() time.Time { return jetzt }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, _ := tor.Erlaubt(ctx, "aktiv@example.com"); !ok {
			t.Fatal("aktiv abgewiesen")
		}
	}
	if k.n.Load() != 1 {
		t.Fatalf("Speicher %d-mal gefragt, erwartet 1 (Cache)", k.n.Load())
	}
	// Sperre ohne Vergessen: wirkt nach Ablauf der TTL.
	k.zustand["aktiv@example.com"] = storage.KontoGesperrt
	jetzt = jetzt.Add(TorCacheTTL - time.Millisecond)
	if ok, _ := tor.Erlaubt(ctx, "aktiv@example.com"); !ok {
		t.Fatal("innerhalb der TTL schon neu gelesen")
	}
	jetzt = jetzt.Add(2 * time.Millisecond)
	if ok, _ := tor.Erlaubt(ctx, "aktiv@example.com"); ok {
		t.Fatal("nach der TTL noch zugelassen")
	}
	// Sperre mit Vergessen: wirkt sofort.
	k.zustand["aktiv@example.com"] = storage.KontoAktiv
	if ok, _ := tor.Erlaubt(ctx, "aktiv@example.com"); ok {
		t.Fatal("Cache sollte noch „gesperrt“ halten")
	}
	tor.Vergessen(" AKTIV@example.com")
	if ok, _ := tor.Erlaubt(ctx, "aktiv@example.com"); !ok {
		t.Fatal("Vergessen hat den Cache nicht verworfen")
	}
}

// Gesperrt → sofort 401 über die Weiche (mit Vergessen), Speicherfehler → 500.
func TestTor1070_Weiche(t *testing.T) {
	k := &fakeKonten{zustand: map[string]string{"anna@example.com": storage.KontoAktiv}}
	tor := testTor(t, "", k)
	fs, sp, _ := weicheTest(t, nil)
	w := (&Sitzungsweiche{Sitzungen: fs, Zugelassen: tor.Erlaubt}).Middleware(sp)
	frage := func() int {
		sp.aufgerufen = false
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil)))
		return rec.Code
	}
	if c := frage(); c != http.StatusOK || !sp.aufgerufen {
		t.Fatalf("aktives Konto: %d", c)
	}
	k.zustand["anna@example.com"] = storage.KontoGesperrt
	tor.Vergessen("anna@example.com")
	if c := frage(); c != http.StatusUnauthorized || sp.aufgerufen {
		t.Fatalf("gesperrt: %d aufgerufen=%v, erwartet sofort 401", c, sp.aufgerufen)
	}
	k.fehler = errors.New("platte voll")
	tor.Vergessen("anna@example.com")
	if c := frage(); c != http.StatusInternalServerError || sp.aufgerufen {
		t.Fatalf("Speicherfehler: %d aufgerufen=%v, erwartet 500", c, sp.aufgerufen)
	}
}

func TestAnmeldungOhneSitzung1070_Pfade(t *testing.T) {
	tok := strings.Repeat("a", EinladungTokenLaenge-2) + "-_"
	offen := []struct{ m, p string }{
		{http.MethodGet, "/anmeldung"}, {http.MethodHead, "/anmeldung"},
		{http.MethodGet, "/anmeldung/start"}, {http.MethodGet, "/anmeldung/rueckruf"},
		{http.MethodGet, "/anmeldung/abgemeldet"},
		{http.MethodGet, "/anmeldung/rueckruf/google"}, {http.MethodGet, "/anmeldung/rueckruf/heim-2"},
		{http.MethodGet, "/anmeldung/rueckruf/" + strings.Repeat("k", AnbieterKuerzelMax)},
		{http.MethodPost, "/anmeldung/passwort"}, {http.MethodPost, "/anmeldung/code"},
		{http.MethodGet, "/einladung/" + tok}, {http.MethodPost, "/einladung/" + tok},
	}
	zu := []struct{ m, p string }{
		{http.MethodPost, "/anmeldung"}, {http.MethodGet, "/anmeldung/"},
		{http.MethodGet, "/anmeldung/passwort"}, {http.MethodPut, "/anmeldung/passwort"},
		{http.MethodGet, "/anmeldung/code"}, {http.MethodPost, "/anmeldung/start"},
		{http.MethodGet, "/anmeldung/rueckruf/"}, {http.MethodGet, "/anmeldung/rueckruf/Google"},
		{http.MethodGet, "/anmeldung/rueckruf/a/b"}, {http.MethodGet, "/anmeldung/rueckruf/a_b"},
		{http.MethodGet, "/anmeldung/rueckruf/" + strings.Repeat("k", AnbieterKuerzelMax+1)},
		{http.MethodPost, "/anmeldung/rueckruf/google"},
		{http.MethodGet, "/einladung/"}, {http.MethodGet, "/einladung/" + tok[1:]},
		{http.MethodGet, "/einladung/" + tok + "a"}, {http.MethodGet, "/einladung/" + tok[1:] + "."},
		{http.MethodGet, "/einladung/" + tok + "/x"}, {http.MethodDelete, "/einladung/" + tok},
		{http.MethodGet, "/boards"},
	}
	for _, f := range offen {
		if !anmeldungOhneSitzung(httptest.NewRequest(f.m, f.p, nil)) {
			t.Errorf("%s %s: sollte ohne Sitzung erreichbar sein", f.m, f.p)
		}
	}
	for _, f := range zu {
		if anmeldungOhneSitzung(httptest.NewRequest(f.m, f.p, nil)) {
			t.Errorf("%s %s: darf nicht ohne Sitzung erreichbar sein", f.m, f.p)
		}
	}
	// Kodierter Pfad (RawPath gesetzt) ist nie eine Ausnahme.
	r := httptest.NewRequest(http.MethodGet, "/anmeldung%2Frueckruf/google", nil)
	if anmeldungOhneSitzung(r) {
		t.Errorf("kodierter Pfad durchgelassen")
	}
}

// ADR-0149 §5.8: CSRF-Schutz bleibt für jeden POST-Weg ohne Sitzung.
func TestAnmeldungOhneSitzung1070_CSRF(t *testing.T) {
	t.Setenv(AuthModeEnvVar, "builtin")
	t.Setenv(AnmeldungEnvVar, "")
	tok := strings.Repeat("b", EinladungTokenLaenge)
	for _, p := range []string{AnmeldungPasswortPfad, AnmeldungCodePfad, EinladungPfadPraefix + tok} {
		for _, f := range []struct {
			site string
			ok   bool
		}{{"cross-site", false}, {"same-site", false}, {"same-origin", true}} {
			_, sp, _ := weicheTest(t, alleZugelassen)
			h := MitCSRFSchutz((&Sitzungsweiche{Zugelassen: nurListe(alleZugelassen)}).Middleware(sp))
			r := httptest.NewRequest(http.MethodPost, "https://board.example"+p, strings.NewReader("x=1"))
			r.Header.Set("Sec-Fetch-Site", f.site)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if sp.aufgerufen != f.ok || (!f.ok && rec.Code != http.StatusForbidden) {
				t.Errorf("POST %s, Sec-Fetch-Site %s: %d aufgerufen=%v, erwartet durchgelassen=%v", p, f.site, rec.Code, sp.aufgerufen, f.ok)
			}
		}
	}
}

func TestAnmeldungModus1070(t *testing.T) {
	for _, f := range []struct {
		neu, alt, want string
		fehler         bool
	}{
		{"", "", "", false},
		{"builtin", "", "builtin", false},
		{" Builtin ", "", "builtin", false},
		{"oidc", "", "builtin", false},
		{"", "oidc", "builtin", false},
		{"", "OIDC", "builtin", false},
		{"builtin", "oidc", "builtin", false},
		{"x", "", "", true},
		{"", "x", "", true},
		{"builtin", "x", "", true},
	} {
		t.Setenv(AuthModeEnvVar, f.neu)
		t.Setenv(AnmeldungEnvVar, f.alt)
		got, err := AnmeldungModus()
		if got != f.want || (err != nil) != f.fehler {
			t.Errorf("AUTH_MODE=%q ANMELDUNG=%q: %q, %v; erwartet %q, fehler=%v", f.neu, f.alt, got, err, f.want, f.fehler)
		}
		if AnmeldungOIDCAktiv() != (f.want == "builtin") {
			t.Errorf("AUTH_MODE=%q ANMELDUNG=%q: AnmeldungOIDCAktiv=%v", f.neu, f.alt, AnmeldungOIDCAktiv())
		}
	}
}

// ADR-0149 §1 Kritik W1: /me meldet "oidc" für beide Schalterwerte — das
// Frontend (src/sidebar.js, src/storage-plugin.js) hängt am Wert.
func TestMe1070_AnmeldungBleibtOIDC(t *testing.T) {
	for _, f := range [][2]string{{"builtin", ""}, {"", "oidc"}, {"builtin", "oidc"}} {
		t.Setenv(AuthModeEnvVar, f[0])
		t.Setenv(AnmeldungEnvVar, f[1])
		body := map[string]any{}
		mitAnmeldung(body)
		if body["anmeldung"] != "oidc" {
			t.Errorf("AUTH_MODE=%q ANMELDUNG=%q: anmeldung=%v, erwartet oidc", f[0], f[1], body["anmeldung"])
		}
	}
	t.Setenv(AuthModeEnvVar, "")
	t.Setenv(AnmeldungEnvVar, "")
	body := map[string]any{}
	mitAnmeldung(body)
	if _, da := body["anmeldung"]; da {
		t.Errorf("ohne eingebaute Anmeldung steht anmeldung in /me")
	}
}
