package api

// schatten_anzeige.go — Anzeige außen: GET /boards/{id}/schatten
// (Karte #781, ADR-0140 §5 „Außen“, §6 Kalender).
//
// Der dritte Leser der Schatten-Tabelle (nach dem iCal-Feed #782 und den
// Agenten-Werkzeugen #789): Die externe Board-Ansicht und die Kalenderansicht
// im Browser holen hier die Schatten interner Karten und Termine ihres Boards.
//
// Nur auf der Seite „außen“ und nur für ein gekoppeltes Board; auf aus und
// innen (und für ein nicht gekoppeltes Board) antwortet der Weg 404 — der
// Browser zeigt dann nichts. Die Seite wird je Anfrage gelesen; ist
// server_config nicht lesbar, antwortet er 503 statt als „aus“ (Lesefehler
// ≠ fehlender Wert, sonst verschwänden die Schatten bis zum nächsten Laden).
//
// Erreichbar nur über die Menschen-Kette: Die Rolle prüft der ACL-Guard
// (GET unter /boards/{id}/…: viewer oder mehr); die Positivliste des
// Agenten-Zugangs kennt den Pfad nicht, ein Agent bekommt 403.
//
// Was hinausgeht, ist ein fester Feldsatz (schattenAnzeigeEintrag): Er
// entsteht aus baueWkSchatten, also nach derselben Regel wie Feed und
// Werkzeuge (schatten.AnzeigeTitel: vertraulich → „Interne Karte“; Bearbeiter
// mit „@“ oder „external:“ fallen weg). Dazu der Link nach innen, und nur der
// aus ADR-0140 §4 (innen_link_basis, bei Karten mit Nummer + „&card=<nr>“).
// Keine interne Board- oder Karten-ID, keine E-Mail-Adresse.

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// SchattenAnzeigeAPI bündelt den Lese-Endpunkt der Schatten für die Anzeige
// im Browser (Seite außen).
type SchattenAnzeigeAPI struct {
	cfg      *storage.ConfigRepo
	paar     *storage.BoardpaarRepo
	schatten *storage.SchattenRepo
}

// NewSchattenAnzeigeAPI: Verdrahtung wie main.go (Konfig für die Seite,
// Kopplungen, Schatten).
func NewSchattenAnzeigeAPI(cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo, sr *storage.SchattenRepo) *SchattenAnzeigeAPI {
	return &SchattenAnzeigeAPI{cfg: cfg, paar: paar, schatten: sr}
}

// Register hängt GET /boards/{id}/schatten an den Mux (Menschen-Kette).
func (a *SchattenAnzeigeAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /boards/{id}/schatten", WithDefaults(http.HandlerFunc(a.holeSchattenAnzeige)))
}

// schattenAnzeigeEintrag: ein Schatten, wie der Browser ihn bekommt.
type schattenAnzeigeEintrag struct {
	SchattenID string `json:"schatten_id"`
	Art        string `json:"art"`
	Nummer     string `json:"nummer,omitempty"`
	Titel      string `json:"titel"`
	// TitelPlatzhalter: der Titel ist der Platzhalter „Interne Karte“ (vertraulich
	// oder ohne Titel). Der Browser setzt die Nummer nur vor einen echten Titel —
	// wie der Feed (ical.go schattenEvent, Klasse 198); er vergleicht keinen Text.
	TitelPlatzhalter bool     `json:"titel_platzhalter"`
	Spalte           string   `json:"spalte,omitempty"`
	Prioritaet       string   `json:"prioritaet,omitempty"`
	StartDatum       string   `json:"start_datum,omitempty"`
	StartZeit        string   `json:"start_zeit,omitempty"`
	EndeDatum        string   `json:"ende_datum,omitempty"`
	EndeZeit         string   `json:"ende_zeit,omitempty"`
	Bearbeiter       []string `json:"bearbeiter"`
	VonAgent         bool     `json:"von_agent"`
	// Link: der Menschen-Link nach innen (§4); fehlt, wenn keine Basis bekannt ist.
	Link string `json:"link,omitempty"`
	// Lane/SpalteID (ADR-0141 §8, Karte #825): Kennung von Lane und Spalte
	// aus dem Hinweg v2. Der Browser findet sie über die Marke `spiegel.id`
	// an Lane und Spalte (Standardspalten und Backlog: Klartext-ID).
	Lane     string `json:"lane,omitempty"`
	SpalteID string `json:"spalte_id,omitempty"`
}

type schattenAnzeigeAntwort struct {
	// LetzterAbgleich: Zeitpunkt des letzten gültigen Laufs (UTC, RFC 3339);
	// null, solange es noch keinen gab.
	LetzterAbgleich *string                  `json:"letzter_abgleich"`
	Schatten        []schattenAnzeigeEintrag `json:"schatten"`
	// Kopie (ADR-0141 §8): Das Board ist eine externe Kopie — Schatten stehen
	// in ihrer Lane und Spalte, nicht im Band.
	Kopie bool `json:"kopie"`
	// Bearbeitbar (ADR-0141 Nachtrag #836): der Schalter „bearbeitbar“ dieser
	// Kopie. Der Browser macht Spiegel nur bearbeitbar, wenn zusätzlich /me
	// `boardpaar_rueckweg: "an"` meldet; fehlt das Feld oder ist es false,
	// bleibt die Kopie „nur lesen“ (Schreibschutz aus 2b). Nur bei Kopie true.
	Bearbeitbar bool `json:"bearbeitbar"`
}

// holeSchattenAnzeige — GET /boards/{id}/schatten.
func (a *SchattenAnzeigeAPI) holeSchattenAnzeige(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "fehlende Board-ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")

	antwort, status, err := a.schattenAnzeigeLesen(ctx, id)
	switch {
	case err != nil:
		writeError(w, status, "schatten nicht lesbar", err)
	case status != http.StatusOK:
		writeError(w, status, "not found")
	default:
		writeJSON(w, http.StatusOK, antwort)
	}
}

// schattenAnzeigeLesen: der EINZIGE Leser der Schatten-Tabelle für die
// Anzeige außen (Wächter, main_schatten_780_test.go: genau ein .Liste und ein
// .Stand, benannt nur von holeSchattenAnzeige). Liefert Status 404 (nicht
// außen oder nicht gekoppelt), 503 mit err (Seite, Kopplung oder Schatten
// nicht lesbar) oder 200 mit der Antwort.
func (a *SchattenAnzeigeAPI) schattenAnzeigeLesen(ctx context.Context, boardID string) (schattenAnzeigeAntwort, int, error) {
	var leer schattenAnzeigeAntwort
	if a.cfg == nil || a.paar == nil || a.schatten == nil {
		return leer, http.StatusNotFound, nil
	}
	seite, err := BoardpaarSeiteMitFehler(ctx, a.cfg)
	if err != nil {
		return leer, http.StatusServiceUnavailable, err
	}
	if seite != BoardpaarSeiteAussen {
		return leer, http.StatusNotFound, nil
	}
	paar, err := a.paar.AussenGekoppelt(ctx, boardID)
	if err != nil {
		return leer, http.StatusServiceUnavailable, err
	}
	if paar == nil {
		return leer, http.StatusNotFound, nil
	}
	zeilen, err := a.schatten.Liste(ctx, boardID)
	if err != nil {
		return leer, http.StatusServiceUnavailable, err
	}
	stand, err := a.schatten.Stand(ctx, boardID)
	if err != nil {
		return leer, http.StatusServiceUnavailable, err
	}
	liste := make([]wkSchatten, 0, len(zeilen))
	for _, z := range zeilen {
		liste = append(liste, baueWkSchatten(z))
	}
	sortiereWkSchatten(liste)
	antwort := schattenAnzeigeAntwort{Schatten: make([]schattenAnzeigeEintrag, 0, len(liste)), Kopie: paar.Kopie,
		Bearbeitbar: paar.Kopie && paar.Bearbeitbar}
	if stand != nil && stand.LetzterAbgleich != "" {
		antwort.LetzterAbgleich = &stand.LetzterAbgleich
	}
	for _, s := range liste {
		e := schattenAnzeigeEintrag{SchattenID: s.SchattenID, Art: s.Art, Nummer: s.Nummer, Titel: s.Titel, TitelPlatzhalter: s.platzhalter,
			Spalte: s.Spalte, Prioritaet: s.Prioritaet, StartDatum: s.StartDatum, StartZeit: s.StartZeit,
			EndeDatum: s.EndeDatum, EndeZeit: s.EndeZeit, Bearbeiter: s.Bearbeiter, VonAgent: s.VonAgent,
			Lane: s.lane, SpalteID: s.spalteID}
		if paar.InnenLinkBasis != "" {
			e.Link = paar.InnenLinkBasis
			if s.Art == schatten.ArtKarte && s.nr > 0 {
				e.Link += "&card=" + strconv.Itoa(s.nr)
			}
		}
		antwort.Schatten = append(antwort.Schatten, e)
	}
	return antwort, http.StatusOK, nil
}
