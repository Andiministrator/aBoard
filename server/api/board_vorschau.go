package api

// Vorschaubild eines Boards für die Kachelansicht der Board-Übersicht (Karte #741).
//
//	GET /boards/{boardId}/vorschaubild
//
// Liefert das Hintergrundbild des Boards verkleinert als JPEG (Kachelgröße,
// mittig zugeschnitten), oder 204, wenn das Board kein Hintergrundbild hat
// oder es sich nicht verkleinern lässt. 204 statt 404, weil „kein Bild“ hier
// der Normalfall ist und keine Fehlermeldung in der Browser-Konsole erzeugen
// soll; die Kachel zeigt dann ihre Grundfarbe.
//
// Autorisierung: Die Route liegt unter /boards/{id}/… und erbt den ACLGuard
// (GET → viewer+), genau wie der Download des Originals. Wer das Board nicht
// lesen darf, sieht auch die Vorschau nicht.
//
// Warum der Server verkleinert und nicht der Browser: Hintergrundbilder sind
// oft mehrere Megabyte groß. Eine Übersicht mit zehn Boards lüde sonst zehn
// Originale, nur um sie auf Kachelgröße zu zeichnen — auf dem Handy über VPN
// eine spürbare Wartezeit. Das Vorschaubild ist wenige Kilobyte groß.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// Kachelgröße (16:9), doppelt so groß wie die Anzeige für scharfe
	// Darstellung auf Bildschirmen mit hoher Pixeldichte.
	vorschauBreite = 480
	vorschauHoehe  = 270
	vorschauJPEG   = 78
	// Schutz gegen Dekomprimierungsbomben: Größe der Datei und der
	// Bildfläche werden VOR dem Dekodieren geprüft (image.DecodeConfig).
	vorschauDateiHoechst = 50 * 1024 * 1024
	vorschauKanteHoechst = 16000
	// 40 MP deckt Kamerafotos und 8K ab. Ein 16-Bit-PNG dieser Fläche braucht
	// dekodiert rund 320 MB; deshalb dekodiert immer nur EINE Anfrage zugleich
	// (Kritiker 07.10., K-2).
	vorschauPixelHoechst = 40_000_000
	// Zwischenspeicher: Ein Anhang ist unter seiner id unveränderlich (ein
	// neues Bild bekommt eine neue id), der Eintrag veraltet also nie.
	vorschauCacheHoechst = 256
)

var errKeineVorschau = errors.New("keine vorschau")

type BoardVorschauAPI struct {
	boards *storage.BoardRepo
	store  *storage.AttachmentStore

	mu    sync.Mutex
	cache map[string][]byte // "<boardId>/<attId>" → JPEG; leerer Wert = nicht verkleinerbar
	// dekodieren begrenzt die gleichzeitigen Dekodierungen auf eine (K-2):
	// Speicherspitzen addieren sich sonst je Betrachter und Kachel.
	dekodieren chan struct{}
}

func NewBoardVorschauAPI(boards *storage.BoardRepo, store *storage.AttachmentStore) *BoardVorschauAPI {
	return &BoardVorschauAPI{boards: boards, store: store, cache: map[string][]byte{}, dekodieren: make(chan struct{}, 1)}
}

func (a *BoardVorschauAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /boards/{boardId}/vorschaubild", WithDefaults(http.HandlerFunc(a.vorschau)))
}

// vorschauState: nur die Felder des Board-Zustands, die hier gebraucht werden.
type vorschauState struct {
	Board struct {
		Background *struct {
			AttachmentID string `json:"attachmentId"`
		} `json:"background"`
		Attachments []struct {
			ID   string `json:"id"`
			Data string `json:"data"`
		} `json:"attachments"`
	} `json:"board"`
}

func (a *BoardVorschauAPI) vorschau(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("boardId")
	if !validAttachmentID(boardID) {
		writeError(w, http.StatusBadRequest, "invalid path segment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	b, err := a.boards.Get(ctx, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}
	var st vorschauState
	if err := json.Unmarshal(b.StateJSON, &st); err != nil || st.Board.Background == nil ||
		st.Board.Background.AttachmentID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	attID := st.Board.Background.AttachmentID
	etag := `"` + attID + `-v1"`
	// no-cache, nicht max-age (K-1): Die URL ist je Board fest, die Anhang-id
	// steckt nur im ETag. Mit max-age zeigte der Browser nach einem
	// Bildwechsel bis zu einer Stunde das alte Bild, und nach entzogenem
	// Leserecht weiter das gespeicherte. no-cache fragt jedes Mal nach, die
	// Antwort ist dann meist ein billiges 304. Preis: Jede Nachfrage lädt den
	// Board-Zustand (K-5, hingenommen).
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	schluessel := boardID + "/" + attID
	bild, ok := a.ausCache(schluessel)
	if !ok {
		select {
		case a.dekodieren <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		var inline string
		for _, att := range st.Board.Attachments {
			if att.ID == attID {
				inline = att.Data
				break
			}
		}
		bild = a.rechne(schluessel, boardID, attID, inline)
	}
	if len(bild) == 0 {
		// Kein Bild oder nicht verkleinerbar (z. B. SVG, zu groß, kaputt):
		// die Kachel zeigt ihre Grundfarbe. Kein Fehler für den Browser.
		w.Header().Del("ETag")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bild)
}

func (a *BoardVorschauAPI) ausCache(schluessel string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bild, ok := a.cache[schluessel]
	return bild, ok
}

func (a *BoardVorschauAPI) inCache(schluessel string, bild []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= vorschauCacheHoechst {
		a.cache = map[string][]byte{} // einfach leeren statt LRU: ein Neuaufbau kostet nur Rechenzeit
	}
	a.cache[schluessel] = bild
}

// vorschauVerkleinern ist VorschauJPEG; als Variable, damit ein interner Test
// eine Panik oder einen langsamen Dekoder einsetzen kann (Verify 07.10., V-1/V-2).
var vorschauVerkleinern = VorschauJPEG

// rechne läuft mit belegtem Dekodier-Platz und gibt ihn in JEDEM Fall frei —
// auch bei einer Panik im Dekoder (Verify 07.10., V-1). Ohne defer bliebe der
// einzige Platz nach einer Panik belegt, und jede weitere Vorschau hinge bis
// zum Neustart. Eine Panik zählt wie ein endgültiger Fehlschlag.
func (a *BoardVorschauAPI) rechne(schluessel, boardID, attID, inline string) (bild []byte) {
	defer func() { <-a.dekodieren }()
	defer func() {
		if p := recover(); p != nil {
			log.Printf("vorschaubild: Panik beim Verkleinern von %s: %v", schluessel, p)
			bild = []byte{}
			a.inCache(schluessel, bild)
		}
	}()
	// Nach dem Warten erneut nachsehen: eine gleichzeitige Anfrage für
	// dasselbe Bild hat es womöglich schon gerechnet.
	if b, ok := a.ausCache(schluessel); ok {
		return b
	}
	roh, lerr := a.ladeOriginal(boardID, attID, inline)
	if lerr == nil {
		bild, lerr = vorschauVerkleinern(roh)
	}
	switch {
	case lerr == nil:
		a.inCache(schluessel, bild)
	case errors.Is(lerr, errKeineVorschau):
		// Ein endgültiger Fehlschlag wird gemerkt (leerer Wert, K-2): sonst
		// liest und dekodiert jedes Öffnen der Übersicht bis zu 50 MB erneut.
		// Ein neues Bild hat eine neue id, also einen neuen Schlüssel.
		bild = []byte{}
		a.inCache(schluessel, bild)
	default:
		// Lesefehler der Ablage: vorübergehend möglich, nicht merken.
		bild = nil
	}
	return bild
}

// ladeOriginal liest das Hintergrundbild: aus der Board-Ablage des Servers
// (seit Karte #443) oder, bei älteren Boards, base64 aus dem Board-Zustand.
func (a *BoardVorschauAPI) ladeOriginal(boardID, attID, inline string) ([]byte, error) {
	rc, _, err := a.store.Open(boardID, storage.BoardAblageId, attID)
	if err == nil {
		defer rc.Close()
		roh, rerr := io.ReadAll(io.LimitReader(rc, vorschauDateiHoechst+1))
		if rerr != nil {
			return nil, rerr
		}
		if len(roh) > vorschauDateiHoechst {
			return nil, fmt.Errorf("%w: datei zu groß", errKeineVorschau)
		}
		return roh, nil
	}
	if !errors.Is(err, storage.ErrAttachmentNotFound) {
		return nil, err
	}
	if inline == "" || base64.StdEncoding.DecodedLen(len(inline)) > vorschauDateiHoechst {
		return nil, fmt.Errorf("%w: kein bild", errKeineVorschau)
	}
	roh, err := base64.StdEncoding.DecodeString(inline)
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %v", errKeineVorschau, err)
	}
	return roh, nil
}

// VorschauJPEG verkleinert ein Bild (JPEG, PNG, GIF, WebP) auf Kachelgröße,
// mittig auf 16:9 zugeschnitten, und kodiert es als JPEG. Andere Formate
// (SVG ist als Hintergrund ohnehin ausgeschlossen, F-128) und übergroße
// Bilder liefern einen Fehler.
func VorschauJPEG(roh []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(roh))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errKeineVorschau, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > vorschauKanteHoechst || cfg.Height > vorschauKanteHoechst ||
		int64(cfg.Width)*int64(cfg.Height) > vorschauPixelHoechst {
		return nil, fmt.Errorf("%w: %d×%d bildpunkte", errKeineVorschau, cfg.Width, cfg.Height)
	}
	var img image.Image
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(roh))
	case "png":
		img, err = png.Decode(bytes.NewReader(roh))
	case "gif":
		img, err = gif.Decode(bytes.NewReader(roh))
	case "webp":
		img, err = webp.Decode(bytes.NewReader(roh))
	default:
		return nil, fmt.Errorf("%w: format %s", errKeineVorschau, format)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errKeineVorschau, err)
	}

	// Mittiger Ausschnitt im Seitenverhältnis der Kachel („cover“).
	q := img.Bounds()
	sw, sh := q.Dx(), q.Dy()
	aw, ah := sw, sw*vorschauHoehe/vorschauBreite
	if ah > sh {
		ah, aw = sh, sh*vorschauBreite/vorschauHoehe
	}
	if aw < 1 {
		aw = 1
	}
	if ah < 1 {
		ah = 1
	}
	x0, y0 := q.Min.X+(sw-aw)/2, q.Min.Y+(sh-ah)/2
	ausschnitt := image.Rect(x0, y0, x0+aw, y0+ah)

	ziel := image.NewRGBA(image.Rect(0, 0, vorschauBreite, vorschauHoehe))
	// Durchsichtige Flächen (PNG/GIF/WebP) auf Weiß statt Schwarz.
	draw.Draw(ziel, ziel.Bounds(), image.White, image.Point{}, draw.Src)
	// BiLinear (nicht ApproxBiLinear): der Kern wächst mit dem
	// Verkleinerungsfaktor und mittelt über die Fläche — ApproxBiLinear tastet
	// nur wenige Punkte ab und erzeugt bei 4000 → 480 Treppen und Moiré.
	draw.BiLinear.Scale(ziel, ziel.Bounds(), img, ausschnitt, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, ziel, &jpeg.Options{Quality: vorschauJPEG}); err != nil {
		return nil, fmt.Errorf("%w: %v", errKeineVorschau, err)
	}
	return buf.Bytes(), nil
}
