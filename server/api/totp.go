package api

// totp.go — Zwei-Faktor mit Einmal-Codes nach RFC 6238 und
// Wiederherstellungscodes (ADR-0149 §3, Karte #1075).
//
// TOTP: HMAC-SHA1 (RFC 4226, dynamische Kürzung), 6 Ziffern, 30 s je Schritt.
// Das ist die Voreinstellung jeder Authenticator-App; andere Werte versteht
// nicht jede. Angenommen wird der aktuelle Schritt und je einer davor und
// danach (Uhrabweichung des Handys), aber nur ein Schritt, der jünger ist als
// der zuletzt angenommene: Ein Code gilt genau einmal (Speicher:
// konten.totp_letzter_schritt).
//
// Geheimnis: 20 Zufallsbytes (160 Bit, RFC 4226 §4), in der App als base32
// ohne Auffüllung, in der Datenbank mit internal/secretcrypto versiegelt.
//
// Wiederherstellungscodes: zehn Stück, je 60 Bit, als „xxxx-xxxx-xxxx“ aus
// a–z und 2–7; gespeichert nur der SHA-256 der Form ohne Bindestriche. Ein
// schneller Hash genügt bei 60 Bit Zufall (kein Passwort, das ein Mensch
// wählt).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// TOTPZiffern, TOTPSchrittweite: Voreinstellung jeder App (RFC 6238).
	TOTPZiffern      = 6
	TOTPSchrittweite = 30 * time.Second
	// totpGeheimnisBytes: 160 Bit (RFC 4226 §4 empfiehlt mindestens 128).
	totpGeheimnisBytes = 20
	// WiederherstellungsAnzahl: so viele Codes beim Einrichten (ADR-0149 §3).
	WiederherstellungsAnzahl = 10
	// totpAussteller steht in der App vor der Adresse.
	totpAussteller = "aBoard"
)

var totpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTPSchritt ist der Zeitschritt zu t.
func TOTPSchritt(t time.Time) int64 { return t.Unix() / int64(TOTPSchrittweite/time.Second) }

// totpCode berechnet den Code zu einem Schritt (RFC 4226 §5.3).
func totpCode(geheimnis []byte, schritt int64) string {
	var zaehler [8]byte
	binary.BigEndian.PutUint64(zaehler[:], uint64(schritt))
	m := hmac.New(sha1.New, geheimnis)
	m.Write(zaehler[:])
	h := m.Sum(nil)
	o := h[len(h)-1] & 0x0f
	wert := binary.BigEndian.Uint32(h[o:o+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", wert%1000000)
}

// istTOTPEingabe: genau sechs Ziffern (Leerzeichen dazwischen erlaubt, wie
// manche Apps sie anzeigen: „123 456“).
func istTOTPEingabe(code string) (string, bool) {
	c := strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(c) != TOTPZiffern {
		return "", false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < '0' || c[i] > '9' {
			return "", false
		}
	}
	return c, true
}

// TOTPPasst sucht den Schritt, zu dem code passt: aktueller Schritt, einer
// davor, einer danach — aber nur Schritte nach letzter. Ergebnis: der
// passende Schritt, oder ok=false. Alle Kandidaten werden immer gerechnet
// (keine Zeitunterschiede je nach Treffer).
func TOTPPasst(geheimnis []byte, code string, jetzt time.Time, letzter int64) (int64, bool) {
	c, ok := istTOTPEingabe(code)
	if !ok {
		return 0, false
	}
	s := TOTPSchritt(jetzt)
	var treffer int64
	gefunden := 0
	for _, kandidat := range []int64{s - 1, s, s + 1} {
		gleich := subtle.ConstantTimeCompare([]byte(totpCode(geheimnis, kandidat)), []byte(c))
		if gleich == 1 && kandidat > letzter && gefunden == 0 {
			treffer, gefunden = kandidat, 1
		}
	}
	return treffer, gefunden == 1
}

// TOTPEinrichtung ist alles, was beim Einrichten entsteht. Klartext
// (GeheimnisText, Link, Codes) wird der Person genau einmal gezeigt und nie
// gespeichert; Geheimnis und CodeHashes gehen in die Datenbank (Geheimnis
// versiegelt der Aufrufer mit secretcrypto).
type TOTPEinrichtung struct {
	Geheimnis     []byte
	GeheimnisText string // base32 ohne Auffüllung, in Vierergruppen
	Link          string // otpauth://totp/…
	Codes         []string
	CodeHashes    []string
}

// NeueTOTPEinrichtung erzeugt Geheimnis und Wiederherstellungscodes für email.
func NeueTOTPEinrichtung(email string) (TOTPEinrichtung, error) {
	g := make([]byte, totpGeheimnisBytes)
	if _, err := rand.Read(g); err != nil {
		return TOTPEinrichtung{}, fmt.Errorf("totp-geheimnis: %w", err)
	}
	codes, hashes, err := neueWiederherstellungscodes()
	if err != nil {
		return TOTPEinrichtung{}, err
	}
	text := totpBase32.EncodeToString(g)
	return TOTPEinrichtung{Geheimnis: g, GeheimnisText: gruppiere(text, 4), Link: totpLink(email, text),
		Codes: codes, CodeHashes: hashes}, nil
}

func gruppiere(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s); i += n {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s[i:min(i+n, len(s))])
	}
	return b.String()
}

// totpLink: otpauth-URI (Key-Uri-Format der Authenticator-Apps).
func totpLink(email, geheimnisB32 string) string {
	label := url.PathEscape(totpAussteller + ":" + email)
	q := url.Values{"secret": {geheimnisB32}, "issuer": {totpAussteller}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(TOTPZiffern)}, "period": {fmt.Sprint(int(TOTPSchrittweite / time.Second))}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

const codeAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

func neueWiederherstellungscodes() ([]string, []string, error) {
	var codes, hashes []string
	for len(codes) < WiederherstellungsAnzahl {
		roh := make([]byte, 12)
		if _, err := rand.Read(roh); err != nil {
			return nil, nil, fmt.Errorf("wiederherstellungscode: %w", err)
		}
		var b strings.Builder
		for i, x := range roh {
			if i > 0 && i%4 == 0 {
				b.WriteByte('-')
			}
			b.WriteByte(codeAlphabet[x&31])
		}
		c := b.String()
		h := WiederherstellungsHash(c)
		doppelt := false
		for _, v := range hashes {
			doppelt = doppelt || v == h
		}
		if doppelt {
			continue
		}
		codes = append(codes, c)
		hashes = append(hashes, h)
	}
	return codes, hashes, nil
}

// WiederherstellungsHash: SHA-256 (hex) der normalisierten Eingabe —
// Kleinbuchstaben, ohne Bindestriche und Leerraum. "" bei einer Eingabe, die
// kein Code sein kann.
func WiederherstellungsHash(eingabe string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(eingabe) {
		switch {
		case r == '-' || r == ' ' || r == '\t':
		case r >= 'a' && r <= 'z' || r >= '2' && r <= '7':
			b.WriteRune(r)
		default:
			return ""
		}
	}
	if b.Len() != 12 {
		return ""
	}
	s := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(s[:])
}
