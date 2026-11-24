package main

// Karte #827 (ADR-0141 §2, §4; Etappe E2): Merge-Eingang an der echten Kette —
// innen Hinweg Format 3 mit ursprung, das Skript übersetzt, außen liest der
// Eingang den Schutz aus Journal und boardpaar_wartend in seiner Transaktion:
// eine offene Stelle bleibt, die wartende Karte bekommt ihre Marke, die Zeile
// in boardpaar_wartend verschwindet, und der Eingang schreibt kein Journal.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaar827_MergeEingangUeberZweiServer(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	// Innen: Stand aus 825, dazu eine außen angelegte und innen schon
	// angewendete Karte (abgeleitete ID, boardpaarUrsprung = Außen-ID).
	innenID := schatten.AbgeleiteteID("w1")
	stand := strings.Replace(innenStand825, `{"id":"k3","displayId":3,"title":"Idee","columnId":"col-backlog"}`,
		`{"id":"k3","displayId":3,"title":"Idee","columnId":"col-backlog"},{"id":"`+innenID+`","displayId":4,"title":"Neu außen","columnId":"col-backlog","boardpaarUrsprung":"w1"}`, 1)
	if stand == innenStand825 {
		t.Fatal("Ersetzung im innen-Stand griff nicht")
	}
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-innen", "Projekt X", "2", []byte(stand)); err != nil {
		t.Fatal(err)
	}
	abs, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	if _, err := innen.paar.AuftragAnlegen(ctx, "b-innen", "team", "https://innen.example/#board=b-innen", "admin"); err != nil {
		t.Fatal(err)
	}
	aussenID := schatten.AbgeleiteteID(skriptKennung("b-innen"))
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe,
		map[string]any{"id": aussenID, "titel": "Projekt X", "innen_link_basis": "https://innen.example/#board=b-innen"}); s != 201 {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	hinweg := func(rueckwegBis any) map[string]any {
		s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen?format=3", abs, nil)
		var h map[string]any
		if err := json.Unmarshal(b, &h); s != 200 || err != nil {
			t.Fatalf("Hinweg: %d %s", s, b)
		}
		st := h["struktur"].(map[string]any)
		for _, l := range st["lanes"].([]any) {
			l.(map[string]any)["id"] = skriptKennung(l.(map[string]any)["id"].(string))
		}
		for _, sp := range st["spalten"].([]any) {
			sp.(map[string]any)["id"] = skriptKennung(sp.(map[string]any)["id"].(string))
		}
		for _, e := range h["schatten"].([]any) {
			m := e.(map[string]any)
			m["schattenId"] = skriptKennung(m["id"].(string))
			delete(m, "id")
			if l, ok := m["lane"].(string); ok {
				m["lane"], m["spalte"] = skriptKennung(l), skriptKennung(m["spalte"].(string))
			}
		}
		for _, e := range h["spiegel"].([]any) {
			m := e.(map[string]any)
			m["spiegelId"] = skriptKennung(m["id"].(string))
			delete(m, "id")
			if m["art"] == "karte" {
				m["lane"], m["spalte"] = skriptKennung(m["lane"].(string)), skriptKennung(m["spalte"].(string))
			}
		}
		return map[string]any{"version": h["version"], "struktur": st, "schatten": h["schatten"],
			"spiegel": h["spiegel"], "rueckweg_bis": rueckwegBis}
	}

	// Erster Lauf ohne die außen angelegte Karte: Sie kommt innen schon mit,
	// wir nehmen sie für den Ausgangsstand außen heraus.
	erster := hinweg(nil)
	var ohne []any
	for _, e := range erster["spiegel"].([]any) {
		if e.(map[string]any)["ursprung"] == nil {
			ohne = append(ohne, e)
		}
	}
	erster["spiegel"] = ohne
	if s, b := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, erster); s != 200 {
		t.Fatalf("erster Eingang: %d %s", s, b)
	}

	// Außen (wie der Ops-Weg es hinterließe): Titel von „Angebot Müller“
	// geändert und eine Karte w1 angelegt — beides im Journal, w1 wartend.
	boards := storage.NewBoardRepo(aussen.db)
	kopie, _ := boards.Get(ctx, aussenID)
	neu := strings.Replace(string(kopie.StateJSON), `"title":"Angebot Müller"`, `"title":"Angebot Müller (außen)"`, 1)
	neu = strings.Replace(neu, `"cards":[`, `"cards":[{"id":"w1","displayId":50,"title":"Neu außen","columnId":"col-backlog"},`, 1)
	if neu == string(kopie.StateJSON) {
		t.Fatal("Bearbeitung außen griff nicht")
	}
	if _, err := aussen.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, neu, aussenID); err != nil {
		t.Fatal(err)
	}
	conn, err := aussen.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k1 := skriptKennung("k1")
	if err := storage.BoardpaarJournalSchreiben(ctx, conn, aussenID, []schatten.JournalEintrag{
		{Objekt: schatten.ArtKarte, Kennung: k1, Art: schatten.JArtFeld, Stelle: "title",
			Vorher: json.RawMessage(`"Angebot Müller"`), Nachher: json.RawMessage(`"Angebot Müller (außen)"`)},
		{Objekt: schatten.ArtKarte, Kennung: "w1", Wartend: true, Art: schatten.JArtAngelegt, Nachher: json.RawMessage(`{}`)},
	}, map[string]string{"w1": "karte"}, "gerd@team.example", "mensch", time.Now()); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	journalVorher := zaehle827(t, aussen, "boardpaar_journal")

	// Zweiter Lauf mit ursprung: Titel bleibt außen (offene Stelle), w1
	// bekommt ihre Marke, die wartende Zeile verschwindet.
	s, b := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, hinweg(nil))
	if s != 200 {
		t.Fatalf("zweiter Eingang: %d %s", s, b)
	}
	kopie, _ = boards.Get(ctx, aussenID)
	st := string(kopie.StateJSON)
	if !strings.Contains(st, `"title":"Angebot Müller (außen)"`) {
		t.Errorf("offene Stelle überschrieben: %s", st)
	}
	if strings.Count(st, `"title":"Neu außen"`) != 1 {
		t.Errorf("w1 nicht genau einmal: %s", st)
	}
	var stand2 struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	_ = json.Unmarshal(kopie.StateJSON, &stand2)
	gefunden := false
	for _, l := range stand2.Board.Sprints {
		for _, k := range l.Cards {
			if k["id"] == "w1" {
				gefunden = true
				m, _ := k["spiegel"].(map[string]any)
				if m == nil || m["id"] != skriptKennung(innenID) || k["displayId"] != float64(50) {
					t.Errorf("w1 nicht markiert oder Nummer verloren: %v", k)
				}
			}
		}
	}
	if !gefunden {
		t.Fatal("w1 fehlt außen")
	}
	if n := zaehle827(t, aussen, "boardpaar_wartend"); n != 0 {
		t.Errorf("boardpaar_wartend: %d Zeilen", n)
	}
	if n := zaehle827(t, aussen, "boardpaar_journal"); n != journalVorher {
		t.Errorf("Eingang hat das Journal verändert: %d → %d", journalVorher, n)
	}
}

func zaehle827(t *testing.T, s *paarSeite, tabelle string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + tabelle).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
