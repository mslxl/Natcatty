# Bundled EternalTerminal client

Netcatty runs EternalTerminal Sessions with a bundled `et` client, never with a
binary found on `PATH`. `et` performs its own SSH bootstrap and ET handshake;
Netcatty gives it a private HOME, a generated `ssh_config`, key material, and an
`SSH_ASKPASS` helper.

## Source

- Release repository: <https://github.com/binaricat/Netcatty-et-bin>
- Release: `et-bin-6.2.10-1`
- Upstream: <https://github.com/MisterTea/EternalTerminal>
- Upstream ref: `et-v6.2.10`
- Upstream commit: `f9a584ac06b2f1730b5bdf0a27150f28478368fb`
- License: Apache-2.0 (see `LICENSE-EternalTerminal-Apache-2.0.txt`)
- Build provenance: `BUILD-PROVENANCE.json` (manylinux2014, macOS universal,
  Windows x64, telemetry disabled)

## Layout and checksums

| Target | File | SHA-256 |
| --- | --- | --- |
| win32-x64 | `win32-x64/et.exe` | `6212e2c089cd55aa9455dc2a9de22ac83d0bc6c8af76ce9ad096668931ee5a25` |
| linux-x64 | `linux-x64/et` | `2f1b3a4fdda433246783b46669d8099707808a27b74f661acf80af24ec4d7d6d` |
| linux-arm64 | `linux-arm64/et` | `408fe4831b80a0ed89a7cbdcabffccb874c7d1d95b3b16ead09717255e035ea4` |
| darwin-universal | `darwin-universal/et` | `64c46d7f4f84724eb7fa0bfae7321e20896568fb771de418698e61bf9749d983` |

Release archive digests:

| Archive | SHA-256 |
| --- | --- |
| `et-win32-x64.tar.gz` | `d8fb0056a08fee0551d8526d4b2cb5674b4f757540147de20f00ecc944064cab` |
| `et-linux-x64.tar.gz` | `25149ca428107822dc374fe186e1dd49098ab8863b899a49f03048ef9dc94935` |
| `et-linux-arm64.tar.gz` | `c0f0fbe18bdd95c409bba11180a98f36876aa03f063ef1fa27048d86471b58ea` |
| `et-darwin-universal.tar.gz` | `d26855cc2272fe3f214d84f482850febe94461703111838c405e968f3eaa0128` |

The Windows x64 binary is committed so local development and acceptance use the
same bytes as a release. Other targets are fetched at packaging time. The app
recomputes the binary's SHA-256 and size before **every** launch; the digests
are compiled into `adapters/bundle/manifests.go`.

## Fetching

```sh
go run ./internal/tools/fetchtransportbins -kind et -target all -out resources
```

The tool downloads the pinned release archive, verifies the archive digest,
extracts only the named client binary (rejecting absolute or parent-relative
paths), verifies the binary digest and size, and writes it under
`resources/et/<target>/`.

## Rebuilding from source

The reported artifacts come from the Netcatty `build-et-binaries` workflow:
Linux in manylinux2014 containers, macOS as a universal binary, Windows x64,
telemetry and Sentry disabled (upstream PR 1212, workflow run 26945446872).
Rebuild by checking out `MisterTea/EternalTerminal` at `et-v6.2.10` and applying
that build configuration; Apache-2.0 requires preserving the license and
notices, which ship next to the binary.

## Restrictions

The original supports at most one jump host and no proxy because `et` exposes a
single `--jumphost` and cannot forward a proxy to its ET socket. Netcatty
returns typed errors for both before starting a process. A second factor or a
host-key confirmation cannot be answered through `SSH_ASKPASS`; the helper
answers only saved passwords and key passphrases, and host keys use
`StrictHostKeyChecking=accept-new` against the Vault snapshot.
