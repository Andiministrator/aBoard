package api

// Tests zum Code-Schritt (ADR-0149 §3, Kritik W5; Karte #1075). Testdaten nur
// example.org.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

const codeTestSchluessel = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeTOTP struct {
	geheimnis map[string]string
	letzter   map[string]int64
	codes     map[string]bool // hash → benutzt
	fehler    error
}

func (f *fakeTOTP) TOTPGeheimnis(_ context.Context, id string) (string, int64, error) {
	if f.fehler != nil {
		return "", 0, f.fehler
	}
	return f.geheimnis[id], f.letzter[id], nil
}

func (f *fakeTOTP) TOTPSchrittVerbrauchen(_ context.Context, id string, s int64) (bool, error) {
	if s <= f.letzter[id] {
		return false, nil
	}
	f.letzter[id] = s
	return true, nil
}

func (f *fakeTOTP) CodeVerbrauchen(_ context.Context, id, h string, _ int64) (bool, error) {
	benutzt, da := f.codes[id+"|"+h]
	if !da || benutzt {
		return false, nil
	}
	f.codes[id+"|"+h] = true
	return true, nil
}

type codeTest struct {
	*passwortTest
	totp      *fakeTOTP
	sitzungen *fakeAnmeldeSitzungen
	geheimnis []byte
	codes     []string
}

func neuerCodeTest(t *testing.T) *codeTest {
	t.Helper()
	pt := neuerPasswortTest(t)
	box, err := secretcrypto.New(codeTestSchluessel)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NeueTOTPEinrichtung("anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	versiegelt, err := box.Seal(e.Geheimnis)
	if err != nil {
		t.Fatal(err)
	}
	ct := &codeTest{passwortTest: pt, sitzungen: &fakeAnmeldeSitzungen{}, geheimnis: e.Geheimnis, codes: e.Codes,
		totp: &fakeTOTP{geheimnis: map[string]string{"k-anna": versiegelt}, letzter: map[string]int64{}, codes: map[string]bool{}}}
	for _, h := range e.CodeHashes {
		ct.totp.codes["k-anna|"+h] = false
	}
	if err := pt.p.MitZweitemSchritt(ct.totp, box, ZweiterSchrittSchluessel(codeTestSchluessel), ct.sitzungen); err != nil {
		t.Fatal(err)
	}
	pt.p.jetzt = pt.uhr.jetzt
	return ct
}

// passwort meldet Anna an und liefert das Zwischen-Cookie.
func (ct *codeTest) passwort(t *testing.T) *http.Cookie {
	t.Helper()
	rec := ct.post("anna@example.org", richtig, "192.0.2.1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/anmeldung/code"`) {
		t.Fatalf("Passwort-Schritt: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ZweiterSchrittCookie && c.Value != "" {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 300 {
				t.Errorf("Cookie-Attribute: %+v", c)
			}
			return c
		}
	}
	t.Fatal("kein Zwischen-Cookie")
	return nil
}

func (ct *codeTest) code(c *http.Cookie, code, remote string, mehr ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+AnmeldungCodePfad, strings.NewReader(url.Values{"code": {code}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remote + ":4711"
	if c != nil {
		r.AddCookie(c)
	}
	for _, m := range mehr {
		r.AddCookie(m)
	}
	rec := httptest.NewRecorder()
	ct.m.CodePost(rec, r)
	return rec
}

func (ct *codeTest) aktuellerCode() string {
	return totpCode(ct.geheimnis, TOTPSchritt(ct.uhr.t))
}

func hatSitzung(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SitzungsCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestCode1075_ErfolgUndFixierung(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	if len(ct.sitzungen.angelegt) != 0 {
		t.Fatal("Sitzung schon nach dem Passwort")
	}
	rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1", &http.Cookie{Name: SitzungsCookie, Value: "mitgebracht"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || !hatSitzung(rec) {
		t.Fatalf("Erfolg: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if len(ct.sitzungen.angelegt) != 1 || ct.sitzungen.angelegt[0] != "anna@example.org" ||
		len(ct.sitzungen.beendet) != 1 || ct.sitzungen.beendet[0] != "mitgebracht" {
		t.Fatalf("angelegt %v, beendet %v", ct.sitzungen.angelegt, ct.sitzungen.beendet)
	}
	// Das Zwischen-Cookie wird gelöscht und gilt nicht noch einmal.
	geloescht := false
	for _, k := range rec.Result().Cookies() {
		geloescht = geloescht || (k.Name == ZweiterSchrittCookie && k.MaxAge < 0)
	}
	if !geloescht {
		t.Error("Zwischen-Cookie nicht gelöscht")
	}
	ct.uhr.t = ct.uhr.t.Add(TOTPSchrittweite)
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) ||
		!strings.Contains(rec.Body.String(), "abgelaufen") {
		t.Fatalf("Zwischen-Cookie zweimal: %d %s", rec.Code, rec.Body.String())
	}
}

// Derselbe TOTP-Code gilt nur einmal, auch in einem neuen Anmeldevorgang.
func TestCode1075_TOTPNurEinmal(t *testing.T) {
	ct := neuerCodeTest(t)
	code := ct.aktuellerCode()
	if rec := ct.code(ct.passwort(t), code, "192.0.2.1"); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	ct.uhr.t = ct.uhr.t.Add(2 * time.Second)
	rec := ct.code(ct.passwort(t), code, "192.0.2.1")
	if rec.Code != http.StatusUnauthorized || hatSitzung(rec) || !strings.Contains(rec.Body.String(), "schon benutzt") {
		t.Fatalf("Code zweimal: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCode1075_Wiederherstellungscode(t *testing.T) {
	ct := neuerCodeTest(t)
	// In Großbuchstaben und mit Leerzeichen statt Bindestrichen.
	eingabe := strings.ToUpper(strings.ReplaceAll(ct.codes[3], "-", " "))
	if rec := ct.code(ct.passwort(t), eingabe, "192.0.2.1"); rec.Code != http.StatusSeeOther || !hatSitzung(rec) {
		t.Fatalf("Wiederherstellungscode: %d %s", rec.Code, rec.Body.String())
	}
	ct.uhr.t = ct.uhr.t.Add(2 * time.Second)
	if rec := ct.code(ct.passwort(t), ct.codes[3], "192.0.2.1"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) {
		t.Fatalf("Wiederherstellungscode zweimal: %d", rec.Code)
	}
	// Ein Code eines anderen Kontos passt nicht: die Codes hängen am Konto.
	ct.totp.codes["k-ben|"+WiederherstellungsHash("bbbb-bbbb-bbbb")] = false
	ct.uhr.t = ct.uhr.t.Add(5 * time.Second)
	if rec := ct.code(ct.passwort(t), "bbbb-bbbb-bbbb", "192.0.2.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("fremder Code: %d", rec.Code)
	}
}

func TestCode1075_ZuVieleFalsche(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	for i := 0; i < zweiterSchrittMaxFehler-1; i++ {
		ct.uhr.t = ct.uhr.t.Add(PasswortWarteMax) // Wartezeit der Adresse abwarten
		if rec := ct.code(c, "000000", "198.51.100."+string(rune('1'+i))); rec.Code != http.StatusUnauthorized ||
			!strings.Contains(rec.Body.String(), `action="/anmeldung/code"`) {
			t.Fatalf("Fehler %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	ct.uhr.t = ct.uhr.t.Add(PasswortWarteMax)
	if rec := ct.code(c, "000000", "198.51.100.9"); !strings.Contains(rec.Body.String(), "Zu viele falsche Codes") {
		t.Fatalf("fünfter Fehler: %d %s", rec.Code, rec.Body.String())
	}
	// Danach hilft auch der richtige Code nicht mehr: von vorn mit dem Passwort.
	ct.uhr.t = ct.uhr.t.Add(PasswortWarteMax)
	if rec := ct.code(c, ct.aktuellerCode(), "198.51.100.10"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) {
		t.Fatalf("nach fünf Fehlern: %d", rec.Code)
	}
}

// Falsche Codes zählen in die Bremse der Gegenstelle und in die Wartezeit
// der Adresse; das richtige Passwort setzt diese nicht zurück.
func TestCode1075_BremseUndWartezeit(t *testing.T) {
	ct := neuerCodeTest(t)
	rec := ct.code(ct.passwort(t), "000000", "192.0.2.1")
	if rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	// Sofort danach: Wartezeit — auch nach erneutem richtigem Passwort.
	c := ct.passwort2(t)
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.2"); rec.Code != http.StatusTooManyRequests || hatSitzung(rec) {
		t.Fatalf("Wartezeit nach falschem Code: %d %s", rec.Code, rec.Body.String())
	}
	// Gegenstelle: fünf falsche Codes von einem Anschluss sperren ihn.
	ct2 := neuerCodeTest(t)
	for i := 0; i < PasswortMaxFehlversuche; i++ {
		ct2.uhr.t = ct2.uhr.t.Add(PasswortWarteMax)
		ct2.code(ct2.passwortVon(t, "203.0.113.5"), "000000", "203.0.113.5")
	}
	ct2.uhr.t = ct2.uhr.t.Add(PasswortWarteMax)
	if rec := ct2.post("anna@example.org", richtig, "203.0.113.5"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Anschluss nach fünf falschen Codes nicht gesperrt: %d", rec.Code)
	}
}

// passwort2 meldet Anna erneut an, ohne die Wartezeit der Adresse
// abzuwarten — sie gilt für das Passwort ebenso; hier gezielt umgangen, um
// den Code-Schritt allein zu prüfen.
func (ct *codeTest) passwort2(t *testing.T) *http.Cookie {
	t.Helper()
	k := adressSchluessel("anna@example.org")
	gespeichert := ct.p.adressen.eintrag[k]
	delete(ct.p.adressen.eintrag, k)
	c := ct.passwort(t)
	ct.p.adressen.eintrag[k] = gespeichert
	return c
}

func (ct *codeTest) passwortVon(t *testing.T, remote string) *http.Cookie {
	t.Helper()
	rec := ct.post("anna@example.org", richtig, remote)
	for _, c := range rec.Result().Cookies() {
		if c.Name == ZweiterSchrittCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("kein Zwischen-Cookie: %d %s", rec.Code, rec.Body.String())
	return nil
}

func TestCode1075_CookieVerfaelschtOderAbgelaufen(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	teil, sig, _ := strings.Cut(c.Value, ".")
	for name, wert := range map[string]string{
		"ohne Signatur":    teil,
		"falsche Signatur": teil + "." + sig[:len(sig)-2] + "AA",
		"leer":             "",
		"fremder Schlüssel": func() string {
			p := &PasswortAnmeldung{schluessel: ZweiterSchrittSchluessel("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")}
			return teil + "." + p.signiere(teil)
		}(),
	} {
		rec := ct.code(&http.Cookie{Name: ZweiterSchrittCookie, Value: wert}, ct.aktuellerCode(), "192.0.2.1")
		if rec.Code != http.StatusUnauthorized || hatSitzung(rec) || !strings.Contains(rec.Body.String(), "abgelaufen") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := ct.code(nil, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) {
		t.Errorf("ohne Cookie: %d", rec.Code)
	}
	// Nach fünf Minuten abgelaufen, auch mit gültiger Signatur.
	ct.uhr.t = ct.uhr.t.Add(zweiterSchrittGueltig)
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) {
		t.Errorf("abgelaufen: %d", rec.Code)
	}
}

// Ein signiertes Cookie, dessen Kennung der Server nicht kennt (Neustart,
// oder ein selbst gebautes mit fremder Konto-ID), gilt nicht.
func TestCode1075_UnbekannteKennung(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	// Neustart: Speicher leer.
	ct.p.zweiter.offen = map[string]*offenerSchritt{}
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusUnauthorized || hatSitzung(rec) {
		t.Fatalf("nach Neustart: %d", rec.Code)
	}
}

func TestCode1075_GesperrtZwischenPasswortUndCode(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	k := ct.konten.konten["anna@example.org"]
	k.Zustand = storage.KontoGesperrt
	ct.konten.konten["anna@example.org"] = k
	rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1")
	if rec.Code != http.StatusForbidden || hatSitzung(rec) || len(ct.sitzungen.angelegt) != 0 {
		t.Fatalf("gesperrt: %d", rec.Code)
	}
	// Tor nein (z. B. von der Liste gestrichen und kein aktives Konto).
	ct = neuerCodeTest(t)
	c = ct.passwort(t)
	ct.tor["anna@example.org"] = false
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusForbidden || hatSitzung(rec) {
		t.Fatalf("Tor nein: %d", rec.Code)
	}
}

func TestCode1075_OhneTOTPKeinZweiterSchritt(t *testing.T) {
	ct := neuerCodeTest(t)
	k := ct.konten.konten["anna@example.org"]
	k.HatTOTP = false
	ct.konten.konten["anna@example.org"] = k
	rec := ct.post("anna@example.org", richtig, "192.0.2.1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Zwei-Faktor fehlt") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ZweiterSchrittCookie {
			t.Fatal("Zwischen-Cookie ohne 2FA")
		}
	}
}

// Ist das Geheimnis nicht lesbar (ABOARD_SECRET_KEY gewechselt), gibt es eine
// verständliche Seite und keine Sitzung — und keinen Hinweis „Code falsch“.
func TestCode1075_GeheimnisUnlesbar(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	ct.totp.geheimnis["k-anna"] = "nicht-versiegelt"
	rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1")
	if rec.Code != http.StatusInternalServerError || hatSitzung(rec) || !strings.Contains(rec.Body.String(), "nicht lesbar") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	ct = neuerCodeTest(t)
	c = ct.passwort(t)
	ct.totp.fehler = errors.New("db weg")
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusInternalServerError || hatSitzung(rec) {
		t.Fatalf("Lesefehler: %d", rec.Code)
	}
}

func TestCode1075_OhneZweitenSchritt404(t *testing.T) {
	pt := neuerPasswortTest(t) // ohne MitZweitemSchritt
	rec := httptest.NewRecorder()
	pt.m.CodePost(rec, httptest.NewRequest(http.MethodPost, "https://board.example"+AnmeldungCodePfad, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%d", rec.Code)
	}
}
