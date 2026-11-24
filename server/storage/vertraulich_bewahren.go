package storage

// vertraulich_bewahren.go — Schalter „vertraulich“ beim Ganz-Board-Schreiben
// nie abschalten (ADR-0140 §10, Karte #778, Stelle 3).
//
// BoardRepo.Update ist der gemeinsame Punkt der Wege, die den ganzen Zustand
// ohne Fold ersetzen: PUT samt „Meine Version behalten“, Import, Restore,
// Mail, LLM, Schleuse. War dort eine Karte oder ein Termin vertraulich,
// bleibt der Bestandswert stehen — gleich, ob der neue Stand den Schlüssel
// weglässt oder false trägt. Sonst entschärfte ein Restore eines älteren
// Stands oder ein veralteter Tab eine inzwischen vertrauliche Karte still,
// und ihr Titel ginge beim nächsten Lauf als Schatten hinaus. Abschalten
// geht deshalb ausschließlich über eine Op (card.field bzw. termin.upsert
// mit false) eines Menschen.
//
// Anders als findingZaehlerBewahren gilt das auch beim Restore: dort gibt es
// keine Ausnahme (ADR-0140 §10, ohne MitFindingNummernBehalten).
//
// Nicht über Update laufen — bewusst ohne diese Bewahrung:
//   - ApplyOpsIdempotent (Fold-Weg): dort bewahrt der Fold selbst
//     (src/ops.js), und die Agenten-Sperre prüft am Ergebnis;
//   - TransferCard (transfer.go): trägt die rohe Karte samt Feld weiter;
//   - Insert: es gibt keinen Bestand.

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// terminListeAus liefert die Termine des Boards (board.termine[]).
func terminListeAus(board map[string]any) []map[string]any {
	var aus []map[string]any
	liste, _ := board["termine"].([]any)
	for _, t := range liste {
		if m, ok := t.(map[string]any); ok {
			aus = append(aus, m)
		}
	}
	return aus
}

// vertraulicherBestand: Kennung → Bestandswert der vertraulichen Karten
// bzw. Termine (nil-Map, wenn der Stand unlesbar ist).
func vertraulicherBestand(stateJSON []byte) (karten, termine map[string]any) {
	var state map[string]any
	if json.Unmarshal(stateJSON, &state) != nil {
		return nil, nil
	}
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return nil, nil
	}
	karten, termine = map[string]any{}, map[string]any{}
	alle := kartenVertraulichkeit(board)
	for _, k := range kartenAus(board) {
		if id, _ := k["id"].(string); id != "" && schatten.VertraulichObjekt(k) {
			karten[id] = k["confidential"]
		}
	}
	for _, t := range terminListeAus(board) {
		id, _ := t["id"].(string)
		if id == "" {
			continue
		}
		if schatten.VertraulichObjekt(t) {
			termine[id] = t["confidential"]
		} else if terminWirksam(t, alle) {
			// Vertraulich nur über die Karte (oder weil sie fehlt): Wird die
			// Verbindung gelöst, bekommt der Termin den Schalter selbst
			// (Kritiker #778, K-1).
			termine[id] = true
		}
	}
	return karten, termine
}

// kartenVertraulichkeit: Kennung → vertraulich, für alle Karten.
func kartenVertraulichkeit(board map[string]any) map[string]bool {
	m := map[string]bool{}
	for _, k := range kartenAus(board) {
		if id, _ := k["id"].(string); id != "" {
			m[id] = m[id] || schatten.VertraulichObjekt(k)
		}
	}
	return m
}

func terminWirksam(t map[string]any, karten map[string]bool) bool {
	kid, _ := t["karteId"].(string)
	return schatten.TerminWirksamVertraulich(schatten.VertraulichObjekt(t), kid, karten)
}

// vertraulichBewahren liefert neuJSON, bei Bedarf mit dem Bestandswert
// „vertraulich“ aus altJSON, und die Kennungen der Einträge, bei denen das
// nötig war (für den Hinweis an den Browser). Im Normalfall bleiben die
// Bytes des Schreibers unverändert. Ein unlesbarer alter Stand ändert
// nichts. Ein unlesbarer neuer Stand bei vertraulichem Bestand ist ein
// Fehler: Er würde sonst ungeprüft gespeichert.
func vertraulichBewahren(altJSON, neuJSON []byte) ([]byte, []string, error) {
	if !schatten.KannVertraulichSein(altJSON) {
		return neuJSON, nil, nil
	}
	altK, altT := vertraulicherBestand(altJSON)
	if len(altK) == 0 && len(altT) == 0 {
		return neuJSON, nil, nil
	}
	// UseNumber: Zahlen bleiben wörtlich, statt durch float64 zu laufen
	// (Kritiker #778, K-7).
	var state map[string]any
	dec := json.NewDecoder(bytes.NewReader(neuJSON))
	dec.UseNumber()
	if err := dec.Decode(&state); err != nil {
		return nil, nil, fmt.Errorf("vertraulich bewahren: neuer Stand unlesbar: %w", err)
	}
	// Decode liest nur das erste Objekt — Daten dahinter sind ein unlesbarer
	// Stand wie bei Unmarshal (Verify #778, V-4).
	if dec.More() || !json.Valid(neuJSON) {
		// More() übersieht ein überzähliges `}`/`]` (Verify-2 #778, W2-2).
		return nil, nil, fmt.Errorf("vertraulich bewahren: neuer Stand hat Daten hinter dem JSON-Objekt")
	}
	board, _ := state["board"].(map[string]any)
	if board == nil {
		return nil, nil, fmt.Errorf("vertraulich bewahren: neuer Stand ohne board")
	}
	var bewahrt []string
	for _, k := range kartenAus(board) {
		id, _ := k["id"].(string)
		if wert, war := altK[id]; war && !schatten.VertraulichObjekt(k) {
			k["confidential"] = wert
			bewahrt = append(bewahrt, id)
		}
	}
	neuKarten := kartenVertraulichkeit(board)
	for _, t := range terminListeAus(board) {
		id, _ := t["id"].(string)
		if wert, war := altT[id]; war && !terminWirksam(t, neuKarten) {
			t["confidential"] = wert
			bewahrt = append(bewahrt, id)
		}
	}
	if len(bewahrt) == 0 {
		return neuJSON, nil, nil
	}
	out, err := json.Marshal(state)
	if err != nil {
		return nil, nil, fmt.Errorf("vertraulich bewahren: %w", err)
	}
	return out, bewahrt, nil
}
