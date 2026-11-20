# go-rfb v0.0.1 gap analysis vs full auth depth + noVNC parity

Status: resolved (ticket `.scratch/vnc/issues/02-go-rfb-gap-analysis.md`)
Date: 2026-11-20
Scope: what `github.com/unistack-org/go-rfb` v0.0.1 actually ships vs what the
VNC effort committed to — auth depth (None, VNC-auth DES, Apple DH type 30,
VeNCrypt/TLS, RA2 family) and noVNC-parity UX — and whether go-rfb is the right
foundation on the `Manager.DialStream` `net.Conn`.

Module under test: `github.com/unistack-org/go-rfb@v0.0.1` (proxy.golang.org zip,
`Origin.Hash fc2c5a2c396f`, tag 2026-11-15; `go.mod` says `go 1.21`, zero deps,
LICENSE = MIT). Renamed from `vtolstov/go-vnc`. ~7.3k LoC incl. tests,
examples, and an ATEN iKVM private-protocol stack we do not need.

Specs checked against: RFC 6143, `rfbproto/rfbproto` master `rfbproto.rst`
(4963 lines; security-type table L367–400, VeNCrypt L661–807, RA2 family
L1099–1334, DH L1336–1372, extension messages 150/248/250/251/255, pseudo-enc
table L3064+, CursorWithAlpha L4465, ExtendedClipboard L4815).

---

## 1. Auth gap table

### 1.1 What ships

`security.go:3-50` defines `SecurityType`/`SecuritySubType` and the plug point:

```go
type SecurityHandler interface {
    Type() SecurityType
    SubType() SecuritySubType
    Auth(Conn) error
}
```

Shipped handlers (all `security_*.go`):

| Handler | Type | SubType | Notes |
|---|---|---|---|
| `ClientAuthNone` | 1 None | — | `security_none.go` |
| `ClientAuthVNC` | 2 VNC | — | `security_vnc.go:60-92`; DES with reversed key bits `AuthVNCEncode:94-120` — correct per rfbproto |
| `ClientAuthVeNCrypt02Plain` | 19 VeNCrypt | 256 Plain | `security_vencryptplain.go` — **wire-incorrect, see below** |
| `ClientAuthATEN` | 16 Tight slot (const alias `SecTypeATEN=16`) | — | `security_aten.go`; ATEN iKVM private auth, not the rfbproto Tight caps handshake |
| Tight caps helpers | — | — | `security_tight.go` only has `readTightTunnels`/`readTightCaps` readers used by the ATEN path |

So out of the committed auth list: **None ✓, VNC-auth ✓, everything else ✗.**

### 1.2 `ClientAuthVeNCrypt02Plain` is not usable as-is

`security_vencryptplain.go:23-109` deviates from rfbproto L661-807:

- It sends `{0,2}` then reads server `{major, minor}` — but per spec the
  **server** sends its version first, then the client replies. Reversed order
  still happens to work only because both sides write then read; acceptable.
- It then writes `uint8(1)` + `SubType` and reads back a `U32` expecting it to
  equal `auth.SubType()` — but the spec order is: server sends `U8 n` +
  `U32[]` subtype list, client picks one `U32`, server acks `U8` for TLS/X509
  subtypes. The implementation skips reading the subtype **list** entirely and
  fabricates a `n=1, [256]` response. Works only against a server that offers
  exactly Plain; breaks otherwise.
- Credential exchange is inverted: it **reads** `u32 ulen, u32 plen, username,
  password` from the server and compares them to its own credentials
  (`security_vencryptplain.go:89-107`). Spec says the **client sends**
  `u32 ulen, u32 plen, username, password`. This is a server-side auth routine
  pasted into a client struct. It cannot succeed against a real VeNCrypt
  server.
- No TLS path exists at all — no `crypto/tls` import anywhere in the module.

**Verdict: VeNCrypt must be written fresh.** The 256-Plain subtype is the only
one scaffolded and even that is wrong; TLS/X509 subtypes (257-264) need a
conn-swap the current `Conn` interface does not support cleanly (§1.4).

### 1.3 Wire summaries for the missing types

**Apple DH (type 30)** — rfbproto L1336-1372:

```
server → client: U16 generator, U16 key-size, key-size[] prime, key-size[] server-public
client: shared = server-public^priv mod prime;  AES key = MD5(shared)
client → server: 128B AES-ECB(key, username64|NUL-pad || password64|NUL-pad), key-size[] client-public
then SecurityResult
```

Stdlib has `crypto/md5`, `crypto/aes` (ECB must be hand-rolled by looping
`Encrypt` — trivial), `math/big` for DH. ~120-150 LoC. Layers cleanly over the
`Connect`/conn seam as a `SecurityHandler` (needs only `Read`/`Write`/`Flush`).

**VeNCrypt (type 19)** — rfbproto L661-807:

```
server → client: U8 major=0, U8 minor=2
client → server: U8 major, U8 minor            (≤ server's)
server → client: U8 ack (0 ok)
server → client: U8 n, n×U32 subtypes
client → server: U32 chosen subtype
[TLS/X509 subtypes] server → client: U8 ack(1), then TLS handshake on the same conn
inner auth: Plain → client sends u32 ulen,u32 plen,uname,pw;  Vnc → VNC-auth;  None → nothing
then SecurityResult (inside TLS channel when applicable)
```

Negotiation itself is ~80 LoC. The hard part is the **TLS conn-swap**: after
subtype ack, `tls.Client(rawConn)` must replace the transport mid-`Auth()`.
`Auth(Conn)` receives the `ClientConn` wrapper, not the raw socket — but
`Conn.Conn() net.Conn` (`conn.go:11`) exposes it, and `ClientConn` wraps it in
`bufio.Reader/Writer` (`client.go:231-232`). Swapping the underlying
`net.Conn` for `*tls.Conn` inside `Auth` does not re-wrap the outer
`bufio.Reader`, so buffered plaintext reads post-handshake are fine (TLS
starts on a clean boundary) **but** all subsequent reads go through the old
`br` bound to the raw conn. Needs either (a) a `SecurityHandler` that calls
`c.Conn()` → `tls.Client` → then a `Read`/`Write` shim on `ClientConn` — not
possible without fork since `c.c`/`c.br` are private — or (b) fork to add a
`SetConn(net.Conn)`/`RebindTLS` hook. **Fork-level change for TLS subtypes.**

**RA2 family (types 5/6/13, 129/130/133)** — rfbproto L1099-1334:

```
server → client: U32 server-key-len, modulus[], exponent[]     (RSA pub)
client → server: U32 client-key-len, modulus[], exponent[]     (client RSA pub)
server → client: U16 len, RSA(client-pub)-encrypted 16B serverRandom
client → server: U16 len, RSA(server-pub)-encrypted 16B clientRandom
derive: ClientSessionKey = SHA1|SHA256(ServerRandom||ClientRandom)[:16]
        ServerSessionKey = SHA1|SHA256(ClientRandom||ServerRandom)[:16]
all further msgs: U16 msg-len, enc(msg), 16B MAC   — AES-EAX (AES-CTR+CMAC),
  nonce = 16B LE msg-index++ , AD = 2B len
server → client: enc(SHA1|256(ServerPub||ClientPub)) + MAC
client → server: enc(SHA1|256(ClientPub||ServerPub)) + MAC
server → client: enc(U8 subtype: 1=user+pw, 2=pw only) + MAC
client → server: enc(U8 ulen, uname, U8 plen, pw) + MAC
then encrypted SecurityResult — EXCEPT RA2ne (6/130): handshake only, rest plaintext;
      RA2r (13/133): second AES-EAX-encrypted kex round before session keys.
```

Crypto cost: RSA + PKCS1v15 (`crypto/rsa`, `crypto/x509` or manual big.Int) ✓
stdlib; SHA1/256 ✓. **AES-EAX is NOT in stdlib** — `crypto/aes` gives the
block cipher, CTR is in `crypto/cipher` (`NewCTR`), but **CMAC must be
hand-rolled** (~80-120 LoC: subkey gen with Rb=0x87 doubling + OMAC padding)
and EAX framing is ~60 LoC on top of CTR+CMAC. The encrypted-session layer
means RA2 (5/129) and RA2r (13/133) need the **same conn-swap problem as TLS**
— a framing/encrypting `net.Conn` shim between `ClientConn`'s bufio and the
socket for all post-handshake traffic → **fork-level**. RA2ne (6/130) only
encrypts the handshake, so `Auth()` can do the whole exchange itself and hand
back a plaintext conn — the one RA2 variant that layers cleanly without
forking; still needs CMAC+EAX (~200 LoC crypto + ~150 handshake).

### 1.4 Layer-vs-fork verdict per type

| Type | In go-rfb? | Layer over `Connect`/conn seam? | Fork needed? |
|---|---|---|---|
| None (1) | ✓ `ClientAuthNone` | shipped | — |
| VNC-auth (2) | ✓ `ClientAuthVNC` | shipped | — |
| Apple DH (30) | ✗ | yes — pure `SecurityHandler`, stdlib crypto | no |
| VeNCrypt Plain (256) | scaffolded but broken | write-fresh `SecurityHandler` (~100 LoC) | no |
| VeNCrypt TLS/X509 (257-264) | ✗ | needs conn-swap → re-wrap bufio | **yes** |
| RA2ne (6/130) | ✗ | handshake self-contained in `Auth()`; needs CMAC+EAX | no |
| RA2 / RA2r (5/13/129/133) | ✗ | needs session-encrypting conn layer | **yes** |

Fork surface for the swap is small: `ClientConn` needs a way to replace its
`c net.Conn` + rebuild `br/bw` (or `Auth` returns a `net.Conn` the handler
loop binds). ~30 LoC of new API. Alternative that avoids the fork: implement
the security handshake **outside** go-rfb on the raw `DialStream` conn before
handing an already-secured conn to `Connect` — i.e. do version+security
negotiation ourselves, then give `Connect` a conn positioned at ClientInit
with a `ClientAuthNone` handler. That works for TLS (swap before `Connect`)
and RA2 session encryption (wrap conn in the EAX layer before `Connect`), and
keeps go-rfb as a pure post-auth protocol engine. **This is the recommended
seam** — it also matches the adapter design where auth is a vault/credential
concern that precedes session start.

---

## 2. Encoding gap — `FrameBuffer.Composite` vs noVNC parity

### 2.1 What `Composite` actually consumes

`framebuffer.go:99-149`: `compositeRectLocked` handles:

- `EncDesktopSizePseudo` (-223), `EncExtendedDesktopSizePseudo` (-308) → `resizeLocked`
- `EncCopyRect` → `copyRectLocked` (with overlap staging)
- `EncRaw`, `EncHextile`, `EncRRE` → `blitLocked` of `[]Color`
- `*AtenHermon` → ATEN sub-rect composite

Rectangle decode (`image.go:170-231`) instantiates encoders for: CopyRect,
Hextile, RRE, Tight (stub — `encoding_tight.go:36-39` `Read` is a no-op,
consumes **zero bytes** so the stream desyncs if a real Tight rect arrives;
README admits this), TightPng (decodes PNG via `png.Decode` into `enc.Image`
but `Composite` has no case for it → pixels dropped), Raw, DesktopSize,
DesktopName, XCursor, ATEN Hermon, LastRect, QEMU PointerMotion, QEMU
ExtendedKeyEvent, QEMU Audio, ExtendedDesktopSize, ContinuousUpdates.
Anything else → `unsupported encoding` error kills the FBU.

So the live decode set is **Raw + CopyRect + RRE + Hextile** (pixels) plus a
set of **side-channel pseudo-encodings** (decoded, not composited — Cursor,
XCursor, DesktopName, LEDState, QEMU pseudo-rects, ContinuousUpdates empty
rects).

### 2.2 ZRLE port

`bhmj/vnc2video@v0.0.2/encoding_zrle.go` (361 LoC) is a real decoder: zlib
stream persisted across rects (`enc.unzipper`), 64×64 tiles, subencodings
raw/solid/palette-packed/palette-RLE/plain-RLE, writes into `draw.Image` via
`SetTargetImage`. Porting into go-rfb = adapt to the `Encoding` interface +
either emit `[]Color` (extra copy) or add a `Composite` case for a ZRLE
encoder carrying its own `draw.Image` (cleaner — one branch). vnc2video's
`readCPixel`/CPixel handling is directly reusable. **Est. 350-450 LoC ported**
(mostly mechanical translation off `logger`/`Conn` differences), zlib is
stdlib. Do it — ZRLE is the QEMU/libvncserver workhorse.

### 2.3 Tight/JPEG for parity

Tight (`encoding_tight.go`) is a stub; TightPNG decodes but isn't composited.
vnc2video `encoding_tight.go` is 757 LoC with real Fill/JPEG/Basic+gradient/
palette filters and 4 zlib streams — portable at ~600-750 LoC.

**Is Tight required for parity?** noVNC supports it, but parity is a UX bar,
not an encoding checklist. QEMU defaults to Tight but always accepts ZRLE;
TigerVNC/x11vnc take ZRLE. Since we advertise the encoding list, we only
receive what we ask for — **Tight is optional for v1; ZRLE covers the same
bandwidth class for non-photo content.** The only real loss is JPEG-compressed
photo/video regions (Tight's JPEG path). Recommendation: ship ZRLE first, keep
Tight as a later port if remote-photo UX matters; never advertise it while
the stub no-ops.

---

## 3. Pseudo-encodings for the MVP

| Feature | Enc / msg | In go-rfb? | Gap |
|---|---|---|---|
| CursorWithAlpha | -314 | **✗** — `EncCursorPseudo` -239 decodes pixels+bitmask but drops alpha-application (`encoding_cursor.go:16-43`, mask read but unused); no -314 constant | write-fresh: decode +S32 sub-encoding + RGBA-premult pixels → cursor sprite; ~120 LoC + overlay blit |
| ExtendedDesktopSize | -308 | ✓ decoded (`encoding_extendeddesktopsize.go`), `Composite` resizes | — |
| client SetDesktopSize | msg 251 | **✗** — `ClientMessageType` iota ends at 6 (`messages.go:34-42`); no type-251 msg, `ClientMessage.Type()` returns `ClientMessageType`(u8) so **any msg ≥128 overflows the uint8 iota domain** — needs a custom `ClientMessage` impl; interface allows it (`Write(c Conn)` writes arbitrary bytes) but `Type()` can't express 251 | write-fresh `ClientMessage` (~80 LoC) — `Type()` field widened to u8 raw value works since `Write` just emits `uint8(Type)` |
| ContinuousUpdates | -313 + msg 150 | pseudo-enc ✓ (`encoding_continuousupdates.go`); **EnableContinuousUpdates client msg 150 ✗**; `EndOfContinuousUpdates` server msg 150 **✗** (not in `DefaultServerMessages`, and unknown server msg-type → `unknown message-type` error in the read loop `client.go:349`) | write 2 msg structs (~100 LoC) + register in `cfg.Messages` |
| Fence | -312 + msg 248 | const only (`encoding.go:70`); no encoding impl, no ClientFence/ServerFence msgs | write: fence pseudo (empty rect) + 2 msgs (~140 LoC); needed if we do SetDesktopSize+SetPixelFormat safely under continuous updates — defer unless resize lands |
| ExtendedClipboard | 0xc0a1e5ce | **✗** — no const; `ClientCutText`/`ServerCutText` are unsigned-len Latin-1 only | write: pseudo-enc const + signed-length cut-text variant + flags/caps/request/notify/provide zlib handling (~250 LoC); defer past MVP unless clipboard parity demanded |
| QEMU ExtendedKeyEvent | -258 + msg 255 sub 0 | pseudo-enc ✓ (`encoding_qemu_extendedkey.go`); **QEMU client msg 255 ✗** | write 1 msg (~70 LoC): U8 255, U8 0, U16 down, U32 keysym, U32 QEMU-keycode |

Pattern: every pseudo-*advertisement* ships as an empty-rect `Encoding`, but
**none of the matching client/server extension messages exist** — the message
set stops at core RFB 3.8. The good news: `ClientMessage`/`ServerMessage` are
interfaces and `cfg.Messages`/`cfg.ClientMessageCh` are open sets, so new
messages plug in without touching the library — **except** the read loop
errors on unregistered server types (`client.go:347-351`), so `EndOfContinuous-
Updates`, `ServerFence`, `ExtendedClipboard ServerCutText` (same msg type 3,
different framing — needs a negotiated-format flag) must be registered/handled.

---

## 4. Embedding posture

- **License**: MIT (`LICENSE`, Mitchell Hashimoto / Kate Ward / Vasiliy Tolstov
  / unistack copyrights). GPL-3.0-compatible, zero friction.
- **Deps**: zero (`go.mod` = module line + `go 1.21`). cgo-free.
- **API seams that are genuinely open**: `Connect(ctx, net.Conn, *ClientConfig)`
  takes our `DialStream` conn directly; `ClientConfig.SecurityHandlers` is a
  `[]SecurityHandler` — custom `Auth(Conn)` handlers plug in without fork;
  `Encodings []Encoding` + `Messages []ServerMessage` + `ClientMessageCh`
  are open lists; `FrameBuffer`/`Composite`/`Screenshot` is a clean
  `draw.Image` target → `ui.Bitmap.Update` path.
- **API seams that are closed / buggy**:
  - `DefaultClientSecurityHandler.Handle` (`handlers.go:121-171`) picks
    `cfg.SecurityHandlers[0]` for the wire (`binary.Write(c, ...Type())`) while
    scanning all handlers into `secType` — so the advertised pick ≠ the matched
    pick unless you order the slice so index 0 wins; and `secType` stays the
    **last** match in iteration order. Effectively: pass exactly one handler,
    or fork. Also no 3.3 security (`U32` type) path — assumes 3.8 `U8`-list
    negotiation only; servers offering only 2 in the list → `secType` nil →
    nil-pointer `secType.Auth`. Manageable: one `SecurityHandler` per dial.
  - `DefaultClientMessageHandler` (`client.go:274-365`) hardcodes the
    auto-FBUR-after-every-FBU behavior (`readAndRequestNext:245-268`) — fine
    for normal use, fights a ContinuousUpdates model where FBURs are ignored
    anyway (harmless) but means we can't suppress the FBUR stream without a
    custom `Handler` (which is just `Handle(Conn) error` — we can replace the
    whole default message handler with our own loop; `cfg.Handlers` is the
    override point). So even the message loop is replaceable without a fork.
  - No `SetConn`/transport-rebind → TLS/RA2 session layers can't be injected
    inside `Auth`; resolved by pre-negotiating security on the raw conn and
    handing `Connect` an already-wrapped conn + `ClientAuthNone`.
- **Verdict on embedding**: the channel/handler shape is awkward but every
  piece we need to replace (security handler, encodings, messages, even the
  whole message loop) is a public interface field — **embedding is viable
  without forking**, provided auth is negotiated before `Connect` and we write
  our own message-handler `Handler` once ContinuousUpdates/Fence/clipboard
  land.

---

## 5. Verdict — go-rfb vs focused client on the `DialStream` conn

**Conditional GO for go-rfb** as the post-auth protocol + framebuffer engine,
with auth and extension messages written by us either as plug-in handlers
(Apple-DH, RA2ne, VeNCrypt-Plain) or pre-`Connect` wire negotiation (VeNCrypt
TLS/X509, RA2 session-encrypted) on the `DialStream` conn. Not a drop-in: its
only TLS-capable auth is broken, the security-type pick has a first-element
bug, and every extension message is missing. The alternative — a focused
client — rewrites the ~1.2k LoC of message/encoding/state plumbing go-rfb
already has *plus* the ~400-line `FrameBuffer.Composite`, for no gain on the
hard parts (auth depth, ZRLE, Tight are missing either way).

### Gap table

| Feature | Status | Path | Est. LoC |
|---|---|---|---|
| Handshake 3.8, ServerInit, msg loop | shipped | — | 0 |
| Auth None / VNC-DES | shipped | — | 0 |
| Auth Apple-DH 30 | missing | write `SecurityHandler` (stdlib md5/aes/big) | ~150 |
| Auth VeNCrypt-Plain 256 | broken impl | rewrite `SecurityHandler` | ~100 |
| Auth VeNCrypt TLS/X509 257-264 | missing | pre-Connect negotiate + `tls.Client` swap | ~200 + tls config |
| Auth RA2ne 6/130 | missing | `SecurityHandler` + CMAC/EAX crypto | ~350 |
| Auth RA2/RA2r 5/13/129/133 | missing | pre-Connect kex + EAX conn wrapper | ~500 (shared w/ RA2ne crypto) |
| CMAC + AES-EAX primitives | missing (not stdlib) | write-fresh (`crypto/cipher` CTR + own CMAC) | ~200 shared |
| Raw/CopyRect/RRE/Hextile decode→fb | shipped | — | 0 |
| ZRLE | missing | port bhmj/vnc2video `encoding_zrle.go` + `Composite` case | ~400 |
| Tight(+JPEG) | stub/no-op | port vnc2video `encoding_tight.go` — **optional** | ~700 |
| CursorWithAlpha -314 | missing | write enc + sprite overlay | ~150 |
| ExtDesktopSize -308 / DesktopSize -223 | shipped | — | 0 |
| SetDesktopSize msg 251 | missing | write `ClientMessage` | ~80 |
| ContinuousUpdates -313 + msg 150 pair | pseudo only | write 2 msgs + maybe own msg loop | ~150 |
| Fence -312 + msg 248 pair | const only | write 2 msgs + pseudo | ~140 |
| ExtendedClipboard 0xc0a1e5ce | missing | write enc + cut-text variant | ~250 |
| QEMU ExtKeyEvent -258 + msg 255 | pseudo only | write 1 msg | ~70 |
| Security-type pick bug / conn-rebind | in src | workaround: 1 handler per dial + pre-Connect auth | ~50 |
| **Total to write/port** | | | **~3.1k** (excl. optional Tight) |

vs focused-client alternative: ~2.4-3.0k to replicate what go-rfb already
ships *plus* the same ~3.1k of genuinely-new code → go-rfb saves roughly the
protocol-plumbing half and brings `FrameBuffer`/`Screenshot` tests.

### Caveats the verdict hangs on

1. Auth-before-Connect seam must be proven: server speaks 3.8, we negotiate
   version+security on the raw conn, then `Connect` must **skip re-doing**
   version+security. `DefaultClientVersionHandler`/`DefaultClientSecurityHandler`
   are separate `Handler`s — `cfg.Handlers` lets us pass only
   `[ClientInitHandler, ServerInitHandler, MessageHandler]`… but the shipped
   `DefaultClientClientInitHandler` etc. assume the prior handlers ran; they
   don't depend on security state, so a 2-element handler chain post-auth
   works. Confirm with a live server in the prototype.
2. Message loop is all-or-nothing (`Handle` runs both goroutines + wg.Wait);
   we either take its auto-FBUR loop or write our own ~120-LoC replacement —
   needed anyway for Fence/SetDesktopSize ordering.
3. go-rfb is a 3-day-old v0.0.1 rename of a 2017 codebase with ATEN-specific
   grafts; treat as vendored-quality — pin the version, keep the adapter thin.

---

## 6. Sources

- Module source: `github.com/unistack-org/go-rfb@v0.0.1` — `security.go:3-50`,
  `security_vencryptplain.go:23-109`, `security_vnc.go:94-120`, `client.go:
  24-49,245-268,274-365,369-382`, `conn.go:9-31`, `handlers.go:121-171`,
  `framebuffer.go:99-149`, `image.go:170-231`, `encoding.go:14-72`,
  `encoding_tight.go:36-39`, `encoding_tightpng.go:59-85`,
  `encoding_extendeddesktopsize.go`, `encoding_continuousupdates.go`,
  `encoding_qemu_extendedkey.go`, `encoding_cursor.go:16-43`,
  `pixel_format.go`, `README.md` (encoding/security tables), `LICENSE` (MIT).
- `github.com/bhmj/vnc2video@v0.0.2` — `encoding_zrle.go:14-361`,
  `encoding_tight.go` (757 LoC, zlib+JPEG+filters).
- rfbproto `rfbproto.rst` master — security table L367-400, VeNCrypt L661-807,
  RA2/RSA-AES L1099-1334, DH L1336-1372, EnableContinuousUpdates L2059,
  ClientFence L2101, SetDesktopSize L2213, QEMU Client Msg L2547,
  CursorWithAlpha L4465, ExtendedClipboard L4815, pseudo-enc table L3064+.
- RFC 6143 — §7.1 handshake/security negotiation, §7.2 security types.
- Prior survey: `docs/research/vnc-client-feasibility.md` §3, §5.3, §7.
