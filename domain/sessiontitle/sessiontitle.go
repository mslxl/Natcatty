// Package sessiontitle resolves the title a Session's Tab and Pane show. The
// connection label (a user rename or the Host label) is the base; a shell's
// OSC 0/2 title replaces it according to the Dynamic tab title mode, matching
// the original application's behavior.
package sessiontitle

import (
	"regexp"
	"strings"

	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
)

// LocalTerminalLabel names a local shell Session. The original uses this exact
// English label for every local Session, so it is a fixed name rather than a
// localized message.
const LocalTerminalLabel = "Local Terminal"

// Input is the title-relevant state of one Session.
type Input struct {
	// CustomName is the user's rename. It wins over both the Host label and
	// the shell-reported title.
	CustomName string
	// HostLabel is the current Host label, or a serial or local label.
	HostLabel string
	// DynamicTitle is the last OSC 0/2 title reported by the shell, or "".
	DynamicTitle string
	// Agent reports that the Session runs a coding CLI agent. "agent" mode
	// follows the dynamic title only for those Sessions.
	Agent bool
}

// ConnectionLabel is the static label of a Session: the user rename, else the
// Host label.
func ConnectionLabel(customName, hostLabel string) string {
	if name := strings.TrimSpace(customName); name != "" {
		return name
	}
	return strings.TrimSpace(hostLabel)
}

// Resolve returns the title a Session Tab or Pane shows. A user rename always
// wins; otherwise the dynamic title is used when the mode allows it and the
// shell reported one. An empty result means the view falls back to its
// numbered local-session name.
func Resolve(in Input, mode modelterminal.DynamicTabTitleMode) string {
	connection := ConnectionLabel(in.CustomName, in.HostLabel)
	if mode == modelterminal.DynamicTabTitleOff {
		return connection
	}
	if name := strings.TrimSpace(in.CustomName); name != "" {
		return name
	}
	if mode == modelterminal.DynamicTabTitleAgents && !in.Agent {
		return connection
	}
	dynamic := strings.TrimSpace(in.DynamicTitle)
	if dynamic == "" {
		return connection
	}
	if normalized := Normalize(dynamic); normalized != "" {
		return normalized
	}
	return dynamic
}

var (
	brailleSpinnerPrefix = regexp.MustCompile(`^[\s⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏✳✴✶✻✽❋◆◇]+`)
	actionRequiredPrefix = regexp.MustCompile(`(?i)^\[\s*[!.]\s*\]\s*(?:action required\s*)?`)
	leadingSeparator     = regexp.MustCompile(`^[\s·•….]+`)
)

// Normalize removes the decoration coding CLI agents put around their titles:
// braille spinners, an "[!] Action Required" marker, and leading separators.
func Normalize(title string) string {
	normalized := strings.TrimSpace(title)
	normalized = strings.TrimSpace(brailleSpinnerPrefix.ReplaceAllString(normalized, ""))
	normalized = strings.TrimSpace(actionRequiredPrefix.ReplaceAllString(normalized, ""))
	normalized = strings.TrimSpace(leadingSeparator.ReplaceAllString(normalized, ""))
	return strings.TrimSpace(normalized)
}

// agentTitlePhrases are the words coding CLI agents, and the shells around
// them, put in OSC titles. Matching mirrors the original's provider signals.
var agentTitlePhrases = []string{
	"claude code", "claude", "anthropic",
	"opencode",
	"codex", "chatgpt",
	"github copilot", "copilot",
	"codebuddy",
	"gemini",
	"moonshot", "kimi",
	"factory droid", "factory ai", "droid",
	"cursor agent", "cursor",
	"grok build", "grok",
	"mimo code", "mimocode", "mimo",
}

// codexStatusWords are the status words Codex puts beside its spinner.
var codexStatusWords = []string{"working", "thinking", "ready", "waiting"}

// brailleSpinnerFrames are the dot-spinner frames Codex and other agent TUIs
// cycle through in the title.
var brailleSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// AgentSession updates the "this Session runs a coding CLI agent" decision
// from a new OSC title. The original tracks the provider across title updates:
// an ambiguous title (for example a Codex project name) keeps the previous
// decision, while an empty or shell-like title clears it. Native Sessions have
// no launch-command provider tracking, so the shell-reported titles are the
// signal.
func AgentSession(previous bool, title string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}
	if LooksLikeAgentTitle(title) {
		return true
	}
	if shellLikeTitle(title) {
		return false
	}
	return previous
}

var (
	shellTitlePattern     = regexp.MustCompile(`(?i)^(?:bash|zsh|fish|pwsh|powershell|sh|nu|xonsh|cmd)(?:\s|$|[(@])`)
	shellPathTitlePattern = regexp.MustCompile(`^(?:(?:[^@\s:]+@)?[^:\s]+:)?(?:~(?:\/|$)|\/|[A-Za-z]:[\\/])`)
)

// shellLikeTitle reports whether a title names a shell or a prompt with a
// path, which means an agent Session has ended or been replaced.
func shellLikeTitle(title string) bool {
	trimmed := strings.TrimSpace(title)
	return shellTitlePattern.MatchString(trimmed) || shellPathTitlePattern.MatchString(trimmed)
}

// LooksLikeAgentTitle reports whether an OSC title carries a coding CLI's
// signals. Native Sessions have no launch-command provider tracking yet, so
// the shell-reported title is the signal that selects "agent" mode.
func LooksLikeAgentTitle(title string) bool {
	raw := strings.TrimSpace(title)
	if raw == "" {
		return false
	}
	for _, phrase := range agentTitlePhrases {
		if includesPhrase(raw, phrase) {
			return true
		}
	}
	if strings.Contains(raw, "✳") {
		return true
	}
	if hasBrailleSpinner(raw) {
		stripped := strings.ToLower(Normalize(raw))
		for _, word := range codexStatusWords {
			if includesPhrase(stripped, word) {
				return true
			}
		}
	}
	return false
}

// hasBrailleSpinner reports whether any of the spinner frames appears in
// title.
func hasBrailleSpinner(title string) bool {
	for _, frame := range brailleSpinnerFrames {
		if strings.Contains(title, frame) {
			return true
		}
	}
	return false
}

// includesPhrase reports whether phrase appears in title with non-alphanumeric
// boundaries on both sides, so "claude" matches "✳ claude: task" but not
// "claudette".
func includesPhrase(title, phrase string) bool {
	haystack := strings.ToLower(title)
	needle := strings.ToLower(strings.TrimSpace(phrase))
	if needle == "" {
		return false
	}
	for start := 0; start <= len(haystack)-len(needle); {
		index := strings.Index(haystack[start:], needle)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(needle)
		if (index == 0 || !isASCIIAlphanumeric(haystack[index-1])) &&
			(end == len(haystack) || !isASCIIAlphanumeric(haystack[end])) {
			return true
		}
		start = index + 1
	}
	return false
}

func isASCIIAlphanumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
