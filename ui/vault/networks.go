package vault

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
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
	kit.SectionBar(c, m.VaultNetworkListTitle(s.NetworkProfileCount()), func() {
		// Two Kinds need two create buttons; a single "New" would have to ask
		// for the Kind before the editor can show its fields.
		if kit.SecondaryButton(c, icons.Plus, m.VaultNetworkNewTailscale()).Clicked() {
			s.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
			c.Invalidate()
		}
		if kit.SecondaryButton(c, icons.Plus, m.VaultNetworkNewWireGuard()).Clicked() {
			s.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
			c.Invalidate()
		}
	})
	if s.NetworkProfileCount() == 0 {
		kit.EmptyHint(c, m.VaultNetworkEmpty())
	}
	for _, profile := range s.NetworkProfiles() {
		networkRow(c, s, profile, m, t)
	}
}

func networkRow(c *ui.Context, s *vaultstate.State, profile domainvault.NetworkProfile,
	m locales.Messages, t *ui.Theme) {
	kit.ListRow(c).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, profile.Label).Bold()
			ui.Text(c, networkSummary(m, profile)).FontSize(12).TextColor(t.TextMuted)
			networkProviderStatus(c, s, profile, m, t)
		})
		if kit.IconButton(c, icons.Pencil, m.VaultHostEdit()).Clicked() {
			s.BeginEditNetworkProfile(profile.ID)
			c.Invalidate()
		}
		if kit.IconButton(c, icons.Trash, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteNetworkProfile(profile.ID)
			c.Invalidate()
		}
	})
}

// networkProviderStatus renders the latest non-secret Provider status of one
// Network Profile plus the interactive-login actions. A Network Profile whose
// Kind is Tailscale and whose Provider has never run offers a login button; a
// failed or cancelled one offers Retry.
// The Auth URL is login material, not a stored secret, and stays on this
// surface: it is never written to the connection log.
func networkProviderStatus(c *ui.Context, s *vaultstate.State,
	profile domainvault.NetworkProfile, m locales.Messages, t *ui.Theme) {
	status, ok := s.NetworkStatus(profile.ID)
	if !ok {
		if profile.Kind == domainvault.NetworkKindTailscale {
			if ui.Button(c, m.VaultNetworkLoginStart()).Clicked() {
				s.StartNetworkProviderLogin(profile.ID)
				c.Invalidate()
			}
		}
		return
	}
	if phase := uiconnection.ProviderPhaseText(m, status.Phase); phase != "" {
		text := phase
		if address := status.Address(); address != "" {
			text = m.VaultNetworkProviderStatus(phase, address)
		}
		ui.Text(c, text).FontSize(12).TextColor(t.TextMuted)
	}
	// A WireGuard Profile reports its live tunnel state: whether a handshake
	// completed, when, and the summed counters. None of it is secret.
	if status.Kind == string(domainvault.NetworkKindWireGuard) {
		ui.Text(c, uiconnection.WireGuardDetail(m, status.HandshakeEstablished,
			status.LastHandshake, status.RxBytes, status.TxBytes)).
			FontSize(12).TextColor(t.TextMuted)
	}
	if status.AuthURL != "" {
		ui.Text(c, m.VaultNetworkLoginRequired(status.AuthURL)).
			FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(6).Wrap().Children(func() {
			if ui.Button(c, m.VaultNetworkLoginCopy()).Clicked() {
				mygo.Clipboard.WriteText(status.AuthURL)
			}
			if ui.Button(c, m.VaultNetworkLoginOpen()).Clicked() {
				_ = mygo.Shell.OpenExternal(status.AuthURL)
			}
		})
	}
	if status.LastError != "" {
		ui.Text(c, m.VaultNetworkProviderStatusError(status.LastError)).
			FontSize(12).TextColor(uitheme.DangerText(t))
	}
	ui.Row(c).Gap(6).Wrap().Children(func() {
		switch status.Phase {
		case appconnection.ProviderPhaseStarting, appconnection.ProviderPhaseNeedsLogin:
			if ui.Button(c, m.VaultNetworkLoginCancel()).Clicked() {
				s.CancelNetworkProviderLogin(profile.ID)
				c.Invalidate()
			}
		case appconnection.ProviderPhaseError, appconnection.ProviderPhaseStopped:
			if ui.Button(c, m.VaultNetworkLoginRetry()).Clicked() {
				s.RetryNetworkProvider(profile.ID)
				c.Invalidate()
			}
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
	id := s.PendingDeleteNetworkProfile()
	if id == "" {
		return
	}
	label := ""
	if profile, ok := s.NetworkProfileByID(id); ok {
		label = profile.Label
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	hosts, groups := s.NetworkProfileUsage(id)
	cancelled, confirmed := kit.ConfirmBar(c, message, func() {
		// Deleting clears soft references, so the affected scope must be
		// visible before the user confirms.
		if hosts > 0 || groups > 0 {
			ui.Text(c, m.VaultNetworkDeleteAffected(hosts, groups)).
				FontSize(12).TextColor(t.TextMuted)
		}
		// Deleting closes the Provider. Say so when a Session or lease is
		// using it, because those owners fail on their next I/O and must
		// retry instead of silently falling back.
		if s.NetworkProfileInUse(id) {
			ui.Text(c, m.VaultNetworkDeleteActiveUse()).
				FontSize(12).TextColor(uitheme.DangerText(t))
		}
	}, m.VaultHostCancel(), m.VaultHostDelete())
	if cancelled {
		s.CancelDeleteNetworkProfile()
		c.Invalidate()
	}
	if confirmed {
		_ = s.ConfirmDeleteNetworkProfile()
		c.Invalidate()
	}
}

// hostNetworkProfileField renders the Host editor's Network Profile selector
// and the line that says whether the effective value is the Host's own or a
// Host Group default.
func hostNetworkProfileField(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.Form()
	ui.Field(c, m.VaultNetworkProfile(), func() {
		ui.RadioGroup(c, func() {
			ui.Radio(c, &form.NetworkProfileID, "", m.VaultNone())
			for _, profile := range s.NetworkProfiles() {
				ui.Radio(c, &form.NetworkProfileID, profile.ID, profile.Label)
			}
		})
	}).Description(m.VaultHostNetworkProfileHint())
	source, path, id := s.HostNetworkProfileSource()
	switch source {
	case vaultstate.NetworkProfileSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHost()).
			FontSize(12).TextColor(t.TextMuted)
	case vaultstate.NetworkProfileSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, s.NetworkProfileLabel(id))).
			FontSize(12).TextColor(t.TextMuted)
	}
}

// groupNetworkProfileField renders the Host Group editor's Network Profile
// default and says whether it belongs to this group or a parent group.
func groupNetworkProfileField(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	form := s.GroupForm()
	ui.Field(c, m.VaultNetworkProfile(), func() {
		ui.RadioGroup(c, func() {
			ui.Radio(c, &form.NetworkProfileID, "", m.VaultNone())
			for _, profile := range s.NetworkProfiles() {
				ui.Radio(c, &form.NetworkProfileID, profile.ID, profile.Label)
			}
		})
	}).Description(m.VaultGroupNetworkProfileHint())
	source, path, id := s.GroupNetworkProfileSource()
	switch source {
	case vaultstate.NetworkProfileSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHostGroup()).
			FontSize(12).TextColor(t.TextMuted)
	case vaultstate.NetworkProfileSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, s.NetworkProfileLabel(id))).
			FontSize(12).TextColor(t.TextMuted)
	}
}

func networkEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.NetworkProfileForm()
	preview := s.NetworkImportPreview()
	title := m.VaultNetworkFormCreateTitle()
	if form.ID != "" {
		title = m.VaultNetworkFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()
		// A parsed .conf is reviewed on its own surface. Save is not offered
		// until the user confirms it, so an unconfirmed import cannot be
		// persisted by a stray Save click.
		if preview != nil {
			networkImportReview(c, s, preview, m, t)
			return
		}

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
				tailscaleFields(c, s, form, m, t)
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

	// The import review owns its own Confirm and Cancel actions. Save stays
	// hidden until the user confirms, so no unconfirmed parse can be persisted.
	if preview != nil {
		return
	}

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveNetworkProfile() }, s.CancelNetworkProfileEdit, m)
}

func tailscaleFields(c *ui.Context, s *vaultstate.State,
	form *vaultstate.NetworkProfileForm, m locales.Messages, t *ui.Theme) {
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
	// tsnet prefers an existing registration in the state directory over a new
	// Auth Key. Say so here so a key that appears to be ignored is explained.
	ui.Text(c, m.VaultNetworkAuthKeyStateHint()).FontSize(12).TextColor(t.TextMuted)

	// A custom Control URL may be a Headscale control plane. Until the
	// Headscale + tsnet matrix in decisions/08 is verified on a real
	// environment, the field is labeled experimental instead of promised.
	ui.Field(c, m.VaultNetworkControlURLExperimental(), func() {
		ui.TextInput(c, &form.ControlURL).Placeholder("https://controlplane.tailscale.com")
	}).Description(m.VaultNetworkControlURLHint())
	ui.Checkbox(c, &form.Ephemeral, m.VaultNetworkEphemeral())
	ui.Field(c, m.VaultNetworkTags(), func() {
		ui.TextInput(c, &form.Tags).Placeholder("tag:prod, tag:ops")
	}).Description(m.VaultNetworkTagsHint())

	// The embedded node's logtail upload cannot be disabled from this build.
	// Say so before the profile is saved rather than implying privacy the
	// Provider does not provide.
	ui.Text(c, m.VaultNetworkPrivacyNotice()).FontSize(12).TextColor(t.TextMuted)

	// A saved Profile can start its node and show the interactive-login state
	// here, so logging in does not require editing a Host first.
	if form.ID != "" {
		if profile, ok := s.NetworkProfileByID(form.ID); ok {
			networkProviderStatus(c, s, profile, m, t)
		}
	}
}

func wireguardFields(c *ui.Context, s *vaultstate.State, form *vaultstate.NetworkProfileForm,
	m locales.Messages, t *ui.Theme) {
	networkImportControls(c, s, m, t)

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
	ui.Field(c, m.VaultNetworkListenPort(), func() {
		ui.TextInput(c, &form.ListenPort).Placeholder("0").Width(120)
	}).Description(m.VaultNetworkListenPortHint())

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
	case domainvault.WarningWireGuardDNSUnavailable:
		return m.VaultNetworkWarningWireGuardDNS()
	default:
		return ""
	}
}
