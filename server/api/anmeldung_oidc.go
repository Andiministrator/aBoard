package api

// anmeldung_oidc.go — Ablauf der eingebauten Anmeldung per OpenID Connect
// (ADR-0148 §1–§3, §6; Karte #816).
//
//   GET /anmeldung/start    → Weiterleitung zum Anbieter (state, nonce, PKCE S256)
//   GET /anmeldung/rueckruf[/<kürzel>]
//                           → state und Kürzel prüfen, Code tauschen, ID-Token
//                             prüfen, Bindung (iss, sub) vor Adresse, Tor,
//                             Sitzung, → /
//
// Seit ADR-0149 (Karte #1071) ist OIDCAnmeldung EIN Anbieter einer Tabelle
// (anmeldung_anbieter.go); Kürzel leer = `standard` (pm).
//
// Die Sitzung selbst und die Weiche, die jede andere Anfrage prüft, stehen in
// sitzung.go (Karte #1051). Die Kryptographie (JWKS, Signatur, iss, aud, exp)
// übernimmt go-oidc; die nonce prüft go-oidc NICHT selbst, das geschieht hier.
//
// Grundsätze:
//   - Die Rückruf-Adresse kommt nur aus ABOARD_PUBLIC_BASE_URL (Basis), nie aus
//     Host oder X-Forwarded-*.
//   - Nach der Anmeldung immer auf „/“ — kein ?next=, kein offener Weiterleiter.
//   - Zugelassen ist, wen das Tor hereinlässt (ADR-0149 §5.1: Vorab-Liste samt
//     Bootstrap-Owner ODER aktives Konto, gesperrt nie). ACL-Eintrag und
//     Domain-Freigabe lassen niemanden herein (ADR-0148 §3).
//   - Die Bindung (iss, sub) bestimmt die Person, nicht die gemeldete Adresse
//     (ADR-0149 §4). Keine Verknüpfung per Adressgleichheit (Kritik W4).
//   - Ist der Anbieter nicht erreichbar, antwortet /anmeldung/start mit 503,
//     bis die Discovery gelingt; kein Rückfall auf eine andere Anmeldung.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// AnmeldungCookie hält state, nonce und Code-Verifier zwischen Start und
	// Rückruf (signiert, nicht verschlüsselt — ADR-0148 §2).
	AnmeldungCookie = "__Host-aboard_anmeldung"
	// anmeldungGueltig: so lange gilt ein begonnener Anmeldevorgang.
	anmeldungGueltig = 10 * time.Minute
	// discoveryWartezeit: frühestens so oft wird ein gescheiterter
	// Discovery-Versuch wiederholt (Schutz des Anbieters bei vielen Aufrufen).
	discoveryWartezeit = 10 * time.Second
	// anbieterZeitgrenze: Zeitgrenze je Anfrage an den Anbieter.
	anbieterZeitgrenze = 15 * time.Second
)

// ---------------------------------------------------------------------------
// Zulassung (ADR-0148 §3)

// Zulassung ist die Liste, die der Betreiber bewusst setzt: Adressen und
// @domain-Einträge, dazu der Bootstrap-Owner. Sie liegt im Speicher und
// ändert sich nur mit einem Neustart.
type Zulassung struct {
	adressen map[string]bool
	domains  map[string]bool
}

// NeueZulassung liest die Liste (Trennzeichen: Komma, Semikolon, Leerraum).
// bootstrap ist die schon geprüfte Adresse des Bootstrap-Owners oder leer.
// Ein ungültiger Eintrag ist ein Fehler — der Start bricht ab, statt einen
// Tippfehler still zu übergehen.
func NeueZulassung(liste, bootstrap string) (*Zulassung, error) {
	z := &Zulassung{adressen: map[string]bool{}, domains: map[string]bool{}}
	felder := strings.FieldsFunc(liste, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	for _, f := range felder {
		if !nurASCII(f) {
			// Kritik A-1: strings.ToLower faltet z. B. das Kelvin-Zeichen zu
			// „k“ — zwei verschiedene Adressen würden gleich. Nur ASCII.
			return nil, fmt.Errorf("Vorab-Liste (ABOARD_OIDC_ALLOWED/ABOARD_OIDC_ZUGELASSEN): %q enthält Zeichen außerhalb von ASCII", f)
		}
		e := storage.NormalizeEmail(f)
		switch {
		case strings.HasPrefix(e, "@"):
			d := strings.TrimPrefix(e, "@")
			if d == "" || strings.Contains(d, "@") || !strings.Contains(d, ".") {
				return nil, fmt.Errorf("Vorab-Liste (ABOARD_OIDC_ALLOWED/ABOARD_OIDC_ZUGELASSEN): %q ist keine Domain (erwartet @firma.de)", f)
			}
			z.domains[d] = true
		case strings.Count(e, "@") == 1 && !strings.HasSuffix(e, "@"):
			z.adressen[e] = true
		default:
			return nil, fmt.Errorf("Vorab-Liste (ABOARD_OIDC_ALLOWED/ABOARD_OIDC_ZUGELASSEN): %q ist weder Adresse noch @domain", f)
		}
	}
	if b := storage.NormalizeEmail(bootstrap); b != "" && nurASCII(bootstrap) && !storage.IstDomainSubjekt(b) { // Verify V-2
		z.adressen[b] = true
	}
	return z, nil
}

// Erlaubt sagt, ob die normalisierte Adresse herein darf.
func (z *Zulassung) Erlaubt(email string) bool {
	if z == nil {
		return false
	}
	e := storage.NormalizeEmail(email)
	if e == "" || storage.IstDomainSubjekt(e) {
		return false
	}
	if z.adressen[e] {
		return true
	}
	at := strings.LastIndex(e, "@")
	return at > 0 && z.domains[e[at+1:]]
}

// Leer sagt, ob niemand zugelassen ist (Startwarnung).
func (z *Zulassung) Leer() bool { return z == nil || (len(z.adressen) == 0 && len(z.domains) == 0) }

// ---------------------------------------------------------------------------
// Ablauf

// AnmeldeSitzungen ist der Teil von storage.SitzungRepo, den der Rückruf braucht.
type AnmeldeSitzungen interface {
	Anlegen(ctx context.Context, email, name string) (string, time.Time, error)
	Beenden(ctx context.Context, klartext string) error
}

// AnmeldeBindungen ist der Teil von storage.AnbieterBindungRepo, den der Rückruf braucht.
type AnmeldeBindungen interface {
	NachKonto(ctx context.Context, iss, sub string) (*storage.AnbieterBindung, error)
	PruefenFuer(ctx context.Context, anbieter, email, iss, sub string) error
}

// NutzerAnlegen liefert die Funktion, mit der storage.AnbieterBindungRepo
// beim ersten Login einer zugelassenen Adresse die users-Zeile samt
// persönlichem Board anlegt — derselbe Weg wie der ACLGuard beim Erstkontakt
// (ADR-0066), nur vor dem Binden statt beim ersten Board-Aufruf (Migration
// 0046, Karte #1069). Der Name bleibt leer; der ACLGuard zieht ihn beim
// ersten Aufruf aus der Sitzung nach (Karte #577).
func NutzerAnlegen(b *storage.BootstrapRepo) func(ctx context.Context, email string) error {
	return func(ctx context.Context, email string) error {
		_, err := b.BootstrapNewUser(ctx, uuid.NewString(), uuid.NewString(), email, "")
		return err
	}
}

// OIDCAnmeldung führt Start und Rückruf für einen Anbieter.
type OIDCAnmeldung struct {
	// Kuerzel des Anbieters ("" = storage.AnbieterStandard). Bestimmt den
	// Rückrufpfad und steht im signierten Anmelde-Zustand.
	Kuerzel string
	// Label beschriftet den Knopf „Mit <Label> anmelden“.
	Label        string
	Issuer       string
	ClientID     string
	ClientSecret string
	// Basis ist ABOARD_PUBLIC_BASE_URL ohne abschliessenden Schrägstrich.
	Basis string
	// Schluessel signiert den Anmelde-Cookie (AnmeldeSchluessel).
	Schluessel []byte
	// Client für alle Anfragen an den Anbieter — im Betrieb mit
	// ausgang.Transport (ADR-0129 §5).
	Client    *http.Client
	Sitzungen AnmeldeSitzungen
	Bindungen AnmeldeBindungen
	Zulassung *Zulassung
	// Tor ersetzt die reine Listenprüfung (ADR-0149 §5.1): Liste ODER aktives
	// Konto, gesperrt nie. nil → nur Zulassung (Tests von ADR-0148).
	Tor func(ctx context.Context, email string) (bool, error)
	// Einladung löst im Rückruf mit Zweck invite ein (Karte #1077); nil =
	// Einladungen über diesen Anbieter sind nicht möglich.
	Einladung *EinladungEinloesen
	// Verknuepfung schließt im Rückruf mit Zweck link ab (Karte #1083); nil =
	// Verknüpfen über diesen Anbieter ist nicht möglich.
	Verknuepfung *KontoEinstellungen
	Wartezeit    time.Duration // 0 → discoveryWartezeit; Tests setzen etwas Kleines
	jetzt        func() time.Time
	mu           sync.Mutex
	anbieter     *oidc.Provider
	letzterVer   time.Time
	letzterErr   error
	laeuft       chan struct{} // offen, solange eine Discovery läuft
}

// AnmeldeSchluessel leitet den Signierschlüssel des Anmelde-Cookies aus
// ABOARD_SECRET_KEY ab (eigener Zweck, damit derselbe Schlüssel nie für zwei
// Dinge gilt).
func AnmeldeSchluessel(masterKey string) []byte {
	m := hmac.New(sha256.New, []byte(strings.TrimSpace(masterKey)))
	m.Write([]byte("aboard-anmeldung-cookie-v1"))
	return m.Sum(nil)
}

func (a *OIDCAnmeldung) kuerzel() string {
	if a.Kuerzel == "" {
		return storage.AnbieterStandard
	}
	return a.Kuerzel
}

func (a *OIDCAnmeldung) label() string {
	if a.Label != "" {
		return a.Label
	}
	return standardLabel(a.kuerzel(), a.Issuer)
}

// RueckrufPfad: /anmeldung/rueckruf für standard, sonst
// /anmeldung/rueckruf/<kürzel> (ADR-0149 §4). So steht es beim Anbieter.
func (a *OIDCAnmeldung) RueckrufPfad() string {
	if a.kuerzel() == storage.AnbieterStandard {
		return AnmeldungRueckrufPfad
	}
	return AnmeldungRueckrufPfad + "/" + a.kuerzel()
}

func (a *OIDCAnmeldung) uhr() time.Time {
	if a.jetzt != nil {
		return a.jetzt()
	}
	return time.Now()
}

func (a *OIDCAnmeldung) clientCtx(ctx context.Context) context.Context {
	if a.Client == nil {
		return ctx
	}
	return oidc.ClientContext(ctx, a.Client)
}

// Anbieter liefert den Anbieter und führt die Discovery bei Bedarf aus.
// Gescheiterte Versuche werden frühestens nach der Wartezeit wiederholt
// (ADR-0148 §6: verzögert, mit Wiederholung, ohne Neustart). Kritik A-4: Das
// Mutex schützt nur den Zustand, nie den Netzaufruf; läuft schon eine
// Discovery, warten weitere Aufrufe auf deren Ende oder brechen mit ihrem
// Kontext ab.
func (a *OIDCAnmeldung) Anbieter(ctx context.Context) (*oidc.Provider, error) {
	for {
		a.mu.Lock()
		if a.anbieter != nil {
			p := a.anbieter
			a.mu.Unlock()
			return p, nil
		}
		if a.laeuft != nil {
			warte := a.laeuft
			a.mu.Unlock()
			select {
			case <-warte:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		warten := a.Wartezeit
		if warten == 0 {
			warten = discoveryWartezeit
		}
		if a.letzterErr != nil && a.uhr().Sub(a.letzterVer) < warten {
			err := a.letzterErr
			a.mu.Unlock()
			return nil, err
		}
		fertig := make(chan struct{})
		a.laeuft = fertig
		a.mu.Unlock()

		// Die Discovery läuft für sich: Sie soll nicht abbrechen, weil der
		// Browser die auslösende Anfrage abbricht (go-oidc behält aus dem
		// Kontext nur den Client). Auch der Auslöser wartet nur so lange, wie
		// sein eigener Kontext es erlaubt (Verify V-4).
		go func() {
			dctx, abbrechen := context.WithTimeout(a.clientCtx(context.Background()), anbieterZeitgrenze)
			p, err := oidc.NewProvider(dctx, a.Issuer)
			abbrechen()
			a.mu.Lock()
			a.letzterVer = a.uhr()
			if err != nil {
				a.letzterErr = err
			} else {
				a.anbieter, a.letzterErr = p, nil
			}
			a.laeuft = nil
			close(fertig)
			a.mu.Unlock()
		}()
		select {
		case <-fertig:
			a.mu.Lock()
			p, err := a.anbieter, a.letzterErr
			a.mu.Unlock()
			if p != nil {
				return p, nil
			}
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (a *OIDCAnmeldung) oauthKonfig(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.ClientID,
		ClientSecret: a.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.Basis + a.RueckrufPfad(),
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// Zwecke eines Anmeldevorgangs (ADR-0149 §4; reauth: Nachtrag #1098 —
// frische Anmeldung beim schon gebundenen Anbieter als Bestätigung).
const (
	ZweckLogin  = "login"
	ZweckInvite = "invite"
	ZweckLink   = "link"
	ZweckReauth = "reauth"
)

// anmeldeZustand steht (signiert) im Anmelde-Cookie. Kürzel und Zweck
// binden den Vorgang an einen Anbieter und eine Absicht: Der Rückruf eines
// anderen Anbieters kann ihn nicht einlösen (Kritik W5).
type anmeldeZustand struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Bis      int64  `json:"e"`
	Kuerzel  string `json:"k"`
	Zweck    string `json:"z"`
	// Einladung: Hash des Einladungs-Tokens bei Zweck invite (Sprint 110).
	Einladung string `json:"i,omitempty"`
	// Sitzung: SHA-256 der Sitzungs-ID der Person, die bei Zweck link den
	// Vorgang begonnen hat (Karte #1083). Der Rückruf bindet nur, wenn dieselbe
	// Sitzung noch besteht — ein fremder oder überschriebener Vorgang bindet
	// nie an die falsche Person.
	Sitzung string `json:"x,omitempty"`
	// Aktion: bei Zweck reauth die Änderung, die die frische Anmeldung
	// bestätigen soll (verknuepfen:<kürzel>, loesen:<kürzel>, passwort;
	// Karte #1098).
	Aktion string `json:"a,omitempty"`
	// Frisch: bei Zweck link wurde der Vorgang mit einem Nachweis „frisch
	// bestätigt“ statt mit dem Passwort begonnen (Karte #1098). Nur der Server
	// signiert den Zustand; die zweite Schranke im Rückruf verlangt Passwort
	// ODER dieses Merkmal.
	Frisch bool `json:"f,omitempty"`
}

// vorgangOpt beschreibt einen Anmeldevorgang für vorgang.
type vorgangOpt struct {
	Zweck, Einladung, Sitzung, Aktion string
	Frisch                            bool
	// KontoWahl verlangt die Kontoauswahl beim Anbieter; AlsJSON schreibt
	// Fehler als JSON statt als Seite (Konto-Einstellungen).
	KontoWahl, AlsJSON bool
}

func zufall() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *OIDCAnmeldung) signiere(teil string) string {
	m := hmac.New(sha256.New, a.Schluessel)
	m.Write([]byte(teil))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *OIDCAnmeldung) zustandKodieren(z anmeldeZustand) (string, error) {
	roh, err := json.Marshal(z)
	if err != nil {
		return "", err
	}
	teil := base64.RawURLEncoding.EncodeToString(roh)
	return teil + "." + a.signiere(teil), nil
}

var errZustand = errors.New("anmeldung: zustand fehlt, ist verfälscht oder abgelaufen")

func (a *OIDCAnmeldung) zustandLesen(wert string) (anmeldeZustand, error) {
	var z anmeldeZustand
	teil, sig, ok := strings.Cut(wert, ".")
	if !ok || len(a.Schluessel) == 0 || !hmac.Equal([]byte(sig), []byte(a.signiere(teil))) {
		return z, errZustand
	}
	roh, err := base64.RawURLEncoding.DecodeString(teil)
	if err != nil || json.Unmarshal(roh, &z) != nil {
		return z, errZustand
	}
	if z.State == "" || z.Nonce == "" || z.Verifier == "" || z.Kuerzel == "" || z.Zweck == "" || a.uhr().Unix() >= z.Bis {
		return z, errZustand
	}
	return z, nil
}

func setzeAnmeldeCookie(w http.ResponseWriter, wert string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     AnmeldungCookie,
		Value:    wert,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Start: GET /anmeldung/start.
func (a *OIDCAnmeldung) Start(w http.ResponseWriter, r *http.Request) {
	a.startMitZweck(w, r, ZweckLogin, "")
}

// startMitZweck beginnt den Vorgang beim Anbieter; einladung ist bei Zweck
// invite der Hash des Einladungs-Tokens (Karte #1077).
func (a *OIDCAnmeldung) startMitZweck(w http.ResponseWriter, r *http.Request, zweck, einladung string) {
	ziel, ok := a.vorgang(w, r, vorgangOpt{Zweck: zweck, Einladung: einladung, KontoWahl: r.URL.Query().Get(AnmeldungKontoWahlParam) == "1"})
	if !ok {
		return
	}
	http.Redirect(w, r, ziel, http.StatusFound)
}

// vorgang beginnt den Vorgang: Anmelde-Cookie setzen und die Adresse beim
// Anbieter liefern. Bei false ist die Antwort schon geschrieben. o.Sitzung ist
// bei Zweck link und reauth der Hash der Sitzungs-ID.
func (a *OIDCAnmeldung) vorgang(w http.ResponseWriter, r *http.Request, o vorgangOpt) (string, bool) {
	zweck, alsJSON := o.Zweck, o.AlsJSON
	w.Header().Set("Cache-Control", "no-store")
	p, err := a.Anbieter(r.Context())
	if err != nil {
		if anbieterLogErlaubt(a.uhr()) {
			log.Printf("aboard-server: anmeldung: Anbieter %s nicht erreichbar: %s (höchstens eine Zeile pro Minute)",
				a.Issuer, redactForLog(err))
		}
		if alsJSON {
			fehlerAntwort(w, http.StatusServiceUnavailable, "nicht_erreichbar")
		} else {
			textSeite(w, r, http.StatusServiceUnavailable, sNichtErreichbar, true)
		}
		return "", false
	}
	state, err1 := zufall()
	nonce, err2 := zufall()
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusInternalServerError, "random failed")
		return "", false
	}
	verifier := oauth2.GenerateVerifier()
	wert, err := a.zustandKodieren(anmeldeZustand{State: state, Nonce: nonce, Verifier: verifier,
		Bis: a.uhr().Add(anmeldungGueltig).Unix(), Kuerzel: a.kuerzel(), Zweck: zweck, Einladung: o.Einladung, Sitzung: o.Sitzung,
		Aktion: o.Aktion, Frisch: o.Frisch})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login state failed", err)
		return "", false
	}
	setzeAnmeldeCookie(w, wert, int(anmeldungGueltig.Seconds()))
	// response_mode bleibt beim Standard „query“ des Code-Flows (kein
	// form_post: ein POST von der Anbieter-Seite trüge das Lax-Cookie nicht).
	optionen := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	// Nach dem Abmelden (Seite /anmeldung/abgemeldet) fragt der Anbieter nach
	// dem Konto, statt still mit der noch offenen Anbieter-Sitzung wieder
	// anzumelden (Sprint-Abschluss 106–108, K2 F-B).
	switch {
	case zweck == ZweckReauth:
		// Frische Anmeldung (Karte #1098): max_age=0 und die ausdrückliche
		// Anforderung von auth_time über den Parameter claims (OpenID Connect
		// Core 5.5) — Google liefert auth_time nur so und nur, wenn es beim
		// OAuth-Client eingeschaltet ist; den Wert prüft der Rückruf.
		// prompt=login nur, wenn der Anbieter es in prompt_values_supported
		// nennt (Google kennt es nicht und bräche mit invalid_request ab);
		// sonst wenigstens die Kontoauswahl.
		prompt := "select_account"
		if promptLoginUnterstuetzt(p) {
			prompt = "login"
		}
		optionen = append(optionen, oauth2.SetAuthURLParam("prompt", prompt), oauth2.SetAuthURLParam("max_age", "0"),
			oauth2.SetAuthURLParam("claims", reauthClaims))
	case o.KontoWahl || zweck == ZweckLink:
		// Beim Verknüpfen immer: Die Person soll das Konto wählen, nicht still
		// das schon offene nehmen (sonst ließe sich kein anderes Konto binden).
		optionen = append(optionen, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return a.oauthKonfig(p).AuthCodeURL(state, optionen...), true
}

// reauthClaims fordert auth_time als wesentlichen Claim des ID-Tokens an.
const reauthClaims = `{"id_token":{"auth_time":{"essential":true}}}`

// promptLoginUnterstuetzt: Nennt das Discovery-Dokument prompt=login unter
// prompt_values_supported? Fehlt das Feld, gilt nein.
func promptLoginUnterstuetzt(p *oidc.Provider) bool {
	var d struct {
		Prompt []string `json:"prompt_values_supported"`
	}
	if p == nil || p.Claims(&d) != nil {
		return false
	}
	for _, v := range d.Prompt {
		if v == "login" {
			return true
		}
	}
	return false
}

// Abgemeldet: GET /anmeldung/abgemeldet — Zielseite nach dem Abmelden. Sie
// leitet NICHT selbst zur Anmeldung: bei offener Anbieter-Sitzung (Google)
// wäre die Person sonst ohne Rückfrage wieder angemeldet (K2 F-B). Der Link
// fordert beim Anbieter die Kontoauswahl an.
func (a *OIDCAnmeldung) Abgemeldet(w http.ResponseWriter, r *http.Request) {
	abgemeldetSeite(w, r)
}

// abgemeldetSeite ist die Seite „Abgemeldet“ — unabhängig von einem Anbieter
// (auch ein Server nur mit Passwort braucht sie; F-2894).
func abgemeldetSeite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	textSeiteZiel(w, r, http.StatusOK, sAbgemeldet, AnmeldungSeitePfad+"?"+AnmeldungKontoWahlParam+"=1")
}

// Der Text „Anmeldung abgelaufen“ (anmeldeTexte, sAbgelaufen): Es gibt nur ein
// Anmelde-Cookie (Sprint-Abschluss 109, K1-4). Startet die Person während des
// Einlösens anderswo eine Anmeldung, überschreibt diese den Einladungs-Zustand;
// der Link ist dann noch nicht verbraucht (Karte #1077).

// Rueckruf: GET /anmeldung/rueckruf[/<kürzel>] (über Anmeldungen).
func (a *OIDCAnmeldung) Rueckruf(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Das Anmelde-Cookie gilt genau einmal — in jedem Ausgang löschen.
	c, cerr := r.Cookie(AnmeldungCookie)
	setzeAnmeldeCookie(w, "", -1)
	if cerr != nil {
		textSeite(w, r, http.StatusBadRequest, sAbgelaufen, true)
		return
	}
	z, err := a.zustandLesen(c.Value)
	q := r.URL.Query()
	if err != nil || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(z.State)) != 1 {
		textSeite(w, r, http.StatusBadRequest, sAbgelaufen, true)
		return
	}
	// Kritik W5: Der Vorgang gehört dem Anbieter, bei dem er begann. Sonst
	// ließe sich ein Code des einen Anbieters am Rückruf eines anderen
	// einlösen.
	if z.Kuerzel != a.kuerzel() {
		// Kritiker Sprint-Abschluss 109 (F-2879): gedrosselt wie die übrigen
		// Abweis-Zeilen — ein Zustands-Cookie lässt sich zehn Minuten lang
		// beliebig oft am falschen Rückruf vorzeigen.
		if kuerzelLogErlaubt(a.uhr()) {
			log.Printf("aboard-server: anmeldung: Rückruf von %q für einen Vorgang bei %q abgewiesen", a.kuerzel(), z.Kuerzel)
		}
		textSeite(w, r, http.StatusBadRequest, sAbgelaufen, true)
		return
	}
	switch {
	case z.Zweck == ZweckLogin:
	case z.Zweck == ZweckInvite && a.Einladung != nil && z.Einladung != "":
	case z.Zweck == ZweckLink && a.Verknuepfung != nil && z.Sitzung != "":
	case z.Zweck == ZweckReauth && a.Verknuepfung != nil && z.Sitzung != "" && z.Aktion != "":
	default:
		textSeite(w, r, http.StatusForbidden, sNichtMoeglich, true)
		return
	}
	if q.Get("error") != "" && (z.Zweck == ZweckLink || z.Zweck == ZweckReauth) {
		// Die Person hat das Verknüpfen beim Anbieter abgebrochen: zurück in die
		// App, die es in den Konto-Einstellungen sagt.
		a.Verknuepfung.zurueck(w, r, KontoErgebnisAbgebrochen)
		return
	}
	if q.Get("error") != "" {
		// Abbruch beim Anbieter (z. B. „access_denied“). Der Text kommt vom
		// Client und wird weder geloggt noch angezeigt.
		textSeite(w, r, http.StatusForbidden, sAbgebrochen, true)
		return
	}
	code := q.Get("code")
	if code == "" {
		textSeite(w, r, http.StatusBadRequest, sUnvollstaendig, true)
		return
	}
	p, err := a.Anbieter(r.Context())
	if err != nil {
		textSeite(w, r, http.StatusServiceUnavailable, sNichtErreichbar, true)
		return
	}
	ctx, abbrechen := context.WithTimeout(r.Context(), anbieterZeitgrenze)
	defer abbrechen()
	if a.Client != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, a.Client)
	}
	tok, err := a.oauthKonfig(p).Exchange(ctx, code, oauth2.VerifierOption(z.Verifier))
	if err != nil {
		log.Printf("aboard-server: anmeldung: Code-Tausch gescheitert: %s", redactForLog(err))
		textSeite(w, r, http.StatusBadGateway, sGescheitert, true)
		return
	}
	rohToken, _ := tok.Extra("id_token").(string)
	if rohToken == "" {
		textSeite(w, r, http.StatusBadGateway, sKeinIDToken, true)
		return
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: a.ClientID}).Verify(a.clientCtx(ctx), rohToken)
	if err != nil {
		log.Printf("aboard-server: anmeldung: ID-Token abgewiesen: %s", redactForLog(err))
		textSeite(w, r, http.StatusForbidden, sTokenUngueltig, true)
		return
	}
	// go-oidc prüft azp nicht (K2 F-D): Bei mehreren Empfängern muss azp
	// unser Client sein, und ein gesetztes azp darf nie ein fremder sein
	// (OpenID Connect Core 3.1.3.7, Punkte 4 und 5).
	if !azpPasst(idt, a.ClientID) {
		log.Printf("aboard-server: anmeldung: ID-Token mit fremdem azp abgewiesen")
		textSeite(w, r, http.StatusForbidden, sTokenUngueltig, true)
		return
	}
	// go-oidc prüft die nonce nicht selbst (ADR-0148 §2).
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(z.Nonce)) != 1 {
		log.Printf("aboard-server: anmeldung: ID-Token mit falscher nonce abgewiesen")
		textSeite(w, r, http.StatusForbidden, sTokenUngueltig, true)
		return
	}
	var claims struct {
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
	}
	if err := idt.Claims(&claims); err != nil {
		textSeite(w, r, http.StatusForbidden, sTokenUnlesbar, true)
		return
	}
	if z.Zweck == ZweckInvite {
		// Einlösen (ADR-0149 §5.4): Die Bindung entsteht am eingeladenen
		// Konto, auch wenn der Anbieter eine andere Adresse meldet. Die
		// gemeldete Adresse sieht der Admin vor der Bestätigung — mit dem Hinweis
		// „nicht bestätigt“ (Feld gemeldete_email_unbestaetigt), wenn der Anbieter
		// email_verified nicht bejaht (F-2911, F-2961).
		a.Einladung.anbieterAbschliessen(ctx, w, r, z.Einladung, a.kuerzel(), idt.Issuer, idt.Subject, claims.Email, claims.Name, !emailBestaetigt(claims.EmailVerified))
		return
	}
	if z.Zweck == ZweckLink {
		// Weiteren Weg an die angemeldete Person hängen (ADR-0149 §4).
		a.Verknuepfung.abschliessen(ctx, w, r, z.Sitzung, z.Frisch, a.kuerzel(), idt.Issuer, idt.Subject)
		return
	}
	if z.Zweck == ZweckReauth {
		// Frische Anmeldung als Bestätigung (Karte #1098): dieselbe Person,
		// dasselbe sub, auth_time frisch — sonst kein Nachweis.
		a.Verknuepfung.frischAbschliessen(ctx, w, r, z, a.kuerzel(), idt)
		return
	}
	if a.Bindungen == nil || a.Sitzungen == nil {
		writeError(w, http.StatusInternalServerError, "login storage missing")
		return
	}
	email, ok := a.person(ctx, w, r, idt.Issuer, idt.Subject, claims.Email, claims.EmailVerified)
	if !ok {
		return
	}
	// Fixierungsschutz: beginneSitzung beendet eine mitgebrachte alte Sitzung.
	if !beginneSitzung(w, r.WithContext(ctx), a.Sitzungen, email, claims.Name) {
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// person bestimmt, als wer angemeldet wird, und hat bei false schon
// geantwortet (ADR-0149 §4, §5.1):
//
//  1. Ist (iss, sub) gebunden, gilt die Adresse der gebundenen Person — gleich,
//     was der Anbieter meldet. Eine Bindung nur an ein unbestätigtes Konto
//     zeigt „wartet auf Bestätigung“.
//  2. Sonst gilt die gemeldete, bestätigte Adresse; nur das Tor (Vorab-Liste
//     oder aktives Konto) lässt herein, und der erste Login bindet. Ist die
//     Person schon an ein anderes Anbieter-Konto gebunden, wird abgewiesen —
//     keine Verknüpfung per Adressgleichheit (Kritik W4).
//
// Danach prüft das Tor die Person in jedem Fall (gesperrt, gestrichen).
func (a *OIDCAnmeldung) person(ctx context.Context, w http.ResponseWriter, r *http.Request, iss, sub, gemeldet string, bestaetigt json.RawMessage) (string, bool) {
	b, err := a.Bindungen.NachKonto(ctx, iss, sub)
	switch {
	case err == nil && b.UserID == "":
		textSeite(w, r, http.StatusForbidden, sWartet, false)
		return "", false
	case err == nil:
		if !a.zugelassen(ctx, w, r, b.Email) {
			return "", false
		}
		return b.Email, true
	case !errors.Is(err, storage.ErrBindungUnbekannt):
		writeError(w, http.StatusInternalServerError, "login binding failed", err)
		return "", false
	}

	email := storage.NormalizeEmail(gemeldet)
	// Kritik A-1: Nicht-ASCII vor dem Normalisieren abweisen (Unicode-Faltung
	// in strings.ToLower machte sonst aus einer fremden Adresse eine
	// zugelassene).
	if !nurASCII(gemeldet) || email == "" || !emailBestaetigt(bestaetigt) || storage.IstDomainSubjekt(email) || strings.Count(email, "@") != 1 {
		textSeite(w, r, http.StatusForbidden, sKeineMail, false)
		return "", false
	}
	// F-2903 (ADR-0149 §4): Ohne Bindung lässt nur die Vorab-Liste herein,
	// nicht schon ein aktives Konto. Sonst käme in ein Passwort-Konto (mit
	// Pflicht-2FA), wer bei irgendeinem Anbieter dieselbe Adresse bestätigt
	// bekommt — am Passwort und am zweiten Faktor vorbei. Eine weitere Bindung
	// entsteht nur über Einladung oder Konto-Einstellungen.
	if !a.Zulassung.Erlaubt(email) {
		if abweisLogErlaubt(a.uhr()) {
			log.Printf("aboard-server: anmeldung: %q ohne Bindung und nicht auf der Vorab-Liste — abgewiesen (höchstens eine Zeile pro Minute)", email)
		}
		textSeite(w, r, http.StatusForbidden, sNichtFrei, false)
		return "", false
	}
	if !a.zugelassen(ctx, w, r, email) {
		return "", false
	}
	if err := a.Bindungen.PruefenFuer(ctx, a.kuerzel(), email, iss, sub); err != nil {
		if errors.Is(err, storage.ErrBindungAbweichend) {
			// Gedrosselt wie die Nicht-zugelassen-Zeile (K2 F-G), aber mit
			// eigenem Zähler: sonst verdrängte eine harmlose Abweisung diese
			// Warnung vor einer Kontoübernahme (Sitzungs-Kritik A, F-2867).
			if bindungLogErlaubt(a.uhr()) {
				log.Printf("aboard-server: anmeldung: %q meldet sich mit einem anderen Konto (sub) als beim ersten Mal — "+
					"abgewiesen. Anderes Anbieter-Konto der Person: angemeldet in den Konto-Einstellungen (Anmeldewege) "+
					"verknüpfen bzw. das alte lösen; Adresse neu vergeben oder Person zieht um: aboard-server nutzer umziehen "+
					"(höchstens eine Zeile pro Minute)", email)
			}
			textSeite(w, r, http.StatusForbidden, sAnderesKonto, false)
			return "", false
		}
		writeError(w, http.StatusInternalServerError, "login binding failed", err)
		return "", false
	}
	return email, true
}

// zugelassen fragt das Tor (ohne Tor: nur die Vorab-Liste) und hat bei false
// schon geantwortet.
func (a *OIDCAnmeldung) zugelassen(ctx context.Context, w http.ResponseWriter, r *http.Request, email string) bool {
	ok := a.Zulassung.Erlaubt(email)
	if a.Tor != nil {
		var err error
		if ok, err = a.Tor(ctx, email); err != nil {
			writeError(w, http.StatusInternalServerError, "access check failed", err)
			return false
		}
	}
	if !ok {
		if abweisLogErlaubt(a.uhr()) {
			log.Printf("aboard-server: anmeldung: %q ist nicht zugelassen — abgewiesen (höchstens eine Zeile pro Minute)", email)
		}
		textSeite(w, r, http.StatusForbidden, sNichtFrei, false)
	}
	return ok
}

// azpPasst prüft den Claim azp (K2 F-D): Pflicht bei mehreren Empfängern,
// und wenn gesetzt, muss er unser Client sein.
func azpPasst(idt *oidc.IDToken, clientID string) bool {
	var c struct {
		Azp string `json:"azp"`
	}
	if err := idt.Claims(&c); err != nil {
		return false
	}
	if len(idt.Audience) > 1 && c.Azp == "" {
		return false
	}
	return c.Azp == "" || c.Azp == clientID
}

// nurASCII: nur druckbare ASCII-Zeichen (Kritik A-1).
func nurASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// emailBestaetigt nimmt true und "true" an (manche Anbieter senden eine
// Zeichenkette, ADR-0148 §2), sonst nichts.
func emailBestaetigt(roh json.RawMessage) bool {
	var b bool
	if json.Unmarshal(roh, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(roh, &s) == nil {
		return strings.EqualFold(strings.TrimSpace(s), "true")
	}
	return false
}

// seitenLink ist ein Link auf einer Anmeldeseite; Ziel ist immer ein fester
// Pfad dieses Servers (nie aus der Anfrage übernommen).
type seitenLink struct{ Text, Ziel string }

var anbieterLogZuletzt, abweisLogZuletzt, bindungLogZuletzt, kuerzelLogZuletzt atomic.Int64

func anbieterLogErlaubt(jetzt time.Time) bool { return drosselErlaubt(&anbieterLogZuletzt, jetzt) }
func abweisLogErlaubt(jetzt time.Time) bool   { return drosselErlaubt(&abweisLogZuletzt, jetzt) }
func bindungLogErlaubt(jetzt time.Time) bool  { return drosselErlaubt(&bindungLogZuletzt, jetzt) }
func kuerzelLogErlaubt(jetzt time.Time) bool  { return drosselErlaubt(&kuerzelLogZuletzt, jetzt) }
