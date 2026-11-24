package api

// termine.go — Termine ohne Karte lesen (ADR-0138 §4, Karte #743).
//
// Ein Termin liegt in `board.termine[]` des Board-Zustands (state_json). Er ist
// KEINE Karte: Die Kartenleser des Servers (Kalender-Sammlung, Werkzeuge,
// Projekte, Mail) sehen ihn nicht, und das ist Absicht (ADR-0138 §4, „Fehlt
// irgendwo ein Leser, fehlt der Termin, statt als Aufgabe aufzutauchen“).
// Gelesen wird er nur hier — für die Kalenderansicht über alle Boards
// (GET /me/termine), den Abo-Feed (ical.go) und das Werkzeug termine_lesen.
//
// Geschrieben wird ausschließlich über die Ops termin.upsert/termin.delete
// (src/ops.js, eingebettet in server/fold), die Form prüft der Fold.
//
// Gelesen wird über exakte Map-Schlüssel (wie leseWkBoard): Ein
// Struct-Unmarshal nähme auch „TITEL“, und dann zeigte der Feed einen anderen
// Titel als der Browser, der exakt liest.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/calendar"
	"github.com/Andiministrator/aBoard/server/storage"
)

// terminEintrag ist ein gültiger Termin samt Board — Wire-Format von
// GET /me/termine und Grundlage für Feed und termine_lesen.
type terminEintrag struct {
	BoardID      string   `json:"board_id"`
	BoardTitle   string   `json:"board_title"`
	ID           string   `json:"id"`
	Titel        string   `json:"titel"`
	Datum        string   `json:"datum"`
	BisDatum     string   `json:"bisDatum,omitempty"`
	Von          string   `json:"von,omitempty"`
	Bis          string   `json:"bis,omitempty"`
	Ort          string   `json:"ort,omitempty"`
	Beschreibung string   `json:"beschreibung,omitempty"`
	Personen     []string `json:"personen,omitempty"`
	KarteID      string   `json:"karteId,omitempty"`
	Erstellt     string   `json:"erstellt,omitempty"`
	ErstelltVon  string   `json:"erstelltVon,omitempty"`
	// VonAgent: zuletzt von einem Agenten geändert (Fold-Marke, Karte #748).
	// Nur für den Feed „für außen“; nicht im Wire-Format.
	VonAgent bool `json:"-"`
	// Vertraulich: Bestandswert von confidential (Karte #797, K-1); nur für
	// die Antwort der Werkzeuge, nicht im Wire-Format.
	Vertraulich bool `json:"-"`
}

// istTerminDatum: Kalenderdatum YYYY-MM-DD (kein 2026-02-30) — wie
// istTerminDatum in src/ops.js.
func istTerminDatum(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// leseTermine liefert die gültigen Termine eines Board-Zustands. Einträge mit
// fehlender id, leerem Titel oder ungültigem Datum fallen weg (der Fold lässt
// solche nicht entstehen; Altbestand aus einem PUT soll keinen Feed brechen).
// Eine ungültige Uhrzeit oder ein ungültiges bisDatum wird verworfen, der
// Termin bleibt (ganztags bzw. eintägig).
func leseTermine(state []byte) []terminEintrag {
	var stand map[string]json.RawMessage
	if json.Unmarshal(state, &stand) != nil {
		return nil
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(stand["board"], &board) != nil || board == nil {
		return nil
	}
	var roh []map[string]json.RawMessage
	if json.Unmarshal(board["termine"], &roh) != nil {
		return nil
	}
	aus := make([]terminEintrag, 0, len(roh))
	for _, t := range roh {
		if t == nil {
			continue
		}
		e := terminEintrag{
			ID: zeichenketteVon(t["id"]), Titel: strings.TrimSpace(zeichenketteVon(t["titel"])),
			Datum: zeichenketteVon(t["datum"]), BisDatum: zeichenketteVon(t["bisDatum"]),
			Von: zeichenketteVon(t["von"]), Bis: zeichenketteVon(t["bis"]),
			Ort: zeichenketteVon(t["ort"]), Beschreibung: zeichenketteVon(t["beschreibung"]),
			Personen: zeichenkettenListe(t["personen"]), KarteID: zeichenketteVon(t["karteId"]),
			Erstellt: zeichenketteVon(t["erstellt"]), ErstelltVon: zeichenketteVon(t["erstelltVon"]),
			VonAgent: string(t["vonAgent"]) == "true", Vertraulich: vertraulichRoh(t),
		}
		if e.ID == "" || e.Titel == "" || !istTerminDatum(e.Datum) {
			continue
		}
		if e.BisDatum != "" && (!istTerminDatum(e.BisDatum) || e.BisDatum < e.Datum) {
			e.BisDatum = ""
		}
		if !calendar.IstUhrzeit(e.Von) {
			e.Von = ""
		}
		if !calendar.IstUhrzeit(e.Bis) {
			e.Bis = ""
		}
		aus = append(aus, e)
	}
	return aus
}

// terminEnde: letzter Tag des Termins (inklusive).
func (t terminEintrag) terminEnde() string {
	if t.BisDatum != "" {
		return t.BisDatum
	}
	return t.Datum
}

// collectTermine sammelt die Termine der erlaubten Boards, die [from,to]
// berühren. permitted == nil → alle Boards (Legacy-Bypass, wie
// collectCalendarCards). from/to == "" → keine Schranke. Das Service-Board
// (Mail-Eingang) trägt keine Termine und wird übersprungen.
func collectTermine(ctx context.Context, boards *storage.BoardRepo, permitted map[string]struct{}, from, to string) ([]terminEintrag, error) {
	summaries, err := boards.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]terminEintrag, 0)
	for _, s := range summaries {
		if s.ID == storage.ServiceBoardID {
			continue
		}
		if permitted != nil {
			if _, ok := permitted[s.ID]; !ok {
				continue
			}
		}
		b, err := boards.Get(ctx, s.ID)
		if err != nil {
			continue
		}
		for _, t := range leseTermine(b.StateJSON) {
			if !cardInRange(t.Datum, t.terminEnde(), from, to) {
				continue
			}
			t.BoardID, t.BoardTitle = b.ID, b.Title
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Datum != out[j].Datum {
			return out[i].Datum < out[j].Datum
		}
		// Ganztags zuerst, dann nach Uhrzeit — wie die Kalenderansicht
		// (src/calendar/collect.js uhrzeitAmTag: Beginn, sonst Ende).
		return sortierZeit(out[i]) < sortierZeit(out[j])
	})
	return out, nil
}

// terminLink: Deep-Link `#board=<id>&termin=<id>` (src/deeplink.js). Beide
// Werte maskiert wie im Kartenlink des Feeds (F-377-Klasse).
func terminLink(basis, boardID, terminID string) string {
	if basis == "" {
		return ""
	}
	return basis + "/#board=" + url.QueryEscape(boardID) + "&termin=" + url.QueryEscape(terminID)
}

// terminEvent: Termin für den Abo-Feed. UID `<termin-id>@<board-id>.aboard`
// (ADR-0138 §6, Vorgabe des Betreibers: das Push-Skript der Außenstelle verwirft
// Ereignisse ohne Board-id). DESCRIPTION wie bei Karten: Beschreibung,
// gekürzt. Die datensparsame Fassung „für außen“ ist Karte #748.
func terminEvent(t terminEintrag, origin string) calendar.Event {
	return calendar.Event{
		UID:         t.ID + "@" + t.BoardID + ".aboard",
		Start:       t.Datum,
		End:         t.terminEnde(),
		StartTime:   t.Von,
		EndTime:     t.Bis,
		Summary:     t.Titel,
		Description: truncate(t.Beschreibung, 500),
		URL:         terminLink(origin, t.BoardID, t.ID),
		Location:    truncate(t.Ort, 200),
	}
}

// meTermine — GET /me/termine?dueDateRange=<from>~<to>: die Termine aller
// Boards mit Leserecht (Kalenderansicht, Modus „alle Boards“). ACL wie
// /me/cards: bei Forward-Auth-Identität nur Boards mit ACL-Eintrag (inkl.
// Domain-Freigabe), sonst (Legacy) alle.
func (a *CalendarAPI) meTermine(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	from, to, ok := parseDueDateRange(r.URL.Query().Get("dueDateRange"))
	if !ok {
		writeError(w, http.StatusBadRequest, "dueDateRange must be <from>~<to> (YYYY-MM-DD)")
		return
	}
	var permitted map[string]struct{}
	if id := IdentityFromContext(ctx); id != nil && a.acl != nil {
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		ids, err := a.acl.ListBoardsForUser(ctx, email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl lookup failed", err)
			return
		}
		permitted = make(map[string]struct{}, len(ids))
		for _, bid := range ids {
			permitted[bid] = struct{}{}
		}
	}
	out, err := collectTermine(ctx, a.boards, permitted, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "boards list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sortierZeit: Uhrzeit, nach der ein Termin an seinem ersten Tag einsortiert
// wird — `von`, sonst `bis` (nur bei eintägigen Terminen), sonst "" (ganztags,
// sortiert zuerst). Gegenstück zu uhrzeitAmTag (src/calendar/collect.js);
// Kritiker 07.10., K-6.
func sortierZeit(t terminEintrag) string {
	if t.Von != "" {
		return t.Von
	}
	if t.terminEnde() == t.Datum {
		return t.Bis
	}
	return ""
}
