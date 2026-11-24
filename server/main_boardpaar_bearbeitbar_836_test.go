package main

// Karte #836 (ADR-0141 Nachtrag „Schalter ‚bearbeitbar‘ je Kopie-Board“) an
// der echten Kette aus hauptHandler: das optionale Feld `bearbeitbar` an
// POST /agent/boardpaar/boards und PUT /agent/boardpaar/spiegel/{board}, und
// der Board-Abruf GET /boards/{id}/schatten, über den der Browser den Wert je
// Board erfährt.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func bearbeitbar836(t *testing.T, s *paarSeite, id string) bool {
	t.Helper()
	k, err := s.paar.AussenGekoppelt(context.Background(), id)
	if err != nil || k == nil {
		t.Fatalf("Kopplung %s: %+v %v", id, k, err)
	}
	return k.Bearbeitbar
}

func anzeige836(t *testing.T, s *paarSeite, id string) (kopie, bearbeitbar bool) {
	t.Helper()
	status, b := anfrage825(t, s.srv, "GET", "/boards/"+id+"/schatten", "", nil)
	var a struct {
		Kopie       bool `json:"kopie"`
		Bearbeitbar bool `json:"bearbeitbar"`
	}
	if status != 200 || json.Unmarshal(b, &a) != nil || !strings.Contains(string(b), `"bearbeitbar":`) {
		t.Fatalf("Anzeige %s: %d %s", id, status, b)
	}
	return a.Kopie, a.Bearbeitbar
}

// (e) POST boards: ohne Feld 0, mit Feld wie gesendet, falscher Typ 422 wie
// die übrigen Formfehler dieses Wegs (nichts angelegt); eine Wiederholung
// ohne Feld lässt den Wert stehen, mit Feld setzt sie ihn.
func TestBoardpaar836_AnlegenMitFeld(t *testing.T) {
	ctx := context.Background()
	aussen := neueSeite(t, "aussen")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	ohne := schatten.AbgeleiteteID(skriptKennung("ohne"))
	mit := schatten.AbgeleiteteID(skriptKennung("mit"))
	falsch := schatten.AbgeleiteteID(skriptKennung("falsch"))
	anlage := func(id string, extra map[string]any) map[string]any {
		m := map[string]any{"id": id, "titel": "K", "innen_link_basis": "https://innen.example/#board=x"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage(ohne, nil)); s != 201 || !strings.Contains(string(b), `"bearbeitbar":false`) {
		t.Fatalf("ohne Feld: %d %s", s, b)
	}
	if bearbeitbar836(t, aussen, ohne) {
		t.Error("ohne Feld: bearbeitbar = 1, erwartet 0")
	}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage(mit, map[string]any{"bearbeitbar": true})); s != 201 ||
		!strings.Contains(string(b), `"bearbeitbar":true`) {
		t.Fatalf("mit Feld: %d %s", s, b)
	}
	if !bearbeitbar836(t, aussen, mit) {
		t.Error("mit Feld: bearbeitbar = 0, erwartet 1")
	}
	for _, wert := range []any{"ja", 1, map[string]any{}} {
		if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage(falsch, map[string]any{"bearbeitbar": wert})); s != 422 {
			t.Errorf("falscher Typ %v: %d %s", wert, s, b)
		}
	}
	if k, _ := aussen.paar.AussenGekoppelt(ctx, falsch); k != nil {
		t.Errorf("falscher Typ hat angelegt: %+v", k)
	}
	// Wiederholung: ohne Feld bleibt 1, mit false wird 0.
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage(mit, nil)); s != 200 || !strings.Contains(string(b), `"bearbeitbar":true`) {
		t.Errorf("Wiederholung ohne Feld: %d %s", s, b)
	}
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe, anlage(mit, map[string]any{"bearbeitbar": false})); s != 200 ||
		!strings.Contains(string(b), `"wiederholt":true`) || !strings.Contains(string(b), `"bearbeitbar":false`) {
		t.Errorf("Wiederholung mit false: %d %s", s, b)
	}
	if bearbeitbar836(t, aussen, mit) {
		t.Error("Wiederholung mit false: bearbeitbar = 1")
	}
}

// (f) Hinweg: mit Feld setzt er den Wert (und meldet ihn), ohne Feld lässt er
// ihn stehen, null zählt wie „nicht gesendet“, ein falscher Typ ist 422 und
// ändert nichts. (i) Der Board-Abruf liefert den Wert je Board.
func TestBoardpaar836_HinwegUndAnzeige(t *testing.T) {
	ctx := context.Background()
	aussen := neueSeite(t, "aussen")
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	id := schatten.AbgeleiteteID(skriptKennung("hinweg"))
	zwei := schatten.AbgeleiteteID(skriptKennung("zweite"))
	for _, k := range []string{id, zwei} {
		if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe,
			map[string]any{"id": k, "titel": "K", "innen_link_basis": "https://innen.example/#board=x"}); s != 201 {
			t.Fatalf("anlegen: %d %s", s, b)
		}
	}
	struktur := schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
		Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}}}
	push := func(board string, extra map[string]any) (int, []byte) {
		m := map[string]any{"version": api.BuildVersion, "struktur": struktur, "schatten": []any{}, "spiegel": []any{}, "rueckweg_bis": nil}
		for k, v := range extra {
			m[k] = v
		}
		return anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+board, abe, m)
	}

	if s, b := push(id, nil); s != 200 || !strings.Contains(string(b), `"bearbeitbar":false`) {
		t.Fatalf("ohne Feld: %d %s", s, b)
	}
	if kopie, bb := anzeige836(t, aussen, id); !kopie || bb {
		t.Errorf("Anzeige vor der Freigabe: kopie=%v bearbeitbar=%v", kopie, bb)
	}
	if s, b := push(id, map[string]any{"bearbeitbar": true}); s != 200 || !strings.Contains(string(b), `"bearbeitbar":true`) {
		t.Fatalf("mit true: %d %s", s, b)
	}
	if !bearbeitbar836(t, aussen, id) {
		t.Error("mit true: nicht gesetzt")
	}
	if s, b := push(id, nil); s != 200 || !bearbeitbar836(t, aussen, id) {
		t.Errorf("ohne Feld danach: %d %s, Wert verloren", s, b)
	}
	if s, b := push(id, map[string]any{"bearbeitbar": nil}); s != 200 || !bearbeitbar836(t, aussen, id) {
		t.Errorf("null: %d %s, Wert verloren", s, b)
	}
	if s, b := push(id, map[string]any{"bearbeitbar": "false"}); s != 422 || !bearbeitbar836(t, aussen, id) {
		t.Errorf("falscher Typ: %d %s (erwartet 422, Wert unverändert)", s, b)
	}
	// (i) Je Board: die erste Kopie freigegeben, die zweite nicht.
	if kopie, bb := anzeige836(t, aussen, id); !kopie || !bb {
		t.Errorf("Anzeige freigegeben: kopie=%v bearbeitbar=%v", kopie, bb)
	}
	if kopie, bb := anzeige836(t, aussen, zwei); !kopie || bb {
		t.Errorf("Anzeige zweite Kopie: kopie=%v bearbeitbar=%v", kopie, bb)
	}
	if s, b := push(id, map[string]any{"bearbeitbar": false}); s != 200 || bearbeitbar836(t, aussen, id) {
		t.Errorf("mit false: %d %s", s, b)
	}
	if _, bb := anzeige836(t, aussen, id); bb {
		t.Error("Anzeige nach false: noch bearbeitbar")
	}
}

// F-2301: Den Schalter setzt am Hinweg nur der Zugang mit „darf Kopien
// anlegen“. Ein anderes abe_ mit dem Feld (true oder false) bekommt 403, und
// nichts wird geschrieben — weder Schalter noch Team-Rolle noch Struktur.
// Ohne Feld (oder mit null) bleibt der Hinweg für jedes abe_ offen.
func TestBoardpaar836_HinwegFeldNurMitAnlegeRecht(t *testing.T) {
	ctx := context.Background()
	aussen := neueSeite(t, "aussen")
	for k, v := range map[string]string{api.KonfigBoardpaarTeam: "team@team.example", api.KonfigBoardpaarRueckweg: "an"} {
		if err := aussen.cfg.Set(ctx, k, v, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	abe, _, _ := aussen.paar.TokenAnlegenMit(ctx, storage.BoardpaarTypEingang, "kopien", "t", true)
	ohneRecht, _, _ := aussen.paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "nur-hinweg", "t")
	id := schatten.AbgeleiteteID(skriptKennung("recht"))
	if s, b := anfrage825(t, aussen.srv, "POST", "/agent/boardpaar/boards", abe,
		map[string]any{"id": id, "titel": "K", "innen_link_basis": "https://innen.example/#board=x", "bearbeitbar": true}); s != 201 {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	rolle := func() string {
		t.Helper()
		ro, _, err := storage.NewAclRepo(aussen.db).GetRole(ctx, id, "team@team.example")
		if err != nil {
			t.Fatal(err)
		}
		return ro
	}
	if rolle() != storage.RoleEditor {
		t.Fatalf("Vorbedingung: Team %q, erwartet editor", rolle())
	}
	stand := func() string {
		t.Helper()
		b, err := storage.NewBoardRepo(aussen.db).Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return string(b.StateJSON)
	}
	vorher := stand()
	struktur := schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog neu", Kind: "backlog"}},
		Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog neu"}}}
	push := func(token string, extra map[string]any) (int, []byte) {
		m := map[string]any{"version": api.BuildVersion, "struktur": struktur, "schatten": []any{}, "spiegel": []any{}, "rueckweg_bis": nil}
		for k, v := range extra {
			m[k] = v
		}
		return anfrage825(t, aussen.srv, "PUT", "/agent/boardpaar/spiegel/"+id, token, m)
	}

	for _, wert := range []bool{false, true} {
		s, b := push(ohneRecht, map[string]any{"bearbeitbar": wert})
		if s != 403 || !strings.Contains(string(b), "darf Kopien anlegen") {
			t.Errorf("ohne Recht mit %v: %d %s, erwartet 403", wert, s, b)
		}
	}
	if !bearbeitbar836(t, aussen, id) || rolle() != storage.RoleEditor || stand() != vorher {
		t.Errorf("nach 403 geändert: bearbeitbar=%v rolle=%q stand gleich=%v", bearbeitbar836(t, aussen, id), rolle(), stand() == vorher)
	}
	// Ohne Feld oder mit null: wie bisher, auch ohne Recht.
	if s, b := push(ohneRecht, map[string]any{"bearbeitbar": nil}); s != 200 {
		t.Errorf("ohne Recht, null: %d %s", s, b)
	}
	if s, b := push(ohneRecht, nil); s != 200 || !bearbeitbar836(t, aussen, id) || rolle() != storage.RoleEditor {
		t.Errorf("ohne Recht, ohne Feld: %d %s", s, b)
	}
	if stand() == vorher {
		t.Error("Hinweg ohne Feld hat die Struktur nicht geschrieben")
	}
	// Mit Recht: setzt wie bisher, samt Team-Rolle.
	if s, b := push(abe, map[string]any{"bearbeitbar": false}); s != 200 || bearbeitbar836(t, aussen, id) || rolle() != storage.RoleViewer {
		t.Errorf("mit Recht, false: %d %s, rolle %q", s, b, rolle())
	}
}
