package appview

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// InputBytes translates one input event into what a terminal program expects, or
// nil when the event must not be forwarded. The live screen is drawn by this
// package, so keys are translated here and handed to the terminal with Send:
// printable text arrives as InputText (also for input methods), and the keys that
// only make sense to a program are mapped to their escape sequences.
func InputBytes(ev ui.InputEvent) []byte {
	switch ev.Kind {
	case ui.InputText:
		return []byte(ev.Text)
	case ui.InputCommand:
		return nil // copy/cut/select all belong to the app, not the program
	case ui.InputKeyDown:
		if b, ok := keyBytes(ev); ok {
			return b
		}
	}
	return nil
}

func keyBytes(ev ui.InputEvent) ([]byte, bool) {
	key := ev.Key
	if key == ui.KeyUnknown && ev.PhysicalKey != ui.KeyUnknown {
		key = ev.PhysicalKey
	}
	ctrl := ev.Mods&ui.Ctrl != 0
	alt := ev.Mods&ui.Alt != 0
	shift := ev.Mods&ui.Shift != 0

	seq := func(s string) ([]byte, bool) { return []byte(s), true }
	switch key {
	case ui.KeyEnter:
		if alt {
			return seq("\x1b\r")
		}
		return seq("\r")
	case ui.KeyBackspace:
		if ctrl {
			return seq("\x08")
		}
		return seq("\x7f")
	case ui.KeyTab:
		if shift {
			return seq("\x1b[Z")
		}
		return seq("\t")
	case ui.KeyEscape:
		return seq("\x1b")
	case ui.KeySpace:
		if ctrl {
			return seq("\x00")
		}
		return nil, false // a plain space comes as text
	case ui.KeyUp:
		return seq("\x1b[A")
	case ui.KeyDown:
		return seq("\x1b[B")
	case ui.KeyRight:
		return seq("\x1b[C")
	case ui.KeyLeft:
		return seq("\x1b[D")
	case ui.KeyHome:
		return seq("\x1b[H")
	case ui.KeyEnd:
		return seq("\x1b[F")
	case ui.KeyDelete:
		return seq("\x1b[3~")
	case ui.KeyPageUp:
		return seq("\x1b[5~")
	case ui.KeyPageDown:
		return seq("\x1b[6~")
	case ui.KeyInsert:
		return seq("\x1b[2~")
	}

	// Control and Alt combinations of a letter.
	if name := keyLetter(key); name != "" {
		if ctrl {
			return []byte{byte(name[0] - 'a' + 1)}, true
		}
		if alt {
			return []byte("\x1b" + name), true
		}
	}
	// Control combinations that are punctuation in ASCII.
	if ctrl {
		switch key {
		case ui.KeyBracketLeft:
			return seq("\x1b")
		case ui.KeyBackslash:
			return seq("\x1c")
		case ui.KeyBracketRight:
			return seq("\x1d")
		case ui.KeySlash:
			return seq("\x1f")
		}
	}
	return nil, false
}

// keyLetter returns the lowercase letter of a letter key, if it is one.
func keyLetter(key ui.Key) string {
	for r := ui.KeyA; r <= ui.KeyZ; r++ {
		if key == r {
			s := string(rune('a' + (r - ui.KeyA)))
			return strings.ToLower(s)
		}
	}
	return ""
}
