package editor

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// The editor and its prompts are overlays over the active surface. The
// geometry keeps them inside the 720x480 minimum window.
const (
	panelMaxWidth  float32 = 760
	panelMaxHeight float32 = 560
	panelTop       float32 = 24
	panelPadding   float32 = 14
)

// View renders the editor document and its prompts. It is mounted by the
// shell, above the SFTP surface, so a conflict raised by an external edit is
// visible wherever the user is.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		m := locales.In(c.Locale())
		t := c.Theme()
		if s.Active() {
			documentView(c, s, m, t)
		}
		if s.ConfirmPending() {
			confirmView(c, s, m, t)
		}
		if s.ConflictPending() {
			conflictView(c, s, m, t)
		}
		if s.FailurePending() {
			failureView(c, s, m, t)
		}
	}
}

func panel(c *ui.Context, t *ui.Theme) ui.Element {
	width, height := c.Size()
	panelWidth := min(width-32, panelMaxWidth)
	panelHeight := min(height-32, panelMaxHeight)
	return ui.Column(c).Role(ui.RoleDialog).
		Width(panelWidth).Height(panelHeight).
		Margin(panelTop, 0, 0, 0).Padding(panelPadding).Gap(8).
		Background(t.Background).Border(1, t.Border).
		Radius(t.Radius+2).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3)).
		Clip()
}

func documentView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	closed := false
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(ui.RGBA(0, 0, 0, 0.4))
		if back.OverlayShortcut(0, ui.KeyEscape) {
			closed = true
		}
		back.Children(func() {
			box := panel(c, t)
			box.OnClick(func() {})
			box.Children(func() {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, m.SftpEditorTitle(s.Name())).FontSize(16).Bold().
						SingleLine().MinWidth(0).TextColor(t.Text)
					ui.Column(c).Grow(1)
					if ui.Button(c, m.SftpEditorClose()).Clicked() {
						s.Close()
						c.Invalidate()
					}
					if ui.PrimaryButton(c, m.SftpEditorSave()).Clicked() {
						s.Save()
						c.Invalidate()
					}
				})
				content := s.Content()
				if content != nil {
					c.Key("editor-content")
					ui.TextArea(c, content).
						Label("editor-content").
						FillWidth().Grow(1).MinHeight(0).AutoFocus()
				}
				switch {
				case s.Saving():
					ui.Text(c, m.SftpEditorSaving()).FontSize(11).TextColor(t.TextMuted)
				case s.SaveError():
					ui.Text(c, m.SftpEditorSaveFailed()).FontSize(11).TextColor(t.Danger)
				case s.Saved():
					ui.Text(c, m.SftpEditorSaved()).FontSize(11).TextColor(t.TextMuted)
				}
			})
		})
	})
	if closed {
		s.Close()
		c.Invalidate()
	}
}

func confirmView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	cancelled := false
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(ui.RGBA(0, 0, 0, 0.4))
		if back.OverlayShortcut(0, ui.KeyEscape) {
			cancelled = true
		}
		back.Children(func() {
			box := panel(c, t)
			box.OnClick(func() {})
			box.Children(func() {
				ui.Text(c, m.SftpEditorUnsavedTitle()).FontSize(16).Bold().TextColor(t.Text)
				ui.Text(c, m.SftpEditorUnsavedBody(s.Name())).FontSize(12).
					TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, m.SftpEditorCancel()).Clicked() {
						s.CancelClose()
						c.Invalidate()
					}
					if ui.PrimaryButton(c, m.SftpEditorDiscard()).Clicked() {
						s.Discard()
						c.Invalidate()
					}
				})
			})
		})
	})
	if cancelled {
		s.CancelClose()
		c.Invalidate()
	}
}

func conflictView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	cancelled := false
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(ui.RGBA(0, 0, 0, 0.4))
		if back.OverlayShortcut(0, ui.KeyEscape) {
			cancelled = true
		}
		back.Children(func() {
			box := panel(c, t)
			box.OnClick(func() {})
			box.Children(func() {
				ui.Text(c, m.SftpEditorConflictTitle()).FontSize(16).Bold().TextColor(t.Text)
				ui.Text(c, m.SftpEditorConflictBody(s.ConflictName())).FontSize(12).
					TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, m.SftpEditorCancel()).Clicked() {
						s.CancelConflict()
						c.Invalidate()
					}
					if ui.PrimaryButton(c, m.SftpEditorOverwrite()).Clicked() {
						s.Overwrite()
						c.Invalidate()
					}
				})
			})
		})
	})
	if cancelled {
		s.CancelConflict()
		c.Invalidate()
	}
}

func failureView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	ui.Overlay(c, func() {
		back := ui.Column(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			AlignItems(ui.Center).Justify(ui.Start).Modal().
			Background(ui.RGBA(0, 0, 0, 0.4))
		back.Children(func() {
			box := panel(c, t)
			box.OnClick(func() {})
			box.Children(func() {
				ui.Text(c, m.SftpEditorSyncFailedTitle()).FontSize(16).Bold().
					TextColor(t.Text)
				ui.Text(c, m.SftpEditorSyncFailedBody(s.failure.name)).FontSize(12).
					TextColor(t.TextMuted)
				ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, m.SftpEditorDismiss()).Clicked() {
						s.DismissFailure()
						c.Invalidate()
					}
					if ui.PrimaryButton(c, m.SftpEditorRetry()).Clicked() {
						s.Retry()
						c.Invalidate()
					}
				})
			})
		})
	})
}
