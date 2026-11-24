package api

// Tests zu TOTP und Wiederherstellungscodes (ADR-0149 §3, Karte #1075).

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Anhang B, SHA-1, Geheimnis "12345678901234567890"; die Tabelle
// nennt 8 Ziffern, 6 Ziffern sind deren letzte sechs.
func TestTOTP1075_RFC6238Vektoren(t *testing.T) {
	g := []byte("12345678901234567890")
	for sek, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := totpCode(g, TOTPSchritt(time.Unix(sek, 0))); got != want {
			t.Errorf("T=%d: %s, erwartet %s", sek, got, want)
		}
	}
}

func TestTOTP1075_Fenster(t *testing.T) {
	g := []byte("12345678901234567890")
	jetzt := time.Unix(1234567890, 0)
	s := TOTPSchritt(jetzt)
	for versatz, ok := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		code := totpCode(g, s+versatz)
		got, passt := TOTPPasst(g, code, jetzt, 0)
		if passt != ok || (ok && got != s+versatz) {
			t.Errorf("Versatz %d: %v %d", versatz, passt, got)
		}
	}
	// Ein schon angenommener Schritt (und jeder ältere) gilt nicht mehr.
	code := totpCode(g, s)
	if _, ok := TOTPPasst(g, code, jetzt, s); ok {
		t.Error("verbrauchter Schritt angenommen")
	}
	if _, ok := TOTPPasst(g, code, jetzt, s-1); !ok {
		t.Error("jüngerer Schritt abgewiesen")
	}
	// Leerzeichen in der Mitte wie in manchen Apps; sonst nur sechs Ziffern.
	if _, ok := TOTPPasst(g, code[:3]+" "+code[3:], jetzt, 0); !ok {
		t.Error("„123 456“ abgewiesen")
	}
	for _, falsch := range []string{"", "12345", "1234567", "12345a", "١٢٣٤٥٦", code + "0"} {
		if _, ok := TOTPPasst(g, falsch, jetzt, 0); ok {
			t.Errorf("%q angenommen", falsch)
		}
	}
	// Anderes Geheimnis: der Code passt nicht.
	if _, ok := TOTPPasst([]byte("anderes-geheimnis-20"), code, jetzt, 0); ok {
		t.Error("fremdes Geheimnis angenommen")
	}
}

func TestTOTP1075_Einrichtung(t *testing.T) {
	e, err := NeueTOTPEinrichtung("anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Geheimnis) != 20 {
		t.Fatalf("Geheimnis %d Bytes", len(e.Geheimnis))
	}
	roh, err := totpBase32.DecodeString(strings.ReplaceAll(e.GeheimnisText, " ", ""))
	if err != nil || string(roh) != string(e.Geheimnis) {
		t.Fatalf("Text passt nicht zum Geheimnis: %v", err)
	}
	u, err := url.Parse(e.Link)
	if err != nil || u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/aBoard:anna@example.org" {
		t.Fatalf("Link: %q %v", e.Link, err)
	}
	q := u.Query()
	if q.Get("secret") != strings.ReplaceAll(e.GeheimnisText, " ", "") || q.Get("issuer") != "aBoard" ||
		q.Get("digits") != "6" || q.Get("period") != "30" || q.Get("algorithm") != "SHA1" {
		t.Fatalf("Parameter: %v", q)
	}
	if len(e.Codes) != WiederherstellungsAnzahl || len(e.CodeHashes) != WiederherstellungsAnzahl {
		t.Fatalf("%d Codes", len(e.Codes))
	}
	gesehen := map[string]bool{}
	for i, c := range e.Codes {
		if len(c) != 14 || c[4] != '-' || c[9] != '-' || gesehen[c] {
			t.Errorf("Code %q", c)
		}
		gesehen[c] = true
		if WiederherstellungsHash(c) != e.CodeHashes[i] || WiederherstellungsHash(strings.ToUpper(strings.ReplaceAll(c, "-", " "))) != e.CodeHashes[i] {
			t.Errorf("Hash zu %q passt nicht", c)
		}
	}
	if e2, _ := NeueTOTPEinrichtung("anna@example.org"); string(e2.Geheimnis) == string(e.Geheimnis) {
		t.Error("zweimal dasselbe Geheimnis")
	}
}

func TestWiederherstellungsHash1075_Ungueltig(t *testing.T) {
	for _, e := range []string{"", "abcd-efgh-ijk", "abcd-efgh-ijkl-m", "abcd-efgh-ij1l", "abcd-efgh-ij8l", "äbcd-efgh-ijkl", "abcd_efgh_ijkl"} {
		if h := WiederherstellungsHash(e); h != "" {
			t.Errorf("%q ergab einen Hash", e)
		}
	}
	if WiederherstellungsHash("abcd-efgh-ijkl") == "" {
		t.Error("gültiger Code ohne Hash")
	}
}
