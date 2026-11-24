package api

import (
	"errors"
	"fmt"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Der Befund, der diese Datei ausgelöst hat (Sprint-57-Kritiker):
// Karte #411 begründete den Verzicht auf jede Redaktion damit, der LLM-Key
// gehe "per x-api-key-Header, nie in die URL". Für Google stimmt das nicht —
// dort steht er als ?key=… im Query-String, und Go legt die vollständige URL
// in den Fehlertext. Jede Netzstörung hätte den Key im Klartext ins
// Container-Log geschrieben.

func TestRedactForLog_GoogleKeyAusDerURL(t *testing.T) {
	// Nachstellung des echten Pfads: url.Error trägt die volle URL.
	u, _ := url.Parse("https://generativelanguage.googleapis.invalid/v1beta/models")
	q := u.Query()
	q.Set("key", "AIzaSyGEHEIM-TESTKEY-987654321")
	u.RawQuery = q.Encode()
	err := fmt.Errorf("provider unreachable: %w",
		&url.Error{Op: "Get", URL: u.String(), Err: errors.New("dial tcp: no such host")})

	got := redactForLog(err)
	if strings.Contains(got, "AIzaSyGEHEIM") {
		t.Errorf("Key steht im Log-Text: %s", got)
	}
	// Die URL selbst MUSS erhalten bleiben — sie ist der diagnostische Wert,
	// wegen dem F-252 überhaupt loggt.
	if !strings.Contains(got, "generativelanguage.googleapis.invalid") {
		t.Errorf("Host verloren, Diagnose wertlos: %s", got)
	}
	if !strings.Contains(got, "key=[redigiert]") {
		t.Errorf("Redaktion nicht sichtbar gemacht: %s", got)
	}
}

func TestRedactString_WeitereGeheimeParameter(t *testing.T) {
	// Die Liste ist bewusst breiter als die heute bekannten Aufrufer — sie
	// soll den nächsten Provider mit abdecken, nicht nur den, der den Befund
	// ausgelöst hat.
	faelle := []string{"api_key", "access_token", "token", "client_secret", "password", "signature"}
	for _, p := range faelle {
		in := fmt.Sprintf(`Get "https://x.invalid/a?%s=SUPERGEHEIM&b=2": timeout`, p)
		got := redactString(in)
		if strings.Contains(got, "SUPERGEHEIM") {
			t.Errorf("%s nicht redigiert: %s", p, got)
		}
		if !strings.Contains(got, "b=2") {
			t.Errorf("%s: harmloser Parameter mitentfernt: %s", p, got)
		}
	}
}

func TestRedactString_MailadresseMaskiert(t *testing.T) {
	// Personenbezug, kein Geheimnis: Die Domain bleibt (ob die Zustellung an
	// einen bestimmten Anbieter scheitert, ist die gesuchte Information), der
	// lokale Teil identifiziert die Person und trägt zur Diagnose nichts bei.
	got := redactString("mail: rcpt patient@klinik.example: 550 unknown user")
	if strings.Contains(got, "patient@") {
		t.Errorf("Mailadresse unmaskiert: %s", got)
	}
	if !strings.Contains(got, "@klinik.example") {
		t.Errorf("Domain verloren, Diagnose wertlos: %s", got)
	}
	if !strings.Contains(got, "550 unknown user") {
		t.Errorf("Serverantwort verloren: %s", got)
	}
}

func TestRedactString_LogForgingUnterbunden(t *testing.T) {
	// Anhang-Dateinamen kommen ungefiltert durch: die Sanitizer entfernen nur
	// NUL und Pfadtrenner, keine Umbrüche. Wer einen Zeilenumbruch ins Log
	// schreiben kann, erfindet eine zweite, echt aussehende Logzeile.
	got := redactString("open a\nb.txt: aboard-server: 200 alles gut\r\n")
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Steuerzeichen durchgereicht: %q", got)
	}
	// Sichtbar ersetzt, nicht entfernt — der Versuch ist die interessante
	// Information.
	if !strings.Contains(got, `\n`) {
		t.Errorf("Umbruch spurlos verschwunden statt sichtbar ersetzt: %q", got)
	}
}

func TestWriteError_RedigiertBevorEsInsLogGeht(t *testing.T) {
	// Verdrahtungstest: Ohne ihn prüfte die Suite nur den Helfer, nie den
	// Aufrufer — genau die Lücke, die in diesem Projekt schon einmal einen
	// "Single Source of Truth"-Helfer unverdrahtet ließ.
	var puffer strings.Builder
	orig := stdlog.Writer()
	stdlog.SetOutput(&puffer)
	defer stdlog.SetOutput(orig)

	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadGateway, "provider unreachable",
		errors.New(`Get "https://x.invalid/a?key=AIzaSyGEHEIM123": timeout`))

	if strings.Contains(puffer.String(), "AIzaSyGEHEIM123") {
		t.Errorf("writeError loggt unredigiert: %s", puffer.String())
	}
	if strings.Contains(rec.Body.String(), "AIzaSyGEHEIM123") ||
		strings.Contains(rec.Body.String(), "x.invalid") {
		t.Errorf("interne Details in der Client-Antwort: %s", rec.Body.String())
	}
}

// Nachtrag aus dem Sprint-57-Verify-Lauf: Die erste Fassung der Redaktion
// prüfte auf `\b(token|...)=` — und `_` ist in Go-RE2 ein Wortzeichen, also
// steht zwischen `refresh_` und `token` keine Wortgrenze. `refresh_token=…`
// lief unredigiert durch, obwohl der Dateikopf ausdrücklich zusagt, den
// nächsten Provider mit abzudecken; bei OAuth ist genau dieser Parametername
// die Regel.
//
// Der Test hält BEIDE Richtungen fest, weil der erste Reparaturversuch die
// eine gegen die andere getauscht hat: mit optionalem Unterstrich wurde
// prompt `monkey=` redigiert (mon+key).
func TestRedactString_CompoundParameterUndKeineFehlalarme(t *testing.T) {
	redigiert := []string{"refresh_token", "oauth_token", "my_password", "user_api_key", "app_client_secret"}
	for _, p := range redigiert {
		got := redactString(p + "=SUPERGEHEIM&b=2")
		if strings.Contains(got, "SUPERGEHEIM") {
			t.Errorf("%s nicht redigiert: %s", p, got)
		}
		// Der volle Name bleibt stehen — im Log muss erkennbar sein, WELCHER
		// Parameter redigiert wurde.
		if !strings.Contains(got, p+"=[redigiert]") {
			t.Errorf("%s: Parametername verstümmelt: %s", p, got)
		}
	}
	unberuehrt := []string{"monkey", "assignee", "author", "board_key_visible", "turkey"}
	for _, p := range unberuehrt {
		got := redactString(p + "=harmlos&b=2")
		if !strings.Contains(got, "harmlos") {
			t.Errorf("Fehlalarm bei %s: %s", p, got)
		}
	}
}

// Der Verify-Lauf hat diese Lücke per Gegenprobe gefunden: Das Logging in
// writeSendError/writeSSEError liess sich ersatzlos entfernen, ohne dass ein
// einziger Test rot wurde. Der Code war richtig — aber gegen Regression
// unbewacht, und ein Refactor hätte ihn lautlos wieder verloren.
func TestWriteSendError_LoggtRedigiertUndHaeltDieAntwortGenerisch(t *testing.T) {
	var puffer strings.Builder
	orig := stdlog.Writer()
	stdlog.SetOutput(&puffer)
	defer stdlog.SetOutput(orig)

	rec := httptest.NewRecorder()
	writeSendError(rec, http.StatusInternalServerError, "internal", "create conversation failed",
		errors.New(`sqlite: disk I/O error at /var/lib/aboard/boards.db?key=AIzaGEHEIM`))

	if puffer.Len() == 0 {
		t.Fatal("writeSendError loggt gar nicht — F-672 ist offen")
	}
	if strings.Contains(puffer.String(), "AIzaGEHEIM") {
		t.Errorf("writeSendError loggt unredigiert: %s", puffer.String())
	}
	if !strings.Contains(puffer.String(), "disk I/O error") {
		t.Errorf("Diagnose-Information verloren: %s", puffer.String())
	}
	if strings.Contains(rec.Body.String(), "/var/lib/aboard") ||
		strings.Contains(rec.Body.String(), "AIzaGEHEIM") {
		t.Errorf("interne Details in der Client-Antwort: %s", rec.Body.String())
	}
}

// TestRedactString_AgentToken (Kritiker Sprint 71, K-A4).
func TestRedactString_AgentToken(t *testing.T) {
	tok := "abt_" + strings.Repeat("Ab9_-", 9)
	got := redactString("lookup fehlgeschlagen für " + tok + " (abt_ ist das Präfix)")
	if strings.Contains(got, tok[4:]) || !strings.Contains(got, "abt_[redigiert]") || !strings.Contains(got, "(abt_ ist das Präfix)") {
		t.Fatalf("redactString: %q", got)
	}
}
