package storage

// finding_pruefung.go — Finding-Karten beim Ganz-Board-Schreiben prüfen
// (F-1572, Karte #680, ADR-0133 §1).
//
// Ops prüft der Fold (src/ops.js, src/finding.js); der PUT eines ganzen
// Browserstands und der Import tragen state_json dagegen opak ein (ADR-0044).
// Damit galten „keine Doppel“ und „geschlossener Wortschatz“ nur für Ops.
// Hier wird der Teil geprüft, der sich ohne den Fold prüfen lässt: doppelte
// `nr`, Wortschatz, `kind: finding` ohne `finding`, fremde Schlüssel.
//
// Der Wortschatz steht in src/finding.js und ist hier als Liste nachgeführt;
// finding_pruefung_test.go liest die JS-Quelle und schlägt an, sobald beide
// auseinanderlaufen.
//
// Alte Stände: Ein Board, das schon vor dem Schreiben Verstöße trägt, soll
// nicht zur Falle werden (jedes Speichern wäre dann abgelehnt, ohne Weg
// zurück). Deshalb zählt für den PUT und den Import auf ein bestehendes Board
// nur, was NEU hinzukommt — NeueFindingVerstoesse.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Wortschatz der Finding-Karte — Spiegel von src/finding.js.
var (
	findingTypen        = []string{"S", "K", "B", "V", "D"}
	findingPrioritaeten = []string{"P0", "P1", "P2", "P3"}
	findingStatus       = []string{
		"offen", "sofort", "eingeplant", "backlog", "teilweise behoben",
		"behoben", "akzeptiert", "behandelt", "aufgelöst", "hinfällig",
	}
	findingFelder = []string{"nr", "typ", "prioritaet", "beleg", "lehre", "status"}
)

// pruefKarte ist eine Karte mit den Feldern, die für die Prüfung zählen.
type pruefKarte struct {
	id   string
	roh  map[string]any
	kind string
}

// pruefKartenListe liest alle Karten eines Ganz-Board-Zustands in Reihenfolge;
// ok=false bei nicht lesbarem Zustand.
func pruefKartenListe(stateJSON []byte) ([]pruefKarte, bool) {
	var state struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return nil, false
	}
	var out []pruefKarte
	for _, sp := range state.Board.Sprints {
		for _, card := range sp.Cards {
			kind, _ := card["kind"].(string)
			out = append(out, pruefKarte{id: fmt.Sprint(card["id"]), roh: card, kind: kind})
		}
	}
	return out, true
}

// kartenVerstoesse prüft eine einzelne Karte (ohne Nummern-Doppelung, die
// braucht den ganzen Zustand) und liefert ihre gültige Nummer, falls sie eine
// hat. Eine normale Karte mit `finding`-Objekt ist ein Verstoß (Fold:
// finding-ohne-kind).
func kartenVerstoesse(k pruefKarte) (v []string, nr int, hatNr bool) {
	id := k.id
	f, ok := k.roh["finding"].(map[string]any)
	if k.kind != "finding" {
		if k.roh["finding"] != nil {
			v = append(v, fmt.Sprintf("card %s: finding object on a card without kind \"finding\"", id))
		}
		return v, 0, false
	}
	if !ok {
		return append(v, fmt.Sprintf("card %s: kind \"finding\" without a finding object", id)), 0, false
	}
	for key := range f {
		if !slices.Contains(findingFelder, key) {
			v = append(v, fmt.Sprintf("card %s: finding.%s is not a known field", id, key))
		}
	}
	for _, w := range []struct {
		feld    string
		erlaubt []string
	}{{"typ", findingTypen}, {"prioritaet", findingPrioritaeten}, {"status", findingStatus}} {
		s, _ := f[w.feld].(string)
		if !slices.Contains(w.erlaubt, s) {
			v = append(v, fmt.Sprintf("card %s: finding.%s is %s, allowed: %s",
				id, w.feld, kurz(f[w.feld]), strings.Join(w.erlaubt, ", ")))
		}
	}
	for _, feld := range []string{"beleg", "lehre"} {
		if x := f[feld]; x != nil {
			if _, ok := x.(string); !ok {
				v = append(v, fmt.Sprintf("card %s: finding.%s must be text", id, feld))
			}
		}
	}
	if n, ok := ganzzahl(f["nr"]); ok {
		return v, n, true
	}
	return append(v, fmt.Sprintf("card %s: finding.nr is missing or not a positive integer", id)), 0, false
}

func doppelteNummern(nummern map[int]int) []int {
	nrs := make([]int, 0, len(nummern))
	for nr, n := range nummern {
		if n > 1 {
			nrs = append(nrs, nr)
		}
	}
	slices.Sort(nrs)
	return nrs
}

// FindingVerstoesse liefert alle Verstöße gegen die Finding-Regeln in einem
// Ganz-Board-Zustand; leer = in Ordnung. Ein nicht lesbarer Zustand liefert
// nichts — ihn abzulehnen ist nicht Sache dieser Prüfung.
func FindingVerstoesse(stateJSON []byte) []string {
	karten, ok := pruefKartenListe(stateJSON)
	if !ok {
		return nil
	}
	var v []string
	nummern := map[int]int{} // nr → Anzahl Karten
	for _, k := range karten {
		kv, nr, hat := kartenVerstoesse(k)
		v = append(v, kv...)
		if hat {
			nummern[nr]++
		}
	}
	for _, nr := range doppelteNummern(nummern) {
		v = append(v, fmt.Sprintf("finding.nr %s is used on more than one card", strconv.Itoa(nr)))
	}
	return v
}

func kurz(x any) string {
	b, _ := json.Marshal(x)
	if len(b) > 40 {
		return string(b[:40]) + "…"
	}
	return string(b)
}

// NeueFindingVerstoesse liefert die Verstöße von neuJSON, die altJSON (der
// bisher gespeicherte Stand; nil bei einem neuen Board) nicht schon trug.
//
// Verglichen wird je Karten-id, nicht über Meldungstexte (KA-3): Neu ist, was
// eine Karte betrifft, die im Altstand fehlt oder dort in `kind`/`finding`
// anders war. Zusätzlich gemeldet wie im Fold (src/ops.js, findingVerwerfung):
// Nummernwechsel einer bestehenden Karte (finding-nr-gesperrt) und Umwidmung
// (finding-umwidmung). Eine Nummer, die mehr als eine Karte trägt, zählt als
// neu, sobald eine dieser Karten die Nummer NEU trägt — als neue Karte, nach
// einem Nummernwechsel oder einer Umwidmung —, auch wenn der Altstand dieselbe
// Nummer schon doppelt trug. Eine sonstige Änderung an einer Karte mit
// Altlast-Doppelnummer (etwa nur `status`) zählt nicht (Verify VO-1): Der Fold
// lässt sie über Ops durch, und der PUT „Meine Version behalten“ darf nicht
// strenger sein als derselbe Weg über Ops.
func NeueFindingVerstoesse(altJSON, neuJSON []byte) []string {
	if altJSON == nil {
		return FindingVerstoesse(neuJSON)
	}
	neuK, ok := pruefKartenListe(neuJSON)
	if !ok {
		return nil
	}
	altK, ok := pruefKartenListe(altJSON)
	if !ok {
		return FindingVerstoesse(neuJSON)
	}
	alt := map[string]pruefKarte{}
	for _, k := range altK {
		if _, da := alt[k.id]; !da {
			alt[k.id] = k
		}
	}
	var v []string
	nummern := map[int][]bool{} // nr → je Karte: trug sie diese Nummer schon im Altstand?
	for _, k := range neuK {
		a, bestand := alt[k.id]
		unveraendert := bestand && a.kind == k.kind && reflect.DeepEqual(a.roh["finding"], k.roh["finding"])
		kv, nr, hat := kartenVerstoesse(k)
		if hat {
			nummern[nr] = append(nummern[nr], bestand && a.kind == "finding" && altNummer(a) == nr)
		}
		if unveraendert {
			continue
		}
		v = append(v, kv...)
		if !bestand {
			continue
		}
		if (a.kind == "finding") != (k.kind == "finding") {
			v = append(v, fmt.Sprintf("card %s: kind changed from %q to %q (a finding stays one, a normal card does not become one)",
				k.id, a.kind, k.kind))
			continue
		}
		if k.kind == "finding" {
			af, _ := a.roh["finding"].(map[string]any)
			nf, _ := k.roh["finding"].(map[string]any)
			if af != nil && nf != nil && !reflect.DeepEqual(af["nr"], nf["nr"]) {
				v = append(v, fmt.Sprintf("card %s: finding.nr must not change (was %s, is %s)",
					k.id, kurz(af["nr"]), kurz(nf["nr"])))
			}
		}
	}
	zaehl := map[int]int{}
	for nr, liste := range nummern {
		zaehl[nr] = len(liste)
	}
	for _, nr := range doppelteNummern(zaehl) {
		if slices.Contains(nummern[nr], false) {
			v = append(v, fmt.Sprintf("finding.nr %s is used on more than one card", strconv.Itoa(nr)))
		}
	}
	return v
}

// altNummer liefert die gültige Nummer einer Finding-Karte im Altstand, sonst 0.
func altNummer(k pruefKarte) int {
	f, _ := k.roh["finding"].(map[string]any)
	if n, ok := ganzzahl(f["nr"]); ok {
		return n
	}
	return 0
}

// FindingVerstossMeldung fasst Verstöße für eine Fehlermeldung zusammen
// (höchstens fünf, dann die Restzahl).
func FindingVerstossMeldung(v []string) string {
	const max = 5
	t := v
	if len(t) > max {
		t = t[:max]
	}
	s := "board contains invalid finding cards; nothing written: " + strings.Join(t, "; ")
	if len(v) > max {
		s += fmt.Sprintf("; … and %d more", len(v)-max)
	}
	return s
}
