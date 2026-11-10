package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/sdk/conformance"
	"github.com/mslxl/natcatty/sdk/idl"
	"github.com/mslxl/natcatty/sdk/internal/plugindir"
)

const scaffoldLib = `//! {name}
//!
//! abi_v1.rs is generated from the Natcatty plugin IDL and defines the guest
//! ABI export plus the typed init-time compatibility check.

mod abi_v1;
use abi_v1 as abi;

static mut HEAP: [u8; 4096] = [0; 4096];
static mut HEAP_USED: u32 = 0;

#[no_mangle]
pub extern "C" fn natcatty_alloc(size: u32) -> u32 {
    unsafe {
        let ptr = core::ptr::addr_of_mut!(HEAP) as *mut u8 as u32 + HEAP_USED;
        HEAP_USED += (size + 7) & !7;
        ptr
    }
}

#[no_mangle]
pub extern "C" fn natcatty_init() -> i32 {
    if let Err(err) = abi::ensure_compatible_abi() {
        abi::log(abi::LogLevel::Error, err.to_string().as_bytes());
        return -1;
    }
    abi::log(abi::LogLevel::Info, b"plugin init");
    0
}

#[no_mangle]
pub extern "C" fn natcatty_dispatch(_command_ptr: u32, _command_len: u32) -> i32 {
    abi::log(abi::LogLevel::Info, b"plugin dispatch");
    if abi::output_write(b"hello from plugin") < 0 {
        -1
    } else {
        0
    }
}

#[no_mangle]
pub extern "C" fn natcatty_poll() -> i32 {
    0
}

#[no_mangle]
pub extern "C" fn natcatty_shutdown() -> i32 {
    0
}
`

const scaffoldCargo = `[package]
name = "{crate}"
version = "0.1.0"
edition = "2021"
publish = false

[lib]
crate-type = ["cdylib"]

[profile.release]
opt-level = "s"
lto = true
strip = true
`

// The linker flags give the module the host's default memory maximum, export
// its memory, and keep it a reactor instead of a command. Structural
// conformance rejects modules that omit any of these.
func scaffoldCargoConfig() string {
	return fmt.Sprintf(`[build]
target = "wasm32-wasip1"

[target.wasm32-wasip1]
rustflags = [
  "-C", "link-arg=--export-memory",
  "-C", "link-arg=--max-memory=%d",
  "-C", "link-arg=--no-entry",
]
`, int64(domainplugin.DefaultMemoryMaxMiB)<<20)
}

const scaffoldGitignore = `/target
`

// manifestTemplate is a valid plugin manifest before the first build. The
// hash is a placeholder that build replaces with the real module digest.
const manifestTemplate = `manifest_version = 1
id = "{id}"
name = "{name}"
version = "0.1.0"
plugin_api = "^0.1"
abi = 1
default_locale = "en"
description = "A Natcatty plugin scaffolded by natcatty-plugin init."

[wasm]
path = "plugin.wasm"
sha256 = "{sha}"
memory_max_mib = {memory}
toolchain = "rust"

[permissions]
required = ["commands"]
optional = []

[[contributions.commands]]
id = "greet"
title = "Say Hello"
title_key = "command_greet"
keywords = ["hello", "greet"]
`

// scaffoldCatalog is the package's default-locale message catalog. The same
// format and limits apply to every plugin, first-party included.
const scaffoldCatalog = `command_greet = "Say Hello"

[meta]
locale = "en"
`

func runInit(_ context.Context, args []string, opts Options) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	id := fs.String("id", "acme.hello", "plugin id (one dot, lowercase)")
	name := fs.String("name", "Acme Hello", "human-readable plugin name")
	positionals, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(positionals[0])
	if err != nil {
		return err
	}
	manifest := domainplugin.Manifest{
		ManifestVersion: 1,
		ID:              *id,
		Name:            *name,
		Version:         "0.1.0",
		PluginAPI:       "^0.1",
		ABI:             1,
		DefaultLocale:   "en",
		Description:     "A Natcatty plugin scaffolded by natcatty-plugin init.",
		Wasm: domainplugin.Wasm{
			Path:         "plugin.wasm",
			SHA256:       strings.Repeat("0", 64),
			MemoryMaxMiB: domainplugin.DefaultMemoryMaxMiB,
			Toolchain:    "rust",
		},
		Permissions: domainplugin.Permissions{Required: []string{"commands"}},
		Contributions: domainplugin.Contributions{
			Commands: []domainplugin.Command{{
				ID:       "greet",
				Title:    "Say Hello",
				TitleKey: "command_greet",
				Keywords: []string{"hello", "greet"},
			}},
		},
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	for _, relative := range []string{
		"plugin.toml", "Cargo.toml", ".gitignore",
		filepath.Join(".cargo", "config.toml"),
		filepath.Join("i18n", "en.toml"),
		filepath.Join("src", "lib.rs"),
		filepath.Join("src", "abi_v1.rs"),
	} {
		if _, err := os.Stat(filepath.Join(dir, relative)); err == nil {
			return fmt.Errorf("init: %s already exists; refusing to overwrite", relative)
		}
	}
	definition, err := idl.Parse()
	if err != nil {
		return fmt.Errorf("init: parse IDL: %w", err)
	}
	crate := crateName(*id)
	files := map[string]string{
		"plugin.toml":                          scaffoldManifest(*id, *name),
		"Cargo.toml":                           strings.ReplaceAll(scaffoldCargo, "{crate}", crate),
		filepath.Join(".cargo", "config.toml"): scaffoldCargoConfig(),
		".gitignore":                           scaffoldGitignore,
		filepath.Join("i18n", "en.toml"):       scaffoldCatalog,
		filepath.Join("src", "lib.rs"):         strings.ReplaceAll(scaffoldLib, "{name}", *name),
		filepath.Join("src", "abi_v1.rs"):      string(definition.GenerateRust()),
	}
	for relative, content := range files {
		path := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("init: create %s: %w", relative, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("init: write %s: %w", relative, err)
		}
	}
	fmt.Fprintf(opts.Stdout, "created %s\n", dir)
	fmt.Fprintf(opts.Stdout, "next: natcatty-plugin build %s && natcatty-plugin run %s --event greet\n", dir, dir)
	return nil
}

func runBuild(ctx context.Context, args []string, opts Options) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	artifact := fs.String("artifact", "",
		"module artifact (default: per toolchain; --artifact skips compilation)")
	positionals, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(positionals[0])
	if err != nil {
		return err
	}
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoManifest, err)
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	relative := *artifact
	if relative == "" {
		if err := runToolchain(ctx, dir, manifest, opts); err != nil {
			return err
		}
		relative, err = defaultArtifact(dir, manifest)
		if err != nil {
			return err
		}
	}
	source := relative
	if !filepath.IsAbs(source) {
		source = filepath.Join(dir, source)
	}
	wasm, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("build: read artifact %q: %w", relative, err)
	}
	if len(wasm) == 0 {
		return fmt.Errorf("build: artifact %q is empty", relative)
	}
	target, err := plugindir.Resolve(dir, manifest.Wasm.Path)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("build: create module directory: %w", err)
	}
	if err := os.WriteFile(target, wasm, 0o644); err != nil {
		return fmt.Errorf("build: write module: %w", err)
	}
	sum := sha256.Sum256(wasm)
	digest := hex.EncodeToString(sum[:])
	if err := updateManifestSHA(dir, digest); err != nil {
		return err
	}
	reloaded, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		return err
	}
	if findings := conformance.CheckStructural(reloaded, wasm); len(findings) > 0 {
		reportConformance(opts.Stderr, conformance.Report{Findings: findings})
		return &ConformanceError{Findings: findings}
	}
	fmt.Fprintf(opts.Stdout, "built %s (%d bytes, sha256 %s)\n", target, len(wasm), digest)
	return nil
}

// runToolchain compiles the module using the toolchain the manifest declares.
// Rust crates build with cargo using the target named in .cargo/config.toml
// (the scaffold writes wasm32-wasip1); C, TinyGo, and Zig packages run their
// checked-in build.sh.
func runToolchain(ctx context.Context, dir string, manifest domainplugin.Manifest,
	opts Options) error {
	switch manifest.Wasm.Toolchain {
	case "", "rust":
		return opts.Runner(ctx, dir, "cargo",
			[]string{"build", "--release", "--target", cargoTarget(dir)},
			opts.Stdout, opts.Stderr)
	case "c", "tinygo", "zig":
		return opts.Runner(ctx, dir, "bash", []string{"build.sh"},
			opts.Stdout, opts.Stderr)
	default:
		return fmt.Errorf("build: unknown wasm.toolchain %q", manifest.Wasm.Toolchain)
	}
}

// defaultArtifact names the file each toolchain produces before the CLI copies
// it to the manifest's module path.
func defaultArtifact(dir string, manifest domainplugin.Manifest) (string, error) {
	switch manifest.Wasm.Toolchain {
	case "", "rust":
		crate, err := cargoCrateName(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join("target", cargoTarget(dir), "release",
			strings.ReplaceAll(crate, "-", "_")+".wasm"), nil
	case "c", "tinygo", "zig":
		return manifest.Wasm.Path, nil
	default:
		return "", fmt.Errorf("build: unknown wasm.toolchain %q", manifest.Wasm.Toolchain)
	}
}

// defaultCargoTarget is the official WASI target every scaffold uses. A
// plugin may select another installed target in .cargo/config.toml; the
// checked-in Rust sample uses wasm32-unknown-unknown for its freestanding
// artifact.
const defaultCargoTarget = "wasm32-wasip1"

func cargoTarget(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		return defaultCargoTarget
	}
	match := cargoTargetPattern.FindSubmatch(data)
	if len(match) != 2 {
		return defaultCargoTarget
	}
	return string(match[1])
}

func cargoCrateName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return "", fmt.Errorf("build: read Cargo.toml: %w", err)
	}
	match := packageNamePattern.FindSubmatch(data)
	if len(match) != 2 {
		return "", fmt.Errorf("build: Cargo.toml has no [package] name")
	}
	return string(match[1]), nil
}

var (
	packageNamePattern = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
	sha256Pattern      = regexp.MustCompile(`(?m)^sha256\s*=\s*"[^"]*"`)
	cargoTargetPattern = regexp.MustCompile(`(?m)^target\s*=\s*"([^"]+)"`)
)

func updateManifestSHA(dir, digest string) error {
	path := filepath.Join(dir, adapterpluginmanifest.ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("build: read manifest: %w", err)
	}
	if !sha256Pattern.Match(data) {
		return fmt.Errorf("build: manifest has no wasm.sha256 field")
	}
	updated := sha256Pattern.ReplaceAll(data, []byte(`sha256 = "`+digest+`"`))
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("build: update manifest: %w", err)
	}
	return nil
}

func crateName(id string) string {
	return strings.ReplaceAll(id, ".", "-")
}

func scaffoldManifest(id, name string) string {
	return strings.NewReplacer(
		"{id}", tomlString(id),
		"{name}", tomlString(name),
		"{sha}", strings.Repeat("0", 64),
		"{memory}", strconv.Itoa(domainplugin.DefaultMemoryMaxMiB),
	).Replace(manifestTemplate)
}

func tomlString(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}
