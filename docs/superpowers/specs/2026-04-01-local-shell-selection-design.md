# Local Shell Selection Design

**Issues:** #493 (支持选择本地终端), #426 (Supports WSL)
**Date:** 2026-04-01

## Goal

Allow users to select from auto-discovered local shells (CMD, PowerShell, WSL distros, Git Bash, Cygwin, etc.) when opening a local terminal tab. Currently netcatty only supports a single manually-typed shell path in settings.

## Non-Goals

- Shell Profile system (custom args/env/cwd per shell) — YAGNI for now
- Host-level shell binding — local shells are local, not tied to remote hosts
- MSYS2, Cmder, VS Dev Tools detection
- Shell-specific settings (e.g., PowerShell execution policy)

## Shell Discovery

### Architecture

New module `electron/bridges/shellDiscovery.cjs` in the Electron main process. Discovery runs once on first IPC call, results cached in memory for the session lifetime.

### Windows Detection

| Shell | Detection Method |
|-------|-----------------|
| CMD | `%ComSpec%` env var, fallback `cmd.exe` |
| PowerShell 5.1 | `where.exe powershell` + fallback `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe` |
| PowerShell Core | `where.exe pwsh` + `reg query "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\pwsh.exe"` |
| WSL distros | `reg query "HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss"` — enumerate subkeys, read `DistributionName`. Each distro launched via `wsl.exe -d <name>` |
| Git Bash | `reg query "HKLM\Software\GitForWindows"` read `InstallPath` → `<path>\bin\bash.exe` with args `['--login', '-i']` |
| Cygwin 64-bit | `reg query "HKLM\Software\Cygwin\setup"` read `rootdir` → `<path>\bin\bash.exe` with args `['--login', '-i']` |
| Cygwin 32-bit | `reg query "HKLM\Software\WOW6432Node\Cygwin\setup"` read `rootdir` (fallback) |

Registry access is done via `child_process.execSync('reg query ...')` — no native npm dependency needed. Follows the existing pattern of `where.exe` calls in `terminalBridge.cjs`.

### macOS / Linux Detection

- Parse `/etc/shells` to get all available shells
- Mark `$SHELL` as the default
- Common shells: zsh, bash, fish, sh, ksh, tcsh

### Data Structure

```typescript
interface DiscoveredShell {
  id: string;           // 'cmd', 'powershell', 'pwsh', 'wsl-ubuntu', 'git-bash', 'cygwin', 'zsh', ...
  name: string;         // 'CMD', 'PowerShell', 'Ubuntu (WSL)', 'Git Bash', 'Zsh', ...
  command: string;      // Executable path
  args?: string[];      // Launch args, e.g., ['-d', 'Ubuntu'] or ['--login', '-i']
  icon: string;         // Icon identifier for the shell (maps to SVG asset)
  isDefault?: boolean;  // Whether this is the system default shell
}
```

Not persisted — discovered fresh each app launch, cached in memory.

### IPC Interface

- `netcatty:shells:discover` → returns `DiscoveredShell[]` (discovers on first call, caches thereafter)
- `netcatty:local:start` — extend existing payload with optional `shell?: { command: string; args?: string[] }`. When omitted, uses the default shell (from settings or system default).

## Shell Icons

Colorful SVG icons stored in `assets/shells/`:

| Shell | Icon |
|-------|------|
| CMD | Windows command prompt icon |
| PowerShell | Official PowerShell blue icon |
| PowerShell Core (pwsh) | PowerShell dark/black icon |
| Ubuntu (WSL) | Ubuntu circle-of-friends logo (orange) |
| Debian (WSL) | Debian swirl (red) |
| Kali (WSL) | Kali dragon |
| Alpine (WSL) | Alpine mountain (blue) |
| openSUSE (WSL) | openSUSE chameleon |
| Generic Linux (WSL fallback) | Tux penguin |
| Git Bash | Git logo (orange-red) |
| Cygwin | Cygwin logo |
| Zsh | Zsh logo or terminal icon |
| Bash | Bash logo (dark) |
| Fish | Fish shell logo (green) |
| Generic shell (fallback) | Terminal icon |

Icons are sourced from official project assets / open-source icon sets, stored as SVG files, and rendered inline in the QuickSwitcher and Settings UI.

## UI Changes

### QuickSwitcher Integration

Add discovered shells as selectable entries in the QuickSwitcher:

- Each shell is an entry with its colorful icon and name
- Selecting a shell opens a new local terminal tab using that shell
- Supports search filtering (typing "wsl" shows WSL distros, "bash" shows Git Bash / Bash, etc.)
- Shells appear in a "Local Shells" section/category within the QuickSwitcher

### Settings → Terminal

Replace the current `localShell` manual text input with a dropdown select:

- First option: "Default" (system default — shows detected default in hint text)
- Followed by all discovered shells
- Last option: "Custom..." — when selected, expands a text input for manual path entry
- Storage unchanged: `localShell` remains a `string` in `TerminalSettings`. Empty string = system default. Otherwise stores the shell command path.

### No Changes to

- Host/Group configuration (local shell is not host-specific)
- Terminal appearance or behavior per shell type

## Data Flow

```
App startup
  → User opens QuickSwitcher or Settings
  → Frontend calls netcatty:shells:discover (IPC)
  → shellDiscovery.cjs runs platform-specific detection (first call only)
  → Returns DiscoveredShell[] (cached for subsequent calls)
  → QuickSwitcher renders shells with icons
  → User selects a shell
  → Frontend calls netcatty:local:start with shell.command + shell.args
  → terminalBridge.cjs spawns node-pty with specified shell
  → New terminal tab opens
```

## Implementation Scope

### New Files
- `electron/bridges/shellDiscovery.cjs` — shell discovery logic
- `assets/shells/*.svg` — shell icons (approx. 15 files)
- Type definitions for `DiscoveredShell` in `global.d.ts` or `domain/models.ts`

### Modified Files
- `electron/preload.cjs` — expose `netcatty:shells:discover` IPC
- `electron/main.cjs` or bridge registration — register IPC handler
- `electron/bridges/terminalBridge.cjs` — accept optional `shell` in `startLocalSession` payload
- QuickSwitcher component — add shell entries with icons
- `components/settings/tabs/SettingsTerminalTab.tsx` — replace text input with dropdown
- `global.d.ts` — add bridge type for discover method
- `application/i18n/locales/en.ts` + `zh-CN.ts` — i18n strings

### Unchanged
- Terminal rendering, xterm configuration, session lifecycle
- SSH/Telnet/Serial terminal paths
- The actual node-pty spawn logic (just receives different command/args)
