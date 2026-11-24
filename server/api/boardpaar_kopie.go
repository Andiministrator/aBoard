package api

// boardpaar_kopie.go — Board-Paar Stufe 2a: externe Kopie, Hinweg v2
// (ADR-0141 §3, §4, §10; Karte #825).
//
// Wege des Skripts (nur am Agenten-Eingang, BoardpaarSchranke):
//
//	innen  GET  /agent/boardpaar/auftraege                     abs_  offene Aufträge „Kopie anlegen“
//	innen  GET  /agent/boardpaar/spiegel/{innenBoardId}        abs_  Hinweg holen (Struktur, Schatten, Spiegel)
//	innen  PUT  /agent/boardpaar/ziele                         abr_  Namen der Ziele melden
//	innen  POST /agent/boardpaar/auftraege/{auftragId}/erledigt abr_ Auftrag erledigt
//	außen  POST /agent/boardpaar/boards                        abe_ mit darf_anlegen  Kopie anlegen
//	außen  PUT  /agent/boardpaar/spiegel/{aussenBoardId}       abe_  Hinweg schieben
//	außen  GET/HEAD /agent/boardpaar/aenderungen/{aussenBoardId} abe_  Journal holen (2c, ≤ 200, weitere)
//	außen  POST /agent/boardpaar/aenderungen/{aussenBoardId}/bestaetigt abe_ Journal bestätigen (2c)
//	innen  POST /agent/boardpaar/anwenden/{innenBoardId}       abr_  Rückweg-Eintrag anwenden (2c)
//
// Jeder Weg gilt nur auf seiner Seite (boardpaar.seite, je Anfrage gelesen);
// auf der falschen Seite oder bei „aus“ antwortet er 404, VOR der
// Token-Prüfung. Wege mit Board-ID antworten 404, wenn das Board nicht
// gekoppelt ist oder außen keine Kopie (kopie = 0) ist (W2-5).
//
// Der innere Server bekommt keinen Ausgang: Er legt nichts außen an, er
// liefert nur, was das Skript abholt (ADR-0141 §3, „außen ruft nie nach
// innen“ bleibt).

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

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Pfade im Mux (am Agenten-Eingang nach dem Abschneiden von /agent).
const (
	BoardpaarAuftraegePfad = "/boardpaar/auftraege"
	BoardpaarZielePfad     = "/boardpaar/ziele"
	BoardpaarBoardsPfad    = "/boardpaar/boards"
	BoardpaarSpiegelPfad   = "/boardpaar/spiegel/"
	// BoardpaarAenderungenPfad: Journal außen (2c, ADR-0141 §5.2).
	BoardpaarAenderungenPfad = "/boardpaar/aenderungen/"
)

// KonfigBoardpaarTeam: Einstellung außen — die ACL-Subjekte (E-Mail oder
// „@domain“), die jede neue Kopie bekommen — als Leser (viewer), bei
// boardpaar.rueckweg = an als Bearbeiter (editor) (ADR-0141 §3.4, W-C).
// Es gibt am Server keine Gruppen; die „Gruppe“ ist diese Liste, eine
// Domain-Freigabe (ADR-0132) wirkt wie eine Gruppe.
const KonfigBoardpaarTeam = storage.KonfigBoardpaarTeamSchluessel

// PruefeBoardpaarTeam: Prüfung beim Speichern (WithKonfigPruefung).
func PruefeBoardpaarTeam(_ string, value string) (string, error) {
	norm, _, err := storage.TeamAusText(value)
	return norm, err
}

// einSegment: rest ist genau ein Pfad-Segment ohne Sonderfälle.
func einSegment(rest string) bool {
	return rest != "" && !strings.Contains(rest, "/") && rest != "." && rest != ".."
}

// istBoardpaarWegFuer: Ist die Anfrage ein Weg, den ein Token des Typs typ
// erreichen darf? Nur Methode und Pfad (wie der Mux ihn sieht), nicht die Seite.
func istBoardpaarWegFuer(typ string, r *http.Request) bool {
	if istBoardpaarErneuernWeg(r) {
		return true
	}
	if r.URL.RawPath != "" {
		return false
	}
	p, m := r.URL.Path, r.Method
	lesend := m == http.MethodGet || m == http.MethodHead
	spiegelRest, istSpiegel := strings.CutPrefix(p, BoardpaarSpiegelPfad)
	istSpiegel = istSpiegel && einSegment(spiegelRest)
	switch typ {
	case storage.BoardpaarTypAusgang:
		return istSchattenAusgangWeg(r) || lesend && (p == BoardpaarAuftraegePfad || istSpiegel)
	case storage.BoardpaarTypEingang:
		if rest, ok := strings.CutPrefix(p, BoardpaarAenderungenPfad); ok {
			if lesend && einSegment(rest) {
				return true
			}
			id, ende, ok := strings.Cut(rest, "/")
			return ok && m == http.MethodPost && einSegment(id) && ende == "bestaetigt"
		}
		return istSchattenEingangWeg(r) || m == http.MethodPost && p == BoardpaarBoardsPfad ||
			m == http.MethodPut && istSpiegel
	case storage.BoardpaarTypSicherungen:
		// ADR-0142 §2: nur lesend, nur die Liste und ein Archiv.
		return lesend && istSicherungenPfad(p)
	case storage.BoardpaarTypRueckweg:
		if m == http.MethodPut && p == BoardpaarZielePfad {
			return true
		}
		if rest, ok := strings.CutPrefix(p, BoardpaarAnwendenPfad); ok && m == http.MethodPost {
			return einSegment(rest)
		}
		if rest, ok := strings.CutPrefix(p, BoardpaarAuftraegePfad+"/"); ok && m == http.MethodPost {
			id, ende, ok := strings.Cut(rest, "/")
			return ok && einSegment(id) && ende == "erledigt"
		}
	}
	return false
}

// BoardpaarKopieAPI bündelt die Wege aus ADR-0141 Schritt 2a.
type BoardpaarKopieAPI struct {
	boards   *storage.BoardRepo
	cfg      *storage.ConfigRepo
	paar     *storage.BoardpaarRepo
	schatten *storage.SchattenRepo
	broker   *EventBroker
	// limit: Fehlversuche je IP (eigenes Kontingent).
	limit   *rateLimiter
	adresse func(*http.Request) string
	// laeuft: je Board höchstens ein Eingang gleichzeitig (wie Stufe 1, P-14).
	laeuft sync.Map
	// ops: Ops-Weg für den Rückweg innen (MitRueckweg, Karte #827).
	ops *BoardsAPI
}

func NewBoardpaarKopieAPI(boards *storage.BoardRepo, cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo,
	sr *storage.SchattenRepo, broker *EventBroker) *BoardpaarKopieAPI {
	return &BoardpaarKopieAPI{boards: boards, cfg: cfg, paar: paar, schatten: sr, broker: broker,
		limit: newRateLimiter(120, time.Hour), adresse: tcpAdresse}
}

// MitAdressregel wie beim Ausgang (F-2030).
func (a *BoardpaarKopieAPI) MitAdressregel(fn func(*http.Request) string) *BoardpaarKopieAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

func (a *BoardpaarKopieAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET "+BoardpaarAuftraegePfad, WithDefaults(http.HandlerFunc(a.auftraege)))
	mux.Handle("POST "+BoardpaarAuftraegePfad+"/{id}/erledigt", WithDefaults(http.HandlerFunc(a.erledigt)))
	mux.Handle("PUT "+BoardpaarZielePfad, WithDefaults(http.HandlerFunc(a.ziele)))
	mux.Handle("GET "+BoardpaarSpiegelPfad+"{boardId}", WithDefaults(http.HandlerFunc(a.hinweg)))
	mux.Handle("POST "+BoardpaarBoardsPfad, WithDefaults(http.HandlerFunc(a.kopieAnlegen)))
	// Ohne WithDefaults: dessen Body-Grenze (10 MiB) läge unter der des
	// Eingangs; der Handler setzt seine eigene (wie Stufe 1).
	mux.Handle("PUT "+BoardpaarSpiegelPfad+"{boardId}", WithSecurityHeaders(http.HandlerFunc(a.eingang)))
	// GET deckt im Mux auch HEAD ab (HEAD = nur lesen, setzt abgeholt_at nicht).
	mux.Handle("GET "+BoardpaarAenderungenPfad+"{boardId}", WithDefaults(http.HandlerFunc(a.aenderungen)))
	mux.Handle("POST "+BoardpaarAenderungenPfad+"{boardId}/bestaetigt", WithDefaults(http.HandlerFunc(a.bestaetigt)))
	// Ohne WithDefaults: Der Eintrag darf so groß sein wie ein Ops-Batch
	// (MaxOpsBodyBytes setzt der Handler selbst).
	mux.Handle("POST "+BoardpaarAnwendenPfad+"{boardId}", WithSecurityHeaders(http.HandlerFunc(a.anwenden)))
}

// seite prüft die Seite VOR dem Token (404 auf der falschen Seite).
func (a *BoardpaarKopieAPI) seite(w http.ResponseWriter, ctx context.Context, soll string) bool {
	if a.paar == nil || BoardpaarSeite(ctx, a.cfg) != soll {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

func (a *BoardpaarKopieAPI) zugang(w http.ResponseWriter, r *http.Request, typ, weg string) *storage.BoardpaarToken {
	return boardpaarZugang(w, r, a.paar, a.limit, a.adresse, typ, weg)
}

// --- innen ---------------------------------------------------------------------

type auftragFuerSkript struct {
	ID             string `json:"id"`
	BoardID        string `json:"board_id"`
	Ziel           string `json:"ziel"`
	Titel          string `json:"titel"`
	InnenLinkBasis string `json:"innen_link_basis"`
}

// auftraege — GET /boardpaar/auftraege (innen, abs_).
func (a *BoardpaarKopieAPI) auftraege(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-auftraege"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteInnen) {
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypAusgang, weg)
	if z == nil {
		return
	}
	liste, err := a.paar.AuftraegeOffen(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	out := make([]auftragFuerSkript, 0, len(liste))
	for _, e := range liste {
		// Kritik Sprint 93 K-7: Titel in der Form, die außen angenommen wird.
		out = append(out, auftragFuerSkript{ID: e.ID, BoardID: e.BoardID, Ziel: e.Ziel, Titel: storage.KopieTitelNormalform(e.Titel),
			InnenLinkBasis: e.InnenLinkBasis})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"auftraege": out})
}

// erledigt — POST /boardpaar/auftraege/{id}/erledigt (innen, abr_). Die
// Meldung enthält nichts außer dem Status; ein Body wird nicht gelesen.
func (a *BoardpaarKopieAPI) erledigt(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-auftrag-erledigt"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteInnen) {
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypRueckweg, weg)
	if z == nil {
		return
	}
	e, err := a.paar.AuftragErledigt(ctx, r.PathValue("id"))
	if errors.Is(err, storage.ErrBoardpaarAuftragUnbekannt) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed", err)
		return
	}
	log.Printf("%s: zugang=%q auftrag=%s board=%s ziel=%q → erledigt", weg, z.Name, e.ID, e.BoardID, e.Ziel)
	writeJSON(w, http.StatusOK, map[string]any{"id": e.ID, "status": e.Status})
}

// ziele — PUT /boardpaar/ziele (innen, abr_). Body {"ziele": ["name", …]}.
func (a *BoardpaarKopieAPI) ziele(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-ziele"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteInnen) {
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypRueckweg, weg)
	if z == nil {
		return
	}
	var body struct {
		Ziele []string `json:"ziele"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Ziele == nil {
		writeError(w, http.StatusUnprocessableEntity, `body: erwartet {"ziele": ["name", …]}`)
		return
	}
	liste, err := a.paar.ZieleSetzen(ctx, body.Ziele)
	switch {
	case errors.Is(err, storage.ErrBoardpaarZielName):
		writeError(w, http.StatusUnprocessableEntity, "ziele: name 1–64 Zeichen aus Buchstaben, Ziffern, . _ -")
		return
	case errors.Is(err, storage.ErrBoardpaarZuVieleZiele):
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("ziele: höchstens %d", storage.BoardpaarZieleMax))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "update failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ziele": liste})
}

// hinwegAntwort: GET /boardpaar/spiegel/{board}. format ist die Fassung des
// Hinwegs (ADR-0141 §4: „version: 2“): 2 = Schritt 2a (alles als Schatten),
// 3 = Schritt 2b (Spiegel für nicht vertrauliche Karten und Termine, Titel
// des Boards in struktur; Karte #826). Das Skript fordert 3 mit ?format=3 an;
// ohne den Parameter bleibt es bei 2 — so läuft ein Skript aus 2a weiter,
// wenn innen schon 2b eingespielt ist (Umstieg ohne Gleichzeitigkeit).
// version ist wie in Stufe 1 die Version dieses Servers — das Skript reicht
// sie beim PUT außen weiter, der Eingang vergleicht die Hauptversion.
type hinwegAntwort struct {
	Format   int                  `json:"format"`
	Version  string               `json:"version"`
	Stand    string               `json:"stand"`
	Struktur schatten.Struktur    `json:"struktur"`
	Schatten []schatten.EintragV2 `json:"schatten"`
	Spiegel  []any                `json:"spiegel"`
	// Faehigkeiten: was dieser Server auf dem Rückweg kann (Plan D11) —
	// „anwenden“ = POST /boardpaar/anwenden.
	Faehigkeiten []string `json:"faehigkeiten"`
}

// hinwegFormat liest ?format: fehlt er oder ist er 2 → 2, 3 → 3, sonst 0
// (ungültig, 400).
func hinwegFormat(r *http.Request) int {
	switch r.URL.Query().Get("format") {
	case "", "2":
		return 2
	case "3":
		return 3
	}
	return 0
}

// hinweg — GET /boardpaar/spiegel/{boardId} (innen, abs_).
func (a *BoardpaarKopieAPI) hinweg(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-hinweg"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteInnen) {
		return
	}
	if a.boards == nil {
		writeError(w, http.StatusInternalServerError, "not wired")
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypAusgang, weg)
	if z == nil {
		return
	}
	boardID := r.PathValue("boardId")
	format := hinwegFormat(r)
	if format == 0 {
		writeError(w, http.StatusBadRequest, "format: erlaubt sind 2 und 3")
		return
	}
	gekoppelt, err := a.paar.InnenGekoppelt(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if !gekoppelt {
		log.Printf("%s: zugang=%q board=%q → 404 (nicht gekoppelt oder unbekannt)", weg, z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Kritik Sprint 93 S-1: Der Hinweg v2 trägt Titel vertraulicher Karten
	// (ADR-0141 Beschluss 3). Das gilt nur für Boards, für die ein Admin innen
	// eine Kopie beauftragt hat — für reine Stufe-1-Boards bleibt ADR-0140
	// („den Titel kennt nur der interne Server“).
	beauftragt, err := a.paar.HatAuftrag(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if !beauftragt {
		log.Printf("%s: zugang=%q board=%s → 404 (kein Auftrag „Externe Kopie“)", weg, z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	merke := func(grund string) {
		merkeStand(ctx, weg, boardID, func(mctx context.Context) error {
			return a.paar.InnenAbfrageMerken(mctx, boardID, grund)
		})
	}
	b, err := a.boards.Get(ctx, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		merke("Board nicht lesbar")
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	erg, err := schatten.BaueV2Mit(b.StateJSON, format == 3)
	if err != nil {
		log.Printf("%s: zugang=%q board=%s → 500 (%v)", weg, z.Name, boardID, err)
		merke(fmt.Sprintf("Board-Zustand nicht auswertbar: %v", err))
		writeError(w, http.StatusInternalServerError, "board state unreadable")
		return
	}
	if n := len(erg.Schatten) + len(erg.Spiegel); n > schatten.EingangMaxSchatten {
		// Ganz oder Fehler (wie Stufe 1): eine gekappte Liste ließe außen
		// Schatten verschwinden. Schatten und Spiegel zählen zusammen.
		merke(fmt.Sprintf("zu viele Einträge: %d, Grenze %d — erledigte Karten archivieren",
			n, schatten.EingangMaxSchatten))
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("too many shadows: %d, limit %d — archive done cards",
			n, schatten.EingangMaxSchatten))
		return
	}
	if format == 3 {
		// F-2140 (Karte #826): Der Titel des Boards geht mit; außen heißt die
		// Kopie danach so wie innen. Dieselbe Form wie im Auftrag.
		titel := storage.KopieTitelNormalform(b.Title)
		erg.Struktur.Titel = &titel
	}
	log.Printf("%s: zugang=%q board=%s format=%d lanes=%d spalten=%d schatten=%d spiegel=%d ohne-ort=%d nicht-baubar=%d ohne-kennung=%d → 200",
		weg, z.Name, boardID, format, len(erg.Struktur.Lanes), len(erg.Struktur.Spalten), len(erg.Schatten), len(erg.Spiegel),
		erg.OhneOrt, len(erg.NichtBaubar), erg.OhneKennung)
	// Kritik #825 A-1: Karten ohne Ort (Spalte nicht in columnDefs, Lane mit
	// ungültiger ID oder Art) gehen nicht hinaus — der Admin sieht es am Stand.
	if erg.OhneOrt > 0 {
		merke(fmt.Sprintf("%d Karte(n) gingen nicht hinaus: %d in einer Spalte, die es nicht gibt; %d in einer Lane mit ungültiger ID oder Art",
			erg.OhneOrt, erg.OhneSpalte, erg.OhneLane))
	} else {
		merke("")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, hinwegAntwort{Format: format, Version: BuildVersion,
		Stand: time.Now().UTC().Format(time.RFC3339), Struktur: erg.Struktur, Schatten: erg.Schatten, Spiegel: erg.Spiegel,
		Faehigkeiten: []string{"anwenden"}})
}

// --- außen ---------------------------------------------------------------------

// kopieAnlegen — POST /boardpaar/boards (außen, abe_ mit darf_anlegen).
// Body {"id", "titel", "innen_link_basis", "bearbeitbar"?}. 201 neu angelegt,
// 200 gab es schon (wiederholbar, VF-7), 403 ohne Recht, 409 ID belegt (kein
// Kopie-Board). bearbeitbar (ADR-0141 Nachtrag #836) ist optional: fehlt es
// oder ist es null, bekommt eine neue Kopie 0 und eine bestehende behält
// ihren Wert; ein anderer Typ als bool ist ein Formfehler (422 wie die
// übrigen Formfehler dieses Wegs).
func (a *BoardpaarKopieAPI) kopieAnlegen(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-kopie-anlegen"
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteAussen) {
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypEingang, weg)
	if z == nil {
		return
	}
	if !z.DarfAnlegen {
		log.Printf("%s: zugang=%q ohne recht „darf kopien anlegen“ → 403", weg, z.Name)
		writeError(w, http.StatusForbidden, "forbidden: this token may not create copies")
		return
	}
	var body struct {
		ID             string `json:"id"`
		Titel          string `json:"titel"`
		InnenLinkBasis string `json:"innen_link_basis"`
		Bearbeitbar    *bool  `json:"bearbeitbar"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusUnprocessableEntity, `body: erwartet {"id", "titel", "innen_link_basis", "bearbeitbar" (optional, true oder false)}`)
		return
	}
	// Das Team liest KopieAnlegenTeamAusKonfig unter der Schreibsperre
	// (Sitzungs-QA #835 K-3) — wie den Rückweg-Schalter.
	erg, err := a.paar.KopieAnlegenTeamAusKonfigMit(ctx, body.ID, body.Titel, body.InnenLinkBasis, "skript:"+z.Name, body.Bearbeitbar)
	switch {
	case errors.Is(err, storage.ErrBoardpaarKopieID):
		writeError(w, http.StatusUnprocessableEntity, "id: erwartet eine UUID in Kleinbuchstaben")
		return
	case errors.Is(err, storage.ErrBoardpaarKopieTitel):
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("titel: 1–%d Zeichen, ohne Steuerzeichen", storage.KopieTitelMax))
		return
	case errors.Is(err, storage.ErrBoardpaarLinkBasis):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case errors.Is(err, storage.ErrBoardpaarKopieBelegt):
		log.Printf("%s: zugang=%q board=%s → 409 (id belegt, keine kopie)", weg, z.Name, body.ID)
		writeError(w, http.StatusConflict, "an existing board that is not a copy uses this id")
		return
	case errors.Is(err, storage.ErrDBBelegt):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "server busy")
		return
	case errors.Is(err, storage.ErrBoardpaarTeamUnlesbar):
		writeError(w, http.StatusServiceUnavailable, "config unreadable", err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "create failed", err)
		return
	}
	// Kritik #825 A-4: Das Skript bekommt nur die Zahl der Mitglieder, keine
	// Adressen (es braucht sie nicht und könnte sie loggen).
	antwort := map[string]any{"id": erg.BoardID, "titel": erg.Titel, "innen_link_basis": erg.InnenLinkBasis,
		"wiederholt": erg.Wiederholt, "wiederaufgenommen": erg.Wiederaufgenommen, "mitglieder": len(erg.Mitglieder),
		"bearbeitbar": erg.Bearbeitbar}
	if erg.Wiederholt {
		log.Printf("%s: zugang=%q board=%s → 200 (gab es schon)", weg, z.Name, erg.BoardID)
		writeJSON(w, http.StatusOK, antwort)
		return
	}
	if erg.Wiederaufgenommen {
		// Kritik Sprint 93 K-3/R-1: außen entkoppelte Kopie, Kopplung neu.
		log.Printf("%s: zugang=%q board=%s → 201 (wieder aufgenommen)", weg, z.Name, erg.BoardID)
		writeJSON(w, http.StatusCreated, antwort)
		return
	}
	log.Printf("%s: zugang=%q board=%s mitglieder=%d → 201", weg, z.Name, erg.BoardID, len(erg.Mitglieder))
	if b, gerr := a.boards.Get(ctx, erg.BoardID); gerr == nil {
		publishBoardUpdated(a.broker, b.ID, b.UpdatedAt, "create")
	}
	writeJSON(w, http.StatusCreated, antwort)
}

// teamNachsichtig liest boardpaar.team Eintrag für Eintrag: Ein ungültiger
// Eintrag (gespeichert vor einer Regeländerung) fällt mit einer Log-Zeile
// weg, die übrigen bleiben (Kritik Sprint 93 K-6 — vorher fiel das ganze
// Team still weg).
func teamNachsichtig(roh string) []string { return storage.TeamNachsichtig(roh) }

// eingang — PUT /boardpaar/spiegel/{boardId} (außen, abe_; nur kopie = 1).
func (a *BoardpaarKopieAPI) eingang(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-eingang"
	ende := time.Now().Add(schattenEingangFrist)
	ctx, cancel := context.WithDeadline(r.Context(), ende)
	defer cancel()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(ende)
	_ = rc.SetWriteDeadline(ende.Add(10 * time.Second))
	if !a.seite(w, ctx, BoardpaarSeiteAussen) || a.schatten == nil {
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypEingang, weg)
	if z == nil {
		return
	}
	boardID := r.PathValue("boardId")
	kopplung, err := a.paar.AussenGekoppelt(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if kopplung == nil || !kopplung.Kopie {
		log.Printf("%s: zugang=%q board=%q → 404 (nicht gekoppelt, unbekannt oder keine kopie)", weg, z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, belegt := a.laeuft.LoadOrStore(boardID, struct{}{}); belegt {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "a sync for this board is already running")
		return
	}
	defer a.laeuft.Delete(boardID)
	merke := func(grund string) {
		merkeStand(ctx, weg, boardID, func(mctx context.Context) error {
			return a.paar.AussenEingangMerken(mctx, boardID, grund)
		})
	}
	body, err := readAllBegrenzt(w, r, schatten.EingangMaxBytes)
	if err != nil {
		var zuGross *http.MaxBytesError
		if errors.As(err, &zuGross) {
			merke(fmt.Sprintf("413: Hinweg über %d Byte", schatten.EingangMaxBytes))
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		merke("400: Hinweg nicht lesbar")
		writeError(w, http.StatusBadRequest, "body unreadable")
		return
	}
	kopie, err := schatten.LiesEingangV2(body, BuildVersion)
	if err != nil {
		var f *schatten.EingangFehler
		if !errors.As(err, &f) {
			merke("500: Prüfung fehlgeschlagen")
			writeError(w, http.StatusInternalServerError, "check failed", err)
			return
		}
		log.Printf("%s: zugang=%q board=%s → %d (%s), nichts geschrieben", weg, z.Name, boardID, f.Status, f.Merkertext())
		merke(fmt.Sprintf("%d: %s", f.Status, f.Merkertext()))
		if len(f.Fehler) > 0 {
			antwort := map[string]any{"error": f.Antworttext(), "fehler": f.Fehler}
			if f.Weitere > 0 {
				antwort["weitere"] = f.Weitere
			}
			writeJSON(w, f.Status, antwort)
			return
		}
		writeError(w, f.Status, f.Antworttext())
		return
	}
	// F-2301 (#836): Den Schalter „bearbeitbar“ setzt nur der Zugang, der
	// Kopien anlegen darf (ADR-0141 Nachtrag Punkt 3). Ein anderes abe_ mit
	// dem Feld bekommt 403, und am Board ändert sich NICHTS — auch der übrige
	// Hinweg nicht, damit das Skript den Fehler sieht, statt dass er halb
	// wirkt. Nur der Eingangs-Merker (merke) hält den Fehlgrund fest, wie bei
	// den anderen Ablehnungen dieses Wegs. Ohne Feld (oder mit null) bleibt der Hinweg für jedes abe_ offen.
	if kopie.Bearbeitbar != nil && !z.DarfAnlegen {
		log.Printf("%s: zugang=%q board=%s → 403 (bearbeitbar ohne recht „darf kopien anlegen“), nichts geschrieben", weg, z.Name, boardID)
		merke("403: bearbeitbar gesendet, Zugang ohne „darf Kopien anlegen“")
		writeError(w, http.StatusForbidden, "bearbeitbar: nur ein Zugang mit „darf Kopien anlegen“ darf den Schalter setzen — Feld weglassen oder den Anlege-Zugang nehmen; nichts geschrieben")
		return
	}
	var spiegelErg *schatten.SpiegelAbgleich
	// Ab 2c mischt der Eingang in den Bestand und schützt unbestätigte
	// Stellen des Journals außen (ADR-0141 §4, Karte #827 E2).
	// #836: kopie.Bearbeitbar (nil = nicht gesendet) setzt der Schreibweg in
	// derselben Transaktion — ein abgelehnter Hinweg ändert ihn nicht.
	erg, err := a.schatten.ErsetzeKopieFreigabe(ctx, boardID, kopie.Bearbeitbar, func(state []byte, schutz *schatten.Schutz) ([]byte, []string, error) {
		schutz.RueckwegBis = kopie.RueckwegBis
		neu, sp, err := schatten.KopieAnwendenMit(state, kopie, schutz)
		if err != nil {
			return nil, nil, err
		}
		if len(neu) > MaxBodyBytes {
			// Sonst entstünde ein Board, das kein Browser mehr per PUT
			// zurückschreiben kann (wie errBoardZuGross bei den Ops).
			return nil, nil, fmt.Errorf("%w: Kopie hätte %d Byte, Grenze %d", errKopieZuGross, len(neu), MaxBodyBytes)
		}
		spiegelErg = sp
		return neu, sp.WartendErledigt, nil
	}, kopie.Schatten)
	if err != nil {
		status, text := ersetzeFehler(err)
		if errors.Is(err, errKopieZuGross) {
			status, text = http.StatusInternalServerError, err.Error()
		}
		if errors.Is(err, schatten.ErrStand) || errors.Is(err, storage.ErrHuelleVerletzt) {
			// Verify #825 V-3: der Grund (z. B. „ID außen doppelt“) gehört in den
			// Fehlgrund am Stand; er nennt nur Struktur, keinen Inhalt.
			status, text = http.StatusInternalServerError, "board state unreadable: "+err.Error()
		}
		log.Printf("%s: zugang=%q board=%s → %d (%s): %v", weg, z.Name, boardID, status, text, err)
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "5")
		}
		merke(fmt.Sprintf("%d: %s", status, text))
		writeError(w, status, text, err)
		return
	}
	if erg.StrukturGeaendert {
		publishBoardUpdated(a.broker, boardID, erg.UpdatedAt, "boardpaar")
	}
	// #836: Auch ein umgestellter Schalter „bearbeitbar“ geht als
	// schatten:aktualisiert hinaus — der Browser holt daraufhin
	// GET /boards/{id}/schatten neu und zeichnet „nur lesen“ bzw. bearbeitbar.
	if (erg.HatAenderung() || erg.BearbeitbarGeaendert) && a.broker != nil {
		if daten, err := json.Marshal(schattenEreignisDaten{BoardID: boardID, Anzahl: erg.Anzahl, LetzteAenderung: erg.LetzteAenderung}); err == nil {
			a.broker.Publish(boardID, Event{Type: SchattenEreignis, Data: string(daten)})
		}
	}
	if spiegelErg == nil {
		spiegelErg = &schatten.SpiegelAbgleich{}
	}
	if erg.BearbeitbarGeaendert {
		log.Printf("%s: zugang=%q board=%s bearbeitbar=%v (vom skript gesetzt)", weg, z.Name, boardID, erg.Bearbeitbar)
	}
	if erg.StrukturGeaendert || erg.HatAenderung() {
		log.Printf("%s: zugang=%q board=%s zustand=%v neu=%d geaendert=%d entfernt=%d anzahl=%d spiegel-neu=%d spiegel-geaendert=%d spiegel-entfernt=%d spiegel=%d → 200",
			weg, z.Name, boardID, erg.StrukturGeaendert, erg.Neu, erg.Geaendert, erg.Entfernt, erg.Anzahl,
			spiegelErg.Neu, spiegelErg.Geaendert, spiegelErg.Entfernt, spiegelErg.Anzahl)
	}
	merke("")
	// Antwort wie in 2a (neu/geaendert/entfernt/anzahl zählen die Schatten,
	// struktur_geaendert heißt „Board-Zustand geschrieben“), dazu die Spiegel.
	writeJSON(w, http.StatusOK, eingangAntwort{KopieAbgleich: erg, Spiegel: spiegelErg})
}

// eingangAntwort: Antwort des PUT /boardpaar/spiegel.
type eingangAntwort struct {
	*storage.KopieAbgleich
	Spiegel *schatten.SpiegelAbgleich `json:"spiegel"`
}

// errKopieZuGross: Die Kopie überschritte MaxBodyBytes; nichts geschrieben.
var errKopieZuGross = errors.New("kopie zu groß")
