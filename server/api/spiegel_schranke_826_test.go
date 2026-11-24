package api_test

// Karte #826 (ADR-0141 §7, Schritt 2b): Spiegel-Schranke am Ops-Endpunkt und
// am Ganz-PUT — mit dem echten Fold.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const kopie826 = "0e5d8a2c-1111-5222-8333-44445555e826"

func legeKopie826An(t *testing.T, env *boardsTestEnv) (spiegelKarte string) {
	t.Helper()
	ctx := context.Background()
	if _, err := storage.NewBoardpaarRepo(env.db).KopieAnlegen(ctx, kopie826, "Kopie", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	// #836: Die Kopie ist freigegeben (bearbeitbar = 1) — hier entscheidet
	// allein der Server-Schalter wie vor dem Nachtrag. Die Fälle mit
	// bearbeitbar = 0 stehen in boardpaar_bearbeitbar_836_test.go.
	if err := storage.NewBoardpaarRepo(env.db).KopieBearbeitbarSetzen(ctx, kopie826, true); err != nil {
		t.Fatal(err)
	}
	kennung := strings.Repeat("K", 43)
	k := &schatten.Kopie{
		Struktur: schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
			Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}, {ID: "col-ready", Titel: "Ready"}}},
		Spiegel: []schatten.SpiegelEingang{{SpiegelID: kennung, Karte: &schatten.SpiegelKarte{Art: schatten.ArtKarte,
			Lane: schatten.BacklogLane, Spalte: "col-backlog", Title: "Spiegel", Labels: []string{}, Assignees: []string{},
			Comments: []schatten.SpiegelKommentar{}}}}}
	if _, err := storage.NewSchattenRepo(env.db).ErsetzeKopie(ctx, kopie826, func(state []byte) ([]byte, error) {
		neu, _, err := schatten.KopieAnwenden(state, k)
		return neu, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	return schatten.AbgeleiteteID(kennung)
}

func TestSpiegel826_OpsSchranke(t *testing.T) {
	env := newOpsTestEnv(t, true)
	karte := legeKopie826An(t, env)

	// Feld einer Spiegelkarte: 422, Protokoll nennt die Op.
	s, out := postOps(t, env, kopie826, `[{"type":"card.field","payload":{"cardId":"`+karte+`","field":"title","value":"x"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v", s, out)
	}
	protokollArt("spiegel-geschuetzt")(t, out)
	protokollIndex(protokollArt("spiegel-geschuetzt"), 0)(t, out)
	spiegelZiel826(t, out, karte)

	// Neue Karte in der gespiegelten Backlog-Lane: 422.
	s, out = postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Anlegen in gespiegelter Lane: %d %v", s, out)
	}
	spiegelZiel826(t, out, "neu")

	// Gemischter Batch: der Index nennt nur die verbotene Op (Index 1).
	s, out = postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}},
		{"type":"card.delete","payload":{"cardId":"`+karte+`"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("gemischt: %d %v", s, out)
	}
	protokollIndex(protokollArt("spiegel-geschuetzt"), 1)(t, out)

	// Was kein Spiegel ist, geht: eigene Lane und eine Karte darin.
	s, out = postOps(t, env, kopie826, `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"eigen","title":"Eigen","kind":"lane","cards":[]}}},
		{"type":"card.upsert","payload":{"card":{"id":"e1","title":"Eigen","columnId":"col-ready"},"sprintId":"eigen"}}]`)
	if s != http.StatusOK {
		t.Fatalf("frei: %d %v", s, out)
	}

	// Ganz-PUT, der die Spiegelkarte ändert: 403 mit Grund.
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/"+kopie826, nil))
	raw := mustReadAll(t, resp.Body)
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	geaendert := strings.Replace(string(raw), `"title":"Spiegel"`, `"title":"außen"`, 1)
	req := opsReq(t, http.MethodPut, env.srv.URL+"/boards/"+kopie826, []byte(geaendert))
	req.Header.Set("If-Match", etag)
	resp = mustDo(t, req)
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(text), "nur lesbar") {
		t.Errorf("PUT: %d %s", resp.StatusCode, text)
	}
	var fehler map[string]any
	_ = json.Unmarshal(text, &fehler)
	if _, ok := fehler["error"]; !ok {
		t.Errorf("PUT ohne error: %s", text)
	}
}

// spiegelZiel826: Das Protokoll trägt genau einen Eintrag „spiegel-geschuetzt“
// mit dieser Karte.
func spiegelZiel826(t *testing.T, out map[string]any, karte string) {
	t.Helper()
	liste, _ := out["protokoll"].([]any)
	if len(liste) != 1 {
		t.Fatalf("Protokoll: %v", out)
	}
	e := liste[0].(map[string]any)
	if e["art"] != "spiegel-geschuetzt" || e["cardId"] != karte {
		t.Errorf("Eintrag: %v, erwartet Karte %s", e, karte)
	}
}
