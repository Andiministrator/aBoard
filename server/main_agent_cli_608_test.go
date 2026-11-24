package main

// Karte #608: agent-token CLI — Person, Gültigkeit, list --json (nie ein
// Token), --warn-days (Exit 3 über errAblaufWarnung), revoke-user.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentTokenCLI_608(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'A', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewAclRepo(h).Set(ctx, "b1", "anna@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()

	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAgentTokenMit(append(args, "--db", db), &out)
		return out.String(), err
	}
	out, err := lauf("create", "--name", "team-projekt", "--board", "b1", "--gueltig-tage", "5")
	if err != nil || !strings.Contains(out, "gültig bis") {
		t.Fatalf("create: %v %s", err, out)
	}
	zeilenOut := strings.Split(strings.TrimSpace(out), "\n")
	token := strings.TrimSpace(zeilenOut[len(zeilenOut)-1])
	if !strings.HasPrefix(token, "abt_") {
		t.Fatalf("Token nicht gefunden: %q", out)
	}
	if out, err := lauf("create", "--name", "annas", "--board", "b1", "--user", "anna@firma.de"); err != nil || !strings.Contains(out, "für anna@firma.de") {
		t.Fatalf("create --user: %v %s", err, out)
	}

	out, err = lauf("list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, token) || strings.Contains(out, token[len("abt_")+11:]) {
		t.Fatal("Token in list --json")
	}
	var zeilen []map[string]any
	if err := json.Unmarshal([]byte(out), &zeilen); err != nil || len(zeilen) != 2 {
		t.Fatalf("JSON: %v %s", err, out)
	}
	personGefunden := false
	for _, z := range zeilen {
		if z["person"] == "anna@firma.de" {
			personGefunden = true
		}
		if z["expires_at"] == nil || z["tage_rest"] == nil {
			t.Fatalf("Ablauf fehlt: %v", z)
		}
	}
	if !personGefunden {
		t.Fatalf("Person fehlt: %s", out)
	}
	// 5 Tage Rest: --warn-days 7 warnt, --warn-days 3 nicht.
	if _, err := lauf("list", "--warn-days", "7"); !errors.Is(err, errAblaufWarnung) {
		t.Fatalf("warn-days 7: %v", err)
	}
	if _, err := lauf("list", "--warn-days", "3", "--json"); err != nil {
		t.Fatalf("warn-days 3: %v", err)
	}
	// Tabelle nennt die Spalte „LÄUFT AB AM“.
	if out, _ := lauf("list"); !strings.Contains(out, "LÄUFT AB AM") {
		t.Fatalf("Tabelle: %s", out)
	}
	// Session-Kritiker Sprint 78, K-2: ein SCHON abgelaufenes (nicht
	// widerrufenes) Token darf die Wache nicht dauerhaft auslösen.
	h2, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h2.Exec(`UPDATE agent_tokens SET expires_at = '2020-01-01T00:00:00Z' WHERE name = 'team-projekt'`); err != nil {
		t.Fatal(err)
	}
	_ = h2.Close()
	// 'annas' läuft erst in 90 Tagen ab → mit 30 Tagen keine Warnung, das
	// abgelaufene 'team-projekt' darf sie nicht auslösen.
	if _, err := lauf("list", "--warn-days", "30"); err != nil {
		t.Fatalf("abgelaufenes Token löste die Warnung aus: %v", err)
	}
	if out, err := lauf("revoke-user", "--user", "anna@firma.de"); err != nil || !strings.Contains(out, "1 Token(s)") {
		t.Fatalf("revoke-user: %v %s", err, out)
	}
	if _, err := lauf("revoke-user"); err == nil {
		t.Fatal("revoke-user ohne --user")
	}
}
