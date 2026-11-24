package api_test

// Karte #752 (ADR-0139 Stufe 1, §6 Drift-Schutz): Der Werkzeug-Katalog
// erzeugt die OpenAPI-Beschreibung und die Routen. Damit der Schutz nicht zum
// Spiegel-Test wird (K-5), stehen hier zwei Quellen von Hand, unabhängig vom
// Katalog und von der Positivliste werkzeugDarf:
//
//  1. die Zusageliste: welche Werkzeuge es gibt, unter welcher Route, und
//     welche davon Open WebUI (delegierendes Token) sieht;
//  2. die Rollen-Matrix: jedes Werkzeug echt aufgerufen, je Token-Art, Rolle,
//     Board-Beziehung und Owner der Person, mit dem erwarteten Status.
//
// Dazu der eingecheckte Abzug werkzeuge_openapi.json. Er zählt nicht als
// Schutz (ADR-0139 §6), macht aber jede Änderung der Beschreibung im Diff
// sichtbar — und tests/werkzeuge-openapi-finding-1558.test.js prüft an ihm die
// Finding-Wortlisten gegen src/finding.js. Neu schreiben:
//
//	ABOARD_WERKZEUG_ABZUG_SCHREIBEN=1 go test ./api/ -run TestAgent752_Abzug

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// zusage752: die von Hand geschriebene Liste der zugesagten Werkzeuge.
// delegiert = Open WebUI sieht es (für jedes neue Werkzeug einzeln mit dem Partnerprojekt
// und dem Maintainer freigegeben, ADR-0139 §7). Pfade und Namen kennt Open WebUI im Team-Betrieb
// — nicht ändern (T-4).
var zusage752 = []struct {
	methode, pfad, name string
	delegiert           bool
}{
	{"GET", "/werkzeuge/boards", "boards", true},
	{"GET", "/werkzeuge/boards/{boardId}", "board_lesen", true},
	{"POST", "/werkzeuge/boards/{boardId}/karten", "karte_anlegen", true},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/kommentare", "kommentieren", true},
	{"GET", "/werkzeuge/boards/{boardId}/karten/{karte}", "karte_zeigen", true},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/verschieben", "verschieben", true},
	{"POST", "/werkzeuge/boards/{boardId}/findings", "finding_anlegen", true},
	{"POST", "/werkzeuge/boards/{boardId}/termine", "termin_anlegen", true},
	{"GET", "/werkzeuge/termine", "termine_lesen", true},
	// Karte #755 (ADR-0139 Stufe 4a) — nicht für Open WebUI freigegeben (§7).
	{"GET", "/werkzeuge/karten", "karten_suchen", false},
	{"GET", "/werkzeuge/boards/{boardId}/verlauf", "board_verlauf", false},
	{"GET", "/werkzeuge/boards/{boardId}/mitglieder", "mitglieder", false},
	{"GET", "/werkzeuge/boards/{boardId}/karten/{karte}/anhaenge", "anhang_lesen", false},
	// Karte #755, Teil 2 — Schreiben, nicht für Open WebUI freigegeben (§7);
	// kommentar_bearbeiten, uebernehmen und freigeben dort nie (§1).
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/aendern", "karte_aendern", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/checkliste", "checkliste_abhaken", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/archivieren", "archivieren", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/wiederherstellen", "wiederherstellen", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/kommentare/{kommentar}", "kommentar_bearbeiten", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/verweise", "verweis_setzen", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/verweise/entfernen", "verweis_entfernen", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/uebernehmen", "uebernehmen", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/freigeben", "freigeben", false},
	{"POST", "/werkzeuge/boards/{boardId}/termine/{termin}", "termin_aendern", false},
	{"POST", "/werkzeuge/boards/{boardId}/swimlanes", "swimlane_anlegen", false},
	{"POST", "/werkzeuge/boards/{boardId}/swimlanes/{swimlane}", "swimlane_aendern", false},
	{"POST", "/werkzeuge/boards/{boardId}/warten", "auf_antwort_warten", false},
	// Karte #757 (ADR-0139 Stufe 4b) — Löschen zweistufig und board_aendern,
	// nicht für Open WebUI (E-2: erst nach Bestätigung durch das Partnerprojekt; E-3: nie).
	{"GET", "/werkzeuge/boards/{boardId}/karten/{karte}/loeschen", "karte_loeschen_pruefen", false},
	{"POST", "/werkzeuge/boards/{boardId}/karten/{karte}/loeschen", "karte_loeschen", false},
	{"GET", "/werkzeuge/boards/{boardId}/swimlanes/{swimlane}/loeschen", "swimlane_loeschen_pruefen", false},
	{"POST", "/werkzeuge/boards/{boardId}/swimlanes/{swimlane}/loeschen", "swimlane_loeschen", false},
	{"GET", "/werkzeuge/boards/{boardId}/termine/{termin}/loeschen", "termin_loeschen_pruefen", false},
	{"POST", "/werkzeuge/boards/{boardId}/termine/{termin}/loeschen", "termin_loeschen", false},
	{"GET", "/werkzeuge/boards/{boardId}/anhaenge/{anhang}/loeschen", "anhang_loeschen_pruefen", false},
	{"POST", "/werkzeuge/boards/{boardId}/anhaenge/{anhang}/loeschen", "anhang_loeschen", false},
	{"GET", "/werkzeuge/boards/{boardId}/spalten/{spalte}/loeschen", "spalte_loeschen_pruefen", false},
	{"POST", "/werkzeuge/boards/{boardId}/spalten/{spalte}/loeschen", "spalte_loeschen", false},
	{"POST", "/werkzeuge/boards/{boardId}/aendern", "board_aendern", false},
}

// abfrage752: Query, die ein Werkzeug in der Matrix braucht (Pflicht-Parameter).
var abfrage752 = map[string]string{"karten_suchen": "?q=Karte"}

// spezWege: "METHODE pfad" → operationId aus einer Beschreibung.
func spezWege(t *testing.T, roh []byte) map[string]string {
	t.Helper()
	var spez struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(roh, &spez); err != nil {
		t.Fatalf("Beschreibung ist kein JSON: %v", err)
	}
	aus := map[string]string{}
	for pfad, ops := range spez.Paths {
		for m, o := range ops {
			aus[strings.ToUpper(m)+" "+pfad] = o.OperationID
		}
	}
	return aus
}

func TestAgent752_ZusagelisteGleichBeschreibungUndRouten(t *testing.T) {
	alle, delegiert := map[string]string{}, map[string]string{}
	for _, z := range zusage752 {
		alle[z.methode+" "+z.pfad] = z.name
		if z.delegiert {
			delegiert[z.methode+" "+z.pfad] = z.name
		}
	}
	if ist := spezWege(t, api.WerkzeugSpezifikation()); !reflect.DeepEqual(ist, alle) {
		t.Errorf("Beschreibung (Projekt/Person) %v\n≠ Zusage %v", ist, alle)
	}
	if ist := spezWege(t, api.WerkzeugSpezifikationDelegiert()); !reflect.DeepEqual(ist, delegiert) {
		t.Errorf("Beschreibung (delegierend) %v\n≠ Zusage %v", ist, delegiert)
	}
	reg := map[string]string{}
	for _, r := range api.WerkzeugRouten() {
		if r.Muster == "/openapi.json" {
			continue
		}
		reg[r.Methode+" "+r.Muster] = r.OperationID
	}
	if !reflect.DeepEqual(reg, alle) {
		t.Errorf("registrierte Routen %v\n≠ Zusage %v", reg, alle)
	}
}

// Die ausgelieferte Beschreibung hängt am Token: delegierend die gefilterte,
// sonst die vollständige. Ohne Token gibt es keine.
func TestAgent752_BeschreibungJeToken(t *testing.T) {
	e := newEnv671(t, true)
	if s, b := e.rufe(t, e.token, "", "GET", "/openapi.json", ""); s != http.StatusOK || string(b) != string(api.WerkzeugSpezifikationDelegiert()) {
		t.Fatalf("delegierend: %d", s)
	}
	for _, tok := range []string{e.projekt, e.leser} {
		if s, b := e.rufe(t, tok, "", "GET", "/openapi.json", ""); s != http.StatusOK || string(b) != string(api.WerkzeugSpezifikation()) {
			t.Fatalf("Projekt-Token: %d", s)
		}
	}
	if s, _ := e.rufe(t, e.vorschlg, "", "GET", "/openapi.json", ""); s != http.StatusForbidden {
		t.Fatalf("Vorschlags-Token: %d, erwartet 403", s)
	}
}

// Der Abzug entspricht der erzeugten Beschreibung — inhaltlich, nicht
// zeichengenau (Reihenfolge der Schlüssel zählt für Open WebUI nicht).
func TestAgent752_Abzug(t *testing.T) {
	erzeugt := api.WerkzeugSpezifikation()
	if os.Getenv("ABOARD_WERKZEUG_ABZUG_SCHREIBEN") == "1" {
		if err := os.WriteFile("werkzeuge_openapi.json", erzeugt, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("werkzeuge_openapi.json neu geschrieben")
	}
	roh, err := os.ReadFile("werkzeuge_openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(roh, &a); err != nil {
		t.Fatalf("Abzug ist kein JSON: %v", err)
	}
	if err := json.Unmarshal(erzeugt, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("werkzeuge_openapi.json weicht von der erzeugten Beschreibung ab — " +
			"neu schreiben mit ABOARD_WERKZEUG_ABZUG_SCHREIBEN=1 go test ./api/ -run TestAgent752_Abzug und den Diff lesen")
	}
}

// --- Rollen-Matrix ------------------------------------------------------------

// Zellen der Matrix: Token-Art und Rolle / Board-Beziehung. „eigen“ ist das
// Board des Tokens bzw. ein Board mit Eintrag der Person, „fremd“ eines ohne.
// „mehrere“ (Karte #754, ADR-0139 §5/§6.3): Personen-Token für eine Liste von
// Boards — „liste“ ein Board der Liste, „nicht-in-liste“ ein Board, auf dem
// die Person owner ist, das aber nicht in der Liste steht, „fremd“ eines ohne
// Eintrag der Person.
// „owner“: Personen-Token einer Person, die am Board owner ist (Token-Rolle
// editor). Werkzeuge ohne Board im Pfad (boards, termine_lesen) haben keine
// Board-Beziehung; dort sind eigen und fremd derselbe Aufruf.
var zellen752 = []string{
	"projekt-editor/eigen", "projekt-editor/fremd",
	"projekt-viewer/eigen", "projekt-viewer/fremd",
	"person-editor/eigen", "person-editor/fremd",
	"person-viewer/eigen",
	"person-owner/eigen",
	"delegiert-editor/eigen", "delegiert-editor/fremd",
	"delegiert-viewer/eigen",
	"vorschlag/eigen",
	"mehrere-editor/liste", "mehrere-editor/nicht-in-liste", "mehrere-editor/fremd",
	"mehrere-viewer/liste",
}

// Zellen, in denen die wirksame Rolle viewer ist.
var viewerZellen752 = []string{"projekt-viewer/eigen", "person-viewer/eigen", "delegiert-viewer/eigen", "mehrere-viewer/liste"}

// Zellen eines delegierenden Tokens.
var delegiertZellen752 = []string{"delegiert-editor/eigen", "delegiert-editor/fremd", "delegiert-viewer/eigen"}

const (
	ok752  = http.StatusOK
	nein52 = http.StatusForbidden
)

// matrix752: erwarteter Status je Werkzeug, in der Reihenfolge von zellen752.
// VON HAND — nie aus Katalog oder Positivliste ableiten.
var matrix752 = map[string][]int{
	//                 p-ed/eig p-ed/fr p-vw/eig p-vw/fr pe-ed/eig pe-ed/fr pe-vw/eig pe-own del-ed/eig del-ed/fr del-vw/eig vorschlag | me-ed/liste me-ed/nicht me-ed/fremd me-vw/liste
	"boards":          {ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, nein52, ok752, ok752, ok752, ok752},
	"board_lesen":     {ok752, nein52, ok752, nein52, ok752, nein52, ok752, ok752, ok752, nein52, ok752, nein52, ok752, nein52, nein52, ok752},
	"karte_zeigen":    {ok752, nein52, ok752, nein52, ok752, nein52, ok752, ok752, ok752, nein52, ok752, nein52, ok752, nein52, nein52, ok752},
	"termine_lesen":   {ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, nein52, ok752, ok752, ok752, ok752},
	"karte_anlegen":   {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, ok752, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"kommentieren":    {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, ok752, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"verschieben":     {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, ok752, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"finding_anlegen": {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, ok752, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"termin_anlegen":  {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, ok752, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	// Karte #755: Lesen; delegierend nie (nicht freigegeben).
	"karten_suchen": {ok752, ok752, ok752, ok752, ok752, ok752, ok752, ok752, nein52, nein52, nein52, nein52, ok752, ok752, ok752, ok752},
	"board_verlauf": {ok752, nein52, ok752, nein52, ok752, nein52, ok752, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, ok752},
	"mitglieder":    {ok752, nein52, ok752, nein52, ok752, nein52, ok752, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, ok752},
	"anhang_lesen":  {ok752, nein52, ok752, nein52, ok752, nein52, ok752, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, ok752},
	// Karte #755, Teil 2: Schreiben (editor), delegierend nie.
	//                      p-ed/eig p-ed/fr p-vw/eig p-vw/fr pe-ed/eig pe-ed/fr pe-vw/eig pe-own del-ed/eig del-ed/fr del-vw/eig vorschlag | me-ed/liste me-ed/nicht me-ed/fremd me-vw/liste
	"karte_aendern":        {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"checkliste_abhaken":   {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"archivieren":          {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"wiederherstellen":     {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"kommentar_bearbeiten": {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"verweis_setzen":       {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"verweis_entfernen":    {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"uebernehmen":          {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"freigeben":            {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"termin_aendern":       {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"swimlane_anlegen":     {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"swimlane_aendern":     {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	// Nur mit Person: Projekt-Tokens haben keine (ADR-0139 §1).
	"auf_antwort_warten": {nein52, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	// Karte #757: Löschen wie Schreiben (editor), auch das lesende Prüfen —
	// ein Code, den man nicht einlösen darf, hilft niemandem. Delegierend nie.
	//                           p-ed/eig p-ed/fr p-vw/eig p-vw/fr pe-ed/eig pe-ed/fr pe-vw/eig pe-own del-ed/eig del-ed/fr del-vw/eig vorschlag | me-ed/liste me-ed/nicht me-ed/fremd me-vw/liste
	"karte_loeschen_pruefen":    {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"karte_loeschen":            {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"swimlane_loeschen_pruefen": {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"swimlane_loeschen":         {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"termin_loeschen_pruefen":   {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"termin_loeschen":           {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"anhang_loeschen_pruefen":   {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	"anhang_loeschen":           {ok752, nein52, nein52, nein52, ok752, nein52, nein52, ok752, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52},
	// Board-Struktur (E-3): nur eine Person, die owner ist — Anna ist owner
	// auf b2 (pe-own), auf b1 nur editor (pe-ed/eig, me-ed/liste).
	"spalte_loeschen_pruefen": {nein52, nein52, nein52, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52, nein52, nein52, nein52, nein52, nein52},
	"spalte_loeschen":         {nein52, nein52, nein52, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52, nein52, nein52, nein52, nein52, nein52},
	"board_aendern":           {nein52, nein52, nein52, nein52, nein52, nein52, nein52, ok752, nein52, nein52, nein52, nein52, nein52, nein52, nein52, nein52},
}

// mehrereTokens754 legt für Anna (u-anna, muss existieren) die beiden
// Mehr-Board-Tokens der Matrix an: das editor-Token mit Liste nur b1 (dort
// editor), b2 (owner) bewusst NICHT in der Liste. Das viewer-Token führt b2 in
// seiner Liste — so trennt die Zelle „nicht-in-liste“ auch eine Abfrage, die
// die Liste irgendeines Tokens der Person statt der eigenen läse.
func mehrereTokens754(t *testing.T, repo *storage.AgentTokenRepo) (editor, viewer string) {
	t.Helper()
	for i, rolle := range []string{storage.AgentRolleEditor, storage.AgentRolleViewer} {
		k, _, err := repo.Anlegen(t.Context(), storage.AgentTokenAnlage{Name: fmt.Sprintf("anna-mehrere-%d", i), UserID: "u-anna",
			Role: rolle, CreatedBy: "cli", BoardIDs: [][]string{{"b1"}, {"b1", "b2"}}[i]})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			editor = k
		} else {
			viewer = k
		}
	}
	return editor, viewer
}

// katalogStufe752: Stufe je Werkzeug, aus der erzeugten Beschreibung nicht
// ablesbar — deshalb über die Methode: GET liest, alles andere schreibt. Die
// Katalog-Prüfung (werkzeugKatalogPruefen) erzwingt genau diese Kopplung.
func schreibt752(methode string) bool { return methode != http.MethodGet }

func TestAgent752_RollenMatrix(t *testing.T) {
	// Vollständigkeit: jede Zeile ein zugesagtes Werkzeug und umgekehrt.
	namen := map[string]string{}
	for _, z := range zusage752 {
		namen[z.name] = z.methode
		if _, ok := matrix752[z.name]; !ok {
			t.Errorf("%s: keine Zeile in der Rollen-Matrix — jedes Werkzeug braucht eine", z.name)
		}
	}
	for name, zeile := range matrix752 {
		if _, ok := namen[name]; !ok {
			t.Errorf("Matrix-Zeile %s ohne zugesagtes Werkzeug", name)
		}
		if len(zeile) != len(zellen752) {
			t.Fatalf("%s: %d Spalten, erwartet %d", name, len(zeile), len(zellen752))
		}
	}
	// Ein schreibendes Werkzeug ohne ausdrückliches Verbot für viewer lässt
	// den Test scheitern (ADR-0139 §6.3); ebenso ein Werkzeug, das Open WebUI
	// nicht sieht, aber mit delegierendem Token aufrufen könnte.
	spalte := map[string]int{}
	for i, z := range zellen752 {
		spalte[z] = i
	}
	for _, z := range zusage752 {
		zeile := matrix752[z.name]
		if zeile == nil {
			continue
		}
		if schreibt752(z.methode) {
			for _, c := range viewerZellen752 {
				if zeile[spalte[c]] != nein52 {
					t.Errorf("%s schreibt, aber %s ist nicht ausdrücklich verboten", z.name, c)
				}
			}
		}
		if !z.delegiert {
			for _, c := range delegiertZellen752 {
				if zeile[spalte[c]] != nein52 {
					t.Errorf("%s ist für Open WebUI nicht freigegeben, aber %s ist erlaubt", z.name, c)
				}
			}
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	e := newEnv671(t, true)
	ctx := t.Context()
	anlegen := func(a storage.AgentTokenAnlage) string {
		k, _, err := e.repo.Anlegen(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	// Personen-Tokens brauchen die Person in users (Rolle aus der ACL).
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	personEditor := anlegen(storage.AgentTokenAnlage{Name: "anna-b1", BoardID: "b1", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	personViewer := anlegen(storage.AgentTokenAnlage{Name: "anna-b1-lesen", BoardID: "b1", UserID: "u-anna", Role: storage.AgentRolleViewer, CreatedBy: "cli"})
	personOwner := anlegen(storage.AgentTokenAnlage{Name: "anna-b2", BoardID: "b2", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	mehrEditor, mehrViewer := mehrereTokens754(t, e.repo)

	// name: Name des Tokens — Karte #755: kommentar_bearbeiten braucht einen
	// eigenen Kommentar (board755: „k-<name>“).
	type zelle struct{ token, person, board, name string }
	aufruf := map[string]zelle{
		"projekt-editor/eigen":          {e.projekt, "", "b1", "projekt"},
		"projekt-editor/fremd":          {e.projekt, "", "b3", "projekt"},
		"projekt-viewer/eigen":          {e.leser, "", "b1", "leser"},
		"projekt-viewer/fremd":          {e.leser, "", "b3", "leser"},
		"person-editor/eigen":           {personEditor, "", "b1", "anna-b1"},
		"person-editor/fremd":           {personEditor, "", "b3", "anna-b1"},
		"person-viewer/eigen":           {personViewer, "", "b1", "anna-b1-lesen"},
		"person-owner/eigen":            {personOwner, "", "b2", "anna-b2"},
		"delegiert-editor/eigen":        {e.token, "anna@firma.de", "b1", "open-webui"},
		"delegiert-editor/fremd":        {e.token, "anna@firma.de", "b3", "open-webui"},
		"delegiert-viewer/eigen":        {e.token, "cora@extern.de", "b1", "open-webui"},
		"vorschlag/eigen":               {e.vorschlg, "", "b1", "recherche"},
		"mehrere-editor/liste":          {mehrEditor, "", "b1", "anna-mehrere-0"},
		"mehrere-editor/nicht-in-liste": {mehrEditor, "", "b2", "anna-mehrere-0"},
		"mehrere-editor/fremd":          {mehrEditor, "", "b3", "anna-mehrere-0"},
		"mehrere-viewer/liste":          {mehrViewer, "", "b1", "anna-mehrere-1"},
	}
	koerper := map[string]string{
		"karte_anlegen":   `{"titel":"Aus der Rollen-Matrix"}`,
		"kommentieren":    `{"text":"Rollen-Matrix"}`,
		"verschieben":     `{"spalte":"Ready"}`,
		"finding_anlegen": `{"titel":"Befund aus der Rollen-Matrix","typ":"K","prioritaet":"P2"}`,
		"termin_anlegen":  `{"titel":"Termin aus der Rollen-Matrix","datum":"2026-10-08"}`,
	}
	stand := e.stand755(t)
	for name, k := range koerper755 {
		koerper[name] = strings.ReplaceAll(k, "STAND", stand)
	}
	var namenSortiert []string
	for n := range namen {
		namenSortiert = append(namenSortiert, n)
	}
	sort.Strings(namenSortiert)
	pfad := map[string]string{}
	for _, z := range zusage752 {
		pfad[z.name] = z.pfad
	}
	for _, name := range namenSortiert {
		for i, c := range zellen752 {
			a := aufruf[c]
			// Karte #755: jede Zelle vom selben Ausgangsstand (Schreib-Werkzeuge
			// ändern ihn).
			e.zuruecksetzen755(t)
			p := pfad755(pfad[name], a.board, a.name) + abfrage752[name]
			// Karte #757: der Code aus dem Prüf-Werkzeug mit demselben Zugang.
			body := e.mitCode757(t, a.token, a.person, pfad[name], a.board, a.name, koerper[name])
			s, b := e.rufe(t, a.token, a.person, namen[name], p, body)
			if soll := matrix752[name][i]; s != soll {
				t.Errorf("%s · %s (%s %s): %d, erwartet %d — %s", name, c, namen[name], p, s, soll, kurz752(b))
			}
		}
	}
}

func kurz752(b []byte) string {
	s := string(b)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return fmt.Sprintf("%q", s)
}
