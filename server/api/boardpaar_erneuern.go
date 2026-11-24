package api

// boardpaar_erneuern.go — Board-Paar-Zugänge erneuern sich selbst (Karte #796,
// T-R12, Vertrag zum Board-Paar (10.10.2026)).
//
//	POST /agent/boardpaar/erneuern   — Authorization: Bearer <abs_…, abr_…, abe_… oder abb_…>
//
// Antwort 200: {"token": "<neues Token>", "typ", "name", "alt_gilt_bis",
// "wiederholt"}. 503 + Retry-After: Systemuhr nicht gestellt — später erneut. Das alte Token gilt bis alt_gilt_bis (10 min) weiter und ist
// dann tot. Wer in dieser Zeit nochmals mit dem alten erneuert, bekommt
// dasselbe neue Token (wiederholt: true) — ein Abbruch zwischen Antwort und
// Speichern sperrt nicht aus. Danach, mit einem widerrufenen oder unbekannten
// Token: 401. Für den Betreiber des Push-Skripts heisst 401 hier „Alarm und Stopp, kein zweiter
// Versuch“: Jemand anderes hat mit dem Token erneuert.
//
// Nur am Agenten-Eingang (BoardpaarSchranke) und nur mit einem Typ, der zur
// Seite der Instanz gehört: abs_ und abr_ innen (ADR-0141 §5.3), abe_ außen. Bei boardpaar.seite = aus
// gibt es den Weg nicht (404, wie Ausgang und Eingang). Ausnahme abb_
// (Sicherungen lesen, ADR-0142): Sein Weg gilt auf jeder Seite, also auch
// sein Erneuern — auf „aus“ nur für abb_, jedes andere Token bekommt dort
// weiter 404.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// BoardpaarErneuernPfad: der Weg im Mux (am Agenten-Eingang nach dem
// Abschneiden von /agent).
const BoardpaarErneuernPfad = "/boardpaar/erneuern"

// BoardpaarErneuernAPI tauscht ein Board-Paar-Token gegen ein neues.
type BoardpaarErneuernAPI struct {
	cfg  *storage.ConfigRepo
	paar *storage.BoardpaarRepo
	// limit: Fehlversuche je IP (eigenes Kontingent).
	limit   *rateLimiter
	adresse func(*http.Request) string
}

func NewBoardpaarErneuernAPI(cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo) *BoardpaarErneuernAPI {
	return &BoardpaarErneuernAPI{cfg: cfg, paar: paar, limit: newRateLimiter(30, time.Hour), adresse: tcpAdresse}
}

// MitAdressregel setzt die Adressregel für Drossel und Protokoll — im Server
// die der Agenten-Bremse (AgentZugang.ClientAdresse, F-2030).
func (a *BoardpaarErneuernAPI) MitAdressregel(fn func(*http.Request) string) *BoardpaarErneuernAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

func (a *BoardpaarErneuernAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST "+BoardpaarErneuernPfad, WithDefaults(http.HandlerFunc(a.erneuern)))
}

type erneuernAntwort struct {
	Token      string `json:"token"`
	Typ        string `json:"typ"`
	Name       string `json:"name"`
	AltGiltBis string `json:"alt_gilt_bis"`
	Wiederholt bool   `json:"wiederholt"`
}

func (a *BoardpaarErneuernAPI) erneuern(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-erneuern"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if a.paar == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Die Seite VOR dem Token, wie am Ausgang: Bei „aus“ gibt es den Weg nicht.
	// Den Typ bestimmt der Präfix des mitgeschickten Tokens (ADR-0141 §5.3):
	// innen abs_ und abr_, außen abe_.
	t, ok := bearerToken(r)
	typ := storage.BoardpaarTypAusPraefix(t)
	var erlaubt []string
	switch BoardpaarSeite(ctx, a.cfg) {
	case BoardpaarSeiteInnen:
		erlaubt = []string{storage.BoardpaarTypAusgang, storage.BoardpaarTypRueckweg, storage.BoardpaarTypSicherungen}
	case BoardpaarSeiteAussen:
		erlaubt = []string{storage.BoardpaarTypEingang, storage.BoardpaarTypSicherungen}
	default:
		if typ != storage.BoardpaarTypSicherungen {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		erlaubt = []string{storage.BoardpaarTypSicherungen}
	}
	if !ok || typ == "" || !slices.Contains(erlaubt, typ) {
		boardpaarAbweisen(w, r, a.limit, a.adresse, weg, "ohne passendes token", "bearer token ("+strings.Join(erlaubt, "_, ")+"_) required")
		return
	}
	adresse := a.adresse(r)
	e, err := a.paar.TokenErneuern(ctx, typ, t, adresse)
	if errors.Is(err, storage.ErrBoardpaarTokenUnbekannt) {
		boardpaarAbweisen(w, r, a.limit, a.adresse, weg, "token unbekannt, widerrufen oder abgelaufen", "unknown, revoked or expired token")
		return
	}
	if errors.Is(err, storage.ErrBoardpaarUhr) {
		// Kritiker K-1: Ohne gestellte Uhr keine Frist — später erneut,
		// kein Alarm. Höchstens eine Zeile pro Minute.
		if schattenAbweisungLogErlaubt(time.Now()) {
			log.Printf("%s: zugang=%s_ von=%s → 503, systemuhr nicht gestellt (Wissen §38)", weg, typ, adresse)
		}
		w.Header().Set("Retry-After", "300")
		writeError(w, http.StatusServiceUnavailable, "server clock not synchronized, retry later")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "renewal failed", err)
		return
	}
	// Vertrag Punkt 3: wer wann von wo erneuert hat — jede Erneuerung, ohne
	// Drossel (sie kommt einmal die Woche), nie das Token.
	art := "erneuert"
	if e.Wiederholt {
		art = "wiederholt (dasselbe neue token)"
	}
	log.Printf("%s: zugang=%s_ %q von=%s → %s, altes token gilt bis %s", weg, typ, e.Zugang.Name, adresse, art, e.AltGiltBis)
	// Kritiker F-B: Eine Wiederholung schreibt nichts in die Datenbank — holt
	// ein Dritter mit dem alten Token in der Übergangszeit dasselbe neue ab,
	// steht seine Adresse nur hier. Deshalb laut, wenn sie abweicht.
	if e.Wiederholt && e.Zugang.ErneuertVon != adresse {
		log.Printf("%s: WARNUNG zugang=%s_ %q: wiederholung von %s, erneuert hatte %s — möglicher diebstahl, zugang prüfen",
			weg, typ, e.Zugang.Name, adresse, e.Zugang.ErneuertVon)
	}
	writeJSON(w, http.StatusOK, erneuernAntwort{Token: e.Token, Typ: typ, Name: e.Zugang.Name, AltGiltBis: e.AltGiltBis, Wiederholt: e.Wiederholt})
}
