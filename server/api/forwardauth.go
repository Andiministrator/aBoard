package api

// forwardauth.go — optionale Forward-Auth-Middleware (Karte #73, ADR-0057).
//
// Implementiert die Header-Trust-Schicht für Caddy + Authelia (Phase 2).
// Standardmäßig deaktiviert (ABOARD_TRUST_FORWARD_AUTH nicht gesetzt).
//
// SCOPE: Diese Datei stellt NUR die Identitäts-Schicht bereit (Header lesen,
// validieren, in Context ablegen, loggen) — keine users/acl/roles-Tabellen,
// die liegen im storage-Paket. Das ACL-Enforcement selbst sitzt NACH dieser
// Middleware in acl.go (Karte #198, ADR-0059/0066) und ist seit 2026-05-16
// umgesetzt (Commit b00e443), nicht mehr offen. Seit Sprint 70 gilt zusätzlich der strenge
// Modus (ADR-0129 §2, F-1124): withForwardAuth weist Requests ohne gültige
// Identität selbst ab (401), siehe dort.

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// forwardAuthSecretHeader ist der Header, über den Caddy das geteilte
// Forward-Auth-Secret mitschickt (ADR-0061 (a), F-262).
const forwardAuthSecretHeader = "X-Aboard-Forward-Auth-Secret"

// forwardAuthSecretOK prüft das Shared-Secret (ADR-0061 (a)).
//
//   - Ist ABOARD_FORWARD_AUTH_SECRET NICHT gesetzt → true. Im Betrieb kommt
//     das nicht vor: Bei aktivem Trust verweigert der Server seit ADR-0129 §2
//     den Start ohne Secret (checkForwardAuthSecret). Der Zweig bleibt für
//     Handler-Stacks, die am Start vorbei gebaut werden (Tests).
//   - Ist es gesetzt → true nur wenn der Request-Header
//     X-Aboard-Forward-Auth-Secret per konstant-Zeit-Vergleich exakt passt.
//
// Bei false antwortet withForwardAuth mit 401 (F-1124). Bis Sprint 70 lief
// der Request stattdessen ohne Identität weiter — in den Legacy-Bypass.
func forwardAuthSecretOK(r *http.Request) bool {
	// Getrimmt wie beim Startcheck (K-A5): Ein Zeilenumbruch aus einer
	// Secret-Datei liesse sonst den Start durch und sperrte zur Laufzeit jeden
	// Request aus. Der Proxy schickt den Wert ohne Umbruch.
	want := strings.TrimSpace(os.Getenv("ABOARD_FORWARD_AUTH_SECRET"))
	if want == "" {
		return true // Secret-Feature deaktiviert → unverändert wie bisher
	}
	got := r.Header.Get(forwardAuthSecretHeader)
	// subtle.ConstantTimeCompare ist nur bei gleicher Länge konstant-Zeit;
	// die Längen-Divergenz selbst leakt keine sicherheitsrelevante Info.
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// EnvSchalterAn liest eine Env-Variable als Ein/Aus-Schalter — die EINE
// Konvention dieses Servers.
//
// F-832 (c), Server-Kritiker Sprint 61: In dieser Datei standen zwei
// unterschiedliche Konventionen zwei Zeilen auseinander.
// `forwardAuthVerbose()` behandelte "0"/"false"/"off" als AUS, während
// ABOARD_TRUST_FORWARD_AUTH auf `!= ""` prüfte — dort schaltete `=0` also
// EIN. Beim Verbose-Schalter ist das Kosmetik; beim Trust-Schalter ist es
// ein Sicherheitsbefund: Ein Operator, der ABOARD_TRUST_FORWARD_AUTH=0
// setzt und "aus" meint, bekam eine Instanz, die Identitäts-Header aus dem
// Netz vertraut. Die Vereinheitlichung wirkt in die sichere Richtung —
// sie kann Trust nur ab-, nie anschalten.
//
// Bewusst pro Aufruf gelesen statt beim Start gecacht: Ein Operator, der im
// Betrieb hinsehen will, setzt die Variable und startet den Container neu —
// ein gecachter Wert wäre dasselbe, aber ein Test könnte ihn nicht mehr
// umschalten, und os.Getenv ist ein Map-Lookup, kein messbarer Kostenpunkt
// neben einem HTTP-Request.
func EnvSchalterAn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// TrustForwardAuthEnabled sagt, ob der Forward-Auth-Trust-Layer aktiv ist.
// Einzige Lesestelle für ABOARD_TRUST_FORWARD_AUTH — vorher stand dieselbe
// Prüfung an drei Stellen (hier 2×, server/forwardauth_bind.go 1×), und
// genau so driftete die Konvention (F-832 (c)).
func TrustForwardAuthEnabled() bool { return EnvSchalterAn("ABOARD_TRUST_FORWARD_AUTH") }

// forwardAuthVerbose sagt, ob die Identitäts-Zeile pro Request geschrieben
// wird (F-255). Default AUS.
func forwardAuthVerbose() bool { return EnvSchalterAn("ABOARD_FORWARD_AUTH_VERBOSE") }

// contextKey ist ein unexportierter Typ für Request-Context-Schlüssel im
// api-Paket. Verhindert Key-Kollisionen mit anderen Paketen.
type contextKey int

const (
	// contextKeyIdentity ist der Schlüssel für die Forward-Auth-Identität
	// im Request-Context.
	contextKeyIdentity contextKey = iota
)

// ForwardAuthIdentity enthält die vom Proxy weitergeleiteten Identitäts-
// informationen. Alle Felder sind optional (leer wenn der Proxy den Header
// nicht sendet).
//
// Mapping zu Authelia/Caddy-forward_auth-Headern:
//   - User  → Remote-User
//   - Email → Remote-Email
//   - Groups → Remote-Groups (kommagetrennte Liste)
//   - Name  → Remote-Name (Anzeigename; Authelia liefert ihn, oauth2-proxy
//     nicht — nur für die Anzeige, nie für Zuordnung oder ACL, siehe
//     AnzeigeName in anzeigename.go, Karte #577)
//
// Abgrenzung: Diese Struct dient nur der Identitäts-Weiterleitung — sie
// trägt kein Rollen-/Berechtigungsfeld. Die ACL-Prüfung (wer darf welches
// Board lesen/schreiben) liest diese Identität aus dem Context und
// entscheidet separat: server/api/acl.go, ACLGuard.Middleware (Karte #198,
// ADR-0059/0066).
type ForwardAuthIdentity struct {
	User   string
	Email  string
	Groups string
	Name   string
}

// IdentityFromContext liest die Forward-Auth-Identität aus dem Request-Context.
// Gibt nil zurück wenn keine Identität gesetzt ist — regulär nur bei
// TrustForwardAuthEnabled()==false (Flag deaktiviert, auth-loser
// Einzelbetrieb) oder auf den offenen Pfaden aus offenerPfad. Im strengen
// Modus (Trust AN, ADR-0129 §2) lehnt withForwardAuth jeden anderen Request
// ohne Identität bereits selbst mit 401 ab, bevor der Context hier gelesen
// wird — ein nil-Rückgabewert an anderer Stelle sollte dort also nicht mehr
// vorkommen.
func IdentityFromContext(ctx context.Context) *ForwardAuthIdentity {
	v, _ := ctx.Value(contextKeyIdentity).(*ForwardAuthIdentity)
	return v
}

// withForwardAuth ist die Forward-Auth-Middleware im strengen Modus
// (ADR-0129 §2, F-1124). Liest Remote-User, Remote-Email und Remote-Groups
// aus den Request-Headern und legt sie im Request-Context ab.
//
// Verhalten:
//   - Secret passt und Header vorhanden → Identität im Context. Geloggt wird
//     NUR, wenn ABOARD_FORWARD_AUTH_VERBOSE gesetzt ist (F-255): hinter
//     Authelia trägt JEDER Request die Header — das Board-Polling allein
//     erzeugt eine Zeile alle ~10 s pro offenem Tab, dazu jedes CRUD. Go-`log`
//     kennt keine Level, also gäbe es keinen Weg, das im Betrieb wieder
//     loszuwerden. Für die Fehlersuche bleibt die Zeile per Env eine Sekunde
//     entfernt.
//   - Secret falsch/fehlend ODER keine Identitäts-Header → 401. Bis Sprint 70
//     lief so ein Request als „nicht authentifiziert" weiter, und der
//     ACLGuard liess ihn per Legacy-Bypass (ADR-0059 §2) ohne jede Prüfung
//     durch: Ein falsches Secret stufte nicht ab, es öffnete alles (F-1124).
//     Wer den Server ohne Proxy erreichte, brauchte gar keine Header.
//   - Ausgenommen sind nur die Pfade aus offenerPfad (Healthcheck,
//     token-geschützter iCal-Feed). Sie laufen ohne Identität weiter.
//
// Der auth-lose Einzelbetrieb bleibt davon unberührt: Ist
// ABOARD_TRUST_FORWARD_AUTH aus, wird diese Middleware gar nicht verdrahtet
// (WithForwardAuthIfEnabled).
//
// Das Secret ist bei aktivem Trust Pflicht — erzwungen beim Start
// (checkForwardAuthSecret in server/forwardauth_bind.go), nicht hier. Ohne
// gesetztes Secret nimmt diese Funktion die Header weiterhin ungeprüft an;
// das erreicht nur, wer den Handler-Stack am Start vorbei baut (Tests).
//
// WICHTIG: Diese Middleware darf nur aktiv sein wenn der Container
// ausschließlich intern gebunden ist (127.0.0.1 oder Unix-Socket).
// Andernfalls kann ein Angreifer die Header selbst setzen (Spoofing).
func withForwardAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offenerPfad(r) {
			// Healthcheck und iCal-Feed tragen keine Proxy-Identität und
			// brauchen keine. Eine mitgeschickte Identität wird hier bewusst
			// NICHT übernommen: Beide Handler lesen sie nicht, und ein offener
			// Pfad soll nicht nebenbei zum Einstieg in den Erstkontakt-Pfad
			// des ACLGuard werden können.
			next.ServeHTTP(w, r)
			return
		}

		user := r.Header.Get("Remote-User")
		email := r.Header.Get("Remote-Email")
		groups := r.Header.Get("Remote-Groups")
		name := r.Header.Get("Remote-Name")

		// Ein Text für beide Fälle: Wer rät, soll nicht erfahren, ob das
		// Secret oder die Identität gefehlt hat.
		if secretOK := forwardAuthSecretOK(r); !secretOK || (user == "" && email == "") {
			// K-A5: Ohne diese Zeile war ein fehlkonfigurierter Proxy (Secret
			// fehlt, falsch, mit Umbruch) im Server-Log unsichtbar, während
			// jeder Nutzer 401 bekam und der Healthcheck grün blieb. Im Log
			// darf der Grund stehen, in der Antwort nicht. Nie das Secret oder
			// die Identität mitschreiben. Gedrosselt, weil ein offener Tab
			// alle paar Sekunden pollt.
			grund := "keine Identität"
			if !secretOK {
				grund = "Secret fehlt oder ist falsch"
			}
			if forwardAuthVerbose() {
				log.Printf("aboard-server: forward-auth: 401 path=%s (%s)", r.URL.Path, grund)
			} else if abweisungsLogErlaubt(time.Now()) {
				log.Printf("aboard-server: forward-auth: Anfragen werden mit 401 abgewiesen (%s) — Proxy-"+
					"Konfiguration prüfen (höchstens eine Zeile pro Minute; ABOARD_FORWARD_AUTH_VERBOSE=1 "+
					"zeigt jede)", grund)
			}
			writeError(w, http.StatusUnauthorized, "unauthorized: forward-auth identity required")
			return
		}

		// F-1511 (ADR-0132 §2): Eine Adresse mit "@" am Anfang ist ein
		// Domain-Schlüssel der ACL, keine Person. Nähme der Server sie als
		// Identität an, läse er Domain-Freigaben als persönliche Einträge —
		// ohne Blick auf ABOARD_ACL_DOMAINS — und der Erstkontakt legte eine
		// Domain als Owner an. 403, nicht 401: Die Anmeldung hat stattgefunden,
		// die Identität taugt nur nicht.
		if storage.IstDomainSubjekt(storage.NormalizeEmail(email)) {
			writeError(w, http.StatusForbidden, "forbidden: invalid identity email")
			return
		}

		id := &ForwardAuthIdentity{
			User:   user,
			Email:  email,
			Groups: groups,
			Name:   name,
		}
		if forwardAuthVerbose() {
			log.Printf("aboard-server: forward-auth: user=%q email=%q groups=%q name=%q path=%s",
				user, email, groups, name, r.URL.Path)
		}
		ctx := context.WithValue(r.Context(), contextKeyIdentity, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// offenerPfad nennt die Pfade, die im strengen Modus OHNE Proxy-Identität
// erreichbar bleiben (ADR-0129 §2). Die Liste ist abschliessend; ein neuer
// Eintrag braucht eine eigene Begründung, warum der Pfad ohne Anmeldung
// sicher ist.
//
//   - /healthz: Liveness-Check von Docker/Podman und Caddy. Sie senden keine
//     Remote-*-Header und kein Secret.
//   - /ical/calendar.ics: Kalender-Abos (Thunderbird, Google Kalender)
//     können keine Proxy-Anmeldung. Der Feed prüft sein eigenes Token
//     (ical.go).
//
// (Bis Karte #795 auch /ical/aussen/* für das Push-Skript der Außenstelle —
// zurückgebaut, ADR-0140 §11.)
//
// Nur GET und HEAD: Beide Pfade sind lesend, und für jede andere Methode
// gibt es keinen Grund, an der Anmeldung vorbeizukommen. Verglichen wird
// r.URL.Path exakt — dieselbe dekodierte Form, auf die der Mux matcht.
func offenerPfad(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// K-A9 (Kritiker Sprint 70): Der Mux routet nach dem KODIERTEN Pfad.
	// `/ical%2Fcalendar.ics` dekodiert zu `/ical/calendar.ics`, landet im Mux
	// aber im Catch-all. Beide offenen Pfade kommen ohne kodierte
	// Sonderzeichen aus; ist RawPath gesetzt, war etwas kodiert → nicht offen.
	if r.URL.RawPath != "" {
		return false
	}
	switch r.URL.Path {
	case "/healthz", "/ical/calendar.ics":
		return true
	}
	return false
}

// WithForwardAuthIfEnabled gibt withForwardAuth zurück wenn
// ABOARD_TRUST_FORWARD_AUTH gesetzt ist, sonst einen No-Op-Wrapper.
// Diese Funktion wird in main.go für den Middleware-Stack genutzt.
//
// Default = AUS: ohne die Env-Var identisches Verhalten wie heute
// (kein Context-Key, kein Log, kein Verhalten geändert).
func WithForwardAuthIfEnabled(h http.Handler) http.Handler {
	if !TrustForwardAuthEnabled() {
		return h
	}
	// EINE Zeile beim Verdrahten statt einer pro Request (F-255). Sie
	// beantwortet die Frage, für die die Request-Zeile eigentlich da war
	// („läuft die Forward-Auth überhaupt?"), und nennt den Schalter für den
	// Fall, dass jemand die Identitäten wirklich sehen muss.
	if !forwardAuthVerbose() {
		log.Printf("aboard-server: forward-auth aktiv (Identitäts-Log aus — " +
			"ABOARD_FORWARD_AUTH_VERBOSE=1 schaltet eine Zeile pro Request ein)")
	} else {
		log.Printf("aboard-server: forward-auth aktiv (ABOARD_FORWARD_AUTH_VERBOSE " +
			"gesetzt — eine Log-Zeile pro Request)")
	}
	return withForwardAuth(h)
}

// injectDevFakeHeaders ist eine Test-Middleware für den lokalen Betrieb
// ohne Caddy. Liest ABOARD_DEV_FAKE_USER, ABOARD_DEV_FAKE_EMAIL,
// ABOARD_DEV_FAKE_GROUPS und ABOARD_DEV_FAKE_NAME (F-1178: damit sich der
// Zweig „Remote-Name gewinnt“ aus #577 auch beim Durchklicken auslösen lässt)
// und injiziert sie als Remote-*-Header.
//
// SICHERHEITSHINWEIS: Diese Middleware darf NIEMALS in Produktion aktiv
// sein. Sie ist ausschließlich für lokale Entwicklung gedacht und wird
// nur aktiviert wenn ABOARD_DEV_FAKE_USER gesetzt ist.
func injectDevFakeHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := os.Getenv("ABOARD_DEV_FAKE_USER")
		email := os.Getenv("ABOARD_DEV_FAKE_EMAIL")
		groups := os.Getenv("ABOARD_DEV_FAKE_GROUPS")
		name := os.Getenv("ABOARD_DEV_FAKE_NAME")

		// Kopie des Requests mit modifizierten Headern erstellen.
		// Wir klonen den Header-Map, um den Original-Request nicht zu
		// verändern (andere Middleware könnte denselben r.Header lesen).
		clone := r.Clone(r.Context())
		// K-A10 (Kritiker Sprint 70): Erst alle Identitäts-Header des Clients
		// entfernen. Sonst übernähme der Dev-Layer bei leerem
		// ABOARD_DEV_FAKE_EMAIL eine vom Client gesendete Remote-Email — und
		// schickte dazu selbst das Secret mit, das gerade gegen solche
		// Nachbarprozesse schützen soll.
		for _, h := range identitaetsHeader {
			clone.Header.Del(h)
		}
		if user != "" {
			clone.Header.Set("Remote-User", user)
		}
		if email != "" {
			clone.Header.Set("Remote-Email", email)
		}
		if groups != "" {
			clone.Header.Set("Remote-Groups", groups)
		}
		if name != "" {
			clone.Header.Set("Remote-Name", name)
		}
		// Seit ADR-0129 §2 ist das Secret bei aktivem Trust Pflicht, und ohne
		// passendes Secret gibt es 401. Der Dev-Modus spielt den Proxy, also
		// schickt er es mit — sonst sperrte er sich selbst aus.
		if secret := os.Getenv("ABOARD_FORWARD_AUTH_SECRET"); secret != "" {
			clone.Header.Set(forwardAuthSecretHeader, secret)
		}

		next.ServeHTTP(w, clone)
	})
}

// WithDevFakeHeadersIfEnabled gibt injectDevFakeHeaders zurück wenn
// ABOARD_DEV_FAKE_USER gesetzt ist (Dev-Modus), sonst einen No-Op-Wrapper.
//
// PRODUKTIONS-SCHUTZ: Ist ABOARD_TRUST_FORWARD_AUTH NICHT gesetzt,
// wird dieser Dev-Wrapper komplett ignoriert — ein Dev-Fake ohne
// aktivierten Trust-Layer wäre wirkungslos und würde nur verwirren.
func WithDevFakeHeadersIfEnabled(h http.Handler) http.Handler {
	// Dev-Fake nur sinnvoll wenn Forward-Auth-Trust auch aktiv ist.
	if !TrustForwardAuthEnabled() {
		return h
	}
	// BEWUSST `!= ""` und NICHT `EnvSchalterAn` (Kritiker Sprint 61):
	// ABOARD_DEV_FAKE_USER ist kein Ein/Aus-Schalter, sondern ein WERT — der
	// Inhalt wandert als `Remote-User` in den Header. Mit der Falsy-Denylist
	// deaktivierte ein Entwickler, der `no`, `off`, `false` oder `0` als
	// Platzhalter-Username wählt, den Dev-Layer lautlos: kein Log, keine
	// Identität, und die Ursache steht in der Variablen, die er gerade
	// gesetzt hat. Die Konvention aus EnvSchalterAn gilt für Booleans;
	// dieser hier ist keins.
	if os.Getenv("ABOARD_DEV_FAKE_USER") == "" {
		return h
	}
	log.Printf("aboard-server: ⚠ DEV-MODUS: ABOARD_DEV_FAKE_USER gesetzt — " +
		"Fake-Auth-Header werden injiziert. NIEMALS in Produktion verwenden!")
	return injectDevFakeHeaders(h)
}

// abweisungsLogZuletzt drosselt die Warnzeile für abgewiesene Anfragen auf
// eine pro Minute (K-A5). Unix-Nanosekunden, atomar gelesen und gesetzt.
var abweisungsLogZuletzt atomic.Int64

// abweisungsLogErlaubt sagt, ob jetzt eine Warnzeile geschrieben werden darf,
// und merkt sich den Zeitpunkt. Bei gleichzeitigen Aufrufen gewinnt genau
// einer.
func abweisungsLogErlaubt(jetzt time.Time) bool { return drosselErlaubt(&abweisungsLogZuletzt, jetzt) }

// drosselErlaubt: höchstens ein true pro Minute je Zähler z. Bei gleichzeitigen
// Aufrufen gewinnt genau einer. Auch für die Sitzungsweiche (sitzung.go).
func drosselErlaubt(z *atomic.Int64, jetzt time.Time) bool {
	zuletzt := z.Load()
	if zuletzt != 0 && jetzt.UnixNano()-zuletzt < int64(time.Minute) {
		return false
	}
	return z.CompareAndSwap(zuletzt, jetzt.UnixNano())
}
