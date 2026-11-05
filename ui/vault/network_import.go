package vault

import (
	"net/netip"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/wireguardconf"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// networkImportControls render the .conf entry point above the manual form.
// Choosing a file only opens a review; the editor form is not touched until
// the user confirms the parsed result.
func networkImportControls(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.VaultNetworkImport()).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultNetworkImportChooseFile()).Clicked() {
			s.ChooseNetworkConfFile()
			c.Invalidate()
		}
	})
	ui.Text(c, m.VaultNetworkImportHint()).FontSize(12).TextColor(t.TextMuted)
	if parseErr := s.NetworkImportError(); parseErr != nil {
		ui.Text(c, m.VaultNetworkImportIssue(parseErr.Line,
			networkImportFieldLabel(m, parseErr.Field),
			networkImportErrorMessage(m, parseErr.Kind))).
			FontSize(12).TextColor(uitheme.DangerText(t))
	}
	if problem := s.NetworkImportProblem(); problem != vaultstate.ProblemNone {
		ui.Text(c, problemText(m, problem)).FontSize(12).TextColor(uitheme.DangerText(t))
	}
}

// networkImportReview shows the normalized configuration and every diagnostic
// before anything reaches the editor form or the Vault.
func networkImportReview(c *ui.Context, s *vaultstate.State,
	preview *vaultstate.NetworkImportPreview, m locales.Messages, t *ui.Theme) {

	mtu := preview.Config.Interface.MTU
	if mtu == 0 {
		mtu = domainvault.DefaultWireGuardMTU
	}
	ui.Text(c, m.VaultNetworkImportResult(preview.FileName)).Bold()
	if value := joinNetworkPrefixes(preview.Config.Interface.Addresses); value != "" {
		ui.Text(c, m.VaultNetworkImportAddresses(value)).FontSize(12)
	}
	if value := joinNetworkAddresses(preview.Config.Interface.DNS); value != "" {
		ui.Text(c, m.VaultNetworkImportDNS(value)).FontSize(12)
	}
	ui.Text(c, m.VaultNetworkImportMTU(int(mtu))).FontSize(12)
	if preview.Config.Interface.ListenPort != 0 {
		ui.Text(c, m.VaultNetworkImportListenPort(int(preview.Config.Interface.ListenPort))).FontSize(12)
	}
	ui.Text(c, m.VaultNetworkImportPeers(len(preview.Config.Peers))).FontSize(12)
	for index, peer := range preview.Config.Peers {
		ui.Column(c).FillWidth().Gap(2).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.VaultNetworkPeerTitle(index+1)).Bold()
			// Public keys and endpoints are not secret. The Preshared Key is
			// only reported as present; its value never reaches this surface.
			ui.Text(c, m.VaultNetworkImportPeerPublicKey(
				wireguardconf.EncodeKey(peer.PublicKey))).FontSize(12)
			if peer.Endpoint.Host != "" && peer.Endpoint.Port != 0 {
				ui.Text(c, m.VaultNetworkImportPeerEndpoint(peer.Endpoint.String())).FontSize(12)
			}
			if allowed := joinNetworkPrefixes(peer.AllowedIPs); allowed != "" {
				ui.Text(c, m.VaultNetworkImportPeerAllowedIPs(allowed)).FontSize(12)
			}
			if peer.PersistentKeepalive != 0 {
				ui.Text(c, m.VaultNetworkImportPeerKeepalive(
					int(peer.PersistentKeepalive))).FontSize(12)
			}
			if peer.PresharedKey != nil {
				ui.Text(c, m.VaultNetworkImportPeerPresharedKeySet()).FontSize(12)
			}
		})
	}

	ui.Text(c, m.VaultNetworkImportDiagnostics()).Bold()
	if len(preview.Diagnostics) == 0 {
		ui.Text(c, m.VaultNetworkImportNoDiagnostics()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, diagnostic := range preview.Diagnostics {
		ui.Text(c, m.VaultNetworkImportIssue(diagnostic.Line,
			networkImportFieldLabel(m, diagnostic.Field),
			networkImportDiagnosticMessage(m, diagnostic.Code))).
			FontSize(12).TextColor(uitheme.WarningText(t))
	}

	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultNetworkImportConfirm()).Clicked() {
			s.ConfirmNetworkImport()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelNetworkImport()
			c.Invalidate()
		}
	})
}

// networkImportFieldLabel renders a diagnostic's field. A file-level problem
// has no key, so it uses the localized whole-file label.
func networkImportFieldLabel(m locales.Messages, field string) string {
	if strings.TrimSpace(field) == "" {
		return m.VaultNetworkImportWholeFile()
	}
	return field
}

// networkImportErrorMessage localizes a hard parser failure. The parser's own
// message is never shown: it is not localized and must not expose a value.
func networkImportErrorMessage(m locales.Messages, kind wireguardconf.Kind) string {
	switch kind {
	case wireguardconf.ErrSyntax:
		return m.VaultNetworkImportErrSyntax()
	case wireguardconf.ErrUnknownKey:
		return m.VaultNetworkImportErrUnknownKey()
	case wireguardconf.ErrUnknownSection:
		return m.VaultNetworkImportErrUnknownSection()
	case wireguardconf.ErrMissingRequired:
		return m.VaultNetworkImportErrMissingRequired()
	case wireguardconf.ErrDuplicatePeer:
		return m.VaultNetworkImportErrDuplicatePeer()
	case wireguardconf.ErrUnsupported:
		return m.VaultNetworkImportErrUnsupported()
	case wireguardconf.ErrLimit:
		return m.VaultNetworkImportErrLimit()
	case wireguardconf.ErrEncoding:
		return m.VaultNetworkImportErrEncoding()
	default:
		return m.VaultNetworkImportErrSyntax()
	}
}

// networkImportDiagnosticMessage localizes a non-blocking parser finding.
func networkImportDiagnosticMessage(m locales.Messages, code wireguardconf.DiagnosticCode) string {
	switch code {
	case wireguardconf.WarnIgnoredField:
		return m.VaultNetworkImportWarnIgnoredField()
	case wireguardconf.WarnSearchDomain:
		return m.VaultNetworkImportWarnSearchDomain()
	case wireguardconf.WarnHostBits:
		return m.VaultNetworkImportWarnHostBits()
	case wireguardconf.WarnEmptyAllowedIPs:
		return m.VaultNetworkImportWarnEmptyAllowedIPs()
	case wireguardconf.WarnDuplicateScalar:
		return m.VaultNetworkImportWarnDuplicateScalar()
	case wireguardconf.WarnEmptyHook:
		return m.VaultNetworkImportWarnEmptyHook()
	default:
		return m.VaultNetworkImportWarnIgnoredField()
	}
}

func joinNetworkPrefixes(prefixes []netip.Prefix) string {
	values := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		values = append(values, prefix.String())
	}
	return strings.Join(values, ", ")
}

func joinNetworkAddresses(addresses []netip.Addr) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		values = append(values, address.String())
	}
	return strings.Join(values, ", ")
}
