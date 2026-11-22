package sessionui

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// buildTerminalMenu fills the menu a right-click opens in the terminal:
// selection and clipboard first, then the working directory and the buffer,
// then the whole Session menu as a submenu. Every item stays visible and is
// disabled when its action is unavailable, as the Session menu does; nothing
// silently does nothing.
//
// acts are the live screen's selection commands. A text view (a Lens body, a
// finished command's cells) passes the zero value and has already added
// MyGo's editing items, which hold Copy and Select All for the text
// selection; this builder must not repeat those labels.
func buildTerminalMenu(menu *ui.Menu, model chromeModel, m locales.Messages, acts appview.MenuActions, actions chromeActions, copyDir func()) {
	// A text view's editing items end above; Paste is the terminal's, because
	// the shell is still the input target even when the right-click landed on
	// finished output. Copy and Select All belong to the live screen's own
	// cell selection only; a text view already has them from EditItems.
	if acts.Live {
		copyItem := menu.Item(m.SessionMenuCopy()).Disabled(acts.Copy == nil)
		if copyItem.Chosen() && acts.Copy != nil {
			acts.Copy()
		}
	}
	paste := menu.Item(m.SessionMenuPaste()).Disabled(actions.Paste == nil || !model.CanPaste)
	if paste.Chosen() && actions.Paste != nil {
		actions.Paste()
	}
	if acts.Live {
		selectAll := menu.Item(m.SessionMenuSelectAll()).Disabled(acts.SelectAll == nil)
		if selectAll.Chosen() && acts.SelectAll != nil {
			acts.SelectAll()
		}
	}

	menu.Separator()
	copyCwd := menu.Item(m.TerminalMenuCopyWorkingDirectory()).Disabled(copyDir == nil)
	if copyCwd.Chosen() && copyDir != nil {
		copyDir()
	}
	clear := menu.Item(m.SessionMenuClearBuffer()).Disabled(actions.ClearBuffer == nil)
	if clear.Chosen() && actions.ClearBuffer != nil {
		actions.ClearBuffer()
	}

	menu.Separator()
	// The Session menu is the same menu the toolbar opens, so the two agree on
	// every command. On the live screen its Copy and Select All are given the
	// live selection's commands too: the same labels must not work above and
	// be disabled inside the submenu.
	if acts.Live {
		actions.Copy = acts.Copy
		actions.SelectAll = acts.SelectAll
	}
	menu.Submenu(m.SessionMenuLabel(), func(sub *ui.Menu) {
		sessionMenu(sub, model, m, actions)
	})
}
