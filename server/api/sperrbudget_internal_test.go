package api

// sperrbudget_internal_test.go — F-1149: Das Rate-Limit des Ops-Endpunkts
// zählte nur Aufrufe (60/min je Agent). Jeder Batch darf die datenbankweite
// Schreibsperre bis zur Fold-Zeitgrenze halten; im schlechtesten Fall waren
// das 90–120 s Sperre je Minute für einen einzigen Agenten.
//
// Die Uhr ist vorgespielt (opsSperrUhr): jeder Batch hält die Sperre 1,5 s,
// ohne dass der Test wartet.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

// GEGENPROBE: Ohne die Prüfung sperre.frei in postOps nimmt der Server alle
// 60 Batches an (nur das Aufruf-Limit greift) — der Test wird rot.
func TestOps_SperrBudgetBegrenztSperrzeitJeAgent(t *testing.T) {
	var mu sync.Mutex
	basis := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	schritt := 0
	alt := opsSperrUhr
	opsSperrUhr = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		schritt++
		return basis.Add(time.Duration(schritt) * 1500 * time.Millisecond)
	}
	t.Cleanup(func() { opsSperrUhr = alt })

	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewBoardRepo(db)
	body := `{"version":"2","board":{"id":"b1","title":"Budget","nextDisplayId":2,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]},
	    {"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","description":"",
	      "columnId":"col-ready","owner":"","assignees":[],"labels":[],"comments":[],"activity":[],"links":[],
	      "qa":false,"critical":false,"blocked":false,"archived":false}]}]}}`
	if _, err := repo.Insert(context.Background(), "b1", "Budget", "2", []byte(body)); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewBoardsAPI(repo).WithFold(motor).Register(mux)
	agent := &AgentPrincipal{TokenID: "t1", Name: "bot", BoardID: "b1", Role: "editor"}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKeyAgent, agent)))
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	angenommen, abgewiesen := 0, false
	for i := 0; i < opsRateMax && !abgewiesen; i++ {
		op := fmt.Sprintf(`{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":%v}}]}`, i%2 == 0)
		resp, err := http.Post(srv.URL+"/boards/b1/ops", "application/json", strings.NewReader(op))
		if err != nil {
			t.Fatal(err)
		}
		antwort, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			angenommen++
		case http.StatusTooManyRequests:
			if !strings.Contains(string(antwort), "lock time budget") {
				t.Fatalf("Batch %d: 429 aus dem falschen Grund: %s", i, antwort)
			}
			abgewiesen = true
		default:
			t.Fatalf("Batch %d: Status %d: %s", i, resp.StatusCode, antwort)
		}
	}
	// 10 s Budget bei 1,5 s je Batch: Batch 1–7 beginnen mit < 10 s
	// Verbrauch (0 … 9 s), der achte wird abgewiesen.
	if angenommen != 7 || !abgewiesen {
		t.Fatalf("%d Batches angenommen (abgewiesen=%v), erwartet 7 und danach 429 — das Budget begrenzt die Sperrzeit nicht", angenommen, abgewiesen)
	}
}

// TestSperrBudget_FensterBeginntNeu: Nach Ablauf des Fensters ist das Budget
// wieder voll (Rückweg des Zustands), und ein Prinzipal sperrt nicht den
// anderen.
func TestSperrBudget_FensterBeginntNeu(t *testing.T) {
	jetzt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s := newSperrBudget(10*time.Second, time.Minute)
	s.now = func() time.Time { return jetzt }
	s.buche("a", 11*time.Second)
	if s.frei("a") {
		t.Fatal("überzogenes Budget meldet frei")
	}
	if !s.frei("b") {
		t.Fatal("anderer Prinzipal ist mitgesperrt")
	}
	jetzt = jetzt.Add(time.Minute)
	if !s.frei("a") {
		t.Fatal("nach Ablauf des Fensters weiter gesperrt — kein Rückweg")
	}
}

// TestOps_SperrzeitWirdDirektNachApplyOpsGebucht (F-1198, verhaltensbasiert
// seit F-1209): Gebucht wird direkt nach ApplyOps, nicht erst am Ende des
// Handlers — sonst zählten Trim, Event und das Schreiben der Antwort an
// einen langsamen Client mit. Die Test-Naht BoardsAPI.nachApplyOps (F-1209)
// läuft im Handler genau an der Stelle, an der bisher nur die Quelltext-Probe
// nachsah: nach der Sperrzeit-Buchung, vor dem Frist-Trim. Der Test lässt sie
// real `schlaf` lang schlafen — steht die Buchung wirklich davor, bleibt die
// gebuchte Sperrzeit darunter. Die Schwelle IST die Schlafzeit, nicht ein
// Bruchteil davon: Die Naht allein bucht mindestens `schlaf`, die Faltung
// allein (erster Gebrauch kompiliert das Bündel, CI unter Last) darf dagegen
// beliebig nahe herankommen, ohne dass der Test fälschlich rot wird.
//
// GEGENPROBE: Verschiebt man den expliziten bucheSperre()-Aufruf in
// boards_ops.go hinter den nachApplyOps-Aufruf (sodass nur noch der defer am
// Funktionsende bucht), schliesst die gebuchte Sperrzeit den Schlaf mit
// ein — der Test wird rot (verbraucht ≥ schlaf).
func TestOps_SperrzeitWirdDirektNachApplyOpsGebucht(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewBoardRepo(db)
	body := `{"version":"2","board":{"id":"b1","title":"Sperrzeit","nextDisplayId":2,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]},
	    {"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","description":"",
	      "columnId":"col-ready","owner":"","assignees":[],"labels":[],"comments":[],"activity":[],"links":[],
	      "qa":false,"critical":false,"blocked":false,"archived":false}]}]}}`
	if _, err := repo.Insert(context.Background(), "b1", "Sperrzeit", "2", []byte(body)); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}

	api := NewBoardsAPI(repo).WithFold(motor)
	const schlaf = time.Second
	nachApplyOpsLief := false
	api.nachApplyOps = func() {
		nachApplyOpsLief = true
		time.Sleep(schlaf)
	}
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	op := `{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}}]}`
	resp, err := http.Post(srv.URL+"/boards/b1/ops", "application/json", strings.NewReader(op))
	if err != nil {
		t.Fatal(err)
	}
	antwort, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d: %s", resp.StatusCode, antwort)
	}
	if !nachApplyOpsLief {
		t.Fatal("nachApplyOps-Naht wurde nicht aufgerufen — Test prüft nichts")
	}

	api.opsSperre.mu.Lock()
	konto, ok := api.opsSperre.konten["anonym"]
	api.opsSperre.mu.Unlock()
	if !ok {
		t.Fatal("keine Sperrzeit für den Legacy-Bypass gebucht")
	}
	if konto.verbraucht <= 0 {
		t.Fatal("gebuchte Sperrzeit ist 0 — bucheSperre() hat gar nicht gebucht")
	}
	if konto.verbraucht >= schlaf {
		t.Fatalf("gebuchte Sperrzeit %v enthält die Schlafzeit %v der Naht — bucheSperre() bucht nicht mehr direkt nach ApplyOps", konto.verbraucht, schlaf)
	}
}
