package session

import (
	"regexp"
	"strings"
	"time"
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
	// lastLogin recognizes the "Last login: ..." line many systems print
	// immediately before the shell prompt when the timestamp is omitted.
	lastLogin = regexp.MustCompile(`(?i)(^|[\s(\[])(last|previous)\s+login(\s*[:：>])?\s*$`)
	// shellPrompt accepts a last line that ends in a shell or device
	// terminator: "$", "#", "%", or a bare "Router>". The ">" form requires a
	// single token so HTML or prose banners ending in ">" are not mistaken for
	// a prompt.
	shellPrompt = regexp.MustCompile(`^[$#%]\s*$|^[^\s<>]+[$#%]\s*$|^[^\s<>]+>\s*$`)
)

// startupPromptReady reports whether the retained output ends on a line that
// looks like a shell or device prompt. The heuristic deliberately only accepts
// shell terminators, so a login banner, MOTD, progress line, or interactive
// pre-shell message cannot release the startup command too early. The bounded
// fallback covers a genuine prompt the heuristic does not recognize.
func startupPromptReady(tail string) bool {
	line := lastOutputLine(tail)
	if line == "" || len(line) > 256 {
		return false
	}
	if loginPrompt.MatchString(line) || continuePrompt.MatchString(line) ||
		lastLogin.MatchString(line) {
		return false
	}
	return shellPrompt.MatchString(line)
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
