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
		// Rework 2: the cmd force-delete family. `del /f` and `rd /s` are the
		// cmd spelling of the recursive/forced deletion the rm rule blocks, and
		// `%ComSpec% /c del /f /q` hides the same hazard behind an environment
		// variable. The rule is textual like the original table: it also sees
		// the payload after a nested interpreter is expanded.
		Name:    "common.force-delete",
		Group:   "common",
		Pattern: `\b(del|erase)\b[^\n;&|]*\s/f\b|\b(rd|rmdir)\b[^\n;&|]*\s/s\b`,
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
		// Rework 2: any text piped into a shell is executable content, not only
		// the base64 spelling. This keeps the original rule names for the curl,
		// wget, and base64 cases (first match) and covers printf/echo/cat and
		// absolute shell paths.
		Name:    "posixNative.pipe-to-shell",
		Group:   "posixNative",
		Pattern: `\|\s*&?\s*(?:(?:[^\s;&|]*[\\/])?(?:sudo|doas|env|command|exec|nohup|nice|time|setsid|xargs)\s+(?:-[^\s;&|]*\s+)*)*(?:[^\s;&|]*[\\/])?(?:sh|bash|zsh|dash|fish|ksh|cmd(?:\.exe)?|powershell(?:\.exe)?|pwsh(?:\.exe)?)\b`,
		match:   matchPipeToShell,
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
		// Rework 2: find's own delete action removes every matched path, which
		// is the rm -rf hazard with a different spelling.
		Name:    "posix.find-delete",
		Group:   "posix",
		Pattern: `\bfind\b[^\n;&|]*\s-delete\b`,
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
	{
		// Rework 2: defining an alias for a blocked command and invoking it
		// later is the PowerShell spelling of the same hazard. The canonical
		// reading resolves the alias, so this rule also catches the definition
		// itself when the target is blocked.
		Name:    "powershell.alias-to-blocked-command",
		Group:   "powershell",
		Pattern: `\b(set-alias|new-alias|sal|alias)\b[^\n;&|]*\b(remove-item|del|erase|rd|ri|rm|rmdir|invoke-expression|iex)\b`,
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

// UnresolvableCommandRule is the fail-closed rule for a simple command whose
// command word is still dynamic after canonicalization. It cannot be a regular
// expression over the whole line: only the first word of each simple command is
// a command position, and ordinary arguments may legitimately contain "$".
const UnresolvableCommandRule = "common.unresolvable-command"

var unresolvableRule = CommandRule{Name: UnresolvableCommandRule, Group: "common"}

var commandShellGroups = map[ShellKind][]string{
	ShellKindPOSIX:      {"common", "posixNative", "posix"},
	ShellKindFish:       {"common", "posixNative", "posix"},
	ShellKindCmd:        {"common", "posixNative"},
	ShellKindPowerShell: {"common", "posixNative", "powershell"},
}

// CommandRules returns the default blocklist rules selected for shell, in
// evaluation order. Unknown shells receive every group in the original union
// order (common, posixNative, posix, powershell), followed by the fail-closed
// dynamic-command-word rule.
func CommandRules(shell ShellKind) []CommandRule {
	groups := commandShellGroups[normalizeShellKind(shell)]
	if groups == nil {
		selected := append([]CommandRule(nil), compiledCommandRules...)
		return append(selected, unresolvableRule)
	}
	selected := make([]CommandRule, 0, len(compiledCommandRules)+1)
	for _, group := range groups {
		for _, rule := range compiledCommandRules {
			if rule.Group == group {
				selected = append(selected, rule)
			}
		}
	}
	return append(selected, unresolvableRule)
}

// CheckCommandBlocklist returns the first rule matching command for the shell.
// Matching is case-insensitive. Every rule is tested against the original text
// and against the canonical text for the shell family, so quoting, escapes,
// line continuations, IFS expansion, simple assignments, and ANSI-C/backtick
// quoting cannot hide a hazardous command. A simple command whose command word
// is still dynamic after expansion is rejected by the named fail-closed rule
// rather than allowed.
func CheckCommandBlocklist(command string, shell ShellKind) (CommandRule, bool) {
	kind := normalizeShellKind(shell)
	canonical := canonicalTexts(command, kind)
	for _, rule := range CommandRules(kind) {
		if rule.Name == UnresolvableCommandRule {
			// The dynamic check is quote-aware, so it reads the original text:
			// a dequoted reading would turn a separator inside an ordinary
			// quoted argument into a fake command boundary.
			if hasUnresolvableCommandWord(command, kind) {
				return rule, true
			}
			continue
		}
		if rule.match != nil && rule.match(command) {
			return rule, true
		}
		for _, text := range canonical {
			if rule.match != nil && rule.match(text) {
				return rule, true
			}
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

// Escaped runes are kept out of assignment expansion and the dynamic-command
// check while canonicalization runs, then restored, so "\$HOME" stays a literal
// dollar sign instead of becoming an expansion.
const (
	escapedDollar      = ''
	escapedBacktick    = ''
	escapedSingleQuote = ''
	escapedDoubleQuote = ''
)

// CanonicalizeCommand returns the text the blocklist matches for the shell
// family. It is exported so tests and diagnostics can show exactly what quoting
// and expansion removed. Unknown shells use the POSIX reading.
func CanonicalizeCommand(command string, shell ShellKind) string {
	return canonicalTexts(command, normalizeShellKind(shell))[0]
}

// canonicalTexts returns one canonical reading per shell family. An unknown
// shell has no reliable grammar, so the strict union of all three readings is
// matched, mirroring the strict rule union.
func canonicalTexts(command string, shell ShellKind) []string {
	switch normalizeShellKind(shell) {
	case ShellKindCmd:
		return []string{canonicalizeCmd(command)}
	case ShellKindPowerShell:
		return []string{canonicalizePowerShell(command)}
	case ShellKindPOSIX, ShellKindFish:
		return []string{canonicalizePOSIX(command)}
	default:
		return []string{
			canonicalizePOSIX(command),
			canonicalizePowerShell(command),
			canonicalizeCmd(command),
		}
	}
}

// canonicalizePOSIX removes shell quoting and the escapes that construct a
// word, joins backslash-newline continuations, expands $IFS, decodes ANSI-C
// quoting, and then substitutes simple NAME=value assignments made earlier in
// the same command string. Quote removal repeats until the text stops changing,
// so a quoted argument whose content is itself quoted (bash -c '"rm" -rf x')
// is read the way the inner shell would read it.
//
// Rework 2 adds two expansion classes: bounded brace expansion ({rm,-rf,$D} is
// the word list rm -rf $D) and a nested cmd payload reading, so a cmd command
// launched from a POSIX shell is judged with cmd's own quote and caret rules.
func canonicalizePOSIX(command string) string {
	text := expandPOSIXBraces(command)
	for pass := 0; pass < 4; pass++ {
		next := canonicalizePOSIXPass(text)
		if next == text {
			break
		}
		text = next
	}
	text = expandPOSIXAssignments(text)
	text = expandNestedWindowsShell(text)
	text = strings.ReplaceAll(text, string(escapedDollar), "$")
	text = strings.ReplaceAll(text, string(escapedBacktick), "`")
	text = strings.ReplaceAll(text, string(escapedSingleQuote), "'")
	return strings.ReplaceAll(text, string(escapedDoubleQuote), `"`)
}

// canonicalizePOSIXPass performs one quote/escape/expansion pass.
func canonicalizePOSIXPass(command string) string {
	runes := []rune(command)
	var builder strings.Builder
	for index := 0; index < len(runes); {
		switch runes[index] {
		case '\\':
			if index+1 < len(runes) && runes[index+1] == '\n' {
				index += 2
				continue
			}
			if index+1 < len(runes) && runes[index+1] == '\r' &&
				index+2 < len(runes) && runes[index+2] == '\n' {
				index += 3
				continue
			}
			if index+1 < len(runes) {
				switch runes[index+1] {
				case '$':
					builder.WriteRune(escapedDollar)
				case '`':
					builder.WriteRune(escapedBacktick)
				case '\'':
					builder.WriteRune(escapedSingleQuote)
				case '"':
					builder.WriteRune(escapedDoubleQuote)
				default:
					builder.WriteRune(runes[index+1])
				}
				index += 2
				continue
			}
			index++
		case '\'':
			index++
			for index < len(runes) && runes[index] != '\'' {
				builder.WriteRune(runes[index])
				index++
			}
			if index < len(runes) {
				index++
			}
		case '"':
			index++
			for index < len(runes) {
				if runes[index] == '\\' && index+1 < len(runes) {
					switch runes[index+1] {
					case '\n':
						index += 2
					case '"', '\\':
						builder.WriteRune(runes[index+1])
						index += 2
					case '$':
						builder.WriteRune(escapedDollar)
						index += 2
					case '`':
						builder.WriteRune(escapedBacktick)
						index += 2
					default:
						builder.WriteRune(runes[index])
						index++
					}
					continue
				}
				if runes[index] == '"' {
					index++
					break
				}
				if runes[index] == '$' && expandIFS(runes, &index, &builder) {
					continue
				}
				builder.WriteRune(runes[index])
				index++
			}
		case '$':
			if index+1 < len(runes) && runes[index+1] == '\'' {
				index += 2
				index = decodeANSIC(runes, index, &builder)
				continue
			}
			if expandIFS(runes, &index, &builder) {
				continue
			}
			builder.WriteRune('$')
			index++
		default:
			builder.WriteRune(runes[index])
			index++
		}
	}
	return builder.String()
}

// expandIFS turns $IFS and ${IFS} into the word-splitting space they stand for.
func expandIFS(runes []rune, index *int, builder *strings.Builder) bool {
	rest := runes[*index:]
	if len(rest) >= 6 && string(rest[:6]) == "${IFS}" {
		builder.WriteRune(' ')
		*index += 6
		return true
	}
	if len(rest) >= 4 && string(rest[:4]) == "$IFS" {
		if len(rest) > 4 && isIdentifierRune(rest[4]) {
			return false
		}
		builder.WriteRune(' ')
		*index += 4
		return true
	}
	return false
}

// decodeANSIC reads the body of a $'...' word, applying the escape forms that
// can build a command word. It returns the index just past the closing quote.
func decodeANSIC(runes []rune, index int, builder *strings.Builder) int {
	for index < len(runes) {
		if runes[index] == '\'' {
			return index + 1
		}
		if runes[index] != '\\' || index+1 >= len(runes) {
			builder.WriteRune(runes[index])
			index++
			continue
		}
		next := runes[index+1]
		switch next {
		case 'x':
			value, width, ok := readDigits(runes[index+2:], 16, 2)
			if ok {
				builder.WriteRune(rune(value))
				index += 2 + width
				continue
			}
		case 'u', 'U':
			widthLimit := 4
			if next == 'U' {
				widthLimit = 8
			}
			value, width, ok := readDigits(runes[index+2:], 16, widthLimit)
			if ok {
				builder.WriteRune(rune(value))
				index += 2 + width
				continue
			}
		case 'n':
			builder.WriteRune('\n')
			index += 2
			continue
		case 't':
			builder.WriteRune('\t')
			index += 2
			continue
		case 'r':
			builder.WriteRune('\r')
			index += 2
			continue
		case 'a':
			builder.WriteRune('\a')
			index += 2
			continue
		case 'b':
			builder.WriteRune('\b')
			index += 2
			continue
		case 'e':
			builder.WriteRune('\x1b')
			index += 2
			continue
		case 'f':
			builder.WriteRune('\f')
			index += 2
			continue
		case 'v':
			builder.WriteRune('\v')
			index += 2
			continue
		case '\\', '\'', '"', '?':
			builder.WriteRune(next)
			index += 2
			continue
		default:
			if next >= '0' && next <= '7' {
				value, width, ok := readDigits(runes[index+1:], 8, 3)
				if ok {
					builder.WriteRune(rune(value))
					index += 1 + width
					continue
				}
			}
		}
		builder.WriteRune(next)
		index += 2
	}
	return index
}

// readDigits reads up to limit base digits and reports how many were used.
func readDigits(runes []rune, base, limit int) (int, int, bool) {
	value := 0
	width := 0
	for width < limit && width < len(runes) {
		digit := digitValue(runes[width])
		if digit < 0 || digit >= base {
			break
		}
		value = value*base + digit
		width++
	}
	return value, width, width > 0
}

func digitValue(character rune) int {
	switch {
	case character >= '0' && character <= '9':
		return int(character - '0')
	case character >= 'a' && character <= 'f':
		return int(character-'a') + 10
	case character >= 'A' && character <= 'F':
		return int(character-'A') + 10
	default:
		return -1
	}
}

// expandPOSIXAssignments substitutes simple NAME=value assignments made earlier
// in the same command string, so "x=rm; $x -rf" is seen as "x=rm; rm -rf".
// Unresolvable variables are left intact for the fail-closed rule.
func expandPOSIXAssignments(text string) string {
	runes := []rune(text)
	variables := map[string]string{}
	var builder strings.Builder
	previous := rune(' ')
	for index := 0; index < len(runes); {
		if isWordBoundary(previous) {
			if name, value, next, ok := readAssignment(runes, index); ok {
				expanded := expandPOSIXVariables(value, variables)
				variables[name] = expanded
				builder.WriteString(name)
				builder.WriteRune('=')
				builder.WriteString(expanded)
				previous = '='
				index = next
				continue
			}
		}
		if runes[index] == '$' {
			if name, next, ok := readVariableName(runes, index); ok {
				if value, known := variables[name]; known {
					builder.WriteString(value)
					previous = lastRune(value, previous)
					index = next
					continue
				}
			}
		}
		builder.WriteRune(runes[index])
		previous = runes[index]
		index++
	}
	return builder.String()
}

// readAssignment parses NAME=value at index when it is at a command boundary.
func readAssignment(runes []rune, index int) (string, string, int, bool) {
	if index >= len(runes) || !isIdentifierStart(runes[index]) {
		return "", "", index, false
	}
	end := index + 1
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}
	if end >= len(runes) || runes[end] != '=' {
		return "", "", index, false
	}
	name := string(runes[index:end])
	valueStart := end + 1
	valueEnd := valueStart
	for valueEnd < len(runes) && !isValueBoundary(runes[valueEnd]) {
		valueEnd++
	}
	return name, string(runes[valueStart:valueEnd]), valueEnd, true
}

// readVariableName parses $NAME or ${NAME} and returns the name and the index
// just past it.
func readVariableName(runes []rune, index int) (string, int, bool) {
	if index >= len(runes) || runes[index] != '$' || index+1 >= len(runes) {
		return "", index, false
	}
	if runes[index+1] == '{' {
		end := index + 2
		for end < len(runes) && isIdentifierRune(runes[end]) {
			end++
		}
		if end < len(runes) && runes[end] == '}' && end > index+2 {
			return string(runes[index+2 : end]), end + 1, true
		}
		return "", index, false
	}
	if !isIdentifierStart(runes[index+1]) {
		return "", index, false
	}
	end := index + 1
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}
	return string(runes[index+1 : end]), end, true
}

func expandPOSIXVariables(text string, variables map[string]string) string {
	if len(variables) == 0 {
		return text
	}
	runes := []rune(text)
	var builder strings.Builder
	for index := 0; index < len(runes); {
		if runes[index] == '$' {
			if name, next, ok := readVariableName(runes, index); ok {
				if value, known := variables[name]; known {
					builder.WriteString(value)
					index = next
					continue
				}
			}
		}
		builder.WriteRune(runes[index])
		index++
	}
	return builder.String()
}

func isIdentifierStart(character rune) bool {
	return character == '_' || (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z')
}

func isIdentifierRune(character rune) bool {
	return isIdentifierStart(character) || (character >= '0' && character <= '9')
}

// isWordBoundary reports whether a rune ends a word, so an assignment can start
// after it.
func isWordBoundary(character rune) bool {
	switch character {
	case ' ', '\t', '\n', '\r', ';', '&', '|', '(', ')':
		return true
	default:
		return false
	}
}

// isValueBoundary ends a simple assignment value. Quotes were already removed,
// so an unquoted separator is the practical boundary for a conservative
// expansion.
func isValueBoundary(character rune) bool {
	return character == ' ' || character == '\t' || character == '\n' ||
		character == '\r' || character == ';' || character == '&' ||
		character == '|' || character == ')' || character == '('
}

func lastRune(text string, fallback rune) rune {
	runes := []rune(text)
	if len(runes) == 0 {
		return fallback
	}
	return runes[len(runes)-1]
}

// canonicalizePowerShell reads a PowerShell command the way the shell would:
// constant string concatenation inside & (...) is folded first, then backtick
// escapes (including backtick-newline continuations), single and double quote
// characters, and doubled quote escapes are removed. Rework 2 also resolves
// aliases defined earlier in the same command string and expands ComSpec in
// command position (including a nested cmd payload).
func canonicalizePowerShell(command string) string {
	text := foldPowerShellStringConstants(command)
	text = canonicalizePowerShellQuotes(text)
	text = resolvePowerShellAliases(text)
	text = expandPowerShellComSpec(text)
	text = expandNestedWindowsShell(text)
	return text
}

// canonicalizePowerShellQuotes removes backtick escapes (including
// backtick-newline continuations), single and double quote characters, and
// doubled quote escapes. PowerShell matching is already case-insensitive.
func canonicalizePowerShellQuotes(command string) string {
	runes := []rune(command)
	var builder strings.Builder
	for index := 0; index < len(runes); {
		switch runes[index] {
		case '`':
			if index+1 < len(runes) {
				if runes[index+1] == '\n' {
					index += 2
					continue
				}
				if runes[index+1] == '\r' && index+2 < len(runes) && runes[index+2] == '\n' {
					index += 3
					continue
				}
				builder.WriteRune(runes[index+1])
				index += 2
				continue
			}
			index++
		case '\'':
			index++
			for index < len(runes) {
				if runes[index] == '\'' {
					if index+1 < len(runes) && runes[index+1] == '\'' {
						builder.WriteRune('\'')
						index += 2
						continue
					}
					index++
					break
				}
				builder.WriteRune(runes[index])
				index++
			}
		case '"':
			index++
			for index < len(runes) {
				if runes[index] == '`' && index+1 < len(runes) {
					builder.WriteRune(runes[index+1])
					index += 2
					continue
				}
				if runes[index] == '"' {
					if index+1 < len(runes) && runes[index+1] == '"' {
						builder.WriteRune('"')
						index += 2
						continue
					}
					index++
					break
				}
				builder.WriteRune(runes[index])
				index++
			}
		default:
			builder.WriteRune(runes[index])
			index++
		}
	}
	return builder.String()
}

// canonicalizeCmd removes cmd caret escapes (including caret-newline
// continuations), double quotes, and the %ComSpec% indirection. Expanding the
// variable here keeps the reading honest for both a top-level `%ComSpec% /c`
// and one nested inside another interpreter's payload.
func canonicalizeCmd(command string) string {
	text := expandCmdComSpec(canonicalizeCmdPass(command))
	// A glued switch (/c"dir", /c/dir) loses its quote before matching and
	// reads as /cdir; separate the switch so the payload is a command word.
	return gluedCmdSwitch.ReplaceAllString(text, "$1/$2 $3")
}

// canonicalizeCmdPass removes cmd caret escapes and double quotes.
func canonicalizeCmdPass(command string) string {
	runes := []rune(command)
	var builder strings.Builder
	for index := 0; index < len(runes); {
		switch runes[index] {
		case '^':
			if index+1 < len(runes) {
				if runes[index+1] == '\n' {
					index += 2
					continue
				}
				if runes[index+1] == '\r' && index+2 < len(runes) && runes[index+2] == '\n' {
					index += 3
					continue
				}
				builder.WriteRune(runes[index+1])
				index += 2
				continue
			}
			index++
		case '"':
			index++
		default:
			builder.WriteRune(runes[index])
			index++
		}
	}
	return builder.String()
}

// Bounded POSIX brace expansion. The safety net only needs the words a brace
// group can turn into, and a real shell caps pathological expansions too;
// exhausting the budget falls back to a flattened reading instead of growing
// without bound.
const (
	braceExpansionGroupBudget = 8
	braceExpansionAltLimit    = 16
)

var braceFlattener = strings.NewReplacer("{", " ", "}", " ", ",", " ")

// expandPOSIXBraces expands brace groups with at least one top-level comma,
// including nested groups, so {rm,-rf,$D} is read as the three words rm, -rf,
// and $D. Groups without a comma (including ${NAME} and find's {}) are left
// for the fail-closed variable reading.
func expandPOSIXBraces(command string) string {
	budget := braceExpansionGroupBudget
	return expandBraceText(command, &budget)
}

func expandBraceText(text string, budget *int) string {
	if *budget <= 0 {
		return braceFlattener.Replace(text)
	}
	start, end, alternatives, ok := findBraceGroup(text)
	if !ok {
		return text
	}
	*budget--
	wordStart := start
	for wordStart > 0 && !isBraceWordBoundary(text[wordStart-1]) {
		wordStart--
	}
	wordEnd := end
	for wordEnd < len(text) && !isBraceWordBoundary(text[wordEnd]) {
		wordEnd++
	}
	word := text[wordStart:wordEnd]
	groupStart, groupEnd := start-wordStart, end-wordStart
	expanded := make([]string, 0, len(alternatives))
	for _, alternative := range alternatives {
		expanded = append(expanded, expandBraceText(word[:groupStart]+alternative+word[groupEnd:], budget))
	}
	// The first group only expands one word. Continue with the rest of the
	// line so a later group, as in `echo {a,b}; {rm,-rf,$D}`, cannot hide an
	// executable command from the table.
	rest := expandBraceText(text[wordEnd:], budget)
	return text[:wordStart] + strings.Join(expanded, " ") + rest
}

func isBraceWordBoundary(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', ';', '&', '|', '(', ')':
		return true
	default:
		return false
	}
}

// findBraceGroup locates the first brace group with top-level comma
// alternatives and returns its byte span and alternatives.
func findBraceGroup(text string) (int, int, []string, bool) {
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		if start > 0 && (text[start-1] == '$' || text[start-1] == '\\') {
			continue
		}
		depth := 0
		closed := -1
		var commas []int
		for index := start; index < len(text); index++ {
			switch text[index] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					closed = index
				}
			case ',':
				if depth == 1 {
					commas = append(commas, index)
				}
			}
			if closed >= 0 {
				break
			}
		}
		if closed < 0 || len(commas) == 0 || len(commas)+1 > braceExpansionAltLimit {
			continue
		}
		alternatives := make([]string, 0, len(commas)+1)
		begin := start + 1
		for _, comma := range commas {
			alternatives = append(alternatives, text[begin:comma])
			begin = comma + 1
		}
		alternatives = append(alternatives, text[begin:closed])
		return start, closed + 1, alternatives, true
	}
	return 0, 0, nil, false
}

// cmdComSpec and powerShellComSpec expand the interpreter indirection in
// command position. Both readings are deliberately broader than either shell's
// literal grammar: the goal is to see the command that could run, not to model
// where a variable would expand.
var (
	cmdComSpec          = regexp.MustCompile(`(?i)%comspec%`)
	powerShellComSpec   = regexp.MustCompile(`(?i)\$(?:\{env:comspec\}|env:comspec)`)
	nestedCmdInvocation = regexp.MustCompile(`(?i)\bcmd(?:\.exe)?\s+(?:/[a-z]+\s+)*/(?:c|k|r)`)
	gluedCmdSwitch      = regexp.MustCompile(`(?i)(\bcmd(?:\.exe)?\s+(?:/[a-z]+\s+)*)/(c|k|r)([^\s/])`)
)

func expandCmdComSpec(text string) string {
	return cmdComSpec.ReplaceAllString(text, "cmd.exe")
}

func expandPowerShellComSpec(text string) string {
	return powerShellComSpec.ReplaceAllString(text, "cmd.exe")
}

// expandNestedWindowsShell re-reads a command that launches cmd with a /c
// payload using cmd's quote and caret rules, so `cmd /c r^m -rf x` and a
// ComSpec nested in the payload are visible to the common rules.
func expandNestedWindowsShell(text string) string {
	if !nestedCmdInvocation.MatchString(text) {
		return text
	}
	text = canonicalizeCmd(text)
	// cmd also accepts the payload glued to the switch (/c"dir", /c/dir).
	// After quote and caret removal that reads as /cdir, so separate the
	// switch again or the command word would stay hidden inside it.
	return gluedCmdSwitch.ReplaceAllString(text, "$1/$2 $3")
}

type powerShellLiteral struct {
	value string
	safe  bool
}

// readPowerShellLiteral reads a single- or double-quoted PowerShell literal.
// Double-quoted literals are safe to fold only when they contain no
// interpolation.
func readPowerShellLiteral(text string, at int) (powerShellLiteral, int, bool) {
	if at >= len(text) || (text[at] != '\'' && text[at] != '"') {
		return powerShellLiteral{}, at, false
	}
	quote := text[at]
	index := at + 1
	safe := true
	var builder strings.Builder
	for index < len(text) {
		character := text[index]
		if character == quote {
			if index+1 < len(text) && text[index+1] == quote {
				builder.WriteByte(quote)
				index += 2
				continue
			}
			return powerShellLiteral{value: builder.String(), safe: safe}, index + 1, true
		}
		if quote == '"' {
			if character == '`' && index+1 < len(text) {
				builder.WriteByte(text[index+1])
				index += 2
				continue
			}
			if character == '$' {
				safe = false
			}
		}
		builder.WriteByte(character)
		index++
	}
	return powerShellLiteral{}, at, false
}

// foldPowerShellStringConstants folds adjacent literal operands joined by +,
// so & (”+'Remove'+'-Item') becomes & ('Remove-Item') before quote removal and
// cannot hide a blocked command behind constant construction.
func foldPowerShellStringConstants(command string) string {
	text := command
	for pass := 0; pass < 8; pass++ {
		next, changed := foldOnePowerShellConcatenation(text)
		if !changed {
			break
		}
		text = next
	}
	return text
}

func foldOnePowerShellConcatenation(text string) (string, bool) {
	for start := 0; start < len(text); start++ {
		first, firstEnd, ok := readPowerShellLiteral(text, start)
		if !ok || !first.safe {
			continue
		}
		index := skipSpaces(text, firstEnd)
		if index >= len(text) || text[index] != '+' {
			continue
		}
		index = skipSpaces(text, index+1)
		second, secondEnd, ok := readPowerShellLiteral(text, index)
		if !ok || !second.safe {
			continue
		}
		folded := "'" + strings.ReplaceAll(first.value+second.value, "'", "''") + "'"
		return text[:start] + folded + text[secondEnd:], true
	}
	return text, false
}

func skipSpaces(text string, index int) int {
	for index < len(text) && (text[index] == ' ' || text[index] == '\t') {
		index++
	}
	return index
}

type shellWordSpan struct {
	start, end   int
	segmentStart bool
}

// scanShellWordSpans splits dequoted text into words and marks the first word
// of each simple command. Parentheses are treated as command boundaries, which
// is what the call operator wraps around.
func scanShellWordSpans(text string) []shellWordSpan {
	var spans []shellWordSpan
	segmentStart := true
	for index := 0; index < len(text); {
		character := text[index]
		switch character {
		case ' ', '\t', '\r':
			index++
			continue
		case '\n', ';', '&', '|':
			segmentStart = true
			index++
			continue
		case '(', ')':
			segmentStart = true
			index++
			continue
		}
		start := index
		for index < len(text) {
			character = text[index]
			if character == ' ' || character == '\t' || character == '\r' ||
				character == '\n' || character == ';' || character == '&' ||
				character == '|' || character == '(' || character == ')' {
				break
			}
			index++
		}
		spans = append(spans, shellWordSpan{start: start, end: index, segmentStart: segmentStart})
		segmentStart = false
	}
	return spans
}

// resolvePowerShellAliases rewrites command-position uses of aliases defined
// earlier in the same command string to their target. The definition itself is
// left in place so the named alias rule can report it.
func resolvePowerShellAliases(text string) string {
	spans := scanShellWordSpans(text)
	aliases := map[string]string{}
	for index, span := range spans {
		if !span.segmentStart {
			continue
		}
		switch strings.ToLower(text[span.start:span.end]) {
		case "set-alias", "new-alias", "sal", "alias":
		default:
			continue
		}
		if name, target, ok := powerShellAliasDefinition(text, spans, index); ok {
			aliases[name] = target
		}
	}
	if len(aliases) == 0 {
		return text
	}
	type edit struct {
		start, end  int
		replacement string
	}
	var edits []edit
	for _, span := range spans {
		if !span.segmentStart {
			continue
		}
		word := strings.ToLower(text[span.start:span.end])
		switch word {
		case "set-alias", "new-alias", "sal", "alias":
			continue
		}
		if target, ok := aliases[word]; ok {
			edits = append(edits, edit{start: span.start, end: span.end, replacement: target})
		}
	}
	for index := len(edits) - 1; index >= 0; index-- {
		text = text[:edits[index].start] + edits[index].replacement + text[edits[index].end:]
	}
	return text
}

func powerShellAliasDefinition(text string, spans []shellWordSpan, index int) (string, string, bool) {
	name, target := "", ""
	expect := ""
	var positional []string
	for next := index + 1; next < len(spans); next++ {
		if spans[next].segmentStart {
			break
		}
		argument := text[spans[next].start:spans[next].end]
		lower := strings.ToLower(argument)
		if expect != "" {
			switch expect {
			case "name":
				name = argument
			case "target":
				target = argument
			}
			expect = ""
			continue
		}
		switch lower {
		case "-name":
			expect = "name"
			continue
		case "-value":
			expect = "target"
			continue
		case "-scope", "-description", "-option":
			expect = "skip"
			continue
		case "-force", "-passthru", "-whatif", "-confirm":
			continue
		}
		if strings.HasPrefix(lower, "-name:") {
			name = argument[len("-name:"):]
			continue
		}
		if strings.HasPrefix(lower, "-value:") {
			target = argument[len("-value:"):]
			continue
		}
		if strings.HasPrefix(argument, "-") && argument != "-" {
			continue
		}
		positional = append(positional, argument)
	}
	if name == "" {
		if len(positional) == 0 {
			return "", "", false
		}
		if before, after, ok := strings.Cut(positional[0], "="); ok {
			name, target = before, after
		} else {
			name = positional[0]
			if len(positional) > 1 {
				target = positional[1]
			}
		}
	}
	if name == "" || target == "" {
		return "", "", false
	}
	return strings.ToLower(name), target, true
}

// hasUnresolvableCommandWord reports whether a simple command's command
// position is still dynamic. The text is read the way a shell reads it, so a
// separator inside a quoted argument is data, not a command boundary:
// `echo "a; $HOME"` stays allowed while `echo ok; $CMD` is rejected. Only
// command positions are judged, so ordinary arguments may carry "$", a
// backtick, or %NAME%.
//
// PowerShell has two statement shapes that are not command invocations and are
// therefore exempt: a bare variable expression ($env:PATH, $D) and a simple
// assignment whose value is one variable or literal word ($D = 'C:\Temp\x').
// This is a textual safety net, not a sandbox; see docs/parity/limitations.md.
func hasUnresolvableCommandWord(command string, shell ShellKind) bool {
	for _, words := range splitShellSegments(command) {
		if segmentHasDynamicCommandWord(words, shell) {
			return true
		}
	}
	return false
}

// splitShellSegments lexes a command line into simple-command word lists. It
// understands POSIX/PowerShell single and double quotes, POSIX backslash and
// cmd caret escapes (including a trailing line continuation), and keeps a
// backtick in the word so an obfuscated command word is still rejected.
func splitShellSegments(command string) [][]string {
	parts := splitShellParts(command)
	segments := make([][]string, 0, len(parts))
	for _, part := range parts {
		segments = append(segments, part.words)
	}
	return segments
}

// shellPart is one simple command and whether a pipe introduced it. The
// pipe flag lets pipe-to-shell detection look only at the command that
// actually receives standard input, while the shared lexer keeps semicolons,
// ampersands, quotes, and escapes honest.
type shellPart struct {
	words []string
	piped bool
}

func splitShellParts(command string) []shellPart {
	var segments []shellPart
	var words []string
	var word strings.Builder
	inWord := false
	pipedNext := false
	quote := rune(0)
	escaped := false
	flushWord := func() {
		if !inWord {
			return
		}
		words = append(words, word.String())
		word.Reset()
		inWord = false
	}
	flushSegment := func(separator rune) {
		flushWord()
		if len(words) > 0 {
			segments = append(segments, shellPart{words: words, piped: pipedNext})
			words = nil
			pipedNext = false
		}
		if separator == '|' {
			pipedNext = true
		}
	}
	runes := []rune(command)
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		if escaped {
			escaped = false
			if character == '\n' || character == '\r' {
				continue
			}
			word.WriteRune(character)
			inWord = true
			continue
		}
		switch quote {
		case '\'':
			if character == '\'' {
				quote = 0
				continue
			}
			word.WriteRune(character)
			inWord = true
			continue
		case '"':
			switch character {
			case '"':
				quote = 0
			case '\\', '^':
				escaped = true
			default:
				word.WriteRune(character)
				inWord = true
			}
			continue
		}
		switch character {
		case '\\', '^':
			escaped = true
			inWord = true
		case '\'':
			quote = '\''
			inWord = true
		case '"':
			quote = '"'
			inWord = true
		case ' ', '\t':
			flushWord()
		default:
			switch character {
			case ';', '&', '|', '\n', '\r':
				flushSegment(character)
			default:
				word.WriteRune(character)
				inWord = true
			}
		}
	}
	flushSegment(0)
	return segments
}

// matchPipeToShell reports whether any pipeline sends its standard input to a
// shell, including through the wrappers that are often written between the
// pipe and the interpreter (`| sudo sh`, `| env FOO=1 bash`,
// `| /usr/bin/env sh`). The word reading is quote-aware, so a quoted pipe is
// data rather than an operator.
func matchPipeToShell(command string) bool {
	for _, part := range splitShellParts(command) {
		if part.piped && pipeCommandIsShell(part.words) {
			return true
		}
	}
	return false
}

// pipeCommandIsShell walks the command position after a pipe through the same
// wrappers the fail-closed check understands.
func pipeCommandIsShell(words []string) bool {
	for index := 0; index < len(words); {
		word := strings.TrimLeft(words[index], "({!")
		word = strings.TrimRight(word, ");")
		if word == "" {
			index++
			continue
		}
		if isAssignmentWord(word) {
			index++
			continue
		}
		if isRedirectionWord(word) {
			if isBareRedirection(word) && index+1 < len(words) {
				index += 2
			} else {
				index++
			}
			continue
		}
		name := shellCommandBasename(word)
		if shellInterpreterName(name) {
			return true
		}
		switch name {
		case "timeout", "chroot", "ssh":
			index = skipWrapperOptions(words, index+1, wrapperValueOptions[name])
			if index >= len(words) {
				return false
			}
			index++
			continue
		}
		if commandWrapperWords[name] {
			index = skipWrapperOptions(words, index+1, wrapperValueOptions[name])
			for index < len(words) && isAssignmentWord(strings.TrimRight(words[index], ");")) {
				index++
			}
			continue
		}
		return false
	}
	return false
}

// shellCommandBasename removes an absolute path and the Windows extension from
// the command word, so /bin/sh, C:\Windows\System32\cmd.exe, and sh compare
// alike.
func shellCommandBasename(word string) string {
	if slash := strings.LastIndexAny(word, `/\`); slash >= 0 {
		word = word[slash+1:]
	}
	return strings.TrimSuffix(strings.ToLower(word), ".exe")
}

func shellInterpreterName(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "fish", "ksh", "cmd", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

// commandWrapperWords execute the simple command that follows them, so their
// command position must be inspected instead of the wrapper itself.
var commandWrapperWords = map[string]bool{
	"builtin": true, "command": true, "doas": true, "env": true,
	"exec": true, "ionice": true, "nice": true, "nohup": true,
	"nsenter": true, "parallel": true, "setsid": true, "stdbuf": true,
	"strace": true, "sudo": true, "time": true, "unshare": true,
	"watch": true, "xargs": true, "ltrace": true, "busybox": true,
}

// wrapperValueOptions lists the wrapper options that consume the next word, so
// `sudo -u root $CMD` judges $CMD rather than the option's value.
var wrapperValueOptions = map[string]map[string]bool{
	"env":      {"-u": true, "--unset": true, "-C": true, "--chdir": true, "-S": true, "--split-string": true},
	"sudo":     {"-u": true, "--user": true, "-g": true, "--group": true, "-C": true, "--close-from": true, "-h": true, "--host": true, "-p": true, "--prompt": true, "-r": true, "--role": true, "-t": true, "--type": true, "-U": true, "--other-user": true, "-D": true, "--chdir": true},
	"doas":     {"-u": true, "-C": true},
	"nice":     {"-n": true, "--adjustment": true},
	"ionice":   {"-c": true, "--class": true, "-n": true, "--classdata": true},
	"xargs":    {"-I": true, "-L": true, "-n": true, "-P": true, "-s": true, "-d": true, "-E": true, "-a": true, "--arg-file": true, "--delimiter": true, "--max-args": true, "--max-procs": true, "--max-chars": true, "--replace": true, "--eof": true},
	"exec":     {"-a": true},
	"time":     {"-f": true, "--format": true, "-o": true, "--output": true},
	"stdbuf":   {"-i": true, "-o": true, "-e": true},
	"nsenter":  {"-t": true, "--target": true, "-S": true, "--setuid": true, "-G": true, "--setgid": true},
	"unshare":  {"-S": true, "--setuid": true, "-G": true, "--setgid": true, "-w": true, "--wd": true},
	"strace":   {"-o": true, "-e": true, "-p": true, "-s": true, "-u": true, "-E": true, "-I": true, "-b": true, "-a": true, "-X": true, "--output": true, "--trace": true},
	"ltrace":   {"-o": true, "-e": true, "-p": true, "-s": true, "-u": true, "-a": true, "-n": true, "--output": true},
	"watch":    {"-n": true, "--interval": true, "-d": true, "--differences": true},
	"parallel": {"-j": true, "--jobs": true, "-S": true, "--sshlogin": true, "-a": true, "--arg-file": true},
	"timeout":  {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"chroot":   {"--userspec": true, "--groups": true},
	"ssh":      {"-p": true, "-i": true, "-l": true, "-o": true, "-F": true, "-J": true, "-L": true, "-R": true, "-D": true, "-W": true, "-b": true, "-c": true, "-E": true, "-e": true, "-m": true, "-O": true, "-Q": true, "-S": true, "-w": true, "-B": true},
}

func segmentHasDynamicCommandWord(words []string, shell ShellKind) bool {
	powershell := normalizeShellKind(shell) == ShellKindPowerShell
	if powershell && isPowerShellReadOrAssignment(words) {
		return false
	}
	// find's -exec family runs its next word as a command anywhere in the
	// argument list, not only in the first position.
	for index, word := range words {
		switch strings.ToLower(word) {
		case "-exec", "-execdir", "-ok", "-okdir":
			if index+1 < len(words) && isDynamicCommandWord(words[index+1]) {
				return true
			}
		}
	}
	for index := 0; index < len(words); {
		word := strings.TrimLeft(words[index], "({!")
		word = strings.TrimRight(word, ");")
		if word == "" {
			index++
			continue
		}
		if isAssignmentWord(word) {
			index++
			continue
		}
		if isRedirectionWord(word) {
			if isBareRedirection(word) {
				index += 2
			} else {
				index++
			}
			continue
		}
		name := strings.ToLower(word)
		switch name {
		case "if", "then", "else", "elif", "while", "until", "do", "done", "fi", "esac":
			index++
			continue
		case "for", "select", "case":
			index += 2 // the loop variable or case expression is not executed
			continue
		case "in":
			index++
			continue
		case "eval":
			return hasUnresolvableCommandWord(strings.Join(words[index+1:], " "), shell)
		}
		if name == "cmd" || name == "cmd.exe" {
			if payload, ok := cmdScriptArgument(words[index+1:]); ok {
				return hasUnresolvableCommandWord(payload, ShellKindCmd)
			}
			return false
		}
		switch name {
		case "timeout", "chroot", "ssh":
			index = skipWrapperOptions(words, index+1, wrapperValueOptions[name])
			if index >= len(words) {
				return false
			}
			index++ // timeout's duration, chroot's root, or ssh's destination
			continue
		}
		if shellLaunchesAScript(name) {
			if script, ok := commandScriptArgument(words[index+1:]); ok {
				return hasUnresolvableCommandWord(script, ShellKindPOSIX)
			}
			return false // an interactive shell or a script file
		}
		if commandWrapperWords[name] {
			index = skipWrapperOptions(words, index+1, wrapperValueOptions[name])
			for index < len(words) && isAssignmentWord(strings.TrimRight(words[index], ");")) {
				index++
			}
			continue
		}
		return isDynamicCommandWord(word)
	}
	return false
}

// isPowerShellReadOrAssignment recognizes the two non-invocation statement
// shapes PowerShell allows through the fail-closed rule. The assignment form is
// deliberately narrow: exactly one value word, which must itself be a variable
// or a single literal word. A value with whitespace-separated words
// ($D = Remove-Item -Recurse -Force x) is still judged as a command.
func isPowerShellReadOrAssignment(words []string) bool {
	if len(words) == 0 {
		return false
	}
	first := strings.TrimLeft(words[0], "({!")
	first = strings.TrimRight(first, ");")
	if len(words) == 1 {
		return isPowerShellVariable(first) || isPowerShellAssignmentWord(first)
	}
	if !isPowerShellVariable(first) {
		return false
	}
	valueStart := 1
	switch {
	case words[1] == "=":
		valueStart = 2
	case strings.HasPrefix(words[1], "="):
		// `$D='path'` is one word. Word-splitting after the attached value
		// would mean something else, so it stays a possible command.
		if len(words) != 2 {
			return false
		}
		value := strings.TrimPrefix(words[1], "=")
		if value == "" {
			return false
		}
		return isPowerShellVariable(value) || !isDynamicCommandWord(value)
	default:
		return false
	}
	if valueStart+1 != len(words) {
		return false
	}
	value := words[valueStart]
	return isPowerShellVariable(value) || !isDynamicCommandWord(value)
}

// isPowerShellVariable reports whether word is a PowerShell variable
// expression: $NAME, $scope:NAME, or the braced ${...} spelling.
func isPowerShellVariable(word string) bool {
	if strings.HasPrefix(word, "${") && strings.HasSuffix(word, "}") && len(word) > 3 {
		word = "$" + word[2:len(word)-1]
	}
	if len(word) < 2 || word[0] != '$' {
		return false
	}
	for index, character := range word[1:] {
		if index == 0 {
			if !isIdentifierStart(character) {
				return false
			}
			continue
		}
		if isIdentifierRune(character) || character == ':' {
			continue
		}
		return false
	}
	return true
}

// isPowerShellAssignmentWord reports whether word is a compact assignment such
// as $D=C:\Temp\x.
func isPowerShellAssignmentWord(word string) bool {
	equals := strings.IndexByte(word, '=')
	if equals <= 1 || word[0] != '$' {
		return false
	}
	return isPowerShellVariable(word[:equals])
}

// cmdScriptArgument returns the payload after cmd's /c, /k, or /r switch.
// /k keeps the interpreter open and /r is its undocumented alias; both still
// run the payload, so a dynamic command word there must fail closed. cmd also
// accepts the command glued to the switch (/c"dir"), which is why a switch
// prefix carries the rest of that word as its payload.
func cmdScriptArgument(args []string) (string, bool) {
	for index := 0; index < len(args); index++ {
		word := strings.ToLower(strings.Trim(args[index], "()"))
		switches := []string{"/c", "/k", "/r"}
		for _, control := range switches {
			if word == control {
				if index+1 >= len(args) {
					return "", false
				}
				return strings.Join(args[index+1:], " "), true
			}
			if len(word) > len(control) && strings.HasPrefix(word, control) {
				payload := args[index][len(control):]
				if rest := strings.Join(args[index+1:], " "); rest != "" {
					payload += " " + rest
				}
				return payload, true
			}
		}
	}
	return "", false
}

func shellLaunchesAScript(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "su", "runuser":
		return true
	default:
		return false
	}
}

// skipWrapperOptions skips option words and the values they consume. It stops
// at the first operand, which is the next command position or a positional
// data argument the caller consumes.
func skipWrapperOptions(words []string, index int, valueOptions map[string]bool) int {
	for index < len(words) {
		word := strings.TrimRight(words[index], ");")
		if word == "--" {
			return index + 1
		}
		if word == "-" || !strings.HasPrefix(word, "-") {
			return index
		}
		if valueOptions[word] {
			index += 2
			continue
		}
		index++
	}
	return index
}

// commandScriptArgument returns the script operand of -c/--command, including
// a combined short-option cluster such as bash -lc.
func commandScriptArgument(args []string) (string, bool) {
	for index := 0; index < len(args); index++ {
		word := strings.TrimRight(args[index], ");")
		if word == "--" {
			return "", false
		}
		if word == "-c" || word == "--command" {
			if index+1 < len(args) {
				return args[index+1], true
			}
			return "", false
		}
		if strings.HasPrefix(word, "--command=") {
			return strings.TrimPrefix(word, "--command="), true
		}
		if len(word) > 1 && word[0] == '-' && word[1] != '-' && strings.ContainsRune(word[1:], 'c') {
			if index+1 < len(args) {
				return args[index+1], true
			}
			return "", false
		}
	}
	return "", false
}

func isDynamicCommandWord(word string) bool {
	return strings.ContainsAny(word, "$`") || hasPercentVariable(word)
}

func isAssignmentWord(word string) bool {
	equals := strings.IndexByte(word, '=')
	if equals <= 0 {
		return false
	}
	for index, character := range word[:equals] {
		if index == 0 && !isIdentifierStart(character) {
			return false
		}
		if index > 0 && !isIdentifierRune(character) {
			return false
		}
	}
	return true
}

func isRedirectionWord(word string) bool {
	if word == "" {
		return false
	}
	runes := []rune(word)
	index := 0
	for index < len(runes) && runes[index] >= '0' && runes[index] <= '9' {
		index++
	}
	return index < len(runes) && (runes[index] == '>' || runes[index] == '<')
}

func isBareRedirection(word string) bool {
	trimmed := strings.TrimRight(word, "><&0123456789")
	return trimmed == ""
}

func hasPercentVariable(word string) bool {
	first := strings.IndexByte(word, '%')
	if first < 0 {
		return false
	}
	return strings.IndexByte(word[first+1:], '%') >= 0
}
