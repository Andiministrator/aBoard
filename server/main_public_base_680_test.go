package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// F-1559: Werkzeug- und Mail-Links beziehen ABOARD_PUBLIC_BASE_URL über
// dieselbe Prüfung. Ein Wert mit Query oder Fragment ergibt keine Links
// (statt kaputter Mail-Links), mit Warnung.
func TestOeffentlicheBasisGeprueft(t *testing.T) {
	var meldungen []string
	logf := func(f string, a ...any) { meldungen = append(meldungen, fmt.Sprintf(f, a...)) }

	if got := oeffentlicheBasisGeprueft(" https://board.example/ ", logf); got != "https://board.example" || len(meldungen) != 0 {
		t.Fatalf("gültig: %q %v", got, meldungen)
	}
	if got := oeffentlicheBasisGeprueft("", logf); got != "" || len(meldungen) != 0 {
		t.Fatalf("leer: %q %v", got, meldungen)
	}
	for _, schlecht := range []string{"https://board.example/?x=1", "https://board.example/#a", "ftp://board.example", "https://u:p@board.example"} {
		meldungen = nil
		if got := oeffentlicheBasisGeprueft(schlecht, logf); got != "" {
			t.Errorf("%q: Wert %q statt leer", schlecht, got)
		}
		if len(meldungen) != 1 || !strings.Contains(meldungen[0], "ABOARD_PUBLIC_BASE_URL") {
			t.Errorf("%q: Meldungen %v", schlecht, meldungen)
		}
	}
}

// Die Mail-Verdrahtung darf den Wert nicht noch einmal ungeprüft lesen.
func TestOeffentlicheBasis_NurEineLesestelle(t *testing.T) {
	roh, err := mainQuelltext()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(roh, `Getenv("ABOARD_PUBLIC_BASE_URL")`) + strings.Count(roh, `envOrDefault("ABOARD_PUBLIC_BASE_URL"`); n != 1 {
		t.Fatalf("%d Lesestellen von ABOARD_PUBLIC_BASE_URL in main.go, erwartet 1", n)
	}
}

func mainQuelltext() (string, error) {
	b, err := os.ReadFile("main.go")
	return string(b), err
}
