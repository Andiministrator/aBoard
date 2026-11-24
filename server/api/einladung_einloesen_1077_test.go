package api

// Karte #1077: Einladung einlösen — Seite, Passwort-Weg in zwei Schritten,
// Anbieter-Weg über den Rückruf, erster Admin, Referrer-Policy (F-2895),
// Bindung nur über Einladung oder Vorab-Liste (F-2903). Testdaten nur
// example.org/example.com.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

const testSchluessel1077 = "0000000000000000000000000000000000000000000000000000000000000001"

// einloeseTest: Mehr-Anbieter-Aufbau plus Einlösen, verdrahtet wie in main.
type einloeseTest struct {
	*mehrAnbieter
	db        *sql.DB
	einladung *storage.EinladungRepo
	konten    *storage.KontoRepo
	tor       *Tor
	ein       *EinladungEinloesen
	jetzt     time.Time
	geaendert []string
}

func neuerEinloeseTest(t *testing.T, liste string) *einloeseTest {
	t.Helper()
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "")
	x := &einloeseTest{mehrAnbieter: neuerMehrAnbieterTest(t, liste), jetzt: time.Now()}
	// neuerMehrAnbieterTest hält seine DB für sich; eine eigene hier, mit
	// denselben Repos an allen Stellen.
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "b.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	x.db = db
	z, _ := NeueZulassung(liste, "")
	x.konten = storage.NewKontoRepo(db)
	x.tor = NeuesTor(z, x.konten)
	x.tor.TTL = time.Hour // Vergessen muss greifen, nicht der Ablauf
	x.sitzungen = storage.NewSitzungRepo(db)
	b := storage.NewAnbieterBindungRepo(db, NutzerAnlegen(storage.NewBootstrapRepo(db)))
	for _, a := range x.tabelle.Anbieter {
		a.Sitzungen, a.Bindungen, a.Zulassung, a.Tor = x.sitzungen, b, z, x.tor.Erlaubt
	}
	x.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	box, err := secretcrypto.New(testSchluessel1077)
	if err != nil {
		t.Fatal(err)
	}
	x.einladung = storage.NewEinladungRepo(db)
	x.ein = &EinladungEinloesen{Speicher: x.einladung, Konten: x.konten, NutzerAnlegen: NutzerAnlegen(storage.NewBootstrapRepo(db)),
		Box: box, Anmeldungen: x.tabelle, Wege: []string{PasswortWeg, "google", "authelia"}, Sitzungen: x.sitzungen,
		NachAenderung: func(email, _ string) { x.tor.Vergessen(email); x.geaendert = append(x.geaendert, email) },
		jetzt:         func() time.Time { return x.jetzt }}
	for _, a := range x.tabelle.Anbieter {
		a.Einladung = x.ein
	}
	x.mux.HandleFunc("GET "+EinladungPfadPraefix+"{token}", x.ein.Seite)
	x.mux.HandleFunc("POST "+EinladungPfadPraefix+"{token}", x.ein.Post)
	return x
}

// mitAdmin legt eine Admin-Person an (dann wartet ein eingelöstes Konto).
func (x *einloeseTest) mitAdmin() {
	x.exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-admin', 'admin@example.org', '', '2026-01-01T00:00:00Z', 1)`)
}

func (x *einloeseTest) einladen(t *testing.T, email string) string {
	t.Helper()
	e, err := x.einladung.Einladen(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return e.Token
}

func (x *einloeseTest) post(t *testing.T, token string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+EinladungPfadPraefix+token, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, r)
	return rec
}

func (x *einloeseTest) zustand(t *testing.T, email string) string {
	t.Helper()
	z, err := x.konten.Zustand(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return z
}

var (
	schluesselMuster   = regexp.MustCompile(`<code>([A-Z2-7 ]+)</code>`)
	einrichtungsMuster = regexp.MustCompile(`name="einrichtung" value="([^"]+)"`)
)

// schritt1 löst den Passwort-Schritt aus und liefert Geheimnis und Siegel.
func (x *einloeseTest) schritt1(t *testing.T, token string) ([]byte, string) {
	t.Helper()
	rec := x.post(t, token, url.Values{"weg": {"password"}, "passwort": {"lang-genug-1234"}, "passwort2": {"lang-genug-1234"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("Schritt 1: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	m, s := schluesselMuster.FindStringSubmatch(body), einrichtungsMuster.FindStringSubmatch(body)
	if m == nil || s == nil {
		t.Fatalf("Schritt 1 ohne Schlüssel oder Siegel: %s", body)
	}
	g, err := totpBase32.DecodeString(strings.ReplaceAll(m[1], " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Wiederherstellungscodes") {
		t.Fatal("Schritt 1 zeigt schon Wiederherstellungscodes")
	}
	return g, strings.ReplaceAll(s[1], "&#43;", "+")
}

func referrerPolicy(t *testing.T, rec *httptest.ResponseRecorder, wo string) {
	t.Helper()
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("%s: Referrer-Policy %q (F-2895)", wo, rec.Header().Get("Referrer-Policy"))
	}
}

func TestEinloesen1077_SeiteUndReferrer(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	tok := x.einladen(t, "anna@example.org")
	rec := x.hole(t, EinladungPfadPraefix+tok, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "anna@example.org") || !strings.Contains(body, `name="passwort2"`) ||
		!strings.Contains(body, "Mit Google anmelden") || !strings.Contains(body, "Mit Team-Konto anmelden") {
		t.Fatalf("Seite: %d %s", rec.Code, body)
	}
	referrerPolicy(t, rec, "GET")
	if rec.Header().Get("Content-Security-Policy") != AnmeldeSeitenCSP {
		t.Errorf("CSP %q", rec.Header().Get("Content-Security-Policy"))
	}
	// Unbekannt: 404, auch dann ohne Referer.
	fremd := strings.Repeat("Z", EinladungTokenLaenge)
	for _, rec := range []*httptest.ResponseRecorder{x.hole(t, EinladungPfadPraefix+fremd, nil), x.post(t, fremd, url.Values{"weg": {"google"}})} {
		if rec.Code != http.StatusNotFound {
			t.Errorf("unbekanntes Token: %d", rec.Code)
		}
		referrerPolicy(t, rec, "unbekannt")
	}
	// Sitzungs-Kritik 03.11. A-1: Anbieter-Wahl ist ein Link (GET), kein
	// Formular — CSP form-action 'self' griffe in Chromium sonst auch auf die
	// Weiterleitung zum Anbieter.
	if !strings.Contains(body, `href="`+EinladungPfadPraefix+tok+`?weg=google"`) || strings.Contains(body, `value="google"`) {
		t.Errorf("Anbieter-Wahl kein Link: %s", body)
	}
	if rec := x.post(t, tok, url.Values{"weg": {"google"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("POST weg=google: %d, erwartet 400", rec.Code)
	}
	// Von einer fremden Seite ausgelöst: kein Start, nur die Auswahl.
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+EinladungPfadPraefix+tok+"?weg=google", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	fremdRec := httptest.NewRecorder()
	x.mux.ServeHTTP(fremdRec, r)
	if fremdRec.Code != http.StatusOK || fremdRec.Header().Get("Location") != "" {
		t.Errorf("cross-site Start: %d Location %q", fremdRec.Code, fremdRec.Header().Get("Location"))
	}
	for _, sfs := range []string{"same-origin", "none"} {
		r := httptest.NewRequest(http.MethodGet, "https://board.example"+EinladungPfadPraefix+tok+"?weg=google", nil)
		r.Header.Set("Sec-Fetch-Site", sfs)
		rec := httptest.NewRecorder()
		x.mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusFound {
			t.Errorf("Sec-Fetch-Site %s: %d", sfs, rec.Code)
		}
	}
	if rec := x.hole(t, EinladungPfadPraefix+tok+"?weg=password", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("?weg=password: %d", rec.Code)
	}
	// Nur die Wege aus ABOARD_INVITE_METHODS.
	x.ein.Wege = []string{"authelia"}
	body = x.hole(t, EinladungPfadPraefix+tok, nil).Body.String()
	if strings.Contains(body, `name="passwort2"`) || strings.Contains(body, "Mit Google") || !strings.Contains(body, "Team-Konto") {
		t.Errorf("Wege nicht gefiltert: %s", body)
	}
	if rec := x.post(t, tok, url.Values{"weg": {"password"}, "passwort": {"lang-genug-1234"}, "passwort2": {"lang-genug-1234"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("nicht angebotener Weg: %d", rec.Code)
	}
	if rec := x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("nicht angebotener Anbieter per Link: %d", rec.Code)
	}
}

func TestEinloesen1077_PasswortWeg(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")

	for name, f := range map[string]url.Values{
		"ungleich": {"weg": {"password"}, "passwort": {"lang-genug-1234"}, "passwort2": {"lang-genug-1235"}},
		"zu kurz":  {"weg": {"password"}, "passwort": {"kurz"}, "passwort2": {"kurz"}},
		"ohne weg": {"passwort": {"lang-genug-1234"}, "passwort2": {"lang-genug-1234"}},
		"falsch":   {"weg": {"github"}},
	} {
		if rec := x.post(t, tok, f); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	g, siegel := x.schritt1(t, tok)
	// Falscher Code: Schritt 2 noch einmal, nichts eingelöst.
	rec := x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel}, "code": {"000000"}})
	if totpCode(g, TOTPSchritt(x.jetzt)) == "000000" {
		t.Skip("Zufall: der falsche Code ist der richtige")
	}
	if rec.Code != http.StatusUnprocessableEntity || x.zustand(t, "anna@example.org") != storage.KontoEingeladen {
		t.Fatalf("falscher Code: %d, Zustand %q", rec.Code, x.zustand(t, "anna@example.org"))
	}
	// Siegel eines anderen Links gilt hier nicht.
	tok2 := x.einladen(t, "ben@example.org")
	_, fremdesSiegel := x.schritt1(t, tok2)
	code := totpCode(g, TOTPSchritt(x.jetzt))
	if rec := x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {fremdesSiegel}, "code": {code}}); rec.Code != http.StatusBadRequest {
		t.Errorf("fremdes Siegel: %d", rec.Code)
	}
	rec = x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel}, "code": {code}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Count(body, "<li><code>") != WiederherstellungsAnzahl || !strings.Contains(body, "wartet auf die Bestätigung") {
		t.Fatalf("Schritt 2: %d %s", rec.Code, body)
	}
	referrerPolicy(t, rec, "Schritt 2")
	if z := x.zustand(t, "anna@example.org"); z != storage.KontoWartet {
		t.Fatalf("Zustand %q, erwartet pending", z)
	}
	// Der Code der Einrichtung gilt danach nicht noch einmal.
	var letzter int64
	if err := x.db.QueryRow(`SELECT totp_letzter_schritt FROM konten WHERE email = 'anna@example.org'`).Scan(&letzter); err != nil || letzter < TOTPSchritt(x.jetzt)-1 {
		t.Errorf("letzter Schritt %d (%v)", letzter, err)
	}
	// Weitergeleiteter Link ohne Bestätigung kommt nicht herein; der Link
	// ist verbraucht.
	if ok, _ := x.tor.Erlaubt(context.Background(), "anna@example.org"); ok {
		t.Fatal("Tor lässt ein unbestätigtes Konto herein")
	}
	if rec := x.hole(t, EinladungPfadPraefix+tok, nil); rec.Code != http.StatusNotFound {
		t.Errorf("benutzter Link: %d", rec.Code)
	}
	if rec := x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel}, "code": {code}}); rec.Code != http.StatusNotFound {
		t.Errorf("Schritt 2 wiederholt: %d", rec.Code)
	}
	// Abgelaufene Einrichtung (Schritt 1 älter als 15 min).
	g2, siegel2 := x.schritt1(t, tok2)
	x.jetzt = x.jetzt.Add(einrichtungGueltig + time.Second)
	if rec := x.post(t, tok2, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel2},
		"code": {totpCode(g2, TOTPSchritt(x.jetzt))}}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "abgelaufen") {
		t.Errorf("abgelaufene Einrichtung: %d", rec.Code)
	}
}

// Ohne Admin gilt die Einladung als bestätigt (Nachtrag #1076): die erste
// Person wird Admin und kann sich sofort anmelden.
func TestEinloesen1077_ErsterAdmin(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	tok := x.einladen(t, "erste@example.org")
	g, siegel := x.schritt1(t, tok)
	rec := x.post(t, tok, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel}, "code": {totpCode(g, TOTPSchritt(x.jetzt))}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jetzt anmelden") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if z := x.zustand(t, "erste@example.org"); z != storage.KontoAktiv {
		t.Fatalf("Zustand %q", z)
	}
	var admin bool
	if err := x.db.QueryRow(`SELECT is_admin FROM users WHERE email = 'erste@example.org'`).Scan(&admin); err != nil || !admin {
		t.Fatalf("erste Person nicht Admin: %v %v", admin, err)
	}
	if ok, err := x.tor.Erlaubt(context.Background(), "erste@example.org"); !ok || err != nil {
		t.Fatalf("Tor nach Bestätigung: %v %v (Vergessen?)", ok, err)
	}
}

func inviteZustand(t *testing.T, rec *httptest.ResponseRecorder) (*http.Cookie, url.Values) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("Start beim Anbieter: %d %s", rec.Code, rec.Body.String())
	}
	referrerPolicy(t, rec, "Weiterleitung zum Anbieter")
	ziel, _ := url.Parse(rec.Header().Get("Location"))
	for _, c := range rec.Result().Cookies() {
		if c.Name == AnmeldungCookie {
			return c, ziel.Query()
		}
	}
	t.Fatal("kein Anmelde-Cookie")
	return nil, nil
}

func TestEinloesen1077_AnbieterWeg(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	// Der Anbieter meldet eine andere Adresse: gebunden wird trotzdem an das
	// eingeladene Konto.
	rec := x.melde(t, x.google, "google", c, q, "i1", map[string]any{"sub": "sub-privat", "email": "anna.privat@example.com", "name": "Anna P."})
	if rec.Code != http.StatusOK || sitzungsCookie(rec) != nil || !strings.Contains(rec.Body.String(), "Bestätigung") {
		t.Fatalf("Rückruf invite: %d %s", rec.Code, rec.Body.String())
	}
	referrerPolicy(t, rec, "Rückruf invite")
	var weg, gemeldet, name string
	if err := x.db.QueryRow(`SELECT weg, gemeldete_email, gemeldeter_name FROM konten WHERE email = 'anna@example.org'`).Scan(&weg, &gemeldet, &name); err != nil ||
		weg != "google" || gemeldet != "anna.privat@example.com" || name != "Anna P." {
		t.Fatalf("Konto: %q %q %q %v", weg, gemeldet, name, err)
	}
	// Anmelden vor der Bestätigung: „wartet“.
	c, q = x.starte(t, "google")
	if rec := x.melde(t, x.google, "google", c, q, "i2", map[string]any{"sub": "sub-privat", "email": "anna.privat@example.com"}); rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("vor der Bestätigung: %d", rec.Code)
	}
	// Bestätigt: Die Sitzung trägt die eingeladene Adresse, nicht die gemeldete.
	if _, err := x.konten.Bestaetigen(context.Background(), kontoID(t, x.db, "anna@example.org"), NutzerAnlegen(storage.NewBootstrapRepo(x.db)), 1); err != nil {
		t.Fatal(err)
	}
	x.tor.Vergessen("anna@example.org")
	c, q = x.starte(t, "google")
	if rec := x.melde(t, x.google, "google", c, q, "i3", map[string]any{"sub": "sub-privat", "email": "anna.privat@example.com"}); x.sitzungsAdresse(t, rec) != "anna@example.org" {
		t.Fatalf("nach der Bestätigung: %d %s", rec.Code, rec.Body.String())
	}
	// Dasselbe Anbieter-Konto löst keine zweite Einladung ein.
	tok2 := x.einladen(t, "ben@example.org")
	c, q = inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok2+"?weg=google", nil))
	if rec := x.melde(t, x.google, "google", c, q, "i4", map[string]any{"sub": "sub-privat"}); rec.Code != http.StatusConflict {
		t.Fatalf("belegtes Anbieter-Konto: %d", rec.Code)
	}
	if x.zustand(t, "ben@example.org") != storage.KontoEingeladen {
		t.Fatal("Ben trotz belegter Bindung eingelöst")
	}
}

func TestEinloesen1077_AnbieterOhneAdminMeldetAn(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	tok := x.einladen(t, "erste@example.org")
	c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=authelia", nil))
	rec := x.melde(t, x.authelia, "authelia", c, q, "e1", map[string]any{"sub": "sub-erste", "email": "egal@example.com"})
	if rec.Code != http.StatusFound || x.sitzungsAdresse(t, rec) != "erste@example.org" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Sprint-Abschluss 109 K1-4: Ein paralleler normaler Anmeldevorgang
// überschreibt den Einladungs-Zustand. Die Fehlerseite sagt, dass der
// Einladungslink noch einmal zu öffnen ist — und der Link gilt noch.
func TestEinloesen1077_EinCookieHinweis(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	x.mitAdmin()
	tok := x.einladen(t, "anna@example.org")
	_, qInvite := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
	cLogin, _ := x.starte(t, "google") // anderer Tab: überschreibt das Cookie
	rec := x.melde(t, x.google, "google", cLogin, qInvite, "k1", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Einladungslink") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if r := x.hole(t, EinladungPfadPraefix+tok, nil); r.Code != http.StatusOK {
		t.Fatalf("Link nach dem Abbruch nicht mehr gültig: %d", r.Code)
	}
}

// F-2903: Ein aktives Konto ohne Bindung lässt sich NICHT über einen Anbieter
// betreten, der dieselbe bestätigte Adresse meldet — sonst ginge es am
// Passwort und an der Pflicht-2FA vorbei. Nur die Vorab-Liste bindet beim
// ersten Login.
func TestEinloesen1077_KeineBindungPerAdresseFuerKonten(t *testing.T) {
	x := neuerEinloeseTest(t, "liste@example.com")
	x.exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, angelegt, geaendert) VALUES ('k-pw', 'anna@example.com', 'active', 'password', 'x', 1, 1)`)
	c, q := x.starte(t, "google")
	rec := x.melde(t, x.google, "google", c, q, "f1", map[string]any{"sub": "fremd", "email": "anna@example.com"})
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("aktives Konto per Adresse betreten: %d", rec.Code)
	}
	var n int
	if err := x.db.QueryRow(`SELECT COUNT(*) FROM anbieter_bindungen`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Bindung angelegt: %d %v", n, err)
	}
	// Die Vorab-Liste bindet beim ersten Login wie bisher.
	c, q = x.starte(t, "google")
	if rec := x.melde(t, x.google, "google", c, q, "f2", map[string]any{"sub": "liste", "email": "liste@example.com"}); x.sitzungsAdresse(t, rec) != "liste@example.com" {
		t.Fatalf("Vorab-Liste: %d", rec.Code)
	}
}

func kontoID(t *testing.T, db *sql.DB, email string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM konten WHERE email = ?`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEinladungsWege1077(t *testing.T) {
	anb := []AnbieterKonfig{{Kuerzel: "google"}, {Kuerzel: "heim"}}
	for _, f := range []struct {
		passwort bool
		invite   string
		want     string
		fehler   string
	}{
		{true, "", "password,google,heim", ""},
		{false, "", "google,heim", ""},
		{true, "heim, password", "password,heim", ""},
		{true, "google", "google", ""},
		{false, "password", "", "kein angebotener Anmeldeweg"},
		{true, "github", "", "kein angebotener Anmeldeweg"},
		{true, "google,google", "", "doppelt"},
		{true, " , ", "", "enthält keinen Weg"},
	} {
		got, fehler := EinladungsWege(anb, f.passwort, f.invite)
		if f.fehler != "" {
			if len(fehler) == 0 || !strings.Contains(strings.Join(fehler, "|"), f.fehler) || !strings.Contains(strings.Join(fehler, "|"), InviteMethodsEnv) {
				t.Errorf("%v %q: Fehler %v, erwartet %q", f.passwort, f.invite, fehler, f.fehler)
			}
			continue
		}
		if strings.Join(got, ",") != f.want || len(fehler) != 0 {
			t.Errorf("%v %q: %v %v, erwartet %s", f.passwort, f.invite, got, fehler, f.want)
		}
	}
}
