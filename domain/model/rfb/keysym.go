package rfb

// KeyCode identifies a physical key by its USB HID usage ID page 0x07, named
// after the DOM KeyboardEvent.code value. Position, not layout, is what the
// keysym and QEMU tables key on: CodeKeyA is the same physical key on every
// layout, and modifier sides and numpad keys stay distinct.
//
// The input layer maps MyGo's key codes onto these values; keeping the enum
// here lets the protocol tables stay free of UI imports.
type KeyCode uint16

// Physical key codes covered by the mapping research.
const (
	CodeUnknown KeyCode = iota

	CodeKeyA
	CodeKeyB
	CodeKeyC
	CodeKeyD
	CodeKeyE
	CodeKeyF
	CodeKeyG
	CodeKeyH
	CodeKeyI
	CodeKeyJ
	CodeKeyK
	CodeKeyL
	CodeKeyM
	CodeKeyN
	CodeKeyO
	CodeKeyP
	CodeKeyQ
	CodeKeyR
	CodeKeyS
	CodeKeyT
	CodeKeyU
	CodeKeyV
	CodeKeyW
	CodeKeyX
	CodeKeyY
	CodeKeyZ

	CodeDigit1
	CodeDigit2
	CodeDigit3
	CodeDigit4
	CodeDigit5
	CodeDigit6
	CodeDigit7
	CodeDigit8
	CodeDigit9
	CodeDigit0

	CodeMinus
	CodeEqual
	CodeBracketLeft
	CodeBracketRight
	CodeBackslash
	CodeSemicolon
	CodeQuote
	CodeBackquote
	CodeComma
	CodePeriod
	CodeSlash
	CodeSpace
	CodeIntlBackslash

	CodeEnter
	CodeEscape
	CodeBackspace
	CodeTab
	CodeDelete
	CodeInsert
	CodeHome
	CodeEnd
	CodePageUp
	CodePageDown
	CodeArrowUp
	CodeArrowDown
	CodeArrowLeft
	CodeArrowRight
	CodePrintScreen
	CodeScrollLock
	CodePause
	CodeContextMenu
	CodeCapsLock
	CodeNumLock

	CodeF1
	CodeF2
	CodeF3
	CodeF4
	CodeF5
	CodeF6
	CodeF7
	CodeF8
	CodeF9
	CodeF10
	CodeF11
	CodeF12
	CodeF13
	CodeF14
	CodeF15
	CodeF16
	CodeF17
	CodeF18
	CodeF19
	CodeF20
	CodeF21
	CodeF22
	CodeF23
	CodeF24

	CodeShiftLeft
	CodeShiftRight
	CodeControlLeft
	CodeControlRight
	CodeAltLeft
	CodeAltRight
	CodeSuperLeft
	CodeSuperRight

	CodeNumpad0
	CodeNumpad1
	CodeNumpad2
	CodeNumpad3
	CodeNumpad4
	CodeNumpad5
	CodeNumpad6
	CodeNumpad7
	CodeNumpad8
	CodeNumpad9
	CodeNumpadDecimal
	CodeNumpadEnter
	CodeNumpadAdd
	CodeNumpadSubtract
	CodeNumpadMultiply
	CodeNumpadDivide
	CodeNumpadEqual
	CodeNumpadComma

	CodeAudioVolumeMute
	CodeAudioVolumeDown
	CodeAudioVolumeUp
	CodePower
	CodeConvert
	CodeNonConvert
	CodeKanaMode
	CodeIntlRo
	CodeIntlYen
	CodeLang1
	CodeLang2
	CodeLang3
	CodeLang4
	CodeBack
	CodeForward
)

// X11 keysym values used by the tables. They are standard keysymdef.h values.
const (
	keysymBackSpace       uint32 = 0xff08
	keysymTab             uint32 = 0xff09
	keysymReturn          uint32 = 0xff0d
	keysymEscape          uint32 = 0xff1b
	keysymDelete          uint32 = 0xffff
	keysymInsert          uint32 = 0xff63
	keysymHome            uint32 = 0xff50
	keysymEnd             uint32 = 0xff57
	keysymPageUp          uint32 = 0xff55
	keysymPageDown        uint32 = 0xff56
	keysymLeft            uint32 = 0xff51
	keysymUp              uint32 = 0xff52
	keysymRight           uint32 = 0xff53
	keysymDown            uint32 = 0xff54
	keysymPrint           uint32 = 0xff61
	keysymScrollLock      uint32 = 0xff14
	keysymPause           uint32 = 0xff13
	keysymMenu            uint32 = 0xff67
	keysymCapsLock        uint32 = 0xffe5
	keysymNumLock         uint32 = 0xff7f
	keysymShiftL          uint32 = 0xffe1
	keysymShiftR          uint32 = 0xffe2
	keysymControlL        uint32 = 0xffe3
	keysymControlR        uint32 = 0xffe4
	keysymAltL            uint32 = 0xffe9
	keysymAltR            uint32 = 0xffea
	keysymSuperL          uint32 = 0xffeb
	keysymSuperR          uint32 = 0xffec
	keysymKPEnter         uint32 = 0xff8d
	keysymKPEqual         uint32 = 0xffbd
	keysymKPMultiply      uint32 = 0xffaa
	keysymKPAdd           uint32 = 0xffab
	keysymKPSeparator     uint32 = 0xffac
	keysymKPSubtract      uint32 = 0xffad
	keysymKPDecimal       uint32 = 0xffae
	keysymKPDivide        uint32 = 0xffaf
	keysymHenkan          uint32 = 0xff23
	keysymMuhenkan        uint32 = 0xff22
	keysymKanaLock        uint32 = 0xff2d
	keysymHangul          uint32 = 0xff31
	keysymHangulHanja     uint32 = 0xff34
	keysymKatakana        uint32 = 0xff27
	keysymHiragana        uint32 = 0xff25
	keysymYen             uint32 = 0xa5
	keysymXF86Mute        uint32 = 0x1008ff12
	keysymXF86LowerVolume uint32 = 0x1008ff11
	keysymXF86RaiseVolume uint32 = 0x1008ff13
	keysymXF86PowerOff    uint32 = 0x1008ff2a
	keysymXF86Back        uint32 = 0x1008ff26
	keysymXF86Forward     uint32 = 0x1008ff27
	// keysymISOLevel3Shift is AltGr. On macOS the viewer synthesizes it for
	// Option, matching noVNC's Alt→ISO_Level3_Shift mapping.
	keysymISOLevel3Shift uint32 = 0xfe03
)

// Exported keysyms the viewer needs for synthesized modifier presses and the
// Ctrl+Alt+Del chord. They are the standard keysymdef.h values.
const (
	KeysymShiftL         = keysymShiftL
	KeysymControlL       = keysymControlL
	KeysymAltL           = keysymAltL
	KeysymSuperL         = keysymSuperL
	KeysymTab            = keysymTab
	KeysymDelete         = keysymDelete
	KeysymISOLevel3Shift = keysymISOLevel3Shift
)

var codeKeysyms = buildCodeKeysyms()

func buildCodeKeysyms() map[KeyCode]uint32 {
	codes := map[KeyCode]uint32{
		CodeMinus:         0x2d,
		CodeEqual:         0x3d,
		CodeBracketLeft:   0x5b,
		CodeBracketRight:  0x5d,
		CodeBackslash:     0x5c,
		CodeSemicolon:     0x3b,
		CodeQuote:         0x27,
		CodeBackquote:     0x60,
		CodeComma:         0x2c,
		CodePeriod:        0x2e,
		CodeSlash:         0x2f,
		CodeSpace:         0x20,
		CodeIntlBackslash: 0x5c,
		CodeIntlRo:        0x5c,
		CodeIntlYen:       keysymYen,

		CodeEnter:       keysymReturn,
		CodeEscape:      keysymEscape,
		CodeBackspace:   keysymBackSpace,
		CodeTab:         keysymTab,
		CodeDelete:      keysymDelete,
		CodeInsert:      keysymInsert,
		CodeHome:        keysymHome,
		CodeEnd:         keysymEnd,
		CodePageUp:      keysymPageUp,
		CodePageDown:    keysymPageDown,
		CodeArrowUp:     keysymUp,
		CodeArrowDown:   keysymDown,
		CodeArrowLeft:   keysymLeft,
		CodeArrowRight:  keysymRight,
		CodePrintScreen: keysymPrint,
		CodeScrollLock:  keysymScrollLock,
		CodePause:       keysymPause,
		CodeContextMenu: keysymMenu,
		CodeCapsLock:    keysymCapsLock,
		CodeNumLock:     keysymNumLock,

		CodeShiftLeft:    keysymShiftL,
		CodeShiftRight:   keysymShiftR,
		CodeControlLeft:  keysymControlL,
		CodeControlRight: keysymControlR,
		CodeAltLeft:      keysymAltL,
		CodeAltRight:     keysymAltR,
		CodeSuperLeft:    keysymSuperL,
		CodeSuperRight:   keysymSuperR,

		CodeNumpadDecimal:  keysymKPDecimal,
		CodeNumpadEnter:    keysymKPEnter,
		CodeNumpadAdd:      keysymKPAdd,
		CodeNumpadSubtract: keysymKPSubtract,
		CodeNumpadMultiply: keysymKPMultiply,
		CodeNumpadDivide:   keysymKPDivide,
		CodeNumpadEqual:    keysymKPEqual,
		CodeNumpadComma:    keysymKPSeparator,

		CodeAudioVolumeMute: keysymXF86Mute,
		CodeAudioVolumeDown: keysymXF86LowerVolume,
		CodeAudioVolumeUp:   keysymXF86RaiseVolume,
		CodePower:           keysymXF86PowerOff,
		CodeConvert:         keysymHenkan,
		CodeNonConvert:      keysymMuhenkan,
		CodeKanaMode:        keysymKanaLock,
		CodeLang1:           keysymHangul,
		CodeLang2:           keysymHangulHanja,
		CodeLang3:           keysymKatakana,
		CodeLang4:           keysymHiragana,
		CodeBack:            keysymXF86Back,
		CodeForward:         keysymXF86Forward,
	}
	for i := 0; i < 26; i++ {
		codes[CodeKeyA+KeyCode(i)] = 0x61 + uint32(i)
	}
	for i := 0; i < 9; i++ {
		codes[CodeDigit1+KeyCode(i)] = 0x31 + uint32(i)
	}
	codes[CodeDigit0] = 0x30
	for i := 0; i < 24; i++ {
		codes[CodeF1+KeyCode(i)] = 0xffbe + uint32(i)
	}
	for i := 0; i < 10; i++ {
		codes[CodeNumpad0+KeyCode(i)] = 0xffb0 + uint32(i)
	}
	return codes
}

// KeysymForCode returns the unshifted X11 keysym for a physical key. Shifted
// symbols are resolved at send time from the layout's typed character, not
// baked into this table.
func KeysymForCode(code KeyCode) (uint32, bool) {
	keysym, ok := codeKeysyms[code]
	return keysym, ok
}

// KeyStroke is one key press as an input layer sees it. The tiers mirror the
// mapping research's fallback order:
//
//  1. Code is the platform's physical key code (USB HID usage page 0x07).
//  2. Physical is the key's ANSI-US position when Code is not reported.
//  3. Logical is the layout's key, the last resort.
//
// A tier that is unavailable is CodeUnknown. Shift reports whether Shift is
// held; the resolver sends the shifted keysym the protocol expects instead of
// relying on the server to apply the shift state.
type KeyStroke struct {
	Code     KeyCode
	Physical KeyCode
	Logical  KeyCode
	Shift    bool
}

// ResolvedKey is the wire identity of one key press. Keysym is the X11 keysym
// to send. Code is the code value of whichever tier matched, so the input layer
// can pair a key-up with the key-down that preceded it. QEMUKeyNumber is the
// QEMU (Linux KEY_*) number when a real physical position is known and 0
// otherwise; a zero number must use a plain KeyEvent, never a QEMU Extended
// Key Event.
type ResolvedKey struct {
	Keysym        uint32
	Code          KeyCode
	QEMUKeyNumber uint32
}

// keyTier records which input identity resolved a key. It is explicit rather
// than inferred from the code value: a US layout reports the same key as both
// Physical and Logical, and comparing values would misclassify the physical
// tier as logical and drop its QEMU key number.
type keyTier uint8

const (
	tierNone keyTier = iota
	tierCode
	tierPhysical
	tierLogical
)

// ResolveKey maps one key press through the physical-code, physical-position
// and logical-key tiers. It reports false when no tier has a keysym, as for
// macOS Fn, rather than guessing: the input layer then drops the event.
func ResolveKey(stroke KeyStroke) (ResolvedKey, bool) {
	code, tier := stroke.Code, tierCode
	if _, ok := KeysymForCode(code); !ok {
		code, tier = stroke.Physical, tierPhysical
	}
	if _, ok := KeysymForCode(code); !ok {
		code, tier = stroke.Logical, tierLogical
	}
	keysym, ok := KeysymForCode(code)
	if !ok {
		return ResolvedKey{}, false
	}
	if stroke.Shift {
		if shifted, ok := shiftedCodeKeysyms[code]; ok {
			keysym = shifted
		}
	}
	resolved := ResolvedKey{Keysym: keysym, Code: code}
	// A logical key is layout-dependent: its ANSI-US position may not be the
	// key the user pressed, so it must not be sent as a physical QEMU code.
	if tier == tierCode || tier == tierPhysical {
		resolved.QEMUKeyNumber, _ = QEMUKeyNumberForCode(code)
	}
	return resolved, true
}

// shiftedCodeKeysyms is the US-layout shifted symbol for each printable
// position, which RFC 6143 §7.5.4 requires the client to send instead of the
// unshifted keysym. Non-printing keys (Tab, arrows, numpad) deliberately have
// no entry, so Shift+Tab stays XK_Tab rather than becoming ISO_Left_Tab.
var shiftedCodeKeysyms = func() map[KeyCode]uint32 {
	shifted := map[KeyCode]uint32{
		CodeMinus:         0x5f, // underscore
		CodeEqual:         0x2b, // plus
		CodeBracketLeft:   0x7b, // braceleft
		CodeBracketRight:  0x7d, // braceright
		CodeBackslash:     0x7c, // bar
		CodeSemicolon:     0x3a, // colon
		CodeQuote:         0x22, // quotedbl
		CodeBackquote:     0x7e, // asciitilde
		CodeComma:         0x3c, // less
		CodePeriod:        0x3e, // greater
		CodeSlash:         0x3f, // question
		CodeIntlBackslash: 0x7c, // bar, the ISO extra key beside left Shift
	}
	for i := 0; i < 26; i++ {
		shifted[CodeKeyA+KeyCode(i)] = 0x41 + uint32(i)
	}
	for i := 0; i < 9; i++ {
		shifted[CodeDigit1+KeyCode(i)] = []uint32{
			0x21, 0x40, 0x23, 0x24, 0x25, 0x5e, 0x26, 0x2a, 0x28,
		}[i]
	}
	shifted[CodeDigit0] = 0x29 // parenright
	return shifted
}()
