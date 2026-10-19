// Package vault renders the Vault surface: the Host list, the Host editor,
// and the delete confirmation. It resolves localized text; the state package
// holds behavior and never contains user-visible strings.
package vault

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// View returns the Vault surface.
func View(s *vaultstate.State) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Fill().Grow(1).Gap(12).Children(func() {
			if s.Editing() {
				editor(c, s, m, t)
				return
			}
			list(c, s, m, t)
		})
	}
}

func list(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Children(func() {
		ui.Text(c, m.VaultHostListTitle(s.HostCount())).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		if ui.PrimaryButton(c, m.VaultHostNew()).Clicked() {
			s.BeginCreate()
			c.Invalidate()
		}
	})

	problem(c, s, t)

	if pending := s.PendingDelete(); pending != "" {
		message := m.VaultHostDeleteConfirmUnknown()
		for _, host := range s.Items() {
			if host.ID == pending {
				message = m.VaultHostDeleteConfirm(host.Label)
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

	if s.HostCount() == 0 {
		ui.Text(c, m.VaultHostEmpty()).TextColor(t.TextMuted)
		return
	}

	ui.Scroll(c).Grow(1).Children(func() {
		for _, host := range s.Items() {
			hostRow(c, s, host, m, t)
		}
	})
}

func hostRow(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Fill().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, host.Label).Bold()
			ui.Text(c, hostSummary(host, m)).FontSize(12).TextColor(t.TextMuted)
		})

		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		pin := m.VaultHostPin()
		if host.Pinned {
			pin = m.VaultHostUnpin()
		}
		if ui.Button(c, pin).Clicked() {
			_ = s.TogglePinned(host.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDelete(host.ID)
			c.Invalidate()
		}
	})
}

func hostSummary(host domainvault.Host, m locales.Messages) string {
	parts := make([]string, 0, 5)
	if host.Protocol == domainvault.ProtocolLocal {
		parts = append(parts, m.VaultHostLocal())
	} else {
		parts = append(parts, net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port)))
	}
	if host.Username != "" {
		parts = append(parts, host.Username)
	}
	if host.Group != "" {
		parts = append(parts, host.Group)
	}
	if len(host.Tags) > 0 {
		parts = append(parts, strings.Join(host.Tags, ", "))
	}
	if host.Pinned {
		parts = append(parts, "★")
	}
	return strings.Join(parts, " · ")
}

func editor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.Form()
	title := m.VaultHostFormCreateTitle()
	if form.ID != "" {
		title = m.VaultHostFormEditTitle()
	}

	ui.Text(c, title).FontSize(20).Bold()

	ui.Form(c, func() {
		ui.Field(c, m.VaultHostLabel(), func() {
			ui.TextInput(c, &form.Label)
		})
		ui.Field(c, m.VaultHostHostname(), func() {
			ui.TextInput(c, &form.Hostname).Placeholder("example.com")
		})
		ui.Row(c).Gap(8).Children(func() {
			ui.Field(c, m.VaultHostPort(), func() {
				ui.TextInput(c, &form.Port).Placeholder("22").Width(120)
			})
			ui.Field(c, m.VaultHostUsername(), func() {
				ui.TextInput(c, &form.Username).Placeholder("root").Width(220)
			})
			ui.Field(c, m.VaultHostProtocol(), func() {
				ui.Select(c, &form.Protocol, []string{
					string(domainvault.ProtocolSSH),
					string(domainvault.ProtocolTelnet),
					string(domainvault.ProtocolLocal),
				}).Width(140)
			})
		})
		ui.Field(c, m.VaultHostGroup(), func() {
			ui.TextInput(c, &form.Group).Placeholder("prod/web")
		}).Description(m.VaultHostGroupHint())
		ui.Field(c, m.VaultHostTags(), func() {
			ui.TextInput(c, &form.Tags).Placeholder("prod, web")
		}).Description(m.VaultHostTagsHint())
		ui.Field(c, m.VaultHostNotes(), func() {
			ui.TextArea(c, &form.Notes).Height(80)
		})
		ui.Checkbox(c, &form.Pinned, m.VaultHostPinned())
	})

	ui.Text(c, m.VaultHostCredentialsNote()).FontSize(12).TextColor(t.TextMuted)
	problem(c, s, t)

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

// problem renders the state's problem in the current language. Unknown ids
// render nothing, so the UI never shows an untranslated token.
func problem(c *ui.Context, s *vaultstate.State, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), s.Problem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(t.Danger)
}

func problemText(m locales.Messages, problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemHostnameRequired:
		return m.VaultProblemHostnameRequired()
	case vaultstate.ProblemHostnameInvalid:
		return m.VaultProblemHostnameInvalid()
	case vaultstate.ProblemLabelRequired:
		return m.VaultProblemLabelRequired()
	case vaultstate.ProblemLabelInvalid:
		return m.VaultProblemLabelInvalid()
	case vaultstate.ProblemPortInvalid:
		return m.VaultProblemPortInvalid()
	case vaultstate.ProblemPortRange:
		return m.VaultProblemPortRange()
	case vaultstate.ProblemProtocol:
		return m.VaultProblemProtocol()
	case vaultstate.ProblemUsername:
		return m.VaultProblemUsername()
	case vaultstate.ProblemGroup:
		return m.VaultProblemGroup()
	case vaultstate.ProblemTags:
		return m.VaultProblemTags()
	case vaultstate.ProblemNotes:
		return m.VaultProblemNotes()
	case vaultstate.ProblemDuplicate:
		return m.VaultProblemDuplicate()
	case vaultstate.ProblemNotFound:
		return m.VaultProblemNotFound()
	case vaultstate.ProblemInternal:
		return m.VaultProblemInternal()
	}
	return ""
}
