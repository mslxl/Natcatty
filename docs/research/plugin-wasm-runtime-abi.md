# WASM runtime and ABI constraints for the Natcatty plugin system

Status: research / ABI proposal  
Date: 2026-11-06  
Scope: determine the hard constraints wazero imposes on a Go-hosted plugin runtime, and derive the
base ABI shape (exports, imports, memory ownership, version negotiation, error model, limits) that
follows from those constraints. The target is a desktop application with a Go host, therefore the
analysis assumes the host is Go and wazero is embedded in-process, not a CLI.

Sources verified against:

- `tetratelabs/wazero` `main` @ `ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7` (2026-10-19), plus
  release **v1.12.0** (published 2026-05-29). All source links below are pinned to that commit.
- WASI Preview 1 WITX at tag `wasi-0.1`
  (`preview1/witx/wasi_snapshot_preview1.witx`), and the WASI 0.1 overview at `wasi.dev`.
- WebAssembly Core Specification 1.0/2.0 references as used by wazero; WebAssembly threads proposal.
- Go 1.21 and 1.24 release notes, Go `cmd/compile` WebAssembly directive documentation, Go
  WebAssembly wiki; TinyGo WASI documentation; Rust `wasm32-wasip1` target documentation; Zig
  language reference; wasi-sdk README.
- wazero issue #2393 (macOS code-signing entitlements) and the wazero CI matrix.

Where a statement is a recommendation rather than a source-backed fact, it is marked
**Recommendation**.

---

## 1. Executive summary

wazero is a suitable base for an in-process plugin runtime because it is pure Go, has no CGO or
host-specific dependencies, sandboxes guest linear memory, and recovers guest traps at the host
boundary. It is not a sandbox against CPU exhaustion: it has no fuel/gas metering, and the only
built-in way to stop a runaway guest is a deadline/cancellation option that injects periodic checks.
The plugin model must therefore be **single-threaded, message-oriented, deadline-bounded, and
explicitly closed**.

The hard constraints that most shape the ABI are:

1. **One linear memory, 32-bit, 64 KiB pages, at most 4 GiB.** WebAssembly Core 2.0 allows at most
   one memory; memory64 and multi-memory are not in wazero's default feature set.
2. **No threads/atomics in the default feature set.** `CoreFeaturesV2` is the default and contains
   no threads, atomics, or shared memory. wazero main has an experimental opt-in
   (`experimental.CoreFeaturesThreads`), but there is no `wasi-threads`/`thread_spawn` import, so a
   guest cannot create threads; the embedder must drive concurrency. Treat guests as single-threaded.
3. **Calls are synchronous and not goroutine-safe.** `api.Function.Call` must be serialized per
   function; the safe plugin model is one serialized call stream per instance.
4. **Cancellation is cooperative and opt-in.** `RuntimeConfig.WithCloseOnContextDone(true)` is
   required for context cancellation/timeouts to interrupt guest code. A blocking host import cannot
   be preempted by wazero; it must honor `context.Context` itself.
5. **Trap isolation is good, typed-error isolation is limited.** A guest trap or a panic in a host
   function is recovered and returned as a Go `error`; it does not crash the host process. Only
   `*sys.ExitError` is a stable public type; trap errors wrap an internal type and are matched by
   message.
6. **WASI Preview 1 has no socket creation.** It can accept/recv/send/shutdown on an already-open
   socket fd, but there is no bind/listen/connect. wazero exposes only host-pre-opened TCP listeners.
   Outbound networking, DNS, UDP, and dynamic listeners must be custom host imports.
7. **darwin/amd64 is not in wazero's tested/supported platform matrix.** The compiler code path
   accepts darwin/amd64, but the project policy is "the only supported operating systems are ones we
   test", and macOS is tested only on arm64. Signed/notarized macOS builds additionally need
   executable-memory entitlements when the compiler engine is used.

### 1.1 Recommended base ABI in one paragraph

Use a **reactor-style core module** (no automatic `_start`), with one exported `memory`, a packed
`natcatty_abi_version() -> i64` export, a small set of scalar-typed exports
(`natcatty_init`, `natcatty_dispatch`, `natcatty_poll`, `natcatty_shutdown`), and a versioned host
import module named `natcatty_host_v1`. All bulk data crosses the boundary as **UTF-8/byte slices
described by `(ptr:u32, len:u32)` in guest linear memory**; the host copies in and out. Errors are
**i32 status codes** (`0` success, positive byte counts, negative error classes), never Go panics or
Wasm exceptions. The host clears the default `_start` start function, configures real or deterministic
clocks/random explicitly, sets a per-runtime memory page limit, enables
`WithCloseOnContextDone(true)`, and serializes all calls per instance. Undeclared or over-budget
memory maxima are rejected before the plugin is accepted.

---

## 2. wazero runtime model: compile, instantiate, drop

### 2.1 Compile

`Runtime.CompileModule(ctx, binary)` decodes and validates a core Wasm binary and returns a
`CompiledModule`. With the compiler engine, AOT compilation happens here, during `CompileModule`, not
at instantiation ([wazero runtime.go L90-L103](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L90-L103),
[wazero README L35-L43](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L35-L43)).
A `CompiledModule` can be instantiated many times; this is the intended way to amortize compilation
across plugin instances.

Memory limits and feature selection are applied during decode/compile, not at instantiation:
`Runtime.CompileModule` passes `r.memoryLimitPages` and the enabled feature set into the binary
decoder ([runtime.go L229-L238](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L229-L238)).
Changing the runtime's memory limit therefore requires recompiling, not just reinstantiating.

A `CompiledModule` is tied to the `Runtime` that produced it. Closing the runtime closes any
`CompiledModule` it compiled, and `CompiledModule.Close` is safe to call while instances are still
outstanding ([wazero config.go L307-L357](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L307-L357)).
Cross-runtime reuse is via a shared `CompilationCache`, not by sharing the object
([wazero cache.go L21-L50](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go#L21-L50)).

### 2.2 Instantiate

`Runtime.InstantiateModule(ctx, compiled, config)` creates one **module instance**. Each instance has
its own linear memory and its own globals. The same `CompiledModule` can be instantiated repeatedly:
`ModuleConfig.WithName("")` makes the instance anonymous, explicitly for the case "instantiate the
same CompiledModule multiple times"
([wazero config.go L496-L510](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L496-L510)).
Non-empty module names must be unique within one `Runtime`; duplicate names are rejected
([config.go L496-L510](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L496-L510)).

Instantiation can fail for: duplicate module name, invalid table initializer, a failing start
function, a WASI non-zero exit, or context cancellation during a start function
([runtime.go L105-L124](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L105-L124)).

**Critical default:** `NewModuleConfig()` sets the start function to `_start`
([config.go L688-L692](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L688-L692)).
For a plugin ABI, this is usually wrong: `_start` commonly exits the module during instantiation,
which prevents later use of exports. Clear it with `WithStartFunctions()` and call an explicit
`_initialize`/init export yourself
([config.go L513-L529](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L513-L529)).

### 2.3 Drop

Lifecycle is explicit:

- `api.Module.Close` / `CloseWithExitCode` releases the instance's resources and removes its name,
  making the name available again
  ([wazero api/wasm.go L186-L197](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L186-L197)).
  Calling it from inside a host function is safe and may cause the outer `Call` to return a
  `*sys.ExitError`
  ([api/wasm.go L193-L196](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L193-L196)).
- `IsClosed` distinguishes "normal zero exit" from "still usable"; `sys.ExitError` is only returned
  for a non-zero exit code
  ([api/wasm.go L199-L212](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L199-L212),
  [sys/error.go L20-L45](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go#L20-L45)).
- `Runtime.Close` closes every module it initialized and every `CompiledModule` it compiled
  ([runtime.go L30-L40](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L30-L40)).

There is no per-instance "pause" or "evict" API other than closing the instance. To hot-reload a
plugin, compile the new binary, instantiate it under a new name, switch traffic, then close the old
instance.

### 2.4 Per-instance memory and globals

- `api.Module.Memory()` returns the instance's memory (`nil` if none); `ExportedMemory("name")` is
  the named lookup. WASI modules must export `memory`
  ([api/wasm.go L143-L182](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L143-L182)).
- `ExportedGlobal(name)` returns an exported global for that instance; `MutableGlobal.Set` mutates it
  ([api/wasm.go L524-L560](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L524-L560)).
- Imported globals/functions live in the host module instance and are therefore shared by every
  importing guest instance. Per-plugin mutable state must not live in host-module mutable globals;
  key it by the `api.Module` passed to the host import (or by the unique instance name).

**Recommendation:** instantiate each plugin once with a unique name (or anonymously) and keep a
host-side registry keyed by the instance handle. Do not use host-module globals for per-plugin state.

---

## 3. Memory-page limits and per-plugin budgets

### 3.1 The mechanics

Wasm linear memory is 32-bit, in 64 KiB pages, with a hard maximum of 65,536 pages (4 GiB)
([wazero internal/wasm/memory.go L21-L29](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/memory.go#L21-L29)).
`RuntimeConfig.WithMemoryLimitPages(n)` sets the maximum pages **per memory** (that is, per instance
with the one-memory limit); the default is 65,536. Values above 65,536 panic. It is documented as
"the maximum pages allowed per memory", so it is not an aggregate process budget
([wazero config.go L55-L78](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L55-L78)).

The limit is applied at decode time with these exact semantics
([internal/wasm/binary/decoder.go L213-L238](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/binary/decoder.go#L213-L238),
[internal/wasm/binary/memory.go L19-L42](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/binary/memory.go#L19-L42)):

- If the binary declares a maximum greater than the runtime limit (but within the 65,536-page spec
  limit), the declared maximum is **clamped down** to the runtime limit. It is not a compile error.
- If the binary declares no maximum, the effective maximum becomes the runtime limit.
- If the binary declares a minimum greater than the runtime limit, validation fails
  ("min N pages ... over limit")
  ([internal/wasm/module.go L923-L943](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/module.go#L923-L943)).
- `CompiledModule.ExportedMemories()` exposes `MemoryDefinition.Max() (max uint32, encoded bool)`;
  `encoded` reports whether the original binary actually declared a maximum. This is the reliable
  way to reject unbounded plugins
  ([internal/wasm/memory_definition.go L123-L128](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/memory_definition.go#L123-L128)).

A guest `memory.grow` beyond the effective maximum returns `-1` (host API `Grow` returns `false`);
it does not trap
([internal/wasm/memory.go L230-L257](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/memory.go#L230-L257),
[api/wasm.go L562-L590](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L562-L590)).
The guest must handle the failure.

### 3.2 Enforcing per-plugin budgets

Because the limit is per runtime and per memory, there are three viable designs:

1. **One `Runtime` per plugin (or per budget class) - recommended for strong isolation.**
   Set `WithMemoryLimitPages(pluginBudgetPages)`. The runtime hard-caps every memory in that runtime.
   Use a shared `CompilationCache` across runtimes
   ([cache.go L21-L50](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go#L21-L50);
   [config.go L115-L137](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L115-L137))
   to avoid recompiling the same plugin in every runtime. A file-backed cache
   (`NewCompilationCacheWithDir`) survives restarts; entries are keyed by wazero version
   ([cache.go L52-L75](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go#L52-L75)).
2. **One `Runtime` with a global cap plus manifest validation.**
   Set the runtime limit to the largest plugin budget, then reject a plugin at install time if
   `MemoryDefinition.Max()` reports `encoded == false` or `max > assignedBudget` (or `Min >
   assignedBudget`). This enforces the declared bound but relies on the guest not exceeding its
   declared maximum; it does not give one runtime multiple distinct hard caps.
3. **Custom allocator accounting (experimental).**
   `experimental.WithMemoryAllocator(ctx, allocator)` installs a per-instantiation `Allocate(cap,
   max)` / `Reallocate(size)` hook; returning `nil` fails growth. This can implement a per-instance
   byte budget or a global accounting pool, but the API is explicitly experimental
   ([experimental/memory.go L1-L60](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/memory.go#L1-L60)).

Do **not** enable `WithMemoryCapacityFromMax(true)` for untrusted plugins unless every plugin declares
a small maximum: the docs warn that with no encoded maximum it allocates 4 GiB
([config.go L67-L78](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L67-L78)).
It is useful only when you want stable host memory views (min=max / eager reservation) and know the
budget.

The host can also read `Memory().Size()` and `Grow(0)`; note `Size()` overflows to zero at the
65,536-page maximum, with `Grow(0)` documented as the workaround
([api/wasm.go L562-L585](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L562-L585)).

**Recommendation:** budget in bytes, convert to pages, reserve a margin, require an explicit maximum
in the plugin manifest and in the binary, and reject unbounded modules. Enforce a separate aggregate
accounting limit at the host level (number of instances times their declared maxima) because
`WithMemoryLimitPages` alone does not cap the sum across instances.

---

## 4. Cancellation and interruption of long-running guest code

By default, wazero does **not** interrupt a running guest. A guest function can block the calling
goroutine (and potentially the underlying OS thread) forever. The docs say this explicitly and
recommend `WithCloseOnContextDone` for untrusted Wasm
([config.go L149-L170](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L149-L170)).

With `RuntimeConfig.WithCloseOnContextDone(true)`, execution is terminated when any of the following
happens:

- the `context.Context` passed to `api.Function.Call` is canceled;
- that context reaches its deadline/timeout;
- `Module.Close` or `Module.CloseWithExitCode` is called during execution.

On termination the module is closed, and the caller receives a `*sys.ExitError` whose exit code is
`ExitCodeContextCanceled` (`0xffffffff`) or `ExitCodeDeadlineExceeded` (`0xefffffff`) for the first
two cases ([config.go L149-L170](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L149-L170),
[sys/error.go L10-L18](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go#L10-L18),
[sys/error.go L20-L70](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go#L20-L70)).

How it works and its limits:

- The compiler/interpreter inserts periodic checks that call a Go function which atomically tests a
  "closed" sentinel at strategic points in the code. This is cooperative instrumentation, not a
  preemptive scheduler
  ([RATIONALE.md L1522-L1535](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1522-L1535)).
- Enabling it has "a bit of extra cost" because of those checks
  ([config.go L149-L170](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L149-L170)).
- A **blocking host import cannot be interrupted by wazero**: the check runs in guest code, not
  inside the host function. Host imports must select on `ctx.Done()` and return, or the deadline
  will not take effect until control returns to the guest.
- There is **no fuel/gas/instruction-budget API** in `RuntimeConfig`. The full option set is core
  features, memory limit, memory capacity, compilation cache, custom sections, debug info, engine
  choice, and close-on-context-done
  ([config.go L30-L175](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L30-L175)).
  Deadline plus a host-side watchdog is the only available CPU bound.
- The module is closed after cancellation, so the instance cannot be reused. Recreate it.

**Recommendation:** enable `WithCloseOnContextDone(true)` on every runtime that executes untrusted
code; give each call a deadline (including `natcatty_init`); have every host import take and honor
`ctx`; and close the instance on cancellation rather than trying to resume it. Add a host-side
watchdog that calls `CloseWithExitCode` if a call exceeds a hard limit by a wide margin.

---

## 5. Host imports, guest exports, re-entrancy, and callback delivery

### 5.1 Host import calling convention

Host functions are defined with `HostModuleBuilder` and exported under a module name and function
name. The normal `WithFunc` form is:

```go
func(ctx context.Context, [m api.Module,] params...) results...
```

Except for the mandatory `context.Context` and the optional second `api.Module`, all parameters and
results must be WebAssembly numeric types: `uint32`, `int32`, `uint64`, `int64`, `float32`, or
`float64`. Strings, slices, and structs do not cross; the only way to share them is to write them into
the importing module's linear memory and pass offsets
([wazero builder.go L20-L38](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go#L20-L38),
[builder.go L80-L115](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go#L80-L115)).
The `api.Module` argument is the **importing** instance, which is how a host import reaches that
instance's memory.

The low-level forms are `WithGoModuleFunction(func(ctx, mod, stack []uint64))` and
`WithGoFunction(func(ctx, stack []uint64))`; these receive/return values through a `uint64` stack and
have no Go return value. Errors are not returned through the function signature; a host function can
panic, but panics are recovered at the call boundary (see section 8) and are a poor control-flow
mechanism. Encode status in the Wasm return value instead
([builder.go L41-L78](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go#L41-L78),
[api/wasm.go L440-L470](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L440-L470)).

Boundary value types exposed by `api` are `i32`, `i64`, `f32`, `f64`, and `externref`
([api/wasm.go L81-L110](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L81-L110)).
There is no `v128` host value type in this tree, so SIMD stays inside the guest. `externref` is an
opaque `uintptr` in wazero's process; do not expose it to untrusted guests as a generic handle.

A missing import fails instantiation; that is the mechanism to make an unsupported host ABI version
fail closed at load time
([runtime.go L105-L124](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L105-L124)).

### 5.2 Guest export calling convention

The host calls an export through `mod.ExportedFunction(name).Call(ctx, params...)`, which returns
`[]uint64` results and an `error`. Params and results are encoded as `uint64`; use `api.EncodeXXX` /
`api.DecodeXXX` for typed values
([api/wasm.go L360-L400](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L360-L400)).
`CallWithStack` is an allocation-optimized variant.

`Call` is explicitly **not goroutine-safe**. Sequential calls are fine; do not call the same
`api.Function` again until the previous call returns. To invoke concurrently, the docs say to create
another `api.Function` for the same export
([api/wasm.go L371-L385](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L371-L385)).
That is a statement about wazero's internal call state, not about guest thread safety: with the
default single-threaded memory model, concurrent calls into the same instance would race on the
guest's memory and globals. The plugin ABI must therefore serialize calls per instance.

**Recommendation:** cache the `api.Function` objects for hot exports per instance, but only use them
from the instance's single dispatcher goroutine.

### 5.3 Re-entrancy

Host-to-guest callbacks are supported and documented: a host function can call
`m.ExportedFunction("__read").Call(ctx, ...)` and propagate the context
([builder.go L103-L113](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go#L103-L113)).
This is the foundation for pull-style host imports.

Constraints:

- The nested call must be sequential on the same goroutine; do not start a second concurrent call
  into the same instance.
- Host modules cannot be invoked directly through `ExportedFunction`; host functions exist to be
  imported by guests
  ([builder.go L177-L184](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go#L177-L184)).
- A trap in a nested call is returned as an `error` from that nested `Call`, so the host import can
  translate it into a status code. If the host import re-panics (or has no status to return), the
  trap unwinds to the outer boundary. Design nested exports to return a status code instead of
  trapping.
- Bound the nesting depth on the host side; unbounded host-import -> guest-export -> host-import
  recursion consumes Go and Wasm stack.

### 5.4 Callback delivery

There is no asynchronous callback mechanism across the boundary:

- Guest-to-host "callbacks" are ordinary imports invoked synchronously by the guest.
- Host-to-guest "callbacks" are ordinary exports invoked synchronously by the host.
- There is no way for a guest to register a function pointer that the host can call later. Tables
  and `call_indirect` are guest-internal; `externref` does not give the host a callable function.
- Wasm has no threads by default and no event loop. The host owns the event loop.
- wazero's `experimental.FunctionListener` reports `Before`, `After`, and `Abort` for observability,
  but it is a tracing hook, not an asynchronous delivery mechanism
  ([experimental/listener.go L35-L100](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/listener.go#L35-L100)).

**Recommendation:** model callbacks as messages. The host calls `natcatty_dispatch(ptr, len)` or the
guest pulls with `input_read`. Do not design an API where the guest expects the host to invoke an
arbitrary guest function asynchronously.

---

## 6. WASI Preview 1: what it provides, and the socket reality

wazero's built-in WASI implementation is `wasi_snapshot_preview1`
([imports/wasi_snapshot_preview1/wasi.go L1-L25](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports/wasi_snapshot_preview1/wasi.go#L1-L25)).
Preview 1 is a single flat module with roughly 45 functions (the `wasi-0.1` WITX exports 46 names),
covering
([WASI 0.1 overview](https://wasi.dev/releases/wasi-p1),
[WASI preview1 WITX](https://github.com/WebAssembly/WASI/blob/wasi-0.1/preview1/witx/wasi_snapshot_preview1.witx)):

- arguments and environment: `args_get`, `args_sizes_get`, `environ_get`, `environ_sizes_get`;
- clocks: `clock_res_get`, `clock_time_get`;
- file descriptors and files: `fd_*` (read, write, seek, tell, close, stat, allocate, sync,
  pread/pwrite, readdir, renumber, fdstat/prestat), `path_*` (open, stat, create/remove directory,
  rename, link, symlink, readlink, unlink, set times);
- polling and process: `poll_oneoff`, `proc_exit`, `proc_raise`, `sched_yield`;
- random: `random_get`;
- sockets: `sock_accept`, `sock_recv`, `sock_send`, `sock_shutdown`.

Wazero exports all of these
([wasi.go L175-L220](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports/wasi_snapshot_preview1/wasi.go#L175-L220)).

### 6.1 Sockets: no creation, only use of pre-opened sockets

The four socket functions are not a networking API. The WITX defines `sock_accept` as accepting on a
"listening socket" fd, and `sock_recv`/`sock_send`/`sock_shutdown` as operations on an existing
socket fd. There is **no `bind`, `listen`, `connect`, `socket`, DNS, or UDP** function in Preview 1
([WITX L497-L544](https://github.com/WebAssembly/WASI/blob/wasi-0.1/preview1/witx/wasi_snapshot_preview1.witx#L497-L544)).
The WASI 0.1 overview describes this as "accept only, no listen or connect"
([WASI 0.1 overview](https://wasi.dev/releases/wasi-p1)).

wazero goes one step further than a bare WASI runtime: the host can pre-open TCP listeners via
`experimental/sock.Config.WithTCPListener(host, port)`, and those listeners are assigned file
descriptors after pre-opened files. The guest can only `sock_accept` on them
([experimental/sock/sock.go L1-L40](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/sock/sock.go#L1-L40),
[internal/sock/sock.go L25-L60](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/sock/sock.go#L25-L60)).
This is a wazero extension, not portable WASI.

**Conclusion:** the plugin ABI must provide networking through custom host imports, not WASI. If the
plugin only needs to accept connections on listeners the host opened, WASI `sock_*` is sufficient,
but a general plugin (outbound connections, DNS, UDP, dynamic listeners) needs an explicit host
capability API.

### 6.2 WASI sandbox defaults

`ModuleConfig` defaults are deliberately restrictive:

- no filesystem access; `path_open` returns unsupported/`ENOSYS` unless an `FSConfig` is provided
  ([config.go L477-L494](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L477-L494));
- stdin returns `io.EOF`, stdout/stderr discard
  ([config.go L764-L790](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L764-L790));
- args and environment are empty
  ([config.go L455-L473](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L455-L473));
- wall time and monotonic time are fake (increase by 1 ms per read), sleep returns immediately, and
  `sched_yield` returns immediately
  ([config.go L570-L650](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L570-L650));
- random is **deterministic** by default; you must pass `rand.Reader` from `crypto/rand` explicitly
  ([config.go L651-L660](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L651-L660)).

These defaults are good for reproducible tests but wrong for production plugins that need real time,
cryptographic randomness, logs, or files. Configure them explicitly per plugin capability.

### 6.3 Preview 1 status and the component-model gap

WASI 0.1/Preview 1 is still widely used, but it is the original stable snapshot, maintained for
historical compatibility; standardization has moved to the component model and WASI 0.2/0.3
([WASI 0.1 overview](https://wasi.dev/releases/wasi-p1),
[Rust wasm32-wasip1 target docs](https://doc.rust-lang.org/nightly/rustc/platform-support/wasm32-wasip1.html)).
wazero's public `Runtime.CompileModule` loads a **core** Wasm binary and its built-in WASI is
`wasi_snapshot_preview1`; there is no component-model loader in the runtime API
([runtime.go L90-L103](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L90-L103),
[imports directory](https://github.com/tetratelabs/wazero/tree/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports)).
Therefore plugin toolchains must target `wasm32-wasip1` (or a freestanding core module), not
`wasm32-wasip2`.

Preview 1 has no async/promise model. `poll_oneoff` reports readiness of WASI file descriptors and
clocks; it does not integrate with host-side asynchronous work. Custom streaming must use host
imports (section 11).

---

## 7. Threads, atomics, async, and streaming consequences

### 7.1 Default: no threads, no atomics, no shared memory

The default feature set is `api.CoreFeaturesV2`, enabling mutable globals, bulk memory, multi-value,
non-trapping float-to-int, reference types, sign-extension, and SIMD. It does **not** define threads,
atomics, or shared memory
([api/features.go L1-L90](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/features.go#L1-L90),
[config.go L196-L203](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L196-L203)).
`RuntimeConfig.WithCoreFeatures` can disable features, or opt into an experimental feature by OR-ing
an `experimental` constant; there is no stable threads constant in `api`.

### 7.2 Experimental threads exist, but guests still cannot spawn threads

wazero main has `experimental.CoreFeaturesThreads` and can validate/execute shared memory and atomic
instructions when enabled. Shared memory must declare a maximum. On arm64 the compiler additionally
requires hardware atomics
([experimental/features.go L5-L20](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/features.go#L5-L20),
[internal/platform/platform.go L20-L38](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go#L20-L38),
[internal/wasm/binary/memory.go L25-L42](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/binary/memory.go#L25-L42)).

However:

- The WebAssembly threads proposal does not add a thread-creation instruction; "the responsibility
  of creating and joining threads is deferred to the embedder"
  ([threads proposal overview](https://github.com/WebAssembly/threads/blob/main/proposals/threads/Overview.md)).
- wazero has no `wasi-threads` / `thread_spawn` import in its imports tree; its WASI Preview 1
  exports are the list in section 6
  ([wasi.go L175-L220](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports/wasi_snapshot_preview1/wasi.go#L175-L220)).
- `api.Memory` exposes no atomic operations; the experimental docs say atomic operations are
  "guest-only until api.Memory or otherwise expose them to host functions"
  ([experimental/features.go L5-L20](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/features.go#L5-L20)).
- wazero's tests drive concurrency from Go: they create a separate `api.Function` per goroutine and
  call the same instance concurrently with a shared, atomic guest memory
  ([internal/integration_test/engine/threads_test.go L90-L120](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/integration_test/engine/threads_test.go#L90-L120)).
  The guest did not create those agents.

**For the plugin ABI this means: do not depend on threads.** Keep the stable ABI single-threaded.
Use the experimental flag only if a specific plugin class needs shared-memory atomics and you accept
an unstable wazero API; even then, the host owns thread creation.

### 7.3 What this means for async

- Guests cannot run in the background on their own. A guest runs only while the host is inside a
  `Call` (or during a start function).
- There is no async host import: every import is a synchronous Go call. The host may block that call
  (for example on a Go channel) but then the guest is blocked too.
- There is no completion callback. Implement async as a state machine:
  - host import starts work and returns an opaque handle plus `WOULD_BLOCK`;
  - guest polls `host_poll(handle)` or a general `host_request` later;
  - host completes the work on its own goroutine and queues the result;
  - next guest call observes the result.
- Because calls must be serialized per instance, one dedicated goroutine per instance should own the
  call stream and the input queue. Parallelism comes from running different plugin instances on
  different goroutines, not from threads inside one instance.

### 7.4 What this means for streaming

- The only shared buffer between host and guest is the guest's linear memory. Host-owned buffers must
  be copied in/out; there is no zero-copy handoff and no shared heap across instances.
- Host memory views (`Memory.Read`) are views, not copies, and are invalidated when the guest grows
  memory. Never retain a `[]byte` from `Read` across a call; re-fetch after any call or grow. For a
  stable view, declare min=max or use eager capacity reservation
  ([api/wasm.go L600-L640](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L600-L640),
  [config.go L55-L78](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L55-L78)).
- Large transfers should be chunked, with an explicit per-chunk maximum. This bounds memory, latency,
  and the damage from a malicious length.
- Backpressure must be explicit: a non-blocking "would block" status, a credit/window counter, or a
  partial-write return. WASI `poll_oneoff` only covers WASI fds; it is not a general host event
  mechanism.

---

## 8. Error and trap isolation

### 8.1 Guest traps

A guest trap is a Wasm runtime error: `unreachable`, out-of-bounds memory access, integer divide by
zero, invalid conversion, indirect-call type mismatch, stack overflow, unaligned atomic, null
reference, uncaught exception, and similar
([internal/wasmruntime/errors.go L1-L40](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasmruntime/errors.go#L1-L40)).
The engine raises these as Go panics internally and recovers them at the call boundary:

- `*sys.ExitError` is returned unwrapped (exit/proc_exit/close);
- an internal Wasm error becomes `fmt.Errorf("wasm error: %w\nwasm stack trace:\n\t%s", ...)`;
- a host-function panic that implements `runtime.Error` becomes
  `"... (recovered by wazero)\nwasm stack trace:..."` plus a Go stack trace;
- any other host panic (error or string) is likewise recovered and wrapped
  ([internal/wasmdebug/debug.go L122-L152](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasmdebug/debug.go#L122-L152)).

The call engine explicitly resets its state after an error so it can be reused
([internal/engine/wazevo/call_engine.go L325-L335](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/engine/wazevo/call_engine.go#L325-L335)).
But a trap can leave guest data structures inconsistent, so the plugin instance should be considered
poisoned and recycled.

A trap does **not** crash the host process and does not, by itself, close the module. Only explicit
close, `proc_exit`, or context-done termination closes it.

Caveat: the trap error type is in `internal/wasmruntime`, so host code cannot type-assert it. The
public, typed error is `*sys.ExitError`; everything else must be matched by message or treated as an
opaque failure. This is a strong argument for defining the plugin ABI's own status codes and never
using traps for expected errors.

### 8.2 `proc_exit` and `CloseWithExitCode`

- `proc_exit(0)` closes the module; because `sys.ExitError` is only returned for a non-zero code, the
  caller must check `Module.IsClosed()` to know the module is no longer usable
  ([sys/error.go L20-L45](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go#L20-L45),
  [api/wasm.go L199-L212](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L199-L212)).
- `proc_exit(nonzero)` / `CloseWithExitCode(nonzero)` returns `*sys.ExitError` with that code.
- The special exit codes `0xffffffff` and `0xefffffff` are reserved for context cancellation and
  deadline expiry
  ([sys/error.go L10-L18](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go#L10-L18)).

**Recommendation:** do not use `proc_exit` as the normal plugin shutdown path. Export
`natcatty_shutdown()` and treat `proc_exit` as an abort. Map `sys.ExitError` to an ABI-level
`EXITED`/`CANCELLED`/`DEADLINE` status.

### 8.3 Sandbox boundary

The guest cannot read or write host memory directly. All host data transfer goes through the
`api.Memory` bounds-checked accessors or explicit imports. The realistic risks are resource
exhaustion (memory, CPU, call time), import misuse (a privileged import trusts unchecked guest
offsets/lengths), and host-function bugs. Validate every `(ptr, len)` pair with `Memory.Read` and
check for overflow before allocating.

---

## 9. Tested platform support and platform risks

### 9.1 wazero's tested matrix

wazero's support policy is explicit: "The only supported operating systems are ones we test"
([README L81-L100](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L81-L100)).
The current CI matrix is:

| Target | Compiler | Interpreter | Evidence |
|---|---|---|---|
| linux/amd64 | tested | tested | CI matrix |
| linux/arm64 | tested | tested | CI matrix |
| linux/riscv64 | no | tested | README |
| darwin/arm64 (macOS) | tested | tested | CI matrix (`macos-14`, arm64) |
| darwin/amd64 (macOS Intel) | **not tested** | **not tested** | README says macOS tested only on arm64 |
| windows/amd64 | tested | tested | CI matrix (`windows-2025`, amd64) |
| windows/arm64 | not tested | not tested | README says Windows tested only on amd64 |
| freebsd/netbsd/openbsd/dragonfly/illumos/solaris amd64 | tested via nested VMs | tested | README |

CI definitions: [commit.yaml L66-L78](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/.github/workflows/commit.yaml#L66-L78).
The compiler conformance table marks amd64 and arm64 as supported and "others" as not
([README L46-L53](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L46-L53)).

### 9.2 darwin/amd64: the macOS universal-build risk

This is the sharpest platform risk for a desktop app.

- The code-level check accepts `darwin` on amd64: `compilerPlatformSupports` permits `darwin` with
  amd64 + SSE4.1 (or arm64), and `CompilerSupported` additionally probes executable `mmap` at
  runtime
  ([internal/platform/platform.go L14-L70](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go#L14-L70)).
- The documented policy and CI do **not** cover darwin/amd64; macOS is tested only on arm64
  ([README L81-L100](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L81-L100)).
- A universal macOS binary contains separate amd64 and arm64 slices. The amd64 slice may run on an
  Intel Mac or under Rosetta on Apple Silicon. wazero is pure Go and has no CGO, so cross-compiling
  both slices is mechanically straightforward, but the amd64 slice is outside wazero's supported
  matrix and has no CI coverage.
- Practical failure modes on the untested slice are: compiler supported/unsupported mismatch,
  executable-mmap restrictions, and untested amd64 code-generation/atomic instruction paths. The
  interpreter is a portable fallback but is roughly 10x slower
  ([README L35-L43](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L35-L43)).

**Recommendation:** use `wazero.NewRuntimeConfig()` (auto-detect, fall back to interpreter) rather
than forcing `NewRuntimeConfigCompiler()`. Add a darwin/amd64 smoke test (an Intel runner, or the
amd64 slice under Rosetta) that compiles and runs the plugin ABI test module. If that is impossible,
either accept interpreter performance on darwin/amd64 or declare arm64-only support for the plugin
runtime.

### 9.3 macOS code-signing entitlements

When the compiler engine is used in a signed macOS app, the app needs executable-memory
entitlements. The wazero issue reports that without the appropriate entitlements, "OSX will kill any
go processes attempting to compile a wasm file"; the reported entitlements are
`com.apple.security.cs.allow-jit` and `com.apple.security.cs.allow-unsigned-executable-memory`
([wazero issue #2393](https://github.com/wazero/wazero/issues/2393)).
The README points developers at this issue
([README L116-L120](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L116-L120)).

The issue was reported on arm64, but hardened-runtime and notarization rules apply to both slices of
a universal app. Sign and notarize both slices with the entitlements, and test the amd64 slice too.
If the product cannot ship these entitlements, force the interpreter on macOS (slower) or use only
the auto-detected fallback.

### 9.4 Windows and Linux

- windows/amd64 is tested for both engines; it is the safest non-macOS desktop target in the matrix.
- linux/amd64 and linux/arm64 are tested for both engines. Experimental threads on arm64 additionally
  require the CPU atomic feature; without it, `CompilerSupports(threads)` is false and the compiler
  cannot be used for threads
  ([platform.go L20-L38](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go#L20-L38)).
- Environments with executable-mmap disabled (hardened containers, `no_prot_exec`) make the compiler
  unavailable; `NewRuntimeConfig()` falls back to the interpreter, while
  `NewRuntimeConfigCompiler()` forces the compiler and does not fall back (it panics if the platform
  lacks support) ([platform.go L40-L70](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go#L40-L70),
  [config.go L225-L240](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L225-L240)).

---

## 10. Toolchain implications: TinyGo, Rust, Zig, C (and Go std)

All plugin toolchains must emit a **core** Wasm module that imports `wasi_snapshot_preview1` (or is
freestanding). WASI modules must export their memory as `memory`; for a custom ABI the host can also
reach the instance's memory through `api.Module.Memory()` even when it is not exported. A WASI
0.2/0.3 component is not loadable by wazero's core-module API.

| Toolchain | Target to use | Notable facts | Size/perf implication |
|---|---|---|---|
| TinyGo | `GOOS=wasip1 GOARCH=wasm` | Supports WASI Preview 1 and 2; `main` is required for `wasip1` even if unused; exports via `//go:wasmexport` ([TinyGo WASI docs](https://tinygo.org/docs/guides/webassembly/wasi/)) | Best Go-family size. Go's own wiki: TinyGo output is often "~10kB" and a hello-world was 575 bytes ([Go WebAssembly wiki](https://go.dev/wiki/WebAssembly)). Requires bulk-memory features, which are in `CoreFeaturesV2` ([config.go L40-L50](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L40-L50)). |
| Go std | `GOOS=wasip1 GOARCH=wasm` | WASI port added in Go 1.21; `//go:wasmimport`; Go 1.24 adds `//go:wasmexport` and `-buildmode=c-shared` reactor builds ([Go 1.21 release notes](https://go.dev/doc/go1.21), [Go 1.24 release notes](https://go.dev/doc/go1.24), [cmd/compile WebAssembly directives](https://pkg.go.dev/cmd/compile)) | Large: "smallest possible size being around ~2MB"; "10MB+ is common" ([Go wiki](https://go.dev/wiki/WebAssembly)). Practical only for large plugins or when a single Go runtime is acceptable. |
| Rust | `wasm32-wasip1` (not `wasm32-wasip2`) | Tier 2; `std` supported but not all APIs; thread spawning always errors (use `wasm32-wasip1-threads`); `panic=abort` by default; rustup ships a self-contained sysroot; `cdylib` for exports ([Rust target docs](https://doc.rust-lang.org/nightly/rustc/platform-support/wasm32-wasip1.html)) | No official size figure in the target docs; static `std` + wasi-libc means measure per plugin. With `panic=abort`, a panic aborts rather than unwinding, so expect a trap/abort rather than a recoverable guest panic. |
| Zig | `wasm32-wasi` (Preview 1) or freestanding | Zig supports WebAssembly out of the box, including a freestanding target and a WASI target; WASI support is "under active development" ([Zig langref, WebAssembly/WASI sections](https://github.com/ziglang/zig/blob/master/doc/langref.html.in#L7006-L7040)) | Use `-O ReleaseSmall` for size ([Zig langref optimization modes](https://github.com/ziglang/zig/blob/master/doc/langref.html.in#L5920-L5975)). No official size figure; small static binaries are typical but should be measured. |
| C/C++ | `wasm32-wasip1` via wasi-sdk | Default target is `wasm32-wasip1`; static sysroot with wasi-libc; no networking in WASIp1; threads only via experimental `wasm32-wasip1-threads`; LTO option ([wasi-sdk README](https://github.com/WebAssembly/wasi-sdk)) | Size scales with libc usage and optimization/LTO; measure per plugin. Static linking is the default and the least surprising for plugins. |

### 10.1 Performance consequences in wazero

- The compiler engine AOT-compiles at `CompileModule`; it is "faster than Interpreter, often by
  order of magnitude (10x) or more" ([README L35-L43](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L35-L43)).
- AOT cost scales with module size. Compile each distinct plugin binary once and instantiate many
  times; persist the compilation cache across process restarts with `NewCompilationCacheWithDir`
  ([cache.go L21-L75](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go#L21-L75)).
- Interpreter fallback is portable but an order of magnitude slower; it is the safety net for
  darwin/amd64 and for environments without executable mmap.
- Host-function crossings and memory copies have a cost; keep chunks reasonably large, avoid one
  import call per byte, and batch events.

---

## 11. Streaming and backpressure options across the host boundary

Because the guest is single-threaded and calls are synchronous, streaming is a scheduling problem,
not an async-I/O problem. The realistic patterns:

### Option A: guest pull (simplest)

Guest export loop:
1. guest calls `input_read(ptr, cap)`;
2. host copies up to `cap` bytes into guest memory and returns the count, or `WOULD_BLOCK`;
3. guest processes and calls `output_write(stream, ptr, len)`;
4. host copies out and returns the number accepted.

Backpressure is natural: the guest only asks for what it can hold, and the host returns at most the
available bytes. Latency is bounded by the guest's poll cadence. A blocking `input_read` is possible
but then wazero cannot interrupt the instance; return `WOULD_BLOCK` and use `sleep_ns`/`yield`
instead, or give the import a deadline-bearing context.

### Option B: host push (lowest latency)

Host calls `natcatty_dispatch(ptr, len)` when input arrives. The host owns a bounded queue and does
not read more from the upstream source than the queue capacity plus the in-flight call. If the guest
is slow, the queue fills and upstream reads stop: that is the backpressure. The host must serialize
`dispatch` with every other call into the instance.

### Option C: hybrid (recommended)

One dispatcher goroutine per instance owns the call stream:

- host pushes small control events with `dispatch`;
- bulk input is pulled by the guest with `input_read`;
- bulk output is pushed by the guest with `output_write`;
- `output_write` returns the accepted byte count; a short return means "try again later";
- `input_credit(n)` acknowledges consumed input and replenishes the host's window.

This gives low-latency control messages without unbounded host buffering, and a natural credit-based
flow-control window for bulk data.

### Option D: opaque host handles

For network/storage streams, the host returns an opaque `i64` handle and the guest uses
`request(handle, op, ...)`. The host keeps the actual buffer; the guest copies chunks in and out.
This avoids retaining guest pointers and lets the host implement cancellation/timeouts. Pair it with
`request_cancel(handle)`.

### Rules that apply to all options

- Define a maximum chunk size (for example 64 KiB) and a maximum total per call.
- Validate `(ptr, len)` before every read/write; reject overflow.
- Never retain a `Memory.Read` view across a call or a potential grow; copy or re-fetch.
- Never block a host import without selecting on `ctx.Done()`.
- Return explicit statuses (`WOULD_BLOCK`, `CLOSED`, `LIMIT_EXCEEDED`), never silently drop data.
- Bound the input queue and the number of in-flight requests; the host's queue is the real
  backpressure signal.
- Remember that `poll_oneoff` is only for WASI fds and clocks; do not try to stretch it into a
  general completion mechanism.

---

## 12. Recommended ABI skeleton

### 12.1 Module shape

- **Core Wasm module**, not a component.
- **Reactor style**: no `_start` invocation by default. Instantiate with
  `ModuleConfig.WithStartFunctions()` (empty) and call an explicit init export.
- Export `memory` (required for WASI and for host access).
- One instance per plugin; unique or anonymous module name. One dispatcher goroutine per instance.
- One linear memory; all pointers are 32-bit offsets into it.

### 12.2 Guest exports

| Export | Wasm signature | Purpose |
|---|---|---|
| `memory` | memory | Guest linear memory. Declare an explicit maximum; prefer min=max for stable host views. |
| `natcatty_abi_version` | `() -> i64` | Packed `major << 32` plus `minor`. Called by the host immediately after instantiation, before any other export. |
| `natcatty_init` | `(ptr: i32, len: i32) -> i32` | One-time initialization with a TLV/JSON config blob (plugin config, granted capabilities, limits). Returns an ABI status. |
| `natcatty_dispatch` | `(ptr: i32, len: i32) -> i32` | Process one control event or input chunk. Host-driven push point. |
| `natcatty_poll` | `(ptr: i32, cap: i32) -> i32` | Guest produces pending output/events into the buffer. Returns bytes written, `WOULD_BLOCK`, or an error. |
| `natcatty_shutdown` | `() -> i32` | Graceful stop. Host calls it before `Close`. |
| `natcatty_scratch` | `() -> i64` | Optional. Packed `ptr << 32` plus `len` of a guest-owned scratch buffer for host writes. |
| `_initialize` | `() -> ()` | Optional toolchain reactor initializer (notably Go `-buildmode=c-shared`); host calls it explicitly if present. |

Do not require `_start`. If a toolchain emits `_start` for a command-style module, clear it and use
the reactor exports instead. TinyGo's `wasip1` target requires a `main` symbol even for a library,
but exports are still reachable via `//go:wasmexport`
([TinyGo WASI docs](https://tinygo.org/docs/guides/webassembly/wasi/)).

### 12.3 Host imports: module `natcatty_host_v1`

The major version is part of the import module name. A host that does not export
`natcatty_host_v1` causes instantiation to fail, which is the desired fail-closed behavior
([runtime.go L105-L124](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go#L105-L124)).

| Import | Wasm signature | Purpose |
|---|---|---|
| `abi_version` | `() -> i64` | Host ABI version, same packing as the guest export. The guest may check it during `natcatty_init`. |
| `log` | `(level: i32, ptr: i32, len: i32) -> i32` | Structured log record; status only. |
| `now_ns` | `() -> i64` | Monotonic nanoseconds. |
| `random_get` | `(ptr: i32, len: i32) -> i32` | Cryptographic random bytes. |
| `sleep_ns` | `(ns: i64) -> i32` | Cooperative sleep; must honor `ctx`. |
| `yield` | `() -> i32` | Cooperative yield point. |
| `input_read` | `(ptr: i32, cap: i32) -> i32` | Pull input. `>=0` bytes read, `WOULD_BLOCK`, `CLOSED`, or a negative error. |
| `input_credit` | `(n: i32) -> i32` | Acknowledge consumed input; returns the remaining window. |
| `output_write` | `(stream: i32, ptr: i32, len: i32) -> i32` | Push output; `>=0` bytes accepted (may be short), or a negative error. |
| `request` | `(op: i32, req_ptr: i32, req_len: i32, resp_ptr: i32, resp_cap: i32) -> i32` | Generic capability RPC: networking, storage, OS integration. The escape hatch that keeps the ABI small. |
| `request_cancel` | `(handle: i64) -> i32` | Cancel an in-flight host request. |
| `abort` | `(code: i32) -> ()` | Optional fatal abort; maps to `CloseWithExitCode`. |

The host should also export `wasi_snapshot_preview1` for toolchain support (clocks, random, stdio,
`sched_yield`, `proc_exit`), but should deny filesystem access by default and configure real clocks
and `crypto/rand` explicitly. The custom imports carry application capabilities; WASI is only the
toolchain runtime.

### 12.4 Memory ownership

1. **Guest owns its linear memory.** Host pointers never cross into the guest; guest pointers never
   escape the call.
2. **All bulk data is `(ptr: u32, len: u32)` in guest memory**, UTF-8 for text, little-endian for
   integers, natural alignment for `i64`/`f64`.
3. **Host writes only into guest-provided buffers** (`input_read` writes into a guest buffer;
   `output_write` reads from one). If the host must allocate for the guest, use a guest export such as
   `natcatty_alloc(len) -> ptr` or a fixed scratch buffer from `natcatty_scratch`.
4. **The host copies in and out.** Do not retain a guest memory view across calls; memory growth
   invalidates it
   ([api/wasm.go L600-L640](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L600-L640)).
5. **No Go pointers, slices, strings, maps, or channels cross the boundary.** Use opaque `i64`
   handles for host-owned resources; validate handles against the instance's capability table.
6. **Do not use `externref` as a generic handle.** It is a raw `uintptr` in wazero and is unsafe to
   expose to untrusted code
   ([api/wasm.go L91-L109](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L91-L109)).
7. **Validate every guest-supplied offset and length.** `Memory.Read`/`Write` return `false` out of
   range; a host import that ignores the boolean is a bug.

### 12.5 Version negotiation

1. **Major version in the import module name**: `natcatty_host_v1`. A major mismatch fails at
   instantiation with a missing-import error, before any guest code runs.
2. **Minor version in exported/imported `abi_version()`**: packed `major << 32` plus `minor`. The host
   checks the guest's export immediately after instantiation.
3. **Manifest**: each plugin ships `abi_major`, `abi_minor`, `memory_min_pages`, `memory_max_pages`,
   and requested capabilities. The host validates the manifest against the compiled module
   (`CompiledModule.ExportedFunctions`, `ExportedMemories`, `ImportedFunctions`) before instantiating.
4. **Rules**: minor versions are additive only. Removing or changing an import, export, status
   meaning, or memory layout requires a new major import module name. Unknown fields in a config
   blob must be ignored by the receiver (forward compatibility).
5. **Failure is closed**: unknown major, missing required export, unexpected memory maximum, or
   changed ABI version => reject before `natcatty_init`.

Wasm has no built-in version negotiation; import/export names and an explicit version function are
the mechanism.

### 12.6 Error model

Use `i32` status codes. Do not use traps or Go panics for expected errors.

| Code | Meaning |
|---|---|
| `0` | Success. |
| `> 0` | Success with a count (bytes read/written, items processed). |
| `-1` | `WOULD_BLOCK` / retry later. |
| `-2` | `CLOSED` / end of stream. |
| `-3` | `INVALID_ARGUMENT` (bad pointer, length, enum). |
| `-4` | `NO_CAPABILITY` (operation not granted). |
| `-5` | `LIMIT_EXCEEDED` (memory, chunk, queue, credit). |
| `-6` | `CANCELLED`. |
| `-7` | `DEADLINE_EXCEEDED`. |
| `-8` | `UNSUPPORTED`. |
| `-100..-199` | ABI/plugin-defined. |
| `-200..-299` | Host/capability-defined. |

Detailed diagnostics go through `log` or a per-call error buffer; the status code must remain stable.
A trap is a fatal invocation failure; map it to `-7`/`-8`/an internal failure, close the instance,
and do not attempt to resume it. Map `*sys.ExitError`:
`ExitCodeContextCanceled -> CANCELLED`, `ExitCodeDeadlineExceeded -> DEADLINE_EXCEEDED`, other
non-zero -> `EXITED`.

### 12.7 Limits

| Limit | Where enforced | Suggested starting point |
|---|---|---|
| Linear memory per instance | `RuntimeConfig.WithMemoryLimitPages`, plus manifest / `MemoryDefinition.Max()` | 64-256 MiB for ordinary plugins; require an encoded maximum. |
| Aggregate memory | host accounting (instance count x budget), optional `experimental.MemoryAllocator` | Set a process-wide cap well below the machine's memory. |
| Module binary size | host before `CompileModule` | A few MiB for TinyGo/Rust/C; larger for Go std requires explicit approval. |
| Chunk size | ABI (`input_read` cap, `output_write` len) | 64 KiB. |
| Per-call wall time | `context.WithTimeout` + `WithCloseOnContextDone(true)` | 50-500 ms for event dispatch; longer for explicit batch jobs. |
| Init/instantiate time | host deadline; close the instance on timeout | A few seconds, including AOT compile if not cached. |
| In-flight calls per instance | host dispatcher | Exactly 1. |
| Input queue depth / credits | host | Bounded by bytes, not just messages. |
| Host-call nesting depth | host | Small fixed limit (for example 8). |
| Concurrent instances | host | Bounded by aggregate memory and CPU. |
| CPU instructions | no wazero fuel API; use deadline + watchdog | Watchdog closes the instance shortly after the deadline. |

wazero's own hard implementation limits are listed in section 13.

### 12.8 Runtime configuration baseline

**Recommendation** for a production plugin runtime:

```go
cache, _ := wazero.NewCompilationCacheWithDir(cacheDir) // optional persistence
r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig(). // auto: compiler if supported
    WithMemoryLimitPages(budgetPages).
    WithCloseOnContextDone(true).
    WithCompilationCache(cache))

wasi_snapshot_preview1.MustInstantiate(ctx, r) // toolchain runtime support
// plus a host module named "natcatty_host_v1"

mod, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
    WithName(uniqueID).
    WithStartFunctions().          // do NOT auto-run _start
    WithSysWalltime().             // or a deterministic clock for tests
    WithSysNanotime().
    WithSysNanosleep().
    WithRandSource(rand.Reader))   // "crypto/rand"; default is deterministic
```

Notes:

- `WithStartFunctions()` with no arguments clears the default `_start`
  ([config.go L513-L529](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L513-L529)).
- Call `_initialize` manually if the toolchain emits it and it is required.
- Defaults are sandboxed for filesystem, stdio, args/env, time, and random (no filesystem, discarded
  stdio, empty args/env, fake clocks, deterministic random); override only the capabilities the plugin
  is granted (section 6.2).
- If one runtime must host plugins with different memory budgets, use one runtime per budget class or
  the manifest + custom-allocator approach (section 3.2).

---

## 13. Hard limits and constraints (consolidated)

| Constraint | Value / behavior | Source |
|---|---|---|
| Max linear memory | 65,536 pages = 4 GiB, per memory | [memory.go L21-L29](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/memory.go#L21-L29) |
| Memories per module | 1 (Core 2.0) | [api/wasm.go L177-L185](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L177-L185) |
| Memory64 / multi-memory | not in the default feature set | [api/features.go L1-L90](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/features.go#L1-L90) |
| Threads / atomics / shared memory | not in `CoreFeaturesV2`; experimental opt-in; no guest thread spawn | [api/features.go](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/features.go), [threads proposal](https://github.com/WebAssembly/threads/blob/main/proposals/threads/Overview.md) |
| Functions per module | 2^27 | [RATIONALE.md L1465-L1472](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1465-L1472) |
| Function types per store | 2^27 | [RATIONALE.md L1472-L1478](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1472-L1478) |
| Values on the stack in a function | 2^27 | [RATIONALE.md L1479-L1486](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1479-L1486) |
| Globals per module | 2^27 | [RATIONALE.md L1487-L1492](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1487-L1492) |
| Tables per module | 2^27 | [RATIONALE.md L1493-L1502](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/RATIONALE.md#L1493-L1502) |
| CPU/fuel metering | none; deadline + cooperative checks only | [config.go L30-L175](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L30-L175) |
| Concurrent calls per `api.Function` | 1 (not goroutine-safe) | [api/wasm.go L371-L385](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L371-L385) |
| Memory view lifetime | invalidated by growth/capacity change | [api/wasm.go L600-L640](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L600-L640) |
| Module names | unique per runtime unless anonymous (`""`) | [config.go L496-L510](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L496-L510) |
| Default start function | `_start` | [config.go L688-L692](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go#L688-L692) |
| WASI version | Preview 1 (`wasi_snapshot_preview1`); no component model | [wasi.go](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports/wasi_snapshot_preview1/wasi.go) |
| WASI sockets | accept/recv/send/shutdown on pre-opened sockets only; no bind/listen/connect | [WITX L497-L544](https://github.com/WebAssembly/WASI/blob/wasi-0.1/preview1/witx/wasi_snapshot_preview1.witx#L497-L544) |
| Trap error typing | only `*sys.ExitError` is public and stable; other traps are string-wrapped | [debug.go L122-L152](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasmdebug/debug.go#L122-L152) |
| Module/instance cleanup | explicit `Close`/`CloseWithExitCode`; runtime close closes all | [api/wasm.go L186-L212](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go#L186-L212) |
| Platform support | tested matrix only; macOS arm64, Windows amd64, Linux amd64/arm64 | [README L81-L100](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L81-L100) |

---

## 14. Platform risks and recommendations

### darwin/amd64 (macOS universal build) - highest risk

- Not tested by wazero CI; the support policy says only tested OSes are supported. The amd64 slice of
  a universal app is therefore outside the documented support matrix, even though the code path
  accepts it.
- The compiler needs SSE4.1 and executable `mmap`; the interpreter is the fallback
  ([platform.go L14-L70](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go#L14-L70)).
- Signed/notarized apps need the macOS executable-memory entitlements; otherwise the OS may kill the
  process when the compiler compiles a module
  ([issue #2393](https://github.com/wazero/wazero/issues/2393)).
- Mitigations, in order of preference:
  1. add a darwin/amd64 smoke test (Intel hardware or the amd64 slice under Rosetta) to the release
     pipeline;
  2. use `NewRuntimeConfig()` auto-detection so an unsupported environment falls back to the
     interpreter instead of panicking or failing at compile;
  3. if neither is possible, force the interpreter on darwin/amd64 and document the performance
     hit, or ship arm64-only plugin support.
- Sign both slices with `com.apple.security.cs.allow-jit` and
  `com.apple.security.cs.allow-unsigned-executable-memory`, and verify notarization on both.

### Windows amd64

- Tested for both engines. Use `NewRuntimeConfig()` so an environment with executable-mmap
  restrictions falls back cleanly.
- No special concern beyond the general memory/CPU limits.

### Linux amd64/arm64

- Tested for both engines. Container images with `no_prot_exec` disable the compiler; auto config
  falls back to the interpreter.
- Experimental threads on arm64 require the CPU atomic feature; do not assume it.

### All platforms

- The interpreter is the portable fallback but 10x slower; decide whether that is acceptable for
  large plugins.
- Pin wazero to a specific minor version. wazero promises semver for exported API, but it is a
  young runtime; the compilation cache is keyed on wazero version, so upgrades invalidate cache
  entries
  ([README L62-L64](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md#L62-L64),
  [cache.go L52-L75](https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go#L52-L75)).

---

## 15. Open questions and tests to add before freezing the ABI

1. **darwin/amd64**: compile and run the ABI conformance module on an Intel Mac and under Rosetta;
   record whether the compiler or interpreter is selected and the throughput difference.
2. **macOS signing**: verify the compiler works with the two entitlements in a hardened, notarized
   universal build, on both slices.
3. **Memory budget**: confirm that a single runtime with `WithMemoryLimitPages(N)` plus a manifest
   check actually rejects/grows correctly for a plugin that declares min=max and for one that
   declares no max. Add a regression test for `MemoryDefinition.Max()` `encoded=false`.
4. **Cancellation**: test a guest infinite loop with and without `WithCloseOnContextDone`; test a
   blocking host import with a deadline to confirm the import must cooperate.
5. **Trap behavior**: confirm a trap does not close the module in the chosen wazero version and that
   `IsClosed` remains false; decide whether the ABI will recycle regardless.
6. **Shared cache**: if multiple runtimes use different memory limits and share a
   `CompilationCache`, verify the cached module's memory bounds are per-runtime and not shared.
7. **Streaming**: benchmark 64 KiB chunking versus larger chunks for the actual plugin workloads;
   tune the credit window and queue depth.
8. **Toolchain sizes**: build a representative "hello plugin" in TinyGo, Rust, Zig, and C with
   `ReleaseSmall`/LTO/`-Os` and record actual module sizes; the published docs give figures only for
   Go and TinyGo.

---

## Appendix: source index

- wazero repository (pinned): <https://github.com/tetratelabs/wazero/tree/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7>
- wazero README (platforms, compiler, support policy, entitlements): <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/README.md>
- wazero `config.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/config.go>
- wazero `runtime.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/runtime.go>
- wazero `api/wasm.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/wasm.go>
- wazero `api/features.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/api/features.go>
- wazero `builder.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/builder.go>
- wazero `cache.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/cache.go>
- wazero `sys/error.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/sys/error.go>
- wazero `internal/wasm/memory.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/memory.go>
- wazero `internal/wasm/binary/decoder.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasm/binary/decoder.go>
- wazero `internal/wasmdebug/debug.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/wasmdebug/debug.go>
- wazero `internal/platform/platform.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/internal/platform/platform.go>
- wazero `imports/wasi_snapshot_preview1/wasi.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/imports/wasi_snapshot_preview1/wasi.go>
- wazero `experimental/features.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/features.go>
- wazero `experimental/memory.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/memory.go>
- wazero `experimental/sock/sock.go`: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/experimental/sock/sock.go>
- wazero CI matrix: <https://github.com/tetratelabs/wazero/blob/ef2dd2f85376c1a6ff79baae0cc1a5aa42c91db7/.github/workflows/commit.yaml>
- wazero issue #2393 (macOS entitlements): <https://github.com/wazero/wazero/issues/2393>
- WASI Preview 1 WITX: <https://github.com/WebAssembly/WASI/blob/wasi-0.1/preview1/witx/wasi_snapshot_preview1.witx>
- WASI 0.1 overview: <https://wasi.dev/releases/wasi-p1>
- WebAssembly threads proposal: <https://github.com/WebAssembly/threads/blob/main/proposals/threads/Overview.md>
- Go WebAssembly wiki: <https://go.dev/wiki/WebAssembly>
- Go 1.21 release notes (wasip1): <https://go.dev/doc/go1.21>
- Go 1.24 release notes (`//go:wasmexport`, reactor): <https://go.dev/doc/go1.24>
- Go compiler WebAssembly directives: <https://pkg.go.dev/cmd/compile>
- TinyGo WASI: <https://tinygo.org/docs/guides/webassembly/wasi/>
- Rust `wasm32-wasip1`: <https://doc.rust-lang.org/nightly/rustc/platform-support/wasm32-wasip1.html>
- Zig language reference (WebAssembly/WASI): <https://github.com/ziglang/zig/blob/master/doc/langref.html.in>
- wasi-sdk: <https://github.com/WebAssembly/wasi-sdk>
