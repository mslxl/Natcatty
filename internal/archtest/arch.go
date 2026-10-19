package archtest

import "strings"

const modulePath = "github.com/mslxl/natcatty"

const mygoPath = "github.com/egoist/mygo"

// checkImport reports the dependency rule broken by pkg importing imp, or ""
// when the import is allowed. pkg is the module-relative package path ("" for
// the main package).
func checkImport(pkg, imp string) string {
	layer := packageLayer(pkg)

	if isStdlib(imp) {
		return ""
	}
	if imp == mygoPath || strings.HasPrefix(imp, mygoPath+"/") {
		switch layer {
		case "ui", "platform", "app":
			return ""
		}
		return "MyGo may be imported only by ui, platform, and app"
	}

	inModule := imp == modulePath || strings.HasPrefix(imp, modulePath+"/")
	if !inModule {
		switch layer {
		case "domain":
			return "domain may import only the standard library and the shared domain/model"
		case "application":
			return "application may import only the standard library, domain, and its own context's ports"
		}
		return ""
	}

	imported := strings.TrimPrefix(imp, modulePath+"/")
	importedLayer := packageLayer(imported)
	switch layer {
	case "":
		if importedLayer != "app" {
			return "the main package may import only app and the standard library"
		}
	case "domain":
		if importedLayer != "domain" {
			return "domain may import only the standard library and the shared domain/model"
		}
		if imported != "domain/model" && !strings.HasPrefix(imported, "domain/model/") {
			return "domain may import only the standard library and the shared domain/model"
		}
	case "application":
		switch importedLayer {
		case "domain":
			// Ports and use cases speak the shared domain model.
		case "application":
			if !sameApplicationContext(pkg, imported) {
				return "application may import only domain and its own context's ports"
			}
		default:
			return "application may import only the standard library, domain, and its own context's ports"
		}
	case "adapters":
		switch importedLayer {
		case "domain", "application", "platform", "adapters":
		default:
			return "adapters may import only domain, application, platform, and adapters"
		}
	case "ui":
		switch importedLayer {
		case "domain", "application", "ui":
		default:
			return "ui may import only domain, application, ui, and MyGo"
		}
	case "platform":
		if importedLayer != "platform" {
			return "platform may import only the standard library, MyGo, and platform"
		}
	}
	return ""
}

// packageLayer returns the top-level directory of a module-relative package
// path, or "" for the main package at the module root.
func packageLayer(pkg string) string {
	if pkg == "" {
		return ""
	}
	if i := strings.IndexByte(pkg, '/'); i >= 0 {
		return pkg[:i]
	}
	return pkg
}

// sameApplicationContext reports whether two application packages belong to
// the same bounded context, so a context may split its ports and use cases
// across packages without importing another context's internals.
func sameApplicationContext(a, b string) bool {
	return applicationContext(a) == applicationContext(b)
}

func applicationContext(pkg string) string {
	rest := strings.TrimPrefix(pkg, "application/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

func relativePackage(importPath string) string {
	if importPath == modulePath {
		return ""
	}
	return strings.TrimPrefix(importPath, modulePath+"/")
}

// isStdlib reports whether an import path is from the standard library: its
// first path element has no dot, unlike a module path such as "example.com/x".
func isStdlib(imp string) bool {
	first := imp
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	return !strings.Contains(first, ".")
}
