package api

// admin_konten.go — Konten über die Admin-API verwalten (ADR-0149 §5.5,
// Nachtrag #1077; Karte #1077).
//
//	POST /admin/konten/{id}/bestaetigen    pending → active (BootstrapNewUser)
//	POST /admin/konten/{id}/ablehnen       pending ohne users-Zeile → weg
//	POST /admin/konten/{id}/sperren        → blocked, Sitzungen enden
//	POST /admin/konten/{id}/entsperren     blocked → active/pending/invited
//	POST /admin/konten/{id}/zuruecksetzen  → invited, neuer Link (Antwort + Mail)
//
// Das eigene Konto kann ein Admin nicht sperren und nicht zurücksetzen (409):
// Er sperrte sich selbst aus, und auf einem Server mit einem Admin bestätigte
// ihn danach niemand. Nach jeder Änderung vergisst das Tor die Adresse und
// ein offener zweiter Anmeldeschritt des Kontos verfällt (kontoGeaendert).

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Audit-Aktionen der Kontenverwaltung (Schlüssel "konten/<id>").
const (
	AuditActionKontoBestaetigt     = "konto-bestaetigt"
	AuditActionKontoAbgelehnt      = "konto-abgelehnt"
	AuditActionKontoGesperrt       = "konto-gesperrt"
	AuditActionKontoEntsperrt      = "konto-entsperrt"
	AuditActionKontoZurueckgesetzt = "konto-zurueckgesetzt"
)

// KontenVerwaltung ist der Teil von storage.KontoRepo/EinladungRepo, den die
// Admin-API braucht.
type KontenVerwaltung interface {
	NachID(ctx context.Context, kontoID string) (storage.KontoKurz, error)
	Bestaetigen(ctx context.Context, kontoID string, nutzerAnlegen func(ctx context.Context, email string) error, jetzt int64) (string, error)
	Ablehnen(ctx context.Context, kontoID string) (string, error)
	Sperren(ctx context.Context, kontoID string, jetzt int64) (string, error)
	Entsperren(ctx context.Context, kontoID string, jetzt int64) (string, string, error)
	Zuruecksetzen(ctx context.Context, kontoID string) (storage.NeueEinladung, error)
}

// WithKontenVerwaltung schaltet die Konto-Routen ein. nutzerAnlegen legt bei
// der Bestätigung die users-Zeile an; geaendert läuft nach jeder Änderung
// (Tor vergessen, offenen zweiten Schritt streichen). Setzt WithEinladungen
// voraus (Link-Basis und Mailversand für Zurücksetzen).
func (a *AdminAPI) WithKontenVerwaltung(k KontenVerwaltung, nutzerAnlegen func(ctx context.Context, email string) error,
	geaendert func(email, kontoID string)) *AdminAPI {
	a.konten, a.kontoNutzerAnlegen, a.kontoGeaendert = k, nutzerAnlegen, geaendert
	return a
}

func (a *AdminAPI) handleKontoAktion(w http.ResponseWriter, r *http.Request) {
	if a.konten == nil || a.einladungen == nil {
		writeError(w, http.StatusNotFound, "accounts need the built-in sign-in (ABOARD_AUTH_MODE=builtin)")
		return
	}
	id, aktion := r.PathValue("id"), r.PathValue("aktion")
	ctx := r.Context()
	jetzt := time.Now().Unix()

	// Eigenes Konto: nicht sperren, nicht zurücksetzen.
	if aktion == "sperren" || aktion == "zuruecksetzen" {
		k, err := a.konten.NachID(ctx, id)
		if errors.Is(err, storage.ErrKontoUnbekannt) {
			writeError(w, http.StatusNotFound, "account not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "account lookup failed", err)
			return
		}
		if ich := IdentityFromContext(ctx); ich != nil && storage.NormalizeEmail(ich.Email) == storage.NormalizeEmail(k.Email) {
			writeError(w, http.StatusConflict, "you cannot block or reset your own account")
			return
		}
	}

	var (
		email, audit string
		antwort      = map[string]any{}
		err          error
	)
	switch aktion {
	case "bestaetigen":
		email, err = a.konten.Bestaetigen(ctx, id, a.kontoNutzerAnlegen, jetzt)
		audit = AuditActionKontoBestaetigt
		antwort["zustand"] = storage.KontoAktiv
	case "ablehnen":
		email, err = a.konten.Ablehnen(ctx, id)
		audit = AuditActionKontoAbgelehnt
	case "sperren":
		email, err = a.konten.Sperren(ctx, id, jetzt)
		audit = AuditActionKontoGesperrt
		antwort["zustand"] = storage.KontoGesperrt
	case "entsperren":
		var neu string
		email, neu, err = a.konten.Entsperren(ctx, id, jetzt)
		audit = AuditActionKontoEntsperrt
		antwort["zustand"] = neu
	case "zuruecksetzen":
		var e storage.NeueEinladung
		e, err = a.konten.Zuruecksetzen(ctx, id)
		if err == nil {
			email = e.Email
			erg := einladungErgebnis{Email: e.Email, KontoID: e.KontoID, Ablauf: e.Ablauf,
				Link: EinladungsLink(a.einladungBasis, e.Token), Mail: "nicht_eingerichtet"}
			if cfg, _ := mailpkg.LoadConfig(ctx, a.cfg); cfg.Validate() == nil {
				liste := []einladungErgebnis{erg}
				a.einladungMailsSenden(ctx, cfg, liste)
				erg = liste[0]
			}
			antwort["zustand"] = storage.KontoEingeladen
			antwort["link"], antwort["ablauf"], antwort["mail"] = erg.Link, erg.Ablauf, erg.Mail
		}
		audit = AuditActionKontoZurueckgesetzt
	default:
		writeError(w, http.StatusNotFound, "unknown action")
		return
	}
	switch {
	case errors.Is(err, storage.ErrKontoUnbekannt):
		writeError(w, http.StatusNotFound, "account not found")
		return
	case errors.Is(err, storage.ErrKontoZustand):
		writeError(w, http.StatusConflict, "account is not in a state that allows this")
		return
	case errors.Is(err, storage.ErrKontoHatZugang):
		writeError(w, http.StatusConflict, "the person already has access: block the account or delete the user instead")
		return
	case errors.Is(err, storage.ErrBindungPersonBelegt):
		// F-2922: nie 500 — der Admin soll lesen, was zu tun ist.
		writeError(w, http.StatusConflict, "the person already has a binding at this provider: remove it first (aboard-server nutzer bindung-loesen) or reset the account")
		return
	case errors.Is(err, storage.ErrDBBelegt):
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "database busy, try again")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "account action failed", err)
		return
	}
	if a.kontoGeaendert != nil {
		a.kontoGeaendert(email, id)
	}
	if err := a.cfg.AppendAudit(ctx, "konten/"+id, adminUserIDFromContext(ctx), audit, "", ""); err != nil {
		log.Printf("aboard-server: konten: Audit-Eintrag für konten/%s fehlt: %s", id, redactForLog(err))
	}
	antwort["id"] = id
	// Zurücksetzen trägt den Link genau einmal (wie Einladen, F-2890).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	writeJSON(w, http.StatusOK, antwort)
}
