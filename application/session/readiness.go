package session

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// startupTailLimit bounds the retained output used to recognize a prompt. A
// prompt is a short line at the end, so older banner bytes can be dropped.
const startupTailLimit = 2048

// defaultStartupReadinessTimeout is the bounded fallback for a device whose
// prompt does not match the heuristic. It is long enough for a login banner,
// MOTD, and a slow shell prompt, and short enough that an unusual console does
// not strand the startup command. A test (or a future Settings option) can
// override it through WithStartupReadinessTimeout.
const defaultStartupReadinessTimeout = 4 * time.Second

var (
	// ansiSequence matches CSI, two-byte escapes, and OSC sequences, so styling
	// around a prompt never changes the recognized line.
	ansiSequence = regexp.MustCompile(`\x1b([@-Z\\_-]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*(\x07|\x1b\\))`)
	// loginPrompt recognizes the pre-shell prompts an SSH/Telnet/serial
	// connection prints before the shell exists. None of them is readiness.
	loginPrompt = regexp.MustCompile(`(?i)(^|[^a-z0-9])(user\s*name|username|login|logon|account|userid|user\s*id|user|password|passwd|passcode|passphrase|pass\s*phrase|pin|密码|口令|登录|用户名|账号)(\s*[:：>])?\s*$`)
	// continuePrompt recognizes "press Enter to continue" banners, which often
	// end in punctuation a prompt check might otherwise accept.
	continuePrompt = regexp.MustCompile(`(?i)(press|hit)\s+.*(return|enter|any\s+key|space).*(continue|get\s+started|start|begin)?\.?\s*$`)
	// lastLogin recognizes a "Last login: ..." line, with or without a
	// timestamp. It is a pre-shell marker: a banner or MOTD may follow it, and
	// none of that output is readiness.
	lastLogin = regexp.MustCompile(`(?i)(^|[\s(\[])(last|previous)\s+login\b`)
	// bannerText recognizes the opening line of a login notice or MOTD. Such a
	// banner is pre-shell output even when no login prompt has arrived yet.
	bannerText = regexp.MustCompile(`(?i)\b(welcome to|authorized (users|access) only|message of the day|motd)\b`)
	// sensitivePrompt recognizes a password, passphrase, or passcode phrase. On
	// a line that also has the widened ">" prompt shape it is an interactive
	// credential prompt, never readiness.
	sensitivePrompt = regexp.MustCompile(`(?i)\b(passphrase|passcode|password)\b`)
	// progressPercent recognizes the token of a boot/progress line such as
	// "100%" or "87.5%". It ends in a shell-like "%" but is never a prompt.
	progressPercent = regexp.MustCompile(`^[0-9]+([.,][0-9]+)?$`)
	// currencyCode recognizes a bare currency token such as "US$" or "HK$".
	// It ends in a shell-like "$" but is a price or banner fragment, not a
	// prompt.
	currencyCode = regexp.MustCompile(`^[A-Z]{2,4}$`)
)

// startupPromptReady reports whether the retained output ends on a line that
// looks like a shell or device prompt. The heuristic deliberately only accepts
// shell terminators, so a login banner, MOTD, progress line, or interactive
// pre-shell message cannot release the startup command too early. The bounded
// fallback covers a genuine prompt the heuristic does not recognize.
func startupPromptReady(tail string) bool {
	return startupPromptLineReady(lastOutputLine(tail))
}

func startupPromptLineReady(line string) bool {
	if line == "" || len(line) > 256 {
		return false
	}
	if loginPrompt.MatchString(line) || continuePrompt.MatchString(line) ||
		lastLogin.MatchString(line) || bannerText.MatchString(line) {
		return false
	}
	return plausibleShellPrompt(line)
}

// plausibleShellPrompt rejects a line that has a shell prompt shape but is
// boot or banner output: a pure-punctuation "->", a "100%" progress line, a
// "US$" price, or a passphrase phrase in the widened ">" form. A genuine
// prompt keeps at least one letter or digit before its terminator.
func plausibleShellPrompt(line string) bool {
	terminator, prefix, ok := splitPromptTerminator(strings.TrimSpace(line))
	if !ok {
		return false
	}
	return plausiblePromptPrefix(terminator, prefix)
}

// splitPromptTerminator returns the prompt terminator and the token, path, or
// bracket prefix in front of it. A shell or device prompt ends in "$", "#",
// "%", ">", or the console-specific ">?". The prefix may contain spaces so
// bracketed RHEL/Debian prompts, PowerShell drive prompts, fish/nushell paths,
// and spaced host prompts are recognized; a bare terminator is legal too.
func splitPromptTerminator(line string) (byte, string, bool) {
	switch {
	case strings.HasSuffix(line, ">?"):
		return '>', strings.TrimSpace(line[:len(line)-2]), true
	case strings.HasSuffix(line, "$"):
		return '$', strings.TrimSpace(line[:len(line)-1]), true
	case strings.HasSuffix(line, "#"):
		return '#', strings.TrimSpace(line[:len(line)-1]), true
	case strings.HasSuffix(line, "%"):
		return '%', strings.TrimSpace(line[:len(line)-1]), true
	case strings.HasSuffix(line, ">"):
		return '>', strings.TrimSpace(line[:len(line)-1]), true
	default:
		return 0, "", false
	}
}

// plausiblePromptPrefix rejects the prompt-shaped fragments that are really
// boot output. The bare ">" form is the riskiest widening, so markup and prose
// are rejected unless the prefix carries a host, path, bracket, or drive
// signal.
func plausiblePromptPrefix(terminator byte, prefix string) bool {
	if prefix == "" {
		// A bare "$", "#", "%", or ">" is a legitimate root/user prompt.
		return true
	}
	if !containsWordRune(prefix) {
		// "->", "==>", "###": punctuation-only fragments, not prompts.
		return false
	}
	if progressPercent.MatchString(prefix) {
		// "100%", "87.5%".
		return false
	}
	if terminator == '$' && currencyCode.MatchString(prefix) {
		// "US$", "HK$".
		return false
	}
	if terminator == '>' {
		if sensitivePrompt.MatchString(prefix) {
			// "Enter passphrase for /home/deploy/key>".
			return false
		}
		if !plausibleAnglePrefix(prefix) {
			return false
		}
	}
	return true
}

// plausibleAnglePrefix keeps the widened ">" form from accepting banner or
// markup lines. A bare or single-token prompt (">", "mysql>", "Router>") is
// fine. A prompt with spaces must carry a shell-ish signal: a user/host "@", a
// path separator, a bracket, a drive colon, or a PowerShell tag. Markup
// fragments ending in ">" are rejected outright.
func plausibleAnglePrefix(prefix string) bool {
	if strings.ContainsAny(prefix, "<>\"=`{}") {
		return false
	}
	if !strings.ContainsAny(prefix, " \t") {
		return true
	}
	if strings.ContainsAny(prefix, "@/\\]:") {
		return true
	}
	return strings.HasPrefix(strings.ToUpper(prefix), "PS ")
}

// sensitiveAnglePrompt reports whether a line is a credential prompt in the
// widened ">" form, so the fallback scans it as pre-shell evidence too.
func sensitiveAnglePrompt(line string) bool {
	terminator, prefix, ok := splitPromptTerminator(strings.TrimSpace(line))
	return ok && terminator == '>' && sensitivePrompt.MatchString(prefix)
}

func containsWordRune(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return true
		}
	}
	return false
}

// startupPreShellVisible reports whether the retained output still shows a
// pre-shell prompt or banner after the last recognized shell prompt: a login,
// username, password, or passphrase prompt, a press-Enter notice, or a
// last-login/MOTD line. The bounded fallback consults it so a device that never
// reaches its shell is left alone instead of having the startup command typed
// into the login. A markerless MOTD cannot be told apart from a device prompt
// the heuristic does not recognize; that remains the fallback's reason to
// exist.
func startupPreShellVisible(tail string) bool {
	tail = ansiSequence.ReplaceAllString(tail, "")
	tail = strings.ReplaceAll(tail, "\r", "\n")
	lines := strings.Split(tail, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimRight(lines[index], " \t\v\f")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if startupPromptLineReady(line) {
			// The console reached a real prompt after any banner; reading the
			// banner again would suppress a legitimate startup command.
			return false
		}
		if loginPrompt.MatchString(line) || continuePrompt.MatchString(line) ||
			lastLogin.MatchString(line) || bannerText.MatchString(line) ||
			sensitiveAnglePrompt(line) {
			return true
		}
	}
	return false
}

// lastOutputLine returns the last non-empty line of output after stripping
// ANSI styling and carriage returns. A chunk may contain several lines.
func lastOutputLine(tail string) string {
	tail = ansiSequence.ReplaceAllString(tail, "")
	tail = strings.ReplaceAll(tail, "\r", "\n")
	lines := strings.Split(tail, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimRight(lines[index], " \t\v\f")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}
