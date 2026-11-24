package api

// sicherungen_lesen.go — Zugang „Sicherungen lesen“ (abb_, ADR-0142, Karte #834).
//
//	GET/HEAD /agent/sicherungen                      — Boards mit abholbaren Sicherungen
//	GET/HEAD /agent/sicherungen/{boardId}/{filename} — ein Archiv, Byte für Byte
//
// Nur am Agenten-Eingang und nur mit einem Token vom Typ `abb` (Präfix abb_);
// die BoardpaarSchranke lässt es auf keinen anderen Weg (403), und kein
// anderes Board-Paar-Token kommt hierher. Das Token prüft der Handler selbst
// wie an den übrigen Board-Paar-Wegen; Fehlversuche zählen in eine eigene
// Per-IP-Drossel (401, ab der Grenze 429).
//
// Anders als die Board-Paar-Wege gelten diese beiden auf jeder Seite der
// Instanz, auch bei boardpaar.seite = aus: Eine Sicherung ist keine
// Board-Paar-Sache (ADR-0142 §2).
//
// Abholbar sind nur fertige, verschlüsselte Archive (`.tar.gz.enc`, Name nach
// safeFilenameRe, nie `-pending`). Wer das Token erbeutet, bekommt so keinen
// Board-Klartext, sondern nur Archive, die ohne ABOARD_SECRET_KEY des Servers
// wertlos sind (ADR-0142 §3). Zurückgespielt wird nur über den Menschenweg
// POST /backup/restore. In ein Board-Protokoll schreibt der Weg nichts.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
)

// SicherungenPfad: Präfix der beiden Wege im Mux (am Agenten-Eingang nach dem
// Abschneiden von /agent). Bewusst nicht „backup“ — so überschneidet er sich
// mit keinem Menschenweg (/backup/restore, /boards/{id}/backup/…).
const SicherungenPfad = "/sicherungen"

// istSicherungenPfad: genau /sicherungen oder /sicherungen/<board>/<datei>,
// je ein Segment ohne Sonderfälle. Nur der Pfad; Methode und RawPath prüft
// istBoardpaarWegFuer.
func istSicherungenPfad(p string) bool {
	if p == SicherungenPfad {
		return true
	}
	rest, ok := strings.CutPrefix(p, SicherungenPfad+"/")
	if !ok {
		return false
	}
	board, datei, ok := strings.Cut(rest, "/")
	return ok && einSegment(board) && einSegment(datei)
}

// sicherungBoardRe: Board-Kennungen, für die es abholbare Dateien geben kann
// — derselbe Zeichenvorrat wie im Board-Teil von safeFilenameRe. Eine andere
// Kennung kann keine Datei mit gültigem Namen haben und kommt nie bis zum
// Dateisystem (kein „..“, kein Schrägstrich).
var sicherungBoardRe = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

// sicherungRestRe: der Teil des Namens hinter „boardbackup-<board>-“ einer
// fertigen, verschlüsselten Sicherung — Zeit, SHA-Anfang, `.tar.gz.enc`. Ein
// Zwischenname (`…-<zufall>-pending.tar.gz[.enc]`) passt nicht, ebenso wenig
// die Datei eines Boards, dessen Kennung mit dieser beginnt (b1 / b1-x).
var sicherungRestRe = regexp.MustCompile(`^\d{8}-\d{6}-[a-zA-Z0-9]+\.tar\.gz\.enc$`)

// sicherungAbholbar: Der Dateiname gehört zu einer fertigen, verschlüsselten
// Sicherung dieses Boards (ADR-0142 §3).
func sicherungAbholbar(boardID, name string) bool {
	if !sicherungBoardRe.MatchString(boardID) || !dateinameGehoertZuBoard(boardID, name) {
		return false
	}
	rest, ok := strings.CutPrefix(name, "boardbackup-"+boardID+"-")
	return ok && sicherungRestRe.MatchString(rest)
}

// Schreibfrist beim Herunterladen eines Archivs (ADR-0142 §5): 60 s plus 1 s
// je angefangene 256 KiB, höchstens 30 min. Gilt am Token-Weg und am
// Menschenweg; ohne sie bräche ein großes Archiv nach der Server-Frist
// WriteTimeout (30 s, main.go) ab.
const (
	sicherungFristSockel = 60 * time.Second
	sicherungFristBlock  = 256 * 1024
	sicherungFristDeckel = 30 * time.Minute
)

// SicherungSchreibfrist liefert die Schreibfrist für ein Archiv der Größe groesse.
func SicherungSchreibfrist(groesse int64) time.Duration {
	if groesse <= 0 {
		return sicherungFristSockel
	}
	bloecke := groesse / sicherungFristBlock
	if groesse%sicherungFristBlock != 0 {
		bloecke++ // angefangener Block; ohne Addition, die bei MaxInt64 überliefe
	}
	// Vor der Multiplikation deckeln: kein Überlauf bei absurden Größen.
	if maxBloecke := int64((sicherungFristDeckel - sicherungFristSockel) / time.Second); bloecke > maxBloecke {
		return sicherungFristDeckel
	}
	return sicherungFristSockel + time.Duration(bloecke)*time.Second
}

// setzeSicherungSchreibfrist setzt die Frist an der Verbindung. Geht das
// nicht, gilt die Server-Frist; eine Log-Zeile sagt es.
func setzeSicherungSchreibfrist(w http.ResponseWriter, groesse int64, weg string) {
	frist := SicherungSchreibfrist(groesse)
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(frist)); err != nil {
		log.Printf("%s: schreibfrist %s nicht setzbar, es gilt die server-frist: %v", weg, frist, err)
	}
}

// SicherungenAPI liefert Liste und Archive an ein abb_-Token.
type SicherungenAPI struct {
	boards *storage.BoardRepo
	cfg    *storage.ConfigRepo
	paar   *storage.BoardpaarRepo
	// limit: Fehlversuche je IP (eigenes Kontingent, ADR-0142 §4).
	limit   *rateLimiter
	adresse func(*http.Request) string
}

func NewSicherungenAPI(boards *storage.BoardRepo, cfg *storage.ConfigRepo, paar *storage.BoardpaarRepo) *SicherungenAPI {
	return &SicherungenAPI{boards: boards, cfg: cfg, paar: paar, limit: newRateLimiter(120, time.Hour), adresse: tcpAdresse}
}

// MitAdressregel wie beim Ausgang (F-2030).
func (a *SicherungenAPI) MitAdressregel(fn func(*http.Request) string) *SicherungenAPI {
	if fn != nil {
		a.adresse = fn
	}
	return a
}

func (a *SicherungenAPI) Register(mux *http.ServeMux) {
	// „GET“ im Muster umfasst HEAD.
	mux.Handle("GET "+SicherungenPfad, WithDefaults(http.HandlerFunc(a.liste)))
	mux.Handle("GET "+SicherungenPfad+"/{boardId}/{filename}", WithSecurityHeaders(http.HandlerFunc(a.archiv)))
}

// sicherungenBoard ist ein Eintrag der Liste — bewusst ohne Board-Titel.
//
// board_status und fehler sind additiv (F-2305, ADR-0142 Nachtrag): `aktiv`,
// `geloescht` (Grabstein liegt) oder `ohne-board`. `fehler` steht nur, wenn
// der Ordner dieses Boards nicht lesbar war („ordner nicht lesbar“) oder die
// Kennung eines Boards mit Datenbankzeile nie abholbar sein kann („kennung
// ungueltig“, F-2325); er nennt eine Kategorie, nie einen Pfad des Servers.
type sicherungenBoard struct {
	BoardID     string                   `json:"board_id"`
	Kopie       bool                     `json:"kopie"`
	Files       []boardbackup.BackupInfo `json:"files"`
	BoardStatus string                   `json:"board_status"`
	Fehler      string                   `json:"fehler,omitempty"`
	// Angelegt: Anlagezeit der Datenbankzeile (RFC 3339, UTC), nur bei
	// Boards mit Zeile. Die Abholung meldet ein leeres Board erst als
	// Lücke, wenn es älter ist als die erste nächtliche Sicherung (F-2322).
	Angelegt string `json:"angelegt,omitempty"`
}

const (
	boardStatusAktiv     = "aktiv"
	boardStatusGeloescht = "geloescht"
	boardStatusOhneBoard = "ohne-board"
	fehlerOrdnerLesen    = "ordner nicht lesbar"
	// fehlerKennungUngueltig: Board mit Datenbankzeile, dessen Kennung nicht auf
	// sicherungBoardRe passt — es kann nie ein abholbares Archiv haben (F-2325).
	fehlerKennungUngueltig = "kennung ungueltig"
)

// boardStatusOhneBoardEintrag: Status eines Ordners, dessen Board die
// Datenbank nicht (mehr) kennt — Grabstein oder nicht.
func boardStatusOhneBoardEintrag(dir, boardID string) string {
	if boardbackup.HatGrabstein(dir, boardID) {
		return boardStatusGeloescht
	}
	return boardStatusOhneBoard
}

// sicherungsOrdner: die Namen der Board-Ordner unter dir, die eine gültige
// Board-Kennung sind (nur echte Verzeichnisse, keine Symlinks). Ein
// unlesbares Wurzelverzeichnis ergibt eine leere Liste; die Boards der
// Datenbank melden dann je Board ihren Fehler.
func sicherungsOrdner(dir string) []string {
	eintraege, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range eintraege {
		if e.IsDir() && sicherungBoardRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

type sicherungenAntwort struct {
	Boards []sicherungenBoard `json:"boards"`
}

const sicherungenWeg = "sicherungen-lesen"

func (a *SicherungenAPI) zugang(w http.ResponseWriter, r *http.Request) *storage.BoardpaarToken {
	return boardpaarZugang(w, r, a.paar, a.limit, a.adresse, storage.BoardpaarTypSicherungen, sicherungenWeg)
}

// abholbareDateien: die abholbaren Sicherungen eines Boards (ListBackups lässt
// Trümmerstücke schon weg; hier fallen zusätzlich alle unverschlüsselten).
func abholbareDateien(dir, boardID string) ([]boardbackup.BackupInfo, error) {
	if !sicherungBoardRe.MatchString(boardID) {
		return nil, nil
	}
	alle, err := boardbackup.ListBackups(dir, boardID)
	if err != nil {
		return nil, err
	}
	var out []boardbackup.BackupInfo
	for _, f := range alle {
		if sicherungAbholbar(boardID, f.Filename) {
			out = append(out, f)
		}
	}
	return out, nil
}

// liste — GET /sicherungen.
func (a *SicherungenAPI) liste(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if a.paar == nil || a.boards == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	z := a.zugang(w, r)
	if z == nil {
		return
	}
	boards, err := a.boards.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board list failed", err)
		return
	}
	aussen, err := a.paar.AussenListe(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	kopie := make(map[string]bool, len(aussen))
	for _, e := range aussen {
		kopie[e.BoardID] = e.Kopie
	}
	dir := boardbackup.LoadConfig(ctx, a.cfg).Dir
	antwort := sicherungenAntwort{Boards: []sicherungenBoard{}}
	dateien := 0
	// Reihenfolge: erst die Boards der Datenbank, dann Ordner ohne Board
	// (nach Name), damit die Abholung außen auch erreicht, was innen weg ist.
	aktiv := make(map[string]bool, len(boards))
	angelegt := make(map[string]string, len(boards))
	ids := make([]string, 0, len(boards))
	for _, b := range boards {
		aktiv[b.ID] = true
		if b.CreatedAt > 0 {
			angelegt[b.ID] = time.UnixMilli(b.CreatedAt).UTC().Format(time.RFC3339)
		}
		ids = append(ids, b.ID)
	}
	for _, id := range sicherungsOrdner(dir) { // ReadDir liefert sortiert
		if !aktiv[id] {
			ids = append(ids, id)
		}
	}
	fehler := 0
	for _, id := range ids {
		status := boardStatusAktiv
		if !aktiv[id] {
			status = boardStatusOhneBoardEintrag(dir, id)
		}
		if aktiv[id] && !sicherungBoardRe.MatchString(id) {
			// F-2325: Ohne `fehler` stünde das Board dauerhaft mit `files: []`
			// da, und die Abholung meldete nach der Schonfrist für immer ROT,
			// ohne den Grund zu nennen. Die Kennung ist hier die Störung.
			fehler++
			log.Printf("%s: zugang=%q board=%s %s", sicherungenWeg, z.Name, kurzeKennung(id), fehlerKennungUngueltig)
			antwort.Boards = append(antwort.Boards, sicherungenBoard{BoardID: id, Kopie: kopie[id], Files: []boardbackup.BackupInfo{}, BoardStatus: status, Fehler: fehlerKennungUngueltig, Angelegt: angelegt[id]})
			continue
		}
		files, err := abholbareDateien(dir, id)
		if err != nil {
			// Fehler je Board melden, den Rest liefern (F-2305). Der Eintrag
			// bleibt mit `fehler` in der Liste, sonst sähe die Lücke aus wie
			// „keine Sicherung“, und das Abholskript räumte womöglich danach.
			fehler++
			log.Printf("%s: zugang=%q board=%s ordner nicht lesbar: %v", sicherungenWeg, z.Name, kurzeKennung(id), err)
			antwort.Boards = append(antwort.Boards, sicherungenBoard{BoardID: id, Kopie: kopie[id], Files: []boardbackup.BackupInfo{}, BoardStatus: status, Fehler: fehlerOrdnerLesen, Angelegt: angelegt[id]})
			continue
		}
		// Ein Ordner ohne Board und ohne Datei hat nichts abzuholen. Ein
		// Board mit Datenbankzeile bleibt dagegen auch ohne Archiv in der
		// Liste, und zwar OHNE `fehler`: Scheitert seine Sicherung von Anfang
		// an, sähe die Abholung es sonst gar nicht und liefe grün (F-2322).
		// `fehler` bleibt Störungen vorbehalten (ADR-0142 §2, Nachtrag).
		if len(files) == 0 && !aktiv[id] {
			continue
		}
		if files == nil {
			files = []boardbackup.BackupInfo{}
		}
		dateien += len(files)
		antwort.Boards = append(antwort.Boards, sicherungenBoard{BoardID: id, Kopie: kopie[id], Files: files, BoardStatus: status, Angelegt: angelegt[id]})
	}
	log.Printf("%s: zugang=%q liste boards=%d dateien=%d fehler=%d → 200", sicherungenWeg, z.Name, len(antwort.Boards), dateien, fehler)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, antwort)
}

// archiv — GET /sicherungen/{boardId}/{filename}.
func (a *SicherungenAPI) archiv(w http.ResponseWriter, r *http.Request) {
	if a.paar == nil || a.boards == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	z := a.zugang(w, r)
	if z == nil {
		return
	}
	boardID, filename := r.PathValue("boardId"), r.PathValue("filename")
	nichtDa := func(grund string) {
		log.Printf("%s: zugang=%q board=%q datei=%q → 404 (%s)", sicherungenWeg, z.Name, kurzeKennung(boardID), kurzeKennung(filename), grund)
		writeError(w, http.StatusNotFound, "not found")
	}
	// 404 für alles, was keine abholbare Datei ist — ohne Unterschied, ob
	// unverschlüsselt, unfertig, fremdes Board oder ungültiger Name.
	if !sicherungAbholbar(boardID, filename) {
		nichtDa("nicht abholbar")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	_, err := a.boards.Get(ctx, boardID)
	cancel()
	// Ein unbekanntes Board ist kein 404: Archive gelöschter Boards und
	// Ordner ohne Board bleiben abholbar (ADR-0142 Nachtrag, F-2305).
	if err != nil && !errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusInternalServerError, "board lookup failed", err)
		return
	}
	dir := boardbackup.LoadConfig(r.Context(), a.cfg).Dir
	f, err := os.Open(filepath.Join(dir, boardID, filename))
	if err != nil {
		nichtDa("datei fehlt")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat failed", err)
		return
	}
	if !fi.Mode().IsRegular() {
		nichtDa("keine reguläre datei")
		return
	}
	setzeSicherungSchreibfrist(w, fi.Size(), sicherungenWeg)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	log.Printf("%s: zugang=%q board=%s datei=%s bytes=%d %s → 200", sicherungenWeg, z.Name, kurzeKennung(boardID), filename, fi.Size(), r.Method)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("%s: zugang=%q board=%s datei=%s abgebrochen: %v", sicherungenWeg, z.Name, kurzeKennung(boardID), filename, err)
	}
}
