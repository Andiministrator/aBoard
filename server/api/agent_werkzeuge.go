package api

// agent_werkzeuge.go — Werkzeug-Endpunkte für Open WebUI (ADR-0133 §5/§7,
// Karte #671).
//
// Open WebUI bindet aBoard als globalen Werkzeug-Server ein: Es holt
// `GET /openapi.json` (unter dem Agenten-Pfad also `/agent/openapi.json`) und
// bietet dem Modell jede Operation als Werkzeug an. Die Werkzeuge sind dünne
// Hüllen:
//
//   - Lesen (`boards`, `board_lesen`) liest das Board und gibt eine KOMPAKTE
//     Auswahl zurück — nicht den ganzen Blob, der den Kontext eines lokalen
//     Modells sprengte.
//   - Schreiben (`karte_anlegen`, `kommentieren`) baut Ops und schickt sie
//     durch postOps — derselbe Weg wie POST /boards/{id}/ops, mit derselben
//     Stempelung (Akteur, onBehalfOf), denselben Sperren für delegierende
//     Tokens, derselben Ratenbegrenzung je Person, demselben Protokoll und
//     derselben Ablehnung bei unbeabsichtigter Wirkung. Eine zweite
//     Schreiblogik gibt es nicht.
//
// Berechtigung: Die Wege stehen in werkzeugDarf (Positivliste, agent.go ruft
// sie auf). Das Board kommt beim delegierenden Token aus dem Pfad, die Rolle
// aus der ACL der Person (agent_owui.go). Projekt- und Personen-Tokens dürfen
// die Werkzeuge auf IHREM Board mit ihrer Rolle benutzen; ein Vorschlags-Token
// keines.
//
// Kartentext ist Fremdtext: Er geht nur als JSON-Wert hinaus, nie als
// Anweisung, und die Beschreibung der Werkzeuge sagt dem Modell das. Die
// lesenden Antworten tragen dazu `sicherheitshinweis`, die schreibenden
// Operationen in der Spezifikation den Satz „nur auf ausdrücklichen Wunsch der
// Person“ (Kritiker Sprint 82, K3-5). Ratenbegrenzung der lesenden und
// Wiederholungsschutz der anlegenden Werkzeuge: agent_werkzeuge_idem.go.
//
// Ein delegierendes Token (Open WebUI) erreicht NUR diese Wege, nicht die
// rohe Agenten-API unter /boards/{id}/… (Kritiker Sprint 82, K1-1, agentDarf).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/calendar"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// werkzeugSegment: erstes Pfadsegment aller Werkzeug-Wege.
	werkzeugSegment = "werkzeuge"
	// werkzeugSpezSegment: die Spezifikation (OpenAPI 3.1).
	werkzeugSpezSegment = "openapi.json"
	// werkzeugMCPSegment: der MCP-Eingang (Karte #753, ADR-0139 §3).
	werkzeugMCPSegment = "mcp"

	// werkzeugVerbotenMeldung: 403 auf einem Werkzeug-Weg. Für fehlende
	// Freigabe und nicht vorhandenes Board dieselbe Meldung — die Antwort
	// verrät nicht, welche Boards es gibt.
	werkzeugVerbotenMeldung = "nicht erlaubt: Das Board gibt es nicht, es ist für diese Person nicht freigegeben, " +
		"oder ihre Rolle reicht nicht (Karten anlegen und kommentieren braucht Schreibrecht). Mit boards prüfen, welche Boards sie sieht."

	// werkzeugMaxBody: Ein Werkzeug-Aufruf trägt Titel, Beschreibung oder
	// Kommentar — nie ein Board.
	werkzeugMaxBody = 64 << 10
	// boardLesenMaxBytes: Obergrenze der Kartenliste in board_lesen (Bytes
	// JSON). Ein lokales Modell hat oft 8–32 k Token Kontext; die Antwort
	// soll davon nur einen Teil belegen.
	boardLesenMaxBytes = 24 << 10
	// boardLesenMaxTitel: längere Titel werden in der Liste gekürzt.
	boardLesenMaxTitel = 200
	// werkzeugMaxKommentar: Zeichen je Kommentar (wie die Beschreibung eines
	// Vorschlags).
	werkzeugMaxKommentar = 8000
)

// Die OpenAPI-Beschreibung und die Routen entstehen aus dem Werkzeug-Katalog
// (agent_werkzeug_katalog.go, ADR-0139 §1, Karte #752). Unabhängig davon
// bleiben zwei Quellen von Hand: die Positivliste werkzeugDarf (unten) und die
// Zusageliste mit der Rollen-Matrix im Test (agent_752_test.go). Bis Karte
// #752 war die Beschreibung selbst handgeschrieben; der eingecheckte Abzug
// werkzeuge_openapi.json zeigt heute nur noch, was erzeugt wird.

// WerkzeugRoute ist die öffentliche Sicht einer Route (für Tests).
type WerkzeugRoute struct{ Methode, Muster, OperationID string }

// WerkzeugRouten listet die registrierten Werkzeug-Routen, die Beschreibung
// selbst (GET /openapi.json, ohne OperationID) eingeschlossen.
func WerkzeugRouten() []WerkzeugRoute {
	aus := []WerkzeugRoute{{http.MethodGet, werkzeugSpezRoute, ""}}
	for _, e := range werkzeugKatalog {
		if e.Zugaenge&zugangOpenAPI != 0 {
			aus = append(aus, WerkzeugRoute{e.Methode, e.Muster, e.Name})
		}
	}
	return aus
}

// RegisterWerkzeuge hängt die Werkzeug-Routen an den Mux. Der Mux ist derselbe
// für Menschen und Agenten; jeder Handler antwortet ohne Agenten-Principal mit
// 404 — auf dem Menschen-Eingang gibt es diese Wege nicht. Ein Katalog, der
// seine eigene Prüfung nicht besteht, hält den Start an (panic beim Aufbau
// des Mux, nicht erst beim ersten Abruf von /openapi.json).
func (a *BoardsAPI) RegisterWerkzeuge(mux *http.ServeMux) {
	if err := werkzeugKatalogPruefen(werkzeugKatalog, werkzeugSchemas); err != nil {
		panic("Werkzeug-Katalog: " + err.Error())
	}
	// Kritiker #789, K-6: Die Seite des Board-Paars wird je Anfrage EINMAL
	// gelesen (mitPaarSeite) und im Kontext mitgeführt.
	mux.Handle(http.MethodGet+" "+werkzeugSpezRoute, WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.werkzeugSpez))))
	// MCP-Eingang (Karte #753): POST für JSON-RPC, GET/DELETE → 405 im Handler.
	// Ohne WithDefaults' Body-Grenze: werkzeugMCP liest selbst mit 64 KiB.
	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		mux.Handle(m+" /"+werkzeugMCPSegment, WithSecurityHeaders(a.mitPaarSeite(http.HandlerFunc(a.werkzeugMCP))))
	}
	for _, e := range werkzeugKatalog {
		if e.Zugaenge&zugangOpenAPI != 0 {
			mux.Handle(e.Methode+" "+e.Muster, WithDefaults(a.mitPaarSeite(e.handler(a))))
		}
	}
}

// WithOeffentlicheBasis setzt die Basis-URL für die Links „im Board öffnen“
// (ABOARD_PUBLIC_BASE_URL, schon geprüft). Leer: Antworten tragen link null.
//
// Bewusst NICHT aus der Anfrage abgeleitet: Der Host-Header einer
// Werkzeug-Anfrage ist der, den Open WebUI anspricht — oft eine interne
// Adresse (Agenten-Listener auf localhost), und fälschbar ist er ohnehin.
// Ein Link, den Menschen anklicken, muss aus der Konfiguration kommen.
func (a *BoardsAPI) WithOeffentlicheBasis(basis string) *BoardsAPI {
	a.oeffentlicheBasis = strings.TrimRight(basis, "/")
	return a
}

// ValidiereOeffentlicheBasis prüft ABOARD_PUBLIC_BASE_URL für die
// Werkzeug-Links: nur http/https, Host nötig, keine Zugangsdaten, keine
// Query, kein Fragment (der Link hängt `/#board=…` selbst an). Leer ist
// erlaubt (keine Links).
func ValidiereOeffentlicheBasis(roh string) (string, error) {
	roh = strings.TrimSpace(roh)
	if roh == "" {
		return "", nil
	}
	fehler := func(grund string) error { return fmt.Errorf("ABOARD_PUBLIC_BASE_URL: %s", grund) }
	if strings.IndexFunc(roh, unicode.IsSpace) >= 0 {
		return "", fehler("enthält Leerraum")
	}
	u, err := url.Parse(roh)
	if err != nil {
		return "", fehler("keine gültige URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fehler(fmt.Sprintf("Schema %q — erlaubt sind nur http und https", u.Scheme))
	}
	if u.Hostname() == "" {
		return "", fehler("kein Host")
	}
	if u.User != nil {
		return "", fehler("keine Zugangsdaten in der URL erlaubt (user:pass@)")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(roh, "#") {
		return "", fehler("keine Query und kein Fragment erlaubt")
	}
	return strings.TrimRight(roh, "/"), nil
}

// boardLink / kartenLink: Deep-Links im Format von src/deeplink.js
// (`#board=<id>&card=<ref>`; card darf displayId sein). nil ohne Basis.
func (a *BoardsAPI) boardLink(boardID string) *string {
	if a.oeffentlicheBasis == "" {
		return nil
	}
	l := a.oeffentlicheBasis + "/#board=" + url.QueryEscape(boardID)
	return &l
}

func (a *BoardsAPI) kartenLink(boardID string, displayID int) *string {
	if a.oeffentlicheBasis == "" || displayID <= 0 {
		return a.boardLink(boardID)
	}
	l := a.oeffentlicheBasis + "/#board=" + url.QueryEscape(boardID) + "&card=" + strconv.Itoa(displayID)
	return &l
}

// --- Positivliste -----------------------------------------------------------

func werkzeugIstSpezifikation(segs []string) bool {
	return len(segs) == 1 && segs[0] == werkzeugSpezSegment
}

// werkzeugIstMCP: der MCP-Eingang (Karte #753).
func werkzeugIstMCP(segs []string) bool {
	return len(segs) == 1 && segs[0] == werkzeugMCPSegment
}

func werkzeugIstBoardListe(segs []string) bool {
	return len(segs) == 2 && segs[0] == werkzeugSegment && segs[1] == "boards"
}

// werkzeugIstTerminListe: termine_lesen (Karte #743) — wie die Board-Liste
// ohne Board im Pfad; welche Boards zählen, liest der Handler aus der ACL.
func werkzeugIstTerminListe(segs []string) bool {
	return len(segs) == 2 && segs[0] == werkzeugSegment && segs[1] == "termine"
}

// werkzeugIstKartenSuche: karten_suchen (Karte #755) — wie boards ohne Board
// im Pfad; welche Boards zählen, liest der Handler (werkzeugErlaubteBoards).
func werkzeugIstKartenSuche(segs []string) bool {
	return len(segs) == 2 && segs[0] == werkzeugSegment && segs[1] == "karten"
}

// werkzeugOhneBoard: Werkzeug-Wege über alle Boards der Person (boards,
// termine_lesen, karten_suchen). Dort gibt es kein Board im Pfad und damit keine Rolle aus
// dem Pfad — es zählt die Person bzw. das eigene Board des Tokens.
func werkzeugOhneBoard(segs []string) bool {
	return werkzeugIstBoardListe(segs) || werkzeugIstTerminListe(segs) || werkzeugIstKartenSuche(segs)
}

// istWerkzeugPfad: Die Anfrage zielt auf einen Werkzeug-Weg (für die Meldung).
func istWerkzeugPfad(r *http.Request) bool {
	segs, ok := agentSegmente(r.URL.EscapedPath())
	return ok && len(segs) > 0 && segs[0] == werkzeugSegment
}

// werkzeugDarf ist die Positivliste der Werkzeug-Wege (aufgerufen aus
// agentDarf). Nur GET, kein HEAD: Die Spezifikation nennt nur GET, und der
// Test „Spezifikation = erlaubte Wege“ prüft, dass nichts darüber hinaus geht.
func werkzeugDarf(p *AgentPrincipal, methode string, segs []string) bool {
	if p.Role == storage.AgentRolleVorschlag || p.tokenRolle == storage.AgentRolleVorschlag {
		// ADR-0133 §6: Die Recherche-Instanz liest nichts — auch keine Liste
		// der Boards und keine Spezifikation.
		return false
	}
	if istInternRolle(p) {
		// Karte #783 (ADR-0140 §8, Stelle 2): Die Rolle intern hat keine
		// Werkzeuge und kein MCP — nur ihre fünf festen Wege. Geprüft an Role
		// UND tokenRolle (Klasse 183); werkzeugDarf läuft auch im MCP-Schritt B.
		return false
	}
	if werkzeugIstSpezifikation(segs) {
		return methode == http.MethodGet
	}
	if werkzeugIstMCP(segs) {
		// Karte #753 (ADR-0139 §3 Schritt A): POST für JSON-RPC; GET und
		// DELETE beantwortet der Handler mit 405 (zustandsloser Server ohne
		// Strom). Was ein tools/call darf, entscheidet Schritt B mit dieser
		// Funktion für den Werkzeug-Pfad.
		return methode == http.MethodPost || methode == http.MethodGet || methode == http.MethodDelete
	}
	if werkzeugIstKartenSuche(segs) {
		// Karte #755: nicht für Open WebUI freigegeben (ADR-0139 §7).
		if p.Delegiert {
			return false
		}
		return methode == http.MethodGet && (p.MehrBoard && p.OnBehalfOf != "" || !p.MehrBoard && p.BoardID != "")
	}
	if werkzeugOhneBoard(segs) {
		if p.Delegiert || p.MehrBoard {
			return methode == http.MethodGet && p.OnBehalfOf != ""
		}
		return methode == http.MethodGet && p.BoardID != ""
	}
	if len(segs) < 3 || segs[0] != werkzeugSegment || segs[1] != "boards" || p.BoardID == "" || segs[2] != p.BoardID {
		return false
	}
	lesen := p.Role == storage.AgentRolleViewer || p.Role == storage.AgentRolleEditor
	editor := p.Role == storage.AgentRolleEditor
	rest := segs[3:]
	switch {
	case len(rest) == 0:
		return methode == http.MethodGet && lesen
	case len(rest) == 1 && rest[0] == "karten":
		return methode == http.MethodPost && editor
	case len(rest) == 1 && rest[0] == "findings":
		return methode == http.MethodPost && editor
	case len(rest) == 1 && rest[0] == "termine":
		// Karte #743: Termin anlegen braucht Schreibrecht (ADR-0138 §4).
		return methode == http.MethodPost && editor
	case len(rest) == 2 && rest[0] == "karten":
		return methode == http.MethodGet && lesen
	// Karte #755 (ADR-0139 Stufe 4a) — Lesen, nicht für Open WebUI (§7):
	case len(rest) == 1 && rest[0] == "verlauf":
		return methode == http.MethodGet && lesen && !p.Delegiert
	case len(rest) == 1 && rest[0] == "mitglieder":
		return methode == http.MethodGet && lesen && !p.Delegiert
	case len(rest) == 3 && rest[0] == "karten" && rest[2] == "anhaenge":
		return methode == http.MethodGet && lesen && !p.Delegiert
	case len(rest) == 3 && rest[0] == "karten" && rest[2] == "kommentare":
		return methode == http.MethodPost && editor
	case len(rest) == 3 && rest[0] == "karten" && rest[2] == "verschieben":
		return methode == http.MethodPost && editor
	// Karte #755 (ADR-0139 Stufe 4a) — Schreiben, nicht für Open WebUI (§7):
	// karte_aendern, checkliste_abhaken, archivieren, wiederherstellen,
	// verweis_setzen, uebernehmen, freigeben.
	case len(rest) == 3 && rest[0] == "karten" && (rest[2] == "aendern" || rest[2] == "checkliste" || rest[2] == "archivieren" ||
		rest[2] == "wiederherstellen" || rest[2] == "verweise" || rest[2] == "uebernehmen" || rest[2] == "freigeben"):
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 4 && rest[0] == "karten" && rest[2] == "kommentare":
		// kommentar_bearbeiten: hinter einem delegierenden Token teilen sich
		// alle Personen den Autor (ADR-0139 §1) — dort nie.
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 4 && rest[0] == "karten" && rest[2] == "verweise" && rest[3] == "entfernen":
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 2 && rest[0] == "termine":
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 1 && rest[0] == "swimlanes":
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 2 && rest[0] == "swimlanes":
		return methode == http.MethodPost && editor && !p.Delegiert
	case len(rest) == 1 && rest[0] == "warten":
		// auf_antwort_warten: nur mit Person (Projekt-Tokens haben keine,
		// ADR-0139 §1; agentPayload lehnt es dort ebenfalls ab).
		return methode == http.MethodPost && editor && !p.Delegiert && p.OnBehalfOf != ""
	// Karte #757 (ADR-0139 Stufe 4b) — Lösch-Paare: GET prüft (Vorschau,
	// Code), POST löscht. Nur editor, nicht für Open WebUI (E-2: erst, wenn
	// das Partnerprojekt bestätigt, dass OWUI vor solchen Werkzeugen nachfragt). Spalten
	// gehören zur Board-Struktur: zusätzlich eine Person (owner prüft der
	// Handler, E-3).
	case len(rest) == 3 && rest[2] == "loeschen" &&
		(rest[0] == "karten" || rest[0] == "swimlanes" || rest[0] == "termine" || rest[0] == "anhaenge" || rest[0] == "spalten"):
		erlaubt := (methode == http.MethodGet || methode == http.MethodPost) && editor && !p.Delegiert
		if rest[0] == "spalten" {
			erlaubt = erlaubt && p.OnBehalfOf != ""
		}
		return erlaubt
	case len(rest) == 1 && rest[0] == "aendern":
		// board_aendern (E-3): nur eine Person, die owner ist (Handler).
		return methode == http.MethodPost && editor && !p.Delegiert && p.OnBehalfOf != ""
	}
	return false
}

// --- Handler ----------------------------------------------------------------

// werkzeugPrincipal: der Agent der Anfrage; ohne (Menschen-Eingang) 404.
func werkzeugPrincipal(w http.ResponseWriter, r *http.Request) *AgentPrincipal {
	p := AgentFromContext(r.Context())
	if p == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return p
}

// contextMitFrist: Frist eines einzelnen Handlers (wie die übrigen Routen).
func contextMitFrist(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), requestTimeout)
}

// werkzeugSpez — GET /openapi.json: die aus dem Katalog erzeugte
// Beschreibung, für ein delegierendes Token nur die dort freigegebenen
// Werkzeuge (ADR-0139 §1).
func (a *BoardsAPI) werkzeugSpez(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	// Karte #789 (E-3): Board-Paar-Texte je Seite der Instanz.
	spez, err := werkzeugSpezFuer(p, a.paarSeite(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tool description failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(spez)
}

type werkzeugBoardEintrag struct {
	BoardID       string  `json:"boardId"`
	Name          string  `json:"name"`
	Rolle         string  `json:"rolle"`
	DarfSchreiben bool    `json:"darfSchreiben"`
	Link          *string `json:"link"`
	// Boardpaar (Karte #789, E-3): nur bei gekoppelten Boards einer Instanz
	// mit Board-Paar; auf „aus“ fehlt das Feld.
	Boardpaar *wkBoardpaar `json:"boardpaar,omitempty"`
}

// werkzeugBoards — GET /werkzeuge/boards: die Boards, die die Person sieht.
// Beim delegierenden Token aus der ACL der Person (inkl. Domain-Freigabe,
// ADR-0132), Rolle gekappt auf die Token-Rolle; das Service-Board nie. Beim
// Personen-Token für mehrere Boards (Karte #754) die Boards seiner Liste mit
// persönlicher Rolle; bei Projekt- und übrigen Personen-Tokens das eigene Board.
func (a *BoardsAPI) werkzeugBoards(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	// Eine Regel für boards und karten_suchen (Kritiker #755 Teil 1, K-5).
	rollen, err := a.werkzeugErlaubteBoards(ctx, p)
	if a.acl == nil && p.Delegiert {
		writeError(w, http.StatusServiceUnavailable, "board list not available (no acl)")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	alle, err := a.repo.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	liste := []werkzeugBoardEintrag{}
	paare := a.werkzeugBoardpaare(ctx)
	for _, b := range alle {
		rolle, ok := rollen[b.ID]
		if !ok {
			continue
		}
		liste = append(liste, werkzeugBoardEintrag{BoardID: b.ID, Name: b.Title, Rolle: rolle,
			DarfSchreiben: rolle == storage.AgentRolleEditor, Link: a.boardLink(b.ID), Boardpaar: paare[b.ID]})
	}
	sort.SliceStable(liste, func(i, j int) bool { return strings.ToLower(liste[i].Name) < strings.ToLower(liste[j].Name) })
	var person any
	if p.OnBehalfOf != "" {
		person = p.OnBehalfOf
	}
	writeJSON(w, http.StatusOK, struct {
		// V1-3: Hinweis als erstes Feld, vor dem Fremdtext (Boardnamen).
		Sicherheitshinweis string                 `json:"sicherheitshinweis"`
		Person             any                    `json:"person"`
		Boards             []werkzeugBoardEintrag `json:"boards"`
	}{werkzeugFremdtextHinweis, person, liste})
}

// --- Board-Stand lesen (exakte Schlüssel wie im Fold, Sitzungs-QA K-1) -------

type wkSpalte struct{ ID, Titel string }

type wkKarte struct {
	ID, Titel, Spalte, Owner, Faellig, Kind string
	DisplayID                               int
	Archiviert                              bool
	FindingNr                               int
	// Vertraulich: der Bestandswert von confidential (Karte #797, K-1) — wie
	// schatten.VertraulichObjekt: fehlt das Feld, false; ein Wert, der kein
	// Wahrheitswert ist, gilt als vertraulich.
	Vertraulich bool
	// Roh: die Karte mit exakten Schlüsseln — für karte_zeigen.
	Roh map[string]json.RawMessage
}

type wkSwimlane struct {
	ID, Titel, Kind string
	Archiviert      bool
	Karten          []wkKarte
}

type wkBoard struct {
	Titel     string
	Spalten   []wkSpalte
	Swimlanes []wkSwimlane
	// aussen: Die Instanz ist die Seite außen eines Board-Paars (Karte #789).
	// Dann ist „i-<nr>“ eine interne Karte — kein Werkzeug außer den Lesern
	// aus ADR-0140 §7 nimmt sie an (findeWerkzeugKarte).
	aussen bool
}

func wkZahl(roh json.RawMessage) int {
	var f float64
	if json.Unmarshal(roh, &f) != nil {
		return 0
	}
	return int(f)
}

// leseWkBoard liest aus dem Board-JSON, was die Werkzeuge brauchen. Gelesen
// wird über exakte Map-Schlüssel: Ein Struct-Unmarshal nähme auch „ID“ oder
// „ARCHIVED“, und dann sähe Go eine andere Karte als der Fold.
func leseWkBoard(state []byte) (*wkBoard, error) {
	var stand map[string]json.RawMessage
	if err := json.Unmarshal(state, &stand); err != nil {
		return nil, err
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(stand["board"], &board); err != nil || board == nil {
		return nil, errors.New("board fehlt")
	}
	b := &wkBoard{Titel: zeichenketteVon(board["title"])}
	var spalten []map[string]json.RawMessage
	_ = json.Unmarshal(board["columnDefs"], &spalten)
	for _, s := range spalten {
		b.Spalten = append(b.Spalten, wkSpalte{ID: zeichenketteVon(s["id"]), Titel: zeichenketteVon(s["title"])})
	}
	var lanes []map[string]json.RawMessage
	_ = json.Unmarshal(board["sprints"], &lanes)
	for _, l := range lanes {
		sl := wkSwimlane{ID: zeichenketteVon(l["id"]), Titel: zeichenketteVon(l["title"]), Kind: zeichenketteVon(l["kind"]),
			Archiviert: jsWahr(l["archived"])}
		var karten []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &karten)
		for _, k := range karten {
			if k == nil {
				continue
			}
			wk := wkKarte{ID: zeichenketteVon(k["id"]), Titel: zeichenketteVon(k["title"]), Spalte: zeichenketteVon(k["columnId"]),
				Owner: zeichenketteVon(k["owner"]), Faellig: faelligMitUhrzeit(k), Kind: zeichenketteVon(k["kind"]),
				DisplayID: wkZahl(k["displayId"]), Archiviert: jsWahr(k["archived"]), Vertraulich: vertraulichRoh(k), Roh: k}
			if wk.Kind == "finding" {
				var f map[string]json.RawMessage
				if json.Unmarshal(k["finding"], &f) == nil {
					wk.FindingNr = wkZahl(f["nr"])
				}
			}
			sl.Karten = append(sl.Karten, wk)
		}
		b.Swimlanes = append(b.Swimlanes, sl)
	}
	return b, nil
}

func (b *wkBoard) spaltenTitel(id string) string {
	for _, s := range b.Spalten {
		if s.ID == id {
			if s.Titel != "" {
				return s.Titel
			}
			break
		}
	}
	return id
}

// wkTreffer: eine gefundene Karte mit ihrer Swimlane.
type wkTreffer struct {
	karte *wkKarte
	lane  *wkSwimlane
}

// kartenTreffer liefert alle Karten mit dieser displayId (auch „#42“); gibt
// es keine, die Karten mit dieser id.
func (b *wkBoard) kartenTreffer(ref string) []wkTreffer {
	ref = strings.TrimSpace(ref)
	var treffer []wkTreffer
	nr := strings.TrimPrefix(ref, "#")
	if n, err := strconv.Atoi(nr); err == nil && n > 0 && strconv.Itoa(n) == nr {
		for i := range b.Swimlanes {
			for j := range b.Swimlanes[i].Karten {
				if b.Swimlanes[i].Karten[j].DisplayID == n {
					treffer = append(treffer, wkTreffer{&b.Swimlanes[i].Karten[j], &b.Swimlanes[i]})
				}
			}
		}
	}
	if len(treffer) > 0 || ref == "" {
		return treffer
	}
	for i := range b.Swimlanes {
		for j := range b.Swimlanes[i].Karten {
			if b.Swimlanes[i].Karten[j].ID == ref {
				treffer = append(treffer, wkTreffer{&b.Swimlanes[i].Karten[j], &b.Swimlanes[i]})
			}
		}
	}
	return treffer
}

// findeKarte sucht über displayId (auch „#42“) oder id und nimmt den ersten
// Treffer. Nur für Server-interne Suchen nach einer selbst vergebenen id
// (Wiederholungsschutz, Rücklesen nach dem Schreiben); was die Person bzw.
// das Modell nennt, geht durch findeWerkzeugKarte.
func (b *wkBoard) findeKarte(ref string) (*wkKarte, *wkSwimlane) {
	if t := b.kartenTreffer(ref); len(t) > 0 {
		return t[0].karte, t[0].lane
	}
	return nil, nil
}

// findeWerkzeugKarte (Kritiker Sprint 82 Runde 2, KO-4): die Karte, die das
// Modell nennt — für karte_zeigen, kommentieren und verschieben. Karten des
// Mail-Eingangs gibt es für die Werkzeuge nicht (K1-6). Kein Treffer: 404.
// Tragen mehrere Karten dieselbe Nummer (doppelte displayId, z. B. nach einem
// Import), wurde bis KO-4 still die erste genommen — das Modell hätte an der
// falschen Karte kommentiert oder sie verschoben. Jetzt 400 mit den
// Karten-ids, wie findeSwimlane bei mehrdeutigem Namen; die id nimmt das
// Werkzeug an Stelle der Nummer an. nil: Antwort ist geschrieben.
func (b *wkBoard) findeWerkzeugKarte(w http.ResponseWriter, ref string) (*wkKarte, *wkSwimlane) {
	// Karte #789 (ADR-0140 §7): Schatten sind außen nur lesbar — kein
	// schreibendes Werkzeug (und keins außer board_lesen, karte_zeigen,
	// karten_suchen) findet sie.
	if schattenNurLesbar(w, b.aussen, ref) {
		return nil, nil
	}
	var sichtbar []wkTreffer
	for _, t := range b.kartenTreffer(ref) {
		if t.lane.Kind != "service" {
			sichtbar = append(sichtbar, t)
		}
	}
	switch len(sichtbar) {
	case 0:
		writeError(w, http.StatusNotFound, "Karte "+strconv.Quote(ref)+" gibt es auf diesem Board nicht. Die Nummer (displayId) nennt board_lesen.")
		return nil, nil
	case 1:
		return sichtbar[0].karte, sichtbar[0].lane
	}
	ids := make([]string, len(sichtbar))
	for i, t := range sichtbar {
		ids[i] = t.karte.ID + " (" + strconv.Quote(t.karte.Titel) + ")"
	}
	writeError(w, http.StatusBadRequest, "Mehrere Karten tragen die Nummer "+strconv.Quote(ref)+" — bitte die id angeben: "+strings.Join(ids, ", "))
	return nil, nil
}

// swimlaneSichtbar: Swimlanes, in die ein Werkzeug Karten legt oder aus denen
// es liest — nicht archiviert (ADR-0117: weggeräumt), nicht der Mail-Eingang.
func swimlaneSichtbar(s *wkSwimlane) bool {
	return !s.Archiviert && s.Kind != "service"
}

func swimlaneName(s *wkSwimlane) string {
	if s.Titel != "" {
		return s.Titel
	}
	return s.ID
}

// findeSwimlane: sichtbare Swimlane per id oder Name (ohne Rücksicht auf
// Groß-/Kleinschreibung). Kein oder mehrdeutiger Treffer: 400, nil.
func (b *wkBoard) findeSwimlane(w http.ResponseWriter, wunsch string) *wkSwimlane {
	var treffer []*wkSwimlane
	for i := range b.Swimlanes {
		s := &b.Swimlanes[i]
		if !swimlaneSichtbar(s) {
			continue
		}
		if s.ID == wunsch {
			return s
		}
		if strings.EqualFold(strings.TrimSpace(s.Titel), wunsch) {
			treffer = append(treffer, s)
		}
	}
	switch len(treffer) {
	case 0:
		writeError(w, http.StatusBadRequest, "Swimlane "+strconv.Quote(wunsch)+" gibt es auf diesem Board nicht (oder sie ist archiviert). Mögliche Swimlanes nennt board_lesen.")
		return nil
	case 1:
		return treffer[0]
	}
	ids := make([]string, len(treffer))
	for i, t := range treffer {
		ids[i] = t.ID
	}
	writeError(w, http.StatusBadRequest, "Mehrere Swimlanes heißen "+strconv.Quote(wunsch)+" — bitte die id angeben: "+strings.Join(ids, ", "))
	return nil
}

// findeSpalte: Spalte per id oder Name (ohne Rücksicht auf Groß-/Kleinschreibung).
func (b *wkBoard) findeSpalte(wunsch string) (string, bool) {
	for _, s := range b.Spalten {
		if s.ID == wunsch {
			return s.ID, true
		}
	}
	for _, s := range b.Spalten {
		if strings.EqualFold(strings.TrimSpace(s.Titel), wunsch) {
			return s.ID, true
		}
	}
	return "", false
}

// spalteFehlt: 400 mit den Spalten des Boards (verschieben, karte_anlegen).
func (b *wkBoard) spalteFehlt(w http.ResponseWriter, wunsch string) {
	namen := make([]string, len(b.Spalten))
	for i, s := range b.Spalten {
		namen[i] = b.spaltenTitel(s.ID)
	}
	writeError(w, http.StatusBadRequest, "Spalte "+strconv.Quote(wunsch)+" gibt es nicht. Spalten dieses Boards: "+strings.Join(namen, ", "))
}

// ladeWkBoard: Board des Pfads laden. Fehlt es, dieselbe 403 wie für eine
// fehlende Freigabe — wer bis hierher kommt, hat zwar eine Rolle am Board
// (ACL-Eintrag ohne Board), aber ein Unterschied in der Antwort verriete den
// Zustand nicht mehr als nötig.
func (a *BoardsAPI) ladeWkBoard(w http.ResponseWriter, r *http.Request, boardID string) *wkBoard {
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	b, err := a.repo.Get(ctx, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusForbidden, verbotenMeldungFuer(AgentFromContext(r.Context())))
		return nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return nil
	}
	wb, err := leseWkBoard(b.StateJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return nil
	}
	if wb.Titel == "" {
		wb.Titel = b.Title
	}
	wb.aussen = a.paarSeite(ctx) == BoardpaarSeiteAussen
	return wb
}

// leseWkBoardStill liest den Board-Stand ohne Antwort zu schreiben (für
// Angaben, die eine bereits erfolgreiche Antwort nur ergänzen).
func (a *BoardsAPI) leseWkBoardStill(r *http.Request, boardID string) (*wkBoard, error) {
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return nil, err
	}
	return leseWkBoard(b.StateJSON)
}

type wkKartenZeile struct {
	DisplayID int     `json:"displayId"`
	Titel     string  `json:"titel"`
	Spalte    string  `json:"spalte"`
	Swimlane  string  `json:"swimlane"`
	Owner     string  `json:"owner,omitempty"`
	Faellig   string  `json:"faellig,omitempty"`
	Finding   string  `json:"finding,omitempty"`
	Link      *string `json:"link"`
}

// wahrParameter liest einen Wahrheitswert aus der Query. Open WebUI kodiert
// Python-Werte (`True`), Menschen schreiben `ja` — beides zählt.
func wahrParameter(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "ja", "yes":
		return true
	}
	return false
}

// werkzeugBoardLesen — GET /werkzeuge/boards/{boardId}: offene Karten kompakt.
// Weg: archivierte Karten und Swimlanes, der Mail-Eingang, Sprint-Meta-Karten
// und — ohne mitErledigten — die Spalte „Erledigt“. Die Kartenliste ist auf
// boardLesenMaxBytes begrenzt; gekürzt wird mit Hinweis.
func (a *BoardsAPI) werkzeugBoardLesen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	boardID := r.PathValue("boardId")
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	q := r.URL.Query()
	filterLane := strings.TrimSpace(q.Get("swimlane"))
	filterSpalte := strings.TrimSpace(q.Get("spalte"))
	mitErledigten := wahrParameter(q.Get("mitErledigten"))

	type laneZeile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Art  string `json:"art"`
	}
	lanes := []laneZeile{}
	for i := range wb.Swimlanes {
		s := &wb.Swimlanes[i]
		if swimlaneSichtbar(s) {
			lanes = append(lanes, laneZeile{ID: s.ID, Name: swimlaneName(s), Art: s.Kind})
		}
	}
	spalten := make([]map[string]string, 0, len(wb.Spalten))
	for _, s := range wb.Spalten {
		spalten = append(spalten, map[string]string{"id": s.ID, "name": wb.spaltenTitel(s.ID)})
	}

	karten := []wkKartenZeile{}
	gesamt, belegt := 0, 0
	for i := range wb.Swimlanes {
		s := &wb.Swimlanes[i]
		if !swimlaneSichtbar(s) {
			continue
		}
		if filterLane != "" && s.ID != filterLane && !strings.EqualFold(strings.TrimSpace(s.Titel), filterLane) {
			continue
		}
		for _, k := range s.Karten {
			if k.Archiviert || k.Kind == "sprint-meta" {
				continue
			}
			if !mitErledigten && k.Spalte == "col-erledigt" {
				continue
			}
			spalte := wb.spaltenTitel(k.Spalte)
			if filterSpalte != "" && k.Spalte != filterSpalte && !strings.EqualFold(spalte, filterSpalte) {
				continue
			}
			gesamt++
			z := wkKartenZeile{DisplayID: k.DisplayID, Titel: kuerze(k.Titel, boardLesenMaxTitel), Spalte: spalte,
				Swimlane: swimlaneName(s), Owner: k.Owner, Faellig: k.Faellig, Link: a.kartenLink(boardID, k.DisplayID)}
			if k.Kind == "finding" && k.FindingNr > 0 {
				z.Finding = "F-" + strconv.Itoa(k.FindingNr)
			}
			roh, _ := json.Marshal(z)
			if belegt+len(roh) > boardLesenMaxBytes {
				continue // weiterzählen, nicht mehr aufnehmen
			}
			belegt += len(roh)
			karten = append(karten, z)
		}
	}
	// Karte #789 (ADR-0140 §7): auf der Seite außen die Schatten des Boards,
	// nur lesbar, ohne Link. Auf aus/innen fehlen die Felder ganz.
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	schattenListe, paarAktiv, err := a.werkzeugSchattenLesen(ctx, boardID)
	if err != nil {
		schattenLeseFehler(w, err)
		return
	}
	var intern []wkSchatten
	internGesamt, internBelegt := 0, 0
	if paarAktiv && filterLane == "" {
		intern = []wkSchatten{}
		for _, sch := range schattenListe {
			if filterSpalte != "" && !strings.EqualFold(sch.Spalte, filterSpalte) {
				continue
			}
			internGesamt++
			roh, _ := json.Marshal(sch)
			if internBelegt+len(roh) > boardLesenMaxSchattenBytes {
				continue
			}
			internBelegt += len(roh)
			intern = append(intern, sch)
		}
	}
	var internFeld *[]wkSchatten
	if intern != nil {
		internFeld = &intern
	}
	// K3-5: Titel, Namen von Spalten und Swimlanes sind Fremdtext. V1-3: der
	// Hinweis steht als ERSTES Feld vor ihm — Struct statt Map, denn
	// encoding/json ordnet Map-Schlüssel alphabetisch.
	antwort := struct {
		Sicherheitshinweis string              `json:"sicherheitshinweis"`
		Board              map[string]any      `json:"board"`
		Spalten            []map[string]string `json:"spalten"`
		Swimlanes          []laneZeile         `json:"swimlanes"`
		Karten             []wkKartenZeile     `json:"karten"`
		Anzahl             int                 `json:"anzahl"`
		Gekuerzt           bool                `json:"gekuerzt"`
		Hinweis            string              `json:"hinweis,omitempty"`
		// Karte #789: nur auf der Seite außen. Kritiker K-5: ohne swimlane-
		// Filter IMMER da, auch leer ([]); mit swimlane fehlt es (nil-Zeiger),
		// weil Schatten keine Swimlane haben. Ein Slice mit omitempty ließe
		// die leere Liste verschwinden.
		Intern         *[]wkSchatten `json:"intern,omitempty"`
		InternAnzahl   int           `json:"internAnzahl,omitempty"`
		InternGekuerzt bool          `json:"internGekuerzt,omitempty"`
	}{
		Sicherheitshinweis: werkzeugFremdtextHinweis,
		Board:              map[string]any{"boardId": boardID, "name": wb.Titel, "rolle": p.Role, "link": a.boardLink(boardID)},
		Spalten:            spalten,
		Swimlanes:          lanes,
		Karten:             karten,
		Anzahl:             gesamt,
		Gekuerzt:           len(karten) < gesamt,
		Intern:             internFeld,
		InternAnzahl:       internGesamt,
		InternGekuerzt:     len(intern) < internGesamt,
	}
	if len(karten) < gesamt {
		antwort.Hinweis = fmt.Sprintf("Gekürzt: %d von %d Karten gezeigt. Mit swimlane oder spalte eingrenzen.", len(karten), gesamt)
	}
	if bp := a.werkzeugBoardpaarFuer(ctx, p, boardID); bp != nil {
		antwort.Board["boardpaar"] = bp
	}
	writeJSON(w, http.StatusOK, antwort)
}

// kuerze schneidet auf n Zeichen (Runen) und hängt „…“ an.
func kuerze(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// --- Schreiben über postOps ------------------------------------------------

// leseWerkzeugBody liest einen JSON-Objekt-Body mit genau den erlaubten
// Feldern. Unbekannte Felder führen zur Ablehnung — still verworfen hielte das
// Modell sie für gesetzt (etwa „owner“).
func leseWerkzeugBody(w http.ResponseWriter, r *http.Request, erlaubt ...string) (map[string]json.RawMessage, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, werkzeugMaxBody)
	roh, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Anfrage zu groß (höchstens %d Bytes)", werkzeugMaxBody))
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(roh, &m) != nil || m == nil {
		writeError(w, http.StatusBadRequest, "Anfrage muss ein JSON-Objekt sein, Felder: "+strings.Join(erlaubt, ", "))
		return nil, false
	}
	if k := schreibweisenDoppel(m); k != "" {
		writeError(w, http.StatusBadRequest, "Feld "+k+" kommt in zwei Schreibweisen vor")
		return nil, false
	}
	for k := range m {
		ok := false
		for _, e := range erlaubt {
			if k == e {
				ok = true
				break
			}
		}
		if !ok {
			writeError(w, http.StatusBadRequest, "unbekanntes Feld "+strconv.Quote(k)+" — erlaubt sind nur: "+strings.Join(erlaubt, ", "))
			return nil, false
		}
	}
	return m, true
}

// boolFeld liest ein optionales Wahrheitsfeld (Karte #797). Fehlt es oder ist
// es null, ist die Antwort nil — „nicht gesendet“, nie ein Zero-Value false.
// Nur die JSON-Literale true und false gelten; eine Zeichenkette wie "false"
// oder eine Zahl wird abgelehnt (400), nicht gedeutet: Ein Modell, das
// "false" schreibt, wollte vermutlich öffnen, und stillschweigend
// vertraulich zu bleiben wäre die sichere, aber unbemerkte Richtung —
// die Ablehnung sagt ihm, was es falsch gemacht hat.
func boolFeld(m map[string]json.RawMessage, name string) (*bool, string) {
	roh, da := m[name]
	if !da {
		return nil, ""
	}
	switch strings.TrimSpace(string(roh)) {
	case "null":
		return nil, ""
	case "true":
		v := true
		return &v, ""
	case "false":
		v := false
		return &v, ""
	}
	return nil, name + " muss true oder false sein (ein Wahrheitswert, kein Text)"
}

// vertraulichRoh liest confidential eines rohen Eintrags (Karte oder Termin)
// mit dem einen Prädikat aller Stellen (schatten.Vertraulich, ADR-0140 §10):
// fehlt es, ist der Eintrag offen; ein Wert, der kein Wahrheitswert ist,
// zählt als vertraulich.
func vertraulichRoh(m map[string]json.RawMessage) bool {
	return schatten.Vertraulich(m)
}

// anlageOffenSumme: Beitrag zum Wiederholungsschlüssel (Karte #797, K-1).
// Nur ein ausdrücklich gesendetes false kommt hinzu — alle bisherigen
// Aufrufe behalten ihren Schlüssel; „vertraulich“ (Standard, true, null)
// bleibt ohne Beitrag. So ist ein zweiter Aufruf mit anderer Angabe nicht
// dieselbe Anfrage: ohne Kopf eine neue Anlage, mit demselben Idempotency-Key
// ein 422 — nie die Meldung „bereits angelegt“ für die falsche Fassung.
func anlageOffenSumme(ctx context.Context) []string {
	if anlageOffenAus(ctx) {
		return []string{"vertraulich=false"}
	}
	return nil
}

// vertraulichAntwort ergänzt die Antwort um den tatsächlichen Stand
// (`vertraulich`), wo die Instanz das Feld anbietet (Karte #797, K-1).
func (a *BoardsAPI) vertraulichAntwort(ctx context.Context, antwort map[string]any, vertraulich bool) {
	if a.vertraulichAusgeben(ctx) {
		antwort["vertraulich"] = vertraulich
	}
}

// vertraulichAngeboten: Bietet diese Instanz das Feld `vertraulich` beim
// Anlegen an? Nur auf der Seite innen des Board-Paars (W-4 gilt nur dort);
// sonst ist es kein Feld, und der Aufruf scheitert wie bei jedem unbekannten
// Feld — passend zum Schema, das es dort nicht nennt. Ist die Seite nicht
// lesbar, gilt das Feld als angeboten: postOps lehnt den Batch dann mit 503
// ab (fail-closed), statt dass hier ein falscher 400 dazwischenkommt.
func (a *BoardsAPI) vertraulichAngeboten(ctx context.Context) bool {
	seite, err := a.paarSeiteMitFehler(ctx)
	return err != nil || seite == BoardpaarSeiteInnen
}

// vertraulichAusgeben: Soll die Antwort `vertraulich` tragen? Strenger als
// vertraulichAngeboten (V-5e): Ist die Seite nicht lesbar, wird nichts
// ausgegeben — die Annahme des Felds bleibt fail-closed (postOps lehnt mit 503
// ab), die Ausgabe dagegen darf auf „aus“ kein Feld preisgeben, das dort
// nicht existiert.
func (a *BoardsAPI) vertraulichAusgeben(ctx context.Context) bool {
	seite, err := a.paarSeiteMitFehler(ctx)
	return err == nil && seite == BoardpaarSeiteInnen
}

// leseAnlageVertraulich wertet `vertraulich` beim Anlegen aus und liefert die
// Anfrage mit Kennzeichen, wenn ausdrücklich false gesendet wurde.
func (a *BoardsAPI) leseAnlageVertraulich(r *http.Request, m map[string]json.RawMessage) (*http.Request, string) {
	v, grund := boolFeld(m, "vertraulich")
	if grund != "" {
		return r, grund
	}
	if v != nil && !*v {
		return r.WithContext(mitAnlageOffen(r.Context())), ""
	}
	return r, ""
}

// anlegenFelder: die erlaubten Felder eines anlegenden Werkzeugs; `vertraulich`
// nur, wo die Instanz es anbietet.
func (a *BoardsAPI) anlegenFelder(ctx context.Context, felder ...string) []string {
	if a.vertraulichAngeboten(ctx) {
		return append(append([]string{}, felder...), "vertraulich")
	}
	return felder
}

// textFeld liest ein Textfeld: Zeichenkette, höchstens max Zeichen, ohne
// Steuerzeichen (Zeilenumbruch und Tab nur mit mehrzeilig). Fehlt es und ist
// es Pflicht, Fehler.
func textFeld(m map[string]json.RawMessage, name string, max int, pflicht, mehrzeilig bool) (string, string) {
	roh, da := m[name]
	if !da || strings.TrimSpace(string(roh)) == "null" {
		if pflicht {
			return "", name + " fehlt"
		}
		return "", ""
	}
	var s string
	if json.Unmarshal(roh, &s) != nil {
		return "", name + " muss Text sein"
	}
	s = strings.TrimSpace(s)
	if pflicht && s == "" {
		return "", name + " darf nicht leer sein"
	}
	if utf8.RuneCountInString(s) > max || hatSteuerzeichen(s, mehrzeilig) {
		return "", fmt.Sprintf("%s: höchstens %d Zeichen, keine Steuerzeichen", name, max)
	}
	return s, ""
}

// textFeldInhalt ist textFeld für Pflichtfelder, die einen Inhalt tragen sollen (Titel, Kommentartext):
// zusätzlich muss mindestens ein Zeichen aus L, N, P oder S vorkommen — dieselbe Pflicht wie bei
// Vorschlägen (F-2120, hatInhaltszeichen). Ein Kombinationszeichen allein (Mn/Mc) ist kein Inhalt;
// TrimSpace kennt es nicht (Karte #863, A-5).
func textFeldInhalt(m map[string]json.RawMessage, name string, max int, pflicht, mehrzeilig bool) (string, string) {
	wert, grund := textFeld(m, name, max, pflicht, mehrzeilig)
	if grund == "" && pflicht && wert != "" && !hatInhaltszeichen(wert) {
		return "", name + ": braucht mindestens einen Buchstaben, eine Ziffer oder ein Satz- oder Sonderzeichen"
	}
	return wert, grund
}

// pufferAntwort nimmt die Antwort von postOps auf, damit das Werkzeug sie in
// seine kompakte Form übersetzen kann.
type pufferAntwort struct {
	kopf   http.Header
	status int
	body   bytes.Buffer
}

func (p *pufferAntwort) Header() http.Header { return p.kopf }
func (p *pufferAntwort) WriteHeader(s int) {
	if p.status == 0 {
		p.status = s
	}
}
func (p *pufferAntwort) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	return p.body.Write(b)
}

// werkzeugOps schickt die Ops durch postOps — derselbe Handler wie
// POST /boards/{id}/ops, mit dem Kontext (Principal, Frist) der Anfrage.
// Bei Erfolg die Quittung je Op, sonst ist die Antwort schon geschrieben.
func (a *BoardsAPI) werkzeugOps(w http.ResponseWriter, r *http.Request, boardID string, ops []map[string]any) ([]map[string]json.RawMessage, bool) {
	quittung, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, ops)
	if !ok && puffer != nil {
		werkzeugOpsFehler(w, puffer)
	}
	return quittung, ok
}

// werkzeugOpsRoh: wie werkzeugOps, schreibt bei einer Ablehnung von postOps
// aber nichts — der Aufrufer bekommt die Antwort (puffer) und übersetzt sie
// selbst, etwa „seit dem Lesen geändert“ oder „gehört gerade jemand anderem“
// (Karte #755). puffer nil und ok false: Antwort ist schon geschrieben.
func (a *BoardsAPI) werkzeugOpsRoh(w http.ResponseWriter, r *http.Request, boardID string, ops []map[string]any) ([]map[string]json.RawMessage, *pufferAntwort, bool) {
	roh, err := json.Marshal(map[string]any{"ops": ops})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ops encode failed", err)
		return nil, nil, false
	}
	// Karte #757: als Werkzeug-Weg gekennzeichnet — nur hier darf ein Agent
	// die Board-Struktur schreiben (agentRohSperre, ADR-0139 E-3).
	r2 := r.Clone(context.WithValue(r.Context(), werkzeugWegSchluessel{}, true))
	r2.Method = http.MethodPost
	r2.Body = io.NopCloser(bytes.NewReader(roh))
	r2.ContentLength = int64(len(roh))
	r2.SetPathValue("id", boardID)
	puffer := &pufferAntwort{kopf: http.Header{}}
	a.postOps(puffer, r2)
	if puffer.status != http.StatusOK {
		return nil, puffer, false
	}
	var quittung struct {
		Ops []map[string]json.RawMessage `json:"ops"`
	}
	if json.Unmarshal(puffer.body.Bytes(), &quittung) != nil || len(quittung.Ops) != len(ops) {
		writeError(w, http.StatusInternalServerError, "ops receipt unreadable")
		return nil, nil, false
	}
	return quittung.Ops, nil, true
}

// opsProtokollArten: die Einträge des Fold-Protokolls einer 422-Ablehnung
// (`protokoll`), sonst nil.
func opsProtokollArten(p *pufferAntwort) []map[string]json.RawMessage {
	if p == nil || p.status != http.StatusUnprocessableEntity {
		return nil
	}
	var antwort struct {
		Protokoll []map[string]json.RawMessage `json:"protokoll"`
	}
	if json.Unmarshal(p.body.Bytes(), &antwort) != nil {
		return nil
	}
	return antwort.Protokoll
}

// werkzeugOpsFehler übersetzt eine Ablehnung von postOps für das Modell:
// deutscher Satz plus die Originalantwort als details.
func werkzeugOpsFehler(w http.ResponseWriter, p *pufferAntwort) {
	var details any
	if json.Unmarshal(p.body.Bytes(), &details) != nil {
		details = nil
	}
	satz := "nicht ausgeführt"
	switch p.status {
	case http.StatusBadRequest:
		satz = "nicht ausgeführt: Die Änderung ist ungültig."
	case http.StatusNotFound:
		satz = "nicht ausgeführt: Board nicht gefunden."
	case http.StatusConflict, http.StatusUnprocessableEntity:
		// Die Begründung des Folds (etwa ein Wert außerhalb des Finding-
		// Wortschatzes) steht im Protokoll unter details.
		satz = "nicht ausgeführt: Das Board hat die Änderung abgelehnt (Grund unter details). Nichts geschrieben."
		for _, e := range opsProtokollArten(p) {
			if zeichenketteVon(e["art"]) == "feld-geaendert" {
				// Karte #755: card.fieldIf — ein Mensch war zwischen Lesen und
				// Schreiben schneller.
				satz = "nicht ausgeführt: Die Karte wurde seit dem Lesen geändert. Mit karte_zeigen neu lesen und die Änderung mit dem neuen stand wiederholen. Nichts geschrieben."
			}
		}
	case http.StatusTooManyRequests:
		satz = "nicht ausgeführt: zu viele Änderungen in kurzer Zeit. Bitte etwas warten und dann erneut versuchen."
		if bytes.Contains(p.body.Bytes(), []byte("anlage-bremse")) {
			// Karte #798: Grenze für nicht vertrauliche Anlagen (sie gehen nach außen).
			satz = fmt.Sprintf("nicht ausgeführt: Diese Stunde wurden schon %d nicht vertrauliche Einträge angelegt, die nach außen gehen. Später erneut versuchen oder ohne vertraulich:false (vertraulich) anlegen. Nichts geschrieben.", anlageBremseMax)
		}
		if ra := p.kopf.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
	case http.StatusRequestEntityTooLarge:
		satz = "nicht ausgeführt: Das Board wäre danach zu groß."
	case http.StatusServiceUnavailable:
		satz = "nicht ausgeführt: Der Server ist gerade ausgelastet. Bitte erneut versuchen."
	}
	writeJSON(w, p.status, map[string]any{"error": satz, "details": details})
}

// werkzeugKarteAnlegen — POST /werkzeuge/boards/{boardId}/karten.
//
// Ziel: Spalte Backlog (col-backlog; fehlt sie, die erste Spalte) im
// allgemeinen Backlog (`__backlog__`) — dort sieht jeder sie, und niemandes
// Sprint wächst ungefragt. Mit `swimlane` (id oder Name) in einer anderen
// sichtbaren Swimlane. Die Karten-id vergibt der Server, die displayId der
// Fold; die Antwort nennt beide und den Link.
func (a *BoardsAPI) werkzeugKarteAnlegen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	// Karte #755: spalte, owner, labels, faellig, faelligUhrzeit (ADR-0139,
	// Werkzeugliste: „erweitert um Spalte, Owner, Labels, Frist mit Uhrzeit“).
	m, ok := leseWerkzeugBody(w, r, a.anlegenFelder(r.Context(), "titel", "beschreibung", "swimlane", "spalte", "owner", "labels", "faellig", "faelligUhrzeit")...)
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
	if p.Delegiert && (feldGesetzt(m, "spalte") || feldGesetzt(m, "owner")) {
		// Kritiker #755 Teil 2, K-1: Open WebUI legt Karten weiter nur im
		// Backlog und ohne Owner an. Sonst könnte ein von Fremdtext gesteuertes
		// Modell eine Karte direkt in Ready mit Claude als Owner anlegen — am
		// Menschen vorbei, der Karten aus dem Backlog nach Ready zieht
		// (ADR-0043). Eine Freigabe dafür wäre eine eigene Entscheidung (§7).
		writeError(w, http.StatusBadRequest, "spalte und owner gibt es über Open WebUI nicht — die Karte entsteht im Backlog; verschieben und Owner setzen macht die Person im Board.")
		return
	}
	titel, grund := textFeldInhalt(m, "titel", maxVorschlagTitel, true, false)
	var beschreibung, laneWunsch, spalteWunsch string
	if grund == "" {
		beschreibung, grund = textFeld(m, "beschreibung", maxVorschlagBeschreibung, false, true)
	}
	if grund == "" {
		laneWunsch, grund = textFeld(m, "swimlane", maxVorschlagTitel, false, false)
	}
	if grund == "" {
		spalteWunsch, grund = textFeld(m, "spalte", maxVorschlagTitel, false, false)
	}
	var extra map[string]any
	var extraSumme []string
	if grund == "" {
		extra, extraSumme, grund = leseKarteAnlegenExtra(m)
	}
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	inhalt := append([]string{titel, beschreibung, laneWunsch, spalteWunsch}, extraSumme...)
	if len(extraSumme) == 0 && spalteWunsch == "" {
		// Ohne die neuen Felder derselbe Schlüssel wie vor #755 — ein Aufruf
		// über das Update hinweg bleibt eine Wiederholung.
		inhalt = []string{titel, beschreibung, laneWunsch}
	}
	// Karte #797, K-1: Ein ausdrückliches vertraulich:false gehört zur Anfrage.
	inhalt = append(inhalt, anlageOffenSumme(r.Context())...)
	if idem, ok := a.werkzeugIdemBeginn(w, r, p, boardID, "karte_anlegen", inhalt...); ok {
		a.karteAnlegen(w, r, boardID, titel, beschreibung, laneWunsch, spalteWunsch, extra, idem)
	}
}

// karteAnlegen legt die Karte an; extra ergänzt Felder (Finding: kind und
// finding; karte_anlegen: owner, labels, dueDate, dueTime). Gemeinsam für
// karte_anlegen und finding_anlegen. spalteWunsch: Spalte per Name oder id,
// leer = Backlog. idem trägt Client-id, Karten-id und Datum
// (Wiederholungsschutz, K1-4): Steht die Karte schon auf dem Board, antwortet
// das Werkzeug „bereits angelegt“, statt ein Doppel zu bauen.
func (a *BoardsAPI) karteAnlegen(w http.ResponseWriter, r *http.Request, boardID, titel, beschreibung, laneWunsch, spalteWunsch string, extra map[string]any, idem wkIdemEintrag) {
	a.karteAnlegenVersuch(w, r, boardID, titel, beschreibung, laneWunsch, spalteWunsch, extra, idem, false)
}

// karteAktiv: steht die Karte sichtbar auf dem Board (nicht archiviert, auch
// nicht über ihre Swimlane)? Nur dann ist eine erneute Anlage eine Wiederholung
// (Verify V1-4).
func karteAktiv(k *wkKarte, lane *wkSwimlane) bool {
	return k != nil && !k.Archiviert && (lane == nil || !lane.Archiviert)
}

// karteAnlegenVersuch: erneuert=true heisst, die Ids wurden in diesem Aufruf
// schon einmal erneuert (V1-4) — kein zweites Mal.
func (a *BoardsAPI) karteAnlegenVersuch(w http.ResponseWriter, r *http.Request, boardID, titel, beschreibung, laneWunsch, spalteWunsch string, extra map[string]any, idem wkIdemEintrag, erneuert bool) {
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	if k, lane := wb.findeKarte(idem.objektID); k != nil {
		if karteAktiv(k, lane) {
			a.karteSchonAngelegt(r.Context(), w, boardID, wb, k, lane)
			return
		}
		if idem.mitKopf {
			// KA-4: Derselbe Idempotency-Key ist dieselbe Anfrage — archiviert
			// heißt nicht „neu anlegen“.
			karteNichtMehrDa(w, boardID, "inzwischen archiviert")
			return
		}
		if !erneuert {
			// V1-4: archiviert (oder in archivierter Swimlane) — neu anlegen.
			a.karteAnlegenVersuch(w, r, boardID, titel, beschreibung, laneWunsch, spalteWunsch, extra, a.werkzeugIdemErneuern(idem), true)
			return
		}
	}
	var ziel *wkSwimlane
	if laneWunsch == "" {
		for i := range wb.Swimlanes {
			if wb.Swimlanes[i].ID == "__backlog__" && swimlaneSichtbar(&wb.Swimlanes[i]) {
				ziel = &wb.Swimlanes[i]
			}
		}
		if ziel == nil {
			writeError(w, http.StatusBadRequest, "Das Board hat keinen allgemeinen Backlog — bitte swimlane angeben (siehe board_lesen).")
			return
		}
	} else if ziel = wb.findeSwimlane(w, laneWunsch); ziel == nil {
		return
	}
	spalte := vorschlagSpalte
	if len(wb.Spalten) > 0 {
		gefunden := false
		for _, s := range wb.Spalten {
			if s.ID == vorschlagSpalte {
				gefunden = true
			}
		}
		if !gefunden {
			spalte = wb.Spalten[0].ID
		}
	}
	if spalteWunsch != "" {
		var gefunden bool
		if spalte, gefunden = wb.findeSpalte(spalteWunsch); !gefunden {
			wb.spalteFehlt(w, spalteWunsch)
			return
		}
	}
	datum := idem.datum
	kartenID := idem.objektID
	// Felder wie Model.createCard (src/model.js) — ohne comments und activity:
	// Die setzt für Agenten nur der Server (agentPayload lehnt sie ab), und
	// der Browser ergänzt fehlende Listen beim Laden.
	karte := map[string]any{
		"id": kartenID, "displayId": nil, "title": titel, "description": beschreibung,
		"status": "offen", "priority": "mittel", "labels": []any{},
		"milestone": nil, "session": nil,
		"qa": false, "critical": false, "blocked": false,
		"owner": "", "assignees": []any{}, "project_id": nil,
		"complexity": "", "effort": nil, "agentContext": "",
		"links": []any{}, "attachments": []any{}, "coverAttachmentId": nil,
		"dueDate": nil, "startDate": nil, "recurrence": nil, "archived": false,
		"columnId": spalte, "created": datum, "updated": datum,
	}
	for k, v := range extra {
		karte[k] = v
	}
	quittung, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{
		"id":      idem.clientID,
		"type":    "card.upsert",
		"payload": map[string]any{"card": karte, "sprintId": ziel.ID, "columnId": spalte},
	}})
	if !ok {
		return
	}
	if jsWahr(quittung[0]["wiederholt"]) {
		// Ein gleichzeitiger Aufruf mit demselben Schlüssel war schneller: Der
		// Ledger hat diese Op schon angenommen. Die Karte aus dem Stand holen.
		if wb2 := a.ladeWkBoard(w, r, boardID); wb2 != nil {
			if k, lane := wb2.findeKarte(kartenID); karteAktiv(k, lane) {
				a.karteSchonAngelegt(r.Context(), w, boardID, wb2, k, lane)
			} else if idem.mitKopf {
				// KA-4: gleicher Schlüssel = gleiche Anfrage; nichts neu anlegen.
				karteNichtMehrDa(w, boardID, "inzwischen gelöscht oder archiviert")
			} else if !erneuert {
				// V1-4: Die Karte des ersten Aufrufs ist inzwischen gelöscht
				// oder archiviert — das ist ein neuer Wunsch, neu anlegen.
				a.karteAnlegenVersuch(w, r, boardID, titel, beschreibung, laneWunsch, spalteWunsch, extra, a.werkzeugIdemErneuern(idem), true)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "bereits angelegt (Wiederholung erkannt) — die Karte steht inzwischen nicht mehr auf dem Board",
					"wiederholt": true, "boardId": boardID})
			}
		}
		return
	}
	displayID := wkZahl(quittung[0]["displayId"])
	if zeichenketteVon(quittung[0]["cardId"]) != kartenID || displayID <= 0 {
		// Ohne displayId keine neue Karte — das darf nach postOps nicht sein.
		writeError(w, http.StatusInternalServerError, "card not created")
		return
	}
	antwort := map[string]any{
		"ergebnis":  fmt.Sprintf("Karte #%d angelegt", displayID),
		"displayId": displayID,
		"titel":     titel,
		"swimlane":  swimlaneName(ziel),
		"spalte":    wb.spaltenTitel(spalte),
		"boardId":   boardID,
		"link":      a.kartenLink(boardID, displayID),
	}
	if a.vertraulichAusgeben(r.Context()) {
		// K-1: der Stand aus dem Bestand, nicht aus der Anfrage. Die Fold-
		// Quittung trägt confidential nicht; das Zusatzlesen kann scheitern —
		// dann sagt die Antwort es, statt das Feld still wegzulassen (V-5b).
		stand := false
		if wb3, err := a.leseWkBoardStill(r, boardID); err == nil {
			if k, _ := wb3.findeKarte(kartenID); k != nil {
				antwort["vertraulich"] = k.Vertraulich
				stand = true
			}
		}
		if !stand {
			antwort["vertraulich_unbekannt"] = true
		}
	}
	if nr := wkZahl(quittung[0]["nr"]); extra != nil && nr > 0 {
		antwort["finding"] = "F-" + strconv.Itoa(nr)
		antwort["ergebnis"] = fmt.Sprintf("Finding F-%d als Karte #%d angelegt", nr, displayID)
	}
	writeJSON(w, http.StatusOK, antwort)
}

// karteNichtMehrDa: Antwort auf eine Wiederholung mit Idempotency-Key, deren
// Karte inzwischen weg ist (KA-4) — es wird nichts angelegt.
func karteNichtMehrDa(w http.ResponseWriter, boardID, was string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ergebnis":   "Karte war bereits angelegt (Wiederholung erkannt) und wurde " + was + " — nichts neu angelegt. Soll sie neu entstehen, den Aufruf ohne Idempotency-Key oder mit einem neuen Schlüssel wiederholen.",
		"wiederholt": true, "boardId": boardID})
}

// karteSchonAngelegt: Antwort auf eine erkannte Wiederholung von
// karte_anlegen/finding_anlegen (K1-4) — dieselbe Karte, kein Doppel.
func (a *BoardsAPI) karteSchonAngelegt(ctx context.Context, w http.ResponseWriter, boardID string, wb *wkBoard, k *wkKarte, lane *wkSwimlane) {
	antwort := map[string]any{
		"ergebnis":   fmt.Sprintf("Karte #%d war bereits angelegt (Wiederholung erkannt, nichts doppelt angelegt)", k.DisplayID),
		"wiederholt": true,
		"displayId":  k.DisplayID,
		"titel":      k.Titel,
		"swimlane":   swimlaneName(lane),
		"spalte":     wb.spaltenTitel(k.Spalte),
		"boardId":    boardID,
		"link":       a.kartenLink(boardID, k.DisplayID),
	}
	a.vertraulichAntwort(ctx, antwort, k.Vertraulich)
	if k.Kind == "finding" && k.FindingNr > 0 {
		antwort["finding"] = "F-" + strconv.Itoa(k.FindingNr)
		antwort["ergebnis"] = fmt.Sprintf("Finding F-%d war bereits als Karte #%d angelegt (Wiederholung erkannt, nichts doppelt angelegt)", k.FindingNr, k.DisplayID)
	}
	writeJSON(w, http.StatusOK, antwort)
}

// werkzeugKommentieren — POST /werkzeuge/boards/{boardId}/karten/{karte}/kommentare.
// Autor stempelt postOps (agentPayload): `agent:<name>` plus `onBehalfOf` =
// E-Mail der Person. Der Browser zeigt das als „agent:open-webui für <Name>“.
func (a *BoardsAPI) werkzeugKommentieren(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "text")
	if !ok {
		return
	}
	text, grund := textFeldInhalt(m, "text", werkzeugMaxKommentar, true, true)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	// Kritiker Sprint 82, K1-6: Karten des Mail-Eingangs gibt es für die
	// Werkzeuge nicht; KO-4: doppelte Nummer → 400 (findeWerkzeugKarte).
	karte, _ := wb.findeWerkzeugKarte(w, r.PathValue("karte"))
	if karte == nil {
		return
	}
	var fuer any
	if p.OnBehalfOf != "" {
		fuer = p.OnBehalfOf
	}
	antwort := func(ergebnis string, wiederholt bool) {
		m := map[string]any{
			"ergebnis":  ergebnis,
			"displayId": karte.DisplayID,
			"titel":     karte.Titel,
			"autor":     p.Akteur(),
			"fuer":      fuer,
			"boardId":   boardID,
			"link":      a.kartenLink(boardID, karte.DisplayID),
		}
		if wiederholt {
			m["wiederholt"] = true
		}
		writeJSON(w, http.StatusOK, m)
	}
	// Wiederholungsschutz (K1-4): Schlüssel aus der aufgelösten Karte, nicht
	// aus dem Pfad — „5“, „#5“ und die id meinen dieselbe Karte.
	idem, ok := a.werkzeugIdemBeginn(w, r, p, boardID, "kommentieren", karte.ID, text)
	if !ok {
		return
	}
	schonDa := fmt.Sprintf("Kommentar an Karte #%d war bereits geschrieben (Wiederholung erkannt, nichts doppelt geschrieben)", karte.DisplayID)
	if kommentarVorhanden(karte, idem.objektID) {
		antwort(schonDa, true)
		return
	}
	// F-1575: Kennt der Ledger die Op, der Kommentar steht aber nicht (mehr) auf
	// der Karte, wurde er im Fenster gelöscht — das ist ein neuer Wunsch, kein
	// Doppel. Dann einmal mit erneuerten Ids schreiben (wie karte_anlegen, V1-4).
	for versuch := 0; versuch < 2; versuch++ {
		quittung, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{
			"id":   idem.clientID,
			"type": "comment.add",
			"payload": map[string]any{"cardId": karte.ID, "comment": map[string]any{
				"id": idem.objektID, "text": text, "created": idem.datum}},
		}})
		if !ok {
			return
		}
		if !jsWahr(quittung[0]["wiederholt"]) {
			antwort(fmt.Sprintf("Kommentar an Karte #%d geschrieben", karte.DisplayID), false)
			return
		}
		if versuch == 1 {
			break
		}
		wb2 := a.ladeWkBoard(w, r, boardID)
		if wb2 == nil {
			return
		}
		k2, _ := wb2.findeKarte(karte.ID)
		if k2 == nil {
			// KA-8: Die Karte ist inzwischen gelöscht — nicht „bereits geschrieben“.
			writeJSON(w, http.StatusOK, map[string]any{
				"ergebnis":   fmt.Sprintf("nicht geschrieben: Karte #%d ist inzwischen gelöscht", karte.DisplayID),
				"wiederholt": true, "boardId": boardID})
			return
		}
		if kommentarVorhanden(k2, idem.objektID) {
			break
		}
		if idem.mitKopf {
			// KA-4: gleicher Schlüssel = gleiche Anfrage; der Kommentar wurde
			// inzwischen gelöscht — nichts neu schreiben.
			antwort(fmt.Sprintf("Kommentar an Karte #%d war bereits geschrieben und wurde inzwischen gelöscht (Wiederholung erkannt, nichts neu geschrieben)", karte.DisplayID), true)
			return
		}
		idem = a.werkzeugIdemErneuern(idem)
	}
	antwort(schonDa, true)
}

// kommentarVorhanden: trägt die Karte schon einen Kommentar mit dieser id?
func kommentarVorhanden(k *wkKarte, id string) bool {
	var kommentare []map[string]json.RawMessage
	_ = json.Unmarshal(k.Roh["comments"], &kommentare)
	for _, c := range kommentare {
		if c != nil && zeichenketteVon(c["id"]) == id {
			return true
		}
	}
	return false
}

// faelligMitUhrzeit: Fälligkeit für die Werkzeug-Antworten, mit Uhrzeit, wenn
// eine gültige gesetzt ist (ADR-0138 §1) — „2026-10-08 14:00“.
func faelligMitUhrzeit(k map[string]json.RawMessage) string {
	return fristMitUhrzeit(k, "dueDate", "dueTime")
}

// fristMitUhrzeit: Datum und, wenn gültig gesetzt, Uhrzeit eines
// Kartenfelds-Paars (dueDate/dueTime, startDate/startTime).
func fristMitUhrzeit(k map[string]json.RawMessage, datumFeld, zeitFeld string) string {
	datum := zeichenketteVon(k[datumFeld])
	if zeit := zeichenketteVon(k[zeitFeld]); datum != "" && calendar.IstUhrzeit(zeit) {
		return datum + " " + zeit
	}
	return datum
}
