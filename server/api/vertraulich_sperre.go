package api

// vertraulich_sperre.go — Schalter „vertraulich“ darf nur ein Mensch
// abschalten (ADR-0140 §10, Karte #778, Stelle 2).
//
// Setzen darf jeder, auch ein Agent. Abschalten — eine Karte oder ein
// Termin, der vorher vertraulich war, ist es nach dem Batch nicht mehr —
// lehnt der Server für Agenten mit 422 ab. Geprüft wird am Ergebnis des
// Folds gegen den Stand vorher, wie die Anhang-Sperre (Karte #766): So zählt
// nur, was am Ende im Board steht, gleich über welche Op — card.field,
// card.fieldIf, card.upsert, termin.upsert, ein Werkzeug. Die ADR sah dafür
// die JS-Brücke in server/fold vor; der Vergleich in Go braucht für Termine
// keine eigene Vorher-Erfassung (Vorprüfung #778, P-12) und benutzt dasselbe
// Prädikat wie der Ausgang (schatten.Vertraulich).
//
// Gelöschte Karten und Termine sind ausgenommen: Löschen ist kein
// Entschärfen (ADR-0140 §10, Grenze X-6). Ein Batch „true → false → true“
// fällt nicht auf — am Ende ist der Eintrag vertraulich, das genügt.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// vertraulichOrt: Karte oder Termin mit seiner Kennung.
type vertraulichOrt struct {
	art string // "karte" | "termin"
	id  string
}

// vertraulicheEintraege liest, welche Karten und Termine vertraulich sind,
// und welche es überhaupt gibt. ok false: Stand nicht lesbar.
func vertraulicheEintraege(state []byte) (vertraulich map[vertraulichOrt]bool, ok bool) {
	var huelle map[string]json.RawMessage
	if json.Unmarshal(state, &huelle) != nil {
		return nil, false
	}
	var b map[string]json.RawMessage
	if json.Unmarshal(huelle["board"], &b) != nil || b == nil {
		return nil, false
	}
	vertraulich = map[vertraulichOrt]bool{}
	var sprints []map[string]json.RawMessage
	_ = json.Unmarshal(b["sprints"], &sprints)
	for _, s := range sprints {
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(s["cards"], &cards)
		for _, k := range cards {
			if id := zeichenketteVon(k["id"]); id != "" {
				o := vertraulichOrt{"karte", id}
				vertraulich[o] = vertraulich[o] || schatten.Vertraulich(k)
			}
		}
	}
	// Termine wirksam: eigener Schalter, vertrauliche Karte oder fehlende
	// Karte zu karteId (Kritiker #778, K-1) — sonst schaltete ein Agent den
	// Termin ab, indem er karteId entfernt.
	karten := map[string]bool{}
	for o, v := range vertraulich {
		karten[o.id] = v
	}
	var termine []map[string]json.RawMessage
	_ = json.Unmarshal(b["termine"], &termine)
	for _, t := range termine {
		if id := zeichenketteVon(t["id"]); id != "" {
			o := vertraulichOrt{"termin", id}
			vertraulich[o] = vertraulich[o] || schatten.TerminWirksamVertraulich(schatten.Vertraulich(t), zeichenketteVon(t["karteId"]), karten)
		}
	}
	return vertraulich, true
}

// vertraulichAbgeschaltet: Einträge, die vorher vertraulich waren und es
// nach dem Batch nicht mehr sind (und noch existieren). Ist einer der beiden
// Stände nicht lesbar, gilt das als Verstoß (Ausfall in Richtung Nichtstun).
// Vorfilter: Trägt der Stand vorher das Wort nicht, gibt es nichts
// abzuschalten.
func vertraulichAbgeschaltet(vorher, nachher []byte) (verstoesse []vertraulichOrt, unlesbar bool) {
	if !schatten.KannVertraulichSein(vorher) {
		return nil, false
	}
	alt, ok1 := vertraulicheEintraege(vorher)
	neu, ok2 := vertraulicheEintraege(nachher)
	if !ok1 || !ok2 {
		return nil, true
	}
	for o, war := range alt {
		ist, gibtEs := neu[o]
		if war && gibtEs && !ist {
			verstoesse = append(verstoesse, o)
		}
	}
	return verstoesse, false
}

// vertraulichVerstossProtokoll: Fold-Protokoll-Form wie
// anhangVerstossProtokoll — betroffen sind die Ops, die den Eintrag nennen;
// findet sich keine, alle.
func vertraulichVerstossProtokoll(verstoesse []vertraulichOrt, unlesbar bool, ops []serverOp) []map[string]any {
	const grund = "only a human may switch off confidential (ADR-0140 §10)"
	if unlesbar {
		verstoesse = append(verstoesse, vertraulichOrt{})
	}
	var aus []map[string]any
	for _, v := range verstoesse {
		var betroffen []serverOp
		for _, op := range ops {
			if v.art == "karte" && opBetrifftKarte(op, v.id) || v.art == "termin" && opBetrifftTermin(op, v.id) {
				betroffen = append(betroffen, op)
			}
		}
		if len(betroffen) == 0 {
			betroffen = ops
		}
		for _, op := range betroffen {
			e := map[string]any{"art": "vertraulich-abgeschaltet", "op": op.ID, "typ": op.Type, "grund": grund}
			switch v.art {
			case "karte":
				e["cardId"] = v.id
			case "termin":
				e["terminId"] = v.id
			}
			aus = append(aus, e)
		}
	}
	return aus
}

// opBetrifftTermin: termin.upsert/termin.delete dieses Termins.
func opBetrifftTermin(op serverOp, terminID string) bool {
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil {
		return false
	}
	if zeichenketteVon(p["terminId"]) == terminID {
		return true
	}
	var t map[string]json.RawMessage
	return json.Unmarshal(p["termin"], &t) == nil && zeichenketteVon(t["id"]) == terminID
}

// VertraulichBewahrtHeader nennt in der PUT-Antwort die Karten und Termine,
// deren Schalter „vertraulich“ BoardRepo.Update stehen gelassen hat
// (ADR-0140 §10, Stelle 3, Grenze W-4): `<anzahl>; ids=<id>,<id>,…`.
const VertraulichBewahrtHeader = "X-Aboard-Vertraulich-Bewahrt"

// vertraulichBewahrtMaxIDs: höchstens so viele Kennungen im Header, damit er
// unter den Grenzen gängiger Proxys bleibt (X-1, Y-5). Die Anzahl zählt immer
// alle.
const vertraulichBewahrtMaxIDs = 20

// vertraulichBewahrtHeader baut den Header-Wert ("" = keiner). Kennungen
// sind freie Zeichenketten aus der Nutzlast; aufgenommen wird nur, was zu
// [A-Za-z0-9_-]{1,64} passt — ein Komma oder Steuerzeichen bräche das
// Format (Vorprüfung #778, P-4).
func vertraulichBewahrtHeader(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	var gut []string
	for _, id := range ids {
		if len(gut) == vertraulichBewahrtMaxIDs {
			break
		}
		if headerKennung(id) {
			gut = append(gut, id)
		}
	}
	return fmt.Sprintf("%d; ids=%s", len(ids), strings.Join(gut, ","))
}

func headerKennung(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
