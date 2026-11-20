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
