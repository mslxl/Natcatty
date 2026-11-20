# Telnet is a first-class Host protocol

A Host may select Telnet (`telnet`, port 23) like SSH, Mosh, EternalTerminal, or
a Local shell. Telnet has its own Session transport (`adapters/telnet`), no SSH
handshake or credential bundle, and no SSH application channels. Built-in
protocol facts live in one descriptor registry, `domain/model/protocol`; the
Vault, the transports, the connectors, and the UI resolve capabilities there
instead of repeating protocol switches.

A Telnet Host keeps the route controls SSH Hosts have. Jump chains, proxies, and
algorithm overrides describe how to reach the endpoint, and SSH forwarding is a
TCP mechanism: every jump authenticates and verifies its host key as it would
for an SSH Session, then the final `direct-tcpip` stream is handed to the Telnet
negotiator without a handshake on the target. `Manager.DialStream` therefore
returns a raw `net.Conn` for the routed case; a Host with only a Network Profile
still dials once through its Provider, and a Host with no route keeps the
adapter's original direct dial.

A false capability hides the control and fails the connector closed with a typed
error:

- **File transfer** is SSH-only. A Telnet editor hides the SFTP/SCP choice and
  sudo, and `sftpRemoteOpener` rejects the Host instead of running an SSH
  command it never opened.
- **Port forwarding** needs a channel-capable SSH transport. A Telnet Host is
  not offered as a rule target and `forwardingConnector.Connect` returns
  `ErrorUnsupported` (permanent, never retried).
- **Monitoring** samples by running commands over an SSH channel. The picker
  hides a Telnet Host and `monitorConnector.Open` returns
  `ReasonUnsupported`.
- **The Vault's Test action** performs an SSH handshake. It is hidden for
  Telnet and `testConnection` returns `KindUnsupported`.
- **X11 forwarding** and **automatic reconnect** do not apply. X11 is cleared
  when the protocol changes; a dropped Telnet Session stays closed until the
  user reconnects.
- **A Local shell** has no TCP endpoint at all, so its editor also clears the
  route fields (chain, proxy, Network Profile, algorithm overrides).

Telnet carries everything in plaintext, including the password. The Host editor
shows a localized warning, and the catalogs are the only place the wording
lives.

**Status**: accepted (2026-11-20)

**Considered Options**:
- **Treat Telnet as SSH on port 23.** Rejected: the target speaks no SSH, so
  every credential control and channel feature would be dead or misleading.
- **Keep the route fields but ignore them (or clear them).** Rejected: SSH jumps
  and a proxy can carry a Telnet TCP stream, so the data is usable. Clearing it
  would make an existing saved route silently disappear on save.
- **Add Telnet to the SSH Connection Manager as a pooled Connection.** Rejected:
  the pool models leases, channels, keepalive, and reuse of an authenticated
  SSH transport. Telnet has exactly one raw stream, so `DialStream` returns the
  stream and closes the jump chain with it; it is not pooled.
- **A plugin protocol registry.** Deferred: the unmerged registry branch also
  introduced plugin IDs. This change stays with built-in descriptors and leaves
  a plugin namespace as a separate design.
- **Support `telnet://` deep links and the command line.** Deferred: launch
  targets still open only saved SSH Hosts, so a Telnet URL is reported as an
  unsupported scheme rather than opening an ad-hoc Session.

**Consequences**:
- A Telnet Host may have a Host Chain and a Proxy Profile. Their authentication
  prompts, known-host checks, and algorithm policies run the SSH path; the
  Telnet target has no host key to trust.
- Host Groups may use Telnet as their default protocol; Mosh and ET remain
  per-Host because they need protocol-specific settings.
- A blank persisted protocol resolves as SSH, which is the Vault's save default,
  so records and in-memory fixtures without a protocol keep their old behavior.
- The capability matrix is covered by `domain/model/protocol` tests. Changing it
  is a product decision; tests fail if a capability flips silently.
