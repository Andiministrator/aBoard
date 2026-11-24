package api_test

// Karte #789 (ADR-0140 §7, §10 W-4): Board-Paar in den Agenten-Werkzeugen.
//
//   - Außen liefern board_lesen, karte_zeigen (i-<nr>) und karten_suchen die
//     Schatten — über OpenAPI und MCP, ohne Link, ohne Basisadresse, ohne
//     interne Kennung, ohne E-Mail-Adresse, ohne den Titel einer
//     vertraulichen Karte (T2, T21, T1 Fall 2). Geprüft auf dem GANZEN
//     Antworttext.
//   - Schreibende Werkzeuge finden Schatten nicht: „i-12“ → 400 mit dem
//     Wortlaut aus §7.
//   - Auf aus und innen bleiben die Antworten byte-gleich zu einer
//     Verdrahtung ohne Board-Paar (alte Verdrahtung, dieselbe Datenbank mit
//     Schatten darin).
//   - Werkzeugtexte je Seite (E-3), in OpenAPI UND MCP; auf aus kein Paar-Text.
//   - T3/W-4: Was ein Agent innen anlegt, entsteht auf diesem (internen)
//     Server und ist vertraulich — roh, über Werkzeug und über MCP; ein
//     Mensch legt nicht vertraulich an; auf aus/außen bleibt alles, wie es war.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	innenUUID789     = "3f2a9c1e-7b4d-4e8a-9c0f-1a2b3c4d5e6f"
	innenBasis789    = "https://innen.example/#board=" + innenUUID789
	sichtbar789      = "SCHATTEN-SICHTBAR-789"
	geheim789        = "GEHEIMTITEL-789"
	termin789        = "SCHATTEN-TERMIN-789"
	eins789          = "SCHATTEN-EINS-789"
	nurLesbar789     = "i-12 ist eine interne Karte und hier nur lesbar"
	paarTextInnen789 = "Was außen steht, kann jeder lesen, auch Cloud-Agenten."
	mitAt789         = "Rückruf an kunde@firma.de"
)

type env789 struct {
	neu    *env671 // Verdrahtung wie main.go (MitBoardpaar)
	alt    *env671 // Verdrahtung ohne Board-Paar, dieselbe Datenbank
	mensch *httptest.Server
	db     interface{ Close() error }
	cfg    *storage.ConfigRepo
	bp     *storage.BoardpaarRepo
	sr     *storage.SchattenRepo
	// baueMit: dieselbe Verdrahtung wie neu, aber mit eigenem Konfig-Repo
	// für die Seite (K-2: nicht lesbare Einstellung). Liefert Agenten- und
	// Menschen-Server.
	baueMit func(cfg *storage.ConfigRepo) (*env671, *httptest.Server)
}

func newEnv789(t *testing.T, mitBasis bool) *env789 {
	t.Helper()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	datei := filepath.Join(t.TempDir(), "owui-jwt-secret")
	if err := os.WriteFile(datei, geheimnis669, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(api.OWUIJWTSecretFileEnv, datei)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	for id, titel := range map[string]string{"b1": "Marketing", "b2": "Technik"} {
		if _, err := boards.Insert(ctx, id, titel, "2", []byte(board671(id, titel))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	for _, z := range [][3]string{{"b1", "anna@firma.de", "editor"}, {"b2", "anna@firma.de", "editor"}} {
		if err := acl.Set(ctx, z[0], z[1], z[2]); err != nil {
			t.Fatal(err)
		}
	}
	repo := storage.NewAgentTokenRepo(db)
	anlegen := func(a storage.AgentTokenAnlage) string {
		k, _, err := repo.Anlegen(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	token := anlegen(storage.AgentTokenAnlage{Name: "open-webui", Role: storage.AgentRolleEditor, CreatedBy: "cli", Delegierend: true})
	projekt := anlegen(storage.AgentTokenAnlage{Name: "projekt", BoardID: "b1", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	bp := storage.NewBoardpaarRepo(db)
	sr := storage.NewSchattenRepo(db)
	zugang := api.NewAgentZugang(repo)
	if err := zugang.MitOWUIDelegation(acl); err != nil {
		t.Fatal(err)
	}
	baue := func(mitPaar bool, paarCfg *storage.ConfigRepo) (*env671, http.Handler) {
		mux := http.NewServeMux()
		bapi := api.NewBoardsAPI(boards).MitKalenderKonfig(cfg).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).
			WithFold(testMotor).WithUsers(users)
		if mitBasis {
			bapi.WithOeffentlicheBasis(basis671 + "/")
		}
		if mitPaar {
			bapi.MitBoardpaar(paarCfg, bp, sr)
		}
		bapi.Register(mux)
		bapi.RegisterWerkzeuge(mux)
		e := &env671{boards: boards, acl: acl, db: db, repo: repo, token: token, projekt: projekt, cfg: cfg, mux: mux}
		e.agent = httptest.NewServer(zugang.Middleware(mux))
		t.Cleanup(e.agent.Close)
		var h http.Handler = api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)
		h = api.WithForwardAuthIfEnabled(h)
		return e, api.OhneBearer(h)
	}
	e := &env789{db: db, cfg: cfg, bp: bp, sr: sr}
	var mensch http.Handler
	e.neu, mensch = baue(true, cfg)
	e.alt, _ = baue(false, cfg)
	e.baueMit = func(c *storage.ConfigRepo) (*env671, *httptest.Server) {
		x, h := baue(true, c)
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return x, srv
	}
	e.mensch = httptest.NewServer(mensch)
	t.Cleanup(e.mensch.Close)

	if _, err := bp.AussenKoppeln(ctx, "b1", innenBasis789, "admin"); err != nil {
		t.Fatal(err)
	}
	mitN := func(n int) *int { return &n }
	text := func(s string) *string { return &s }
	if _, err := sr.Ersetze(ctx, "b1", []schatten.Eingang{
		{SchattenID: sid782("A", 1), Art: schatten.ArtKarte, Nr: mitN(12), Titel: text(sichtbar789), Spalte: text("In Arbeit"),
			Prioritaet: text("hoch"), EndeDatum: text("2026-10-14"), EndeZeit: text("09:30"),
			// Die E-Mail-Adresse lässt der Eingang nicht zu; hier steht sie
			// trotzdem in der Tabelle, damit die Werkzeuge als zweite Schicht
			// geprüft werden (T21).
			Bearbeiter: []string{"Gerd Muster", "gerd@firma.de", "external:x"}, VonAgent: true},
		// Vertraulich, der Titel steht trotzdem in der Tabelle (zweite Schicht, T1 Fall 2).
		{SchattenID: sid782("B", 2), Art: schatten.ArtKarte, Nr: mitN(13), Vertraulich: true, Titel: text(geheim789)},
		{SchattenID: sid782("C", 3), Art: schatten.ArtTermin, Titel: text(termin789), StartDatum: text("2026-10-15"), StartZeit: text("10:00"),
			Bearbeiter: []string{}},
		// Gleiche Nummer wie die externe Karte #1 (Katalog „Kartennummern“).
		{SchattenID: sid782("D", 4), Art: schatten.ArtKarte, Nr: mitN(1), Titel: text(eins789)},
		// Kritiker K-8: Der Titel einer NICHT vertraulichen Karte ist Inhalt
		// von Menschen und geht unverändert hinaus — auch mit „@“ (der
		// Eingang lehnt ihn ab; hier steht er trotzdem, damit die Grenze
		// getestet ist). T21 betrifft die Systemfelder, nicht diesen Titel.
		{SchattenID: sid782("E", 5), Art: schatten.ArtKarte, Nr: mitN(14), Titel: text(mitAt789)},
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env789) seite(t *testing.T, wert string) {
	t.Helper()
	if err := e.cfg.Set(context.Background(), api.KonfigBoardpaarSeite, wert, false, "test"); err != nil {
		t.Fatal(err)
	}
}

// mcpText: der ganze JSON-Text einer MCP-Antwort.
func mcpText789(t *testing.T, m map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(m)
	return string(b)
}

// verboten789: was in keiner Agenten-Antwort außen stehen darf (T2, T21,
// T1 Fall 2). mitHTTP: ohne öffentliche Basis darf gar kein „http“ stehen.
func verboten789(t *testing.T, wo, text string, mitHTTP bool) {
	t.Helper()
	// Die HTML-Ansicht trägt CSS (@media) und eine meta-Zeile (http-equiv) —
	// beides kein Inhalt; alles andere zählt.
	// Ebenso der nicht vertrauliche Titel mit „@“ (K-8, Grenze: Inhalt von
	// Menschen geht unverändert hinaus; geprüft in _AgentensichtAussen).
	text = strings.NewReplacer("@media", "", "http-equiv", "", mitAt789, "", jsonEsc789(mitAt789), "").Replace(text)
	for _, v := range []string{innenUUID789, "innen.example", "#board=" + innenUUID789, geheim789, "@", "external:"} {
		if strings.Contains(text, v) {
			t.Errorf("%s: enthält %q: %.600s", wo, v, text)
		}
	}
	if mitHTTP && strings.Contains(strings.ToLower(text), "http") {
		t.Errorf("%s: enthält einen Link (http): %.600s", wo, text)
	}
}

// jsonEsc789: wie ein Text in JSON erscheint (MCP gibt ihn noch einmal als
// JSON-String im Text aus).
func jsonEsc789(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// TestBoardpaar789_AgentensichtAussen: E-1/T2/T21 über OpenAPI und MCP.
func TestBoardpaar789_AgentensichtAussen(t *testing.T) {
	for _, mitBasis := range []bool{false, true} {
		t.Run(fmt.Sprintf("basis=%v", mitBasis), func(t *testing.T) {
			e := newEnv789(t, mitBasis)
			e.seite(t, api.BoardpaarSeiteAussen)
			x := e.neu
			// OpenAPI.
			for _, f := range []struct{ pfad, muss string }{
				{"/werkzeuge/boards/b1", sichtbar789},
				{"/werkzeuge/boards/b1/karten/i-12?format=json", sichtbar789},
				{"/werkzeuge/boards/b1/karten/i-12", sichtbar789}, // HTML-Ansicht
				{"/werkzeuge/boards/b1/karten/i-13?format=json", "Interne Karte"},
				{"/werkzeuge/karten?q=" + sichtbar789, sichtbar789},
				{"/werkzeuge/karten?q=i-12", sichtbar789},
				{"/werkzeuge/boards", "Marketing"},
			} {
				s, b := x.rufe(t, x.projekt, "", "GET", f.pfad, "")
				if s != http.StatusOK || !strings.Contains(string(b), f.muss) {
					t.Fatalf("GET %s: %d %.400s", f.pfad, s, b)
				}
				verboten789(t, "GET "+f.pfad, string(b), !mitBasis)
			}
			// Der Feldsatz aus §7 im Einzelnen (board_lesen).
			_, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1", "")
			var stand struct {
				Board  map[string]any   `json:"board"`
				Karten []map[string]any `json:"karten"`
				Intern []map[string]any `json:"intern"`
			}
			if err := json.Unmarshal(b, &stand); err != nil {
				t.Fatal(err)
			}
			if len(stand.Intern) != 5 {
				t.Fatalf("intern: %d Einträge, erwartet 5: %s", len(stand.Intern), b)
			}
			// K-8: nicht vertraulicher Titel mit „@“ unverändert.
			mitAt := false
			for _, s := range stand.Intern {
				mitAt = mitAt || (s["nummer"] == "i-14" && s["titel"] == mitAt789)
			}
			if !mitAt {
				t.Errorf("K-8: Titel %q nicht unverändert unter intern: %s", mitAt789, b)
			}
			var zwoelf, dreizehn, terminS map[string]any
			for _, s := range stand.Intern {
				switch s["nummer"] {
				case "i-12":
					zwoelf = s
				case "i-13":
					dreizehn = s
				}
				if s["art"] == "termin" {
					terminS = s
				}
			}
			erwartet := map[string]any{"art": "karte", "nummer": "i-12", "schattenId": sid782("A", 1), "titel": sichtbar789,
				"spalte": "In Arbeit", "prioritaet": "hoch", "endeDatum": "2026-10-14", "endeZeit": "09:30",
				"bearbeiter": []any{"Gerd Muster"}, "vonAgent": true, "nurLesbar": true}
			if g, w := jsonText(zwoelf), jsonText(erwartet); g != w {
				t.Errorf("Feldsatz i-12:\n got %s\nwant %s", g, w)
			}
			if dreizehn["titel"] != "Interne Karte" || dreizehn["nurLesbar"] != true {
				t.Errorf("vertrauliche Karte: %v", dreizehn)
			}
			if terminS == nil || terminS["titel"] != termin789 || terminS["startZeit"] != "10:00" || terminS["nummer"] != nil {
				t.Errorf("Termin-Schatten: %v", terminS)
			}
			if bp, _ := stand.Board["boardpaar"].(map[string]any); bp["rolle"] != "extern" || bp["gegenstueck"] != "intern" {
				t.Errorf("board.boardpaar: %v", stand.Board)
			}
			// Kartennummern (Katalog): „1“ ist die externe Karte, „i-1“ der Schatten.
			if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", ""); s != http.StatusOK ||
				!strings.Contains(string(b), "Offene Karte") || strings.Contains(string(b), eins789) {
				t.Errorf("karte 1: %d %.300s", s, b)
			}
			if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1/karten/i-1?format=json", ""); s != http.StatusOK ||
				!strings.Contains(string(b), eins789) || strings.Contains(string(b), "Offene Karte") {
				t.Errorf("karte i-1: %d %.300s", s, b)
			}
			// Die Suche liest den Platzhalter „Interne Karte“ nicht als Titel —
			// sonst verriete sie, welche Karten vertraulich sind.
			if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/karten?q=Interne", ""); s != http.StatusOK || !strings.Contains(string(b), `"intern":[]`) {
				t.Errorf("Suche nach dem Platzhalter: %d %.300s", s, b)
			}
			if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1/karten/i-99?format=json", ""); s != http.StatusNotFound {
				t.Errorf("karte i-99: %d %s", s, b)
			}
			// Delegierend (Open WebUI) sieht dieselben Schatten.
			if s, b := x.rufe(t, x.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1", ""); s != http.StatusOK || !strings.Contains(string(b), sichtbar789) {
				t.Errorf("delegierend board_lesen: %d %.300s", s, b)
			} else {
				verboten789(t, "delegierend board_lesen", string(b), !mitBasis)
			}
			// MCP.
			for _, f := range []struct{ name, args, muss string }{
				{"board_lesen", `{"boardId":"b1"}`, sichtbar789},
				{"karte_zeigen", `{"boardId":"b1","karte":"i-12","format":"json"}`, sichtbar789},
				{"karte_zeigen", `{"boardId":"b1","karte":"i-13","format":"json"}`, "Interne Karte"},
				{"karten_suchen", `{"q":"SCHATTEN"}`, sichtbar789},
			} {
				s, m := x.mcpWerkzeug(t, x.projekt, "", f.name, f.args)
				fehler, text, _ := ergebnis753(t, m)
				if s != http.StatusOK || fehler || !strings.Contains(text, f.muss) {
					t.Fatalf("MCP %s %s: %d %v %.300s", f.name, f.args, s, fehler, text)
				}
				verboten789(t, "MCP "+f.name, mcpText789(t, m), !mitBasis)
			}
		})
	}
}

// TestBoardpaar789_SchreibendeWerkzeugeFindenKeineSchatten: „i-12“ → 400
// mit dem Wortlaut aus §7, und die Schatten bleiben unverändert.
func TestBoardpaar789_SchreibendeWerkzeugeFindenKeineSchatten(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	x := e.neu
	vorher := schattentest.Momentaufnahme(t, x.db)
	staende := x.stand(t, "b1")
	for _, f := range []struct{ methode, pfad, body string }{
		{"POST", "/werkzeuge/boards/b1/karten/i-12/kommentare", `{"text":"Hallo"}`},
		{"POST", "/werkzeuge/boards/b1/karten/i-12/verschieben", `{"spalte":"Ready"}`},
		{"POST", "/werkzeuge/boards/b1/karten/i-12/aendern", `{"prioritaet":"hoch"}`},
		{"POST", "/werkzeuge/boards/b1/karten/i-12/archivieren", `{}`},
		{"POST", "/werkzeuge/boards/b1/karten/1/verweise", `{"ziel":"i-12","typ":"related"}`},
		{"POST", "/werkzeuge/boards/b1/karten/1/verweise/entfernen", `{"ziel":"i-12","typ":"related"}`},
		{"GET", "/werkzeuge/boards/b1/karten/i-12/loeschen", ""},
		{"POST", "/werkzeuge/boards/b1/karten/i-12/loeschen", `{"code":"x"}`},
		{"GET", "/werkzeuge/boards/b1/karten/i-12/anhaenge", ""},
	} {
		s, b := x.rufe(t, x.projekt, "", f.methode, f.pfad, f.body)
		if s != http.StatusBadRequest || !strings.Contains(string(b), nurLesbar789) {
			t.Errorf("%s %s: %d %s", f.methode, f.pfad, s, b)
		}
	}
	// MCP: dasselbe als Werkzeug-Fehler.
	_, m := x.mcpWerkzeug(t, x.projekt, "", "kommentieren", `{"boardId":"b1","karte":"i-12","text":"Hallo"}`)
	if fehler, text, _ := ergebnis753(t, m); !fehler || !strings.Contains(text, nurLesbar789) {
		t.Errorf("MCP kommentieren i-12: %v %s", fehler, text)
	}
	if staende != x.stand(t, "b1") {
		t.Error("Board wurde geändert")
	}
	schattentest.Unveraendert(t, x.db, vorher, "schreibende Werkzeuge mit i-12")
}

// TestBoardpaar789_AusUndInnenUnveraendert: Auf aus und innen ist jede
// Antwort byte-gleich zur Verdrahtung ohne Board-Paar — obwohl die Tabelle
// Schatten trägt. Gegenkontrolle: außen unterscheidet sich board_lesen.
func TestBoardpaar789_AusUndInnenUnveraendert(t *testing.T) {
	e := newEnv789(t, true)
	pfade := []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/werkzeuge/boards/b1?spalte=Ready",
		"/werkzeuge/boards/b1/karten/1?format=json", "/werkzeuge/boards/b1/karten/i-12?format=json",
		"/werkzeuge/karten?q=Karte", "/werkzeuge/karten?q=i-12", "/werkzeuge/karten?q=SCHATTEN", "/openapi.json"}
	vergleiche := func(seite string) {
		t.Helper()
		e.seite(t, seite)
		for _, p := range pfade {
			s1, b1 := e.alt.rufe(t, e.alt.projekt, "", "GET", p, "")
			s2, b2 := e.neu.rufe(t, e.neu.projekt, "", "GET", p, "")
			if s1 != s2 || string(b1) != string(b2) {
				t.Errorf("%s %s: alt %d, neu %d\nalt %.300s\nneu %.300s", seite, p, s1, s2, b1, b2)
			}
			if strings.Contains(string(b2), "SCHATTEN-") {
				t.Errorf("%s %s: Schatten in der Antwort: %.300s", seite, p, b2)
			}
		}
		_, m1 := e.alt.mcpRufe(t, e.alt.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
		_, m2 := e.neu.mcpRufe(t, e.neu.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
		if mcpText789(t, m1) != mcpText789(t, m2) {
			t.Errorf("%s: MCP tools/list unterscheidet sich", seite)
		}
		_, m1 = e.alt.mcpWerkzeug(t, e.alt.projekt, "", "board_lesen", `{"boardId":"b1"}`)
		_, m2 = e.neu.mcpWerkzeug(t, e.neu.projekt, "", "board_lesen", `{"boardId":"b1"}`)
		if mcpText789(t, m1) != mcpText789(t, m2) {
			t.Errorf("%s: MCP board_lesen unterscheidet sich", seite)
		}
	}
	vergleiche(api.BoardpaarSeiteAus)
	vergleiche("Unsinn") // ungültiger Wert wirkt wie aus (§4)
	// innen: Antworten unverändert bis auf die Werkzeugtexte (eigener Test);
	// /openapi.json und tools/list deshalb hier nicht.
	pfade = pfade[:len(pfade)-1]
	e.seite(t, api.BoardpaarSeiteInnen)
	for _, p := range pfade {
		s1, b1 := e.alt.rufe(t, e.alt.projekt, "", "GET", p, "")
		s2, b2 := e.neu.rufe(t, e.neu.projekt, "", "GET", p, "")
		if s1 != s2 || string(b1) != string(b2) {
			t.Errorf("innen %s: alt %d, neu %d\nalt %.300s\nneu %.300s", p, s1, s2, b1, b2)
		}
	}
	// Gegenkontrolle: außen trennt der Vergleich.
	e.seite(t, api.BoardpaarSeiteAussen)
	_, b1 := e.alt.rufe(t, e.alt.projekt, "", "GET", "/werkzeuge/boards/b1", "")
	_, b2 := e.neu.rufe(t, e.neu.projekt, "", "GET", "/werkzeuge/boards/b1", "")
	if string(b1) == string(b2) {
		t.Fatal("außen: board_lesen gleich wie ohne Board-Paar — der Vergleich trennt nicht")
	}
}

// TestBoardpaar789_WerkzeugtexteJeSeite: E-3 in OpenAPI UND MCP.
func TestBoardpaar789_WerkzeugtexteJeSeite(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	texte := func() (openapi, openapiDeleg, liste, discover string) {
		t.Helper()
		_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
		openapi = string(b)
		_, b = x.rufe(t, x.token, "", "GET", "/openapi.json", "")
		openapiDeleg = string(b)
		_, m := x.mcpRufe(t, x.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
		liste = mcpText789(t, m)
		_, m = x.mcpRufe(t, x.projekt, "", `{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{`+meta753()+`}}`, nil)
		discover = mcpText789(t, m)
		return
	}
	// aus: kein Paar-Text, kein „intern“, und /openapi.json ist inhaltlich
	// (als JSON) gleich der eingecheckten Datei werkzeuge_openapi.json —
	// verglichen gegen die Datei, nicht gegen den Cache (Kritiker K-3,
	// Klasse 155), so streng wie TestAgent752_Abzug. Für die delegierende
	// Fassung gibt es keinen Abzug; dort prüft der Test nur „kein Paar-Text“.
	abzug, err := os.ReadFile("werkzeuge_openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var abzugJSON any
	if err := json.Unmarshal(abzug, &abzugJSON); err != nil {
		t.Fatal(err)
	}
	for _, seite := range []string{api.BoardpaarSeiteAus, "kaputt"} {
		e.seite(t, seite)
		o, od, l, d := texte()
		var oJSON any
		if err := json.Unmarshal([]byte(o), &oJSON); err != nil || !reflect.DeepEqual(oJSON, abzugJSON) {
			t.Errorf("%s: /openapi.json weicht vom eingecheckten Abzug werkzeuge_openapi.json ab", seite)
		}
		if !strings.Contains(d, "aBoard ist ein Kanban-Board") || !strings.Contains(l, "board_lesen") {
			t.Fatalf("%s: MCP-Antworten leer: %.200s / %.200s", seite, d, l)
		}
		for wo, s := range map[string]string{"openapi": o, "openapi delegiert": od, "tools/list": l, "discover": d} {
			if strings.Contains(strings.ToLower(s), "intern") || strings.Contains(s, "Board-Paar") || strings.Contains(s, "boardpaar") {
				t.Errorf("%s %s: Paar-Text auf einer Instanz ohne Paar", seite, wo)
			}
		}
	}
	// aussen
	e.seite(t, api.BoardpaarSeiteAussen)
	o, od, l, d := texte()
	for wo, s := range map[string]string{"openapi": o, "openapi delegiert": od, "tools/list": l, "discover": d} {
		if !strings.Contains(s, "Zu jedem Board gibt es ein internes Board") || !strings.Contains(s, "Neue Karten gehören nach innen") {
			t.Errorf("aussen %s: Paar-Text fehlt", wo)
		}
		if strings.Contains(s, paarTextInnen789) {
			t.Errorf("aussen %s: Text der Innenseite", wo)
		}
	}
	// Die Fassung je Token bleibt auch je Seite getrennt (Lehre #752): Das
	// delegierende Token sieht karten_suchen nicht (ADR-0139 §7).
	if !strings.Contains(o, `"operationId": "karten_suchen"`) || strings.Contains(od, `"operationId": "karten_suchen"`) {
		t.Error("aussen: Fassungen für delegierendes und Projekt-Token nicht getrennt")
	}
	if !strings.Contains(l, "karte darf auch i-") {
		t.Error("aussen tools/list: karte_zeigen ohne i-Hinweis")
	}
	// innen (T3: Werkzeugliste innen — neue Karten entstehen hier, intern)
	e.seite(t, api.BoardpaarSeiteInnen)
	o, od, l, d = texte()
	for wo, s := range map[string]string{"openapi": o, "openapi delegiert": od, "tools/list": l, "discover": d} {
		if !strings.Contains(s, paarTextInnen789) || !strings.Contains(s, "Neue Karten entstehen hier, also intern") {
			t.Errorf("innen %s: Paar-Text fehlt", wo)
		}
		if strings.Contains(s, "Zu jedem Board gibt es ein internes Board") {
			t.Errorf("innen %s: Text der Außenseite", wo)
		}
	}
	var spez struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	_ = json.Unmarshal([]byte(o), &spez)
	if _, ok := spez.Paths["/werkzeuge/boards/{boardId}/karten"]["post"]; !ok {
		t.Error("innen: karte_anlegen fehlt in der Werkzeugliste")
	}
	if !strings.Contains(l, "vertraulich, bis ein Mensch es freigibt") {
		t.Error("innen tools/list: karte_anlegen ohne W-4-Hinweis")
	}
}

// TestBoardpaar789_BoardpaarFeld: boards und board_lesen nennen die Rolle
// gekoppelter Boards — ohne Kennung oder Adresse des Gegenstücks.
func TestBoardpaar789_BoardpaarFeld(t *testing.T) {
	e := newEnv789(t, true)
	if err := e.bp.InnenKoppeln(context.Background(), "b2", "admin"); err != nil {
		t.Fatal(err)
	}
	x := e.neu
	e.seite(t, api.BoardpaarSeiteInnen)
	_, b := x.rufe(t, x.token, "anna@firma.de", "GET", "/werkzeuge/boards", "")
	var liste struct {
		Boards []map[string]any `json:"boards"`
	}
	_ = json.Unmarshal(b, &liste)
	gefunden := 0
	for _, bd := range liste.Boards {
		bp, _ := bd["boardpaar"].(map[string]any)
		switch bd["boardId"] {
		case "b2":
			gefunden++
			if bp["rolle"] != "intern" || bp["gegenstueck"] != "extern" || len(bp) != 3 {
				t.Errorf("innen b2: %v", bp)
			}
		case "b1": // außen gekoppelt, auf dieser (inneren) Seite ohne Bedeutung
			gefunden++
			if bp != nil {
				t.Errorf("innen b1: %v", bp)
			}
		}
	}
	if gefunden != 2 {
		t.Fatalf("boards: %s", b)
	}
	e.seite(t, api.BoardpaarSeiteAussen)
	_, b = x.rufe(t, x.token, "anna@firma.de", "GET", "/werkzeuge/boards", "")
	verboten789(t, "boards außen", strings.ReplaceAll(string(b), "anna@firma.de", ""), false)
	if !strings.Contains(string(b), `"rolle":"extern"`) {
		t.Errorf("außen boards ohne boardpaar: %s", b)
	}
}

// --- W-4 und T3 -----------------------------------------------------------------

func (e *env789) karteVon(t *testing.T, board, titel string) map[string]any {
	t.Helper()
	var stand struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
			Termine []map[string]any `json:"termine"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(e.neu.stand(t, board)), &stand); err != nil {
		t.Fatal(err)
	}
	for _, s := range stand.Board.Sprints {
		for _, k := range s.Cards {
			if k["title"] == titel {
				return k
			}
		}
	}
	for _, k := range stand.Board.Termine {
		if k["titel"] == titel || k["title"] == titel {
			return k
		}
	}
	t.Fatalf("Eintrag %q fehlt auf %s", titel, board)
	return nil
}

func (e *env789) mensch789(t *testing.T, methode, pfad, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(methode, e.mensch.URL+pfad, strings.NewReader(body))
	req.Header.Set("Remote-Email", "anna@firma.de")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func neueKarte789(id, titel, extra string) string {
	return `{"type":"card.upsert","payload":{"sprintId":"__backlog__","columnId":"col-backlog","card":{"id":"` + id +
		`","title":"` + titel + `","columnId":"col-backlog","description":"","labels":[],"assignees":[],"links":[],"attachments":[]` + extra + `}}}`
}

func neuerTermin789(id, titel, extra string) string {
	return `{"type":"termin.upsert","payload":{"termin":{"id":"` + id + `","titel":"` + titel + `","datum":"2026-10-20"` + extra + `}}}`
}

// TestBoardpaar789_W4AgentenAnlagenInnenVertraulich: jeder Agentenweg, der
// anlegt — roh, Werkzeug, MCP —, und beide Arten (Karte, Termin).
func TestBoardpaar789_W4AgentenAnlagenInnenVertraulich(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	roh := func(ops string) {
		t.Helper()
		if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+ops+`]}`); s != http.StatusOK {
			t.Fatalf("ops: %d %s", s, b)
		}
	}
	vertraulich := func(titel string) any {
		t.Helper()
		v, ok := e.karteVon(t, "b1", titel)["confidential"]
		if !ok {
			return "<fehlt>"
		}
		return v
	}
	// rohe Ops: ohne Feld, mit ausdrücklichem false, mit "" und null.
	roh(neueKarte789("w4-a", "W4 roh ohne", ""))
	roh(neueKarte789("w4-b", "W4 roh false", `,"confidential":false`))
	roh(neueKarte789("w4-c", "W4 roh leer", `,"confidential":""`))
	roh(neueKarte789("w4-d", "W4 roh null", `,"confidential":null`))
	roh(neuerTermin789("w4-t1", "W4 Termin ohne", ""))
	roh(neuerTermin789("w4-t2", "W4 Termin false", `,"confidential":false`))
	// Werkzeuge (OpenAPI) und MCP.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"W4 Werkzeug"}`); s != http.StatusOK {
		t.Fatalf("karte_anlegen: %d %s", s, b)
	}
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/findings", `{"titel":"W4 Finding","typ":"K","prioritaet":"P2"}`); s != http.StatusOK {
		t.Fatalf("finding_anlegen: %d %s", s, b)
	}
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"W4 Werkzeug-Termin","datum":"2026-10-21"}`); s != http.StatusOK {
		t.Fatalf("termin_anlegen: %d %s", s, b)
	}
	if s, m := x.mcpWerkzeug(t, x.projekt, "", "karte_anlegen", `{"boardId":"b1","titel":"W4 MCP"}`); s != http.StatusOK {
		t.Fatalf("MCP karte_anlegen: %d %v", s, m)
	} else if fehler, text, _ := ergebnis753(t, m); fehler {
		t.Fatalf("MCP karte_anlegen: %s", text)
	}
	// Delegierend (Open WebUI).
	if s, b := x.rufe(t, x.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"W4 Assistent"}`); s != http.StatusOK {
		t.Fatalf("Assistent karte_anlegen: %d %s", s, b)
	}
	for _, titel := range []string{"W4 roh ohne", "W4 roh false", "W4 roh leer", "W4 roh null", "W4 Termin ohne", "W4 Termin false",
		"W4 Werkzeug", "W4 Finding", "W4 Werkzeug-Termin", "W4 MCP", "W4 Assistent"} {
		if v := vertraulich(titel); v != true {
			t.Errorf("%s: confidential = %v, erwartet true", titel, v)
		}
	}
	// T3: Die Karte entsteht auf DIESEM (internen) Board, im Backlog.
	if k := e.karteVon(t, "b1", "W4 Werkzeug"); k["columnId"] != "col-backlog" {
		t.Errorf("T3: Karte nicht im Backlog: %v", k["columnId"])
	}
	// Nur bei Neuanlage: Ein Agent, der eine bestehende, nicht vertrauliche
	// Karte bearbeitet, schaltet nichts ein.
	roh(`{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Offene Karte geändert"}}`)
	if v := vertraulich("Offene Karte geändert"); v != "<fehlt>" {
		t.Errorf("Bestandskarte nach Agenten-Bearbeitung: %v", v)
	}
	// Freigeben darf nur ein Mensch (Sperre aus #778): Agent → 422.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops",
		`{"ops":[{"type":"card.field","payload":{"cardId":"w4-a","field":"confidential","value":false}}]}`); s != http.StatusUnprocessableEntity {
		t.Errorf("Agent gibt frei: %d %s", s, b)
	}
	// Ganzersatz durch einen Menschen ohne das Feld verliert es nicht (Klasse 194).
	k := e.karteVon(t, "b1", "W4 roh ohne")
	delete(k, "confidential")
	k["title"] = "W4 roh ohne ersetzt"
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[{"type":"card.upsert","payload":{"card":`+jsonText(k)+`}}]}`); s != http.StatusOK {
		t.Fatalf("Mensch-Ganzersatz: %d %s", s, b)
	}
	if v := vertraulich("W4 roh ohne ersetzt"); v != true {
		t.Errorf("nach Mensch-Ganzersatz: %v", v)
	}
	// Ein Mensch gibt frei.
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"w4-a","field":"confidential","value":false}}]}`); s != http.StatusOK {
		t.Fatalf("Mensch gibt frei: %d %s", s, b)
	}
	if v := vertraulich("W4 roh ohne ersetzt"); v != false {
		t.Errorf("nach Freigabe: %v", v)
	}
	// Ein Mensch legt an → nicht vertraulich.
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("m-1", "Mensch legt an", "")+`,`+neuerTermin789("m-t", "Mensch Termin", "")+`]}`); s != http.StatusOK {
		t.Fatalf("Mensch legt an: %d %s", s, b)
	}
	for _, titel := range []string{"Mensch legt an", "Mensch Termin"} {
		if v := vertraulich(titel); v != "<fehlt>" {
			t.Errorf("%s: %v", titel, v)
		}
	}
	// Nur bei Neuanlage: Ein Agent ersetzt die Karte eines Menschen ganz
	// (card.upsert auf Bestand) → bleibt offen.
	k = e.karteVon(t, "b1", "Mensch legt an")
	k["title"] = "Mensch legt an, Agent ersetzt"
	roh(`{"type":"card.upsert","payload":{"card":` + jsonText(k) + `}}`)
	if v := vertraulich("Mensch legt an, Agent ersetzt"); v != "<fehlt>" {
		t.Errorf("Bestandskarte nach Agenten-Ganzersatz: %v", v)
	}
	// Nur bei Neuanlage, auch für den Ganzersatz eines Termins: Ein Agent
	// ändert den Termin eines Menschen per termin.upsert → bleibt offen.
	roh(neuerTermin789("m-t", "Mensch Termin vom Agenten geändert", ""))
	if v := vertraulich("Mensch Termin vom Agenten geändert"); v != "<fehlt>" {
		t.Errorf("Bestandstermin nach Agenten-Upsert: %v", v)
	}
}

// TestBoardpaar789_W4NurInnen: auf aus und außen legt ein Agent an wie
// bisher (kein Feld), auf innen vertraulich — dieselbe Nutzlast.
func TestBoardpaar789_W4NurInnen(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	for i, seite := range []string{api.BoardpaarSeiteAus, api.BoardpaarSeiteAussen, "kaputt", api.BoardpaarSeiteInnen} {
		e.seite(t, seite)
		titel := "Seite " + seite
		ops := neueKarte789(fmt.Sprintf("s-%d", i), titel, "") + "," + neuerTermin789(fmt.Sprintf("st-%d", i), "Termin "+seite, "")
		if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+ops+`]}`); s != http.StatusOK {
			t.Fatalf("%s: %d %s", seite, s, b)
		}
		for _, tt := range []string{titel, "Termin " + seite} {
			v, ok := e.karteVon(t, "b1", tt)["confidential"]
			if seite == api.BoardpaarSeiteInnen {
				if v != true {
					t.Errorf("%s: %v", tt, v)
				}
			} else if ok {
				t.Errorf("%s: confidential gesetzt (%v), erwartet fehlend", tt, v)
			}
		}
	}
}

// TestBoardpaar789_W4ImSelbenBatch (Kritiker K-1): Wer im SELBEN Batch neu
// anlegt und danach abschaltet — zweiter Upsert mit false, card.field false,
// zweiter termin.upsert mit false —, bekommt trotzdem vertraulich. Löschen und
// Neuanlage unter derselben id im Batch ebenso. Ein späterer Agenten-Batch,
// der abschaltet, scheitert an der Sperre aus #778
// (_W4AgentenAnlagenInnenVertraulich, „Agent gibt frei“).
func TestBoardpaar789_W4ImSelbenBatch(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	for name, f := range map[string]struct{ ops, titel string }{
		"Karte Upsert-false": {neueKarte789("kb-a", "KB Upsert", "") + "," + neueKarte789("kb-a", "KB Upsert", `,"confidential":false`), "KB Upsert"},
		"Karte field-false": {neueKarte789("kb-b", "KB Feld", "") +
			`,{"type":"card.field","payload":{"cardId":"kb-b","field":"confidential","value":false}}`, "KB Feld"},
		"Termin Upsert-false": {neuerTermin789("kb-t", "KB Termin", "") + "," + neuerTermin789("kb-t", "KB Termin", `,"confidential":false`), "KB Termin"},
		"Löschen und neu":     {`{"type":"card.delete","payload":{"cardId":"k1"}},` + neueKarte789("k1", "KB Neu", `,"confidential":false`), "KB Neu"},
	} {
		if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+f.ops+`]}`); s != http.StatusOK {
			t.Fatalf("%s: %d %s", name, s, b)
		}
		if v := e.karteVon(t, "b1", f.titel)["confidential"]; v != true {
			t.Errorf("%s: confidential = %v, erwartet true", name, v)
		}
	}
}

// TestBoardpaar789_SeiteNichtLesbar (Kritiker K-2): Ist server_config nicht
// lesbar (Lesefehler, nicht fehlender Schlüssel), lehnt der Ops-Weg einen
// Agenten-Batch mit 503 ab und schreibt nichts — auch über ein Werkzeug.
// Menschen schreiben weiter; Lesewerkzeuge gelten als „aus“ (keine Schatten).
func TestBoardpaar789_SeiteNichtLesbar(t *testing.T) {
	e := newEnv789(t, false)
	kaputtDB, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = kaputtDB.Close() // jede Abfrage scheitert: „database is closed“
	kaputt := storage.NewConfigRepo(kaputtDB, nil, func() string { return "x" })
	x, mensch := e.baueMit(kaputt)
	vorher := x.stand(t, "b1")
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("ks-1", "KS roh", "")+`]}`); s != http.StatusServiceUnavailable {
		t.Errorf("roh: %d %s", s, b)
	}
	if s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"KS Werkzeug"}`); s != http.StatusServiceUnavailable {
		t.Errorf("karte_anlegen: %d %s", s, b)
	}
	if x.stand(t, "b1") != vorher {
		t.Error("Board trotz 503 geändert")
	}
	if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1", ""); s != http.StatusOK || strings.Contains(string(b), `"intern":`) {
		t.Errorf("board_lesen bei unlesbarer Seite: %d %.300s", s, b)
	}
	req, _ := http.NewRequest("POST", mensch.URL+"/boards/b1/ops", strings.NewReader(`{"ops":[`+neueKarte789("ks-m", "KS Mensch", "")+`]}`))
	req.Header.Set("Remote-Email", "anna@firma.de")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Mensch bei unlesbarer Seite: %d", resp.StatusCode)
	}
}

// TestBoardpaar789_SucheEigenesBudget (Kritiker K-4): Viele Schatten
// verdrängen in karten_suchen keine externen Treffer; die Kürzung der
// internen meldet internGekuerzt mit eigenem Hinweis.
func TestBoardpaar789_SucheEigenesBudget(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	var menge []schatten.Eingang
	for i := 1; i <= 150; i++ {
		titel, nr := fmt.Sprintf("Karte Schatten %03d mit einem recht langen Titel zum Füllen", i), 100+i
		menge = append(menge, schatten.Eingang{SchattenID: "S" + fmt.Sprintf("%042d", i), Art: schatten.ArtKarte, Nr: &nr, Titel: &titel})
	}
	if _, err := e.sr.Ersetze(context.Background(), "b1", menge); err != nil {
		t.Fatal(err)
	}
	x := e.neu
	s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/karten?q=Karte", "")
	var m struct {
		Treffer        []map[string]any `json:"treffer"`
		Gekuerzt       bool             `json:"gekuerzt"`
		Intern         []map[string]any `json:"intern"`
		InternAnzahl   int              `json:"internAnzahl"`
		InternGekuerzt bool             `json:"internGekuerzt"`
		InternHinweis  string           `json:"internHinweis"`
	}
	if s != http.StatusOK || json.Unmarshal(b, &m) != nil {
		t.Fatalf("%d %.300s", s, b)
	}
	offen := false
	for _, tr := range m.Treffer {
		offen = offen || tr["titel"] == "Offene Karte"
	}
	if !offen || m.Gekuerzt {
		t.Errorf("externe Treffer verdrängt: offen=%v gekuerzt=%v", offen, m.Gekuerzt)
	}
	if !m.InternGekuerzt || m.InternAnzahl != 150 || len(m.Intern) >= 150 || len(m.Intern) == 0 || m.InternHinweis == "" {
		t.Errorf("interne Kürzung: gekuerzt=%v anzahl=%d gezeigt=%d hinweis=%q", m.InternGekuerzt, m.InternAnzahl, len(m.Intern), m.InternHinweis)
	}
}

// TestBoardpaar789_InternFeldImmerAussen (Kritiker K-5): Außen trägt
// board_lesen ohne swimlane-Filter immer `intern` — auch leer; mit
// swimlane-Filter fehlt das Feld.
func TestBoardpaar789_InternFeldImmerAussen(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	x := e.neu
	if err := x.acl.Set(context.Background(), "b2", "anna@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	// b2 ist nicht gekoppelt, hat also keine Schatten — Abruf über den
	// delegierenden Zugang der Person (das Projekt-Token gilt nur für b1).
	if s, b := x.rufe(t, x.token, "anna@firma.de", "GET", "/werkzeuge/boards/b2", ""); s != http.StatusOK || !strings.Contains(string(b), `"intern":[]`) {
		t.Errorf("b2 ohne Schatten: %d %.400s", s, b)
	}
	if s, b := x.rufe(t, x.projekt, "", "GET", "/werkzeuge/boards/b1?swimlane=Backlog", ""); s != http.StatusOK || strings.Contains(string(b), `"intern":`) {
		t.Errorf("mit swimlane: %d %.400s", s, b)
	}
	if _, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", ""); !strings.Contains(string(b), "Ohne swimlane steht intern immer in der Antwort") {
		t.Error("Beschreibung von board_lesen nennt die Regel nicht")
	}
}

// TestBoardpaar789_W4KeinZwischenstand (Verify V-2): Die Brücke setzt
// vertraulich NACH JEDER Op, nicht erst am Ende des Batches. Belegt über
// card.fieldIf im selben Batch, das die frisch angelegte Karte als „nicht
// vertraulich“ (Feld fehlt = null) erwartet: Der Fold sieht schon true, meldet
// feld-geaendert, und der Server lehnt den ganzen Batch ab (422, nichts
// geschrieben). Eine Fassung, die erst am Ende setzt, ließe das fieldIf
// wirken und den Batch durch.
func TestBoardpaar789_W4KeinZwischenstand(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	vorher := x.stand(t, "b1")
	for _, erwartet := range []string{`null`, `false`} {
		s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("kz-1", "KZ Zwischenstand", "")+
			`,{"type":"card.fieldIf","payload":{"cardId":"kz-1","field":"confidential","value":false,"expect":`+erwartet+`}}]}`)
		if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "feld-geaendert") {
			t.Errorf("expect %s: %d %s", erwartet, s, b)
		}
		if x.stand(t, "b1") != vorher {
			t.Errorf("expect %s: Batch trotz Ablehnung geschrieben", erwartet)
		}
	}
	// Gegenkontrolle: Mit expect true passt das fieldIf (der Wert ist zu dem
	// Zeitpunkt schon true) und schaltet ab; nach der Op setzt die Brücke
	// wieder true. Batch 200, Karte vertraulich — die Ablehnung oben kommt
	// also vom Vergleich mit dem Zwischenstand, nicht vom fieldIf an sich.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("kz-2", "KZ Gegenkontrolle", "")+
		`,{"type":"card.fieldIf","payload":{"cardId":"kz-2","field":"confidential","value":false,"expect":true}}]}`); s != http.StatusOK {
		t.Fatalf("Gegenkontrolle expect true: %d %s", s, b)
	}
	if v := e.karteVon(t, "b1", "KZ Gegenkontrolle")["confidential"]; v != true {
		t.Errorf("Gegenkontrolle: confidential = %v", v)
	}
}
