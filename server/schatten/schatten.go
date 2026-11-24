// Package schatten baut die Schatten eines internen Boards (ADR-0140 §2,
// Karte #778) und hält die Regeln, die Ausgang innen und Eingang außen
// (#779) teilen: den festen Feldsatz, die zweite Titel-Schicht, die
// Wertemengen für Spalte, Priorität und Bearbeiter und das Prädikat
// „vertraulich“.
//
// Ein Schatten ist KEINE Kopie einer Karte. Er wird aus einer festen
// Feldliste neu gebaut (Vertrag Push-Skript, G-3), nichts wird durchgereicht: keine
// Beschreibung, keine Kommentare, keine Anhänge, keine Labels, kein Verlauf,
// keine E-Mail-Adresse.
//
// Gelesen wird über exakte Map-Schlüssel (wie leseWkBoard und leseTermine):
// Ein Struct-Unmarshal nähme auch „TITLE“, und dann ginge ein anderer Titel
// hinaus, als der Browser zeigt.
package schatten

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/calendar"
	"golang.org/x/text/unicode/norm"
)

// Felder ist der Feldsatz eines Schattens, in der Reihenfolge der Antwort.
// Ausgang, Eingang und Tests teilen genau diese Liste; ein Test prüft, dass
// Eintrag keinen anderen JSON-Schlüssel trägt.
var Felder = []string{
	"id", "art", "nr", "titel", "vertraulich", "spalte", "prioritaet",
	"startDatum", "startZeit", "endeDatum", "endeZeit", "bearbeiter", "vonAgent",
}

// Arten eines Schattens.
const (
	ArtKarte  = "karte"
	ArtTermin = "termin"
)

// Ersatztext für einen Titel, der nach verpackten Daten aussieht (Vertrag
// Push-Skript, Ziffer 9.2 und 3a). Der Mensch sieht den echten Titel innen.
const Ersatztext = "Interne Karte (Titel im Board)"

// Grenzen der Textbehandlung (ADR-0140 §2).
const (
	TitelMaxZeichen  = 80
	SpalteMaxZeichen = 40
	// Grenzen des Eingangs außen (ADR-0140 §3): Hält der Ausgang sie nicht
	// ein, scheitert der ganze Push am 422 (Kritiker #778, K-2).
	BearbeiterMax        = 10
	BearbeiterMaxZeichen = 60
)

// Eintrag ist ein Schatten im Wire-Format des Ausgangs. Für Termine fehlen
// nr, spalte und prioritaet ganz; titel ist null, wenn der Eintrag
// vertraulich ist.
type Eintrag struct {
	ID          string   `json:"id"`
	Art         string   `json:"art"`
	Nr          *int     `json:"nr,omitempty"`
	Titel       *string  `json:"titel"`
	Vertraulich bool     `json:"vertraulich"`
	Spalte      *string  `json:"spalte,omitempty"`
	Prioritaet  *string  `json:"prioritaet,omitempty"`
	StartDatum  *string  `json:"startDatum"`
	StartZeit   *string  `json:"startZeit"`
	EndeDatum   *string  `json:"endeDatum"`
	EndeZeit    *string  `json:"endeZeit"`
	Bearbeiter  []string `json:"bearbeiter"`
	VonAgent    bool     `json:"vonAgent"`
}

// Ergebnis eines Baus: die Schatten und was dabei auffiel.
type Ergebnis struct {
	Eintraege []Eintrag
	// NichtBaubar: Kennungen von Einträgen, die sich nicht bauen ließen. Sie
	// gehen als vertraulich hinaus, nur mit Nummer (ADR-0140 §2, K-6).
	NichtBaubar []string
	// OhneKennung: Karten oder Termine ohne id — sie gehen nicht hinaus,
	// denn ohne Kennung gibt es keinen Schatten. Nur gezählt.
	OhneKennung int
	// OhneIdentitaeten: Das Board trägt keine board.identities; menschliche
	// Bearbeiter fallen dann weg (fail-closed, ADR-0140 §2).
	OhneIdentitaeten bool
}

// Vertraulich ist das eine Prädikat aller Stellen (ADR-0140 §10, W-2):
// Fehlender Schlüssel und echtes false heißen „nicht vertraulich“, jeder
// andere Wert — true, null, "", 0, "false" — heißt „vertraulich“.
// Gegenstück in JavaScript: istVertraulich in src/ops.js.
func Vertraulich(obj map[string]json.RawMessage) bool {
	roh, ok := obj["confidential"]
	if !ok {
		return false
	}
	return strings.TrimSpace(string(roh)) != "false"
}

// TerminWirksamVertraulich: Ein Termin ist vertraulich, wenn er es selbst
// ist, wenn die Karte, die karteId nennt, es ist — oder wenn karteId gesetzt
// ist und die Karte fehlt (gelöscht, übertragen, nie da). Fail-closed: Sonst
// entschärfte das Löschen der Karte oder das Entfernen von karteId den Termin
// still (Kritiker #778, K-1). karten: Kennung → vertraulich, für alle Karten
// des Stands.
func TerminWirksamVertraulich(eigen bool, karteID string, karten map[string]bool) bool {
	if eigen {
		return true
	}
	if karteID == "" {
		return false
	}
	v, gibtEs := karten[karteID]
	return !gibtEs || v
}

// KannVertraulichSein: Vorfilter für die Sperre und das Bewahren — trägt der
// Stand überhaupt etwas, das vertraulich sein kann? Das sind ein Schalter
// `confidential`, ein Termin mit `karteId` (wirksam vertraulich auch über eine
// FEHLENDE Karte, Verify #778 V-1) und jeder Stand mit einem \u-Escape (ein
// Schlüssel kann so geschrieben sein, K-8). Im Zweifel wird geprüft.
func KannVertraulichSein(state []byte) bool {
	return bytes.Contains(state, []byte("confidential")) || bytes.Contains(state, []byte("karteId")) ||
		bytes.Contains(state, []byte(`\u`))
}

// GueltigeKennung: Kennungen, die als Schatten hinausgehen dürfen —
// [A-Za-z0-9_-]{1,64}. Eine Kennung ist ein freier Text des Erstellers und
// geht bis zum Skript; mehr als eine UUID braucht es nicht (Kritiker #778,
// K-6). Eine andere Kennung geht nicht hinaus.
func GueltigeKennung(s string) bool {
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

// VertraulichObjekt ist dasselbe Prädikat für ein bereits entpacktes Objekt
// (map[string]any, wie BoardRepo.Update es liest). Ein Test prüft beide
// Fassungen mit denselben Fällen.
func VertraulichObjekt(obj map[string]any) bool {
	v, ok := obj["confidential"]
	if !ok {
		return false
	}
	b, istBool := v.(bool)
	return !istBool || b
}

// --- Textbehandlung (zweite Schicht nach ADR-0044 3a) ------------------------

// unsichtbar: Zeichen, die vor jeder Prüfung entfernt werden. Die Liste aus
// 3a (U+200B–U+200F, U+2060, U+FEFF) und dazu alle Formatzeichen (Cf):
// Sie stehen zwischen den Zeichen eines Musters („ww​w.“) und würden es vor
// der ersten Musterprüfung verdecken. Beobachtbar ist die Cf-Regel über die
// Liste hinaus an zwei Stellen: wo ein Cf-Zeichen eine NFKC-Zusammensetzung
// verhindert („A“ + U+00AD + U+0308 + „pfel“ wird mit der Regel „Äpfel“, ohne
// sie „Apfel“, T-2) und wo es die Lauflänge der ersten Musterprüfung
// verlängert („Donaudampf“ + SHY + „schiff“ + SHY + „fahrts“ + SHY + „ Ende“
// bleibt mit der Regel Text, ohne sie wird es der Ersatztext, V-2).
func unsichtbar(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || r == 0x2060 || r == 0xFEFF || unicode.Is(unicode.Cf, r)
}

// erlaubt: Zeichenvorrat nach 3a — lateinische Buchstaben (A–Z, a–z und die
// Latin-1-Buchstaben mit Akzent, Umlaute, ß), Ziffern, Leerzeichen und
// . , : ; - – / & ( ) + ' ! ?
func erlaubt(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r >= 0xC0 && r <= 0xFF && r != 0xD7 && r != 0xF7:
		return true
	}
	return strings.ContainsRune(" .,:;-–/&()+'!?", r)
}

// istHex: Ziffer oder a–f, A–F.
func istHex(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

// verpackt: Trägt der Text eines der Muster verpackter Daten? Eine
// Zeichenfolge ohne Leerzeichen von mehr als 24 Zeichen, mehr als 12 Ziffern
// insgesamt, „://“, „www.“, „@“ oder mehr als 16 Hex-Zeichen am Stück.
// Hex wird als Lauf gemessen, nicht nur als ganzes Wort — das ist strenger
// als 3a („ein Wort aus mehr als 16 Hex-Zeichen“) und fängt auch
// „x:deadbeef…“.
func verpackt(s string) bool {
	klein := strings.ToLower(s)
	if strings.Contains(klein, "://") || strings.Contains(klein, "www.") || strings.Contains(klein, "@") {
		return true
	}
	ziffern, lauf, hex := 0, 0, 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			ziffern++
		}
		if unicode.IsSpace(r) {
			lauf = 0
		} else {
			lauf++
		}
		if istHex(r) {
			hex++
		} else {
			hex = 0
		}
		if lauf > 24 || hex > 16 {
			return true
		}
	}
	return ziffern > 12
}

// behandle wendet die Textregeln an: unsichtbare Zeichen weg, NFKC, Muster
// prüfen (vor UND nach dem Filtern — sonst fiele „@“ dem Filter zum Opfer,
// Vertrag Push-Skript, F-880 Q-12), Zeichenvorrat filtern, jeden Lauf von Leerzeichen zu
// einem zusammenziehen und die Ränder trimmen (L-1, Absprache
// Board-Paar 11.10.2026), auf max Zeichen kürzen und erneut trimmen. Die Musterprüfung
// liegt VOR dem Kürzen. ok = false, wenn ein Muster greift oder nichts übrig
// bleibt.
//
// Nach NFKC und Filter ist das gewöhnliche Leerzeichen U+0020 der einzige
// Leerraum: NBSP, U+2000–U+200A, U+202F, U+205F und U+3000 macht NFKC zu
// U+0020; Tab, LF, VT, FF, CR, U+0085, U+1680, U+2028 und U+2029 entfernt der
// Filter; U+180E ist Cf und fällt schon vorher weg. strings.Fields trennt an
// jedem Leerraum und deckt damit alle Fälle.
//
// Anders in der ERSTEN Musterprüfung (verpackt): Dort trennt ein Wort, was
// Go unicode.IsSpace ist — U+0009–U+000D, U+0020, U+0085, U+00A0, U+1680,
// U+2000–U+200A, U+2028, U+2029, U+202F, U+205F, U+3000 — und ausdrücklich
// NICHT U+001C–U+001F (V-3).
func behandle(roh string, max int) (string, bool) {
	s := strings.Map(func(r rune) rune {
		if unsichtbar(r) {
			return -1
		}
		return r
	}, roh)
	s = norm.NFKC.String(s)
	if verpackt(s) {
		return "", false
	}
	s = strings.Map(func(r rune) rune {
		if erlaubt(r) {
			return r
		}
		return -1
	}, s)
	if verpackt(s) {
		return "", false
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	if s == "" {
		return "", false
	}
	return s, true
}

// Titel liefert den Titel, wie er hinausgehen darf. Greift ein Muster oder
// bleibt nach dem Filtern nichts übrig, steht der Ersatztext da.
func Titel(roh string) string {
	if s, ok := behandle(roh, TitelMaxZeichen); ok {
		return s
	}
	return Ersatztext
}

// TitelPruefen wendet dieselben Textregeln wie Titel an, mit eigener
// Obergrenze, ersetzt aber nicht: ok = false, wenn ein Muster verpackter
// Daten greift oder nichts übrig bleibt. Für Vorschläge des internen Agenten
// (ADR-0140 §8, Karte #783) — dort wird ein solcher Titel abgelehnt statt
// durch den Ersatztext ersetzt, denn er stünde sonst als Titel einer externen
// Karte da. Die Kürzung auf max ist Sache des Aufrufers (er lehnt zu lange
// Titel vorher ab); hier wird nur noch gefiltert.
func TitelPruefen(roh string, max int) (string, bool) {
	return behandle(roh, max)
}

// Prioritaet: nur niedrig, mittel oder hoch, sonst leer (K-2).
func Prioritaet(roh string) string {
	switch roh {
	case "niedrig", "mittel", "hoch":
		return roh
	}
	return ""
}

// claudeLabels: die festen Claude-Modelle (CLAUDE_DEFAULTS in
// src/identity.js). Ein generisches „Claude“ und Versionsnamen gehören
// bewusst nicht dazu (fail-closed, ADR-0140 §2).
// Fable seit #1172 (ADR-0026 Nachtrag 2026-11-19); TestClaudeLabelsWieIdentityJS hält die Liste gleich.
var claudeLabels = []string{"Claude Opus", "Claude Sonnet", "Claude Haiku", "Claude Fable"}

// bearbeiterFilter kennt die erlaubten Bearbeiter eines Boards: getrimmt und
// ohne Groß-/Kleinschreibung verglichen, ausgeliefert wird das kanonische
// Label (W-9).
type bearbeiterFilter map[string]string

func neuerBearbeiterFilter(board map[string]json.RawMessage) (bearbeiterFilter, bool) {
	f := bearbeiterFilter{}
	for _, l := range claudeLabels {
		f[strings.ToLower(l)] = l
	}
	roh, ok := board["identities"]
	if !ok {
		return f, false
	}
	var ids []map[string]json.RawMessage
	if json.Unmarshal(roh, &ids) != nil {
		return f, false
	}
	for _, i := range ids {
		label := strings.TrimSpace(zeichenkette(i["label"]))
		if label == "" || !bearbeiterZulaessig(label) {
			continue
		}
		if _, schon := f[strings.ToLower(label)]; !schon {
			f[strings.ToLower(label)] = label
		}
	}
	return f, true
}

// bearbeiterZulaessig: nie eine E-Mail-Adresse, nie eine externe Person.
func bearbeiterZulaessig(label string) bool {
	return !strings.Contains(label, "@") && !strings.HasPrefix(strings.ToLower(label), "external:")
}

// filtere liefert die erlaubten Bearbeiter aus den Kandidaten, kanonisch und
// ohne Doppel. Nie nil.
func (f bearbeiterFilter) filtere(kandidaten ...string) []string {
	aus := []string{}
	gesehen := map[string]bool{}
	for _, k := range kandidaten {
		k = strings.TrimSpace(k)
		if k == "" || !bearbeiterZulaessig(k) {
			continue
		}
		kanon, ok := f[strings.ToLower(k)]
		if !ok {
			continue
		}
		// Auch ein Label, das ein Mensch angelegt hat, ist ein Textkanal:
		// dieselben Regeln wie beim Titel, höchstens 60 Zeichen; greift ein
		// Muster, fällt der Name weg (K-2).
		// Zulässigkeit NACH dem Säubern noch einmal: behandle entfernt
		// unsichtbare Zeichen und faltet NFKC — aus „ex<U+00AD>ternal:Bob“
		// würde sonst „external:Bob“, das hinausginge und am Eingang jeden
		// Push des Boards mit 422 scheitern ließe (Kritiker #779, K-2).
		name, gut := behandle(kanon, BearbeiterMaxZeichen)
		if !gut || !bearbeiterZulaessig(name) || gesehen[name] {
			continue
		}
		gesehen[name] = true
		aus = append(aus, name)
		if len(aus) == BearbeiterMax {
			break
		}
	}
	return aus
}

// --- Bau ---------------------------------------------------------------------

// ErrStand: Der Board-Zustand ist als Ganzes nicht lesbar. Der Ausgang
// antwortet dann 500 ohne Teilliste (ADR-0140 §2).
var ErrStand = errors.New("schatten: board-zustand nicht lesbar")

func zeichenkette(roh json.RawMessage) string {
	var s string
	_ = json.Unmarshal(roh, &s)
	return s
}

// istZeichenkette: Der Wert ist eine JSON-Zeichenkette.
func istZeichenkette(roh json.RawMessage) bool {
	var s string
	return json.Unmarshal(roh, &s) == nil
}

func zeichenketten(roh json.RawMessage) []string {
	var l []json.RawMessage
	if json.Unmarshal(roh, &l) != nil {
		return nil
	}
	var aus []string
	for _, e := range l {
		if s := zeichenkette(e); s != "" {
			aus = append(aus, s)
		}
	}
	return aus
}

// jsWahr: Wahrheitswert wie in JavaScript (der Browser prüft archived so).
func jsWahr(roh json.RawMessage) bool {
	switch t := strings.TrimSpace(string(roh)); t {
	case "", "null", "false", `""`:
		return false
	default:
		var n float64
		if json.Unmarshal(roh, &n) == nil {
			return n != 0
		}
		return true
	}
}

// echtWahr: nur das JSON-Literal true (Marken des Folds).
func echtWahr(roh json.RawMessage) bool {
	return strings.TrimSpace(string(roh)) == "true"
}

func istDatum(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func zeiger(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// datumZeit: Datum nur, wenn gültig; Uhrzeit nur zu einem gültigen Datum.
func datumZeit(datum, zeit string) (*string, *string) {
	if !istDatum(datum) {
		return nil, nil
	}
	if !calendar.IstUhrzeit(zeit) {
		zeit = ""
	}
	return zeiger(datum), zeiger(zeit)
}

// kartenStand: was ein Termin über seine Karte wissen muss.
type kartenStand struct {
	ausgenommen bool
	vertraulich bool
}

// Baue liefert die Schatten eines Board-Zustands (state_json).
//
// Welche Karten: alle außer archivierten Karten, Karten in archivierten
// Lanes, Sprint-Meta-Karten und Karten in Service-Lanes. Erledigte gehen mit.
// Welche Termine: alle; ein Termin mit karteId erbt deren Einstufung und
// fehlt, wenn die Karte ausgenommen ist.
//
// Ganz oder Fehler: Ist der Zustand als Ganzes nicht lesbar (kein Board,
// sprints kein Array, eine Lane keine Liste von Karten, termine kein Array),
// kommt ErrStand. Eine einzelne Karte, die sich nicht bauen lässt, legt
// dagegen nicht alles still — sie geht als vertraulich nur mit Nummer.
func Baue(state []byte) (*Ergebnis, error) {
	var stand map[string]json.RawMessage
	if err := json.Unmarshal(state, &stand); err != nil || stand == nil {
		return nil, fmt.Errorf("%w: kein JSON-Objekt", ErrStand)
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(stand["board"], &board); err != nil || board == nil {
		return nil, fmt.Errorf("%w: board fehlt", ErrStand)
	}
	var lanes []map[string]json.RawMessage
	if roh, ok := board["sprints"]; !ok || json.Unmarshal(roh, &lanes) != nil {
		return nil, fmt.Errorf("%w: sprints fehlt oder ist keine Liste", ErrStand)
	}
	var termine []map[string]json.RawMessage
	if roh, ok := board["termine"]; ok && strings.TrimSpace(string(roh)) != "null" {
		if json.Unmarshal(roh, &termine) != nil {
			return nil, fmt.Errorf("%w: termine ist keine Liste", ErrStand)
		}
	}

	spalten := map[string]string{}
	var defs []map[string]json.RawMessage
	_ = json.Unmarshal(board["columnDefs"], &defs)
	for _, d := range defs {
		if id := zeichenkette(d["id"]); id != "" {
			if name, ok := behandle(zeichenkette(d["title"]), SpalteMaxZeichen); ok {
				spalten[id] = name
			} else {
				spalten[id] = ""
			}
		}
	}
	filter, mitIdentitaeten := neuerBearbeiterFilter(board)

	erg := &Ergebnis{Eintraege: []Eintrag{}, NichtBaubar: []string{}, OhneIdentitaeten: !mitIdentitaeten}
	karten := map[string]kartenStand{}

	// Vorlauf: Vertraulichkeit je Karten-Kennung über alle Vorkommen (ODER)
	// und doppelte Kennungen. Eine doppelte Kennung geht nur einmal hinaus,
	// und dann vertraulich — welches Vorkommen „gilt“, ist nicht
	// entscheidbar, und der Eingang lehnte doppelte Kennungen ab (K-5).
	vorkommen := map[string]int{}
	kartenVertraulich := map[string]bool{}
	for _, l := range lanes {
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		for _, k := range cards {
			if id := zeichenkette(k["id"]); id != "" && k != nil {
				vorkommen[id]++
				kartenVertraulich[id] = kartenVertraulich[id] || Vertraulich(k)
			}
		}
	}
	for id, n := range vorkommen {
		if n > 1 {
			kartenVertraulich[id] = true
		}
	}
	ausgegeben := map[string]bool{}

	for _, l := range lanes {
		if l == nil {
			continue
		}
		var cards []map[string]json.RawMessage
		if roh, ok := l["cards"]; ok && strings.TrimSpace(string(roh)) != "null" {
			if json.Unmarshal(roh, &cards) != nil {
				return nil, fmt.Errorf("%w: cards einer Lane ist keine Liste", ErrStand)
			}
		}
		laneAus := jsWahr(l["archived"]) || zeichenkette(l["kind"]) == "service"
		for _, k := range cards {
			if k == nil {
				continue
			}
			id := zeichenkette(k["id"])
			aus := laneAus || jsWahr(k["archived"]) || zeichenkette(k["kind"]) == "sprint-meta"
			vertraulich := kartenVertraulich[id]
			if id != "" {
				// Bei doppelter Kennung ist die Karte ausgenommen nur, wenn
				// es alle Vorkommen sind.
				vorher, schon := karten[id]
				karten[id] = kartenStand{ausgenommen: aus && (!schon || vorher.ausgenommen), vertraulich: vertraulich}
			}
			if aus {
				continue
			}
			if !GueltigeKennung(id) {
				erg.OhneKennung++
				continue
			}
			if ausgegeben[id] {
				erg.NichtBaubar = append(erg.NichtBaubar, id)
				continue
			}
			ausgegeben[id] = true
			erg.Eintraege = append(erg.Eintraege, baueKarte(id, k, vertraulich, spalten, filter, erg))
		}
	}

	for _, t := range termine {
		if t == nil {
			continue
		}
		id := zeichenkette(t["id"])
		kid := zeichenkette(t["karteId"])
		if ks, ok := karten[kid]; kid != "" && ok && ks.ausgenommen {
			continue
		}
		vertraulich := TerminWirksamVertraulich(Vertraulich(t), kid, kartenVertraulich)
		if !GueltigeKennung(id) {
			erg.OhneKennung++
			continue
		}
		if ausgegeben["termin:"+id] {
			erg.NichtBaubar = append(erg.NichtBaubar, id)
			continue
		}
		ausgegeben["termin:"+id] = true
		if e, ok := baueTermin(id, t, vertraulich, filter, erg); ok {
			erg.Eintraege = append(erg.Eintraege, e)
		}
	}
	return erg, nil
}

func baueKarte(id string, k map[string]json.RawMessage, vertraulich bool, spalten map[string]string,
	filter bearbeiterFilter, erg *Ergebnis) Eintrag {
	leer := ""
	e := Eintrag{ID: id, Art: ArtKarte, Vertraulich: true, Spalte: &leer, Prioritaet: &leer, Bearbeiter: []string{}}
	var nr float64
	// Obergrenze wie am Eingang (int32): Eine größere Nummer gibt es in der
	// Praxis nicht; sie fehlt dann, statt den Push am 422 scheitern zu lassen
	// (Kritiker #779, K-5).
	if json.Unmarshal(k["displayId"], &nr) == nil && nr > 0 && nr <= math.MaxInt32 && nr == float64(int(nr)) {
		n := int(nr)
		e.Nr = &n
	}
	// Nicht baubar: ein Titel, der keine Zeichenkette ist. Dann nur Nummer,
	// vertraulich, ohne die übrigen Felder (K-6).
	if roh, ok := k["title"]; ok && !istZeichenkette(roh) {
		erg.NichtBaubar = append(erg.NichtBaubar, id)
		return e
	}
	e.Vertraulich = vertraulich
	if !vertraulich {
		t := Titel(zeichenkette(k["title"]))
		e.Titel = &t
	}
	spalte := spalten[zeichenkette(k["columnId"])]
	prio := Prioritaet(zeichenkette(k["priority"]))
	e.Spalte, e.Prioritaet = &spalte, &prio
	start := zeichenkette(k["startDate"])
	e.StartDatum, e.StartZeit = datumZeit(start, zeichenkette(k["startTime"]))
	e.EndeDatum, e.EndeZeit = datumZeit(zeichenkette(k["dueDate"]), zeichenkette(k["dueTime"]))
	e.Bearbeiter = filter.filtere(append([]string{zeichenkette(k["owner"])}, zeichenketten(k["assignees"])...)...)
	e.VonAgent = echtWahr(k["titelVonAgent"])
	return e
}

// baueTermin: ein Termin ohne gültiges Datum ist kein Termin (der Fold lässt
// ihn nicht entstehen) und fehlt; ein Titel, der keine Zeichenkette ist,
// macht ihn vertraulich ohne Titel.
//
// Abbildung: datum/von → startDatum/startZeit, bisDatum/bis →
// endeDatum/endeZeit. Ein eintägiger Termin mit Endzeit („10:00–11:00“)
// trägt endeDatum = datum, damit endeZeit ein Datum hat.
func baueTermin(id string, t map[string]json.RawMessage, vertraulich bool, filter bearbeiterFilter, erg *Ergebnis) (Eintrag, bool) {
	datum := zeichenkette(t["datum"])
	if !istDatum(datum) {
		return Eintrag{}, false
	}
	e := Eintrag{ID: id, Art: ArtTermin, Vertraulich: vertraulich, Bearbeiter: []string{}}
	e.StartDatum, e.StartZeit = datumZeit(datum, zeichenkette(t["von"]))
	bisDatum := zeichenkette(t["bisDatum"])
	if !istDatum(bisDatum) || bisDatum < datum {
		bisDatum = ""
	}
	bis := zeichenkette(t["bis"])
	if bisDatum == "" && calendar.IstUhrzeit(bis) {
		bisDatum = datum
	}
	e.EndeDatum, e.EndeZeit = datumZeit(bisDatum, bis)
	if roh, ok := t["titel"]; ok && !istZeichenkette(roh) {
		erg.NichtBaubar = append(erg.NichtBaubar, id)
		e.Vertraulich = true
		e.StartDatum, e.StartZeit, e.EndeDatum, e.EndeZeit = nil, nil, nil, nil
		return e, true
	}
	if !vertraulich {
		ti := Titel(zeichenkette(t["titel"]))
		e.Titel = &ti
	}
	e.Bearbeiter = filter.filtere(zeichenketten(t["personen"])...)
	e.VonAgent = echtWahr(t["vonAgent"])
	return e, true
}
