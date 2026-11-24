package main

// Karte #1076: `aboard-server nutzer einladen` (ADR-0149 §5.7). Testdaten nur
// example.org.

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestNutzerEinladen1076(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv("ABOARD_PUBLIC_BASE_URL", "https://board.example.org")
	pfad := filepath.Join(t.TempDir(), "a.db")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var out bytes.Buffer
	if err := runNutzerMit(context.Background(), []string{"einladen", "--db", pfad, "--email", "Erste@Example.org"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	link := regexp.MustCompile(`https://board\.example\.org/einladung/[A-Za-z0-9_-]{43}`).FindString(out.String())
	if link == "" || !strings.Contains(out.String(), "erste@example.org") {
		t.Fatalf("Ausgabe ohne Link:\n%s", out.String())
	}
	// Der Link ist gültig.
	db, err = storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	token := strings.TrimPrefix(link, "https://board.example.org"+api.EinladungPfadPraefix)
	if o, err := storage.NewEinladungRepo(db).Pruefen(context.Background(), token); err != nil || o.Email != "erste@example.org" {
		t.Fatalf("Link ungültig: %+v %v", o, err)
	}
	// Zweiter Aufruf: neuer Link, Hinweis auf den ungültigen alten.
	out.Reset()
	if err := runNutzerMit(context.Background(), []string{"einladen", "--db", pfad, "--email", "erste@example.org"}, &out); err != nil ||
		!strings.Contains(out.String(), "der vorige gilt nicht mehr") {
		t.Fatalf("erneut: %v\n%s", err, out.String())
	}
	if _, err := storage.NewEinladungRepo(db).Pruefen(context.Background(), token); err == nil {
		t.Fatal("alter Link gilt noch")
	}

	for name, f := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"ohne email":   {args: []string{"einladen", "--db", pfad}, want: "--email"},
		"ungültig":     {args: []string{"einladen", "--db", pfad, "--email", "kein-adresse"}, want: "keine gültige"},
		"ohne Betrieb": {args: []string{"einladen", "--db", pfad, "--email", "x@example.org"}, env: map[string]string{"ABOARD_ANMELDUNG": ""}, want: "eingebauten Anmeldung"},
		"ohne Basis":   {args: []string{"einladen", "--db", pfad, "--email", "x@example.org"}, env: map[string]string{"ABOARD_PUBLIC_BASE_URL": ""}, want: "ABOARD_PUBLIC_BASE_URL"},
		"falsche DB":   {args: []string{"einladen", "--db", pfad + ".fehlt", "--email", "x@example.org"}, want: "nicht gefunden"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range f.env {
				t.Setenv(k, v)
			}
			var o bytes.Buffer
			err := runNutzerMit(context.Background(), f.args, &o)
			if err == nil || !strings.Contains(err.Error(), f.want) {
				t.Fatalf("%v, erwartet %q", err, f.want)
			}
			if strings.Contains(o.String(), "/einladung/") {
				t.Fatal("Link trotz Fehler")
			}
		})
	}
}
