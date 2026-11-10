package app

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	"github.com/mslxl/natcatty/ui/pluginview"
)

// acceleratorBinding is one menu shortcut the native window handles. It is
// derived from the menu itself, so a label the user sees and the key that
// runs it cannot drift.
type acceleratorBinding struct {
	mods ui.Modifiers
	key  ui.Key
	item *mygo.MenuItem
}

// keyShortcut is one binding-derived action the window handles. It differs
// from a menu accelerator only in how it runs.
type keyShortcut struct {
	mods ui.Modifiers
	key  ui.Key
	run  func()
}

// MenuAccelerators walks the menu and returns the shortcuts that run an
// application command. Only items with both an accelerator and a Click
// handler qualify: the standard role menus (Edit, Window, the macOS
// application menu) are left to MyGo and never shadow a terminal's own keys,
// such as Ctrl+C and Ctrl+V.
//
// The result is the single source of truth for native accelerator dispatch:
// it is built from the same menu the OS displays.
func MenuAccelerators(menu *mygo.Menu) []acceleratorBinding {
	if menu == nil {
		return nil
	}
	return collectAccelerators(menu.Items(), nil)
}

func collectAccelerators(items []*mygo.MenuItem, out []acceleratorBinding) []acceleratorBinding {
	for _, item := range items {
		if item == nil {
			continue
		}
		if len(item.Submenu) > 0 {
			out = collectAccelerators(item.Submenu, out)
			continue
		}
		if item.Accelerator == "" || item.Click == nil || item.Role != "" {
			continue
		}
		mods, key, ok := parseAccelerator(item.Accelerator)
		if !ok {
			continue
		}
		out = append(out, acceleratorBinding{mods: mods, key: key, item: item})
	}
	return out
}

// bindAccelerators registers every menu shortcut at the window's root
// context. MyGo claims a registered window shortcut before the focused
// element's input handler sees the key, so an accelerator fires even while a
// terminal has focus; combinations that are not in the table, such as Ctrl+C
// and Ctrl+V, still reach the terminal untouched.
func bindAccelerators(c *ui.Context, bindings []acceleratorBinding) {
	if c == nil {
		return
	}
	for _, binding := range bindings {
		item := binding.item
		c.OnShortcut(binding.mods, binding.key, func() {
			item.Click(item, nil)
		})
	}
}

// bindShortcuts registers binding-derived global actions at window scope.
func bindShortcuts(c *ui.Context, shortcuts []keyShortcut) {
	if c == nil {
		return
	}
	for _, shortcut := range shortcuts {
		shortcut := shortcut
		c.OnShortcut(shortcut.mods, shortcut.key, shortcut.run)
	}
}

// The native UI environment names a host shortcut with one opaque token per
// window table entry. A plugin view reports the token back through its command
// runner, which resolves it against the live tables; a token is stable while
// the binding exists, so a rebuild never runs a different command.
const (
	menuShortcutPrefix      = "menu:"
	menuChordShortcutPrefix = "menu-chord:"
	bindingShortcutPrefix   = "binding:"
)

func shortcutChordToken(mods ui.Modifiers, key ui.Key) string {
	return strconv.FormatUint(uint64(mods), 10) + ":" + strconv.FormatUint(uint64(key), 10)
}

// HostShortcuts returns the live window shortcut table as the native UI
// environment describes it. Menu accelerators come first, exactly as
// bindAccelerators registers them, and a chord bound twice is listed once, so
// a plugin view sees the action the window would actually run.
func (m *windowManager) HostShortcuts() []domainnativeui.Shortcut {
	if m == nil {
		return nil
	}
	seen := make(map[string]bool, len(m.accelerators)+len(m.shortcuts))
	shortcuts := make([]domainnativeui.Shortcut, 0, len(m.accelerators)+len(m.shortcuts))
	add := func(mods ui.Modifiers, key ui.Key, command string) {
		chord := shortcutChordToken(mods, key)
		if seen[chord] {
			return
		}
		shortcut, ok := pluginview.NativeShortcut(mods, key, command)
		if !ok {
			return
		}
		seen[chord] = true
		shortcuts = append(shortcuts, shortcut)
	}
	for _, binding := range m.accelerators {
		if binding.item == nil {
			continue
		}
		command := menuChordShortcutPrefix + shortcutChordToken(binding.mods, binding.key)
		if binding.item.ID != "" {
			command = menuShortcutPrefix + binding.item.ID
		}
		add(binding.mods, binding.key, command)
	}
	for _, shortcut := range m.shortcuts {
		if shortcut.run == nil {
			continue
		}
		add(shortcut.mods, shortcut.key, bindingShortcutPrefix+shortcutChordToken(shortcut.mods, shortcut.key))
	}
	return shortcuts
}

// RunHostShortcut runs the action one environment token names. It is the
// plugin view's command runner, so a host shortcut that reaches a view runs
// the same action a window accelerator would. An unknown token reports false.
func (m *windowManager) RunHostShortcut(command string) bool {
	if m == nil {
		return false
	}
	switch {
	case strings.HasPrefix(command, menuShortcutPrefix):
		id := strings.TrimPrefix(command, menuShortcutPrefix)
		for _, binding := range m.accelerators {
			if binding.item != nil && binding.item.ID == id && binding.item.Click != nil {
				binding.item.Click(binding.item, nil)
				return true
			}
		}
	case strings.HasPrefix(command, menuChordShortcutPrefix):
		chord := strings.TrimPrefix(command, menuChordShortcutPrefix)
		for _, binding := range m.accelerators {
			if binding.item != nil && binding.item.Click != nil &&
				shortcutChordToken(binding.mods, binding.key) == chord {
				binding.item.Click(binding.item, nil)
				return true
			}
		}
	case strings.HasPrefix(command, bindingShortcutPrefix):
		chord := strings.TrimPrefix(command, bindingShortcutPrefix)
		for _, shortcut := range m.shortcuts {
			if shortcut.run != nil && shortcutChordToken(shortcut.mods, shortcut.key) == chord {
				shortcut.run()
				return true
			}
		}
	}
	return false
}

// parseAccelerator reads the menu's Electron-style accelerator string. It
// supports the names the menu uses today and the keys MyGo exposes.
func parseAccelerator(spec string) (ui.Modifiers, ui.Key, bool) {
	parts := strings.Split(spec, "+")
	var mods ui.Modifiers
	for _, raw := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "cmdorctrl", "commandorcontrol":
			// The platform's shortcut modifier: Command on macOS, Control
			// elsewhere, exactly as MyGo draws it.
			mods |= ui.Cmd
		case "cmd", "command":
			mods |= ui.Super
		case "ctrl", "control":
			mods |= ui.Ctrl
		case "shift":
			mods |= ui.Shift
		case "alt", "option":
			mods |= ui.Alt
		case "super", "meta":
			mods |= ui.Super
		default:
			return 0, 0, false
		}
	}
	key, ok := parseAcceleratorKey(strings.TrimSpace(parts[len(parts)-1]))
	if !ok {
		return 0, 0, false
	}
	return mods, key, true
}

func parseAcceleratorKey(name string) (ui.Key, bool) {
	switch strings.ToUpper(name) {
	case "LEFT":
		return ui.KeyLeft, true
	case "RIGHT":
		return ui.KeyRight, true
	case "UP":
		return ui.KeyUp, true
	case "DOWN":
		return ui.KeyDown, true
	case "HOME":
		return ui.KeyHome, true
	case "END":
		return ui.KeyEnd, true
	case "PAGEUP", "PGUP":
		return ui.KeyPageUp, true
	case "PAGEDOWN", "PGDN":
		return ui.KeyPageDown, true
	case "ENTER", "RETURN":
		return ui.KeyEnter, true
	case "ESCAPE", "ESC":
		return ui.KeyEscape, true
	case "TAB":
		return ui.KeyTab, true
	case "SPACE":
		return ui.KeySpace, true
	case "BACKSPACE":
		return ui.KeyBackspace, true
	case "DELETE", "DEL":
		return ui.KeyDelete, true
	case "INSERT":
		return ui.KeyInsert, true
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
	case "=", "+":
		return ui.KeyEqual, true
	}
	if len(name) == 1 {
		char := name[0]
		switch {
		case char >= 'a' && char <= 'z':
			return ui.KeyA + ui.Key(char-'a'), true
		case char >= 'A' && char <= 'Z':
			return ui.KeyA + ui.Key(char-'A'), true
		case char >= '0' && char <= '9':
			return ui.Key0 + ui.Key(char-'0'), true
		}
	}
	if len(name) > 1 && (name[0] == 'f' || name[0] == 'F') {
		if number, err := strconv.Atoi(name[1:]); err == nil && number >= 1 && number <= 12 {
			return ui.KeyF1 + ui.Key(number-1), true
		}
	}
	return 0, false
}
