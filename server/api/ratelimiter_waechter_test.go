package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// F-1578: Das Räumen des rateLimiter (F-1570) hängt an allowOderWartezeit. Ein
// zweiter Zugriff auf `.buckets` an anderer Stelle räumte nicht mit. Dieser
// Wächter schlägt an, sobald außerhalb von allowOderWartezeit auf das Feld
// zugegriffen wird.
func TestRateLimiter_F1578_NurEinZugriffspfadAufBuckets(t *testing.T) {
	dateien, err := filepath.Glob("*.go")
	if err != nil || len(dateien) == 0 {
		t.Fatalf("keine Quelldateien: %v", err)
	}
	fset := token.NewFileSet()
	gefunden := 0
	for _, d := range dateien {
		if strings.HasSuffix(d, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, d, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "buckets" {
					gefunden++
					if fn.Name.Name != "allowOderWartezeit" {
						t.Errorf("%s: Zugriff auf .buckets in %s — räumt nicht mit; über allowOderWartezeit gehen",
							fset.Position(sel.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if gefunden == 0 {
		t.Fatal("kein Zugriff auf .buckets gefunden — Wächter prüft nichts mehr")
	}
}
