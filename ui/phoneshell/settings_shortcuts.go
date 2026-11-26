package phoneshell

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/terminal"
)

// ShortcutService is the shared keyboard-binding document the phone Shortcuts
// page edits. application/keybindings.Service implements it; the page writes
// the same catalog IDs, scheme, and validated chords the desktop Settings
// window writes, so an external keyboard behaves the same on both platforms.
//
// The active scheme is device-local (the platform's default differs), while
// the overrides are a separate document the phone treats as locally authored:
// its Vault sync leaves keybindings to the desktop, as the spec requires, so a
// phone edit never overwrites the desktop's choices.
type ShortcutService interface {
	Scheme() domainkeybindings.Scheme
	Catalog() []domainkeybindings.Binding
	Active() []domainkeybindings.Active
	Chord(id string, scheme domainkeybindings.Scheme) string
	IsOverridden(id string, scheme domainkeybindings.Scheme) bool
	SetScheme(domainkeybindings.Scheme) error
	SetBinding(id string, scheme domainkeybindings.Scheme, chord string, menus []domainkeybindings.MenuShortcut) ([]domainkeybindings.Conflict, error)
	ResetBinding(id string, scheme domainkeybindings.Scheme) error
}

// WithShortcuts gives the Settings pages the shared keyboard-binding service.
// Without it the Shortcuts page states that shortcuts are unavailable instead
// of writing a document nobody reads. The same service supplies the terminal
// key table a new phone terminal captures, so an edit reaches the Runtime
// without a second wiring step.
func WithShortcuts(service ShortcutService) Option {
	return func(s *Shell) {
		s.settings.shortcuts = service
		// The same document drives the external-keyboard dispatcher.
		s.installKeyboardShortcuts(service)
		s.terminalKeybinds = func() []terminal.Keybind {
			return phoneTerminalKeybinds(service)
		}
	}
}

// terminalKeybindsFunc supplies the bindings a newly built phone terminal
// captures.
type terminalKeybindsFunc func() []terminal.Keybind

// phoneTerminalKeybinds maps the active catalog onto the terminal's key table,
// as the desktop composition root does. Only the terminal category is mapped:
// every other binding is an application action dispatched by the
// external-keyboard handler (see keyboard.go), and must not swallow a key the
// terminal needs through MyGo's own table. The catalog's terminal
// defaults are explicitly unbound when the user moved or disabled them, so the
// old chord does not keep firing through MyGo's own defaults.
func phoneTerminalKeybinds(service ShortcutService) []terminal.Keybind {
	if service == nil {
		return nil
	}
	scheme := service.Scheme()
	binds := make([]terminal.Keybind, 0, len(service.Catalog()))
	used := make(map[phoneChordKey]bool)
	for _, candidate := range service.Active() {
		if candidate.Binding.Category != domainkeybindings.CategoryTerminal {
			continue
		}
		action, performable, ok := phoneTerminalAction(candidate.Binding.Action)
		if !ok || candidate.Parsed.Range || candidate.Parsed.Arrows {
			continue
		}
		key, ok := phoneKey(candidate.Parsed.Key)
		if !ok {
			continue
		}
		mods := phoneMods(candidate.Parsed)
		used[phoneChordKey{mods: mods, key: key}] = true
		binds = append(binds, terminal.Keybind{
			Mods: mods, Key: key, Action: action, Performable: performable,
		})
	}
	for _, binding := range service.Catalog() {
		if binding.Category != domainkeybindings.CategoryTerminal {
			continue
		}
		parsed, ok := domainkeybindings.ParseChord(
			domainkeybindings.DefaultChord(binding, scheme))
		if !ok || parsed.Range || parsed.Arrows {
			continue
		}
		key, ok := phoneKey(parsed.Key)
		if !ok {
			continue
		}
		mods := phoneMods(parsed)
		if used[phoneChordKey{mods: mods, key: key}] {
			continue
		}
		binds = append(binds, terminal.Keybind{Mods: mods, Key: key, Action: terminal.ActionNone})
	}
	return binds
}

type phoneChordKey struct {
	mods ui.Modifiers
	key  ui.Key
}

func phoneTerminalAction(action string) (terminal.Action, bool, bool) {
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

func phoneMods(chord domainkeybindings.Chord) ui.Modifiers {
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

// phoneKey maps the catalog's canonical key name to MyGo's ui.Key.
func phoneKey(name string) (ui.Key, bool) {
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
	if len(name) >= 2 && name[0] == 'F' && len(name) <= 3 {
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

// shortcutsPage draws the external-keyboard binding editor. The catalog comes
// from the shared domain package and the labels from the shared locale
// catalogs, so the phone does not carry its own copy of either. A chord is
// either recorded from an external keyboard or typed in the catalog notation;
// the service validates it before persisting, and a rejected value leaves the
// document untouched.
func (p *settingsPages) shortcutsPage(c *ui.Context) {
	service := p.shortcuts
	if service == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			"Keyboard shortcuts are not available on this device.", nil)
		return
	}
	m := locales.In(c.Locale())
	scheme := service.Scheme()
	p.choices(c, m.SettingsShortcutScheme(), []choice{
		pick(m.SettingsSchemeMac(), scheme == domainkeybindings.SchemeMac, func() {
			p.setShortcutScheme(service, domainkeybindings.SchemeMac)
		}),
		pick(m.SettingsSchemePC(), scheme == domainkeybindings.SchemePC, func() {
			p.setShortcutScheme(service, domainkeybindings.SchemePC)
		}),
		pick(m.SettingsSchemeDisabled(), scheme == domainkeybindings.SchemeDisabled, func() {
			p.setShortcutScheme(service, domainkeybindings.SchemeDisabled)
		}),
	})
	kit.EmptyHint(c, m.SettingsShortcutSchemeDescription())
	kit.EmptyHint(c, m.SettingsShortcutTerminalNote())
	if scheme == domainkeybindings.SchemeDisabled {
		kit.EmptyHint(c, "Choose Mac or PC to edit shortcuts.")
		return
	}

	p.shortcutEditor(c, service, m)
	kit.EmptyHint(c, m.SettingsShortcutRecordHint())

	for _, category := range domainkeybindings.Categories() {
		bindings := make([]domainkeybindings.Binding, 0)
		for _, binding := range service.Catalog() {
			if binding.Category == category {
				bindings = append(bindings, binding)
			}
		}
		if len(bindings) == 0 {
			continue
		}
		ui.Text(c, shortcutCategoryLabel(m, category)).FontSize(13).
			FontWeight(600).TextColor(c.Theme().Text)
		for _, binding := range bindings {
			p.shortcutRow(c, service, binding, m, scheme)
		}
	}
}

// shortcutRow is one binding: its localized label, the chord that currently
// fires, and a Reset control once the user overrides the catalog default.
// Tapping the row opens the editor for that binding.
func (p *settingsPages) shortcutRow(c *ui.Context, service ShortcutService,
	binding domainkeybindings.Binding, m locales.Messages, scheme domainkeybindings.Scheme) {

	t := c.Theme()
	label := shortcutBindingLabel(m, binding.ID)
	chord := service.Chord(binding.ID, scheme)
	shown := chord
	if chord == domainkeybindings.Disabled {
		shown = m.SettingsShortcutDisabled()
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		row := ui.ButtonBase(c).Grow(1).MinWidth(0).Padding(12, 12).Gap(12).
			AlignItems(ui.Center).Background(t.Surface).Radius(12).
			Label(label + " shortcut")
		row.Children(func() {
			ui.Text(c, label).Grow(1).MinWidth(0).TextColor(t.Text)
			ui.Text(c, shown).FontSize(13).TextColor(t.TextMuted)
		})
		if row.Clicked() {
			// Tapping a binding is the record gesture the hint describes:
			// the next chord press replaces it.
			p.shortcutSelected = binding.ID
			p.recording = true
			p.shortcutChord = ""
		}
		if service.IsOverridden(binding.ID, scheme) {
			if kit.GhostButton(c, "", m.SettingsShortcutReset()).
				Label("Reset " + label).Clicked() {
				if err := service.ResetBinding(binding.ID, scheme); err != nil {
					p.problem = "Could not reset the shortcut."
					return
				}
				p.problem = ""
				if p.shortcutSelected == binding.ID {
					p.shortcutChord = service.Chord(binding.ID, scheme)
				}
			}
		}
	})
}

// shortcutEditor edits the selected binding. Recording captures the next key
// press from an external keyboard; the text field accepts the catalog notation
// for a touch keyboard. Both paths end in applyShortcutChord, which validates
// through the shared service before anything is written.
func (p *settingsPages) shortcutEditor(c *ui.Context, service ShortcutService,
	m locales.Messages) {

	if p.shortcutSelected == "" {
		return
	}
	binding, ok := domainkeybindings.Lookup(p.shortcutSelected)
	if !ok {
		p.shortcutSelected = ""
		return
	}
	t := c.Theme()
	label := shortcutBindingLabel(m, binding.ID)
	ui.Column(c).FillWidth().Padding(12).Gap(8).Background(t.Surface).
		Radius(12).Children(func() {
		ui.Text(c, "Edit "+label).FontWeight(600).TextColor(t.Text)
		if p.recording {
			shown := m.SettingsShortcutRecording()
			field := ui.TextInput(c, &shown).ReadOnly(true).Label("Shortcut chord")
			field = field.Focus()
			field.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape && k.Mods == 0 {
					p.recording = false
					return true
				}
				if k.Key == ui.KeyBackspace && k.Mods == 0 {
					p.recording = false
					p.shortcutChord = ""
					p.applyShortcutChord(service, binding.ID, domainkeybindings.Disabled)
					return true
				}
				chord, ok := phoneRecordedChord(k.Key, k.Mods, service.Scheme())
				if !ok {
					p.problem = "That key cannot be used as a shortcut. Add Ctrl, Alt, or Shift, or choose a function key."
					return true
				}
				p.recording = false
				p.shortcutChord = chord
				p.applyShortcutChord(service, binding.ID, chord)
				return true
			})
		} else {
			ui.TextInput(c, &p.shortcutChord).Label("Shortcut chord").
				Placeholder("e.g. Ctrl+Shift+C").
				OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEscape && k.Mods == 0 {
						p.shortcutSelected = ""
						return true
					}
					return false
				})
		}
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			if kit.SecondaryButton(c, "", "Record shortcut").Clicked() {
				p.recording = true
				p.problem = ""
			}
			if kit.SecondaryButton(c, "", "Type chord").Clicked() {
				p.recording = false
				p.problem = ""
			}
			if kit.PrimaryButton(c, "", "Save chord").Clicked() {
				p.applyShortcutChord(service, binding.ID, p.shortcutChord)
			}
			if kit.GhostButton(c, "", "Close").Clicked() {
				p.shortcutSelected = ""
				p.recording = false
				p.problem = ""
			}
		})
	})
}

// setShortcutScheme switches the active scheme. The service persists the
// choice locally; a selection in progress belongs to the old scheme and is
// discarded.
func (p *settingsPages) setShortcutScheme(service ShortcutService, scheme domainkeybindings.Scheme) {
	if err := service.SetScheme(scheme); err != nil {
		p.problem = "Could not save the shortcut scheme."
		return
	}
	p.problem = ""
	p.recording = false
	p.shortcutSelected = ""
}

// applyShortcutChord validates and saves one override. SetBinding checks the
// binding ID, the scheme, and the chord, and reports a conflict without
// writing; a failure leaves the previous binding intact, so a rejected value
// can never half-save.
func (p *settingsPages) applyShortcutChord(service ShortcutService, id, chord string) {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		p.problem = "Type or record a shortcut first."
		return
	}
	conflicts, err := service.SetBinding(id, service.Scheme(), chord, nil)
	if err != nil {
		if errors.Is(err, appkeybindings.ErrInvalidChord) {
			p.problem = strconv.Quote(chord) + " is not a valid shortcut. Use a key with Ctrl, Alt, Shift, or a function key."
		} else {
			p.problem = "Could not save the shortcut."
		}
		return
	}
	if len(conflicts) > 0 {
		p.problem = strconv.Quote(chord) + " is already used by another shortcut."
		return
	}
	p.problem = ""
}

// phoneRecordedChord turns a key press into the catalog's chord notation. It
// reports false for a bare modifier or an unmappable key, which the editor
// states rather than saving.
func phoneRecordedChord(key ui.Key, mods ui.Modifiers, scheme domainkeybindings.Scheme) (string, bool) {
	name, ok := phoneRecordedKeyName(key)
	if !ok {
		return "", false
	}
	var chord domainkeybindings.Chord
	if mods&ui.Ctrl != 0 {
		chord.Mods |= domainkeybindings.ModCtrl
	}
	if mods&ui.Alt != 0 {
		chord.Mods |= domainkeybindings.ModAlt
	}
	if mods&ui.Shift != 0 {
		chord.Mods |= domainkeybindings.ModShift
	}
	if mods&ui.Super != 0 {
		chord.Mods |= domainkeybindings.ModSuper
	}
	chord.Key = name
	rendered := chord.String(scheme)
	if _, ok := domainkeybindings.ParseChord(rendered); !ok {
		return "", false
	}
	return rendered, true
}

func phoneRecordedKeyName(key ui.Key) (string, bool) {
	switch key {
	case ui.KeyEnter:
		return "Enter", true
	case ui.KeyEscape:
		return "Esc", true
	case ui.KeyBackspace:
		return "Backspace", true
	case ui.KeyTab:
		return "Tab", true
	case ui.KeySpace:
		return "Space", true
	case ui.KeyDelete:
		return "Delete", true
	case ui.KeyInsert:
		return "Insert", true
	case ui.KeyHome:
		return "Home", true
	case ui.KeyEnd:
		return "End", true
	case ui.KeyPageUp:
		return "PageUp", true
	case ui.KeyPageDown:
		return "PageDown", true
	case ui.KeyLeft:
		return "Left", true
	case ui.KeyRight:
		return "Right", true
	case ui.KeyUp:
		return "Up", true
	case ui.KeyDown:
		return "Down", true
	case ui.KeyMinus:
		return "-", true
	case ui.KeyEqual:
		return "=", true
	case ui.KeyComma:
		return ",", true
	case ui.KeyPeriod:
		return ".", true
	case ui.KeySlash:
		return "/", true
	case ui.KeySemicolon:
		return ";", true
	case ui.KeyQuote:
		return "'", true
	case ui.KeyBracketLeft:
		return "[", true
	case ui.KeyBracketRight:
		return "]", true
	case ui.KeyBackslash:
		return "\\", true
	case ui.KeyBackquote:
		return "`", true
	}
	if key >= ui.KeyF1 && key <= ui.KeyF12 {
		return "F" + strconv.Itoa(int(key-ui.KeyF1)+1), true
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		return string(rune('A' + int(key-ui.KeyA))), true
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		return string(rune('0' + int(key-ui.Key0))), true
	}
	return "", false
}

// shortcutCategoryLabel localizes a catalog category with the shared locale
// message catalog, as the desktop Settings window does.
func shortcutCategoryLabel(m locales.Messages, category domainkeybindings.Category) string {
	switch category {
	case domainkeybindings.CategoryTabs:
		return m.SettingsShortcutCategoryTabs()
	case domainkeybindings.CategoryTerminal:
		return m.SettingsShortcutCategoryTerminal()
	case domainkeybindings.CategoryNavigation:
		return m.SettingsShortcutCategoryNavigation()
	case domainkeybindings.CategoryApp:
		return m.SettingsShortcutCategoryApp()
	case domainkeybindings.CategorySFTP:
		return m.SettingsShortcutCategorySFTP()
	default:
		return ""
	}
}

// shortcutBindingLabel localizes a catalog binding. The strings live in the
// shared locale catalog rather than in the domain, per the localization ADR.
func shortcutBindingLabel(m locales.Messages, id string) string {
	switch id {
	case "switch-tab-1-9":
		return m.SettingsBindingSwitchTab1to9()
	case "next-tab":
		return m.SettingsBindingNextTab()
	case "prev-tab":
		return m.SettingsBindingPrevTab()
	case "close-tab":
		return m.SettingsBindingCloseTab()
	case "close-session":
		return m.SettingsBindingCloseSession()
	case "new-tab":
		return m.SettingsBindingNewTab()
	case "copy":
		return m.SettingsBindingCopy()
	case "paste":
		return m.SettingsBindingPaste()
	case "paste-selection":
		return m.SettingsBindingPasteSelection()
	case "select-all":
		return m.SettingsBindingSelectAll()
	case "clear-buffer":
		return m.SettingsBindingClearBuffer()
	case "search-terminal":
		return m.SettingsBindingSearchTerminal()
	case "increase-terminal-font-size":
		return m.SettingsBindingIncreaseFont()
	case "decrease-terminal-font-size":
		return m.SettingsBindingDecreaseFont()
	case "reset-terminal-font-size":
		return m.SettingsBindingResetFont()
	case "move-focus":
		return m.SettingsBindingMoveFocus()
	case "split-horizontal":
		return m.SettingsBindingSplitHorizontal()
	case "split-vertical":
		return m.SettingsBindingSplitVertical()
	case "toggle-pane-zoom":
		return m.SettingsBindingTogglePaneZoom()
	case "open-hosts":
		return m.SettingsBindingOpenHosts()
	case "open-local":
		return m.SettingsBindingOpenLocal()
	case "open-sftp":
		return m.SettingsBindingOpenSFTP()
	case "port-forwarding":
		return m.SettingsBindingPortForwarding()
	case "command-palette":
		return m.SettingsBindingCommandPalette()
	case "quick-switch":
		return m.SettingsBindingQuickSwitch()
	case "new-workspace":
		return m.SettingsBindingNewWorkspace()
	case "snippets":
		return m.SettingsBindingSnippets()
	case "broadcast":
		return m.SettingsBindingBroadcast()
	case "toggle-side-panel":
		return m.SettingsBindingToggleSidePanel()
	case "toggle-host-list":
		return m.SettingsBindingToggleHostList()
	case "open-settings":
		return m.SettingsBindingOpenSettings()
	case "toggle-opacity":
		return m.SettingsBindingToggleOpacity()
	case "sftp-copy":
		return m.SettingsBindingSFTPCopy()
	case "sftp-cut":
		return m.SettingsBindingSFTPCut()
	case "sftp-paste":
		return m.SettingsBindingSFTPPaste()
	case "sftp-select-all":
		return m.SettingsBindingSFTPSelectAll()
	case "sftp-rename":
		return m.SettingsBindingSFTPRename()
	case "sftp-delete":
		return m.SettingsBindingSFTPDelete()
	case "sftp-refresh":
		return m.SettingsBindingSFTPRefresh()
	case "sftp-new-folder":
		return m.SettingsBindingSFTPNewFolder()
	case "sftp-open":
		return m.SettingsBindingSFTPOpen()
	case "sftp-go-parent":
		return m.SettingsBindingSFTPGoParent()
	case "sftp-navigate-to":
		return m.SettingsBindingSFTPNavigateTo()
	case "transfer-center":
		return m.SettingsBindingTransferCenter()
	case "history-search":
		return m.SettingsBindingHistorySearch()
	default:
		return id
	}
}
