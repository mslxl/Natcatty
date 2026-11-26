package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestPhoneRootLeavesTrayAndAppIconChromeOnTheDesktop covers the fourth
// acceptance checkbox. The tray controller and the application-icon providers
// are desktop chrome: the phone root must not call them.
//
// The dependency gate (scripts/mobile/check-deps.sh) proves ui/tray is absent
// from the phone graph. This guard additionally covers the same-package calls
// a dependency walk cannot see, because app_icons.go is compiled on phones
// even though no phone file should call it.
func TestPhoneRootLeavesTrayAndAppIconChromeOnTheDesktop(t *testing.T) {
	root, err := parser.ParseFile(token.NewFileSet(), "run_mobile.go", nil, 0)
	if err != nil {
		t.Fatalf("parse run_mobile.go: %v", err)
	}
	forbidden := map[string]bool{
		"appIconPNG":        true,
		"trayIconPNG":       true,
		"scaleTrayIcon":     true,
		"SetIconProvider":   true,
		"desktopController": true,
	}
	ast.Inspect(root, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if ok && forbidden[ident.Name] {
			t.Errorf("the phone composition root references desktop chrome %q", ident.Name)
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "tray" {
			t.Errorf("the phone composition root references the tray package")
		}
		return true
	})
}
