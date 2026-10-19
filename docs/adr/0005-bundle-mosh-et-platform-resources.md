# Bundle MoshCatty and EternalTerminal as signed platform resources

mosh and EternalTerminal ship as checksum-pinned, platform-specific binaries under
`resources/` and are executed through the app's PTY/process supervisor, with no fallback
to binaries on `PATH`. This reproduces the original app's behavior and keeps identical
versions across platforms; the cost is larger artifacts, per-platform build/signing
work, and GPL/Apache notice and source-offer obligations.

**Status**: accepted

**Considered Options**:
- Require system `mosh-client`/`et`: smaller artifacts, but different versions and
  missing binaries on user machines.
- Pure-Go clients: no third-party binaries, but mosh needs AES-128-OCB3 and SSP, and
  both are substantial greenfield projects.

**Consequences**:
- `windows/arm64` is unsupported for mosh/ET because upstream provides no assets.
- Linux arm64 glibc floor becomes 2.31 when bundled mosh is used.
- Release artifacts must include license texts and a GPL corresponding-source offer.
