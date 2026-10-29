# Netcatty Native

This is the **Native edition** of [Netcatty](https://github.com/binaricat/Netcatty).

---

## ⚠️ Warning: Essentially No Human Involvement

**This repository is generated and maintained almost entirely by AI, with essentially no human involvement.**

- Code, commits, documentation, and releases may not have been reviewed by a human.
- Functionality, stability, and security are **not guaranteed** in any way.
- Bugs, incomplete implementations, breaking changes — or a build that simply does not run — are all possible.
- **Do not** use this in production or for anything important.
- Any consequences of using this repository are the user's own responsibility.

For a stable and reliable version, use the upstream project: [binaricat/Netcatty](https://github.com/binaricat/Netcatty).

## Building and release artifacts

The native app is built with MyGo (see [docs/packaging.md](docs/packaging.md)):

```sh
go tool mygo build -platform windows/amd64 -o dist
go tool mygo build -platform linux/amd64,linux/arm64 -o dist
go tool mygo build -platform darwin/universal -o dist    # macOS runner needed for the DMG
```

A release produces a macOS universal `.app` and DMG, a Windows amd64 NSIS Setup
`.exe`, and Linux amd64/arm64 `.tar.gz` archives with `install.sh` plus `.deb`
packages. The macOS app is **ad hoc signed only**; nothing is Developer ID
signed, notarized, or Authenticode signed. The app ships no self-updater and no
update metadata. Pushing a strict SemVer release tag publishes a GitHub release
page with those installers and the checksum manifest; a manually dispatched
workflow run only validates and builds.

The interface uses the operating system's fonts. On Linux, a system without a
CJK font (for example a minimal container) renders Chinese text with no
visible glyphs. Install a CJK font such as `fonts-noto-cjk` before running the
app; the `.deb` does not declare a CJK font dependency. The CI images install
`fonts-noto-cjk` for the same reason.

## Nix development and installation

The flake supports `aarch64-darwin`, `aarch64-linux`, and `x86_64-linux`:

```sh
nix develop
nix build .#natcatty
```

The Nix package keeps `mygo.json`, `CHANGELOG.md`, and the `resources/`
directory beside the executable so bundled runtime resources can be resolved
from a Nix store path. On NixOS or nix-darwin, import the matching module and
enable the program:

```nix
{
  # In a NixOS module, with `natcatty` bound to the flake input:
  imports = [ natcatty.nixosModules.default ];
  programs.natcatty.enable = true;

  # nix-darwin uses `natcatty.darwinModules.default` instead.
}
```
