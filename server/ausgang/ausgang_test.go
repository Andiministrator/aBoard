package ausgang

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestAusUmgebung(t *testing.T) {
	faelle := []struct {
		wert, liste string
		fehler      bool
		aus         bool
	}{
		{"", "", false, false},
		{"an", "", false, false},
		{"AUS", "", false, true},
		{" aus ", "llm.internal:8000, gotify.internal", false, true},
		{"0", "", true, false},
		{"off", "", true, false},
		{"aus", "https://api.openai.com", true, false},
		{"aus", "*.internal", true, false},
	}
	for _, f := range faelle {
		t.Setenv("ABOARD_AUSGAENGE", f.wert)
		t.Setenv("ABOARD_AUSGAENGE_ERLAUBT", f.liste)
		p, err := AusUmgebung()
		if (err != nil) != f.fehler {
			t.Errorf("%q/%q: Fehler %v, erwartet Fehler=%v", f.wert, f.liste, err, f.fehler)
			continue
		}
		if err == nil && p.aus != f.aus {
			t.Errorf("%q: aus=%v, erwartet %v", f.wert, p.aus, f.aus)
		}
	}
}

func TestPruefe(t *testing.T) {
	p := Neu(true, "Llm.Internal:8000", "gotify.internal", "::1")
	faelle := map[string]bool{
		"llm.internal:8000":        true,  // host:port genau
		"llm.internal:8001":        false, // anderer Port
		"gotify.internal:443":      true,  // Host ohne Port erlaubt jeden Port
		"GOTIFY.internal:80":       true,
		"api.openai.com:443":       false,
		"[::1]:25":                 true,
		"evil.gotify.internal:443": false, // kein Suffix-Treffer
	}
	for ziel, erlaubt := range faelle {
		err := p.Pruefe(ziel)
		if (err == nil) != erlaubt {
			t.Errorf("%s: err=%v, erwartet erlaubt=%v", ziel, err, erlaubt)
		}
		if err != nil && !errors.Is(err, ErrGesperrt) {
			t.Errorf("%s: Fehler ist kein ErrGesperrt: %v", ziel, err)
		}
	}
	if err := Neu(false).Pruefe("irgendwo:1"); err != nil {
		t.Errorf("an: %v", err)
	}
	if err := (&Politik{}).Pruefe("irgendwo:1"); err != nil {
		t.Errorf("Nullwert muss alles erlauben: %v", err)
	}
}

func TestPruefeURLStandardPort(t *testing.T) {
	p := Neu(true, "a.internal:443", "b.internal:80")
	if err := p.PruefeURL("a.internal", "https"); err != nil {
		t.Errorf("https ohne Port = 443: %v", err)
	}
	if err := p.PruefeURL("b.internal", "http"); err != nil {
		t.Errorf("http ohne Port = 80: %v", err)
	}
	if err := p.PruefeURL("a.internal", "http"); err == nil {
		t.Error("a.internal:80 steht nicht auf der Liste")
	}
}

type zaehlTransport struct{ n int }

func (z *zaehlTransport) RoundTrip(*http.Request) (*http.Response, error) {
	z.n++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestTransportRuftBasisNichtAuf(t *testing.T) {
	defer Setze(Setze(Neu(true, "erlaubt.internal")))
	basis := &zaehlTransport{}
	c := &http.Client{Transport: Transport(basis)}
	_, err := c.Get("https://api.openai.com/v1/models")
	if !errors.Is(err, ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if !strings.Contains(err.Error(), "api.openai.com:443") {
		t.Errorf("Meldung nennt das Ziel nicht: %v", err)
	}
	if basis.n != 0 {
		t.Fatalf("Basis-Transport %d-mal aufgerufen", basis.n)
	}
	if _, err := c.Get("https://erlaubt.internal/x"); err != nil || basis.n != 1 {
		t.Fatalf("erlaubtes Ziel: err=%v, Aufrufe=%d", err, basis.n)
	}
}

type zaehlDialer struct{ n int }

func (z *zaehlDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	z.n++
	return nil, errors.New("gewählt")
}

func TestWaehleWaehltNicht(t *testing.T) {
	defer Setze(Setze(Neu(true)))
	d := &zaehlDialer{}
	if _, err := Waehle(context.Background(), d, "tcp", "smtp.example.com:587"); !errors.Is(err, ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if d.n != 0 {
		t.Fatalf("Dialer %d-mal aufgerufen", d.n)
	}
	Setze(Neu(false))
	_, _ = Waehle(context.Background(), d, "tcp", "smtp.example.com:587")
	if d.n != 1 {
		t.Fatalf("bei an muss gewählt werden, Aufrufe=%d", d.n)
	}
}

func TestBasisPacktNurDieEigeneHuelleAus(t *testing.T) {
	innen := &zaehlTransport{}
	if Basis(Transport(innen)) != http.RoundTripper(innen) {
		t.Error("Basis(Transport(x)) != x")
	}
	if Basis(innen) != http.RoundTripper(innen) {
		t.Error("fremder Transport muss unverändert zurückkommen")
	}
}
