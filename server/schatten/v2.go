package schatten

// v2.go — Hinweg v2 des Board-Paars (ADR-0141 §4, Karte #825, Schritt 2a).
//
// Innen baut BaueV2 aus dem Board-Zustand die Antwort von
// GET /agent/boardpaar/spiegel/{board}: die Struktur (Lanes und Spalten mit
// innerer ID, Titel, kind, archived), die Schatten samt Lane, Spalte und
// innerer ID, und die Spiegel (in 2a immer leer). Das Skript ersetzt jede
// innere ID durch seine Kennung (HMAC) und schiebt das Ergebnis außen hinein.
//
// Außen prüft LiesEingangV2 den Body von PUT /agent/boardpaar/spiegel/{board}
// — wie der Eingang aus Stufe 1 als Fixpunkt der Ausgangsregeln: Was der
// Ausgang erzeugt hat und das Skript nur in den Kennungen verändert hat, geht
// durch, alles andere ist 422. StrukturAnwenden schreibt die Struktur in den
// Board-Zustand der Kopie.
//
// Abweichungen gegenüber Stufe 1, beide aus ADR-0141:
//   - Schatten tragen ihren Titel auch, wenn sie vertraulich sind (Beschluss 3:
//     „mit Titel, Priorität, Terminen und Bearbeitern“). Der Titel durchläuft
//     dieselbe Textbehandlung (T16) wie in Stufe 1.
//   - In Schritt 2a gehen auch die NICHT vertraulichen Karten und Termine als
//     Schatten hinaus (vertraulich = false): Spiegel gibt es erst ab 2b. So ist
//     die Kopie schon in 2a eine lesbare Kopie in Lane und Spalte.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/calendar"
)

// NSBoardpaar ist der feste Namensraum für alle abgeleiteten Kennungen des
// Board-Paars (ADR-0141 §2, „NS_BOARDPAAR“): UUID v5 (SHA-1) über die Kennung
// als UTF-8-Bytes, Ergebnis in Kleinbuchstaben. Das Skript bildet damit die
// Außen-Board-ID, der Eingang die Außen-IDs gespiegelter Lanes und Spalten
// (und ab 2b der Spiegel), ab 2c die innere ID außen angelegter Karten.
// NIE ändern: Jede abgeleitete ID hinge daran.
var NSBoardpaar = uuid.MustParse("17afd522-ad42-40fd-ad04-1406d6de81d2")

// AbgeleiteteID liefert UUIDv5(NSBoardpaar, kennung).
func AbgeleiteteID(kennung string) string {
	return uuid.NewSHA1(NSBoardpaar, []byte(kennung)).String()
}

// Klartext-IDs, die nicht durch eine Kennung ersetzt werden (ADR-0141 §2,
// PK-10): die vier Standardspalten und die Backlog-Lane.
const BacklogLane = "__backlog__"

var standardSpalten = map[string]bool{
	"col-backlog": true, "col-ready": true, "col-in-arbeit": true, "col-erledigt": true,
}

// IstStandardSpalte: eine der vier Standardspalten.
func IstStandardSpalte(id string) bool { return standardSpalten[id] }

// AussenLaneID: die ID einer gespiegelten Lane außen.
func AussenLaneID(kennung string) string {
	if kennung == BacklogLane {
		return BacklogLane
	}
	return AbgeleiteteID(kennung)
}

// AussenSpalteID: die ID einer gespiegelten Spalte außen.
func AussenSpalteID(kennung string) string {
	if IstStandardSpalte(kennung) {
		return kennung
	}
	return AbgeleiteteID(kennung)
}

// Grenzen der Struktur (ADR-0141 §4).
const (
	LaneTitelMax   = 200
	SpalteTitelMax = 100
	MaxLanes       = 1000
	MaxSpalten     = 100
)

// Lane-Arten, die hinausgehen. Service-Lanes gibt es nur auf dem
// Service-Board, das nie gekoppelt wird; sie gehen nicht hinaus.
var laneArten = map[string]bool{"sprint": true, "lane": true, "backlog": true}

// LaneTraegtKartenHinaus: Gehen die Karten einer Lane mit dieser ID, Art und
// Archiv-Marke beim Hinweg hinaus (BaueV2 für die Struktur, baueSpiegel für
// die Karten)? Gemeinsames Prädikat für den Rückweg (Ort einer Anlage von
// außen), damit beide Seiten nicht auseinanderlaufen (Sitzungs-QA #835,
// Verify 2 V-1; Klasse 295). Der Test TestRueckwegSitzungsQA835_AnlageImmerSichtbar
// gleicht es gegen BaueV2Mit ab.
func LaneTraegtKartenHinaus(id, kind string, archived bool) bool {
	if !GueltigeKennung(id) || archived || kind == "service" {
		return false
	}
	if id == BacklogLane {
		return true // BaueV2 setzt kind = backlog
	}
	switch kind {
	case "":
		kind = "sprint"
	case "backlog":
		kind = "lane" // Kritik Sprint 93 K-4: eigene ID mit kind backlog geht als Lane hinaus
	}
	return laneArten[kind]
}

// SpalteGehtHinaus: Spiegelt der Hinweg eine Spalte mit dieser ID (BaueV2
// nimmt nur gültige Kennungen)?
func SpalteGehtHinaus(id string) bool { return GueltigeKennung(id) }

// StrukturTitel: Titel einer Lane oder Spalte, wie er hinausgeht —
// Steuerzeichen und unsichtbare Formatzeichen entfernt, Ränder getrimmt, auf
// max Zeichen gekürzt. Sonst unverändert: Die Struktur ist außen für die
// Mitglieder sichtbar wie innen (ADR-0141 Beschluss 2), die Textbehandlung
// T16 gilt für Schatten, nicht für Struktur. Bewusste Entscheidung (Sprint-93-
// Kritik S-2, ADR-0141 „Umsetzung Schritt 2a“ Punkt 7): ADR-0140 K-2 ist damit
// für Kopien aufgehoben; die Grenze ist der Auftrag (HatAuftrag im Hinweg).
func StrukturTitel(roh string, max int) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unsichtbar(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, roh)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

// StrukturLane ist eine Lane im Hinweg.
type StrukturLane struct {
	ID       string `json:"id"`
	Titel    string `json:"titel"`
	Kind     string `json:"kind"`
	Archived bool   `json:"archived"`
}

// StrukturSpalte ist eine Spalte im Hinweg.
type StrukturSpalte struct {
	ID    string `json:"id"`
	Titel string `json:"titel"`
}

// Struktur: Lanes und Spalten in ihrer Reihenfolge. Titel (ab Format 3,
// Karte #826, F-2140) ist der Titel des Boards innen, in der Form, die POST
// /agent/boardpaar/boards annimmt; außen wird die Kopie danach benannt. Im
// Format 2 fehlt er, dann bleibt der Titel außen, wie er ist.
type Struktur struct {
	Titel   *string          `json:"titel,omitempty"`
	Lanes   []StrukturLane   `json:"lanes"`
	Spalten []StrukturSpalte `json:"spalten"`
}

// KopieTitelMax: Zeichen eines Board-Titels der Kopie (wie
// storage.KopieTitelMax).
const KopieTitelMax = 200

// KopieTitelGueltig: 1–KopieTitelMax Zeichen, getrimmt, gültiges UTF-8, ohne
// Steuerzeichen — dieselbe Regel wie storage.PruefeKopieTitel (das Paket
// storage importiert schatten, nicht umgekehrt).
func KopieTitelGueltig(t string) bool {
	return t != "" && t == strings.TrimSpace(t) && utf8.ValidString(t) && utf8.RuneCountInString(t) <= KopieTitelMax &&
		strings.IndexFunc(t, unicode.IsControl) < 0
}

// EintragV2 ist ein Schatten im Hinweg v2. Gegenüber Stufe 1: lane und
// spalte sind IDs (Spalte nicht mehr der Name), titel steht auch bei
// vertraulichen Einträgen, ursprung ist neu (ab 2c die Außen-ID einer außen
// angelegten Karte, in 2a immer null). Termine tragen nr, lane, spalte und
// prioritaet nie.
type EintragV2 struct {
	ID          string   `json:"id"`
	Art         string   `json:"art"`
	Nr          *int     `json:"nr,omitempty"`
	Titel       *string  `json:"titel"`
	Vertraulich bool     `json:"vertraulich"`
	Lane        *string  `json:"lane,omitempty"`
	Spalte      *string  `json:"spalte,omitempty"`
	Prioritaet  *string  `json:"prioritaet,omitempty"`
	StartDatum  *string  `json:"startDatum"`
	StartZeit   *string  `json:"startZeit"`
	EndeDatum   *string  `json:"endeDatum"`
	EndeZeit    *string  `json:"endeZeit"`
	Bearbeiter  []string `json:"bearbeiter"`
	VonAgent    bool     `json:"vonAgent"`
	Ursprung    *string  `json:"ursprung"`
}

// ErgebnisV2 eines Baus.
type ErgebnisV2 struct {
	Struktur Struktur
	Schatten []EintragV2
	// Spiegel (nur mit mitSpiegel, Format 3): SpiegelKarte und SpiegelTermin,
	// erst alle Karten in Lane-Reihenfolge, dann die Termine.
	Spiegel []any
	// NichtBaubar, OhneKennung, OhneIdentitaeten wie in Stufe 1.
	NichtBaubar      []string
	OhneKennung      int
	OhneIdentitaeten bool
	// OhneOrt: Karten, deren Lane oder Spalte nicht hinausgeht (Spalte fehlt
	// in columnDefs, Lane-ID ungültig) — sie gehen nicht hinaus.
	OhneOrt int
	// OhneLane/OhneSpalte: die beiden Ursachen von OhneOrt getrennt (Verify
	// #825 V-1) — Lane mit ungültiger ID oder Art bzw. Spalte fehlt in columnDefs.
	OhneLane, OhneSpalte int
}

// BaueV2 liefert Struktur und Schatten eines Board-Zustands.
//
// Karten: dieselbe Auswahl wie Baue (keine archivierten, keine in
// archivierten Lanes, keine Sprint-Meta-Karten, keine Service-Lanes).
// Struktur: alle Lanes außer Service-Lanes, auch archivierte (mit
// archived = true), alle Spalten aus columnDefs. Ganz oder Fehler wie Baue:
// Ein nicht lesbarer Zustand, eine doppelte Lane- oder Spalten-ID → ErrStand.
func BaueV2(state []byte) (*ErgebnisV2, error) {
	return BaueV2Mit(state, false)
}

// BaueV2Mit ist BaueV2; mit mitSpiegel (Format 3, Karte #826) gehen nicht
// vertrauliche Karten und Termine als Spiegel hinaus und nicht mehr als
// Schatten — Schatten sind dann nur noch die vertraulichen (und die nicht
// baubaren). Ohne mitSpiegel gilt Schritt 2a: alle als Schatten, Spiegel leer.
func BaueV2Mit(state []byte, mitSpiegel bool) (*ErgebnisV2, error) {
	// Der Bau aus Stufe 1 liefert Auswahl, Titel, Bearbeiter, Termine — v2
	// übernimmt ihn und ergänzt Ort und Titel vertraulicher Einträge.
	alt, err := Baue(state)
	if err != nil {
		return nil, err
	}
	var stand map[string]json.RawMessage
	_ = json.Unmarshal(state, &stand)
	var board map[string]json.RawMessage
	_ = json.Unmarshal(stand["board"], &board)
	var lanes []map[string]json.RawMessage
	_ = json.Unmarshal(board["sprints"], &lanes)
	var defs []map[string]json.RawMessage
	if roh, ok := board["columnDefs"]; ok && strings.TrimSpace(string(roh)) != "null" {
		if json.Unmarshal(roh, &defs) != nil {
			return nil, fmt.Errorf("%w: columnDefs ist keine Liste", ErrStand)
		}
	}
	// Kritik #825 A-1: Ein Board ohne Spalten zeigt der Browser mit den vier
	// Standardspalten (migrateDefaultColumns) — dieselbe Annahme hier, sonst
	// ginge jede Karte als „ohne Ort“ verloren und außen stünde eine leere
	// Spaltenliste.
	if len(defs) == 0 {
		for _, d := range [][2]string{{"col-backlog", "Backlog"}, {"col-ready", "Ready"}, {"col-in-arbeit", "In Arbeit"}, {"col-erledigt", "Erledigt"}} {
			defs = append(defs, map[string]json.RawMessage{"id": roh(d[0]), "title": roh(d[1])})
		}
	}

	erg := &ErgebnisV2{Struktur: Struktur{Lanes: []StrukturLane{}, Spalten: []StrukturSpalte{}},
		Schatten: []EintragV2{}, Spiegel: []any{}, NichtBaubar: alt.NichtBaubar, OhneKennung: alt.OhneKennung,
		OhneIdentitaeten: alt.OhneIdentitaeten}

	spalten := map[string]bool{}
	for _, d := range defs {
		id := zeichenkette(d["id"])
		if !GueltigeKennung(id) {
			continue
		}
		if spalten[id] {
			return nil, fmt.Errorf("%w: Spalten-ID doppelt", ErrStand)
		}
		spalten[id] = true
		erg.Struktur.Spalten = append(erg.Struktur.Spalten,
			StrukturSpalte{ID: id, Titel: StrukturTitel(zeichenkette(d["title"]), SpalteTitelMax)})
	}
	if len(erg.Struktur.Spalten) > MaxSpalten {
		return nil, fmt.Errorf("%w: mehr als %d Spalten", ErrStand, MaxSpalten)
	}

	// Ort und Titel je Karten-Kennung — aus dem ersten Vorkommen, wie Baue.
	type ort struct{ lane, spalte, titel string }
	orte := map[string]ort{}
	// ursprung (ab 2c): die Außen-ID einer außen angelegten Karte bzw. eines
	// Termins, auch an Schatten (ADR-0141 §2, W2-3) — der Eingang erkennt
	// daran, dass ein wartender Spiegel innen vertraulich geworden ist.
	ursprung := map[string]json.RawMessage{}
	lanesGesehen := map[string]bool{}
	backlog := false
	for _, l := range lanes {
		if l == nil {
			continue
		}
		id := zeichenkette(l["id"])
		kind := zeichenkette(l["kind"])
		if id == BacklogLane {
			kind = "backlog"
		} else if kind == "" {
			kind = "sprint"
		} else if kind == "backlog" {
			// Kritik Sprint 93 K-4: Eine Lane mit kind backlog und eigener ID
			// ist innen gültig (Karte #332). Außen gibt es genau einen Backlog;
			// sie geht als gewöhnliche Lane hinaus, statt mit ihren Karten zu
			// fehlen.
			kind = "lane"
		}
		if kind == "service" || !laneArten[kind] || !GueltigeKennung(id) || (kind == "backlog") != (id == BacklogLane) {
			continue
		}
		if lanesGesehen[id] {
			return nil, fmt.Errorf("%w: Lane-ID doppelt", ErrStand)
		}
		lanesGesehen[id] = true
		backlog = backlog || id == BacklogLane
		erg.Struktur.Lanes = append(erg.Struktur.Lanes, StrukturLane{ID: id,
			Titel: StrukturTitel(zeichenkette(l["title"]), LaneTitelMax), Kind: kind, Archived: jsWahr(l["archived"])})
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		for _, k := range cards {
			kid := zeichenkette(k["id"])
			if kid == "" {
				continue
			}
			if _, schon := orte[kid]; schon {
				continue
			}
			o := ort{lane: id, spalte: zeichenkette(k["columnId"])}
			ursprung[spiegelSchluessel(ArtKarte, kid)] = k["boardpaarUrsprung"]
			if istZeichenkette(k["title"]) {
				o.titel = zeichenkette(k["title"])
			}
			orte[kid] = o
		}
	}
	if !backlog {
		return nil, fmt.Errorf("%w: Backlog-Lane fehlt", ErrStand)
	}
	if len(erg.Struktur.Lanes) > MaxLanes {
		return nil, fmt.Errorf("%w: mehr als %d Lanes", ErrStand, MaxLanes)
	}

	// Titel der Termine (auch vertraulicher).
	termTitel := map[string]*string{}
	var termine []map[string]json.RawMessage
	_ = json.Unmarshal(board["termine"], &termine)
	for _, t := range termine {
		if t == nil {
			continue
		}
		id := zeichenkette(t["id"])
		if _, schon := ursprung[spiegelSchluessel(ArtTermin, id)]; !schon {
			ursprung[spiegelSchluessel(ArtTermin, id)] = t["boardpaarUrsprung"]
		}
		if _, schon := termTitel[id]; schon || !istZeichenkette(t["titel"]) {
			continue
		}
		ti := Titel(zeichenkette(t["titel"]))
		termTitel[id] = &ti
	}

	nichtBaubar := map[string]bool{}
	for _, id := range alt.NichtBaubar {
		nichtBaubar[id] = true
	}
	var sp spiegelErgebnis
	if mitSpiegel {
		lanesAus := map[string]bool{}
		for _, l := range erg.Struktur.Lanes {
			lanesAus[l.ID] = true
		}
		sp = baueSpiegel(board, lanes, lanesAus, spalten)
		for _, k := range sp.karten {
			erg.Spiegel = append(erg.Spiegel, k)
		}
		for _, t := range sp.termine {
			erg.Spiegel = append(erg.Spiegel, t)
		}
		erg.OhneOrt += sp.ohneOrt
		erg.OhneLane += sp.ohneLane
		erg.OhneSpalte += sp.ohneSpalte
	}
	for _, e := range alt.Eintraege {
		if sp.gesetzt[spiegelSchluessel(e.Art, e.ID)] {
			continue // geht als Spiegel hinaus
		}
		v := EintragV2{ID: e.ID, Art: e.Art, Nr: e.Nr, Titel: e.Titel, Vertraulich: e.Vertraulich,
			Prioritaet: e.Prioritaet, StartDatum: e.StartDatum, StartZeit: e.StartZeit, EndeDatum: e.EndeDatum,
			EndeZeit: e.EndeZeit, Bearbeiter: e.Bearbeiter, VonAgent: e.VonAgent,
			Ursprung: ursprungVon(ursprung[spiegelSchluessel(e.Art, e.ID)])}
		if e.Art == ArtKarte {
			o := orte[e.ID]
			if !lanesGesehen[o.lane] {
				erg.OhneOrt++
				erg.OhneLane++
				continue
			}
			if !spalten[o.spalte] {
				erg.OhneOrt++
				erg.OhneSpalte++
				continue
			}
			lane, spalte := o.lane, o.spalte
			v.Lane, v.Spalte = &lane, &spalte
			// Beschluss 3: der Titel auch bei vertraulichen Karten — außer die
			// Karte war nicht baubar (dann bleibt sie ohne Titel, wie Stufe 1).
			if v.Titel == nil && !nichtBaubar[e.ID] {
				t := Titel(o.titel)
				v.Titel = &t
			}
		} else if v.Titel == nil && !nichtBaubar[e.ID] {
			v.Titel = termTitel[e.ID]
		}
		erg.Schatten = append(erg.Schatten, v)
	}
	return erg, nil
}

// --- Eingang v2 (außen) --------------------------------------------------------

// Kopie ist eine geprüfte Hinweg-Menge v2.
type Kopie struct {
	Struktur Struktur
	Schatten []Eingang
	// Spiegel (ab Format 3, Karte #826); leer im Format 2.
	Spiegel []SpiegelEingang
	// RueckwegBis: Zählmarke des Skripts (§2, ab 2c); nil = nicht gesendet.
	RueckwegBis *int64
	// Bearbeitbar: Schalter „bearbeitbar“ der Kopie (ADR-0141 Nachtrag
	// #836), optional; nil = nicht gesendet (fehlt oder null), der Wert außen
	// bleibt dann, wie er ist.
	Bearbeitbar *bool
}

// GueltigeLaneKennung: Kennung des Skripts oder die Backlog-Lane.
func GueltigeLaneKennung(s string) bool { return s == BacklogLane || GueltigeSchattenID(s) }

// GueltigeSpaltenKennung: Kennung des Skripts oder eine Standardspalte.
func GueltigeSpaltenKennung(s string) bool { return IstStandardSpalte(s) || GueltigeSchattenID(s) }

var (
	felderKarteV2 = map[string]bool{
		"schattenId": true, "art": true, "titel": true, "vertraulich": true, "lane": true, "spalte": true,
		"prioritaet": true, "startDatum": true, "startZeit": true, "endeDatum": true, "endeZeit": true,
		"bearbeiter": true, "vonAgent": true, "ursprung": true,
	}
	felderTerminV2 = map[string]bool{
		"schattenId": true, "art": true, "titel": true, "vertraulich": true,
		"startDatum": true, "startZeit": true, "endeDatum": true, "endeZeit": true,
		"bearbeiter": true, "vonAgent": true, "ursprung": true,
	}
)

// LiesEingangV2 prüft den Body des PUT /agent/boardpaar/spiegel. Ein Fehler
// ist immer ein *EingangFehler (422, bei abweichender Hauptversion 409); dann
// darf nichts geschrieben werden.
func LiesEingangV2(body []byte, eigeneVersion string) (*Kopie, error) {
	// Höchstens sechs Schlüssel: die fünf Pflichtschlüssel und das optionale
	// bearbeitbar (#836).
	huelle, err := objekt(body, 6)
	if err != nil {
		return nil, abgelehnt("Hülle: %v", err)
	}
	// Der JSON-Schlüssel der Schattenliste — einmal als Literal (Wächter
	// main_schatten_780_test.go zählt es), sonst über die Konstante.
	const schattenSchluessel = "schatten"
	erlaubt := map[string]bool{"version": true, "struktur": true, schattenSchluessel: true, "spiegel": true, "rueckweg_bis": true}
	for k := range huelle {
		if !erlaubt[k] && k != "bearbeitbar" {
			return nil, abgelehnt("Hülle: unbekannter Schlüssel %q (erlaubt sind version, struktur, schatten, spiegel, rueckweg_bis, bearbeitbar)", kurz(k))
		}
	}
	for k := range erlaubt {
		if _, ok := huelle[k]; !ok {
			return nil, abgelehnt("Hülle: %s fehlt", k)
		}
	}
	version, ok := text(huelle["version"])
	if !ok || !versionFrei.MatchString(version) {
		return nil, abgelehnt("Hülle: version ist keine Versionsangabe")
	}
	if ihre, ok := Hauptversion(version); ok {
		if meine, ok := Hauptversion(eigeneVersion); ok && meine != ihre {
			return nil, &EingangFehler{Status: 409, Grund: fmt.Sprintf(
				"Hauptversion %d weicht von der eigenen (%d) ab — Push anhalten bis zum Gleichstand", ihre, meine)}
		}
	}
	k := &Kopie{}
	if roh := huelle["rueckweg_bis"]; !istNull(roh) {
		var n int64
		if json.Unmarshal(roh, &n) != nil || n < 0 {
			return nil, abgelehnt("Hülle: rueckweg_bis: erwartet null oder eine ganze Zahl ab 0")
		}
		k.RueckwegBis = &n
	}
	if roh, ok := huelle["bearbeitbar"]; ok && !istNull(roh) {
		var b bool
		if json.Unmarshal(roh, &b) != nil {
			return nil, abgelehnt("Hülle: bearbeitbar: erwartet true, false oder null")
		}
		k.Bearbeitbar = &b
	}
	if k.Struktur, err = liesStruktur(huelle["struktur"]); err != nil {
		return nil, abgelehnt("struktur: %v", err)
	}
	lanes := map[string]bool{}
	for _, l := range k.Struktur.Lanes {
		lanes[l.ID] = true
	}
	spalten := map[string]string{}
	for _, s := range k.Struktur.Spalten {
		spalten[s.ID] = s.Titel
	}

	if istNull(huelle[schattenSchluessel]) {
		return nil, abgelehnt("Hülle: schatten fehlt")
	}
	liste, err := elemente(huelle[schattenSchluessel], EingangMaxSchatten)
	if err != nil {
		return nil, abgelehnt("Hülle: schatten: %v", err)
	}
	k.Schatten = make([]Eingang, 0, len(liste))
	gesehen := make(map[string]int, len(liste))
	var fehler []ElementFehler
	anzahl := 0
	melde := func(i int, grund string) {
		anzahl++
		if len(fehler) < EingangMaxFehlerListe {
			fehler = append(fehler, ElementFehler{Index: i, Grund: grund})
		}
	}
	for i, roh := range liste {
		e, err := liesSchattenV2(roh, lanes, spalten)
		if e.SchattenID != "" {
			if j, schon := gesehen[e.SchattenID]; schon {
				grund := fmt.Sprintf("schattenId wie schatten[%d]", j)
				if err != nil {
					grund = err.Error() + "; " + grund
				}
				melde(i, grund)
				continue
			}
			gesehen[e.SchattenID] = i
		}
		if err != nil {
			melde(i, err.Error())
			continue
		}
		k.Schatten = append(k.Schatten, e)
	}
	if anzahl > 0 {
		f := abgelehnt("schatten[%d]: %s", fehler[0].Index, fehler[0].Grund)
		f.Fehler, f.Weitere = fehler, anzahl-len(fehler)
		return nil, f
	}
	// Spiegel (Format 3, Karte #826): nach den Schatten, denn eine Kennung
	// darf nicht in beiden Listen stehen.
	schattenIDs := make(map[string]bool, len(k.Schatten))
	for _, e := range k.Schatten {
		schattenIDs[e.SchattenID] = true
	}
	if k.Spiegel, err = liesSpiegelListe(huelle["spiegel"], lanes, spalten, schattenIDs); err != nil {
		return nil, err
	}
	// ursprung (ab 2c): eine Außen-ID steht höchstens einmal in der ganzen
	// Menge — sonst wüsste der Eingang nicht, welcher Eintrag die wartende
	// Karte bzw. den wartenden Termin übernimmt.
	ursprungBei := map[string]string{}
	pruefeUrsprung := func(u *string, wo string) error {
		if u == nil {
			return nil
		}
		if vorher, schon := ursprungBei[*u]; schon {
			return abgelehnt("%s: ursprung wie %s", wo, vorher)
		}
		ursprungBei[*u] = wo
		return nil
	}
	for i, e := range k.Schatten {
		if err := pruefeUrsprung(e.Ursprung, fmt.Sprintf("schatten[%d]", i)); err != nil {
			return nil, err
		}
	}
	for i, e := range k.Spiegel {
		u := (*string)(nil)
		if e.Karte != nil {
			u = e.Karte.Ursprung
		} else if e.Termin != nil {
			u = e.Termin.Ursprung
		}
		if err := pruefeUrsprung(u, fmt.Sprintf("spiegel[%d]", i)); err != nil {
			return nil, err
		}
	}
	return k, nil
}

// liesStruktur prüft {lanes, spalten}.
func liesStruktur(roh json.RawMessage) (Struktur, error) {
	var s Struktur
	o, err := objekt(roh, 4)
	if err != nil {
		return s, err
	}
	for key := range o {
		if key != "lanes" && key != "spalten" && key != "titel" {
			return s, fmt.Errorf("unbekannter Schlüssel %q", kurz(key))
		}
	}
	if roh, ok := o["titel"]; ok {
		t, ok := text(roh)
		if !ok || !KopieTitelGueltig(t) {
			return s, fmt.Errorf("titel: 1–%d Zeichen, getrimmt, ohne Steuerzeichen", KopieTitelMax)
		}
		s.Titel = &t
	}
	if istNull(o["lanes"]) || istNull(o["spalten"]) || o["lanes"] == nil || o["spalten"] == nil {
		return s, errors.New("lanes und spalten sind Pflicht")
	}
	ls, err := elemente(o["lanes"], MaxLanes)
	if err != nil {
		return s, fmt.Errorf("lanes: %v", err)
	}
	s.Lanes = make([]StrukturLane, 0, len(ls))
	gesehen := map[string]bool{}
	backlog := false
	for i, r := range ls {
		lo, err := objekt(r, 5)
		if err != nil {
			return s, fmt.Errorf("lanes[%d]: %v", i, err)
		}
		if len(lo) != 4 {
			return s, fmt.Errorf("lanes[%d]: erwartet genau id, titel, kind, archived", i)
		}
		var l StrukturLane
		var ok bool
		if l.ID, ok = text(lo["id"]); !ok || !GueltigeLaneKennung(l.ID) {
			return s, fmt.Errorf("lanes[%d].id: erwartet eine Kennung (43 Zeichen base64url) oder __backlog__", i)
		}
		if gesehen[l.ID] {
			return s, fmt.Errorf("lanes[%d].id: doppelt", i)
		}
		gesehen[l.ID] = true
		if l.Titel, ok = text(lo["titel"]); !ok || StrukturTitel(l.Titel, LaneTitelMax) != l.Titel {
			return s, fmt.Errorf("lanes[%d].titel: Steuerzeichen, Ränder oder Länge verletzt", i)
		}
		if l.Kind, ok = text(lo["kind"]); !ok || !laneArten[l.Kind] || (l.Kind == "backlog") != (l.ID == BacklogLane) {
			return s, fmt.Errorf("lanes[%d].kind: erlaubt sind sprint, lane und backlog (nur für __backlog__)", i)
		}
		if l.Archived, ok = wahrheitswert(lo["archived"]); !ok {
			return s, fmt.Errorf("lanes[%d].archived: erwartet true oder false", i)
		}
		backlog = backlog || l.ID == BacklogLane
		s.Lanes = append(s.Lanes, l)
	}
	if !backlog {
		return s, errors.New("lanes: __backlog__ fehlt")
	}
	ss, err := elemente(o["spalten"], MaxSpalten)
	if err != nil {
		return s, fmt.Errorf("spalten: %v", err)
	}
	s.Spalten = make([]StrukturSpalte, 0, len(ss))
	gesehen = map[string]bool{}
	for i, r := range ss {
		so, err := objekt(r, 3)
		if err != nil {
			return s, fmt.Errorf("spalten[%d]: %v", i, err)
		}
		if len(so) != 2 {
			return s, fmt.Errorf("spalten[%d]: erwartet genau id und titel", i)
		}
		var sp StrukturSpalte
		var ok bool
		if sp.ID, ok = text(so["id"]); !ok || !GueltigeSpaltenKennung(sp.ID) {
			return s, fmt.Errorf("spalten[%d].id: erwartet eine Kennung (43 Zeichen base64url) oder eine Standardspalte", i)
		}
		if gesehen[sp.ID] {
			return s, fmt.Errorf("spalten[%d].id: doppelt", i)
		}
		gesehen[sp.ID] = true
		if sp.Titel, ok = text(so["titel"]); !ok || StrukturTitel(sp.Titel, SpalteTitelMax) != sp.Titel {
			return s, fmt.Errorf("spalten[%d].titel: Steuerzeichen, Ränder oder Länge verletzt", i)
		}
		s.Spalten = append(s.Spalten, sp)
	}
	return s, nil
}

func liesSchattenV2(roh json.RawMessage, lanes map[string]bool, spalten map[string]string) (Eingang, error) {
	var e Eingang
	o, err := objekt(roh, len(felderKarteV2)+2)
	if err != nil {
		return e, err
	}
	if id, ok := text(o["schattenId"]); ok && GueltigeSchattenID(id) {
		e.SchattenID = id
	}
	art, ok := text(o["art"])
	if !ok || (art != ArtKarte && art != ArtTermin) {
		return e, errors.New("art: erlaubt sind karte und termin")
	}
	e.Art = art
	felder := felderTerminV2
	if art == ArtKarte {
		felder = felderKarteV2
	}
	for k := range o {
		if !felder[k] && !(art == ArtKarte && k == "nr") {
			return e, fmt.Errorf("unbekannter Schlüssel %q für art=%s", kurz(k), art)
		}
	}
	for k := range felder {
		if _, ok := o[k]; !ok {
			return e, fmt.Errorf("%s fehlt", k)
		}
	}
	if e.SchattenID == "" {
		return e, errors.New("schattenId: erwartet 43 Zeichen base64url")
	}
	if roh, ok := o["nr"]; ok {
		var n int64
		if istNull(roh) || json.Unmarshal(roh, &n) != nil || n < 1 || n > math.MaxInt32 {
			return e, errors.New("nr: erwartet eine ganze Zahl ab 1")
		}
		nr := int(n)
		e.Nr = &nr
	}
	if e.Vertraulich, ok = wahrheitswert(o["vertraulich"]); !ok {
		return e, errors.New("vertraulich: erwartet true oder false")
	}
	// Titel: auch bei vertraulichen Einträgen (Beschluss 3); null nur bei einem
	// nicht baubaren Eintrag, und der ist immer vertraulich.
	if istNull(o["titel"]) {
		if !e.Vertraulich {
			return e, errors.New("titel: null nur bei vertraulich=true")
		}
	} else {
		t, ok := text(o["titel"])
		if !ok || !PruefeTitel(t) {
			return e, errors.New("titel: Zeichenvorrat, Länge oder Muster verletzt")
		}
		e.Titel = &t
	}
	if e.Ursprung, err = liesUrsprung(o["ursprung"]); err != nil {
		return e, err
	}
	if art == ArtKarte {
		lane, ok := text(o["lane"])
		if !ok || !lanes[lane] {
			return e, errors.New("lane: keine Lane der struktur")
		}
		spalte, ok := text(o["spalte"])
		titel, gibtEs := spalten[spalte]
		if !ok || !gibtEs {
			return e, errors.New("spalte: keine Spalte der struktur")
		}
		p, ok := text(o["prioritaet"])
		if !ok || !PruefePrioritaet(p) {
			return e, errors.New("prioritaet: erlaubt sind leer, niedrig, mittel, hoch")
		}
		// Spalte (Name) für die Leser aus Stufe 1 (Werkzeuge, Feed): der Titel
		// der Spalte nach den Textregeln, sonst leer — wie der Ausgang aus Stufe 1.
		name, gut := behandle(titel, SpalteMaxZeichen)
		if !gut {
			name = ""
		}
		e.Lane, e.SpalteID, e.Spalte, e.Prioritaet = &lane, &spalte, &name, &p
	}
	for _, f := range []struct {
		datum, zeit string
		d, z        **string
	}{{"startDatum", "startZeit", &e.StartDatum, &e.StartZeit}, {"endeDatum", "endeZeit", &e.EndeDatum, &e.EndeZeit}} {
		if *f.d, ok = optText(o[f.datum], istDatum); !ok {
			return e, fmt.Errorf("%s: erwartet null oder YYYY-MM-DD", f.datum)
		}
		if *f.z, ok = optText(o[f.zeit], calendar.IstUhrzeit); !ok {
			return e, fmt.Errorf("%s: erwartet null oder HH:MM", f.zeit)
		}
		if *f.z != nil && *f.d == nil {
			return e, fmt.Errorf("%s: Uhrzeit ohne Datum", f.zeit)
		}
	}
	if istNull(o["bearbeiter"]) {
		return e, errors.New("bearbeiter: erwartet eine Liste")
	}
	bearbeiter, err := elemente(o["bearbeiter"], BearbeiterMax)
	if err != nil {
		return e, fmt.Errorf("bearbeiter: %v", err)
	}
	e.Bearbeiter = make([]string, 0, len(bearbeiter))
	for j, b := range bearbeiter {
		s, ok := text(b)
		if !ok || !PruefeBearbeiter(s) {
			return e, fmt.Errorf("bearbeiter[%d]: Zeichenvorrat, Länge, Muster oder @ verletzt", j)
		}
		e.Bearbeiter = append(e.Bearbeiter, s)
	}
	if e.VonAgent, ok = wahrheitswert(o["vonAgent"]); !ok {
		return e, errors.New("vonAgent: erwartet true oder false")
	}
	return e, nil
}

// --- Struktur in den Board-Zustand der Kopie ---------------------------------

// spiegelMarke ist die Marke an Lane und Spalte (ADR-0141 §4, §8).
type spiegelMarke struct {
	ID string `json:"id"`
}

func roh(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// StrukturAnwenden schreibt die Struktur in den Board-Zustand einer Kopie
// (ADR-0141 §4). Lanes und Spalten der Struktur stehen danach in ihrer
// Reihenfolge, mit Titel (Lanes auch kind und archived) und der Marke
// `spiegel: {id: <Kennung>}`; ihre IDs sind die abgeleiteten (Standardspalten
// und Backlog: Klartext). Karten in den Lanes bleiben unangetastet.
//
// Was nicht in der Struktur steht: eine leere Lane oder eine Spalte ohne
// Karten fällt weg; eine Lane mit Karten oder eine Spalte, in der Karten
// liegen, bleibt — ohne Marke, hinten angehängt. Der Eingang löscht nie eine
// Karte.
//
// Ändert sich nichts, kommt state byte-gleich zurück (dann wird nicht
// geschrieben, PK-11).
func StrukturAnwenden(state []byte, s Struktur) ([]byte, error) {
	var stand map[string]json.RawMessage
	if err := json.Unmarshal(state, &stand); err != nil || stand == nil {
		return nil, fmt.Errorf("%w: kein JSON-Objekt", ErrStand)
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(stand["board"], &board); err != nil || board == nil {
		return nil, fmt.Errorf("%w: board fehlt", ErrStand)
	}
	var lanes []map[string]json.RawMessage
	if roh, ok := board["sprints"]; ok && strings.TrimSpace(string(roh)) != "null" {
		if json.Unmarshal(roh, &lanes) != nil {
			return nil, fmt.Errorf("%w: sprints ist keine Liste", ErrStand)
		}
	}
	var defs []map[string]json.RawMessage
	if roh, ok := board["columnDefs"]; ok && strings.TrimSpace(string(roh)) != "null" {
		if json.Unmarshal(roh, &defs) != nil {
			return nil, fmt.Errorf("%w: columnDefs ist keine Liste", ErrStand)
		}
	}

	// Spalten, in denen Karten liegen.
	belegt := map[string]bool{}
	for _, l := range lanes {
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		for _, k := range cards {
			belegt[zeichenkette(k["columnId"])] = true
		}
	}

	// Kritik #825 A-7: Doppelte IDs außen — eine Map über die ID verlöre die
	// Karten der früheren Lane. Ganz oder Fehler, wie BaueV2 innen.
	for _, liste := range [][]map[string]json.RawMessage{defs, lanes} {
		gesehen := map[string]bool{}
		for _, o := range liste {
			if o == nil {
				continue
			}
			id := zeichenkette(o["id"])
			if id == "" {
				continue // keine ID ist keine Dublette (Verify #825 V-3)
			}
			if gesehen[id] {
				return nil, fmt.Errorf("%w: Lane- oder Spalten-ID außen doppelt", ErrStand)
			}
			gesehen[id] = true
		}
	}

	// Spalten.
	altDefs := map[string]map[string]json.RawMessage{}
	for _, d := range defs {
		if d != nil {
			altDefs[zeichenkette(d["id"])] = d
		}
	}
	neueDefs := make([]map[string]json.RawMessage, 0, len(s.Spalten))
	vergeben := map[string]bool{}
	for _, sp := range s.Spalten {
		id := AussenSpalteID(sp.ID)
		d := map[string]json.RawMessage{}
		if alt, ok := altDefs[id]; ok {
			for k, v := range alt {
				d[k] = v
			}
		} else {
			d["color"] = json.RawMessage("null")
		}
		d["id"] = roh(id)
		d["title"] = roh(sp.Titel)
		d["spiegel"] = roh(spiegelMarke{ID: sp.ID})
		vergeben[id] = true
		neueDefs = append(neueDefs, d)
	}
	for _, d := range defs {
		id := zeichenkette(d["id"])
		if d == nil || vergeben[id] || !belegt[id] {
			continue
		}
		vergeben[id] = true
		kopie := map[string]json.RawMessage{}
		for k, v := range d {
			if k != "spiegel" {
				kopie[k] = v
			}
		}
		neueDefs = append(neueDefs, kopie)
	}

	// Lanes.
	altLanes := map[string]map[string]json.RawMessage{}
	for _, l := range lanes {
		if l != nil {
			altLanes[zeichenkette(l["id"])] = l
		}
	}
	neueLanes := make([]map[string]json.RawMessage, 0, len(s.Lanes))
	vergeben = map[string]bool{}
	for _, sl := range s.Lanes {
		id := AussenLaneID(sl.ID)
		l := map[string]json.RawMessage{}
		if alt, ok := altLanes[id]; ok {
			for k, v := range alt {
				l[k] = v
			}
		} else {
			l["cards"] = json.RawMessage("[]")
		}
		if _, ok := l["cards"]; !ok || strings.TrimSpace(string(l["cards"])) == "null" {
			l["cards"] = json.RawMessage("[]")
		}
		l["id"] = roh(id)
		l["title"] = roh(sl.Titel)
		l["kind"] = roh(sl.Kind)
		// Kritik Sprint 93 B-1: archived immer schreiben. Fehlt das Feld,
		// ergänzt es die Browser-Migration (migrateSprintArchived) lokal, und
		// ab 2b lehnte die Spiegel-Schranke den Batch mit dieser Abweichung ab.
		if sl.Archived {
			l["archived"] = json.RawMessage("true")
		} else {
			l["archived"] = json.RawMessage("false")
		}
		l["spiegel"] = roh(spiegelMarke{ID: sl.ID})
		vergeben[id] = true
		neueLanes = append(neueLanes, l)
	}
	for _, l := range lanes {
		id := zeichenkette(l["id"])
		if l == nil || vergeben[id] {
			continue
		}
		var cards []json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		if len(cards) == 0 {
			continue
		}
		// F-2143: Lanes ohne ID nie über vergeben zusammenlegen — sonst fiele
		// die zweite solche Lane über vergeben[""] samt ihren Karten weg.
		// Kaputte Daten (der Browser vergibt IDs), aber kein stiller Verlust.
		if id != "" {
			vergeben[id] = true
		}
		kopie := map[string]json.RawMessage{}
		for k, v := range l {
			if k != "spiegel" {
				kopie[k] = v
			}
		}
		neueLanes = append(neueLanes, kopie)
	}

	// Kritik #826 K-3: Der Backlog steht außen immer zuletzt — wie der Browser
	// ihn beim Laden setzt (migrateBacklogPosition, src/storage.js). Sonst
	// schöbe ihn jeder Browser hinter eine eigene Lane mit Karten (swimlane.move),
	// und der nächste Eingang schriebe ihn wieder davor: Schreiben ohne Ende.
	for i, l := range neueLanes {
		if zeichenkette(l["id"]) == BacklogLane && i != len(neueLanes)-1 {
			neueLanes = append(append(neueLanes[:i:i], neueLanes[i+1:]...), l)
			break
		}
	}

	if gleicheListe(defs, neueDefs) && gleicheListe(lanes, neueLanes) {
		return state, nil
	}
	board["columnDefs"] = roh(neueDefs)
	board["sprints"] = roh(neueLanes)
	stand["board"] = roh(board)
	return json.Marshal(stand)
}

// gleicheListe vergleicht zwei Listen von Objekten inhaltlich (Schlüssel und
// kanonisch kodierte Werte), unabhängig von der Schlüsselreihenfolge.
func gleicheListe(a, b []map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for k, va := range a[i] {
			vb, ok := b[i][k]
			if !ok || !gleicherWert(va, vb) {
				return false
			}
		}
	}
	return true
}

func gleicherWert(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return string(roh(x)) == string(roh(y))
}

// KopieAnwenden schreibt eine geprüfte Hinweg-Menge in den Board-Zustand
// einer Kopie (ADR-0141 §4): Struktur, Spiegel (Karte #826) und — ab Format 3
// — den Titel des Boards (F-2140). Die Struktur läuft zweimal: Eine Lane oder
// Spalte, die innen wegfiel, bleibt beim ersten Mal stehen, solange noch eine
// Spiegelkarte in ihr liegt; nachdem SpiegelAnwenden die Karte umgesetzt oder
// entfernt hat, fällt sie im selben Lauf weg (sonst schriebe erst der nächste
// Lauf, ohne dass innen etwas geschah). Ändert sich nichts, kommt state
// byte-gleich zurück.
func KopieAnwenden(state []byte, k *Kopie) ([]byte, *SpiegelAbgleich, error) {
	return KopieAnwendenMit(state, k, nil)
}

// KopieAnwendenMit wie KopieAnwenden, mit dem Schutz aus dem Journal außen
// (ab 2c, spiegel_mischen.go). schutz.RueckwegBis setzt der Aufrufer aus
// k.RueckwegBis.
func KopieAnwendenMit(state []byte, k *Kopie, schutz *Schutz) ([]byte, *SpiegelAbgleich, error) {
	neu, err := StrukturAnwenden(state, k.Struktur)
	if err != nil {
		return nil, nil, err
	}
	neu, abgleich, err := SpiegelAnwendenMit(neu, k.Spiegel, k.Schatten, schutz)
	if err != nil {
		return nil, nil, err
	}
	if neu, err = StrukturAnwenden(neu, k.Struktur); err != nil {
		return nil, nil, err
	}
	if k.Struktur.Titel != nil {
		if neu, err = titelAnwenden(neu, *k.Struktur.Titel); err != nil {
			return nil, nil, err
		}
	}
	if bytes.Equal(neu, state) {
		return state, abgleich, nil
	}
	return neu, abgleich, nil
}

// titelAnwenden setzt board.title (unverändert → state byte-gleich zurück).
func titelAnwenden(state []byte, titel string) ([]byte, error) {
	var stand map[string]json.RawMessage
	if err := json.Unmarshal(state, &stand); err != nil || stand == nil {
		return nil, fmt.Errorf("%w: kein JSON-Objekt", ErrStand)
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(stand["board"], &board); err != nil || board == nil {
		return nil, fmt.Errorf("%w: board fehlt", ErrStand)
	}
	if istZeichenkette(board["title"]) && zeichenkette(board["title"]) == titel {
		return state, nil
	}
	board["title"] = roh(titel)
	stand["board"] = roh(board)
	return json.Marshal(stand)
}
