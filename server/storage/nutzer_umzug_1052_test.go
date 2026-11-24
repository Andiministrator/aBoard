package storage

// Tests zum Umzug eines Kontos auf eine andere Adresse (Karte #1052,
// ADR-0148 §7 Schritt 3). Der Kern ist TestNutzerUmzug_SchemaAlleSpalten: Er
// läuft per sucheReste (user_loeschung_test.go) über JEDE Spalte der
// Datenbank. Eine künftige Migration mit einer neuen Spalte, die die Adresse
// trägt, lässt ihn rot werden, bis sie in NutzerUmziehen eingeordnet oder hier
// begründet als historisch erlaubt ist.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

const umzugNeu = "anna.neu@example.net"

// momentaufnahme gibt den Inhalt aller Tabellen als Text zurück (Tabellen
// sortiert, Zeilen in Tabellenreihenfolge) — zum Nachweis „nichts geschrieben“.
// Tabellen aus `ohne` bleiben draußen.
func momentaufnahme(t *testing.T, db *sql.DB, ohne ...string) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tabellen []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tabellen = append(tabellen, n)
	}
	rows.Close()
	var sb strings.Builder
	for _, tab := range tabellen {
		uebersprungen := false
		for _, o := range ohne {
			uebersprungen = uebersprungen || o == tab
		}
		if uebersprungen {
			continue
		}
		r, err := db.Query(fmt.Sprintf(`SELECT * FROM "%s" ORDER BY rowid`, tab))
		if err != nil {
			// Tabellen ohne rowid gibt es hier nicht; sonst ohne Ordnung lesen.
			r, err = db.Query(fmt.Sprintf(`SELECT * FROM "%s"`, tab))
			if err != nil {
				t.Fatal(err)
			}
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			_ = r.Scan(ptr...)
			fmt.Fprintf(&sb, "%s|%v\n", tab, vals)
		}
		r.Close()
	}
	return sb.String()
}

func umzugDB(t *testing.T) *sql.DB {
	t.Helper()
	db := loeschDB(t)
	befuelle(t, db)
	return db
}

func TestNutzerUmzug_SchemaAlleSpalten(t *testing.T) {
	db := umzugDB(t)

	// Gegenprobe des Suchlaufs: vorher muss er A in jeder Tabelle finden, die
	// die Adresse als Schlüssel führt — sonst beweist „kein Rest“ nichts.
	vorher := map[string]bool{}
	for _, f := range sucheReste(t, db, loeschA) {
		vorher[f.tabelle+"."+f.spalte] = true
	}
	for _, ts := range []string{"users.email", "acl.user_email", "schleuse_auftraege.user_email",
		"schleuse_auftraege.freigegeben_von", "schleuse_auftraege.zweit_freigegeben_von",
		"llm_conversations.created_by", "sitzungen.email", "konten.email"} {
		if !vorher[ts] {
			t.Errorf("Suchlauf findet A vor dem Umzug nicht in %s — Befüllung oder Suchlauf kaputt", ts)
		}
	}

	erg, err := NutzerUmziehen(context.Background(), db, "  Anna@Example.com ", " ANNA.Neu@Example.NET", false)
	if err != nil {
		t.Fatalf("NutzerUmziehen: %v", err)
	}
	if erg.Von != loeschA || erg.Nach != umzugNeu {
		t.Fatalf("Adressen nicht normalisiert: %+v", erg)
	}

	// Erlaubte Reste der alten Adresse — jede Ausnahme mit Grund:
	erlaubt := func(f fundstelle) (bool, string) {
		switch {
		case f.tabelle == "activities":
			return true, "Protokoll bleibt unverändert (actor, note, from_val, to_val, on_behalf_of) — ADR-0148 §7, bekannte Grenze"
		case f.tabelle == "boards" && f.spalte == "state_json":
			return true, "Board-Inhalte (Karten-Owner, Kommentar-Autoren) bleiben unverändert — bekannte Grenze"
		case f.tabelle == "mail_autoreply_log" && f.spalte == "sender_email":
			return true, "fremder Absender am Service-Board, kein Konto-Schlüssel; Verlauf der Auto-Replies"
		case f.tabelle == "konten" && f.spalte == "gemeldete_email":
			return true, "was der Anbieter beim Einlösen gemeldet hat, kein Schlüssel; Prüfgrundlage für den Admin (ADR-0149 §5.5)"
		}
		return false, ""
	}
	for _, f := range sucheReste(t, db, loeschA) {
		if ok, _ := erlaubt(f); ok {
			continue
		}
		t.Errorf("Rest der alten Adresse in %s.%s: %q — Spalte in NutzerUmziehen einordnen oder hier begründet erlauben", f.tabelle, f.spalte, f.wert)
	}

	// Die neue Adresse steht dort, wo die alte stand; die Zeilen sind dieselben.
	nachher := map[string]bool{}
	for _, f := range sucheReste(t, db, umzugNeu) {
		nachher[f.tabelle+"."+f.spalte] = true
	}
	for ts := range vorher {
		if strings.HasPrefix(ts, "activities.") || ts == "boards.state_json" || ts == "mail_autoreply_log.sender_email" || ts == "konten.gemeldete_email" {
			continue
		}
		if ts == "sitzungen.email" {
			if nachher[ts] {
				t.Errorf("Sitzungen der alten Adresse müssen weg sein, nicht umgeschrieben")
			}
			continue
		}
		if !nachher[ts] {
			t.Errorf("neue Adresse fehlt in %s", ts)
		}
	}

	// users.id bleibt (daran hängen Tokens, Benachrichtigungen, iCal, LLM-Schlüssel).
	if zaehle(t, db, `SELECT COUNT(*) FROM users WHERE id = ? AND email = ? AND is_admin = 0`, loeschAID, umzugNeu) != 1 {
		t.Errorf("users-Zeile von A: id/is_admin nicht erhalten")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM agent_tokens WHERE user_id = ?`, loeschAID) != 1 {
		t.Errorf("Agenten-Token von A hängt nicht mehr an user_id")
	}
	// Die Bindungen tragen keine Adresse (Migration 0046); sie folgen users.id
	// und konten.id von selbst.
	if zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = ? AND iss = 'https://accounts.google.com' AND sub = '1234'`, loeschAID) != 1 ||
		zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen b JOIN konten k ON k.id = b.konto_id WHERE k.email = ?`, umzugNeu) != 1 {
		t.Errorf("Anbieter-Bindungen folgen der Person nicht")
	}
	// Sitzungen: Annas weg, Carlas bleibt.
	if erg.SitzungenGeloescht != 1 || zaehle(t, db, `SELECT COUNT(*) FROM sitzungen`) != 1 {
		t.Errorf("Sitzungen: gelöscht %d, übrig %d, erwartet 1 und 1", erg.SitzungenGeloescht, zaehle(t, db, `SELECT COUNT(*) FROM sitzungen`))
	}
	// Mitnutzerin C bleibt unberührt, Anzahl der Boards/ACL-Zeilen gleich.
	if zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, loeschC) != 2 || zaehle(t, db, `SELECT COUNT(*) FROM acl`) != 5 {
		t.Errorf("ACL-Zeilen: Carla verändert oder Zeilenzahl nicht erhalten")
	}
	// Historisch: das Protokoll nennt A weiter.
	if zaehle(t, db, `SELECT COUNT(*) FROM activities WHERE actor = ?`, loeschA) != 1 {
		t.Errorf("activities.actor wurde angefasst — bleibt laut ADR-0148 §7 unverändert")
	}
}

func TestNutzerUmzug_Trockenlauf(t *testing.T) {
	db := umzugDB(t)
	vorher := momentaufnahme(t, db)
	probe, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, true)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Trockenlauf {
		t.Errorf("Ergebnis nicht als Trockenlauf gekennzeichnet")
	}
	if momentaufnahme(t, db) != vorher {
		t.Fatalf("Trockenlauf hat geschrieben")
	}
	if probe.Gezaehlt["users.email"] != 1 || probe.Gezaehlt["acl.user_email"] != 3 || probe.Gezaehlt["llm_conversations.created_by"] != 2 ||
		probe.Gezaehlt["konten.email"] != 1 || probe.SitzungenGeloescht != 1 {
		t.Errorf("Zahlen des Trockenlaufs unerwartet: %+v", probe)
	}
	echt, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, false)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(echt.Gezaehlt) != fmt.Sprint(probe.Gezaehlt) || echt.SitzungenGeloescht != probe.SitzungenGeloescht {
		t.Errorf("Trockenlauf nannte %v / %d, der echte Lauf änderte %v / %d", probe.Gezaehlt, probe.SitzungenGeloescht, echt.Gezaehlt, echt.SitzungenGeloescht)
	}
}

func TestNutzerUmzug_Rueckweg(t *testing.T) {
	db := umzugDB(t)
	// Sitzungen sind nach dem Umzug bewusst weg. Groß-/Kleinschreibung gleichen
	// wir an: Der Umzug normalisiert die Altbestände (ANNA@example.com), der
	// Rückweg stellt sie nicht in der alten Schreibweise her.
	// server_config_audit bleibt draußen: Jeder Umzug hängt einen Eintrag an (F-2829).
	vorher := strings.ToLower(momentaufnahme(t, db, "sitzungen", "server_config_audit"))
	if _, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, false); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(momentaufnahme(t, db, "sitzungen", "server_config_audit")) == vorher {
		t.Fatalf("Umzug hat nichts geändert")
	}
	if _, err := NutzerUmziehen(context.Background(), db, umzugNeu, loeschA, false); err != nil {
		t.Fatalf("Rückweg: %v", err)
	}
	if got := strings.ToLower(momentaufnahme(t, db, "sitzungen", "server_config_audit")); got != vorher {
		t.Fatalf("Rückweg stellt den Stand nicht her.\nvorher:\n%s\nnachher:\n%s", vorher, got)
	}
}

func TestNutzerUmzug_Abbrueche_SchreibenNichts(t *testing.T) {
	ctx := context.Background()
	for _, trocken := range []bool{false, true} {
		t.Run(fmt.Sprintf("trockenlauf=%v", trocken), func(t *testing.T) {
			db := umzugDB(t)
			vorher := momentaufnahme(t, db)
			pruefe := func(name string, von, nach string, ziel error) {
				t.Helper()
				_, err := NutzerUmziehen(ctx, db, von, nach, trocken)
				if !errors.Is(err, ziel) {
					t.Errorf("%s: Fehler %v, erwartet %v", name, err, ziel)
				}
				if momentaufnahme(t, db) != vorher {
					t.Errorf("%s: es wurde geschrieben", name)
				}
			}
			pruefe("Ziel hat Konto", loeschA, loeschC, ErrUmzugZielBesetzt)
			pruefe("Ziel hat Konto, andere Schreibweise", loeschA, " CARLA@example.com", ErrUmzugZielBesetzt)
			pruefe("Quelle ohne Konto", "niemand@example.com", umzugNeu, ErrUmzugQuelleFehlt)
			pruefe("Domain-Subjekt als --von", "@example.com", umzugNeu, ErrUmzugAdresse)
			pruefe("Domain-Subjekt als --nach", loeschA, "@example.net", ErrUmzugAdresse)
			pruefe("kein @", loeschA, "anna", ErrUmzugAdresse)
			pruefe("leer", "", umzugNeu, ErrUmzugAdresse)
			pruefe("gleiche Adresse", loeschA, " ANNA@example.com", ErrUmzugAdresse)
		})
	}
}

func TestNutzerUmzug_ZielMitKontoBrichtAb(t *testing.T) {
	db := umzugDB(t)
	// Die Zieladresse hat (noch) keine users-Zeile, ist aber eingeladen
	// (ADR-0149 §5.2). Seit Migration 0046 tragen Bindungen keine Adresse;
	// die Einladung ist der Fall, der vorher die Bindung war.
	mustExec(t, db, `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('konto-ziel', ?, 'invited', 1, 1)`, umzugNeu)
	vorher := momentaufnahme(t, db)
	_, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, false)
	if !errors.Is(err, ErrUmzugZielBesetzt) || !strings.Contains(err.Error(), "konten") {
		t.Fatalf("Fehler %v, erwartet ErrUmzugZielBesetzt mit Hinweis auf konten", err)
	}
	if momentaufnahme(t, db) != vorher {
		t.Fatalf("es wurde geschrieben")
	}
}

func TestNutzerUmzug_AclKonfliktListetBoards(t *testing.T) {
	db := umzugDB(t)
	// Der neuen Adresse wurde vorab ein Board freigegeben, auf dem A auch steht
	// (boardFremd: A Editorin), und zwei weitere — eines mit A, eines ohne.
	mustExec(t, db, `INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, 'viewer', '2026-01-01')`, boardFremd, umzugNeu)
	mustExec(t, db, `INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, 'viewer', '2026-01-01')`, boardGeteil, umzugNeu)
	vorher := momentaufnahme(t, db)
	_, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, false)
	var k *UmzugAclKonflikt
	if !errors.As(err, &k) {
		t.Fatalf("Fehler %v, erwartet *UmzugAclKonflikt", err)
	}
	want := []string{boardFremd, boardGeteil}
	sort.Strings(want)
	if fmt.Sprint(k.Boards) != fmt.Sprint(want) {
		t.Errorf("Boards im Konflikt: %v, erwartet %v", k.Boards, want)
	}
	if momentaufnahme(t, db) != vorher {
		t.Fatalf("es wurde geschrieben")
	}
}

func TestNutzerUmzug_FremdeAclDerNeuenAdresseOhneKonfliktBleibt(t *testing.T) {
	db := umzugDB(t)
	// Das Board hat A nicht als Eintrag: kein Konflikt, der Eintrag der neuen
	// Adresse bleibt, wie er ist.
	mustExec(t, db, `INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('board-frei', 'T', '1', '{}', 1, 1)`)
	mustExec(t, db, `INSERT INTO acl (board_id, user_email, role, created) VALUES ('board-frei', ?, 'viewer', '2026-01-01')`, umzugNeu)
	if _, err := NutzerUmziehen(context.Background(), db, loeschA, umzugNeu, false); err != nil {
		t.Fatal(err)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE board_id = 'board-frei' AND user_email = ? AND role = 'viewer'`, umzugNeu) != 1 {
		t.Errorf("Eintrag der neuen Adresse verändert")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, umzugNeu) != 4 {
		t.Errorf("erwartet drei umgeschriebene plus ein vorhandener ACL-Eintrag")
	}
}

func TestNutzerUmzug_AdminFlagBleibt(t *testing.T) {
	db := umzugDB(t)
	if _, err := NutzerUmziehen(context.Background(), db, loeschB, "bert.neu@example.net", false); err != nil {
		t.Fatal(err)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM users WHERE id = ? AND email = 'bert.neu@example.net' AND is_admin = 1`, loeschBID) != 1 {
		t.Errorf("is_admin ging beim Umzug verloren")
	}
}

// F-2829: Der Umzug hinterlässt einen adressfreien Audit-Eintrag; der Trockenlauf
// und jeder Abbruch keinen.
func TestNutzerUmzug_Audit_F2829(t *testing.T) {
	db := umzugDB(t)
	ctx := context.Background()
	zahl := func(q string, a ...any) int { return zaehle(t, db, q, a...) }
	vorher := zahl(`SELECT COUNT(*) FROM server_config_audit WHERE action = ?`, AuditActionUserUmzug)
	if _, err := NutzerUmziehen(ctx, db, loeschA, umzugNeu, true); err != nil {
		t.Fatal(err)
	}
	if _, err := NutzerUmziehen(ctx, db, loeschA, loeschA, false); err == nil {
		t.Fatal("gleiche Adresse hätte abbrechen müssen")
	}
	if n := zahl(`SELECT COUNT(*) FROM server_config_audit WHERE action = ?`, AuditActionUserUmzug); n != vorher {
		t.Fatalf("Trockenlauf/Abbruch hat Audit geschrieben (%d -> %d)", vorher, n)
	}
	if _, err := NutzerUmziehen(ctx, db, loeschA, umzugNeu, false); err != nil {
		t.Fatal(err)
	}
	if n := zahl(`SELECT COUNT(*) FROM server_config_audit WHERE action = ? AND key = ? AND user_id = '' AND old_value_redacted IS NULL AND new_value_redacted IS NULL AND ts <> ''`,
		AuditActionUserUmzug, "users/"+loeschAID); n != 1 {
		t.Fatalf("erwartet genau 1 adressfreien Umzugs-Eintrag, gefunden %d", n)
	}
	// Kein Audit-Wert (Schlüssel, Akteur, Werte) trägt eine der beiden Adressen.
	for _, f := range sucheReste(t, db, loeschA, umzugNeu) {
		if f.tabelle == "server_config_audit" {
			t.Errorf("Adresse im Audit: %s.%s = %q", f.tabelle, f.spalte, f.wert)
		}
	}
	// Die Löschung der umgezogenen Person findet im Audit danach nichts außer
	// den dokumentierten users/<id>-Schlüsseln (ID, keine Adresse).
	if _, err := NewUserRepo(db).Loeschen(ctx, LoeschAnweisung{UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB, Uebertragen: map[string]string{boardEigen: loeschB}}); err != nil {
		t.Fatalf("Loeschen nach Umzug: %v", err)
	}
	for _, f := range sucheReste(t, db, umzugNeu, loeschA, loeschAID) {
		// Nur das Audit zählt hier; activities/Kommentare behalten nach dem
		// Umzug die alte Adresse (dokumentierte Grenze im Kopf von nutzer_umzug.go).
		if f.tabelle != "server_config_audit" {
			continue
		}
		if f.spalte == "key" && f.wert == "users/"+loeschAID {
			continue
		}
		t.Errorf("Rest nach Umzug+Löschung in %s.%s: %q", f.tabelle, f.spalte, f.wert)
	}
}
