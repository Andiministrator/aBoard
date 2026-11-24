package api

// agent_werkzeuge_schreiben.go — Schreib-Werkzeuge aus ADR-0139 Stufe 4a
// (Karte #755): karte_aendern, checkliste_abhaken, archivieren,
// wiederherstellen, kommentar_bearbeiten, verweis_setzen, verweis_entfernen,
// uebernehmen, freigeben, termin_aendern, swimlane_anlegen, swimlane_aendern,
// auf_antwort_warten — dazu die Feldprüfungen, die karte_anlegen mit
// karte_aendern teilt.
//
// Wie bei allen Werkzeugen: Rechte entscheidet allein werkzeugDarf (keines
// davon ist für ein delegierendes Token freigegeben, ADR-0139 §7), geschrieben
// wird nur über postOps (werkzeugOps) — dieselbe Stempelung, Ratenbegrenzung,
// dasselbe Protokoll und dieselben Fold-Regeln wie auf dem rohen Weg.
//
// Freitext (Titel, Beschreibung, Agent-Kontext) ändert ein Werkzeug nur, wenn
// er seit dem Lesen gleich geblieben ist: Der Aufruf nennt den `stand` aus
// karte_zeigen, und die Op ist ein card.fieldIf mit dem gelesenen Rohwert als
// Erwartung. Ändert ein Mensch die Karte dazwischen, scheitert der Aufruf mit
// 409 bzw. 422 („seit dem Lesen geändert“) — nichts wird überschrieben.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/calendar"
)

const (
	// werkzeugMaxOwner: Zeichen für owner und je Eintrag in beteiligte.
	werkzeugMaxOwner = 200
	// werkzeugMaxLabel: Zeichen je Label.
	werkzeugMaxLabel = 100
	// werkzeugMaxListe: Einträge in labels bzw. beteiligte.
	werkzeugMaxListe = 50
	// werkzeugMaxAufwand: Stunden.
	werkzeugMaxAufwand = 10000
	// werkzeugMaxDauerMinuten: längste Übernahme (wie CLAIM_LEASE_MAX in src/ops.js).
	werkzeugMaxDauerMinuten = 24 * 60
	// werkzeugMaxFrage: Zeichen einer Frage in auf_antwort_warten (AWAITING_FRAGE_MAX in src/awaiting.js).
	werkzeugMaxFrage = 2000
)

// Werte mit fester Auswahl — dieselben Listen wie das Karten-Modal und die
// LLM-Mutationen (src/llm-mutations.js SET_FIELD_ENUMS, F-412).
var (
	prioritaetWerte   = []string{"", "niedrig", "mittel", "hoch"}
	komplexitaetWerte = []string{"", "einfach", "mittel", "hoch"}
	verweisTypen      = map[string]string{"blocks": "blocked-by", "blocked-by": "blocks", "depends-on": "required-for",
		"required-for": "depends-on", "related": "related"}
)

// --- Feldprüfungen ------------------------------------------------------------

// feldDa: steht das Feld im Body (auch als null)?
func feldDa(m map[string]json.RawMessage, name string) bool {
	_, da := m[name]
	return da
}

// feldGesetzt: steht das Feld mit einem Wert im Body — nicht fehlend, nicht
// null, kein leerer Text (Modelle schicken optionale Felder gern leer; Verify
// #755 Teil 2, V-1).
func feldGesetzt(m map[string]json.RawMessage, name string) bool {
	roh, da := m[name]
	if !da {
		return false
	}
	switch t := strings.TrimSpace(string(roh)); t {
	case "null", `""`:
		return false
	}
	var s string
	return json.Unmarshal(roh, &s) != nil || strings.TrimSpace(s) != ""
}

// leseTextListe liest eine Liste von Texten (labels, beteiligte): null = leer,
// Doppel fallen weg (Reihenfolge bleibt). da: das Feld steht im Body.
func leseTextListe(m map[string]json.RawMessage, name string, maxEintrag int) (liste []string, da bool, grund string) {
	roh, da := m[name]
	if !da {
		return nil, false, ""
	}
	if strings.TrimSpace(string(roh)) == "null" {
		return []string{}, true, ""
	}
	var eintraege []json.RawMessage
	if json.Unmarshal(roh, &eintraege) != nil {
		return nil, true, name + " muss eine Liste von Texten sein"
	}
	if len(eintraege) > werkzeugMaxListe {
		return nil, true, fmt.Sprintf("%s: höchstens %d Einträge", name, werkzeugMaxListe)
	}
	liste = []string{}
	gesehen := map[string]bool{}
	for _, e := range eintraege {
		var s string
		if json.Unmarshal(e, &s) != nil {
			return nil, true, name + " muss eine Liste von Texten sein"
		}
		s = strings.TrimSpace(s)
		if s == "" || utf8.RuneCountInString(s) > maxEintrag || hatSteuerzeichen(s, false) {
			return nil, true, fmt.Sprintf("%s: je Eintrag 1 bis %d Zeichen, keine Steuerzeichen", name, maxEintrag)
		}
		if !gesehen[s] {
			gesehen[s] = true
			liste = append(liste, s)
		}
	}
	return liste, true, ""
}

// leseDatumFeld: YYYY-MM-DD; leer oder null = Feld leeren.
func leseDatumFeld(m map[string]json.RawMessage, name string) (string, string) {
	s, grund := textFeld(m, name, 10, false, false)
	if grund == "" && s != "" && !istTerminDatum(s) {
		grund = name + " muss ein Datum YYYY-MM-DD sein (z. B. 2026-10-08), leer zum Entfernen"
	}
	return s, grund
}

// leseUhrzeitFeld: HH:MM (24 Stunden); leer oder null = Feld leeren.
func leseUhrzeitFeld(m map[string]json.RawMessage, name string) (string, string) {
	s, grund := textFeld(m, name, 5, false, false)
	if grund == "" && s != "" && !calendar.IstUhrzeit(s) {
		grund = name + " muss eine Uhrzeit HH:MM sein (24 Stunden, z. B. 14:30), leer zum Entfernen"
	}
	return s, grund
}

// leseWahrFeld: true oder false, nichts anderes.
func leseWahrFeld(m map[string]json.RawMessage, name string) (bool, string) {
	switch strings.TrimSpace(string(m[name])) {
	case "true":
		return true, ""
	case "false":
		return false, ""
	}
	return false, name + " muss true oder false sein"
}

// leseGanzzahl: eine ganze Zahl (auch als Text „3“), sonst Grund.
func leseGanzzahl(m map[string]json.RawMessage, name string, min, max int) (int, string) {
	roh := m[name]
	var f float64
	if json.Unmarshal(roh, &f) != nil {
		var s string
		if json.Unmarshal(roh, &s) != nil {
			return 0, fmt.Sprintf("%s muss eine ganze Zahl von %d bis %d sein", name, min, max)
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Sprintf("%s muss eine ganze Zahl von %d bis %d sein", name, min, max)
		}
		f = float64(n)
	}
	if f != math.Trunc(f) || f < float64(min) || f > float64(max) {
		return 0, fmt.Sprintf("%s muss eine ganze Zahl von %d bis %d sein", name, min, max)
	}
	return int(f), ""
}

func inListe(wert string, liste []string) bool {
	for _, l := range liste {
		if l == wert {
			return true
		}
	}
	return false
}

// leseKarteAnlegenExtra: die mit #755 hinzugekommenen Felder von
// karte_anlegen (owner, labels, faellig, faelligUhrzeit). extra trägt die
// Kartenfelder, summe die Teile für den Wiederholungsschutz (nur gesetzte).
func leseKarteAnlegenExtra(m map[string]json.RawMessage) (extra map[string]any, summe []string, grund string) {
	owner, grund := textFeld(m, "owner", werkzeugMaxOwner, false, false)
	if grund != "" {
		return nil, nil, grund
	}
	labels, _, grund := leseTextListe(m, "labels", werkzeugMaxLabel)
	if grund != "" {
		return nil, nil, grund
	}
	faellig, grund := leseDatumFeld(m, "faellig")
	if grund != "" {
		return nil, nil, grund
	}
	uhrzeit, grund := leseUhrzeitFeld(m, "faelligUhrzeit")
	if grund != "" {
		return nil, nil, grund
	}
	if uhrzeit != "" && faellig == "" {
		return nil, nil, "faelligUhrzeit nur zusammen mit faellig (Datum)"
	}
	extra = map[string]any{}
	if owner != "" {
		extra["owner"] = owner
		summe = append(summe, "owner="+owner)
	}
	if len(labels) > 0 {
		extra["labels"] = labels
		summe = append(summe, "labels="+strings.Join(labels, "\x00"))
	}
	if faellig != "" {
		extra["dueDate"] = faellig
		summe = append(summe, "faellig="+faellig)
	}
	if uhrzeit != "" {
		extra["dueTime"] = uhrzeit
		summe = append(summe, "uhrzeit="+uhrzeit)
	}
	if len(extra) == 0 {
		return nil, nil, ""
	}
	return extra, summe, ""
}

// --- gemeinsame Bausteine -----------------------------------------------------

// werkzeugKarteAusPfad: Board laden und die Karte des Pfads finden
// (findeWerkzeugKarte: Mail-Eingang nie, doppelte Nummer 400). nil: Antwort
// ist geschrieben.
func (a *BoardsAPI) werkzeugKarteAusPfad(w http.ResponseWriter, r *http.Request) (string, *wkBoard, *wkKarte, *wkSwimlane) {
	boardID := r.PathValue("boardId")
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return "", nil, nil, nil
	}
	k, lane := wb.findeWerkzeugKarte(w, r.PathValue("karte"))
	if k == nil {
		return "", nil, nil, nil
	}
	return boardID, wb, k, lane
}

// rohOderNull: der gelesene Rohwert eines Kartenfelds als Erwartung für
// card.fieldIf — ein fehlendes Feld gilt dort als null (src/ops.js).
func rohOderNull(roh json.RawMessage) json.RawMessage {
	if len(roh) == 0 {
		return json.RawMessage("null")
	}
	return roh
}

func feldOp(cardID, feld string, wert any) map[string]any {
	return map[string]any{"type": "card.field", "payload": map[string]any{"cardId": cardID, "field": feld, "value": wert}}
}

func feldWennOp(cardID, feld string, wert any, erwartet json.RawMessage) map[string]any {
	return map[string]any{"type": "card.fieldIf", "payload": map[string]any{"cardId": cardID, "field": feld, "value": wert, "expect": rohOderNull(erwartet)}}
}

// standGeaendert: 409 — der Freitext der Karte ist nicht mehr der gelesene.
func standGeaendert(w http.ResponseWriter, k *wkKarte) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": fmt.Sprintf("nicht ausgeführt: Karte #%d wurde seit dem Lesen geändert (stand passt nicht). Mit karte_zeigen neu lesen, "+
			"die Änderung am neuen Text prüfen und mit dem neuen stand wiederholen. Nichts geschrieben.", k.DisplayID)})
}

// pruefeStand: Freitext nur mit passendem stand. false: Antwort geschrieben.
func pruefeStand(w http.ResponseWriter, m map[string]json.RawMessage, k *wkKarte) bool {
	stand, grund := textFeld(m, "stand", 64, true, false)
	if grund != "" {
		writeError(w, http.StatusBadRequest, "stand fehlt: Freitext (titel, beschreibung, agentKontext, Checkliste) nur mit dem stand aus karte_zeigen ändern")
		return false
	}
	if stand != wkStand(k) {
		standGeaendert(w, k)
		return false
	}
	return true
}

// werkzeugSchreibFehler übersetzt eine Ablehnung von postOps; bekannte
// Fold-Meldungen der neuen Werkzeuge bekommen einen eigenen Satz.
func werkzeugSchreibFehler(w http.ResponseWriter, puffer *pufferAntwort, k *wkKarte) {
	if puffer == nil {
		return
	}
	for _, e := range opsProtokollArten(puffer) {
		switch zeichenketteVon(e["art"]) {
		case "feld-geaendert":
			standGeaendert(w, k)
			return
		case "fremder-kommentar":
			writeError(w, http.StatusForbidden, "nicht ausgeführt: Bearbeiten lassen sich nur Kommentare, die dieser Agent selbst geschrieben hat.")
			return
		case "ziel-fehlt":
			writeError(w, http.StatusNotFound, "nicht ausgeführt: Die Karte oder der Kommentar ist inzwischen gelöscht. Nichts geschrieben.")
			return
		}
	}
	werkzeugOpsFehler(w, puffer)
}

// karteNachSchreiben: die Karte nach dem Schreiben neu lesen (neuer stand).
func (a *BoardsAPI) karteNachSchreiben(r *http.Request, boardID, kartenID string) *wkKarte {
	ctx, cancel := contextMitFrist(r)
	defer cancel()
	b, err := a.repo.Get(ctx, boardID)
	if err != nil {
		return nil
	}
	wb, err := leseWkBoard(b.StateJSON)
	if err != nil {
		return nil
	}
	k, _ := wb.findeKarte(kartenID)
	return k
}

// kartenErgebnis: die gemeinsame Antwort der Karten-Werkzeuge.
func (a *BoardsAPI) kartenErgebnis(r *http.Request, boardID string, k *wkKarte, ergebnis string, mehr map[string]any) map[string]any {
	aus := map[string]any{"ergebnis": ergebnis, "displayId": k.DisplayID, "titel": k.Titel, "boardId": boardID,
		"link": a.kartenLink(boardID, k.DisplayID)}
	if neu := a.karteNachSchreiben(r, boardID, k.ID); neu != nil {
		aus["titel"] = neu.Titel
		aus["stand"] = wkStand(neu)
	}
	for kk, v := range mehr {
		aus[kk] = v
	}
	return aus
}

// listenOps: card.listAdd/listRemove, damit das Feld danach genau soll
// enthält. Mengen-Ops statt card.field: Was ein Mensch gleichzeitig an einem
// anderen Eintrag ändert, bleibt erhalten (ADR-0130 §3).
func listenOps(k *wkKarte, feld string, soll []string) []map[string]any {
	var ist []json.RawMessage
	_ = json.Unmarshal(k.Roh[feld], &ist)
	willSoll := map[string]bool{}
	for _, s := range soll {
		willSoll[s] = true
	}
	anzahl := map[string]int{}
	for _, e := range ist {
		var s string
		if json.Unmarshal(e, &s) == nil {
			anzahl[s]++
		}
	}
	// hat: steht nach den Ops genau einmal da. Ein gewollter Eintrag, den der
	// Bestand doppelt führt, wird entfernt und neu angehängt — listRemove
	// entfernt ALLE gleichen Einträge (src/ops.js), danach wäre er sonst weg
	// (Kritiker #755 Teil 2, K-3).
	hat := map[string]bool{}
	entfernt := map[string]bool{}
	var ops []map[string]any
	for _, e := range ist {
		var s string
		if json.Unmarshal(e, &s) == nil {
			if willSoll[s] && anzahl[s] == 1 {
				hat[s] = true
				continue
			}
			if entfernt[s] {
				continue // ein listRemove trifft alle Kopien
			}
			entfernt[s] = true
		}
		ops = append(ops, map[string]any{"type": "card.listRemove", "payload": map[string]any{"cardId": k.ID, "field": feld, "item": e}})
	}
	for _, s := range soll {
		if !hat[s] {
			ops = append(ops, map[string]any{"type": "card.listAdd", "payload": map[string]any{"cardId": k.ID, "field": feld, "item": s}})
		}
	}
	return ops
}

// --- karte_aendern ------------------------------------------------------------

var karteAendernFelder = []string{"stand", "titel", "beschreibung", "agentKontext", "owner", "beteiligte", "labels",
	"faellig", "faelligUhrzeit", "start", "startUhrzeit", "prioritaet", "komplexitaet", "aufwand", "kritisch", "blockiert", "qa"}

// werkzeugKarteAendern — POST /werkzeuge/boards/{boardId}/karten/{karte}/aendern.
// Nur die genannten Felder; Listen (beteiligte, labels) sind die ganze neue
// Liste. Ein Batch: Entweder wirken alle Änderungen oder keine.
func (a *BoardsAPI) werkzeugKarteAendern(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, karteAendernFelder...)
	if !ok {
		return
	}
	if len(m) == 0 || (len(m) == 1 && feldDa(m, "stand")) {
		writeError(w, http.StatusBadRequest, "keine Änderung angegeben — mindestens ein Feld außer stand nennen")
		return
	}
	boardID, _, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	if k.Kind == "sprint-meta" {
		for f := range m {
			if f != "beschreibung" && f != "stand" {
				writeError(w, http.StatusBadRequest, "Bei Sprint-Start- und Sprint-Abschluss-Karten ist nur die beschreibung änderbar.")
				return
			}
		}
	}
	var ops []map[string]any
	var geaendert []string
	fehler := func(grund string) { writeError(w, http.StatusBadRequest, grund) }

	// Freitext: nur mit passendem stand, als card.fieldIf.
	freitext := []struct {
		feld, schluessel string
		max              int
		pflicht, zeilen  bool
	}{
		{"titel", "title", maxVorschlagTitel, true, false},
		{"beschreibung", "description", maxVorschlagBeschreibung, false, true},
		{"agentKontext", "agentContext", maxVorschlagBeschreibung, false, true},
	}
	mitFreitext := false
	for _, f := range freitext {
		mitFreitext = mitFreitext || feldDa(m, f.feld)
	}
	if mitFreitext && !pruefeStand(w, m, k) {
		return
	}
	for _, f := range freitext {
		if !feldDa(m, f.feld) {
			continue
		}
		// Ein geänderter Titel braucht ein Inhaltszeichen; der unveränderte Bestandstitel bleibt
		// ausgenommen (F-2403, titelUnveraendert-Muster).
		pruefen := textFeld
		if f.feld == "titel" && !titelUnveraendert(m, "titel", zeichenketteVon(k.Roh["title"])) {
			pruefen = textFeldInhalt
		}
		wert, grund := pruefen(m, f.feld, f.max, f.pflicht, f.zeilen)
		if grund != "" {
			fehler(grund)
			return
		}
		alt := k.Roh[f.schluessel]
		if utf8.RuneCountInString(zeichenketteVon(alt)) > karteZeigenMaxBeschreibung {
			// karte_zeigen hat den Text gekürzt gezeigt — wer ihn ersetzt, hätte
			// den Rest nie gesehen.
			writeError(w, http.StatusConflict, fmt.Sprintf("nicht ausgeführt: %s ist länger als %d Zeichen und wurde dir gekürzt gezeigt. Bitte im Board bearbeiten.",
				f.feld, karteZeigenMaxBeschreibung))
			return
		}
		ops = append(ops, feldWennOp(k.ID, f.schluessel, wert, alt))
		geaendert = append(geaendert, f.feld)
	}

	if feldDa(m, "owner") {
		owner, grund := textFeld(m, "owner", werkzeugMaxOwner, false, false)
		if grund != "" {
			fehler(grund)
			return
		}
		ops = append(ops, feldOp(k.ID, "owner", owner))
		geaendert = append(geaendert, "owner")
	}
	for _, f := range []struct {
		feld, schluessel string
		max              int
	}{{"beteiligte", "assignees", werkzeugMaxOwner}, {"labels", "labels", werkzeugMaxLabel}} {
		liste, da, grund := leseTextListe(m, f.feld, f.max)
		if grund != "" {
			fehler(grund)
			return
		}
		if da {
			ops = append(ops, listenOps(k, f.schluessel, liste)...)
			geaendert = append(geaendert, f.feld)
		}
	}

	// Fristen: Datum und Uhrzeit. Eine Uhrzeit braucht ein Datum (ADR-0138
	// §1); wird das Datum entfernt, fällt die Uhrzeit mit.
	for _, f := range []struct{ datum, zeit, datumFeld, zeitFeld string }{
		{"faellig", "faelligUhrzeit", "dueDate", "dueTime"}, {"start", "startUhrzeit", "startDate", "startTime"},
	} {
		datumNeu := zeichenketteVon(k.Roh[f.datumFeld])
		zeitNeu := zeichenketteVon(k.Roh[f.zeitFeld])
		if feldDa(m, f.datum) {
			d, grund := leseDatumFeld(m, f.datum)
			if grund != "" {
				fehler(grund)
				return
			}
			datumNeu = d
			ops = append(ops, feldOp(k.ID, f.datumFeld, nilWennLeer(d)))
			geaendert = append(geaendert, f.datum)
			if d == "" && zeitNeu != "" && !feldDa(m, f.zeit) {
				zeitNeu = ""
				ops = append(ops, feldOp(k.ID, f.zeitFeld, nil))
			}
		}
		if feldDa(m, f.zeit) {
			z, grund := leseUhrzeitFeld(m, f.zeit)
			if grund != "" {
				fehler(grund)
				return
			}
			if z != "" && datumNeu == "" {
				fehler(f.zeit + " nur zusammen mit einem Datum (" + f.datum + ")")
				return
			}
			ops = append(ops, feldOp(k.ID, f.zeitFeld, nilWennLeer(z)))
			geaendert = append(geaendert, f.zeit)
		}
	}

	for _, f := range []struct {
		feld, schluessel string
		werte            []string
	}{{"prioritaet", "priority", prioritaetWerte}, {"komplexitaet", "complexity", komplexitaetWerte}} {
		if !feldDa(m, f.feld) {
			continue
		}
		wert, grund := textFeld(m, f.feld, 20, false, false)
		if grund == "" && !inListe(wert, f.werte) {
			grund = fmt.Sprintf("%s muss einer dieser Werte sein: %q", f.feld, f.werte)
		}
		if grund != "" {
			fehler(grund)
			return
		}
		ops = append(ops, feldOp(k.ID, f.schluessel, wert))
		geaendert = append(geaendert, f.feld)
	}
	if feldDa(m, "aufwand") {
		var aufwand any
		if strings.TrimSpace(string(m["aufwand"])) != "null" {
			var f float64
			if json.Unmarshal(m["aufwand"], &f) != nil || math.IsNaN(f) || f < 0 || f > werkzeugMaxAufwand {
				fehler(fmt.Sprintf("aufwand muss eine Zahl (Stunden) von 0 bis %d sein, oder null für „nicht geschätzt“", werkzeugMaxAufwand))
				return
			}
			aufwand = f
		}
		ops = append(ops, feldOp(k.ID, "effort", aufwand))
		geaendert = append(geaendert, "aufwand")
	}
	for _, f := range []struct{ feld, schluessel string }{{"kritisch", "critical"}, {"blockiert", "blocked"}, {"qa", "qa"}} {
		if !feldDa(m, f.feld) {
			continue
		}
		wert, grund := leseWahrFeld(m, f.feld)
		if grund != "" {
			fehler(grund)
			return
		}
		ops = append(ops, feldOp(k.ID, f.schluessel, wert))
		geaendert = append(geaendert, f.feld)
	}

	if len(ops) > 0 {
		if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, ops); !ok {
			werkzeugSchreibFehler(w, puffer, k)
			return
		}
	}
	ergebnis := fmt.Sprintf("Karte #%d geändert: %s", k.DisplayID, strings.Join(geaendert, ", "))
	if len(ops) == 0 {
		ergebnis = fmt.Sprintf("Karte #%d: nichts zu ändern (die Listen hatten schon diesen Inhalt)", k.DisplayID)
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, ergebnis, map[string]any{"geaendert": geaendert}))
}

func nilWennLeer(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- checkliste_abhaken ---------------------------------------------------------

// checklisteZeile: eine Zeile der Checkliste — dasselbe Muster wie der
// Browser (src/markdown.js CHECKLIST_RE).
var checklisteZeile = regexp.MustCompile(`(?i)^- \[( |x)\] (.*)`)

// checklistePunkte: Zeilenindizes der Checklisten-Punkte einer Beschreibung,
// in Reihenfolge — ohne Zeilen in Code-Blöcken (```). Näherung an den
// Browser: Dort zählt eine Zeile nur am Anfang eines Blocks; in einem
// HTML-Block oder einer Tabelle stünde sie hier trotzdem als Punkt.
func checklistePunkte(zeilen []string) []int {
	var punkte []int
	imCode := false
	for i, z := range zeilen {
		if strings.HasPrefix(z, "```") {
			imCode = !imCode
			continue
		}
		if !imCode && checklisteZeile.MatchString(z) {
			punkte = append(punkte, i)
		}
	}
	return punkte
}

// werkzeugChecklisteAbhaken — POST …/karten/{karte}/checkliste:
// {punkt, erledigt?, stand}. Ohne erledigt wird umgeschaltet.
func (a *BoardsAPI) werkzeugChecklisteAbhaken(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, "punkt", "erledigt", "stand")
	if !ok {
		return
	}
	if !feldDa(m, "punkt") {
		writeError(w, http.StatusBadRequest, "punkt fehlt (Nummer des Checklisten-Punkts, ab 1)")
		return
	}
	punkt, grund := leseGanzzahl(m, "punkt", 1, 10000)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	boardID, _, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil || !pruefeStand(w, m, k) {
		return
	}
	alt := k.Roh["description"]
	zeilen := strings.Split(zeichenketteVon(alt), "\n")
	punkte := checklistePunkte(zeilen)
	if punkt > len(punkte) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Karte #%d hat %d Checklisten-Punkte (Zeilen „- [ ] …“ in der Beschreibung), es gibt keinen Punkt %d.",
			k.DisplayID, len(punkte), punkt))
		return
	}
	i := punkte[punkt-1]
	treffer := checklisteZeile.FindStringSubmatch(zeilen[i])
	war := strings.EqualFold(treffer[1], "x")
	soll := !war
	if feldDa(m, "erledigt") {
		if soll, grund = leseWahrFeld(m, "erledigt"); grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
	}
	text := kuerze(treffer[2], boardLesenMaxTitel)
	mehr := map[string]any{"punkt": punkt, "text": text, "erledigt": soll}
	if soll == war {
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Punkt %d war schon %s — nichts geändert", punkt, map[bool]string{true: "abgehakt", false: "offen"}[soll]), mehr))
		return
	}
	if soll {
		zeilen[i] = "- [x]" + zeilen[i][len("- [ ]"):]
	} else {
		zeilen[i] = "- [ ]" + zeilen[i][len("- [x]"):]
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, []map[string]any{feldWennOp(k.ID, "description", strings.Join(zeilen, "\n"), alt)}); !ok {
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	ergebnis := fmt.Sprintf("Punkt %d an Karte #%d abgehakt", punkt, k.DisplayID)
	if !soll {
		ergebnis = fmt.Sprintf("Punkt %d an Karte #%d wieder offen", punkt, k.DisplayID)
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, ergebnis, mehr))
}

// --- archivieren / wiederherstellen -----------------------------------------------

// werkzeugArchivieren — POST …/karten/{karte}/archivieren (Body {}).
func (a *BoardsAPI) werkzeugArchivieren(w http.ResponseWriter, r *http.Request) {
	a.werkzeugArchivSetzen(w, r, true)
}

// werkzeugWiederherstellen — POST …/karten/{karte}/wiederherstellen (Body {}).
func (a *BoardsAPI) werkzeugWiederherstellen(w http.ResponseWriter, r *http.Request) {
	a.werkzeugArchivSetzen(w, r, false)
}

func (a *BoardsAPI) werkzeugArchivSetzen(w http.ResponseWriter, r *http.Request, archivieren bool) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	if _, ok := leseWerkzeugBody(w, r); !ok {
		return
	}
	boardID, _, k, lane := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	if k.Kind == "sprint-meta" {
		writeError(w, http.StatusBadRequest, "Sprint-Start- und Sprint-Abschluss-Karten lassen sich nicht archivieren (ADR-0036).")
		return
	}
	mehr := map[string]any{"archiviert": archivieren}
	if lane.Archiviert {
		mehr["hinweis"] = "Die Swimlane " + strconv.Quote(swimlaneName(lane)) + " ist archiviert; im Board sieht man die Karte erst, wenn die Swimlane wiederhergestellt ist (swimlane_aendern mit archiviert: false)."
	}
	if k.Archiviert == archivieren {
		zustand := map[bool]string{true: "schon archiviert", false: "nicht archiviert"}[archivieren]
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Karte #%d war %s — nichts geändert", k.DisplayID, zustand), mehr))
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, []map[string]any{feldOp(k.ID, "archived", archivieren)}); !ok {
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	ergebnis := fmt.Sprintf("Karte #%d archiviert", k.DisplayID)
	if !archivieren {
		ergebnis = fmt.Sprintf("Karte #%d wiederhergestellt", k.DisplayID)
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, ergebnis, mehr))
}

// --- kommentar_bearbeiten ---------------------------------------------------------

// werkzeugKommentarBearbeiten — POST …/karten/{karte}/kommentare/{kommentar}:
// {text}. Nur eigene Kommentare (Autor = dieser Agent); verbindlich prüft der
// Fold (eigenerAutor, fremder-kommentar), hier steht die Vorprüfung für eine
// verständliche Antwort. Den Bearbeitungsvermerk stempelt der Server.
func (a *BoardsAPI) werkzeugKommentarBearbeiten(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, "text")
	if !ok {
		return
	}
	text, grund := textFeldInhalt(m, "text", werkzeugMaxKommentar, true, true)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	boardID, _, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	kommentarID := r.PathValue("kommentar")
	var kommentare []map[string]json.RawMessage
	_ = json.Unmarshal(k.Roh["comments"], &kommentare)
	var gefunden map[string]json.RawMessage
	for _, c := range kommentare {
		if c != nil && zeichenketteVon(c["id"]) == kommentarID {
			gefunden = c
		}
	}
	switch {
	case gefunden == nil:
		writeError(w, http.StatusNotFound, "Kommentar "+strconv.Quote(kommentarID)+" gibt es an Karte #"+strconv.Itoa(k.DisplayID)+" nicht. Die ids nennt karte_zeigen (format json).")
		return
	case zeichenketteVon(gefunden["author"]) != p.Akteur():
		writeError(w, http.StatusForbidden, "nicht ausgeführt: Bearbeiten lassen sich nur Kommentare, die dieser Agent selbst geschrieben hat.")
		return
	case zeichenketteVon(gefunden["kind"]) == "extern":
		writeError(w, http.StatusBadRequest, "Die Antwort eines externen Modells ist unveränderlich (ADR-0134).")
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, []map[string]any{{"type": "comment.edit",
		"payload": map[string]any{"cardId": k.ID, "commentId": kommentarID, "text": text}}}); !ok {
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Kommentar an Karte #%d bearbeitet", k.DisplayID),
		map[string]any{"kommentarId": kommentarID}))
}

// --- verweis_setzen / verweis_entfernen ---------------------------------------------

// leseVerweisBody: {ziel, typ} mit gültigem Typ.
func leseVerweisBody(w http.ResponseWriter, r *http.Request) (ziel, typ string, ok bool) {
	m, ok := leseWerkzeugBody(w, r, "ziel", "typ")
	if !ok {
		return "", "", false
	}
	ziel, grund := textFeld(m, "ziel", maxVorschlagTitel, true, false)
	if grund == "" {
		typ, grund = textFeld(m, "typ", 20, true, false)
	}
	if grund == "" && verweisTypen[typ] == "" {
		grund = "typ muss einer dieser Werte sein: blocks, blocked-by, depends-on, required-for, related"
	}
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return "", "", false
	}
	return ziel, typ, true
}

// verweiseNach: die gespeicherten Verweis-Objekte der Karte auf targetId mit
// diesem Typ (exakt, mit allen Zusatzschlüsseln — listRemove vergleicht
// über die ganze Form).
func verweiseNach(k *wkKarte, targetID, typ string) []json.RawMessage {
	var links []json.RawMessage
	_ = json.Unmarshal(k.Roh["links"], &links)
	var aus []json.RawMessage
	for _, l := range links {
		var v map[string]json.RawMessage
		if json.Unmarshal(l, &v) == nil && v != nil && zeichenketteVon(v["targetId"]) == targetID && zeichenketteVon(v["type"]) == typ {
			aus = append(aus, l)
		}
	}
	return aus
}

// werkzeugVerweisSetzen — POST …/karten/{karte}/verweise: {ziel, typ}. Setzt
// immer beide Richtungen (ADR-0032), fehlende Seite nachträglich auch.
func (a *BoardsAPI) werkzeugVerweisSetzen(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	ziel, typ, ok := leseVerweisBody(w, r)
	if !ok {
		return
	}
	boardID, wb, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	z, _ := wb.findeWerkzeugKarte(w, ziel)
	if z == nil {
		return
	}
	if k.Kind == "sprint-meta" || z.Kind == "sprint-meta" {
		// Kritiker #755 Teil 2, K-5: Bei Sprint-Meta-Karten sind nur
		// Beschreibung und Kommentare änderbar (ADR-0036).
		writeError(w, http.StatusBadRequest, "Sprint-Start- und Sprint-Abschluss-Karten bekommen keine Verweise (ADR-0036).")
		return
	}
	if z.ID == k.ID {
		writeError(w, http.StatusBadRequest, "Eine Karte kann nicht auf sich selbst verweisen.")
		return
	}
	datum := opsNow().Format("2006-01-02")
	var ops []map[string]any
	if len(verweiseNach(k, z.ID, typ)) == 0 {
		ops = append(ops, map[string]any{"type": "card.listAdd", "payload": map[string]any{"cardId": k.ID, "field": "links",
			"item": map[string]any{"targetId": z.ID, "type": typ, "created": datum}}})
	}
	if inv := verweisTypen[typ]; len(verweiseNach(z, k.ID, inv)) == 0 {
		ops = append(ops, map[string]any{"type": "card.listAdd", "payload": map[string]any{"cardId": z.ID, "field": "links",
			"item": map[string]any{"targetId": k.ID, "type": inv, "created": datum}}})
	}
	mehr := map[string]any{"ziel": z.DisplayID, "typ": typ}
	if len(ops) == 0 {
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Verweis #%d → #%d (%s) bestand schon — nichts geändert", k.DisplayID, z.DisplayID, typ), mehr))
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, ops); !ok {
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Verweis #%d → #%d (%s) gesetzt, Rückverweis (%s) ebenso", k.DisplayID, z.DisplayID, typ, verweisTypen[typ]), mehr))
}

// werkzeugVerweisEntfernen — POST …/karten/{karte}/verweise/entfernen: {ziel,
// typ}. Entfernt den Verweis auf beiden Karten. Gibt es die Zielkarte nicht
// mehr (hängender Verweis), darf ziel ihre id sein; dann fällt nur der
// Verweis dieser Karte.
func (a *BoardsAPI) werkzeugVerweisEntfernen(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	ziel, typ, ok := leseVerweisBody(w, r)
	if !ok {
		return
	}
	boardID, wb, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	if schattenNurLesbar(w, wb.aussen, ziel) {
		// Karte #789: Auf Schatten gibt es keine Verweise (ADR-0140 §1).
		return
	}
	var treffer []wkTreffer
	for _, t := range wb.kartenTreffer(ziel) {
		if t.lane.Kind != "service" {
			treffer = append(treffer, t)
		}
	}
	if len(treffer) > 1 {
		// Wie findeWerkzeugKarte: doppelte Nummer → ids nennen.
		wb.findeWerkzeugKarte(w, ziel)
		return
	}
	zielID, zielNr := strings.TrimSpace(ziel), 0
	var z *wkKarte
	if len(treffer) == 1 {
		z = treffer[0].karte
		zielID, zielNr = z.ID, z.DisplayID
	}
	var ops []map[string]any
	for _, l := range verweiseNach(k, zielID, typ) {
		ops = append(ops, map[string]any{"type": "card.listRemove", "payload": map[string]any{"cardId": k.ID, "field": "links", "item": l}})
	}
	if z != nil {
		for _, l := range verweiseNach(z, k.ID, verweisTypen[typ]) {
			ops = append(ops, map[string]any{"type": "card.listRemove", "payload": map[string]any{"cardId": z.ID, "field": "links", "item": l}})
		}
	}
	mehr := map[string]any{"typ": typ}
	if zielNr > 0 {
		mehr["ziel"] = zielNr
	}
	if len(ops) == 0 {
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Karte #%d hat keinen solchen Verweis (%s) — nichts zu entfernen", k.DisplayID, typ), mehr))
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, ops); !ok {
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Verweis (%s) an Karte #%d entfernt, auf beiden Seiten", typ, k.DisplayID), mehr))
}

// --- uebernehmen / freigeben ---------------------------------------------------------

// leseSitzung: Pflicht — der zustandslose Zugang kennt keine Sitzung, und ohne
// sie wären zwei Sitzungen mit demselben Token derselbe Halter (ADR-0130 §1).
func leseSitzung(w http.ResponseWriter, m map[string]json.RawMessage) (string, bool) {
	s, grund := textFeld(m, "session", maxSessionLaenge, true, false)
	if grund != "" {
		writeError(w, http.StatusBadRequest, "session fehlt oder ist ungültig: eine Kennung dieser Sitzung (z. B. die Chat-Kennung), höchstens "+
			strconv.Itoa(maxSessionLaenge)+" Zeichen. Für freigeben dieselbe wie bei uebernehmen.")
		return "", false
	}
	return s, true
}

// claimVon: die Übernahme einer Karte (by, session, until) — leer ohne.
func claimVon(k *wkKarte) map[string]json.RawMessage {
	var c map[string]json.RawMessage
	if json.Unmarshal(k.Roh["claim"], &c) != nil {
		return nil
	}
	return c
}

// halterAnzeige: wer eine Karte hält — Menschen mit Namen, die Adresse nur
// für eine owner-Person (wie board_verlauf).
func (a *BoardsAPI) halterAnzeige(ctx context.Context, p *AgentPrincipal, boardID string, e map[string]json.RawMessage, schluessel string) string {
	return a.personAnzeige(ctx, zeichenketteVon(e[schluessel]), a.personIstOwner(ctx, p, boardID), map[string]string{})
}

// werkzeugUebernehmen — POST …/karten/{karte}/uebernehmen: {session,
// dauerMinuten?}. Hält eine andere Sitzung die Karte noch, 409 mit Halter.
func (a *BoardsAPI) werkzeugUebernehmen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, "session", "dauerMinuten")
	if !ok {
		return
	}
	sitzung, ok := leseSitzung(w, m)
	if !ok {
		return
	}
	payload := map[string]any{"session": sitzung}
	if feldDa(m, "dauerMinuten") && strings.TrimSpace(string(m["dauerMinuten"])) != "null" {
		minuten, grund := leseGanzzahl(m, "dauerMinuten", 1, werkzeugMaxDauerMinuten)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		payload["leaseSeconds"] = minuten * 60
	}
	boardID, _, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	if k.Kind == "sprint-meta" {
		writeError(w, http.StatusBadRequest, "Sprint-Start- und Sprint-Abschluss-Karten lassen sich nicht übernehmen (ADR-0036).")
		return
	}
	payload["cardId"] = k.ID
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, []map[string]any{{"type": "card.claim", "payload": payload}}); !ok {
		ctx, cancel := contextMitFrist(r)
		defer cancel()
		for _, e := range opsProtokollArten(puffer) {
			switch zeichenketteVon(e["art"]) {
			case "beansprucht":
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": fmt.Sprintf("nicht übernommen: Karte #%d hält gerade %s (bis %s). Nicht daran arbeiten; die Person fragen oder später erneut versuchen.",
						k.DisplayID, a.halterAnzeige(ctx, p, boardID, e, "halter"), zeichenketteVon(e["until"])),
					"displayId": k.DisplayID})
				return
			case "uhr-unsicher":
				writeError(w, http.StatusServiceUnavailable, "nicht übernommen: Die Uhr des Servers ist nicht gestellt; Übernahmen sind gesperrt, bis sie es ist.")
				return
			}
		}
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	mehr := map[string]any{"session": sitzung}
	if neu := a.karteNachSchreiben(r, boardID, k.ID); neu != nil {
		if c := claimVon(neu); c != nil {
			mehr["bis"] = zeichenketteVon(c["until"])
		}
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Karte #%d übernommen", k.DisplayID), mehr))
}

// werkzeugFreigeben — POST …/karten/{karte}/freigeben: {session}. Gibt nur
// frei, was dieser Agent in dieser Sitzung hält; eine fremde Übernahme hebt
// nur ein Mensch auf (ADR-0130 §1).
func (a *BoardsAPI) werkzeugFreigeben(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, "session")
	if !ok {
		return
	}
	sitzung, ok := leseSitzung(w, m)
	if !ok {
		return
	}
	boardID, _, k, _ := a.werkzeugKarteAusPfad(w, r)
	if k == nil {
		return
	}
	if claimVon(k) == nil {
		writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Karte #%d war nicht übernommen — nichts freizugeben", k.DisplayID), nil))
		return
	}
	if _, puffer, ok := a.werkzeugOpsRoh(w, r, boardID, []map[string]any{{"type": "card.release",
		"payload": map[string]any{"cardId": k.ID, "session": sitzung}}}); !ok {
		ctx, cancel := contextMitFrist(r)
		defer cancel()
		for _, e := range opsProtokollArten(puffer) {
			if zeichenketteVon(e["art"]) == "nicht-halter" {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": fmt.Sprintf("nicht freigegeben: Karte #%d hält %s, nicht diese Sitzung. Freigeben kann nur der Halter mit seiner session; eine fremde Übernahme hebt ein Mensch im Board auf.",
						k.DisplayID, a.halterAnzeige(ctx, p, boardID, e, "halter")),
					"displayId": k.DisplayID})
				return
			}
		}
		werkzeugSchreibFehler(w, puffer, k)
		return
	}
	writeJSON(w, http.StatusOK, a.kartenErgebnis(r, boardID, k, fmt.Sprintf("Karte #%d freigegeben", k.DisplayID), nil))
}

// --- termin_aendern ---------------------------------------------------------------

// rohTermin: der Termin mit dieser id, mit exakten Schlüsseln (wie der Fold) —
// damit Felder, die das Werkzeug nicht kennt (personen, karteId, Herkunft),
// beim Ersetzen erhalten bleiben. nil: kein solcher Termin.
func rohTermin(state []byte, id string) map[string]json.RawMessage {
	var stand map[string]json.RawMessage
	if json.Unmarshal(state, &stand) != nil {
		return nil
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(stand["board"], &board) != nil {
		return nil
	}
	var termine []map[string]json.RawMessage
	_ = json.Unmarshal(board["termine"], &termine)
	for _, t := range termine {
		if t != nil && zeichenketteVon(t["id"]) == id {
			return t
		}
	}
	return nil
}

// werkzeugTerminAendern — POST /werkzeuge/boards/{boardId}/termine/{termin}:
// nur die genannten Felder; leer oder null entfernt ein optionales Feld. Der
// ganze Termin wird danach wie bei termin_anlegen geprüft und als Ganzes
// geschrieben (termin.upsert, Last-Write-Wins je Termin, ADR-0138 §4).
func (a *BoardsAPI) werkzeugTerminAendern(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	m, ok := leseWerkzeugBody(w, r, terminAnlegenFelder...)
	if !ok {
		return
	}
	if len(m) == 0 {
		writeError(w, http.StatusBadRequest, "keine Änderung angegeben — mindestens ein Feld nennen")
		return
	}
	boardID, terminID := r.PathValue("boardId"), r.PathValue("termin")
	ctx, cancel := contextMitFrist(r)
	b, err := a.repo.Get(ctx, boardID)
	cancel()
	if err != nil {
		if a.ladeWkBoard(w, r, boardID) == nil {
			return // 403 bzw. 500 wie überall
		}
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	roh := rohTermin(b.StateJSON, terminID)
	if roh == nil {
		writeError(w, http.StatusNotFound, "Termin "+strconv.Quote(terminID)+" gibt es auf diesem Board nicht. Die terminId nennt termine_lesen.")
		return
	}
	// Bestehende Werte, darüber die genannten — dann alles wie bei der Anlage prüfen.
	gemischt := map[string]json.RawMessage{}
	for _, f := range terminAnlegenFelder {
		if s := zeichenketteVon(roh[f]); s != "" {
			gemischt[f], _ = json.Marshal(s)
		}
	}
	for f, v := range m {
		gemischt[f] = v
	}
	e, grund := leseTerminEingaben(gemischt, zeichenketteVon(roh["titel"]))
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	termin := map[string]any{}
	for k, v := range roh {
		termin[k] = v
	}
	termin["titel"], termin["datum"] = e.titel, e.datum
	for k, v := range map[string]string{"bisDatum": e.bisDatum, "von": e.von, "bis": e.bis, "ort": e.ort, "beschreibung": e.beschreibung} {
		if v == "" {
			delete(termin, k)
		} else {
			termin[k] = v
		}
	}
	if _, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{"type": "termin.upsert", "payload": map[string]any{"termin": termin}}}); !ok {
		return
	}
	t := terminEintrag{ID: terminID, Titel: e.titel, Datum: e.datum, BisDatum: e.bisDatum, Von: e.von, Bis: e.bis, Ort: e.ort}
	antwort := map[string]any{"ergebnis": "Termin geändert: " + e.titel + " am " + terminZeitText(t), "terminId": terminID,
		"titel": e.titel, "datum": e.datum, "boardId": boardID, "link": a.werkzeugTerminLink(boardID, terminID)}
	for k, v := range map[string]string{"bisDatum": e.bisDatum, "von": e.von, "bis": e.bis, "ort": e.ort} {
		if v != "" {
			antwort[k] = v
		}
	}
	writeJSON(w, http.StatusOK, antwort)
}

// --- swimlane_anlegen / swimlane_aendern ---------------------------------------------

var swimlaneArten = []string{"lane", "sprint"}

// leseSwimlaneArt: lane oder sprint; leer = standard.
func leseSwimlaneArt(m map[string]json.RawMessage, standard string) (string, string) {
	art, grund := textFeld(m, "art", 10, false, false)
	if grund != "" {
		return "", grund
	}
	if art == "" {
		art = standard
	}
	if !inListe(art, swimlaneArten) {
		return "", "art muss lane (Themen-Swimlane) oder sprint sein"
	}
	return art, ""
}

func (a *BoardsAPI) swimlaneAntwort(boardID string, s *wkSwimlane, ergebnis string, wiederholt bool) map[string]any {
	aus := map[string]any{"ergebnis": ergebnis, "swimlaneId": s.ID, "titel": swimlaneName(s), "art": s.Kind,
		"archiviert": s.Archiviert, "boardId": boardID, "link": a.boardLink(boardID)}
	if wiederholt {
		aus["wiederholt"] = true
	}
	return aus
}

// findeSwimlaneZumAendern: Swimlane per id oder Name, auch archivierte — nicht
// der allgemeine Backlog und nie der Mail-Eingang. nil: Antwort geschrieben.
func (b *wkBoard) findeSwimlaneZumAendern(w http.ResponseWriter, wunsch string) *wkSwimlane {
	var treffer []*wkSwimlane
	for i := range b.Swimlanes {
		s := &b.Swimlanes[i]
		if s.Kind == "service" || s.ID == "__backlog__" {
			continue
		}
		if s.ID == wunsch {
			return s
		}
		if strings.EqualFold(strings.TrimSpace(s.Titel), strings.TrimSpace(wunsch)) {
			treffer = append(treffer, s)
		}
	}
	switch len(treffer) {
	case 0:
		writeError(w, http.StatusNotFound, "Swimlane "+strconv.Quote(wunsch)+" gibt es auf diesem Board nicht (den allgemeinen Backlog ändert dieses Werkzeug nicht). Swimlanes nennt board_lesen.")
		return nil
	case 1:
		return treffer[0]
	}
	ids := make([]string, len(treffer))
	for i, t := range treffer {
		ids[i] = t.ID
	}
	writeError(w, http.StatusBadRequest, "Mehrere Swimlanes heißen "+strconv.Quote(wunsch)+" — bitte die id angeben: "+strings.Join(ids, ", "))
	return nil
}

// werkzeugSwimlaneAnlegen — POST /werkzeuge/boards/{boardId}/swimlanes:
// {titel, art?}. Wiederholungsschutz wie karte_anlegen; die Sprint-Karten
// (📋/🏁) legt der Browser beim nächsten Laden an, nie ein Werkzeug.
func (a *BoardsAPI) werkzeugSwimlaneAnlegen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "titel", "art")
	if !ok {
		return
	}
	titel, grund := textFeldInhalt(m, "titel", maxVorschlagTitel, true, false)
	art := ""
	if grund == "" {
		art, grund = leseSwimlaneArt(m, "lane")
	}
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	idem, ok := a.werkzeugIdemBeginn(w, r, p, boardID, "swimlane_anlegen", titel, art)
	if !ok {
		return
	}
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	for i := range wb.Swimlanes {
		s := &wb.Swimlanes[i]
		if s.ID == idem.objektID {
			writeJSON(w, http.StatusOK, a.swimlaneAntwort(boardID, s, "Swimlane war bereits angelegt (Wiederholung erkannt, nichts doppelt angelegt)", true))
			return
		}
	}
	for i := range wb.Swimlanes {
		s := &wb.Swimlanes[i]
		if s.Kind != "service" && strings.EqualFold(strings.TrimSpace(s.Titel), titel) {
			writeError(w, http.StatusBadRequest, "Eine Swimlane "+strconv.Quote(titel)+" gibt es schon (id "+s.ID+
				map[bool]string{true: ", archiviert — mit swimlane_aendern wiederherstellen", false: ""}[s.Archiviert]+"). Bitte einen anderen Titel wählen.")
			return
		}
	}
	for versuch := 0; ; versuch++ {
		quittung, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{"id": idem.clientID, "type": "swimlane.upsert",
			"payload": map[string]any{"sprint": map[string]any{"id": idem.objektID, "title": titel, "kind": art}}}})
		if !ok {
			return
		}
		if !jsWahr(quittung[0]["wiederholt"]) {
			break
		}
		// Der Ledger kennt die Op. Steht die Swimlane noch da, ist es eine
		// Wiederholung; ist sie inzwischen gelöscht, ein neuer Wunsch — wie
		// karte_anlegen (V1-4; Kritiker #755 Teil 2, K-2).
		if wb2 := a.ladeWkBoard(w, r, boardID); wb2 == nil {
			return
		} else {
			for i := range wb2.Swimlanes {
				if wb2.Swimlanes[i].ID == idem.objektID {
					writeJSON(w, http.StatusOK, a.swimlaneAntwort(boardID, &wb2.Swimlanes[i], "Swimlane war bereits angelegt (Wiederholung erkannt, nichts doppelt angelegt)", true))
					return
				}
			}
		}
		if idem.mitKopf || versuch > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "Swimlane war bereits angelegt (Wiederholung erkannt) und ist inzwischen gelöscht — nichts neu angelegt.",
				"wiederholt": true, "boardId": boardID})
			return
		}
		idem = a.werkzeugIdemErneuern(idem)
	}
	neu := &wkSwimlane{ID: idem.objektID, Titel: titel, Kind: art}
	ergebnis := "Swimlane " + strconv.Quote(titel) + " angelegt"
	if art == "sprint" {
		ergebnis += " (Sprint; die Karten 📋 Sprint-Start und 🏁 Sprint-Abschluss legt das Board beim nächsten Öffnen an)"
	}
	writeJSON(w, http.StatusOK, a.swimlaneAntwort(boardID, neu, ergebnis, false))
}

// werkzeugSwimlaneAendern — POST /werkzeuge/boards/{boardId}/swimlanes/{swimlane}:
// {titel?, art?, archiviert?}. Geschrieben werden nur die genannten
// Meta-Felder; Karten fasst swimlane.upsert nie an.
func (a *BoardsAPI) werkzeugSwimlaneAendern(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "titel", "art", "archiviert")
	if !ok {
		return
	}
	if len(m) == 0 {
		writeError(w, http.StatusBadRequest, "keine Änderung angegeben — titel, art oder archiviert nennen")
		return
	}
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	s := wb.findeSwimlaneZumAendern(w, r.PathValue("swimlane"))
	if s == nil {
		return
	}
	titel, art, archiviert := s.Titel, s.Kind, s.Archiviert
	// Der Titel geht immer mit (Verify V-4): Wird die Swimlane zwischen Lesen
	// und Falten gelöscht, entstünde sonst eine ohne Titel. Er ist das Feld,
	// bei dem ein Überschreiben am wenigsten Schaden anrichtet.
	meta := map[string]any{"id": s.ID, "title": titel}
	var grund string
	if feldDa(m, "titel") {
		// Unveränderter Bestandstitel braucht kein Inhaltszeichen (V-7, wie bei neueSpalten).
		if titelUnveraendert(m, "titel", s.Titel) {
			titel = strings.TrimSpace(s.Titel)
		} else {
			titel, grund = textFeldInhalt(m, "titel", maxVorschlagTitel, true, false)
		}
		meta["title"] = titel
	}
	if grund == "" && feldDa(m, "art") {
		art, grund = leseSwimlaneArt(m, art)
		meta["kind"] = art
	}
	if grund == "" && feldDa(m, "archiviert") {
		archiviert, grund = leseWahrFeld(m, "archiviert")
		meta["archived"] = archiviert
	}
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	// Sonst nur die genannten Meta-Felder (Kritiker #755 Teil 2, K-4): Der
	// Fold mischt nur gesetzte, eine gleichzeitige Änderung eines Menschen an
	// art oder archiviert bleibt. Hingenommen: Wird die Swimlane zwischen Lesen
	// und Falten gelöscht, legt swimlane.upsert sie mit diesen Feldern neu an
	// (der Fold kennt für swimlane.upsert kein Ziel — der Browser legt
	// Swimlanes darüber an).
	if _, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{"type": "swimlane.upsert",
		"payload": map[string]any{"sprint": meta}}}); !ok {
		return
	}
	if art == "" {
		art = "sprint" // wie der Fold für eine Swimlane ohne kind (Verify V-5)
	}
	neu := &wkSwimlane{ID: s.ID, Titel: titel, Kind: art, Archiviert: archiviert}
	writeJSON(w, http.StatusOK, a.swimlaneAntwort(boardID, neu, "Swimlane "+strconv.Quote(swimlaneName(neu))+" geändert", false))
}

// --- auf_antwort_warten --------------------------------------------------------------

// werkzeugAufAntwortWarten — POST /werkzeuge/boards/{boardId}/warten:
// {frage}. Setzt den Eintrag „wartet auf dich“ der eigenen Person (Banner im
// Board); frage leer oder null entfernt ihn. Nur Personen-Tokens
// (werkzeugDarf; agentPayload prüft die Person noch einmal).
func (a *BoardsAPI) werkzeugAufAntwortWarten(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "frage")
	if !ok {
		return
	}
	if !feldDa(m, "frage") {
		writeError(w, http.StatusBadRequest, "frage fehlt — die Frage an die Person, oder null, um den Eintrag zu entfernen")
		return
	}
	frage, grund := textFeld(m, "frage", werkzeugMaxFrage, false, true)
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	ctx, cancel := contextMitFrist(r)
	b, err := a.repo.Get(ctx, boardID)
	cancel()
	if err != nil {
		if a.ladeWkBoard(w, r, boardID) == nil {
			return
		}
		writeError(w, http.StatusInternalServerError, "board read failed", err)
		return
	}
	// Vorhandene Schlüssel der Person, ohne Rücksicht auf Groß-/Kleinschreibung
	// (der Fold setzt und löscht `person` wörtlich; der Browser vergleicht
	// ohne Rücksicht, src/awaiting.js). Sitzungs-Einträge („Person · Sitzung“)
	// gehören den Hooks und bleiben unberührt.
	var stand struct {
		Board map[string]json.RawMessage `json:"board"`
	}
	_ = json.Unmarshal(b.StateJSON, &stand)
	var eintraege map[string]json.RawMessage
	_ = json.Unmarshal(stand.Board["awaitingUser"], &eintraege)
	var vorhanden []string
	for k := range eintraege {
		if strings.EqualFold(k, p.OnBehalfOf) {
			vorhanden = append(vorhanden, k)
		}
	}
	var ops []map[string]any
	if frage == "" {
		for _, k := range vorhanden {
			ops = append(ops, map[string]any{"type": "board.awaiting", "payload": map[string]any{"person": k, "value": nil}})
		}
		if len(ops) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ergebnis": "Es gab keinen Eintrag „wartet auf dich“ für diese Person — nichts entfernt.",
				"person": p.OnBehalfOf, "boardId": boardID, "link": a.boardLink(boardID)})
			return
		}
	} else {
		// Kritiker #755 Teil 2, K-7: deterministisch — die exakte Schreibweise
		// zuerst, sonst die erste in Sortierung; weitere Varianten derselben
		// Person fallen weg, sonst bliebe ein zweites Banner stehen.
		sort.Strings(vorhanden)
		person := p.OnBehalfOf
		for i, k := range vorhanden {
			if k == p.OnBehalfOf {
				vorhanden[0], vorhanden[i] = vorhanden[i], vorhanden[0]
			}
		}
		if len(vorhanden) > 0 {
			person = vorhanden[0]
		}
		for _, k := range vorhanden[min(1, len(vorhanden)):] {
			ops = append(ops, map[string]any{"type": "board.awaiting", "payload": map[string]any{"person": k, "value": nil}})
		}
		ops = append(ops, map[string]any{"type": "board.awaiting", "payload": map[string]any{"person": person,
			"value": map[string]any{"question": frage, "since": opsNow().UTC().Format("2006-01-02T15:04:05.000Z"), "askedBy": p.Akteur()}}})
	}
	if _, ok := a.werkzeugOps(w, r, boardID, ops); !ok {
		return
	}
	antwort := map[string]any{"person": p.OnBehalfOf, "boardId": boardID, "link": a.boardLink(boardID)}
	if frage == "" {
		antwort["ergebnis"] = "Eintrag „wartet auf dich“ entfernt"
	} else {
		antwort["ergebnis"] = "Eintrag „wartet auf dich“ gesetzt — die Person sieht ihn als Banner im Board"
		antwort["frage"] = frage
	}
	writeJSON(w, http.StatusOK, antwort)
}
