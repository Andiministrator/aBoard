package main

// Karte #827 (ADR-0141 §5.3, §6, „Umsetzung 2c“ Punkte 20–24; Etappe E3b):
// POST /agent/boardpaar/anwenden an der echten Kette aus hauptHandler —
// Zugang, Schranke, Seite, Kopplung, und je Ergebniszeile der Tabelle aus
// §5.3 ein Fall. Der Ops-Weg läuft mit echtem Fold.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const innenStand827 = `{"version":"2","board":{"id":"b-innen","title":"Projekt X","nextDisplayId":5,
 "identities":[{"label":"Gerd Muster"},{"label":"Andi"}],
 "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"},{"id":"spalte-review","title":"Review"}],
 "termine":[{"id":"t1","titel":"Abnahme","datum":"2026-10-20","karteId":"k1"},{"id":"t2","titel":"Ohne Karte","datum":"2026-10-21"}],
 "sprints":[
  {"id":"sprint-1","title":"Sprint 1","kind":"sprint","cards":[
    {"id":"k1","displayId":1,"title":"Angebot Müller","columnId":"col-ready","description":"- [ ] Preis\n- [ ] Termin",
     "labels":[],"assignees":[],"comments":[{"id":"c-innen","author":"Andi","authorEmail":"andi@innen.example","text":"Hallo"}]},
    {"id":"k2","displayId":2,"title":"Geheim","columnId":"col-ready","confidential":true},
    {"id":"k4","displayId":4,"title":"Zweite","columnId":"col-ready"}]},
  {"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"k3","displayId":3,"title":"Idee","columnId":"col-backlog"}]}]}}`

type innen827 struct {
	db     *sql.DB
	srv    *httptest.Server
	boards *storage.BoardRepo
	abr    string
}

// neueInnenSeite827: Seite innen mit Ops-Weg (Fold, Protokoll) und einem
// gekoppelten, beauftragten Board.
func neueInnenSeite827(t *testing.T) *innen827 {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := storage.NewConfigRepo(db, nil, uuidString)
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	boards := storage.NewBoardRepo(db)
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	ops := api.NewBoardsAPI(boards).WithFold(motor).WithActivities(storage.NewActivityRepo(db))
	mux := http.NewServeMux()
	api.NewBoardpaarKopieAPI(boards, cfg, paar, storage.NewSchattenRepo(db), nil).MitRueckweg(ops).Register(mux)
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(storage.NewAgentTokenRepo(db)), true))
	t.Cleanup(srv.Close)
	if _, err := boards.Insert(ctx, "b-innen", "Projekt X", "2", []byte(innenStand827)); err != nil {
		t.Fatal(err)
	}
	abr, _, _ := paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	if _, err := paar.AuftragAnlegen(ctx, "b-innen", "team-extern", "https://innen.example/#board=b-innen", "admin"); err != nil {
		t.Fatal(err)
	}
	return &innen827{db: db, srv: srv, boards: boards, abr: abr}
}

func (s *innen827) anwenden(t *testing.T, eintrag map[string]any) (int, map[string]any) {
	t.Helper()
	if _, da := eintrag["person"]; !da {
		eintrag["person"] = "gerd@team.example"
	}
	if _, da := eintrag["zeit"]; !da {
		eintrag["zeit"] = "2026-10-14T08:15:00.000Z"
	}
	code, b := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-innen", s.abr, eintrag)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Antwort kein JSON: %d %s", code, b)
	}
	return code, m
}

func (s *innen827) erwarte(t *testing.T, eintrag map[string]any, ergebnis, grund string) {
	t.Helper()
	code, m := s.anwenden(t, eintrag)
	g, _ := m["grund"].(string)
	if code != 200 || m["ergebnis"] != ergebnis || g != grund {
		t.Fatalf("%v: %d %v, erwartet %s/%s", eintrag["op_id"], code, m, ergebnis, grund)
	}
}

// stand liest Karte, Termin und das Board innen.
func (s *innen827) stand(t *testing.T) map[string]any {
	t.Helper()
	b, err := s.boards.Get(context.Background(), "b-innen")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	_ = json.Unmarshal(b.StateJSON, &st)
	return st["board"].(map[string]any)
}

func karte827(board map[string]any, id string) (lane string, k map[string]any) {
	for _, l := range board["sprints"].([]any) {
		lm := l.(map[string]any)
		for _, c := range lm["cards"].([]any) {
			if cm := c.(map[string]any); cm["id"] == id {
				return lm["id"].(string), cm
			}
		}
	}
	return "", nil
}

func termin827(board map[string]any, id string) map[string]any {
	for _, t := range board["termine"].([]any) {
		if tm := t.(map[string]any); tm["id"] == id {
			return tm
		}
	}
	return nil
}

func kommentare827(k map[string]any) []map[string]any {
	var aus []map[string]any
	if l, ok := k["comments"].([]any); ok {
		for _, c := range l {
			aus = append(aus, c.(map[string]any))
		}
	}
	return aus
}

func feld827(opID, id, stelle string, vorher, nachher any) map[string]any {
	return map[string]any{"op_id": opID, "objekt": "karte", "id": id, "art": "feld", "stelle": stelle,
		"vorher": vorher, "nachher": nachher}
}

func TestBoardpaar827_AnwendenFeldUndKonflikt(t *testing.T) {
	s := neueInnenSeite827(t)
	// angewendet, mit Akteur „boardpaar: <person>“ und actor_kind system.
	s.erwarte(t, feld827("bp-1", "k1", "title", "Angebot Müller", "Angebot Müller GmbH"), "angewendet", "")
	if _, k := karte827(s.stand(t), "k1"); k["title"] != "Angebot Müller GmbH" {
		t.Fatalf("Titel: %v", k["title"])
	}
	akt, _ := storage.NewActivityRepo(s.db).List(context.Background(), "b-innen", 10)
	if len(akt) == 0 || akt[0].Actor != "boardpaar: gerd@team.example" || akt[0].ActorKind != "system" || akt[0].OpType != "card.fieldIf" {
		t.Fatalf("Aktivität: %+v", akt)
	}
	// Wiederholung → schon-angewendet; dieselbe op_id mit anderem Inhalt → 409.
	s.erwarte(t, feld827("bp-1", "k1", "title", "Angebot Müller", "Angebot Müller GmbH"), "schon-angewendet", "")
	if code, m := s.anwenden(t, feld827("bp-1", "k1", "title", "Angebot Müller", "Anders")); code != 409 {
		t.Fatalf("op_id wiederverwendet: %d %v", code, m)
	}
	// Innen steht schon derselbe Wert (vorher passt nicht): angewendet, kein Kommentar.
	s.erwarte(t, feld827("bp-2", "k1", "title", "Alt", "Angebot Müller GmbH"), "angewendet", "")
	// Konflikt: innen anders als vorher → Kommentar „<Op-ID>-k“.
	s.erwarte(t, feld827("bp-3", "k1", "title", "Alt", "Außen neu"), "konflikt", "")
	_, k := karte827(s.stand(t), "k1")
	if k["title"] != "Angebot Müller GmbH" {
		t.Fatalf("Konflikt hat geschrieben: %v", k["title"])
	}
	ks := kommentare827(k)
	letzter := ks[len(ks)-1]
	text, _ := letzter["text"].(string)
	if letzter["author"] != api.BoardpaarKonfliktAutor || letzter["authorEmail"] != nil ||
		!strings.Contains(text, "gerd@team.example hat außen am 14.10.2026 08:15 UTC den Titel geändert auf: Außen neu.") ||
		!strings.Contains(text, "es gilt die innere Fassung") || letzter["id"] != schatten.AbgeleiteteID("konflikt:bp-3") {
		t.Fatalf("Konflikt-Kommentar: %v", letzter)
	}
	// Wiederholung: konflikt, ohne die Bedingung erneut zu prüfen — auch wenn
	// innen inzwischen wieder „Alt“ steht (sonst widerspräche das Ergebnis
	// dem Kommentar).
	s.erwarte(t, feld827("bp-4", "k1", "title", "Angebot Müller GmbH", "Alt"), "angewendet", "")
	s.erwarte(t, feld827("bp-3", "k1", "title", "Alt", "Außen neu"), "konflikt", "")
	if _, k := karte827(s.stand(t), "k1"); k["title"] != "Alt" || len(kommentare827(k)) != len(ks) {
		t.Fatalf("Wiederholung des Konflikts hat geschrieben: %v, %d Kommentare", k["title"], len(kommentare827(k)))
	}
	// Dieselbe op_id mit anderem Eintrag, nachdem der Konflikt gemerkt ist: 409.
	if code, m := s.anwenden(t, feld827("bp-3", "k1", "title", "Alt", "Noch anders")); code != 409 {
		t.Fatalf("op_id des Konflikts wiederverwendet: %d %v", code, m)
	}
	// Feld außerhalb der Feldliste, vertraulich gewordene und fehlende Karte.
	s.erwarte(t, feld827("bp-5", "k1", "qa", false, true), "verworfen", "feld-nicht-erlaubt")
	s.erwarte(t, feld827("bp-6", "k2", "title", "Geheim", "x"), "verworfen", "vertraulich-geworden")
	s.erwarte(t, feld827("bp-7", "k-weg", "title", "a", "b"), "verworfen", "karte-weg")
	if _, k := karte827(s.stand(t), "k2"); len(kommentare827(k)) != 0 {
		t.Fatal("vertraulich-geworden darf keinen Kommentar schreiben (§9)")
	}
	// Regelverletzung des Folds (wirkungslos) → 422: am Termin-Datum geprüft
	// (TestBoardpaar827_AnwendenAngelegtUndTermine, t-8); Kartendaten prüft der
	// Fold nicht.
	// Entfernt ist nie erlaubt.
	s.erwarte(t, map[string]any{"op_id": "bp-9", "objekt": "karte", "id": "k1", "art": "entfernt", "stelle": ""},
		"verworfen", "loeschen-nicht-erlaubt")
}

func TestBoardpaar827_AnwendenOrtListenCheckliste(t *testing.T) {
	s := neueInnenSeite827(t)
	ort := func(lane, spalte string, nach any) map[string]any {
		return map[string]any{"lane": lane, "spalte": spalte, "nach": nach}
	}
	verschoben := func(op, id string, v, n map[string]any) map[string]any {
		return map[string]any{"op_id": op, "objekt": "karte", "id": id, "art": "verschoben", "stelle": "ort", "vorher": v, "nachher": n}
	}
	s.erwarte(t, verschoben("o-1", "k1", ort("sprint-1", "col-ready", nil), ort("sprint-1", "spalte-review", nil)), "angewendet", "")
	if l, k := karte827(s.stand(t), "k1"); l != "sprint-1" || k["columnId"] != "spalte-review" {
		t.Fatalf("verschoben: %s %v", l, k["columnId"])
	}
	// Erwarteter Ort passt nicht mehr → Konflikt mit Lane/Spalte im Kommentar.
	s.erwarte(t, verschoben("o-2", "k1", ort("sprint-1", "col-ready", nil), ort("__backlog__", "col-backlog", "k3")), "konflikt", "")
	_, k := karte827(s.stand(t), "k1")
	ks := kommentare827(k)
	if text, _ := ks[len(ks)-1]["text"].(string); !strings.Contains(text, "Lane und Spalte geändert auf: Backlog / Backlog") {
		t.Fatalf("Konflikt-Text: %q", text)
	}
	s.erwarte(t, verschoben("o-3", "k1", ort("sprint-1", "spalte-review", nil), ort("lane-gibt-es-nicht", "col-backlog", nil)),
		"verworfen", "lane-unbekannt")
	s.erwarte(t, verschoben("o-4", "k1", ort("sprint-1", "spalte-review", nil), ort("__backlog__", "spalte-gibt-es-nicht", nil)),
		"verworfen", "spalte-unbekannt")
	// Mit Anker hinter k3 in den Backlog.
	s.erwarte(t, verschoben("o-5", "k1", ort("sprint-1", "spalte-review", nil), ort("__backlog__", "col-backlog", "k3")), "angewendet", "")
	b := s.stand(t)
	for _, l := range b["sprints"].([]any) {
		if lm := l.(map[string]any); lm["id"] == "__backlog__" {
			cards := lm["cards"].([]any)
			if len(cards) != 2 || cards[1].(map[string]any)["id"] != "k1" {
				t.Fatalf("Anker: %v", cards)
			}
		}
	}
	// Reihenfolge: an den Anfang (nach = null), ohne Bedingung.
	s.erwarte(t, map[string]any{"op_id": "o-6", "objekt": "karte", "id": "k1", "art": "reihenfolge", "stelle": "reihenfolge",
		"vorher": map[string]any{"nach": "k3"}, "nachher": map[string]any{"nach": nil}}, "angewendet", "")
	for _, l := range s.stand(t)["sprints"].([]any) {
		if lm := l.(map[string]any); lm["id"] == "__backlog__" {
			if cards := lm["cards"].([]any); cards[0].(map[string]any)["id"] != "k1" {
				t.Fatalf("reihenfolge: %v", cards)
			}
		}
	}
	// Listen: Bearbeiter nur aus den Identitäten innen; Labels frei.
	liste := func(op, art, stelle string, wert any) map[string]any {
		e := map[string]any{"op_id": op, "objekt": "karte", "id": "k1", "art": art, "stelle": stelle}
		if art == "liste.add" {
			e["nachher"] = wert
		} else {
			e["vorher"] = wert
		}
		return e
	}
	s.erwarte(t, liste("l-1", "liste.add", "assignees:Fremd", "Fremd"), "verworfen", "bearbeiter-unbekannt")
	s.erwarte(t, liste("l-2", "liste.add", "assignees:Gerd Muster", "Gerd Muster"), "angewendet", "")
	s.erwarte(t, liste("l-3", "liste.add", "labels:Kunde", "Kunde"), "angewendet", "")
	s.erwarte(t, liste("l-4", "liste.remove", "labels:Kunde", "Kunde"), "angewendet", "")
	if code, m := s.anwenden(t, liste("l-5", "liste.add", "labels:A", "B")); code != 422 {
		t.Fatalf("Stelle und Wert verschieden: %d %v", code, m)
	}
	if _, k := karte827(s.stand(t), "k1"); len(k["assignees"].([]any)) != 1 || len(k["labels"].([]any)) != 0 {
		t.Fatalf("Listen: %v %v", k["assignees"], k["labels"])
	}
	// Checkliste: genau ein Punkt.
	punkt := func(op string, vorher, nachher bool) map[string]any {
		return map[string]any{"op_id": op, "objekt": "karte", "id": "k1", "art": "checkpunkt.feld", "stelle": "checkpunkt:1:Termin",
			"vorher":  map[string]any{"text": "Termin", "vorkommen": 1, "erledigt": vorher},
			"nachher": map[string]any{"text": "Termin", "vorkommen": 1, "erledigt": nachher}}
	}
	s.erwarte(t, punkt("p-1", false, true), "angewendet", "")
	if _, k := karte827(s.stand(t), "k1"); k["description"] != "- [ ] Preis\n- [x] Termin" {
		t.Fatalf("Checkliste: %q", k["description"])
	}
	s.erwarte(t, punkt("p-2", false, true), "angewendet", "") // steht schon so
	s.erwarte(t, punkt("p-3", true, false), "angewendet", "")
	// Steht schon so (innen offen, außen offen gesetzt): angewendet, kein Konflikt.
	s.erwarte(t, map[string]any{"op_id": "p-4", "objekt": "karte", "id": "k1", "art": "checkpunkt.feld", "stelle": "checkpunkt:1:Preis",
		"vorher":  map[string]any{"text": "Preis", "vorkommen": 1, "erledigt": true},
		"nachher": map[string]any{"text": "Preis", "vorkommen": 1, "erledigt": false}}, "angewendet", "")
	// Punkt gibt es innen nicht (mehr): Konflikt.
	s.erwarte(t, map[string]any{"op_id": "p-5", "objekt": "karte", "id": "k1", "art": "checkpunkt.feld", "stelle": "checkpunkt:1:Angebot",
		"vorher":  map[string]any{"text": "Angebot", "vorkommen": 1, "erledigt": false},
		"nachher": map[string]any{"text": "Angebot", "vorkommen": 1, "erledigt": true}}, "konflikt", "")
	_, k = karte827(s.stand(t), "k1")
	ks = kommentare827(k)
	if text, _ := ks[len(ks)-1]["text"].(string); !strings.Contains(text, "den Checklisten-Punkt „Angebot“ geändert auf: erledigt") {
		t.Fatalf("Konflikt-Text Checkliste: %q", text)
	}
}

func TestBoardpaar827_AnwendenKommentareUndVertraulich(t *testing.T) {
	s := neueInnenSeite827(t)
	s.erwarte(t, map[string]any{"op_id": "c-1", "objekt": "karte", "id": "k1", "art": "kommentar.add", "stelle": "kommentar:c-aussen",
		"nachher": map[string]any{"id": "c-aussen", "author": "Max Extern", "text": "Von außen", "created": "2026-10-14T08:00:00Z"}},
		"angewendet", "")
	_, k := karte827(s.stand(t), "k1")
	ks := kommentare827(k)
	if c := ks[len(ks)-1]; c["id"] != "c-aussen" || c["author"] != "extern: Max Extern" || c["authorEmail"] != nil || c["text"] != "Von außen" {
		t.Fatalf("kommentar.add: %v", c)
	}
	edit := func(op, id, vorher, nachher string) map[string]any {
		return map[string]any{"op_id": op, "objekt": "karte", "id": "k1", "art": "kommentar.edit", "stelle": "kommentar:" + id,
			"vorher": vorher, "nachher": nachher}
	}
	s.erwarte(t, edit("c-2", "c-aussen", "Von außen", "Von außen, korrigiert"), "angewendet", "")
	s.erwarte(t, edit("c-3", "c-innen", "Hallo", "Gekapert"), "verworfen", "kommentar-nicht-erlaubt")
	s.erwarte(t, edit("c-4", "c-aussen", "Veraltet", "Neu"), "konflikt", "")
	_, k = karte827(s.stand(t), "k1")
	for _, c := range kommentare827(k) {
		if c["id"] == "c-innen" && c["text"] != "Hallo" {
			t.Fatal("Kommentar von innen bearbeitet")
		}
		if c["id"] == "c-aussen" && c["text"] != "Von außen, korrigiert" {
			t.Fatalf("Kommentar von außen: %v", c["text"])
		}
	}
	// Vertraulich: nur true; danach ist die Karte für den Rückweg zu.
	if code, m := s.anwenden(t, map[string]any{"op_id": "v-0", "objekt": "karte", "id": "k4", "art": "vertraulich",
		"stelle": "confidential", "vorher": true, "nachher": false}); code != 422 {
		t.Fatalf("vertraulich false: %d %v", code, m)
	}
	s.erwarte(t, map[string]any{"op_id": "v-1", "objekt": "karte", "id": "k4", "art": "vertraulich", "stelle": "confidential",
		"vorher": false, "nachher": true}, "angewendet", "")
	if _, k := karte827(s.stand(t), "k4"); k["confidential"] != true {
		t.Fatalf("vertraulich: %v", k["confidential"])
	}
	s.erwarte(t, feld827("v-2", "k4", "title", "Zweite", "x"), "verworfen", "vertraulich-geworden")
}

func TestBoardpaar827_AnwendenAngelegtUndTermine(t *testing.T) {
	s := neueInnenSeite827(t)
	aussenID := "4a1b2c3d-0000-4000-8000-00000000a827"
	innenID := schatten.AbgeleiteteID(aussenID)
	anlage := func(op, id, ursprung string) map[string]any {
		return map[string]any{"op_id": op, "objekt": "karte", "id": id, "art": "angelegt", "stelle": "", "ursprung": ursprung,
			"nachher": map[string]any{"lane": "sprint-1", "spalte": "col-ready", "nach": "k1", "title": "Neu von außen",
				"description": "Text", "priority": "hoch", "critical": false, "blocked": false, "owner": "", "startDate": nil,
				"startTime": nil, "dueDate": "2026-11-01", "dueTime": nil, "archived": false, "labels": []string{"Kunde"},
				"assignees": []string{"Gerd Muster", "Fremd"}, "vertraulich": false,
				"comments": []map[string]any{{"id": "c-neu", "author": "Max", "text": "Erster", "created": "2026-10-14T08:00:00Z"}}}}
	}
	if code, m := s.anwenden(t, anlage("a-0", "falsche-id", aussenID)); code != 422 {
		t.Fatalf("id nicht abgeleitet: %d %v", code, m)
	}
	s.erwarte(t, anlage("a-1", innenID, aussenID), "angewendet", "")
	b := s.stand(t)
	lane, k := karte827(b, innenID)
	if lane != "sprint-1" || k["boardpaarUrsprung"] != aussenID || k["title"] != "Neu von außen" || k["confidential"] != nil ||
		k["displayId"] == nil || len(k["assignees"].([]any)) != 1 || k["dueDate"] != "2026-11-01" {
		t.Fatalf("angelegt: %s %v", lane, k)
	}
	if c := kommentare827(k)[0]; c["author"] != "extern: Max" || c["authorEmail"] != nil {
		t.Fatalf("Kommentar der Anlage: %v", c)
	}
	// Idempotent auch unter neuer Op-ID (nach Ablauf von op_ids, §2).
	s.erwarte(t, anlage("a-2", innenID, aussenID), "schon-angewendet", "")

	// Termine: angelegt, Feld, Konflikt an die Karte des Termins, ohne Karte
	// ohne Kommentar, vertraulich.
	tAussen := "4a1b2c3d-0000-4000-8000-00000000b827"
	tInnen := schatten.AbgeleiteteID(tAussen)
	s.erwarte(t, map[string]any{"op_id": "t-1", "objekt": "termin", "id": tInnen, "art": "angelegt", "stelle": "", "ursprung": tAussen,
		"nachher": map[string]any{"titel": "Workshop", "beschreibung": nil, "datum": "2026-10-30", "bisDatum": nil, "von": "10:00",
			"bis": nil, "ort": "Raum 1", "personen": []string{"Andi", "Fremd"}, "karteId": "k1", "vertraulich": false}}, "angewendet", "")
	tm := termin827(s.stand(t), tInnen)
	if tm == nil || tm["boardpaarUrsprung"] != tAussen || tm["titel"] != "Workshop" || len(tm["personen"].([]any)) != 1 {
		t.Fatalf("Termin angelegt: %v", tm)
	}
	terminFeld := func(op, id, stelle string, vorher, nachher any) map[string]any {
		return map[string]any{"op_id": op, "objekt": "termin", "id": id, "art": "termin.feld", "stelle": stelle,
			"vorher": vorher, "nachher": nachher}
	}
	s.erwarte(t, terminFeld("t-2", "t1", "ort", nil, "Halle"), "angewendet", "")
	s.erwarte(t, terminFeld("t-3", "t1", "titel", "Alt", "Neu"), "konflikt", "")
	_, k1 := karte827(s.stand(t), "k1")
	ks := kommentare827(k1)
	if text, _ := ks[len(ks)-1]["text"].(string); !strings.Contains(text, "den Titel des Termins geändert auf: Neu") {
		t.Fatalf("Konflikt am Termin: %q", text)
	}
	vorher := len(ks)
	s.erwarte(t, terminFeld("t-4", "t2", "titel", "Alt", "Neu"), "konflikt", "")
	if _, k1 := karte827(s.stand(t), "k1"); len(kommentare827(k1)) != vorher {
		t.Fatal("Termin ohne Karte: kein Kommentar erwartet")
	}
	s.erwarte(t, terminFeld("t-5", "t1", "personen", nil, []string{"Fremd"}), "verworfen", "bearbeiter-unbekannt")
	s.erwarte(t, terminFeld("t-6", "t1", "erstelltVon", nil, "x"), "verworfen", "feld-nicht-erlaubt")
	s.erwarte(t, terminFeld("t-7", "t-weg", "titel", "a", "b"), "verworfen", "termin-weg")
	if code, m := s.anwenden(t, terminFeld("t-8", "t1", "datum", "2026-10-20", "kein-datum")); code != 422 {
		t.Fatalf("Termin-Datum ungültig (wirkungslos): %d %v", code, m)
	}
	s.erwarte(t, map[string]any{"op_id": "t-9", "objekt": "termin", "id": "t2", "art": "vertraulich", "stelle": "confidential",
		"vorher": false, "nachher": true}, "angewendet", "")
	if tm := termin827(s.stand(t), "t2"); tm["confidential"] != true || tm["titel"] != "Ohne Karte" {
		t.Fatalf("Termin vertraulich: %v", tm)
	}
	s.erwarte(t, terminFeld("t-10", "t2", "titel", "Ohne Karte", "x"), "verworfen", "vertraulich-geworden")
}

func TestBoardpaar827_AnwendenWegeUndEintrag(t *testing.T) {
	s := neueInnenSeite827(t)
	ctx := context.Background()
	paar := storage.NewBoardpaarRepo(s.db)
	abs, _, _ := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	gut := feld827("w-1", "k1", "title", "Angebot Müller", "x")
	// Fremder Zugang: Die Schranke lässt abs_ nicht auf diesen Weg.
	if code, _ := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-innen", abs, gut); code != 403 {
		t.Fatalf("abs_: %d", code)
	}
	if code, _ := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-innen", "", gut); code != 401 {
		t.Fatalf("ohne Token: %d", code)
	}
	// Fähigkeit im Hinweg (Plan D11).
	if code, b := anfrage825(t, s.srv, "GET", "/agent/boardpaar/spiegel/b-innen?format=3", abs, nil); code != 200 ||
		!strings.Contains(string(b), `"faehigkeiten":["anwenden"]`) {
		t.Fatalf("hinweg: %d %.300s", code, b)
	}
	// Nicht gekoppeltes Board: 404.
	if code, _ := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-fremd", s.abr, gut); code != 404 {
		t.Fatalf("nicht gekoppelt: %d", code)
	}
	// Fehlerhafte Einträge: 422.
	for name, e := range map[string]map[string]any{
		"unbekannter Schlüssel": {"op_id": "x", "objekt": "karte", "id": "k1", "art": "feld", "stelle": "title", "kennung": "K"},
		"op_id zu lang":         feld827(strings.Repeat("x", 127), "k1", "title", "a", "b"),
		"objekt":                {"op_id": "x", "objekt": "lane", "id": "k1", "art": "feld", "stelle": "title"},
		"art am Termin":         {"op_id": "x", "objekt": "termin", "id": "t1", "art": "feld", "stelle": "titel"},
		"ursprung ohne angelegt": {"op_id": "x", "objekt": "karte", "id": "k1", "art": "feld", "stelle": "title",
			"vorher": "a", "nachher": "b", "ursprung": "u"},
	} {
		if code, m := s.anwenden(t, e); code != 422 {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	// Seite außen: der Weg gibt es dort nicht.
	if err := storage.NewConfigRepo(s.db, nil, uuidString).Set(ctx, api.KonfigBoardpaarSeite, "aussen", false, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-innen", s.abr, gut); code != 404 {
		t.Fatalf("Seite außen: %d", code)
	}
}

// Kritik E3b K-1, K-2, K-4, K-5, K-7, K-8: Nacharbeit.
func TestBoardpaar827_AnwendenNacharbeitKritik(t *testing.T) {
	s := neueInnenSeite827(t)
	terminFeld := func(op, id, stelle string, vorher, nachher any) map[string]any {
		return map[string]any{"op_id": op, "objekt": "termin", "id": id, "art": "termin.feld", "stelle": stelle,
			"vorher": vorher, "nachher": nachher}
	}
	// K-1: Bezug auf eine vertrauliche oder fehlende Karte ist verworfen.
	s.erwarte(t, terminFeld("n-1", "t2", "karteId", nil, "k2"), "verworfen", "vertraulich-geworden")
	s.erwarte(t, terminFeld("n-2", "t2", "karteId", nil, "k-weg"), "verworfen", "karte-weg")
	s.erwarte(t, terminFeld("n-3", "t2", "karteId", nil, "k4"), "angewendet", "")
	// K-1: Termin wirksam vertraulich über seine Karte.
	s.erwarte(t, map[string]any{"op_id": "n-4", "objekt": "karte", "id": "k1", "art": "vertraulich", "stelle": "confidential",
		"vorher": false, "nachher": true}, "angewendet", "")
	s.erwarte(t, terminFeld("n-5", "t1", "ort", nil, "Halle"), "verworfen", "vertraulich-geworden")
	if tm := termin827(s.stand(t), "t1"); tm["ort"] != nil {
		t.Fatalf("Termin mit vertraulicher Karte geändert: %v", tm)
	}
	// K-1: angelegter Termin mit Bezug auf eine vertrauliche Karte → ohne Bezug.
	tAussen := "4a1b2c3d-0000-4000-8000-00000000c827"
	tInnen := schatten.AbgeleiteteID(tAussen)
	s.erwarte(t, map[string]any{"op_id": "n-6", "objekt": "termin", "id": tInnen, "art": "angelegt", "stelle": "", "ursprung": tAussen,
		"nachher": map[string]any{"titel": "W", "datum": "2026-10-30", "karteId": "k2"}}, "angewendet", "")
	if tm := termin827(s.stand(t), tInnen); tm == nil || tm["karteId"] != nil {
		t.Fatalf("angelegter Termin trägt Bezug auf vertrauliche Karte: %v", tm)
	}
	// K-2: Typen der Feldliste.
	for name, e := range map[string]map[string]any{
		"titel objekt":  feld827("n-7", "k4", "title", "Zweite", map[string]any{"a": 1}),
		"critical text": feld827("n-8", "k4", "critical", false, "ja"),
		"datum":         feld827("n-9", "k4", "dueDate", nil, "kein-datum"),
		"uhrzeit":       feld827("n-10", "k4", "dueTime", nil, "9 Uhr"),
	} {
		if code, m := s.anwenden(t, e); code != 422 {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	s.erwarte(t, feld827("n-11", "k4", "dueDate", nil, "2026-11-02"), "angewendet", "")
	s.erwarte(t, feld827("n-11b", "k4", "description", "", nil), "angewendet", "") // null erlaubt (Verify V-5)
	// Sitzungs-QA A-4: Titel null → leerer Text, kein 422.
	s.erwarte(t, feld827("n-11d", "k4", "title", "Zweite", nil), "angewendet", "")
	if _, k := karte827(s.stand(t), "k4"); k["title"] != "" {
		t.Fatalf("Titel null: %v", k["title"])
	}
	// Sitzungs-QA A-2: created nur als Text, sonst weggelassen.
	s.erwarte(t, map[string]any{"op_id": "n-11e", "objekt": "karte", "id": "k4", "art": "kommentar.add", "stelle": "kommentar:c-zeit",
		"nachher": map[string]any{"id": "c-zeit", "author": "M", "text": "t", "created": map[string]any{"a": 1}}}, "angewendet", "")
	_, k4 := karte827(s.stand(t), "k4")
	for _, c := range kommentare827(k4) {
		if c["id"] == "c-zeit" && c["created"] != nil {
			t.Fatalf("created als Objekt übernommen: %v", c["created"])
		}
	}
	// Verify V-1: Typprüfung auch bei angelegt.
	falsch := "4a1b2c3d-0000-4000-8000-00000000d827"
	if code, m := s.anwenden(t, map[string]any{"op_id": "n-11c", "objekt": "karte", "id": schatten.AbgeleiteteID(falsch),
		"art": "angelegt", "stelle": "", "ursprung": falsch, "nachher": map[string]any{"lane": "sprint-1", "spalte": "col-ready",
			"nach": nil, "title": map[string]any{"a": 1}, "labels": []string{}, "assignees": []string{}}}); code != 422 {
		t.Fatalf("angelegt mit falschem Typ: %d %v", code, m)
	}
	// K-4: Kommentar-ID belegt.
	add := func(op, text string) map[string]any {
		return map[string]any{"op_id": op, "objekt": "karte", "id": "k4", "art": "kommentar.add", "stelle": "kommentar:c-x",
			"nachher": map[string]any{"id": "c-x", "author": "Max", "text": text}}
	}
	s.erwarte(t, add("n-12", "Eins"), "angewendet", "")
	s.erwarte(t, add("n-13", "Eins"), "angewendet", "")
	s.erwarte(t, add("n-14", "Zwei"), "verworfen", "kommentar-id-belegt")
	// K-5: bearbeiteter Kommentar fehlt.
	s.erwarte(t, map[string]any{"op_id": "n-15", "objekt": "karte", "id": "k4", "art": "kommentar.edit", "stelle": "kommentar:c-weg",
		"vorher": "a", "nachher": "b"}, "verworfen", "kommentar-weg")
	// K-8: Termin-Konflikt ohne Karte — eine Wiederholung prüft erneut.
	s.erwarte(t, terminFeld("n-16", "t2", "titel", "Alt", "Neu"), "konflikt", "")
	s.erwarte(t, terminFeld("n-16", "t2", "titel", "Alt", "Neu"), "konflikt", "")
	// K-7: Daten nach dem Eintrag → 422; zu großer Rumpf → 413.
	req, _ := http.NewRequest("POST", s.srv.URL+"/agent/boardpaar/anwenden/b-innen",
		strings.NewReader(`{"op_id":"n-17","objekt":"karte","id":"k4","art":"entfernt","stelle":"","person":"p"} {"x":1}`))
	req.Header.Set("Authorization", "Bearer "+s.abr)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 422 {
		t.Fatalf("Daten nach dem Eintrag: %v %v", err, resp)
	}
	gross := feld827("n-18", "k4", "description", "", strings.Repeat("x", 1<<20))
	if code, _ := anfrage825(t, s.srv, "POST", "/agent/boardpaar/anwenden/b-innen", s.abr, gross); code != 413 {
		t.Fatalf("zu groß: %d", code)
	}
}
