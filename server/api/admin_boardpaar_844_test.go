package api_test

// Karte #844 (ADR-0141 Nachtrag): Admin-Rückmeldung.
//   - F-2299: PUT …/bearbeitbar sendet schatten:aktualisiert für das Board.
//   - F-2240: Notweg „Kopie außen löschen“ nennt die offenen Änderungen
//     (unbestätigtes Journal + wartende Zeilen) in Antwort und Liste.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestBearbeitbar844_SendetSchattenEreignis(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	if _, err := e.paar.KopieAnlegen(ctx, kopie831ID, "Kopie", kopie831Basis, "abe:test", nil); err != nil {
		t.Fatal(err)
	}
	ch, ab := e.broker.Subscribe(kopie831ID)
	defer ab()
	pfad := "/admin/boardpaar/kopien/" + kopie831ID + "/bearbeitbar"

	// Abgewiesener Aufruf (kein Admin): kein Ereignis.
	if s, _, b := e.do(t, "PUT", pfad, "owner@firma.de", json.RawMessage(`{"bearbeitbar":true}`)); s != http.StatusForbidden {
		t.Fatalf("ohne Admin: %d %s", s, b)
	}
	select {
	case ev := <-ch:
		t.Fatalf("Ereignis nach abgewiesenem Aufruf: %+v", ev)
	default:
	}

	if s, _, b := e.do(t, "PUT", pfad, "admin@firma.de", json.RawMessage(`{"bearbeitbar":true}`)); s != http.StatusOK {
		t.Fatalf("an: %d %s", s, b)
	}
	select {
	case ev := <-ch:
		if ev.Type != api.SchattenEreignis {
			t.Errorf("Ereignistyp: %q", ev.Type)
		}
		var d struct {
			BoardID string `json:"board_id"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &d); err != nil || d.BoardID != kopie831ID {
			t.Errorf("Ereignisdaten: %q (%v)", ev.Data, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kein schatten:aktualisiert nach dem Umstellen")
	}
}

func TestKopieLoeschen844_ZaehltOffeneAenderungen(t *testing.T) {
	e := newKopieLoeschEnv(t)
	kopieMitAnhang(t, e.db, e.paar) // 1 Journal (unbestätigt, k1) + 1 wartende Zeile
	zeile := func(k string, bestaetigt bool) {
		bt := "NULL"
		if bestaetigt {
			bt = "'2026-10-14T11:00:00Z'"
		}
		if _, err := e.db.Exec(`INSERT INTO boardpaar_journal (board_id, objekt, kennung, art, zeit, bestaetigt_at)
			VALUES ('` + kopie831ID + `', 'karte', '` + k + `', 'geaendert', '2026-10-14T10:00:00Z', ` + bt + `)`); err != nil {
			t.Fatal(err)
		}
	}
	zeile("k2", false)
	zeile("k3", true) // bestätigt: zählt nicht
	for i, k := range []string{"k2", "k3"} {
		if _, err := e.db.Exec(`INSERT INTO boardpaar_wartend (board_id, aussen_id, objekt, seq_angelegt) VALUES ('`+kopie831ID+`', '`+k+`', 'karte', ?)`, i+2); err != nil {
			t.Fatal(err)
		}
	}
	// Nur das unbestätigte Journal (k1, k2). Wartende Zeilen zählen nicht
	// eigens: k1/k2 stehen schon über ihren Journal-Eintrag drin (sonst
	// doppelt), k3 ist bestätigt — das Skript hat die Karte innen.
	const soll = 2

	// Liste (gekoppelt): dieselbe Zahl, die das Löschen gleich nennt.
	_, liste, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil)
	a, _ := liste["aussen"].([]any)
	if len(a) != 1 || a[0].(map[string]any)["offene_aenderungen"] != float64(soll) {
		t.Fatalf("Liste aussen: %s", raw)
	}

	s, m, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+kopie831ID, "admin@firma.de", nil)
	if s != http.StatusOK || m["offene_aenderungen"] != float64(soll) {
		t.Fatalf("Antwort: %d %s", s, raw)
	}
	// Die Zahl ist das, was tatsächlich weg ist.
	if zaehle(t, e.db, "boardpaar_journal", "board_id") != 0 || zaehle(t, e.db, "boardpaar_wartend", "board_id") != 0 {
		t.Error("Journal oder Wartend nach dem Löschen noch da")
	}
}

func TestKopieLoeschen844_EntkoppeltUndOhneOffene(t *testing.T) {
	e := newKopieLoeschEnv(t)
	kopieMitAnhang(t, e.db, e.paar)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`,
		`{"version":"2","board":{"id":"`+kopie831ID+`","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}]}}`,
		kopie831ID); err != nil {
		t.Fatal(err)
	}
	if err := e.paar.AussenEntkoppeln(context.Background(), kopie831ID); err != nil {
		t.Fatal(err)
	}
	_, liste, raw := e.do(t, http.MethodGet, "/admin/boardpaar", "admin@firma.de", nil)
	f, _ := liste["kopien_entkoppelt"].([]any)
	if len(f) != 1 || f[0].(map[string]any)["offene_aenderungen"] != float64(1) {
		t.Fatalf("Liste kopien_entkoppelt: %s", raw)
	}
	// Frische Kopie ohne Journal: 0, nicht fehlend.
	s, m, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+kopie831ID, "admin@firma.de", nil)
	if s != http.StatusOK || m["offene_aenderungen"] != float64(1) || m["entkoppelt"] != true {
		t.Fatalf("entkoppelt: %d %s", s, raw)
	}
}

func TestKopieLoeschen844_NullOffene(t *testing.T) {
	e := newKopieLoeschEnv(t)
	if _, err := e.paar.KopieAnlegen(context.Background(), kopie831ID, "Kopie", kopie831Basis, "abe:test", nil); err != nil {
		t.Fatal(err)
	}
	s, m, raw := e.do(t, http.MethodDelete, "/admin/boardpaar/kopien/"+kopie831ID, "admin@firma.de", nil)
	if v, ok := m["offene_aenderungen"]; s != http.StatusOK || !ok || v != float64(0) {
		t.Fatalf("ohne Journal: %d %s", s, raw)
	}
}
