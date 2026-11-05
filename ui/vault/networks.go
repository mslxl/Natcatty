package vault

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// networkKindLabel names one Network Profile Kind in the user's language.
func networkKindLabel(m locales.Messages, kind domainvault.NetworkProfileKind) string {
	switch kind {
	case domainvault.NetworkKindTailscale:
		return m.VaultNetworkKindTailscale()
	case domainvault.NetworkKindWireGuard:
		return m.VaultNetworkKindWireGuard()
	default:
		return string(kind)
	}
}

func networkSection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultNetworkListTitle(s.NetworkProfileCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		// Two Kinds need two create buttons; a single "New" would have to ask
		// for the Kind before the editor can show its fields.
		if ui.Button(c, m.VaultNetworkNewTailscale()).Clicked() {
			s.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultNetworkNewWireGuard()).Clicked() {
			s.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
			c.Invalidate()
		}
	})
	if s.NetworkProfileCount() == 0 {
		ui.Text(c, m.VaultNetworkEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, profile := range s.NetworkProfiles() {
		networkRow(c, s, profile, m, t)
	}
}

func networkRow(c *ui.Context, s *vaultstate.State, profile domainvault.NetworkProfile,
	m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, profile.Label).Bold()
			ui.Text(c, networkSummary(m, profile)).FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditNetworkProfile(profile.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteNetworkProfile(profile.ID)
			c.Invalidate()
		}
	})
}

// networkSummary names a profile's Kind and the endpoint a list row can show.
// It never includes key material: the profile record does not carry any.
func networkSummary(m locales.Messages, profile domainvault.NetworkProfile) string {
	kind := networkKindLabel(m, profile.Kind)
	var detail string
	switch profile.Kind {
	case domainvault.NetworkKindTailscale:
		if profile.Tailscale != nil {
			detail = profile.Tailscale.Hostname
			if detail == "" {
				detail = profile.Tailscale.ControlURL
			}
		}
	case domainvault.NetworkKindWireGuard:
		if profile.WireGuard != nil {
			detail = strings.Join(profile.WireGuard.Addresses, ", ")
			if detail == "" && len(profile.WireGuard.Peers) > 0 {
				detail = profile.WireGuard.Peers[0].Endpoint
			}
		}
	}
	if detail == "" {
		return kind
	}
	return m.VaultNetworkSummary(kind, detail)
}

func networkDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteNetworkProfile() == "" {
		return
	}
	label := ""
	if profile, ok := s.NetworkProfileByID(s.PendingDeleteNetworkProfile()); ok {
		label = profile.Label
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteNetworkProfile()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteNetworkProfile()
			c.Invalidate()
		}
	})
}

func networkEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.NetworkProfileForm()
	title := m.VaultNetworkFormCreateTitle()
	if form.ID != "" {
		title = m.VaultNetworkFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			if form.ID == "" {
				ui.Field(c, m.VaultNetworkKind(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.Kind, string(domainvault.NetworkKindTailscale),
							m.VaultNetworkKindTailscale())
						ui.Radio(c, &form.Kind, string(domainvault.NetworkKindWireGuard),
							m.VaultNetworkKindWireGuard())
					})
				}).Description(m.VaultNetworkKindHint())
			} else {
				// A profile's Kind decides which secret fields exist, so an
				// edit shows it without offering a change.
				ui.Field(c, m.VaultNetworkKind(), func() {
					ui.Text(c, networkKindLabel(m, domainvault.NetworkProfileKind(form.Kind)))
				})
			}

			// Read the Kind after its selector so switching it swaps the field
			// group in the same frame.
			switch domainvault.NetworkProfileKind(form.Kind) {
			case domainvault.NetworkKindTailscale:
				tailscaleFields(c, form, m)
			case domainvault.NetworkKindWireGuard:
				wireguardFields(c, s, form, m, t)
			}
		})

		// Warnings never block Save; they tell the user before the profile is
		// persisted that a local Control URL is unencrypted.
		for _, warning := range s.NetworkProfileWarnings() {
			ui.Text(c, networkWarningText(m, warning)).FontSize(12).TextColor(uitheme.DangerText(t))
		}
	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveNetworkProfile() }, s.CancelNetworkProfileEdit, m)
}

func tailscaleFields(c *ui.Context, form *vaultstate.NetworkProfileForm, m locales.Messages) {
	ui.Field(c, m.VaultNetworkHostname(), func() {
		ui.TextInput(c, &form.Hostname).Placeholder("netcatty-node")
	}).Description(m.VaultNetworkHostnameHint())

	authKeyHint := m.VaultNetworkAuthKeyHint()
	if form.HasAuthKey {
		authKeyHint = m.VaultNetworkAuthKeySaved()
	}
	ui.Field(c, m.VaultNetworkAuthKey(), func() {
		ui.TextInput(c, &form.AuthKey).Password()
	}).Description(authKeyHint)

	ui.Field(c, m.VaultNetworkControlURL(), func() {
		ui.TextInput(c, &form.ControlURL).Placeholder("https://controlplane.tailscale.com")
	}).Description(m.VaultNetworkControlURLHint())
	ui.Checkbox(c, &form.Ephemeral, m.VaultNetworkEphemeral())
	ui.Field(c, m.VaultNetworkTags(), func() {
		ui.TextInput(c, &form.Tags).Placeholder("tag:prod, tag:ops")
	}).Description(m.VaultNetworkTagsHint())
}

func wireguardFields(c *ui.Context, s *vaultstate.State, form *vaultstate.NetworkProfileForm,
	m locales.Messages, t *ui.Theme) {
	privateKeyHint := m.VaultNetworkPrivateKeyHint()
	if form.HasPrivateKey {
		privateKeyHint = m.VaultNetworkPrivateKeySaved()
	}
	ui.Field(c, m.VaultNetworkPrivateKey(), func() {
		ui.TextInput(c, &form.PrivateKey).Password()
	}).Description(privateKeyHint)

	ui.Field(c, m.VaultNetworkAddresses(), func() {
		ui.TextInput(c, &form.Addresses).Placeholder("10.0.0.2/32, fd00::2/128")
	}).Description(m.VaultNetworkAddressesHint())
	ui.Field(c, m.VaultNetworkDNS(), func() {
		ui.TextInput(c, &form.DNS).Placeholder("10.0.0.1")
	}).Description(m.VaultNetworkDNSHint())
	ui.Field(c, m.VaultNetworkMTU(), func() {
		ui.TextInput(c, &form.MTU).Placeholder(strconv.Itoa(domainvault.DefaultWireGuardMTU)).Width(120)
	}).Description(m.VaultNetworkMTUHint())

	ui.Text(c, m.VaultNetworkPeers()).FontSize(14).Bold()
	for index := range form.Peers {
		peerFields(c, s, form, index, m, t)
	}
	if ui.Button(c, m.VaultNetworkPeerAdd()).Clicked() {
		s.AddNetworkPeer()
		c.Invalidate()
	}
}

func peerFields(c *ui.Context, s *vaultstate.State, form *vaultstate.NetworkProfileForm,
	index int, m locales.Messages, t *ui.Theme) {
	peer := &form.Peers[index]
	ui.Column(c).FillWidth().Gap(6).Padding(8).Background(t.Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.VaultNetworkPeerTitle(index+1)).Bold()
			ui.Column(c).Grow(1)
			if ui.Button(c, m.VaultNetworkPeerRemove()).Clicked() {
				s.RemoveNetworkPeer(index)
				c.Invalidate()
			}
		})
		ui.Row(c).Gap(8).Wrap().Children(func() {
			ui.Field(c, m.VaultNetworkPeerPublicKey(), func() {
				ui.TextInput(c, &peer.PublicKey)
			})
			ui.Field(c, m.VaultNetworkPeerEndpoint(), func() {
				ui.TextInput(c, &peer.Endpoint).Placeholder("vpn.example.com:51820")
			})
		})
		ui.Field(c, m.VaultNetworkPeerAllowedIPs(), func() {
			ui.TextInput(c, &peer.AllowedIPs).Placeholder("10.0.0.0/24, 0.0.0.0/0")
		}).Description(m.VaultNetworkPeerAllowedIPsHint())
		ui.Row(c).Gap(8).Wrap().Children(func() {
			presharedHint := m.VaultNetworkPeerPresharedKeyHint()
			if peer.HasPresharedKey {
				presharedHint = m.VaultNetworkPeerPresharedKeySaved()
			}
			ui.Field(c, m.VaultNetworkPeerPresharedKey(), func() {
				ui.TextInput(c, &peer.PresharedKey).Password()
			}).Description(presharedHint)
			ui.Field(c, m.VaultNetworkPeerKeepalive(), func() {
				ui.TextInput(c, &peer.PersistentKeepalive).Placeholder("25").Width(120)
			})
		})
	})
}

// networkWarningText localizes one non-blocking Network Profile warning.
func networkWarningText(m locales.Messages, warning domainvault.NetworkWarning) string {
	switch warning {
	case domainvault.WarningControlURLInsecure:
		return m.VaultNetworkWarningControlURLInsecure()
	default:
		return ""
	}
}
