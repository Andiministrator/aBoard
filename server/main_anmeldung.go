package main

// main_anmeldung.go — Startprüfungen und Verdrahtung der eingebauten Anmeldung
// (ADR-0148 §1, §3, §6; Karten #1051, #816).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

// checkAnmeldungKonfig prüft den Schalter ABOARD_ANMELDUNG und im OIDC-Betrieb
// die Pflichtvariablen (ADR-0148 §1). Läuft in runPreflight, vor jedem
// DB-/Netzwerk-Setup. ABOARD_PUBLIC_BASE_URL prüft pruefeAnmeldungBasis, weil
// main.go sie nur an einer Stelle liest (F-1559).
func checkAnmeldungKonfig() error {
	modus, err := api.AnmeldungModus()
	if err != nil {
		return err
	}
	// ADR-0149 Nachtrag #1099: Passwort nachträglich anlegen. Tippfehler im
	// Wert halten den Start in jedem Betrieb an; der Name fällt nicht unter die
	// Präfixe unten und wird deshalb ausdrücklich geprüft.
	selbstPasswort, selbstErr := api.KontoSelbstPasswort(os.Getenv)
	if selbstErr != nil {
		return selbstErr
	}
	// ADR-0149 Nachtrag 2026-11-09 (#1101): Tippfehler im Wert halten den
	// Start an; ohne eingebaute Anmeldung greift die Präfix-Prüfung unten.
	if _, err := api.KontoReauthFallback(os.Getenv); err != nil {
		return err
	}
	if modus == "" {
		if selbstPasswort {
			return fmt.Errorf("%s ist an, aber die eingebaute Anmeldung ist aus — %s=builtin setzen oder den Schalter entfernen",
				api.KontoSelbstPasswortEnv, api.AuthModeEnvVar)
		}
		// Fail-closed (K2 F-A, ADR-0149 §1 Kritik W2): Wer die eingebaute
		// Anmeldung einrichtet und den Schalter vergisst, bekäme sonst still den
		// auth-losen Einzelbetrieb — bei #1055 direkt am Netz, ohne vorgeschalteten Proxy
		// davor. Geprüft wird per Präfix, damit auch künftige Variablen
		// (ABOARD_OIDC_<ID>_…, ABOARD_AUTH_METHODS) mitzählen.
		if gesetzt := gesetzteAnmeldeVariablen(os.Environ()); len(gesetzt) > 0 {
			return fmt.Errorf("%s gesetzt, aber die eingebaute Anmeldung ist aus — der Server liefe ohne Anmeldung. "+
				"%s=builtin (gleichbedeutend %s=oidc) setzen oder diese Variablen entfernen", strings.Join(gesetzt, ", "),
				api.AuthModeEnvVar, api.AnmeldungEnvVar)
		}
		return nil
	}
	var fehler []string
	if api.TrustForwardAuthEnabled() {
		fehler = append(fehler, "ABOARD_TRUST_FORWARD_AUTH ist zugleich an — es gibt nie zwei Identitätsquellen; "+
			"eines von beiden ausschalten")
	}
	for _, v := range []string{"ABOARD_DEV_FAKE_USER", "ABOARD_DEV_FAKE_EMAIL"} {
		if os.Getenv(v) != "" {
			fehler = append(fehler, v+" ist gesetzt — der Dev-Modus spielt einen Proxy und hat im OIDC-Betrieb nichts verloren")
		}
	}
	// ADR-0149 §1, §4: Anbieter-Tabelle samt Kürzel-Variablen und den alten
	// Namen (Anbieter standard); Warnungen erst bei der Einrichtung loggen.
	// ABOARD_AUTH_METHODS (ADR-0149 §1, #1072, #1074): Tippfehler, Doppelte
	// und ein leeres Ergebnis halten den Start an; `password` ist ein Weg.
	_, _, anbieterFehler := api.AnbieterMitAuswahl(os.Getenv, os.Environ())
	fehler = append(fehler, anbieterFehler...)
	if selbstPasswort && !api.PasswortAngeboten(os.Getenv(api.AuthMethodsEnv)) {
		fehler = append(fehler, api.KontoSelbstPasswortEnv+" ist an, aber "+api.AuthMethodsEnv+" bietet den Weg password nicht an — "+
			"ein angelegtes Passwort ließe sich nie benutzen; password in "+api.AuthMethodsEnv+" aufnehmen oder den Schalter ausschalten")
	}
	if k := os.Getenv("ABOARD_SECRET_KEY"); k == "" {
		fehler = append(fehler, "ABOARD_SECRET_KEY fehlt (signiert den Anmeldevorgang; 32 Bytes hex oder base64)")
	} else if _, err := secretcrypto.New(k); err != nil {
		fehler = append(fehler, fmt.Sprintf("ABOARD_SECRET_KEY ungültig: %v", err))
	}
	if liste, err := api.VorabListe(os.Getenv); err != nil {
		fehler = append(fehler, err.Error())
	} else if _, err := api.NeueZulassung(liste, ""); err != nil {
		fehler = append(fehler, err.Error())
	}
	if len(fehler) > 0 {
		return errors.New("Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc) nicht startbar:\n  - " + strings.Join(fehler, "\n  - ") +
			"\nSiehe docs/handbuch/server/anmeldung.md")
	}
	return nil
}

// gesetzteAnmeldeVariablen nennt alle nicht leeren ABOARD_OIDC_*-,
// ABOARD_AUTH_*- und ABOARD_INVITE_*-Variablen aus env (Form NAME=WERT),
// sortiert. ABOARD_INVITE_* steht nicht in ADR-0149 §1, gehört aber zur
// selben Anmeldung (ABOARD_INVITE_METHODS). Die Schalter
// selbst zählen nicht (ABOARD_AUTH_MODE ist hier leer oder schon geprüft).
func gesetzteAnmeldeVariablen(env []string) []string {
	var out []string
	for _, kv := range env {
		name, wert, _ := strings.Cut(kv, "=")
		if name == api.AuthModeEnvVar || strings.TrimSpace(wert) == "" {
			continue
		}
		if strings.HasPrefix(name, "ABOARD_OIDC_") || strings.HasPrefix(name, "ABOARD_AUTH_") || strings.HasPrefix(name, "ABOARD_INVITE_") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// pruefeAnmeldungBasis: Im OIDC-Betrieb muss ABOARD_PUBLIC_BASE_URL gesetzt
// und https sein — aus ihr allein entsteht die Rückruf-Adresse (ADR-0148 §2).
// basis ist der schon geprüfte Wert aus oeffentlicheBasisGeprueft.
func pruefeAnmeldungBasis(basis string) error {
	if !api.AnmeldungOIDCAktiv() {
		return nil
	}
	if u, err := url.Parse(basis); err == nil && strings.HasPrefix(basis, "https://") && u.Path != "" && u.Path != "/" {
		// Kritik A-5: Der Server bedient /anmeldung/rueckruf an der Wurzel; eine
		// Basis mit Pfad ergäbe eine Rückruf-Adresse, die ins Leere geht.
		return fmt.Errorf("Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc): ABOARD_PUBLIC_BASE_URL=%q trägt einen Pfad — "+
			"die eingebaute Anmeldung braucht eine eigene (Sub-)Domain ohne Pfad", basis)
	}
	if !strings.HasPrefix(basis, "https://") {
		return fmt.Errorf("Eingebaute Anmeldung (ABOARD_AUTH_MODE=builtin / ABOARD_ANMELDUNG=oidc): ABOARD_PUBLIC_BASE_URL muss gesetzt und eine " +
			"https-Adresse sein (z. B. https://pm.example.de) — aus ihr entsteht die Rückruf-Adresse " +
			"<basis>/anmeldung/rueckruf, die beim Anbieter eingetragen ist")
	}
	return nil
}

// anmeldungEinrichten baut im OIDC-Betrieb Sitzungsspeicher, Zulassung und
// Ablauf, meldet die Routen am mux an und liefert die Option für die
// Menschen-Kette. Ausserhalb des OIDC-Betriebs: nichts.
//
// admin (höchstens einer, Karte #1077) bekommt die Kontenverwaltung
// (Bestätigen, Sperren, Zurücksetzen …) samt Tor und Passwort-Weg verdrahtet.
func anmeldungEinrichten(ctx context.Context, db *sql.DB, mux *http.ServeMux, basis string, admin ...*api.AdminAPI) ([]menschenOption, error) {
	if !api.AnmeldungOIDCAktiv() {
		return nil, nil
	}
	bootstrap, _, ok := storage.BootstrapOwnerAusEnv()
	if !ok {
		bootstrap = ""
	}
	liste, err := api.VorabListe(os.Getenv)
	if err != nil {
		return nil, err
	}
	zulassung, err := api.NeueZulassung(liste, bootstrap)
	if err != nil {
		return nil, err
	}
	anbieter, warnungen, fehler := api.AnbieterMitAuswahl(os.Getenv, os.Environ())
	if len(fehler) > 0 {
		return nil, errors.New("anmeldung: " + strings.Join(fehler, "; "))
	}
	for _, w := range warnungen {
		log.Printf("aboard-server: WARNUNG: anmeldung: %s", w)
	}
	konten := storage.NewKontoRepo(db)
	tor := api.NeuesTor(zulassung, konten)
	// ADR-0149 §5.7: Die Warnung richtet sich nach dem Tor — leer ist es erst,
	// wenn auch kein aktives Konto existiert.
	if zulassung.Leer() {
		if n, err := konten.AnzahlAktiv(ctx); err != nil {
			return nil, err
		} else if n == 0 {
			log.Printf("aboard-server: WARNUNG: anmeldung: niemand ist zugelassen — die Vorab-Liste (ABOARD_OIDC_ALLOWED/ABOARD_OIDC_ZUGELASSEN) und " +
				"ABOARD_BOOTSTRAP_OWNER sind leer, und es gibt kein aktives Konto. Jede Anmeldung endet mit „Kein Zugang“.")
		}
	}

	sitzungen := storage.NewSitzungRepo(db)
	if n, err := sitzungen.AbgelaufeneAbraeumen(ctx); err != nil {
		return nil, fmt.Errorf("sitzungen abräumen: %w", err)
	} else if n > 0 {
		log.Printf("aboard-server: anmeldung: %d abgelaufene Sitzung(en) abgeräumt", n)
	}
	// ADR-0149 §7 (#1076): abgelaufene Einladungs-Links und verbrauchte
	// Wiederherstellungscodes nach der Aufbewahrungsfrist.
	if n, err := storage.NewEinladungRepo(db).AbgelaufeneAbraeumen(ctx); err != nil {
		return nil, fmt.Errorf("einladungen abräumen: %w", err)
	} else if n > 0 {
		log.Printf("aboard-server: anmeldung: %d abgelaufene Einladungs-Link(s) bzw. verbrauchte Code(s) abgeräumt", n)
	}
	// Kritik A-2: Sitzungen gestrichener Adressen gehen beim Start, sonst
	// lebten sie bei einer Wiederaufnahme in die Liste wieder auf.
	if n, err := sitzungen.BeendeNichtZugelassene(ctx, tor.Erlaubt); err != nil {
		return nil, fmt.Errorf("sitzungen nicht zugelassener Adressen beenden: %w", err)
	} else if n > 0 {
		log.Printf("aboard-server: anmeldung: %d Sitzung(en) nicht mehr zugelassener Adressen beendet", n)
	}

	// ADR-0148 §6 / ADR-0129 §5: Anbieter-Verbindungen über die
	// Ausgangs-Schicht. Sitzungen, Bindungen, Tor und Schlüssel teilen sich
	// alle Anbieter; der Anmelde-Zustand trägt das Kürzel (Kritik W5).
	client := &http.Client{Transport: ausgang.Transport(nil), Timeout: 20 * time.Second}
	bindungen := storage.NewAnbieterBindungRepo(db, api.NutzerAnlegen(storage.NewBootstrapRepo(db)))
	schluessel := api.AnmeldeSchluessel(os.Getenv("ABOARD_SECRET_KEY"))
	tabelle := &api.Anmeldungen{}
	box, err := secretcrypto.New(os.Getenv("ABOARD_SECRET_KEY"))
	if err != nil {
		return nil, fmt.Errorf("anmeldung: ABOARD_SECRET_KEY: %w", err)
	}
	for _, k := range anbieter {
		k := k
		if k.Kuerzel == storage.AnbieterStandard {
			k.Kuerzel = ""
		}
		tabelle.Anbieter = append(tabelle.Anbieter, &api.OIDCAnmeldung{
			Kuerzel: k.Kuerzel, Label: k.Label,
			Issuer: k.Issuer, ClientID: k.ClientID, ClientSecret: k.ClientSecret,
			Basis: strings.TrimRight(basis, "/"), Schluessel: schluessel, Client: client,
			Sitzungen: sitzungen, Bindungen: bindungen, Zulassung: zulassung, Tor: tor.Erlaubt,
		})
	}
	mux.HandleFunc("GET "+api.AnmeldungSeitePfad, tabelle.Seite)
	mux.HandleFunc("GET "+api.AnmeldungStartPfad, tabelle.Start)
	mux.HandleFunc("GET "+api.AnmeldungRueckrufPfad, tabelle.RueckrufStandard)
	mux.HandleFunc("GET "+api.AnmeldungRueckrufPfad+"/{kuerzel}", tabelle.RueckrufKuerzel)
	// ADR-0149 §3 (#1074): Passwort-Weg, wenn ABOARD_AUTH_METHODS ihn anbietet
	// (leer = ja). Ohne ihn antwortet POST /anmeldung/passwort mit 404.
	if api.PasswortAngeboten(os.Getenv(api.AuthMethodsEnv)) {
		pw, err := api.NeuePasswortAnmeldung(konten, tor.Erlaubt, os.Getenv(api.AgentProxyEnvVar))
		if err != nil {
			return nil, err
		}
		// #1075: 2FA ist Pflicht; erst der Code-Schritt legt die Sitzung an.
		if err := pw.MitZweitemSchritt(konten, box, api.ZweiterSchrittSchluessel(os.Getenv("ABOARD_SECRET_KEY")), sitzungen); err != nil {
			return nil, err
		}
		tabelle.Passwort = pw
		// F-2894: Ohne ABOARD_AGENT_PROXY zählt die Bremse des Passwort-Formulars
		// je TCP-Gegenstelle — hinter einem Proxy alle Personen gemeinsam.
		if os.Getenv(api.AgentProxyEnvVar) == "" {
			log.Printf("aboard-server: WARNUNG: anmeldung: %s ist leer — die Sperre nach Fehlversuchen am Passwort-Formular zählt je TCP-Gegenstelle. "+
				"Steht ein Proxy davor, sperren fünf falsche Passwörter irgendeiner Person alle Passwort-Anmeldungen für bis zu 15 Minuten. "+
				"Proxy eintragen (z. B. ein Reverse-Proxy) und ihn X-Real-IP mit der Client-Adresse ÜBERSCHREIBEN lassen "+
				"(Caddy: header_up X-Real-IP {client_ip}; nginx: proxy_set_header X-Real-IP $remote_addr) — "+
				"sonst wählt der Client seine Adresse selbst; ohne Proxy davor ist nichts zu tun.", api.AgentProxyEnvVar)
		}
	}
	mux.HandleFunc("GET "+api.AnmeldungAbgemeldetPfad, tabelle.Abgemeldet)
	mux.HandleFunc("POST "+api.AnmeldungPasswortPfad, tabelle.PasswortPost)
	mux.HandleFunc("POST "+api.AnmeldungCodePfad, tabelle.CodePost)

	// ADR-0149 §5.4–§5.6 (#1077): Einladung einlösen; die Wege aus
	// ABOARD_INVITE_METHODS (geprüft in checkAnmeldungKonfig).
	wege, wegeFehler := api.EinladungsWege(anbieter, tabelle.Passwort != nil, os.Getenv(api.InviteMethodsEnv))
	if len(wegeFehler) > 0 {
		return nil, errors.New("anmeldung: " + strings.Join(wegeFehler, "; "))
	}
	einladungen := storage.NewEinladungRepo(db)
	nutzerAnlegen := api.NutzerAnlegen(storage.NewBootstrapRepo(db))
	// Nach jeder Änderung eines Kontos: Das Tor vergisst die Adresse, und ein
	// offener zweiter Anmeldeschritt des Kontos verfällt.
	geaendert := func(email, kontoID string) {
		tor.Vergessen(email)
		tabelle.Passwort.SchrittVerwerfen(kontoID)
	}
	einloesen := &api.EinladungEinloesen{
		Speicher: einladungen, Konten: konten, NutzerAnlegen: nutzerAnlegen, Box: box,
		Anmeldungen: tabelle, Wege: wege, Sitzungen: sitzungen, NachAenderung: geaendert,
	}
	for _, anm := range tabelle.Anbieter {
		anm.Einladung = einloesen
	}
	mux.HandleFunc("GET "+api.EinladungPfadPraefix+"{token}", einloesen.Seite)
	mux.HandleFunc("POST "+api.EinladungPfadPraefix+"{token}", einloesen.Post)
	// ADR-0149 §3, §4 (#1083): Konto-Einstellungen der angemeldeten Person.
	einstellungen, err := api.NeueKontoEinstellungen(konten, bindungen, sitzungen, tabelle, box, os.Getenv(api.AgentProxyEnvVar))
	if err != nil {
		return nil, err
	}
	// Audit-Protokoll der Änderungen (Sicherheitsprüfung #1083); AppendAudit braucht keinen Schlüssel.
	einstellungen.Audit = storage.NewConfigRepo(db, nil, uuid.NewString)
	// ADR-0149 Nachtrag #1099: geprüft in checkAnmeldungKonfig (Wert und
	// Passwort-Weg); hier zusätzlich nur mit eingerichtetem Passwort-Weg.
	if an, _ := api.KontoSelbstPasswort(os.Getenv); an && tabelle.Passwort != nil {
		einstellungen.SelbstPasswort = true
		log.Printf("aboard-server: Anmeldung: %s an — Personen ohne Passwort können sich nach frischer Anbieter-Anmeldung ein Passwort mit 2FA anlegen", api.KontoSelbstPasswortEnv)
	}
	if an, _ := api.KontoReauthFallback(os.Getenv); an {
		einstellungen.ReauthFallback = true
		log.Printf("aboard-server: Anmeldung: %s an — liefert ein Anbieter kein auth_time, genügt zur Bestätigung ein in diesem Vorgang frisch ausgestelltes Token (ADR-0149, Nachtrag 2026-11-09)", api.KontoReauthFallbackEnv)
	}
	einstellungen.Anmelden(mux)
	if len(admin) > 0 && admin[0] != nil {
		admin[0].WithKontenVerwaltung(kontenVerwaltung{konten, einladungen}, nutzerAnlegen, geaendert)
	}

	// Discovery im Hintergrund anstossen: Ist ein Anbieter nicht erreichbar,
	// startet der Server trotzdem; sein Start antwortet 503 und versucht es
	// erneut (ADR-0148 §6). Die anderen Anbieter bleiben nutzbar (ADR-0149 §2).
	for _, anm := range tabelle.Anbieter {
		anm := anm
		go func() {
			if _, err := anm.Anbieter(ctx); err != nil {
				log.Printf("aboard-server: anmeldung: Anbieter %s beim Start nicht erreichbar (%v) — sein Start "+
					"antwortet 503, bis er erreichbar ist", anm.Issuer, err)
				return
			}
			log.Printf("aboard-server: anmeldung: Anbieter %s erreichbar", anm.Issuer)
		}()
		log.Printf("aboard-server: Anmeldung: OIDC-Anbieter %q (%s), Rückruf %s%s", anm.Label, anm.Issuer, anm.Basis, anm.RueckrufPfad())
	}
	log.Printf("aboard-server: Anmeldung: eingebaut, %d Anbieter, Passwort %s, Wege beim Einlösen: %s; Forward-Auth aus", len(tabelle.Anbieter),
		map[bool]string{true: "an", false: "aus"}[tabelle.Passwort != nil], strings.Join(wege, ", "))
	return []menschenOption{mitSitzungsweiche(&api.Sitzungsweiche{Sitzungen: sitzungen, Zugelassen: tor.Erlaubt})}, nil
}

// kontenVerwaltung fasst Konto- und Einladungs-Repo für die Admin-API
// zusammen (api.KontenVerwaltung, Karte #1077).
type kontenVerwaltung struct {
	*storage.KontoRepo
	*storage.EinladungRepo
}
