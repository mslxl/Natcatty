# Network Providers

A **Network Profile** lets a Host or Host Group reach a virtual network from
inside Natcatty. A **Network Provider** is the in-process dialing port that
makes it work:

- **Tailscale Net** joins a tailnet with an embedded `tsnet` node.
- **WireGuard Net** dials through a userspace `wireguard-go` netstack device.

Both run inside the application process. Neither needs a system VPN client, a
system service, a sidecar, a TUN device, a route-table entry, or administrator
rights. Nothing starts until a Host that references a profile first dials.

The placement decision and its trade-offs are in
[ADR-0018](adr/0018-network-provider-below-proxy.md); the vocabulary is in
[GLOSSARY.md](../GLOSSARY.md).

## Where a Provider applies

A Provider supplies the **first TCP dial** of a route only:

- With a jump chain, the first jump is dialed through the Provider and later
  hops use the previous SSH hop's `direct-tcpip` channel.
- Without a jump chain, the target Host is dialed through the Provider.
- The Host's own Network Profile wins over its Host Group's; with a jump chain
  the first hop's effective profile wins, otherwise the target Host's.

A Provider sits **below** Proxy Profiles. An HTTP CONNECT or SOCKS5 proxy server
address is dialed through the same Provider, then the existing handshake runs,
so "VPN to reach the proxy, proxy to reach the target" composes.

**ProxyCommand cannot be combined with a Network Profile.** ProxyCommand spawns
a local operating-system process whose traffic cannot enter the in-process
netstack. The combination fails before the process starts with a localized
`KindUnsupported` error that names both selections. Remove one of them.

The port accepts `tcp`, `tcp4`, and `tcp6` only and offers no inbound `Listen`.
Failures are fail-closed: when a route has a Network Profile, a missing
Provider, a failed start, a login requirement, or a dial failure ends the
connection with a typed error. The application never falls back to a direct
connection and never downgrades a Provider failure into an ordinary network
error.

## Transport support

| Transport | Network Profile | Notes |
| --- | --- | --- |
| SSH | Yes | Provider carries the first dial; later hops stay in SSH. |
| telnet | Yes | Provider wraps the target dial. |
| ET, in-process client | Yes | The route is handed to the Connection Manager. |
| mosh | No | Its UDP channel is not wired to a Provider yet; the UI directs the user to an OS-level VPN. |

## Tailscale Net

Supported: Auth Key login, interactive browser login with a copyable Auth URL,
node hostname, ephemeral nodes, tags, MagicDNS and tailnet peer targets,
multiple independent profiles per application, and a custom `ControlURL`.

Limits and boundaries:

- **No exit node, subnet router, or Tailscale SSH integration.** A tailnet
  node can be the target of a normal SSH Session, but the app does not offer
  exit-node selection, advertised subnet routes, or the Tailscale SSH server
  feature. Serve, Funnel, and Tailscale-managed OS routing are out of scope.
- **Headscale is experimental and unverified.** `ControlURL` is passed to
  `tsnet`, but no real Headscale control server was available to verify
  registration, MagicDNS short names or FQDNs, `Dial` behavior, status events,
  or failure modes. Do not assume Headscale works; the UI labels the field
  experimental.
- **Privacy:** tsnet uploads operational logs to `log.tailscale.com` by
  default. This build has no switch to disable that upload, and using a
  self-hosted control server does not stop it. The Network Profile editor shows
  this notice. `Logf`/`UserLogf` only control local diagnostics; they do not
  disable the upload. If that is unacceptable, do not use Tailscale Net.
- The node's state directory lives under the application data directory,
  isolated per profile with mode 0700. It is credential material: it is not
  cloud-synced, not exported, and removed when the profile is deleted after the
  user confirms. It never points at a system Tailscale state directory.

## WireGuard Net

Supported: wg-quick `.conf` import and in-app editing, multiple peers, multiple
local addresses, split tunnel through `AllowedIPs`, tunnel DNS, IPv4 and IPv6,
persistent keepalive, endpoint host names resolved at start and re-resolved as
their cached resolution expires, and multiple independent profiles.

Limits and boundaries:

- **No OS routes, TUN interface, kill switch, or firewall rules.** Routing
  happens inside the userspace netstack through cryptokey routing. `FwMark` and
  `Table` have no meaning and are never applied. The app does not provide a
  kill switch or global proxy mode.
- **DNS only through the tunnel.** Only the profile's configured DNS servers
  are used. There is no operating-system resolver fallback. A profile with no
  DNS server can reach IP-literal targets only; a host name then fails with a
  typed Provider error, and the editor warns about it.
- Importing a `.conf` supports `PrivateKey`, `ListenPort`, `PublicKey`,
  `PresharedKey`, `AllowedIPs`, `Endpoint`, `PersistentKeepalive`, `Address`,
  DNS IPs, and `MTU`. `Table`, `FwMark`, `SaveConfig`, and DNS search domains
  are parsed but ignored with a warning. Non-empty `PreUp`/`PostUp`/
  `PreDown`/`PostDown`, unknown keys or sections, and malformed keys, CIDRs,
  ports, or MTUs are rejected before saving.
- Endpoint host-name resolution happens on the physical network before the
  tunnel is up. That is bootstrap resolution, not target DNS; target names are
  resolved only by the tunnel's DNS servers.

## mosh

mosh is currently unsupported with a Network Profile because its UDP channel
has not been integrated. A mosh Session on a Host whose route resolves a
Network Profile fails closed before the SSH bootstrap or `mosh-server` starts,
and the localized error tells the user to use an OS-level VPN. The
`NetworkProvider` port deliberately has no datagram capability yet; UDP
integration is a separate follow-up ticket.

## Validation status

The default test suite is hermetic. It uses fake Providers, the `.conf` parser,
and in-process WireGuard loopback devices; it never contacts a tailnet or a
real WireGuard peer.

| Area | Status | Evidence |
| --- | --- | --- |
| Provider port, routing, pool invalidation, lifecycle | Verified | Default `go test ./...` on Linux, macOS, and Windows CI. |
| Tailscale state machine (key login, interactive login, cancel, retry, close) | Verified with a fake backend | `adapters/tailscale` default tests. |
| WireGuard `.conf` parser | Verified | `domain/wireguardconf` table-driven tests. |
| WireGuard netstack data path | Verified with two in-process devices | `adapters/wireguard.TestLoopbackPeersHandshakeAndCarryTCPThroughTheTunnel`, `TestLoopbackProviderCloseStopsTheTunnel`. |
| Windows real-machine Tailscale Net | **Unverified here** | No tailnet credentials or reachable peer were available. Run the opt-in test below on Windows. |
| Windows real-machine WireGuard Net | **Unverified here** | No WireGuard peer was available. Run the opt-in test below on Windows. |
| macOS runtime | **Unverified** | macOS guarantees the builds in this release; the default CI checks also run the hermetic tests on macOS, but no real-network check is performed. |
| Headscale + tsnet | **Unverified / experimental** | No Headscale instance was available; no registration, DNS, dial, or error-mode matrix was executed. |

Linux coverage is the hermetic fake-Provider, parser, and netstack loopback
tests plus the CI test job. macOS is at least built for both architectures; the
runtime items above stay recorded as unverified rather than claimed.

### Opt-in real-network tests

These tests skip with an explicit message when their credentials are absent.
Default CI runs them with no credentials and requires the skip.

Tailscale Net, against a real tailnet peer that accepts SSH:

```sh
go test ./adapters/tailscale -run TestRealTailnetSSH -count=1 -v
```

| Variable | Required | Meaning |
| --- | --- | --- |
| `NETCATTY_TAILSCALE_AUTH_KEY` | Yes | Reusable or ephemeral auth key for the embedded node. |
| `NETCATTY_TAILSCALE_HOST` | Yes | Peer host name or tailnet IP reachable from the node. |
| `NETCATTY_TAILSCALE_SSH_USER` | Yes | SSH user on that peer. |
| `NETCATTY_TAILSCALE_SSH_PASSWORD` or `NETCATTY_TAILSCALE_SSH_KEY_FILE` | One of them | SSH credential. |
| `NETCATTY_TAILSCALE_SSH_PORT` | No | SSH port, default 22. |
| `NETCATTY_TAILSCALE_CONTROL_URL` | No | Custom control server (Headscale). Experimental. |
| `NETCATTY_TAILSCALE_HOSTNAME` | No | Requested node name. |

WireGuard Net, against a real WireGuard peer:

```sh
go test ./adapters/wireguard -run TestRealPeerTCP -count=1 -v
```

| Variable | Required | Meaning |
| --- | --- | --- |
| `NATCATTY_WIREGUARD_E2E` | Yes | Must be `1`. |
| `NATCATTY_WIREGUARD_E2E_PRIVATE_KEY` | Yes | Local private key, base64 or hex. |
| `NATCATTY_WIREGUARD_E2E_PEER_PUBLIC_KEY` | Yes | Peer public key. |
| `NATCATTY_WIREGUARD_E2E_PEER_ENDPOINT` | Yes | Peer `host:port` reachable on the physical network. |
| `NATCATTY_WIREGUARD_E2E_ALLOWED_IPS` | Yes | Comma-separated prefixes routed to the peer. |
| `NATCATTY_WIREGUARD_E2E_TARGET` | Yes | Target `host:port` to dial through the tunnel. |
| `NATCATTY_WIREGUARD_E2E_ADDRESSES` | No | Local addresses, default `10.0.0.2/32`. |
| `NATCATTY_WIREGUARD_E2E_DNS` | No | Comma-separated tunnel DNS servers. |

Run these on a Windows machine with a real tailnet and a real WireGuard peer to
close the two "unverified here" rows. A pass from both tests, plus a Host
connection through the desktop app, is the Windows real-machine evidence. Do
not record a real-network pass from a fake, a loopback pair, or a skipped test.

## Release size

The measured release-artifact increment on `windows/amd64` is +17.72 MiB for
Tailscale Net only, +4.41 MiB for WireGuard Net only, and +18.24 MiB for both,
relative to a build with neither provider. The exact builds, commands, and
installer figures are in
[releases/network-provider-binary-size.md](releases/network-provider-binary-size.md).
These are measured values, not the research-probe estimates.

## CI coverage

`.github/workflows/ci.yml` runs its checks on `ubuntu-latest`, `macos-latest`,
and `windows-latest` with `CGO_ENABLED=0`: each checks job formats, builds,
vets, and tests, and a cross-compile step builds `windows/amd64`,
`linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`. The race and
launch-smoke jobs add the race detector and a real window launch. A dedicated
step asserts that the real-network variables are unset and that the opt-in
tests report `SKIP`, so default CI neither needs nor accidentally uses real
credentials.
`release.yml` builds the macOS universal app, the Windows amd64 installer, and
the Linux amd64/arm64 packages with the same `CGO_ENABLED=0` constraint.
