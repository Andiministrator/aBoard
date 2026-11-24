package main

// Karte #669 (ADR-0133 §4): `agent-token create --delegierend` — ohne Board
// und Person, list zeigt es als delegierend, nie mit Token.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentTokenCLI_669_Delegierend(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAgentTokenMit(append(args, "--db", db), &out)
		return out.String(), err
	}

	// Mit Board oder Person: abgelehnt, bevor etwas angelegt wird.
	// Die Meldung nennt den Grund (nicht nur „board/person unbekannt“).
	for _, extra := range [][]string{{"--board", "b1"}, {"--user", "anna@firma.de"}} {
		args := append([]string{"create", "--delegierend", "--name", "open-webui"}, extra...)
		if _, err := lauf(args...); err == nil || !strings.Contains(err.Error(), "--delegierend schliesst") {
			t.Fatalf("%v: %v", extra, err)
		}
	}
	out, err := lauf("create", "--delegierend", "--name", "open-webui")
	if err != nil || !strings.Contains(out, "Delegierendes Dienst-Token") || !strings.Contains(out, "ABOARD_OWUI_JWT_SECRET_FILE") {
		t.Fatalf("create --delegierend: %v %s", err, out)
	}
	zeilen := strings.Split(strings.TrimSpace(out), "\n")
	token := strings.TrimSpace(zeilen[len(zeilen)-1])
	if !strings.HasPrefix(token, "abt_") {
		t.Fatalf("Token nicht gefunden: %q", out)
	}

	out, err = lauf("list", "--json")
	if err != nil || strings.Contains(out, token) {
		t.Fatalf("list --json: %v", err)
	}
	var l []agentTokenListeZeile
	if err := json.Unmarshal([]byte(out), &l); err != nil || len(l) != 1 || !l[0].Delegierend || l[0].Board != "" {
		t.Fatalf("list --json: %v %s", err, out)
	}
	if out, err = lauf("list"); err != nil || !strings.Contains(out, "(delegierend)") {
		t.Fatalf("list: %v %s", err, out)
	}
}
