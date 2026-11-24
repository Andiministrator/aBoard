package api

// agent_loeschen.go — Löschen über die Agenten-Werkzeuge, zweistufig
// (ADR-0139 E-2, Karte #757).
//
// Je Objektart (Karte, Swimlane, Termin, Anhang, Spalte) gibt es ein Paar:
//
//   …_loeschen_pruefen (GET, nur lesend) — Vorschau und Bestätigungscode;
//   …_loeschen         (POST, zerstörerisch) — löscht nur mit gültigem Code.
//
// Der Code ist ein HMAC mit einem Server-Geheimnis (Datenbank, Migration
// 0032) über Token-ID, Person, Board-ID, Objektart, Objekt-ID, Ablauf,
// Zufallswert — und, in einem zweiten Teil, über den Stand des Objekts. Er
// gilt 10 Minuten und nur einmal (Verbrauchsliste). Jede Änderung am Objekt
// macht ihn ungültig: Bestätigt wurde, was gezeigt wurde.
//
// Zeitlücke Stand-Prüfung ↔ Op: Der Stand wird unmittelbar vor dem Schreiben
// noch einmal geprüft, und zwar in postOps unter derselben Board-Sperre, die
// auch das Falten hält (opsVorbedingung). Zwischen Prüfung und COMMIT ändert
// so niemand das Objekt.
//
// Was der Code NICHT leistet (ADR-0139 Konsequenzen): Er beweist nicht, dass
// ein Mensch zugestimmt hat — ein Modell kann beide Schritte selbst aufrufen.
// Er schützt vor dem Versehen, nicht vor einem gesteuerten Modell; dafür
// bleibt der rohe Weg (card.delete/swimlane.delete) ohnehin offen (E-2).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// loeschCodeGueltig: so lange gilt ein Bestätigungscode (ADR-0139 E-2).
	loeschCodeGueltig = 10 * time.Minute
	// loeschCodePraefix: erkennbar im Chat, versioniert.
	loeschCodePraefix = "lc1-"
	// loeschCodeMacLaenge: Bytes je HMAC-Teil im Code (96 Bit).
	loeschCodeMacLaenge = 12
)

// loeschCodeUhr: Zeitquelle der Codes (Test-Naht für den Ablauf).
var loeschCodeUhr = time.Now

// werkzeugVorDemSchreiben ist eine Test-Naht (Karte #757): Im Produktivbetrieb
// nil. Läuft in den Lösch-Werkzeugen und in board_aendern nach dem Lesen und
// Prüfen, unmittelbar bevor die Ops an postOps gehen — ein Test ändert dort
// das Board und prüft, dass die Vorbedingung unter der Sperre es bemerkt.
var werkzeugVorDemSchreiben func()

// --- Kontext: Werkzeug-Weg und Vorbedingung --------------------------------

type werkzeugWegSchluessel struct{}
type opsVorbedingungSchluessel struct{}

// werkzeugWegAus: Die Ops dieser Anfrage kommen von einem Werkzeug
// (werkzeugOpsRoh), nicht über POST /boards/{id}/ops. Der Schlüssel ist
// unexportiert; von außen lässt er sich nicht setzen.
func werkzeugWegAus(ctx context.Context) bool {
	v, _ := ctx.Value(werkzeugWegSchluessel{}).(bool)
	return v
}

type anlageOffenSchluessel struct{}

// mitAnlageOffen kennzeichnet einen Werkzeug-Aufruf, der beim Anlegen
// AUSDRÜCKLICH `vertraulich: false` gesendet hat (Karte #797, ADR-0141 W-A).
// Nur karte_anlegen und termin_anlegen setzen es, und nur nach dieser
// Prüfung; postOps nimmt dann für diesen Aufruf die Regel W-4 (Agenten-Anlage
// innen ist vertraulich) zurück. Der Schlüssel ist unexportiert; eine Nutzlast
// kann ihn nicht setzen — auf dem rohen Weg (POST /boards/{id}/ops) bleibt
// ein mitgeschicktes `false` wirkungslos (Fold-Kommentar neuAnlage).
func mitAnlageOffen(ctx context.Context) context.Context {
	return context.WithValue(ctx, anlageOffenSchluessel{}, true)
}

func anlageOffenAus(ctx context.Context) bool {
	v, _ := ctx.Value(anlageOffenSchluessel{}).(bool)
	return v
}

// mitOpsVorbedingung: postOps ruft f unter der Board-Sperre mit dem Stand
// vor den Ops auf; ein Fehler bricht ab, ohne zu schreiben (409).
func mitOpsVorbedingung(ctx context.Context, f func(state []byte) error) context.Context {
	return context.WithValue(ctx, opsVorbedingungSchluessel{}, f)
}

func opsVorbedingungAus(ctx context.Context) func(state []byte) error {
	f, _ := ctx.Value(opsVorbedingungSchluessel{}).(func(state []byte) error)
	return f
}

// opsVorbedingungArt: die art der 409-Antwort von postOps.
const opsVorbedingungArt = "vorbedingung"

type opsVorbedingungVerletzt struct{ grund string }

func (e *opsVorbedingungVerletzt) Error() string { return "vorbedingung verletzt: " + e.grund }

// --- Sperre auf dem rohen Weg (ADR-0139 E-3) -------------------------------

// agentRohSperre: Auf dem rohen Weg (POST /boards/{id}/ops) schreibt ein
// Agent die Board-Struktur nicht — Titel, Spalten, Hintergrund und Vorlagen
// ändert nur board_aendern (owner-Person) bzw. spalte_loeschen. `identities`
// sperrt agentPayload schon für jeden Weg; `attachments` bleibt offen
// (ADR-0139 §4). Werkzeuge kennzeichnen ihre Anfragen (werkzeugWegAus) und
// sind ausgenommen (V-2 aus F-1849).
func agentRohSperre(typ string, payload json.RawMessage) string {
	if typ != "board.field" {
		return ""
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	switch zeichenketteVon(p["field"]) {
	case "title", "columnDefs", "background", "templates":
		return "agents may not change the board structure (title, columnDefs, background, templates) via raw ops; use the tool board_aendern (owner only)"
	}
	return ""
}

// --- Stand-Kennung ---------------------------------------------------------

// standKennung: Hash über die kanonische Form eines Werts (Schlüssel
// sortiert, Zahlen wörtlich). Jede Änderung — auch ein neuer Kommentar —
// ergibt eine andere Kennung.
func standKennung(teile ...any) string {
	h := sha256.New()
	for _, t := range teile {
		var v any = t
		if roh, ok := t.(json.RawMessage); ok {
			v = kanonischerWert(roh)
		}
		b, _ := json.Marshal(v)
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// kanonischerWert: JSON mit json.Number statt float64 (nichts wird gerundet).
// Beim erneuten Marshal sortiert Go die Schlüssel jeder Map.
func kanonischerWert(roh json.RawMessage) any {
	if len(roh) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(roh))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return string(roh)
	}
	return v
}

// --- Ziel einer Löschung ---------------------------------------------------

// loeschZiel: was gelöscht würde — ermittelt aus dem Roh-Stand, damit
// dieselbe Funktion vor der Sperre (Vorschau, Code) und unter der Sperre
// (Vorbedingung) gilt.
type loeschZiel struct {
	art, objektID string
	stand         string
	vorschau      map[string]any
	ops           []map[string]any
	// dateien: Anhänge, deren Dateien nach dem COMMIT wegfallen
	// (Karten-id bzw. boardAttachmentCardID, Anhang-id; Anhang-id leer =
	// alle Dateien der Karte).
	dateien [][2]string
}

// loeschFehler: Status und Satz für das Modell.
type loeschFehler struct {
	status  int
	meldung string
	mehr    map[string]any
}

func (f *loeschFehler) schreibe(w http.ResponseWriter) {
	aus := map[string]any{"error": f.meldung}
	for k, v := range f.mehr {
		aus[k] = v
	}
	writeJSON(w, f.status, aus)
}

// loeschArt: eine Objektart mit Ermittlung aus dem Roh-Stand.
type loeschArt struct {
	art       string // in der Code-Bindung
	name      string // Werkzeug-Präfix: <name>_loeschen(_pruefen)
	pfadParam string // Pfad-Parameter mit der Bezeichnung
	nurOwner  bool   // Board-Struktur (Spalte): Person muss owner sein
	// ermittle: Ziel zu ref (wie das Modell es nennt). exakt: ref ist die
	// Objekt-ID aus einem früheren Aufruf (Vorbedingung) — keine Namen.
	ermittle func(a *BoardsAPI, boardID string, stand rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler)
}

// rohStand: der Board-Stand mit exakten Schlüsseln (wie der Fold).
type rohStand struct {
	board map[string]json.RawMessage
	lanes []map[string]json.RawMessage
}

func leseRohStand(state []byte) (rohStand, bool) {
	var s rohStand
	var huelle map[string]json.RawMessage
	if json.Unmarshal(state, &huelle) != nil {
		return s, false
	}
	if json.Unmarshal(huelle["board"], &s.board) != nil || s.board == nil {
		return s, false
	}
	_ = json.Unmarshal(s.board["sprints"], &s.lanes)
	return s, true
}

func (s rohStand) karten(lane map[string]json.RawMessage) []map[string]json.RawMessage {
	var k []map[string]json.RawMessage
	_ = json.Unmarshal(lane["cards"], &k)
	return k
}

func zaehleListe(roh json.RawMessage) int {
	var l []json.RawMessage
	_ = json.Unmarshal(roh, &l)
	return len(l)
}

func nichtGefunden(was string) *loeschFehler {
	return &loeschFehler{status: http.StatusNotFound, meldung: was}
}

// --- Karte -----------------------------------------------------------------

var loeschKarte = loeschArt{art: "karte", name: "karte", pfadParam: "karte",
	ermittle: func(a *BoardsAPI, boardID string, s rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler) {
		type treffer struct {
			karte, lane map[string]json.RawMessage
		}
		ref = strings.TrimSpace(ref)
		nr := strings.TrimPrefix(ref, "#")
		n, nErr := strconv.Atoi(nr)
		alsNummer := !exakt && nErr == nil && n > 0 && strconv.Itoa(n) == nr
		var gefunden []treffer
		for _, alsID := range []bool{false, true} {
			if !alsID && !alsNummer {
				continue
			}
			for _, l := range s.lanes {
				if l == nil || zeichenketteVon(l["kind"]) == "service" {
					continue // Mail-Eingang: für Werkzeuge nicht vorhanden (K1-6)
				}
				for _, k := range s.karten(l) {
					if k == nil {
						continue
					}
					if (!alsID && wkZahl(k["displayId"]) == n) || (alsID && zeichenketteVon(k["id"]) == ref) {
						gefunden = append(gefunden, treffer{k, l})
					}
				}
			}
			if len(gefunden) > 0 {
				break
			}
		}
		switch {
		case len(gefunden) == 0:
			return nil, nichtGefunden("Karte " + strconv.Quote(ref) + " gibt es auf diesem Board nicht (mehr). Die Nummer nennt board_lesen.")
		case len(gefunden) > 1:
			ids := make([]string, len(gefunden))
			for i, t := range gefunden {
				ids[i] = zeichenketteVon(t.karte["id"])
			}
			return nil, &loeschFehler{status: http.StatusBadRequest, meldung: "Mehrere Karten tragen die Nummer " + strconv.Quote(ref) + " — bitte die id angeben: " + strings.Join(ids, ", ")}
		}
		k, l := gefunden[0].karte, gefunden[0].lane
		if zeichenketteVon(k["kind"]) == "sprint-meta" {
			return nil, &loeschFehler{status: http.StatusForbidden, meldung: "Die Karten 📋 Sprint-Start und 🏁 Sprint-Abschluss lassen sich nicht löschen (ADR-0036)."}
		}
		id := zeichenketteVon(k["id"])
		// Rückverweise anderer Karten auf diese räumt der Browser beim Löschen
		// mit ab (Links.purgeLinksTo, ADR-0032) — das Werkzeug ebenso, sonst
		// blieben hängende Verweise. Sie gehören zum Stand: Kommt einer dazu,
		// gilt der Code nicht mehr.
		var ops []map[string]any
		var rueck []any
		for _, andere := range s.lanes {
			for _, ak := range s.karten(andere) {
				if ak == nil || zeichenketteVon(ak["id"]) == id {
					continue
				}
				var links []json.RawMessage
				_ = json.Unmarshal(ak["links"], &links)
				for _, lr := range links {
					var lm map[string]json.RawMessage
					if json.Unmarshal(lr, &lm) != nil || zeichenketteVon(lm["targetId"]) != id {
						continue
					}
					ops = append(ops, map[string]any{"type": "card.listRemove", "payload": map[string]any{"cardId": zeichenketteVon(ak["id"]), "field": "links", "item": lr}})
					rueck = append(rueck, []any{zeichenketteVon(ak["id"]), kanonischerWert(lr)})
				}
			}
		}
		ops = append(ops, map[string]any{"type": "card.delete", "payload": map[string]any{"cardId": id}})
		kRoh, _ := json.Marshal(k)
		return &loeschZiel{art: "karte", objektID: id, stand: standKennung(json.RawMessage(kRoh), rueck),
			vorschau: map[string]any{"displayId": wkZahl(k["displayId"]), "titel": zeichenketteVon(k["title"]),
				"swimlane": swimlaneTitelRoh(l), "spalte": spaltenTitelRoh(s, zeichenketteVon(k["columnId"])),
				"archiviert": jsWahr(k["archived"]), "kommentare": zaehleListe(k["comments"]), "anhaenge": zaehleListe(k["attachments"]),
				"verweise": zaehleListe(k["links"]), "rueckverweise": len(rueck), "link": a.kartenLink(boardID, wkZahl(k["displayId"]))},
			ops:     ops,
			dateien: [][2]string{{id, ""}},
		}, nil
	}}

func swimlaneTitelRoh(l map[string]json.RawMessage) string {
	if t := zeichenketteVon(l["title"]); t != "" {
		return t
	}
	return zeichenketteVon(l["id"])
}

func spaltenTitelRoh(s rohStand, id string) string {
	var defs []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["columnDefs"], &defs)
	for _, d := range defs {
		if zeichenketteVon(d["id"]) == id {
			if t := zeichenketteVon(d["title"]); t != "" {
				return t
			}
		}
	}
	return id
}

// --- Swimlane --------------------------------------------------------------

var loeschSwimlane = loeschArt{art: "swimlane", name: "swimlane", pfadParam: "swimlane",
	ermittle: func(a *BoardsAPI, boardID string, s rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler) {
		ref = strings.TrimSpace(ref)
		var gefunden []map[string]json.RawMessage
		for _, alsName := range []bool{false, true} {
			if alsName && exakt {
				break
			}
			for _, l := range s.lanes {
				if l == nil {
					continue
				}
				if (!alsName && zeichenketteVon(l["id"]) == ref) || (alsName && strings.EqualFold(strings.TrimSpace(zeichenketteVon(l["title"])), ref)) {
					gefunden = append(gefunden, l)
				}
			}
			if len(gefunden) > 0 {
				break
			}
		}
		switch {
		case len(gefunden) == 0:
			return nil, nichtGefunden("Swimlane " + strconv.Quote(ref) + " gibt es auf diesem Board nicht (mehr). Swimlanes nennt board_lesen.")
		case len(gefunden) > 1:
			ids := make([]string, len(gefunden))
			for i, l := range gefunden {
				ids[i] = zeichenketteVon(l["id"])
			}
			return nil, &loeschFehler{status: http.StatusBadRequest, meldung: "Mehrere Swimlanes heißen " + strconv.Quote(ref) + " — bitte die id angeben: " + strings.Join(ids, ", ")}
		}
		l := gefunden[0]
		id := zeichenketteVon(l["id"])
		if id == "__backlog__" || zeichenketteVon(l["kind"]) == "service" {
			return nil, &loeschFehler{status: http.StatusForbidden, meldung: "Den allgemeinen Backlog und den Mail-Eingang löscht dieses Werkzeug nicht."}
		}
		normal, meta, kommentare, anhaenge := 0, 0, 0, 0
		var dateien [][2]string
		for _, k := range s.karten(l) {
			if k == nil {
				continue
			}
			if zeichenketteVon(k["kind"]) == "sprint-meta" {
				// Fold (swimlane.delete): Meta-Karten gehen mit der Swimlane.
				meta++
				dateien = append(dateien, [2]string{zeichenketteVon(k["id"]), ""})
				continue
			}
			normal++
			kommentare += zaehleListe(k["comments"])
			anhaenge += zaehleListe(k["attachments"])
		}
		lRoh, _ := json.Marshal(l)
		vorschau := map[string]any{"swimlaneId": id, "titel": swimlaneTitelRoh(l), "art": zeichenketteVon(l["kind"]),
			"archiviert": jsWahr(l["archived"]), "karten": normal, "sprintKarten": meta,
			"kommentare": kommentare, "anhaenge": anhaenge, "link": a.boardLink(boardID)}
		if normal > 0 && !hatBacklog(s) {
			// Kritiker #757 K-3: Ohne allgemeinen Backlog schiebt der Fold die
			// Karten nirgendwohin — sie wären weg, die Vorschau sagte anderes.
			return nil, &loeschFehler{status: http.StatusConflict, meldung: "Dieses Board hat keinen allgemeinen Backlog; die Karten der Swimlane gingen beim Löschen verloren. Erst die Karten verschieben oder im Board löschen."}
		}
		if normal > 0 {
			vorschau["folge"] = fmt.Sprintf("Die %d Karte(n) der Swimlane bleiben erhalten und wandern in den allgemeinen Backlog (mit Kommentaren und Anhängen).", normal)
		}
		if meta > 0 {
			vorschau["folgeSprintKarten"] = "Die Karten 📋 Sprint-Start und 🏁 Sprint-Abschluss werden mit gelöscht."
		}
		return &loeschZiel{art: "swimlane", objektID: id, stand: standKennung(json.RawMessage(lRoh)), vorschau: vorschau,
			ops:     []map[string]any{{"type": "swimlane.delete", "payload": map[string]any{"sprintId": id}}},
			dateien: dateien}, nil
	}}

func hatBacklog(s rohStand) bool {
	for _, l := range s.lanes {
		if l != nil && zeichenketteVon(l["id"]) == "__backlog__" {
			return true
		}
	}
	return false
}

// --- Termin ----------------------------------------------------------------

var loeschTermin = loeschArt{art: "termin", name: "termin", pfadParam: "termin",
	ermittle: func(a *BoardsAPI, boardID string, s rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler) {
		var termine []map[string]json.RawMessage
		_ = json.Unmarshal(s.board["termine"], &termine)
		ref = strings.TrimSpace(ref)
		for _, t := range termine {
			if t == nil || zeichenketteVon(t["id"]) != ref || ref == "" {
				continue
			}
			tRoh, _ := json.Marshal(t)
			v := map[string]any{"terminId": ref, "titel": zeichenketteVon(t["titel"]), "datum": zeichenketteVon(t["datum"]),
				"link": a.werkzeugTerminLink(boardID, ref)}
			for roh, name := range map[string]string{"bisDatum": "bisDatum", "von": "von", "bis": "bis", "ort": "ort"} {
				if w := zeichenketteVon(t[roh]); w != "" {
					v[name] = w
				}
			}
			return &loeschZiel{art: "termin", objektID: ref, stand: standKennung(json.RawMessage(tRoh)), vorschau: v,
				ops: []map[string]any{{"type": "termin.delete", "payload": map[string]any{"terminId": ref}}}}, nil
		}
		return nil, nichtGefunden("Termin " + strconv.Quote(ref) + " gibt es auf diesem Board nicht (mehr). Die terminId nennt termine_lesen.")
	}}

// --- Anhang ----------------------------------------------------------------

// Anhänge an Karten und Board-Anhänge, gefunden über die id (zufällig
// vergeben). Anhänge eines Kommentars und das Hintergrundbild nicht: Die
// gehören zum Kommentar (ADR-0136, Kommentare löscht ein Agent nie) bzw. zur
// Board-Einstellung (erst mit board_aendern lösen).
var loeschAnhang = loeschArt{art: "anhang", name: "anhang", pfadParam: "anhang",
	ermittle: func(a *BoardsAPI, boardID string, s rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler) {
		ref = strings.TrimSpace(ref)
		type fund struct {
			ort   string // Karten-id oder boardAttachmentCardID
			liste []map[string]json.RawMessage
			index int
			karte map[string]json.RawMessage
		}
		var funde []fund
		suche := func(ort string, roh json.RawMessage, karte map[string]json.RawMessage) {
			var liste []map[string]json.RawMessage
			_ = json.Unmarshal(roh, &liste)
			for i, m := range liste {
				if m != nil && ref != "" && zeichenketteVon(m["id"]) == ref {
					funde = append(funde, fund{ort, liste, i, karte})
				}
			}
		}
		suche(boardAttachmentCardID, s.board["attachments"], nil)
		for _, l := range s.lanes {
			if l == nil || zeichenketteVon(l["kind"]) == "service" {
				continue
			}
			for _, k := range s.karten(l) {
				if k != nil {
					suche(zeichenketteVon(k["id"]), k["attachments"], k)
				}
			}
		}
		switch len(funde) {
		case 0:
			return nil, nichtGefunden("Anhang " + strconv.Quote(ref) + " gibt es auf diesem Board nicht (mehr). Die ids nennt anhang_lesen.")
		case 1:
		default:
			return nil, &loeschFehler{status: http.StatusConflict, meldung: "Die Anhang-id " + strconv.Quote(ref) + " kommt mehrfach vor — bitte im Board löschen."}
		}
		f := funde[0]
		meta := f.liste[f.index]
		if zeichenketteVon(meta["commentId"]) != "" {
			return nil, &loeschFehler{status: http.StatusForbidden, meldung: "Dieser Anhang gehört zu einem Kommentar; er fällt nur mit dem Kommentar weg (Kommentare löschen Agenten nicht)."}
		}
		if f.karte == nil {
			var hg map[string]json.RawMessage
			if json.Unmarshal(s.board["background"], &hg) == nil && zeichenketteVon(hg["attachmentId"]) == ref {
				return nil, &loeschFehler{status: http.StatusConflict, meldung: "Dieser Anhang ist das Hintergrundbild des Boards — erst mit board_aendern (hintergrund null) lösen."}
			}
		}
		rest := make([]map[string]json.RawMessage, 0, len(f.liste)-1)
		rest = append(rest, f.liste[:f.index]...)
		rest = append(rest, f.liste[f.index+1:]...)
		metaRoh, _ := json.Marshal(meta)
		v := map[string]any{"anhangId": ref, "dateiname": kuerze(zeichenketteVon(meta["filename"]), maxVorschlagTitel),
			"mime": zeichenketteVon(meta["mime"]), "groesse": wkZahl(meta["size"])}
		var op map[string]any
		if f.karte != nil {
			v["karte"] = map[string]any{"displayId": wkZahl(f.karte["displayId"]), "titel": zeichenketteVon(f.karte["title"])}
			v["link"] = a.kartenLink(boardID, wkZahl(f.karte["displayId"]))
			op = feldOp(f.ort, "attachments", rest)
		} else {
			v["karte"] = nil
			v["link"] = a.boardLink(boardID)
			op = map[string]any{"type": "board.field", "payload": map[string]any{"field": "attachments", "value": rest}}
		}
		ops := []map[string]any{op}
		if f.karte != nil && zeichenketteVon(f.karte["coverAttachmentId"]) == ref {
			// F-1883: Wie der Browser (src/main.js) fällt das Titelbild der
			// Karte mit dem Anhang weg — sonst zeigt coverAttachmentId ins Leere.
			ops = append(ops, feldOp(f.ort, "coverAttachmentId", nil))
			v["titelbild"] = true
		}
		return &loeschZiel{art: "anhang", objektID: ref, stand: standKennung(f.ort, json.RawMessage(metaRoh)), vorschau: v,
			ops: ops, dateien: [][2]string{{f.ort, ref}}}, nil
	}}

// --- Spalte ----------------------------------------------------------------

// Spalten gehören zur Board-Struktur: nur für eine owner-Person (E-3), nur
// leer, nie die letzte. Die Vorschau einer belegten Spalte nennt ihre Karten
// und gibt keinen Code.
var loeschSpalte = loeschArt{art: "spalte", name: "spalte", pfadParam: "spalte", nurOwner: true,
	ermittle: func(a *BoardsAPI, boardID string, s rohStand, ref string, exakt bool) (*loeschZiel, *loeschFehler) {
		var defs []map[string]json.RawMessage
		_ = json.Unmarshal(s.board["columnDefs"], &defs)
		ref = strings.TrimSpace(ref)
		index := -1
		for _, alsName := range []bool{false, true} {
			if alsName && exakt {
				break
			}
			treffer := 0
			for i, d := range defs {
				if d == nil {
					continue
				}
				if (!alsName && zeichenketteVon(d["id"]) == ref) || (alsName && strings.EqualFold(strings.TrimSpace(zeichenketteVon(d["title"])), ref)) {
					index = i
					treffer++
				}
			}
			if treffer > 1 {
				return nil, &loeschFehler{status: http.StatusBadRequest, meldung: "Mehrere Spalten heißen " + strconv.Quote(ref) + " — bitte die id aus board_lesen angeben."}
			}
			if index >= 0 {
				break
			}
		}
		if index < 0 {
			return nil, nichtGefunden("Spalte " + strconv.Quote(ref) + " gibt es auf diesem Board nicht (mehr). Spalten nennt board_lesen.")
		}
		d := defs[index]
		id := zeichenketteVon(d["id"])
		// Alle Karten zählen — auch archivierte, auch der Mail-Eingang: Eine
		// Karte ohne Spalte zeigt das Board nicht mehr.
		var belegt []map[string]any
		var ids []string
		for _, l := range s.lanes {
			for _, k := range s.karten(l) {
				if k != nil && zeichenketteVon(k["columnId"]) == id {
					ids = append(ids, zeichenketteVon(k["id"]))
					if len(belegt) < 20 {
						belegt = append(belegt, map[string]any{"displayId": wkZahl(k["displayId"]), "titel": zeichenketteVon(k["title"]),
							"swimlane": swimlaneTitelRoh(l), "archiviert": jsWahr(k["archived"])})
					}
				}
			}
		}
		titel := zeichenketteVon(d["title"])
		if len(ids) > 0 {
			return nil, &loeschFehler{status: http.StatusConflict,
				meldung: fmt.Sprintf("Spalte %q enthält noch %d Karte(n) (auch archivierte zählen); gelöscht wird nur eine leere Spalte. Erst die Karten verschieben oder löschen.", titel, len(ids)),
				mehr:    map[string]any{"karten": belegt}}
		}
		if len(defs) <= 1 {
			return nil, &loeschFehler{status: http.StatusConflict, meldung: "Die letzte Spalte eines Boards lässt sich nicht löschen."}
		}
		rest := make([]map[string]json.RawMessage, 0, len(defs)-1)
		rest = append(rest, defs[:index]...)
		rest = append(rest, defs[index+1:]...)
		dRoh, _ := json.Marshal(d)
		return &loeschZiel{art: "spalte", objektID: id, stand: standKennung(json.RawMessage(dRoh), ids),
			vorschau: map[string]any{"spalteId": id, "titel": titel, "karten": 0, "link": a.boardLink(boardID)},
			ops:      []map[string]any{{"type": "board.field", "payload": map[string]any{"field": "columnDefs", "value": rest}}}}, nil
	}}

// loeschArten: alle Paare, in Katalog-Reihenfolge.
var loeschArten = []*loeschArt{&loeschKarte, &loeschSwimlane, &loeschTermin, &loeschAnhang, &loeschSpalte}

// --- Bestätigungscode ------------------------------------------------------

// loeschBindung: woran ein Code hängt (ADR-0139 E-2). Projekt-Tokens haben
// keine Person (leer), beim delegierenden Token ist es die Person aus dem JWT.
func loeschBindung(p *AgentPrincipal, boardID, art, objektID string, ablauf int64, zufall []byte) string {
	return strings.Join([]string{"aboard-loeschcode-v1", p.TokenID, storage.NormalizeEmail(p.OnBehalfOf), boardID, art, objektID,
		strconv.FormatInt(ablauf, 10), hex.EncodeToString(zufall)}, "\x00")
}

func loeschMac(geheimnis []byte, teil, bindung, stand string) []byte {
	m := hmac.New(sha256.New, geheimnis)
	m.Write([]byte(teil + "\x00" + bindung))
	if stand != "" {
		m.Write([]byte("\x00" + stand))
	}
	return m.Sum(nil)[:loeschCodeMacLaenge]
}

// bildeLoeschCode: zufall(16) | ablauf(8) | mac Bindung | mac Bindung+Stand.
// Zwei Teile, damit die Antwort unterscheiden kann: „gehört nicht hierher“
// gegen „das Objekt hat sich seit der Prüfung geändert“.
func bildeLoeschCode(geheimnis []byte, p *AgentPrincipal, boardID string, z *loeschZiel, jetzt time.Time) (string, time.Time, error) {
	zufall := make([]byte, 16)
	if _, err := rand.Read(zufall); err != nil {
		return "", time.Time{}, err
	}
	ablauf := jetzt.Add(loeschCodeGueltig).Truncate(time.Second)
	bindung := loeschBindung(p, boardID, z.art, z.objektID, ablauf.Unix(), zufall)
	var roh bytes.Buffer
	roh.Write(zufall)
	_ = binary.Write(&roh, binary.BigEndian, ablauf.Unix())
	roh.Write(loeschMac(geheimnis, "bindung", bindung, ""))
	roh.Write(loeschMac(geheimnis, "stand", bindung, z.stand))
	return loeschCodePraefix + base64.RawURLEncoding.EncodeToString(roh.Bytes()), ablauf, nil
}

// geprüfter Code: Zufallswert (für die Verbrauchsliste) und Ablauf.
type loeschCodeTeile struct {
	zufall []byte
	ablauf time.Time
}

// pruefeLoeschCode: nil = gültig für genau dieses Ziel im heutigen Stand.
func pruefeLoeschCode(geheimnis []byte, code string, p *AgentPrincipal, boardID string, z *loeschZiel, jetzt time.Time) (*loeschCodeTeile, *loeschFehler) {
	neuHolen := " Mit " + z.art + "_loeschen_pruefen einen neuen Code holen und die Person erneut fragen."
	ungueltig := &loeschFehler{status: http.StatusForbidden, meldung: "nicht gelöscht: Der Bestätigungscode passt nicht zu diesem Objekt, Board oder Zugang." + neuHolen}
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, loeschCodePraefix) {
		return nil, &loeschFehler{status: http.StatusBadRequest, meldung: "nicht gelöscht: code fehlt oder ist kein Bestätigungscode (beginnt mit „" + loeschCodePraefix + "“)." + neuHolen}
	}
	roh, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, loeschCodePraefix))
	if err != nil || len(roh) != 16+8+2*loeschCodeMacLaenge {
		return nil, ungueltig
	}
	zufall := roh[:16]
	ablaufUnix := int64(binary.BigEndian.Uint64(roh[16:24]))
	macBindung, macStand := roh[24:24+loeschCodeMacLaenge], roh[24+loeschCodeMacLaenge:]
	bindung := loeschBindung(p, boardID, z.art, z.objektID, ablaufUnix, zufall)
	if subtle.ConstantTimeCompare(macBindung, loeschMac(geheimnis, "bindung", bindung, "")) != 1 {
		return nil, ungueltig
	}
	ablauf := time.Unix(ablaufUnix, 0)
	if !jetzt.Before(ablauf) {
		return nil, &loeschFehler{status: http.StatusGone, meldung: "nicht gelöscht: Der Bestätigungscode ist abgelaufen (gültig 10 Minuten)." + neuHolen}
	}
	if subtle.ConstantTimeCompare(macStand, loeschMac(geheimnis, "stand", bindung, z.stand)) != 1 {
		return nil, &loeschFehler{status: http.StatusConflict, meldung: "nicht gelöscht: Das Objekt wurde seit der Prüfung geändert — bestätigt war ein anderer Stand." + neuHolen}
	}
	return &loeschCodeTeile{zufall: zufall, ablauf: ablauf}, nil
}

// --- Handler ---------------------------------------------------------------

func (a *BoardsAPI) loeschCodeRepo() *storage.LoeschCodeRepo {
	a.werkzeugZustand()
	return a.loeschCodes
}

// loeschVorbereiten: Principal, Owner-Prüfung (Spalte), Stand, Ziel. nil:
// Antwort ist geschrieben.
func (a *BoardsAPI) loeschVorbereiten(w http.ResponseWriter, r *http.Request, art *loeschArt) (*AgentPrincipal, string, *loeschZiel) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return nil, "", nil
	}
	boardID := r.PathValue("boardId")
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	if art.pfadParam == "karte" && schattenNurLesbar(w, a.paarSeite(ctx) == BoardpaarSeiteAussen, r.PathValue(art.pfadParam)) {
		// Karte #789 (ADR-0140 §7): Schatten löscht kein Werkzeug.
		return nil, "", nil
	}
	if art.nurOwner && !a.personIstOwner(ctx, p, boardID) {
		writeError(w, http.StatusForbidden, "nicht erlaubt: Die Board-Struktur (Spalten) ändert nur ein persönlicher Zugang einer Person, die owner dieses Boards ist.")
		return nil, "", nil
	}
	b, err := a.repo.Get(ctx, boardID)
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusForbidden, werkzeugVerbotenMeldung)
		return nil, "", nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return nil, "", nil
	}
	s, ok := leseRohStand(b.StateJSON)
	if !ok {
		writeError(w, http.StatusInternalServerError, "board read failed")
		return nil, "", nil
	}
	z, f := art.ermittle(a, boardID, s, r.PathValue(art.pfadParam), false)
	if f != nil {
		f.schreibe(w)
		return nil, "", nil
	}
	return p, boardID, z
}

// werkzeugLoeschenPruefen — GET …/loeschen: Vorschau und Code, löscht nichts.
func (a *BoardsAPI) werkzeugLoeschenPruefen(art *loeschArt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p := werkzeugPrincipal(w, r); p == nil || !a.werkzeugLesenErlaubt(w, p) {
			return
		}
		p, boardID, z := a.loeschVorbereiten(w, r, art)
		if z == nil {
			return
		}
		ctx, cancel := contextMitFrist(r)
		defer cancel()
		geheimnis, err := a.loeschCodeRepo().Geheimnis(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "confirmation code unavailable", err)
			return
		}
		code, ablauf, err := bildeLoeschCode(geheimnis, p, boardID, z, loeschCodeUhr())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "confirmation code unavailable", err)
			return
		}
		antwort := map[string]any{
			"ergebnis":   "nichts gelöscht — Vorschau",
			"boardId":    boardID,
			"vorschau":   z.vorschau,
			"code":       code,
			"gueltigBis": ablauf.UTC().Format(time.RFC3339),
			"naechsterSchritt": "Zeige der Person, was gelöscht würde, und frage ausdrücklich nach. Nur nach einem klaren Ja " +
				art.name + "_loeschen mit diesem code aufrufen. Ändert sich das Objekt vorher, ist der Code ungültig.",
		}
		schreibeMitHinweis(w, antwort)
	}
}

// werkzeugLoeschen — POST …/loeschen {code}: löscht, wenn der Code für genau
// dieses Objekt im heutigen Stand ausgestellt wurde.
func (a *BoardsAPI) werkzeugLoeschen(art *loeschArt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if werkzeugPrincipal(w, r) == nil {
			return
		}
		m, ok := leseWerkzeugBody(w, r, "code")
		if !ok {
			return
		}
		code, grund := textFeld(m, "code", 200, true, false)
		if grund != "" {
			writeError(w, http.StatusBadRequest, "nicht gelöscht: code fehlt. Erst "+art.name+"_loeschen_pruefen aufrufen, der Person die Vorschau zeigen und nur nach ihrem ausdrücklichen Ja mit dem code löschen.")
			return
		}
		p, boardID, z := a.loeschVorbereiten(w, r, art)
		if z == nil {
			return
		}
		ctx, cancel := contextMitFrist(r)
		defer cancel()
		codes := a.loeschCodeRepo()
		geheimnis, err := codes.Geheimnis(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "confirmation code unavailable", err)
			return
		}
		teile, f := pruefeLoeschCode(geheimnis, code, p, boardID, z, loeschCodeUhr())
		if f != nil {
			f.schreibe(w)
			return
		}
		zufall := hex.EncodeToString(teile.zufall)
		if err := codes.Verbrauche(ctx, zufall, teile.ablauf); errors.Is(err, storage.ErrLoeschCodeVerbraucht) {
			writeError(w, http.StatusConflict, "nicht gelöscht: Dieser Bestätigungscode wurde schon benutzt. War die Löschung erfolgreich, ist das Objekt weg (board_lesen); sonst mit "+art.name+"_loeschen_pruefen neu prüfen.")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "confirmation code check failed", err)
			return
		}
		// Unter der Sperre: Objekt noch da und unverändert — und die Ops, die
		// geschrieben werden, noch dieselben (Kritiker #757 K-1): Spalte und
		// Anhang schreiben eine ganze Liste; hat ein Mensch einen ANDEREN
		// Eintrag darin geändert, wäre seine Änderung sonst weg.
		erwartet := z.stand
		erwarteteOps := opsKennung(z.ops)
		vorbedingung := func(state []byte) error {
			s, ok := leseRohStand(state)
			if !ok {
				return &opsVorbedingungVerletzt{grund: "stand-unlesbar"}
			}
			jetzt, f := art.ermittle(a, boardID, s, z.objektID, true)
			if f != nil {
				return &opsVorbedingungVerletzt{grund: "weg"}
			}
			if jetzt.stand != erwartet || opsKennung(jetzt.ops) != erwarteteOps {
				return &opsVorbedingungVerletzt{grund: "geaendert"}
			}
			return nil
		}
		r2 := r.WithContext(mitOpsVorbedingung(r.Context(), vorbedingung))
		if werkzeugVorDemSchreiben != nil {
			werkzeugVorDemSchreiben()
		}
		puffer := &pufferAntwort{kopf: http.Header{}}
		if _, puf, ok := a.werkzeugOpsRoh(puffer, r2, boardID, z.ops); !ok {
			loeschNichtGeschrieben(r.Context(), w, codes, zufall, art, puffer, puf)
			return
		}
		a.loescheDateien(boardID, z.dateien)
		antwort := map[string]any{"ergebnis": "gelöscht", "boardId": boardID, "geloescht": z.vorschau}
		if art == &loeschSwimlane {
			if folge, ok := z.vorschau["folge"]; ok {
				antwort["hinweis"] = folge
			}
		}
		schreibeMitHinweis(w, antwort) // Fremdtext (Titel, Dateinamen), K-7
	}
}

// loeschNichtGeschrieben: Antwort, wenn postOps die Lösch-Ops nicht
// angenommen hat. Den Code gibt es nur nach einer Ablehnung vor dem COMMIT
// wieder frei (loeschCodeFreigebbar). Eigene Funktion, damit ein Test die
// Verdrahtung mit dem echten Code-Speicher prüft (Abschluss-QA #757, Q-1).
func loeschNichtGeschrieben(ctx context.Context, w http.ResponseWriter, codes *storage.LoeschCodeRepo, zufall string, art *loeschArt, puffer, puf *pufferAntwort) {
	if loeschCodeFreigebbar(puf) {
		// postOps hat abgelehnt, nichts geschrieben: Der Code gilt weiter
		// (bis zu seinem Ablauf und nur für denselben Stand).
		if err := codes.Freigeben(context.WithoutCancel(ctx), zufall); err != nil {
			log.Printf("aboard-server: Bestätigungscode nach Ablehnung nicht freigegeben: %v", err)
		}
	}
	if puf == nil {
		// werkzeugOpsRoh hat die Antwort in puffer geschrieben.
		kopiereAntwort(w, puffer)
		return
	}
	if puf.status == http.StatusConflict && bytes.Contains(puf.body.Bytes(), []byte(`"`+opsVorbedingungArt+`"`)) {
		writeError(w, http.StatusConflict, "nicht gelöscht: Das Objekt wurde gerade eben geändert oder entfernt. Mit "+art.name+"_loeschen_pruefen neu prüfen und die Person erneut fragen.")
		return
	}
	werkzeugOpsFehler(w, puf)
}

// loeschCodeFreigebbar: Nur wenn sicher nichts geschrieben wurde, gilt der
// Code wieder — postOps hat mit einer Ablehnung VOR dem COMMIT geantwortet
// (400, 404, 409, 413, 422, 429). Ohne Antwort (unlesbare Quittung nach dem
// COMMIT, Kritiker K-7) und bei 5xx (Commit-Fehler, Frist — offen, ob
// geschrieben wurde, Verify V-3) bleibt er verbraucht.
func loeschCodeFreigebbar(puf *pufferAntwort) bool {
	if puf == nil {
		return false
	}
	switch puf.status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	}
	return false
}

// opsKennung: Kennung der Ops, wie sie geschrieben würden.
func opsKennung(ops []map[string]any) string {
	roh, _ := json.Marshal(ops)
	return standKennung(json.RawMessage(roh))
}

// kopiereAntwort: eine gepufferte Antwort an den Client geben.
func kopiereAntwort(w http.ResponseWriter, p *pufferAntwort) {
	for k, v := range p.kopf {
		w.Header()[k] = v
	}
	status := p.status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	_, _ = w.Write(p.body.Bytes())
}

// loescheDateien: Anhang-Dateien nach dem COMMIT entfernen. Best-effort wie
// loescheKommentarAnhaenge — die Löschung im Board gilt; eine liegen
// gebliebene Datei gehört ins Log.
func (a *BoardsAPI) loescheDateien(boardID string, dateien [][2]string) {
	if a.attach == nil {
		return
	}
	for _, d := range dateien {
		ort, anhang := d[0], d[1]
		var err error
		switch {
		case !validAttachmentID(ort) && ort != boardAttachmentCardID:
			continue
		case anhang == "":
			err = a.attach.DeleteCard(boardID, ort)
		case validAttachmentID(anhang):
			err = a.attach.Delete(boardID, ort, anhang)
		default:
			continue
		}
		if err != nil {
			log.Printf("aboard-server: Anhang-Dateien %s/%s (Board %s) nach dem Löschen nicht entfernt: %v", ort, anhang, boardID, err)
		}
	}
}
