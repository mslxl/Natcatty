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
	// QuickConnectIssueNoSessions means the shell has no Session service.
	QuickConnectIssueNoSessions
)

// QuickConnectForm is the tab bar's quick-connect dialog. Nothing in it is
// saved to the Vault: connecting opens an Unsaved Host Session.
type QuickConnectForm struct {
	// Protocol is ProtocolSSH or ProtocolTelnet.
	Protocol domainvault.Protocol
	Hostname string
	// Port stays text so an incomplete entry can be corrected without the form
	// silently coercing it.
	Port string
	// Username and Password apply to SSH only. Password is a one-shot
	// credential and never leaves memory.
	Username string
	Password string
	// NetworkDevice starts the temporary Session in network-device mode. It
	// applies to SSH only.
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
	if protocol == domainvault.ProtocolTelnet {
		return "23"
	}
	return "22"
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
			Protocol: domainvault.ProtocolSSH,
			Port:     defaultQuickConnectPort(domainvault.ProtocolSSH),
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

// SetQuickConnectProtocol switches the form's protocol. The port follows the
// protocol's default while the user has not typed a port of their own.
func (s *State) SetQuickConnectProtocol(protocol domainvault.Protocol) {
	form := &s.quickConnect.form
	if protocol != domainvault.ProtocolSSH && protocol != domainvault.ProtocolTelnet {
		return
	}
	previous := form.Protocol
	form.Protocol = protocol
	if protocol == domainvault.ProtocolTelnet {
		form.Username = ""
		form.Password = ""
		form.NetworkDevice = false
	}
	if form.Port == "" || form.Port == defaultQuickConnectPort(previous) {
		form.Port = defaultQuickConnectPort(protocol)
	}
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
	port, err := strconv.Atoi(strings.TrimSpace(form.Port))
	if err != nil || port < 1 || port > 65535 {
		s.quickConnect.issue = QuickConnectIssuePort
		s.RequestRedraw()
		return nil
	}
	s.quickConnect.issue = QuickConnectIssueNone

	protocol := form.Protocol
	username := strings.TrimSpace(form.Username)
	host := domainvault.Host{
		Label:    hostname,
		Hostname: hostname,
		Port:     port,
		Protocol: protocol,
	}
	password := ""
	if protocol == domainvault.ProtocolSSH {
		host.Username = username
		password = form.Password
		if username != "" {
			host.Label = username + "@" + hostname
		}
		if form.NetworkDevice {
			host.DeviceType = domainvault.DeviceTypeNetwork
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
		f.Username == "" && f.Password == "" && !f.NetworkDevice
}

// closeQuickConnect resets the dialog, clearing the password from memory.
func (s *State) closeQuickConnect() {
	s.quickConnect = quickConnectState{}
	s.RequestRedraw()
}
