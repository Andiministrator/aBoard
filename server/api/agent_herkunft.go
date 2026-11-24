package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Anbieter und Beleg eines Agenten-Tokens (ADR-0140 §8, Karte #799, Vertrag
// Push-Skript §11): Inhalte des externen Boards dürfen nicht zum Training
// verwendet werden. Auf der Seite außen (boardpaar.seite = aussen) verlangt
// jeder Weg, der ein Agenten-Token anlegt, zwei Angaben — wer den Agenten
// betreibt und wo der vertragliche Trainingsausschluss belegt ist.
//
// GRENZE, ehrlich: Das dokumentiert nur. Der Server erkennt nicht, ob ein
// gültiges Token in einem Privatkonto-Werkzeug steckt; er prüft weder den
// Anbieter noch die Fundstelle. Bestandstokens ohne Angaben bleiben gültig.
const (
	AgentAnbieterMax = 80
	AgentBelegMax    = 300
)

// hatInhaltszeichen: mindestens ein Buchstabe, eine Ziffer, ein Satz- oder ein
// Symbolzeichen (L, N, P, S). Ein Kombinationszeichen (Mn/Mc) allein zählt nicht
// (F-2118, F-2120).
func hatInhaltszeichen(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

// PruefeAgentHerkunft trimmt und prüft beide Angaben (Zeichen, nicht Bytes;
// keine Steuerzeichen und unsichtbaren Zeichen, auch kein Zeilenumbruch —
// beide stehen einzeilig in Listen und CLI-Ausgabe) und verlangt sie auf der Seite außen. Die Seite
// wird je Anfrage gelesen (BoardpaarSeiteMitFehler); ist server_config nicht
// lesbar, fällt die Anlage zu (503), statt die Pflicht stillschweigend zu
// erlassen. Liefert die Normalformen, sonst eine Meldung und den
// HTTP-Status. Ohne cfg (Tests ohne Konfiguration) gilt „aus“.
func PruefeAgentHerkunft(ctx context.Context, cfg *storage.ConfigRepo, anbieter, beleg string) (string, string, string, int) {
	anbieter, beleg = strings.TrimSpace(anbieter), strings.TrimSpace(beleg)
	seite, err := BoardpaarSeiteMitFehler(ctx, cfg)
	if err != nil {
		return "", "", "die einstellung boardpaar.seite ist gerade nicht lesbar", http.StatusServiceUnavailable
	}
	if seite == BoardpaarSeiteAussen && (anbieter == "" || beleg == "") {
		return "", "", "auf dem externen server brauchen agenten-zugänge anbieter und beleg: wer betreibt den agenten, und wo ist der vertragliche trainingsausschluss belegt (fundstelle)? (anbieter und beleg fehlen oder sind leer)", http.StatusBadRequest
	}
	for _, f := range []struct {
		name, wert string
		max        int
	}{{"anbieter", anbieter, AgentAnbieterMax}, {"beleg", beleg, AgentBelegMax}} {
		if utf8.RuneCountInString(f.wert) > f.max {
			return "", "", fmt.Sprintf("%s: höchstens %d zeichen", f.name, f.max), http.StatusBadRequest
		}
		// Die strenge Prüfung der Vorschläge (unsichtbaresZeichen): außer
		// Steuerzeichen und Zeilenumbruch auch Nullbreiten-Zeichen, Braille-
		// Leerzeichen, Hangul-Füller und Bidi-Umkehrungen. TrimSpace kennt sie
		// nicht — sonst erfüllte ein unsichtbarer Wert die Pflicht (Kritiker K-1).
		if z, pos, ja := unsichtbaresZeichen(f.wert, false); ja {
			return "", "", fmt.Sprintf("%s: enthält ein unsichtbares oder nicht darstellbares Zeichen (U+%04X an Stelle %d)", f.name, z, pos), http.StatusBadRequest
		}
		// F-2116: Das Semikolon trennt die Felder im Audit-Wert
		// (`…;anbieter=…;beleg=…`); ein Semikolon im Inhalt machte ihn mehrdeutig
		// (`x;user=u-admin` erzeugte ein zweites `user=`). Das Komma bleibt erlaubt.
		if strings.Contains(f.wert, ";") {
			return "", "", fmt.Sprintf("%s: das zeichen ; ist nicht erlaubt (trennt die felder im audit-eintrag)", f.name), http.StatusBadRequest
		}
		// F-2118: Kombinationszeichen (Mn) lässt unsichtbaresZeichen durch — ein
		// Akzent allein ist aber kein Inhalt. Verlangt wird mindestens ein
		// Buchstabe, eine Ziffer, ein Satz- oder ein Symbolzeichen.
		if f.wert != "" && !hatInhaltszeichen(f.wert) {
			return "", "", fmt.Sprintf("%s: braucht mindestens einen buchstaben, eine ziffer oder ein satz- oder sonderzeichen", f.name), http.StatusBadRequest
		}
	}
	return anbieter, beleg, "", 0
}
