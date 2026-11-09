package editor

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// The editor and its prompts are overlays over the active surface. The
// geometry keeps them inside the 720x480 minimum window and the 704x421
// client area.
const (
	panelMaxWidth  float32 = 760
	panelMaxHeight float32 = 560
	panelTop       float32 = 24
	panelPadding   float32 = 14

	// promptWidth is the confirmation prompts' maximum width. Their height
	// follows their content, so a prompt reads as a dialog, not as an empty
	// editor frame.
	promptWidth  float32 = 440
	promptMargin float32 = 16
)

// promptPanel is a compact, centred confirmation: title, body, actions, and
// no editor frame or text area. Modal supplies the dim backdrop, the centre,
// and Escape/outside-click dismissal. The panel's height follows its content;
// its width is clamped so a 704-pixel client keeps the margin.
func promptPanel(c *ui.Context, label string, build func()) bool {
	width, _ := c.Size()
	promptPanelWidth := promptWidth
	if promptPanelWidth > width-2*promptMargin {
		promptPanelWidth = width - 2*promptMargin
	}
	open := true
	kit.DialogWith(c, &open, kit.DialogOptions{Role: kit.DialogSheet, Label: label, Width: promptPanelWidth}, build, nil)
	return open
}

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

func documentView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	width, height := c.Size()
	closed := kit.Palette(c, kit.PaletteOptions{
		Width: min(width-32, panelMaxWidth), Height: min(height-32, panelMaxHeight),
		Top: panelTop, Padding: panelPadding, Gap: 8,
	}, func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SftpEditorTitle(s.Name())).FontSize(kit.M(c).Font.Title3).Bold().
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
			ui.Text(c, m.SftpEditorSaving()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		case s.SaveError():
			ui.Text(c, m.SftpEditorSaveFailed()).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.DangerText(t))
		case s.Saved():
			ui.Text(c, m.SftpEditorSaved()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		}
	})
	if closed {
		s.Close()
		c.Invalidate()
	}
}

func confirmView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	open := promptPanel(c, "editor-confirm-prompt", func() {
		ui.Text(c, m.SftpEditorUnsavedTitle()).FontSize(kit.M(c).Font.Title3).Bold().TextColor(t.Text)
		ui.Text(c, m.SftpEditorUnsavedBody(s.Name())).FontSize(kit.M(c).Font.Footnote).
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
	if !open {
		s.CancelClose()
		c.Invalidate()
	}
}

func conflictView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	open := promptPanel(c, "editor-conflict-prompt", func() {
		ui.Text(c, m.SftpEditorConflictTitle()).FontSize(kit.M(c).Font.Title3).Bold().TextColor(t.Text)
		ui.Text(c, m.SftpEditorConflictBody(s.ConflictName())).FontSize(kit.M(c).Font.Footnote).
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
	if !open {
		s.CancelConflict()
		c.Invalidate()
	}
}

func failureView(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	open := promptPanel(c, "editor-failure-prompt", func() {
		ui.Text(c, m.SftpEditorSyncFailedTitle()).FontSize(kit.M(c).Font.Title3).Bold().
			TextColor(t.Text)
		ui.Text(c, m.SftpEditorSyncFailedBody(s.failure.name)).FontSize(kit.M(c).Font.Footnote).
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
	if !open {
		s.DismissFailure()
		c.Invalidate()
	}
}
