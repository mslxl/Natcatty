package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestPhoneRootLeavesTrayAndAppIconChromeOnTheDesktop covers the fourth
// acceptance checkbox. The tray controller and the application-icon providers
// are desktop chrome: the phone root must not call them, while their portable
// artwork code stays in the shared package for the desktop to use.
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

	artwork, err := os.ReadFile("app_icons.go")
	if err != nil {
		t.Fatalf("the portable app-icon code was deleted: %v", err)
	}
	if strings.Contains(string(artwork), "//go:build") {
		t.Fatal("app_icons.go became build-tagged instead of staying portable")
	}
	for _, name := range []string{"func appIconPNG(", "func trayIconPNG("} {
		if !strings.Contains(string(artwork), name) {
			t.Errorf("app_icons.go no longer defines %s", name)
		}
	}

	desktop, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("read the desktop composition root: %v", err)
	}
	if !strings.Contains(string(desktop), "SetIconProvider") ||
		!strings.Contains(string(desktop), "trayIconPNG(") {
		t.Fatal("the desktop integration no longer wires the tray or application icon")
	}
	trayModel, err := os.ReadFile("../ui/tray/model.go")
	if err != nil {
		t.Fatalf("the tray package was deleted: %v", err)
	}
	if len(trayModel) == 0 {
		t.Fatal("the tray package is empty")
	}
}
