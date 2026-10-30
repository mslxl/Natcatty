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
	CanPaste     bool
	CanReconnect bool
	CanClear     bool
	CanSave      bool
	CanRename    bool
	CanSplit     bool
	CanDetach    bool
	CanClosePane bool
	Hibernated   bool
}

// sessionMenuActions are the Session menu's commands. Nil entries show their
// item disabled, so an unavailable action is visibly unavailable rather than
// silently doing nothing.
type sessionMenuActions struct {
	Copy           func()
	Paste          func()
	PasteSelection func()
	SelectAll      func()
	OpenBrowser    func()
	Reconnect      func()
	SendYmodem     func()
	ReceiveYmodem  func()
	SaveScreen     func()
	ClearBuffer    func()
	Rename         func()
	SetEncoding    func(domainterminal.Encoding)
	ToggleLog      func()
	SetLogFormat   func(domainterminal.LogFormat)
	SplitRight     func()
	SplitDown      func()
	Detach         func()
	ClosePane      func()
	Hibernate      func()
	Resume         func()
	Observe        func()
}

// buildSessionMenu fills the Session menu in the original's order: selection,
// browser, reconnect, YMODEM, splits, screen, identity, and lifetime. Encoding
// and logging sit after the screen commands because the original exposed them
// in its toolbar rather than this menu. Every item stays visible and shows a
// disabled state when its backend is absent; the parity notes list the three
// selection actions the terminal plugin exposes only through its native menu.
func buildSessionMenu(menu *ui.Menu, model sessionMenuModel, m locales.Messages,
	actions sessionMenuActions) {
	copyItem := menu.Item(m.SessionMenuCopy()).Disabled(actions.Copy == nil)
	copyItem.Shortcut(ui.Ctrl|ui.Shift, ui.KeyC)
	if copyItem.Chosen() && actions.Copy != nil {
		actions.Copy()
	}
	paste := menu.Item(m.SessionMenuPaste()).Disabled(actions.Paste == nil || !model.CanPaste)
	paste.Shortcut(ui.Ctrl|ui.Shift, ui.KeyV)
	if paste.Chosen() && actions.Paste != nil {
		actions.Paste()
	}
	pasteSelection := menu.Item(m.SessionMenuPasteSelection()).Disabled(actions.PasteSelection == nil)
	if pasteSelection.Chosen() && actions.PasteSelection != nil {
		actions.PasteSelection()
	}
	selectAll := menu.Item(m.SessionMenuSelectAll()).Disabled(actions.SelectAll == nil)
	selectAll.Shortcut(ui.Ctrl|ui.Shift, ui.KeyA)
	if selectAll.Chosen() && actions.SelectAll != nil {
		actions.SelectAll()
	}

	menu.Separator()
	openBrowser := menu.Item(m.SessionMenuOpenBrowser()).Disabled(actions.OpenBrowser == nil)
	if openBrowser.Chosen() && actions.OpenBrowser != nil {
		actions.OpenBrowser()
	}

	menu.Separator()
	reconnect := menu.Item(m.SessionMenuReconnect()).Disabled(actions.Reconnect == nil || !model.CanReconnect)
	if reconnect.Chosen() && actions.Reconnect != nil {
		actions.Reconnect()
	}

	menu.Separator()
	send := menu.Item(m.SessionMenuSendYmodem()).Disabled(actions.SendYmodem == nil)
	if send.Chosen() && actions.SendYmodem != nil {
		actions.SendYmodem()
	}
	receive := menu.Item(m.SessionMenuReceiveYmodem()).Disabled(actions.ReceiveYmodem == nil)
	if receive.Chosen() && actions.ReceiveYmodem != nil {
		actions.ReceiveYmodem()
	}

	menu.Separator()
	// The original calls the top/bottom split Horizontal and the left/right
	// split Vertical. Keep those names; the shell's SplitDown is top/bottom
	// and SplitRight is left/right.
	splitHorizontal := menu.Item(m.SessionMenuSplitHorizontal()).Disabled(actions.SplitDown == nil || !model.CanSplit)
	splitHorizontal.Shortcut(ui.Ctrl|ui.Shift, ui.KeyD)
	if splitHorizontal.Chosen() && actions.SplitDown != nil {
		actions.SplitDown()
	}
	splitVertical := menu.Item(m.SessionMenuSplitVertical()).Disabled(actions.SplitRight == nil || !model.CanSplit)
	splitVertical.Shortcut(ui.Ctrl|ui.Shift, ui.KeyE)
	if splitVertical.Chosen() && actions.SplitRight != nil {
		actions.SplitRight()
	}

	menu.Separator()
	saveScreen := menu.Item(m.SessionMenuSaveScreen()).Disabled(actions.SaveScreen == nil || !model.CanSave)
	if saveScreen.Chosen() && actions.SaveScreen != nil {
		actions.SaveScreen()
	}
	clear := menu.Item(m.SessionMenuClearBuffer()).Disabled(actions.ClearBuffer == nil || !model.CanClear)
	clear.Shortcut(ui.Ctrl|ui.Shift, ui.KeyK)
	if clear.Chosen() && actions.ClearBuffer != nil {
		actions.ClearBuffer()
	}

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
		// The reason Start is refused shows where the user looks for it,
		// not as a standing line above every terminal.
		if !model.Logging && model.LogDirectory == "" {
			sub.Item(m.SessionLoggingNoDirectory()).Disabled(true)
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
	// The lifetime slot holds Hibernate while the Session has a live screen
	// and Resume while it is hibernated. One item keeps the menu's shape
	// stable and its state always visible.
	lifetimeLabel := m.SessionMenuHibernate()
	lifetimeAction := actions.Hibernate
	if model.Hibernated {
		lifetimeLabel = m.SessionResume()
		lifetimeAction = actions.Resume
	}
	lifetime := menu.Item(lifetimeLabel).Disabled(lifetimeAction == nil)
	if lifetime.Chosen() && lifetimeAction != nil {
		lifetimeAction()
	}
	observe := menu.Item(m.SessionMenuObserveSnapshot()).Disabled(actions.Observe == nil)
	if observe.Chosen() && actions.Observe != nil {
		actions.Observe()
	}

	menu.Separator()
	rename := menu.Item(m.SessionMenuRename()).Disabled(actions.Rename == nil || !model.CanRename)
	if rename.Chosen() && actions.Rename != nil {
		actions.Rename()
	}

	menu.Separator()
	detach := menu.Item(m.SessionMenuDetach()).Disabled(actions.Detach == nil || !model.CanDetach)
	if detach.Chosen() && actions.Detach != nil {
		actions.Detach()
	}

	menu.Separator()
	closePane := menu.Item(m.SessionMenuCloseTerminal()).Disabled(actions.ClosePane == nil || !model.CanClosePane)
	closePane.Shortcut(ui.Ctrl|ui.Shift, ui.KeyW)
	if closePane.Chosen() && actions.ClosePane != nil {
		actions.ClosePane()
	}
}
