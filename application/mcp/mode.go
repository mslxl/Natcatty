// Package mcp owns the external MCP permission policy: the forced scope and
// its intersection with a client's requested scope, the session allowlist,
// per-scope ownership generations, the observer/confirm/auto permission
// modes, and the expiring single-use approvals that gate writes.
//
// The package is pure application logic. It knows nothing about the tool
// catalog, the loopback transport, MyGo, or the UI: the composition root
// supplies tool policy flags, a clock, a mode source, and the client identity.
// Protocol errors carry stable English codes because they are consumed by
// external clients, not rendered as translated UI text.
package mcp

import "strings"

// Mode is the external client's permission mode.
type Mode string

const (
	// ModeObserver allows reads and denies writes, except the two lifecycle
	// tools the catalog marks as bypassing the observer block.
	ModeObserver Mode = "observer"
	// ModeConfirm asks the user for an approval before every write that does
	// not bypass approval.
	ModeConfirm Mode = "confirm"
	// ModeAuto allows reads and writes without an approval prompt.
	ModeAuto Mode = "auto"
)

// DefaultMode is the original bridge's default when no mode is configured.
const DefaultMode = ModeConfirm

// ParseMode reports whether value is one of the three permission modes.
func ParseMode(value string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case ModeObserver:
		return ModeObserver, true
	case ModeConfirm:
		return ModeConfirm, true
	case ModeAuto:
		return ModeAuto, true
	}
	return "", false
}

// NormalizeMode parses value and falls back to confirm for an empty or
// unknown setting, matching the original's defense-in-depth default.
func NormalizeMode(value string) Mode {
	if mode, ok := ParseMode(value); ok {
		return mode
	}
	return DefaultMode
}
