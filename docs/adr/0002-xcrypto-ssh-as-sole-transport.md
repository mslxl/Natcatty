# `x/crypto/ssh` as the sole SSH transport, with app-level orchestration

We use `golang.org/x/crypto/ssh` v0.58.0 as the only SSH wire implementation and put
pooling, leases, keepalive, reconnect, host-key trust, jump chains, and forwarding in
our own `application/connection` layer. The library provides primitives but no
orchestration, so this keeps behavior under our control and avoids a second SSH stack;
the cost is that compression and several legacy algorithms are unsupported and must
fail with typed errors rather than silently degrade.

**Status**: accepted

**Considered Options**:
- A third-party SSH library with more legacy support: less orchestration work, but a
  second dependency with its own security and maintenance risk.
- Shelling out to system OpenSSH for all connections: broad compatibility, but loses
  pooling, prompt control, and cross-platform consistency.

**Consequences**:
- FIDO/SK keys are delegated to an external OpenSSH agent (see ADR-0004).
- Users of zlib compression, `hmac-md5`, DH group 15/17, or CBC ciphers receive an
  explicit unsupported error.
