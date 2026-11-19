package session

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"

	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainprotocol "github.com/mslxl/natcatty/domain/model/protocol"
	telnetmodel "github.com/mslxl/natcatty/domain/model/telnet"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// HostSpec maps a saved Host onto the Session service's StartSpec. The
// desktop shell and the phone composition root both call it, so one Host
// opens under the same protocol, endpoint, tmux, X11, and reconnect rules
// everywhere. A Host whose protocol or transport this build cannot open keeps
// the zero Kind: callers fail closed instead of dialing it as SSH.
func HostSpec(host domainvault.Host) StartSpec {
	label := host.Label
	if label == "" {
		label = host.Hostname
	}
	spec := StartSpec{
		Label:          label,
		StartupCommand: host.StartupCommand,
		HostID:         host.ID,
		Hostname:       host.Hostname,
		Port:           host.Port,
		Username:       host.Username,
		// Input compatibility is local-only: the Session translates typed keys
		// before they reach this transport.
		InputCompatibility: host.InputCompatibility(),
	}
	if !host.ConnectionSupported() {
		// A removed protocol or an unknown transport never falls through to
		// SSH. An empty Kind is the caller's signal to fail closed.
		return spec
	}
	// The Host resolves its effective transport: an SSH Host with Transport
	// mosh or et selects that Session kind; this switch only builds the
	// kind-specific start payload.
	switch host.SessionTransport() {
	case domainprotocol.TransportTelnet:
		cfg := telnetmodel.Config{Hostname: host.Hostname, Port: host.Port}.Normalized()
		spec.Kind = KindTelnet
		spec.Telnet = &cfg
		// Telnet keeps the manual Reconnect action but never retries a dropped
		// socket automatically, matching the original's SSH-only reconnect.
		spec.Reconnect = TelnetReconnectPolicy()
	case domainprotocol.TransportMosh:
		spec.Kind = KindMosh
		spec.Mosh = moshConfig(host)
		spec.Reconnect = &ReconnectPolicy{}
	case domainprotocol.TransportET:
		spec.Kind = KindET
		spec.ET = etConfig(host)
		spec.ResumeKey = NewETResumeKey()
		spec.Reconnect = &ReconnectPolicy{}
	case domainprotocol.TransportVNC:
		// VNC follows the SSH reconnect policy: a dropped pixel stream retries,
		// but an authentication failure never does.
		spec.Kind = KindVNC
		spec.VNC = vncConfig(host)
		spec.Reconnect = &ReconnectPolicy{}
	default:
		// X11 forwarding, raw network-device execution, and tmux auto-attach
		// need an SSH PTY, so a Host of another protocol never carries them.
		spec.Kind = KindSSH
		spec.X11Forwarding = host.X11Enabled()
		spec.NetworkDevice = host.IsNetworkDevice()
		spec.AutoAttachTmux = host.AutoAttachTmuxEnabled()
		// A dropped SSH transport retries every five seconds under the
		// original reason predicate.
		spec.Reconnect = &ReconnectPolicy{}
	}
	return spec
}

// TelnetReconnectPolicy allows the Session menu's manual Reconnect action but
// never retries a dropped Telnet socket automatically.
func TelnetReconnectPolicy() *ReconnectPolicy {
	return &ReconnectPolicy{
		ShouldRetry: func(End) bool { return false },
	}
}

// moshConfig is the non-secret bootstrap half of a Mosh Session. Credentials,
// jump trust, and MFA stay with the SSH Connection Manager.
func moshConfig(host domainvault.Host) *moshmodel.Config {
	cfg := moshmodel.Config{
		Hostname:   host.Hostname,
		Port:       host.Port,
		Username:   host.Username,
		ServerPath: host.MoshServerPath,
	}.Normalized()
	return &cfg
}

// etConfig is the non-secret half of an EternalTerminal Session. The app adds
// credentials, host-key snapshots, and the single jump Host after resolving
// the route.
func etConfig(host domainvault.Host) *etmodel.Config {
	cfg := etmodel.Config{
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
		ETPort:   host.ETPort,
	}.Normalized()
	return &cfg
}

// NewETResumeKey returns the opaque, non-secret locator for a resumable ET
// Session's saved credentials. It is generated here, where both composition
// roots can use it, rather than in a UI package.
// vncConfig is the kind-selected start section for a VNC Host. The password is
// resolved by the app from the Host's own secret slot; this mapping carries the
// endpoint, the non-secret viewer configuration, and the route references.
func vncConfig(host domainvault.Host) *modelvnc.SessionConfig {
	configuration := modelvnc.DefaultConfig()
	if host.VNC != nil {
		configuration = *modelvnc.NormalizeConfig(host.VNC)
	}
	cfg := modelvnc.SessionConfig{
		Hostname:      host.Hostname,
		Port:          host.Port,
		Username:      host.Username,
		Configuration: configuration,
		Route: modelvnc.Route{
			Jumps:            host.HostChain.IDs(),
			ProxyProfileID:   host.ProxyProfileID,
			NetworkProfileID: host.NetworkProfileID,
			DirectProxy:      host.DirectProxy,
		},
	}.Normalized()
	return &cfg
}

func NewETResumeKey() string {
	var id [16]byte
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(time.Now().UnixNano()))
	copy(id[:8], stamp[:])
	if _, err := rand.Read(id[8:]); err != nil {
		// A failed random read still yields a usable key: the timestamp half
		// alone is unique enough for one process's ET Sessions, and the
		// credential store treats the key as opaque.
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return "et-resume-" + hex.EncodeToString(id[:])
}
