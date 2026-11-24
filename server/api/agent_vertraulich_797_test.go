package api_test

// Karte #797 (ADR-0141 W-A, Entscheid des Maintainers 13.10.2026): Agenten innen können beim
// Anlegen von Karte und Termin ausdrücklich `vertraulich: false` senden.
// Dazu F-2230: Werkzeugtexte außen nach Stufe-1-Board und Kopie getrennt.

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// vertraulich797: der Wert von confidential am Eintrag; "<fehlt>" ohne Feld.
func (e *env789) vertraulich797(t *testing.T, titel string) any {
	t.Helper()
	v, ok := e.karteVon(t, "b1", titel)["confidential"]
	if !ok {
		return "<fehlt>"
	}
	return v
}

// anlegen797: Karte oder Termin über OpenAPI, Antwort muss 200 sein.
func (e *env789) anlegen797(t *testing.T, token, email, pfad, body string) {
	t.Helper()
	if s, b := e.neu.rufe(t, token, email, "POST", pfad, body); s != http.StatusOK {
		t.Fatalf("%s %s: %d %s", pfad, body, s, b)
	}
}

func TestVertraulich797_KarteUndTermin_Innen(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten, termine := "/werkzeuge/boards/b1/karten", "/werkzeuge/boards/b1/termine"

	// OpenAPI, Projekt-Token.
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"V797 ohne"}`)
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"V797 false","vertraulich":false}`)
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"V797 true","vertraulich":true}`)
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"V797 null","vertraulich":null}`)
	e.anlegen797(t, x.projekt, "", termine, `{"titel":"V797 T ohne","datum":"2026-10-21"}`)
	e.anlegen797(t, x.projekt, "", termine, `{"titel":"V797 T false","datum":"2026-10-21","vertraulich":false}`)
	e.anlegen797(t, x.projekt, "", termine, `{"titel":"V797 T true","datum":"2026-10-21","vertraulich":true}`)
	// Delegierend (Open WebUI) gilt dasselbe.
	e.anlegen797(t, x.token, "anna@firma.de", karten, `{"titel":"V797 Gerd false","vertraulich":false}`)
	e.anlegen797(t, x.token, "anna@firma.de", karten, `{"titel":"V797 Gerd ohne"}`)

	for titel, soll := range map[string]any{
		"V797 ohne": true, "V797 false": "<fehlt>", "V797 true": true, "V797 null": true,
		"V797 T ohne": true, "V797 T false": "<fehlt>", "V797 T true": true,
		"V797 Gerd false": "<fehlt>", "V797 Gerd ohne": true,
	} {
		if v := e.vertraulich797(t, titel); v != soll {
			t.Errorf("%s: confidential = %v, erwartet %v", titel, v, soll)
		}
	}

	// MCP: dieselben Fälle.
	for _, c := range []struct {
		args, titel string
		soll        any
	}{
		{`{"boardId":"b1","titel":"V797 M ohne"}`, "V797 M ohne", true},
		{`{"boardId":"b1","titel":"V797 M false","vertraulich":false}`, "V797 M false", "<fehlt>"},
		{`{"boardId":"b1","titel":"V797 M true","vertraulich":true}`, "V797 M true", true},
	} {
		s, m := x.mcpWerkzeug(t, x.projekt, "", "karte_anlegen", c.args)
		if fehler, text, _ := ergebnis753(t, m); s != http.StatusOK || fehler {
			t.Fatalf("MCP %s: %d %s", c.args, s, text)
		}
		if v := e.vertraulich797(t, c.titel); v != c.soll {
			t.Errorf("%s: confidential = %v, erwartet %v", c.titel, v, c.soll)
		}
	}
	s, m := x.mcpWerkzeug(t, x.projekt, "", "termin_anlegen", `{"boardId":"b1","titel":"V797 M Termin false","datum":"2026-10-22","vertraulich":false}`)
	if fehler, text, _ := ergebnis753(t, m); s != http.StatusOK || fehler {
		t.Fatalf("MCP termin_anlegen: %d %s", s, text)
	}
	if v := e.vertraulich797(t, "V797 M Termin false"); v != "<fehlt>" {
		t.Errorf("MCP Termin false: %v", v)
	}

	// Das false gilt nur für den einen Aufruf: Der nächste ohne Angabe ist
	// wieder vertraulich (kein Zustand zwischen Aufrufen).
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"V797 danach"}`)
	if v := e.vertraulich797(t, "V797 danach"); v != true {
		t.Errorf("Aufruf nach einem false: %v", v)
	}
}

// Ein false, das der Agent nicht gesendet hat, darf es nicht geben: Rohe
// Ops mit confidential:false und mit einem mitgeschickten Feld in der Nutzlast
// bleiben wirkungslos, nur das Werkzeug-Feld öffnet.
func TestVertraulich797_RoheOpsOeffnenNicht(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops",
		`{"ops":[`+neueKarte789("v797-roh", "V797 roh false", `,"confidential":false,"vertraulich":false`)+`]}`); s != http.StatusOK {
		t.Fatalf("roh: %d %s", s, b)
	}
	if v := e.vertraulich797(t, "V797 roh false"); v != true {
		t.Errorf("rohe Op mit false: %v", v)
	}
}

// Ungültiger Typ: abgelehnt (400), nichts angelegt. Begründung steht in
// boolFeld: Zeichenkette "false" wird nicht gedeutet.
func TestVertraulich797_UngueltigerTyp(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	for i, wert := range []string{`"false"`, `"true"`, `0`, `1`, `""`, `[]`, `{}`} {
		for _, p := range []struct{ pfad, body string }{
			{"/werkzeuge/boards/b1/karten", `{"titel":"V797 U` + string(rune('a'+i)) + `","vertraulich":` + wert + `}`},
			{"/werkzeuge/boards/b1/termine", `{"titel":"V797 UT` + string(rune('a'+i)) + `","datum":"2026-10-21","vertraulich":` + wert + `}`},
		} {
			s, b := x.rufe(t, x.projekt, "", "POST", p.pfad, p.body)
			if s != http.StatusBadRequest || !strings.Contains(string(b), "vertraulich muss true oder false sein") {
				t.Errorf("%s %s: %d %s", p.pfad, p.body, s, b)
			}
		}
	}
	// MCP: Zeichenkette "false" → Werkzeugfehler, nichts angelegt.
	_, m := x.mcpWerkzeug(t, x.projekt, "", "karte_anlegen", `{"boardId":"b1","titel":"V797 U MCP","vertraulich":"false"}`)
	if fehler, _, _ := ergebnis753(t, m); !fehler {
		t.Error("MCP: \"false\" als Text wurde angenommen")
	}
	var stand string = x.stand(t, "b1")
	if strings.Contains(stand, "V797 U") {
		t.Error("trotz Ablehnung wurde etwas angelegt")
	}
}

// Das Feld gibt es nur innen: aus und außen lehnen es wie jedes unbekannte
// Feld ab; finding_anlegen kennt es nirgends (bleibt vertraulich).
func TestVertraulich797_NurInnenUndNurKarteTermin(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	for _, seite := range []string{api.BoardpaarSeiteAus, api.BoardpaarSeiteAussen} {
		e.seite(t, seite)
		for _, p := range []struct{ pfad, body string }{
			{"/werkzeuge/boards/b1/karten", `{"titel":"V797 N","vertraulich":false}`},
			{"/werkzeuge/boards/b1/termine", `{"titel":"V797 NT","datum":"2026-10-21","vertraulich":false}`},
		} {
			s, b := x.rufe(t, x.projekt, "", "POST", p.pfad, p.body)
			if s != http.StatusBadRequest || !strings.Contains(string(b), "unbekanntes Feld") {
				t.Errorf("%s %s: %d %s", seite, p.pfad, s, b)
			}
		}
	}
	e.seite(t, api.BoardpaarSeiteInnen)
	s, b := x.rufe(t, x.projekt, "", "POST", "/werkzeuge/boards/b1/findings", `{"titel":"V797 F","typ":"K","prioritaet":"P2","vertraulich":false}`)
	if s != http.StatusBadRequest || !strings.Contains(string(b), "unbekanntes Feld") {
		t.Errorf("finding_anlegen mit vertraulich: %d %s", s, b)
	}
	e.anlegen797(t, x.projekt, "", "/werkzeuge/boards/b1/findings", `{"titel":"V797 F2","typ":"K","prioritaet":"P2"}`)
	if v := e.vertraulich797(t, "V797 F2"); v != true {
		t.Errorf("Finding innen: %v", v)
	}
}

// Das Schema nennt das Feld nur innen (OpenAPI und MCP), mit der Folge.
func TestVertraulich797_SchemaNurInnen(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	eigenschaften := func(seite string) (openapi map[string]bool, mcp map[string]bool, text string) {
		t.Helper()
		e.seite(t, seite)
		openapi, mcp = map[string]bool{}, map[string]bool{}
		_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
		var spez struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]struct {
						Type        json.RawMessage `json:"type"`
						Description string          `json:"description"`
					} `json:"properties"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(b, &spez); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"KarteAnlegen", "TerminAnlegen"} {
			p, da := spez.Components.Schemas[n].Properties["vertraulich"]
			openapi[n] = da
			if da {
				if string(p.Type) != `"boolean"` {
					t.Errorf("%s/%s: Typ %q", seite, n, p.Type)
				}
				text = p.Description
			}
		}
		_, m := x.mcpRufe(t, x.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
		liste, _ := m["result"].(map[string]any)["tools"].([]any)
		for _, w := range liste {
			w := w.(map[string]any)
			if n, _ := w["name"].(string); n == "karte_anlegen" || n == "termin_anlegen" || n == "finding_anlegen" {
				schema, _ := w["inputSchema"].(map[string]any)
				props, _ := schema["properties"].(map[string]any)
				_, da := props["vertraulich"]
				mcp[n] = da
			}
		}
		return
	}
	for _, seite := range []string{api.BoardpaarSeiteAus, api.BoardpaarSeiteAussen} {
		o, m, _ := eigenschaften(seite)
		if o["KarteAnlegen"] || o["TerminAnlegen"] || m["karte_anlegen"] || m["termin_anlegen"] {
			t.Errorf("%s: Feld vertraulich im Schema: openapi %v mcp %v", seite, o, m)
		}
	}
	o, m, text := eigenschaften(api.BoardpaarSeiteInnen)
	if !o["KarteAnlegen"] || !o["TerminAnlegen"] || !m["karte_anlegen"] || !m["termin_anlegen"] {
		t.Errorf("innen: Feld fehlt: openapi %v mcp %v", o, m)
	}
	if m["finding_anlegen"] {
		t.Error("innen: finding_anlegen darf vertraulich nicht anbieten")
	}
	// K-6b: auch das OpenAPI-Schema von finding_anlegen nennt das Feld nicht.
	{
		_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
		var spez struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(b, &spez); err != nil {
			t.Fatal(err)
		}
		f, da := spez.Components.Schemas["FindingAnlegen"]
		if !da || len(f.Properties) == 0 {
			t.Fatal("Schema FindingAnlegen fehlt")
		}
		if _, hat := f.Properties["vertraulich"]; hat {
			t.Error("innen: FindingAnlegen (OpenAPI) nennt vertraulich")
		}
	}
	if !strings.Contains(text, "vollständig nach außen") || !strings.Contains(text, "ausdrückliches false") {
		t.Errorf("Feldtext ohne Folge: %q", text)
	}
}

// --- F-2230: Texte außen nach Stufe-1-Board und Kopie ------------------------

func (e *env789) hinweis797(t *testing.T) (hinweis string, kopie bool) {
	t.Helper()
	s, b := e.neu.rufe(t, e.neu.projekt, "", "GET", "/werkzeuge/boards/b1", "")
	if s != http.StatusOK {
		t.Fatalf("board_lesen: %d %.300s", s, b)
	}
	var m struct {
		Board struct {
			Boardpaar *struct {
				Hinweis string `json:"hinweis"`
				Kopie   bool   `json:"kopie"`
			} `json:"boardpaar"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Board.Boardpaar == nil {
		t.Fatalf("kein boardpaar: %v %.300s", err, b)
	}
	return m.Board.Boardpaar.Hinweis, m.Board.Boardpaar.Kopie
}

func TestVertraulich797_F2230_HinweisJeBoardart(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	ctx := context.Background()

	// Stufe-1-Board: unverändert, kein Feld kopie.
	h, kopie := e.hinweis797(t)
	if kopie || !strings.Contains(h, "Neue Karten gehören nach innen") || strings.Contains(h, "Kopie") {
		t.Errorf("Stufe 1: kopie=%v %q", kopie, h)
	}
	if _, b := e.neu.rufe(t, e.neu.projekt, "", "GET", "/werkzeuge/boards/b1", ""); strings.Contains(string(b), `"kopie"`) {
		t.Errorf("Stufe-1-Antwort trägt das Feld kopie: %.300s", b)
	}

	// Kopie, Schalter aus (Voreinstellung).
	if _, err := e.neu.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	h, kopie = e.hinweis797(t)
	if !kopie || !strings.Contains(h, "externe Kopie") || !strings.Contains(h, "echtem Titel") ||
		!strings.Contains(h, "Bearbeiten außen ist nicht erlaubt") || strings.Contains(h, "Bearbeiten außen ist erlaubt") {
		t.Errorf("Kopie, Schalter aus: kopie=%v %q", kopie, h)
	}

	// Kopie, Schalter an.
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	h, kopie = e.hinweis797(t)
	if !kopie || !strings.Contains(h, "Bearbeiten außen ist erlaubt") || !strings.Contains(h, "neue Karten in gespiegelten Swimlanes anlegen") ||
		strings.Contains(h, "Neue Karten gehören nach innen") || strings.Contains(h, "nicht erlaubt") {
		t.Errorf("Kopie, Schalter an: kopie=%v %q", kopie, h)
	}

	// Schalter wieder aus → zurück zum strengen Text.
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAus, false, "test"); err != nil {
		t.Fatal(err)
	}
	if h, _ = e.hinweis797(t); !strings.Contains(h, "nicht erlaubt") {
		t.Errorf("Schalter wieder aus: %q", h)
	}
}

func TestVertraulich797_F2230_GlobaleTexteNennenDieKopie(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	x := e.neu
	_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
	_, m := x.mcpRufe(t, x.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
	_, d := x.mcpRufe(t, x.projekt, "", `{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{`+meta753()+`}}`, nil)
	for wo, s := range map[string]string{"openapi": string(b), "tools/list": mcpText789(t, m), "discover": mcpText789(t, d)} {
		// Stufe 1 bleibt wörtlich; die Kopie hat ihren eigenen Hinweis.
		if !strings.Contains(s, "Neue Karten gehören nach innen") {
			t.Errorf("%s: Stufe-1-Satz fehlt", wo)
		}
		if !strings.Contains(s, "boardpaar.kopie") {
			t.Errorf("%s: kein Verweis auf boardpaar.kopie", wo)
		}
		// Die Gesamtbeschreibung steht in OpenAPI und discover; tools/list trägt
		// nur die Werkzeugzeilen (dort der Verweis auf boardpaar.kopie).
		if wo != "tools/list" && !strings.Contains(s, "außer auf einer externen Kopie: dort der echte Titel") {
			t.Errorf("%s: Titelregel der Kopie fehlt", wo)
		}
	}
}

// --- Nacharbeit #797 (Kritiker K-1…K-6) -----------------------------------------

// K-1: `vertraulich` gehört zum Wiederholungsschlüssel — ohne Kopf: anderer
// Wert = neue Anlage (nie „bereits angelegt“ für die offene Karte); mit
// gleichem Idempotency-Key und anderem Wert: 422. Die Antwort nennt den Stand.
func TestVertraulich797_K1_Wiederholungsschluessel(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten, termine := "/werkzeuge/boards/b1/karten", "/werkzeuge/boards/b1/termine"
	rufe := func(pfad, body string, kopf ...string) (int, map[string]any) {
		t.Helper()
		s, b := x.rufeMitKopf(t, x.projekt, "", "POST", pfad, body, kopf...)
		return s, json671(t, b)
	}
	zahl := func(m map[string]any) float64 { f, _ := m["displayId"].(float64); return f }

	// Ohne Kopf: false, dann true.
	s, offen := rufe(karten, `{"titel":"V797 K1 Karte","vertraulich":false}`)
	if s != 200 || offen["vertraulich"] != false {
		t.Fatalf("offen: %d %v", s, offen)
	}
	s, zu := rufe(karten, `{"titel":"V797 K1 Karte","vertraulich":true}`)
	if s != 200 || zu["wiederholt"] != nil || zu["vertraulich"] != true || zahl(zu) == zahl(offen) {
		t.Errorf("true nach false wurde als Wiederholung der offenen Karte behandelt: %d %v", s, zu)
	}
	// Dieselbe Angabe zweimal bleibt eine Wiederholung und nennt den Bestand.
	s, nochmal := rufe(karten, `{"titel":"V797 K1 Karte","vertraulich":false}`)
	if s != 200 || nochmal["wiederholt"] != true || nochmal["vertraulich"] != false || zahl(nochmal) != zahl(offen) {
		t.Errorf("Wiederholung von false: %d %v", s, nochmal)
	}
	s, nochmal = rufe(karten, `{"titel":"V797 K1 Karte"}`)
	if s != 200 || nochmal["wiederholt"] != true || nochmal["vertraulich"] != true || zahl(nochmal) != zahl(zu) {
		t.Errorf("Wiederholung ohne Angabe: %d %v", s, nochmal)
	}

	// Mit Kopf: gleicher Schlüssel, andere Angabe → 422, nichts geschrieben.
	if s, m := rufe(karten, `{"titel":"V797 K1 Kopf","vertraulich":false}`, "Idempotency-Key", "k797-a"); s != 200 || m["vertraulich"] != false {
		t.Fatalf("Kopf, false: %d %v", s, m)
	}
	vorher := x.stand(t, "b1")
	if s, m := rufe(karten, `{"titel":"V797 K1 Kopf","vertraulich":true}`, "Idempotency-Key", "k797-a"); s != http.StatusUnprocessableEntity {
		t.Errorf("Kopf, andere Angabe: %d %v", s, m)
	}
	if x.stand(t, "b1") != vorher {
		t.Error("trotz 422 geschrieben")
	}
	// Termin: dasselbe.
	if s, m := rufe(termine, `{"titel":"V797 K1 T","datum":"2026-10-21","vertraulich":false}`, "Idempotency-Key", "k797-t"); s != 200 || m["vertraulich"] != false {
		t.Fatalf("Termin Kopf, false: %d %v", s, m)
	}
	if s, m := rufe(termine, `{"titel":"V797 K1 T","datum":"2026-10-21"}`, "Idempotency-Key", "k797-t"); s != http.StatusUnprocessableEntity {
		t.Errorf("Termin Kopf, andere Angabe: %d %v", s, m)
	}
	s, t1 := rufe(termine, `{"titel":"V797 K1 T2","datum":"2026-10-22","vertraulich":false}`)
	s2, t2 := rufe(termine, `{"titel":"V797 K1 T2","datum":"2026-10-22","vertraulich":true}`)
	if s != 200 || s2 != 200 || t1["vertraulich"] != false || t2["vertraulich"] != true || t2["wiederholt"] != nil || t1["terminId"] == t2["terminId"] {
		t.Errorf("Termin ohne Kopf: %d %v / %d %v", s, t1, s2, t2)
	}
	if s, m := rufe(termine, `{"titel":"V797 K1 T2","datum":"2026-10-22","vertraulich":false}`); s != 200 || m["wiederholt"] != true || m["vertraulich"] != false {
		t.Errorf("Termin Wiederholung: %d %v", s, m)
	}

	// Schlüssel für alle bisherigen Aufrufe unverändert: Ein Aufruf ohne das
	// Feld und ein Aufruf mit true teilen sich den Schlüssel (ohne Kopf: Wiederholung).
	if s, m := rufe(karten, `{"titel":"V797 K1 Alt"}`); s != 200 || m["vertraulich"] != true {
		t.Fatalf("Alt: %d %v", s, m)
	}
	if s, m := rufe(karten, `{"titel":"V797 K1 Alt","vertraulich":true}`); s != 200 || m["wiederholt"] != true {
		t.Errorf("true gleicht ohne Angabe: %d %v", s, m)
	}

	// Auf „aus“ trägt die Antwort das Feld nicht.
	e.seite(t, api.BoardpaarSeiteAus)
	if s, m := rufe(karten, `{"titel":"V797 K1 Aus"}`); s != 200 {
		t.Fatalf("aus: %d %v", s, m)
	} else if _, da := m["vertraulich"]; da {
		t.Errorf("aus: Antwort trägt vertraulich: %v", m)
	}
}

// K-2: Spiegel tragen eine gewöhnliche Nummer, i-<nr> sind Schatten.
func TestVertraulich797_K2_SpiegelNichtSchatten(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	ctx := context.Background()
	if _, err := e.neu.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	pruefe := func(wer, text string) {
		t.Helper()
		for _, falsch := range []string{"i-12 sind Spiegel", "wie i-12", "Nummern wie i-"} {
			if strings.Contains(text, falsch) {
				t.Errorf("%s: verwechselt Schatten mit Spiegeln (%q): %s", wer, falsch, text)
			}
		}
		if !strings.Contains(text, "Spiegel") || !strings.Contains(text, "(#N)") || !strings.Contains(text, "Schatten i-<nr>") {
			t.Errorf("%s: nennt #N für Spiegel und i-<nr> für Schatten nicht: %s", wer, text)
		}
	}
	h, _ := e.hinweis797(t)
	pruefe("Kopie aus", h)
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	h, _ = e.hinweis797(t)
	pruefe("Kopie an", h)

	// Gesamttexte: Die Ausnahme der Kopie nennt die richtige Zuordnung.
	x := e.neu
	_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
	if s := string(b); !strings.Contains(s, "Spiegel mit gewöhnlicher Nummer #N") || !strings.Contains(s, "nur vertrauliche als Schatten i-<nr>") {
		t.Error("openapi: Ausnahme der Kopie ohne richtige Zuordnung")
	}
}

// K-3: Ist der Schalter boardpaar.rueckweg nicht lesbar (Lesefehler, nicht
// fehlender Schlüssel), nennt der Hinweis der Kopie „aus“ (der strengere Text).
// Hinweis (V-2): Die Richtung „unlesbar heißt aus“ sichert dieser Test nur
// zusammen mit dem Leser: BoardpaarRueckwegAnMitFehler liefert bei Lesefehler
// schon false (TestVertraulich797_LeserLiefertBeiFehlerAus); die Aufrufstelle
// in werkzeugBoardpaare gibt nur weiter und würde eine Umkehr im Leser nicht
// selbst fangen.
func TestVertraulich797_K3_SchalterUnlesbarGiltAls_Aus(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteAussen)
	ctx := context.Background()
	if _, err := e.neu.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	if h, _ := e.hinweis797(t); strings.Contains(h, "nicht erlaubt") {
		t.Fatalf("Vorprobe: Schalter an, Text %q", h)
	}
	// Lesefehler nur für diesen Schlüssel: als Geheimnis markiert, das Repo
	// des Servers hat keine Crypto-Box (ErrSecretsUnavailable, kein NotFound).
	if _, err := e.neu.db.ExecContext(ctx, `UPDATE server_config SET is_secret = 1, value_plain = NULL, value_encrypted = 'x' WHERE key = ?`, api.KonfigBoardpaarRueckweg); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cfg.Get(ctx, api.KonfigBoardpaarRueckweg); err == nil {
		t.Fatal("Vorbedingung: der Schalter ist noch lesbar")
	}
	if h, _ := e.hinweis797(t); !strings.Contains(h, "Bearbeiten außen ist nicht erlaubt") || strings.Contains(h, "ist erlaubt") {
		t.Errorf("unlesbarer Schalter: %q", h)
	}
}

// K-4: Der interne Agent auf einer Kopie bekommt den Kopie-Text, nicht den
// Stufe-1-Satz über „eine externe Karte“, und mit ausgeschaltetem Bearbeiten
// außen keine Zusage, Spiegel zu ändern.
func TestVertraulich797_K4_InternerAgentAufKopie(t *testing.T) {
	e := newEnv783(t)
	ctx := context.Background()
	hinweis := func() string {
		t.Helper()
		s, b := e.alsAgent(t, e.assistent, "GET", "/intern/b1/board", "")
		var m struct {
			Board struct {
				Boardpaar struct {
					Hinweis string `json:"hinweis"`
					Kopie   bool   `json:"kopie"`
				} `json:"boardpaar"`
			} `json:"board"`
		}
		if err := json.Unmarshal(b, &m); err != nil || s != http.StatusOK {
			t.Fatalf("%d %.300s (%v)", s, b, err)
		}
		return m.Board.Boardpaar.Hinweis
	}
	// Stufe 1: unverändert der bisherige Text.
	if h := hinweis(); !strings.Contains(h, "Spalte und Priorität einer externen Karte änderst du hier sofort") || strings.Contains(h, "Kopie") {
		t.Errorf("Stufe 1: %q", h)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	h := hinweis()
	if !strings.Contains(h, "externen Kopie") || !strings.Contains(h, "Bearbeiten außen ist nicht erlaubt") ||
		strings.Contains(h, "einer externen Karte") || !strings.Contains(h, "Schatten i-<nr>") || !strings.Contains(h, "(#N)") ||
		!strings.Contains(h, "Vorschläge zu Spiegeln und neue Karten im gespiegelten Backlog lassen sich") || !strings.Contains(h, "nicht bestätigen") {
		t.Errorf("Kopie aus: %q", h)
	}
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	h = hinweis()
	if !strings.Contains(h, "Bearbeiten außen ist erlaubt") || strings.Contains(h, "nicht erlaubt") || !strings.Contains(h, "Schatten i-<nr>") ||
		!strings.Contains(h, "Löschen von Spiegeln bleibt gesperrt") || strings.Contains(h, "nicht bestätigen") {
		t.Errorf("Kopie an: %q", h)
	}
}

// V-4 (Verify #797): Der interne Agent auf einer Kopie ändert Spalte und
// Priorität eines Spiegels bei „Bearbeiten außen: an“ sofort (200); bei „aus“
// lehnt der Server ab (422, Spiegel-Schranke). Der Hinweistext sagt dasselbe.
func TestVertraulich797_V4_InternerAgentSpiegelFelder(t *testing.T) {
	e := newEnv783(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `UPDATE boardpaar_aussen SET kopie = 1, bearbeitbar = 1 WHERE aussen_board_id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	kennung := strings.Repeat("S", 43)
	k := &schatten.Kopie{
		Struktur: schatten.Struktur{Lanes: []schatten.StrukturLane{{ID: schatten.BacklogLane, Titel: "Backlog", Kind: "backlog"}},
			Spalten: []schatten.StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}, {ID: "col-ready", Titel: "Ready"}}},
		Spiegel: []schatten.SpiegelEingang{{SpiegelID: kennung, Karte: &schatten.SpiegelKarte{Art: schatten.ArtKarte,
			Lane: schatten.BacklogLane, Spalte: "col-backlog", Title: "Spiegel", Labels: []string{}, Assignees: []string{},
			Comments: []schatten.SpiegelKommentar{}}}}}
	if _, err := storage.NewSchattenRepo(e.db).ErsetzeKopie(ctx, "b1", func(state []byte) ([]byte, error) {
		neu, _, err := schatten.KopieAnwenden(state, k)
		return neu, err
	}, nil); err != nil {
		t.Fatal(err)
	}
	id := schatten.AbgeleiteteID(kennung)
	var nr int
	if err := e.db.QueryRow(`SELECT COALESCE(json_extract(c.value, '$.displayId'), 0) FROM boards, json_each(boards.state_json, '$.board.sprints') s, json_each(s.value, '$.cards') c WHERE boards.id = 'b1' AND json_extract(c.value, '$.id') = ?`, id).Scan(&nr); err != nil || nr == 0 {
		t.Fatalf("Spiegel ohne Nummer: %d %v", nr, err)
	}
	pfad := "/intern/b1/karte/" + strconv.Itoa(nr) + "/felder"
	body := `{"spalte":"Ready","prioritaet":"hoch"}`

	// aus (Standard): Spiegel-Schranke.
	if s, b := e.alsAgent(t, e.assistent, "POST", pfad, body); s != http.StatusUnprocessableEntity {
		t.Fatalf("aus: %d %s", s, b)
	}
	if got := e.karte783(t, "b1", id); got["columnId"] != "col-backlog" || got["priority"] != nil {
		t.Fatalf("aus: Spiegel geändert: %v", got)
	}
	// an: sofort.
	if err := e.cfg.Set(ctx, api.KonfigBoardpaarRueckweg, api.BoardpaarRueckwegAn, false, "test"); err != nil {
		t.Fatal(err)
	}
	if s, b := e.alsAgent(t, e.assistent, "POST", pfad, body); s != http.StatusOK {
		t.Fatalf("an: %d %s", s, b)
	}
	if got := e.karte783(t, "b1", id); got["columnId"] != "col-ready" || got["priority"] != "hoch" {
		t.Fatalf("an: Spiegel nicht geändert: %v", got)
	}
}

// V-2: Der Leser selbst liefert bei einem Lesefehler false UND den Fehler —
// die Aufrufstelle des Werkzeug-Hinweises verlässt sich darauf.
func TestVertraulich797_LeserLiefertBeiFehlerAus(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "kaputt.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" })
	an, err := api.BoardpaarRueckwegAnMitFehler(ctx, cfg)
	if err == nil {
		t.Fatal("Testaufbau: Schalter ist lesbar, der Test prüfte nichts")
	}
	if an {
		t.Error("Lesefehler: der Leser liefert true statt false")
	}
}

// V-5e (Verify #797): Ist boardpaar.seite nicht lesbar, wird das Feld
// `vertraulich` zwar noch angenommen (postOps lehnt Schreibversuche ohnehin
// mit 503 ab), die Antwort einer erkannten Wiederholung gibt es aber nicht
// aus — auf der Seite „aus“ gibt es das Feld nicht.
func TestVertraulich797_V5e_SeiteUnlesbarKeineAusgabe(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	ctx := context.Background()
	karten := "/werkzeuge/boards/b1/karten"
	rufe := func(body string) (int, map[string]any) {
		t.Helper()
		s, b := x.rufeMitKopf(t, x.projekt, "", "POST", karten, body, "Idempotency-Key", "v797-5e")
		return s, json671(t, b)
	}
	if s, m := rufe(`{"titel":"V797 5e","vertraulich":false}`); s != 200 || m["vertraulich"] != false {
		t.Fatalf("Anlage: %d %v", s, m)
	}
	if s, m := rufe(`{"titel":"V797 5e","vertraulich":false}`); s != 200 || m["wiederholt"] != true || m["vertraulich"] != false {
		t.Fatalf("Vorprobe Wiederholung (Seite lesbar): %d %v", s, m)
	}
	if _, err := e.neu.db.ExecContext(ctx, `UPDATE server_config SET is_secret = 1, value_plain = NULL, value_encrypted = 'x' WHERE key = ?`, api.KonfigBoardpaarSeite); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cfg.Get(ctx, api.KonfigBoardpaarSeite); err == nil {
		t.Fatal("Vorbedingung: die Seite ist noch lesbar")
	}
	s, m := rufe(`{"titel":"V797 5e","vertraulich":false}`)
	if s != 200 || m["wiederholt"] != true {
		t.Fatalf("Wiederholung bei unlesbarer Seite: %d %v", s, m)
	}
	if _, da := m["vertraulich"]; da {
		t.Errorf("unlesbare Seite: Antwort trägt vertraulich: %v", m)
	}
}

// V-5d: Die Ergebnisschemata nennen `vertraulich` nur innen; auf „aus“ und
// außen bleibt /openapi.json byte-gleich (TestBoardpaar789_WerkzeugtexteJeSeite
// vergleicht „aus“ gegen den eingecheckten Abzug).
func TestVertraulich797_V5d_ErgebnisschemaNurInnen(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	hat := func(seite string) map[string]bool {
		t.Helper()
		e.seite(t, seite)
		_, b := x.rufe(t, x.projekt, "", "GET", "/openapi.json", "")
		var o struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(b, &o); err != nil {
			t.Fatal(err)
		}
		r := map[string]bool{}
		for _, n := range []string{"KarteErgebnis", "TerminErgebnis"} {
			s, da := o.Components.Schemas[n]
			if !da {
				t.Fatalf("%s: Schema %s fehlt", seite, n)
			}
			_, r[n] = s.Properties["vertraulich"]
		}
		return r
	}
	if r := hat(api.BoardpaarSeiteInnen); !r["KarteErgebnis"] || !r["TerminErgebnis"] {
		t.Errorf("innen: %v", r)
	}
	for _, seite := range []string{api.BoardpaarSeiteAussen, api.BoardpaarSeiteAus} {
		if r := hat(seite); r["KarteErgebnis"] || r["TerminErgebnis"] {
			t.Errorf("%s: %v", seite, r)
		}
	}
}

// F-2234: Scheitert nach dem Anlegen das Zusatzlesen des Board-Stands, sagt die
// Antwort `vertraulich_unbekannt: true` und trägt kein `vertraulich`. Der
// Fehlschlag wird in der eigenen Test-Datenbank nachgebaut: Ein Trigger macht
// den gespeicherten Stand nach jedem Schreiben unlesbar. Die Karte ist
// angelegt (das Schreiben selbst gelang), nur das Nachlesen scheitert.
func TestVertraulich797_LeseFehlerNachAnlegen_Unbekannt(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten := "/werkzeuge/boards/b1/karten"

	// Vorher (Gegenstück): Das Nachlesen gelingt, `vertraulich` steht da, kein Ausfallzeichen.
	s, b := x.rufe(t, x.projekt, "", "POST", karten, `{"titel":"V2234 heil"}`)
	var heil map[string]any
	if s != http.StatusOK || json.Unmarshal(b, &heil) != nil {
		t.Fatalf("heil: %d %s", s, b)
	}
	if _, da := heil["vertraulich_unbekannt"]; da || heil["vertraulich"] != true {
		t.Fatalf("heil: vertraulich/-_unbekannt falsch: %s", b)
	}

	if _, err := x.db.Exec(`CREATE TRIGGER stand_kaputt AFTER UPDATE OF state_json ON boards
		BEGIN UPDATE boards SET state_json = '{kaputt' WHERE id = NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	s, b = x.rufe(t, x.projekt, "", "POST", karten, `{"titel":"V2234 kaputt"}`)
	var m map[string]any
	if s != http.StatusOK || json.Unmarshal(b, &m) != nil {
		t.Fatalf("kaputt: %d %s", s, b)
	}
	if m["vertraulich_unbekannt"] != true {
		t.Errorf("vertraulich_unbekannt fehlt: %s", b)
	}
	if v, da := m["vertraulich"]; da {
		t.Errorf("vertraulich darf bei unlesbarem Stand nicht stehen, steht aber da: %v", v)
	}
	if !strings.Contains(m["ergebnis"].(string), "angelegt") {
		t.Errorf("Anlage nicht gemeldet: %s", b)
	}
}
