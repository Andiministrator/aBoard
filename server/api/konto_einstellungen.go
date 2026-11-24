package api

// konto_einstellungen.go — Konto-Einstellungen der angemeldeten Person
// (ADR-0149 §3, §4, Nachtrag Karte #1083): Passwort ändern, 2FA neu,
// Wiederherstellungscodes neu, weiteren Anmeldeweg verknüpfen oder lösen,
// andere Sitzungen beenden.
//
//	GET  /users/me/konto                  Stand: Wege, Passwort, 2FA, Codes
//	POST /users/me/konto/passwort         {aktuell, code, neu}
//	POST /users/me/konto/2fa/start        {aktuell, code} → Geheimnis, QR (SVG), Zwischenstand
//	POST /users/me/konto/2fa/abschluss    {zwischen, code} → neue Wiederherstellungscodes
//	POST /users/me/konto/codes            {aktuell, code} → neue Wiederherstellungscodes
//	POST /users/me/konto/sitzungen/beenden
//	POST /users/me/konto/wege/verknuepfen {anbieter, aktuell, code} → {ziel}
//	POST /users/me/konto/wege/loesen      {anbieter, aktuell, code}
//	POST /users/me/konto/frisch/start     {anbieter, aktion, ziel} → {ziel} (konto_frisch.go, #1098)
//	POST /users/me/konto/passwort/anlegen/start|abschluss (konto_passwort_anlegen.go, #1099)
//
// Alle Wege liegen hinter der Sitzungsweiche (Menschen-Kette) und dem
// CSRF-Schutz; ein Agent kommt nie hierher.
//
// Erneute Bestätigung: Jede Änderung verlangt das aktuelle Passwort, falls die
// Person eines hat, und bei eingerichtetem 2FA zusätzlich einen gültigen Code
// (Wiederherstellungscode genügt). Wer kein Passwort hat (nur Anbieter oder
// Vorab-Liste), bestätigt mit einem Nachweis „frisch bestätigt“ aus einer
// frischen Anmeldung beim schon gebundenen Anbieter, nur für die Aktion, die er
// nennt (konto_frisch.go, Karte #1098); ohne ihn 409 frische_anmeldung_noetig.
// Falsche Bestätigungen zählen in zwei Bremsen: je Gegenstelle und je Person
// (5 in 15 Minuten); die Sperre betrifft nur diese Einstellungen, nie die
// Anmeldung. Je Person läuft höchstens eine Änderung gleichzeitig.
//
// Fehlantworten tragen {"fehler": "<kennung>"} — nie 401 (das heißt für das
// Frontend „Sitzung abgelaufen“): falsche Bestätigung ist 403.
//
// Sitzungen: Passwort ändern und 2FA neu beenden alle anderen Sitzungen der
// Person in derselben Transaktion und stellen die laufende Sitzung neu aus
// (neue Kennung, Fixierungsschutz) — die Person bleibt angemeldet.
//
// Verknüpfen (§4): nur aus dem angemeldeten Zustand. Der Vorgang beginnt mit
// einem POST (nie GET: ein fremder Link darf keine Verknüpfung auslösen), trägt
// Zweck link und den Hash der Sitzungs-ID im signierten Anmelde-Zustand und
// bindet im Rückruf nur, wenn dieselbe Sitzung noch besteht. Es gibt nur ein
// Anmelde-Cookie: Ein paralleler Anmeldevorgang überschreibt den Zustand, der
// Rückruf weist dann ab — fail closed, es wird nichts gebunden.

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// KontoPfad ist die Wurzel der Konto-Einstellungen.
	KontoPfad = "/users/me/konto"
	// KontoErgebnisParam: Rückkehr aus dem Anbieter nach dem Verknüpfen
	// (/?konto=<ergebnis>); die Oberfläche öffnet die Einstellungen.
	KontoErgebnisParam = "konto"
	// Ergebnisse der Rückkehr.
	KontoErgebnisVerknuepft  = "verknuepft"
	KontoErgebnisErsetzt     = "ersetzt"
	KontoErgebnisBelegt      = "belegt"
	KontoErgebnisAbgebrochen = "abgebrochen"
	KontoErgebnisAbgelaufen  = "abgelaufen"
	KontoErgebnisFehler      = "fehler"

	// Audit-Aktionen (Schlüssel "personen/<users.id>", ohne Adresse, ohne Geheimnis).
	AuditActionKontoPasswort           = "konto-passwort-geaendert"
	AuditActionKonto2FA                = "konto-2fa-erneuert"
	AuditActionKontoCodes              = "konto-codes-erneuert"
	AuditActionKontoWegVerknuepft      = "konto-weg-verknuepft"
	AuditActionKontoWegErsetzt         = "konto-weg-ersetzt"
	AuditActionKontoWegGeloest         = "konto-weg-geloest"
	AuditActionKontoSitzungenEnde      = "konto-sitzungen-beendet"
	AuditActionKontoBestaetigungFalsch = "konto-bestaetigung-falsch"
	AuditActionKontoBindungGeloestCLI  = "konto-bindung-geloest-cli"

	// KontoMaxFehler je Gegenstelle und je Person im KontoFenster.
	KontoMaxFehler = 5
	KontoFenster   = 15 * time.Minute
	// kontoZwischenGueltig: so lange gilt der Zwischenstand der 2FA-Erneuerung.
	kontoZwischenGueltig = 15 * time.Minute
	kontoBodyMax         = 16 << 10
)

// KontoSpeicher ist der Teil von storage.KontoRepo, den die Einstellungen
// brauchen.
type KontoSpeicher interface {
	TOTPKonten
	FuerAnmeldung(ctx context.Context, email string) (storage.KontoAnmeldung, error)
	OffeneCodes(ctx context.Context, kontoID string) (int, error)
	PasswortAendern(ctx context.Context, kontoID, hash, behalteKlartext string, jetzt int64) error
	PasswortAnlegen(ctx context.Context, email, neueID, hash, versiegelt string, codeHashes []string, schritt, jetzt int64) (string, error)
	TOTPNeu(ctx context.Context, kontoID, versiegelt string, codeHashes []string, schritt int64, behalteKlartext string, jetzt int64) error
	CodesErsetzen(ctx context.Context, kontoID string, codeHashes []string, jetzt int64) error
}

// KontoBindungen ist der Teil von storage.AnbieterBindungRepo, den die
// Einstellungen brauchen.
type KontoBindungen interface {
	FuerEmail(ctx context.Context, email string) ([]storage.AnbieterBindung, error)
	Verknuepfen(ctx context.Context, email, anbieter, iss, sub string) (bool, error)
	Loesen(ctx context.Context, email, anbieter string) error
	NutzerID(ctx context.Context, email string) (string, error)
	NachKonto(ctx context.Context, iss, sub string) (*storage.AnbieterBindung, error)
}

// KontoAudit schreibt Audit-Einträge (storage.ConfigRepo.AppendAudit).
type KontoAudit interface {
	AppendAudit(ctx context.Context, key, userID, action, oldRedacted, newRedacted string) error
}

// KontoSitzungen ist der Teil von storage.SitzungRepo, den die Einstellungen
// brauchen.
type KontoSitzungen interface {
	AnmeldeSitzungen
	Pruefen(ctx context.Context, klartext string) (*storage.Sitzung, error)
	AndereBeenden(ctx context.Context, email, behalteKlartext string) (int64, error)
}

// KontoEinstellungen bedient /users/me/konto/….
type KontoEinstellungen struct {
	Konten      KontoSpeicher
	Bindungen   KontoBindungen
	Sitzungen   KontoSitzungen
	Anmeldungen *Anmeldungen
	Box         Siegel
	// Audit: nil = kein Audit (Tests). Im Betrieb immer gesetzt.
	Audit KontoAudit
	// SelbstPasswort: ABOARD_ACCOUNT_SELF_PASSWORD an und der Passwort-Weg
	// angeboten (Karte #1099).
	SelbstPasswort bool
	// ReauthFallback: ABOARD_OIDC_REAUTH_FALLBACK an — fehlt auth_time, genügt
	// ein in diesem Vorgang frisch ausgestelltes ID-Token (iat; ADR-0149,
	// Nachtrag 2026-11-09, Karte #1101).
	ReauthFallback bool

	gegenstelle *fehlversuchBremse
	personen    *fehlversuchBremse
	jetzt       func() time.Time
	// pruefen und hashen sind PasswortPruefen/PasswortHashen; Tests setzen
	// etwas Schnelles.
	pruefen func(ctx context.Context, pw, hash string) (bool, error)
	hashen  func(ctx context.Context, pw string) (string, error)

	laufendMu sync.Mutex
	laufend   map[string]bool

	// frisch: offene Nachweise „frisch bestätigt“ (Kennung → Ablauf, Unix);
	// eingelöst wird gestrichen (Karte #1098).
	frischMu sync.Mutex
	frisch   map[string]int64
}

// NeueKontoEinstellungen baut die Einstellungen. proxy ist der Wert von
// ABOARD_AGENT_PROXY (dieselbe Adressregel wie bei der Passwort-Anmeldung).
func NeueKontoEinstellungen(konten KontoSpeicher, bindungen KontoBindungen, sitzungen KontoSitzungen, anmeldungen *Anmeldungen, box Siegel, proxy string) (*KontoEinstellungen, error) {
	if konten == nil || bindungen == nil || sitzungen == nil || anmeldungen == nil || box == nil {
		return nil, errors.New("konto-einstellungen: konten, bindungen, sitzungen, anmeldungen und box sind pflicht")
	}
	p, err := parseProxy(proxy)
	if err != nil {
		return nil, err
	}
	return &KontoEinstellungen{
		Konten: konten, Bindungen: bindungen, Sitzungen: sitzungen, Anmeldungen: anmeldungen, Box: box,
		gegenstelle: neueBremseMit(p, KontoMaxFehler, KontoFenster, "Konto-Einstellungen"),
		personen:    neueBremseMit(p, KontoMaxFehler, KontoFenster, "Konto-Einstellungen (Person)"),
		pruefen:     PasswortPruefen,
		hashen:      PasswortHashen,
	}, nil
}

// Anmelden hängt die Routen an den Mux und verbindet jeden Anbieter mit dem
// Rückruf-Abschluss des Verknüpfens.
func (h *KontoEinstellungen) Anmelden(mux *http.ServeMux) {
	for _, a := range h.Anmeldungen.Anbieter {
		a.Verknuepfung = h
	}
	mux.Handle("GET "+KontoPfad, h.schutz(h.stand, false))
	mux.Handle("POST "+KontoPfad+"/passwort", h.schutz(h.passwortAendern, true))
	mux.Handle("POST "+KontoPfad+"/2fa/start", h.schutz(h.zweiFaktorStart, true))
	mux.Handle("POST "+KontoPfad+"/2fa/abschluss", h.schutz(h.zweiFaktorAbschluss, true))
	mux.Handle("POST "+KontoPfad+"/codes", h.schutz(h.codesNeu, true))
	mux.Handle("POST "+KontoPfad+"/sitzungen/beenden", h.schutz(h.sitzungenBeenden, true))
	mux.Handle("POST "+KontoPfad+"/wege/verknuepfen", h.schutz(h.wegVerknuepfen, true))
	mux.Handle("POST "+KontoPfad+"/wege/loesen", h.schutz(h.wegLoesen, true))
	mux.Handle("POST "+KontoPfad+"/frisch/start", h.schutz(h.frischStart, true))
	mux.Handle("POST "+KontoPfad+"/passwort/anlegen/start", h.schutz(h.passwortAnlegenStart, true))
	mux.Handle("POST "+KontoPfad+"/passwort/anlegen/abschluss", h.schutz(h.passwortAnlegenAbschluss, true))
}

func (h *KontoEinstellungen) uhr() time.Time {
	if h.jetzt != nil {
		return h.jetzt()
	}
	return time.Now()
}

// kontoAnfrage ist, was ein Handler über die Anfrage wissen muss.
type kontoAnfrage struct {
	Email string
	Name  string
	// Sitzung ist die Klartext-ID der laufenden Sitzung (Cookie).
	Sitzung string
	// Konto: die Person hat eine konten-Zeile (aktives Konto, ADR-0149 §5.2).
	Konto bool
	Daten storage.KontoAnmeldung
	// KontoZustand: Zustand der konten-Zeile ("" = keine), auch wenn nicht aktiv.
	KontoZustand string
	Adress       string // Gegenstelle für die Bremse
	// NutzerID: users.id für Audit und Log ("" = unbekannt).
	NutzerID string
}

// schutz: nur Menschen mit Sitzung; ein Agent kommt nie an. Mutationen laufen
// je Person nacheinander (429 „läuft schon“).
func (h *KontoEinstellungen) schutz(fn func(w http.ResponseWriter, r *http.Request, a *kontoAnfrage), mutation bool) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if AgentFromContext(r.Context()) != nil {
			writeError(w, http.StatusForbidden, "forbidden for agent")
			return
		}
		id := IdentityFromContext(r.Context())
		email := ""
		if id != nil {
			email = storage.NormalizeEmail(id.Email)
		}
		c, cerr := r.Cookie(SitzungsCookie)
		if email == "" || storage.IstDomainSubjekt(email) || cerr != nil || c.Value == "" {
			writeError(w, http.StatusForbidden, "forbidden: session required")
			return
		}
		a := &kontoAnfrage{Email: email, Name: id.Name, Sitzung: c.Value, Adress: h.gegenstelle.proxy.clientAdresse(r)}
		if mutation {
			frei, ok := h.belege(email)
			if !ok {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"fehler": "laeuft"})
				return
			}
			defer frei()
		}
		if uid, err := h.Bindungen.NutzerID(r.Context(), email); err == nil {
			a.NutzerID = uid
		}
		k, err := h.Konten.FuerAnmeldung(r.Context(), email)
		if err == nil {
			a.KontoZustand = k.Zustand
		}
		switch {
		case err == nil && k.Zustand == storage.KontoAktiv:
			a.Konto, a.Daten = true, k
		case err != nil && !errors.Is(err, storage.ErrKontoUnbekannt):
			writeError(w, http.StatusInternalServerError, "account lookup failed", err)
			return
		}
		fn(w, r, a)
	}))
}

func (h *KontoEinstellungen) belege(schluessel string) (func(), bool) {
	h.laufendMu.Lock()
	defer h.laufendMu.Unlock()
	if h.laufend == nil {
		h.laufend = map[string]bool{}
	}
	if h.laufend[schluessel] {
		return nil, false
	}
	h.laufend[schluessel] = true
	return func() {
		h.laufendMu.Lock()
		defer h.laufendMu.Unlock()
		delete(h.laufend, schluessel)
	}, true
}

// hatPasswort: aktives Konto mit Passwort — Voraussetzung für jede Änderung.
func (a *kontoAnfrage) hatPasswort() bool { return a.Konto && a.Daten.PasswortHash != "" }

func fehlerAntwort(w http.ResponseWriter, status int, kennung string) {
	writeJSON(w, status, map[string]string{"fehler": kennung})
}

// lies liest den JSON-Körper in v. Bei false ist 400 geschrieben.
func (h *KontoEinstellungen) lies(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, kontoBodyMax)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fehlerAntwort(w, http.StatusBadRequest, "eingabe")
		return false
	}
	return true
}

// bestaetigung ist die erneute Bestätigung, die jede Änderung mitbringt.
type bestaetigung struct {
	Aktuell string `json:"aktuell"`
	Code    string `json:"code"`
}

// bestaetigen prüft die erneute Bestätigung (siehe Kopf). aktion ist die
// Änderung, die ein Nachweis „frisch bestätigt“ nennen muss ("" = keiner gilt).
// Bei false ist die Antwort geschrieben.
func (h *KontoEinstellungen) bestaetigen(w http.ResponseWriter, r *http.Request, a *kontoAnfrage, b bestaetigung, aktion string) bool {
	if !a.hatPasswort() {
		// Fail closed (Sicherheitsprüfung #1083, P1): Ohne Passwort bestätigt
		// nur ein Nachweis aus einer frischen Anmeldung beim gebundenen Anbieter,
		// und nur für genau diese Aktion; er gilt einmal (#1098).
		if h.nachweisEinloesen(w, r, a, aktion) {
			return true
		}
		h.frischeAnmeldungNoetig(w)
		return false
	}
	if h.gesperrtAntwort(w, a) {
		return false
	}
	falsch := func() bool {
		h.audit(r.Context(), a.NutzerID, AuditActionKontoBestaetigungFalsch, "ergebnis=falsch")
		h.gegenstelle.fehlschlag("a:" + a.Adress)
		h.personen.fehlschlag("k:" + a.Email)
		fehlerAntwort(w, http.StatusForbidden, "falsch")
		return false
	}
	ok, err := h.pruefen(r.Context(), b.Aktuell, a.Daten.PasswortHash)
	switch {
	case errors.Is(err, ErrPasswortUeberlastet):
		w.Header().Set("Retry-After", "5")
		fehlerAntwort(w, http.StatusServiceUnavailable, "ueberlastet")
		return false
	case err != nil:
		writeError(w, http.StatusInternalServerError, "password check failed", err)
		return false
	case !ok:
		return falsch()
	}
	if a.Daten.HatTOTP {
		ok, err := zweitenFaktorPruefen(r.Context(), h.Konten, h.Box, h.uhr(), a.Daten.ID, b.Code)
		if err != nil {
			log.Printf("aboard-server: konto: 2FA-Prüfung gescheitert: %s — wurde ABOARD_SECRET_KEY gewechselt? Person zurücksetzen lassen", redactForLog(err))
			fehlerAntwort(w, http.StatusInternalServerError, "defekt")
			return false
		}
		if !ok {
			return falsch()
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Stand

type wegAnsicht struct {
	Anbieter     string `json:"anbieter"`
	Label        string `json:"label"`
	Verknuepft   bool   `json:"verknuepft"`
	Konfiguriert bool   `json:"konfiguriert"`
	Loesbar      bool   `json:"loesbar"`
}

type kontoStand struct {
	Email         string       `json:"email"`
	Konto         bool         `json:"konto"`
	Passwort      bool         `json:"passwort"`
	ZweiterFaktor bool         `json:"zweiter_faktor"`
	CodesOffen    int          `json:"codes_offen"`
	Wege          []wegAnsicht `json:"wege"`
	// NachFrage: welche Felder die erneute Bestätigung braucht.
	// WegeAenderbar: Verknüpfen und Lösen sind möglich (mit Passwort, oder
	// ohne über eine frische Anmeldung bei einem gebundenen Anbieter, #1098).
	WegeAenderbar bool `json:"wege_aenderbar"`
	// FrischNoetig: Änderungen brauchen eine frische Anmeldung (kein
	// Passwort); FrischAnbieter: bei welchen Anbietern das geht; Frisch: der
	// vorliegende Nachweis (Aktion und Ablauf, nie sein Wert).
	FrischNoetig   bool           `json:"frisch_noetig"`
	FrischAnbieter []anbieterKurz `json:"frisch_anbieter"`
	Frisch         *frischAnsicht `json:"frisch,omitempty"`
	// PasswortAnlegen: Passwort mit 2FA lässt sich nachträglich anlegen (#1099).
	PasswortAnlegen bool `json:"passwort_anlegen"`
	NachFrage       struct {
		Passwort bool `json:"passwort"`
		Code     bool `json:"code"`
	} `json:"nachfrage"`
}

type anbieterKurz struct {
	Anbieter string `json:"anbieter"`
	Label    string `json:"label"`
}

func (h *KontoEinstellungen) stand(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	s := kontoStand{Email: a.Email, Konto: a.Konto, Wege: []wegAnsicht{}, FrischAnbieter: []anbieterKurz{}}
	if a.Konto {
		s.Passwort = a.Daten.PasswortHash != ""
		s.ZweiterFaktor = a.Daten.HatTOTP
		if s.ZweiterFaktor {
			n, err := h.Konten.OffeneCodes(r.Context(), a.Daten.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "codes lookup failed", err)
				return
			}
			s.CodesOffen = n
		}
	}
	s.NachFrage.Passwort = s.Passwort
	s.NachFrage.Code = s.Passwort && s.ZweiterFaktor
	bind, err := h.Bindungen.FuerEmail(r.Context(), a.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bindings lookup failed", err)
		return
	}
	if !s.Passwort {
		s.FrischNoetig = true
		for _, an := range h.gebundeneAnbieter(bind) {
			s.FrischAnbieter = append(s.FrischAnbieter, anbieterKurz{Anbieter: an.kuerzel(), Label: an.label()})
		}
		s.Frisch = h.nachweisAnsicht(r, a)
		s.PasswortAnlegen = h.passwortAnlegenMoeglich(a) && len(s.FrischAnbieter) > 0
	}
	s.WegeAenderbar = s.Passwort || len(s.FrischAnbieter) > 0
	gebunden := map[string]bool{}
	for _, b := range bind {
		gebunden[b.Anbieter] = true
	}
	loesbar := s.Passwort || len(bind) > 1
	for _, an := range h.Anmeldungen.Anbieter {
		k := an.kuerzel()
		s.Wege = append(s.Wege, wegAnsicht{Anbieter: k, Label: an.label(), Verknuepft: gebunden[k], Konfiguriert: true, Loesbar: gebunden[k] && loesbar && s.WegeAenderbar})
		delete(gebunden, k)
	}
	// Bindungen an Anbieter, die der Betreiber nicht mehr eingerichtet hat:
	// sichtbar und lösbar, aber nicht neu verknüpfbar.
	for _, b := range bind {
		if gebunden[b.Anbieter] {
			s.Wege = append(s.Wege, wegAnsicht{Anbieter: b.Anbieter, Label: b.Anbieter, Verknuepft: true, Loesbar: loesbar && s.WegeAenderbar})
			delete(gebunden, b.Anbieter)
		}
	}
	writeJSON(w, http.StatusOK, s)
}

// ---------------------------------------------------------------------------
// Sitzung nach einer Änderung neu ausstellen

// neueSitzung stellt nach einer Änderung eine neue Sitzung aus. Die alten
// Sitzungen der Person, auch die laufende, hat der Speicher in derselben
// Transaktion wie die Änderung gelöscht (Sicherheitsprüfung #1083, P3c): Es gibt
// keinen Weg, auf dem eine alte Kennung weiterlebt, weil ein späteres Beenden
// scheitert. Scheitert nur das Ausstellen, antwortet der Endpunkt mit 500 — die
// Änderung gilt, die Person meldet sich neu an. Bei false ist die Antwort
// geschrieben.
func (h *KontoEinstellungen) neueSitzung(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) bool {
	return ausstellen(w, r, h.Sitzungen, a.Email, a.Name)
}

func ausstellen(w http.ResponseWriter, r *http.Request, s AnmeldeSitzungen, email, name string) bool {
	id, ablauf, err := s.Anlegen(r.Context(), email, name)
	if err != nil {
		LoescheSitzungsCookie(w)
		writeError(w, http.StatusInternalServerError, "session create failed", err)
		return false
	}
	SetzeSitzungsCookie(w, id, ablauf)
	return true
}

// audit trägt eine Änderung ins Audit-Protokoll ein: Person-ID, Aktion und
// Ergebnis, nie Adresse oder Geheimnis. Ein Fehler wird nur geloggt.
func (h *KontoEinstellungen) audit(ctx context.Context, nutzerID, action, detail string) {
	if h.Audit == nil {
		return
	}
	if err := h.Audit.AppendAudit(ctx, "personen/"+nutzerID, nutzerID, action, "", detail); err != nil {
		log.Printf("aboard-server: konto: Audit-Eintrag %s für personen/%s fehlt: %s", action, nutzerID, redactForLog(err))
	}
}

// ---------------------------------------------------------------------------
// Passwort

func (h *KontoEinstellungen) passwortAendern(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in struct {
		bestaetigung
		Neu string `json:"neu"`
	}
	if !h.lies(w, r, &in) {
		return
	}
	if !a.Konto || a.Daten.PasswortHash == "" {
		fehlerAntwort(w, http.StatusConflict, "kein_passwort")
		return
	}
	// Die Regeln zuerst: ein zu kurzes Passwort soll keinen Code verbrauchen.
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
	if !h.bestaetigen(w, r, a, in.bestaetigung, "") {
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
	if err := h.Konten.PasswortAendern(r.Context(), a.Daten.ID, hash, "", h.uhr().Unix()); err != nil {
		h.speicherFehler(w, err, "password change failed")
		return
	}
	if !h.neueSitzung(w, r, a) {
		return
	}
	h.audit(r.Context(), a.NutzerID, AuditActionKontoPasswort, "ergebnis=ok")
	log.Printf("aboard-server: konto: Passwort geändert (Konto %s, Person %s), alle Sitzungen neu ausgestellt", a.Daten.ID, a.NutzerID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *KontoEinstellungen) speicherFehler(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, storage.ErrKontoUnbekannt) {
		fehlerAntwort(w, http.StatusConflict, "konto")
		return
	}
	writeError(w, http.StatusInternalServerError, msg, err)
}

// ---------------------------------------------------------------------------
// 2FA neu

// zwischenstand steht versiegelt zwischen den beiden Schritten beim Client:
// das neue Geheimnis, gebunden an Konto, laufende Sitzung und das bisherige
// Geheimnis (wurde es inzwischen ersetzt, gilt der Zwischenstand nicht mehr —
// so lässt er sich auch nur einmal einlösen).
type zwischenstand struct {
	KontoID   string `json:"k"`
	Sitzung   string `json:"s"` // SHA-256 der Sitzungs-ID
	Alt       string `json:"a"` // bisheriges versiegeltes Geheimnis
	Geheimnis string `json:"g"` // base64 (roh)
	Bis       int64  `json:"b"`
}

func (h *KontoEinstellungen) zweiFaktorStart(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in bestaetigung
	if !h.lies(w, r, &in) {
		return
	}
	if !a.hatPasswort() || !a.Daten.HatTOTP {
		fehlerAntwort(w, http.StatusConflict, "kein_passwort")
		return
	}
	// Alles, was scheitern kann, vor der Bestätigung (P3a): Ein Fehler soll
	// keinen Code verbrauchen.
	alt, _, err := h.Konten.TOTPGeheimnis(r.Context(), a.Daten.ID)
	if err != nil {
		h.speicherFehler(w, err, "2fa lookup failed")
		return
	}
	ein, err := NeueTOTPEinrichtung(a.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa setup failed", err)
		return
	}
	roh, _ := json.Marshal(zwischenstand{KontoID: a.Daten.ID, Sitzung: storage.SitzungsHash(a.Sitzung), Alt: alt,
		Geheimnis: base64.StdEncoding.EncodeToString(ein.Geheimnis), Bis: h.uhr().Add(kontoZwischenGueltig).Unix()})
	versiegelt, err := h.Box.Seal(roh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "2fa seal failed", err)
		return
	}
	svg, err := qrSVG(ein.Link, "QR-Code für die Authenticator-App", 220)
	if err != nil {
		svg = "" // Schlüssel und Link bleiben; der QR-Code ist Zugabe
	}
	if !h.bestaetigen(w, r, a, in, "") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"zwischen": versiegelt, "geheimnis": ein.GeheimnisText, "link": ein.Link, "qr": svg,
	})
}

func (h *KontoEinstellungen) zweiFaktorAbschluss(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in struct {
		Zwischen string `json:"zwischen"`
		Code     string `json:"code"`
	}
	if !h.lies(w, r, &in) {
		return
	}
	if !a.hatPasswort() || !a.Daten.HatTOTP {
		fehlerAntwort(w, http.StatusConflict, "kein_passwort")
		return
	}
	var z zwischenstand
	klar, err := h.Box.Open(in.Zwischen)
	if err != nil || json.Unmarshal(klar, &z) != nil || z.KontoID != a.Daten.ID ||
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
	// Ein falscher Code aus der neuen App zählt in die Bremsen wie ein
	// falsches Passwort (sonst probierte man gegen den Zwischenstand).
	if g, rest := h.gegenstelle.gesperrt("a:" + a.Adress); g {
		w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
		fehlerAntwort(w, http.StatusTooManyRequests, "gesperrt")
		return
	}
	if g, rest := h.personen.gesperrt("k:" + a.Email); g {
		w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
		fehlerAntwort(w, http.StatusTooManyRequests, "gesperrt")
		return
	}
	schritt, ok := TOTPPasst(geheimnis, in.Code, h.uhr(), 0)
	if !ok {
		h.gegenstelle.fehlschlag("a:" + a.Adress)
		h.personen.fehlschlag("k:" + a.Email)
		fehlerAntwort(w, http.StatusForbidden, "falsch")
		return
	}
	jetztAlt, _, err := h.Konten.TOTPGeheimnis(r.Context(), a.Daten.ID)
	if err != nil {
		h.speicherFehler(w, err, "2fa lookup failed")
		return
	}
	if jetztAlt != z.Alt {
		fehlerAntwort(w, http.StatusConflict, "zwischen") // inzwischen ersetzt
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
	if err := h.Konten.TOTPNeu(r.Context(), a.Daten.ID, versiegelt, hashes, schritt, "", h.uhr().Unix()); err != nil {
		h.speicherFehler(w, err, "2fa change failed")
		return
	}
	if !h.neueSitzung(w, r, a) {
		return
	}
	h.audit(r.Context(), a.NutzerID, AuditActionKonto2FA, "ergebnis=ok")
	log.Printf("aboard-server: konto: 2FA neu eingerichtet (Konto %s, Person %s), alle Sitzungen neu ausgestellt", a.Daten.ID, a.NutzerID)
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// ---------------------------------------------------------------------------
// Wiederherstellungscodes neu

func (h *KontoEinstellungen) codesNeu(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in bestaetigung
	if !h.lies(w, r, &in) {
		return
	}
	if !a.hatPasswort() || !a.Daten.HatTOTP {
		fehlerAntwort(w, http.StatusConflict, "kein_passwort")
		return
	}
	codes, hashes, err := neueWiederherstellungscodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recovery codes failed", err)
		return
	}
	if !h.bestaetigen(w, r, a, in, "") {
		return
	}
	if err := h.Konten.CodesErsetzen(r.Context(), a.Daten.ID, hashes, h.uhr().Unix()); err != nil {
		h.speicherFehler(w, err, "codes change failed")
		return
	}
	h.audit(r.Context(), a.NutzerID, AuditActionKontoCodes, "ergebnis=ok")
	log.Printf("aboard-server: konto: Wiederherstellungscodes erneuert (Konto %s, Person %s)", a.Daten.ID, a.NutzerID)
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// ---------------------------------------------------------------------------
// Andere Sitzungen beenden

func (h *KontoEinstellungen) sitzungenBeenden(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	n, err := h.Sitzungen.AndereBeenden(r.Context(), a.Email, a.Sitzung)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "end sessions failed", err)
		return
	}
	h.audit(r.Context(), a.NutzerID, AuditActionKontoSitzungenEnde, "ergebnis=ok;anzahl="+strconv.FormatInt(n, 10))
	writeJSON(w, http.StatusOK, map[string]int64{"beendet": n})
}

// ---------------------------------------------------------------------------
// Wege verknüpfen und lösen

type wegEingabe struct {
	bestaetigung
	Anbieter string `json:"anbieter"`
}

func (h *KontoEinstellungen) wegVerknuepfen(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in wegEingabe
	if !h.lies(w, r, &in) {
		return
	}
	// Vorbedingungen vor der Bestätigung (P3a), die erste zuerst (P1): ohne
	// Passwort nur mit Nachweis für genau diese Verknüpfung (#1098).
	kuerzel := strings.TrimSpace(in.Anbieter)
	aktion := frischAktion(frischVerknuepfen, kuerzel)
	if !a.hatPasswort() && kontoNichtAktiv(w, a) {
		return
	}
	if !a.hatPasswort() && !h.nachweisFuer(r, a, aktion) {
		h.frischeAnmeldungNoetig(w)
		return
	}
	an := h.Anmeldungen.Finde(kuerzel)
	if an == nil || an.Verknuepfung == nil {
		fehlerAntwort(w, http.StatusNotFound, "anbieter")
		return
	}
	// Die Discovery des Anbieters (gepuffert) vorziehen: Ein nicht erreichbarer
	// Anbieter soll keinen Code verbrauchen.
	if _, err := an.Anbieter(r.Context()); err != nil {
		fehlerAntwort(w, http.StatusServiceUnavailable, "nicht_erreichbar")
		return
	}
	frisch := !a.hatPasswort()
	if !h.bestaetigen(w, r, a, in.bestaetigung, aktion) {
		return
	}
	ziel, ok := an.vorgang(w, r, vorgangOpt{Zweck: ZweckLink, Sitzung: storage.SitzungsHash(a.Sitzung), Frisch: frisch, KontoWahl: true, AlsJSON: true})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ziel": ziel})
}

func (h *KontoEinstellungen) wegLoesen(w http.ResponseWriter, r *http.Request, a *kontoAnfrage) {
	var in wegEingabe
	if !h.lies(w, r, &in) {
		return
	}
	anbieter := strings.TrimSpace(in.Anbieter)
	aktion := frischAktion(frischLoesen, anbieter)
	if !a.hatPasswort() && kontoNichtAktiv(w, a) {
		return
	}
	if !a.hatPasswort() && !h.nachweisFuer(r, a, aktion) {
		h.frischeAnmeldungNoetig(w)
		return
	}
	if !IstAnbieterKuerzel(anbieter) {
		fehlerAntwort(w, http.StatusNotFound, "anbieter")
		return
	}
	// Besteht die Bindung? (P3a) Ob ein Weg übrig bleibt, prüft der Speicher in
	// der Transaktion (ohne Passwort seit #1098 erreichbar). Vorab auch hier,
	// damit ein unmögliches Lösen keinen Nachweis verbraucht.
	bind, err := h.Bindungen.FuerEmail(r.Context(), a.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bindings lookup failed", err)
		return
	}
	da := false
	for _, b := range bind {
		da = da || b.Anbieter == anbieter
	}
	if !da {
		fehlerAntwort(w, http.StatusNotFound, "anbieter")
		return
	}
	if !a.hatPasswort() && len(bind) < 2 {
		fehlerAntwort(w, http.StatusConflict, "letzter_weg")
		return
	}
	if !h.bestaetigen(w, r, a, in.bestaetigung, aktion) {
		return
	}
	switch err := h.Bindungen.Loesen(r.Context(), a.Email, anbieter); {
	case errors.Is(err, storage.ErrLetzterWeg):
		fehlerAntwort(w, http.StatusConflict, "letzter_weg")
		return
	case errors.Is(err, storage.ErrBindungFehlt), errors.Is(err, storage.ErrUserNotFound):
		fehlerAntwort(w, http.StatusNotFound, "anbieter")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "unbind failed", err)
		return
	}
	// Alle Sitzungen sind in der Transaktion gefallen: neu ausstellen.
	if !h.neueSitzung(w, r, a) {
		return
	}
	h.audit(r.Context(), a.NutzerID, AuditActionKontoWegGeloest, "anbieter="+anbieter+";ergebnis=ok")
	log.Printf("aboard-server: konto: Anmeldeweg %q gelöst (Person %s), alle Sitzungen neu ausgestellt", anbieter, a.NutzerID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Rückruf mit Zweck link

// zurueck leitet nach dem Verknüpfen in die App; die Oberfläche öffnet die
// Einstellungen und nennt das Ergebnis. Nie ein frei wählbares Ziel.
func (h *KontoEinstellungen) zurueck(w http.ResponseWriter, r *http.Request, ergebnis string) {
	http.Redirect(w, r, "/?"+KontoErgebnisParam+"="+ergebnis, http.StatusSeeOther)
}

// abschliessen bindet das Anbieter-Konto (iss, sub) an die Person, die den
// Vorgang begonnen hat — aber nur, wenn deren Sitzung (Hash im Zustand) noch
// die ist, die dieses Cookie trägt. Alles andere bindet nichts. frisch: der
// Vorgang wurde mit einem eingelösten Nachweis begonnen (#1098).
func (h *KontoEinstellungen) abschliessen(ctx context.Context, w http.ResponseWriter, r *http.Request, sitzungHash string, frisch bool, kuerzel, iss, sub string) {
	s, _, ok := h.sitzungDesVorgangs(ctx, w, r, sitzungHash, "Verknüpfen")
	if !ok {
		return
	}
	// Zweite Schranke (P1, fail closed): Wege ändern darf eine Person mit
	// aktivem Konto und Passwort, oder ein Vorgang, den ein eingelöster
	// Nachweis „frisch bestätigt“ begonnen hat (Merkmal im signierten Zustand).
	k, kerr := h.Konten.FuerAnmeldung(ctx, s.Email)
	if kerr != nil && !errors.Is(kerr, storage.ErrKontoUnbekannt) {
		writeError(w, http.StatusInternalServerError, "account lookup failed", kerr)
		return
	}
	mitPasswort := kerr == nil && k.Zustand == storage.KontoAktiv && k.PasswortHash != ""
	// Ein vorhandenes, nicht aktives Konto ändert nie Wege (B3), auch mit Frisch.
	if (kerr == nil && k.Zustand != storage.KontoAktiv) || (!mitPasswort && !frisch) {
		h.zurueck(w, r, KontoErgebnisFehler)
		return
	}
	uid, _ := h.Bindungen.NutzerID(ctx, s.Email)
	ersetzt, err := h.Bindungen.Verknuepfen(ctx, s.Email, kuerzel, iss, sub)
	switch {
	case errors.Is(err, storage.ErrBindungBelegt):
		h.zurueck(w, r, KontoErgebnisBelegt)
		return
	case errors.Is(err, storage.ErrUserNotFound):
		h.zurueck(w, r, KontoErgebnisFehler)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "bind failed", err)
		return
	}
	log.Printf("aboard-server: konto: Anmeldeweg %q verknüpft (Person %s, ersetzt: %v)", kuerzel, uid, ersetzt)
	if ersetzt {
		// Der Speicher hat alle Sitzungen der Person beendet: neu ausstellen.
		h.audit(ctx, uid, AuditActionKontoWegErsetzt, "anbieter="+kuerzel+";ergebnis=ok")
		if !ausstellen(w, r, h.Sitzungen, s.Email, s.Name) {
			return
		}
		h.zurueck(w, r, KontoErgebnisErsetzt)
		return
	}
	h.audit(ctx, uid, AuditActionKontoWegVerknuepft, "anbieter="+kuerzel+";ergebnis=ok")
	h.zurueck(w, r, KontoErgebnisVerknuepft)
}
