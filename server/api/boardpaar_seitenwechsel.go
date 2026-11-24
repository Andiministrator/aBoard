package api

// boardpaar_seitenwechsel.go — F-2021 (Sprint-89-Kritiker KS-6, Karte #805).
//
// Die Seite einer Instanz (boardpaar.seite) lässt sich nur wechseln, wenn auf
// der bisherigen Seite kein Board mehr gekoppelt ist. Vorher blieben beim
// Umschalten weg von „aussen“ Kopplungen und Schatten stehen (einziger
// Löschweg ist das Entkoppeln, das die Schatten per Fremdschlüssel mitnimmt);
// beim Zurückschalten erschien bis zum nächsten Lauf der alte Stand. Statt
// beim Umschalten still Bestand zu löschen, verweigert der Wechsel: erst
// entkoppeln (Admin-Reiter „Board-Paar“ bzw. `aboard-server boardpaar
// entkoppeln`), dann umschalten. Gilt für alle drei Schreibwege — PUT und
// DELETE /admin/config/boardpaar.seite sowie die CLI.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Andiministrator/aBoard/server/storage"
)

// ErrSeitenwechselGekoppelt: Auf der bisherigen Seite sind noch Boards
// gekoppelt. Handler antworten 409.
var ErrSeitenwechselGekoppelt = errors.New("boardpaar: seite nicht wechselbar, solange Boards gekoppelt sind")

// PruefeSeitenwechsel prüft, ob die Seite von ihrem heutigen Wert auf neu
// wechseln darf. neu muss bereits normalisiert sein (PruefeBoardpaarSeite).
// Ohne Repo (Instanz ohne Board-Paar-Verdrahtung) gibt es keine Kopplungen.
// Ist die heutige Seite nicht lesbar, kommt der Lesefehler zurück — der
// Wechsel wird dann nicht blind erlaubt.
func PruefeSeitenwechsel(ctx context.Context, cfg *storage.ConfigRepo, repo *storage.BoardpaarRepo, neu string) error {
	if repo == nil {
		return nil
	}
	alt, err := BoardpaarSeiteMitFehler(ctx, cfg)
	if err != nil {
		return err
	}
	if alt == neu || alt == BoardpaarSeiteAus {
		return nil
	}
	var boards []string
	switch alt {
	case BoardpaarSeiteInnen:
		liste, lerr := repo.InnenListe(ctx)
		if lerr != nil {
			return lerr
		}
		for _, e := range liste {
			boards = append(boards, e.BoardID)
		}
	case BoardpaarSeiteAussen:
		liste, lerr := repo.AussenListe(ctx)
		if lerr != nil {
			return lerr
		}
		for _, e := range liste {
			boards = append(boards, e.BoardID)
		}
	}
	if len(boards) == 0 {
		return nil
	}
	return fmt.Errorf("%w: von „%s“ auf „%s“ erst nach dem Entkoppeln von %d Board(s) %v",
		ErrSeitenwechselGekoppelt, alt, neu, len(boards), boards)
}

// schreibeSeitenwechselFehler: 409 bei bestehenden Kopplungen (die Meldung
// nennt die Boards, der Admin-Dialog zeigt sie an), 503, wenn die heutige
// Seite oder die Kopplungen nicht lesbar sind.
func schreibeSeitenwechselFehler(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSeitenwechselGekoppelt) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusServiceUnavailable, "boardpaar: seite nicht prüfbar", err)
}
