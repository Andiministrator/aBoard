package api

// Karte #460 / F-878 (Teil 2) — die Zuordnung Store-Fehler → HTTP-Status.
//
// Warum ein interner Test und kein Routen-Test: Über die HTTP-Routen ist
// ErrReservierteAblage nicht erreichbar. `cardIDAusPfad` weist die Board-Ablage
// schon vor dem Store-Aufruf mit 400 ab (und `handleTransfer` ebenso). Ein
// Routen-Test würde also den Vorab-Check messen und wäre grün, egal was
// `attachmentFehlerAntwort` tut — genau die Sorte Zusicherung, die etwas
// anderes prüft als das, was sie zu prüfen behauptet.
//
// GRENZE DIESES TESTS, ausdrücklich benannt (Sprint-62-Verify): Er prüft die
// ZUORDNUNG, nicht ihre Verwendung. Dass `deleteCard` und `handleTransfer` sie
// tatsächlich aufrufen, steht hier NICHT — eine Gegenprobe, die den Aufruf in
// `transfer.go` durch ein pauschales 500 ersetzt, lässt diesen Test grün. Das
// ist keine Nachlässigkeit, sondern dieselbe Lage wie oben: Beide Aufrufstellen
// sind über die Routen nicht erreichbar (Vorab-Checks in `cardIDAusPfad` und
// `handleTransfer` fangen die Board-Ablage vorher ab), und `a.attach` ist ein
// konkreter `*storage.AttachmentStore` ohne Interface — es gibt keinen Weg,
// dem Handler den Fehler unterzuschieben, ohne genau die Vorab-Prüfung
// auszubauen, die ihn richtig beantwortet. Eine offen benannte Lücke ist
// besser als eine behauptete Abdeckung.
//
// Die Aussenwirkung sichert `board_attachments_test.go` zu
// (TestBoardAttachments_ReservierterBezeichnerAufKartenRouteAbgelehnt: DELETE auf
// /boards/{id}/attachments/__board__ → 400). Sie war schon vor dieser Karte
// richtig, eben weil der Vorab-Check greift — der Fix betrifft ausschliesslich
// die zweite Schranke dahinter. Beide Tests zusammen decken den Weg ab;
// einzeln deckt keiner den Fix.

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAttachmentFehlerAntwort(t *testing.T) {
	faelle := []struct {
		name       string
		err        error
		fallback   string // leer = "delete failed"
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "reservierte Board-Ablage ist eine abgelehnte Client-Anfrage",
			err:        storage.ErrReservierteAblage,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "reserved cardId",
		},
		{
			name: "auch eingepackt — der Store darf den Fehler kontextualisieren",
			// %w statt %v: Ein `fmt.Errorf("…: %v", err)` bricht die Kette und
			// die Zuordnung fiele still auf 500 zurück. Der Test hält fest,
			// dass errors.Is benutzt wird und nicht ein Gleichheitsvergleich.
			err:        fmt.Errorf("attachments.DeleteCard: %w", storage.ErrReservierteAblage),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "reserved cardId",
		},
		{
			// D1 (Sprint-62-Kritiker): Dieselbe Zuordnung wird von zwei
			// Aufrufern gebraucht — `deleteCard` (Fallback "delete failed")
			// und `handleTransfer` (Fallback "attachment copy failed"). Der
			// Fallback-Text darf die Zuordnung nicht beeinflussen.
			name:       "reservierte Ablage — auch mit dem Transfer-Fallbacktext",
			err:        storage.ErrReservierteAblage,
			fallback:   "attachment copy failed",
			wantStatus: http.StatusBadRequest,
			wantMsg:    "reserved cardId",
		},
		{
			name:     "Transfer-Fallback bleibt bei anderen Fehlern erhalten",
			err:      errors.New("disk on fire"),
			fallback: "attachment copy failed",

			wantStatus: http.StatusInternalServerError,
			wantMsg:    "attachment copy failed",
		},
		{
			name:       "alles andere bleibt ein Serverfehler",
			err:        errors.New("disk on fire"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "delete failed",
		},
		{
			name:       "auch ein Pfad-Fehler des Stores — er entsteht nicht aus der Anfrage allein",
			err:        storage.ErrUnsafePathComponent,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "delete failed",
		},
	}

	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			fallback := f.fallback
			if fallback == "" {
				fallback = "delete failed"
			}
			status, msg := attachmentFehlerAntwort(f.err, fallback)
			if status != f.wantStatus {
				t.Errorf("Status: want %d, got %d", f.wantStatus, status)
			}
			if msg != f.wantMsg {
				t.Errorf("Meldung: want %q, got %q", f.wantMsg, msg)
			}
		})
	}
}
