package vault

import (
	"github.com/mslxl/natcatty/domain/model/launch"
)

// MatchHost finds the one saved SSH Host a launch target addresses. The
// original behavior is preserved: protocol, hostname, an optional username,
// and the effective port must match, and exactly one Host must remain. It
// lives here, beside Host, so both the desktop application and the phone shell
// resolve a deep link without importing each other's wiring.
func MatchHost(hosts []Host, target launch.Target) (Host, bool) {
	if target.Kind != launch.TargetSSH {
		return Host{}, false
	}
	var matches []Host
	for _, host := range hosts {
		if host.Protocol != "" && host.Protocol != ProtocolSSH {
			continue
		}
		if launch.NormalizeHostname(host.Hostname) != target.Hostname {
			continue
		}
		if target.Username != "" && host.Username != target.Username {
			continue
		}
		hostPort := host.Port
		if hostPort == 0 {
			hostPort = launch.DefaultSSHPort
		}
		if hostPort != target.TargetPort() {
			continue
		}
		matches = append(matches, host)
	}
	if len(matches) != 1 {
		return Host{}, false
	}
	return matches[0], true
}

// DraftHost builds an unsaved Host for a target with no unique saved match.
// It carries no credentials: an ssh:// password is parsed for validation and
// ordering, but the Vault owns persisted secrets.
func DraftHost(target launch.Target) Host {
	label := target.Hostname
	if target.Username != "" {
		label = target.Username + "@" + target.Hostname
	}
	return Host{
		Label:    label,
		Hostname: target.Hostname,
		Port:     target.TargetPort(),
		Username: target.Username,
		Protocol: ProtocolSSH,
	}
}
