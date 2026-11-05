# Network Provider sits below Proxy and the jump chain

A Network Profile is applied by one in-process Network Provider that supplies the route's **first TCP dial only**. A route's First endpoint is the first jump when a jump chain exists, otherwise the target Host, and the effective Network Profile follows the same first-endpoint rule as the Proxy Profile: the first hop's profile wins, otherwise the target's. Later hops keep using the previous SSH hop's `direct-tcpip` channel and never consult a Provider.

The Provider sits **below** Proxy: an HTTP CONNECT or SOCKS5 proxy server address is dialed through the same Provider, then the existing proxy handshake runs unchanged. This is what makes "VPN to reach the proxy, proxy to reach the target" compose. A ProxyCommand spawns a local operating-system process, which an in-process userspace network cannot carry, so ProxyCommand combined with a Network Profile is rejected with a typed `KindUnsupported` error that names both selections before any process starts.

Providers are native and in-process: Tailscale Net uses `tsnet` and WireGuard Net uses `wireguard-go`'s `tun/netstack`. There is no sidecar, daemon, system service, TUN device, route-table change, or administrator privilege. WireGuard's DNS goes through the netstack and the OS resolver is not used as a fallback.

Failures are fail-closed. Whenever a Network Route is present, a missing registry or factory, an unknown or empty Kind, a dangling profile reference, a failed startup, or a dial failure ends the connection attempt as a typed error carrying `ProviderInfo{ProfileID, Label, Kind, Phase}`. The runtime never retries, downgrades, or falls back to a direct connection, and it never turns a provider failure into an ordinary network error to keep going. An unreachable target still reports `KindNetwork`; an expired deadline still reports `KindTimeout`; login, configuration, and tunnel startup failures report `KindProvider`. Secrets never enter the route, request, dial request, pool key, log line, or error text.

**Status**: accepted

**Considered Options**:
- A global VPN switch or system-level Tailscale/WireGuard integration: needs installation, privileges, and route management that managed environments often forbid, and it cannot scope one network to one Host.
- A SOCKS sidecar per Network Profile: adds a process and port to install and supervise, and still cannot cover transports that do not speak SOCKS.
- Provider above Proxy (dial the target, then hand the socket to the proxy): impossible for CONNECT-style proxies, which must reach the proxy server themselves.
- Provider as an SSH-level ProxyCommand replacement: a spawned process uses the OS network, so the embedded netstack could never see its traffic.
- One in-process Provider below Proxy and the jump chain, first endpoint only (chosen).

**Consequences**:
- Editing a Network Profile, rotating its Auth Key, Private Key, or Preshared Key, deleting it, or stopping its Provider invalidates idle pooled Connections for that profile. Active leases keep their transport and observe the failure on the next I/O instead of being silently re-dialed through another path.
- The pool key includes a profile ID and a non-secret configuration fingerprint. The display label is excluded from the key itself; any edit, including a rename, still bumps the profile version in the fingerprint and therefore intentionally invalidates idle transports.
- Provider lifecycle is lazy and per profile: `Acquire` starts a Provider on first use, a failed start is retryable, and application shutdown closes every Provider through `Manager.CloseAll`. One profile has exactly one Provider instance, and concurrent Acquires share one open attempt.
- The dial port accepts only `tcp`, `tcp4`, and `tcp6` and deliberately has no `Listen`; the application never accepts inbound connections through a Network Profile.
- mosh UDP support is deferred until the in-process mosh rewrite lands. `adapters/mosh/` and `internal/mosh/` are untouched, and the follow-up adds an optional datagram capability beside the port rather than widening it. Until then, ticket 09 owns the UI requirement that a mosh Session cannot use an embedded Network Profile and must direct the user to an OS-level VPN.
- The network layer is intentionally not a general routing engine: no TUN interface, OS routes, kill switch, global proxy, or Tailscale exit-node/subnet-router behavior.
