package api_test

// kommentar_anhaenge_endpunkt_test.go — Karte #678 (ADR-0135), Ende-zu-Ende
// über POST /boards/{id}/ops: comment.delete nimmt die Datei des Kommentars
// mit, die übrigen Dateien der Karte bleiben.
//
// Der Ganz-Board-PUT löscht dagegen nie Dateien (KA-1).
//
// Setzt den Einbau in server/api/boards_ops.go voraus (postOps ruft
// kommentarAnhaengeZumLoeschen und loescheKommentarAnhaenge).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestOpsCommentDeleteLoeschtKommentarDatei(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	store := storage.NewAttachmentStore(t.TempDir())
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).WithFold(testMotor).WithAttachments(store).Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)

	for _, id := range []string{"a1", "a2"} {
		if err := store.Save("b1", "k1", id, id+".txt", strings.NewReader("xyz")); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"version": "2", "board": map[string]any{
		"id": "b1", "title": "B", "nextDisplayId": 2,
		"columnDefs": []any{map[string]any{"id": "col-ready", "title": "Ready"}},
		"sprints": []any{map[string]any{"id": "s1", "title": "S", "kind": "sprint", "cards": []any{
			map[string]any{"id": "k1", "displayId": 1, "title": "K", "columnId": "col-ready",
				"comments": []any{map[string]any{"id": "c1", "author": "Andi", "text": "x", "created": "2026-10-04"}},
				"attachments": []any{
					map[string]any{"id": "a1", "filename": "a1.txt", "commentId": "c1"},
					map[string]any{"id": "a2", "filename": "a2.txt"},
				}},
		}}},
	}})
	resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards", body))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	resp.Body.Close()

	code, out := postOps(t, env, "b1", `[{"type":"comment.delete","payload":{"cardId":"k1","commentId":"c1"}}]`)
	if code != http.StatusOK {
		t.Fatalf("ops: %d %v", code, out)
	}
	if _, _, err := store.Open("b1", "k1", "a1"); !errors.Is(err, storage.ErrAttachmentNotFound) {
		t.Errorf("Datei a1 des gelöschten Kommentars liegt noch: %v", err)
	}
	if rc, _, err := store.Open("b1", "k1", "a2"); err != nil {
		t.Errorf("Karten-Anhang a2 muss bleiben: %v", err)
	} else {
		rc.Close()
	}
}

// KA-1 (ADR-0135 §3): Ein Ganz-Board-PUT löscht KEINE Dateien. Der Browser
// schickt auch heute PUTs („Meine Version behalten“, Rückfall bei 4xx); ein
// veralteter Stand dürfte sonst die Dateien eines frisch angelegten Kommentars
// endgültig vernichten. Ein verlorener Eintrag ist wiederherstellbar, eine
// gelöschte Datei nicht.
func TestPutOhneKommentarLaesstDateienLiegen(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	store := storage.NewAttachmentStore(t.TempDir())
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).WithFold(testMotor).WithAttachments(store).Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)

	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if err := store.Save("b1", "k1", id, id+".txt", strings.NewReader("xyz")); err != nil {
			t.Fatal(err)
		}
	}
	stand := func(kommentare, anhaenge []any) []byte {
		body, _ := json.Marshal(map[string]any{"version": "2", "board": map[string]any{
			"id": "b1", "title": "B", "nextDisplayId": 2,
			"columnDefs": []any{map[string]any{"id": "col-ready", "title": "Ready"}},
			"sprints": []any{map[string]any{"id": "s1", "title": "S", "kind": "sprint", "cards": []any{
				map[string]any{"id": "k1", "displayId": 1, "title": "K", "columnId": "col-ready",
					"comments": kommentare, "attachments": anhaenge},
			}}},
		}})
		return body
	}
	komm := func(id string) any {
		return map[string]any{"id": id, "author": "Andi", "text": "x", "created": "2026-10-04"}
	}
	anh := func(id, kommentar string) any {
		m := map[string]any{"id": id, "filename": id + ".txt"}
		if kommentar != "" {
			m["commentId"] = kommentar
		}
		return m
	}
	vorhanden := func(id string) bool {
		rc, _, err := store.Open("b1", "k1", id)
		if err == nil {
			rc.Close()
			return true
		}
		if !errors.Is(err, storage.ErrAttachmentNotFound) {
			t.Fatalf("Open %s: %v", id, err)
		}
		return false
	}

	resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards", stand(
		[]any{komm("c1"), komm("c2")},
		[]any{anh("a1", "c1"), anh("a2", ""), anh("a3", "c1"), anh("a4", "c2")})))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	// c1 fällt im PUT, a1 fehlt dort auch als Eintrag: alle Dateien bleiben.
	req := opsReq(t, http.MethodPut, env.srv.URL+"/boards/b1", stand(
		[]any{komm("c2")}, []any{anh("a2", ""), anh("a3", "c1"), anh("a4", "c2")}))
	req.Header.Set("If-Match", etag)
	resp = mustDo(t, req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		if !vorhanden(id) {
			t.Errorf("Datei %s muss bleiben", id)
		}
	}
}
