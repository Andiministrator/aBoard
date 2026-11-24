package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Karte #1052: `nutzer umziehen --von <alt> --nach <neu> [--dry-run]`.
func nutzerDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pfad := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-andi', 'andi@alt.example', 'Andi', '2026-01-01', 1)`,
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-bert', 'bert@example.com', 'Bert', '2026-01-01', 0)`,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'T', '1', '{}', 1, 1)`,
		`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'andi@alt.example', 'owner', '2026-01-01')`,
		`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('https://accounts.google.com', '42', 'standard', 'u-andi', 1)`,
		`INSERT INTO sitzungen (id_hash, email, angelegt, zuletzt, ablauf) VALUES ('` + strings.Repeat("a", 64) + `', 'andi@alt.example', 1, 1, 9999999999)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return pfad
}

func nutzerLauf(pfad string, args ...string) (string, error) {
	var out bytes.Buffer
	err := runNutzerMit(context.Background(), append(append([]string{}, args...), "--db", pfad), &out)
	return out.String(), err
}

func nutzerAnzahl(t *testing.T, pfad, q string, args ...any) int {
	t.Helper()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunNutzer_UmziehenDryRunEchtRueckweg(t *testing.T) {
	pfad := nutzerDB(t)
	const alt, neu = "andi@alt.example", "andi@neu.example"

	aus, err := nutzerLauf(pfad, "umziehen", "--von", " Andi@Alt.example", "--nach", neu, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, aus)
	}
	for _, w := range []string{"Trockenlauf", alt + " -> " + neu, "users.email", "acl.user_email", "konten.email", "sitzungen (gelöscht"} {
		if !strings.Contains(aus, w) {
			t.Errorf("dry-run-Ausgabe ohne %q:\n%s", w, aus)
		}
	}
	if nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM users WHERE email = ?`, alt) != 1 || nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM sitzungen`) != 1 {
		t.Fatalf("dry-run hat geschrieben")
	}

	aus, err = nutzerLauf(pfad, "umziehen", "--von", alt, "--nach", neu)
	if err != nil || !strings.HasPrefix(aus, "Umgezogen:") {
		t.Fatalf("Umzug: %v\n%s", err, aus)
	}
	if nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM users WHERE email = ? AND is_admin = 1`, neu) != 1 ||
		nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, neu) != 1 ||
		nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM anbieter_bindungen b JOIN users u ON u.id = b.user_id WHERE u.email = ? AND b.sub = '42'`, neu) != 1 ||
		nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM sitzungen`) != 0 {
		t.Fatalf("Umzug unvollständig:\n%s", aus)
	}

	// Rückweg = vertauschte Adressen.
	if aus, err := nutzerLauf(pfad, "umziehen", "--von", neu, "--nach", alt); err != nil {
		t.Fatalf("Rückweg: %v\n%s", err, aus)
	}
	if nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM users WHERE email = ?`, alt) != 1 ||
		nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, alt) != 1 ||
		nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM anbieter_bindungen b JOIN users u ON u.id = b.user_id WHERE u.email = ?`, alt) != 1 {
		t.Fatalf("Rückweg stellt den Stand nicht her")
	}
}

func TestRunNutzer_Abbrueche(t *testing.T) {
	pfad := nutzerDB(t)
	for name, args := range map[string][]string{
		"Ziel hat Konto":      {"umziehen", "--von", "andi@alt.example", "--nach", "bert@example.com"},
		"Domain-Subjekt nach": {"umziehen", "--von", "andi@alt.example", "--nach", "@example.net"},
		"Domain-Subjekt von":  {"umziehen", "--von", "@alt.example", "--nach", "andi@neu.example"},
		"Quelle ohne Konto":   {"umziehen", "--von", "nie@example.com", "--nach", "andi@neu.example"},
		"ohne --nach":         {"umziehen", "--von", "andi@alt.example"},
		"unbekannter Befehl":  {"loeschen", "--von", "a@b.de", "--nach", "c@d.de"},
		"ohne Befehl":         {},
		"Rest nach Optionen":  {"umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example", "--dry-run", "extra"},
	} {
		if aus, err := nutzerLauf(pfad, args...); err == nil {
			t.Errorf("%s: kein Fehler\n%s", name, aus)
		}
	}
	if nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM users WHERE email = 'andi@alt.example'`) != 1 {
		t.Errorf("ein Abbruch hat geschrieben")
	}
}

func TestRunNutzer_AclKonfliktNenntBoards(t *testing.T) {
	pfad := nutzerDB(t)
	db, _ := storage.Open(context.Background(), storage.Config{DBPath: pfad})
	if _, err := db.Exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'andi@neu.example', 'viewer', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	aus, err := nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example")
	if err == nil || !strings.Contains(aus, "nichts geschrieben") || !strings.Contains(aus, "  b1") {
		t.Fatalf("erwartet Abbruch mit Board-Liste: %v\n%s", err, aus)
	}
	if nutzerAnzahl(t, pfad, `SELECT COUNT(*) FROM users WHERE email = 'andi@alt.example'`) != 1 {
		t.Errorf("Konflikt-Abbruch hat geschrieben")
	}
}

func TestRunNutzer_FehlendeDatenbank(t *testing.T) {
	pfad := filepath.Join(t.TempDir(), "gibt-es-nicht.db")
	if _, err := nutzerLauf(pfad, "umziehen", "--von", "a@b.de", "--nach", "c@d.de"); err == nil {
		t.Fatal("fehlende Datenbank muss scheitern")
	}
	if _, err := os.Stat(pfad); err == nil {
		t.Fatal("es wurde eine leere Datenbank angelegt")
	}
}

func TestUnterbefehle_NutzerBekannt(t *testing.T) {
	found := false
	for _, u := range unterbefehle {
		found = found || u == "nutzer"
	}
	if !found {
		t.Fatal("nutzer fehlt in unterbefehle (Tippfehler-Schutz)")
	}
}

// Kritik A-7: Der Umzug erinnert an die Zulassungsliste, und ein schon
// belegtes Ziel nennt den Weg.
func TestRunNutzer_HinweiseNachUmzug(t *testing.T) {
	pfad := nutzerDB(t)
	aus, err := nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "bert@example.com")
	if err == nil || !strings.Contains(aus, "Admin-Bereich löschen") {
		t.Errorf("belegtes Ziel ohne Hinweis: %v\n%s", err, aus)
	}
	t.Setenv("ABOARD_ANMELDUNG", "")
	aus, err = nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example", "--dry-run")
	if err != nil || strings.Contains(aus, "ABOARD_OIDC_ZUGELASSEN") {
		t.Errorf("Zulassungs-Hinweis im Einzelbetrieb (Verify V-3): %v\n%s", err, aus)
	}
	t.Setenv("ABOARD_ANMELDUNG", "oidc")
	aus, err = nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example")
	if err != nil || !strings.Contains(aus, "ABOARD_OIDC_ZUGELASSEN") {
		t.Errorf("Umzug ohne Hinweis auf die Zulassungsliste: %v\n%s", err, aus)
	}
}

// K2 F-E (Sprint-Abschluss 106–108): Steht die alte Adresse in
// ABOARD_BOOTSTRAP_OWNER, erinnert der Umzug daran — sonst nicht.
func TestRunNutzer_HinweisBootstrapOwner(t *testing.T) {
	pfad := nutzerDB(t)
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "")
	aus, err := nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example", "--dry-run")
	if err != nil || strings.Contains(aus, "ABOARD_BOOTSTRAP_OWNER") {
		t.Errorf("Bootstrap-Hinweis ohne Bootstrap-Owner: %v\n%s", err, aus)
	}
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "Andi@Alt.example")
	aus, err = nutzerLauf(pfad, "umziehen", "--von", "andi@alt.example", "--nach", "andi@neu.example", "--dry-run")
	if err != nil || !strings.Contains(aus, "ABOARD_BOOTSTRAP_OWNER steht noch auf andi@alt.example") {
		t.Errorf("Umzug der Bootstrap-Adresse ohne Hinweis: %v\n%s", err, aus)
	}
}
