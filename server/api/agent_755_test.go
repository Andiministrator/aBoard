package api_test

// Karte #755 (ADR-0139 Stufe 4a, Teil 1) — Nacharbeit zum Sonnet-Kritiker:
// Hinweis zuerst (K-2), Zahl-Argument über MCP (K-1), Verlauf ohne
// Freigabe-Einträge für Nicht-owner und ohne archivierte Karten (K-3, K-4).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgent755_SicherheitshinweisZuerst(t *testing.T) {
	e := newEnv671(t, true)
	for _, pfad := range []string{"/werkzeuge/karten?q=Karte", "/werkzeuge/boards/b1/verlauf", "/werkzeuge/boards/b1/mitglieder",
		"/werkzeuge/boards/b1/karten/1/anhaenge"} {
		s, b := e.rufe(t, e.projekt, "", "GET", pfad, "")
		if s != http.StatusOK {
			t.Fatalf("%s: %d %s", pfad, s, b)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(b)), `{"sicherheitshinweis":`) {
			t.Errorf("%s: Hinweis nicht als erstes Feld: %.120s", pfad, b)
		}
	}
}

func TestAgent755_MCPZahlArgument(t *testing.T) {
	e := newEnv671(t, true)
	for _, args := range []string{`{"boardId":"b1","anzahl":5}`, `{"boardId":"b1","anzahl":"5"}`} {
		s, m := e.mcpWerkzeug(t, e.projekt, "", "board_verlauf", args)
		fehler, text, _ := ergebnis753(t, m)
		if s != http.StatusOK || fehler {
			t.Errorf("%s: %d isError=%v %q", args, s, fehler, text)
		}
	}
	// Eine ungültige Zahl prüft der Handler wie bei OpenAPI.
	if _, m := e.mcpWerkzeug(t, e.projekt, "", "board_verlauf", `{"boardId":"b1","anzahl":500}`); func() bool { f, _, _ := ergebnis753(t, m); return !f }() {
		t.Errorf("anzahl 500 angenommen")
	}
}

func TestAgent755_VerlaufOhneFreigabenUndArchiv(t *testing.T) {
	e := newEnv671(t, true)
	ctx := context.Background()
	akt := storage.NewActivityRepo(e.db)
	if err := akt.Record(ctx, "b1", "admin@firma.de", "acl-set", "role", "", "editor", "bert@firma.de"); err != nil {
		t.Fatal(err)
	}
	if err := akt.RecordOps(ctx, "b1", "anna@firma.de", []storage.OpActivityEntry{
		{CardID: "k7", OpType: "card.field"},   // archivierte Swimlane
		{CardID: "k1", OpType: "card.field"},   // sichtbar
		{CardID: "weg", OpType: "card.field"},  // gibt es nicht mehr
		{CardID: "weg", OpType: "card.delete"}, // das Löschen bleibt sichtbar
	}); err != nil {
		t.Fatal(err)
	}
	s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/verlauf", "")
	text := string(b)
	if s != http.StatusOK || strings.Contains(text, "acl-set") || strings.Contains(text, "@") {
		t.Fatalf("Projekt-Token sieht Freigaben oder Adressen: %d %s", s, b)
	}
	if n := strings.Count(text, `"art":"card.field"`); n != 1 {
		t.Errorf("%d card.field-Einträge, erwartet 1 (nur k1): %s", n, b)
	}
	if !strings.Contains(text, `"art":"card.delete"`) {
		t.Errorf("Löschen fehlt: %s", b)
	}
	// Owner-Person (anna ist owner von b2) sieht Freigaben mit Adresse.
	if err := akt.Record(ctx, "b2", "admin@firma.de", "acl-set", "role", "", "editor", "bert@firma.de"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	k, _, err := e.repo.Anlegen(ctx, storage.AgentTokenAnlage{Name: "anna-b2", BoardID: "b2", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if s, b := e.rufe(t, k, "", "GET", "/werkzeuge/boards/b2/verlauf", ""); s != http.StatusOK || !strings.Contains(string(b), "acl-set") || !strings.Contains(string(b), "admin@firma.de") {
		t.Errorf("owner-Person: %d %s", s, b)
	}
}

// Verify #755, V-1/V-2: Die Anhang-Liste ist auf 100 begrenzt (gekuerzt),
// gewählt werden kann trotzdem jeder Anhang der Karte.
func TestAgent755_AnhangListeBegrenztAuswahlVoll(t *testing.T) {
	e := newEnv671(t, true)
	b, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	var anh []string
	for i := 0; i < 101; i++ {
		anh = append(anh, fmt.Sprintf(`{"id":"a%03d","filename":"f%d.bin","mime":"application/octet-stream","size":1}`, i, i))
	}
	stand := strings.Replace(string(b.StateJSON), `"title":"Offene Karte",`, `"title":"Offene Karte","attachments":[`+strings.Join(anh, ",")+`],`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, stand); err != nil {
		t.Fatal(err)
	}
	s, body := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1/anhaenge", "")
	if s != http.StatusOK || !strings.Contains(string(body), `"gekuerzt":true`) || strings.Count(string(body), `"dateiname"`) != 100 {
		t.Fatalf("Liste: %d, %d Einträge, %.200s", s, strings.Count(string(body), `"dateiname"`), body)
	}
	if s, body := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1/anhaenge?anhang=a100", ""); s != http.StatusOK || !strings.Contains(string(body), `"id":"a100"`) {
		t.Errorf("Anhang 101 nicht wählbar: %d %.200s", s, body)
	}
}

// Verify #755, V-3: Einträge ohne Karte bleiben im Verlauf.
func TestAgent755_VerlaufOhneKarteSichtbar(t *testing.T) {
	e := newEnv671(t, true)
	if err := storage.NewActivityRepo(e.db).Record(context.Background(), "b1", "agent:x", "board-updated", "title", "alt", "neu", ""); err != nil {
		t.Fatal(err)
	}
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/verlauf", ""); s != http.StatusOK || !strings.Contains(string(b), `"art":"board-updated"`) {
		t.Errorf("%d %s", s, b)
	}
}
