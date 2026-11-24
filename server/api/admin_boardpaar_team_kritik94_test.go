package api_test

// Sprint-94-Kritik S-2 und Verify F-V1/F-V2: Die Folge von boardpaar.team
// wirkt auf gekoppelten UND außen entkoppelten Kopien. Seit F-2268 entzieht
// nur PUT /admin/boardpaar/team mit Bestätigung; der allgemeine Konfig-Weg
// (PUT/DELETE /admin/config/boardpaar.team) lehnt einen Entzug mit 409 ab.

import (
	"context"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaarKritik94_TeamAustrittUeberAdminWeg(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	acl := storage.NewAclRepo(e.db)
	team := []string{"anna@team.example", "bernd@team.example"}
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.team", "admin@firma.de",
		map[string]any{"value": "anna@team.example, bernd@team.example"}); code != 200 {
		t.Fatalf("Team setzen: %d %s", code, raw)
	}
	frei := true // #836: freigegeben, damit „an“ das Team zum Bearbeiter macht
	if _, err := e.paar.KopieAnlegenMit(ctx, kopie831ID, "Kopie", kopie831Basis, "abe:test", team, &frei); err != nil {
		t.Fatal(err)
	}
	entkoppelt := "0b8f3a52-6c1e-4f7a-9d2b-3e4c5a6b7c8e"
	if _, err := e.paar.KopieAnlegen(ctx, entkoppelt, "Alt", kopie831Basis, "abe:test", team); err != nil {
		t.Fatal(err)
	}
	if err := e.paar.AussenEntkoppeln(ctx, entkoppelt); err != nil {
		t.Fatal(err)
	}
	rolle := func(board, wer string) string {
		t.Helper()
		ro, ok, err := acl.GetRole(ctx, board, wer)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return ro
	}
	for _, b := range []string{kopie831ID, entkoppelt} {
		if rolle(b, "bernd@team.example") != storage.RoleViewer {
			t.Fatalf("Vorbedingung %s: Bernd ist Leser", b)
		}
	}

	// F-2276 (Sitzungs-Kritik N-10): Rückweg an → das Team wird auf der
	// gekoppelten Kopie editor; auch diese Rolle muss der Austritt entziehen.
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.rueckweg", "admin@firma.de",
		map[string]any{"value": "an"}); code != 200 {
		t.Fatalf("Rückweg an: %d %s", code, raw)
	}
	if ro := rolle(kopie831ID, "bernd@team.example"); ro != storage.RoleEditor {
		t.Fatalf("Vorbedingung: Bernd ist editor der gekoppelten Kopie, hat %q", ro)
	}

	// F-2268: Der allgemeine Konfig-Weg entzieht nicht — 409, nichts geändert.
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.team", "admin@firma.de",
		map[string]any{"value": "anna@team.example"}); code != http.StatusConflict {
		t.Fatalf("allgemeiner Weg mit Entzug: %d %s, erwartet 409", code, raw)
	}
	// Der Team-Weg ohne Bestätigung: Vorschau, nichts geändert.
	bisher := "anna@team.example, bernd@team.example"
	code, body, raw := e.do(t, http.MethodPut, "/admin/boardpaar/team", "admin@firma.de",
		map[string]any{"team": "anna@team.example", "bisher": bisher})
	if code != http.StatusConflict || body["bestaetigung_noetig"] != true || body["zeilen"] != float64(2) {
		t.Fatalf("Vorschau: %d %s, erwartet 409 mit bestaetigung_noetig und zeilen=2", code, raw)
	}
	if weg, _ := body["weg"].([]any); len(weg) != 1 || weg[0] != "bernd@team.example" {
		t.Fatalf("Vorschau weg: %v", body["weg"])
	}
	if ro := rolle(kopie831ID, "bernd@team.example"); ro != storage.RoleEditor {
		t.Fatalf("Vorschau hat entzogen: %q", ro)
	}
	// Veraltet: bisher stimmt nicht mehr → 409 veraltet, auch mit Bestätigung.
	if code, body, raw := e.do(t, http.MethodPut, "/admin/boardpaar/team", "admin@firma.de",
		map[string]any{"team": "anna@team.example", "bisher": "anna@team.example", "bestaetigt": true}); code != http.StatusConflict || body["veraltet"] != true {
		t.Fatalf("veraltet: %d %s", code, raw)
	}
	// Ohne bisher → 400.
	if code, _, raw := e.do(t, http.MethodPut, "/admin/boardpaar/team", "admin@firma.de",
		map[string]any{"team": "anna@team.example", "bestaetigt": true}); code != http.StatusBadRequest {
		t.Fatalf("ohne bisher: %d %s", code, raw)
	}
	if ro := rolle(kopie831ID, "bernd@team.example"); ro != storage.RoleEditor {
		t.Fatalf("abgelehnte Aufrufe haben entzogen: %q", ro)
	}
	// Bestätigt: entzogen, Zahl in der Antwort.
	if code, body, raw := e.do(t, http.MethodPut, "/admin/boardpaar/team", "admin@firma.de",
		map[string]any{"team": "anna@team.example", "bisher": bisher, "bestaetigt": true}); code != 200 || body["zeilen"] != float64(2) {
		t.Fatalf("Team ändern bestätigt: %d %s", code, raw)
	}
	for _, b := range []string{kopie831ID, entkoppelt} {
		if ro := rolle(b, "bernd@team.example"); ro != "" {
			t.Errorf("%s: Bernd behält %q nach dem Austritt", b, ro)
		}
	}
	if ro := rolle(kopie831ID, "anna@team.example"); ro != storage.RoleEditor {
		t.Errorf("gekoppelte Kopie: Anna bleibt editor, hat %q", ro)
	}
	if ro := rolle(entkoppelt, "anna@team.example"); ro != storage.RoleViewer {
		t.Errorf("entkoppelte Kopie: Anna bleibt Leserin, hat %q", ro)
	}
	// Ein Team ohne Entzug geht weiter über den allgemeinen Weg.
	if code, _, raw := e.do(t, http.MethodPut, "/admin/config/boardpaar.team", "admin@firma.de",
		map[string]any{"value": "anna@team.example, carla@team.example"}); code != 200 {
		t.Fatalf("allgemeiner Weg ohne Entzug: %d %s", code, raw)
	}
	// DELETE über den allgemeinen Weg entzöge Anna — 409.
	if code, _, raw := e.do(t, http.MethodDelete, "/admin/config/boardpaar.team", "admin@firma.de", nil); code != http.StatusConflict {
		t.Fatalf("Team löschen ohne Bestätigung: %d %s, erwartet 409", code, raw)
	}
	if code, _, raw := e.do(t, http.MethodPut, "/admin/boardpaar/team", "admin@firma.de",
		map[string]any{"team": "", "bisher": "anna@team.example, carla@team.example", "bestaetigt": true}); code != 200 {
		t.Fatalf("Team leeren bestätigt: %d %s", code, raw)
	}
	for _, b := range []string{kopie831ID, entkoppelt} {
		if ro := rolle(b, "anna@team.example"); ro != "" {
			t.Errorf("%s: Anna behält %q nach dem Leeren des Teams", b, ro)
		}
	}
}

// F-2268: Ein Lesefehler von boardpaar.team ist kein leeres Team — die Liste
// antwortet mit 500 statt still team: "".
func TestBoardpaarKritik94_TeamLesefehlerNichtLeer(t *testing.T) {
	e := newKopieLoeschEnv(t)
	// Geheimwert ohne Schlüssel im Repo: Get scheitert.
	if _, err := e.db.Exec(`INSERT INTO server_config (key, value_plain, value_encrypted, is_secret, updated_at)
		VALUES ('boardpaar.team', NULL, 'xx', 1, '2026-10-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if code, body, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil); code != http.StatusInternalServerError {
		t.Fatalf("Liste mit unlesbarem Team: %d %v %s, erwartet 500", code, body["team"], raw)
	}
}
