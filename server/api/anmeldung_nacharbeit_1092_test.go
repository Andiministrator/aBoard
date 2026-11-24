package api

// Nacharbeit Sitzungs-Kritik 03.11. (Karte #1092): parallele Anfragen
// (F-2888), bekannte Anschlüsse (F-2889), Texte (F-2893). Testdaten nur
// example.org.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Ein Schub paralleler Fehlversuche von einem Anschluss rechnet höchstens so
// oft, wie die Bremse der Gegenstelle erlaubt — nicht so oft, wie Anfragen
// eintreffen.
func TestNacharbeit1092_ParallelePasswoerter(t *testing.T) {
	pt := neuerPasswortTest(t)
	var gerechnet atomic.Int32
	pt.p.pruefen = func(context.Context, string, string) (bool, error) {
		gerechnet.Add(1)
		time.Sleep(5 * time.Millisecond)
		return false, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pt.post(fmt.Sprintf("x%d@example.org", i), "falsch-falsch-falsch", "192.0.2.50")
		}(i)
	}
	wg.Wait()
	if n := gerechnet.Load(); n > PasswortMaxFehlversuche {
		t.Fatalf("%d Prüfungen gerechnet, erlaubt %d", n, PasswortMaxFehlversuche)
	}
	// Dieselbe Adresse von vielen Anschlüssen: höchstens eine Prüfung, dann Wartezeit.
	pt2 := neuerPasswortTest(t)
	var gerechnet2 atomic.Int32
	pt2.p.pruefen = func(context.Context, string, string) (bool, error) {
		gerechnet2.Add(1)
		time.Sleep(5 * time.Millisecond)
		return false, nil
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pt2.post("anna@example.org", "falsch-falsch-falsch", fmt.Sprintf("198.51.100.%d", i+1))
		}(i)
	}
	wg.Wait()
	if n := gerechnet2.Load(); n != 1 {
		t.Fatalf("dieselbe Adresse: %d Prüfungen, erwartet 1", n)
	}
}

type zaehlTOTP struct {
	*fakeTOTP
	mu       sync.Mutex
	geprueft int
}

func (z *zaehlTOTP) CodeVerbrauchen(ctx context.Context, id, h string, j int64) (bool, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.geprueft++
	return z.fakeTOTP.CodeVerbrauchen(ctx, id, h, j)
}

func (z *zaehlTOTP) TOTPGeheimnis(ctx context.Context, id string) (string, int64, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.geprueft++
	return z.fakeTOTP.TOTPGeheimnis(ctx, id)
}

func (z *zaehlTOTP) TOTPSchrittVerbrauchen(ctx context.Context, id string, s int64) (bool, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.fakeTOTP.TOTPSchrittVerbrauchen(ctx, id, s)
}

// Ein Zwischen-Cookie prüft auch bei hundert parallelen Anfragen höchstens
// fünf Codes, egal von wie vielen Anschlüssen.
func TestNacharbeit1092_ParalleleCodes(t *testing.T) {
	ct := neuerCodeTest(t)
	z := &zaehlTOTP{fakeTOTP: ct.totp}
	ct.p.totp = z
	c := ct.passwort(t)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ct.code(c, fmt.Sprintf("%06d", i), fmt.Sprintf("203.0.113.%d", i+1))
		}(i)
	}
	wg.Wait()
	z.mu.Lock()
	n := z.geprueft
	z.mu.Unlock()
	if n > zweiterSchrittMaxFehler {
		t.Fatalf("%d Codes geprüft, erlaubt %d", n, zweiterSchrittMaxFehler)
	}
	if len(ct.sitzungen.angelegt) != 0 {
		t.Fatal("Sitzung durch Raten")
	}
}

// F-2889: Fremde Fehlversuche sperren die Person an ihrem bekannten Anschluss
// nicht aus; an einem neuen Anschluss gilt die Wartezeit weiter.
func TestNacharbeit1092_BekannterAnschluss(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwortVon(t, "192.0.2.10")
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.10"); rec.Code != http.StatusSeeOther {
		t.Fatalf("erste Anmeldung: %d", rec.Code)
	}
	// Ein Fremder probiert von vier Anschlüssen.
	for i := 0; i < 4; i++ {
		ct.post("anna@example.org", "falsch-falsch-falsch", fmt.Sprintf("198.51.100.%d", 20+i))
	}
	// Anna am bekannten Anschluss: keine Wartezeit (nach vier Fehlversuchen
	// wären es sonst 8 s).
	if rec := ct.post("anna@example.org", richtig, "192.0.2.10"); rec.Code != http.StatusOK {
		t.Fatalf("bekannter Anschluss wartet: %d %s", rec.Code, rec.Body.String())
	}
	// Ein neuer Anschluss wartet weiter.
	if rec := ct.post("anna@example.org", richtig, "192.0.2.11"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("neuer Anschluss: %d", rec.Code)
	}
	// Nach PasswortBekanntDauer ist der Anschluss nicht mehr bekannt.
	ct.post("anna@example.org", "falsch-falsch-falsch", "198.51.100.30")
	ct.uhr.t = ct.uhr.t.Add(PasswortBekanntDauer)
	ct.post("anna@example.org", "falsch-falsch-falsch", "198.51.100.31")
	if w := ct.p.adressen.warten("anna@example.org", "192.0.2.10"); w == 0 {
		t.Fatal("bekannt über die Dauer hinaus")
	}
}

// F-2888: je Konto höchstens ein offener zweiter Schritt; ein neuer ersetzt
// den alten.
func TestNacharbeit1092_EinZweiterSchrittJeKonto(t *testing.T) {
	ct := neuerCodeTest(t)
	alt := ct.passwort(t)
	ct.uhr.t = ct.uhr.t.Add(time.Second)
	neu := ct.passwort(t)
	if len(ct.p.zweiter.offen) != 1 {
		t.Fatalf("%d offene Schritte", len(ct.p.zweiter.offen))
	}
	if rec := ct.code(alt, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("alter Schritt gilt: %d", rec.Code)
	}
	if rec := ct.code(neu, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusSeeOther {
		t.Fatalf("neuer Schritt: %d", rec.Code)
	}
	if len(ct.p.zweiter.offen) != 0 || len(ct.p.zweiter.jeKonto) != 0 {
		t.Fatalf("Reste: %d %d", len(ct.p.zweiter.offen), len(ct.p.zweiter.jeKonto))
	}
}

// F-2888 (K-5): Der Schlüssel der Wartezeit ist ein Hash fester Länge.
func TestNacharbeit1092_SchluesselFesteLaenge(t *testing.T) {
	pt := neuerPasswortTest(t)
	pt.post(strings.Repeat("a", 10000)+"@example.org", "falsch-falsch-falsch", "192.0.2.1")
	for k := range pt.p.adressen.eintrag {
		if len(k) != 64 {
			t.Fatalf("Schlüssellänge %d", len(k))
		}
	}
}

// F-2893: Zustandsseiten folgen Accept-Language.
func TestNacharbeit1092_TexteEnglisch(t *testing.T) {
	pt := neuerPasswortTest(t)
	pt.p.NachPasswort = nil
	rec := pt.post("anna@example.org", richtig, "192.0.2.1", "Accept-Language", "en")
	if !strings.Contains(rec.Body.String(), "Two-factor sign-in missing") || strings.Contains(rec.Body.String(), "Zwei-Faktor") {
		t.Fatalf("englisch: %s", rec.Body.String())
	}
	pt = neuerPasswortTest(t)
	rec = pt.post("ben@example.org", richtig, "192.0.2.1", "Accept-Language", "en")
	if !strings.Contains(rec.Body.String(), "Waiting for confirmation") {
		t.Fatalf("englisch wartet: %s", rec.Body.String())
	}
}
