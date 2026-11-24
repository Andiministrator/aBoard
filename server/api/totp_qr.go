package api

import (
	"fmt"
	"html"
	"strings"

	"rsc.io/qr"
)

// qrRuhezone: Rand in Modulen (ISO/IEC 18004 verlangt vier).
const qrRuhezone = 4

// qrSVG zeichnet text als QR-Code (Fehlerkorrektur M) in ein eingebettetes
// <svg> (#1082, ADR-0149 §6). Nur Präsentationsattribute, kein style=, kein
// <img>/data:-URL: Die CSP der Anmeldeseiten bleibt unverändert. Der Text
// selbst steht nicht im SVG, nur die Modulfläche. beschriftung (Klartext, wird
// escaped) ist der Screenreader-Name; groesse in CSS-Pixeln.
func qrSVG(text, beschriftung string, groesse int) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", fmt.Errorf("qr-code: %w", err)
	}
	seite := c.Size + 2*qrRuhezone
	var d strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; {
			if !c.Black(x, y) {
				x++
				continue
			}
			start := x
			for x < c.Size && c.Black(x, y) {
				x++
			}
			fmt.Fprintf(&d, "M%d %dh%dv1h-%dz", start+qrRuhezone, y+qrRuhezone, x-start, x-start)
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-label="%s" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`,
		html.EscapeString(beschriftung), groesse, groesse, seite, seite, seite, seite, d.String()), nil
}
