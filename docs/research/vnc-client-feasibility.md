# VNC (RFB) client feasibility in MyGo for natcatty

Status: research  
Date: 2026-11-20  
Scope: whether a VNC client (RFB protocol, RFC 6143) can be implemented inside
natcatty on the MyGo UI, which of the four candidate paths (native framebuffer
widget / noVNC in webview / external viewer / VNC-over-SSH preset) is
recommended, and the concrete architecture a VNC feature would take in the
existing `domain`/`ports`/`application`/`adapters`/`ui` layering.

Sources verified against:

- **RFC 6143** (`rfc-editor.org/rfc/rfc6143.txt`) — handshake, security
  negotiation, message tables, PIXEL_FORMAT, encodings.
- **`github.com/rfbproto/rfbproto`** (`rfbproto.rst`, master) — the community
  spec registering security types, extension messages, pseudo-encodings, and
  the VeNCrypt/RA2/DH/Tight/ZRLE wire details.
- **QEMU `ui/vnc.h` + `ui/vnc.c`** and **TigerVNC `common/rfb/encodings.h` +
  `Security.cxx`** — server-side encoding/auth support matrices.
- **MyGo v0.9.2** module source in the local module cache
  (`github.com/egoist/mygo@v0.9.2`, pinned by natcatty's `go.mod`):
  `ui/bitmap.go`, `ui/canvas.go`, `ui/handler.go`, `ui/input.go`,
  `ui/paint.go`, `ui/api_gen.go`, `ui/glyphs.go`,
  `internal/platform/keycode.go`, `plugins/terminal/`, `plugins/game/`,
  `webview.go`, `protocol.go`, `modules.go`, `ipc.go`, plus
  `docs/ui/{image,input,rendering}.md` and `docs/architecture.md`.
- **Go VNC library module zips** fetched from proxy.golang.org (bhmj/vnc2video
  additionally via raw.githubusercontent.com at tag v0.0.2):
  mitchellh/go-vnc, unistack-org/go-rfb v0.0.1 (renamed from vtolstov/go-vnc),
  amitbet/vncproxy, kward/go-vnc, tenthirtyam/go-vnc v1.0.2,
  bigangryrobot/go-vnc v0.1.5, bhmj/vnc2video v0.0.2.
- **noVNC** repository (README, LICENSE.txt, `core/rfb.js`, `core/websock.js`,
  `vnc_lite.html`, docs/API.md) and **websockify** (COPYING = LGPL-3.0).
- **This repo** (branch `research-vnc-mygo`): `application/session/session.go`,
  `adapters/telnet/telnet.go`, `adapters/ssh/`, `application/forwarding/`,
  `domain/forwarding/rule.go`, `ui/browser/panel.go`, `go.mod` (`mygo v0.9.2`,
  `go 1.27.4`, `coder/websocket v1.8.15` indirect), `justfile`
  (`CGO_ENABLED := "0"`).

---

## 1. Executive summary and verdict

**Verdict: feasible, with one architectural caveat.** RFB is a genuinely simple
wire protocol — a minimal pure-Go client (handshake + VNC auth + Raw/CopyRect
decode) is ~800–1200 lines of `encoding/binary` work with no cgo. The hard part
is not the protocol; it is that natcatty's session model is built around
`appsession.Transport`, a **byte stream for text terminals**
(`application/session/session.go:302-307`: `io.ReadWriteCloser` +
`Resize(cols, rows)`), while VNC needs a pixel surface plus pointer/keyboard
events in the opposite direction.

The decisive question — can MyGo v0.9.2 render and drive a framebuffer —
answers **yes, fully**: `ui.Bitmap.Update(img image.Image)` re-uploads pixels
into an existing GPU texture "for one that changes as video does"
(`ui/bitmap.go:159`), and `Element.HandleInput` delivers key down/up at
HID-scancode granularity plus pointer move/down/up/scroll with element-local
coordinates (`ui/handler.go`, `ui/input.go:479`). CGO_ENABLED=0 is preserved —
docs/architecture.md states "No cgo. Everything builds with CGO_ENABLED=0" and
all GPU renderers are pure Go.

### Ranked paths

1. **Native framebuffer widget + embedded Go RFB client (recommended end
   state).** A `VNCView` element (`ui.Box.Focusable().AutoFocus().HandleInput()
   + Draw`) painting a `*ui.Bitmap`, fed by a decode goroutine, input forwarded
   as RFB KeyEvent/PointerEvent. Embed `github.com/unistack-org/go-rfb` as the
   RFB transport (only surveyed lib shipping a `FrameBuffer.Composite` →
   `draw.Image` loop; MIT, zero deps, actively maintained). Best UX, fits the
   adapter layering, pure Go.
2. **VNC-over-SSH preset (recommended first ship / security story).** RFB with
   VNC-auth is plaintext; natcatty already has `application/forwarding` +
   `adapters/ssh` direct-tcpip. Auto-creating a local rule
   `127.0.0.1:<free> → localhost:5900+display` and launching a viewer is mostly
   glue, and it is the security answer no Go VNC library provides (no surveyed
   lib does RA2; the only VeNCrypt/TLS implementation is hard-coded X509Vnc
   with `InsecureSkipVerify`).
3. **noVNC in `ui.WebView` (pragmatic middle path).** Serve embedded noVNC
   `core/` via `mygo.Protocol.Handle` + `FileServer(embed.FS)` on a custom
   scheme, plus a ~100-line loopback WS↔TCP bridge (`coder/websocket` is
   already an indirect dep). Real in-app tab today without owning an RFB
   client; MPL-2.0 core is GPL-3.0-compatible. Carries webview/CSP/platform
   unknowns (§8).
4. **External viewer (zero-effort fallback).** `mygo.Shell.OpenExternal`
   (`vnc://host:port`) or spawn TigerVNC `vncviewer` via `exec.Command`. Poor
   in-app UX but trivially available and pairs naturally with path 2.

Recommended sequencing: ship 2+4 first (cheap, secure), then 1 (the native
client) as the real feature; keep 3 as the contingency if the native client
slips or a webview renderer is preferred.

---

## 2. RFB protocol essentials a client must implement

All values verified against RFC 6143 and rfbproto.rst master.

### 2.1 Handshake

- Server sends 12 ASCII bytes `"RFB xxx.yyy\n"`; client replies with the
  version it wants. Only 3.3 / 3.7 / 3.8 are published; unknown 3.x is treated
  as 3.3. All version differences are confined to the handshake — after
  ClientInit the protocol is identical (RFC §7.1.1, App. A).
- **Security negotiation.** 3.8: server sends `U8 count` + `U8[]` type list;
  client picks one with a single `U8`. `count == 0` means failure → `U32
  reason-length` + string, then close (§7.1.2). 3.3: server dictates a single
  `U32` type 0/1/2 (App. A.1). 3.7 uses the same U8-count+list mechanism as 3.8
  (per rfbproto's "Version 3.7 onwards" section; RFC App. A.2 only lists the
  two deltas — no SecurityResult for type None, no reason string on failure).
- `SecurityResult` = `U32 status` (0 OK / 1 failed; rfbproto adds 2 "too many
  attempts", Tight-only). On 3.8 failure a reason string follows; on ≤3.7 the
  server just closes.

### 2.2 Security types

- `0` Invalid, `1` None, `2` VNC Authentication: 16-byte server challenge,
  DES-encrypted with a password-derived key — password truncated to 8 chars or
  NUL-padded, and **each DES key byte's bits are reversed** vs standard DES
  ("reverse order of most implementations"). Cryptographically weak; §9.
- Registered extended types (rfbproto table): `16` Tight (tunnel/auth
  negotiation framework), `19` VeNCrypt (subtype negotiation wrapping TLS +
  inner auth; subtypes 256–267: Plain, TLSNone, TLSVnc, TLSPlain, X509None,
  X509Vnc, X509Plain, X509SASL, TLSSASL, Ident, TLSIdent, X509Ident), `20`
  SASL, `30` Diffie-Hellman (Apple-style username+password, AES-ECB), `113`
  MSLogonII (UltraVNC; 64-bit DH + DES-CBC, "cracked immediately"), `5/6/13`
  and `129/130/133` RA2/RA2ne/RA2_256 family (RealVNC: RSA key exchange →
  16-byte randoms → session keys = truncated SHA1/SHA256 → AES-EAX layer over
  the whole session; RA2ne encrypts only the handshake). `31–35` Apple Inc.,
  `18` TLS, `17` Ultra, others RealVNC/private.
- **Minimum viable set:** None + VNC-auth covers TigerVNC/x11vnc/QEMU/TightVNC
  defaults. RealVNC servers want RA2ne or VeNCrypt. macOS Screen Sharing offers
  type 2 but user-auth in practice needs type 30 (DH) or VeNCrypt-Plain —
  interop knowledge, not spec text (see §8).

### 2.3 Initialization and the update loop

- `ClientInit` = `U8 shared-flag`. `ServerInit` = `U16 w, h`, 16-byte
  `PIXEL_FORMAT` (bpp/depth/endian/true-colour + per-channel max/shift), `U32
  name-len` + name.
- Always send `SetPixelFormat` (client msg 0) for **32bpp true-colour
  depth-24** — every pixel becomes a fixed 4-byte unpack, a trivial blit into
  `image.NRGBA`. Constraint: no outstanding non-incremental FBUR when changing
  pixel format (rfbproto).
- `SetEncodings` (msg 2): `U16 count` + `S32[]` preference list — also the
  extension-negotiation channel (pseudo-encodings opt into server behaviors).
  The list is a *preference hint*, not a contract; Raw may always be sent.
  Strict refusal on unknown rect encodings is still a sound integrity check
  (real servers honor the list) but is an inference, not spec text.
- **Update loop is request-driven:** one non-incremental
  `FramebufferUpdateRequest` (msg 3) at start, then an incremental FBUR after
  applying each `FramebufferUpdate` (server msg 0 = `U16 n-rects`, then per
  rect `x,y,w,h + S32 encoding` + data). One FBU may answer several FBURs.

### 2.4 Message set

Client→server: `0` SetPixelFormat, `2` SetEncodings, `3` FBUR (incremental
flag), `4` KeyEvent (`down-flag` + `U32` X11 keysym; RFC §7.5.4 interop rules:
case matters, send Ctrl then 'a' for Ctrl-A, prefer shifted-Tab over
ISO_Left_Tab), `5` PointerEvent (`U8` button bitmask 1–8, `U16 x,y`; wheel =
press+release of buttons 4/5), `6` ClientCutText (Latin-1).
Server→client: `0` FBU, `1` SetColourMapEntries, `2` Bell, `3` ServerCutText.
Extension messages: client `150` EnableContinuousUpdates, `248` ClientFence,
`251` SetDesktopSize, `255` QEMU Client Message (submsg 0 = Extended Key
Event); server `150`/`248`/`255` counterparts.

### 2.5 Encodings, by decoder complexity

- **Mandatory:** Raw (0) — memcpy + pixel-format expand; CopyRect (1) —
  `U16 src-x,src-y`, ~10 lines.
- **Trivial:** RRE (2), CoRRE (4) — fill rects; zlib (6) — one persistent zlib
  stream.
- **Workhorse:** ZRLE (16) — one zlib stream of 64×64 tiles (raw / solid /
  palette packed 1,2,4-bit / plain RLE / palette RLE); ~300–500 lines; what
  QEMU and libvncserver default to. TRLE (15) shares the tile shape at 16×16
  uncompressed (details per original TRLE spec, not in rfbproto — §8).
- **Skip for v1:** Hextile (5, fiddly state), Tight (7, 4 zlib streams +
  Fill/JPEG/Basic filters, ~600–800 lines with `image/jpeg`), JPEG/H.264/Tight
  PNG.
- **Pseudo-encodings that matter:** ExtendedDesktopSize (-308) + client msg
  251 SetDesktopSize (live resize — big UX win); Cursor (-239) /
  CursorWithAlpha (-314) (local cursor — avoids pointer lag and double cursor);
  ContinuousUpdates (-313) + Fence (-312); QEMU Extended Key Event (-258) —
  **caveat:** spec says "XT keycode" but QEMU implements it as a QEMU keycode
  (≈ Linux input key number); encode QEMU keycodes or keys land wrong;
  ExtendedClipboard (0xc0a1e5ce) for >Latin-1 clipboard.

### 2.6 Interoperability snapshot

- **QEMU `-vnc`:** Raw, CopyRect, Hextile, zlib, Tight, TightPNG, ZRLE; rich +
  alpha cursor, both resize pseudo-encodings, ext key event, extended
  clipboard; auth None/VNC/RA2/Tight/TLS/VeNCrypt/SASL. Sweet spot for a VM
  console: ZRLE + ExtKeyEvent + ExtendedDesktopSize/SetDesktopSize. (QEMU
  *defines* RRE/CoRRE/XCursor constants but does not act on them.)
- **TigerVNC:** all standard encodings + JPEG/H.264 + Fence/ContinuousUpdates/
  ExtendedDesktopSize/CursorWithAlpha/QEMUKeyEvent; top-level auth = None,
  VncAuth, Tight, RA2 family, SSPI, VeNCrypt, DH, MSLogonII (TLS and Plain
  exist only *via* VeNCrypt subtypes).
- **x11vnc/libvncserver:** ZRLE/Tight/zlib/Hextile; VNC-auth or none.
- **UltraVNC:** MSLogonII or VNC-auth; tolerate unknown private rect types by
  not advertising them.
- **macOS Screen Sharing:** needs type 30 DH or VeNCrypt-Plain for
  username+password auth (see §8).

---

## 3. Go library landscape and recommended foundation

Surveyed module sources (all MIT, all cgo-free):

| Module | Latest | RFB | Auth (client) | Decodes to pixels | Notes |
|---|---|---|---|---|---|
| `unistack-org/go-rfb` (was vtolstov/go-vnc) | v0.0.1, 2026-11-15 | 3.3/3.7/3.8 | None, VNC-DES, VeNCrypt-Plain(256), Tight, ATEN | Raw, CopyRect, RRE, Hextile, TightPNG (PNG decode only — **not composited**); pseudo: DesktopSize, ExtDesktopSize, DesktopName, XCursor, LastRect, ContinuousUpdates | Ships `FrameBuffer.Composite(*FramebufferUpdate)` → `draw.Image`; channel/handler-based `Connect(ctx, conn, cfg)` API; zero deps; **recommended** |
| `tenthirtyam/go-vnc` | v1.0.2, 2026-11-09 | 3.8 only | None, VNC-DES + AuthRegistry | Raw, CopyRect, RRE, Hextile; Cursor/DesktopSize pseudo (`[]Color` per rect) | Modernized mitchellh API: `ClientWithOptions(ctx, conn, WithAuth…)`, timeouts, validation; **fallback** |
| `bhmj/vnc2video` | v0.0.2 | 3.8 | None, VNC-DES, VeNCrypt-Plain, Tight, ATEN | Raw, CopyRect, CoRRE, RRE, Hextile, Zlib, **ZRLE**, **Tight** → `draw.Image` (`SetTargetImage`) | The only lib whose ZRLE/Tight actually decode; reference to port ZRLE from; `icza/mjpeg` in go.mod (pure Go) |
| `bigangryrobot/go-vnc` | v0.1.5 | 3.3/3.8 | None, VNC-DES, VeNCrypt/TLS | + ZRLE, Tight | Only TLS-capable lib, but hard-codes X509Vnc + `InsecureSkipVerify` + panic; conn-swap template only |
| `kward/go-vnc` | 2026-05-15 | 3.3/3.8 | None, VNC-DES | Raw + DesktopSize only | Rich keysym/button constants packages |
| `mitchellh/go-vnc` | 2015 | 3.8 only | None, VNC-DES | Raw only | Canonical ancestor API; archived |
| `amitbet/vncproxy` | 2020 | 3.8 | None, VNC-DES | Captures raw bytes | Proxy/recorder shape, not a display client |

**Recommendation: embed `unistack-org/go-rfb`** — the only library providing
the framebuffer-composite → `draw.Image` loop `ui.Bitmap.Update` consumes, the
broadest maintained encoding coverage, zero deps, and a handler-chain design
that sits naturally behind an `adapters/vnc` port. Advertise **only** the
encodings `Composite` actually consumes (Raw, CopyRect, RRE, Hextile,
DesktopSize, ExtendedDesktopSize): TightPNG decodes via `png.Decode` but
`Composite` has no case for it, so those pixels are silently dropped. If the
channel-based API feels alien, `tenthirtyam/go-vnc` is the fallback (classic
`ClientConn.KeyEvent/PointerEvent` API, ~40 lines of manual rect compositing).
Either way you write yourself: the keysym table, pointer mapping, DesktopSize →
view resize plumbing, and TLS (VeNCrypt — via the conn-swap pattern — or
preferably SSH tunnel).

---

## 4. MyGo native rendering/input feasibility (v0.9.2)

**The decisive answer: everything needed exists as public API.**

### Rendering a framebuffer

- `ui.NewBitmap(image.Image)` (`ui/bitmap.go:73`) +
  `(*Bitmap).Update(img image.Image)` (`:159`) — doc: "shows img in place of
  the bitmap's picture, for one that changes as video does: the GPU keeps the
  bitmap's texture and uploads the new pixels." An `*image.RGBA` with `4*width`
  stride is taken **without a copy** — but "don't change its pixels until the
  next Update": the decode loop must double-buffer or allocate a fresh RGBA.
- `ui.Image(c, bitmap)` widget (`api_gen.go:1643`) with `Fit(Contain/Cover/
  FillBox/ScaleDown/NaturalSize)`; or `Painter.Image`/`ImageRegion`
  (`paint.go:1745,1774`) inside `Element.Draw` for overlays (cursor,
  letterbox). `Bitmap.Pixelated()` (`:66`) for crisp scaled pixels; mipmaps
  auto-maintained.
- `ui.Canvas` (`canvas.go`) — GPU render-to-texture ("as Skia's SkSurface,
  LÖVE's Canvas, raylib's RenderTexture2D") for compositing framebuffer +
  overlays.
- Pacing/threading: decode goroutine → `Window.Update(fn)` /
  `Window.Invalidate()` (`content.go:190,159`) marshals to the main thread;
  `Painter.AnimationFrame()`/`AnimationFrameRate(fps)` (`paint.go:155,170`)
  for continuous repaint. GPU rendering on all targets; `MYGO_UNCAPPED=1`
  removes vsync — the doc literally names "remote desktops" as the use case.
- Precedent: `plugins/game`'s `game.View` is literally
  `ui.Box.Focusable().AutoFocus().HandleInput(l.input).Draw(l.draw)` with
  `game.IntegerFit` for pixel-perfect letterboxing — a VNC viewer is that
  exact shape blitting a framebuffer. `plugins/terminal` wires
  `HandleInput`+`Draw` the same way (`view.go:46,48`).

### Input capture

- `Element.HandleInput` (`api_gen.go:5513`) delivers `InputEvent{Kind, Key,
  PhysicalKey, Code, Mods, Repeat, X, Y, Button, DX, DY, Precise, Momentum,
  Scale, ...}` on the main thread. `X,Y` are element-local.
- Pointer: `InputPointerMove` arrives with `Button:-1` when hovering
  (`input.go:479-481`) — required for VNC move events. Scroll =
  `InputScroll{DX,DY}` → wheel buttons 4/5. No pointer-leave event kind exists;
  use `PointerPosition()`'s `over` bool.
- Keyboard: `InputKeyDown/Up` carry `Key` (layout-dependent), `PhysicalKey`
  (US-position equivalent), and **`Code` — USB HID usage ID page 0x07 named
  after `KeyboardEvent.code`** (`internal/platform/keycode.go`), distinguishing
  modifier sides and numpad; docs/ui/input.md says "one table turns them into
  a guest's scancodes". Fallback order: `ev.Code` → `PhysicalKey` → `Key`
  (NB: `Text` is never populated on key down/up; it arrives as separate
  `InputText` events). `InputMethod(false)` disables IME so raw keysyms go
  through. `Code` may be `CodeUnknown` on some platforms/keys (macOS Fn,
  Windows Print Screen) — hence the fallback chain.
- HiDPI: `Painter.Scale()` (`glyphs.go:245`) gives device-pixels-per-DIP for
  1:1 sizing or rely on `Fit`/`Pixelated()`.

### What is missing

Nothing at the widget level. Missing pieces are **architectural** (§6): a
pixel-session port, a VNC host/credential spec type, a session lifecycle use
case, keysym tables, and a decision on the security story.

---

## 5. Fallback paths

### 5.1 noVNC in `ui.WebView`

- noVNC's reusable core is `core/rfb.js` (`export default class RFB`), driven
  as `new RFB(target, wsURL_or_WebSocket, {credentials:{password}})` — e.g.
  `vnc_lite.html` builds `ws://host:port/websockify`. It **requires WebSocket**
  (no raw TCP), sets `binaryType="arraybuffer"`, needs no subprotocol.
- Serving: `mygo.Protocol.Handle("novnc", mygo.FileServer(embedFS))` serves
  bundled `core/` + a thin `vnc.html` on a custom scheme;
  `win.NewWebView({URL:"novnc://localhost/vnc.html?…"})` + `ui.WebView(c, v)`
  — the same pattern `ui/browser/panel.go:527` already uses.
  `Event.EmitPage`/`mygo.Bind` can carry RFB events (bell, desktopname,
  clipboard, disconnect) back to Go.
- Bridge: `ws`/`wss` are in `reservedSchemes` (`protocol.go:34`), so the
  WS↔TCP proxy must listen on loopback TCP. ~100 lines: `coder/websocket`
  accept-loop copying binary frames into `net.Conn` — and when the target is
  an SSH host, swap `net.Dial` for `sshClient.DialContext` (the direct-tcpip
  channel `application/forwarding` already uses). `coder/websocket v1.8.15` is
  already an indirect dep.
- License: `core/` is **MPL-2.0** (not LGPL — websockify is the LGPL piece,
  and it is not needed); app HTML/CSS BSD-2, images CC-BY-SA, pako MIT.
  MPL-2.0-in-a-GPL-3.0-binary is a reasonable compatibility reading (FSF treats
  it as GPL-compatible); ship the license texts and source files in the binary.
- Precedent: KasmVNC bundles a noVNC fork served from a built-in HTTP+WS
  server — the same architecture.
- Open platform risk: whether a custom-scheme page may open `ws://127.0.0.1`
  differs per webview backend (§8); serving the page over `http://127.0.0.1`
  too is the conservative fix.

### 5.2 External viewer

`mygo.Shell.OpenExternal("vnc://host:590N")` / `LaunchApp` / `DefaultApp`
(`modules.go`), or `exec.Command` a detected TigerVNC `vncviewer`
(GPL-2.0, cross-platform; macOS additionally has built-in Screen Sharing via
the `vnc://` scheme; Remmina on Linux). Per-OS probe list + `vnc://` fallback.
Low effort, poor integration, but the natural partner for the SSH-tunnel
preset.

### 5.3 VNC-over-SSH preset

Conventional `ssh -L 5901:localhost:5901 host` is already first-class:
`domain/forwarding/rule.go` (`TypeLocal`, `DefaultBindAddress="127.0.0.1"`,
`LocalPort`/`RemoteHost`/`RemotePort`) driven by
`application/forwarding.Manager`, which opens `sshClient.DialContext`
direct-tcpip channels (`adapters/ssh/terminal.go`). The feature is mostly UI:
a "Connect VNC" action that (a) creates/starts a local rule
`127.0.0.1:<free> → localhost:5900+display` and (b) points the native viewer /
webview / external viewer at the local port. This is also the **security
answer**: RFB+VNC-auth is plaintext DES-challenge auth; tunneling through SSH
beats every VeNCrypt implementation that exists in Go. Competitive note:
mainstream terminals (e.g. Tabby) offer forwarding but no VNC client at all —
even tunnel+external-viewer exceeds the norm.

---

## 6. Proposed architecture

Following the existing split — protocol state machine in `domain/model/<p>`,
socket and glue in `adapters/<p>`, session lifecycle in `application/session`,
view in `ui/<feature>` (the telnet adapter is the template:
`adapters/telnet/telnet.go` owns the socket + lazy negotiation while
`domain/model/telnet` owns the parser/negotiator).

### 6.1 Domain

```
domain/model/rfb/          # pure Go, stdlib only — mirrors domain/model/telnet
  protocol.go              # version negotiation, message frame parsing
  pixelformat.go           # PIXEL_FORMAT struct, 32bpp-truecolour normalization
  security.go              # security-type registry, DES-reversed-key VNC auth
  encoding.go              # Raw/CopyRect decoders, rect dispatch
  keysym.go                # X11 keysym constants + interop rules (RFC §7.5.4)
```

Host model: a VNC target is a `Host` with `Protocol` (or a dedicated
`VNCConfig` on `StartSpec`, alongside `Serial`/`Telnet`/`Mosh`/`ET`):
hostname, port/display (`5900+N`), auth type (none / vnc-password /
vencrypt-subtype), and credential references into the existing vault — never
plaintext.

### 6.2 Ports — a second session kind

`appsession.Transport` is a byte stream and stays that way; VNC gets a peer
port rather than being shoehorned into `Transport`:

```go
// DisplaySession is one pixel session: framebuffer events in,
// input/resize/clipboard events out.
type DisplaySession interface {
    io.Closer
    Events() <-chan DisplayEvent      // FramebufferRect, Resize, Bell, CutText, CursorShape
    SendKey(keysym uint32, down bool)
    SendPointer(mask uint8, x, y uint16)
    RequestUpdate(incremental bool, r image.Rectangle)
    SetDesktopSize(w, h int) error    // only when -308 negotiated
    SendCutText(text string)
}

type DisplayEvent struct{ /* typed union mirroring appsession.Notice pattern */ }
```

### 6.3 Adapter

```
adapters/vnc/
  factory.go    # Factory.Open(ctx, spec) (DisplaySession, error) — same shape as
                # appsession.Factory; Dial seam for Network Provider / SSH lease
  conn.go       # TCP dial (or sshClient.DialContext direct-tcpip), read/write loops
  client.go     # wraps unistack go-rfb Connect + FrameBuffer; translate msgs <-> DisplayEvent
  auth.go       # credential fetch from vault; VeNCrypt/TLS conn-swap when implemented
```

The `Dial func(ctx, network, address) (net.Conn, error)` seam (as in
`adapters/telnet/telnet.go:39`) is what makes "VNC over SSH" free: the app
injects `sshClient.DialContext` when the Host rides an SSH connection, exactly
like the Network Profile seam.

### 6.4 Application

`application/vncsession` (or extend `application/session` with a Kind
discriminant): owns the session lifecycle, reconnect policy, connection-log
rows (`ConnectionEvent` already records HostID/Kind/outcome), and clipboard
sync policy. A `KindVNC` entry in the `Kind` enum
(`application/session/session.go:62`) keeps the model uniform, but the
transport interface differs — so either a parallel `DisplayService` or a
`Session` variant keyed on surface type. The session-title/status-bar/
hibernate concepts carry over; `Transport`-specific hooks (capture hook, codec,
scrollback) simply don't apply.

### 6.5 UI

```
ui/vnc/
  view.go     # VNCView: ui.Box().Focusable().AutoFocus()
              #   .HandleInput(rfbInput)          — InputKeyDown/Up -> Code->keysym
              #                                 —   table -> SendKey; pointer -> SendPointer
              #   .Draw(paint)                    — p.Image(fbBitmap, r, fit) + cursor overlay
  bitmap.go   # *image.RGBA double-buffer; decode goroutine ->
              # win.Update(func(){ fb.Update(img) })
  scale.go    # game.IntegerFit-style letterbox math; Painter.Scale() for 1:1
  clipboard.go# ClientCutText/ServerCutText <-> mygo clipboard
```

Session pane integration follows `ui/session/`: a VNC pane is just another
leaf in the workspace's Pane tree bound to a `DisplaySession` instead of a
terminal `Transport`.

### 6.6 Keysym mapping

`ev.Code` (HID usage ID) → QEMU-keycode or X11-keysym static table (~200
entries; kward/go-vnc ships X11 constants to seed it), fallback `PhysicalKey`
→ `Key`. Apply RFC §7.5.4 rules: fake shift presses for shifted symbols,
Ctrl+Alt+Del = three keysyms, prefer keysym-mode over `InputText`.

---

## 7. Phased effort estimate

For one engineer comfortable with binary protocols:

| Phase | Content | Effort |
|---|---|---|
| 0 — SSH preset | "Connect VNC" action: auto local rule to `localhost:5900+N` + external viewer launch | 2–4 days |
| 1 — Minimal native client | 3.8 handshake, None+VNC-auth, ServerInit, SetPixelFormat(32bpp), Raw+CopyRect, FBUR loop, key/pointer/cuttext; `VNCView` bitmap + input mapping | ~800–1200 LoC protocol + UI; **1–2 weeks** incl. keysym table |
| 2 — Production-grade | ZRLE (+port from bhmj/vnc2video or own), CursorWithAlpha local cursor, ExtendedDesktopSize + SetDesktopSize, ContinuousUpdates + Fence, clipboard, DesktopName | +2–4 weeks; total ~4–6k LoC |
| 3 — Security depth | VeNCrypt/TLS via conn-swap with real cert verification, or formalize SSH-tunnel as the documented story; maybe RA2ne, DH(30) for macOS Screen Sharing | ~1 week each auth type |
| Parallel — noVNC | embed core/, custom-scheme FileServer, loopback WS↔TCP bridge, thin vnc.html, event bridge | ~1 week |

Complexity concentrates in: ZRLE/Tight tile decoders (bounded), the ~10 auth
variants (reversed DES bits, AES-EAX hand-rolled — not in stdlib, ~100 lines
for CMAC too), keysym/interop edge cases, and version fallbacks.

---

## 8. Risks and "could not verify"

### Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Bitmap.Update throughput at full-screen 1080p+/4K damage | Laggy remote desktop | Prototype benchmark; dirty-region blit into RGBA then one Update is already optimal; `MYGO_UNCAPPED=1` for latency |
| Channel/handler API of unistack vs natcatty's port shape | Awkward adapter seam | Wrap behind `DisplaySession`; fall back to tenthirtyam's classic API |
| Auth zoo edge cases (reversed DES, AES-EAX, DH byte order) | Interop failures vs RealVNC/UltraVNC/ARD | Test vectors per type; SSH tunnel sidesteps most of it for v1 |
| Keysym/layout mismatches | Wrong keys on VMs | QEMU ExtKeyEvent with **QEMU keycodes** (not XT scancodes — spec vs implementation discrepancy); HID `Code` table |
| RFB plaintext by default | Credentials/desktop exposure | Default to SSH-tunnel transport; warn on direct VNC-auth |
| Pointer-leave / OS-reserved shortcuts (Cmd-Tab) never reach the window | Minor UX gaps vs native VNC clients | `PointerPosition().over`; document limitation |
| `Code` == `CodeUnknown` on some keys/platforms | Missing keysyms | Fallback chain Code → PhysicalKey → Key |
| unistack `Composite` drops TightPNG rects | Blank areas if advertised | Advertise only composited encodings, or blit `enc.Image` ourselves |
| mygo pre-1.0 (v0.9.2) API churn | Rework on upgrades | Isolate in `ui/vnc` + `adapters/vnc` |

### Could not verify (open questions)

- **Security story for direct VNC:** implement VeNCrypt X509Vnc/TLSVnc with
  real cert verification (bigangryrobot conn-swap pattern exists but needs
  `InsecureSkipVerify` removed and natcatty's trust store wired), or declare
  "VNC over SSH" the primary answer? Worth a product/ADR decision before
  scoping auth work.
- **macOS Screen Sharing:** rfbproto defines type 30 but does not state macOS
  *requires* it — "ARD needs username+password via type 30 or VeNCrypt-Plain"
  is interop lore, true in practice; ARD-specific extensions (drag-drop,
  multi-display) are undocumented. Needs live testing against a real Mac.
- **TRLE tile details:** registered in rfbproto by name only; the 16×16
  uncompressed tile description matches the original TRLE spec, not the cited
  source.
- **Extended Clipboard mechanics:** signed-S32-length + flags-header details
  are documented in UltraVNC docs, not confirmed in rfbproto's CutText
  sections.
- **WebView mixed content:** whether a page served from a custom scheme can
  open `ws://127.0.0.1` on each backend (WKWebView / WebView2 / WebKitGTK);
  WebView2's `http://<scheme>.localhost` mapping is an assumption. May force
  serving the page from `http://127.0.0.1` too.
- **`Window.NewWebView` requires native `Content`** (`webview.go`) — relevant
  if any natcatty surface ever shows a Page instead of native UI.
- **Session modeling:** new peer port alongside `appsession.Transport`
  (`DisplaySession`) vs an extensibility point for non-terminal session kinds —
  an architecture decision, not a blocker.
- **`Code` population completeness** across platforms for every physical key.
- **Bitmap.Update at 4K + per-frame full damage** — needs a benchmark.
- **GPL-3.0 bundling judgment for MPL-2.0 noVNC** — reasonable legal reading,
  not a stated fact; ship license texts.

---

## 9. Sources

### Protocol specifications

- RFC 6143, The Remote Framebuffer Protocol:
  <https://www.rfc-editor.org/rfc/rfc6143.txt> (§7.1 handshake, §7.2 security
  types, §7.3 init, §7.4 PIXEL_FORMAT, §7.5–7.6 message tables, §7.7 encodings,
  §7.8 pseudo-encodings, §8.1 IANA registry, App. A version deltas; 5900+N
  port convention)
- rfbproto community spec (master `rfbproto.rst`): security-type table
  (~L367–406), pseudo-encoding table (~L3064), extension messages 150/248/251/
  255, VNC Authentication reversed-DES-bits note, "Version 3.7 onwards"
  handshake, ZRLE/Tight sections, Diffie-Hellman (type 30) section, RA2
  RSA+AES-EAX sections, VeNCrypt subtypes 256–267, QEMU ExtKeyEvent "XT
  keycode" (~L2604), SetPixelFormat outstanding-FBUR constraint:
  <https://github.com/rfbproto/rfbproto>
- QEMU source: `ui/vnc.h` (VNC_ENCODING_*/VNC_AUTH_* constants) and
  `ui/vnc.c` (`set_encodings` switch; `do_key_event` →
  `qemu_input_key_number_to_linux`): <https://github.com/qemu/qemu>
- TigerVNC source: `common/rfb/encodings.h`, `common/rfb/Security.cxx`
  (`secTypeNum` list): <https://github.com/TigerVNC/tigervnc>

### Go libraries (module zips, proxy.golang.org)

- `github.com/unistack-org/go-rfb` v0.0.1 (2026-11-15; renamed from
  vtolstov/go-vnc — `go.mod` declares the new path): `framebuffer.go:29-282`
  (`NewFrameBuffer`, `Composite`, `Screenshot`, `compositeRectLocked` —
  no TightPNG case), `encoding_*.go` (raw/copyrect/rre/hextile/tightpng via
  `png.Decode`; `encoding_tight.go` stub), `security_*.go` (none/vnc/
  vencryptplain:256/tight/aten), `client.go`, `messages.go`. MIT.
- `github.com/tenthirtyam/go-vnc` v1.0.2 (2026-11-09): `client.go:130-282`
  (`ClientWithContext`/`ClientWithOptions`/`WithAuth`/`WithAuthRegistry`/
  `WithTimeout`), `auth.go:222-242`, `encoding_hextile.go`, `validation.go`;
  `go 1.26.10`. MIT.
- `github.com/bhmj/vnc2video` v0.0.2 (2025-04-27): `encoding_zrle.go:14-60`
  (`ZRLEEncoding{Image draw.Image}` + `SetTargetImage`), `encoding_tight.go`
  (real Fill/JPEG/filter decode); `require github.com/icza/mjpeg`. MIT.
- `github.com/bigangryrobot/go-vnc` v0.1.5: `vencrypt.go:56-77`
  (`ClientAuthVeNCryptAuth`, hard-coded subtype 261 X509Vnc,
  `InsecureSkipVerify`, panic on error); `golang/glog` dep. MIT.
- `github.com/kward/go-vnc` (2026-05-15): `encodings.go:59-145` (Raw +
  DesktopSize only), `server.go:386` (unused `colorsToImage`), `keys` pkg. MIT.
- `github.com/mitchellh/go-vnc` (2015-06-29): `client.go:317-324` (3.8-only),
  `encoding.go` (Raw only), `client_auth.go` (None + DES). MIT, archived.
- `github.com/amitbet/vncproxy` (2020): `encodings/enc-*.go` (raw-byte
  capture + `WriteTo` replay). MIT.

### MyGo v0.9.2 (module cache + docs)

- `ui/bitmap.go:73` NewBitmap, `:128` DecodeBitmap, `:159` Update (video-rate
  texture reuse, zero-copy *image.RGBA, "don't change its pixels" caveat),
  `:66` Pixelated; `ui/yuv.go:74` UpdateYUV
- `ui/handler.go:73-145` InputEvent + HandleInput (public `api_gen.go:5513`);
  `:196` InputMethod; `ui/input.go:479-481` InputPointerMove Button:-1,
  `:115-118` PointerLeave clears hover (no leave event), `:1113-1118` key
  events never carry Text
- `internal/platform/keycode.go:4-7` KeyCode = USB HID page 0x07;
  `ui/keys.go:98-104`
- `ui/paint.go:155,170` AnimationFrame/AnimationFrameRate; `:1745,1774`
  Painter.Image/ImageRegion; `content.go:159,190` Invalidate/Update;
  `ui/glyphs.go:245` Painter.Scale; `ui/element.go:760-771` Fit constants
- `ui/canvas.go` Canvas render-to-texture; `plugins/game/game.go:72-106,228,
  243` game.View/Input/IntegerFit; `plugins/terminal/view.go:46-48,609-853`
  HandleInput+Draw wiring
- `webview.go:62` NewWebView (requires native Content); `api_gen.go:3254`
  ui.WebView; `protocol.go:34` reservedSchemes (ws/wss reserved), `:53`
  Protocol.Handle, `:344` FileServer; `modules.go` ShellModule.OpenExternal/
  LaunchApp/DefaultApp; `ipc.go:541` Event.EmitPage
- docs: `docs/ui/image.md`, `docs/ui/input.md:715-733` ("one table turns them
  into a guest's scancodes"), `docs/ui/rendering.md` (GPU backends,
  MYGO_UNCAPPED "remote desktops" :58-60), `docs/architecture.md:25` ("No
  cgo"), `docs/plugins/game.md`
- <https://github.com/egoist/mygo> (v0.9.2)

### natcatty repo (this worktree)

- `application/session/session.go:302-307` Transport (byte stream +
  Resize(cols,rows)); `:62-76` Kind enum; `:383-453` StartSpec (Serial/Telnet/
  Mosh/ET config pattern); `:476-478` Factory
- `adapters/telnet/telnet.go` — adapter template: Factory + Dial seam +
  domain-state-machine split (`domain/model/telnet`)
- `adapters/ssh/terminal.go:45` — `sshClient.DialContext` direct-tcpip;
  `application/forwarding/manager.go`; `domain/forwarding/rule.go` (TypeLocal,
  DefaultBindAddress 127.0.0.1, LocalPort/RemoteHost/RemotePort)
- `ui/browser/panel.go:527` — `ui.WebView` placement precedent
- `go.mod` — `github.com/egoist/mygo v0.9.2`, `go 1.27.4`,
  `github.com/coder/websocket v1.8.15` (indirect); `justfile` CGO_ENABLED=0
- `GLOSSARY.md` — Host/Session/Pane/Workspace/Port Forwarding Rule vocabulary

### noVNC / websockify / viewers

- noVNC: <https://github.com/novnc/noVNC> — README ("does require WebSockets
  support"), `core/rfb.js` (`export default class RFB`), `core/websock.js`
  (`binaryType="arraybuffer"`, no required subprotocol), `vnc_lite.html`
  (`new RFB(el, url, {credentials:{password}})`), `docs/API.md`,
  `LICENSE.txt` (core MPL-2.0; app BSD-2/CC-BY-SA/OFL/MIT)
- websockify: <https://github.com/novnc/websockify> — COPYING = LGPL-3.0
- KasmVNC (bundled noVNC fork + built-in HTTP/WS server, GPL-2.0):
  <https://github.com/kasmtech/KasmVNC>
- TigerVNC viewer (GPL-2.0): <https://github.com/TigerVNC/tigervnc>;
  macOS universal binary per tigervnc.org
- Tabby (forwarding, no VNC client): <https://github.com/Eugeny/tabby>
