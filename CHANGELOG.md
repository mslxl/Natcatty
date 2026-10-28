# Changelog

All notable changes to Natcatty are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Release notes are generated from this file by `internal/tools/releasenotes`;
do not maintain them by hand.

## [Unreleased]

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
- Strict SemVer release validation, per-platform and aggregate `SHA256SUMS`,
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

The complete, signed-off list with parity rows is in
`docs/parity/limitations.md`.
