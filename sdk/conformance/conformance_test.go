package conformance_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/sdk/conformance"
	"github.com/mslxl/natcatty/sdk/fakehost"
)

func TestStructuralAcceptsAValidReactor(t *testing.T) {
	dir := writePlugin(t, "hello.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	findings := conformance.CheckStructural(manifest, wasm)
	if len(findings) != 0 {
		t.Fatalf("findings = %v", findings)
	}
}

func TestSampleCommandPluginPasses(t *testing.T) {
	dir := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "hello-command")
	report, err := conformance.Check(context.Background(), conformance.Options{
		Directory: dir,
		StartNS:   1000,
		StepNS:    10,
		Seed:      42,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() {
		t.Fatalf("report = %+v", report)
	}
	if report.Behavior == nil || len(report.Behavior.Events) != 1 {
		t.Fatalf("behavior = %+v", report.Behavior)
	}
	if got := report.Behavior.Outputs[0]; !strings.HasPrefix(got, "hello from plugin now=1000 rand=") {
		t.Fatalf("output = %q", got)
	}
}

// TestSampleHostServicesPluginAcceptsGeneratedServiceImports proves the
// conformance rules accept the generated natcatty:svc/* imports the sample
// guest uses, including their real wasm signatures. It stops at the structural
// check because dispatch needs live host services.
func TestSampleHostServicesPluginAcceptsGeneratedServiceImports(t *testing.T) {
	dir := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "host-services")
	manifest, wasm := load(t, dir)
	findings := conformance.CheckStructural(manifest, wasm)
	if len(findings) != 0 {
		t.Fatalf("findings = %v", findings)
	}
	if len(manifest.Permissions.Optional) == 0 {
		t.Fatal("sample manifest grants no optional permissions")
	}
	for _, finding := range findings {
		if finding.Rule == conformance.RuleImports {
			t.Fatalf("service imports rejected: %v", finding)
		}
	}
}

// TestSampleServicePluginsPassStructuralConformance proves the generated ABI
// accepts the broker's services host module, the service_respond import, and
// the natcatty_service_request export used by the committed cross-plugin
// samples.
func TestSampleServicePluginsPassStructuralConformance(t *testing.T) {
	for _, name := range []string{"service-provider", "service-consumer"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", name)
			manifest, wasm := load(t, dir)
			findings := conformance.CheckStructural(manifest, wasm)
			if len(findings) != 0 {
				t.Fatalf("findings = %v", findings)
			}
			if len(manifest.Provides) == 0 || len(manifest.Inject) == 0 {
				t.Fatalf("sample manifest declares provides=%d inject=%d",
					len(manifest.Provides), len(manifest.Inject))
			}
		})
	}
}

func TestStructuralRejectsWrongABI(t *testing.T) {
	dir := writePlugin(t, "hello.wasm", 2, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleABI)
}

func TestStructuralRejectsMissingExport(t *testing.T) {
	dir := writePlugin(t, "missing-export.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleExports)
}

func TestStructuralRejectsWrongExportSignature(t *testing.T) {
	dir := writePlugin(t, "bad-signature.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleExports)
}

func TestStructuralRejectsExtraExport(t *testing.T) {
	dir := writePlugin(t, "extra-export.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	findings := conformance.CheckStructural(manifest, wasm)
	assertRule(t, findings, conformance.RuleExports)
	for _, finding := range findings {
		if finding.Rule == conformance.RuleExports &&
			strings.Contains(finding.Message, "natcatty_extra") {
			return
		}
	}
	t.Fatalf("findings = %v, want an unsupported export finding for natcatty_extra", findings)
}

func TestStructuralRejectsWrongImportSignature(t *testing.T) {
	dir := writePlugin(t, "bad-signature.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	findings := conformance.CheckStructural(manifest, wasm)
	assertRule(t, findings, conformance.RuleImports)
	for _, finding := range findings {
		if finding.Rule == conformance.RuleImports &&
			!strings.Contains(finding.Message, "natcatty_host_v1.log") {
			t.Fatalf("import finding = %q", finding.Message)
		}
	}
}

func TestStructuralRejectsUnsupportedImport(t *testing.T) {
	dir := writePlugin(t, "bad-import.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleImports)
}

func TestStructuralRejectsForbiddenWASI(t *testing.T) {
	dir := writePlugin(t, "wasi-import.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleWASI)
}

func TestStructuralRejectsUnboundedMemory(t *testing.T) {
	dir := writePlugin(t, "unbounded-memory.wasm", 1, 64, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleMemoryMaximum)
}

func TestStructuralRejectsMemoryAboveTheManifestMaximum(t *testing.T) {
	dir := writePlugin(t, "hello.wasm", 1, 1, true)
	manifest, wasm := load(t, dir)
	assertRule(t, conformance.CheckStructural(manifest, wasm), conformance.RuleMemoryMaximum)
}

func TestBehavioralConformanceRunsDeclaredCommands(t *testing.T) {
	dir := writePlugin(t, "hello.wasm", 1, 64, true)
	report, err := conformance.Check(context.Background(), conformance.Options{
		Directory: dir,
		Seed:      7,
		StartNS:   100,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() {
		t.Fatalf("report = %+v", report)
	}
	if report.Behavior == nil || len(report.Behavior.Events) != 1 {
		t.Fatalf("behavior = %+v", report.Behavior)
	}
	if report.Behavior.Events[0].Command != "plugin.acme.conformance.greet" {
		t.Fatalf("event = %q", report.Behavior.Events[0].Command)
	}
	if got := report.Behavior.Feedback(); got != "hello from plugin" {
		t.Fatalf("feedback = %q", got)
	}
}

// TestBehavioralConformanceRejectsAWrongABIExport covers the case structural
// validation cannot see: the manifest declares ABI 1 but the exported
// natcatty_abi_version returns 2. Only the runtime can read it.
func TestBehavioralConformanceRejectsAWrongABIExport(t *testing.T) {
	dir := writePlugin(t, "bad-abi.wasm", 1, 64, true)
	report, err := conformance.Check(context.Background(), conformance.Options{Directory: dir})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.OK() {
		t.Fatalf("report passed: %+v", report)
	}
	if report.BehaviorErr == nil {
		t.Fatal("the ABI mismatch was not reported behaviorally")
	}
}

func TestBehavioralConformanceReportsInitFailure(t *testing.T) {
	dir := writePlugin(t, "init-fail.wasm", 1, 64, true)
	report, err := conformance.Check(context.Background(), conformance.Options{Directory: dir})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.OK() {
		t.Fatalf("report passed: %+v", report)
	}
	if report.BehaviorErr == nil {
		t.Fatal("behavioral failure was not reported")
	}
}

func TestStructuralOnlySkipsTheGuest(t *testing.T) {
	dir := writePlugin(t, "hello.wasm", 1, 64, true)
	report, err := conformance.Check(context.Background(), conformance.Options{
		Directory:      dir,
		StructuralOnly: true,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() || report.Behavior != nil {
		t.Fatalf("report = %+v", report)
	}
}

// TestSixSamplesMatchOnFakeAndRealRuntimes is the behavioral parity gate: the
// same derived scenario runs against the fake host and the production
// container, and every observable surface must agree.
func TestSixSamplesMatchOnFakeAndRealRuntimes(t *testing.T) {
	checks := map[string]func(*testing.T, conformance.Report){
		"counter-view": func(t *testing.T, report conformance.Report) {
			if len(report.Behavior.UIEvents) != 2 || len(report.Runtime.UIEvents) != 2 {
				t.Fatalf("ui events fake=%+v real=%+v",
					report.Behavior.UIEvents, report.Runtime.UIEvents)
			}
			if !strings.Contains(strings.Join(report.Runtime.Outputs, "\n"), "counter=1") {
				t.Fatalf("real outputs = %v", report.Runtime.Outputs)
			}
		},
		"webview-view": func(t *testing.T, report conformance.Report) {
			output := strings.Join(report.Runtime.Outputs, "\n")
			if !strings.Contains(output, "handled=1") || !strings.Contains(output, "last=greet") {
				t.Fatalf("real output missing the bridge result:\n%s", output)
			}
		},
		"loopback-provider": func(t *testing.T, report conformance.Report) {
			if len(report.Behavior.Provider) != 4 || len(report.Runtime.Provider) != 4 {
				t.Fatalf("provider calls fake=%+v real=%+v",
					report.Behavior.Provider, report.Runtime.Provider)
			}
			if string(report.Runtime.Provider[2].Output) != "fake host\n" {
				t.Fatalf("real pump output = %q", report.Runtime.Provider[2].Output)
			}
		},
		"data-provider": func(t *testing.T, report conformance.Report) {
			if len(report.Behavior.Data) != 2 || len(report.Runtime.Data) != 2 {
				t.Fatalf("data calls fake=%+v real=%+v",
					report.Behavior.Data, report.Runtime.Data)
			}
			if report.Runtime.Data[1].Rows != 3 || !report.Runtime.Data[1].More {
				t.Fatalf("real query = %+v", report.Runtime.Data[1])
			}
		},
		"mcp-tools": func(t *testing.T, report conformance.Report) {
			if len(report.Behavior.Tools) != 4 || len(report.Runtime.Tools) != 4 {
				t.Fatalf("tool calls fake=%+v real=%+v",
					report.Behavior.Tools, report.Runtime.Tools)
			}
			if !strings.Contains(report.Runtime.Tools[0].Result, "echo: fake host") {
				t.Fatalf("real echo = %+v", report.Runtime.Tools[0])
			}
		},
		"host-services": func(t *testing.T, report conformance.Report) {
			output := strings.Join(report.Runtime.Outputs, "\n")
			if !strings.Contains(output, "selftest.done") {
				t.Fatalf("real output missing selftest report:\n%s", output)
			}
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			report, err := conformance.Check(context.Background(), conformance.Options{
				Directory: filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", name),
			})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !report.OK() {
				t.Fatalf("report = %+v (behavior=%v runtime=%v parity=%v)",
					report, report.BehaviorErr, report.RuntimeErr, report.ParityErr)
			}
			if report.Behavior == nil || report.Runtime == nil {
				t.Fatalf("missing run: behavior=%v runtime=%v", report.Behavior, report.Runtime)
			}
			if report.ParitySkipped != "" {
				t.Fatalf("parity unexpectedly skipped: %s", report.ParitySkipped)
			}
			check(t, report)
		})
	}
}

// TestCrossPluginSamplesRunBothRuntimesButSkipParity documents the one
// deliberate parity gap: the single-instance fake host has no service broker.
func TestCrossPluginSamplesRunBothRuntimesButSkipParity(t *testing.T) {
	for _, name := range []string{"service-provider", "service-consumer"} {
		t.Run(name, func(t *testing.T) {
			report, err := conformance.Check(context.Background(), conformance.Options{
				Directory: filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", name),
			})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !report.OK() {
				t.Fatalf("report = %+v", report)
			}
			if report.Behavior == nil || report.Runtime == nil {
				t.Fatalf("both runtimes must execute: %+v", report)
			}
			if report.ParitySkipped == "" {
				t.Fatal("cross-plugin parity was not marked inapplicable")
			}
		})
	}
}

func TestEventOverrideReplacesTheDerivedScenario(t *testing.T) {
	report, err := conformance.Check(context.Background(), conformance.Options{
		Directory: filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "hello-command"),
		Events:    []string{"greet", "greet"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Behavior.Events) != 2 || len(report.Runtime.Events) != 2 {
		t.Fatalf("events fake=%+v real=%+v", report.Behavior.Events, report.Runtime.Events)
	}
}

func TestProviderResizeAndSignalMatchAcrossRuntimes(t *testing.T) {
	scenario := fakehost.Scenario{ProviderCalls: []fakehost.ProviderCall{
		{Method: uint32(domainpluginabi.ProviderMethodOpen),
			Payload: `{"target":"sample","hostId":"host-1"}`},
		{Method: uint32(domainpluginabi.ProviderMethodResize),
			Payload: `{"stream":1,"cols":100,"rows":30}`},
		{Method: uint32(domainpluginabi.ProviderMethodSignal),
			Payload: `{"stream":1,"name":"interrupt"}`},
		{Method: uint32(domainpluginabi.ProviderMethodClose), Payload: `{"stream":1}`},
	}}
	report, err := conformance.Check(context.Background(), conformance.Options{
		Directory: filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "loopback-provider"),
		Scenario:  &scenario,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.OK() {
		t.Fatalf("report = %+v (parity=%v runtime=%v)", report, report.ParityErr, report.RuntimeErr)
	}
	if len(report.Runtime.Provider) != 4 {
		t.Fatalf("runtime provider observations = %+v", report.Runtime.Provider)
	}
}

func assertRule(t *testing.T, findings []conformance.Finding, rule string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Rule == rule {
			return
		}
	}
	t.Fatalf("findings = %v, want rule %q", findings, rule)
}

func load(t *testing.T, dir string) (domainplugin.Manifest, []byte) {
	t.Helper()
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	return manifest, wasm
}

// writePlugin writes a minimal valid manifest beside a committed fixture.
func writePlugin(t *testing.T, wasmName string, abiVersion, memoryMiB int, command bool) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(testutil.ModuleRoot(t), "adapters", "wasm", "testdata", wasmName)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture %s: %v", wasmName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), data, 0o644); err != nil {
		t.Fatalf("write module: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "i18n"), 0o755); err != nil {
		t.Fatalf("create catalog directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "i18n", "en.toml"),
		[]byte("command_greet = \"Say Hello\"\n"), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	contributions := ""
	if command {
		contributions = `
[[contributions.commands]]
id = "greet"
title = "Say Hello"
`
	}
	manifest := strings.Join([]string{
		"manifest_version = 1",
		`id = "acme.conformance"`,
		`name = "Acme Conformance"`,
		`version = "1.0.0"`,
		`plugin_api = "^0.1"`,
		"abi = " + strconv.Itoa(abiVersion),
		`default_locale = "en"`,
		"[wasm]",
		`path = "plugin.wasm"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		"memory_max_mib = " + strconv.Itoa(memoryMiB),
		"[permissions]",
		`required = []`,
		`optional = []`,
		contributions,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}
