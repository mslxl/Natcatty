package app

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domain "github.com/mslxl/natcatty/domain/keybindings"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/domain/workspace"
	uisearch "github.com/mslxl/natcatty/ui/search"
	"github.com/mslxl/natcatty/ui/shell/state"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// unavailableBindings are catalog entries whose feature this build does not
// have. They keep their row and override controls in Settings but are never
// dispatched, so a key can never run a different action than its label says.
var unavailableBindings = map[string]bool{
	"toggle-pane-zoom": true,

	"port-forwarding":   true,
	"new-workspace":     true,
	"snippets":          true,
	"toggle-side-panel": true,
	"paste-selection":   true,
	"search-terminal":   true,
}

// catalogBindingAvailable reports whether this build can run a catalog
// binding. It is the single availability source for the Settings window and
// the dispatcher.
func catalogBindingAvailable(id string) bool { return !unavailableBindings[id] }

// menuHandledBindings already have a menu accelerator, which dispatches the
// same action. Registering them again as global shortcuts would make two
// handlers race for one key.
var menuHandledBindings = map[string]bool{
	"new-tab":          true,
	"close-tab":        true,
	"close-session":    true,
	"split-horizontal": true,
	"split-vertical":   true,
	"open-settings":    true,
}

// derivedMenuItems are menu items whose accelerator comes from the binding
// catalog. Conflict detection must not report those as foreign commands.
var derivedMenuItems = map[string]bool{
	"menu.newTab":     true,
	"menu.closeTab":   true,
	"menu.closePane":  true,
	"menu.splitRight": true,
	"menu.splitDown":  true,
	"menu.settings":   true,
	"menu.focusLeft":  true,
	"menu.focusRight": true,
	"menu.focusUp":    true,
	"menu.focusDown":  true,
}

// menuBindingSource projects the active scheme and overrides onto the native
// menu's accelerator strings.
type menuBindingSource struct {
	service *appkeybindings.Service
}

func (s menuBindingSource) Accelerator(id string) (string, bool) {
	binding, ok := domain.Lookup(id)
	if !ok || !catalogBindingAvailable(id) {
		return "", false
	}
	chord := domain.EffectiveChord(binding, s.service.Scheme(), s.service.State().Overrides)
	parsed, ok := domain.ParseChord(chord)
	if !ok || s.reserved(parsed) {
		return "", false
	}
	return menuAccelerator(chord)
}

// reserved reports whether the terminal owns a chord. A menu accelerator
// registered at window scope would run before the focused terminal, so the
// menu must not display a key that the terminal keeps. The conflict is
// reported while recording; after Replace the terminal still wins.
func (s menuBindingSource) reserved(chord domain.Chord) bool {
	key, ok := mygoKey(chord.Key)
	if !ok {
		return false
	}
	reserved := terminalReservedChords(s.service.Scheme(), s.service.State().Overrides)
	return reserved[chordKey{mods: chordMods(chord), key: key}]
}

func (s menuBindingSource) FocusAccelerator(direction string) (string, bool) {
	binding, ok := domain.Lookup("move-focus")
	if !ok || !catalogBindingAvailable("move-focus") {
		return "", false
	}
	scheme := s.service.Scheme()
	// An override is shown by the dispatcher, not by four menu items that
	// cannot know which arrow the user meant.
	if s.service.IsOverridden("move-focus", scheme) {
		return "", false
	}
	parsed, ok := domain.ParseChord(domain.EffectiveChord(binding, scheme, s.service.State().Overrides))
	if !ok || !parsed.Arrows {
		return "", false
	}
	focus := domain.Chord{Mods: parsed.Mods, Key: direction}
	if s.reserved(focus) {
		return "", false
	}
	return acceleratorString(focus), true
}

func (s menuBindingSource) MenuSuppressed(menuID string) bool {
	return s.service.MenuSuppressed(menuID)
}

// menuAccelerator renders a single-key chord as an Electron accelerator.
// Patterns and disabled bindings have no menu representation.
func menuAccelerator(chord string) (string, bool) {
	if chord == "" || chord == domain.Disabled {
		return "", false
	}
	parsed, ok := domain.ParseChord(chord)
	if !ok || parsed.Range || parsed.Arrows || parsed.Key == "" {
		return "", false
	}
	return acceleratorString(parsed), true
}

func acceleratorString(chord domain.Chord) string {
	var parts []string
	if chord.Mods&domain.ModSuper != 0 {
		parts = append(parts, "Super")
	}
	if chord.Mods&domain.ModCtrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if chord.Mods&domain.ModAlt != 0 {
		parts = append(parts, "Alt")
	}
	if chord.Mods&domain.ModShift != 0 {
		parts = append(parts, "Shift")
	}
	return strings.Join(append(parts, chord.Key), "+")
}

// menuShortcuts lists the live menu accelerators for conflict detection. A
// binding-derived accelerator is marked so it does not conflict with the
// binding that produced it.
func menuShortcuts(menu *mygo.Menu) []domain.MenuShortcut {
	if menu == nil {
		return nil
	}
	return collectMenuShortcuts(menu.Items(), nil)
}

func collectMenuShortcuts(items []*mygo.MenuItem, out []domain.MenuShortcut) []domain.MenuShortcut {
	for _, item := range items {
		if item == nil {
			continue
		}
		if len(item.Submenu) > 0 {
			out = collectMenuShortcuts(item.Submenu, out)
			continue
		}
		if item.Accelerator == "" || item.Click == nil || item.Role != "" {
			continue
		}
		out = append(out, domain.MenuShortcut{
			ID:          item.ID,
			Label:       item.Label,
			Accelerator: item.Accelerator,
			Derived:     derivedMenuItems[item.ID],
		})
	}
	return out
}

// bindingDispatcher turns active catalog bindings into window shortcuts. It
// deliberately leaves terminal-owned chords and menu-handled bindings alone.
type bindingDispatcher struct {
	service *appkeybindings.Service
	shell   *state.State
	search  *uisearch.State
	// transfer owns the SFTP surface's state. The surface registers its own
	// scoped shortcuts; only open-sftp is dispatched at window scope.
	transfer      *transferstate.State
	focusMain     func()
	toggleOpacity func()
}

func (d *bindingDispatcher) Shortcuts() []keyShortcut {
	if d == nil || d.service == nil || d.shell == nil {
		return nil
	}
	active := d.service.Active()
	scheme := d.service.Scheme()
	overridden := d.service.IsOverridden
	reserved := terminalReservedChords(scheme, d.service.State().Overrides)

	var out []keyShortcut
	add := func(mods ui.Modifiers, key ui.Key, run func()) {
		if reserved[chordKey{mods: mods, key: key}] {
			return
		}
		out = append(out, keyShortcut{mods: mods, key: key, run: run})
	}
	for _, candidate := range active {
		id := candidate.Binding.ID
		if !catalogBindingAvailable(id) {
			continue
		}
		if id == "move-focus" {
			// Without an override the four menu accelerators run it; with one
			// the menu cannot show a direction, so the dispatcher owns it.
			if !overridden(id, scheme) {
				continue
			}
			d.addDirectional(add, candidate.Parsed)
			continue
		}
		if menuHandledBindings[id] {
			continue
		}
		action, ok := d.action(id)
		if !ok {
			continue
		}
		if candidate.Parsed.Range {
			for number := 1; number <= 9; number++ {
				number := number
				add(chordMods(candidate.Parsed), ui.Key0+ui.Key(number), func() { action(number) })
			}
			continue
		}
		if candidate.Parsed.Arrows {
			// Only move-focus uses the arrow pattern, handled above.
			continue
		}
		key, ok := mygoKey(candidate.Parsed.Key)
		if !ok {
			continue
		}
		run := func() { action(0) }
		add(chordMods(candidate.Parsed), key, run)
	}
	return out
}

// addDirectional registers a move-focus override. The catalog's pattern
// covers all four arrows; a recorded single arrow affects only that one.
func (d *bindingDispatcher) addDirectional(add func(ui.Modifiers, ui.Key, func()),
	chord domain.Chord) {
	mods := chordMods(chord)
	if chord.Arrows {
		for _, direction := range []struct {
			key  ui.Key
			side workspace.FocusDirection
		}{
			{ui.KeyLeft, workspace.FocusLeft}, {ui.KeyRight, workspace.FocusRight},
			{ui.KeyUp, workspace.FocusUp}, {ui.KeyDown, workspace.FocusDown},
		} {
			direction := direction
			add(mods, direction.key, func() { d.shell.FocusPane(direction.side) })
		}
		return
	}
	side, ok := arrowDirection(chord.Key)
	if !ok {
		return
	}
	key, ok := mygoKey(chord.Key)
	if !ok {
		return
	}
	add(mods, key, func() { d.shell.FocusPane(side) })
}

func arrowDirection(key string) (workspace.FocusDirection, bool) {
	switch key {
	case "Left":
		return workspace.FocusLeft, true
	case "Right":
		return workspace.FocusRight, true
	case "Up":
		return workspace.FocusUp, true
	case "Down":
		return workspace.FocusDown, true
	default:
		return "", false
	}
}

// action returns the numbered action for a binding ID. The argument is only
// meaningful for switchToTab (1-9) and moveFocus (a workspace direction).
func (d *bindingDispatcher) action(id string) (func(int), bool) {
	switch id {
	case "switch-tab-1-9":
		return func(number int) {
			if number >= 1 && number <= d.shell.TabCount() {
				d.shell.SelectTab(number - 1)
			}
		}, true
	case "next-tab":
		return func(int) {
			if count := d.shell.TabCount(); count > 0 {
				d.shell.SelectTab((d.shell.ActiveTab() + 1) % count)
			}
		}, true
	case "prev-tab":
		return func(int) {
			if count := d.shell.TabCount(); count > 0 {
				d.shell.SelectTab((d.shell.ActiveTab() - 1 + count) % count)
			}
		}, true
	case "open-hosts":
		return func(int) { d.shell.ShowVault() }, true
	case "open-sftp":
		if d.transfer == nil {
			return nil, false
		}
		return func(int) {
			d.shell.ShowSFTP()
			d.transfer.EnsureStarted()
		}, true
	case "transfer-center":
		if d.transfer == nil {
			return nil, false
		}
		// The center lives on the SFTP surface, so opening it from a terminal
		// also reveals that surface; pressing the chord again closes it.
		return func(int) {
			if d.transfer.CenterOpen() {
				d.transfer.CloseCenter()
				return
			}
			d.shell.ShowSFTP()
			d.transfer.EnsureStarted()
			d.transfer.OpenCenter()
		}, true
	case "open-local":
		return func(int) { _ = d.shell.NewTab() }, true
	case "broadcast":
		return func(int) { d.shell.ToggleBroadcastPanel() }, true
	case "command-palette":
		if d.search == nil {
			return nil, false
		}
		return func(int) { d.revealSearch(uisearch.ModeCommandPalette) }, true
	case "quick-switch":
		if d.search == nil {
			return nil, false
		}
		return func(int) { d.revealSearch(uisearch.ModeQuickSwitcher) }, true
	case "toggle-opacity":
		if d.toggleOpacity == nil {
			return nil, false
		}
		return func(int) { d.toggleOpacity() }, true
	default:
		return nil, false
	}
}

// revealSearch opens the overlay in the main window, which is the only
// window that renders it, and redraws that window. The Quick Switcher toggles
// on its chord; the command palette only opens.
func (d *bindingDispatcher) revealSearch(mode uisearch.Mode) {
	if d.search == nil {
		return
	}
	if mode == uisearch.ModeQuickSwitcher {
		d.search.Toggle(mode)
	} else {
		d.search.Open(mode)
	}
	if d.focusMain != nil {
		d.focusMain()
	}
	d.shell.RequestRedraw()
}

type chordKey struct {
	mods ui.Modifiers
	key  ui.Key
}

// terminalReservedChords is every key the terminal itself handles: the
// catalog's terminal defaults, the active terminal bindings, and MyGo's
// platform defaults. A global binding on one of these would shadow terminal
// copy or SIGINT, so the dispatcher skips it and lets the terminal win.
//
// The catalog default stays reserved even when an override moves or disables
// the binding: ReplaceBinding disables the displaced binding, and the freed
// chord still belongs to the terminal rather than to a window accelerator.
func terminalReservedChords(scheme domain.Scheme, overrides domain.Overrides) map[chordKey]bool {
	reserved := make(map[chordKey]bool)
	reserve := func(chord string) {
		if chord == "" || chord == domain.Disabled {
			return
		}
		parsed, ok := domain.ParseChord(chord)
		if !ok || parsed.Range || parsed.Arrows {
			return
		}
		if key, ok := mygoKey(parsed.Key); ok {
			reserved[chordKey{mods: chordMods(parsed), key: key}] = true
		}
	}
	for _, binding := range domain.Catalog() {
		if binding.Category != domain.CategoryTerminal {
			continue
		}
		reserve(domain.DefaultChord(binding, scheme))
		reserve(domain.EffectiveChord(binding, scheme, overrides))
	}
	for _, binding := range terminal.DefaultKeybinds() {
		reserved[chordKey{mods: binding.Mods, key: binding.Key}] = true
	}
	// The terminal always interprets these control chords itself: Ctrl+C is
	// SIGINT and Ctrl+V is the literal-next character, even though neither
	// appears in the catalog's terminal defaults.
	reserved[chordKey{mods: ui.Ctrl, key: ui.KeyC}] = true
	reserved[chordKey{mods: ui.Ctrl, key: ui.KeyV}] = true
	return reserved
}

// terminalKeybinds is the MyGo binding list for new terminals. It maps the
// active terminal bindings and clears a catalog default the user overrode or
// disabled, so the effective chord is the user's and not both.
func terminalKeybinds(service *appkeybindings.Service) []terminal.Keybind {
	if service == nil {
		return nil
	}
	scheme := service.Scheme()
	active := service.Active()
	binds := make([]terminal.Keybind, 0, len(active)+4)
	used := make(map[chordKey]bool)
	for _, candidate := range active {
		if candidate.Binding.Category != domain.CategoryTerminal ||
			!catalogBindingAvailable(candidate.Binding.ID) {
			continue
		}
		action, performable, ok := terminalAction(candidate.Binding.Action)
		if !ok {
			continue
		}
		if candidate.Parsed.Range || candidate.Parsed.Arrows {
			continue
		}
		key, ok := mygoKey(candidate.Parsed.Key)
		if !ok {
			continue
		}
		mods := chordMods(candidate.Parsed)
		used[chordKey{mods: mods, key: key}] = true
		binds = append(binds, terminal.Keybind{
			Mods: mods, Key: key, Action: action, Performable: performable,
		})
	}
	// Unbind catalog defaults that no active binding uses. Without this a
	// disabled copy binding would still copy through MyGo's default chord.
	for _, binding := range service.Catalog() {
		if binding.Category != domain.CategoryTerminal ||
			!catalogBindingAvailable(binding.ID) {
			continue
		}
		parsed, ok := domain.ParseChord(domain.DefaultChord(binding, scheme))
		if !ok || parsed.Range || parsed.Arrows {
			continue
		}
		key, ok := mygoKey(parsed.Key)
		if !ok {
			continue
		}
		mods := chordMods(parsed)
		if used[chordKey{mods: mods, key: key}] {
			continue
		}
		binds = append(binds, terminal.Keybind{Mods: mods, Key: key, Action: terminal.ActionNone})
	}
	return binds
}

func terminalAction(action string) (terminal.Action, bool, bool) {
	switch action {
	case "copy":
		return terminal.ActionCopy, true, true
	case "paste":
		return terminal.ActionPaste, true, true
	case "selectAll":
		return terminal.ActionSelectAll, false, true
	case "clearBuffer":
		return terminal.ActionClearScreen, true, true
	case "increaseTerminalFontSize":
		return terminal.ActionIncreaseFontSize, false, true
	case "decreaseTerminalFontSize":
		return terminal.ActionDecreaseFontSize, false, true
	case "resetTerminalFontSize":
		return terminal.ActionResetFontSize, false, true
	default:
		return terminal.ActionNone, false, false
	}
}

func chordMods(chord domain.Chord) ui.Modifiers {
	var mods ui.Modifiers
	if chord.Mods&domain.ModCtrl != 0 {
		mods |= ui.Ctrl
	}
	if chord.Mods&domain.ModAlt != 0 {
		mods |= ui.Alt
	}
	if chord.Mods&domain.ModShift != 0 {
		mods |= ui.Shift
	}
	if chord.Mods&domain.ModSuper != 0 {
		mods |= ui.Super
	}
	return mods
}

// mygoKey maps the catalog's canonical key name to MyGo's ui.Key.
func mygoKey(name string) (ui.Key, bool) {
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
	if name[0] == 'F' && len(name) <= 3 {
		var number int
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

// toggleOpacityTarget keeps a transparency toggle noticeable without ever
// hiding the window: the minimum is the preference domain's own floor.
func toggleOpacityTarget(current float64) float64 {
	if current < 1 {
		return 1
	}
	if domainpreferences.MinWindowOpacity > 0.85 {
		return domainpreferences.MinWindowOpacity
	}
	return 0.85
}
