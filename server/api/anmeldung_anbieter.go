package api

// anmeldung_anbieter.go — mehrere OIDC-Anbieter (ADR-0149 §1, §4; Karte #1071).
//
// Statt eines einzelnen Anbieters gibt es eine Tabelle je Kürzel:
//
//	ABOARD_OIDC_PROVIDERS=google,authelia
//	ABOARD_OIDC_GOOGLE_CLIENT_ID=…   ABOARD_OIDC_GOOGLE_CLIENT_SECRET=…
//	ABOARD_OIDC_AUTHELIA_ISSUER=https://auth.example.org  …_CLIENT_ID …_CLIENT_SECRET …_LABEL
//
// Die bisherigen Variablen ABOARD_OIDC_ISSUER/_CLIENT_ID/_CLIENT_SECRET bilden
// den Anbieter mit dem reservierten Kürzel `standard` (pm); sein Rückruf
// bleibt /anmeldung/rueckruf. Jeder andere Anbieter ruft
// /anmeldung/rueckruf/<kürzel> zurück.
//
// Startprüfung (Kritik W2): Pflicht ist ein Anbieter nur, wenn eine seiner
// eigenen Variablen gesetzt ist; ein halb eingerichteter hält den Start an,
// ein ganz leerer wird nicht angeboten (Warnung). Variablen eines Kürzels,
// das nicht in ABOARD_OIDC_PROVIDERS steht, und unbekannte ABOARD_OIDC_*-Namen
// halten den Start an (Tippfehler sollen nicht still einen Anbieter abschalten).

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// AnbieterListeEnv nennt die Kürzel der OIDC-Anbieter.
	AnbieterListeEnv = "ABOARD_OIDC_PROVIDERS"
	// VorabListeEnv ist der englische Name der Vorab-Zulassung; der deutsche
	// VorabListeEnvAlt bleibt gültig (ADR-0149 §1).
	VorabListeEnv    = "ABOARD_OIDC_ALLOWED"
	VorabListeEnvAlt = "ABOARD_OIDC_ZUGELASSEN"
	// anbieterPraefix leitet jede Anbieter-Variable ein.
	anbieterPraefix = "ABOARD_OIDC_"
	// GoogleIssuer ist für das Kürzel google vorbelegt.
	GoogleIssuer = "https://accounts.google.com"
	// AnbieterParam wählt an /anmeldung/start den Anbieter.
	AnbieterParam = "anbieter"
)

// anbieterEndungen sind die Variablen je Kürzel: ABOARD_OIDC_<ID><Endung>.
// Der Doku-Wächter (tests/doku-umgebungsvariablen.test.js) liest diese Liste
// und verlangt je Endung eine Zeile `ABOARD_OIDC_<ID>_…` im Handbuch.
var anbieterEndungen = []string{"_ISSUER", "_CLIENT_ID", "_CLIENT_SECRET", "_LABEL"}

// altVariablen des Anbieters standard (ADR-0148).
var altVariablen = []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET"}

// AnbieterKonfig ist ein eingerichteter Anbieter.
type AnbieterKonfig struct {
	Kuerzel      string
	Issuer       string
	ClientID     string
	ClientSecret string
	Label        string
}

// anbieterEnvID macht aus einem Kürzel den Teil im Variablennamen
// (heim-2 → HEIM_2). Kürzel enthalten nie `_`, also ist das eindeutig.
func anbieterEnvID(k string) string {
	return strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
}

// standardLabel: Beschriftung, wenn _LABEL fehlt.
func standardLabel(k, issuer string) string {
	if issuer == GoogleIssuer {
		return "Google"
	}
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Host
	}
	return k
}

// AnbieterAusUmgebung liest alle Anbieter. getenv liest eine Variable,
// environ liefert alle (Form NAME=WERT), damit unbekannte Namen auffallen.
// Ergebnis: Anbieter in fester Reihenfolge (standard zuerst, dann wie in
// ABOARD_OIDC_PROVIDERS), Warnungen, Fehler (alle auf einmal, nicht nur der
// erste). Kein Anbieter ist kein Fehler (Passwort-Weg, #1074).
func AnbieterAusUmgebung(getenv func(string) string, environ []string) ([]AnbieterKonfig, []string, []string) {
	var out []AnbieterKonfig
	var warnungen, fehler []string
	wert := func(n string) string { return strings.TrimSpace(getenv(n)) }

	// pruefe: namen sind die Variablen für Issuer, Client-ID, Client-Secret —
	// Meldungen nennen sie wörtlich, damit der Betreiber weiß, was fehlt.
	pruefe := func(k string, namen [3]string, a AnbieterKonfig) {
		var fehlt []string
		if a.Issuer == "" {
			fehlt = append(fehlt, namen[0])
		} else if u, err := url.Parse(a.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
			fehler = append(fehler, fmt.Sprintf("%s=%q ist keine https-Adresse (Anbieter %s)", namen[0], a.Issuer, k))
			return
		}
		if a.ClientID == "" {
			fehlt = append(fehlt, namen[1])
		}
		if a.ClientSecret == "" {
			fehlt = append(fehlt, namen[2])
		}
		if len(fehlt) > 0 {
			fehler = append(fehler, fmt.Sprintf("Anbieter %s halb eingerichtet: %s fehlt", k, strings.Join(fehlt, ", ")))
			return
		}
		if a.Label == "" {
			a.Label = standardLabel(k, a.Issuer)
		}
		out = append(out, a)
	}

	// standard aus den bisherigen Variablen.
	altGesetzt := false
	for _, v := range altVariablen {
		altGesetzt = altGesetzt || wert(v) != ""
	}
	if altGesetzt {
		pruefe(storage.AnbieterStandard, [3]string{altVariablen[0], altVariablen[1], altVariablen[2]}, AnbieterKonfig{
			Kuerzel: storage.AnbieterStandard, Issuer: wert("ABOARD_OIDC_ISSUER"),
			ClientID: wert("ABOARD_OIDC_CLIENT_ID"), ClientSecret: wert("ABOARD_OIDC_CLIENT_SECRET"),
		})
	}

	// Kürzel aus ABOARD_OIDC_PROVIDERS.
	gelistet := map[string]bool{}
	for _, k := range strings.FieldsFunc(wert(AnbieterListeEnv), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	}) {
		switch {
		case !IstAnbieterKuerzel(k):
			fehler = append(fehler, fmt.Sprintf("%s: %q ist kein gültiges Kürzel (1–%d Zeichen a–z, 0–9, -)", AnbieterListeEnv, k, AnbieterKuerzelMax))
			continue
		case k == storage.AnbieterStandard || k == "password":
			fehler = append(fehler, fmt.Sprintf("%s: %q ist reserviert", AnbieterListeEnv, k))
			continue
		case gelistet[k]:
			fehler = append(fehler, fmt.Sprintf("%s: %q steht doppelt", AnbieterListeEnv, k))
			continue
		}
		gelistet[k] = true
		id := anbieterEnvID(k)
		eigen := map[string]string{}
		for _, e := range anbieterEndungen {
			eigen[e] = wert(anbieterPraefix + id + e)
		}
		if eigen["_ISSUER"] == "" && eigen["_CLIENT_ID"] == "" && eigen["_CLIENT_SECRET"] == "" && eigen["_LABEL"] == "" {
			warnungen = append(warnungen, fmt.Sprintf("Anbieter %s steht in %s, hat aber keine Zugangsdaten — wird nicht angeboten", k, AnbieterListeEnv))
			continue
		}
		issuer := eigen["_ISSUER"]
		if issuer == "" && k == "google" {
			issuer = GoogleIssuer
		}
		pruefe(k, [3]string{anbieterPraefix + id + "_ISSUER", anbieterPraefix + id + "_CLIENT_ID", anbieterPraefix + id + "_CLIENT_SECRET"}, AnbieterKonfig{Kuerzel: k, Issuer: issuer,
			ClientID: eigen["_CLIENT_ID"], ClientSecret: eigen["_CLIENT_SECRET"], Label: eigen["_LABEL"]})
	}

	// Unbekannte ABOARD_OIDC_*-Namen und Variablen fremder Kürzel.
	bekannt := map[string]bool{AnbieterListeEnv: true, VorabListeEnv: true, VorabListeEnvAlt: true, KontoReauthFallbackEnv: true}
	for _, v := range altVariablen {
		bekannt[v] = true
	}
	for k := range gelistet {
		for _, e := range anbieterEndungen {
			bekannt[anbieterPraefix+anbieterEnvID(k)+e] = true
		}
	}
	var fremd []string
	for _, kv := range environ {
		name, w, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, anbieterPraefix) && strings.TrimSpace(w) != "" && !bekannt[name] {
			fremd = append(fremd, name)
		}
	}
	sort.Strings(fremd)
	for _, name := range fremd {
		fehler = append(fehler, fmt.Sprintf("%s ist unbekannt — Tippfehler, oder fehlt das Kürzel in %s?", name, AnbieterListeEnv))
	}

	// Sitzungs-Kritik 02.11. K1-3: Die Bindung ist (iss, sub); zwei Kürzel mit
	// demselben Aussteller teilten sich jede Bindung, und eine Person käme über
	// beide herein, auch über ein per ABOARD_AUTH_METHODS nicht gemeintes.
	ausgabe := map[string]string{}
	for _, a := range out {
		if k, doppelt := ausgabe[a.Issuer]; doppelt {
			fehler = append(fehler, fmt.Sprintf("Anbieter %s und %s haben denselben Issuer %q — je Aussteller nur ein Anbieter", k, a.Kuerzel, a.Issuer))
			continue
		}
		ausgabe[a.Issuer] = a.Kuerzel
	}

	// Kein Anbieter ist seit #1074 kein Fehler: Der Passwort-Weg ist im
	// eingebauten Betrieb immer eingerichtet (ADR-0149 §1). Ob am Ende ein Weg
	// übrig bleibt, prüft FiltereAnbieter.
	return out, warnungen, fehler
}

// VorabListe liest die Vorab-Zulassung unter beiden Namen. Stehen beide mit
// verschiedenem Inhalt da, ist das ein Fehler (ADR-0149 §1).
func VorabListe(getenv func(string) string) (string, error) {
	neu, alt := strings.TrimSpace(getenv(VorabListeEnv)), strings.TrimSpace(getenv(VorabListeEnvAlt))
	if neu != "" && alt != "" && neu != alt {
		return "", fmt.Errorf("%s und %s widersprechen sich — nur einen der beiden Namen setzen", VorabListeEnv, VorabListeEnvAlt)
	}
	if neu != "" {
		return neu, nil
	}
	return alt, nil
}

// ---------------------------------------------------------------------------
// Anbieter-Tabelle

// Anmeldungen hält alle eingerichteten Anbieter (Reihenfolge wie
// AnbieterAusUmgebung).
type Anmeldungen struct {
	Anbieter []*OIDCAnmeldung
	// Passwort: der Passwort-Weg (#1074); nil = nicht angeboten
	// (ABOARD_AUTH_METHODS ohne `password`).
	Passwort *PasswortAnmeldung
}

// Abgemeldet: GET /anmeldung/abgemeldet — auch ohne jeden Anbieter.
func (m *Anmeldungen) Abgemeldet(w http.ResponseWriter, r *http.Request) {
	abgemeldetSeite(w, r)
}

// Finde liefert den Anbieter zum Kürzel oder nil.
func (m *Anmeldungen) Finde(k string) *OIDCAnmeldung {
	for _, a := range m.Anbieter {
		if a.kuerzel() == k {
			return a
		}
	}
	return nil
}

// Start: GET /anmeldung/start[?anbieter=<kürzel>]. Ohne Kürzel und mit genau
// einem Weg, der ein Anbieter ist, geht es direkt dorthin (pm); sonst führt der Server
// zur Anmeldeseite /anmeldung (Seite), die den Weg wählen lässt.
func (m *Anmeldungen) Start(w http.ResponseWriter, r *http.Request) {
	k := r.URL.Query().Get(AnbieterParam)
	switch {
	case k != "":
		if a := m.Finde(k); a != nil {
			a.Start(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		textSeite(w, r, http.StatusNotFound, sUnbekannt, true)
	case len(m.Anbieter) == 1 && m.Passwort == nil:
		m.Anbieter[0].Start(w, r)
	default:
		w.Header().Set("Cache-Control", "no-store")
		ziel := AnmeldungSeitePfad
		if r.URL.Query().Get(AnmeldungKontoWahlParam) == "1" {
			ziel += "?" + AnmeldungKontoWahlParam + "=1"
		}
		http.Redirect(w, r, ziel, http.StatusFound)
	}
}

// RueckrufStandard: GET /anmeldung/rueckruf — nur für den Anbieter standard.
func (m *Anmeldungen) RueckrufStandard(w http.ResponseWriter, r *http.Request) {
	m.rueckruf(w, r, storage.AnbieterStandard)
}

// RueckrufKuerzel: GET /anmeldung/rueckruf/{kuerzel}. standard ruft nie hier
// zurück (bei seinem Anbieter steht /anmeldung/rueckruf).
func (m *Anmeldungen) RueckrufKuerzel(w http.ResponseWriter, r *http.Request) {
	k := r.PathValue("kuerzel")
	if k == storage.AnbieterStandard {
		k = ""
	}
	m.rueckruf(w, r, k)
}

func (m *Anmeldungen) rueckruf(w http.ResponseWriter, r *http.Request, k string) {
	a := m.Finde(k)
	if k == "" || a == nil {
		w.Header().Set("Cache-Control", "no-store")
		setzeAnmeldeCookie(w, "", -1)
		textSeite(w, r, http.StatusNotFound, sUnbekannt, true)
		return
	}
	a.Rueckruf(w, r)
}
