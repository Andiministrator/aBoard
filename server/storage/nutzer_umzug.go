package storage

// nutzer_umzug.go — Konto auf eine andere Adresse umziehen (Karte #1052,
// ADR-0148 §7 Schritt 3, Teil von #816).
//
// Anlass: Google liefert nur die Hauptadresse des Kontos, keinen Alias. Wer
// bisher unter einer anderen Adresse (Forward-Auth, Authelia) geführt wird,
// behält mit dem Umzug seine Boards, Rechte und Schlüssel.
//
// Die Adresse ist in dieser Datenbank an mehreren Stellen der Schlüssel einer
// Person (users.id bleibt, Agenten-Tokens, Benachrichtigungen, iCal und
// LLM-Schlüssel hängen an user_id und folgen von selbst):
//
//	umgeschrieben   users.email (samt is_admin, dieselbe Zeile)
//	                acl.user_email
//	                schleuse_auftraege.user_email, .freigegeben_von,
//	                  .zweit_freigegeben_von (die Vier-Augen-Prüfung
//	                  vergleicht sie mit der Adresse der angemeldeten Person)
//	                llm_conversations.created_by
//	                konten.email (Konto im Werden oder mit eigenem Passwort,
//	                  ADR-0149; Einladungen und Codes hängen an konten.id)
//	folgen users.id anbieter_bindungen (ADR-0149 §4, seit Migration 0046
//	                ohne Adresse)
//	gelöscht        sitzungen der alten Adresse (die Person meldet sich neu an)
//
// Bleibt unverändert, bekannte Grenze (wie bei der Nutzerlöschung): activities
// (Protokoll), Kommentar-Autoren und Adressen in boards.state_json,
// mail_autoreply_log.sender_email (fremder Absender am Service-Board).
//
// Alles läuft in EINER BEGIN-IMMEDIATE-Transaktion (F-191-Muster). Der
// Trockenlauf führt dieselben Anweisungen aus und rollt zurück — die Zahlen
// sind deshalb dieselben wie im echten Lauf.
//
// Ein Test (nutzer_umzug_1052_test.go) durchsucht per sqlite_master jede
// Spalte der Datenbank: Eine künftige Migration mit einer neuen Spalte, die
// die Adresse trägt, macht ihn rot, bis sie hier eingeordnet ist.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// AuditActionUserUmzug: Audit-Aktion eines Umzugs (F-2829). Der Eintrag trägt
// Schlüssel "users/<users.id>", user_id leer (die CLI kennt keine Akteurs-ID;
// ” heißt laut Schema „System/CLI“, wie bei `nutzer bindung-loesen`) und
// keinen Wert — weder alte noch neue Adresse, damit die Nutzerlöschung
// (sucheReste) nichts Adressartiges im Protokoll findet. users.id bleibt beim
// Umzug gleich; wer wann umgezogen ist, ergibt sich aus Schlüssel und ts.
const AuditActionUserUmzug = "user-umzug"

var (
	// ErrUmzugAdresse: --von/--nach ist keine Personenadresse (Domain-Subjekt
	// "@firma.de", kein '@', Leerraum) oder beide sind gleich.
	ErrUmzugAdresse = errors.New("storage: ungültige adresse für den umzug")
	// ErrUmzugQuelleFehlt: Die alte Adresse hat kein Konto.
	ErrUmzugQuelleFehlt = errors.New("storage: die alte adresse hat kein konto")
	// ErrUmzugZielBesetzt: Die neue Adresse hat schon eine users-Zeile oder
	// eine Zeile in konten (Einladung, ADR-0149).
	ErrUmzugZielBesetzt = errors.New("storage: die neue adresse ist schon vergeben")
)

// UmzugAclKonflikt: Auf diesen Boards tragen alte und neue Adresse je einen
// ACL-Eintrag; der Primärschlüssel (board_id, user_email) verbietet das
// stille Zusammenführen.
type UmzugAclKonflikt struct{ Boards []string }

func (e *UmzugAclKonflikt) Error() string {
	return "storage: die neue adresse hat schon acl-einträge auf board(s) " + strings.Join(e.Boards, ", ")
}

// UmzugErgebnis nennt, was der Umzug geändert hat bzw. (Trockenlauf) ändern
// würde. Gezaehlt: je "tabelle.spalte" die Zahl der umgeschriebenen Zeilen.
type UmzugErgebnis struct {
	Von                string           `json:"von"`
	Nach               string           `json:"nach"`
	Trockenlauf        bool             `json:"trockenlauf"`
	Gezaehlt           map[string]int64 `json:"gezaehlt"`
	SitzungenGeloescht int64            `json:"sitzungen_geloescht"`
}

// Schluessel liefert die Zählschlüssel sortiert (stabile Ausgabe).
func (e *UmzugErgebnis) Schluessel() []string {
	k := make([]string, 0, len(e.Gezaehlt))
	for s := range e.Gezaehlt {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

// NutzerUmziehen schreibt die Adresse von `von` auf `nach` um. Beide Adressen
// werden wie NormalizeEmail normalisiert und müssen Personenadressen sein.
// Bei trockenlauf wird nichts geschrieben.
func NutzerUmziehen(ctx context.Context, db *sql.DB, von, nach string, trockenlauf bool) (*UmzugErgebnis, error) {
	von, nach = NormalizeEmail(von), NormalizeEmail(nach)
	if !IstPersonenadresse(von) {
		return nil, fmt.Errorf("%w: --von %q ist keine personenadresse", ErrUmzugAdresse, von)
	}
	if !IstPersonenadresse(nach) {
		return nil, fmt.Errorf("%w: --nach %q ist keine personenadresse", ErrUmzugAdresse, nach)
	}
	if von == nach {
		return nil, fmt.Errorf("%w: alte und neue adresse sind gleich", ErrUmzugAdresse)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("nutzer umziehen: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("nutzer umziehen: begin: %w", err)
	}
	fertig := false
	defer func() {
		if !fertig {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	zaehle := func(q string, args ...any) (int, error) {
		var n int
		if err := conn.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return 0, fmt.Errorf("nutzer umziehen: prüfung: %w", err)
		}
		return n, nil
	}

	// Prüfungen — vor dem ersten Schreiben.
	if n, err := zaehle(`SELECT COUNT(*) FROM users WHERE lower(email) = ?`, von); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, fmt.Errorf("%w: %s", ErrUmzugQuelleFehlt, von)
	}
	if n, err := zaehle(`SELECT COUNT(*) FROM users WHERE lower(email) = ?`, nach); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, fmt.Errorf("%w: %s hat schon ein konto", ErrUmzugZielBesetzt, nach)
	}
	if n, err := zaehle(`SELECT COUNT(*) FROM konten WHERE lower(email) = ?`, nach); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, fmt.Errorf("%w: %s hat schon ein konto oder eine einladung (konten)", ErrUmzugZielBesetzt, nach)
	}
	rows, err := conn.QueryContext(ctx,
		`SELECT DISTINCT a.board_id FROM acl a
		  WHERE lower(a.user_email) = ?
		    AND EXISTS (SELECT 1 FROM acl b WHERE b.board_id = a.board_id AND lower(b.user_email) = ?)
		  ORDER BY a.board_id`, nach, von)
	if err != nil {
		return nil, fmt.Errorf("nutzer umziehen: acl-konflikte: %w", err)
	}
	var konflikt []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("nutzer umziehen: acl-konflikte scan: %w", err)
		}
		konflikt = append(konflikt, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(konflikt) > 0 {
		return nil, &UmzugAclKonflikt{Boards: konflikt}
	}

	var userID string
	if err := conn.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ?`, von).Scan(&userID); err != nil {
		return nil, fmt.Errorf("nutzer umziehen: user-id: %w", err)
	}

	erg := &UmzugErgebnis{Von: von, Nach: nach, Trockenlauf: trockenlauf, Gezaehlt: map[string]int64{}}
	umschreiben := func(name, q string) error {
		res, err := conn.ExecContext(ctx, q, nach, von)
		if err != nil {
			return fmt.Errorf("nutzer umziehen: %s: %w", name, err)
		}
		n, _ := res.RowsAffected()
		erg.Gezaehlt[name] = n
		return nil
	}
	for _, s := range []struct{ name, q string }{
		{"users.email", `UPDATE users SET email = ? WHERE lower(email) = ?`},
		{"acl.user_email", `UPDATE acl SET user_email = ? WHERE lower(user_email) = ?`},
		{"schleuse_auftraege.user_email", `UPDATE schleuse_auftraege SET user_email = ? WHERE lower(user_email) = ?`},
		{"schleuse_auftraege.freigegeben_von", `UPDATE schleuse_auftraege SET freigegeben_von = ? WHERE lower(freigegeben_von) = ?`},
		{"schleuse_auftraege.zweit_freigegeben_von", `UPDATE schleuse_auftraege SET zweit_freigegeben_von = ? WHERE lower(zweit_freigegeben_von) = ?`},
		{"llm_conversations.created_by", `UPDATE llm_conversations SET created_by = ? WHERE lower(created_by) = ?`},
		{"konten.email", `UPDATE konten SET email = ? WHERE lower(email) = ?`},
	} {
		if err := umschreiben(s.name, s.q); err != nil {
			return nil, err
		}
	}
	res, err := conn.ExecContext(ctx, `DELETE FROM sitzungen WHERE lower(email) = ?`, von)
	if err != nil {
		return nil, fmt.Errorf("nutzer umziehen: sitzungen: %w", err)
	}
	erg.SitzungenGeloescht, _ = res.RowsAffected()

	if trockenlauf {
		return erg, nil // Rollback im defer
	}
	// Audit (F-2829) in derselben Transaktion: ohne Adresse, ohne Wert.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
		 VALUES (?, ?, '', ?, NULL, NULL, ?)`,
		uuid.NewString(), "users/"+userID, AuditActionUserUmzug, nowISO()); err != nil {
		return nil, fmt.Errorf("nutzer umziehen: audit: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("nutzer umziehen: commit: %w", err)
	}
	fertig = true
	return erg, nil
}
