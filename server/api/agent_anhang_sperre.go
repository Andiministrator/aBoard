package api

import (
	"encoding/json"
)

// --- Anhang-Listen auf dem rohen Weg nur ergänzen (Karte #766) -------------
//
// ADR-0139 E-3 ließ `attachments` offen (Verify W-9): Ein Agent konnte auf dem
// rohen Weg die Liste der Board-Anhänge (board.field) oder die einer Karte
// (card.field, card.upsert ohne Anhänge) überschreiben. Die Dateien bleiben
// dabei liegen, verloren geht die Zuordnung — niemand findet sie im Board
// wieder. Hochladen geht über die Anhang-Endpunkte, eingetragen wird danach
// per Op; deshalb keine Sperre des Felds, sondern: jeder bestehende Eintrag
// bleibt unverändert, neue dürfen dazukommen. Entfernen nur über das
// Lösch-Werkzeug (anhang_loeschen, Werkzeug-Weg, löscht auch die Datei).
//
// Geprüft wird am Ergebnis des Folds (wie ADR-0136), nicht an den Ops — so
// zählt nur, was am Ende im Board steht, gleich über welche Op. Karten, die
// es danach nicht mehr gibt, sind ausgenommen: card.delete/swimlane.delete
// bleiben auf dem rohen Weg erlaubt (ADR-0139 E-2).

// anhangSichereOps: Op-Typen, die keine Anhang-Liste einer bestehenden
// Karte oder des Boards kürzen können — sie nennen die Liste nicht und
// ersetzen weder eine Karte noch das Board als Ganzes. card.delete und
// swimlane.delete entfernen Karten, das ist erlaubt (E-2). Alles andere —
// card.upsert, swimlane.upsert, board.field, maintenance.*, jeder unbekannte
// Typ — gilt als anhangsrelevant (F-1886: lieber zu oft prüfen als zu selten).
// comment.delete gehört NICHT hierher: der Fold entfernt mit dem Kommentar
// auch seine Einträge aus card.attachments (ADR-0135, src/ops.js).
var anhangSichereOps = map[string]bool{
	"card.move": true, "card.delete": true, "card.claim": true, "card.release": true,
	"comment.add": true, "comment.edit": true,
	"swimlane.move": true, "swimlane.delete": true, "board.awaiting": true,
	"termin.upsert": true, "termin.delete": true,
}

// anhangFeldOps: Op-Typen, die genau ein Kartenfeld setzen — relevant nur,
// wenn das Feld attachments ist. Gelesen über den exakten Schlüssel, wie
// der Fold ihn liest; ein unlesbares Payload gilt als relevant.
var anhangFeldOps = map[string]bool{"card.field": true, "card.fieldIf": true, "card.listAdd": true, "card.listRemove": true}

// opsBeruehrenAnhaenge: ob die Anhang-Prüfung für diesen Batch laufen muss
// (Vorfilter, F-1886). Nur ein billiger Blick auf Typ und Feldname; das
// Urteil fällt weiter am Ergebnis des Folds.
func opsBeruehrenAnhaenge(ops []serverOp) bool {
	if anhangVorfilterNaht != nil {
		anhangVorfilterNaht()
	}
	for _, op := range ops {
		if anhangSichereOps[op.Type] {
			continue
		}
		if !anhangFeldOps[op.Type] {
			return true
		}
		var p map[string]json.RawMessage
		var feld string
		if json.Unmarshal(op.Payload, &p) != nil || json.Unmarshal(p["field"], &feld) != nil || feld == "attachments" {
			return true
		}
	}
	return false
}

// anhangVorfilterNaht / anhangPruefungNaht: nur für Tests — zählen, wie oft
// der Vorfilter und die eigentliche Prüfung laufen.
var anhangVorfilterNaht, anhangPruefungNaht func()

// anhangOrtBoard: Ort der Board-Anhänge im Verstoß (keine Karten-id).
const anhangOrtBoard = ""

type anhangVerstoss struct {
	cardID string // anhangOrtBoard = Board-Anhänge
	anzahl int    // fehlende oder veränderte Einträge
}

// anhangListen liest die Anhang-Einträge des Boards und je Karte, jeden
// Eintrag in kanonischer Form (Schlüssel sortiert, Zahlen wörtlich). Gelesen
// wird über exakte Map-Schlüssel — Go ordnete Struct-Felder ohne Rücksicht
// auf die Schreibweise zu, der Fold liest exakt (vgl. agentPayload).
// ok false: Stand nicht lesbar.
func anhangListen(state []byte) (board []string, karten map[string][]string, ok bool) {
	var huelle map[string]json.RawMessage
	if json.Unmarshal(state, &huelle) != nil {
		return nil, nil, false
	}
	var b map[string]json.RawMessage
	if json.Unmarshal(huelle["board"], &b) != nil || b == nil {
		return nil, nil, false
	}
	karten = map[string][]string{}
	board = anhangEintraege(b["attachments"])
	var sprints []map[string]json.RawMessage
	_ = json.Unmarshal(b["sprints"], &sprints)
	for _, s := range sprints {
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(s["cards"], &cards)
		for _, k := range cards {
			var id string
			if json.Unmarshal(k["id"], &id) != nil || id == "" {
				continue
			}
			// Eine Karte, die (fehlerhaft) mehrfach vorkommt, trägt alle
			// Einträge aller Vorkommen; sie gilt als vorhanden.
			karten[id] = append(karten[id], anhangEintraege(k["attachments"])...)
		}
	}
	return board, karten, true
}

func anhangEintraege(roh json.RawMessage) []string {
	var liste []json.RawMessage
	if json.Unmarshal(roh, &liste) != nil {
		return nil
	}
	aus := make([]string, 0, len(liste))
	for _, el := range liste {
		k, _ := json.Marshal(kanonischerWert(el))
		aus = append(aus, string(k))
	}
	return aus
}

// fehlendeEintraege: wie viele Einträge aus alt in neu nicht (oder nicht so
// oft) vorkommen — Vergleich als Multimenge.
func fehlendeEintraege(alt, neu []string) int {
	da := map[string]int{}
	for _, e := range neu {
		da[e]++
	}
	fehlt := 0
	for _, e := range alt {
		if da[e] > 0 {
			da[e]--
		} else {
			fehlt++
		}
	}
	return fehlt
}

// anhangListenVerstoesse vergleicht Stand vorher und Ergebnis. Ist einer der
// beiden nicht lesbar, gilt das als Verstoß (Ausfall in Richtung Nichtstun):
// Für Agenten schreibt der Server dann nichts.
func anhangListenVerstoesse(vorher, nachher []byte) []anhangVerstoss {
	if anhangPruefungNaht != nil {
		anhangPruefungNaht()
	}
	altB, altK, ok1 := anhangListen(vorher)
	neuB, neuK, ok2 := anhangListen(nachher)
	if !ok1 || !ok2 {
		return []anhangVerstoss{{cardID: anhangOrtBoard, anzahl: -1}}
	}
	var aus []anhangVerstoss
	if n := fehlendeEintraege(altB, neuB); n > 0 {
		aus = append(aus, anhangVerstoss{cardID: anhangOrtBoard, anzahl: n})
	}
	for id, alt := range altK {
		neu, gibtEs := neuK[id]
		if !gibtEs {
			continue // gelöscht — E-2
		}
		if n := fehlendeEintraege(alt, neu); n > 0 {
			aus = append(aus, anhangVerstoss{cardID: id, anzahl: n})
		}
	}
	return aus
}

// anhangVerstossProtokoll: Fold-Protokoll-Form (art, op, typ, cardId), wie
// kommentarVerstossProtokoll — betroffen sind die Ops, die die Karte nennen
// bzw. bei den Board-Anhängen die board.field-Ops; findet sich keine, alle.
func anhangVerstossProtokoll(verstoesse []anhangVerstoss, ops []serverOp) []map[string]any {
	const grund = "agents may only add attachment entries via raw ops; existing entries must stay unchanged (remove attachments with the tool anhang_loeschen)"
	var aus []map[string]any
	for _, v := range verstoesse {
		var betroffen []serverOp
		for _, op := range ops {
			if v.cardID == anhangOrtBoard {
				if op.Type == "board.field" {
					betroffen = append(betroffen, op)
				}
			} else if opBetrifftKarte(op, v.cardID) {
				betroffen = append(betroffen, op)
			}
		}
		if len(betroffen) == 0 {
			betroffen = ops
		}
		for _, op := range betroffen {
			e := map[string]any{"art": "anhang-entfernt", "op": op.ID, "typ": op.Type, "grund": grund}
			if v.cardID != anhangOrtBoard {
				e["cardId"] = v.cardID
			}
			aus = append(aus, e)
		}
	}
	return aus
}
