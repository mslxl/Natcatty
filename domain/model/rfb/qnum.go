package rfb

// QEMU extended key events carry a QEMU key number, which QEMU interprets as
// a Linux input-event-codes.h KEY_* value, not as an XT scancode. The wire
// doc says "XT keycode", but qemu_input_key_number_to_qcode reads this table,
// so the client sends the KEY_* numbers below.

var codeQNumbers = buildCodeQNumbers()

func buildCodeQNumbers() map[KeyCode]uint32 {
	codes := map[KeyCode]uint32{
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
		codes[CodeDigit1+KeyCode(i)] = 2 + uint32(i)
	}
	codes[CodeDigit0] = 11
	for i, code := range []KeyCode{
		CodeKeyQ, CodeKeyW, CodeKeyE, CodeKeyR, CodeKeyT,
		CodeKeyY, CodeKeyU, CodeKeyI, CodeKeyO, CodeKeyP,
	} {
		codes[code] = 16 + uint32(i)
	}
	for i, code := range []KeyCode{
		CodeKeyA, CodeKeyS, CodeKeyD, CodeKeyF, CodeKeyG,
		CodeKeyH, CodeKeyJ, CodeKeyK, CodeKeyL,
	} {
		codes[code] = 30 + uint32(i)
	}
	for i, code := range []KeyCode{
		CodeKeyZ, CodeKeyX, CodeKeyC, CodeKeyV, CodeKeyB,
		CodeKeyN, CodeKeyM,
	} {
		codes[code] = 44 + uint32(i)
	}
	for i := 0; i < 10; i++ {
		codes[CodeF1+KeyCode(i)] = 59 + uint32(i)
	}
	for i := 0; i < 12; i++ {
		codes[CodeF13+KeyCode(i)] = 183 + uint32(i)
	}
	return codes
}

// QEMUKeyNumberForCode returns the QEMU key number for a physical key. Keys
// outside the QEMU table report false; a caller must then send a plain
// KeyEvent instead of a QEMU Extended Key Event.
func QEMUKeyNumberForCode(code KeyCode) (uint32, bool) {
	number, ok := codeQNumbers[code]
	return number, ok
}
