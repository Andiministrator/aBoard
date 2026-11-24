package api_test

// F-1884/F-1885 (Kritiker der Sprint-86-End-QA zu board_aendern): Eine
// unveränderte Vorlagenliste schreibt nicht; doppelte Spaltentitel aus dem
// Bestand eines Menschen sperren das Werkzeug nicht.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// setzeSpalten1884: ersetzt columnDefs von b2 durch defs (rohes JSON).
func (e *env671) setzeSpalten1884(t *testing.T, defs string) {
	t.Helper()
	st := e.stand(t, "b2")
	anfang := strings.Index(st, `"columnDefs":[`)
	if anfang < 0 {
		t.Fatal("columnDefs fehlt")
	}
	ende := anfang + strings.Index(st[anfang:], "]") + 1
	st = st[:anfang] + `"columnDefs":` + defs + st[ende:]
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, st); err != nil {
		t.Fatal(err)
	}
}

func (e *env671) geaendertAm1884(t *testing.T) int64 {
	t.Helper()
	b, err := e.boards.Get(context.Background(), "b2")
	if err != nil {
		t.Fatal(err)
	}
	return b.UpdatedAt
}

func TestBoardAendern_VorlagenUnveraendert(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	st := e.stand(t, "b2")
	vorlagen := `"templates":[{"id":"v1","isTemplate":true,"title":"V","description":"lang","labels":["a"],"owner":"Anna","complexity":"hoch","effort":1.5}],`
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, strings.Replace(st, `"nextDisplayId":10,`, `"nextDisplayId":10,`+vorlagen, 1)); err != nil {
		t.Fatal(err)
	}
	vorher, zeitVorher := e.stand(t, "b2"), e.geaendertAm1884(t)

	// Dieselbe Liste wie gelesen — vollständig und als Teilangabe (nur id).
	for _, body := range []string{
		`{"vorlagen":[{"id":"v1","titel":"V","beschreibung":"lang","labels":["a"],"owner":"Anna","komplexitaet":"hoch","aufwand":1.5}]}`,
		`{"vorlagen":[{"id":"v1","titel":"V"}]}`,
	} {
		s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", body)
		if m := json671(t, b); s != http.StatusOK || m["ergebnis"] != "nichts geändert" {
			t.Errorf("%s: %d %s", body, s, b)
		}
	}
	if e.stand(t, "b2") != vorher || e.geaendertAm1884(t) != zeitVorher {
		t.Error("unveränderte Vorlagen haben geschrieben")
	}

	// Leere Liste auf einem Board ohne Vorlagen: ebenfalls nichts.
	e.zuruecksetzen755(t)
	zeitVorher = e.geaendertAm1884(t)
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[]}`); s != http.StatusOK || json671(t, b)["ergebnis"] != "nichts geändert" {
		t.Errorf("leere Liste: %d %s", s, b)
	}
	if e.geaendertAm1884(t) != zeitVorher {
		t.Error("leere Vorlagenliste hat geschrieben")
	}

	// Gegenrichtung: eine echte Änderung schreibt weiter.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"titel":"Neu"}]}`); s != http.StatusOK || json671(t, b)["ergebnis"] != "geändert" {
		t.Errorf("neue Vorlage: %d %s", s, b)
	}
}

func TestBoardAendern_DoppelteSpaltentitel(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	e.setzeSpalten1884(t, `[{"id":"col-a","title":"Todo"},{"id":"col-b","title":"todo"},{"id":"col-c","title":"Erledigt"}]`)

	// Bestand mit Dopplung, beide Titel bleiben: geht durch (umordnen, eine
	// dritte Spalte umbenennen).
	s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[{"id":"col-c","titel":"Fertig"},{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"todo"}]}`)
	if s != http.StatusOK || json671(t, b)["ergebnis"] != "geändert" {
		t.Fatalf("Dopplung aus dem Bestand: %d %s", s, b)
	}
	if st := e.stand(t, "b2"); !strings.Contains(st, `"title":"Fertig"`) || strings.Index(st, `"col-c"`) > strings.Index(st, `"col-a"`) {
		t.Errorf("Stand: %.300s", st[strings.Index(st, `"columnDefs"`):])
	}
	// Kritiker Sprint 87 K-4: Bestandstitel mit Leerzeichen am Rand gilt
	// getrimmt — kein Scheinumbenennen, das die Dopplung „herstellt“.
	e.setzeSpalten1884(t, `[{"id":"col-a","title":"Todo "},{"id":"col-b","title":"todo"},{"id":"col-c","title":"Erledigt"}]`)
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[{"id":"col-c","titel":"Fertig"},{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"todo"}]}`); s != http.StatusOK {
		t.Errorf("Bestandstitel mit Leerzeichen: %d %s", s, b)
	}
	e.setzeSpalten1884(t, `[{"id":"col-a","title":"Todo"},{"id":"col-b","title":"todo"},{"id":"col-c","title":"Fertig"}]`)
	// Den Ausweg (umbenennen) nimmt das Werkzeug an.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[{"id":"col-c","titel":"Fertig"},{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"Später"}]}`); s != http.StatusOK {
		t.Errorf("umbenennen: %d %s", s, b)
	}

	// Eine Dopplung herstellen bleibt verboten — durch Umbenennen, durch eine
	// neue Spalte, und die Meldung nennt den Ausweg.
	e.setzeSpalten1884(t, `[{"id":"col-a","title":"Todo"},{"id":"col-b","title":"todo"},{"id":"col-c","title":"Erledigt"}]`)
	for _, body := range []string{
		`{"spalten":[{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"todo"},{"id":"col-c","titel":"TODO"}]}`,
		`{"spalten":[{"id":"col-c","titel":"todo"},{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"Später"}]}`,
		`{"spalten":[{"id":"col-a","titel":"Todo"},{"id":"col-b","titel":"Später"},{"id":"col-c","titel":"Erledigt"},{"titel":"erledigt"}]}`,
	} {
		s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", body)
		if s != http.StatusBadRequest || !strings.Contains(string(b), "anderen Titel") {
			t.Errorf("%s: %d %s", body, s, b)
		}
	}
}

// Kritiker Sprint-87-Abschluss K-1: Länge und Steuerzeichen gelten nur für
// neue oder umbenannte Titel. Ein Bestandstitel, den ein Mensch länger, mit
// Tab oder mit Leerzeichen am Rand angelegt hat, sperrt das Werkzeug nicht
// und bleibt wörtlich stehen.
func TestBoardAendern_BestandstitelWoertlich(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	lang := strings.Repeat("x", 150)
	e.setzeSpalten1884(t, `[{"id":"col-a","title":"`+lang+`"},{"id":"col-b","title":"Mit\tTab"},{"id":"col-c","title":"Rand "}]`)
	gelesen := `{"id":"col-a","titel":"` + lang + `"},{"id":"col-b","titel":"Mit\tTab"},{"id":"col-c","titel":"Rand"}`

	// Unverändert zurückgegeben: nichts geschrieben, auch nicht „Rand “ → „Rand“.
	zeitVorher := e.geaendertAm1884(t)
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[`+gelesen+`]}`); s != http.StatusOK || json671(t, b)["ergebnis"] != "nichts geändert" {
		t.Fatalf("unveränderte Liste: %d %s", s, b)
	}
	if e.geaendertAm1884(t) != zeitVorher {
		t.Error("unveränderte Bestandstitel haben geschrieben")
	}

	// Umordnen und eine neue Spalte: geht durch, Bestandstitel bleiben wörtlich.
	body := `{"spalten":[{"id":"col-c","titel":"Rand "},` + gelesen[:strings.Index(gelesen, `,{"id":"col-c"`)] + `,{"titel":"Neu"}]}`
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", body); s != http.StatusOK || json671(t, b)["ergebnis"] != "geändert" {
		t.Fatalf("umordnen: %d %s", s, b)
	}
	st := e.stand(t, "b2")
	for _, soll := range []string{`"title":"` + lang + `"`, `"title":"Mit\tTab"`, `"title":"Rand "`, `"title":"Neu"`} {
		if !strings.Contains(st, soll) {
			t.Errorf("Stand ohne %.40s: %.400s", soll, st[strings.Index(st, `"columnDefs"`):])
		}
	}
	if strings.Index(st, `"col-c"`) > strings.Index(st, `"col-a"`) {
		t.Error("nicht umgeordnet")
	}

	// Umbenennen oder neu anlegen prüft weiter: zu lang, Steuerzeichen.
	for _, body := range []string{
		`{"spalten":[{"id":"col-c","titel":"Rand"},{"id":"col-a","titel":"` + lang + `y"},{"id":"col-b","titel":"Mit\tTab"},{"titel":"Neu"}]}`,
		`{"spalten":[{"id":"col-c","titel":"Rand"},{"id":"col-a","titel":"` + lang + `"},{"id":"col-b","titel":"Ohne\tTab"},{"titel":"Neu"}]}`,
		`{"spalten":[{"id":"col-c","titel":"Rand"},{"id":"col-a","titel":"` + lang + `"},{"id":"col-b","titel":"Mit\tTab"},{"titel":"Neu"},{"titel":"` + lang + `"}]}`,
	} {
		if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", body); s != http.StatusBadRequest || !strings.Contains(string(b), "höchstens 100 Zeichen") {
			t.Errorf("%.120s: %d %s", body, s, b)
		}
	}
}

// F-2390 (Rest): Ein unveränderter Board-Titel ohne Inhaltszeichen blockiert
// Änderungen an anderer Stelle nicht (wie bei Vorlagen und Swimlanes); ein
// geänderter Titel ohne Inhaltszeichen wird weiter abgelehnt.
func TestBoardAendern_BoardTitelUnveraendertOhneInhalt(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	st := e.stand(t, "b2")
	var m map[string]any
	if err := json.Unmarshal([]byte(st), &m); err != nil {
		t.Fatal(err)
	}
	// Ein kombinierendes Zeichen allein: kein Leerraum, aber auch kein Inhaltszeichen (Buchstabe, Ziffer,
	// Satz- oder Sonderzeichen). Als Go-Wert gebaut, damit kein Escape durch Werkzeugparameter läuft.
	leer := string(rune(0x301))
	m["board"].(map[string]any)["title"] = leer
	jsonLeer, _ := json.Marshal(leer)
	neu, _ := json.Marshal(m)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, string(neu)); err != nil {
		t.Fatal(err)
	}
	zeitVorher := e.geaendertAm1884(t)
	// Unveränderter Titel allein: nichts geändert, kein Fehler.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"titel":`+string(jsonLeer)+`}`); s != http.StatusOK || json671(t, b)["ergebnis"] != "nichts geändert" {
		t.Errorf("unveränderter Titel: %d %s", s, b)
	}
	if e.geaendertAm1884(t) != zeitVorher {
		t.Error("unveränderter Titel hat geschrieben")
	}
	// Zusammen mit einer echten Änderung an anderer Stelle: geht durch.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"titel":`+string(jsonLeer)+`,"vorlagen":[{"titel":"Neu"}]}`); s != http.StatusOK || json671(t, b)["ergebnis"] != "geändert" {
		t.Errorf("Titel + neue Vorlage: %d %s", s, b)
	}
	// Gegenrichtung: ein geänderter Titel ohne Inhaltszeichen wird abgelehnt.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"titel":"\u0302"}`); s != http.StatusBadRequest {
		t.Errorf("geänderter Titel ohne Inhalt: %d %s, erwartet 400", s, b)
	}
}
