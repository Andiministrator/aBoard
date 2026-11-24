package main

// Karte #827 (ADR-0141 §5.1, „Umsetzung 2c“ Entscheidung 2, Plan D2):
// Quelltext-Wächter für das Journal außen. Er liest den Syntaxbaum jeder
// Nicht-Test-Datei unter server/ (Muster: main_schatten_780_test.go) und
// nagelt fest:
//
//   - Schreibstellen auf boards: genau vier UPDATE mit state_json
//     (ApplyOpsIdempotent, Update, updateBoardInTx für TransferCard,
//     ErsetzeKopieFreigabe = Eingang, seit #836; vorher ErsetzeKopieMit) und genau drei INSERT (Insert,
//     BootstrapNewUser, KopieAnlegen). Eine neue Schreibstelle muss hier
//     entscheiden: Journal, Schranke im Schutzmodus — oder beides nicht, mit
//     Begründung.
//   - Die Schranke im Schutzmodus (spiegelSchranke) rufen genau Update und
//     TransferCard.
//   - Das Journal schreibt nur der Ops-Weg: BoardpaarJournalSchreiben hat
//     genau einen Aufrufer (pruefeUndSchreibe), der genau einen
//     (schreibvorgangMitSpiegel), und der genau einen (postOps). Der Eingang
//     (ErsetzeKopie) schreibt damit kein Journal.
//   - SQL, das boardpaar_journal oder boardpaar_wartend schreibt, steht nur in
//     storage/boardpaar_journal.go; .sql-Dateien nennen beide Tabellen nur in
//     Migration 0041. Auch das Abräumen wartender Zeilen durch den Eingang
//     (E2) läuft über eine Funktion dort (BoardpaarWartendErledigt).
//
// Grenze wie beim Vorbild: Zur Laufzeit zusammengesetztes SQL und
// Funktionswerte, die über Paketvariablen wandern, sieht keine
// Quelltextprüfung; dafür gibt es die Verhaltenstests
// (api/boardpaar_journal_ops_827_test.go, storage/spiegel_schranke_827_test.go).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestJournal827_QuelltextWaechter(t *testing.T) {
	updateState := regexp.MustCompile(`(?is)\bUPDATE\s+boards\s+SET\b[^;]*?\bstate_json\b`)
	insertBoards := regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+["'` + "`" + `]?boards\b`)
	journalSchreiben := regexp.MustCompile(`(?is)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+["'` + "`" + `]?boardpaar_(journal|wartend)\b`)
	journalTabelle := regexp.MustCompile(`(?i)\bboardpaar_(journal|wartend)\b`)

	// Bezeichner → erwartete Bezüge je Datei#Funktion (ohne die eigene
	// Deklaration).
	bezuege := map[string]map[string]int{
		"spiegelSchranke":           {"storage/boards.go#Update": 1, "storage/transfer.go#TransferCard": 1},
		"BoardpaarJournalSchreiben": {"api/boardpaar_journal_ops.go#pruefeUndSchreibe": 1},
		"pruefeUndSchreibe":         {"api/boards_ops.go#schreibvorgangMitSpiegel": 1},
		"schreibvorgangMitSpiegel":  {"api/boards_ops.go#postOps": 1},
	}
	soll := map[string]map[string]int{
		"UPDATE boards … state_json": {"storage/boards_ops.go#ApplyOpsIdempotent": 1, "storage/boards.go#Update": 1,
			"storage/transfer.go#updateBoardInTx": 1, "storage/schatten.go#ErsetzeKopieFreigabe": 1},
		"INSERT INTO boards": {"storage/boards.go#Insert": 1, "storage/bootstrap.go#BootstrapNewUser": 1,
			// Sitzungs-QA #835 K-3: die Anlage steht in kopieAnlegen, aufgerufen
			// von KopieAnlegen und KopieAnlegenTeamAusKonfig.
			"storage/boardpaar_kopie.go#kopieAnlegen": 1},
	}
	ist := map[string]map[string]int{}
	for k := range soll {
		ist[k] = map[string]int{}
	}
	for k := range bezuege {
		ist[k] = map[string]int{}
	}
	var fehler []string
	sqlErlaubt := "storage/migrations/0041_boardpaar_journal.sql"
	sqlGesehen := false

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
			roh, err := os.ReadFile(pfad)
			if err != nil {
				return err
			}
			if journalTabelle.Match(roh) {
				if datei != sqlErlaubt {
					fehler = append(fehler, "SQL-Datei nennt Journal/Wartend: "+datei)
				}
				sqlGesehen = true
			}
			return nil
		}
		if !strings.HasSuffix(datei, ".go") || strings.HasSuffix(datei, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, pfad, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			funktion := ""
			var eigenerName *ast.Ident
			if fd, ok := decl.(*ast.FuncDecl); ok {
				funktion, eigenerName = fd.Name.Name, fd.Name
			}
			wo := datei + "#" + funktion
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
					ist["UPDATE boards … state_json"][wo] += len(updateState.FindAllString(s, -1))
					ist["INSERT INTO boards"][wo] += len(insertBoards.FindAllString(s, -1))
					if journalSchreiben.MatchString(s) && datei != "storage/boardpaar_journal.go" {
						fehler = append(fehler, "Journal/Wartend geschrieben außerhalb storage/boardpaar_journal.go: "+
							datei+":"+strconv.Itoa(fset.Position(x.Pos()).Line))
					}
				case *ast.Ident:
					if _, ok := bezuege[x.Name]; ok && x != eigenerName {
						ist[x.Name][wo]++
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
	if !sqlGesehen {
		fehler = append(fehler, "Migration 0041 nicht gefunden — liest der Wächter das richtige Verzeichnis?")
	}
	maps.Copy(soll, bezuege)
	for _, k := range slices.Sorted(maps.Keys(soll)) {
		for wo, n := range ist[k] {
			if n == 0 {
				delete(ist[k], wo)
			}
		}
		if !maps.Equal(ist[k], soll[k]) {
			fehler = append(fehler, k+": ist "+formatZaehler827(ist[k])+", soll "+formatZaehler827(soll[k]))
		}
	}
	for _, f := range fehler {
		t.Error(f)
	}
}

func formatZaehler827(m map[string]int) string {
	var teile []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		teile = append(teile, k+"×"+strconv.Itoa(m[k]))
	}
	return "{" + strings.Join(teile, ", ") + "}"
}
