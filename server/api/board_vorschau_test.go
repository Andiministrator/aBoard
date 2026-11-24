package api_test

// Tests für GET /boards/{id}/vorschaubild (Karte #741, Kachelansicht der
// Board-Übersicht).

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type vorschauEnv struct {
	srv    *httptest.Server
	boards *storage.BoardRepo
	store  *storage.AttachmentStore
}

func newVorschauEnv(t *testing.T) *vorschauEnv {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	boards := storage.NewBoardRepo(db)
	store := storage.NewAttachmentStore(t.TempDir())
	mux := http.NewServeMux()
	api.NewBoardVorschauAPI(boards, store).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &vorschauEnv{srv: srv, boards: boards, store: store}
}

// pngBild: ein w×h-PNG, links rot, rechts blau.
func pngBild(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{200, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 200, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *vorschauEnv) board(t *testing.T, id, state string) {
	t.Helper()
	if _, err := e.boards.Insert(context.Background(), id, "Board "+id, "1", []byte(state)); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func (e *vorschauEnv) get(t *testing.T, id string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+"/boards/"+id+"/vorschaubild", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestVorschau_AblageBildWirdVerkleinert(t *testing.T) {
	e := newVorschauEnv(t)
	if err := e.store.Save("b1", storage.BoardAblageId, "att-1", "bg.png", bytes.NewReader(pngBild(t, 1600, 900))); err != nil {
		t.Fatal(err)
	}
	e.board(t, "b1", `{"board":{"background":{"attachmentId":"att-1"},"attachments":[{"id":"att-1","mime":"image/png"}]}}`)

	res := e.get(t, "b1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type: %q", ct)
	}
	img, err := jpeg.Decode(res.Body)
	if err != nil {
		t.Fatalf("kein JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 480 || b.Dy() != 270 {
		t.Fatalf("Größe %v, want 480×270", b)
	}
	// Mittiger Zuschnitt erhält links rot und rechts blau.
	if r, _, bl, _ := img.At(20, 135).RGBA(); r>>8 < 150 || bl>>8 > 80 {
		t.Fatalf("links nicht rot")
	}
	if r, _, bl, _ := img.At(460, 135).RGBA(); bl>>8 < 150 || r>>8 > 80 {
		t.Fatalf("rechts nicht blau")
	}
	if res.Header.Get("ETag") == "" {
		t.Fatal("ETag fehlt")
	}
}

func TestVorschau_IfNoneMatchLiefert304(t *testing.T) {
	e := newVorschauEnv(t)
	_ = e.store.Save("b1", storage.BoardAblageId, "att-1", "bg.png", bytes.NewReader(pngBild(t, 64, 64)))
	e.board(t, "b1", `{"board":{"background":{"attachmentId":"att-1"}}}`)
	etag := e.get(t, "b1", nil).Header.Get("ETag")
	if res := e.get(t, "b1", map[string]string{"If-None-Match": etag}); res.StatusCode != http.StatusNotModified {
		t.Fatalf("want 304, got %d", res.StatusCode)
	}
}

func TestVorschau_InlineBildAusAltemBoard(t *testing.T) {
	e := newVorschauEnv(t)
	data := base64.StdEncoding.EncodeToString(pngBild(t, 300, 600)) // hochkant: Zuschnitt in der Höhe
	e.board(t, "b2", `{"board":{"background":{"attachmentId":"att-2"},"attachments":[{"id":"att-2","mime":"image/png","data":"`+data+`"}]}}`)
	res := e.get(t, "b2", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	if img, err := jpeg.Decode(res.Body); err != nil || img.Bounds().Dx() != 480 {
		t.Fatalf("kein 480er JPEG: %v", err)
	}
}

func TestVorschau_OhneHintergrund204(t *testing.T) {
	e := newVorschauEnv(t)
	e.board(t, "b3", `{"board":{"sprints":[]}}`)
	if res := e.get(t, "b3", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("ohne Hintergrund: want 204, got %d", res.StatusCode)
	}
	// Verweis auf einen Anhang, den es nicht gibt.
	e.board(t, "b4", `{"board":{"background":{"attachmentId":"weg"}}}`)
	if res := e.get(t, "b4", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("fehlender Anhang: want 204, got %d", res.StatusCode)
	}
	// Kein Bild (z. B. SVG-Text): 204, kein 500.
	_ = e.store.Save("b5", storage.BoardAblageId, "att-5", "bg.svg", bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)))
	e.board(t, "b5", `{"board":{"background":{"attachmentId":"att-5"}}}`)
	if res := e.get(t, "b5", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("SVG: want 204, got %d", res.StatusCode)
	}
}

func TestVorschau_UnbekanntesBoard404(t *testing.T) {
	e := newVorschauEnv(t)
	if res := e.get(t, "gibtsnicht", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
}

// Dekomprimierungsbombe: Die Bildfläche wird vor dem Dekodieren geprüft.
// Ein PNG mit 20000×20000 Bildpunkten ist als Datei winzig, dekodiert aber
// 1,6 GB groß.
func TestVorschauJPEG_LehntUebergrosseBildflaecheAb(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 20000, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, err := api.VorschauJPEG(buf.Bytes()); err == nil {
		t.Fatal("20000 Bildpunkte breit: Fehler erwartet")
	}
	if _, err := api.VorschauJPEG(pngBild(t, 64, 36)); err != nil {
		t.Fatalf("kleines Bild muss gehen: %v", err)
	}
}

// K-3 (Kritiker 07.10.): Die Flächengrenze greift auch, wenn keine Kante
// zu lang ist. 10000×10000 = 100 MP bei Kanten unter 16000.
func TestVorschauJPEG_LehntFlaecheUeberGrenzeAbAuchBeiKurzenKanten(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 10000, 10000))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, err := api.VorschauJPEG(buf.Bytes()); err == nil {
		t.Fatal("100 MP bei kurzen Kanten: Fehler erwartet")
	}
}

// K-4: Hochkant wird mittig zugeschnitten, nicht gestaucht. Oben 40 % rot,
// Mitte 20 % weiß, unten 40 % blau. Der 16:9-Ausschnitt liegt fast ganz im
// weißen Band; bei 25 % der Vorschauhöhe wäre ein gestauchtes Bild rot.
func TestVorschauJPEG_SchneidetHochkantMittigZu(t *testing.T) {
	w, h := 300, 600
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c := color.RGBA{255, 255, 255, 255}
		if y < h*2/5 {
			c = color.RGBA{200, 0, 0, 255}
		} else if y >= h*3/5 {
			c = color.RGBA{0, 0, 200, 255}
		}
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	out, err := api.VorschauJPEG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	v, _ := jpeg.Decode(bytes.NewReader(out))
	r, g, b, _ := v.At(240, 270/4).RGBA()
	if r>>8 < 200 || g>>8 < 200 || b>>8 < 200 {
		t.Fatalf("bei 25 %% Höhe nicht weiß (%d,%d,%d) — gestaucht statt zugeschnitten?", r>>8, g>>8, b>>8)
	}
}

// K-4: Durchsichtige Flächen werden weiß, nicht schwarz.
func TestVorschauJPEG_TransparenzWirdWeiss(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 160, 90)) // ganz durchsichtig
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	out, err := api.VorschauJPEG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	v, _ := jpeg.Decode(bytes.NewReader(out))
	if r, g, b, _ := v.At(240, 135).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("durchsichtig → (%d,%d,%d), want weiß", r>>8, g>>8, b>>8)
	}
}

// K-1: Der Browser muss jedes Mal nachfragen (no-cache), sonst zeigt er nach
// einem Bildwechsel bis zu einer Stunde das alte Bild.
func TestVorschau_CacheControlFragtJedesMalNach(t *testing.T) {
	e := newVorschauEnv(t)
	_ = e.store.Save("b1", storage.BoardAblageId, "att-1", "bg.png", bytes.NewReader(pngBild(t, 64, 36)))
	e.board(t, "b1", `{"board":{"background":{"attachmentId":"att-1"}}}`)
	cc := e.get(t, "b1", nil).Header.Get("Cache-Control")
	if cc != "private, no-cache" {
		t.Fatalf("Cache-Control %q, want \"private, no-cache\"", cc)
	}
}

// K-2: Ein endgültiger Fehlschlag wird gemerkt — auch wenn die Datei danach
// ersetzt würde, bleibt es bei 204 (gleiche Anhang-id = gleicher Inhalt).
// Gegenprobe: Ohne Merken würde der zweite Abruf die jetzt gültige Datei
// verkleinern und 200 liefern.
func TestVorschau_MerktEndgueltigenFehlschlag(t *testing.T) {
	e := newVorschauEnv(t)
	_ = e.store.Save("b6", storage.BoardAblageId, "att-6", "bg.png", bytes.NewReader([]byte("kein bild")))
	e.board(t, "b6", `{"board":{"background":{"attachmentId":"att-6"}}}`)
	if res := e.get(t, "b6", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("kaputt: want 204, got %d", res.StatusCode)
	}
	_ = e.store.Save("b6", storage.BoardAblageId, "att-6", "bg.png", bytes.NewReader(pngBild(t, 64, 36)))
	if res := e.get(t, "b6", nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("zweiter Abruf: want 204 aus dem Zwischenspeicher, got %d", res.StatusCode)
	}
}
