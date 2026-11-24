package api

// agent_werkzeug_katalog.go — der Werkzeug-Katalog (ADR-0139 §1, Karte #752)
// und die daraus erzeugte OpenAPI-Beschreibung.
//
// Ein Eintrag beschreibt ein Werkzeug vollständig: Name (= operationId),
// Route, Texte für das Modell, Parameter und Schemata, Stufe, Zugänge,
// „delegierend erlaubt“ und den Handler. Die Einträge selbst stehen in
// agent_werkzeug_katalog_daten.go.
//
// Was der Katalog NICHT entscheidet: Rechte. Durchgesetzt wird allein von der
// handgeschriebenen Positivliste werkzeugDarf. Aus dem Katalog die Rechte zu
// erzeugen, wäre verworfen (ADR-0139 „Verworfen“, K-5): Positivliste,
// Beschreibung und Matrix hätten dann eine gemeinsame Quelle, und der Test
// prüfte nur sich selbst.
//
// Die Beschreibung hängt am Token: Ein delegierendes Token (Open WebUI) sieht
// nur Werkzeuge mit DelegierendErlaubt, Projekt- und Personen-Tokens alle mit
// Zugang openapi (ADR-0139 §1). Beide Fassungen entstehen einmal beim ersten
// Abruf; der Katalog ändert sich zur Laufzeit nicht.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// werkzeugStufe: was ein Werkzeug tut. Steuert die Anzeige und ab Stufe 2 die
// MCP-Hinweise (readOnlyHint, destructiveHint …), nicht die Rechte.
type werkzeugStufe string

const (
	stufeLesen     werkzeugStufe = "lesen"
	stufeSchreiben werkzeugStufe = "schreiben"
	stufeLoeschen  werkzeugStufe = "loeschen"
	stufeVerwalten werkzeugStufe = "verwalten"
)

// werkzeugZugang: über welche Eingänge ein Werkzeug angeboten wird (Bitmaske).
type werkzeugZugang uint8

const (
	zugangOpenAPI werkzeugZugang = 1 << iota
	zugangMCP
)

// werkzeugParameter: ein Pfad- oder Query-Parameter. Schema steht inline,
// nie als $ref (Open WebUI v0.11.4 liest bei Parametern nur name/schema).
type werkzeugParameter struct {
	Name, Ort, Beschreibung string // Ort: "path" | "query"
	Pflicht                 bool
	Schema                  json.RawMessage
}

// werkzeugEintrag: ein Werkzeug im Katalog.
type werkzeugEintrag struct {
	Name            string // operationId; für Open WebUI im Team-Betrieb unveränderlich (T-4)
	Methode, Muster string // Route (ServeMux-Muster ohne Methode)
	Zusammenfassung string
	Beschreibung    string // für das Modell
	Parameter       []werkzeugParameter
	Eingabe         string // Schema-Name des Bodys in werkzeugSchemas; leer = kein Body
	Antwort         string // Schema-Name der Antwort (200, application/json)
	AntwortText     string // description der Antwort 200
	AntwortHTML     bool   // zusätzlich text/html (karte_zeigen)
	Stufe           werkzeugStufe
	Zugaenge        werkzeugZugang
	// DelegierendErlaubt: Open WebUI (delegierendes Token) sieht das Werkzeug.
	// Standard nein; jedes neue Werkzeug wird für den Assistenten einzeln mit einem anderen Projekt und
	// dem Maintainer freigegeben (ADR-0139 §7). Nur Anzeige — ob ein delegierendes
	// Token es aufrufen DARF, entscheidet werkzeugDarf.
	DelegierendErlaubt bool
	// Paar (Karte #757, ADR-0139 E-2): bei …_loeschen_pruefen der Name des
	// zerstörerischen Partners. Das Prüf-Werkzeug ist lesend (readOnlyHint),
	// angezeigt wird es aber nur, wo der Partner angezeigt wird — ein Code,
	// den man nicht einlösen darf, hilft niemandem.
	Paar string
	// NurOwner (Karte #757, E-3): Board-Struktur (board_aendern, Spalten
	// löschen) — nur für ein Token mit Person, nie delegierend. Ob die Person
	// owner ist, prüft der Handler je Aufruf (ACL); die Anzeige kennt nur die
	// Obergrenze.
	NurOwner bool
	handler  func(a *BoardsAPI) http.HandlerFunc
}

// werkzeugNachName: der Eintrag dieses Namens, sonst nil.
func werkzeugNachName(katalog []werkzeugEintrag, name string) *werkzeugEintrag {
	for i := range katalog {
		if katalog[i].Name == name {
			return &katalog[i]
		}
	}
	return nil
}

// werkzeugSpezRoute: die Route der Beschreibung selbst (kein Werkzeug).
const werkzeugSpezRoute = "/openapi.json"

// werkzeugKatalogPruefen prüft den Katalog auf innere Widersprüche — keine
// Rechte, nur Form. Läuft beim Erzeugen der Beschreibung und im Test.
func werkzeugKatalogPruefen(katalog []werkzeugEintrag, schemas map[string]json.RawMessage) error {
	namen := map[string]bool{}
	wege := map[string]bool{}
	for _, e := range katalog {
		switch {
		case e.Name == "":
			return fmt.Errorf("Werkzeug ohne Namen (%s %s)", e.Methode, e.Muster)
		case namen[e.Name]:
			return fmt.Errorf("%s: Name doppelt", e.Name)
		case wege[e.Methode+" "+e.Muster]:
			return fmt.Errorf("%s: Route %s %s doppelt", e.Name, e.Methode, e.Muster)
		case e.Methode != http.MethodGet && e.Methode != http.MethodPost:
			return fmt.Errorf("%s: Methode %s — Werkzeuge kennen nur GET und POST", e.Name, e.Methode)
		case !strings.HasPrefix(e.Muster, "/"+werkzeugSegment+"/"):
			return fmt.Errorf("%s: Route %s liegt nicht unter /%s/", e.Name, e.Muster, werkzeugSegment)
		case strings.TrimSpace(e.Zusammenfassung) == "" || strings.TrimSpace(e.Beschreibung) == "":
			return fmt.Errorf("%s: Zusammenfassung oder Beschreibung fehlt", e.Name)
		case e.Stufe != stufeLesen && e.Stufe != stufeSchreiben && e.Stufe != stufeLoeschen && e.Stufe != stufeVerwalten:
			return fmt.Errorf("%s: Stufe %q unbekannt", e.Name, e.Stufe)
		case e.Stufe == stufeLesen && e.Methode != http.MethodGet:
			return fmt.Errorf("%s: Stufe lesen, aber Methode %s", e.Name, e.Methode)
		case e.Stufe != stufeLesen && e.Methode == http.MethodGet:
			return fmt.Errorf("%s: Stufe %s über GET", e.Name, e.Stufe)
		case e.Zugaenge == 0:
			return fmt.Errorf("%s: kein Zugang", e.Name)
		case e.Stufe == stufeVerwalten && !e.NurOwner:
			return fmt.Errorf("%s: Stufe verwalten nur mit NurOwner", e.Name)
		case e.NurOwner && e.DelegierendErlaubt:
			return fmt.Errorf("%s: NurOwner verträgt kein delegierendes Token", e.Name)
		case e.Paar != "" && e.Stufe != stufeLesen:
			return fmt.Errorf("%s: ein Prüf-Werkzeug (Paar) ist lesend", e.Name)
		case e.handler == nil:
			return fmt.Errorf("%s: kein Handler", e.Name)
		case e.Antwort == "" || schemas[e.Antwort] == nil:
			return fmt.Errorf("%s: Antwort-Schema %q fehlt", e.Name, e.Antwort)
		case e.Eingabe != "" && schemas[e.Eingabe] == nil:
			return fmt.Errorf("%s: Eingabe-Schema %q fehlt", e.Name, e.Eingabe)
		case (e.Eingabe != "") != (e.Methode == http.MethodPost):
			return fmt.Errorf("%s: Body genau bei POST", e.Name)
		}
		namen[e.Name] = true
		wege[e.Methode+" "+e.Muster] = true
		if e.Paar != "" {
			partner := werkzeugNachName(katalog, e.Paar)
			if partner == nil || partner.Stufe != stufeLoeschen || partner.Muster != e.Muster ||
				partner.NurOwner != e.NurOwner || partner.DelegierendErlaubt != e.DelegierendErlaubt || partner.Zugaenge != e.Zugaenge {
				return fmt.Errorf("%s: Partner %s fehlt oder passt nicht (Stufe loeschen, gleiche Route, gleiche Freigaben)", e.Name, e.Paar)
			}
		}
		pfadParam := map[string]bool{}
		for _, seg := range strings.Split(e.Muster, "/") {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				pfadParam[strings.Trim(seg, "{}")] = true
			}
		}
		for _, pa := range e.Parameter {
			if strings.TrimSpace(pa.Beschreibung) == "" {
				return fmt.Errorf("%s: Parameter %s ohne Beschreibung", e.Name, pa.Name)
			}
			if bytes.Contains(pa.Schema, []byte(`"$ref"`)) {
				return fmt.Errorf("%s: Parameter %s als $ref", e.Name, pa.Name)
			}
			if !json.Valid(pa.Schema) {
				return fmt.Errorf("%s: Parameter %s: Schema ist kein JSON", e.Name, pa.Name)
			}
			switch pa.Ort {
			case "path":
				if !pa.Pflicht || !pfadParam[pa.Name] {
					return fmt.Errorf("%s: Pfad-Parameter %s passt nicht zur Route", e.Name, pa.Name)
				}
				delete(pfadParam, pa.Name)
			case "query":
			default:
				return fmt.Errorf("%s: Parameter %s: Ort %q", e.Name, pa.Name, pa.Ort)
			}
		}
		if len(pfadParam) > 0 {
			return fmt.Errorf("%s: Pfad-Parameter ohne Beschreibung: %v", e.Name, pfadParam)
		}
		namenParam := map[string]bool{}
		for _, pa := range e.Parameter {
			if namenParam[pa.Name] {
				return fmt.Errorf("%s: Parameter %s doppelt", e.Name, pa.Name)
			}
			namenParam[pa.Name] = true
		}
		// MCP: das zusammengesetzte Eingabe-Schema schon hier bauen, damit ein
		// Fehler darin ($ref, Feld in Pfad und Body, kein Objekt) den Start
		// anhält und nicht erst den ersten /mcp-Aufruf mit 500 beantwortet
		// (Sitzungs-QA 08.10., K-1).
		if e.Zugaenge&zugangMCP != 0 {
			if _, err := mcpEingabeSchema(e, schemas); err != nil {
				return err
			}
		}
	}
	for name, s := range schemas {
		if !json.Valid(s) {
			return fmt.Errorf("Schema %s ist kein JSON", name)
		}
	}
	return nil
}

// werkzeugSpezErzeugen baut die OpenAPI-3.1-Beschreibung aus den Einträgen,
// die zeige durchlässt. components.schemas enthält nur, was diese Einträge
// (auch über Verweise zwischen Schemata) brauchen — ein delegierendes Token
// sieht so auch keine Schemata nicht freigegebener Werkzeuge.
func werkzeugSpezErzeugen(katalog []werkzeugEintrag, schemas map[string]json.RawMessage, zeige func(werkzeugEintrag) bool) ([]byte, error) {
	return werkzeugSpezErzeugenMit(katalog, schemas, zeige, werkzeugSpezBeschreibung)
}

// werkzeugSpezErzeugenMit: wie werkzeugSpezErzeugen, mit eigener
// info.description (Board-Paar-Texte je Seite, Karte #789).
func werkzeugSpezErzeugenMit(katalog []werkzeugEintrag, schemas map[string]json.RawMessage, zeige func(werkzeugEintrag) bool, beschreibung string) ([]byte, error) {
	if err := werkzeugKatalogPruefen(katalog, schemas); err != nil {
		return nil, err
	}
	verweis := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	pfade := map[string]map[string]any{}
	gebraucht := map[string]bool{}
	var offen []string
	brauche := func(name string) {
		if !gebraucht[name] {
			gebraucht[name] = true
			offen = append(offen, name)
		}
	}
	for _, e := range katalog {
		if e.Zugaenge&zugangOpenAPI == 0 || !zeige(e) {
			continue
		}
		op := map[string]any{
			"operationId": e.Name,
			"summary":     e.Zusammenfassung,
			"description": e.Beschreibung,
		}
		if len(e.Parameter) > 0 {
			params := make([]any, 0, len(e.Parameter))
			for _, pa := range e.Parameter {
				params = append(params, map[string]any{"name": pa.Name, "in": pa.Ort, "required": pa.Pflicht,
					"description": pa.Beschreibung, "schema": pa.Schema})
			}
			op["parameters"] = params
		}
		if e.Eingabe != "" {
			op["requestBody"] = map[string]any{"required": true,
				"content": map[string]any{"application/json": map[string]any{"schema": verweis(e.Eingabe)}}}
			brauche(e.Eingabe)
		}
		inhalt := map[string]any{"application/json": map[string]any{"schema": verweis(e.Antwort)}}
		if e.AntwortHTML {
			inhalt["text/html"] = map[string]any{"schema": map[string]any{"type": "string"}}
		}
		brauche(e.Antwort)
		op["responses"] = map[string]any{"200": map[string]any{"description": e.AntwortText, "content": inhalt}}
		if pfade[e.Muster] == nil {
			pfade[e.Muster] = map[string]any{}
		}
		pfade[e.Muster][strings.ToLower(e.Methode)] = op
	}
	// Verweise zwischen Schemata auflösen (transitiv).
	for len(offen) > 0 {
		name := offen[0]
		offen = offen[1:]
		for _, ziel := range schemaVerweise(schemas[name]) {
			if schemas[ziel] == nil {
				return nil, fmt.Errorf("Schema %s verweist auf fehlendes Schema %s", name, ziel)
			}
			brauche(ziel)
		}
	}
	komponenten := map[string]json.RawMessage{}
	for name := range gebraucht {
		komponenten[name] = schemas[name]
	}
	spez := map[string]any{
		"openapi":  "3.1.0",
		"info":     map[string]any{"title": "aBoard-Werkzeuge", "version": "1", "description": beschreibung},
		"security": []any{map[string]any{"bearer": []any{}}},
		"paths":    pfade,
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}},
			"schemas":         komponenten,
		},
	}
	var puffer bytes.Buffer
	enc := json.NewEncoder(&puffer)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spez); err != nil {
		return nil, err
	}
	return puffer.Bytes(), nil
}

// schemaVerweise: die Namen aller "#/components/schemas/…"-Verweise in s.
func schemaVerweise(s json.RawMessage) []string {
	var wert any
	if json.Unmarshal(s, &wert) != nil {
		return nil
	}
	var aus []string
	var lauf func(v any)
	lauf = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				aus = append(aus, strings.TrimPrefix(ref, "#/components/schemas/"))
			}
			for _, w := range x {
				lauf(w)
			}
		case []any:
			for _, w := range x {
				lauf(w)
			}
		}
	}
	lauf(wert)
	sort.Strings(aus)
	return aus
}

// werkzeugSpezen: die beiden Fassungen der Beschreibung.
type werkzeugSpezen struct {
	alle      []byte // Projekt- und Personen-Tokens
	delegiert []byte // delegierendes Token (Open WebUI)
}

func werkzeugSpezenErzeugen(katalog []werkzeugEintrag, schemas map[string]json.RawMessage) (*werkzeugSpezen, error) {
	return werkzeugSpezenErzeugenSeite(katalog, schemas, BoardpaarSeiteAus)
}

// werkzeugSpezenErzeugenSeite: beide Fassungen mit den Board-Paar-Texten
// einer Seite (Karte #789, E-3). Auf „aus“ byte-gleich zu vorher.
func werkzeugSpezenErzeugenSeite(katalog []werkzeugEintrag, schemas map[string]json.RawMessage, seite string) (*werkzeugSpezen, error) {
	katalog = katalogFuerSeite(katalog, seite)
	schemas = werkzeugSchemasFuerSeite(schemas, seite) // Karte #797: `vertraulich` nur innen
	beschreibung := werkzeugSpezBeschreibung + werkzeugPaarText(seite)
	alle, err := werkzeugSpezErzeugenMit(katalog, schemas, func(werkzeugEintrag) bool { return true }, beschreibung)
	if err != nil {
		return nil, err
	}
	delegiert, err := werkzeugSpezErzeugenMit(katalog, schemas, func(e werkzeugEintrag) bool { return e.DelegierendErlaubt }, beschreibung)
	if err != nil {
		return nil, err
	}
	return &werkzeugSpezen{alle: alle, delegiert: delegiert}, nil
}

// fuer: die Fassung, die dieser Principal sieht.
func (s *werkzeugSpezen) fuer(p *AgentPrincipal) []byte {
	if p.Delegiert {
		return s.delegiert
	}
	return s.alle
}

var (
	werkzeugSpezEinmal sync.Once
	werkzeugSpezCache  *werkzeugSpezen
	werkzeugSpezFehler error
)

func werkzeugSpezBauen() {
	werkzeugSpezEinmal.Do(func() {
		werkzeugSpezCache, werkzeugSpezFehler = werkzeugSpezenErzeugen(werkzeugKatalog, werkzeugSchemas)
	})
}

// werkzeugSpezSeiten: die Fassungen mit Board-Paar-Texten je Seite (innen,
// aussen), bei erster Benutzung gebaut. Die Seite liest der Handler je
// Anfrage (ADR-0140 §4); der Katalog selbst ändert sich zur Laufzeit nicht.
var (
	werkzeugSpezSeitenSperre sync.Mutex
	werkzeugSpezSeiten       = map[string]*werkzeugSpezen{}
)

// werkzeugSpezFuer: die Beschreibung, die dieser Principal auf dieser Seite
// sieht. Auf „aus“ die unveränderte Fassung (eingecheckter Abzug).
func werkzeugSpezFuer(p *AgentPrincipal, seite string) ([]byte, error) {
	werkzeugSpezBauen()
	if werkzeugSpezFehler != nil {
		return nil, werkzeugSpezFehler
	}
	if werkzeugPaarText(seite) == "" {
		return werkzeugSpezCache.fuer(p), nil
	}
	werkzeugSpezSeitenSperre.Lock()
	defer werkzeugSpezSeitenSperre.Unlock()
	s, ok := werkzeugSpezSeiten[seite]
	if !ok {
		var err error
		if s, err = werkzeugSpezenErzeugenSeite(werkzeugKatalog, werkzeugSchemas, seite); err != nil {
			return nil, err
		}
		werkzeugSpezSeiten[seite] = s
	}
	return s.fuer(p), nil
}

// WerkzeugSpezifikation liefert die vollständige erzeugte Beschreibung
// (Projekt- und Personen-Tokens) — für Tests und den eingecheckten Abzug.
func WerkzeugSpezifikation() []byte {
	werkzeugSpezBauen()
	if werkzeugSpezFehler != nil {
		panic(werkzeugSpezFehler)
	}
	return append([]byte(nil), werkzeugSpezCache.alle...)
}

// WerkzeugSpezifikationDelegiert liefert die Beschreibung, die ein
// delegierendes Token (Open WebUI) sieht — für Tests.
func WerkzeugSpezifikationDelegiert() []byte {
	werkzeugSpezBauen()
	if werkzeugSpezFehler != nil {
		panic(werkzeugSpezFehler)
	}
	return append([]byte(nil), werkzeugSpezCache.delegiert...)
}
