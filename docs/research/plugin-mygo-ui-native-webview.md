# MyGo native UI versus WebView for DBX-class plugin UI

Status: research
Date: 2026-11-06
Scope: MyGo **v0.6.0** (`github.com/egoist/mygo v0.6.0`, the version in this
repository's `go.mod`), the Natcatty `ui/` packages, and
`docs/adr/0007-webview-boundary.md`.

Assumption used below: **DBX-class** means the plugin UI of a database
explorer/IDE — connection and schema browsing, a SQL code editor, a
virtualised result grid, ER diagrams, and explain-plan textual and graphical
views. The repository contains no "DBX" identifier, so this is a working
definition for the ticket, not a codebase term.

## 1. Verdict

1. **Almost all of a DBX UI can be native in MyGo v0.6.0.** The native toolkit
   already has the hard, high-volume pieces: a code editor with SQL
   highlighting and LSP hooks, row-virtualised lists/tables/trees/outlines, a
   terminal plugin, drag and drop, native menus and dialogs, charts, and GPU
   drawing/shader canvases ([`docs/ui/README.md:1-7`](mygo@v0.6.0/docs/ui/README.md),
   [`docs/ui/code-editor.md:10-11`](mygo@v0.6.0/docs/ui/code-editor.md),
   [`docs/ui/table.md:5-10`](mygo@v0.6.0/docs/ui/table.md)).
2. **WebView is required only for UI that is not Go**: a third-party plugin
   panel written in HTML/CSS/JS, remote web content, or a JS-only visual
   library the product chooses to reuse. MyGo "plugins" are compiled-in Go
   services plus npm packages, not a dynamic native plugin ABI
   ([`docs/plugins.md:3-30`](mygo@v0.6.0/docs/plugins.md),
   [`docs/plugins.md:75-105`](mygo@v0.6.0/docs/plugins.md)); therefore a
   Go-only DBX surface never *has* to use WebView.
3. **Three native gaps need new widgets** before a first-party DBX UI is
   fully native: a true wide **data grid** (column virtualisation, frozen
   rows/columns, multi-range selection, typed editors, matrix clipboard), a
   **graph/diagram canvas** for ER diagrams, and a **plan-graph** view for
   explain plans. `ui.Table` virtualises rows but not columns; `ui/chart`
   omits zoom/pan/log scales; the toolkit has no node/edge widget
   ([`ui/table.go:207-254`](mygo@v0.6.0/ui/table.go),
   [`docs/ui/chart.md:258-259`](mygo@v0.6.0/docs/ui/chart.md),
   [`docs/ui/README.md:189-248`](mygo@v0.6.0/docs/ui/README.md)).
4. **WebView is not a plugin sandbox in v0.6.0.** Each web view has its own
   logical page/secret/trusted origins, but cookies and storage live in one
   app-wide WebView2 profile / default WKWebsiteDataStore / default
   WebKitWebContext. The source confirms the limitation ADR 0007 recorded
   ([`docs/adr/0007-webview-boundary.md:1-14`](docs/adr/0007-webview-boundary.md),
   [`internal/windows/app.go:139-174`](mygo@v0.6.0/internal/windows/app.go),
   [`internal/darwin/cookies.go:16-22`](mygo@v0.6.0/internal/darwin/cookies.go),
   [`internal/linux/cookies.go:95-117`](mygo@v0.6.0/internal/linux/cookies.go)).
   Moreover trust is app-global: **any registered custom protocol scheme is
   trusted and receives CORS headers**, so two plugin origins are not a
   privilege boundary by themselves
   ([`window.go:1158-1187`](mygo@v0.6.0/window.go),
   [`protocol.go:169-194`](mygo@v0.6.0/protocol.go)).
5. **ADR 0007 must be superseded if WebView is opened to plugin UI.** The ADR
   permits WebView only for the host-routed browser side panel and requires
   every other surface to be native. Plugin panels, ER diagrams, or
   explain-plan visualisers in WebView are outside that accepted boundary
   ([`docs/adr/0007-webview-boundary.md:1-5`](docs/adr/0007-webview-boundary.md)).

## 2. Native side inventory

### 2.1 Main-thread and build-pass rules

- Native UI is a Go function from app state to elements, called by MyGo on
  the **main thread** after input, model changes, and while something
  animates ([`docs/ui/views.md:3-10`](mygo@v0.6.0/docs/ui/views.md)).
- A frame can contain several **build passes**, so an action can show its
  outcome before painting. Each `ui.Element` value belongs to exactly one
  pass; owner/slot/generation are checked, and stale use is diagnosed in
  development and tests ([`docs/ui/views.md:10-20`](mygo@v0.6.0/docs/ui/views.md),
  [`docs/ui/input.md:5-10`](mygo@v0.6.0/docs/ui/input.md)).
- Persistent identity must be `ui.Handle`/`Context.Key`; widget state such as
  `ListState`/`OutlineState` belongs in app state, never build values
  ([`docs/ui/views.md:16-20`](mygo@v0.6.0/docs/ui/views.md),
  [`docs/ui/views.md:150-166`](mygo@v0.6.0/docs/ui/views.md)).
- State from other goroutines reaches the view through `Window.Update` (runs
  on the main thread, then redraws) or `Window.Invalidate` (redraw only);
  inside a view, `c.Invalidate()` and `c.After(d)` schedule frames
  ([`docs/ui/views.md:168-184`](mygo@v0.6.0/docs/ui/views.md)).
- MyGo's own contract: every backend method and every handler callback runs
  on the main thread; every public method is safe from any goroutine; never
  block the main thread waiting on work that needs it
  ([`AGENTS.md:32-34`](mygo@v0.6.0/AGENTS.md),
  [`docs/architecture.md:173-220`](mygo@v0.6.0/docs/architecture.md)).
- `mygo vet` detects build values stored in structs, package variables, or
  goroutines ([`docs/ui/views.md:191-195`](mygo@v0.6.0/docs/ui/views.md)).

This model is a good fit for a DBX UI: query results, schema refreshes, and
plan updates arrive on goroutines and are committed to the view with
`Window.Update`.

### 2.2 Widget inventory

| Need | MyGo native API | Evidence |
|---|---|---|
| Layout | `Row`, `Column`, `Box`, flexbox sizing/alignment, `Grid`, absolute placement, split view, clipping | [`docs/ui/layout.md:1-136`](mygo@v0.6.0/docs/ui/layout.md), [`docs/ui/README.md:199-209`](mygo@v0.6.0/docs/ui/README.md) |
| Text | `Text`, `RichText`, spans, fonts (system text engine), selection/copy, bidi, RTL | [`docs/ui/text.md:1-181`](mygo@v0.6.0/docs/ui/text.md) |
| Buttons/actions | `Button`, `PrimaryButton`, `Toggle`, segmented control, toolbar, menu button, link | [`docs/ui/README.md:118-129`](mygo@v0.6.0/docs/ui/README.md), [`docs/ui/button.md:1-50`](mygo@v0.6.0/docs/ui/button.md) |
| Text inputs | `TextInput`, `TextArea`, selection, undo/redo, cut/copy/paste, IME composition, password mode, line tracking for gutters | [`docs/ui/text-input.md:1-225`](mygo@v0.6.0/docs/ui/text-input.md) |
| Tables | `ui.Table` / `OutlineTable`: row-virtualised, sortable/resizable/movable columns, cell-block selection, fixed columns, header/cell menus, inline rename | [`docs/ui/table.md:1-197`](mygo@v0.6.0/docs/ui/table.md) |
| Lists | `ui.List`: any row count, only rows in view built, keyed selection, multi-select, drag-select, reorder, headers/labels, accessibility "5 of 10,000" | [`docs/ui/list.md:1-267`](mygo@v0.6.0/docs/ui/list.md) |
| Scroll | `Scroll`, `ScrollHorizontal`, `ScrollBoth`, tracked offsets, scroll-into-view, scrollbars | [`docs/ui/scroll.md:1-101`](mygo@v0.6.0/docs/ui/scroll.md) |
| Trees/outlines | `ui.Tree` (eager), `ui.Outline`/`OutlineTable` (virtualised, any size, columns, keyboard open/close) | [`docs/ui/tree.md:1-39`](mygo@v0.6.0/docs/ui/tree.md), [`docs/ui/outline.md:1-72`](mygo@v0.6.0/docs/ui/outline.md) |
| Canvas/shader | `Draw`/`Painter` (paths, text shaping, images, gradients), `ui.Canvas` render-to-texture, `ui/shader` GPU/CPU shaders, transforms | [`docs/ui/drawing.md:1-228`](mygo@v0.6.0/docs/ui/drawing.md), [`docs/ui/shaders.md:1-308`](mygo@v0.6.0/docs/ui/shaders.md) |
| Dialogs | `ui.Modal`, `AlertDialog`, `Popover`, `Tooltip`, `Toast`, plus system `mygo.Dialog` file/message dialogs | [`docs/ui/dialog.md:1-72`](mygo@v0.6.0/docs/ui/dialog.md), [`docs/ui/overlays.md:1-100`](mygo@v0.6.0/docs/ui/overlays.md), [`docs/native.md:7-48`](mygo@v0.6.0/docs/native.md) |
| Menus | System context menus, menu buttons, window/app menu templates, roles, accelerators | [`docs/ui/context-menu.md:1-72`](mygo@v0.6.0/docs/ui/context-menu.md), [`docs/menus.md:1-80`](mygo@v0.6.0/docs/menus.md) |
| Drag and drop | Typed in-window drags, reorder, files from the OS, cross-window/app data with MIME representations, lazy providers | [`docs/ui/drag-and-drop.md:1-195`](mygo@v0.6.0/docs/ui/drag-and-drop.md) |
| Clipboard | `mygo.Clipboard` text/HTML/PNG, multi-format `transfer.Data`, `c.ReadClipboard`/`WriteClipboard` | [`docs/native.md:157-180`](mygo@v0.6.0/docs/native.md), [`docs/ui/input.md:210-235`](mygo@v0.6.0/docs/ui/input.md) |
| Code editing | `ui/editor`: million-line files, only visible lines laid out, SQL among 30+ languages, multi-caret, folds, find/replace, completions, hover, diagnostics, inlay hints, diff viewer | [`docs/ui/code-editor.md:1-403`](mygo@v0.6.0/docs/ui/code-editor.md), [`docs/ui/diff-viewer.md:1-211`](mygo@v0.6.0/docs/ui/diff-viewer.md) |
| Charts | `ui/chart`: band/linear/time scales, axes, marks, lines/areas, stacks, pies, selection | [`docs/ui/chart.md:1-259`](mygo@v0.6.0/docs/ui/chart.md) |
| Terminal | `plugins/terminal` native view, used by this repo's Session surface | [`docs/plugins.md:20-25`](mygo@v0.6.0/docs/plugins.md), [`ui/session/surface.go:1-16`](ui/session/surface.go) |

### 2.3 Theming, locale, focus, keyboard routing

- **Theming.** Widgets take colors and metrics from `c.Theme()`, which follows
  the system light/dark appearance and the desktop's accent, high-contrast,
  text-scale, and reduced-motion settings. There are no stylesheets or state
  selectors: the view is code, so an element's look follows state directly.
  `c.SetTheme` changes spacing, radius, font size, and colors
  ([`docs/ui/styling.md:100-162`](mygo@v0.6.0/docs/ui/styling.md),
  [`docs/native.md:269-317`](mygo@v0.6.0/docs/native.md)).
- **Locale.** `c.Locale()` and `c.Region()` drive language and regional
  formats; `Element.Lang`/`Span.Lang` select per-element language; dates,
  numbers, first weekday, and hour cycle follow the region. The window lays
  out RTL when the language is RTL, with logical edges and mirrored widgets;
  code, diffs, terminals, and time inputs deliberately stay LTR
  ([`docs/i18n.md:229-276`](mygo@v0.6.0/docs/i18n.md),
  [`docs/ui/right-to-left.md:1-108`](mygo@v0.6.0/docs/ui/right-to-left.md)).
- **Focus.** `Focusable`, Tab/Shift+Tab order, focus rings, `AutoFocus`,
  `FocusWithin`; persistent `ui.Handle` focus; `FocusBind` binds focus to a
  comparable app field; `FocusGroup` makes a toolbar/radio/tab group one Tab
  stop with arrow navigation; dialogs/popovers trap focus and restore it on
  close ([`docs/ui/input.md:62-134`](mygo@v0.6.0/docs/ui/input.md),
  [`docs/ui/overlays.md:27-44`](mygo@v0.6.0/docs/ui/overlays.md),
  [`docs/ui/dialog.md:29-36`](mygo@v0.6.0/docs/ui/dialog.md)).
- **Keyboard.** `c.Shortcut` is window-wide; `Element.Shortcut` is
  focus-subtree-only; focused text inputs take the platform's editing keys
  first; `HandleInput` delivers every key/text/IME/pointer event for
  terminal-like widgets; text composition uses the system IME. Menu
  accelerators and Edit-menu roles still apply
  ([`docs/ui/input.md:136-235`](mygo@v0.6.0/docs/ui/input.md)).
- **Accessibility.** Roles, names, values, states, descriptions, errors,
  announcements, and list/table row counts are first-class
  ([`docs/ui/accessibility.md:1-115`](mygo@v0.6.0/docs/ui/accessibility.md)).

### 2.4 Limits by DBX workload

#### SQL code editor

`ui/editor` is a genuine native code editor: it lays out and paints only the
lines in view, so a million-line file scrolls and edits like a small one, and
SQL is a built-in language ([`docs/ui/code-editor.md:10-11`](mygo@v0.6.0/docs/ui/code-editor.md),
[`docs/ui/code-editor.md:203-209`](mygo@v0.6.0/docs/ui/code-editor.md)). It
provides multi-caret editing, structural selection, line operations,
folding, find/replace, completions/hovers/diagnostics/inlay hints supplied by
the app or an LSP, custom keybindings, diff gutter, and themes
([`docs/ui/code-editor.md:75-200`](mygo@v0.6.0/docs/ui/code-editor.md),
[`docs/ui/code-editor.md:283-322`](mygo@v0.6.0/docs/ui/code-editor.md)).

Limits: the editor does **not** ship a SQL parser, schema-aware completion,
or a language server. The host must implement `OnCompletion`, `OnHover`,
`OnDefinition`, and `SetDiagnostics`; custom highlighters run on the main
thread in `Highlight`/`Tokens`, though a parser may work in the background
from immutable snapshots ([`docs/ui/code-editor.md:245-267`](mygo@v0.6.0/docs/ui/code-editor.md),
[`docs/ui/code-editor.md:283-322`](mygo@v0.6.0/docs/ui/code-editor.md)). The
editor is Go-only: a third-party JS editor cannot be embedded in it.

#### Virtualised data grid

`ui.Table` builds only rows in view, keeps place by row identity rather than
pixel offset, supports millions of rows, and keeps a list of 1,000,000 rows
at ~14.5 µs/frame in MyGo's benchmark
([`docs/ui/list.md:5-10`](mygo@v0.6.0/docs/ui/list.md),
[`docs/ui/list.md:52-80`](mygo@v0.6.0/docs/ui/list.md),
[`docs/ui/performance.md:8-14`](mygo@v0.6.0/docs/ui/performance.md)). It has
sortable/resizable/moved columns, one fixed-column category, row and cell
block selection, header/cell context menus, and inline editing
([`docs/ui/table.md:40-108`](mygo@v0.6.0/docs/ui/table.md),
[`docs/ui/table.md:110-188`](mygo@v0.6.0/docs/ui/table.md)).

The limits matter for result sets:

- **No column virtualisation.** The cell builder loops over every column for
  every built row and header, so cost is O(visible rows × columns). The
  overscan is 2 rows and a frame is capped at 4,096 built rows
  ([`ui/table.go:207-254`](mygo@v0.6.0/ui/table.go),
  [`ui/list.go:23-29`](mygo@v0.6.0/ui/list.go)). A 200-column result grid
  therefore builds thousands of cell elements per frame even though only a
  dozen columns fit on screen.
- **One rectangular cell block.** Separate/additive ranges (Excel Cmd-drag)
  are explicitly not supported
  ([`docs/ui/table.md:45-46`](mygo@v0.6.0/docs/ui/table.md)).
- **No frozen header rows**, only fixed columns and pinned section headers
  ([`docs/ui/table.md:32-35`](mygo@v0.6.0/docs/ui/table.md),
  [`docs/ui/table.md:100-104`](mygo@v0.6.0/docs/ui/table.md)).
- **No built-in spreadsheet semantics**: type-aware cell editors, rectangular
  TSV/CSV copy/paste, fill/drag, or per-column data types must be built by
  the app from arbitrary cell elements and the clipboard APIs
  ([`docs/ui/table.md:176-188`](mygo@v0.6.0/docs/ui/table.md),
  [`docs/native.md:157-180`](mygo@v0.6.0/docs/native.md)).

For a DBX result tab with tens of columns, `ui.Table` is adequate. For
hundreds of columns, pinned header rows, and spreadsheet-grade selection and
clipboard, a new native data-grid widget is required.

#### ER diagrams

MyGo has the primitives but not the widget. `Draw`/`Painter` supports
rectangles, paths, gradients, images, shaped text, clipping, and nested
transforms; `ui.Canvas` renders to a bitmap on the GPU and can be reused as
an image; `ui/shader` can compute per-pixel effects
([`docs/ui/drawing.md:1-109`](mygo@v0.6.0/docs/ui/drawing.md),
[`docs/ui/shaders.md:1-9`](mygo@v0.6.0/docs/ui/shaders.md)). A pan/zoom
diagram is an explicitly documented use of `Painter.Transform`
([`docs/ui/drawing.md:111-137`](mygo@v0.6.0/docs/ui/drawing.md)).

But: **input is not transformed** — the app must map pointer coordinates
through the inverse view itself; there is no node/edge model, layout engine,
edge routing, hit-testing, selection model, drag handles, minimap, or export
([`docs/ui/drawing.md:134-137`](mygo@v0.6.0/docs/ui/drawing.md),
[`docs/ui/README.md:189-248`](mygo@v0.6.0/docs/ui/README.md)). A first-party
ER diagram is therefore native **only with a new graph canvas widget**;
otherwise a JS diagramming library in a WebView is the shorter path.

#### Explain-plan visualisations

- Textual plans: fully native — `ui/editor`/`TextArea` for the plan text,
  `ui.Tree`/`Outline` for collapsible nodes, `ui.Table` for
  `EXPLAIN ANALYZE` rows
  ([`docs/ui/code-editor.md:10-11`](mygo@v0.6.0/docs/ui/code-editor.md),
  [`docs/ui/outline.md:1-72`](mygo@v0.6.0/docs/ui/outline.md),
  [`docs/ui/table.md:1-10`](mygo@v0.6.0/docs/ui/table.md)).
- Charts for cost/time breakdowns: `ui/chart` covers linear/band/time scales,
  bars, lines, areas, stacks, and selection, but explicitly **omits
  logarithmic scales, zooming/panning, legends, and label collision
  avoidance** ([`docs/ui/chart.md:1-10`](mygo@v0.6.0/docs/ui/chart.md),
  [`docs/ui/chart.md:258-259`](mygo@v0.6.0/docs/ui/chart.md)).
- A node-link plan graph (heatmapped costs, collapse/expand, detail
  popovers) needs the same graph canvas as ER diagrams, or a WebView hosting
  an existing JS plan visualiser.

## 3. WebView side inventory

### 3.1 Creation, composition, and lifecycle

- A web view is created with `Window.NewWebView(WebViewOptions{...})` and can
  exist **only in a window showing native UI** (`WindowOptions.Content`);
  otherwise it returns `errNoContent`. The page then works like a window's
  page: `LoadURL`, `LoadHTML`, `Eval`, navigation events, typed bindings, and
  events ([`webview.go:12-108`](mygo@v0.6.0/webview.go),
  [`docs/ui/web-view.md:46-73`](mygo@v0.6.0/docs/ui/web-view.md)).
- Each web view is a **logical `Window` of its own** (own id, random bridge
  secret, trusted origins, page context), hosted by the native window
  ([`webview.go:73-100`](mygo@v0.6.0/webview.go),
  [`bridge.go:217-228`](mygo@v0.6.0/bridge.go)).
- It is placed with `ui.WebView(c, view)`; the system web engine composites
  it *under* the native UI, which paints a hole for it. Popovers, menus,
  dialogs, tooltips, and toasts painted after it show over the page
  ([`docs/ui/web-view.md:75-122`](mygo@v0.6.0/docs/ui/web-view.md),
  [`ui/webview.go:16-34`](mygo@v0.6.0/ui/webview.go)).
- A web view that the view does not build is hidden but **keeps its page**;
  it closes with its window or on `Destroy`
  ([`docs/ui/web-view.md:62-65`](mygo@v0.6.0/docs/ui/web-view.md),
  [`webview.go:134-140`](mygo@v0.6.0/webview.go)).
- The page's `window.close()` leaves the host window open
  ([`docs/ui/web-view.md:141`](mygo@v0.6.0/docs/ui/web-view.md)).
- Page lifecycle events include `OnWillNavigate` (cancellable),
  `OnDidNavigate`, `OnDOMReady`, `OnDidFinishLoad`, `OnDidFailLoad`,
  `OnPageTitleUpdated`, and `OnRenderProcessGone` (reload recovers)
  ([`docs/windows.md:492-503`](mygo@v0.6.0/docs/windows.md)).

### 3.2 Per-window and per-plugin isolation

What is isolated:

- Each native window with a page gets its **own macOS `WKUserContentController`**
  for script messages, user scripts, and bridge configuration, so messages and
  scripts do not leak between windows
  ([`internal/darwin/window.go:378-383`](mygo@v0.6.0/internal/darwin/window.go)).
- Each web view's page has its own `Window`, random `secret`, and
  `trustedOrigins`; the bridge only accepts messages prefixed with that
  page's secret ([`webview.go:79-96`](mygo@v0.6.0/webview.go),
  [`window.go:434`](mygo@v0.6.0/window.go),
  [`bridge.go:217-228`](mygo@v0.6.0/bridge.go)).
- On Windows the bridge is injected at document creation only into the
  top-level frame; iframes get no `chrome.webview` handler
  ([`docs/architecture.md:517-523`](mygo@v0.6.0/docs/architecture.md)).
- Origin checks and trusted origins are per page/navigation
  ([`window.go:1725-1732`](mygo@v0.6.0/window.go)).

What is **not** isolated:

- **Storage and cookies are app-wide.** Windows creates one WebView2
  environment with one user-data directory for the app; macOS reads cookies
  from `WKWebsiteDataStore.defaultDataStore`; Linux uses the default WebKit
  web context ([`internal/windows/app.go:139-174`](mygo@v0.6.0/internal/windows/app.go),
  [`internal/darwin/cookies.go:16-22`](mygo@v0.6.0/internal/darwin/cookies.go),
  [`internal/linux/cookies.go:95-117`](mygo@v0.6.0/internal/linux/cookies.go)).
  There is no per-webview session/profile/partition option in v0.6.0, which
  is exactly the limitation ADR 0007 recorded against v0.5.5
  ([`docs/adr/0007-webview-boundary.md:1-6`](docs/adr/0007-webview-boundary.md)).
- **Trust is app-global per scheme.** `isTrusted` returns true for `about:`,
  `file:`, the frontend scheme, and **any registered custom protocol
  scheme** ([`window.go:1158-1187`](mygo@v0.6.0/window.go)). Consequently a
  plugin page served from its own scheme can call every bound Go method, not
  only its own plugin's.
- **CORS is granted to every trusted origin.** `allowCORS` reflects the
  request origin and exposes all response headers when `isTrusted` says yes
  ([`protocol.go:169-194`](mygo@v0.6.0/protocol.go)). Two plugin schemes are
  separate origins, but both are trusted, so plugin A can `fetch` plugin B's
  or the app's custom-scheme endpoints and read the responses unless the
  handlers add their own authorization.
- Browser-engine process isolation is the engine's normal renderer
  isolation, not a per-plugin security boundary; all pages still share the
  browser profile.

Therefore, for **untrusted** plugins, WebView in v0.6.0 provides origin
separation for DOM/storage keys but not a separate profile, cookie jar, or
privilege domain. A hostile-plugin model needs a separate process/profile
(not available here), or plugins must be treated as trusted code.

### 3.3 Content-Security-Policy, origin allowlists, asset serving

- **CSP.** MyGo does not add a CSP of its own; CSP is whatever the served
  document carries. A strict page policy does not break the bridge: the E2E
  test loads `default-src 'none'; script-src 'nonce-e2e'` and still calls Go,
  because the bridge and its messages are not the page's scripts
  ([`internal/e2e/e2e_test.go:1060-1079`](mygo@v0.6.0/internal/e2e/e2e_test.go)).
  Note that `Eval` is out-of-page execution and **ignores the page's CSP** on
  Windows (DevTools `Runtime.evaluate`)
  ([`docs/architecture.md:517-520`](mygo@v0.6.0/docs/architecture.md)).
  Custom-scheme responses propagate headers on all three backends, so a
  plugin asset handler can send `Content-Security-Policy` like any
  `http.Handler` ([`protocol.go:128-167`](mygo@v0.6.0/protocol.go),
  [`internal/darwin/scheme.go:118-135`](mygo@v0.6.0/internal/darwin/scheme.go),
  [`internal/linux/scheme.go:136-151`](mygo@v0.6.0/internal/linux/scheme.go),
  [`internal/windows/webview.go:1052-1080`](mygo@v0.6.0/internal/windows/webview.go)).
- **Origin allowlists.** Only the app's own pages may call Go: frontend and
  custom-protocol pages, `file:`/`about:`, the dev server, and localhost in
  development; other sites need an explicit `PageOptions.TrustedOrigins`
  entry. `"*"` trusts every origin including later navigations; embedded
  frames never call Go, whatever their origin
  ([`docs/bindings.md:350-369`](mygo@v0.6.0/docs/bindings.md),
  [`window.go:1158-1187`](mygo@v0.6.0/window.go)).
- **Asset serving.** In builds the frontend is served from
  `mygo://localhost/` (`http://mygo.localhost` on Windows) and is a secure
  context; in development it comes from the configured dev server.
  `mygo.Protocol.Handle`/`HandleFunc`/`FileServer` serve extra schemes from
  an `http.Handler`; each scheme is a separate origin, and pages of the
  app's schemes get CORS headers and preflights. `LoadHTML`/`LoadFile` are
  also available ([`docs/frontend.md:10-60`](mygo@v0.6.0/docs/frontend.md),
  [`docs/frontend.md:78-135`](mygo@v0.6.0/docs/frontend.md)).
  Platform exception: on Linux, WebKitGTK cannot play `<video>`/`<audio>`
  from a custom scheme; serve media from loopback HTTP
  ([`docs/frontend.md:108-125`](mygo@v0.6.0/docs/frontend.md)).

### 3.4 Message bridge and binding rules

- The page gets `window.mygo`; `mygo-runtime` and the generated client call
  bound services by name. Values cross as JSON; `context.Context` is hidden
  from TypeScript and canceled when the page navigates away or the window
  closes; errors reject the promise; events are typed; channels stream
  ordered values with ~1 MiB backpressure
  ([`docs/bindings.md:10-100`](mygo@v0.6.0/docs/bindings.md),
  [`docs/bindings.md:211-325`](mygo@v0.6.0/docs/bindings.md)).
- Every bound call runs on its own goroutine; panics are recovered and
  reported as errors; `CallerPage`/`CallerWindow` identify the caller
  ([`docs/bindings.md:55-100`](mygo@v0.6.0/docs/bindings.md)).
- Bridge messages are plain `postMessage`-style JSON prefixed with the
  page's secret; a message without the secret is ignored
  ([`bridge.go:217-228`](mygo@v0.6.0/bridge.go)).
- `PreloadScript` runs before the page's own scripts once `window.mygo`
  exists ([`docs/frontend.md:390-393`](mygo@v0.6.0/docs/frontend.md),
  [`docs/windows.md:479-486`](mygo@v0.6.0/docs/windows.md)).
- "Plugins" in MyGo are Go services exposed as `plugin:<name>` plus an npm
  package that calls them through the same bridge; they are not dynamically
  loaded native code ([`docs/plugins.md:3-30`](mygo@v0.6.0/docs/plugins.md),
  [`docs/plugins.md:75-105`](mygo@v0.6.0/docs/plugins.md)).
- This repository's browser panel already implements the restrictive
  pattern: no preload, no trusted origins, DevTools disabled, all permissions
  denied, popups refused, and no app binding for the gateway
  ([`app/browser.go:244-285`](app/browser.go),
  [`app/browser_sandbox_test.go:27-45`](app/browser_sandbox_test.go),
  [`docs/adr/0007-webview-boundary.md:7-14`](docs/adr/0007-webview-boundary.md)).

### 3.5 Absence of Node integration

There is no Node integration to disable: MyGo windows use the **system
webview** (WKWebView, WebView2, WebKitGTK) and the app is Go. Bun is dev
tooling only and "nothing in an app may need Bun at run time"
([`AGENTS.md:17-21`](mygo@v0.6.0/AGENTS.md),
[`docs/architecture.md:27-32`](mygo@v0.6.0/docs/architecture.md)). The page's
only privileged surface is `window.mygo`, a JSON bridge to bound Go methods.
Natcatty's README states the same product constraint: "No Node.js, Chromium
renderer, or plugin runtime is shipped"
([`README.md:72`](README.md)). A plugin cannot `require`, spawn processes, or
read files except through Go methods the app binds.

### 3.6 DevTools, lifecycle, and platform limitations

- **DevTools.** `PageOptions.DevTools` is per page: `DevToolsAuto` (inspector
  in development builds), `DevToolsEnabled`, or `DevToolsDisabled`; the last
  also turns off WebView2's own F5/Ctrl+R/Ctrl+P/Ctrl+F/Ctrl+Plus keys.
  `OpenDevTools`/`CloseDevTools`/`ToggleDevTools` control it
  ([`docs/windows.md:470-486`](mygo@v0.6.0/docs/windows.md),
  [`docs/frontend.md:395-406`](mygo@v0.6.0/docs/frontend.md)).
- **Composition limits.** The webview is not part of the native window's
  frames: backdrop-reading effects (e.g. the glass plugin) cannot see it, and
  `Window.CapturePage` leaves a hole where it is; `WebView.CapturePage`
  captures the page. On Windows, Narrator finds the page's elements in a
  separate window rather than inside the webview element
  ([`docs/ui/web-view.md:221-233`](mygo@v0.6.0/docs/ui/web-view.md)).
- **Platforms.** macOS uses WKWebView, Linux WebKitGTK (4.1, 4.0 fallback),
  Windows WebView2. A native-UI-only app avoids the webview runtime entirely
  (no WebKitGTK, no WebView2 Runtime); adding one plugin WebView reintroduces
  it ([`docs/architecture.md:27-32`](mygo@v0.6.0/docs/architecture.md),
  [`docs/ui/windows.md:90-93`](mygo@v0.6.0/docs/ui/windows.md)).
- **Isolation.** No per-webview session partition in v0.6.0 (see 3.2). ADR
  0007 explicitly says per-host cookie/localStorage isolation "is not
  promised until a prototype proves it"
  ([`docs/adr/0007-webview-boundary.md:12-14`](docs/adr/0007-webview-boundary.md)).

## 4. DBX surface-by-surface verdict

Legend: **N** = native with today's toolkit; **N+W** = native after a new
widget; **W** = WebView; **N/W** = native preferred, WebView acceptable for a
third-party/JS implementation.

| DBX surface | Verdict | Reason |
|---|---|---|
| App shell, window chrome, tabs, splits, status bar | **N** | Native window/tab model is already the app's architecture ([`docs/adr/0006-native-window-and-tab-model.md:1-20`](docs/adr/0006-native-window-and-tab-model.md), [`ui/shell/view.go`](ui/shell/view.go)). |
| Connection manager, vault, auth, settings, app lock | **N** | ADR 0007 requires these to be native; the existing `ui/vault`, `ui/settings`, `ui/applock` packages are native ([`docs/adr/0007-webview-boundary.md:7-11`](docs/adr/0007-webview-boundary.md)). |
| Schema/object explorer tree | **N** | `ui.Outline`/`OutlineTable` virtualise rows, support columns and keyboard open/close ([`docs/ui/outline.md:1-72`](mygo@v0.6.0/docs/ui/outline.md)). |
| SQL code editor | **N** | `ui/editor` supports SQL, million-line files, completions/hover/diagnostics, multi-caret, folds, find/replace ([`docs/ui/code-editor.md:1-11`](mygo@v0.6.0/docs/ui/code-editor.md), [`docs/ui/code-editor.md:203-209`](mygo@v0.6.0/docs/ui/code-editor.md)). |
| Query result grid, ordinary width (tens of columns) | **N** | `ui.Table` virtualises rows, supports sorting/resizing/moving, cell-block selection and inline editing ([`docs/ui/table.md:1-108`](mygo@v0.6.0/docs/ui/table.md)). |
| Query result grid, very wide / spreadsheet semantics | **N+W** (W fallback) | No column virtualisation; one selection block; no typed editors or matrix clipboard ([`ui/table.go:207-254`](mygo@v0.6.0/ui/table.go), [`docs/ui/table.md:45-46`](mygo@v0.6.0/docs/ui/table.md)). |
| Explain plan as text / collapsible tree / table | **N** | `ui/editor`, `ui.Outline`, `ui.Table` cover these directly ([`docs/ui/outline.md:1-72`](mygo@v0.6.0/docs/ui/outline.md), [`docs/ui/table.md:1-10`](mygo@v0.6.0/docs/ui/table.md)). |
| Explain plan as a node-link graph / heatmap | **N+W** or **W** | No graph widget; `ui/chart` lacks zoom/pan/log scales ([`docs/ui/chart.md:258-259`](mygo@v0.6.0/docs/ui/chart.md)). |
| ER diagram | **N+W** or **W** | `Draw`/`Canvas`/`Transform` can paint it, but there is no node/edge model, layout, hit-testing, or edge routing; input is not transformed ([`docs/ui/drawing.md:111-137`](mygo@v0.6.0/docs/ui/drawing.md)). |
| Schema/DDL diff | **N** | `editor.DiffView` provides split/unified diffs, inline word changes, hunk actions, annotations ([`docs/ui/diff-viewer.md:1-211`](mygo@v0.6.0/docs/ui/diff-viewer.md)). |
| Charts, cost/time dashboards | **N** | `ui/chart` supports scales, axes, bars/lines/areas, stacks, selection; zoom/pan/log scales must be app-drawn ([`docs/ui/chart.md:1-259`](mygo@v0.6.0/docs/ui/chart.md)). |
| SQL console / log stream | **N** | `ui/editor` read-only, `ui.List`, or the terminal plugin ([`docs/ui/code-editor.md:182-188`](mygo@v0.6.0/docs/ui/code-editor.md), [`ui/session/surface.go:1-16`](ui/session/surface.go)). |
| Local Markdown/notes/docs | **N** | `ui/markdown`, `ui/richtext`, `ui/text` ([`docs/ui/README.md:174-178`](mygo@v0.6.0/docs/ui/README.md)). |
| Remote HTML / external web content | **W** | Only the web engine can render it; this is the existing browser-panel case ([`docs/ui/web-view.md:1-10`](mygo@v0.6.0/docs/ui/web-view.md)). |
| Arbitrary third-party plugin panel (HTML/JS) | **W (must)** | MyGo has no dynamic native plugin ABI; the only execution environment for JS UI is the webview ([`docs/plugins.md:3-30`](mygo@v0.6.0/docs/plugins.md), [`docs/plugins.md:75-105`](mygo@v0.6.0/docs/plugins.md)). |
| Third-party JS-only editor/grid/diagram/plan library | **W** | Monaco, CodeMirror, AG Grid, React Flow, PEV2, etc. cannot run in native UI; porting them is a rewrite ([`docs/ui/README.md:1-7`](mygo@v0.6.0/docs/ui/README.md)). |
| Dialogs, menus, toolbars, toasts, tooltips | **N** | Native system menus/dialogs and native overlays ([`docs/native.md:7-48`](mygo@v0.6.0/docs/native.md), [`docs/ui/overlays.md:1-100`](mygo@v0.6.0/docs/ui/overlays.md)). |
| Drag/drop, clipboard | **N** | Native typed drags, OS drops, multi-format clipboard ([`docs/ui/drag-and-drop.md:1-195`](mygo@v0.6.0/docs/ui/drag-and-drop.md), [`docs/native.md:157-180`](mygo@v0.6.0/docs/native.md)). |

**Bottom line.** For an app-owned DBX UI written in Go, **no surface must be
WebView**. WebView becomes mandatory only when the plugin itself ships
browser UI or when the product insists on reusing a JS-only visual library.
The ER diagram, graphical explain plan, and very wide result grid are the
three places where "native" currently means "write a new widget".

## 5. New native widgets required

1. **Data grid (`ui/datagrid` or `ui/dbx/grid`).**
   - Column virtualisation (build only visible columns, like `List` does for
     rows) to complement the existing row virtualisation.
   - Frozen header rows and frozen columns; grouped/multi-level headers.
   - Multiple/additive selection ranges, row/column range selection,
     select-all, and row headers.
   - Type-aware cell editors (text, number, NULL, boolean, date/time, JSON,
     enum) with commit/cancel semantics.
   - Spreadsheet keyboard navigation (arrows/Tab/Enter/Home/End/PageUp/
     PageDown, Shift/Cmd/Ctrl modifiers) and rectangular TSV/CSV copy/paste,
     including NULL representation.
   - Per-cell renderers and alignment, and hooks for row height/content.
   - Basis: extend `ui.Table`/`ListState` rather than invent a parallel
     virtualisation model ([`ui/table.go`](mygo@v0.6.0/ui/table.go),
     [`docs/ui/list.md:1-10`](mygo@v0.6.0/docs/ui/list.md)).
2. **Graph/diagram canvas (`ui/graph` or `ui/diagram`).**
   - Node/edge scene model with stable IDs and geometry.
   - Pan/zoom that transforms both painting and hit-testing (inverse mapping
     is currently the app's job; [`docs/ui/drawing.md:134-137`](mygo@v0.6.0/docs/ui/drawing.md)).
   - Selection, drag, hover, context menus, keyboard navigation, and focus
     integration.
   - Pluggable layout (layered/tree, force-directed, manual) and edge
     routing/ports; viewport culling; minimap; fit-to-content.
   - Export to PNG/SVG (PNG is available via `CapturePage`/canvas; SVG needs
     a serializer).
   - Basis: `Painter`/`ui.Canvas`/`Transform` and `ui.Local` state
     ([`docs/ui/drawing.md:81-137`](mygo@v0.6.0/docs/ui/drawing.md),
     [`docs/ui/custom-widgets.md:67-104`](mygo@v0.6.0/docs/ui/custom-widgets.md)).
3. **Plan graph (`ui/plangraph`, likely built on the graph canvas).**
   - Hierarchical plan layout, node cost/rows/time heatmaps.
   - Collapse/expand subtrees, detail popover, and selection linked to the
     text/tabular plan.
   - Pan/zoom and large-plan culling; optional log-scale cost bars drawn with
     `Draw` (since `ui/chart` has no log scale).
4. **Not required:** a new SQL editor — `ui/editor` already covers it; a new
   terminal — `plugins/terminal` exists; a new tree — `ui.Outline` exists; a
   new diff viewer — `editor.DiffView` exists.

## 6. Decision notes and consequences

- If DBX plugins are **trusted, compiled-in Go**, keep them native and treat
  WebView as unnecessary. Their UI can use the three new widgets and the
  existing toolkit.
- If DBX plugins are **third-party HTML/JS**, their UI must run in WebView.
  That is a new trust boundary, and v0.6.0 does not provide per-plugin
  storage/profile isolation. Mitigations available today:
  - one custom scheme per plugin, served by a dedicated `http.Handler` with a
    strict CSP;
  - no `PreloadScript`, no `TrustedOrigins`, `DevToolsDisabled`, all
    permissions denied, popups refused;
  - bind only a narrow per-plugin service, and authorize inside every method
    using `CallerPage`; never bind broad app services and never trust `"*"`
    ([`docs/bindings.md:350-369`](mygo@v0.6.0/docs/bindings.md),
    [`app/browser.go:244-285`](app/browser.go));
  - do not expose sensitive app data on custom schemes that plugin origins
    can read via CORS, because every registered scheme is trusted for CORS
    ([`protocol.go:169-194`](mygo@v0.6.0/protocol.go),
    [`window.go:1158-1187`](mygo@v0.6.0/window.go));
  - explicitly `Destroy` plugin web views when a plugin is disabled, since a
    hidden web view keeps its page and process
    ([`docs/ui/web-view.md:62-65`](mygo@v0.6.0/docs/ui/web-view.md)).
- If the product wants a **single plugin UI model for both Go and JS
  plugins**, the safest split is: native chrome and data surfaces, WebView
  only for plugin-rendered panels; ER diagrams and plan graphs either
  WebView (third-party) or the new native graph widget (first-party).
- ADR 0007's "native everywhere except the browser panel" remains correct for
  v0.6.0's isolation guarantees. Opening WebView to plugins requires a
  superseding ADR that states the trust model, the absence of per-plugin
  storage partitioning, and the CORS/trusted-scheme behavior above
  ([`docs/adr/0007-webview-boundary.md:1-14`](docs/adr/0007-webview-boundary.md)).

## 7. Sources

Primary sources read for this report (all paths resolved; `mygo@v0.6.0` is
`~/go/pkg/mod/github.com/egoist/mygo@v0.6.0`, matching `go.mod:6`):

- MyGo v0.6.0: `AGENTS.md`, `docs/architecture.md`, `docs/bindings.md`,
  `docs/frontend.md`, `docs/menus.md`, `docs/native.md`, `docs/plugins.md`,
  `docs/windows.md`, `docs/i18n.md`, `docs/ui/*.md` (README, views, input,
  layout, text, text-input, list, table, tree, outline, scroll, code-editor,
  diff-viewer, drawing, shaders, chart, drag-and-drop, context-menu, dialog,
  overlays, styling, accessibility, right-to-left, custom-widgets,
  performance, web-view, windows), `ui/table.go`, `ui/list.go`,
  `ui/webview.go`, `webview.go`, `window.go`, `bridge.go`, `ipc.go`,
  `protocol.go`, `internal/e2e/e2e_test.go`,
  `internal/darwin/{window,cookies,scheme}.go`,
  `internal/linux/{cookies,scheme}.go`, `internal/windows/{app,webview}.go`.
- Natcatty repo: `docs/adr/0006-native-window-and-tab-model.md`,
  `docs/adr/0007-webview-boundary.md`, `docs/research/go-mygo-rewrite.md`,
  `README.md`, `app/browser.go`, `app/browser_sandbox_test.go`,
  `ui/browser/panel.go`, `ui/session/surface.go`, `ui/editor/view.go`,
  `ui/sysmanager/view.go`, `ui/monitoring/view.go`.
- Dependency version: `go.mod:6` pins `github.com/egoist/mygo v0.6.0` with no
  `replace`.
