//go:build live

package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Inspect the actual patched LoginBot, not a duplicate fixture-selection helper.
func TestPinnedExactNameCreationPolicy(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), ".deps/azerothghost/e2e/e2eharness/session.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var login *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == "LoginBot" {
			login = f
		}
	}
	if login == nil {
		t.Fatal("LoginBot missing")
	}
	exactNames := false
	createCalls := 0
	timeoutReturns := false
	ast.Inspect(login, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
			id, ok := assign.Lhs[0].(*ast.Ident)
			if ok && id.Name == "names" {
				literal, ok := assign.Rhs[0].(*ast.CompositeLit)
				if ok && len(literal.Elts) == 1 {
					name, ok := literal.Elts[0].(*ast.Ident)
					exactNames = ok && name.Name == "charName"
				}
			}
		}
		if loop, ok := node.(*ast.RangeStmt); ok {
			list, ok := loop.X.(*ast.Ident)
			if ok && list.Name == "names" {
				ast.Inspect(loop.Body, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok {
						if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "CreateCharacter" {
							createCalls++
							if len(call.Args) == 0 {
								t.Fatal("character creation lacks name")
							}
							name, ok := call.Args[0].(*ast.Ident)
							if !ok || name.Name != "tryName" {
								t.Fatal("creation bypasses requested-name iteration")
							}
						}
					}
					if clause, ok := node.(*ast.CommClause); ok {
						if receive, ok := clause.Comm.(*ast.ExprStmt); ok {
							if unary, ok := receive.X.(*ast.UnaryExpr); ok {
								if call, ok := unary.X.(*ast.CallExpr); ok {
									if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "After" {
										_, timeoutReturns = clause.Body[len(clause.Body)-1].(*ast.ReturnStmt)
									}
								}
							}
						}
					}
					return true
				})
			}
		}
		return true
	})
	if !exactNames || createCalls != 1 || !timeoutReturns {
		t.Fatal("creation must try only requested name and return on unknown result")
	}
}
