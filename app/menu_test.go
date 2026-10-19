package app

import (
	"context"
	"runtime"
	"testing"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/application/command"
	"github.com/mslxl/natcatty/ui/locales"
)

func TestMenuDispatchesNewAndCloseTabCommands(t *testing.T) {
	registry := command.NewRegistry()
	var dispatched []command.ID
	for _, id := range []command.ID{command.NewTab, command.CloseTab} {
		id := id
		if err := registry.Register(id, func(context.Context) error {
			dispatched = append(dispatched, id)
			return nil
		}); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}

	menu := BuildMenu(registry, "en")

	newTab := findItem(t, menu.Items(), "menu.newTab")
	if newTab.Label != "New Tab" {
		t.Errorf("New Tab label = %q, want %q", newTab.Label, "New Tab")
	}
	if newTab.Accelerator != "CmdOrCtrl+T" {
		t.Errorf("New Tab accelerator = %q, want %q", newTab.Accelerator, "CmdOrCtrl+T")
	}
	newTab.Click(newTab, nil)

	closeTab := findItem(t, menu.Items(), "menu.closeTab")
	if closeTab.Label != "Close Tab" {
		t.Errorf("Close Tab label = %q, want %q", closeTab.Label, "Close Tab")
	}
	if closeTab.Accelerator != "CmdOrCtrl+W" {
		t.Errorf("Close Tab accelerator = %q, want %q", closeTab.Accelerator, "CmdOrCtrl+W")
	}
	closeTab.Click(closeTab, nil)

	if len(dispatched) != 2 || dispatched[0] != command.NewTab || dispatched[1] != command.CloseTab {
		t.Fatalf("dispatched = %v, want [%s %s]", dispatched, command.NewTab, command.CloseTab)
	}
}

func TestMenuCarriesTheStandardQuitRoleOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keeps Quit in the standard application menu")
	}

	menu := BuildMenu(command.NewRegistry(), "en")
	appMenu := findRole(t, menu.Items(), mygo.RoleAppMenu)
	if appMenu.Hidden {
		t.Fatal("the macOS app menu is hidden")
	}
	quit := findRole(t, appMenu.Submenu, mygo.RoleQuit)
	if quit.Hidden {
		t.Fatal("the macOS Quit item is hidden")
	}
}

func TestMenuDispatchesQuitCommandElsewhere(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses the standard application menu's Quit role")
	}

	registry := command.NewRegistry()
	quits := 0
	if err := registry.Register(command.Quit, func(context.Context) error {
		quits++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	menu := BuildMenu(registry, "en")
	file := findLabel(t, menu.Items(), "File")
	quit := findItem(t, file.Submenu, "menu.quit")
	if quit.Label != "Quit" {
		t.Errorf("Quit label = %q, want %q", quit.Label, "Quit")
	}
	if quit.Accelerator != "CmdOrCtrl+Q" {
		t.Errorf("Quit accelerator = %q, want %q", quit.Accelerator, "CmdOrCtrl+Q")
	}
	quit.Click(quit, nil)
	if quits != 1 {
		t.Fatalf("quits = %d after choosing Quit, want 1", quits)
	}
}

func TestMenuLocalizesItsLabels(t *testing.T) {
	menu := BuildMenu(command.NewRegistry(), "ru")

	file := findLabel(t, menu.Items(), "Файл")
	newTab := findItem(t, file.Submenu, "menu.newTab")
	if newTab.Label != "Новая вкладка" {
		t.Errorf("New Tab label = %q, want %q", newTab.Label, "Новая вкладка")
	}
}

func TestMenuLanguageItemsDispatchLocaleCommands(t *testing.T) {
	registry := command.NewRegistry()
	var dispatched []command.ID
	for _, tag := range locales.Tags() {
		id := command.LocaleCommandID(tag)
		if err := registry.Register(id, func(context.Context) error {
			dispatched = append(dispatched, id)
			return nil
		}); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}

	menu := BuildMenu(registry, "en")
	view := findLabel(t, menu.Items(), "View")
	language := findLabel(t, view.Submenu, "Language")

	var spanish *mygo.MenuItem
	var english *mygo.MenuItem
	for _, item := range language.Submenu {
		if item.Label == "español" {
			spanish = item
		}
		if item.Label == "English" {
			english = item
		}
	}
	if spanish == nil || english == nil {
		t.Fatalf("language items are missing, got %v", submenuLabels(language))
	}
	if !english.Checked {
		t.Error("English is not checked in an English menu")
	}
	if spanish.Checked {
		t.Error("Spanish is checked in an English menu")
	}

	spanish.Click(spanish, nil)
	if len(dispatched) != 1 || dispatched[0] != command.LocaleCommandID("es") {
		t.Fatalf("dispatched = %v, want [%s]", dispatched, command.LocaleCommandID("es"))
	}
}

// findMenuItem returns the first item in the menu tree that matches, depth
// first.
func findMenuItem(items []*mygo.MenuItem, match func(*mygo.MenuItem) bool) *mygo.MenuItem {
	for _, item := range items {
		if match(item) {
			return item
		}
		if item.Submenu != nil {
			if found := findMenuItem(item.Submenu, match); found != nil {
				return found
			}
		}
	}
	return nil
}

func findItem(t *testing.T, items []*mygo.MenuItem, id string) *mygo.MenuItem {
	t.Helper()
	found := findMenuItem(items, func(item *mygo.MenuItem) bool { return item.ID == id })
	if found == nil {
		t.Fatalf("menu item %q not found", id)
	}
	return found
}

func findRole(t *testing.T, items []*mygo.MenuItem, role mygo.MenuRole) *mygo.MenuItem {
	t.Helper()
	found := findMenuItem(items, func(item *mygo.MenuItem) bool { return item.Role == role })
	if found == nil {
		t.Fatalf("menu role %q not found", role)
	}
	return found
}

func findLabel(t *testing.T, items []*mygo.MenuItem, label string) *mygo.MenuItem {
	t.Helper()
	found := findMenuItem(items, func(item *mygo.MenuItem) bool { return item.Label == label })
	if found == nil {
		t.Fatalf("menu item labelled %q not found", label)
	}
	return found
}

func submenuLabels(item *mygo.MenuItem) []string {
	labels := make([]string, 0, len(item.Submenu))
	for _, child := range item.Submenu {
		labels = append(labels, child.Label)
	}
	return labels
}
