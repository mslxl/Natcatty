package command

// Command IDs used by the application shell. New IDs belong here as the
// command surface grows, so menus, key bindings, and the future command
// palette name the same actions.
const (
	Quit     ID = "app.quit"
	LockApp  ID = "app.lock"
	NewTab   ID = "tab.new"
	CloseTab ID = "tab.close"

	CloseOtherTabs ID = "tab.closeOthers"
	CloseTabsRight ID = "tab.closeRight"
	DuplicateTab   ID = "tab.duplicate"
	DetachTab      ID = "tab.detach"

	SplitPaneRight ID = "pane.splitRight"
	SplitPaneDown  ID = "pane.splitDown"
	ClosePane      ID = "pane.close"
	FocusPaneLeft  ID = "pane.focusLeft"
	FocusPaneRight ID = "pane.focusRight"
	FocusPaneUp    ID = "pane.focusUp"
	FocusPaneDown  ID = "pane.focusDown"
)

// LocaleCommandID returns the ID of the command that shows the language of
// tag.
func LocaleCommandID(tag string) ID {
	return ID("locale.set." + tag)
}
