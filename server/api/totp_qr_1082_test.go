package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"rsc.io/qr"
)

var svgMuster = regexp.MustCompile(`<svg[^>]*>`)

// einrichtenSeiteHolen löst Schritt 1 aus und liefert den Antworttext.
func (x *einloeseTest) einrichtenSeiteHolen(t *testing.T, lang string) string {
	t.Helper()
	tok := x.einladen(t, "anna@example.org")
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+EinladungPfadPraefix+tok,
		strings.NewReader(url.Values{"weg": {"password"}, "passwort": {"lang-genug-1234"}, "passwort2": {"lang-genug-1234"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	x.mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("Schritt 1: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") != AnmeldeSeitenCSP {
		t.Errorf("CSP %q", rec.Header().Get("Content-Security-Policy"))
	}
	return rec.Body.String()
}

func TestQR1082_SeiteMitGenauEinemSVG(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	body := x.einrichtenSeiteHolen(t, "de")
	// #1107: Das Logo im Seitenrahmen ist ein eigenes, festes SVG (aria-hidden)
	// — genau einmal da, danach zählt nur noch der QR-Code.
	if strings.Count(body, anmeldeLogo) != 1 {
		t.Fatal("Logo fehlt im Seitenrahmen")
	}
	body = strings.Replace(body, anmeldeLogo, "", 1)
	if n := strings.Count(body, "<svg"); n != 1 {
		t.Fatalf("%d <svg> statt 1", n)
	}
	kopf := svgMuster.FindString(body)
	if !strings.Contains(kopf, `role="img"`) || !strings.Contains(kopf, `aria-label="QR-Code zum Einrichten`) {
		t.Errorf("svg-Kopf: %s", kopf)
	}
	for _, verboten := range []string{"data:", "style=\"", "<img", "otpauth://totp/" + "anna"} {
		// Der otpauth-Link steht nur im href der Textvariante, nie im SVG.
		if verboten == "otpauth://totp/anna" {
			svg := body[strings.Index(body, "<svg"):strings.Index(body, "</svg>")]
			if strings.Contains(svg, "otpauth") || strings.Contains(svg, "secret") {
				t.Errorf("Klartext im SVG")
			}
			continue
		}
		if strings.Contains(body, verboten) {
			t.Errorf("Seite enthält %q", verboten)
		}
	}
	// Schlüssel und Link bleiben (Barrierefreiheit, Handy-Fall).
	if !strings.Contains(body, "<code>") || !strings.Contains(body, `href="otpauth://totp/`) {
		t.Error("Schlüssel oder otpauth-Link fehlt")
	}
}

func TestQR1082_Hinweistext(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	if b := x.einrichtenSeiteHolen(t, "de"); !strings.Contains(b, "Mit der Authenticator-App scannen") {
		t.Error("DE-Hinweis fehlt")
	}
	if b := x.einrichtenSeiteHolen(t, "en"); !strings.Contains(b, "Scan with the authenticator app") ||
		!strings.Contains(b, `aria-label="QR code for setting up`) {
		t.Error("EN-Hinweis fehlt")
	}
}

// pfadModule liest die Rechtecke des <path> zurück in eine Modulkarte.
func pfadModule(t *testing.T, svg string, seite int) map[[2]int]bool {
	t.Helper()
	m := regexp.MustCompile(` d="([^"]*)"`).FindStringSubmatch(svg)
	if m == nil {
		t.Fatal("kein path")
	}
	karte := map[[2]int]bool{}
	for _, s := range regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-(\d+)z`).FindAllStringSubmatch(m[1], -1) {
		var x, y, w int
		_, _ = fmtSscan(s[1], &x)
		_, _ = fmtSscan(s[2], &y)
		_, _ = fmtSscan(s[3], &w)
		for i := 0; i < w; i++ {
			karte[[2]int{x + i, y}] = true
		}
	}
	return karte
}

func TestQR1082_Struktur(t *testing.T) {
	const text = "otpauth://totp/aBoard:anna@example.org?secret=JBSWY3DPEHPK3PXP&issuer=aBoard"
	svg, err := qrSVG(text, "Name", 200)
	if err != nil {
		t.Fatal(err)
	}
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	seite := c.Size + 8
	if !strings.Contains(svg, `viewBox="0 0 `+itoa(seite)+" "+itoa(seite)+`"`) {
		t.Errorf("viewBox passt nicht zu Modulzahl %d + 8: %s", c.Size, svg[:200])
	}
	if !strings.Contains(svg, `width="200" height="200"`) || strings.Contains(svg, "otpauth") {
		t.Error("Größe oder Klartext")
	}
	karte := pfadModule(t, svg, seite)
	// Ruhezone leer, Modulfläche identisch zur Bibliothek.
	for k := range karte {
		if k[0] < 4 || k[1] < 4 || k[0] >= 4+c.Size || k[1] >= 4+c.Size {
			t.Fatalf("Modul %v in der Ruhezone", k)
		}
	}
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if karte[[2]int{x + 4, y + 4}] != c.Black(x, y) {
				t.Fatalf("Modul %d,%d weicht ab", x, y)
			}
		}
	}
	// Finder-Muster: Außenring 7x7 schwarz, Innenquadrat 3x3 schwarz, Ring dazwischen weiß, an drei Ecken.
	for _, o := range [][2]int{{0, 0}, {c.Size - 7, 0}, {0, c.Size - 7}} {
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 7; dx++ {
				rand := dx == 0 || dx == 6 || dy == 0 || dy == 6
				kern := dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
				if karte[[2]int{o[0] + dx + 4, o[1] + dy + 4}] != (rand || kern) {
					t.Fatalf("Finder-Muster bei %v kaputt (%d,%d)", o, dx, dy)
				}
			}
		}
	}
	if karte[[2]int{c.Size - 7 + 4, c.Size - 7 + 4}] && !c.Black(c.Size-7, c.Size-7) {
		t.Error("unten rechts darf kein Finder liegen")
	}
}

func fmtSscan(s string, p *int) (int, error) { return fmt.Sscan(s, p) }
func itoa(n int) string                      { return fmt.Sprint(n) }
