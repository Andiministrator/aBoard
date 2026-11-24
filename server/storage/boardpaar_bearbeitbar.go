package storage

// boardpaar_bearbeitbar.go — Schalter „bearbeitbar“ je Kopie-Board (ADR-0141
// Nachtrag #836, Migration 0044).
//
// boardpaar.rueckweg gilt für den ganzen externen Server; der Betreiber gibt den
// Rückweg aber je Board frei. Deshalb trägt jede Kopie (kopie = 1) ein eigenes
// Feld bearbeitbar. Wirksam ist der Rückweg nur bei boardpaar.rueckweg = an
// UND bearbeitbar = 1 — die Verknüpfung bildet api.kopieRueckwegAn, hier steht
// nur das Lesen und Setzen des Felds.
//
// Gesetzt wird es an drei Stellen: beim Anlegen der Kopie (kopieAnlegen, in
// derselben Transaktion), am Hinweg (ErsetzeKopieFreigabe, in derselben
// Transaktion wie Struktur und Schatten) und im Admin-Reiter
// (KopieBearbeitbarSetzen). Abschalten ändert keinen Bestand: Journal und
// wartende Spiegel bleiben, wie sie sind (Nachtrag Punkt 4).
//
// Die Rolle des Board-Paar-Teams folgt je Kopie dem wirksamen Rückweg
// (Nachtrag Punkt 6): editor nur bei an UND bearbeitbar, sonst viewer. Jeder
// der drei Wege gleicht sie in derselben Transaktion an
// (bearbeitbarSetzenMitTeamTx); ist der Server-Schalter aus, ist das Team
// ohnehin Leser, und es geschieht nichts.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// kopieBearbeitbarMit: Ist das Board eine Kopie (kopie = 1) mit
// bearbeitbar = 1? Keine Zeile oder keine Kopie → false ohne Fehler; ein
// Lesefehler → false mit Fehler (der Aufrufer schreibt dann nichts).
func kopieBearbeitbarMit(ctx context.Context, q zeilenAbfrager, id string) (bool, error) {
	var kopie, bearbeitbar int
	err := q.QueryRowContext(ctx, `SELECT kopie, bearbeitbar FROM boardpaar_aussen WHERE aussen_board_id = ?`, id).
		Scan(&kopie, &bearbeitbar)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("boards.kopie-bearbeitbar: %w", err)
	}
	return kopie == 1 && bearbeitbar == 1, nil
}

// KopieBearbeitbar: Ist die Kopie außen freigegeben (bearbeitbar = 1)? Nur
// zusammen mit boardpaar.rueckweg = an wirksam (api.kopieRueckwegAn).
func (r *BoardRepo) KopieBearbeitbar(ctx context.Context, id string) (bool, error) {
	return kopieBearbeitbarMit(ctx, r.db, id)
}

// KopieRueckwegWirksamTx: der wirksame Rückweg einer Kopie — boardpaar.rueckweg
// = an UND bearbeitbar = 1 —, gelesen auf der Verbindung der laufenden
// Transaktion (F-2300). Der Ops-Weg liest beide Schalter schon vor der Sperre
// (api.kopieRueckwegAnMitFehler); ein Umschalten zwischen diesem Lesen und dem
// COMMIT ließe den Batch sonst nach der alten Regel durch. Unter BEGIN
// IMMEDIATE schließt sich das Lesen hier mit jedem Setzweg aus. Den Schalter
// des Boards liest es nur, wenn der des Servers an ist; ein Lesefehler ist
// false mit Fehler.
func KopieRueckwegWirksamTx(ctx context.Context, q zeilenAbfrager, boardID string) (bool, error) {
	an, err := rueckwegAnTx(ctx, q)
	if err != nil || !an {
		return false, err
	}
	return kopieBearbeitbarMit(ctx, q, boardID)
}

// bearbeitbarSetzenTx setzt das Feld einer Kopie auf der Verbindung der
// laufenden Transaktion. Trifft es keine Zeile mit kopie = 1, geschieht
// nichts (die Aufrufer haben die Kopie vorher geprüft).
func bearbeitbarSetzenTx(ctx context.Context, ex Ausfuehrer, boardID string, wert bool) error {
	v := 0
	if wert {
		v = 1
	}
	if _, err := ex.ExecContext(ctx,
		`UPDATE boardpaar_aussen SET bearbeitbar = ? WHERE aussen_board_id = ? AND kopie = 1`, v, boardID); err != nil {
		return fmt.Errorf("boardpaar.bearbeitbar: %w", err)
	}
	return nil
}

// teamAusKonfigTx: boardpaar.team auf der Verbindung der laufenden
// Transaktion (nachsichtig gelesen wie beim Anlegen). Ein Lesefehler ist
// ErrBoardpaarTeamUnlesbar.
func teamAusKonfigTx(ctx context.Context, conn *sql.Conn) ([]string, error) {
	roh, err := KonfigKlartextTx(ctx, conn, KonfigBoardpaarTeamSchluessel)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBoardpaarTeamUnlesbar, err)
	}
	return TeamNachsichtig(roh), nil
}

// bearbeitbarSetzenMitTeamTx setzt den Schalter und gleicht, wenn
// boardpaar.rueckweg = an ist, die Rolle des Teams auf DIESER Kopie an
// (Nachtrag Punkt 6). team nil heißt: aus boardpaar.team lesen, in derselben
// Transaktion.
func bearbeitbarSetzenMitTeamTx(ctx context.Context, conn *sql.Conn, boardID string, wert bool, team []string) error {
	if err := bearbeitbarSetzenTx(ctx, conn, boardID, wert); err != nil {
		return err
	}
	an, err := rueckwegAnTx(ctx, conn)
	if err != nil {
		return fmt.Errorf("boardpaar.bearbeitbar: %w", err)
	}
	if !an {
		return nil
	}
	if team == nil {
		if team, err = teamAusKonfigTx(ctx, conn); err != nil {
			return err
		}
	}
	n, err := BoardpaarTeamRolleTx(ctx, conn, boardID, team, true)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("boardpaar: kopie %s bearbeitbar=%v, team-rolle angeglichen (%d zeile(n))", boardID, wert, n)
	}
	return nil
}

// KopieBearbeitbarSetzen — der Admin-Weg (Notweg, Nachtrag Punkt 3). Nicht
// gekoppelt → ErrBoardpaarNichtGekoppelt; gekoppelt, aber keine Kopie
// (Stufe 1, kopie = 0) → ErrBoardpaarKeineKopie. Schickt das Skript das Feld
// am nächsten Hinweg, gewinnt sein Wert. Schalter und Team-Rolle in einer
// Transaktion (BEGIN IMMEDIATE, wie der Schalter-Weg von boardpaar.rueckweg).
func (r *BoardpaarRepo) KopieBearbeitbarSetzen(ctx context.Context, boardID string, wert bool) error {
	if boardID == "" || boardID == ServiceBoardID {
		return ErrBoardpaarNichtGekoppelt
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("boardpaar.bearbeitbar: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return fmt.Errorf("boardpaar.bearbeitbar: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var kopie int
	err = conn.QueryRowContext(ctx, `SELECT kopie FROM boardpaar_aussen WHERE aussen_board_id = ?`, boardID).Scan(&kopie)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBoardpaarNichtGekoppelt
	}
	if err != nil {
		return fmt.Errorf("boardpaar.bearbeitbar: %w", err)
	}
	if kopie != 1 {
		return ErrBoardpaarKeineKopie
	}
	if err := bearbeitbarSetzenMitTeamTx(ctx, conn, boardID, wert, nil); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("boardpaar.bearbeitbar: commit: %w", err)
	}
	committed = true
	return nil
}
