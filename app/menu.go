package app

import (
	"context"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"

	"github.com/mslxl/natcatty/application/command"
	"github.com/mslxl/natcatty/ui/locales"
)

// MenuBindings supplies the accelerators the menu shows from the active
// keyboard-binding scheme. It is the only place the native menu and the
// catalog meet, so a user override and a disabled scheme change both the
// label and the key that runs it.
type MenuBindings interface {
	// Accelerator returns the Electron-style accelerator for a catalog
	// binding, or false when the binding is disabled or cannot be shown as a
	// single menu accelerator.
	Accelerator(bindingID string) (string, bool)
	// FocusAccelerator returns one direction's accelerator for move-focus.
	FocusAccelerator(direction string) (string, bool)
	// MenuSuppressed reports a foreign menu accelerator the user displaced
	// while resolving a conflict.
	MenuSuppressed(menuID string) bool
}

// BuildMenu returns the native application menu. Standard roles provide the
// platform-conventional Edit, Window, and macOS Quit behavior; the app's
// command registry provides New Tab, Close Tab, non-macOS Quit, and language
// switching.
//
// Call it again when the language changes: custom labels and the checked
// language item are part of the template.
func BuildMenu(registry *command.Registry, tag string) *mygo.Menu {
	return buildMenu(registry, tag, nil)
}

// BuildMenuWithBindings builds the menu with accelerators from the active
// keyboard-binding scheme. A nil bindings uses the platform defaults.
func BuildMenuWithBindings(registry *command.Registry, tag string,
	bindings MenuBindings) *mygo.Menu {
	return buildMenu(registry, tag, bindings)
}

func buildMenu(registry *command.Registry, tag string, bindings MenuBindings) *mygo.Menu {
	m := locales.In(tag)
	dispatch := func(id command.ID) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			if err := registry.Dispatch(context.Background(), id); err != nil {
				mygo.Dialog.Error(m.CommandFailed(), err.Error())
			}
		}
	}
	accelerator := func(menuID, bindingID, fallback string) string {
		if bindings == nil {
			return fallback
		}
		if bindings.MenuSuppressed(menuID) {
			return ""
		}
		if bindingID == "" {
			return fallback
		}
		value, ok := bindings.Accelerator(bindingID)
		if !ok {
			return ""
		}
		return value
	}
	focusAccelerator := func(direction, fallback string) string {
		if bindings == nil {
			return fallback
		}
		value, ok := bindings.FocusAccelerator(direction)
		if !ok {
			return ""
		}
		return value
	}

	fileItems := []*mygo.MenuItem{
		{
			ID:          "menu.newTab",
			Label:       m.MenuNewTab(),
			Accelerator: accelerator("menu.newTab", "new-tab", "CmdOrCtrl+T"),
			Click:       dispatch(command.NewTab),
		},
		{
			ID:    "menu.serial",
			Label: m.MenuSerial(),
			Click: dispatch(command.ConnectSerial),
		},
		{
			ID:          "menu.closeTab",
			Label:       m.MenuCloseTab(),
			Accelerator: accelerator("menu.closeTab", "close-tab", "CmdOrCtrl+W"),
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
			ID:          "menu.settings",
			Label:       m.NavSettings(),
			Accelerator: accelerator("menu.settings", "open-settings", "CmdOrCtrl+,"),
			Click:       dispatch(command.OpenSettings),
		},
		{
			ID:          "menu.backups",
			Label:       m.VaultBackupTitle(),
			Accelerator: accelerator("menu.backups", "", "CmdOrCtrl+Shift+B"),
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
				Accelerator: accelerator("menu.quit", "", "CmdOrCtrl+Q"),
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
				Accelerator: accelerator("menu.splitRight", "split-vertical", "CmdOrCtrl+Shift+E"),
				Click:       dispatch(command.SplitPaneRight),
			},
			{
				ID:          "menu.splitDown",
				Label:       m.PaneSplitDown(),
				Accelerator: accelerator("menu.splitDown", "split-horizontal", "CmdOrCtrl+Shift+D"),
				Click:       dispatch(command.SplitPaneDown),
			},
			{
				ID:          "menu.closePane",
				Label:       m.PaneClose(),
				Accelerator: accelerator("menu.closePane", "close-session", "CmdOrCtrl+Shift+W"),
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
				Accelerator: focusAccelerator("Left", "CmdOrCtrl+Alt+Left"),
				Click:       dispatch(command.FocusPaneLeft),
			},
			{
				ID:          "menu.focusRight",
				Label:       m.PaneFocusRight(),
				Accelerator: focusAccelerator("Right", "CmdOrCtrl+Alt+Right"),
				Click:       dispatch(command.FocusPaneRight),
			},
			{
				ID:          "menu.focusUp",
				Label:       m.PaneFocusUp(),
				Accelerator: focusAccelerator("Up", "CmdOrCtrl+Alt+Up"),
				Click:       dispatch(command.FocusPaneUp),
			},
			{
				ID:          "menu.focusDown",
				Label:       m.PaneFocusDown(),
				Accelerator: focusAccelerator("Down", "CmdOrCtrl+Alt+Down"),
				Click:       dispatch(command.FocusPaneDown),
			},
			mygo.Separator(),
			{Label: m.NavLanguage(), Submenu: languageItems},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}
