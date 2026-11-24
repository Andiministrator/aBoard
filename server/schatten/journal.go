package schatten

// journal.go — Journal des Board-Paar-Rückwegs, Stufe 2c (ADR-0141 §5.1,
// Karte #827, Etappe E1a).
//
// JournalEintraege vergleicht zwei Stände einer externen Kopie (vor und nach
// einem Schreibvorgang) und liefert je geänderter Stelle einen Eintrag. Wie
// die Spiegel-Schranke arbeitet es am Ergebnis, nicht an den Ops: So zählt,
// was am Ende im Board steht, gleich über welchen Weg.
//
// Erfasst werden
//   - Spiegelkarten und Spiegeltermine (Marke `spiegel`, Kennung = spiegel.id),
//   - wartende Spiegel (außen angelegt, Marke steht noch aus; Kennung = ihre
//     Außen-ID, Wartend = true),
//   - neue Karten ohne Marke in einer markierten Lane und neue Termine ohne
//     Marke (Art `angelegt`; sie werden damit wartend).
//
// Werte stehen in der Form des Hinwegs (Format 3, Feldnamen der Karte bzw. des
// Termins). Lanes und Spalten stehen als Kennung (die `spiegel.id` ihrer
// Marke; Backlog und Standardspalten tragen dort ihre Klartext-ID), Bezüge auf
// Karten als {"kennung", "wartend"}. Das Skript übersetzt Kennungen in innere
// IDs (§2). Welche Änderungen überhaupt erlaubt sind, entscheidet die
// Spiegel-Schranke vorher; was sie durchlässt und hier keine Art hat (etwa
// `activity`), erzeugt keinen Eintrag.

import (
	"encoding/json"
	"sort"
	"strconv"
)

// Arten eines Journal-Eintrags (§5.1, ohne checkpunkt.add/.remove — Punkt 1
// von „Umsetzung Schritt 2b“).
const (
	JArtFeld          = "feld"
	JArtVerschoben    = "verschoben"
	JArtReihenfolge   = "reihenfolge"
	JArtListeAdd      = "liste.add"
	JArtListeRemove   = "liste.remove"
	JArtCheckpunkt    = "checkpunkt.feld"
	JArtKommentarAdd  = "kommentar.add"
	JArtKommentarEdit = "kommentar.edit"
	JArtTerminFeld    = "termin.feld"
	JArtAngelegt      = "angelegt"
	JArtEntfernt      = "entfernt"
	JArtVertraulich   = "vertraulich"
)

// JournalEintrag ist eine Änderung an genau einer Stelle.
type JournalEintrag struct {
	Objekt  string          `json:"objekt"` // "karte" | "termin"
	Kennung string          `json:"kennung"`
	Wartend bool            `json:"wartend"`
	Art     string          `json:"art"`
	Stelle  string          `json:"stelle"`
	Vorher  json.RawMessage `json:"vorher"`
	Nachher json.RawMessage `json:"nachher"`
}

// KartenFeldliste: die einzelnen Felder einer Spiegelkarte, die als Art
// `feld` erfasst werden (§4; labels/assignees sind Mengen, Kommentare und
// Ort eigene Arten).
var KartenFeldliste = []string{"title", "description", "priority", "critical", "blocked", "owner",
	"startDate", "startTime", "dueDate", "dueTime", "archived"}

// TerminFeldliste: die Felder eines Spiegeltermins (§4), wie TERMIN_FELDLISTE
// in src/ops.js.
var TerminFeldliste = []string{"titel", "beschreibung", "datum", "bisDatum", "von", "bis", "ort", "personen", "karteId"}

// Bezug auf eine Karte im Journal.
type journalBezug struct {
	Kennung string `json:"kennung"`
	Wartend bool   `json:"wartend"`
}

// journalKarte: eine erfasste Karte in einem Stand.
type journalKarte struct {
	kennung string
	wartend bool
	karte   map[string]json.RawMessage
	lane    string // Kennung der Lane
	spalte  string // Kennung der Spalte ("" = Spalte ohne Marke)
}

// journalBild: was JournalEintraege aus einem Stand liest.
type journalBild struct {
	karten     map[string]*journalKarte              // Schlüssel: "k:"/"w:" + Kennung
	reihen     map[string][]string                   // je "lane\x00spalte": Kartenschlüssel in Reihenfolge
	neu        []*journalKarte                       // unmarkierte, nicht wartende Karten in markierten Lanes
	termine    map[string]map[string]json.RawMessage // Schlüssel: "t:"/"w:" + Kennung
	neuT       []map[string]json.RawMessage          // unmarkierte, nicht wartende Termine
	idZuBezug  map[string]journalBezug               // Karten-ID außen → Bezug (für karteId, Anker)
	vorhanden  map[string]bool                       // alle Karten-IDs (auch unerfasste)
	vorhandenT map[string]bool                       // alle Termin-IDs
	neuIDs     map[string]bool                       // IDs der Karten in neu
	ordnung    []string                              // erfasste Karten in Board-Reihenfolge (Lane für Lane, oben nach unten)
	doppelt    map[string]bool                       // Schlüssel, die mehr als einmal vorkommen (Marke doppelt)
}

func schluesselKarte(kennung string, wartend bool) string {
	if wartend {
		return "w:" + kennung
	}
	return "k:" + kennung
}

func liesJournalBild(state []byte, wartend map[string]bool) (*journalBild, bool) {
	var stand map[string]json.RawMessage
	if json.Unmarshal(state, &stand) != nil || stand == nil {
		return nil, false
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(stand["board"], &board) != nil || board == nil {
		return nil, false
	}
	b := &journalBild{karten: map[string]*journalKarte{}, reihen: map[string][]string{},
		termine: map[string]map[string]json.RawMessage{}, idZuBezug: map[string]journalBezug{},
		vorhanden: map[string]bool{}, vorhandenT: map[string]bool{}, neuIDs: map[string]bool{}, doppelt: map[string]bool{}}
	var defs []map[string]json.RawMessage
	if r, ok := board["columnDefs"]; ok && !istNull(r) && json.Unmarshal(r, &defs) != nil {
		return nil, false
	}
	spaltenKennung := map[string]string{}
	for _, d := range defs {
		if d != nil {
			if m := markeVon(d); m != "" {
				spaltenKennung[zeichenkette(d["id"])] = m
			}
		}
	}
	var lanes []map[string]json.RawMessage
	if r, ok := board["sprints"]; ok && !istNull(r) && json.Unmarshal(r, &lanes) != nil {
		return nil, false
	}
	for _, l := range lanes {
		if l == nil {
			continue
		}
		laneKennung := markeVon(l)
		var cards []map[string]json.RawMessage
		if r, ok := l["cards"]; ok && !istNull(r) && json.Unmarshal(r, &cards) != nil {
			return nil, false
		}
		for _, k := range cards {
			if k == nil {
				continue
			}
			id := zeichenkette(k["id"])
			b.vorhanden[id] = true
			jk := &journalKarte{karte: k, lane: laneKennung, spalte: spaltenKennung[zeichenkette(k["columnId"])]}
			switch m := markeVon(k); {
			case m != "":
				jk.kennung = m
			case id != "" && wartend[id]:
				jk.kennung, jk.wartend = id, true
			case laneKennung != "" && id != "":
				// Ohne Marke, nicht wartend, in einer markierten Lane: neu
				// angelegt (oder hereingeschoben) — wird wartend.
				jk.kennung, jk.wartend = id, true
				b.neu = append(b.neu, jk)
				b.neuIDs[id] = true
			default:
				continue
			}
			s := schluesselKarte(jk.kennung, jk.wartend)
			if _, schon := b.karten[s]; schon {
				b.doppelt[s] = true
			}
			b.karten[s] = jk
			b.ordnung = append(b.ordnung, s)
			b.idZuBezug[id] = journalBezug{Kennung: jk.kennung, Wartend: jk.wartend}
			if laneKennung != "" {
				g := laneKennung + "\x00" + jk.spalte
				b.reihen[g] = append(b.reihen[g], s)
			}
		}
	}
	var termine []map[string]json.RawMessage
	if r, ok := board["termine"]; ok && !istNull(r) && json.Unmarshal(r, &termine) != nil {
		return nil, false
	}
	for _, t := range termine {
		if t == nil {
			continue
		}
		id := zeichenkette(t["id"])
		b.vorhandenT[id] = true
		switch m := markeVon(t); {
		case m != "":
			if _, schon := b.termine["t:"+m]; schon {
				b.doppelt["t:"+m] = true
			}
			b.termine["t:"+m] = t
		case id != "" && wartend[id]:
			b.termine["w:"+id] = t
		case id != "":
			b.neuT = append(b.neuT, t)
		}
	}
	return b, true
}

func wertOderNull(o map[string]json.RawMessage, feld string) json.RawMessage {
	if v, ok := o[feld]; ok && v != nil {
		return v
	}
	return json.RawMessage("null")
}

// listeAlsMenge: Zeichenketten einer Liste (Reihenfolge erhalten, ohne Doppel).
func listeAlsMenge(roh json.RawMessage) []string {
	var aus []string
	gesehen := map[string]bool{}
	for _, s := range nurZeichenketten(roh) {
		if !gesehen[s] {
			gesehen[s] = true
			aus = append(aus, s)
		}
	}
	return aus
}

// journalKommentar: Kommentar in Journal-Form (ohne authorEmail, kind, Anhänge).
func journalKommentar(c map[string]json.RawMessage) json.RawMessage {
	return roh(map[string]json.RawMessage{"id": wertOderNull(c, "id"), "author": wertOderNull(c, "author"),
		"text": wertOderNull(c, "text"), "created": wertOderNull(c, "created")})
}

func kommentareNachID(roh json.RawMessage) ([]string, map[string]map[string]json.RawMessage) {
	var l []map[string]json.RawMessage
	_ = json.Unmarshal(roh, &l)
	var ids []string
	m := map[string]map[string]json.RawMessage{}
	for _, c := range l {
		if c == nil {
			continue
		}
		id := zeichenkette(c["id"])
		if id == "" {
			continue
		}
		if _, schon := m[id]; !schon {
			ids = append(ids, id)
		}
		m[id] = c
	}
	return ids, m
}

// bezugVon: Bezug auf eine Karte über ihre ID außen, null ohne erfasste Karte.
func (b *journalBild) bezugVon(id string) json.RawMessage {
	if bz, ok := b.idZuBezug[id]; ok && id != "" {
		return roh(bz)
	}
	return json.RawMessage("null")
}

// ankerVon: der Vorgänger einer Karte in ihrer Gruppe (Lane+Spalte), null am
// Anfang.
func (b *journalBild) ankerVon(schluessel string, jk *journalKarte) json.RawMessage {
	reihe := b.reihen[jk.lane+"\x00"+jk.spalte]
	for i, s := range reihe {
		if s == schluessel {
			if i == 0 {
				return json.RawMessage("null")
			}
			v := b.karten[reihe[i-1]]
			return roh(journalBezug{Kennung: v.kennung, Wartend: v.wartend})
		}
	}
	return json.RawMessage("null")
}

func ortVon(lane, spalte string, nach json.RawMessage) json.RawMessage {
	return roh(map[string]json.RawMessage{"lane": roh(lane), "spalte": roh(spalte), "nach": nach})
}

// karteAngelegt: Feldliste-Form einer außen angelegten Karte.
func (b *journalBild) karteAngelegt(jk *journalKarte, nach json.RawMessage) json.RawMessage {
	k := jk.karte
	o := map[string]json.RawMessage{"lane": roh(jk.lane), "spalte": roh(jk.spalte), "nach": nach,
		"labels": roh(orLeer(listeAlsMenge(k["labels"]))), "assignees": roh(orLeer(listeAlsMenge(k["assignees"]))),
		"vertraulich": roh(Vertraulich(k))}
	for _, f := range KartenFeldliste {
		o[f] = wertOderNull(k, f)
	}
	ids, km := kommentareNachID(k["comments"])
	kommentare := []json.RawMessage{}
	for _, id := range ids {
		kommentare = append(kommentare, journalKommentar(km[id]))
	}
	o["comments"] = roh(kommentare)
	return roh(o)
}

func orLeer(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// terminForm: Feldliste-Form eines Termins (karteId als Bezug).
func (b *journalBild) terminForm(t map[string]json.RawMessage) json.RawMessage {
	o := map[string]json.RawMessage{"vertraulich": roh(Vertraulich(t))}
	for _, f := range TerminFeldliste {
		if f == "karteId" {
			o[f] = b.bezugVon(zeichenkette(t["karteId"]))
			continue
		}
		o[f] = wertOderNull(t, f)
	}
	return roh(o)
}

// JournalEintraege liefert die Journal-Einträge des Schritts vorher → nachher.
// wartend: Außen-IDs aus boardpaar_wartend. neuWartend: Außen-IDs, die durch
// `angelegt` wartend werden, mit Art ("karte"/"termin"). Ist ein Stand nicht
// lesbar: ok = false (der Aufrufer schreibt dann nichts).
func JournalEintraege(vorher, nachher []byte, wartend map[string]bool) (eintraege []JournalEintrag, neuWartend map[string]string, ok bool) {
	alt, ok1 := liesJournalBild(vorher, wartend)
	neu, ok2 := liesJournalBild(nachher, wartend)
	if !ok1 || !ok2 {
		return nil, nil, false
	}
	neuWartend = map[string]string{}
	add := func(e JournalEintrag) { eintraege = append(eintraege, e) }
	// Karten, die schon im alten Stand unmarkiert und nicht wartend waren,
	// gibt es nur außen: kein Eintrag, kein Anker (das Skript könnte sie
	// nicht übersetzen — ALARM beim Betreiber).
	for _, b := range []*journalBild{alt, neu} {
		for g, reihe := range b.reihen {
			gefiltert := reihe[:0:0]
			for _, k := range reihe {
				if jk := b.karten[k]; !(jk.wartend && alt.neuIDs[jk.kennung]) {
					gefiltert = append(gefiltert, k)
				}
			}
			b.reihen[g] = gefiltert
		}
	}

	// Karten in Schlüssel-Reihenfolge (deterministisch).
	schluessel := make([]string, 0, len(alt.karten))
	for s := range alt.karten {
		schluessel = append(schluessel, s)
	}
	sort.Strings(schluessel)
	verschoben := map[string]bool{}
	// Einträge zur Position (verschoben, reihenfolge, angelegt) gehen erst am
	// Ende hinaus, in der Reihenfolge des NEUEN Stands: So steht jeder Anker
	// („nach Karte X“) schon an seinem Platz, wenn das Skript den Eintrag
	// anwendet (Sitzungs-Kritik A-2). Je Karte höchstens einer.
	position := map[string]JournalEintrag{}
	for _, s := range schluessel {
		a := alt.karten[s]
		if a.wartend && alt.neuIDs[a.kennung] {
			// Schon im alten Stand unmarkiert und nicht wartend: eine Karte,
			// die es nur außen gibt (aus der Zeit vor 2c) — nie erfasst.
			continue
		}
		n, gibtEs := neu.karten[s]
		basis := JournalEintrag{Objekt: ArtKarte, Kennung: a.kennung, Wartend: a.wartend}
		if !gibtEs {
			e := basis
			e.Art = JArtEntfernt
			add(e)
			continue
		}
		if a.lane != n.lane || a.spalte != n.spalte {
			verschoben[s] = true
			e := basis
			e.Art, e.Stelle = JArtVerschoben, "ort"
			e.Vorher = ortVon(a.lane, a.spalte, alt.ankerVon(s, a))
			e.Nachher = ortVon(n.lane, n.spalte, neu.ankerVon(s, n))
			position[s] = e
		}
		for _, f := range KartenFeldliste {
			va, vn := wertOderNull(a.karte, f), wertOderNull(n.karte, f)
			if gleicherWert(va, vn) {
				continue
			}
			if f == "description" {
				if punkte, nur := NurHaekchenGeaendert(zeichenkette(va), zeichenkette(vn)); nur {
					for _, p := range punkte {
						e := basis
						e.Art = JArtCheckpunkt
						e.Stelle = "checkpunkt:" + strconv.Itoa(p.Vorkommen) + ":" + p.Text
						e.Vorher = roh(map[string]any{"text": p.Text, "vorkommen": p.Vorkommen, "erledigt": !p.Erledigt})
						e.Nachher = roh(map[string]any{"text": p.Text, "vorkommen": p.Vorkommen, "erledigt": p.Erledigt})
						add(e)
					}
					continue
				}
			}
			e := basis
			e.Art, e.Stelle, e.Vorher, e.Nachher = JArtFeld, f, va, vn
			add(e)
		}
		for _, f := range []string{"labels", "assignees"} {
			la, ln := listeAlsMenge(a.karte[f]), listeAlsMenge(n.karte[f])
			inA, inN := map[string]bool{}, map[string]bool{}
			for _, x := range la {
				inA[x] = true
			}
			for _, x := range ln {
				inN[x] = true
			}
			for _, x := range ln {
				if !inA[x] {
					e := basis
					e.Art, e.Stelle, e.Nachher = JArtListeAdd, f+":"+x, roh(x)
					add(e)
				}
			}
			for _, x := range la {
				if !inN[x] {
					e := basis
					e.Art, e.Stelle, e.Vorher = JArtListeRemove, f+":"+x, roh(x)
					add(e)
				}
			}
		}
		_, ka := kommentareNachID(a.karte["comments"])
		idsN, kn := kommentareNachID(n.karte["comments"])
		for _, id := range idsN {
			c := kn[id]
			if alter, war := ka[id]; war {
				if ta, tn := wertOderNull(alter, "text"), wertOderNull(c, "text"); !gleicherWert(ta, tn) {
					e := basis
					e.Art, e.Stelle, e.Vorher, e.Nachher = JArtKommentarEdit, "kommentar:"+id, ta, tn
					add(e)
				}
				continue
			}
			e := basis
			e.Art, e.Stelle, e.Nachher = JArtKommentarAdd, "kommentar:"+id, journalKommentar(c)
			add(e)
		}
		if !Vertraulich(a.karte) && Vertraulich(n.karte) {
			e := basis
			e.Art, e.Stelle, e.Vorher, e.Nachher = JArtVertraulich, "confidential", roh(false), roh(true)
			add(e)
		}
	}

	// Reihenfolge je Gruppe (Lane+Spalte): Karten, die in beiden Ständen in
	// derselben Gruppe stehen und nicht in der längsten gemeinsamen
	// Teilfolge liegen, bekommen einen Eintrag mit ihrem neuen Vorgänger.
	gruppen := make([]string, 0, len(neu.reihen))
	for g := range neu.reihen {
		gruppen = append(gruppen, g)
	}
	sort.Strings(gruppen)
	for _, g := range gruppen {
		posAlt := map[string]int{}
		for i, s := range alt.reihen[g] {
			posAlt[s] = i
		}
		var folge []string
		for _, s := range neu.reihen[g] {
			if _, war := posAlt[s]; war && !verschoben[s] {
				folge = append(folge, s)
			}
		}
		stabil := laengsteSteigende(folge, posAlt)
		for _, s := range folge {
			if stabil[s] {
				continue
			}
			a, n := alt.karten[s], neu.karten[s]
			position[s] = JournalEintrag{Objekt: ArtKarte, Kennung: n.kennung, Wartend: n.wartend, Art: JArtReihenfolge,
				Stelle: "reihenfolge", Vorher: roh(map[string]json.RawMessage{"nach": alt.ankerVon(s, a)}),
				Nachher: roh(map[string]json.RawMessage{"nach": neu.ankerVon(s, n)})}
		}
	}

	// Neu angelegte Karten: nicht im alten Stand erfasst (neu oder aus einer
	// unmarkierten Lane hereingeschoben).
	for _, jk := range neu.neu {
		id := zeichenkette(jk.karte["id"])
		if _, war := alt.karten[schluesselKarte(id, true)]; war {
			continue // war schon wartend oder rein außen (alt.neuIDs)
		}
		neuWartend[id] = ArtKarte
		position[schluesselKarte(id, true)] = JournalEintrag{Objekt: ArtKarte, Kennung: id, Wartend: true, Art: JArtAngelegt,
			Nachher: neu.karteAngelegt(jk, neu.vorgaengerNeu(jk))}
	}
	for _, s := range neu.ordnung {
		if e, ok := position[s]; ok {
			add(e)
			delete(position, s)
		}
	}

	// Termine.
	tSchluessel := make([]string, 0, len(alt.termine))
	for s := range alt.termine {
		tSchluessel = append(tSchluessel, s)
	}
	sort.Strings(tSchluessel)
	for _, s := range tSchluessel {
		a := alt.termine[s]
		basis := JournalEintrag{Objekt: ArtTermin, Kennung: s[2:], Wartend: s[0] == 'w'}
		n, gibtEs := neu.termine[s]
		if !gibtEs {
			e := basis
			e.Art = JArtEntfernt
			add(e)
			continue
		}
		for _, f := range TerminFeldliste {
			var va, vn json.RawMessage
			if f == "karteId" {
				va, vn = alt.bezugVon(zeichenkette(a[f])), neu.bezugVon(zeichenkette(n[f]))
			} else {
				va, vn = wertOderNull(a, f), wertOderNull(n, f)
			}
			if !gleicherWert(va, vn) {
				e := basis
				e.Art, e.Stelle, e.Vorher, e.Nachher = JArtTerminFeld, f, va, vn
				add(e)
			}
		}
		if !Vertraulich(a) && Vertraulich(n) {
			e := basis
			e.Art, e.Stelle, e.Vorher, e.Nachher = JArtVertraulich, "confidential", roh(false), roh(true)
			add(e)
		}
	}
	for _, t := range neu.neuT {
		id := zeichenkette(t["id"])
		if alt.vorhandenT[id] {
			continue // bestand schon (eigener Termin außen), nicht neu
		}
		neuWartend[id] = ArtTermin
		add(JournalEintrag{Objekt: ArtTermin, Kennung: id, Wartend: true, Art: JArtAngelegt, Nachher: neu.terminForm(t)})
	}
	return eintraege, neuWartend, true
}

// vorgaengerNeu: Anker einer neuen Karte — der nächste erfasste (oder ebenfalls
// neue) Vorgänger in derselben Lane und Spalte, null am Anfang.
func (b *journalBild) vorgaengerNeu(jk *journalKarte) json.RawMessage {
	return b.ankerVon(schluesselKarte(jk.kennung, true), jk)
}

// laengsteSteigende: die Elemente einer längsten Teilfolge, deren alte
// Positionen streng steigen (Patience-Sortierung, O(n log n)).
func laengsteSteigende(folge []string, pos map[string]int) map[string]bool {
	n := len(folge)
	if n == 0 {
		return map[string]bool{}
	}
	enden := []int{}       // Index in folge des kleinsten Endes je Länge
	vorg := make([]int, n) // Vorgänger in der Kette
	for i, s := range folge {
		p := pos[s]
		lo, hi := 0, len(enden)
		for lo < hi {
			m := (lo + hi) / 2
			if pos[folge[enden[m]]] < p {
				lo = m + 1
			} else {
				hi = m
			}
		}
		if lo > 0 {
			vorg[i] = enden[lo-1]
		} else {
			vorg[i] = -1
		}
		if lo == len(enden) {
			enden = append(enden, i)
		} else {
			enden[lo] = i
		}
	}
	aus := map[string]bool{}
	for i := enden[len(enden)-1]; i >= 0; i = vorg[i] {
		aus[folge[i]] = true
	}
	return aus
}
