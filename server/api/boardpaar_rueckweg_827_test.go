package api_test

// Karte #827 (ADR-0141 §5.3): Die vier bedingten Rückweg-Ops nimmt der
// Ops-Endpunkt nur auf dem Rückweg des Board-Paars an. Für Browser und Agenten
// wären sie neue Schreibwege an den vorhandenen Regeln vorbei — 400, nichts
// geschrieben.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRueckweg827_OpsNurAufDemRueckweg(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	vorher := leseOpsBoard(t, env)
	for _, op := range []string{
		`{"type":"card.moveIf","payload":{"cardId":"k1","toSprintId":"s1","toColId":"col-ready","expectSprintId":"s1","expectColId":"col-backlog"}}`,
		`{"type":"card.checkItemIf","payload":{"cardId":"k1","text":"a","vorkommen":1,"erledigt":true,"expect":false}}`,
		`{"type":"comment.editIf","payload":{"cardId":"k1","commentId":"c","text":"x","expect":"y"}}`,
		`{"type":"termin.fieldIf","payload":{"terminId":"t1","field":"ort","value":"x","expect":null}}`,
	} {
		code, out := postOps(t, env, "b1", "["+op+"]")
		if code != http.StatusBadRequest || !strings.Contains(alsText827(out), "return path") {
			t.Fatalf("%s: Status %d (%v), erwartet 400 mit Hinweis auf den Rückweg", op, code, out)
		}
	}
	if leseOpsBoard(t, env).UpdatedAt != vorher.UpdatedAt {
		t.Fatal("Board wurde geschrieben")
	}
}

func alsText827(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
