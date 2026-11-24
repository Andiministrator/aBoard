package api

// konto_passwort_anlegen.go — Passwort mit Pflicht-2FA nachträglich anlegen
// (ADR-0149, Nachtrag 2026-11-04 zu Karte #1099, F-2919). Nur wenn der
// Betreiber ABOARD_ACCOUNT_SELF_PASSWORD einschaltet; Voreinstellung aus.
//
//	POST /users/me/konto/passwort/anlegen/start     {} → Geheimnis, QR (SVG), Zwischenstand
//	POST /users/me/konto/passwort/anlegen/abschluss {zwischen, neu, code} → Wiederherstellungscodes
//
// Voraussetzung: Person ohne Passwort, ohne Konto oder mit aktivem Konto ohne
// Passwort, mit users-Zeile, und ein Nachweis „frisch bestätigt“ mit Aktion
// passwort (konto_frisch.go). Der Nachweis wird beim Start eingelöst; der
// versiegelte Zwischenstand ist an Adresse und laufende Sitzung gebunden.

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

// KontoSelbstPasswortEnv schaltet „Passwort nachträglich anlegen“ ein.
const KontoSelbstPasswortEnv = "ABOARD_ACCOUNT_SELF_PASSWORD"

// AuditActionKontoPasswortAngelegt: Passwort und 2FA nachträglich angelegt.
const AuditActionKontoPasswortAngelegt = "konto-passwort-angelegt"

// KontoSelbstPasswort liest den Schalter nach der Ein/Aus-Konvention
// (docs/betrieb.md §6.1, EnvSchalterAn), aber mit Tippfehler-Schutz: Nur
// eindeutige Werte gelten, alles andere ist ein Fehler (Startabbruch). Nach
// der reinen Konvention hieße „aus“ oder „nein“ AN.
func KontoSelbstPasswort(getenv func(string) string) (bool, error) {
	return schalterStreng(getenv, KontoSelbstPasswortEnv)
}

// schalterStreng liest einen Ein/Aus-Schalter mit Tippfehler-Schutz (siehe
// KontoSelbstPasswort); auch für ABOARD_OIDC_REAUTH_FALLBACK (#1101).
func schalterStreng(getenv func(string) string, name string) (bool, error) {
	switch w := strings.ToLower(strings.TrimSpace(getenv(name))); w {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on", "ja", "an":
		return true, nil
	default:
		return false, fmt.Errorf("%s=%q ist kein eindeutiger Ein/Aus-Wert — an: 1, true, yes, on; aus: leer, 0, false, no, off", name, w)
	}
}

// passwortAnlegenMoeglich: Schalter an, Person ohne Passwort, ohne Konto oder
// mit aktivem Konto ohne Passwort, mit users-Zeile.
func (h *KontoEinstellungen) passwortAnlegenMoeglich(a *kontoAnfrage) bool {
	return h.SelbstPasswort && !a.hatPasswort() && a.NutzerID != "" &&
		(a.KontoZustand == "" || a.KontoZustand == storage.KontoAktiv)
}

// anlegeZwischen steht versiegelt zwischen Start und Abschluss beim Client.
type anlegeZwischen struct {
	Zweck     string `json:"z"` // immer "anlegen" (nie mit dem 2FA-Zwischenstand verwechselbar)
	Email     string `json:"e"`
	Sitzung   string `json:"s"`
	Geheimnis string `json:"g"`
	Bis       int64  `json:"b"`
}

const anlegeZweck = "anlegen"

// passwortAnlegenVorab: gemeinsame Vorbedingungen. Bei false ist die Antwort
// geschrieben.
func (h *KontoEinstellungen) passwortAnlegenVorab(w http.ResponseWriter, a *kontoAnfrage) bool {
	if !h.SelbstPasswort {
		fehlerAntwort(w, http.StatusNotFound, "aus")
		return false
	}
	switch {
	case a.hatPasswort():
		fehlerAntwort(w, http.StatusConflict, "hat_passwort")
		return false
	case a.KontoZustand != "" && a.KontoZustand != storage.KontoAktiv:
		fehlerAntwort(w, http.StatusConflict, "konto_zustand")
		return false
	case a.NutzerID == "":
		fehlerAntwort(w, http.StatusConflict, "konto")
		return false
	}
	return true
}

func (h *KontoEinstellungen) passwortAnlegenStart(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in struct{}
	if !h.lies(w, r, &in) {
		return
	}
	if !h.passwortAnlegenVorab(w, a) {
		return
	}
	if !h.nachweisFuer(r, a, frischPasswort) {
		h.frischeAnmeldungNoetig(w)
		return
	}
	// Alles, was scheitern kann, vor dem Einlösen des Nachweises (P3a).
	ein, err := NeueTOTPEinrichtung(a.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa setup failed", err)
		return
	}
	roh, _ := json.Marshal(anlegeZwischen{Zweck: anlegeZweck, Email: a.Email, Sitzung: storage.SitzungsHash(a.Sitzung),
		Geheimnis: base64.StdEncoding.EncodeToString(ein.Geheimnis), Bis: h.uhr().Add(kontoZwischenGueltig).Unix()})
	versiegelt, err := h.Box.Seal(roh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "seal failed", err)
		return
	}
	svg, err := qrSVG(ein.Link, "QR-Code für die Authenticator-App", 220)
	if err != nil {
		svg = ""
	}
	if !h.bestaetigen(w, r, a, bestaetigung{}, frischPasswort) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"zwischen": versiegelt, "geheimnis": ein.GeheimnisText, "link": ein.Link, "qr": svg,
	})
}

func (h *KontoEinstellungen) passwortAnlegenAbschluss(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in struct {
		Zwischen string `json:"zwischen"`
		Neu      string `json:"neu"`
		Code     string `json:"code"`
	}
	if !h.lies(w, r, &in) {
		return
	}
	if !h.passwortAnlegenVorab(w, a) {
		return
	}
	var z anlegeZwischen
	klar, err := h.Box.Open(in.Zwischen)
	if err != nil || json.Unmarshal(klar, &z) != nil || z.Zweck != anlegeZweck || z.Email != a.Email ||
		subtle.ConstantTimeCompare([]byte(z.Sitzung), []byte(storage.SitzungsHash(a.Sitzung))) != 1 ||
		h.uhr().Unix() >= z.Bis {
		fehlerAntwort(w, http.StatusBadRequest, "zwischen")
		return
	}
	geheimnis, err := base64.StdEncoding.DecodeString(z.Geheimnis)
	if err != nil || len(geheimnis) != totpGeheimnisBytes {
		fehlerAntwort(w, http.StatusBadRequest, "zwischen")
		return
	}
	switch err := PasswortZulaessig(in.Neu); {
	case errors.Is(err, ErrPasswortZuKurz):
		fehlerAntwort(w, http.StatusBadRequest, "zu_kurz")
		return
	case errors.Is(err, ErrPasswortZuLang):
		fehlerAntwort(w, http.StatusBadRequest, "zu_lang")
		return
	case err != nil:
		fehlerAntwort(w, http.StatusBadRequest, "eingabe")
		return
	}
	if h.gesperrtAntwort(w, a) {
		return
	}
	schritt, ok := TOTPPasst(geheimnis, in.Code, h.uhr(), 0)
	if !ok {
		h.gegenstelle.fehlschlag("a:" + a.Adress)
		h.personen.fehlschlag("k:" + a.Email)
		fehlerAntwort(w, http.StatusForbidden, "falsch")
		return
	}
	hash, err := h.hashen(r.Context(), in.Neu)
	if errors.Is(err, ErrPasswortUeberlastet) {
		w.Header().Set("Retry-After", "5")
		fehlerAntwort(w, http.StatusServiceUnavailable, "ueberlastet")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "password hash failed", err)
		return
	}
	versiegelt, err := h.Box.Seal(geheimnis)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa seal failed", err)
		return
	}
	codes, hashes, err := neueWiederherstellungscodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recovery codes failed", err)
		return
	}
	kontoID, err := h.Konten.PasswortAnlegen(r.Context(), a.Email, uuid.NewString(), hash, versiegelt, hashes, schritt, h.uhr().Unix())
	switch {
	case errors.Is(err, storage.ErrHatPasswort):
		fehlerAntwort(w, http.StatusConflict, "hat_passwort")
		return
	case errors.Is(err, storage.ErrKontoZustand):
		fehlerAntwort(w, http.StatusConflict, "konto_zustand")
		return
	case errors.Is(err, storage.ErrUserNotFound):
		fehlerAntwort(w, http.StatusConflict, "konto")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "password create failed", err)
		return
	}
	// Alle Sitzungen sind in der Transaktion gefallen: neu ausstellen. Scheitert
	// nur das, gilt das Anlegen trotzdem — die Codes erscheinen dann dennoch
	// (sonst wären sie verloren), mit dem Hinweis, sich neu anzumelden.
	id, ablauf, serr := h.Sitzungen.Anlegen(r.Context(), a.Email, a.Name)
	if serr != nil {
		LoescheSitzungsCookie(w)
		h.audit(r.Context(), a.NutzerID, AuditActionKontoPasswortAngelegt, "ergebnis=ok;sitzung=fehlt")
		log.Printf("aboard-server: konto: Passwort angelegt (Konto %s, Person %s), neue Sitzung gescheitert: %s — Person meldet sich neu an", kontoID, a.NutzerID, redactForLog(serr))
		writeJSON(w, http.StatusOK, map[string]any{"codes": codes, "neu_anmelden": true})
		return
	}
	SetzeSitzungsCookie(w, id, ablauf)
	h.audit(r.Context(), a.NutzerID, AuditActionKontoPasswortAngelegt, "ergebnis=ok")
	log.Printf("aboard-server: konto: Passwort mit 2FA nachträglich angelegt (Konto %s, Person %s), alle Sitzungen neu ausgestellt", kontoID, a.NutzerID)
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}
