package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// BootstrapOwnerEnv ist der Name der Umgebungsvariable für den ersten Owner
// und — seit F-1855 — den Bootstrap-Admin.
const BootstrapOwnerEnv = "ABOARD_BOOTSTRAP_OWNER"

// BootstrapOwnerAusEnv liest ABOARD_BOOTSTRAP_OWNER und normalisiert sie wie
// ACL und Forward-Auth (F-260). gesetzt = die Variable ist nicht leer;
// ok = der Wert ist eine Personenadresse (Lokalteil, '@', Domain).
//
// F-1862: Default-Board und Service-Board prüfen mit DERSELBEN Funktion. Bis
// dahin lehnte nur das Default-Board einen Wert ohne '@' ab, das
// Service-Board schrieb jeden Tippfehler als Owner ins ACL. Ein Domain-
// Schlüssel ("@firma.de", ADR-0132 §2) ist keine Person und gilt ebenfalls
// nicht.
func BootstrapOwnerAusEnv() (owner string, gesetzt, ok bool) {
	roh := os.Getenv(BootstrapOwnerEnv)
	if roh == "" {
		return "", false, false
	}
	owner = NormalizeEmail(roh)
	return owner, true, IstPersonenadresse(owner)
}

// IstPersonenadresse: normalisierte Adresse mit Lokalteil, '@' und Domain,
// ohne Leerraum. Ein Domain-Schlüssel ("@firma.de") ist keine Person.
func IstPersonenadresse(e string) bool {
	at := strings.Index(e, "@")
	return at > 0 && at < len(e)-1 && strings.Count(e, "@") == 1 && !strings.ContainsAny(e, " \t\r\n")
}

// BootstrapAdminNachholen macht beim Serverstart die Person aus
// ABOARD_BOOTSTRAP_OWNER zum Admin, wenn es noch KEINEN Admin gibt und die
// Person schon ein Konto hat (F-1855). Das deckt den Fall, dass die Variable
// erst nach dem Erstkontakt gesetzt oder berichtigt wurde — etwa weil sie
// zuerst auf ein Funktionspostfach zeigte, das sich nie anmeldet.
// Rückgabe: true, wenn jemand Admin wurde.
func BootstrapAdminNachholen(ctx context.Context, db *sql.DB, owner string) (bool, error) {
	if owner == "" {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("bootstrap-admin: %w", err)
	}
	defer tx.Rollback()
	var admins int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&admins); err != nil {
		return false, fmt.Errorf("bootstrap-admin: admin-count: %w", err)
	}
	if admins > 0 {
		return false, nil
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("bootstrap-admin: user lesen: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET is_admin = 1 WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("bootstrap-admin: setzen: %w", err)
	}
	return true, tx.Commit()
}

// BoardOhneOwner ist ein Board, an dem kein ACL-Eintrag mit Rolle owner hängt.
type BoardOhneOwner struct {
	ID    string
	Title string
}

// BoardsOhneOwner listet Boards ohne owner-Eintrag (F-1854). Im
// Mehrnutzer-Betrieb sieht solche Boards niemand außer über den Admin-Weg —
// typisch nach dem Umstieg vom Einzelbetrieb, weil seedDefaultBoard einen
// bestehenden Bestand bewusst nicht anfasst. Zugeteilt wird NICHT automatisch
// (Widerlegungsbedingung des Findings: verborgene Boards können gewollt
// sein), sondern ausdrücklich mit `aboard-server acl adopt`.
func BoardsOhneOwner(ctx context.Context, db *sql.DB) ([]BoardOhneOwner, error) {
	// Das Service-Board (Mail-Eingang) zählt nicht: Es hat ohne
	// ABOARD_BOOTSTRAP_OWNER bewusst keinen Owner, der Hinweis würde sonst bei
	// jedem Start rauschen, und `adopt` machte jemanden still zum Owner des
	// Mail-Eingangs (Kritiker S-10). Gezielt geht es mit `acl set`.
	rows, err := db.QueryContext(ctx, `SELECT id, title FROM boards
		WHERE id <> ? AND id NOT IN (SELECT board_id FROM acl WHERE role = 'owner') ORDER BY id`, ServiceBoardID)
	if err != nil {
		return nil, fmt.Errorf("boards ohne owner: %w", err)
	}
	defer rows.Close()
	var out []BoardOhneOwner
	for rows.Next() {
		var b BoardOhneOwner
		if err := rows.Scan(&b.ID, &b.Title); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
