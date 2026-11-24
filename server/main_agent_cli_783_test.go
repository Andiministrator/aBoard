package main

// Karte #783 (ADR-0140 §8): `agent-token create --role intern` — nur auf der
// Seite außen und nur als Projekt-Token (Kritik K-B6: die CLI war ungetestet).

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentTokenCLI_783_Intern(t *testing.T) {
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
	cfg := storage.NewConfigRepo(h, nil, uuidString)
	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAgentTokenMit(append(args, "--db", db), &out)
		return out.String(), err
	}
	create := []string{"create", "--board", "b1", "--role", "intern", "--name", "assistent"}
	for _, seite := range []string{"", api.BoardpaarSeiteInnen} {
		if seite != "" {
			if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := lauf(create...); err == nil || !strings.Contains(err.Error(), "aussen") {
			t.Fatalf("Seite %q: %v %s", seite, err, out)
		}
	}
	if err := cfg.Set(ctx, api.KonfigBoardpaarSeite, api.BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	if out, err := lauf(append(create, "--user", "anna@firma.de")...); err == nil {
		t.Fatalf("intern mit --user: %s", out)
	}
	_ = h.Close()
	// Karte #799: außen sind --anbieter und --beleg Pflicht.
	out, err := lauf(append(create, "--anbieter", "Beispiel-Anbieter", "--beleg", "vertraege/vertrag.md")...)
	if err != nil || !strings.Contains(out, "internen Agenten") || !strings.Contains(out, "abt_") {
		t.Fatalf("Seite außen: %v %s", err, out)
	}
}
