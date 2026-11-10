// Package conformance validates a plugin before an author, the CLI, or CI
// runs it in the app.
//
// Structural conformance inspects the manifest and the WebAssembly binary
// without executing guest code: ABI declaration, required exports, import
// allowlist, forbidden WASI, and the memory maximum. Behavioral conformance
// then runs the module in the fake host, dispatching every declared command.
package conformance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
	"github.com/mslxl/natcatty/sdk/fakehost"
	"github.com/mslxl/natcatty/sdk/internal/plugindir"
)

// Rule names identify the structural check that failed.
const (
	RuleManifest      = "manifest"
	RuleModule        = "module"
	RuleABI           = "abi"
	RuleExports       = "exports"
	RuleImports       = "imports"
	RuleWASI          = "wasi"
	RuleMemoryMaximum = "memory-maximum"
	RuleReactor       = "reactor"
)

// Finding is one failed structural check.
type Finding struct {
	Rule    string
	Message string
}

func (f Finding) String() string { return f.Rule + ": " + f.Message }

// Options configures a conformance check.
type Options struct {
	// Directory is the unpacked plugin directory.
	Directory string
	// Events overrides the derived command events, whether they came from the
	// manifest or from Scenario. The default dispatches every command the
	// manifest contributes.
	Events []string
	// Scenario overrides the derived scenario. The fake host and the real
	// runtime receive the same value.
	Scenario *fakehost.Scenario
	// StartNS, StepNS, and Seed configure both behavioral environments.
	StartNS int64
	StepNS  int64
	Seed    uint64
	// Limits overrides runtime deadlines; zero means the contract defaults.
	Limits appplugin.Limits
	// DeniedPermissions withholds named permissions in both environments.
	DeniedPermissions []string
	// StructuralOnly skips every behavioral run.
	StructuralOnly bool
}

// Report is the conformance result.
type Report struct {
	Findings    []Finding
	Behavior    *fakehost.Report
	BehaviorErr error
	// Runtime is the same scenario executed by the production Container.
	Runtime    *fakehost.Report
	RuntimeErr error
	// ParityErr reports an observable difference between the fake host and the
	// real runtime.
	ParityErr error
	// ParitySkipped names why the comparison was not applicable. The
	// single-instance fake host does not simulate cross-plugin service peers.
	ParitySkipped string
}

// OK reports whether every structural and behavioral check passed.
func (r Report) OK() bool {
	return len(r.Findings) == 0 && r.BehaviorErr == nil &&
		r.RuntimeErr == nil && r.ParityErr == nil
}

// Check loads a plugin directory and validates it. A load failure is an error;
// validation failures are findings in the returned report.
func Check(ctx context.Context, opts Options) (Report, error) {
	report := Report{}
	dir, err := filepath.Abs(opts.Directory)
	if err != nil {
		return report, fmt.Errorf("conformance: resolve %q: %w", opts.Directory, err)
	}
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		return report, fmt.Errorf("conformance: load manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		report.Findings = []Finding{{Rule: RuleManifest, Message: err.Error()}}
		return report, nil
	}
	wasmPath, err := plugindir.Resolve(dir, manifest.Wasm.Path)
	if err != nil {
		return report, fmt.Errorf("conformance: %w", err)
	}
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return report, fmt.Errorf("conformance: read module %q: %w", manifest.Wasm.Path, err)
	}
	report.Findings = CheckStructural(manifest, wasmBytes)
	if len(report.Findings) > 0 || opts.StructuralOnly {
		return report, nil
	}
	scenario := DefaultScenario(manifest)
	if opts.Scenario != nil {
		scenario = *opts.Scenario
	}
	if len(opts.Events) > 0 {
		scenario.Events = append([]string(nil), opts.Events...)
	}
	events := scenario.Events
	behavior, behaviorErr := fakehost.Run(ctx, fakehost.Options{
		Directory:         dir,
		Events:            events,
		Scenario:          scenario,
		StubServices:      false,
		DeniedPermissions: opts.DeniedPermissions,
		StartNS:           opts.StartNS,
		StepNS:            opts.StepNS,
		Seed:              opts.Seed,
		Limits:            opts.Limits,
	})
	report.Behavior = &behavior
	report.BehaviorErr = behaviorErr

	runtimeReport, runtimeErr := runReal(ctx, opts, scenario)
	report.Runtime = &runtimeReport
	report.RuntimeErr = runtimeErr
	parityReason := parityNotApplicable(manifest)
	switch {
	case behaviorErr != nil || runtimeErr != nil:
		// The failing environment already makes the report non-conformant.
	case parityReason != "":
		report.ParitySkipped = parityReason

	default:
		if diff := compareReports(behavior, runtimeReport); diff != "" {
			report.ParityErr = fmt.Errorf("fake host and real runtime differ: %s", diff)
		}
	}
	return report, nil
}

// CheckStructural checks one manifest and module without executing it.
func CheckStructural(manifest domainplugin.Manifest, wasmBytes []byte) []Finding {
	var findings []Finding
	add := func(rule, format string, args ...any) {
		findings = append(findings, Finding{Rule: rule, Message: fmt.Sprintf(format, args...)})
	}
	if err := manifest.Validate(); err != nil {
		add(RuleManifest, "%v", err)
	}
	if manifest.ABI != domainpluginabi.Version {
		add(RuleABI, "manifest declares ABI %d, generated bindings speak %d",
			manifest.ABI, domainpluginabi.Version)
	}
	if len(wasmBytes) == 0 {
		add(RuleModule, "module is empty")
		return findings
	}

	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)
	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		add(RuleModule, "compile module: %v", err)
		return findings
	}
	defer compiled.Close(ctx)

	exports := compiled.ExportedFunctions()
	for _, required := range domainpluginabi.GuestExports {
		definition, ok := exports[required.Wire]
		if !ok {
			if required.Optional {
				continue
			}
			add(RuleExports, "missing required export %s", required.Wire)
			continue
		}
		if err := domainpluginabi.CheckSignature(required.Wire, required.Params, required.Result,
			definition.ParamTypes(), definition.ResultTypes()); err != nil {
			add(RuleExports, "%v", err)
		}
	}
	if _, ok := exports["_start"]; ok {
		add(RuleReactor, "module exports _start; plugins must be reactors")
	}
	// The guest export table is closed: anything outside the generated ABI is
	// rejected, not just missing or mistyped declared exports.
	for _, name := range sortedFunctionNames(exports) {
		if name == "_start" {
			// Already reported as the more specific reactor violation.
			continue
		}
		if _, ok := domainpluginabi.GuestExportByName(name); !ok {
			add(RuleExports, "module exports unsupported function %s", name)
		}
	}

	memory, ok := exportedMemory(compiled)
	if !ok {
		add(RuleExports, "missing exported memory")
	} else {
		effective := manifest.Wasm.EffectiveMemoryMaxBytes()
		if minimum := int64(memory.Min()) * 64 << 10; minimum > effective {
			add(RuleMemoryMaximum,
				"module initial memory is %d bytes, above the declared %d byte maximum",
				minimum, effective)
		}
		maximum, bounded := memory.Max()
		if !bounded {
			add(RuleMemoryMaximum, "module declares an unbounded linear memory")
		} else if maximumBytes := int64(maximum) * 64 << 10; maximumBytes > effective {
			add(RuleMemoryMaximum,
				"module memory maximum is %d bytes, above the declared %d byte maximum",
				maximumBytes, effective)
		}
	}

	for _, imported := range compiled.ImportedFunctions() {
		module, name, isImport := imported.Import()
		if !isImport {
			continue
		}
		if isWASI(module) {
			add(RuleWASI, "module imports forbidden WASI function %s.%s", module, name)
			continue
		}
		function, ok := domainpluginabi.HostFunction(module, name)
		if !ok {
			add(RuleImports, "module imports unsupported function %s.%s", module, name)
			continue
		}
		if err := domainpluginabi.CheckSignature(module+"."+name, function.Params, function.Result,
			imported.ParamTypes(), imported.ResultTypes()); err != nil {
			add(RuleImports, "%v", err)
		}
	}
	for _, imported := range compiled.ImportedMemories() {
		module, name, _ := imported.Import()
		add(RuleImports, "module imports memory %s.%s; plugins must own their memory", module, name)
	}
	return findings
}

// sortedFunctionNames returns exported function names in stable order so
// findings do not depend on map iteration.
func sortedFunctionNames(functions map[string]api.FunctionDefinition) []string {
	names := make([]string, 0, len(functions))
	for name := range functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func exportedMemory(compiled wazero.CompiledModule) (api.MemoryDefinition, bool) {
	memories := compiled.ExportedMemories()
	if memory, ok := memories["memory"]; ok {
		return memory, true
	}
	for _, memory := range memories {
		return memory, true
	}
	return nil, false
}

func isWASI(module string) bool {
	return module == "wasi_snapshot_preview1" || module == "wasi_unstable" ||
		strings.HasPrefix(module, "wasi:")
}
