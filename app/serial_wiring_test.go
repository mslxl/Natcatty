package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCompositionWiresSerialAndTelnetAutoLogin pins the construction root: the
// shell State must receive both auto-login resolver options, and each closure
// must call the production resolver rather than returning nil. A Quick Connect
// test injects its own option, so only this source assertion catches a
// regression in app.go.
func TestCompositionWiresSerialAndTelnetAutoLogin(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var shellCall *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || name.Name != "shellState" {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "NewState" {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != "state" {
			return true
		}
		shellCall = call
		return true
	})
	if shellCall == nil {
		t.Fatal("app.go has no shellState = state.NewState(...) construction")
	}

	resolvers := map[string]string{
		"WithSerialAutoLogin": "serialAutoLoginOptions",
		"WithTelnetAutoLogin": "telnetAutoLoginOptions",
	}
	found := map[string]bool{}
	for _, argument := range shellCall.Args {
		option, ok := argument.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := option.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "state" {
			continue
		}
		resolver, ok := resolvers[selector.Sel.Name]
		if !ok {
			continue
		}
		found[selector.Sel.Name] = true
		if len(option.Args) != 1 {
			t.Errorf("state.%s has %d arguments, want one resolver", selector.Sel.Name, len(option.Args))
			continue
		}
		literal, ok := option.Args[0].(*ast.FuncLit)
		if !ok {
			t.Errorf("state.%s does not receive a function literal", selector.Sel.Name)
			continue
		}
		callsResolver := false
		ast.Inspect(literal.Body, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && ident.Name == resolver {
				callsResolver = true
			}
			return true
		})
		if !callsResolver {
			t.Errorf("state.%s does not call %s", selector.Sel.Name, resolver)
		}
	}
	for option := range resolvers {
		if !found[option] {
			t.Errorf("the shell construction does not pass state.%s", option)
		}
	}
}
