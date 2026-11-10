package idl_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"

	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/sdk/idl"
)

// TestGeneratedServiceExternsImportTheCallWasmField compiles a tiny wasm32
// guest against the committed SDK bindings and checks the actual wasm import
// field names, then runs the install-time structural validator on the result.
//
// The plain Go dev shell and the CI Go job do not carry a Rust toolchain, so
// this test skips unless rustc and cargo are available. Two explicit overrides
// let a Rust-equipped environment point at a split Nix toolchain:
//
//	NATCATTY_TEST_RUSTC     path to rustc
//	NATCATTY_TEST_CARGO     path to cargo
//	NATCATTY_TEST_RUST_SRC  path to <rust-src>/library
//	NATCATTY_TEST_RUST_STD  path to a rust-std sysroot (when rustc's sysroot
//	                        has no host lib directory)
//
// With a normal rustup toolchain that has the wasm32-unknown-unknown target
// installed, no overrides are needed. Without a prebuilt wasm32 core the test
// builds core from rust-src with cargo's -Z build-std=core.
func TestGeneratedServiceExternsImportTheCallWasmField(t *testing.T) {
	definition, err := idl.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	abiPath, err := filepath.Abs(filepath.Join("..", "rust", "src", "abi_v1.rs"))
	if err != nil {
		t.Fatalf("resolve abi_v1.rs: %v", err)
	}
	if _, err := os.Stat(abiPath); err != nil {
		t.Fatalf("committed bindings %s: %v", abiPath, err)
	}

	dir := t.TempDir()
	cargo, rustc, buildStd := prepareRustToolchain(t, dir)
	guestDir := filepath.Join(dir, "probe")
	writeProbeCrate(t, dir, guestDir, abiPath, definition)

	targetDir := filepath.Join(dir, "target")
	args := []string{"build", "--release", "--target", "wasm32-unknown-unknown"}
	if buildStd {
		args = append([]string{"-Z", "build-std=core"}, args...)
	}
	command := exec.Command(cargo, args...)
	command.Dir = guestDir
	command.Env = append(os.Environ(),
		"CARGO_TARGET_DIR="+targetDir,
		// build-std resolves the rust-src workspace, including proc_macro's
		// crates.io dependency, so it uses the caller's Cargo cache. Offline
		// keeps a local toolchain probe from reaching the network.
		"CARGO_NET_OFFLINE=true",
		"RUSTC="+rustc,
		"RUSTFLAGS=-C link-arg=--export-memory -C link-arg=--max-memory=67108864 -C link-arg=--no-entry",
	)
	if buildStd {
		command.Env = append(command.Env, "RUSTC_BOOTSTRAP=1")
	}
	if output, err := command.CombinedOutput(); err != nil {
		// cargo resolves the whole rust-src workspace for -Z build-std, which
		// includes proc_macro's crates.io dependency. A clean Cargo cache
		// without network access cannot resolve it; that is an environment
		// limitation, not an SDK failure.
		if buildStd && strings.Contains(string(output), "no matching package named") {
			t.Skipf("rust-src build-std needs a warmed Cargo cache or network access:\n%s", output)
		}
		t.Fatalf("build wasm probe: %v\n%s", err, output)
	}

	wasmPath := filepath.Join(targetDir, "wasm32-unknown-unknown", "release",
		"natcatty_wasm_import_probe.wasm")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("read built probe: %v", err)
	}

	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(context.Background())
	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("compile built probe: %v", err)
	}
	imports := map[string]string{}
	for _, function := range compiled.ImportedFunctions() {
		module, name, ok := function.Import()
		if ok {
			imports[module] = name
		}
	}
	modules := make([]string, 0, len(imports))
	for module := range imports {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	for _, service := range definition.Services {
		module := service.Module()
		name, ok := imports[module]
		if !ok {
			t.Errorf("built guest does not import service module %s (imports: %v)", module, modules)
			continue
		}
		if name != "call" {
			t.Errorf("wasm import field for %s is %q, want \"call\" (imports: %v)", module, name, modules)
		}
	}
	if err := adapterwasm.ValidateModule(ctx, wasmBytes,
		int64(domainplugin.DefaultMemoryMaxMiB)<<20); err != nil {
		t.Fatalf("structural validation rejected the SDK probe: %v\nimports: %v", err, modules)
	}
}

// prepareRustToolchain returns cargo, the rustc cargo must invoke, and whether
// core must be built from source. It skips with an actionable reason when the
// environment has no usable toolchain.
func prepareRustToolchain(t *testing.T, dir string) (cargo, rustc string, buildStd bool) {
	t.Helper()
	cargo = os.Getenv("NATCATTY_TEST_CARGO")
	if cargo == "" {
		if found, err := exec.LookPath("cargo"); err == nil {
			cargo = found
		}
	}
	rustc = os.Getenv("NATCATTY_TEST_RUSTC")
	if rustc == "" {
		if found, err := exec.LookPath("rustc"); err == nil {
			rustc = found
		}
	}
	if cargo == "" || rustc == "" {
		t.Skip("rustc and cargo are not available; the Go dev shell and CI Go job are Rust-free")
	}
	cargo, err := filepath.Abs(cargo)
	if err != nil {
		t.Fatalf("resolve cargo: %v", err)
	}
	rustc, err = filepath.Abs(rustc)
	if err != nil {
		t.Fatalf("resolve rustc: %v", err)
	}
	sysrootOutput, err := exec.Command(rustc, "--print", "sysroot").Output()
	if err != nil {
		t.Skipf("rustc --print sysroot failed (%v); no usable toolchain", err)
	}
	sysroot := strings.TrimSpace(string(sysrootOutput))
	if wasmCoreInstalled(sysroot) {
		return cargo, rustc, false
	}

	rustSrc := os.Getenv("NATCATTY_TEST_RUST_SRC")
	if rustSrc == "" {
		candidate := filepath.Join(sysroot, "lib", "rustlib", "src", "rust", "library")
		if isDir(candidate) {
			rustSrc = candidate
		}
	}
	if rustSrc == "" {
		t.Skip("no prebuilt wasm32 core and no rust-src; install the target or set NATCATTY_TEST_RUST_SRC")
	}
	host := rustHost(t, rustc)
	hostLib := filepath.Join(sysroot, "lib", "rustlib", host, "lib")
	if !isDir(hostLib) {
		if std := os.Getenv("NATCATTY_TEST_RUST_STD"); std != "" {
			hostLib = filepath.Join(std, "lib", "rustlib", host, "lib")
		}
	}
	if !isDir(hostLib) {
		t.Skip("split toolchain has no host std directory; set NATCATTY_TEST_RUST_STD")
	}
	hostBin := filepath.Join(sysroot, "lib", "rustlib", host, "bin")
	if !isDir(hostBin) {
		t.Skip("split toolchain has no host bin directory (rust-lld); cannot link wasm")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a split Rust sysroot needs a POSIX wrapper; install the wasm32 target instead")
	}

	// Build a merged sysroot so cargo's build-std finds rust-src while the
	// compiler and linker still see the host std libraries.
	merged := filepath.Join(dir, "sysroot")
	hostDir := filepath.Join(merged, "lib", "rustlib", host)
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatalf("create merged sysroot: %v", err)
	}
	linkOrFail(t, hostBin, filepath.Join(hostDir, "bin"))
	linkOrFail(t, hostLib, filepath.Join(hostDir, "lib"))
	srcDir := filepath.Join(merged, "lib", "rustlib", "src", "rust")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("create merged rust-src: %v", err)
	}
	linkOrFail(t, rustSrc, filepath.Join(srcDir, "library"))

	wrapper := filepath.Join(dir, "rustc")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--print\" ] && [ \"$2\" = \"sysroot\" ]; then\n" +
		"  echo " + shellQuote(merged) + "\n  exit 0\nfi\n" +
		"exec " + shellQuote(rustc) + " --sysroot " + shellQuote(merged) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write rustc wrapper: %v", err)
	}
	return cargo, wrapper, true
}

func wasmCoreInstalled(sysroot string) bool {
	matches, err := filepath.Glob(filepath.Join(sysroot, "lib", "rustlib",
		"wasm32-unknown-unknown", "lib", "libcore-*.rlib"))
	return err == nil && len(matches) > 0
}

func rustHost(t *testing.T, rustc string) string {
	t.Helper()
	output, err := exec.Command(rustc, "-vV").Output()
	if err != nil {
		t.Fatalf("rustc -vV: %v", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if host, ok := strings.CutPrefix(line, "host: "); ok {
			return strings.TrimSpace(host)
		}
	}
	t.Fatalf("rustc -vV has no host line:\n%s", output)
	return ""
}

func writeProbeCrate(t *testing.T, root, guestDir, abiPath string, definition *idl.Definition) {
	t.Helper()
	shimDir := filepath.Join(root, "std-shim")
	writeFile(t, filepath.Join(shimDir, "Cargo.toml"), `[package]
name = "std-shim"
version = "0.1.0"
edition = "2021"

[lib]
path = "src/lib.rs"
`)
	writeFile(t, filepath.Join(shimDir, "src", "lib.rs"), `#![no_std]
pub mod error {
    pub use core::error::Error;
}
`)
	writeFile(t, filepath.Join(guestDir, "Cargo.toml"), fmt.Sprintf(`[package]
name = "natcatty-wasm-import-probe"
version = "0.1.0"
edition = "2021"

[lib]
name = "natcatty_wasm_import_probe"
crate-type = ["cdylib"]

[dependencies]
std = { path = %q, package = "std-shim" }

[profile.release]
opt-level = "s"
`, shimDir))
	var source strings.Builder
	source.WriteString("#![no_std]\n")
	source.WriteString("use core::panic::PanicInfo;\n")
	source.WriteString("#[panic_handler]\nfn panic(_: &PanicInfo) -> ! { loop {} }\n")
	fmt.Fprintf(&source, "#[path = %q]\nmod abi_v1;\n", abiPath)
	source.WriteString("fn service_probe() {\n    let mut response = [0u8; 8];\n")
	for _, service := range definition.Services {
		fmt.Fprintf(&source,
			"    abi_v1::service::%s::call_method(1, b\"{}\", &mut response);\n", service.Name)
	}
	source.WriteString("}\n")
	// abi_v1.rs already exports natcatty_abi_version for the host.
	source.WriteString("#[no_mangle]\npub extern \"C\" fn natcatty_init() -> i32 { service_probe(); 0 }\n")
	source.WriteString("#[no_mangle]\npub extern \"C\" fn natcatty_dispatch(_command_ptr: u32, _command_len: u32) -> i32 { 0 }\n")
	source.WriteString("#[no_mangle]\npub extern \"C\" fn natcatty_poll() -> i32 { 0 }\n")
	source.WriteString("#[no_mangle]\npub extern \"C\" fn natcatty_shutdown() -> i32 { 0 }\n")
	writeFile(t, filepath.Join(guestDir, "src", "lib.rs"), source.String())
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func linkOrFail(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("link %s -> %s: %v", link, target, err)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
