package api

// boardpaar_aenderungen.go — Journal außen abholen und bestätigen
// (ADR-0141 §5.2, Karte #827, Etappe E1b Teil 3).
//
//	GET  /agent/boardpaar/aenderungen/{aussenBoardId}?nach=<seq>&max=<n>  abe_
//	POST /agent/boardpaar/aenderungen/{aussenBoardId}/bestaetigt {"bis": <seq>}  abe_
//
// Nur außen und nur für Kopien (kopie = 1); sonst 404 wie die übrigen Wege mit
// Board-ID (W2-5). GET setzt abgeholt_at; HEAD liest nur (ein Proxy oder eine
// Prüfung, die HEAD schickt, verschiebt nichts). Ein Cursor jenseits der
// Journal-Marke ist 409: Außen wurde eine Sicherung eingespielt, das Skript
// geht in HALT (§5.1, PK-5 d).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// aenderungenUhr: Zeitquelle für abgeholt_at, bestaetigt_at und „veraltet“.
// Test-Naht; im Betrieb time.Now.
var aenderungenUhr = time.Now

// kopieAussen prüft Seite, Zugang und Kopplung eines Journal-Wegs. nil heißt:
// Antwort ist geschrieben.
func (a *BoardpaarKopieAPI) kopieAussen(w http.ResponseWriter, r *http.Request, ctx context.Context, weg string) (string, *storage.BoardpaarToken) {
	if !a.seite(w, ctx, BoardpaarSeiteAussen) {
		return "", nil
	}
	z := a.zugang(w, r, storage.BoardpaarTypEingang, weg)
	if z == nil {
		return "", nil
	}
	boardID := r.PathValue("boardId")
	kopplung, err := a.paar.AussenGekoppelt(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return "", nil
	}
	if kopplung == nil || !kopplung.Kopie {
		log.Printf("%s: zugang=%q board=%q → 404 (nicht gekoppelt, unbekannt oder keine kopie)", weg, z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return "", nil
	}
	return boardID, z
}

// zahlParameter: nicht-negative ganze Zahl aus der Abfrage; fehlt sie, gilt
// standard.
func zahlParameter(r *http.Request, name string, standard int64) (int64, bool) {
	roh := r.URL.Query().Get(name)
	if roh == "" {
		return standard, true
	}
	n, err := strconv.ParseInt(roh, 10, 64)
	return n, err == nil && n >= 0
}

// journalFehler ordnet einen Speicherfehler einem Status zu.
func journalFehler(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrJournalCursor):
		writeError(w, http.StatusConflict, "cursor beyond the end of the journal (backup restored on this side?)")
	case errors.Is(err, storage.ErrBoardpaarNichtGekoppelt):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, storage.ErrDBBelegt), errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "database busy, try again")
	default:
		writeError(w, http.StatusInternalServerError, "journal failed", err)
	}
}

// aenderungen — GET/HEAD /boardpaar/aenderungen/{boardId}.
func (a *BoardpaarKopieAPI) aenderungen(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-aenderungen"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	boardID, z := a.kopieAussen(w, r, ctx, weg)
	if z == nil {
		return
	}
	nach, ok1 := zahlParameter(r, "nach", 0)
	max, ok2 := zahlParameter(r, "max", storage.JournalAbrufMax)
	if !ok1 || !ok2 {
		writeError(w, http.StatusUnprocessableEntity, "nach und max: nicht-negative ganze Zahlen")
		return
	}
	if max > storage.JournalAbrufMax {
		max = storage.JournalAbrufMax
	}
	nurLesen := r.Method == http.MethodHead
	abruf, err := a.boards.JournalAbholen(ctx, boardID, nach, int(max), aenderungenUhr(), nurLesen)
	if err != nil {
		if errors.Is(err, storage.ErrJournalCursor) {
			log.Printf("%s: zugang=%q board=%s nach=%d → 409 (Cursor jenseits des Journals)", weg, z.Name, boardID, nach)
		}
		journalFehler(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, abruf)
}

// bestaetigt — POST /boardpaar/aenderungen/{boardId}/bestaetigt {"bis": <seq>}.
func (a *BoardpaarKopieAPI) bestaetigt(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-bestaetigt"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	boardID, z := a.kopieAussen(w, r, ctx, weg)
	if z == nil {
		return
	}
	var body struct {
		Bis *int64 `json:"bis"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Bis == nil || *body.Bis < 0 {
		writeError(w, http.StatusUnprocessableEntity, `body: erwartet {"bis": <seq>}`)
		return
	}
	n, err := a.boards.JournalBestaetigen(ctx, boardID, *body.Bis, aenderungenUhr())
	if err != nil {
		journalFehler(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bestaetigt": n})
}
