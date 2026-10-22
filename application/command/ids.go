package command

// Command IDs used by the application shell. New IDs belong here as the
// command surface grows, so menus, key bindings, and the future command
// palette name the same actions.
const (
	Quit    ID = "app.quit"
	LockApp ID = "app.lock"
	// OpenSettings shows the one Settings window.
	OpenSettings ID = "app.settings"
	NewTab       ID = "tab.new"
	CloseTab     ID = "tab.close"

	CloseOtherTabs ID = "tab.closeOthers"
	CloseTabsRight ID = "tab.closeRight"
	DuplicateTab   ID = "tab.duplicate"
	DetachTab      ID = "tab.detach"
	// CopyTabToNewWindow opens a detached window holding a copy of the active
	// Tab; the source Tab stays where it is.
	CopyTabToNewWindow ID = "tab.copyToWindow"

	SplitPaneRight ID = "pane.splitRight"
	SplitPaneDown  ID = "pane.splitDown"
	ClosePane      ID = "pane.close"
	// DetachPane moves the focused Pane's Session into its own detached
	// window.
	DetachPane     ID = "pane.detach"
	FocusPaneLeft  ID = "pane.focusLeft"
	FocusPaneRight ID = "pane.focusRight"
	FocusPaneUp    ID = "pane.focusUp"
	FocusPaneDown  ID = "pane.focusDown"

	// OpenBackups shows the Vault's local backup panel.
	OpenBackups ID = "vault.backups"
)

// LocaleCommandID returns the ID of the command that shows the language of
// tag.
func LocaleCommandID(tag string) ID {
	return ID("locale.set." + tag)
}
