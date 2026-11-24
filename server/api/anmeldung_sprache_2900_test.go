package api

// F-2900 (Karte #1093): Auch die Seiten des Anbieter-Wegs, die Abgemeldet-Seite
// und „Unbekannter Anmeldedienst“ richten sich nach Accept-Language (ADR-0149
// §2), nicht nur Passwort- und Code-Pfad.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func (x *mehrAnbieter) holeSprache(t *testing.T, pfad string, c *http.Cookie, sprache string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil)
	if sprache != "" {
		r.Header.Set("Accept-Language", sprache)
	}
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, r)
	return rec
}

func TestAnmeldeseiten2900_Sprache(t *testing.T) {
	// Jeder Fall liefert eine frische Antwort; titelDe/titelEn stehen im <h1>.
	type fall struct {
		name           string
		titelDe        string
		titelEn        string
		textEn, linkEn string
		hole           func(x *mehrAnbieter, sprache string) *httptest.ResponseRecorder
	}
	faelle := []fall{
		{"unbekannter Anbieter am Start", "Unbekannter Anmeldedienst", "Unknown sign-in service", "no such sign-in service", "Sign in again", nil},
		{"unbekannter Anbieter am Rückruf", "Unbekannter Anmeldedienst", "Unknown sign-in service", "no such sign-in service", "Sign in again", nil},
		{"Rückruf ohne Cookie", "Anmeldung abgelaufen", "Sign-in expired", "no longer valid", "Sign in again", nil},
		{"Abgemeldet", "Abgemeldet", "Signed out", "signed out of aBoard", "Sign in again", nil},
		{"Abbruch beim Anbieter", "Anmeldung abgebrochen", "Sign-in cancelled", "cancelled", "Sign in again", nil},
		{"Antwort fehlt", "Anmeldung unvollständig", "Sign-in incomplete", "response", "Sign in again", nil},
		{"Code-Tausch gescheitert", "Anmeldung gescheitert", "Sign-in failed", "did not confirm", "Sign in again", nil},
		{"falsche Signatur", "Anmeldung abgewiesen", "Sign-in rejected", "token is invalid", "Sign in again", nil},
		{"unbekannte Adresse", "Kein Zugang", "No access", "not enabled for this board", "", nil},
		{"Adresse unbestätigt", "Kein Zugang", "No access", "confirmed email address", "", nil},
		{"Anbieter nicht erreichbar", "Anmeldung gerade nicht möglich", "Sign-in not possible right now", "not reachable", "Sign in again", nil},
	}
	hole := func(name string) func(x *mehrAnbieter, t *testing.T, sprache string) *httptest.ResponseRecorder {
		return func(x *mehrAnbieter, t *testing.T, sprache string) *httptest.ResponseRecorder {
			switch name {
			case "unbekannter Anbieter am Start":
				return x.holeSprache(t, AnmeldungStartPfad+"?anbieter=github", nil, sprache)
			case "unbekannter Anbieter am Rückruf":
				return x.holeSprache(t, AnmeldungRueckrufPfad+"/github", nil, sprache)
			case "Rückruf ohne Cookie":
				return x.holeSprache(t, AnmeldungRueckrufPfad+"/google", nil, sprache)
			case "Abgemeldet":
				r := httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung/abgemeldet", nil)
				if sprache != "" {
					r.Header.Set("Accept-Language", sprache)
				}
				rec := httptest.NewRecorder()
				x.tabelle.Anbieter[0].Abgemeldet(rec, r)
				return rec
			case "Anbieter nicht erreichbar":
				x.google.weg = true
				return x.holeSprache(t, AnmeldungStartPfad+"?anbieter=google", nil, sprache)
			}
			c, q := x.starte(t, "google")
			state := url.QueryEscape(q.Get("state"))
			rueck := AnmeldungRueckrufPfad + "/google?state=" + state
			switch name {
			case "Abbruch beim Anbieter":
				return x.holeSprache(t, rueck+"&error=access_denied", c, sprache)
			case "Antwort fehlt":
				return x.holeSprache(t, rueck, c, sprache)
			case "Code-Tausch gescheitert":
				return x.holeSprache(t, rueck+"&code=unbekannt", c, sprache)
			case "falsche Signatur":
				x.google.falscheSignatur = true
			case "unbekannte Adresse":
				x.google.mu.Lock()
				x.google.challenge["c1"] = q.Get("code_challenge")
				x.google.claims = map[string]any{"nonce:c1": q.Get("nonce"), "email": "fremd@example.org", "sub": "sub-f"}
				x.google.mu.Unlock()
				return x.holeSprache(t, rueck+"&code=c1", c, sprache)
			case "Adresse unbestätigt":
				x.google.mu.Lock()
				x.google.challenge["c1"] = q.Get("code_challenge")
				x.google.claims = map[string]any{"nonce:c1": q.Get("nonce"), "email_verified": false}
				x.google.mu.Unlock()
				return x.holeSprache(t, rueck+"&code=c1", c, sprache)
			}
			x.google.mu.Lock()
			x.google.challenge["c1"] = q.Get("code_challenge")
			x.google.claims = map[string]any{"nonce:c1": q.Get("nonce")}
			x.google.mu.Unlock()
			return x.holeSprache(t, rueck+"&code=c1", c, sprache)
		}
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			h := hole(f.name)
			for _, sprache := range []string{"", "fr, de;q=0.5"} {
				x := neuerMehrAnbieterTest(t, "anna@example.com")
				rec := h(x, t, sprache)
				body := rec.Body.String()
				if !strings.Contains(body, "<h1>"+f.titelDe+"</h1>") || !strings.Contains(body, `<html lang="de">`) || rec.Header().Get("Content-Language") != "de" {
					t.Errorf("Accept-Language %q: erwartet deutsche Seite %q, Antwort %d: %s", sprache, f.titelDe, rec.Code, body)
				}
			}
			for _, sprache := range []string{"en-GB,en;q=0.9,de;q=0.5"} {
				x := neuerMehrAnbieterTest(t, "anna@example.com")
				rec := h(x, t, sprache)
				body := rec.Body.String()
				if !strings.Contains(body, "<h1>"+f.titelEn+"</h1>") || !strings.Contains(body, `<html lang="en">`) ||
					rec.Header().Get("Content-Language") != "en" || rec.Header().Get("Vary") != "Accept-Language" {
					t.Errorf("Accept-Language %q: erwartet englische Seite %q, Antwort %d: %s", sprache, f.titelEn, rec.Code, body)
				}
				if !strings.Contains(strings.ToLower(body), strings.ToLower(f.textEn)) {
					t.Errorf("Accept-Language %q: Text %q fehlt: %s", sprache, f.textEn, body)
				}
				if f.linkEn != "" && !strings.Contains(body, ">"+f.linkEn+"</a>") {
					t.Errorf("Accept-Language %q: Link %q fehlt: %s", sprache, f.linkEn, body)
				}
				for _, deutsch := range []string{"Neu anmelden", "Anmeldedienst", "Zugang"} {
					if strings.Contains(body, deutsch) {
						t.Errorf("Accept-Language %q: deutscher Rest %q: %s", sprache, deutsch, body)
					}
				}
			}
		})
	}
}
