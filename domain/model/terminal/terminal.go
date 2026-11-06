// Package terminal holds the terminal model values shared by the terminal
// settings and the Session title resolver. It has no UI or transport
// knowledge.
package terminal

// DynamicTabTitleMode selects which Session tabs follow the shell-reported
// OSC 0/2 title instead of the connection label.
type DynamicTabTitleMode string

const (
	// DynamicTabTitleOff always shows the connection label: a user rename or
	// the Host label.
	DynamicTabTitleOff DynamicTabTitleMode = "off"
	// DynamicTabTitleAgents follows the dynamic title only for Sessions that
	// run a coding CLI agent.
	DynamicTabTitleAgents DynamicTabTitleMode = "agent"
	// DynamicTabTitleAll follows the dynamic title for every Session. It is
	// the default, so a plain shell's reported title names its Tab as well.
	DynamicTabTitleAll DynamicTabTitleMode = "all"
)

// Valid reports whether the mode is one the application knows.
func (mode DynamicTabTitleMode) Valid() bool {
	switch mode {
	case DynamicTabTitleOff, DynamicTabTitleAgents, DynamicTabTitleAll:
		return true
	}
	return false
}

// NormalizeDynamicTabTitleMode falls back to the default for an unknown mode.
func NormalizeDynamicTabTitleMode(mode DynamicTabTitleMode) DynamicTabTitleMode {
	if mode.Valid() {
		return mode
	}
	return DynamicTabTitleAll
}
