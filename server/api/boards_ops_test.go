package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Gemeinsamer Motor für alle Tests dieser Datei: Kompilieren und Pool kosten
// je Motor rund 1 ms, sind aber goroutinensicher teilbar.
var testMotor = func() *fold.Motor {
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		panic(err)
	}
	return m
}()

func newOpsTestEnv(t *testing.T, mitFold bool) *boardsTestEnv {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	mux := http.NewServeMux()
	b := api.NewBoardsAPI(storage.NewBoardRepo(db))
	if mitFold {
		b.WithFold(testMotor)
	}
	b.Register(mux)
	env := &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
	t.Cleanup(env.close)
	return env
}

// opsBoard: ein Sprint s1 mit zwei Karten, vier Spalten, nextDisplayId 3.
func legeOpsBoardAn(t *testing.T, env *boardsTestEnv) {
	t.Helper()
	karte := func(id string, nr int) map[string]any {
		return map[string]any{"id": id, "displayId": nr, "title": "Karte " + id, "description": "",
			"columnId": "col-ready", "owner": "", "assignees": []any{}, "labels": []any{},
			"comments": []any{}, "activity": []any{}, "links": []any{}, "qa": false,
			"critical": false, "blocked": false, "archived": false}
	}
	body, _ := json.Marshal(map[string]any{"version": "2", "board": map[string]any{
		"id": "b1", "title": "Ops-Board", "nextDisplayId": 3,
		"columnDefs": []any{
			map[string]any{"id": "col-backlog", "title": "Backlog"},
			map[string]any{"id": "col-ready", "title": "Ready"},
			map[string]any{"id": "col-erledigt", "title": "Erledigt"},
		},
		"sprints": []any{
			map[string]any{"id": "__backlog__", "title": "Backlog", "kind": "backlog", "cards": []any{}},
			map[string]any{"id": "s1", "title": "Sprint 1", "kind": "sprint", "cards": []any{karte("k1", 1), karte("k2", 2)}},
		},
	}})
	resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards", body))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func opsReq(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func postOps(t *testing.T, env *boardsTestEnv, board, ops string) (int, map[string]any) {
	t.Helper()
	resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards/"+board+"/ops", []byte(`{"ops":`+ops+`}`)))
	defer resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &out)
	return resp.StatusCode, out
}

type opsBoardStand struct {
	UpdatedAt string
	Board     struct {
		Title         string `json:"title"`
		NextDisplayID int    `json:"nextDisplayId"`
		Sprints       []struct {
			ID    string `json:"id"`
			Cards []struct {
				ID        string           `json:"id"`
				DisplayID int              `json:"displayId"`
				Title     string           `json:"title"`
				ColumnID  string           `json:"columnId"`
				QA        bool             `json:"qa"`
				Comments  []map[string]any `json:"comments"`
				Owner     *string          `json:"owner"`
			} `json:"cards"`
		} `json:"sprints"`
	} `json:"board"`
}

func leseOpsBoard(t *testing.T, env *boardsTestEnv) opsBoardStand {
	t.Helper()
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
	defer resp.Body.Close()
	var s opsBoardStand
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &s); err != nil {
		t.Fatal(err)
	}
	s.UpdatedAt = resp.Header.Get("ETag")
	return s
}

func (s opsBoardStand) karte(id string) (sprint string, idx int) {
	for _, sp := range s.Board.Sprints {
		for i, c := range sp.Cards {
			if c.ID == id {
				return sp.ID, i
			}
		}
	}
	return "", -1
}

func TestOps_OhneMotor503(t *testing.T) {
	env := newOpsTestEnv(t, false)
	legeOpsBoardAn(t, env)
	if code, _ := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`); code != http.StatusServiceUnavailable {
		t.Fatalf("Status %d, erwartet 503", code)
	}
}

// TestOps_HappyPath: drei Op-Typen in einem Batch wirken zusammen; die neue
// Karte bekommt die nächste displayId vom Server; die Quittung nennt
// Server-IDs; ETag ändert sich.
func TestOps_HappyPath(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	vorher := leseOpsBoard(t, env)
	code, out := postOps(t, env, "b1", `[
	 {"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}},
	 {"type":"comment.add","payload":{"cardId":"k2","comment":{"id":"c1","author":"Agent","text":"hallo","created":"2026-09-28"}}},
	 {"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k3","title":"Neu","description":"","owner":"",
	   "assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":false,"critical":false,"blocked":false,"archived":false}}}
	]`)
	if code != http.StatusOK {
		t.Fatalf("Status %d: %v", code, out)
	}
	quittung, _ := out["ops"].([]any)
	if len(quittung) != 3 {
		t.Fatalf("Quittung: %v", out)
	}
	nachher := leseOpsBoard(t, env)
	if nachher.UpdatedAt == vorher.UpdatedAt {
		t.Error("ETag unverändert")
	}
	sp, i := nachher.karte("k1")
	if !nachher.Board.Sprints[1].Cards[i].QA || sp != "s1" {
		t.Error("card.field hat nicht gewirkt")
	}
	_, i = nachher.karte("k2")
	if k := nachher.Board.Sprints[1].Cards[i].Comments; len(k) != 1 || k[0]["author"] != "Agent" {
		t.Errorf("comment.add: %v — bei Menschen bleibt der Autor aus dem Payload (K-A2 gilt nur für Agenten)", k)
	}
	_, i = nachher.karte("k3")
	if i < 0 || nachher.Board.Sprints[1].Cards[i].DisplayID != 3 || nachher.Board.NextDisplayID != 4 {
		t.Errorf("neue Karte: idx %d, nextDisplayId %d", i, nachher.Board.NextDisplayID)
	}
}

// TestOps_ServerStempeltZeitUndID: Die Ordnung im Batch ist die Eingangs-
// ordnung, nicht die Client-Uhr. Beide Ops setzen denselben Titel; die erste
// trägt einen Client-ts in der Zukunft. Gälte der Client-ts, gewänne sie.
func TestOps_ServerStempeltZeitUndID(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	code, out := postOps(t, env, "b1", `[
	 {"id":"fremd-1","ts":"2099-01-01T00:00:00.000Z","seq":99,"actor":"gefälscht","schemaVersion":1,"type":"card.field","payload":{"cardId":"k1","field":"title","value":"erste"}},
	 {"id":"fremd-2","ts":"2000-01-01T00:00:00.000Z","type":"card.field","payload":{"cardId":"k1","field":"title","value":"zweite"}}
	]`)
	if code != http.StatusOK {
		t.Fatalf("Status %d: %v", code, out)
	}
	for _, q := range out["ops"].([]any) {
		if id := q.(map[string]any)["id"].(string); strings.HasPrefix(id, "fremd") {
			t.Errorf("Client-ID %q übernommen", id)
		}
	}
	s := leseOpsBoard(t, env)
	_, i := s.karte("k1")
	if got := s.Board.Sprints[1].Cards[i].Title; got != "zweite" {
		t.Fatalf("Titel %q — die Client-Zeit hat die Ordnung bestimmt", got)
	}
}

// protokollArt prüft, dass das 422-Protokoll genau einen Eintrag der
// erwarteten Art trägt — sonst könnte ein Fall aus einem anderen Grund
// abgelehnt werden als dem, den er belegen soll.
func protokollArt(art string) func(t *testing.T, out map[string]any) {
	return func(t *testing.T, out map[string]any) {
		p, _ := out["protokoll"].([]any)
		if len(p) != 1 || p[0].(map[string]any)["art"] != art {
			t.Errorf("Protokoll %v, erwartet genau einen Eintrag der Art %q", out["protokoll"], art)
		}
	}
}

// protokollIndex prüft zusätzlich, dass der eine Protokolleintrag den Index
// der Op im Batch nennt (Karte #565): Nur daran erkennt der Browser, welche
// seiner Ops abgelehnt wurde — die `op`-id vergibt der Server.
func protokollIndex(pruefe func(t *testing.T, out map[string]any), index int) func(t *testing.T, out map[string]any) {
	return func(t *testing.T, out map[string]any) {
		pruefe(t, out)
		p, _ := out["protokoll"].([]any)
		if len(p) != 1 {
			return
		}
		if i, ok := p[0].(map[string]any)["index"].(float64); !ok || int(i) != index {
			t.Errorf("Protokoll %v, erwartet index=%d", p[0], index)
		}
	}
}

// grund prüft, dass die 400-Meldung der ersten Op den Text enthält.
func grund(teil string) func(t *testing.T, out map[string]any) {
	return func(t *testing.T, out map[string]any) {
		f, _ := out["ops"].([]any)
		if len(f) == 0 || !strings.Contains(fmt.Sprint(f[0].(map[string]any)["grund"]), teil) {
			t.Errorf("Meldung %v, erwartet Grund mit %q", out["ops"], teil)
		}
	}
}

// protokollZiel prüft ein 422-Protokoll mit genau einem Eintrag
// `ziel-fehlt`, der das fehlende Ziel unter schluessel nennt (F-1146).
func protokollZiel(schluessel, wert string) func(t *testing.T, out map[string]any) {
	return func(t *testing.T, out map[string]any) {
		protokollArt("ziel-fehlt")(t, out)
		p, _ := out["protokoll"].([]any)
		if len(p) == 1 && p[0].(map[string]any)[schluessel] != wert {
			t.Errorf("Protokoll %v, erwartet %s=%q", p[0], schluessel, wert)
		}
	}
}

// neueKarteJSON: eine vollständige neue Karte (keine Entkernung, kein
// Massstab-Befund) für card.upsert.
func neueKarteJSON(id string) string {
	return `{"id":"` + id + `","title":"Neu","description":"","owner":"","assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":false,"critical":false,"blocked":false,"archived":false}`
}

// TestOps_AblehnungSchreibtNichts: Jede Ablehnung lässt das Board samt ETag
// unverändert — auch wenn andere Ops im selben Batch gültig wären.
func TestOps_AblehnungSchreibtNichts(t *testing.T) {
	gueltig := `{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}`
	faelle := []struct {
		name   string
		ops    string
		status int
		pruefe func(t *testing.T, out map[string]any)
	}{
		{"unbekannter Schlüssel mit Vorschlag", `[` + gueltig + `,{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1","toColumnId":"col-erledigt"}}]`, 400,
			func(t *testing.T, out map[string]any) {
				f := out["ops"].([]any)[0].(map[string]any)
				u := f["unbekannt"].([]any)[0].(map[string]any)
				if f["index"].(float64) != 1 || u["key"] != "toColumnId" || u["meant"] != "toColId" {
					t.Errorf("Meldung: %v", f)
				}
			}},
		{"Pflichtfeld fehlt", `[{"type":"card.field","payload":{"field":"qa","value":true}}]`, 400, nil},
		{"unbekannter Typ", `[{"type":"card.zaubern","payload":{}}]`, 400, nil},
		{"payload kein Objekt", `[{"type":"card.field","payload":"x"}]`, 400, nil},
		{"Kommentar kein Objekt", `[{"type":"comment.add","payload":{"cardId":"k1","comment":["x"]}}]`, 400, nil},
		// F-1152 (Karte #561): Formen, die fold() still verwarf — 400 vor der Sperre.
		{"card.id keine Zeichenkette", `[{"type":"card.upsert","payload":{"card":{"id":123,"title":"x"}}}]`, 400, grund("card.id")},
		{"card.id leer", `[{"type":"card.upsert","payload":{"card":{"id":"","title":"x"}}}]`, 400, grund("card.id")},
		{"card kein Objekt", `[{"type":"card.upsert","payload":{"card":"x"}}]`, 400, grund("card muss")},
		{"sprintId keine Zeichenkette", `[{"type":"card.upsert","payload":{"sprintId":7,"card":` + neueKarteJSON("k7") + `}}]`, 400, grund("sprintId")},
		{"swimlane.upsert ohne sprint.id", `[{"type":"swimlane.upsert","payload":{"sprint":{"title":"x"}}}]`, 400, grund("sprint.id")},
		{"Wartungs-Op", `[{"type":"maintenance.compact","payload":{}}]`, 400, nil},
		{"leer", `[]`, 400, nil},
		{"Hülle verletzt (Titel leer)", `[{"type":"board.field","payload":{"field":"title","value":""}}]`, 422, nil},
		{"wirkungslos (gesperrtes Feld)", `[` + gueltig + `,{"type":"card.field","payload":{"cardId":"k1","field":"displayId","value":9}}]`, 422, protokollIndex(protokollArt("wirkungslos"), 1)},
		{"Klemmung (Spalte fehlt)", `[{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1","toColId":"col-gibtsnicht"}}]`, 422, protokollIndex(protokollArt("klemmung"), 0)},
		{"Entkernung (Teil-Upsert)", `[{"type":"card.upsert","payload":{"sprintId":"s1","card":{"id":"k1","title":"nur Titel"}}}]`, 422, protokollIndex(protokollArt("entkernung"), 0)},
		// F-1146 (Karte #558): Ops auf ein Ziel, das es im Stand vor ihnen nicht gibt.
		{"Ziel fehlt: card.field", `[` + gueltig + `,{"type":"card.field","payload":{"cardId":"geraten","field":"qa","value":true}}]`, 422, protokollIndex(protokollZiel("cardId", "geraten"), 1)},
		{"Ziel fehlt: comment.add", `[{"type":"comment.add","payload":{"cardId":"geraten","comment":{"id":"c9","author":"A","text":"x","created":"2026-09-28"}}}]`, 422, protokollZiel("cardId", "geraten")},
		{"Ziel fehlt: card.move (Karte)", `[{"type":"card.move","payload":{"cardId":"geraten","toSprintId":"s1","toColId":"col-ready"}}]`, 422, protokollZiel("cardId", "geraten")},
		{"Ziel fehlt: card.move (Swimlane)", `[{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s-geraten","toColId":"col-ready"}}]`, 422, protokollZiel("sprintId", "s-geraten")},
		{"Ziel fehlt: card.upsert in fehlende Swimlane", `[{"type":"card.upsert","payload":{"sprintId":"s-geraten","columnId":"col-ready","card":` + neueKarteJSON("k7") + `}}]`, 422, protokollZiel("sprintId", "s-geraten")},
		{"Ziel fehlt: swimlane.move", `[{"type":"swimlane.move","payload":{"sprintId":"s-geraten","targetIndex":0}}]`, 422, protokollZiel("sprintId", "s-geraten")},
		{"Ziel fehlt: im Batch vorher gelöscht", `[{"type":"card.delete","payload":{"cardId":"k1"}},` + gueltig + `]`, 422, protokollIndex(protokollZiel("cardId", "k1"), 1)},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			env := newOpsTestEnv(t, true)
			legeOpsBoardAn(t, env)
			vorher := leseOpsBoard(t, env)
			code, out := postOps(t, env, "b1", f.ops)
			if code != f.status {
				t.Fatalf("Status %d, erwartet %d: %v", code, f.status, out)
			}
			if f.pruefe != nil {
				f.pruefe(t, out)
			}
			if nachher := leseOpsBoard(t, env); nachher.UpdatedAt != vorher.UpdatedAt {
				t.Fatal("Board trotz Ablehnung geschrieben")
			}
		})
	}
}

// TestOps_ZielPruefungLaesstDurch (F-1146, Karte #558): Die Ziel-Prüfung
// kennt den Stand VOR jeder Op, nicht nur den Snapshot — eine im selben Batch
// angelegte Karte ist vorhanden. Und Löschungen prüfen ihr Ziel nicht: Die
// Wiederholung eines Batches nach einem Verbindungsabbruch muss gelingen.
func TestOps_ZielPruefungLaesstDurch(t *testing.T) {
	faelle := []struct{ name, ops string }{
		{"im Batch angelegt, dann geändert", `[{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":` + neueKarteJSON("k7") + `}},
		  {"type":"card.field","payload":{"cardId":"k7","field":"qa","value":true}},
		  {"type":"comment.add","payload":{"cardId":"k7","comment":{"id":"c7","author":"A","text":"x","created":"2026-09-28"}}}]`},
		{"card.delete wiederholt", `[{"type":"card.delete","payload":{"cardId":"k1"}},{"type":"card.delete","payload":{"cardId":"k1"}}]`},
		{"comment.delete auf fehlende Karte", `[{"type":"comment.delete","payload":{"cardId":"geraten","commentId":"c1"}}]`},
		{"swimlane.delete auf fehlende Swimlane", `[{"type":"swimlane.delete","payload":{"sprintId":"s-geraten"}}]`},
		// F-1152 (Karte #561) + ADR-0129 §4, Nachtrag Sprint 72, Punkt 4: Eine Op,
		// die den Stand nicht ändert, bleibt erlaubt — sonst scheitert jede
		// Wiederholung nach verlorener Antwort.
		{"card.field mit unverändertem Wert", `[{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Karte k1"}}]`},
		{"card.move an dieselbe Stelle", `[{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1","toColId":"col-ready"}}]`},
		{"card.move ohne toColId (Spalte bleibt)", `[{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1"}}]`},
		{"card.field ohne value (Feld leeren)", `[{"type":"card.field","payload":{"cardId":"k1","field":"description"}}]`},
		{"sprintId null bei Neuanlage (Backlog)", `[{"type":"card.upsert","payload":{"sprintId":null,"card":` + neueKarteJSON("k8") + `}}]`},
		{"swimlane.upsert wiederholt", `[{"type":"swimlane.upsert","payload":{"sprint":{"id":"s1","title":"Sprint 1","kind":"sprint"}}}]`},
		{"card.upsert auf Bestand mit fremder sprintId", `[{"type":"card.upsert","payload":{"sprintId":"s-geraten","card":{"id":"k1","displayId":1,"title":"Karte k1","description":"","columnId":"col-ready","owner":"","assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":true,"critical":false,"blocked":false,"archived":false}}}]`},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			env := newOpsTestEnv(t, true)
			legeOpsBoardAn(t, env)
			if code, out := postOps(t, env, "b1", f.ops); code != http.StatusOK {
				t.Fatalf("Status %d, erwartet 200: %v", code, out)
			}
		})
	}
}

// TestOps_NullFeldIstKeinVerlust (F-1161, Karte #574): Ein Volltext-Upsert,
// der ein Feld weglässt, dessen Bestandswert null ist, verliert nichts und
// muss durchgehen. Genau diesen Upsert schickt der Browser aus diffToOps,
// sobald ein Feld verschwindet (ADR-0129 §4, Nachtrag Sprint 72). Bis F-1161
// meldete der Fold „entkernung“, und der Server antwortete 422.
func TestOps_NullFeldIstKeinVerlust(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	voll := func(extra string) string {
		return `[{"type":"card.upsert","payload":{"sprintId":"s1","card":{"id":"k1","displayId":1,"title":"Karte k1","description":"","columnId":"col-ready","owner":"","assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":false,"critical":false,"blocked":false,"archived":false` + extra + `}}}]`
	}
	if code, out := postOps(t, env, "b1", voll(`,"dueDate":null`)); code != http.StatusOK {
		t.Fatalf("Vorbereitung (dueDate:null setzen): Status %d: %v", code, out)
	}
	if code, out := postOps(t, env, "b1", voll("")); code != http.StatusOK {
		t.Fatalf("Upsert ohne das null-Feld: Status %d, erwartet 200: %v", code, out)
	}
	// Gegenrichtung: Ein Feld MIT Wert wegzulassen bleibt eine Entkernung.
	if code, out := postOps(t, env, "b1", voll(`,"dueDate":"2026-10-01"`)); code != http.StatusOK {
		t.Fatalf("Vorbereitung (dueDate setzen): Status %d: %v", code, out)
	}
	code, out := postOps(t, env, "b1", voll(""))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("Upsert ohne Feld mit Wert: Status %d, erwartet 422: %v", code, out)
	}
	protokollArt("entkernung")(t, out)
}

// TestOps_KommentarBearbeitenNebenFremdemKommentar (ADR-0130 §4, Karte
// #601): Der Browser bearbeitet k1-c1 per comment.edit, während ein anderer
// Schreiber c2 an dieselbe Karte hängt. Beide überleben — bis Sprint 76 ging
// die Bearbeitung als Voll-card.upsert hinaus und nahm c2 wieder mit.
// Bearbeitet jemand einen inzwischen gelöschten Kommentar, antwortet der
// Server 422 ziel-fehlt mit commentId, statt still nichts zu tun.
func TestOps_KommentarBearbeitenNebenFremdemKommentar(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	k := func(id, text string) string {
		return `{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"` + id + `","author":"a","text":"` + text + `","created":"2026-09-30"}}}`
	}
	if s, out := postOps(t, env, "b1", `[`+k("c1", "alt")+`]`); s != http.StatusOK {
		t.Fatalf("Vorbereitung: %d %v", s, out)
	}
	if s, out := postOps(t, env, "b1", `[`+k("c2", "fremd")+`]`); s != http.StatusOK {
		t.Fatalf("fremder Kommentar: %d %v", s, out)
	}
	if s, out := postOps(t, env, "b1", `[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"neu"}}]`); s != http.StatusOK {
		t.Fatalf("comment.edit: %d %v", s, out)
	}
	st := leseOpsBoard(t, env)
	sp, i := st.karte("k1")
	var karte []map[string]any
	for _, s := range st.Board.Sprints {
		if s.ID == sp {
			karte = s.Cards[i].Comments
		}
	}
	if len(karte) != 2 || karte[0]["text"] != "neu" || karte[1]["id"] != "c2" {
		t.Fatalf("Kommentare nach comment.edit: %v", karte)
	}

	if s, out := postOps(t, env, "b1", `[{"type":"comment.delete","payload":{"cardId":"k1","commentId":"c2"}}]`); s != http.StatusOK {
		t.Fatalf("comment.delete: %d %v", s, out)
	}
	s, out := postOps(t, env, "b1", `[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c2","text":"zu spät"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("comment.edit auf gelöschten Kommentar: %d, erwartet 422", s)
	}
	protokollZiel("commentId", "c2")(t, out)
}

// TestOps_KommentarBearbeitenStempeltEdited (Karte #639, F-1264): Der Server
// setzt `edited` bei jedem comment.edit auf seine Eingangszeit — fehlt es im
// Payload ebenso wie bei einem mitgeschickten Fremdwert (auch andere
// Schreibweise). Ein unbearbeiteter Kommentar trägt keins.
func TestOps_KommentarBearbeitenStempeltEdited(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	feste := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	defer api.SetzeOpsNowFuerTest(func() time.Time { return feste })()
	add := `{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","author":"a","text":"alt","created":"2026-09-30"}}}`
	if s, out := postOps(t, env, "b1", `[`+add+`]`); s != http.StatusOK {
		t.Fatalf("Vorbereitung: %d %v", s, out)
	}
	holen := func() map[string]any {
		st := leseOpsBoard(t, env)
		sp, i := st.karte("k1")
		for _, s := range st.Board.Sprints {
			if s.ID == sp {
				return s.Cards[i].Comments[0]
			}
		}
		t.Fatal("Karte fehlt")
		return nil
	}
	if _, da := holen()["edited"]; da {
		t.Fatalf("unbearbeiteter Kommentar trägt edited: %v", holen())
	}
	erwartet := "2026-10-03T12:00:00.000Z"
	for _, op := range []string{
		`{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"neu1"}}`,
		`{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"neu2","edited":"1999-01-01"}}`,
		`{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"neu3","EDITED":"1999-01-01"}}`,
	} {
		if s, out := postOps(t, env, "b1", `[`+op+`]`); s != http.StatusOK {
			t.Fatalf("comment.edit: %d %v", s, out)
		}
		if k := holen(); k["edited"] != erwartet {
			t.Fatalf("edited = %v, erwartet Server-Stempel %s (Op %s)", k["edited"], erwartet, op)
		}
	}
}

// TestOps_FeldMitErwartetemVorwert (ADR-0130 §2, Karte #604): card.fieldIf
// wirkt nur, wenn der Stand am Server dem erwarteten Vorwert gleicht. Sonst
// 422 feld-geaendert mit dem aktuellen Wert und dem Index — nichts
// geschrieben. Ohne expect ist es ein Formfehler (400).
func TestOps_FeldMitErwartetemVorwert(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	fieldIf := func(wert, erwartet string) string {
		return `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"title","value":"` + wert + `","expect":"` + erwartet + `"}}]`
	}
	if s, out := postOps(t, env, "b1", fieldIf("A", "Karte k1")); s != http.StatusOK {
		t.Fatalf("passender Vorwert: %d %v", s, out)
	}
	s, out := postOps(t, env, "b1", fieldIf("B", "Karte k1"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("veralteter Vorwert: %d, erwartet 422", s)
	}
	protokollIndex(protokollArt("feld-geaendert"), 0)(t, out)
	if p, _ := out["protokoll"].([]any); len(p) == 1 {
		e := p[0].(map[string]any)
		if e["aktuell"] != "A" || e["feld"] != "title" {
			t.Errorf("Protokoll %v, erwartet feld=title aktuell=A", e)
		}
	}
	st := leseOpsBoard(t, env)
	sp, i := st.karte("k1")
	for _, x := range st.Board.Sprints {
		if x.ID == sp && x.Cards[i].Title != "A" {
			t.Fatalf("Titel nach Ablehnung %q, erwartet A", x.Cards[i].Title)
		}
	}
	s, out = postOps(t, env, "b1", `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"title","value":"C"}}]`)
	if s != http.StatusBadRequest {
		t.Fatalf("ohne expect: %d, erwartet 400", s)
	}
	grund("expect fehlt")(t, out)
	if s, out := postOps(t, env, "b1", `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"owner","value":"x","expect":null}}]`); s != http.StatusUnprocessableEntity {
		t.Fatalf("expect null gegen \"\": %d %v, erwartet 422", s, out)
	}
}

// TestOps_MengenOps (ADR-0130 §3, Karte #605): zwei Schreiber tragen je einen
// Beteiligten ein — beide bleiben. Eine Mengen-Op auf ein Feld, das keine
// Mengen-Liste ist, wirkt nicht und ergibt 422.
func TestOps_MengenOps(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	add := func(wer string) string {
		return `[{"type":"card.listAdd","payload":{"cardId":"k1","field":"assignees","item":"` + wer + `"}}]`
	}
	for _, wer := range []string{"andi", "berta", "andi"} {
		if s, out := postOps(t, env, "b1", add(wer)); s != http.StatusOK {
			t.Fatalf("listAdd %s: %d %v", wer, s, out)
		}
	}
	var st struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID        string   `json:"id"`
					Assignees []string `json:"assignees"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &st)
	resp.Body.Close()
	var got []string
	for _, sp := range st.Board.Sprints {
		for _, c := range sp.Cards {
			if c.ID == "k1" {
				got = c.Assignees
			}
		}
	}
	if len(got) != 2 || got[0] != "andi" || got[1] != "berta" {
		t.Fatalf("assignees %v, erwartet [andi berta]", got)
	}
	// Sitzungs-QA K-4: Mengen-Op auf eine fehlende Karte → 422 ziel-fehlt.
	for _, typ := range []string{"card.listAdd", "card.listRemove"} {
		s, out := postOps(t, env, "b1", `[{"type":"`+typ+`","payload":{"cardId":"gibtsnicht","field":"labels","item":"x"}}]`)
		if s != http.StatusUnprocessableEntity {
			t.Fatalf("%s auf fehlende Karte: %d, erwartet 422", typ, s)
		}
		protokollZiel("cardId", "gibtsnicht")(t, out)
	}
	s, out := postOps(t, env, "b1", `[{"type":"card.listAdd","payload":{"cardId":"k1","field":"comments","item":{"id":"x"}}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("listAdd auf comments: %d, erwartet 422", s)
	}
	protokollArt("wirkungslos")(t, out)
}

// TestOps_AwaitingJePerson (ADR-0130 §7, Karte #609): awaitingUser ist eine
// Abbildung je Person, geschrieben nur über board.awaiting. Zwei Personen, die
// nacheinander (oder gleichzeitig) fragen, behalten beide ihren Eintrag; ein
// Löschen trifft nur den eigenen Schlüssel; board.field auf awaitingUser
// ersetzte die ganze Abbildung und wird deshalb abgelehnt.
func TestOps_AwaitingJePerson(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	frage := func(person, text string) string {
		return `[{"type":"board.awaiting","payload":{"person":"` + person + `","value":{"question":"` + text +
			`","since":"2026-09-30T10:00:00.000Z","askedBy":"Claude Opus 5"}}}]`
	}
	lies := func() map[string]map[string]any {
		var st struct {
			Board struct {
				AwaitingUser map[string]map[string]any `json:"awaitingUser"`
			} `json:"board"`
		}
		resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
		_ = json.Unmarshal(mustReadAll(t, resp.Body), &st)
		resp.Body.Close()
		return st.Board.AwaitingUser
	}

	for person, text := range map[string]string{"andi@example.org": "Frage an Andi?", "berta@example.org": "Frage an Berta?"} {
		if s, out := postOps(t, env, "b1", frage(person, text)); s != http.StatusOK {
			t.Fatalf("board.awaiting %s: %d %v", person, s, out)
		}
	}
	au := lies()
	if len(au) != 2 || au["andi@example.org"]["question"] != "Frage an Andi?" || au["berta@example.org"]["question"] != "Frage an Berta?" {
		t.Fatalf("beide Einträge müssen bleiben, gefunden: %v", au)
	}

	// Löschen (value: null) trifft nur den eigenen Schlüssel.
	if s, out := postOps(t, env, "b1", `[{"type":"board.awaiting","payload":{"person":"andi@example.org","value":null}}]`); s != http.StatusOK {
		t.Fatalf("Löschen: %d %v", s, out)
	}
	au = lies()
	if len(au) != 1 || au["berta@example.org"] == nil {
		t.Fatalf("nur Bertas Eintrag darf bleiben, gefunden: %v", au)
	}

	// Der alte Weg: board.field auf awaitingUser wirkt nicht (422) und lässt die
	// Abbildung unberührt.
	s, out := postOps(t, env, "b1", `[{"type":"board.field","payload":{"field":"awaitingUser","value":{"question":"alt?","since":"s","askedBy":"x"}}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("board.field auf awaitingUser: %d, erwartet 422", s)
	}
	protokollArt("wirkungslos")(t, out)
	if au = lies(); len(au) != 1 || au["berta@example.org"] == nil {
		t.Fatalf("board.field darf die Abbildung nicht ersetzen, gefunden: %v", au)
	}

	// Formfehler des Schreibers: 400, nichts geschrieben.
	for name, ops := range map[string]string{
		"person keine Zeichenkette": `[{"type":"board.awaiting","payload":{"person":5,"value":null}}]`,
		"value eine Zeichenkette":   `[{"type":"board.awaiting","payload":{"person":"x","value":"text"}}]`,
		"leere Frage":               `[{"type":"board.awaiting","payload":{"person":"x","value":{"question":"","since":"s","askedBy":"a"}}}]`,
		"askedBy fehlt":             `[{"type":"board.awaiting","payload":{"person":"x","value":{"question":"q","since":"s"}}}]`,
	} {
		if s, _ := postOps(t, env, "b1", ops); s != http.StatusBadRequest {
			t.Fatalf("%s: %d, erwartet 400", name, s)
		}
	}

	// Prototyp-Schlüssel als Person: wirkungslos (422), die Abbildung bleibt.
	if s, _ := postOps(t, env, "b1", frage("__proto__", "x?")); s != http.StatusUnprocessableEntity {
		t.Fatalf("__proto__ als Person: %d, erwartet 422", s)
	}
	if au = lies(); len(au) != 1 {
		t.Fatalf("Abbildung nach Prototyp-Versuch: %v", au)
	}
}

// TestOps_WiederholungMitClientID (ADR-0130 §5, Karte #607): Kommt derselbe
// Batch (gleiche Client-ids) nach einem Verbindungsabbruch noch einmal, faltet
// der Server ihn nicht erneut — auch dann nicht, wenn zwischen beiden
// Versuchen jemand anders dasselbe Feld geändert hat. Bis Sprint 76 gewann die
// Wiederholung mit neuer Eingangszeit (ADR-0129 §4 Punkt 4, „bekannter Rest“).
func TestOps_WiederholungMitClientID(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	batchA := `[{"id":"client-a-1","type":"card.field","payload":{"cardId":"k1","field":"title","value":"von A"}}]`
	if s, out := postOps(t, env, "b1", batchA); s != http.StatusOK {
		t.Fatalf("erster Versuch: %d %v", s, out)
	}
	if s, out := postOps(t, env, "b1", `[{"id":"client-b-1","type":"card.field","payload":{"cardId":"k1","field":"title","value":"von B"}}]`); s != http.StatusOK {
		t.Fatalf("B: %d %v", s, out)
	}
	s, out := postOps(t, env, "b1", batchA)
	if s != http.StatusOK || out["wiederholt"] != true {
		t.Fatalf("Wiederholung: %d %v, erwartet 200 mit wiederholt=true", s, out)
	}
	titel := func() string {
		st := leseOpsBoard(t, env)
		sp, i := st.karte("k1")
		for _, x := range st.Board.Sprints {
			if x.ID == sp {
				return x.Cards[i].Title
			}
		}
		return ""
	}
	if got := titel(); got != "von B" {
		t.Fatalf("Titel nach Wiederholung %q, erwartet „von B“ — die Wiederholung hat überschrieben", got)
	}
	// Teilweise bekannt: die bekannte Op wird übersprungen, die neue wirkt.
	s, out = postOps(t, env, "b1", `[{"id":"client-a-1","type":"card.field","payload":{"cardId":"k1","field":"title","value":"von A"}},`+
		`{"id":"client-a-2","type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`)
	if s != http.StatusOK {
		t.Fatalf("teilweise bekannt: %d %v", s, out)
	}
	q, _ := out["ops"].([]any)
	if len(q) != 2 || q[0].(map[string]any)["wiederholt"] != true || q[1].(map[string]any)["wiederholt"] != nil {
		t.Fatalf("Quittung %v, erwartet [wiederholt, neu]", out["ops"])
	}
	if got := titel(); got != "von B" {
		t.Fatalf("Titel %q, erwartet „von B“", got)
	}
	// Sitzungs-QA K-3: dieselbe id mit anderem Inhalt → 400, nichts geschrieben.
	if s, out := postOps(t, env, "b1", `[{"id":"client-a-1","type":"card.field","payload":{"cardId":"k1","field":"title","value":"ganz anders"}}]`); s != http.StatusBadRequest {
		t.Fatalf("wiederverwendete id: %d %v, erwartet 400", s, out)
	}
	if got := titel(); got != "von B" {
		t.Fatalf("Titel %q nach wiederverwendeter id, erwartet „von B“", got)
	}
	// Verify V-2: dieselbe id zweimal im selben Batch mit anderem Inhalt → 400.
	if s, out := postOps(t, env, "b1", `[{"id":"doppelt","type":"card.field","payload":{"cardId":"k1","field":"title","value":"eins"}},`+
		`{"id":"doppelt","type":"card.field","payload":{"cardId":"k1","field":"title","value":"zwei"}}]`); s != http.StatusBadRequest {
		t.Fatalf("doppelte id im Batch: %d %v, erwartet 400", s, out)
	}
	// Gleiche id, gleicher Inhalt im selben Batch: einmal angewandt, kein Fehler.
	if s, out := postOps(t, env, "b1", `[{"id":"gleich","type":"card.field","payload":{"cardId":"k1","field":"qa","value":false}},`+
		`{"id":"gleich","type":"card.field","payload":{"cardId":"k1","field":"qa","value":false}}]`); s != http.StatusOK {
		t.Fatalf("gleiche id und Inhalt im Batch: %d %v, erwartet 200", s, out)
	}
	// Sprint-77-Kritiker: untaugliche id (zu lang, keine Zeichenkette) → 400
	// statt still ohne Schlüssel angewandt; id: null gilt wie ohne id.
	lang := strings.Repeat("x", storage.MaxClientIDLaenge+1)
	for _, id := range []string{`"` + lang + `"`, `42`, `{"a":1}`, `""`, `"  "`} {
		if s, out := postOps(t, env, "b1", `[{"id":`+id+`,"type":"card.field","payload":{"cardId":"k1","field":"title","value":"untauglich"}}]`); s != http.StatusBadRequest {
			t.Fatalf("id %.20s…: %d %v, erwartet 400", id, s, out)
		}
	}
	if got := titel(); got != "von B" {
		t.Fatalf("Titel %q nach untauglicher id, erwartet „von B“", got)
	}
	if s, out := postOps(t, env, "b1", `[{"id":null,"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`); s != http.StatusOK {
		t.Fatalf("id null: %d %v, erwartet 200", s, out)
	}
	// Ohne id: wie bisher, jede Sendung wirkt.
	for i := 0; i < 2; i++ {
		if s, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"ohne id"}}]`); s != http.StatusOK || out["wiederholt"] != nil {
			t.Fatalf("ohne id (%d): %d %v", i, s, out)
		}
	}
}

func TestOps_Grenzen(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	op := `{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}`
	zuViele := "[" + strings.TrimSuffix(strings.Repeat(op+",", api.MaxOpsJeBatch+1), ",") + "]"
	if code, _ := postOps(t, env, "b1", zuViele); code != http.StatusRequestEntityTooLarge {
		t.Errorf("201 Ops: Status %d, erwartet 413", code)
	}
	gross := `[{"type":"card.field","payload":{"cardId":"k1","field":"description","value":"` + strings.Repeat("x", api.MaxOpsBodyBytes) + `"}}]`
	if code, _ := postOps(t, env, "b1", gross); code != http.StatusRequestEntityTooLarge {
		t.Errorf("Body > 1 MiB: Status %d, erwartet 413", code)
	}
	if code, _ := postOps(t, env, "gibtsnicht", "["+op+"]"); code != http.StatusNotFound {
		t.Errorf("fehlendes Board: Status %d, erwartet 404", code)
	}
	resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards/b1/ops", []byte(`[kein json`)))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("kaputtes JSON: Status %d", resp.StatusCode)
	}
}

// TestOps_RateLimit: Der Eimer zählt jeden Aufruf, auch abgelehnte — sonst
// könnte eine Schleife mit fehlerhaften Batches die Vorprüfung beliebig oft
// auslösen.
// TestOps_RateLimit: Für Menschen gilt 600 pro Minute, nicht die 60 der
// Agenten — der Browser speichert ab Sprint 72 über diesen Weg (Kritiker
// Sprint 71). Die Agenten-Grenze prüft TestAgentKette_RateLimit.
func TestOps_RateLimit(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	for i := 0; i < 600; i++ {
		if code, _ := postOps(t, env, "b1", `[]`); code != http.StatusBadRequest {
			t.Fatalf("Aufruf %d: Status %d", i, code)
		}
	}
	if code, _ := postOps(t, env, "b1", `[]`); code != http.StatusTooManyRequests {
		t.Fatalf("601. Aufruf: Status %d, erwartet 429", code)
	}
}

// TestOps_NebenlaeufigEindeutigeDisplayIDs: 8 Schreiber legen gleichzeitig je
// 5 Karten an. Alle 40 kommen an, displayIds sind eindeutig (K-5).
func TestOps_NebenlaeufigEindeutigeDisplayIDs(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	const schreiber, runden = 8, 5
	var wg sync.WaitGroup
	fehler := make(chan string, schreiber*runden)
	for g := 0; g < schreiber; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < runden; k++ {
				op := fmt.Sprintf(`[{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"n-%d-%d","title":"n","description":"","owner":"",
				  "assignees":[],"labels":[],"comments":[],"activity":[],"links":[],"qa":false,"critical":false,"blocked":false,"archived":false}}}]`, g, k)
				resp := mustDo(t, opsReq(t, http.MethodPost, env.srv.URL+"/boards/b1/ops", []byte(`{"ops":`+op+`}`)))
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					fehler <- fmt.Sprintf("g%d k%d: %d", g, k, resp.StatusCode)
				}
			}
		}(g)
	}
	wg.Wait()
	close(fehler)
	for f := range fehler {
		t.Error(f)
	}
	s := leseOpsBoard(t, env)
	gesehen := map[int]string{}
	n := 0
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			n++
			if alt, ok := gesehen[c.DisplayID]; ok {
				t.Errorf("displayId %d doppelt: %s, %s", c.DisplayID, alt, c.ID)
			}
			gesehen[c.DisplayID] = c.ID
		}
	}
	if n != 2+schreiber*runden {
		t.Fatalf("%d Karten, erwartet %d", n, 2+schreiber*runden)
	}
}

// TestOps_FehlendesBoardNimmtKeineSperre (F-1186, Session-Kritiker nach
// Sprint 73): Ein Batch auf ein Board, das es nicht gibt, darf die
// datenbankweite Schreibsperre nicht nehmen — sonst hielte er sie, ohne dass
// das Sperrzeit-Budget (F-1149) etwas bucht (falte läuft nie).
//
// Konstruiertes Fenster (§13 im Nebenläufigkeits-Topic): Eine fremde
// Verbindung hält BEGIN IMMEDIATE. GEGENPROBE: Ohne die Vorab-Prüfung wartet
// ApplyOps auf die Sperre (busy_timeout 5 s) und antwortet 503 statt 404.
func TestOps_FehlendesBoardNimmtKeineSperre(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	ctx := context.Background()
	fremd, err := env.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fremd.Close()
	if _, err := fremd.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fremd.ExecContext(context.Background(), "ROLLBACK") }()

	start := time.Now()
	code, out := postOps(t, env, "gibtsnicht", `[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]`)
	dauer := time.Since(start)
	if code != http.StatusNotFound {
		t.Fatalf("Status %d nach %v, erwartet 404 ohne Warten auf die Sperre: %v", code, dauer, out)
	}
	if dauer > 2*time.Second {
		t.Fatalf("404 erst nach %v — der Batch hat auf die Schreibsperre gewartet", dauer)
	}
}
