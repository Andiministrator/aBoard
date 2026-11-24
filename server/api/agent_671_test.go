package api_test

// Karte #671 (ADR-0133 §5/§7): Werkzeug-Endpunkte für Open WebUI — über die
// echte Kette (Agenten-Zugang → Mux → Werkzeug → postOps → Fold → Protokoll).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

const basis671 = "https://board.example"

type env671 struct {
	agent    *httptest.Server
	mux      *http.ServeMux
	boards   *storage.BoardRepo
	acl      *storage.AclRepo
	db       *sql.DB
	repo     *storage.AgentTokenRepo
	token    string                   // delegierend
	projekt  string                   // Projekt-Token editor auf b1
	leser    string                   // Projekt-Token viewer auf b1
	vorschlg string                   // Vorschlags-Token auf b1
	cfg      *storage.ConfigRepo      // Instanz-Zeitzone (ADR-0138 §2, Verify 07.10. b, V-3)
	anhaenge *storage.AttachmentStore // anhang_lesen (Karte #755)
}

// board671 baut ein Board mit Standardspalten, allgemeinem Backlog, einer
// sichtbaren und einer archivierten Swimlane sowie Karten jeder Sorte.
func board671(id, titel string) string {
	return `{"version":"2","board":{"id":"` + id + `","title":"` + titel + `","nextDisplayId":10,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"},{"id":"col-in-arbeit","title":"In Arbeit"},{"id":"col-erledigt","title":"Erledigt"}],
	  "sprints":[
	    {"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[
	      {"id":"k1","displayId":1,"title":"Offene Karte","columnId":"col-backlog","owner":"Anna","dueDate":"2026-10-10","comments":[]},
	      {"id":"k2","displayId":2,"title":"Erledigte Karte","columnId":"col-erledigt","comments":[]},
	      {"id":"k3","displayId":3,"title":"Archivierte Karte","columnId":"col-ready","archived":true,"comments":[]}]},
	    {"id":"s1","title":"Sprint 1","kind":"sprint","cards":[
	      {"id":"m1","displayId":4,"title":"📋 Sprint-Start","kind":"sprint-meta","metaType":"start","columnId":"col-backlog","comments":[]},
	      {"id":"k5","displayId":5,"title":"Im Sprint","columnId":"col-in-arbeit","comments":[]},
	      {"id":"f6","displayId":6,"title":"Ein Befund","kind":"finding","finding":{"nr":3,"typ":"K","prioritaet":"P2","status":"offen"},"columnId":"col-ready","comments":[]}]},
	    {"id":"alt","title":"Altlasten","kind":"lane","archived":true,"cards":[
	      {"id":"k7","displayId":7,"title":"Versteckt","columnId":"col-ready","comments":[]}]}]}}`
}

func newEnv671(t *testing.T, mitBasis bool) *env671 {
	t.Helper()
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	datei := filepath.Join(t.TempDir(), "owui-jwt-secret")
	if err := os.WriteFile(datei, geheimnis669, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	for id, titel := range map[string]string{"b1": "Marketing", "b2": "Technik", "b3": "Geheim"} {
		if _, err := boards.Insert(ctx, id, titel, "2", []byte(board671(id, titel))); err != nil {
			t.Fatal(err)
		}
	}
	// Das Service-Board legt storage.Open schon an (seed_service.go).
	if _, err := boards.Insert(ctx, storage.ServiceBoardID, "Service", "2", []byte(board671(storage.ServiceBoardID, "Service"))); err != nil && !strings.Contains(err.Error(), "exist") {
		t.Logf("service-board: %v", err)
	}
	for _, e := range [][3]string{
		{"b1", "anna@firma.de", "editor"},
		{"b2", "anna@firma.de", "owner"},
		{"b1", "cora@extern.de", "viewer"},
		{"b1", "@firma.de", "editor"},
		{storage.ServiceBoardID, "anna@firma.de", "owner"},
	} {
		if err := acl.Set(ctx, e[0], e[1], e[2]); err != nil {
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
	e := &env671{boards: boards, acl: acl, db: db, repo: repo,
		token:    anlegen(storage.AgentTokenAnlage{Name: "open-webui", Role: storage.AgentRolleEditor, CreatedBy: "cli", Delegierend: true}),
		projekt:  anlegen(storage.AgentTokenAnlage{Name: "projekt", BoardID: "b1", Role: storage.AgentRolleEditor, CreatedBy: "cli"}),
		leser:    anlegen(storage.AgentTokenAnlage{Name: "leser", BoardID: "b1", Role: storage.AgentRolleViewer, CreatedBy: "cli"}),
		vorschlg: anlegen(storage.AgentTokenAnlage{Name: "recherche", BoardID: "b1", Role: storage.AgentRolleVorschlag, CreatedBy: "cli"}),
	}
	mux := http.NewServeMux()
	n := 0
	e.cfg = storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("audit-%d", n) })
	// Karte #755: Namen (mitglieder, Verlauf) und Anhang-Inhalte (anhang_lesen).
	e.anhaenge = storage.NewAttachmentStore(t.TempDir())
	bapi := api.NewBoardsAPI(boards).MitKalenderKonfig(e.cfg).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).WithFold(testMotor).
		WithUsers(storage.NewUserRepo(db)).WithAttachments(e.anhaenge)
	if mitBasis {
		bapi.WithOeffentlicheBasis(basis671 + "/")
	}
	bapi.Register(mux)
	bapi.RegisterWerkzeuge(mux)
	e.mux = mux
	t.Setenv(api.OWUIJWTSecretFileEnv, datei)
	zugang := api.NewAgentZugang(repo)
	if err := zugang.MitOWUIDelegation(acl); err != nil {
		t.Fatal(err)
	}
	e.agent = httptest.NewServer(zugang.Middleware(mux))
	t.Cleanup(e.agent.Close)
	return e
}

// rufe: Anfrage mit Token; email != "" setzt ein frisches OWUI-JWT.
func (e *env671) rufe(t *testing.T, token, email, methode, pfad, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(methode, e.agent.URL+pfad, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if email != "" {
		req.Header.Set("X-OpenWebUI-User-Jwt", frisch669(t, email))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func json671(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("kein JSON-Objekt: %s", b)
	}
	return m
}

func (e *env671) stand(t *testing.T, id string) string {
	t.Helper()
	b, err := e.boards.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return string(b.StateJSON)
}

// --- Spezifikation = erlaubte Wege ------------------------------------------

type spezOp struct {
	methode, pfad, opID string
	op                  map[string]any
}

func spezOps(t *testing.T) ([]spezOp, map[string]any) {
	t.Helper()
	var spez map[string]any
	if err := json.Unmarshal(api.WerkzeugSpezifikation(), &spez); err != nil {
		t.Fatalf("Spezifikation ist kein JSON: %v", err)
	}
	var ops []spezOp
	pfade, _ := spez["paths"].(map[string]any)
	for pfad, v := range pfade {
		for methode, o := range v.(map[string]any) {
			op, _ := o.(map[string]any)
			id, _ := op["operationId"].(string)
			ops = append(ops, spezOp{strings.ToUpper(methode), pfad, id, op})
		}
	}
	return ops, spez
}

// Drei unabhängige Quellen müssen übereinstimmen: die von Hand geschriebene
// Spezifikation, die registrierten Routen und die Positivliste (geprüft über
// echte Anfragen). Dazu die Zusage an das Partnerprojekt (operationIds) als vierte.
func TestAgent671_SpezifikationGleichErlaubteWege(t *testing.T) {
	e := newEnv671(t, true)
	ops, spez := spezOps(t)
	if v, _ := spez["openapi"].(string); !strings.HasPrefix(v, "3.1") {
		t.Fatalf("openapi-Version %q, zugesagt 3.1", v)
	}

	// 1. Spezifikation ↔ Zusage (operationIds, Stufe 1 und 2).
	var ids []string
	for _, o := range ops {
		ids = append(ids, o.opID)
	}
	sort.Strings(ids)
	// Karte #755: die Fassung für Projekt-/Personen-Tokens enthält auch die
	// Werkzeuge aus Stufe 4a und (Karte #757) 4b; was Open WebUI sieht, prüft
	// agent_752_test.go.
	if got, want := strings.Join(ids, ","), "anhang_lesen,anhang_loeschen,anhang_loeschen_pruefen,archivieren,auf_antwort_warten,board_aendern,"+
		"board_lesen,board_verlauf,boards,checkliste_abhaken,finding_anlegen,freigeben,karte_aendern,karte_anlegen,karte_loeschen,"+
		"karte_loeschen_pruefen,karte_zeigen,karten_suchen,kommentar_bearbeiten,kommentieren,mitglieder,spalte_loeschen,spalte_loeschen_pruefen,"+
		"swimlane_aendern,swimlane_anlegen,swimlane_loeschen,swimlane_loeschen_pruefen,termin_aendern,termin_anlegen,termin_loeschen,"+
		"termin_loeschen_pruefen,termine_lesen,uebernehmen,verschieben,verweis_entfernen,verweis_setzen,wiederherstellen"; got != want {
		t.Fatalf("operationIds der Spezifikation: %s, zugesagt: %s", got, want)
	}

	// 2. Spezifikation ↔ registrierte Routen (ohne /openapi.json selbst).
	spezSatz := map[string]string{}
	for _, o := range ops {
		spezSatz[o.methode+" "+o.pfad] = o.opID
	}
	regSatz := map[string]string{}
	for _, r := range api.WerkzeugRouten() {
		if r.Muster == "/openapi.json" {
			continue
		}
		regSatz[r.Methode+" "+r.Muster] = r.OperationID
	}
	if fmt.Sprint(spezSatz) != fmt.Sprint(regSatz) {
		t.Fatalf("Spezifikation %v ≠ registriert %v", spezSatz, regSatz)
	}

	// 3. Jede Operation ist für ein Modell beschrieben, Parameter ohne $ref
	// (Open WebUI v0.11.4 löst $ref nur im requestBody auf).
	for _, o := range ops {
		for _, f := range []string{"summary", "description"} {
			if s, _ := o.op[f].(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s: %s fehlt", o.opID, f)
			}
		}
		params, _ := o.op["parameters"].([]any)
		for _, p := range params {
			pm := p.(map[string]any)
			if _, ref := pm["$ref"]; ref {
				t.Errorf("%s: Parameter als $ref — Open WebUI liest dort nur name/schema", o.opID)
			}
			if s, _ := pm["description"].(string); strings.TrimSpace(s) == "" {
				t.Errorf("%s: Parameter %v ohne description", o.opID, pm["name"])
			}
		}
	}

	// 4. Jeder beschriebene Weg ist erreichbar UND erlaubt: echte Anfrage mit
	// delegierendem Token (Person mit Schreibrecht) → 200.
	koerper := map[string]string{
		"karte_anlegen":   `{"titel":"Aus dem Spezifikationstest"}`,
		"kommentieren":    `{"text":"Spezifikationstest"}`,
		"verschieben":     `{"spalte":"Ready"}`,
		"finding_anlegen": `{"titel":"Befund aus dem Spezifikationstest","typ":"K","prioritaet":"P2"}`,
		"termin_anlegen":  `{"titel":"Termin aus dem Spezifikationstest","datum":"2026-10-08","von":"14:00","bis":"15:00"}`,
	}
	pfadFuer := func(p string) string { return pfad755(p, "b1", "projekt") }
	// Karte #755: Werkzeuge ohne Freigabe für Open WebUI mit dem Projekt-Token,
	// auf_antwort_warten mit einem Personen-Token (Projekt-Tokens haben keine
	// Person); jeder Aufruf vom selben Ausgangsstand (board755).
	fuerOWUI := map[string]bool{}
	for _, z := range zusage752 {
		fuerOWUI[z.name] = z.delegiert
	}
	stand := e.stand755(t)
	for name, k := range koerper755 {
		koerper[name] = strings.ReplaceAll(k, "STAND", stand)
	}
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	personToken, _, err := e.repo.Anlegen(t.Context(), storage.AgentTokenAnlage{Name: "anna-b1", BoardID: "b1", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	// Karte #757: Board-Struktur (Spalten, board_aendern) nur für eine Person,
	// die owner ist — Anna auf b2.
	ownerToken, _, err := e.repo.Anlegen(t.Context(), storage.AgentTokenAnlage{Name: "anna-b2", BoardID: "b2", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	abfrage := map[string]string{"karten_suchen": "?q=Karte"}
	for _, o := range ops {
		token, person := e.token, "anna@firma.de"
		if !fuerOWUI[o.opID] {
			token, person = e.projekt, ""
		}
		if o.opID == "auf_antwort_warten" {
			token = personToken
		}
		board, agent := "b1", "projekt"
		if nurMitPerson757[o.opID] {
			token, board, agent = ownerToken, "b2", "anna-b2"
		}
		e.zuruecksetzen755(t)
		body := e.mitCode757(t, token, person, o.pfad, board, agent, koerper[o.opID])
		if s, b := e.rufe(t, token, person, o.methode, pfad755(o.pfad, board, agent)+abfrage[o.opID], body); s != http.StatusOK {
			t.Errorf("%s %s: %d %s", o.methode, o.pfad, s, b)
		}
	}

	// 5. Nichts darüber hinaus: jede andere Methode auf diesen Pfaden und
	// Pfade daneben werden abgelehnt (403 Positivliste oder 405 Mux).
	alle := []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	pfade := map[string]bool{"/openapi.json": true}
	for _, o := range ops {
		pfade[o.pfad] = true
	}
	for pfad := range pfade {
		for _, m := range alle {
			if pfad == "/openapi.json" && m == "GET" {
				continue
			}
			if _, beschrieben := spezSatz[m+" "+pfad]; beschrieben {
				continue
			}
			if s, b := e.rufe(t, e.token, "anna@firma.de", m, pfadFuer(pfad), "{}"); s < 400 {
				t.Errorf("%s %s nicht beschrieben, aber %d %s", m, pfad, s, b)
			}
		}
	}
	for _, pfad := range []string{"/werkzeuge", "/werkzeuge/boards/b1/karten/1/kommentare/x", "/werkzeuge/boards/b1/karten/1/x",
		"/werkzeuge/boards/b1/findings/1", "/werkzeuge/boards/b1/karten/1/verschieben/x", "/werkzeuge/x"} {
		for _, m := range []string{"GET", "POST"} {
			if s, _ := e.rufe(t, e.token, "anna@firma.de", m, pfad, "{}"); s != http.StatusForbidden {
				t.Errorf("%s %s: %d, erwartet 403 (nicht in der Positivliste)", m, pfad, s)
			}
		}
	}

	// 6. Die Spezifikation selbst: delegierendes Token OHNE JWT (Open WebUI
	// holt sie aus dem Backend, ohne Person) → genau die eingebettete Datei.
	s, b := e.rufe(t, e.token, "", "GET", "/openapi.json", "")
	if s != http.StatusOK || string(b) != string(api.WerkzeugSpezifikationDelegiert()) {
		t.Fatalf("openapi.json ohne JWT: %d", s)
	}
	// Ohne Token gibt es sie nicht.
	resp, err := http.Get(e.agent.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("openapi.json ohne Token: %d", resp.StatusCode)
	}
}

// --- boards -------------------------------------------------------------------

func boardsListe671(t *testing.T, b []byte) map[string]map[string]any {
	t.Helper()
	m := json671(t, b)
	aus := map[string]map[string]any{}
	for _, x := range m["boards"].([]any) {
		e := x.(map[string]any)
		aus[e["boardId"].(string)] = e
	}
	return aus
}

func TestAgent671_Boards(t *testing.T) {
	e := newEnv671(t, true)
	// Anna: b1 (eigener Eintrag editor), b2 (owner → editor), Service-Board nie.
	s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards", "")
	if s != http.StatusOK {
		t.Fatalf("boards: %d %s", s, b)
	}
	l := boardsListe671(t, b)
	if len(l) != 2 || l["b1"]["rolle"] != "editor" || l["b2"]["rolle"] != "editor" || l["b1"]["name"] != "Marketing" ||
		l["b1"]["darfSchreiben"] != true || l["b1"]["link"] != basis671+"/#board=b1" {
		t.Fatalf("anna: %s", b)
	}
	if json671(t, b)["person"] != "anna@firma.de" {
		t.Fatalf("person: %s", b)
	}
	// Bert nur über die Domain-Freigabe; Cora als viewer.
	_, b = e.rufe(t, e.token, "bert@firma.de", "GET", "/werkzeuge/boards", "")
	if l := boardsListe671(t, b); len(l) != 1 || l["b1"]["rolle"] != "editor" {
		t.Fatalf("bert: %s", b)
	}
	_, b = e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards", "")
	if l := boardsListe671(t, b); len(l) != 1 || l["b1"]["rolle"] != "viewer" || l["b1"]["darfSchreiben"] != false {
		t.Fatalf("cora: %s", b)
	}
	// Ohne Freigabe: leere Liste.
	_, b = e.rufe(t, e.token, "dora@anders.de", "GET", "/werkzeuge/boards", "")
	if l := boardsListe671(t, b); len(l) != 0 {
		t.Fatalf("dora: %s", b)
	}
	// Ohne JWT keine Person → 401 (nicht die Liste irgendwessen).
	if s, _ := e.rufe(t, e.token, "", "GET", "/werkzeuge/boards", ""); s != http.StatusUnauthorized {
		t.Fatalf("ohne JWT: %d", s)
	}
	// Projekt-Token: nur das eigene Board, mit seiner Rolle.
	_, b = e.rufe(t, e.leser, "", "GET", "/werkzeuge/boards", "")
	if l := boardsListe671(t, b); len(l) != 1 || l["b1"]["rolle"] != "viewer" {
		t.Fatalf("projekt-leser: %s", b)
	}
	// Vorschlags-Token: kein Werkzeug, auch keine Spezifikation.
	for _, pfad := range []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/openapi.json"} {
		if s, _ := e.rufe(t, e.vorschlg, "", "GET", pfad, ""); s != http.StatusForbidden {
			t.Fatalf("vorschlag %s: %d", pfad, s)
		}
	}
	if s, _ := e.rufe(t, e.vorschlg, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"x"}`); s != http.StatusForbidden {
		t.Fatalf("vorschlag karte_anlegen: %d", s)
	}
}

// --- board_lesen --------------------------------------------------------------

func kartenNummern(t *testing.T, b []byte) []int {
	t.Helper()
	var nr []int
	for _, x := range json671(t, b)["karten"].([]any) {
		nr = append(nr, int(x.(map[string]any)["displayId"].(float64)))
	}
	sort.Ints(nr)
	return nr
}

func TestAgent671_BoardLesen(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards/b1", "")
	if s != http.StatusOK {
		t.Fatalf("board_lesen: %d %s", s, b)
	}
	// Weg: erledigt (2), archiviert (3), Meta (4), archivierte Swimlane (7).
	if got := fmt.Sprint(kartenNummern(t, b)); got != "[1 5 6]" {
		t.Fatalf("offene Karten %s: %s", got, b)
	}
	m := json671(t, b)
	k1 := m["karten"].([]any)[0].(map[string]any)
	if k1["titel"] != "Offene Karte" || k1["spalte"] != "Backlog" || k1["swimlane"] != "Backlog" || k1["owner"] != "Anna" ||
		k1["faellig"] != "2026-10-10" || k1["link"] != basis671+"/#board=b1&card=1" {
		t.Fatalf("Kartenzeile: %v", k1)
	}
	if strings.Contains(string(b), "Altlasten") || strings.Contains(string(b), "Versteckt") || strings.Contains(string(b), "comments") {
		t.Fatalf("archivierte Swimlane oder Blob in der Antwort: %s", b)
	}
	if !strings.Contains(string(b), `"finding":"F-3"`) || m["gekuerzt"] != false {
		t.Fatalf("finding/gekuerzt: %s", b)
	}
	// Filter und Erledigte. Open WebUI kodiert Python-Wahrheitswerte als „True“.
	_, b = e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards/b1?mitErledigten=True", "")
	if got := fmt.Sprint(kartenNummern(t, b)); got != "[1 2 5 6]" {
		t.Fatalf("mitErledigten: %s", got)
	}
	_, b = e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards/b1?swimlane=sprint%201", "")
	if got := fmt.Sprint(kartenNummern(t, b)); got != "[5 6]" {
		t.Fatalf("swimlane: %s", got)
	}
	_, b = e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards/b1?spalte=In%20Arbeit", "")
	if got := fmt.Sprint(kartenNummern(t, b)); got != "[5]" {
		t.Fatalf("spalte: %s", got)
	}
	// Kein Recht, kein Board, Service-Board: 403 mit derselben Meldung.
	for _, pfad := range []string{"/werkzeuge/boards/b3", "/werkzeuge/boards/gibt-es-nicht", "/werkzeuge/boards/" + storage.ServiceBoardID} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "GET", pfad, "")
		if s != http.StatusForbidden || !strings.Contains(string(b), "nicht erlaubt") {
			t.Fatalf("%s: %d %s", pfad, s, b)
		}
	}
}

func TestAgent671_BoardLesenKuerzt(t *testing.T) {
	e := newEnv671(t, false)
	var karten []string
	for i := 1; i <= 600; i++ {
		karten = append(karten, fmt.Sprintf(`{"id":"g%d","displayId":%d,"title":"%s","columnId":"col-backlog"}`, i, i, strings.Repeat("x", 300)))
	}
	state := `{"version":"2","board":{"id":"gross","title":"Gross","nextDisplayId":601,"columnDefs":[{"id":"col-backlog","title":"Backlog"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[` + strings.Join(karten, ",") + `]}]}}`
	ctx := context.Background()
	if _, err := e.boards.Insert(ctx, "gross", "Gross", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	if err := e.acl.Set(ctx, "gross", "anna@firma.de", "viewer"); err != nil {
		t.Fatal(err)
	}
	s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/gross", "")
	if s != http.StatusOK {
		t.Fatalf("%d %s", s, b)
	}
	m := json671(t, b)
	n := len(m["karten"].([]any))
	if m["gekuerzt"] != true || m["anzahl"].(float64) != 600 || n == 0 || n >= 600 || !strings.Contains(m["hinweis"].(string), "eingrenzen") {
		t.Fatalf("gekuerzt=%v anzahl=%v gezeigt=%d hinweis=%v", m["gekuerzt"], m["anzahl"], n, m["hinweis"])
	}
	if len(b) > 32<<10 {
		t.Fatalf("Antwort %d Bytes — zu gross für ein lokales Modell", len(b))
	}
	if strings.Contains(string(b), strings.Repeat("x", 201)) {
		t.Fatal("Titel nicht gekürzt")
	}
	// Ohne Basis-URL: link null, kein aus der Anfrage geratener Host.
	if !strings.Contains(string(b), `"link":null`) || strings.Contains(string(b), "127.0.0.1") {
		t.Fatalf("link ohne Basis: %.300s", b)
	}
}

// --- karte_anlegen -------------------------------------------------------------

func TestAgent671_KarteAnlegen(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "Anna@Firma.de", "POST", "/werkzeuge/boards/b1/karten",
		`{"titel":"Messestand buchen","beschreibung":"Bis Freitag\n- Halle 3"}`)
	if s != http.StatusOK {
		t.Fatalf("karte_anlegen: %d %s", s, b)
	}
	m := json671(t, b)
	if m["displayId"].(float64) != 10 || m["swimlane"] != "Backlog" || m["spalte"] != "Backlog" || m["link"] != basis671+"/#board=b1&card=10" {
		t.Fatalf("Antwort: %s", b)
	}
	// Im Board: allgemeiner Backlog, Spalte Backlog, displayId vom Fold.
	var stand struct {
		Board struct {
			Sprints []struct {
				ID    string           `json:"id"`
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(e.stand(t, "b1")), &stand); err != nil {
		t.Fatal(err)
	}
	var neu map[string]any
	for _, sp := range stand.Board.Sprints {
		for _, c := range sp.Cards {
			if c["title"] == "Messestand buchen" {
				if sp.ID != "__backlog__" {
					t.Fatalf("Swimlane %s", sp.ID)
				}
				neu = c
			}
		}
	}
	if neu == nil || neu["columnId"] != "col-backlog" || neu["displayId"].(float64) != 10 || neu["description"] != "Bis Freitag\n- Halle 3" || neu["owner"] != "" {
		t.Fatalf("neue Karte: %v", neu)
	}
	// Protokoll: dieselbe Spur wie POST /ops — Dienst und Person.
	var fuer, akteur string
	if err := e.db.QueryRow(`SELECT on_behalf_of, actor FROM activities WHERE board_id = 'b1' AND op_type = 'card.upsert'`).Scan(&fuer, &akteur); err != nil {
		t.Fatal(err)
	}
	if fuer != "anna@firma.de" || akteur != "agent:open-webui" {
		t.Fatalf("Protokoll: %q %q", fuer, akteur)
	}

	// Andere Swimlane per Name; archivierte gibt es für das Werkzeug nicht.
	s, b = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"In den Sprint","swimlane":"Sprint 1"}`)
	if s != http.StatusOK || json671(t, b)["swimlane"] != "Sprint 1" {
		t.Fatalf("swimlane: %d %s", s, b)
	}
	if s, b = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"x","swimlane":"Altlasten"}`); s != http.StatusBadRequest {
		t.Fatalf("archivierte swimlane: %d %s", s, b)
	}
	// Unbekannte Felder werden nicht still verworfen.
	for _, body := range []string{`{"titel":"x","besitzer":"Chef"}`, `{"titel":""}`, `{"beschreibung":"ohne titel"}`, `[1]`, `{"titel":"a\u0007b"}`} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body); s != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, s, b)
		}
	}
	// Leserecht reicht nicht; fremdes Board auch nicht.
	if s, _ := e.rufe(t, e.token, "cora@extern.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"x"}`); s != http.StatusForbidden {
		t.Fatalf("cora: %d", s)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b3/karten", `{"titel":"x"}`); s != http.StatusForbidden {
		t.Fatalf("b3: %d", s)
	}
	// Projekt-Token editor: eigenes Board ja, fremdes nein; viewer nein.
	if s, b := e.rufe(t, e.projekt, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"vom Projekt"}`); s != http.StatusOK {
		t.Fatalf("projekt: %d %s", s, b)
	}
	if s, _ := e.rufe(t, e.projekt, "", "POST", "/werkzeuge/boards/b2/karten", `{"titel":"x"}`); s != http.StatusForbidden {
		t.Fatalf("projekt fremdes board: %d", s)
	}
	if s, _ := e.rufe(t, e.leser, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"x"}`); s != http.StatusForbidden {
		t.Fatalf("projekt-leser: %d", s)
	}
	if strings.Contains(e.stand(t, "b1"), `"title":"x"`) {
		t.Fatal("abgelehnte Anlage steht im Board")
	}
}

// --- kommentieren ------------------------------------------------------------

func TestAgent671_Kommentieren(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/5/kommentare", `{"text":"Erledige ich morgen."}`)
	if s != http.StatusOK {
		t.Fatalf("kommentieren: %d %s", s, b)
	}
	m := json671(t, b)
	if m["autor"] != "agent:open-webui" || m["fuer"] != "anna@firma.de" || m["displayId"].(float64) != 5 || m["link"] != basis671+"/#board=b1&card=5" {
		t.Fatalf("Antwort: %s", b)
	}
	st := e.stand(t, "b1")
	if !strings.Contains(st, `"text":"Erledige ich morgen."`) || !strings.Contains(st, `"author":"agent:open-webui"`) || !strings.Contains(st, `"onBehalfOf":"anna@firma.de"`) {
		t.Fatalf("Kommentar-Stempel: %s", st)
	}
	// Karte über „#1“ und über die id.
	for _, ref := range []string{"%231", "k1"} {
		if s, b := e.rufe(t, e.token, "bert@firma.de", "POST", "/werkzeuge/boards/b1/karten/"+ref+"/kommentare", `{"text":"über `+ref+`"}`); s != http.StatusOK {
			t.Fatalf("%s: %d %s", ref, s, b)
		}
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/99/kommentare", `{"text":"x"}`); s != http.StatusNotFound {
		t.Fatalf("unbekannte Karte: %d", s)
	}
	if s, _ := e.rufe(t, e.token, "cora@extern.de", "POST", "/werkzeuge/boards/b1/karten/5/kommentare", `{"text":"x"}`); s != http.StatusForbidden {
		t.Fatalf("cora: %d", s)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/5/kommentare", `{"text":"x","author":"chef@firma.de"}`); s != http.StatusBadRequest {
		t.Fatalf("author mitgeschickt: %d", s)
	}
}

// Auf dem Menschen-Eingang (ohne Agenten-Principal) gibt es die Werkzeuge nicht.
func TestAgent671_NichtFuerMenschen(t *testing.T) {
	e := newEnv671(t, true)
	for _, f := range [][2]string{{"GET", "/openapi.json"}, {"GET", "/werkzeuge/boards"}, {"GET", "/werkzeuge/boards/b1"},
		{"POST", "/werkzeuge/boards/b1/karten"}, {"POST", "/werkzeuge/boards/b1/karten/1/kommentare"},
		{"GET", "/werkzeuge/boards/b1/karten/1"}, {"POST", "/werkzeuge/boards/b1/karten/1/verschieben"}, {"POST", "/werkzeuge/boards/b1/findings"}} {
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, httptest.NewRequest(f[0], f[1], strings.NewReader(`{"titel":"x","text":"x"}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s ohne Agent: %d", f[0], f[1], rec.Code)
		}
	}
}

func TestAgent671_BasisURLPruefung(t *testing.T) {
	for roh, ok := range map[string]bool{
		"": true, "https://board.example": true, "https://board.example/pfad/": true,
		"ftp://x": false, "https://u:p@x": false, "https://x/?a=1": false, "https://x/#f": false, "https:// x": false, "/relativ": false,
	} {
		_, err := api.ValidiereOeffentlicheBasis(roh)
		if (err == nil) != ok {
			t.Errorf("%q: err=%v", roh, err)
		}
	}
}

// --- Stufe 2: karte_zeigen ------------------------------------------------------

func TestAgent671_KarteZeigen(t *testing.T) {
	e := newEnv671(t, true)
	// Fremdtext mit HTML und Skript in Titel, Beschreibung und Kommentar.
	boese := `{"version":"2","board":{"id":"b9","title":"<b>Board</b>","nextDisplayId":2,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[
	    {"id":"x1","displayId":1,"title":"<script>alert(1)</script>","description":"</div><img src=x onerror=alert(2)>\n**fett**",
	     "columnId":"col-backlog","labels":["<i>l</i>"],"assignees":["anna@firma.de"],
	     "comments":[{"id":"c1","author":"<svg onload=alert(3)>","onBehalfOf":"anna@firma.de","created":"2026-10-04","text":"<a href=javascript:alert(4)>klick</a>"}]}]}]}}`
	ctx := context.Background()
	if _, err := e.boards.Insert(ctx, "b9", "Böse", "2", []byte(boese)); err != nil {
		t.Fatal(err)
	}
	if err := e.acl.Set(ctx, "b9", "cora@extern.de", "viewer"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", e.agent.URL+"/werkzeuge/boards/b9/karten/1", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("X-OpenWebUI-User-Jwt", frisch669(t, "cora@extern.de"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("karte_zeigen: %d %s", resp.StatusCode, b)
	}
	// Open WebUI bettet nur ein bei inline + text/html.
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || resp.Header.Get("Content-Disposition") != "inline" ||
		!strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "Content-Disposition") {
		t.Fatalf("Kopfzeilen: %v", resp.Header)
	}
	html := string(b)
	for _, verboten := range []string{"<script", "<img", "<svg", "<a href=javascript", `href="javascript`, "<i>", "<b>"} {
		if strings.Contains(strings.ToLower(html), verboten) {
			t.Errorf("unmaskiert in der Ansicht: %s", verboten)
		}
	}
	for _, soll := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "**fett**", "anna@firma.de", "Content-Security-Policy",
		`content="default-src 'none'`, "&lt;img src=x onerror=alert(2)&gt;", "&lt;a href=javascript:alert(4)&gt;", `href="https://board.example/#board=b9&amp;card=1"`, `rel="noopener noreferrer"`} {
		if !strings.Contains(html, soll) {
			t.Errorf("fehlt in der Ansicht: %s", soll)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("CSP-Kopfzeile: %q", resp.Header.Get("Content-Security-Policy"))
	}
	// format=json: derselbe Inhalt als Daten fürs Modell.
	s, b := e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/boards/b9/karten/1?format=json", "")
	m := json671(t, b)
	if s != http.StatusOK || m["titel"] != "<script>alert(1)</script>" || !strings.Contains(m["beschreibung"].(string), "**fett**") ||
		len(m["kommentare"].([]any)) != 1 || m["link"] != basis671+"/#board=b9&card=1" {
		t.Fatalf("json: %d %s", s, b)
	}
	// Finding-Felder; unbekannte Karte; falsches Format; ohne Recht.
	_, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/6?format=json", "")
	if f, _ := json671(t, b)["finding"].(map[string]any); f == nil || f["nr"] != "F-3" || f["typ"] != "K" {
		t.Fatalf("finding: %s", b)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/99", ""); s != http.StatusNotFound {
		t.Fatalf("unbekannt: %d", s)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/1?format=xml", ""); s != http.StatusBadRequest {
		t.Fatalf("format: %d", s)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b9/karten/1", ""); s != http.StatusForbidden {
		t.Fatalf("anna ohne Recht auf b9: %d", s)
	}
}

// --- Stufe 2: verschieben -----------------------------------------------------

func TestAgent671_Verschieben(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/1/verschieben", `{"spalte":"in arbeit"}`)
	if s != http.StatusOK {
		t.Fatalf("verschieben: %d %s", s, b)
	}
	m := json671(t, b)
	if m["nachher"].(map[string]any)["spalte"] != "In Arbeit" || m["vorher"].(map[string]any)["spalte"] != "Backlog" || m["link"] != basis671+"/#board=b1&card=1" {
		t.Fatalf("Antwort: %s", b)
	}
	if !strings.Contains(e.stand(t, "b1"), `"id":"k1"`) {
		t.Fatal("k1 weg")
	}
	_, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	if json671(t, b)["spalte"] != "In Arbeit" {
		t.Fatalf("Stand nach verschieben: %s", b)
	}
	// Mit Swimlane-Wechsel.
	if s, b = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/1/verschieben", `{"spalte":"col-ready","swimlane":"Sprint 1"}`); s != http.StatusOK {
		t.Fatalf("mit swimlane: %d %s", s, b)
	}
	_, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	if m := json671(t, b); m["spalte"] != "Ready" || m["swimlane"] != "Sprint 1" {
		t.Fatalf("nach swimlane-wechsel: %s", b)
	}
	var fuer string
	if err := e.db.QueryRow(`SELECT on_behalf_of FROM activities WHERE board_id = 'b1' AND op_type = 'card.move' LIMIT 1`).Scan(&fuer); err != nil || fuer != "anna@firma.de" {
		t.Fatalf("Protokoll: %q %v", fuer, err)
	}
	vorher := e.stand(t, "b1")
	for name, f := range map[string][2]string{
		"meta":          {"4", `{"spalte":"Erledigt"}`},
		"archiviert":    {"3", `{"spalte":"Backlog"}`},
		"spalte fehlt":  {"5", `{}`},
		"spalte unbek.": {"5", `{"spalte":"Irgendwo"}`},
		"lane archiv.":  {"5", `{"spalte":"Ready","swimlane":"Altlasten"}`},
		"feld unbek.":   {"5", `{"spalte":"Ready","toColId":"col-ready"}`},
	} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/"+f[0]+"/verschieben", f[1]); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, s, b)
		}
	}
	if s, _ := e.rufe(t, e.token, "cora@extern.de", "POST", "/werkzeuge/boards/b1/karten/5/verschieben", `{"spalte":"Ready"}`); s != http.StatusForbidden {
		t.Fatalf("cora: %d", s)
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("abgelehntes Verschieben hat das Board geändert")
	}
}

// --- Stufe 2: finding_anlegen -----------------------------------------------

func TestAgent671_FindingAnlegen(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings",
		`{"titel":"Login ohne Ratenbegrenzung","typ":"S","prioritaet":"P1","beleg":"20 Versuche/s gemessen"}`)
	if s != http.StatusOK {
		t.Fatalf("finding_anlegen: %d %s", s, b)
	}
	m := json671(t, b)
	// Board hat schon F-3 → nächste Nummer 4; displayId 10.
	if m["finding"] != "F-4" || m["displayId"].(float64) != 10 || m["link"] != basis671+"/#board=b1&card=10" {
		t.Fatalf("Antwort: %s", b)
	}
	_, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/10?format=json", "")
	f, _ := json671(t, b)["finding"].(map[string]any)
	if f == nil || f["typ"] != "S" || f["prioritaet"] != "P1" || f["status"] != "offen" || f["beleg"] != "20 Versuche/s gemessen" {
		t.Fatalf("Finding im Board: %s", b)
	}
	// Wortschatz prüft der Fold; seine Begründung kommt als details.
	vorher := e.stand(t, "b1")
	s, b = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings", `{"titel":"x","typ":"X","prioritaet":"P1"}`)
	if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "finding.typ") {
		t.Fatalf("typ X: %d %s", s, b)
	}
	for _, body := range []string{`{"titel":"x","typ":"S"}`, `{"titel":"x","typ":"S","prioritaet":"P1","prio":"P0"}`, `{"typ":"S","prioritaet":"P1"}`} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, s, b)
		}
	}
	if s, _ := e.rufe(t, e.token, "cora@extern.de", "POST", "/werkzeuge/boards/b1/findings", `{"titel":"x","typ":"S","prioritaet":"P1"}`); s != http.StatusForbidden {
		t.Fatalf("cora: %d", s)
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("abgelehntes Finding hat das Board geändert")
	}
}
