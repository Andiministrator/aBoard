package api_test

// Karte #1197, ADR-0156: GET /boards liefert je Board `awaiting_user`, wenn
// gesetzt — im Legacy- wie im Forward-Auth-Zweig, nie für fremde Boards.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

const awEintrag1197 = `{"anna@example.org":{"question":"Q?","since":"2026-11-23T10:00:00Z"}}`

func seedAwaiting1197(t *testing.T, env *aclTestEnv, id string, aw string) {
	t.Helper()
	body := []byte(`{"version":"2","board":{"id":"` + id + `","title":"Board ` + id + `","awaitingUser":` + aw + `}}`)
	if _, err := env.boards.Insert(context.Background(), id, "Board "+id, "2", body); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func listeAwaiting1197(t *testing.T, env *aclTestEnv, email string) map[string]json.RawMessage {
	t.Helper()
	resp := env.do(t, http.MethodGet, "/boards", email, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /boards = %d", resp.StatusCode)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(mustReadAll(t, resp.Body), &list); err != nil {
		t.Fatalf("json: %v", err)
	}
	out := map[string]json.RawMessage{}
	for _, e := range list {
		var id string
		_ = json.Unmarshal(e["id"], &id)
		out[id] = e["awaiting_user"] // nil = Schlüssel fehlt
	}
	return out
}

func pruefeAw1197(t *testing.T, got json.RawMessage) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("awaiting_user kein JSON: %q", got)
	}
	_ = json.Unmarshal([]byte(awEintrag1197), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("awaiting_user = %s, erwartet %s", gb, wb)
	}
}

func TestBoardsListe_AwaitingUser_Legacy(t *testing.T) {
	env := newACLTestEnv(t, false)
	defer env.close()
	seedAwaiting1197(t, env, "mit", awEintrag1197)
	seedAwaiting1197(t, env, "ohne", "null")
	l := listeAwaiting1197(t, env, "")
	if _, ok := l["mit"]; !ok {
		t.Fatalf("Board fehlt in der Liste: %v", l)
	}
	pruefeAw1197(t, l["mit"])
	if l["ohne"] != nil {
		t.Errorf("ohne Eintrag: awaiting_user = %s, erwartet weggelassen", l["ohne"])
	}
}

func TestBoardsListe_AwaitingUser_ForwardAuthNurSichtbare(t *testing.T) {
	env := newACLTestEnv(t, true)
	defer env.close()
	seedAwaiting1197(t, env, "A", awEintrag1197)
	seedAwaiting1197(t, env, "B", awEintrag1197)
	if err := env.acl.Set(context.Background(), "A", "u@example.org", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	l := listeAwaiting1197(t, env, "u@example.org")
	if _, ok := l["B"]; ok {
		t.Fatalf("fremdes Board B in der Liste: %v", l)
	}
	if _, ok := l["A"]; !ok {
		t.Fatalf("Board A fehlt: %v", l)
	}
	pruefeAw1197(t, l["A"])
}
