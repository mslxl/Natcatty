package transfer

import (
	"strings"

	"github.com/egoist/mygo/ui"

	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// installShortcuts registers the active catalog's SFTP actions on the surface
// root. The handlers are scoped to the root's focus subtree, so they never
// shadow a terminal's Ctrl+C and never fire while another surface is shown.
// The view skips this while a prompt is open, where Enter belongs to the
// field.
func installShortcuts(root ui.Element, s *transferstate.State) {
	for _, active := range s.Bindings() {
		action := active.Binding.Action
		if !strings.HasPrefix(action, "sftp") {
			continue
		}
		if active.Parsed.Range || active.Parsed.Arrows {
			continue
		}
		key, ok := shortcutKey(active.Parsed.Key)
		if !ok {
			continue
		}
		act := action
		root.OnShortcut(shortcutMods(active.Parsed), key, func() {
			s.Run(act)
		})
	}
	// The arrows move the focused pane's selection, as the original's list;
	// Shift extends it.
	for _, arrow := range []struct {
		key   ui.Key
		delta int
	}{{ui.KeyUp, -1}, {ui.KeyDown, 1}} {
		delta := arrow.delta
		root.OnShortcut(0, arrow.key, func() { s.MoveSelection(s.Focused(), delta, false) })
		root.OnShortcut(ui.Shift, arrow.key, func() { s.MoveSelection(s.Focused(), delta, true) })
	}
}

func shortcutMods(chord domainkeybindings.Chord) ui.Modifiers {
	var mods ui.Modifiers
	if chord.Mods&domainkeybindings.ModCtrl != 0 {
		mods |= ui.Ctrl
	}
	if chord.Mods&domainkeybindings.ModAlt != 0 {
		mods |= ui.Alt
	}
	if chord.Mods&domainkeybindings.ModShift != 0 {
		mods |= ui.Shift
	}
	if chord.Mods&domainkeybindings.ModSuper != 0 {
		mods |= ui.Super
	}
	return mods
}

// shortcutKey maps the catalog's canonical key name onto MyGo's key. It
// covers the keys the SFTP catalog uses and the navigation punctuation a
// rebound shortcut may choose.
func shortcutKey(name string) (ui.Key, bool) {
	if len(name) == 1 {
		switch char := name[0]; {
		case char >= 'a' && char <= 'z':
			return ui.KeyA + ui.Key(char-'a'), true
		case char >= 'A' && char <= 'Z':
			return ui.KeyA + ui.Key(char-'A'), true
		case char >= '0' && char <= '9':
			return ui.Key0 + ui.Key(char-'0'), true
		}
		switch name {
		case ",":
			return ui.KeyComma, true
		case ".":
			return ui.KeyPeriod, true
		case "/":
			return ui.KeySlash, true
		case ";":
			return ui.KeySemicolon, true
		case "'":
			return ui.KeyQuote, true
		case "[":
			return ui.KeyBracketLeft, true
		case "]":
			return ui.KeyBracketRight, true
		case "\\":
			return ui.KeyBackslash, true
		case "`":
			return ui.KeyBackquote, true
		case "-":
			return ui.KeyMinus, true
		case "=":
			return ui.KeyEqual, true
		}
	}
	switch name {
	case "Esc":
		return ui.KeyEscape, true
	case "Backspace":
		return ui.KeyBackspace, true
	case "Delete":
		return ui.KeyDelete, true
	case "Enter":
		return ui.KeyEnter, true
	case "Tab":
		return ui.KeyTab, true
	case "Space":
		return ui.KeySpace, true
	case "Up":
		return ui.KeyUp, true
	case "Down":
		return ui.KeyDown, true
	case "Left":
		return ui.KeyLeft, true
	case "Right":
		return ui.KeyRight, true
	case "Home":
		return ui.KeyHome, true
	case "End":
		return ui.KeyEnd, true
	case "PageUp":
		return ui.KeyPageUp, true
	case "PageDown":
		return ui.KeyPageDown, true
	case "Insert":
		return ui.KeyInsert, true
	}
	if len(name) > 1 && name[0] == 'F' && len(name) <= 3 {
		number := 0
		for _, digit := range name[1:] {
			if digit < '0' || digit > '9' {
				return 0, false
			}
			number = number*10 + int(digit-'0')
		}
		if number >= 1 && number <= 12 {
			return ui.KeyF1 + ui.Key(number-1), true
		}
	}
	return 0, false
}
