package api

// agent_werkzeuge_termine.go — Werkzeuge für Termine ohne Karte (ADR-0138 §4,
// Karte #743): termin_anlegen und termine_lesen. Damit ersetzt aBoard den
// eingebauten Kalender von Open WebUI (das Partnerprojekt schaltet ihn danach ab).
//
// Wege, Berechtigung und Schreibpfad wie in agent_werkzeuge.go:
//   - termin_anlegen: POST /werkzeuge/boards/{boardId}/termine, Schreibrecht
//     auf dem Board (werkzeugDarf). Das Board ist Pflicht — einen
//     Standard-Kalender gibt es nicht (ADR-0138 §6); nennt die Person keins,
//     fragt das Modell nach (Beschreibung in der Spezifikation). Geschrieben
//     wird über postOps (termin.upsert), mit demselben Wiederholungsschutz wie
//     karte_anlegen (agent_werkzeuge_idem.go). `erstelltVon` stempelt postOps
//     (agentPayload, agentTerminHerkunft).
//   - termine_lesen: GET /werkzeuge/termine — kein Board im Pfad, wie
//     `boards`: beim delegierenden Token die Boards der Person aus der ACL
//     (Rolle gekappt, Service-Board nie), bei Projekt- und Personen-Tokens das
//     eigene Board.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/calendar"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// termineLesenStandardTage: Zeitraum ohne `bis` (ab inklusive).
	termineLesenStandardTage = 30
	// termineLesenMaxTage: längster Zeitraum je Anfrage.
	termineLesenMaxTage = 366
	// termineLesenMaxBytes: Obergrenze der Terminliste (wie board_lesen).
	termineLesenMaxBytes = boardLesenMaxBytes
	// terminBeschreibungMax: Zeichen der Beschreibung in termine_lesen.
	terminBeschreibungMax = 500
)

// terminAnlegenFelder: was termin_anlegen annimmt (ADR-0138 §4; bisDatum für
// mehrtägige Termine wie eine Messe).
var terminAnlegenFelder = []string{"titel", "datum", "bisDatum", "von", "bis", "ort", "beschreibung"}

// werkzeugTerminLink: „im Board öffnen“ für einen Termin — die Kalenderansicht
// öffnet ihn (src/deeplink.js parseTerminRef). nil ohne öffentliche Basis.
func (a *BoardsAPI) werkzeugTerminLink(boardID, terminID string) *string {
	l := terminLink(a.oeffentlicheBasis, boardID, terminID)
	if l == "" {
		return nil
	}
	return &l
}

// terminEingaben: geprüfte Eingaben von termin_anlegen.
type terminEingaben struct {
	titel, datum, bisDatum, von, bis, ort, beschreibung string
}

// leseTerminEingaben prüft die Eingaben. bestandTitel ist beim Ändern der Titel des Bestandstermins ("" bei der Anlage): Ein unverändert
// übernommener Titel braucht kein Inhaltszeichen (F-2402, wie titelUnveraendert bei Board/Swimlane);
// nur ein neuer oder geänderter Titel wird mit textFeldInhalt geprüft.
func leseTerminEingaben(m map[string]json.RawMessage, bestandTitel string) (terminEingaben, string) {
	var e terminEingaben
	var grund string
	titelPruefen := textFeldInhalt
	if titelUnveraendert(m, "titel", bestandTitel) {
		titelPruefen = textFeld
	}
	if e.titel, grund = titelPruefen(m, "titel", maxVorschlagTitel, true, false); grund != "" {
		return e, grund
	}
	if e.datum, grund = textFeld(m, "datum", 10, true, false); grund != "" {
		return e, grund
	}
	if !istTerminDatum(e.datum) {
		return e, "datum muss ein Datum YYYY-MM-DD sein (z. B. 2026-10-08)"
	}
	if e.bisDatum, grund = textFeld(m, "bisDatum", 10, false, false); grund != "" {
		return e, grund
	}
	if e.bisDatum != "" && (!istTerminDatum(e.bisDatum) || e.bisDatum < e.datum) {
		return e, "bisDatum muss ein Datum YYYY-MM-DD sein, nicht vor datum (nur für mehrtägige Termine)"
	}
	if e.bisDatum == e.datum {
		e.bisDatum = ""
	}
	for _, f := range []struct {
		name string
		ziel *string
	}{{"von", &e.von}, {"bis", &e.bis}} {
		if *f.ziel, grund = textFeld(m, f.name, 5, false, false); grund != "" {
			return e, grund
		}
		if *f.ziel != "" && !calendar.IstUhrzeit(*f.ziel) {
			return e, f.name + " muss eine Uhrzeit HH:MM sein (24 Stunden, z. B. 14:30)"
		}
	}
	if e.bisDatum == "" && e.von != "" && e.bis != "" && e.bis <= e.von {
		return e, "bis muss nach von liegen (gleicher Tag)"
	}
	if e.ort, grund = textFeld(m, "ort", maxVorschlagTitel, false, false); grund != "" {
		return e, grund
	}
	if e.beschreibung, grund = textFeld(m, "beschreibung", maxVorschlagBeschreibung, false, true); grund != "" {
		return e, grund
	}
	return e, ""
}

// werkzeugTerminAnlegen — POST /werkzeuge/boards/{boardId}/termine.
func (a *BoardsAPI) werkzeugTerminAnlegen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, a.anlegenFelder(r.Context(), terminAnlegenFelder...)...)
	if !ok {
		return
	}
	// Karte #797 (ADR-0141 W-A): optionales `vertraulich`, nur ein
	// ausdrückliches false öffnet.
	r, grund := a.leseAnlageVertraulich(r, m)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	e, grund := leseTerminEingaben(m, "")
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	// Karte #797, K-1: Ein ausdrückliches vertraulich:false gehört zur Anfrage.
	inhalt := append([]string{e.titel, e.datum, e.bisDatum, e.von, e.bis, e.ort, e.beschreibung}, anlageOffenSumme(r.Context())...)
	idem, ok := a.werkzeugIdemBeginn(w, r, p, boardID, "termin_anlegen", inhalt...)
	if !ok {
		return
	}
	a.terminAnlegenVersuch(w, r, boardID, e, idem, false)
}

// terminAufBoard: der Termin mit dieser id im Board-Stand, sonst nil. Liest
// wie der Fold über exakte Schlüssel (leseTermine).
func (a *BoardsAPI) terminAufBoard(w http.ResponseWriter, r *http.Request, boardID, terminID string) (*terminEintrag, bool) {
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		if errors.Is(err, storage.ErrBoardNotFound) {
			writeError(w, http.StatusForbidden, werkzeugVerbotenMeldung)
		} else {
			writeError(w, http.StatusInternalServerError, "board read failed", err)
		}
		return nil, false
	}
	for _, t := range leseTermine(b.StateJSON) {
		if t.ID == terminID {
			t.BoardID, t.BoardTitle = b.ID, b.Title
			return &t, true
		}
	}
	return nil, true
}

// terminAnlegenVersuch: wie karteAnlegenVersuch. erneuert=true: Die Ids sind
// in diesem Aufruf schon einmal erneuert worden (V1-4) — kein zweites Mal.
func (a *BoardsAPI) terminAnlegenVersuch(w http.ResponseWriter, r *http.Request, boardID string, e terminEingaben, idem wkIdemEintrag, erneuert bool) {
	vorhanden, ok := a.terminAufBoard(w, r, boardID, idem.objektID)
	if !ok {
		return
	}
	if vorhanden != nil {
		a.terminAntwort(r.Context(), w, boardID, *vorhanden, true)
		return
	}
	termin := map[string]any{"id": idem.objektID, "titel": e.titel, "datum": e.datum, "erstellt": idem.datum}
	for k, v := range map[string]string{"bisDatum": e.bisDatum, "von": e.von, "bis": e.bis, "ort": e.ort, "beschreibung": e.beschreibung} {
		if v != "" {
			termin[k] = v
		}
	}
	quittung, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{
		"id":      idem.clientID,
		"type":    "termin.upsert",
		"payload": map[string]any{"termin": termin},
	}})
	if !ok {
		return
	}
	jetzt, ok := a.terminAufBoard(w, r, boardID, idem.objektID)
	if !ok {
		return
	}
	if !jsWahr(quittung[0]["wiederholt"]) {
		if jetzt == nil {
			// Nach postOps ohne Ablehnung muss der Termin da sein.
			writeError(w, http.StatusInternalServerError, "termin not created")
			return
		}
		a.terminAntwort(r.Context(), w, boardID, *jetzt, false)
		return
	}
	// Ein gleichzeitiger Aufruf mit demselben Schlüssel war schneller.
	switch {
	case jetzt != nil:
		a.terminAntwort(r.Context(), w, boardID, *jetzt, true)
	case idem.mitKopf:
		// KA-4: gleicher Schlüssel = gleiche Anfrage; nichts neu anlegen.
		writeJSON(w, http.StatusOK, map[string]any{
			"ergebnis":   "Termin war bereits angelegt (Wiederholung erkannt) und wurde inzwischen gelöscht — nichts neu angelegt.",
			"wiederholt": true, "boardId": boardID})
	case !erneuert:
		// V1-4: Der Termin des ersten Aufrufs ist inzwischen gelöscht — ein
		// neuer Wunsch, neu anlegen.
		a.terminAnlegenVersuch(w, r, boardID, e, a.werkzeugIdemErneuern(idem), true)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"ergebnis":   "bereits angelegt (Wiederholung erkannt) — der Termin steht inzwischen nicht mehr auf dem Board",
			"wiederholt": true, "boardId": boardID})
	}
}

// terminZeitText: „2026-10-08 14:00–15:00“, „2026-10-20 bis 2026-10-22“ …
func terminZeitText(t terminEintrag) string {
	s := t.Datum
	if t.Von != "" {
		s += " " + t.Von
	}
	if t.BisDatum != "" {
		s += " bis " + t.BisDatum
		if t.Bis != "" {
			s += " " + t.Bis
		}
		return s
	}
	if t.Bis != "" {
		if t.Von != "" {
			return s + "–" + t.Bis
		}
		return s + " bis " + t.Bis
	}
	return s
}

func (a *BoardsAPI) terminAntwort(ctx context.Context, w http.ResponseWriter, boardID string, t terminEintrag, wiederholt bool) {
	ergebnis := "Termin angelegt: " + t.Titel + " am " + terminZeitText(t)
	if wiederholt {
		ergebnis = "Termin war bereits angelegt (Wiederholung erkannt, nichts doppelt angelegt): " + t.Titel + " am " + terminZeitText(t)
	}
	antwort := map[string]any{
		"ergebnis": ergebnis,
		"terminId": t.ID,
		"titel":    t.Titel,
		"datum":    t.Datum,
		"boardId":  boardID,
		"link":     a.werkzeugTerminLink(boardID, t.ID),
	}
	for k, v := range map[string]string{"bisDatum": t.BisDatum, "von": t.Von, "bis": t.Bis, "ort": t.Ort} {
		if v != "" {
			antwort[k] = v
		}
	}
	if wiederholt {
		antwort["wiederholt"] = true
	}
	a.vertraulichAntwort(ctx, antwort, t.Vertraulich)
	writeJSON(w, http.StatusOK, antwort)
}

// wkTerminZeile: ein Termin in termine_lesen.
type wkTerminZeile struct {
	TerminID     string  `json:"terminId"`
	Titel        string  `json:"titel"`
	Datum        string  `json:"datum"`
	BisDatum     string  `json:"bisDatum,omitempty"`
	Von          string  `json:"von,omitempty"`
	Bis          string  `json:"bis,omitempty"`
	Ort          string  `json:"ort,omitempty"`
	Beschreibung string  `json:"beschreibung,omitempty"`
	ErstelltVon  string  `json:"erstelltVon,omitempty"`
	Board        string  `json:"board"`
	BoardID      string  `json:"boardId"`
	Link         *string `json:"link"`
}

// werkzeugTermineLesen — GET /werkzeuge/termine?ab=YYYY-MM-DD&bis=YYYY-MM-DD.
// Ohne `ab` gilt heute in der Instanz-Zeitzone (kalender.zeitzone, Standard
// Europe/Berlin), ohne `bis` ab+30 Tage.
func (a *BoardsAPI) werkzeugTermineLesen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	q := r.URL.Query()
	ab := strings.TrimSpace(q.Get("ab"))
	if ab == "" {
		// „heute“ in der Instanz-Zeitzone, nicht in UTC (ADR-0138 §2,
		// Kritiker 07.10., K-5) — sonst liefert es kurz nach Mitternacht den
		// Vortag.
		loc, _, _ := KalenderZeitzone(r.Context(), a.kalenderCfg)
		ab = opsNow().In(loc).Format("2006-01-02")
	}
	if !istTerminDatum(ab) {
		writeError(w, http.StatusBadRequest, "ab muss ein Datum YYYY-MM-DD sein")
		return
	}
	abZeit, _ := time.Parse("2006-01-02", ab)
	bis := strings.TrimSpace(q.Get("bis"))
	if bis == "" {
		bis = abZeit.AddDate(0, 0, termineLesenStandardTage).Format("2006-01-02")
	}
	bisZeit, err := time.Parse("2006-01-02", bis)
	if err != nil || len(bis) != 10 || bis < ab {
		writeError(w, http.StatusBadRequest, "bis muss ein Datum YYYY-MM-DD sein, nicht vor ab")
		return
	}
	if bisZeit.Sub(abZeit) > termineLesenMaxTage*24*time.Hour {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Zeitraum zu lang: höchstens %d Tage je Anfrage", termineLesenMaxTage))
		return
	}

	ctx, cancel := contextMitFrist(r)
	defer cancel()
	permitted := map[string]struct{}{}
	if p.Delegiert {
		if a.acl == nil {
			writeError(w, http.StatusServiceUnavailable, "board list not available (no acl)")
			return
		}
		eintraege, err := a.acl.ListBoardsWithRoleForUser(ctx, p.OnBehalfOf)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "board list failed", err)
			return
		}
		for _, e := range eintraege {
			if storage.KappeAgentRolle(p.tokenRolle, e.Role) != "" {
				permitted[e.BoardID] = struct{}{}
			}
		}
	} else if p.MehrBoard {
		for b := range p.boardRollen {
			permitted[b] = struct{}{}
		}
	} else if p.BoardID != "" {
		permitted[p.BoardID] = struct{}{}
	}
	delete(permitted, storage.ServiceBoardID)
	termine, err := collectTermine(ctx, a.repo, permitted, ab, bis)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	// collectTermine liefert nach Datum, dann Uhrzeit sortiert (ganztags zuerst).
	zeilen := []wkTerminZeile{}
	belegt := 0
	for _, t := range termine {
		z := wkTerminZeile{TerminID: t.ID, Titel: kuerze(t.Titel, boardLesenMaxTitel), Datum: t.Datum, BisDatum: t.BisDatum,
			Von: t.Von, Bis: t.Bis, Ort: t.Ort, Beschreibung: kuerze(t.Beschreibung, terminBeschreibungMax),
			ErstelltVon: t.ErstelltVon, Board: t.BoardTitle, BoardID: t.BoardID, Link: a.werkzeugTerminLink(t.BoardID, t.ID)}
		roh, _ := json.Marshal(z)
		if belegt+len(roh) > termineLesenMaxBytes {
			continue
		}
		belegt += len(roh)
		zeilen = append(zeilen, z)
	}
	var person any
	if p.OnBehalfOf != "" {
		person = p.OnBehalfOf
	}
	antwort := struct {
		// V1-3: Hinweis als erstes Feld, vor dem Fremdtext.
		Sicherheitshinweis string          `json:"sicherheitshinweis"`
		Person             any             `json:"person"`
		Zeitraum           map[string]any  `json:"zeitraum"`
		Termine            []wkTerminZeile `json:"termine"`
		Anzahl             int             `json:"anzahl"`
		Gekuerzt           bool            `json:"gekuerzt"`
		Hinweis            string          `json:"hinweis,omitempty"`
	}{
		Sicherheitshinweis: werkzeugFremdtextHinweis,
		Person:             person,
		Zeitraum:           map[string]any{"ab": ab, "bis": bis},
		Termine:            zeilen,
		Anzahl:             len(termine),
		Gekuerzt:           len(zeilen) < len(termine),
	}
	if antwort.Gekuerzt {
		antwort.Hinweis = "Gekürzt: " + strconv.Itoa(len(zeilen)) + " von " + strconv.Itoa(len(termine)) + " Terminen gezeigt. Den Zeitraum mit ab und bis eingrenzen."
	}
	writeJSON(w, http.StatusOK, antwort)
}
