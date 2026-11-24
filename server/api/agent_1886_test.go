package api_test

// F-1886 (Kritiker der Sprint-86-End-QA zu #766): Die Anhang-Sperre liest
// beide Stände nur noch, wenn eine Op des Batches eine Anhang-Liste
// erreichen kann. Der Vorfilter darf dabei keine Op auslassen, die eine
// Liste kürzt — comment.delete etwa nimmt die Einträge seines Kommentars
// mit (ADR-0135).

import (
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestAgent1886_Vorfilter(t *testing.T) {
	e := newEnv671(t, true)
	var vorfilter, pruefung int
	defer api.ZaehleAnhangPruefungFuerTest(&vorfilter, &pruefung)()

	sende := func(ops string) (int, string) {
		t.Helper()
		vorfilter, pruefung = 0, 0
		s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+ops+`]}`)
		return s, string(b)
	}

	// Ohne anhangsrelevante Op: Vorfilter ja, Prüfung nein.
	ohne := []string{
		`{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Neu"}}`,
		`{"type":"card.move","payload":{"cardId":"k1","toSprintId":"__backlog__","toColId":"col-ready"}}`,
		`{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c-1886","author":"agent:projekt","text":"Hallo","created":"2026-10-09"}}}`,
	}
	for _, op := range ohne {
		e.mitBoardAnhang766(t)
		if s, b := sende(op); s != http.StatusOK || vorfilter != 1 || pruefung != 0 {
			t.Errorf("%s: %d vorfilter=%d pruefung=%d %s", op, s, vorfilter, pruefung, b)
		}
	}

	// Anhangsrelevant: die Prüfung läuft (Ergebnis dort, nicht im Filter).
	mit := []string{
		`{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[` + anhangA1_766 + `,` + anhangNeu766 + `]}}`,
		`{"type":"board.field","payload":{"field":"attachments","value":[` + anhangB1_766 + `]}}`,
		// Eine harmlose Op vor der relevanten verdeckt sie nicht.
		`{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Neu"}},{"type":"card.field","payload":{"cardId":"k1","field":"attachments","value":[]}}`,
	}
	for _, op := range mit {
		e.mitBoardAnhang766(t)
		if s, b := sende(op); pruefung != 1 {
			t.Errorf("%s: Prüfung lief nicht (%d, pruefung=%d) %s", op, s, pruefung, b)
		}
	}

}

// Der Vorfilter je Op — auch für Wege, die heute eine frühere Sperre
// schließt (comment.delete ist Agenten roh verboten, listAdd auf attachments
// lehnt der Fold ab): fällt diese Sperre, darf der Filter nicht mitfallen.
func TestAgent1886_VorfilterJeOp(t *testing.T) {
	for _, f := range []struct {
		typ, payload string
		relevant     bool
	}{
		{"card.field", `{"cardId":"k1","field":"title","value":"x"}`, false},
		{"card.fieldIf", `{"cardId":"k1","field":"labels","value":[]}`, false},
		{"card.move", `{"cardId":"k1","toSprintId":"s1"}`, false},
		{"card.delete", `{"cardId":"k1"}`, false},
		{"swimlane.delete", `{"sprintId":"s1"}`, false},
		{"termin.upsert", `{"termin":{}}`, false},
		{"card.field", `{"cardId":"k1","field":"attachments","value":[]}`, true},
		{"card.fieldIf", `{"cardId":"k1","field":"attachments","value":[]}`, true},
		{"card.listRemove", `{"cardId":"k1","field":"attachments","item":{}}`, true},
		{"card.listAdd", `{"cardId":"k1","field":"attachments","item":{}}`, true},
		{"card.field", `kein json`, true},
		{"card.field", `{"cardId":"k1","field":7}`, true},
		{"comment.delete", `{"cardId":"k1","commentId":"c"}`, true},
		{"card.upsert", `{"card":{"id":"k1"}}`, true},
		{"swimlane.upsert", `{"sprint":{"id":"s1"}}`, true},
		{"board.field", `{"field":"title","value":"x"}`, true},
		{"maintenance.compact", `{}`, true},
		{"gibt.es.nicht", `{}`, true},
	} {
		if got := api.OpsBeruehrenAnhaengeFuerTest(f.typ, f.payload); got != f.relevant {
			t.Errorf("%s %s: relevant=%v, erwartet %v", f.typ, f.payload, got, f.relevant)
		}
	}
}
