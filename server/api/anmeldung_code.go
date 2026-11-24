package api

// anmeldung_code.go — zweiter Schritt der Passwort-Anmeldung: 2FA- oder
// Wiederherstellungscode (ADR-0149 §3, Kritik W5; Karte #1075).
//
// Nach richtigem Passwort setzt der Server ein eigenes Cookie „Passwort ok,
// Code fehlt“ (ZweiterSchrittCookie): signiert (HMAC mit einem eigenen, aus
// ABOARD_SECRET_KEY abgeleiteten Schlüssel), fünf Minuten gültig, und nur
// einmal verwendbar — die Kennung im Cookie steht zusätzlich im Speicher des
// Servers und wird beim Erfolg, nach zu vielen falschen Codes und beim Ablauf
// gestrichen. Ein Neustart verwirft alle offenen zweiten Schritte (dann eben
// noch einmal das Passwort).
//
// Schranken für Codes:
//   - höchstens zweiterSchrittMaxFehler falsche Codes je zweitem Schritt,
//     danach von vorn mit dem Passwort;
//   - jeder falsche Code zählt in die Bremse der Gegenstelle (dieselbe wie
//     beim Passwort) und in die Wartezeit der Adresse. Die Wartezeit setzt
//     erst ein vollständiger Erfolg zurück, nicht schon das richtige
//     Passwort — sonst setzte jedes richtige Passwort den Zähler der Codes
//     zurück.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// ZweiterSchrittCookie trägt „Passwort ok, Code fehlt“.
	ZweiterSchrittCookie = "__Host-aboard_zweiter_schritt"
	// zweiterSchrittGueltig: so lange gilt der zweite Schritt.
	zweiterSchrittGueltig = 5 * time.Minute
	// zweiterSchrittMaxFehler: falsche Codes je zweitem Schritt.
	zweiterSchrittMaxFehler = 5
	// zweiterSchrittMaxOffen begrenzt den Speicher.
	zweiterSchrittMaxOffen = 10000
)

// ZweiterSchrittSchluessel leitet den Signierschlüssel des Zwischen-Cookies
// aus ABOARD_SECRET_KEY ab (eigener Zweck, nie derselbe wie beim
// Anmelde-Cookie der Anbieter).
func ZweiterSchrittSchluessel(masterKey string) []byte {
	m := hmac.New(sha256.New, []byte(strings.TrimSpace(masterKey)))
	m.Write([]byte("aboard-zweiter-schritt-cookie-v1"))
	return m.Sum(nil)
}

// TOTPKonten ist der Teil von storage.KontoRepo, den der Code-Schritt braucht.
type TOTPKonten interface {
	TOTPGeheimnis(ctx context.Context, kontoID string) (string, int64, error)
	TOTPSchrittVerbrauchen(ctx context.Context, kontoID string, schritt int64) (bool, error)
	CodeVerbrauchen(ctx context.Context, kontoID, codeHash string, jetzt int64) (bool, error)
}

// Oeffner entschlüsselt ein versiegeltes Geheimnis (secretcrypto.Box).
type Oeffner interface {
	Open(ciphertextB64 string) ([]byte, error)
}

// zweiterSchritt: offene zweite Schritte, Schlüssel ist die Kennung im Cookie.
type zweiterSchritt struct {
	mu    sync.Mutex
	offen map[string]*offenerSchritt
	// jeKonto: je Konto höchstens ein offener zweiter Schritt (F-2888) — ein
	// neuer ersetzt den alten. Sonst füllte, wer ein Passwort kennt, die
	// Tabelle bis zur Grenze, und alle anderen bekämen „überlastet“.
	jeKonto map[string]string
}

type offenerSchritt struct {
	kontoID string
	bis     time.Time
	fehler  int
}

// zwischenWert steht signiert im Cookie.
type zwischenWert struct {
	Kennung string `json:"n"`
	KontoID string `json:"k"`
	Email   string `json:"m"`
	Bis     int64  `json:"e"`
}

// MitZweitemSchritt schaltet den Code-Schritt ein: Nach richtigem Passwort
// verlangt die Anmeldung den Code; erst danach entsteht über beginneSitzung
// eine Sitzung.
func (p *PasswortAnmeldung) MitZweitemSchritt(totp TOTPKonten, box Oeffner, schluessel []byte, sitzungen AnmeldeSitzungen) error {
	if totp == nil || box == nil || len(schluessel) == 0 || sitzungen == nil {
		return errors.New("zweiter schritt: totp, box, schlüssel und sitzungen sind pflicht")
	}
	p.totp, p.box, p.schluessel, p.sitzungen = totp, box, schluessel, sitzungen
	p.zweiter = &zweiterSchritt{offen: map[string]*offenerSchritt{}, jeKonto: map[string]string{}}
	p.NachPasswort = p.beginneZweitenSchritt
	return nil
}

func (p *PasswortAnmeldung) uhr() time.Time {
	if p.jetzt != nil {
		return p.jetzt()
	}
	return time.Now()
}

func (p *PasswortAnmeldung) signiere(teil string) string {
	m := hmac.New(sha256.New, p.schluessel)
	m.Write([]byte(teil))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func setzeZwischenCookie(w http.ResponseWriter, wert string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: ZweiterSchrittCookie, Value: wert, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// beginneZweitenSchritt: NachPasswort im Betrieb. Ohne eingerichtetes 2FA gibt
// es keine Sitzung (Pflicht für Passwort-Konten).
func (p *PasswortAnmeldung) beginneZweitenSchritt(w http.ResponseWriter, r *http.Request, k storage.KontoAnmeldung) {
	if !k.HatTOTP {
		textSeite(w, r, http.StatusForbidden, func(t seitenTexte) (string, string) { return t.ZweiFaktorFehlt, t.ZweiFaktorFehltText }, false)
		return
	}
	kennung, err := zufall()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "random failed", err)
		return
	}
	jetzt := p.uhr()
	bis := jetzt.Add(zweiterSchrittGueltig)
	z := p.zweiter
	z.mu.Lock()
	if len(z.offen) >= zweiterSchrittMaxOffen {
		z.aufraeumen(jetzt)
	}
	// Ersetzt der neue Schritt einen alten desselben Kontos, wächst die
	// Tabelle nicht — dann nie 503. Den alten erst nach der Prüfung streichen
	// (Verify N5): Bei Überlast bleibt er gültig.
	alt, hatAlt := z.jeKonto[k.ID]
	if !hatAlt && len(z.offen) >= zweiterSchrittMaxOffen {
		z.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		textSeite(w, r, http.StatusServiceUnavailable, func(t seitenTexte) (string, string) { return t.Titel, t.Ueberlastet }, true)
		return
	}
	if hatAlt {
		z.streiche(alt)
	}
	z.offen[kennung] = &offenerSchritt{kontoID: k.ID, bis: bis}
	z.jeKonto[k.ID] = kennung
	z.mu.Unlock()

	roh, _ := json.Marshal(zwischenWert{Kennung: kennung, KontoID: k.ID, Email: k.Email, Bis: bis.Unix()})
	teil := base64.RawURLEncoding.EncodeToString(roh)
	setzeZwischenCookie(w, teil+"."+p.signiere(teil), int(zweiterSchrittGueltig.Seconds()))
	p.codeSeite(w, r, http.StatusOK, "")
}

// aufraeumen (unter mu): abgelaufene zweite Schritte weg.
func (z *zweiterSchritt) aufraeumen(jetzt time.Time) {
	for k, o := range z.offen {
		if !jetzt.Before(o.bis) {
			delete(z.offen, k)
			if z.jeKonto[o.kontoID] == k {
				delete(z.jeKonto, o.kontoID)
			}
		}
	}
}

var errZwischen = errors.New("zweiter schritt fehlt, ist verfälscht, abgelaufen oder verbraucht")

// zwischenLesen prüft Signatur, Ablauf und die offene Kennung im Speicher.
func (p *PasswortAnmeldung) zwischenLesen(r *http.Request) (zwischenWert, error) {
	var v zwischenWert
	c, err := r.Cookie(ZweiterSchrittCookie)
	if err != nil || c.Value == "" {
		return v, errZwischen
	}
	teil, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(p.signiere(teil))) {
		return v, errZwischen
	}
	roh, err := base64.RawURLEncoding.DecodeString(teil)
	if err != nil || json.Unmarshal(roh, &v) != nil || v.Kennung == "" || v.KontoID == "" || v.Email == "" {
		return v, errZwischen
	}
	jetzt := p.uhr()
	if jetzt.Unix() >= v.Bis {
		return v, errZwischen
	}
	z := p.zweiter
	z.mu.Lock()
	defer z.mu.Unlock()
	o := z.offen[v.Kennung]
	if o == nil || o.kontoID != v.KontoID || !jetzt.Before(o.bis) {
		return v, errZwischen
	}
	return v, nil
}

// zwischenReservieren zählt den Versuch VOR dem Prüfen (F-2888): So gelten
// die fünf Versuche je zweitem Schritt auch, wenn Anfragen sich überholen.
// false = Schritt unbekannt oder schon verbraucht.
func (p *PasswortAnmeldung) zwischenReservieren(kennung string) bool {
	z := p.zweiter
	z.mu.Lock()
	defer z.mu.Unlock()
	o := z.offen[kennung]
	if o == nil {
		return false
	}
	if o.fehler >= zweiterSchrittMaxFehler {
		z.streiche(kennung)
		return false
	}
	o.fehler++
	return true
}

// zwischenErschoepft: nach einem falschen Code — true = keine Versuche mehr,
// der Schritt ist gestrichen.
func (p *PasswortAnmeldung) zwischenErschoepft(kennung string) bool {
	z := p.zweiter
	z.mu.Lock()
	defer z.mu.Unlock()
	o := z.offen[kennung]
	if o == nil || o.fehler >= zweiterSchrittMaxFehler {
		z.streiche(kennung)
		return true
	}
	return false
}

// streiche (unter mu) entfernt einen offenen Schritt samt Kontozuordnung.
func (z *zweiterSchritt) streiche(kennung string) {
	if o := z.offen[kennung]; o != nil && z.jeKonto[o.kontoID] == kennung {
		delete(z.jeKonto, o.kontoID)
	}
	delete(z.offen, kennung)
}

// zwischenEnde streicht die Kennung; true = sie war noch offen (nur ein
// gleichzeitiger zweiter Erfolg findet sie nicht mehr).
func (p *PasswortAnmeldung) zwischenEnde(kennung string) bool {
	z := p.zweiter
	z.mu.Lock()
	defer z.mu.Unlock()
	_, ok := z.offen[kennung]
	z.streiche(kennung)
	return ok
}

// SchrittVerwerfen streicht den offenen zweiten Schritt eines Kontos (nach
// Sperren oder Zurücksetzen, Karte #1077). Der Code-Schritt fragt Zustand und
// Tor zwar vor der Sitzung noch einmal, aber ein offener Schritt soll nicht
// bis zu fünf Minuten weiter gelten.
func (p *PasswortAnmeldung) SchrittVerwerfen(kontoID string) {
	if p == nil || p.zweiter == nil {
		return
	}
	z := p.zweiter
	z.mu.Lock()
	defer z.mu.Unlock()
	if k, ok := z.jeKonto[kontoID]; ok {
		z.streiche(k)
	}
}

var codeLogZuletzt atomic.Int64

// CodePost: POST /anmeldung/code (über Anmeldungen).
func (m *Anmeldungen) CodePost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := m.Passwort
	if p == nil || p.zweiter == nil {
		writeError(w, http.StatusNotFound, "not available")
		return
	}
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	t := anmeldeTexte[lang]
	neu := func(text string) {
		setzeZwischenCookie(w, "", -1)
		textSeite(w, r, http.StatusUnauthorized, func(t seitenTexte) (string, string) { return t.Titel, text }, true)
	}
	v, err := p.zwischenLesen(r)
	if err != nil {
		neu(t.CodeAbgelaufen)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, passwortFormularMax)
	if err := r.ParseForm(); err != nil {
		p.codeSeite(w, r, http.StatusBadRequest, t.FalscheEingabe)
		return
	}
	adresse := p.gegenstelle.proxy.clientAdresse(r)
	schluessel := "a:" + adresse
	// Verify N1: erst lesend prüfen, dann belegen, dann noch einmal. Wie in
	// PasswortPost (F-2911 Teil 2): Eine abweisende Vorprüfung beantwortet die
	// Anfrage und kehrt zurück. Früher fiel sie ohne Belegen durch zu den
	// Zweitprüfungen; lief die Wartezeit genau dazwischen ab, prüfte die Anfrage
	// den Code ohne die Belegung (und damit ohne Zähler je Zwischen-Kennung, F-2888).
	abgewiesen := func() bool {
		if gesperrt, rest := p.gegenstelle.gesperrt(schluessel); gesperrt {
			w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
			p.zwischenEnde(v.Kennung)
			setzeZwischenCookie(w, "", -1)
			textSeite(w, r, http.StatusTooManyRequests, func(t seitenTexte) (string, string) {
				return t.Titel, fmt.Sprintf(t.Gesperrt, int(rest.Minutes())+1)
			}, true)
			return true
		}
		if rest := p.adressen.warten(v.Email, adresse); rest > 0 {
			sek := int(rest.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(sek))
			p.codeSeite(w, r, http.StatusTooManyRequests, fmt.Sprintf(t.Warten, sek))
			return true
		}
		return false
	}
	if abgewiesen() {
		return
	}
	frei, freiOK := p.belege("g:"+adresse, "z:"+v.Kennung, "m:"+adressSchluessel(v.Email))
	if !freiOK {
		w.Header().Set("Retry-After", "1")
		p.codeSeite(w, r, http.StatusTooManyRequests, t.Laeuft)
		return
	}
	defer frei()
	if abgewiesen() {
		return
	}

	if !p.zwischenReservieren(v.Kennung) {
		neu(t.CodeZuOft)
		return
	}
	ok, err := p.codePruefen(r.Context(), v.KontoID, r.PostForm.Get("code"))
	if err != nil {
		if drosselErlaubt(&codeLogZuletzt, time.Now()) {
			log.Printf("aboard-server: anmeldung: 2FA-Prüfung gescheitert: %s — wurde ABOARD_SECRET_KEY gewechselt? "+
				"Dann die Person zurücksetzen (neue Einladung). Höchstens eine Zeile pro Minute", redactForLog(err))
		}
		p.zwischenEnde(v.Kennung)
		setzeZwischenCookie(w, "", -1)
		textSeite(w, r, http.StatusInternalServerError, func(t seitenTexte) (string, string) { return t.Titel, t.CodeDefekt }, false)
		return
	}
	if !ok {
		p.gegenstelle.fehlschlag(schluessel)
		p.adressen.fehlschlag(v.Email)
		if p.zwischenErschoepft(v.Kennung) {
			neu(t.CodeZuOft)
			return
		}
		p.codeSeite(w, r, http.StatusUnauthorized, t.CodeFalsch)
		return
	}
	if !p.zwischenEnde(v.Kennung) {
		neu(t.CodeAbgelaufen)
		return
	}
	setzeZwischenCookie(w, "", -1)
	p.adressen.erfolg(v.Email, adresse)

	// Zwischen Passwort und Code kann das Konto gesperrt worden sein: Zustand
	// und Tor noch einmal fragen, dann erst die Sitzung.
	k, err := p.Konten.FuerAnmeldung(r.Context(), v.Email)
	if err != nil && !errors.Is(err, storage.ErrKontoUnbekannt) {
		writeError(w, http.StatusInternalServerError, "login lookup failed", err)
		return
	}
	erlaubt := false
	if err == nil && k.ID == v.KontoID && k.Zustand == storage.KontoAktiv {
		if erlaubt, err = p.Tor(r.Context(), k.Email); err != nil {
			writeError(w, http.StatusInternalServerError, "access check failed", err)
			return
		}
	}
	if !erlaubt {
		textSeite(w, r, http.StatusForbidden, func(t seitenTexte) (string, string) { return t.KeinZugang, t.KeinZugangText }, false)
		return
	}
	if !beginneSitzung(w, r, p.sitzungen, k.Email, "") {
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// codePruefen: sechs Ziffern → TOTP, sonst Wiederherstellungscode. Fehler nur
// bei Speicher- oder Entschlüsselungsproblemen, nie bei einem falschen Code.
func (p *PasswortAnmeldung) codePruefen(ctx context.Context, kontoID, eingabe string) (bool, error) {
	return zweitenFaktorPruefen(ctx, p.totp, p.box, p.uhr(), kontoID, eingabe)
}

// zweitenFaktorPruefen ist die gemeinsame Prüfung des zweiten Faktors für die
// Anmeldung und die Konto-Einstellungen (Karte #1083): sechs Ziffern → TOTP
// (jeder Zeitschritt gilt einmal), sonst Wiederherstellungscode (einmal).
func zweitenFaktorPruefen(ctx context.Context, totp TOTPKonten, box Oeffner, jetzt time.Time, kontoID, eingabe string) (bool, error) {
	if _, istTOTP := istTOTPEingabe(eingabe); istTOTP {
		versiegelt, letzter, err := totp.TOTPGeheimnis(ctx, kontoID)
		if errors.Is(err, storage.ErrKontoUnbekannt) || (err == nil && versiegelt == "") {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		g, err := box.Open(versiegelt)
		if err != nil {
			return false, err
		}
		schritt, ok := TOTPPasst(g, eingabe, jetzt, letzter)
		if !ok {
			return false, nil
		}
		return totp.TOTPSchrittVerbrauchen(ctx, kontoID, schritt)
	}
	return totp.CodeVerbrauchen(ctx, kontoID, WiederherstellungsHash(eingabe), jetzt.Unix())
}

// codeSeite: Formular für den Code.
func (p *PasswortAnmeldung) codeSeite(w http.ResponseWriter, r *http.Request, status int, fehler string) {
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	t := anmeldeTexte[lang]
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Content-Language", lang)
	f := ""
	if fehler != "" {
		f = `<p role="alert"><strong>` + html.EscapeString(fehler) + `</strong></p>`
	}
	f += `<form method="post" action="` + AnmeldungCodePfad + `">` +
		`<p><label>` + html.EscapeString(t.Code) + `<br><input type="text" name="code" inputmode="text" autocomplete="one-time-code" autocapitalize="off" spellcheck="false" required autofocus></label></p>` +
		`<p><button type="submit">` + html.EscapeString(t.Anmelden) + `</button></p></form>`
	anmeldeSeiteAnsicht(w, status, seitenAnsicht{Lang: lang, Titel: t.Titel, Text: t.CodeHinweis, Formular: f,
		Links: []seitenLink{{Text: t.NeuAnmelden, Ziel: AnmeldungSeitePfad}}})
}
