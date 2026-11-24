package storage

// Karte #827 (ADR-0141 „Umsetzung 2c“ Entscheidung 2; F-2208): Ganz-PUT und
// Transfer lehnen auch Änderungen an wartenden Spiegeln ab — sie
// journalisieren nicht. Und die Reste entkoppelter Kopien räumt der
// retention-Lauf nach Frist ab, nicht beim Entkoppeln.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// wartendeKarte827 legt in der Kopie eine Karte „w1“ ohne Marke in die
// gespiegelte Backlog-Spalte und trägt sie samt Eintrag „angelegt“ als
// wartend ein — so, wie der Ops-Weg mit Rückweg sie hinterlässt.
func wartendeKarte827(t *testing.T, boards *BoardRepo, zeit time.Time) {
	t.Helper()
	ctx := context.Background()
	st := zustand826(t, boards, kopieID826)
	for _, l := range st["board"].(map[string]any)["sprints"].([]any) {
		lm := l.(map[string]any)
		if lm["id"] == schatten.BacklogLane {
			lm["cards"] = append(lm["cards"].([]any), map[string]any{"id": "w1", "title": "Wartend", "columnId": "col-backlog"})
		}
	}
	raw, _ := json.Marshal(st)
	if _, err := boards.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, raw, kopieID826); err != nil {
		t.Fatal(err)
	}
	conn, err := boards.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := BoardpaarJournalSchreiben(ctx, conn, kopieID826, []schatten.JournalEintrag{{Objekt: schatten.ArtKarte, Kennung: "w1",
		Wartend: true, Art: schatten.JArtAngelegt, Nachher: json.RawMessage(`{"title":"Wartend"}`)}},
		map[string]string{"w1": "karte"}, "gerd@team.example", "mensch", zeit); err != nil {
		t.Fatal(err)
	}
}

func TestSpiegel827_UpdateUndTransferSchuetzenWartende(t *testing.T) {
	ctx := context.Background()
	boards, _ := kopie826(t)
	wartendeKarte827(t, boards, time.Now())

	// Ganz-PUT, der nur die wartende Karte ändert: abgelehnt, mit Ort.
	st := zustand826(t, boards, kopieID826)
	for _, l := range st["board"].(map[string]any)["sprints"].([]any) {
		for _, k := range l.(map[string]any)["cards"].([]any) {
			if km := k.(map[string]any); km["id"] == "w1" {
				km["title"] = "per PUT"
			}
		}
	}
	raw, _ := json.Marshal(st)
	_, err := boards.Update(ctx, kopieID826, "Kopie", "2", raw, 0)
	var g *SpiegelGeschuetztError
	if !errors.As(err, &g) || len(g.Orte) != 1 || g.Orte[0] != (schatten.SpiegelOrt{Art: "karte", ID: "w1"}) {
		t.Fatalf("Update: %v", err)
	}

	// Transfer der wartenden Karte hinaus: abgelehnt.
	_, err = boards.TransferCard(ctx, TransferCardParams{SrcBoardID: kopieID826, DstBoardID: "frei",
		CardID: "w1", DstColumnID: "col-backlog", Actor: "a@x"})
	if !errors.As(err, &g) || len(g.Orte) != 1 || g.Orte[0].ID != "w1" {
		t.Fatalf("Transfer: %v", err)
	}
}

func journalZahl827(t *testing.T, boards *BoardRepo) (journal, wartend int) {
	t.Helper()
	if err := boards.db.QueryRow(`SELECT (SELECT COUNT(*) FROM boardpaar_journal), (SELECT COUNT(*) FROM boardpaar_wartend)`).
		Scan(&journal, &wartend); err != nil {
		t.Fatal(err)
	}
	return journal, wartend
}

func TestJournal827_ResteEntkoppelterKopienNachFrist(t *testing.T) {
	ctx := context.Background()
	boards, paar := kopie826(t)
	jetzt := time.Now()
	wartendeKarte827(t, boards, jetzt.Add(-26*24*time.Hour))

	// Gekoppelt: auch ein alter, offener Eintrag bleibt (dort meldet der
	// Abruf „veraltet“, und das Skript geht in HALT).
	if _, err := boards.JournalAufraeumen(ctx, jetzt); err != nil {
		t.Fatal(err)
	}
	if j, w := journalZahl827(t, boards); j != 1 || w != 1 {
		t.Fatalf("gekoppelt: journal=%d wartend=%d", j, w)
	}

	// Entkoppelt, aber noch innerhalb der Frist: bleibt (Wiederaufnahme).
	if err := paar.AussenEntkoppeln(ctx, kopieID826); err != nil {
		t.Fatal(err)
	}
	if _, err := boards.JournalAufraeumen(ctx, jetzt.Add(-2*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if j, w := journalZahl827(t, boards); j != 1 || w != 1 {
		t.Fatalf("entkoppelt, in der Frist: journal=%d wartend=%d", j, w)
	}

	// Entkoppelt und über der Frist: Eintrag und wartende Zeile weg.
	n, err := boards.JournalAufraeumen(ctx, jetzt)
	if err != nil {
		t.Fatal(err)
	}
	if j, w := journalZahl827(t, boards); j != 0 || w != 0 || n != 2 {
		t.Fatalf("entkoppelt, über der Frist: journal=%d wartend=%d n=%d", j, w, n)
	}
}

// Ein bestätigter Eintrag „angelegt“: Die Karte ist innen angekommen. Nach
// 30 Tagen fällt der Eintrag regulär weg, die wartende Zeile bleibt — sonst
// legte der Hinweg die Karte nach einer Wiederaufnahme ein zweites Mal an.
func TestJournal827_WartendNachBestaetigtemAnlegenBleibt(t *testing.T) {
	ctx := context.Background()
	boards, paar := kopie826(t)
	jetzt := time.Now()
	wartendeKarte827(t, boards, jetzt.Add(-40*24*time.Hour))
	alt := jZeit(jetzt.Add(-35 * 24 * time.Hour))
	if _, err := boards.db.Exec(`UPDATE boardpaar_journal SET abgeholt_at = ?, bestaetigt_at = ?`, alt, alt); err != nil {
		t.Fatal(err)
	}
	if err := paar.AussenEntkoppeln(ctx, kopieID826); err != nil {
		t.Fatal(err)
	}
	if _, err := boards.JournalAufraeumen(ctx, jetzt); err != nil {
		t.Fatal(err)
	}
	if j, w := journalZahl827(t, boards); j != 0 || w != 1 {
		t.Fatalf("journal=%d wartend=%d, erwartet 0/1", j, w)
	}
}
