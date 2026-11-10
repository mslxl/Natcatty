// Package samples_test holds the shared acceptance tests for the C, TinyGo,
// and Zig hello-command samples. The checked-in modules prove structural and
// fake-host conformance with no toolchain. The build test recompiles each
// sample from source through the CLI when the pinned toolchain is on PATH, so
// CI and `nix develop` exercise the real builds while a plain Go checkout can
// still run the artifact tests.
package samples_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	"github.com/mslxl/natcatty/sdk/cli"
	"github.com/mslxl/natcatty/sdk/conformance"
	"github.com/mslxl/natcatty/sdk/fakehost"
	"github.com/mslxl/natcatty/sdk/idl"
)

type languageSample struct {
	// Language is also the manifest's wasm.toolchain value.
	Language string
	// Directory is relative to sdk/samples.
	Directory string
	// Output is the exact visible output of the greet command.
	Output string
	// Tools are the commands build.sh needs on PATH.
	Tools []string
}

var languageSamples = []languageSample{
	{Language: "c", Directory: "hello-command-c", Output: "hello from c", Tools: []string{"clang", "wasm-ld"}},
	{Language: "tinygo", Directory: "hello-command-tinygo", Output: "hello from tinygo", Tools: []string{"tinygo", "wasm-opt"}},
	{Language: "zig", Directory: "hello-command-zig", Output: "hello from zig", Tools: []string{"zig"}},
}

// TestLanguageSampleArtifactsConform is the toolchain-free gate: every
// checked-in module passes the same structural rules as the host install path
// and runs the greet command in the fake host.
func TestLanguageSampleArtifactsConform(t *testing.T) {
	for _, sample := range languageSamples {
		t.Run(sample.Language, func(t *testing.T) {
			dir := sample.Directory
			manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
			if err != nil {
				t.Fatalf("load manifest: %v", err)
			}
			if err := manifest.Validate(); err != nil {
				t.Fatalf("validate manifest: %v", err)
			}
			if manifest.Wasm.Toolchain != sample.Language {
				t.Fatalf("manifest toolchain = %q, want %q", manifest.Wasm.Toolchain, sample.Language)
			}
			wasm, err := os.ReadFile(filepath.Join(dir, manifest.Wasm.Path))
			if err != nil {
				t.Fatalf("read module: %v", err)
			}
			if findings := conformance.CheckStructural(manifest, wasm); len(findings) > 0 {
				t.Fatalf("structural conformance: %v", findings)
			}
			report, err := fakehost.Run(t.Context(), fakehost.Options{
				Directory: dir,
				Events:    []string{"greet"},
			})
			if err != nil {
				t.Fatalf("fake host: %v", err)
			}
			if got := report.Feedback(); got != sample.Output {
				t.Fatalf("fake-host output = %q, want %q", got, sample.Output)
			}
		})
	}
}

// TestLanguageSamplesRejectIncompatibleHostABI runs each committed module
// against a host whose abi_version is 2. The generated init code must fail
// closed, return -1, and log one ABI mismatch line instead of dispatching.
func TestLanguageSamplesRejectIncompatibleHostABI(t *testing.T) {
	for _, sample := range languageSamples {
		t.Run(sample.Language, func(t *testing.T) {
			manifest, err := (adapterpluginmanifest.Source{}).Load(sample.Directory)
			if err != nil {
				t.Fatalf("load manifest: %v", err)
			}
			wasm, err := os.ReadFile(filepath.Join(sample.Directory, manifest.Wasm.Path))
			if err != nil {
				t.Fatalf("read module: %v", err)
			}
			abi, init, logs := runWithHostABI(t, wasm, 2)
			if abi != 1 {
				t.Fatalf("guest abi_version = %d, want 1", abi)
			}
			if init != -1 {
				t.Fatalf("natcatty_init = %d, want -1 for host ABI 2", init)
			}
			if !strings.Contains(logs, "ABI") || !strings.Contains(logs, "mismatch") {
				t.Fatalf("init log = %q, want an ABI mismatch line", logs)
			}
			t.Logf("host ABI 2: guest abi=%d natcatty_init=%d log=%q", abi, init, logs)
		})
	}
}

// runWithHostABI instantiates one module against a minimal host module that
// reports hostABI, then calls natcatty_abi_version and natcatty_init.
func runWithHostABI(t *testing.T, wasm []byte, hostABI int32) (abi, init int32, logs string) {
	t.Helper()
	ctx := t.Context()
	runtime := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	var captured bytes.Buffer
	builder := runtime.NewHostModuleBuilder("natcatty_host_v1")
	builder.NewFunctionBuilder().
		WithFunc(func() int32 { return hostABI }).
		Export("abi_version")
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, level, ptr, length uint32) {
			if length == 0 {
				return
			}
			if data, ok := mod.Memory().Read(ptr, length); ok {
				captured.Write(data)
			}
		}).
		Export("log")
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) int32 {
			return int32(length)
		}).
		Export("output_write")
	if _, err := builder.Instantiate(ctx); err != nil {
		t.Fatalf("instantiate host module: %v", err)
	}
	compiled, err := runtime.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatalf("compile module: %v", err)
	}
	module, err := runtime.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		t.Fatalf("instantiate module: %v", err)
	}

	abiFn := module.ExportedFunction("natcatty_abi_version")
	initFn := module.ExportedFunction("natcatty_init")
	if abiFn == nil || initFn == nil {
		t.Fatal("module is missing natcatty_abi_version or natcatty_init")
	}
	abiResults, err := abiFn.Call(ctx)
	if err != nil {
		t.Fatalf("natcatty_abi_version: %v", err)
	}
	initResults, err := initFn.Call(ctx)
	if err != nil {
		t.Fatalf("natcatty_init: %v", err)
	}
	return int32(uint32(abiResults[0])), int32(uint32(initResults[0])), captured.String()
}

// TestLanguageSamplesBuildCheckRunAndDev recompiles every sample from source in
// a temporary copy, then drives the CLI build, check, run, and dev verbs. It
// skips with an actionable message when the pinned toolchain is absent unless
// NATCATTY_REQUIRE_LANGUAGE_SDKS=1, which is what CI and the Nix dev shell set.
func TestLanguageSamplesBuildCheckRunAndDev(t *testing.T) {
	require := os.Getenv("NATCATTY_REQUIRE_LANGUAGE_SDKS") == "1"
	for _, sample := range languageSamples {
		t.Run(sample.Language, func(t *testing.T) {
			missing := missingTools(sample.Tools)
			if len(missing) > 0 {
				if require {
					t.Fatalf("NATCATTY_REQUIRE_LANGUAGE_SDKS=1 but %s is missing", strings.Join(missing, ", "))
				}
				t.Skipf("skipping the %s build: %s not on PATH (enter nix develop to run it)",
					sample.Language, strings.Join(missing, ", "))
			}

			source := sample.Directory
			dir := filepath.Join(t.TempDir(), sample.Directory)
			if err := os.CopyFS(dir, os.DirFS(source)); err != nil {
				t.Fatalf("copy sample: %v", err)
			}
			buildScript := filepath.Join(dir, "build.sh")
			if err := os.Chmod(buildScript, 0o755); err != nil {
				t.Fatalf("chmod build.sh: %v", err)
			}
			// Delete the committed artifact so a successful build proves the
			// declared toolchain ran instead of a stale file being reused.
			manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
			if err != nil {
				t.Fatalf("load copied manifest: %v", err)
			}
			if err := os.Remove(filepath.Join(dir, manifest.Wasm.Path)); err != nil {
				t.Fatalf("remove committed artifact: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var stdout, stderr bytes.Buffer
			opts := cli.Options{Stdout: &stdout, Stderr: &stderr}
			if err := cli.Run(ctx, []string{"build", dir}, opts); err != nil {
				t.Fatalf("CLI build: %v\nstderr:\n%s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), "built ") {
				t.Fatalf("CLI build stdout = %q", stdout.String())
			}
			built, err := (adapterpluginmanifest.Source{}).Load(dir)
			if err != nil {
				t.Fatalf("reload built manifest: %v", err)
			}
			if built.Wasm.SHA256 == strings.Repeat("0", 64) {
				t.Fatal("CLI build did not refresh the manifest hash")
			}
			if _, err := os.Stat(filepath.Join(dir, built.Wasm.Path)); err != nil {
				t.Fatalf("built module: %v", err)
			}

			stdout.Reset()
			stderr.Reset()
			if err := cli.Run(ctx, []string{"check", dir}, opts); err != nil {
				t.Fatalf("CLI check: %v\nstderr:\n%s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), "conformant") {
				t.Fatalf("CLI check stdout = %q", stdout.String())
			}
			report, err := conformance.Check(ctx, conformance.Options{Directory: dir})
			if err != nil {
				t.Fatalf("conformance.Check: %v", err)
			}
			if !report.OK() {
				t.Fatalf("conformance report: %+v", report)
			}

			stdout.Reset()
			stderr.Reset()
			if err := cli.Run(ctx, []string{"run", dir, "--event", "greet"}, opts); err != nil {
				t.Fatalf("CLI run: %v\nstderr:\n%s", err, stderr.String())
			}
			if !strings.Contains(stdout.String(), "output "+sample.Output) {
				t.Fatalf("CLI run stdout = %q, want output %q", stdout.String(), sample.Output)
			}

			stdout.Reset()
			stderr.Reset()
			var launched string
			opts.Launcher = func(_ context.Context, binary, directory string, _, _ io.Writer) error {
				launched = directory
				if binary != "fake-natcatty" {
					t.Errorf("dev binary = %q, want fake-natcatty", binary)
				}
				return nil
			}
			if err := cli.Run(ctx, []string{"dev", dir, "--app", "fake-natcatty"}, opts); err != nil {
				t.Fatalf("CLI dev: %v\nstderr:\n%s", err, stderr.String())
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				t.Fatalf("absolute sample directory: %v", err)
			}
			if launched != abs {
				t.Fatalf("dev launched directory = %q, want %q", launched, abs)
			}
		})
	}
}

func missingTools(tools []string) []string {
	var missing []string
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

// TestLanguageSamplesUseOnlyTheGeneratedABI is a readable cross-check that the
// checked-in artifacts import exactly the three host functions the samples
// call, all from the generated v1 module, and nothing from WASI.
func TestLanguageSamplesUseOnlyTheGeneratedABI(t *testing.T) {
	for _, sample := range languageSamples {
		t.Run(sample.Language, func(t *testing.T) {
			manifest, err := (adapterpluginmanifest.Source{}).Load(sample.Directory)
			if err != nil {
				t.Fatalf("load manifest: %v", err)
			}
			wasm, err := os.ReadFile(filepath.Join(sample.Directory, manifest.Wasm.Path))
			if err != nil {
				t.Fatalf("read module: %v", err)
			}
			ctx := t.Context()
			runtime := wazero.NewRuntime(ctx)
			defer runtime.Close(context.Background())
			compiled, err := runtime.CompileModule(ctx, wasm)
			if err != nil {
				t.Fatalf("compile module: %v", err)
			}
			want := map[string]bool{"abi_version": true, "log": true, "output_write": true}
			seen := map[string]bool{}
			for _, imported := range compiled.ImportedFunctions() {
				module, name, ok := imported.Import()
				if !ok {
					continue
				}
				if module != "natcatty_host_v1" {
					t.Errorf("module imports %s.%s outside the generated host module", module, name)
					continue
				}
				if !want[name] {
					t.Errorf("module imports unexpected host function %s", name)
				}
				seen[name] = true
			}
			for name := range want {
				if !seen[name] {
					t.Errorf("module does not import the generated host function %s", name)
				}
			}
		})
	}
}

// TestLanguageSampleABIValues proves the source manifests, not just the
// artifacts, declare the generated ABI major.
func TestLanguageSampleABIValues(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("parse IDL: %v", err)
	}
	for _, sample := range languageSamples {
		t.Run(sample.Language, func(t *testing.T) {
			manifest, err := (adapterpluginmanifest.Source{}).Load(sample.Directory)
			if err != nil {
				t.Fatalf("load manifest: %v", err)
			}
			if manifest.ABI != definition.ABI {
				t.Fatalf("manifest abi = %d, want generated ABI %d", manifest.ABI, definition.ABI)
			}
		})
	}
}
