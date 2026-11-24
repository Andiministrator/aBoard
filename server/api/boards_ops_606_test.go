package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// Karte #606 / ADR-0130 §1 — card.claim/card.release am Ops-Endpunkt.

type claimStand struct {
	Board struct {
		Sprints []struct {
			Cards []struct {
				ID       string         `json:"id"`
				ColumnID string         `json:"columnId"`
				Claim    map[string]any `json:"claim"`
			} `json:"cards"`
		} `json:"sprints"`
	} `json:"board"`
}

func leseClaim(t *testing.T, env *boardsTestEnv, kartenID string) (claim map[string]any, spalte string) {
	t.Helper()
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
	defer resp.Body.Close()
	var s claimStand
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &s); err != nil {
		t.Fatal(err)
	}
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			if c.ID == kartenID {
				return c.Claim, c.ColumnID
			}
		}
	}
	t.Fatalf("Karte %s fehlt", kartenID)
	return nil, ""
}

// stelleUhr setzt die Eingangszeit des Endpunkts fest; die Rückgabe stellt sie weiter.
func stelleUhr(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	var mu sync.Mutex
	jetzt := start
	t.Cleanup(api.SetzeOpsNowFuerTest(func() time.Time { mu.Lock(); defer mu.Unlock(); return jetzt }))
	return func(d time.Duration) { mu.Lock(); jetzt = jetzt.Add(d); mu.Unlock() }
}

func claimBatch(sitzung string) string {
	return `[{"type":"card.claim","payload":{"cardId":"k1","session":"` + sitzung + `"}},` +
		`{"type":"card.move","payload":{"cardId":"k1","toSprintId":"s1","toColId":"col-backlog"}}]`
}

// TestOps_ClaimNebenlaeufigGenauEinerGewinnt ist der DoD-Fall der Karte: Zwei
// Sitzungen schicken gleichzeitig [claim, move]. Genau eine bekommt 200, die
// andere 422 „beansprucht“ — und von ihrem Batch wirkt nichts, auch nicht das
// move (Batch-Atomarität, ADR-0129 §4 Punkt 4). Mit -race laufen lassen.
// Das Testboard hat keine Spalte „In Arbeit“, col-backlog vertritt sie. NICHT
// col-erledigt: dort gilt eine Übernahme als erloschen, der zweite Batch dürfte
// sie dann zu Recht übernehmen.
func TestOps_ClaimNebenlaeufigGenauEinerGewinnt(t *testing.T) {
	for runde := 0; runde < 10; runde++ {
		env := newOpsTestEnv(t, true)
		legeOpsBoardAn(t, env)
		sitzungen := []string{"sitzung-a", "sitzung-b"}
		status := make([]int, 2)
		antwort := make([]map[string]any, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range sitzungen {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				status[i], antwort[i] = postOps(t, env, "b1", claimBatch(sitzungen[i]))
			}(i)
		}
		close(start)
		wg.Wait()
		gewinner := -1
		for i, s := range status {
			switch s {
			case http.StatusOK:
				if gewinner != -1 {
					t.Fatalf("Runde %d: beide 200", runde)
				}
				gewinner = i
			case http.StatusUnprocessableEntity:
				protokollArt("beansprucht")(t, antwort[i])
			default:
				t.Fatalf("Runde %d: Status %d (%v)", runde, s, antwort[i])
			}
		}
		if gewinner == -1 {
			t.Fatalf("Runde %d: niemand hat gewonnen (%v)", runde, status)
		}
		claim, spalte := leseClaim(t, env, "k1")
		if claim["session"] != sitzungen[gewinner] {
			t.Fatalf("Runde %d: Halter %v, Gewinner %s", runde, claim["session"], sitzungen[gewinner])
		}
		if spalte != "col-backlog" {
			t.Fatalf("Runde %d: Spalte %s", runde, spalte)
		}
	}
}

// Verlierer-Batch schreibt nichts: nacheinander, damit die Reihenfolge feststeht.
func TestOps_ClaimVerliererSchreibtNichts(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	stelleUhr(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	if s, out := postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"k1","session":"a"}}]`); s != http.StatusOK {
		t.Fatalf("claim a: %d %v", s, out)
	}
	s, out := postOps(t, env, "b1", `[{"type":"card.move","payload":{"cardId":"k2","toSprintId":"s1","toColId":"col-erledigt"}},`+
		`{"type":"card.claim","payload":{"cardId":"k1","session":"b"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("claim b: %d %v", s, out)
	}
	protokollIndex(protokollArt("beansprucht"), 1)(t, out)
	if _, spalte := leseClaim(t, env, "k2"); spalte != "col-ready" {
		t.Fatalf("move aus dem abgelehnten Batch hat gewirkt: %s", spalte)
	}
}

// Ablauf, Erneuerung und Uhr-Gate mit gestellter Uhr (knowledge §38).
func TestOps_ClaimAblaufErneuerungUndUhr(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	vor := stelleUhr(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	claimVon := func(sitzung string) (int, map[string]any) {
		return postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"k1","session":"`+sitzung+`","leaseSeconds":600}}]`)
	}
	if s, out := claimVon("a"); s != http.StatusOK {
		t.Fatalf("claim a: %d %v", s, out)
	}
	vor(9 * time.Minute)
	if s, _ := claimVon("b"); s != http.StatusUnprocessableEntity {
		t.Fatalf("b vor Ablauf: %d", s)
	}
	if s, out := claimVon("a"); s != http.StatusOK { // Erneuerung: bis 10:19
		t.Fatalf("Erneuerung a: %d %v", s, out)
	}
	vor(5 * time.Minute) // 10:14 — ohne Erneuerung wäre die Lease seit 10:10 abgelaufen
	if s, _ := claimVon("b"); s != http.StatusUnprocessableEntity {
		t.Fatalf("b nach Erneuerung: %d", s)
	}
	vor(5 * time.Minute) // 10:19 — abgelaufen

	// Uhr nicht gestellt → uhr-unsicher, der Halter bleibt.
	alt := uhr.Gestellt
	uhr.Gestellt = func() (bool, error) { return false, errors.New("nicht synchron") }
	s, out := claimVon("b")
	uhr.Gestellt = alt
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("b bei ungestellter Uhr: %d %v", s, out)
	}
	protokollArt("uhr-unsicher")(t, out)
	if claim, _ := leseClaim(t, env, "k1"); claim["session"] != "a" {
		t.Fatalf("Halter nach uhr-unsicher: %v", claim)
	}

	// Uhr gestellt → Übernahme, vorheriger Halter in vorher.
	if s, out := claimVon("b"); s != http.StatusOK {
		t.Fatalf("b nach Ablauf: %d %v", s, out)
	}
	claim, _ := leseClaim(t, env, "k1")
	vorher, _ := claim["vorher"].(map[string]any)
	if claim["session"] != "b" || vorher["session"] != "a" {
		t.Fatalf("Übernahme: %v", claim)
	}
}

func TestOps_ClaimFreigabe(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	if s, _ := postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"k1","session":"a"}}]`); s != http.StatusOK {
		t.Fatal("claim")
	}
	s, out := postOps(t, env, "b1", `[{"type":"card.release","payload":{"cardId":"k1","session":"b"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("fremde Freigabe: %d", s)
	}
	protokollArt("nicht-halter")(t, out)
	if s, out := postOps(t, env, "b1", `[{"type":"card.release","payload":{"cardId":"k1","session":"b","erzwingen":true}}]`); s != http.StatusOK {
		t.Fatalf("erzwungen (Mensch): %d %v", s, out)
	}
	if claim, _ := leseClaim(t, env, "k1"); claim != nil {
		t.Fatalf("claim nach Freigabe: %v", claim)
	}
	// Wiederholung ohne Übernahme: 200 (idempotent).
	if s, out := postOps(t, env, "b1", `[{"type":"card.release","payload":{"cardId":"k1","session":"a"}}]`); s != http.StatusOK {
		t.Fatalf("Freigabe ohne Übernahme: %d %v", s, out)
	}
	// Formfehler → 400, fehlende Karte → 422 ziel-fehlt, claim per card.field → 422.
	for _, f := range []string{
		`[{"type":"card.claim","payload":{"cardId":"k1","leaseSeconds":"lang"}}]`,
		`[{"type":"card.claim","payload":{"cardId":"k1","session":7}}]`,
		`[{"type":"card.release","payload":{"cardId":"k1","erzwingen":"ja"}}]`,
	} {
		if s, _ := postOps(t, env, "b1", f); s != http.StatusBadRequest {
			t.Errorf("%s: %d, erwartet 400", f, s)
		}
	}
	s, out = postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"fehlt"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("fehlende Karte: %d", s)
	}
	protokollArt("ziel-fehlt")(t, out)
	s, out = postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"claim","value":{"by":"x"}}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("claim per card.field: %d", s)
	}
	protokollArt("wirkungslos")(t, out)
	if e := out["protokoll"].([]any)[0].(map[string]any); e["grund"] != "card-feld-gesperrt" {
		t.Fatalf("Grund %v", e["grund"])
	}
}

// Sprint-77-Kritiker: Bei ungestellter Uhr wäre JEDES neue Lease-Ende falsch —
// auch die Übernahme einer freien Karte und die Erneuerung der eigenen melden
// deshalb uhr-unsicher, und der Stand bleibt.
func TestOps_ClaimUhrUngestelltFreiUndEigen(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	stelleUhr(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	claimVon := func(sitzung string) (int, map[string]any) {
		return postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"k1","session":"`+sitzung+`","leaseSeconds":600}}]`)
	}
	alt := uhr.Gestellt
	uhr.Gestellt = func() (bool, error) { return false, errors.New("nicht synchron") }
	s, out := claimVon("a")
	uhr.Gestellt = alt
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("freie Karte bei ungestellter Uhr: %d %v", s, out)
	}
	protokollArt("uhr-unsicher")(t, out)
	if claim, _ := leseClaim(t, env, "k1"); claim != nil {
		t.Fatalf("freie Karte trotz ungestellter Uhr übernommen: %v", claim)
	}

	if s, out := claimVon("a"); s != http.StatusOK {
		t.Fatalf("claim a mit gestellter Uhr: %d %v", s, out)
	}
	vorher, _ := leseClaim(t, env, "k1")
	uhr.Gestellt = func() (bool, error) { return false, errors.New("nicht synchron") }
	s, out = claimVon("a")
	uhr.Gestellt = alt
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Erneuerung bei ungestellter Uhr: %d %v", s, out)
	}
	protokollArt("uhr-unsicher")(t, out)
	if nachher, _ := leseClaim(t, env, "k1"); nachher["until"] != vorher["until"] {
		t.Fatalf("Lease trotz ungestellter Uhr verlängert: %v → %v", vorher, nachher)
	}
}
