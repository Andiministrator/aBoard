package api

// Karte #1071 (ADR-0149 §1, §4): Anbieter-Tabelle aus der Umgebung, Rückruf
// je Kürzel, Kürzel-Vertauschung, Bindung vor Adresse, keine Verknüpfung per
// Adressgleichheit. Testdaten nur mit example-Adressen.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func umgebung(m map[string]string) (func(string) string, []string) {
	var env []string
	for k, v := range m {
		env = append(env, k+"="+v)
	}
	return func(n string) string { return m[n] }, env
}

func TestAnbieterAusUmgebung1071(t *testing.T) {
	type fall struct {
		env      map[string]string
		kuerzel  string // erwartete Kürzel, kommagetrennt
		fehler   string // Teilstring eines Fehlers, "" = keiner
		warnung  string
		pruefung func(t *testing.T, a []AnbieterKonfig)
	}
	for name, f := range map[string]fall{
		"pm unverändert": {env: map[string]string{"ABOARD_OIDC_ISSUER": GoogleIssuer, "ABOARD_OIDC_CLIENT_ID": "c", "ABOARD_OIDC_CLIENT_SECRET": "s", "ABOARD_OIDC_ZUGELASSEN": "anna@example.com"},
			kuerzel: "standard", pruefung: func(t *testing.T, a []AnbieterKonfig) {
				if a[0].Label != "Google" || a[0].Issuer != GoogleIssuer {
					t.Errorf("standard: %+v", a[0])
				}
			}},
		"google und authelia": {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "google, authelia",
			"ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs",
			"ABOARD_OIDC_AUTHELIA_ISSUER": "https://auth.example.org", "ABOARD_OIDC_AUTHELIA_CLIENT_ID": "a",
			"ABOARD_OIDC_AUTHELIA_CLIENT_SECRET": "as", "ABOARD_OIDC_AUTHELIA_LABEL": "Team-Konto"},
			kuerzel: "google,authelia", pruefung: func(t *testing.T, a []AnbieterKonfig) {
				if a[0].Issuer != GoogleIssuer || a[0].Label != "Google" || a[1].Label != "Team-Konto" || a[1].ClientSecret != "as" {
					t.Errorf("Tabelle: %+v", a)
				}
			}},
		"alt und neu nebeneinander": {env: map[string]string{"ABOARD_OIDC_ISSUER": GoogleIssuer, "ABOARD_OIDC_CLIENT_ID": "c", "ABOARD_OIDC_CLIENT_SECRET": "s",
			"ABOARD_OIDC_PROVIDERS": "heim-2", "ABOARD_OIDC_HEIM_2_ISSUER": "https://heim.example.org", "ABOARD_OIDC_HEIM_2_CLIENT_ID": "h", "ABOARD_OIDC_HEIM_2_CLIENT_SECRET": "hs"},
			kuerzel: "standard,heim-2", pruefung: func(t *testing.T, a []AnbieterKonfig) {
				if a[1].Label != "heim.example.org" {
					t.Errorf("Label aus dem Host erwartet: %q", a[1].Label)
				}
			}},
		"gelistet ohne Daten": {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "google,authelia", "ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs"},
			kuerzel: "google", warnung: "authelia"},
		"halb eingerichtet":     {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "google", "ABOARD_OIDC_GOOGLE_CLIENT_ID": "g"}, fehler: "ABOARD_OIDC_GOOGLE_CLIENT_SECRET"},
		"alter Name halb":       {env: map[string]string{"ABOARD_OIDC_ISSUER": GoogleIssuer, "ABOARD_OIDC_CLIENT_ID": "c"}, fehler: "ABOARD_OIDC_CLIENT_SECRET"},
		"Issuer ohne https":     {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "heim", "ABOARD_OIDC_HEIM_ISSUER": "http://heim.example.org", "ABOARD_OIDC_HEIM_CLIENT_ID": "h", "ABOARD_OIDC_HEIM_CLIENT_SECRET": "s"}, fehler: "https"},
		"Kürzel nicht gelistet": {env: map[string]string{"ABOARD_OIDC_ISSUER": GoogleIssuer, "ABOARD_OIDC_CLIENT_ID": "c", "ABOARD_OIDC_CLIENT_SECRET": "s", "ABOARD_OIDC_GOOGEL_CLIENT_ID": "x"}, fehler: "ABOARD_OIDC_GOOGEL_CLIENT_ID"},
		"Kürzel reserviert":     {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "standard"}, fehler: "reserviert"},
		"Kürzel password":       {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "password"}, fehler: "reserviert"},
		"Kürzel ungültig":       {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "Google"}, fehler: "kein gültiges Kürzel"},
		"Kürzel doppelt": {env: map[string]string{"ABOARD_OIDC_PROVIDERS": "google,google", "ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs"},
			fehler: "doppelt"},
		// Seit #1074 kein Fehler mehr: Der Passwort-Weg reicht (ADR-0149 §1).
		"kein Anbieter": {env: map[string]string{"ABOARD_OIDC_ZUGELASSEN": "anna@example.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			getenv, environ := umgebung(f.env)
			a, warn, fehler := AnbieterAusUmgebung(getenv, environ)
			alle := strings.Join(fehler, "\n")
			if f.fehler == "" && len(fehler) > 0 || f.fehler != "" && !strings.Contains(alle, f.fehler) {
				t.Fatalf("Fehler %q, erwartet %q", alle, f.fehler)
			}
			if f.warnung != "" && !strings.Contains(strings.Join(warn, "\n"), f.warnung) {
				t.Errorf("Warnung %v, erwartet %q", warn, f.warnung)
			}
			if f.fehler != "" {
				return
			}
			var ks []string
			for _, x := range a {
				ks = append(ks, x.Kuerzel)
			}
			if strings.Join(ks, ",") != f.kuerzel {
				t.Fatalf("Kürzel %v, erwartet %s", ks, f.kuerzel)
			}
			if f.pruefung != nil {
				f.pruefung(t, a)
			}
		})
	}
}

func TestVorabListe1071(t *testing.T) {
	for _, f := range []struct {
		neu, alt, want string
		fehler         bool
	}{
		{"", "anna@example.com", "anna@example.com", false},
		{"anna@example.com", "", "anna@example.com", false},
		{" anna@example.com ", "anna@example.com", "anna@example.com", false},
		{"anna@example.com", "bert@example.com", "", true},
	} {
		got, err := VorabListe(func(n string) string {
			return map[string]string{VorabListeEnv: f.neu, VorabListeEnvAlt: f.alt}[n]
		})
		if got != f.want || (err != nil) != f.fehler {
			t.Errorf("neu=%q alt=%q: %q, %v", f.neu, f.alt, got, err)
		}
	}
}

// mehrAnbieter: zwei Test-Anbieter (google, authelia) an einer Tabelle mit
// echtem Speicher, verdrahtet wie in main (anmeldungEinrichten).
type mehrAnbieter struct {
	google, authelia *testAnbieter
	tabelle          *Anmeldungen
	mux              *http.ServeMux
	sitzungen        *storage.SitzungRepo
	exec             func(q string, args ...any)
}

func neuerMehrAnbieterTest(t *testing.T, liste string) *mehrAnbieter {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	z, err := NeueZulassung(liste, "")
	if err != nil {
		t.Fatal(err)
	}
	tor := NeuesTor(z, storage.NewKontoRepo(db))
	tor.TTL = time.Nanosecond
	s := storage.NewSitzungRepo(db)
	b := storage.NewAnbieterBindungRepo(db, NutzerAnlegen(storage.NewBootstrapRepo(db)))
	x := &mehrAnbieter{google: neuerTestAnbieter(t), authelia: neuerTestAnbieter(t), sitzungen: s}
	x.google.rueckruf = "https://board.example/anmeldung/rueckruf/google"
	x.authelia.rueckruf = "https://board.example/anmeldung/rueckruf/authelia"
	neu := func(k, label string, anb *testAnbieter) *OIDCAnmeldung {
		return &OIDCAnmeldung{Kuerzel: k, Label: label, Issuer: anb.srv.URL, ClientID: "client-1", ClientSecret: "geheim",
			Basis: "https://board.example", Schluessel: AnmeldeSchluessel("k"), Client: anb.srv.Client(),
			Sitzungen: s, Bindungen: b, Zulassung: z, Tor: tor.Erlaubt, Wartezeit: time.Nanosecond}
	}
	x.tabelle = &Anmeldungen{Anbieter: []*OIDCAnmeldung{neu("google", "Google", x.google), neu("authelia", "Team-Konto", x.authelia)}}
	x.mux = http.NewServeMux()
	x.mux.HandleFunc("GET "+AnmeldungStartPfad, x.tabelle.Start)
	x.mux.HandleFunc("GET "+AnmeldungRueckrufPfad, x.tabelle.RueckrufStandard)
	x.mux.HandleFunc("GET "+AnmeldungRueckrufPfad+"/{kuerzel}", x.tabelle.RueckrufKuerzel)
	x.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return x
}

func (x *mehrAnbieter) hole(t *testing.T, pfad string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil)
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, r)
	return rec
}

// starte beginnt bei Anbieter k und liefert Cookie und Parameter.
func (x *mehrAnbieter) starte(t *testing.T, k string) (*http.Cookie, url.Values) {
	t.Helper()
	rec := x.hole(t, AnmeldungStartPfad+"?"+AnbieterParam+"="+k, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("Start %s: %d %s", k, rec.Code, rec.Body.String())
	}
	ziel, _ := url.Parse(rec.Header().Get("Location"))
	for _, c := range rec.Result().Cookies() {
		if c.Name == AnmeldungCookie {
			return c, ziel.Query()
		}
	}
	t.Fatal("kein Anmelde-Cookie")
	return nil, nil
}

// melde: anb gibt den Code aus, der Rückruf geht an pfadKuerzel.
func (x *mehrAnbieter) melde(t *testing.T, anb *testAnbieter, pfadKuerzel string, c *http.Cookie, q url.Values, code string, claims map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	anb.mu.Lock()
	anb.challenge[code] = q.Get("code_challenge")
	anb.claims = map[string]any{"nonce:" + code: q.Get("nonce")}
	for k, v := range claims {
		anb.claims[k] = v
	}
	anb.mu.Unlock()
	return x.hole(t, AnmeldungRueckrufPfad+"/"+pfadKuerzel+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), c)
}

func (x *mehrAnbieter) sitzungsAdresse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	sc := sitzungsCookie(rec)
	if sc == nil {
		return ""
	}
	s, err := x.sitzungen.Pruefen(context.Background(), sc.Value)
	if err != nil {
		t.Fatal(err)
	}
	return s.Email
}

func TestMehrAnbieter1071_StartUndAuswahl(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	_, q := x.starte(t, "authelia")
	if q.Get("redirect_uri") != "https://board.example/anmeldung/rueckruf/authelia" {
		t.Fatalf("redirect_uri %q", q.Get("redirect_uri"))
	}
	// Ohne Kürzel führt der Start seit #1072 zur Anmeldeseite (mit konto=1).
	rec := x.hole(t, AnmeldungStartPfad+"?konto=1", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != AnmeldungSeitePfad+"?konto=1" {
		t.Fatalf("Start ohne Kürzel: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := x.hole(t, AnmeldungStartPfad+"?anbieter=github", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unbekanntes Kürzel am Start: %d", rec.Code)
	}
	// Mit genau einem Anbieter geht der Start direkt dorthin (pm).
	x.tabelle.Anbieter = x.tabelle.Anbieter[:1]
	if rec := x.hole(t, AnmeldungStartPfad, nil); rec.Code != http.StatusFound {
		t.Errorf("ein Anbieter: %d, erwartet direkte Weiterleitung", rec.Code)
	}
}

// Kritik W5: Ein Vorgang, der bei authelia begann, lässt sich nicht am
// Rückruf von google einlösen — der Code wird gar nicht erst getauscht.
func TestMehrAnbieter1071_KuerzelVertauschung(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	c, q := x.starte(t, "authelia")
	rec := x.melde(t, x.google, "google", c, q, "v1", nil)
	if rec.Code != http.StatusBadRequest || sitzungsCookie(rec) != nil {
		t.Fatalf("vertauschter Rückruf: %d", rec.Code)
	}
	if x.google.tokenAufrufe != 0 {
		t.Fatalf("Code beim falschen Anbieter getauscht")
	}
	// Unbekannte Kürzel und standard unter dem Kürzel-Pfad: 404.
	for _, p := range []string{"/github", "/standard"} {
		if rec := x.hole(t, AnmeldungRueckrufPfad+p, c); rec.Code != http.StatusNotFound {
			t.Errorf("Rückruf %s: %d, erwartet 404", p, rec.Code)
		}
	}
	// Ohne standard-Anbieter ist auch /anmeldung/rueckruf unbekannt.
	if rec := x.hole(t, AnmeldungRueckrufPfad, c); rec.Code != http.StatusNotFound {
		t.Errorf("Rückruf ohne Kürzel ohne standard: %d", rec.Code)
	}
	// Richtig zugeordnet geht es.
	c, q = x.starte(t, "authelia")
	if rec := x.melde(t, x.authelia, "authelia", c, q, "v2", nil); rec.Code != http.StatusFound || x.sitzungsAdresse(t, rec) != "anna@example.com" {
		t.Fatalf("authelia: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMehrAnbieter1071_ZweckNurLogin(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	a := x.tabelle.Finde("google")
	for _, z := range []anmeldeZustand{
		{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix(), Kuerzel: "google", Zweck: ZweckInvite},
		{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix(), Kuerzel: "google", Zweck: ZweckLink},
		{State: "s", Nonce: "n", Verifier: "v", Bis: time.Now().Add(time.Minute).Unix()}, // Cookie von vor #1071
	} {
		wert, err := a.zustandKodieren(z)
		if err != nil {
			t.Fatal(err)
		}
		rec := x.hole(t, AnmeldungRueckrufPfad+"/google?code=c&state=s", &http.Cookie{Name: AnmeldungCookie, Value: wert})
		if rec.Code == http.StatusFound || sitzungsCookie(rec) != nil || x.google.tokenAufrufe != 0 {
			t.Errorf("Zweck %q Kürzel %q: %d, erwartet Abweisung ohne Code-Tausch", z.Zweck, z.Kuerzel, rec.Code)
		}
	}
}

// ADR-0149 §4: Die Bindung bestimmt die Person. Meldet der Anbieter später
// eine andere Adresse, bleibt es dieselbe Person (#1055 wäre damit ohne Umzug
// gegangen). Ein zweiter Anbieter mit derselben Adresse wird abgewiesen.
func TestMehrAnbieter1071_BindungVorAdresse(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	c, q := x.starte(t, "google")
	if rec := x.melde(t, x.google, "google", c, q, "b1", nil); x.sitzungsAdresse(t, rec) != "anna@example.com" {
		t.Fatalf("erste Anmeldung: %d", rec.Code)
	}
	// Dasselbe Google-Konto meldet jetzt eine fremde, nicht zugelassene Adresse.
	c, q = x.starte(t, "google")
	rec := x.melde(t, x.google, "google", c, q, "b2", map[string]any{"email": "anna.privat@example.org"})
	if rec.Code != http.StatusFound || x.sitzungsAdresse(t, rec) != "anna@example.com" {
		t.Fatalf("gebundenes Konto mit anderer Adresse: %d, Sitzung %q — erwartet Anna", rec.Code, x.sitzungsAdresse(t, rec))
	}
	// … auch ohne bestätigte Adresse: Die Bindung trägt.
	c, q = x.starte(t, "google")
	rec = x.melde(t, x.google, "google", c, q, "b3", map[string]any{"email": nil, "email_verified": false})
	if x.sitzungsAdresse(t, rec) != "anna@example.com" {
		t.Fatalf("gebundenes Konto ohne Adresse: %d", rec.Code)
	}
	// Kritik W4: authelia meldet Annas Adresse — keine Verknüpfung per Adresse.
	c, q = x.starte(t, "authelia")
	rec = x.melde(t, x.authelia, "authelia", c, q, "b4", map[string]any{"sub": "anna-bei-authelia"})
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("zweiter Anbieter per Adresse: %d, erwartet 403", rec.Code)
	}
	// Gesperrt: Die Bindung hilft nicht am Tor vorbei.
	x.exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-anna', 'anna@example.com', 'blocked', 1, 1)`)
	c, q = x.starte(t, "google")
	rec = x.melde(t, x.google, "google", c, q, "b5", nil)
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("gesperrt mit Bindung: %d, erwartet 403", rec.Code)
	}
}

// Eine Bindung nur an ein unbestätigtes Konto (Einladung eingelöst, Admin hat
// noch nicht bestätigt) zeigt „wartet auf Bestätigung“ und legt keine
// Sitzung an.
func TestMehrAnbieter1071_WartetAufBestaetigung(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "")
	x.exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-carla', 'carla@example.com', 'pending', 1, 1)`)
	x.exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES (?, 'sub-carla', 'google', 'k-carla', 1)`, x.google.srv.URL)
	c, q := x.starte(t, "google")
	rec := x.melde(t, x.google, "google", c, q, "w1", map[string]any{"sub": "sub-carla", "email": "carla@example.com"})
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil || !strings.Contains(rec.Body.String(), "Bestätigung") {
		t.Fatalf("unbestätigtes Konto: %d %s", rec.Code, rec.Body.String())
	}
}
