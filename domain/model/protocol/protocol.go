// Package protocol defines the built-in Host connection protocols and the
// facts the rest of the app reads about them.
//
// A Host stores its protocol by ID. Validation, port defaults, transport
// selection, and the capability guards in the Vault, the connection manager
// adapters, and the UI all resolve the same Descriptor instead of repeating a
// switch. The descriptor is deliberately limited to built-in protocols: a
// plugin protocol namespace is a separate, unreleased design.
package protocol

// ID names a Host connection protocol. Built-in values are persisted in Host
// records.
type ID string

const (
	// SSH is a Host reached over SSH.
	SSH ID = "ssh"
	// Telnet is a Host reached over Telnet.
	Telnet ID = "telnet"
	// VNC is a Host reached over the RFB protocol. Its Session carries pixels
	// through a display-session port rather than a byte stream.
	VNC ID = "vnc"
)

// Transport names the Session transport a protocol selects. The values match
// application/session.Kind and the Workspace Session kinds, so selection needs
// no translation table.
type Transport string

const (
	// TransportLocal is a pseudo-terminal on this machine.
	TransportLocal Transport = "local"
	// TransportSSH is an interactive shell over an SSH Connection.
	TransportSSH Transport = "ssh"
	// TransportTelnet is a Telnet terminal.
	TransportTelnet Transport = "telnet"
	// TransportVNC is a VNC pixel Session over RFB.
	TransportVNC Transport = "vnc"
	// TransportMosh is a UDP Mosh Session bootstrapped over SSH.
	TransportMosh Transport = "mosh"
	// TransportET is an EternalTerminal Session.
	TransportET Transport = "et"
	// TransportUnsupported is returned when a persisted Host names a protocol
	// or transport this build does not support. It is deliberately not a
	// Session kind: callers fail closed instead of falling back to SSH.
	TransportUnsupported Transport = "unsupported"
)

// Descriptor is one protocol's identity and the facts the app resolves from
// it. A false capability means the protocol's editor hides the control, its
// transport ignores the setting, and a connector that needs the capability
// rejects the Host with a typed unsupported error.
type Descriptor struct {
	// ID is the persisted protocol value.
	ID ID
	// Transport is the Session transport the protocol opens.
	Transport Transport
	// DefaultPort is the port a Host uses when neither the Host nor its Group
	// sets one. Zero means the protocol has no TCP port.
	DefaultPort int
	// SSHAuth reports whether the protocol authenticates with the shared SSH
	// credential bundle (Identity, SSH Key, Method, agent, MFA).
	SSHAuth bool
	// HostProtocol reports whether a Host record may select the protocol.
	HostProtocol bool
	// GroupDefault reports whether a Host Group may set the protocol as the
	// default for its members.
	GroupDefault bool
	// FileTransfer reports whether SFTP/SCP apply to the Host.
	FileTransfer bool
	// Forwarding reports whether Port Forwarding rules may target the Host.
	Forwarding bool
	// Monitoring reports whether remote metric sampling may target the Host.
	Monitoring bool
	// Proxy reports whether jump chains, proxies, and SSH algorithm overrides
	// apply to the Host's route.
	Proxy bool
	// X11 reports whether X11 forwarding applies to the Host.
	X11 bool
	// Plaintext reports whether the protocol's own transport sends credentials
	// and keystrokes over the network unencrypted. A route through SSH encrypts
	// the wire, but the capability describes the native protocol and lets the
	// Vault editor disclose the exposure without comparing protocol names.
	Plaintext bool
}

// builtins is the ordered built-in Host protocol set. The order is the order
// the Vault presents when a caller does not impose its own. Mosh and ET are
// SSH transports, not Host protocols, so they are absent here.
var builtins = []Descriptor{
	{
		ID: SSH, Transport: TransportSSH, DefaultPort: 22,
		SSHAuth: true, HostProtocol: true, GroupDefault: true,
		FileTransfer: true, Forwarding: true,
		Monitoring: true, Proxy: true, X11: true,
	},
	{
		ID: Telnet, Transport: TransportTelnet, DefaultPort: 23,
		SSHAuth: false, HostProtocol: true, GroupDefault: true,
		// Telnet's target has no SSH handshake, but its route can still run
		// through SSH jumps and a proxy, so the route controls apply.
		FileTransfer: false, Forwarding: false,
		Monitoring: false, Proxy: true, X11: false,
		// The native protocol sends its password and every keystroke in the
		// clear. The editor discloses that through this capability.
		Plaintext: true,
	},
	{
		ID: VNC, Transport: TransportVNC, DefaultPort: 5900,
		SSHAuth: false, HostProtocol: true, GroupDefault: true,
		// RFB is a single pixel stream. Jump chains and a proxy can still
		// carry it, like a Telnet stream, but there is no SSH application
		// channel for SFTP, forwarding, monitoring, or X11.
		FileTransfer: false, Forwarding: false,
		Monitoring: false, Proxy: true, X11: false,
		// VNC password authentication is challenge-response over an
		// unencrypted stream; only TLS or an SSH route encrypts the wire.
		Plaintext: true,
	},
}

// transportDescriptors is the capability table keyed by Session transport. It
// adds Mosh and ET, which an SSH Host selects through its Transport field and
// which are never persisted as Host protocol IDs.
var transportDescriptors = func() []Descriptor {
	descriptors := append([]Descriptor(nil), builtins...)
	return append(descriptors,
		Descriptor{
			Transport: TransportMosh, DefaultPort: 22,
			SSHAuth: true, HostProtocol: false, GroupDefault: false,
			FileTransfer: false, Forwarding: true,
			Monitoring: true, Proxy: true, X11: false,
		},
		Descriptor{
			Transport: TransportET, DefaultPort: 22,
			SSHAuth: true, HostProtocol: false, GroupDefault: false,
			FileTransfer: false, Forwarding: true,
			Monitoring: true, Proxy: true, X11: false,
		},
	)
}()

// Builtins returns the built-in Host protocol descriptors in display order.
// The slice is a copy; callers may keep or reorder it.
func Builtins() []Descriptor {
	return append([]Descriptor(nil), builtins...)
}

// Resolve returns the descriptor for a built-in Host protocol. Mosh and ET do
// not resolve: they are transports, not protocol IDs.
func Resolve(id ID) (Descriptor, bool) {
	for _, descriptor := range builtins {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

// DescriptorForTransport returns the capability descriptor for a Session
// transport. It lets a Host resolve its effective capabilities from
// `protocol: ssh` + `transport: mosh|et` without comparing protocol names. An
// unknown transport has no descriptor.
func DescriptorForTransport(transport Transport) (Descriptor, bool) {
	for _, descriptor := range transportDescriptors {
		if descriptor.Transport == transport {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

// ValidHostID reports whether a Host record may carry id.
func ValidHostID(id ID) bool {
	descriptor, ok := Resolve(id)
	return ok && descriptor.HostProtocol
}

// ValidGroupDefault reports whether a Host Group may set id as its default.
// An empty value means the group does not set a protocol, so it is valid.
func ValidGroupDefault(id ID) bool {
	if id == "" {
		return true
	}
	descriptor, ok := Resolve(id)
	return ok && descriptor.GroupDefault
}
