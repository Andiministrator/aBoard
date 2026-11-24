package api

// Karte #608: Fehlversuch-Bremse am Agenten-Zugang.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// bremsTokens: zwei gültige Tokens neuen Formats (Projekt A und B).
type bremsTokens struct {
	gueltig map[string]*storage.AgentToken
}

func (b *bremsTokens) Lookup(_ context.Context, k string) (*storage.AgentToken, error) {
	if t, ok := b.gueltig[k]; ok {
		return t, nil
	}
	return nil, storage.ErrAgentTokenUnbekannt
}
func (b *bremsTokens) IstAktiv(context.Context, string) (bool, error) { return true, nil }
func (b *bremsTokens) KennungBekannt(_ context.Context, k string) (bool, error) {
	for kl := range b.gueltig {
		if storage.AgentTokenKennung(kl) == k {
			return true, nil
		}
	}
	return false, nil
}

const (
	tokenA  = "abt_aaaaaaaaaa_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tokenB  = "abt_bbbbbbbbbb_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	falschA = "abt_aaaaaaaaaa_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
)

func bremsZugang(t *testing.T, proxy string) (*AgentZugang, http.Handler, *time.Time) {
	t.Helper()
	z := NewAgentZugang(&bremsTokens{gueltig: map[string]*storage.AgentToken{
		tokenA: {ID: "ta", Name: "a", BoardID: "b1", Role: "editor"},
		tokenB: {ID: "tb", Name: "b", BoardID: "b1", Role: "editor"},
	}})
	if err := z.MitProxy(proxy); err != nil {
		t.Fatal(err)
	}
	jetzt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	z.bremse.jetzt = func() time.Time { return jetzt }
	h := z.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	return z, h, &jetzt
}

func bremsAnfrage(h http.Handler, remote, token, realIP string, extra ...string) int {
	r := httptest.NewRequest(http.MethodGet, "/boards/b1", nil)
	r.RemoteAddr = remote
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if realIP != "" {
		r.Header.Set("X-Real-IP", realIP)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		r.Header.Set(extra[i], extra[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestBremse_SperrtNachZehnFehlversuchenUndGibtFrei(t *testing.T) {
	_, h, jetzt := bremsZugang(t, "")
	for i := 0; i < MaxFehlversuche; i++ {
		if c := bremsAnfrage(h, "203.0.113.5:1000", "abt_quatsch", ""); c != http.StatusUnauthorized {
			t.Fatalf("Versuch %d: %d", i, c)
		}
	}
	if c := bremsAnfrage(h, "203.0.113.5:1000", "abt_quatsch", ""); c != http.StatusTooManyRequests {
		t.Fatalf("nach %d Fehlversuchen: %d", MaxFehlversuche, c)
	}
	// Ohne Token von dort: derselbe Adress-Schlüssel, gesperrt. (Dass ein
	// RICHTIGES Token unter einem gesperrten Schlüssel ebenfalls 429 bekommt,
	// prüft TestBremse_ProjektASperrtProjektBNicht.)
	if c := bremsAnfrage(h, "203.0.113.5:1000", "", ""); c != http.StatusTooManyRequests {
		t.Fatalf("ohne Token von gesperrter Adresse: %d", c)
	}
	// Andere Adresse unberührt.
	if c := bremsAnfrage(h, "198.51.100.7:1000", tokenA, ""); c != http.StatusOK {
		t.Fatalf("andere Adresse: %d", c)
	}
	// Fenster abgelaufen → wieder frei.
	*jetzt = jetzt.Add(FehlversuchFenster + time.Second)
	if c := bremsAnfrage(h, "203.0.113.5:1000", "abt_quatsch", ""); c != http.StatusUnauthorized {
		t.Fatalf("nach Fensterablauf: %d", c)
	}
}

// Betreiberwunsch: Hinter derselben Adresse (NAT, rootless Podman) sperrt ein
// falsches Token von Projekt A nicht Projekt B.
func TestBremse_ProjektASperrtProjektBNicht(t *testing.T) {
	_, h, _ := bremsZugang(t, "")
	for i := 0; i <= MaxFehlversuche; i++ {
		bremsAnfrage(h, "10.0.0.2:1000", falschA, "")
	}
	if c := bremsAnfrage(h, "10.0.0.2:1000", tokenA, ""); c != http.StatusTooManyRequests {
		t.Fatalf("A selbst: %d (erwartet 429)", c)
	}
	if c := bremsAnfrage(h, "10.0.0.2:1000", tokenB, ""); c != http.StatusOK {
		t.Fatalf("B hinter derselben Adresse: %d", c)
	}
}

// Erfundene Kennungen fallen auf die Adresse zurück — sonst umginge man die
// Bremse mit einer neuen Kennung je Versuch.
func TestBremse_ErfundeneKennungZaehltAufDieAdresse(t *testing.T) {
	_, h, _ := bremsZugang(t, "")
	for i := 0; i < MaxFehlversuche; i++ {
		erfunden := "abt_" + strings.Repeat(string(rune('c'+i%20)), 10) + "_" + strings.Repeat("Z", 43)
		bremsAnfrage(h, "203.0.113.9:1", erfunden, "")
	}
	if c := bremsAnfrage(h, "203.0.113.9:1", "abt_zzzzzzzzzz_"+strings.Repeat("Q", 43), ""); c != http.StatusTooManyRequests {
		t.Fatalf("rotierende Kennungen: %d", c)
	}
}

// Proxy: X-Real-IP zählt nur von der Proxy-Gegenstelle; X-Forwarded-For nie.
func TestBremse_ClientAdresseNurVomProxy(t *testing.T) {
	z, h, _ := bremsZugang(t, "10.9.0.0/16")
	p := z.bremse.proxy
	direkt := httptest.NewRequest(http.MethodGet, "/", nil)
	direkt.RemoteAddr = "203.0.113.5:4000"
	direkt.Header.Set("X-Real-IP", "1.2.3.4")
	direkt.Header.Set("X-Forwarded-For", "5.6.7.8")
	if a := p.clientAdresse(direkt); a != "203.0.113.5" {
		t.Fatalf("gefälschter Header von direkter Gegenstelle: %q", a)
	}
	vomProxy := httptest.NewRequest(http.MethodGet, "/", nil)
	vomProxy.RemoteAddr = "10.9.1.2:4000"
	vomProxy.Header.Set("X-Real-IP", "1.2.3.4")
	vomProxy.Header.Set("X-Forwarded-For", "5.6.7.8")
	if a := p.clientAdresse(vomProxy); a != "1.2.3.4" {
		t.Fatalf("vom Proxy: %q", a)
	}
	vomProxy.Header.Del("X-Real-IP")
	if a := p.clientAdresse(vomProxy); a != "10.9.1.2" {
		t.Fatalf("Proxy ohne X-Real-IP (XFF darf nicht zählen): %q", a)
	}
	// Ende-zu-Ende: Zehn Fehlversuche „von“ 1.2.3.4 über den Proxy sperren
	// 1.2.3.4, aber nicht 1.2.3.5 hinter demselben Proxy.
	for i := 0; i < MaxFehlversuche; i++ {
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "1.2.3.4")
	}
	if c := bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "1.2.3.4"); c != http.StatusTooManyRequests {
		t.Fatalf("über Proxy gesperrt: %d", c)
	}
	if c := bremsAnfrage(h, "10.9.1.2:4000", tokenA, "1.2.3.5"); c != http.StatusOK {
		t.Fatalf("anderer Client hinter dem Proxy: %d", c)
	}
}

// Proxy: Der Proxy als DNS-Name — nach einem Adresswechsel (Container neu
// erzeugt) wird X-Real-IP weiter ausgewertet, sobald neu aufgelöst ist.
func TestBremse_ProxyAlsNameUeberlebtAdresswechsel(t *testing.T) {
	z, _, _ := bremsZugang(t, "proxy")
	p := z.bremse.proxy
	var mu sync.Mutex
	adresse := "10.0.5.1"
	fehlschlag := false
	p.aufloeser = func(_ context.Context, host string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if host != "proxy" {
			t.Errorf("aufgelöst: %q", host)
		}
		if fehlschlag {
			return nil, errors.New("dns weg")
		}
		return []string{adresse}, nil
	}
	uhr := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	p.jetzt = func() time.Time { return uhr }
	anfrage := func(remote string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote + ":1"
		r.Header.Set("X-Real-IP", "1.2.3.4")
		return p.clientAdresse(r)
	}
	if a := anfrage("10.0.5.1"); a != "1.2.3.4" {
		t.Fatalf("vor dem Wechsel: %q", a)
	}
	mu.Lock()
	adresse = "10.0.7.9"
	mu.Unlock()
	uhr = uhr.Add(proxyNeuAufloesen + time.Second)
	if a := anfrage("10.0.7.9"); a != "1.2.3.4" {
		t.Fatalf("nach dem Wechsel: %q (fiel auf die TCP-Adresse zurück)", a)
	}
	if a := anfrage("10.0.5.1"); a != "10.0.5.1" {
		t.Fatalf("alte Adresse ist nicht mehr der Proxy: %q", a)
	}
	// DNS-Aussetzer: letzter Stand bleibt.
	mu.Lock()
	fehlschlag = true
	mu.Unlock()
	uhr = uhr.Add(proxyNeuAufloesen + time.Second)
	if a := anfrage("10.0.7.9"); a != "1.2.3.4" {
		t.Fatalf("bei DNS-Aussetzer: %q", a)
	}
}

func TestBremse_ProxyKonfiguration(t *testing.T) {
	p, err := parseProxy(" proxy , 10.0.0.0/8, 192.0.2.1, unix ")
	if err != nil || !p.unix || len(p.netze) != 2 || len(p.namen) != 1 {
		t.Fatalf("parse: %+v %v", p, err)
	}
	if _, err := parseProxy("10.0.0.0/99"); err == nil {
		t.Fatal("kaputtes Netz akzeptiert")
	}
	leer, _ := parseProxy("")
	if leer.aktiv() {
		t.Fatal("leer = kein Proxy")
	}
	// Unix-Socket-Gegenstelle nur mit `unix`.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "@"
	r.Header.Set("X-Real-IP", "1.2.3.4")
	if a := p.clientAdresse(r); a != "1.2.3.4" {
		t.Fatalf("unix mit Vertrauen: %q", a)
	}
	ohne, _ := parseProxy("10.0.0.0/8")
	if a := ohne.clientAdresse(r); a == "1.2.3.4" {
		t.Fatalf("unix ohne Vertrauen nahm X-Real-IP: %q", a)
	}
}

// Session-Kritiker Sprint 78, K-3: Volle Tabelle — ein neuer Schlüssel wird
// trotzdem gezählt (ältester Eintrag weicht), die Tabelle bleibt begrenzt.
func TestBremse_VolleTabelleZaehltWeiter(t *testing.T) {
	_, h, jetzt := bremsZugang(t, "10.9.0.0/16")
	for i := 0; i < maxBremsEintraege; i++ {
		*jetzt = jetzt.Add(time.Millisecond)
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", fmt.Sprintf("198.18.%d.%d", i/250, i%250+1))
	}
	for i := 0; i < MaxFehlversuche; i++ {
		*jetzt = jetzt.Add(time.Millisecond)
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "203.0.113.77")
	}
	if c := bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "203.0.113.77"); c != http.StatusTooManyRequests {
		t.Fatalf("neue Adresse bei voller Tabelle: %d (erwartet 429)", c)
	}
}

func TestBremse_VolleTabelleBleibtBegrenzt(t *testing.T) {
	z, h, jetzt := bremsZugang(t, "10.9.0.0/16")
	for i := 0; i < maxBremsEintraege+50; i++ {
		*jetzt = jetzt.Add(time.Millisecond)
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", fmt.Sprintf("198.19.%d.%d", i/250, i%250+1))
	}
	if n := len(z.bremse.eintrag); n > maxBremsEintraege {
		t.Fatalf("Tabelle gewachsen: %d", n)
	}
}

// IPv6: ein /64 zählt als eine Adresse.
func TestBremse_IPv6ZaehltJe64(t *testing.T) {
	_, h, _ := bremsZugang(t, "")
	for i := 0; i < MaxFehlversuche; i++ {
		bremsAnfrage(h, fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1), "abt_quatsch", "")
	}
	if c := bremsAnfrage(h, "[2001:db8:1:2::ffff]:4000", "abt_quatsch", ""); c != http.StatusTooManyRequests {
		t.Fatalf("anderes Interface im selben /64: %d", c)
	}
	if c := bremsAnfrage(h, "[2001:db8:1:3::1]:4000", "abt_quatsch", ""); c != http.StatusUnauthorized {
		t.Fatalf("anderes /64: %d", c)
	}
}

// K-9: Eine eingetretene Sperre steht genau einmal im Log, ohne Token.
func TestBremse_SperreImLogEinmal(t *testing.T) {
	var zeilen []string
	alt := bremsLog
	bremsLog = func(f string, a ...any) { zeilen = append(zeilen, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { bremsLog = alt })
	_, h, _ := bremsZugang(t, "")
	for i := 0; i < MaxFehlversuche+5; i++ {
		bremsAnfrage(h, "10.0.0.2:1000", falschA, "")
	}
	if len(zeilen) != 1 || !strings.Contains(zeilen[0], "aaaaaaaaaa") || strings.Contains(zeilen[0], "XXXX") {
		t.Fatalf("Log: %q", zeilen)
	}
}

// K-8: Während eine Neuauflösung läuft, warten andere nicht — sie bekommen
// den letzten Stand.
func TestBremse_DNSOhneSperre(t *testing.T) {
	z, _, _ := bremsZugang(t, "proxy")
	p := z.bremse.proxy
	uhr := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	p.jetzt = func() time.Time { return uhr }
	haelt := make(chan struct{})
	gestartet := make(chan struct{}, 1)
	erster := true
	var mu sync.Mutex
	p.aufloeser = func(context.Context, string) ([]string, error) {
		mu.Lock()
		e := erster
		erster = false
		mu.Unlock()
		if !e {
			gestartet <- struct{}{}
			<-haelt
		}
		return []string{"10.0.5.1"}, nil
	}
	if !p.namensAdressen(context.Background())["10.0.5.1"] {
		t.Fatal("erste Auflösung")
	}
	uhr = uhr.Add(proxyNeuAufloesen + time.Second)
	go p.namensAdressen(context.Background())
	<-gestartet
	fertig := make(chan map[string]bool, 1)
	go func() { fertig <- p.namensAdressen(context.Background()) }()
	select {
	case m := <-fertig:
		if !m["10.0.5.1"] {
			t.Fatalf("letzter Stand fehlt: %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("zweiter Aufruf wartete auf die laufende Auflösung")
	}
	close(haelt)
}

// Verify Sprint 78, V-3: Wer die Tabelle mit frischen Adressen füllt,
// verdrängt nicht seine eigene Sperre.
func TestBremse_VerdraengungSchontGesperrte(t *testing.T) {
	_, h, jetzt := bremsZugang(t, "10.9.0.0/16")
	for i := 0; i < MaxFehlversuche; i++ {
		*jetzt = jetzt.Add(time.Millisecond)
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "203.0.113.66")
	}
	for i := 0; i < maxBremsEintraege+10; i++ {
		*jetzt = jetzt.Add(time.Millisecond)
		bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", fmt.Sprintf("198.18.%d.%d", i/250, i%250+1))
	}
	if c := bremsAnfrage(h, "10.9.1.2:4000", "abt_quatsch", "203.0.113.66"); c != http.StatusTooManyRequests {
		t.Fatalf("gesperrte Adresse nach Auffüllen: %d (erwartet weiter 429)", c)
	}
}

// Verify Sprint 78, V-4: Ein abgebrochener Request-Kontext darf die
// Proxy-Auflösung nicht scheitern lassen; ein Panic lässt `loest` nicht hängen.
func TestBremse_DNSOhneRequestKontextUndNachPanic(t *testing.T) {
	z, _, _ := bremsZugang(t, "proxy")
	p := z.bremse.proxy
	p.aufloeser = func(c context.Context, _ string) ([]string, error) {
		if c.Err() != nil {
			return nil, c.Err()
		}
		return []string{"10.0.5.1"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !p.namensAdressen(ctx)["10.0.5.1"] {
		t.Fatal("abgebrochener Kontext verhinderte die Auflösung")
	}
	uhr := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	p.jetzt = func() time.Time { return uhr }
	p.stand = uhr.Add(-2 * proxyNeuAufloesen)
	p.aufloeser = func(context.Context, string) ([]string, error) { panic("kaputt") }
	func() {
		defer func() { _ = recover() }()
		p.namensAdressen(context.Background())
	}()
	p.mu.Lock()
	haengt := p.loest
	p.mu.Unlock()
	if haengt {
		t.Fatal("loest blieb nach Panic gesetzt")
	}
}
