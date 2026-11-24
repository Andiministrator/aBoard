package main

// F-3056 (Sprint-117-Verify): Quelltext-Wächter für F-3050 — die Reste nachgeholter
// Purges werden beim Start aufgelöst, und zwar bevor der HTTP-Mux entsteht (vor dem
// ersten Request läuft kein Purge, nur deshalb gilt dort kein Alter).
// F-3059 (Verify 2): über den Syntaxbaum statt Textsuche — ein auskommentierter
// Aufruf oder einer in `if false { … }` hielte eine Textsuche grün. Verlangt wird
// eine unbedingte Anweisung auf oberster Ebene derselben Funktion, vor `mux :=
// http.NewServeMux()`.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestF3056_MainLoestPurgeResteVorDemErstenRequest(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ruftPurgeReste := func(st ast.Stmt) bool {
		es, ok := st.(*ast.ExprStmt)
		if !ok {
			return false
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RaeumePurgeResteAuf" || len(call.Args) != 1 {
			return false
		}
		arg, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return false
		}
		argSel, ok := arg.Fun.(*ast.SelectorExpr)
		return ok && argSel.Sel.Name == "AufraeumLage"
	}
	legtMuxAn := func(st ast.Stmt) bool {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return false
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "mux" {
			return false
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "NewServeMux"
	}
	gefunden := 0
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		aufruf, mux := -1, -1
		for i, st := range fn.Body.List {
			if ruftPurgeReste(st) {
				gefunden++
				if aufruf < 0 {
					aufruf = i
				}
			}
			if legtMuxAn(st) && mux < 0 {
				mux = i
			}
		}
		if aufruf >= 0 {
			if mux < 0 {
				t.Fatalf("%s ruft RaeumePurgeResteAuf, legt aber keinen Mux an — Wächter anpassen", fn.Name.Name)
			}
			if aufruf > mux {
				t.Fatal("RaeumePurgeResteAuf steht nach http.NewServeMux() — Requests könnten gleichzeitig purgen")
			}
		}
	}
	if gefunden != 1 {
		t.Fatalf("unbedingter Aufruf RaeumePurgeResteAuf(api.AufraeumLage(…)) auf oberster Ebene: %d, erwartet 1 (F-3050)", gefunden)
	}
}
