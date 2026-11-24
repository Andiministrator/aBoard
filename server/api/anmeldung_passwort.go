package api

// anmeldung_passwort.go — Anmeldung mit E-Mail-Adresse und Passwort
// (ADR-0149 §2, §3; Karte #1074).
//
// POST /anmeldung/passwort prüft Adresse und Passwort. Danach fehlt immer
// noch der zweite Faktor (2FA ist für Passwort-Konten Pflicht, ADR-0149 §3);
// den baut #1075 (NachPasswort). Bis dahin endet eine richtige Eingabe mit
// „Zwei-Faktor fehlt“ — nie mit einer Sitzung.
//
// Schranken:
//   - Bremse je Gegenstelle: PasswortMaxFehlversuche Fehlversuche in
//     PasswortFenster → Sperre bis zum Fensterende (429). Die Gegenstelle
//     bestimmt dieselbe Adressregel wie die Agenten-Bremse (X-Real-IP nur vom
//     Proxy aus ABOARD_AGENT_PROXY, IPv6 je /64).
//   - Je eingegebener Adresse keine harte Sperre, sondern eine ansteigende
//     Wartezeit (1, 2, 4, … bis 30 s): Eine harte Sperre ließe einen Fremden
//     ein bekanntes Konto dauerhaft aussperren (Sitzungs-Kritik 02.11.). Die
//     Wartezeit hängt an der eingegebenen Adresse, nicht am Konto, und verrät
//     deshalb nicht, ob es das Konto gibt.
//   - Beides liegt im Speicher und überlebt keinen Neustart (bewusst so).
//   - Gleiche Meldung und gleiche Rechenzeit für „Adresse unbekannt“ und
//     „Passwort falsch“ (PasswortPruefen rechnet gegen einen Blind-Hash).
//   - CSRF: MitCSRFSchutz umhüllt den ganzen Menschen-Listener; ein fremdes
//     Formular kann also auch keine Anmeldung auslösen (Login-CSRF).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// PasswortMaxFehlversuche je Gegenstelle und PasswortFenster (ADR-0149 §3).
	PasswortMaxFehlversuche = 5
	// PasswortFenster: Zählfenster und Sperrdauer der Gegenstelle; zugleich
	// die Zeit, nach der die Wartezeit einer Adresse wieder bei null beginnt.
	PasswortFenster = 15 * time.Minute
	// PasswortWarteMax: längste Wartezeit je Versuch für eine Adresse.
	PasswortWarteMax = 30 * time.Second
	// passwortFormularMax: größter angenommener Formular-Körper.
	passwortFormularMax = 16 << 10
)

// PasswortKonten ist der Teil von storage.KontoRepo, den die Anmeldung liest.
type PasswortKonten interface {
	FuerAnmeldung(ctx context.Context, email string) (storage.KontoAnmeldung, error)
}

// PasswortAnmeldung bedient POST /anmeldung/passwort.
type PasswortAnmeldung struct {
	Konten PasswortKonten
	// Tor: dieselbe Prüfung wie in der Sitzungsweiche (Liste ODER aktives
	// Konto, gesperrt schlägt die Liste). Pflicht.
	Tor func(ctx context.Context, email string) (bool, error)
	// NachPasswort übernimmt nach richtigem Passwort eines aktiven,
	// zugelassenen Kontos (#1075: Code-Schritt). nil = „Zwei-Faktor fehlt“.
	NachPasswort func(w http.ResponseWriter, r *http.Request, k storage.KontoAnmeldung)

	gegenstelle *fehlversuchBremse
	adressen    *adressWartezeit
	// pruefen ist PasswortPruefen; Tests setzen etwas Schnelles.
	pruefen func(ctx context.Context, pw, hash string) (bool, error)

	// Code-Schritt (#1075, MitZweitemSchritt).
	totp       TOTPKonten
	box        Oeffner
	schluessel []byte
	sitzungen  AnmeldeSitzungen
	zweiter    *zweiterSchritt
	jetzt      func() time.Time

	// laufend: Schlüssel mit einer Prüfung in Arbeit (belege, F-2888).
	laufendMu sync.Mutex
	laufend   map[string]bool
}

// NeuePasswortAnmeldung baut den Weg. proxy ist der Wert von
// ABOARD_AGENT_PROXY (dieselbe Adressregel wie die Agenten-Bremse).
func NeuePasswortAnmeldung(konten PasswortKonten, tor func(ctx context.Context, email string) (bool, error), proxy string) (*PasswortAnmeldung, error) {
	if konten == nil || tor == nil {
		return nil, errors.New("passwort-anmeldung: konten und tor sind pflicht")
	}
	p, err := parseProxy(proxy)
	if err != nil {
		return nil, err
	}
	return &PasswortAnmeldung{
		Konten:      konten,
		Tor:         tor,
		gegenstelle: neueBremseMit(p, PasswortMaxFehlversuche, PasswortFenster, "Passwort-Anmeldung"),
		adressen:    neueAdressWartezeit(),
		pruefen:     PasswortPruefen,
	}, nil
}

// ---------------------------------------------------------------------------
// Wartezeit je eingegebener Adresse

// Schlüssel ist der SHA-256 der normalisierten Eingabe (F-2888): eine
// beliebig lange „Adresse“ belegt so nur 64 Zeichen.
//
// Bekannte Anschlüsse (F-2889): Hat sich eine Person von einer Gegenstelle
// vollständig angemeldet (Passwort und Code), gilt die Wartezeit ihrer
// Adresse für diese Gegenstelle PasswortBekanntDauer lang nicht. Sonst hielte
// ein Fremder mit wenigen Anschlüssen die Wartezeit eines bekannten Kontos
// dauerhaft hoch — genau die Aussperrung, die ADR-0149 §3 vermeiden will.
// Die Bremse der Gegenstelle gilt für bekannte Anschlüsse weiter. Nach einem
// Neustart ist niemand bekannt (Speicher, wie die Bremsen).

// PasswortBekanntDauer: so lange gilt ein Anschluss nach einer vollständigen
// Anmeldung als bekannt.
const PasswortBekanntDauer = 30 * 24 * time.Hour

type adressEintrag struct {
	fehler  int
	letzter time.Time
}

type adressWartezeit struct {
	mu      sync.Mutex
	jetzt   func() time.Time
	eintrag map[string]*adressEintrag
	bekannt map[string]time.Time // adressSchluessel|gegenstelle → letzte vollständige Anmeldung
}

func neueAdressWartezeit() *adressWartezeit {
	return &adressWartezeit{jetzt: time.Now, eintrag: map[string]*adressEintrag{}, bekannt: map[string]time.Time{}}
}

func adressSchluessel(email string) string {
	s := sha256.Sum256([]byte(email))
	return hex.EncodeToString(s[:])
}

// wartezeitNach: Wartezeit nach n Fehlversuchen (1 s, 2 s, 4 s, … bis
// PasswortWarteMax). Der erste Fehlversuch kostet schon eine Sekunde.
func wartezeitNach(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	if n > 6 {
		return PasswortWarteMax
	}
	return min(time.Duration(1<<(n-1))*time.Second, PasswortWarteMax)
}

// warten: wie lange die Adresse von dieser Gegenstelle noch warten muss
// (0 = darf versuchen).
func (a *adressWartezeit) warten(email, gegenstelle string) time.Duration {
	k := adressSchluessel(email)
	a.mu.Lock()
	defer a.mu.Unlock()
	jetzt := a.jetzt()
	if t, ok := a.bekannt[k+"|"+gegenstelle]; ok && jetzt.Sub(t) < PasswortBekanntDauer {
		return 0
	}
	e := a.eintrag[k]
	if e == nil {
		return 0
	}
	if jetzt.Sub(e.letzter) >= PasswortFenster {
		delete(a.eintrag, k)
		return 0
	}
	if rest := e.letzter.Add(wartezeitNach(e.fehler)).Sub(jetzt); rest > 0 {
		return rest
	}
	return 0
}

func (a *adressWartezeit) fehlschlag(email string) {
	k := adressSchluessel(email)
	a.mu.Lock()
	defer a.mu.Unlock()
	jetzt := a.jetzt()
	e := a.eintrag[k]
	if e == nil || jetzt.Sub(e.letzter) >= PasswortFenster {
		if e == nil && len(a.eintrag) >= maxBremsEintraege {
			a.aufraeumen(jetzt)
		}
		e = &adressEintrag{}
		a.eintrag[k] = e
	}
	e.fehler++
	e.letzter = jetzt
}

// erfolg: vollständige Anmeldung — Wartezeit weg, Gegenstelle bekannt.
func (a *adressWartezeit) erfolg(email, gegenstelle string) {
	k := adressSchluessel(email)
	a.mu.Lock()
	defer a.mu.Unlock()
	jetzt := a.jetzt()
	delete(a.eintrag, k)
	if len(a.bekannt) >= maxBremsEintraege {
		var aeltester string
		var zeit time.Time
		for b, t := range a.bekannt {
			if jetzt.Sub(t) >= PasswortBekanntDauer {
				delete(a.bekannt, b)
				continue
			}
			if aeltester == "" || t.Before(zeit) {
				aeltester, zeit = b, t
			}
		}
		if len(a.bekannt) >= maxBremsEintraege {
			delete(a.bekannt, aeltester)
		}
	}
	a.bekannt[k+"|"+gegenstelle] = jetzt
}

// aufraeumen (unter mu): abgelaufene Einträge weg; ist die Tabelle danach
// noch voll, weicht der mit dem ältesten letzten Fehlversuch.
func (a *adressWartezeit) aufraeumen(jetzt time.Time) {
	var aeltester string
	var zeit time.Time
	for k, e := range a.eintrag {
		if jetzt.Sub(e.letzter) >= PasswortFenster {
			delete(a.eintrag, k)
			continue
		}
		if aeltester == "" || e.letzter.Before(zeit) {
			aeltester, zeit = k, e.letzter
		}
	}
	if len(a.eintrag) >= maxBremsEintraege {
		delete(a.eintrag, aeltester)
	}
}

// ---------------------------------------------------------------------------
// Höchstens eine Prüfung gleichzeitig (F-2888)

// belege reserviert die Schlüssel (Gegenstelle, Adresse, Zwischen-Kennung)
// für genau eine laufende Prüfung. Die Bremsen zählen erst nach dem Rechnen;
// ohne diese Sperre käme ein Schub paralleler Anfragen an allen vorbei.
// false = mindestens einer ist belegt, nichts reserviert.
func (p *PasswortAnmeldung) belege(schluessel ...string) (func(), bool) {
	p.laufendMu.Lock()
	defer p.laufendMu.Unlock()
	if p.laufend == nil {
		p.laufend = map[string]bool{}
	}
	for _, k := range schluessel {
		if p.laufend[k] {
			return nil, false
		}
	}
	for _, k := range schluessel {
		p.laufend[k] = true
	}
	return func() {
		p.laufendMu.Lock()
		defer p.laufendMu.Unlock()
		for _, k := range schluessel {
			delete(p.laufend, k)
		}
	}, true
}

// ---------------------------------------------------------------------------
// Handler

var passwortLogZuletzt atomic.Int64

// PasswortPost: POST /anmeldung/passwort (über Anmeldungen, damit die Seite
// bei einem Fehler mit allen Wegen neu erscheint).
func (m *Anmeldungen) PasswortPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p := m.Passwort
	if p == nil {
		writeError(w, http.StatusNotFound, "not available")
		return
	}
	lang := WaehleSprache(r.Header.Get("Accept-Language"))
	t := anmeldeTexte[lang]
	r.Body = http.MaxBytesReader(w, r.Body, passwortFormularMax)
	if err := r.ParseForm(); err != nil {
		m.seite(w, r, http.StatusBadRequest, t.FalscheEingabe, "")
		return
	}
	eingabe := r.PostForm.Get("email")
	email := storage.NormalizeEmail(eingabe)
	pw := r.PostForm.Get("passwort")

	adresse := p.gegenstelle.proxy.clientAdresse(r)
	schluessel := "a:" + adresse
	// Sperre und Wartezeit zuerst nur lesen (Verify N1): Eine abgewiesene
	// Anfrage soll die Belegung nicht halten. Nach dem Belegen noch einmal,
	// weil eine eben beendete Prüfung den Zähler erhöht haben kann.
	abgewiesen := func() bool {
		if gesperrt, rest := p.gegenstelle.gesperrt(schluessel); gesperrt {
			w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
			m.seite(w, r, http.StatusTooManyRequests, fmt.Sprintf(t.Gesperrt, int(rest.Minutes())+1), eingabe)
			return true
		}
		if rest := p.adressen.warten(email, adresse); rest > 0 {
			sek := int(rest.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(sek))
			m.seite(w, r, http.StatusTooManyRequests, fmt.Sprintf(t.Warten, sek), eingabe)
			return true
		}
		return false
	}
	if abgewiesen() {
		return
	}
	frei, ok := p.belege("g:"+adresse, "m:"+adressSchluessel(email))
	if !ok {
		w.Header().Set("Retry-After", "1")
		m.seite(w, r, http.StatusTooManyRequests, t.Laeuft, eingabe)
		return
	}
	defer frei()
	if abgewiesen() {
		return
	}

	var k storage.KontoAnmeldung
	if email != "" && !storage.IstDomainSubjekt(email) {
		var err error
		k, err = p.Konten.FuerAnmeldung(r.Context(), email)
		if err != nil && !errors.Is(err, storage.ErrKontoUnbekannt) {
			writeError(w, http.StatusInternalServerError, "login lookup failed", err)
			return
		}
	}
	ok, err := p.pruefen(r.Context(), pw, k.PasswortHash)
	switch {
	case errors.Is(err, ErrPasswortUeberlastet):
		w.Header().Set("Retry-After", "5")
		m.seite(w, r, http.StatusServiceUnavailable, t.Ueberlastet, eingabe)
		return
	case err != nil:
		// Ein unlesbarer gespeicherter Hash ist ein Datenfehler, kein
		// Fehlversuch — der Betreiber muss ihn sehen.
		if drosselErlaubt(&passwortLogZuletzt, time.Now()) {
			log.Printf("aboard-server: anmeldung: Passwort-Hash eines Kontos unlesbar: %s (höchstens eine Zeile pro Minute)", redactForLog(err))
		}
		writeError(w, http.StatusInternalServerError, "login check failed")
		return
	}
	if !ok || k.ID == "" {
		p.gegenstelle.fehlschlag(schluessel)
		p.adressen.fehlschlag(email)
		m.seite(w, r, http.StatusUnauthorized, t.Falsch, eingabe)
		return
	}
	// Die Wartezeit der Adresse setzt erst der Code-Schritt zurück (#1075):
	// Sonst setzte jedes richtige Passwort auch die falschen Codes zurück.

	// Ab hier ist das Passwort richtig: Zustand und Tor dürfen deutlich
	// werden, die Person kennt ihr Konto.
	kein := func(t seitenTexte) (string, string) { return t.KeinZugang, t.KeinZugangText }
	switch k.Zustand {
	case storage.KontoAktiv:
	case storage.KontoWartet:
		textSeite(w, r, http.StatusForbidden, func(t seitenTexte) (string, string) { return t.Wartet, t.WartetText }, false)
		return
	default:
		textSeite(w, r, http.StatusForbidden, kein, false)
		return
	}
	if erlaubt, err := p.Tor(r.Context(), k.Email); err != nil {
		writeError(w, http.StatusInternalServerError, "access check failed", err)
		return
	} else if !erlaubt {
		textSeite(w, r, http.StatusForbidden, kein, false)
		return
	}
	if p.NachPasswort == nil {
		// 2FA ist Pflicht (ADR-0149 §3); ohne Code-Schritt keine Sitzung.
		textSeite(w, r, http.StatusForbidden, func(t seitenTexte) (string, string) { return t.ZweiFaktorFehlt, t.ZweiFaktorFehltText }, false)
		return
	}
	p.NachPasswort(w, r, k)
}

// passwortFormular: das Formular der Anmeldeseite. email ist die zuletzt
// eingegebene Adresse (wird escaped); fehler steht darüber.
func passwortFormular(t seitenTexte, email, fehler string) string {
	f := ""
	if fehler != "" {
		f = `<p role="alert"><strong>` + html.EscapeString(fehler) + `</strong></p>`
	}
	return f + `<form method="post" action="` + AnmeldungPasswortPfad + `">` +
		`<p><label>` + html.EscapeString(t.Email) + `<br><input type="email" name="email" autocomplete="username" required value="` + html.EscapeString(email) + `"></label></p>` +
		`<p><label>` + html.EscapeString(t.Passwort) + `<br><input type="password" name="passwort" autocomplete="current-password" required></label></p>` +
		`<p><button type="submit">` + html.EscapeString(t.Anmelden) + `</button></p></form>`
}

// beginneSitzung legt nach erfolgreicher Anmeldung die Sitzung an und setzt
// das Cookie. Eine mitgebrachte alte Sitzung endet vorher — eine womöglich
// abgegriffene ID lebt sonst bis zu ihrem Ablauf weiter (Sitzungsfixierung,
// K2 F-C, ADR-0149 §3 Kritik H3). Scheitert das Löschen, geht die Anmeldung
// trotzdem weiter; die alte Sitzung läuft dann regulär ab. Bei false ist die
// Antwort schon geschrieben. Gilt für jeden Anmeldeweg (OIDC-Rückruf,
// Code-Schritt #1075).
func beginneSitzung(w http.ResponseWriter, r *http.Request, s AnmeldeSitzungen, email, name string) bool {
	if alt, err := r.Cookie(SitzungsCookie); err == nil && alt.Value != "" {
		if err := s.Beenden(r.Context(), alt.Value); err != nil {
			log.Printf("aboard-server: anmeldung: alte Sitzung nicht beendet: %s", redactForLog(err))
		}
	}
	id, ablauf, err := s.Anlegen(r.Context(), email, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session create failed", err)
		return false
	}
	SetzeSitzungsCookie(w, id, ablauf)
	return true
}
