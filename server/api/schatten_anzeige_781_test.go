package api_test

// Karte #781 (ADR-0140 §5, §6): GET /boards/{id}/schatten — die Anzeige der
// Schatten im Browser außen (Board-Ansicht und Kalenderansicht).
//
//   - Nur auf der Seite „außen“ und nur für ein gekoppeltes Board; auf aus,
//     innen, ungültigem Wert und für ein nicht gekoppeltes Board: 404.
//   - Menschen-Kette: Rolle viewer genügt, ohne Rolle 403, ein Agent 403
//     (Positivliste), ein Board-Paar-Token 403.
//   - Ist server_config, die Kopplung oder die Schatten-Tabelle nicht lesbar:
//     503 — nicht „aus“ (Lesefehler ≠ fehlender Wert).
//   - Der GANZE Antworttext trägt weder den Titel eines vertraulichen
//     Schattens (auch wenn er in der Tabelle steht — zweite Schicht), noch
//     eine interne ID, eine E-Mail-Adresse oder einen Link außer dem aus §4.
//   - Aktualität (`letzter_abgleich`) kommt aus schatten_abgleich.

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
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	geheim781   = "GEHEIMTITEL-781"
	basis781    = "https://innen.example/#board=innen-uuid-781"
	sichtbar781 = "Angebot Müller 781"
)

type env781 struct {
	db   *sql.DB
	cfg  *storage.ConfigRepo
	sr   *storage.SchattenRepo
	bp   *storage.BoardpaarRepo
	srv  *httptest.Server // Menschen-Kette: ForwardAuth → ACLGuard → mux
	agt  *httptest.Server // Agenten-Kette: Bearer → Positivliste → mux
	toks map[string]string
	// baue: dieselbe Verdrahtung mit anderen Repos (Lesefehler-Tests)
	baue func(cfg *storage.ConfigRepo, bp *storage.BoardpaarRepo, sr *storage.SchattenRepo) *httptest.Server
}

func s781(s string) *string { return &s }
func n781(n int) *int       { return &n }

func newEnv781(t *testing.T) *env781 {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	for _, id := range []string{"ext1", "ext2", "ext3"} {
		st := `{"version":"2","board":{"id":"` + id + `","title":"Extern ` + id + `","sprints":[]}}`
		if _, err := boards.Insert(ctx, id, "Extern "+id, "2", []byte(st)); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de"}, {"u-berta", "berta@firma.de"}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	// anna: viewer auf ext1 und ext2; berta: nur ext2 (also keine Rolle auf ext1).
	for _, z := range [][2]string{{"ext1", "anna@firma.de"}, {"ext2", "anna@firma.de"}, {"ext2", "berta@firma.de"}} {
		if err := acl.Set(ctx, z[0], z[1], "viewer"); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	bp := storage.NewBoardpaarRepo(db)
	sr := storage.NewSchattenRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)

	e := &env781{db: db, cfg: cfg, sr: sr, bp: bp, toks: map[string]string{}}
	e.baue = func(c *storage.ConfigRepo, b *storage.BoardpaarRepo, s *storage.SchattenRepo) *httptest.Server {
		mux := http.NewServeMux()
		api.NewSchattenAnzeigeAPI(c, b, s).Register(mux)
		srv := httptest.NewServer(api.WithForwardAuthIfEnabled(api.NewACLGuard(acl, users, bootstrap).Middleware(mux)))
		t.Cleanup(srv.Close)
		return srv
	}
	e.srv = e.baue(cfg, bp, sr)

	// Agenten-Kette mit dem Mux der Anzeige: Der Pfad steht nicht in der Positivliste.
	repo := storage.NewAgentTokenRepo(db)
	k, _, err := repo.Anlegen(ctx, storage.AgentTokenAnlage{Name: "projekt", BoardID: "ext1", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	e.toks["agent"] = k
	for _, typ := range []string{storage.BoardpaarTypEingang, storage.BoardpaarTypAusgang} {
		kl, _, err := bp.TokenAnlegen(ctx, typ, "t-"+typ, "admin")
		if err != nil {
			t.Fatal(err)
		}
		e.toks[typ] = kl
	}
	mux := http.NewServeMux()
	api.NewSchattenAnzeigeAPI(cfg, bp, sr).Register(mux)
	e.agt = httptest.NewServer(api.BoardpaarSchranke(api.NewAgentZugang(repo).Middleware(mux), true))
	t.Cleanup(e.agt.Close)

	// ext1 und ext3 sind gekoppelt, ext2 nicht.
	for id, basis := range map[string]string{"ext1": basis781, "ext3": "https://innen.example/#board=innen-ext3"} {
		if _, err := bp.AussenKoppeln(ctx, id, basis, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	// ext1: Karte mit Nummer, vertrauliche Karte (Titel TROTZDEM in der Tabelle, zweite
	// Schicht), Termin (Titel mit „@“ im Bearbeiter), Agentenkarte, Karte mit leerem Titel.
	e.ersetze(t, "ext1", []schatten.Eingang{
		{SchattenID: e.id("A", 1), Art: schatten.ArtKarte, Nr: n781(42), Titel: s781(sichtbar781), Spalte: s781("In Arbeit"),
			Prioritaet: s781("hoch"), EndeDatum: s781("2026-10-22"), Bearbeiter: []string{"Gerd Muster", "gerd@firma.de", "external:x@y.de"}},
		{SchattenID: e.id("B", 2), Art: schatten.ArtKarte, Vertraulich: true, Titel: s781(geheim781), Nr: n781(43), EndeDatum: s781("2026-10-23")},
		{SchattenID: e.id("C", 3), Art: schatten.ArtTermin, Titel: s781("Jour fixe"), StartDatum: s781("2026-10-24"), StartZeit: s781("10:00"),
			EndeDatum: s781("2026-10-24"), EndeZeit: s781("11:00")},
		{SchattenID: e.id("D", 4), Art: schatten.ArtKarte, Nr: n781(44), Titel: s781("Von Agent"), VonAgent: true},
		{SchattenID: e.id("E", 5), Art: schatten.ArtKarte, Nr: n781(45), Titel: s781("   ")},
	})
	e.ersetze(t, "ext3", []schatten.Eingang{{SchattenID: e.id("F", 6), Art: schatten.ArtKarte, Nr: n781(7), Titel: s781("Nachbar-Schatten-781")}})
	e.seite(t, api.BoardpaarSeiteAussen)
	return e
}

func (e *env781) id(prefix string, n int) string {
	return prefix + strings.Repeat("x", schatten.SchattenIDZeichen-len(prefix)-1) + fmt.Sprint(n%10)
}

func (e *env781) ersetze(t *testing.T, board string, menge []schatten.Eingang) {
	t.Helper()
	if _, err := e.sr.Ersetze(context.Background(), board, menge); err != nil {
		t.Fatal(err)
	}
}

func (e *env781) seite(t *testing.T, wert string) {
	t.Helper()
	if err := e.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, wert, false, "test"); err != nil {
		t.Fatal(err)
	}
}

func hole781(t *testing.T, srv *httptest.Server, pfad, wer, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+pfad, nil)
	if wer != "" {
		req.Header.Set("Remote-Email", wer)
		req.Header.Set("Remote-User", wer)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type antwort781 struct {
	LetzterAbgleich *string `json:"letzter_abgleich"`
	Schatten        []struct {
		SchattenID  string   `json:"schatten_id"`
		Art         string   `json:"art"`
		Nummer      string   `json:"nummer"`
		Titel       string   `json:"titel"`
		Platzhalter bool     `json:"titel_platzhalter"`
		Spalte      string   `json:"spalte"`
		Prioritaet  string   `json:"prioritaet"`
		StartDatum  string   `json:"start_datum"`
		EndeDatum   string   `json:"ende_datum"`
		Bearbeiter  []string `json:"bearbeiter"`
		VonAgent    bool     `json:"von_agent"`
		Link        string   `json:"link"`
	} `json:"schatten"`
}

func lies781(t *testing.T, e *env781, board string) (antwort781, string) {
	t.Helper()
	s, b := hole781(t, e.srv, "/boards/"+board+"/schatten", "anna@firma.de", "")
	if s != http.StatusOK {
		t.Fatalf("GET %s: %d %s", board, s, b)
	}
	var a antwort781
	if err := json.Unmarshal([]byte(b), &a); err != nil {
		t.Fatalf("Antwort: %v: %s", err, b)
	}
	return a, b
}

// Außen: Feldsatz wörtlich, Titelregel, Link nur aus §4, Reihenfolge.
func TestSchattenAnzeige781_Aussen(t *testing.T) {
	e := newEnv781(t)
	a, roh := lies781(t, e, "ext1")
	if len(a.Schatten) != 5 {
		t.Fatalf("%d Schatten, erwartet 5: %s", len(a.Schatten), roh)
	}
	// Karten nach Nummer, dann Termine: 42, 43, 44, 45, Termin.
	// platzhalter: der Titel ist „Interne Karte“ (K-6): der Browser setzt die Nummer
	// im Kalender nur vor einen echten Titel, wie der Feed (ical.go schattenEvent).
	soll := []struct {
		nr, titel, link string
		platzhalter     bool
	}{
		{"i-42", sichtbar781, basis781 + "&card=42", false},
		{"i-43", "Interne Karte", basis781 + "&card=43", true},
		{"i-44", "Von Agent", basis781 + "&card=44", false},
		{"i-45", "Interne Karte", basis781 + "&card=45", true},
		{"", "Jour fixe", basis781, false}, // Termine haben keine Nummer: der Link führt aufs Board
	}
	for i, s := range soll {
		g := a.Schatten[i]
		if g.Nummer != s.nr || g.Titel != s.titel || g.Link != s.link || g.Platzhalter != s.platzhalter {
			t.Errorf("Eintrag %d: nummer=%q titel=%q link=%q platzhalter=%v, erwartet %+v", i, g.Nummer, g.Titel, g.Link, g.Platzhalter, s)
		}
	}
	k := a.Schatten[0]
	if k.Art != "karte" || k.Spalte != "In Arbeit" || k.Prioritaet != "hoch" || k.EndeDatum != "2026-10-22" {
		t.Errorf("Feldsatz der Karte: %+v", k)
	}
	if strings.Join(k.Bearbeiter, "|") != "Gerd Muster" {
		t.Errorf("Bearbeiter mit @ oder external: bleiben drin: %v", k.Bearbeiter)
	}
	if !a.Schatten[2].VonAgent || a.Schatten[0].VonAgent {
		t.Error("von_agent falsch")
	}
	// Der ganze Text: nie der vertrauliche Titel, keine Adresse, keine fremden Schatten.
	for _, verboten := range []string{geheim781, "@", "external:", "Nachbar-Schatten-781", "innen-ext3"} {
		if strings.Contains(roh, verboten) {
			t.Errorf("Antwort enthält %q: %s", verboten, roh)
		}
	}
	// Der einzige Link nach innen ist der aus §4 (Basis + &card=<nr>).
	for _, tail := range strings.Split(roh, "http")[1:] {
		if !strings.HasPrefix("http"+tail, "https://innen.example/#board=innen-uuid-781") {
			t.Errorf("fremder Link: http%.60s", tail)
		}
	}
	// Aktualität: der Zeitpunkt des letzten Laufs (Ersetze setzt ihn).
	if a.LetzterAbgleich == nil || *a.LetzterAbgleich == "" {
		t.Errorf("letzter_abgleich fehlt: %s", roh)
	}
}

// Ohne Basis keine Link-Angabe (der Eintrag fehlt, statt auf eine geratene Adresse zu zeigen) —
// und ein Board ohne Schatten liefert eine leere Liste, nicht null.
func TestSchattenAnzeige781_LeereListeUndKeinLink(t *testing.T) {
	e := newEnv781(t)
	e.ersetze(t, "ext1", nil)
	a, roh := lies781(t, e, "ext1")
	if a.Schatten == nil || len(a.Schatten) != 0 || !strings.Contains(roh, `"schatten":[]`) {
		t.Errorf("leer: %s", roh)
	}
	// Basis im Repo auf leer setzen: kein Link in der Antwort.
	if _, err := e.db.Exec(`UPDATE boardpaar_aussen SET innen_link_basis = '' WHERE aussen_board_id = 'ext1'`); err != nil {
		t.Fatal(err)
	}
	e.ersetze(t, "ext1", []schatten.Eingang{{SchattenID: e.id("A", 1), Art: schatten.ArtKarte, Nr: n781(1), Titel: s781("X")}})
	if _, roh := lies781(t, e, "ext1"); strings.Contains(roh, `"link"`) {
		t.Errorf("Link trotz leerer Basis: %s", roh)
	}
}

// Auf aus, innen, ungültigem Wert: 404, auch wenn Schatten in der Tabelle stehen.
func TestSchattenAnzeige781_AusUndInnen(t *testing.T) {
	e := newEnv781(t)
	for _, seite := range []string{"aus", "innen", "quatsch", ""} {
		e.seite(t, seite)
		s, b := hole781(t, e.srv, "/boards/ext1/schatten", "anna@firma.de", "")
		if s != http.StatusNotFound {
			t.Errorf("Seite %q: %d %s, erwartet 404", seite, s, b)
		}
		if strings.Contains(b, sichtbar781) || strings.Contains(b, "innen.example") {
			t.Errorf("Seite %q liefert Schatten aus: %s", seite, b)
		}
	}
	e.seite(t, api.BoardpaarSeiteAussen) // Gegenkontrolle: der Test ist nicht leer gelaufen
	if s, _ := hole781(t, e.srv, "/boards/ext1/schatten", "anna@firma.de", ""); s != http.StatusOK {
		t.Errorf("außen: %d", s)
	}
}

// Ein nicht gekoppeltes Board hat keine Schatten-Ansicht: 404.
func TestSchattenAnzeige781_NichtGekoppelt(t *testing.T) {
	e := newEnv781(t)
	if s, b := hole781(t, e.srv, "/boards/ext2/schatten", "anna@firma.de", ""); s != http.StatusNotFound {
		t.Errorf("ext2 (nicht gekoppelt): %d %s", s, b)
	}
}

// Rolle: viewer genügt; ohne Rolle am Board 403 (ACL-Guard) — und die Schatten
// eines anderen Boards erscheinen nie (nur das gefragte).
func TestSchattenAnzeige781_Rolle(t *testing.T) {
	e := newEnv781(t)
	if s, _ := hole781(t, e.srv, "/boards/ext1/schatten", "anna@firma.de", ""); s != http.StatusOK {
		t.Errorf("viewer: %d", s)
	}
	if s, b := hole781(t, e.srv, "/boards/ext1/schatten", "berta@firma.de", ""); s != http.StatusForbidden || strings.Contains(b, sichtbar781) {
		t.Errorf("ohne Rolle: %d %s", s, b)
	}
}

// Agenten und Board-Paar-Tokens erreichen den Weg nicht (Positivliste, Schranke).
func TestSchattenAnzeige781_AgentenNicht(t *testing.T) {
	e := newEnv781(t)
	for name, tok := range e.toks {
		s, b := hole781(t, e.agt, "/boards/ext1/schatten", "", tok)
		if s != http.StatusForbidden {
			t.Errorf("%s: %d %s, erwartet 403", name, s, b)
		}
		if strings.Contains(b, sichtbar781) || strings.Contains(b, "innen.example") {
			t.Errorf("%s: Schatten ausgeliefert: %s", name, b)
		}
	}
}

// Lesefehler ≠ „aus“: Ist die Einstellung, die Kopplung oder die Schatten-Tabelle
// nicht lesbar, antwortet der Weg 503 (kein leerer Stand, der die Anzeige räumt).
func TestSchattenAnzeige781_LesefehlerIst503(t *testing.T) {
	e := newEnv781(t)
	kaputt := func() *sql.DB {
		db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
		if err != nil {
			t.Fatal(err)
		}
		_ = db.Close() // jede Abfrage scheitert
		return db
	}()
	n := 0
	for name, srv := range map[string]*httptest.Server{
		"Einstellung": e.baue(storage.NewConfigRepo(kaputt, nil, func() string { n++; return fmt.Sprint(n) }), e.bp, e.sr),
		"Kopplung":    e.baue(e.cfg, storage.NewBoardpaarRepo(kaputt), e.sr),
		"Schatten":    e.baue(e.cfg, e.bp, storage.NewSchattenRepo(kaputt)),
	} {
		s, b := hole781(t, srv, "/boards/ext1/schatten", "anna@firma.de", "")
		if s != http.StatusServiceUnavailable {
			t.Errorf("%s nicht lesbar: %d %s, erwartet 503", name, s, b)
		}
		if strings.Contains(b, sichtbar781) {
			t.Errorf("%s: %s", name, b)
		}
	}
}
