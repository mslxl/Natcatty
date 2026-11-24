package vnc

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// rfbKeyCode maps a MyGo key identity onto the protocol's position-based key
// code. MyGo names the letter and digit keys by what they type on the active
// layout, while PhysicalKey names the ANSI-US position; both are used as
// position tiers by the resolver, the logical one only when the physical one
// is unknown.
//
// MyGo v0.7.4 has no KeyPrintScreen, KeyFn, modifier-side, numpad or media
// identities, so those protocol codes are reachable only by a future platform
// report; the resolver already understands them.
func rfbKeyCode(key ui.Key) rfb.KeyCode {
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ:
		return rfb.CodeKeyA + rfb.KeyCode(key-ui.KeyA)
	case key >= ui.KeyF1 && key <= ui.KeyF12:
		return rfb.CodeF1 + rfb.KeyCode(key-ui.KeyF1)
	}
	switch key {
	case ui.Key0:
		return rfb.CodeDigit0
	case ui.Key1:
		return rfb.CodeDigit1
	case ui.Key2:
		return rfb.CodeDigit2
	case ui.Key3:
		return rfb.CodeDigit3
	case ui.Key4:
		return rfb.CodeDigit4
	case ui.Key5:
		return rfb.CodeDigit5
	case ui.Key6:
		return rfb.CodeDigit6
	case ui.Key7:
		return rfb.CodeDigit7
	case ui.Key8:
		return rfb.CodeDigit8
	case ui.Key9:
		return rfb.CodeDigit9
	case ui.KeyMinus:
		return rfb.CodeMinus
	case ui.KeyEqual:
		return rfb.CodeEqual
	case ui.KeyComma:
		return rfb.CodeComma
	case ui.KeyPeriod:
		return rfb.CodePeriod
	case ui.KeySlash:
		return rfb.CodeSlash
	case ui.KeySemicolon:
		return rfb.CodeSemicolon
	case ui.KeyQuote:
		return rfb.CodeQuote
	case ui.KeyBracketLeft:
		return rfb.CodeBracketLeft
	case ui.KeyBracketRight:
		return rfb.CodeBracketRight
	case ui.KeyBackslash:
		return rfb.CodeBackslash
	case ui.KeyBackquote:
		return rfb.CodeBackquote
	case ui.KeyEnter:
		return rfb.CodeEnter
	case ui.KeyEscape:
		return rfb.CodeEscape
	case ui.KeyBackspace:
		return rfb.CodeBackspace
	case ui.KeyTab:
		return rfb.CodeTab
	case ui.KeySpace:
		return rfb.CodeSpace
	case ui.KeyDelete:
		return rfb.CodeDelete
	case ui.KeyInsert:
		return rfb.CodeInsert
	case ui.KeyHome:
		return rfb.CodeHome
	case ui.KeyEnd:
		return rfb.CodeEnd
	case ui.KeyPageUp:
		return rfb.CodePageUp
	case ui.KeyPageDown:
		return rfb.CodePageDown
	case ui.KeyLeft:
		return rfb.CodeArrowLeft
	case ui.KeyRight:
		return rfb.CodeArrowRight
	case ui.KeyUp:
		return rfb.CodeArrowUp
	case ui.KeyDown:
		return rfb.CodeArrowDown
	case ui.KeyContextMenu:
		return rfb.CodeContextMenu
	case ui.KeyBack:
		return rfb.CodeBack
	case ui.KeyForward:
		return rfb.CodeForward
	}
	return rfb.CodeUnknown
}
