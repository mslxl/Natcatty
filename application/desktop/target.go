package desktop

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TargetKind is the transport a launch target addresses.
type TargetKind string

const (
	// TargetSSH is an ssh:// URL or an ssh-style command-line Host.
	TargetSSH TargetKind = "ssh"
	// TargetTelnet is a telnet:// URL. The rewrite has no Telnet transport
	// yet, so the app reports it as an unknown target instead of connecting.
	TargetTelnet TargetKind = "telnet"
)

// Target is a launch target parsed from a deep link or the command line.
type Target struct {
	Kind TargetKind
	// Hostname is trimmed, lower cased, and without IPv6 brackets.
	Hostname string
	Username string
	// Password is form input from the URL. It is never stored: the Vault
	// credential store owns saved passwords.
	Password string
	// Port is the explicit port, or 0 for the protocol default.
	Port int
	// Raw is the original argument, kept for diagnostics.
	Raw string
}

// DefaultSSHPort is the port an ssh:// target uses when the URL omits one.
const DefaultSSHPort = 22

// ParseDeepLink parses an ssh:// or telnet:// URL. Other schemes are rejected
// so a window that receives an unrelated URL does not try to connect.
func ParseDeepLink(raw string) (Target, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("%w: %v", ErrInvalidTarget, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	var kind TargetKind
	switch scheme {
	case "ssh":
		kind = TargetSSH
	case "telnet":
		kind = TargetTelnet
	default:
		return Target{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, scheme)
	}
	if parsed.Hostname() == "" {
		return Target{}, fmt.Errorf("%w: missing host", ErrInvalidTarget)
	}
	target := Target{
		Kind:     kind,
		Hostname: normalizeHostname(parsed.Hostname()),
		Port:     0,
		Raw:      raw,
	}
	if target.Hostname == "" {
		return Target{}, fmt.Errorf("%w: missing host", ErrInvalidTarget)
	}
	if parsed.User != nil {
		target.Username = parsed.User.Username()
		if password, ok := parsed.User.Password(); ok {
			target.Password = password
		}
	}
	if rawPort := parsed.Port(); rawPort != "" {
		port, err := parsePort(rawPort)
		if err != nil {
			return Target{}, err
		}
		target.Port = port
	}
	return target, nil
}

// ParseCommandLine extracts Host targets from launch arguments. The arguments
// must exclude the program name. It mirrors the original app's command-line
// support: ssh:// and telnet:// URLs, user@host, host:port, the -l/-p/-P
// options, PuTTY-style switches such as -ssh/-pw/-L, and SecureCRT-style
// switches such as /SSH2 /L /P /PASSWORD used by bastion launchers.
//
// Every known value switch consumes its operand before Host detection, so a
// password, key path, or forwarding spec is never mistaken for a Host. Unknown
// options are ignored; unrelated arguments produce no target.
func ParseCommandLine(args []string) ([]Target, []error) {
	var targets []Target
	var problems []error
	kind := TargetSSH
	username := ""
	password := ""
	port := 0

	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if strings.Contains(raw, "://") {
			target, err := ParseDeepLink(raw)
			if err != nil {
				problems = append(problems, err)
				return
			}
			if username != "" {
				target.Username = username
			}
			if password != "" {
				target.Password = password
			}
			if port != 0 {
				target.Port = port
			}
			targets = append(targets, target)
			return
		}
		target, err := parseHostArgument(raw)
		if err != nil {
			problems = append(problems, err)
			return
		}
		target.Kind = kind
		if username != "" {
			target.Username = username
		}
		if password != "" {
			target.Password = password
		}
		if port != 0 {
			target.Port = port
		}
		target.Raw = raw
		targets = append(targets, target)
	}

	consume := func(flag, value string) {
		switch {
		case commandLineUserFlags[flag] || secureCRTUserFlags[flag]:
			username = value
		case commandLinePortFlags[flag] || secureCRTPortFlags[flag]:
			parsed, err := parsePort(value)
			if err != nil {
				problems = append(problems, err)
				return
			}
			port = parsed
		case commandLinePasswordFlags[flag] || secureCRTPasswordFlags[flag]:
			password = value
		}
	}

	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" || arg == "." || arg == "--" {
			continue
		}
		if protocol, isProtocol, unsupported := commandLineProtocol(arg); isProtocol {
			if unsupported {
				problems = append(problems, fmt.Errorf("%w: %q", ErrUnsupportedScheme, arg))
				return nil, problems
			}
			kind = protocol
			continue
		}
		lower := strings.ToLower(arg)
		switch {
		case arg == "--user" || lower == "--user":
			if index+1 < len(args) {
				index++
				username = args[index]
			}
		case strings.HasPrefix(lower, "--user="):
			username = arg[len("--user="):]
		case arg == "--port" || lower == "--port":
			if index+1 < len(args) {
				index++
				if value, err := parsePort(args[index]); err == nil {
					port = value
				} else {
					problems = append(problems, err)
				}
			}
		case strings.HasPrefix(lower, "--port="):
			if value, err := parsePort(arg[len("--port="):]); err == nil {
				port = value
			} else {
				problems = append(problems, err)
			}
		case commandLineValueFlags[arg]:
			if index+1 < len(args) {
				index++
				consume(arg, args[index])
			}
		case secureCRTValueFlags[lower]:
			if index+1 < len(args) {
				index++
				consume(lower, args[index])
			}
		case strings.HasPrefix(arg, "-"), strings.HasPrefix(arg, "/"):
			// A client option the rewrite does not consume, such as -X, -A,
			// -v, -N, /t, or /new.
		case strings.ContainsAny(arg, `/\`) && !strings.Contains(arg, "://"):
			// A Windows path or other file argument is not a Host.
		case isDigits(arg) && port == 0:
			// PuTTY accepts a bare numeric port before the Host.
			if value, err := parsePort(arg); err == nil {
				port = value
			}
		default:
			add(arg)
		}
	}
	return targets, problems
}

// commandLineProtocol recognizes the explicit transport switches. The third
// result reports a switch the rewrite cannot map to a transport, which makes
// the launch line invalid instead of silently connecting over SSH.
func commandLineProtocol(arg string) (TargetKind, bool, bool) {
	switch strings.ToLower(arg) {
	case "-ssh", "--ssh", "/ssh", "/ssh1", "/ssh2":
		return TargetSSH, true, false
	case "-telnet", "--telnet", "/telnet":
		return TargetTelnet, true, false
	case "-raw", "-rlogin", "-serial", "-ssh-connection", "-supdup",
		"/serial", "/rlogin", "/tapi":
		return "", true, true
	}
	return "", false, false
}

// PuTTY-style switches whose next argument is a value. Values are consumed and,
// where the rewrite has a use for them, applied to the following Host.
var commandLineValueFlags = map[string]bool{
	"-p": true, "-P": true, "-l": true, "-pw": true, "-pwfile": true,
	"-i": true, "-m": true, "-cmd": true, "-L": true, "-R": true, "-D": true,
	"-nc": true, "-hostkey": true, "-loghost": true, "-sercfg": true,
	"-sessionlog": true, "-sshlog": true, "-sshrawlog": true,
	"-proxycmd": true, "-newtab": true, "-title": true, "-url": true,
}

var (
	commandLineUserFlags     = map[string]bool{"-l": true}
	commandLinePortFlags     = map[string]bool{"-p": true, "-P": true}
	commandLinePasswordFlags = map[string]bool{"-pw": true}
)

// SecureCRT-style switches (case-insensitive). 4A/PAM bastion launchers emit
// these when the operator selects SecureCRT as the client.
var secureCRTValueFlags = map[string]bool{
	"/l": true, "/p": true, "/password": true,
	"/auth": true, "/i": true, "/passphrase": true, "/s": true, "/n": true,
	"/titlebar": true, "/log": true, "/logappend": true, "/firewall": true,
	"/fwfirewall": true, "/proxy": true,
}

var (
	secureCRTUserFlags     = map[string]bool{"/l": true}
	secureCRTPortFlags     = map[string]bool{"/p": true}
	secureCRTPasswordFlags = map[string]bool{"/password": true}
)

// parseHostArgument parses user@host, host, host:port, and [v6]:port.
func parseHostArgument(raw string) (Target, error) {
	hostspec := raw
	username := ""
	if at := strings.LastIndex(hostspec, "@"); at >= 0 {
		username = hostspec[:at]
		hostspec = hostspec[at+1:]
	}
	hostname := hostspec
	port := 0
	if host, value, err := net.SplitHostPort(hostspec); err == nil {
		hostname = host
		port, err = parsePort(value)
		if err != nil {
			return Target{}, err
		}
	} else if strings.Count(hostspec, ":") == 1 && !strings.Contains(hostspec, "::") {
		host, value, found := strings.Cut(hostspec, ":")
		if found && value != "" {
			parsed, err := parsePort(value)
			if err != nil {
				return Target{}, err
			}
			hostname, port = host, parsed
		}
	}
	hostname = normalizeHostname(hostname)
	if hostname == "" {
		return Target{}, fmt.Errorf("%w: missing host", ErrInvalidTarget)
	}
	return Target{Kind: TargetSSH, Hostname: hostname, Username: username, Port: port}, nil
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%w: port %q", ErrInvalidTarget, raw)
	}
	return port, nil
}

func isDigits(raw string) bool {
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeHostname(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

// TargetPort returns the explicit port or the SSH default.
func (t Target) TargetPort() int {
	if t.Port > 0 {
		return t.Port
	}
	return DefaultSSHPort
}

// MatchHost finds the one saved SSH Host a target addresses. The original
// behavior is preserved: protocol, hostname, an optional username, and the
// effective port must match, and exactly one Host must remain.
func MatchHost(hosts []domainvault.Host, target Target) (domainvault.Host, bool) {
	if target.Kind != TargetSSH {
		return domainvault.Host{}, false
	}
	var matches []domainvault.Host
	for _, host := range hosts {
		if host.Protocol != "" && host.Protocol != domainvault.ProtocolSSH {
			continue
		}
		if normalizeHostname(host.Hostname) != target.Hostname {
			continue
		}
		if target.Username != "" && host.Username != target.Username {
			continue
		}
		hostPort := host.Port
		if hostPort == 0 {
			hostPort = DefaultSSHPort
		}
		if hostPort != target.TargetPort() {
			continue
		}
		matches = append(matches, host)
	}
	if len(matches) != 1 {
		return domainvault.Host{}, false
	}
	return matches[0], true
}

// DraftHost builds an unsaved Host for a target with no unique saved match.
// It carries no credentials: an ssh:// password is parsed for validation and
// ordering, but the Vault owns persisted secrets.
func DraftHost(target Target) domainvault.Host {
	label := target.Hostname
	if target.Username != "" {
		label = target.Username + "@" + target.Hostname
	}
	return domainvault.Host{
		Label:    label,
		Hostname: target.Hostname,
		Port:     target.TargetPort(),
		Username: target.Username,
		Protocol: domainvault.ProtocolSSH,
	}
}
