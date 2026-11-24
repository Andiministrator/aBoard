package storage

// Karte #826 (ADR-0141 §7, Schritt 2b): Spiegel-Schranke an Update und
// TransferCard, Marken-Entfernung beim Insert.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

const kopieID826 = "0e5d8a2c-1111-5222-8333-44445555d826"

var kennung826 = strings.Repeat("K", 43)

// kopie826: eine externe Kopie mit einer Spiegelkarte im Backlog und einem
// Spiegeltermin; dazu ein gewöhnliches Board „frei“.
func kopie826(t *testing.T) (*BoardRepo, *BoardpaarRepo) {
	t.Helper()
	ctx := context.Background()
	db := kopieTestDB(t)
	paar := NewBoardpaarRepo(db)
	if _, err := paar.KopieAnlegen(ctx, kopieID826, "Kopie", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	k := &schatten.Kopie{
		Struktur: schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
			Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}}},
		Spiegel: []schatten.SpiegelEingang{
			{SpiegelID: kennung826, Karte: &schatten.SpiegelKarte{Art: schatten.ArtKarte, Lane: schatten.BacklogLane,
				Spalte: "col-backlog", Title: "Spiegel", Labels: []string{}, Assignees: []string{}, Comments: []schatten.SpiegelKommentar{}}},
			{SpiegelID: strings.Repeat("T", 43), Termin: &schatten.SpiegelTermin{Art: schatten.ArtTermin, Titel: "Termin",
				Datum: "2026-11-01", Personen: []string{}}},
		}}
	if _, err := NewSchattenRepo(db).ErsetzeKopie(ctx, kopieID826, func(state []byte) ([]byte, error) {
		neu, _, err := schatten.KopieAnwenden(state, k)
		return neu, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	boards := NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "frei", "Frei", "2", CanonicalEmptyBoardState("frei", "Frei")); err != nil {
		t.Fatal(err)
	}
	return boards, paar
}

func zustand826(t *testing.T, boards *BoardRepo, id string) map[string]any {
	t.Helper()
	b, err := boards.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	_ = json.Unmarshal(b.StateJSON, &st)
	return st
}

func spiegelKarte826(st map[string]any) map[string]any {
	for _, l := range st["board"].(map[string]any)["sprints"].([]any) {
		for _, k := range l.(map[string]any)["cards"].([]any) {
			if km := k.(map[string]any); km["spiegel"] != nil {
				return km
			}
		}
	}
	return nil
}

func TestSpiegel826_UpdateGesperrt(t *testing.T) {
	ctx := context.Background()
	boards, paar := kopie826(t)
	st := zustand826(t, boards, kopieID826)
	if spiegelKarte826(st) == nil {
		t.Fatal("keine Spiegelkarte angelegt")
	}
	schreibe := func(st map[string]any) error {
		raw, _ := json.Marshal(st)
		_, err := boards.Update(ctx, kopieID826, "Kopie", "2", raw, 0)
		return err
	}
	spiegelKarte826(st)["title"] = "außen geändert"
	err := schreibe(st)
	var se *SpiegelGeschuetztError
	if !errors.Is(err, ErrSpiegelGeschuetzt) || !errors.As(err, &se) || len(se.Orte) != 1 || se.Orte[0].Art != "karte" {
		t.Fatalf("Ganz-PUT auf Spiegel: %v", err)
	}
	if !strings.Contains(err.Error(), "nur lesbar") {
		t.Errorf("Meldung: %v", err)
	}
	if got := spiegelKarte826(zustand826(t, boards, kopieID826)); got["title"] != "Spiegel" {
		t.Errorf("trotzdem geschrieben: %v", got["title"])
	}
	// Was kein Spiegel ist, bleibt frei.
	st = zustand826(t, boards, kopieID826)
	st["board"].(map[string]any)["awaitingUser"] = nil
	if err := schreibe(st); err != nil {
		t.Errorf("freies Feld: %v", err)
	}
	// Ein gewöhnliches Board trifft die Schranke nicht, auch mit Marken.
	frei := zustand826(t, boards, "frei")
	frei["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["spiegel"] = map[string]any{"id": "x"}
	raw, _ := json.Marshal(frei)
	if _, err := boards.Update(ctx, "frei", "Frei", "2", raw, 0); err != nil {
		t.Errorf("gewöhnliches Board: %v", err)
	}
	// Außen entkoppelt: keine Kopie mehr, wieder frei bearbeitbar.
	if err := paar.AussenEntkoppeln(ctx, kopieID826); err != nil {
		t.Fatal(err)
	}
	st = zustand826(t, boards, kopieID826)
	spiegelKarte826(st)["title"] = "nach dem Entkoppeln"
	if err := schreibe(st); err != nil {
		t.Errorf("entkoppelte Kopie: %v", err)
	}
	if ist, _ := boards.IstKopie(ctx, kopieID826); ist {
		t.Error("IstKopie nach Entkoppeln")
	}
}

func TestSpiegel826_TransferGesperrt(t *testing.T) {
	ctx := context.Background()
	boards, _ := kopie826(t)
	karte := spiegelKarte826(zustand826(t, boards, kopieID826))
	_, err := boards.TransferCard(ctx, TransferCardParams{SrcBoardID: kopieID826, DstBoardID: "frei",
		CardID: karte["id"].(string), DstColumnID: "col-backlog", Actor: "a@x"})
	if !errors.Is(err, ErrSpiegelGeschuetzt) {
		t.Errorf("Transfer aus der Kopie: %v", err)
	}
	// In die Kopie hinein: die Backlog-Lane ist gespiegelt.
	st := zustand826(t, boards, "frei")
	b := st["board"].(map[string]any)
	b["columnDefs"] = []any{map[string]any{"id": "col-backlog", "title": "Backlog"}}
	b["sprints"].([]any)[0].(map[string]any)["cards"] = []any{map[string]any{"id": "eigen", "title": "Eigen", "columnId": "col-backlog"}}
	raw, _ := json.Marshal(st)
	if _, err := boards.Update(ctx, "frei", "Frei", "2", raw, 0); err != nil {
		t.Fatal(err)
	}
	_, err = boards.TransferCard(ctx, TransferCardParams{SrcBoardID: "frei", DstBoardID: kopieID826,
		CardID: "eigen", DstColumnID: "col-backlog", Actor: "a@x"})
	if !errors.Is(err, ErrSpiegelGeschuetzt) {
		t.Errorf("Transfer in die Kopie: %v", err)
	}
	if spiegelKarte826(zustand826(t, boards, kopieID826)) == nil {
		t.Error("Spiegelkarte nach abgelehntem Transfer weg")
	}
}

func TestSpiegel826_InsertOhneMarken(t *testing.T) {
	ctx := context.Background()
	boards, _ := kopie826(t)
	b, _ := boards.Get(ctx, kopieID826)
	neu := strings.Replace(string(b.StateJSON), kopieID826, "abzug", 1)
	ins, err := boards.Insert(ctx, "abzug", "Abzug", "2", []byte(neu))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ins.StateJSON), `"spiegel"`) {
		t.Error("Insert behält Marken")
	}
	if gel, _ := boards.Get(ctx, "abzug"); strings.Contains(string(gel.StateJSON), `"spiegel"`) {
		t.Error("gespeichert mit Marken")
	}
}
