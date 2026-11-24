package api

// board_scan.go — ein Durchlauf über ein Board-JSON bis zu den Feldern jeder
// Karte (F-1628).
//
// Die Prüfungen am Ergebnis (Kommentar-Löschrecht, Verlaufsschutz) brauchen
// je Karte nur wenige Felder als Rohbytes. Verschachteltes json.Unmarshal in
// map[string]json.RawMessage liest das Dokument dafür je Ebene zweimal (gut
// zehnmal bei fünf Ebenen) — bei 7 MB über 300 ms je Stand, unter der
// Schreibsperre und für jeden Batch. Dieser Leser läuft einmal durch und merkt
// sich nur Bereiche.
//
// Schlüssel liest er EXAKT, wie der Fold und der Browser (JSON.parse): keine
// Gross-/Kleinschreibung-Toleranz (Fehlerklasse 122), Escapes in Schlüsseln
// werden aufgelöst, bei doppelten Schlüsseln gilt der letzte. Was er nicht
// versteht, ist „nicht lesbar“ — die Aufrufer lehnen dann ab (fail-closed).

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// scanTiefeMax begrenzt die Verschachtelung wie encoding/json (10000) — sonst
// liesse ein Dokument aus lauter „[“ den Stack wachsen.
const scanTiefeMax = 10000

type boardScanner struct {
	b []byte
}

func (s *boardScanner) ws(i int) int {
	for i < len(s.b) {
		switch s.b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// zeichenkette: i steht auf '"'; liefert das Ende (hinter dem schliessenden
// '"') und ob ein Escape vorkam.
func (s *boardScanner) zeichenkette(i int) (int, bool, bool) {
	if i >= len(s.b) || s.b[i] != '"' {
		return 0, false, false
	}
	escape := false
	for j := i + 1; j < len(s.b); j++ {
		switch c := s.b[j]; {
		case c == '"':
			return j + 1, escape, true
		case c == '\\':
			escape = true
			if j+1 >= len(s.b) {
				return 0, false, false
			}
			switch s.b[j+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				j++
			case 'u':
				if j+5 >= len(s.b) {
					return 0, false, false
				}
				for _, h := range s.b[j+2 : j+6] {
					if !istHex(h) {
						return 0, false, false
					}
				}
				j += 5
			default:
				return 0, false, false
			}
		case c < 0x20:
			return 0, false, false
		}
	}
	return 0, false, false
}

// schluessel liest einen Objektschlüssel, Escapes aufgelöst.
func (s *boardScanner) schluessel(i int) (string, int, bool) {
	ende, escape, ok := s.zeichenkette(i)
	if !ok {
		return "", 0, false
	}
	// Ohne Escape und mit gültigem UTF-8 direkt; sonst wie encoding/json
	// (ungültige Bytes werden U+FFFD — wie im Fold, der UTF-8 zu einem
	// JS-String dekodiert).
	if roh := s.b[i+1 : ende-1]; !escape && utf8.Valid(roh) {
		return string(roh), ende, true
	}
	var k string
	if json.Unmarshal(s.b[i:ende], &k) != nil {
		return "", 0, false
	}
	return k, ende, true
}

// wert überspringt einen JSON-Wert ab i (ohne führenden Leerraum) und liefert
// sein Ende.
func (s *boardScanner) wert(i, tiefe int) (int, bool) {
	if i >= len(s.b) || tiefe > scanTiefeMax {
		return 0, false
	}
	switch c := s.b[i]; {
	case c == '{':
		return s.objekt(i, tiefe+1, nil)
	case c == '[':
		return s.feld(i, tiefe+1, nil)
	case c == '"':
		ende, _, ok := s.zeichenkette(i)
		return ende, ok
	case c == 't':
		return s.literal(i, "true")
	case c == 'f':
		return s.literal(i, "false")
	case c == 'n':
		return s.literal(i, "null")
	case c == '-' || (c >= '0' && c <= '9'):
		return s.zahl(i)
	}
	return 0, false
}

// zahl: -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)? — wie encoding/json.
func (s *boardScanner) zahl(i int) (int, bool) {
	ziffern := func(j int) int {
		for j < len(s.b) && s.b[j] >= '0' && s.b[j] <= '9' {
			j++
		}
		return j
	}
	if s.b[i] == '-' {
		i++
	}
	switch {
	case i < len(s.b) && s.b[i] == '0':
		i++
	case i < len(s.b) && s.b[i] >= '1' && s.b[i] <= '9':
		i = ziffern(i)
	default:
		return 0, false
	}
	if i < len(s.b) && s.b[i] == '.' {
		j := ziffern(i + 1)
		if j == i+1 {
			return 0, false
		}
		i = j
	}
	if i < len(s.b) && (s.b[i] == 'e' || s.b[i] == 'E') {
		i++
		if i < len(s.b) && (s.b[i] == '+' || s.b[i] == '-') {
			i++
		}
		j := ziffern(i)
		if j == i {
			return 0, false
		}
		i = j
	}
	return i, true
}

func istHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (s *boardScanner) literal(i int, w string) (int, bool) {
	if bytes.HasPrefix(s.b[i:], []byte(w)) {
		return i + len(w), true
	}
	return 0, false
}

// objekt läuft über die Mitglieder eines Objekts ab '{'. fn (falls gesetzt)
// bekommt den Schlüssel und den Anfang des Werts und VERBRAUCHT den Wert
// selbst (liefert sein Ende) — so liest jede Ebene ihren Teilbaum genau
// einmal. fn=nil überspringt.
func (s *boardScanner) objekt(i, tiefe int, fn func(k string, von int) (int, bool)) (int, bool) {
	if tiefe > scanTiefeMax || i >= len(s.b) || s.b[i] != '{' {
		return 0, false
	}
	i = s.ws(i + 1)
	if i < len(s.b) && s.b[i] == '}' {
		return i + 1, true
	}
	for {
		k, j, ok := s.schluessel(i)
		if !ok {
			return 0, false
		}
		j = s.ws(j)
		if j >= len(s.b) || s.b[j] != ':' {
			return 0, false
		}
		von := s.ws(j + 1)
		if von >= len(s.b) {
			return 0, false // abgeschnitten — fn darf s.b[von] lesen
		}
		var bis int
		if fn != nil {
			bis, ok = fn(k, von)
		} else {
			bis, ok = s.wert(von, tiefe)
		}
		if !ok {
			return 0, false
		}
		i = s.ws(bis)
		if i >= len(s.b) {
			return 0, false
		}
		switch s.b[i] {
		case ',':
			i = s.ws(i + 1)
		case '}':
			return i + 1, true
		default:
			return 0, false
		}
	}
}

// feld läuft über die Elemente eines Arrays ab '['; fn verbraucht jedes
// Element selbst (wie bei objekt).
func (s *boardScanner) feld(i, tiefe int, fn func(von int) (int, bool)) (int, bool) {
	if tiefe > scanTiefeMax || i >= len(s.b) || s.b[i] != '[' {
		return 0, false
	}
	i = s.ws(i + 1)
	if i < len(s.b) && s.b[i] == ']' {
		return i + 1, true
	}
	for {
		if i >= len(s.b) {
			return 0, false // abgeschnitten — fn darf s.b[i] lesen
		}
		var bis int
		var ok bool
		if fn != nil {
			bis, ok = fn(i)
		} else {
			bis, ok = s.wert(i, tiefe)
		}
		if !ok {
			return 0, false
		}
		i = s.ws(bis)
		if i >= len(s.b) {
			return 0, false
		}
		switch s.b[i] {
		case ',':
			i = s.ws(i + 1)
		case ']':
			return i + 1, true
		default:
			return 0, false
		}
	}
}

func (s *boardScanner) istNull(von int) bool {
	return bytes.HasPrefix(s.b[von:], []byte("null"))
}

// Bei den folgenden Funktionen heisst ok=false: kein gültiges JSON (Abbruch).
// struktur=false: gültig, aber nicht die erwartete Form — das zählt nur, wenn
// es das LETZTE Vorkommen eines Schlüssels ist (doppelte Schlüssel: der letzte
// gilt, ein früherer muss nur gültiges JSON sein — wie encoding/json und
// JSON.parse).

// kartenListe: Karten eines Arrays ab '[' (je Karte Schlüssel → Rohbytes).
func (s *boardScanner) kartenListe(von int) (karten []map[string][]byte, struktur bool, ende int, ok bool) {
	struktur = true
	ende, ok = s.feld(von, 3, func(v int) (int, bool) {
		if s.b[v] != '{' {
			struktur = false // Karte kein Objekt (auch null)
			return s.wert(v, 3)
		}
		roh := map[string][]byte{}
		e, ok := s.objekt(v, 4, func(k string, w int) (int, bool) {
			b, ok := s.wert(w, 4)
			if ok {
				roh[k] = s.b[w:b]
			}
			return b, ok
		})
		karten = append(karten, roh)
		return e, ok
	})
	return karten, struktur, ende, ok
}

// sprintListe: alle Karten der Sprints eines Arrays ab '['.
func (s *boardScanner) sprintListe(von int) (alle []map[string][]byte, struktur bool, ende int, ok bool) {
	struktur = true
	ende, ok = s.feld(von, 2, func(v int) (int, bool) {
		if s.b[v] != '{' {
			struktur = false // Sprint kein Objekt (auch null)
			return s.wert(v, 2)
		}
		var karten []map[string][]byte
		kartenOK := true
		e, ok := s.objekt(v, 3, func(k string, w int) (int, bool) {
			if k != "cards" {
				return s.wert(w, 3)
			}
			karten, kartenOK = nil, true
			switch {
			case s.istNull(w):
				return s.wert(w, 3)
			case s.b[w] != '[':
				kartenOK = false
				return s.wert(w, 3)
			}
			liste, st, b, ok := s.kartenListe(w)
			karten, kartenOK = liste, st
			return b, ok
		})
		if !kartenOK {
			struktur = false
		}
		alle = append(alle, karten...)
		return e, ok
	})
	return alle, struktur, ende, ok
}

// durchlaufeKarten ruft fn für jede Karte des Boards mit ihren Feldern
// (Schlüssel → Rohbytes, exakt), in Dokumentreihenfolge. ok=false, wenn das
// Dokument kein gültiges JSON ist oder die Struktur bis zu den Karten nicht
// stimmt: Wurzel oder board kein Objekt bzw. board fehlt, sprints/cards
// weder fehlend noch null noch Array, ein Sprint oder eine Karte kein Objekt
// — wie leseKommentarBestand vor F-1628. Fehlende sprints: leeres Board
// (F-1152). Bei ok=false wird fn nie gerufen.
func durchlaufeKarten(state []byte, fn func(felder map[string][]byte)) bool {
	s := &boardScanner{b: state}
	start := s.ws(0)
	if start >= len(state) || state[start] != '{' {
		return false
	}
	var karten []map[string][]byte
	boardDa, boardOK := false, false
	ende, ok := s.objekt(start, 1, func(k string, v int) (int, bool) {
		if k != "board" {
			return s.wert(v, 1)
		}
		boardDa, boardOK, karten = true, false, nil
		if state[v] != '{' {
			return s.wert(v, 1)
		}
		sprintsOK := true
		e, ok := s.objekt(v, 2, func(k string, w int) (int, bool) {
			if k != "sprints" {
				return s.wert(w, 2)
			}
			karten, sprintsOK = nil, true
			switch {
			case s.istNull(w):
				return s.wert(w, 2)
			case state[w] != '[':
				sprintsOK = false
				return s.wert(w, 2)
			}
			liste, st, b, ok := s.sprintListe(w)
			karten, sprintsOK = liste, st
			return b, ok
		})
		boardOK = sprintsOK
		return e, ok
	})
	if !ok || !boardDa || !boardOK || s.ws(ende) != len(state) {
		return false
	}
	for _, k := range karten {
		fn(k)
	}
	return true
}
