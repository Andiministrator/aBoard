package schatten

// checkliste.go — Checklisten-Punkte einer Beschreibung in der engen Fassung
// des Board-Paar-Rückwegs (ADR-0141 „Umsetzung Schritt 2b“ Punkt 1, Karte
// #827, D8 des Etappenplans). Dieselbe Definition steht in JS
// (checklistePunkte in src/ops.js, für card.checkItemIf); beide prüfen die
// Vektordatei testdata/checkliste-827.json.
//
// Ein Punkt ist eine Zeile `- [ ] Text` / `- [x] Text` / `- [X] Text` —
// genau ein Bindestrich am Zeilenanfang, ohne Einrückung —, die nicht in
// einem Code-Zaun (``` oder ~~~, bis zu drei Leerzeichen eingerückt) steht.
// Ein `\r` am Zeilenende zählt nicht zum Text. Vorkommen zählt ab 1
// gleichlautende Punkte davor.

import (
	"regexp"
	"strings"
)

// ChecklistePunkt ist ein Punkt der Checkliste.
type ChecklistePunkt struct {
	Text      string `json:"text"`
	Vorkommen int    `json:"vorkommen"`
	Erledigt  bool   `json:"erledigt"`
	Zeile     int    `json:"zeile"`
}

var (
	zaunMuster  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	punktMuster = regexp.MustCompile(`^- \[( |x|X)\] ([^\n]*)$`) // wie in JS (Kritik A-3)
)

// ChecklistePunkte liefert die Punkte einer Beschreibung (nie nil).
func ChecklistePunkte(beschreibung string) []ChecklistePunkt {
	punkte := []ChecklistePunkt{}
	zaun := byte(0)
	gezaehlt := map[string]int{}
	for i, zeile := range strings.Split(beschreibung, "\n") {
		zeile = strings.TrimSuffix(zeile, "\r")
		if z := zaunMuster.FindStringSubmatch(zeile); z != nil {
			switch {
			case zaun == 0:
				zaun = z[1][0]
			case z[1][0] == zaun:
				zaun = 0
			}
			continue
		}
		if zaun != 0 {
			continue
		}
		m := punktMuster.FindStringSubmatch(zeile)
		if m == nil {
			continue
		}
		gezaehlt[m[2]]++
		punkte = append(punkte, ChecklistePunkt{Text: m[2], Vorkommen: gezaehlt[m[2]], Erledigt: m[1] != " ", Zeile: i})
	}
	return punkte
}

// NurHaekchenGeaendert: Unterscheiden sich zwei Beschreibungen ausschließlich
// in Häkchen von Checklisten-Punkten (sonst zeichengleich, gleiche Punkte in
// gleicher Reihenfolge)? Dann liefert sie die geänderten Punkte in ihrer
// Fassung nachher; sonst ok = false (die Änderung gilt dann für die ganze
// Beschreibung).
func NurHaekchenGeaendert(vorher, nachher string) (geaendert []ChecklistePunkt, ok bool) {
	if vorher == nachher {
		return nil, false
	}
	alt, neu := ChecklistePunkte(vorher), ChecklistePunkte(nachher)
	if len(alt) != len(neu) {
		return nil, false
	}
	za, zn := strings.Split(vorher, "\n"), strings.Split(nachher, "\n")
	if len(za) != len(zn) {
		return nil, false
	}
	punktZeile := map[int]bool{}
	for j, p := range alt {
		q := neu[j]
		if p.Zeile != q.Zeile || p.Text != q.Text || p.Vorkommen != q.Vorkommen {
			return nil, false
		}
		punktZeile[p.Zeile] = true
		if p.Erledigt != q.Erledigt {
			geaendert = append(geaendert, q)
		}
	}
	for i := range za {
		if !punktZeile[i] {
			if za[i] != zn[i] {
				return nil, false
			}
			continue
		}
		// Gleicher Punkt: nur das Häkchen (Zeichen 3) darf abweichen.
		if za[i][:3] != zn[i][:3] || za[i][4:] != zn[i][4:] {
			return nil, false
		}
	}
	return geaendert, len(geaendert) > 0
}
