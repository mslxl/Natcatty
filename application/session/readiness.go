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
	// shellPrompt accepts a last line that ends in a shell or device
	// terminator: "$", "#", "%", or a bare "Router>". The ">" form requires a
	// single token so HTML or prose banners ending in ">" are not mistaken for
	// a prompt.
	shellPrompt = regexp.MustCompile(`^[$#%]\s*$|^[^\s<>]+[$#%]\s*$|^[^\s<>]+>\s*$`)
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

// plausibleShellPrompt rejects a line that matches shellPrompt's shape but is
// boot or banner output: a pure-punctuation "->", a "100%" progress line, or
// a "US$" price. These lines appear before a login banner, so releasing on
// them would type the startup command into the next login prompt. A genuine
// prompt keeps at least one letter or digit before its terminator.
func plausibleShellPrompt(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || !shellPrompt.MatchString(line) {
		return false
	}
	terminator := line[len(line)-1]
	prefix := line[:len(line)-1]
	if prefix == "" {
		// A bare "$", "#", or "%" is a legitimate root/user prompt.
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
	return true
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
// username, or password prompt, a press-Enter notice, or a last-login/MOTD
// line. The bounded fallback consults it so a device that never reaches its
// shell is left alone instead of having the startup command typed into the
// login. A markerless MOTD cannot be told apart from a device prompt the
// heuristic does not recognize; that remains the fallback's reason to exist.
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
			lastLogin.MatchString(line) || bannerText.MatchString(line) {
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
