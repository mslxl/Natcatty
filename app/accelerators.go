package app

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// acceleratorBinding is one menu shortcut the native window handles. It is
// derived from the menu itself, so a label the user sees and the key that
// runs it cannot drift.
type acceleratorBinding struct {
	mods ui.Modifiers
	key  ui.Key
	item *mygo.MenuItem
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
