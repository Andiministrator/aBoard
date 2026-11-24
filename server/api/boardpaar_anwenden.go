package api

// boardpaar_anwenden.go — Rückweg des Board-Paars innen, Stufe 2c (ADR-0141
// §5.3, §6, „Umsetzung 2c“ Punkte 20–27; Karte #827, Etappe E3b).
//
//	innen  POST /agent/boardpaar/anwenden/{innenBoardId}   abr_  einen Rückweg-Eintrag anwenden
//
// Das Skript hat einen Journal-Eintrag von außen abgeholt und seine Kennungen
// in innere IDs übersetzt. Der Server baut daraus GENAU EINE Op nach einer
// festen Liste — rohe Ops nimmt der Weg nicht an — und schickt sie durch
// postOps (Plan D6): So gelten alle Prüfungen des Ops-Wegs (Kommentarrecht,
// Protokoll, Idempotenz, Größe), ohne dass die Closure umgebaut wird.
//
// Konflikt in zwei Schritten (§5.3, PK-5 a–c, VF-12): Schlägt die bedingte Op
// fehl, wird sie nicht gemerkt; danach schreibt der Server den
// Konflikt-Kommentar als eigene Op „<Op-ID>-k“. Ist diese schon gemerkt, ist
// die Antwort „konflikt“, ohne die Bedingung erneut zu prüfen.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// BoardpaarAnwendenPfad: Weg im Mux (am Agenten-Eingang nach /agent).
const BoardpaarAnwendenPfad = "/boardpaar/anwenden/"

// Ergebnisse (ADR-0141 §5.3, „Umsetzung 2c“ Punkt 21).
const (
	rwAngewendet      = "angewendet"
	rwSchonAngewendet = "schon-angewendet"
	rwKonflikt        = "konflikt"
	rwVerworfen       = "verworfen"
)

// Gründe zu „verworfen“ und Gründe der Vorbedingung unter der Sperre.
const (
	rwGrundFeldNichtErlaubt      = "feld-nicht-erlaubt"
	rwGrundVertraulichGeworden   = "vertraulich-geworden"
	rwGrundKarteWeg              = "karte-weg"
	rwGrundTerminWeg             = "termin-weg"
	rwGrundLaneUnbekannt         = "lane-unbekannt"
	rwGrundSpalteUnbekannt       = "spalte-unbekannt" // Sitzungs-QA E3b A-3
	rwGrundLoeschenNichtErlaubt  = "loeschen-nicht-erlaubt"
	rwGrundBearbeiterUnbekannt   = "bearbeiter-unbekannt"
	rwGrundKommentarNichtErlaubt = "kommentar-nicht-erlaubt"
	// Kritik E3b K-4/K-5: Kommentar-ID schon mit anderem Text belegt;
	// bearbeiteter Kommentar fehlt innen.
	rwGrundKommentarIDBelegt = "kommentar-id-belegt"
	rwGrundKommentarWeg      = "kommentar-weg"
	// Nur Vorbedingung: Es gibt die Karte bzw. den Termin schon (angelegt),
	// oder der gelesene Stand hat sich geändert (neuer Versuch).
	rwGrundSchonDa        = "schon-da"
	rwGrundStandGeaendert = "stand-geaendert"
)

const (
	// rwOpIDMax: Platz für die Endung „-k“ (storage.MaxClientIDLaenge = 128).
	rwOpIDMax = storage.MaxClientIDLaenge - 2
	// rwVersuche: neue Versuche, wenn sich der gelesene Stand unter der
	// Sperre als geändert erweist (reihenfolge, vertraulich am Termin).
	rwVersuche = 3
	// rwKonfliktWertMax: Zeichen eines Werts im Konflikt-Kommentar. Eine
	// Kommentarlänge gibt es am Server nicht; die Grenze hält den Kommentar
	// lesbar und das Board klein („Umsetzung 2c“ Punkt 24).
	rwKonfliktWertMax = 10000
	rwTextMax         = 200
)

// anwendenEintrag: Rumpf von POST /boardpaar/anwenden („Umsetzung 2c“ Punkt 20).
type anwendenEintrag struct {
	OpID     string          `json:"op_id"`
	Objekt   string          `json:"objekt"`
	ID       string          `json:"id"`
	Art      string          `json:"art"`
	Stelle   string          `json:"stelle"`
	Vorher   json.RawMessage `json:"vorher"`
	Nachher  json.RawMessage `json:"nachher"`
	Ursprung string          `json:"ursprung"`
	Person   string          `json:"person"`
	Zeit     string          `json:"zeit"`
}

// rwArten: erlaubte Arten je Objekt.
var rwArten = map[string][]string{
	schatten.ArtKarte: {schatten.JArtFeld, schatten.JArtVerschoben, schatten.JArtReihenfolge, schatten.JArtListeAdd,
		schatten.JArtListeRemove, schatten.JArtCheckpunkt, schatten.JArtKommentarAdd, schatten.JArtKommentarEdit,
		schatten.JArtAngelegt, schatten.JArtEntfernt, schatten.JArtVertraulich},
	schatten.ArtTermin: {schatten.JArtTerminFeld, schatten.JArtAngelegt, schatten.JArtEntfernt, schatten.JArtVertraulich},
}

// kurzerText: nicht leer, höchstens max Byte, ohne Steuerzeichen.
func kurzerText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func istNullRoh(r json.RawMessage) bool {
	return len(r) == 0 || strings.TrimSpace(string(r)) == "null"
}

// pruefe: Form des Eintrags (422 bei Verstoß). Die Form der Werte prüft die
// Abbildung je Art.
func (e *anwendenEintrag) pruefe() string {
	switch {
	case !kurzerText(e.OpID, rwOpIDMax):
		return fmt.Sprintf("op_id: 1–%d Byte ohne Steuerzeichen", rwOpIDMax)
	case rwArten[e.Objekt] == nil:
		return `objekt: "karte" oder "termin"`
	case !slices.Contains(rwArten[e.Objekt], e.Art):
		return fmt.Sprintf("art: %q gibt es für %s nicht", e.Art, e.Objekt)
	case !kurzerText(e.ID, rwTextMax):
		return "id: innere ID fehlt"
	case !kurzerText(e.Person, rwTextMax):
		return fmt.Sprintf("person: 1–%d Byte ohne Steuerzeichen", rwTextMax)
	case len(e.Zeit) > 64:
		return "zeit: höchstens 64 Byte"
	case len(e.Stelle) > 64*1024:
		return "stelle: zu lang"
	}
	if e.Art == schatten.JArtAngelegt {
		// Sprint-94-Kritik K-2: dieselbe Form wie Fold und Hinweg — sonst
		// entstünde innen eine Karte ohne Ursprung, außen verschwände sie.
		if !schatten.GueltigeKennung(e.Ursprung) || schatten.AbgeleiteteID(e.Ursprung) != e.ID {
			return "ursprung: Außen-ID ([A-Za-z0-9_-]{1,64}) fehlt, oder id ist nicht UUIDv5(NS_BOARDPAAR, ursprung)"
		}
	} else if e.Ursprung != "" {
		return "ursprung: nur bei angelegt"
	}
	return ""
}

// ── Stand innen lesen ─────────────────────────────────────────────────────

type rwLane struct {
	ID       string                       `json:"id"`
	Title    string                       `json:"title"`
	Archived bool                         `json:"archived"`
	Kind     string                       `json:"kind"`
	Cards    []map[string]json.RawMessage `json:"cards"`
}

type rwBoard struct {
	Sprints    []rwLane `json:"sprints"`
	ColumnDefs []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"columnDefs"`
	Termine    []map[string]json.RawMessage `json:"termine"`
	Identities []struct {
		Label string `json:"label"`
	} `json:"identities"`
}

func leseRwBoard(state []byte) (*rwBoard, error) {
	var s struct {
		Board rwBoard `json:"board"`
	}
	if err := json.Unmarshal(state, &s); err != nil {
		return nil, err
	}
	return &s.Board, nil
}

// karte: Lane und Karte mit dieser ID (die erste, wie findCard in ops.js).
func (b *rwBoard) karte(id string) (*rwLane, map[string]json.RawMessage) {
	for i := range b.Sprints {
		for _, k := range b.Sprints[i].Cards {
			if k != nil && zeichenketteVon(k["id"]) == id {
				return &b.Sprints[i], k
			}
		}
	}
	return nil, nil
}

func (b *rwBoard) termin(id string) map[string]json.RawMessage {
	for _, t := range b.Termine {
		if t != nil && zeichenketteVon(t["id"]) == id {
			return t
		}
	}
	return nil
}

func (b *rwBoard) lane(id string) *rwLane {
	for i := range b.Sprints {
		if b.Sprints[i].ID == id {
			return &b.Sprints[i]
		}
	}
	return nil
}

// rwStandardSpalten: die vier Spalten, die der Browser einem Board ohne
// columnDefs gibt (migrateDefaultColumns) — dieselbe Annahme wie der Hinweg
// (schatten/v2.go, Kritik #825 A-1). Ohne sie verwarf der Rückweg auf einem
// solchen Board jedes angelegt und jedes verschoben (Sprint-94-Kritik K-1).
var rwStandardSpalten = [][2]string{{"col-backlog", "Backlog"}, {"col-ready", "Ready"},
	{"col-in-arbeit", "In Arbeit"}, {"col-erledigt", "Erledigt"}}

// spalten: die wirksamen Spalten (ID, Titel) in Reihenfolge.
func (b *rwBoard) spalten() [][2]string {
	if len(b.ColumnDefs) == 0 {
		return rwStandardSpalten
	}
	l := make([][2]string, 0, len(b.ColumnDefs))
	for _, d := range b.ColumnDefs {
		l = append(l, [2]string{d.ID, d.Title})
	}
	return l
}

// spalte: Gibt es die Spalte (columnDefs, sonst die Standardspalten)?
func (b *rwBoard) spalte(id string) bool {
	for _, d := range b.spalten() {
		if d[0] == id {
			return true
		}
	}
	return false
}

func (b *rwBoard) spaltenTitel(id string) string {
	for _, d := range b.spalten() {
		if d[0] == id {
			return d[1]
		}
	}
	return id
}

// anlageLane: Kann eine Anlage von außen in diese Lane? Nicht, wenn es sie
// innen nicht gibt, wenn sie archiviert ist (der Hinweg gibt sie zwar mit
// archived = true hinaus, aber ohne ihre Karten) oder eine Service-Lane (gibt
// er gar nicht hinaus) — die Karte verschwände außen still und stünde innen,
// wo sie niemand sieht (Sitzungs-Kritik N-3, F-2270; ADR-0117).
//
// Das gilt auch für die Backlog-Lane: BaueV2 gibt __backlog__ zwar immer als
// Lane hinaus, baueSpiegel aber ohne die Karten einer archivierten oder
// Service-Lane (Verify zu 9c975691, V-1). Eine Anlage dorthin wäre derselbe
// stille Verlust. ersatzLane liefert deshalb in diesem Fall keinen Ort, und
// das angelegt endet mit 422 (HALT) — statt, wie vor Sitzungs-QA #835 K-1, bei
// jedem Versuch denselben ungültigen Ersatzort zu planen (Ende: 503, endlos).
//
// Verify 2 V-1: dazu dieselben Regeln wie der Hinweg für Art und Kennung
// (unbekanntes kind, ID außerhalb von GueltigeKennung) — gemeinsames
// Prädikat schatten.LaneTraegtKartenHinaus.
func (b *rwBoard) anlageLane(id string) bool {
	l := b.lane(id)
	return l != nil && schatten.LaneTraegtKartenHinaus(l.ID, l.Kind, l.Archived)
}

// anlageSpalte: Kann eine Anlage in diese Spalte? Es muss sie geben, und der
// Hinweg muss sie spiegeln (Verify 2 V-1).
func (b *rwBoard) anlageSpalte(id string) bool {
	return b.spalte(id) && schatten.SpalteGehtHinaus(id)
}

// ersatzLane: wohin eine Anlage von außen geht, deren Lane nicht in Frage
// kommt (anlageLane) — die Backlog-Lane. Einen anderen Rückfall gibt es
// nicht: Ohne Backlog-Lane baut der Hinweg keinen Spiegel (schatten.BaueV2),
// es gäbe also auch nichts außen anzulegen (F-2270). "" = keine.
func (b *rwBoard) ersatzLane() string {
	if b.anlageLane(schatten.BacklogLane) {
		return schatten.BacklogLane
	}
	return ""
}

// ersatzSpalte: wohin eine Anlage geht, deren Spalte es innen nicht gibt —
// col-backlog, sonst die erste Spalte mit einer Kennung, die der Hinweg
// spiegelt (F-2270: spalten()[0] konnte eine sein, die außen nie erscheint).
// "" = keine.
func (b *rwBoard) ersatzSpalte() string {
	if b.spalte("col-backlog") {
		return "col-backlog"
	}
	for _, d := range b.spalten() {
		if schatten.SpalteGehtHinaus(d[0]) {
			return d[0]
		}
	}
	return ""
}

// karteBezug: Grund, aus dem ein Termin nicht auf diese Karte zeigen darf —
// sie fehlt (karte-weg) oder ist vertraulich (Kritik E3b K-1: der Termin
// würde wirksam vertraulich, schatten.TerminWirksamVertraulich). "" = in
// Ordnung, auch für keinen Bezug (null).
func (b *rwBoard) karteBezug(roh json.RawMessage) (string, bool) {
	if istNullRoh(roh) {
		return "", true
	}
	id, ok := zeichenketteRoh(roh)
	if !ok || !kurzerText(id, rwTextMax) {
		return "", false
	}
	_, k := b.karte(id)
	switch {
	case k == nil:
		return rwGrundKarteWeg, true
	case schatten.Vertraulich(k):
		return rwGrundVertraulichGeworden, true
	}
	return "", true
}

// terminVertraulich: wirksam vertraulich (§9, ADR-0140 §10) — eigenes Flag,
// oder die Karte aus karteId ist vertraulich oder fehlt (fail-closed, wie
// Hinweg und Vertraulich-Sperre; Kritik E3b K-1).
func (b *rwBoard) terminVertraulich(t map[string]json.RawMessage) bool {
	id := zeichenketteVon(t["karteId"])
	karten := map[string]bool{}
	if id != "" {
		if _, k := b.karte(id); k != nil {
			karten[id] = schatten.Vertraulich(k)
		}
	}
	return schatten.TerminWirksamVertraulich(schatten.Vertraulich(t), id, karten)
}

func (b *rwBoard) identitaet(name string) bool {
	for _, i := range b.Identities {
		if i.Label == name {
			return true
		}
	}
	return false
}

// ── Abbildung Eintrag → Op ────────────────────────────────────────────────

// rwPlan: die Op zu einem Eintrag und die Prüfung unter der Sperre.
type rwPlan struct {
	op map[string]any
	// bindung: Prüfung unter der Sperre (Vorbedingung von postOps); ""
	// heißt „in Ordnung“, sonst ein Grund (rwGrund…).
	bindung func(b *rwBoard) string
}

// rwSofort: Ergebnis ohne Op (verworfen, schon angewendet) oder 422.
type rwSofort struct {
	ergebnis, grund string
	fehler          string // ≠ "": fehlerhafter Eintrag (422)
}

func rwFehler(format string, a ...any) *rwSofort { return &rwSofort{fehler: fmt.Sprintf(format, a...)} }

func rwVerwirf(grund string) *rwSofort { return &rwSofort{ergebnis: rwVerworfen, grund: grund} }

type rwOrt struct {
	Lane   *string         `json:"lane"`
	Spalte *string         `json:"spalte"`
	Nach   json.RawMessage `json:"nach"`
}

// nachAnker: Anker „nach“ als Payload-Teil — null = Anfang der Spalte.
func nachAnker(nach json.RawMessage, payload map[string]any) bool {
	if istNullRoh(nach) {
		payload["targetIndex"] = 0
		return true
	}
	var id string
	if json.Unmarshal(nach, &id) != nil || !kurzerText(id, rwTextMax) {
		return false
	}
	payload["anchor"] = map[string]any{"mode": "after", "cardId": id}
	return true
}

func liesOrt(roh json.RawMessage, mitLane bool) (*rwOrt, bool) {
	var o rwOrt
	dec := json.NewDecoder(bytes.NewReader(roh))
	dec.DisallowUnknownFields()
	if istNullRoh(roh) || dec.Decode(&o) != nil {
		return nil, false
	}
	if mitLane && (o.Lane == nil || o.Spalte == nil || !kurzerText(*o.Lane, rwTextMax) || !kurzerText(*o.Spalte, rwTextMax)) {
		return nil, false
	}
	if !mitLane && (o.Lane != nil || o.Spalte != nil) {
		return nil, false
	}
	return &o, true
}

func zeichenketteRoh(roh json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(roh, &s) != nil {
		return "", false
	}
	return s, true
}

// externerAutor: Autor eines Kommentars von außen innen — „extern: <Name>“.
// Name ist der Autor außen (ohne ein schon vorhandenes „extern: “), sonst
// die Person.
func externerAutor(autor, person string) string {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(autor), "extern: "))
	if name == "" {
		name = person
	}
	return "extern: " + name
}

// baue bildet den Eintrag auf eine Op ab (§5.3, „Umsetzung 2c“ Punkt 22).
func (e *anwendenEintrag) baue(b *rwBoard) (*rwPlan, *rwSofort) {
	if e.Art == schatten.JArtEntfernt {
		return nil, rwVerwirf(rwGrundLoeschenNichtErlaubt)
	}
	if e.Objekt == schatten.ArtTermin {
		return e.baueTermin(b)
	}
	if e.Art == schatten.JArtAngelegt {
		return e.baueAnlage(b)
	}
	lane, karte := b.karte(e.ID)
	if karte == nil {
		return nil, rwVerwirf(rwGrundKarteWeg)
	}
	if e.Art == schatten.JArtVertraulich {
		if !bytes.Equal(bytes.TrimSpace(e.Nachher), []byte("true")) {
			return nil, rwFehler("nachher: vertraulich kennt nur true")
		}
		if schatten.Vertraulich(karte) {
			return nil, &rwSofort{ergebnis: rwAngewendet} // steht schon so
		}
		return &rwPlan{op: rwOp("card.field", map[string]any{"cardId": e.ID, "field": "confidential", "value": true}),
			bindung: e.bindungKarte(nil)}, nil
	}
	if schatten.Vertraulich(karte) {
		return nil, rwVerwirf(rwGrundVertraulichGeworden)
	}
	switch e.Art {
	case schatten.JArtFeld:
		if !slices.Contains(schatten.KartenFeldliste, e.Stelle) {
			return nil, rwVerwirf(rwGrundFeldNichtErlaubt)
		}
		if e.Vorher == nil || e.Nachher == nil {
			return nil, rwFehler("vorher und nachher sind Pflicht")
		}
		if !kartenFeldTypOK(e.Stelle, e.Nachher) {
			return nil, rwFehler("nachher: falscher Typ für %s", e.Stelle)
		}
		// Sprint-94-Kritik S-1: der Verantwortliche wie die Bearbeiter (§5.3,
		// PK-8) — leer oder eine Identität innen. Sonst wiese eine Person
		// außen eine innere Karte einem internen Namen oder Agenten zu.
		if e.Stelle == "owner" {
			if o, _ := zeichenketteRoh(e.Nachher); o != "" && !b.identitaet(o) {
				return nil, rwVerwirf(rwGrundBearbeiterUnbekannt)
			}
		}
		wert := e.Nachher
		if e.Stelle == "title" && istNullRoh(wert) {
			// Ein außen verschwundener Titel kommt als null (wertOderNull);
			// innen bleibt der Titel Text (Sitzungs-QA E3b A-4).
			wert = json.RawMessage(`""`)
		}
		return &rwPlan{op: rwOp("card.fieldIf", map[string]any{"cardId": e.ID, "field": e.Stelle,
			"value": wert, "expect": e.Vorher}), bindung: e.bindungKarte(nil)}, nil
	case schatten.JArtVerschoben:
		v, ok1 := liesOrt(e.Vorher, true)
		n, ok2 := liesOrt(e.Nachher, true)
		if !ok1 || !ok2 {
			return nil, rwFehler("vorher/nachher: {lane, spalte, nach} mit inneren IDs")
		}
		// F-2293: Ziel-Lane wie bei der Anlage (anlageLane) — eine innen
		// archivierte oder Service-Lane trägt ihre Karten nicht hinaus. Die
		// Karte bliebe innen zwar erhalten, außen verschwände ihr Spiegel aber
		// beim nächsten Hinweg, für die Person außen wie gelöscht. Verworfen
		// bleibt sie außen am alten Ort.
		if !b.anlageLane(*n.Lane) {
			return nil, rwVerwirf(rwGrundLaneUnbekannt)
		}
		// Ebenso die Spalte (anlageSpalte): eine Spalte, die der Hinweg nicht
		// spiegelt, ließe die Karte außen genauso verschwinden.
		if !b.anlageSpalte(*n.Spalte) {
			return nil, rwVerwirf(rwGrundSpalteUnbekannt)
		}
		p := map[string]any{"cardId": e.ID, "toSprintId": *n.Lane, "toColId": *n.Spalte,
			"expectSprintId": *v.Lane, "expectColId": *v.Spalte}
		if !nachAnker(n.Nach, p) {
			return nil, rwFehler("nachher.nach: innere Karten-ID oder null")
		}
		// Unter der Sperre archiviert: neu planen (dann verworfen, siehe oben).
		ziel, karteGebunden := *n.Lane, e.bindungKarte(nil)
		return &rwPlan{op: rwOp("card.moveIf", p), bindung: func(b2 *rwBoard) string {
			if g := karteGebunden(b2); g != "" {
				return g
			}
			if !b2.anlageLane(ziel) {
				return rwGrundStandGeaendert
			}
			return ""
		}}, nil
	case schatten.JArtReihenfolge:
		n, ok := liesOrt(e.Nachher, false)
		if !ok {
			return nil, rwFehler("nachher: {nach} mit innerer Karten-ID oder null")
		}
		laneID, spalte := lane.ID, zeichenketteVon(karte["columnId"])
		p := map[string]any{"cardId": e.ID, "toSprintId": laneID, "toColId": spalte,
			"expectSprintId": laneID, "expectColId": spalte}
		if !nachAnker(n.Nach, p) {
			return nil, rwFehler("nachher.nach: innere Karten-ID oder null")
		}
		// Ohne Bedingung (§5.3): An den gelesenen Ort gebunden — hat ihn innen
		// jemand geändert, liest der Endpunkt neu, statt die Karte in die
		// alte Lane zurückzuholen („Umsetzung 2c“ Punkt 22).
		return &rwPlan{op: rwOp("card.moveIf", p), bindung: e.bindungKarte(func(l *rwLane, k map[string]json.RawMessage) string {
			if l.ID == laneID && zeichenketteVon(k["columnId"]) == spalte {
				return ""
			}
			return rwGrundStandGeaendert
		})}, nil
	case schatten.JArtListeAdd, schatten.JArtListeRemove:
		feld, item, ok := strings.Cut(e.Stelle, ":")
		wert := e.Nachher
		if e.Art == schatten.JArtListeRemove {
			wert = e.Vorher
		}
		w, okW := zeichenketteRoh(wert)
		if !ok || (feld != "labels" && feld != "assignees") || !okW || w != item || item == "" {
			return nil, rwFehler("stelle labels:<x> bzw. assignees:<x>, Wert = <x>")
		}
		if e.Art == schatten.JArtListeAdd && feld == "assignees" && !b.identitaet(item) {
			return nil, rwVerwirf(rwGrundBearbeiterUnbekannt)
		}
		typ := "card.listAdd"
		if e.Art == schatten.JArtListeRemove {
			typ = "card.listRemove"
		}
		return &rwPlan{op: rwOp(typ, map[string]any{"cardId": e.ID, "field": feld, "item": item}),
			bindung: e.bindungKarte(nil)}, nil
	case schatten.JArtCheckpunkt:
		var v, n struct {
			Text      *string `json:"text"`
			Vorkommen *int    `json:"vorkommen"`
			Erledigt  *bool   `json:"erledigt"`
		}
		if json.Unmarshal(e.Vorher, &v) != nil || json.Unmarshal(e.Nachher, &n) != nil || v.Text == nil || n.Text == nil ||
			v.Vorkommen == nil || n.Vorkommen == nil || v.Erledigt == nil || n.Erledigt == nil ||
			*v.Text != *n.Text || *v.Vorkommen != *n.Vorkommen {
			return nil, rwFehler("vorher/nachher: {text, vorkommen, erledigt}, Punkt gleich")
		}
		return &rwPlan{op: rwOp("card.checkItemIf", map[string]any{"cardId": e.ID, "text": *n.Text, "vorkommen": *n.Vorkommen,
			"erledigt": *n.Erledigt, "expect": *v.Erledigt}), bindung: e.bindungKarte(nil)}, nil
	case schatten.JArtKommentarAdd:
		var k struct {
			ID      string          `json:"id"`
			Author  string          `json:"author"`
			Text    *string         `json:"text"`
			Created json.RawMessage `json:"created"`
		}
		if json.Unmarshal(e.Nachher, &k) != nil || !schatten.GueltigeKennung(k.ID) || k.Text == nil || e.Stelle != "kommentar:"+k.ID {
			return nil, rwFehler("nachher: {id, author, text, created}, stelle kommentar:<id>")
		}
		kommentar := map[string]any{"id": k.ID, "author": externerAutor(k.Author, e.Person), "text": *k.Text}
		if zeit, ok := zeichenketteRoh(k.Created); ok {
			kommentar["created"] = zeit // nur Text (Sitzungs-QA E3b A-2)
		}
		// Kritik E3b K-4: comment.add ist über die ID idempotent — eine schon
		// belegte ID mit anderem Text verschwände still.
		belegt := func(k map[string]json.RawMessage, text string) string {
			if c := kommentarMitID(k, kommentar["id"].(string)); c != nil && zeichenketteVon(c["text"]) != text {
				return rwGrundKommentarIDBelegt
			}
			return ""
		}
		if g := belegt(karte, *k.Text); g != "" {
			return nil, rwVerwirf(g)
		}
		text := *k.Text
		return &rwPlan{op: rwOp("comment.add", map[string]any{"cardId": e.ID, "comment": kommentar}),
			bindung: e.bindungKarte(func(_ *rwLane, k map[string]json.RawMessage) string { return belegt(k, text) })}, nil
	case schatten.JArtKommentarEdit:
		id, ok := strings.CutPrefix(e.Stelle, "kommentar:")
		v, okV := zeichenketteRoh(e.Vorher)
		n, okN := zeichenketteRoh(e.Nachher)
		if !ok || id == "" || !okV || !okN {
			return nil, rwFehler("stelle kommentar:<id>, vorher/nachher Text")
		}
		if kommentarMitID(karte, id) == nil {
			return nil, rwVerwirf(rwGrundKommentarWeg)
		}
		return &rwPlan{op: rwOp("comment.editIf", map[string]any{"cardId": e.ID, "commentId": id, "text": n, "expect": v}),
			bindung: e.bindungKarte(nil)}, nil
	}
	return nil, rwFehler("art %q nicht abbildbar", e.Art)
}

// bindungKarte: unter der Sperre — die Karte ist nicht vertraulich geworden
// (außer bei Art vertraulich), ein bearbeiteter Kommentar stammt von außen,
// und optional ist der gelesene Stand noch derselbe. Fehlt die Karte, meldet
// das der Fold (ziel-fehlt).
func (e *anwendenEintrag) bindungKarte(zusatz func(*rwLane, map[string]json.RawMessage) string) func(*rwBoard) string {
	return func(b *rwBoard) string {
		l, k := b.karte(e.ID)
		if k == nil {
			return ""
		}
		if e.Art != schatten.JArtVertraulich && schatten.Vertraulich(k) {
			return rwGrundVertraulichGeworden
		}
		if e.Art == schatten.JArtKommentarEdit {
			id, _ := strings.CutPrefix(e.Stelle, "kommentar:")
			if c := kommentarMitID(k, id); c != nil &&
				!istExternerAutor(zeichenketteVon(c["author"]), zeichenketteVon(c["authorEmail"])) {
				return rwGrundKommentarNichtErlaubt
			}
		}
		if zusatz != nil {
			return zusatz(l, k)
		}
		return ""
	}
}

// kommentarMitID: der Kommentar mit dieser ID an der Karte, sonst nil.
func kommentarMitID(k map[string]json.RawMessage, id string) map[string]json.RawMessage {
	var liste []map[string]json.RawMessage
	_ = json.Unmarshal(k["comments"], &liste)
	for _, c := range liste {
		if c != nil && zeichenketteVon(c["id"]) == id {
			return c
		}
	}
	return nil
}

// baueAnlage: angelegt (Karte) → card.upsert mit abgeleiteter ID (§2).
func (e *anwendenEintrag) baueAnlage(b *rwBoard) (*rwPlan, *rwSofort) {
	if _, k := b.karte(e.ID); k != nil {
		return nil, &rwSofort{ergebnis: rwSchonAngewendet}
	}
	var n map[string]json.RawMessage
	if json.Unmarshal(e.Nachher, &n) != nil || n == nil {
		return nil, rwFehler("nachher: Karte in Journal-Form")
	}
	ort, ok := liesOrt(rwRoh(map[string]json.RawMessage{"lane": n["lane"], "spalte": n["spalte"], "nach": n["nach"]}), true)
	if !ok {
		return nil, rwFehler("nachher: lane, spalte, nach mit inneren IDs")
	}
	// Sprint-94-Kritik K-1: angelegt wird NIE wegen des Orts verworfen. Ein
	// verworfenes angelegt bestätigt das Skript; der nächste Hinweg mit
	// rueckweg_bis ≥ seq_angelegt fände innen keine Karte mit diesem
	// Ursprung und entfernte die wartende Karte außen still (§2, W2-3) —
	// samt Titel, Beschreibung und Kommentaren. Fehlt Lane oder Spalte innen,
	// geht die Karte an einen Ersatzort und trägt einen Hinweis-Kommentar.
	lane, spalte, ersatz := *ort.Lane, *ort.Spalte, false
	if !b.anlageLane(lane) {
		if lane = b.ersatzLane(); lane == "" {
			return nil, rwFehler("innen gibt es keine nutzbare Backlog-Lane für die Anlage (fehlt, archiviert oder Service-Lane)")
		}
		ersatz, ort.Nach = true, nil
	}
	if !b.anlageSpalte(spalte) {
		if spalte = b.ersatzSpalte(); spalte == "" {
			return nil, rwFehler("innen gibt es keine Spalte für die Anlage")
		}
		ersatz, ort.Nach = true, nil
	}
	jetzt := time.Now().UTC().Format("2006-01-02")
	// Felder wie die Werkzeug-Anlage (werkzeugKarteAnlegen), dann die
	// Feldliste von außen.
	karte := map[string]any{
		"id": e.ID, "displayId": nil, "title": "", "description": "",
		"status": "offen", "priority": "mittel", "labels": []any{},
		"milestone": nil, "session": nil,
		"qa": false, "critical": false, "blocked": false,
		"owner": "", "assignees": []any{}, "project_id": nil,
		"complexity": "", "effort": nil, "agentContext": "",
		"links": []any{}, "attachments": []any{}, "coverAttachmentId": nil,
		"dueDate": nil, "startDate": nil, "recurrence": nil, "archived": false,
		"columnId": spalte, "created": jetzt, "updated": jetzt,
		"boardpaarUrsprung": e.Ursprung,
	}
	for _, f := range schatten.KartenFeldliste {
		if v, da := n[f]; da && (!istNullRoh(v) || strings.HasSuffix(f, "Date") || strings.HasSuffix(f, "Time")) {
			if !kartenFeldTypOK(f, v) {
				return nil, rwFehler("nachher.%s: falscher Typ", f)
			}
			karte[f] = v
		}
	}
	var labels, assignees []string
	if json.Unmarshal(n["labels"], &labels) != nil || json.Unmarshal(n["assignees"], &assignees) != nil {
		return nil, rwFehler("nachher: labels und assignees sind Listen von Zeichenketten")
	}
	karte["labels"] = orLeereListe(labels)
	bekannt := []string{}
	for _, a := range assignees {
		if b.identitaet(a) {
			bekannt = append(bekannt, a)
		}
	}
	karte["assignees"] = bekannt
	// S-1: ein Verantwortlicher, der innen keine Identität ist, fällt weg.
	if o, _ := zeichenketteRoh(n["owner"]); o == "" || !b.identitaet(o) {
		karte["owner"] = ""
	}
	var kommentare []struct {
		ID      string          `json:"id"`
		Author  string          `json:"author"`
		Text    *string         `json:"text"`
		Created json.RawMessage `json:"created"`
	}
	if r, da := n["comments"]; da && !istNullRoh(r) && json.Unmarshal(r, &kommentare) != nil {
		return nil, rwFehler("nachher.comments: Liste von {id, author, text, created}")
	}
	liste := []any{}
	for _, k := range kommentare {
		if !schatten.GueltigeKennung(k.ID) || k.Text == nil {
			return nil, rwFehler("nachher.comments: id ([A-Za-z0-9_-]{1,64}) und text sind Pflicht")
		}
		c := map[string]any{"id": k.ID, "author": externerAutor(k.Author, e.Person), "text": *k.Text}
		if zeit, ok := zeichenketteRoh(k.Created); ok {
			c["created"] = zeit
		}
		liste = append(liste, c)
	}
	if ersatz {
		// Hinweis an der Karte selbst, mit fester ID: eine Wiederholung
		// erzeugt denselben Kommentar (die Anlage ist ohnehin idempotent).
		liste = append(liste, map[string]any{"id": schatten.AbgeleiteteID("ort:" + e.OpID),
			"author": BoardpaarKonfliktAutor, "text": ersatzortText(b, e, *ort.Lane, *ort.Spalte, lane, spalte),
			"created": time.Now().UTC().Format(time.RFC3339)})
	}
	karte["comments"] = liste
	if bytes.Equal(bytes.TrimSpace(n["vertraulich"]), []byte("true")) {
		karte["confidential"] = true
	}
	p := map[string]any{"card": karte, "sprintId": lane, "columnId": spalte}
	if !nachAnker(ort.Nach, p) {
		return nil, rwFehler("nachher.nach: innere Karten-ID oder null")
	}
	return &rwPlan{op: rwOp("card.upsert", p), bindung: func(b *rwBoard) string {
		// §5.3 (W2-6): card.upsert auf Bestand ersetzte die ganze Karte —
		// deshalb unter der Sperre geprüft.
		if _, k := b.karte(e.ID); k != nil {
			return rwGrundSchonDa
		}
		// K-1: Ist der Ort unter der Sperre verschwunden, neu planen — der
		// nächste Versuch wählt den Ersatzort, statt dass der Fold die
		// Karte still im Backlog ablegt oder ziel-fehlt meldet.
		if !b.anlageLane(lane) || !b.anlageSpalte(spalte) {
			return rwGrundStandGeaendert
		}
		return ""
	}}, nil
}

// ersatzortText: Hinweis-Kommentar, wenn eine Anlage von außen nicht an
// ihren Ort konnte (Sprint-94-Kritik K-1). Der Wunschort steht mit Titel da,
// nie als rohe innere ID (Sitzungs-Kritik N-7, F-2273): Gibt es Lane oder
// Spalte innen nicht mehr, sagt der Text genau das.
func ersatzortText(b *rwBoard, e *anwendenEintrag, wunschLane, wunschSpalte, lane, spalte string) string {
	laneTitel := func(id string) string {
		if l := b.lane(id); l != nil && l.Title != "" {
			return l.Title
		}
		return "Lane ohne Titel"
	}
	// Sitzungs-QA #835 K-4: auch eine Spalte ohne Titel bekommt einen
	// Ersatztext, und eine Service-Lane wird als solche benannt.
	spaltenTitel := func(id string) string {
		if t := b.spaltenTitel(id); strings.TrimSpace(t) != "" {
			return t
		}
		return "Spalte ohne Titel"
	}
	wunschL := "eine Lane, die es innen nicht mehr gibt"
	if l := b.lane(wunschLane); l != nil {
		switch {
		case l.Archived:
			wunschL = "„" + laneTitel(wunschLane) + "“ (archiviert)"
		case l.Kind == "service":
			wunschL = "„" + laneTitel(wunschLane) + "“ (Service-Lane, wird nicht gespiegelt)"
		default:
			wunschL = "„" + laneTitel(wunschLane) + "“"
		}
	}
	wunschS := "eine Spalte, die es innen nicht mehr gibt"
	if b.spalte(wunschSpalte) {
		wunschS = "„" + spaltenTitel(wunschSpalte) + "“"
	}
	return fmt.Sprintf("⚠️ Board-Paar: %s hat diese Karte außen angelegt, gewünscht war %s / %s. Dieser Ort steht "+
		"innen nicht zur Verfügung — die Karte steht deshalb in „%s / %s“. Bei Bedarf von Hand verschieben.",
		e.Person, wunschL, wunschS, laneTitel(lane), spaltenTitel(spalte))
}

func orLeereListe(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// personenBekannt: Termin-Personen, die unter den Identitäten innen stehen.
func (b *rwBoard) personenBekannt(roh json.RawMessage) ([]string, bool, bool) {
	if istNullRoh(roh) {
		return nil, true, true
	}
	var l []string
	if json.Unmarshal(roh, &l) != nil {
		return nil, false, false
	}
	bekannt := []string{}
	alle := true
	for _, p := range l {
		if b.identitaet(p) {
			bekannt = append(bekannt, p)
		} else {
			alle = false
		}
	}
	return bekannt, alle, true
}

// baueTermin: termin.feld, vertraulich und angelegt an Terminen.
func (e *anwendenEintrag) baueTermin(b *rwBoard) (*rwPlan, *rwSofort) {
	t := b.termin(e.ID)
	if e.Art == schatten.JArtAngelegt {
		if t != nil {
			return nil, &rwSofort{ergebnis: rwSchonAngewendet}
		}
		var n map[string]json.RawMessage
		if json.Unmarshal(e.Nachher, &n) != nil || n == nil {
			return nil, rwFehler("nachher: Termin in Journal-Form")
		}
		termin := map[string]any{"id": e.ID, "boardpaarUrsprung": e.Ursprung,
			"erstellt": time.Now().UTC().Format(time.RFC3339), "erstelltVon": boardpaarAkteurPraefix + e.Person}
		for _, f := range schatten.TerminFeldliste {
			v, da := n[f]
			if !da || istNullRoh(v) {
				continue
			}
			switch f {
			case "karteId":
				// Kritik E3b K-1: nur auf eine vorhandene, nicht vertrauliche
				// Karte; sonst ohne Bezug anlegen (der Termin bliebe sonst
				// innen wirksam vertraulich und außen unsichtbar).
				if g, ok := b.karteBezug(v); !ok {
					return nil, rwFehler("nachher.karteId: innere Karten-ID oder null")
				} else if g == "" {
					termin[f] = v
				}
			case "personen":
				bekannt, _, ok := b.personenBekannt(v)
				if !ok {
					return nil, rwFehler("nachher.personen: Liste von Zeichenketten")
				}
				termin[f] = bekannt
			default:
				termin[f] = v
			}
		}
		if bytes.Equal(bytes.TrimSpace(n["vertraulich"]), []byte("true")) {
			termin["confidential"] = true
		}
		return &rwPlan{op: rwOp("termin.upsert", map[string]any{"termin": termin}), bindung: func(b *rwBoard) string {
			if b.termin(e.ID) != nil {
				return rwGrundSchonDa
			}
			if id, da := termin["karteId"]; da {
				if g, _ := b.karteBezug(id.(json.RawMessage)); g != "" {
					return rwGrundStandGeaendert // Karte inzwischen weg oder vertraulich: neu bauen
				}
			}
			return ""
		}}, nil
	}
	if t == nil {
		return nil, rwVerwirf(rwGrundTerminWeg)
	}
	if e.Art == schatten.JArtVertraulich {
		if !bytes.Equal(bytes.TrimSpace(e.Nachher), []byte("true")) {
			return nil, rwFehler("nachher: vertraulich kennt nur true")
		}
		if schatten.Vertraulich(t) {
			return nil, &rwSofort{ergebnis: rwAngewendet}
		}
		// termin.fieldIf kennt confidential nicht: der ganze Termin, an den
		// gelesenen Stand gebunden („Umsetzung 2c“ Punkt 22).
		gelesen := rwKanonisch(rwRoh(t))
		neu := map[string]json.RawMessage{}
		for k, v := range t {
			neu[k] = v
		}
		neu["confidential"] = json.RawMessage("true")
		return &rwPlan{op: rwOp("termin.upsert", map[string]any{"termin": neu}), bindung: func(b *rwBoard) string {
			if jetzt := b.termin(e.ID); jetzt != nil && rwKanonisch(rwRoh(jetzt)) != gelesen {
				return rwGrundStandGeaendert
			}
			return ""
		}}, nil
	}
	if b.terminVertraulich(t) {
		return nil, rwVerwirf(rwGrundVertraulichGeworden)
	}
	if !slices.Contains(schatten.TerminFeldliste, e.Stelle) {
		return nil, rwVerwirf(rwGrundFeldNichtErlaubt)
	}
	if e.Vorher == nil || e.Nachher == nil {
		return nil, rwFehler("vorher und nachher sind Pflicht")
	}
	if e.Stelle == "personen" {
		_, alle, ok := b.personenBekannt(e.Nachher)
		if !ok {
			return nil, rwFehler("nachher: Liste von Zeichenketten")
		}
		if !alle {
			return nil, rwVerwirf(rwGrundBearbeiterUnbekannt)
		}
	}
	if e.Stelle == "karteId" {
		g, ok := b.karteBezug(e.Nachher)
		if !ok {
			return nil, rwFehler("nachher: innere Karten-ID oder null")
		}
		if g != "" {
			return nil, rwVerwirf(g)
		}
	}
	return &rwPlan{op: rwOp("termin.fieldIf", map[string]any{"terminId": e.ID, "field": e.Stelle,
		"value": e.Nachher, "expect": e.Vorher}), bindung: func(b *rwBoard) string {
		jetzt := b.termin(e.ID)
		if jetzt == nil {
			return ""
		}
		if b.terminVertraulich(jetzt) {
			return rwGrundVertraulichGeworden
		}
		if e.Stelle == "karteId" {
			if g, _ := b.karteBezug(e.Nachher); g != "" {
				return g
			}
		}
		return ""
	}}, nil
}

// kartenFeldTypOK: Form eines Werts der Feldliste (Kritik E3b K-2). Der Fold
// prüft Kartenfelder nicht; ohne diese Tabelle käme ein Objekt als Titel bis
// ins innere Board.
func kartenFeldTypOK(feld string, roh json.RawMessage) bool {
	var v any
	if json.Unmarshal(roh, &v) != nil {
		return false
	}
	switch feld {
	case "title":
		_, ok := v.(string)
		return v == nil || ok // null wird beim Abbilden zu "" (A-4)
	case "description", "priority", "owner":
		// null: Das Journal meldet ein außen verschwundenes Feld als null
		// (wertOderNull) — sonst bliebe das Skript an dem Eintrag hängen
		// (Verify E3b V-5).
		_, ok := v.(string)
		return v == nil || ok
	case "critical", "blocked", "archived":
		_, ok := v.(bool)
		return v == nil || ok
	case "startDate", "dueDate":
		s, ok := v.(string)
		return v == nil || ok && rwDatum.MatchString(s)
	case "startTime", "dueTime":
		s, ok := v.(string)
		return v == nil || ok && rwUhrzeit.MatchString(s)
	}
	return false
}

var (
	rwDatum   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	rwUhrzeit = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`)
)

// rwOpTyp: der Op-Typ, den baue für einen Eintrag bildet ("" ohne Op).
func rwOpTyp(e *anwendenEintrag) string {
	if e.Objekt == schatten.ArtTermin {
		switch e.Art {
		case schatten.JArtTerminFeld:
			return "termin.fieldIf"
		case schatten.JArtAngelegt, schatten.JArtVertraulich:
			return "termin.upsert"
		}
		return ""
	}
	return map[string]string{schatten.JArtFeld: "card.fieldIf", schatten.JArtVertraulich: "card.field",
		schatten.JArtVerschoben: "card.moveIf", schatten.JArtReihenfolge: "card.moveIf", schatten.JArtListeAdd: "card.listAdd",
		schatten.JArtListeRemove: "card.listRemove", schatten.JArtCheckpunkt: "card.checkItemIf",
		schatten.JArtKommentarAdd: "comment.add", schatten.JArtKommentarEdit: "comment.editIf",
		schatten.JArtAngelegt: "card.upsert"}[e.Art]
}

func rwOp(typ string, payload map[string]any) map[string]any {
	return map[string]any{"type": typ, "payload": payload}
}

// rwKanonisch: JSON mit sortierten Schlüsseln (Go sortiert Map-Schlüssel).
func rwKanonisch(r json.RawMessage) string {
	var v any
	if json.Unmarshal(r, &v) != nil {
		return string(r)
	}
	aus, _ := json.Marshal(v)
	return string(aus)
}

// rwRoh: Wert als JSON (für Vergleiche und Payloads).
func rwRoh(v any) json.RawMessage {
	aus, _ := json.Marshal(v)
	return aus
}

func gleichJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// ── Anwenden ──────────────────────────────────────────────────────────────

// rwAntwort: Ergebnis des Anwendens. status ≠ 200: Fehler (body steht schon).
type rwAntwort struct {
	status int
	body   map[string]any
	kopf   http.Header
}

func rwErgebnis(ergebnis, grund string) rwAntwort {
	body := map[string]any{"ergebnis": ergebnis}
	if grund != "" {
		body["grund"] = grund
	}
	return rwAntwort{status: http.StatusOK, body: body}
}

func rwFehlerAntwort(status int, text string) rwAntwort {
	return rwAntwort{status: status, body: map[string]any{"error": text}}
}

// rueckwegPostOps schickt eine Op durch postOps — mit den Merkmalen des
// Rückwegs (ADR-0141 §5.3) und der Vorbedingung unter der Sperre.
func (a *BoardsAPI) rueckwegPostOps(ctx context.Context, r *http.Request, boardID, clientID string, op map[string]any,
	bindung func(*rwBoard) string) (*pufferAntwort, error) {
	op["id"] = clientID
	rumpf, err := json.Marshal(map[string]any{"ops": []map[string]any{op}})
	if err != nil {
		return nil, err
	}
	vorbedingung := func(state []byte) error {
		b, err := leseRwBoard(state)
		if err != nil {
			return &opsVorbedingungVerletzt{grund: "stand-unlesbar"}
		}
		if g := bindung(b); g != "" {
			return &opsVorbedingungVerletzt{grund: g}
		}
		return nil
	}
	r2 := r.Clone(mitOpsVorbedingung(ctx, vorbedingung))
	r2.Method = http.MethodPost
	r2.Header = r.Header.Clone()
	r2.Header.Del(SitzungHeader)
	r2.Body = io.NopCloser(bytes.NewReader(rumpf))
	r2.ContentLength = int64(len(rumpf))
	r2.SetPathValue("id", boardID)
	puffer := &pufferAntwort{kopf: http.Header{}}
	a.postOps(puffer, r2)
	return puffer, nil
}

// vorbedingungGrund: der Grund einer 409-Vorbedingung von postOps, sonst "".
func vorbedingungGrund(p *pufferAntwort) string {
	if p.status != http.StatusConflict {
		return ""
	}
	var v struct {
		Art   string `json:"art"`
		Grund string `json:"grund"`
	}
	if json.Unmarshal(p.body.Bytes(), &v) != nil || v.Art != opsVorbedingungArt {
		return ""
	}
	return v.Grund
}

// rwBekannteArten: Protokollarten des Folds, die §5.3 zuordnet.
var rwBekannteArten = map[string]bool{"feld-geaendert": true, "ziel-fehlt": true, "klemmung": true,
	"wirkungslos": true, "entfernt": true}

// rwNachVersuchNaht: Test-Naht (Kritik E3b K-8), im Betrieb nil — läuft
// zwischen Lesen und Sperre eines Versuchs.
var rwNachVersuchNaht func(versuch int)

// boardpaarAnwenden wendet einen Eintrag an. ctx trägt die Rückweg-Merkmale.
func (a *BoardsAPI) boardpaarAnwenden(ctx context.Context, r *http.Request, boardID string, e *anwendenEintrag) rwAntwort {
	ctx = mitBoardpaarAkteur(mitBoardpaarRueckweg(ctx), e.Person)
	// Zuerst die Idempotenz: angenommen → schon angewendet; Konflikt-Kommentar
	// gemerkt → konflikt, ohne die Bedingung erneut zu prüfen (§5.3). Ein
	// anderer Eintrag unter derselben op_id ist 409.
	eintrag, err := json.Marshal(e)
	if err != nil {
		return rwFehlerAntwort(http.StatusInternalServerError, "entry encode failed")
	}
	eintrag = json.RawMessage(rwKanonisch(eintrag))
	ctx = mitRueckwegHash(ctx, eintrag)
	actor := actorFromContext(ctx)
	for _, k := range []struct{ id, typ, ergebnis string }{
		{e.OpID + "-k", "comment.add", rwKonflikt}, {e.OpID, rwOpTyp(e), rwSchonAngewendet}} {
		h, bekannt, err := a.repo.OpIDHash(ctx, boardID, k.id)
		if err != nil {
			return rwFehlerAntwort(http.StatusInternalServerError, "lookup failed")
		}
		if !bekannt {
			continue
		}
		if h != "" && h != opHash(serverOp{ClientID: k.id, Actor: actor, Type: k.typ, Payload: eintrag, hashNutzlast: eintrag}) {
			return rwFehlerAntwort(http.StatusConflict, "op_id was already used for a different entry")
		}
		return rwErgebnis(k.ergebnis, "")
	}
	for versuch := 0; versuch < rwVersuche; versuch++ {
		gelesen, err := a.repo.Get(ctx, boardID)
		if errors.Is(err, storage.ErrBoardNotFound) {
			return rwFehlerAntwort(http.StatusNotFound, "not found")
		}
		if err != nil {
			return rwFehlerAntwort(http.StatusInternalServerError, "board read failed")
		}
		b, err := leseRwBoard(gelesen.StateJSON)
		if err != nil {
			return rwFehlerAntwort(http.StatusInternalServerError, "board state unreadable")
		}
		plan, sofort := e.baue(b)
		if sofort != nil {
			if sofort.fehler != "" {
				return rwFehlerAntwort(http.StatusUnprocessableEntity, sofort.fehler)
			}
			return rwErgebnis(sofort.ergebnis, sofort.grund)
		}
		if plan.op["type"] != rwOpTyp(e) {
			// Der Hash der Vorprüfung oben rechnet mit rwOpTyp — beide müssen
			// übereinstimmen, sonst erkennte sie eine Wiederholung nicht.
			return rwFehlerAntwort(http.StatusInternalServerError, "op type mismatch")
		}
		if rwNachVersuchNaht != nil {
			rwNachVersuchNaht(versuch)
		}
		p, err := a.rueckwegPostOps(ctx, r, boardID, e.OpID, plan.op, plan.bindung)
		if err != nil {
			return rwFehlerAntwort(http.StatusInternalServerError, "op encode failed")
		}
		switch grund := vorbedingungGrund(p); grund {
		case "":
		case rwGrundStandGeaendert:
			continue
		case rwGrundSchonDa:
			return rwErgebnis(rwSchonAngewendet, "")
		case rwGrundVertraulichGeworden, rwGrundKommentarNichtErlaubt, rwGrundKommentarIDBelegt, rwGrundKarteWeg:
			return rwErgebnis(rwVerworfen, grund)
		default:
			return rwFehlerAntwort(http.StatusInternalServerError, "precondition failed: "+grund)
		}
		return a.rwDeute(ctx, r, boardID, e, plan, p)
	}
	return rwAntwort{status: http.StatusServiceUnavailable, body: map[string]any{"error": "board changes too fast, retry later"},
		kopf: http.Header{"Retry-After": []string{"5"}}}
}

// rwDeute ordnet die Antwort von postOps einem Ergebnis zu (§5.3, Punkt 21).
func (a *BoardsAPI) rwDeute(ctx context.Context, r *http.Request, boardID string, e *anwendenEintrag, plan *rwPlan, p *pufferAntwort) rwAntwort {
	switch p.status {
	case http.StatusOK:
		var q struct {
			Wiederholt bool `json:"wiederholt"`
		}
		_ = json.Unmarshal(p.body.Bytes(), &q)
		if q.Wiederholt {
			return rwErgebnis(rwSchonAngewendet, "")
		}
		return rwErgebnis(rwAngewendet, "")
	case http.StatusUnprocessableEntity:
		protokoll := opsProtokollArten(p)
		if protokoll == nil {
			// 422 ohne Protokoll: Hülle verletzt o. ä. — kein Ergebnis aus §5.3.
			return rwAntwort{status: http.StatusInternalServerError, body: map[string]any{"error": "ops rejected",
				"details": json.RawMessage(p.body.Bytes())}}
		}
		for _, eintrag := range protokoll {
			if art := zeichenketteVon(eintrag["art"]); !rwBekannteArten[art] {
				// VF-11: jede andere Protokollart ist ein Fehler — Skript HALT.
				return rwAntwort{status: http.StatusInternalServerError, body: map[string]any{
					"error": "unexpected fold protocol", "protokollart": art}}
			}
		}
		for _, eintrag := range protokoll {
			art := zeichenketteVon(eintrag["art"])
			if e.Art == schatten.JArtAngelegt && (art == "ziel-fehlt" || art == "klemmung") {
				// Sitzungs-Kritik N-4 (F-2271), Abwehr in der Tiefe zu ADR-0141
				// Nachtrag Punkt 1: ein verworfenes angelegt bestätigte das
				// Skript, und der nächste Hinweg entfernte die wartende Karte
				// (bzw. den Termin) außen. baueAnlage wählt den Ort unter der
				// Sperre, hier kommt also nur ein Fehler an — HALT.
				return rwAntwort{status: http.StatusInternalServerError, body: map[string]any{
					"error": "angelegt not placed (fold: " + art + ")", "protokoll": protokoll}}
			}
			switch art {
			case "wirkungslos":
				// „Umsetzung 2c“ Punkt 21: eine Regelverletzung, kein „schon so“.
				return rwAntwort{status: http.StatusUnprocessableEntity, body: map[string]any{
					"error": "entry does not apply: " + zeichenketteVon(eintrag["grund"]), "protokoll": protokoll}}
			case "ziel-fehlt":
				switch {
				case e.Objekt == schatten.ArtTermin:
					return rwErgebnis(rwVerworfen, rwGrundTerminWeg)
				case zeichenketteVon(eintrag["commentId"]) != "":
					return rwErgebnis(rwVerworfen, rwGrundKommentarWeg)
				case zeichenketteVon(eintrag["sprintId"]) != "":
					// Sprint-94-Kritik V-1: die Ziel-Lane fehlt, nicht die
					// Karte — „karte-weg“ läse das Skript als „innen gelöscht“.
					return rwErgebnis(rwVerworfen, rwGrundLaneUnbekannt)
				}
				return rwErgebnis(rwVerworfen, rwGrundKarteWeg)
			case "klemmung":
				// Klemmung = die Zielspalte gibt es nicht (Sitzungs-QA E3b A-3).
				return rwErgebnis(rwVerworfen, rwGrundSpalteUnbekannt)
			case "entfernt":
				return rwErgebnis(rwVerworfen, rwGrundLoeschenNichtErlaubt)
			}
		}
		// Nur noch feld-geaendert.
		if e.Art == schatten.JArtFeld {
			// Innen steht schon derselbe Wert: angewendet, kein Konflikt
			// („Umsetzung 2c“ Punkt 21).
			if akt, ok := protokoll[0]["aktuell"]; ok && gleichJSON(akt, e.Nachher) {
				return rwErgebnis(rwAngewendet, "")
			}
		}
		return a.rwKonflikt(ctx, r, boardID, e)
	case http.StatusBadRequest:
		if bytes.Contains(p.body.Bytes(), []byte(opIDWiederverwendetText)) {
			// Dieselbe Op-ID mit anderem Inhalt (z. B. nach einem Rücksprung
			// von seq außen, §5.1) — HALT.
			return rwFehlerAntwort(http.StatusConflict, "op_id was already used for a different entry")
		}
		return rwAntwort{status: http.StatusUnprocessableEntity, body: map[string]any{
			"error": "entry does not make a valid op", "details": json.RawMessage(p.body.Bytes())}}
	}
	// 404, 413, 429, 503 …: durchreichen.
	var body map[string]any
	if json.Unmarshal(p.body.Bytes(), &body) != nil {
		body = map[string]any{"error": strings.TrimSpace(p.body.String())}
	}
	kopf := http.Header{}
	if ra := p.kopf.Get("Retry-After"); ra != "" {
		kopf.Set("Retry-After", ra)
	}
	return rwAntwort{status: p.status, body: body, kopf: kopf}
}

// rwKonflikt: Schritt 2 — der Konflikt-Kommentar als Op „<Op-ID>-k“ (§6).
func (a *BoardsAPI) rwKonflikt(ctx context.Context, r *http.Request, boardID string, e *anwendenEintrag) rwAntwort {
	gelesen, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return rwFehlerAntwort(http.StatusInternalServerError, "board read failed")
	}
	b, err := leseRwBoard(gelesen.StateJSON)
	if err != nil {
		return rwFehlerAntwort(http.StatusInternalServerError, "board state unreadable")
	}
	ziel := e.ID
	if e.Objekt == schatten.ArtTermin {
		// „Umsetzung 2c“ Punkt 24: an die Karte des Termins, sonst ohne
		// Kommentar.
		ziel = ""
		// Ist der Termin inzwischen vertraulich, prüft das bindungKonflikt
		// unter der Sperre (verworfen/vertraulich-geworden — dasselbe Ergebnis
		// wie eine Wiederholung; Sitzungs-QA E3b A-1).
		if t := b.termin(e.ID); t != nil {
			if id := zeichenketteVon(t["karteId"]); id != "" {
				if _, k := b.karte(id); k != nil && !schatten.Vertraulich(k) {
					ziel = id
				}
			}
		}
		if ziel == "" {
			return rwErgebnis(rwKonflikt, "")
		}
	}
	kommentar := map[string]any{"id": schatten.AbgeleiteteID("konflikt:" + e.OpID), "author": BoardpaarKonfliktAutor,
		"text": konfliktText(b, e), "created": time.Now().UTC().Format(time.RFC3339)}
	p, err := a.rueckwegPostOps(ctx, r, boardID, e.OpID+"-k", rwOp("comment.add",
		map[string]any{"cardId": ziel, "comment": kommentar}), bindungKonflikt(ziel, e))
	if err != nil {
		return rwFehlerAntwort(http.StatusInternalServerError, "op encode failed")
	}
	if g := vorbedingungGrund(p); g != "" {
		if g == rwGrundVertraulichGeworden {
			return rwErgebnis(rwVerworfen, g)
		}
		return rwFehlerAntwort(http.StatusInternalServerError, "precondition failed: "+g)
	}
	if p.status == http.StatusUnprocessableEntity {
		for _, eintrag := range opsProtokollArten(p) {
			if zeichenketteVon(eintrag["art"]) == "ziel-fehlt" {
				// Die Karte ist zwischen den Schritten verschwunden.
				return rwErgebnis(rwVerworfen, rwGrundKarteWeg)
			}
		}
	}
	if p.status != http.StatusOK {
		erg := a.rwDeute(ctx, r, boardID, e, nil, p)
		if erg.status == http.StatusOK {
			// Kann nicht vorkommen (200 ist oben behandelt); sicherheitshalber
			// kein „angewendet“ für einen Konflikt melden.
			return rwFehlerAntwort(http.StatusInternalServerError, "conflict comment not written")
		}
		return erg
	}
	return rwErgebnis(rwKonflikt, "")
}

// bindungKonflikt: §9 — ist die Zielkarte inzwischen vertraulich, kein
// Konflikt-Kommentar.
func bindungKonflikt(ziel string, e *anwendenEintrag) func(*rwBoard) string {
	return func(b *rwBoard) string {
		if _, k := b.karte(ziel); k != nil && schatten.Vertraulich(k) {
			return rwGrundVertraulichGeworden
		}
		if e.Objekt == schatten.ArtTermin {
			if t := b.termin(e.ID); t != nil && b.terminVertraulich(t) {
				return rwGrundVertraulichGeworden
			}
		}
		return ""
	}
}

// ── Konflikt-Kommentar (§6) ───────────────────────────────────────────────

var rwStellenName = map[string]string{
	"title": "den Titel", "description": "die Beschreibung", "priority": "die Priorität",
	"critical": "„kritisch“", "blocked": "„blockiert“", "owner": "den Verantwortlichen",
	"startDate": "das Startdatum", "startTime": "die Startzeit", "dueDate": "die Fälligkeit",
	"dueTime": "die Uhrzeit der Fälligkeit", "archived": "„archiviert“",
}

var rwTerminStellenName = map[string]string{
	"titel": "den Titel des Termins", "beschreibung": "die Beschreibung des Termins", "datum": "das Datum des Termins",
	"bisDatum": "das Enddatum des Termins", "von": "den Beginn des Termins", "bis": "das Ende des Termins",
	"ort": "den Ort des Termins", "personen": "die Personen des Termins", "karteId": "die Karte des Termins",
}

// konfliktText baut den Kommentar nach §6.
func konfliktText(b *rwBoard, e *anwendenEintrag) string {
	stelle, wert := e.Stelle, konfliktWert(e.Nachher)
	switch e.Art {
	case schatten.JArtFeld:
		if n, ok := rwStellenName[e.Stelle]; ok {
			stelle = n
		}
	case schatten.JArtTerminFeld:
		if n, ok := rwTerminStellenName[e.Stelle]; ok {
			stelle = n
		}
	case schatten.JArtVerschoben:
		stelle = "Lane und Spalte"
		if o, ok := liesOrt(e.Nachher, true); ok {
			laneTitel := *o.Lane
			if l := b.lane(*o.Lane); l != nil && l.Title != "" {
				laneTitel = l.Title
			}
			wert = laneTitel + " / " + b.spaltenTitel(*o.Spalte)
		}
	case schatten.JArtCheckpunkt:
		var n struct {
			Text     string `json:"text"`
			Erledigt bool   `json:"erledigt"`
		}
		_ = json.Unmarshal(e.Nachher, &n)
		stelle, wert = "den Checklisten-Punkt „"+n.Text+"“", "offen"
		if n.Erledigt {
			wert = "erledigt"
		}
	case schatten.JArtKommentarEdit:
		stelle = "einen Kommentar von außen"
	}
	zeit := e.Zeit
	if t, err := time.Parse(time.RFC3339Nano, e.Zeit); err == nil {
		zeit = t.UTC().Format("02.01.2006 15:04") + " UTC"
	}
	return fmt.Sprintf("⚠️ Konflikt über das Board-Paar: %s hat außen am %s %s geändert auf: %s. Innen war diese Stelle "+
		"inzwischen schon geändert — es gilt die innere Fassung. Bei Bedarf von Hand übernehmen.", e.Person, zeit, stelle, wert)
}

// konfliktWert: ein Wert lesbar, lange Werte gekürzt mit Hinweis.
func konfliktWert(r json.RawMessage) string {
	var v any
	if json.Unmarshal(r, &v) != nil {
		return string(r)
	}
	var s string
	switch x := v.(type) {
	case nil:
		s = "(leer)"
	case string:
		s = x
		if s == "" {
			s = "(leer)"
		}
	case bool:
		s = "nein"
		if x {
			s = "ja"
		}
	case []any:
		teile := make([]string, 0, len(x))
		for _, t := range x {
			if ts, ok := t.(string); ok {
				teile = append(teile, ts)
			} else {
				aus, _ := json.Marshal(t)
				teile = append(teile, string(aus))
			}
		}
		s = strings.Join(teile, ", ")
		if s == "" {
			s = "(leer)"
		}
	default:
		aus, _ := json.Marshal(x)
		s = string(aus)
	}
	if utf8.RuneCountInString(s) > rwKonfliktWertMax {
		s = string([]rune(s)[:rwKonfliktWertMax]) + " … (gekürzt; vollständig auf dem externen Board)"
	}
	return s
}

// ── Handler ───────────────────────────────────────────────────────────────

// MitRueckweg verdrahtet den Ops-Weg für POST /boardpaar/anwenden (innen).
func (a *BoardpaarKopieAPI) MitRueckweg(b *BoardsAPI) *BoardpaarKopieAPI {
	a.ops = b
	return a
}

// anwenden — POST /boardpaar/anwenden/{boardId} (innen, abr_).
func (a *BoardpaarKopieAPI) anwenden(w http.ResponseWriter, r *http.Request) {
	const weg = "boardpaar-anwenden"
	ctx, cancel := context.WithTimeout(r.Context(), 2*requestTimeout)
	defer cancel()
	if !a.seite(w, ctx, BoardpaarSeiteInnen) {
		return
	}
	if a.ops == nil || a.ops.fold == nil {
		writeError(w, http.StatusInternalServerError, "not wired")
		return
	}
	z := a.zugang(w, r, storage.BoardpaarTypRueckweg, weg)
	if z == nil {
		return
	}
	boardID := r.PathValue("boardId")
	gekoppelt, err := a.paar.InnenGekoppelt(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	beauftragt := false
	if gekoppelt {
		if beauftragt, err = a.paar.HatAuftrag(ctx, boardID); err != nil {
			writeError(w, http.StatusInternalServerError, "lookup failed", err)
			return
		}
	}
	if !gekoppelt || !beauftragt {
		log.Printf("%s: zugang=%q board=%q → 404 (nicht gekoppelt, unbekannt oder ohne auftrag)", weg, z.Name, kurzeKennung(boardID))
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var e anwendenEintrag
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxOpsBodyBytes))
	dec.DisallowUnknownFields()
	err = dec.Decode(&e)
	var zuGross *http.MaxBytesError
	if errors.As(err, &zuGross) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", zuGross.Limit))
		return
	}
	if err == nil {
		// Nach dem Objekt darf nichts mehr kommen (Kritik E3b K-7).
		if _, weiter := dec.Token(); weiter != io.EOF {
			err = errors.New("daten nach dem eintrag")
		}
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "body: erwartet ein Rückweg-Eintrag (ADR-0141, Umsetzung 2c Punkt 20)")
		return
	}
	if grund := e.pruefe(); grund != "" {
		writeError(w, http.StatusUnprocessableEntity, grund)
		return
	}
	erg := a.ops.boardpaarAnwenden(ctx, r.WithContext(ctx), boardID, &e)
	log.Printf("%s: zugang=%q board=%s op=%q art=%s → %d %v", weg, z.Name, boardID, e.OpID, e.Art, erg.status, erg.body["ergebnis"])
	for k, v := range erg.kopf {
		w.Header()[k] = v
	}
	writeJSON(w, erg.status, erg.body)
}
