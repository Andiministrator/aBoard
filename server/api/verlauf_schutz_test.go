package api_test

// F-1621: Der Verlauf einer Karte ist ein Protokoll. Niemand entfernt einen
// bestehenden Eintrag (auch nicht der Owner — der Eintrag
// „comment-deleted-by-owner“ ist der Nachweis gegen ihn), niemand erfindet
// einen server-eigenen. Der Server führt den Verlauf zusammen, damit ein
// veralteter Browser beim Anhängen nicht abgelehnt wird.

import (
	"strings"
	"testing"
)

// mitOwnerLoeschung: Anna kommentiert, Olga (Owner) löscht — danach trägt k1
// den Verlaufseintrag comment-deleted-by-owner.
func mitOwnerLoeschung(t *testing.T) *envLoesch {
	t.Helper()
	e := newEnvLoesch(t)
	if code, b := e.ops(t, "anna@firma.de", kommentarAdd("a1", "von Anna", "")); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	if code, b := e.ops(t, "olga@firma.de", kommentarDelete("a1")); code != 200 {
		t.Fatalf("Owner löscht: %d %s", code, b)
	}
	if v := verlaufAktionen(t, e); len(v) != 1 || v[0] != "comment-deleted-by-owner" {
		t.Fatalf("Ausgangslage: %v", v)
	}
	return e
}

func verlaufAktionen(t *testing.T, e *envLoesch) []string {
	t.Helper()
	act, _ := e.karte(t)["activity"].([]any)
	var aus []string
	for _, a := range act {
		m, _ := a.(map[string]any)
		s, _ := m["action"].(string)
		aus = append(aus, s)
	}
	return aus
}

func feldOp(feld, wert string) string {
	return `[{"type":"card.field","payload":{"cardId":"k1","field":"` + feld + `","value":` + wert + `}}]`
}

func TestVerlaufSchutz_LeerenEntferntNichts(t *testing.T) {
	e := mitOwnerLoeschung(t)
	for _, wer := range []string{"anna@firma.de", "olga@firma.de", "root@firma.de"} {
		if code, b := e.ops(t, wer, feldOp("activity", `[]`)); code != 200 {
			t.Fatalf("%s: card.field activity []: %d %s", wer, code, b)
		}
		if v := verlaufAktionen(t, e); len(v) != 1 || v[0] != "comment-deleted-by-owner" {
			t.Fatalf("%s hat den Nachweis entfernt: %v", wer, v)
		}
	}
	// card.fieldIf und card.upsert ebenso.
	fieldIf := `[{"type":"card.fieldIf","payload":{"cardId":"k1","field":"activity","value":[],"expect":[]}}]`
	_, _ = e.ops(t, "anna@firma.de", fieldIf)
	upsert := `[{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k1","displayId":1,` +
		`"title":"Karte eins","columnId":"col-ready","comments":[` + altKommentar + `],"activity":[]}}}]`
	if code, b := e.ops(t, "anna@firma.de", upsert); code != 200 {
		t.Fatalf("card.upsert: %d %s", code, b)
	}
	if v := verlaufAktionen(t, e); len(v) != 1 || v[0] != "comment-deleted-by-owner" {
		t.Fatalf("fieldIf/upsert hat den Nachweis entfernt: %v", v)
	}
}

func TestVerlaufSchutz_VeralteterBrowserHaengtAn(t *testing.T) {
	e := mitOwnerLoeschung(t)
	// Der Browser kennt den Server-Eintrag noch nicht und hängt eine eigene
	// Änderung an — sie kommt dazu, der Server-Eintrag bleibt.
	neu := `[{"id":"x1","author":"Anna","action":"owner-changed","timestamp":"2026-10-05T10:00:00Z","from":"","to":"Anna"}]`
	if code, b := e.ops(t, "anna@firma.de", feldOp("activity", neu)); code != 200 {
		t.Fatalf("Anhängen: %d %s", code, b)
	}
	if v := verlaufAktionen(t, e); len(v) != 2 || v[0] != "comment-deleted-by-owner" || v[1] != "owner-changed" {
		t.Fatalf("Verlauf: %v", v)
	}
	// Dieselbe Liste noch einmal: nichts doppelt.
	if code, b := e.ops(t, "anna@firma.de", feldOp("activity", neu)); code != 200 {
		t.Fatalf("Wiederholung: %d %s", code, b)
	}
	if v := verlaufAktionen(t, e); len(v) != 2 {
		t.Fatalf("Eintrag doppelt: %v", v)
	}
}

func TestVerlaufSchutz_ServerEintragFaelschen(t *testing.T) {
	e := newEnvLoesch(t)
	for _, f := range []struct{ name, ops string }{
		{"llm-sent per field", feldOp("activity", `[{"action":"llm-sent","to":"openai/gpt"}]`)},
		{"comment-deleted per field", feldOp("activity", `[{"id":"f","action":"comment-deleted-by-owner","author":"olga@firma.de","from":"bert@firma.de","to":"gibt es nicht"}]`)},
		{"board-transferred unter type", feldOp("activity", `[{"type":"board-transferred","from":"x","to":"y"}]`)},
		{"neue Karte mit llm-sent", `[{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k2","title":"neu",` +
			`"columnId":"col-ready","comments":[],"activity":[{"action":"llm-sent","to":"a/b"}]}}}]`},
	} {
		for _, wer := range []string{"anna@firma.de", "olga@firma.de"} {
			code, b := e.ops(t, wer, f.ops)
			if code != 422 || !strings.Contains(b, "verlauf-gefaelscht") {
				t.Errorf("%s (%s): %d %s, erwartet 422 verlauf-gefaelscht", f.name, wer, code, b)
			}
		}
	}
	if v := verlaufAktionen(t, e); len(v) != 0 {
		t.Fatalf("Fälschung gespeichert: %v", v)
	}
}

func TestVerlaufSchutz_EintragAendern(t *testing.T) {
	e := mitOwnerLoeschung(t)
	act, _ := e.karte(t)["activity"].([]any)
	eintrag := act[0].(map[string]any)
	// Den Nachweis umschreiben (anderer Textanfang): Das ist ein NEUER Eintrag
	// mit server-eigener Aktion — abgelehnt; der alte bleibt.
	geaendert := `[{"id":"` + eintrag["id"].(string) + `","author":"olga@firma.de","action":"comment-deleted-by-owner",` +
		`"from":"anna@firma.de","to":"harmlos","timestamp":"` + eintrag["timestamp"].(string) + `"}]`
	code, b := e.ops(t, "olga@firma.de", feldOp("activity", geaendert))
	if code != 422 || !strings.Contains(b, "verlauf-gefaelscht") {
		t.Fatalf("Nachweis umschreiben: %d %s, erwartet 422", code, b)
	}
	if v, _ := e.karte(t)["activity"].([]any); len(v) != 1 || v[0].(map[string]any)["to"] != "von Anna" {
		t.Fatalf("Nachweis verändert: %v", v)
	}
}

func TestVerlaufSchutz_PUT(t *testing.T) {
	e := mitOwnerLoeschung(t)
	board := func(activity string) string {
		return `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,"columnDefs":[{"id":"col-ready","title":"Ready"}],` +
			`"sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"Karte eins","columnId":"col-ready",` +
			`"comments":[` + altKommentar + `],"activity":` + activity + `}]}]}}`
	}
	// Leeren per PUT (auch „Meine Version behalten“ mit altem Stand): der
	// Eintrag bleibt.
	if code, b := e.put(t, "olga@firma.de", board(`[]`)); code != 200 {
		t.Fatalf("PUT activity []: %d %s", code, b)
	}
	if v := verlaufAktionen(t, e); len(v) != 1 || v[0] != "comment-deleted-by-owner" {
		t.Fatalf("PUT hat den Nachweis entfernt: %v", v)
	}
	// Fälschen per PUT: abgelehnt.
	code, b := e.put(t, "anna@firma.de", board(`[{"action":"llm-sent","to":"a/b"}]`))
	if code != 422 || !strings.Contains(b, "activity") {
		t.Fatalf("PUT fälscht: %d %s, erwartet 422 mit Hinweis auf activity", code, b)
	}
}

// Kritiker KB-2: Eine vorbestehend doppelte Karten-id mit server-eigenem
// Eintrag sperrt die Karte nicht, und der Verlauf wächst nicht mit jedem PUT.
func TestVerlaufSchutz_DoppelteKartenID(t *testing.T) {
	e := mitOwnerLoeschung(t)
	ev := `{"id":"v1","action":"comment-deleted-by-owner","author":"o","from":"a","to":"t","timestamp":"2026-10-05T10:00:00Z"}`
	karte := `{"id":"k1","displayId":1,"title":"Karte eins","columnId":"col-ready","comments":[` + altKommentar + `],"activity":[` + ev + `]}`
	st := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,"columnDefs":[{"id":"col-ready","title":"Ready"}],` +
		`"sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[` + karte + `]},{"id":"s2","title":"S2","kind":"sprint","cards":[` + karte + `]}]}}`
	b, _ := e.boards.Get(t.Context(), "b1")
	if _, err := e.boards.Update(t.Context(), b.ID, b.Title, b.Version, []byte(st), b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if code, body := e.put(t, "olga@firma.de", st); code != 200 {
			t.Fatalf("PUT unverändert #%d: %d %s", i, code, body)
		}
		if code, body := e.ops(t, "olga@firma.de", feldOp("activity", `[{"id":"n`+string(rune('a'+i))+`","action":"owner-changed"}]`)); code != 200 {
			t.Fatalf("activity anhängen #%d: %d %s", i, code, body)
		}
	}
	gespeichert, _ := e.boards.Get(t.Context(), "b1")
	if n := strings.Count(string(gespeichert.StateJSON), `"comment-deleted-by-owner"`); n > 2 {
		t.Errorf("Verlauf wächst: %d Nachweis-Einträge (höchstens einer je Kopie)", n)
	}
}
