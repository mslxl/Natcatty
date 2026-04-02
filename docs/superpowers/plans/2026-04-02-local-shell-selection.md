# Local Shell Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow users to select from auto-discovered local shells (CMD, PowerShell, WSL, Git Bash, Cygwin, zsh, bash, fish, etc.) in the QuickSwitcher and Settings.

**Architecture:** New `shellDiscovery.cjs` module in Electron main process detects available shells per platform using `reg query` (Windows) and `/etc/shells` (Unix). Results exposed via IPC, consumed by QuickSwitcher (new "Local Shells" section) and Settings (dropdown replacing text input). Shell SVG icons stored in `public/shells/`.

**Tech Stack:** Electron IPC, node child_process (`reg query`, `where.exe`), React, Radix UI Select, SVG icons

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `electron/bridges/shellDiscovery.cjs` | Platform-specific shell detection + IPC handler registration |
| `public/shells/*.svg` | Colorful SVG icons for each shell type (~12 files) |
| `lib/useDiscoveredShells.ts` | React hook: calls IPC once, caches result, returns `DiscoveredShell[]` |

### Modified Files
| File | Changes |
|------|---------|
| `global.d.ts` | Add `DiscoveredShell` type + `discoverShells` bridge method |
| `electron/preload.cjs` | Expose `netcatty:shells:discover` IPC |
| `electron/bridges/terminalBridge.cjs` | Accept `shellArgs` in `startLocalSession` payload |
| `components/QuickSwitcher.tsx` | Add "Local Shells" section with icons, replace single "Local Terminal" action |
| `components/settings/tabs/SettingsTerminalTab.tsx` | Replace text input with shell dropdown + "Custom" fallback |
| `application/state/useSessionState.ts` | Extend `createLocalTerminal` to accept shell command/args |
| `App.tsx` | Pass shell info through `handleCreateLocalTerminal` |
| `application/i18n/locales/en.ts` | Add i18n strings |
| `application/i18n/locales/zh-CN.ts` | Add i18n strings |

---

## Task 1: Type definitions and IPC bridge wiring

**Files:**
- Modify: `global.d.ts:179` (near `startLocalSession`)
- Modify: `electron/preload.cjs` (near other IPC expose calls)

- [ ] **Step 1: Add DiscoveredShell type and bridge method to global.d.ts**

In `global.d.ts`, add the `DiscoveredShell` interface near the top-level types, and add `discoverShells` to the `NetcattyBridge` interface:

```typescript
interface DiscoveredShell {
  id: string;
  name: string;
  command: string;
  args?: string[];
  icon: string;
  isDefault?: boolean;
}
```

Add to `NetcattyBridge`:
```typescript
discoverShells?(): Promise<DiscoveredShell[]>;
```

Also update `startLocalSession` signature to accept `shellArgs`:
```typescript
startLocalSession?(options: {
  sessionId?: string;
  cols?: number;
  rows?: number;
  shell?: string;
  shellArgs?: string[];  // ← ADD THIS
  cwd?: string;
  env?: Record<string, string>;
  sessionLog?: { enabled: boolean; directory: string; format: string };
}): Promise<string>;
```

- [ ] **Step 2: Expose IPC in preload.cjs**

Find where other IPC methods are exposed in `electron/preload.cjs` (search for `getDefaultShell`). Add nearby:

```javascript
discoverShells: () => ipcRenderer.invoke("netcatty:shells:discover"),
```

- [ ] **Step 3: Commit**

```bash
git add global.d.ts electron/preload.cjs
git commit -m "feat(shell-selection): add DiscoveredShell type and IPC bridge wiring"
```

---

## Task 2: Shell discovery backend — Windows detection

**Files:**
- Create: `electron/bridges/shellDiscovery.cjs`

- [ ] **Step 1: Create shellDiscovery.cjs with Windows shell detection**

```javascript
// electron/bridges/shellDiscovery.cjs
"use strict";

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const os = require("os");

let cachedShells = null;

/**
 * Query a Windows registry key and return its values as an object.
 * Returns null if the key doesn't exist.
 */
function regQuery(keyPath) {
  try {
    const output = execFileSync("reg", ["query", keyPath], {
      encoding: "utf8",
      timeout: 5000,
      windowsHide: true,
    });
    return output;
  } catch {
    return null;
  }
}

/**
 * Query a specific value from a Windows registry key.
 * Returns the value string or null.
 */
function regQueryValue(keyPath, valueName) {
  try {
    const output = execFileSync("reg", ["query", keyPath, "/v", valueName], {
      encoding: "utf8",
      timeout: 5000,
      windowsHide: true,
    });
    // Parse "REG_SZ    value" pattern
    const match = output.match(new RegExp(`${valueName}\\s+REG_\\w+\\s+(.+)`));
    return match ? match[1].trim() : null;
  } catch {
    return null;
  }
}

/**
 * Enumerate registry subkeys under a given path.
 * Returns array of full subkey paths.
 */
function regEnumSubkeys(keyPath) {
  try {
    const output = execFileSync("reg", ["query", keyPath], {
      encoding: "utf8",
      timeout: 5000,
      windowsHide: true,
    });
    // Subkeys are lines that start with the keyPath prefix (full paths)
    return output
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line.startsWith("HKEY_") && line !== keyPath && line.startsWith(keyPath + "\\"));
  } catch {
    return [];
  }
}

function findExecutableOnPath(name) {
  try {
    const result = execFileSync("where.exe", [name], {
      encoding: "utf8",
      timeout: 5000,
      windowsHide: true,
    });
    const candidates = result
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);
    for (const c of candidates) {
      if (fs.existsSync(c)) return c;
    }
  } catch {
    // not found
  }
  return null;
}

function discoverWindowsShells() {
  const shells = [];

  // --- CMD ---
  const comspec = process.env.ComSpec || "cmd.exe";
  shells.push({
    id: "cmd",
    name: "CMD",
    command: comspec,
    args: [],
    icon: "cmd",
  });

  // --- PowerShell 5.1 (Windows built-in) ---
  const ps51 =
    findExecutableOnPath("powershell") ||
    path.join(
      process.env.SystemRoot || "C:\\Windows",
      "System32",
      "WindowsPowerShell",
      "v1.0",
      "powershell.exe"
    );
  if (fs.existsSync(ps51)) {
    shells.push({
      id: "powershell",
      name: "Windows PowerShell",
      command: ps51,
      args: ["-NoLogo"],
      icon: "powershell",
    });
  }

  // --- PowerShell Core (pwsh 7+) ---
  let pwsh = findExecutableOnPath("pwsh");
  if (!pwsh) {
    const regPath = regQueryValue(
      "HKLM\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\App Paths\\pwsh.exe",
      ""
    );
    if (regPath && fs.existsSync(regPath)) pwsh = regPath;
  }
  if (!pwsh) {
    const defaultPath = path.join(
      process.env.ProgramFiles || "C:\\Program Files",
      "PowerShell",
      "7",
      "pwsh.exe"
    );
    if (fs.existsSync(defaultPath)) pwsh = defaultPath;
  }
  if (pwsh && fs.existsSync(pwsh)) {
    shells.push({
      id: "pwsh",
      name: "PowerShell 7",
      command: pwsh,
      args: ["-NoLogo"],
      icon: "pwsh",
    });
  }

  // --- WSL distributions ---
  const lxssPath = "HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Lxss";
  const wslExe =
    path.join(process.env.SystemRoot || "C:\\Windows", "System32", "wsl.exe");
  
  if (fs.existsSync(wslExe)) {
    const subkeys = regEnumSubkeys(lxssPath);
    for (const subkey of subkeys) {
      const distroName = regQueryValue(subkey, "DistributionName");
      if (!distroName) continue;

      const slug = distroName.toLowerCase().replace(/[^a-z0-9]/g, "-");
      shells.push({
        id: `wsl-${slug}`,
        name: `${distroName} (WSL)`,
        command: wslExe,
        args: ["-d", distroName],
        icon: mapWslDistroIcon(distroName),
      });
    }
  }

  // --- Git Bash ---
  let gitInstallPath =
    regQueryValue("HKLM\\Software\\GitForWindows", "InstallPath") ||
    regQueryValue("HKCU\\Software\\GitForWindows", "InstallPath");
  if (!gitInstallPath) {
    // Fallback: check common path
    const defaultGitPath = path.join(
      process.env.ProgramFiles || "C:\\Program Files",
      "Git"
    );
    if (fs.existsSync(path.join(defaultGitPath, "bin", "bash.exe"))) {
      gitInstallPath = defaultGitPath;
    }
  }
  if (gitInstallPath) {
    const gitBash = path.join(gitInstallPath, "bin", "bash.exe");
    if (fs.existsSync(gitBash)) {
      shells.push({
        id: "git-bash",
        name: "Git Bash",
        command: gitBash,
        args: ["--login", "-i"],
        icon: "git-bash",
      });
    }
  }

  // --- Cygwin ---
  const cygwin64Root = regQueryValue(
    "HKLM\\Software\\Cygwin\\setup",
    "rootdir"
  );
  const cygwin32Root = regQueryValue(
    "HKLM\\Software\\WOW6432Node\\Cygwin\\setup",
    "rootdir"
  );
  const cygwinRoot = cygwin64Root || cygwin32Root;
  if (cygwinRoot) {
    const cygwinBash = path.join(cygwinRoot, "bin", "bash.exe");
    if (fs.existsSync(cygwinBash)) {
      shells.push({
        id: "cygwin",
        name: "Cygwin",
        command: cygwinBash,
        args: ["--login", "-i"],
        icon: "cygwin",
      });
    }
  }

  // Mark default: prefer pwsh > powershell > cmd
  const defaultId = shells.find((s) => s.id === "pwsh")
    ? "pwsh"
    : shells.find((s) => s.id === "powershell")
      ? "powershell"
      : "cmd";
  const defaultShell = shells.find((s) => s.id === defaultId);
  if (defaultShell) defaultShell.isDefault = true;

  return shells;
}

/** Map WSL distro names to icon identifiers. Falls back to generic linux. */
function mapWslDistroIcon(distroName) {
  const lower = distroName.toLowerCase();
  if (lower.includes("ubuntu")) return "ubuntu";
  if (lower.includes("debian")) return "debian";
  if (lower.includes("kali")) return "kali";
  if (lower.includes("alpine")) return "alpine";
  if (lower.includes("opensuse") || lower.includes("suse")) return "opensuse";
  if (lower.includes("fedora")) return "fedora";
  if (lower.includes("arch")) return "arch";
  if (lower.includes("oracle")) return "oracle";
  return "linux";
}

module.exports = { discoverWindowsShells, regQuery, regQueryValue, regEnumSubkeys, findExecutableOnPath, mapWslDistroIcon };
```

- [ ] **Step 2: Commit**

```bash
git add electron/bridges/shellDiscovery.cjs
git commit -m "feat(shell-selection): add Windows shell discovery (CMD, PowerShell, WSL, Git Bash, Cygwin)"
```

---

## Task 3: Shell discovery backend — Unix detection + IPC registration

**Files:**
- Modify: `electron/bridges/shellDiscovery.cjs`
- Modify: `electron/bridges/terminalBridge.cjs` (or wherever IPC handlers are registered)

- [ ] **Step 1: Add Unix shell detection and main discover function**

Append to `shellDiscovery.cjs`:

```javascript
function discoverUnixShells() {
  const shells = [];
  const defaultShellPath = process.env.SHELL || "/bin/bash";

  // Read /etc/shells
  let etcShells = [];
  try {
    const content = fs.readFileSync("/etc/shells", "utf8");
    etcShells = content
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"));
  } catch {
    // /etc/shells not available, use fallback
    etcShells = ["/bin/bash", "/bin/zsh", "/bin/sh"];
    if (defaultShellPath && !etcShells.includes(defaultShellPath)) {
      etcShells.unshift(defaultShellPath);
    }
  }

  // Deduplicate and filter to shells that exist
  const seen = new Set();
  for (const shellPath of etcShells) {
    if (seen.has(shellPath)) continue;
    seen.add(shellPath);
    if (!fs.existsSync(shellPath)) continue;

    const basename = path.basename(shellPath);
    const id = basename;
    const name = mapUnixShellName(basename);
    const icon = mapUnixShellIcon(basename);
    const isDefault = shellPath === defaultShellPath;

    shells.push({
      id,
      name,
      command: shellPath,
      args: isLoginShell(basename) ? ["-l"] : [],
      icon,
      isDefault,
    });
  }

  // Ensure system default is in the list even if not in /etc/shells
  if (defaultShellPath && !seen.has(defaultShellPath) && fs.existsSync(defaultShellPath)) {
    const basename = path.basename(defaultShellPath);
    shells.unshift({
      id: basename,
      name: mapUnixShellName(basename),
      command: defaultShellPath,
      args: isLoginShell(basename) ? ["-l"] : [],
      icon: mapUnixShellIcon(basename),
      isDefault: true,
    });
  }

  return shells;
}

function mapUnixShellName(basename) {
  const map = {
    zsh: "Zsh",
    bash: "Bash",
    fish: "Fish",
    sh: "sh",
    ksh: "Ksh",
    tcsh: "Tcsh",
    csh: "Csh",
    dash: "Dash",
    nu: "Nushell",
    pwsh: "PowerShell",
  };
  return map[basename] || basename;
}

function mapUnixShellIcon(basename) {
  const map = {
    zsh: "zsh",
    bash: "bash",
    fish: "fish",
    sh: "terminal",
    ksh: "terminal",
    tcsh: "terminal",
    csh: "terminal",
    dash: "terminal",
    nu: "nushell",
    pwsh: "pwsh",
  };
  return map[basename] || "terminal";
}

function isLoginShell(basename) {
  return ["bash", "zsh", "fish", "ksh", "sh"].includes(basename);
}

/**
 * Main discovery function — platform-dispatched, cached.
 */
function discoverShells() {
  if (cachedShells) return cachedShells;

  if (process.platform === "win32") {
    cachedShells = discoverWindowsShells();
  } else {
    cachedShells = discoverUnixShells();
  }

  return cachedShells;
}
```

Update `module.exports` to include the new functions:
```javascript
module.exports = {
  discoverShells,
  discoverWindowsShells,
  discoverUnixShells,
  // ... keep existing exports
};
```

- [ ] **Step 2: Register IPC handler**

Find where IPC handlers are registered (search for `ipcMain.handle("netcatty:` in the main process bridge registration file — likely `terminalBridge.cjs` around line 1040 or the main bridge setup). Add:

```javascript
const { discoverShells } = require("./shellDiscovery.cjs");

// Near other ipcMain.handle calls:
ipcMain.handle("netcatty:shells:discover", () => discoverShells());
```

- [ ] **Step 3: Extend startLocalSession to accept shellArgs**

In `terminalBridge.cjs`, function `startLocalSession` (line 250), change how `shell` and `shellArgs` are resolved:

Current code (lines 254-256):
```javascript
const defaultShell = getDefaultLocalShell();
const shell = normalizeExecutablePath(payload?.shell) || defaultShell;
const shellArgs = getLocalShellArgs(shell);
```

Replace with:
```javascript
const defaultShell = getDefaultLocalShell();
const shell = normalizeExecutablePath(payload?.shell) || defaultShell;
// Use explicit shellArgs from payload if provided (from shell discovery),
// otherwise auto-detect based on shell path
const shellArgs = payload?.shellArgs ?? getLocalShellArgs(shell);
```

- [ ] **Step 4: Commit**

```bash
git add electron/bridges/shellDiscovery.cjs electron/bridges/terminalBridge.cjs
git commit -m "feat(shell-selection): add Unix shell discovery, IPC registration, and shellArgs passthrough"
```

---

## Task 4: Shell icons

**Files:**
- Create: `public/shells/cmd.svg`
- Create: `public/shells/powershell.svg`
- Create: `public/shells/pwsh.svg`
- Create: `public/shells/git-bash.svg`
- Create: `public/shells/cygwin.svg`
- Create: `public/shells/bash.svg`
- Create: `public/shells/zsh.svg`
- Create: `public/shells/fish.svg`
- Create: `public/shells/terminal.svg`
- Create: `public/shells/nushell.svg`

Note: WSL distro icons (ubuntu, debian, kali, alpine, opensuse, fedora, arch, oracle, linux) already exist in `public/distro/` — we will reuse those, not duplicate them.

- [ ] **Step 1: Source and create shell SVG icons**

Search the web for official/open-source SVG icons for each shell. Create colorful, 24x24 or 32x32 SVGs in `public/shells/`. Some sources:
- **CMD**: Windows command prompt icon (simple `>_` prompt style, or the Windows Terminal icon)
- **PowerShell**: Official PowerShell blue chevron logo from Microsoft (MIT licensed from the PowerShell repo)
- **pwsh**: Same as PowerShell but with dark/black variant, or use the PowerShell 7 specific icon
- **Git Bash**: Git logo (orange-red branching icon)
- **Cygwin**: Cygwin logo (the black hat)
- **Bash**: GNU Bash logo (dark shell icon)
- **Zsh**: Zsh logo or a distinctive terminal icon
- **Fish**: Fish shell logo (green fish)
- **Terminal**: Generic terminal icon (for sh, ksh, tcsh, etc.)
- **Nushell**: Nushell logo (green/teal)

Each SVG should be self-contained (no external references), roughly square, and look good at 20-24px render size. Use `viewBox="0 0 24 24"` or `viewBox="0 0 32 32"`.

- [ ] **Step 2: Commit**

```bash
git add public/shells/
git commit -m "feat(shell-selection): add colorful SVG icons for shell types"
```

---

## Task 5: React hook for shell discovery

**Files:**
- Create: `lib/useDiscoveredShells.ts`

- [ ] **Step 1: Create the hook**

```typescript
import { useEffect, useState } from "react";
import { netcattyBridge } from "../infrastructure/services/netcattyBridge";

interface DiscoveredShell {
  id: string;
  name: string;
  command: string;
  args?: string[];
  icon: string;
  isDefault?: boolean;
}

let shellCache: DiscoveredShell[] | null = null;
let shellPromise: Promise<DiscoveredShell[]> | null = null;

/**
 * Returns the list of locally available shells.
 * Calls the Electron backend once, caches for the session.
 */
export function useDiscoveredShells(): DiscoveredShell[] {
  const [shells, setShells] = useState<DiscoveredShell[]>(shellCache ?? []);

  useEffect(() => {
    if (shellCache) {
      setShells(shellCache);
      return;
    }

    const bridge = netcattyBridge.get();
    if (!bridge?.discoverShells) return;

    if (!shellPromise) {
      shellPromise = bridge.discoverShells();
    }

    shellPromise.then((result) => {
      shellCache = result;
      setShells(result);
    }).catch((err) => {
      console.warn("Failed to discover shells:", err);
    });
  }, []);

  return shells;
}

/**
 * Resolve the icon path for a shell.
 * WSL distro icons come from public/distro/, others from public/shells/.
 */
export function getShellIconPath(iconId: string): string {
  // WSL distros reuse existing distro icons
  const distroIcons = new Set([
    "ubuntu", "debian", "kali", "alpine", "opensuse",
    "fedora", "arch", "oracle", "linux",
  ]);
  if (distroIcons.has(iconId)) {
    return `/distro/${iconId}.svg`;
  }
  return `/shells/${iconId}.svg`;
}
```

- [ ] **Step 2: Commit**

```bash
git add lib/useDiscoveredShells.ts
git commit -m "feat(shell-selection): add useDiscoveredShells hook with icon path resolver"
```

---

## Task 6: QuickSwitcher — replace single Local Terminal with discovered shells

**Files:**
- Modify: `components/QuickSwitcher.tsx`

- [ ] **Step 1: Update QuickSwitcherProps and imports**

Add imports and update props:

```typescript
// Add to imports at top
import { useDiscoveredShells, getShellIconPath } from "../lib/useDiscoveredShells";

// Update QuickSwitcherItem type to include shell data:
type QuickSwitcherItem = {
  type: "host" | "tab" | "workspace" | "action" | "shell";
  id: string;
  data?: Host | TerminalSession | Workspace;
};
```

Update `QuickSwitcherProps` — replace `onCreateLocalTerminal` with a more specific callback:

```typescript
interface QuickSwitcherProps {
  isOpen: boolean;
  query: string;
  results: Host[];
  sessions: TerminalSession[];
  workspaces: Workspace[];
  onQueryChange: (value: string) => void;
  onSelect: (host: Host) => void;
  onSelectTab: (tabId: string) => void;
  onClose: () => void;
  onCreateLocalTerminal?: (shell?: { command: string; args?: string[] }) => void;
  keyBindings?: KeyBinding[];
}
```

- [ ] **Step 2: Add shells to the flatItems list and render the "Local Shells" section**

Inside `QuickSwitcherInner`, call the hook:

```typescript
const discoveredShells = useDiscoveredShells();
```

In the `flatItems` useMemo, replace the single `local-terminal` action with individual shell entries. Before the action items block, add:

```typescript
// Local shells
discoveredShells.forEach((shell) =>
  items.push({ type: "shell", id: shell.id }),
);
```

Remove the old `items.push({ type: "action", id: "local-terminal" });` line.

Add `discoveredShells` to the useMemo dependency array.

Update `handleItemSelect` to handle the `"shell"` type:

```typescript
case "shell": {
  const shell = discoveredShells.find(s => s.id === item.id);
  if (shell && onCreateLocalTerminal) {
    onCreateLocalTerminal({ command: shell.command, args: shell.args });
    onClose();
  }
  break;
}
```

- [ ] **Step 3: Render the "Local Shells" section in the JSX**

Replace the "Quick connect" section (the `<div>` containing the `Local Terminal` action, around lines 372-403) with:

```tsx
{/* Local Shells section */}
{discoveredShells.length > 0 && (
  <div>
    <div className="px-4 py-1.5">
      <span className="text-xs font-medium text-muted-foreground">
        {t("qs.localShells")}
      </span>
    </div>
    {discoveredShells.map((shell) => {
      const idx = getItemIndex("shell", shell.id);
      const isSelected = idx === selectedIndex;
      return (
        <div
          key={shell.id}
          className={`flex items-center gap-3 px-4 py-2.5 cursor-pointer transition-colors ${
            isSelected ? "bg-primary/15" : "hover:bg-muted/50"
          }`}
          onClick={() => {
            if (onCreateLocalTerminal) {
              onCreateLocalTerminal({ command: shell.command, args: shell.args });
              onClose();
            }
          }}
          onMouseEnter={() => setSelectedIndex(idx)}
        >
          <div className="h-6 w-6 rounded flex items-center justify-center">
            <img
              src={getShellIconPath(shell.icon)}
              alt={shell.name}
              className="h-5 w-5"
            />
          </div>
          <span className="text-sm font-medium">{shell.name}</span>
          {shell.isDefault && (
            <span className="text-[10px] text-muted-foreground bg-muted px-1.5 py-0.5 rounded">
              {t("qs.default")}
            </span>
          )}
        </div>
      );
    })}
  </div>
)}
```

- [ ] **Step 4: Make the search filter work for shells**

Shells need to be filtered by search query. Wrap the `discoveredShells` with a filtered version:

```typescript
const filteredShells = useMemo(() => {
  if (!query.trim()) return discoveredShells;
  const q = query.toLowerCase();
  return discoveredShells.filter(
    (s) => s.name.toLowerCase().includes(q) || s.id.toLowerCase().includes(q)
  );
}, [discoveredShells, query]);
```

Use `filteredShells` instead of `discoveredShells` in both the `flatItems` useMemo and the JSX render.

- [ ] **Step 5: Commit**

```bash
git add components/QuickSwitcher.tsx
git commit -m "feat(shell-selection): add discovered shells to QuickSwitcher with icons and search"
```

---

## Task 7: Wire up App.tsx — pass shell info through to session creation

**Files:**
- Modify: `App.tsx`
- Modify: `application/state/useSessionState.ts`

- [ ] **Step 1: Update createLocalTerminal in useSessionState.ts**

In `useSessionState.ts`, update `createLocalTerminal` (around line 41) to accept shell command/args:

```typescript
const createLocalTerminal = useCallback((options?: {
  shellType?: TerminalSession['shellType'];
  shell?: string;
  shellArgs?: string[];
}) => {
  const sessionId = crypto.randomUUID();
  const localHostId = `local-${sessionId}`;
  const newSession: TerminalSession = {
    id: sessionId,
    hostId: localHostId,
    hostLabel: 'Local Terminal',
    hostname: 'localhost',
    username: 'local',
    status: 'connecting',
    protocol: 'local',
    shellType: options?.shellType,
    localShell: options?.shell,       // Store for use in session starter
    localShellArgs: options?.shellArgs,
  };
  setSessions(prev => [...prev, newSession]);
```

This requires adding `localShell` and `localShellArgs` to the `TerminalSession` type in `domain/models.ts` (or `types.ts`). Add these optional fields:

```typescript
// In TerminalSession interface
localShell?: string;       // Shell command for local terminals
localShellArgs?: string[]; // Shell args for local terminals
```

- [ ] **Step 2: Update App.tsx handleCreateLocalTerminal**

Change `handleCreateLocalTerminal` to accept optional shell info:

```typescript
const handleCreateLocalTerminal = useCallback((shell?: { command: string; args?: string[] }) => {
  const { username, hostname } = systemInfoRef.current;
  const shellType = classifyLocalShellType(
    shell?.command || terminalSettings.localShell,
    navigator.userAgent
  );
  const sessionId = createLocalTerminal({
    shellType,
    shell: shell?.command,
    shellArgs: shell?.args,
  });
  addConnectionLog({
    sessionId,
    hostId: '',
    hostLabel: 'Local Terminal',
    hostname: 'localhost',
    username: username,
    protocol: 'local',
    startTime: Date.now(),
```

Update `createLocalTerminalWithCurrentShell` similarly:

```typescript
const createLocalTerminalWithCurrentShell = useCallback(() => {
  return createLocalTerminal({
    shellType: classifyLocalShellType(terminalSettings.localShell, navigator.userAgent),
  });
}, [createLocalTerminal, terminalSettings.localShell]);
```

Update the QuickSwitcher's `onCreateLocalTerminal` prop to pass the new callback:

```tsx
onCreateLocalTerminal={(shell) => {
  handleCreateLocalTerminal(shell);
  setIsQuickSwitcherOpen(false);
  setQuickSearch('');
}}
```

- [ ] **Step 3: Pass shell info to session starter**

In `createTerminalSessionStarters.ts`, the `startLocal` function (line 750) currently reads `ctx.terminalSettings?.localShell`. It needs to also check for per-session shell override. Find where `startLocalSession` is called (line 771) and update:

```typescript
const id = await ctx.terminalBackend.startLocalSession({
  sessionId: ctx.sessionId,
  cols: term.cols,
  rows: term.rows,
  shell: /* per-session shell override */ || localShell,
  shellArgs: /* per-session shellArgs */,
  cwd: localStartDir,
  env: {
    TERM: ctx.terminalSettings?.terminalEmulationType ?? "xterm-256color",
  },
  sessionLog: ctx.sessionLog?.enabled ? ctx.sessionLog : undefined,
});
```

The per-session shell/shellArgs come from the `TerminalSession` object. You need to thread these through the session starter context. The cleanest approach: read them from the host/session object that's available in `ctx`. Check where `ctx` is constructed in `Terminal.tsx` and add `localShell`/`localShellArgs` from the session.

- [ ] **Step 4: Commit**

```bash
git add application/state/useSessionState.ts App.tsx components/terminal/runtime/createTerminalSessionStarters.ts domain/models.ts
git commit -m "feat(shell-selection): wire shell selection through App → session state → session starter"
```

---

## Task 8: Settings — replace text input with shell dropdown

**Files:**
- Modify: `components/settings/tabs/SettingsTerminalTab.tsx`

- [ ] **Step 1: Import hook and update the Local Shell section**

Add import:
```typescript
import { useDiscoveredShells, getShellIconPath } from "../../../lib/useDiscoveredShells";
```

Inside the component, call the hook:
```typescript
const discoveredShells = useDiscoveredShells();
```

Replace the current Input for `localShell` (lines 894-920) with a dropdown:

```tsx
<SettingRow
  label={t("settings.terminal.localShell.shell")}
  description={t("settings.terminal.localShell.shell.desc")}
>
  <div className="flex flex-col gap-1 items-end">
    <select
      className="h-9 w-48 rounded-md border border-input bg-background px-3 text-sm"
      value={terminalSettings.localShell || ""}
      onChange={(e) => {
        const value = e.target.value;
        if (value === "__custom__") {
          // Switch to custom mode — keep current value but let user type
          updateTerminalSetting("localShell", terminalSettings.localShell || "");
          setShowCustomShellInput(true);
        } else {
          updateTerminalSetting("localShell", value);
          setShowCustomShellInput(false);
        }
      }}
    >
      <option value="">{t("settings.terminal.localShell.shell.default")}{defaultShell ? ` (${path.basename(defaultShell)})` : ""}</option>
      {discoveredShells.filter(s => !s.isDefault).map((shell) => (
        <option key={shell.id} value={shell.command}>
          {shell.name}
        </option>
      ))}
      <option value="__custom__">{t("settings.terminal.localShell.shell.custom")}</option>
    </select>
    {showCustomShellInput && (
      <Input
        value={terminalSettings.localShell}
        placeholder={t("settings.terminal.localShell.shell.placeholder")}
        onChange={(e) => updateTerminalSetting("localShell", e.target.value)}
        className={cn(
          "w-48",
          shellValidation && !shellValidation.valid && "border-destructive focus-visible:ring-destructive"
        )}
      />
    )}
    {shellValidation && !shellValidation.valid && shellValidation.message && (
      <span className="text-xs text-destructive flex items-center gap-1">
        <AlertCircle size={12} />
        {shellValidation.message}
      </span>
    )}
  </div>
</SettingRow>
```

Add state for custom mode:
```typescript
const [showCustomShellInput, setShowCustomShellInput] = useState(() => {
  // Show custom input if current value doesn't match any discovered shell
  if (!terminalSettings.localShell) return false;
  return !discoveredShells.some(s => s.command === terminalSettings.localShell);
});
```

- [ ] **Step 2: Commit**

```bash
git add components/settings/tabs/SettingsTerminalTab.tsx
git commit -m "feat(shell-selection): replace shell text input with dropdown in Settings"
```

---

## Task 9: i18n strings

**Files:**
- Modify: `application/i18n/locales/en.ts`
- Modify: `application/i18n/locales/zh-CN.ts`

- [ ] **Step 1: Add English strings**

Search for `qs.localTerminal` in `en.ts` and add nearby:

```typescript
"qs.localShells": "Local Shells",
"qs.default": "default",
```

Search for `settings.terminal.localShell.shell` and update:

```typescript
"settings.terminal.localShell.shell.default": "System Default",
"settings.terminal.localShell.shell.custom": "Custom...",
```

- [ ] **Step 2: Add Chinese strings**

Same keys in `zh-CN.ts`:

```typescript
"qs.localShells": "本地终端",
"qs.default": "默认",
"settings.terminal.localShell.shell.default": "系统默认",
"settings.terminal.localShell.shell.custom": "自定义...",
```

- [ ] **Step 3: Commit**

```bash
git add application/i18n/locales/en.ts application/i18n/locales/zh-CN.ts
git commit -m "feat(shell-selection): add i18n strings for shell selection UI"
```

---

## Task 10: Integration testing and cleanup

- [ ] **Step 1: Manual test on macOS**

1. Open QuickSwitcher — verify "Local Shells" section shows zsh, bash, sh, fish (if installed), each with correct icon
2. Default shell (zsh on macOS) should have "default" badge
3. Click a shell — new local terminal tab opens with that shell
4. Type in search "fish" — only Fish shell shows (if installed)
5. Open Settings → Terminal → verify dropdown shows discovered shells + "Custom..." option

- [ ] **Step 2: Manual test on Windows (if available)**

1. Verify CMD, PowerShell, PowerShell Core (if installed), WSL distros, Git Bash, Cygwin are all detected
2. Each WSL distro shows its correct distro icon
3. Selecting a WSL distro opens a terminal in that distro
4. Settings dropdown works correctly

- [ ] **Step 3: Verify backward compatibility**

1. Users with existing `localShell` string in settings should still work
2. If `localShell` doesn't match any discovered shell, Settings shows "Custom..." with the text input
3. Empty `localShell` (default) still uses system default shell

- [ ] **Step 4: Final commit**

```bash
git add -A
git commit -m "feat(shell-selection): final integration polish"
```
