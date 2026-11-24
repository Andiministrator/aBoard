package schatten

// spiegel.go — Board-Paar Stufe 2b (ADR-0141 §4, §7, §9; Karte #826): Spiegel
// nicht vertraulicher Karten und Termine.
//
// Innen baut baueSpiegel (über BaueV2Mit, Hinweg im Format 3) aus dem
// Board-Zustand die Spiegel: eine feste Feldliste je Karte und Termin, die
// Werte unverändert (§4 „keine Kürzung, keine Ersatztexte“). Vertrauliche
// Karten und Termine bleiben Schatten.
//
// Außen prüft liesSpiegel jeden Spiegel des PUT exakt, und SpiegelAnwenden
// schreibt die Menge in den Board-Zustand der Kopie: Karten mit der Marke
// `spiegel: {id}` in ihrer Lane und Spalte, Termine mit derselben Marke in
// board.termine. Markierte, die nicht mehr in der Menge stehen, fallen weg
// (innen gelöscht oder vertraulich geworden, §9); unmarkierte bleiben
// unberührt (§4: „entfernt nie eine Karte ohne Marke“).
//
// SpiegelVerstoesse ist die Spiegel-Schranke (§7) in der Fassung von 2b: Jede
// Änderung an einem Spiegel-Objekt ist ein Verstoß (Schreibschutz bis 2c).
// Die drei Schreibstellen (Ops, Ganz-PUT, Transfer) prüfen damit das Ergebnis
// gegen den Stand davor.
//
// Die Checkliste ist in aBoard kein eigenes Feld, sondern die Zeilen
// „- [ ] …“ der Beschreibung (src/markdown.js, checkliste_abhaken). Sie geht
// mit der Beschreibung hinaus; ein eigenes Feld „Checkliste (Punkte mit ID)“
// wie in §4 gibt es nicht (Abweichung, festgehalten in ADR-0141 „Umsetzung
// Schritt 2b“; für den Rückweg in 2c offen).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Andiministrator/aBoard/server/calendar"
)

// Grenzen eines Spiegels (Schutz vor krankhaften Mengen; die eigentliche
// Grenze ist die Größe des Boards, MaxBodyBytes am Server).
const (
	SpiegelListeMax      = 1000  // labels, assignees, personen
	SpiegelKommentareMax = 10000 // Kommentare je Karte
	SpiegelPositionMax   = 1_000_000
)

// Herkunft eines Kommentars (§4): „aussen“ = über das Board-Paar von außen
// gekommen (Autor „extern: <Name>“, §5.3), sonst „innen“.
const (
	HerkunftInnen      = "innen"
	HerkunftAussen     = "aussen"
	autorPraefixExtern = "extern: "
)

// SpiegelKommentar ist ein Kommentar im Spiegel. Bewusst NICHT dabei:
// authorEmail (Server-Stempel), kind (Ausgangsschleuse), Anhänge.
type SpiegelKommentar struct {
	ID       string  `json:"id"`
	Author   string  `json:"author"`
	Text     string  `json:"text"`
	Created  string  `json:"created"`
	Edited   *string `json:"edited"`
	Herkunft string  `json:"herkunft"`
}

// SpiegelKarte ist eine Karte im Hinweg (Format 3). Die Feldnamen sind die
// der Karte, damit der Rückweg (2c) sie ohne Übersetzung vergleichen kann.
// id, lane und spalte sind innere IDs; das Skript ersetzt sie durch
// Kennungen (id → spiegelId). position ist der Index in der Kartenliste der
// Lane innen.
type SpiegelKarte struct {
	ID          string             `json:"id"`
	Art         string             `json:"art"`
	Lane        string             `json:"lane"`
	Spalte      string             `json:"spalte"`
	Position    int                `json:"position"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Priority    *string            `json:"priority"`
	Critical    bool               `json:"critical"`
	Blocked     bool               `json:"blocked"`
	Labels      []string           `json:"labels"`
	Owner       string             `json:"owner"`
	Assignees   []string           `json:"assignees"`
	StartDate   *string            `json:"startDate"`
	StartTime   *string            `json:"startTime"`
	DueDate     *string            `json:"dueDate"`
	DueTime     *string            `json:"dueTime"`
	Archived    bool               `json:"archived"`
	Comments    []SpiegelKommentar `json:"comments"`
	Anhaenge    int                `json:"anhaenge"`
	Ursprung    *string            `json:"ursprung"`
}

// SpiegelTermin ist ein Termin im Hinweg (Format 3), Feldnamen wie in
// board.termine. karte ist die innere ID der Karte — nur, wenn diese Karte
// selbst als Spiegel hinausgeht (§4), sonst null.
type SpiegelTermin struct {
	ID           string   `json:"id"`
	Art          string   `json:"art"`
	Titel        string   `json:"titel"`
	Beschreibung *string  `json:"beschreibung"`
	Datum        string   `json:"datum"`
	BisDatum     *string  `json:"bisDatum"`
	Von          *string  `json:"von"`
	Bis          *string  `json:"bis"`
	Ort          *string  `json:"ort"`
	Personen     []string `json:"personen"`
	Karte        *string  `json:"karte"`
	Ursprung     *string  `json:"ursprung"`
}

// --- Hinweg (innen) ------------------------------------------------------------

func optZeichenkette(roh json.RawMessage) *string {
	if !istZeichenkette(roh) || istNull(roh) {
		return nil
	}
	s := zeichenkette(roh)
	return &s
}

// nurZeichenketten: die Zeichenketten einer Liste in ihrer Reihenfolge, alles
// andere fällt weg (nie null, höchstens SpiegelListeMax).
func nurZeichenketten(roh json.RawMessage) []string {
	aus := []string{}
	var l []json.RawMessage
	if json.Unmarshal(roh, &l) != nil {
		return aus
	}
	for _, e := range l {
		if len(aus) == SpiegelListeMax {
			break
		}
		if istZeichenkette(e) && !istNull(e) {
			aus = append(aus, zeichenkette(e))
		}
	}
	return aus
}

func baueKommentare(roh json.RawMessage) []SpiegelKommentar {
	aus := []SpiegelKommentar{}
	var l []map[string]json.RawMessage
	if json.Unmarshal(roh, &l) != nil {
		return aus
	}
	for _, c := range l {
		if len(aus) == SpiegelKommentareMax {
			break
		}
		if c == nil || !GueltigeKennung(zeichenkette(c["id"])) {
			continue
		}
		k := SpiegelKommentar{ID: zeichenkette(c["id"]), Author: zeichenkette(c["author"]),
			Text: zeichenkette(c["text"]), Created: zeichenkette(c["created"]), Edited: optZeichenkette(c["edited"]),
			Herkunft: HerkunftInnen}
		if strings.HasPrefix(k.Author, autorPraefixExtern) {
			k.Herkunft = HerkunftAussen
		}
		aus = append(aus, k)
	}
	return aus
}

func anzahlListe(roh json.RawMessage) int {
	var l []json.RawMessage
	if json.Unmarshal(roh, &l) != nil {
		return 0
	}
	return len(l)
}

// ursprungVon: boardpaarUrsprung, wenn es eine gültige Kennung ist (ab 2c).
func ursprungVon(roh json.RawMessage) *string {
	if s := zeichenkette(roh); GueltigeKennung(s) {
		return &s
	}
	return nil
}

// spiegelErgebnis: was baueSpiegel liefert.
type spiegelErgebnis struct {
	karten  []SpiegelKarte
	termine []SpiegelTermin
	// gesetzt: Kennungen (Art + innere ID) der Spiegel — sie gehen nicht
	// zusätzlich als Schatten hinaus.
	gesetzt map[string]bool
	// ohneOrt/ohneLane/ohneSpalte: archivierte Karten ohne Ort (die übrigen
	// zählt BaueV2 schon bei den Schatten).
	ohneOrt, ohneLane, ohneSpalte int
}

func spiegelSchluessel(art, id string) string { return art + ":" + id }

// baueSpiegel: die Spiegel eines Board-Zustands. lanesAus: Lanes, die
// hinausgehen (Struktur); spalten: Spalten der Struktur.
//
// Auswahl der Karten: wie die Schatten (keine Sprint-Meta-Karten, keine
// Karten in archivierten Lanes oder Service-Lanes), dazu aber auch
// archivierte Karten (archived = true): Außen soll „Archiv anzeigen“ sie
// zeigen, und ab 2c kann man außen archivieren und zurückholen (Beschluss 6).
// Nicht als Spiegel gehen: vertrauliche Karten (auch eine doppelte Kennung
// gilt als vertraulich, wie bei Baue), Karten ohne gültige Kennung und
// Karten, deren Titel keine Zeichenkette ist (nicht baubar) — sie bleiben
// Schatten oder fehlen wie in Stufe 1.
//
// Termine: nicht vertrauliche mit gültigem Datum und Titel; ein Termin, dessen
// Karte ausgenommen (archiviert, Meta, archivierte Lane) ist, fehlt wie bei den
// Schatten.
func baueSpiegel(board map[string]json.RawMessage, lanes []map[string]json.RawMessage,
	lanesAus map[string]bool, spalten map[string]bool) spiegelErgebnis {
	erg := spiegelErgebnis{karten: []SpiegelKarte{}, termine: []SpiegelTermin{}, gesetzt: map[string]bool{}}

	// Vertraulichkeit und doppelte Kennungen wie in Baue.
	vorkommen := map[string]int{}
	vertraulich := map[string]bool{}
	ausgenommen := map[string]bool{}
	for _, l := range lanes {
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		laneAus := jsWahr(l["archived"]) || zeichenkette(l["kind"]) == "service"
		for _, k := range cards {
			if k == nil {
				continue
			}
			if id := zeichenkette(k["id"]); id != "" {
				vorkommen[id]++
				vertraulich[id] = vertraulich[id] || Vertraulich(k)
				aus := laneAus || jsWahr(k["archived"]) || zeichenkette(k["kind"]) == "sprint-meta"
				if vorher, schon := ausgenommen[id]; schon {
					ausgenommen[id] = vorher && aus
				} else {
					ausgenommen[id] = aus
				}
			}
		}
	}
	for id, n := range vorkommen {
		if n > 1 {
			vertraulich[id] = true
		}
	}

	for _, l := range lanes {
		if l == nil || jsWahr(l["archived"]) || zeichenkette(l["kind"]) == "service" {
			continue
		}
		laneID := zeichenkette(l["id"])
		var cards []map[string]json.RawMessage
		_ = json.Unmarshal(l["cards"], &cards)
		for pos, k := range cards {
			if k == nil {
				continue
			}
			id := zeichenkette(k["id"])
			if !GueltigeKennung(id) || vertraulich[id] || zeichenkette(k["kind"]) == "sprint-meta" {
				continue
			}
			if roh, ok := k["title"]; ok && !istZeichenkette(roh) {
				continue // nicht baubar → bleibt Schatten
			}
			archiviert := jsWahr(k["archived"])
			spalte := zeichenkette(k["columnId"])
			if !lanesAus[laneID] || !spalten[spalte] {
				// Nicht archivierte zählt BaueV2 bei den Schatten.
				if archiviert {
					erg.ohneOrt++
					if !lanesAus[laneID] {
						erg.ohneLane++
					} else {
						erg.ohneSpalte++
					}
				}
				continue
			}
			s := SpiegelKarte{ID: id, Art: ArtKarte, Lane: laneID, Spalte: spalte, Position: pos,
				Title: zeichenkette(k["title"]), Description: zeichenkette(k["description"]),
				Priority: optZeichenkette(k["priority"]), Critical: echtWahr(k["critical"]), Blocked: echtWahr(k["blocked"]),
				Labels: nurZeichenketten(k["labels"]), Owner: zeichenkette(k["owner"]), Assignees: nurZeichenketten(k["assignees"]),
				Archived: archiviert, Comments: baueKommentare(k["comments"]), Anhaenge: anzahlListe(k["attachments"]),
				Ursprung: ursprungVon(k["boardpaarUrsprung"])}
			s.StartDate, s.StartTime = datumZeit(zeichenkette(k["startDate"]), zeichenkette(k["startTime"]))
			s.DueDate, s.DueTime = datumZeit(zeichenkette(k["dueDate"]), zeichenkette(k["dueTime"]))
			erg.karten = append(erg.karten, s)
			erg.gesetzt[spiegelSchluessel(ArtKarte, id)] = true
		}
	}

	var termine []map[string]json.RawMessage
	_ = json.Unmarshal(board["termine"], &termine)
	terminVorkommen := map[string]int{}
	for _, t := range termine {
		if t != nil {
			terminVorkommen[zeichenkette(t["id"])]++
		}
	}
	for _, t := range termine {
		if t == nil {
			continue
		}
		id := zeichenkette(t["id"])
		kid := zeichenkette(t["karteId"])
		if !GueltigeKennung(id) || terminVorkommen[id] > 1 || (kid != "" && ausgenommen[kid] && vorkommen[kid] > 0) {
			continue
		}
		if TerminWirksamVertraulich(Vertraulich(t), kid, vertraulich) {
			continue
		}
		titel, ok := t["titel"]
		datum := zeichenkette(t["datum"])
		if !ok || !istZeichenkette(titel) || strings.TrimSpace(zeichenkette(titel)) == "" || !istDatum(datum) {
			continue
		}
		s := SpiegelTermin{ID: id, Art: ArtTermin, Titel: zeichenkette(titel), Beschreibung: optZeichenkette(t["beschreibung"]),
			Datum: datum, Ort: optZeichenkette(t["ort"]), Personen: nurZeichenketten(t["personen"]),
			Ursprung: ursprungVon(t["boardpaarUrsprung"])}
		if b := zeichenkette(t["bisDatum"]); istDatum(b) && b >= datum {
			s.BisDatum = &b
		}
		if v := zeichenkette(t["von"]); calendar.IstUhrzeit(v) {
			s.Von = &v
		}
		if b := zeichenkette(t["bis"]); calendar.IstUhrzeit(b) {
			s.Bis = &b
		}
		if kid != "" && erg.gesetzt[spiegelSchluessel(ArtKarte, kid)] {
			k := kid
			s.Karte = &k
		}
		erg.termine = append(erg.termine, s)
		erg.gesetzt[spiegelSchluessel(ArtTermin, id)] = true
	}
	return erg
}

// --- Eingang (außen) ------------------------------------------------------------

// SpiegelEingang ist ein geprüfter Spiegel aus dem PUT. Genau eines von
// Karte und Termin ist gesetzt; SpiegelID ist die Kennung.
type SpiegelEingang struct {
	SpiegelID string
	Karte     *SpiegelKarte
	Termin    *SpiegelTermin
}

var (
	felderSpiegelKarte = map[string]bool{
		"spiegelId": true, "art": true, "lane": true, "spalte": true, "position": true, "title": true,
		"description": true, "priority": true, "critical": true, "blocked": true, "labels": true, "owner": true,
		"assignees": true, "startDate": true, "startTime": true, "dueDate": true, "dueTime": true, "archived": true,
		"comments": true, "anhaenge": true, "ursprung": true,
	}
	felderSpiegelTermin = map[string]bool{
		"spiegelId": true, "art": true, "titel": true, "beschreibung": true, "datum": true, "bisDatum": true,
		"von": true, "bis": true, "ort": true, "personen": true, "karte": true, "ursprung": true,
	}
	felderSpiegelKommentar = map[string]bool{
		"id": true, "author": true, "text": true, "created": true, "edited": true, "herkunft": true,
	}
)

// textListe: eine Liste von Zeichenketten (nie null), höchstens max.
func textListe(roh json.RawMessage, max int) ([]string, error) {
	if istNull(roh) {
		return nil, errors.New("erwartet eine Liste")
	}
	l, err := elemente(roh, max)
	if err != nil {
		return nil, err
	}
	aus := make([]string, 0, len(l))
	for j, e := range l {
		s, ok := text(e)
		if !ok {
			return nil, fmt.Errorf("[%d]: erwartet eine Zeichenkette", j)
		}
		aus = append(aus, s)
	}
	return aus, nil
}

// ganzeZahl: eine ganze Zahl in [0, max].
func ganzeZahl(roh json.RawMessage, max int) (int, bool) {
	var f float64
	if istNull(roh) || json.Unmarshal(roh, &f) != nil || f < 0 || f > float64(max) || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// liesSpiegel prüft einen Spiegel des PUT. lanes/spalten: Kennungen der
// Struktur.
func liesSpiegel(roh json.RawMessage, lanes map[string]bool, spalten map[string]string) (SpiegelEingang, error) {
	var e SpiegelEingang
	o, err := objekt(roh, len(felderSpiegelKarte)+1)
	if err != nil {
		return e, err
	}
	if id, ok := text(o["spiegelId"]); ok && GueltigeSchattenID(id) {
		e.SpiegelID = id
	}
	art, ok := text(o["art"])
	if !ok || (art != ArtKarte && art != ArtTermin) {
		return e, errors.New("art: erlaubt sind karte und termin")
	}
	felder := felderSpiegelTermin
	if art == ArtKarte {
		felder = felderSpiegelKarte
	}
	for k := range o {
		if !felder[k] {
			return e, fmt.Errorf("unbekannter Schlüssel %q für art=%s", kurz(k), art)
		}
	}
	for k := range felder {
		if _, ok := o[k]; !ok {
			return e, fmt.Errorf("%s fehlt", k)
		}
	}
	if e.SpiegelID == "" {
		return e, errors.New("spiegelId: erwartet 43 Zeichen base64url")
	}
	ursprung, err := liesUrsprung(o["ursprung"])
	if err != nil {
		return e, err
	}
	datumOderNull := func(f string) (*string, error) {
		d, ok := optText(o[f], istDatum)
		if !ok {
			return nil, fmt.Errorf("%s: erwartet null oder YYYY-MM-DD", f)
		}
		return d, nil
	}
	zeitOderNull := func(f string) (*string, error) {
		z, ok := optText(o[f], calendar.IstUhrzeit)
		if !ok {
			return nil, fmt.Errorf("%s: erwartet null oder HH:MM", f)
		}
		return z, nil
	}
	if art == ArtTermin {
		t := &SpiegelTermin{Art: ArtTermin}
		if t.Titel, ok = text(o["titel"]); !ok || strings.TrimSpace(t.Titel) == "" {
			return e, errors.New("titel: erwartet einen nicht leeren Text")
		}
		if t.Beschreibung, ok = optText(o["beschreibung"], func(string) bool { return true }); !ok {
			return e, errors.New("beschreibung: erwartet null oder einen Text")
		}
		if t.Ort, ok = optText(o["ort"], func(string) bool { return true }); !ok {
			return e, errors.New("ort: erwartet null oder einen Text")
		}
		if t.Datum, ok = text(o["datum"]); !ok || !istDatum(t.Datum) {
			return e, errors.New("datum: erwartet YYYY-MM-DD")
		}
		if t.BisDatum, err = datumOderNull("bisDatum"); err != nil {
			return e, err
		}
		if t.BisDatum != nil && *t.BisDatum < t.Datum {
			return e, errors.New("bisDatum: liegt vor datum")
		}
		if t.Von, err = zeitOderNull("von"); err != nil {
			return e, err
		}
		if t.Bis, err = zeitOderNull("bis"); err != nil {
			return e, err
		}
		if t.Personen, err = textListe(o["personen"], SpiegelListeMax); err != nil {
			return e, fmt.Errorf("personen: %v", err)
		}
		if !istNull(o["karte"]) {
			k, ok := text(o["karte"])
			if !ok || !GueltigeSchattenID(k) {
				return e, errors.New("karte: erwartet null oder die spiegelId einer Karte")
			}
			t.Karte = &k
		}
		t.Ursprung = ursprung
		e.Termin = t
		return e, nil
	}

	k := &SpiegelKarte{Art: ArtKarte, Ursprung: ursprung}
	if k.Lane, ok = text(o["lane"]); !ok || !lanes[k.Lane] {
		return e, errors.New("lane: keine Lane der struktur")
	}
	if k.Spalte, ok = text(o["spalte"]); !ok {
		return e, errors.New("spalte: keine Spalte der struktur")
	}
	if _, gibtEs := spalten[k.Spalte]; !gibtEs {
		return e, errors.New("spalte: keine Spalte der struktur")
	}
	if k.Position, ok = ganzeZahl(o["position"], SpiegelPositionMax); !ok {
		return e, fmt.Errorf("position: erwartet eine ganze Zahl von 0 bis %d", SpiegelPositionMax)
	}
	if k.Title, ok = text(o["title"]); !ok {
		return e, errors.New("title: erwartet einen Text")
	}
	if k.Description, ok = text(o["description"]); !ok {
		return e, errors.New("description: erwartet einen Text")
	}
	if k.Priority, ok = optText(o["priority"], func(string) bool { return true }); !ok {
		return e, errors.New("priority: erwartet null oder einen Text")
	}
	if k.Critical, ok = wahrheitswert(o["critical"]); !ok {
		return e, errors.New("critical: erwartet true oder false")
	}
	if k.Blocked, ok = wahrheitswert(o["blocked"]); !ok {
		return e, errors.New("blocked: erwartet true oder false")
	}
	if k.Archived, ok = wahrheitswert(o["archived"]); !ok {
		return e, errors.New("archived: erwartet true oder false")
	}
	if k.Labels, err = textListe(o["labels"], SpiegelListeMax); err != nil {
		return e, fmt.Errorf("labels: %v", err)
	}
	if k.Owner, ok = text(o["owner"]); !ok {
		return e, errors.New("owner: erwartet einen Text")
	}
	if k.Assignees, err = textListe(o["assignees"], SpiegelListeMax); err != nil {
		return e, fmt.Errorf("assignees: %v", err)
	}
	for _, f := range []struct {
		datum, zeit string
		d, z        **string
	}{{"startDate", "startTime", &k.StartDate, &k.StartTime}, {"dueDate", "dueTime", &k.DueDate, &k.DueTime}} {
		if *f.d, err = datumOderNull(f.datum); err != nil {
			return e, err
		}
		if *f.z, err = zeitOderNull(f.zeit); err != nil {
			return e, err
		}
		if *f.z != nil && *f.d == nil {
			return e, fmt.Errorf("%s: Uhrzeit ohne Datum", f.zeit)
		}
	}
	if k.Anhaenge, ok = ganzeZahl(o["anhaenge"], math.MaxInt32); !ok {
		return e, errors.New("anhaenge: erwartet eine ganze Zahl ab 0")
	}
	if istNull(o["comments"]) {
		return e, errors.New("comments: erwartet eine Liste")
	}
	liste, err := elemente(o["comments"], SpiegelKommentareMax)
	if err != nil {
		return e, fmt.Errorf("comments: %v", err)
	}
	k.Comments = make([]SpiegelKommentar, 0, len(liste))
	ids := map[string]bool{}
	for j, r := range liste {
		c, err := liesKommentar(r)
		if err != nil {
			return e, fmt.Errorf("comments[%d]: %v", j, err)
		}
		if ids[c.ID] {
			return e, fmt.Errorf("comments[%d].id: doppelt", j)
		}
		ids[c.ID] = true
		k.Comments = append(k.Comments, c)
	}
	e.Karte = k
	return e, nil
}

// liesUrsprung: null oder die Außen-ID einer außen angelegten Karte bzw. eines
// Termins (ADR-0141 §2, ab 2c). Das Skript reicht sie unübersetzt durch.
func liesUrsprung(roh json.RawMessage) (*string, error) {
	if istNull(roh) {
		return nil, nil
	}
	s, ok := text(roh)
	if !ok || !GueltigeKennung(s) {
		return nil, errors.New("ursprung: erwartet null oder eine ID [A-Za-z0-9_-]{1,64}")
	}
	return &s, nil
}

func liesKommentar(roh json.RawMessage) (SpiegelKommentar, error) {
	var c SpiegelKommentar
	o, err := objekt(roh, len(felderSpiegelKommentar)+1)
	if err != nil {
		return c, err
	}
	for k := range o {
		if !felderSpiegelKommentar[k] {
			return c, fmt.Errorf("unbekannter Schlüssel %q", kurz(k))
		}
	}
	for k := range felderSpiegelKommentar {
		if _, ok := o[k]; !ok {
			return c, fmt.Errorf("%s fehlt", k)
		}
	}
	var ok bool
	if c.ID, ok = text(o["id"]); !ok || !GueltigeKennung(c.ID) {
		return c, errors.New("id: erwartet [A-Za-z0-9_-]{1,64}")
	}
	for _, f := range []struct {
		name string
		ziel *string
	}{{"author", &c.Author}, {"text", &c.Text}, {"created", &c.Created}} {
		if *f.ziel, ok = text(o[f.name]); !ok {
			return c, fmt.Errorf("%s: erwartet einen Text", f.name)
		}
	}
	if c.Edited, ok = optText(o["edited"], func(string) bool { return true }); !ok {
		return c, errors.New("edited: erwartet null oder einen Text")
	}
	if c.Herkunft, ok = text(o["herkunft"]); !ok || (c.Herkunft != HerkunftInnen && c.Herkunft != HerkunftAussen) {
		return c, errors.New("herkunft: erlaubt sind innen und aussen")
	}
	return c, nil
}

// liesSpiegelListe prüft die Liste spiegel des PUT: jeder Eintrag, eindeutige
// Kennungen (auch gegenüber den Schatten), Termin-Bezug auf eine Karte der
// Liste. Erster Fehler gewinnt.
func liesSpiegelListe(roh json.RawMessage, lanes map[string]bool, spalten map[string]string, schattenIDs map[string]bool) ([]SpiegelEingang, error) {
	if istNull(roh) {
		return nil, abgelehnt("Hülle: spiegel fehlt")
	}
	liste, err := elemente(roh, EingangMaxSchatten)
	if err != nil {
		return nil, abgelehnt("Hülle: spiegel: %v", err)
	}
	aus := make([]SpiegelEingang, 0, len(liste))
	gesehen := map[string]int{}
	karten := map[string]bool{}
	for i, r := range liste {
		e, err := liesSpiegel(r, lanes, spalten)
		if err != nil {
			return nil, abgelehnt("spiegel[%d]: %v", i, err)
		}
		if j, schon := gesehen[e.SpiegelID]; schon {
			return nil, abgelehnt("spiegel[%d]: spiegelId wie spiegel[%d]", i, j)
		}
		if schattenIDs[e.SpiegelID] {
			return nil, abgelehnt("spiegel[%d]: spiegelId steht auch unter schatten", i)
		}
		gesehen[e.SpiegelID] = i
		if e.Karte != nil {
			karten[e.SpiegelID] = true
		}
		aus = append(aus, e)
	}
	for i, e := range aus {
		if e.Termin != nil && e.Termin.Karte != nil && !karten[*e.Termin.Karte] {
			return nil, abgelehnt("spiegel[%d]: karte ist keine Karte der Liste spiegel", i)
		}
	}
	return aus, nil
}

// --- Anwenden (außen) -----------------------------------------------------------

// SpiegelAbgleich zählt, was SpiegelAnwenden geändert hat.
type SpiegelAbgleich struct {
	Neu       int `json:"neu"`
	Geaendert int `json:"geaendert"`
	Entfernt  int `json:"entfernt"`
	Anzahl    int `json:"anzahl"`
	// WartendErledigt: Außen-IDs wartender Spiegel, die markiert oder
	// entfernt wurden — der Speicher löscht ihre Zeile in boardpaar_wartend
	// (ab 2c).
	WartendErledigt []string `json:"-"`
}

// spiegelMarkeKarte: die Marke an einer Spiegelkarte. anhaenge nur, wenn die
// Karte innen Anhänge hat (§8: „N Anhänge, nur intern“).
type spiegelMarkeKarte struct {
	ID       string `json:"id"`
	Anhaenge int    `json:"anhaenge,omitempty"`
}

// markeVon: Kennung der Marke `spiegel` an einem Objekt ("" ohne Marke).
func markeVon(o map[string]json.RawMessage) string {
	roh, ok := o["spiegel"]
	if !ok {
		return ""
	}
	var m struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(roh, &m) != nil {
		return ""
	}
	return m.ID
}

func nullOder(s *string) json.RawMessage {
	if s == nil {
		return json.RawMessage("null")
	}
	return roh(*s)
}

// SpiegelKarteAussen baut eine Spiegelkarte, wie sie außen im Board steht.
// Neben den Feldern der Feldliste trägt sie die Felder, die der Browser beim
// Laden auf JEDER Karte ergänzt (src/storage.js, Lade-Migrationen): links,
// attachments, activity, project_id, coverAttachmentId, recurrence. Fehlten
// sie, schickte jeder Browser nach dem Laden Ops, die sie nachtragen — und die
// Spiegel-Schranke lehnte sie ab. Ein Test hält das gegen die Migrationen fest
// (testdata/spiegel-karte.json, tests/boardpaar-spiegel-826.test.js).
func SpiegelKarteAussen(k *SpiegelKarte, kennung, id string, displayID int, spalteID string) map[string]json.RawMessage {
	kommentare := make([]map[string]json.RawMessage, 0, len(k.Comments))
	for _, c := range k.Comments {
		m := map[string]json.RawMessage{"id": roh(c.ID), "author": roh(c.Author), "text": roh(c.Text),
			"created": roh(c.Created), "herkunft": roh(c.Herkunft)}
		if c.Edited != nil {
			m["edited"] = roh(*c.Edited)
		}
		kommentare = append(kommentare, m)
	}
	labels, assignees := k.Labels, k.Assignees
	if labels == nil {
		labels = []string{}
	}
	if assignees == nil {
		assignees = []string{}
	}
	karte := map[string]json.RawMessage{
		"id": roh(id), "displayId": roh(displayID), "title": roh(k.Title), "description": roh(k.Description),
		"columnId": roh(spalteID), "critical": roh(k.Critical), "blocked": roh(k.Blocked), "labels": roh(labels),
		"owner": roh(k.Owner), "assignees": roh(assignees), "startDate": nullOder(k.StartDate),
		"dueDate": nullOder(k.DueDate), "archived": roh(k.Archived), "comments": roh(kommentare),
		"links": json.RawMessage("[]"), "attachments": json.RawMessage("[]"), "activity": json.RawMessage("[]"),
		"project_id": json.RawMessage("null"), "coverAttachmentId": json.RawMessage("null"),
		"recurrence": json.RawMessage("null"), "spiegel": roh(spiegelMarkeKarte{ID: kennung, Anhaenge: k.Anhaenge}),
	}
	if k.Priority != nil {
		karte["priority"] = roh(*k.Priority)
	}
	if k.StartTime != nil {
		karte["startTime"] = roh(*k.StartTime)
	}
	if k.DueTime != nil {
		karte["dueTime"] = roh(*k.DueTime)
	}
	return karte
}

// spiegelTerminAussen baut einen Spiegeltermin, wie er außen in board.termine
// steht. karteID: die Außen-ID der Karte oder "".
func spiegelTerminAussen(t *SpiegelTermin, kennung, id, karteID string) map[string]json.RawMessage {
	personen := t.Personen
	if personen == nil {
		personen = []string{}
	}
	termin := map[string]json.RawMessage{"id": roh(id), "titel": roh(t.Titel), "datum": roh(t.Datum),
		"personen": roh(personen), "spiegel": roh(spiegelMarke{ID: kennung})}
	for k, v := range map[string]*string{"bisDatum": t.BisDatum, "von": t.Von, "bis": t.Bis, "ort": t.Ort,
		"beschreibung": t.Beschreibung} {
		if v != nil {
			termin[k] = roh(*v)
		}
	}
	if karteID != "" {
		termin["karteId"] = roh(karteID)
	}
	return termin
}

// hatMarke: Das Objekt trägt den Schlüssel `spiegel` (gleich mit welchem
// Wert) — für die Schranke zählt schon der Schlüssel.
func hatMarke(o map[string]json.RawMessage) bool {
	_, ok := o["spiegel"]
	return ok
}

func gleichesObjekt(a, b map[string]json.RawMessage) bool {
	return gleicheListe([]map[string]json.RawMessage{a}, []map[string]json.RawMessage{b})
}

// displayZahl: displayId einer Karte, 0 ohne gültige Zahl.
func displayZahl(roh json.RawMessage) int {
	var f float64
	if json.Unmarshal(roh, &f) != nil || f < 1 || f > math.MaxInt32 || f != math.Trunc(f) {
		return 0
	}
	return int(f)
}

// SpiegelAnwenden schreibt die Spiegel in den Board-Zustand einer Kopie
// (ADR-0141 §4), ohne Journal und ohne wartende (Schritt 2b). Siehe
// SpiegelAnwendenMit.
func SpiegelAnwenden(state []byte, menge []SpiegelEingang) ([]byte, *SpiegelAbgleich, error) {
	return SpiegelAnwendenMit(state, menge, nil, nil)
}

// SpiegelAnwendenMit schreibt die Spiegel in den Board-Zustand einer Kopie
// (ADR-0141 §4) und mischt sie ab 2c in den Bestand (spiegel_mischen.go).
// Die Struktur muss schon angewendet sein (Lanes und Spalten der Spiegel
// existieren). schattenListe: die Schatten derselben Menge (nur ihr
// `ursprung` zählt hier). Ändert sich nichts, kommt state byte-gleich zurück.
//
// Karten: Eine Spiegelkarte wird über ihre Marke gefunden, sonst über ihren
// `ursprung` (eine wartende Karte, die jetzt ihre Marke bekommt), sonst über
// die abgeleitete ID (eine Karte mit dieser ID ohne Marke ist eine frühere
// Spiegelkarte, deren Marke verloren ging — sie wird wieder aufgenommen statt
// verdoppelt). Ihre ID und displayId bleiben; eine neue bekommt die
// abgeleitete ID und die nächste Nummer des externen Boards (PK-11). Jede Lane
// führt ihre Spiegelkarten nach position. Karten, die der Eingang nicht
// platziert (ohne Marke, oder mit geschütztem Platz), stehen hinter ihrem
// bisherigen Vorgänger, ohne einen am Anfang. Markierte Karten, die nicht in
// der Menge sind, fallen weg; Karten ohne Marke bleiben — außer wartende in
// den beiden Fällen aus §2 (W2-3).
//
// Termine: ebenso über Marke, ursprung oder abgeleitete ID; markierte, die
// fehlen, fallen weg. Die Liste führt erst die Termine ohne Marke, dann die
// Spiegel in der Reihenfolge der Menge.
func SpiegelAnwendenMit(state []byte, menge []SpiegelEingang, schattenListe []Eingang, schutz *Schutz) ([]byte, *SpiegelAbgleich, error) {
	if schutz == nil {
		schutz = &Schutz{}
	}
	erg := &SpiegelAbgleich{Anzahl: len(menge)}
	var stand map[string]json.RawMessage
	if err := json.Unmarshal(state, &stand); err != nil || stand == nil {
		return nil, nil, fmt.Errorf("%w: kein JSON-Objekt", ErrStand)
	}
	var board map[string]json.RawMessage
	if err := json.Unmarshal(stand["board"], &board); err != nil || board == nil {
		return nil, nil, fmt.Errorf("%w: board fehlt", ErrStand)
	}
	var lanes []map[string]json.RawMessage
	if r, ok := board["sprints"]; ok && !istNull(r) {
		if json.Unmarshal(r, &lanes) != nil {
			return nil, nil, fmt.Errorf("%w: sprints ist keine Liste", ErrStand)
		}
	}
	var termine []map[string]json.RawMessage
	if r, ok := board["termine"]; ok && !istNull(r) {
		if json.Unmarshal(r, &termine) != nil {
			return nil, nil, fmt.Errorf("%w: termine ist keine Liste", ErrStand)
		}
	}

	// Wartende (§2, W2-3): übernimmt ein Spiegel sie über ursprung, bekommen
	// sie ihre Marke. Sonst fallen sie weg, wenn ihr ursprung an einem
	// Schatten steht (innen vertraulich geworden) oder wenn sie nirgends
	// stehen, obwohl ihr angelegt bis rueckweg_bis bestätigt war (innen
	// gelöscht). Alle übrigen bleiben unberührt.
	ursprungSpiegel, ursprungSchatten := map[string]bool{}, map[string]bool{}
	for _, e := range menge {
		switch {
		case e.Karte != nil && e.Karte.Ursprung != nil:
			ursprungSpiegel[*e.Karte.Ursprung] = true
		case e.Termin != nil && e.Termin.Ursprung != nil:
			ursprungSpiegel[*e.Termin.Ursprung] = true
		}
	}
	for _, e := range schattenListe {
		if e.Ursprung != nil {
			ursprungSchatten[*e.Ursprung] = true
		}
	}
	wartendWeg := map[string]bool{}
	for u, w := range schutz.Wartend {
		switch {
		case ursprungSpiegel[u]:
		case ursprungSchatten[u]:
			wartendWeg[u] = true
		case schutz.RueckwegBis != nil && w.SeqAngelegt <= *schutz.RueckwegBis:
			wartendWeg[u] = true
		}
	}
	erledigt := map[string]bool{}

	// Bestand: Karten je Lane, markierte Karten nach Kennung, Karten nach ID.
	type ort struct {
		lane, idx int
	}
	laneIdx := map[string]int{}
	kartenJeLane := make([][]map[string]json.RawMessage, len(lanes))
	markiert := map[string]ort{}
	nachID := map[string]ort{}
	hoechste := 0
	for i, l := range lanes {
		if l == nil {
			continue
		}
		laneIdx[zeichenkette(l["id"])] = i
		if r, ok := l["cards"]; ok && !istNull(r) {
			if json.Unmarshal(r, &kartenJeLane[i]) != nil {
				return nil, nil, fmt.Errorf("%w: cards einer Lane ist keine Liste", ErrStand)
			}
		}
		for j, k := range kartenJeLane[i] {
			if k == nil {
				continue
			}
			if m := markeVon(k); m != "" {
				if _, schon := markiert[m]; schon {
					return nil, nil, fmt.Errorf("%w: Spiegel-Marke außen doppelt", ErrStand)
				}
				markiert[m] = ort{i, j}
			}
			if id := zeichenkette(k["id"]); id != "" {
				nachID[id] = ort{i, j}
			}
			hoechste = max(hoechste, displayZahl(k["displayId"]))
		}
	}
	naechste := max(displayZahl(board["nextDisplayId"]), hoechste+1)
	naechsteVorher := naechste

	// Spiegelkarten: neu bauen, in den Bestand mischen, platzieren.
	type platz struct {
		pos, reihe int
		karte      map[string]json.RawMessage
		quelle     ort // Bestandskarte, die der Spiegel übernimmt (lane -1: keine)
	}
	neuJeLane := make([][]platz, len(lanes))
	verbraucht := map[ort]bool{}                   // Bestandskarten, die ein Spiegel übernimmt
	bleibt := map[ort]map[string]json.RawMessage{} // übernommen, Platz geschützt
	aussenID := map[string]string{}                // Kennung → Außen-ID der Karte
	for reihe, e := range menge {
		if e.Karte == nil {
			continue
		}
		li, ok := laneIdx[AussenLaneID(e.Karte.Lane)]
		if !ok {
			return nil, nil, fmt.Errorf("%w: Lane eines Spiegels fehlt außen", ErrStand)
		}
		schluessel := []string{OffenSchluessel(ArtKarte, e.SpiegelID, false)}
		alt, gibtEs := markiert[e.SpiegelID]
		if u := e.Karte.Ursprung; !gibtEs && u != nil {
			if o, ok := nachID[*u]; ok && markeVon(kartenJeLane[o.lane][o.idx]) == "" {
				alt, gibtEs = o, true
				if _, w := schutz.Wartend[*u]; w {
					erledigt[*u] = true
				}
			}
		}
		if !gibtEs {
			if o, ok := nachID[AbgeleiteteID(e.SpiegelID)]; ok && markeVon(kartenJeLane[o.lane][o.idx]) == "" {
				alt, gibtEs = o, true
			}
		}
		id, nr := AbgeleiteteID(e.SpiegelID), 0
		var bestand map[string]json.RawMessage
		if gibtEs {
			bestand = kartenJeLane[alt.lane][alt.idx]
			verbraucht[alt] = true
			if bid := zeichenkette(bestand["id"]); bid != "" {
				id = bid
				// F-2220: Journal-Einträge aus der Wartezeit (wartend = 1,
				// Kennung = Außen-ID = id am Bestand) bleiben offen, bis das
				// Skript sie bestätigt — auch nach dem Lauf, der die Marke
				// setzt und die Zeile in boardpaar_wartend löscht. Deshalb für
				// jede übernommene Karte, nicht nur im Lauf über ursprung.
				schluessel = append(schluessel, OffenSchluessel(ArtKarte, bid, true))
			}
			nr = displayZahl(bestand["displayId"])
		}
		if nr == 0 {
			nr = naechste
			naechste++
		}
		aussenID[e.SpiegelID] = id
		karte := SpiegelKarteAussen(e.Karte, e.SpiegelID, id, nr, AussenSpalteID(e.Karte.Spalte))
		offen := schutz.stellenVon(schluessel...)
		if gibtEs {
			karte = mischeKarte(bestand, karte, offen)
		}
		switch {
		case !gibtEs:
			erg.Neu++
		case !gleichesObjekt(bestand, karte):
			erg.Geaendert++
		}
		if gibtEs && offen.platz() {
			bleibt[alt] = karte
			continue
		}
		quelle := ort{-1, -1}
		if gibtEs {
			quelle = alt
		}
		neuJeLane[li] = append(neuJeLane[li], platz{pos: e.Karte.Position, reihe: reihe, karte: karte, quelle: quelle})
	}
	for i := range lanes {
		sort.SliceStable(neuJeLane[i], func(a, b int) bool {
			if neuJeLane[i][a].pos != neuJeLane[i][b].pos {
				return neuJeLane[i][a].pos < neuJeLane[i][b].pos
			}
			return neuJeLane[i][a].reihe < neuJeLane[i][b].reihe
		})
	}
	geaendert := false
	for i, l := range lanes {
		if l == nil {
			continue
		}
		// quelleIn: Index im Bestand dieser Lane je Platz der neuen Liste (-1:
		// kommt nicht aus dieser Lane).
		neu := make([]map[string]json.RawMessage, 0, len(kartenJeLane[i])+len(neuJeLane[i]))
		quelleIn := make([]int, 0, cap(neu))
		for _, p := range neuJeLane[i] {
			neu = append(neu, p.karte)
			if p.quelle.lane == i {
				quelleIn = append(quelleIn, p.quelle.idx)
			} else {
				quelleIn = append(quelleIn, -1)
			}
		}
		for j, k := range kartenJeLane[i] {
			o := ort{i, j}
			if gemischt, ok := bleibt[o]; ok {
				k = gemischt
			} else if verbraucht[o] {
				continue
			} else if k != nil && markeVon(k) != "" {
				erg.Entfernt++ // markiert, aber nicht mehr in der Menge
				continue
			} else if id := zeichenkette(k["id"]); k != nil && wartendWeg[id] {
				erg.Entfernt++
				erledigt[id] = true
				continue
			}
			// Hinter den nächsten Vorgänger aus dem Bestand, der in der neuen
			// Liste steht; ohne einen an den Anfang.
			pos := 0
			for v := j - 1; v >= 0 && pos == 0; v-- {
				for x, q := range quelleIn {
					if q == v {
						pos = x + 1
						break
					}
				}
			}
			neu = append(neu[:pos], append([]map[string]json.RawMessage{k}, neu[pos:]...)...)
			quelleIn = append(quelleIn[:pos], append([]int{j}, quelleIn[pos:]...)...)
		}
		if !gleicheListe(kartenJeLane[i], neu) {
			geaendert = true
			l["cards"] = roh(neu)
		}
	}

	// Termine.
	markiertT := map[string]int{}
	nachIDT := map[string]int{}
	for i, t := range termine {
		if t == nil {
			continue
		}
		if m := markeVon(t); m != "" {
			if _, schon := markiertT[m]; schon {
				return nil, nil, fmt.Errorf("%w: Spiegel-Marke außen doppelt", ErrStand)
			}
			markiertT[m] = i
		}
		if id := zeichenkette(t["id"]); id != "" {
			nachIDT[id] = i
		}
	}
	verbrauchtT := map[int]bool{}
	var spiegelTermine []map[string]json.RawMessage
	for _, e := range menge {
		if e.Termin == nil {
			continue
		}
		schluessel := []string{OffenSchluessel(ArtTermin, e.SpiegelID, false)}
		alt, gibtEs := markiertT[e.SpiegelID]
		if u := e.Termin.Ursprung; !gibtEs && u != nil {
			if i, ok := nachIDT[*u]; ok && markeVon(termine[i]) == "" {
				alt, gibtEs = i, true
				if _, w := schutz.Wartend[*u]; w {
					erledigt[*u] = true
				}
			}
		}
		if !gibtEs {
			if i, ok := nachIDT[AbgeleiteteID(e.SpiegelID)]; ok && markeVon(termine[i]) == "" {
				alt, gibtEs = i, true
			}
		}
		id := AbgeleiteteID(e.SpiegelID)
		if gibtEs {
			verbrauchtT[alt] = true
			if bid := zeichenkette(termine[alt]["id"]); bid != "" {
				id = bid
				schluessel = append(schluessel, OffenSchluessel(ArtTermin, bid, true)) // F-2220, wie bei Karten
			}
		}
		karteID := ""
		if e.Termin.Karte != nil {
			karteID = aussenID[*e.Termin.Karte]
		}
		t := spiegelTerminAussen(e.Termin, e.SpiegelID, id, karteID)
		if gibtEs {
			t = mischeTermin(termine[alt], t, schutz.stellenVon(schluessel...))
		}
		switch {
		case !gibtEs:
			erg.Neu++
		case !gleichesObjekt(termine[alt], t):
			erg.Geaendert++
		}
		spiegelTermine = append(spiegelTermine, t)
	}
	neueTermine := make([]map[string]json.RawMessage, 0, len(termine)+len(spiegelTermine))
	for i, t := range termine {
		if verbrauchtT[i] {
			continue
		}
		if t != nil && markeVon(t) != "" {
			erg.Entfernt++
			continue
		}
		if id := zeichenkette(t["id"]); t != nil && wartendWeg[id] {
			erg.Entfernt++
			erledigt[id] = true
			continue
		}
		neueTermine = append(neueTermine, t)
	}
	neueTermine = append(neueTermine, spiegelTermine...)
	if !gleicheListe(termine, neueTermine) {
		geaendert = true
		board["termine"] = roh(neueTermine)
	}
	// Wartende, deren Objekt außen nicht mehr da ist (etwa nach einem
	// Restore), aber die nach W2-3 wegfallen: Zeile trotzdem abräumen.
	for u := range wartendWeg {
		erledigt[u] = true
	}
	for u := range erledigt {
		erg.WartendErledigt = append(erg.WartendErledigt, u)
	}
	sort.Strings(erg.WartendErledigt)
	// Neue Nummern vergeben: der Zähler des externen Boards rückt nach.
	if naechste != naechsteVorher {
		board["nextDisplayId"] = roh(naechste)
		geaendert = true
	}
	if !geaendert {
		return state, erg, nil
	}
	board["sprints"] = roh(lanes)
	stand["board"] = roh(board)
	neu, err := json.Marshal(stand)
	if err != nil {
		return nil, nil, err
	}
	return neu, erg, nil
}

// --- Spiegel-Schranke (§7) -------------------------------------------------------

// SpiegelOrt: ein Spiegel-Objekt, das ein Schreibvorgang verändern wollte.
type SpiegelOrt struct {
	Art string // "karte", "termin", "lane", "spalte"
	ID  string // ID im Board-Zustand außen
}

// spiegelBild: die Spiegel-Objekte eines Zustands.
type spiegelBild struct {
	lanes       []string                              // markierte Lanes in Reihenfolge
	laneMeta    map[string]map[string]json.RawMessage // ohne cards
	spalten     []string
	spaltenMeta map[string]map[string]json.RawMessage
	karten      map[string]map[string]json.RawMessage // markierte Karten nach ID
	karteLane   map[string]string
	reihe       map[string][]string        // je markierter Lane: markierte Karten in Reihenfolge
	fremde      map[string]map[string]bool // je markierter Lane: Karten ohne Marke
	termine     map[string]map[string]json.RawMessage
	// doppelt (Kritik #826 K-2): IDs, die an einem markierten Objekt stehen und
	// im selben Zustand mehr als einmal vorkommen. Die Maps oben behielten nur
	// das letzte Vorkommen — eine geänderte Kopie VOR dem Original fiele nicht
	// auf, und danach hinge jeder Eingang an „Spiegel-Marke außen doppelt“.
	doppelt []SpiegelOrt
}

func liesSpiegelBild(state []byte) (*spiegelBild, bool) {
	var stand map[string]json.RawMessage
	if json.Unmarshal(state, &stand) != nil || stand == nil {
		return nil, false
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(stand["board"], &board) != nil || board == nil {
		return nil, false
	}
	b := &spiegelBild{laneMeta: map[string]map[string]json.RawMessage{}, spaltenMeta: map[string]map[string]json.RawMessage{},
		karten: map[string]map[string]json.RawMessage{}, karteLane: map[string]string{}, reihe: map[string][]string{},
		fremde: map[string]map[string]bool{}, termine: map[string]map[string]json.RawMessage{}}
	var lanes []map[string]json.RawMessage
	if r, ok := board["sprints"]; ok && !istNull(r) && json.Unmarshal(r, &lanes) != nil {
		return nil, false
	}
	// Vorkommen je ID und Art (alle Objekte, markiert oder nicht).
	zahl := map[SpiegelOrt]int{}
	markiert := map[SpiegelOrt]bool{}
	zaehle := func(art string, o map[string]json.RawMessage) {
		ort := SpiegelOrt{Art: art, ID: zeichenkette(o["id"])}
		zahl[ort]++
		if hatMarke(o) {
			markiert[ort] = true
		}
	}
	defer func() {
		for ort := range markiert {
			if zahl[ort] > 1 {
				b.doppelt = append(b.doppelt, ort)
			}
		}
		// Verify V-2: feste Reihenfolge statt Map-Reihenfolge.
		sort.Slice(b.doppelt, func(i, j int) bool {
			if b.doppelt[i].Art != b.doppelt[j].Art {
				return b.doppelt[i].Art < b.doppelt[j].Art
			}
			return b.doppelt[i].ID < b.doppelt[j].ID
		})
	}()
	for _, l := range lanes {
		if l == nil {
			continue
		}
		lid := zeichenkette(l["id"])
		zaehle("lane", l)
		laneMarkiert := hatMarke(l)
		if laneMarkiert {
			b.lanes = append(b.lanes, lid)
			meta := map[string]json.RawMessage{}
			for k, v := range l {
				if k != "cards" {
					meta[k] = v
				}
			}
			b.laneMeta[lid] = meta
			b.fremde[lid] = map[string]bool{}
		}
		var cards []map[string]json.RawMessage
		if r, ok := l["cards"]; ok && !istNull(r) && json.Unmarshal(r, &cards) != nil {
			return nil, false
		}
		for _, k := range cards {
			if k == nil {
				continue
			}
			id := zeichenkette(k["id"])
			zaehle("karte", k)
			if hatMarke(k) {
				b.karten[id] = k
				b.karteLane[id] = lid
				b.reihe[lid] = append(b.reihe[lid], id)
			} else if laneMarkiert {
				b.fremde[lid][id] = true
			}
		}
	}
	var defs []map[string]json.RawMessage
	if r, ok := board["columnDefs"]; ok && !istNull(r) && json.Unmarshal(r, &defs) != nil {
		return nil, false
	}
	for _, d := range defs {
		if d != nil {
			zaehle("spalte", d)
		}
		if d != nil && hatMarke(d) {
			id := zeichenkette(d["id"])
			b.spalten = append(b.spalten, id)
			b.spaltenMeta[id] = d
		}
	}
	var termine []map[string]json.RawMessage
	if r, ok := board["termine"]; ok && !istNull(r) && json.Unmarshal(r, &termine) != nil {
		return nil, false
	}
	for _, t := range termine {
		if t != nil {
			zaehle("termin", t)
		}
		if t != nil && hatMarke(t) {
			b.termine[zeichenkette(t["id"])] = t
		}
	}
	return b, true
}

// KannSpiegelBetreffen: Vorfilter — trägt einer der Stände eine Marke (oder
// ein \u-Escape, hinter dem sich der Schlüssel verbergen kann)?
func KannSpiegelBetreffen(vorher, nachher []byte) bool {
	return bytes.Contains(vorher, []byte("spiegel")) || bytes.Contains(nachher, []byte("spiegel")) ||
		bytes.Contains(nachher, []byte(`\u`))
}

// SpiegelVerstoesse ist die Spiegel-Schranke in der Fassung von Schritt 2b
// (ADR-0141 §7, Schreibschutz): Jede Änderung an einem Spiegel-Objekt zwischen
// vorher und nachher ist ein Verstoß —
//   - eine markierte Lane oder Spalte: geändert, entfernt, umsortiert, neu markiert;
//   - eine markierte Karte: geändert (jedes Feld, auch confidential, Marke),
//     entfernt, in eine andere Lane verschoben, innerhalb der Lane umsortiert;
//   - eine Karte ohne Marke, die in einer markierten Lane neu auftaucht
//     (Anlegen oder Hineinschieben, §7: „auch für das Anlegen neuer Karten in
//     gespiegelten Lanes“);
//   - ein markierter Termin: geändert, entfernt; ein neu markierter Termin.
//
// Ist einer der Stände nicht lesbar, ist das ein Verstoß (unlesbar = true,
// Ausfall in Richtung Nichtstun). Gilt nur für Boards mit kopie = 1; das
// prüft der Aufrufer.
func SpiegelVerstoesse(vorher, nachher []byte) (verstoesse []SpiegelOrt, unlesbar bool) {
	if !KannSpiegelBetreffen(vorher, nachher) {
		return nil, false
	}
	alt, ok1 := liesSpiegelBild(vorher)
	neu, ok2 := liesSpiegelBild(nachher)
	if !ok1 || !ok2 {
		return nil, true
	}
	melde := func(art, id string) { verstoesse = append(verstoesse, SpiegelOrt{Art: art, ID: id}) }
	// K-2: Eine doppelte ID an einem Spiegel ist immer ein Verstoß.
	for _, o := range neu.doppelt {
		melde(o.Art, o.ID)
	}

	vergleicheReihe := func(art string, a, b []string, metaA, metaB map[string]map[string]json.RawMessage) {
		gemeldet := map[string]bool{}
		for i, id := range a {
			m, gibtEs := metaB[id]
			if !gibtEs || i >= len(b) || b[i] != id || !gleichesObjekt(metaA[id], m) {
				melde(art, id)
				gemeldet[id] = true
			}
		}
		for _, id := range b {
			if _, war := metaA[id]; !war && !gemeldet[id] {
				melde(art, id)
			}
		}
	}
	vergleicheReihe("lane", alt.lanes, neu.lanes, alt.laneMeta, neu.laneMeta)
	vergleicheReihe("spalte", alt.spalten, neu.spalten, alt.spaltenMeta, neu.spaltenMeta)

	karteGemeldet := map[string]bool{}
	meldeKarte := func(id string) {
		if !karteGemeldet[id] {
			karteGemeldet[id] = true
			melde("karte", id)
		}
	}
	for id, k := range alt.karten {
		n, gibtEs := neu.karten[id]
		if !gibtEs || neu.karteLane[id] != alt.karteLane[id] || !gleichesObjekt(k, n) {
			meldeKarte(id)
		}
	}
	for id := range neu.karten {
		if _, war := alt.karten[id]; !war {
			meldeKarte(id)
		}
	}
	for lid, a := range alt.reihe {
		b := neu.reihe[lid]
		for i, id := range a {
			if i >= len(b) || b[i] != id {
				meldeKarte(id)
			}
		}
	}
	for lid, fremde := range neu.fremde {
		vorher, laneWar := alt.fremde[lid]
		for id := range fremde {
			if !laneWar || !vorher[id] {
				meldeKarte(id)
			}
		}
	}
	for id, t := range alt.termine {
		if n, gibtEs := neu.termine[id]; !gibtEs || !gleichesObjekt(t, n) {
			melde("termin", id)
		}
	}
	for id := range neu.termine {
		if _, war := alt.termine[id]; !war {
			melde("termin", id)
		}
	}
	sort.SliceStable(verstoesse, func(i, j int) bool {
		if verstoesse[i].Art != verstoesse[j].Art {
			return verstoesse[i].Art < verstoesse[j].Art
		}
		return verstoesse[i].ID < verstoesse[j].ID
	})
	return verstoesse, false
}

// OhneSpiegelMarken entfernt alle Marken `spiegel` (Lanes, Spalten, Karten,
// Termine) aus einem Board-Zustand (ADR-0141 §5.1: Insert — Import, Restore
// als neues Board, Kopie als neues Board — „entfernt alle spiegel-Marken“;
// eine Kopie eines externen Boards ist kein Spiegel). Ohne Marke kommt state
// byte-gleich zurück; ein nicht lesbarer Zustand ebenfalls (Insert prüft ihn
// nicht, er bleibt wie geschickt).
func OhneSpiegelMarken(state []byte) []byte {
	if !bytes.Contains(state, []byte("spiegel")) && !bytes.Contains(state, []byte(`\u`)) {
		return state
	}
	var stand map[string]json.RawMessage
	if json.Unmarshal(state, &stand) != nil || stand == nil {
		return state
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(stand["board"], &board) != nil || board == nil {
		return state
	}
	geaendert := false
	ohne := func(liste []map[string]json.RawMessage) {
		for _, o := range liste {
			if _, ok := o["spiegel"]; ok {
				delete(o, "spiegel")
				geaendert = true
			}
		}
	}
	var lanes []map[string]json.RawMessage
	if json.Unmarshal(board["sprints"], &lanes) == nil {
		for _, l := range lanes {
			if l == nil {
				continue
			}
			var cards []map[string]json.RawMessage
			if json.Unmarshal(l["cards"], &cards) == nil {
				vorher := geaendert
				geaendert = false
				ohne(cards)
				if geaendert {
					l["cards"] = roh(cards)
				}
				geaendert = geaendert || vorher
			}
		}
		vorher := geaendert
		geaendert = false
		ohne(lanes)
		if geaendert || vorher {
			board["sprints"] = roh(lanes)
		}
		geaendert = geaendert || vorher
	}
	for _, schluessel := range []string{"columnDefs", "termine"} {
		var liste []map[string]json.RawMessage
		if json.Unmarshal(board[schluessel], &liste) == nil {
			vorher := geaendert
			geaendert = false
			ohne(liste)
			if geaendert {
				board[schluessel] = roh(liste)
			}
			geaendert = geaendert || vorher
		}
	}
	if !geaendert {
		return state
	}
	stand["board"] = roh(board)
	neu, err := json.Marshal(stand)
	if err != nil {
		return state
	}
	return neu
}
