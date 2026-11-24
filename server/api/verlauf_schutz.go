package api

// verlauf_schutz.go — der Verlauf einer Karte (`activity`) ist ein Protokoll,
// kein Freitext (F-1621).
//
// Bis Sprint 82 konnte jeder mit Schreibrecht den Verlauf per card.field,
// card.upsert oder Ganz-Board-PUT ersetzen: den Eintrag
// „comment-deleted-by-owner“ entfernen, der dem Autor nachweist, dass der
// Owner seinen Kommentar gelöscht hat (ADR-0136), oder einen „llm-sent“-
// Eintrag erfinden. Jetzt gilt am ERGEBNIS (wie beim Kommentar-Löschrecht,
// Fehlerklasse 121):
//
//   - Kein bestehender Eintrag verschwindet oder ändert sich, solange es die
//     Karte gibt — für niemanden, auch nicht für Owner oder Admin: Der Eintrag
//     ist gerade der Nachweis gegen den Owner. Eine Karte zu löschen bleibt
//     Schreibrecht.
//   - Einträge mit einer Aktion, die nur der Server schreibt
//     (serverVerlaufAktionen), kann kein Mensch und kein Agent neu anlegen.
//     Das gilt auch für Owner und Admin beim Ganz-Board-PUT einer Karte mit
//     neuer id (Kritiker KB-3): Ein Import, der Karten samt „llm-sent“ an
//     ein bestehendes Board hängt, wird abgelehnt — sonst wäre genau das der
//     Weg, einen Nachweis zu erfinden. Ein Board als Ganzes neu anlegen
//     (POST) prüft das nicht.
//
// Damit ein Browser mit veraltetem Verlauf (der Server hat inzwischen etwas
// angehängt) beim nächsten Speichern nicht abgelehnt wird, FÜHRT der Server
// den Verlauf vor dem Fold ZUSAMMEN: bestehende Einträge in ihrer Reihenfolge,
// dahinter, was der Schreiber neu mitbringt. Ein `card.field activity []`
// leert den Verlauf deshalb nicht — es ändert nichts. Verbindlich bleibt die
// Prüfung am Ergebnis; das Zusammenführen ist Komfort.

import (
	"bytes"
	"encoding/json"
)

// serverVerlaufAktionen: Verlaufseinträge, die nur der Server schreibt — unter
// `action` (llm/cardops.go, kommentarVerlauf, Vorschlags-Zugang) oder `type`
// (storage/transfer.go).
var serverVerlaufAktionen = map[string]bool{
	"llm-sent":                 true,
	"llm-response-discarded":   true,
	"comment-deleted-by-owner": true,
	"vorschlag":                true,
	"board-transferred":        true,
	// Karte #686 (ADR-0134 §3.7): Protokoll der Ausgangsschleuse.
	"schleuse-gesendet":            true,
	"schleuse-beantwortet":         true,
	"schleuse-fehlgeschlagen":      true,
	"schleuse-kommentar-geloescht": true,
}

type verlaufEintrag struct {
	kanon  string
	roh    json.RawMessage
	aktion string // server-eigene Aktion, sonst leer
}

// verlaufRoh: der Verlauf einer Karte als Rohbytes (alle activity-Werte ohne
// null; bei doppelter Karten-id mehrere), geparst erst bei Bedarf.
type verlaufRoh struct {
	roh     []byte
	listen  [][]byte
	geparst bool
	kaputt  bool
	eintr   []verlaufEintrag
}

func (v *verlaufRoh) parse() {
	if v.geparst {
		return
	}
	v.geparst = true
	for _, roh := range v.listen {
		eintr, ok := parseVerlauf(roh)
		if !ok {
			v.kaputt = true
			continue
		}
		v.eintr = append(v.eintr, eintr...)
	}
}

// parseVerlauf liest eine activity-Liste. ok=false: kein Array, oder ein
// Eintrag ist kein Objekt.
func parseVerlauf(roh []byte) ([]verlaufEintrag, bool) {
	var liste []json.RawMessage
	if json.Unmarshal(roh, &liste) != nil {
		return nil, false
	}
	aus := make([]verlaufEintrag, 0, len(liste))
	for _, eRoh := range liste {
		var e map[string]json.RawMessage
		if json.Unmarshal(eRoh, &e) != nil || e == nil {
			return nil, false
		}
		k, ok := kanonisch(eRoh)
		if !ok {
			return nil, false
		}
		ve := verlaufEintrag{kanon: k, roh: eRoh}
		for _, feld := range []string{"action", "type"} {
			var a string
			if json.Unmarshal(e[feld], &a) == nil && serverVerlaufAktionen[a] {
				ve.aktion = a
			}
		}
		aus = append(aus, ve)
	}
	return aus, true
}

// pruefeVerlauf: die Regeln oben für eine Karte, die es danach gibt. ka ist
// bei einer neuen Karte leer.
func pruefeVerlauf(cardID string, ka, kn *karteBestand, s kommentarSchreiber, erlaubt map[string]bool) []kommentarVerstoss {
	if bytes.Equal(ka.verlauf.roh, kn.verlauf.roh) {
		return nil
	}
	ka.verlauf.parse()
	kn.verlauf.parse()
	if kn.verlauf.kaputt {
		return []kommentarVerstoss{{art: "stand-unlesbar", cardID: cardID}}
	}
	// Als MENGE verglichen (Kritiker KB-2): Jeder bestehende Eintrag muss
	// danach mindestens einmal stehen, und neu ist nur, was es an der Karte
	// noch gar nicht gab. Eine Kopie eines echten Eintrags ist keine
	// Fälschung — sonst sperrte eine vorbestehend doppelte Karten-id (zwei
	// Kopien, beide bekommen den zusammengeführten Verlauf) die Karte. Ein
	// kaputter Bestand vorher schützt nichts, was jemand lesen könnte — ihn
	// darf jeder durch einen lesbaren ersetzen.
	danach := map[string]bool{}
	for _, e := range kn.verlauf.eintr {
		danach[e.kanon] = true
	}
	vorher := map[string]bool{}
	var aus []kommentarVerstoss
	if !ka.verlauf.kaputt {
		for _, e := range ka.verlauf.eintr {
			vorher[e.kanon] = true
			if !danach[e.kanon] {
				aus = append(aus, kommentarVerstoss{art: "verlauf-geaendert", cardID: cardID})
				break
			}
		}
	}
	// Neue Einträge: keine server-eigene Aktion.
	for _, e := range kn.verlauf.eintr {
		if vorher[e.kanon] {
			continue
		}
		if e.aktion != "" && !erlaubt[e.aktion] {
			aus = append(aus, kommentarVerstoss{art: "verlauf-gefaelscht", cardID: cardID})
			break
		}
	}
	return aus
}

// verlaufZusammen: bestehender Verlauf der Karte (Reihenfolge des Stands),
// dahinter die Einträge aus `neu`, die es dort noch nicht gibt. ok=false:
// nichts zusammenzuführen (Karte neu, Bestand oder `neu` nicht lesbar) — dann
// geht `neu` unverändert in den Fold, und die Prüfung entscheidet.
func verlaufZusammen(bestand *kommentarBestand, cardID string, neu json.RawMessage) (json.RawMessage, bool) {
	kb := bestand.karten[cardID]
	if kb == nil {
		return nil, false
	}
	kb.verlauf.parse()
	if kb.verlauf.kaputt {
		return nil, false
	}
	eintr, ok := parseVerlauf(neu)
	if !ok {
		return nil, false
	}
	// Als Menge (KB-2): Jeder Eintrag einmal, in der Reihenfolge des Stands.
	// Bei einer doppelten Karten-id stehen die Einträge beider Kopien im
	// Bestand; ohne Mengenbildung wüchse der Verlauf mit jedem PUT.
	vorhanden := map[string]bool{}
	liste := make([]json.RawMessage, 0, len(kb.verlauf.eintr)+len(eintr))
	for _, e := range append(append([]verlaufEintrag(nil), kb.verlauf.eintr...), eintr...) {
		if !vorhanden[e.kanon] {
			vorhanden[e.kanon] = true
			liste = append(liste, e.roh)
		}
	}
	aus, err := json.Marshal(liste)
	return aus, err == nil
}

// opsSetzenFeld: Setzt eine Op das Kartenfeld `feld` als Ganzes —
// card.upsert mit dem Schlüssel in `card`, card.field/card.fieldIf auf
// `feld`? Exakte Schlüssel, wie der Fold.
func opsSetzenFeld(ops []serverOp, feld string) bool {
	for _, op := range ops {
		if _, _, ok := opFeldWert(op, feld); ok {
			return true
		}
	}
	return false
}

// opFeldWert liefert Karten-id und Wert, wenn die Op `feld` setzt.
func opFeldWert(op serverOp, feld string) (string, json.RawMessage, bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil || p == nil {
		return "", nil, false
	}
	var cardID string
	switch op.Type {
	case "card.upsert":
		var karte map[string]json.RawMessage
		if json.Unmarshal(p["card"], &karte) != nil || karte == nil {
			return "", nil, false
		}
		roh, mit := karte[feld]
		_ = json.Unmarshal(karte["id"], &cardID)
		return cardID, roh, mit
	case "card.field", "card.fieldIf":
		var f string
		if json.Unmarshal(p["field"], &f) != nil || f != feld {
			return "", nil, false
		}
		roh, mit := p["value"]
		_ = json.Unmarshal(p["cardId"], &cardID)
		return cardID, roh, mit
	}
	return "", nil, false
}

// fuehreVerlaufZusammen: Ops-Weg — jede Op, die `activity` setzt, bekommt den
// zusammengeführten Verlauf (siehe oben). Für alle Schreiber, auch Agenten.
func fuehreVerlaufZusammen(bestand *kommentarBestand, okBestand bool, ops []serverOp) []serverOp {
	if !okBestand || !opsSetzenFeld(ops, "activity") {
		return ops
	}
	aus := make([]serverOp, 0, len(ops))
	for _, op := range ops {
		if cardID, roh, ok := opFeldWert(op, "activity"); ok {
			if zus, ok := verlaufZusammen(bestand, cardID, roh); ok {
				op.Payload = setzeOpFeld(op, "activity", zus)
			}
		}
		aus = append(aus, op)
	}
	return aus
}

// setzeOpFeld ersetzt den Wert, den opFeldWert gefunden hat.
func setzeOpFeld(op serverOp, feld string, wert json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(op.Payload, &p) != nil || p == nil {
		return op.Payload
	}
	if op.Type == "card.upsert" {
		var karte map[string]json.RawMessage
		if json.Unmarshal(p["card"], &karte) != nil || karte == nil {
			return op.Payload
		}
		karte[feld] = wert
		p["card"], _ = json.Marshal(karte)
	} else {
		p["value"] = wert
	}
	neu, err := json.Marshal(p)
	if err != nil {
		return op.Payload
	}
	return neu
}
