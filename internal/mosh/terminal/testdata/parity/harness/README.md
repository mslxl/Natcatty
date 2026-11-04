# mosh 1.4.0 terminal parity harness

This directory makes the stock mosh 1.4.0 terminal model reproducible. The
Dockerfile downloads the pinned upstream release, verifies its SHA-256, builds
stock `mosh-client`/`mosh-server`, and compiles `driver.cc` against the stock
`terminal/` sources.

| Item | Value |
| --- | --- |
| Source | https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz |
| SHA-256 | `872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd` |
| Base image reference | `ubuntu:24.04` |
| Base image digest observed for this verification | `sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852` |
| Docker Engine observed for this verification | 29.8.2 |
| Image tag used by tests | `natcatty/mosh-parity:1.4.0` |
| Image ID observed for this verification | `sha256:50d77c735cf5fd76eac410bdfb565d852a81bc6065c12977ad17ee416cb6d59d` |
| Driver | `/usr/local/bin/mosh-terminal-ref` |
| Stock clients | `/usr/local/bin/mosh-client`, `/usr/local/bin/mosh-server` |

The mosh release tarball is pinned by URL and SHA-256. Ubuntu archive packages
installed by the Dockerfile are not version-pinned, so a later rebuild may
produce a different image ID; regenerate the goldens only after confirming the
same upstream source hash and reviewing the diff. Build from the repository
root:

```sh
docker build -t natcatty/mosh-parity:1.4.0 \
  internal/mosh/terminal/testdata/parity/harness
```

Run the live differential against the driver and the committed corpus:

```sh
NATCATTY_MOSH_PARITY=1 \
NATCATTY_MOSH_PARITY_IMAGE=natcatty/mosh-parity:1.4.0 \
  go test ./internal/mosh/terminal -run 'TestStockParity' -count=1
```

Run the end-to-end stock-client differential. It starts a scripted SSP server,
runs stock `mosh-client` in this image with a real PTY and
`MOSH_NO_TERM_INIT=1`, then feeds the same host states to a real
`application/session.Session` backed by the in-process transport. The two byte
streams must match, including the final title-clearing frame and
`Display::close`. The client requires a numeric peer address because mosh
1.4.0 passes `AI_NUMERICHOST` to `getaddrinfo`, so the test resolves Docker's
host-gateway address and passes it to `mosh-client`; `MOSH_KEY` is supplied in
the container environment (test-only random key) and the PTY is sized with
`stty` because a detached `docker run -t` starts at 0x0.

```sh
NATCATTY_MOSH_PARITY=1 \
NATCATTY_MOSH_PARITY_IMAGE=natcatty/mosh-parity:1.4.0 \
  go test ./adapters/mosh -run TestSessionMatchesStockMoshClient -count=1 -v
```

The stock-client script excludes corpus events that query the terminal
(`DA`, `DSR`). Stock `mosh-server` never sends those; stock
`Complete::apply_string` asserts that parsing a host state produces no
terminal-to-host reply. The internal driver comparison still covers the model
behavior for those sequences.

Stock also draws a wall-clock status bar ("Nothing received from server on UDP
port …") if the container's first UDP exchange is scheduled more than 250 ms
after the client starts. That frame depends on container scheduling, not on the
scripted host states, so the differential retries a run that contains it and
compares only runs without the startup status frame. The notification engine's
formatting, expiry, and count-up behavior remain covered by the terminal-model
tests; the stock-client differential is the scripted terminal stream oracle.

Regenerate the committed goldens after changing a scenario:

```sh
NATCATTY_MOSH_PARITY=1 NATCATTY_MOSH_PARITY_UPDATE=1 \
NATCATTY_MOSH_PARITY_IMAGE=natcatty/mosh-parity:1.4.0 \
  go test ./internal/mosh/terminal -run TestStockParityTerminalModel -count=1
```

The committed corpus under `../goldens` is then exercised by normal
`go test` without Docker. Each file records the exact stock
`Display::new_frame` bytes and a deterministic terminal-state dump for the
initial frame and every event, including wide characters, combining marks,
unknown SGR, resize/reflow, scrollback, tabs/insert/delete, bracketed paste,
mouse modes, cursor visibility, application cursor keys, and the alternate
screen sequences.

The oracle constructs `Display(false)`, matching stock mosh-client with
`MOSH_NO_TERM_INIT=1` (ECH/BCE/title enabled, no smcup/rmcup). The desktop
transport deliberately uses the same capabilities because MyGo is the
renderer; `XtermDisplayOptions` covers the alternate-screen enter/leave bytes
for a stand-alone terminal.
