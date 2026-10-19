package sessionui

import (
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// sessionMenuModel is the state the Session menu shows. It is a plain value so
// menu tests can open the production menu without a native terminal.
type sessionMenuModel struct {
	Encoding     domainterminal.Encoding
	Logging      bool
	LogFormat    domainterminal.LogFormat
	LogDirectory string
	CanSplit     bool
	CanDetach    bool
	CanClosePane bool
	CanPaste     bool
	CanClear     bool
}

// sessionMenuActions are the Session menu's commands. Nil entries show their
// item disabled, so an unavailable action is visibly unavailable rather than
// silently doing nothing.
type sessionMenuActions struct {
	Paste        func()
	ClearBuffer  func()
	SetEncoding  func(domainterminal.Encoding)
	ToggleLog    func()
	SetLogFormat func(domainterminal.LogFormat)
	SplitRight   func()
	SplitDown    func()
	Detach       func()
	ClosePane    func()
}

// sessionMenuButton renders the Session menu as a menu button in the status
// bar. MyGo's terminal owns its own right-click menu (Copy, Paste, Select All,
// Clear Scrollback), so the Session commands live here where they are always
// reachable and testable.
func sessionMenuButton(c *ui.Context, model sessionMenuModel, m locales.Messages,
	t *ui.Theme, actions sessionMenuActions) {
	button := ui.MenuButton(c, m.PaneMenuGlyph(), func(menu *ui.Menu) {
		buildSessionMenu(menu, model, m, actions)
	})
	button.Label(m.SessionMenuLabel())
	button.TextColor(t.TextMuted)
}

// buildSessionMenu fills the Session menu. The order follows the original
// where the native terminal does not already supply the item: paste, split,
// clear, then pane lifetime, encoding, and logging.
func buildSessionMenu(menu *ui.Menu, model sessionMenuModel, m locales.Messages,
	actions sessionMenuActions) {
	paste := menu.Item(m.SessionMenuPaste()).Disabled(actions.Paste == nil || !model.CanPaste)
	paste.Shortcut(ui.Ctrl|ui.Shift, ui.KeyV)
	if paste.Chosen() && actions.Paste != nil {
		actions.Paste()
	}

	menu.Separator()
	splitRight := menu.Item(m.SessionMenuSplitRight()).Disabled(actions.SplitRight == nil || !model.CanSplit)
	if splitRight.Chosen() && actions.SplitRight != nil {
		actions.SplitRight()
	}
	splitDown := menu.Item(m.SessionMenuSplitDown()).Disabled(actions.SplitDown == nil || !model.CanSplit)
	if splitDown.Chosen() && actions.SplitDown != nil {
		actions.SplitDown()
	}

	menu.Separator()
	clear := menu.Item(m.SessionMenuClearBuffer()).Disabled(actions.ClearBuffer == nil || !model.CanClear)
	if clear.Chosen() && actions.ClearBuffer != nil {
		actions.ClearBuffer()
	}

	menu.Separator()
	menu.Submenu(m.SessionMenuEncoding(), func(sub *ui.Menu) {
		for _, encoding := range domainterminal.SupportedEncodings() {
			item := sub.Item(locales.EncodingLabel(m, encoding)).Checked(encoding == model.Encoding)
			if encoding == model.Encoding {
				item.Disabled(true)
			}
			if item.Chosen() && actions.SetEncoding != nil {
				actions.SetEncoding(encoding)
			}
		}
	})

	menu.Submenu(m.SessionMenuLogging(), func(sub *ui.Menu) {
		label := m.SessionLoggingStart()
		if model.Logging {
			label = m.SessionLoggingStop()
		}
		toggle := sub.Item(label).Disabled(actions.ToggleLog == nil ||
			(!model.Logging && model.LogDirectory == ""))
		if toggle.Chosen() && actions.ToggleLog != nil {
			actions.ToggleLog()
		}
		sub.Submenu(m.SessionMenuLogFormat(), func(formats *ui.Menu) {
			for index, format := range chromeFormats {
				item := formats.Item(formatLabels(m)[index]).Checked(format == model.LogFormat)
				if item.Chosen() && actions.SetLogFormat != nil {
					actions.SetLogFormat(format)
				}
			}
		})
	})

	menu.Separator()
	detach := menu.Item(m.SessionMenuDetach()).Disabled(actions.Detach == nil || !model.CanDetach)
	if detach.Chosen() && actions.Detach != nil {
		actions.Detach()
	}
	closePane := menu.Item(m.SessionMenuClosePane()).Disabled(actions.ClosePane == nil || !model.CanClosePane)
	if closePane.Chosen() && actions.ClosePane != nil {
		actions.ClosePane()
	}
}
