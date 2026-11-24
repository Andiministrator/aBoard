package main

// Karte #799: CLI `agent-token create` — --anbieter/--beleg sind auf dem
// externen Server (boardpaar.seite = aussen) Pflicht, sonst optional.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentTokenCLI_Herkunft799(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	setzeSeite := func(seite string) {
		t.Helper()
		if err := storage.NewConfigRepo(h, nil, uuidString).Set(ctx, api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runAgentTokenMit(append(args, "--db", db), &out)
		return out.String(), err
	}

	// aus: ohne Angaben angelegt.
	if out, err := lauf("create", "--name", "cli-aus", "--board", "b1"); err != nil {
		t.Fatalf("aus: %v %s", err, out)
	}
	setzeSeite(api.BoardpaarSeiteAussen)
	if out, err := lauf("create", "--name", "cli-fehlt", "--board", "b1"); err == nil || !strings.Contains(err.Error(), "anbieter") || !strings.Contains(err.Error(), "beleg") {
		t.Fatalf("außen ohne Angaben: %v %s", err, out)
	}
	if _, err := lauf("create", "--name", "cli-halb", "--board", "b1", "--anbieter", "A"); err == nil {
		t.Fatal("außen mit nur --anbieter angelegt")
	}
	if _, err := lauf("create", "--name", "cli-lang", "--board", "b1", "--anbieter", strings.Repeat("a", 81), "--beleg", "b"); err == nil {
		t.Fatal("zu langer Anbieter angelegt")
	}
	if _, err := lauf("create", "--name", "cli-steuer", "--board", "b1", "--anbieter", "A", "--beleg", "b\x07"); err == nil {
		t.Fatal("Beleg mit Steuerzeichen angelegt")
	}
	// Delegierend (eigener Anlage-Weg der CLI) ist ebenso betroffen.
	if _, err := lauf("create", "--name", "cli-deleg", "--delegierend"); err == nil {
		t.Fatal("delegierend außen ohne Angaben angelegt")
	}
	if out, err := lauf("create", "--name", "cli-ok", "--board", "b1", "--anbieter", "Beispiel-Anbieter, Geschäftstarif", "--beleg", "vertraege/vertrag.md"); err != nil {
		t.Fatalf("mit Angaben: %v %s", err, out)
	}
	if out, err := lauf("create", "--name", "cli-deleg-ok", "--delegierend", "--anbieter", "A", "--beleg", "B"); err != nil {
		t.Fatalf("delegierend mit Angaben: %v %s", err, out)
	}
	out, err := lauf("list", "--json")
	if err != nil || !strings.Contains(out, `"anbieter": "Beispiel-Anbieter, Geschäftstarif"`) || !strings.Contains(out, `"beleg": "vertraege/vertrag.md"`) {
		t.Fatalf("list --json: %v %s", err, out)
	}
	if strings.Contains(out, "cli-fehlt") || strings.Contains(out, "cli-halb") {
		t.Fatalf("abgelehnte Tokens stehen in der Liste: %s", out)
	}
	if out, _ := lauf("list"); !strings.Contains(out, "ANBIETER") || !strings.Contains(out, "vertrag.md") {
		t.Fatalf("Tabelle: %s", out)
	}
	_ = h.Close()
}

// Kritiker K-1: unsichtbare Zeichen erfüllen die Pflicht nicht (auch nicht
// innen: abgelehnt, nicht gespeichert).
func TestAgentTokenCLI_Herkunft799_Unsichtbar(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewConfigRepo(h, nil, uuidString).Set(ctx, api.KonfigBoardpaarSeite, api.BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for fall, v := range map[string]string{"ZWSP": "\u200b", "Braille leer": "\u2800", "Hangul-Füller": "\u3164",
		"Bidi-Umkehr": "A\u202eB", "nur Unsichtbares+Leerraum": " \u200b "} {
		for feld, paar := range map[string][2]string{"anbieter": {v, "b"}, "beleg": {"A", v}} {
			var out bytes.Buffer
			n++ // eigener Name je Fall: ein Namenskonflikt dürfte keine Ablehnung vortäuschen
			err := runAgentTokenMit([]string{"create", "--name", fmt.Sprintf("cli-unsichtbar-%d", n), "--board", "b1",
				"--anbieter", paar[0], "--beleg", paar[1], "--db", db}, &out)
			if err == nil {
				t.Errorf("%s/%s: angelegt statt abgelehnt", fall, feld)
			}
		}
	}
	var anzahl int
	if err := h.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&anzahl); err != nil || anzahl != 0 {
		t.Errorf("abgelehnte Versuche haben Tokens angelegt: n=%d err=%v", anzahl, err)
	}
}

// Kritiker K-2: Ist server_config nicht lesbar, bricht die Anlage ab (fail-closed),
// statt die Pflicht zu erlassen; angelegt wird nichts.
func TestAgentTokenCLI_Herkunft799_SeiteNichtLesbar(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`DROP TABLE server_config`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = runAgentTokenMit([]string{"create", "--name", "cli-kaputt", "--board", "b1", "--anbieter", "A", "--beleg", "B", "--db", db}, &out)
	if err == nil || !strings.Contains(err.Error(), "nicht lesbar") {
		t.Fatalf("bei unlesbarer Seite: err=%v out=%s", err, out.String())
	}
	var n int
	if err := h.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&n); err != nil || n != 0 {
		t.Errorf("trotz Abbruch angelegt: n=%d err=%v", n, err)
	}
}

// Kritiker K-5: `list` markiert auf der Seite außen geltende Tokens ohne
// vollständige Angaben mit „(fehlt)“ und in der JSON-Ausgabe mit
// herkunft_fehlt; widerrufene Tokens, vollständige und Tokens außerhalb von
// „außen“ bekommen nichts.
func TestAgentTokenCLI_Herkunft799_ListeMarkiertFehlt(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	lauf := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := runAgentTokenMit(append(args, "--db", db), &out); err != nil {
			t.Fatalf("%v: %v %s", args, err, out.String())
		}
		return out.String()
	}
	// Bestand aus der Zeit vor außen: ohne Angaben.
	lauf("create", "--name", "alt-offen", "--board", "b1")
	lauf("create", "--name", "alt-widerrufen", "--board", "b1")
	lauf("revoke", "--name", "alt-widerrufen")
	// F-2115: abgelaufen (nicht widerrufen) und ohne Angaben: kein Hinweis.
	lauf("create", "--name", "alt-abgelaufen", "--board", "b1")
	if _, err := h.ExecContext(ctx, `UPDATE agent_tokens SET expires_at = '2020-01-01T00:00:00Z' WHERE name = 'alt-abgelaufen'`); err != nil {
		t.Fatal(err)
	}
	if out := lauf("list"); strings.Contains(out, "(fehlt)") {
		t.Fatalf("Seite aus: Hinweis trotzdem: %s", out)
	}
	if err := storage.NewConfigRepo(h, nil, uuidString).Set(ctx, api.KonfigBoardpaarSeite, api.BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	lauf("create", "--name", "neu-vollstaendig", "--board", "b1", "--anbieter", "A", "--beleg", "B")
	var zeilen []map[string]any
	if err := json.Unmarshal([]byte(lauf("list", "--json")), &zeilen); err != nil {
		t.Fatal(err)
	}
	fehlt := map[string]bool{}
	for _, z := range zeilen {
		fehlt[z["name"].(string)], _ = z["herkunft_fehlt"].(bool)
	}
	if !fehlt["alt-offen"] || fehlt["alt-widerrufen"] || fehlt["alt-abgelaufen"] || fehlt["neu-vollstaendig"] || len(fehlt) != 4 {
		t.Errorf("herkunft_fehlt: %v", fehlt)
	}
	tabelle := lauf("list")
	for _, zeile := range strings.Split(tabelle, "\n") {
		hat := strings.Contains(zeile, "(fehlt)")
		switch {
		case strings.HasPrefix(zeile, "alt-offen") && strings.Count(zeile, "(fehlt)") != 2:
			t.Errorf("alt-offen: erwartet zweimal (fehlt): %q", zeile)
		case !strings.HasPrefix(zeile, "alt-offen") && hat:
			t.Errorf("Hinweis an falscher Zeile: %q", zeile)
		}
	}
}

// F-2116: Ein Semikolon in Anbieter/Beleg macht den Audit-Wert mehrdeutig - die
// CLI lehnt es ab (Komma bleibt erlaubt).
func TestAgentTokenCLI_Herkunft799_Semikolon(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	for i, p := range [][2]string{{"A;beleg=vertrag.md", ""}, {"x;user=u-admin", "B"}, {"A", "a;b"}} {
		var out bytes.Buffer
		err := runAgentTokenMit([]string{"create", "--name", fmt.Sprintf("cli-semi-%d", i), "--board", "b1",
			"--anbieter", p[0], "--beleg", p[1], "--db", db}, &out)
		if err == nil || !strings.Contains(err.Error(), ";") {
			t.Errorf("%q/%q: angelegt oder Meldung ohne das Zeichen: %v", p[0], p[1], err)
		}
	}
	var out bytes.Buffer
	if err := runAgentTokenMit([]string{"create", "--name", "cli-komma", "--board", "b1",
		"--anbieter", "Beispiel-Anbieter, Geschäftstarif", "--beleg", "B", "--db", db}, &out); err != nil {
		t.Errorf("Komma abgelehnt: %v", err)
	}
	var anzahl int
	if err := h.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&anzahl); err != nil || anzahl != 1 {
		t.Errorf("Anzahl Tokens: %d err=%v", anzahl, err)
	}
}
