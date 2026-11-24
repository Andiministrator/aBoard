package main

// Karte #780 (ADR-0140 §1, B-3): Nachweis „kein Schreibweg und kein Leser
// erreicht die Schatten“ — hier der JSON-Import und der Quelltext-Wächter,
// der die beiden Schatten-Tabellen und die Kaskaden-Auslöser auf ihre
// erlaubten Stellen festnagelt. Die übrigen Wege prüfen
// api/, storage/, mail/, llm/ und boardbackup/ (je schatten_780_test.go).

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchatten780_Import(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")
	if err := os.WriteFile(jsonPath, testBoardJSON(t, "b1", "Alter Titel", "2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("Erst-Import: %v", err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	schattentest.Bestuecke(t, db, "b1")
	schattentest.MussBestueckt(t, db, "b1")
	vorher := schattentest.Momentaufnahme(t, db)
	_ = db.Close()

	// Re-Import desselben Boards: der Überschreib-Zweig.
	if err := os.WriteFile(jsonPath, testBoardJSON(t, "b1", "Neuer Titel", "2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("Re-Import: %v", err)
	}
	db, err = storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if b, _ := storage.NewBoardRepo(db).Get(ctx, "b1"); b.Title != "Neuer Titel" {
		t.Fatalf("Re-Import hat nicht geschrieben: %q", b.Title)
	}
	schattentest.Unveraendert(t, db, vorher, "JSON-Import (Überschreiben)")
}

// Quelltext-Wächter. Er liest den Syntaxbaum jeder Nicht-Test-Datei unter
// server/ (go/parser) — Kommentare zählen damit nicht, und SQL über mehrere
// Zeilen, in Backticks oder hinter einem „//“ in einem String wird gesehen
// (Kritik K-2 zur ersten, zeilenweisen Fassung). Was er festnagelt:
//
//   - SQL auf die Tabellen schatten/schatten_abgleich steht nur in
//     storage/schatten.go. Ein String-Literal, das genau „schatten“ oder
//     „schatten_abgleich“ lautet (Tabellenname aus Variable oder Verkettung),
//     ist nur an den gezählten Stellen erlaubt (heute der JSON-Schlüssel der
//     Eingangs-Hülle).
//
//   - Kein Nicht-Test-Code wählt eine Methode .Liste oder .Stand aus — gleich
//     an welchem Empfänger (K-1), als Aufruf oder als Methodenwert
//     (f := sr.Liste). Das sind die Lesewege des SchattenRepo. Ausnahmen gibt
//     es genau die drei aus der Tabelle; wer einen weiteren Leser braucht,
//     nimmt ihn hier mit Begründung auf (Zeile in der Tabelle, Eintrag in
//     erlaubteLeser, Weg bis zur Registrierung in lesewege und soll).
//
//     Erlaubte Leser (Klasse 217: Aufrufstelle, umschließende Funktion, Weg
//     bis zur Registrierung):
//
//     | Datei#Funktion                                      | Grund                                  | Karte |
//     |-----------------------------------------------------|----------------------------------------|-------|
//     | api/ical.go#schattenEvents                          | iCal-Feed außen (.Liste)               | #782  |
//     | api/agent_werkzeuge_schatten.go#werkzeugSchattenLesen | Agentensicht außen (.Liste)         | #789  |
//     | api/schatten_anzeige.go#schattenAnzeigeLesen        | Anzeige im Browser außen (.Liste, .Stand) | #781 |
//
//     Weg bis zur Registrierung (je Bezeichner genau die genannte Stelle):
//
//     | Bezeichner                                          | einziger Bezug in                      | Karte |
//     |-----------------------------------------------------|----------------------------------------|-------|
//     | schattenEvents                                      | api/ical.go#feed                       | #782  |
//     | feed                                                | api/ical.go#Register                   | #782  |
//     | werkzeugSchattenLesen                               | je einmal in werkzeugBoardLesen, werkzeugKarteZeigen, werkzeugKartenSuchen | #789 |
//     | werkzeugBoardLesen/-KarteZeigen/-KartenSuchen       | Katalog-Eintrag (Paketebene) und je einmal der Weg der Rolle intern (internBoard, internKarte, internSuche) | #789, #783 |
//     | internBoard/internKarte/internSuche                 | api/vorschlaege.go#RegisterVorschlaege | #783  |
//     | RegisterVorschlaege                                 | main.go#run                            | #783  |
//     | schattenAnzeigeLesen                                | api/schatten_anzeige.go#holeSchattenAnzeige | #781 |
//     | holeSchattenAnzeige                                 | api/schatten_anzeige.go#Register       | #781  |
//     | NewSchattenAnzeigeAPI                               | main.go#run, unmittelbar `.Register(mux)` | #781 |
//     | SchattenAnzeigeAPI (der Typ)                        | nur api/schatten_anzeige.go (Typ, Konstruktor, drei Methoden) | #781 |
//
//     Bekannte FELDER namens Stand im Wissens-Paket (api/wissen.go,
//     wissen/wissen.go) sind in gezählter Anzahl ausgenommen.
//
//   - DELETE FROM boards steht genau einmal in BoardRepo.Delete und einmal
//     im Notweg BoardpaarRepo.KopieLoeschen (Karte #831), DELETE FROM
//     boardpaar_aussen genau einmal (AussenEntkoppeln) — die gewollten
//     Kaskaden (TestSchatten779_Kaskade, TestKopieLoeschen831_*). Gezählt
//     wird je Datei (K-8).
//
//   - REPLACE INTO / INSERT OR REPLACE (in SQLite ein Löschen samt Kaskade)
//     kommt nicht vor.
//
//   - Eine .sql-Datei (Migration, Trigger) nennt die Schatten nur, wenn sie
//     in der Liste steht (Verify V-3).
//
//   - Kein Nicht-Test-Code importiert schatten/schattentest (das Paket liest
//     die Tabellen und ist selbst ausgenommen; Sitzungs-QA C-3).
//
// Die Tabellen-Regel erkennt auch die Komma-Verknüpfung zweier Tabellen mit
// höchstens einem einwortigen Alias (FROM boards b, schatten s) und jeden
// Namen, der mit „schatten“ beginnt (schatten_neu; Sitzungs-QA C-2). Nicht
// erkannt: AS-Alias, drei und mehr Tabellen in der Komma-Liste,
// schema-qualifizierte Namen (main.schatten) — Verify W-1. Umgekehrt meldet
// sie einen Go-String wie „from boards, Schatten …“ fälschlich; das ist
// fail-closed und heute ohne Treffer. Der Inhalt der erlaubten Migrationen 0033–0035 wird nicht
// weiter geprüft: Migrationen sind versioniert und werden nicht nachträglich
// geändert (C-4).
//
// Grenze (Leserweg): Welche Route ein Handler trägt, prüfen die Katalog-Tests
// (agent_752_test.go) bzw. der Test der Anzeige (schatten_anzeige_781_test.go:
// Menschen-Kette, Rolle, Agent 403), nicht dieser Wächter. Reflexion
// (reflect MethodByName) und Datenfluss (ein Funktionswert, der über eine
// Paketvariable wandert) sieht keine Quelltextprüfung; dafür gibt es die
// Verhaltenstests je Weg. Der allgemeine Name feed schlägt bei jeder lokalen
// Variablen gleichen Namens fail-closed an (Sitzungs-QA C-5).
//
// Grenze: Ein Tabellenname, der zur Laufzeit zusammengesetzt wird
// ("schat"+"ten", aus Konfiguration), entgeht jeder Quelltextprüfung. Das
// fängt kein Wächter; dafür gibt es die Verhaltenstests je Weg.
//
// Gegenprobe: eine dieser Formen in eine fremde Datei schreiben, der Test
// meldet Datei und Zeile (docs/qa/gegenproben/0780-schatten.py).
func TestSchatten780_QuelltextWaechter(t *testing.T) {
	firstArg := func(c *ast.CallExpr) ast.Expr {
		if len(c.Args) == 0 {
			return nil
		}
		return c.Args[0]
	}
	// Name der aufgerufenen Funktion: `f(…)` → f, `pkg.f(…)` → f, sonst "".
	aufrufName := func(e ast.Expr) string {
		switch f := e.(type) {
		case *ast.Ident:
			return f.Name
		case *ast.SelectorExpr:
			return f.Sel.Name
		}
		return ""
	}
	tabelle := regexp.MustCompile(`(?is)(\b(FROM|INTO|UPDATE|JOIN|TABLE)\s+|\b(FROM|JOIN)\s+\w+(\s+\w+)?\s*,\s*)["'` + "`" + `]?schatten\w*`)
	nackt := regexp.MustCompile(`^(?i)schatten(_abgleich)?$`)
	loescheBoards := regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+boards\b`)
	loescheKopplung := regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+boardpaar_aussen\b`)
	ersetze := regexp.MustCompile(`(?is)\b(REPLACE\s+INTO|INSERT\s+OR\s+REPLACE)\b`)

	var fehler []string
	sqlSchatten := regexp.MustCompile(`(?i)\bschatten`)
	// 0040 (Karte #825): Spalten lane, spalte_id, fassung an schatten.
	sqlErlaubt := []string{"storage/migrations/0033_boardpaar_innen.sql", "storage/migrations/0034_boardpaar_aussen.sql",
		"storage/migrations/0035_schatten.sql", "storage/migrations/0040_boardpaar_kopie.sql"}
	sqlGesehen := 0
	aufrufe := map[*ast.SelectorExpr]bool{}
	aufrufVon := map[*ast.SelectorExpr]*ast.CallExpr{} // Selektor → der Aufruf, dessen Fun er ist
	// Dateien mit einem FELD namens Stand (kein SchattenRepo): nur dort darf ein
	// .Stand ohne Aufruf stehen; die erlaubte Anzahl steht in soll unten.
	stehtFeld := map[string]bool{"api/wissen.go": true, "wissen/wissen.go": true}
	// Bezeichner auf dem Weg zu einem erlaubten Leser (Klasse 217): jeder Bezug
	// außerhalb der eigenen Deklaration wird je Datei#Funktion gezählt.
	lesewege := []string{"schattenEvents", "feed", // #782, Kalender außen
		"werkzeugSchattenLesen", "werkzeugBoardLesen", "werkzeugKarteZeigen", "werkzeugKartenSuchen", // #789, Agentensicht außen
		"schattenAnzeigeLesen", "holeSchattenAnzeige", "NewSchattenAnzeigeAPI", "SchattenAnzeigeAPI", // #781, Anzeige außen
		"internBoard", "internKarte", "internSuche", "RegisterVorschlaege"} // #783, Rolle intern außen (ADR-0140 §8: lesen mit Schatten nach §7)
	zaehler := map[string]map[string]int{"DELETE FROM boards": {}, "DELETE FROM boardpaar_aussen": {}, "Literal „schatten“": {},
		"erlaubter Leser .Liste(…)": {}, "erlaubter Leser .Stand(…)": {}, "Feld .Stand (Wissens-Paket)": {},
		"Register auf NewSchattenAnzeigeAPI(…)": {}}
	for _, n := range lesewege {
		zaehler["Bezug auf "+n] = map[string]int{}
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(pfad string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); pfad != "." && (n == "node_modules" || n == "testdata" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		datei := filepath.ToSlash(pfad)
		if strings.HasSuffix(datei, ".sql") {
			// Migrationen, Trigger: Jede .sql-Datei, die die Schatten nennt,
			// steht in der Liste unten (Verify V-3). Ein Trigger auf
			// schatten in einer späteren Migration wäre sonst unsichtbar.
			roh, err := os.ReadFile(pfad)
			if err != nil {
				return err
			}
			if sqlSchatten.Match(roh) && !slices.Contains(sqlErlaubt, datei) {
				fehler = append(fehler, "SQL-Datei nennt die Schatten: "+datei)
			}
			sqlGesehen += strings.Count(strings.Join(sqlErlaubt, "\n"), datei)
			return nil
		}
		if !strings.HasSuffix(datei, ".go") || strings.HasSuffix(datei, "_test.go") || strings.HasPrefix(datei, "schatten/schattentest/") {
			return nil
		}
		f, err := parser.ParseFile(fset, pfad, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			// Name der umgebenden Funktion ("" außerhalb von Funktionen). Die
			// Ausnahmen unten gelten nur innerhalb einer benannten Funktion.
			funktion := ""
			var eigenerName *ast.Ident
			if fd, ok := decl.(*ast.FuncDecl); ok {
				funktion, eigenerName = fd.Name.Name, fd.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(x.Value)
					if err != nil {
						s = x.Value
					}
					wo := datei + ":" + strconv.Itoa(fset.Position(x.Pos()).Line)
					if nackt.MatchString(s) {
						zaehler["Literal „schatten“"][datei]++
					}
					if tabelle.MatchString(s) && datei != "storage/schatten.go" {
						fehler = append(fehler, "Schatten-Tabelle außerhalb storage/schatten.go: "+wo)
					}
					if ersetze.MatchString(s) {
						fehler = append(fehler, "REPLACE: "+wo)
					}
					zaehler["DELETE FROM boards"][datei] += len(loescheBoards.FindAllString(s, -1))
					zaehler["DELETE FROM boardpaar_aussen"][datei] += len(loescheKopplung.FindAllString(s, -1))
				case *ast.ImportSpec:
					if strings.HasSuffix(strings.Trim(x.Path.Value, `"`), "/schatten/schattentest") {
						fehler = append(fehler, "Import von schattentest im Server-Code: "+datei)
					}
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						aufrufe[sel] = true
						aufrufVon[sel] = x
					}
				case *ast.SelectorExpr:
					// K-5 zu #781: Die Anzeige wird genau so registriert, wie main.go#run
					// es tut — `api.NewSchattenAnzeigeAPI(…).Register(mux)`, unmittelbar.
					// Ein Wert in einer Variablen, ein zweiter Aufruf oder eine
					// Registrierung an anderer Stelle bricht die Zählung.
					// V-3: der Selektor muss selbst aufgerufen werden (kein Methodenwert
					// `f := api.New…(…).Register`), und das einzige Argument ist `mux`
					// (nicht `http.NewServeMux()` o. Ä.); sonst ein Fehler mit Stelle.
					if c, ok := x.X.(*ast.CallExpr); ok && x.Sel.Name == "Register" && aufrufName(c.Fun) == "NewSchattenAnzeigeAPI" {
						wo := datei + ":" + strconv.Itoa(fset.Position(x.Pos()).Line)
						reg := aufrufVon[x]
						if reg == nil {
							fehler = append(fehler, "Register der Anzeige als Methodenwert: "+wo)
						} else if id, ok := firstArg(reg).(*ast.Ident); len(reg.Args) != 1 || !ok || id.Name != "mux" {
							fehler = append(fehler, "Register der Anzeige nicht mit `mux`: "+wo)
						} else {
							zaehler["Register auf NewSchattenAnzeigeAPI(…)"][datei+"#"+funktion]++
						}
					}
					// Jede Auswahl .Liste / .Stand zählt — Aufruf UND Methodenwert
					// (f := sr.Liste; f(…), Kritik K-2 zu #782). Ein Aufruf steht
					// als SelectorExpr im CallExpr und wird hier genau einmal gesehen.
					if x.Sel.Name == "Liste" || x.Sel.Name == "Stand" {
						if x.Sel.Name == "Stand" && !aufrufe[x] && stehtFeld[datei] {
							// Bekannte Felder gleichen Namens im Wissens-Paket (idx.Stand):
							// kein Methodenwert, aber gezählt, damit eine Zugabe auffällt.
							zaehler["Feld .Stand (Wissens-Paket)"][datei]++
						} else if x.Sel.Name == "Liste" && (datei == "api/ical.go" && funktion == "schattenEvents" ||
							datei == "api/agent_werkzeuge_schatten.go" && funktion == "werkzeugSchattenLesen" ||
							datei == "api/schatten_anzeige.go" && funktion == "schattenAnzeigeLesen") {
							// Die drei Ausnahmen für .Liste (#782 Kalender, #789
							// Werkzeuge, #781 Anzeige): genau diese Funktionen.
							zaehler["erlaubter Leser .Liste(…)"][datei+"#"+funktion]++
						} else if x.Sel.Name == "Stand" && datei == "api/schatten_anzeige.go" && funktion == "schattenAnzeigeLesen" {
							// .Stand nur für die Aktualität der Anzeige (#781).
							zaehler["erlaubter Leser .Stand(…)"][datei+"#"+funktion]++
						} else {
							fehler = append(fehler, "Zugriff ."+x.Sel.Name+": "+datei+":"+strconv.Itoa(fset.Position(x.Pos()).Line)+" (Funktion "+funktion+")")
						}
					}
				case *ast.Ident:
					// Der Weg zu den erlaubten Lesern (K-1 und V-4 zu #782, #789):
					// jeder Bezug auf eine Funktion der Kette, außer ihrer eigenen
					// Deklaration. Kein anderer Bezeichner dieser Namen im
					// Nicht-Test-Code (gemessen).
					if x != eigenerName && slices.Contains(lesewege, x.Name) {
						zaehler["Bezug auf "+x.Name][datei+"#"+funktion]++
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	soll := map[string]map[string]int{
		// Karte #831: dritte gewollte Kaskade — Notweg „Kopie außen löschen“
		// (BoardpaarRepo.KopieLoeschen, nur Kopien, Prüfung in derselben
		// Transaktion; api/admin_boardpaar_831_test.go prüft die Kaskade).
		"DELETE FROM boards":           {"storage/boards.go": 1, "storage/boardpaar_kopie_loeschen.go": 1},
		"DELETE FROM boardpaar_aussen": {"storage/boardpaar.go": 1},
		// Der JSON-Schlüssel „schatten“ der Eingangs-Hülle (ADR-0140 §3), kein
		// Tabellenname. Jede weitere Stelle braucht eine Entscheidung.
		// Karte #825 (ADR-0141 §4): derselbe Schlüssel in der Hülle des
		// Eingangs v2, einmal als Konstante.
		"Literal „schatten“": {"schatten/eingang.go": 2, "schatten/v2.go": 1},
		// Karte #782 (ADR-0140 §6): der Feed der Seite außen liest die Schatten
		// mit Datum — genau ein Aufruf, genau diese Datei.
		// Karte #789 (ADR-0140 §7): die Agentensicht außen — genau ein Aufruf in
		// werkzeugSchattenLesen.
		// Karte #781 (ADR-0140 §5): die Anzeige außen — ein .Liste und ein .Stand,
		// beide in schattenAnzeigeLesen.
		"erlaubter Leser .Liste(…)": {"api/ical.go#schattenEvents": 1, "api/agent_werkzeuge_schatten.go#werkzeugSchattenLesen": 1,
			"api/schatten_anzeige.go#schattenAnzeigeLesen": 1},
		"erlaubter Leser .Stand(…)": {"api/schatten_anzeige.go#schattenAnzeigeLesen": 1},
		// … und der Weg bis zur Registrierung: Leser ← Handler ← Register ← main.
		"Bezug auf schattenAnzeigeLesen":  {"api/schatten_anzeige.go#holeSchattenAnzeige": 1},
		"Bezug auf holeSchattenAnzeige":   {"api/schatten_anzeige.go#Register": 1},
		"Bezug auf NewSchattenAnzeigeAPI": {"main.go#run": 1},
		// … und zwar unmittelbar als `…(…).Register(mux)` (kein Wert in einer Variablen).
		"Register auf NewSchattenAnzeigeAPI(…)": {"main.go#run": 1},
		// Der Typ selbst kommt nur in schatten_anzeige.go vor (Deklaration, Konstruktor,
		// drei Methoden): kein Literal `&SchattenAnzeigeAPI{…}` an anderer Stelle,
		// das den Konstruktor und damit die Zählung umgeht (K-5).
		"Bezug auf SchattenAnzeigeAPI": {"api/schatten_anzeige.go#": 1, "api/schatten_anzeige.go#NewSchattenAnzeigeAPI": 2,
			"api/schatten_anzeige.go#Register": 1, "api/schatten_anzeige.go#holeSchattenAnzeige": 1, "api/schatten_anzeige.go#schattenAnzeigeLesen": 1},
		// … und diese Funktion wird nur vom Feed-Handler benannt (K-1 zu #782).
		"Bezug auf schattenEvents": {"api/ical.go#feed": 1},
		"Bezug auf feed":           {"api/ical.go#Register": 1},
		// #789: werkzeugSchattenLesen nur aus den drei Lesern aus §7, und jeder
		// von ihnen nur aus seinem Katalog-Eintrag (Paketebene, Funktion "").
		"Bezug auf werkzeugSchattenLesen": {"api/agent_werkzeuge.go#werkzeugBoardLesen": 1,
			"api/agent_werkzeuge_karte.go#werkzeugKarteZeigen": 1, "api/agent_werkzeuge_lesen.go#werkzeugKartenSuchen": 1},
		// #783: dazu je einmal der feste Weg der Rolle intern (ADR-0140 §8), der
		// diese Leser mit fester Query aufruft — und dieser Weg nur aus seiner
		// Registrierung, die nur aus main.go#run.
		"Bezug auf werkzeugBoardLesen":   {"api/agent_werkzeug_katalog_daten.go#": 1, "api/vorschlaege.go#internBoard": 1},
		"Bezug auf werkzeugKarteZeigen":  {"api/agent_werkzeug_katalog_daten.go#": 1, "api/vorschlaege.go#internKarte": 1},
		"Bezug auf werkzeugKartenSuchen": {"api/agent_werkzeug_katalog_lesen.go#": 1, "api/vorschlaege.go#internSuche": 1},
		"Bezug auf internBoard":          {"api/vorschlaege.go#RegisterVorschlaege": 1},
		"Bezug auf internKarte":          {"api/vorschlaege.go#RegisterVorschlaege": 1},
		"Bezug auf internSuche":          {"api/vorschlaege.go#RegisterVorschlaege": 1},
		"Bezug auf RegisterVorschlaege":  {"main.go#run": 1},
		// Felder Index.Stand im Wissens-Paket (kein SchattenRepo.Stand).
		"Feld .Stand (Wissens-Paket)": {"api/wissen.go": 4, "wissen/wissen.go": 1},
	}
	for was, ist := range zaehler {
		for datei, n := range ist {
			if n != soll[was][datei] {
				fehler = append(fehler, fmt.Sprintf("%s: %s %d-mal, erlaubt %d", was, datei, n, soll[was][datei]))
			}
		}
		for datei, n := range soll[was] {
			if ist[datei] != n {
				fehler = append(fehler, fmt.Sprintf("%s: %s %d-mal, erwartet %d — Wächter liest die Stelle nicht mehr", was, datei, ist[datei], n))
			}
		}
	}
	if sqlGesehen != len(sqlErlaubt) {
		fehler = append(fehler, fmt.Sprintf("SQL-Dateien: %d von %d erlaubten gefunden — Wächter liest die Migrationen nicht mehr", sqlGesehen, len(sqlErlaubt)))
	}
	sort.Strings(fehler)
	fehler = slices.Compact(fehler)
	for _, f := range fehler {
		t.Error(f)
	}
}
