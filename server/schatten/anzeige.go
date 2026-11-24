package schatten

import "strings"

// InterneKarteTitel ist die Anzeige eines Schattens, dessen Titel nicht
// nach außen geht (ADR-0140 §6/§7).
const InterneKarteTitel = "Interne Karte"

// AnzeigeTitel ist die EINE Regel, nach der Kalender-Feed und Agenten-
// Werkzeuge einen Schatten benennen (Sitzungs-QA C-2, Klasse 198): Die
// strengere Sicht gilt — vertraulich, fehlender oder nur aus Leerraum
// bestehender Titel heißen „Interne Karte“ (zweites Ergebnis false). Nur ein
// echter Titel kommt durch (zweites Ergebnis true). Die Funktion bekommt
// Werte und liest keine Tabelle.
func AnzeigeTitel(vertraulich bool, titel *string) (string, bool) {
	if vertraulich || titel == nil || strings.TrimSpace(*titel) == "" {
		return InterneKarteTitel, false
	}
	return *titel, true
}
