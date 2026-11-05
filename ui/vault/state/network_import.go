package state

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/wireguardconf"
)

// ErrNetworkConfTooLarge reports a selected .conf that exceeds the import
// limit. The app's picker returns it after checking the file's size, so an
// oversized file is never read into memory. ChooseNetworkConfFile checks the
// length again for other pickers.
var ErrNetworkConfTooLarge = errors.New("vault: WireGuard .conf exceeds the import limit")

// WireGuardConfPicker opens the native picker and reads one wg-quick `.conf`.
// The app wires a MyGo dialog and the filesystem; tests provide a fake.
type WireGuardConfPicker interface {
	PickWireGuardConf() (name string, content []byte, err error)
}

// WithWireGuardConfPicker gives the Network Profile editor a .conf import
// picker. Without it the editor reports that importing is unavailable.
func WithWireGuardConfPicker(picker WireGuardConfPicker) Option {
	return func(s *State) { s.networkImport.picker = picker }
}

// NetworkImportPreview is one parsed .conf shown for review before the user
// confirms it. It contains key material in memory only; the UI never renders
// the PrivateKey or PresharedKey values.
type NetworkImportPreview struct {
	FileName    string
	Config      wireguardconf.Config
	Diagnostics []wireguardconf.Diagnostic
}

type networkImportState struct {
	picker  WireGuardConfPicker
	preview *NetworkImportPreview
	// parseErr is the typed failure of the last parse, so the editor can show
	// its 1-based line and field. It is nil after a successful choice.
	parseErr *wireguardconf.ParseError
	problem  Problem
}

// resetNetworkImport drops an unconfirmed parse and its problem. The editor's
// form is never touched, so an import that failed or was cancelled cannot
// leave partial data behind.
func (s *State) resetNetworkImport() {
	s.networkImport.preview = nil
	s.networkImport.parseErr = nil
	s.networkImport.problem = ProblemNone
}

// NetworkImportPreview returns the configuration awaiting confirmation, or
// nil when no .conf has been parsed successfully.
func (s *State) NetworkImportPreview() *NetworkImportPreview {
	return s.networkImport.preview
}

// NetworkImportError returns the typed failure of the last parse, or nil.
func (s *State) NetworkImportError() *wireguardconf.ParseError {
	return s.networkImport.parseErr
}

// NetworkImportProblem returns the file-level failure the editor must show.
func (s *State) NetworkImportProblem() Problem { return s.networkImport.problem }

// ChooseNetworkConfFile opens the picker, reads one wg-quick configuration,
// and parses it. A successful parse becomes a preview; nothing is applied to
// the form or saved until ConfirmNetworkImport. A failure leaves the form
// exactly as it was.
func (s *State) ChooseNetworkConfFile() bool {
	if !s.editingNetwork ||
		domainvault.NetworkProfileKind(s.networkForm.Kind) != domainvault.NetworkKindWireGuard {
		return false
	}
	if s.networkImport.picker == nil {
		s.resetNetworkImport()
		s.networkImport.problem = ProblemNetworkImportUnavailable
		s.redraw()
		return false
	}
	name, content, err := s.networkImport.picker.PickWireGuardConf()
	if err != nil {
		s.resetNetworkImport()
		if errors.Is(err, ErrNetworkConfTooLarge) {
			s.networkImport.problem = ProblemNetworkImportTooLarge
		} else {
			s.networkImport.problem = ProblemNetworkImportFile
		}
		s.redraw()
		return false
	}
	if name == "" {
		// The user cancelled the dialog; leave the editor as it is.
		return false
	}
	if len(content) > wireguardconf.MaxFileBytes {
		s.resetNetworkImport()
		s.networkImport.problem = ProblemNetworkImportTooLarge
		s.redraw()
		return false
	}
	config, diagnostics, parseErr := wireguardconf.Parse(content)
	if parseErr != nil {
		s.resetNetworkImport()
		s.networkImport.problem = ProblemNetworkImportParse
		var typed *wireguardconf.ParseError
		if errors.As(parseErr, &typed) {
			s.networkImport.parseErr = typed
		}
		s.redraw()
		return false
	}
	s.networkImport.preview = &NetworkImportPreview{
		FileName:    networkConfBaseName(name),
		Config:      *config,
		Diagnostics: diagnostics,
	}
	s.networkImport.parseErr = nil
	s.networkImport.problem = ProblemNone
	s.problem = ProblemNone
	s.redraw()
	return true
}

// ConfirmNetworkImport applies the reviewed configuration to the editor form.
// It does not persist anything: the user still reviews the filled form and
// presses Save, which validates it again. It reports false when no preview is
// waiting or the editor is not open.
func (s *State) ConfirmNetworkImport() bool {
	preview := s.networkImport.preview
	if preview == nil || !s.editingNetwork {
		return false
	}
	form := &s.networkForm
	form.Kind = string(domainvault.NetworkKindWireGuard)
	form.PrivateKey = wireguardconf.EncodeKey(preview.Config.Interface.PrivateKey)
	form.Addresses = joinPrefixes(preview.Config.Interface.Addresses)
	form.DNS = joinAddresses(preview.Config.Interface.DNS)
	mtu := preview.Config.Interface.MTU
	if mtu == 0 {
		mtu = domainvault.DefaultWireGuardMTU
	}
	form.MTU = strconv.Itoa(int(mtu))
	form.ListenPort = ""
	if preview.Config.Interface.ListenPort != 0 {
		form.ListenPort = strconv.Itoa(int(preview.Config.Interface.ListenPort))
	}
	form.Peers = make([]NetworkPeerForm, 0, len(preview.Config.Peers))
	for _, peer := range preview.Config.Peers {
		row := NetworkPeerForm{
			PublicKey:  wireguardconf.EncodeKey(peer.PublicKey),
			AllowedIPs: joinPrefixes(peer.AllowedIPs),
		}
		// An endpoint is optional in wg-quick. The zero Endpoint must stay an
		// empty form field: rendering it would produce ":0", which the domain
		// rejects as a malformed endpoint.
		if peer.Endpoint.Host != "" && peer.Endpoint.Port != 0 {
			row.Endpoint = peer.Endpoint.String()
		}
		if peer.PresharedKey != nil {
			row.PresharedKey = wireguardconf.EncodeKey(*peer.PresharedKey)
		}
		if peer.PersistentKeepalive != 0 {
			row.PersistentKeepalive = strconv.Itoa(int(peer.PersistentKeepalive))
		}
		form.Peers = append(form.Peers, row)
	}
	s.resetNetworkImport()
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelNetworkImport discards the preview and leaves the editor untouched.
func (s *State) CancelNetworkImport() {
	if s.networkImport.preview == nil && s.networkImport.parseErr == nil &&
		s.networkImport.problem == ProblemNone {
		return
	}
	s.resetNetworkImport()
	s.redraw()
}

func joinPrefixes(prefixes []netip.Prefix) string {
	values := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		values = append(values, prefix.String())
	}
	return strings.Join(values, ", ")
}

func joinAddresses(addresses []netip.Addr) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		values = append(values, address.String())
	}
	return strings.Join(values, ", ")
}

// networkConfBaseName strips directory components for display.
func networkConfBaseName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		return name[index+1:]
	}
	return name
}
