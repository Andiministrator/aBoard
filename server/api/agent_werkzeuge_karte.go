package api

// agent_werkzeuge_karte.go — Werkzeuge an einer einzelnen Karte und Findings
// (ADR-0133 §5/§7, Karte #671, Stufe 2): karte_zeigen, verschieben,
// finding_anlegen. Wege, Berechtigung und Schreibpfad wie in
// agent_werkzeuge.go.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// karteZeigenMaxKommentare: die jüngsten so vielen Kommentare.
	karteZeigenMaxKommentare = 20
	// karteZeigenMaxKommentar: Zeichen je Kommentar in der Ansicht.
	karteZeigenMaxKommentar = 2000
	// karteZeigenMaxBeschreibung: Zeichen der Beschreibung in der Ansicht.
	karteZeigenMaxBeschreibung = 8000

	// karteZeigenCSP: kein Skript, keine fremden Ressourcen, kein Formular.
	// Steht als meta-Element IM Dokument: Open WebUI bettet die Antwort als
	// srcdoc ein, dort zählt keine Kopfzeile unserer Antwort — und sein
	// iframe erlaubt Skripte (allow-scripts ist immer an). Zusätzlich als
	// Kopfzeile für den Fall, dass jemand die Adresse direkt öffnet.
	karteZeigenCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; base-uri 'none'; form-action 'none'"
)

// kartenAnsicht ist der Inhalt einer Karte, wie karte_zeigen ihn ausgibt.
// Alle Texte sind Fremdtext: in JSON als Werte, im HTML über html/template
// kontextgerecht maskiert, Markdown wird NICHT gerendert.
type kartenAnsicht struct {
	// Sicherheitshinweis (K3-5, Verify V1-3): ERSTES Feld, damit er in der
	// JSON-Fassung vor allem Fremdtext steht — das Modell liest ihn, bevor es
	// Titel, Beschreibung und Kommentare liest. encoding/json schreibt
	// Struct-Felder in Deklarationsreihenfolge; eine Map ordnete alphabetisch.
	Sicherheitshinweis string   `json:"sicherheitshinweis"`
	DisplayID          int      `json:"displayId"`
	Titel              string   `json:"titel"`
	Beschreibung       string   `json:"beschreibung"`
	Spalte             string   `json:"spalte"`
	Swimlane           string   `json:"swimlane"`
	Owner              string   `json:"owner,omitempty"`
	Beteiligte         []string `json:"beteiligte,omitempty"`
	Faellig            string   `json:"faellig,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	Archiviert         bool     `json:"archiviert,omitempty"`
	// Karte #755: was karte_aendern setzen kann, damit das Modell den
	// aktuellen Wert sieht, bevor es ihn ändert.
	Start        string          `json:"start,omitempty"`
	Prioritaet   string          `json:"prioritaet,omitempty"`
	Komplexitaet string          `json:"komplexitaet,omitempty"`
	Aufwand      *float64        `json:"aufwand,omitempty"`
	Kritisch     bool            `json:"kritisch,omitempty"`
	Blockiert    bool            `json:"blockiert,omitempty"`
	QA           bool            `json:"qa,omitempty"`
	Verweise     []kartenVerweis `json:"verweise,omitempty"`
	// BeschreibungGekuerzt: Die Beschreibung ist länger als gezeigt —
	// karte_aendern ersetzt sie dann nicht (der Rest wäre ungesehen weg).
	BeschreibungGekuerzt bool `json:"beschreibungGekuerzt,omitempty"`
	// AgentKontext (ADR-0030) nur für Projekt- und Personen-Tokens, nie für
	// Open WebUI — das Feld ist ein Orchestrator-Werkzeug, kein Karteninhalt.
	AgentKontext string `json:"agentKontext,omitempty"`
	// Stand (Karte #755): Kennung von Titel, Beschreibung und Agent-Kontext.
	// karte_aendern und checkliste_abhaken verlangen ihn für Freitext.
	Stand            string            `json:"stand"`
	Finding          map[string]any    `json:"finding,omitempty"`
	Kommentare       []kartenKommentar `json:"kommentare"`
	KommentareGesamt int               `json:"kommentareGesamt"`
	BoardName        string            `json:"board"`
	BoardID          string            `json:"boardId"`
	Link             *string           `json:"link"`
}

type kartenKommentar struct {
	// ID (Karte #755): für kommentar_bearbeiten.
	ID    string `json:"id,omitempty"`
	Autor string `json:"autor"`
	Fuer  string `json:"fuer,omitempty"`
	Datum string `json:"datum,omitempty"`
	Text  string `json:"text"`
}

// kartenVerweis: ein Verweis der Karte (ADR-0032). Karte ist die Nummer der
// Zielkarte (0, wenn es sie nicht mehr gibt), id ihre Kennung — die nimmt
// verweis_entfernen auch für einen hängenden Verweis.
type kartenVerweis struct {
	Typ   string `json:"typ"`
	Karte int    `json:"karte,omitempty"`
	Titel string `json:"titel,omitempty"`
	ID    string `json:"id"`
}

func zeichenkettenListe(roh json.RawMessage) []string {
	var l []json.RawMessage
	if json.Unmarshal(roh, &l) != nil {
		return nil
	}
	var aus []string
	for _, e := range l {
		if s := zeichenketteVon(e); s != "" {
			aus = append(aus, s)
		}
	}
	return aus
}

func (a *BoardsAPI) baueAnsicht(boardID string, wb *wkBoard, k *wkKarte, lane *wkSwimlane, mitAgentKontext bool) kartenAnsicht {
	v := kartenAnsicht{DisplayID: k.DisplayID, Titel: k.Titel, Spalte: wb.spaltenTitel(k.Spalte), Swimlane: swimlaneName(lane),
		Owner: k.Owner, Faellig: k.Faellig, Archiviert: k.Archiviert || lane.Archiviert, BoardName: wb.Titel, BoardID: boardID,
		Beschreibung: kuerze(zeichenketteVon(k.Roh["description"]), karteZeigenMaxBeschreibung),
		Beteiligte:   zeichenkettenListe(k.Roh["assignees"]), Labels: zeichenkettenListe(k.Roh["labels"]),
		Kommentare: []kartenKommentar{}, Link: a.kartenLink(boardID, k.DisplayID), Sicherheitshinweis: werkzeugFremdtextHinweis,
		Stand: wkStand(k), Start: fristMitUhrzeit(k.Roh, "startDate", "startTime"),
		Prioritaet: zeichenketteVon(k.Roh["priority"]), Komplexitaet: zeichenketteVon(k.Roh["complexity"]),
		Kritisch: jsWahr(k.Roh["critical"]), Blockiert: jsWahr(k.Roh["blocked"]), QA: jsWahr(k.Roh["qa"]),
		BeschreibungGekuerzt: utf8.RuneCountInString(zeichenketteVon(k.Roh["description"])) > karteZeigenMaxBeschreibung}
	var aufwand float64
	if json.Unmarshal(k.Roh["effort"], &aufwand) == nil && strings.TrimSpace(string(k.Roh["effort"])) != "null" {
		v.Aufwand = &aufwand
	}
	if mitAgentKontext {
		v.AgentKontext = kuerze(zeichenketteVon(k.Roh["agentContext"]), karteZeigenMaxBeschreibung)
	}
	var links []map[string]json.RawMessage
	_ = json.Unmarshal(k.Roh["links"], &links)
	for _, l := range links {
		ziel := zeichenketteVon(l["targetId"])
		if l == nil || ziel == "" {
			continue
		}
		vw := kartenVerweis{Typ: zeichenketteVon(l["type"]), ID: ziel}
		if t := wb.kartenTreffer(ziel); len(t) == 1 && t[0].karte.ID == ziel {
			if t[0].lane.Kind == "service" {
				continue // Mail-Eingang gibt es für die Werkzeuge nicht
			}
			vw.Karte, vw.Titel = t[0].karte.DisplayID, kuerze(t[0].karte.Titel, boardLesenMaxTitel)
		}
		v.Verweise = append(v.Verweise, vw)
	}
	if k.Kind == "finding" {
		var f map[string]json.RawMessage
		if json.Unmarshal(k.Roh["finding"], &f) == nil && f != nil {
			v.Finding = map[string]any{"nr": "F-" + strconv.Itoa(wkZahl(f["nr"])), "typ": zeichenketteVon(f["typ"]),
				"prioritaet": zeichenketteVon(f["prioritaet"]), "status": zeichenketteVon(f["status"]),
				"beleg": zeichenketteVon(f["beleg"]), "lehre": zeichenketteVon(f["lehre"])}
		}
	}
	var kommentare []map[string]json.RawMessage
	_ = json.Unmarshal(k.Roh["comments"], &kommentare)
	v.KommentareGesamt = len(kommentare)
	start := 0
	if len(kommentare) > karteZeigenMaxKommentare {
		start = len(kommentare) - karteZeigenMaxKommentare
	}
	for _, c := range kommentare[start:] {
		if c == nil {
			continue
		}
		v.Kommentare = append(v.Kommentare, kartenKommentar{ID: zeichenketteVon(c["id"]), Autor: zeichenketteVon(c["author"]), Fuer: zeichenketteVon(c["onBehalfOf"]),
			Datum: zeichenketteVon(c["created"]), Text: kuerze(zeichenketteVon(c["text"]), karteZeigenMaxKommentar)})
	}
	return v
}

// karteZeigenVorlage: schreibgeschützte Ansicht. Kein Skript, keine externen
// Ressourcen; der einzige Link führt ins Board (neuer Tab).
var karteZeigenVorlage = template.Must(template.New("karte").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="` + karteZeigenCSP + `">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>#{{.DisplayID}} {{.Titel}}</title>
<style>
:root{color-scheme:light dark;--fg:#1f2328;--mute:#59636e;--bg:#fff;--rand:#d1d9e0;--marke:#0969da}
@media (prefers-color-scheme:dark){:root{--fg:#e6edf3;--mute:#9198a1;--bg:#0d1117;--rand:#3d444d;--marke:#4493f8}}
body{margin:0;padding:16px;font:14px/1.5 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
h1{font-size:18px;margin:4px 0 12px}h2{font-size:14px;margin:16px 0 6px}
.nr{color:var(--mute);font-weight:600}.marke{display:inline-block;border:1px solid var(--rand);border-radius:10px;padding:0 8px;margin-right:4px;font-size:12px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:2px 12px;margin:0}dt{color:var(--mute)}dd{margin:0}
.text{white-space:pre-wrap;overflow-wrap:anywhere;border-left:3px solid var(--rand);padding-left:10px}
.kommentar{margin:8px 0}.kopf{color:var(--mute);font-size:12px}
a{color:var(--marke)}
</style></head><body><article>
<div><span class="nr">#{{.DisplayID}}</span>{{with .Finding}} <span class="marke">{{index . "nr"}} · {{index . "typ"}} · {{index . "prioritaet"}} · {{index . "status"}}</span>{{end}}{{if .Archiviert}} <span class="marke">archiviert</span>{{end}}</div>
<h1>{{.Titel}}</h1>
<dl>
<dt>Board</dt><dd>{{.BoardName}}</dd>
<dt>Swimlane</dt><dd>{{.Swimlane}}</dd>
<dt>Spalte</dt><dd>{{.Spalte}}</dd>
{{if .Owner}}<dt>Owner</dt><dd>{{.Owner}}</dd>{{end}}
{{if .Beteiligte}}<dt>Beteiligte</dt><dd>{{range $i, $b := .Beteiligte}}{{if $i}}, {{end}}{{$b}}{{end}}</dd>{{end}}
{{if .Faellig}}<dt>Fällig</dt><dd>{{.Faellig}}</dd>{{end}}
{{if .Labels}}<dt>Labels</dt><dd>{{range .Labels}}<span class="marke">{{.}}</span>{{end}}</dd>{{end}}
</dl>
{{if .Beschreibung}}<h2>Beschreibung</h2><div class="text">{{.Beschreibung}}</div>{{end}}
{{with .Finding}}{{if index . "beleg"}}<h2>Beleg</h2><div class="text">{{index . "beleg"}}</div>{{end}}{{if index . "lehre"}}<h2>Lehre</h2><div class="text">{{index . "lehre"}}</div>{{end}}{{end}}
{{if .Kommentare}}<h2>Kommentare{{if gt .KommentareGesamt (len .Kommentare)}} (die jüngsten {{len .Kommentare}} von {{.KommentareGesamt}}){{end}}</h2>
{{range .Kommentare}}<div class="kommentar"><div class="kopf">{{.Autor}}{{if .Fuer}} für {{.Fuer}}{{end}}{{if .Datum}} · {{.Datum}}{{end}}</div><div class="text">{{.Text}}</div></div>
{{end}}{{end}}
{{with .Link}}<p><a href="{{.}}" target="_blank" rel="noopener noreferrer">im Board öffnen</a></p>{{end}}
</article></body></html>
`))

// werkzeugKarteZeigen — GET /werkzeuge/boards/{boardId}/karten/{karte}.
// format=html (Standard): fertige, schreibgeschützte Ansicht für die Person
// (Open WebUI bettet sie ein, wenn Content-Disposition inline und
// Content-Type text/html — das Modell sieht dann nur, dass etwas angezeigt
// wurde). format=json: derselbe Inhalt als Daten für das Modell.
func (a *BoardsAPI) werkzeugKarteZeigen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil || !a.werkzeugLesenErlaubt(w, p) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format != "" && format != "html" && format != "json" {
		writeError(w, http.StatusBadRequest, "format muss html oder json sein")
		return
	}
	boardID := r.PathValue("boardId")
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	if ref := r.PathValue("karte"); wb.aussen && schattenNummer(ref) > 0 {
		// Karte #789 (ADR-0140 §7): „Karte nach Nummer“ mit i-<nr> zeigt den
		// Schatten, nur lesbar und ohne Link.
		ctx, cancel := contextMitFrist(r)
		defer cancel()
		liste, _, err := a.werkzeugSchattenLesen(ctx, boardID)
		if err != nil {
			schattenLeseFehler(w, err)
			return
		}
		schattenZeigen(w, boardID, ref, format, liste)
		return
	}
	k, lane := wb.findeWerkzeugKarte(w, r.PathValue("karte")) // 404 bzw. 400 bei doppelter Nummer (KO-4)
	if k == nil {
		return
	}
	// agentKontext (ADR-0030): nur, wer ihn auch schreiben darf — editor, nie
	// Open WebUI (Kritiker #755 Teil 2, K-9).
	ansicht := a.baueAnsicht(boardID, wb, k, lane, !p.Delegiert && p.Role == storage.AgentRolleEditor)
	if format == "json" {
		writeJSON(w, http.StatusOK, ansicht)
		return
	}
	var puffer strings.Builder
	if err := karteZeigenVorlage.Execute(&puffer, ansicht); err != nil {
		writeError(w, http.StatusInternalServerError, "render failed", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	// Für einen Werkzeug-Server, den der Browser direkt anspricht (Nutzer-
	// Werkzeug-Server in OWUI): sonst sähe dessen Skript die Kopfzeile nicht.
	w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition")
	w.Header().Set("Content-Security-Policy", karteZeigenCSP+"; "+FrameAncestorsPolicy)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(puffer.String()))
}

// werkzeugVerschieben — POST /werkzeuge/boards/{boardId}/karten/{karte}/verschieben.
// {spalte, swimlane?}: Spalte per Name oder id; ohne swimlane bleibt die Karte
// in ihrer. Sprint-Meta-Karten und archivierte Karten nicht (die verschiebt
// auch der Browser nicht).
func (a *BoardsAPI) werkzeugVerschieben(w http.ResponseWriter, r *http.Request) {
	if werkzeugPrincipal(w, r) == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "spalte", "swimlane")
	if !ok {
		return
	}
	spalteWunsch, grund := textFeld(m, "spalte", maxVorschlagTitel, true, false)
	laneWunsch := ""
	if grund == "" {
		laneWunsch, grund = textFeld(m, "swimlane", maxVorschlagTitel, false, false)
	}
	if grund != "" {
		writeError(w, http.StatusBadRequest, grund)
		return
	}
	wb := a.ladeWkBoard(w, r, boardID)
	if wb == nil {
		return
	}
	k, lane := wb.findeWerkzeugKarte(w, r.PathValue("karte")) // 404 bzw. 400 bei doppelter Nummer (KO-4)
	if k == nil {
		return
	}
	if k.Kind == "sprint-meta" || k.Archiviert || lane.Archiviert {
		writeError(w, http.StatusBadRequest, "Diese Karte lässt sich nicht verschieben (Sprint-Start/-Abschluss oder archiviert).")
		return
	}
	spalte, gefunden := wb.findeSpalte(spalteWunsch)
	if !gefunden {
		wb.spalteFehlt(w, spalteWunsch)
		return
	}
	ziel := lane
	if laneWunsch != "" {
		if ziel = wb.findeSwimlane(w, laneWunsch); ziel == nil {
			return
		}
	}
	vorher := map[string]string{"spalte": wb.spaltenTitel(k.Spalte), "swimlane": swimlaneName(lane)}
	if _, ok := a.werkzeugOps(w, r, boardID, []map[string]any{{
		"type":    "card.move",
		"payload": map[string]any{"cardId": k.ID, "toSprintId": ziel.ID, "toColId": spalte},
	}}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ergebnis":  fmt.Sprintf("Karte #%d nach %s verschoben", k.DisplayID, wb.spaltenTitel(spalte)),
		"displayId": k.DisplayID,
		"titel":     k.Titel,
		"vorher":    vorher,
		"nachher":   map[string]string{"spalte": wb.spaltenTitel(spalte), "swimlane": swimlaneName(ziel)},
		"boardId":   boardID,
		"link":      a.kartenLink(boardID, k.DisplayID),
	})
}

// werkzeugFindingAnlegen — POST /werkzeuge/boards/{boardId}/findings.
// Eine Karte mit kind 'finding' (ADR-0133 §1). Den Wortschatz von typ,
// prioritaet und status prüft der Fold (src/finding.js) — eine Regel, ein
// Aufschrieb; seine Begründung kommt als details zurück. Die Nummer vergibt
// der Fold.
func (a *BoardsAPI) werkzeugFindingAnlegen(w http.ResponseWriter, r *http.Request) {
	p := werkzeugPrincipal(w, r)
	if p == nil {
		return
	}
	boardID := r.PathValue("boardId")
	m, ok := leseWerkzeugBody(w, r, "titel", "beschreibung", "typ", "prioritaet", "status", "beleg", "lehre", "swimlane")
	if !ok {
		return
	}
	werte := map[string]string{}
	for _, f := range []struct {
		name            string
		max             int
		pflicht, zeilen bool
	}{
		{"titel", maxVorschlagTitel, true, false}, {"beschreibung", maxVorschlagBeschreibung, false, true},
		{"typ", 10, true, false}, {"prioritaet", 10, true, false}, {"status", 40, false, false},
		{"beleg", maxVorschlagBeschreibung, false, true}, {"lehre", maxVorschlagBeschreibung, false, true},
		{"swimlane", maxVorschlagTitel, false, false},
	} {
		// Pflicht-Titel wie bei karte_anlegen mit Inhaltszeichen (F-2403).
		pruefen := textFeld
		if f.name == "titel" {
			pruefen = textFeldInhalt
		}
		v, grund := pruefen(m, f.name, f.max, f.pflicht, f.zeilen)
		if grund != "" {
			writeError(w, http.StatusBadRequest, grund)
			return
		}
		werte[f.name] = v
	}
	if werte["status"] == "" {
		werte["status"] = "offen"
	}
	finding := map[string]any{"typ": werte["typ"], "prioritaet": werte["prioritaet"], "status": werte["status"]}
	for _, f := range []string{"beleg", "lehre"} {
		if werte[f] != "" {
			finding[f] = werte[f]
		}
	}
	idem, ok := a.werkzeugIdemBeginn(w, r, p, boardID, "finding_anlegen", werte["titel"], werte["beschreibung"], werte["typ"],
		werte["prioritaet"], werte["status"], werte["beleg"], werte["lehre"], werte["swimlane"])
	if !ok {
		return
	}
	a.karteAnlegen(w, r, boardID, werte["titel"], werte["beschreibung"], werte["swimlane"], "",
		map[string]any{"kind": "finding", "finding": finding}, idem)
}
