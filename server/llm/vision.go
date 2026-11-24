// vision.go — Bilder als Teil des Prompts (#429, ADR-0121).
//
// Der Nutzer wählt im KI-Tab Anhänge aus; der Server lädt sie aus dem
// Attachment-Store und hängt sie an den User-Turn. Der Client schickt nur
// die Anhang-ids — nicht die Bytes: Der Request-Body des Endpunkts ist auf
// MaxLLMSendRequestBytes (64 KB) begrenzt, und ein zweiter Upload desselben
// Bildes wäre ohnehin verschwendet.
//
// Dieses Paket kennt weder Store noch ACL. Hier stehen Typ, Grenzen und die
// Prüfung; das Laden und die Berechtigung liegen in server/api.

package llm

import (
	"fmt"
	"strings"
)

// MaxImageBytes — Obergrenze pro Bild, gemessen an den ROHEN Bytes (nicht an
// der base64-Fassung, die rund ein Drittel grösser ist).
//
// 5 MB ist Anthropics dokumentierte Grenze und zugleich die schärfste der
// vier Anbieter — eine gemeinsame Grenze ist hier besser als vier: Sonst
// hinge es am gewählten Modell, ob dasselbe Bild durchgeht, und der Nutzer
// müsste die Anbieter-Grenzen kennen, um seine Auswahl zu verstehen.
//
// Das Attachment-Limit des Uploads liegt bei 50 MB (defaultMaxAttachmentBytes)
// — ein hochgeladenes Bild kann also legitim zu gross zum Mitschicken sein.
// Genau deshalb nennt die Ablehnung beide Zahlen.
const MaxImageBytes = 5 * 1024 * 1024

// MaxImagesPerSend — Anzahl der Bilder pro Anfrage. Fünf ist keine technische
// Grenze der Anbieter, sondern eine Kostengrenze: Ein grosses Bild kostet
// leicht mehr Tokens als die ganze Karte drumherum.
const MaxImagesPerSend = 5

// MaxImageBytesTotal — Summe über alle Bilder einer Anfrage. Ohne diese
// Grenze wären fünf Bilder à 5 MB zulässig, und base64 macht daraus rund
// 33 MB im Request an den Anbieter.
const MaxImageBytesTotal = 12 * 1024 * 1024

// AllowedImageMIME — die MIME-Typen, die alle vier Anbieter verstehen.
// Bewusst eine Allowlist: Eine Blockliste müsste bei jedem neuen Format
// nachgezogen werden, und was hier durchrutscht, geht an einen fremden
// Dienst.
var AllowedImageMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// ImageAttachment — ein Bild, das mit dem User-Turn reist.
// Data sind die ROHEN Bytes; die base64-Kodierung macht jeder Provider
// selbst, weil sie sich im Ziel-Schema unterscheidet (Anthropic trennt
// media_type und data, OpenAI verlangt eine vollständige data:-URL).
type ImageAttachment struct {
	ID       string // Anhang-id (Audit)
	Filename string // Anzeigename (Audit)
	MIME     string
	Data     []byte
}

// PruefeBild prüft einen einzelnen Anhang gegen MIME-Allowlist und
// Grössengrenze. Rückgabe "" heisst: in Ordnung.
//
// Der MIME-Typ kommt aus dem Karten-Zustand und ist damit eine Behauptung
// des Uploads. Für die Frage „darf das an ein fremdes Modell" reicht das:
// Passt der Inhalt nicht zum deklarierten Typ, lehnt der Anbieter ab — es
// entsteht kein Zugriff auf etwas, das nicht ohnehin an der Karte hängt.
func PruefeBild(mime string, groesse int64) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	// Ein "; charset=…"-Anhang am MIME-Typ ist bei Bildern unüblich, aber
	// nicht ausgeschlossen — vor dem Vergleich abschneiden, sonst scheitert
	// eine gültige Datei an einem Detail, das niemand sieht.
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	if m == "" {
		return "Anhang ohne MIME-Typ — nur Bilder können mitgeschickt werden"
	}
	if !AllowedImageMIME[m] {
		return fmt.Sprintf("Anhang-Typ %q kann nicht mitgeschickt werden (erlaubt: %s)", m, ErlaubteBildTypen())
	}
	if groesse > MaxImageBytes {
		return fmt.Sprintf("Anhang ist %d B gross, Grenze fuer den Versand an ein Modell sind %d B (hochladen darf man mehr)", groesse, MaxImageBytes)
	}
	return ""
}

// ErlaubteBildTypen liefert die Allowlist als stabil sortierte, lesbare
// Aufzählung für Fehlermeldungen.
func ErlaubteBildTypen() string {
	// Feste Reihenfolge statt Map-Iteration — eine Fehlermeldung, die bei
	// jedem Aufruf anders aussieht, ist in Tests und Logs unbrauchbar.
	return "image/gif, image/jpeg, image/png, image/webp"
}

// PruefeBildMenge prüft Anzahl und Gesamtgrösse. Rückgabe "" heisst: in
// Ordnung. Wird VOR dem Laden der Bytes aufgerufen, damit eine zu grosse
// Auswahl nicht erst den Speicher füllt.
func PruefeBildMenge(anzahl int, summe int64) string {
	if anzahl > MaxImagesPerSend {
		return fmt.Sprintf("%d Anhaenge ausgewaehlt, hoechstens %d pro Anfrage", anzahl, MaxImagesPerSend)
	}
	if summe > MaxImageBytesTotal {
		return fmt.Sprintf("ausgewaehlte Anhaenge sind zusammen %d B gross, Grenze sind %d B", summe, MaxImageBytesTotal)
	}
	return ""
}
