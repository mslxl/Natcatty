package wasm

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func TestValidateModuleAcceptsTheWalkingSkeletonModule(t *testing.T) {
	wasm := readFixture(t, "hello.wasm")
	if err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20); err != nil {
		t.Fatalf("ValidateModule: %v", err)
	}
}

func TestValidateModuleRejectsWASIAndUnknownImports(t *testing.T) {
	wasm := readFixture(t, "bad-import.wasm")
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var importErr *appplugin.ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("ValidateModule = %v, want ImportError", err)
	}
}

func TestValidateModuleRejectsMissingExports(t *testing.T) {
	wasm := readFixture(t, "missing-export.wasm")
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var missing *appplugin.MissingExportError
	if !errors.As(err, &missing) {
		t.Fatalf("ValidateModule = %v, want MissingExportError", err)
	}
}

func TestValidateModuleRejectsAnOverBudgetMemoryMaximum(t *testing.T) {
	wasm := readFixture(t, "hello.wasm")
	err := ValidateModule(context.Background(), wasm, 64<<10)
	var limit *appplugin.MemoryLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("ValidateModule = %v, want MemoryLimitError", err)
	}
}

func TestValidateModuleRejectsADeclaredMemoryMaximumAboveTheCap(t *testing.T) {
	// 17 pages is one page above the 1 MiB (16 page) manifest cap. The minimum
	// alone fits, so only the declared maximum can reject the module.
	wasm := moduleBytes(memoryLimits{min: 1, max: 17}, false, false)
	err := ValidateModule(context.Background(), wasm, 1<<20)
	var limit *appplugin.MemoryLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("ValidateModule = %v, want MemoryLimitError", err)
	}
	if limit.Declared != 1<<20 || limit.Required != 17*wasmPageSize {
		t.Fatalf("memory limit error = %+v", limit)
	}
}

func TestValidateModuleRejectsUnboundedMemory(t *testing.T) {
	wasm := moduleBytes(memoryLimits{min: 1, noMax: true}, false, false)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var limit *appplugin.MemoryLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("ValidateModule = %v, want MemoryLimitError for an unbounded memory", err)
	}
}

func TestValidateModuleRejectsInvertedMemoryBounds(t *testing.T) {
	wasm := moduleBytes(memoryLimits{min: 2, max: 1}, false, false)
	if err := ValidateModule(context.Background(), wasm, 1<<20); err == nil {
		t.Fatal("a module declaring memory minimum above its maximum was accepted")
	}
}

func TestValidateModuleAcceptsADeclaredMemoryMaximumWithinTheCap(t *testing.T) {
	wasm := moduleBytes(memoryLimits{min: 1, max: 16}, false, false)
	if err := ValidateModule(context.Background(), wasm, 1<<20); err != nil {
		t.Fatalf("ValidateModule: %v", err)
	}
}

func TestValidateModuleRejectsImportedGlobalsAndTables(t *testing.T) {
	global := importEntry("env", "g", importKindGlobal, []byte{0x7f, 0x00})
	table := importEntry("env", "t", importKindTable, []byte{0x70, 0x00, 0x01})
	for name, entry := range map[string][]byte{"global": global, "table": table} {
		t.Run(name, func(t *testing.T) {
			wasm := moduleWithImport(entry)
			err := ValidateModule(context.Background(), wasm,
				int64(domainplugin.DefaultMemoryMaxMiB)<<20)
			var importErr *appplugin.ImportError
			if !errors.As(err, &importErr) {
				t.Fatalf("ValidateModule = %v, want ImportError for an imported %s", err, name)
			}
			if importErr.Module != "env" {
				t.Fatalf("import error = %+v", importErr)
			}
		})
	}
}

func TestValidateModuleAcceptsTheGeneratedHostEnvironmentImports(t *testing.T) {
	// host-env.wasm imports now_ns and random_get in addition to log and
	// output_write; install-time and runtime validation must agree with the
	// generated v1 allowlist.
	wasm := readFixture(t, "host-env.wasm")
	if err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20); err != nil {
		t.Fatalf("ValidateModule: %v", err)
	}
}

func TestValidateModuleRejectsUnknownHostFunctionImports(t *testing.T) {
	entry := importEntry("natcatty_host_v1", "unknown", importKindFunction, []byte{0x00})
	wasm := moduleWithFunctionImport(entry)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var importErr *appplugin.ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("ValidateModule = %v, want ImportError", err)
	}
	if importErr.Module != "natcatty_host_v1" || importErr.Name != "unknown" {
		t.Fatalf("import error = %+v", importErr)
	}
}

func TestValidateModuleRejectsImportedMemory(t *testing.T) {
	memory := importEntry("env", "memory", importKindMemory, []byte{0x00, 0x01})
	wasm := moduleWithImportedMemory(memory)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var importErr *appplugin.ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("ValidateModule = %v, want ImportError", err)
	}
}

func TestValidateModuleRejectsStartFunctions(t *testing.T) {
	wasm := moduleBytes(memoryLimits{shared: false}, true, false)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("ValidateModule = %v, want ModuleValidationError", err)
	}
	if !strings.Contains(validation.Problem, "_start") {
		t.Fatalf("validation error = %q, want the _start rule", validation.Problem)
	}
}

func TestValidateModuleRejectsCoreStartSections(t *testing.T) {
	wasm := readFixture(t, "start-section.wasm")
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("ValidateModule = %v, want ModuleValidationError", err)
	}
	if !strings.Contains(validation.Problem, "start section") {
		t.Fatalf("validation error = %q, want the start section rule", validation.Problem)
	}

	// The fixture is a valid reactor apart from section 8: removing the start
	// section must make it pass every other install rule.
	if err := ValidateModule(context.Background(), withoutStartSection(t, wasm),
		int64(domainplugin.DefaultMemoryMaxMiB)<<20); err != nil {
		t.Fatalf("ValidateModule without the start section: %v", err)
	}
}

func TestValidateModuleRejectsSharedMemory(t *testing.T) {
	wasm := moduleBytes(memoryLimits{shared: true}, false, false)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("ValidateModule = %v, want ModuleValidationError", err)
	}
}

func TestValidateModuleRejectsAtomics(t *testing.T) {
	wasm := moduleBytes(memoryLimits{shared: false}, false, true)
	err := ValidateModule(context.Background(), wasm, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("ValidateModule = %v, want ModuleValidationError", err)
	}
}

func TestValidateModuleRejectsAnEmptyModule(t *testing.T) {
	err := ValidateModule(context.Background(), nil, 64<<20)
	var validation *appplugin.ModuleValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("ValidateModule = %v, want ModuleValidationError", err)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	wasm, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return wasm
}

// withoutStartSection copies a core module without its start section so a
// start-section fixture can prove every other rule accepts it.
func withoutStartSection(t *testing.T, wasm []byte) []byte {
	t.Helper()
	if len(wasm) < 8 {
		t.Fatal("module is shorter than the core header")
	}
	out := append([]byte(nil), wasm[:8]...)
	rest := wasm[8:]
	for len(rest) > 0 {
		id := rest[0]
		size, n := binary.Uvarint(rest[1:])
		if n <= 0 || size > uint64(len(rest)-1-n) {
			t.Fatalf("section %d has an invalid size", id)
		}
		total := 1 + n + int(size)
		if id != wasmStartSection {
			out = append(out, rest[:total]...)
		}
		rest = rest[total:]
	}
	return out
}

// --- minimal module encoder ------------------------------------------------

type memoryLimits struct {
	shared bool
	min    uint32
	max    uint32
	noMax  bool
}

// moduleBytes builds a core module exporting the five required ABI functions,
// one memory, and optionally _start or an atomic instruction in the first
// function body.
func moduleBytes(memory memoryLimits, start bool, atomic bool) []byte {
	return buildModule(memory, start, atomic, nil, 0, true)
}

// moduleWithImport builds the same module with one raw import entry. The entry
// is the module/name/kind/descriptor encoding without the import-vector count.
func moduleWithImport(entry []byte) []byte {
	return buildModule(memoryLimits{}, false, false, entry, 0, true)
}

// moduleWithFunctionImport additionally shifts the defined function indices,
// because a function import occupies an earlier function index.
func moduleWithFunctionImport(entry []byte) []byte {
	return buildModule(memoryLimits{}, false, false, entry, 1, true)
}

// moduleWithImportedMemory leaves out the defined memory section: a module may
// import or define a memory, never both.
func moduleWithImportedMemory(entry []byte) []byte {
	return buildModule(memoryLimits{}, false, false, entry, 0, false)
}

func buildModule(memory memoryLimits, start bool, atomic bool, importEntry []byte,
	functionImports byte, definedMemory bool) []byte {
	var out []byte
	out = append(out, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Type section: type 0 is () -> i32 for the reactor exports; type 1 is
	// (i32, i32) -> i32 for natcatty_dispatch. The generated ABI enforces both.
	typeSection := []byte{0x02, 0x60, 0x00, 0x01, 0x7f, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f}
	out = append(out, section(1, typeSection)...)

	if len(importEntry) > 0 {
		importSection := append([]byte{0x01}, importEntry...)
		out = append(out, section(2, importSection)...)
	}

	// Function section: five functions; dispatch uses type 1.
	functions := []byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00}
	out = append(out, section(3, functions)...)

	// Memory section: one bounded memory unless noMax asks for an unbounded
	// one. A zero minimum/maximum uses one page so existing callers keep the
	// tiny fixture shape.
	min := memory.min
	if min == 0 {
		min = 1
	}
	max := memory.max
	if max == 0 {
		max = 1
	}
	if definedMemory {
		memorySection := []byte{0x01}
		switch {
		case memory.shared:
			memorySection = append(memorySection, 0x03) // min, max, shared
			memorySection = append(memorySection, leb(min)...)
			memorySection = append(memorySection, leb(max)...)
		case memory.noMax:
			memorySection = append(memorySection, 0x00) // min only
			memorySection = append(memorySection, leb(min)...)
		default:
			memorySection = append(memorySection, 0x01) // min and max
			memorySection = append(memorySection, leb(min)...)
			memorySection = append(memorySection, leb(max)...)
		}
		out = append(out, section(5, memorySection)...)
	}

	// Export section: memory and the required functions.
	type export struct {
		name  string
		kind  byte
		index byte
	}
	exports := []export{
		{"memory", 0x02, 0x00},
		{"natcatty_abi_version", 0x00, 0x00},
		{"natcatty_init", 0x00, 0x01},
		{"natcatty_dispatch", 0x00, 0x02},
		{"natcatty_poll", 0x00, 0x03},
		{"natcatty_shutdown", 0x00, 0x04},
	}
	if start {
		exports = append(exports, export{"_start", 0x00, 0x00})
	}
	exportPayload := []byte{byte(len(exports))}
	for _, item := range exports {
		exportPayload = append(exportPayload, byte(len(item.name)))
		exportPayload = append(exportPayload, item.name...)
		index := item.index
		if item.kind == 0x00 { // function exports follow function imports
			index += functionImports
		}
		exportPayload = append(exportPayload, item.kind, index)
	}
	out = append(out, section(7, exportPayload)...)

	// Code section: five bodies. The first carries an atomic opcode when
	// requested; all return i32.const 1.
	codePayload := []byte{0x05}
	for i := 0; i < 5; i++ {
		var body []byte
		if i == 0 && atomic {
			// 0xFE is the atomic prefix. i32.atomic.load (0x10) with align
			// and offset immediates; the body is invalid without the threads
			// proposal, which is exactly what the host rejects.
			body = []byte{0x00, 0x41, 0x00, 0xfe, 0x10, 0x02, 0x00, 0x0b}
		} else {
			body = []byte{0x00, 0x41, 0x01, 0x0b}
		}
		codePayload = append(codePayload, byte(len(body)))
		codePayload = append(codePayload, body...)
	}
	out = append(out, section(10, codePayload)...)
	return out
}

// importEntry encodes one import entry: module name, field name, descriptor
// kind, and descriptor payload.
func importEntry(module, name string, kind byte, descriptor []byte) []byte {
	out := append(leb(uint32(len(module))), module...)
	out = append(out, leb(uint32(len(name)))...)
	out = append(out, name...)
	out = append(out, kind)
	return append(out, descriptor...)
}

func section(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, leb(uint32(len(payload)))...)
	return append(out, payload...)
}

func leb(value uint32) []byte {
	var out []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}
