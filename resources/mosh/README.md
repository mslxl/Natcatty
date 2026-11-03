# Bundled mosh-client

Netcatty runs Mosh Sessions with a bundled `mosh-client`, never with a binary
found on `PATH`. The SSH bootstrap uses the app's own SSH Connection Manager;
only the local UDP client is bundled.

## Source and provenance

- Component: `mosh-client` from MoshCatty `moshcatty-0.1.8`, a pure-Rust
  implementation that is wire-compatible with stock mosh
- Source repository: <https://github.com/binaricat/MoshCatty>
- Release: `moshcatty-0.1.8`, published 2026-07-17
- Corresponding source (tag commit
  `554b9d305e7ac4b11de740d764bbc3e05f816d7b`):
  <https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8>
- Release workflow run:
  <https://github.com/binaricat/MoshCatty/actions/runs/29559896639>
- License: GPL-3.0-or-later (see `LICENSE-MoshCatty-GPL-3.0.txt`)
- Authors declared in `Cargo.toml`: `binaricat`, `Netcatty contributors`
- Protocol and behavioral reference: upstream
  <https://github.com/mobile-shell/mosh>. It is **not** the source of this
  binary; MoshCatty's release workflow does not check out or build it.
- MIT-derived material: MoshCatty contains adapted MIT-licensed material from
  <https://github.com/unixshells/mosh-go> at commit
  `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f`. The MIT notice is shipped as
  `LICENSE-MoshGo-MIT.txt` and reproduced in `../THIRD-PARTY-NOTICES.md`.
- Rust dependency notices: 49 third-party crates are pinned by MoshCatty's
  `Cargo.lock`. Their versions, license expressions, repositories, and
  copyright/authors are in `RUST-DEPENDENCIES.json` and
  `../THIRD-PARTY-NOTICES.md`.

MoshCatty provides builds for Windows x64, Linux x64, Linux arm64, and macOS
universal. There is no Windows arm64 build; Netcatty reports a typed
unsupported-platform error there.

## Modifications and build

Natcatty does not modify MoshCatty's source. MoshCatty's release workflow runs
`cargo build --release` on its own Rust source at the tag and publishes the four
archives plus `SHA256SUMS`. The committed Windows x64 binary is byte-identical
to the published `mosh-client-win32-x64.tar.gz` payload: its SHA-256
(`e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a`) and size
(545,280 bytes) match the release record. The byte-for-byte asset comparison is
recorded in the ticket 13 research; this repository re-verifies the local file
against the compiled-in manifest before every launch. The other targets are
downloaded from the same release and verified by archive and binary SHA-256 at
packaging time.

**Reproducible build: not established.** The release workflow uses the floating
`stable` Rust toolchain and has no `rust-toolchain.toml`, rebuild comparison,
build attestation, or reproducibility statement. Only digest correspondence
between the published assets and the bundled files has been checked. Do **not**
describe these binaries as reproducible. Independent build evidence is pending
human review.

## Layout and checksums

| Target | File | SHA-256 |
| --- | --- | --- |
| win32-x64 | `win32-x64/mosh-client.exe` | `e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a` |
| linux-x64 | `linux-x64/mosh-client` | `215e2527961058a625e13afb5f7e4bc372cec11314260258dcfa2347961f48b8` |
| linux-arm64 | `linux-arm64/mosh-client` | `2553988bea5af73d6d15ecad918855e375ddbf46393ce9379a8ad25c69ceb069` |
| darwin-universal | `darwin-universal/mosh-client` | `460662ccb768e8ffcc7f0c07f58bb5dd1c32202c3b7e4062207a91a722eb83ed` |

Release archive digests:

| Archive | SHA-256 |
| --- | --- |
| `mosh-client-win32-x64.tar.gz` | `ee437592c351361e47c600fafe04ce97a2551aa61fbef1fb3d6dda565835311e` |
| `mosh-client-linux-x64.tar.gz` | `c911d70eac729cb368667766ed817f7aa0a8dd8e2f8a006c38777900d6ef3575` |
| `mosh-client-linux-arm64.tar.gz` | `ebeb217e11f4bec34269fd06373d067d587fa931808cfa7d5d58d870fc3a846a` |
| `mosh-client-darwin-universal.tar.gz` | `daa2da20d2f135190fdbebf952a692d80eed0e21d48cee252a28e901bccc2402` |

The Windows x64 binary is committed so local development and acceptance use the
same bytes as a release. Other targets are fetched at packaging time. The app
recomputes the binary's SHA-256 and size before **every** launch; the digests
are compiled into `adapters/bundle/manifests.go`.

## Fetching

```sh
go run ./internal/tools/fetchtransportbins -kind mosh -target all -out resources
```

The tool downloads the pinned release archive, verifies the archive digest,
extracts only the named client binary (rejecting absolute or parent-relative
paths), verifies the binary digest and size, and writes it under
`resources/mosh/<target>/`.

## Rebuilding from source

The Corresponding Source is MoshCatty's Rust repository at tag
`moshcatty-0.1.8`, commit
`554b9d305e7ac4b11de740d764bbc3e05f816d7b`. From a checkout of that commit:

```sh
cargo build --release
```

The release workflow builds the four targets and publishes `SHA256SUMS`.
`mobile-shell/mosh` is a protocol and behavior reference only. It is not the
source of this binary and must not be offered as its Corresponding Source.

## Licensing and notice obligations

MoshCatty is GPL-3.0-or-later. The combined distribution must carry the GPL
text, the MoshCatty Corresponding Source offer, the mosh-go MIT notice, and the
Rust dependency notices. See `../THIRD-PARTY-NOTICES.md`,
`LICENSE-MoshGo-MIT.txt`, and `RUST-DEPENDENCIES.json`.

This file is engineering work, not legal advice and not an independent audit.
Independent provenance/derivative-work review and legal review are pending; see
[`docs/audit/mosh-provenance.md`](../../docs/audit/mosh-provenance.md).

## Divergence from the original

The original Electron bridge shelled out to the system `ssh` for the bootstrap.
The native app runs the same `sh -c` bootstrap command over its own SSH
Connection Manager, because that already owns saved credentials, MFA prompts,
known-host verification, jump chains, and proxies. Mosh still rejects jump
chains and proxies, matching the original: its UDP channel cannot carry them.
