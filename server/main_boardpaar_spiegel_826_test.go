package main

// Karte #826 (ADR-0141 Schritt 2b): Spiegel über zwei Server an der echten
// Kette — innen Hinweg im Format 3 (?format=3), das Skript übersetzt die IDs,
// außen legt der Eingang die Spiegel in Lane und Spalte an. Ohne ?format=3
// bleibt der Hinweg im Format 2 (Umstieg ohne Gleichzeitigkeit).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaar826_SpiegelUeberZweiServer(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-innen", "Projekt X", "2", []byte(innenStand825)); err != nil {
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

	// Ohne ?format bleibt es bei Format 2; ein unbekanntes Format ist 400.
	if s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen", abs, nil); s != 200 ||
		!strings.Contains(string(b), `"format":2`) || !strings.Contains(string(b), `"spiegel":[]`) {
		t.Errorf("Format 2: %d %s", s, b)
	}
	if s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen?format=4", abs, nil); s != 400 {
		t.Errorf("Format 4: %d %s", s, b)
	}

	hole := func() map[string]any {
		s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen?format=3", abs, nil)
		var h map[string]any
		if err := json.Unmarshal(b, &h); s != 200 || err != nil || h["format"] != float64(3) {
			t.Fatalf("Format 3: %d %s", s, b)
		}
		return h
	}
	// Das Skript: jede innere ID → Kennung.
	uebersetze := func(h map[string]any) map[string]any {
		st := h["struktur"].(map[string]any)
		for _, l := range st["lanes"].([]any) {
			l.(map[string]any)["id"] = skriptKennung(l.(map[string]any)["id"].(string))
		}
		for _, s := range st["spalten"].([]any) {
			s.(map[string]any)["id"] = skriptKennung(s.(map[string]any)["id"].(string))
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
			} else if k, ok := m["karte"].(string); ok {
				m["karte"] = skriptKennung(k)
			}
		}
		return map[string]any{"version": h["version"], "struktur": st, "schatten": h["schatten"],
			"spiegel": h["spiegel"], "rueckweg_bis": nil}
	}
	h := hole()
	if len(h["spiegel"].([]any)) != 2 || len(h["schatten"].([]any)) != 1 || h["struktur"].(map[string]any)["titel"] != "Projekt X" {
		t.Fatalf("Hinweg: %v", h)
	}
	s, b := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, uebersetze(h))
	if s != 200 || !strings.Contains(string(b), `"spiegel":{"neu":2,`) {
		t.Fatalf("eingang: %d %s", s, b)
	}
	kopie, _ := storage.NewBoardRepo(aussen.db).Get(ctx, aussenID)
	for _, soll := range []string{`"title":"Angebot Müller"`, `"title":"Idee"`, `"owner":"Gerd Muster"`} {
		if !strings.Contains(string(kopie.StateJSON), soll) {
			t.Errorf("außen fehlt %s", soll)
		}
	}
	if strings.Contains(string(kopie.StateJSON), "Geheim Kunde") {
		t.Error("vertrauliche Karte steht im Board-Zustand außen")
	}
	if zeilen, _ := storage.NewSchattenRepo(aussen.db).Liste(ctx, aussenID); len(zeilen) != 1 || !zeilen[0].Vertraulich {
		t.Errorf("Schatten außen: %+v", zeilen)
	}

	// Unverändert: kein Schreiben.
	if s, b := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, uebersetze(hole())); s != 200 ||
		!strings.Contains(string(b), `"struktur_geaendert":false`) {
		t.Errorf("zweiter Lauf: %d %s", s, b)
	}
	if danach, _ := storage.NewBoardRepo(aussen.db).Get(ctx, aussenID); danach.UpdatedAt != kopie.UpdatedAt {
		t.Error("unveränderter Hinweg hat geschrieben")
	}

	// Innen wird „Idee“ vertraulich und das Board umbenannt (F-2140): außen
	// verschwindet die Karte, der Schatten tritt an ihre Stelle, der Titel folgt.
	geaendert := strings.Replace(innenStand825, `"title":"Idee","columnId":"col-backlog"`, `"title":"Idee","columnId":"col-backlog","confidential":true`, 1)
	geaendert = strings.Replace(geaendert, `"title":"Projekt X"`, `"title":"Projekt Y"`, 1)
	if _, err := storage.NewBoardRepo(innen.db).Update(ctx, "b-innen", "Projekt Y", "2", []byte(geaendert), 0); err != nil {
		t.Fatal(err)
	}
	if s, b := anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, uebersetze(hole())); s != 200 ||
		!strings.Contains(string(b), `"entfernt":1`) {
		t.Errorf("vertraulich geworden: %d %s", s, b)
	}
	kopie, _ = storage.NewBoardRepo(aussen.db).Get(ctx, aussenID)
	if strings.Contains(string(kopie.StateJSON), `"title":"Idee"`) || kopie.Title != "Projekt Y" {
		t.Errorf("nach dem Wechsel: Titel %q, %s", kopie.Title, kopie.StateJSON)
	}
	if zeilen, _ := storage.NewSchattenRepo(aussen.db).Liste(ctx, aussenID); len(zeilen) != 2 {
		t.Errorf("Schatten nach dem Wechsel: %d", len(zeilen))
	}
}
