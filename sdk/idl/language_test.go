package idl_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/sdk/idl"
)

// TestGeneratedCExposesTheWholeWireSurface proves the C bindings carry the
// ABI constants, every enum value, every host import, every service module and
// stable method id, and the generated init-time ABI check. The checked-in
// header is the one Zig consumes, so this is also the Zig ID coverage.
func TestGeneratedCExposesTheWholeWireSurface(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	header := string(definition.GenerateCHeader())
	for _, want := range []string{
		"#define NATCATTY_ABI_VERSION 1",
		`#define NATCATTY_HOST_MODULE "natcatty_host_v1"`,
		`NATCATTY_IMPORT("natcatty_host_v1", "abi_version")`,
		"NATCATTY_EXPORT_INIT",
		"NATCATTY_EXPORT_DISPATCH",
		"NATCATTY_EXPORT_SCRATCH",
		"natcatty_i32 natcatty_guest_init(void);",
		"natcatty_abi_mismatch_message",
	} {
		if !strings.Contains(header, want) {
			t.Errorf("generated C header is missing %q", want)
		}
	}
	for _, enum := range definition.Enums {
		prefix := "NATCATTY_" + strings.ToUpper(cSnake(enum.Name)) + "_"
		for _, value := range enum.Values {
			constant := fmt.Sprintf("%s%s = %d", prefix, strings.ToUpper(value.Name), value.Value)
			if !strings.Contains(header, constant) {
				t.Errorf("generated C header is missing enum value %s=%d", enum.Name+"."+value.Name, value.Value)
			}
		}
	}
	for _, function := range definition.HostModule().Functions {
		declaration := fmt.Sprintf("natcatty_host_%s(", cSnake(function.Name))
		if !strings.Contains(header, declaration) {
			t.Errorf("generated C header is missing host wrapper %s", function.Name)
		}
		importLine := fmt.Sprintf("NATCATTY_IMPORT(%q, %q)",
			definition.HostModule().Name, function.Name)
		if !strings.Contains(header, importLine) {
			t.Errorf("generated C implementation is missing import %s", function.Name)
		}
	}
	for _, service := range definition.Services {
		module := fmt.Sprintf(`NATCATTY_SVC_%s_MODULE %q`,
			strings.ToUpper(service.Name), service.Module())
		if !strings.Contains(header, module) {
			t.Errorf("generated C header is missing service module %q", service.Module())
		}
		for _, method := range service.Methods {
			constant := fmt.Sprintf("NATCATTY_SVC_%s_METHOD_%s %du",
				strings.ToUpper(service.Name), strings.ToUpper(method.Name), method.ID)
			if !strings.Contains(header, constant) {
				t.Errorf("generated C header is missing %s", constant)
			}
		}
		if !strings.Contains(header, fmt.Sprintf("natcatty_svc_%s_call(", service.Name)) {
			t.Errorf("generated C header is missing the %s call wrapper", service.Name)
		}
	}
}

// TestGeneratedTinyGoExposesTheWholeWireSurface proves every tinygo.wasm
// import carries the IDL module and field name, and that the generated ABI
// check calls the host abi_version before init succeeds.
func TestGeneratedTinyGoExposesTheWholeWireSurface(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	constants := squashSpace(string(definition.GenerateTinyGoConstants()))
	host := string(definition.GenerateTinyGoHost())
	stub := string(definition.GenerateTinyGoStub())
	for _, want := range []string{
		"ABIVersion int32 = 1",
		"func CheckABI() int32",
		"if HostABIVersion() != ABIVersion",
		"func GuestInit() int32",
		"HostLog(LogLevelError, abiMismatchMessage)",
		`ExportABIVersion = "natcatty_abi_version"`,
	} {
		if !strings.Contains(constants, want) {
			t.Errorf("generated TinyGo constants are missing %q", want)
		}
	}
	for _, function := range definition.HostModule().Functions {
		directive := fmt.Sprintf("//go:wasmimport %s %s", definition.HostModule().Name, function.Name)
		if !strings.Contains(host, directive) {
			t.Errorf("generated TinyGo host file is missing %q", directive)
		}
	}
	for _, service := range definition.Services {
		directive := fmt.Sprintf("//go:wasmimport %s call", service.Module())
		if !strings.Contains(host, directive) {
			t.Errorf("generated TinyGo host file is missing %q", directive)
		}
		for _, method := range service.Methods {
			constant := fmt.Sprintf("Service%sMethod%s uint32 = %d",
				goCamelForTest(service.Name), goCamelForTest(method.Name), method.ID)
			if !strings.Contains(constants, constant) {
				t.Errorf("generated TinyGo constants are missing %s", constant)
			}
		}
	}
	if !strings.Contains(host, "//go:build tinygo.wasm") {
		t.Error("generated TinyGo host file has no tinygo.wasm build constraint")
	}
	if !strings.Contains(stub, "//go:build !tinygo.wasm") {
		t.Error("generated TinyGo stub file has no !tinygo.wasm build constraint")
	}
	if !strings.Contains(stub, "return ABIVersion") {
		t.Error("generated TinyGo native stub does not report the generated ABI")
	}
}

// TestLanguageOutputsMatchCommittedFiles proves every checked-in C and TinyGo
// output, including the byte-identical sample copies, matches what the IDL
// generator produces now. `go run ./sdk/idl/gen -check` covers the same files.
func TestLanguageOutputsMatchCommittedFiles(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	root := filepath.Join("..", "..")
	for _, output := range definition.LanguageOutputs() {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(output.Path)))
		if err != nil {
			t.Fatalf("read committed %s: %v", output.Path, err)
		}
		if !bytes.Equal(got, output.Content) {
			t.Errorf("%s is stale; run go run ./sdk/idl/gen", output.Path)
		}
	}
}

// TestSampleGlueUsesGeneratedNames proves the hand-written sample glue only
// names symbols the generator emitted: guest export lists match exactly, the
// Zig source consumes the C header, and no sample carries a private module
// name or ABI number.
func TestSampleGlueUsesGeneratedNames(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	root := filepath.Join("..", "..")

	exportNames := map[string]bool{}
	for _, export := range definition.GuestModule().Exports {
		exportNames[export.Wire] = true
	}
	tinyGoSource, err := os.ReadFile(filepath.Join(root, "sdk/samples/hello-command-tinygo/main.go"))
	if err != nil {
		t.Fatalf("read TinyGo sample: %v", err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(tinyGoSource), "\n") {
		line = strings.TrimSpace(line)
		if wire, ok := strings.CutPrefix(line, "//export "); ok {
			if !exportNames[wire] {
				t.Errorf("TinyGo sample exports %q, which the IDL does not declare", wire)
			}
			seen[wire] = true
		}
	}
	for _, export := range definition.GuestModule().RequiredExports() {
		if !seen[export.Wire] {
			t.Errorf("TinyGo sample does not export required wire name %q", export.Wire)
		}
	}

	zigSource, err := os.ReadFile(filepath.Join(root, "sdk/samples/hello-command-zig/src/main.zig"))
	if err != nil {
		t.Fatalf("read Zig sample: %v", err)
	}
	for _, want := range []string{`@cInclude("natcatty_v1.h")`, "c.NATCATTY_ABI_VERSION", "c.natcatty_guest_init()"} {
		if !strings.Contains(string(zigSource), want) {
			t.Errorf("Zig sample is missing %q", want)
		}
	}
	var zigExports strings.Builder
	for _, line := range strings.Split(string(zigSource), "\n") {
		if strings.Contains(line, "@export(") {
			zigExports.WriteString(line)
		}
	}
	for _, export := range definition.GuestModule().RequiredExports() {
		constant := "c.NATCATTY_WIRE_" + strings.ToUpper(cSnake(export.Name))
		if !strings.Contains(zigExports.String(), constant) {
			t.Errorf("Zig sample does not @export required wire name via %s", constant)
		}
		if strings.Contains(string(zigSource), `"`+export.Wire+`"`) {
			t.Errorf("Zig sample restates wire name %q instead of using the C header", export.Wire)
		}
	}
	if strings.Contains(string(zigSource), "natcatty:svc/") {
		t.Error("Zig sample restates a service module instead of using the C header")
	}

	cSource, err := os.ReadFile(filepath.Join(root, "sdk/samples/hello-command-c/src/main.c"))
	if err != nil {
		t.Fatalf("read C sample: %v", err)
	}
	for _, want := range []string{`#include "natcatty_v1.h"`, "natcatty_guest_init()", "NATCATTY_EXPORT_INIT"} {
		if !strings.Contains(string(cSource), want) {
			t.Errorf("C sample is missing %q", want)
		}
	}
}

// squashSpace collapses gofmt's column alignment so constant assertions read
// naturally.
func squashSpace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// cSnake mirrors the generator's CamelCase-to-lower_snake_case rule.
func cSnake(name string) string {
	runes := []rune(name)
	var out strings.Builder
	for i, r := range runes {
		switch {
		case r >= 'A' && r <= 'Z':
			prevLower := i > 0 && runes[i-1] >= 'a' && runes[i-1] <= 'z'
			prevDigit := i > 0 && runes[i-1] >= '0' && runes[i-1] <= '9'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if i > 0 && (prevLower || prevDigit || (nextLower && runes[i-1] >= 'A' && runes[i-1] <= 'Z')) {
				out.WriteByte('_')
			}
			out.WriteRune(r - 'A' + 'a')
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
