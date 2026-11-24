package storage

// boardpaar_zeile_loeschen.go — abgeschlossene Board-Paar-Zugänge aus der
// Tabelle entfernen (Karte #848, Wunsch des Maintainers, 20.10.2026).
//
// Mit jeder wöchentlichen Erneuerung (Karte #796) bleibt eine abgelöste Zeile
// stehen; die Tabelle im Admin-Reiter wuchs ohne Ende. Entfernt werden darf
// nur, was nicht mehr gilt:
//   - widerrufen (revoked_at gesetzt), oder
//   - abgelöst UND die Übergangszeit ist vorbei (gilt_bis in der Vergangenheit).
// Ein Zugang ohne Erneuerung und ohne Widerruf gilt; ein erneuerter in seiner
// Übergangszeit auch — erneuerungWiederholen braucht dann seine Zeile. Beides
// lehnt der Server ab (ErrBoardpaarZugangGiltNoch).
//
// Die Frist wird gegen die Uhr geprüft und deshalb nur bei gestellter Uhr
// (Wissen §38): Ein Pi, der in der Vergangenheit startet, hielte sonst einen
// Vorgänger für abgelaufen, dessen zehn Minuten noch laufen. Ein widerrufener
// Zugang hängt an keiner Frist und darf auch bei ungestellter Uhr weg.
//
// Prüfen und Löschen sind EIN Statement (Wissen §50, F-2321): Das DELETE trägt
// den gelesenen Stand als Bedingung. Hat sich die Zeile dazwischen geändert,
// trifft es nichts, und es wird neu gelesen — nie gezählt und dann gelöscht.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrBoardpaarZugangGiltNoch: der Zugang gilt (oder ist in seiner
// Übergangszeit) — er wird widerrufen, nicht entfernt.
var ErrBoardpaarZugangGiltNoch = errors.New("storage: board-paar-zugang gilt noch und lässt sich nicht entfernen")

// AuditActionBoardpaarZugangEntfernen protokolliert das Entfernen einer
// Zugangszeile. Ein vergangener Vorgang, kein geltender Zustand — deshalb
// nicht in AuditAktionenOhneFrist, er läuft mit der normalen Frist ab.
const AuditActionBoardpaarZugangEntfernen = "boardpaar-zugang-entfernen"

// BoardpaarZeile: was vom entfernten Zugang für Audit und Log bleibt.
type BoardpaarZeile struct {
	ID   string
	Typ  string
	Name string
}

// zeileAbgeschlossen: widerrufen, oder abgelöst und die Übergangszeit vorbei.
// Ein unlesbarer Zeitpunkt zählt als „gilt noch“ — beim Löschen ist Behalten
// die vorsichtige Richtung (umgekehrt zu tokenGilt, wo Ablehnen es ist).
func zeileAbgeschlossen(revokedAt, giltBis string, jetzt time.Time, uhrOK bool) (bool, error) {
	if revokedAt != "" {
		return true, nil
	}
	if giltBis == "" {
		return false, nil
	}
	if !uhrOK {
		return false, ErrBoardpaarUhr
	}
	bis, err := time.Parse(time.RFC3339Nano, giltBis)
	if err != nil {
		return false, nil
	}
	return !bis.After(jetzt), nil
}

// zeileNachLesen läuft zwischen Lesen und Löschen — nur für Tests, die dort
// eine Änderung der Zeile einschieben (wie erneuernNachLesen, Wissen §29).
var zeileNachLesen = func(id string) {}

// ZugangZeileEntfernen löscht die Zeile eines abgeschlossenen Zugangs.
// Unbekannt → ErrBoardpaarTokenUnbekannt; gilt noch → ErrBoardpaarZugangGiltNoch;
// abgelöst, aber Uhr nicht gestellt → ErrBoardpaarUhr.
func (r *BoardpaarRepo) ZugangZeileEntfernen(ctx context.Context, id string) (*BoardpaarZeile, error) {
	// Zwei Runden: Ändert sich die Zeile zwischen Lesen und Löschen (Widerruf
	// oder Erneuerung), liest die zweite Runde den neuen Stand.
	for runde := 0; runde < 2; runde++ {
		z := BoardpaarZeile{ID: id}
		var revoked, giltBis string
		err := r.db.QueryRowContext(ctx,
			`SELECT typ, name, coalesce(revoked_at, ''), coalesce(gilt_bis, '') FROM boardpaar_tokens WHERE id = ?`, id).
			Scan(&z.Typ, &z.Name, &revoked, &giltBis)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBoardpaarTokenUnbekannt
		}
		if err != nil {
			return nil, fmt.Errorf("boardpaar.zeile-entfernen: %w", err)
		}
		fertig, err := zeileAbgeschlossen(revoked, giltBis, timeNow().UTC(), uhrGestellt())
		if err != nil {
			return nil, err
		}
		if !fertig {
			return nil, ErrBoardpaarZugangGiltNoch
		}
		zeileNachLesen(id)
		res, err := r.db.ExecContext(ctx,
			`DELETE FROM boardpaar_tokens WHERE id = ? AND coalesce(revoked_at, '') = ? AND coalesce(gilt_bis, '') = ?`,
			id, revoked, giltBis)
		if err != nil {
			return nil, fmt.Errorf("boardpaar.zeile-entfernen: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return &z, nil
		}
	}
	return nil, ErrBoardpaarZugangGiltNoch
}

// ZugaengeAufraeumen entfernt alle abgeschlossenen Zugänge; jeder einzeln über
// ZugangZeileEntfernen, also mit derselben Prüfung. Zurück kommen die
// entfernten Zeilen und die Zahl der übersprungenen Kandidaten (gilt noch,
// Uhr nicht gestellt, inzwischen weg). Ein Datenbankfehler bricht ab; was bis
// dahin entfernt ist, bleibt entfernt und steht in der Rückgabe.
func (r *BoardpaarRepo) ZugaengeAufraeumen(ctx context.Context) ([]BoardpaarZeile, int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM boardpaar_tokens WHERE revoked_at IS NOT NULL OR gilt_bis IS NOT NULL ORDER BY created_at`)
	if err != nil {
		return nil, 0, fmt.Errorf("boardpaar.aufraeumen: %w", err)
	}
	var kandidaten []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, 0, fmt.Errorf("boardpaar.aufraeumen: %w", err)
		}
		kandidaten = append(kandidaten, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, fmt.Errorf("boardpaar.aufraeumen: %w", err)
	}
	_ = rows.Close()
	weg := []BoardpaarZeile{}
	uebersprungen := 0
	for _, id := range kandidaten {
		z, err := r.ZugangZeileEntfernen(ctx, id)
		switch {
		case err == nil:
			weg = append(weg, *z)
		case errors.Is(err, ErrBoardpaarZugangGiltNoch), errors.Is(err, ErrBoardpaarUhr), errors.Is(err, ErrBoardpaarTokenUnbekannt):
			uebersprungen++
		default:
			return weg, uebersprungen, err
		}
	}
	return weg, uebersprungen, nil
}
