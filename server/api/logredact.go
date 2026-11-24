package api

import (
	"fmt"
	"regexp"
	"strings"
)

// Redaktion für Fehlerwerte, die ins Server-Log geschrieben werden.
//
// WARUM ES DAS GIBT (Sprint-57-Kritiker, F-670/F-671/F-672):
// Karte #411 hat writeError beigebracht, den zugrunde liegenden Fehler zu
// loggen — vorher verschwand er spurlos (F-252). Die Karte prüfte dabei, ob
// Geheimnisse in Fehlerwerten stecken können, und kam zu dem Schluss, es
// brauche keine Redaktion: "der LLM-Key geht per x-api-key-Header, nie in
// die URL". Für Anthropic und OpenAI stimmt das. Für Google nicht — dort
// steht der Key als `?key=…` im Query-String (siehe llm/google.go, dort im
// Dateikommentar ausdrücklich vermerkt), und Go's *url.Error trägt die
// vollständige URL im Fehlertext. Der Kritiker hat das reproduziert: eine
// unerreichbare Google-Endpunkt-Adresse erzeugte eine Logzeile mit dem Key
// im Klartext.
//
// Die Lehre ist nicht "eine Stelle vergessen", sondern: Ein Fehlerwert ist
// unkontrollierter Text aus fremden Bibliotheken. Was er enthält, entscheidet
// nicht der Aufrufer.
//
// GRENZEN — bewusst benannt statt behauptet:
// Diese Funktion kennt drei Klassen. Sie ist gegenüber DIESEN vollständig,
// nicht gegenüber allem, was je in einem Fehlerwert stehen kann:
//
//  1. Geheimnisse in Query-Parametern (Google-Key, generell token/secret/…)
//  2. Steuerzeichen, die eine zweite Logzeile vortäuschen (Log-Forging)
//  3. Mailadressen (Personenbezug, kein Geheimnis)
//
// Was sie NICHT kann: Geheimnisse in Positionen erkennen, die nicht als
// Query-Parameter auftreten — etwa ein Token mitten in einem Freitext oder
// in einem JSON-Fragment einer Provider-Antwort. Wer eine neue Quelle
// anschliesst, prüft das für seine Fehlerwerte selbst und erweitert hier,
// statt sich darauf zu verlassen, dass die Liste schon passen wird.

// Query-Parameter, deren Wert als Geheimnis gilt. Bewusst breiter als die
// heute bekannten Aufrufer: die Liste soll den nächsten Provider mit
// abdecken, nicht nur den, der den Befund ausgelöst hat.
var geheimeQueryParameter = []string{
	"key", "api_key", "apikey", "access_token", "token",
	"secret", "client_secret", "password", "passwd", "auth", "signature", "sig",
}

// Trifft `param=wert` in einer URL — der Wert endet am nächsten Trennzeichen
// (&, #, Anführungszeichen, Leerraum). Case-insensitive, weil Provider die
// Schreibweise nicht garantieren.
//
// Das `(?:[a-z0-9]+_)*`-Präfix fängt Compound-Namen (Sprint-57-Verify-Lauf):
// In Go-RE2 ist `_` ein Wortzeichen, also steht zwischen `refresh_` und
// `token` KEINE Wortgrenze — `\btoken=` matchte `refresh_token=` nicht.
// Gemessen: `refresh_token`, `oauth_token`, `my_password` und `user_api_key`
// liefen unredigiert durch. Kein heutiger Aufrufer nutzt diese Namen, aber
// genau das ist der Anspruch dieser Liste: den nächsten Provider mit
// abdecken. `refresh_token` im Query-String ist bei OAuth die Regel.
//
// Der Unterstrich im Präfix ist PFLICHT (`[a-z0-9]+_`, nicht `[a-z0-9]*_?`).
// Die erste Fassung dieses Fixes liess ihn optional — und redigierte prompt
// `monkey=…`, weil `mon`+`key` passt. Ein Fehlalarm ist hier milder als eine
// Lücke (es geht nur Diagnose-Information verloren), aber er wäre eingecheckt
// worden mit einem Kommentar, der das Gegenteil behauptet. Nachgemessen:
// `monkey`, `assignee`, `author` bleiben unangetastet, `refresh_token`,
// `oauth_token`, `my_password`, `user_api_key` werden redigiert.
var geheimQueryRe = regexp.MustCompile(
	`(?i)\b(?:[a-z0-9]+_)*(` + strings.Join(geheimeQueryParameter, "|") + `)=([^&#"'\s]+)`)

// Grobe, absichtlich einfache Mailadress-Erkennung. Sie muss nicht RFC-5322
// vollständig abdecken — sie soll das offensichtliche Muster in Fehlertexten
// wie `mail: rcpt patient@klinik.example: 550 …` fassen.
var mailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// redactForLog macht einen Fehlerwert logfähig.
//
// Rückgabe ist bewusst ein string und kein error: Was hier herauskommt, ist
// nicht mehr der Fehler, sondern seine für ein Log gekürzte Darstellung. Wer
// den echten Fehler braucht (Vergleich, errors.Is), nimmt den Originalwert.
// agentTokenRe trifft Agenten-Tokens (storage.AgentTokenPraefix + URL-sicheres
// Base64). Mindestlänge 20, damit das blosse Wort „abt_" in Texten stehen bleibt.
var agentTokenRe = regexp.MustCompile(`abt_[A-Za-z0-9_-]{20,}`)

func redactForLog(err error) string {
	if err == nil {
		return ""
	}
	return redactString(err.Error())
}

// redactString ist der eigentliche Filter — getrennt von redactForLog, damit
// er auch auf Nicht-Fehler-Text anwendbar und direkt testbar ist.
func redactString(s string) string {
	// $0 statt $1: Der ganze getroffene Name inklusive Compound-Präfix soll
	// stehen bleiben, damit im Log erkennbar ist, WELCHER Parameter redigiert
	// wurde — `refresh_token=[redigiert]`, nicht `token=[redigiert]`.
	s = geheimQueryRe.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.Index(m, "="); i >= 0 {
			return m[:i] + "=[redigiert]"
		}
		return m
	})
	s = mailRe.ReplaceAllStringFunc(s, maskMail)
	// Agenten-Tokens (Karte #553, Kritiker Sprint 71 K-A4): Das Präfix abt_
	// macht sie auch ausserhalb eines Query-Parameters erkennbar.
	s = agentTokenRe.ReplaceAllString(s, "abt_[redigiert]")
	return entschaerfeSteuerzeichen(s)
}

// maskMail behält so viel, wie zur Diagnose nötig ist, und entfernt den Rest.
// `patient@klinik.example` → `p***@klinik.example`.
//
// Die Domain bleibt stehen: Ob eine Zustellung an einen bestimmten Anbieter
// scheitert, ist genau die Information, wegen der F-252 überhaupt loggt. Der
// lokale Teil identifiziert dagegen die Person und trägt zur Diagnose nichts
// bei — er ist der Teil, der weg muss.
func maskMail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "[redigiert]"
	}
	lokal := addr[:at]
	rest := addr[at:]
	if len(lokal) == 1 {
		// Ein einzelnes Zeichen stehen zu lassen wäre keine Maskierung.
		return "*" + "***" + rest
	}
	return lokal[:1] + "***" + rest
}

// entschaerfeSteuerzeichen verhindert Log-Forging: Ein Fehlerwert kann
// nutzerkontrollierten Text enthalten (Anhang-Dateinamen kommen ungefiltert
// durch — die Sanitizer entfernen nur NUL und Pfadtrenner, keine Umbrüche).
// Wer einen Zeilenumbruch ins Log schreiben kann, kann eine zweite, echt
// aussehende Logzeile erfinden und damit die Auswertung fälschen.
//
// Ersetzt wird sichtbar statt entfernt: `\n` verschwinden zu lassen würde den
// Versuch verbergen, und genau der ist die interessante Information.
func entschaerfeSteuerzeichen(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf(`\x%02x`, r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
