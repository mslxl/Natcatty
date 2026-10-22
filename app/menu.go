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
			if err := registry.Dispatch(context.Background(), id); err != nil {
				mygo.Dialog.Error(m.CommandFailed(), err.Error())
			}
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
		{
			ID:    "menu.closeOtherTabs",
			Label: m.TabCloseOthers(),
			Click: dispatch(command.CloseOtherTabs),
		},
		{
			ID:    "menu.closeTabsRight",
			Label: m.TabCloseRight(),
			Click: dispatch(command.CloseTabsRight),
		},
		{
			ID:    "menu.duplicateTab",
			Label: m.TabDuplicate(),
			Click: dispatch(command.DuplicateTab),
		},
		{
			ID:    "menu.detachTab",
			Label: m.TabDetach(),
			Click: dispatch(command.DetachTab),
		},
		{
			ID:    "menu.copyTabToWindow",
			Label: m.TabCopyToNewWindow(),
			Click: dispatch(command.CopyTabToNewWindow),
		},
		{
			ID:    "menu.lockApp",
			Label: m.MenuLockApp(),
			Click: dispatch(command.LockApp),
		},
		{
			ID:          "menu.backups",
			Label:       m.VaultBackupTitle(),
			Accelerator: "CmdOrCtrl+Shift+B",
			Click:       dispatch(command.OpenBackups),
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
			{
				ID:          "menu.splitRight",
				Label:       m.PaneSplitRight(),
				Accelerator: "CmdOrCtrl+Shift+E",
				Click:       dispatch(command.SplitPaneRight),
			},
			{
				ID:          "menu.splitDown",
				Label:       m.PaneSplitDown(),
				Accelerator: "CmdOrCtrl+Shift+D",
				Click:       dispatch(command.SplitPaneDown),
			},
			{
				ID:          "menu.closePane",
				Label:       m.PaneClose(),
				Accelerator: "CmdOrCtrl+Shift+W",
				Click:       dispatch(command.ClosePane),
			},
			{
				ID:    "menu.detachPane",
				Label: m.PaneDetach(),
				Click: dispatch(command.DetachPane),
			},
			mygo.Separator(),
			{
				ID:          "menu.focusLeft",
				Label:       m.PaneFocusLeft(),
				Accelerator: "CmdOrCtrl+Alt+Left",
				Click:       dispatch(command.FocusPaneLeft),
			},
			{
				ID:          "menu.focusRight",
				Label:       m.PaneFocusRight(),
				Accelerator: "CmdOrCtrl+Alt+Right",
				Click:       dispatch(command.FocusPaneRight),
			},
			{
				ID:          "menu.focusUp",
				Label:       m.PaneFocusUp(),
				Accelerator: "CmdOrCtrl+Alt+Up",
				Click:       dispatch(command.FocusPaneUp),
			},
			{
				ID:          "menu.focusDown",
				Label:       m.PaneFocusDown(),
				Accelerator: "CmdOrCtrl+Alt+Down",
				Click:       dispatch(command.FocusPaneDown),
			},
			mygo.Separator(),
			{Label: m.NavLanguage(), Submenu: languageItems},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}
