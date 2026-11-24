package api

// admin_einladungen.go — Einladungen über die Admin-API (ADR-0149 §5.3;
// Karte #1076).
//
//	POST   /admin/einladungen        {"emails": ["…", …]}  einladen / erneut einladen
//	GET    /admin/konten                                      Konten mit Zustand
//	DELETE /admin/einladungen/{id}                            Einladung zurückziehen
//
// Der Admin gibt nur Adressen ein. Je Adresse entsteht ein Link (256 Bit,
// nur als Hash gespeichert, einmal nutzbar, 7 Tage gültig). Der Admin sieht
// ihn genau einmal in der Antwort; ist SMTP eingerichtet, geht er zusätzlich
// per Mail an die Adresse. Eine Einladung gibt keine Admin-Rolle mit (Kritik
// H4): Admin wird nur die erste Person überhaupt (BootstrapNewUser, #1077).
//
// Sammelweg: Eine Anfrage nimmt bis zu einladungenJeAnfrage Adressen und
// zählt in der Drossel der Admin-Mutationen (30/Stunde) nur einmal.
//
// Nur im eingebauten Anmeldebetrieb verdrahtet (WithEinladungen); sonst 404.
// Der Mailversand hängt nicht am Hauptschalter der Benachrichtigungen
// (ABOARD_NOTIFICATIONS_DELIVERY): Er ist eine ausdrückliche Handlung des
// Admins wie „Karte per Mail senden“, keine automatische Benachrichtigung.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// einladungenJeAnfrage: höchstens so viele Adressen je Sammelanfrage.
	einladungenJeAnfrage = 50
	// einladungMailFrist: so lange dürfen ALLE Mails einer Anfrage zusammen
	// dauern (F-2890) — sie laufen parallel und enden vor dem WriteTimeout
	// des Servers (30 s); der Admin bekommt seine Links in jedem Fall.
	einladungMailFrist = 20 * time.Second
	// einladungMailParallel: so viele Mails gleichzeitig.
	einladungMailParallel = 5

	// Audit-Aktionen (server_config_audit); Schlüssel ist "konten/<id>",
	// nie die Adresse.
	AuditActionEinladung        = "einladung"
	AuditActionEinladungErneut  = "einladung-erneut"
	AuditActionEinladungZurueck = "einladung-zurueckgezogen"
)

// Einladungen ist der Teil von storage.EinladungRepo, den die API braucht.
type Einladungen interface {
	Einladen(ctx context.Context, email string) (storage.NeueEinladung, error)
	Zurueckziehen(ctx context.Context, kontoID string) (string, error)
	Uebersicht(ctx context.Context) ([]storage.KontoUebersicht, error)
}

// WithEinladungen schaltet die Einladungs-Routen ein. basis ist
// ABOARD_PUBLIC_BASE_URL (geprüft); daraus entsteht der Link.
func (a *AdminAPI) WithEinladungen(e Einladungen, basis string) *AdminAPI {
	a.einladungen = e
	a.einladungBasis = strings.TrimRight(basis, "/")
	if a.einladungSenden == nil {
		a.einladungSenden = mailpkg.SendMailContext
	}
	return a
}

// MitEinladungVersand ersetzt den Mailversand der Einladungen (Tests).
func (a *AdminAPI) MitEinladungVersand(f func(ctx context.Context, cfg mailpkg.Config, msg mailpkg.Message) error) *AdminAPI {
	a.einladungSenden = f
	return a
}

// MitEinladungMailFrist setzt die Gesamtfrist der Mails (Tests).
func (a *AdminAPI) MitEinladungMailFrist(d time.Duration) *AdminAPI {
	a.einladungFrist = d
	return a
}

// EinladungsLink baut den Link aus Basis und Token.
func EinladungsLink(basis, token string) string {
	return strings.TrimRight(basis, "/") + EinladungPfadPraefix + token
}

// einladungErgebnis ist eine Zeile der Antwort auf POST /admin/einladungen.
type einladungErgebnis struct {
	Email   string    `json:"email"`
	KontoID string    `json:"konto_id,omitempty"`
	Link    string    `json:"link,omitempty"`
	Ablauf  time.Time `json:"ablauf,omitzero"`
	Erneut  bool      `json:"erneut,omitempty"`
	// Mail: "gesendet", "nicht_eingerichtet" oder "fehlgeschlagen".
	Mail string `json:"mail,omitempty"`
	// Fehler: "adresse", "hat_zugang", "schon_eingeloest", "belegt" (gleich
	// erneut versuchen), "intern".
	Fehler string `json:"fehler,omitempty"`
}

func (a *AdminAPI) handleEinladen(w http.ResponseWriter, r *http.Request) {
	if a.einladungen == nil {
		writeError(w, http.StatusNotFound, "invitations need the built-in sign-in (ABOARD_AUTH_MODE=builtin)")
		return
	}
	var body struct {
		Emails []string `json:"emails"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: expected {\"emails\": [...]}")
		return
	}
	if len(body.Emails) == 0 || len(body.Emails) > einladungenJeAnfrage {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("emails: 1 to %d addresses", einladungenJeAnfrage))
		return
	}
	uid := adminUserIDFromContext(r.Context())
	mailCfg, _ := mailpkg.LoadConfig(r.Context(), a.cfg)
	mailBereit := mailCfg.Validate() == nil

	gesehen := map[string]bool{}
	out := make([]einladungErgebnis, 0, len(body.Emails))
	for _, roh := range body.Emails {
		erg := einladungErgebnis{Email: strings.TrimSpace(roh)}
		if n, err := storage.EinladungsAdresse(roh); err == nil {
			if gesehen[n] {
				continue // doppelt in derselben Anfrage: ein Link genügt
			}
			gesehen[n] = true
		}
		e, err := a.einladungen.Einladen(r.Context(), roh)
		switch {
		case errors.Is(err, storage.ErrEinladungAdresse):
			erg.Fehler = "adresse"
		case errors.Is(err, storage.ErrEinladungHatZugang):
			erg.Fehler = "hat_zugang"
		case errors.Is(err, storage.ErrEinladungKontoWeiter):
			erg.Fehler = "schon_eingeloest"
		case errors.Is(err, storage.ErrDBBelegt):
			// Vorübergehend (Verify N2): gleich noch einmal einladen.
			erg.Fehler = "belegt"
		case err != nil:
			log.Printf("aboard-server: einladung: %s", redactForLog(err))
			erg.Fehler = "intern"
		default:
			erg.Email, erg.KontoID, erg.Ablauf, erg.Erneut = e.Email, e.KontoID, e.Ablauf, e.Erneut
			erg.Link = EinladungsLink(a.einladungBasis, e.Token)
			aktion := AuditActionEinladung
			if e.Erneut {
				aktion = AuditActionEinladungErneut
			}
			if err := a.cfg.AppendAudit(r.Context(), "konten/"+e.KontoID, uid, aktion, "", ""); err != nil {
				log.Printf("aboard-server: einladung: Audit-Eintrag für konten/%s fehlt: %s", e.KontoID, redactForLog(err))
			}
			erg.Mail = "nicht_eingerichtet"
		}
		out = append(out, erg)
	}
	if mailBereit {
		a.einladungMailsSenden(r.Context(), mailCfg, out)
	}
	// Die Antwort trägt die Links genau einmal: Sie darf nicht am
	// WriteTimeout scheitern (F-2890). Fehler (etwa ein Recorder in Tests)
	// sind egal, dann gilt die Server-Frist.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	writeJSON(w, http.StatusOK, map[string]any{"ergebnisse": out})
}

// einladungMailsSenden schickt alle Mails parallel (höchstens
// einladungMailParallel gleichzeitig) mit einer gemeinsamen Frist und trägt
// das Ergebnis in out ein (F-2890).
func (a *AdminAPI) einladungMailsSenden(ctx context.Context, cfg mailpkg.Config, out []einladungErgebnis) {
	frist := einladungMailFrist
	if a.einladungFrist > 0 {
		frist = a.einladungFrist
	}
	ctx, cancel := context.WithTimeout(ctx, frist)
	defer cancel()
	plaetze := make(chan struct{}, einladungMailParallel)
	var wg sync.WaitGroup
	for i := range out {
		if out[i].Link == "" {
			continue
		}
		wg.Add(1)
		go func(e *einladungErgebnis) {
			defer wg.Done()
			select {
			case plaetze <- struct{}{}:
			case <-ctx.Done():
				e.Mail = "fehlgeschlagen"
				return
			}
			defer func() { <-plaetze }()
			if err := a.einladungMail(ctx, cfg, e.Email, e.Link, e.Ablauf); err != nil {
				// Nie den Link loggen; die Adresse braucht der Betreiber, um
				// die Zustellung zu prüfen.
				log.Printf("aboard-server: einladung: Mail an %s nicht zugestellt: %s — der Admin hat den Link in der Antwort",
					e.Email, redactForLog(err))
				e.Mail = "fehlgeschlagen"
				return
			}
			e.Mail = "gesendet"
		}(&out[i])
	}
	wg.Wait()
}

// einladungMail schickt den Link. Text zweisprachig, ohne Board-Inhalte.
func (a *AdminAPI) einladungMail(ctx context.Context, cfg mailpkg.Config, an, link string, ablauf time.Time) error {
	bis := ablauf.UTC().Format("02.01.2006 15:04 UTC")
	text := "Hallo,\n\ndu bist zu aBoard eingeladen. Über diesen Link legst du fest, wie du dich anmeldest:\n\n" +
		link + "\n\nDer Link gilt bis " + bis + " und nur einmal. Danach bestätigt der Admin deinen Zugang.\n" +
		"Hast du keine Einladung erwartet, ignoriere diese Mail.\n\n---\n\n" +
		"Hello,\n\nyou have been invited to aBoard. Use this link to choose how you sign in:\n\n" +
		link + "\n\nThe link is valid until " + bis + " and can be used once. An administrator then confirms your access.\n" +
		"If you did not expect an invitation, ignore this email.\n"
	return a.einladungSenden(ctx, cfg, mailpkg.Message{
		To: []string{an}, Subject: "Einladung zu aBoard / Invitation to aBoard", BodyText: text,
	})
}

func (a *AdminAPI) handleKontenListe(w http.ResponseWriter, r *http.Request) {
	if a.einladungen == nil {
		writeError(w, http.StatusNotFound, "accounts need the built-in sign-in (ABOARD_AUTH_MODE=builtin)")
		return
	}
	l, err := a.einladungen.Uebersicht(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "accounts list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"konten": l})
}

func (a *AdminAPI) handleEinladungZurueck(w http.ResponseWriter, r *http.Request) {
	if a.einladungen == nil {
		writeError(w, http.StatusNotFound, "invitations need the built-in sign-in (ABOARD_AUTH_MODE=builtin)")
		return
	}
	id := r.PathValue("id")
	_, err := a.einladungen.Zurueckziehen(r.Context(), id)
	switch {
	case errors.Is(err, storage.ErrKontoUnbekannt):
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	case errors.Is(err, storage.ErrEinladungPersonHatZugang):
		// F-2921: Das Löschen ließe die Person ohne Konto zurück.
		writeError(w, http.StatusConflict, "the person already has access: issue a new link (reset) instead of withdrawing")
		return
	case errors.Is(err, storage.ErrEinladungKontoWeiter):
		writeError(w, http.StatusConflict, "already redeemed: reject, block or delete the account instead")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "withdraw failed", err)
		return
	}
	if err := a.cfg.AppendAudit(r.Context(), "konten/"+id, adminUserIDFromContext(r.Context()), AuditActionEinladungZurueck, "", ""); err != nil {
		log.Printf("aboard-server: einladung: Audit-Eintrag für konten/%s fehlt: %s", id, redactForLog(err))
	}
	w.WriteHeader(http.StatusNoContent)
}
