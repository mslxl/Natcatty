package shell

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// The quick-connect card's geometry. The card keeps a bounded height and
// scrolls its fields, so the form and its buttons stay inside the 720x480
// minimum window.
const (
	quickConnectWidth     float32 = 400
	quickConnectMaxHeight float32 = 560
	quickConnectPadding   float32 = 20
	quickConnectGap       float32 = 12
	quickConnectMinTop    float32 = 16
)

// quickConnectButton is the tab bar's entry to the quick-connect dialog.
func quickConnectButton(c *ui.Context, s *state.State, m locales.Messages) {
	button := kit.IconButtonWith(c, icons.Zap, m.MenuQuickConnect(), kit.IconOptions{
		Role: kit.IconTab,
		Key:  "quick-connect",
	}).Margin(0, 0, 4, 4)
	if button.Clicked() {
		s.OpenQuickConnect()
		c.Invalidate()
	}
}

// quickConnectView draws the quick-connect dialog and, when a partly filled
// form is about to be discarded, its confirmation. It is a no-op while the
// dialog is closed, so the shell can call it unconditionally.
//
// It uses Palette rather than Dialog because the dismissal rule differs: an
// outside click must keep a partly filled form, while Escape asks before
// discarding it. Palette's backdrop ignores outside clicks and reports Escape.
func quickConnectView(c *ui.Context, s *state.State, m locales.Messages) {
	if !s.QuickConnectOpen() {
		return
	}
	t := c.Theme()
	_, windowHeight := c.Size()
	height := min(windowHeight-32, quickConnectMaxHeight)
	top := max(quickConnectMinTop, (windowHeight-height)/2)
	closed := kit.Palette(c, kit.PaletteOptions{
		Width:   quickConnectWidth,
		Height:  height,
		Top:     top,
		Padding: quickConnectPadding,
		Gap:     quickConnectGap,
	}, func() { quickConnectForm(c, s, m, t) })
	if closed {
		s.RequestQuickConnectClose()
		c.Invalidate()
	}

	if s.QuickConnectConfirmingDiscard() {
		confirmed, cancelled := kit.Confirm(c, true,
			m.QuickConnectDiscardTitle(), m.QuickConnectDiscardDescription(),
			m.QuickConnectDiscard(), m.QuickConnectKeepEditing())
		switch {
		case confirmed:
			s.DiscardQuickConnect()
			c.Invalidate()
		case cancelled:
			s.KeepQuickConnectEditing()
			c.Invalidate()
		}
	}
}

// quickConnectForm lays out the fields in a scroll and keeps the buttons
// below it, so every field stays reachable at the minimum window. The scroll
// reserves the overlay scroll bar's gutter, so the fields never run under it.
func quickConnectForm(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Fill().Children(func() {
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).PaddingEnd(quickConnectScrollbarGutter(t)).
			Children(func() {
				ui.Column(c).FillWidth().Gap(quickConnectGap).Children(func() {
					quickConnectFields(c, s, m, t)
				})
			})
		ui.Row(c).FillWidth().Gap(8).Justify(ui.End).Children(func() {
			if kit.PrimaryButton(c, "", m.VaultHostConnect()).Clicked() {
				_ = s.ConnectQuickConnect()
				c.Invalidate()
			}
			if kit.SecondaryButton(c, "", m.VaultHostCancel()).Clicked() {
				s.RequestQuickConnectClose()
				c.Invalidate()
			}
		})
	})
}

// quickConnectScrollbarGutter is the width of the overlay scroll bar's track:
// the thumb (t.ScrollbarWidth DIPs, 6 when unset) plus the 6 DIPs it is inset
// and padded by in MyGo's scroll geometry. Reserving it keeps the fields from
// running under the bar.
func quickConnectScrollbarGutter(t *ui.Theme) float32 {
	width := t.ScrollbarWidth
	if width <= 0 {
		width = 6
	}
	return width + 6
}

func quickConnectFields(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	form := s.QuickConnectForm()
	ui.Text(c, m.MenuQuickConnect()).FontSize(kit.M(c).Font.Title).Bold()
	ui.Text(c, m.QuickConnectDescription()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)

	protocolField(c, s, m)
	switch form.Protocol {
	case domainvault.ProtocolSSH:
		transportField(c, s, m)
	case domainvault.ProtocolVNC:
		vncTransportField(c, s, m)
	}
	// Every field carries its label in a FormRow, the same as the New Host
	// editor: a TextInput label alone is not drawn, which left the address,
	// port, and credentials as unlabeled boxes.
	kit.FormRow(c, m.QuickConnectAddress(), func() {
		ui.TextInput(c.Key("quick-connect-address"), &form.Hostname).
			Placeholder(m.QuickConnectAddressPlaceholder()).FillWidth().AutoFocus()
	}).FillWidth()
	if form.Protocol == domainvault.ProtocolVNC {
		vncEndpointFields(c, s, m)
	} else {
		kit.FormRow(c, m.QuickConnectPort(), func() {
			ui.TextInput(c.Key("quick-connect-port"), &form.Port).FillWidth()
		}).FillWidth()
	}
	switch form.Protocol {
	case domainvault.ProtocolSSH:
		kit.FormRow(c, m.QuickConnectUsername(), func() {
			ui.TextInput(c.Key("quick-connect-username"), &form.Username).FillWidth()
		}).FillWidth()
		kit.FormRow(c, m.QuickConnectPassword(), func() {
			ui.TextInput(c.Key("quick-connect-password"), &form.Password).Password().FillWidth()
		}).FillWidth()
		if form.Transport == domainvault.TransportPlain {
			ui.Checkbox(c.Key("quick-connect-network-device"), &form.NetworkDevice,
				m.VaultHostNetworkDevice())
		}
		quickConnectJumpHosts(c, s, m, t)
	case domainvault.ProtocolVNC:
		kit.FormRow(c, m.QuickConnectUsername(), func() {
			ui.TextInput(c.Key("quick-connect-username"), &form.Username).FillWidth()
		}).FillWidth()
		kit.FormRow(c, m.VaultHostVNCPassword(), func() {
			ui.TextInput(c.Key("quick-connect-vnc-password"), &form.VNCPassword).
				Password().FillWidth()
		}).FillWidth()
	}

	if problem := quickConnectProblem(m, s.QuickConnectIssue()); problem != "" {
		ui.Text(c, problem).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
	}
}

// protocolField switches the form between SSH, Telnet, and VNC. The labels are
// the protocol names, which are not translated.
func protocolField(c *ui.Context, s *state.State, m locales.Messages) {
	labels := []string{
		string(domainvault.ProtocolSSH),
		string(domainvault.ProtocolTelnet),
		string(domainvault.ProtocolVNC),
	}
	current := string(s.QuickConnectForm().Protocol)
	found := false
	for _, label := range labels {
		if current == label {
			found = true
			break
		}
	}
	if !found {
		current = labels[0]
	}
	kit.FormRow(c, m.QuickConnectProtocol(), func() {
		ui.Select(c.Key("quick-connect-protocol"), &current, labels).FillWidth()
	}).FillWidth()
	if current != string(s.QuickConnectForm().Protocol) {
		s.SetQuickConnectProtocol(domainvault.Protocol(current))
		c.Invalidate()
	}
}

// transportField switches an SSH form between plain SSH, Mosh, and ET. Mosh and
// ET are SSH transports, so they keep the same Username and Password fields.
func transportField(c *ui.Context, s *state.State, m locales.Messages) {
	labels := []string{
		m.VaultHostTransportSSH(),
		m.VaultHostTransportMosh(),
		m.VaultHostTransportET(),
	}
	values := []string{
		string(domainvault.TransportPlain),
		string(domainvault.TransportMosh),
		string(domainvault.TransportET),
	}
	current := quickConnectOptionLabel(labels, values, string(s.QuickConnectForm().Transport))
	kit.FormRow(c, m.VaultHostTransport(), func() {
		ui.Select(c.Key("quick-connect-transport"), &current, labels).FillWidth()
	}).FillWidth()
	transport := domainvault.Transport(quickConnectOptionValue(values, labels, current))
	if transport != s.QuickConnectForm().Transport {
		s.SetQuickConnectTransport(transport)
		c.Invalidate()
	}
}

// vncTransportField chooses a VNC form's transport. A raw RFB endpoint is a
// display or port; a noVNC/websockify endpoint is an HTTP service, so the
// choice decides what the Port field means and which details follow.
func vncTransportField(c *ui.Context, s *state.State, m locales.Messages) {
	labels := []string{
		m.VaultHostVNCTransportTCP(),
		m.VaultHostVNCWebSocket(),
	}
	current := labels[0]
	if s.QuickConnectForm().VNCWebSocket {
		current = labels[1]
	}
	kit.FormRow(c, m.VaultHostTransport(), func() {
		ui.Select(c.Key("quick-connect-vnc-transport"), &current, labels).FillWidth()
	}).FillWidth()
	if websocket := current == labels[1]; websocket != s.QuickConnectForm().VNCWebSocket {
		s.SetQuickConnectVNCWebSocket(websocket)
		c.Invalidate()
	}
}

// vncEndpointFields renders the VNC endpoint as a display number or a raw TCP
// port. Both modes describe the same stored port.
func vncEndpointFields(c *ui.Context, s *state.State, m locales.Messages) {
	form := s.QuickConnectForm()
	if form.VNCWebSocket {
		// A noVNC/websockify endpoint is an HTTP port; the display-number
		// toggle cannot describe it.
		kit.FormRow(c, m.QuickConnectPort(), func() {
			ui.TextInput(c.Key("quick-connect-vnc-port"), &form.Port).
				Placeholder(strconv.Itoa(domainvault.VNCDefaultWebSocketPort)).FillWidth()
		}).FillWidth()
		kit.FormRow(c, m.VaultHostVNCWebSocketPath(), func() {
			ui.TextInput(c.Key("quick-connect-vnc-websocket-path"), &form.VNCWebSocketPath).
				Placeholder(domainvault.VNCDefaultWebSocketPath).FillWidth()
		}).FillWidth()
		ui.Checkbox(c.Key("quick-connect-vnc-websocket-secure"), &form.VNCWebSocketSecure,
			m.VaultHostVNCWebSocketSecure())
		return
	}
	inputLabels := []string{m.VaultHostVNCInputDisplay(), m.VaultHostVNCInputPort()}
	inputModes := []string{
		string(domainvault.VNCDisplayNumber),
		string(domainvault.VNCPort),
	}
	inputLabel := quickConnectOptionLabel(inputLabels, inputModes,
		string(domainvault.NormalizeDisplayMode(form.VNCMode)))
	kit.FormRow(c, m.VaultHostVNCInputMode(), func() {
		ui.Select(c.Key("quick-connect-vnc-mode"), &inputLabel, inputLabels).FillWidth()
	}).FillWidth()
	mode := domainvault.VNCDisplayMode(quickConnectOptionValue(inputModes, inputLabels, inputLabel))
	if mode != domainvault.NormalizeDisplayMode(form.VNCMode) {
		s.SetQuickConnectVNCMode(mode)
		c.Invalidate()
	}

	if domainvault.NormalizeDisplayMode(form.VNCMode) == domainvault.VNCPort {
		kit.FormRow(c, m.QuickConnectPort(), func() {
			ui.TextInput(c.Key("quick-connect-vnc-port"), &form.Port).FillWidth()
		}).FillWidth()
		return
	}
	kit.FormRow(c, m.VaultHostVNCDisplay(), func() {
		ui.TextInput(c.Key("quick-connect-vnc-display"), &form.VNCDisplay).FillWidth()
	}).FillWidth()
}

// quickConnectJumpHosts renders the ad-hoc jump chain: each hop in order
// (first closest to the client), its temporary endpoint inputs, the
// reorder/remove controls, and the two ways to add a hop — borrowing a saved
// Vault Host from the menu, or typing a temporary one. A Mosh transport cannot
// carry a chain, so the section explains the refusal instead of hiding it.
func quickConnectJumpHosts(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	form := s.QuickConnectForm()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, m.QuickConnectJumpHosts()).FontSize(kit.M(c).Font.Callout).Bold()
		ui.Text(c, m.QuickConnectJumpHostsHint()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)

		mosh := domainvault.NormalizeTransport(form.Protocol, form.Transport) == domainvault.TransportMosh
		if mosh {
			ui.Text(c, m.QuickConnectJumpMoshUnsupported()).FontSize(kit.M(c).Font.Footnote).
				TextColor(uitheme.DangerText(t))
		}

		// A click mutates the chain mid-frame while later rows still draw, so
		// the mutation is deferred past the draw loop. The rows draw from a
		// snapshot copy: even after the slice shrinks, the text inputs keep a
		// stable row for the rest of the frame.
		type jumpEdit struct {
			index int
			delta int
			drop  bool
		}
		var edit *jumpEdit
		type jumpRow struct {
			jump  state.QuickConnectJump
			index int
		}
		rows := make([]jumpRow, len(form.Jumps))
		for index := range form.Jumps {
			rows[index] = jumpRow{jump: form.Jumps[index], index: index}
		}
		for _, row := range rows {
			index := row.index
			jump := row.jump
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, strconv.Itoa(index+1)+".").FontSize(kit.M(c).Font.Footnote).
					TextColor(t.TextMuted).Shrink(0)
				if jump.Kind == state.QuickConnectJumpSaved {
					ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(4).Children(func() {
						kit.Icon(c, icons.Server)
						ui.Text(c, jump.Label).SingleLine()
					})
				} else {
					ui.Column(c).Grow(1).Gap(4).Children(func() {
						ui.Row(c).FillWidth().Gap(4).Children(func() {
							ui.TextInput(c.Key("quick-connect-jump-host-"+strconv.Itoa(index)), &form.Jumps[index].Hostname).
								Placeholder(m.QuickConnectAddressPlaceholder()).Grow(1)
							ui.TextInput(c.Key("quick-connect-jump-port-"+strconv.Itoa(index)), &form.Jumps[index].Port).
								Placeholder("22").Width(64).Shrink(0)
						})
						ui.Row(c).FillWidth().Gap(4).Children(func() {
							ui.TextInput(c.Key("quick-connect-jump-user-"+strconv.Itoa(index)), &form.Jumps[index].Username).
								Placeholder(m.QuickConnectUsername()).Grow(1)
							ui.TextInput(c.Key("quick-connect-jump-pass-"+strconv.Itoa(index)), &form.Jumps[index].Password).
								Placeholder(m.QuickConnectPassword()).Password().Grow(1)
						})
					})
				}
				ui.Row(c).Shrink(0).Gap(2).AlignItems(ui.Center).Children(func() {
					// Each control names its hop, so assistive technology — and
					// a row of identical arrows — can tell them apart.
					hop := strconv.Itoa(index + 1)
					up := kit.IconButton(c, icons.ArrowUp, m.QuickConnectJumpMoveUp()+" "+hop)
					if index == 0 {
						up.Disabled(true)
					}
					if up.Clicked() {
						edit = &jumpEdit{index: index, delta: -1}
						c.Invalidate()
					}
					down := kit.IconButton(c, icons.ArrowDown, m.QuickConnectJumpMoveDown()+" "+hop)
					if index == len(rows)-1 {
						down.Disabled(true)
					}
					if down.Clicked() {
						edit = &jumpEdit{index: index, delta: 1}
						c.Invalidate()
					}
					if kit.IconButton(c, icons.X, m.QuickConnectJumpRemove()+" "+hop).Clicked() {
						edit = &jumpEdit{index: index, drop: true}
						c.Invalidate()
					}
				})
			})
		}
		// The chain edits after every row has drawn, so no input ever binds to
		// a row the slice no longer has.
		if edit != nil {
			if edit.drop {
				s.QuickConnectRemoveJump(edit.index)
			} else {
				s.QuickConnectMoveJump(edit.index, edit.delta)
			}
		}

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			hosts := s.QuickConnectJumpHosts()
			add := kit.SecondaryButton(c, icons.Plus, m.QuickConnectAddSavedJump())
			if len(hosts) == 0 {
				add.Disabled(true)
			}
			add.Menu(func(menu *ui.Menu) {
				for _, host := range hosts {
					label := host.Label
					if label == "" {
						label = host.Hostname
					}
					if menu.Item(label).Chosen() {
						s.QuickConnectAddSavedJump(host.ID)
						c.Invalidate()
					}
				}
			})
			if kit.SecondaryButton(c, icons.Plus, m.QuickConnectAddTemporaryJump()).Clicked() {
				s.QuickConnectAddTemporaryJump()
				c.Invalidate()
			}
		})
	})
}

// quickConnectOptionLabel returns the label for current, or the first label
// when current is not one of the options.
func quickConnectOptionLabel(labels, values []string, current string) string {
	for index, candidate := range values {
		if candidate == current {
			return labels[index]
		}
	}
	return labels[0]
}

// quickConnectOptionValue returns the value for label, or the first value when
// label is not one of the options.
func quickConnectOptionValue(values, labels []string, label string) string {
	for index, candidate := range labels {
		if candidate == label {
			return values[index]
		}
	}
	return values[0]
}

// quickConnectProblem localizes the validation issue, if any.
func quickConnectProblem(m locales.Messages, issue state.QuickConnectIssue) string {
	switch issue {
	case state.QuickConnectIssueAddress:
		return m.QuickConnectAddressRequired()
	case state.QuickConnectIssuePort:
		return m.QuickConnectPortInvalid()
	case state.QuickConnectIssueDisplay:
		return m.QuickConnectDisplayInvalid()
	case state.QuickConnectIssueWebSocketPath:
		return m.VaultProblemVncWebSocketPath()
	case state.QuickConnectIssueNoSessions:
		return m.QuickConnectUnavailable()
	case state.QuickConnectIssueJump:
		return m.QuickConnectJumpInvalid()
	case state.QuickConnectIssueJumpTransport:
		return m.QuickConnectJumpMoshUnsupported()
	default:
		return ""
	}
}
