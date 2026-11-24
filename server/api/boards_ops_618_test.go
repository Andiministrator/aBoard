package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Karte #618 / F-1257 — card.release mit erwartetem Halter am Ops-Endpunkt.
// Der Browser schickt „Übernahme aufheben“ als [card.release {erzwingen,
// expect}, comment.add]. Hält inzwischen ein anderer die Karte, lehnt der
// Server den ganzen Batch mit 422 halter-geaendert ab — die Übernahme bleibt,
// und der Grund-Kommentar mit dem alten Halter entsteht nicht.

func erwartetAus(t *testing.T, claim map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"by": claim["by"], "session": claim["session"], "since": claim["since"]})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func aufhebenBatch(expect string) string {
	return `[{"type":"card.release","payload":{"cardId":"k1","erzwingen":true,"expect":` + expect + `}},` +
		`{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"g1","author":"Andi","text":"Grund","created":"2026-10-01"}}}]`
}

func kommentarAnzahl(t *testing.T, env *boardsTestEnv) int {
	t.Helper()
	resp := mustDo(t, opsReq(t, http.MethodGet, env.srv.URL+"/boards/b1", nil))
	defer resp.Body.Close()
	var s struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID       string `json:"id"`
					Comments []any  `json:"comments"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &s); err != nil {
		t.Fatal(err)
	}
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			if c.ID == "k1" {
				return len(c.Comments)
			}
		}
	}
	t.Fatal("Karte k1 fehlt")
	return 0
}

func TestOps_ReleaseMitErwartetemHalter(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	vor := stelleUhr(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	claimVon := func(sitzung string) {
		t.Helper()
		if s, out := postOps(t, env, "b1", `[{"type":"card.claim","payload":{"cardId":"k1","session":"`+sitzung+`","leaseSeconds":600}}]`); s != http.StatusOK {
			t.Fatalf("claim %s: %d %v", sitzung, s, out)
		}
	}

	// Das Modal zeigt Sitzung a; deren Lease läuft ab, b übernimmt ordnungsgemäß.
	claimVon("a")
	claimA, _ := leseClaim(t, env, "k1")
	gesehen := erwartetAus(t, claimA)
	vor(11 * time.Minute)
	claimVon("b")

	s, out := postOps(t, env, "b1", aufhebenBatch(gesehen))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("veralteter Halter: %d %v", s, out)
	}
	protokollArt("halter-geaendert")(t, out)
	e := out["protokoll"].([]any)[0].(map[string]any)
	if e["halter"] == nil || e["session"] != "b" || e["index"] != float64(0) {
		t.Fatalf("Protokoll nennt nicht den aktuellen Halter: %v", e)
	}
	if claim, _ := leseClaim(t, env, "k1"); claim["session"] != "b" {
		t.Fatalf("Übernahme von b aufgehoben: %v", claim)
	}
	if n := kommentarAnzahl(t, env); n != 0 {
		t.Fatalf("Grund-Kommentar trotz Ablehnung geschrieben (%d)", n)
	}

	// Mit dem aktuellen Halter wirkt es — beide Ops.
	claimB, _ := leseClaim(t, env, "k1")
	if s, out := postOps(t, env, "b1", aufhebenBatch(erwartetAus(t, claimB))); s != http.StatusOK {
		t.Fatalf("passender Halter: %d %v", s, out)
	}
	if claim, _ := leseClaim(t, env, "k1"); claim != nil {
		t.Fatalf("claim nach Aufheben: %v", claim)
	}
	if n := kommentarAnzahl(t, env); n != 1 {
		t.Fatalf("Grund-Kommentar fehlt (%d)", n)
	}

	// Übernahme schon weg: 422 mit halter null, kein zweiter Kommentar.
	s, out = postOps(t, env, "b1", aufhebenBatch(erwartetAus(t, claimB)))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("ohne Übernahme: %d %v", s, out)
	}
	protokollArt("halter-geaendert")(t, out)
	if e := out["protokoll"].([]any)[0].(map[string]any); e["halter"] != nil {
		t.Fatalf("halter sollte null sein: %v", e)
	}
	if n := kommentarAnzahl(t, env); n != 1 {
		t.Fatalf("irreführender zweiter Kommentar (%d)", n)
	}

	// Formfehler → 400.
	for _, f := range []string{`"a"`, `["a"]`, `{"by":1}`, `{"since":{}}`} {
		if s, _ := postOps(t, env, "b1", `[{"type":"card.release","payload":{"cardId":"k1","expect":`+f+`}}]`); s != http.StatusBadRequest {
			t.Errorf("expect %s: %d, erwartet 400", f, s)
		}
	}
}
