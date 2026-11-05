# Network Provider release binary size

Measured on 2026-11-06 for `windows/amd64`. The numbers below come from
release-style MyGo builds of this tree, not from the minimal dependency probes
used during research. The research probe estimate (about +29–31 MiB for tsnet
and +8–9 MiB for the WireGuard netstack) is not a promise; the measured values
here are what the release artifact actually adds.

## Result

| Build | `Natcatty.exe` bytes | MiB | Delta vs. neither (bytes) | Delta (MiB) |
| --- | ---: | ---: | ---: | ---: |
| neither provider | 37,132,288 | 35.41 | — | — |
| Tailscale Net only | 55,712,768 | 53.13 | 18,580,480 | 17.72 |
| WireGuard Net only | 41,753,088 | 39.82 | 4,620,800 | 4.41 |
| both providers | 56,254,464 | 53.65 | 19,122,176 | 18.24 |

The deltas are not additive. Enabling both costs less than the sum of the two
single-provider deltas (17.72 + 4.41 = 22.13 MiB) because tsnet and the
WireGuard netstack share dependencies that the "neither" build does not link.

The "neither" baseline is not a build without Network Profiles: it keeps the
Vault model, editors, route selection, status plumbing, and the Network
Provider port. Each delta answers "what does enabling this provider add?" and
isolates that provider's implementation and dependency tree.

The Windows NSIS installer compresses the app, so its deltas are smaller and
vary slightly between builds. Observed values from the same four trees:

| Build | `Natcatty Setup 0.1.0 amd64.exe` bytes | MiB | Delta vs. neither (bytes) | Delta (MiB) |
| --- | ---: | ---: | ---: | ---: |
| neither provider | 15,711,835 | 14.98 | — | — |
| Tailscale Net only | 20,461,159 | 19.51 | 4,749,324 | 4.53 |
| WireGuard Net only | 16,849,999 | 16.07 | 1,138,164 | 1.09 |
| both providers | 20,596,700 | 19.64 | 4,884,865 | 4.66 |

Only the executable size is treated as a stable measurement: two builds of the
same tree in different directories produced the same `Natcatty.exe`
(56,254,464 bytes, SHA-256
`11cffbb1d87def9edb5de082f15846bb239dba20516c9676a2a14451888b0380`) but
installer sizes that differed by a few KiB. NSIS packaging is not
byte-reproducible here.

## Method

All builds used the release toolchain and flags:

```sh
GOTOOLCHAIN=go1.27.3 GOPROXY=off CGO_ENABLED=0 \
  go tool mygo build -platform windows/amd64 -o <out>
```

MyGo v0.6.0 compiles the app with `-trimpath` and
`-ldflags "-s -w ... -H=windowsgui"` and copies the same `resources/` tree into
every variant, so the only difference between artifacts is the Network
Provider code.

Each variant was built from a temporary copy of the working tree outside the
repository (`.git` and `.claude` excluded). The unwanted provider was removed
at the source level, not disabled at run time:

- the adapter import was deleted, so the linker could drop the adapter and
  every package reachable only from it;
- the factory `switch` case, route loader, production opener, status sink
  method, redaction helper, and Connection Log mapper were deleted;
- the composition-root observer/factory wiring for that provider was deleted;
- for the "neither" build, the Tailscale state-directory cleaner became a
  no-op and the imports used only by the removed paths were deleted.

`go build ./...` succeeds in every variant. `go list -deps .` confirms the
dead-code elimination:

| Variant | `tailscale.com/tsnet` | `golang.zx2c4.com/wireguard` | `github.com/tailscale/wireguard-go` | Total deps |
| --- | ---: | ---: | ---: | ---: |
| both | 1 | 12 | 11 | 872 |
| Tailscale Net only | 1 | 1 | 11 | 859 |
| WireGuard Net only | 0 | 11 | 0 | 562 |
| neither | 0 | 0 | 0 | 495 |

The `github.com/tailscale/wireguard-go` packages in the Tailscale-only build
are tsnet's own fork, not this application's WireGuard Net adapter. The
`golang.zx2c4.com/wireguard` packages disappear entirely from the "neither"
build, and `tailscale.com/tsnet` disappears from the WireGuard-only build.

## Scope

- This page measures `windows/amd64`, the platform where the release workflow
  uploads a single executable plus NSIS Setup.
- Linux and macOS release artifacts are produced by MyGo with the same compile
  flags, but their exact increments were not measured in this environment.
  Expect the same order of magnitude; do not quote these bytes as macOS or
  Linux values.
- The readings are a snapshot of this tree at the final Network Provider
  ticket. Dependency updates can change them; re-measure with the method above
  rather than treating them as permanent.
