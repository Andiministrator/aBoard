package api

// sitzung.go — Anmelde-Weiche für den OIDC-Betrieb (ADR-0148 §1, §2, §4;
// Karte #1051).
//
// Im OIDC-Betrieb (ABOARD_ANMELDUNG=oidc) ersetzt diese Middleware
// withForwardAuth in der Menschen-Kette (main.go menschenHandler). Sie legt
// dieselbe ForwardAuthIdentity in den Request-Context, ACL, Erstkontakt, Admin
// und Domain-Freigaben laufen dahinter unverändert.
//
// Grundsätze:
//   - Der Server glaubt keiner Kopfzeile. Remote-* und das Forward-Auth-Secret
//     werden als Erstes entfernt.
//   - Ausnahmen von der Anmeldung entscheidet allein der echte Pfad
//     (r.URL.Path, RawPath leer, exakter Vergleich) — nie X-Forwarded-Uri,
//     X-Original-URL oder Ähnliches (Randbedingung 5, Falle F-945 K-1).
//   - Fail-closed: Ohne Sitzungsspeicher oder ohne Zulassungsprüfung kommt
//     niemand herein.
//
// Was NICHT hier steht: der Ablauf /anmeldung/start und /anmeldung/rueckruf
// (Karte #816) und die Vorab-Liste ABOARD_OIDC_ALLOWED/_ZUGELASSEN (ebenfalls
// #816, sie wird als Funktion Zugelassen eingehängt).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// SitzungsCookie trägt die Sitzungs-ID. Präfix __Host-: nur mit Secure,
	// ohne Domain und mit Path=/ — der Browser nimmt ihn sonst nicht an.
	SitzungsCookie = "__Host-aboard_sitzung"
	// AnmeldungStartPfad, AnmeldungRueckrufPfad, AnmeldungEndePfad: die drei
	// Wege der eingebauten Anmeldung. Start und Rückruf sind ohne Sitzung
	// erreichbar (nur GET/HEAD), Ende nur als POST.
	AnmeldungStartPfad    = "/anmeldung/start"
	AnmeldungRueckrufPfad = "/anmeldung/rueckruf"
	AnmeldungEndePfad     = "/anmeldung/ende"
	// AnmeldungAbgemeldetPfad: Zielseite nach dem Abmelden, ohne Sitzung
	// erreichbar (K2 F-B). AnmeldungKontoWahlParam=1 an /anmeldung/start
	// verlangt beim Anbieter die Kontoauswahl (prompt=select_account).
	AnmeldungAbgemeldetPfad = "/anmeldung/abgemeldet"
	AnmeldungKontoWahlParam = "konto"
	// AnmeldungEnvVar ist der bisherige Schalter der Betriebsart (ADR-0148 §1,
	// Wert oidc). Er bleibt gültig (pm).
	AnmeldungEnvVar = "ABOARD_ANMELDUNG"
	// AuthModeEnvVar ist der englische Name desselben Schalters (ADR-0149 §1,
	// Wert builtin; oidc bleibt gleichbedeutend).
	AuthModeEnvVar = "ABOARD_AUTH_MODE"
	// AnmeldungEingebaut ist der Wert für die eingebaute Anmeldung.
	AnmeldungEingebaut = "builtin"
)

// anmeldungWert normalisiert einen Schalterwert: "" bleibt "", builtin und
// oidc (Groß-/Kleinschreibung egal) werden "builtin", alles andere ist ungültig.
func anmeldungWert(name string) (string, error) {
	roh := strings.TrimSpace(os.Getenv(name))
	switch strings.ToLower(roh) {
	case "":
		return "", nil
	case AnmeldungEingebaut, "oidc":
		return AnmeldungEingebaut, nil
	}
	return "", fmt.Errorf("%s=%q ungültig (erwartet builtin, oidc oder leer)", name, roh)
}

// AnmeldungModus liest beide Schalter (ABOARD_AUTH_MODE, ABOARD_ANMELDUNG).
// Ergebnis "" (Einzelbetrieb/Forward-Auth) oder "builtin". Ein ungültiger
// Wert unter einem der Namen ist ein Fehler (ADR-0149 §1).
func AnmeldungModus() (string, error) {
	neu, err := anmeldungWert(AuthModeEnvVar)
	if err != nil {
		return "", err
	}
	alt, err := anmeldungWert(AnmeldungEnvVar)
	if err != nil {
		return "", err
	}
	// Beide Namen kennen nur „builtin“ (oidc wird darauf abgebildet); ein
	// Widerspruch zwischen zwei gültigen Werten ist deshalb unmöglich, und ein
	// ungültiger Wert ist oben schon ein Fehler (Sitzungs-Kritik 02.11. K2-4).
	if neu != "" {
		return neu, nil
	}
	return alt, nil
}

// AnmeldungOIDCAktiv sagt, ob die eingebaute Anmeldung eingeschaltet ist
// (ABOARD_AUTH_MODE=builtin oder ABOARD_ANMELDUNG=oidc). Der Name stammt aus
// ADR-0148, als es nur OIDC gab. Ungültige Werte zählen als aus; die
// Startprüfung (checkAnmeldungKonfig) hält den Server dann ohnehin an.
func AnmeldungOIDCAktiv() bool {
	m, err := AnmeldungModus()
	return err == nil && m == AnmeldungEingebaut
}

// SitzungsSpeicher ist der Teil von storage.SitzungRepo, den die Weiche
// braucht.
type SitzungsSpeicher interface {
	Pruefen(ctx context.Context, klartext string) (*storage.Sitzung, error)
	Beenden(ctx context.Context, klartext string) error
}

// Sitzungsweiche ist die Anmelde-Middleware des OIDC-Betriebs.
type Sitzungsweiche struct {
	// Sitzungen: nil heisst „keine Sitzung ist gültig“ (fail-closed).
	Sitzungen SitzungsSpeicher
	// Zugelassen prüft bei JEDER Anfrage, ob die Adresse der Sitzung noch
	// herein darf — das Tor aus ADR-0149 §5.1 (Vorab-Liste oder aktives
	// Konto, gesperrt nie). nil heisst „niemand“; ein Fehler heisst 500.
	Zugelassen func(ctx context.Context, email string) (bool, error)
}

// Wege der Anmeldung, die ohne Sitzung erreichbar sein müssen (ADR-0149 §5.8,
// Kritik B4). Die POST-Wege sind hier festgelegt, damit Sprint 110 (#1074,
// #1075, #1077) sie nicht neu erfinden muss:
const (
	// AnmeldungSeitePfad: Anmeldeseite mit Auswahl der Wege (#1072).
	AnmeldungSeitePfad = "/anmeldung"
	// AnmeldungPasswortPfad: POST E-Mail-Adresse und Passwort (#1074).
	AnmeldungPasswortPfad = "/anmeldung/passwort"
	// AnmeldungCodePfad: POST 2FA- oder Wiederherstellungscode (#1075).
	AnmeldungCodePfad = "/anmeldung/code"
	// EinladungPfadPraefix: GET zeigt das Formular, POST löst ein (#1076/#1077).
	// Danach folgt das Token (EinladungTokenLaenge Zeichen base64url).
	EinladungPfadPraefix = "/einladung/"
	// EinladungTokenLaenge: 256 Bit als base64url ohne Auffüllung.
	EinladungTokenLaenge = 43
	// AnbieterKuerzelMax: längstes Kürzel eines OIDC-Anbieters im Pfad
	// /anmeldung/rueckruf/<kürzel> (#1071).
	AnbieterKuerzelMax = 32
)

// anmeldungOhneSitzung: die Wege der Anmeldung, die ohne Sitzung erreichbar
// sind — sonst käme niemand zur ersten Sitzung (ADR-0149 §5.8). Nur der
// exakte, unkodierte Pfad (RawPath leer, wie offenerPfad), feste Pfade und
// zwei Muster (Kürzel, Einladungs-Token); jede Methode namentlich.
func anmeldungOhneSitzung(r *http.Request) bool {
	if r.URL.RawPath != "" {
		return false
	}
	p := r.URL.Path
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		switch p {
		case AnmeldungSeitePfad, AnmeldungStartPfad, AnmeldungRueckrufPfad, AnmeldungAbgemeldetPfad:
			return true
		}
		if k, ok := strings.CutPrefix(p, AnmeldungRueckrufPfad+"/"); ok {
			return IstAnbieterKuerzel(k)
		}
		if t, ok := strings.CutPrefix(p, EinladungPfadPraefix); ok {
			return istEinladungToken(t)
		}
	case http.MethodPost:
		switch p {
		case AnmeldungPasswortPfad, AnmeldungCodePfad:
			return true
		}
		if t, ok := strings.CutPrefix(p, EinladungPfadPraefix); ok {
			return istEinladungToken(t)
		}
	}
	return false
}

// Seit #1077 sind alle Wege ohne Sitzung gebaut; der frühere Platzhalter
// (AnmeldewegePlatzhalter, 404 statt App) ist entfallen. Jeder Weg aus
// anmeldungOhneSitzung hat in main_anmeldung.go eine eigene Route — fehlte
// eine, fiele der Pfad auf den Catch-all der App (TestAnmeldungEinrichten1070
// prüft das je Pfad).

// IstAnbieterKuerzel: 1–32 Zeichen aus [a-z0-9-] (ADR-0149 §1). Reservierte
// Kürzel prüft die Startprüfung, nicht dieses Muster.
func IstAnbieterKuerzel(k string) bool {
	if k == "" || len(k) > AnbieterKuerzelMax {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// istEinladungToken: genau EinladungTokenLaenge Zeichen base64url.
func istEinladungToken(t string) bool {
	if len(t) != EinladungTokenLaenge {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ohneAnmeldungErlaubt fasst die Ausnahmen beider strengen Betriebsarten
// zusammen. Im Forward-Auth-Betrieb gehören die Anmeldewege nicht dazu.
func ohneAnmeldungErlaubt(r *http.Request) bool {
	return offenerPfad(r) || (AnmeldungOIDCAktiv() && anmeldungOhneSitzung(r))
}

// strengeAnmeldung sagt, ob eine Anfrage ohne Identität abzuweisen ist
// (Forward-Auth im strengen Modus oder OIDC). Nur im auth-losen
// Einzelbetrieb gilt der Legacy-Bypass des ACLGuard.
func strengeAnmeldung() bool { return TrustForwardAuthEnabled() || AnmeldungOIDCAktiv() }

// StrengeAnmeldung sagt, ob der Server im Mehrnutzer-Betrieb mit Anmeldung
// läuft (Forward-Auth oder OIDC) — die eine Quelle für „Mehrnutzer“ (Kritik A-6).
func StrengeAnmeldung() bool { return strengeAnmeldung() }

// Middleware baut die Weiche um next.
func (s *Sitzungsweiche) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ADR-0148 §1: Im OIDC-Betrieb glaubt der Server keiner Kopfzeile.
		// Kein Handler dahinter soll je ein vom Client gesetztes Remote-*
		// oder das Proxy-Secret sehen.
		IdentitaetsHeaderEntfernen(r)

		if r.URL.Path == AnmeldungEndePfad && r.URL.RawPath == "" {
			s.abmelden(w, r)
			return
		}
		if ohneAnmeldungErlaubt(r) {
			next.ServeHTTP(w, r)
			return
		}

		sitzung, ok := s.gueltigeSitzung(w, r)
		if !ok {
			return
		}
		id := &ForwardAuthIdentity{Email: sitzung.Email, Name: sitzung.Name}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKeyIdentity, id)))
	})
}

// gueltigeSitzung liefert die Sitzung der Anfrage oder hat schon geantwortet
// (302/401/403/500).
func (s *Sitzungsweiche) gueltigeSitzung(w http.ResponseWriter, r *http.Request) (*storage.Sitzung, bool) {
	c, err := r.Cookie(SitzungsCookie)
	if err != nil || c.Value == "" || s.Sitzungen == nil {
		ohneSitzungAbweisen(w, r)
		return nil, false
	}
	sitzung, err := s.Sitzungen.Pruefen(r.Context(), c.Value)
	if errors.Is(err, storage.ErrSitzungUnbekannt) {
		ohneSitzungAbweisen(w, r)
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session check failed", err)
		return nil, false
	}
	email := storage.NormalizeEmail(sitzung.Email)
	if email == "" || storage.IstDomainSubjekt(email) {
		// Die Tabelle lässt beides nicht zu; doppelt hält besser.
		ohneSitzungAbweisen(w, r)
		return nil, false
	}
	erlaubt := false
	if s.Zugelassen != nil {
		var err error
		if erlaubt, err = s.Zugelassen(r.Context(), email); err != nil {
			writeError(w, http.StatusInternalServerError, "access check failed", err)
			return nil, false
		}
	}
	if !erlaubt {
		// ADR-0148 §3: gestrichen heisst draussen, auch mit gültiger Sitzung.
		// Die Sitzung bleibt in der Tabelle; sie läuft ab oder wird beim
		// Abmelden gelöscht. Die Adresse steht nicht im Log (gedrosselt
		// protokolliert der Rückruf in #816).
		if sitzungAbweisLogErlaubt(time.Now()) {
			log.Printf("aboard-server: anmeldung: Sitzung einer nicht (mehr) zugelassenen Adresse abgewiesen " +
				"(höchstens eine Zeile pro Minute)")
		}
		ohneSitzungAbweisen(w, r)
		return nil, false
	}
	sitzung.Email = email
	return sitzung, true
}

// abmelden: POST /anmeldung/ende löscht die Sitzung und den Cookie. Andere
// Methoden: 405. Der CSRF-Schutz liegt aussen (MitCSRFSchutz) und greift, weil
// POST. Eine fehlende oder unbekannte Sitzung ist kein Fehler.
func (s *Sitzungsweiche) abmelden(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if c, err := r.Cookie(SitzungsCookie); err == nil && c.Value != "" && s.Sitzungen != nil {
		if err := s.Sitzungen.Beenden(r.Context(), c.Value); err != nil {
			writeError(w, http.StatusInternalServerError, "logout failed", err)
			return
		}
	}
	LoescheSitzungsCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// ohneSitzungAbweisen: Seitenaufrufe gehen zur Anmeldung, alles andere bekommt
// 401 JSON (ADR-0148 §4). Seitenaufruf heisst GET/HEAD mit text/html im
// Accept-Header — so fragt der Browser bei einer Navigation, fetch() fragt mit
// */* und bekommt 401, das das Frontend auswertet (Karte #1053).
func ohneSitzungAbweisen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
		http.Redirect(w, r, AnmeldungSeitePfad, http.StatusFound)
		return
	}
	writeError(w, http.StatusUnauthorized, "unauthorized: session required")
}

// SetzeSitzungsCookie legt den Sitzungs-Cookie (Rückruf, Karte #816). Er lebt
// höchstens bis zur harten Grenze der Sitzung; den Leerlauf prüft der Server.
func SetzeSitzungsCookie(w http.ResponseWriter, klartext string, ablauf time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SitzungsCookie,
		Value:    klartext,
		Path:     "/",
		Expires:  ablauf,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// LoescheSitzungsCookie entfernt den Sitzungs-Cookie im Browser.
func LoescheSitzungsCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SitzungsCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// sitzungAbweisLogZuletzt drosselt die Warnzeile für nicht zugelassene
// Sitzungen auf eine pro Minute (wie abweisungsLogErlaubt).
var sitzungAbweisLogZuletzt atomic.Int64

func sitzungAbweisLogErlaubt(jetzt time.Time) bool {
	return drosselErlaubt(&sitzungAbweisLogZuletzt, jetzt)
}
