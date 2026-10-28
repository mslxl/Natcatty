package parity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ModulePath is the repository's Go module path. Parity acceptance tests are
// written either as package-relative paths (application/vault.TestFoo) or with
// the module prefix; both resolve against the same index.
const ModulePath = "github.com/mslxl/natcatty"

// TestIndex records every Go test function under a repository root and whether
// the default CI build compiles it.
type TestIndex struct {
	byDir map[string]map[string]testInfo
	all   map[string]testInfo
}

// testInfo records whether the default CI build runs a test declaration. A
// test behind the selftest or launchsmoke build tag is real, but the default
// `go test ./...` jobs do not execute it, so it cannot be the only evidence
// for a verified row.
type testInfo struct {
	runsInCI bool
}

// Resolves reports whether entry names a Go test that exists, whatever build
// tag guards its file.
func (idx TestIndex) Resolves(entry string) bool {
	entry = idx.normalize(entry)
	if i := strings.LastIndex(entry, "."); i > 0 {
		_, found := idx.byDir[entry[:i]][entry[i+1:]]
		return found
	}
	_, found := idx.all[entry]
	return found
}

// RunsInCI reports whether any declaration of entry is in a file the default
// CI build compiles. A test declared twice counts as runnable if either
// declaration is untagged.
func (idx TestIndex) RunsInCI(entry string) bool {
	entry = idx.normalize(entry)
	if i := strings.LastIndex(entry, "."); i > 0 {
		return idx.byDir[entry[:i]][entry[i+1:]].runsInCI
	}
	return idx.all[entry].runsInCI
}

func (idx TestIndex) normalize(entry string) string {
	entry = strings.TrimPrefix(entry, "./")
	return strings.TrimPrefix(entry, ModulePath+"/")
}

// HasCIRunnableTest reports whether row names at least one Go test that the
// default CI test jobs compile and run. Manual QA entries do not count.
func HasCIRunnableTest(row *Row, tests TestIndex) bool {
	for _, entry := range row.AcceptanceTests {
		if strings.HasPrefix(entry, "QA-") {
			continue
		}
		if tests.RunsInCI(entry) {
			return true
		}
	}
	return false
}

// BuildTestIndex parses every _test.go file under root. Build output and other
// non-source trees are skipped so a packaged build directory cannot pollute the
// index.
func BuildTestIndex(root string) (TestIndex, error) {
	idx := TestIndex{byDir: map[string]map[string]testInfo{}, all: map[string]testInfo{}}
	record := func(target map[string]testInfo, name string, runsInCI bool) {
		info := target[name]
		info.runsInCI = info.runsInCI || runsInCI
		target[name] = info
	}
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".scratch", "build", ".mygo", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), filePath, data, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filePath, err)
		}
		dir, err := filepath.Rel(root, filepath.Dir(filePath))
		if err != nil {
			return err
		}
		dir = filepath.ToSlash(dir)
		runsInCI := !tagExcludesCI(testBuildConstraint(data))
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			record(idx.all, fn.Name.Name, runsInCI)
			if idx.byDir[dir] == nil {
				idx.byDir[dir] = map[string]testInfo{}
			}
			record(idx.byDir[dir], fn.Name.Name, runsInCI)
		}
		return nil
	})
	if err != nil {
		return TestIndex{}, err
	}
	return idx, nil
}

// tagExcludesCI reports whether a build constraint keeps the file out of the
// default CI test jobs. Other constraints (an OS or a feature the release
// workflow does not use) still run on at least one CI job and are accepted.
func tagExcludesCI(constraint string) bool {
	for _, tag := range []string{"selftest", "launchsmoke"} {
		if strings.Contains(constraint, tag) {
			return true
		}
	}
	return false
}

// testBuildConstraint returns the file's //go:build or // +build line, if any.
func testBuildConstraint(data []byte) string {
	for _, line := range strings.SplitN(string(data), "\n", 30) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build") {
			return line
		}
	}
	return ""
}
