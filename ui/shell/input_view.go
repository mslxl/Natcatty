package shell

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// inputLineHeight is the height of one line of text in the broadcast and
// compose controls. Platforms lay the same label out at different heights
// (macOS draws a 12-DIP label at 15.3 DIPs), so a control that leaves the
// line to the font can render below the 16-DIP minimum it needs. A fixed
// line height keeps every one of them one readable line tall everywhere.
const inputLineHeight float32 = 17

// inputButton and inputPrimaryButton are the buttons of the broadcast and
// compose controls, with their text line pinned.
func inputButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).FixedLineHeight(inputLineHeight)
}

func inputPrimaryButton(c *ui.Context, label string) ui.Element {
	return ui.PrimaryButton(c, label).FixedLineHeight(inputLineHeight)
}

// inputCheckbox is a checkbox of the broadcast and compose controls, with
// its label's text line pinned like the buttons'.
func inputCheckbox(c *ui.Context, value *bool, label string) ui.Element {
	return ui.Checkbox(c, value, label).FixedLineHeight(inputLineHeight)
}

// terminalToolbar renders the broadcast and compose toggles in the focused
// Pane's header, beside the Session's own controls. Broadcast is explicit: the toggle
// states whether it is active, the panel selects exactly which Sessions
// receive input, and every delivery is reported per Session. Compose edits a
// draft, previews the original transformations, and sends to the focused
// Session or the broadcast set.
func terminalToolbar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	if s.ComposeOpen() {
		// A pending reconnect is surfaced before a send starts; the draft
		// stays in the editor until the target is ready.
		switch {
		case s.ComposeReconnecting():
			ui.Text(c, m.ConnectionStatusReconnecting()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		case s.ComposeSending():
			ui.Text(c, m.ComposeSending()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		}
	}
	broadcastLabel := m.BroadcastButton()
	if s.Broadcasting() {
		broadcastLabel = m.BroadcastButtonActive(s.BroadcastTargetCount())
	}
	broadcastButton := kit.IconButtonWith(c, icons.Radio, broadcastLabel, kit.IconOptions{Role: kit.IconPane, On: s.BroadcastPanelOpen()})
	if s.Broadcasting() {
		broadcastButton.Background(t.Accent.Alpha(0.14)).TextColor(t.Accent)
	}
	if broadcastButton.Clicked() {
		s.ToggleBroadcastPanel()
		c.Invalidate()
	}
	if s.Broadcasting() {
		stop := kit.IconButtonWith(c, icons.CircleX, m.BroadcastStop(), kit.IconOptions{Role: kit.IconPane, On: false}).TextColor(t.Danger)
		if stop.Clicked() {
			s.StopBroadcast()
			c.Invalidate()
		}
	}
	if kit.IconButtonWith(c, icons.NotebookPen, m.ComposeButton(), kit.IconOptions{Role: kit.IconPane, On: s.ComposeOpen()}).Clicked() {
		if s.ComposeOpen() {
			s.CloseCompose()
		} else {
			s.OpenCompose()
		}
		c.Invalidate()
	}
}

// terminalPanels renders the open broadcast and compose panels above the
// active Tab's Panes.
func terminalPanels(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	if !s.BroadcastPanelOpen() && !s.ComposeOpen() {
		return
	}
	ui.Column(c).FillWidth().Padding(8, 8, 0, 8).Gap(6).Children(func() {
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
	ui.Column(c).FillWidth().Padding(4).Gap(2).Radius(t.Radius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.BroadcastPanelTitle()).FontSize(kit.M(c).Font.Callout).Bold()
			if s.Broadcasting() {
				ui.Text(c, m.BroadcastActive(s.BroadcastTargetCount())).
					FontSize(kit.M(c).Font.Caption).TextColor(t.Accent)
			} else {
				ui.Text(c, m.BroadcastStopped()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
			}
			ui.Column(c).Grow(1)
			if inputButton(c, m.BroadcastClose()).Clicked() {
				s.CloseBroadcastPanel()
				c.Invalidate()
			}
		})

		// A second row keeps the controls readable at the minimum width
		// instead of squeezing them beside the title.
		ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			if len(sessions) > 0 {
				if inputButton(c, m.BroadcastSelectAll()).Clicked() {
					for _, info := range sessions {
						if info.Running {
							s.SetBroadcastTarget(info.ID, true)
						}
					}
					c.Invalidate()
				}
				if inputButton(c, m.BroadcastSelectNone()).Clicked() {
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
			start := inputPrimaryButton(c, label)
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
			ui.Text(c, m.BroadcastSelectHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		}
		ui.Text(c, m.BroadcastTargetsTitle()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		for _, info := range sessions {
			broadcastTargetRow(c, s, m, t, info)
		}
		if notice := s.BroadcastNotice(); notice > 0 {
			ui.Text(c, m.BroadcastTargetsRemoved(notice)).FontSize(kit.M(c).Font.Caption).
				TextColor(uitheme.WarningText(t))
		}
		if result, ok := s.BroadcastResult(); ok {
			ui.Text(c, m.BroadcastSummary(result.DeliveredCount(), len(result.Deliveries))).
				FontSize(kit.M(c).Font.Caption).Bold().TextColor(summaryColor(result, t))
		}
	})
}

func broadcastTargetRow(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	info state.BroadcastSessionInfo) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		selected := info.Selected
		box := inputCheckbox(c, &selected, sessionTitle(m, info))
		box.Disabled(!info.Running)
		if box.Changed() {
			s.SetBroadcastTarget(info.ID, selected)
			c.Invalidate()
		}
		if info.Focused {
			ui.Text(c, m.BroadcastFocused()).FontSize(kit.M(c).Font.Caption2).Bold().TextColor(t.Accent)
		}
		ui.Column(c).Grow(1)
		if info.HasStatus {
			ui.Text(c, deliveryText(m, info.Status)).FontSize(kit.M(c).Font.Caption).
				TextColor(deliveryColor(info.Status, t))
		} else if !info.Running {
			ui.Text(c, m.BroadcastMissing()).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.DangerText(t))
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
		return uitheme.WarningText(t)
	default:
		return uitheme.DangerText(t)
	}
}

func summaryColor(result appsession.SendResult, t *ui.Theme) ui.Color {
	if result.Complete() {
		return t.Success
	}
	return uitheme.WarningText(t)
}

// composeBar edits a multi-line draft and previews the original
// transformations before sending to the focused Session or the broadcast set.
func composeBar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	preview := s.ComposePreview()
	_, windowHeight := c.Size()
	// The compose bar shares the content column with the terminal and, when
	// open, the broadcast target list. At the 421-DIP client of the enforced
	// 720x480 window that pressure pushed Trim, Paste, and Send below the
	// client. With both panels open at that client the header, editor, and
	// preview scroll as a bounded body; the option row with Send stays pinned
	// at the bar's bottom, as the Vault and forwarding editors pin their
	// actions, so the primary action is always inside the window. A 480-DIP
	// content height still fits the whole bar, and with no target list there
	// is room for it, so those cases keep the preview in view.
	bounded := s.BroadcastPanelOpen() && windowHeight > 0 && windowHeight < 470
	draftHeight, previewHeight := float32(40), float32(36)
	if bounded {
		draftHeight, previewHeight = 26, 24
	}

	body := func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.ComposeTitle()).FontSize(kit.M(c).Font.Callout).Bold()
			// The target is a menu button: its label is what Send would
			// deliver to now, and the menu is where the compose-specific set
			// is chosen. It never edits the toolbar broadcast selection.
			inputButton(c, composeTargetText(s, m)).Menu(func(menu *ui.Menu) {
				if menu.Item(m.ComposeTargetFollow()).Checked(!s.ComposeTargetExplicit()).Chosen() {
					s.ResetComposeTargets()
					c.Invalidate()
				}
				menu.Separator()
				for _, info := range s.BroadcastSessions() {
					if !info.Running {
						continue
					}
					selected := s.ComposeTargetSelected(info.ID)
					if menu.Item(sessionTitle(m, info)).Checked(selected).Chosen() {
						s.SetComposeTarget(info.ID, !selected)
						c.Invalidate()
					}
				}
			})
			if inputButton(c, m.ComposeDirectoryButton()).Clicked() {
				s.ToggleComposeDirectory()
				c.Invalidate()
			}
			if result, ok := s.ComposeResult(); ok {
				ui.Text(c, composeResultText(m, result)).FontSize(kit.M(c).Font.Caption).
					TextColor(summaryColor(result, t)).SingleLine()
			}
			ui.Column(c).Grow(1)
			if inputButton(c, m.ComposeCancel()).Clicked() {
				s.CloseCompose()
				c.Invalidate()
			}
		})

		if s.ComposeTargetDiverges() {
			ui.Text(c, m.ComposeTargetsDiverged()).FontSize(kit.M(c).Font.Caption).
				TextColor(uitheme.WarningText(t))
		}
		if notice := s.ComposeNotice(); notice > 0 {
			ui.Text(c, m.BroadcastTargetsRemoved(notice)).FontSize(kit.M(c).Font.Caption).
				TextColor(uitheme.WarningText(t))
		}

		composeDirectoryPanel(c, s, m, t)

		area := ui.TextArea(c, s.ComposeDraft()).FillWidth().Height(draftHeight).
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
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, m.ComposePreview()).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted)
			// The transformed draft wraps in a short scroll view: a long
			// multi-line command can be inspected in full before Send instead
			// of being cut at the first line.
			ui.Scroll(c).Grow(1).Height(previewHeight).Children(func() {
				ui.Text(c, previewText).FontSize(kit.M(c).Font.Caption).Wrap().TextColor(t.Text)
			})
		})
	}

	actions := func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			if inputCheckbox(c, s.ComposeTrim(), m.ComposeTrim()).Changed() {
				c.Invalidate()
			}
			if inputCheckbox(c, s.ComposeDecodeEscapes(), m.ComposeDecodeEscapes()).Changed() {
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
			send := inputPrimaryButton(c, m.ComposeSend()).
				Disabled(preview.Empty() || len(targets) == 0 || s.ComposeSending() || s.ComposeReconnecting())
			if send.Clicked() {
				s.SendCompose()
				c.Invalidate()
			}
		})
	}

	bar := ui.Column(c).FillWidth().Padding(6).Gap(3).Radius(t.Radius).
		Background(t.Surface).Border(1, t.Border)
	if bounded {
		bar.MaxHeight(windowHeight * 0.28)
	}
	bar.Children(func() {
		if bounded {
			ui.Scroll(c).FillWidth().Grow(1).Children(body)
		} else {
			body()
		}
		actions()
	})
}

// composeDirectoryPanel browses the Session's filesystem from the working
// directory the shell reported. Listing runs through the Session's existing
// command path; confirming a path only inserts it into the draft.
func composeDirectoryPanel(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	// Follow a changed OSC 7 directory and a Session that ended.
	s.RefreshComposeDirectory()
	if !s.ComposeDirectoryOpen() {
		return
	}
	ui.Column(c).FillWidth().Padding(4).Gap(3).Radius(t.Radius).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.ComposeDirectoryTitle()).FontSize(kit.M(c).Font.Caption).Bold()
			if path := s.ComposeDirectoryPath(); path != "" {
				ui.Text(c, path).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted).SingleLine()
			}
			ui.Column(c).Grow(1)
			if inputButton(c, m.ComposeDirectoryUp()).Clicked() {
				s.ComposeDirectoryUp()
				c.Invalidate()
			}
		})
		if s.ComposeDirectoryUnknownCWD() {
			ui.Text(c, m.ComposeDirectoryUnknownCWD()).FontSize(kit.M(c).Font.Caption2).
				TextColor(uitheme.WarningText(t))
		}
		ui.Row(c).FillWidth().Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			input := ui.TextInput(c, s.ComposeDirectoryTyped()).
				Label(m.ComposeDirectoryPathLabel()).Width(360)
			if input.Changed() {
				c.Invalidate()
			}
			if inputPrimaryButton(c, m.ComposeDirectoryInsert()).Clicked() {
				s.ConfirmComposeDirectory()
				c.Invalidate()
			}
		})
		ui.Text(c, m.ComposeDirectoryHint()).FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted)

		switch {
		case s.ComposeDirectoryUnsupported():
			ui.Text(c, m.ComposeDirectoryUnsupported()).FontSize(kit.M(c).Font.Caption).
				TextColor(uitheme.DangerText(t))
		case s.ComposeDirectoryLoading():
			ui.Text(c, m.ComposeDirectoryLoading()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		case s.ComposeDirectoryFailed():
			ui.Text(c, m.ComposeDirectoryFailed()).FontSize(kit.M(c).Font.Caption).
				TextColor(uitheme.DangerText(t))
		case len(s.ComposeDirectoryEntries()) == 0:
			ui.Text(c, m.ComposeDirectoryEmpty()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		default:
			// A long listing scrolls inside the panel so the draft, options, and
			// Send stay in the window.
			ui.Scroll(c).FillWidth().Height(120).Children(func() {
				ui.Column(c).FillWidth().Gap(2).Children(func() {
					for _, entry := range s.ComposeDirectoryEntries() {
						label := entry.Name
						if entry.Directory {
							label += "/"
						}
						if inputButton(c, label).Clicked() {
							if entry.Directory {
								s.NavigateComposeDirectory(entry.Name)
							} else {
								s.InsertComposeDirectoryEntry(entry.Name)
							}
							c.Invalidate()
						}
					}
				})
			})
		}
	})
}

func composeTargetText(s *state.State, m locales.Messages) string {
	ids, broadcast := s.ComposeTargets()
	if s.ComposeTargetExplicit() {
		if len(ids) == 0 {
			return m.ComposeTargetNone()
		}
		return m.ComposeTargetCustom(len(ids))
	}
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
