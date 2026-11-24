package storage

// Tests zum Löschpfad für Nutzer (Karte #571, ADR-0129 §5 A-8).
//
// Der Kern ist sucheReste: Er läuft über JEDE Tabelle und JEDE Spalte der
// Datenbank (sqlite_master + pragma_table_info) und meldet jede Fundstelle
// von E-Mail oder ID des gelöschten Nutzers. Eine Tabelle, die eine künftige
// Migration hinzufügt und die der Löschpfad nicht kennt, fällt hier auf —
// ohne dass jemand diesen Test anpassen muss.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const (
	loeschA     = "anna@example.com" // wird gelöscht
	loeschAID   = "user-anna"
	loeschB     = "bert@example.com" // Admin, führt aus
	loeschBID   = "user-bert"
	loeschC     = "carla@example.com" // Mitnutzerin
	loeschCID   = "user-carla"
	boardEigen  = "board-anna-eigen" // A einzige Ownerin
	boardGeteil = "board-geteilt"    // A und C Ownerinnen
	boardFremd  = "board-carla"      // C Ownerin, A Editorin
)

func loeschDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// befuelle legt A mit Daten in jeder Tabelle an, die Nutzerbezug hat.
func befuelle(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, u := range [][3]any{{loeschAID, loeschA, 0}, {loeschBID, loeschB, 1}, {loeschCID, loeschC, 0}} {
		mustExec(t, db, `INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u[0], u[1], u[2])
	}
	// state_json von boardEigen nennt A — Board-Inhalte bleiben bewusst
	// unangetastet (Entscheid des Maintainers 2026-09-29).
	for _, b := range [][2]string{{boardEigen, `{"owner":"` + loeschA + `"}`}, {boardGeteil, `{}`}, {boardFremd, `{}`}} {
		mustExec(t, db, `INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, '1', ?, 1, 1)`, b[0], "Titel "+b[0], b[1])
	}
	for _, a := range [][3]string{
		{boardEigen, loeschA, "owner"},
		{boardGeteil, loeschA, "owner"}, {boardGeteil, loeschC, "owner"},
		{boardFremd, loeschC, "owner"}, {boardFremd, loeschA, "editor"},
	} {
		mustExec(t, db, `INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, ?, '2026-01-01')`, a[0], a[1], a[2])
	}
	// activities: A als Akteur, A als Gegenstand einer ACL-Änderung,
	// ein Legacy-Bypass-Eintrag (actor '') und einer von C.
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, ts) VALUES ('act-1', ?, ?, 'card-move', '2026-01-01')`, boardFremd, loeschA)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, field, to_val, note, ts) VALUES ('act-2', ?, ?, 'acl-set', 'role', 'editor', ?, '2026-01-01')`, boardFremd, loeschC, loeschA)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, from_val, ts) VALUES ('act-3', ?, ?, 'acl-deleted', ?, '2026-01-01')`, boardFremd, loeschC, loeschA)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, ts) VALUES ('act-legacy', ?, '', 'card-move', '2026-01-01')`, boardFremd)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, ts) VALUES ('act-c', ?, ?, 'card-move', '2026-01-01')`, boardFremd, loeschC)

	mustExec(t, db, `INSERT INTO llm_api_keys (user_id, provider, key_encrypted, created_at) VALUES (?, 'anthropic', 'x', '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO llm_conversations (id, board_id, card_id, provider, model, created_by, created_at) VALUES ('conv-a', ?, 'k1', 'anthropic', 'm', ?, '2026-01-01')`, boardFremd, loeschA)
	mustExec(t, db, `INSERT INTO llm_turns (id, conversation_id, ts, role, content) VALUES ('turn-a', 'conv-a', '2026-01-01', 'user', 'hallo')`)
	mustExec(t, db, `INSERT INTO llm_conversations (id, board_id, card_id, provider, model, created_by, created_at) VALUES ('conv-c', ?, 'k1', 'anthropic', 'm', ?, '2026-01-01')`, boardFremd, loeschC)
	mustExec(t, db, `INSERT INTO notification_subscriptions (user_id, event_type, channel, enabled, updated_at) VALUES (?, 'card-assigned', 'email', 1, '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO notification_config (user_id, channel, config_encrypted, created_at, updated_at) VALUES (?, 'gotify', 'x', '2026-01-01', '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO notification_log (id, user_id, event_type, channel, ts, status) VALUES ('nl-a', ?, 'card-assigned', 'email', '2026-01-01', 'sent')`, loeschAID)
	mustExec(t, db, `INSERT INTO ical_subscriptions (id, user_id, board_id, token_hash, token_enc, created_at) VALUES ('ical-a', ?, ?, 'h', 'e', '2026-01-01')`, loeschAID, boardFremd)
	mustExec(t, db, `INSERT INTO server_config (key, value_plain, is_secret, updated_by, updated_at) VALUES ('smtp.host', 'mail', 0, ?, '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO server_config_audit (id, key, user_id, action, ts) VALUES ('aud-a', 'smtp.host', ?, 'set', '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by) VALUES ('tok-a', 'bot', ?, 'editor', 'th', '2026-01-01', ?)`, boardFremd, loeschAID)
	// Karte #608: ein Token, das A gehört (Agent „für A“), und ein Protokoll-
	// eintrag eines Agenten für A (on_behalf_of) — beides muss die Löschung
	// erfassen (Widerruf, Pseudonym).
	mustExec(t, db, `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, user_id, expires_at, kennung) VALUES ('tok-person-a', 'annas-claude', ?, 'editor', 'th2', '2026-01-01', ?, ?, '2099-01-01T00:00:00Z', 'aaaaaaaaaa')`, boardFremd, loeschAID, loeschAID)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, ts, actor_kind, on_behalf_of, session) VALUES ('act-fuer-a', ?, 'agent:annas-claude', 'op', '2026-01-01', 'agent', ?, 's1')`, boardFremd, loeschA)
	mustExec(t, db, `INSERT INTO mail_spam_senders (sender_email, added_at, added_by) VALUES ('spam@example.net', '2026-01-01', ?)`, loeschAID)
	// Karte #1051 (ADR-0148 §2): Sitzungen der eingebauten Anmeldung von A und
	// C, dazu Annas (iss, sub)-Bindung. Annas gehen mit ihr, Carlas bleibt.
	mustExec(t, db, `INSERT INTO sitzungen (id_hash, email, angelegt, zuletzt, ablauf) VALUES (?, ?, 1, 1, 9999999999)`, strings.Repeat("a", 64), loeschA)
	mustExec(t, db, `INSERT INTO sitzungen (id_hash, email, angelegt, zuletzt, ablauf) VALUES (?, ?, 1, 1, 9999999999)`, strings.Repeat("c", 64), loeschC)
	// ADR-0149 §7 (Karte #1069): A hat eine Anbieter-Bindung und ein Konto mit
	// Einladung und Wiederherstellungscode. Carla hat eine Bindung (bleibt).
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('https://accounts.google.com', '1234', 'standard', ?, 1)`, loeschAID)
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('https://accounts.google.com', '5678', 'standard', ?, 1)`, loeschCID)
	mustExec(t, db, `INSERT INTO konten (id, email, zustand, weg, gemeldete_email, angelegt, geaendert) VALUES ('konto-a', ?, 'active', 'google', ?, 1, 1)`, loeschA, loeschA)
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES ('https://auth.example', 'a-1', 'authelia', 'konto-a', 1)`)
	mustExec(t, db, `INSERT INTO einladungen (token_hash, konto_id, zweck, angelegt, ablauf) VALUES (?, 'konto-a', 'invite', 1, 2)`, strings.Repeat("e", 64))
	mustExec(t, db, `INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES ('konto-a', ?, 1)`, strings.Repeat("d", 64))
	// Karte #748 (Kritiker K-1): A hat einen Zugang des Push-Skripts angelegt.
	mustExec(t, db, `INSERT INTO kalender_ausgang_tokens (id, name, token_hash, created_at, created_by) VALUES ('kat-a', 'push', 'kh', '2026-01-01', ?)`, loeschAID)
	// Karte #788 (Vorprüfung, Lücke 3): A hat als Admin im Dialog gekoppelt
	// (innen und außen) und einen Zugang des Board-Paars angelegt.
	mustExec(t, db, `INSERT INTO boardpaar_innen (board_id, seit, von) VALUES (?, '2026-01-01', ?)`, boardFremd, loeschAID)
	mustExec(t, db, `INSERT INTO boardpaar_aussen (aussen_board_id, innen_link_basis, seit, von) VALUES (?, 'https://innen.example/#board=b', '2026-01-01', ?)`, boardFremd, loeschAID)
	mustExec(t, db, `INSERT INTO boardpaar_tokens (id, typ, name, token_hash, created_at, created_by) VALUES ('bpt-a', 'abs', 'push', 'bh', '2026-01-01', ?)`, loeschAID)
	// A schrieb einmal ans Service-Board (K-A2), und ein Altbestand führt A in
	// anderer Schreibweise (K-A3) — beides muss die Löschung erfassen.
	mustExec(t, db, `INSERT INTO mail_autoreply_log (sender_email, card_id, sent_at) VALUES (?, 'k1', '2026-01-01')`, loeschA)
	mustExec(t, db, `INSERT INTO activities (id, board_id, actor, action, ts) VALUES ('act-gross', ?, 'Anna@Example.COM', 'card-move', '2026-01-01')`, boardFremd)
	mustExec(t, db, `INSERT INTO llm_conversations (id, board_id, card_id, provider, model, created_by, created_at) VALUES ('conv-gross', ?, 'k2', 'anthropic', 'm', 'ANNA@example.com', '2026-01-01')`, boardFremd)
	// Karte #684 (ADR-0134 §2, §4): A hat als Admin-Vorgängerin eine
	// Freigabe angelegt und bestätigt und einen eigenen Schlüssel hinterlegt;
	// C hat ebenfalls einen (bleibt).
	mustExec(t, db, `INSERT INTO schleuse_freigaben (id, anbieter, basis_url, vertragsform_bestaetigt, vertragsform_bestaetigt_am, vertragsform_bestaetigt_von, erstellt_am, erstellt_von, geaendert_am, geaendert_von) VALUES ('fg-1', 'anthropic', 'https://api.anthropic.com', 1, '2026-01-01', ?, '2026-01-01', ?, '2026-01-01', ?)`, loeschAID, loeschAID, loeschAID)
	mustExec(t, db, `INSERT INTO schleuse_schluessel (user_id, freigabe_id, key_encrypted, geschaeftskonto_bestaetigt_am, erstellt_am) VALUES (?, 'fg-1', 'x', '2026-01-01', '2026-01-01')`, loeschAID)
	mustExec(t, db, `INSERT INTO schleuse_schluessel (user_id, freigabe_id, key_encrypted, geschaeftskonto_bestaetigt_am, erstellt_am) VALUES (?, 'fg-1', 'y', '2026-01-01', '2026-01-01')`, loeschCID)
	// Karte #685 (ADR-0134 §4): je ein Auftrag von A und C — Annas geht samt
	// Zuordnung mit ihr, Carlas bleibt.
	for _, z := range [][3]string{{"auf-a", loeschAID, loeschA}, {"auf-c", loeschCID, "carla@example.com"}} {
		mustExec(t, db, `INSERT INTO schleuse_auftraege (id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell, text, text_sha256, zuordnung_verschluesselt, erstellt_am, geaendert_am) VALUES (?, ?, 'k1', ?, ?, 'fg-1', 'anthropic', 'm', 't', 'h', 'enc', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, z[0], boardFremd, z[1], z[2])
	}
	// Karte #686: Ein gesendeter Auftrag von A ist Protokoll — er bleibt,
	// pseudonymisiert und ohne Zuordnung. In Carlas Auftrag hat A als zweite
	// Person freigegeben.
	mustExec(t, db, `INSERT INTO schleuse_auftraege (id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell, zustand, text, text_sha256, zuordnung_verschluesselt, freigabe_sha256, freigegeben_von, freigegeben_am, gesendet_am, erstellt_am, geaendert_am) VALUES ('auf-a-gesendet', ?, 'k1', ?, ?, 'fg-1', 'anthropic', 'm', 'beantwortet', 't', 'h', 'enc', 'h', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, boardFremd, loeschAID, loeschA, loeschA)
	mustExec(t, db, `UPDATE schleuse_auftraege SET zustand = 'freigegeben', freigabe_sha256 = 'h', freigegeben_von = 'carla@example.com', freigegeben_am = '2026-01-01T00:00:00.000Z', vier_augen = 1, zweit_freigegeben_von = ? WHERE id = 'auf-c'`, strings.ToUpper(loeschA))
}

// fundstelle ist eine Zeile, in der E-Mail oder ID noch vorkommt.
type fundstelle struct{ tabelle, spalte, wert string }

// sucheReste durchsucht jede Spalte jeder Tabelle nach den Nadeln.
func sucheReste(t *testing.T, db *sql.DB, nadeln ...string) []fundstelle {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	var tabellen []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tabellen = append(tabellen, n)
	}
	rows.Close()
	if len(tabellen) < 15 {
		t.Fatalf("nur %d Tabellen gefunden — Suchlauf wäre wertlos", len(tabellen))
	}
	var out []fundstelle
	for _, tab := range tabellen {
		crows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tab)
		if err != nil {
			t.Fatalf("table_info %s: %v", tab, err)
		}
		var spalten []string
		for crows.Next() {
			var n string
			_ = crows.Scan(&n)
			spalten = append(spalten, n)
		}
		crows.Close()
		for _, sp := range spalten {
			for _, nadel := range nadeln {
				// lower() auf beiden Seiten: ein Rest in anderer Schreibweise
				// ist auch ein Rest (K-A3).
				q := fmt.Sprintf(`SELECT CAST("%s" AS TEXT) FROM "%s" WHERE instr(lower(CAST("%s" AS TEXT)), lower(?)) > 0`, sp, tab, sp)
				vrows, err := db.Query(q, nadel)
				if err != nil {
					t.Fatalf("suche %s.%s: %v", tab, sp, err)
				}
				for vrows.Next() {
					var v string
					_ = vrows.Scan(&v)
					out = append(out, fundstelle{tab, sp, v})
				}
				vrows.Close()
			}
		}
	}
	return out
}

func zaehle(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func TestNutzerLoeschen_VollstaendigUeberAlleTabellen(t *testing.T) {
	db := loeschDB(t)
	befuelle(t, db)
	r := NewUserRepo(db)

	// Gegenprobe des Suchlaufs selbst: vorher muss er A finden, und zwar in
	// jeder befüllten Tabelle — sonst beweist das „nichts gefunden“ danach nichts.
	vorher := map[string]bool{}
	for _, f := range sucheReste(t, db, loeschA, loeschAID) {
		vorher[f.tabelle] = true
	}
	for _, tab := range []string{"users", "acl", "activities", "llm_api_keys", "llm_conversations",
		"notification_subscriptions", "notification_config", "notification_log", "ical_subscriptions",
		"server_config", "server_config_audit", "agent_tokens", "kalender_ausgang_tokens", "mail_spam_senders", "mail_autoreply_log", "boards",
		"schleuse_freigaben", "schleuse_schluessel", "schleuse_auftraege",
		"boardpaar_innen", "boardpaar_aussen", "boardpaar_tokens", "sitzungen", "anbieter_bindungen", "konten"} {
		if !vorher[tab] {
			t.Errorf("Suchlauf findet A vor der Löschung nicht in %s — Befüllung oder Suchlauf kaputt", tab)
		}
	}

	erg, err := r.Loeschen(context.Background(), LoeschAnweisung{
		UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB,
		Uebertragen: map[string]string{boardEigen: "  Bert@Example.com "},
	})
	if err != nil {
		t.Fatalf("Loeschen: %v", err)
	}
	if !strings.HasPrefix(erg.Pseudonym, PseudonymPraefix) || len(erg.Pseudonym) != len(PseudonymPraefix)+12 {
		t.Fatalf("Pseudonym unerwartet: %q", erg.Pseudonym)
	}
	if erg.Uebertragen[boardEigen] != loeschB {
		t.Fatalf("Übertragung nicht normalisiert: %+v", erg.Uebertragen)
	}

	// Kein Rest — außer an den zwei dokumentierten Stellen:
	//  - boards.state_json (Board-Inhalte bleiben unangetastet, Entscheid des Maintainers)
	//  - server_config_audit.key "users/<id>" des Lösch-Eintrags selbst
	for _, f := range sucheReste(t, db, loeschA, loeschAID) {
		if f.tabelle == "boards" && f.spalte == "state_json" {
			continue
		}
		if f.tabelle == "server_config_audit" && f.spalte == "key" && f.wert == "users/"+loeschAID {
			continue
		}
		t.Errorf("Rest von A in %s.%s: %q", f.tabelle, f.spalte, f.wert)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM boards WHERE id = ? AND instr(state_json, ?) > 0`, boardEigen, loeschA) != 1 {
		t.Errorf("state_json wurde angefasst — laut Entscheid bleibt es unverändert")
	}

	// Übertragung: B ist Owner des vormals eigenen Boards, mit Protokoll.
	if zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE board_id = ? AND user_email = ? AND role = 'owner'`, boardEigen, loeschB) != 1 {
		t.Errorf("boardEigen nicht an B übertragen")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM activities WHERE board_id = ? AND action = 'acl-set' AND actor = ? AND note = ?`, boardEigen, loeschB, loeschB) != 1 {
		t.Errorf("Übertragung fehlt im Board-Protokoll")
	}
	// Geteiltes Board bleibt bei C; kein Board verschwindet.
	if zaehle(t, db, `SELECT COUNT(*) FROM boards`) != 3 {
		t.Errorf("ein Board ist verschwunden")
	}

	// Pseudonymisiert statt gelöscht: Anzahl der Protokollzeilen unverändert.
	if n := zaehle(t, db, `SELECT COUNT(*) FROM activities WHERE actor = ? OR note = ? OR from_val = ?`, erg.Pseudonym, erg.Pseudonym, erg.Pseudonym); n != 4 {
		t.Errorf("pseudonymisierte activities: %d, erwartet 4 (drei exakt, einer in Großschreibung)", n)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM activities WHERE id = 'act-legacy' AND actor = ''`) != 1 {
		t.Errorf("Legacy-Bypass-Eintrag (actor '') wurde verändert")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM server_config_audit WHERE id = 'aud-a' AND user_id = ?`,
		`SELECT COUNT(*) FROM server_config WHERE key = 'smtp.host' AND updated_by = ?`,
		`SELECT COUNT(*) FROM agent_tokens WHERE id = 'tok-a' AND created_by = ?`,
		`SELECT COUNT(*) FROM mail_spam_senders WHERE sender_email = 'spam@example.net' AND added_by = ?`,
	} {
		if zaehle(t, db, q, erg.Pseudonym) != 1 {
			t.Errorf("nicht pseudonymisiert: %s", q)
		}
	}
	// Karte #684: Annas Schlüssel ist weg, Carlas bleibt; die Freigabe bleibt
	// und nennt das Pseudonym statt Annas ID.
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_schluessel WHERE user_id = ?`, loeschCID) != 1 {
		t.Errorf("fremder Schleusen-Schlüssel gelöscht")
	}
	// Karte #685: Annas Auftrag ist weg, Carlas bleibt.
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_auftraege WHERE id = 'auf-a'`) != 0 {
		t.Errorf("Schleusen-Auftrag der gelöschten Person noch da")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_auftraege WHERE id = 'auf-c'`) != 1 {
		t.Errorf("fremder Schleusen-Auftrag gelöscht")
	}
	// Karte #686: Der gesendete Auftrag bleibt als Protokoll — mit Pseudonym,
	// ohne Zuordnung; in Carlas Auftrag steht das Pseudonym als zweite Person.
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_auftraege WHERE id = 'auf-a-gesendet' AND user_id = ? AND user_email = ? AND freigegeben_von = ? AND zuordnung_verschluesselt IS NULL`, erg.Pseudonym, erg.Pseudonym, erg.Pseudonym) != 1 {
		t.Errorf("gesendeter Schleusen-Auftrag nicht als pseudonymisiertes Protokoll erhalten")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_auftraege WHERE id = 'auf-c' AND zweit_freigegeben_von = ?`, erg.Pseudonym) != 1 {
		t.Errorf("zweite Freigabe in fremdem Auftrag nicht pseudonymisiert")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM schleuse_freigaben WHERE id = 'fg-1' AND erstellt_von = ? AND geaendert_von = ? AND vertragsform_bestaetigt_von = ?`, erg.Pseudonym, erg.Pseudonym, erg.Pseudonym) != 1 {
		t.Errorf("Schleusen-Freigabe nicht pseudonymisiert")
	}
	// Karte #1051: Carlas Sitzung bleibt.
	if zaehle(t, db, `SELECT COUNT(*) FROM sitzungen WHERE email = ?`, loeschC) != 1 {
		t.Errorf("fremde Sitzung gelöscht")
	}
	// Karte #1069 (ADR-0149 §7): Konto, Einladung, Code und beide Bindungen
	// von A sind weg — auch die nur am Konto hängende, die keine Adresse
	// trägt und deshalb im Suchlauf nicht auffiele. Carlas Bindung bleibt.
	for _, q := range []string{
		`SELECT COUNT(*) FROM konten`, `SELECT COUNT(*) FROM einladungen`, `SELECT COUNT(*) FROM konto_codes`,
		`SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id IS NOT ? OR konto_id IS NOT NULL`,
	} {
		if n := zaehle(t, db, q, loeschCID); n != 0 {
			t.Errorf("%s: %d, erwartet 0", q, n)
		}
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = ? AND sub = '5678'`, loeschCID) != 1 {
		t.Errorf("fremde Anbieter-Bindung gelöscht")
	}
	if erg.Gezaehlt["konten"] != 1 || erg.Gezaehlt["anbieter_bindungen"] != 2 || erg.Gezaehlt["einladungen"] != 1 || erg.Gezaehlt["konto_codes"] != 1 {
		t.Errorf("Zahlen im Ergebnis: %v", erg.Gezaehlt)
	}
	// Daten anderer Nutzer bleiben.
	if zaehle(t, db, `SELECT COUNT(*) FROM llm_conversations WHERE id = 'conv-c'`) != 1 {
		t.Errorf("fremder LLM-Verlauf gelöscht")
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM llm_turns`) != 0 {
		t.Errorf("Turns des gelöschten Verlaufs übrig")
	}
	// Audit-Eintrag der Löschung.
	if zaehle(t, db, `SELECT COUNT(*) FROM server_config_audit WHERE key = ? AND user_id = ? AND action = ? AND new_value_redacted LIKE ?`,
		"users/"+loeschAID, loeschBID, AuditActionUserDelete, erg.Pseudonym+"%") != 1 {
		t.Errorf("Audit-Eintrag der Löschung fehlt")
	}
}

func TestNutzerLoeschen_EinzigerOwner_409OhneAenderung(t *testing.T) {
	db := loeschDB(t)
	befuelle(t, db)
	r := NewUserRepo(db)

	_, err := r.Loeschen(context.Background(), LoeschAnweisung{UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB})
	var einzel *EinzigerOwnerFehler
	if !errors.As(err, &einzel) {
		t.Fatalf("erwartet EinzigerOwnerFehler, bekam %v", err)
	}
	// Nur boardEigen — boardGeteil hat mit C eine zweite Ownerin.
	if len(einzel.Boards) != 1 || einzel.Boards[0].ID != boardEigen || einzel.Boards[0].Title != "Titel "+boardEigen {
		t.Fatalf("Liste unerwartet: %+v", einzel.Boards)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM users WHERE id = ?`, loeschAID) != 1 ||
		zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, loeschA) != 3 ||
		zaehle(t, db, `SELECT COUNT(*) FROM activities WHERE actor = ?`, loeschA) != 1 {
		t.Fatalf("abgelehnte Löschung hat trotzdem etwas geändert")
	}
}

func TestNutzerLoeschen_UngueltigeUebertragung(t *testing.T) {
	for name, u := range map[string]map[string]string{
		"ziel unbekannt":          {boardEigen: "niemand@example.com"},
		"ziel ist nutzer selbst":  {boardEigen: loeschA},
		"ziel leer":               {boardEigen: " "},
		"board kein einzel-owner": {boardEigen: loeschB, boardGeteil: loeschB},
	} {
		t.Run(name, func(t *testing.T) {
			db := loeschDB(t)
			befuelle(t, db)
			_, err := NewUserRepo(db).Loeschen(context.Background(), LoeschAnweisung{UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB, Uebertragen: u})
			if !errors.Is(err, ErrUebertragungUngueltig) {
				t.Fatalf("erwartet ErrUebertragungUngueltig, bekam %v", err)
			}
			if zaehle(t, db, `SELECT COUNT(*) FROM users WHERE id = ?`, loeschAID) != 1 {
				t.Fatalf("Nutzer trotz Fehler gelöscht")
			}
		})
	}
}

func TestNutzerLoeschen_Schutzregeln(t *testing.T) {
	db := loeschDB(t)
	befuelle(t, db)
	r := NewUserRepo(db)
	ctx := context.Background()

	if _, err := r.Loeschen(ctx, LoeschAnweisung{UserID: loeschBID, AdminID: loeschBID, AdminEmail: loeschB}); !errors.Is(err, ErrSelbstloeschung) {
		t.Errorf("Selbstlöschung: bekam %v", err)
	}
	// B ist der einzige Admin. Ob der Ausführende Admin ist, prüft die API
	// (requireAdmin); hier zählt nur, dass B nicht verschwinden darf.
	if _, err := r.Loeschen(ctx, LoeschAnweisung{UserID: loeschBID, AdminID: loeschCID, AdminEmail: loeschC}); !errors.Is(err, ErrLastAdminLockout) {
		t.Errorf("letzter Admin: bekam %v", err)
	}
	if _, err := r.Loeschen(ctx, LoeschAnweisung{UserID: "gibt-es-nicht", AdminID: loeschBID}); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("unbekannt: bekam %v", err)
	}
	if _, err := r.Loeschen(ctx, LoeschAnweisung{UserID: "", AdminID: loeschBID}); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("leere ID: bekam %v", err)
	}
}

// Nach der Löschung meldet sich A erneut an (Forward-Auth kennt A weiter):
// ADR-0066 legt ein NEUES leeres Board an — das alte, übertragene bleibt bei B.
func TestNutzerLoeschen_WiederanmeldungBekommtAltesBoardNicht(t *testing.T) {
	db := loeschDB(t)
	befuelle(t, db)
	ctx := context.Background()
	if _, err := NewUserRepo(db).Loeschen(ctx, LoeschAnweisung{UserID: loeschAID, AdminID: loeschBID, AdminEmail: loeschB,
		Uebertragen: map[string]string{boardEigen: loeschB}}); err != nil {
		t.Fatalf("Loeschen: %v", err)
	}
	created, err := NewBootstrapRepo(db).BootstrapNewUser(ctx, "user-anna-neu", "board-anna-neu", loeschA, "Anna")
	if err != nil || !created {
		t.Fatalf("Bootstrap: created=%v err=%v", created, err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = ?`, loeschA); n != 1 {
		t.Fatalf("A hat nach Wiederanmeldung %d ACL-Einträge, erwartet 1 (nur das neue Board)", n)
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = ? AND board_id = 'board-anna-neu'`, loeschA) != 1 {
		t.Fatalf("neues Board fehlt")
	}
}
