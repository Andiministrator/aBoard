package api

// vorschlaege.go — Rolle „interner Agent“ auf dem externen Server des
// Board-Paars (ADR-0140 §8, Karte #783, Vertrag Push-Skript 3a/9.6).
//
// Der interne Agent (Open-WebUI-Assistent) erreicht das externe Board über ein Relais bei
// dem Partnerprojekt mit einem abt_-Token der Rolle `intern`. Er hat genau fünf Wege, alle
// am Agenten-Eingang (agentDarf, internDarf):
//
//	GET  /intern/{board}/board               Board lesen (wie board_lesen, mit Schatten)
//	GET  /intern/{board}/karte/{nr}          eine Karte (12 oder i-12; wie karte_zeigen)
//	GET  /intern/{board}/suche?q=            Suche, q höchstens 100 Zeichen (wie karten_suchen)
//	POST /intern/{board}/karte/{nr}/felder   SOFORT wirksam, nur spalte und prioritaet
//	                                         einer bestehenden externen Karte (30 je Stunde und Board)
//	POST /intern/{board}/vorschlag           alles andere als Vorschlag
//
// Ein Vorschlag liegt in der Tabelle vorschlaege, bis ein Mensch ihn
// bestätigt oder ablehnt oder er nach 24 h abläuft — dann wird er hart
// gelöscht. Offene Vorschläge sieht kein Agent; nur die Menschen-Kette hat
//
//	GET    /boards/{id}/vorschlaege                      (Rolle editor)
//	POST   /boards/{id}/vorschlaege/{vid}/bestaetigen    {hash} (Rolle editor, Menschen-Sitzung)
//	DELETE /boards/{id}/vorschlaege/{vid}                ablehnen (Rolle editor, Menschen-Sitzung)
//
// Bestätigt wird nie über ein Werkzeug (Vertrag Push-Skript, 3a): Der Bestätigungsweg liegt
// unter /boards/…, und die Positivliste des Agenten-Eingangs kennt ihn nicht.
// Ausgeführt wird ein bestätigter Vorschlag durch genau das Werkzeug, das ein
// Agent sonst selbst aufriefe (karte_anlegen, karte_aendern, kommentieren,
// archivieren, Löschen), mit einem synthetischen Agenten-Principal im Kontext:
// Name = Token-Name, Rolle intern, ohne Person (ADR-0140 §8, V-1). Damit gelten
// alle Agenten-Regeln des Schreibwegs (Autor agent:<name>, keine authorEmail,
// Agenten-Marken, „vertraulich“ schaltet kein Agent ab), und das Protokoll
// trägt die Ops mit actor_kind 'agent'. Wer bestätigt hat, steht in einem
// zusätzlichen Protokolleintrag.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

const (
	// internSegment: erstes Pfadsegment der Wege des internen Agenten.
	internSegment = "intern"
	// internBremseMax je internBremseFenster und Board: sofort wirksame
	// Feldänderungen (Vertrag Push-Skript 9.6, ADR-0140 §8). Jede geänderte Eigenschaft
	// zählt einzeln (Vertrag Push-Skript, T14: „31. Feldänderung in einer Stunde → gebremst“).
	internBremseMax     = 30
	internBremseFenster = time.Hour
	// internSucheMax: Länge des Suchtexts (Vertrag Push-Skript 9.4 (4)).
	internSucheMax = 100
	// Textgrenzen der Vorschläge (ADR-0140 §8).
	vorschlagTitelMax        = 200
	vorschlagBeschreibungMax = 8000
	vorschlagKommentarMax    = 4000
	vorschlagFuerMax         = 80
	// vorschlagBodyMax: Größe einer Vorschlags-Anfrage.
	vorschlagBodyMax = 64 << 10
	// vorschlagAufraeumTakt: so oft räumt der Server abgelaufene Vorschläge
	// ab (VorschlaegeAufraeumen, aus main.go gestartet).
	vorschlagAufraeumTakt = 10 * time.Minute
)

// vorschlagArtText: Anzeige der Arten im Board.
var vorschlagArtText = map[string]string{
	storage.VorschlagKarteAnlegen:     "Neue externe Karte",
	storage.VorschlagKarteText:        "Text einer Karte ändern",
	storage.VorschlagKarteBearbeiter:  "Bearbeiter ändern",
	storage.VorschlagKommentar:        "Kommentar",
	storage.VorschlagKarteLoeschen:    "Karte löschen",
	storage.VorschlagKarteArchivieren: "Karte archivieren",
}

// vorschlagFelder: die Felder einer Vorschlags-Anfrage je Art (zusätzlich zu
// art, fuer und — bei Arten mit Ziel-Karte — karte). Was nicht hier steht,
// wird abgelehnt. Bewusst NICHT vorschlagbar (Vertrag Push-Skript, T14, ADR-0140 §8):
// Anhänge, Umstufen, Labels, Swimlanes, Termine ohne Karte. Bei
// karte.anlegen fehlen owner (Bearbeiter sind eine eigene Art) und
// prioritaet (sofort wirksam über …/felder, sobald die Karte steht).
var vorschlagFelder = map[string][]string{
	storage.VorschlagKarteAnlegen:     {"titel", "beschreibung", "spalte", "faellig", "faelligUhrzeit"},
	storage.VorschlagKarteText:        {"titel", "beschreibung"},
	storage.VorschlagKarteBearbeiter:  {"owner", "beteiligte"},
	storage.VorschlagKommentar:        {"text"},
	storage.VorschlagKarteLoeschen:    {},
	storage.VorschlagKarteArchivieren: {},
}

// vorschlagMitKarte: Arten, die eine bestehende externe Karte betreffen.
func vorschlagMitKarte(art string) bool { return art != storage.VorschlagKarteAnlegen }

// --- Bremse für sofort wirksame Änderungen ----------------------------------

// internBremse zählt sofort wirksame Feldänderungen je Board in einem festen
// Fenster. Im Speicher: Ein Neustart setzt sie zurück (wie die übrigen
// Ratenbegrenzer des Servers).
type internBremse struct {
	mu      sync.Mutex
	eimer   map[string]*bucket
	jetzt   func() time.Time
	max     int
	fenster time.Duration
}

func neueInternBremse() *internBremse {
	return &internBremse{eimer: map[string]*bucket{}, jetzt: time.Now, max: internBremseMax, fenster: internBremseFenster}
}

// nimm reserviert n Änderungen für das Board. false: Grenze erreicht, mit der
// Wartezeit bis zum neuen Fenster.
func (b *internBremse) nimm(board string, n int) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	jetzt := b.jetzt()
	for k, e := range b.eimer {
		if jetzt.Sub(e.since) >= b.fenster {
			delete(b.eimer, k)
		}
	}
	e, ok := b.eimer[board]
	if !ok {
		e = &bucket{since: jetzt}
		b.eimer[board] = e
	}
	if e.count+n > b.max {
		return false, b.fenster - jetzt.Sub(e.since)
	}
	e.count += n
	return true, 0
}

// gib nimmt eine Reservierung zurück (Schreiben abgelehnt, nichts geändert).
func (b *internBremse) gib(board string, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.eimer[board]; ok {
		e.count -= n
		if e.count < 0 {
			e.count = 0
		}
	}
}

func (a *BoardsAPI) internBremseHolen() *internBremse {
	a.internBremseInit.Do(func() {
		if a.internBremse == nil {
			a.internBremse = neueInternBremse()
		}
	})
	return a.internBremse
}

// --- Positivliste ----------------------------------------------------------

// istInternRolle: Der Principal trägt die Rolle intern (wirksam oder als
// Token-Rolle — wie bei vorschlag beide geprüft, Klasse 183).
func istInternRolle(p *AgentPrincipal) bool {
	return p != nil && (p.Role == storage.AgentRolleIntern || p.tokenRolle == storage.AgentRolleIntern)
}

// internDarf ist die abschliessende Liste der Wege der Rolle intern
// (ADR-0140 §8, Stelle 1). Aufgerufen ganz am Anfang von agentDarf, vor jeder
// anderen Prüfung: Sonst erbte die Rolle Blob-Lesen, Ereignisse, Anhänge oder
// Werkzeuge. Mehr-Board- und delegierende Principals gibt es für die Rolle
// nicht; jeder andere Pfad ist verboten.
func internDarf(p *AgentPrincipal, methode string, segs []string, ok bool) bool {
	if !ok || p.Delegiert || p.MehrBoard || p.BoardID == "" || len(segs) < 3 || segs[0] != internSegment || segs[1] != p.BoardID {
		return false
	}
	rest := segs[2:]
	switch {
	case len(rest) == 1 && (rest[0] == "board" || rest[0] == "suche"):
		return methode == http.MethodGet
	case len(rest) == 2 && rest[0] == "karte":
		return methode == http.MethodGet
	case len(rest) == 3 && rest[0] == "karte" && rest[2] == "felder":
		return methode == http.MethodPost
	case len(rest) == 1 && rest[0] == "vorschlag":
		return methode == http.MethodPost
	}
	return false
}

// RegisterVorschlaege hängt die Wege des internen Agenten und die
// Menschen-Wege der Vorschläge an den Mux. Der Mux ist derselbe für Menschen
// und Agenten: Die /intern/…-Wege antworten ohne Agenten-Principal der Rolle
// intern mit 403 (Stelle 4), die /boards/…/vorschlaege-Wege ohne
// Menschen-Sitzung mit 403.
func (a *BoardsAPI) RegisterVorschlaege(mux *http.ServeMux) {
	mux.Handle("GET /"+internSegment+"/{board}/board", WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.internBoard))))
	mux.Handle("GET /"+internSegment+"/{board}/karte/{nr}", WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.internKarte))))
	mux.Handle("GET /"+internSegment+"/{board}/suche", WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.internSuche))))
	mux.Handle("POST /"+internSegment+"/{board}/karte/{nr}/felder", WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.internFelder))))
	mux.Handle("POST /"+internSegment+"/{board}/vorschlag", WithDefaults(a.mitPaarSeite(http.HandlerFunc(a.internVorschlag))))
	mux.Handle("GET /boards/{id}/vorschlaege", WithDefaults(http.HandlerFunc(a.vorschlaegeListe)))
	mux.Handle("POST /boards/{id}/vorschlaege/{vid}/bestaetigen", WithDefaults(http.HandlerFunc(a.vorschlagBestaetigen)))
	mux.Handle("DELETE /boards/{id}/vorschlaege/{vid}", WithDefaults(http.HandlerFunc(a.vorschlagAblehnen)))
}

// InternRolleAnlegbar: Ein Token der Rolle intern gibt es nur auf der Seite
// außen (ADR-0140 §8). Leer = erlaubt; sonst Grund und HTTP-Status (400 bei
// anderer Seite, 503 bei nicht lesbarer Einstellung). Für Admin-API und CLI.
func InternRolleAnlegbar(ctx context.Context, cfg *storage.ConfigRepo) (string, int) {
	if cfg == nil {
		return "die rolle intern gibt es nur auf dem externen server eines board-paars (boardpaar.seite = aussen)", http.StatusBadRequest
	}
	seite, err := BoardpaarSeiteMitFehler(ctx, cfg)
	if err != nil {
		return "die einstellung boardpaar.seite ist gerade nicht lesbar", http.StatusServiceUnavailable
	}
	if seite != BoardpaarSeiteAussen {
		return "die rolle intern gibt es nur auf dem externen server eines board-paars (boardpaar.seite = aussen)", http.StatusBadRequest
	}
	return "", 0
}

// internZugang prüft einen Weg des internen Agenten: Principal mit Rolle
// intern, eigenes Board, Seite außen, Board gekoppelt. Liefert Principal und
// Board; nil heisst, die Antwort ist geschrieben.
func (a *BoardsAPI) internZugang(w http.ResponseWriter, r *http.Request) (*AgentPrincipal, string) {
	p := AgentFromContext(r.Context())
	if !istInternRolle(p) || p.Delegiert || p.MehrBoard {
		// Auch an der Menschen-Kette erreichbar (gemeinsamer Mux): dort ohne
		// Principal — 403, nicht 404, wie es ADR-0140 §8 Stelle 4 festlegt.
		writeError(w, http.StatusForbidden, "only for the internal agent (role intern)")
		return nil, ""
	}
	board := r.PathValue("board")
	if board == "" || board != p.BoardID {
		writeError(w, http.StatusForbidden, verbotenMeldungFuer(p))
		return nil, ""
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	seite, err := a.paarSeiteMitFehler(ctx)
	if err != nil {
		log.Printf("intern: boardpaar.seite nicht lesbar, Anfrage abgelehnt: %v", err)
		writeError(w, http.StatusServiceUnavailable, "Die Einstellung des Board-Paars ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.")
		return nil, ""
	}
	if seite != BoardpaarSeiteAussen {
		writeError(w, http.StatusForbidden, "Die Rolle intern gibt es nur auf dem externen Server eines Board-Paars.")
		return nil, ""
	}
	if a.paar == nil {
		writeError(w, http.StatusForbidden, "Dieses Board ist nicht gekoppelt.")
		return nil, ""
	}
	paar, err := a.paar.AussenGekoppelt(ctx, board)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Die Kopplung ist gerade nicht lesbar. Bitte erneut versuchen; nichts geschrieben.", err)
		return nil, ""
	}
	if paar == nil {
		writeError(w, http.StatusForbidden, "Dieses Board ist nicht gekoppelt.")
		return nil, ""
	}
	return p, board
}

// internWeiter ruft einen Lese-Handler der Werkzeuge mit festem Pfad und
// fester Query auf — die Rolle intern bekommt ihre festen Formen (Vertrag Push-Skript 9.4
// (4)), nicht die volle Parameterliste des Werkzeugs.
func internWeiter(w http.ResponseWriter, r *http.Request, boardID string, pfad map[string]string, query url.Values, h http.HandlerFunc) {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = query.Encode()
	r2.URL = &u
	r2.SetPathValue("boardId", boardID)
	for k, v := range pfad {
		r2.SetPathValue(k, v)
	}
	h(w, r2)
}

// internBoard — GET /intern/{board}/board: wie board_lesen (externe Karten
// und Schatten nach §7). Erlaubt sind die Filter swimlane, spalte und
// mitErledigten.
func (a *BoardsAPI) internBoard(w http.ResponseWriter, r *http.Request) {
	_, board := a.internZugang(w, r)
	if board == "" {
		return
	}
	q := url.Values{}
	for _, k := range []string{"swimlane", "spalte", "mitErledigten"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	internWeiter(w, r, board, nil, q, a.werkzeugBoardLesen)
}

// internKarte — GET /intern/{board}/karte/{nr}: wie karte_zeigen, als JSON
// (format=html auf Wunsch). „i-12“ zeigt den Schatten.
func (a *BoardsAPI) internKarte(w http.ResponseWriter, r *http.Request) {
	_, board := a.internZugang(w, r)
	if board == "" {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	internWeiter(w, r, board, map[string]string{"karte": r.PathValue("nr")}, url.Values{"format": {format}}, a.werkzeugKarteZeigen)
}

// internSuche — GET /intern/{board}/suche?q=: wie karten_suchen, nur auf dem
// eigenen Board, q höchstens internSucheMax Zeichen.
func (a *BoardsAPI) internSuche(w http.ResponseWriter, r *http.Request) {
	_, board := a.internZugang(w, r)
	if board == "" {
		return
	}
	such := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(such) > internSucheMax {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("q: höchstens %d Zeichen", internSucheMax))
		return
	}
	q := url.Values{"q": {such}, "boardId": {board}}
	if v := r.URL.Query().Get("mitErledigten"); v != "" {
		q.Set("mitErledigten", v)
	}
	internWeiter(w, r, board, nil, q, a.werkzeugKartenSuchen)
}

// internFelder — POST /intern/{board}/karte/{nr}/felder {spalte?, prioritaet?}:
// sofort wirksam, nur für eine bestehende externe Karte (keine Schatten, keine
// Sprint-Meta-Karte, nicht archiviert). Bearbeiter sind ein Vorschlag (§8).
func (a *BoardsAPI) internFelder(w http.ResponseWriter, r *http.Request) {
	p, board := a.internZugang(w, r)
	if board == "" || !a.werkzeugLesenErlaubt(w, p) { // K-A6: auch Fehlversuche zählen
		return
	}
	m, ok := leseWerkzeugBody(w, r, "spalte", "prioritaet")
	if !ok {
		return
	}
	if !feldDa(m, "spalte") && !feldDa(m, "prioritaet") {
		writeError(w, http.StatusBadRequest, "keine Änderung angegeben — spalte und/oder prioritaet nennen")
		return
	}
	ref := r.PathValue("nr")
	if schattenNurLesbar(w, true, ref) {
		return
	}
	wb := a.ladeWkBoard(w, r, board)
	if wb == nil {
		return
	}
	k, lane := wb.findeWerkzeugKarte(w, ref)
	if k == nil {
		return
	}
	if k.Kind == "sprint-meta" || k.Archiviert || lane.Archiviert {
		writeError(w, http.StatusBadRequest, "Diese Karte lässt sich so nicht ändern (Sprint-Start/-Abschluss oder archiviert).")
		return
	}
	var ops []map[string]any
	var geaendert []string
	if feldDa(m, "spalte") {
		wunsch, grund := textFeld(m, "spalte", maxVorschlagTitel, true, false)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		spalte, gefunden := wb.findeSpalte(wunsch)
		if !gefunden {
			wb.spalteFehlt(w, wunsch)
			return
		}
		if spalte != k.Spalte {
			ops = append(ops, map[string]any{"type": "card.move", "payload": map[string]any{"cardId": k.ID, "toSprintId": lane.ID, "toColId": spalte}})
			geaendert = append(geaendert, "spalte")
		}
	}
	if feldDa(m, "prioritaet") {
		wert, grund := textFeld(m, "prioritaet", 20, false, false)
		if grund == "" && !inListe(wert, prioritaetWerte) {
			grund = fmt.Sprintf("prioritaet muss einer dieser Werte sein: %q", prioritaetWerte)
		}
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		if wert != zeichenketteVon(k.Roh["priority"]) {
			ops = append(ops, feldOp(k.ID, "priority", wert))
			geaendert = append(geaendert, "prioritaet")
		}
	}
	if len(ops) == 0 {
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, board, k, fmt.Sprintf("Karte #%d: nichts zu ändern", k.DisplayID), map[string]any{"geaendert": []string{}}))
		return
	}
	bremse := a.internBremseHolen()
	if frei, warten := bremse.nimm(board, len(ops)); !frei {
		log.Printf("intern: Bremse greift — agent:%s, Board %s, %d Änderung(en) abgelehnt (höchstens %d je Stunde und Board)", p.Name, board, len(ops), internBremseMax)
		w.Header().Set("Retry-After", strconv.Itoa(int(warten.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": fmt.Sprintf(
			"nicht ausgeführt: höchstens %d sofort wirksame Änderungen je Stunde auf diesem Board. Bitte später erneut versuchen oder die Person bitten, es im Board zu tun.", internBremseMax)})
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, board, ops); !ok {
		bremse.gib(board, len(ops))
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	log.Printf("intern: agent:%s hat auf Board %s Karte #%d sofort geändert: %s", p.Name, board, k.DisplayID, strings.Join(geaendert, ", "))
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, board, k, fmt.Sprintf("Karte #%d geändert: %s", k.DisplayID, strings.Join(geaendert, ", ")),
		map[string]any{"geaendert": geaendert}))
}

// --- Vorschlag annehmen ----------------------------------------------------

// unsichtbaresZeichen: das erste Zeichen, das ein Mensch beim Bestätigen
// nicht sieht, das aber gespeichert und von anderen Agenten gelesen würde
// (Kritik K-A1), mit Position (Zeichen, ab 1). ok = false: keins gefunden.
//
// Abgelehnt werden Format-Zeichen (Cf, darunter Bidi-Steuerung, ZWNJ und der
// Tag-Block U+E0000–U+E007F), private und nicht vergebene Codepunkte,
// U+2028/U+2029, Variation Selectors, unsichtbare Füllzeichen (Braille leer,
// Hangul-Füller, CGJ, Khmer-Vokalträger; Verify V-4) und jedes andere nicht
// druckbare Zeichen. Leerzeichen der Kategorie Zs sind erlaubt; Zeilenumbruch
// und Tabulator nur mit mitUmbruch.
//
// Ausnahmen für gewöhnliche Emoji (Verify V-1, enger nach Verify 2 W-1):
// U+FE0F/U+FE0E direkt nach einem Bildzeichen (⚠️, ❤️) oder in einer Keycap-
// Folge Ziffer+VS16+U+20E3 (1️⃣), ZWJ einzeln zwischen zwei Bildzeichen
// (👨‍👩‍👧) — je Feld höchstens 4 Variation Selectors und 7 ZWJ. Der Restkanal
// (ob ein Bildzeichen VS16 trägt, ob zwei Emoji per ZWJ verbunden sind) ist
// damit auf wenige Bits je Feld begrenzt, nicht geschlossen (ADR-0140).
// ZWJ und ZWNJ zwischen Buchstaben bleiben abgelehnt.
//
// Abgelehnt statt still entfernt — sonst wiche das Gespeicherte von dem ab,
// was der Agent schickte.
func unsichtbaresZeichen(s string, mitUmbruch bool) (rune, int, bool) {
	rs := []rune(s)
	// emoji: Bildzeichen-Blöcke, keine beliebigen Symbole (°, ©) — Verify 2 W-1.
	emoji := func(r rune) bool {
		return unicode.Is(unicode.So, r) && (r >= 0x1F000 || (r >= 0x2190 && r <= 0x2BFF)) || (r >= 0x1F3FB && r <= 0x1F3FF)
	}
	vs, zwj := 0, 0 // höchstens vsMax/zwjMax je Feld: begrenzt den Restkanal (Verify 2 W-1)
	const vsMax, zwjMax = 4, 7
	for i, r := range rs {
		vorher := rune(0)
		if i > 0 {
			vorher = rs[i-1]
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			if !mitUmbruch {
				return r, i + 1, true
			}
		case r == 0xFE0F || r == 0xFE0E:
			keycap := (vorher >= '0' && vorher <= '9' || vorher == '#' || vorher == '*') && i+1 < len(rs) && rs[i+1] == 0x20E3
			if vs++; vs > vsMax || !(emoji(vorher) || keycap) {
				return r, i + 1, true
			}
		case r == 0x200D:
			basis := vorher
			if (basis == 0xFE0F || (basis >= 0x1F3FB && basis <= 0x1F3FF)) && i > 1 {
				basis = rs[i-2]
			}
			if zwj++; zwj > zwjMax || !emoji(basis) || i+1 >= len(rs) || !emoji(rs[i+1]) {
				return r, i + 1, true
			}
		case unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
			// Alle übrigen Variation Selectors (auch die mongolischen U+180B–U+180F, die
			// IsPrint durchlässt) und alles Default-Ignorable: unsichtbar, also ein
			// Restkanal ohne Grenze (Sprint-91-Kritiker K-S1). FE0E/FE0F stehen oben.
			return r, i + 1, true
		case r == 0x2800 || r == 0x3164 || r == 0xFFA0 || r == 0x115F || r == 0x1160 || r == 0x034F || r == 0x17B4 || r == 0x17B5:
			return r, i + 1, true
		case unicode.Is(unicode.Zs, r):
		case !unicode.IsPrint(r): // Cc, Cf, Co, Cn, Zl, Zp
			return r, i + 1, true
		}
	}
	return 0, 0, false
}

// hatUnsichtbareZeichen: unsichtbaresZeichen als Ja/Nein.
func hatUnsichtbareZeichen(s string, mitUmbruch bool) bool {
	_, _, ja := unsichtbaresZeichen(s, mitUmbruch)
	return ja
}

// unsichtbarGrund: die Meldung für den Agenten, mit Codepunkt und Position.
func unsichtbarGrund(feld string, r rune, pos int) string {
	return fmt.Sprintf("%s: enthält ein unsichtbares oder nicht darstellbares Zeichen (U+%04X an Stelle %d) — so nicht vorschlagbar", feld, r, pos)
}

// vorschlagText: textFeld plus die Prüfung auf unsichtbare Zeichen.
func vorschlagText(m map[string]json.RawMessage, name string, max int, pflicht, mehrzeilig bool) (string, string) {
	if roh, ok := m[name]; ok {
		var roheZeichen string
		if json.Unmarshal(roh, &roheZeichen) == nil {
			if z, pos, ja := unsichtbaresZeichen(roheZeichen, mehrzeilig); ja {
				return "", unsichtbarGrund(name, z, pos)
			}
		}
	}
	wert, grund := textFeld(m, name, max, pflicht, mehrzeilig)
	// F-2120: Kombinationszeichen (Mn/Mc) passieren unsichtbaresZeichen; ein
	// Pflichtfeld braucht deshalb zusätzlich ein Zeichen aus L, N, P oder S.
	if grund == "" && pflicht && wert != "" && !hatInhaltszeichen(wert) {
		return "", name + ": braucht mindestens einen Buchstaben, eine Ziffer oder ein Satz- oder Sonderzeichen"
	}
	return wert, grund
}

// bearbeiterZulaessig: kein externer Bearbeiter. Eine Zuweisung löst eine
// Benachrichtigung aus; an „external:…“ oder eine Adresse ginge sie als Mail
// an Externe — genau der Weg, wegen dessen Bearbeiter kein sofort wirksames
// Feld sind (ADR-0140 §8, Kritik K-A7).
func bearbeiterZulaessig(s string) bool {
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "external:") && !strings.Contains(s, "@")
}

// protokoll schreibt einen Eintrag ohne Nutzlast mit Akteur-Art ("mensch",
// "agent", "system"). Nicht-kritisch wie recordActivity.
func (a *BoardsAPI) protokoll(ctx context.Context, board, akteur, art, aktion, feld, notiz string) {
	if a.activities == nil {
		return
	}
	if err := a.activities.RecordArt(ctx, board, akteur, art, aktion, feld, "", "", notiz); err != nil {
		log.Printf("aboard-server: activity-log-fehler board=%s action=%s: %v", board, aktion, err)
	}
}

// vorschlagHash: SHA-256 über Art, Ziel-Karte, Nutzlast und „für wen“ — das,
// was der Mensch beim Bestätigen gesehen hat.
func vorschlagHash(art, karteID, nutzlast, fuer string) string {
	roh, _ := json.Marshal([]string{art, karteID, nutzlast, fuer})
	h := sha256.Sum256(roh)
	return hex.EncodeToString(h[:])
}

// leseVorschlagBody liest den Body einer Vorschlags-Anfrage: art zuerst, dann
// genau die Felder dieser Art. Unbekannte Felder führen zur Ablehnung.
func leseVorschlagBody(w http.ResponseWriter, r *http.Request) (string, map[string]json.RawMessage, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, vorschlagBodyMax)
	roh, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Anfrage zu groß (höchstens %d Bytes)", vorschlagBodyMax))
		return "", nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(roh, &m) != nil || m == nil {
		writeError(w, http.StatusBadRequest, "Anfrage muss ein JSON-Objekt mit art sein")
		return "", nil, false
	}
	if k := schreibweisenDoppel(m); k != "" {
		writeError(w, http.StatusBadRequest, "Feld "+k+" kommt in zwei Schreibweisen vor")
		return "", nil, false
	}
	art := zeichenketteVon(m["art"])
	felder, bekannt := vorschlagFelder[art]
	if !bekannt {
		arten := make([]string, 0, len(vorschlagFelder))
		for a := range vorschlagFelder {
			arten = append(arten, a)
		}
		sort.Strings(arten)
		writeError(w, http.StatusBadRequest, "art muss einer dieser Werte sein: "+strings.Join(arten, ", ")+
			" (Anhänge, Umstufen, Labels, Swimlanes und Termine ohne Karte gibt es nicht als Vorschlag)")
		return "", nil, false
	}
	erlaubt := map[string]bool{"art": true, "fuer": true}
	if vorschlagMitKarte(art) {
		erlaubt["karte"] = true
	}
	for _, f := range felder {
		erlaubt[f] = true
	}
	for k := range m {
		if !erlaubt[k] {
			liste := make([]string, 0, len(erlaubt))
			for _, f := range append([]string{"art", "fuer", "karte"}, felder...) {
				if erlaubt[f] {
					liste = append(liste, f)
				}
			}
			writeError(w, http.StatusBadRequest, "unbekanntes Feld "+strconv.Quote(k)+" für "+art+" — erlaubt sind nur: "+strings.Join(liste, ", "))
			return "", nil, false
		}
	}
	return art, m, true
}

// vorschlagTitel: Titel eines Vorschlags — höchstens vorschlagTitelMax
// Zeichen, ohne Steuerzeichen, dann die Titelbehandlung aus ADR-0140 §2
// (schatten.TitelPruefen). Greift dort ein Muster verpackter Daten, wird der
// Vorschlag abgelehnt statt den Titel zu ersetzen.
func vorschlagTitel(m map[string]json.RawMessage) (string, string) {
	titel, grund := vorschlagText(m, "titel", vorschlagTitelMax, true, false)
	if grund != "" {
		return "", grund
	}
	behandelt, ok := schatten.TitelPruefen(titel, vorschlagTitelMax)
	if !ok {
		return "", "titel: sieht nach verpackten Daten aus (Adresse, „@“, lange Ziffern- oder Zeichenfolge ohne Leerzeichen) oder enthält nach dem Filtern nichts — so nicht vorschlagbar"
	}
	return behandelt, ""
}

// baueVorschlagNutzlast prüft die Felder einer Art und baut daraus den Body
// des Werkzeugs, das die Bestätigung ausführt. k ist die Ziel-Karte (nil bei
// karte.anlegen).
func baueVorschlagNutzlast(art string, m map[string]json.RawMessage, wb *wkBoard, k *wkKarte) (map[string]any, string) {
	n := map[string]any{}
	switch art {
	case storage.VorschlagKarteAnlegen:
		titel, grund := vorschlagTitel(m)
		if grund != "" {
			return nil, grund
		}
		n["titel"] = titel
		beschreibung, grund := vorschlagText(m, "beschreibung", vorschlagBeschreibungMax, false, true)
		if grund != "" {
			return nil, grund
		}
		if beschreibung != "" {
			n["beschreibung"] = beschreibung
		}
		if feldGesetzt(m, "spalte") {
			wunsch, grund := textFeld(m, "spalte", maxVorschlagTitel, false, false)
			if grund != "" {
				return nil, grund
			}
			spalte, gefunden := wb.findeSpalte(wunsch)
			if !gefunden {
				return nil, "spalte " + strconv.Quote(wunsch) + " gibt es auf diesem Board nicht"
			}
			n["spalte"] = spalte
		}
		datum, grund := leseDatumFeld(m, "faellig")
		if grund != "" {
			return nil, grund
		}
		zeit, grund := leseUhrzeitFeld(m, "faelligUhrzeit")
		if grund != "" {
			return nil, grund
		}
		if zeit != "" && datum == "" {
			return nil, "faelligUhrzeit nur zusammen mit faellig"
		}
		if datum != "" {
			n["faellig"] = datum
		}
		if zeit != "" {
			n["faelligUhrzeit"] = zeit
		}
	case storage.VorschlagKarteText:
		if !feldDa(m, "titel") && !feldDa(m, "beschreibung") {
			return nil, "titel und/oder beschreibung nennen"
		}
		n["stand"] = wkStand(k)
		if feldDa(m, "titel") {
			titel, grund := vorschlagTitel(m)
			if grund != "" {
				return nil, grund
			}
			n["titel"] = titel
		}
		if feldDa(m, "beschreibung") {
			beschreibung, grund := vorschlagText(m, "beschreibung", vorschlagBeschreibungMax, false, true)
			if grund != "" {
				return nil, grund
			}
			n["beschreibung"] = beschreibung
		}
	case storage.VorschlagKarteBearbeiter:
		if !feldDa(m, "owner") && !feldDa(m, "beteiligte") {
			return nil, "owner und/oder beteiligte nennen"
		}
		if feldDa(m, "owner") {
			owner, grund := vorschlagText(m, "owner", werkzeugMaxOwner, false, false)
			if grund == "" && owner != "" && !bearbeiterZulaessig(owner) {
				grund = "owner: externe Bearbeiter (external:… oder eine Adresse) gibt es nicht als Vorschlag"
			}
			if grund != "" {
				return nil, grund
			}
			n["owner"] = owner
		}
		if feldDa(m, "beteiligte") {
			liste, _, grund := leseTextListe(m, "beteiligte", werkzeugMaxOwner)
			var roh []string
			_ = json.Unmarshal(m["beteiligte"], &roh) // geprüft wird der Rohwert, vor dem Trimmen
			for _, b := range roh {
				if z, pos, ja := unsichtbaresZeichen(b, false); grund == "" && ja {
					grund = unsichtbarGrund("beteiligte", z, pos)
				}
			}
			for _, b := range liste {
				if grund == "" && !bearbeiterZulaessig(b) {
					grund = "beteiligte: externe Bearbeiter (external:… oder eine Adresse) gibt es nicht als Vorschlag"
				}
			}
			if grund != "" {
				return nil, grund
			}
			if liste == nil {
				liste = []string{}
			}
			n["beteiligte"] = liste
		}
	case storage.VorschlagKommentar:
		text, grund := vorschlagText(m, "text", vorschlagKommentarMax, true, true)
		if grund != "" {
			return nil, grund
		}
		n["text"] = text
	case storage.VorschlagKarteLoeschen:
		// Der Stand der Karte beim Vorschlag: Hat sie sich bis zur Bestätigung
		// geändert, löscht die Bestätigung nicht (vorschlagLoeschenAusfuehren).
		kRoh, _ := json.Marshal(k.Roh)
		n["stand"] = standKennung(json.RawMessage(kRoh))
	case storage.VorschlagKarteArchivieren:
	}
	return n, ""
}

// internVorschlag — POST /intern/{board}/vorschlag {art, fuer?, karte?, …}.
func (a *BoardsAPI) internVorschlag(w http.ResponseWriter, r *http.Request) {
	p, board := a.internZugang(w, r)
	if board == "" || !a.werkzeugLesenErlaubt(w, p) { // K-A6: auch Fehlversuche zählen
		return
	}
	art, m, ok := leseVorschlagBody(w, r)
	if !ok {
		return
	}
	fuer, grund := vorschlagText(m, "fuer", vorschlagFuerMax, false, false)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	wb := a.ladeWkBoard(w, r, board)
	if wb == nil {
		return
	}
	var k *wkKarte
	if vorschlagMitKarte(art) {
		ref, grund := textFeld(m, "karte", 100, true, false)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund+" (Nummer der externen Karte)")
			return
		}
		var lane *wkSwimlane
		if k, lane = wb.findeWerkzeugKarte(w, ref); k == nil {
			return
		}
		if k.Kind == "sprint-meta" {
			writeError(w, http.StatusBadRequest, "Sprint-Start- und Sprint-Abschluss-Karten gibt es nicht als Ziel eines Vorschlags.")
			return
		}
		if art != storage.VorschlagKarteLoeschen && (k.Archiviert || lane.Archiviert) {
			writeError(w, http.StatusBadRequest, "Die Karte ist archiviert.")
			return
		}
	}
	nutzlast, grund := baueVorschlagNutzlast(art, m, wb, k)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	roh, err := json.Marshal(nutzlast)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion encode failed", err)
		return
	}
	karteID := ""
	if k != nil {
		karteID = k.ID
	}
	v := &storage.Vorschlag{ID: uuid.NewString(), BoardID: board, TokenID: p.TokenID, TokenName: p.Name, Art: art,
		KarteID: karteID, Nutzlast: string(roh), Fuer: fuer}
	v.Hash = vorschlagHash(art, karteID, v.Nutzlast, fuer)
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	if alt, err := a.repo.Vorschlaege().Anlegen(ctx, v); errors.Is(err, storage.ErrVorschlagDoppelt) {
		// Wiederholung derselben Anfrage: kein zweiter Vorschlag.
		writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "Dieser Vorschlag liegt schon vor und wartet auf die Bestätigung durch einen Menschen im Board.",
			"laeuftAb": alt.LaeuftAb, "wiederholt": true})
		return
	} else if errors.Is(err, storage.ErrVorschlagGrenze) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": fmt.Sprintf(
			"nicht angelegt: Es liegen schon %d offene Vorschläge dieses Zugangs vor. Erst wenn ein Mensch welche bestätigt oder ablehnt (oder sie nach 24 Stunden ablaufen), geht ein neuer.", storage.VorschlaegeJeTokenMax)})
		return
	} else if errors.Is(err, storage.ErrVorschlagNochmal) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "nicht angelegt: gerade gleichzeitig geändert — bitte gleich erneut senden")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion not stored", err)
		return
	}
	a.protokoll(context.WithoutCancel(r.Context()), board, p.Akteur(), "agent", "vorschlag-angelegt", art, "Vorschlag "+v.ID)
	log.Printf("intern: agent:%s hat auf Board %s einen Vorschlag angelegt (%s, %s)", p.Name, board, art, v.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ergebnis": "Vorschlag angelegt. Er wirkt erst, wenn ein Mensch ihn im Board bestätigt; bis dahin sieht ihn kein Agent. Sag der Person, dass im externen Board ein Vorschlag auf sie wartet.",
		"laeuftAb": v.LaeuftAb,
	})
}

// --- Menschen-Wege ---------------------------------------------------------

// vorschlagMensch prüft einen Menschen-Weg der Vorschläge: kein Agent, eine
// Menschen-Sitzung (Forward-Auth-Identität, kein Legacy-Bypass), Rolle editor
// oder mehr am Board, Seite außen. Liefert Board und E-Mail; leer heisst, die
// Antwort ist geschrieben.
func (a *BoardsAPI) vorschlagMensch(w http.ResponseWriter, r *http.Request) (string, string) {
	if AgentFromContext(r.Context()) != nil {
		writeError(w, http.StatusForbidden, "forbidden for agent")
		return "", ""
	}
	id := IdentityFromContext(r.Context())
	email := ""
	if id != nil {
		email = storage.NormalizeEmail(id.Email)
	}
	if email == "" {
		writeError(w, http.StatusForbidden, "Vorschläge sieht und bestätigt nur eine angemeldete Person.")
		return "", ""
	}
	board := r.PathValue("id")
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	if a.acl == nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", ""
	}
	rolle, gefunden, err := a.acl.GetRole(ctx, board, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return "", ""
	}
	if !gefunden || roleRank[rolle] < roleRank[storage.RoleEditor] {
		writeError(w, http.StatusForbidden, "Vorschläge sieht und bestätigt nur, wer das Board bearbeiten darf (Rolle editor).")
		return "", ""
	}
	seite, err := a.paarSeiteMitFehler(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Die Einstellung des Board-Paars ist gerade nicht lesbar.", err)
		return "", ""
	}
	if seite != BoardpaarSeiteAussen {
		writeError(w, http.StatusNotFound, "not found")
		return "", ""
	}
	return board, email
}

// vorschlagFeld: ein Feld der Anzeige, als reiner Text (kein Markdown, kein
// HTML — die Oberfläche setzt textContent, Vertrag Push-Skript K-3).
type vorschlagFeld struct {
	// Schluessel: fester Name des Felds für die Übersetzung in der Oberfläche.
	Schluessel string `json:"schluessel"`
	Name       string `json:"name"`
	Wert       string `json:"wert"`
}

type vorschlagKarteAnzeige struct {
	DisplayID int    `json:"displayId"`
	Titel     string `json:"titel"`
}

type vorschlagAnzeige struct {
	ID       string                 `json:"id"`
	Art      string                 `json:"art"`
	ArtText  string                 `json:"artText"`
	Agent    string                 `json:"agent"`
	Fuer     string                 `json:"fuer"`
	Angelegt string                 `json:"angelegt"`
	LaeuftAb string                 `json:"laeuftAb"`
	Hash     string                 `json:"hash"`
	Karte    *vorschlagKarteAnzeige `json:"karte"`
	// KarteFehlt: Die Ziel-Karte gibt es nicht mehr — bestätigen schlägt dann fehl.
	KarteFehlt bool `json:"karteFehlt"`
	// ZugangAktiv: Das Token hinter dem Vorschlag gilt noch. Sonst schlägt
	// bestätigen fehl (409) — die Liste sagt es vorher (Kritik K-A9).
	ZugangAktiv bool            `json:"zugangAktiv"`
	Felder      []vorschlagFeld `json:"felder"`
}

// vorschlagFelderAnzeige: die Nutzlast als Liste lesbarer Felder.
func vorschlagFelderAnzeige(v storage.Vorschlag, wb *wkBoard) []vorschlagFeld {
	var n map[string]json.RawMessage
	_ = json.Unmarshal([]byte(v.Nutzlast), &n)
	felder := []vorschlagFeld{}
	text := func(name, schluessel string) {
		if roh, ok := n[schluessel]; ok {
			felder = append(felder, vorschlagFeld{schluessel, name, leerAnzeige(zeichenketteVon(roh))})
		}
	}
	switch v.Art {
	case storage.VorschlagKarteAnlegen:
		text("Titel", "titel")
		text("Beschreibung", "beschreibung")
		if roh, ok := n["spalte"]; ok {
			felder = append(felder, vorschlagFeld{"spalte", "Spalte", wb.spaltenTitel(zeichenketteVon(roh))})
		}
		text("Fällig", "faellig")
		text("Uhrzeit", "faelligUhrzeit")
	case storage.VorschlagKarteText:
		text("Neuer Titel", "titel")
		text("Neue Beschreibung", "beschreibung")
	case storage.VorschlagKarteBearbeiter:
		// F-2093: Der Vorschlag ersetzt Owner und Beteiligte ganz, ohne den Stand
		// beim Vorschlagen zu prüfen. Wer zwischen Vorschlag und Bestätigung
		// ergänzt wurde, fiele still weg — deshalb zeigt die Liste den heutigen
		// Wert neben dem neuen, sobald sie sich unterscheiden.
		var k *wkKarte
		if v.KarteID != "" {
			k = karteExakt(wb, v.KarteID)
		}
		if roh, ok := n["owner"]; ok {
			neu := zeichenketteVon(roh)
			wert := leerAnzeige(neu)
			if k != nil && k.Owner != neu {
				wert = leerAnzeige(k.Owner) + " → " + wert
			}
			felder = append(felder, vorschlagFeld{"owner", "Owner", wert})
		}
		if roh, ok := n["beteiligte"]; ok {
			neu := zeichenkettenListe(roh)
			wert := listenAnzeige(neu)
			if k != nil {
				if alt := zeichenkettenListe(k.Roh["assignees"]); !slices.Equal(alt, neu) {
					wert = listenAnzeige(alt) + " → " + wert
				}
			}
			felder = append(felder, vorschlagFeld{"beteiligte", "Beteiligte", wert})
		}
	case storage.VorschlagKommentar:
		text("Kommentar", "text")
	case storage.VorschlagKarteLoeschen:
		felder = append(felder, vorschlagFeld{"wirkung-loeschen", "Wirkung", "Die Karte wird mit Kommentaren und Anhängen gelöscht."})
	case storage.VorschlagKarteArchivieren:
		felder = append(felder, vorschlagFeld{"wirkung-archivieren", "Wirkung", "Die Karte wird archiviert."})
	}
	return felder
}

// leerAnzeige: ein geleertes Feld erscheint als „(leer)“, nicht als leere
// Zeile, die wie ein Anzeigefehler aussieht (F-2093).
func leerAnzeige(s string) string {
	if s == "" {
		return "(leer)"
	}
	return s
}

func listenAnzeige(l []string) string {
	if len(l) == 0 {
		return "(niemand)"
	}
	return strings.Join(l, ", ")
}

// vorschlaegeListe — GET /boards/{id}/vorschlaege.
func (a *BoardsAPI) vorschlaegeListe(w http.ResponseWriter, r *http.Request) {
	board, _ := a.vorschlagMensch(w, r)
	if board == "" {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// F-2096: Kein Aufräumen beim Lesen. Das Band fragt je Editor minütlich;
	// jeder Abruf schrieb so ein DELETE über alle Boards. Abgelaufene blendet
	// Offene beim Lesen aus, gelöscht wird im Takt (StarteVorschlagAufraeumen).
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	liste, err := a.repo.Vorschlaege().Offene(ctx, board)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestions unreadable", err)
		return
	}
	b, err := a.repo.Get(ctx, board)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	wb, err := leseWkBoard(b.StateJSON)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	aus := make([]vorschlagAnzeige, 0, len(liste))
	aktiv := map[string]bool{}
	for _, v := range liste {
		if _, gesehen := aktiv[v.TokenID]; !gesehen {
			ok, err := a.repo.AgentTokens().IstAktiv(ctx, v.TokenID)
			aktiv[v.TokenID] = err == nil && ok
		}
		e := vorschlagAnzeige{ID: v.ID, Art: v.Art, ArtText: vorschlagArtText[v.Art], Agent: "agent:" + v.TokenName, Fuer: v.Fuer,
			Angelegt: v.Angelegt, LaeuftAb: v.LaeuftAb, Hash: v.Hash, Felder: vorschlagFelderAnzeige(v, wb), ZugangAktiv: aktiv[v.TokenID]}
		if v.KarteID != "" {
			if k := karteExakt(wb, v.KarteID); k != nil {
				e.Karte = &vorschlagKarteAnzeige{DisplayID: k.DisplayID, Titel: k.Titel}
			} else {
				e.KarteFehlt = true
			}
		}
		aus = append(aus, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"vorschlaege": aus})
}

// vorschlagBestaetigerSchluessel: Kontext-Schlüssel für die E-Mail des
// Menschen, der einen Vorschlag bestätigt. postOps zählt die Ausführung dann
// im Eimer dieses Menschen, nicht in dem des Agenten (ADR-0140 §8: „Der
// Ratenbegrenzer der Agenten zählt die Ausführung nicht“). Unexportiert, von
// außen nicht zu setzen.
type vorschlagBestaetigerSchluessel struct{}

func vorschlagBestaetigerAus(ctx context.Context) string {
	s, _ := ctx.Value(vorschlagBestaetigerSchluessel{}).(string)
	return s
}

// vorschlagBestaetigen — POST /boards/{id}/vorschlaege/{vid}/bestaetigen {hash}.
func (a *BoardsAPI) vorschlagBestaetigen(w http.ResponseWriter, r *http.Request) {
	board, email := a.vorschlagMensch(w, r)
	if board == "" {
		return
	}
	m, ok := leseWerkzeugBody(w, r, "hash")
	if !ok {
		return
	}
	hash, grund := textFeld(m, "hash", 128, true, false)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	repo := a.repo.Vorschlaege()
	v, err := repo.Hole(ctx, board, r.PathValue("vid"))
	if errors.Is(err, storage.ErrVorschlagUnbekannt) {
		writeError(w, http.StatusNotFound, "Diesen Vorschlag gibt es nicht (mehr) — schon bestätigt, abgelehnt oder abgelaufen.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion unreadable", err)
		return
	}
	if v.Abgelaufen() {
		// Nicht mehr bestätigbar. Gelöscht wird er vom Aufräumen — nur bei
		// gestellter Uhr (F-1192), deshalb hier nicht.
		a.VorschlaegeAufraeumen(r.Context())
		writeError(w, http.StatusGone, "Dieser Vorschlag ist abgelaufen (nach 24 Stunden) und lässt sich nicht mehr bestätigen.")
		return
	}
	if hash != v.Hash {
		writeError(w, http.StatusConflict, "Der Vorschlag entspricht nicht dem, was angezeigt wurde. Bitte die Liste neu laden.")
		return
	}
	if aktiv, err := a.repo.AgentTokens().IstAktiv(ctx, v.TokenID); err != nil {
		writeError(w, http.StatusInternalServerError, "token check failed", err)
		return
	} else if !aktiv {
		writeError(w, http.StatusConflict, "Der Zugang des Agenten (agent:"+v.TokenName+") ist widerrufen oder abgelaufen. Den Vorschlag bitte ablehnen.")
		return
	}
	// Erst entfernen, dann ausführen: Von zwei gleichzeitigen Bestätigungen
	// führt genau eine aus. Lehnt die Ausführung ab, ohne zu schreiben, kommt
	// der Vorschlag zurück.
	if err := repo.Entfernen(ctx, board, v.ID); errors.Is(err, storage.ErrVorschlagUnbekannt) {
		writeError(w, http.StatusConflict, "Dieser Vorschlag wurde eben schon bestätigt oder abgelehnt.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion not removed", err)
		return
	}
	puffer := a.vorschlagAusfuehren(r, board, email, v)
	if puffer.status != http.StatusOK {
		freigebbar := loeschCodeFreigebbar(puffer)
		if freigebbar {
			// Abgelehnt vor dem COMMIT, nichts geschrieben: Der Vorschlag gilt weiter.
			if err := repo.Wiederherstellen(context.WithoutCancel(r.Context()), v); err != nil {
				log.Printf("vorschlaege: Vorschlag %s nach abgelehnter Ausführung nicht wiederhergestellt: %v", v.ID, err)
			}
		} else {
			a.recordActivity(context.WithoutCancel(r.Context()), board, email, "vorschlag-unklar", v.Art, "", "",
				"Vorschlag "+v.ID+" von agent:"+v.TokenName+": Ausführung mit Status "+strconv.Itoa(puffer.status)+", ob geschrieben wurde, ist offen")
		}
		var details any
		if json.Unmarshal(puffer.body.Bytes(), &details) != nil {
			details = nil
		}
		status := puffer.status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, map[string]any{"error": vorschlagFehlerText(freigebbar), "details": details})
		return
	}
	a.recordActivity(context.WithoutCancel(r.Context()), board, email, "vorschlag-bestaetigt", v.Art, "", "",
		"Vorschlag "+v.ID+" von agent:"+v.TokenName+" bestätigt") // wer: actor (Verify V-7, keine Adresse in der Notiz)
	log.Printf("vorschlaege: %s hat auf Board %s den Vorschlag %s (%s) von agent:%s bestätigt", email, board, v.ID, v.Art, v.TokenName)
	var details any
	if json.Unmarshal(puffer.body.Bytes(), &details) != nil {
		details = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "Vorschlag ausgeführt.", "details": details})
}

// vorschlagFehlerText: Meldung zu einer gescheiterten Bestätigung. Nur wenn das
// Werkzeug vor dem Schreiben abgelehnt hat, steht fest, dass nichts geschah; sonst
// wäre „nicht ausgeführt“ eine Behauptung, die das eigene Protokoll („ob
// geschrieben wurde, ist offen“) nicht deckt — wer ihr glaubt, lässt den Vorschlag
// neu stellen und hat Kommentar oder Karte doppelt (Sprint-91-Kritiker K-S3).
func vorschlagFehlerText(freigebbar bool) string {
	if freigebbar {
		return "Der Vorschlag wurde nicht ausgeführt."
	}
	return "Ob der Vorschlag ausgeführt wurde, ist offen. Bitte im Board nachsehen, bevor er neu gestellt wird."
}

// vorschlagAusfuehren führt einen Vorschlag über das passende Werkzeug aus —
// mit synthetischem Agenten-Principal (Name = Token-Name, Rolle intern, ohne
// Person) und der E-Mail des Bestätigers für die Ratenbegrenzung. Die Antwort
// des Werkzeugs steht im Puffer.
func (a *BoardsAPI) vorschlagAusfuehren(r *http.Request, board, email string, v *storage.Vorschlag) *pufferAntwort {
	puffer := &pufferAntwort{kopf: http.Header{}}
	p := &AgentPrincipal{TokenID: v.TokenID, Name: v.TokenName, BoardID: board, Role: storage.AgentRolleIntern,
		tokenRolle: storage.AgentRolleIntern}
	// F-2090: Der Vorschlag ist zu diesem Zeitpunkt schon entfernt. Bräche der
	// Browser jetzt ab, scheiterte die Ausführung am abgebrochenen Kontext mit
	// einem Status, der „ob geschrieben wurde, ist offen“ bedeutet — der
	// Vorschlag wäre weg, obwohl nichts geschah. Deshalb ohne Abbruch der
	// Anfrage, mit eigener Frist (die Werkzeuge setzen ihre zusätzlich).
	basis, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), requestTimeout)
	defer cancel()
	ctx := context.WithValue(basis, contextKeyAgent, p)
	ctx = context.WithValue(ctx, vorschlagBestaetigerSchluessel{}, email)
	var h http.HandlerFunc
	nutzlast := []byte(v.Nutzlast)
	switch v.Art {
	case storage.VorschlagKarteAnlegen:
		h = a.werkzeugKarteAnlegen
	case storage.VorschlagKarteText, storage.VorschlagKarteBearbeiter:
		h = a.werkzeugKarteAendern
	case storage.VorschlagKommentar:
		h = a.werkzeugKommentieren
	case storage.VorschlagKarteArchivieren:
		h = a.werkzeugArchivieren
		nutzlast = []byte("{}")
	case storage.VorschlagKarteLoeschen:
		a.vorschlagLoeschenAusfuehren(puffer, ctx, board, v)
		return puffer
	default:
		writeError(puffer, http.StatusInternalServerError, "unknown suggestion kind")
		return puffer
	}
	if v.KarteID != "" {
		// Kritik K-A2: Die Werkzeuge suchen eine Karte zuerst über die Nummer.
		// Hiesse eine Karten-id wie eine Nummer, träfe die Ausführung eine andere
		// Karte. Deshalb hier: genau diese Karte, eindeutig, sonst nichts.
		b, err := a.repo.Get(ctx, board)
		if err != nil {
			writeError(puffer, http.StatusNotFound, "board not found")
			return puffer
		}
		wb, err := leseWkBoard(b.StateJSON)
		if err != nil {
			writeError(puffer, http.StatusInternalServerError, "board read failed", err)
			return puffer
		}
		if karteExakt(wb, v.KarteID) == nil {
			writeError(puffer, http.StatusConflict, "Die Karte des Vorschlags gibt es nicht mehr (oder sie ist nicht eindeutig zu finden). Den Vorschlag bitte ablehnen.")
			return puffer
		}
	}
	r2, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(nutzlast))
	if err != nil {
		writeError(puffer, http.StatusInternalServerError, "request build failed", err)
		return puffer
	}
	r2.Header.Set("Content-Type", "application/json")
	// Kritik K-A3: eigener Wiederholungs-Schlüssel je Vorschlag. Sonst hielte
	// der Wiederholungsschutz der Werkzeuge (Akteur + Inhalt, 10 min) einen
	// zweiten gleichlautenden Vorschlag für eine Wiederholung und schriebe nichts.
	r2.Header.Set(IdempotencyKeyHeader, "vorschlag:"+v.ID)
	r2.SetPathValue("boardId", board)
	if v.KarteID != "" {
		r2.SetPathValue("karte", v.KarteID)
	}
	h(puffer, r2)
	return puffer
}

// karteExakt: die Karte mit genau dieser id — und nur, wenn die Suche der
// Werkzeuge (Nummer vor id) genau sie findet. Sonst nil (Kritik K-A2).
func karteExakt(wb *wkBoard, id string) *wkKarte {
	treffer := wb.kartenTreffer(id)
	if len(treffer) != 1 || treffer[0].karte.ID != id || treffer[0].lane.Kind == "service" {
		return nil
	}
	return treffer[0].karte
}

// vorschlagLoeschenAusfuehren löscht die Ziel-Karte wie karte_loeschen — aber
// ohne Code: Die Zustimmung ist die Bestätigung des Menschen. Der Stand der
// Karte muss noch der vom Vorschlag sein (unter der Sperre geprüft); sonst
// wird nichts gelöscht (409).
func (a *BoardsAPI) vorschlagLoeschenAusfuehren(w http.ResponseWriter, ctx context.Context, board string, v *storage.Vorschlag) {
	var n map[string]json.RawMessage
	_ = json.Unmarshal([]byte(v.Nutzlast), &n)
	erwartet := zeichenketteVon(n["stand"])
	fctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	b, err := a.repo.Get(fctx, board)
	if err != nil {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	s, ok := leseRohStand(b.StateJSON)
	if !ok {
		writeError(w, http.StatusInternalServerError, "board read failed")
		return
	}
	z, f := loeschKarte.ermittle(a, board, s, v.KarteID, true)
	if f != nil {
		f.schreibe(w)
		return
	}
	kartenStand := func(st rohStand) (string, bool) {
		for _, l := range st.lanes {
			for _, k := range st.karten(l) {
				if k != nil && zeichenketteVon(k["id"]) == v.KarteID {
					roh, _ := json.Marshal(k)
					return standKennung(json.RawMessage(roh)), true
				}
			}
		}
		return "", false
	}
	if jetzt, _ := kartenStand(s); jetzt != erwartet {
		writeError(w, http.StatusConflict, "Die Karte wurde seit dem Vorschlag geändert — nichts gelöscht. Den Vorschlag bitte ablehnen.")
		return
	}
	erwarteteOps := opsKennung(z.ops)
	vorbedingung := func(state []byte) error {
		st, ok := leseRohStand(state)
		if !ok {
			return &opsVorbedingungVerletzt{grund: "stand-unlesbar"}
		}
		jetzt, f := loeschKarte.ermittle(a, board, st, v.KarteID, true)
		if f != nil {
			return &opsVorbedingungVerletzt{grund: "weg"}
		}
		if ks, _ := kartenStand(st); ks != erwartet || opsKennung(jetzt.ops) != erwarteteOps {
			return &opsVorbedingungVerletzt{grund: "geaendert"}
		}
		return nil
	}
	r2, err := http.NewRequestWithContext(mitOpsVorbedingung(ctx, vorbedingung), http.MethodPost, "/", http.NoBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "request build failed", err)
		return
	}
	inner := &pufferAntwort{kopf: http.Header{}}
	if _, puf, ok := a.werkzeugOpsRoh(inner, r2, board, z.ops); !ok {
		if puf == nil {
			kopiereAntwort(w, inner)
			return
		}
		if puf.status == http.StatusConflict && bytes.Contains(puf.body.Bytes(), []byte(`"`+opsVorbedingungArt+`"`)) {
			writeError(w, http.StatusConflict, "Die Karte wurde gerade eben geändert oder entfernt — nichts gelöscht.")
			return
		}
		kopiereAntwort(w, puf)
		return
	}
	a.loescheDateien(board, z.dateien)
	writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "gelöscht", "boardId": board, "geloescht": z.vorschau})
}

// vorschlagAblehnen — DELETE /boards/{id}/vorschlaege/{vid}: hart löschen.
func (a *BoardsAPI) vorschlagAblehnen(w http.ResponseWriter, r *http.Request) {
	board, email := a.vorschlagMensch(w, r)
	if board == "" {
		return
	}
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	repo := a.repo.Vorschlaege()
	v, err := repo.Hole(ctx, board, r.PathValue("vid"))
	if errors.Is(err, storage.ErrVorschlagUnbekannt) {
		writeError(w, http.StatusNotFound, "Diesen Vorschlag gibt es nicht (mehr).")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion unreadable", err)
		return
	}
	if err := repo.Entfernen(ctx, board, v.ID); errors.Is(err, storage.ErrVorschlagUnbekannt) {
		writeError(w, http.StatusNotFound, "Diesen Vorschlag gibt es nicht (mehr).")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "suggestion not removed", err)
		return
	}
	a.recordActivity(context.WithoutCancel(r.Context()), board, email, "vorschlag-abgelehnt", v.Art, "", "", "Vorschlag "+v.ID+" von agent:"+v.TokenName+" abgelehnt")
	log.Printf("vorschlaege: %s hat auf Board %s den Vorschlag %s (%s) von agent:%s abgelehnt", email, board, v.ID, v.Art, v.TokenName)
	writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "Vorschlag abgelehnt und gelöscht."})
}

// VorschlaegeAufraeumen löscht abgelaufene Vorschläge hart und vermerkt jeden
// im Protokoll seines Boards, ohne Nutzlast. Nur bei gestellter Uhr
// (server/uhr, F-1192): Geht die Uhr vor, verschwänden Vorschläge zu früh.
// Fehler gehen ins Log, nie an den Aufrufer.
func (a *BoardsAPI) VorschlaegeAufraeumen(ctx context.Context) {
	if !uhr.Erlaubt("vorschlaege", nil) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()
	weg, err := a.repo.Vorschlaege().AbgelaufeneLoeschen(ctx)
	if err != nil {
		log.Printf("vorschlaege: abgelaufene nicht gelöscht: %v", err)
		return
	}
	for _, v := range weg {
		a.protokoll(ctx, v.BoardID, "aboard-server", "system", "vorschlag-abgelaufen", v.Art, "Vorschlag "+v.ID+" von agent:"+v.TokenName)
	}
	if len(weg) > 0 {
		log.Printf("vorschlaege: %d abgelaufene Vorschläge gelöscht", len(weg))
	}
}

// StarteVorschlagAufraeumen räumt alle vorschlagAufraeumTakt ab, bis ctx
// endet. Aus main.go; eine Goroutine je Server.
func (a *BoardsAPI) StarteVorschlagAufraeumen(ctx context.Context) {
	go func() {
		takt := time.NewTicker(vorschlagAufraeumTakt)
		defer takt.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-takt.C:
				a.VorschlaegeAufraeumen(ctx)
			}
		}
	}()
}
