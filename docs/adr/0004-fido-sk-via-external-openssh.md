# FIDO/SK keys delegate to external OpenSSH

Hardware-backed SSH keys are handled by delegating to the system OpenSSH agent and the
`sk-*` signature path, preserving SK flags and counters with a custom agent wrapper,
rather than reimplementing CTAP2/HID or binding libfido2. This matches the original
app's behavior and keeps `CGO_ENABLED=0`; the cost is a runtime dependency on OpenSSH
and documented platform limitations when it is absent.

**Status**: accepted

**Consequences**:
- Without OpenSSH/agent support, hardware-key authentication returns a typed
  `unsupported` error; there is no silent fallback.
- `x/crypto/ssh/agent` drops trailing SK metadata, so a wrapper is required.
- A pure-Go CTAP2/HID stack is explicitly deferred.
