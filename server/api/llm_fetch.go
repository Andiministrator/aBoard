package api

// llm_fetch.go — Autorisierung für das Lese-Werkzeug `fetch-related-card`
// (#428, ADR-0120).
//
// Diese Datei ist der Sicherheitskern der Karte. Ein Werkzeug, das auf Zuruf
// eine Karte nach id liefert, ist ein Exfiltrations-Kanal: Ein Modell — oder
// ein Text, den jemand in eine Karte geschrieben hat — kann sich sonst über
// gezielte Anfragen durch ein ganzes Board hangeln, und der Nutzer sieht
// davon nur die fertige Antwort.
//
// Zwei Bedingungen, beide notwendig, beide fail-closed (ADR-0120 §1):
//
//	(b) genau eine Verknüpfungs-Ebene zur Quell-Karte, in BEIDEN Richtungen
//	    geprüft (die Symmetrie aus ADR-0032 ist Konvention, kein Zwang)
//	(a) mindestens `viewer` auf dem Board, in dem die Karte GEFUNDEN wurde —
//	    nicht auf dem Board aus der URL. Dieselbe Fehlerklasse wie F-391
//	    ("die Berechtigung wird nicht an dem Board geprüft, an dem sie
//	    zählt"); F-391 selbst war eine andere Ausprägung davon, nämlich eine
//	    ACL-Lücke im Legacy-Bypass — nicht, wie hier zuerst stand, eine
//	    Verwechslung von URL-Board und Karten-Standort.
//
// Was hier ausdrücklich NICHT passiert: eine Suche über alle Boards. Der
// Suchraum ist der Board-Snapshot des Turns. Schon die Auskunft „diese id
// gibt es hier nicht, aber anderswo" wäre der Kanal, den (a) verschliesst.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// fetchOutcome ist das Ergebnis einer einzelnen Nachlade-Anfrage. Genau eines
// von Content/Reason ist belegt. Reason geht als Werkzeug-Antwort an das
// Modell zurück — benannt, nicht pauschal: „nicht verknüpft" ist eine andere
// Auskunft als „kein Zugriff", und ein Modell kann mit der ersten seinen
// nächsten Versuch korrigieren (dieselbe Überlegung wie bei den
// Ablehnungsgründen in ADR-0067/F-451).
type fetchOutcome struct {
	OK        bool
	CardID    string
	DisplayID int
	Title     string
	Content   string
	Reason    string
}

// Label liefert die Kurzbezeichnung der Karte für Audit und Oberfläche
// ("#123" bzw. die id, wenn die Karte keine displayId trägt).
func (o fetchOutcome) Label() string {
	if o.DisplayID > 0 {
		return fmt.Sprintf("#%d", o.DisplayID)
	}
	return o.CardID
}

// resolveFetch beantwortet eine Nachlade-Anfrage des Modells.
//
// snapshot ist der Karten-Index des Turns (llmCardContext.CardsByID) —
// bewusst nicht ein frisch gelesenes Board: Zwei Fetches desselben Turns
// sollen gegen denselben Stand gelten.
//
// standortBoardID ist das Board, aus dem der Snapshot stammt, und damit der
// Standort jeder darin gefundenen Karte. Heute ist das immer das Board aus
// der URL, weil Verweise board-lokal sind (siehe loadCardForLLM). Der
// Parameter existiert trotzdem eigenständig: Sobald #303 Cross-Board-Verweise
// nachliefert, ist er die Stelle, an der der echte Standort einzusetzen ist —
// und die ACL-Prüfung hängt dann schon am richtigen Wert.
func (a *LLMSendAPI) resolveFetch(
	ctx context.Context,
	email string,
	standortBoardID string,
	sourceCard map[string]any,
	snapshot map[string]map[string]any,
	targetID string,
	includeFields []string,
) fetchOutcome {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return fetchOutcome{Reason: "cardId ist leer"}
	}
	out := fetchOutcome{CardID: targetID}

	sourceID, _ := sourceCard["id"].(string)
	if targetID == sourceID {
		out.Reason = "das ist die Karte, um die es ohnehin geht — ihr Inhalt steht bereits im Kontext"
		return out
	}

	target, gefunden := snapshot[targetID]
	if !gefunden {
		// Bewusst dieselbe Auskunft wie bei „nicht verknüpft": Die
		// Unterscheidung „existiert nicht" vs. „existiert, aber nicht
		// verknüpft" wäre ein Orakel über den Kartenbestand.
		out.Reason = "keine mit dieser Karte verknüpfte Karte mit dieser cardId"
		return out
	}

	// (b) One-Hop — beide Richtungen.
	if !istEinHopVerknuepft(sourceCard, target) {
		out.Reason = "keine mit dieser Karte verknüpfte Karte mit dieser cardId"
		return out
	}

	// (a) Standort-ACL. Ohne ACL-Repo gibt es keine Standort-Prüfung, also
	// auch kein Nachladen — der Fetch wird in dem Fall gar nicht erst
	// angeboten (siehe fetchVerfuegbar), diese Zeile ist der Riegel dahinter.
	if a.acl == nil {
		out.Reason = "Nachladen ist auf diesem Server nicht verfügbar"
		return out
	}
	rolle, gefundenACL, err := a.acl.GetRole(ctx, standortBoardID, email)
	if err != nil {
		out.Reason = "Berechtigung konnte nicht geprüft werden"
		return out
	}
	if !gefundenACL || roleRank[rolle] < roleRank[storage.RoleViewer] {
		out.Reason = "kein Zugriff auf das Board, auf dem diese Karte liegt"
		return out
	}

	out.OK = true
	out.Title, _ = target["title"].(string)
	if v, ok := target["displayId"].(float64); ok {
		out.DisplayID = int(v)
	}
	out.Content = llm.BuildFetchedCard(target, includeFields)
	return out
}

// istEinHopVerknuepft prüft die Verknüpfung in beiden Richtungen.
//
// Warum beide: ADR-0032 verlangt symmetrische Verweise, aber das ist eine
// Konvention über Daten, die auch von aussen kommen können (Import,
// Op-Inbox, fremde Clients). Sich auf die eigene Seite zu verlassen wäre
// eine Annahme über fremde Daten — und der Fehler ginge in die falsche
// Richtung: Eine legitime, nur einseitig eingetragene Verknüpfung würde
// abgelehnt.
func istEinHopVerknuepft(source, target map[string]any) bool {
	sourceID, _ := source["id"].(string)
	targetID, _ := target["id"].(string)
	if sourceID == "" || targetID == "" {
		return false
	}
	return hatLinkAuf(source, targetID) || hatLinkAuf(target, sourceID)
}

// hatLinkAuf sagt, ob card einen links-Eintrag mit targetId == id trägt.
// Der Verweis-TYP spielt keine Rolle: blocks, depends-on und related sind
// alle „eine Ebene weit". Eine Auswahl nach Typ wäre eine zusätzliche Regel
// ohne Sicherheitsgewinn.
func hatLinkAuf(card map[string]any, id string) bool {
	links, ok := card["links"].([]any)
	if !ok {
		return false
	}
	for _, l := range links {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := lm["targetId"].(string); t == id {
			return true
		}
	}
	return false
}

// fetchVerfuegbar entscheidet, ob das Lese-Werkzeug diesem Send überhaupt
// mitgegeben wird (ADR-0120 §4).
//
// Zwei Sperren:
//
//   - Ohne ACL-Repo keine Standort-Prüfung → kein Nachladen.
//   - Mit gesetztem userPromptOverride hat der Nutzer den PII-Filter benutzt
//     (ADR-0097): Er hat im Browser gesehen und redigiert, was rausgeht. Eine
//     Karte, die der Server nachlädt, hat diesen Weg strukturell nicht
//     genommen. Statt den Filter in Go nachzubauen — zwei Regelwerke, die
//     auseinanderlaufen — schliessen sich beide Wege aus. Die Oberfläche
//     nennt den Grund an der Checkbox.
func (a *LLMSendAPI) fetchVerfuegbar(angefordert bool, userPromptOverride string, piiFilterActive bool) bool {
	if !angefordert {
		return false
	}
	if a.acl == nil {
		return false
	}
	// Der Client meldet ausdrücklich, ob der PII-Filter aktiv war.
	//
	// Diese Prüfung stand zuerst NUR auf `userPromptOverride != ""` — und das
	// war die falsche Frage. Der Override entsteht erst, wenn der Filter
	// Treffer hatte UND der Nutzer das Vorschau-Modal durchlaufen ist. Bei
	// aktivem Filter ohne Treffer (der Normalfall bei sauberen Karten, und
	// „always" ist der Default-Modus) war er leer, und das Nachladen lief
	// trotzdem — obwohl ADR-0120 §4 das Gegenteil zusagt. Der Stellvertreter
	// beantwortete „wurde redigiert?", zugesagt ist „ist der Filter aktiv?".
	//
	// Beide Signale sperren jetzt, nicht nur eines: Ein alter Client, der das
	// neue Feld nicht kennt, verliert dadurch nichts, was er vorher hatte.
	if piiFilterActive {
		return false
	}
	// „Gesetzt" heisst hier exakt dasselbe wie im Handler: nicht der
	// Leerstring. NICHT getrimmt — der Handler entscheidet mit
	// `userPrompt := req.UserPromptOverride; if userPrompt == ""` darüber,
	// ob überhaupt noch ein Prompt gebaut wird. Ein Override aus reinem
	// Whitespace gilt dort als gesetzt (der Nutzer hat im PII-Modal alles
	// wegredigiert), und diese Prüfung muss dieselbe Grenze ziehen. Zwei
	// Schreibweisen derselben Frage driften.
	if userPromptOverride != "" {
		return false
	}
	return true
}
