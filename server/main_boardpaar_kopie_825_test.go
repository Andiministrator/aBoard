package main

// Karte #825 (ADR-0141 Schritt 2a): der ganze Weg „Externe Kopie anlegen“
// über zwei Server an der echten Kette aus hauptHandler — innen Ziele,
// Aufträge, Hinweg v2; außen Kopie anlegen und Eingang v2. Das Skript des
// Betreibers spielt der Test nach: Er ersetzt jede innere ID durch eine Kennung
// (43 Zeichen base64url) und rechnet die Außen-Board-ID als UUID v5.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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

// skriptKennung bildet, wie das Skript, aus einer inneren ID eine Kennung
// (hier SHA-256 statt HMAC — der Server sieht nur das Format).
func skriptKennung(innen string) string {
	if innen == schatten.BacklogLane || schatten.IstStandardSpalte(innen) {
		return innen
	}
	h := sha256.Sum256([]byte("825|" + innen))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

type paarSeite struct {
	db   *sql.DB
	srv  *httptest.Server
	cfg  *storage.ConfigRepo
	paar *storage.BoardpaarRepo
}

func neueSeite(t *testing.T, seite string) *paarSeite {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	boards := storage.NewBoardRepo(db)
	mux := http.NewServeMux()
	api.NewSchattenAusgangAPI(boards, cfg, paar).Register(mux)
	api.NewSchattenEingangAPI(cfg, paar, storage.NewSchattenRepo(db), nil).Register(mux)
	api.NewBoardpaarErneuernAPI(cfg, paar).Register(mux)
	api.NewBoardpaarKopieAPI(boards, cfg, paar, storage.NewSchattenRepo(db), nil).Register(mux)
	api.NewSchattenAnzeigeAPI(cfg, paar, storage.NewSchattenRepo(db)).Register(mux)
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(storage.NewAgentTokenRepo(db)), true))
	t.Cleanup(srv.Close)
	return &paarSeite{db: db, srv: srv, cfg: cfg, paar: paar}
}

func anfrage825(t *testing.T, srv *httptest.Server, methode, pfad, token string, body any) (int, []byte) {
	t.Helper()
	var rumpf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rumpf = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(methode, srv.URL+pfad, rumpf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

const innenStand825 = `{"version":"2","board":{"id":"b-innen","title":"Projekt X","nextDisplayId":4,
 "identities":[{"label":"Gerd Muster"}],
 "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"},{"id":"spalte-review","title":"Review"}],
 "sprints":[
  {"id":"sprint-1","title":"Sprint 1","kind":"sprint","cards":[
    {"id":"k1","displayId":1,"title":"Angebot Müller","columnId":"col-ready","owner":"Gerd Muster"},
    {"id":"k2","displayId":2,"title":"Geheim Kunde","columnId":"spalte-review","confidential":true},
    {"id":"meta","kind":"sprint-meta","metaType":"start","title":"Start","columnId":"col-backlog"}]},
  {"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"k3","displayId":3,"title":"Idee","columnId":"col-backlog"}]}]}}`

func TestBoardpaar825_KopieUeberZweiServer(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-innen", "Projekt X", "2", []byte(innenStand825)); err != nil {
		t.Fatal(err)
	}
	abs, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abr, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	abeOhne, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "stufe1", "t")
	if _, err := aussen.db.Exec(`INSERT INTO users (id,email,name,created,is_admin) VALUES ('u1','admin@team.example','A','x',1)`); err != nil {
		t.Fatal(err)
	}

	// 1. Skript meldet seine Ziele.
	if s, b := anfrage825(t, innen.srv, "PUT", "/agent/boardpaar/ziele", abr, map[string]any{"ziele": []string{"team-extern"}}); s != 200 {
		t.Fatalf("ziele: %d %s", s, b)
	}
	// 2. Admin innen legt den Auftrag an.
	auftrag, err := innen.paar.AuftragAnlegen(ctx, "b-innen", "team-extern", "https://innen.example/#board=b-innen", "admin")
	if err != nil {
		t.Fatal(err)
	}
	// 3. Skript holt die Aufträge.
	s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/auftraege", abs, nil)
	var auftraege struct {
		Auftraege []map[string]string `json:"auftraege"`
	}
	_ = json.Unmarshal(b, &auftraege)
	if s != 200 || len(auftraege.Auftraege) != 1 || auftraege.Auftraege[0]["board_id"] != "b-innen" ||
		auftraege.Auftraege[0]["titel"] != "Projekt X" || auftraege.Auftraege[0]["innen_link_basis"] != "https://innen.example/#board=b-innen" {
		t.Fatalf("auftraege: %d %s", s, b)
	}
	// 4. Skript legt außen an — wiederholbar.
	aussenID := schatten.AbgeleiteteID(skriptKennung("b-innen"))
	anlage := map[string]any{"id": aussenID, "titel": "Projekt X", "innen_link_basis": "https://innen.example/#board=b-innen"}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abeOhne, anlage); s != 403 {
		t.Errorf("anlegen ohne Recht: %d %s", s, b)
	}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage); s != 201 ||
		strings.Contains(string(b), "@") || !strings.Contains(string(b), `"mitglieder":1`) {
		t.Fatalf("anlegen (Kritik A-4: nur die Zahl der Mitglieder, keine Adressen): %d %s", s, b)
	}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage); s != 200 || !strings.Contains(string(b), `"wiederholt":true`) {
		t.Errorf("anlegen wiederholt: %d %s", s, b)
	}
	if rolle, ok, _ := storage.NewAclRepo(aussen.db).GetRole(ctx, aussenID, "admin@team.example"); !ok || rolle != "owner" {
		t.Errorf("Admin außen: %q %v", rolle, ok)
	}

	// 5. Hinweg holen, übersetzen, schieben.
	s, b = anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen", abs, nil)
	if s != 200 {
		t.Fatalf("hinweg: %d %s", s, b)
	}
	var hinweg struct {
		Format   int                  `json:"format"`
		Version  string               `json:"version"`
		Struktur schatten.Struktur    `json:"struktur"`
		Schatten []schatten.EintragV2 `json:"schatten"`
		Spiegel  []any                `json:"spiegel"`
	}
	if err := json.Unmarshal(b, &hinweg); err != nil || hinweg.Format != 2 || hinweg.Spiegel == nil {
		t.Fatalf("hinweg: %v %s", err, b)
	}
	if len(hinweg.Schatten) != 3 {
		t.Fatalf("hinweg: %d Schatten, erwartet 3 (ohne Meta-Karte)", len(hinweg.Schatten))
	}
	push := func() (int, []byte) {
		for i := range hinweg.Struktur.Lanes {
			hinweg.Struktur.Lanes[i].ID = skriptKennung(hinweg.Struktur.Lanes[i].ID)
		}
		for i := range hinweg.Struktur.Spalten {
			hinweg.Struktur.Spalten[i].ID = skriptKennung(hinweg.Struktur.Spalten[i].ID)
		}
		var liste []map[string]any
		for _, e := range hinweg.Schatten {
			eb, _ := json.Marshal(e)
			var m map[string]any
			_ = json.Unmarshal(eb, &m)
			delete(m, "id")
			m["schattenId"] = skriptKennung(e.ID)
			if e.Lane != nil {
				m["lane"], m["spalte"] = skriptKennung(*e.Lane), skriptKennung(*e.Spalte)
			}
			liste = append(liste, m)
		}
		return anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, map[string]any{
			"version": hinweg.Version, "struktur": hinweg.Struktur, "schatten": liste, "spiegel": []any{}, "rueckweg_bis": nil})
	}
	if s, b := push(); s != 200 || !strings.Contains(string(b), `"struktur_geaendert":true`) {
		t.Fatalf("eingang: %d %s", s, b)
	}
	kopie, _ := storage.NewBoardRepo(aussen.db).Get(ctx, aussenID)
	var st struct {
		Board struct {
			Sprints    []map[string]any `json:"sprints"`
			ColumnDefs []map[string]any `json:"columnDefs"`
		} `json:"board"`
	}
	_ = json.Unmarshal(kopie.StateJSON, &st)
	if len(st.Board.Sprints) != 2 || st.Board.Sprints[0]["title"] != "Sprint 1" ||
		st.Board.Sprints[0]["spiegel"].(map[string]any)["id"] != skriptKennung("sprint-1") ||
		len(st.Board.Sprints[0]["cards"].([]any)) != 0 {
		t.Errorf("Lanes außen: %+v", st.Board.Sprints)
	}
	if len(st.Board.ColumnDefs) != 3 || st.Board.ColumnDefs[2]["id"] != schatten.AussenSpalteID(skriptKennung("spalte-review")) {
		t.Errorf("Spalten außen: %+v", st.Board.ColumnDefs)
	}
	if strings.Contains(string(kopie.StateJSON), "Geheim Kunde") || strings.Contains(string(kopie.StateJSON), "Angebot") {
		t.Error("Karteninhalt im Board-Zustand außen (Schatten gehören in die Tabelle)")
	}
	zeilen, _ := storage.NewSchattenRepo(aussen.db).Liste(ctx, aussenID)
	if len(zeilen) != 3 {
		t.Fatalf("Schatten außen: %d", len(zeilen))
	}
	for _, z := range zeilen {
		if z.Fassung != 2 {
			t.Errorf("Fassung %d", z.Fassung)
		}
		if z.Nr != nil && *z.Nr == 2 && (z.Titel == nil || *z.Titel != "Geheim Kunde" || !z.Vertraulich || *z.Spalte != "Review") {
			t.Errorf("vertraulicher Schatten: %+v", z)
		}
	}
	// Anzeige im Browser außen (Menschen-Kette): kopie, Lane und Spalte je
	// Schatten, vertraulicher Titel sichtbar (Beschluss 3, Fassung 2).
	s, b = anfrage825(t, aussen.srv, "GET", "/boards/"+aussenID+"/schatten", "", nil)
	var anzeige struct {
		Kopie    bool             `json:"kopie"`
		Schatten []map[string]any `json:"schatten"`
	}
	if err := json.Unmarshal(b, &anzeige); s != 200 || err != nil || !anzeige.Kopie || len(anzeige.Schatten) != 3 {
		t.Fatalf("anzeige: %d %s", s, b)
	}
	for _, e := range anzeige.Schatten {
		if e["nummer"] == "i-2" && (e["titel"] != "Geheim Kunde" || e["titel_platzhalter"] != false ||
			e["lane"] != skriptKennung("sprint-1") || e["spalte_id"] != skriptKennung("spalte-review")) {
			t.Errorf("anzeige vertraulich: %v", e)
		}
	}

	// Unverändert nochmal: kein Schreiben des Board-Zustands.
	s, b = anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen", abs, nil)
	_ = json.Unmarshal(b, &hinweg)
	if s, b := push(); s != 200 || !strings.Contains(string(b), `"struktur_geaendert":false`) {
		t.Errorf("zweiter Lauf: %d %s", s, b)
	}
	if danach, _ := storage.NewBoardRepo(aussen.db).Get(ctx, aussenID); danach.UpdatedAt != kopie.UpdatedAt {
		t.Error("unveränderter Hinweg hat den Board-Zustand geschrieben")
	}

	// 6. Skript quittiert den Auftrag.
	if s, b := anfrage825(t, innen.srv, "POST", "/agent/boardpaar/auftraege/"+auftrag.ID+"/erledigt", abr, nil); s != 200 || !strings.Contains(string(b), "erledigt") {
		t.Errorf("erledigt: %d %s", s, b)
	}
	if s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/auftraege", abs, nil); s != 200 || strings.Contains(string(b), auftrag.ID) {
		t.Errorf("nach erledigt noch offen: %s", b)
	}

	// Verify V-1: Karten ohne Ort stehen als Fehlgrund am Abgleich-Stand innen,
	// mit beiden Ursachen getrennt; nach der Behebung ist der Grund wieder leer.
	ohneOrt := strings.Replace(innenStand825, `"columnId":"col-ready","owner"`, `"columnId":"gibt-es-nicht","owner"`, 1)
	if _, err := storage.NewBoardRepo(innen.db).Update(ctx, "b-innen", "Projekt X", "2", []byte(ohneOrt), 0); err != nil {
		t.Fatal(err)
	}
	anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen", abs, nil)
	liste, _ := innen.paar.InnenListe(ctx)
	if len(liste) != 1 || !strings.Contains(liste[0].LetzterFehlgrund, "1 Karte(n) gingen nicht hinaus: 1 in einer Spalte") {
		t.Errorf("Fehlgrund: %+v", liste)
	}

	// Stufe 1 und Kopie bleiben getrennt (W2-1).
	if _, err := storage.NewBoardRepo(aussen.db).Insert(ctx, "s1", "S", "2", storage.CanonicalEmptyBoardState("s1", "S")); err != nil {
		t.Fatal(err)
	}
	_, _ = aussen.paar.AussenKoppeln(ctx, "s1", "https://innen.example/#board=x", "a")
	if s, _ := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/s1", abe, map[string]any{}); s != 404 {
		t.Errorf("v2 auf Stufe-1-Board: %d, erwartet 404", s)
	}
	if s, _ := anfrage825(t, aussen.srv, "PUT", "/agent/schatten/"+aussenID, abe, map[string]any{"version": "dev", "schatten": []any{}}); s != 404 {
		t.Errorf("Stufe-1-Eingang auf Kopie: %d, erwartet 404", s)
	}
	if s, _ := anfrage825(t, aussen.srv, "PUT", "/agent/schatten/s1", abe, map[string]any{"version": "dev", "schatten": []any{}}); s != 200 {
		t.Errorf("Stufe 1 läuft nicht mehr: %d", s)
	}

	// Kritik Sprint 93 S-1: Ein innen gekoppeltes Board OHNE Auftrag (reines
	// Stufe-1-Board) bekommt keinen Hinweg v2 — die Titel vertraulicher
	// Karten bleiben innen (ADR-0140).
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-stufe1", "Stufe 1", "2", []byte(innenStand825)); err != nil {
		t.Fatal(err)
	}
	if err := innen.paar.InnenKoppeln(ctx, "b-stufe1", "admin"); err != nil {
		t.Fatal(err)
	}
	if s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-stufe1", abs, nil); s != 404 || strings.Contains(string(b), "Geheim") {
		t.Errorf("Hinweg ohne Auftrag: %d %s, erwartet 404", s, b)
	}

	// Kritik Sprint 93 K-3/R-1: Außen entkoppelt → erneutes Anlegen nimmt die
	// Kopie wieder auf (201), statt dauerhaft 409.
	if err := aussen.paar.AussenEntkoppeln(ctx, aussenID); err != nil {
		t.Fatal(err)
	}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage); s != 201 ||
		!strings.Contains(string(b), `"wiederaufgenommen":true`) || !strings.Contains(string(b), `"wiederholt":false`) {
		t.Errorf("Wiederaufnahme nach Entkoppeln: %d %s, erwartet 201 mit wiederaufgenommen (Verify V-1)", s, b)
	}
	if e, _ := aussen.paar.AussenGekoppelt(ctx, aussenID); e == nil || !e.Kopie {
		t.Errorf("nach Wiederaufnahme nicht als Kopie gekoppelt: %+v", e)
	}
}

// Kritik Sprint 93 K-7: Der Titel im Auftrag kommt in der Form, die außen
// angenommen wird — auch bei einem langen Titel mit Tab.
func TestBoardpaar93_AuftragTitelNormalform(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	titel := "Projekt\t" + strings.Repeat("x", 300)
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-lang", titel, "2", []byte(innenStand825)); err != nil {
		t.Fatal(err)
	}
	abs, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abr, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	anfrage825(t, innen.srv, "PUT", "/agent/boardpaar/ziele", abr, map[string]any{"ziele": []string{"team"}})
	if _, err := innen.paar.AuftragAnlegen(ctx, "b-lang", "team", "https://innen.example/#board=b-lang", "admin"); err != nil {
		t.Fatal(err)
	}
	_, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/auftraege", abs, nil)
	var auftraege struct {
		Auftraege []map[string]string `json:"auftraege"`
	}
	_ = json.Unmarshal(b, &auftraege)
	if len(auftraege.Auftraege) != 1 {
		t.Fatalf("auftraege: %s", b)
	}
	if _, err := storage.PruefeKopieTitel(auftraege.Auftraege[0]["titel"]); err != nil {
		t.Errorf("Titel %q besteht die Prüfung außen nicht: %v", auftraege.Auftraege[0]["titel"], err)
	}
}

// Schranke und Seiten: jeder Typ erreicht nur seine Wege, jeder Weg gilt nur
// auf seiner Seite (404 vor dem Token), die Menschen-Kette nie.
func TestBoardpaar825_SchrankeUndSeiten(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	abs, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abr, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	abrAussen, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	type fall struct {
		name          string
		srv           *httptest.Server
		methode, pfad string
		token         string
		want          int
	}
	for _, f := range []fall{
		// Fremde Typen auf fremden Wegen: Schranke, 403.
		{"abe auf auftraege", innen.srv, "GET", "/agent/boardpaar/auftraege", abe, 403},
		{"abs auf ziele", innen.srv, "PUT", "/agent/boardpaar/ziele", abs, 403},
		{"abr auf spiegel GET", innen.srv, "GET", "/agent/boardpaar/spiegel/b", abr, 403},
		{"abs auf boards", aussen.srv, "POST", "/agent/boardpaar/boards", abs, 403},
		{"abr auf spiegel PUT", aussen.srv, "PUT", "/agent/boardpaar/spiegel/b", abr, 403},
		{"abr auf erledigt mit Unterpfad", innen.srv, "POST", "/agent/boardpaar/auftraege/a/erledigt/x", abr, 403},
		{"abr auf erledigt per GET", innen.srv, "GET", "/agent/boardpaar/auftraege/a/erledigt", abr, 403},
		{"abs am Menschen-Pfad", innen.srv, "GET", "/boardpaar/auftraege", abs, 403},
		{"abr am Menschen-Pfad", innen.srv, "PUT", "/boardpaar/ziele", abr, 403},
		{"abr auf Boards-Liste", innen.srv, "GET", "/agent/boards", abr, 403},
		// Eigene Wege auf der falschen Seite: 404 vor der Token-Prüfung.
		{"auftraege außen", aussen.srv, "GET", "/agent/boardpaar/auftraege", "abs_erfunden", 404},
		{"ziele außen", aussen.srv, "PUT", "/agent/boardpaar/ziele", abrAussen, 404},
		{"boards innen", innen.srv, "POST", "/agent/boardpaar/boards", "abe_erfunden", 404},
		{"spiegel GET außen", aussen.srv, "GET", "/agent/boardpaar/spiegel/b", "abs_x", 404},
		// Richtige Seite, falsches Token: 401.
		{"erfundenes abs", innen.srv, "GET", "/agent/boardpaar/auftraege", "abs_erfunden", 401},
		{"erfundenes abr", innen.srv, "PUT", "/agent/boardpaar/ziele", "abr_erfunden", 401},
		// Board nicht gekoppelt: 404.
		{"hinweg ungekoppelt", innen.srv, "GET", "/agent/boardpaar/spiegel/b", abs, 404},
		{"eingang ungekoppelt", aussen.srv, "PUT", "/agent/boardpaar/spiegel/b", abe, 404},
		{"erledigt unbekannt", innen.srv, "POST", "/agent/boardpaar/auftraege/gibt-es-nicht/erledigt", abr, 404},
		// abr erneuert sich innen, außen gibt es ihn nicht.
		{"abr erneuern außen", aussen.srv, "POST", "/agent/boardpaar/erneuern", abrAussen, 401},
		{"abr erneuern innen", innen.srv, "POST", "/agent/boardpaar/erneuern", abr, 200},
	} {
		if s, b := anfrage825(t, f.srv, f.methode, f.pfad, f.token, nil); s != f.want {
			t.Errorf("%s: %d, erwartet %d (%s)", f.name, s, f.want, b)
		}
	}
	// Ungültige Bodies: 422.
	if s, _ := anfrage825(t, innen.srv, "PUT", "/agent/boardpaar/ziele", abr, map[string]any{"ziele": []string{"a b"}}); s != 422 {
		t.Errorf("Zielname: %d", s)
	}
	if s, _ := anfrage825(t, innen.srv, "PUT", "/agent/boardpaar/ziele", abr, map[string]any{"andere": 1}); s != 422 {
		t.Errorf("fremder Schlüssel: %d", s)
	}
	if s, _ := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, map[string]any{"id": "x", "titel": "T", "innen_link_basis": "https://i/#board=b"}); s != 422 {
		t.Errorf("ID keine UUID: %d", s)
	}
}

// Verify Sprint 93 V-3: Ein ungültiger Eintrag in boardpaar.team (gespeichert
// am Prüfweg vorbei) lässt das übrige Team nicht wegfallen — über den Handler.
func TestBoardpaar93_KopieMitKaputtemTeam(t *testing.T) {
	ctx := context.Background()
	aussen := neueSeite(t, "aussen")
	cfg := storage.NewConfigRepo(aussen.db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarTeam, "team@team.example, kaputt", false, "test"); err != nil {
		t.Fatal(err)
	}
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	id := "0e5d8a2c-1111-5222-8333-44445555dddd"
	s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe,
		map[string]any{"id": id, "titel": "P", "innen_link_basis": "https://innen.example/#board=x"})
	if s != 201 || !strings.Contains(string(b), `"mitglieder":1`) {
		t.Fatalf("anlegen: %d %s, erwartet 201 mit einem Mitglied (team@team.example)", s, b)
	}
	if rolle, ok, _ := storage.NewAclRepo(aussen.db).GetRole(ctx, id, "team@team.example"); !ok || rolle != storage.RoleViewer {
		t.Errorf("team@team.example: %q %v, erwartet viewer", rolle, ok)
	}
}
