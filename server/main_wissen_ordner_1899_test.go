package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/Andiministrator/aBoard/server/wissen"
)

// F-1899 (Verify-2 W-3 der Sprint-86-End-QA): Der Ordner der Datenbank
// gehört nicht in die Sperrliste für wissen.dir (F-1893). Liegt die DB
// direkt unter /data (Standard), sperrte er sonst jeden Wissensordner.
func TestWissenServerOrdner(t *testing.T) {
	daten := t.TempDir()
	db := filepath.Join(daten, "aboard.db")
	anhaenge := filepath.Join(daten, "attachments")

	ordner := wissenServerOrdner(anhaenge)
	if !slices.Contains(ordner, anhaenge) {
		t.Errorf("Anhang-Ordner fehlt: %v", ordner)
	}
	for _, o := range ordner {
		if o == daten || o == filepath.Dir(db) {
			t.Errorf("DB-Ordner in der Sperrliste: %v", ordner)
		}
	}
	// Die Wirkung: ein Wissensordner neben der DB ist erlaubt, einer im
	// Anhang-Ordner nicht.
	if err := wissen.PruefeKeinServerOrdner(filepath.Join(daten, "wissen"), ordner); err != nil {
		t.Errorf("Wissensordner neben der DB gesperrt: %v", err)
	}
	if err := wissen.PruefeKeinServerOrdner(filepath.Join(anhaenge, "x"), ordner); err == nil {
		t.Error("Wissensordner im Anhang-Ordner erlaubt")
	}
	// Die Standardwerte des Containers.
	if slices.Contains(wissenServerOrdner(defaultAttachmentsDir), filepath.Dir(defaultDBPath)) {
		t.Errorf("Standard: DB-Ordner %s gesperrt", filepath.Dir(defaultDBPath))
	}
}
