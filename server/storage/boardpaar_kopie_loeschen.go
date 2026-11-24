package storage

// boardpaar_kopie_loeschen.go — Notweg „Kopie außen löschen“ (ADR-0141
// Nachtrag #831, F-2232).
//
// Ein Server-Admin löscht außen eine externe Kopie, ohne Board-Owner zu sein.
// Getroffen werden NUR Kopien: ein Board mit kopie = 1 in boardpaar_aussen
// oder eine außen entkoppelte Kopie (Eintrag in boardpaar_kopie_herkunft,
// keine Kopplungszeile — dasselbe Merkmal wie die Wiederaufnahme in
// KopieAnlegen). Ein allgemeines Admin-Löschen beliebiger Boards gibt es nicht.
//
// Nicht an den Spiegel-Marken im Board-Zustand (Nacharbeit #831, K-1): Die
// setzt jeder Owner per Ganz-PUT oder Op an sein Board, und eine Sicherung
// einer früheren Kopie bringt sie per Restore mit — ein Admin-Notweg, der
// daran entschiede, löschte gewöhnliche Boards. Die Herkunft schreibt nur
// KopieAnlegen (Migration 0042). Die Marken sind auch keine zusätzliche
// Bedingung: Eine frische Kopie, die nie einen Hinweg bekam, ist kanonisch
// leer und trägt keine — sie muss nach dem Entkoppeln trotzdem löschbar sein.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrBoardpaarKeineKopie: Das Board ist keine externe Kopie (Stufe-1-Board,
// Board ohne Herkunft als Kopie, Service-Board) — der Notweg löscht es nicht.
var ErrBoardpaarKeineKopie = errors.New("storage: das board ist keine externe kopie; der notweg löscht nur kopien")

// KopieGeloescht beschreibt ein gelöschtes Board für Log und Antwort.
type KopieGeloescht struct {
	BoardID string `json:"board_id"`
	Titel   string `json:"titel"`
	// Entkoppelt: Die Kopie war schon außen entkoppelt (keine Kopplungszeile).
	Entkoppelt bool `json:"entkoppelt"`
	// OffeneAenderungen (F-2240): Was mit dem Board verloren ging und innen
	// noch nicht angekommen war — unbestätigte Journal-Einträge
	// (offeneAenderungenSQL), in DERSELBEN Transaktion vor dem Löschen gezählt.
	OffeneAenderungen int `json:"offene_aenderungen"`
}

// BoardpaarKopieFrei ist eine außen entkoppelte Kopie (Admin-Liste).
type BoardpaarKopieFrei struct {
	BoardID string `json:"board_id"`
	Titel   string `json:"titel"`
	// OffeneAenderungen (F-2240): unbestätigtes Journal (offeneAenderungenSQL) —
	// die Zahl, die der Dialog VOR dem Löschen nennt.
	OffeneAenderungen int `json:"offene_aenderungen"`
}

// offeneAenderungenSQL zählt, was ein Löschen der Kopie unwiederbringlich
// verwirft: Journal-Einträge ohne Bestätigung des Skripts (bestaetigt_at IS
// NULL). Wartende Spiegel (boardpaar_wartend) zählen nicht eigens: Solange
// ihr angelegt-Eintrag unbestätigt ist, steht er schon hier (sonst zählte
// eine Karte doppelt); ist er bestätigt, hat das Skript die Karte nach innen
// gebracht, und mit der Kopie geht nichts verloren. Eine Stelle für Liste und
// Löschen, damit die Zahl vor und nach dem Löschen dasselbe misst. %s steht
// für die Board-ID (Spalte oder Platzhalter).
const offeneAenderungenSQL = `(SELECT count(*) FROM boardpaar_journal j WHERE j.board_id = %s AND j.bestaetigt_at IS NULL)`

// KopieLoeschen löscht außen eine externe Kopie samt allem, was am Board
// hängt (Kopplungszeile, Schatten, Journal, wartende Zeilen per Kaskade;
// Schleuse-Aufträge wie BoardRepo.Delete). Prüfung und Löschen laufen in
// EINER Transaktion (BEGIN IMMEDIATE): Zwischen „ist eine Kopie“ und dem
// Löschen kann kein Koppeln oder Anlegen dazwischenkommen.
//
// Fehler: leere ID → ErrBoardpaarBoardFehlt; kein Board → ErrBoardpaarBoardUnbekannt;
// keine Kopie → ErrBoardpaarKeineKopie. Anhänge, Backup-Grabstein und das
// Lösch-Ereignis sind Sache des Aufrufers (wie beim REST-Löschen).
func (r *BoardpaarRepo) KopieLoeschen(ctx context.Context, boardID string) (*KopieGeloescht, error) {
	if boardID == "" {
		return nil, ErrBoardpaarBoardFehlt
	}
	if boardID == ServiceBoardID {
		return nil, ErrBoardpaarKeineKopie
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var titel string
	var kopie sql.NullInt64
	var herkunft int
	err = conn.QueryRowContext(ctx,
		`SELECT b.title, a.kopie, EXISTS (SELECT 1 FROM boardpaar_kopie_herkunft h WHERE h.board_id = b.id)
		 FROM boards b LEFT JOIN boardpaar_aussen a ON a.aussen_board_id = b.id WHERE b.id = ?`, boardID).
		Scan(&titel, &kopie, &herkunft)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarBoardUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: %w", err)
	}
	erg := &KopieGeloescht{BoardID: boardID, Titel: titel}
	switch {
	case kopie.Valid && kopie.Int64 == 1:
		// gekoppelte Kopie
	case kopie.Valid:
		return nil, ErrBoardpaarKeineKopie // Stufe-1-Board (kopie = 0)
	case herkunft == 1:
		erg.Entkoppelt = true // außen entkoppelte Kopie
	default:
		return nil, ErrBoardpaarKeineKopie
	}
	// F-2240: zählen, bevor die Kaskade das Journal mitnimmt — auf derselben
	// Verbindung unter BEGIN IMMEDIATE, also genau der Bestand, der gleich fällt.
	if err := conn.QueryRowContext(ctx, "SELECT "+fmt.Sprintf(offeneAenderungenSQL, "?"), boardID).
		Scan(&erg.OffeneAenderungen); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: offene aenderungen: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM boards WHERE id = ?`, boardID); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: board: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM schleuse_auftraege WHERE board_id = ?`, boardID); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: schleuse_auftraege: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-loeschen: commit: %w", err)
	}
	committed = true
	return erg, nil
}

// EntkoppelteKopien liefert die außen entkoppelten Kopien (Eintrag in
// boardpaar_kopie_herkunft, keine Zeile in boardpaar_aussen), nach ID
// sortiert — für den Admin-Reiter, damit „Kopie außen löschen“ auch nach einem
// Entkoppeln erreichbar bleibt. Dasselbe Merkmal wie in KopieLoeschen.
func (r *BoardpaarRepo) EntkoppelteKopien(ctx context.Context) ([]BoardpaarKopieFrei, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT b.id, b.title, `+fmt.Sprintf(offeneAenderungenSQL, "b.id")+` FROM boardpaar_kopie_herkunft h
		 JOIN boards b ON b.id = h.board_id
		 LEFT JOIN boardpaar_aussen a ON a.aussen_board_id = b.id
		 WHERE a.aussen_board_id IS NULL AND b.id <> ?
		 ORDER BY b.id`, ServiceBoardID)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.entkoppelte-kopien: %w", err)
	}
	defer rows.Close()
	out := []BoardpaarKopieFrei{}
	for rows.Next() {
		var k BoardpaarKopieFrei
		if err := rows.Scan(&k.BoardID, &k.Titel, &k.OffeneAenderungen); err != nil {
			return nil, fmt.Errorf("boardpaar.entkoppelte-kopien scan: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
