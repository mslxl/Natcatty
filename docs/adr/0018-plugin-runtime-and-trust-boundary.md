# Plugin runtime and trust boundary

The original `.ncpkg` runtime is not reproduced, but the product still needs an
extension surface for untrusted, unsigned code. We choose a WASM-only third-party
runtime on wazero and compile-time Go packages for first-party plugins, both inside a
Cordis-style container with host-authoritative permissions, effects, and lifecycle.
This buys memory safety, portability, and a small auditable capability surface without
spawning extra processes; the cost is a narrower performance envelope and no reuse of
native libraries, JVMs, or sidecars. Authentication and host-key trust stay host-owned,
and external products are treated as capability probes, never parity targets.

**Status**: accepted

**Amends**: ADR-0006, ADR-0007, ADR-0009. Plugin UI rules are in ADR-0019.

**Considered Options**:
- Sidecar or native helper processes, JVM/JDBC drivers: broader ecosystem, but adds
  processes the host cannot sandbox or audit as one capability boundary.
- In-process dynamic Go, the Go `plugin` build mode, or moejs: no stable cross-version
  ABI, unsafe unload, and tight coupling to the host toolchain.
- No plugin system: simplest, but drops the extension surface the product requires.

**Consequences**:
- Third-party plugins run only as WASM modules in wazero: reactor-style, one in-flight
  call per instance, scalar host imports, no sockets, threads, or ambient authority.
  First-party plugins are compiled-in Go packages using the same contribution and
  lifecycle model.
- Permissions are a closed, resource-scoped, user-granted set enforced at the host
  boundary and audited. Unsigned installation relies on install/activation consent, the
  sandbox and resource caps, integrity hashes with TOFU, no silent updates, and
  rollback; the registry is not a trust anchor.
- Authentication and host-key trust remain host-owned. Plugins may invoke host
  authentication only and may never provide or modify it. A host-owned native engine is
  a host feature: no plugin may require one, and it is never exposed as a plugin
  capability.
- External products are capability probes, never parity targets. No plugin interface,
  scope tier, driver list, or UI may be derived from one.
- There is no `.ncpkg` compatibility and no plugin-data migration.
- The runtime follows the layered boundaries: `domain/plugin`, `application/plugin`,
  `adapters/wasm`, and `ui/` consumers; first-party plugins live under `plugins/<id>`.
  Contribution descriptors live in `domain/plugin`, consumer contexts keep their own
  registries, and `application/plugin` reaches them through ports wired in `app/`.
- Plugin strings are package assets with their own catalog contract; they do not enter
  the MyGo catalog generator governed by ADR-0009.
- Supported plugin platforms are macOS arm64, macOS amd64 (the universal Intel slice,
  including Rosetta; the amd64 slice runs a compiler self-test and falls back to the
  interpreter), Windows amd64, and Linux amd64/arm64. wazero auto-selects compiler or
  interpreter, both engines ship in every artifact, and the build stays cgo-free. The
  selected mode is recorded in the startup log and exposed to plugin diagnostics;
  interpreter compatibility mode keeps every timeout unchanged. CI runs a real-engine
  smoke test on all five targets (including both macOS slices), and the full conformance
  suite on linux/amd64 and macOS arm64. The compiler plus interpreter cost is checked
  against a soft 10 MiB per-target growth budget by `sizecheck`; startup
  compilation happens lazily on first plugin activation, and no on-disk compilation
  cache is used. The current ad-hoc macOS release needs no JIT entitlement; a future
  Developer ID or notarized build requires `com.apple.security.cs.allow-jit` and
  `com.apple.security.cs.allow-unsigned-executable-memory` and must rerun the compiler
  smoke test on both slices.
- Plugin-to-plugin services are host-brokered only. Providers declare methods and
  events; consumers declare injects; calls, streams, and events flow through the host
  under `services.invoke` and `services.provide`, with no direct plugin-to-plugin
  channel. Streaming, events, dynamic registration, optional injection, multiple
  providers, and JSON or binary payloads are all part of the v1 contract.
