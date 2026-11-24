package api

// schatten_ausgang.go — Board-Paar intern/extern, Seite innen: der
// Schatten-Ausgang (ADR-0140 §2, Karte #778).
//
//	GET /agent/schatten/{boardId}   — Schattenliste eines gekoppelten Boards
//
// Nur am Agenten-Eingang (Pfad /agent/ oder Agenten-Listener) und nur mit
// einem Token vom Typ `abs` (Präfix abs_). Das Token geht direkt in
// den Mux, ohne Agentenrolle und ohne ACL (agentHandler in main_agent.go).
// Abholer ist das Push-Skript des Partnerprojekts (Vertrag Push-Skript, Ziffer 9); es ersetzt die
// interne Kennung jedes Eintrags durch eine HMAC und schiebt die Liste außen
// hinein (#779).
//
// Den Ausgang gibt es nur, wenn die Instanz-Einstellung boardpaar.seite
// „innen“ ist — je Anfrage gelesen, ein ungültiger oder unlesbarer Wert
// wirkt wie „aus“ (ADR-0140 §4). Sonst, und für jedes Board außerhalb der
// Partnerliste innen, antwortet er 404, ohne Unterschied zu „gibt es nicht“.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// KonfigBoardpaarSeite ist der server_config-Schlüssel der Seite dieser
	// Instanz im Board-Paar: aus (Standard), innen oder aussen.
	KonfigBoardpaarSeite = "boardpaar.seite"

	BoardpaarSeiteAus    = "aus"
	BoardpaarSeiteInnen  = "innen"
	BoardpaarSeiteAussen = "aussen"

	// SchattenPfad: Präfix des Ausgangs im Mux (am Agenten-Eingang nach dem
	// Abschneiden von /agent).
	SchattenPfad = "/schatten/"
)

// PruefeBoardpaarSeite liefert die Normalform eines Werts für
// boardpaar.seite (Karte #788): aus, innen oder aussen, ohne Rücksicht auf
// Gross-/Kleinschreibung und Leerraum; leer heisst aus. Alles andere ist ein
// Fehler — beim Speichern, nicht erst beim Lesen (dort gilt es als „aus“).
func PruefeBoardpaarSeite(roh string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(roh)); s {
	case "", BoardpaarSeiteAus:
		return BoardpaarSeiteAus, nil
	case BoardpaarSeiteInnen, BoardpaarSeiteAussen:
		return s, nil
	}
	return "", fmt.Errorf("%s: erlaubt sind aus, innen oder aussen (nicht %q)", KonfigBoardpaarSeite, roh)
}

// BoardpaarSeite liest die Seite dieser Instanz, je Anfrage. Fehlt der
// Wert, ist er unbekannt oder nicht lesbar → „aus“ (Klasse 173, V-11).
func BoardpaarSeite(ctx context.Context, cfg *storage.ConfigRepo) string {
	seite, err := BoardpaarSeiteMitFehler(ctx, cfg)
	if err != nil {
		log.Printf("boardpaar: seite nicht lesbar, gilt als aus: %v", err)
	}
	return seite
}

// BoardpaarSeiteMitFehler wie BoardpaarSeite, meldet aber einen Lesefehler
// (Kritiker #789, K-2): Ein fehlender Schlüssel und ein ungültiger Wert sind
// „aus“ ohne Fehler (ADR-0140 §4); nur wenn server_config nicht lesbar ist,
// kommt err ≠ nil (Seite dann „aus“). Wer fail-closed entscheiden muss — der
// Ops-Weg für Agenten (W-4) —, nimmt diese Fassung.
func BoardpaarSeiteMitFehler(ctx context.Context, cfg *storage.ConfigRepo) (string, error) {
	if cfg == nil {
		return BoardpaarSeiteAus, nil
	}
	v, err := cfg.Get(ctx, KonfigBoardpaarSeite)
	if err != nil {
		if errors.Is(err, storage.ErrConfigKeyNotFound) {
			return BoardpaarSeiteAus, nil
		}
		return BoardpaarSeiteAus, err
	}
	switch s := strings.TrimSpace(v.Value); s {
	case BoardpaarSeiteInnen, BoardpaarSeiteAussen:
		return s, nil
	case BoardpaarSeiteAus, "":
		return BoardpaarSeiteAus, nil
	default:
		log.Printf("boardpaar: seite %q ungültig, gilt als aus", s)
		return BoardpaarSeiteAus, nil
	}
}

// boardpaarTokenTyp: Typ des Board-Paar-Tokens der Anfrage ("" = keins; nur
// der Präfix, nicht geprüft).
func boardpaarTokenTyp(r *http.Request) string {
	t, ok := bearerToken(r)
	if !ok {
		return ""
	}
	return storage.BoardpaarTypAusPraefix(t)
}

// HatBoardpaarToken: Die Anfrage trägt ein Token vom Typ abs, abe, abr oder abb.
func HatBoardpaarToken(r *http.Request) bool {
	return boardpaarTokenTyp(r) != ""
}

// istSchattenWeg: genau /schatten/<boardId>, ein Segment, ohne kodierte
// Sonderzeichen — der Pfad, wie ihn der Mux am Agenten-Eingang sieht.
func istSchattenWeg(r *http.Request) bool {
	if r.URL.RawPath != "" {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, SchattenPfad)
	return ok && rest != "" && !strings.Contains(rest, "/") && rest != "." && rest != ".."
}

// istSchattenAusgangWeg: GET/HEAD /schatten/<boardId> (Ausgang innen, abs_).
func istSchattenAusgangWeg(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && istSchattenWeg(r)
}

// istSchattenEingangWeg: PUT /schatten/<boardId> (Eingang außen, abe_).
func istSchattenEingangWeg(r *http.Request) bool {
	return r.Method == http.MethodPut && istSchattenWeg(r)
}

// istBoardpaarErneuernWeg: genau POST /boardpaar/erneuern (Karte #796), für
// beide Typen; welcher Typ auf welcher Seite gilt, prüft der Handler.
func istBoardpaarErneuernWeg(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.RawPath == "" && r.URL.Path == BoardpaarErneuernPfad
}

// BoardpaarSchranke weist ein Board-Paar-Token auf jedem Weg außer seinem
// eigenen mit 403 ab — vor jeder anderen Prüfung, an beiden Ketten
// (ADR-0140 §2, §3). Eigene Wege gibt es nur an der Agenten-Kette:
//   - abs_: GET/HEAD /schatten/<board> (Stufe 1), GET /boardpaar/auftraege,
//     GET/HEAD /boardpaar/spiegel/<board> (ADR-0141 §10);
//   - abe_: PUT /schatten/<board> (Karte #779), POST /boardpaar/boards,
//     PUT /boardpaar/spiegel/<board>, GET/HEAD /boardpaar/aenderungen/<board>
//     und POST /boardpaar/aenderungen/<board>/bestaetigt (Karte #827);
//   - abr_: PUT /boardpaar/ziele, POST /boardpaar/auftraege/<id>/erledigt,
//     POST /boardpaar/anwenden/<board> (Karte #827);
//   - abb_: GET/HEAD /sicherungen und GET/HEAD /sicherungen/<board>/<datei>
//     (Sicherungen lesen, ADR-0142, Karte #834) — auf jeder Seite, auch „aus“;
//   - alle vier: POST /boardpaar/erneuern (Karte #796).
//
// Ob ein Weg auf DIESER Seite gilt (innen/außen) und ob das Token gilt,
// prüft der Handler. Jede andere Methode, jeder andere Pfad und die ganze
// Menschen-Kette: 403.
func BoardpaarSchranke(next http.Handler, agentKette bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		typ := boardpaarTokenTyp(r)
		if typ == "" {
			next.ServeHTTP(w, r)
			return
		}
		if agentKette && istBoardpaarWegFuer(typ, r) {
			next.ServeHTTP(w, r)
			return
		}
		if schattenAbweisungLogErlaubt(time.Now()) {
			log.Printf("boardpaar: %s-token auf fremdem weg → 403: %s %q (höchstens eine Zeile pro Minute)", typ, r.Method, r.URL.Path)
		}
		writeError(w, http.StatusForbidden, "forbidden: this token does not reach this path")
	})
}

var schattenAbweisungLogZuletzt atomic.Int64

func schattenAbweisungLogErlaubt(jetzt time.Time) bool {
	zuletzt := schattenAbweisungLogZuletzt.Load()
	if zuletzt != 0 && jetzt.UnixNano()-zuletzt < int64(time.Minute) {
		return false
	}
	return schattenAbweisungLogZuletzt.CompareAndSwap(zuletzt, jetzt.UnixNano())
}

// SchattenAusgangAPI liefert die Schattenliste.
type SchattenAusgangAPI struct {
	boards *storage.BoardRepo
	cfg    *storage.ConfigRepo
	paar   *storage.BoardpaarRepo
	// limit: Fehlversuche je IP (eigenes Kontingent).
	limit *rateLimiter
	// adresse: Schlüssel-Adresse der Drossel (F-2030); MitAdressregel.
	adresse func(*http.Request) string
}

func NewSchattenAusgangAPI(boards *storage.BoardRepo, cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo) *SchattenAusgangAPI {
	return &SchattenAusgangAPI{boards: boards, cfg: cfg, paar: paar, limit: newRateLimiter(120, time.Hour), adresse: tcpAdresse}
}

// MitAdressregel setzt die Adressregel der Drossel — im Server die der
// Agenten-Bremse (AgentZugang.ClientAdresse, F-2030). Ohne Aufruf zählt die
// TCP-Gegenstelle.
func (a *SchattenAusgangAPI) MitAdressregel(fn func(*http.Request) string) *SchattenAusgangAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

// merkeStand schreibt den Abgleich-Stand eines Boards (F-2019) mit eigener
// Frist und OHNE die Abbruch-Verkettung der Anfrage: Kappt der Client die
// Verbindung oder läuft die Frist der Anfrage ab, soll der Admin gerade
// diesen Lauf noch sehen. Ein Fehler wird nur geloggt.
func merkeStand(ctx context.Context, weg, boardID string, merk func(context.Context) error) {
	mctx, mcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer mcancel()
	if err := merk(mctx); err != nil {
		log.Printf("%s: board=%s stand nicht gemerkt: %v", weg, boardID, err)
	}
}

// tcpAdresse: Adressregel ohne Proxy-Vertrauen — die TCP-Gegenstelle.
func tcpAdresse(r *http.Request) string { return (*proxyVertrauen)(nil).clientAdresse(r) }

func (a *SchattenAusgangAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET "+SchattenPfad+"{boardId}", WithDefaults(http.HandlerFunc(a.ausgang)))
}

// zugang prüft das abs_-Token. Liefert nil + geschriebene Fehlerantwort.
func (a *SchattenAusgangAPI) zugang(w http.ResponseWriter, r *http.Request) *storage.BoardpaarToken {
	return boardpaarZugang(w, r, a.paar, a.limit, a.adresse, storage.BoardpaarTypAusgang, "schatten-ausgang")
}

// boardpaarZugang prüft ein Board-Paar-Token des Typs typ (abs, abe, abr
// oder abb). Liefert nil + geschriebene Fehlerantwort (401, 429, 500 oder 503).
func boardpaarZugang(w http.ResponseWriter, r *http.Request, paar *storage.BoardpaarRepo, limit *rateLimiter,
	adresse func(*http.Request) string, typ, weg string) *storage.BoardpaarToken {
	abweisen := func(grund, text string) { boardpaarAbweisen(w, r, limit, adresse, weg, grund, text) }
	praefix := storage.BoardpaarPraefix(typ)
	if praefix == "" {
		writeError(w, http.StatusInternalServerError, "token check failed")
		return nil
	}
	t, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(t, praefix) {
		abweisen("ohne token", "bearer token ("+weg+") required")
		return nil
	}
	z, err := paar.TokenPruefe(r.Context(), typ, t)
	if errors.Is(err, storage.ErrBoardpaarTokenUnbekannt) {
		abweisen("token unbekannt oder widerrufen", "unknown or revoked token")
		return nil
	}
	if errors.Is(err, storage.ErrBoardpaarUhr) {
		// Vorgänger in der Übergangszeit, Uhr nicht gestellt (Karte #796,
		// Verify F2): später wieder, kein 401. Ins Log, damit der Betreiber
		// die Ursache sieht (Verify 2, F-A); höchstens eine Zeile pro Minute.
		if schattenAbweisungLogErlaubt(time.Now()) {
			log.Printf("%s: zugang=%s_ vorgänger in der übergangszeit, systemuhr nicht gestellt → 503 (Wissen §38; höchstens eine Zeile pro Minute)", weg, typ)
		}
		w.Header().Set("Retry-After", "300")
		writeError(w, http.StatusServiceUnavailable, "server clock not synchronized, retry later")
		return nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token check failed", err)
		return nil
	}
	return z
}

// boardpaarAbweisen antwortet auf einen Fehlversuch mit 401 (oder 429).
// Fehlversuche zählen in eine Per-IP-Drossel, je Weg ein eigenes
// Kontingent. Die Adresse folgt derselben Regel wie die Agenten-Bremse
// (F-2030): X-Real-IP nur vom Proxy aus ABOARD_AGENT_PROXY, sonst die
// TCP-Gegenstelle — nie X-Forwarded-For, den der Client anhängen kann.
// Zählt nur Fehlversuche; ein gültiges Token läuft nie in die Drossel.
func boardpaarAbweisen(w http.ResponseWriter, r *http.Request, limit *rateLimiter, adresse func(*http.Request) string,
	weg, grund, text string) {
	if !limit.allow(adresse(r)) {
		writeError(w, http.StatusTooManyRequests, "rate limit: too many failed requests")
		return
	}
	if schattenAbweisungLogErlaubt(time.Now()) {
		log.Printf("%s: zugang=- %s → 401 (höchstens eine Zeile pro Minute)", weg, grund)
	}
	writeError(w, http.StatusUnauthorized, text)
}

// schattenAntwort ist die Hülle der Liste — bewusst ohne Board-ID.
type schattenAntwort struct {
	Version  string             `json:"version"`
	Stand    string             `json:"stand"`
	Schatten []schatten.Eintrag `json:"schatten"`
}

// ausgang — GET /schatten/{boardId}.
func (a *SchattenAusgangAPI) ausgang(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if a.paar == nil || a.boards == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Die Seite VOR dem Token: Bei „aus“ oder „aussen“ gibt es den Weg
	// nicht, auch nicht als 401.
	if BoardpaarSeite(ctx, a.cfg) != BoardpaarSeiteInnen {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	z := a.zugang(w, r)
	if z == nil {
		return
	}
	boardID := r.PathValue("boardId")
	gekoppelt, err := a.paar.InnenGekoppelt(ctx, boardID)
	if err != nil {
		log.Printf("schatten-ausgang: zugang=%q partnerliste nicht lesbar → 500: %v", z.Name, err)
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if !gekoppelt {
		log.Printf("schatten-ausgang: zugang=%q board=%q → 404 (nicht gekoppelt oder unbekannt)", z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// F-2019: Ab hier ist das Board gekoppelt — jeder Ausgang wird je Board
	// mit Zeitpunkt und Fehlgrund gemerkt, damit der Admin innen ein
	// hängendes Board sieht (Admin-Reiter, `boardpaar liste`).
	merke := func(grund string) {
		merkeStand(ctx, "schatten-ausgang", boardID, func(mctx context.Context) error {
			return a.paar.InnenAbfrageMerken(mctx, boardID, grund)
		})
	}
	b, err := a.boards.Get(ctx, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		log.Printf("schatten-ausgang: zugang=%q board=%s board nicht lesbar → 500: %v", z.Name, boardID, err)
		merke("Board nicht lesbar")
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	erg, err := schatten.Baue(b.StateJSON)
	if err != nil {
		// Ganz oder Fehler: lieber keine Liste als eine, nach der das Skript
		// außen löscht (Lehre F-1957/V-4).
		log.Printf("schatten-ausgang: zugang=%q board=%s → 500 (%v)", z.Name, boardID, err)
		merke(fmt.Sprintf("Board-Zustand nicht auswertbar: %v", err))
		writeError(w, http.StatusInternalServerError, "board state unreadable")
		return
	}
	if len(erg.Eintraege) > schatten.EingangMaxSchatten {
		// Betriebsgrenze (ADR-0140 §3, Kritiker #779 K-4): Der Eingang außen
		// nimmt höchstens EingangMaxSchatten an. Eine gekappte Liste wäre
		// falsch — außen fehlten die übrigen und würden gelöscht. Also ganz
		// oder Fehler, mit lesbarem Grund. Abhilfe: erledigte Karten
		// archivieren (archivierte gehen nicht hinaus).
		log.Printf("schatten-ausgang: zugang=%q board=%s → 500 (%d Schatten, Grenze %d; erledigte Karten archivieren)",
			z.Name, boardID, len(erg.Eintraege), schatten.EingangMaxSchatten)
		merke(fmt.Sprintf("zu viele Einträge: %d, Grenze %d — erledigte Karten archivieren",
			len(erg.Eintraege), schatten.EingangMaxSchatten))
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("too many shadows: %d, limit %d — archive done cards",
			len(erg.Eintraege), schatten.EingangMaxSchatten))
		return
	}
	hinweis := ""
	if erg.OhneIdentitaeten {
		hinweis = " ohne-identities(nur Claude-Bearbeiter)"
	}
	log.Printf("schatten-ausgang: zugang=%q board=%s schatten=%d nicht-baubar=%v ohne-kennung=%d%s → 200",
		z.Name, boardID, len(erg.Eintraege), erg.NichtBaubar, erg.OhneKennung, hinweis)
	merke("")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, schattenAntwort{
		Version:  BuildVersion,
		Stand:    time.Now().UTC().Format(time.RFC3339),
		Schatten: erg.Eintraege,
	})
}

// kurzeKennung: eine fremde, womöglich lange Kennung fürs Log.
func kurzeKennung(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
