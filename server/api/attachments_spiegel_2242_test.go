package api_test

// F-2242 (Sprint-94-Kritiker Server K-4, Rest): Der Upload-Weg kennt Spiegel.
// Auf einer Kopie mit wirksamem Rückweg nimmt er für Kartenverzeichnisse von
// Spiegeln und wartenden Karten keine Datei mehr an (422, nichts auf der
// Platte) — dieselbe Unterscheidung wie die Kommentar-Schranke (F-2241). Auf
// normalen Boards und auf Kopien ohne Rückweg ändert sich nichts.

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func newUploadEnv2242(t *testing.T, rueckweg string) (*boardsTestEnv, *storage.ConfigRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	if err := cfg.Set(ctx, api.KonfigBoardpaarRueckweg, rueckweg, false, "test"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	boardsAPI := api.NewBoardsAPI(storage.NewBoardRepo(db)).WithFold(testMotor).
		MitBoardpaar(cfg, storage.NewBoardpaarRepo(db), storage.NewSchattenRepo(db))
	mux := http.NewServeMux()
	boardsAPI.Register(mux)
	api.NewAttachmentsAPI(storage.NewAttachmentStore(dir), 1<<20).WithSpiegelUploadRecht(boardsAPI).Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db, attachDir: dir}
	t.Cleanup(env.close)
	return env, cfg
}

func lade2242(t *testing.T, env *boardsTestEnv, board, karte, attId string) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("attachmentId", attId)
	fw, _ := mw.CreateFormFile("file", "beleg.txt")
	_, _ = fw.Write([]byte("inhalt"))
	_ = mw.Close()
	req, err := http.NewRequestWithContext(t.Context(), "POST", env.srv.URL+"/boards/"+board+"/attachments/"+karte, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp := mustDo(t, req)
	defer resp.Body.Close()
	return resp.StatusCode
}

func dateiDa2242(env *boardsTestEnv, board, karte string) bool {
	_, err := os.Stat(filepath.Join(env.attachDir, board, karte))
	return err == nil
}

func TestUpload2242_SpiegelUndWartendeGesperrt(t *testing.T) {
	env, _ := newUploadEnv2242(t, "an")
	spiegel := legeKopie826An(t, env)

	// Eigene Lane (ohne Marke) mit einer eigenen Karte: bleibt frei.
	if s, out := postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}},
	  {"type":"card.upsert","payload":{"card":{"id":"meine","title":"Meine","columnId":"col-backlog"},"sprintId":"eigen"}}]`); s != http.StatusOK {
		t.Fatalf("eigene Karte anlegen: %d %v", s, out)
	}
	// Neue Karte in der markierten Spalte: wird wartend.
	if s, out := postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`); s != http.StatusOK {
		t.Fatalf("wartende Karte anlegen: %d %v", s, out)
	}

	if s := lade2242(t, env, kopie826, spiegel, "att-1"); s != http.StatusUnprocessableEntity {
		t.Errorf("Spiegelkarte: %d, erwartet 422", s)
	}
	if dateiDa2242(env, kopie826, spiegel) {
		t.Errorf("Spiegelkarte: Verzeichnis auf der Platte angelegt")
	}
	if s := lade2242(t, env, kopie826, "neu", "att-2"); s != http.StatusUnprocessableEntity {
		t.Errorf("wartende Karte: %d, erwartet 422", s)
	}
	if dateiDa2242(env, kopie826, "neu") {
		t.Errorf("wartende Karte: Verzeichnis auf der Platte angelegt")
	}
	if s := lade2242(t, env, kopie826, "meine", "att-3"); s != http.StatusCreated {
		t.Errorf("eigene Karte: %d, erwartet 201", s)
	}
	if !dateiDa2242(env, kopie826, "meine") {
		t.Errorf("eigene Karte: Datei fehlt")
	}
}

// F-2374: Auch ohne wirksamen Rückweg (Server-Schalter aus oder Kopie nicht
// bearbeitbar) ist die Kopie im Lesemodus — Uploads an Spiegeln und wartenden
// Karten werden abgelehnt (dieselbe Kartenmenge wie mit Rückweg), eigene Karten
// bleiben frei, normale Boards bleiben unverändert.
func TestUpload2374_LesemodusSperrtSpiegel(t *testing.T) {
	for _, fall := range []string{"rueckweg-aus", "nicht-bearbeitbar"} {
		t.Run(fall, func(t *testing.T) {
			rueckweg := "aus"
			if fall == "nicht-bearbeitbar" {
				rueckweg = "an"
			}
			env, _ := newUploadEnv2242(t, rueckweg)
			spiegel := legeKopie826An(t, env)
			if fall == "nicht-bearbeitbar" {
				if err := storage.NewBoardpaarRepo(env.db).KopieBearbeitbarSetzen(context.Background(), kopie826, false); err != nil {
					t.Fatal(err)
				}
			}
			if s := lade2242(t, env, kopie826, spiegel, "att-1"); s != http.StatusUnprocessableEntity {
				t.Errorf("Spiegelkarte im Lesemodus: %d, erwartet 422", s)
			}
			if dateiDa2242(env, kopie826, spiegel) {
				t.Errorf("Spiegelkarte: Verzeichnis auf der Platte angelegt")
			}
			// Eigene Karte außerhalb markierter Lanes bleibt frei (Ops-Weg: Lesemodus lässt sie zu).
			if s, out := postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}},
			  {"type":"card.upsert","payload":{"card":{"id":"meine","title":"Meine","columnId":"col-backlog"},"sprintId":"eigen"}}]`); s != http.StatusOK {
				t.Fatalf("eigene Karte anlegen: %d %v", s, out)
			}
			if s := lade2242(t, env, kopie826, "meine", "att-3"); s != http.StatusCreated {
				t.Errorf("eigene Karte im Lesemodus: %d, erwartet 201", s)
			}
		})
	}
}

func TestUpload2242_NormalesBoardUnveraendert(t *testing.T) {
	// Normales Board im selben Server (Rückweg an oder aus, aber keine Kopie).
	for _, rueckweg := range []string{"an", "aus"} {
		env2, _ := newUploadEnv2242(t, rueckweg)
		state := `{"version":"2","board":{"id":"nb","title":"N","sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","title":"K","columnId":"col-ready"}]}]}}`
		if _, err := storage.NewBoardRepo(env2.db).Insert(context.Background(), "nb", "N", "2", []byte(state)); err != nil {
			t.Fatal(err)
		}
		if s := lade2242(t, env2, "nb", "k1", "att-1"); s != http.StatusCreated {
			t.Errorf("normales Board (Rückweg %s): %d, erwartet 201", rueckweg, s)
		}
	}
}
