# A Host protocol is the endpoint; Mosh and ET are SSH transports

A Host selects one of three protocols: SSH (`ssh`, port 22), Telnet (`telnet`,
port 23), or VNC (`vnc`, port 5900). The protocol answers what the endpoint
speaks, which default port applies, and which credential bundle authenticates
it. A blank persisted protocol is the SSH default. A Local shell is not a Host
protocol: it has no endpoint, so it remains a Session the user opens from New
Tab without a Vault record.

Mosh and EternalTerminal (ET) are not protocols. They are two ways an SSH Host
carries its interactive Session, selected by a new `Host.Transport` field:
the empty value is plain SSH, `mosh` and `et` bootstrap their own Session from
an SSH login. The pair is persisted as `protocol: "ssh"` plus `transport:
"mosh"` or `transport: "et"`, and a non-SSH protocol never keeps a transport.
The field is omitted from JSON for plain SSH, so the common Host is unchanged.

The capability matrix follows the pair, not the protocol name alone. Mosh and
ET keep the SSH credential bundle, jump chains, proxies, Network Profiles,
forwarding, and monitoring. They have no file transfer (SFTP/SCP) and no X11
forwarding, and the plain-SSH-only controls — network-device mode, automatic
tmux attach, and the input/encoding preferences tied to a login shell — do not
apply. Telnet and VNC keep their existing capabilities. The registry in
`domain/model/protocol` exposes the protocol descriptors and a transport
descriptor table; the Vault, editors, connectors, and Session factory resolve
capabilities from those descriptors instead of comparing names.

The New Host editor presents Protocol first, then a Transport selector only
when Protocol is SSH. Quick Connect offers the same three protocols and the
same SSH transport choice, with a one-shot VNC password that is never saved.
The Session kinds stay `local`, `ssh`, `mosh`, `et`, `telnet`, `serial`, and
`vnc`; the transport field selects among them without changing the Workspace
record shape.

**Status**: accepted (2026-11-23). Supersedes the statements in ADR 0023 and
ADR 0024 that Mosh, ET, and Local are Host protocols.

**Considered Options**:
- **Keep Mosh and ET as protocols.** Rejected: they require an SSH handshake
  and the SSH credentials before their own Session starts, so treating them as
  peer protocols duplicated the SSH checks and made every new SSH feature
  branch three ways.
- **Keep Local as a Host protocol.** Rejected: a Local shell has no address,
  port, credentials, route, or remote endpoint. Storing it as a Host made the
  Vault and the connection forms model something that is not a connection.
- **Migrate or alias the removed values.** Rejected by product decision. The
  old `protocol: "local" | "mosh" | "et"` values and spellings such as `ssh2`
  or `eternalterminal` are unsupported: they are not rewritten, aliased, or
  inferred. A record that still carries one is preserved for validation and is
  rejected on save, import, and sync, rather than silently becoming an SSH
  Session the user never configured.
- **A separate top-level transport record.** Rejected: the transport is
  meaningless without an SSH endpoint, so it belongs on the Host and is cleared
  whenever the protocol is not SSH.

**Consequences**:
- Portable JSON and CSV export new records as `protocol: "ssh"` plus their
  transport, and import rejects the removed protocol values and unknown
  transports.
- Sync carries `transport` as an ordinary Host field; a payload without it is
  plain SSH.
- Host Groups may use SSH, Telnet, or VNC as a default protocol. A transport is
  always a per-Host choice because Mosh server paths and ET ports differ.
- The Vault list, avatars, and summaries show Mosh and ET as SSH Hosts with a
  transport, not as separate protocols.
- Local shells continue to work from New Tab, with their own Session kind,
  history scope, and workspace descriptor.
