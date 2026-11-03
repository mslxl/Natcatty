# Mosh terminal emulator parity and reuse

Status: research / decision input
Date: 2026-11-04
Scope: whether Natcatty can replace the bundled `mosh-client` process with an in-process
Go mosh client that feeds the existing MyGo terminal, or whether the mosh client needs
its own terminal framebuffer and emulator, on desktop and mobile.

Question: **Can the existing terminal emulator (MyGo's native terminal on desktop and
mobile) consume mosh's `HostBytes` stream with stock-quality parity, or does the mosh
client need its own framebuffer and emulator?**

Sources verified against:

- Mosh **1.4.0-39-gdecd9b7**, commit
  [`decd9b705eb81626f694335b8d5940538beb06da`](https://github.com/mobile-shell/mosh/commit/decd9b705eb81626f694335b8d5940538beb06da)
  (2026-03-06), checked out at `/tmp/mosh-src`.
- MyGo **v0.5.5**, the version pinned by this repository
  ([`go.mod:6`](../../go.mod)), read from the module cache at
  `/Users/mslxl/go/pkg/mod/github.com/egoist/mygo@v0.5.5`.
- MyGo PR **#940**, branch `mobile`, head
  [`7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`](https://github.com/egoist/mygo/commit/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a),
  fetched as source and read at `/tmp/mygo-pr940-src`.
- This repository at current HEAD `f958521e78b16cbbf9f1edba1d566b33bfc6187c`.
- Bundled mosh resources: MoshCatty `moshcatty-0.1.8`, desktop only
  ([`resources/mosh/BUILD-PROVENANCE.json`](../../resources/mosh/BUILD-PROVENANCE.json),
  [`adapters/bundle/manifests.go:14-51`](../../adapters/bundle/manifests.go)).

No runtime measurement was possible in this environment: there is no Go toolchain on
`PATH`, no mosh binary installed, no mobile SDK/toolchain, and no way to build
libghostty-vt for iOS or Android. Everything below is source-based analysis plus a test
plan. The explicit unknowns are in [section 7](#7-unknowns--needs-verification).

## Answers

1. **The MyGo terminal cannot consume `HostBytes` directly and deliver stock-quality
   parity.** `HostBytes` is a delta against mosh's own client-side model, not a
   self-contained terminal program. The stock mosh client never treats the real terminal
   as the owner of session state: it maintains a `Terminal::Complete` (an emulator plus a
   framebuffer), applies prediction/notification overlays to a copy, diffs that against
   the screen it last drew, and only then writes display bytes to the terminal. MyGo's
   terminal can render that final byte stream, but it cannot replace the model in the
   middle.

2. **The mosh client needs its own framebuffer and parser ("emulator"), but not a
   general-purpose terminal emulator.** It needs a mosh-compatible `Complete`: cells,
   rows, cursor, scroll region, the modes mosh tracks, title/clipboard/bell state,
   input translation state, and echo-ack history. Because the server only ever sends
   the subset produced by mosh's `Display`, the client parser can be a bounded parser
   for that subset rather than full ECMA-48. The client also needs mosh's display
   encoder and prediction/overlay engine.

3. **On desktop, MyGo's terminal can and should remain the renderer.** It is already the
   renderer on the current stock path: the adapter spawns the bundled `mosh-client` in a
   PTY and feeds its output to MyGo through `terminal.Options.Conn`
   ([`adapters/mosh/mosh.go:295-303`](../../adapters/mosh/mosh.go),
   [`ui/session/surface.go:336-367`](../../ui/session/surface.go),
   [`docs/adr/0003-app-owned-transports-mygo-conn.md:1-5`](../../docs/adr/0003-app-owned-transports-mygo-conn.md)).
   A Go client that emits byte-identical display frames to `mosh-client` inherits that
   rendering parity. Correct server-authoritative resize additionally requires upgrading
   MyGo to a release with `Options.ResizeByConn` (present in PR #940, absent from v0.5.5).

4. **On mobile, MyGo's terminal is not available at all**, so it cannot be the renderer
   there. PR #940 has three separate blockers in the stack: libghostty-vt is not built for
   iOS/Android, the terminal plugin has no mobile library entries, and the CLI refuses to
   select a mobile resource for it. Mobile mosh therefore needs either substantial
   upstream MyGo/libghostty-vt work, a small application-owned cell renderer fed from the
   client model, or deferral. Feeding `HostBytes` to a terminal does not solve mobile,
   because there is no terminal to feed.

5. **If the bundled `mosh-client` stays on desktop, no terminal work is needed for
   stock parity today.** The current architecture is already the stock pipeline, with the
   terminal at the end of it. The direct-`HostBytes` design is only relevant if Natcatty
   moves mosh in-process, and then the missing middle stage must be implemented in Go.

6. **Do not ship direct `HostBytes`-to-MyGo as a parity implementation.** It can be a
   useful demo for static output, but it loses prediction, echo acknowledgement, input
   mode translation, repaint/resynchronization, server-authoritative resize, title and
   status overlays, and any ability to repair a predicted or missed state. Those are not
   cosmetic gaps; they are the reasons the stock client has a framebuffer.

## 1. What mosh actually sends, and what the stock client does with it

### 1.1 `HostBytes` is a delta against a model

A mosh server sends protobuf instructions: `HostBytes`, `ResizeMessage`, and `EchoAck`
([`src/protobufs/hostinput.proto:15-31`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/protobufs/hostinput.proto#L15-L31)).
`HostBytes` carries a `hoststring`, which is terminal output, but it is not a fresh
screen and it is not computed against the real terminal.

`Complete::diff_from(existing)` computes it against `existing`, the server's belief about
the receiver's framebuffer:

```text
Complete::diff_from(existing):
  if echo_ack changed:      send EchoAck
  if size changed:          send Resize
  if framebuffer changed:   send HostBytes(Display::new_frame(true, existing.fb, current.fb))
```

See [`src/statesync/completeterminal.cc:69-94`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/statesync/completeterminal.cc#L69-L94).
The important parts:

- The `hoststring` is generated by `Display::new_frame` with `initialized = true`. That
  mode is optimized for a receiver already standing exactly on `existing.fb`.
  `initialized = false` is what emits a full reset/repaint, and the server never uses it
  for `HostBytes`; stock uses it only for its own initial frame, resume, and Ctrl-L.
- `Resize` is a separate instruction, not bytes in the hoststring. The client must apply
  it to its framebuffer between frames
  ([`completeterminal.cc:101-124`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/statesync/completeterminal.cc#L101-L124)).
- `EchoAck` is a separate instruction and matters only to prediction.

So the hoststring has a precondition: "your model is `existing.fb`". Without that model
there is no defined result.

### 1.2 The stock client keeps the model and uses the terminal only for output

`Terminal::Complete` owns a `Terminal::Emulator` and a framebuffer
([`src/statesync/completeterminal.h:45-74`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/statesync/completeterminal.h#L45-L74)).
The client flow is:

1. Receive network state; take the server's framebuffer as `new_state`
   ([`src/frontend/stmclient.cc:275-286`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L275-L286)).
2. Apply overlays to `new_state`: prediction, notifications, title prefix
   ([`src/frontend/terminaloverlay.cc:329-336`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/terminaloverlay.cc#L329-L336)).
3. Diff against `local_framebuffer`, the screen last drawn to the terminal, with
   `Display::new_frame(!repaint_requested, local_framebuffer, new_state)`, and write the
   resulting bytes to stdout (the real terminal)
   ([`stmclient.cc:287-293`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L287-L293)).
4. Set `local_framebuffer = new_state`.

The real terminal is never read back for state. It is an output device. The client's
knowledge lives in `Complete` and in `local_framebuffer`. That is why resume and Ctrl-L
set `repaint_requested`, which calls `new_frame(false, ...)` and re-establishes everything
the terminal might have lost
([`stmclient.cc:74-80`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L74-L80),
[`stmclient.cc:401-402`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L401-L402),
[`src/terminal/terminaldisplay.cc:61-324`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L61-L324)).

`Display::open()` writes `ESC [ ? 1 h` (application cursor keys) and, unless disabled,
`smcup`; `close()` restores modes and `rmcup`
([`terminaldisplay.cc:48-59`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L48-L59)).
Natcatty disables the alternate-screen strings with `MOSH_NO_TERM_INIT=1`
([`adapters/mosh/mosh.go:301-303`](../../adapters/mosh/mosh.go)), and the mosh manual
documents that behavior ([`man/mosh.1:228-231`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/man/mosh.1#L228-L231)).

### 1.3 Input also goes through the model

The stock client does not simply forward stdin. Every byte is fed to its local
`Complete` as a `Parser::UserByte`
([`stmclient.cc:396-405`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L396-L405)).
`UserInput::input` is a small state machine that watches for `ESC O` and, if the remote
application has **not** enabled application cursor keys, rewrites SS3 `A`-`D` to CSI
`[ A`-`D` before sending them to the server
([`src/terminal/terminaluserinput.cc:39-72`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaluserinput.cc#L39-L72)).
The decision reads `emu->fb.ds.application_mode_cursor_keys`
([`src/terminal/parseraction.cc:90-95`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/parseraction.cc#L90-L95)).

That mode is tracked by mosh's parser
([`src/terminal/terminalfunctions.cc:280-311`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminalfunctions.cc#L280-L311)),
but **`Display` never emits `?1h`/`?1l` in a frame**. `?1h` appears only in `open()`, and
`?1l` only in `close()`. A renderer that only sees display frames cannot learn the remote
DECCKM state; the client model must carry it.

The same distinction applies to other input modes. Mosh tracks bracketed paste, the mouse
reporting mode and encoding, and focus events, and `Display` does emit those mode changes
when needed
([`terminaldisplay.cc:278-318`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L278-L318)),
so the renderer can encode those inputs. DECCKM is the exception that forces
client-side state.

### 1.4 Resize and scrollback are not modelled the way a normal terminal models them

Mosh's `Framebuffer::resize` truncates or pads rows; it does not reflow
([`src/terminal/terminalframebuffer.cc:406-428`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminalframebuffer.cc#L406-L428)).
On a size change, `Display` emits `ESC [ r`, `ESC [ 0 m`, `ESC [ H`, `ESC [ 2 J`, marks
the frame uninitialized, and repaints all rows
([`terminaldisplay.cc:117-137`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L117-L137)).
That full clear is why a client model that has been resized differently can still produce
a correct *screen* on the next frame; it does not make the models equal for subsequent
deltas.

Mosh's framebuffer has no scrollback. Scrollback is owned by the real terminal: `Display`
emits newlines/scroll-region operations and rows that leave the screen become history in
the terminal
([`terminaldisplay.cc:167-260`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L167-L260)).
On the current Natcatty path, MyGo's terminal provides that scrollback (10 MB by default;
[`mygo@v0.5.5/plugins/terminal/terminal.go:448-455`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/terminal.go#L448-L455)).
Any in-process client must reproduce the same display bytes to get the same scrollback
behavior from the same terminal.

PR #940's `ResizeByConn` was designed for exactly this server-authoritative case: the
view asks `Conn` for its size, the screen keeps the server's size until the caller calls
`Terminal.Resize` when the server's stream says it resized, so output written for the old
size is not laid out at the new size
([`plugins/terminal/terminal.go:100-112`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/terminal.go#L100-L112),
[`docs/plugins/terminal.md:582-593`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md#L582-L593)).
v0.5.5 has `Options.Conn` but no `ResizeByConn`
([`mygo@v0.5.5/plugins/terminal/terminal.go:48-65`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/terminal.go#L48-L65)),
so the pinned version cannot implement correct server-authoritative resize for an
in-process mosh client.

### 1.5 Prediction and echo acknowledgement require cells, not a screen

The prediction engine operates on a `Framebuffer`'s cells and epochs:
`PredictionEngine::apply`, `cull`, and `new_user_byte`
([`src/frontend/terminaloverlay.cc:350-500`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/terminaloverlay.cc#L350-L500),
[`terminaloverlay.cc:621-700`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/terminaloverlay.cc#L621-L700)).
It predicts echo locally, marks the affected cells, and later culls or confirms them when
the server's state and `EchoAck` arrive. This is the feature that makes mosh usable on a
high-latency link; it is not an optimization.

A renderer that has already drawn predicted text cannot be corrected by feeding the next
`HostBytes` delta, because that delta is computed against the server's model, not against
the predicted screen. Stock avoids this by keeping `local_framebuffer` (what was drawn,
including overlays) separate from the server's `Complete`, and diffing the next state
against `local_framebuffer`. Two states are involved; a single terminal buffer cannot be
both.

## 2. MyGo's terminal: capable renderer, opaque model, unavailable on mobile

### 2.1 Desktop renderer

The terminal plugin binds Ghostty's libghostty-vt through purego and is documented as
supporting colors/SGR, wide characters, emoji and grapheme clusters, scrollback with
reflow, alternate screen, keyboard protocols, mouse/focus/bracketed paste, synchronized
output, OSC 8 hyperlinks, title (OSC 0/2), working directory (OSC 7), and clipboard
(OSC 52/5522)
([`docs/plugins/terminal.md:51-69`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md#L51-L69)).
It is a superset of the sequences mosh's `Display` emits. On the current stock mosh path,
this same terminal already consumes mosh-client's output through `Options.Conn`
([`ui/session/surface.go:336-367`](../../ui/session/surface.go)), so the mosh display
subset is exercised by the product today.

`Options.Conn` is mechanically a good fit for a client engine: bytes read from `Conn` are
shown as program output, bytes typed are written to `Conn`, and a `Conn` with
`Resize(cols, rows)` is told the size
([`mygo@v0.5.5/plugins/terminal/terminal.go:61-65`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/terminal.go#L61-L65),
[`plugins/terminal/terminal.go:99-112`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/terminal.go#L99-L112)).
The repo's `TerminalConn` already implements the Resize duck type
([`application/session/conn.go:112-115`](../../application/session/conn.go)).

What `Conn` does not provide is a way to say "these bytes are a delta against a model you
cannot see", or a way to ask MyGo's terminal what its cells, cursor, or DECCKM state are.

### 2.2 The public API is deliberately opaque

The public surface of `terminal.Terminal` in PR #940 includes `Size`, `Text`, `Resize`,
`Snapshot`, `Send`, `Feed`, `ClearScreen`, `ClearScrollback`, `Search`, `History`, and
`NewMirror`
([`plugins/terminal/terminal.go:1008-1108`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/terminal.go#L1008-L1108),
[`plugins/terminal/mirror.go:71-103`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mirror.go#L71-L103),
[`plugins/terminal/mirror.go:772`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mirror.go#L772)).
There is no exported cell accessor, cursor getter, row dump, or mode getter. `Snapshot`
is an ANSI serialization of the screen and scrollback, and `History` is text rows; neither
is a cell model with identities or epochs, and neither can answer "is DECCKM on?" or
"what is in cell (x, y), including rendition and wide/combining state?".

That opacity is fatal for using the terminal as `Complete`:

- prediction needs to read and rewrite cells;
- input translation needs the remote DECCKM state;
- echo acknowledgement needs the client's own input history, which the terminal does not
  have;
- repaint needs a full model-to-model diff, not `Snapshot`.

The internal `plugins/terminal/internal/vt` package does expose cells and modes, but it is
inside MyGo's module and cannot be imported by Natcatty under Go's `internal` rule.

### 2.3 `Mirror` is not a shortcut

PR #940's `Mirror` is the closest existing design to what mosh needs: a server-side MyGo
terminal produces frames that bring another terminal to the same screen, scrollback,
cursor, and input-relevant modes
([`docs/plugins/terminal.md:550-615`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md#L550-L615)).
It even mirrors DECCKM, the keypad modes, focus, bracketed paste, grapheme mode, and the
mouse modes
([`plugins/terminal/mirror.go:532-543`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mirror.go#L532-L543)),
which is evidence from MyGo itself that those modes are not recoverable from display
output alone.

But `Mirror` cannot be the mosh client's model:

- Its source must be a MyGo terminal with libghostty-vt, which is unavailable on mobile
  and absent from v0.5.5.
- Feeding mosh `HostBytes` into a hidden MyGo terminal would make MyGo the model, with
  Ghostty's resize/reflow and grapheme semantics, not mosh's truncate/pad and `wcwidth`
  semantics. Subsequent deltas are computed against mosh's model, so the models drift.
- `Mirror` has no way to apply prediction cells or answer the DECCKM question through the
  public API.
- `Mirror` is not in v0.5.5.

The docs' warning about feeding a snapshot to a terminal is the same failure mode:
"a window fed the program's output from a snapshot of the server's screen can [come to
show something else], as programs that redraw by moving the cursor relative to where it
is show"
([`docs/plugins/terminal.md:558-565`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md#L558-L565)).

### 2.4 Mobile status

MyGo PR #940 adds a mobile runtime, but the terminal plugin is not part of it:

- libghostty-vt loading is compiled out on iOS and Android:
  `//go:build (darwin && !ios) || (linux && !android && (amd64 || arm64)) || (windows && (amd64 || arm64))`
  in [`plugins/terminal/internal/vt/ffi.go:1`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/vt/ffi.go#L1),
  with the complementary `Supported = false` in
  [`ffi_other.go:1-12`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/vt/ffi_other.go#L1-L12).
- The terminal manifest names only `darwin-{amd64,arm64}`, `linux-{amd64,arm64}`, and
  `windows-{amd64,arm64}`
  ([`plugins/terminal/mygo-plugin.json`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mygo-plugin.json)).
- The library loader resolves `runtime.GOOS + "-" + runtime.GOARCH` and errors
  `terminal: libghostty-vt has no build for <target>`
  ([`plugins/terminal/internal/library/library.go:36-47`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/library/library.go#L36-L47)).
- The libghostty-vt build targets contain no iOS or Android entries
  ([`plugins/terminal/internal/libbuild/main.go:40-51`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/libbuild/main.go#L40-L51)).
- `mygo build` scans dependency `mygo-plugin.json` files and calls `pick(goos+"-"+goarch)`;
  for `ios/arm64` or `android/arm64` the terminal entry has no file and the build fails.
  The only mobile escape is `builtForPhones`, which requires the plugin to declare
  `mobile.native` with the same library name
  ([`cmd/mygo/natives.go:86-95`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/natives.go#L86-L95),
  [`cmd/mygo/mobile_natives.go:324-334`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go#L324-L334)).
  The terminal plugin does not declare it.
- The mobile docs' official plugin table does not list terminal
  ([`docs/mobile.md:378-397`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md#L378-L397)),
  and the terminal library's supported platforms are macOS, Linux, and Windows
  ([`docs/plugins/terminal.md:670-674`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md#L670-L674)).
  The mobile native path compiles C sources (`mobile.native`), not Zig/prebuilt
  libghostty-vt
  ([`docs/mobile.md:467-487`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md#L467-L487)).

v0.5.5 is no better: its `vt` build tag also excludes the `ios` and `android` GOOS
values, and its complementary `ffi_other.go` sets `Supported = false`
([`mygo@v0.5.5/plugins/terminal/internal/vt/ffi.go:1`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/internal/vt/ffi.go#L1),
[`ffi_other.go:1-8`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/internal/vt/ffi_other.go#L1-L8)).
`New` therefore fails at `Load()` with `terminal: not supported on <goos>/<goarch>`
([`library.go:30-44`](https://github.com/egoist/mygo/blob/v0.5.5/plugins/terminal/library.go#L30-L44)),
before it ever reaches the missing manifest target. There is also no mobile runtime in
that release.

The repository itself has no mobile app target today (`mygo.json` is desktop-only, no
`ios`/`android` sections, and the bundle manifest has no mobile mosh assets). Mobile is a
forward-looking requirement in this ticket, not an existing supported path.

## 3. What happens if `HostBytes` is fed through `Options.Conn`

### 3.1 It is mechanically possible

A `Conn` implementation can read protobuf states, concatenate each `hostbytes.hoststring`,
and return those bytes to MyGo; MyGo will parse and draw them. Typed bytes come back on
`Write`, and a `Resize` method receives view size changes. A prototype can therefore show
static output.

### 3.2 Why it is not parity

1. **No client model, therefore no defined base.** `HostBytes` is generated against
   `existing.fb`. A fresh MyGo screen is only the same as that base if the server's
   assumed receiver state is a blank screen of the same size and every delta since then
   arrived in order. Reattach, restart, suspend/resume, restored windows, and any state
   repair break that assumption. Stock's answer is a model plus `new_frame(false)` on
   repaint; there is no server request for a full screen.

2. **No prediction.** MyGo will draw the server's state only. There is no predicted echo,
   so every keystroke waits a round trip; this is the opposite of mosh's defining UX.
   If prediction is added by writing predicted text to MyGo, the next server diff is
   still computed against the server model, so it cannot repair the prediction
   (section 1.5). A separate displayed framebuffer and server `Complete` are required.

3. **No `EchoAck` handling.** The instruction has no effect on a terminal, and without
   input history and predicted-cell epochs it cannot be used.

4. **No remote DECCKM for input.** MyGo's key encoder follows the modes of the terminal
   it is drawing
   ([`plugins/terminal/view.go:867-881`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/view.go#L867-L881)).
   Mosh's display stream does not carry remote DECCKM, and the public API cannot answer
   whether it is on. The stock solution is to force local DECCKM on with `open()` and
   translate SS3 arrows in the client model. Direct feeding has neither half.

5. **No ordering for resize.** The `ResizeMessage` is a protobuf instruction that must be
   applied between display frames. A byte stream cannot encode that exact ordering. The
   engine can call `Terminal.Resize` out of band, but only if the engine owns the
   instruction stream and the terminal reference; that is already an engine, not a direct
   feed. v0.5.5 also lacks `ResizeByConn`, so the view would resize the screen before the
   server's layout is known.

6. **No repaint/resynchronization.** Ctrl-L, resume from suspend, and reconnect require a
   full `new_frame(false)` from a model. MyGo's `Snapshot` is a serialization of its own
   state, not the server's; replaying it does not recreate the server model or preserve
   mode history.

7. **No title/status overlays.** `[mosh] ` title prefix and connection/suspend
   notifications are applied by `OverlayManager` to the framebuffer before display
   ([`terminaloverlay.h:315-340`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/terminaloverlay.h#L315-L340),
   [`stmclient.cc:126-128`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/frontend/stmclient.cc#L126-L128)).
   `HostBytes` never contains them. Stock parity includes them.

8. **No cell-level repair or introspection.** The API cannot read or modify a cell, row,
   cursor, or mode. Any bug in prediction, any need to cull a stale cell, any input
   translation, and any differential verification against stock is impossible.

9. **Mosh's model and Ghostty's model differ.** Mosh uses `wcwidth` plus explicit wide
   cells and appends combining marks to the previous cell, with an NBSP fallback for a
   leading combining mark
   ([`src/terminal/terminal.cc:58-140`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminal.cc#L58-L140),
   [`terminalframebuffer.h:205-225`](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminalframebuffer.h#L205-L225)).
   Ghostty clusters graphemes and has its own width policy. Feeding deltas into a model
   with different width/cluster semantics can drift, especially when a later delta assumes
   the server's cell boundaries. Rendering a frame is more forgiving than being the model.

### 3.3 Can MyGo be the model via `Feed`/`Snapshot`/`Mirror`?

No, for the same reasons plus API constraints:

- `Feed` writes program output into the terminal, but the terminal still has no exported
  cells/modes, and its resize/reflow semantics differ from `Framebuffer::resize`.
- `Snapshot` is a VT serialization, not a cell dump; parsing it back loses row identity,
  epochs, and the distinction between predicted and confirmed state.
- `Mirror` requires a MyGo terminal as source, is absent from v0.5.5, and cannot apply
  prediction or be queried for modes.
- Even if it could, the visible terminal and the server model would be the same buffer,
  which is exactly the prediction-repair problem in section 1.5.

### 3.4 Can libghostty-vt itself be the model?

The app could bind libghostty-vt directly (MyGo already ships it on desktop) and use its
cell/grid API. That gives cell access, but:

- Ghostty reflows scrollback and clusters graphemes; mosh's `Complete` does neither the
  same way. Deltas are computed against mosh's semantics.
- Mosh's prediction overlays operate on mosh cells/epochs, not Ghostty grid references.
- It does not exist on mobile either.
- It couples Natcatty to an unexported/internal ABI that MyGo currently hides.

It is useful as a reference renderer or test oracle, not as the mosh model.

## 4. Source-based coverage matrix

Legend: **Yes** = supported by the source; **No** = not supported; **Inherit** = behavior
comes from stock mosh's own limitations; **Test** = plausible but must be verified
differentially.

### 4.1 Rendering and display features

| Capability | Mosh server model / `Display` | MyGo desktop terminal | Direct `HostBytes` to MyGo | MyGo as renderer for client frames |
|---|---|---|---|---|
| ECMA-48/ANSI parsing breadth | Server parses program output; client sees only the narrow `Display` subset | Superset: Ghostty VT parser | Parses the subset, but with wrong model semantics | Yes |
| Known SGR (bold, italic, underline, blink, inverse, invisible, 8/16/256/truecolor) | Supported; emits a normalized subset (`Renditions::sgr`, `terminalframebuffer.cc:541-600`) | Yes | Renders the emitted subset | Yes |
| Unknown SGR / faint / strikethrough / underline styles | Faint and unknown are ignored by mosh (`Renditions::set_rendition`, `terminalframebuffer.cc:462-525`); not emitted | Yes, supports more | Inherit mosh's loss; parity with stock | Yes |
| Wide characters (CJK, emoji) | `wcwidth`, explicit wide cells, overlapping cell cleared (`terminal.cc:58-129`) | Yes, Ghostty width rules | Test: width model can drift; rendering may still be right per frame | Yes, with width differential tests |
| Combining marks | Appends to previous cell; leading combining gets NBSP fallback (`terminal.cc:118-140`; `terminalframebuffer.h:212-222`) | Yes, grapheme clustering | Test: cluster boundaries/width can differ | Yes, with grapheme differential tests |
| Scrollback | No model scrollback; relies on terminal history via emitted scrolls | Yes, reflowing scrollback (10 MB default) | Populates history, but predictions/resizes cannot be repaired in history | Yes; same terminal behavior as current stock path |
| Resize/reflow | Truncate/pad, no reflow; size change forces full clear/repaint | Reflows scrollback and screen; `ResizeByConn` defers screen size | Test: screen may repaint correctly, model drifts; ordering is the hard part | Yes, with `ResizeByConn` and byte-identical frames |
| Alternate screen | Mosh does not model alt screen; `smcup` only around the local client; disabled by `MOSH_NO_TERM_INIT` | Yes, but no 1049 is present in mosh display output | Unused; same as stock | Yes, with no-init |
| Cursor visibility (`?25`) | Tracked; emitted when changed / on full repaint (`terminaldisplay.cc:264-272`) | Yes | Yes per frame; no model repair | Yes |
| Cursor shape | Not tracked by mosh; no DECSCUSR | Yes, user/program setting | Inherit stock | Yes |
| Application cursor keys (DECCKM, `?1`) | Tracked for input translation; **never emitted in frames**; only `open()` sets it | Tracks local mode; no public getter | No: renderer cannot learn remote mode, engine cannot query it | Yes, if engine tracks it and forces `open()` |
| Bracketed paste (`?2004`) | Tracked and emitted (`terminaldisplay.cc:278-281`) | Yes | Frames set the mode; no model/EchoAck | Yes |
| Mouse modes (9/1000/1001/1002/1003) and encodings (1005/1006/1015) | Tracked and emitted (`terminaldisplay.cc:283-318`) | Yes | Renderer/input encoding works; no model | Yes |
| Mouse focus (`?1004`), alternate scroll (`?1007`) | Tracked and emitted | Yes | Per frame; no model | Yes |
| Focus reports | Tracked with 1004 | Yes | Per frame | Yes |
| Title/icon (OSC 0/1/2) | Tracked; emitted when changed; stock applies `[mosh] ` prefix overlay | Yes, plus `OnTitle` | Missing prefix/overlays | Yes, if engine applies overlays |
| Clipboard (OSC 52;c) | Tracked; emits `OSC 52;c` (`terminaldisplay.cc:96-106`) | Yes, with app policy | Per frame; no model | Yes |
| OSC 8 hyperlinks | Tracked; emitted (`FrameState::update_hyperlink`) | Yes | Per frame | Yes |
| Bell | Counted; emits BEL | Yes | Per frame | Yes |
| Synchronized output (2026) / Kitty keyboard | Not supported by mosh's emulator | Yes, but never told by mosh | Inherit stock; features unavailable | Same as stock |
| Origin/auto-wrap/insert modes | Tracked server-side; omitted from `DrawState::operator==`; not emitted | Defaults/behavior apply locally | Inherit stock's omissions | Yes for display; engine model only where needed |
| Repaint / Ctrl-L / resume | Full `new_frame(false)` from model | No server-state equivalent | No | Yes, if engine implements it |

### 4.2 Client-side state and behavior

| Requirement | Stock mosh client | MyGo terminal | Direct `HostBytes` | Engine + MyGo renderer |
|---|---|---|---|---|
| Server state framebuffer | `Terminal::Complete` | No | No | Engine implements it |
| Cell-level prediction | `PredictionEngine` | No public cells | No | Engine implements it |
| Echo acknowledgement | `Complete::set_echo_ack`, input history | No | No | Engine implements it |
| Input SS3/CSI translation | `UserInput` + remote DECCKM | Local encoder only | No | Engine implements it |
| Server-authoritative resize | Own `Complete` resize + terminal resize | `ResizeByConn` in PR #940 only | No | Engine + upgraded MyGo |
| Repaint and resync | `repaint_requested`, model diff | No | No | Engine implements it |
| Title/notification overlays | `OverlayManager` | No | No | Engine implements it |
| Byte-for-byte parity testability | Reference implementation | Not comparable (opaque) | Not defined | Yes: compare engine output to stock `mosh-client` |

### 4.3 Mobile

| Requirement | MyGo v0.5.5 | MyGo PR #940 | Consequence |
|---|---|---|---|
| `terminal` package loads on iOS/Android | `vt.Supported = false` on ios/android; manifest has no targets either | `vt.Supported = false` on ios/android | `terminal.New` fails |
| libghostty-vt mobile binary | None | None; libbuild has only desktop targets | Cannot load a VT library |
| `mygo build` resource selection | Fails for the plugin's missing target | Fails unless plugin has `mobile.native`; terminal does not | Build fails before runtime |
| Mobile native plugin path | No mobile runtime in v0.5.5 | C sources via `mobile.native`, not Zig/prebuilt | Would need a new MyGo mechanism |
| Official mobile plugin support | No | Terminal absent from the plugin table | Unsupported path |
| `ResizeByConn` / `Mirror` | No | Yes, but require a working terminal | No help on mobile |
| Existing bundled mosh-client | Desktop assets only | N/A | No mobile stock fallback |

## 5. Recommendation

### 5.1 Desktop, keeping the bundled client (status quo)

Keep the current architecture. The bundled `mosh-client` is the stock client; MyGo's
terminal receives its stdout through a PTY-backed `Conn`. That is stock parity by
construction: the terminal stage is exactly what the stock client was designed to drive.
No framebuffer work is needed for this path. The tradeoffs are the external GPL binary,
per-platform bundling, no Windows arm64, and no mobile.

### 5.2 Desktop, moving mosh in-process

If Natcatty wants an in-process client (for control, licensing, mobile groundwork, or
removing the bundled binary), implement the **client half of mosh** in Go and keep MyGo as
the renderer:

1. **`mosh Complete`**: cells (text, width, renditions subset, hyperlink, wrap), rows,
   cursor, scroll region, tracked modes, title/icon, clipboard, bell, echo-ack history,
   input state. Resize must truncate/pad like mosh, not reflow. The parser only needs the
   sequences mosh's `Display` emits, plus user-input byte handling.
2. **`Display` encoder**: port the semantics of `new_frame`, `put_row`, `append_move`,
   `append_silent_move`, run detection, scroll optimization, ECH/EL use, SGR/hyperlink
   emission, mode emission, cursor visibility, and the `open`/`close` strings. Configure
   it like the stock client: `TERM=xterm-256color`, `MOSH_NO_TERM_INIT=1`,
   `MOSH_TITLE_NOPREFIX` unset.
3. **Overlays**: prediction engine, notification engine, and title prefix. Apply them to
   the copy that is diffed for display, never to the server `Complete`.
4. **Client plumbing**: implement `Options.Conn`. `Read` returns display bytes; `Write`
   feeds user bytes into the client's input path; `Resize` sends the mosh resize user
   input. On a server `Resize` instruction, call `Terminal.Resize` before feeding that
   frame's hoststring. Preserve output backpressure (the current adapter's bounded buffer
   is a useful model).
5. **MyGo upgrade**: require a release containing PR #940's `ResizeByConn`, set it on the
   terminal, and keep `Options.Conn`. v0.5.5 is not sufficient.
6. **Startup/shutdown**: emit `Display::open()` (without `smcup`) before the first frame
   and `Display::close()` at the end, so MyGo's DECCKM and modes match the stock setup.
   Feed the initial `new_frame(false, local, local)` as stock does.
7. **Verification**: use byte equality of the display stream against the bundled
   `mosh-client` as the primary gate. Equal bytes into the same MyGo terminal give equal
   rendering and scrollback without needing an opaque cell API.

This is a bounded reimplementation of mosh's terminal *client semantics*, not a second
general-purpose terminal emulator. The server still parses the program's full output; the
client only sees mosh's simplified stream.

### 5.3 Mobile

Do not feed `HostBytes` to a terminal: there is no usable MyGo terminal on mobile. The
realistic options are:

- **Preferred if mobile mosh is a near-term goal:** add mobile support upstream in MyGo
  and libghostty-vt (iOS/Android builds, manifest entries, loader/PureGo support, CLI
  resource selection). Then the same engine + MyGo renderer architecture works on both
  platforms. This is substantial and depends on Ghostty's mobile buildability and the
  App Store's rules for bundled native code; verify before committing.
- **Fallback:** an application-owned cell renderer on mobile, fed directly from the
  engine's `Complete` cells. It needs monospace grid drawing, cursor, selection/copy,
  touch scrolling, IME/soft keyboard, link handling, and accessibility. It does not need a
  full VT parser because the engine already owns the model; it is much smaller than a
  general terminal, but it is still product work.
- **Defer mobile mosh** until one of the above is funded. Shipping mosh only on desktop is
  coherent; the current bundle has no mobile assets either.

Do not use a webview/xterm.js terminal on mobile as the primary plan: it reintroduces the
web runtime the native rewrite is trying to remove, and it still needs the mosh client
model behind it.

### 5.4 Rejected alternatives

- **Direct `HostBytes` → MyGo terminal.** Prototype only; loses prediction, EchoAck,
  input translation, repaint, resize ordering, and overlays.
- **MyGo as `Complete` via `Feed` + `Snapshot`/`Mirror`.** Model semantics differ; no
  public cells/modes; prediction impossible; `Mirror` absent from v0.5.5 and unavailable
  on mobile.
- **Direct libghostty-vt as `Complete`.** Different resize/grapheme model than the
  server's deltas; internal ABI; absent on mobile.
- **Port mosh's C++ client by linking/copying it into the Go process.** Licensing and
  maintenance burden; see below.
- **Keep mosh-client but add a second emulator anyway.** Adds work without changing
  desktop parity; only justified by mobile or app-owned rendering.

### 5.5 Licensing

Mosh is GPL-3.0-or-later; the bundled MoshCatty binary is distributed as a separate
program with a GPL notice and source-offer obligation
([`resources/mosh/BUILD-PROVENANCE.json`](../../resources/mosh/BUILD-PROVENANCE.json),
[`resources/mosh/README.md`](../../resources/mosh/README.md),
[`docs/adr/0005-bundle-mosh-et-platform-resources.md:12-19`](../../docs/adr/0005-bundle-mosh-et-platform-resources.md)).
A Go port that copies mosh's C++ code, or linking mosh's code into Natcatty, changes the
licensing analysis and may impose GPL-3.0 on the combined application. This is not legal
advice; get review before choosing port-by-copy. A clean-room, behavior-compatible
implementation driven by tests has a different posture, but the test corpus and protocol
details still need counsel. The existing "separate bundled binary" approach does not
automatically extend to linked or copied code.

## 6. Test plan

Measurement was not possible here; these are the tests needed to turn the source-based
conclusions into evidence. The primary oracle should be the **bundled MoshCatty 0.1.8
`mosh-client`**, because that is the behavior Natcatty ships today; upstream
1.4.0-39-gdecd9b7 is the source reference used above, and its exact relation to
MoshCatty's build is an unknown (section 7).

### 6.1 Gate 1: byte-identical display stream (desktop)

Build a C++ reference harness around mosh's `Complete`, `Display`, and `OverlayManager`
(or instrument the bundled client) that accepts a scripted sequence of:

- program output (parsed server-side into framebuffer changes),
- resize instructions,
- echo acknowledgements,
- prediction on/off and local input bytes.

Capture the exact bytes `mosh-client` would write to stdout for each state, with
`TERM=xterm-256color`, `MOSH_NO_TERM_INIT=1`, and `MOSH_TITLE_NOPREFIX` unset. Run the Go
engine over the same instruction stream and compare the concatenated display bytes
byte-for-byte. This is the strongest gate: if the streams are identical, the existing MyGo
terminal, fed identically, renders identically, including scrollback.

Corpus:

- blank/first frame, initial `new_frame(false)`, resume, Ctrl-L;
- single-line edits, cursor moves in all directions, CR/LF/BS shortcuts;
- scrolling whole screen and partial scroll regions; ECH/EL thresholds; BCE on/off;
- full/partial row changes, wrap flags, last-column writes;
- 8/16/256/truecolor, bold/italic/underline/blink/inverse/invisible, unknown SGR;
- CJK wide, emoji, ZWJ sequences, combining marks, leading combining, ambiguous width;
- titles, icon names, OSC 52, OSC 8, BEL;
- bracketed paste, mouse 9/1000/1001/1002/1003, encodings 1005/1006/1015, focus 1004,
  reverse video, cursor hide/show;
- resize narrow↔wide, grow/shrink, repeated resizes, resize with pending output;
- prediction overlays applied/culled, notification rows, title prefix.

### 6.2 Gate 2: model equality

Expose a debug dump of the Go `Complete` (cells with text/width/rendition/hyperlink/wrap,
cursor, scroll region, tracked modes, title, echo ack) and the C++ reference framebuffer.
After every state, compare. This catches drift that a single display frame can hide, and
is required before enabling prediction or input translation.

### 6.3 Gate 3: input equivalence

Replay stdin byte sequences through the stock `UserInput`/`Complete` path and the Go
engine with identical remote states; compare the user bytes sent to the server. Include:

- arrows with remote DECCKM on/off, SS3 vs CSI, `ESC O` split across read boundaries;
- ESC handling and escape-key sequences;
- Ctrl-L, Ctrl-^ quit sequence, suspend/resume (where applicable);
- mouse reports in every enabled mode/encoding, focus reports, bracketed paste;
- UTF-8 split across reads, Option-as-Alt, key repeat, paste >100 bytes;
- paste and typing while prediction is enabled.

Also use MyGo's `ui.NewTester` to drive real key events into the terminal and capture what
its `Conn` writes, with `open()`'s `?1h` in effect, and compare against a reference
terminal's encoding.

### 6.4 Gate 4: resize, reflow, and scrollback

Compare the native engine path against the current stock path at identical sizes:

- resize while output is in flight, before the server acknowledges, and after;
- `ResizeByConn`: view resize must not lay out old-size bytes at the new width;
- full repaint after resize; scrollback depth, ordering, wrapped lines, pull-back;
- narrow↔wide cycles with long lines and full-screen programs;
- scroll region changes, alternate-screen-like programs (vim, less, htop, tmux);
- rows that scroll off exactly at a resize boundary.

### 6.5 Gate 5: prediction and latency

Use a local UDP proxy or mosh's network simulation to add delay/loss/reordering. Verify:

- predicted echo appears within one RTT, is culled on server confirmation, and is
  repaired on mismatch;
- `EchoAck` timing and input-history culling;
- Ctrl-L and resume full repaint;
- roaming (address change) and connection suspension;
- no corruption when a prediction is pending and a resize or title change arrives.

### 6.6 Gate 6: real applications and corpus

Differential screen captures (or terminal `Text()` plus style dumps) for: zsh/bash/fish
prompts, vim/nvim, emacs -nw, htop/top, less/man, tmux inside mosh, fzf, git log --graph,
lazygit/tig, mc, nested ssh, Unicode test suites, and a fuzz corpus of random HostBytes
generated from the reference model. Include selection/copy, search, hyperlinks, and
clipboard policy tests.

### 6.7 Gate 7: mobile renderer (if pursued)

- Engine tests are platform-independent; run them on the mobile Go target without the
  terminal package.
- Renderer: cell grid at device scale, rotation/resize, safe areas, soft keyboard/IME
  composition, special-key bar, touch scroll/selection, copy/paste, links, accessibility.
- Network: foreground/background transitions, UDP socket lifetime, iOS background
  restrictions, roaming, battery/scroll performance.
- Acceptance is rendered-cell equality and interaction tests, not byte equality with
  desktop, because the renderer differs.

### 6.8 Performance and security

- Frame encode/decode and diff throughput at large sizes (e.g., 300x100), 10 MB
  scrollback, large pastes, many rapid updates.
- Memory growth and backpressure under a slow renderer.
- OSC 52/clipboard policy, hyperlink handling, title injection, and untrusted remote
  output fuzzing; ensure the app's clipboard gate remains authoritative.
- No regression in startup latency or session handshake.

## 7. Unknowns / needs verification

1. **MyGo PR #940 merge/release status and API stability.** The analysis uses its head
   commit; it is not in v0.5.5. Whether `ResizeByConn`, `Mirror`, and the loader/build
   changes ship in a release, and with what names/semantics, must be confirmed before
   depending on them.
2. **No runtime verification of MyGo rendering the mosh display subset.** Source and the
   existing stock path strongly suggest it works, but no desktop session was run here.
   Gate 1 and Gate 6 are required.
3. **Exact MoshCatty 0.1.8 source commit and patches.** The provenance names upstream
   mosh but not a commit; the bundled binary may differ from 1.4.0-39-gdecd9b7. Use the
   bundled binary as the oracle and record its build provenance.
4. **Mosh's width/grapheme behavior versus Ghostty's** for ambiguous-width characters,
   emoji ZWJ sequences, variation selectors, and combining sequences. The display stream
   may be byte-identical while the renderer's width differs; differential width tests are
   needed. This is also a stock mosh limitation in any terminal, but "stock quality" needs
   a defined reference terminal.
5. **Mobile libghostty-vt feasibility.** No iOS/Android builds exist in the inspected
   MyGo sources. Building Ghostty's VT library for those targets, loading it through
   purego from a framework/jniLibs, App Store review, and MyGo CLI integration are all
   unverified. Also check Ghostty's supported platforms at the pinned commit.
6. **The mosh test suite was not present in the extracted upstream source** (`/tmp/mosh-src`
   has no `tests/` directory; the export may omit it), so upstream terminal/conformance
   tests could not be inspected or reused. Gate harnesses must be built.
7. **Mobile mosh beyond the terminal.** iOS background UDP behavior, sandboxing, and
   whether the mosh client/SSP can run in-process on mobile are outside this ticket but
   determine whether a mobile client is viable at all.
8. **Licensing of a port.** Whether a behavior-compatible Go implementation is clean, and
   what the existing GPL binary obligations imply for an in-process replacement, needs
   legal review.
9. **Prediction behavior across scrollback boundaries and resizes.** Stock prediction
   engine semantics are subtle; a port must be tested against the reference, especially
   when predicted rows scroll off or a resize arrives with predictions pending.
10. **Go performance and memory** of a cell-level Complete + display encoder + prediction
    under high-throughput output; no benchmark exists yet.
11. **Whether MyGo's mobile `ui` exposes enough custom-drawing primitives** for an
    efficient cell-grid renderer (per-cell text/background styles, cursor, selection,
    touch scrolling) if the application-owned mobile fallback is chosen. This was not
    investigated for this ticket and determines that fallback's cost.

## 8. Sources

### Mosh (upstream 1.4.0-39-gdecd9b7)

- `src/protobufs/hostinput.proto` — HostBytes/Resize/EchoAck instructions.
- `src/statesync/completeterminal.{h,cc}` — `Complete`, `diff_from`, `init_diff`,
  `apply_string`, `set_echo_ack`.
- `src/frontend/stmclient.cc` — init/resume/repaint, input path, output frame pipeline,
  resize.
- `src/frontend/terminaloverlay.{h,cc}` — prediction, notification, title overlays.
- `src/terminal/terminaldisplay.{h,cc}` — `open`/`close`/`new_frame`, `Display` emission
  subset.
- `src/terminal/terminalframebuffer.{h,cc}` — cells, rows, DrawState, resize, renditions.
- `src/terminal/terminal.{h,cc}` — emulator print/width/combining behavior.
- `src/terminal/terminalfunctions.cc` — DEC modes, mouse, bracketed paste, SGR, OSC.
- `src/terminal/terminaluserinput.cc` — SS3↔CSI input translation.
- `man/mosh.1` — `--no-init` / alternate-screen behavior.

### MyGo v0.5.5 (pinned)

- `plugins/terminal/terminal.go` — `Options.Conn`, `Feed`, `Send`, `Resize`, `Snapshot`,
  grapheme mode, 10 MB scrollback.
- `plugins/terminal/mygo-plugin.json` — desktop-only libghostty-vt targets.
- `plugins/terminal/library.go`, `internal/vt/ffi.go`, `internal/library/library.go` —
  loading and platform errors.

### MyGo PR #940 (head 7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a)

- `plugins/terminal/terminal.go` — `Options.Conn`, `ResizeByConn`, `New`, `Feed`,
  `Resize`, `Snapshot`, grapheme mode, scrollback.
- `plugins/terminal/mirror.go` — Mirror, `Frame`, `History`, mirrored modes.
- `plugins/terminal/view.go` — key encoder synced from terminal modes.
- `plugins/terminal/internal/vt/ffi.go`, `ffi_other.go` — iOS/Android exclusion.
- `plugins/terminal/internal/library/library.go`, `internal/libbuild/main.go` — desktop
  build targets.
- `plugins/terminal/mygo-plugin.json` — desktop targets only.
- `cmd/mygo/natives.go`, `cmd/mygo/mobile_natives.go` — mobile resource selection.
- `docs/plugins/terminal.md` — feature list, Mirror/ResizeByConn section, library
  platforms.
- `docs/mobile.md` — mobile runtime, official plugin list, C/native plugin path.

### This repository

- `go.mod` — MyGo v0.5.5 pin.
- `adapters/mosh/mosh.go` — bundled mosh-client PTY path, `TERM`,
  `MOSH_NO_TERM_INIT=1`, viewport reset.
- `adapters/bundle/manifests.go`, `resources/mosh/` — MoshCatty 0.1.8 assets and GPL
  provenance.
- `ui/session/surface.go` — MyGo terminal construction with `Options.Conn`.
- `application/session/conn.go` — `TerminalConn.Resize`.
- `docs/adr/0003-app-owned-transports-mygo-conn.md` — app-owned transports.
- `docs/adr/0005-bundle-mosh-et-platform-resources.md` — bundled mosh decision and
  pure-Go-client tradeoff.
- `docs/research/go-mygo-rewrite.md` — earlier terminal/MyGo research and scope.
