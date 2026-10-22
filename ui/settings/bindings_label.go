package settings

import (
	domain "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/locales"
)

// catalogBindings returns the original 42-binding catalog.
func catalogBindings() []domain.Binding { return domain.Catalog() }

// bindingLabel localizes a binding. Every catalog binding has a message, so
// no display string lives in the domain (ADR-0009).
func bindingLabel(m locales.Messages, binding domain.Binding) string {
	switch binding.ID {
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
	default:
		return ""
	}
}

// categoryLabel localizes a catalog category.
func categoryLabel(m locales.Messages, category domain.Category) string {
	switch category {
	case domain.CategoryTabs:
		return m.SettingsShortcutCategoryTabs()
	case domain.CategoryTerminal:
		return m.SettingsShortcutCategoryTerminal()
	case domain.CategoryNavigation:
		return m.SettingsShortcutCategoryNavigation()
	case domain.CategoryApp:
		return m.SettingsShortcutCategoryApp()
	case domain.CategorySFTP:
		return m.SettingsShortcutCategorySFTP()
	default:
		return ""
	}
}

// bindingField returns the stable reset identity of a binding row. Preference
// fields own typed groups; shortcut rows are addressed by their binding ID.
func bindingField(id string) string { return "binding." + id }
