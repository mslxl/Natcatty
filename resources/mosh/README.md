# Bundled mosh-client

Netcatty runs Mosh Sessions with a bundled `mosh-client`, never with a binary
found on `PATH`. The SSH bootstrap uses the app's own SSH Connection Manager;
only the local UDP client is bundled.

## Source

- Release repository: <https://github.com/binaricat/MoshCatty>
- Release: `moshcatty-0.1.8`
- License: GPL-3.0-or-later (see `LICENSE-MoshCatty-GPL-3.0.txt`)
- Upstream mosh: <https://github.com/mobile-shell/mosh>

MoshCatty builds mosh-client for Windows, Linux, and macOS. macOS is a
universal binary. There is no Windows arm64 build; Netcatty reports a typed
unsupported-platform error there.

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

MoshCatty's release workflow builds mosh from upstream
`mobile-shell/mosh` (GPL-3.0-or-later) and publishes the client. GPL requires
that the corresponding source be available: use the MoshCatty repository at the
release tag, or upstream mosh at the version named in MoshCatty's release
notes. Netcatty's distribution offer must point recipients at that source, and
these notices must accompany the binary.

## Divergence from the original

The original Electron bridge shelled out to the system `ssh` for the bootstrap.
The native app runs the same `sh -c` bootstrap command over its own SSH
Connection Manager, because that already owns saved credentials, MFA prompts,
known-host verification, jump chains, and proxies. Mosh still rejects jump
chains and proxies, matching the original: its UDP channel cannot carry them.
