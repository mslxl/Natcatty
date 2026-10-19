package archtest

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

func TestCheckImportRejectsLayerViolations(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		imp  string
	}{
		{"domain may not use MyGo", "domain/vault", "github.com/egoist/mygo"},
		{"domain may not use the application layer", "domain/model", "github.com/mslxl/natcatty/application/vault"},
		{"domain may not import another context", "domain/vault", "github.com/mslxl/natcatty/domain/connection"},
		{"application may not use the UI", "application/vault", "github.com/mslxl/natcatty/ui/shell"},
		{"application may not use adapters", "application/vault", "github.com/mslxl/natcatty/adapters/store"},
		{"application may not use MyGo", "application/vault", "github.com/egoist/mygo/ui"},
		{"application may not import another context", "application/vault", "github.com/mslxl/natcatty/application/connection"},
		{"adapters may not use the UI", "adapters/store", "github.com/mslxl/natcatty/ui/shell"},
		{"adapters may not use the composition root", "adapters/store", "github.com/mslxl/natcatty/app"},
		{"adapters may not use MyGo", "adapters/store", "github.com/egoist/mygo"},
		{"ui may not use adapters", "ui/shell", "github.com/mslxl/natcatty/adapters/store"},
		{"ui may not use the composition root", "ui/shell", "github.com/mslxl/natcatty/app"},
		{"unrecognized packages may not use MyGo", "tools/foo", "github.com/egoist/mygo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := checkImport(test.pkg, test.imp); got == "" {
				t.Fatalf("checkImport(%q, %q) allowed a violation", test.pkg, test.imp)
			}
		})
	}
}

func TestCheckImportAllowsTheDocumentedDirection(t *testing.T) {
	tests := []struct {
		name string
		pkg  string
		imp  string
	}{
		{"domain uses the standard library", "domain/model", "context"},
		{"domain uses the shared model", "domain/vault", "github.com/mslxl/natcatty/domain/model"},
		{"domain model packages use each other", "domain/model", "github.com/mslxl/natcatty/domain/model/types"},
		{"application uses domain", "application/vault", "github.com/mslxl/natcatty/domain/model"},
		{"application uses its own ports", "application/vault", "github.com/mslxl/natcatty/application/vault/ports"},
		{"application context packages use each other", "application/vault", "github.com/mslxl/natcatty/application/vault/ports/ssh"},
		{"adapters use application and platform", "adapters/store", "github.com/mslxl/natcatty/application/vault"},
		{"adapters use platform", "adapters/store", "github.com/mslxl/natcatty/platform/linux"},
		{"ui uses MyGo", "ui/shell", "github.com/egoist/mygo/ui"},
		{"ui uses application", "ui/shell", "github.com/mslxl/natcatty/application/vault"},
		{"ui uses generated catalogs", "ui/shell", "github.com/mslxl/natcatty/ui/locales"},
		{"platform uses MyGo", "platform/linux", "github.com/egoist/mygo"},
		{"app uses everything", "app", "github.com/mslxl/natcatty/adapters/store"},
		{"main uses app", "", "github.com/mslxl/natcatty/app"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := checkImport(test.pkg, test.imp); got != "" {
				t.Fatalf("checkImport(%q, %q) = %q, want no violation", test.pkg, test.imp, got)
			}
		})
	}
}

func TestArchitecture(t *testing.T) {
	root := testutil.ModuleRoot(t)

	packages := listPackages(t, root)
	if len(packages) == 0 {
		t.Fatal("go list returned no packages")
	}
	for _, pkg := range packages {
		rel := relativePackage(pkg.ImportPath)
		for _, importSet := range [][]string{pkg.Imports, pkg.TestImports, pkg.XTestImports} {
			for _, imported := range importSet {
				if violation := checkImport(rel, imported); violation != "" {
					t.Errorf("%s imports %s: %s", pkg.ImportPath, imported, violation)
				}
			}
		}
	}

	for _, use := range webViewUses(t, root) {
		t.Errorf("%s: ui.WebView is allowed only in ui/browser, the host-routed browser panel", use)
	}
}

func TestFileUsesWebViewRecognizesAnyImportName(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{
			name: "plain import",
			source: `package p
import "github.com/egoist/mygo/ui"
func view(c *ui.Context) { ui.WebView(c, nil) }`,
			want: true,
		},
		{
			name: "aliased import",
			source: `package p
import u "github.com/egoist/mygo/ui"
func view(c *u.Context) { u.WebView(c, nil) }`,
			want: true,
		},
		{
			name: "dot import",
			source: `package p
import . "github.com/egoist/mygo/ui"
func view(c *Context) { WebView(c, nil) }`,
			want: true,
		},
		{
			name: "WebView from another package",
			source: `package p
import "example.com/other"
func view(c *other.Context) { other.WebView(c, nil) }`,
			want: false,
		},
		{
			name: "ui imported without WebView",
			source: `package p
import "github.com/egoist/mygo/ui"
func view(c *ui.Context) { ui.Text(c, "hi") }`,
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "test.go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := fileUsesWebView(file); got != test.want {
				t.Fatalf("fileUsesWebView() = %v, want %v", got, test.want)
			}
		})
	}
}

type listedPackage struct {
	ImportPath   string
	Name         string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func listPackages(t *testing.T, root string) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./...: %v\n%s", err, stderr.String())
	}

	var packages []listedPackage
	decoder := json.NewDecoder(bytes.NewReader(out))
	for decoder.More() {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func webViewUses(t *testing.T, root string) []string {
	t.Helper()
	var uses []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "build", ".mygo", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "ui/browser/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
		if err != nil {
			return err
		}
		if fileUsesWebView(file) {
			uses = append(uses, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return uses
}

func fileUsesWebView(file *ast.File) bool {
	aliases, dotImported := webViewImportNames(file)
	if len(aliases) == 0 && !dotImported {
		return false
	}

	used := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.SelectorExpr:
			if node.Sel.Name != "WebView" {
				return true
			}
			if qualifier, ok := node.X.(*ast.Ident); ok && aliases[qualifier.Name] {
				used = true
			}
		case *ast.Ident:
			// A dot import puts WebView in the file's own scope.
			if dotImported && node.Name == "WebView" {
				used = true
			}
		}
		return true
	})
	return used
}

// webViewImportNames reports the local names under which a file imports
// github.com/egoist/mygo/ui, so an aliased import cannot hide a ui.WebView
// use. dotImported reports `import . ".../ui"`.
func webViewImportNames(file *ast.File) (aliases map[string]bool, dotImported bool) {
	aliases = make(map[string]bool)
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "github.com/egoist/mygo/ui" {
			continue
		}
		if imp.Name == nil {
			aliases["ui"] = true
			continue
		}
		switch imp.Name.Name {
		case ".":
			dotImported = true
		case "_":
			// A blank import cannot name WebView.
		default:
			aliases[imp.Name.Name] = true
		}
	}
	return aliases, dotImported
}
