package command

// Command IDs used by the application shell. New IDs belong here as the
// command surface grows, so menus, key bindings, and the future command
// palette name the same actions.
const (
	Quit     ID = "app.quit"
	NewTab   ID = "tab.new"
	CloseTab ID = "tab.close"
)

// LocaleCommandID returns the ID of the command that shows the language of
// tag.
func LocaleCommandID(tag string) ID {
	return ID("locale.set." + tag)
}
