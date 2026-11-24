package schleuse

// versand_test.go — Re-Identifizierung und Versand-Client (Karte #686,
// ADR-0134 §1, §3.6).

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReIdentifiziere_KanonischUndMeldung(t *testing.T) {
	z := map[string]string{"[PERSON_1]": "Anna Müller", "[FIRMA_1]": "Gartenbau Huber GmbH",
		"[PERSON_2]": "[PERSON_1]"} // Original, das wie ein Platzhalter aussieht
	erg := ReIdentifiziere("[PERSON_1] schreibt an [FIRMA_1]. [PERSON_2] ruft an. Unbekannt: [PERSON_9], "+
		"[Person_1], [PERSON 1], (FIRMA_1), firma_1, Person 1 bleibt Text. (Firma 2024) [Ort 5] board_1 Ort_2", z)
	soll := "Anna Müller schreibt an Gartenbau Huber GmbH. [PERSON_1] ruft an. Unbekannt: [PERSON_9], " +
		"[Person_1], [PERSON 1], (FIRMA_1), firma_1, Person 1 bleibt Text. (Firma 2024) [Ort 5] board_1 Ort_2"
	if erg.Text != soll {
		t.Errorf("Text:\n%s\nsoll:\n%s", erg.Text, soll)
	}
	if erg.Ersetzt != 3 {
		t.Errorf("ersetzt: %d", erg.Ersetzt)
	}
	// Abwägung SK-1: Abwandlungen in jeder Schreibweise werden gemeldet,
	// auch auf Kosten von Fehlalarmen („[Ort 5]“, „board_1“); nicht gemeldet
	// werden Jahreszahlen und „Person 1“ ohne Klammern und Trenner.
	want := []string{"(FIRMA_1)", "Ort_2", "[Ort 5]", "[PERSON 1]", "[PERSON_9]", "[Person_1]", "board_1", "firma_1"}
	if !reflect.DeepEqual(erg.Veraendert, want) {
		t.Errorf("gemeldet: %q, soll %q", erg.Veraendert, want)
	}
	// Ohne Zuordnung (nach Ablauf der Frist) bleibt alles stehen und wird gemeldet.
	if e := ReIdentifiziere("[PERSON_1]", nil); e.Text != "[PERSON_1]" || len(e.Veraendert) != 1 {
		t.Errorf("ohne Zuordnung: %+v", e)
	}
}

func TestPruefeVerbindung_InterneAdressen(t *testing.T) {
	for _, adr := range []string{"127.0.0.1:443", "[::1]:443", "10.1.2.3:443", "169.254.169.254:80",
		"[::ffff:192.168.0.1]:443", "[64:ff9b::7f00:1]:443", "[fe80::1%eth0]:443", "[2002:7f00:1::]:443", "0.0.0.0:443"} {
		if err := pruefeVerbindung("tcp", adr, nil); !errors.Is(err, ErrInterneAdresse) {
			t.Errorf("%s nicht abgelehnt: %v", adr, err)
		}
	}
	for _, adr := range []string{"93.184.216.34:443", "[2606:4700::1111]:443"} {
		if err := pruefeVerbindung("tcp", adr, nil); err != nil {
			t.Errorf("%s abgelehnt: %v", adr, err)
		}
	}
}

// Ein Name, der auf 127.0.0.1 auflöst (hier: ein httptest-Server über
// „localhost“), wird beim Verbinden abgelehnt — nach der Auflösung, nicht
// am Text der URL (DNS-Rebinding).
func TestVersandTransport_AufgeloesteAdresseGeprueft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	ziel := "http://localhost:" + u.Port()
	basis := http.DefaultTransport.(*http.Transport).Clone()
	basis.Proxy = nil
	rt, err := VersandTransport(ziel, basis, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ziel+"/x", nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, ErrInterneAdresse) {
		t.Fatalf("Verbindung zu localhost: %v", err)
	}
	if !errors.Is(rt.Verstoss(), ErrInterneAdresse) {
		t.Errorf("Verstoss nicht gemerkt: %v", rt.Verstoss())
	}
}

// Mit Proxy geht die Verbindung NUR an den Proxy; jede andere direkte
// Verbindung ist ein Fehler.
func TestVersandTransport_NurDerProxy(t *testing.T) {
	var gesehen string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gesehen = r.URL.String()
		_, _ = io.WriteString(w, "über proxy")
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	basis := http.DefaultTransport.(*http.Transport).Clone()
	basis.Proxy = http.ProxyURL(pu)
	rt, err := VersandTransport("http://api.example.com", basis, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://api.example.com/v1", nil)
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("über Proxy: %v", err)
	}
	res.Body.Close()
	if !strings.Contains(gesehen, "api.example.com") {
		t.Errorf("Proxy sah %q", gesehen)
	}
}

type festeAntwort struct{ status int }

func (f festeAntwort) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {"http://127.0.0.1/"}}}, nil
}

func TestVersandRT_UmleitungIstFehler(t *testing.T) {
	rt := &VersandRT{basis: festeAntwort{status: http.StatusTemporaryRedirect}}
	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1", nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, ErrUmleitung) || !errors.Is(rt.Verstoss(), ErrUmleitung) {
		t.Fatalf("Umleitung: %v / %v", err, rt.Verstoss())
	}
	ok := &VersandRT{basis: festeAntwort{status: http.StatusOK}}
	if res, err := ok.RoundTrip(req); err != nil || res.StatusCode != 200 || ok.Verstoss() != nil {
		t.Errorf("200: %v %v", err, ok.Verstoss())
	}
}

// Kritiker KS3-8: weitere abgewandelte Formen werden gemeldet, die Kappung
// sagt, wie viele fehlen.
func TestReIdentifiziere_WeitereFormenUndKappung(t *testing.T) {
	erg := ReIdentifiziere("[PERSON_1a] [PERSON__1] [PERSON_１] ［PERSON_1］ PERSON__2", map[string]string{"[PERSON_1]": "Anna"})
	want := []string{"PERSON__2", "[PERSON_1a]", "[PERSON__1]", "[PERSON_１]", "［PERSON_1］"}
	if !reflect.DeepEqual(erg.Veraendert, want) {
		t.Errorf("gemeldet: %q, soll %q", erg.Veraendert, want)
	}
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("[PERSON_9" + strconv.Itoa(i) + "] ")
	}
	e := ReIdentifiziere(b.String(), nil)
	if len(e.Veraendert) != 51 || e.Veraendert[50] != "… +10" {
		t.Errorf("Kappung: %d %q", len(e.Veraendert), e.Veraendert[len(e.Veraendert)-1])
	}
}

// Sitzungs-Kritiker SK-1: die Formen, die ein Modell aus „[PERSON_1]“
// tatsächlich macht, werden gemeldet; Jahreszahlen nicht.
func TestReIdentifiziere_GaengigeAbwandlungen(t *testing.T) {
	for _, form := range []string{"[Person 1]", "(Person 1)", "[Person-1]", "[Kennung-4]", "<Email 3>", "Person_1", "person_1", "PERSON-1", "[person_1]", "{Person_2}"} {
		if e := ReIdentifiziere("x "+form+" y", nil); len(e.Veraendert) != 1 || e.Veraendert[0] != form {
			t.Errorf("%q: gemeldet %q", form, e.Veraendert)
		}
	}
	for _, text := range []string{"(Firma 2024)", "[Ort 12345]", "Person 1 hat angerufen", "Personal 1"} {
		if e := ReIdentifiziere(text, nil); len(e.Veraendert) != 0 {
			t.Errorf("%q: Fehlalarm %q", text, e.Veraendert)
		}
	}
}
