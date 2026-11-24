package api

// me.go — /me-Endpoint (Karte #201, ADR-0059 Frontend-Schicht).
//
// Gibt die Forward-Auth-Identität des anfragenden Users zurück.
// Nur relevant wenn ABOARD_TRUST_FORWARD_AUTH gesetzt ist — im Legacy-
// Bypass (kein Kontext, Single-User-Modus) antwortet der Endpoint mit
// null-Feldern, damit das Frontend den richtigen Pfad einschlägt.
//
// GET /me → { name, email, is_admin, owui_url, anmeldung?, schleuse?, wissen? } | { name: null, email: null, is_admin: false }
//
// #306/ADR-0101: is_admin wird mitgeliefert, damit das Frontend die Sidebar-
// Aktion "🔧 Server-Administration" gating kann (nur bei is_admin=true).
//
// Diese Route liegt außerhalb von /boards/* — der ACL-Guard greift hier
// nicht. Der Endpoint ist informativ: er liefert, was die Forward-Auth-
// Middleware für diesen Request im Context abgelegt hat (leer = kein Auth).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/Andiministrator/aBoard/server/storage"
)

// RegisterMeHandler hängt GET /me an einen ServeMux. userRepo darf nil
// sein (Legacy-Modus ohne is_admin-Anreicherung); dann fällt is_admin
// auf false zurück.
func RegisterMeHandler(mux *http.ServeMux, userRepo *storage.UserRepo, opts ...MeOption) {
	var cfg meConfig
	for _, o := range opts {
		o(&cfg)
	}
	mux.Handle("GET /me", WithDefaults(meHandler(userRepo, cfg)))
}

// meConfig trägt Betreiber-Einstellungen, die das Frontend aus /me liest.
type meConfig struct {
	owuiURL  string
	schleuse bool
	wissen   func(context.Context, *storage.User) string
	// boardpaar: Instanz-Einstellung boardpaar.seite (Karte #778).
	boardpaar *storage.ConfigRepo
}

// MeOption passt RegisterMeHandler an.
type MeOption func(*meConfig)

// MeWithOWUIURL gibt die (bereits mit ValidateOWUIURL geprüfte) Adresse der
// Open-WebUI-Instanz an das Frontend weiter (Karte #672, ADR-0133 §7). Leer =
// kein Knopf „In Open WebUI weiterbearbeiten“. Die Adresse ist kein Geheimnis
// (der Knopf öffnet sie im Browser), steht aber nur im /me-Zweig mit Identität
// und im Bypass — beides liefert den Schlüssel `owui_url`.
func MeWithOWUIURL(u string) MeOption {
	return func(c *meConfig) { c.owuiURL = u }
}

// ValidateOWUIURL prüft ABOARD_OWUI_URL: nur http/https, Host nötig, keine
// Zugangsdaten, keine Query, kein Fragment (der Knopf hängt `/?submit=false&q=…`
// selbst an). Leer ist erlaubt (Funktion aus). Ein Pfad ist erlaubt
// (OWUI hinter Unterpfad); ein abschließender Schrägstrich wird entfernt.
func ValidateOWUIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	fehler := func(grund string) error {
		return fmt.Errorf("ABOARD_OWUI_URL: %s", grund)
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fehler("enthält Leerraum")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fehler("keine gültige URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fehler(fmt.Sprintf("Schema %q — erlaubt sind nur http und https", u.Scheme))
	}
	if u.Hostname() == "" {
		return "", fehler("kein Host")
	}
	if u.User != nil {
		return "", fehler("keine Zugangsdaten in der URL erlaubt (user:pass@)")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fehler("keine Query erlaubt")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fehler("kein Fragment erlaubt")
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// MeWithWissen meldet der Oberfläche den Namen der Wissensquelle, wenn sie
// an ist und die Person sie sehen darf (Karte #739, ADR-0137 §3). Sonst fehlt
// das Feld `wissen`, und die Seitenleiste zeigt keinen Eintrag.
func MeWithWissen(fn func(context.Context, *storage.User) string) MeOption {
	return func(c *meConfig) { c.wissen = fn }
}

// MeWithBoardpaar meldet der Oberfläche die Seite dieser Instanz im
// Board-Paar (ADR-0140 §4, Karte #778): `boardpaar: "innen" | "aussen"`, je
// Anfrage gelesen. Bei „aus“ fehlt das Feld — die Oberfläche zeigt dann
// keinen Schalter „vertraulich“. Außen steht bei boardpaar.rueckweg = an
// zusätzlich `boardpaar_rueckweg: "an"` (Karte #827 E4). Instanzweit, kein Geheimnis: auch ohne
// Identität (Bypass). Seit #836 (ADR-0141 Nachtrag) ist das nur die Hälfte:
// Bearbeitbar ist eine Kopie erst, wenn zusätzlich ihr eigener Schalter
// „bearbeitbar“ steht — den liefert der Board-Abruf GET /boards/{id}/schatten.
func MeWithBoardpaar(cfg *storage.ConfigRepo) MeOption {
	return func(c *meConfig) { c.boardpaar = cfg }
}

// MeWithSchleuse meldet der Oberfläche, dass die Ausgangsschleuse an ist
// (Karte #684, ADR-0134 §1). Nur dann trägt /me `schleuse: "an"`; bei „aus“
// fehlt das Feld, und die Oberfläche zeigt nichts davon.
func MeWithSchleuse(an bool) MeOption {
	return func(c *meConfig) { c.schleuse = an }
}

func meHandler(userRepo *storage.UserRepo, cfg meConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil || (id.User == "" && id.Email == "") {
			body := map[string]any{
				"name":     nil,
				"email":    nil,
				"is_admin": false,
				"owui_url": cfg.owuiURL,
			}
			mitAnmeldung(body)
			cfg.mitBoardpaar(r.Context(), body)
			writeJSON(w, http.StatusOK, body)
			return
		}
		isAdmin := false
		var nutzer *storage.User
		if userRepo != nil {
			email := storage.NormalizeEmail(id.Email)
			if email != "" {
				if u, err := userRepo.GetByEmail(r.Context(), email); err == nil {
					isAdmin = u.IsAdmin
					nutzer = u
				} else if !errors.Is(err, storage.ErrUserNotFound) {
					// Lookup-Fehler — kein Crash, kein is_admin=true.
					isAdmin = false
				}
			}
		}
		body := map[string]any{
			"name":     id.AnzeigeName(), // Karte #577: nie die numerische Google-ID
			"email":    id.Email,
			"is_admin": isAdmin,
			"owui_url": cfg.owuiURL,
		}
		mitAnmeldung(body)
		// Karte #684: nur mit Identität — die Schleuse ist an Personen gebunden.
		if cfg.schleuse {
			body["schleuse"] = "an"
		}
		if cfg.wissen != nil {
			if name := cfg.wissen(r.Context(), nutzer); name != "" {
				body["wissen"] = name
			}
		}
		cfg.mitBoardpaar(r.Context(), body)
		writeJSON(w, http.StatusOK, body)
	}
}

// mitAnmeldung trägt `anmeldung: "oidc"` ein, wenn die eingebaute Anmeldung
// läuft (Karte #1053, ADR-0148 §5). Daran erkennt die Oberfläche, dass ein 401
// „Sitzung abgelaufen“ heißt und der Knopf „Abmelden“ etwas tut. Sonst fehlt das
// Feld: im Forward-Auth-Betrieb ändert sich nichts. Kein Geheimnis.
func mitAnmeldung(body map[string]any) {
	if AnmeldungOIDCAktiv() {
		body["anmeldung"] = "oidc"
	}
}

// mitBoardpaar trägt `boardpaar` ein, wenn die Instanz Teil eines
// Board-Paars ist. Ist server_config nicht lesbar, steht statt der Seite
// `boardpaar_fehler: true` (F-2020): Der Browser merkt sich dann kein „aus“
// und fragt beim nächsten Mal neu, statt Schalter „vertraulich“ und Band
// „Intern“ bis zum Neuladen auszublenden. /me selbst bleibt 200 — an ihm
// hängt die Anmeldung, nicht nur das Board-Paar.
func (c meConfig) mitBoardpaar(ctx context.Context, body map[string]any) {
	if c.boardpaar == nil {
		return
	}
	seite, err := BoardpaarSeiteMitFehler(ctx, c.boardpaar)
	if err != nil {
		log.Printf("boardpaar: seite für /me nicht lesbar: %v", err)
		body["boardpaar_fehler"] = true
		return
	}
	if seite == BoardpaarSeiteAussen {
		// Karte #827 E4 (Plan D10): Der Browser macht Spiegelkarten nur bei
		// `boardpaar_rueckweg: "an"` bearbeitbar; fehlt das Feld, bleiben sie
		// nur lesbar. Unlesbar → wie bei der Seite: nichts merken, neu fragen.
		an, err := BoardpaarRueckwegAnMitFehler(ctx, c.boardpaar)
		if err != nil {
			log.Printf("boardpaar: rueckweg für /me nicht lesbar: %v", err)
			body["boardpaar_fehler"] = true
			return
		}
		if an {
			body["boardpaar_rueckweg"] = BoardpaarRueckwegAn
		}
	}
	if seite != BoardpaarSeiteAus {
		body["boardpaar"] = seite
	}
}
