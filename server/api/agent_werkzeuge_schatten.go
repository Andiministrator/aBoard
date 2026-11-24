package api

// agent_werkzeuge_schatten.go — Board-Paar in den Agenten-Werkzeugen
// (Karte #789, ADR-0140 §7 und §10 W-4).
//
// Außen (boardpaar.seite = aussen) lesen die Werkzeuge die Schatten interner
// Karten und Termine — NUR an drei Stellen: board_lesen, karte_zeigen mit
// „i-<nr>“ und karten_suchen. Der einzige Leser der Tabelle ist
// werkzeugSchattenLesen; der Quelltext-Wächter (main_schatten_780_test.go)
// lässt ihn genau dort und nur aus diesen drei Handlern zu.
//
// Was hinausgeht, ist ein fester Feldsatz (wkSchatten): Art, Nummer i-<nr>,
// Schatten-ID (HMAC des Skripts, nicht umkehrbar), Titel bzw. „Interne
// Karte“, Spalte, Priorität, Termine, Bearbeiter, vonAgent, nurLesbar. KEIN
// Link, keine Basisadresse, keine interne Board- oder Karten-ID, keine
// E-Mail-Adresse (Vertrag Push-Skript K-19, T2, T21). Die Adresse des internen Boards
// (boardpaar_aussen.innen_link_basis) geben die Werkzeuge nie aus; die
// Schatten-Zeilen tragen sie ohnehin nicht (ADR-0140 §1).
//
// Schreibende Werkzeuge finden Schatten nicht: Eine Kartenangabe „i-<nr>“
// beantworten sie auf der Seite außen mit 400 (findeWerkzeugKarte).
//
// Werkzeugtexte je Seite (E-3): werkzeugPaarText und katalogFuerSeite. Auf
// „aus“ ändert sich kein Byte der Beschreibung.

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	// schattenNurLesbarMeldung: 400 eines schreibenden Werkzeugs auf „i-<nr>“
	// (ADR-0140 §7, wörtlich).
	schattenNurLesbarMeldung = "%s ist eine interne Karte und hier nur lesbar"
	// boardLesenMaxSchattenBytes: eigenes Budget der Schatten in board_lesen,
	// damit viele externe Karten die internen nicht verdrängen (und umgekehrt).
	boardLesenMaxSchattenBytes = 8 << 10
)

// MitBoardpaar hängt Seite, Kopplungen und Schatten an (Karte #789). Ohne
// diesen Aufruf gilt die Seite „aus“: keine Schatten, keine Paar-Texte, keine
// W-4-Regel.
func (a *BoardsAPI) MitBoardpaar(cfg *storage.ConfigRepo, bp *storage.BoardpaarRepo, sr *storage.SchattenRepo) *BoardsAPI {
	a.paarCfg, a.paar, a.schatten = cfg, bp, sr
	return a
}

// paarSeiteSchluessel: Kontext-Schlüssel der einmal je Anfrage gelesenen
// Seite (Kritiker #789, K-6).
type paarSeiteSchluessel struct{}

type paarSeiteWert struct {
	seite string
	err   error
}

// mitPaarSeite liest die Seite EINMAL je Anfrage und legt sie samt einem
// Lesefehler in den Kontext (Kritiker #789, K-6). Alle Werkzeug-Wege, die
// Beschreibung und der MCP-Eingang laufen hindurch (RegisterWerkzeuge);
// Folgeaufrufe derselben Anfrage (board_lesen, werkzeugOpsRoh → postOps,
// MCP tools/call) lesen aus dem Kontext statt aus server_config. Ohne
// MitBoardpaar entfällt die Lesung ganz.
func (a *BoardsAPI) mitPaarSeite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.paarCfg == nil {
			next.ServeHTTP(w, r)
			return
		}
		seite, err := BoardpaarSeiteMitFehler(r.Context(), a.paarCfg)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), paarSeiteSchluessel{}, paarSeiteWert{seite, err})))
	})
}

// paarSeiteMitFehler: Seite dieser Instanz (ADR-0140 §4) — aus dem Kontext,
// wenn mitPaarSeite sie schon gelesen hat, sonst einmal aus server_config.
// err ≠ nil nur bei einem Lesefehler (Seite dann „aus“).
func (a *BoardsAPI) paarSeiteMitFehler(ctx context.Context) (string, error) {
	if v, ok := ctx.Value(paarSeiteSchluessel{}).(paarSeiteWert); ok {
		return v.seite, v.err
	}
	if a.paarCfg == nil {
		return BoardpaarSeiteAus, nil
	}
	return BoardpaarSeiteMitFehler(ctx, a.paarCfg)
}

// paarSeite: wie paarSeiteMitFehler; ein Lesefehler gilt als „aus“ (Lesewege
// und Texte — fail-closed heißt dort: kein Schatten, kein Paar-Text).
func (a *BoardsAPI) paarSeite(ctx context.Context) string {
	seite, err := a.paarSeiteMitFehler(ctx)
	if err != nil {
		log.Printf("boardpaar: seite nicht lesbar, gilt als aus: %v", err)
	}
	return seite
}

// schattenNummerMuster: „i-12“ (auch „#i-12“, „I-12“) — die Nummer eines
// Schattens, wie Menschen und Werkzeuge sie nennen.
var schattenNummerMuster = regexp.MustCompile(`^#?[iI]-([1-9][0-9]{0,9})$`)

// schattenNummer: die Zahl hinter „i-“, sonst 0.
func schattenNummer(ref string) int {
	m := schattenNummerMuster.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// schattenNurLesbar: 400, wenn ref auf der Seite außen eine Schatten-Nummer
// ist. true: Antwort ist geschrieben.
func schattenNurLesbar(w http.ResponseWriter, aussen bool, ref string) bool {
	if !aussen || schattenNummer(ref) == 0 {
		return false
	}
	writeError(w, http.StatusBadRequest, strings.Replace(schattenNurLesbarMeldung, "%s", strings.TrimPrefix(strings.TrimSpace(ref), "#"), 1))
	return true
}

// wkSchatten: ein Schatten, wie die Werkzeuge ihn zeigen (ADR-0140 §7).
type wkSchatten struct {
	Art        string   `json:"art"`
	Nummer     string   `json:"nummer,omitempty"`
	SchattenID string   `json:"schattenId"`
	Titel      string   `json:"titel"`
	Spalte     string   `json:"spalte,omitempty"`
	Prioritaet string   `json:"prioritaet,omitempty"`
	StartDatum string   `json:"startDatum,omitempty"`
	StartZeit  string   `json:"startZeit,omitempty"`
	EndeDatum  string   `json:"endeDatum,omitempty"`
	EndeZeit   string   `json:"endeZeit,omitempty"`
	Bearbeiter []string `json:"bearbeiter"`
	VonAgent   bool     `json:"vonAgent"`
	NurLesbar  bool     `json:"nurLesbar"`
	// nr / vertraulich / platzhalter: nur für Suche, Nummernvergleich und die
	// Anzeige (titel_platzhalter), nie im JSON der Werkzeuge. platzhalter: Der
	// Titel ist „Interne Karte“, nicht der echte (vertraulich oder ohne Titel).
	nr          int
	vertraulich bool
	platzhalter bool
	// lane/spalteID (Karte #825): Kennungen aus dem Hinweg v2, nur für die
	// Anzeige in Lane und Spalte; nie im JSON der Werkzeuge.
	lane, spalteID string
}

func textOderLeer(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// baueWkSchatten: fester Feldsatz aus einer Tabellenzeile. Den Titel eines
// vertraulichen Schattens liest diese Funktion nicht (die Tabelle trägt ihn
// ohnehin nicht, der Eingang lehnt ihn ab — zweite Schicht wie im Kalender).
// Bearbeiter mit „@“ oder „external:“ fallen weg, auch wenn der Eingang sie
// schon ablehnt (fail-closed, T21).
func baueWkSchatten(z storage.SchattenZeile) wkSchatten {
	s := wkSchatten{Art: z.Art, SchattenID: z.SchattenID, Bearbeiter: []string{},
		VonAgent: z.VonAgent, NurLesbar: true, vertraulich: z.Vertraulich,
		StartDatum: textOderLeer(z.StartDatum), StartZeit: textOderLeer(z.StartZeit),
		EndeDatum: textOderLeer(z.EndeDatum), EndeZeit: textOderLeer(z.EndeZeit)}
	var echt bool
	// ADR-0141 Beschluss 3: Zeilen aus dem Hinweg v2 (Fassung 2) zeigen auch
	// den Titel eines vertraulichen Schattens; Fassung 1 bleibt streng.
	s.Titel, echt = schatten.AnzeigeTitel(z.Vertraulich && z.Fassung != 2, z.Titel) // eine Regel mit dem Feed (C-2)
	s.platzhalter = !echt
	if z.Art == schatten.ArtKarte {
		if z.Nr != nil && *z.Nr > 0 {
			s.nr = *z.Nr
			s.Nummer = "i-" + strconv.Itoa(*z.Nr)
		}
		s.Spalte, s.Prioritaet = textOderLeer(z.Spalte), textOderLeer(z.Prioritaet)
		s.lane, s.spalteID = textOderLeer(z.Lane), textOderLeer(z.SpalteID)
	}
	for _, b := range z.Bearbeiter {
		if b == "" || strings.Contains(b, "@") || strings.HasPrefix(strings.ToLower(b), "external:") {
			continue
		}
		s.Bearbeiter = append(s.Bearbeiter, b)
	}
	return s
}

// werkzeugSchattenLesen: die Schatten eines Boards für die Agentensicht außen.
// Der EINZIGE Leser der Schatten-Tabelle in den Werkzeugen — erlaubt nur aus
// werkzeugBoardLesen, werkzeugKarteZeigen und werkzeugKartenSuchen (Wächter).
// aktiv false: Seite nicht außen (oder kein Speicher) — dann bleiben die
// Antworten, wie sie ohne Board-Paar waren.
func (a *BoardsAPI) werkzeugSchattenLesen(ctx context.Context, boardID string) (liste []wkSchatten, aktiv bool, err error) {
	if a.schatten == nil || a.paarSeite(ctx) != BoardpaarSeiteAussen {
		return nil, false, nil
	}
	zeilen, err := a.schatten.Liste(ctx, boardID)
	if err != nil {
		return nil, true, err
	}
	liste = make([]wkSchatten, 0, len(zeilen))
	for _, z := range zeilen {
		liste = append(liste, baueWkSchatten(z))
	}
	sortiereWkSchatten(liste)
	return liste, true, nil
}

// sortiereWkSchatten: Karten nach Nummer, dann Termine nach Datum — stabil
// für das Modell und für die Anzeige außen (#781, dieselbe Reihenfolge).
func sortiereWkSchatten(liste []wkSchatten) {
	sort.SliceStable(liste, func(i, j int) bool {
		if (liste[i].Art == schatten.ArtKarte) != (liste[j].Art == schatten.ArtKarte) {
			return liste[i].Art == schatten.ArtKarte
		}
		if liste[i].nr != liste[j].nr {
			return liste[i].nr < liste[j].nr
		}
		return liste[i].StartDatum+liste[i].EndeDatum < liste[j].StartDatum+liste[j].EndeDatum
	})
}

// wkBoardpaar: die Rolle eines gekoppelten Boards im Paar (boards,
// board_lesen). Bewusst OHNE Kennung oder Adresse des Gegenstücks: außen
// verbietet §7 jede Kennung nach innen; innen kennt der Server das externe
// Board nicht (die Zuordnung führt das Skript, „Umsetzung #788“).
type wkBoardpaar struct {
	Rolle       string `json:"rolle"`       // "extern" | "intern"
	Gegenstueck string `json:"gegenstueck"` // "intern" | "extern"
	Hinweis     string `json:"hinweis"`
	// Kopie (Karte #797, F-2230): Das Board ist eine externe Kopie (ADR-0141).
	// Nur dann gesetzt; Stufe-1-Boards tragen das Feld nicht (omitempty), ihre
	// Antwort bleibt Byte für Byte wie vorher.
	Kopie bool `json:"kopie,omitempty"`
	// rueckwegAn: der wirksame Rückweg dieses Boards — boardpaar.rueckweg = an
	// UND bearbeitbar = 1 (#836); nur für die Wahl des Texts.
	rueckwegAn bool
}

const (
	paarHinweisAussen = "Zu diesem Board gibt es ein internes Board im Firmennetz. Karten mit Nummern wie i-12 leben dort; hier sind sie nur lesbar. Neue Karten gehören nach innen, solange die Person nichts anderes sagt."
	// paarHinweisInternerAgent: F-2078 — statt paarHinweisAussen für die Rolle
	// intern (#783). Sie liest außen über /intern/{board}/board; „Neue Karten
	// gehören nach innen“ ist für sie falsch, ihre kritischen Aktionen werden
	// zu Vorschlägen (ADR-0140 §8).
	paarHinweisInternerAgent = "Du bist der interne Agent dieses Boards. Karten mit Nummern wie i-12 leben innen und sind hier nur lesbar. " +
		"Spalte und Priorität einer externen Karte änderst du hier sofort. Als Vorschlag, den ein Mensch bestätigt, gehen nur: " +
		"Anlegen, Text, Bearbeiter, Kommentar, Löschen, Archivieren; anderes geht hier nicht. Was vertraulich ist, trägst du nie nach außen."
	// Karte #797 (F-2230, K-2): Auf einer externen Kopie (ADR-0141) gilt der
	// Stufe-1-Text nicht. Nicht vertrauliche Karten sind Spiegel mit
	// gewöhnlicher externer Nummer (#N); nur vertrauliche stehen als Schatten
	// i-<nr>, mit echtem Titel, und sind immer nur lesbar. Mit „Bearbeiten
	// außen erlauben“ (boardpaar.rueckweg = an, seit #836 nur auf Kopien mit
	// bearbeitbar = 1) wird außen bearbeitet und
	// angelegt, der Rückweg trägt es nach innen.
	paarHinweisKopieAus = "Dieses Board ist eine externe Kopie eines internen Boards. Nicht vertrauliche Karten des internen Boards stehen hier als Spiegel mit gewöhnlicher Nummer (#N): " +
		"gewöhnliche Karten, hier nur lesbar. Vertrauliche stehen nur als Schatten i-<nr>, mit echtem Titel, immer nur lesbar. " +
		"Änderungen an Spiegeln und neue Karten in gespiegelten Swimlanes lehnt der Server ab (Bearbeiten außen ist nicht erlaubt); " +
		"eigene Swimlanes außen sind frei. Neue Karten gehören nach innen, solange die Person nichts anderes sagt."
	paarHinweisKopieAn = "Dieses Board ist eine externe Kopie eines internen Boards, und Bearbeiten außen ist erlaubt. Nicht vertrauliche Karten des internen Boards stehen hier als Spiegel mit gewöhnlicher Nummer (#N); " +
		"vertrauliche nur als Schatten i-<nr>, mit echtem Titel, immer nur lesbar. Spiegel (#N) darfst du in den erlaubten Feldern bearbeiten und neue Karten in gespiegelten Swimlanes anlegen; die Änderung geht danach nach innen " +
		"(und gilt dort nicht, wenn innen inzwischen etwas anderes steht). Löschen, QA, Aufwand, Anhänge und Verweise bleiben gesperrt. " +
		"Lege nur an oder ändere, was die Person will."
	// K-4: Der interne Agent auf einer Kopie bekommt nicht den Satz „Spalte und
	// Priorität änderst du sofort“ für Spiegel, wenn Bearbeiten außen aus ist.
	paarHinweisInternerAgentKopieAus = "Du bist der interne Agent dieses Boards, einer externen Kopie. Nicht vertrauliche Karten stehen hier als Spiegel mit gewöhnlicher Nummer (#N) und sind nur lesbar: " +
		"Änderungen daran lehnt der Server ab (Bearbeiten außen ist nicht erlaubt). Vertrauliche stehen nur als Schatten i-<nr>, mit echtem Titel, immer nur lesbar. " +
		"Für Karten in eigenen Swimlanes außen gilt wie sonst: Spalte und Priorität änderst du sofort. Als Vorschlag, den ein Mensch bestätigt, gehen nur: " +
		"Anlegen, Text, Bearbeiter, Kommentar, Löschen, Archivieren; anderes geht hier nicht. " +
		"Vorschläge zu Spiegeln und neue Karten im gespiegelten Backlog lassen sich, solange Bearbeiten außen nicht erlaubt ist, nicht bestätigen. Was vertraulich ist, trägst du nie nach außen."
	paarHinweisInternerAgentKopieAn = "Du bist der interne Agent dieses Boards, einer externen Kopie, und Bearbeiten außen ist erlaubt. Nicht vertrauliche Karten stehen hier als Spiegel mit gewöhnlicher Nummer (#N), " +
		"vertrauliche nur als Schatten i-<nr>, mit echtem Titel, immer nur lesbar. Spalte und Priorität einer externen Karte, auch eines Spiegels, änderst du hier sofort. " +
		"Als Vorschlag, den ein Mensch bestätigt, gehen nur: Anlegen, Text, Bearbeiter, Kommentar, Löschen, Archivieren; anderes geht hier nicht. Löschen von Spiegeln bleibt gesperrt. Was vertraulich ist, trägst du nie nach außen."
	paarHinweisInnen = "Zu diesem Board gibt es ein externes Board. Was außen steht, kann jeder lesen, auch Cloud-Agenten. Karten und Termine, die ein Agent hier anlegt, sind vertraulich, bis ein Mensch sie freigibt — außer der Agent sendet ausdrücklich vertraulich: false; dann gehen sie vollständig nach außen."
)

// werkzeugBoardpaare: Rolle je gekoppeltem Board dieser Instanz (nil auf
// „aus“ oder ohne Speicher). Ein Lesefehler heißt „kein Paar“ — die Angabe
// ist eine Lesehilfe, keine Grenze.
func (a *BoardsAPI) werkzeugBoardpaare(ctx context.Context) map[string]*wkBoardpaar {
	if a.paar == nil {
		return nil
	}
	aus := map[string]*wkBoardpaar{}
	switch a.paarSeite(ctx) {
	case BoardpaarSeiteAussen:
		liste, err := a.paar.AussenListe(ctx)
		if err != nil {
			return nil
		}
		// Der Schalter „Bearbeiten außen erlauben“ gilt je Instanz (und wirkt je
		// Kopie nur mit bearbeitbar = 1, #836); nur gelesen,
		// wenn eine Kopie dabei ist. Unlesbar heißt „aus“ (der strengere Text).
		rueckwegAn, rueckwegGelesen := false, false
		for _, e := range liste {
			bp := &wkBoardpaar{Rolle: "extern", Gegenstueck: "intern", Hinweis: paarHinweisAussen}
			if e.Kopie {
				if !rueckwegGelesen {
					rueckwegGelesen = true
					// Die Richtung „unlesbar heißt aus“ trägt der Leser selbst:
					// BoardpaarRueckwegAnMitFehler liefert bei einem Lesefehler
					// schon false (Test TestVertraulich797_LeserLiefertBeiFehlerAus).
					// Die Zuweisung hier ist damit nur Weitergabe — wer den Leser
					// ändert, muss diese Zusage mitnehmen.
					if an, err := a.rueckwegAnMitFehler(ctx); err == nil {
						rueckwegAn = an
					}
				}
				bp.Kopie = true
				// #836 (ADR-0141 Nachtrag): Der Server-Schalter wirkt nur auf
				// Kopien mit bearbeitbar = 1; die Partnerliste trägt den Wert
				// (Lesefehler: die ganze Liste fehlt, oben → kein Paar).
				bp.rueckwegAn = rueckwegAn && e.Bearbeitbar
				bp.Hinweis = paarHinweisKopieAus
				if bp.rueckwegAn {
					bp.Hinweis = paarHinweisKopieAn
				}
			}
			aus[e.BoardID] = bp
		}
	case BoardpaarSeiteInnen:
		liste, err := a.paar.InnenListe(ctx)
		if err != nil {
			return nil
		}
		for _, e := range liste {
			aus[e.BoardID] = &wkBoardpaar{Rolle: "intern", Gegenstueck: "extern", Hinweis: paarHinweisInnen}
		}
	default:
		return nil
	}
	return aus
}

// werkzeugBoardpaarVon: die Rolle eines Boards, nil ohne Kopplung.
func (a *BoardsAPI) werkzeugBoardpaarVon(ctx context.Context, boardID string) *wkBoardpaar {
	return a.werkzeugBoardpaare(ctx)[boardID]
}

// werkzeugBoardpaarFuer: wie werkzeugBoardpaarVon, mit dem Hinweis für die
// Rolle des Aufrufers (F-2078). Die Rolle intern bekommt außen ihren eigenen
// Text; die Zeilen in werkzeugPaarZusatz (karte_anlegen & Co.) erreicht sie
// nicht, weil sie weder Werkzeuge noch MCP hat (werkzeugDarf).
func (a *BoardsAPI) werkzeugBoardpaarFuer(ctx context.Context, p *AgentPrincipal, boardID string) *wkBoardpaar {
	bp := a.werkzeugBoardpaarVon(ctx, boardID)
	if bp != nil && istInternRolle(p) && bp.Rolle == "extern" {
		kopie := *bp
		kopie.Hinweis = paarHinweisInternerAgent
		if bp.Kopie {
			kopie.Hinweis = paarHinweisInternerAgentKopieAus
			if bp.rueckwegAn {
				kopie.Hinweis = paarHinweisInternerAgentKopieAn
			}
		}
		return &kopie
	}
	return bp
}

// --- karte_zeigen mit i-<nr> ---------------------------------------------------

// wkSchattenAnsicht: Antwort von karte_zeigen für einen Schatten.
type wkSchattenAnsicht struct {
	Sicherheitshinweis string `json:"sicherheitshinweis"`
	wkSchatten
	BoardID string `json:"boardId"`
	Hinweis string `json:"hinweis"`
}

const schattenAnsichtHinweis = "Interne Karte: Sie lebt auf dem internen Board im Firmennetz. Hier stehen nur diese Felder, nur lesbar — Beschreibung, Kommentare und Anhänge gibt es außen nicht. Ändern lässt sie sich nur innen."

var schattenZeigenVorlage = template.Must(template.New("schatten-karte").Parse(`<!doctype html>
<html lang="de"><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="` + karteZeigenCSP + `">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Nummer}} {{.Titel}}</title>
<style>
:root{color-scheme:light dark;--fg:#1f2328;--mute:#59636e;--bg:#fff}
@media (prefers-color-scheme:dark){:root{--fg:#e6edf3;--mute:#9198a1;--bg:#0d1117}}
body{margin:0;padding:16px;font:14px/1.5 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
h1{font-size:18px;margin:4px 0 12px}.nr{color:var(--mute);font-weight:600}
dl{display:grid;grid-template-columns:max-content 1fr;gap:2px 12px;margin:0}dt{color:var(--mute)}dd{margin:0}
p{color:var(--mute)}
</style></head><body><article>
<div><span class="nr">{{.Nummer}}</span> · interne Karte, nur lesbar</div>
<h1>{{.Titel}}</h1>
<dl>
{{if .Spalte}}<dt>Spalte</dt><dd>{{.Spalte}}</dd>{{end}}
{{if .Prioritaet}}<dt>Priorität</dt><dd>{{.Prioritaet}}</dd>{{end}}
{{if .StartDatum}}<dt>Start</dt><dd>{{.StartDatum}} {{.StartZeit}}</dd>{{end}}
{{if .EndeDatum}}<dt>Fällig</dt><dd>{{.EndeDatum}} {{.EndeZeit}}</dd>{{end}}
{{if .Bearbeiter}}<dt>Bearbeiter</dt><dd>{{range $i, $b := .Bearbeiter}}{{if $i}}, {{end}}{{$b}}{{end}}</dd>{{end}}
</dl>
<p>{{.Hinweis}}</p>
</article></body></html>
`))

// schattenZeigen beantwortet karte_zeigen für „i-<nr>“ aus der Liste, die
// werkzeugKarteZeigen gelesen hat.
func schattenZeigen(w http.ResponseWriter, boardID, ref, format string, liste []wkSchatten) {
	nr := schattenNummer(ref)
	var treffer []wkSchatten
	for _, s := range liste {
		if s.Art == schatten.ArtKarte && s.nr == nr {
			treffer = append(treffer, s)
		}
	}
	switch len(treffer) {
	case 0:
		writeError(w, http.StatusNotFound, "Interne Karte i-"+strconv.Itoa(nr)+" gibt es zu diesem Board nicht (oder sie ist noch nicht abgeglichen). Interne Karten nennt board_lesen unter intern.")
		return
	case 1:
	default:
		writeError(w, http.StatusBadRequest, "Mehrere interne Karten tragen die Nummer i-"+strconv.Itoa(nr)+" — sie sind in board_lesen unter intern einzeln aufgeführt.")
		return
	}
	ansicht := wkSchattenAnsicht{Sicherheitshinweis: werkzeugFremdtextHinweis, wkSchatten: treffer[0], BoardID: boardID, Hinweis: schattenAnsichtHinweis}
	if format == "json" {
		writeJSON(w, http.StatusOK, ansicht)
		return
	}
	var puffer strings.Builder
	if err := schattenZeigenVorlage.Execute(&puffer, ansicht); err != nil {
		writeError(w, http.StatusInternalServerError, "render failed", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition")
	w.Header().Set("Content-Security-Policy", karteZeigenCSP+"; "+FrameAncestorsPolicy)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(puffer.String()))
}

// schattenLeseFehler: 500 ohne Teilantwort, wenn die Tabelle nicht lesbar ist.
func schattenLeseFehler(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusServiceUnavailable, "Interne Karten sind gerade nicht lesbar. Bitte erneut versuchen.")
		return
	}
	writeError(w, http.StatusInternalServerError, "shadow read failed", err)
}

// --- Werkzeugtexte je Seite (E-3) ----------------------------------------------

// paarKernAussen / paarKernInnen: die Kernaussage je Seite (ADR-0140 §7,
// E-3) — in der Gesamtbeschreibung und im Werkzeug boards, damit sie auch ein
// Client sieht, der nur tools/list liest.
const (
	paarKernAussen = "Board-Paar: Dies ist der externe Server. Zu jedem Board gibt es ein internes Board im Firmennetz. " +
		"Karten mit Nummern wie i-12 leben dort und sind hier nur lesbar. Neue Karten gehören nach innen, solange die Person nichts anderes sagt " +
		"(Ausnahme: eine externe Kopie, boardpaar.kopie = true; dort stehen nicht vertrauliche Karten als Spiegel mit gewöhnlicher Nummer #N, nur vertrauliche als Schatten i-<nr>, und es gilt der Hinweis des Boards)."
	paarKernInnen = "Board-Paar: Dies ist der interne Server. Zu gekoppelten Boards gibt es ein externes Board. " +
		"Was außen steht, kann jeder lesen, auch Cloud-Agenten. Neue Karten entstehen hier, also intern."
)

// werkzeugPaarText: Zusatz zur Beschreibung (OpenAPI info.description, MCP
// instructions) je Seite. Auf „aus“ leer — dann erwähnt kein Text ein Paar.
func werkzeugPaarText(seite string) string {
	switch seite {
	case BoardpaarSeiteAussen:
		return " " + paarKernAussen + " Hier stehen von internen Karten nur Nummer, Titel (bei vertraulichen „Interne Karte“, außer auf einer externen Kopie: dort der echte Titel; nicht vertrauliche Karten sind dort Spiegel mit gewöhnlicher Nummer, keine i-Nummer), " +
			"Spalte, Priorität, Termine und Bearbeiter, nur lesbar (board_lesen unter intern, karte_zeigen mit i-12, karten_suchen). " +
			"Schreibende Werkzeuge nehmen keine i-Nummer an. Lege hier nur an, wenn die Person es ausdrücklich für dieses externe Board will."
	case BoardpaarSeiteInnen:
		return " " + paarKernInnen + " Karten und Termine, die du anlegst, sind vertraulich (außen erscheint nur „Interne Karte“), " +
			"bis ein Mensch sie freigibt. karte_anlegen und termin_anlegen nehmen vertraulich: false an (nur ausdrücklich); " +
			"eine so angelegte Karte geht auf einer externen Kopie vollständig nach außen, auf einem Stufe-1-Board nur mit ihrem Titel."
	}
	return ""
}

// werkzeugPaarZusatz: Zusatz je Werkzeug und Seite (nur wo das Paar die
// Bedienung ändert).
func werkzeugPaarZusatz(seite, name string) string {
	switch seite {
	case BoardpaarSeiteAussen:
		switch name {
		case "boards":
			return " " + paarKernAussen + " Gekoppelte Boards tragen boardpaar (rolle extern, gegenstueck intern)."
		case "board_lesen":
			return " Interne Karten und Termine stehen unter intern (art, nummer i-<nr>, schattenId, titel, spalte, prioritaet, Termine, bearbeiter, vonAgent, nurLesbar) — nur lesbar, ohne Link. Ohne swimlane steht intern immer in der Antwort, auch leer; mit swimlane fehlt es, weil interne Karten keine Swimlane haben."
		case "karte_zeigen":
			return " karte darf auch i-<nr> sein (interne Karte): dann nur die lesbaren Felder, ohne Beschreibung und Kommentare."
		case "karten_suchen":
			return " Interne Karten, deren Titel oder Nummer (i-<nr>) passt, stehen unter intern — nur lesbar."
		case "karte_anlegen", "finding_anlegen", "termin_anlegen":
			return " Dies ist der externe Server: Neue Karten gehören nach innen, solange die Person nichts anderes sagt. " +
				"Auf einer externen Kopie (boardpaar.kopie = true) gilt stattdessen der Hinweis des Boards: Dort wird bei erlaubtem Bearbeiten außen angelegt."
		}
	case BoardpaarSeiteInnen:
		switch name {
		case "boards":
			return " " + paarKernInnen + " Gekoppelte Boards tragen boardpaar (rolle intern, gegenstueck extern)."
		case "karte_anlegen", "termin_anlegen":
			return " Was du hier anlegst, ist vertraulich, bis ein Mensch es freigibt; außen erscheint es nur als „Interne Karte“. " +
				"Mit vertraulich: false (nur ausdrücklich) legst du es offen an; dann geht es auf einer externen Kopie vollständig nach außen, auf einem Stufe-1-Board nur mit seinem Titel."
		case "finding_anlegen":
			return " Was du hier anlegst, ist vertraulich, bis ein Mensch es freigibt; außen erscheint es nur als „Interne Karte“."
		}
	}
	return ""
}

// vertraulichFeldText: die Beschreibung des Felds `vertraulich` in karte_anlegen
// und termin_anlegen (Karte #797, ADR-0141 W-A). Er nennt die Folge: Eine
// offene Eintragung geht ab Stufe 2b vollständig nach außen. Das Wort
// „intern“ steht nicht darin (Texte der Seite „aus“ sind davon frei).
const vertraulichFeldText = "Optional. Ohne Angabe ist, was du anlegst, vertraulich (außen nur „Interne Karte“, auf einer externen Kopie als grauer Schatten). " +
	"Nur ein ausdrückliches false macht es offen; dann geht es auf einer externen Kopie vollständig nach außen (als Spiegel mit Beschreibung und Kommentaren), auf einem Stufe-1-Board nur mit seinem Titel. " +
	"Sende false nur, wenn die Person es ausdrücklich will."

// schemaMitVertraulich: das Eingabe-Schema um das Feld `vertraulich` ergänzt.
// Bei einem Schema ohne properties-Objekt unverändert.
func schemaMitVertraulich(roh json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(roh, &m) != nil {
		return roh
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return roh
	}
	props["vertraulich"] = map[string]any{"type": "boolean", "description": vertraulichFeldText}
	aus, err := json.Marshal(m)
	if err != nil {
		return roh
	}
	return aus
}

// vertraulichErgebnisText: Beschreibung des Antwortfelds (nur innen).
const vertraulichErgebnisText = "Nur auf dem internen Board: der tatsächliche Stand des Eintrags (true = vertraulich). " +
	"Fehlt es und steht vertraulich_unbekannt: true da, konnte der Stand nicht nachgelesen werden."

// schemaErgebnisMitVertraulich: das Antwort-Schema um `vertraulich` (und das
// Ausfallzeichen `vertraulich_unbekannt`) ergänzt. Bei einem Schema ohne
// properties-Objekt unverändert.
func schemaErgebnisMitVertraulich(roh json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(roh, &m) != nil {
		return roh
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return roh
	}
	props["vertraulich"] = map[string]any{"type": "boolean", "description": vertraulichErgebnisText}
	props["vertraulich_unbekannt"] = map[string]any{"type": "boolean"}
	aus, err := json.Marshal(m)
	if err != nil {
		return roh
	}
	return aus
}

// werkzeugSchemasFuerSeite: die Body-Schemas dieser Seite. Nur innen tragen
// KarteAnlegen und TerminAnlegen das Feld `vertraulich` (W-4 gilt nur dort),
// KarteErgebnis und TerminErgebnis das Antwortfeld gleichen Namens;
// sonst dieselbe Abbildung, unverändert (Seite „aus“: kein Byte anders).
func werkzeugSchemasFuerSeite(schemas map[string]json.RawMessage, seite string) map[string]json.RawMessage {
	if seite != BoardpaarSeiteInnen {
		return schemas
	}
	aus := make(map[string]json.RawMessage, len(schemas))
	for k, v := range schemas {
		aus[k] = v
	}
	for _, name := range []string{"KarteAnlegen", "TerminAnlegen"} {
		if v, da := aus[name]; da {
			aus[name] = schemaMitVertraulich(v)
		}
	}
	for _, name := range []string{"KarteErgebnis", "TerminErgebnis"} {
		if v, da := aus[name]; da {
			aus[name] = schemaErgebnisMitVertraulich(v)
		}
	}
	return aus
}

// mcpSchemaFuerSeite: das inputSchema eines MCP-Werkzeugs auf dieser Seite.
func mcpSchemaFuerSeite(name string, roh json.RawMessage, seite string) json.RawMessage {
	if seite == BoardpaarSeiteInnen && (name == "karte_anlegen" || name == "termin_anlegen") {
		return schemaMitVertraulich(roh)
	}
	return roh
}

// katalogFuerSeite: der Katalog mit den Paar-Texten dieser Seite. Auf „aus“
// derselbe Katalog (kein Byte anders).
func katalogFuerSeite(katalog []werkzeugEintrag, seite string) []werkzeugEintrag {
	if werkzeugPaarText(seite) == "" {
		return katalog
	}
	aus := make([]werkzeugEintrag, len(katalog))
	copy(aus, katalog)
	for i := range aus {
		aus[i].Beschreibung += werkzeugPaarZusatz(seite, aus[i].Name)
	}
	return aus
}

// verbotenMeldungFuer: die 403-Meldung der Werkzeug-Wege. Die Rolle intern
// hat weder boards noch Werkzeuge (K-4, W-1, #808): Der Standardtext schickte
// sie zu einem Werkzeug, das sie nicht hat. Der erreichbare Fall ist ein
// /werkzeuge/…-Pfad, den agentDarf ablehnt (agent.go); die Stellen in
// internZugang, werkzeugKartenSuchen und ladeWkBoard sind vorab abgefangen
// und nur Absicherung. Der Text ist deshalb neutral gehalten (das eigene Board
// kann in ladeWkBoard auch gelöscht sein).
func verbotenMeldungFuer(p *AgentPrincipal) string {
	if istInternRolle(p) {
		return "nicht erlaubt: Dieser Zugang kann nur die Wege unter /agent/intern/<board>/ nutzen."
	}
	return werkzeugVerbotenMeldung
}

// sucheEingrenzen: der Kürzungshinweis der Suche. Für die Rolle intern ist
// boardId fest gesetzt, „Mit boardId eingrenzen“ wäre irreführend (K-4).
func sucheEingrenzen(p *AgentPrincipal) string {
	if istInternRolle(p) {
		return "Mit einem genaueren Suchbegriff eingrenzen."
	}
	return "Mit boardId oder einem genaueren Suchbegriff eingrenzen."
}
