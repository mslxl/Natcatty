package forwarding

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/ui/locales"
)

// View returns a standalone forwarding surface. The Vault embeds Section
// instead; this entry point keeps the surface testable in isolation.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		if s.Editing() {
			Editor(c, s)
			return
		}
		Section(c, s)
	}
}

// Section renders the Port Forwarding Rule list inside the Vault's scroll.
func Section(c *ui.Context, s *State) {
	s.Flush()
	m := locales.In(c.Locale())
	t := c.Theme()

	ui.Row(c).Fill().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultForwardingTitle(s.RuleCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultForwardingNew()).Clicked() {
			s.BeginCreate()
			c.Invalidate()
		}
	})

	if pending := s.PendingDelete(); pending != "" {
		message := m.VaultForwardingDeleteConfirmUnknown()
		for _, view := range s.Items() {
			if view.Rule.ID == pending {
				message = m.VaultForwardingDeleteConfirm(view.Rule.Label)
				break
			}
		}
		ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, message).Grow(1).TextColor(t.Danger)
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelDelete()
				c.Invalidate()
			}
			if ui.Button(c, m.VaultHostDelete()).Clicked() {
				_ = s.ConfirmDelete()
				c.Invalidate()
			}
		})
	}

	if s.Problem() != ProblemNone {
		if text := problemText(m, s.Problem()); text != "" {
			ui.Text(c, text).TextColor(t.Danger)
		}
	}

	if s.RuleCount() == 0 {
		ui.Text(c, m.VaultForwardingEmpty()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	for _, view := range s.Items() {
		ruleRow(c, s, view, m, t)
	}
}

func ruleRow(c *ui.Context, s *State, view appforwarding.View, m locales.Messages, t *ui.Theme) {
	rule := view.Rule
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, rule.Label).Bold()
			ui.Text(c, ruleSummary(s, rule, m)).FontSize(12).TextColor(t.TextMuted)
			if text, danger := statusText(m, view); text != "" {
				color := t.TextMuted
				if danger {
					color = t.Danger
				}
				ui.Text(c, text).FontSize(12).TextColor(color)
			}
		})

		switch view.Status {
		case domainforward.StatusActive, domainforward.StatusConnecting:
			if ui.Button(c, m.VaultForwardingStop()).Clicked() {
				s.Stop(rule.ID)
				c.Invalidate()
			}
		default:
			if ui.Button(c, m.VaultForwardingStart()).Clicked() {
				s.Start(rule.ID)
				c.Invalidate()
			}
		}
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEdit(rule.ID)
			c.Invalidate()
		}
		enabledLabel := m.VaultForwardingDisable()
		if !rule.IsEnabled() {
			enabledLabel = m.VaultForwardingEnable()
		}
		if ui.Button(c, enabledLabel).Clicked() {
			s.ToggleEnabled(rule.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDelete(rule.ID)
			c.Invalidate()
		}
	})
}

// statusText returns the localized runtime status and whether it is a
// failure. A runtime failure includes the failing hop when the connector
// reported one.
func statusText(m locales.Messages, view appforwarding.View) (string, bool) {
	switch view.Status {
	case domainforward.StatusActive:
		return m.VaultForwardingStatusActive(), false
	case domainforward.StatusConnecting:
		return m.VaultForwardingStatusConnecting(), false
	case domainforward.StatusInactive:
		return m.VaultForwardingStatusInactive(), false
	case domainforward.StatusError:
		parts := []string{m.VaultForwardingStatusError()}
		if text := runtimeErrorText(m, view.Error); text != "" {
			parts = append(parts, text)
		}
		if view.Hop != nil {
			parts = append(parts, m.VaultForwardingHop(view.Hop.Index, view.Hop.Total, view.Hop.Label))
		}
		return strings.Join(parts, " · "), true
	case domainforward.StatusUnknown:
		return m.VaultForwardingStatusUnknown(), false
	default:
		return "", false
	}
}

func runtimeErrorText(m locales.Messages, kind appforwarding.ErrorKind) string {
	switch kind {
	case appforwarding.ErrorBindConflict:
		return m.VaultForwardingErrorBindConflict()
	case appforwarding.ErrorAuth:
		return m.VaultForwardingErrorAuth()
	case appforwarding.ErrorHostKey:
		return m.VaultForwardingErrorHostKey()
	case appforwarding.ErrorNetwork:
		return m.VaultForwardingErrorNetwork()
	case appforwarding.ErrorTimeout:
		return m.VaultForwardingErrorTimeout()
	case appforwarding.ErrorUnsupported:
		return m.VaultForwardingErrorUnsupported()
	case appforwarding.ErrorCancelled:
		return m.VaultForwardingErrorCancelled()
	case appforwarding.ErrorUnforward:
		return m.VaultForwardingErrorUnforward()
	case appforwarding.ErrorInternal:
		return m.VaultForwardingErrorInternal()
	default:
		return ""
	}
}

func ruleSummary(s *State, rule domainforward.Rule, m locales.Messages) string {
	parts := []string{
		typeLabel(m, rule.Type),
		s.HostLabel(rule.HostID),
		net.JoinHostPort(rule.BindAddress, strconv.Itoa(rule.LocalPort)),
	}
	if rule.Type != domainforward.TypeDynamic {
		parts = append(parts, "→ "+net.JoinHostPort(rule.RemoteHost, strconv.Itoa(rule.RemotePort)))
	}
	return strings.Join(parts, " · ")
}

func typeLabel(m locales.Messages, kind domainforward.Type) string {
	switch kind {
	case domainforward.TypeRemote:
		return m.VaultForwardingTypeRemote()
	case domainforward.TypeDynamic:
		return m.VaultForwardingTypeDynamic()
	default:
		return m.VaultForwardingTypeLocal()
	}
}

// Editor renders the create or edit form.
func Editor(c *ui.Context, s *State) {
	m := locales.In(c.Locale())
	t := c.Theme()
	form := s.Form()
	title := m.VaultForwardingFormCreateTitle()
	if form.ID != "" {
		title = m.VaultForwardingFormEditTitle()
	}
	ui.Text(c, title).FontSize(20).Bold()

	ui.Form(c, func() {
		ui.Field(c, m.VaultForwardingType(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.Type, string(domainforward.TypeLocal), m.VaultForwardingTypeLocal())
				ui.Radio(c, &form.Type, string(domainforward.TypeRemote), m.VaultForwardingTypeRemote())
				ui.Radio(c, &form.Type, string(domainforward.TypeDynamic), m.VaultForwardingTypeDynamic())
			})
		})
		ui.Field(c, m.VaultForwardingLabel(), func() {
			ui.TextInput(c, &form.Label)
		})
		ui.Field(c, m.VaultForwardingHost(), func() {
			if len(s.Hosts()) == 0 {
				ui.Text(c, m.VaultForwardingHostNone()).FontSize(12).TextColor(t.Danger)
				return
			}
			ui.RadioGroup(c, func() {
				for _, host := range s.Hosts() {
					ui.Radio(c, &form.HostID, string(host.ID), host.Label)
				}
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			ui.Field(c, m.VaultForwardingBindAddress(), func() {
				ui.TextInput(c, &form.BindAddress).Width(200)
			})
			ui.Field(c, m.VaultForwardingLocalPort(), func() {
				ui.TextInput(c, &form.LocalPort).Placeholder("8080").Width(120)
			})
		})
		if form.Type != string(domainforward.TypeDynamic) {
			ui.Row(c).Gap(8).Children(func() {
				ui.Field(c, m.VaultForwardingRemoteHost(), func() {
					ui.TextInput(c, &form.RemoteHost).Placeholder("localhost").Width(220)
				})
				ui.Field(c, m.VaultForwardingRemotePort(), func() {
					ui.TextInput(c, &form.RemotePort).Placeholder("80").Width(120)
				})
			})
		}
		ui.Checkbox(c, &form.AutoStart, m.VaultForwardingAutoStart())
		ui.Field(c, m.VaultForwardingAutoReconnect(), func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.AutoReconnect, AutoReconnectFollow, m.VaultForwardingAutoReconnectFollow())
				ui.Radio(c, &form.AutoReconnect, AutoReconnectOn, m.VaultForwardingAutoReconnectOn())
				ui.Radio(c, &form.AutoReconnect, AutoReconnectOff, m.VaultForwardingAutoReconnectOff())
			})
		})
		ui.Checkbox(c, &form.Enabled, m.VaultForwardingEnabled())
	})

	if s.Problem() != ProblemNone {
		if text := problemText(m, s.Problem()); text != "" {
			ui.Text(c, text).TextColor(t.Danger)
		}
	}

	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultHostSave()).Clicked() {
			_ = s.Save()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelEdit()
			c.Invalidate()
		}
	})
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemLabelRequired:
		return m.VaultForwardingProblemLabelRequired()
	case ProblemLabelInvalid:
		return m.VaultForwardingProblemLabelInvalid()
	case ProblemType:
		return m.VaultForwardingProblemType()
	case ProblemHostRequired:
		return m.VaultForwardingProblemHostRequired()
	case ProblemBindInvalid:
		return m.VaultForwardingProblemBindInvalid()
	case ProblemLocalPortInvalid:
		return m.VaultForwardingProblemLocalPortInvalid()
	case ProblemLocalPortRange:
		return m.VaultForwardingProblemLocalPortRange()
	case ProblemRemoteHostRequired:
		return m.VaultForwardingProblemRemoteHostRequired()
	case ProblemRemoteHostInvalid:
		return m.VaultForwardingProblemRemoteHostInvalid()
	case ProblemRemotePortInvalid:
		return m.VaultForwardingProblemRemotePortInvalid()
	case ProblemRemotePortRange:
		return m.VaultForwardingProblemRemotePortRange()
	case ProblemNotFound:
		return m.VaultForwardingProblemNotFound()
	case ProblemDisabled:
		return m.VaultForwardingProblemDisabled()
	case ProblemInternal:
		return m.VaultForwardingProblemInternal()
	default:
		return ""
	}
}
