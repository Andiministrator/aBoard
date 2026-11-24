package api

// Verify 07.10. (V-1, V-2) zu GET /boards/{id}/vorschaubild: Der einzige
// Dekodier-Platz muss nach einer Panik frei werden, und zwei gleichzeitige
// Anfragen auf dasselbe Bild dürfen es nur einmal rechnen. Intern, weil der
// Dekoder über `vorschauVerkleinern` ersetzt wird.

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func vorschauIntern(t *testing.T) (*httptest.Server, *storage.BoardRepo, *storage.AttachmentStore) {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	boards := storage.NewBoardRepo(db)
	store := storage.NewAttachmentStore(t.TempDir())
	mux := http.NewServeMux()
	NewBoardVorschauAPI(boards, store).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, boards, store
}

func kleinesPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 32, 18))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func boardMitBild(t *testing.T, boards *storage.BoardRepo, store *storage.AttachmentStore, id string) {
	t.Helper()
	if err := store.Save(id, storage.BoardAblageId, "att-"+id, "bg.png", bytes.NewReader(kleinesPNG(t))); err != nil {
		t.Fatal(err)
	}
	if _, err := boards.Insert(context.Background(), id, id, "1",
		[]byte(`{"board":{"background":{"attachmentId":"att-`+id+`"}}}`)); err != nil {
		t.Fatal(err)
	}
}

func holeMitFrist(t *testing.T, url string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Abruf %s: %v (hängt der Dekodier-Platz?)", url, err)
	}
	res.Body.Close()
	return res.StatusCode
}

// V-1: Panikt der Dekoder, antwortet die Anfrage mit 204, und der Platz ist
// danach frei — die nächste Vorschau kommt durch.
func TestVorschauIntern_PanikGibtDenPlatzFrei(t *testing.T) {
	srv, boards, store := vorschauIntern(t)
	boardMitBild(t, boards, store, "p1")
	boardMitBild(t, boards, store, "p2")
	alt := vorschauVerkleinern
	t.Cleanup(func() { vorschauVerkleinern = alt })

	vorschauVerkleinern = func([]byte) ([]byte, error) { panic("kaputter Dekoder") }
	if c := holeMitFrist(t, srv.URL+"/boards/p1/vorschaubild"); c != http.StatusNoContent {
		t.Fatalf("Panik: want 204, got %d", c)
	}
	vorschauVerkleinern = alt
	if c := holeMitFrist(t, srv.URL+"/boards/p2/vorschaubild"); c != http.StatusOK {
		t.Fatalf("nach der Panik: want 200, got %d", c)
	}
}

// V-2: Zwei gleichzeitige Anfragen auf dasselbe Bild — gerechnet wird einmal,
// die zweite nimmt das Ergebnis aus dem Zwischenspeicher.
func TestVorschauIntern_GleichzeitigeAnfragenRechnenEinmal(t *testing.T) {
	srv, boards, store := vorschauIntern(t)
	boardMitBild(t, boards, store, "g1")
	alt := vorschauVerkleinern
	t.Cleanup(func() { vorschauVerkleinern = alt })

	var laeufe atomic.Int32
	weiter := make(chan struct{})
	vorschauVerkleinern = func(roh []byte) ([]byte, error) {
		laeufe.Add(1)
		<-weiter
		return alt(roh)
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = holeMitFrist(t, srv.URL+"/boards/g1/vorschaubild")
		}(i)
	}
	time.Sleep(200 * time.Millisecond) // beide Anfragen sind angekommen, eine wartet am Platz
	close(weiter)
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("Antworten %v, want beide 200", codes)
	}
	if n := laeufe.Load(); n != 1 {
		t.Fatalf("Dekoder lief %d-mal, want 1", n)
	}
}
