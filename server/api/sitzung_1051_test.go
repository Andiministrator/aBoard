package api

// Tests zur Anmelde-Weiche des OIDC-Betriebs (ADR-0148 §1, §2, §4; Karte #1051).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const testSitzungsID = "abcdefghijabcdefghijabcdefghijabcdefghijabc" // 43 Zeichen

type fakeSitzungen struct {
	gueltig  map[string]*storage.Sitzung
	fehler   error
	beendet  []string
	geprueft int
}

func (f *fakeSitzungen) Pruefen(_ context.Context, id string) (*storage.Sitzung, error) {
	f.geprueft++
	if f.fehler != nil {
		return nil, f.fehler
	}
	if s, ok := f.gueltig[id]; ok {
		kopie := *s
		return &kopie, nil
	}
	return nil, storage.ErrSitzungUnbekannt
}

func (f *fakeSitzungen) Beenden(_ context.Context, id string) error {
	f.beendet = append(f.beendet, id)
	return nil
}

// spion merkt sich, ob und mit welcher Identität und welchen Kopfzeilen die
// Anfrage hinter der Weiche ankam.
type spion struct {
	aufgerufen bool
	identitaet *ForwardAuthIdentity
	kopf       http.Header
}

func (s *spion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.aufgerufen = true
	s.identitaet = IdentityFromContext(r.Context())
	s.kopf = r.Header.Clone()
	w.WriteHeader(http.StatusOK)
}

func weicheTest(t *testing.T, zugelassen func(string) bool) (*fakeSitzungen, *spion, http.Handler) {
	t.Helper()
	t.Setenv(AnmeldungEnvVar, "oidc")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	f := &fakeSitzungen{gueltig: map[string]*storage.Sitzung{
		testSitzungsID: {Email: "anna@example.com", Name: "Anna"},
	}}
	sp := &spion{}
	w := &Sitzungsweiche{Sitzungen: f, Zugelassen: nurListe(zugelassen)}
	return f, sp, w.Middleware(sp)
}

func alleZugelassen(string) bool { return true }

// nurListe macht aus einer reinen Listenprüfung ein Tor ohne Konten.
func nurListe(f func(string) bool) func(context.Context, string) (bool, error) {
	if f == nil {
		return nil
	}
	return func(_ context.Context, e string) (bool, error) { return f(e), nil }
}

func mitSitzung(r *http.Request) *http.Request {
	r.AddCookie(&http.Cookie{Name: SitzungsCookie, Value: testSitzungsID})
	return r
}

func TestSitzungsweiche1051_OhneSitzung_SeiteGehtZurAnmeldung(t *testing.T) {
	_, sp, h := weicheTest(t, alleZugelassen)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != AnmeldungSeitePfad {
		t.Fatalf("Seitenaufruf ohne Sitzung: %d Location=%q, erwartet 302 %s", rec.Code, rec.Header().Get("Location"), AnmeldungSeitePfad)
	}
	if sp.aufgerufen {
		t.Fatal("Anfrage ohne Sitzung kam hinter der Weiche an")
	}
}

func TestSitzungsweiche1051_OhneSitzung_APIBekommt401(t *testing.T) {
	_, sp, h := weicheTest(t, alleZugelassen)
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(m, "/boards", nil)
		r.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "session required") {
			t.Errorf("%s /boards ohne Sitzung: %d %q, erwartet 401 JSON", m, rec.Code, rec.Body.String())
		}
	}
	if sp.aufgerufen {
		t.Fatal("Anfrage ohne Sitzung kam hinter der Weiche an")
	}
}

// ADR-0148 §4: Keine Ausnahme nach einer Kopfzeile, die der Client setzen
// kann (Falle F-945 K-1). Jede Fälschung bleibt 302/401.
func TestSitzungsweiche1051_GefaelschteKopfzeilenOeffnenNichts(t *testing.T) {
	faelschungen := []map[string]string{
		{"X-Forwarded-Uri": "/healthz"},
		{"X-Original-URL": "/healthz"},
		{"X-Original-Uri": "/anmeldung/start"},
		{"X-Rewrite-URL": "/healthz"},
		{"X-Forwarded-Prefix": "/healthz"},
		{"Remote-Email": "anna@example.com", "Remote-User": "anna"},
		{"Remote-Email": "anna@example.com", forwardAuthSecretHeader: "geheim"},
	}
	for _, kopf := range faelschungen {
		for _, accept := range []string{"text/html", "application/json"} {
			_, sp, h := weicheTest(t, alleZugelassen)
			t.Setenv("ABOARD_FORWARD_AUTH_SECRET", "geheim")
			r := httptest.NewRequest(http.MethodGet, "/boards", nil)
			r.Header.Set("Accept", accept)
			for k, v := range kopf {
				r.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
				t.Errorf("%v (%s): %d, erwartet 302/401", kopf, accept, rec.Code)
			}
			if sp.aufgerufen {
				t.Errorf("%v (%s): Anfrage kam ohne Sitzung hinter der Weiche an", kopf, accept)
			}
		}
	}
}

func TestSitzungsweiche1051_OffenePfadeNurNachEchtemPfad(t *testing.T) {
	offen := []struct{ methode, pfad string }{
		{http.MethodGet, "/healthz"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/ical/calendar.ics"},
		{http.MethodGet, AnmeldungStartPfad},
		{http.MethodGet, AnmeldungRueckrufPfad},
	}
	for _, f := range offen {
		_, sp, h := weicheTest(t, alleZugelassen)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(f.methode, f.pfad, nil))
		if !sp.aufgerufen || sp.identitaet != nil {
			t.Errorf("%s %s: aufgerufen=%v identitaet=%v, erwartet offen und ohne Identität", f.methode, f.pfad, sp.aufgerufen, sp.identitaet)
		}
	}
	zu := []struct{ methode, pfad string }{
		{http.MethodPost, AnmeldungStartPfad},
		{http.MethodPost, AnmeldungRueckrufPfad},
		{http.MethodGet, "/anmeldung%2Fstart"},
		{http.MethodGet, "/anmeldung/start/"},
		{http.MethodGet, "/Anmeldung/start"},
		{http.MethodGet, "/anmeldung/startx"},
		{http.MethodGet, "/anmeldung/"},
	}
	for _, f := range zu {
		_, sp, h := weicheTest(t, alleZugelassen)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(f.methode, f.pfad, nil))
		if sp.aufgerufen {
			t.Errorf("%s %s: ohne Sitzung durchgelassen", f.methode, f.pfad)
		}
	}
}

func TestSitzungsweiche1051_GueltigeSitzungSetztIdentitaet(t *testing.T) {
	_, sp, h := weicheTest(t, alleZugelassen)
	r := mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil))
	// Eine mitgeschickte fremde Identität zählt nicht und kommt nicht durch.
	r.Header.Set("Remote-Email", "mallory@example.com")
	r.Header.Set("Remote-User", "mallory")
	r.Header.Set(forwardAuthSecretHeader, "x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if !sp.aufgerufen || sp.identitaet == nil {
		t.Fatalf("gültige Sitzung nicht durchgelassen: %d", rec.Code)
	}
	if sp.identitaet.Email != "anna@example.com" || sp.identitaet.Name != "Anna" || sp.identitaet.User != "" {
		t.Fatalf("Identität %+v, erwartet die der Sitzung", sp.identitaet)
	}
	for _, k := range identitaetsHeader {
		if sp.kopf.Get(k) != "" {
			t.Errorf("Kopfzeile %s kam hinter der Weiche an", k)
		}
	}
}

// ADR-0148 §3: Zulassung bei JEDER Anfrage; fehlt die Prüfung, kommt niemand
// herein (fail-closed).
func TestSitzungsweiche1051_ZulassungBeiJederAnfrage(t *testing.T) {
	for name, z := range map[string]func(string) bool{
		"gestrichen":     func(string) bool { return false },
		"keine Prüfung":  nil,
		"andere Adresse": func(e string) bool { return e == "bert@example.com" },
	} {
		_, sp, h := weicheTest(t, z)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil)))
		if rec.Code != http.StatusUnauthorized || sp.aufgerufen {
			t.Errorf("%s: %d aufgerufen=%v, erwartet 401", name, rec.Code, sp.aufgerufen)
		}
	}
	// Die Prüfung bekommt die normalisierte Adresse.
	var gesehen string
	f, sp, h := weicheTest(t, func(e string) bool { gesehen = e; return true })
	f.gueltig[testSitzungsID].Email = " Anna@Example.COM"
	h.ServeHTTP(httptest.NewRecorder(), mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil)))
	if gesehen != "anna@example.com" || !sp.aufgerufen || sp.identitaet.Email != "anna@example.com" {
		t.Fatalf("Zulassung sah %q, Identität %+v", gesehen, sp.identitaet)
	}
}

func TestSitzungsweiche1051_OhneSpeicherNiemand(t *testing.T) {
	t.Setenv(AnmeldungEnvVar, "oidc")
	sp := &spion{}
	h := (&Sitzungsweiche{Zugelassen: nurListe(alleZugelassen)}).Middleware(sp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil)))
	if rec.Code != http.StatusUnauthorized || sp.aufgerufen {
		t.Fatalf("Weiche ohne Speicher: %d aufgerufen=%v", rec.Code, sp.aufgerufen)
	}
}

func TestSitzungsweiche1051_SpeicherfehlerIst500(t *testing.T) {
	f, sp, h := weicheTest(t, alleZugelassen)
	f.fehler = errors.New("platte voll")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodGet, "/boards", nil)))
	if rec.Code != http.StatusInternalServerError || sp.aufgerufen {
		t.Fatalf("Speicherfehler: %d aufgerufen=%v, erwartet 500 ohne Durchlass", rec.Code, sp.aufgerufen)
	}
}

func TestSitzungsweiche1051_Abmelden(t *testing.T) {
	f, sp, h := weicheTest(t, alleZugelassen)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodPost, AnmeldungEndePfad, nil)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST %s: %d, erwartet 204", AnmeldungEndePfad, rec.Code)
	}
	if len(f.beendet) != 1 || f.beendet[0] != testSitzungsID {
		t.Fatalf("Sitzung nicht beendet: %v", f.beendet)
	}
	sc := rec.Header().Get("Set-Cookie")
	if !strings.Contains(sc, SitzungsCookie+"=;") || !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("Cookie nicht gelöscht: %q", sc)
	}
	if sp.aufgerufen {
		t.Fatal("Abmelden lief in den Mux")
	}
	// Ohne Sitzung: trotzdem 204, nichts beendet.
	f2, _, h2 := weicheTest(t, alleZugelassen)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, AnmeldungEndePfad, nil))
	if rec.Code != http.StatusNoContent || len(f2.beendet) != 0 {
		t.Fatalf("Abmelden ohne Sitzung: %d beendet=%v", rec.Code, f2.beendet)
	}
	// Nur POST.
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, mitSitzung(httptest.NewRequest(http.MethodGet, AnmeldungEndePfad, nil)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s: %d, erwartet 405", AnmeldungEndePfad, rec.Code)
	}
}

func TestSitzungsweiche1051_CookieAttribute(t *testing.T) {
	rec := httptest.NewRecorder()
	SetzeSitzungsCookie(rec, testSitzungsID, time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC))
	sc := rec.Header().Get("Set-Cookie")
	for _, teil := range []string{SitzungsCookie + "=" + testSitzungsID, "Path=/", "Secure", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(sc, teil) {
			t.Errorf("Set-Cookie %q ohne %q", sc, teil)
		}
	}
	if strings.Contains(sc, "Domain=") {
		t.Errorf("__Host-Cookie darf keine Domain tragen: %q", sc)
	}
}

// ADR-0148 §1: Der Legacy-Bypass des ACLGuard ist im OIDC-Betrieb nie
// erreichbar, auch wenn eine Kette die Weiche vergisst.
func TestACLGuard1051_OIDCOhneIdentitaetIst401(t *testing.T) {
	t.Setenv(AnmeldungEnvVar, "oidc")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	g := NewACLGuard(nil, nil, nil)
	for _, f := range []struct {
		methode, pfad string
		offen         bool
	}{
		{http.MethodGet, "/boards", false},
		{http.MethodGet, "/", false},
		{http.MethodPost, "/boards", false},
		{http.MethodGet, "/healthz", true},
		{http.MethodGet, AnmeldungStartPfad, true},
		{http.MethodGet, AnmeldungRueckrufPfad, true},
	} {
		sp := &spion{}
		rec := httptest.NewRecorder()
		g.Middleware(sp).ServeHTTP(rec, httptest.NewRequest(f.methode, f.pfad, nil))
		if sp.aufgerufen != f.offen {
			t.Errorf("%s %s: durchgelassen=%v, erwartet %v (Code %d)", f.methode, f.pfad, sp.aufgerufen, f.offen, rec.Code)
		}
	}
	// Gegenprobe: Im Einzelbetrieb (beides aus) bleibt der Legacy-Bypass.
	t.Setenv(AnmeldungEnvVar, "")
	sp := &spion{}
	g.Middleware(sp).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boards", nil))
	if !sp.aufgerufen {
		t.Fatal("Einzelbetrieb: Legacy-Bypass fehlt")
	}
	// Im Forward-Auth-Betrieb sind die Anmeldewege NICHT offen.
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	sp = &spion{}
	g.Middleware(sp).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, AnmeldungStartPfad, nil))
	if sp.aufgerufen {
		t.Fatal("Forward-Auth-Betrieb: /anmeldung/start ohne Identität durchgelassen")
	}
}

func TestAnmeldungOIDCAktiv1051_Werte(t *testing.T) {
	for wert, erwartet := range map[string]bool{"": false, "oidc": true, " OIDC ": true, "0": false, "forward": false, "oidcx": false} {
		t.Setenv(AnmeldungEnvVar, wert)
		if AnmeldungOIDCAktiv() != erwartet {
			t.Errorf("ABOARD_ANMELDUNG=%q: %v, erwartet %v", wert, !erwartet, erwartet)
		}
	}
}

// Kritik A-6: Der Transfer behandelt den OIDC-Betrieb als Mehrnutzer-Betrieb,
// ohne die ACL zu befragen.
func TestTransfer1051_OIDCIstMehrnutzer(t *testing.T) {
	t.Setenv(AnmeldungEnvVar, "oidc")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	mehr, err := (&TransferAPI{}).multiUserMode(context.Background())
	if err != nil || !mehr {
		t.Fatalf("OIDC-Betrieb: mehr=%v err=%v", mehr, err)
	}
}
