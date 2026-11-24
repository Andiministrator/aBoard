package api

// boardpaar_journal_ops.go — Spiegel-Schranke mit Rückweg und Journal außen
// am Ops-Weg (ADR-0141 §5.1, §7; Karte #827, Etappe E1b Teil 3).
//
// Läuft im Schreibvorgang von ApplyOpsIdempotent, vor dem COMMIT, auf dessen
// Verbindung („Umsetzung 2c“ Entscheidung 3): Die Liste der wartenden
// Spiegel stammt so aus derselben Transaktion wie das Board, und Board und
// Journal werden gemeinsam wirksam oder gar nicht (knowledge §36). Ein
// zweites Lesen über den Pool hielte die Schreibsperre.
//
// Nur der Ops-Weg journalisiert (Entscheidung 2). Ganz-PUT und Transfer
// lehnen Änderungen an Spiegeln und wartenden Spiegeln ab (storage,
// spiegelSchranke).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Gründe im Protokoll.
const (
	spiegelRueckwegGrund = "mirror of the internal board — this change cannot be carried back to the internal board (ADR-0141 §7)"
	wartendGrund         = "created here and not yet arrived on the internal board — read-only until it arrives (ADR-0141 §5.1)"
)

// errKopieSchalterUnlesbar: boardpaar.rueckweg oder bearbeitbar war unter der
// Sperre nicht lesbar (F-2300) — nichts geschrieben, 503 wie beim Lesen vor
// der Sperre.
var errKopieSchalterUnlesbar = errors.New("boardpaar: rueckweg oder bearbeitbar unter der sperre nicht lesbar")

// errJournalUnlesbar: Ein Board-Zustand war für das Journal nicht lesbar,
// obwohl die Schranke ihn lesen konnte — nichts geschrieben.
var errJournalUnlesbar = errors.New("boardpaar.journal: board-zustand nicht lesbar")

// spiegelSchreibvorgang: die Prüfung und das Journal eines Ops-Batches auf
// einer Kopie (kopie = 1). vorher ist der Stand unter der Sperre, nachher das
// Ergebnis des Folds.
//   - Rückweg aus (2b): Spiegel prüft schon die Falt-Funktion; hier kommen
//     die wartenden Spiegel dazu (Reste aus einer Zeit mit Rückweg an).
//   - Rückweg an: feine Schranke (SpiegelVerstoesseRueckweg), dann Journal.
type spiegelSchreibvorgang struct {
	boardID    string
	rueckwegAn bool
	person     string
	akteurArt  string
	jetzt      func() time.Time
}

func (s spiegelSchreibvorgang) pruefeUndSchreibe(ctx context.Context, ex storage.Ausfuehrer, vorher, nachher []byte,
	angewandt, ops []serverOp) error {
	q, err := storage.AlsAbfrager(ex)
	if err != nil {
		return err
	}
	// F-2300: Vor der Sperre gelesen war der Rückweg an — unter der Sperre
	// noch einmal, ob er es geblieben ist. Ist er inzwischen aus (Server-
	// oder Board-Schalter), gilt die Regel von 2b: Die Spiegel-Prüfung, die
	// die Falt-Funktion bei „aus“ macht, kommt hier nach. Der umgekehrte Fall
	// (vorher aus, jetzt an) braucht nichts: der Batch ist nach der
	// strengeren Regel geprüft.
	rueckwegAn := s.rueckwegAn
	if rueckwegAn {
		jetzt, err := storage.KopieRueckwegWirksamTx(ctx, q, s.boardID)
		if err != nil {
			return fmt.Errorf("%w: %w", errKopieSchalterUnlesbar, err)
		}
		if !jetzt {
			if v, unlesbar := schatten.SpiegelVerstoesse(vorher, nachher); len(v) > 0 || unlesbar {
				roh, _ := json.Marshal(spiegelVerstossProtokoll(v, unlesbar, angewandt))
				return &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
			}
			rueckwegAn = false
		}
	}
	wartend, err := storage.BoardpaarWartend(ctx, q, s.boardID)
	if err != nil {
		return err
	}
	if !rueckwegAn {
		if v, unlesbar := schatten.WartendVerstoesse(vorher, nachher, wartend); len(v) > 0 || unlesbar {
			roh, _ := json.Marshal(spiegelVerstossProtokollGrund(wartendGrund, v, unlesbar, angewandt))
			return &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
		}
		return nil
	}
	if v, unlesbar := schatten.SpiegelVerstoesseRueckweg(vorher, nachher, wartend); len(v) > 0 || unlesbar {
		roh, _ := json.Marshal(spiegelVerstossProtokollGrund(spiegelRueckwegGrund, v, unlesbar, angewandt))
		return &opsAbgelehnt{protokoll: mitIndex(string(roh), ops)}
	}
	eintraege, neuWartend, ok := schatten.JournalEintraege(vorher, nachher, wartend)
	if !ok {
		return errJournalUnlesbar
	}
	if len(eintraege) == 0 {
		return nil
	}
	return storage.BoardpaarJournalSchreiben(ctx, q, s.boardID, eintraege, neuWartend, s.person, s.akteurArt, s.jetzt())
}

// journalPerson: wer außen geschrieben hat (§5.1). Ein bestätigter Vorschlag
// läuft unter dem Agenten, gilt aber als Änderung des bestätigenden Menschen
// (§7: „als person steht dann der bestätigende Mensch“); die Akteur-Art
// bleibt „agent“ und ist der Hinweis auf den Agenten.
func journalPerson(ctx context.Context, actor string) string {
	if b := vorschlagBestaetigerAus(ctx); b != "" {
		return b
	}
	if actor == "" {
		return "anonym" // Legacy-Bypass ohne Identität
	}
	return actor
}
