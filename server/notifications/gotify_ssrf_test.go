package notifications

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/storage"
)

// F-2668: Der Standard-Client (client nil, so verdrahtet main.go) verbindet
// nicht zu internen Adressen und verrät dem Nutzer weder Fehlertext noch Body.
// Das Ziel ist ein TLS-Server auf 127.0.0.1 im Test; ohne den Schutz käme die
// Anfrage dort an (treffer > 0) — mit Schutz wird schon der Verbindungsaufbau
// abgelehnt. Kein Test verlässt den Rechner.
func TestGotify_InternesZielWirdAbgelehnt(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	var treffer atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		treffer.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "GEHEIMER-BODY")
	}))
	defer srv.Close()

	// Basis-Transport kennt das Test-Zertifikat: Ohne den Schutz käme die
	// Anfrage an (sonst scheiterte sie schon am Zertifikat, und der Test
	// trennte nicht).
	basis := srv.Client().Transport.(*http.Transport).Clone()
	basis.Proxy = nil
	g := NewGotifySender(nil, nil)
	g.basis = basis
	// Seit F-2689 lehnt schon ValidateGotifyURL das IP-Literal ab; dieser
	// Test misst den Dialer, deshalb direkt senden.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/message", strings.NewReader("{}"))
	err := g.senden(req, srv.URL)
	if err == nil {
		t.Fatal("interne Adresse 127.0.0.1 wurde nicht abgelehnt")
	}
	if treffer.Load() != 0 {
		t.Fatalf("%d Anfrage(n) am internen Ziel angekommen", treffer.Load())
	}
	if !errors.Is(err, ErrGotifyNichtErreichbar) || err.Error() != "nicht erreichbar" {
		t.Fatalf("Nutzerfehler = %q, erwartet exakt \"nicht erreichbar\"", err.Error())
	}
	for _, verboten := range []string{"127.0.0.1", "refused", "intern", "GEHEIMER-BODY", "dial"} {
		if strings.Contains(err.Error(), verboten) {
			t.Errorf("Fehlertext verrät %q: %q", verboten, err.Error())
		}
	}

	// SendOne läuft über denselben Pfad.
	g2 := NewGotifySender(festeGotifyConfig{cfg: storage.GotifyConfig{URL: srv.URL, Token: "t"}}, nil)
	g2.basis = basis
	if err := g2.SendOne(context.Background(), "u", "", "", Event{Type: EventBackupFailed}); err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("SendOne: %v", err)
	}
	if treffer.Load() != 0 {
		t.Fatalf("SendOne: %d Anfrage(n) am internen Ziel", treffer.Load())
	}
}

type festeGotifyConfig struct{ cfg storage.GotifyConfig }

func (f festeGotifyConfig) GetGotify(context.Context, string) (*storage.GotifyConfig, error) {
	return &f.cfg, nil
}

// F-2668: Ein Redirect der Gegenstelle wird nicht verfolgt. Die Gegenstelle
// ist hier ein Proxy (erlaubt VersandTransport genau seine Adresse), der auf
// einen internen Server umleitet; der darf nie getroffen werden.
func TestGotify_RedirectWirdNichtVerfolgt(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	var intern atomic.Int64
	const internHost = "intern.example.org"
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Hostname() == internHost {
			intern.Add(1)
			_, _ = io.WriteString(w, "INTERN")
			return
		}
		http.Redirect(w, r, "http://"+internHost+"/message", http.StatusTemporaryRedirect)
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	basis := http.DefaultTransport.(*http.Transport).Clone()
	basis.Proxy = http.ProxyURL(pu)

	g := NewGotifySender(nil, nil)
	g.basis = basis
	// senden direkt: ValidateGotifyURL (nur https) ist nicht Teil dieses Tests.
	ziel := "http://gotify.example.org"
	req, _ := http.NewRequest(http.MethodPost, ziel+"/message", strings.NewReader("{}"))
	err := g.senden(req, ziel)
	if err == nil || err.Error() != "nicht erreichbar" {
		t.Fatalf("Redirect: Fehler = %v, erwartet \"nicht erreichbar\"", err)
	}
	if intern.Load() != 0 {
		t.Fatalf("Redirect wurde verfolgt: %d Treffer am internen Ziel", intern.Load())
	}
}

// F-2668: Bei Status außerhalb 2xx steht nur der Statuscode im Fehler, nie
// Body-Bytes. Injizierter Client, damit das Ziel auf 127.0.0.1 erreichbar ist.
func TestGotify_StatusfehlerOhneBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "GEHEIMER-BODY")
	}))
	defer srv.Close()
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))
	g := NewGotifySender(nil, srv.Client())
	err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: srv.URL, Token: "t"})
	if err == nil || err.Error() != "status 403" {
		t.Fatalf("TestConnection: %v, erwartet \"status 403\"", err)
	}
	g2 := NewGotifySender(festeGotifyConfig{cfg: storage.GotifyConfig{URL: srv.URL, Token: "t"}}, srv.Client())
	err = g2.SendOne(context.Background(), "u", "", "", Event{Type: EventBackupFailed})
	if err == nil || strings.Contains(err.Error(), "GEHEIMER") || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("SendOne: %v", err)
	}
}

// F-2668: Der Ausgangs-Schalter greift weiter vor dem VersandTransport; der
// Nutzer sieht keinen Zielnamen.
func TestGotify_AusgangsSchalterMitVersandTransport(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	g := NewGotifySender(nil, nil)
	err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: "https://gotify.example.invalid", Token: "t"})
	if !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("Schalter umgangen: %v", err)
	}
	if strings.Contains(err.Error(), "gotify.example.invalid") {
		t.Errorf("Fehlertext nennt das Ziel: %q", err.Error())
	}
}

// G-4: Das Server-Log nennt bei einem Verbindungsfehler Host und Ursache,
// aber nie die Query der Nutzer-URL (dort kann ein Token stehen).
func TestGotify_LogOhneQuery(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	var puffer bytes.Buffer
	alt := log.Writer()
	log.SetOutput(&puffer)
	defer log.SetOutput(alt)
	g := NewGotifySender(nil, nil)
	ziel := "https://127.0.0.1:1/?token=GEHEIMQUERY"
	req, _ := http.NewRequest(http.MethodPost, ziel, strings.NewReader("{}"))
	_ = g.senden(req, ziel)
	text := puffer.String()
	if !strings.Contains(text, "gotify: Post 127.0.0.1:1:") {
		t.Fatalf("Log nennt Methode und Host nicht: %q", text)
	}
	if strings.Contains(text, "GEHEIMQUERY") {
		t.Fatalf("Log enthält die Query der URL: %q", text)
	}
}

// gotifyTLSZiel startet einen TLS-Server auf 127.0.0.1 und einen Basis-
// Transport, der sein Zertifikat kennt (ohne Proxy). Der Server zählt Treffer.
func gotifyTLSZiel(t *testing.T, status int) (srv *httptest.Server, basis *http.Transport, treffer *atomic.Int64) {
	t.Helper()
	treffer = &atomic.Int64{}
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		treffer.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	basis = srv.Client().Transport.(*http.Transport).Clone()
	basis.Proxy = nil
	return srv, basis, treffer
}

// F-2689 (G-1): Steht der Host wörtlich auf der Admin-Liste, erreicht der
// Versand das interne Ziel (Treffer >= 1); nicht auf der Liste: Treffer 0.
func TestGotify_InternerHostAufListeErreichbar(t *testing.T) {
	srv, basis, treffer := gotifyTLSZiel(t, http.StatusOK)

	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))
	g := NewGotifySender(nil, nil)
	g.basis = basis
	if err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: srv.URL, Token: "t"}); err != nil {
		t.Fatalf("freigegebener interner Host: %v", err)
	}
	if treffer.Load() < 1 {
		t.Fatalf("freigegebener Host: %d Treffer, erwartet >= 1", treffer.Load())
	}

	// Anderer Eintrag auf der Liste: 127.0.0.1 bleibt gesperrt.
	treffer.Store(0)
	ausgang.Setze(ausgang.Neu(false).MitGotifyIntern("gotify.internal"))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/message", strings.NewReader("{}"))
	err := g.senden(req, srv.URL)
	if err == nil || err.Error() != "nicht erreichbar" {
		t.Fatalf("nicht freigegeben: Fehler = %v", err)
	}
	if treffer.Load() != 0 {
		t.Fatalf("nicht freigegeben: %d Treffer", treffer.Load())
	}
}

// F-2689: Auch ein freigegebener Host darf nicht umleiten; die Umleitung
// würde auf ein internes Ziel führen und wird nicht verfolgt.
func TestGotify_FreigegebenerHostUmleitungVerboten(t *testing.T) {
	var ziel atomic.Int64
	zielSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ziel.Add(1) }))
	defer zielSrv.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, zielSrv.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	basis := srv.Client().Transport.(*http.Transport).Clone()
	basis.Proxy = nil
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))
	g := NewGotifySender(nil, nil)
	g.basis = basis
	err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: srv.URL, Token: "t"})
	if err == nil || err.Error() != "nicht erreichbar" {
		t.Fatalf("Umleitung: Fehler = %v", err)
	}
	if ziel.Load() != 0 {
		t.Fatalf("Umleitung verfolgt: %d Treffer", ziel.Load())
	}
}

// F-2689: Der Ausgangs-Schalter „aus“ gilt weiter, auch für einen auf der
// Intern-Liste stehenden Host (die Liste ersetzt ABOARD_AUSGAENGE_ERLAUBT nicht).
func TestGotify_InternListeUmgehtAusgangsSchalterNicht(t *testing.T) {
	srv, basis, treffer := gotifyTLSZiel(t, http.StatusOK)
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))
	g := NewGotifySender(nil, nil)
	g.basis = basis
	err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: srv.URL, Token: "t"})
	if !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("Schalter umgangen: %v", err)
	}
	if treffer.Load() != 0 {
		t.Fatalf("%d Treffer trotz Schalter", treffer.Load())
	}
}

// F-2689 (G-3): Interne IP-Literale und localhost werden beim Eintragen
// abgelehnt, solange sie nicht auf der Liste stehen; der Text nennt nichts.
func TestValidateGotifyURL_InterneLiteraleAbgelehnt(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	for _, u := range []string{
		"https://169.254.169.254", "https://10.0.0.1/", "https://127.0.0.1:8443", "https://[::1]/",
		"https://localhost", "https://LOCALHOST:8080", "https://gotify.localhost", "https://[fd00::1]",
		"https://192.168.1.5", "https://2130706433", "https://0x7f.1", "https://[::ffff:127.0.0.1]",
		"https://localhost./", "https://127.0.0.1./", "https://2130706433./",
	} {
		err := ValidateGotifyURL(u)
		if err == nil {
			t.Errorf("%s: nicht abgelehnt", u)
			continue
		}
		if !errors.Is(err, ErrGotifyInternesZiel) {
			t.Errorf("%s: falscher Fehler %v", u, err)
		}
		for _, verboten := range []string{"169.254", "10.0.0.1", "127.0.0.1", "192.168"} {
			if strings.Contains(err.Error(), verboten) {
				t.Errorf("%s: Fehlertext nennt %q", u, verboten)
			}
		}
	}
	for _, u := range []string{"https://gotify.example.org", "https://8.8.8.8", "https://gotify.internal:8443/x"} {
		if err := ValidateGotifyURL(u); err != nil {
			t.Errorf("%s: zu Unrecht abgelehnt: %v", u, err)
		}
	}
	ausgang.Setze(ausgang.Neu(false).MitGotifyIntern("127.0.0.1:8443", "localhost", "10.0.0.1"))
	for _, u := range []string{"https://127.0.0.1:8443", "https://localhost", "https://10.0.0.1"} {
		if err := ValidateGotifyURL(u); err != nil {
			t.Errorf("%s: freigegeben, aber abgelehnt: %v", u, err)
		}
	}
	// A-1: Freigabe gilt nur für den eingetragenen Port (ohne Port: 443).
	ausgang.Setze(ausgang.Neu(false).MitGotifyIntern("localhost", "gotify:8443", "[::1]:8080"))
	for _, u := range []string{"https://localhost:5432/x", "https://gotify:5432", "https://gotify", "https://[::1]:9", "https://localhost:8443"} {
		if err := ValidateGotifyURL(u); !errors.Is(err, ErrGotifyInternesZiel) {
			t.Errorf("%s: fremder Port nicht abgelehnt: %v", u, err)
		}
	}
	for _, u := range []string{"https://localhost", "https://localhost:443", "https://gotify:8443/x", "https://[::1]:8080"} {
		if err := ValidateGotifyURL(u); err != nil {
			t.Errorf("%s: freigegeben, aber abgelehnt: %v", u, err)
		}
	}
	// A-3: Nicht-ASCII-Hosts (Vollbreitenziffern, ideographischer Punkt).
	for _, u := range []string{"https://１２７.0.0.1/", "https://127。0。0。1/", "https://gotify.exämple.org"} {
		if err := ValidateGotifyURL(u); !errors.Is(err, ErrGotifyHostUngueltig) {
			t.Errorf("%s: Nicht-ASCII nicht abgelehnt: %v", u, err)
		}
	}
	// Punkt am Ende ist derselbe Name wie ohne.
	ausgang.Setze(ausgang.Neu(false).MitGotifyIntern("gotify.internal", "localhost."))
	for _, u := range []string{"https://gotify.internal./", "https://GOTIFY.internal", "https://localhost"} {
		if err := ValidateGotifyURL(u); err != nil {
			t.Errorf("%s: freigegeben, aber abgelehnt: %v", u, err)
		}
	}
	ausgang.Setze(ausgang.Neu(false).MitGotifyIntern("127.0.0.1:8443", "localhost", "10.0.0.1"))
	if err := ValidateGotifyURL("https://10.0.0.2"); err == nil {
		t.Error("10.0.0.2 steht nicht auf der Liste, wurde aber angenommen")
	}
}

// A-1: Der Dialer verbindet zu einem freigegebenen Host nur auf dem
// freigegebenen Port. Ein zweiter Server auf anderem Port bleibt ungetroffen.
func TestGotify_FreigabeGiltNurFuerDenPort(t *testing.T) {
	srv, basis, treffer := gotifyTLSZiel(t, http.StatusOK)
	srv2, _, treffer2 := gotifyTLSZiel(t, http.StatusOK) // anderer Port, nicht freigegeben
	falsch := srv2.URL
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))
	g := NewGotifySender(nil, nil)
	g.basis = basis
	req, _ := http.NewRequest(http.MethodPost, falsch+"/message", strings.NewReader("{}"))
	err := g.senden(req, falsch)
	if err == nil || err.Error() != "nicht erreichbar" {
		t.Fatalf("fremder Port: %v", err)
	}
	if treffer2.Load() != 0 {
		t.Fatalf("fremder Port: %d Treffer", treffer2.Load())
	}
	// Passender Port: erreichbar.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/message", strings.NewReader("{}"))
	if err := g.senden(req, srv.URL); err != nil || treffer.Load() < 1 {
		t.Fatalf("passender Port: %v, Treffer %d", err, treffer.Load())
	}
	// Beim Eintragen: derselbe Host mit anderem Port wird abgelehnt.
	if err := ValidateGotifyURL(falsch); !errors.Is(err, ErrGotifyInternesZiel) {
		t.Fatalf("Validate fremder Port: %v", err)
	}
}
