package api_test

// ADR-0136 (Karte #693): Umgehungswege des Löschrechts, die der Kritiker über
// e29ae330 belegt hat (F-1611, F-1612, F-1613, F-1615). Jeder Fall lief
// damals mit 200 durch und entfernte oder fälschte einen fremden Kommentar.
// Fixture: newEnvLoesch (kommentar_loeschrecht_test.go) — Karte k1 mit dem
// Altkommentar alt1 ohne Stempel; anna/bert editor, olga owner.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const altKommentar = `{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01"}`

// altUnveraendert: alt1 steht noch, ohne Stempel, mit dem alten Text.
func altUnveraendert(t *testing.T, e *envLoesch, fall string) {
	t.Helper()
	k := e.kommentar(t, "alt1")
	if k == nil || k["text"] != "alter Text" || k["author"] != "Altbestand" {
		t.Errorf("%s: alt1 entfernt oder geändert: %v", fall, k)
		return
	}
	for schl := range k {
		if strings.EqualFold(schl, "authorEmail") {
			t.Errorf("%s: alt1 trägt jetzt eine Urheberschaft: %v", fall, k)
		}
	}
	if karte := e.karte(t); len(karte) > 0 {
		for schl := range karte {
			if schl != "comments" && strings.EqualFold(schl, "comments") {
				t.Errorf("%s: Schattenschlüssel %q an der Karte", fall, schl)
			}
		}
	}
}

// F-1611: Kein Op-Weg entfernt oder fälscht einen fremden Kommentar.
func TestKommentarLoeschrecht_UmgehungUeberOps(t *testing.T) {
	faelle := map[string]string{
		"card.fieldIf leert":        `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"comments","expect":[` + altKommentar + `],"value":[]}}]`,
		"card.fieldIf stempelt":     `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"comments","expect":[` + altKommentar + `],"value":[{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01","authorEmail":"anna@firma.de"}]}}]`,
		"card.listRemove":           `[{"type":"card.listRemove","payload":{"cardId":"k1","field":"comments","item":` + altKommentar + `}}]`,
		"card.field Zeichenkette":   `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":"x"}}]`,
		"card.field ohne value":     `[{"type":"card.field","payload":{"cardId":"k1","field":"comments"}}]`,
		"card.field null":           `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":null}}]`,
		"card.field Nicht-Objekt":   `[{"type":"card.field","payload":{"cardId":"k1","field":"comments","value":["x"]}}]`,
		"card.upsert Zeichenkette":  `[{"type":"card.upsert","payload":{"card":{"id":"k1","title":"T","columnId":"col-ready","comments":"x"}}}]`,
		"card.upsert Objekt":        `[{"type":"card.upsert","payload":{"card":{"id":"k1","title":"T","columnId":"col-ready","comments":{}}}}]`,
		"card.upsert ohne comments": `[{"type":"card.upsert","payload":{"card":{"id":"k1","title":"T","columnId":"col-ready"}}}]`,
		"card.upsert Comments":      `[{"type":"card.upsert","payload":{"card":{"id":"k1","title":"T","columnId":"col-ready","Comments":[]}}}]`,
		"card.upsert Nicht-Objekt":  `[{"type":"card.upsert","payload":{"card":{"id":"k1","title":"Karte eins","displayId":1,"columnId":"col-ready","activity":[],"comments":["x"]}}}]`,
		// Karte löschen und im selben Batch mit derselben id ohne den fremden
		// Kommentar neu anlegen: Die Karte gibt es danach — die Regel gilt.
		"card.delete + Neuanlage": `[{"type":"card.delete","payload":{"cardId":"k1"}},` +
			`{"type":"card.upsert","payload":{"card":{"id":"k1","title":"Karte eins","columnId":"col-ready","comments":[]},"sprintId":"s1"}}]`,
	}
	// Hier stempelt der Server den mitgeschickten authorEmail weg (Stand:
	// keiner) — die Op bleibt wirkungslos, 200 ist dann richtig.
	wirkungslos := map[string]bool{"card.fieldIf stempelt": true}
	for fall, ops := range faelle {
		t.Run(fall, func(t *testing.T) {
			e := newEnvLoesch(t)
			code, b := e.ops(t, "anna@firma.de", ops)
			if code == http.StatusOK && !wirkungslos[fall] {
				t.Errorf("%s: 200, erwartet Ablehnung (%s)", fall, b)
			}
			altUnveraendert(t, e, fall)
		})
	}
}

// F-1611 (KL-2, doppelte id): Ein Kommentar mit derselben id an einer
// anderen Karte darf die Urheberschaft von alt1 nicht auf den Schreiber
// umstempeln.
func TestKommentarLoeschrecht_DoppelteIdStempeltNichtUm(t *testing.T) {
	e := newEnvLoesch(t)
	vorbereitung := `[{"type":"card.upsert","payload":{"card":{"id":"k2","title":"Z","columnId":"col-ready","comments":[]},"sprintId":"s1"}},` +
		`{"type":"comment.add","payload":{"cardId":"k2","comment":{"id":"alt1","author":"A","text":"x","created":"2026-10-04"}}}]`
	if code, b := e.ops(t, "anna@firma.de", vorbereitung); code != 200 {
		t.Fatalf("Vorbereitung: %d %s", code, b)
	}
	// Vollständige Karte, damit keine andere Prüfung (Entkernung) vorgreift;
	// mitgeschickt wird Annas Stempel an alt1.
	code, b := e.ops(t, "anna@firma.de", `[{"type":"card.upsert","payload":{"card":{"id":"k1","displayId":1,"title":"Karte eins","columnId":"col-ready","activity":[],"comments":[`+
		`{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01","authorEmail":"anna@firma.de"}]}}}]`)
	// Der Server stempelt alt1 mit dem Wert derselben Karte (keiner) — die Op
	// ist dann wirkungslos (200) oder abgelehnt; umgestempelt wird nie.
	if code != http.StatusOK && code != 422 {
		t.Errorf("doppelte id: %d %s", code, b)
	}
	altUnveraendert(t, e, "doppelte id")
}

// F-1611 (KL-5): Schattenschlüssel `Comments` — erst anlegen, dann wäre
// jeder Kommentar „frei“. Beides wird abgelehnt.
func TestKommentarLoeschrecht_Schattenschluessel(t *testing.T) {
	e := newEnvLoesch(t)
	code1, b1 := e.ops(t, "anna@firma.de", `[{"type":"card.field","payload":{"cardId":"k1","field":"Comments","value":[]}}]`)
	if code1 != 422 || !strings.Contains(b1, "schattenschluessel") {
		t.Errorf("card.field Comments: %d %s, erwartet 422 schattenschluessel", code1, b1)
	}
	code2, b2 := e.ops(t, "anna@firma.de", kommentarDelete("alt1"))
	if code2 != 422 {
		t.Errorf("Löschen danach: %d %s, erwartet 422", code2, b2)
	}
	altUnveraendert(t, e, "Schattenschlüssel")
}

// F-1611 (KL-6): Ein Typfehler in einem anderen Feld schaltet die Prüfung
// nicht ab (früher: Go-Struct unlesbar → fail-open).
func TestKommentarLoeschrecht_TypfehlerSchaltetNichtAb(t *testing.T) {
	e := newEnvLoesch(t)
	e.ops(t, "anna@firma.de", `[{"type":"card.field","payload":{"cardId":"k1","field":"title","value":123}}]`)
	code, b := e.ops(t, "anna@firma.de", kommentarDelete("alt1"))
	if code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Errorf("Löschen nach title:123: %d %s, erwartet 422 fremder-kommentar", code, b)
	}
	altUnveraendert(t, e, "Typfehler")
}

// F-1613: comment.edit eines fremden Kommentars — nur Owner/Admin; eigene
// Kommentare bleiben bearbeitbar.
func TestKommentarLoeschrecht_FremdenKommentarBearbeiten(t *testing.T) {
	e := newEnvLoesch(t)
	edit := func(id, text string) string {
		return `[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"` + id + `","text":"` + text + `"}}]`
	}
	if code, b := e.ops(t, "anna@firma.de", edit("alt1", "")); code != 422 || !strings.Contains(b, "fremder-kommentar") {
		t.Errorf("editor leert fremden Kommentar: %d %s, erwartet 422", code, b)
	}
	altUnveraendert(t, e, "comment.edit")
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	if code, b := e.ops(t, "anna@firma.de", edit("a1", "neu von Anna")); code != 200 {
		t.Errorf("eigenen bearbeiten: %d %s", code, b)
	}
	if code, b := e.ops(t, "bert@firma.de", edit("a1", "Bert war hier")); code != 422 {
		t.Errorf("Bert bearbeitet Annas Kommentar: %d %s, erwartet 422", code, b)
	}
	if code, b := e.ops(t, "olga@firma.de", edit("alt1", "vom Owner berichtigt")); code != 200 {
		t.Errorf("Owner bearbeitet fremden: %d %s", code, b)
	}
}

// Owner entfernt einen fremden Kommentar über einen anderen Weg als
// comment.delete (card.fieldIf): Verlauf und Hinweis entstehen trotzdem —
// sie hängen am Ergebnis, nicht am Op-Typ.
func TestKommentarLoeschrecht_OwnerUeberFieldIfMitVerlauf(t *testing.T) {
	e := newEnvLoesch(t)
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	stand, _ := json.Marshal(e.karte(t)["comments"])
	ops := `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"comments","expect":` + string(stand) + `,"value":[` + altKommentar + `]}}]`
	if code, b := e.ops(t, "olga@firma.de", ops); code != 200 {
		t.Fatalf("Owner per fieldIf: %d %s", code, b)
	}
	if e.kommentar(t, "a1") != nil {
		t.Fatal("a1 steht noch")
	}
	act, _ := e.karte(t)["activity"].([]any)
	if len(act) != 1 || act[0].(map[string]any)["action"] != "comment-deleted-by-owner" || act[0].(map[string]any)["from"] != "anna@firma.de" {
		t.Fatalf("Verlauf: %v", act)
	}
	if len(e.hinweise) != 1 || e.hinweise[0].AutorEmail != "anna@firma.de" {
		t.Fatalf("Hinweis: %+v", e.hinweise)
	}
}

// F-1615: Eine KI-Antwort von vor ADR-0136 (ohne authorEmail, mit dem
// Server-Label) darf die Person verwerfen, die gefragt hat — niemand sonst.
func TestKommentarLoeschrecht_AlteKIAntwort(t *testing.T) {
	e := newEnvLoesch(t)
	e.setzeKommentare(t, `[`+altKommentar+`,{"id":"ki1","author":"🤖 anthropic/claude (im Auftrag von anna@firma.de)","text":"Antwort","created":"2026-09-01"}]`)
	if code, b := e.ops(t, "bert@firma.de", kommentarDelete("ki1")); code != 422 {
		t.Errorf("Bert verwirft Annas alte KI-Antwort: %d %s, erwartet 422", code, b)
	}
	if code, b := e.ops(t, "anna@firma.de", kommentarDelete("ki1")); code != 200 {
		t.Errorf("Anna verwirft ihre alte KI-Antwort: %d %s", code, b)
	}
	if e.kommentar(t, "ki1") != nil {
		t.Error("ki1 steht noch")
	}
	if act, _ := e.karte(t)["activity"].([]any); len(act) != 0 {
		t.Errorf("eigene Antwort verworfen, trotzdem Verlauf: %v", act)
	}
}

// F-1612: Der Ganz-Board-PUT prüft dasselbe wie die Ops.
func TestKommentarLoeschrecht_PUT(t *testing.T) {
	e := newEnvLoesch(t)
	ohneAlt := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,"columnDefs":[{"id":"col-ready","title":"Ready"}],` +
		`"sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"Karte eins","columnId":"col-ready","comments":[],"activity":[]}]}]}}`
	if code, b := e.put(t, "anna@firma.de", ohneAlt); code != 422 || !strings.Contains(b, "comments of other people") {
		t.Errorf("editor entfernt per PUT: %d %s, erwartet 422", code, b)
	}
	altUnveraendert(t, e, "PUT")
	gestempelt := strings.Replace(ohneAlt, `"comments":[]`, `"comments":[{"id":"alt1","author":"Altbestand","text":"alter Text","created":"2026-01-01","authorEmail":"anna@firma.de"}]`, 1)
	// Verify VB-1: Der PUT stempelt wie die Ops — ein mitgeschickter
	// authorEmail zählt nicht, der gespeicherte bleibt (hier: keiner). Die
	// Fälschung kommt also gar nicht erst an; 200 ist richtig.
	if code, b := e.put(t, "anna@firma.de", gestempelt); code != 200 {
		t.Errorf("editor fälscht per PUT: %d %s, erwartet 200 (Stempel neutralisiert)", code, b)
	}
	altUnveraendert(t, e, "PUT-Stempel")

	// Verify VB-1: ein neuer Kommentar per PUT (Import, Rückfall bei zu großen
	// Ops) — ohne authorEmail, wie der Browser ihn schickt — wird angenommen
	// und auf den Schreiber gestempelt.
	mitNeu := strings.Replace(ohneAlt, `"comments":[]`, `"comments":[`+altKommentar+`,{"id":"n1","author":"Anna","text":"neu per PUT","created":"2026-10-05"}]`, 1)
	if code, b := e.put(t, "anna@firma.de", mitNeu); code != 200 {
		t.Fatalf("neuer Kommentar per PUT: %d %s, erwartet 200", code, b)
	}
	if k := e.kommentar(t, "n1"); k == nil || k["authorEmail"] != "anna@firma.de" {
		t.Fatalf("neuer Kommentar per PUT nicht gestempelt: %v", k)
	}

	// KA-1: „Meine Version behalten“ mit veraltetem Stand — Berts neuer
	// Kommentar fehlt darin. Er darf nicht still verschwinden.
	if code, b := e.ops(t, "bert@firma.de", kommentarAdd("b1", "von Bert", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	veraltet := strings.Replace(ohneAlt, `"comments":[]`, `"comments":[`+altKommentar+`]`, 1)
	if code, b := e.put(t, "anna@firma.de", veraltet); code != 422 {
		t.Errorf("veralteter PUT ohne Berts Kommentar: %d %s, erwartet 422", code, b)
	}
	if e.kommentar(t, "b1") == nil {
		t.Error("Berts Kommentar ist weg")
	}
	// Der Owner darf (ADR-0136 §2).
	if code, b := e.put(t, "olga@firma.de", ohneAlt); code != 200 {
		t.Errorf("Owner per PUT: %d %s", code, b)
	}
}

// put schreibt das ganze Board mit aktuellem If-Match.
func (e *envLoesch) put(t *testing.T, wer, body string) (int, string) {
	t.Helper()
	b, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/boards/b1", strings.NewReader(body))
	req.Header.Set("Remote-Email", wer)
	req.Header.Set("Remote-User", wer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"`+jsonZahl(b.UpdatedAt)+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	roh, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(roh)
}

func jsonZahl(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// setzeKommentare ersetzt die Kommentare von k1 direkt im Repo (Altbestand).
func (e *envLoesch) setzeKommentare(t *testing.T, kommentare string) {
	t.Helper()
	ctx := context.Background()
	b, err := e.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(b.StateJSON, &state); err != nil {
		t.Fatal(err)
	}
	var ks any
	if err := json.Unmarshal([]byte(kommentare), &ks); err != nil {
		t.Fatal(err)
	}
	state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)["comments"] = ks
	neu, _ := json.Marshal(state)
	if _, err := e.boards.Update(ctx, b.ID, b.Title, b.Version, neu, b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}
