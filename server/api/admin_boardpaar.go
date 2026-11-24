package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Admin-Endpunkte des Board-Paars (ADR-0140 §4, Karte #788): Partnerlisten
// innen und außen und die Zugänge abs_/abe_/abr_/abb_. Koppeln ist eine
// Instanzentscheidung, deshalb nur Server-Admins — ein Board-Owner kann den
// Ausgang nicht selbst eröffnen. Dieselben Schutzhüllen wie die übrige
// Admin-API (requireAdmin, Mutationen mit Rate-Limit, CSRF zentral).
//
// Die Seite der Instanz (boardpaar.seite) setzt der Dialog über den
// allgemeinen Weg PUT /admin/config/boardpaar.seite; handleConfigSet prüft
// den Wert dort (PruefeBoardpaarSeite).
//
//	GET    /admin/boardpaar                 — Seite, beide Listen, Zugänge (ohne Geheimnis)
//	POST   /admin/boardpaar/innen           — {board_id}
//	DELETE /admin/boardpaar/innen/{board}
//	POST   /admin/boardpaar/aussen          — {board_id, innen_link_basis}
//	DELETE /admin/boardpaar/aussen/{board}
//	POST   /admin/boardpaar/zugaenge        — {typ: abs|abe|abr|abb, name, darf_anlegen} → Klartext GENAU EINMAL
//	DELETE /admin/boardpaar/zugaenge/{id}
//	DELETE /admin/boardpaar/zugaenge/{id}/zeile — abgeschlossenen Zugang aus der Tabelle entfernen (#848)
//	POST   /admin/boardpaar/zugaenge/aufraeumen — alle abgeschlossenen entfernen (#848)
//	POST   /admin/boardpaar/auftraege       — {board_id, ziel, innen_link_basis?} „Externe Kopie anlegen“ (ADR-0141 §3)
//	DELETE /admin/boardpaar/auftraege/{id}  — offenen Auftrag zurückziehen
//	DELETE /admin/boardpaar/kopien/{board}  — Notweg „Kopie außen löschen“ (#831, nur außen, nur Kopien)
//	PUT    /admin/boardpaar/kopien/{board}/bearbeitbar — {bearbeitbar: bool} (#836, nur außen, nur Kopien)
//	PUT    /admin/boardpaar/team            — {team, bisher, bestaetigt} (F-2268, Vorschau + Bestätigung)
//
// Die Einstellung boardpaar.team (außen, ADR-0141 §3.4) speichert der
// Admin-Dialog über PUT /admin/boardpaar/team: Fällt dabei jemand heraus, der
// auf Kopien noch Zugriff hat, antwortet der erste Aufruf mit 409 und der
// Liste (Vorschau), erst der bestätigte entzieht (F-2268). Der allgemeine
// Konfig-Weg bleibt für Änderungen ohne Entzug; einen Entzug lehnt er mit 409
// ab. Geprüft wird der Wert beim Speichern (PruefeBoardpaarTeam).
// Ebenso boardpaar.rueckweg (außen, ADR-0141 §7): PruefeBoardpaarRueckweg
// nimmt aus/an an; beim Umschalten gleicht BoardpaarRueckwegFolge in derselben
// Transaktion die Rolle des Teams auf den Kopien an (Plan D9).
func (a *AdminAPI) RegisterBoardpaar(mux *http.ServeMux, repo *storage.BoardpaarRepo) {
	h := &boardpaarAdmin{a: a, repo: repo}
	a.boardpaar = repo
	a.WithKonfigPruefung(KonfigBoardpaarTeam, PruefeBoardpaarTeam)
	a.WithKonfigPruefung(KonfigBoardpaarRueckweg, PruefeBoardpaarRueckweg)
	a.WithKonfigFolge(KonfigBoardpaarRueckweg, BoardpaarRueckwegFolge)
	a.WithKonfigFolge(KonfigBoardpaarTeam, BoardpaarTeamFolge)
	mux.Handle("POST /admin/boardpaar/auftraege", a.protectMutate(h.auftragAnlegen))
	mux.Handle("DELETE /admin/boardpaar/auftraege/{id}", a.protectMutate(h.auftragZurueckziehen))
	mux.Handle("GET /admin/boardpaar", a.protect(h.liste))
	mux.Handle("POST /admin/boardpaar/innen", a.protectMutate(h.innenKoppeln))
	mux.Handle("DELETE /admin/boardpaar/innen/{board}", a.protectMutate(h.innenEntkoppeln))
	mux.Handle("POST /admin/boardpaar/aussen", a.protectMutate(h.aussenKoppeln))
	mux.Handle("DELETE /admin/boardpaar/aussen/{board}", a.protectMutate(h.aussenEntkoppeln))
	mux.Handle("POST /admin/boardpaar/zugaenge", a.protectMutate(h.zugangAnlegen))
	mux.Handle("DELETE /admin/boardpaar/zugaenge/{id}", a.protectMutate(h.zugangWiderrufen))
	mux.Handle("DELETE /admin/boardpaar/zugaenge/{id}/zeile", a.protectMutate(h.zugangZeileEntfernen))
	mux.Handle("POST /admin/boardpaar/zugaenge/aufraeumen", a.protectMutate(h.zugaengeAufraeumen))
	mux.Handle("DELETE /admin/boardpaar/kopien/{board}", a.protectMutate(h.kopieLoeschen))
	mux.Handle("PUT /admin/boardpaar/kopien/{board}/bearbeitbar", a.protectMutate(h.kopieBearbeitbar))
	mux.Handle("PUT /admin/boardpaar/team", a.protectMutate(h.teamSpeichern))
}

type boardpaarAdmin struct {
	a    *AdminAPI
	repo *storage.BoardpaarRepo
}

func (h *boardpaarAdmin) liste(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	innen, err := h.repo.InnenListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	aussen, err := h.repo.AussenListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	zugaenge, err := h.repo.TokenListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	ziele, err := h.repo.ZieleListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	auftraege, err := h.repo.AuftraegeListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	// Karte #831: außen entkoppelte Kopien — für „Kopie außen löschen“.
	frei, err := h.repo.EntkoppelteKopien(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	// F-2268: Ein Lesefehler ist kein leeres Team — sonst zeigte der Dialog
	// ein leeres Feld, und Speichern entzöge allen den Zugriff.
	team := ""
	if v, gerr := h.a.cfg.Get(ctx, KonfigBoardpaarTeam); gerr == nil {
		team = v.Value
	} else if !errors.Is(gerr, storage.ErrConfigKeyNotFound) {
		writeError(w, http.StatusInternalServerError, "config unreadable", gerr)
		return
	}
	// Karte #827 E4: der Schalter des Rückwegs (außen) für den Admin-Reiter.
	rueckwegAn, err := BoardpaarRueckwegAnMitFehler(ctx, h.a.cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config unreadable", err)
		return
	}
	rueckweg := BoardpaarRueckwegAus
	if rueckwegAn {
		rueckweg = BoardpaarRueckwegAn
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"seite":     BoardpaarSeite(ctx, h.a.cfg),
		"innen":     innen,
		"aussen":    aussen,
		"zugaenge":  zugaenge,
		"ziele":     ziele,
		"auftraege": auftraege,
		// Karte #831: Kopien ohne Kopplungszeile (Herkunft, Migration 0042), nur Liste.
		"kopien_entkoppelt": frei,
		"team":              team,
		"rueckweg":          rueckweg,
		// Vorgabe für die Adresse des Boards innen (ABOARD_PUBLIC_BASE_URL);
		// leer heißt: der Admin gibt sie beim Auftrag an.
		"link_basis_vorgabe": h.a.boardpaarBasis,
	})
}

// teamSpeichern — PUT /admin/boardpaar/team (F-2268). bisher ist Pflicht: der
// Wert, den der Admin gesehen hat (409 veraltet, wenn er nicht mehr stimmt).
// Entzöge das Speichern Zugriff und fehlt bestaetigt, wird nichts geschrieben:
// 409 mit bestaetigung_noetig, weg (die herausgefallenen Einträge) und zeilen.
// Sonst 200 mit dem gespeicherten Wert, weg und zeilen.
func (h *boardpaarAdmin) teamSpeichern(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Team       string  `json:"team"`
		Bisher     *string `json:"bisher"`
		Bestaetigt bool    `json:"bestaetigt"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Bisher == nil {
		writeError(w, http.StatusBadRequest, "bisher: der zuletzt gelesene Wert von boardpaar.team ist Pflicht")
		return
	}
	norm, err := PruefeBoardpaarTeam(KonfigBoardpaarTeam, body.Team)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var erg BoardpaarTeamEntzug
	err = h.a.cfg.SetMitFolge(r.Context(), KonfigBoardpaarTeam, norm, false, adminUserIDFromContext(r.Context()),
		BoardpaarTeamFolgeMit(body.Bestaetigt, body.Bisher, &erg))
	switch {
	case errors.Is(err, errBoardpaarTeamVorschau):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "confirmation required",
			"bestaetigung_noetig": true, "weg": erg.Weg, "zeilen": erg.Zeilen})
		return
	case errors.Is(err, ErrBoardpaarTeamVeraltet):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "boardpaar.team changed meanwhile, reload", "veraltet": true})
		return
	case errors.Is(err, storage.ErrKonfigGeheimwert):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "config set failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": norm, "weg": erg.Weg, "zeilen": erg.Zeilen})
}

// auftragAnlegen — „Externe Kopie anlegen“ (ADR-0141 §3.2). Nur innen. Ohne
// innen_link_basis gilt ABOARD_PUBLIC_BASE_URL + „/#board=<id>“.
func (h *boardpaarAdmin) auftragAnlegen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BoardID        string `json:"board_id"`
		Ziel           string `json:"ziel"`
		InnenLinkBasis string `json:"innen_link_basis"`
	}
	if !leseBoardpaarBody(w, r, &body) {
		return
	}
	if BoardpaarSeite(r.Context(), h.a.cfg) != BoardpaarSeiteInnen {
		writeError(w, http.StatusConflict, "externe Kopien legt nur die Seite innen an (boardpaar.seite = innen)")
		return
	}
	link := strings.TrimSpace(body.InnenLinkBasis)
	if link == "" {
		if h.a.boardpaarBasis == "" {
			writeError(w, http.StatusBadRequest, "innen_link_basis fehlt (ABOARD_PUBLIC_BASE_URL ist nicht gesetzt)")
			return
		}
		link = strings.TrimRight(h.a.boardpaarBasis, "/") + "/#board=" + url.QueryEscape(body.BoardID)
	}
	e, err := h.repo.AuftragAnlegen(r.Context(), body.BoardID, body.Ziel, link, adminUserIDFromContext(r.Context()))
	switch {
	case errors.Is(err, storage.ErrBoardpaarZielName):
		writeError(w, http.StatusBadRequest, "ziel: 1–64 characters, letters, digits, . _ -")
		return
	case errors.Is(err, storage.ErrBoardpaarAuftragOffen):
		writeError(w, http.StatusConflict, "for this board and target an open order already exists")
		return
	case err != nil:
		koppelFehler(w, err)
		return
	}
	log.Printf("boardpaar: auftrag %s „kopie anlegen“ board %s ziel %q (Admin)", e.ID, e.BoardID, e.Ziel)
	writeJSON(w, http.StatusCreated, e)
}

func (h *boardpaarAdmin) auftragZurueckziehen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.repo.AuftragZurueckziehen(r.Context(), id)
	switch {
	case errors.Is(err, storage.ErrBoardpaarAuftragUnbekannt):
		writeError(w, http.StatusNotFound, "no such order")
		return
	case errors.Is(err, storage.ErrBoardpaarAuftragErledigt):
		writeError(w, http.StatusConflict, "order already done")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	log.Printf("boardpaar: auftrag %s zurückgezogen (Admin)", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// koppelFehler bildet die Fehler des Koppelns auf Statuscodes ab. Ein
// unbekanntes Board ist 404, das Service-Board und eine ungültige Adresse
// 400 mit Grund.
func koppelFehler(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrBoardpaarBoardUnbekannt):
		writeError(w, http.StatusNotFound, "board not found")
	case errors.Is(err, storage.ErrBoardpaarBoardFehlt):
		writeError(w, http.StatusBadRequest, "board_id required")
	case errors.Is(err, storage.ErrBoardpaarNichtFreigebbar):
		writeError(w, http.StatusBadRequest, "the service board cannot be paired")
	case errors.Is(err, storage.ErrBoardpaarLinkBasis):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, storage.ErrBoardpaarIstKopie):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "pairing failed", err)
	}
}

func leseBoardpaarBody(w http.ResponseWriter, r *http.Request, ziel any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	if err := json.NewDecoder(r.Body).Decode(ziel); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

func (h *boardpaarAdmin) innenKoppeln(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BoardID string `json:"board_id"`
	}
	if !leseBoardpaarBody(w, r, &body) {
		return
	}
	if err := h.repo.InnenKoppeln(r.Context(), body.BoardID, adminUserIDFromContext(r.Context())); err != nil {
		koppelFehler(w, err)
		return
	}
	log.Printf("boardpaar: board %s innen gekoppelt (Admin)", body.BoardID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *boardpaarAdmin) innenEntkoppeln(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("board")
	err := h.repo.InnenEntkoppeln(r.Context(), id)
	if errors.Is(err, storage.ErrBoardpaarNichtGekoppelt) {
		writeError(w, http.StatusNotFound, "board is not paired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unpairing failed", err)
		return
	}
	log.Printf("boardpaar: board %s innen entkoppelt (Admin)", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *boardpaarAdmin) aussenKoppeln(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BoardID        string `json:"board_id"`
		InnenLinkBasis string `json:"innen_link_basis"`
	}
	if !leseBoardpaarBody(w, r, &body) {
		return
	}
	vorher, _ := h.repo.AussenGekoppelt(r.Context(), body.BoardID)
	e, err := h.repo.AussenKoppeln(r.Context(), body.BoardID, body.InnenLinkBasis, adminUserIDFromContext(r.Context()))
	if err != nil {
		koppelFehler(w, err)
		return
	}
	if vorher != nil {
		log.Printf("boardpaar: board %s außen — adresse des internen boards geändert (Admin)", body.BoardID)
	} else {
		log.Printf("boardpaar: board %s außen gekoppelt (Admin)", body.BoardID)
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *boardpaarAdmin) aussenEntkoppeln(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("board")
	err := h.repo.AussenEntkoppeln(r.Context(), id)
	if errors.Is(err, storage.ErrBoardpaarNichtGekoppelt) {
		writeError(w, http.StatusNotFound, "board is not paired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unpairing failed", err)
		return
	}
	log.Printf("boardpaar: board %s außen entkoppelt (Admin)", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// kopieLoeschen — Notweg „Kopie außen löschen“ (ADR-0141 Nachtrag #831,
// F-2232). Nur Server-Admins (protectMutate), nur auf der Seite außen, nur
// für Kopien (gekoppelt mit kopie = 1 oder außen entkoppelt mit Herkunft,
// Migration 0042) — ohne die Board-Rolle owner. Kein allgemeines
// Admin-Löschen: Stufe-1-Boards, Boards ohne Herkunft als Kopie (auch mit
// selbst gesetzten Spiegel-Marken) und das Service-Board → 409.
func (h *boardpaarAdmin) kopieLoeschen(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("board")
	seite, err := BoardpaarSeiteMitFehler(ctx, h.a.cfg)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "config unreadable", err)
		return
	}
	if seite != BoardpaarSeiteAussen {
		writeError(w, http.StatusConflict, "externe Kopien löscht nur die Seite außen (boardpaar.seite = aussen)")
		return
	}
	g, err := h.repo.KopieLoeschen(ctx, id)
	switch {
	case errors.Is(err, storage.ErrBoardpaarBoardFehlt):
		writeError(w, http.StatusBadRequest, "board id required")
		return
	case errors.Is(err, storage.ErrBoardpaarBoardUnbekannt):
		writeError(w, http.StatusNotFound, "board not found")
		return
	case errors.Is(err, storage.ErrBoardpaarKeineKopie):
		writeError(w, http.StatusConflict, "das Board ist keine externe Kopie — der Notweg löscht nur Kopien")
		return
	case errors.Is(err, storage.ErrDBBelegt):
		writeError(w, http.StatusServiceUnavailable, "database busy, try again", err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "delete failed", err)
		return
	}
	if h.a.boardGeloescht != nil {
		h.a.boardGeloescht(ctx, id)
	}
	art := "gekoppelt"
	if g.Entkoppelt {
		art = "außen entkoppelt"
	}
	log.Printf("boardpaar: kopie %s (%s) außen gelöscht, %d offene Änderungen verworfen (Admin %s)", id, art, g.OffeneAenderungen, adminUserIDFromContext(ctx))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "board_id": g.BoardID, "entkoppelt": g.Entkoppelt,
		"offene_aenderungen": g.OffeneAenderungen})
}

// kopieBearbeitbar — Schalter „bearbeitbar“ einer Kopie (ADR-0141 Nachtrag
// #836, Punkt 3: Notweg im Admin-Reiter). Nur Server-Admins (protectMutate:
// Admin-Prüfung, Rate-Limit, CSRF zentral), nur auf der Seite außen, nur für
// gekoppelte Kopien (kopie = 1). Body {"bearbeitbar": true|false}, Pflicht.
// 404 nicht gekoppelt, 409 keine Kopie oder nicht außen. Schickt das Skript
// das Feld am nächsten Hinweg, gewinnt sein Wert. Abschalten ändert keinen
// Bestand (Journal und wartende Spiegel bleiben). Die Rolle des
// Board-Paar-Teams auf dieser Kopie folgt in derselben Transaktion
// (Nachtrag Punkt 6: editor nur bei boardpaar.rueckweg = an UND bearbeitbar).
func (h *boardpaarAdmin) kopieBearbeitbar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("board")
	var body struct {
		Bearbeitbar *bool `json:"bearbeitbar"`
	}
	if !leseBoardpaarBody(w, r, &body) {
		return
	}
	if body.Bearbeitbar == nil {
		writeError(w, http.StatusBadRequest, "bearbeitbar: true oder false ist Pflicht")
		return
	}
	seite, err := BoardpaarSeiteMitFehler(ctx, h.a.cfg)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "config unreadable", err)
		return
	}
	if seite != BoardpaarSeiteAussen {
		writeError(w, http.StatusConflict, "den Schalter „bearbeitbar“ gibt es nur auf der Seite außen (boardpaar.seite = aussen)")
		return
	}
	err = h.repo.KopieBearbeitbarSetzen(ctx, id, *body.Bearbeitbar)
	switch {
	case errors.Is(err, storage.ErrBoardpaarNichtGekoppelt):
		writeError(w, http.StatusNotFound, "board is not paired")
		return
	case errors.Is(err, storage.ErrBoardpaarKeineKopie):
		writeError(w, http.StatusConflict, "das Board ist keine externe Kopie — „bearbeitbar“ gibt es nur für Kopien")
		return
	case errors.Is(err, storage.ErrDBBelegt):
		writeError(w, http.StatusServiceUnavailable, "database busy, try again", err)
		return
	case errors.Is(err, storage.ErrBoardpaarTeamUnlesbar):
		// #836 Punkt 6: ohne lesbares Team keine Rollen-Angleichung — nichts geschrieben.
		writeError(w, http.StatusServiceUnavailable, "config unreadable", err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "update failed", err)
		return
	}
	// F-2299: offene Browser sofort informieren (sie holen den Schatten neu und
	// zeichnen „nur lesen“ bzw. bearbeitbar), statt auf das Nachholen zu warten.
	if h.a.broker != nil {
		if daten, err := json.Marshal(schattenEreignisDaten{BoardID: id}); err == nil {
			h.a.broker.Publish(id, Event{Type: SchattenEreignis, Data: string(daten)})
		}
	}
	log.Printf("boardpaar: kopie %s bearbeitbar=%v (Admin %s)", id, *body.Bearbeitbar, adminUserIDFromContext(ctx))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "board_id": id, "bearbeitbar": *body.Bearbeitbar})
}

func (h *boardpaarAdmin) zugangAnlegen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Typ         string `json:"typ"`
		Name        string `json:"name"`
		DarfAnlegen bool   `json:"darf_anlegen"`
	}
	if !leseBoardpaarBody(w, r, &body) {
		return
	}
	klartext, t, err := h.repo.TokenAnlegenMit(r.Context(), body.Typ, body.Name, adminUserIDFromContext(r.Context()), body.DarfAnlegen)
	switch {
	case errors.Is(err, storage.ErrBoardpaarTyp):
		writeError(w, http.StatusBadRequest, "typ: abs, abe, abr or abb")
		return
	case errors.Is(err, storage.ErrBoardpaarAnlegeRecht):
		writeError(w, http.StatusBadRequest, "darf_anlegen: only for typ abe")
		return
	case errors.Is(err, storage.ErrBoardpaarTokenName):
		writeError(w, http.StatusBadRequest, "name: 1–64 characters, letters, digits, . _ -")
		return
	case errors.Is(err, storage.ErrBoardpaarTokenVergeben):
		writeError(w, http.StatusConflict, "an active token of this type already uses this name")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "create failed", err)
		return
	}
	log.Printf("boardpaar: zugang %s %q angelegt (Admin)", t.Typ, t.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":   klartext,
		"zugang":  t,
		"hinweis": "Das Token wird nur jetzt angezeigt.",
	})
}

func (h *boardpaarAdmin) zugangWiderrufen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.repo.TokenWiderrufen(r.Context(), id)
	if errors.Is(err, storage.ErrBoardpaarTokenUnbekannt) {
		writeError(w, http.StatusNotFound, "no active token with this id")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	log.Printf("boardpaar: zugang %s widerrufen (Admin)", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// zugangZeileEntfernen — DELETE /admin/boardpaar/zugaenge/{id}/zeile (#848).
// Nur abgeschlossene Zugänge (widerrufen, oder abgelöst nach der
// Übergangszeit); ein geltender bleibt — 409, er wird widerrufen, nicht
// entfernt. Eigener Pfad, damit DELETE …/{id} weiter „widerrufen“ heisst.
func (h *boardpaarAdmin) zugangZeileEntfernen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	z, err := h.repo.ZugangZeileEntfernen(r.Context(), id)
	switch {
	case errors.Is(err, storage.ErrBoardpaarTokenUnbekannt):
		writeError(w, http.StatusNotFound, "no access with this id")
		return
	case errors.Is(err, storage.ErrBoardpaarZugangGiltNoch):
		writeError(w, http.StatusConflict, "access still valid; revoke it first")
		return
	case errors.Is(err, storage.ErrBoardpaarUhr):
		writeError(w, http.StatusServiceUnavailable, "system clock not synchronised; try again later")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "remove failed", err)
		return
	}
	h.zeileAudit(r, *z)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// zugaengeAufraeumen — POST /admin/boardpaar/zugaenge/aufraeumen (#848).
// Entfernt jeden abgeschlossenen Zugang einzeln mit derselben Prüfung; was
// noch gilt oder bei ungestellter Uhr nicht beurteilbar ist, bleibt und wird
// als `uebersprungen` gezählt.
func (h *boardpaarAdmin) zugaengeAufraeumen(w http.ResponseWriter, r *http.Request) {
	weg, uebersprungen, err := h.repo.ZugaengeAufraeumen(r.Context())
	for _, z := range weg {
		h.zeileAudit(r, z)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cleanup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entfernt": len(weg), "uebersprungen": uebersprungen})
}

func (h *boardpaarAdmin) zeileAudit(r *http.Request, z storage.BoardpaarZeile) {
	uid := adminUserIDFromContext(r.Context())
	log.Printf("boardpaar: zugang %s (%s_ %q) aus der tabelle entfernt (Admin)", z.ID, z.Typ, z.Name)
	if err := h.a.cfg.AppendAudit(r.Context(), "boardpaar/zugaenge/"+z.ID, uid, storage.AuditActionBoardpaarZugangEntfernen,
		z.Typ+"_ "+z.Name, ""); err != nil {
		log.Printf("boardpaar: audit für entfernten zugang %s nicht geschrieben: %v", z.ID, err)
	}
}
