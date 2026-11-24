package api_test

// Karte #778 (ADR-0140 §10, Stelle 3): Ein PUT (auch „Meine Version
// behalten“) schaltet „vertraulich“ nicht ab. Der Wert bleibt, der Header
// X-Aboard-Vertraulich-Bewahrt nennt die Karte, der Body bleibt das Board
// (Fälle f und g). Umgebung aus agent_608_test.go (newEnv608).

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func put778(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	get, _ := http.NewRequest("GET", url, nil)
	get.Header.Set("Remote-Email", "anna@firma.de")
	get.Header.Set("Remote-User", "anna@firma.de")
	resp, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	req, _ := http.NewRequest("PUT", url, strings.NewReader(body))
	req.Header.Set("Remote-Email", "anna@firma.de")
	req.Header.Set("Remote-User", "anna@firma.de")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func boardMit778(karte, termin string) string {
	return `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "termine":[{"id":"t1","titel":"Notar","datum":"2026-10-08"` + termin + `}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]` + karte + `}]}]}}`
}

func TestVertraulichPut778(t *testing.T) {
	e := newEnv608(t)
	url := e.mensch.URL + "/boards/b1"

	// Ausgangslage per PUT: Karte und Termin vertraulich.
	if resp, b := put778(t, url, boardMit778(`,"confidential":true`, `,"confidential":true`)); resp.StatusCode != 200 {
		t.Fatalf("PUT 1: %d %s", resp.StatusCode, b)
	} else if resp.Header.Get("X-Aboard-Vertraulich-Bewahrt") != "" {
		t.Fatalf("Header ohne Anlass: %q", resp.Header.Get("X-Aboard-Vertraulich-Bewahrt"))
	}

	// (f) PUT mit false bzw. ohne Schlüssel: bleibt vertraulich, Header nennt beide.
	for _, fall := range []struct{ name, karte, termin string }{
		{"false", `,"confidential":false`, `,"confidential":false`},
		{"ohne Schlüssel", ``, ``},
	} {
		resp, body := put778(t, url, boardMit778(fall.karte, fall.termin))
		if resp.StatusCode != 200 {
			t.Fatalf("(f) %s: %d %s", fall.name, resp.StatusCode, body)
		}
		h := resp.Header.Get("X-Aboard-Vertraulich-Bewahrt")
		if h != "2; ids=k1,t1" {
			t.Errorf("(f) %s: Header %q", fall.name, h)
		}
		var stand map[string]json.RawMessage
		if json.Unmarshal([]byte(body), &stand) != nil || stand["board"] == nil {
			t.Fatalf("(f) %s: Body ist nicht das Board: %s", fall.name, body)
		}
		if strings.Count(body, `"confidential":true`) != 2 {
			t.Errorf("(f) %s: nicht mehr vertraulich: %s", fall.name, body)
		}
		if resp.Header.Get("X-Aboard-Stand-Geaendert") != "1" {
			t.Errorf("(f) %s: X-Aboard-Stand-Geaendert fehlt", fall.name)
		}
	}

	// (g) Ein Mensch schaltet per Op ab — das wirkt.
	op := `{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}}]}`
	if s, b := e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de", op); s != 200 {
		t.Fatalf("(g) Op: %d %s", s, b)
	}
	// Danach ein PUT mit false: kein Header für die Karte (nichts zu bewahren),
	// der Termin bleibt vertraulich und wird genannt.
	resp, body := put778(t, url, boardMit778(`,"confidential":false`, `,"confidential":false`))
	if resp.StatusCode != 200 || resp.Header.Get("X-Aboard-Vertraulich-Bewahrt") != "1; ids=t1" {
		t.Fatalf("(g) PUT nach Op: %d %q %s", resp.StatusCode, resp.Header.Get("X-Aboard-Vertraulich-Bewahrt"), body)
	}
}
