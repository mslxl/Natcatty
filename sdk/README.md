# Natcatty plugin SDK

The SDK is a monorepo of tooling that does not depend on the app:

- `idl/natcatty_v1.idl` is the single source of truth for the v1 host modules,
  functions, records, enums, guest exports, and the generation-tagged handle.
- `idl/gen` regenerates the committed host Go types in
  `domain/plugin/abi/abi_gen.go`, the Rust bindings in
  `sdk/rust/src/abi_v1.rs`, and the C and TinyGo bindings described below.
- `rust` is the reference Rust SDK. `abi_v1.rs` is generated and committed
  because the Nix dev shell has no Rust toolchain; the generated code includes
  the guest ABI export and the typed `ensure_compatible_abi` init check.
- `stub` exposes `idl/stub.json`, the one service-response definition shared by
  the CLI fake host and the generated Rust in-language mock
  (`sdk/rust/src/stub_gen.rs`). It is validated against the IDL, so a stub can
  never name a service or method the wire ABI does not declare.
- `fakehost` loads a module in wazero with a virtual clock, deterministic
  randomness, injected command and UI events, provider and data fixtures, MCP
  tool calls, and explicit lifecycle control (`Start`, `Apply`, `Report`,
  `Unload`, `Close`, `InFlight`). Host services run through the production
  dispatcher over deterministic fixture ports; `StubServices` instead answers
  them from the shared stub definition.
- `conformance` is the structural and behavioral validator used by the CLI and
  by author CI. Structural checks never execute guest code. Behavioral checks
  run the same scenario against the fake host and a real production container,
  then compare normalized logs, output, UI state, provider/data observations,
  and MCP tool results.
- `cli` implements `natcatty-plugin`, wrapped by `cmd/natcatty-plugin`.
- `samples/hello-command` is the command sample. It contributes its `greet`
  command to the palette and a menu item that runs the same command. Its
  checked-in `plugin.wasm` is compiled from `src/lib.rs` against the reference
  SDK. The Nix dev shell
  has no Rust toolchain, so the artifact is committed and CI exercises it
  through the fake host and conformance checks instead of rebuilding it.
- `samples/host-services` is the C host-services sample. Its checked-in
  `plugin.wasm` is compiled from `src/main.c` with clang and wasm-ld and
  imports all five generated `natcatty:svc/*_v1` modules. It has a settings
  schema (two defaults plus one volatile field) and declares scoped optional
  permissions. `e2e_test.go` packages the sample as a `.natplug` and runs it
  through the real archive validator, install store, consent flow, manager,
  container, wazero runtime, and host services in both an all-granted and a
  required-only permission mode.
- `samples/workspace-services` is the C workspace-services sample. Its
  checked-in `plugin.wasm` is compiled from `src/main.c` with clang and
  wasm-ld and imports the files, hosts, sessions, and terminal
  `natcatty:svc/*_v1` modules. The guest discovers its granted directory
  through `files.preopens`, lists Hosts, observes a read-only Session tap,
  performs a range write and read, and receives typed `denied` results for
  every operation outside its Host, Session, and directory scopes.
  `e2e_test.go` runs it through the real manager and wazero path in both an
  all-granted and a required-only mode.
- `samples/language-provider` is the C editor language-service sample. Its
  checked-in `plugin.wasm` is compiled from `src/main.c` with clang and
  wasm-ld and implements the optional `natcatty_language_request` export. It
  contributes a `language.provider` for SQL with all eight capabilities,
  answers completion and diagnostics with bounded JSON data, and reuses the
  `language_respond` and `language_cancelled` host imports. `e2e_test.go`
  packages the sample, installs and enables it through the real manager and
  container, and drives the real `ui/editor` state through wazero to prove the
  completion and diagnostic results reach the editor.
- `samples/mcp-tools` is the C external-MCP-tools sample. It declares five
  static `mcp_tools` contributions (read/write access, no/session targets,
  schemas, short timeout), imports `mcp_tool_result` and `mcp_tool_progress`,
  and answers the host's `mcp_tool_call` dispatch command. The app's
  `plugin_mcp_e2e_test.go` packages it as a `.natplug` and drives it through
  the real validator, manager, container, wazero runtime, MCP policy handler,
  loopback listener, and `cmd/natcatty-mcp` stdio client.
- `samples/webview-view` is the sandboxed WebView sample. It declares a
  standalone `surface = "webview"` View served from
  `natcatty-plugin://acme.webview-view/ui/index.html` and a native View with an
  embedded webview node whose `src` is `ui/embed.html`. Both pages call only
  the fixed `greet` and `report` bridge methods through a same-origin
  `POST /__bridge`; the guest listens for the session-wide webview event and
  answers through `ui.webview_respond`.
  `app/webview_sample_e2e_test.go` packages the sample and drives the real
  archive validator, manager, container, wazero runtime, asset transport,
  bridge allowlist, and guest poll (with a fake native page).

## Sample artifact provenance

`sdk/samples/hello-command/plugin.wasm` was built with rustc 1.99 and lld 21
from outside the Nix dev shell. That rustc ships std for
`wasm32-unknown-unknown` but not for `wasm32-wasip1`, so the committed
artifact uses the former; it is still a pure core module with no WASI
imports and passes structural conformance. Rebuild it with:

```sh
rustc --edition 2021 --crate-type rlib --crate-name natcatty_plugin_sdk \
  --target wasm32-unknown-unknown -O sdk/rust/src/lib.rs \
  -o /tmp/libnatcatty_plugin_sdk.rlib
rustc --edition 2021 --crate-type cdylib --target wasm32-unknown-unknown -O \
  --extern natcatty_plugin_sdk=/tmp/libnatcatty_plugin_sdk.rlib \
  -C link-arg=--export-memory -C link-arg=--max-memory=67108864 \
  -C link-arg=--no-entry \
  sdk/samples/hello-command/src/lib.rs \
  -o sdk/samples/hello-command/plugin.wasm
```

rustc embeds build metadata, so two rebuilds are not byte-identical. After
rebuilding the sample by hand, refresh `wasm.sha256`; `natcatty-plugin build`
does that automatically for author plugins.

The sample carries `.cargo/config.toml` selecting `wasm32-unknown-unknown`,
the export-memory/max-memory/no-entry link flags, and `build-std` from the
checked-out `rust-src`. The CLI reads that target when it invokes cargo, so
`natcatty-plugin build sdk/samples/hello-command` works with the pinned
toolchain (set `RUSTC_BOOTSTRAP=1` because the pinned sysroot has no prebuilt
std). A normal author plugin scaffolded by `init` targets the official
`wasm32-wasip1` target instead.

## Host-services sample provenance

`sdk/samples/host-services/plugin.wasm` is a freestanding C module compiled
with clang 21 and lld 21 from `src/main.c`. It has no libc dependency; the
source defines the few compiler helper functions (`memset`, `memcpy`,
`strlen`) the module needs. Rebuild and refresh the manifest hash with
`build.sh`:

```sh
# wasm-ld is not in the Nix dev shell; point WASM_LD at an lld build.
WASM_LD=$(nix build --print-out-paths nixpkgs#lld)/bin/wasm-ld \
  ./sdk/samples/host-services/build.sh
```

The script prints the new sha256. Put that value in
`sdk/samples/host-services/plugin.toml` under `[wasm].sha256` and commit the
manifest and module together; the package validator rejects a mismatch.

## MCP-tools sample provenance

`sdk/samples/mcp-tools/plugin.wasm` is the same freestanding C setup compiled
from `src/main.c` with clang and lld. It imports only the two MCP host
functions in addition to the core ABI and answers tool calls synchronously
from `natcatty_dispatch`. Rebuild it with its own script:

```sh
# wasm-ld is not in the Nix dev shell; point WASM_LD at an lld build.
WASM_LD=$(nix build --print-out-paths nixpkgs#lld)/bin/wasm-ld \
  ./sdk/samples/mcp-tools/build.sh
```

The script prints the new sha256. Update
`sdk/samples/mcp-tools/plugin.toml` under `[wasm].sha256` and commit both
files together.

`sdk/samples/workspace-services/plugin.wasm` uses the same freestanding C
recipe, so rebuild it the same way with
`./sdk/samples/workspace-services/build.sh` and refresh its manifest hash.

`sdk/samples/webview-view/plugin.wasm` is the same freestanding C recipe
compiled from `src/main.c`. It imports only `output_write` and the ui service,
commits the embedded webview node, listens for the session-wide webview event,
and answers bridge requests with `ui.webview_respond`. Rebuild it with:

```sh
# wasm-ld is not in the Nix dev shell; point WASM_LD at an lld build.
WASM_LD=$(nix build --print-out-paths nixpkgs#lld)/bin/wasm-ld \
  ./sdk/samples/webview-view/build.sh
```

The script prints the new sha256. Update
`sdk/samples/webview-view/plugin.toml` under `[wasm].sha256` and commit the
manifest and module together.

## C, TinyGo, and Zig SDKs

The same `idl/natcatty_v1.idl` generates all three bindings. C and TinyGo are
generated outputs checked into the repository; Zig consumes the generated C
bindings. Enum values, method ids, service module names, host imports, and ABI
numbers all come from the generator. C uses generated `NATCATTY_EXPORT_*`
macros and Zig passes generated `NATCATTY_WIRE_*` names to `@export`; TinyGo's
`//export` directive requires literal names, and `go test ./sdk/idl` checks
them against the IDL.

| Generated output | Purpose |
| --- | --- |
| `c/natcatty_v1.h` | Single-header C bindings: ABI constants, enums, records, handles, export macros, typed host imports, service modules and method ids, `natcatty_guest_init`. |
| `c/natcatty_v1.c` | The one translation unit behind `NATCATTY_IMPLEMENTATION`: binds each generated wrapper to its wasm import and implements the ABI check. |
| `tinygo/abi_v1.go` | TinyGo package constants, enums, records, handles, service helpers, and the generated `CheckABI`/`GuestInit` negotiation. |
| `tinygo/host_tinygo.go` | `tinygo.wasm` half: one `//go:wasmimport` per host function and service module, plus `string`/`[]byte` wrappers. |
| `tinygo/host_stub.go` | Native type-check stub so ordinary `go build` and `go vet` compile the package. |
| `samples/hello-command-c`, `hello-command-tinygo`, `hello-command-zig` | Self-contained command samples; each carries byte-identical copies of the generated bindings its build uses. |

Build and validate a sample with the ordinary CLI; the manifest's
`wasm.toolchain` selects `build.sh`:

```sh
nix develop                       # pins the toolchains below
go run ./cmd/natcatty-plugin build ./sdk/samples/hello-command-c
go run ./cmd/natcatty-plugin check ./sdk/samples/hello-command-c
go run ./cmd/natcatty-plugin run   ./sdk/samples/hello-command-c --event greet
go run ./cmd/natcatty-plugin dev   ./sdk/samples/hello-command-c --app ./natcatty
```

The same commands work for `hello-command-tinygo` and `hello-command-zig`.
`check` runs structural conformance, the fake host, and the real production
runtime, and fails when their observations differ. `sdk/samples/language_sdks_test.go`
rebuilds every sample from source through the CLI when the toolchain is on
PATH; `NATCATTY_REQUIRE_LANGUAGE_SDKS=1` turns a missing tool into a failure.
The flake exports it in `nix develop`, and the CI language-SDK job sets it too.

### Pinned toolchains

The repository flake pins these in `devShells.default`; `flake.lock` pins the
nixpkgs revision. Do not substitute host binaries.

- TinyGo 0.43.0 (`pkgs.tinygo`)
- Zig 0.16.0 (`pkgs.zig_0_16`)
- clang 21.1.8 and wasm-ld/LLD 21.1.8 (`pkgs.clang`, `pkgs.lld`)
- binaryen 133 `wasm-opt` (`pkgs.binaryen`)

The C sample is a freestanding reactor compiled with
`clang --target=wasm32 -nostdlib`. The flake exports `NATCATTY_CLANG` (the
pinned LLVM without the Nix macOS cc-wrapper, whose hardening flags wasm32
rejects) and `NATCATTY_WASM_LD`, and `build.sh` uses those when set; the
TinyGo sample uses
`samples/hello-command-tinygo/target.json`, derived from TinyGo's
`wasm-unknown` target, with `gc=none`, `panic=trap`, `scheduler=none`, no entry
point, exported memory, and the manifest's 64 MiB maximum. TinyGo's c-shared
linker exports compiler-rt helpers and `_initialize`; `build.sh` removes those
toolchain symbols with `wasm-opt --remove-exports` because the plugin contract
has a closed export table. The Zig sample compiles `src/main.zig` together with
the generated `natcatty_v1.c`; Zig's `@cImport` cannot carry clang's
`import_module`/`import_name` attributes, so the generated C file, not the Zig
source, binds the wasm imports. Zig still reads every constant, enum, module
name, method id, and guest export name from `natcatty_v1.h`.

### ABI negotiation

Every generated binding exposes the generated major (`NATCATTY_ABI_VERSION` /
`ABIVersion`) and an init helper (`natcatty_guest_init` in C and Zig,
`GuestInit` in TinyGo). The helper reads the host's `abi_version`, compares it
with the generated major, and on mismatch logs one error line and returns -1
without dispatching. Samples fail init in that case. Independently, the host
reads the guest's `natcatty_abi_version` export before init and rejects an
incompatible guest. `sdk/samples/language_sdks_test.go` runs every committed
language artifact against a host reporting ABI 2 and asserts the -1 result and
the logged mismatch.

### Supported surface and limits against the Rust reference

The Rust SDK remains the reference and the only typed host: it provides typed
UI builders, provider/data/language/MCP/cross-plugin helpers, and an
in-language mock host. C, TinyGo, and Zig expose the complete wire surface
(every host import, service module, stable method id, enum, record, handle,
guest export name, and the ABI check), but payloads are raw.

| Capability | Rust reference | C | TinyGo | Zig |
| --- | --- | --- | --- | --- |
| Core host imports (`log`, `output_write`, clock, random, respond/cancel, MCP, service-response length) | typed wrappers | generated typed wrappers | generated `Host*` wrappers with `string`/`[]byte` | generated C wrappers via `@cImport` |
| Host services (`storage` ... `vault`) | typed modules | module/method macros, `natcatty_svc_<name>_call` and `_invoke` | constants, `Service<Name>Call`, typed `Service<Name><Method>` helpers | C macros and `<name>_invoke` |
| Native UI | typed builders and simulator | raw length-prefixed batches | raw batches | raw batches |
| Connection/data providers, language service, MCP tools, cross-plugin broker | typed helpers | raw JSON/binary envelopes | raw envelopes | raw envelopes |
| In-language mock host | yes | no | no | no |
| Guest exports | SDK `ensure_compatible_abi` + typed handlers | generated export macros, `natcatty_guest_init` | sample `//export` glue, generated `GuestInit` | Zig `export fn`, generated `natcatty_guest_init` |
| Runtime constraints | wasm32 reactor | wasm32 freestanding, no libc/WASI | freestanding target, `gc=none`, no scheduler, no goroutines, no package init or heap allocation | wasm32 freestanding, no Zig std libc/allocator |
| Toolchain | rustc/cargo (not in the flake) | clang 21.1.8 + wasm-ld 21.1.8 | TinyGo 0.43.0 + binaryen 133 | Zig 0.16.0 |

Limits shared by C, TinyGo, and Zig: only the wire ABI is generated, so a
plugin that wants typed UI/provider/service ergonomics or the mock host should
use Rust; JSON and native-UI payload encoding stays in plugin code. The
TinyGo target deliberately has no allocator and runs no `_initialize`, so use
static buffers and `unsafe.StringData`/`unsafe.SliceData` rather than package
init or heap allocation; the samples export `natcatty_scratch` instead of
`natcatty_alloc`. The Zig sample also has no allocator and exports a static
scratch buffer. All three samples declare only the `commands` permission and
answer the manifest's `greet` command.

## Regenerating bindings

```sh
go run ./sdk/idl/gen          # rewrite the committed files
go run ./sdk/idl/gen -check   # fail if the committed files are stale
```

`go test ./sdk/idl` also compares generated output with the committed files, so
drift fails normal CI. CI additionally runs the `-check` command and the Go
fake-host and conformance tests. The Rust bindings are type-checked during
development; CI does not build Rust because the Nix dev shell has no toolchain.

## CLI

```sh
go run ./cmd/natcatty-plugin init ./my-plugin
go run ./cmd/natcatty-plugin build ./my-plugin
go run ./cmd/natcatty-plugin check ./my-plugin
go run ./cmd/natcatty-plugin run ./my-plugin --event greet --start-ns 0 --step-ns 1000000 --seed 1
go run ./cmd/natcatty-plugin dev ./my-plugin --app ./natcatty
```

`run` uses the fake host and prints logs, events, visible output, and the final
virtual clock. `check` runs structural conformance, the fake host, and the real
runtime, and fails if their observations differ. `dev` runs the same check and
then starts the app with `NATCATTY_PLUGIN_DIR` pointing at the directory.
`build` runs the toolchain the manifest declares (`cargo build --release
--target wasm32-wasip1` for Rust; `bash build.sh` for C, TinyGo, and Zig),
copies the module,
refreshes `wasm.sha256`, and re-runs the structural gate. `build --artifact
PATH` copies a prebuilt module and skips compilation. Deterministic `.natplug`
packaging belongs to the install ticket.

## Conformance

```go
report, err := conformance.Check(ctx, conformance.Options{Directory: dir})
if err != nil { /* load failure */ }
if !report.OK() { /* findings or behavioral error */ }
```

Structural checks reject an unsupported ABI, missing required exports, export
or import signatures that differ from the IDL, imports outside the generated
`natcatty_host_v1` and `natcatty:svc/*_v1` allowlist, any WASI import, an
unbounded memory, and a memory maximum above the manifest's declaration. The
host-services sample is the committed coverage that the generated service
imports are accepted.

Behavioral checks derive a scenario from the manifest: every declared command
runs, and the six reference samples get their typed surfaces exercised:

- `counter-view` enqueues an input and a click event;
- `webview-view` delivers the session-wide bridge event kind 18;
- `loopback-provider` drives Validate, Open, Pump (with input), and Close;
- `data-provider` drives Open and a `rows` query;
- `mcp-tools` calls every tool except the intentionally blocking `slow` one;
- `host-services` runs its selftest against the live fixture services.

The same scenario runs in the fake host and through `appplugin.NewContainer`
with real host services, provider/data/language hosts, and the MCP tool
registrar. Observations are normalized and compared. Cross-plugin
`provides`/`inject` samples still run on both runtimes but skip parity, because
the single-instance fake host deliberately has no service broker; the report
carries that reason in `ParitySkipped`. Fake-host fixtures can be cancelled or
unloaded mid-call: `Unload` cooperatively cancels an in-flight data request
and waits for the runtime's call deadline before returning `ErrCallInFlight`,
which callers retry once the watchdog has stopped the guest.
