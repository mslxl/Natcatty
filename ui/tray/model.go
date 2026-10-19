// Package tray localizes the system-tray menu's Tab and Port Forwarding Rule
// lines and maps domain directions to the original L/R/D markers. The data
// stays pure, so headless tests can verify the text without a native
// notification-area icon; the composition root builds the MyGo menu.
package tray

import (
	"fmt"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
)

// TabStatus mirrors the shell's aggregate transport state for a Tab.
type TabStatus string

const (
	TabConnected    TabStatus = "connected"
	TabConnecting   TabStatus = "connecting"
	TabDisconnected TabStatus = "disconnected"
)

// Tab is one tray entry for a Workspace Tab. TabIndex is -1 for a detached
// Tab. A Tab may host several Sessions; Status aggregates their transport
// state.
type Tab struct {
	TabID    model.TabID
	TabIndex int
	Detached bool
	Number   int
	Label    string
	Status   TabStatus
}

// Rule is one tray Port Forwarding Rule entry.
type Rule struct {
	ID         model.ForwardingRuleID
	Direction  string
	Label      string
	LocalPort  int
	RemoteHost string
	RemotePort int
	Status     string
}

// TabText is the localized menu line for a Tab.
func TabText(m locales.Messages, tab Tab) string {
	label := tab.Label
	if label == "" {
		label = m.TabTitle(tab.Number)
	}
	status := m.TrayStatusConnected()
	switch tab.Status {
	case TabConnecting:
		status = m.TrayStatusConnecting()
	case TabDisconnected:
		status = m.TrayStatusDisconnected()
	}
	return fmt.Sprintf("%s — %s", label, status)
}

// RuleText is the localized menu line for a Port Forwarding Rule. Status is
// the domain status name localized here rather than in the domain layer.
func RuleText(m locales.Messages, rule Rule) string {
	label := rule.Label
	if label == "" {
		label = m.TrayRuleUnnamed()
	}
	detail := fmt.Sprintf(":%d", rule.LocalPort)
	if rule.Direction == "L" && rule.RemoteHost != "" {
		detail += fmt.Sprintf(" → %s:%d", rule.RemoteHost, rule.RemotePort)
	}
	return fmt.Sprintf("[%s] %s %s (%s)", rule.Direction, label, detail, RuleStatusText(m, rule.Status))
}

// RuleStatusText localizes a forwarding runtime status.
func RuleStatusText(m locales.Messages, status string) string {
	switch status {
	case "active":
		return m.TrayStatusActive()
	case "connecting":
		return m.TrayStatusConnecting()
	case "error":
		return m.TrayStatusError()
	default:
		return m.TrayStatusInactive()
	}
}

// RuleKindLetter maps a forwarding type to the original tray's L/R/D marker.
func RuleKindLetter(kind string) string {
	switch kind {
	case "remote":
		return "R"
	case "dynamic":
		return "D"
	default:
		return "L"
	}
}
