# VNC key/pointer mapping spec — MyGo `InputEvent` → RFB KeyEvent / QEMU ExtendedKeyEvent

Wayfinding answer for `.scratch/vnc/issues/03-keysym-input-mapping.md`.
Consumers: `ui/vnc` input layer + `domain/model/rfb/keysym.go` (+ `qnum.go`).

Sources (primary only):

- MyGo v0.9.2 module `~/go/pkg/mod/github.com/egoist/mygo@v0.9.2/`:
  `ui/handler.go` (`InputEvent` fields), `internal/platform/keycode.go` (`KeyCode` enum + platform→code tables), `internal/platform/surface.go:483` (`ModShift/Ctrl/Alt/Super` bits), `ui/input.go:479` (hover `Button:-1`), `internal/platform/surface.go:410` (`Button` 0/1/2 = primary/secondary/middle)
- RFC 6143 §7.5.4 (KeyEvent + interop rules), §7.5.5 (PointerEvent)
- rfbproto.rst (QEMU Extended Key Event Message, pseudo-encoding -258)
- QEMU v8.2.0 `ui/vnc.c` (`ext_key_event` → `qemu_input_key_number_to_qcode`), `ui/input-keymap.c`
- noVNC v1.5.0 `core/input/keyboard.js`, `core/input/util.js`, `core/input/xtscancodes.js`, `core/rfb.js`

---

## 1. Input model recap

`Element.HandleInput` gives on the main thread:

| Field | Type | Meaning for us |
|---|---|---|
| `Kind` | `InputKind` | `InputKeyDown/Up`, `InputPointerMove/Down/Up`, `InputScroll`, `InputText` |
| `Key` | `Key` | layout-dependent (`KeyUnknown` if platform doesn't report) |
| `PhysicalKey` | `Key` | ANSI-US-position equivalent |
| `Code` | `KeyCode` | **USB HID usage ID page 0x07, named after `KeyboardEvent.code`** — position-only, distinguishes L/R modifier sides and numpad |
| `Mods` | `Modifiers` | bitmask: `ModShift=1`, `ModCtrl=2`, `ModAlt=4`, `ModSuper=8` |
| `Repeat` | `bool` | auto-repeat keydown |
| `X, Y` | `float32` | **element-local** DIP coords |
| `Button` | `int` | `0`=primary(left), `1`=secondary(right), `2`=middle; `-1` on hover move |
| `DX, DY` | `float32` | scroll deltas; positive `DY` scrolls view down |
| `Text` | `string` | only on `InputText`/`InputCompose`/`InputCommand` — **never on keydown** |

`InputMethod(false)` disables IME → raw keysym path. `Text` arrives as separate
`InputText` events; we run **keysym mode** (see §4), so `InputText` is ignored
for VNC (or used only as a cut-text source).

---

## 2. `Code` → X11 keysym table (primary, position-based)

The `KeyCode` enum in `internal/platform/keycode.go` is a HID usage-ID page-0x07
value whose *names* match `KeyboardEvent.code`. That is exactly the same key
identity noVNC keys off, so the table below mirrors noVNC's `keysymdef.js` +
`domkeytable.js` semantics. Keysym values are standard X11 (`keysymdef.h`).

### 2.1 Printable keys — use the *position* (`Code`) for the unshifted keysym

For letter/digit/punct keys, `Code` gives the **unshifted** keysym; the shifted
variant is produced per §4's fake-shift rule, not baked into the table.

| `ev.Code` | keysym (unshifted) | hex |
|---|---|---|
| `CodeKeyA`–`CodeKeyZ` | `XK_a`–`XK_z` | `0x61`–`0x7a` |
| `CodeDigit1`–`CodeDigit0` | `XK_1`–`XK_0` | `0x31`–`0x30` |
| `CodeMinus` | `XK_minus` | `0x2d` |
| `CodeEqual` | `XK_equal` | `0x3d` |
| `CodeBracketLeft` | `XK_bracketleft` | `0x5b` |
| `CodeBracketRight` | `XK_bracketright` | `0x5d` |
| `CodeBackslash` | `XK_backslash` | `0x5c` |
| `CodeSemicolon` | `XK_semicolon` | `0x3b` |
| `CodeQuote` | `XK_apostrophe` | `0x27` |
| `CodeBackquote` | `XK_grave` | `0x60` |
| `CodeComma` | `XK_comma` | `0x2c` |
| `CodePeriod` | `XK_period` | `0x2e` |
| `CodeSlash` | `XK_slash` | `0x2f` |
| `CodeSpace` | `XK_space` | `0x20` |
| `CodeIntlBackslash` | `XK_backslash` | `0x5c` (ISO extra key) |

### 2.2 Action/navigation keys

| `ev.Code` | keysym | hex |
|---|---|---|
| `CodeEnter` | `XK_Return` | `0xff0d` |
| `CodeEscape` | `XK_Escape` | `0xff1b` |
| `CodeBackspace` | `XK_BackSpace` | `0xff08` |
| `CodeTab` | `XK_Tab` (+ shift → see §4 ISO_Left_Tab rule) | `0xff09` |
| `CodeDelete` | `XK_Delete` | `0xffff` |
| `CodeInsert` | `XK_Insert` | `0xff63` |
| `CodeHome` | `XK_Home` | `0xff50` |
| `CodeEnd` | `XK_End` | `0xff57` |
| `CodePageUp` | `XK_Page_Up` | `0xff55` |
| `CodePageDown` | `XK_Page_Down` | `0xff56` |
| `CodeArrowUp/Down/Left/Right` | `XK_Up/Down/Left/Right` | `0xff52/54/51/53` |
| `CodePrintScreen` | `XK_Print` | `0xff61` |
| `CodeScrollLock` | `XK_Scroll_Lock` | `0xff14` |
| `CodePause` | `XK_Pause` | `0xff13` |
| `CodeContextMenu` | `XK_Menu` | `0xff67` |
| `CodeCapsLock` | `XK_Caps_Lock` | `0xffe5` |
| `CodeNumLock` | `XK_Num_Lock` | `0xff7f` |
| `CodeF1`–`CodeF12` | `XK_F1`–`XK_F12` | `0xffbe`–`0xffc9` |
| `CodeF13`–`CodeF24` | `XK_F13`–`XK_F24` | `0xffca`–`0xffd5` |

### 2.3 Modifier sides — `Code` *distinguishes* left/right

| `ev.Code` | keysym | hex |
|---|---|---|
| `CodeShiftLeft` | `XK_Shift_L` | `0xffe1` |
| `CodeShiftRight` | `XK_Shift_R` | `0xffe2` |
| `CodeControlLeft` | `XK_Control_L` | `0xffe3` |
| `CodeControlRight` | `XK_Control_R` | `0xffe4` |
| `CodeAltLeft` | `XK_Alt_L` | `0xffe9` |
| `CodeAltRight` | `XK_Alt_R` | `0xffea` |
| `CodeSuperLeft` | `XK_Super_L` (macOS: see §4 swap) | `0xffeb` |
| `CodeSuperRight` | `XK_Super_R` | `0xffec` |

`Code` is the only field that reliably gives the side — `PhysicalKey`/`Key`
collapse `KeyShiftL/R` inconsistently across platforms. **Do not** derive side
from `Mods` alone.

### 2.4 Numpad — distinct keysyms, NumLock-aware

| `ev.Code` | keysym | hex |
|---|---|---|
| `CodeNumpad0`–`CodeNumpad9` | `XK_KP_0`–`XK_KP_9` | `0xffb0`–`0xffb9` |
| `CodeNumpadDecimal` | `XK_KP_Decimal` | `0xffae` |
| `CodeNumpadEnter` | `XK_KP_Enter` | `0xff8d` |
| `CodeNumpadAdd/Subtract/Multiply/Divide` | `XK_KP_Add/Subtract/Multiply/Divide` | `0xffab/0xffad/0xffaa/0xffaf` |
| `CodeNumpadEqual` | `XK_KP_Equal` | `0xffbd` |
| `CodeNumpadComma` | `XK_KP_Separator` | `0xffac` |
| `CodeNumLock` | `XK_Num_Lock` | `0xff7f` |

When NumLock is *off*, X servers translate `KP_n` into `KP_End/Down/…` nav
keysyms themselves — we still send `XK_KP_n`; the guest resolves it.
(noVNC relies on the same behaviour; its `NumLock` lock-sync only matters when
the server can't tell lock state — see §4 lock-sync note.)

### 2.5 Misc / media (send only if the table has them — else fall back to `Key`)

| `ev.Code` | keysym | hex |
|---|---|---|
| `CodeAudioVolumeMute` | `XF86XK_AudioMute` | `0x1008ff12` |
| `CodeAudioVolumeDown` | `XF86XK_AudioLowerVolume` | `0x1008ff11` |
| `CodeAudioVolumeUp` | `XF86XK_AudioRaiseVolume` | `0x1008ff13` |
| `CodePower` | `XF86XK_PowerOff` | `0x1008ff2a` |
| `CodeConvert` (Henkan) | `XK_Henkan` | `0xff23` |
| `CodeNonConvert` (Muhenkan) | `XK_Muhenkan` | `0xff22` |
| `CodeKanaMode` | `XK_Kana_Lock`/compose per platform | `0xff2d` |
| `CodeIntlRo` | `XK_backslash` + `XK_underscore` context — send `XK_backslash` | `0x5c` |
| `CodeIntlYen` | `XK_yen` | `0xa5` |
| `CodeLang1` | `XK_Hangul` | `0xff31` |
| `CodeLang2` | `XK_Hangul_Hanja` | `0xff34` |
| `CodeLang3` | `XK_Katakana` | `0xff27` |
| `CodeLang4` | `XK_Hiragana` | `0xff25` |

---

## 3. Fallback chain `Code` → `PhysicalKey` → `Key`, and `CodeUnknown` degradation

**Order (matches noVNC `getKeycode`→`getKeysym` precedence):**

1. **`ev.Code` known** → use §2 table. Authoritative — layout-independent.
2. **`ev.Code == CodeUnknown`, `ev.PhysicalKey` known** → map `PhysicalKey`
   through the *same* table by treating `KeyX` as `CodeKeyX`, etc. (`Key`
   enum shares names with `Code` for the printable+nav block.) Side info for
   modifiers may be lost (`PhysicalKey` collapses L/R) — acceptable, sends the
   left-side keysym.
3. **both unknown, `ev.Key` known** → map `Key` (layout-dependent). Still sends
   *a* keysym; may be wrong for the guest's layout, better than dropping.
4. **all unknown** → drop the event (log). No valid keysym to send.

### `CodeUnknown` degradation cases

| Case | Platform | `Code` | `PhysicalKey` | `Key` | Result |
|---|---|---|---|---|---|
| macOS **Fn** | `KeyCodeForMac` returns `CodeUnknown` (not in `macCodes`), `MacFlagsChanged` has no Fn branch | `CodeUnknown` | `KeyUnknown` | `KeyFn`/`KeyUnknown` | drop — correct (Fn is a hardware toggle, no guest meaning) |
| Windows **PrintScreen** | some paths report `CodePrintScreen` via XT 0x37-ext or 0x54-plain; older paths → `CodeUnknown` | `CodeUnknown` | `KeyPrintScreen` | `KeyPrintScreen` | fall to `PhysicalKey` → `XK_Print` |
| Mouse **side buttons** | `handler.go:82` — `CodeUnknown`, named by `Key` alone | `CodeUnknown` | `KeyBack`/`KeyForward` | same | map `Key` → `XF86XK_Back/Forward` `0x1008ff26/27`, or drop |
| Caps Lock (macOS) | `MacFlagsChanged` → `CodeCapsLock` | `CodeCapsLock` | — | — | `XK_Caps_Lock`; macOS already emits press+release per toggle |

---

## 4. RFC 6143 §7.5.4 interop rules — what the encoder must do

Encode these as send-time behaviours, not data:

1. **Fake shift for shifted symbols.** "The shift state should only be used as
   a hint." When the user types a *shifted* keysym (`#`, `!`, `A`…) **and** no
   Shift keysym is currently down (e.g. UK-layout `#`, or caps-locked `A` that
   maps to uppercase without shift), the *server* simulates shift — but our
   side must still send the **shifted keysym** (`XK_numbersign`), never the
   unshifted `XK_3`. Get the shifted keysym from `ev.Key` (layout `Key` for the
   actual char), not from `Code`.

2. **No keysyms for control combos.** "no keysyms for ASCII control characters
   such as Ctrl-A — send a Control press followed by an 'a' press." So
   `Ctrl`+`KeyA` = `XK_Control_L` down, `XK_a` down/up, `XK_Control_L` up —
   the encoder emits them as the raw events arrive; MyGo already gives
   `CodeControlLeft` keydown then `CodeKeyA` keydown.

3. **Ctrl+Alt+Del** = three independent keysyms in order: `XK_Control_L`,
   `XK_Alt_L`, `XK_Delete` down; releases in reverse. Expose as a toolbar
   button that injects the six events (guests rarely see the real chord because
   the host OS grabs it — same as noVNC's `sendCtrlAltDel`).

4. **Shifted-Tab over `ISO_Left_Tab`.** "In the RFB protocol, the latter
   approach is preferred": send `XK_Tab` with `XK_Shift_L` held, **not**
   `XK_ISO_Left_Tab` (`0xfe20`) and not a private `BackTab`. i.e. `Shift`+`CodeTab`
   → `Shift_L` + `Tab`; never emit `0xfe20`.

5. **Keysym mode, not text mode.** `InputMethod(false)` turns IME off; we then
   drive purely off `InputKeyDown/Up`+`Code`. `InputText` events (committed
   IME text, dead-key composition) are ignored for the remote — the guest's own
   input handling produces characters. This is how every real VNC client works
   and avoids double-typing.

6. **Modifier release-before-character (AltGr class).** Where Ctrl/Alt produce
   a *character* keysym (e.g. German `Ctrl-Alt-Q`→`@`), the spec requires the
   client to send simulated Ctrl/Alt *releases* so the char keysym reads clean.
   On MyGo this is the Windows `AltGr`→fake-`Ctrl`+`Alt` case: see §5 AltGr
   merge — the merge already yields a single `Alt_R`/`ISO_Level3_Shift`, so no
   extra release is needed *if* we merge; without merging we must emit the
   releases. Merge is the simpler path (and what noVNC does).

7. **Lock keys.** Send `XK_Caps_Lock`/`XK_Num_Lock` as normal key events. Do
   **not** try to track lock state client-side beyond what §5's LED-sync note
   needs — servers "should ignore lock keysyms where possible" and read case
   directly.

---

## 5. noVNC-derived behaviours worth copying

From `core/input/keyboard.js` + `core/rfb.js` — the proven reference:

- **Down-list keyed by `code`** (`_keyDownList[code] = keysym`). On `InputKeyUp`
  send the *recorded* keysym, not a re-derived one — guarantees down/up pairs
  match even if modifier interpretation changed mid-press. Replicates the
  MyGo `InputKeyUp`/`InputKeyDown` pairing requirement.
- **Key repeat**: if `code` already in down-list on a new `keydown` (`ev.Repeat`),
  reuse the recorded keysym and just re-send down (or skip — guest auto-repeats).
  noVNC re-sends; either is fine, keep parity and re-send.
- **`_allKeysUp()` on ungrab/blur** — when the element loses focus, emit release
  events for every `code` still in the down-list so the guest doesn't see stuck
  keys. MyGo equivalent: hook the element's `HandleInput` for the focus-loss
  signal (or `PointerPosition`/`Active` change) and flush.
- **Windows AltGr merge** (`_altGrArmed` + 50 ms `AltRight` window): on Windows,
  `Ctrl`+`Alt` arriving <50 ms apart with `AltRight` is really `AltGr` → send
  `XK_ISO_Level3_Shift` instead of `Ctrl_L`+`Alt_L`. Implement in the input
  layer, gated to Windows (`runtime.GOOS == "windows"`), same 50 ms window.
- **macOS modifier remap** (`browser.isMac()` block): noVNC swaps
  `Super_L→Alt_L`, `Super_R→Super_L`, `Alt_L→Mode_switch`, `Alt_R→ISO_Level3_Shift`
  because macOS `Alt` behaves like `AltGraph`. Apply the same swap when
  `runtime.GOOS == "darwin"` so a Mac user's physical `Cmd`/`Opt` land as the
  guest's `Alt`/`Super` the way RealVNC/TigerVNC users expect.
- **macOS Cmd-held release bug** (`e.metaKey && code!==Meta*`): macOS swallows
  key-up for keys pressed while `Cmd` is held — noVNC fakes press+release
  immediately. Apply: on darwin, `Mods&ModSuper != 0` for a non-modifier key →
  send down+up atomically.
- **macOS CapsLock** (`code==='CapsLock'`): macOS sends a toggle event, not
  press/release — fake `XK_Caps_Lock` down+up atomically.
- **Windows Japanese-IM fake release** (`jpBadKeys`): `Zenkaku/Eisu/Katakana/
  Hiragana/Romaji` on Windows get no key-up → fake down+up.
- **CapsLock/NumLock LED sync** (`_handleKeyEvent`): if the server reported LED
  state (QEMU LED-State pseudo-encoding -261) and our local `capslock/numlock`
  differs, inject a press+release first to resync. Optional — only when we wire
  the LED-state pseudo-encoding; note the *remote* state arrives in
  FBU pseudo-rects, so plumb it through the session. `ev.getModifierState`
  equivalent on MyGo is not exposed — gate this on having the pseudo-encoding,
  else skip.
- **PrintScreen/Pause normalisation**: spec says send `0x54` for Print and
  `0xc6` for Pause regardless of modifiers — handled by the §2 table (single
  `CodePrintScreen`/`CodePause` → fixed keycode), no extra work.
- **`Text`/`InputText`**: noVNC runs pure keysym mode (no text-input path) —
  same decision as §4.5; keep `InputMethod(false)`.

---

## 6. QEMU Extended Key Event — keycode table (QEMU key *numbers*, not XT)

**Spec-vs-impl discrepancy (the ticket's core trap):**

- `rfbproto.rst` *QEMU Extended Key Event Message* says the `keycode` field is
  "the XT keycode … encoded to fit in a single U32" (with the `0xe0`→high-bit
  folding, e.g. Right = `0xcd`).
- **QEMU actually implements it differently**: `vnc.c:ext_key_event` →
  `do_key_event(keycode)` → `qemu_input_key_number_to_qcode(keycode)`
  (`ui/input-keymap.c:46`) — i.e. the field is a **QEMU key number** which is
  the **Linux `input-event-codes.h` `KEY_*` number**, *not* XT.
- noVNC sends `XtScancode[code]` (XT) and relies on QEMU's tolerance; QEMU's
  `qnum_to_qcode` table happens to share most low numbers with XT set-1 make
  codes for the alpha block, but they diverge for extended keys (arrows,
  numpad, right modifiers). **Send real Linux key numbers** so QEMU guests get
  correct keys — matching QEMU's own interpretation, not the doc's.

### `Code` → QEMU key number (Linux `KEY_*` value)

Complete table for every `Code` MyGo can produce; `linux:` value from
`input-event-codes.h` (also embedded in noVNC `xtscancodes.js` comments):

| `ev.Code` | Linux KEY_ | qnum |
|---|---|---|
| `CodeEscape` | `KEY_ESC` | 1 |
| `CodeDigit1`–`CodeDigit9` | `KEY_1`–`KEY_9` | 2–10 |
| `CodeDigit0` | `KEY_0` | 11 |
| `CodeMinus`/`CodeEqual`/`CodeBackspace`/`CodeTab` | `KEY_MINUS/EQUAL/BACKSPACE/TAB` | 12/13/14/15 |
| `CodeKeyQ`–`CodeKeyP` | `KEY_Q`–`KEY_P` | 16–25 |
| `CodeBracketLeft`/`CodeBracketRight`/`CodeEnter`/`CodeControlLeft` | `KEY_LEFTBRACE/RIGHTBRACE/ENTER/LEFTCTRL` | 26/27/28/29 |
| `CodeKeyA`–`CodeKeyL` | `KEY_A`–`KEY_L` | 30–38 |
| `CodeSemicolon`/`CodeQuote`/`CodeBackquote` | `KEY_SEMICOLON/APOSTROPHE/GRAVE` | 39/40/41 |
| `CodeShiftLeft`/`CodeBackslash` | `KEY_LEFTSHIFT/BACKSLASH` | 42/43 |
| `CodeKeyZ`–`CodeKeyM` | `KEY_Z`–`KEY_M` | 44–50 |
| `CodeComma`/`CodePeriod`/`CodeSlash`/`CodeShiftRight` | `KEY_COMMA/DOT/SLASH/RIGHTSHIFT` | 51/52/53/54 |
| `CodeNumpadMultiply` | `KEY_KPASTERISK` | 55 |
| `CodeAltLeft` | `KEY_LEFTALT` | 56 |
| `CodeSpace` | `KEY_SPACE` | 57 |
| `CodeCapsLock` | `KEY_CAPSLOCK` | 58 |
| `CodeF1`–`CodeF10` | `KEY_F1`–`KEY_F10` | 59–68 |
| `CodeNumLock` | `KEY_NUMLOCK` | 69 |
| `CodeScrollLock` | `KEY_SCROLLLOCK` | 70 |
| `CodeNumpad7`–`CodeNumpad9` | `KEY_KP7`–`KEY_KP9` | 71/72/73 |
| `CodeNumpadSubtract` | `KEY_KPMINUS` | 74 |
| `CodeNumpad4`–`CodeNumpad6` | `KEY_KP4`–`KEY_KP6` | 75/76/77 |
| `CodeNumpadAdd` | `KEY_KPPLUS` | 78 |
| `CodeNumpad1`–`CodeNumpad3` | `KEY_KP1`–`KEY_KP3` | 79/80/81 |
| `CodeNumpad0`/`CodeNumpadDecimal` | `KEY_KP0/KPDOT` | 82/83 |
| `CodeIntlBackslash` | `KEY_102ND` | 86 |
| `CodeF11`/`CodeF12` | `KEY_F11/F12` | 87/88 |
| `CodeIntlRo` | `KEY_RO` | 89 |
| `CodeLang3`/`CodeLang4` | `KEY_KATAKANA/HIRAGANA` | 90/91 |
| `CodeConvert`/`CodeNonConvert` | `KEY_HENKAN/MUHENKAN` | 92/94 |
| `CodeNumpadComma` | `KEY_KPCOMMA` | 121 |
| `CodeNumpadEnter` | `KEY_KPENTER` | 96 |
| `CodeControlRight` | `KEY_RIGHTCTRL` | 97 |
| `CodeNumpadDivide` | `KEY_KPSLASH` | 98 |
| `CodePrintScreen` | `KEY_SYSRQ` | 99 |
| `CodeAltRight` | `KEY_RIGHTALT` | 100 |
| `CodeHome`/`CodeArrowUp`/`CodePageUp` | `KEY_HOME/UP/PAGEUP` | 102/103/104 |
| `CodeArrowLeft`/`CodeArrowRight`/`CodeEnd` | `KEY_LEFT/RIGHT/END` | 105/106/107 |
| `CodeArrowDown`/`CodePageDown`/`CodeInsert`/`CodeDelete` | `KEY_DOWN/PAGEDOWN/INSERT/DELETE` | 108/109/110/111 |
| `CodeAudioVolumeMute/Down/Up` | `KEY_MUTE/VOLUMEDOWN/VOLUMEUP` | 113/114/115 |
| `CodePower` | `KEY_POWER` | 116 |
| `CodeNumpadEqual` | `KEY_KPEQUAL` | 117 |
| `CodePause` | `KEY_PAUSE` | 119 |
| `CodeKanaMode` | `KEY_KATAKANAHIRAGANA` | 93 |
| `CodeLang1`/`CodeLang2` | `KEY_HANGEUL/HANJA` | 122/123 |
| `CodeIntlYen` | `KEY_YEN` | 124 |
| `CodeSuperLeft`/`CodeSuperRight` | `KEY_LEFTMETA/RIGHTMETA` | 125/126 |
| `CodeContextMenu` | `KEY_COMPOSE` | 127 |
| `CodeF13`–`CodeF24` | `KEY_F13`–`KEY_F24` | 183–194 |

`CodeUnknown` → omit the message entirely; per spec *"The client must not send
a QEMU Extended Key Event Message if the keycode isn't known. Instead a
standard KeyEvent should be used"* — fall back to the §2 keysym path.

Wire format (msg `255`, submsg `0`): `U8 255`, `U8 0`, `U16 down-flag`,
`U32 keysym` (send `0` when unknown — spec allows `NoSymbol`), `U32 keycode`
(the qnum above). Server opts in via pseudo-encoding `-258`.

---

## 7. Pointer & wheel → RFB PointerEvent

| MyGo event | RFB |
|---|---|
| `InputPointerMove` (`Button:-1` = hover, `input.go:479`) | `PointerEvent` mask `0` (no buttons), `x,y` = `ev.X`,`ev.Y` rounded to guest pixels — **send these; remote cursor must track hover** |
| `InputPointerDown` `Button=0/1/2` | set bit `1<<(Button)` → left=`0x01`, middle=`0x02`, right=`0x04` in mask; send with `x,y` |
| `InputPointerUp` | clear the bit for that `Button`, send new mask |
| `InputPointerMove` while `Button!=-1` (drag) | keep the held-button bits in mask while moving |
| `InputScroll` `DY>0` (view down) | **button 5** = mask bit `0x10` — send `PointerEvent` down **and** release (wheel = momentary) |
| `InputScroll` `DY<0` (view up) | **button 4** = mask bit `0x08`, down+release |
| `InputScroll` `DX≠0` | buttons 6/7 (left/right tilt) = `0x20/0x40`, down+release — optional |
| `InputZoom` / pinch | not in RFB pointer model — map to `Ctrl`+`4`/`5` only if the guest wants zoom; otherwise ignore |

`PointerEvent` = `U8 5`, `U8 button-mask` (bits 1–8 as above), `U16 x`, `U16 y`.

> **Correction (verified against the vendored MyGo):** an earlier revision of
> this table paired `DY>0` with button 4 and `DY<0` with button 5. MyGo
> documents positive `DY` as scrolling the view down, and X11/noVNC pair
> down-scrolling with wheel button 5, so the mapping above is the correct one:
> `+DY` → button 5 (`0x10`), `−DY` → button 4 (`0x08`). `ui/vnc` sends it this
> way; the wheel test asserts buttons 5 then 4 for `dy=+1` then `dy=-1`.

`x,y` are **guest framebuffer pixels** — convert `ev.X,Y` (element-local DIPs)
by the `Fit`/`Pixelated` scale factor the `ui.Image` renders at (§4.4 of the
feasibility doc), and clamp to `[0, w-1]`. Drag outside the element still
delivers move events; clamp there too.

Maintain the mask as local state: `downMask` bits per pressed button; each
pointer event sends current `downMask`. Hover → `downMask=0` but still send
position updates (throttle to ~30 fps to avoid flooding on `Precise` pads).

---

## 8. Deliverable checklist for `domain/model/rfb/keysym.go` + input layer

- [ ] `codeToKeysym` map — §2 tables 2.1–2.5 (position → unshifted keysym; modifiers; numpad; media/IME)
- [ ] `keyToKeysym` map — §3 fallback for `PhysicalKey`/`Key` (layout char → shifted keysym for §4.1)
- [ ] `codeToQNum` map — §6 Linux key numbers for QEMU path
- [ ] Down-list `map[KeyCode]uint32` + `_allKeysUp()` flush — §5
- [ ] Shifted-keysym resolution via `ev.Key` — §4.1
- [ ] Windows AltGr merge (50 ms `AltRight` window) — §5, `runtime.GOOS=="windows"`
- [ ] macOS swaps (`Super→Alt`, Cmd-held fake-release, CapsLock toggle) — §5, `runtime.GOOS=="darwin"`
- [ ] Windows JP-IM fake release — §5
- [ ] shifted-Tab → `XK_Tab`+shift, never `0xfe20` — §4.4
- [ ] Pointer mask state + wheel 4/5 down+release — §7
- [ ] `CodeUnknown` → skip QEMU msg, fallback keysym `KeyEvent` — §6/§3
- [ ] `InputMethod(false)` on the element — §4.5
