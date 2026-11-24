package main

// Karte #670 (ADR-0133 §6): `agent-token create --role vorschlag` — nur als
// Projekt-Token, nicht mit --user und nicht mit --delegierend.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentTokenCLI_670_Vorschlag(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewAclRepo(h).Set(ctx, "b1", "anna@firma.de", "owner"); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAgentTokenMit(append(args, "--db", db), &out)
		return out.String(), err
	}

	for _, extra := range [][]string{{"--user", "anna@firma.de"}, {"--delegierend"}} {
		args := append([]string{"create", "--board", "b1", "--role", "vorschlag", "--name", "recherche"}, extra...)
		if _, err := lauf(args...); err == nil {
			t.Fatalf("%v: angelegt, erwartet Ablehnung", extra)
		}
	}
	out, err := lauf("create", "--board", "b1", "--role", "vorschlag", "--name", "recherche")
	if err != nil || !strings.Contains(out, "Vorschlags-Token") || !strings.Contains(out, "„Vorschläge“") {
		t.Fatalf("create --role vorschlag: %v %s", err, out)
	}
	if out, err = lauf("list"); err != nil || !strings.Contains(out, "vorschlag") || strings.Count(out, "recherche") != 1 {
		t.Fatalf("list: %v %s", err, out)
	}
}
