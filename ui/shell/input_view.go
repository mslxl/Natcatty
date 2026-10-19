package shell

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// terminalInputs renders the broadcast and compose controls above the active
// Tab. Broadcast is explicit: the toolbar states whether it is active, the
// panel selects exactly which Sessions receive input, and every delivery is
// reported per Session. Compose edits a draft, previews the original
// transformations, and sends to the focused Session or the broadcast set.
func terminalInputs(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			broadcastLabel := m.BroadcastButton()
			if s.Broadcasting() {
				broadcastLabel = m.BroadcastButtonActive(s.BroadcastTargetCount())
			}
			broadcastButton := ui.Button(c, broadcastLabel)
			if s.Broadcasting() {
				broadcastButton.TextColor(t.Accent).Bold()
			}
			if broadcastButton.Clicked() {
				s.ToggleBroadcastPanel()
				c.Invalidate()
			}
			if s.Broadcasting() {
				if ui.PrimaryButton(c, m.BroadcastStop()).Clicked() {
					s.StopBroadcast()
					c.Invalidate()
				}
			}
			composeButton := ui.Button(c, m.ComposeButton())
			if composeButton.Clicked() {
				if s.ComposeOpen() {
					s.CloseCompose()
				} else {
					s.OpenCompose()
				}
				c.Invalidate()
			}
			ui.Column(c).Grow(1)
			if s.ComposeOpen() && s.ComposeSending() {
				ui.Text(c, m.ComposeSending()).FontSize(11).TextColor(t.TextMuted)
			}
		})
		if s.BroadcastPanelOpen() {
			broadcastPanel(c, s, m, t)
		}
		if s.ComposeOpen() {
			composeBar(c, s, m, t)
		}
	})
}

// broadcastPanel selects the Sessions that receive broadcast input and shows
// the most recent per-Session delivery result.
func broadcastPanel(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	sessions := s.BroadcastSessions()
	ui.Column(c).FillWidth().Padding(6).Gap(4).Radius(t.Radius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.BroadcastPanelTitle()).FontSize(13).Bold()
			if s.Broadcasting() {
				ui.Text(c, m.BroadcastActive(s.BroadcastTargetCount())).
					FontSize(11).TextColor(t.Accent)
			} else {
				ui.Text(c, m.BroadcastStopped()).FontSize(11).TextColor(t.TextMuted)
			}
			ui.Column(c).Grow(1)
			if ui.Button(c, m.BroadcastClose()).Clicked() {
				s.CloseBroadcastPanel()
				c.Invalidate()
			}
		})

		// A second row keeps the controls readable at the minimum width
		// instead of squeezing them beside the title.
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			if len(sessions) > 0 {
				if ui.Button(c, m.BroadcastSelectAll()).Clicked() {
					for _, info := range sessions {
						if info.Running {
							s.SetBroadcastTarget(info.ID, true)
						}
					}
					c.Invalidate()
				}
				if ui.Button(c, m.BroadcastSelectNone()).Clicked() {
					for _, info := range sessions {
						s.SetBroadcastTarget(info.ID, false)
					}
					c.Invalidate()
				}
			}
			stop := s.Broadcasting()
			label := m.BroadcastStart()
			if stop {
				label = m.BroadcastStop()
			}
			start := ui.PrimaryButton(c, label)
			if !stop {
				start.Disabled(!s.BroadcastCanStart())
			}
			if start.Clicked() {
				if stop {
					s.StopBroadcast()
				} else {
					s.StartBroadcast()
				}
				c.Invalidate()
			}
		})

		if !s.Broadcasting() && !s.BroadcastCanStart() {
			ui.Text(c, m.BroadcastSelectHint()).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, m.BroadcastTargetsTitle()).FontSize(11).TextColor(t.TextMuted)
		for _, info := range sessions {
			broadcastTargetRow(c, s, m, t, info)
		}
		if result, ok := s.BroadcastResult(); ok {
			ui.Text(c, m.BroadcastSummary(result.DeliveredCount(), len(result.Deliveries))).
				FontSize(11).Bold().TextColor(summaryColor(result, t))
		}
	})
}

func broadcastTargetRow(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	info state.BroadcastSessionInfo) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		selected := info.Selected
		box := ui.Checkbox(c, &selected, sessionTitle(m, info))
		box.Disabled(!info.Running)
		if box.Changed() {
			s.SetBroadcastTarget(info.ID, selected)
			c.Invalidate()
		}
		if info.Focused {
			ui.Text(c, m.BroadcastFocused()).FontSize(10).Bold().TextColor(t.Accent)
		}
		ui.Column(c).Grow(1)
		if info.HasStatus {
			ui.Text(c, deliveryText(m, info.Status)).FontSize(11).
				TextColor(deliveryColor(info.Status, t))
		} else if !info.Running {
			ui.Text(c, m.BroadcastMissing()).FontSize(11).TextColor(t.Danger)
		}
	})
}

func sessionTitle(m locales.Messages, info state.BroadcastSessionInfo) string {
	if info.Label != "" {
		return info.Label
	}
	number := info.TabNumber
	if number == 0 {
		number = info.Number
	}
	return m.LocalSessionTab(number)
}

func deliveryText(m locales.Messages, status appsession.DeliveryStatus) string {
	switch status {
	case appsession.DeliveryDelivered:
		return m.BroadcastDelivered()
	case appsession.DeliveryGated:
		return m.BroadcastGated()
	case appsession.DeliveryMissing:
		return m.BroadcastMissing()
	default:
		return m.BroadcastFailed()
	}
}

func deliveryColor(status appsession.DeliveryStatus, t *ui.Theme) ui.Color {
	switch status {
	case appsession.DeliveryDelivered:
		return t.Success
	case appsession.DeliveryGated:
		return t.Warning
	default:
		return t.Danger
	}
}

func summaryColor(result appsession.SendResult, t *ui.Theme) ui.Color {
	if result.Complete() {
		return t.Success
	}
	return t.Warning
}

// composeBar edits a multi-line draft and previews the original
// transformations before sending to the focused Session or the broadcast set.
func composeBar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	preview := s.ComposePreview()
	ui.Column(c).FillWidth().Padding(6).Gap(4).Radius(t.Radius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.ComposeTitle()).FontSize(13).Bold()
			ui.Text(c, composeTargetText(s, m)).FontSize(11).TextColor(t.TextMuted)
			if result, ok := s.ComposeResult(); ok {
				ui.Text(c, composeResultText(m, result)).FontSize(11).
					TextColor(summaryColor(result, t))
			}
			ui.Column(c).Grow(1)
			if ui.Button(c, m.ComposeCancel()).Clicked() {
				s.CloseCompose()
				c.Invalidate()
			}
		})

		area := ui.TextArea(c, s.ComposeDraft()).FillWidth().Height(48).
			Placeholder(m.ComposePlaceholder()).Label(m.ComposePlaceholder())
		area.OnTextKey(func(k ui.TextKey) bool {
			switch {
			case k.Key == ui.KeyEscape:
				s.CloseCompose()
				c.Invalidate()
				return true
			case k.Key == ui.KeyEnter && k.Mods&ui.Shift == 0:
				// The editor may hold text typed after the last frame, so the
				// key carries the draft to send. The editor clears only when
				// the send was accepted; Escape above cancels it.
				if s.SendComposeDraft(k.Text()) {
					k.SetText("")
				}
				c.Invalidate()
				return true
			}
			return false // Shift+Enter adds a line, as the placeholder says.
		})
		if area.Changed() {
			c.Invalidate()
		}

		previewText := m.ComposePreviewEmpty()
		if !preview.Empty() {
			previewText = preview.PreviewText()
		}
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, m.ComposePreview()).FontSize(10).TextColor(t.TextMuted)
			ui.Text(c, previewText).FontSize(11).SingleLine().
				TextColor(t.Text).Grow(1)
		})

		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.Checkbox(c, s.ComposeTrim(), m.ComposeTrim()).Changed() {
				c.Invalidate()
			}
			if ui.Checkbox(c, s.ComposeDecodeEscapes(), m.ComposeDecodeEscapes()).Changed() {
				c.Invalidate()
			}
			mode := m.ComposePaste()
			if s.ComposeLineMode() == appsession.ComposeLineByLine {
				mode = m.ComposeLineByLine()
			}
			selectElement := ui.Select(c, &mode, []string{m.ComposePaste(), m.ComposeLineByLine()})
			if selectElement.Changed() {
				if mode == m.ComposeLineByLine() {
					s.SetComposeLineMode(appsession.ComposeLineByLine)
				} else {
					s.SetComposeLineMode(appsession.ComposePaste)
				}
				c.Invalidate()
			}
			ui.Column(c).Grow(1)
			targets, _ := s.ComposeTargets()
			send := ui.PrimaryButton(c, m.ComposeSend()).
				Disabled(preview.Empty() || len(targets) == 0 || s.ComposeSending())
			if send.Clicked() {
				s.SendCompose()
				c.Invalidate()
			}
		})
	})
}

func composeTargetText(s *state.State, m locales.Messages) string {
	ids, broadcast := s.ComposeTargets()
	if broadcast {
		return m.ComposeTargetBroadcast(len(ids))
	}
	for _, info := range s.BroadcastSessions() {
		if info.Focused {
			return m.ComposeTargetFocusedNamed(sessionTitle(m, info))
		}
	}
	return m.ComposeTargetFocused()
}

func composeResultText(m locales.Messages, result appsession.SendResult) string {
	if result.DeliveredCount() == 0 {
		return m.ComposeFailed()
	}
	return m.ComposeSent(result.DeliveredCount())
}
