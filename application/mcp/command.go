package mcp

import (
	"regexp"
	"strings"
)

// ShellKind is the shell family used to select the default command blocklist
// groups. The values match the original Electron shell detection. Anything
// unknown, including an empty kind, deliberately falls back to the strict union
// of every group rather than silently allowing a command.
type ShellKind string

const (
	ShellKindUnknown    ShellKind = ""
	ShellKindPOSIX      ShellKind = "posix"
	ShellKindFish       ShellKind = "fish"
	ShellKindPowerShell ShellKind = "powershell"
	ShellKindCmd        ShellKind = "cmd"
)

// CommandRule is one default blocklist entry. Name is the stable, localized
// rule identifier reported to clients; Pattern is the original regular
// expression text where one exists.
type CommandRule struct {
	Name    string
	Group   string
	Pattern string

	match func(string) bool
}

var defaultCommandRules = []CommandRule{
	{
		Name:    "common.rm-recursive-force",
		Group:   "common",
		Pattern: `\brm\s+(?=[^\n;&|]*(?:-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\b|-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*\b|-[a-zA-Z]*r[a-zA-Z]*\b[^\n;&|]*-[a-zA-Z]*f[a-zA-Z]*\b|-[a-zA-Z]*f[a-zA-Z]*\b[^\n;&|]*-[a-zA-Z]*r[a-zA-Z]*\b|--recursive\b[^\n;&|]*(?:--force\b|-[a-zA-Z]*f[a-zA-Z]*\b)|(?:--force\b|-[a-zA-Z]*f[a-zA-Z]*\b)[^\n;&|]*--recursive\b))`,
		match:   matchRecursiveForceRM,
	},
	{
		Name:    "common.system-power",
		Group:   "common",
		Pattern: `\b(shutdown|reboot|poweroff|halt)\b`,
	},
	{
		Name:    "posix.mkfs",
		Group:   "posixNative",
		Pattern: `\bmkfs\.`,
	},
	{
		Name:    "posix.dd-to-device",
		Group:   "posixNative",
		Pattern: `\bdd\s+if=.*\s+of=/dev/`,
	},
	{
		Name:    "posix.redirect-to-disk",
		Group:   "posixNative",
		Pattern: `>\s*/dev/sd`,
	},
	{
		Name:    "posix.chmod-recursive-root",
		Group:   "posixNative",
		Pattern: `\bchmod\s+(-[a-zA-Z]*R[a-zA-Z]*|--recursive)\s+777\s+/`,
	},
	{
		Name:    "posix.move-root",
		Group:   "posixNative",
		Pattern: `\bmv\s+/\s`,
	},
	{
		Name:    "posix.curl-sudo-bash",
		Group:   "posixNative",
		Pattern: `\bcurl\s+.*\|\s*\bsudo\s+\bbash\b`,
	},
	{
		Name:    "posix.wget-sudo-bash",
		Group:   "posixNative",
		Pattern: `\bwget\s+.*\|\s*\bsudo\s+\bbash\b`,
	},
	{
		Name:    "posix.base64-pipe-shell",
		Group:   "posixNative",
		Pattern: `base64.*\|.*(?:ba)?sh`,
	},
	{
		Name:    "posix.fork-bomb",
		Group:   "posix",
		Pattern: `:\(\)\{\s*:\|:\&\s*\};:`,
	},
	{
		Name:    "posix.redirect-etc",
		Group:   "posix",
		Pattern: `:\s*>\s*/etc/`,
	},
	{
		Name:    "posix.eval",
		Group:   "posix",
		Pattern: `\beval\b`,
	},
	{
		Name:    "posix.command-substitution",
		Group:   "posix",
		Pattern: `\$\(`,
	},
	{
		Name:    "posix.backtick-substitution",
		Group:   "posix",
		Pattern: "`.+`",
	},
	{
		Name:    "powershell.remove-recursive-force",
		Group:   "powershell",
		Pattern: `\b(Remove-Item|del|erase|rd|ri|rm|rmdir)\b[^\n;&|]*-(rec\w*|re|r)\b[^\n;&|]*-(fo\w*|f)\b`,
	},
	{
		Name:    "powershell.remove-force-recursive",
		Group:   "powershell",
		Pattern: `\b(Remove-Item|del|erase|rd|ri|rm|rmdir)\b[^\n;&|]*-(fo\w*|f)\b[^\n;&|]*-(rec\w*|re|r)\b`,
	},
	{
		Name:    "powershell.invoke-expression",
		Group:   "powershell",
		Pattern: `\b(iex|Invoke-Expression)\b`,
	},
	{
		Name:    "powershell.download-pipe-expression",
		Group:   "powershell",
		Pattern: `\b(curl|wget|iwr|irm|Invoke-WebRequest|Invoke-RestMethod)\b[^\n;&|]*\|\s*(iex|Invoke-Expression)\b`,
	},
	{
		Name:    "powershell.execution-policy-bypass",
		Group:   "powershell",
		Pattern: `\bSet-ExecutionPolicy\b[^\n;&|]*\b(Bypass|Unrestricted)\b`,
	},
	{
		Name:    "powershell.destructive-disk-or-computer",
		Group:   "powershell",
		Pattern: `\b(Format-Volume|Clear-Disk|Initialize-Disk|Stop-Computer|Restart-Computer)\b`,
	},
}

// rmInvocation and the two flag probes are the RE2-equivalent of the original
// lookahead rule. Go's regexp has no lookahead, so the rule finds each rm
// invocation, takes the same same-line/same-command segment the lookahead
// scanned, and requires both a recursive and a force short-style flag. Because
// the original probes are unanchored, "--recursive" also satisfies the r probe
// at its second hyphen, and "--force" likewise satisfies f; this helper keeps
// that exact (deliberately over-blocking) behavior.
var (
	rmInvocation = regexp.MustCompile(`(?i)\brm\s+([^\n;&|]*)`)
	rmRecursive  = regexp.MustCompile(`(?i)-[a-z]*r[a-z]*\b|--recursive\b`)
	rmForce      = regexp.MustCompile(`(?i)-[a-z]*f[a-z]*\b|--force\b`)
)

func matchRecursiveForceRM(command string) bool {
	for _, match := range rmInvocation.FindAllStringSubmatch(command, -1) {
		segment := match[1]
		if rmRecursive.MatchString(segment) && rmForce.MatchString(segment) {
			return true
		}
	}
	return false
}

var compiledCommandRules = func() []CommandRule {
	rules := make([]CommandRule, len(defaultCommandRules))
	copy(rules, defaultCommandRules)
	for index := range rules {
		if rules[index].match != nil {
			continue
		}
		expression := regexp.MustCompile("(?i)" + rules[index].Pattern)
		rules[index].match = expression.MatchString
	}
	return rules
}()

var commandShellGroups = map[ShellKind][]string{
	ShellKindPOSIX:      {"common", "posixNative", "posix"},
	ShellKindFish:       {"common", "posixNative", "posix"},
	ShellKindCmd:        {"common", "posixNative"},
	ShellKindPowerShell: {"common", "posixNative", "powershell"},
}

// CommandRules returns the default blocklist rules selected for shell, in
// evaluation order. Unknown shells receive every group in the original union
// order (common, posixNative, posix, powershell).
func CommandRules(shell ShellKind) []CommandRule {
	groups := commandShellGroups[normalizeShellKind(shell)]
	if groups == nil {
		return append([]CommandRule(nil), compiledCommandRules...)
	}
	selected := make([]CommandRule, 0, len(compiledCommandRules))
	for _, group := range groups {
		for _, rule := range compiledCommandRules {
			if rule.Group == group {
				selected = append(selected, rule)
			}
		}
	}
	return selected
}

// CheckCommandBlocklist returns the first rule matching command for the shell.
// Matching is case-insensitive and scans the whole command, so chained, quoted,
// nested, or obfuscated occurrences are rejected exactly like the original.
func CheckCommandBlocklist(command string, shell ShellKind) (CommandRule, bool) {
	for _, rule := range CommandRules(shell) {
		if rule.match != nil && rule.match(command) {
			return rule, true
		}
	}
	return CommandRule{}, false
}

// CommandBlockedError builds the typed rejection naming the matched rule.
func CommandBlockedError(tool string, rule CommandRule) error {
	return &Error{
		Code:    CodeCommandBlocked,
		Tool:    tool,
		Rule:    rule.Name,
		Message: "Command blocked by safety policy. Rule: " + rule.Name,
	}
}

func normalizeShellKind(shell ShellKind) ShellKind {
	return ShellKind(strings.ToLower(strings.TrimSpace(string(shell))))
}
