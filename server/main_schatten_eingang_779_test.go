package main

// Karte #779 (ADR-0140 §3): Der Schatten-Eingang ist nur über den
// Agenten-Eingang mit einem abe_-Token erreichbar, nur bei boardpaar.seite =
// aussen und nur für gekoppelte Boards. Geprüft an der echten Kette aus
// hauptHandler im strengen Forward-Auth-Modus.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

type eingangUmgebung struct {
	db               *sql.DB
	srv              *httptest.Server
	cfg              *storage.ConfigRepo
	paar             *storage.BoardpaarRepo
	boards           *storage.BoardRepo
	schatten         *storage.SchattenRepo
	broker           *api.EventBroker
	abe, abs, altAbe string
	abt              string
	mux              *http.ServeMux
	zugang           *api.AgentZugang
}

func eingangAufbau(t *testing.T) *eingangUmgebung {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	u := &eingangUmgebung{db: db, boards: storage.NewBoardRepo(db), cfg: storage.NewConfigRepo(db, nil, uuidString),
		paar: storage.NewBoardpaarRepo(db), schatten: storage.NewSchattenRepo(db), broker: api.NewEventBroker()}
	for _, id := range []string{"b1", "b2"} {
		if _, err := u.boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := u.paar.AussenKoppeln(ctx, "b1", "https://innen.example/#board=innen-b1", "test"); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	u.mux = http.NewServeMux()
	api.NewBoardsAPI(u.boards).WithACL(storage.NewAclRepo(db)).WithActivities(storage.NewActivityRepo(db)).WithFold(motor).Register(u.mux)
	api.NewSchattenAusgangAPI(u.boards, u.cfg, u.paar).Register(u.mux)
	api.NewSchattenEingangAPI(u.cfg, u.paar, u.schatten, u.broker).Register(u.mux)
	if u.abe, _, err = u.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "push", "test"); err != nil {
		t.Fatal(err)
	}
	if u.abs, _, err = u.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "ausgang", "test"); err != nil {
		t.Fatal(err)
	}
	altAbe, alt, err := u.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "alt", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := u.paar.TokenWiderrufen(ctx, alt.ID); err != nil {
		t.Fatal(err)
	}
	u.altAbe = altAbe
	agenten := storage.NewAgentTokenRepo(db)
	if u.abt, _, err = agenten.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test"); err != nil {
		t.Fatal(err)
	}
	u.zugang = api.NewAgentZugang(agenten)
	ohneACL := func(h http.Handler) http.Handler { return h }
	u.srv = httptest.NewServer(hauptHandler(u.mux, ohneACL, u.zugang, true))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *eingangUmgebung) seite(t *testing.T, wert string) {
	t.Helper()
	if err := u.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, wert, false, "test"); err != nil {
		t.Fatal(err)
	}
}

// put schickt einen PUT und liefert Status und Antwort.
func put(t *testing.T, srv *httptest.Server, pfad, token, body string, kopf map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", srv.URL+pfad, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range kopf {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func schattenID779(n int) string {
	return strings.Replace(fmt.Sprintf("%043d", n), "0", "S", 1)
}

func eingangBody(n int) string {
	liste := make([]string, n)
	for i := range liste {
		liste[i] = fmt.Sprintf(`{"schattenId":%q,"art":"termin","titel":null,"vertraulich":true,"startDatum":"2026-10-15","startZeit":null,"endeDatum":null,"endeZeit":null,"bearbeiter":[],"vonAgent":false}`, schattenID779(i+1))
	}
	return `{"version":"v1.52.0","schatten":[` + strings.Join(liste, ",") + `]}`
}

const eingangKarte = `{"version":"1.52.0","schatten":[{"schattenId":"%s","art":"karte","nr":12,"titel":"Angebot prüfen","vertraulich":false,
	"spalte":"In Arbeit","prioritaet":"hoch","startDatum":null,"startZeit":null,"endeDatum":"2026-10-14","endeZeit":"09:30",
	"bearbeiter":["Gerd Muster"],"vonAgent":false}]}`

func TestSchattenEingang779_Wege(t *testing.T) {
	u := eingangAufbau(t)
	gut := fmt.Sprintf(eingangKarte, schattenID779(1))
	type fall = struct {
		name, methode, pfad, token string
		want                       int
	}
	pruefe := func(stufe string, faelle []fall) {
		t.Helper()
		for _, f := range faelle {
			if s := agentAnfrage(t, u.srv, f.methode, f.pfad, f.token, gut, nil); s != f.want {
				t.Errorf("%s / %s: %d, erwartet %d", stufe, f.name, s, f.want)
			}
		}
	}
	// Seite „aus“ und „innen“: Den Weg gibt es nicht, auch nicht als 401.
	// Ohne Board-Paar-Token entscheidet die Agenten-Kette (401) wie für
	// jeden anderen Pfad, bevor der Handler läuft.
	pruefe("aus", []fall{
		{"gekoppeltes Board", "PUT", "/agent/schatten/b1", u.abe, 404},
		{"ohne Token", "PUT", "/agent/schatten/b1", "", 401},
	})
	u.seite(t, "innen")
	pruefe("innen", []fall{{"gekoppeltes Board", "PUT", "/agent/schatten/b1", u.abe, 404}})
	if n := len(mustListe(t, u, "b1")); n != 0 {
		t.Fatalf("vor „aussen“ geschrieben: %d", n)
	}

	u.seite(t, "aussen")
	pruefe("aussen", []fall{
		{"gekoppeltes Board", "PUT", "/agent/schatten/b1", u.abe, 200},
		{"nicht gekoppelt", "PUT", "/agent/schatten/b2", u.abe, 404},
		{"unbekanntes Board", "PUT", "/agent/schatten/gibtsnicht", u.abe, 404},
		{"Service-Board", "PUT", "/agent/schatten/service", u.abe, 404},
		{"ohne Token", "PUT", "/agent/schatten/b1", "", 401},
		{"widerrufenes Token", "PUT", "/agent/schatten/b1", u.altAbe, 401},
		{"erfundenes abe_", "PUT", "/agent/schatten/b1", "abe_erfunden", 401},
		// abe_ auf jedem anderen Weg und mit jeder anderen Methode: 403.
		{"abe_ GET", "GET", "/agent/schatten/b1", u.abe, 403},
		{"abe_ HEAD", "HEAD", "/agent/schatten/b1", u.abe, 403},
		{"abe_ POST", "POST", "/agent/schatten/b1", u.abe, 403},
		{"abe_ DELETE", "DELETE", "/agent/schatten/b1", u.abe, 403},
		{"abe_ Unterpfad", "PUT", "/agent/schatten/b1/x", u.abe, 403},
		{"abe_ kodierter Pfad", "PUT", "/agent/schatten/b%2F1", u.abe, 403},
		{"abe_ auf Board (/agent/)", "PUT", "/agent/boards/b1", u.abe, 403},
		{"abe_ Ops (/agent/)", "POST", "/agent/boards/b1/ops", u.abe, 403},
		{"abe_ am Menschen-Pfad", "PUT", "/schatten/b1", u.abe, 403},
		{"abe_ auf Board (Menschen)", "GET", "/boards/b1", u.abe, 403},
		// Das Ausgangs-Token schreibt nicht, ein Agenten-Token auch nicht.
		{"abs_ PUT", "PUT", "/agent/schatten/b1", u.abs, 403},
		{"Agenten-Token (/agent/)", "PUT", "/agent/schatten/b1", u.abt, 403},
		{"Agenten-Token (Menschen)", "PUT", "/schatten/b1", u.abt, 401},
	})
	if n := len(mustListe(t, u, "b1")); n != 1 {
		t.Errorf("nach den Wegen: %d Schatten, erwartet 1", n)
	}
	if n := len(mustListe(t, u, "b2")); n != 0 {
		t.Errorf("nicht gekoppeltes Board beschrieben: %d", n)
	}
}

func mustListe(t *testing.T, u *eingangUmgebung, board string) []storage.SchattenZeile {
	t.Helper()
	l, err := u.schatten.Liste(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Eine Proxy-Identität wirkt nie: weder ersetzt sie das Token, noch stört
// sie einen gültigen Lauf (ADR-0140 §3).
func TestSchattenEingang779_ProxyIdentitaetOhneWirkung(t *testing.T) {
	u := eingangAufbau(t)
	u.seite(t, "aussen")
	kopf := map[string]string{"Remote-Email": "admin@example.org", "Remote-User": "admin", "X-Forwarded-Auth-Secret": pfadTestSecret}
	gut := fmt.Sprintf(eingangKarte, schattenID779(1))
	if s, _ := put(t, u.srv, "/agent/schatten/b1", "", gut, kopf); s != 401 {
		t.Errorf("ohne Token mit Remote-Email: %d", s)
	}
	if s, _ := put(t, u.srv, "/schatten/b1", "", gut, kopf); s == 200 {
		t.Errorf("Menschen-Pfad mit Remote-Email: 200")
	}
	if n := len(mustListe(t, u, "b1")); n != 0 {
		t.Fatalf("ohne Token geschrieben: %d", n)
	}
	if s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, gut, kopf); s != 200 {
		t.Errorf("abe_ mit Remote-Email: %d %s", s, b)
	}
}

// ABOARD_AGENT_PFAD=aus: Den Weg gibt es nicht (hauptHandler antwortet auf
// /agent/ mit 400, der Menschen-Pfad weist abe_ mit 403 ab).
func TestSchattenEingang779_AgentPfadAus(t *testing.T) {
	u := eingangAufbau(t)
	u.seite(t, "aussen")
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(u.mux, ohneACL, u.zugang, false))
	t.Cleanup(srv.Close)
	gut := fmt.Sprintf(eingangKarte, schattenID779(1))
	if s, _ := put(t, srv, "/agent/schatten/b1", u.abe, gut, nil); s != 400 {
		t.Errorf("/agent/ bei Pfad aus: %d", s)
	}
	if s, _ := put(t, srv, "/schatten/b1", u.abe, gut, nil); s != 403 {
		t.Errorf("Menschen-Pfad: %d", s)
	}
	if n := len(mustListe(t, u, "b1")); n != 0 {
		t.Errorf("geschrieben: %d", n)
	}
}

// Integrität: abgelehnt heißt nichts geschrieben — auch der Abgleich-Stand
// bleibt (Katalog: Hülle mit board, 6000 kleine, über 32 MB).
func TestSchattenEingang779_Grenzen(t *testing.T) {
	u := eingangAufbau(t)
	u.seite(t, "aussen")
	if s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, eingangBody(3), nil); s != 200 {
		t.Fatalf("Vorlauf: %d %s", s, b)
	}
	vorher, _ := u.schatten.Stand(context.Background(), "b1")
	faelle := []struct {
		name, body string
		want       int
	}{
		{"Hülle mit board", `{"version":"1.52.0","board":"innen-b1","schatten":[]}`, 422},
		{"6000 kleine Schatten", eingangBody(6000), 422},
		{"5001 Schatten", eingangBody(5001), 422},
		{"UUID als schattenId", strings.Replace(eingangBody(1), schattenID779(1), "0b7c6d1e-1111-4222-8333-944455556666", 1), 422},
		{"Bearbeiter mit @", strings.Replace(eingangBody(1), `"bearbeiter":[]`, `"bearbeiter":["g@x.de"]`, 1), 422},
		{"Hauptversion", strings.Replace(eingangBody(1), `"v1.52.0"`, `"v2.0.0"`, 1), 409},
		{"kein JSON", `{`, 422},
		{"über 32 MB", `{"version":"1.52.0","schatten":[],"x":"` + strings.Repeat("a", 32<<20) + `"}`, 413},
	}
	altVersion := api.BuildVersion
	api.BuildVersion = "v1.52.0"
	t.Cleanup(func() { api.BuildVersion = altVersion })
	for _, f := range faelle {
		s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, f.body, nil)
		if s != f.want {
			t.Errorf("%s: %d, erwartet %d (%.200s)", f.name, s, f.want, b)
		}
		if strings.Contains(b, "innen-b1") || strings.Contains(b, "g@x.de") {
			t.Errorf("%s: Antwort nennt den Wert: %.200s", f.name, b)
		}
	}
	if n := len(mustListe(t, u, "b1")); n != 3 {
		t.Errorf("nach Ablehnungen: %d Schatten, erwartet 3", n)
	}
	if nachher, _ := u.schatten.Stand(context.Background(), "b1"); *nachher != *vorher {
		t.Errorf("Abgleich-Stand bewegt: %+v → %+v", vorher, nachher)
	}
	// Token und Kopplung entscheiden VOR dem Body: Ein nicht gekoppeltes
	// Board ist 404, auch mit kaputtem oder übergroßem Body (ADR-0140 §3,
	// Reihenfolge im Handler). Ersetze prüft die Kopplung zwar noch einmal,
	// aber erst nach dem Lesen — dieser Fall trennt die beiden Stellen.
	for _, body := range []string{`{`, `{"version":"1.52.0","schatten":[],"x":"` + strings.Repeat("a", 32<<20) + `"}`} {
		if s, _ := put(t, u.srv, "/agent/schatten/b2", u.abe, body, nil); s != 404 {
			t.Errorf("nicht gekoppelt, Body %.10s…: %d, erwartet 404", body, s)
		}
	}
	// 5000 gehen durch.
	if s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, eingangBody(5000), nil); s != 200 {
		t.Errorf("5000: %d %.200s", s, b)
	}
}

// Nebenwirkungen: zweimal derselbe PUT → zweiter Lauf ohne Änderung, kein
// SSE; das Board (updated_at, Zustand) und das Aktivitäts-Log bleiben
// unberührt. Mit Änderung genau ein Ereignis schatten:aktualisiert, kein
// board:updated.
func TestSchattenEingang779_Nebenwirkungen(t *testing.T) {
	u := eingangAufbau(t)
	u.seite(t, "aussen")
	ctx := context.Background()
	vorher, _ := u.boards.Get(ctx, "b1")
	aktivitaeten := zaehle(t, u.db, `SELECT COUNT(*) FROM activities`)
	ereignisse, ab := u.broker.Subscribe("b1")
	defer ab()

	body := fmt.Sprintf(eingangKarte, schattenID779(1))
	s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, body, nil)
	var erg map[string]int
	_ = json.Unmarshal([]byte(b), &erg)
	if s != 200 || erg["neu"] != 1 || erg["geaendert"] != 0 || erg["entfernt"] != 0 || erg["anzahl"] != 1 || len(erg) != 4 {
		t.Fatalf("erster Lauf: %d %s", s, b)
	}
	select {
	case ev := <-ereignisse:
		if ev.Type != api.SchattenEreignis || strings.Contains(ev.Data, "etag") || !strings.Contains(ev.Data, `"anzahl":1`) {
			t.Errorf("Ereignis: %+v", ev)
		}
	default:
		t.Error("kein Ereignis nach Änderung")
	}
	s, b = put(t, u.srv, "/agent/schatten/b1", u.abe, body, nil)
	if s != 200 || !strings.Contains(b, `"neu":0,"geaendert":0,"entfernt":0,"anzahl":1`) {
		t.Fatalf("Wiederholung: %d %s", s, b)
	}
	select {
	case ev := <-ereignisse:
		t.Errorf("Ereignis ohne Änderung: %+v", ev)
	default:
	}
	nachher, _ := u.boards.Get(ctx, "b1")
	if nachher.UpdatedAt != vorher.UpdatedAt || string(nachher.StateJSON) != string(vorher.StateJSON) {
		t.Errorf("Board verändert")
	}
	if n := zaehle(t, u.db, `SELECT COUNT(*) FROM activities`); n != aktivitaeten {
		t.Errorf("Aktivitäten: %d → %d", aktivitaeten, n)
	}
	// Eine gültige leere Liste entfernt alle.
	if s, b := put(t, u.srv, "/agent/schatten/b1", u.abe, `{"version":"1.52.0","schatten":[]}`, nil); s != 200 || !strings.Contains(b, `"entfernt":1`) {
		t.Errorf("leere Liste: %d %s", s, b)
	}
}
