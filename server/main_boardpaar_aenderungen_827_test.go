package main

// Karte #827 (ADR-0141 §5.2, §10; Etappe E1b Teil 3): Journal außen abholen
// und bestätigen an der echten Kette aus hauptHandler — Wege, Seiten,
// Zugänge, HEAD ohne Abholen, Cursor-Rücksprung.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const kopie827 = "0e5d8a2c-1111-5222-8333-44445555e827"

type abruf827 struct {
	Eintraege []storage.JournalAusgabe `json:"eintraege"`
	Weitere   bool                     `json:"weitere"`
	LetzteSeq int64                    `json:"letzte_seq"`
	Veraltet  bool                     `json:"veraltet"`
}

func holeAenderungen827(t *testing.T, s *paarSeite, methode, query, token string) (int, abruf827) {
	t.Helper()
	code, b := anfrage825(t, s.srv, methode, "/agent/boardpaar/aenderungen/"+kopie827+query, token, nil)
	var a abruf827
	if code == http.StatusOK && methode == http.MethodGet {
		if err := json.Unmarshal(b, &a); err != nil {
			t.Fatalf("Antwort: %v %s", err, b)
		}
	}
	return code, a
}

// legeJournalAn827: Kopie außen und drei Einträge, wie der Ops-Weg sie
// schreibt (der Weg selbst ist in api/boardpaar_journal_ops_827_test.go
// geprüft).
func legeJournalAn827(t *testing.T, aussen *paarSeite) {
	t.Helper()
	ctx := context.Background()
	if _, err := aussen.paar.KopieAnlegen(ctx, kopie827, "Kopie", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	conn, err := aussen.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	kennung := strings.Repeat("K", 43)
	var eintraege []schatten.JournalEintrag
	for i, stelle := range []string{"title", "description", "priority"} {
		eintraege = append(eintraege, schatten.JournalEintrag{Objekt: schatten.ArtKarte, Kennung: kennung, Art: schatten.JArtFeld,
			Stelle: stelle, Vorher: json.RawMessage(`"alt"`), Nachher: json.RawMessage(fmt.Sprintf(`"neu %d"`, i))})
	}
	if err := storage.BoardpaarJournalSchreiben(ctx, conn, kopie827, eintraege, nil, "gerd@team.example", "mensch", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestBoardpaar827_AenderungenAbholenUndBestaetigen(t *testing.T) {
	ctx := context.Background()
	aussen := neueSeite(t, "aussen")
	legeJournalAn827(t, aussen)
	abe, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "kopien", "t")

	// HEAD liest nur: Danach ist nichts abgeholt, Bestätigen greift nicht.
	if code, _ := holeAenderungen827(t, aussen, http.MethodHead, "", abe); code != http.StatusOK {
		t.Fatalf("HEAD: %d", code)
	}
	if code, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/aenderungen/"+kopie827+"/bestaetigt", abe, map[string]any{"bis": 1000}); code != 200 || !strings.Contains(string(b), `"bestaetigt":0`) {
		t.Fatalf("bestätigen nach HEAD: %d %s", code, b)
	}

	// max=2: zwei Einträge, weitere.
	code, a := holeAenderungen827(t, aussen, http.MethodGet, "?max=2", abe)
	if code != 200 || len(a.Eintraege) != 2 || !a.Weitere || a.Veraltet || a.Eintraege[0].Person != "gerd@team.example" ||
		a.LetzteSeq != a.Eintraege[1].Seq || string(a.Eintraege[0].Nachher) != `"neu 0"` {
		t.Fatalf("Abruf 1: %d %+v", code, a)
	}
	// Ohne Bestätigung kommen dieselben Einträge erneut (§5.2).
	if _, b := holeAenderungen827(t, aussen, http.MethodGet, "", abe); len(b.Eintraege) != 3 || b.Weitere {
		t.Fatalf("Abruf 2: %+v", b)
	}
	// Bestätigen bis zum zweiten: zwei; danach liefert der Abruf ab dort nur den dritten.
	if code, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/aenderungen/"+kopie827+"/bestaetigt", abe, map[string]any{"bis": a.LetzteSeq}); code != 200 || !strings.Contains(string(b), `"bestaetigt":2`) {
		t.Fatalf("bestätigen: %d %s", code, b)
	}
	if _, b := holeAenderungen827(t, aussen, http.MethodGet, fmt.Sprintf("?nach=%d", a.LetzteSeq), abe); len(b.Eintraege) != 1 || b.Eintraege[0].Stelle != "priority" {
		t.Fatalf("Abruf 3: %+v", b)
	}

	// Cursor jenseits der Journal-Marke: 409 (Sicherung eingespielt, HALT).
	if code, _ := holeAenderungen827(t, aussen, http.MethodGet, "?nach=99999", abe); code != http.StatusConflict {
		t.Errorf("Cursor jenseits: %d", code)
	}
	// Ungültige Eingaben: 422.
	for _, q := range []string{"?nach=-1", "?nach=x", "?max=-5"} {
		if code, _ := holeAenderungen827(t, aussen, http.MethodGet, q, abe); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", q, code)
		}
	}
	for _, body := range []any{map[string]any{}, map[string]any{"bis": -1}, map[string]any{"bis": 1, "x": 2}} {
		if code, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/aenderungen/"+kopie827+"/bestaetigt", abe, body); code != http.StatusUnprocessableEntity {
			t.Errorf("Body %v: %d %s", body, code, b)
		}
	}
}

// Wege und Seiten: nur abe_, nur außen, nur Kopien; jeder andere Pfad und
// die Menschen-Kette 403.
func TestBoardpaar827_AenderungenWege(t *testing.T) {
	ctx := context.Background()
	innen := neueSeite(t, "innen")
	aussen := neueSeite(t, "aussen")
	legeJournalAn827(t, aussen)
	abe, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "kopien", "t")
	abeInnen, _, _ := innen.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "kopien", "t")
	abs, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "t")
	abr, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypRueckweg, "rueck", "t")
	// Stufe-1-Kopplung (kopie = 0): kein Journal-Weg.
	if _, err := storage.NewBoardRepo(aussen.db).Insert(ctx, "s1", "S", "2", []byte(`{"version":"2","board":{"id":"s1","title":"S","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := aussen.paar.AussenKoppeln(ctx, "s1", "https://innen.example/#board=x", "a"); err != nil {
		t.Fatal(err)
	}
	pfad := "/agent/boardpaar/aenderungen/" + kopie827
	for _, f := range []struct {
		name          string
		srv           *paarSeite
		methode, pfad string
		token         string
		body          any
		want          int
	}{
		{"abs auf aenderungen", aussen, "GET", pfad, abs, nil, 403},
		{"abr auf aenderungen", aussen, "GET", pfad, abr, nil, 403},
		{"abs auf bestaetigt", aussen, "POST", pfad + "/bestaetigt", abs, map[string]any{"bis": 1}, 403},
		{"abe mit Unterpfad", aussen, "GET", pfad + "/x", abe, nil, 403},
		{"abe falsches Ende", aussen, "POST", pfad + "/anders", abe, map[string]any{"bis": 1}, 403},
		{"abe bestaetigt per GET", aussen, "GET", pfad + "/bestaetigt", abe, nil, 403},
		{"abe aenderungen per POST", aussen, "POST", pfad, abe, nil, 403},
		{"abe am Menschen-Pfad", aussen, "GET", "/boardpaar/aenderungen/" + kopie827, abe, nil, 403},
		{"innen: 404 vor dem Token", innen, "GET", pfad, abeInnen, nil, 404},
		{"erfundenes abe", aussen, "GET", pfad, "abe_erfunden", nil, 401},
		{"unbekanntes Board", aussen, "GET", "/agent/boardpaar/aenderungen/gibt-es-nicht", abe, nil, 404},
		{"Stufe-1-Board", aussen, "GET", "/agent/boardpaar/aenderungen/s1", abe, nil, 404},
		{"Stufe-1-Board bestätigen", aussen, "POST", "/agent/boardpaar/aenderungen/s1/bestaetigt", abe, map[string]any{"bis": 1}, 404},
		{"Kopie", aussen, "GET", pfad, abe, nil, 200},
	} {
		if s, b := anfrage825(t, f.srv.srv, f.methode, f.pfad, f.token, f.body); s != f.want {
			t.Errorf("%s: %d, erwartet %d (%s)", f.name, s, f.want, b)
		}
	}
}
