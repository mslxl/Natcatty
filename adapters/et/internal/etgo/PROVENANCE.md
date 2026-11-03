# Vendored et-go protocol core

This directory contains Go code vendored from
[`tphakala/et-go`](https://github.com/tphakala/et-go), a client for the Eternal
Terminal protocol.

- Upstream commit: `bc33dd76b558418bc1079ba27c8c477b8145eea5`
  (`ci: bump golangci-lint to v2.15.0`)
- Upstream license: Apache License 2.0. The upstream `LICENSE` and `NOTICE`
  are copied into this directory unchanged.
- Upstream copyright: Copyright 2026 Tomi P. Hakala.
- The protocol buffer definitions under `protocol/proto` are copied from
  [Eternal Terminal](https://github.com/MisterTea/EternalTerminal), Copyright
  Jason Gauci and contributors, Apache License 2.0.

## Copied packages

| Directory | Upstream path | Purpose |
| --- | --- | --- |
| `protocol/` | `internal/protocol` | ET protobuf messages and packet headers |
| `wire/` | `internal/wire` | length framing, packet layout, handshake messages |
| `seal/` | `internal/seal` | libsodium-compatible secretbox streams |
| `etcp/` | `internal/etcp` | protocol-6 handshake, encrypted packet connection |
| `session/` | `internal/session` | INITIAL_PAYLOAD, packet routing, terminal service |
| `console/` | `internal/console/console.go` | `Size` only; no platform console code |
| `etservertest/` | `internal/etservertest` | in-process fake etserver used by tests |
| `bootstrap/` | `internal/bootstrap` | placeholder, IDPASSKEY parser, remote command |

## Modifications from upstream

- Go import paths rewritten from `github.com/tphakala/et-go/internal/...` to
  `github.com/mslxl/natcatty/adapters/et/internal/etgo/...`. The directory is
  under `adapters/et` so the adapter layer may import it and no other layer
  can; the repository's architecture test forbids adapters importing
  top-level `internal/` packages.
- `// SPDX-License-Identifier: Apache-2.0` headers added.
- `console/` keeps only the portable `Size` type and `resizeCheck` helper; the
  platform console implementations are not copied.
- `bootstrap/` excludes the subprocess SSH runner (`bootstrap.go`,
  `command.go`'s `sshArgs`, signal helpers) because the application runs
  `etterminal` over `connection.Manager`. `export.go` adds exported wrappers
  for the unchanged parser and placeholder functions, and `command.go` is
  reduced to the remote command and its validation.
- Generated protobuf files keep upstream's generated code. Each has an SPDX
  license line and a copied-from Eternal Terminal provenance header above the
  generated header; the generated messages and methods are unchanged.

The application adapter that consumes these packages lives in `adapters/et`.
