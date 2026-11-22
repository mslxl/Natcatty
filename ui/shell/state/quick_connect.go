package state

import (
	"errors"
	"strconv"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// QuickConnectIssue explains why the quick-connect form cannot connect yet.
type QuickConnectIssue int

const (
	// QuickConnectIssueNone means the form is ready to connect.
	QuickConnectIssueNone QuickConnectIssue = iota
	// QuickConnectIssueAddress means the address is empty.
	QuickConnectIssueAddress
	// QuickConnectIssuePort means the port is not a number between 1 and 65535.
	QuickConnectIssuePort
	// QuickConnectIssueDisplay means the VNC display is not a number between 0
	// and VNCDisplayMax.
	QuickConnectIssueDisplay
	// QuickConnectIssueNoSessions means the shell has no Session service.
	QuickConnectIssueNoSessions
)

// QuickConnectForm is the tab bar's quick-connect dialog. Nothing in it is
// saved to the Vault: connecting opens an Unsaved Host Session.
type QuickConnectForm struct {
	// Protocol is ProtocolSSH, ProtocolTelnet, or ProtocolVNC.
	Protocol domainvault.Protocol
	// Transport is the SSH transport (plain SSH, Mosh, or ET). It applies to
	// SSH only and stays TransportPlain for every other protocol.
	Transport domainvault.Transport
	Hostname  string
	// Port stays text so an incomplete entry can be corrected without the form
	// silently coercing it. It is the SSH/Telnet port and the VNC TCP port when
	// VNCMode is VNCPort.
	Port string
	// Username applies to SSH (including Mosh and ET) and VNC.
	Username string
	// Password is a one-shot SSH credential and never leaves memory.
	Password string
	// VNCMode selects display-number or raw-port VNC input.
	VNCMode domainvault.VNCDisplayMode
	// VNCDisplay is the display number typed in display mode ("1" or ":1").
	VNCDisplay string
	// VNCPassword is a one-shot VNC credential and never leaves memory.
	VNCPassword string
	// NetworkDevice starts the temporary Session in network-device mode. It
	// applies to plain SSH only.
	NetworkDevice bool
}

// quickConnectState is the tab bar's quick-connect dialog state, held on the UI
// thread only.
type quickConnectState struct {
	open bool
	form QuickConnectForm
	// confirmingDiscard asks before closing a partly filled form, so an
	// accidental Escape cannot lose typed details.
	confirmingDiscard bool
	issue             QuickConnectIssue
}

// defaultQuickConnectPort is the port the form starts with for a protocol.
func defaultQuickConnectPort(protocol domainvault.Protocol) string {
	switch protocol {
	case domainvault.ProtocolTelnet:
		return "23"
	case domainvault.ProtocolVNC:
		return strconv.Itoa(domainvault.VNCDisplayBasePort)
	default:
		return "22"
	}
}

// defaultQuickConnectDisplay is the VNC display the form starts with, the
// display whose TCP port is the protocol default.
const defaultQuickConnectDisplay = "0"

// parseQuickConnectDisplay reads a VNC display number typed as "1" or ":1".
func parseQuickConnectDisplay(text string) (int, bool) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, ":")
	if text == "" {
		return 0, false
	}
	display, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return display, true
}

// OpenQuickConnect shows the quick-connect dialog with a fresh form. An open
// dialog keeps what the user already typed.
func (s *State) OpenQuickConnect() {
	if s.quickConnect.open {
		return
	}
	s.quickConnect = quickConnectState{
		open: true,
		form: QuickConnectForm{
			Protocol:   domainvault.ProtocolSSH,
			Port:       defaultQuickConnectPort(domainvault.ProtocolSSH),
			VNCMode:    domainvault.VNCDisplayNumber,
			VNCDisplay: defaultQuickConnectDisplay,
		},
	}
	s.RequestRedraw()
}

// QuickConnectOpen reports whether the dialog is shown.
func (s *State) QuickConnectOpen() bool { return s.quickConnect.open }

// QuickConnectForm returns the dialog's editable form.
func (s *State) QuickConnectForm() *QuickConnectForm { return &s.quickConnect.form }

// QuickConnectConfirmingDiscard reports whether the dialog is asking before it
// closes a partly filled form.
func (s *State) QuickConnectConfirmingDiscard() bool { return s.quickConnect.confirmingDiscard }

// QuickConnectIssue reports why the form cannot connect yet.
func (s *State) QuickConnectIssue() QuickConnectIssue { return s.quickConnect.issue }

// SetQuickConnectProtocol switches the form's protocol. Values whose controls
// the new protocol hides are cleared so a hidden field can never reach the
// Session. The port follows the protocol's default while the user has not typed
// a port of their own.
func (s *State) SetQuickConnectProtocol(protocol domainvault.Protocol) {
	switch protocol {
	case domainvault.ProtocolSSH, domainvault.ProtocolTelnet, domainvault.ProtocolVNC:
	default:
		return
	}
	form := &s.quickConnect.form
	previous := form.Protocol
	if previous == protocol {
		return
	}
	form.Protocol = protocol

	// A transport, network-device mode, and the SSH password belong to SSH;
	// another protocol has no control for them. The username stays because VNC
	// has its own visible Username field.
	if previous == domainvault.ProtocolSSH {
		form.Transport = domainvault.TransportPlain
		form.NetworkDevice = false
		form.Password = ""
	}
	// Leaving VNC hides every VNC control.
	if previous == domainvault.ProtocolVNC {
		form.VNCPassword = ""
		form.VNCMode = domainvault.VNCDisplayNumber
		form.VNCDisplay = ""
	}
	// Telnet shows no credentials or device controls at all.
	if protocol == domainvault.ProtocolTelnet {
		form.Username = ""
		form.Password = ""
		form.NetworkDevice = false
		form.VNCMode = domainvault.VNCDisplayNumber
		form.VNCDisplay = ""
		form.VNCPassword = ""
	}
	// Entering VNC starts from a fresh display endpoint, the same default the
	// New Host editor presents. A port the user typed is kept, like every other
	// protocol switch; display mode ignores the hidden Port and switching to
	// port input derives it from the display, so the two cannot disagree.
	if protocol == domainvault.ProtocolVNC {
		form.VNCMode = domainvault.VNCDisplayNumber
		form.VNCDisplay = defaultQuickConnectDisplay
	}
	if form.Port == "" || form.Port == defaultQuickConnectPort(previous) {
		form.Port = defaultQuickConnectPort(protocol)
	}
}

// SetQuickConnectTransport switches the SSH transport. Mosh and ET reuse the
// SSH credentials; only plain SSH can start in network-device mode, so leaving
// it clears that flag.
func (s *State) SetQuickConnectTransport(transport domainvault.Transport) {
	form := &s.quickConnect.form
	if form.Protocol != domainvault.ProtocolSSH {
		transport = domainvault.TransportPlain
	}
	form.Transport = domainvault.NormalizeTransport(form.Protocol, transport)
	if form.Transport != domainvault.TransportPlain {
		form.NetworkDevice = false
	}
}

// SetQuickConnectVNCMode switches the VNC endpoint between display-number and
// raw-port input, carrying the current endpoint across so the same server stays
// selected.
func (s *State) SetQuickConnectVNCMode(mode domainvault.VNCDisplayMode) {
	form := &s.quickConnect.form
	next := domainvault.NormalizeDisplayMode(mode)
	if next == domainvault.NormalizeDisplayMode(form.VNCMode) {
		return
	}
	switch next {
	case domainvault.VNCPort:
		display, ok := parseQuickConnectDisplay(form.VNCDisplay)
		if !ok {
			// A malformed display must not become a raw port. Keep display mode
			// so connect still reports the invalid display.
			return
		}
		port, ok := domainvault.VNCPortForDisplay(display)
		if !ok {
			return
		}
		form.Port = strconv.Itoa(port)
	default:
		form.VNCDisplay = ""
		if port, err := strconv.Atoi(strings.TrimSpace(form.Port)); err == nil {
			if display, ok := domainvault.VNCDisplayForPort(port); ok {
				form.VNCDisplay = strconv.Itoa(display)
			}
		}
	}
	form.VNCMode = next
}

// RequestQuickConnectClose closes an empty form at once. A form with any typed
// detail first asks to discard it, because an outside click or Escape must not
// lose a partly filled form.
func (s *State) RequestQuickConnectClose() {
	if !s.quickConnect.open {
		return
	}
	if s.quickConnect.form.untouched() {
		s.closeQuickConnect()
		return
	}
	s.quickConnect.confirmingDiscard = true
	s.RequestRedraw()
}

// DiscardQuickConnect closes the dialog and forgets everything typed in it.
func (s *State) DiscardQuickConnect() { s.closeQuickConnect() }

// KeepQuickConnectEditing dismisses the discard confirmation and returns to the
// form with its contents intact.
func (s *State) KeepQuickConnectEditing() {
	s.quickConnect.confirmingDiscard = false
	s.RequestRedraw()
}

// ConnectQuickConnect validates the form, closes it, and opens an Unsaved Host
// Session. A validation failure keeps the form open and reports the issue.
func (s *State) ConnectQuickConnect() error {
	if !s.quickConnect.open {
		return nil
	}
	form := &s.quickConnect.form
	hostname := strings.TrimSpace(form.Hostname)
	if hostname == "" {
		s.quickConnect.issue = QuickConnectIssueAddress
		s.RequestRedraw()
		return nil
	}

	protocol := form.Protocol
	port := 0
	if protocol == domainvault.ProtocolVNC &&
		domainvault.NormalizeDisplayMode(form.VNCMode) == domainvault.VNCDisplayNumber {
		display, ok := parseQuickConnectDisplay(form.VNCDisplay)
		if !ok || display < 0 || display > domainvault.VNCDisplayMax {
			s.quickConnect.issue = QuickConnectIssueDisplay
			s.RequestRedraw()
			return nil
		}
		vncPort, ok := domainvault.VNCPortForDisplay(display)
		if !ok {
			s.quickConnect.issue = QuickConnectIssueDisplay
			s.RequestRedraw()
			return nil
		}
		port = vncPort
	} else {
		typed, err := strconv.Atoi(strings.TrimSpace(form.Port))
		if err != nil || typed < 1 || typed > 65535 {
			s.quickConnect.issue = QuickConnectIssuePort
			s.RequestRedraw()
			return nil
		}
		port = typed
	}
	s.quickConnect.issue = QuickConnectIssueNone

	username := strings.TrimSpace(form.Username)
	host := domainvault.Host{
		Label:    hostname,
		Hostname: hostname,
		Port:     port,
		Protocol: protocol,
	}
	password := ""
	switch protocol {
	case domainvault.ProtocolSSH:
		host.Username = username
		host.Transport = domainvault.NormalizeTransport(protocol, form.Transport)
		password = form.Password
		if username != "" {
			host.Label = username + "@" + hostname
		}
		if host.Transport == domainvault.TransportPlain && form.NetworkDevice {
			host.DeviceType = domainvault.DeviceTypeNetwork
		}
	case domainvault.ProtocolVNC:
		host.Username = username
		configuration := domainvault.DefaultVNCConfig()
		configuration.DisplayMode = domainvault.NormalizeDisplayMode(form.VNCMode)
		host.VNC = &configuration
		// The VNC transport copies the one-shot password into its credential,
		// so it is never stored on the Host or in the Vault.
		password = form.VNCPassword
		if username != "" {
			host.Label = username + "@" + hostname
		}
	}

	if err := s.openHostWithPassword(host, StartupInput{}, false, password); err != nil {
		if errors.Is(err, ErrNoSessions) {
			s.quickConnect.issue = QuickConnectIssueNoSessions
		}
		s.RequestRedraw()
		return err
	}
	s.closeQuickConnect()
	return nil
}

// untouched reports whether the form still holds only its opening defaults, so
// closing it loses nothing worth confirming. Switching protocol on its default
// port leaves nothing to discard, so the protocol itself does not count.
func (f QuickConnectForm) untouched() bool {
	return f.Hostname == "" && f.Port == defaultQuickConnectPort(f.Protocol) &&
		f.Username == "" && f.Password == "" && !f.NetworkDevice &&
		f.Transport == domainvault.TransportPlain &&
		f.VNCPassword == "" &&
		domainvault.NormalizeDisplayMode(f.VNCMode) == domainvault.VNCDisplayNumber &&
		(f.VNCDisplay == "" || f.VNCDisplay == defaultQuickConnectDisplay)
}

// closeQuickConnect resets the dialog, clearing the password from memory.
func (s *State) closeQuickConnect() {
	s.quickConnect = quickConnectState{}
	s.RequestRedraw()
}
