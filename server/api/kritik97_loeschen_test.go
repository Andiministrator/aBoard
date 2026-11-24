package api_test

// Kritiker Sprint 97 K-1: DELETE /boards/{id} auf einer Kopie mit
// unbestätigtem Journal löscht nicht, sondern antwortet 409 mit der Zahl —
// sonst umginge „Board löschen…“ im Browser (#845) die Warnung des
// Admin-Notwegs (F-2240).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestKritik97_LoeschenMitOffenemJournalIst409(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	if s, out := postOps(t, env, kopie826, feldOp836(karte, "außen")); s != http.StatusOK {
		t.Fatalf("Feld: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 1 {
		t.Fatalf("Journal: %+v", j)
	}

	req, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/boards/"+kopie826, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusConflict || body["offene_aenderungen"] != float64(1) {
		t.Fatalf("DELETE: %d %v, erwartet 409 mit offene_aenderungen 1", resp.StatusCode, body)
	}
	if _, err := storage.NewBoardRepo(env.db).Get(context.Background(), kopie826); err != nil {
		t.Fatalf("Board trotz 409 weg: %v", err)
	}
	if j := journal827(t, env); len(j) != 1 {
		t.Errorf("Journal nach 409 verändert: %+v", j)
	}
}

// Ohne offenes Journal (bestätigt) löscht DELETE wie bisher.
func TestKritik97_LoeschenOhneOffenesJournal(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)
	if s, out := postOps(t, env, kopie826, feldOp836(karte, "außen")); s != http.StatusOK {
		t.Fatalf("Feld: %d %v", s, out)
	}
	if _, err := env.db.Exec(`UPDATE boardpaar_journal SET bestaetigt_at = '2026-10-20T08:00:00Z' WHERE board_id = ?`, kopie826); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/boards/"+kopie826, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d, erwartet 204", resp.StatusCode)
	}
}
