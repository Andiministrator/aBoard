package schleuse

import (
	"slices"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

func TestAusUmgebung(t *testing.T) {
	t.Setenv(SchalterEnvVar, "")
	t.Setenv(AufbewahrungEnvVar, "")
	k, err := AusUmgebung()
	if err != nil || k.An || k.AufbewahrungTage != AufbewahrungStandardTage {
		t.Fatalf("Vorgabe: %+v %v", k, err)
	}
	t.Setenv(SchalterEnvVar, " AN ")
	t.Setenv(AufbewahrungEnvVar, "7")
	if k, err = AusUmgebung(); err != nil || !k.An || k.AufbewahrungTage != 7 {
		t.Fatalf("an/7: %+v %v", k, err)
	}
	for _, f := range [][2]string{{"ja", ""}, {"an", "0"}, {"an", "x"}, {"an", "99999"}} {
		t.Setenv(SchalterEnvVar, f[0])
		t.Setenv(AufbewahrungEnvVar, f[1])
		if _, err := AusUmgebung(); err == nil {
			t.Errorf("%v: kein Fehler", f)
		}
	}
}

func TestPruefeBasis(t *testing.T) {
	if b, err := PruefeBasis(AnbieterAnthropic, ""); err != nil || b != "https://api.anthropic.com" {
		t.Errorf("Standard: %q %v", b, err)
	}
	if _, err := PruefeBasis(AnbieterOpenAIKompatibel, ""); err == nil {
		t.Errorf("openai-kompatibel ohne URL akzeptiert")
	}
	if b, err := PruefeBasis(AnbieterOpenAIKompatibel, "https://x.example:8443/v1/"); err != nil || b != "https://x.example:8443/v1" {
		t.Errorf("gültig: %q %v", b, err)
	}
	for _, kaputt := range []string{"http://x.example", "https://u:p@x.example", "https://x.example/?a=1",
		"https://x.example/#f", "https://x.example/../v1", "https://x.example:0", "ftp://x", "https:// x"} {
		if _, err := PruefeBasis(AnbieterMistral, kaputt); err == nil {
			t.Errorf("%q akzeptiert", kaputt)
		}
	}
}

// KC-1: Die Schleuse geht nur zu externen Anbietern. Interne Literal-
// Adressen, Zahlformen und intern auflösende Namen sind abgelehnt — die
// gemessenen Fälle aus dem Kritiker-Lauf zuerst.
func TestPruefeBasis_NurExterneZiele(t *testing.T) {
	for _, intern := range []string{
		// gemessen (Kritiker Sprint 82)
		"https://127.0.0.1/v1", "https://localhost/v1", "https://169.254.169.254/latest",
		"https://[::1]/", "https://10.0.0.5:8443/", "https://0x7f.1/", "https://2130706433/",
		// weitere Bereiche und Formen
		"https://172.16.0.1/", "https://192.168.1.1/", "https://100.64.0.1/", "https://0.0.0.0/",
		"https://0.0.0.1/", "https://224.0.0.1/", "https://[fd00::1]/", "https://[fe80::1]/",
		"https://[::ffff:127.0.0.1]/", "https://[::]/", "https://127.1/", "https://0177.0.0.1/",
		"https://017700000001/", "https://1.2.3.0x4/",
		"https://LOCALHOST/", "https://localhost./", "https://api.localhost/", "https://drucker.local/",
		"https://llm.firma.internal/v1", "https://nas.home.arpa/", "https://intranet/v1",
		// Verify VS-2: Sonderbereiche und eingebettete IPv4-Formen
		"https://[::127.0.0.1]/", "https://[::ffff:0:127.0.0.1]/", "https://[64:ff9b::7f00:1]/",
		"https://[64:ff9b:1::1]/", "https://[2002:7f00:1::]/", "https://[2001:0:7f00:1::]/",
		"https://198.18.0.1/", "https://192.0.0.1/", "https://240.0.0.1/", "https://255.255.255.255/",
		"https://192.0.2.1/", "https://198.51.100.1/", "https://203.0.113.1/", // Verify VV-2
		"https://[fec0::1]/", "https://[100::1]/", "https://[2001:db8::1]/",
		"https://localhost.localdomain/", "https://srv.localdomain./",
	} {
		if b, err := PruefeBasis(AnbieterOpenAIKompatibel, intern); err == nil {
			t.Errorf("%q akzeptiert (→ %q)", intern, b)
		}
	}
	// Positivfälle: öffentliche Namen und eine öffentliche Literal-Adresse.
	for roh, soll := range map[string]string{
		"https://api.openai.com/v1":    "https://api.openai.com/v1",
		"https://llm.example.net/v1":   "https://llm.example.net/v1",
		"https://8.8.8.8/v1":           "https://8.8.8.8/v1",
		"https://xn--bcher-kva.de/api": "https://xn--bcher-kva.de/api",
	} {
		if b, err := PruefeBasis(AnbieterOpenAIKompatibel, roh); err != nil || b != soll {
			t.Errorf("%q: %q %v, erwartet %q", roh, b, err, soll)
		}
	}
}

// KC-4: eine Schreibweise je Gegenüber, sonst greift UNIQUE(anbieter, basis_url) nicht.
func TestPruefeBasis_Normalform(t *testing.T) {
	soll := "https://llm.example.net/v1"
	for _, roh := range []string{
		"https://llm.example.net/v1", "https://LLM.Example.NET/v1", "https://llm.example.net:443/v1",
		"https://llm.example.net/v1/", "https://llm.example.net/v1///", "https://llm.example.net./v1",
		"HTTPS://llm.example.net:443/v1/",
		// Verify VS-3: Port als Zahl
		"https://llm.example.net:0443/v1", "https://llm.example.net:00443/v1",
	} {
		if b, err := PruefeBasis(AnbieterOpenAIKompatibel, roh); err != nil || b != soll {
			t.Errorf("%q → %q %v, erwartet %q", roh, b, err, soll)
		}
	}
	if b, err := PruefeBasis(AnbieterOpenAIKompatibel, "https://llm.example.net:8443/"); err != nil || b != "https://llm.example.net:8443" {
		t.Errorf("Nicht-Standardport: %q %v", b, err)
	}
	for _, kaputt := range []string{"https://llm.example.net//v1", "https://llm.example.net/a//b",
		"https://llm.example.net/%2e%2e/v1", "https://llm.example.net/a%2F%2Fb", "https://llm.example.net../v1"} {
		if b, err := PruefeBasis(AnbieterOpenAIKompatibel, kaputt); err == nil {
			t.Errorf("%q akzeptiert (→ %q)", kaputt, b)
		}
	}
	if NormalformOderRoh("https://LLM.example.net/v1/") != soll {
		t.Errorf("NormalformOderRoh normalisiert nicht")
	}
	if NormalformOderRoh("https://127.0.0.1/v1") != "https://127.0.0.1/v1" {
		t.Errorf("NormalformOderRoh: ungültiger Altwert muss unverändert bleiben")
	}
}

func TestPruefeModelle(t *testing.T) {
	m, err := PruefeModelle([]string{" a ", "", "a", "org/modell:1.5"})
	if err != nil || strings.Join(m, ",") != "a,org/modell:1.5" {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := PruefeModelle([]string{"a b"}); err == nil {
		t.Errorf("Leerraum akzeptiert")
	}
	// KC-5: keine ./..- und keine leeren Segmente.
	for _, kaputt := range []string{"a/../b", "a//b", "a/./b", "a/..", "a/"} {
		if _, err := PruefeModelle([]string{kaputt}); err == nil {
			t.Errorf("%q akzeptiert", kaputt)
		}
	}
	if _, err := PruefeModelle([]string{"models/gemini-1.5-pro", "org/a..b", "x.y"}); err != nil {
		t.Errorf("gültige Namen mit Punkten abgelehnt: %v", err)
	}
}

// Zweiter Riegel (ADR-0134 §1): bei ABOARD_AUSGAENGE=aus nur nutzbar, wenn
// der Host auf der Liste steht; bei „an“ immer.
func TestBeurteile_Ausgangsliste(t *testing.T) {
	basis := "https://api.anthropic.com"
	if b := Beurteile(true, true, []string{"m"}, basis, ausgang.Neu(false)); !b.Nutzbar {
		t.Errorf("Ausgänge an: %+v", b)
	}
	if b := Beurteile(true, true, []string{"m"}, basis, ausgang.Neu(true)); b.Nutzbar || strings.Join(b.Gruende, ",") != GrundAusgangGesperrt || !b.Freigegeben {
		t.Errorf("Ausgänge aus, Host fehlt: %+v", b)
	}
	if b := Beurteile(true, true, []string{"m"}, basis, ausgang.Neu(true, "api.anthropic.com")); !b.Nutzbar {
		t.Errorf("Host auf der Liste: %+v", b)
	}
	if b := Beurteile(false, false, nil, basis, ausgang.Neu(false)); b.Freigegeben || strings.Join(b.Gruende, ",") != "inaktiv,vertragsform-fehlt,keine-modelle" {
		t.Errorf("alles fehlt: %+v", b)
	}
}

// Verify VS-3: Prozent-Kodierung und Port-Ziffern in Normalform.
func TestPruefeBasis_NormalformEscapesUndPort(t *testing.T) {
	for roh, soll := range map[string]string{
		"https://llm.example.net:08443/v1":   "https://llm.example.net:8443/v1",
		"https://llm.example.net/a%2fb":      "https://llm.example.net/a%2Fb",
		"https://llm.example.net/a%2Fb":      "https://llm.example.net/a%2Fb",
		"https://llm.example.net/%7Euser/v1": "https://llm.example.net/~user/v1",
		"https://llm.example.net/~user/v1":   "https://llm.example.net/~user/v1",
		"https://llm.example.net/v%31":       "https://llm.example.net/v1",
	} {
		if b, err := PruefeBasis(AnbieterOpenAIKompatibel, roh); err != nil || b != soll {
			t.Errorf("%q → %q %v, erwartet %q", roh, b, err, soll)
		}
	}
}

// Verify VS-6: Ein gespeicherter Eintrag von vor KC-1 mit internem Ziel ist
// nie nutzbar — auch nicht, wenn sein Host in der Ausgangsliste steht.
func TestBeurteile_AltbestandInternesZiel(t *testing.T) {
	pol := ausgang.Neu(true, "127.0.0.1", "llm.example.net")
	if pol.PruefeURL("127.0.0.1", "https") != nil {
		t.Fatal("Vorbedingung: Host steht in der Ausgangsliste")
	}
	b := Beurteile(true, true, []string{"m"}, "https://127.0.0.1/v1", pol)
	if b.Nutzbar || !slices.Contains(b.Gruende, GrundBasisIntern) {
		t.Fatalf("interner Altbestand: %+v", b)
	}
	if b := Beurteile(true, true, []string{"m"}, "https://llm.example.net/v1", pol); slices.Contains(b.Gruende, GrundBasisIntern) {
		t.Fatalf("öffentlicher Eintrag als intern beurteilt: %+v", b)
	}
}
