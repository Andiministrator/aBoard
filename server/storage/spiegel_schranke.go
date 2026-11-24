package storage

// spiegel_schranke.go — Spiegel-Schranke des Board-Paars, Schritt 2b
// (ADR-0141 §7, Karte #826).
//
// Auf einer externen Kopie (boardpaar_aussen mit kopie = 1) sind Spiegel —
// Karten, Termine, Lanes und Spalten mit der Marke `spiegel` — bis Schritt 2c
// nur lesbar: Es gibt noch keinen Rückweg, eine Änderung außen ginge beim
// nächsten Hinweg still verloren (VF-9). Geprüft wird am Ergebnis gegen den
// Stand davor (schatten.SpiegelVerstoesse), an jeder Schreibstelle auf
// boards.state_json, die ein Mensch oder Agent erreicht:
//   - ApplyOps: im Ops-Handler (server/api/boards_ops.go), mit Protokoll je Op;
//   - Update (Ganz-PUT, auch Restore): hier, in der Transaktion;
//   - TransferCard: hier, für Quell- und Ziel-Board.
// Der Eingang (ErsetzeKopie) ist der einzige Schreiber der Spiegel und läuft
// an der Schranke vorbei — er hat einen eigenen Schreibweg, kein
// Kontext-Merkmal ist nötig. Insert (Import, Restore als neues Board, Kopie als
// neues Board) entfernt die Marken (§5.1): Ein so angelegtes Board ist nie eine
// Kopie.
//
// Die Einstellung boardpaar.rueckweg aus §7 gibt es in 2b bewusst nicht: Sie
// hätte nur den Wert „aus“, und ein Schalter, der nichts schaltet, verwirrt.
// Sie kommt mit dem Rückweg in 2c.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// ErrSpiegelGeschuetzt: Der Schreibvorgang änderte einen Spiegel einer
// externen Kopie; nichts geschrieben. Der Aufrufer antwortet mit 403.
var ErrSpiegelGeschuetzt = errors.New("storage: spiegel einer externen kopie sind nur lesbar")

// SpiegelGeschuetztError nennt die betroffenen Spiegel-Objekte.
type SpiegelGeschuetztError struct {
	Orte     []schatten.SpiegelOrt
	Unlesbar bool
}

func (e *SpiegelGeschuetztError) Error() string {
	if e.Unlesbar {
		return "Spiegel-Schranke: Board-Zustand nicht lesbar — nichts geschrieben"
	}
	return SpiegelMeldung(e.Orte)
}

func (e *SpiegelGeschuetztError) Unwrap() error { return ErrSpiegelGeschuetzt }

// SpiegelMeldung: die Meldung für Menschen (PUT, Transfer), mit höchstens
// fünf Objekten.
func SpiegelMeldung(orte []schatten.SpiegelOrt) string {
	var teile []string
	for i, o := range orte {
		if i == 5 {
			teile = append(teile, fmt.Sprintf("und %d weitere", len(orte)-5))
			break
		}
		teile = append(teile, o.Art+" "+o.ID)
	}
	return "Dieses Board ist eine externe Kopie des internen Boards; Karten, Termine, Lanes und Spalten von innen " +
		"sind hier nur lesbar — Änderungen bitte im internen Board vornehmen (ADR-0141 §7). Betroffen: " +
		strings.Join(teile, ", ")
}

type zeilenAbfrager interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// istKopieMit: Steht das Board als Kopie (kopie = 1) in boardpaar_aussen?
func istKopieMit(ctx context.Context, q zeilenAbfrager, id string) (bool, error) {
	var kopie int
	err := q.QueryRowContext(ctx, `SELECT kopie FROM boardpaar_aussen WHERE aussen_board_id = ?`, id).Scan(&kopie)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("boards.ist-kopie: %w", err)
	}
	return kopie == 1, nil
}

// IstKopie: Ist das Board eine externe Kopie (ADR-0141 §3, kopie = 1)? Nur
// dann gilt die Spiegel-Schranke. Ein außen entkoppeltes Board (Marken, aber
// keine Zeile) ist keine Kopie mehr und wieder frei bearbeitbar.
func (r *BoardRepo) IstKopie(ctx context.Context, id string) (bool, error) {
	return istKopieMit(ctx, r.db, id)
}

// spiegelSchranke prüft einen Schreibvorgang auf einem Board: Ist es eine
// Kopie und berührt der Schritt von vorher nach nachher einen Spiegel oder
// einen wartenden Spiegel → *SpiegelGeschuetztError.
//
// Ganz-PUT und Transfer journalisieren nicht, auch mit Rückweg nicht
// („Umsetzung 2c“ Entscheidung 2): Sie lehnen deshalb jede Änderung an
// Spiegeln (2b-Fassung) und an wartenden Spiegeln (außen angelegt, Marke
// steht aus) ab, gleich wie boardpaar.rueckweg steht. Sonst ginge eine
// solche Änderung still verloren.
//
// Der Vorfilter KannSpiegelBetreffen trägt für wartende Karten: Es gibt sie
// nur in markierten Spalten einer Kopie, und die Marken stehen dann im Stand
// vorher. Für wartende Termine trägt er nicht (Sprint-94-Kritik V-2): Jeder
// neue Termin auf einer Kopie wird wartend, auch vor dem ersten Hinweg — auf
// einer Kopie ohne Marken überspringt der Vorfilter WartendVerstoesse dann.
// Hingenommen: Der Termin steht innen erst nach dem Anwenden, der nächste
// Hinweg bringt die innere Fassung zurück.
func spiegelSchranke(ctx context.Context, q Abfrager, id string, vorher, nachher []byte) error {
	if !schatten.KannSpiegelBetreffen(vorher, nachher) {
		return nil
	}
	kopie, err := istKopieMit(ctx, q, id)
	if err != nil || !kopie {
		return err
	}
	orte, unlesbar := schatten.SpiegelVerstoesse(vorher, nachher)
	wartend, err := BoardpaarWartend(ctx, q, id)
	if err != nil {
		return err
	}
	w, wUnlesbar := schatten.WartendVerstoesse(vorher, nachher, wartend)
	orte = schatten.SpiegelOrteVereint(orte, w)
	if len(orte) > 0 || unlesbar || wUnlesbar {
		return &SpiegelGeschuetztError{Orte: orte, Unlesbar: unlesbar || wUnlesbar}
	}
	return nil
}
