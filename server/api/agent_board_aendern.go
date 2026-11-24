package api

// agent_board_aendern.go — Werkzeug board_aendern (ADR-0139 E-3, Karte #757):
// Titel, Spalten anlegen/umbenennen/ordnen, Hintergrund, Vorlagen. Nur für
// den persönlichen Zugang einer Person, die owner des Boards ist (eigene
// ACL-Abfrage — die Agenten-Rolle ist höchstens editor), nie über ein
// delegierendes Token. Spalten löschen geht nur über spalte_loeschen
// (zweistufig, nur leer).
//
// Ohne Felder ändert das Werkzeug nichts und nennt die aktuellen
// Einstellungen — so kommt das Modell an die Vorlagen-ids und die
// Board-Anhänge für den Hintergrund, ohne ein eigenes Lese-Werkzeug.
//
// Geschrieben wird je Feld board.field (ganzer Wert). Damit dabei nichts
// verloren geht, was ein Mensch zwischen Lesen und Schreiben geändert hat,
// prüft postOps unter der Board-Sperre, dass jedes geschriebene Feld noch so
// ist wie gelesen (opsVorbedingung).

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	boardAendernMaxTitel   = 200
	boardAendernMaxSpalten = 30
	boardAendernMaxVorlage = 50
)

// boardAendernFelder: was board_aendern annimmt.
var boardAendernFelder = []string{"titel", "spalten", "hintergrund", "vorlagen"}

// boardEinstellungen: die aktuelle Fassung für die Antwort.
func (a *BoardsAPI) boardEinstellungen(boardID string, s rohStand) map[string]any {
	var defs []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["columnDefs"], &defs)
	spalten := []map[string]any{}
	for _, d := range defs {
		if d != nil {
			spalten = append(spalten, map[string]any{"id": zeichenketteVon(d["id"]), "titel": zeichenketteVon(d["title"])})
		}
	}
	var hg map[string]json.RawMessage
	var hintergrund any
	if json.Unmarshal(s.board["background"], &hg) == nil && hg != nil && zeichenketteVon(hg["attachmentId"]) != "" {
		darstellung := zeichenketteVon(hg["darstellung"])
		if darstellung == "" {
			darstellung = "getoent"
		}
		hintergrund = map[string]any{"anhang": zeichenketteVon(hg["attachmentId"]), "darstellung": darstellung}
	}
	var anhaenge []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["attachments"], &anhaenge)
	boardAnhaenge := []map[string]any{}
	for _, m := range anhaenge {
		if m != nil && zeichenketteVon(m["id"]) != "" {
			boardAnhaenge = append(boardAnhaenge, map[string]any{"id": zeichenketteVon(m["id"]),
				"dateiname": kuerze(zeichenketteVon(m["filename"]), maxVorschlagTitel), "mime": zeichenketteVon(m["mime"])})
		}
	}
	var vorlagenRoh []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["templates"], &vorlagenRoh)
	vorlagen := []map[string]any{}
	for _, v := range vorlagenRoh {
		if v == nil {
			continue
		}
		var aufwand any
		if e := strings.TrimSpace(string(v["effort"])); e != "" && e != "null" {
			aufwand = json.RawMessage(v["effort"])
		}
		vorlagen = append(vorlagen, map[string]any{"id": zeichenketteVon(v["id"]), "titel": zeichenketteVon(v["title"]),
			"beschreibung": zeichenketteVon(v["description"]), "labels": zeichenkettenListe(v["labels"]),
			"owner": zeichenketteVon(v["owner"]), "komplexitaet": zeichenketteVon(v["complexity"]), "aufwand": aufwand})
	}
	return map[string]any{"titel": zeichenketteVon(s.board["title"]), "spalten": spalten, "hintergrund": hintergrund,
		"boardAnhaenge": boardAnhaenge, "vorlagen": vorlagen, "link": a.boardLink(boardID)}
}

// werkzeugBoardAendern — POST /werkzeuge/boards/{boardId}/aendern.
func (a *BoardsAPI) werkzeugBoardAendern(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, boardAendernFelder...)
	if !ok {
		return
	}
	boardID := r.PathValue("boardId")
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	if !a.personIstOwner(ctx, p, boardID) {
		writeError(w, http.StatusForbidden, "nicht erlaubt: Board-Einstellungen ändert nur ein persönlicher Zugang einer Person, die owner dieses Boards ist.")
		return
	}
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		writeError(w, http.StatusForbidden, werkzeugVerbotenMeldung)
		return
	}
	s, ok := leseRohStand(b.StateJSON)
	if !ok {
		writeError(w, http.StatusInternalServerError, "board read failed")
		return
	}

	var ops []map[string]any
	var geaendert []string
	gelesen := map[string]string{} // Feld → Stand-Kennung beim Lesen
	setze := func(feld, name string, wert any) {
		gelesen[feld] = standKennung(json.RawMessage(rohOderNull(s.board[feld])))
		ops = append(ops, map[string]any{"type": "board.field", "payload": map[string]any{"field": feld, "value": wert}})
		geaendert = append(geaendert, name)
	}

	if feldDa(m, "titel") {
		// Unveränderter Bestandstitel ist von der Inhaltszeichen-Pflicht ausgenommen (F-2390, wie bei Vorlagen
		// und Swimlanes): ein Altbestand ohne Inhaltszeichen darf keine Änderung an anderer Stelle blockieren.
		if !titelUnveraendert(m, "titel", zeichenketteVon(s.board["title"])) {
			titel, grund := textFeldInhalt(m, "titel", boardAendernMaxTitel, true, false)
			if grund != "" {
				writeError(w, http.StatusBadRequest, grund)
				return
			}
			if titel != zeichenketteVon(s.board["title"]) {
				setze("title", "titel", titel)
			}
		}
	}
	if feldDa(m, "spalten") {
		neu, grund := neueSpalten(m["spalten"], s)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		if neu != nil {
			setze("columnDefs", "spalten", neu)
		}
	}
	if feldDa(m, "hintergrund") {
		neu, grund := neuerHintergrund(m["hintergrund"], s)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		if standKennung(neu) != standKennung(kanonischerWert(rohOderNull(s.board["background"]))) {
			setze("background", "hintergrund", neu)
			// Der Anhang muss beim Schreiben noch da sein (K-7).
			gelesen["attachments"] = standKennung(json.RawMessage(rohOderNull(s.board["attachments"])))
		}
	}
	if feldDa(m, "vorlagen") {
		neu, grund := neueVorlagen(m["vorlagen"], s)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		if neu != nil {
			setze("templates", "vorlagen", neu)
		}
	}

	if len(ops) == 0 {
		antwort := map[string]any{"ergebnis": "nichts geändert", "boardId": boardID, "einstellungen": a.boardEinstellungen(boardID, s)}
		schreibeMitHinweis(w, antwort)
		return
	}
	vorbedingung := func(state []byte) error {
		jetzt, ok := leseRohStand(state)
		if !ok {
			return &opsVorbedingungVerletzt{grund: "stand-unlesbar"}
		}
		for feld, kennung := range gelesen {
			if standKennung(json.RawMessage(rohOderNull(jetzt.board[feld]))) != kennung {
				return &opsVorbedingungVerletzt{grund: "geaendert:" + feld}
			}
		}
		return nil
	}
	r2 := r.WithContext(mitOpsVorbedingung(r.Context(), vorbedingung))
	if werkzeugVorDemSchreiben != nil {
		werkzeugVorDemSchreiben()
	}
	puffer := &pufferAntwort{kopf: http.Header{}}
	if _, puf, ok := a.werkzeugOpsRoh(puffer, r2, boardID, ops); !ok {
		if puf == nil {
			kopiereAntwort(w, puffer)
			return
		}
		if puf.status == http.StatusConflict && strings.Contains(puf.body.String(), `"`+opsVorbedingungArt+`"`) {
			writeError(w, http.StatusConflict, "nicht ausgeführt: Jemand hat die Board-Einstellungen gerade geändert. board_aendern ohne Felder aufrufen, neu lesen und dann wiederholen. Nichts geschrieben.")
			return
		}
		werkzeugOpsFehler(w, puf)
		return
	}
	antwort := map[string]any{"ergebnis": "geändert", "boardId": boardID, "geaendert": geaendert}
	if nachher, err := a.repo.Get(ctx, boardID); err == nil {
		if sn, ok := leseRohStand(nachher.StateJSON); ok {
			antwort["einstellungen"] = a.boardEinstellungen(boardID, sn)
		}
	}
	schreibeMitHinweis(w, antwort)
}

// neueSpalten: die ganze neue Spaltenliste [{id?, titel}]. Jede bestehende
// Spalte muss genau einmal vorkommen (gelöscht wird nur über
// spalte_loeschen); ohne id entsteht eine neue. Weitere Felder einer
// bestehenden Spalte (etwa color) bleiben. nil ohne Grund: unverändert.
//
// Zwei Spalten mit demselben Titel (ohne Groß-/Kleinschreibung) lehnt es ab
// — aber nur, wenn das Werkzeug die Dopplung herstellen würde. Hat ein
// Mensch sie schon angelegt („Todo“/„todo“) und bleiben beide Titel, wie sie
// sind, geht die Liste durch; sonst wäre jede vollständige Liste abgelehnt
// und das Werkzeug für dieses Board gesperrt (F-1885). Aus demselben Grund
// gelten Länge und Steuerzeichen nur für neue oder umbenannte Titel: Ein
// unverändert zurückgegebener Bestandstitel bleibt wörtlich stehen, auch
// wenn ein Mensch ihn länger oder mit Tab angelegt hat (Kritiker
// Sprint-87-Abschluss K-1).
func neueSpalten(roh json.RawMessage, s rohStand) ([]map[string]any, string) {
	var eingabe []map[string]json.RawMessage
	if json.Unmarshal(roh, &eingabe) != nil {
		return nil, "spalten muss eine Liste von Objekten {id?, titel} sein"
	}
	if len(eingabe) == 0 || len(eingabe) > boardAendernMaxSpalten {
		return nil, "spalten: 1 bis " + strconv.Itoa(boardAendernMaxSpalten) + " Einträge"
	}
	var defs []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["columnDefs"], &defs)
	bestand := map[string]map[string]json.RawMessage{}
	for _, d := range defs {
		if d != nil {
			bestand[zeichenketteVon(d["id"])] = d
		}
	}
	gesehen := map[string]bool{}
	// Titel → ob einer der Einträge damit neu oder umbenannt ist.
	titelGesehen := map[string]bool{}
	titelDurchWerkzeug := map[string]bool{}
	titelAnzeige := map[string]string{}
	neu := make([]map[string]any, 0, len(eingabe))
	for i, e := range eingabe {
		if e == nil {
			return nil, "spalten[" + strconv.Itoa(i) + "] ist kein Objekt"
		}
		for k := range e {
			if k != "id" && k != "titel" {
				return nil, "spalten[" + strconv.Itoa(i) + "]: unbekanntes Feld " + strconv.Quote(k) + " — erlaubt sind id und titel"
			}
		}
		id, grund := textFeld(e, "id", 100, false, false)
		if grund != "" {
			return nil, "spalten[" + strconv.Itoa(i) + "]: " + grund
		}
		alterTitel, bestehend := "", false
		if d, da := bestand[id]; id != "" && da {
			// Wie die Eingabe getrimmt (textFeld), sonst gälte „Todo “ aus
			// dem Bestand als umbenannt (Kritiker Sprint 87 K-4).
			alterTitel, bestehend = strings.TrimSpace(zeichenketteVon(d["title"])), true
		}
		titel, unveraendert := "", false
		if bestehend {
			var roh string
			if r := e["titel"]; r != nil && strings.TrimSpace(string(r)) != "null" && json.Unmarshal(r, &roh) == nil &&
				strings.TrimSpace(roh) == alterTitel {
				titel, unveraendert = alterTitel, true
			}
		}
		if !unveraendert {
			titel, grund = textFeldInhalt(e, "titel", 100, true, false)
			if grund != "" {
				return nil, "spalten[" + strconv.Itoa(i) + "]: " + grund
			}
		}
		schluessel := strings.ToLower(titel)
		vomWerkzeug := !unveraendert
		if titelGesehen[schluessel] && (vomWerkzeug || titelDurchWerkzeug[schluessel]) {
			return nil, "spalten: der Titel " + strconv.Quote(titel) + " kommt zweimal vor (auch " + strconv.Quote(titelAnzeige[schluessel]) +
				", Groß-/Kleinschreibung zählt nicht) — einer der beiden Spalten einen anderen Titel geben"
		}
		titelGesehen[schluessel] = true
		titelDurchWerkzeug[schluessel] = titelDurchWerkzeug[schluessel] || vomWerkzeug
		titelAnzeige[schluessel] = titel
		if id == "" {
			eintrag := map[string]any{"id": "col-" + uuid.NewString()[:8], "title": titel, "color": nil}
			neu = append(neu, eintrag)
			continue
		}
		alt, da := bestand[id]
		if !da {
			return nil, "spalten[" + strconv.Itoa(i) + "]: Spalte " + strconv.Quote(id) + " gibt es nicht — neue Spalten ohne id angeben"
		}
		if gesehen[id] {
			return nil, "spalten: " + strconv.Quote(id) + " kommt zweimal vor"
		}
		gesehen[id] = true
		eintrag := map[string]any{}
		for k, v := range alt {
			eintrag[k] = v
		}
		if !unveraendert {
			eintrag["title"] = titel
		}
		neu = append(neu, eintrag)
	}
	var fehlen []string
	for _, d := range defs {
		if id := zeichenketteVon(d["id"]); d != nil && !gesehen[id] {
			fehlen = append(fehlen, id)
		}
	}
	if len(fehlen) > 0 {
		return nil, "spalten: es fehlen " + strings.Join(fehlen, ", ") + " — Spalten löschen geht nur über spalte_loeschen_pruefen und spalte_loeschen (nur leere Spalten)"
	}
	alt, _ := json.Marshal(defs)
	neuRoh, _ := json.Marshal(neu)
	if standKennung(json.RawMessage(alt)) == standKennung(json.RawMessage(neuRoh)) {
		return nil, ""
	}
	return neu, ""
}

// neuerHintergrund: null (keiner) oder {anhang, darstellung?}. Der Anhang
// muss ein Bild unter den Board-Anhängen sein; hochladen geht über die
// Werkzeuge nicht (ADR-0139 §1).
func neuerHintergrund(roh json.RawMessage, s rohStand) (any, string) {
	if strings.TrimSpace(string(roh)) == "null" {
		return nil, ""
	}
	var e map[string]json.RawMessage
	if json.Unmarshal(roh, &e) != nil || e == nil {
		return nil, "hintergrund muss null oder ein Objekt {anhang, darstellung} sein"
	}
	for k := range e {
		if k != "anhang" && k != "darstellung" {
			return nil, "hintergrund: unbekanntes Feld " + strconv.Quote(k) + " — erlaubt sind anhang und darstellung"
		}
	}
	anhang, grund := textFeld(e, "anhang", 100, true, false)
	if grund != "" {
		return nil, "hintergrund: " + grund
	}
	darstellung, grund := textFeld(e, "darstellung", 20, false, false)
	if grund != "" {
		return nil, "hintergrund: " + grund
	}
	if darstellung != "" && darstellung != "voll" && darstellung != "abgedunkelt" && darstellung != "getoent" {
		return nil, "hintergrund.darstellung: voll, abgedunkelt oder getoent"
	}
	var anhaenge []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["attachments"], &anhaenge)
	for _, m := range anhaenge {
		if m == nil || zeichenketteVon(m["id"]) != anhang {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(zeichenketteVon(m["mime"])), "image/") {
			return nil, "hintergrund.anhang: " + strconv.Quote(anhang) + " ist kein Bild"
		}
		neu := map[string]any{"attachmentId": anhang}
		if darstellung == "" {
			// Wie der Browser beim Bildwechsel (configdialog.js, #727): die
			// bisherige Darstellung bleibt (Kritiker #757 K-4).
			var alt map[string]json.RawMessage
			if json.Unmarshal(s.board["background"], &alt) == nil {
				darstellung = zeichenketteVon(alt["darstellung"])
			}
		}
		if darstellung != "" {
			neu["darstellung"] = darstellung
		}
		return neu, ""
	}
	return nil, "hintergrund.anhang: " + strconv.Quote(anhang) + " ist kein Board-Anhang (die Liste nennt board_aendern ohne Felder unter einstellungen.boardAnhaenge)"
}

// neueVorlagen: die ganze neue Vorlagenliste in Reihenfolge. Eine Vorlage mit
// id ändert die bestehende — nur die genannten Felder, die übrigen bleiben
// (Kritiker #757 K-2: „nicht gesendet“ ist nicht „leer“); ohne id entsteht
// eine neue. Jede bestehende muss vorkommen: Vorlagen löscht das Werkzeug
// nicht (wie Spalten; E-2 verlangt fürs Löschen eine Bestätigung).
func neueVorlagen(roh json.RawMessage, s rohStand) ([]map[string]any, string) {
	var eingabe []map[string]json.RawMessage
	if json.Unmarshal(roh, &eingabe) != nil {
		return nil, "vorlagen muss eine Liste von Objekten sein"
	}
	if len(eingabe) > boardAendernMaxVorlage {
		return nil, "vorlagen: höchstens " + strconv.Itoa(boardAendernMaxVorlage) + " Einträge"
	}
	var alt []map[string]json.RawMessage
	_ = json.Unmarshal(s.board["templates"], &alt)
	bestand := map[string]map[string]json.RawMessage{}
	for _, v := range alt {
		if v != nil {
			bestand[zeichenketteVon(v["id"])] = v
		}
	}
	erlaubt := map[string]bool{"id": true, "titel": true, "beschreibung": true, "labels": true, "owner": true, "komplexitaet": true, "aufwand": true}
	gesehen := map[string]bool{}
	neu := make([]map[string]any, 0, len(eingabe))
	for i, e := range eingabe {
		wo := "vorlagen[" + strconv.Itoa(i) + "]: "
		if e == nil {
			return nil, wo + "kein Objekt"
		}
		for k := range e {
			if !erlaubt[k] {
				return nil, wo + "unbekanntes Feld " + strconv.Quote(k)
			}
		}
		// Unveränderter Bestandstitel ist von der Inhaltszeichen-Pflicht ausgenommen (wie bei neueSpalten, V-7):
		// ein Altbestand ohne Inhaltszeichen darf keine Änderung an anderer Stelle blockieren.
		var titel, grund string
		if id0, _ := textFeld(e, "id", 100, false, false); id0 != "" && titelUnveraendert(e, "titel", zeichenketteVon(bestand[id0]["title"])) {
			titel = strings.TrimSpace(zeichenketteVon(bestand[id0]["title"]))
		} else {
			titel, grund = textFeldInhalt(e, "titel", 300, true, false)
		}
		if grund != "" {
			return nil, wo + grund
		}
		beschreibung, grund := textFeld(e, "beschreibung", 8000, false, true)
		if grund != "" {
			return nil, wo + grund
		}
		owner, grund := textFeld(e, "owner", 200, false, false)
		if grund != "" {
			return nil, wo + grund
		}
		komplexitaet, grund := textFeld(e, "komplexitaet", 20, false, false)
		if grund != "" || !inListe(komplexitaet, []string{"", "einfach", "mittel", "hoch"}) {
			return nil, wo + "komplexitaet: einfach, mittel, hoch oder leer"
		}
		labels, _, grund := leseTextListe(e, "labels", 100)
		if grund != "" {
			return nil, wo + grund
		}
		if labels == nil {
			labels = []string{}
		}
		var aufwand any
		if feldGesetzt(e, "aufwand") {
			var f float64
			if json.Unmarshal(e["aufwand"], &f) != nil || f < 0 || f > 10000 {
				return nil, wo + "aufwand: Zahl ≥ 0 (Stunden) oder null"
			}
			aufwand = f
		}
		id, grund := textFeld(e, "id", 100, false, false)
		if grund != "" {
			return nil, wo + grund
		}
		eintrag := map[string]any{}
		if id != "" {
			v, da := bestand[id]
			if !da {
				return nil, wo + "Vorlage " + strconv.Quote(id) + " gibt es nicht — neue Vorlagen ohne id angeben"
			}
			if gesehen[id] {
				return nil, "vorlagen: " + strconv.Quote(id) + " kommt zweimal vor"
			}
			gesehen[id] = true
			for k, w := range v {
				eintrag[k] = w
			}
		} else {
			eintrag["id"] = uuid.NewString()
			eintrag["isTemplate"] = true
		}
		neuAngelegt := id == ""
		eintrag["title"] = titel
		for _, f := range []struct {
			feld, name string
			wert       any
		}{{"description", "beschreibung", beschreibung}, {"labels", "labels", labels}, {"owner", "owner", owner},
			{"complexity", "komplexitaet", komplexitaet}, {"effort", "aufwand", aufwand}} {
			if neuAngelegt || feldDa(e, f.name) {
				eintrag[f.feld] = f.wert
			}
		}
		neu = append(neu, eintrag)
	}
	var fehlen []string
	for pos, v := range alt {
		if v == nil {
			continue
		}
		id := zeichenketteVon(v["id"])
		if id == "" {
			// Altbestand ohne id (Verify V-5): lässt sich nicht nennen, also
			// unverändert behalten statt das Werkzeug dauerhaft zu sperren —
			// an seiner bisherigen Stelle (Abschluss-QA Q-2), nicht am Ende.
			erhalten := map[string]any{}
			for k, w := range v {
				erhalten[k] = w
			}
			stelle := min(pos, len(neu))
			neu = append(neu[:stelle], append([]map[string]any{erhalten}, neu[stelle:]...)...)
			continue
		}
		if !gesehen[id] {
			fehlen = append(fehlen, id)
		}
	}
	if len(fehlen) > 0 {
		return nil, "vorlagen: es fehlen " + strings.Join(fehlen, ", ") + " — jede bestehende Vorlage muss vorkommen (mit id); Vorlagen löscht dieses Werkzeug nicht, nur das Board"
	}
	// Unverändert → nil, wie bei neueSpalten (F-1884): sonst schriebe ein
	// Aufruf mit der gelesenen Liste Op, Verlauf und SSE ohne Änderung und
	// liefe in einen Vorbedingungs-Konflikt, den es nicht gibt. Fehlende
	// Liste und leere Liste sind dasselbe.
	if len(neu) == 0 && len(alt) == 0 {
		return nil, ""
	}
	altRoh, _ := json.Marshal(alt)
	neuRoh, _ := json.Marshal(neu)
	if standKennung(json.RawMessage(altRoh)) == standKennung(json.RawMessage(neuRoh)) {
		return nil, ""
	}
	return neu, ""
}

// titelUnveraendert sagt, ob das mitgeschickte Textfeld (getrimmt) dem Bestandswert (getrimmt) entspricht.
func titelUnveraendert(m map[string]json.RawMessage, name, alt string) bool {
	r := m[name]
	if r == nil || strings.TrimSpace(alt) == "" {
		return false
	}
	var roh string
	return json.Unmarshal(r, &roh) == nil && strings.TrimSpace(roh) == strings.TrimSpace(alt)
}
