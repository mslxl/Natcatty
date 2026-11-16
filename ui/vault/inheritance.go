package vault

import (
	"strings"

	"github.com/egoist/mygo/ui"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// hostProxyProfileSourceLine says whether the Host editor's effective Proxy
// Profile belongs to the Host or is inherited from its Host Group. A group
// default is labelled inherited so it is not mistaken for the Host's own
// selection; a Host-level value clears the label.
func hostProxyProfileSourceLine(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	source, path, id := s.HostProxyProfileSource()
	switch source {
	case vaultstate.InheritedValueSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHost()).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	case vaultstate.InheritedValueSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, s.ProxyProfileLabel(id))).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}
}

// groupProxyProfileSourceLine says whether the Host Group editor's effective
// Proxy Profile is the group's own or an ancestor's default.
func groupProxyProfileSourceLine(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	source, path, id := s.GroupProxyProfileSource()
	switch source {
	case vaultstate.InheritedValueSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHostGroup()).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	case vaultstate.InheritedValueSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, s.ProxyProfileLabel(id))).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}
}

// hostX11SourceLine labels an X11 preference that the Host editor shows but
// the Host does not own: the checkbox reflects the Host Group's default, so
// the user can tell an inherited value from a Host-level one.
func hostX11SourceLine(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	source, path, _ := s.HostX11Source()
	if source != vaultstate.InheritedValueSourceInherited {
		return
	}
	ui.Text(c, m.VaultX11Inherited(path)).
		FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
}

// hostJumpHostsField renders the effective jump chain read-only. A Host does
// not edit routing here: its own chain wins whole, otherwise the Host Group's
// default applies, and editing stays on that group.
func hostJumpHostsField(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, protocol string) {
	source, path, ids := s.HostJumpChainSource()
	labels := strings.Join(s.JumpHostLabels(ids), ", ")
	if labels == "" && protocol != string(domainvault.ProtocolSSH) {
		// Only an SSH session can carry a jump chain, so another protocol
		// shows the row only when a chain is actually resolved for it.
		return
	}
	kit.FormRow(c, m.VaultHostJumpHosts(), func() {
		if labels == "" {
			ui.Text(c, m.VaultNone())
			return
		}
		ui.Text(c, labels)
	}).Description(m.VaultHostJumpHostsHint())
	switch source {
	case vaultstate.InheritedValueSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHost()).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	case vaultstate.InheritedValueSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, labels)).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}
}

// groupJumpHostsField renders the Host Group's jump-chain default. Member
// Hosts with no chain of their own use these hops, first hop closest to the
// client.
func groupJumpHostsField(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	candidates := s.JumpHostCandidates()
	kit.FormRow(c, m.VaultHostJumpHosts(), func() {
		if len(candidates) == 0 {
			ui.Text(c, m.VaultNone())
			return
		}
		for _, host := range candidates {
			selected := s.GroupJumpHostSelected(host.ID)
			if ui.Checkbox(c, &selected, host.Label).Changed() {
				s.ToggleGroupJumpHost(host.ID)
				c.Invalidate()
			}
		}
	}).Description(m.VaultGroupJumpHostsHint())
	source, path, ids := s.GroupJumpChainSource()
	switch source {
	case vaultstate.InheritedValueSourceRecord:
		ui.Text(c, m.VaultNetworkProfileSourceHostGroup()).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	case vaultstate.InheritedValueSourceInherited:
		ui.Text(c, m.VaultNetworkProfileInherited(path, strings.Join(s.JumpHostLabels(ids), ", "))).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}
}
