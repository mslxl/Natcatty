package shell

import (
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
// below it, so every field stays reachable at the minimum window.
func quickConnectForm(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Fill().Children(func() {
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
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

func quickConnectFields(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	form := s.QuickConnectForm()
	ui.Text(c, m.MenuQuickConnect()).FontSize(kit.M(c).Font.Title).Bold()
	ui.Text(c, m.QuickConnectDescription()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)

	protocolField(c, s, m)
	ui.TextInput(c.Key("quick-connect-address"), &form.Hostname).
		Label(m.QuickConnectAddress()).Placeholder(m.QuickConnectAddressPlaceholder()).
		FillWidth().AutoFocus()
	ui.TextInput(c.Key("quick-connect-port"), &form.Port).
		Label(m.QuickConnectPort()).FillWidth()
	if form.Protocol == domainvault.ProtocolSSH {
		ui.TextInput(c.Key("quick-connect-username"), &form.Username).
			Label(m.QuickConnectUsername()).FillWidth()
		ui.TextInput(c.Key("quick-connect-password"), &form.Password).
			Label(m.QuickConnectPassword()).Password().FillWidth()
		ui.Checkbox(c.Key("quick-connect-network-device"), &form.NetworkDevice,
			m.VaultHostNetworkDevice())
	}

	if problem := quickConnectProblem(m, s.QuickConnectIssue()); problem != "" {
		ui.Text(c, problem).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
	}
}

// protocolField switches the form between SSH and Telnet. The labels are the
// protocol names, which are not translated.
func protocolField(c *ui.Context, s *state.State, m locales.Messages) {
	labels := []string{string(domainvault.ProtocolSSH), string(domainvault.ProtocolTelnet)}
	current := string(s.QuickConnectForm().Protocol)
	if current != labels[0] && current != labels[1] {
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

// quickConnectProblem localizes the validation issue, if any.
func quickConnectProblem(m locales.Messages, issue state.QuickConnectIssue) string {
	switch issue {
	case state.QuickConnectIssueAddress:
		return m.QuickConnectAddressRequired()
	case state.QuickConnectIssuePort:
		return m.QuickConnectPortInvalid()
	case state.QuickConnectIssueNoSessions:
		return m.QuickConnectUnavailable()
	default:
		return ""
	}
}
