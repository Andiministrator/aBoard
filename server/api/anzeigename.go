package api

import (
	"strings"
	"unicode"
)

// anzeigeNameMaxRunen begrenzt, was aus einem Proxy-Header in users.name
// und in die /me-Antwort gelangt. Der Header ist vom Proxy gesetzt, aber
// nicht von uns geprüft; ein Anzeigename braucht keine 200 Zeichen.
const anzeigeNameMaxRunen = 200

// AnzeigeName liefert den Namen, den die Oberfläche für diese Identität
// zeigt (Header-Avatar über GET /me, Nutzerliste im Admin-Dialog über
// users.name). Er dient NUR der Anzeige — Zuordnung und ACL laufen über die
// normalisierte E-Mail (ADR-0059 §1, ADR-0066) und bleiben davon unberührt.
//
// Reihenfolge (Karte #577):
//  1. Remote-Name, falls der Proxy einen liefert (Authelia).
//  2. Remote-User, solange er nicht rein aus Ziffern besteht.
//  3. Remote-Email.
//  4. Remote-User, auch wenn numerisch — besser als gar nichts.
//
// Hintergrund zu 2/3: oauth2-proxy mit Google (Team-Betrieb) setzt in Remote-User die
// numerische Konto-ID und liefert keinen Anzeigenamen. Die Oberfläche zeigte
// dann eine zwanzigstellige Zahl. Eine Kennung aus lauter Ziffern ist nie
// ein Name, den ein Mensch sich gegeben hat — deshalb der Rückfall auf die
// E-Mail, die der Nutzer wiedererkennt.
func (id *ForwardAuthIdentity) AnzeigeName() string {
	if id == nil {
		return ""
	}
	if n := bereinigeAnzeigeName(id.Name); n != "" {
		return n
	}
	user := bereinigeAnzeigeName(id.User)
	if user != "" && !nurZiffern(user) {
		return user
	}
	if e := bereinigeAnzeigeName(id.Email); e != "" {
		return e
	}
	return user
}

// bereinigeAnzeigeName entfernt Steuer- und Formatzeichen, kürzt Leerraum an
// den Rändern und begrenzt die Länge in Runen (nie mitten in einem
// UTF-8-Zeichen). Formatzeichen (Kategorie Cf) sind u. a. Richtungswechsel
// wie U+202E und unsichtbare Zeichen wie U+200B — mit ihnen liesse sich ein
// Name im Tooltip oder in der Nutzerliste anders darstellen, als er
// gespeichert ist (Kritiker Sprint 73, F-1174). Ausgenommen sind die Zeichen
// aus formatzeichenInNamen, die in echten Namen vorkommen (F-1180).
func bereinigeAnzeigeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && !formatzeichenInNamen[r]) || leereGlyphen[r] {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > anzeigeNameMaxRunen {
		s = strings.TrimSpace(string(r[:anzeigeNameMaxRunen]))
	}
	// F-1183: Ein Rest aus lauter unsichtbaren Zeichen (etwa nur ZWNJ oder
	// RLM aus formatzeichenInNamen) ist kein Name — leer zurückgeben, damit
	// AnzeigeName auf die nächste Quelle zurückfällt, statt einen unsichtbaren
	// Namen zu speichern.
	if !hatSichtbaresZeichen(s) {
		return ""
	}
	return s
}

// hatSichtbaresZeichen meldet, ob s mindestens einen Buchstaben, eine Ziffer,
// ein Satzzeichen oder ein Symbol enthält (Kategorien L, N, P, S).
// Kombinationszeichen (M) zählen nicht (Kritiker Sprint 74): Sie zeichnen
// nur an einem Basiszeichen, und darunter sind unsichtbare wie die Variation
// Selectors (U+FE00–FE0F, U+E0100–E01EF), U+034F oder U+180B–U+180F — ein
// Name aus lauter solchen galt sonst als sichtbar. Leerraum (Z) und
// Formatzeichen (Cf) zählen ohnehin nicht. Strukturell statt als Liste; die
// Füllzeichen, die trotz Kategorie L/S nichts zeichnen, entfernt
// bereinigeAnzeigeName vorher (leereGlyphen).
func hatSichtbaresZeichen(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.L, unicode.N, unicode.P, unicode.S) {
			return true
		}
	}
	return false
}

// formatzeichenInNamen sind Formatzeichen, die bereinigeAnzeigeName stehen
// lässt, weil echte Namen sie brauchen (Verify Sprint 73, F-1180): ZWNJ und
// ZWJ steuern die Buchstabenverbindung (persische Namen, indische Schriften,
// Emoji-Sequenzen), LRM/RLM/ALM sind schwache Richtungsmarken für gemischte
// Schreibrichtung. Keines davon kehrt Buchstabentext um — das tun nur die
// Einbettungen, Overrides und Isolate (U+202A–U+202E, U+2066–U+2069), und die
// fallen weg. Neben Ziffern können LRM/RLM/ALM deren Lage verschieben
// (UTR #36); für einen Anzeigenamen nehmen wir das hin (Verify Sprint 73).
var formatzeichenInNamen = map[rune]bool{
	'\u200c': true, // ZWNJ
	'\u200d': true, // ZWJ
	'\u200e': true, // LRM
	'\u200f': true, // RLM
	'\u061c': true, // ALM
}

// leereGlyphen sind Zeichen, die Unicode als Buchstabe (L) oder Symbol (S)
// führt, die aber nichts zeichnen (F-1185, dritter Verify Sprint 73):
// unicode.IsGraphic hält sie für sichtbar, ein Name aus lauter solchen
// Zeichen unterliefe den Rückfall auf die E-Mail wie in F-1183. Sie fallen
// deshalb wie Formatzeichen weg. Eine feste Liste, keine Heuristik: Das sind
// die Füllzeichen, die in Anzeigenamen als „leerer Name“ kursieren.
var leereGlyphen = map[rune]bool{
	'\u115f': true, // HANGUL CHOSEONG FILLER
	'\u1160': true, // HANGUL JUNGSEONG FILLER
	'\u3164': true, // HANGUL FILLER
	'\uffa0': true, // HALFWIDTH HANGUL FILLER
	'\u2800': true, // BRAILLE PATTERN BLANK
}

// nurZiffern meldet, ob s nicht leer ist und ausschliesslich aus den
// ASCII-Ziffern 0–9 besteht.
func nurZiffern(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
