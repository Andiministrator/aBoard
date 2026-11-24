package api_test

// schleuse_versand_test.go — Freigabe, Vier-Augen, Hintergrundauftrag,
// Antwortkommentar „extern“ und Beleg über HTTP (Karte #686, ADR-0134
// §3.4–§3.7).
//
// Der externe Anbieter ist ein httptest-Server. Der echte Versand-Transport
// lehnt 127.0.0.1 ab (das prüft schleuse/versand_test.go); hier lenkt ein
// Test-Transport jede Anfrage auf den Nachbau um — Anbieterlogik,
// Ausgangs-Schalter und Fehlerbehandlung laufen unverändert.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/schleuse"
	"github.com/Andiministrator/aBoard/server/storage"
)

type anbieterAttrappe struct {
	mu       sync.Mutex
	anfragen []map[string]any
	schl     []string
	antwort  string
	status   int
	body     string
	waehrend func() // läuft während der Anfrage (Verify VS3-2)
}

func (a *anbieterAttrappe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	roh, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(roh, &m)
	a.mu.Lock()
	a.anfragen = append(a.anfragen, m)
	a.schl = append(a.schl, r.Header.Get("x-api-key"))
	antwort, status, body, waehrend := a.antwort, a.status, a.body, a.waehrend
	a.mu.Unlock()
	if waehrend != nil {
		waehrend()
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}
	out, _ := json.Marshal(map[string]any{"model": "claude-x", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": antwort}},
		"usage":   map[string]any{"input_tokens": 11, "output_tokens": 7}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (a *anbieterAttrappe) zahl() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.anfragen)
}

// umlenken schickt jede Anfrage an ziel (Schema/Host), Pfad bleibt.
type umlenken struct{ ziel *url.URL }

func (u umlenken) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host, r2.Host = u.ziel.Scheme, u.ziel.Host, u.ziel.Host
	return http.DefaultTransport.RoundTrip(r2)
}

type versandEnv struct {
	*auszugEnv
	anbieter *anbieterAttrappe
}

func neueVersandEnv(t *testing.T) *versandEnv {
	t.Helper()
	e := &versandEnv{auszugEnv: neueAuszugEnv(t, schleuse.Konfig{AuftraegeProStunde: 2}),
		anbieter: &anbieterAttrappe{antwort: "Vorschlag für [PERSON_1]: Kontakt mit [FIRMA_1]. Offen: [Person_2] und [PERSON_99]."}}
	srv := httptest.NewServer(e.anbieter)
	t.Cleanup(srv.Close)
	ziel, _ := url.Parse(srv.URL)
	e.sl.MitVersand(storage.NewBoardRepo(e.db), nil, "test").MitVersandTransport(umlenken{ziel: ziel})
	return e
}

// erstelle legt als Anna einen Auszug an und liefert id und Hash.
func (e *versandEnv) erstelle(t *testing.T) (string, string, string) {
	t.Helper()
	code, roh := e.roh(t, http.MethodPost, e.pfad(""), slAnna, e.auszug())
	if code != http.StatusCreated {
		t.Fatalf("Auszug: %d %s", code, roh)
	}
	a := auftragAus(t, roh)
	return a["id"].(string), a["sha256"].(string), a["text"].(string)
}

func (e *versandEnv) karte(t *testing.T) map[string]any {
	t.Helper()
	var st string
	if err := e.db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, slBoard).Scan(&st); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(st), &root)
	for _, sp := range root["board"].(map[string]any)["sprints"].([]any) {
		for _, k := range sp.(map[string]any)["cards"].([]any) {
			if km := k.(map[string]any); km["id"] == "k1" {
				return km
			}
		}
	}
	t.Fatal("Karte k1 fehlt")
	return nil
}

func externeKommentare(k map[string]any) []map[string]any {
	var out []map[string]any
	for _, c := range k["comments"].([]any) {
		if cm := c.(map[string]any); cm["kind"] == "extern" {
			out = append(out, cm)
		}
	}
	return out
}

func schleuseVerlauf(k map[string]any) []string {
	var out []string
	if vl, ok := k["activity"].([]any); ok {
		for _, v := range vl {
			if a, _ := v.(map[string]any)["action"].(string); strings.HasPrefix(a, "schleuse-") {
				out = append(out, a)
			}
		}
	}
	return out
}

func (e *versandEnv) logText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.logs, "\n")
}

// Kern: Gesendet wird genau der gespeicherte, freigegebene Text mit dem
// Schlüssel der Person; die Antwort kommt re-identifiziert als Kommentar
// „extern“ an die Karte, veränderte Platzhalter bleiben stehen und werden
// gemeldet, der Verlauf protokolliert Versand und Antwort, der Beleg zeigt
// „unverändert“ — und nach einer Änderung am Kommentar „weicht ab“.
func TestSchleuseVersand_EndeZuEnde(t *testing.T) {
	e := neueVersandEnv(t)
	id, sha, text := e.erstelle(t)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slVera, map[string]any{"sha256": sha}); code != http.StatusForbidden {
		t.Errorf("Viewer gibt frei: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slBert, map[string]any{"sha256": sha}); code != http.StatusNotFound {
		t.Errorf("fremde Person gibt frei: %d %s", code, roh)
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": schleuse.Hash("anders")}); code != http.StatusConflict || !strings.Contains(roh, "veraltet") {
		t.Errorf("fremder Hash: %d %s", code, roh)
	}
	code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha})
	if code != http.StatusOK {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()

	if e.anbieter.zahl() != 1 {
		t.Fatalf("Anfragen an den Anbieter: %d", e.anbieter.zahl())
	}
	anfrage := e.anbieter.anfragen[0]
	msgs := anfrage["messages"].([]any)
	gesendet := msgs[len(msgs)-1].(map[string]any)["content"]
	if s, ok := gesendet.(string); !ok || s != text || schleuse.Hash(s) != sha {
		t.Fatalf("gesendet wurde nicht genau der freigegebene Text:\n%v\nsoll:\n%s", gesendet, text)
	}
	if e.anbieter.schl[0] != "sk-anna" {
		t.Errorf("Schlüssel: %q", e.anbieter.schl[0])
	}
	if sys, _ := anfrage["system"].(string); !strings.Contains(sys, "Platzhalter") {
		t.Errorf("System-Text: %q", sys)
	}
	raw, _ := json.Marshal(anfrage)
	keinOriginal(t, "Anfrage an den Anbieter", string(raw))

	z, err := e.repo.Zuordnung(context.Background(), id, slAnnaID)
	if err != nil || z["[PERSON_1]"] == "" || z["[FIRMA_1]"] == "" {
		t.Fatalf("Zuordnung: %v %v", z, err)
	}
	k := e.karte(t)
	ext := externeKommentare(k)
	if len(ext) != 1 {
		t.Fatalf("externe Kommentare: %d", len(ext))
	}
	c := ext[0]
	soll := "Vorschlag für " + z["[PERSON_1]"] + ": Kontakt mit " + z["[FIRMA_1]"] + ". Offen: [Person_2] und [PERSON_99]."
	if c["text"] != soll {
		t.Errorf("Kommentar:\n%v\nsoll:\n%s", c["text"], soll)
	}
	if c["authorEmail"] != slAnna || !strings.Contains(c["author"].(string), "anthropic/claude-x") {
		t.Errorf("Kennzeichnung: %v / %v", c["authorEmail"], c["author"])
	}
	if sl, _ := c["schleuse"].(map[string]any); sl["auftragId"] != id || sl["freigegebenVon"] != slAnna || sl["gesendetAm"] == "" {
		t.Errorf("Beleg-Verweis: %v", c["schleuse"])
	}
	if got := strings.Join(schleuseVerlauf(k), ","); got != "schleuse-gesendet,schleuse-beantwortet" {
		t.Errorf("Verlauf: %s", got)
	}

	code, roh = e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil)
	a := auftragAus(t, roh)
	if code != 200 || a["zustand"] != "beantwortet" || a["antwort_text"] != e.anbieter.antwort || a["kommentar_id"] != c["id"] {
		t.Fatalf("Auftrag: %d %v", code, a)
	}
	if v := a["veraenderte_platzhalter"].([]any); len(v) != 2 {
		t.Errorf("veränderte Platzhalter: %v", v)
	}

	// Der Beleg ist für jedes Mitglied lesbar (Vera ist viewer).
	beleg := func() map[string]any {
		code, roh := e.roh(t, http.MethodGet, "/boards/"+slBoard+"/cards/k1/schleuse-belege/"+id, slVera, nil)
		if code != 200 {
			t.Fatalf("Beleg: %d %s", code, roh)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(roh), &m)
		return m["beleg"].(map[string]any)
	}
	if b := beleg(); b["abweichend"] != false || b["kommentar_vorhanden"] != true || b["auszug"] != text || b["absender"] != slAnna {
		t.Errorf("Beleg: %v", b)
	}
	// Ein Schreibweg vorbei an der Prüfung (direkt in der Datenbank) ändert
	// den Kommentar — der Beleg zeigt es.
	var st string
	_ = e.db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, slBoard).Scan(&st)
	st = strings.Replace(st, "Vorschlag für", "Gefälscht für", 1)
	_, _ = e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, st, slBoard)
	if b := beleg(); b["abweichend"] != true {
		t.Errorf("Abweichung nicht erkannt: %v", b)
	}

	// Kein Antwort- oder Auszugstext im Server-Log.
	logs := e.logText()
	if strings.Contains(logs, "Vorschlag für") || strings.Contains(logs, "## Beschreibung") {
		t.Errorf("Inhalt im Log:\n%s", logs)
	}
	if !strings.Contains(logs, "Auftrag "+id+" gesendet") || !strings.Contains(logs, sha) {
		t.Errorf("Betriebsprotokoll fehlt:\n%s", logs)
	}
}

// Vier-Augen: Nach Annas Freigabe geht nichts hinaus; Anna kann nicht selbst
// zweitfreigeben, Vera (viewer) nicht; Bert (editor) sieht den Auftrag und
// gibt frei — dann wird gesendet, mit beiden Namen im Beleg.
func TestSchleuseVersand_VierAugenUeberHTTP(t *testing.T) {
	e := neueVersandEnv(t)
	if _, err := e.db.Exec(`UPDATE schleuse_freigaben SET vier_augen = 1 WHERE id = ?`, e.freigabeID); err != nil {
		t.Fatal(err)
	}
	id, sha, _ := e.erstelle(t)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 0 {
		t.Fatalf("ohne zweite Freigabe gesendet")
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/zweitfreigabe"), slAnna, map[string]any{"sha256": sha}); code != http.StatusConflict || !strings.Contains(roh, "selbstfreigabe") {
		t.Errorf("Selbstfreigabe: %d %s", code, roh)
	}
	if code, _ := e.roh(t, http.MethodPost, e.pfad("/"+id+"/zweitfreigabe"), slVera, map[string]any{"sha256": sha}); code != http.StatusForbidden {
		t.Errorf("Viewer gibt frei: %d", code)
	}
	code, roh := e.roh(t, http.MethodGet, e.pfad(""), slBert, nil)
	var liste map[string]any
	_ = json.Unmarshal([]byte(roh), &liste)
	zur, _ := liste["zur_freigabe"].([]any)
	if code != 200 || len(zur) != 1 || zur[0].(map[string]any)["id"] != id || zur[0].(map[string]any)["antragsteller"] != slAnna {
		t.Fatalf("Liste für Bert: %d %s", code, roh)
	}
	keinOriginal(t, "Zweitfreigabe-Sicht", strings.ReplaceAll(roh, slAnna, ""))
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/zweitfreigabe"), slBert, map[string]any{"sha256": sha}); code != 200 {
		t.Fatalf("Zweitfreigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 1 {
		t.Fatalf("Anfragen: %d", e.anbieter.zahl())
	}
	_, roh = e.roh(t, http.MethodGet, "/boards/"+slBoard+"/cards/k1/schleuse-belege/"+id, slVera, nil)
	if !strings.Contains(roh, `"zweit_freigegeben_von":"`+slBert+`"`) || !strings.Contains(roh, `"freigegeben_von":"`+slAnna+`"`) {
		t.Errorf("Beleg ohne beide Freigaben: %s", roh)
	}
}

// Fehler beim Anbieter: einmal versucht, nie wiederholt, „fehlgeschlagen“
// mit Grund, Verlaufseintrag, kein Kommentar — und der Text der
// Fehlerantwort (die den Auszug zurückspiegelt) steht in keinem Log.
func TestSchleuseVersand_AnbieterfehlerOhneWiederholung(t *testing.T) {
	e := neueVersandEnv(t)
	id, sha, text := e.erstelle(t)
	e.anbieter.status = http.StatusBadRequest
	e.anbieter.body = `{"error":{"message":"bad request: ` + strings.ReplaceAll(text[:40], `"`, "") + `"}}`
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 1 {
		t.Errorf("Anfragen: %d (keine Wiederholung erwartet)", e.anbieter.zahl())
	}
	_, roh := e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil)
	a := auftragAus(t, roh)
	if a["zustand"] != "fehlgeschlagen" || a["fehler_grund"] != "abgelehnt" {
		t.Errorf("Auftrag: %v / %v", a["zustand"], a["fehler_grund"])
	}
	k := e.karte(t)
	if len(externeKommentare(k)) != 0 || strings.Join(schleuseVerlauf(k), ",") != "schleuse-gesendet,schleuse-fehlgeschlagen" {
		t.Errorf("Karte: %d Kommentare, Verlauf %v", len(externeKommentare(k)), schleuseVerlauf(k))
	}
	if strings.Contains(e.logText(), text[:40]) {
		t.Errorf("Fehlerantwort im Log:\n%s", e.logText())
	}
	// Erneut freigeben geht nicht — neuer Versand heisst neuer Auszug.
	if code, _ := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != http.StatusConflict {
		t.Errorf("erneute Freigabe nach Fehlschlag: %d", code)
	}
}

// Die Stundengrenze (hier 2) gilt je Person für GESENDETE Aufträge; die
// Freigabe darüber hinaus wird abgelehnt, ohne dass etwas hinausgeht.
func TestSchleuseVersand_StundengrenzeUeberHTTP(t *testing.T) {
	e := neueVersandEnv(t)
	for i := 0; i < 2; i++ {
		id, sha, _ := e.erstelle(t)
		if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
			t.Fatalf("Freigabe %d: %d %s", i, code, roh)
		}
		e.sl.WarteVersand()
	}
	id, sha, _ := e.erstelle(t)
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != http.StatusTooManyRequests {
		t.Errorf("dritte Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 2 {
		t.Errorf("Anfragen: %d", e.anbieter.zahl())
	}
}

// Eine Sperre zwischen Freigabe und Versand hält den Auftrag auf: Der
// Hintergrundauftrag prüft sie in der Transaktion, die „gesendet“ setzt.
func TestSchleuseVersand_SperreVorDemVerbinden(t *testing.T) {
	e := neueVersandEnv(t)
	if _, err := e.db.Exec(`UPDATE schleuse_freigaben SET vier_augen = 1 WHERE id = ?`, e.freigabeID); err != nil {
		t.Fatal(err)
	}
	id, sha, _ := e.erstelle(t)
	if code, _ := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatal("Freigabe")
	}
	// Zweite Freigabe direkt im Speicher, dann Sperre, dann Versand anstossen.
	if _, err := e.repo.ZweitFreigeben(context.Background(), id, slBoard, "k1", slBert, sha); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE boards SET schleuse_gesperrt = 1 WHERE id = ?`, slBoard); err != nil {
		t.Fatal(err)
	}
	e.sl.VersandStart(context.Background())
	e.sl.WarteVersand()
	if e.anbieter.zahl() != 0 {
		t.Fatalf("trotz Sperre gesendet")
	}
	_, roh := e.roh(t, http.MethodGet, e.pfad("/"+id), slAnna, nil)
	if a := auftragAus(t, roh); a["zustand"] != "entwurf" || a["fehler_grund"] != "board-gesperrt" {
		t.Errorf("Auftrag: %v / %v", a["zustand"], a["fehler_grund"])
	}
}

// Verify VS3-2: Wird die Karte während des Versands in ein anderes Board
// übertragen, kommt die Antwort dort an — der Transfer nimmt den Auftrag mit.
func TestSchleuseVersand_TransferWaehrendDesVersands(t *testing.T) {
	e := neueVersandEnv(t)
	ziel := `{"version":"1","board":{"title":"Ziel","columnDefs":[{"id":"col-backlog","title":"Backlog"}],"nextDisplayId":1,
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}]}}`
	if _, err := e.db.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('board-ziel', 'Ziel', '1', ?, 1, 1)`, ziel); err != nil {
		t.Fatal(err)
	}
	// Die Karte braucht eine Spalte, die es im Ziel gibt.
	var st string
	_ = e.db.QueryRow(`SELECT state_json FROM boards WHERE id = ?`, slBoard).Scan(&st)
	_, _ = e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, strings.Replace(st, `"id":"k1",`, `"id":"k1","columnId":"col-backlog",`, 1), slBoard)
	id, sha, _ := e.erstelle(t)
	e.anbieter.waehrend = func() {
		if _, err := storage.NewBoardRepo(e.db).TransferCard(context.Background(), storage.TransferCardParams{
			SrcBoardID: slBoard, DstBoardID: "board-ziel", CardID: "k1", DstColumnID: "col-backlog", Actor: slAnna}); err != nil {
			t.Errorf("Transfer: %v", err)
		}
	}
	if code, roh := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatalf("Freigabe: %d %s", code, roh)
	}
	e.sl.WarteVersand()
	var zielStand string
	_ = e.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'board-ziel'`).Scan(&zielStand)
	if !strings.Contains(zielStand, `"kind":"extern"`) {
		t.Fatalf("Antwortkommentar nicht im Ziel-Board")
	}
	var zustand, board string
	_ = e.db.QueryRow(`SELECT zustand, board_id FROM schleuse_auftraege WHERE id = ?`, id).Scan(&zustand, &board)
	if zustand != "beantwortet" || board != "board-ziel" {
		t.Errorf("Auftrag: %s / %s", zustand, board)
	}
}

// Kritiker KS3-7 (F-1646): Ohne Antwortkommentar ist der Beleg nur für die
// Person, die gesendet hat (der Auszug trägt ihre Aufgabenstellung).
func TestSchleuseVersand_BelegOhneKommentarNurAbsender(t *testing.T) {
	e := neueVersandEnv(t)
	id, sha, _ := e.erstelle(t)
	e.anbieter.status = http.StatusInternalServerError
	if code, _ := e.roh(t, http.MethodPost, e.pfad("/"+id+"/freigabe"), slAnna, map[string]any{"sha256": sha}); code != 200 {
		t.Fatal("Freigabe")
	}
	e.sl.WarteVersand()
	belegPfad := "/boards/" + slBoard + "/cards/k1/schleuse-belege/" + id
	if code, _ := e.roh(t, http.MethodGet, belegPfad, slVera, nil); code != http.StatusNotFound {
		t.Errorf("Beleg ohne Kommentar für Viewer: %d", code)
	}
	if code, _ := e.roh(t, http.MethodGet, belegPfad, slAnna, nil); code != http.StatusOK {
		t.Errorf("Beleg ohne Kommentar für die Antragstellerin: %d", code)
	}
}
