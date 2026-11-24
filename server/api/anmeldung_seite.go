package api

// anmeldung_seite.go — die Anmeldeseite /anmeldung und die Auswahl der Wege
// (ADR-0149 §1, §2; Karte #1072).
//
// Die Seite wird vom Server gerendert und ruft NIE die Discovery eines
// Anbieters auf: sie hängt nicht davon ab, dass ein Anbieter erreichbar ist.
// Ein nicht erreichbarer Anbieter zeigt seine Meldung erst beim Klick
// (Start, 503); die anderen Wege bleiben nutzbar.

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// AnmeldeSeitenCSP gilt für alle Seiten, die der Server bei der Anmeldung
// selbst rendert (ADR-0149 §6). form-action 'self' (#1074): Formulare dieser
// Seiten senden nur an diesen Server — ein eingeschleustes Formular schickte
// das Passwort sonst anderswohin.
const AnmeldeSeitenCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'"

// AuthMethodsEnv wählt die angebotenen Anmeldewege (ADR-0149 §1): Kommaliste
// aus den Kürzeln der OIDC-Anbieter (`standard` für den Anbieter aus den
// bisherigen Variablen) und `password`. Leer = alle eingerichteten Wege.
const AuthMethodsEnv = "ABOARD_AUTH_METHODS"

// PasswortWeg ist der Wert für das Passwort-Formular (#1074). Er ist im
// eingebauten Betrieb immer eingerichtet (ADR-0149 §1).
const PasswortWeg = "password"

// PasswortAngeboten: Bietet ABOARD_AUTH_METHODS den Passwort-Weg an? Leer
// heißt alle eingerichteten Wege, also ja. Fehler im Wert meldet
// FiltereAnbieter.
func PasswortAngeboten(methoden string) bool {
	methoden = strings.TrimSpace(methoden)
	if methoden == "" {
		return true
	}
	for _, k := range strings.FieldsFunc(methoden, methodenTrenner) {
		if k == PasswortWeg {
			return true
		}
	}
	return false
}

func methodenTrenner(r rune) bool {
	return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
}

// FiltereAnbieter wendet ABOARD_AUTH_METHODS auf die eingerichteten Anbieter
// an. Ergebnis: die angebotenen Anbieter (in der Reihenfolge von alle) und
// die Fehler (alle auf einmal) — Tippfehler, Doppelte, und ein Wert ohne
// jeden Weg. `password` zählt als Weg, ist aber kein Anbieter (siehe
// PasswortAngeboten). Ein leerer Wert lässt alle durch.
func FiltereAnbieter(alle []AnbieterKonfig, methoden string) ([]AnbieterKonfig, []string) {
	methoden = strings.TrimSpace(methoden)
	if methoden == "" {
		return alle, nil
	}
	eingerichtet := make([]string, 0, len(alle)+1)
	eingerichtet = append(eingerichtet, PasswortWeg)
	for _, a := range alle {
		eingerichtet = append(eingerichtet, a.Kuerzel)
	}
	liste := strings.Join(eingerichtet, ", ")
	gewaehlt := map[string]bool{}
	var fehler []string
	for _, k := range strings.FieldsFunc(methoden, methodenTrenner) {
		switch {
		case gewaehlt[k]:
			fehler = append(fehler, fmt.Sprintf("%s: %q steht doppelt", AuthMethodsEnv, k))
		case k == PasswortWeg:
		default:
			gefunden := false
			for _, e := range eingerichtet {
				gefunden = gefunden || e == k
			}
			if !gefunden {
				fehler = append(fehler, fmt.Sprintf("%s: %q ist kein eingerichteter Anmeldeweg (eingerichtet: %s) — Tippfehler?",
					AuthMethodsEnv, k, liste))
			}
		}
		gewaehlt[k] = true
	}
	if len(gewaehlt) == 0 {
		fehler = append(fehler, fmt.Sprintf("%s=%q enthält keinen Anmeldeweg — leer lassen für alle eingerichteten (eingerichtet: %s)",
			AuthMethodsEnv, methoden, liste))
	}
	if len(fehler) > 0 {
		return nil, fehler
	}
	var out []AnbieterKonfig
	for _, a := range alle {
		if gewaehlt[a.Kuerzel] {
			out = append(out, a)
		}
	}
	return out, nil
}

// AnbieterMitAuswahl liest die Anbieter (AnbieterAusUmgebung) und filtert sie
// nach ABOARD_AUTH_METHODS. Gibt es schon Fehler beim Lesen der Anbieter,
// bleiben es nur diese — eine Filterliste gegen eine unvollständige Tabelle
// zu prüfen, ergäbe Folgefehler.
//
// Unbekannte ABOARD_AUTH_*- und ABOARD_INVITE_*-Namen kommen in beiden Fällen
// dazu (UnbekannteSchalter).
func AnbieterMitAuswahl(getenv func(string) string, environ []string) ([]AnbieterKonfig, []string, []string) {
	schalterFehler := UnbekannteSchalter(environ)
	alle, warnungen, fehler := AnbieterAusUmgebung(getenv, environ)
	if len(fehler) > 0 {
		return nil, warnungen, append(fehler, schalterFehler...)
	}
	gefiltert, fehler := FiltereAnbieter(alle, getenv(AuthMethodsEnv))
	if len(fehler) == 0 {
		// ABOARD_INVITE_METHODS: Teilmenge der angebotenen Wege (Karte #1077).
		_, ef := EinladungsWege(gefiltert, PasswortAngeboten(getenv(AuthMethodsEnv)), getenv(InviteMethodsEnv))
		fehler = append(fehler, ef...)
	}
	return gefiltert, warnungen, append(fehler, schalterFehler...)
}

// InviteMethodsEnv wählt die Wege beim Einlösen einer Einladung (ADR-0149
// §1, §5.4): Teilmenge der angebotenen Wege, leer = alle (EinladungsWege,
// Karte #1077).
const InviteMethodsEnv = "ABOARD_INVITE_METHODS"

// UnbekannteSchalter meldet jede nicht leere ABOARD_AUTH_*- und
// ABOARD_INVITE_*-Variable, die der Server nicht kennt (Kritiker
// Sprint-Abschluss 109, F-2878): Ein Tippfehler wie ABOARD_AUTH_METHOD
// schaltete sonst die Auswahl der Wege still ab — der Server böte alle
// Anbieter an, auch einen, den der Betreiber ausschließen wollte. Dieselbe
// Regel gilt für ABOARD_OIDC_* schon in AnbieterAusUmgebung.
func UnbekannteSchalter(environ []string) []string {
	var fremd []string
	for _, kv := range environ {
		name, w, _ := strings.Cut(kv, "=")
		if strings.TrimSpace(w) == "" {
			continue
		}
		switch {
		case name == AuthModeEnvVar || name == AuthMethodsEnv || name == InviteMethodsEnv:
		case strings.HasPrefix(name, "ABOARD_AUTH_") || strings.HasPrefix(name, "ABOARD_INVITE_"):
			fremd = append(fremd, name)
		}
	}
	sort.Strings(fremd)
	var fehler []string
	for _, name := range fremd {
		fehler = append(fehler, fmt.Sprintf("%s ist unbekannt — Tippfehler? (bekannt: %s, %s, %s)", name, AuthModeEnvVar, AuthMethodsEnv, InviteMethodsEnv))
	}
	return fehler
}

// ---------------------------------------------------------------------------
// Sprache

// WaehleSprache liest den Accept-Language-Header: die unterstützte Sprache
// (de, en) mit dem höchsten Qualitätswert gewinnt, bei Gleichstand die
// zuerst genannte; q=0 schließt aus. Rückfall: Deutsch.
func WaehleSprache(kopf string) string {
	beste, besteQ := "de", -1.0
	for _, teil := range strings.Split(kopf, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(teil), ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if v, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(p)), "q="); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					q = f
				} else {
					q = 0
				}
			}
		}
		haupt, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if (haupt == "de" || haupt == "en") && q > 0 && q > besteQ {
			beste, besteQ = haupt, q
		}
	}
	return beste
}

// seitenTexte sind die Texte der Anmeldeseite je Sprache.
type seitenTexte struct {
	Titel, Hinweis, Knopf, KeinWeg, Oder string
	// Passwort-Formular (#1074).
	Email, Passwort, Anmelden                             string
	Falsch, Warten, Gesperrt, Ueberlastet, FalscheEingabe string
	// Code-Schritt (#1075).
	Code, CodeHinweis, CodeFalsch, CodeZuOft, CodeAbgelaufen, CodeDefekt, NeuAnmelden string
	// Zustandsseiten und Einzelprüfung (F-2888, F-2893).
	Wartet, WartetText, KeinZugang, KeinZugangText, ZweiFaktorFehlt, ZweiFaktorFehltText, Laeuft string
	// Seiten des Anbieter-Wegs und Abgemeldet (F-2900, #1093).
	Abgelaufen, AbgelaufenText, NichtMoeglich, NichtMoeglichText, Abgebrochen, AbgebrochenText string
	Unvollstaendig, UnvollstaendigText, NichtErreichbar, NichtErreichbarText, Gescheitert      string
	GescheitertText, KeinIDToken, Abgewiesen, TokenUngueltig, TokenUnlesbar, KeineMail         string
	NichtFrei, AnderesKonto, Unbekannt, UnbekanntText, Abgemeldet, AbgemeldetText              string
}

// textSeite: kleine Seite in der Sprache der Anfrage; texte wählt Titel und
// Text aus den Texten dieser Sprache. neuAnmelden zeigt den Link zur
// Anmeldeseite (F-2893: vorher fest deutsch).
func textSeite(w http.ResponseWriter, r *http.Request, status int, texte func(seitenTexte) (string, string), neuAnmelden bool) {
	ziel := ""
	if neuAnmelden {
		ziel = AnmeldungSeitePfad
	}
	textSeiteZiel(w, r, status, texte, ziel)
}

// textSeiteZiel: wie textSeite, der Link „Neu anmelden“ zeigt auf ziel (leer =
// kein Link). ziel ist immer ein fester Pfad aus diesem Paket.
func textSeiteZiel(w http.ResponseWriter, r *http.Request, status int, texte func(seitenTexte) (string, string), ziel string) {
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	t := anmeldeTexte[lang]
	titel, text := texte(t)
	var links []seitenLink
	if ziel != "" {
		links = []seitenLink{{Text: t.NeuAnmelden, Ziel: ziel}}
	}
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Content-Language", lang)
	anmeldeSeiteAnsicht(w, status, seitenAnsicht{Lang: lang, Titel: titel, Text: text, Links: links})
}

// Zugriffe auf Titel und Text der Seiten des Anbieter-Wegs (F-2900).
var (
	sAbgelaufen      = func(t seitenTexte) (string, string) { return t.Abgelaufen, t.AbgelaufenText }
	sNichtMoeglich   = func(t seitenTexte) (string, string) { return t.NichtMoeglich, t.NichtMoeglichText }
	sAbgebrochen     = func(t seitenTexte) (string, string) { return t.Abgebrochen, t.AbgebrochenText }
	sUnvollstaendig  = func(t seitenTexte) (string, string) { return t.Unvollstaendig, t.UnvollstaendigText }
	sNichtErreichbar = func(t seitenTexte) (string, string) { return t.NichtErreichbar, t.NichtErreichbarText }
	sGescheitert     = func(t seitenTexte) (string, string) { return t.Gescheitert, t.GescheitertText }
	sKeinIDToken     = func(t seitenTexte) (string, string) { return t.Gescheitert, t.KeinIDToken }
	sTokenUngueltig  = func(t seitenTexte) (string, string) { return t.Abgewiesen, t.TokenUngueltig }
	sTokenUnlesbar   = func(t seitenTexte) (string, string) { return t.Abgewiesen, t.TokenUnlesbar }
	sWartet          = func(t seitenTexte) (string, string) { return t.Wartet, t.WartetText }
	sKeineMail       = func(t seitenTexte) (string, string) { return t.KeinZugang, t.KeineMail }
	sNichtFrei       = func(t seitenTexte) (string, string) { return t.KeinZugang, t.NichtFrei }
	sAnderesKonto    = func(t seitenTexte) (string, string) { return t.KeinZugang, t.AnderesKonto }
	sUnbekannt       = func(t seitenTexte) (string, string) { return t.Unbekannt, t.UnbekanntText }
	sAbgemeldet      = func(t seitenTexte) (string, string) { return t.Abgemeldet, t.AbgemeldetText }
)

var anmeldeTexte = map[string]seitenTexte{
	"de": {Titel: "Anmelden", Hinweis: "Wähle, wie du dich anmeldest.", Knopf: "Mit %s anmelden", Oder: "oder",
		KeinWeg: "Es ist kein Anmeldeweg eingerichtet. Bitte den Betreiber fragen.",
		Email:   "E-Mail-Adresse", Passwort: "Passwort", Anmelden: "Anmelden",
		Falsch:         "E-Mail-Adresse oder Passwort stimmt nicht.",
		Warten:         "Zu viele Versuche. Bitte %d Sekunden warten und dann erneut versuchen.",
		Gesperrt:       "Zu viele Fehlversuche von diesem Anschluss. Bitte in %d Minuten erneut versuchen.",
		Ueberlastet:    "Gerade melden sich sehr viele an. Bitte in ein paar Sekunden erneut versuchen.",
		FalscheEingabe: "Die Eingabe war unvollständig. Bitte erneut versuchen.",
		Code:           "Code",
		CodeHinweis:    "Gib den sechsstelligen Code aus deiner Authenticator-App ein, oder einen Wiederherstellungscode.",
		CodeFalsch:     "Der Code stimmt nicht oder wurde schon benutzt.",
		CodeZuOft:      "Zu viele falsche Codes. Bitte noch einmal mit dem Passwort anmelden.",
		CodeAbgelaufen: "Die Anmeldung ist abgelaufen. Bitte noch einmal mit dem Passwort anmelden.",
		CodeDefekt:     "Die Zwei-Faktor-Anmeldung dieses Zugangs ist nicht lesbar. Bitte den Admin fragen.",
		NeuAnmelden:    "Neu anmelden",
		Wartet:         "Wartet auf Bestätigung", WartetText: "Dein Zugang ist eingerichtet und wartet auf die Bestätigung durch den Admin.",
		KeinZugang: "Kein Zugang", KeinZugangText: "Dieser Zugang ist nicht freigeschaltet. Wer Zugang braucht, fragt den Betreiber.",
		ZweiFaktorFehlt: "Zwei-Faktor fehlt", ZweiFaktorFehltText: "Für diesen Zugang ist die Zwei-Faktor-Anmeldung nicht eingerichtet. Bitte den Admin fragen.",
		Laeuft:     "Gerade läuft schon eine Prüfung für diesen Zugang oder von diesem Anschluss. Bitte gleich noch einmal versuchen.",
		Abgelaufen: "Anmeldung abgelaufen", AbgelaufenText: "Der Anmeldevorgang ist nicht mehr gültig. Bitte neu anmelden. " +
			"Kamst du von einem Einladungslink, öffne ihn noch einmal — er gilt dann weiter.",
		NichtMoeglich: "Nicht möglich", NichtMoeglichText: "Dieser Anmeldeweg ist hier noch nicht verfügbar.",
		Abgebrochen: "Anmeldung abgebrochen", AbgebrochenText: "Die Anmeldung wurde beim Anmeldedienst abgebrochen.",
		Unvollstaendig: "Anmeldung unvollständig", UnvollstaendigText: "Es fehlt die Antwort des Anmeldedienstes.",
		NichtErreichbar: "Anmeldung gerade nicht möglich", NichtErreichbarText: "Der Anmeldedienst ist nicht erreichbar. Bitte in einer Minute erneut versuchen.",
		Gescheitert: "Anmeldung gescheitert", GescheitertText: "Der Anmeldedienst hat die Anmeldung nicht bestätigt. Bitte neu anmelden.",
		KeinIDToken: "Der Anmeldedienst hat kein ID-Token geliefert.",
		Abgewiesen:  "Anmeldung abgewiesen", TokenUngueltig: "Das Anmeldetoken ist ungültig.", TokenUnlesbar: "Das Anmeldetoken ist unlesbar.",
		KeineMail:    "Der Anmeldedienst hat keine bestätigte E-Mail-Adresse geliefert.",
		NichtFrei:    "Diese Adresse ist für dieses Board nicht freigeschaltet. Wer Zugang braucht, fragt den Betreiber.",
		AnderesKonto: "Diese Adresse gehört hier zu einem anderen Konto. Bitte den Betreiber fragen.",
		Unbekannt:    "Unbekannter Anmeldedienst", UnbekanntText: "Diesen Anmeldedienst gibt es hier nicht.",
		Abgemeldet: "Abgemeldet", AbgemeldetText: "Du bist von aBoard abgemeldet. Beim Anmeldedienst (z. B. Google) bist du womöglich noch angemeldet — " +
			"auf einem geteilten Rechner dort ebenfalls abmelden."},
	"en": {Titel: "Sign in", Hinweis: "Choose how to sign in.", Knopf: "Sign in with %s", Oder: "or",
		KeinWeg: "No way to sign in is set up. Please ask the administrator.",
		Email:   "Email address", Passwort: "Password", Anmelden: "Sign in",
		Falsch:         "Email address or password is incorrect.",
		Warten:         "Too many attempts. Please wait %d seconds and try again.",
		Gesperrt:       "Too many failed attempts from this connection. Please try again in %d minutes.",
		Ueberlastet:    "Many people are signing in right now. Please try again in a few seconds.",
		FalscheEingabe: "The form was incomplete. Please try again.",
		Code:           "Code",
		CodeHinweis:    "Enter the six-digit code from your authenticator app, or a recovery code.",
		CodeFalsch:     "The code is incorrect or has already been used.",
		CodeZuOft:      "Too many incorrect codes. Please sign in again with your password.",
		CodeAbgelaufen: "The sign-in has expired. Please sign in again with your password.",
		CodeDefekt:     "Two-factor sign-in for this account cannot be read. Please ask the administrator.",
		NeuAnmelden:    "Sign in again",
		Wartet:         "Waiting for confirmation", WartetText: "Your access is set up and waiting for an administrator to confirm it.",
		KeinZugang: "No access", KeinZugangText: "This account is not enabled. If you need access, ask the administrator.",
		ZweiFaktorFehlt: "Two-factor sign-in missing", ZweiFaktorFehltText: "Two-factor sign-in is not set up for this account. Please ask the administrator.",
		Laeuft:     "A check for this account or from this connection is already running. Please try again in a moment.",
		Abgelaufen: "Sign-in expired", AbgelaufenText: "The sign-in is no longer valid. Please sign in again. " +
			"If you came from an invitation link, open it again — it still works then.",
		NichtMoeglich: "Not possible", NichtMoeglichText: "This way of signing in is not available here yet.",
		Abgebrochen: "Sign-in cancelled", AbgebrochenText: "The sign-in was cancelled at the sign-in service.",
		Unvollstaendig: "Sign-in incomplete", UnvollstaendigText: "The response from the sign-in service is missing.",
		NichtErreichbar: "Sign-in not possible right now", NichtErreichbarText: "The sign-in service is not reachable. Please try again in a minute.",
		Gescheitert: "Sign-in failed", GescheitertText: "The sign-in service did not confirm the sign-in. Please sign in again.",
		KeinIDToken: "The sign-in service did not return an ID token.",
		Abgewiesen:  "Sign-in rejected", TokenUngueltig: "The sign-in token is invalid.", TokenUnlesbar: "The sign-in token cannot be read.",
		KeineMail:    "The sign-in service did not provide a confirmed email address.",
		NichtFrei:    "This address is not enabled for this board. If you need access, ask the administrator.",
		AnderesKonto: "This address belongs to a different account here. Please ask the administrator.",
		Unbekannt:    "Unknown sign-in service", UnbekanntText: "There is no such sign-in service here.",
		Abgemeldet: "Signed out", AbgemeldetText: "You are signed out of aBoard. You may still be signed in at the sign-in service (e.g. Google) — " +
			"on a shared computer, sign out there as well."},
}

// ---------------------------------------------------------------------------
// Seite

// startLink ist das Ziel des Knopfs von Anbieter k; konto reicht die
// Kontoauswahl (nach dem Abmelden) weiter.
func startLink(k string, konto bool) string {
	q := url.Values{AnbieterParam: {k}}
	if konto {
		q.Set(AnmeldungKontoWahlParam, "1")
	}
	return AnmeldungStartPfad + "?" + q.Encode()
}

// Seite: GET /anmeldung. Ist genau ein Weg übrig und ist es ein Anbieter →
// direkt dorthin (heutiges Verhalten von pm, ADR-0149 §2); sonst die Seite
// mit dem Passwort-Formular (wenn angeboten) und einem Knopf je Anbieter. Die
// Seite fragt die Discovery nie ab.
func (m *Anmeldungen) Seite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	konto := r.URL.Query().Get(AnmeldungKontoWahlParam) == "1"
	if len(m.Anbieter) == 1 && m.Passwort == nil {
		http.Redirect(w, r, startLink(m.Anbieter[0].kuerzel(), konto), http.StatusFound)
		return
	}
	m.seite(w, r, http.StatusOK, "", "")
}

// seite schreibt die Anmeldeseite mit allen Wegen; fehler steht über dem
// Passwort-Formular, email ist die zuletzt eingegebene Adresse.
func (m *Anmeldungen) seite(w http.ResponseWriter, r *http.Request, status int, fehler, email string) {
	konto := r.URL.Query().Get(AnmeldungKontoWahlParam) == "1"
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	t := anmeldeTexte[lang]
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Content-Language", lang)
	ans := seitenAnsicht{Lang: lang, Titel: t.Titel, Text: t.Hinweis, Oder: t.Oder}
	if len(m.Anbieter) == 0 && m.Passwort == nil {
		ans.Text = t.KeinWeg
	}
	if m.Passwort != nil {
		ans.Formular = passwortFormular(t, email, fehler)
	}
	for _, a := range m.Anbieter {
		ans.Links = append(ans.Links, seitenLink{Text: fmt.Sprintf(t.Knopf, a.label()), Ziel: startLink(a.kuerzel(), konto)})
	}
	anmeldeSeiteAnsicht(w, status, ans)
}

// seitenAnsicht beschreibt eine Anmeldeseite. Formular ist fertiges, vom
// Paket erzeugtes HTML (nie Eingaben von außen) und steht zwischen Text und
// Links.
type seitenAnsicht struct {
	Lang, Titel, Text, Formular string
	Links                       []seitenLink
	// Oder: Text des Trenners zwischen Formular und Anbieter-Knöpfen (leer =
	// kein Trenner; #1107).
	Oder string
}

// anmeldeLogo ist das aBoard-Zeichen (assets/aBoard-icon.svg) als Inline-SVG:
// Die CSP der Anmeldeseiten lädt nichts nach (default-src 'none'), Inline-SVG
// ist kein Abruf. Farben über die Klassen im Seiten-Stil (hell/dunkel).
const anmeldeLogo = `<svg viewBox="74 48 362 224" aria-hidden="true" focusable="false"><g transform="translate(40,40)" fill="none" stroke-width="18" stroke-linecap="round" stroke-linejoin="round" class="lm">` +
	`<rect x="120" y="20" width="260" height="200" rx="34"/><circle cx="86" cy="160" r="40"/><path d="M128 160 L128 70 Q128 30 168 30 L205 30"/>` +
	`<line x1="220" y1="50" x2="220" y2="220" stroke-width="6"/><line x1="300" y1="50" x2="300" y2="220" stroke-width="6"/></g>` +
	`<g transform="translate(40,40)"><rect x="158" y="66" width="42" height="30" rx="7" fill="#63D2B0"/><rect x="158" y="114" width="42" height="30" rx="7" class="lg"/>` +
	`<rect x="240" y="66" width="42" height="46" rx="7" fill="#E9C85D"/><rect x="240" y="132" width="42" height="38" rx="7" fill="#63D2B0"/>` +
	`<rect x="318" y="66" width="42" height="46" rx="7" class="lg"/><rect x="318" y="130" width="42" height="38" rx="7" fill="#E9C85D"/></g></svg>`

// anmeldeStil: gemeinsamer Stil aller Anmeldeseiten (#1107) — Karte in der
// Mitte, hell/dunkel nach Systemeinstellung, Anbieter als Knöpfe. Nur
// Inline-CSS (CSP style-src 'unsafe-inline').
const anmeldeStil = `:root{color-scheme:light dark;--bg:#eef2f5;--karte:#fff;--text:#1d2329;--leise:#5b6670;--rand:#d3dae0;--akzent:#0B5D73;--akzent-text:#fff;--fehler:#a8231b;--fehler-bg:#fdecea;--grau:#D9DEE3}` +
	`@media (prefers-color-scheme:dark){:root{--bg:#11161b;--karte:#1a2127;--text:#e6eaee;--leise:#9aa6b1;--rand:#2f3b45;--akzent:#4FB3C8;--akzent-text:#08191e;--fehler:#f4b9b4;--fehler-bg:#3b1f1c;--grau:#56626d}}` +
	`*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px 16px;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}` +
	`main{width:100%;max-width:400px;background:var(--karte);border:1px solid var(--rand);border-radius:14px;padding:28px 26px 22px;box-shadow:0 10px 30px rgba(0,0,0,.08)}` +
	`.marke{display:flex;align-items:center;gap:10px;margin:0 0 22px;font-weight:700;font-size:19px;letter-spacing:.2px}.marke svg{width:46px;height:auto}.lm{stroke:var(--akzent)}.lg{fill:var(--grau)}` +
	`h1{font-size:22px;line-height:1.25;margin:0 0 6px}main>p{color:var(--leise);margin:0 0 18px}` +
	`form p{margin:0 0 14px}label{display:block;font-weight:600;font-size:14px}` +
	`input{display:block;width:100%;margin-top:6px;padding:10px 12px;font:inherit;color:inherit;background:var(--bg);border:1px solid var(--rand);border-radius:8px}` +
	`input:focus{outline:2px solid var(--akzent);outline-offset:1px;border-color:var(--akzent)}` +
	`button,a.knopf{display:block;width:100%;padding:11px 14px;font:inherit;font-weight:600;text-align:center;text-decoration:none;border-radius:8px;cursor:pointer}` +
	`button{background:var(--akzent);color:var(--akzent-text);border:0}button:hover{filter:brightness(1.08)}` +
	`a.knopf{color:var(--text);background:transparent;border:1px solid var(--rand);margin:0 0 10px}a.knopf:hover{border-color:var(--akzent)}` +
	`button:focus-visible,a.knopf:focus-visible{outline:2px solid var(--akzent);outline-offset:2px}` +
	`.oder{display:flex;align-items:center;gap:12px;color:var(--leise);font-size:13px;margin:18px 0}.oder::before,.oder::after{content:"";flex:1;border-top:1px solid var(--rand)}` +
	`[role=alert]{background:var(--fehler-bg);color:var(--fehler);border-radius:8px;padding:10px 12px;margin:0 0 14px}[role=alert] strong{font-weight:600}` +
	`main a:not(.knopf){color:var(--akzent)}form svg{background:#fff;border-radius:6px}@media (max-width:420px){main{padding:22px 18px 16px;border-radius:12px}}`

// anmeldeSeiteAnsicht schreibt die Seite; Titel, Text und Links werden escaped.
func anmeldeSeiteAnsicht(w http.ResponseWriter, status int, a seitenAnsicht) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// frame-ancestors 'none': Diese Header-CSP ersetzt die aus withSecurityHeaders
	// (frame-ancestors 'self'); ohne die Direktive wären Anmeldeseiten einbettbar
	// (Sitzungs-Kritik 02.11. K1-1). Anmeldeseiten haben in keinem Rahmen etwas zu suchen.
	w.Header().Set("Content-Security-Policy", AnmeldeSeitenCSP)
	// F-2911 Teil 1: Diese Seiten laufen nicht durch WithDefaults.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	var lb strings.Builder
	if a.Oder != "" && a.Formular != "" && len(a.Links) > 0 {
		lb.WriteString(`<div class="oder">` + html.EscapeString(a.Oder) + `</div>`)
	}
	for _, l := range a.Links {
		lb.WriteString(`<a class="knopf" href="` + html.EscapeString(l.Ziel) + `">` + html.EscapeString(l.Text) + `</a>`)
	}
	fmt.Fprintf(w, `<!doctype html><html lang="%s"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s — aBoard</title><style>%s</style></head><body><main><div class="marke">%saBoard</div><h1>%s</h1><p>%s</p>%s%s</main></body></html>`,
		html.EscapeString(a.Lang), html.EscapeString(a.Titel), anmeldeStil, anmeldeLogo, html.EscapeString(a.Titel), html.EscapeString(a.Text), a.Formular, lb.String())
}
