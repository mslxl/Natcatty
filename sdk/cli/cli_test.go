package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/sdk/cli"
)

func sampleDirectory(t *testing.T) string {
	t.Helper()
	return filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "hello-command")
}

func run(t *testing.T, opts cli.Options, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.Stdout = &stdout
	opts.Stderr = &stderr
	err := cli.Run(context.Background(), args, opts)
	return stdout.String(), stderr.String(), err
}

func TestInitScaffoldsAStandalonePlugin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	stdout, _, err := run(t, cli.Options{}, "init", dir, "--id", "acme.init", "--name", "Acme Init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(stdout, "created") {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, relative := range []string{
		"plugin.toml", "Cargo.toml", ".cargo/config.toml", ".gitignore",
		filepath.Join("src", "lib.rs"), filepath.Join("src", "abi_v1.rs"),
	} {
		if _, err := os.Stat(filepath.Join(dir, relative)); err != nil {
			t.Fatalf("scaffold is missing %s: %v", relative, err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `id = "acme.init"`) {
		t.Fatalf("manifest = %s", manifest)
	}
	loaded, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		t.Fatalf("load scaffolded manifest: %v", err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("validate scaffolded manifest: %v", err)
	}
	if loaded.Wasm.Toolchain != "rust" || loaded.Description == "" {
		t.Fatalf("scaffolded manifest fields = %+v", loaded)
	}
	if len(loaded.Permissions.Required) != 1 || loaded.Permissions.Required[0] != "commands" {
		t.Fatalf("scaffolded permissions = %+v", loaded.Permissions)
	}
	if loaded.Wasm.EffectiveMemoryMaxMiB() != 64 {
		t.Fatalf("scaffolded memory maximum = %d", loaded.Wasm.EffectiveMemoryMaxMiB())
	}
	bindings, err := os.ReadFile(filepath.Join(dir, "src", "abi_v1.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bindings), "ensure_compatible_abi") ||
		!strings.Contains(string(bindings), "AbiMismatch") {
		t.Fatal("scaffolded bindings do not carry the typed ABI check")
	}
	config, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "--max-memory=67108864") {
		t.Fatal("scaffold does not set the required memory maximum")
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, cli.Options{}, "init", dir)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("init error = %v", err)
	}
}

func TestCheckSamplePlugin(t *testing.T) {
	stdout, _, err := run(t, cli.Options{}, "check", sampleDirectory(t))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(stdout, "conformant") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestCheckRunsBothRuntimes(t *testing.T) {
	stdout, _, err := run(t, cli.Options{}, "check", sampleDirectory(t))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(stdout, "fake host + real runtime") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestCheckRejectsANonConformantModule(t *testing.T) {
	dir := writePlugin(t, "wasi-import.wasm")
	_, stderr, err := run(t, cli.Options{}, "check", dir)
	var conformanceErr *cli.ConformanceError
	if !errors.As(err, &conformanceErr) {
		t.Fatalf("check error = %v, want ConformanceError", err)
	}
	if !strings.Contains(stderr, "wasi") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunUsesTheFakeHost(t *testing.T) {
	stdout, _, err := run(t, cli.Options{}, "run", sampleDirectory(t),
		"--event", "greet", "--seed", "7", "--start-ns", "1000", "--json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var report struct {
		PluginID string   `json:"plugin"`
		Outputs  []string `json:"outputs"`
		Events   []string `json:"events"`
		ClockEnd int64    `json:"clock_end_ns"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if report.PluginID != "acme.hello" {
		t.Fatalf("plugin = %q", report.PluginID)
	}
	if len(report.Events) != 1 || report.Events[0] != "plugin.acme.hello.greet" {
		t.Fatalf("events = %v", report.Events)
	}
	if len(report.Outputs) != 1 || !strings.HasPrefix(report.Outputs[0], "hello from plugin now=1000") {
		t.Fatalf("outputs = %v", report.Outputs)
	}
}

func TestBuildRefreshesTheManifestAndStaysConformant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "built")
	if _, _, err := run(t, cli.Options{}, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	sampleWasm, err := os.ReadFile(filepath.Join(sampleDirectory(t), "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, workDir, name string, args []string,
		_, _ io.Writer) error {
		if name != "cargo" {
			t.Fatalf("runner command = %q", name)
		}
		target := filepath.Join(workDir, "target", "wasm32-wasip1", "release", "acme_hello.wasm")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, sampleWasm, 0o644)
	}
	_, _, err = run(t, cli.Options{Runner: runner}, "build", dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), strings.Repeat("0", 64)) {
		t.Fatal("build did not refresh the sha256")
	}
	if _, _, err := run(t, cli.Options{}, "run", dir, "--event", "greet", "--start-ns", "5"); err != nil {
		t.Fatalf("run after build: %v", err)
	}
}

func TestBuildDispatchesTheDeclaredCToolchain(t *testing.T) {
	dir := t.TempDir()
	sampleWasm, err := os.ReadFile(filepath.Join(sampleDirectory(t), "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Join([]string{
		"manifest_version = 1",
		`id = "acme.cbuild"`,
		`name = "Acme C Build"`,
		`version = "1.0.0"`,
		`plugin_api = "^0.1"`,
		"abi = 1",
		`default_locale = "en"`,
		"[wasm]",
		`path = "plugin.wasm"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		"memory_max_mib = 64",
		`toolchain = "c"`,
		"[permissions]",
		`required = []`,
		`optional = []`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, workDir, name string, args []string,
		_, _ io.Writer) error {
		gotName, gotArgs = name, args
		return os.WriteFile(filepath.Join(workDir, "plugin.wasm"), sampleWasm, 0o644)
	}
	stdout, _, err := run(t, cli.Options{Runner: runner}, "build", dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if gotName != "bash" || len(gotArgs) != 1 || gotArgs[0] != "build.sh" {
		t.Fatalf("toolchain command = %q %v, want bash build.sh", gotName, gotArgs)
	}
	if !strings.Contains(stdout, "built") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestBuildDispatchesTheDeclaredScriptToolchains(t *testing.T) {
	sampleWasm, err := os.ReadFile(filepath.Join(sampleDirectory(t), "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, toolchain := range []string{"tinygo", "zig"} {
		t.Run(toolchain, func(t *testing.T) {
			dir := t.TempDir()
			manifest := strings.Join([]string{
				"manifest_version = 1",
				`id = "acme.scriptbuild"`,
				`name = "Acme Script Build"`,
				`version = "1.0.0"`,
				`plugin_api = "^0.1"`,
				"abi = 1",
				`default_locale = "en"`,
				"[wasm]",
				`path = "plugin.wasm"`,
				`sha256 = "` + strings.Repeat("0", 64) + `"`,
				"memory_max_mib = 64",
				`toolchain = "` + toolchain + `"`,
				"[permissions]",
				`required = []`,
				`optional = []`,
			}, "\n")
			if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "build.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			var gotName string
			var gotArgs []string
			runner := func(_ context.Context, workDir, name string, args []string,
				_, _ io.Writer) error {
				gotName, gotArgs = name, args
				return os.WriteFile(filepath.Join(workDir, "plugin.wasm"), sampleWasm, 0o644)
			}
			stdout, _, err := run(t, cli.Options{Runner: runner}, "build", dir)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if gotName != "bash" || len(gotArgs) != 1 || gotArgs[0] != "build.sh" {
				t.Fatalf("toolchain command = %q %v, want bash build.sh", gotName, gotArgs)
			}
			if !strings.Contains(stdout, "built") {
				t.Fatalf("stdout = %q", stdout)
			}
			reloaded, err := os.ReadFile(filepath.Join(dir, "plugin.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(reloaded), strings.Repeat("0", 64)) {
				t.Fatal("build did not refresh the sha256")
			}
		})
	}
}

func TestBuildUsesTheCargoTargetFromConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-target")
	if _, _, err := run(t, cli.Options{}, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	configPath := filepath.Join(dir, ".cargo", "config.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = bytes.Replace(config, []byte(`target = "wasm32-wasip1"`),
		[]byte(`target = "wasm32-unknown-unknown"`), 1)
	if err := os.WriteFile(configPath, config, 0o644); err != nil {
		t.Fatal(err)
	}
	sampleWasm, err := os.ReadFile(filepath.Join(sampleDirectory(t), "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	runner := func(_ context.Context, workDir, name string, args []string,
		_, _ io.Writer) error {
		gotArgs = args
		target := filepath.Join(workDir, "target", "wasm32-unknown-unknown",
			"release", "acme_hello.wasm")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, sampleWasm, 0o644)
	}
	if _, _, err := run(t, cli.Options{Runner: runner}, "build", dir); err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--target wasm32-unknown-unknown") {
		t.Fatalf("cargo args = %v, want the configured target", gotArgs)
	}
}

func TestBuildArtifactSkipsCompilation(t *testing.T) {
	dir := writePlugin(t, "hello.wasm")
	artifact := filepath.Join(t.TempDir(), "prebuilt.wasm")
	data, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, data, 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		called = true
		return nil
	}
	stdout, _, err := run(t, cli.Options{Runner: runner}, "build", dir, "--artifact", artifact)
	if err != nil {
		t.Fatalf("build --artifact: %v", err)
	}
	if called {
		t.Fatal("--artifact still ran the toolchain")
	}
	if !strings.Contains(stdout, "built") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestBuildReportsAMissingToolchain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "toolchain")
	if _, _, err := run(t, cli.Options{}, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		return &cli.ToolchainError{Command: "cargo", Purpose: "building a plugin"}
	}
	_, _, err := run(t, cli.Options{Runner: runner}, "build", dir)
	var missing *cli.ToolchainError
	if !errors.As(err, &missing) {
		t.Fatalf("build error = %v, want ToolchainError", err)
	}
}

func TestDevLaunchesTheAppWithTheDevelopmentDirectory(t *testing.T) {
	var binary, directory string
	launcher := func(_ context.Context, gotBinary, gotDirectory string, _, _ io.Writer) error {
		binary, directory = gotBinary, gotDirectory
		return nil
	}
	_, _, err := run(t, cli.Options{Launcher: launcher}, "dev", sampleDirectory(t), "--app", "fake-natcatty")
	if err != nil {
		t.Fatalf("dev: %v", err)
	}
	if binary != "fake-natcatty" {
		t.Fatalf("binary = %q", binary)
	}
	if directory != sampleDirectory(t) {
		t.Fatalf("directory = %q, want %q", directory, sampleDirectory(t))
	}
}

func TestUnknownCommandIsAnError(t *testing.T) {
	_, _, err := run(t, cli.Options{}, "frobnicate")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v", err)
	}
}

// TestCLIWorksForEverySample runs check, build, and dev for every committed
// sample. The copies keep the build's manifest rewrite out of the checkout.
func TestCLIWorksForEverySample(t *testing.T) {
	samplesDir := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples")
	entries, err := os.ReadDir(samplesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), entry.Name())
			copyTree(t, filepath.Join(samplesDir, entry.Name()), dir)
			stdout, _, err := run(t, cli.Options{}, "check", dir)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if !strings.Contains(stdout, "conformant") {
				t.Fatalf("check stdout = %q", stdout)
			}
			stdout, _, err = run(t, cli.Options{}, "build", dir, "--artifact", "plugin.wasm")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.Contains(stdout, "built") {
				t.Fatalf("build stdout = %q", stdout)
			}
			launched := false
			launcher := func(_ context.Context, binary, gotDir string, _, _ io.Writer) error {
				launched = true
				if binary != "fake-natcatty" || gotDir != dir {
					t.Fatalf("dev launcher = %q %q", binary, gotDir)
				}
				return nil
			}
			stdout, _, err = run(t, cli.Options{Launcher: launcher},
				"dev", dir, "--app", "fake-natcatty")
			if err != nil {
				t.Fatalf("dev: %v", err)
			}
			if !launched {
				t.Fatalf("dev did not launch the app; stdout = %q", stdout)
			}
		})
	}
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy sample: %v", err)
	}
}

// writePlugin writes a manifest and one committed fixture into a temporary
// directory.
func writePlugin(t *testing.T, wasmName string) string {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t), "adapters", "wasm", "testdata", wasmName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := strings.Join([]string{
		"manifest_version = 1",
		`id = "acme.cli"`,
		`name = "Acme CLI"`,
		`version = "1.0.0"`,
		`plugin_api = "^0.1"`,
		"abi = 1",
		`default_locale = "en"`,
		"[wasm]",
		`path = "plugin.wasm"`,
		`sha256 = "` + strings.Repeat("0", 64) + `"`,
		"memory_max_mib = 64",
		"[permissions]",
		`required = []`,
		`optional = []`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
