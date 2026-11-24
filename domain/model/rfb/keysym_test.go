package rfb

import (
	"reflect"
	"testing"
)

func TestKeysymTableCoversMappingResearch(t *testing.T) {
	want := map[KeyCode]uint32{
		CodeMinus:           0x2d,
		CodeEqual:           0x3d,
		CodeBracketLeft:     0x5b,
		CodeBracketRight:    0x5d,
		CodeBackslash:       0x5c,
		CodeSemicolon:       0x3b,
		CodeQuote:           0x27,
		CodeBackquote:       0x60,
		CodeComma:           0x2c,
		CodePeriod:          0x2e,
		CodeSlash:           0x2f,
		CodeSpace:           0x20,
		CodeIntlBackslash:   0x5c,
		CodeEnter:           0xff0d,
		CodeEscape:          0xff1b,
		CodeBackspace:       0xff08,
		CodeTab:             0xff09,
		CodeDelete:          0xffff,
		CodeInsert:          0xff63,
		CodeHome:            0xff50,
		CodeEnd:             0xff57,
		CodePageUp:          0xff55,
		CodePageDown:        0xff56,
		CodeArrowUp:         0xff52,
		CodeArrowDown:       0xff54,
		CodeArrowLeft:       0xff51,
		CodeArrowRight:      0xff53,
		CodePrintScreen:     0xff61,
		CodeScrollLock:      0xff14,
		CodePause:           0xff13,
		CodeContextMenu:     0xff67,
		CodeCapsLock:        0xffe5,
		CodeNumLock:         0xff7f,
		CodeShiftLeft:       0xffe1,
		CodeShiftRight:      0xffe2,
		CodeControlLeft:     0xffe3,
		CodeControlRight:    0xffe4,
		CodeAltLeft:         0xffe9,
		CodeAltRight:        0xffea,
		CodeSuperLeft:       0xffeb,
		CodeSuperRight:      0xffec,
		CodeNumpadDecimal:   0xffae,
		CodeNumpadEnter:     0xff8d,
		CodeNumpadAdd:       0xffab,
		CodeNumpadSubtract:  0xffad,
		CodeNumpadMultiply:  0xffaa,
		CodeNumpadDivide:    0xffaf,
		CodeNumpadEqual:     0xffbd,
		CodeNumpadComma:     0xffac,
		CodeAudioVolumeMute: 0x1008ff12,
		CodeAudioVolumeDown: 0x1008ff11,
		CodeAudioVolumeUp:   0x1008ff13,
		CodePower:           0x1008ff2a,
		CodeConvert:         0xff23,
		CodeNonConvert:      0xff22,
		CodeKanaMode:        0xff2d,
		CodeIntlRo:          0x5c,
		CodeIntlYen:         0xa5,
		CodeLang1:           0xff31,
		CodeLang2:           0xff34,
		CodeLang3:           0xff27,
		CodeLang4:           0xff25,
		CodeBack:            0x1008ff26,
		CodeForward:         0x1008ff27,
	}
	for i := 0; i < 26; i++ {
		want[CodeKeyA+KeyCode(i)] = 0x61 + uint32(i)
	}
	for i := 0; i < 9; i++ {
		want[CodeDigit1+KeyCode(i)] = 0x31 + uint32(i)
	}
	want[CodeDigit0] = 0x30
	for i := 0; i < 24; i++ {
		want[CodeF1+KeyCode(i)] = 0xffbe + uint32(i)
	}
	for i := 0; i < 10; i++ {
		want[CodeNumpad0+KeyCode(i)] = 0xffb0 + uint32(i)
	}
	if !reflect.DeepEqual(codeKeysyms, want) {
		for code := CodeUnknown; code <= CodeForward; code++ {
			if codeKeysyms[code] != want[code] {
				t.Errorf("keysym[%d] = %#x, want %#x", code, codeKeysyms[code], want[code])
			}
		}
		t.Fatal("keysym table differs from the mapping research")
	}
	for code := CodeUnknown + 1; code <= CodeForward; code++ {
		if _, ok := KeysymForCode(code); !ok {
			t.Fatalf("no keysym for key code %d", code)
		}
	}
}

func TestResolveKeyUSLayout(t *testing.T) {
	tests := []struct {
		name   string
		stroke KeyStroke
		keysym uint32
		qnum   uint32
	}{
		{"letter unshifted", KeyStroke{Code: CodeKeyA}, 0x61, 30},
		{"letter shifted", KeyStroke{Code: CodeKeyA, Shift: true}, 0x41, 30},
		{"digit shifted", KeyStroke{Code: CodeDigit2, Shift: true}, 0x40, 3},
		{"digit zero shifted", KeyStroke{Code: CodeDigit0, Shift: true}, 0x29, 11},
		{"minus shifted", KeyStroke{Code: CodeMinus, Shift: true}, 0x5f, 12},
		{"equal shifted", KeyStroke{Code: CodeEqual, Shift: true}, 0x2b, 13},
		{"bracket left shifted", KeyStroke{Code: CodeBracketLeft, Shift: true}, 0x7b, 26},
		{"bracket right shifted", KeyStroke{Code: CodeBracketRight, Shift: true}, 0x7d, 27},
		{"backslash shifted", KeyStroke{Code: CodeBackslash, Shift: true}, 0x7c, 43},
		{"semicolon shifted", KeyStroke{Code: CodeSemicolon, Shift: true}, 0x3a, 39},
		{"quote shifted", KeyStroke{Code: CodeQuote, Shift: true}, 0x22, 40},
		{"backquote shifted", KeyStroke{Code: CodeBackquote, Shift: true}, 0x7e, 41},
		{"comma shifted", KeyStroke{Code: CodeComma, Shift: true}, 0x3c, 51},
		{"period shifted", KeyStroke{Code: CodePeriod, Shift: true}, 0x3e, 52},
		{"slash shifted", KeyStroke{Code: CodeSlash, Shift: true}, 0x3f, 53},
		{"space unaffected by shift", KeyStroke{Code: CodeSpace, Shift: true}, 0x20, 57},
		{"tab unaffected by shift", KeyStroke{Code: CodeTab, Shift: true}, 0xff09, 15},
		{"numpad unaffected by shift", KeyStroke{Code: CodeNumpad2, Shift: true}, 0xffb2, 80},
		{"shift is not ISO_Left_Tab", KeyStroke{Code: CodeTab, Shift: true}, 0xff09, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveKey(tt.stroke)
			if !ok {
				t.Fatalf("ResolveKey(%+v) dropped the key", tt.stroke)
			}
			if got.Keysym != tt.keysym || got.QEMUKeyNumber != tt.qnum {
				t.Fatalf("ResolveKey(%+v) = %#x/q%d, want %#x/q%d",
					tt.stroke, got.Keysym, got.QEMUKeyNumber, tt.keysym, tt.qnum)
			}
		})
	}
}

func TestResolveKeyISOLayout(t *testing.T) {
	// The ISO extra key beside the left Shift has no US position of its own;
	// the mapping research sends it as backslash, and bar with Shift.
	tests := []struct {
		name   string
		stroke KeyStroke
		keysym uint32
	}{
		{"iso extra key", KeyStroke{Physical: CodeIntlBackslash}, 0x5c},
		{"iso extra key shifted", KeyStroke{Physical: CodeIntlBackslash, Shift: true}, 0x7c},
		{"iso section types # but sends the physical position", KeyStroke{Physical: CodeBackslash}, 0x5c},
		{"iso section shifted", KeyStroke{Physical: CodeBackslash, Shift: true}, 0x7c},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveKey(tt.stroke)
			if !ok {
				t.Fatalf("ResolveKey(%+v) dropped the key", tt.stroke)
			}
			if got.Keysym != tt.keysym {
				t.Fatalf("ResolveKey(%+v) = %#x, want %#x", tt.stroke, got.Keysym, tt.keysym)
			}
		})
	}
}

func TestResolveKeyFallbackChain(t *testing.T) {
	// Windows PrintScreen arrives without a physical code but with the
	// PrintScreen physical identity; the resolver still maps it to XK_Print.
	got, ok := ResolveKey(KeyStroke{Physical: CodePrintScreen})
	if !ok || got.Keysym != 0xff61 {
		t.Fatalf("PrintScreen fallback = %+v, %v; want XK_Print", got, ok)
	}
	if got.QEMUKeyNumber != 99 {
		t.Fatalf("PrintScreen QEMU key number = %d, want 99", got.QEMUKeyNumber)
	}

	// A dead key reports no logical key but a physical position: send the
	// position's keysym rather than guessing a dead keysym.
	got, ok = ResolveKey(KeyStroke{Physical: CodeQuote, Shift: true})
	if !ok || got.Keysym != 0x22 {
		t.Fatalf("dead key = %+v, %v; want quotedbl from the physical position", got, ok)
	}

	// A logical-only key still sends a keysym but must not claim a physical
	// QEMU key number, because the layout may not match the US position.
	got, ok = ResolveKey(KeyStroke{Logical: CodeKeyA, Shift: true})
	if !ok || got.Keysym != 0x41 {
		t.Fatalf("logical fallback = %+v, %v; want XK_A", got, ok)
	}
	if got.QEMUKeyNumber != 0 {
		t.Fatalf("logical fallback QEMU key number = %d, want none", got.QEMUKeyNumber)
	}

	// A preferred code wins over the other tiers.
	got, ok = ResolveKey(KeyStroke{Code: CodeKeyB, Physical: CodeKeyA, Logical: CodeKeyC})
	if !ok || got.Keysym != 0x62 || got.Code != CodeKeyB {
		t.Fatalf("tier precedence = %+v, %v; want CodeKeyB", got, ok)
	}

	// A US layout reports the same key at the physical and logical positions.
	// The physical tier must still carry its QEMU key number; the tier is not
	// the code value, so equality must not misclassify it as logical.
	got, ok = ResolveKey(KeyStroke{Physical: CodeKeyA, Logical: CodeKeyA})
	if !ok || got.QEMUKeyNumber != 30 {
		t.Fatalf("physical==logical = %+v, %v; want QEMU key number 30", got, ok)
	}
}

func TestResolveKeyDropsUnmappedKey(t *testing.T) {
	// macOS Fn has no keysym and no physical identity; it is a hardware
	// toggle with no guest meaning, so the input layer drops it.
	if got, ok := ResolveKey(KeyStroke{}); ok {
		t.Fatalf("ResolveKey(drop) = %+v, want the event dropped", got)
	}
	if _, ok := ResolveKey(KeyStroke{Code: CodeUnknown, Physical: CodeUnknown, Logical: CodeUnknown}); ok {
		t.Fatal("an all-unknown stroke was not dropped")
	}
}

func TestQEMUKeyNumberTableCoversMappingResearch(t *testing.T) {
	want := map[KeyCode]uint32{
		CodeEscape:          1,
		CodeMinus:           12,
		CodeEqual:           13,
		CodeBackspace:       14,
		CodeTab:             15,
		CodeBracketLeft:     26,
		CodeBracketRight:    27,
		CodeEnter:           28,
		CodeControlLeft:     29,
		CodeSemicolon:       39,
		CodeQuote:           40,
		CodeBackquote:       41,
		CodeShiftLeft:       42,
		CodeBackslash:       43,
		CodeComma:           51,
		CodePeriod:          52,
		CodeSlash:           53,
		CodeShiftRight:      54,
		CodeNumpadMultiply:  55,
		CodeAltLeft:         56,
		CodeSpace:           57,
		CodeCapsLock:        58,
		CodeNumLock:         69,
		CodeScrollLock:      70,
		CodeNumpad7:         71,
		CodeNumpad8:         72,
		CodeNumpad9:         73,
		CodeNumpadSubtract:  74,
		CodeNumpad4:         75,
		CodeNumpad5:         76,
		CodeNumpad6:         77,
		CodeNumpadAdd:       78,
		CodeNumpad1:         79,
		CodeNumpad2:         80,
		CodeNumpad3:         81,
		CodeNumpad0:         82,
		CodeNumpadDecimal:   83,
		CodeIntlBackslash:   86,
		CodeF11:             87,
		CodeF12:             88,
		CodeIntlRo:          89,
		CodeLang3:           90,
		CodeLang4:           91,
		CodeConvert:         92,
		CodeKanaMode:        93,
		CodeNonConvert:      94,
		CodeNumpadEnter:     96,
		CodeControlRight:    97,
		CodeNumpadDivide:    98,
		CodePrintScreen:     99,
		CodeAltRight:        100,
		CodeHome:            102,
		CodeArrowUp:         103,
		CodePageUp:          104,
		CodeArrowLeft:       105,
		CodeArrowRight:      106,
		CodeEnd:             107,
		CodeArrowDown:       108,
		CodePageDown:        109,
		CodeInsert:          110,
		CodeDelete:          111,
		CodeAudioVolumeMute: 113,
		CodeAudioVolumeDown: 114,
		CodeAudioVolumeUp:   115,
		CodePower:           116,
		CodeNumpadEqual:     117,
		CodePause:           119,
		CodeNumpadComma:     121,
		CodeLang1:           122,
		CodeLang2:           123,
		CodeIntlYen:         124,
		CodeSuperLeft:       125,
		CodeSuperRight:      126,
		CodeContextMenu:     127,
	}
	for i := 0; i < 9; i++ {
		want[CodeDigit1+KeyCode(i)] = 2 + uint32(i)
	}
	want[CodeDigit0] = 11
	for i, code := range []KeyCode{
		CodeKeyQ, CodeKeyW, CodeKeyE, CodeKeyR, CodeKeyT,
		CodeKeyY, CodeKeyU, CodeKeyI, CodeKeyO, CodeKeyP,
	} {
		want[code] = 16 + uint32(i)
	}
	for i, code := range []KeyCode{
		CodeKeyA, CodeKeyS, CodeKeyD, CodeKeyF, CodeKeyG,
		CodeKeyH, CodeKeyJ, CodeKeyK, CodeKeyL,
	} {
		want[code] = 30 + uint32(i)
	}
	for i, code := range []KeyCode{
		CodeKeyZ, CodeKeyX, CodeKeyC, CodeKeyV, CodeKeyB,
		CodeKeyN, CodeKeyM,
	} {
		want[code] = 44 + uint32(i)
	}
	for i := 0; i < 10; i++ {
		want[CodeF1+KeyCode(i)] = 59 + uint32(i)
	}
	for i := 0; i < 12; i++ {
		want[CodeF13+KeyCode(i)] = 183 + uint32(i)
	}
	if !reflect.DeepEqual(codeQNumbers, want) {
		for code := CodeUnknown; code <= CodeForward; code++ {
			if codeQNumbers[code] != want[code] {
				t.Errorf("qnum[%d] = %d, want %d", code, codeQNumbers[code], want[code])
			}
		}
		t.Fatal("QEMU key-number table differs from the mapping research")
	}
	if _, ok := QEMUKeyNumberForCode(CodeBack); ok {
		t.Fatal("CodeBack unexpectedly has a QEMU key number")
	}
	if _, ok := QEMUKeyNumberForCode(CodeForward); ok {
		t.Fatal("CodeForward unexpectedly has a QEMU key number")
	}
	for code, number := range want {
		got, ok := QEMUKeyNumberForCode(code)
		if !ok || got != number {
			t.Fatalf("QEMUKeyNumberForCode(%d) = %d, %v; want %d", code, got, ok, number)
		}
	}
}
