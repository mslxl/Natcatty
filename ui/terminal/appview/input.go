package appview

import (
	"fmt"
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
	// xterm's modifier parameter is 1 plus Shift (1), Alt (2) and Ctrl (4).
	// It is what a program reads a modified arrow or function key as; without
	// it Ctrl+Left arrived as a plain Left, so word-wise movement in readline,
	// vim and less never happened.
	mod := 1
	if shift {
		mod++
	}
	if alt {
		mod += 2
	}
	if ctrl {
		mod += 4
	}
	// cursorKey is an arrow or Home/End key: CSI final, or CSI 1 ; mod final
	// with a modifier.
	cursorKey := func(final byte) []byte {
		if mod == 1 {
			return []byte{0x1b, '[', final}
		}
		return []byte(fmt.Sprintf("\x1b[1;%d%c", mod, final))
	}
	// tildeKey is Insert, Delete, PageUp/PageDown or F5-F12: CSI number ~,
	// with the modifier before the tilde.
	tildeKey := func(number int) []byte {
		if mod == 1 {
			return []byte(fmt.Sprintf("\x1b[%d~", number))
		}
		return []byte(fmt.Sprintf("\x1b[%d;%d~", number, mod))
	}
	// fKey is F1-F4, which xterm sends as SS3 without modifiers and as
	// CSI 1 ; mod final with them.
	fKey := func(final byte) []byte {
		if mod == 1 {
			return []byte{0x1b, 'O', final}
		}
		return []byte(fmt.Sprintf("\x1b[1;%d%c", mod, final))
	}
	switch key {
	case ui.KeyEnter:
		if alt {
			return seq("\x1b\r")
		}
		return seq("\r")
	case ui.KeyBackspace:
		b := []byte{0x7f}
		if ctrl {
			b = []byte{0x08}
		}
		if alt {
			// Meta-Backspace deletes the word before the cursor in readline.
			b = append([]byte{0x1b}, b...)
		}
		return b, true
	case ui.KeyTab:
		if shift {
			return seq("\x1b[Z")
		}
		return seq("\t")
	case ui.KeyEscape:
		if alt {
			return seq("\x1b\x1b")
		}
		return seq("\x1b")
	case ui.KeySpace:
		if ctrl {
			return seq("\x00")
		}
		return nil, false // a plain space comes as text
	case ui.KeyUp:
		return cursorKey('A'), true
	case ui.KeyDown:
		return cursorKey('B'), true
	case ui.KeyRight:
		return cursorKey('C'), true
	case ui.KeyLeft:
		return cursorKey('D'), true
	case ui.KeyHome:
		return cursorKey('H'), true
	case ui.KeyEnd:
		return cursorKey('F'), true
	case ui.KeyDelete:
		return tildeKey(3), true
	case ui.KeyPageUp:
		return tildeKey(5), true
	case ui.KeyPageDown:
		return tildeKey(6), true
	case ui.KeyInsert:
		return tildeKey(2), true
	}
	if key >= ui.KeyF1 && key <= ui.KeyF12 {
		n := int(key - ui.KeyF1)
		if n < 4 {
			return fKey("PQRS"[n]), true
		}
		// F5 starts the tilde group; F11 and F12 skip 22.
		return tildeKey([]int{15, 17, 18, 19, 20, 21, 23, 24}[n-4]), true
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
