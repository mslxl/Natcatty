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
	default:
		return ""
	}
}
