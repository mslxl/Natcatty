# Netcatty native rewrite: Go environment, MyGo UI, and module design

Status: research / design proposal  
Date: 2026-10-19  
Scope: replace the Electron + React implementation with a Go desktop application using
[egoist/mygo](https://github.com/egoist/mygo)'s native UI.

Sources verified against:

- MyGo **v0.5.5**, released 2026-10-16 (`go.mod` says `go 1.27.1`).
- Go **1.27.1**, the version installed on the development machine.
- The deleted Netcatty source at commit **73d15da7**. All original paths below are
  read with `git show 73d15da7:<path>`; nothing from that commit has been restored
  into the working tree.

The recommendation is deliberately specific, but it is not a promise that MyGo can
replace every Electron feature on day one. The gaps are listed explicitly in
section 6.

---

## 1. Executive summary and recommendation

### Verdict

**Build a new native Go application, not a line-for-line port of the React app.**
Use MyGo native UI for the shell, vault, terminal workspace, SFTP, forwarding,
settings, and AI panels. Keep a webview only as an escape hatch for third-party
plugin UI and, if needed, OAuth pages.

This is credible primarily because MyGo ships a first-class terminal plugin built
on **libghostty-vt**, Ghostty's terminal emulator, with an `io.ReadWriteCloser`
bridge specifically intended for SSH sessions and serial ports. That removes the
two hardest Electron-specific pieces: node-pty ownership and xterm.js rendering.
The plugin is pure Go from the app's point of view, loads a native library with
purego, and does not require cgo.

The rest of the app is a large but conventional desktop application. MyGo's native
widget set covers the screens: virtualized lists/tables/trees, split views, tabs,
sidebars, dialogs, context menus, text inputs with IME, code editor, Markdown,
charts, drag-and-drop, tray, global shortcuts, deep links, notifications, and
system credential storage.

### Why a mechanical port would fail

The original is not a small app:

| Area | Files at 73d15da7 |
|---|---:|
| `electron/` | 760 |
| `domain/` | 396 |
| `application/` | 644 |
| `infrastructure/` | 248 |
| `components/` | 986 |
| `packages/` | 27 |
| Non-test `.ts`/`.tsx` | 1,370 |
| `.test.*` files | 1,402 |
| `.cjs` files | 802 |

(Counted with `git ls-tree -r --name-only 73d15da7:<dir> | wc -l`.) The React
hooks, Electron IPC surface, and component tree encode behavior that has to be
redesigned as Go controllers and view functions. Copying JS structure into Go
would produce a slow, awkward program.

### Recommended shape

- **UI:** MyGo native UI (`ui.View` functions on the main thread).
- **Terminal:** `github.com/egoist/mygo/plugins/terminal` for local shells, SSH,
  serial, telnet, mosh, and EternalTerminal.
- **SSH:** `golang.org/x/crypto/ssh` v0.58.0.
- **SFTP:** `github.com/pkg/sftp` v1.13.11.
- **Persistence:** MyGo's SQLite plugin (`plugins/sqlite`, purego, no cgo) with
  versioned migrations.
- **Secrets:** `mygo.Secrets` for a master key and OAuth tokens (2560-byte limit),
  plus AES-256-GCM for vault fields at rest; Argon2id fallback for a passphrase.
- **Architecture:** pure `domain`, explicit `ports`, `application` use cases,
  `adapters` for I/O, `ui` for presentation, `app` as composition root.
- **Plugins:** compiled-in built-ins first; out-of-process JSON-RPC host with an
  optional webview UI later. Do not assume the Electron `.ncpkg` runtime is
  reusable (it is not, directly).
- **Migration:** vertical slices, starting with a walking skeleton that opens a
  local shell and persists a host list. Do not wait for “the new architecture” to
  be perfect before proving the terminal and SSH path.

### Go / no-go gates before committing to the full rewrite

Run these in the first two weeks:

1. **Terminal throughput and latency.** MyGo publishes UI benchmarks but no
   terminal throughput numbers. Run `BenchmarkOutput` and `BenchmarkFrame` in
   `plugins/terminal/bench_test.go` on target hardware, including a 1600x1000
   window at scale 2 and a `yes`-style flood. Add an SSH round-trip latency test.
2. **IME and font behavior.** Test Chinese/Japanese/Korean composition, dead keys,
   Option-as-Meta on macOS, Windows IME, and a ligature-heavy font in the terminal.
3. **SSH compatibility.** Test password, public key, certificate, agent,
   keyboard-interactive/MFA, jump host, SOCKS/HTTP proxy, and old network gear.
   Confirm which algorithms are missing from `x/crypto/ssh`.
4. **Plugin webview.** Prove one plugin panel can run in `ui.WebView` with a Go
   backend, no Node integration, and a permission-checked message bridge.
5. **Data export.** Build a one-time exporter in the archived Electron app. Do not
   bet on parsing Chromium's LevelDB in Go (see section 5).

If gate 1 fails, the fallback is to embed xterm.js in a MyGo `ui.WebView` and keep
the rest native. That is a contingency, not the plan.

### Licensing caution

The original is **GPL-3.0-or-later** (`git show 73d15da7:LICENSE`;
`package.json` has `"license": "GPL-3.0-or-later"`). A rewritten derivative
distributed to users must remain GPL-compatible. MyGo is MIT, Ghostty is MIT, and
`golang.org/x/crypto` is BSD-3-Clause, so they are compatible with a GPL-3.0
application. This is an engineering observation, not legal advice; get counsel if
the intended license differs.

---

## 2. Concrete Go environment setup

### 2.1 Toolchain and prerequisites

| Requirement | Detail |
|---|---|
| Go | **1.27.1** or newer. MyGo's `go.mod` declares `go 1.27.1`; its getting-started guide says Go 1.27+. |
| cgo | Not needed. MyGo defaults `CGO_ENABLED=0`; the terminal and SQLite plugins load prebuilt native libraries with purego. |
| Bun | Not needed for a native-UI app. Only MyGo projects with a TypeScript frontend need Bun. |
| Linux | GTK 3. Native UI needs nothing else; the terminal library requires glibc 2.28+ (Debian 10, Ubuntu 20.04) on amd64/arm64. |
| macOS | macOS 13+ for the terminal library. Set `macos.minimumSystemVersion` to `13.0`. |
| Windows | Windows 10 1809+ (ConPTY) on amd64/arm64. |
| WebView | Only needed if a webview is used: WebKitGTK 4.1 on Linux, WebView2 on Windows (Windows 11 includes it), WKWebView on macOS. |

Sources: `go.mod`, `docs/getting-started.md`, `docs/plugins/terminal.md` in
[egoist/mygo](https://github.com/egoist/mygo).

### 2.2 Module initialization

Recommended module path: `github.com/mslxl/natcatty` (lowercase; adjust if the
canonical remote changes). The module path does not have to match the directory
name.

```sh
go mod init github.com/mslxl/natcatty
go get github.com/egoist/mygo@v0.5.5
go get -tool github.com/egoist/mygo/cmd/mygo@v0.5.5
go mod tidy
```

`go get -tool` requires Go 1.24+ and writes a `tool` directive. This is how MyGo's
own native template pins the CLI (`cmd/mygo/init.go`). The resulting `go.mod`
should look approximately like:

```go
module github.com/mslxl/natcatty

go 1.27.1

tool github.com/egoist/mygo/cmd/mygo

require (
	github.com/egoist/mygo v0.5.5
	golang.org/x/crypto v0.58.0
	github.com/pkg/sftp v1.13.11
	// ... application dependencies
)
```

Notes:

- The `go` line is the **minimum** language/toolchain version. A separate
  `toolchain go1.27.1` line is optional; omitting it lets a newer local toolchain
  build the module. Pin the CI image/toolchain explicitly instead of relying on
  auto-download.
- `tool github.com/egoist/mygo/cmd/mygo` makes `go tool mygo dev`,
  `go tool mygo build`, `go tool mygo vet`, and `go tool mygo generate` run the
  version pinned in `go.mod`.
- Keep a **single Go module** initially. A `go.work` file is only worth adding if
  the plugin SDK or a CLI becomes a separately versioned module; see
  <https://go.dev/ref/mod#go-work-file>.

### 2.3 `mygo.json`

Native-UI projects can use JSON instead of `mygo.config.ts`; no JavaScript is
needed. A sensible starting point:

```json
{
  "name": "Natcatty",
  "identifier": "io.github.mslxl.natcatty",
  "version": "0.1.0",
  "out": "build",
  "resources": "resources",
  "urlSchemes": ["natcatty"],
  "macos": {
    "minimumSystemVersion": "13.0"
  }
}
```

- `identifier` is the service name used by `mygo.Secrets`, and the dev app uses
  `<identifier>.dev`, so development secrets are kept separately.
- `out` defaults to `dist`; MyGo's native template uses `build` to avoid confusion
  with a Vite `dist`.
- `resources` defaults to `resources`; files there are copied into every build.
- `macos.minimumSystemVersion` defaults to `12.0`, but the terminal library needs
  13.0.
- `urlSchemes` is how deep links reach `App.OnOpenURL`; `fileAssociations` is
  available if vault/backup files get a double-click behavior.

Sources: `docs/configuration.md`, `docs/app.md`, `docs/native.md` (Secrets).

### 2.4 Directory tree

```
.
├── main.go                     # minimal: app.New(), App.WhenReady, App.Run
├── go.mod
├── go.sum
├── mygo.json
├── resources/
│   ├── icon.png
│   ├── themes/                 # imported/curated terminal themes
│   ├── mosh/                   # optional: per-platform mosh-client binaries
│   └── et/                     # optional: per-platform EternalTerminal binaries
├── app/                        # composition root; no business rules
│   ├── app.go                  # service construction, lifecycle, Path setup
│   ├── window.go               # NewWindow, StateKey, per-window UI state
│   ├── menu.go                 # application menu, roles, accelerators
│   ├── routes.go               # deep links, file open, single instance
│   └── wiring.go               # controllers -> views
├── ui/                         # native presentation only
│   ├── shell/                  # window chrome, tab strip, workspace splits
│   ├── vault/                  # hosts, keys, identities, snippets, notes
│   ├── terminal/               # pane, tabs, broadcast, status bar, dialogs
│   ├── sftp/                   # dual pane, transfer queue, conflicts, editor
│   ├── forwarding/             # rules, status, logs
│   ├── settings/               # appearance, terminal, AI, system, shortcuts
│   ├── ai/                     # chat, tool cards, provider setup, MCP
│   ├── plugins/                # installed plugins, permissions, plugin panels
│   ├── widgets/                # shared native controls and panel primitives
│   └── theme/                  # app theme, spacing, native style tokens
├── application/                # use cases / controllers
│   ├── vault/
│   ├── sessions/               # TerminalManager, restore, logs
│   ├── connections/            # SSH pool, auth flows, host-key challenges
│   ├── transfer/               # SFTP queue, resume, conflicts
│   ├── forwarding/
│   ├── settings/
│   ├── ai/
│   ├── sync/
│   └── plugins/
├── domain/                     # pure Go; stdlib only
│   ├── model/                  # Host, Key, Identity, Session, Workspace, ...
│   ├── vault/
│   ├── workspace/              # split tree math, focus, tab order
│   ├── session/                # restore payload/versioning
│   ├── sftp/                   # FileInfo, TransferTask, conflict policy
│   ├── forwarding/
│   ├── settings/
│   ├── ai/
│   └── plugin/
├── ports/                      # interfaces implemented by adapters
├── adapters/
│   ├── sshdial/                # x/crypto/ssh: auth, jump, proxy, known hosts
│   ├── sftpx/                  # pkg/sftp + local FS adapter
│   ├── serialx/                # go.bug.st/serial
│   ├── store/                  # MyGo sqlite, migrations, repositories
│   ├── secrets/                # mygo.Secrets + AES-GCM + Argon2id
│   ├── sync/                   # S3, WebDAV, Gist, OneDrive, Google Drive
│   ├── ai/                     # provider HTTP/SSE, external-agent drivers
│   ├── pluginhost/             # out-of-process JSON-RPC host (later)
│   └── platform/               # app-lock/biometrics, shell discovery, OS bits
├── internal/
│   ├── testutil/               # fake SSH/SFTP, temp DB, ui.NewTester helpers
│   └── fakes/
└── cmd/
    └── natcatty-mcp/           # MCP stdio server, replaces the internal tool CLI
```

Do not put `mygo` or `ui` imports in `domain`, `ports`, or `application`. The UI
depends on controllers and domain types; controllers do not know about windows or
widgets.

### 2.5 Build, run, test commands

```sh
# Check the machine's Go, GTK/webview, and toolchain prerequisites
go tool mygo doctor

# Development with rebuild/restart on .go/config/resource changes
go tool mygo dev

# Unit and headless UI tests
go test ./...
go test -race ./...

# Standard Go checks plus MyGo's build-lifetime checks
go vet ./...
go tool mygo vet

# Build packages for the current platform (output in build/)
go tool mygo build

# Cross-platform release builds (from any supported host)
go tool mygo build -platform darwin/universal,windows/amd64,windows/arm64,linux/amd64,linux/arm64
```

MyGo's CLI defaults to `CGO_ENABLED=0`. If a dependency genuinely needs cgo, set
`CGO_ENABLED=1` for `dev`, `build`, and `generate`, and provide a target C
compiler. The recommended stack does not need it.

For tests that instantiate the terminal, the native library is downloaded once
into `<cache>/mygo/natives/` when running under `go run`/`go test`; packaged apps
never download it. `MYGO_GHOSTTY_VT` can point at a locally built library.
`terminal.Load()` can be called first to produce a clear error instead of a window
failure.

### 2.6 Cross-platform packaging

- macOS: `mygo build` produces `.app` and `.dmg`; signing and notarization require
  a macOS runner, `codesign`, and `notarytool`. Universal builds include both
  architectures of `libghostty-vt.dylib`.
- Windows: NSIS installers are produced by the CLI; cross-building from Linux is
  supported, and signing can use `osslsigncode` or a Windows runner with
  `signtool`/`signCommand`.
- Linux: binary, desktop entry, archive, and `.deb`; GTK 3 is required. Terminal
  library requires glibc 2.28+.
- The CLI bundles the right `libghostty-vt` (`dylib`, `.so`, or `.dll`) per
  platform into app resources (`plugins/terminal/mygo-plugin.json`).
- If mosh/ET binaries are shipped, place them in platform resource directories
  (`resources/mosh/darwin-arm64/...`) so a build only includes its target's
  binaries; see `docs/distribution.md` "Platform resources" and "Helper
  executables".

### 2.7 CI outline

```yaml
name: ci
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.27.1'
      - run: sudo apt-get update && sudo apt-get install -y libgtk-3-dev
      - run: go test -race ./...
      - run: go tool mygo vet

  release:
    if: startsWith(github.ref, 'refs/tags/')
    strategy:
      fail-fast: false
      matrix:
        include:
          - { name: macOS,   os: macos-latest,  platform: 'darwin/universal' }
          - { name: Windows, os: ubuntu-latest,  platform: 'windows/amd64,windows/arm64' }
          - { name: Linux,   os: ubuntu-latest,  platform: 'linux/amd64,linux/arm64' }
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.27.1'
      - run: go tool mygo build -platform "${{ matrix.platform }}" -upload
```

`-upload` requires the `updates` section in `mygo.json`/`mygo.config.ts` and the
update signing key; omit it for a plain build. Add `osslsigncode`/NSIS on the
Windows cross-build row, and the macOS certificate and notary credentials to the
macOS row. MyGo's own workflow documentation gives
the exact signing steps (`docs/github-actions.md`). Cache `~/.cache/mygo/natives`
(and the Go module/build caches) to avoid repeated terminal-library downloads.

### 2.8 Runtime files and directories

Use MyGo's paths rather than constructing OS-specific paths:

| MyGo path | Use |
|---|---|
| `PathUserData` | `natcatty.db`, vault backups, session logs, plugin data |
| `PathCache` | thumbnails, downloaded native libraries under dev/test, transfer temp |
| `PathLogs` | structured logs (`log/slog` JSON) |
| `PathResources` | icon, themes, bundled mosh/ET binaries |
| `PathTemp` | short-lived transfer staging |
| `PathExe`, `PathHome`, user folders | shell discovery, file dialogs |

`App.Path` creates user-data/cache/log directories on first use. `App.SetName`
affects `PathUserData`, so set the name before calling `Path` or starting the app.
Never use `os.TempDir()` directly for app files; use `PathTemp`.

### 2.9 Suggested first dependencies

Versions verified against `proxy.golang.org` on 2026-10-19:

| Purpose | Module | Version |
|---|---|---|
| UI/framework | `github.com/egoist/mygo` | v0.5.5 |
| SSH | `golang.org/x/crypto` | v0.58.0 |
| SFTP | `github.com/pkg/sftp` | v1.13.11 |
| Serial | `go.bug.st/serial` | v1.8.0 |
| SQLite (if not using MyGo plugin) | `modernc.org/sqlite` | v1.60.1 |
| MCP | `github.com/modelcontextprotocol/go-sdk` | v1.8.0 |
| Keychain (CLI only) | `github.com/zalando/go-keyring` | v0.2.8 |
| SOCKS5 | `github.com/things-go/go-socks5` | v0.1.3 |
| S3 | `github.com/aws/aws-sdk-go-v2/service/s3` | v1.114.3 |
| WebDAV | `github.com/studio-b12/gowebdav` | v0.13.0 |
| GitHub API/Gist | `github.com/google/go-github/v74` | v74.0.0 |
| OAuth2 | `golang.org/x/oauth2` | v0.37.0 |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` | v1.82.0 |
| OpenAI | `github.com/openai/openai-go/v3` | v3.81.0 |
| Gemini | `google.golang.org/genai` | v1.75.0 |

Use the MyGo SQLite plugin first; it is already bundled and avoids a second
SQLite native library. Keep `modernc.org/sqlite` as a fallback only if ATTACH,
extensions, or a different SQL surface becomes necessary.

---

## 3. Capability assessment of MyGo native UI

### 3.1 What is supported

**Application and window integration**

- Native windows with `mygo.NewWindow(mygo.WindowOptions{...})`, native `Content`
  view, minimum/maximum size, title bar styles, window state persistence
  (`StateKey`), multiple windows, and per-window UI state.
- Menus and roles (`mygo.NewMenu`, `RoleAppMenu`, `RoleEditMenu`, accelerators,
  checkbox/radio items, context menus, Dock menu, tray menus).
- Single-instance lock, deep links (`App.OnOpenURL`), file associations, recent
  documents, start-at-login, Dock/taskbar badges and jump lists.
- Dialogs, notifications, clipboard (text/HTML/PNG), opening URLs/paths in the
  system shell, screen/display info, dark-mode and accent preferences, power
  events, network status, file watching, global shortcuts.
- `mygo.Secrets` backed by macOS Keychain, Windows Credential Manager, or Linux
  Secret Service, with a 2560-byte per-secret limit. `Secrets.Service(name)`
  selects another service, which lets a CLI share the app's secrets.

**Native widget set (relevant subset)**

- Layout: row/column/grid, padding, gaps, alignment, sizing, scroll containers,
  nested split views (binary split; nest for N panes).
- Collections: `ui.List` (virtualized; builds only rows in view), `ui.Table`,
  `ui.Tree`, `ui.Outline`, `ui.GridView`, with selection, keyboard navigation,
  reordering, and accessibility semantics.
- Navigation: tabs, sidebar/source list, breadcrumbs, back/forward.
- Input: single-line and multiline text with selection, undo, clipboard, IME;
  password fields; number, search, combo, autocomplete, token fields; find bar;
  rich text editor; code editor with highlighting, multiple carets, folding,
  completions, diagnostics.
- Documents: Markdown rendering (including streaming), diff viewer.
- Dialogs and status: dialog, alert, popover, tooltip, toast, progress, spinner,
  meter, badge.
- Charts: scales, axes, bars, lines, areas, stacks, donuts, selection.
- Images: bitmap, SVG, icons, avatars.
- Drag and drop: internal reordering plus system drag/drop of files, text, URLs,
  HTML, PNG, and custom formats; file drops from other apps.

**Terminal plugin**

`github.com/egoist/mygo/plugins/terminal` is a terminal for native UI backed by
libghostty-vt:

- Runs a program in a PTY (`Options.Command`, `Dir`, `Env`) or connects to any
  `io.ReadWriteCloser` (`Options.Conn`) such as an SSH channel or serial port.
  A `Conn` that implements `Resize(cols, rows int) error` is told the terminal
  size.
- Colors (16/256/24-bit), bold/italic/faint/inverse/strikethrough/underline
  variants, Unicode wide chars/emoji/grapheme clusters, drawn box drawing and
  Powerline separators, font fallback.
- Scrollback that reflows on resize (default 10 MiB; `Options.Scrollback`),
  alternate screen, synchronized output.
- Keyboard: legacy sequences, xterm `modifyOtherKeys`, Kitty keyboard protocol,
  IME composition at the cursor.
- Mouse reports, focus reports, bracketed paste, OSC 8 hyperlinks, title (OSC 0/2),
  working directory (OSC 7), OSC 52/5522 clipboard, OSC 9/777 notifications.
- Selection with Ghostty-like behavior, copy/paste, configurable copy-on-select,
  right-click actions, links, font zoom, themes, cursor styles.
- Fonts: family list, size, weight, OpenType features (including `calt`/`liga`
  ligatures), line height, thickening.
- Methods: `Send`, `Paste`, `Feed`, `Text`, `Snapshot`, `Resize`, `Close`,
  `SetFont`, `SetTheme`, `Done`, `ExitCode`, `Title`, `Dir`, `Size`.
- `Snapshot()` returns styled escape sequences for the scrollback and screen; a
  new terminal of the same size fed that snapshot shows the same content. This is
  the basis for session restore/hibernate.
- `ui.NewTester` can type into a terminal and read `Text()` without a window.

The plugin also ships `BenchmarkOutput` and `BenchmarkFrame` in
`plugins/terminal/bench_test.go`, though no results are published in the docs.

### 3.2 Native UI performance evidence

MyGo's published CPU benchmark table (Apple M5, Go 1.27.1, `GOMAXPROCS=1`, median
of six 300 ms samples) reports:

| Fixture | Time per frame | Allocs |
|---|---:|---:|
| 1,000 static labels | 397 us | 0 |
| Visible rows of a million-row list | 14.5 us | 20 |
| Diff view with no changes | 389 us | 2 |
| 50 KB Markdown reply, whole | 1.3 ms | 353 |
| Same Markdown streaming, 16 bytes/frame | 0.8 ms | 350 |

Retained heap for 1,000 labels is roughly 2.5 MB. These are CPU-renderer,
headless measurements and explicitly do not compare MyGo to another framework.
They are good enough to justify a prototype, not enough to promise parity with
xterm.js + WebGL. The terminal benchmarks must be run on target hardware.

### 3.3 Gaps and missing APIs that matter to Netcatty

These are the concrete blockers found by reading the terminal plugin source and
docs. They should drive the go/no-go decisions.

| Original feature | MyGo terminal status | Consequence / mitigation |
|---|---|---|
| xterm `addon-image` inline images | **Not supported.** Kitty graphics images are explicitly refused (`plugins/terminal/internal/vt/terminal.go`: "Images of the Kitty graphics protocol are not drawn: refuse them"). No sixel/iTerm image API is documented. | Inline image previews in the terminal are lost. Use an external viewer or a native image pane; do not promise xterm parity. |
| xterm `addon-search` | **No search API.** No search method or scroll-to-match in `Options`/`Terminal`/`View`. `Text()` returns the joined screen+scrollback text, which can support a crude text search but not highlighting or scrolling to a match. | Add a native find bar that searches `Text()` and reports matches without scroll-to; or contribute a search API upstream. |
| Selection getter for AI “explain selection” | **No exported selection getter.** `SelectionText()` exists on the internal VT terminal; the public `Terminal` exposes copy/select-all actions and `CopyOnSelect`, but not the selected string. | Read the clipboard after a copy action, or contribute `SelectionText()` upstream. |
| Cursor geometry for overlays | **No exported cursor rect/cell geometry.** `view.caret()` and `vt.Geometry` are internal. | Autocomplete popups, timestamp gutters, and keyword-highlight overlays cannot be positioned at the cursor. Implement them as fixed bars, or add API upstream. |
| Keyword highlighting / cell styling | No public API to style existing cells or map screen coordinates to cells. | Defer; could fork the plugin and add a cell-style hook. |
| Raw output tap for logs, ZMODEM/YMODEM, broadcast | **No `OnOutput` callback** in `Options`. | For SSH/serial/telnet, wrap `Conn.Read` and tee bytes. Local-shell sessions use the plugin's internal PTY and have no public raw-output tap; use periodic `Snapshot()`/`Text()` for logs, or run local shells through a separate PTY adapter. |
| Broadcast input across panes | One terminal per `*terminal.Terminal`; one view per terminal. | Tee `Conn.Write` bytes to peer sessions for SSH/serial. Local PTY sessions need an input encoder or a separate PTY adapter. |
| Observer/mirror popup for one session | “A terminal shows in one view at a time.” | A second mirror terminal can be fed a snapshot and subsequent output, but input routing and scrollback must be managed by the app. Not free. |
| Session logging for local shells | No output hook. | Snapshot/text logging, or custom PTY. |
| SSH compression | `x/crypto/ssh` supports only compression `none` (`supportedCompressions = []string{"none"}`). | The original `compress` algorithm option cannot be honored. Document the regression or use a different SSH implementation (none is as mature). |
| FIDO/security-key SSH auth (`ED25519-SK`, `ECDSA-SK`) | In `x/crypto/ssh` v0.58.0 there are no SK signature algorithms; the package cannot itself sign with a FIDO key. An external agent may still hold the key. | Use the system OpenSSH agent/helper or a third-party FIDO library; mark as phase 4. |
| X11 forwarding | No `Session.RequestX11`; must send `x11-req` via `Session.SendRequest` and accept server-opened `x11` channels with `Client.HandleChannelOpen("x11")`. | Implement manually in a later phase; test carefully. |
| SSH legacy algorithms | `x/crypto/ssh` supports a modern subset; some algorithms offered by ssh2 are absent. | Audit old network-device users; document unsupported hosts. |
| Custom CSS | Native UI has no CSS. | Rebuild styling with native style tokens; custom CSS setting cannot be preserved 1:1. |
| App lock / biometrics | No biometric API in MyGo's documented native APIs. | Passphrase + OS keychain first; platform-specific LocalAuthentication/Windows Hello/fprintd later. |
| Dynamic third-party plugins | MyGo “plugins” are compiled into the app (Go services plus an optional JS package for pages) and can declare native libraries; there is no documented dynamic loader for third-party plugin packages. | Build an out-of-process host; see section 4.13. |
| Plugin UI sandbox/partition | `ui.WebView` exists, but docs do not describe per-webview session partitions or a Node-free sandbox equivalent to Electron's. | Use one webview per plugin, no Node, strict CSP/origin checks, and a permission-checked message bridge; validate before shipping. |

### 3.4 Webview assessment and recommended use

MyGo supports two webview modes:

1. **Whole-window webview:** `mygo.NewWindow(WindowOptions{URL: ...})` or
   `WindowOptions{Page: ...}`; this is the web-frontend mode. `Content` is the
   native-UI alternative, and `URL`/`Page` are ignored when `Content` is set.
2. **Embedded webview in native UI:** `Window.NewWebView(WebViewOptions{URL,
   Page, Transparent, BackgroundColor})` returns `*mygo.WebView`, then
   `ui.WebView(c, wv)` places it in a native layout. The window must show
   `Content`; `NewWebView` fails for a window already showing a page. Native UI
   can be painted above the webview (menus, dialogs). `WebView.Page()` exposes
   navigation, zoom, user agent, DevTools, capture, print, and typed bindings
   (`mygo.Bind`/`mygo generate`).

Recommendation:

- **Use webviews for third-party plugin panels only**, plus an optional OAuth
  fallback if opening the system browser is unacceptable. Prefer the system
  browser with a loopback redirect for OAuth; it is safer and avoids embedded
  webview policy issues.
- **Do not use a webview for the terminal** unless the native terminal fails the
  go/no-go gate. A webview terminal would reintroduce xterm.js, a JS bridge, and
  much of the original renderer complexity.
- **Do not use a webview for core vault/SFTP/settings/AI screens.** Native widgets
  cover them, and native keeps keyboard/focus/accessibility coherent.

Webview limits to design around:

- Windows WebView2 keeps page elements in a separate visual tree; the docs note
  limitations for overlays. Native UI painted over a webview works, but do not
  assume arbitrary per-element composition.
- No documented per-webview cookie/session partition. For multiple plugin
  instances or accounts, verify isolation before relying on it.
- Plugin webviews must have no Node integration equivalent. Expose only a narrow
  Go API; validate every message; scope permissions per plugin and per resource.

---

## 4. Proposed Go package organization

### 4.1 Dependency direction

```
main
  └── app (composition root: constructs everything)
        ├── ui (native views; imports domain + controller interfaces)
        ├── application (use cases; imports domain + ports)
        ├── adapters/* (I/O; imports domain + ports + third-party libs)
        └── ports (interfaces; imports domain)
              └── domain (pure types and rules; stdlib only)
```

Rules:

- `domain` must not import `mygo`, `ui`, `ssh`, `sqlite`, or `net/http`.
- `ports` defines interfaces in terms of domain types and stdlib types (`context`,
  `io`, `time`). Keep interfaces small and consumer-owned.
- `application` orchestrates; it does not render and does not know window handles.
- `ui` reads immutable snapshots and calls controller methods. It may depend on
  `mygo/ui`, `domain`, and small controller interfaces; it must not import
  `adapters`.
- `adapters` translate external errors into domain errors and never call UI.
- `app` is the only package that knows concrete adapters and windows.
- No package imports `app` or `ui` except `main`/`app`.

If a cycle appears, move the shared types into `domain` and the interface into
`ports`; do not merge layers.

### 4.2 Mapping from the Electron layers

| Original at 73d15da7 | New Go package(s) | Responsibility |
|---|---|---|
| `electron/main.cjs`, `electron/main/registerBridges.cjs` | `app/` | Lifecycle, service construction, IPC replacement, single instance, protocol handlers. |
| `electron/preload.cjs`, `electron/preload/api.cjs` | `app/`, `ui/` | No preload. UI calls controllers directly; `mygo.Bind` only for webview plugin pages. |
| `electron/bridges/sshBridge.cjs`, `sshConnectionPool.cjs` | `adapters/sshdial`, `application/connections` | Dial, auth, host keys, jump/proxy, keepalive, pooled `*ssh.Client`. |
| `electron/bridges/sftpBridge.cjs` | `adapters/sftpx`, `application/transfer` | SFTP/SCP operations, browse, transfers, resume, conflicts. |
| `electron/bridges/terminalBridge.cjs`, `terminalWorkerManager.cjs`, `electron/terminalWorker/` | `application/sessions`, `ui/terminal` | Session ownership, local/SSH/serial/telnet/mosh/ET, snapshots, restore, broadcast, logs. |
| `electron/bridges/portForwardingBridge.cjs`, `x11Forwarding.cjs` | `application/forwarding`, `adapters/sshdial` | Local/remote/dynamic forwards, status, reconnect; X11 later. |
| `electron/bridges/credentialBridge.cjs` | `adapters/secrets` | OS credential storage and encryption/decryption. |
| `electron/bridges/vaultBackupBridge.cjs` | `application/vault` | Encrypted backup/export/import. |
| `electron/bridges/cloudSyncBridge.cjs` + OAuth bridges | `adapters/sync`, `application/sync` | S3/WebDAV/Gist/OneDrive/Google Drive, encryption, conflict handling. |
| `electron/bridges/aiBridge.cjs`, `electron/bridges/aiBridge/*`, MCP bridges | `application/ai`, `adapters/ai`, `cmd/natcatty-mcp` | Providers, streaming, agent harness, tools, external-agent drivers, MCP client/server. |
| `electron/plugins/browserPluginRuntime.cjs`, `utilityPluginRuntime.cjs`, `pluginManager.cjs` | `adapters/pluginhost`, `application/plugins`, `ui/plugins` | Dynamic host, permissions, contributions, plugin UI; see 4.9. |
| `domain/models.ts`, `domain/models/*` | `domain/model` | Host, SSHKey, Identity, Snippet, VaultNote, Workspace, PortForwardingRule, SftpFileEntry, TransferTask, KnownHost, ConnectionLog, settings. |
| `domain/workspace.ts`, `domain/sidePanelLayout.ts` | `domain/workspace` | Split-tree prune/insert/append, sizes, focus, tab order. |
| `domain/sessionRestore.ts` | `domain/session` | Restore payload/versioning; same JSON shape can be imported. |
| `application/state/useVaultState.ts` | `application/vault`, `ui/vault` | Persistence, optimistic updates, secret hydration, import/export. |
| `application/state/useSessionState.ts` | `application/sessions`, `ui/shell` | Tabs, workspaces, focus, broadcast, logs, restore. |
| `application/state/useSettingsState.ts` | `application/settings`, `ui/settings` | Typed settings, defaults, migrations, sync. |
| `application/state/*` other hooks/stores | `application/*` + `ui/*/state.go` | Use-case controllers and per-window view state. |
| `infrastructure/config/storageKeys.ts` | `domain/settings`, `adapters/store` | 194 storage keys become typed settings fields/tables, not stringly-typed keys. |
| `infrastructure/persistence/localStorageAdapter.ts` | `adapters/store` | SQLite repositories and migrations. |
| `infrastructure/services/*` | `application/*`, `adapters/sync`, `adapters/secrets` | Cloud sync, encryption, forwarding, host browser, vault import. |
| `infrastructure/ai/harness/*` | `application/ai/harness` | Agent runtime, context/compaction, tool loop, trace. |
| `components/terminal/*` | `ui/terminal` | Native pane, tabs, status, dialogs, find bar, context menu, compose bar. |
| `components/sftp/*` | `ui/sftp` | Dual pane, transfer queue, conflicts, editor sync. |
| `components/settings/*` | `ui/settings` | Settings sections. |
| `components/vault/*`, `components/port-forwarding/*`, `components/ai*` | `ui/vault`, `ui/forwarding`, `ui/ai` | Native views. |
| `components/ui/aside-panel.tsx` design system | `ui/widgets` | Shared panel/layout primitives. |
| `packages/plugin-contract/schema/plugin-contract.schema.json` | `domain/plugin`, `adapters/pluginhost/contract` | Wire/manifest types; keep the JSON Schema as canonical if compatibility matters. |
| `electron/cli/netcatty-tool-cli.cjs`, `electron/mcp/*` | `cmd/natcatty-mcp` | MCP stdio server and app-controlled tools. |

### 4.3 Domain package

Keep models close to the original but make them Go-native:

```go
type Host struct {
	ID            string
	Label         string
	Hostname      string
	Port          uint16
	Username      string
	IdentityID    string
	Group         string
	Tags          []string
	Protocol      Protocol // ssh, telnet, mosh, et, local, serial, plugin
	AuthMethod    AuthMethod
	ProxyProfile  string
	JumpHostIDs   []string
	Algorithms    AlgorithmOverrides
	Keepalive     Keepalive
	Env           []EnvVar
	Startup       []string
	// ...
}

type WorkspaceNode struct {
	ID       string
	Type     NodeType // pane or split
	Session  string
	Dir      SplitDirection
	Children []WorkspaceNode
	Sizes    []float32
}
```

Port the pure workspace functions from `domain/workspace.ts` almost directly:
`pruneWorkspaceNode`, `appendPaneToWorkspaceRoot`, `insertPaneIntoWorkspace`,
`collectSessionIds`, and the side-panel minimum-size math. They are deterministic
and should have table-driven Go tests. Do not port React dependencies.

Session restore (`domain/sessionRestore.ts`) is already a versioned JSON payload.
Define a Go struct with the same version and fields; import old payloads, then
write the new format. Include `requireFreshConnection`, `restoreState`, workspace
trees, tab order, and per-session overrides.

### 4.4 Ports

Define only what consumers need:

```go
type SSHConnector interface {
	Dial(ctx context.Context, req ConnectRequest) (Conn, error)
}

type Conn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error // optional in terminal, but useful here
}

type FileSystem interface {
	ReadDir(ctx context.Context, path string) ([]domain.FileInfo, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	Stat(ctx context.Context, path string) (domain.FileInfo, error)
	Lstat(ctx context.Context, path string) (domain.FileInfo, error)
	MkdirAll(ctx context.Context, path string, mode os.FileMode) error
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
}
```

Other ports: `VaultStore`, `SettingsStore`, `SecretStore`, `SessionManager`,
`TransferQueue`, `Forwarder`, `SyncProvider`, `AIProvider`, `AgentRunner`,
`PluginHost`, `Clock`, `EventSink`.

Interfaces should be defined in the package that consumes them, or centrally in
`ports` if that avoids duplication. Keep them 3-6 methods.

### 4.5 Application layer and threading

MyGo's threading rules are explicit (`docs/app.md`): every MyGo function is safe
from any goroutine, but view functions, event listeners, menu clicks, and
global-shortcut callbacks run on the **main thread**, while bound methods run on
their own goroutines. Slow work belongs in goroutines.

Recommended pattern:

- Each window owns a `ShellState` (tabs, workspace tree, focus, dialogs,
  selections) that is only mutated on the main thread.
- Controllers expose methods safe from any goroutine. They perform I/O in
  goroutines and publish immutable snapshots.
- A controller receives an `invalidate func()` from `app`, implemented as
  `win.Update(func(){ ... })` (docs: `Window.Update` runs a function on the main
  thread; `Window.Invalidate` only redraws). Coalesce bursts of progress events
  (e.g. transfer bytes, terminal output) to one invalidation per frame.
- Never store `ui.Element`, `ui.Handle`, or build-pass values in controllers or
  domain state. `go tool mygo vet` catches violations.
- Use `mygo.RunOnMain` only for native handles that require it; prefer
  `Window.Update`.

Example controller boundary:

```go
type SessionController struct {
	mu      sync.RWMutex
	view    SessionSnapshot
	changed func() // app wires this to win.Update
}

func (c *SessionController) Snapshot() SessionSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.view // immutable value
}

func (c *SessionController) Connect(ctx context.Context, hostID string) {
	// I/O in this goroutine; update snapshot under lock; then c.changed()
}
```

### 4.6 Terminal session design

The terminal plugin's `Conn` abstraction is the central design seam.

```go
type Session struct {
	ID     string
	Kind   SessionKind // local, ssh, serial, telnet, mosh, et, plugin
	Term   *terminal.Terminal
	Conn   Conn        // nil for local Command sessions
	Cancel context.CancelFunc
	Meta   SessionMeta
}
```

- **SSH:** `ssh.Session` + `RequestPty` + `WindowChange` wrapped as `Conn`.
  `terminal.New(terminal.Options{Conn: conn, Font: ..., Theme: ...})`.
- **Local shell:** `terminal.Options{Command: []string{shell, "-l"}, Dir: ...,
  Env: ...}`. The plugin owns the PTY; do not add a second PTY layer.
- **Serial:** `serial.Open` returns an `io.ReadWriteCloser`; wrap it as `Conn`.
- **Telnet:** `net.Dial` plus NVT option negotiation, wrapped as `Conn`.
- **mosh/ET:** run the bundled `mosh-client`/`et` binary as
  `Options.Command`, with `MOSH_KEY`/locale env after the SSH bootstrap. This is
  the same architecture as the original (`electron/bridges/moshHandshake.cjs`
  comments describe the handshake and the PTY swap).
- **Plugin protocol:** an RPC stream exposed as a `Conn`; `Resize` becomes an RPC.

Resize order is a common bug: `x/crypto/ssh` uses `RequestPty(term, rows, cols,
modes)` and `WindowChange(rows, cols)`, while the terminal plugin uses
`Resize(cols, rows)`. The adapter must swap them:

```go
func (c *sshConn) Resize(cols, rows int) error {
	return c.session.WindowChange(rows, cols)
}
```

Session restore:

- On graceful shutdown, call `term.Snapshot()` and store it with session metadata.
- On restore, create a terminal with `Conn` or `Command`, then `Feed(snapshot)`
  before attaching the view. The snapshot includes styles, cursor, and modes.
- `Text()` joins wrapped lines; use it for search/export, not for restore.
- One terminal has one view at a time. For a mirror popup, create a second
  terminal, feed it the snapshot and subsequent output; route input deliberately.
- Broadcast input by wrapping the active `Conn.Write` and copying bytes to peer
  `Conn`s. For local `Command` sessions there is no public raw-output/input tap;
  either accept no broadcast initially or run local shells through a custom PTY.

Logging:

- For `Conn`-based sessions, tee `Read` bytes into a bounded log writer. Store
  metadata in SQLite and compressed logs under `PathUserData/logs`.
- For local `Command` sessions, snapshot/text logging is the initial option. Do
  not claim raw byte-accurate logs until an output tap exists.

### 4.7 SSH connection and authentication design

`golang.org/x/crypto/ssh` provides the required primitives:

- `ssh.Dial` or `ssh.NewClientConn` over a custom `net.Conn`.
- `ssh.ClientConfig{User, Auth, HostKeyCallback, HostKeyAlgorithms, Timeout,
  BannerCallback}`.
- Auth methods: `ssh.Password`, `ssh.PublicKeys`, `ssh.PublicKeysCallback`
  (agent), `ssh.KeyboardInteractive`, `ssh.RetryableAuthMethod` for multi-round
  MFA, `ssh.CertSigner` for certificates.
- `ssh/agent` to talk to `SSH_AUTH_SOCK`; `agent.ForwardToAgent` and
  `agent.RequestAgentForwarding` for agent forwarding.
- `ssh/knownhosts` for `known_hosts` files, including hashed entries.
- `ssh.FingerprintSHA256` for UI display.
- `Client.Dial`/`Client.Listen` for jump hosts and forwarding.
- `Session.RequestPty`, `Shell`, `Start`, `Setenv`, `WindowChange`, `Signal`,
  `Wait`, `ExitError`.

Design:

```go
type ConnectRequest struct {
	Host        domain.Host
	Auth        domain.AuthMaterial
	Proxy       *domain.ProxyConfig
	Jump        []domain.Host
	Algorithms  domain.AlgorithmOverrides
	Keepalive   domain.Keepalive
	HostKeyMode domain.HostKeyMode
}
```

Dial order:

1. Direct TCP, SOCKS5/HTTP CONNECT, or `ProxyCommand` (`exec.Command` with pipes).
2. For each jump host, dial and `ssh.NewClientConn`, then
   `client.Dial("tcp", nextAddr)` to reach the next hop.
3. Build `ClientConfig` with ordered auth methods and a host-key callback.
4. `ssh.NewClientConn(conn, addr, config)` then `ssh.NewClient(conn, chans,
   reqs)`.
5. Start a keepalive goroutine using
   `client.SendRequest("keepalive@openssh.com", true, nil)`; count failures
   against `KeepaliveCountMax`; cancel on close.
6. Expose `OpenTerminal`, `OpenSFTP`, and `OpenForward` from the pooled client.

Host-key handling is asynchronous from the UI's perspective but synchronous in
the SSH handshake. The callback can emit a `HostKeyChallenge` to the controller
and block on a decision channel; the UI resolves it on the main thread. Never
default to `InsecureIgnoreHostKey`.

Pooling:

- One `*ssh.Client` supports many channels/sessions; no per-terminal transport is
  needed.
- Key the pool by endpoint + auth identity + proxy/jump chain + algorithm
  overrides. Reference-count terminals, SFTP clients, and forwards; park idle
  transports for a configurable TTL; close on auth change.
- Reconnect policy is application-level: create a new `Conn`, replay startup
  commands only if the user opted in, and never silently re-run destructive
  commands.

Known limitations to encode in the UI:

- Compression is not supported by `x/crypto/ssh` (only `none`).
- `x/crypto/ssh` cannot sign with FIDO SK keys itself; use the system
  agent/helper, a third-party library, or defer.
- Some legacy key-exchange/cipher/host-key algorithms may be unavailable; per-host
  algorithm overrides map to `Config.KeyExchanges`, `Config.Ciphers`,
  `Config.MACs`, and `ClientConfig.HostKeyAlgorithms`.
- X11 requires custom `x11-req` handling.

### 4.8 SFTP and transfer design

Use one `FileSystem` interface for local and remote panes. `pkg/sftp` provides:

- `sftp.NewClient(sshClient, ...)` and `sftp.NewClientPipe(reader, writer)`.
- `ReadDir`, `Open`, `Create`, `Stat`, `Lstat`, `MkdirAll`, `RemoveAll`,
  `Rename`, `Walk`.
- `File.ReadFrom`, `ReadFromWithConcurrency`, `WriteTo`.
- Options: `UseConcurrentReads(true)` (default), `UseConcurrentWrites(true)`,
  `MaxPacket`, `MaxConcurrentRequestsPerFile`.

Transfer engine:

- A queue with per-task context, bounded worker pool, progress coalescing, pause
  and cancel.
- Resume by offset/checkpoint; for directories, persist a manifest and completed
  entry count. The original models already define resume checkpoints; port the
  semantics, not the exact file format.
- Conflict policy: stop, skip, replace, duplicate, merge (from
  `domain/models/sftp.ts`).
- Local↔remote, remote↔remote (via local staging or streaming through the app),
  and local↔local copies.
- Integrity: optional SHA-256; do not compute a full digest by default for large
  files. The original has digest/verify features; make them opt-in.
- SCP fallback: `pkg/sftp` does not implement SCP. Either implement the SCP wire
  protocol over an SSH session or shell out to the system `scp`; mark as optional
  compatibility work.
- Filename encoding: `pkg/sftp` treats names as strings/bytes. Use
  `golang.org/x/text/encoding/simplifiedchinese` and a configured decoder for
  GB18030/legacy servers, as the original did.
- Compression: the original supports compressed upload. Use `compress/gzip` or
  `klauspost/compress/zstd` with a staged temp file; do not stream untrusted
  archives into a remote path without limits.

UI:

- `ui.Split` for dual panes; `ui.List`/`ui.Table` for entries; `ui.Breadcrumbs`;
  `ui.FindBar`; context menus; `ui.Drag`/`ui.DropData` for internal and system
  drag/drop.
- `ui.CodeEditor` for remote file editing; `ui.Markdown` for preview.
- Transfer queue as a dedicated panel with progress, speed, ETA, conflict
  dialogs, and a global transfer center.
- Dragging files out of the app requires existing paths per MyGo's drag docs
  (lazy providers cannot create promised files), so download to a temp path first
  when dragging remote files to the OS.

### 4.9 Port forwarding design

- Local forward: `net.Listen` on `bindAddress:localPort`; for each accepted
  connection, `sshClient.Dial("tcp", remoteHost:remotePort)` and bidirectional
  `io.Copy`.
- Remote forward: `sshClient.Listen("tcp", bindAddress:remotePort)`; for each
  accepted connection, dial the local target and copy.
- Dynamic: a SOCKS5 server; `things-go/go-socks5` can be configured with a custom
  dialer that uses `sshClient.Dial`. Or implement SOCKS5 directly for tighter
  control.
- Supervisor: per-rule state machine (`inactive`, `connecting`, `active`,
  `error`), auto-start, bounded reconnect with backoff, and status events to the
  UI. Do not persist runtime status.
- X11: later phase; send `x11-req` with `Session.SendRequest`, accept
  server-opened `x11` channels with `Client.HandleChannelOpen("x11")`, and rewrite
  the Xauthority cookie as the original does. This is not a drop-in API.

### 4.10 Vault, secrets, and persistence

**SQLite**

Open the database in `PathUserData` (create the directory first):

```go
dir, _ := mygo.App.Path(mygo.PathUserData)
db, err := sqlite.Open(ctx, filepath.Join(dir, "natcatty.db"), sqlite.OpenOptions{})
```

The MyGo SQLite plugin:

- Is purego, no cgo, and bundled by the CLI.
- Supports `Execute`, `Query`, and `Transaction` (BEGIN IMMEDIATE batches).
- Rejects `ATTACH`, `DETACH`, `VACUUM INTO`, transaction-control statements in
  user SQL, and extension loading.
- Returns buffered results; use `LIMIT`/pagination for large tables.
- Connections are safe from any goroutine; close them on shutdown.

Use `PRAGMA journal_mode=WAL`, `PRAGMA foreign_keys=ON`, and `PRAGMA
busy_timeout` during initialization (`Execute` for non-row-returning pragmas,
`Query` for row-returning ones such as `journal_mode`). Keep migrations in
`adapters/store/migrations` as ordered Go strings or embedded SQL with a
`schema_version` table.

Suggested tables (names illustrative):

- `hosts`, `host_groups`, `ssh_keys`, `identities`, `snippets`,
  `snippet_packages`, `notes`, `note_groups`, `proxy_profiles`, `known_hosts`,
  `port_forward_rules`, `connection_logs`, `command_history`, `workspaces`,
  `session_restore`, `settings`, `custom_themes`, `ai_providers`, `ai_sessions`,
  `ai_messages`, `plugins`, `plugin_settings`, `sync_accounts`, `managed_sources`.
- Store secrets as ciphertext columns (or a separate `secrets` table keyed by
  record/field); never store plaintext passwords, private keys, or tokens.
- Store terminal scrollback/log blobs as compressed files, not in SQLite rows.
  Keep only metadata and file paths in the DB.
- Store `session_restore` as a versioned JSON blob if normalization adds no value;
  it is already a document.

**Secret strategy**

- Generate a random 32-byte vault master key. Store it with
  `mygo.Secrets.Set("vault-master-key", key)`. `mygo.Secrets` uses the app
  identifier as the service, and the dev app uses `<identifier>.dev`.
- Encrypt sensitive fields with AES-256-GCM. Use a fresh 12-byte nonce per
  encryption and AAD containing table, record ID, and field name, so ciphertexts
  cannot be swapped between rows.
- For a passphrase-protected vault, derive a key with Argon2id
  (`golang.org/x/crypto/argon2`). Store version, salt, memory, iterations, and
  parallelism alongside the ciphertext. Do not store the passphrase.
- OAuth refresh tokens and API keys: prefer `mygo.Secrets` if under 2560 bytes;
  otherwise encrypt them in the DB with the master key.
- On Linux without a Secret Service, `mygo.Secrets` returns an error matching
  `errors.ErrUnsupported`. Fall back to a passphrase prompt or an explicitly
  insecure session-only mode; never silently write a plaintext key.
- App lock clears decrypted keys from memory, locks the UI, and re-prompts.
  Biometric unlock needs platform-specific work.

**Settings**

- Replace the 194 `STORAGE_KEY_*` constants with typed structs grouped by domain
  (`TerminalSettings`, `AppearanceSettings`, `AISettings`, `SyncSettings`, ...).
- Persist a JSON document per settings group, or normalized key/value rows with a
  typed decoder. Version each group; migrations are explicit.
- Keep defaults in Go and expose a “reset section” operation.
- Do not store runtime-only fields (transfer status, connection state) in the
  settings store.

**Import/export**

- Export a versioned JSON document containing hosts, keys (encrypted or
  passphrase-protected), identities, snippets, notes, settings, themes, forwarding
  rules, known hosts, and AI provider configuration (tokens redacted or
  re-encrypted).
- Import validates and migrates. The original already has vault import/export and
  encrypted backups; preserve the user-facing concept, not necessarily the exact
  file format.

### 4.11 AI and MCP

Keep AI optional at the package level so the core terminal app can ship without
provider SDKs.

- `ports.AIProvider` with `Stream(ctx, request) (<-chan Chunk, error)`.
- `adapters/ai` implements Anthropic, OpenAI-compatible, Gemini, and local
  providers via official Go SDKs or plain HTTP/SSE. Prefer official SDKs where
  they exist: `anthropic-sdk-go`, `openai-go/v3`, `google.golang.org/genai`.
- `application/ai/harness` ports the original agent loop: context assembly,
  compaction, tool registry, permission checks, trace, cancellation, and
  retries. The original harness is in `infrastructure/ai/harness/*`.
- Tool execution must go through `ports.ToolHost`, not raw package calls, so
  permissions and audit logging are centralized. Tools include SSH exec, SFTP
  list/read/write, forwarding status, and vault queries.
- External agents (Claude/Codex/Copilot and similar CLIs) are subprocess drivers:
  `os/exec`, JSON-lines or the CLI's documented protocol, context cancellation,
  bounded output, and per-agent permission profiles. Port the behavior from
  `electron/bridges/aiBridge/`; do not assume the protocols are stable.
- MCP: use `github.com/modelcontextprotocol/go-sdk` for both client and server.
  `cmd/natcatty-mcp` exposes a stdio MCP server with explicit, permission-gated
  tools. This replaces `electron/cli/netcatty-tool-cli.cjs` and the MCP bridge.
- UI: `ui.Markdown` for streaming replies, native tool cards, `ui.TextInput`
  composer, `ui.Popover` for permissions, `ui.List` for sessions. Selection
  actions need the terminal selection gap resolved (section 3.3).
- Secrets: provider keys and OAuth tokens live in `mygo.Secrets` or encrypted
  DB fields; never in logs or trace payloads.

### 4.12 Sync

- `ports.SyncProvider`: `Connect`, `Pull`, `Push`, `Status`, `Disconnect`.
- `adapters/sync/s3`: AWS SDK v2 S3 with a user-supplied bucket/prefix/region or
  S3-compatible endpoint.
- `adapters/sync/webdav`: `studio-b12/gowebdav`.
- `adapters/sync/gist`: `google/go-github/v74` with a fine-grained token.
- OneDrive/Google Drive: Microsoft Graph SDK or Google API Go client, or raw REST
  with `golang.org/x/oauth2`. Keep these adapters isolated; provider SDKs are
  large.
- Client-side encryption: encrypt the sync payload with the vault master key or a
  sync-specific key before upload; include a format version and KDF metadata.
- Conflict handling: last-writer-wins is not enough for hosts/snippets. Use a
  per-record revision and a deterministic merge for independent fields; surface
  conflicts that cannot merge.
- OAuth: system browser + loopback redirect is preferred. If a webview is used,
  restrict it to the provider domain and never expose provider tokens to plugin
  pages.

### 4.13 Plugin surface

This is the largest architectural difference from Electron.

**What MyGo calls a plugin** is compiled into the app: Go services bound under
`plugin:<name>`, plus an optional JavaScript package whose pages call those
services (`plugin.go`; `mygo.Use(...)`). Official examples include terminal,
SQLite, fetch, watch, websocket, glass, and game; native libraries are declared in
`mygo-plugin.json`. There is no documented dynamic loader for third-party plugin
packages, manifest permission model, or sandboxed JS host. The original Netcatty
plugin contract is a JSON-RPC protocol with manifests, permissions, commands,
menus, keybindings, settings, providers, importers, sync, terminal providers, and
companion executables (`packages/plugin-contract/schema/plugin-contract.schema.json`).

Recommended staging:

1. **Phase 0-3: built-in plugins only.** Model built-ins behind `ports.PluginHost`
   so the UI does not care whether a feature is compiled in.
2. **Declarative contributions.** Define `domain/plugin` types for commands,
   menus, keybindings, settings, and providers. Built-ins register these at
   startup. This gives a stable UI surface without a runtime.
3. **Out-of-process host (later).** Spawn a plugin process with a restricted
   environment and speak JSON-RPC over stdin/stdout. The host owns permissions,
   filesystem roots, network access, and secret leases. This is the closest
   practical equivalent to Electron's `utilityProcess` runtime. Reuse the original
   schema/wire ideas if plugin compatibility matters; do not promise `.ncpkg`
   binary compatibility.
4. **Plugin UI.** Use one `ui.WebView` per plugin panel, with no Node integration,
   a strict Content-Security-Policy, origin allowlists, and a narrow Go binding.
   If per-webview session partitioning is required and MyGo does not provide it,
   run each plugin UI in a separate hidden window or defer the feature.
5. **Terminal providers.** Expose a plugin-provided connection as a `Conn` backed
   by a bidirectional JSON-RPC stream; `Resize` is an RPC. This mirrors the
   original plugin terminal-provider contract without embedding plugin code in the
   app process.
6. **Permissions.** Resource-scoped permissions (host IDs, paths, network
   destinations) with explicit user consent; audit every tool call. Do not rely on
   the plugin's self-declared manifest alone.

### 4.14 UI state and view organization

Replace React hooks with:

- **Controllers** in `application/*` for shared state and I/O.
- **Window state** in `ui/shell` for tabs, active tab, workspace tree, focus,
  dialogs, and selections.
- **Widget-local state** via `ui.Local`, `ui.ListState`, `ui.Selection`,
  `ui.ScrollState`, and `ui.Handle`, following `docs/ui/views.md`.

Rules:

- Build values (`ui.Element`, `ui.Handle`) are per-frame or per-build; never store
  them in domain/controller state. `go tool mygo vet` enforces this.
- Give each stateful control a stable key (`Context.Key`, `ListState.Handle`) so
  focus/selection survive rebuilds and list reordering.
- For long lists (hosts, directory entries, transfers, log lines), use virtualized
  `ui.List`/`ui.Table`/`ui.Outline` and stable keys. Do not build a slice of
  widgets for every row.
- Persist only durable UI state (window bounds, split sizes, tab order, column
  widths); keep transient hover/drag state in memory.
- Keep view functions cheap; start I/O in controller methods and invalidate when
  a snapshot changes.

Mapping of the original state hooks:

| Original hook/store | New home |
|---|---|
| `useVaultState` | `application/vault` + `ui/vault/state.go` |
| `useSessionState` | `application/sessions` + `ui/shell/workspace.go` |
| `useSettingsState` | `application/settings` + `ui/settings/state.go` |
| `useAIChatStreaming`, `aiSessionsStore` | `application/ai` + `ui/ai/state.go` |
| terminal settings/runtime hooks | `ui/terminal/state.go` + `application/sessions` |
| `sftp/*` hooks | `application/transfer` + `ui/sftp/state.go` |
| `plugin*` hooks | `application/plugins` + `ui/plugins/state.go` |
| session restore hooks | `application/sessions/restore.go` + `domain/session` |
| custom themes, keybindings | `application/settings` + `ui/theme` |

### 4.15 Error handling and observability

- Define domain errors (`ErrAuthFailed`, `ErrHostKeyChanged`, `ErrNotConnected`,
  `ErrConflict`, `ErrPermission`) and map adapter errors with `errors.Is/As`.
- Never show raw Go errors to users; translate to actionable messages with a
  “details” expander.
- Use `log/slog` with JSON handlers to `PathLogs`. Include session ID, host ID
  (not hostname if sensitive), operation, duration, and error class. Never log
  passwords, private keys, tokens, or full terminal output.
- Recover panics in goroutines; report to logs and, optionally, a crash reporter.
- Add a diagnostics page mirroring the original “system” view: app version, OS,
  terminal library version/path, database schema version, provider status, and
  recent errors (redacted).

---

## 5. Phased migration plan

The plan is vertical: every phase produces a usable app and tests the riskiest
integration early. Timelines are indicative for one experienced Go engineer and
should be re-estimated after the go/no-go gates.

### Phase 0: Walking skeleton (1-2 weeks)

Deliverables:

- `go.mod`, `mygo.json`, `main.go`, `app/`, `ui/shell`, `resources/icon.png`.
- A window with a host sidebar (static or SQLite-backed), one terminal tab, and a
  local shell.
- SQLite open/migrate, a `hosts` table, and add/edit/delete host.
- `go test ./...`, `go tool mygo vet`, and `go tool mygo build` on macOS, Linux,
  and Windows.
- Terminal benchmark harness and a documented latency/throughput result.

Exit criteria:

- The app opens a local shell, resizes correctly, and survives `go test -race`.
- A host persists across restarts.
- CI builds all three platforms.
- Go/no-go gate results are recorded in `docs/adr/`.

Minimal shape (verified APIs):

```go
package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type app struct {
	term *terminal.Terminal
}

func (a *app) view(c *ui.Context) {
	terminal.View(c, a.term).Fill().AutoFocus()
}

func main() {
	a := &app{}
	mygo.App.WhenReady(func() {
		term, err := terminal.New(terminal.Options{})
		if err != nil {
			log.Fatal(err)
		}
		a.term = term
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:    "Natcatty",
			Width:    1100,
			Height:   720,
			MinWidth: 720,
			MinHeight: 480,
			StateKey: "main",
			Content:  ui.View(a.view),
		})
		win.OnClosed(func() { _ = term.Close() })
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
```

This is the same structure as MyGo's native template and terminal example; add
controllers and the sidebar incrementally.

### Phase 1: SSH core + vault (3-6 weeks)

Deliverables:

- Domain models for hosts, keys, identities, groups, known hosts, proxy profiles.
- SSH adapter: password, public key, certificate, agent, keyboard-interactive/MFA;
  host-key verification and challenge dialog; jump hosts; SOCKS5/HTTP/command
  proxy; keepalive; reconnect.
- Connection pool with reference counting and idle park.
- Terminal sessions over SSH with correct PTY resize, title, cwd, exit status,
  and basic session logging.
- Vault UI for host/key/identity CRUD, search, groups, tags, import/export.
- Secrets: master key in `mygo.Secrets`, encrypted private keys/passwords.
- Session restore and tab/workspace split persistence.
- Menus, settings for terminal font/theme/scrollback, and a host-key prompt.

Exit criteria:

- Daily-driver SSH client for password and key hosts, including a jump host and an
  MFA host.
- No plaintext secrets on disk.
- Restart restores tabs/workspaces and reconnects only on user action.
- Integration tests use an in-process or containerized SSH server.

### Phase 2: SFTP, local protocols, forwarding (3-6 weeks)

Deliverables:

- `FileSystem` port with local and SFTP implementations.
- Dual-pane SFTP UI, breadcrumbs, context menus, search/filter, file editor,
  image/preview, drag/drop, transfer queue, conflicts, pause/cancel/resume.
- Local shell discovery and profiles; serial (baud/parity/flow control); telnet
  with option negotiation and auto-login.
- Port forwarding: local, remote, dynamic (SOCKS5), auto-start, reconnect, status.
- Server monitor (CPU/memory/disk) over SSH exec, with native charts and a status
  bar.
- Connection logs and command history.

Exit criteria:

- Upload/download a directory tree with resume and conflict handling.
- A local port forward and a dynamic SOCKS proxy work through a jump host.
- Serial and telnet sessions work on at least Windows, macOS, and Linux.

### Phase 3: UX and desktop parity (3-6 weeks)

Deliverables:

- Full settings sections: appearance, terminal, keyboard, AI, system, shortcuts,
  sync, plugins, app lock.
- Themes: app theme tokens, Ghostty terminal themes, custom theme import; no
  custom CSS.
- i18n catalogs and `mygo i18n check`; RTL where needed.
- Snippets, notes, command palette, context menus, compose bar, tab reordering,
  session broadcast (Conn-based first), keyword highlighting (deferred or fixed
  bar), find bar (text search without scroll-to).
- Tray, global shortcuts, deep links (`natcatty://`), file associations, recent
  documents, start-at-login, auto-update.
- Encrypted vault backup, cloud sync (S3/WebDAV/Gist/OneDrive/Google Drive).
- App lock with passphrase; OS biometrics as a follow-up.
- Accessibility audit: keyboard navigation, focus order, labels, screen readers.

Exit criteria:

- A user can migrate a vault, use the app daily, sync it, back it up, and recover
  it.
- All core screens are keyboard-operable.
- Known parity gaps are documented in the UI (“not available in the native
  version”) rather than silently missing.

### Phase 4: AI, agents, plugins, long-tail protocols (ongoing)

Deliverables:

- AI providers, streaming chat, tool permissions, agent harness, MCP server.
- External agent drivers (Claude/Codex/Copilot/etc.) as subprocess adapters.
- Out-of-process plugin host, permissions, declarative contributions, optional
  webview UI, terminal providers.
- mosh and EternalTerminal via bundled clients; X11 forwarding; FIDO SK auth;
  inline images/search if upstream APIs become available.
- Advanced terminal features that depend on upstream: selection getter, cursor
  geometry, keyword highlighting, observer popups.

Exit criteria: defined per feature, with explicit user-facing limitations where
parity is impossible.

### Migration of existing user data

The original persists through `localStorageAdapter` into Chromium's localStorage
(`infrastructure/persistence/localStorageAdapter.ts`), with sensitive fields using
Electron `safeStorage` (`electron/bridges/credentialBridge.cjs`). There is no
official, stable Go reader for Chromium's LevelDB localStorage, and safeStorage
ciphertext is tied to the old app's OS credential entry.

Recommended path:

1. Add a one-time **Export** command to the archived Electron build (source is at
   `73d15da7`). It reads the existing stores via the app's own adapters, decrypts
   with `safeStorage`, and writes a versioned JSON bundle (secrets optionally
   passphrase-encrypted).
2. The Go app imports that bundle into SQLite, validates every record, and
   re-encrypts secrets with the new master key.
3. For users who cannot run the old build, support the original vault export
   format if it is self-contained; otherwise document that migration requires the
   old app.
4. Do **not** make LevelDB parsing the primary path. It is version-specific and
   brittle; treat it as a last-resort research spike.

The session restore payload is already a versioned JSON document
(`domain/sessionRestore.ts`), so importing it is straightforward. Themes are JSON.
Plugin packages are files; their runtime compatibility is a separate question.

### Testing strategy

- **Domain:** table-driven tests for workspace split math, session restore,
  conflict policy, path normalization, validation, and settings migrations.
- **Adapters:** integration tests against a real OpenSSH server and SFTP server
  (containerized in CI). Test auth methods, host-key verification, jump hosts,
  resize, exit status, and transfer resume.
- **UI:** `ui.NewTester` for headless view tests: click, type, key, scroll, and
  terminal `Text()` assertions. Use a fake clock for animations/debounce.
- **Terminal:** `ui.NewTester` with `terminal.Options{Command: []string{shell}}`
  and platform-specific shell commands; test IME-like input, paste, selection,
  resize, snapshot/restore.
- **Race:** `go test -race ./...`; controllers must be goroutine-safe.
- **Fuzz:** parsers for SSH config, known_hosts, theme JSON, import bundles, and
  terminal protocol adapters.
- **Golden files:** exported/imported vault JSON and session restore payloads.
- **E2E:** launch the packaged app on each OS in CI where possible; at minimum
  smoke-test startup, window creation, local shell, and DB migration.

---

## 6. Risks, unknowns, and explicit “could not verify” items

### High-impact risks

| Risk | Impact | Mitigation |
|---|---|---|
| Terminal throughput/latency is insufficient for heavy output | Core UX failure | Run `BenchmarkOutput`/`BenchmarkFrame` and a real SSH flood test in week 1; keep the xterm.js-in-webview contingency. |
| Missing terminal APIs: search, selection getter, cursor geometry, raw output, inline images, multiple views | Feature parity gaps (search, AI selection, highlights, ZMODEM, observer, images) | Prioritize upstream contributions or a vendored fork; ship explicit limitations; design alternatives (fixed compose bar, snapshot logs, external viewer). |
| SSH compression and FIDO SK unsupported by `x/crypto/ssh` | Some users cannot connect as before | Audit affected users; use system OpenSSH/agent helpers; document unsupported options. |
| Dynamic plugin compatibility with `.ncpkg` | Plugin ecosystem breakage | Build an out-of-process host; keep the schema; do not promise binary compatibility; start with built-ins. |
| Data migration from Electron localStorage/safeStorage | Users lose vault/settings | Build the old-app export bridge; test with real profiles; keep a rollback path. |
| Native UI accessibility/IME/font parity | Usability and compliance | Test VoiceOver/Narrator/Orca, CJK IME, dead keys, RTL, and ligatures early; fix or document. |
| Webview isolation for plugin UI | Security boundary weaker than Electron | No Node, strict CSP, origin allowlists, narrow bindings, permission checks; verify session partitioning; consider separate processes/windows. |
| GPL-3.0 obligations | Distribution/legal | Keep the derivative GPL-3.0-or-later; include notices for MyGo, Ghostty, x/crypto, and bundled binaries. |
| MyGo is pre-1.0 (v0.5.5) | API churn | Pin exact versions; isolate MyGo calls in `ui/` and `app/`; keep adapters framework-free. |
| macOS 13+ / Windows 10 1809+ / glibc 2.28+ | User coverage | Document requirements; detect at startup and show a clear message. |

### Could not verify

These require a prototype, upstream discussion, or a legal/product decision:

- Actual terminal input latency, sustained throughput, frame pacing, and CPU/GPU
  usage under heavy output on each OS. The benchmark code exists; published
  results do not.
- IME behavior on Windows, Linux (IBus/Fcitx), and macOS in the terminal plugin,
  including candidate windows at the cursor.
- Screen-reader behavior for the native terminal (does it expose the grid/text?),
  and full accessibility parity for custom widgets.
- Whether `ui.WebView` supports per-webview cookie/session partitions, and
  whether multiple plugin webviews can be isolated without separate windows.
- Whether MyGo's webview can enforce an Electron-like sandbox with no Node and
  narrow native bindings in every platform backend.
- Whether mosh-client and EternalTerminal binaries can be redistributed under
  terms compatible with GPL-3.0 in all target markets; verify each project's
  license and bundling terms.
- Whether `x/crypto/ssh` covers the exact legacy algorithm set used by Netcatty's
  network-device users. `compression` is confirmed `none`-only; other algorithm
  gaps need an inventory.
- Whether FIDO2/SK SSH keys can be supported through the system agent on all
  platforms, or require a third-party library with cgo.
- Whether `go.bug.st/serial` and its enumerator build and work with
  `CGO_ENABLED=0` on every target (the Darwin USB enumerator has cgo files; the
  core port may be fine, but test it).
- Whether the MyGo SQLite plugin supports the required WAL mode, large BLOB
  handling, and concurrent read/write patterns for this workload. The API is
  buffered and rejects ATTACH; test with realistic vault sizes.
- Whether the original vault export format is self-contained enough for direct
  import, or whether the old-app export bridge is mandatory.
- Whether the full original plugin contract (permissions, providers, importers,
  sync, terminal providers, companion executables) can be mapped without breaking
  third-party plugins.
- Whether native UI performance on Linux software rendering (VMs, WSL) is
  acceptable for a terminal-heavy app; MyGo documents CPU fallback, but not this
  workload.
- Whether MyGo supports the desired code-signing/notarization and auto-update
  flow for all distribution channels (Mac App Store, enterprise MSI, Linux
  packages). The documented flows cover Developer ID/NSIS/.deb; App Store
  sandboxing is unverified.
- Biometric app unlock on macOS/Windows/Linux. MyGo's documented native APIs do
  not include biometrics; plan platform-specific work or defer.

### Product decisions that should become ADRs

- Native-first UI, webview only for plugin UI/OAuth (`docs/adr/0001-ui-strategy.md`).
- SQLite via MyGo plugin, with `modernc.org/sqlite` as fallback.
- Secrets: `mygo.Secrets` master key + AES-GCM; Argon2id passphrase fallback.
- Plugin strategy: built-ins first, out-of-process host later, no `.ncpkg`
  compatibility promise.
- Terminal gaps: which features are dropped, which are upstreamed, which justify
  a fork.
- Data migration: old-app export bridge vs. LevelDB parsing.
- Licensing: GPL-3.0-or-later.

---

## 7. Sources

### MyGo (verified at v0.5.5)

- Repository: <https://github.com/egoist/mygo>
- Release: <https://github.com/egoist/mygo/releases/tag/v0.5.5>
- Module file / Go version: <https://github.com/egoist/mygo/blob/main/go.mod>
- Getting started and requirements: <https://github.com/egoist/mygo/blob/main/docs/getting-started.md>
- Native UI overview and widget catalog: <https://github.com/egoist/mygo/blob/main/docs/ui/README.md>
- Views, state, `Window.Update`: <https://github.com/egoist/mygo/blob/main/docs/ui/views.md>
- Lists and virtualization: <https://github.com/egoist/mygo/blob/main/docs/ui/list.md>
- Tables/trees/outlines: <https://github.com/egoist/mygo/blob/main/docs/ui/table.md>, <https://github.com/egoist/mygo/blob/main/docs/ui/tree.md>, <https://github.com/egoist/mygo/blob/main/docs/ui/outline.md>
- Split views: <https://github.com/egoist/mygo/blob/main/docs/ui/split.md>
- Tabs: <https://github.com/egoist/mygo/blob/main/docs/ui/tabs.md>
- Text input and IME: <https://github.com/egoist/mygo/blob/main/docs/ui/text-input.md>
- Drag and drop: <https://github.com/egoist/mygo/blob/main/docs/ui/drag-and-drop.md>
- Code editor: <https://github.com/egoist/mygo/blob/main/docs/ui/code-editor.md>
- Markdown: <https://github.com/egoist/mygo/blob/main/docs/ui/markdown.md>
- Web views: <https://github.com/egoist/mygo/blob/main/docs/ui/web-view.md>
- Testing: <https://github.com/egoist/mygo/blob/main/docs/ui/testing.md>
- Performance: <https://github.com/egoist/mygo/blob/main/docs/ui/performance.md>
- Rendering backends: <https://github.com/egoist/mygo/blob/main/docs/ui/rendering.md>
- Desktop APIs (dialogs, clipboard, notifications, Secrets, etc.): <https://github.com/egoist/mygo/blob/main/docs/native.md>
- Application lifecycle, threads, single instance, deep links: <https://github.com/egoist/mygo/blob/main/docs/app.md>
- Menus and tray: <https://github.com/egoist/mygo/blob/main/docs/menus.md>
- Configuration (`mygo.json`, macOS minimum version, resources): <https://github.com/egoist/mygo/blob/main/docs/configuration.md>
- CLI (`dev`, `build`, `vet`, `generate`, `doctor`): <https://github.com/egoist/mygo/blob/main/docs/cli.md>
- Distribution and packaging: <https://github.com/egoist/mygo/blob/main/docs/distribution.md>
- GitHub Actions and signing: <https://github.com/egoist/mygo/blob/main/docs/github-actions.md>
- Terminal plugin docs: <https://github.com/egoist/mygo/blob/main/docs/plugins/terminal.md>
- Terminal plugin source: <https://github.com/egoist/mygo/blob/main/plugins/terminal/terminal.go>
- Terminal options, callbacks, and methods: same file, `Options`, `Terminal`.
- Terminal fonts: <https://github.com/egoist/mygo/blob/main/plugins/terminal/font.go>
- Terminal themes: <https://github.com/egoist/mygo/blob/main/plugins/terminal/theme.go>
- Terminal view/clipboard/keys: <https://github.com/egoist/mygo/blob/main/plugins/terminal/view.go>
- Kitty graphics refusal: <https://github.com/egoist/mygo/blob/main/plugins/terminal/internal/vt/terminal.go>
- Terminal benchmarks: <https://github.com/egoist/mygo/blob/main/plugins/terminal/bench_test.go>
- Terminal native library manifest: <https://github.com/egoist/mygo/blob/main/plugins/terminal/mygo-plugin.json>
- SQLite plugin docs: <https://github.com/egoist/mygo/blob/main/docs/plugins/sqlite.md>
- Plugin API (`mygo.Use`, Go services + page package): <https://github.com/egoist/mygo/blob/main/plugin.go>
- Window API: <https://github.com/egoist/mygo/blob/main/window.go>
- Web view API: <https://github.com/egoist/mygo/blob/main/webview.go>
- Application API: <https://github.com/egoist/mygo/blob/main/app.go>
- Paths: <https://github.com/egoist/mygo/blob/main/paths.go>
- Native project template: <https://github.com/egoist/mygo/blob/main/cmd/mygo/template/native/main.go.tmpl>
- CLI project initialization: <https://github.com/egoist/mygo/blob/main/cmd/mygo/init.go>
- Terminal example: <https://github.com/egoist/mygo/blob/main/examples/terminal/main.go>
- Documentation site: <https://mygo.egoist.dev>

### Go toolchain and modules

- Download/install: <https://go.dev/doc/install>
- Module layout: <https://go.dev/doc/modules/layout>
- Managing dependencies, including tools: <https://go.dev/doc/modules/managing-dependencies>
- `go.mod` reference (`go`, `toolchain`, `tool`): <https://go.dev/ref/mod>
- Workspaces: <https://go.dev/ref/mod#go-work-file>
- Toolchain selection: <https://go.dev/doc/toolchain>
- Build constraints: <https://pkg.go.dev/cmd/go#hdr-Build_constraints>

### SSH, SFTP, and supporting libraries

- `golang.org/x/crypto/ssh` v0.58.0: <https://pkg.go.dev/golang.org/x/crypto/ssh>
- SSH agent: <https://pkg.go.dev/golang.org/x/crypto/ssh/agent>
- Known hosts: <https://pkg.go.dev/golang.org/x/crypto/ssh/knownhosts>
- Argon2id: <https://pkg.go.dev/golang.org/x/crypto/argon2>
- `github.com/pkg/sftp` v1.13.11: <https://pkg.go.dev/github.com/pkg/sftp>
- `go.bug.st/serial` v1.8.0: <https://pkg.go.dev/go.bug.st/serial>
- `golang.org/x/text/encoding/simplifiedchinese`: <https://pkg.go.dev/golang.org/x/text/encoding/simplifiedchinese>
- `golang.org/x/net/proxy`: <https://pkg.go.dev/golang.org/x/net/proxy>
- `github.com/things-go/go-socks5` v0.1.3: <https://pkg.go.dev/github.com/things-go/go-socks5>
- `modernc.org/sqlite` v1.60.1: <https://pkg.go.dev/modernc.org/sqlite>
- `github.com/zalando/go-keyring` v0.2.8: <https://pkg.go.dev/github.com/zalando/go-keyring>
- `github.com/modelcontextprotocol/go-sdk` v1.8.0: <https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk>
- `golang.org/x/oauth2` v0.37.0: <https://pkg.go.dev/golang.org/x/oauth2>
- AWS SDK v2 S3: <https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3>
- `github.com/studio-b12/gowebdav` v0.13.0: <https://pkg.go.dev/github.com/studio-b12/gowebdav>
- `github.com/google/go-github/v74`: <https://pkg.go.dev/github.com/google/go-github/v74/github>
- Anthropic Go SDK: <https://pkg.go.dev/github.com/anthropics/anthropic-sdk-go>
- OpenAI Go SDK v3: <https://pkg.go.dev/github.com/openai/openai-go/v3>
- Google GenAI Go SDK: <https://pkg.go.dev/google.golang.org/genai>
- Microsoft Graph SDK: <https://pkg.go.dev/github.com/microsoftgraph/msgraph-sdk-go>
- Google API Go client: <https://pkg.go.dev/google.golang.org/api>

### Original Netcatty source (commit 73d15da7)

Read with `git show 73d15da7:<path>` (do not restore into the tree):

- Architecture overview: `AGENTS.md`
- Package manifest and dependencies: `package.json`
- Electron entry and bridge registration: `electron/main.cjs`, `electron/main/registerBridges.cjs`
- Preload API: `electron/preload.cjs`, `electron/preload/api.cjs`
- SSH and connection pool: `electron/bridges/sshBridge.cjs`, `electron/bridges/sshConnectionPool.cjs`
- SFTP: `electron/bridges/sftpBridge.cjs`
- Terminal/local/telnet/mosh/serial: `electron/bridges/terminalBridge.cjs`, `electron/bridges/terminalWorkerManager.cjs`, `electron/terminalWorker/`
- Mosh handshake: `electron/bridges/moshHandshake.cjs`
- Port forwarding: `electron/bridges/portForwardingBridge.cjs`, `electron/bridges/x11Forwarding.cjs`
- Credentials: `electron/bridges/credentialBridge.cjs`
- Vault backup: `electron/bridges/vaultBackupBridge.cjs`
- Cloud sync and OAuth: `electron/bridges/cloudSyncBridge.cjs`, related OAuth bridges
- AI bridge and harness: `electron/bridges/aiBridge.cjs`, `electron/bridges/aiBridge/`, `infrastructure/ai/harness/`
- Plugin runtimes: `electron/plugins/browserPluginRuntime.cjs`, `electron/plugins/utilityPluginRuntime.cjs`, `electron/plugins/pluginManager.cjs`
- Tool CLI/MCP: `electron/cli/netcatty-tool-cli.cjs`, `electron/mcp/`
- Domain models: `domain/models.ts`, `domain/models/connection.ts`, `domain/models/terminal.ts`, `domain/models/sftp.ts`, `domain/models/portForwarding.ts`, `domain/models/workspace.ts`, `domain/models/history.ts`, `domain/models/keyBindings.ts`
- Workspace logic: `domain/workspace.ts`, `domain/sidePanelLayout.ts`
- Session restore: `domain/sessionRestore.ts`
- State hooks: `application/state/useVaultState.ts`, `application/state/useSessionState.ts`, `application/state/useSettingsState.ts`, `application/state/aiSessionsStore.ts`
- Storage keys and adapter: `infrastructure/config/storageKeys.ts`, `infrastructure/persistence/localStorageAdapter.ts`
- Services: `infrastructure/services/`
- Terminal components: `components/terminal/`
- SFTP components: `components/sftp/`
- Settings components: `components/settings/`
- Shared aside-panel design system: `components/ui/aside-panel.tsx`
- Plugin contract schema: `packages/plugin-contract/schema/plugin-contract.schema.json`
- License: `LICENSE`

### Licenses

- MyGo: MIT (`gh api repos/egoist/mygo/license`).
- Ghostty (libghostty-vt upstream): MIT (`gh api repos/ghostty-org/ghostty/license`).
- `golang.org/x/crypto`: BSD-3-Clause.
- Original Netcatty: GPL-3.0; `package.json` declares `GPL-3.0-or-later`.
- Verify the licenses of any bundled mosh/ET binaries and third-party assets
  before distribution.
