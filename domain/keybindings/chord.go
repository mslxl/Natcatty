package keybindings

import "strings"

// Modifiers is the set of modifiers a chord requires. The four bits are
// independent of the platform: the mac scheme uses Super, Ctrl, and Alt for
// the displayed ⌘, ⌃, and ⌥, and the PC scheme uses Ctrl, Alt, and Super for
// Ctrl, Alt, and Win.
type Modifiers uint8

const (
	ModCtrl Modifiers = 1 << iota
	ModAlt
	ModShift
	ModSuper
)

// Chord is one parsed shortcut. Range and Arrows are patterns rather than
// single keys: Range expands to digits 1 through 9, and Arrows expands to the
// four arrow keys.
type Chord struct {
	Mods   Modifiers
	Key    string
	Range  bool
	Arrows bool
}

// ParseChord parses the original's chord notation. It accepts the symbolic
// mac forms (⌘, ⌃, ⌥, ↑, ⌫, ↵, ⇥) and the textual PC forms (Ctrl, Alt, Shift,
// Win, Super, ArrowUp, Backspace, Enter, Tab) so a persisted override from
// either scheme round-trips.
func ParseChord(raw string) (Chord, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == Disabled {
		return Chord{}, false
	}
	parts := strings.Split(raw, "+")
	var chord Chord
	for _, part := range parts {
		token := strings.TrimSpace(part)
		switch strings.ToLower(token) {
		case "⌘", "cmd", "command", "super", "meta", "win":
			chord.Mods |= ModSuper
		case "⌃", "ctrl", "control":
			chord.Mods |= ModCtrl
		case "⌥", "alt", "option":
			chord.Mods |= ModAlt
		case "shift":
			chord.Mods |= ModShift
		}
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return Chord{}, false
	}
	if mods, isModifier := modifierToken(last); isModifier {
		// A chord must end in a key, not a bare modifier.
		_ = mods
		return Chord{}, false
	}
	switch last {
	case "[1...9]":
		chord.Range = true
		return chord, true
	case "arrows":
		chord.Arrows = true
		return chord, true
	}
	key, ok := canonicalKey(last)
	if !ok {
		return Chord{}, false
	}
	chord.Key = key
	return chord, true
}

// AcceleratorChord parses an Electron-style menu accelerator. CmdOrCtrl is
// the platform's shortcut modifier: Command on macOS, Control elsewhere.
func AcceleratorChord(raw, goos string) (Chord, bool) {
	replaced := raw
	platformMod := "Ctrl"
	acceleratorName := "CmdOrCtrl"
	if goos == "darwin" {
		platformMod = "Super"
	}
	replaced = strings.ReplaceAll(replaced, acceleratorName, platformMod)
	// CommandOrControl is Electron's other spelling.
	replaced = strings.ReplaceAll(replaced, "CommandOrControl", platformMod)
	return ParseChord(replaced)
}

func modifierToken(token string) (Modifiers, bool) {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "⌘", "cmd", "command", "super", "meta", "win":
		return ModSuper, true
	case "⌃", "ctrl", "control":
		return ModCtrl, true
	case "⌥", "alt", "option":
		return ModAlt, true
	case "shift":
		return ModShift, true
	}
	return 0, false
}

var namedKeys = map[string]string{
	"space":      "Space",
	" ":          "Space",
	"↑":          "Up",
	"arrowup":    "Up",
	"up":         "Up",
	"↓":          "Down",
	"arrowdown":  "Down",
	"down":       "Down",
	"←":          "Left",
	"arrowleft":  "Left",
	"left":       "Left",
	"→":          "Right",
	"arrowright": "Right",
	"right":      "Right",
	"esc":        "Esc",
	"escape":     "Esc",
	"⌫":          "Backspace",
	"backspace":  "Backspace",
	"del":        "Delete",
	"delete":     "Delete",
	"↵":          "Enter",
	"enter":      "Enter",
	"return":     "Enter",
	"⇥":          "Tab",
	"tab":        "Tab",
}

func canonicalKey(token string) (string, bool) {
	if named, ok := namedKeys[strings.ToLower(token)]; ok {
		return named, true
	}
	if len(token) == 1 {
		char := token[0]
		switch {
		case char >= 'a' && char <= 'z':
			return strings.ToUpper(token), true
		case char >= 'A' && char <= 'Z':
			return token, true
		case char >= '0' && char <= '9':
			return token, true
		default:
			// Punctuation is compared literally, including , . / ; ' [ ] \ - = and `.
			if char > ' ' && char < 0x7f {
				return token, true
			}
		}
	}
	if len(token) > 1 && (token[0] == 'F' || token[0] == 'f') {
		number := token[1:]
		if number == "1" || number == "2" || number == "3" || number == "4" ||
			number == "5" || number == "6" || number == "7" || number == "8" ||
			number == "9" || number == "10" || number == "11" || number == "12" {
			return "F" + number, true
		}
	}
	return "", false
}

// Overlaps reports whether two chords can fire on the same key press. A
// range covers digits 1-9 and the arrow pattern covers the four arrows, so
// conflict detection treats patterns as sets rather than strings.
func (c Chord) Overlaps(other Chord) bool {
	if c.Mods != other.Mods {
		return false
	}
	switch {
	case c.Range && other.Range:
		return true
	case c.Range:
		return isDigit1to9(other.Key)
	case other.Range:
		return isDigit1to9(c.Key)
	case c.Arrows && other.Arrows:
		return true
	case c.Arrows:
		return isArrow(other.Key)
	case other.Arrows:
		return isArrow(c.Key)
	default:
		return c.Key == other.Key
	}
}

func isDigit1to9(key string) bool {
	return len(key) == 1 && key[0] >= '1' && key[0] <= '9'
}

func isArrow(key string) bool {
	switch key {
	case "Up", "Down", "Left", "Right":
		return true
	default:
		return false
	}
}

// String renders the chord in the notation of scheme. It is what the binding
// recorder stores, so a round trip through ParseChord is stable.
func (c Chord) String(scheme Scheme) string {
	var parts []string
	mac := scheme == SchemeMac
	if mac {
		if c.Mods&ModSuper != 0 {
			parts = append(parts, "⌘")
		}
		if c.Mods&ModCtrl != 0 {
			parts = append(parts, "⌃")
		}
		if c.Mods&ModAlt != 0 {
			parts = append(parts, "⌥")
		}
		if c.Mods&ModShift != 0 {
			parts = append(parts, "Shift")
		}
	} else {
		if c.Mods&ModCtrl != 0 {
			parts = append(parts, "Ctrl")
		}
		if c.Mods&ModAlt != 0 {
			parts = append(parts, "Alt")
		}
		if c.Mods&ModShift != 0 {
			parts = append(parts, "Shift")
		}
		if c.Mods&ModSuper != 0 {
			parts = append(parts, "Win")
		}
	}
	switch {
	case c.Range:
		parts = append(parts, "[1...9]")
	case c.Arrows:
		parts = append(parts, "arrows")
	default:
		parts = append(parts, displayKey(c.Key))
	}
	return strings.Join(parts, " + ")
}

func displayKey(key string) string {
	switch key {
	case "Up":
		return "↑"
	case "Down":
		return "↓"
	case "Left":
		return "←"
	case "Right":
		return "→"
	case "Backspace":
		return "⌫"
	case "Delete":
		return "Del"
	case "Enter":
		return "↵"
	case "Tab":
		return "⇥"
	case "Esc":
		return "Esc"
	default:
		return key
	}
}
