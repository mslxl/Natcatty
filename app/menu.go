package app

import (
	"context"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"

	"github.com/mslxl/natcatty/application/command"
	"github.com/mslxl/natcatty/ui/locales"
)

// BuildMenu returns the native application menu. Standard roles provide the
// platform-conventional Edit, Window, and macOS Quit behavior; the app's
// command registry provides New Tab, Close Tab, non-macOS Quit, and language
// switching.
//
// Call it again when the language changes: custom labels and the checked
// language item are part of the template.
func BuildMenu(registry *command.Registry, tag string) *mygo.Menu {
	m := locales.In(tag)
	dispatch := func(id command.ID) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			_ = registry.Dispatch(context.Background(), id)
		}
	}

	fileItems := []*mygo.MenuItem{
		{
			ID:          "menu.newTab",
			Label:       m.MenuNewTab(),
			Accelerator: "CmdOrCtrl+T",
			Click:       dispatch(command.NewTab),
		},
		{
			ID:          "menu.closeTab",
			Label:       m.MenuCloseTab(),
			Accelerator: "CmdOrCtrl+W",
			Click:       dispatch(command.CloseTab),
		},
	}
	if runtime.GOOS != "darwin" {
		// macOS keeps Quit in its standard application menu, whose role
		// MyGo localizes. Elsewhere the File menu's Quit goes through the
		// app command engine like the other app commands.
		fileItems = append(fileItems,
			mygo.Separator(),
			&mygo.MenuItem{
				ID:          "menu.quit",
				Label:       m.MenuQuit(),
				Accelerator: "CmdOrCtrl+Q",
				Click:       dispatch(command.Quit),
			},
		)
	}

	tags := locales.Tags()
	languageItems := make([]*mygo.MenuItem, 0, len(tags))
	for _, localeTag := range tags {
		languageItems = append(languageItems, &mygo.MenuItem{
			ID:      "menu.locale." + localeTag,
			Label:   i18n.Autonym(localeTag),
			Type:    mygo.MenuItemRadio,
			Checked: localeTag == tag,
			Click:   dispatch(command.LocaleCommandID(localeTag)),
		})
	}

	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: m.MenuFile(), Submenu: fileItems},
		{Role: mygo.RoleEditMenu},
		{Label: m.MenuView(), Submenu: []*mygo.MenuItem{
			{Label: m.NavLanguage(), Submenu: languageItems},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}
