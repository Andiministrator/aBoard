package api

// spiegel_schranke.go — Spiegel-Schranke am Ops-Endpunkt (ADR-0141 §7,
// Karte #826, Schritt 2b).
//
// Auf einer externen Kopie (kopie = 1) sind Spiegel bis 2c nur lesbar. Der
// Ops-Handler prüft am Ergebnis des Folds gegen den Stand davor
// (schatten.SpiegelVerstoesse) — dasselbe Muster wie die Vertraulich-Sperre
// (vertraulich_sperre.go): So zählt, was am Ende im Board steht, gleich über
// welche Op. Das Protokoll nennt je Verstoß die Ops, die das Objekt
// erreichen; der Browser nimmt sie aus seinem Batch und schickt den Rest
// einmal neu (rest-ops.js). Ganz-PUT und Transfer prüft storage selbst
// (spiegel_schranke.go dort).

import (
	"encoding/json"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// spiegelGrund: Text im Protokoll.
const spiegelGrund = "mirror of the internal board — read-only on the external copy until the return path exists (ADR-0141 §7)"

// opBetrifftLane: Op, die eine Lane nennt (Anlegen/Ändern, Verschieben,
// Löschen, Karte hinein).
func opBetrifftLane(op serverOp, laneID string) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil {
		return false
	}
	for _, k := range []string{"sprintId", "toSprintId"} {
		if zeichenketteVon(p[k]) == laneID {
			return true
		}
	}
	var sprint map[string]json.RawMessage
	return json.Unmarshal(p["sprint"], &sprint) == nil && zeichenketteVon(sprint["id"]) == laneID
}

// opBetrifftSpalten: board.field columnDefs.
func opBetrifftSpalten(op serverOp) bool {
	if op.Type != "board.field" {
		return false
	}
	var p map[string]json.RawMessage
	return json.Unmarshal(op.Payload, &p) == nil && zeichenketteVon(p["field"]) == "columnDefs"
}

// spiegelVerstossProtokoll: Fold-Protokoll-Form wie vertraulichVerstossProtokoll
// — betroffen sind die Ops, die das Objekt nennen; findet sich keine, alle.
func spiegelVerstossProtokoll(verstoesse []schatten.SpiegelOrt, unlesbar bool, ops []serverOp) []map[string]any {
	return spiegelVerstossProtokollGrund(spiegelGrund, verstoesse, unlesbar, ops)
}

// spiegelVerstossProtokollGrund wie spiegelVerstossProtokoll, mit eigenem
// Grund (Rückweg an, wartende Spiegel; Karte #827).
func spiegelVerstossProtokollGrund(grund string, verstoesse []schatten.SpiegelOrt, unlesbar bool, ops []serverOp) []map[string]any {
	if unlesbar {
		verstoesse = append(verstoesse, schatten.SpiegelOrt{})
	}
	var aus []map[string]any
	for _, v := range verstoesse {
		var betroffen []serverOp
		for _, op := range ops {
			switch v.Art {
			case "karte":
				if opBetrifftKarte(op, v.ID) {
					betroffen = append(betroffen, op)
				}
			case "termin":
				if opBetrifftTermin(op, v.ID) {
					betroffen = append(betroffen, op)
				}
			case "lane":
				if opBetrifftLane(op, v.ID) {
					betroffen = append(betroffen, op)
				}
			case "spalte":
				if opBetrifftSpalten(op) {
					betroffen = append(betroffen, op)
				}
			}
		}
		if len(betroffen) == 0 {
			betroffen = ops
		}
		for _, op := range betroffen {
			e := map[string]any{"art": "spiegel-geschuetzt", "op": op.ID, "typ": op.Type, "grund": grund}
			switch v.Art {
			case "karte":
				e["cardId"] = v.ID
			case "termin":
				e["terminId"] = v.ID
			case "lane":
				e["sprintId"] = v.ID
			case "spalte":
				e["columnId"] = v.ID
			}
			aus = append(aus, e)
		}
	}
	return aus
}
