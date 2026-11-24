package main

// Nacharbeit #831, Kritik K-4: Nach dem Notweg „Kopie außen löschen“ läuft der
// nächste Hinweg des Skripts ins Leere (404, nichts angelegt), und ein neuer
// Auftrag legt die Kopie frisch an — danach nimmt der Eingang den Hinweg
// wieder an. Gerüst und Skript-Nachbau wie main_boardpaar_kopie_825_test.go.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestBoardpaar831_HinwegNachNotweg(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	if _, err := storage.NewBoardRepo(innen.db).Insert(ctx, "b-innen", "Projekt X", "2", []byte(innenStand825)); err != nil {
		t.Fatal(err)
	}
	abs, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	if _, err := innen.paar.AuftragAnlegen(ctx, "b-innen", "team-extern", "https://innen.example/#board=b-innen", "admin"); err != nil {
		t.Fatal(err)
	}
	aussenID := schatten.AbgeleiteteID(skriptKennung("b-innen"))
	anlage := map[string]any{"id": aussenID, "titel": "Projekt X", "innen_link_basis": "https://innen.example/#board=b-innen"}

	// Hinweg holen und wie das Skript übersetzen.
	push := func() (int, []byte) {
		t.Helper()
		s, b := anfrage825(t, innen.srv, "GET", "/agent/boardpaar/spiegel/b-innen", abs, nil)
		if s != 200 {
			t.Fatalf("hinweg innen: %d %s", s, b)
		}
		var hinweg struct {
			Version  string               `json:"version"`
			Struktur schatten.Struktur    `json:"struktur"`
			Schatten []schatten.EintragV2 `json:"schatten"`
		}
		if err := json.Unmarshal(b, &hinweg); err != nil {
			t.Fatal(err)
		}
		for i := range hinweg.Struktur.Lanes {
			hinweg.Struktur.Lanes[i].ID = skriptKennung(hinweg.Struktur.Lanes[i].ID)
		}
		for i := range hinweg.Struktur.Spalten {
			hinweg.Struktur.Spalten[i].ID = skriptKennung(hinweg.Struktur.Spalten[i].ID)
		}
		var liste []map[string]any
		for _, e := range hinweg.Schatten {
			eb, _ := json.Marshal(e)
			var m map[string]any
			_ = json.Unmarshal(eb, &m)
			delete(m, "id")
			m["schattenId"] = skriptKennung(e.ID)
			if e.Lane != nil {
				m["lane"], m["spalte"] = skriptKennung(*e.Lane), skriptKennung(*e.Spalte)
			}
			liste = append(liste, m)
		}
		return anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+aussenID, abe, map[string]any{
			"version": hinweg.Version, "struktur": hinweg.Struktur, "schatten": liste, "spiegel": []any{}, "rueckweg_bis": nil})
	}

	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage); s != 201 {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	if s, b := push(); s != 200 {
		t.Fatalf("erster Hinweg: %d %s", s, b)
	}

	// Notweg (Storage-Teil; der HTTP-Weg ist in api/admin_boardpaar_831_test.go geprüft).
	if g, err := aussen.paar.KopieLoeschen(ctx, aussenID); err != nil || g.Entkoppelt {
		t.Fatalf("KopieLoeschen: %+v %v", g, err)
	}

	// Hinweg an das gelöschte Board: 404, und es entsteht nichts.
	if s, b := push(); s != 404 {
		t.Errorf("Hinweg nach dem Löschen: %d %s, erwartet 404", s, b)
	}
	if b, err := storage.NewBoardRepo(aussen.db).Get(ctx, aussenID); err == nil && b != nil {
		t.Errorf("Hinweg hat das gelöschte Board wieder angelegt: %+v", b)
	}
	if z, _ := storage.NewSchattenRepo(aussen.db).Liste(ctx, aussenID); len(z) != 0 {
		t.Errorf("Hinweg hat Schatten ohne Board geschrieben: %d", len(z))
	}

	// Neuer Auftrag: frisch angelegt (keine Wiederholung, keine Wiederaufnahme),
	// danach nimmt der Eingang den Hinweg wieder an.
	s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage)
	if s != 201 || !strings.Contains(string(b), `"wiederholt":false`) || !strings.Contains(string(b), `"wiederaufgenommen":false`) {
		t.Fatalf("neu anlegen nach dem Löschen: %d %s", s, b)
	}
	if s, b := push(); s != 200 || !strings.Contains(string(b), `"struktur_geaendert":true`) {
		t.Errorf("Hinweg an die neue Kopie: %d %s", s, b)
	}
	if z, _ := storage.NewSchattenRepo(aussen.db).Liste(ctx, aussenID); len(z) != 3 {
		t.Errorf("Schatten der neuen Kopie: %d, erwartet 3", len(z))
	}
}
