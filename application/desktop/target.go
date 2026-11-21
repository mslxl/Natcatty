package desktop

// Target parsing is shared with the phone shell, which delivers the same
// ssh:// links into the same Host-matching rules. The parser lives in the
// shared domain model and Host matching lives beside Host in domain/vault, so
// ui/phoneshell does not import this desktop-only package; the aliases below
// keep this package's public API and its tests unchanged.

import (
	"github.com/mslxl/natcatty/domain/model/launch"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TargetKind is the transport a launch target addresses.
type TargetKind = launch.TargetKind

// Target is a launch target parsed from a deep link or the command line.
type Target = launch.Target

const (
	// TargetSSH is an ssh:// URL or an ssh-style command-line Host.
	TargetSSH = launch.TargetSSH
	// TargetTelnet is a telnet:// URL.
	TargetTelnet = launch.TargetTelnet
	// TargetLocalShell is a local terminal requested with
	// --open-terminal-shell. It opens a Tab instead of dialing a Host.
	TargetLocalShell = launch.TargetLocalShell
	// DefaultSSHPort is the port an ssh:// target uses when the URL omits one.
	DefaultSSHPort = launch.DefaultSSHPort
)

// Deep-link failures. They are re-exported so callers and tests keep using
// errors.Is against this package's names.
var (
	ErrInvalidTarget     = launch.ErrInvalidTarget
	ErrUnsupportedScheme = launch.ErrUnsupportedScheme
)

// ParseDeepLink parses an ssh:// or telnet:// URL.
func ParseDeepLink(raw string) (Target, error) { return launch.ParseDeepLink(raw) }

// ParseCommandLine extracts Host targets from launch arguments.
func ParseCommandLine(args []string) ([]Target, []error) {
	return launch.ParseCommandLine(args)
}

// MatchHost finds the one saved SSH Host a target addresses.
func MatchHost(hosts []domainvault.Host, target Target) (domainvault.Host, bool) {
	return domainvault.MatchHost(hosts, target)
}

// DraftHost builds an unsaved Host for a target with no unique saved match.
func DraftHost(target Target) domainvault.Host { return domainvault.DraftHost(target) }
