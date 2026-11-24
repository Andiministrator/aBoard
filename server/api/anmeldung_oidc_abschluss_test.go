package api

// Tests zur Nacharbeit des Sprint-Abschlusses 106–108 (Kritiker K2):
// F-B Abmelden ohne stilles Wiederanmelden, F-C Sitzungsfixierung,
// F-D azp-Prüfung.

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// meldeMitAlterSitzung ruft den Rückruf wie melde, schickt aber zusätzlich
// einen Sitzungs-Cookie mit.
func (x *anmeldeTest) meldeMitAlterSitzung(t *testing.T, c *http.Cookie, q url.Values, code, alt string) *httptest.ResponseRecorder {
	t.Helper()
	x.anb.mu.Lock()
	x.anb.challenge[code] = q.Get("code_challenge")
	if x.anb.claims == nil {
		x.anb.claims = map[string]any{}
	}
	x.anb.claims["nonce:"+code] = q.Get("nonce")
	x.anb.mu.Unlock()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungRueckrufPfad+"?code="+url.QueryEscape(code)+
		"&state="+url.QueryEscape(q.Get("state")), nil)
	r.AddCookie(c)
	r.AddCookie(&http.Cookie{Name: SitzungsCookie, Value: alt})
	rec := httptest.NewRecorder()
	x.anm.Rueckruf(rec, r)
	return rec
}

func TestAnmeldungAbschluss_RueckrufBeendetAlteSitzung(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	ctx := context.Background()
	alt, _, err := x.sitzungen.Anlegen(ctx, "anna@example.com", "Anna A.")
	if err != nil {
		t.Fatal(err)
	}
	c, q := x.starte(t)
	rec := x.meldeMitAlterSitzung(t, c, q, "code-f", alt)
	neu := sitzungsCookie(rec)
	if rec.Code != http.StatusFound || neu == nil {
		t.Fatalf("Rückruf: %d", rec.Code)
	}
	if neu.Value == alt {
		t.Fatal("Rückruf hat die mitgebrachte Sitzungs-ID übernommen")
	}
	if s, err := x.sitzungen.Pruefen(ctx, alt); err == nil && s != nil {
		t.Fatal("alte Sitzung lebt nach der neuen Anmeldung weiter (Sitzungsfixierung)")
	}
	if s, err := x.sitzungen.Pruefen(ctx, neu.Value); err != nil || s == nil {
		t.Fatalf("neue Sitzung ungültig: %v", err)
	}
}

func TestAnmeldungAbschluss_AzpWirdGeprueft(t *testing.T) {
	faelle := map[string]struct {
		claims map[string]any
		ok     bool
	}{
		"mehrere aud ohne azp":     {map[string]any{"aud": []string{"client-1", "fremd"}}, false},
		"mehrere aud, azp fremd":   {map[string]any{"aud": []string{"client-1", "fremd"}, "azp": "fremd"}, false},
		"ein aud, azp fremd":       {map[string]any{"azp": "fremd"}, false},
		"mehrere aud, azp unser":   {map[string]any{"aud": []string{"client-1", "fremd"}, "azp": "client-1"}, true},
		"ein aud, azp unser":       {map[string]any{"azp": "client-1"}, true},
		"ein aud, ohne azp (Std.)": {map[string]any{}, true},
	}
	for name, f := range faelle {
		x := neuerAnmeldeTest(t, "anna@example.com")
		c, q := x.starte(t)
		x.anb.claims = f.claims
		rec := x.melde(t, c, q, "code-azp")
		hatSitzung := sitzungsCookie(rec) != nil
		if f.ok && (rec.Code != http.StatusFound || !hatSitzung) {
			t.Errorf("%s: %d Sitzung=%v — erwartet Anmeldung", name, rec.Code, hatSitzung)
		}
		if !f.ok && (rec.Code != http.StatusForbidden || hatSitzung) {
			t.Errorf("%s: %d Sitzung=%v — erwartet 403 ohne Sitzung", name, rec.Code, hatSitzung)
		}
	}
}

func TestAnmeldungAbschluss_KontoWahlNurAufWunsch(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	if _, q := x.starte(t); q.Has("prompt") {
		t.Fatalf("normaler Start setzt prompt=%q", q.Get("prompt"))
	}
	rec := httptest.NewRecorder()
	x.anm.Start(rec, httptest.NewRequest(http.MethodGet,
		"https://board.example"+AnmeldungStartPfad+"?"+AnmeldungKontoWahlParam+"=1", nil))
	ziel, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || rec.Code != http.StatusFound {
		t.Fatalf("Start mit Kontowahl: %d %v", rec.Code, err)
	}
	if ziel.Query().Get("prompt") != "select_account" {
		t.Fatalf("Start mit Kontowahl: prompt=%q, erwartet select_account", ziel.Query().Get("prompt"))
	}
}

// Verify N-3: die Drossel der Abweis-Zeilen (auch F-2859, sub-Abweichung)
// lässt in einer Minute nur eine Zeile durch.
func TestAnmeldungAbschluss_AbweisDrossel(t *testing.T) {
	abweisLogZuletzt.Store(0)
	t.Cleanup(func() { abweisLogZuletzt.Store(0) })
	t0 := time.Unix(1_800_000_000, 0)
	if !abweisLogErlaubt(t0) {
		t.Fatal("erste Zeile gedrosselt")
	}
	if abweisLogErlaubt(t0.Add(30 * time.Second)) {
		t.Fatal("zweite Zeile innerhalb einer Minute nicht gedrosselt")
	}
	if !abweisLogErlaubt(t0.Add(61 * time.Second)) {
		t.Fatal("Zeile nach einer Minute weiter gedrosselt")
	}
}

func TestAnmeldungAbschluss_AbgemeldetSeite(t *testing.T) {
	x := neuerAnmeldeTest(t, "anna@example.com")
	rec := httptest.NewRecorder()
	x.anm.Abgemeldet(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+AnmeldungAbgemeldetPfad, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("Abgemeldet: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("Abgemeldet leitet weiter (%q) — muss stehen bleiben, sonst meldet der Anbieter still wieder an", loc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="`+AnmeldungSeitePfad+"?"+AnmeldungKontoWahlParam+`=1"`) {
		t.Fatalf("Link zur Kontowahl fehlt: %s", body)
	}
	// Ohne Sitzung erreichbar, nur per GET/HEAD.
	if !anmeldungOhneSitzung(httptest.NewRequest(http.MethodGet, AnmeldungAbgemeldetPfad, nil)) {
		t.Fatal("/anmeldung/abgemeldet verlangt eine Sitzung")
	}
	if anmeldungOhneSitzung(httptest.NewRequest(http.MethodPost, AnmeldungAbgemeldetPfad, nil)) {
		t.Fatal("POST /anmeldung/abgemeldet ohne Sitzung erlaubt")
	}
}

// Sitzungs-Kritik A (F-2867): Die Warnung „anderes sub“ hat einen eigenen
// Drossel-Zähler — eine vorangehende Nicht-zugelassen-Abweisung verdrängt sie
// nicht, und eine zweite sub-Abweisung in derselben Minute schreibt keine
// zweite Zeile. Geprüft an der Aufrufstelle über das Log.
func TestAnmeldungAbschluss_SubWarnungEigeneDrossel(t *testing.T) {
	abweisLogZuletzt.Store(0)
	bindungLogZuletzt.Store(0)
	t.Cleanup(func() { abweisLogZuletzt.Store(0); bindungLogZuletzt.Store(0) })
	var puffer bytes.Buffer
	altAus, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&puffer)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(altAus); log.SetFlags(altFlags) })

	x := neuerAnmeldeTest(t, "anna@example.com")
	c, q := x.starte(t)
	if rec := x.melde(t, c, q, "s1"); rec.Code != http.StatusFound {
		t.Fatalf("erste Anmeldung: %d", rec.Code)
	}
	// Nicht zugelassene Adresse verbraucht die Abweis-Drossel.
	c, q = x.starte(t)
	// Eigenes Anbieter-Konto: mit Annas sub gälte seit ADR-0149 §4 die Bindung
	// (Anna), gleich welche Adresse gemeldet wird.
	x.anb.claims = map[string]any{"email": "fremd@example.org", "sub": "sub-fremd"}
	if rec := x.melde(t, c, q, "s2"); rec.Code != http.StatusForbidden {
		t.Fatalf("nicht zugelassen: %d", rec.Code)
	}
	for i, code := range []string{"s3", "s4"} {
		c, q = x.starte(t)
		x.anb.claims = map[string]any{"sub": "sub-jemand-anders"}
		if rec := x.melde(t, c, q, code); rec.Code != http.StatusForbidden {
			t.Fatalf("anderes sub %d: %d", i, rec.Code)
		}
	}
	if n := strings.Count(puffer.String(), "anderen Konto (sub)"); n != 1 {
		t.Fatalf("sub-Warnung %d-mal im Log, erwartet genau 1:\n%s", n, puffer.String())
	}
}
