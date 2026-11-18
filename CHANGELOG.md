# Changelog

All notable changes to Natcatty are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
GitHub generates each release body from the tag's commits and merged pull
requests; this file is the human-maintained changelog.

## [Unreleased]

### Added

- A local shell's Tab now closes when the shell exits normally, instead of
  keeping the exit screen. Settings → Terminal → Local shell exit keeps the
  ended screen instead; SSH, Telnet, and failed local transports still keep
  theirs, and a tmux detach still closes as before.
- Settings → Layout & Tabs → Open on launch can now show a new local terminal
  or the Monitoring page at startup, in addition to the Vault and the last
  session. A restore that finds nothing falls back to the chosen landing.
  Because the setting is synced, a device still running an older build
  normalizes the new values to Vault once; re-select the choice after all
  devices are upgraded.
- The global shortcut has four user-selectable Hotkey Modes: release it to the
  system, toggle the main window as before, pull down a Quake terminal, or
  pull down the complete application. Quake is the default. The Dropdown
  Panel opens on the display under the pointer at the work area's full width,
  with configurable percent or pixel heights (50% for Quake, 80% for
  top-down) and independent hide-on-blur, hide-on-Escape, and
  hide-on-click-outside switches. The Quake dropdown keeps its own local Shell
  and Host Tabs while hidden; Follow Mode can instead render the main
  window's active Tab without moving or reordering it, and hides and restores
  the main window around the Panel.
- Synced upstream `binaricat/Netcatty` `73d15da76..ad0586702`
  (2026-10-19..2026-11-09): workspace pane rearrange with drop guards,
  compose bracketed paste / send target / reconnect / directory picker,
  Shift+Enter, archive extraction links, snippets and command descriptions
  at prompts, startup commands, host-list toggle, custom shell and
  `--open-terminal-shell`, quick connect, per-GPU/per-core/disk-I/O
  monitoring, global default proxy, serial named pipes and auto-login,
  SSH TOTP/rekey/host-certificates, MCP Streamable HTTP and dedicated SFTP
  dial, SFTP duplicate/create-archive, Nginx highlighting, quit-while-
  transferring guard, vault marquee/CSV jump chains/inherited proxy+X11,
  and convergent migration conflict resolution. Session menu can open the
  session log, and a local shell's cwd is probed without OSC 7.
- Mosh Sessions can use a Network Profile. The in-process Mosh client carries
  both its SSH bootstrap and its UDP flow through the selected Network
  Provider, so Mosh works over Tailscale Net and WireGuard Net without an
  OS-level VPN. A Provider that cannot carry datagrams fails the Session
  closed; Mosh never falls back to the host network.

### Fixed

- Restored GPL-3.0-or-later distribution compliance: a root `LICENSE` and
  `NOTICE`, expanded third-party notices (the per-crate copyright and mosh-go
  gaps are marked for ticket 15), an About & Licenses settings area, a
  per-release Corresponding Source statement generated into every package, and
  `internal/legal` checks that fail when a document is missing. The defective
  `v0.1.0` release and tag were withdrawn on 2026-11-05; `v0.1.0-beta.1` is the
  first release built with the restored license files.

### Changed

- External MCP is now opt-in. A fresh or existing profile opens no loopback
  listener and publishes no discovery file until Settings → External MCP is
  enabled; `natcatty-mcp` clients cannot connect before that, and the stored
  document needs no migration. The same area now exposes the Streamable HTTP
  endpoint's enable switch, listen address, and port, and all three apply
  without a restart.
- The bundle identifier is now `com.mslxl.natcatty` on every platform (it was
  `io.github.mslxl.natcatty`). The operating system treats this build as a
  different app: quit the previous version before installing, then expect to
  grant permissions again and to re-enter saved passwords and passphrases
  (they are still in the system credential store, under the old name, but the
  app can no longer read them). On macOS the notification authorization is
  asked again and an existing Start at login entry is no longer recognised;
  re-enable it once and delete the old entry if System Settings shows two. On
  Windows the old Apps & Features entry and the old Run value under
  `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` linger and should be
  removed. The Vault, Hosts, settings, Sessions and window layout live under
  the unchanged application name and survive untouched.
- Settings → System → Global Hotkey is now the four-mode choice above. The old
  enable checkbox and the stored `globalHotkeyEnabled` value were removed; an
  old record loads with Quake as the default and no migration, while tray,
  notification, and deep-link settings keep their values.
- Mosh and EternalTerminal always use their in-process clients. The global and
  per-Host client choice, its release default and parity-gate policy, the
  settings provenance/migration machinery, the bundled-client retention
  window, and the per-platform waivers were removed. Stored
  `transportImplementation` values from older builds are ignored on load; no
  configuration migration or Host re-save is required.
- Session restore is opt-in. A fresh profile lands on the Vault and opens no
  Tabs; the previous Tabs and layout are restored only after enabling Restore
  the previous session in Settings → Layout & Tabs.
- Release tags now publish a GitHub Release page with the platform installers
  (the macOS disk images for Apple Silicon and Intel, the Windows Setup and
  portable zip, and the Linux
  tar.gz and .deb packages for amd64 and arm64) and the checksum manifest,
  under one `natcatty-<version>-<os>-<arch>` naming scheme. The standalone
  Linux `install.sh` is no longer published. A SemVer pre-release tag is
  marked as a GitHub pre-release and never becomes Latest. A manually
  dispatched workflow run still only validates and builds. The release body is
  generated by GitHub from the tag's commits and merged pull requests. Release
  preparation uses `scripts/release.nu`; the Go `releasecheck` helper, its
  release-time checks, and the custom release-note generator were removed, and
  `internal/tools/sourceoffer` writes the package's Corresponding Source
  offer.
- MyGo is upgraded from v0.6.0 to v0.9.2 and the native UI uses its checked
  element value API. The module graph is refreshed: `golang.org/x/crypto`
  v0.59.0, `golang.org/x/net` v0.62.0, `golang.org/x/image` v0.48.0,
  `golang.org/x/sys` v0.50.0, `golang.org/x/text` v0.44.0,
  `golang.org/x/sync` v0.25.0, `golang.org/x/term` v0.48.0,
  `github.com/glebarez/go-sqlite` v1.23.0, `github.com/pkg/sftp` v1.13.12,
  `gorm.io/gorm` v1.31.2, `tailscale.com` v1.104.7, and its
  `github.com/tailscale/wireguard-go`
  v0.0.0-20261106233718-9e0ab45c16ca. Tailscale v1.104.7 requires Go 1.27.4,
  so the toolchain moves to 1.27.4; nixpkgs still ships 1.27.3, and `flake.nix`
  overrides the Go source until nixpkgs catches up. `golang.zx2c4.com/wireguard`
  stays at v0.0.0-20261024220952-b3bc704ef0ee.
- The pinned MyGo submodule is upgraded from v0.7.4 to v0.10.0, so the version
  compiled through the `replace` directive now matches `go.mod`; the five local
  patches are rebased onto v0.10.0. MyGo v0.10.0's `InputEvent.Code` is not
  wired into the VNC key resolver yet.

### Removed

- The bundled Mosh client (`resources/mosh/`, the MoshCatty binary, and its
  Rust crate notices) and the bundled EternalTerminal client (`resources/et/`,
  the `et`/`et.exe` binary, its provenance/license/README files, the
  Netcatty-et-bin manifest, and the `fetchtransportbins` tooling). Mosh and ET
  always use the in-process Go clients; a stored `bundled` or unknown
  implementation value is ignored without a configuration migration.
- The generated parity matrix, its `paritygen` CLI, the CI and release gates
  that read it, and the signed limitations list. The MyGo rewrite is complete;
  the regular test suite and the workflow's artifact checks cover releases.

### Security

- Release builds use Go 1.27.4, which fixes GO-2026-6859, a `net/http` HTTP/1
  server crash caused by a `MaxBytesReader` header race that `govulncheck`
  reported as reachable from called code in the Go 1.27.3 build. With the
  toolchain and module graph updated, the pinned `govulncheck` run reports no
  remaining vulnerabilities.

## [0.1.0] - 2026-10-28

### Added

- Native Go application built with MyGo v0.5.5, replacing the Electron
  renderer. No Node.js, Chromium renderer, or plugin runtime is shipped.
- SSH workspace: Hosts, Host Groups, SSH Keys, Identities, Proxy Profiles,
  Known Hosts, Connections with lease sharing and idle parking, and the local
  shell.
- Terminal Sessions with tabs, recursive split Panes, Workspaces, detach,
  Hibernate/Resume, and Observer snapshots. Session logging in raw, text, and
  HTML form, command tracking, activity indicators, OSC 7/52/99/133 handling,
  and ZMODEM/YMODEM detection gating.
- Terminal encoding switching between UTF-8 and legacy encodings, protected
  multiline paste, the Session menu with the original actions and shortcuts,
  and file/path drag and drop with shell-correct quoting.
- SFTP and SCP file browsing and transfers with pause/cancel latches, bounded
  checkpoints, resume, reattach after reconnect, directory transfers, and
  overwrite and restart behavior.
- Port forwarding rules (local, remote, and dynamic) with runtime status, and
  host routing through dedicated SSH transports.
- Monitoring panels and the System Manager (overview, processes, signals,
  ports, services, tmux, Docker, and GPU detection).
- Host-routed browser side panel with a dedicated per-Host tunnel, address bar,
  navigation, tabs, host and all-Host bookmarks, favicons, downloads, and a
  typed bridge with CSP and origin enforcement.
- Vault search across Hosts, Host Groups, SSH Keys, Identities, Snippets,
  Snippet Packages, Vault Notes, Note Groups, Proxy Profiles, and Knowledge,
  plus the Quick Switcher and command palette.
- Import and export for the supported portable formats, encrypted Local
  Backups with retention, protective backups before Restore, and deterministic
  Exports.
- Cloud sync with GitHub Gist, Google Drive, OneDrive, WebDAV, and S3
  providers, the four merge strategies, three-way field merge with an encrypted
  anchor, shrink and tombstone guards, durable conflict resolution with
  backups, and opt-in convergent sync v2 with version vectors, multi-value
  registers, HLC tie-breaking, fail-closed migration, and divergence detection.
- External MCP stdio server and tool catalog with forced scope, live Session
  allowlists, ownership generations, observer/confirm/auto permission modes,
  single-use expiring approvals, 34 executable write tools through real
  transports, the command blocklist, bounded output, and a Settings approval
  surface.
- Five locale catalogs (English, Spanish, Russian, Simplified Chinese, and
  Traditional Chinese) compiled into the executable, with per-Session
  encoding and localized product surfaces.
- 124 UI appearance presets, accent and app-icon selection, window opacity,
  terminal color schemes, and `.itermcolors` import.
- App Lock with a master password, autostart, tray and menu integration, deep
  links, portable data, and crash logs.
- Packaging for macOS (universal DMG), Windows amd64 (NSIS Setup), and Linux
  amd64/arm64 (tar.gz, deb, and `install.sh`) with bundled mosh and
  EternalTerminal clients, license notices, and SHA-256 checksums.
- Strict SemVer release validation, per-platform and aggregate checksum manifests,
  release notes generated from this changelog, and a parity matrix gate that
  requires every in-scope row to be verified or explicitly excluded.

### Changed

- The application is a single native process; there is no Electron main
  process, renderer process, or preload bridge.
- The terminal context actions are reachable through the Session menu because
  MyGo v0.5.5 installs its own native terminal menu.
- External MCP approvals are single-use and expire after an absolute 110
  seconds instead of offering persistent Always allow grants.
- Sync runs through the native provider adapters; plugin sync providers are
  not supported.

### Fixed

- Window layout and control reachability at the 720x480 minimum and
  1100x720 default sizes in all five locales.
- Contrast of danger and warning text on every light and dark appearance
  preset, and of accent-colored self-test overlay text.
- macOS launch no longer panics when the Dock icon is applied during startup;
  the icon is set once the application is ready.
- CJK text rendering in the packaged application on systems with a CJK font,
  and a dedicated search path that keeps CJK queries literal.

### Security

- Host keys, credentials, and Vault secrets are stored through the platform
  secret store; approval prompts redact secret-named arguments.
- Web content runs behind a tunnel gateway with CSP, origin, and document-type
  enforcement; product APIs are not reachable from the page.
- Release builds are unsigned (ad hoc macOS signature only) and contain no
  auto-update, notarization, or signing metadata.
- The terminal command blocklist is a textual safety net for external MCP
  writes, not an OS sandbox.

### Known limitations

- Terminal search, programmatic selection export, cursor geometry, inline
  images, a second live terminal view, a terminal accessibility tree, write
  receipts, and exact Kitty key encoding are unavailable in MyGo v0.5.5.
- SSH compression other than `none` and several legacy algorithms are not
  supported; security-key authentication requires the system OpenSSH agent.
- Windows arm64 is not a supported release target because bundled mosh and ET
  binaries are unavailable.
- Browser cookies are not partitioned per Host; the browser engine draws its
  own blocked and load-error page, which stays in the engine locale.
- Live cloud-provider endpoints are not contacted by tests; the providers are
  verified against local protocol fakes.
- On Linux, install a CJK font (for example `fonts-noto-cjk`) before using
  Chinese or Traditional Chinese text; the package does not declare a font
  dependency.

The complete, signed-off list with parity rows was published with `v0.1.0`;
it remains available in that tag's tree at `docs/parity/limitations.md`.
