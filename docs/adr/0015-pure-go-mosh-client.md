# Replace the bundled mosh client with a pure-Go implementation

Desktop mosh moves from the bundled MoshCatty binary to an application-owned, pure-Go client. `adapters/mosh` implements the session transport; `internal/mosh` owns the protocol core (OCB3, datagram framing, fragmentation, TransportInstruction, SSP, prediction/local echo, and the bounded `Complete`/`Display` layer). MyGo's desktop terminal remains the renderer through `terminal.Options.Conn`, with no local PTY and no cgo. The client must match stock `mosh-client` behavior against stock `mosh-server`; `mosh-go` is the protocol reference and MoshCatty plus stock mosh are behavioral oracles.

**Status**: accepted

**Considered Options**:
- Keep the bundled MoshCatty binary: preserves parity by construction but defeats the goal of removing bundled clients.
- Run MoshCatty in-process through cgo/FFI: closest behavior, but adds a Rust/static-library toolchain and complicates Windows arm64 cross-compilation.
- Implement only from the protocol specification: avoids GPL-derived code, but is unnecessary under the GPL host app and costs the most.
- Pure Go using `mosh-go` as reference with MoshCatty/stock as oracles (chosen).

**Consequences**:
- Full parity is the release gate: real UDP interop against `mosh-server` 1.4.0 and 1.3.2 on Debian/Ubuntu LTS, Fedora, macOS Homebrew, and FreeBSD, plus byte-level Display and terminal golden tests. No waivers.
- OCB3 is implemented in-repo from RFC 7253 and validated with official vectors; the `MOSH CONNECT` key stays in memory, is never logged or persisted, and is zeroized on close.
- MyGo must be upgraded to the first release containing `Options.ResizeByConn`. If no such release exists, a documented client-side resize-ordering fallback with a `TODO` is a development stopgap, not a release-qualified path.
- Windows arm64 becomes supported because no bundled binary is required.
- Record provenance for any MoshCatty-derived code and ship its notices with the app.
- Supersedes ADR-0005.
