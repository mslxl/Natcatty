// Package keybindings owns the original application's keyboard shortcut
// catalog, the mac/PC/disabled schemes, user overrides, and chord conflict
// rules. It is pure: the UI and the app composition root translate the
// domain's chords into native key events.
package keybindings

// Scheme selects which column of the catalog is active. It is explicit
// rather than derived at every use, so a user on any platform can choose the
// mac or PC muscle memory the original offered.
type Scheme string

const (
	// SchemeDisabled turns every application binding off.
	SchemeDisabled Scheme = "disabled"
	// SchemeMac uses the original's Command/Ctrl/Option chords.
	SchemeMac Scheme = "mac"
	// SchemePC uses the original's Ctrl/Alt/Win chords.
	SchemePC Scheme = "pc"
)

// ParseScheme validates a stored or user-selected scheme.
func ParseScheme(raw string) (Scheme, bool) {
	switch Scheme(raw) {
	case SchemeDisabled, SchemeMac, SchemePC:
		return Scheme(raw), true
	default:
		return "", false
	}
}

// DefaultScheme is the original's platform default: mac on macOS and PC
// everywhere else.
func DefaultScheme(goos string) Scheme {
	if goos == "darwin" {
		return SchemeMac
	}
	return SchemePC
}

// Category groups the catalog the way the Settings window lists it.
type Category string

const (
	CategoryTabs       Category = "tabs"
	CategoryTerminal   Category = "terminal"
	CategoryNavigation Category = "navigation"
	CategoryApp        Category = "app"
	CategorySFTP       Category = "sftp"
)

// Categories returns the display order of the catalog's categories.
func Categories() []Category {
	return []Category{CategoryTabs, CategoryTerminal, CategoryNavigation, CategoryApp, CategorySFTP}
}

// Disabled is the catalog's sentinel for a binding that does not fire. It is
// part of the persisted contract, so it is never localized.
const Disabled = "Disabled"

// Binding is one original shortcut. Mac and PC hold the scheme's display
// notation, including special patterns such as "[1...9]" and "arrows". The
// label lives in the UI catalogs, as ADR-0009 requires.
type Binding struct {
	ID       string
	Action   string
	Mac      string
	PC       string
	Category Category
}

// catalog is the original DEFAULT_KEY_BINDINGS in its original order. IDs,
// actions, categories, and both scheme columns are a compatibility
// contract: muscle memory and synced overrides refer to them.
var catalog = []Binding{
	// Tab Management
	{ID: "switch-tab-1-9", Action: "switchToTab", Mac: "⌘ + [1...9]", PC: "Ctrl + [1...9]", Category: CategoryTabs},
	{ID: "next-tab", Action: "nextTab", Mac: "⌘ + Shift + ]", PC: "Ctrl + Tab", Category: CategoryTabs},
	{ID: "prev-tab", Action: "prevTab", Mac: "⌘ + Shift + [", PC: "Ctrl + Shift + Tab", Category: CategoryTabs},
	{ID: "close-tab", Action: "closeTab", Mac: "⌘ + W", PC: "Ctrl + W", Category: CategoryTabs},
	{ID: "close-session", Action: "closeSession", Mac: "⌘ + Shift + W", PC: "Ctrl + Shift + W", Category: CategoryTabs},
	{ID: "new-tab", Action: "newTab", Mac: "⌘ + T", PC: "Ctrl + T", Category: CategoryTabs},

	// Terminal Operations
	{ID: "copy", Action: "copy", Mac: "⌘ + C", PC: "Ctrl + Shift + C", Category: CategoryTerminal},
	{ID: "paste", Action: "paste", Mac: "⌘ + V", PC: "Ctrl + Shift + V", Category: CategoryTerminal},
	{ID: "paste-selection", Action: "pasteSelection", Mac: "⌘ + Shift + X", PC: "Ctrl + Shift + X", Category: CategoryTerminal},
	{ID: "select-all", Action: "selectAll", Mac: "⌘ + A", PC: "Ctrl + Shift + A", Category: CategoryTerminal},
	{ID: "clear-buffer", Action: "clearBuffer", Mac: "⌘ + ⌃ + K", PC: "Ctrl + Shift + K", Category: CategoryTerminal},
	{ID: "search-terminal", Action: "searchTerminal", Mac: "⌘ + F", PC: "Ctrl + F", Category: CategoryTerminal},
	{ID: "increase-terminal-font-size", Action: "increaseTerminalFontSize", Mac: "⌘ + =", PC: "Ctrl + =", Category: CategoryTerminal},
	{ID: "decrease-terminal-font-size", Action: "decreaseTerminalFontSize", Mac: "⌘ + -", PC: "Ctrl + -", Category: CategoryTerminal},
	{ID: "reset-terminal-font-size", Action: "resetTerminalFontSize", Mac: "⌘ + 0", PC: "Ctrl + 0", Category: CategoryTerminal},

	// Navigation / Split View
	{ID: "move-focus", Action: "moveFocus", Mac: "⌘ + ⌥ + arrows", PC: "Ctrl + Alt + arrows", Category: CategoryNavigation},
	{ID: "split-horizontal", Action: "splitHorizontal", Mac: "⌘ + D", PC: "Ctrl + Shift + D", Category: CategoryNavigation},
	{ID: "split-vertical", Action: "splitVertical", Mac: "⌘ + Shift + D", PC: "Ctrl + Shift + E", Category: CategoryNavigation},
	{ID: "toggle-pane-zoom", Action: "togglePaneZoom", Mac: "⌥ + M", PC: "Alt + M", Category: CategoryNavigation},

	// App Features
	{ID: "open-hosts", Action: "openHosts", Mac: Disabled, PC: Disabled, Category: CategoryApp},
	{ID: "open-local", Action: "openLocal", Mac: "⌘ + L", PC: Disabled, Category: CategoryApp},
	{ID: "open-sftp", Action: "openSftp", Mac: "⌘ + Shift + O", PC: "Ctrl + Shift + O", Category: CategoryApp},
	{ID: "port-forwarding", Action: "portForwarding", Mac: "⌘ + P", PC: "Ctrl + P", Category: CategoryApp},
	{ID: "command-palette", Action: "commandPalette", Mac: "⌘ + K", PC: "Ctrl + K", Category: CategoryApp},
	{ID: "quick-switch", Action: "quickSwitch", Mac: "⌘ + J", PC: "Ctrl + J", Category: CategoryApp},
	{ID: "new-workspace", Action: "newWorkspace", Mac: "⌘ + Shift + J", PC: "Ctrl + Shift + J", Category: CategoryApp},
	{ID: "snippets", Action: "snippets", Mac: "⌘ + Shift + S", PC: "Ctrl + Shift + S", Category: CategoryApp},
	{ID: "broadcast", Action: "broadcast", Mac: "⌘ + B", PC: "Ctrl + B", Category: CategoryApp},
	{ID: "toggle-side-panel", Action: "toggleSidePanel", Mac: "⌘ + \\", PC: "Ctrl + \\", Category: CategoryApp},
	{ID: "open-settings", Action: "openSettings", Mac: "⌘ + ,", PC: "Ctrl + ,", Category: CategoryApp},
	{ID: "toggle-opacity", Action: "toggleOpacity", Mac: "⌘ + U", PC: "Ctrl + Shift + U", Category: CategoryApp},

	// SFTP Operations
	{ID: "sftp-copy", Action: "sftpCopy", Mac: "⌘ + C", PC: "Ctrl + C", Category: CategorySFTP},
	{ID: "sftp-cut", Action: "sftpCut", Mac: "⌘ + X", PC: "Ctrl + X", Category: CategorySFTP},
	{ID: "sftp-paste", Action: "sftpPaste", Mac: "⌘ + V", PC: "Ctrl + V", Category: CategorySFTP},
	{ID: "sftp-select-all", Action: "sftpSelectAll", Mac: "⌘ + A", PC: "Ctrl + A", Category: CategorySFTP},
	{ID: "sftp-rename", Action: "sftpRename", Mac: "F2", PC: "F2", Category: CategorySFTP},
	{ID: "sftp-delete", Action: "sftpDelete", Mac: "⌘ + ⌫", PC: "Delete", Category: CategorySFTP},
	{ID: "sftp-refresh", Action: "sftpRefresh", Mac: "⌘ + R", PC: "F5", Category: CategorySFTP},
	{ID: "sftp-new-folder", Action: "sftpNewFolder", Mac: "⌘ + Shift + N", PC: "Ctrl + Shift + N", Category: CategorySFTP},
	{ID: "sftp-open", Action: "sftpOpen", Mac: "Enter", PC: "Enter", Category: CategorySFTP},
	{ID: "sftp-go-parent", Action: "sftpGoParent", Mac: "⌫", PC: "Backspace", Category: CategorySFTP},
	{ID: "sftp-navigate-to", Action: "sftpNavigateTo", Mac: "⌘ + Enter", PC: "Ctrl + Enter", Category: CategorySFTP},
}

// Catalog returns a copy of the original 42-binding catalog, in order.
func Catalog() []Binding {
	out := make([]Binding, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the binding with id.
func Lookup(id string) (Binding, bool) {
	for _, binding := range catalog {
		if binding.ID == id {
			return binding, true
		}
	}
	return Binding{}, false
}

// DefaultChord returns a binding's unmodified chord for scheme.
func DefaultChord(binding Binding, scheme Scheme) string {
	switch scheme {
	case SchemeMac:
		return binding.Mac
	case SchemePC:
		return binding.PC
	default:
		return Disabled
	}
}
