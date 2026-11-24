package storage

// finding_zaehler.go — board.nextFindingNr beim Ganz-Board-Schreiben nie
// absenken (F-1544, Karte #676, ADR-0133 §1).
//
// Der Zähler ist das einzige Gedächtnis für gelöschte Findings: Fehlt er, leitet
// der Fold (src/finding.js nextFindingNr) ihn aus max(nr) + 1 ab — doppelt wird
// dabei nie etwas, aber die Nummer eines gelöschten HÖCHSTEN Findings kehrt
// zurück, und Verweise darauf (Commits, Kommentare) zeigten dann auf einen
// anderen Befund.
//
// Verloren geht das Feld nur auf einem Weg, der den ganzen Zustand ersetzt:
// dem PUT eines Browserstands (Konfliktauflösung „meins gewinnt“, ein Stand
// von vor dem ersten Finding) und dem Import (server/import.go). Beide laufen
// durch BoardRepo.Update, deshalb sitzt die Sperre hier und nicht im Handler.
// Fold (ApplyOps), Transfer und LLM-Kartenoperationen lesen den gespeicherten
// Zustand und tragen das Feld weiter.
//
// state_json bleibt sonst opak (ADR-0044): gelesen wird nur `board.nextFindingNr`,
// umgeschrieben nur, wenn der neue Stand den Zähler absenken würde. Im
// Normalfall bleiben die Bytes des Schreibers unverändert.

import (
	"context"
	"encoding/json"
	"fmt"
)

type findingNummernBehaltenKey struct{}

// MitFindingNummernBehalten markiert einen Schreibvorgang, der Finding-Karten
// mit ihrer URSPRÜNGLICHEN Nummer zurückbringt — die Archiv-Rückspielung
// (boardbackup/restore.go). Eine dort seit dem Sichern gelöschte Karte ist
// derselbe Befund, kein neuer; sie umzunummerieren ließe Verweise (Commits,
// Kommentare auf „F-7“) ins Leere zeigen. Der Zähler wird trotzdem nie
// abgesenkt (F-1544).
func MitFindingNummernBehalten(ctx context.Context) context.Context {
	return context.WithValue(ctx, findingNummernBehaltenKey{}, true)
}

func findingNummernBehalten(ctx context.Context) bool {
	v, _ := ctx.Value(findingNummernBehaltenKey{}).(bool)
	return v
}

type findingZaehlerSicht struct {
	Board struct {
		NextFindingNr any `json:"nextFindingNr"`
	} `json:"board"`
}

// findingZaehler liest board.nextFindingNr als ganze Zahl ≥ 1; sonst 0.
func findingZaehler(stateJSON []byte) int {
	var sicht findingZaehlerSicht
	if err := json.Unmarshal(stateJSON, &sicht); err != nil {
		return 0
	}
	nr, _ := ganzzahl(sicht.Board.NextFindingNr)
	return nr
}

// findingZaehlerBewahren liefert neuJSON, bei Bedarf mit dem Zähler aus altJSON:
// liegt board.nextFindingNr im neuen Stand unter dem alten (oder fehlt), wird
// der alte Wert eingesetzt. Ein unlesbarer alter Stand ändert nichts — dann
// gibt es nichts zu bewahren. Ein unlesbarer neuer Stand ebenfalls nicht; ihn
// abzulehnen ist nicht Sache dieser Sperre.
//
// F-1685 (Kritiker R2 KF-8): Dazu bekommt eine NEUE Finding-Karte (ihre id
// fehlt im alten Stand), deren Nummer unter dem Zähler liegt, fortlaufend die
// nächste freie Nummer — jede Nummer unter dem Zähler war schon einmal
// vergeben, und ein Verweis darauf (Commit, Kommentar) zeigte sonst auf einen
// anderen Befund. Umnummeriert statt abgelehnt: Ein Browser mit veraltetem
// Zähler soll im PUT-Rückfall nicht scheitern (derselbe Grund, aus dem der
// Fold die Nummer selbst vergibt). Liegt die Nummer noch auf einer bestehenden
// Karte, hat NeueFindingVerstoesse den Schreibvorgang schon vorher abgelehnt.
// Bestehende Karten — auch alte Doppelnummern — bleiben unangetastet.
func findingZaehlerBewahren(altJSON, neuJSON []byte, umnummerieren bool) ([]byte, error) {
	altZaehler := findingZaehler(altJSON)
	altIDs, altMax := findingKartenSicht(altJSON)
	grenze := max(altZaehler, altMax+1)
	if altIDs == nil || grenze <= 1 {
		return neuJSON, nil // unlesbar, oder nichts, was je vergeben war
	}
	var state map[string]any
	if err := json.Unmarshal(neuJSON, &state); err != nil {
		return neuJSON, nil
	}
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return neuJSON, nil
	}
	type findingKarte struct {
		f         map[string]any
		nr        int
		umnummern bool
	}
	var karten []findingKarte
	belegt := map[int]bool{} // Nummern, die bleiben — beim Vergeben überspringen (PF-1)
	for _, k := range kartenAus(board) {
		f, _ := k["finding"].(map[string]any)
		if k["kind"] != "finding" || f == nil {
			continue
		}
		nr, ok := ganzzahl(f["nr"])
		id, _ := k["id"].(string)
		fk := findingKarte{f: f, nr: nr, umnummern: umnummerieren && ok && !altIDs[id] && nr < grenze}
		if !fk.umnummern && ok {
			belegt[nr] = true
		}
		karten = append(karten, fk)
	}
	naechste := grenze
	neuMax := 0
	geaendert := false
	for _, fk := range karten {
		if fk.umnummern {
			for belegt[naechste] {
				naechste++
			}
			fk.f["nr"] = float64(naechste)
			belegt[naechste] = true
			fk.nr = naechste
			naechste++
			geaendert = true
		}
		neuMax = max(neuMax, fk.nr)
	}
	ziel := max(altZaehler, naechste, neuMax+1)
	if !geaendert && findingZaehler(neuJSON) >= altZaehler {
		return neuJSON, nil
	}
	if !geaendert {
		ziel = altZaehler // wie bisher: nur nicht absenken
	}
	if z := findingZaehler(neuJSON); z > ziel {
		ziel = z
	}
	board["nextFindingNr"] = float64(ziel)
	out, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("nextFindingNr bewahren: %w", err)
	}
	return out, nil
}

// findingKartenSicht: alle Karten-ids des Stands (nil = unlesbar) und die
// höchste vorhandene Finding-Nummer.
func findingKartenSicht(stateJSON []byte) (map[string]bool, int) {
	var state map[string]any
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return nil, 0
	}
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return nil, 0
	}
	ids := map[string]bool{}
	hoechste := 0
	for _, k := range kartenAus(board) {
		if id, _ := k["id"].(string); id != "" {
			ids[id] = true
		}
		if f, _ := k["finding"].(map[string]any); k["kind"] == "finding" && f != nil {
			if nr, ok := ganzzahl(f["nr"]); ok {
				hoechste = max(hoechste, nr)
			}
		}
	}
	return ids, hoechste
}

// kartenAus liefert die Karten aller Swimlanes (board.sprints[].cards[]).
func kartenAus(board map[string]any) []map[string]any {
	var aus []map[string]any
	sprints, _ := board["sprints"].([]any)
	for _, sp := range sprints {
		m, _ := sp.(map[string]any)
		cards, _ := m["cards"].([]any)
		for _, c := range cards {
			if k, ok := c.(map[string]any); ok {
				aus = append(aus, k)
			}
		}
	}
	return aus
}
