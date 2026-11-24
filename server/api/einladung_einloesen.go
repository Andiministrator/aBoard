package api

// einladung_einloesen.go — Einladung einlösen (ADR-0149 §5.4, §5.5, Nachtrag
// #1077; Karte #1077).
//
//	GET  /einladung/<token>   Seite: eingeladene Adresse und die Wege aus
//	                          ABOARD_INVITE_METHODS
//	GET  /einladung/<token>?weg=<kürzel>
//	                          weiter zum Anbieter; eingelöst wird im
//	                          Rückruf (Zweck invite)
//	POST /einladung/<token>   weg=password: Passwort prüfen, 2FA einrichten
//	                          (Schritt 1), ersten Code prüfen und einlösen
//	                          (Schritt 2, schritt=code)
//
// Der Anbieter-Weg ist ein Link, kein Formular (Sitzungs-Kritik 03.11. A-1):
// Die Seite trägt CSP form-action 'self', und Chromium wendet form-action auch
// auf die Weiterleitung nach dem Absenden an — ein POST mit 302 zum Anbieter
// scheiterte dort still. Damit der Start nicht von fremden Seiten ausgelöst
// wird (vorher hielt das MitCSRFSchutz für den POST ab), startet er nur bei
// Sec-Fetch-Site same-origin/none oder ohne den Kopf (ältere Browser).
//
// Jede Antwort trägt Referrer-Policy: no-referrer (F-2895): Der Pfad enthält
// das Token, und ein Referer zum Anbieter oder zu einem Link gäbe es weiter.
//
// Zwischen den beiden Passwort-Schritten hält der Server nichts: Hash,
// Geheimnis, Token-Hash und Ablauf stehen versiegelt (secretcrypto.Box) in
// einem versteckten Feld. Erst Schritt 2 schreibt — atomar über
// EinladungRepo.Einloesen —, und erst dann entstehen die
// Wiederherstellungscodes, die genau einmal erscheinen.
//
// Nach dem Einlösen steht das Konto auf pending; der Admin bestätigt. Gibt es
// noch keinen Admin, gilt die Einladung als bestätigt (Nachtrag #1076): So
// kommt die erste Person aus der CLI herein und wird Admin.

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// einrichtungGueltig: so lange gilt Schritt 1 des Passwort-Wegs.
	einrichtungGueltig = 15 * time.Minute
	// einloeseFormularMax: größter angenommener Formular-Körper.
	einloeseFormularMax = 16 << 10
)

// EinloeseSpeicher ist der Teil von storage.EinladungRepo, den das Einlösen
// braucht.
type EinloeseSpeicher interface {
	Pruefen(ctx context.Context, token string) (storage.OffeneEinladung, error)
	Einloesen(ctx context.Context, tokenHash string, d storage.Einloesung) (storage.Eingeloest, error)
}

// EinloeseKonten ist der Teil von storage.KontoRepo, den das Einlösen braucht.
type EinloeseKonten interface {
	GibtAdmin(ctx context.Context) (bool, error)
	Bestaetigen(ctx context.Context, kontoID string, nutzerAnlegen func(ctx context.Context, email string) error, jetzt int64) (string, error)
}

// Siegel versiegelt und öffnet (secretcrypto.Box).
type Siegel interface {
	Seal(plaintext []byte) (string, error)
	Open(ciphertextB64 string) ([]byte, error)
}

// EinladungEinloesen bedient /einladung/<token>.
type EinladungEinloesen struct {
	Speicher EinloeseSpeicher
	Konten   EinloeseKonten
	// NutzerAnlegen legt bei der Bestätigung die users-Zeile an (BootstrapNewUser).
	NutzerAnlegen func(ctx context.Context, email string) error
	Box           Siegel
	// Anmeldungen: Anbieter-Tabelle (für die Knöpfe und den Start beim Anbieter).
	Anmeldungen *Anmeldungen
	// Wege: die wählbaren Wege (ABOARD_INVITE_METHODS, geprüft) — "password"
	// und Kürzel.
	Wege []string
	// Sitzungen: nach einer Bestätigung ohne Admin meldet der Anbieter-Weg
	// direkt an.
	Sitzungen AnmeldeSitzungen
	// NachAenderung: nach jeder Zustandsänderung eines Kontos (Tor vergessen).
	NachAenderung func(email, kontoID string)

	jetzt func() time.Time

	// ohneAdminMu reiht „gibt es einen Admin? — dann bestätigen“ (Sitzungs-
	// Kritik 03.11. A-5): Zwei gleichzeitige Einlösungen auf einem Server ohne
	// Admin sähen sonst beide „kein Admin“, und die zweite würde ohne
	// Bestätigung aktiv. Ein Prozess je Datenbank (ADR-0003), also genügt das.
	ohneAdminMu sync.Mutex
}

func (e *EinladungEinloesen) uhr() time.Time {
	if e.jetzt != nil {
		return e.jetzt()
	}
	return time.Now()
}

func (e *EinladungEinloesen) wegErlaubt(w string) bool {
	for _, x := range e.Wege {
		if x == w {
			return true
		}
	}
	return false
}

// einloeseKopf: für jede Antwort unter /einladung/ (auch Weiterleitungen).
func einloeseKopf(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// Texte der Einlöse-Seiten je Sprache.
type einloeseTexte struct {
	Titel, Einladung, Zurueckgesetzt, KeinWeg                         string
	Ungueltig, UngueltigText                                          string
	Passwort, PasswortWdh, PasswortKnopf, PasswortRegel, Ungleich     string
	ZuKurz, ZuLang, Ueberlastet, Knopf                                string
	EinrichtenTitel, EinrichtenText, Geheimnis, AppLink, Code, Weiter string
	QrHinweis, QrName                                                 string
	CodeFalsch, Abgelaufen                                            string
	FertigTitel, FertigWartet, FertigBestaetigt, Codes, CodesText     string
	Anmelden, Belegt, Wartet                                          string
}

var einloeseText = map[string]einloeseTexte{
	"de": {
		Titel: "Einladung", Einladung: "Einladung für %s. Wähle, wie du dich künftig anmeldest.",
		Zurueckgesetzt: "Dein Zugang %s wurde zurückgesetzt. Wähle, wie du dich künftig anmeldest.",
		KeinWeg:        "Für Einladungen ist kein Anmeldeweg eingerichtet. Bitte den Betreiber fragen.",
		Ungueltig:      "Link nicht gültig", UngueltigText: "Dieser Einladungslink ist unbekannt, abgelaufen oder schon benutzt. Bitte den Admin um einen neuen Link.",
		Passwort: "Passwort", PasswortWdh: "Passwort wiederholen", PasswortKnopf: "Passwort und Zwei-Faktor einrichten",
		PasswortRegel: "Mindestens %d Zeichen. Danach richtest du eine Authenticator-App ein.",
		Ungleich:      "Die beiden Passwörter stimmen nicht überein.",
		ZuKurz:        "Das Passwort braucht mindestens %d Zeichen.", ZuLang: "Das Passwort ist zu lang.",
		Ueberlastet:     "Gerade ist sehr viel los. Bitte in ein paar Sekunden erneut versuchen.",
		Knopf:           "Mit %s anmelden",
		EinrichtenTitel: "Authenticator-App einrichten",
		EinrichtenText:  "Lege in deiner Authenticator-App einen neuen Eintrag an: QR-Code scannen, den Link öffnen oder den Schlüssel eintippen. Gib dann den sechsstelligen Code aus der App ein.",
		QrHinweis:       "Mit der Authenticator-App scannen:", QrName: "QR-Code zum Einrichten der Authenticator-App",
		Geheimnis: "Schlüssel", AppLink: "In der Authenticator-App öffnen", Code: "Code aus der App", Weiter: "Einrichten abschließen",
		CodeFalsch:  "Der Code stimmt nicht. Prüfe die Uhrzeit des Handys und gib den aktuellen Code ein.",
		Abgelaufen:  "Die Einrichtung ist abgelaufen. Bitte den Einladungslink noch einmal öffnen.",
		FertigTitel: "Zugang eingerichtet", FertigWartet: "Dein Zugang ist eingerichtet und wartet auf die Bestätigung durch den Admin.",
		FertigBestaetigt: "Dein Zugang ist eingerichtet. Du kannst dich jetzt anmelden.",
		Codes:            "Wiederherstellungscodes",
		CodesText:        "Falls dein Handy fehlt, meldest du dich mit einem dieser Codes an; jeder gilt einmal. Schreib sie jetzt auf oder speichere sie sicher — sie erscheinen nicht noch einmal.",
		Anmelden:         "Anmelden",
		Belegt:           "Dieses Konto beim Anmeldedienst gehört hier schon zu einem anderen Zugang. Wähle einen anderen Weg oder frag den Admin.",
		Wartet:           "Wartet auf Bestätigung",
	},
	"en": {
		Titel: "Invitation", Einladung: "Invitation for %s. Choose how you will sign in.",
		Zurueckgesetzt: "Your access %s has been reset. Choose how you will sign in.",
		KeinWeg:        "No way to sign in is set up for invitations. Please ask the administrator.",
		Ungueltig:      "Link not valid", UngueltigText: "This invitation link is unknown, expired or already used. Please ask the administrator for a new link.",
		Passwort: "Password", PasswortWdh: "Repeat password", PasswortKnopf: "Set up password and two-factor sign-in",
		PasswortRegel: "At least %d characters. Next you set up an authenticator app.",
		Ungleich:      "The two passwords do not match.",
		ZuKurz:        "The password needs at least %d characters.", ZuLang: "The password is too long.",
		Ueberlastet:     "Very busy right now. Please try again in a few seconds.",
		Knopf:           "Sign in with %s",
		EinrichtenTitel: "Set up your authenticator app",
		EinrichtenText:  "Add a new entry in your authenticator app, by scanning the QR code, opening the link or typing the key. Then enter the six-digit code from the app.",
		QrHinweis:       "Scan with the authenticator app:", QrName: "QR code for setting up the authenticator app",
		Geheimnis: "Key", AppLink: "Open in authenticator app", Code: "Code from the app", Weiter: "Finish setup",
		CodeFalsch:  "The code is incorrect. Check your phone's clock and enter the current code.",
		Abgelaufen:  "The setup has expired. Please open the invitation link again.",
		FertigTitel: "Access set up", FertigWartet: "Your access is set up and waiting for an administrator to confirm it.",
		FertigBestaetigt: "Your access is set up. You can sign in now.",
		Codes:            "Recovery codes",
		CodesText:        "If your phone is missing, sign in with one of these codes; each works once. Write them down or store them safely now — they will not be shown again.",
		Anmelden:         "Sign in",
		Belegt:           "This account at the sign-in service already belongs to another user here. Choose another way or ask the administrator.",
		Wartet:           "Waiting for confirmation",
	},
}

func (e *EinladungEinloesen) texte(r *http.Request) (string, einloeseTexte) {
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	return lang, einloeseText[lang]
}

func einloeseSeite(w http.ResponseWriter, r *http.Request, status int, titel, text, formular string, links []seitenLink) {
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Content-Language", lang)
	anmeldeSeiteAnsicht(w, status, seitenAnsicht{Lang: lang, Titel: titel, Text: text, Formular: formular, Links: links})
}

func (e *EinladungEinloesen) ungueltig(w http.ResponseWriter, r *http.Request) {
	_, t := e.texte(r)
	einloeseSeite(w, r, http.StatusNotFound, t.Ungueltig, t.UngueltigText, "", nil)
}

// offen liest den Link; false = Seite „nicht gültig“ ist geschrieben.
func (e *EinladungEinloesen) offen(w http.ResponseWriter, r *http.Request) (string, storage.OffeneEinladung, bool) {
	token := r.PathValue("token")
	if !istEinladungToken(token) {
		e.ungueltig(w, r)
		return "", storage.OffeneEinladung{}, false
	}
	o, err := e.Speicher.Pruefen(r.Context(), token)
	if errors.Is(err, storage.ErrEinladungUnbekannt) || (err == nil && o.Zustand != storage.KontoEingeladen) {
		e.ungueltig(w, r)
		return "", storage.OffeneEinladung{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invitation lookup failed", err)
		return "", storage.OffeneEinladung{}, false
	}
	return token, o, true
}

// Seite: GET /einladung/{token}.
func (e *EinladungEinloesen) Seite(w http.ResponseWriter, r *http.Request) {
	einloeseKopf(w)
	token, o, ok := e.offen(w, r)
	if !ok {
		return
	}
	if weg := r.URL.Query().Get("weg"); weg != "" {
		a := e.Anmeldungen.Finde(weg)
		if weg == PasswortWeg || a == nil || !e.wegErlaubt(weg) {
			e.wahlSeite(w, r, http.StatusBadRequest, token, o, "")
			return
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
			a.startMitZweck(w, r, ZweckInvite, storage.EinladungTokenHash(token))
		default:
			// Von einer fremden Seite ausgelöst: nur die Auswahl zeigen.
			e.wahlSeite(w, r, http.StatusOK, token, o, "")
		}
		return
	}
	e.wahlSeite(w, r, http.StatusOK, token, o, "")
}

// wahlSeite: die Wege als Formulare; fehler steht über dem Passwort-Formular.
func (e *EinladungEinloesen) wahlSeite(w http.ResponseWriter, r *http.Request, status int, token string, o storage.OffeneEinladung, fehler string) {
	_, t := e.texte(r)
	ziel := html.EscapeString(EinladungPfadPraefix + token)
	text := fmt.Sprintf(t.Einladung, o.Email)
	if o.Zweck == storage.EinladungZweckReset {
		text = fmt.Sprintf(t.Zurueckgesetzt, o.Email)
	}
	var f strings.Builder
	if fehler != "" {
		f.WriteString(`<p role="alert"><strong>` + html.EscapeString(fehler) + `</strong></p>`)
	}
	angeboten := 0
	for _, weg := range e.Wege {
		if weg == PasswortWeg {
			angeboten++
			f.WriteString(`<form method="post" action="` + ziel + `"><input type="hidden" name="weg" value="password">` +
				`<p>` + html.EscapeString(fmt.Sprintf(t.PasswortRegel, PasswortMindestLaenge)) + `</p>` +
				`<p><label>` + html.EscapeString(t.Passwort) + `<br><input type="password" name="passwort" autocomplete="new-password" required minlength="` + fmt.Sprint(PasswortMindestLaenge) + `"></label></p>` +
				`<p><label>` + html.EscapeString(t.PasswortWdh) + `<br><input type="password" name="passwort2" autocomplete="new-password" required></label></p>` +
				`<p><button type="submit">` + html.EscapeString(t.PasswortKnopf) + `</button></p></form>`)
			continue
		}
		a := e.Anmeldungen.Finde(weg)
		if a == nil {
			continue
		}
		angeboten++
		f.WriteString(`<p><a href="` + ziel + `?weg=` + html.EscapeString(url.QueryEscape(a.kuerzel())) + `">` +
			html.EscapeString(fmt.Sprintf(t.Knopf, a.label())) + `</a></p>`)
	}
	if angeboten == 0 {
		text = t.KeinWeg
	}
	einloeseSeite(w, r, status, t.Titel, text, f.String(), nil)
}

// Post: POST /einladung/{token}.
func (e *EinladungEinloesen) Post(w http.ResponseWriter, r *http.Request) {
	einloeseKopf(w)
	token, o, ok := e.offen(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, einloeseFormularMax)
	if err := r.ParseForm(); err != nil {
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, "")
		return
	}
	weg := r.PostForm.Get("weg")
	if !e.wegErlaubt(weg) {
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, "")
		return
	}
	if weg != PasswortWeg {
		// Anbieter-Wege laufen über den Link (GET ?weg=), siehe Kopf.
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, "")
		return
	}
	if r.PostForm.Get("schritt") == "code" {
		e.passwortAbschliessen(w, r, token, o)
		return
	}
	e.passwortEinrichten(w, r, token, o)
}

// einrichtung steht versiegelt zwischen den beiden Passwort-Schritten im
// Formular.
type einrichtung struct {
	TokenHash string `json:"h"`
	Hash      string `json:"p"`
	Geheimnis string `json:"g"` // base64 (roh)
	Bis       int64  `json:"b"`
}

// passwortEinrichten: Schritt 1 — Passwort prüfen und hashen, Geheimnis
// erzeugen, die Seite zum Einrichten der App zeigen.
func (e *EinladungEinloesen) passwortEinrichten(w http.ResponseWriter, r *http.Request, token string, o storage.OffeneEinladung) {
	_, t := e.texte(r)
	pw, pw2 := r.PostForm.Get("passwort"), r.PostForm.Get("passwort2")
	if subtle.ConstantTimeCompare([]byte(pw), []byte(pw2)) != 1 {
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, t.Ungleich)
		return
	}
	hash, err := PasswortHashen(r.Context(), pw)
	switch {
	case errors.Is(err, ErrPasswortZuKurz):
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, fmt.Sprintf(t.ZuKurz, PasswortMindestLaenge))
		return
	case errors.Is(err, ErrPasswortZuLang):
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, t.ZuLang)
		return
	case errors.Is(err, ErrPasswortUeberlastet):
		w.Header().Set("Retry-After", "5")
		e.wahlSeite(w, r, http.StatusServiceUnavailable, token, o, t.Ueberlastet)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "password hash failed", err)
		return
	}
	neu, err := NeueTOTPEinrichtung(o.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa setup failed", err)
		return
	}
	roh, _ := json.Marshal(einrichtung{TokenHash: storage.EinladungTokenHash(token), Hash: hash,
		Geheimnis: base64.RawStdEncoding.EncodeToString(neu.Geheimnis), Bis: e.uhr().Add(einrichtungGueltig).Unix()})
	versiegelt, err := e.Box.Seal(roh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa setup failed", err)
		return
	}
	e.einrichtenSeite(w, r, http.StatusOK, token, o.Email, neu.Geheimnis, versiegelt, "")
}

// einrichtenSeite: Schlüssel, App-Link und das Feld für den ersten Code.
func (e *EinladungEinloesen) einrichtenSeite(w http.ResponseWriter, r *http.Request, status int, token, email string, geheimnis []byte, versiegelt, fehler string) {
	_, t := e.texte(r)
	text := totpBase32.EncodeToString(geheimnis)
	var f strings.Builder
	if fehler != "" {
		f.WriteString(`<p role="alert"><strong>` + html.EscapeString(fehler) + `</strong></p>`)
	}
	svg, err := qrSVG(totpLink(email, text), t.QrName, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa qr failed", err)
		return
	}
	f.WriteString(`<p>` + html.EscapeString(t.QrHinweis) + `</p><p>` + svg + `</p>`)
	f.WriteString(`<p>` + html.EscapeString(t.Geheimnis) + `: <code>` + html.EscapeString(gruppiere(text, 4)) + `</code></p>`)
	f.WriteString(`<p><a href="` + html.EscapeString(totpLink(email, text)) + `">` + html.EscapeString(t.AppLink) + `</a></p>`)
	f.WriteString(`<form method="post" action="` + html.EscapeString(EinladungPfadPraefix+token) + `">` +
		`<input type="hidden" name="weg" value="password"><input type="hidden" name="schritt" value="code">` +
		`<input type="hidden" name="einrichtung" value="` + html.EscapeString(versiegelt) + `">` +
		`<p><label>` + html.EscapeString(t.Code) + `<br><input type="text" name="code" inputmode="numeric" autocomplete="one-time-code" required autofocus></label></p>` +
		`<p><button type="submit">` + html.EscapeString(t.Weiter) + `</button></p></form>`)
	einloeseSeite(w, r, status, t.EinrichtenTitel, t.EinrichtenText, f.String(), nil)
}

// passwortAbschliessen: Schritt 2 — ersten Code prüfen, Codes erzeugen,
// einlösen.
func (e *EinladungEinloesen) passwortAbschliessen(w http.ResponseWriter, r *http.Request, token string, o storage.OffeneEinladung) {
	_, t := e.texte(r)
	versiegelt := r.PostForm.Get("einrichtung")
	var ein einrichtung
	roh, err := e.Box.Open(versiegelt)
	if err != nil || json.Unmarshal(roh, &ein) != nil ||
		subtle.ConstantTimeCompare([]byte(ein.TokenHash), []byte(storage.EinladungTokenHash(token))) != 1 ||
		e.uhr().Unix() >= ein.Bis || ein.Hash == "" {
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, t.Abgelaufen)
		return
	}
	geheimnis, err := base64.RawStdEncoding.DecodeString(ein.Geheimnis)
	if err != nil || len(geheimnis) == 0 {
		e.wahlSeite(w, r, http.StatusBadRequest, token, o, t.Abgelaufen)
		return
	}
	schritt, ok := TOTPPasst(geheimnis, r.PostForm.Get("code"), e.uhr(), 0)
	if !ok {
		e.einrichtenSeite(w, r, http.StatusUnprocessableEntity, token, o.Email, geheimnis, versiegelt, t.CodeFalsch)
		return
	}
	codes, hashes, err := neueWiederherstellungscodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recovery codes failed", err)
		return
	}
	g, err := e.Box.Seal(geheimnis)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa setup failed", err)
		return
	}
	erg, err := e.Speicher.Einloesen(r.Context(), ein.TokenHash, storage.Einloesung{
		PasswortHash: ein.Hash, TOTPVersiegelt: g, TOTPSchritt: schritt, CodeHashes: hashes,
	})
	if !e.einloeseFehler(w, r, err) {
		return
	}
	bestaetigt, ok := e.bestaetigeOhneAdmin(w, r, erg)
	if !ok {
		return
	}
	text := t.FertigWartet
	var links []seitenLink
	if bestaetigt {
		text = t.FertigBestaetigt
		links = []seitenLink{{Text: t.Anmelden, Ziel: AnmeldungSeitePfad}}
	}
	var f strings.Builder
	f.WriteString(`<h2>` + html.EscapeString(t.Codes) + `</h2><p>` + html.EscapeString(t.CodesText) + `</p><ul>`)
	for _, c := range codes {
		f.WriteString(`<li><code>` + html.EscapeString(c) + `</code></li>`)
	}
	f.WriteString(`</ul>`)
	einloeseSeite(w, r, http.StatusOK, t.FertigTitel, text, f.String(), links)
}

var einloeseLogZuletzt atomic.Int64

// einloeseFehler beantwortet einen Fehler von Einloesen; true = kein Fehler.
func (e *EinladungEinloesen) einloeseFehler(w http.ResponseWriter, r *http.Request, err error) bool {
	_, t := e.texte(r)
	switch {
	case err == nil:
		return true
	case errors.Is(err, storage.ErrEinladungUnbekannt):
		e.ungueltig(w, r)
	case errors.Is(err, storage.ErrBindungBelegt):
		einloeseSeite(w, r, http.StatusConflict, t.Titel, t.Belegt, "", nil)
	case errors.Is(err, storage.ErrDBBelegt):
		w.Header().Set("Retry-After", "2")
		einloeseSeite(w, r, http.StatusServiceUnavailable, t.Titel, t.Ueberlastet, "", nil)
	default:
		writeError(w, http.StatusInternalServerError, "redeem failed", err)
	}
	return false
}

// bestaetigeOhneAdmin: Gibt es noch keinen Admin, gilt die eingelöste
// Einladung als bestätigt (Nachtrag #1076). false = Antwort geschrieben.
func (e *EinladungEinloesen) bestaetigeOhneAdmin(w http.ResponseWriter, r *http.Request, erg storage.Eingeloest) (bool, bool) {
	if e.NachAenderung != nil {
		defer e.NachAenderung(erg.Email, erg.KontoID)
	}
	e.ohneAdminMu.Lock()
	defer e.ohneAdminMu.Unlock()
	gibt, err := e.Konten.GibtAdmin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin check failed", err)
		return false, false
	}
	if gibt {
		return false, true
	}
	// F-2909 (#1081): Mit ABOARD_BOOTSTRAP_OWNER wird nur diese Adresse Erst-Admin
	// (BootstrapNewUser, F-1855). Jede andere Einladung wartet dann auf die
	// Bestätigung, sonst wäre bis zum Einlösen des Owners — bei ungültigem Wert
	// für immer — jede CLI-Einladung ohne Bestätigung aktiv.
	if owner, gesetzt, ok := storage.BootstrapOwnerAusEnv(); gesetzt && (!ok || owner != storage.NormalizeEmail(erg.Email)) {
		return false, true
	}
	if _, err := e.Konten.Bestaetigen(r.Context(), erg.KontoID, e.NutzerAnlegen, e.uhr().Unix()); err != nil {
		// Eingelöst ist trotzdem; das Konto wartet, bis jemand bestätigt.
		if drosselErlaubt(&einloeseLogZuletzt, time.Now()) {
			log.Printf("aboard-server: einladung: Bestätigung ohne Admin gescheitert (konten/%s): %s", erg.KontoID, redactForLog(err))
		}
		return false, true
	}
	log.Printf("aboard-server: einladung: konten/%s eingelöst und ohne Admin bestätigt (erste Person)", erg.KontoID)
	return true, true
}

// anbieterAbschliessen: Rückruf mit Zweck invite (anmeldung_oidc.go). Bindet
// (iss, sub) an das eingeladene Konto, auch bei abweichender gemeldeter
// Adresse (ADR-0149 §5.4).
func (e *EinladungEinloesen) anbieterAbschliessen(ctx context.Context, w http.ResponseWriter, r *http.Request,
	tokenHash, kuerzel, iss, sub, gemeldet, name string, gemeldetUnbestaetigt bool) {
	einloeseKopf(w)
	_, t := e.texte(r)
	if !e.wegErlaubt(kuerzel) {
		e.ungueltig(w, r)
		return
	}
	erg, err := e.Speicher.Einloesen(ctx, tokenHash, storage.Einloesung{
		Anbieter: kuerzel, Iss: iss, Sub: sub, GemeldeteEmail: gemeldet, GemeldeterName: name, GemeldeteEmailUnbestaetigt: gemeldetUnbestaetigt,
	})
	if !e.einloeseFehler(w, r, err) {
		return
	}
	bestaetigt, ok := e.bestaetigeOhneAdmin(w, r.WithContext(ctx), erg)
	if !ok {
		return
	}
	if !bestaetigt || e.Sitzungen == nil {
		einloeseSeite(w, r, http.StatusOK, t.Wartet, t.FertigWartet, "", nil)
		return
	}
	if !beginneSitzung(w, r.WithContext(ctx), e.Sitzungen, erg.Email, name) {
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// ---------------------------------------------------------------------------
// Auswahl der Wege beim Einlösen (ABOARD_INVITE_METHODS)

// EinladungsWege: die wählbaren Wege beim Einlösen. angeboten sind die
// angebotenen Anbieter (nach ABOARD_AUTH_METHODS), passwort sagt, ob der
// Passwort-Weg angeboten wird. invite leer = alle angebotenen Wege; sonst eine
// Teilmenge davon (ADR-0149 §1), Abweichungen sind Fehler.
func EinladungsWege(angeboten []AnbieterKonfig, passwort bool, invite string) ([]string, []string) {
	var alle []string
	if passwort {
		alle = append(alle, PasswortWeg)
	}
	for _, a := range angeboten {
		alle = append(alle, a.Kuerzel)
	}
	invite = strings.TrimSpace(invite)
	if invite == "" {
		return alle, nil
	}
	liste := strings.Join(alle, ", ")
	gewaehlt := map[string]bool{}
	var fehler []string
	var out []string
	for _, k := range strings.FieldsFunc(invite, methodenTrenner) {
		gefunden := false
		for _, a := range alle {
			gefunden = gefunden || a == k
		}
		switch {
		case gewaehlt[k]:
			fehler = append(fehler, fmt.Sprintf("%s: %q steht doppelt", InviteMethodsEnv, k))
		case !gefunden:
			fehler = append(fehler, fmt.Sprintf("%s: %q ist kein angebotener Anmeldeweg (angeboten: %s) — die Wege beim Einlösen "+
				"müssen eine Teilmenge von %s sein", InviteMethodsEnv, k, liste, AuthMethodsEnv))
		default:
			out = append(out, k)
		}
		gewaehlt[k] = true
	}
	if len(gewaehlt) == 0 {
		fehler = append(fehler, fmt.Sprintf("%s=%q enthält keinen Weg — leer lassen für alle angebotenen (%s)", InviteMethodsEnv, invite, liste))
	}
	if len(fehler) > 0 {
		return nil, fehler
	}
	// Reihenfolge wie angeboten (Passwort zuerst, dann die Anbieter).
	var sortiert []string
	for _, a := range alle {
		for _, k := range out {
			if a == k {
				sortiert = append(sortiert, a)
			}
		}
	}
	return sortiert, nil
}
