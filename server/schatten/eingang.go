package schatten

// eingang.go — Prüfung der Schattenmenge, die der Eingang außen annimmt
// (ADR-0140 §3, Karte #779).
//
// Der Eingang LEHNT AB, er säubert nicht: Was der Ausgang innen erzeugt hat
// und das Skript nur in der Kennung verändert hat, geht durch; alles andere
// ist 422, und dann wird nichts geschrieben. Die Prüfungen sind deshalb als
// Fixpunkt der Ausgangsregeln gebaut (behandle(x) == x), nicht als zweite
// Fassung derselben Regeln — sonst liefen Eingang und Ausgang auseinander.
//
// Gelesen wird über exakte Map-Schlüssel mit einem Token-Decoder: Ein
// Struct-Unmarshal nähme „TITEL“ als „titel“ an, ließe bei doppelten
// Schlüsseln still den letzten gewinnen und unterschiede „fehlt“ nicht von
// null (Vorprüfung #779, P-2, gemessen).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Andiministrator/aBoard/server/calendar"
)

// Grenzen des Eingangs (ADR-0140 §3).
const (
	// EingangMaxSchatten: höchstens so viele Schatten je Board. Die Zählgrenze
	// greift vor der Bytegrenze (EingangMaxBytes, im Handler).
	EingangMaxSchatten = 5000
	// EingangMaxBytes: Body-Grenze des PUT (W-5/X-7/Y-8).
	EingangMaxBytes = 32 << 20
	// SchattenIDZeichen: Länge der HMAC-Kennung (base64url ohne Auffüllung
	// von 32 Byte, Vertrag Push-Skript, Ziffer 9.12).
	SchattenIDZeichen = 43
)

// Eingang ist ein geprüfter Schatten, wie er in die Tabelle schatten geht.
// Für Termine sind Nr, Spalte und Prioritaet nil; Titel ist nil genau dann,
// wenn Vertraulich gilt.
type Eingang struct {
	SchattenID  string
	Art         string
	Nr          *int
	Titel       *string
	Vertraulich bool
	Spalte      *string
	Prioritaet  *string
	StartDatum  *string
	StartZeit   *string
	EndeDatum   *string
	EndeZeit    *string
	Bearbeiter  []string
	VonAgent    bool
	// Lane/SpalteID (ADR-0141 §4, Hinweg v2): Kennung der Lane und der Spalte
	// (bzw. Klartext-ID einer Standardspalte und des Backlogs). Nur Karten aus
	// dem Eingang v2 tragen sie; Stufe 1 lässt sie nil.
	Lane     *string
	SpalteID *string
	// Ursprung (ADR-0141 §2, ab 2c): Außen-ID, wenn die Karte bzw. der Termin
	// außen angelegt wurde. Nur der Eingang v2 liest es; in die Tabelle
	// schatten geht es nicht.
	Ursprung *string
}

// EingangFehler: die Menge ist abgelehnt. Status ist 422 oder 409; Grund
// nennt Stelle und Feld, nie den Wert (Titel sind Inhalt, und die Meldung
// landet im Log des Skripts).
//
// Bei Fehlern in einzelnen Schatten (T16/F-2017) nennt Fehler ALLE betroffenen
// Indizes mit Grund je Index, höchstens EingangMaxFehlerListe; Weitere zählt
// den Rest. Grund bleibt der erste Fehler (rückwärtsverträglich).
type EingangFehler struct {
	Status  int
	Grund   string
	Fehler  []ElementFehler
	Weitere int
}

// ElementFehler: ein abgelehnter Schatten der Liste.
type ElementFehler struct {
	Index int    `json:"index"`
	Grund string `json:"grund"`
}

// EingangMaxFehlerListe: so viele Einzelfehler stehen höchstens in der
// 422-Antwort; der Rest wird nur gezählt.
const EingangMaxFehlerListe = 50

func (f *EingangFehler) Error() string { return f.Grund }

// Antworttext ist der Text des HTTP-Feldes "error": Grund (erster Fehler) und,
// wenn es mehrere gab, der Hinweis auf die Liste "fehler". Der Hinweis gehört
// NICHT in Grund — Grund geht auch in den Merker der Admin-Anzeige, und die
// kennt kein Feld "fehler" (Karte #809, T-9).
func (f *EingangFehler) Antworttext() string {
	if n := f.weitereFehler(); n > 0 {
		return fmt.Sprintf("%s (und %d weitere fehlerhafte Schatten, siehe fehler)", f.Grund, n)
	}
	return f.Grund
}

// Merkertext ist der Text für Merker und Log: Grund und die Zahl der weiteren
// Fehler, aber ohne Verweis auf die Liste "fehler" (V-1, Verify #809).
func (f *EingangFehler) Merkertext() string {
	if n := f.weitereFehler(); n > 0 {
		return fmt.Sprintf("%s (und %d weitere fehlerhafte Schatten)", f.Grund, n)
	}
	return f.Grund
}

// weitereFehler: Zahl der fehlerhaften Schatten nach dem ersten.
func (f *EingangFehler) weitereFehler() int { return f.Weitere + len(f.Fehler) - 1 }

func abgelehnt(format string, a ...any) *EingangFehler {
	return &EingangFehler{Status: 422, Grund: fmt.Sprintf(format, a...)}
}

// --- Prüffunktionen (Fixpunkte der Ausgangsregeln) ---------------------------

// PruefeTitel: Der Titel ist genau so, wie der Ausgang ihn erzeugt — Ergebnis
// der zweiten Titel-Schicht oder der Ersatztext.
func PruefeTitel(s string) bool {
	if s == Ersatztext {
		return true
	}
	t, ok := behandle(s, TitelMaxZeichen)
	return ok && t == s
}

// PruefeSpalte: leer (unbekannte Spalte) oder ein Spaltenname nach den
// Titelregeln mit höchstens 40 Zeichen.
func PruefeSpalte(s string) bool {
	if s == "" {
		return true
	}
	t, ok := behandle(s, SpalteMaxZeichen)
	return ok && t == s
}

// PruefeBearbeiter: ein Anzeigename nach den Titelregeln mit höchstens 60
// Zeichen, nie eine E-Mail-Adresse oder externe Person.
func PruefeBearbeiter(s string) bool {
	if !bearbeiterZulaessig(s) {
		return false
	}
	t, ok := behandle(s, BearbeiterMaxZeichen)
	return ok && t == s
}

// PruefePrioritaet: leer oder niedrig, mittel, hoch.
func PruefePrioritaet(s string) bool {
	return s == "" || Prioritaet(s) == s
}

// IstDatum: YYYY-MM-DD und ein Kalendertag.
func IstDatum(s string) bool { return istDatum(s) }

// GueltigeSchattenID: 43 Zeichen base64url. Eine UUID (36 Zeichen) fällt
// schon an der Länge durch — die interne Kennung darf nicht nach außen
// (Sicherung aus dem Vertrag Push-Skript, K-19).
func GueltigeSchattenID(s string) bool {
	return len(s) == SchattenIDZeichen && GueltigeKennung(s)
}

// --- Version -----------------------------------------------------------------

var semver = regexp.MustCompile(`^v?(\d+)\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]*)?$`)

// versionFrei: Zeichen einer Versionsangabe (Tag, Commit-Kürzel, „dev“).
var versionFrei = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,64}$`)

// Hauptversion liefert die Hauptversion einer SemVer-Angabe (mit oder ohne
// „v“). ok = false für alles andere, etwa ein Commit-Kürzel oder „dev“.
func Hauptversion(v string) (int, bool) {
	m := semver.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// --- Lesen -------------------------------------------------------------------

// objekt liest ein JSON-Objekt Schlüssel für Schlüssel und lehnt doppelte
// Schlüssel ab. Gross-/Kleinschreibung zählt (TITEL ≠ titel). Mehr als
// maxSchluessel Schlüssel brechen beim Lesen ab — eine Schlüsselflut legte
// sonst erst die ganze Map an (Sitzungs-QA #779 C-1, Klasse 214).
func objekt(roh []byte, maxSchluessel int) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(roh))
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("kein JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("kein Objekt")
	}
	aus := map[string]json.RawMessage{}
	for dec.More() {
		if len(aus) == maxSchluessel {
			return nil, fmt.Errorf("mehr als %d Schlüssel", maxSchluessel)
		}
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("kein JSON")
		}
		k, _ := tok.(string)
		if _, schon := aus[k]; schon {
			return nil, fmt.Errorf("Schlüssel %q doppelt", kurz(k))
		}
		var wert json.RawMessage
		if err := dec.Decode(&wert); err != nil {
			return nil, errors.New("kein JSON")
		}
		aus[k] = wert
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("kein JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("Daten hinter dem Objekt")
	}
	return aus, nil
}

// elemente liest eine JSON-Liste Element für Element und bricht beim
// Element max+1 ab. Ein json.Unmarshal in []json.RawMessage legte erst ALLE
// Elemente an und zählte danach — 32 MB aus „0,0,0,…“ sind 16,7 Mio.
// Elemente, gemessen 2,9 GB Allokation je Anfrage (Kritiker #779, K-1).
func elemente(roh json.RawMessage, max int) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(roh))
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("kein JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errors.New("keine Liste")
	}
	aus := []json.RawMessage{}
	for dec.More() {
		if len(aus) == max {
			return nil, fmt.Errorf("mehr als %d Elemente", max)
		}
		var e json.RawMessage
		if err := dec.Decode(&e); err != nil {
			return nil, errors.New("kein JSON")
		}
		aus = append(aus, e)
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("kein JSON")
	}
	return aus, nil
}

func istNull(roh json.RawMessage) bool {
	return strings.TrimSpace(string(roh)) == "null"
}

// text: eine JSON-Zeichenkette (null ist keine).
func text(roh json.RawMessage) (string, bool) {
	if istNull(roh) {
		return "", false
	}
	var s string
	if json.Unmarshal(roh, &s) != nil {
		return "", false
	}
	return s, true
}

// wahrheitswert: nur die Literale true und false.
func wahrheitswert(roh json.RawMessage) (bool, bool) {
	switch strings.TrimSpace(string(roh)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// optText: null oder eine Zeichenkette, die pruef besteht.
func optText(roh json.RawMessage, pruef func(string) bool) (*string, bool) {
	if istNull(roh) {
		return nil, true
	}
	s, ok := text(roh)
	if !ok || !pruef(s) {
		return nil, false
	}
	return &s, true
}

// felderKarte / felderTermin: der Feldsatz aus §2 mit schattenId statt id.
// nr ist bei Karten optional — der Ausgang lässt es weg, wenn die Karte keine
// Nummer trägt. Termine tragen nr, spalte und prioritaet nie, auch nicht als
// null (der Ausgang lässt sie weg).
var (
	felderKarte = map[string]bool{
		"schattenId": true, "art": true, "titel": true, "vertraulich": true, "spalte": true, "prioritaet": true,
		"startDatum": true, "startZeit": true, "endeDatum": true, "endeZeit": true, "bearbeiter": true, "vonAgent": true,
	}
	felderTermin = map[string]bool{
		"schattenId": true, "art": true, "titel": true, "vertraulich": true,
		"startDatum": true, "startZeit": true, "endeDatum": true, "endeZeit": true, "bearbeiter": true, "vonAgent": true,
	}
)

// LiesEingang prüft den Body des PUT und liefert die Menge. Ein Fehler ist
// immer ein *EingangFehler (422, bei abweichender Hauptversion 409); dann
// darf nichts geschrieben werden. eigeneVersion ist die Version dieses
// Servers — ist sie keine SemVer-Angabe (Entwicklungsstand, Build ohne Tag),
// entfällt der Vergleich, ebenso bei einer gesendeten Angabe ohne SemVer.
func LiesEingang(body []byte, eigeneVersion string) ([]Eingang, error) {
	huelle, err := objekt(body, 3)
	if err != nil {
		return nil, abgelehnt("Hülle: %v", err)
	}
	for k := range huelle {
		if k != "version" && k != "schatten" {
			return nil, abgelehnt("Hülle: unbekannter Schlüssel %q (erlaubt sind version und schatten)", kurz(k))
		}
	}
	vroh, ok := huelle["version"]
	if !ok {
		return nil, abgelehnt("Hülle: version fehlt")
	}
	version, ok := text(vroh)
	if !ok || !versionFrei.MatchString(version) {
		return nil, abgelehnt("Hülle: version ist keine Versionsangabe")
	}
	if ihre, ok := Hauptversion(version); ok {
		if meine, ok := Hauptversion(eigeneVersion); ok && meine != ihre {
			return nil, &EingangFehler{Status: 409, Grund: fmt.Sprintf(
				"Hauptversion %d weicht von der eigenen (%d) ab — Push anhalten bis zum Gleichstand", ihre, meine)}
		}
	}
	sroh, ok := huelle["schatten"]
	if !ok || istNull(sroh) {
		return nil, abgelehnt("Hülle: schatten fehlt")
	}
	liste, err := elemente(sroh, EingangMaxSchatten)
	if err != nil {
		return nil, abgelehnt("Hülle: schatten: %v", err)
	}
	aus := make([]Eingang, 0, len(liste))
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
		e, err := liesSchatten(roh)
		if err != nil {
			// Auch ein fehlerhafter Schatten belegt seine Kennung, wenn sie
			// lesbar und gültig war: Sonst bliebe ein späteres Doppel
			// unbemerkt, solange das erste Vorkommen selbst fehlerhaft ist
			// (Kritiker #809, T-4).
			if id := e.SchattenID; GueltigeSchattenID(id) {
				if j, schon := gesehen[id]; schon {
					melde(i, fmt.Sprintf("%s; schattenId wie schatten[%d]", err.Error(), j))
					continue
				}
				gesehen[id] = i
			}
			melde(i, err.Error())
			continue
		}
		if j, schon := gesehen[e.SchattenID]; schon {
			melde(i, fmt.Sprintf("schattenId wie schatten[%d]", j))
			continue
		}
		gesehen[e.SchattenID] = i
		aus = append(aus, e)
	}
	if anzahl > 0 {
		f := abgelehnt("schatten[%d]: %s", fehler[0].Index, fehler[0].Grund)
		f.Fehler, f.Weitere = fehler, anzahl-len(fehler)
		return nil, f
	}
	return aus, nil
}

// kurz: ein fremder Schlüssel fürs Log, gekürzt.
func kurz(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func liesSchatten(roh json.RawMessage) (Eingang, error) {
	var e Eingang
	o, err := objekt(roh, len(felderKarte)+2)
	if err != nil {
		return e, err
	}
	// Eine gültige Kennung steht auch im Rückgabewert eines fehlerhaften
	// Eintrags (LiesEingang merkt sie sich, T-4) — ohne ein zweites Parsen.
	if id, ok := text(o["schattenId"]); ok && GueltigeSchattenID(id) {
		e.SchattenID = id
	}
	art, ok := text(o["art"])
	if !ok || (art != ArtKarte && art != ArtTermin) {
		return e, errors.New("art: erlaubt sind karte und termin")
	}
	e.Art = art
	felder := felderTermin
	if art == ArtKarte {
		felder = felderKarte
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

	if e.SchattenID, ok = text(o["schattenId"]); !ok || !GueltigeSchattenID(e.SchattenID) {
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
	if e.Vertraulich {
		if !istNull(o["titel"]) {
			return e, errors.New("titel: muss bei vertraulich=true null sein")
		}
	} else {
		t, ok := text(o["titel"])
		if !ok || !PruefeTitel(t) {
			return e, errors.New("titel: Zeichenvorrat, Länge oder Muster verletzt")
		}
		e.Titel = &t
	}
	if art == ArtKarte {
		s, ok := text(o["spalte"])
		if !ok || !PruefeSpalte(s) {
			return e, errors.New("spalte: Zeichenvorrat oder Länge verletzt")
		}
		p, ok := text(o["prioritaet"])
		if !ok || !PruefePrioritaet(p) {
			return e, errors.New("prioritaet: erlaubt sind leer, niedrig, mittel, hoch")
		}
		e.Spalte, e.Prioritaet = &s, &p
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
	// Element für Element mit Grenze (C-1): ein json.Unmarshal legte bei
	// „[0,0,…]“ über 31 MB erst alle Elemente an — rund 2,7 GB (gemessen 2731 MiB) je Anfrage.
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
