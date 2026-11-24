package main

// Karte #1083 / F-2877: `aboard-server nutzer bindung-loesen`. Testdaten nur
// example.org.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestNutzerBindungLoesen1083(t *testing.T) {
	pfad := filepath.Join(t.TempDir(), "a.db")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@example.org', '', '2026-01-01T00:00:00Z', 0)`,
		`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('https://iss.example.org', 's1', 'standard', 'u1', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	var out bytes.Buffer
	if err := runNutzerMit(context.Background(), []string{"bindung-loesen", "--db", pfad, "--email", "Anna@Example.org", "--anbieter", "standard"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "gelöst") || !strings.Contains(out.String(), "Boards und Rechte sind unverändert") {
		t.Fatalf("Ausgabe: %s", out.String())
	}
	db, err = storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n, u int
	_ = db.QueryRow(`SELECT COUNT(*) FROM anbieter_bindungen`).Scan(&n)
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&u)
	var key, action, detail string
	if err := db.QueryRow(`SELECT key, action, new_value_redacted FROM server_config_audit WHERE action = ?`, api.AuditActionKontoBindungGeloestCLI).Scan(&key, &action, &detail); err != nil ||
		key != "personen/u1" || detail != "anbieter=standard;ergebnis=ok" {
		t.Fatalf("Audit: %q %q %v", key, detail, err)
	}
	if n != 0 || u != 1 {
		t.Fatalf("Bindungen=%d (erwartet 0), users=%d (erwartet 1)", n, u)
	}
	for name, f := range map[string]struct {
		args []string
		want string
	}{
		"nochmal":      {[]string{"bindung-loesen", "--db", pfad, "--email", "anna@example.org", "--anbieter", "standard"}, "keine Bindung"},
		"unbekannt":    {[]string{"bindung-loesen", "--db", pfad, "--email", "x@example.org", "--anbieter", "standard"}, "keinen Zugang"},
		"ohne anbiet.": {[]string{"bindung-loesen", "--db", pfad, "--email", "anna@example.org"}, "--anbieter"},
		"falsche DB":   {[]string{"bindung-loesen", "--db", pfad + ".fehlt", "--email", "anna@example.org", "--anbieter", "standard"}, "nicht gefunden"},
	} {
		t.Run(name, func(t *testing.T) {
			err := runNutzerMit(context.Background(), f.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), f.want) {
				t.Fatalf("Fehler %v, erwartet %q", err, f.want)
			}
		})
	}
}
