// Command gen regenerates the checked-in bindings from the Natcatty plugin
// IDL.
//
// Usage:
//
//	go run ./sdk/idl/gen          # rewrite the committed files
//	go run ./sdk/idl/gen -check   # verify the committed files are current
//
// The command has no external dependencies, so CI can run the check with the
// same Go toolchain as the rest of the repository. The Rust bindings are
// committed because the Nix dev shell does not include a Rust toolchain.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mslxl/natcatty/sdk/idl"
)

func main() {
	root := flag.String("root", ".", "module root containing the generated files")
	check := flag.Bool("check", false, "verify the committed files instead of writing them")
	flag.Parse()

	definition, err := idl.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "idl gen: %v\n", err)
		os.Exit(1)
	}
	stub, err := idl.StubDefinition()
	if err != nil {
		fmt.Fprintf(os.Stderr, "idl gen: %v\n", err)
		os.Exit(1)
	}
	rustStub, err := definition.GenerateRustStub(stub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "idl gen: %v\n", err)
		os.Exit(1)
	}
	outputs := []idl.GeneratedFile{
		{Path: idl.GoPath, Content: definition.GenerateGo()},
		{Path: idl.RustPath, Content: definition.GenerateRust()},
		{Path: idl.RustStubPath, Content: rustStub},
	}
	outputs = append(outputs, definition.LanguageOutputs()...)

	failed := false
	for _, output := range outputs {
		path := filepath.Join(*root, filepath.FromSlash(output.Path))
		if *check {
			committed, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "idl gen: %s: %v\n", output.Path, err)
				failed = true
				continue
			}
			if string(committed) != string(output.Content) {
				fmt.Fprintf(os.Stderr, "idl gen: %s is stale; run go run ./sdk/idl/gen\n", output.Path)
				failed = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "idl gen: %s: %v\n", output.Path, err)
			failed = true
			continue
		}
		if err := os.WriteFile(path, output.Content, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "idl gen: %s: %v\n", output.Path, err)
			failed = true
			continue
		}
		fmt.Printf("wrote %s\n", output.Path)
	}
	if failed {
		os.Exit(1)
	}
}
