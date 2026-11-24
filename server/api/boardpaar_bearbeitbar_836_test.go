package api_test

// Karte #836 (ADR-0141 Nachtrag „Schalter ‚bearbeitbar‘ je Kopie-Board“):
// Wirksam ist der Rückweg auf einer Kopie nur bei boardpaar.rueckweg = an UND
// bearbeitbar = 1. Sonst gilt für dieses Board der Schreibschutz aus 2b — die
// Schranke am Ops-Weg lehnt jede Änderung an Spiegeln ab, mit demselben Grund
// wie in 2b. Dazu der Admin-Weg PUT /admin/boardpaar/kopien/{board}/bearbeitbar.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

const kopie836b = "0e5d8a2c-1111-5222-8333-44445555e836"

// legeKopie836An: Kopie mit einer Spiegelkarte im Backlog, Schalter
// „bearbeitbar“ wie angegeben. Liefert die Außen-ID der Spiegelkarte.
func legeKopie836An(t *testing.T, env *boardsTestEnv, id string, bearbeitbar bool) string {
	t.Helper()
	ctx := context.Background()
	paar := storage.NewBoardpaarRepo(env.db)
	if _, err := paar.KopieAnlegen(ctx, id, "Kopie "+id[len(id)-4:], "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	if err := paar.KopieBearbeitbarSetzen(ctx, id, bearbeitbar); err != nil {
		t.Fatal(err)
	}
	kennung := strings.Repeat("B", 39) + id[len(id)-4:]
	k := &schatten.Kopie{
		Struktur: schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
			Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}, {ID: "col-ready", Titel: "Ready"}}},
		Spiegel: []schatten.SpiegelEingang{{SpiegelID: kennung, Karte: &schatten.SpiegelKarte{Art: schatten.ArtKarte,
			Lane: schatten.BacklogLane, Spalte: "col-backlog", Title: "Spiegel", Labels: []string{}, Assignees: []string{},
			Comments: []schatten.SpiegelKommentar{}}}}}
	if _, err := storage.NewSchattenRepo(env.db).ErsetzeKopie(ctx, id, func(state []byte) ([]byte, error) {
		neu, _, err := schatten.KopieAnwenden(state, k)
		return neu, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	return schatten.AbgeleiteteID(kennung)
}

func feldOp836(karte, wert string) string {
	return `[{"type":"card.field","payload":{"cardId":"` + karte + `","field":"title","value":"` + wert + `"}}]`
}

// (a) Server-Schalter an, Board nicht freigegeben: abgelehnt wie in 2b, kein
// Journal. Auch das Anlegen in der gespiegelten Lane (das mit Rückweg ginge)
// ist abgelehnt.
func TestBearbeitbar836_AnAberNichtFreigegeben(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, false)

	s, out := postOps(t, env, kopie826, feldOp836(karte, "außen"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v", s, out)
	}
	grund827(t, out, karte, "read-only on the external copy until the return path exists")
	s, out = postOps(t, env, kopie826, `[{"type":"card.upsert","payload":{"card":{"id":"neu","title":"Neu","columnId":"col-backlog"},"sprintId":"__backlog__"}}]`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Anlegen in gespiegelter Lane: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal trotz Schreibschutz: %+v", j)
	}
}

// (b) an + freigegeben: wie 2c — angenommen, ein Journal-Eintrag.
func TestBearbeitbar836_AnUndFreigegeben(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	karte := legeKopie836An(t, env, kopie826, true)

	if s, out := postOps(t, env, kopie826, feldOp836(karte, "außen")); s != http.StatusOK {
		t.Fatalf("Feld: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 1 || j[0].Art != "feld" || j[0].Stelle != "title" {
		t.Fatalf("Journal: %+v", j)
	}
}

// (c) Server-Schalter aus, Board freigegeben: abgelehnt — die Freigabe allein
// öffnet nichts.
func TestBearbeitbar836_AusUndFreigegeben(t *testing.T) {
	env, _ := newKopieEnv827(t, "")
	karte := legeKopie836An(t, env, kopie826, true)

	s, out := postOps(t, env, kopie826, feldOp836(karte, "außen"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("Feld: %d %v", s, out)
	}
	grund827(t, out, karte, "read-only on the external copy until the return path exists")
	if j := journal827(t, env); len(j) != 0 {
		t.Errorf("Journal: %+v", j)
	}
}

// (h) Zwei Kopien auf einem Server, eine freigegeben, eine nicht: Sie wirken
// unabhängig. Danach umgekehrt — und Abschalten lässt den Bestand des
// Journals stehen (Nachtrag Punkt 4).
func TestBearbeitbar836_ZweiKopienUnabhaengig(t *testing.T) {
	env, _ := newKopieEnv827(t, "an")
	frei := legeKopie836An(t, env, kopie826, true)
	gesperrt := legeKopie836An(t, env, kopie836b, false)

	if s, out := postOps(t, env, kopie826, feldOp836(frei, "eins")); s != http.StatusOK {
		t.Fatalf("freigegebene Kopie: %d %v", s, out)
	}
	s, out := postOps(t, env, kopie836b, feldOp836(gesperrt, "zwei"))
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("gesperrte Kopie: %d %v", s, out)
	}
	grund827(t, out, gesperrt, "read-only on the external copy until the return path exists")

	ctx := context.Background()
	paar := storage.NewBoardpaarRepo(env.db)
	if err := paar.KopieBearbeitbarSetzen(ctx, kopie826, false); err != nil {
		t.Fatal(err)
	}
	if err := paar.KopieBearbeitbarSetzen(ctx, kopie836b, true); err != nil {
		t.Fatal(err)
	}
	if s, out := postOps(t, env, kopie826, feldOp836(frei, "drei")); s != http.StatusUnprocessableEntity {
		t.Fatalf("abgeschaltete Kopie: %d %v", s, out)
	}
	if s, out := postOps(t, env, kopie836b, feldOp836(gesperrt, "vier")); s != http.StatusOK {
		t.Fatalf("freigeschaltete Kopie: %d %v", s, out)
	}
	if j := journal827(t, env); len(j) != 1 || string(j[0].Nachher) != `"eins"` {
		t.Errorf("Journal der abgeschalteten Kopie nach dem Abschalten: %+v", j)
	}
}

// (g) Admin-Weg: setzt und liest zurück (Partnerliste); nur Admins; keine
// Kopie → 409; nicht gekoppelt → 404; ohne Feld oder falscher Typ → 400.
func TestBearbeitbar836_AdminWeg(t *testing.T) {
	e := newKopieLoeschEnv(t)
	ctx := context.Background()
	if _, err := e.paar.KopieAnlegen(ctx, kopie831ID, "Kopie", kopie831Basis, "abe:test", nil); err != nil {
		t.Fatal(err)
	}
	pfad := "/admin/boardpaar/kopien/" + kopie831ID + "/bearbeitbar"
	an := json.RawMessage(`{"bearbeitbar":true}`)

	if s, _, b := e.do(t, "PUT", pfad, "owner@firma.de", an); s != http.StatusForbidden {
		t.Errorf("ohne Admin: %d %s", s, b)
	}
	if k, _ := e.paar.AussenGekoppelt(ctx, kopie831ID); k == nil || k.Bearbeitbar {
		t.Fatalf("Voreinstellung (auch nach dem abgewiesenen Aufruf): %+v", k)
	}
	if s, _, b := e.do(t, "PUT", pfad, "admin@firma.de", an); s != http.StatusOK || !strings.Contains(b, `"bearbeitbar":true`) {
		t.Fatalf("an: %d %s", s, b)
	}
	if k, _ := e.paar.AussenGekoppelt(ctx, kopie831ID); k == nil || !k.Bearbeitbar {
		t.Errorf("nach an: %+v", k)
	}
	if s, _, b := e.do(t, "GET", "/admin/boardpaar", "admin@firma.de", nil); s != http.StatusOK || !strings.Contains(b, `"bearbeitbar":true`) {
		t.Errorf("Partnerliste: %d %s", s, b)
	}
	if s, _, b := e.do(t, "PUT", pfad, "admin@firma.de", json.RawMessage(`{"bearbeitbar":false}`)); s != http.StatusOK {
		t.Fatalf("aus: %d %s", s, b)
	}
	if k, _ := e.paar.AussenGekoppelt(ctx, kopie831ID); k == nil || k.Bearbeitbar {
		t.Errorf("nach aus: %+v", k)
	}
	for _, rumpf := range []string{`{}`, `{"bearbeitbar":"ja"}`, `{"bearbeitbar":1}`} {
		if s, _, b := e.do(t, "PUT", pfad, "admin@firma.de", json.RawMessage(rumpf)); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", rumpf, s, b)
		}
	}
	if s, _, b := e.do(t, "PUT", "/admin/boardpaar/kopien/b2/bearbeitbar", "admin@firma.de", an); s != http.StatusNotFound {
		t.Errorf("nicht gekoppelt: %d %s", s, b)
	}
	// Stufe-1-Kopplung (kopie = 0): kein Schalter.
	if _, err := e.paar.AussenKoppeln(ctx, "b1", kopie831Basis, "admin"); err != nil {
		t.Fatal(err)
	}
	if s, _, b := e.do(t, "PUT", "/admin/boardpaar/kopien/b1/bearbeitbar", "admin@firma.de", an); s != http.StatusConflict {
		t.Errorf("Stufe 1: %d %s", s, b)
	}
	// Seite innen: kein Schalter.
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarSeite, "innen", false, "t"); err != nil {
		t.Fatal(err)
	}
	if s, _, b := e.do(t, "PUT", pfad, "admin@firma.de", an); s != http.StatusConflict {
		t.Errorf("Seite innen: %d %s", s, b)
	}
}
