# Replace the bundled et client with a pure-Go implementation

Desktop ET moves from the bundled `et` binary to an application-owned, pure-Go client forked from the Apache-2.0 `tphakala/et-go` protocol core, extended with ET 7.1 challenge auth, reset/`--attach`, `RETRY_LATER`, and exit status while retaining the protocol-6 handshake used by 6.2.10 servers. The client uses `connection.Manager` leases: `OpenExec` runs the `etterminal` bootstrap, and `Forwarding().DialContext` opens a `direct-tcpip` channel to `etserver`. It implements the session transport and feeds MyGo's terminal through `terminal.Options.Conn`; no cgo, no plugin, no subprocess.

**Status**: accepted

**Considered Options**:
- Run the upstream C++ client in-process: upstream cannot run as-is (it forks `ssh`, needs a PTY, and ships only static C++ libraries); a wrapper is estimated at 12–24 engineer-weeks with high risk.
- Dynamic library plus purego or a MyGo plugin: still needs a stable ABI and a mobile/native build pipeline that upstream does not provide.
- Pure Go forked from `et-go` (chosen).
- Keep the bundled `et` binary: preserves current behavior but defeats the removal goal.

**Consequences**:
- ET Hosts gain the same SSH route capabilities as SSH Hosts: proxy and jump chains, shared credentials, timeouts, keepalives, and host-key verification. The current one-jump/no-proxy restriction, private HOME, `ssh_config`, `known_hosts`, and askpass helper are removed.
- Full stock `et` 7.1.1 parity is the release gate against etserver 7.1.1 and 6.2.10 on Debian/Ubuntu LTS, Fedora, Arch, and macOS, with real SSH/TCP tests. No waivers.
- ET port forwarding is out of scope for v1; the transport interface must leave room for it.
- The vendored `et-go` code needs its Apache-2.0 LICENSE/NOTICE and attribution in the app's third-party notices.
- Mobile remains deferred; the pure-Go client is portable when the mobile gate reopens.
- Supersedes ADR-0005.
