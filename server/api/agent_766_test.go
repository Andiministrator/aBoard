package api_test

// Karte #766 (ADR-0139 E-3, Rest W-9): Auf dem rohen Weg ergänzt ein Agent
// Anhang-Listen nur — am Board und an jeder Karte, die es danach noch gibt.
// Bestehende Einträge bleiben unverändert; card.delete bleibt erlaubt (E-2);
// Menschen und das Lösch-Werkzeug sind ausgenommen.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mitBoardAnhang766: b1 wie board755, dazu ein Board-Anhang b-1.
func (e *env671) mitBoardAnhang766(t *testing.T) {
	t.Helper()
	e.zuruecksetzen755(t)
	st := e.stand(t, "b1")
	anker := `"nextDisplayId":10,`
	if strings.Count(st, anker) != 1 {
		t.Fatalf("Anker %d×", strings.Count(st, anker))
	}
	st = strings.Replace(st, anker, anker+`"attachments":[{"id":"b-1","filename":"plan.pdf","mime":"application/pdf","size":7,"created":"2026-10-01"}],`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, st, "b1"); err != nil {
		t.Fatal(err)
	}
}

const (
	anhangA1_766 = `{"id":"a-1","filename":"notiz.txt","mime":"text/plain","size":4,"created":"2026-10-01"}`
	anhangB1_766 = `{"id":"b-1","filename":"plan.pdf","mime":"application/pdf","size":7,"created":"2026-10-01"}`
	anhangNeu766 = `{"id":"a-neu","filename":"neu.txt","mime":"text/plain","size":3,"created":"2026-10-08"}`
)

func TestAgent766_AnhangListenRoh(t *testing.T) {
	e := newEnv671(t, true)
	abgelehnt := []struct{ name, body, erhalten string }{
		{"Karte leeren", `{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[]}}`, `"a-1"`},
		{"Karte: Eintrag verändert", `{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[` +
			strings.Replace(anhangA1_766, "notiz.txt", "anders.txt", 1) + `]}}`, `"notiz.txt"`},
		{"Karte: ersetzt statt ergänzt", `{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[` + anhangNeu766 + `]}}`, `"a-1"`},
		{"Board leeren", `{"type":"board.field","payload":{"field":"attachments","value":[]}}`, `"b-1"`},
		{"Board: null", `{"type":"board.field","payload":{"field":"attachments","value":null}}`, `"b-1"`},
	}
	for _, f := range abgelehnt {
		e.mitBoardAnhang766(t)
		s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+f.body+`]}`)
		if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "anhang-entfernt") || !strings.Contains(string(b), "anhang_loeschen") {
			t.Errorf("%s: %d %s", f.name, s, b)
		}
		if !strings.Contains(e.stand(t, "b1"), f.erhalten) {
			t.Errorf("%s: Eintrag %s verloren", f.name, f.erhalten)
		}
	}

	erlaubt := []struct{ name, body, erwartet string }{
		{"Karte ergänzen", `{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[` + anhangA1_766 + `,` + anhangNeu766 + `]}}`, `"a-neu"`},
		{"Karte: Reihenfolge", `{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[` + anhangNeu766 + `,` + anhangA1_766 + `]}}`, `"a-neu"`},
		{"Board ergänzen", `{"type":"board.field","payload":{"field":"attachments","value":[` + anhangB1_766 + `,` + anhangNeu766 + `]}}`, `"a-neu"`},
		// E-2: Karte samt Anhang-Liste löschen bleibt roh erlaubt.
		{"card.delete", `{"type":"card.delete","payload":{"cardId":"k1"}}`, `"b-1"`},
		// Andere Ops auf einer Karte mit Anhang lassen die Liste in Ruhe.
		{"Titel", `{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Neu"}}`, `"a-1"`},
	}
	for _, f := range erlaubt {
		e.mitBoardAnhang766(t)
		s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+f.body+`]}`)
		if s != http.StatusOK || !strings.Contains(e.stand(t, "b1"), f.erwartet) {
			t.Errorf("%s: %d %s", f.name, s, b)
		}
	}
	e.mitBoardAnhang766(t)
	if s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.delete","payload":{"cardId":"k1"}}]}`); s != http.StatusOK || strings.Contains(e.stand(t, "b1"), `"a-1"`) {
		t.Errorf("card.delete nimmt die Liste mit: %d %s", s, b)
	}
}

// Fällt eine Op, fällt der ganze Batch — auch die harmlose Op davor.
// card.upsert auf eine bestehende Karte braucht hier keinen eigenen Fall:
// Agenten dürfen keine comments mitschicken (agentPayload), also lehnt die
// Entkernungs-Prüfung des Folds jeden solchen Upsert schon vorher ab.
func TestAgent766_GanzerBatch(t *testing.T) {
	e := newEnv671(t, true)
	e.mitBoardAnhang766(t)
	s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"k5","field":"title","value":"X"}},`+
		`{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[]}}]}`)
	st := e.stand(t, "b1")
	if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), `"index":1`) || !strings.Contains(st, `"a-1"`) || strings.Contains(st, `"title":"X"`) {
		t.Errorf("Batch: %d %s", s, b)
	}
}

// Das Lösch-Werkzeug entfernt Einträge weiter (Werkzeug-Weg, ADR-0139 E-2).
func TestAgent766_WerkzeugAusgenommen(t *testing.T) {
	e := newEnv671(t, true)
	e.mitBoardAnhang766(t)
	pfad := "/werkzeuge/boards/b1/anhaenge/a-1/loeschen"
	code, _ := e.pruefe757(t, e.projekt, pfad)
	if s, m := e.loesche757(t, e.projekt, pfad, code); s != http.StatusOK || strings.Contains(e.stand(t, "b1"), `"a-1"`) {
		t.Errorf("anhang_loeschen: %d %v", s, m)
	}
}

// Menschen (ohne Agenten-Kontext) sind ausgenommen: Der Browser entfernt
// Einträge nach dem DELETE der Datei per Op (src/main.js).
func TestAgent766_MenschAusgenommen(t *testing.T) {
	e := newEnv671(t, true)
	e.mitBoardAnhang766(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/boards/b1/ops", strings.NewReader(`{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[]}}]}`))
	req.Header.Set("Content-Type", "application/json")
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(e.stand(t, "b1"), `"a-1"`) {
		t.Errorf("Mensch: %d %s", rec.Code, rec.Body.String())
	}
}

// F-1882 (Sprint-86-Kritiker K-A1): Den rohen DELETE auf Anhänge gibt es für
// Agenten nicht — Löschen nur über anhang_loeschen (Code, Datei und Eintrag
// zusammen, Hintergrundbild geschützt).
func TestAgent766_RohDelete(t *testing.T) {
	e := newEnv671(t, true)
	e.mitBoardAnhang766(t)
	for _, pfad := range []string{"/boards/b1/attachments/k1/a-1", "/boards/b1/board-attachments/b-1"} {
		if s, b := e.rufe(t, e.projekt, "", "DELETE", pfad, ""); s != http.StatusForbidden {
			t.Errorf("DELETE %s: %d %s", pfad, s, b)
		}
	}
	// Lesen und Hochladen bleiben (Hochladen wird hier nur bis zur Positivliste geprüft).
	if s, b := e.rufe(t, e.projekt, "", "GET", "/boards/b1/board-attachments/b-1", ""); s == http.StatusForbidden {
		t.Errorf("GET Board-Anhang: %d %s", s, b)
	}
}

// F-1883 (K-A2): anhang_loeschen räumt das Titelbild der Karte mit ab, wie
// der Browser (src/main.js).
func TestAgent766_TitelbildLoeschen(t *testing.T) {
	e := newEnv671(t, true)
	e.mitBoardAnhang766(t)
	st := e.stand(t, "b1")
	alt := `"attachments":[` + anhangA1_766 + `],`
	if strings.Count(st, alt) != 1 {
		t.Fatalf("Anker %d×", strings.Count(st, alt))
	}
	st = strings.Replace(st, alt, alt+`"coverAttachmentId":"a-1",`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, st, "b1"); err != nil {
		t.Fatal(err)
	}
	pfad := "/werkzeuge/boards/b1/anhaenge/a-1/loeschen"
	code, _ := e.pruefe757(t, e.projekt, pfad)
	if s, m := e.loesche757(t, e.projekt, pfad, code); s != http.StatusOK {
		t.Fatalf("anhang_loeschen: %d %v", s, m)
	}
	if nach := e.stand(t, "b1"); strings.Contains(nach, `"coverAttachmentId":"a-1"`) || strings.Contains(nach, `"a-1"`) {
		t.Errorf("Titelbild hängt noch: %s", nach)
	}
}
