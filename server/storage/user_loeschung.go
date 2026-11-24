package storage

// user_loeschung.go — Löschpfad für Nutzer (ADR-0129 §5 A-8, Karte #571).
//
// Ein Admin entfernt einen Nutzer vollständig. Entscheid 2026-09-29:
//
//  1. Boards, deren EINZIGER Owner der Nutzer ist, verschwinden nie still.
//     Ohne ausdrückliche Übertragung für jedes dieser Boards scheitert die
//     Löschung mit *EinzigerOwnerFehler (API: 409 + Liste). Löschen will der
//     Admin ein solches Board, überträgt er es an sich selbst und löscht es
//     danach auf dem normalen Weg (DELETE /boards/{id}).
//  2. Protokolle werden pseudonymisiert, nicht gelöscht: activities,
//     server_config_audit, server_config.updated_by, agent_tokens.created_by,
//     kalender_ausgang_tokens.created_by, boardpaar_tokens.created_by,
//     boardpaar_innen.von, boardpaar_aussen.von, mail_spam_senders.added_by. Das Protokoll bleibt lückenlos (wer hat wann
//     was getan), nur ohne Personenbezug.
//  3. Namen/E-Mails INNERHALB der Board-Inhalte (boards.state_json:
//     Karten-Owner, Assignees, Kommentar-Autoren) bleiben unangetastet —
//     bekannte Grenze, dokumentiert in docs/server-betrieb.md §Datenschutz.
//
// Gelöscht werden: users-Zeile, alle ACL-Einträge, Sitzungen, Konto,
// Einladungen, Wiederherstellungscodes und Anbieter-Bindungen, LLM-Schlüssel,
// LLM-Verläufe (Turns per FK), Benachrichtigungs-Abos, -Konfiguration und
// -Log, iCal-Abos, Schlüssel der Ausgangsschleuse (Karte #684) und ihre
// Aufträge samt Zuordnungen (Karte #685).
//
// Das Pseudonym ist je Löschung zufällig ("geloescht:" + 12 Hex-Zeichen),
// nicht aus E-Mail oder ID abgeleitet: Ein Hash der E-Mail ließe sich mit
// einer Liste vermuteter Adressen zurückrechnen. Innerhalb einer Löschung ist
// es überall dasselbe, damit die Einträge eines Nutzers zusammengehörig
// lesbar bleiben.
//
// Alles läuft in EINER BEGIN-IMMEDIATE-Transaktion (F-191-Muster): Prüfung,
// Übertragung, Löschung, Pseudonymisierung und Audit-Eintrag gelingen
// gemeinsam oder gar nicht.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// AuditActionUserDelete ist die Audit-Action der Nutzer-Löschung.
const AuditActionUserDelete = "user-delete"

// PseudonymPraefix leitet jedes Pseudonym eines gelöschten Nutzers ein.
const PseudonymPraefix = "geloescht:"

var (
	// ErrSelbstloeschung: Ein Admin kann sich über diesen Weg nicht selbst
	// entfernen — er verlöre mitten in der Aktion seinen Zugang.
	ErrSelbstloeschung = errors.New("storage: admin kann sich nicht selbst löschen")
	// ErrUebertragungUngueltig: Die Übertragungsliste nennt ein Board, das
	// kein Einzel-Owner-Board des Nutzers ist, oder ein Ziel, das es nicht
	// gibt bzw. der Nutzer selbst ist.
	ErrUebertragungUngueltig = errors.New("storage: ungültige übertragung")
)

// EinzigerOwnerBoard nennt ein Board, das ohne Übertragung besitzerlos würde.
type EinzigerOwnerBoard struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// EinzigerOwnerFehler: Für diese Boards fehlt eine Übertragung.
type EinzigerOwnerFehler struct {
	Boards []EinzigerOwnerBoard
}

func (e *EinzigerOwnerFehler) Error() string {
	ids := make([]string, len(e.Boards))
	for i, b := range e.Boards {
		ids[i] = b.ID
	}
	return "storage: nutzer ist einziger owner von " + strings.Join(ids, ", ")
}

// NutzerLoeschErgebnis fasst zusammen, was die Löschung bewirkt hat.
// Gezaehlt nennt je Tabelle bzw. Spalte die betroffenen Zeilen.
type NutzerLoeschErgebnis struct {
	Pseudonym   string            `json:"pseudonym"`
	Uebertragen map[string]string `json:"uebertragen"`
	Gezaehlt    map[string]int64  `json:"gezaehlt"`
	// AgentTokens: IDs der dabei widerrufenen Agenten-Tokens — der Aufrufer
	// schliesst damit deren offene Verbindungen (Karte #608).
	AgentTokens []string `json:"-"`
}

// LoeschAnweisung ist, was der Admin angibt.
type LoeschAnweisung struct {
	UserID     string
	AdminID    string
	AdminEmail string
	// Uebertragen: Board-ID → E-Mail des künftigen Owners. Nur für Boards,
	// deren einziger Owner der Nutzer ist.
	Uebertragen map[string]string
}

// neuesPseudonym liefert "geloescht:" + 12 zufällige Hex-Zeichen.
var neuesPseudonym = func() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return PseudonymPraefix + hex.EncodeToString(b), nil
}

// Loeschen entfernt einen Nutzer nach den Regeln oben.
func (r *UserRepo) Loeschen(ctx context.Context, a LoeschAnweisung) (*NutzerLoeschErgebnis, error) {
	if strings.TrimSpace(a.UserID) == "" {
		return nil, fmt.Errorf("users.Loeschen: %w", ErrUserNotFound)
	}
	if a.UserID == a.AdminID {
		return nil, ErrSelbstloeschung
	}
	pseudonym, err := neuesPseudonym()
	if err != nil {
		return nil, fmt.Errorf("users.Loeschen: pseudonym: %w", err)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("users.Loeschen: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("users.Loeschen: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var email string
	var isAdmin int
	err = conn.QueryRowContext(ctx, `SELECT email, is_admin FROM users WHERE id = ?`, a.UserID).Scan(&email, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("users.Loeschen: %w", ErrUserNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("users.Loeschen: lookup: %w", err)
	}
	email = NormalizeEmail(email)
	// Ohne E-Mail träfen die WHERE-Klauseln auf user_email/actor = '' alle
	// Legacy-Bypass-Einträge (ADR-0059 §2) — nie mit leerem Schlüssel löschen.
	if strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("users.Loeschen: users-zeile %s ohne email", a.UserID)
	}

	if isAdmin == 1 {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n); err != nil {
			return nil, fmt.Errorf("users.Loeschen: admin count: %w", err)
		}
		if n <= 1 {
			return nil, ErrLastAdminLockout
		}
	}

	einzel, err := einzigerOwnerBoards(ctx, conn, email)
	if err != nil {
		return nil, err
	}
	uebertragen, err := pruefeUebertragung(ctx, conn, email, einzel, a.Uebertragen)
	if err != nil {
		return nil, err
	}

	g := map[string]int64{}
	exec := func(name, q string, args ...any) error {
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("users.Loeschen: %s: %w", name, err)
		}
		n, _ := res.RowsAffected()
		g[name] += n
		return nil
	}
	ts := nowISO()

	// 1. Übertragungen — vor dem Entfernen der ACL-Zeilen des Nutzers.
	for _, boardID := range sortierteSchluessel(uebertragen) {
		ziel := uebertragen[boardID]
		if err := exec("acl_uebertragen",
			`INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, 'owner', ?)
			 ON CONFLICT(board_id, user_email) DO UPDATE SET role = 'owner'`,
			boardID, ziel, ts); err != nil {
			return nil, err
		}
		// Board-Historie: der Owner-Wechsel steht im Protokoll des Boards,
		// Akteur ist der Admin (nicht der gelöschte Nutzer).
		if err := exec("activities_uebertragung",
			`INSERT INTO activities (id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind)
			 VALUES (?, ?, ?, 'acl-set', 'role', '', 'owner', ?, ?, '', '', 'mensch')`,
			uuid.New().String(), boardID, a.AdminEmail, ziel, activityTS()); err != nil { // feste Breite, F-1157
			return nil, err
		}
	}

	// 2. Löschen.
	if err := exec("acl", `DELETE FROM acl WHERE lower(user_email) = ?`, email); err != nil {
		return nil, err
	}
	// Eingebaute Anmeldung (ADR-0148 §2, Karte #1051): Eine gelöschte Person
	// bleibt nicht im offenen Browser angemeldet. Auch ihre (iss, sub)-
	// Bindungen und ihr Konto samt Einladungen und Wiederherstellungscodes
	// gehen (ADR-0149 §7, Karte #1069), denn die Löschung lässt keine Adresse
	// zurück (sucheReste). Wird die Adresse später wieder zugelassen oder
	// eingeladen, beginnt alles neu. Ausdrücklich gelöscht, nicht über
	// ON DELETE CASCADE, damit die Zahlen im Ergebnis stehen.
	if err := exec("sitzungen", `DELETE FROM sitzungen WHERE lower(email) = ?`, email); err != nil {
		return nil, err
	}
	const kontenDerPerson = `SELECT id FROM konten WHERE lower(email) = ?`
	if err := exec("anbieter_bindungen", `DELETE FROM anbieter_bindungen WHERE user_id = ? OR konto_id IN (`+kontenDerPerson+`)`, a.UserID, email); err != nil {
		return nil, err
	}
	if err := exec("einladungen", `DELETE FROM einladungen WHERE konto_id IN (`+kontenDerPerson+`)`, email); err != nil {
		return nil, err
	}
	if err := exec("konto_codes", `DELETE FROM konto_codes WHERE konto_id IN (`+kontenDerPerson+`)`, email); err != nil {
		return nil, err
	}
	if err := exec("konten", `DELETE FROM konten WHERE lower(email) = ?`, email); err != nil {
		return nil, err
	}
	if err := exec("llm_api_keys", `DELETE FROM llm_api_keys WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("llm_turns", `DELETE FROM llm_turns WHERE conversation_id IN (SELECT id FROM llm_conversations WHERE lower(created_by) = ?)`, email); err != nil {
		return nil, err
	}
	if err := exec("llm_conversations", `DELETE FROM llm_conversations WHERE lower(created_by) = ?`, email); err != nil {
		return nil, err
	}
	if err := exec("notification_subscriptions", `DELETE FROM notification_subscriptions WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("notification_config", `DELETE FROM notification_config WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("notification_log", `DELETE FROM notification_log WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("ical_subscriptions", `DELETE FROM ical_subscriptions WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	// Ausgangsschleuse (ADR-0134 §4, Karte #684): die Schlüssel der Person.
	if err := exec("schleuse_schluessel", `DELETE FROM schleuse_schluessel WHERE user_id = ?`, a.UserID); err != nil {
		return nil, err
	}
	// Karte #685/#686 (ADR-0134 §4): „Schlüssel, offene Aufträge und
	// Zuordnungen dieser Person sind weg.“ Offen heisst: noch nichts ist
	// hinausgegangen (entwurf, freigegeben) — diese Zeilen gehen ganz.
	// Gesendete Aufträge sind Protokoll (wer, wann, welcher Anbieter, Hash,
	// Kosten): Sie bleiben stehen, pseudonymisiert und ohne Zuordnung — ohne
	// sie ist der Auftrag nicht mehr re-identifizierbar. Die E-Mail-Spalte
	// zählt mit, falls eine Zeile je mit anderer ID geschrieben wurde.
	if err := exec("schleuse_auftraege", `DELETE FROM schleuse_auftraege WHERE (user_id = ? OR lower(user_email) = ?) AND zustand IN ('entwurf', 'freigegeben')`, a.UserID, email); err != nil {
		return nil, err
	}
	if err := exec("schleuse_auftraege.protokoll", `UPDATE schleuse_auftraege SET user_id = ?, user_email = ?, zuordnung_verschluesselt = NULL WHERE user_id = ? OR lower(user_email) = ?`, pseudonym, pseudonym, a.UserID, email); err != nil {
		return nil, err
	}
	// Karte #686: Spuren als freigebende Person in fremden Aufträgen.
	for _, spalte := range []string{"freigegeben_von", "zweit_freigegeben_von"} {
		if err := exec("schleuse_auftraege."+spalte, `UPDATE schleuse_auftraege SET `+spalte+` = ? WHERE lower(`+spalte+`) = ?`, pseudonym, email); err != nil {
			return nil, err
		}
	}
	// mail_autoreply_log führt die Adresse als Absender ans Service-Board
	// (Session-Kritiker Sprint 72, K-A2). Folge des Löschens: Schreibt die
	// Adresse erneut, bekommt sie wieder eine Auto-Reply — hinnehmbar.
	// mail_spam_senders bleibt dagegen bewusst stehen: Die Sperrliste ist
	// eine Schutzfunktion, kein Nutzerprofil (docs/server-betrieb.md).
	if err := exec("mail_autoreply_log", `DELETE FROM mail_autoreply_log WHERE lower(sender_email) = ?`, email); err != nil {
		return nil, err
	}
	// Agenten-Tokens der Person (ADR-0130 §6, Karte #608): widerrufen, nicht
	// löschen — der Audit-Eintrag verweist auf den Namen. Ohne users-Zeile
	// gälten sie ohnehin nicht mehr (Lookup verlangt die Person); der
	// Widerruf macht das ausdrücklich und protokolliert es.
	widerrufen, err := widerrufeFuerPersonIn(ctx, conn, a.UserID, a.AdminID, ts, "user="+pseudonym)
	if err != nil {
		return nil, err
	}
	g["agent_tokens_widerrufen"] = int64(len(widerrufen))
	if err := exec("users", `DELETE FROM users WHERE id = ?`, a.UserID); err != nil {
		return nil, err
	}

	// 3. Pseudonymisieren. activities kennen den Nutzer per E-Mail — als
	// Akteur und als Gegenstand von ACL-Einträgen (acl-set: note = E-Mail).
	// Verglichen wird der ganze Wert, nicht per Teilstring (Freitext wird
	// nicht umgeschrieben), aber ohne Rücksicht auf Groß-/Kleinschreibung:
	// Alle Schreibstellen normalisieren heute, die Löschung soll davon nicht
	// abhängen (K-A3). users.email ist normalisiert, also genügt lower(spalte).
	for _, spalte := range []string{"actor", "note", "from_val", "to_val", "on_behalf_of"} {
		if err := exec("activities."+spalte, `UPDATE activities SET `+spalte+` = ? WHERE lower(`+spalte+`) = ?`, pseudonym, email); err != nil {
			return nil, err
		}
	}
	if err := exec("server_config_audit.user_id", `UPDATE server_config_audit SET user_id = ? WHERE user_id = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	// Die Konto-Einstellungen und die CLI `nutzer bindung-loesen` schreiben ihre
	// Audit-Zeilen unter dem Schlüssel „personen/<users.id>“ (Karte #1083): Auch
	// der Schlüssel trägt die alte ID und wird mit pseudonymisiert.
	if err := exec("server_config_audit.key", `UPDATE server_config_audit SET key = 'personen/' || ? WHERE key = 'personen/' || ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("server_config.updated_by", `UPDATE server_config SET updated_by = ? WHERE updated_by = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("agent_tokens.created_by", `UPDATE agent_tokens SET created_by = ? WHERE created_by = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	// Karte #748 (Kritiker K-1): Zugänge des Push-Skripts tragen die ID des
	// Admins, der sie angelegt hat.
	if err := exec("kalender_ausgang_tokens.created_by", `UPDATE kalender_ausgang_tokens SET created_by = ? WHERE created_by = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	// Karte #788 (Vorprüfung, Lücke 3): Kopplungen und Zugänge des
	// Board-Paars tragen die ID des Admins, der sie im Dialog angelegt hat.
	if err := exec("boardpaar_tokens.created_by", `UPDATE boardpaar_tokens SET created_by = ? WHERE created_by = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("boardpaar_innen.von", `UPDATE boardpaar_innen SET von = ? WHERE von = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("boardpaar_aussen.von", `UPDATE boardpaar_aussen SET von = ? WHERE von = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	// Karte #608: die (widerrufenen) Tokens der Person tragen ihre ID.
	if err := exec("agent_tokens.user_id", `UPDATE agent_tokens SET user_id = ? WHERE user_id = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	if err := exec("mail_spam_senders.added_by", `UPDATE mail_spam_senders SET added_by = ? WHERE added_by = ?`, pseudonym, a.UserID); err != nil {
		return nil, err
	}
	// Karte #684: Admin-Spuren in der Freigabeliste der Ausgangsschleuse —
	// wer angelegt, geändert oder die Vertragsform bestätigt hat.
	for _, spalte := range []string{"erstellt_von", "geaendert_von", "vertragsform_bestaetigt_von"} {
		if err := exec("schleuse_freigaben."+spalte, `UPDATE schleuse_freigaben SET `+spalte+` = ? WHERE `+spalte+` = ?`, pseudonym, a.UserID); err != nil {
			return nil, err
		}
	}

	// 4. Audit — im selben Commit. Der Schlüssel nennt die alte ID (ohne die
	// users-Zeile kein Personenbezug mehr), der Wert das Pseudonym, damit
	// sich die pseudonymisierten Einträge der Löschung zuordnen lassen.
	neu := pseudonym
	if len(uebertragen) > 0 {
		neu += fmt.Sprintf(" uebertragen=%d", len(uebertragen))
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, ?, ?, NULL, ?, ?)`,
		uuid.New().String(), "users/"+a.UserID, a.AdminID, AuditActionUserDelete, neu, ts); err != nil {
		return nil, fmt.Errorf("users.Loeschen: audit: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("users.Loeschen: commit: %w", err)
	}
	committed = true
	return &NutzerLoeschErgebnis{Pseudonym: pseudonym, Uebertragen: uebertragen, Gezaehlt: g, AgentTokens: widerrufen}, nil
}

// einzigerOwnerBoards liefert die Boards, auf denen email Owner ist und kein
// anderer Owner existiert.
func einzigerOwnerBoards(ctx context.Context, conn *sql.Conn, email string) ([]EinzigerOwnerBoard, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT a.board_id, COALESCE(b.title, '') FROM acl a
		   LEFT JOIN boards b ON b.id = a.board_id
		  WHERE lower(a.user_email) = ? AND a.role = 'owner'
		    AND NOT EXISTS (SELECT 1 FROM acl o WHERE o.board_id = a.board_id AND o.role = 'owner' AND lower(o.user_email) <> lower(a.user_email))
		  ORDER BY a.board_id`, email)
	if err != nil {
		return nil, fmt.Errorf("users.Loeschen: einzel-owner: %w", err)
	}
	defer rows.Close()
	var out []EinzigerOwnerBoard
	for rows.Next() {
		var b EinzigerOwnerBoard
		if err := rows.Scan(&b.ID, &b.Title); err != nil {
			return nil, fmt.Errorf("users.Loeschen: einzel-owner scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// pruefeUebertragung gleicht die Übertragungsliste gegen die
// Einzel-Owner-Boards ab. Jedes Einzel-Owner-Board braucht ein Ziel; jedes
// genannte Board muss ein Einzel-Owner-Board sein; jedes Ziel ein bestehender
// Nutzer außer dem zu löschenden. Liefert die normalisierte Liste.
func pruefeUebertragung(ctx context.Context, conn *sql.Conn, email string, einzel []EinzigerOwnerBoard, wunsch map[string]string) (map[string]string, error) {
	istEinzel := map[string]bool{}
	for _, b := range einzel {
		istEinzel[b.ID] = true
	}
	out := map[string]string{}
	for _, boardID := range sortierteSchluessel(wunsch) {
		if !istEinzel[boardID] {
			return nil, fmt.Errorf("%w: board %s ist kein board, dessen einziger owner der nutzer ist", ErrUebertragungUngueltig, boardID)
		}
		ziel := NormalizeEmail(wunsch[boardID])
		if ziel == "" || ziel == email {
			return nil, fmt.Errorf("%w: ziel für board %s fehlt oder ist der nutzer selbst", ErrUebertragungUngueltig, boardID)
		}
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, ziel).Scan(&n); err != nil {
			return nil, fmt.Errorf("users.Loeschen: ziel lookup: %w", err)
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: ziel %s für board %s ist kein bekannter nutzer", ErrUebertragungUngueltig, ziel, boardID)
		}
		out[boardID] = ziel
	}
	var fehlend []EinzigerOwnerBoard
	for _, b := range einzel {
		if _, ok := out[b.ID]; !ok {
			fehlend = append(fehlend, b)
		}
	}
	if len(fehlend) > 0 {
		return nil, &EinzigerOwnerFehler{Boards: fehlend}
	}
	return out, nil
}

func sortierteSchluessel(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}
