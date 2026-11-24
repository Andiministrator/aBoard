package api

// Karte #752 (ADR-0139 §1): Filter „delegierend erlaubt“ und die
// Form-Prüfung des Katalogs — an einem eigenen kleinen Katalog, weil heute
// alle Werkzeuge für Open WebUI freigegeben sind und der Filter am echten
// Katalog nichts zeigen würde.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func probeKatalog752() ([]werkzeugEintrag, map[string]json.RawMessage) {
	h := func(a *BoardsAPI) http.HandlerFunc { return func(http.ResponseWriter, *http.Request) {} }
	katalog := []werkzeugEintrag{
		{Name: "lesen_frei", Methode: http.MethodGet, Muster: "/werkzeuge/boards/{boardId}", Zusammenfassung: "z", Beschreibung: "b",
			Parameter: []werkzeugParameter{{Name: "boardId", Ort: "path", Pflicht: true, Beschreibung: "id", Schema: json.RawMessage(`{"type":"string"}`)}},
			Antwort:   "Ergebnis", AntwortText: "ok", Stufe: stufeLesen, Zugaenge: zugangOpenAPI | zugangMCP, DelegierendErlaubt: true, handler: h},
		{Name: "schreiben_gesperrt", Methode: http.MethodPost, Muster: "/werkzeuge/boards/{boardId}/x", Zusammenfassung: "z", Beschreibung: "b",
			Parameter: []werkzeugParameter{{Name: "boardId", Ort: "path", Pflicht: true, Beschreibung: "id", Schema: json.RawMessage(`{"type":"string"}`)}},
			Eingabe:   "NurHier", Antwort: "Ergebnis", AntwortText: "ok", Stufe: stufeSchreiben, Zugaenge: zugangOpenAPI | zugangMCP, handler: h},
		{Name: "nur_mcp", Methode: http.MethodGet, Muster: "/werkzeuge/y", Zusammenfassung: "z", Beschreibung: "b",
			Antwort: "Ergebnis", AntwortText: "ok", Stufe: stufeLesen, Zugaenge: zugangMCP, DelegierendErlaubt: true, handler: h},
	}
	schemas := map[string]json.RawMessage{
		"Ergebnis":  json.RawMessage(`{"type":"object","properties":{"link":{"$ref":"#/components/schemas/Link"}}}`),
		"Link":      json.RawMessage(`{"type":["string","null"]}`),
		"NurHier":   json.RawMessage(`{"type":"object","properties":{"geheim":{"type":"string"}}}`),
		"Unbenutzt": json.RawMessage(`{"type":"string"}`),
	}
	return katalog, schemas
}

func TestWerkzeugSpez752_FilterUndSchemata(t *testing.T) {
	katalog, schemas := probeKatalog752()
	lies := func(zeige func(werkzeugEintrag) bool) (wege []string, namen map[string]bool) {
		roh, err := werkzeugSpezErzeugen(katalog, schemas, zeige)
		if err != nil {
			t.Fatal(err)
		}
		var spez struct {
			Paths      map[string]map[string]json.RawMessage `json:"paths"`
			Components struct {
				Schemas map[string]json.RawMessage `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(roh, &spez); err != nil {
			t.Fatal(err)
		}
		for p, ops := range spez.Paths {
			for m := range ops {
				wege = append(wege, strings.ToUpper(m)+" "+p)
			}
		}
		namen = map[string]bool{}
		for n := range spez.Components.Schemas {
			namen[n] = true
		}
		return wege, namen
	}
	// Alle: beide OpenAPI-Werkzeuge, nicht das reine MCP-Werkzeug; Schemata
	// nur, was gebraucht wird (Link über den Verweis in Ergebnis).
	wege, namen := lies(func(werkzeugEintrag) bool { return true })
	if len(wege) != 2 || namen["Unbenutzt"] || !namen["Link"] || !namen["NurHier"] || !namen["Ergebnis"] {
		t.Fatalf("alle: Wege %v, Schemata %v", wege, namen)
	}
	// Delegierend: nur das freigegebene; sein Eingabe-Schema fehlt mit.
	wege, namen = lies(func(e werkzeugEintrag) bool { return e.DelegierendErlaubt })
	if len(wege) != 1 || wege[0] != "GET /werkzeuge/boards/{boardId}" || namen["NurHier"] {
		t.Fatalf("delegierend: Wege %v, Schemata %v", wege, namen)
	}
}

func TestWerkzeugKatalog752_Pruefung(t *testing.T) {
	if err := werkzeugKatalogPruefen(werkzeugKatalog, werkzeugSchemas); err != nil {
		t.Fatalf("echter Katalog: %v", err)
	}
	faelle := map[string]func(k []werkzeugEintrag, s map[string]json.RawMessage){
		"Name doppelt": func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[1].Name = k[0].Name },
		"Route doppelt": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			k[2].Muster, k[2].Methode = k[0].Muster, k[0].Methode
		},
		"lesen über POST":    func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[1].Stufe = stufeLesen },
		"schreiben über GET": func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Stufe = stufeSchreiben },
		"Stufe unbekannt":    func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Stufe = "alles" },
		"kein Zugang":        func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Zugaenge = 0 },
		"kein Handler":       func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].handler = nil },
		"Antwort fehlt":      func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Antwort = "Gibtsnicht" },
		"Parameter als $ref": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			k[0].Parameter[0].Schema = json.RawMessage(`{"$ref":"#/x"}`)
		},
		"Parameter ohne Text":  func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Parameter[0].Beschreibung = " " },
		"Pfad-Parameter fehlt": func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Parameter = nil },
		"Route außerhalb":      func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[2].Muster = "/boards/y" },
		"DELETE":               func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[1].Methode = http.MethodDelete },
		"Body bei GET":         func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Eingabe = "NurHier" },
		"Beschreibung fehlt":   func(k []werkzeugEintrag, s map[string]json.RawMessage) { k[0].Beschreibung = "" },
		"Schema kein JSON":     func(k []werkzeugEintrag, s map[string]json.RawMessage) { s["Unbenutzt"] = json.RawMessage(`{`) },
		// Sitzungs-QA 08.10. (K-1): das MCP-Eingabe-Schema wird schon hier gebaut.
		"Body mit $ref (MCP)": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			s["NurHier"] = json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/components/schemas/Link"}}}`)
		},
		"Feld in Pfad und Body": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			s["NurHier"] = json.RawMessage(`{"type":"object","properties":{"boardId":{"type":"string"}}}`)
		},
		"Body kein Objekt (MCP)": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			s["NurHier"] = json.RawMessage(`{"type":"string"}`)
		},
		"Parameter-Schema kein Objekt": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			k[0].Parameter[0].Schema = json.RawMessage(`"string"`)
		},
		"Parameter doppelt": func(k []werkzeugEintrag, s map[string]json.RawMessage) {
			k[0].Parameter = append(k[0].Parameter, werkzeugParameter{Name: "boardId", Ort: "query", Beschreibung: "x", Schema: json.RawMessage(`{"type":"string"}`)})
		},
	}
	for name, kaputt := range faelle {
		k, s := probeKatalog752()
		if err := werkzeugKatalogPruefen(k, s); err != nil {
			t.Fatalf("Probe-Katalog vor %q: %v", name, err)
		}
		kaputt(k, s)
		if err := werkzeugKatalogPruefen(k, s); err == nil {
			t.Errorf("%s: nicht erkannt", name)
		}
	}
	// Verweis auf ein fehlendes Schema fällt beim Erzeugen auf.
	k, s := probeKatalog752()
	s["Ergebnis"] = json.RawMessage(`{"$ref":"#/components/schemas/Fehlt"}`)
	if _, err := werkzeugSpezErzeugen(k, s, func(werkzeugEintrag) bool { return true }); err == nil {
		t.Error("Verweis auf fehlendes Schema nicht erkannt")
	}
}

// Welche Fassung ein Principal bekommt — am Probe-Katalog, wo sie sich
// unterscheiden (am echten Katalog sind heute beide gleich).
func TestWerkzeugSpez752_FassungJePrincipal(t *testing.T) {
	katalog, schemas := probeKatalog752()
	spezen, err := werkzeugSpezenErzeugen(katalog, schemas)
	if err != nil {
		t.Fatal(err)
	}
	enthaelt := func(b []byte) bool { return strings.Contains(string(b), "schreiben_gesperrt") }
	if enthaelt(spezen.fuer(&AgentPrincipal{Delegiert: true, OnBehalfOf: "anna@firma.de"})) {
		t.Error("delegierend sieht ein nicht freigegebenes Werkzeug")
	}
	for _, p := range []*AgentPrincipal{{BoardID: "b1"}, {BoardID: "b1", OnBehalfOf: "anna@firma.de"}} {
		if !enthaelt(spezen.fuer(p)) {
			t.Errorf("%+v sieht nicht alle Werkzeuge", p)
		}
	}
}

// Karte #753: Die Positivliste lässt /mcp genau mit POST, GET und DELETE zu
// (GET/DELETE beantwortet der Handler mit 405) — unabhängig davon, dass der
// Mux andere Methoden ohnehin abweist; er ist die zweite Hürde, nicht die
// erste. Vorschlags-Tokens nie.
func TestWerkzeugDarf753_MCP(t *testing.T) {
	segs, _ := agentSegmente("/mcp")
	for _, p := range []*AgentPrincipal{
		{Name: "open-webui", Delegiert: true, tokenRolle: "editor"},
		{Name: "projekt", BoardID: "b1", Role: "viewer", tokenRolle: "viewer"},
		{Name: "person", BoardID: "b1", Role: "editor", OnBehalfOf: "anna@firma.de", tokenRolle: "editor"},
	} {
		for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			soll := m == http.MethodPost || m == http.MethodGet || m == http.MethodDelete
			if ist := werkzeugDarf(p, m, segs); ist != soll {
				t.Errorf("%s %s /mcp: %v, erwartet %v", p.Name, m, ist, soll)
			}
		}
	}
	vorschlag := &AgentPrincipal{Name: "recherche", BoardID: "b1", Role: "vorschlag", tokenRolle: "vorschlag"}
	if werkzeugDarf(vorschlag, http.MethodPost, segs) {
		t.Error("Vorschlags-Token darf /mcp")
	}
	for _, pfad := range []string{"/mcp/x", "/mcp/werkzeuge/boards/b1"} {
		s, _ := agentSegmente(pfad)
		if werkzeugDarf(&AgentPrincipal{Name: "p", BoardID: "b1", Role: "editor", tokenRolle: "editor"}, http.MethodPost, s) {
			t.Errorf("%s erlaubt", pfad)
		}
	}
}
