package wasm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Validator implements the application's ModuleValidator port. Install-time
// validation compiles the module but never instantiates or runs it.
type Validator struct{}

var _ appplugin.ModuleValidator = Validator{}

// ValidateModule checks structural package rules: the generated guest export
// set and signatures, no _start export or core start section, the generated
// host-import allowlist (which rejects WASI, imported globals, imported tables,
// and imported memories), the exported memory's declared minimum and maximum
// against the manifest's effective cap, and the absence of threads, atomics,
// and shared memory. Compiling with CoreFeaturesV2 keeps the threads proposal
// disabled, so a module that uses it fails before execution.
func (Validator) ValidateModule(ctx context.Context, wasm []byte, memoryMaxBytes int64) error {
	return ValidateModule(ctx, wasm, memoryMaxBytes)
}

// ValidateModule is Validator without the receiver, for callers that only need
// the structural check.
func ValidateModule(ctx context.Context, wasm []byte, memoryMaxBytes int64) error {
	if len(wasm) == 0 {
		return &appplugin.ModuleValidationError{Problem: "empty module"}
	}
	if memoryMaxBytes <= 0 {
		return &appplugin.ModuleValidationError{Problem: "memory maximum must be positive"}
	}
	pages := uint32((memoryMaxBytes + wasmPageSize - 1) / wasmPageSize)
	if pages == 0 || pages > 65536 {
		return &appplugin.ModuleValidationError{Problem: "memory maximum is out of range"}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2).
		WithMemoryLimitPages(pages))
	defer rt.Close(context.Background())

	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		if typed := classifyCompileError(ctx, wasm, err, memoryMaxBytes, nil, nil); typed != nil {
			return typed
		}
		return &appplugin.ModuleValidationError{Problem: "compile module: " + err.Error()}
	}
	return validateModule(wasm, compiled, memoryMaxBytes)
}

// validateModule is the single structural validator used by both the install
// path and the runtime. It accepts an already-compiled module and the raw
// bytes because wazero's CompiledModule does not expose imported globals or
// tables, and it clamps declared memory maxima while decoding.
func validateModule(wasm []byte, compiled wazero.CompiledModule, memoryMax int64) error {
	if err := rejectStartSection(wasm); err != nil {
		return err
	}
	if err := validateExports(compiled); err != nil {
		return err
	}
	if err := validateMemory(wasm, compiled, memoryMax); err != nil {
		return err
	}
	return validateImports(wasm, compiled)
}

// validateExports enforces the generated GuestExports table: every required
// export is present with its generated signature, the module does not export
// _start, and no function is exported outside the generated ABI.
func validateExports(compiled wazero.CompiledModule) error {
	exports := compiled.ExportedFunctions()
	for _, export := range domainpluginabi.GuestExports {
		definition, ok := exports[export.Wire]
		if !ok {
			if export.Optional {
				continue
			}
			return &appplugin.MissingExportError{Name: export.Wire}
		}
		if err := domainpluginabi.CheckSignature(export.Wire, export.Params, export.Result,
			definition.ParamTypes(), definition.ResultTypes()); err != nil {
			return err
		}
	}
	if _, ok := exports["_start"]; ok {
		return &appplugin.ModuleValidationError{
			Problem: "exports _start; plugins must be reactors",
		}
	}
	// The guest export table is closed: anything outside the generated ABI is
	// rejected, not just missing or mistyped declared exports.
	for _, exported := range sortedFunctionNames(exports) {
		if _, ok := domainpluginabi.GuestExportByName(exported); !ok {
			return &appplugin.UnexpectedExportError{Name: exported}
		}
	}
	return nil
}

// validateMemory requires an exported memory and enforces the manifest's
// effective cap against the raw WebAssembly memory declaration. Checking the
// raw section is necessary because wazero clamps the declared maximum to the
// runtime's page limit while decoding. An unbounded memory is always rejected:
// the guest could otherwise ask for an unbounded address space.
func validateMemory(wasm []byte, compiled wazero.CompiledModule, memoryMax int64) error {
	if len(compiled.ExportedMemories()) == 0 {
		return &appplugin.MissingExportError{Name: "memory"}
	}
	declaration, ok, err := decodeMemoryDeclaration(wasm)
	if err != nil {
		return &appplugin.ModuleValidationError{Problem: "decode memory declaration: " + err.Error()}
	}
	if !ok {
		return nil
	}
	return checkDeclaredMemory(declaration, memoryMax)
}

// validateImports enforces the exact generated v1 host-import allowlist.
// Functions must come from the generated host module and match a generated
// signature; globals, tables, and memories have no allowed imports.
func validateImports(wasm []byte, compiled wazero.CompiledModule) error {
	imports, err := decodeImports(wasm)
	if err != nil {
		return &appplugin.ModuleValidationError{Problem: "decode imports: " + err.Error()}
	}
	signatures := make(map[importKey]api.FunctionDefinition)
	for _, imported := range compiled.ImportedFunctions() {
		module, name, ok := imported.Import()
		if !ok {
			continue
		}
		signatures[importKey{module: module, name: name}] = imported
	}
	for _, imported := range imports {
		if imported.Kind != importKindFunction {
			return &appplugin.ImportError{Module: imported.Module, Name: imported.Name}
		}
		function, ok := domainpluginabi.HostFunction(imported.Module, imported.Name)
		if !ok {
			return &appplugin.ImportError{Module: imported.Module, Name: imported.Name}
		}
		definition, ok := signatures[importKey{module: imported.Module, name: imported.Name}]
		if !ok {
			return &appplugin.ImportError{Module: imported.Module, Name: imported.Name}
		}
		if err := domainpluginabi.CheckSignature(imported.Module+"."+imported.Name,
			function.Params, function.Result, definition.ParamTypes(), definition.ResultTypes()); err != nil {
			return err
		}
	}
	return nil
}

type importKey struct {
	module string
	name   string
}

// sortedFunctionNames returns exported function names in stable order so the
// reported unexpected export does not depend on map iteration.
func sortedFunctionNames(functions map[string]api.FunctionDefinition) []string {
	names := make([]string, 0, len(functions))
	for name := range functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// classifyCompileError maps a wazero compile failure to a stable typed error
// when the cause is the manifest memory cap, inverted memory bounds, or a
// forbidden proposal. It returns nil for every other compile failure so
// callers keep their own wrapping. config is the caller's selected runtime
// engine; the runtime passes its engine selection so the probe never forces
// the compiler on a platform or process that fell back to the interpreter,
// while install-time validation passes nil for wazero's automatic selection.
func classifyCompileError(ctx context.Context, wasm []byte, compileErr error,
	memoryMax int64, cache wazero.CompilationCache, config wazero.RuntimeConfig) error {
	if limitErr := probeMemoryLimit(ctx, wasm, memoryMax, cache, config); limitErr != nil {
		return limitErr
	}
	if declaration, ok, err := decodeMemoryDeclaration(wasm); err == nil && ok {
		if declaration.bounded && declaration.min > declaration.max {
			return &appplugin.ModuleValidationError{Problem: "memory minimum exceeds maximum"}
		}
	}
	if unsupportedModuleFeature(compileErr) {
		return &appplugin.ModuleValidationError{
			Problem: "uses a forbidden WebAssembly feature (threads, atomics, or shared memory): " + compileErr.Error(),
		}
	}
	return nil
}

// probeMemoryLimit re-decodes a module without the manifest cap to produce the
// stable typed memory error instead of a generic compile failure. A nil cache
// keeps the probe isolated; the runtime passes its shared cache. A nil config
// means wazero's automatic engine selection; the runtime passes the config it
// already selected so the probe matches the instance's engine.
func probeMemoryLimit(ctx context.Context, wasm []byte, memoryMax int64,
	cache wazero.CompilationCache, config wazero.RuntimeConfig) error {
	if config == nil {
		config = wazero.NewRuntimeConfig()
	}
	if cache != nil {
		config = config.WithCompilationCache(cache)
	}
	probe := wazero.NewRuntimeWithConfig(ctx, config)
	defer probe.Close(context.Background())
	compiled, err := probe.CompileModule(ctx, wasm)
	if err != nil {
		return nil
	}
	for _, memory := range compiled.ExportedMemories() {
		if err := checkMemoryLimit(memory, memoryMax); err != nil {
			return err
		}
	}
	return nil
}

func unsupportedModuleFeature(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"shared memory", "atomic", "threads", "0xfe", "unsupported feature",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// rejectStartSection rejects a core start section (section id 8). wazero runs
// that function during InstantiateModule, before natcatty_init and outside
// every plugin deadline, so a reactor ABI must not declare one even when no
// _start export exists.
func rejectStartSection(wasm []byte) error {
	err := walkSections(wasm, func(id byte, _ []byte) error {
		if id == wasmStartSection {
			return &appplugin.ModuleValidationError{
				Problem: "declares a core start section; plugins must be reactors",
			}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	var validation *appplugin.ModuleValidationError
	if errors.As(err, &validation) {
		return err
	}
	return &appplugin.ModuleValidationError{Problem: "decode sections: " + err.Error()}
}

// WebAssembly import descriptor kinds from the core binary format.
const (
	importKindFunction byte = 0
	importKindTable    byte = 1
	importKindMemory   byte = 2
	importKindGlobal   byte = 3
)

// moduleImport is one declared import. The host allowlist is intentionally
// function-only; the kind is kept so diagnostics and validation can reject
// globals, tables, and memories explicitly.
type moduleImport struct {
	Module string
	Name   string
	Kind   byte
}

const (
	// wasmImportSection is the section id holding the import vector.
	wasmImportSection byte = 2
	// wasmMemorySection is the section id holding defined memories.
	wasmMemorySection byte = 5
	// wasmStartSection is the section id holding the core start function.
	wasmStartSection byte = 8
)

// walkSections validates the core module header and calls fn for each
// top-level section payload in order. wazero clamps a declared memory maximum
// to its runtime limit while decoding, so structural validation reads the raw
// section bytes to see the declaration the guest actually shipped.
func walkSections(wasm []byte, fn func(id byte, payload []byte) error) error {
	const (
		headerSize = 8
		magic0     = 0x00
		magic1     = 0x61
		magic2     = 0x73
		magic3     = 0x6d
	)
	if len(wasm) < headerSize ||
		wasm[0] != magic0 || wasm[1] != magic1 || wasm[2] != magic2 || wasm[3] != magic3 ||
		wasm[4] != 1 || wasm[5] != 0 || wasm[6] != 0 || wasm[7] != 0 {
		return fmt.Errorf("not a version 1 core WebAssembly module")
	}
	rest := wasm[headerSize:]
	for len(rest) > 0 {
		id := rest[0]
		rest = rest[1:]
		size, n := binary.Uvarint(rest)
		if n <= 0 {
			return fmt.Errorf("section %d has an invalid size", id)
		}
		rest = rest[n:]
		if size > uint64(len(rest)) {
			return fmt.Errorf("section %d declares %d bytes, %d remain", id, size, len(rest))
		}
		payload := rest[:size]
		rest = rest[size:]
		if err := fn(id, payload); err != nil {
			return err
		}
	}
	return nil
}

// decodeImports returns the module's imports. wazero's CompiledModule exposes
// imported functions and memories but not imported globals or tables, so
// structural validation needs the raw binary to reject those. The module has
// already compiled successfully when this runs, so malformed input is reported
// as an error and rejected.
func decodeImports(wasm []byte) ([]moduleImport, error) {
	var imports []moduleImport
	err := walkSections(wasm, func(id byte, payload []byte) error {
		if id != wasmImportSection {
			return nil
		}
		parsed, err := decodeImportSection(payload)
		if err != nil {
			return err
		}
		imports = parsed
		return nil
	})
	return imports, err
}

// memoryDeclaration is one memory's declared limits exactly as encoded. Wazero
// clamps the maximum to its runtime limit during decoding, so this is the only
// reliable source for an install-time cap check.
type memoryDeclaration struct {
	min     uint32
	max     uint32
	bounded bool
}

// decodeMemoryDeclaration returns the first defined memory's declaration.
// Imported memories are rejected separately; ok is false when the module has
// no defined memory section.
func decodeMemoryDeclaration(wasm []byte) (memoryDeclaration, bool, error) {
	var declaration memoryDeclaration
	found := false
	err := walkSections(wasm, func(id byte, payload []byte) error {
		if id != wasmMemorySection || found {
			return nil
		}
		reader := &wasmReader{data: payload}
		count, err := reader.u32()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		flags, err := reader.byte()
		if err != nil {
			return err
		}
		declaration.min, err = reader.u32()
		if err != nil {
			return err
		}
		if flags&0x01 != 0 {
			declaration.max, err = reader.u32()
			if err != nil {
				return err
			}
			declaration.bounded = true
		}
		found = true
		return nil
	})
	return declaration, found, err
}

func decodeImportSection(payload []byte) ([]moduleImport, error) {
	reader := &wasmReader{data: payload}
	count, err := reader.u32()
	if err != nil {
		return nil, err
	}
	if count > uint32(len(payload)) {
		return nil, fmt.Errorf("import count %d exceeds the section size", count)
	}
	imports := make([]moduleImport, 0, count)
	for i := uint32(0); i < count; i++ {
		module, err := reader.name()
		if err != nil {
			return nil, err
		}
		name, err := reader.name()
		if err != nil {
			return nil, err
		}
		kind, err := reader.byte()
		if err != nil {
			return nil, err
		}
		switch kind {
		case importKindFunction:
			_, err = reader.u32() // type index
		case importKindTable:
			if _, err = reader.byte(); err == nil { // reference type
				err = reader.limits()
			}
		case importKindMemory:
			err = reader.limits()
		case importKindGlobal:
			if _, err = reader.byte(); err == nil { // value type
				_, err = reader.byte() // mutability
			}
		default:
			err = fmt.Errorf("unknown import kind %d", kind)
		}
		if err != nil {
			return nil, err
		}
		imports = append(imports, moduleImport{Module: module, Name: name, Kind: kind})
	}
	if !reader.done() {
		return nil, fmt.Errorf("import section has trailing bytes")
	}
	return imports, nil
}

type wasmReader struct {
	data []byte
	off  int
}

func (r *wasmReader) done() bool { return r.off == len(r.data) }

func (r *wasmReader) byte() (byte, error) {
	if r.off >= len(r.data) {
		return 0, fmt.Errorf("unexpected end of import section")
	}
	value := r.data[r.off]
	r.off++
	return value, nil
}

func (r *wasmReader) u32() (uint32, error) {
	value, n := binary.Uvarint(r.data[r.off:])
	if n <= 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("invalid unsigned LEB128 value")
	}
	r.off += n
	return uint32(value), nil
}

func (r *wasmReader) name() (string, error) {
	size, err := r.u32()
	if err != nil {
		return "", err
	}
	if uint64(size) > uint64(len(r.data)-r.off) {
		return "", fmt.Errorf("import name length %d exceeds the section", size)
	}
	raw := r.data[r.off : r.off+int(size)]
	r.off += int(size)
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("import name is not valid UTF-8")
	}
	return string(raw), nil
}

func (r *wasmReader) limits() error {
	flags, err := r.byte()
	if err != nil {
		return err
	}
	if _, err := r.u32(); err != nil { // minimum
		return err
	}
	if flags&0x01 != 0 {
		if _, err := r.u32(); err != nil { // maximum
			return err
		}
	}
	return nil
}

// checkDeclaredMemory enforces the manifest's effective cap against the raw
// WebAssembly memory declaration.
func checkDeclaredMemory(declaration memoryDeclaration, memoryMax int64) error {
	if required := int64(declaration.min) * wasmPageSize; required > memoryMax {
		return &appplugin.MemoryLimitError{Declared: memoryMax, Required: required}
	}
	if !declaration.bounded {
		return &appplugin.MemoryLimitError{
			Declared: memoryMax,
			Required: memoryMax + wasmPageSize,
		}
	}
	if required := int64(declaration.max) * wasmPageSize; required > memoryMax {
		return &appplugin.MemoryLimitError{Declared: memoryMax, Required: required}
	}
	return nil
}

// checkMemoryLimit enforces the manifest's effective memory cap against a
// wazero memory definition. It is used by the probe that recompiles without
// the manifest cap, where the declared maximum is not clamped.
func checkMemoryLimit(memory api.MemoryDefinition, memoryMax int64) error {
	if memory == nil {
		return nil
	}
	if required := int64(memory.Min()) * wasmPageSize; required > memoryMax {
		return &appplugin.MemoryLimitError{Declared: memoryMax, Required: required}
	}
	maximum, bounded := memory.Max()
	if !bounded {
		return &appplugin.MemoryLimitError{
			Declared: memoryMax,
			Required: memoryMax + wasmPageSize,
		}
	}
	if required := int64(maximum) * wasmPageSize; required > memoryMax {
		return &appplugin.MemoryLimitError{Declared: memoryMax, Required: required}
	}
	return nil
}
