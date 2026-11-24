package api_test

// Karte #780 (ADR-0140 §1, B-3): Kein Board-Schreibweg der Menschen-API und
// kein Leser erreicht die Schatten (Menschen-API, Agenten-Werkzeuge). Einzige
// Ausnahme seit #782: der iCal-Feed außen (api/ical.go, ADR-0140 §6), den der
// Quelltext-Wächter eng zulässt. Je Weg: Schatten anlegen, Momentaufnahme,
// Weg fahren und belegen, dass er wirklich geschrieben hat, dann die Schatten
// zeichengleich vergleichen.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func boardStand780(t *testing.T, env *boardsTestEnv) *storage.Board {
	t.Helper()
	b, err := storage.NewBoardRepo(env.db).Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchatten780_PutBoard(t *testing.T) {
	env := newOpsTestEnv(t, false)
	legeOpsBoardAn(t, env)
	schattentest.Bestuecke(t, env.db, "b1")
	schattentest.MussBestueckt(t, env.db, "b1")
	vorher := schattentest.Momentaufnahme(t, env.db)
	alt := boardStand780(t, env)

	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
	etag := resp.Header.Get("ETag")
	var envl map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &envl)
	resp.Body.Close()
	envl["board"].(map[string]any)["title"] = "Neu per PUT"
	body, _ := json.Marshal(envl)
	req := opsReq(t, http.MethodPut, env.srv.URL+"/boards/b1", body)
	req.Header.Set("If-Match", etag)
	resp = mustDo(t, req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	if neu := boardStand780(t, env); !strings.Contains(string(neu.StateJSON), "Neu per PUT") {
		t.Fatalf("PUT hat nicht geschrieben: updated_at %d → %d", alt.UpdatedAt, neu.UpdatedAt)
	}
	schattentest.Unveraendert(t, env.db, vorher, "PUT /boards/{id}")
}

func TestSchatten780_OpsFold(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	schattentest.Bestuecke(t, env.db, "b1")
	schattentest.MussBestueckt(t, env.db, "b1")
	vorher := schattentest.Momentaufnahme(t, env.db)
	alt := boardStand780(t, env)

	code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Neu per Op"}}]`)
	if code != http.StatusOK {
		t.Fatalf("ops: %d %v", code, out)
	}
	if neu := boardStand780(t, env); !strings.Contains(string(neu.StateJSON), "Neu per Op") {
		t.Fatalf("Op hat nicht geschrieben: updated_at %d → %d", alt.UpdatedAt, neu.UpdatedAt)
	}
	schattentest.Unveraendert(t, env.db, vorher, "POST /boards/{id}/ops")
}

// Leser: Die Board-Endpunkte der Menschen-API liefern keine Schatten aus —
// weder das Board selbst noch die Liste.
func TestSchatten780_LeserMenschenAPI(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	schattentest.Bestuecke(t, env.db, "b1")
	schattentest.MussBestueckt(t, env.db, "b1")
	for _, pfad := range []string{"/boards/b1", "/boards"} {
		resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+pfad, nil))
		text := string(mustReadAll(t, resp.Body))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", pfad, resp.StatusCode)
		}
		if !strings.Contains(text, "Ops-Board") {
			t.Fatalf("GET %s liefert das Board nicht: %.200s", pfad, text)
		}
		if strings.Contains(text, schattentest.Marker) || strings.Contains(text, "innen.example") {
			t.Errorf("GET %s liefert Schatten aus: %.400s", pfad, text)
		}
	}
}

// Schleuse-Versand: Die Antwort kommt als Kommentar „extern“ an die Karte
// (boards.Update im Versand-Worker) — die Schatten bleiben unberührt.
func TestSchatten780_SchleuseVersand(t *testing.T) {
	e := neueVersandEnv(t)
	schattentest.Bestuecke(t, e.db, slBoard)
	schattentest.MussBestueckt(t, e.db, slBoard)
	vorher := schattentest.Momentaufnahme(t, e.db)

	id, sha, _ := e.erstelle(t)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if n := len(externeKommentare(e.karte(t))); n != 1 {
		t.Fatalf("Versand hat nicht geschrieben: %d externe Kommentare", n)
	}
	schattentest.Unveraendert(t, e.db, vorher, "Schleuse-Versand")
}

// Leser der Agenten-Werkzeuge (K-3 aus der Kritik): Boards, Board lesen,
// Verlauf, Mitglieder, Volltextsuche und Termine liefern keine Schatten aus —
// auch nicht die Termin-Schatten im abgefragten Zeitraum.
func TestSchatten780_LeserAgentenWerkzeuge(t *testing.T) {
	e := newEnv671(t, true)
	schattentest.Bestuecke(t, e.db, "b1")
	schattentest.MussBestueckt(t, e.db, "b1")
	for _, pfad := range []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/werkzeuge/boards/b1/verlauf",
		"/werkzeuge/boards/b1/mitglieder", "/werkzeuge/karten?q=Karte", "/werkzeuge/karten?q=SCHATTEN",
		"/werkzeuge/termine?ab=2026-10-01&bis=2026-10-31"} {
		s, b := e.rufe(t, e.projekt, "", "GET", pfad, "")
		if s != http.StatusOK {
			t.Fatalf("GET %s: %d %s", pfad, s, b)
		}
		if strings.Contains(string(b), schattentest.Marker) || strings.Contains(string(b), "innen.example") {
			t.Errorf("GET %s liefert Schatten aus: %.400s", pfad, b)
		}
	}
	// Gegenkontrolle: Die Suche findet echte Karten, ist also nicht leer gelaufen.
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/karten?q=Karte", ""); s != http.StatusOK || !strings.Contains(string(b), "Karte") {
		t.Fatalf("Suche ohne Treffer: %d %s", s, b)
	}
}
