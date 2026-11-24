package main

// Karte #825: CLI — Zugang abr, Recht --darf-anlegen, Auftrag kopie-anlegen.

import (
	"strings"
	"testing"
)

func TestBoardpaar825_CLI(t *testing.T) {
	db := boardpaarCLIDB(t)
	if out, err := boardpaarCLI(t, db, "token-neu", "--typ", "abr", "--name", "rueck"); err != nil || !strings.Contains(out, "abr_") {
		t.Errorf("abr: %q %v", out, err)
	}
	if _, err := boardpaarCLI(t, db, "token-neu", "--typ", "abs", "--name", "x", "--darf-anlegen"); err == nil {
		t.Error("abs mit --darf-anlegen angenommen")
	}
	if out, err := boardpaarCLI(t, db, "token-neu", "--typ", "abe", "--name", "kopien", "--darf-anlegen"); err != nil || !strings.Contains(out, "darf Kopien anlegen") {
		t.Errorf("abe mit Recht: %q %v", out, err)
	}
	// Seite aus: kein Auftrag.
	if _, err := boardpaarCLI(t, db, "kopie-anlegen", "--board", "b1", "--ziel", "team", "--link", "https://i.example/#board=b1"); err == nil {
		t.Error("Auftrag auf Seite aus angenommen")
	}
	if _, err := boardpaarCLI(t, db, "seite", "--setzen", "innen"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABOARD_PUBLIC_BASE_URL", "")
	if _, err := boardpaarCLI(t, db, "kopie-anlegen", "--board", "b1", "--ziel", "team"); err == nil || !strings.Contains(err.Error(), "ABOARD_PUBLIC_BASE_URL") {
		t.Errorf("ohne Adresse: %v", err)
	}
	t.Setenv("ABOARD_PUBLIC_BASE_URL", "https://innen.example")
	if out, err := boardpaarCLI(t, db, "kopie-anlegen", "--board", "b1", "--ziel", "team"); err != nil || !strings.Contains(out, "Auftrag") {
		t.Fatalf("Auftrag: %q %v", out, err)
	}
	if _, err := boardpaarCLI(t, db, "kopie-anlegen", "--board", "b1", "--ziel", "team"); err == nil {
		t.Error("zweiter offener Auftrag angenommen")
	}
	if _, err := boardpaarCLI(t, db, "kopie-anlegen", "--board", "b1", "--ziel", "team", "--typ", "abs"); err == nil {
		t.Error("fremde Option angenommen")
	}
	out, err := boardpaarCLI(t, db, "liste")
	if err != nil || !strings.Contains(out, "b1 → team") || !strings.Contains(out, "offen") {
		t.Errorf("liste: %q %v", out, err)
	}
	if out, _ := boardpaarCLI(t, db, "liste", "--json"); !strings.Contains(out, `"auftraege"`) || !strings.Contains(out, `"darf_anlegen": true`) {
		t.Errorf("liste --json: %s", out)
	}
}
