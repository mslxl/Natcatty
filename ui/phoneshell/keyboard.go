package phoneshell

// The phone's external-keyboard dispatcher. A hardware keyboard on a phone
// should behave like the desktop's: the shared binding document decides which
// application action a chord runs, and every chord it does not bind falls
// through to the terminal unchanged. The dispatcher is installed on the
// terminal screen, which is where an external keyboard is used, so it runs in
// the terminal's own input path rather than as a window shortcut that would
// fire before the focused terminal could be consulted.
//
// Two rules order it:
//
//   - Reserved keys win. The terminal category of the catalog, MyGo's own
//     terminal defaults, and the control characters a terminal always owns
//     (Ctrl+C, Ctrl+V, Ctrl+A) are never dispatched as application actions,
//     even when the user rebinds an application action onto one of them. The
//     desktop applies the same rule before it registers its accelerators.
//   - Only actions with a phone surface are dispatched. Quake, the tray, window
//     transparency, detached desktop windows, and the desktop-only surfaces
//     (SFTP, broadcast, the side panel) are skipped, so a chord never claims a
//     key without doing anything on this device.
//
// Android's keyboard bridge adds two quirks the mapping handles explicitly:
// a Ctrl or Meta combination may arrive with the key unknown and the text
// U+0000, and an Alt combination arrives as the composed text (Alt+C is "ç").
// A chord is therefore matched by the physical key position where the platform
// reports one, while the composed text is typed when no binding takes the key.
// The U+0000 artifact is never typed.

import (
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/egoist/mygo/ui"
)

// WithKeyboardShortcuts gives the phone's terminals the external-keyboard
// dispatcher built from the same binding document the Shortcuts page edits.
// A Shell without it still accepts the terminal's own key table, but no
// application binding is dispatched.
func WithKeyboardShortcuts(service ShortcutService) Option {
	return func(s *Shell) {
		s.installKeyboardShortcuts(service)
	}
}

// installKeyboardShortcuts records the shared binding document and the Shell
// transitions the catalog's application actions need. It is one call so the
// composition root's existing WithShortcuts can adopt it with a single line.
func (s *Shell) installKeyboardShortcuts(service ShortcutService) {
	if s == nil || s.sessions == nil {
		return
	}
	s.sessions.shortcuts = service
	s.sessions.openHosts = func() { s.Select(Hosts) }
	s.sessions.openSettings = func() { s.Select(Settings) }
	s.sessions.openPanePicker = func(tabID string) {
		s.Select(Sessions)
		s.sections[Sessions].router.Push("/session/" + tabID + "/new-pane")
	}
}

// phoneShortcutActions are the phone behaviors a catalog binding can run. A
// nil field leaves that binding undispatched, exactly as a phone without the
// surface would.
type phoneShortcutActions struct {
	// SwitchTab selects a numbered Tab, and NextTab/PrevTab move by one.
	SwitchTab func(number int)
	NextTab   func()
	PrevTab   func()
	NewTab    func()
	CloseTab  func()
	ClosePane func()
	// MoveFocus takes the arrow key that named the direction.
	MoveFocus func(key ui.Key)
	// SplitPane adds a Pane to the visible Tab; vertical is the catalog's
	// split-vertical action.
	SplitPane    func(vertical bool)
	OpenHosts    func()
	OpenSettings func()
	History      func()
}

// phoneShortcut is one application binding installed on the terminal.
type phoneShortcut struct {
	mods ui.Modifiers
	key  ui.Key
	run  func()
}

// phoneKeyboard dispatches application bindings from the terminal's input.
// The zero value dispatches nothing.
type phoneKeyboard struct {
	bindings []phoneShortcut
	reserved map[phoneChordKey]bool
}

// run executes the binding for a chord and reports whether it took the key. A
// reserved chord is never taken, even when a binding names it, so control
// characters keep reaching the terminal.
func (k *phoneKeyboard) run(mods ui.Modifiers, key ui.Key) bool {
	if k == nil || key == ui.KeyUnknown {
		return false
	}
	chord := phoneChordKey{mods: mods, key: key}
	if k.reserved[chord] {
		return false
	}
	for _, binding := range k.bindings {
		if binding.mods == mods && binding.key == key {
			binding.run()
			return true
		}
	}
	return false
}

// add installs one chord unless it is reserved. Reserved filtering here keeps
// the table honest even before run's own check.
func (k *phoneKeyboard) add(mods ui.Modifiers, key ui.Key, run func()) {
	if key == ui.KeyUnknown || run == nil {
		return
	}
	if k.reserved[phoneChordKey{mods: mods, key: key}] {
		return
	}
	k.bindings = append(k.bindings, phoneShortcut{mods: mods, key: key, run: run})
}

// phoneKeyboardFor builds the dispatcher from the active binding document.
// Terminal-category bindings are left to the terminal's own key table (which
// the same document fills); application bindings are installed only when a
// phone action exists for them.
func phoneKeyboardFor(service ShortcutService, actions phoneShortcutActions) *phoneKeyboard {
	keyboard := &phoneKeyboard{reserved: phoneReservedChords(service)}
	if service == nil {
		return keyboard
	}
	for _, candidate := range service.Active() {
		binding := candidate.Binding
		if binding.Category == domainkeybindings.CategoryTerminal ||
			binding.Category == domainkeybindings.CategorySFTP {
			continue
		}
		if phoneUnavailableActions[binding.ID] {
			continue
		}
		action, ok := phoneShortcutAction(binding.ID, actions)
		if !ok {
			continue
		}
		parsed := candidate.Parsed
		mods := phoneMods(parsed)
		switch {
		case parsed.Range:
			for number := 1; number <= 9; number++ {
				number := number
				keyboard.add(mods, ui.Key0+ui.Key(number), func() { action(number, 0) })
			}
		case parsed.Arrows:
			for _, key := range []ui.Key{ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown} {
				key := key
				keyboard.add(mods, key, func() { action(0, key) })
			}
		default:
			key, ok := phoneKey(parsed.Key)
			if !ok {
				continue
			}
			keyboard.add(mods, key, func() { action(0, 0) })
		}
	}
	return keyboard
}

// phoneShortcutAction maps a catalog ID to the phone action it runs. The
// binding's number and arrow key are only meaningful to the pattern actions.
func phoneShortcutAction(id string, actions phoneShortcutActions) (func(number int, key ui.Key), bool) {
	switch id {
	case "switch-tab-1-9":
		if actions.SwitchTab == nil {
			return nil, false
		}
		return func(number int, _ ui.Key) { actions.SwitchTab(number) }, true
	case "next-tab":
		if actions.NextTab == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.NextTab() }, true
	case "prev-tab":
		if actions.PrevTab == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.PrevTab() }, true
	case "new-tab":
		if actions.NewTab == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.NewTab() }, true
	case "close-tab":
		if actions.CloseTab == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.CloseTab() }, true
	case "close-session":
		if actions.ClosePane == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.ClosePane() }, true
	case "move-focus":
		if actions.MoveFocus == nil {
			return nil, false
		}
		return func(_ int, key ui.Key) { actions.MoveFocus(key) }, true
	case "split-horizontal":
		if actions.SplitPane == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.SplitPane(false) }, true
	case "split-vertical":
		if actions.SplitPane == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.SplitPane(true) }, true
	case "open-hosts":
		if actions.OpenHosts == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.OpenHosts() }, true
	case "open-settings":
		if actions.OpenSettings == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.OpenSettings() }, true
	case "history-search":
		if actions.History == nil {
			return nil, false
		}
		return func(int, ui.Key) { actions.History() }, true
	default:
		return nil, false
	}
}

// phoneUnavailableActions are catalog bindings whose desktop action has no
// phone surface. They are skipped rather than bound to a no-op, so their keys
// go on to the terminal unchanged.
var phoneUnavailableActions = map[string]bool{
	// Desktop-only window model: a local PTY, a detached Workspace window,
	// and window transparency.
	"open-local":     true,
	"new-workspace":  true,
	"toggle-opacity": true,
	// Desktop surfaces this build does not render. The phone reaches Files,
	// forwarding, snippets and history through entries rather than the
	// desktop's panels and overlays, and has no broadcast or command palette.
	"open-sftp":         true,
	"transfer-center":   true,
	"port-forwarding":   true,
	"snippets":          true,
	"broadcast":         true,
	"toggle-side-panel": true,
	"toggle-host-list":  true,
	"toggle-pane-zoom":  true,
	"command-palette":   true,
	"quick-switch":      true,
}

// phoneReservedChords is every chord the focused terminal owns. It mirrors the
// desktop's rule: the terminal category's defaults and active bindings, MyGo's
// own terminal defaults, and the control characters a terminal always reads
// (Ctrl+C interrupts, Ctrl+V is literal-next, and Ctrl+A moves to the line's
// start). A catalog default stays reserved even when moved or disabled, so the
// freed chord belongs to the terminal rather than to an application action.
func phoneReservedChords(service ShortcutService) map[phoneChordKey]bool {
	reserved := make(map[phoneChordKey]bool)
	reserve := func(chord string) {
		if chord == "" || chord == domainkeybindings.Disabled {
			return
		}
		parsed, ok := domainkeybindings.ParseChord(chord)
		if !ok || parsed.Range || parsed.Arrows {
			return
		}
		key, ok := phoneKey(parsed.Key)
		if !ok {
			return
		}
		reserved[phoneChordKey{mods: phoneMods(parsed), key: key}] = true
	}
	if service != nil {
		scheme := service.Scheme()
		for _, binding := range service.Catalog() {
			if binding.Category != domainkeybindings.CategoryTerminal {
				continue
			}
			reserve(domainkeybindings.DefaultChord(binding, scheme))
		}
		for _, candidate := range service.Active() {
			if candidate.Binding.Category != domainkeybindings.CategoryTerminal {
				continue
			}
			if candidate.Parsed.Range || candidate.Parsed.Arrows {
				continue
			}
			key, ok := phoneKey(candidate.Parsed.Key)
			if !ok {
				continue
			}
			reserved[phoneChordKey{mods: phoneMods(candidate.Parsed), key: key}] = true
		}
	}
	for _, binding := range terminal.DefaultKeybinds() {
		reserved[phoneChordKey{mods: binding.Mods, key: binding.Key}] = true
	}
	for _, key := range []ui.Key{ui.KeyC, ui.KeyV, ui.KeyA} {
		reserved[phoneChordKey{mods: ui.Ctrl, key: key}] = true
	}
	return reserved
}

// phoneKeyInput normalizes one key event to the chord a binding matches and
// the text the terminal would type if the key is left to it. Android's bridge
// is why the two are separate:
//
//   - A Ctrl or Meta combination can arrive with the key unknown and the text
//     U+0000. The physical key position names it, and the U+0000 artifact is
//     discarded rather than typed.
//   - An Alt combination arrives as the composed text (Alt+C is "ç"). The
//     physical key still names the chord for the binding table, while the
//     composed text stays what the terminal types when no binding takes it,
//     matching the desktop.
//
// ok is false when the event names no key at all, as a bare modifier or a
// clipboard-only event.
func phoneKeyInput(ev ui.InputEvent) (mods ui.Modifiers, key ui.Key, text string, ok bool) {
	mods = ev.Mods
	text = ev.Text
	if text == "\u0000" {
		// U+0000 is Android's modifier artifact, not text the user typed.
		text = ""
	}
	key = ev.Key
	if key == ui.KeyUnknown {
		// A chord is matched by position where the platform reports one:
		// that is what a shortcut means on a layout that types another
		// character, and what survives Android's composed Alt text.
		if ev.PhysicalKey != ui.KeyUnknown && mods&(ui.Ctrl|ui.Alt|ui.Super) != 0 {
			key = ev.PhysicalKey
		} else if fromText, named := phoneKeyFromText(text); named {
			key = fromText
		} else if ev.PhysicalKey != ui.KeyUnknown {
			key = ev.PhysicalKey
		}
	}
	if key == ui.KeyUnknown {
		return mods, ui.KeyUnknown, text, false
	}
	return mods, key, text, true
}

// phoneKeyFromText names the key a single typed rune stands for. It is the
// fallback for a platform that reports no key name, and it deliberately does
// not map a composed character such as "ç" to C: that text belongs to the
// terminal, not to an application shortcut.
func phoneKeyFromText(text string) (ui.Key, bool) {
	runes := []rune(text)
	if len(runes) != 1 {
		return ui.KeyUnknown, false
	}
	return phoneKey(string(runes[0]))
}

// phoneTypesText reports whether a key-down's composed text is what the
// terminal should type, because the key's own encoding does not type it:
//
//   - A punctuation or space key: the Pane encodes neither, and Android's
//     keyNames entry suppresses the text fallback that used to carry them.
//   - A key with no name, as a layout's composed character.
//   - A letter or digit whose platform text is not the character the key
//     types, as Android's Alt-composed "ç" for C. Plain typing keeps the
//     platform's text path, where an input method may still compose it.
//
// A Ctrl or Super chord is the terminal's control character, and a key with
// meaning of its own (Enter, Tab, an arrow) is encoded by the terminal rather
// than typed, so neither sends its text here.
func phoneTypesText(mods ui.Modifiers, key ui.Key, text string) bool {
	if text == "" || mods&(ui.Ctrl|ui.Super) != 0 {
		return false
	}
	switch key {
	case ui.KeySpace, ui.KeyMinus, ui.KeyEqual, ui.KeyComma, ui.KeyPeriod, ui.KeySlash,
		ui.KeySemicolon, ui.KeyQuote, ui.KeyBracketLeft, ui.KeyBracketRight,
		ui.KeyBackslash, ui.KeyBackquote:
		return true
	case ui.KeyUnknown:
		return true
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		want := '0' + rune(key-ui.Key0)
		if mods&ui.Shift != 0 {
			want = rune(")!@#$%^&*("[key-ui.Key0])
		}
		// The platform's text path types the key's own character; only a
		// composition that replaced it is the terminal's to type here.
		return text != string(want)
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		want := 'a' + rune(key-ui.KeyA)
		if mods&ui.Shift != 0 {
			want -= 'a' - 'A'
		}
		return text != string(want)
	}
	return false
}
