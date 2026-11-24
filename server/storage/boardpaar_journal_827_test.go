package storage

// Karte #827 (ADR-0141 §5.1, §5.2): Journal außen schreiben (Verschmelzen nur
// vor dem Abholen), wartende Spiegel, Abholen mit Grenze, Bestätigen,
// Aufräumen.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
)

func journalSchreiben827(t *testing.T, boards *BoardRepo, e []schatten.JournalEintrag, nw map[string]string, person string, zeit time.Time) {
	t.Helper()
	ctx := context.Background()
	conn, err := boards.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := BoardpaarJournalSchreiben(ctx, conn, kopieID826, e, nw, person, "mensch", zeit); err != nil {
		t.Fatal(err)
	}
}

func titel827(vorher, nachher string) schatten.JournalEintrag {
	v, _ := json.Marshal(vorher)
	n, _ := json.Marshal(nachher)
	return schatten.JournalEintrag{Objekt: "karte", Kennung: "K1", Art: schatten.JArtFeld, Stelle: "title", Vorher: v, Nachher: n}
}

func abholen827(t *testing.T, boards *BoardRepo, nach int64, max int, jetzt time.Time) *JournalAbruf {
	t.Helper()
	a, err := boards.JournalAbholen(context.Background(), kopieID826, nach, max, jetzt, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestJournal827_VerschmelzenNurVorDemAbholen(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("A", "B")}, nil, "anna@x", jetzt)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("B", "C")}, nil, "anna@x", jetzt)
	a := abholen827(t, boards, 0, 0, jetzt)
	if len(a.Eintraege) != 1 || string(a.Eintraege[0].Vorher) != `"A"` || string(a.Eintraege[0].Nachher) != `"C"` {
		t.Fatalf("verschmolzen erwartet (A→C): %+v", a.Eintraege)
	}
	// Nach dem Abholen: neuer Eintrag, nicht verschmolzen (VF-3).
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("C", "D")}, nil, "anna@x", jetzt)
	b := abholen827(t, boards, 0, 0, jetzt)
	if len(b.Eintraege) != 2 || b.Eintraege[1].Seq <= b.Eintraege[0].Seq || string(b.Eintraege[1].Vorher) != `"C"` {
		t.Fatalf("zweiter Eintrag erwartet: %+v", b.Eintraege)
	}
	// Andere Person: nicht verschmolzen.
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("D", "E")}, nil, "bert@x", jetzt)
	// Zurück auf den Ausgangswert vor dem Abholen: Eintrag fällt weg.
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("E", "D")}, nil, "bert@x", jetzt)
	if c := abholen827(t, boards, 0, 0, jetzt); len(c.Eintraege) != 2 {
		t.Fatalf("D→E→D derselben Person ergibt keinen Eintrag: %+v", c.Eintraege)
	}
}

func TestJournal827_AngelegtTraegtWartend(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Now()
	e := schatten.JournalEintrag{Objekt: "karte", Kennung: "aussen-1", Wartend: true, Art: schatten.JArtAngelegt, Nachher: json.RawMessage(`{"title":"N"}`)}
	journalSchreiben827(t, boards, []schatten.JournalEintrag{e}, map[string]string{"aussen-1": "karte"}, "anna@x", jetzt)
	conn, _ := boards.db.Conn(context.Background())
	defer conn.Close()
	w, err := BoardpaarWartend(context.Background(), conn, kopieID826)
	if err != nil || !w["aussen-1"] {
		t.Fatalf("wartend: %v %v", w, err)
	}
	var seq int64
	if err := conn.QueryRowContext(context.Background(), `SELECT seq_angelegt FROM boardpaar_wartend WHERE aussen_id = 'aussen-1'`).Scan(&seq); err != nil || seq == 0 {
		t.Fatalf("seq_angelegt: %d %v", seq, err)
	}
	a := abholen827(t, boards, 0, 0, jetzt)
	if len(a.Eintraege) != 1 || !a.Eintraege[0].Wartend || a.Eintraege[0].Seq != seq {
		t.Fatalf("%+v", a.Eintraege)
	}
	// Ohne zugehörigen Eintrag angelegt: Fehler (nichts still verloren).
	conn2, _ := boards.db.Conn(context.Background())
	defer conn2.Close()
	if err := BoardpaarJournalSchreiben(context.Background(), conn2, kopieID826, nil, map[string]string{"x": "karte"}, "p", "mensch", jetzt); err == nil {
		t.Fatal("wartend ohne angelegt muss scheitern")
	}
}

func TestJournal827_AbholenGrenzeCursorVeraltet(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	alt := jetzt.Add(-26 * 24 * time.Hour)
	var es []schatten.JournalEintrag
	for _, s := range []string{"a", "b", "c"} {
		es = append(es, schatten.JournalEintrag{Objekt: "karte", Kennung: "K" + s, Art: schatten.JArtFeld, Stelle: "title",
			Vorher: json.RawMessage(`"x"`), Nachher: json.RawMessage(`"y"`)})
	}
	journalSchreiben827(t, boards, es[:1], nil, "p", alt)
	journalSchreiben827(t, boards, es[1:], nil, "p", jetzt)
	a := abholen827(t, boards, 0, 2, jetzt)
	if len(a.Eintraege) != 2 || !a.Weitere || a.LetzteSeq != a.Eintraege[1].Seq || !a.Veraltet {
		t.Fatalf("%+v", a)
	}
	b := abholen827(t, boards, a.LetzteSeq, 2, jetzt)
	if len(b.Eintraege) != 1 || b.Weitere || b.Veraltet {
		t.Fatalf("%+v", b)
	}
	if _, err := boards.JournalAbholen(context.Background(), kopieID826, 999, 0, jetzt, false); !errors.Is(err, ErrJournalCursor) {
		t.Fatalf("Cursor jenseits des Endes: %v", err)
	}
	if _, err := boards.JournalAbholen(context.Background(), "frei", 0, 0, jetzt, false); !errors.Is(err, ErrBoardpaarNichtGekoppelt) {
		t.Fatalf("keine Kopie: %v", err)
	}
	// Grenze 200 auch bei größerem max.
	if JournalAbrufMax != 200 {
		t.Fatal("Grenze 200 (L-4)")
	}
}

func TestJournal827_BestaetigenUndAufraeumen(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("A", "B")}, nil, "p", jetzt)
	// Nie abgeholt: nicht bestätigt.
	if n, err := boards.JournalBestaetigen(context.Background(), kopieID826, 100, jetzt); err != nil || n != 0 {
		t.Fatalf("nicht abgeholt: %d %v", n, err)
	}
	a := abholen827(t, boards, 0, 0, jetzt)
	if n, _ := boards.JournalBestaetigen(context.Background(), kopieID826, a.LetzteSeq, jetzt); n != 1 {
		t.Fatalf("bestätigt: %d", n)
	}
	if b := abholen827(t, boards, 0, 0, jetzt); len(b.Eintraege) != 0 {
		t.Fatalf("bestätigte kommen nicht wieder: %+v", b.Eintraege)
	}
	if n, _ := boards.JournalAufraeumen(context.Background(), jetzt.Add(29*24*time.Hour)); n != 0 {
		t.Fatal("vor 30 Tagen nichts löschen")
	}
	if n, _ := boards.JournalAufraeumen(context.Background(), jetzt.Add(31*24*time.Hour)); n != 1 {
		t.Fatal("nach 30 Tagen löschen")
	}
}

// Sitzungs-Kritik A-1: Nach Bestätigen und Aufräumen ist das Journal leer;
// ein Abruf mit dem letzten Cursor ist KEIN Rücksprung.
func TestJournal827_CursorNachAufraeumenKeinHalt(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("A", "B")}, nil, "p", jetzt)
	a := abholen827(t, boards, 0, 0, jetzt)
	if _, err := boards.JournalBestaetigen(context.Background(), kopieID826, a.LetzteSeq, jetzt); err != nil {
		t.Fatal(err)
	}
	spaeter := jetzt.Add(31 * 24 * time.Hour)
	if n, _ := boards.JournalAufraeumen(context.Background(), spaeter); n != 1 {
		t.Fatalf("aufgeräumt: %d", n)
	}
	b, err := boards.JournalAbholen(context.Background(), kopieID826, a.LetzteSeq, 0, spaeter, false)
	if err != nil || len(b.Eintraege) != 0 || b.LetzteSeq != a.LetzteSeq {
		t.Fatalf("leerer Abruf erwartet, kein Rücksprung: %+v %v", b, err)
	}
}

// Sitzungs-Kritik B-1: zwei Personen an derselben Stelle VOR dem Abholen —
// nicht verschmolzen.
func TestJournal827_AnderePersonNichtVerschmolzen(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Now()
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("A", "B")}, nil, "anna@x", jetzt)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("B", "C")}, nil, "bert@x", jetzt)
	if a := abholen827(t, boards, 0, 0, jetzt); len(a.Eintraege) != 2 || a.Eintraege[0].Person != "anna@x" || a.Eintraege[1].Person != "bert@x" {
		t.Fatalf("zwei Einträge erwartet: %+v", a.Eintraege)
	}
}

// Sitzungs-Kritik A-6: Verschmelzen lässt die Zeit des ersten Eintrags stehen —
// „veraltet“ misst das Alter.
func TestJournal827_VerschmelzenBehaeltZeit(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("A", "B")}, nil, "p", jetzt.Add(-26*24*time.Hour))
	journalSchreiben827(t, boards, []schatten.JournalEintrag{titel827("B", "C")}, nil, "p", jetzt)
	a := abholen827(t, boards, 0, 0, jetzt)
	if len(a.Eintraege) != 1 || !a.Veraltet || string(a.Eintraege[0].Nachher) != `"C"` {
		t.Fatalf("verschmolzen, aber veraltet: %+v", a)
	}
}

// Sitzungs-Kritik B-6: max über 200 wird auf 200 gekappt.
func TestJournal827_GrenzeBeiGroessermMax(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Now()
	es := make([]schatten.JournalEintrag, 0, 201)
	for i := 0; i < 201; i++ {
		es = append(es, schatten.JournalEintrag{Objekt: "karte", Kennung: "K" + strconv.Itoa(i), Art: schatten.JArtListeAdd,
			Stelle: "labels:x", Nachher: json.RawMessage(`"x"`)})
	}
	journalSchreiben827(t, boards, es, nil, "p", jetzt)
	a := abholen827(t, boards, 0, 500, jetzt)
	if len(a.Eintraege) != 200 || !a.Weitere {
		t.Fatalf("200 und weitere erwartet: %d %v", len(a.Eintraege), a.Weitere)
	}
}

// Verify V-1: Ein verschmolzener Eintrag mit Anker reiht sich hinten neu ein —
// sein neuer Anker kann erst nach dem alten Eintrag angelegt worden sein.
func TestJournal827_VerschmolzenerAnkerReihtSichNeuEin(t *testing.T) {
	boards, _ := kopie826(t)
	jetzt := time.Now()
	ort := func(nach string) json.RawMessage {
		return json.RawMessage(`{"lane":"kl","spalte":"col-backlog","nach":` + nach + `}`)
	}
	versch := func(v, n json.RawMessage) schatten.JournalEintrag {
		return schatten.JournalEintrag{Objekt: "karte", Kennung: "KX", Art: schatten.JArtVerschoben, Stelle: "ort", Vorher: v, Nachher: n}
	}
	start := json.RawMessage(`{"lane":"kl","spalte":"col-ready","nach":null}`)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{versch(start, ort(`{"kennung":"KA","wartend":false}`))}, nil, "p", jetzt)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{{Objekt: "karte", Kennung: "y1", Wartend: true, Art: schatten.JArtAngelegt,
		Nachher: json.RawMessage(`{"title":"Y"}`)}}, map[string]string{"y1": "karte"}, "p", jetzt)
	journalSchreiben827(t, boards, []schatten.JournalEintrag{versch(ort(`{"kennung":"KA","wartend":false}`), ort(`{"kennung":"y1","wartend":true}`))}, nil, "p", jetzt)
	a := abholen827(t, boards, 0, 0, jetzt)
	if len(a.Eintraege) != 2 || a.Eintraege[0].Art != schatten.JArtAngelegt || a.Eintraege[1].Art != schatten.JArtVerschoben ||
		string(a.Eintraege[1].Vorher) != string(start) || !strings.Contains(string(a.Eintraege[1].Nachher), `"y1"`) {
		b, _ := json.Marshal(a.Eintraege)
		t.Fatalf("angelegt Y vor dem verschmolzenen verschoben X erwartet: %s", b)
	}
}
