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
	// connection prints before the shell exists. None of them is readiness. The
	// optional bracketed default covers "Login [admin]>" and
	// "Username [admin]>", which are still login prompts.
	loginPrompt = regexp.MustCompile(`(?i)(^|[^a-z0-9])(user\s*name|username|login|logon|account|userid|user\s*id|user|password|passwd|passcode|passphrase|pass\s*phrase|pin|密码|口令|登录|用户名|账号)(\s*\[[^\]]*\])?(\s*[:：>]\??)?\s*$`)
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
	// sensitivePrompt recognizes a password, passphrase, passcode, or PIN
	// phrase. On a line that also has the widened ">" prompt shape it is an
	// interactive credential prompt, never readiness.
	sensitivePrompt = regexp.MustCompile(`(?i)\b(passphrase|passcode|password|pin)\b`)
	// progressPercent recognizes the token of a boot/progress line such as
	// "100%" or "87.5%". It ends in a shell-like "%" but is never a prompt.
	progressPercent = regexp.MustCompile(`^[0-9]+([.,][0-9]+)?$`)
	// currencyCode recognizes a bare currency token such as "US$" or "HK$".
	// It ends in a shell-like "$" but is a price or banner fragment, not a
	// prompt.
	currencyCode = regexp.MustCompile(`^[A-Z]{2,4}$`)
	// bootProgressLabel recognizes boot/progress prose that must make the
	// bounded fallback safe-wait instead of typing into a device that is still
	// initializing. The leading boundary is a line start, whitespace, a colon,
	// or a bracket, not a generic word boundary: "U-Boot>" is a device prompt
	// ("U-" is not a boot label), while "Booting...", "boot:", and
	// "Loading kernel" still are.
	bootProgressLabel = regexp.MustCompile(`(?i)(^|[\s:\[\]()])(fsck|checking|rebuilding|loading|progress|boot|booting|copying|starting|waiting|installing|verifying|scanning|mounting|initializing|configuring|downloading|extracting|formatting|creating|updating|percent)\b`)
	// progressBar recognizes a rendered progress bar such as "[#####    ]".
	progressBar = regexp.MustCompile(`\[[#=.\- ]+\]|[#=]{3,}`)
	// markupText recognizes HTML/XML tags and the dashed angle banners such as
	// "---->System>". A lone ">" in ordinary output is not markup.
	markupText = regexp.MustCompile(`(?i)<[a-z/!?][^>]*>|[-=]{2,}>`)
	// promptDrivePathPattern is a Windows drive path: `C:\Users\deploy`.
	promptDrivePathPattern = `[A-Za-z]:\\[^\s]*`
	// promptPathPattern is one path token without spaces: "~/src",
	// "/var/log", or `C:\Users\deploy`.
	promptPathPattern = `(?:~(/[^\s]*)?|/[^\s]*|` + promptDrivePathPattern + `)`
	// promptSpacedPathPattern is a path token with at most one space, such as
	// "~/my dir", "/home/my dir", or `C:\Program Files`. A space is accepted
	// only inside a token that starts with "~", "/", or a drive letter and
	// carries a path separator, so ordinary prose with spaces is not a path.
	promptSpacedPathPattern = `(?:~/[^\s]*( [^\s]+)?|/[^\s]*( [^\s]+)?|[A-Za-z]:\\[^\s]*( [^\s]+)?)`
	// promptAnyPathPattern accepts either path token.
	promptAnyPathPattern = `(?:` + promptPathPattern + `|` + promptSpacedPathPattern + `)`
	// promptUserAtHostPattern is a user and host token: "deploy@web".
	promptUserAtHostPattern = `[A-Za-z0-9._-]+@[A-Za-z0-9._-]+`
	// userHostPrompt accepts "user@host", "user@host:~", "user@host:/var/log",
	// the spaced forms "user@host ~/src" / "user@host /var/log", and the same
	// forms with a spaced path such as "user@host:~/my dir".
	userHostPrompt = regexp.MustCompile(`^` + promptUserAtHostPattern + `(:` + promptAnyPathPattern + `)?( ` + promptAnyPathPattern + `)?$`)
	// powerShellPrompt accepts "PS C:\", "PS C:\Users\deploy", and the POSIX
	// forms "PS /home/deploy" / "PS ~/src".
	powerShellPrompt = regexp.MustCompile(`^PS ` + promptAnyPathPattern + `$`)
	// promptPathOnly accepts a bare path prompt such as "~/src" or
	// `C:\Users\deploy`.
	promptPathOnly = regexp.MustCompile(`^` + promptPathPattern + `$`)
	// singleTokenPrompt is the old rule: a hostname, user, or device prompt
	// such as "root", "Router", "mysql", or "deploy@web".
	singleTokenPrompt = regexp.MustCompile(`^\S+$`)
)

// startupPromptReady reports whether the retained output ends on a line that
// looks like a shell or device prompt. The heuristic deliberately only accepts
// a strict prompt grammar, so a login banner, MOTD, progress line, markup, or
// interactive pre-shell message cannot release the startup command too early.
// The bounded fallback covers a genuine prompt the heuristic does not
// recognize.
func startupPromptReady(tail string) bool {
	return startupPromptLineReady(lastOutputLine(tail))
}

func startupPromptLineReady(line string) bool {
	if line == "" || len(line) > 256 {
		return false
	}
	if loginPrompt.MatchString(line) || continuePrompt.MatchString(line) ||
		lastLogin.MatchString(line) || bannerText.MatchString(line) ||
		continuationStatusPrompt(line) {
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

// plausiblePromptPrefix is the strict prompt-shape grammar. It accepts exactly
// the real forms (bracket, PowerShell, user@host, path, single token, bare
// terminator) and rejects prose, markup, URLs, email-in-prose, progress bars,
// ellipses, and boot/progress labels.
func plausiblePromptPrefix(terminator byte, prefix string) bool {
	if prefix == "" {
		// A bare "$", "#", "%", or ">" is a legitimate root/user prompt.
		return true
	}
	if !containsWordRune(prefix) {
		// "->", "==>", "###": punctuation-only fragments, not prompts.
		return false
	}
	// Markup, quoting, shell operators, and embedded progress/currency tokens
	// never belong in a prompt prefix. "#" and "%" are terminators, not prefix
	// text.
	if strings.ContainsAny(prefix, "<>\"'`={}%#\t") {
		return false
	}
	// A URL ("://", "//") or an email in prose never forms a prompt.
	if strings.Contains(prefix, "://") || strings.Contains(prefix, "//") {
		return false
	}
	// An ellipsis is boot/banner prose, never a prompt.
	if strings.Contains(prefix, "...") {
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
	if terminator == '>' && sensitivePrompt.MatchString(prefix) {
		// "Enter passphrase for /home/deploy/key>".
		return false
	}
	// The explicit prompt shapes. PowerShell is a ">" prompt, so it must not
	// release on a "$", "#", or "%" line that merely starts with "PS ".
	if userHostPrompt.MatchString(prefix) || bracketPrompt(prefix) ||
		promptPathOnly.MatchString(prefix) ||
		(terminator == '>' && powerShellPrompt.MatchString(prefix)) {
		return true
	}
	// The single-token rule covers hostnames, users, and device prompts. A
	// boot/progress label that happens to be one token ("booting") is not a
	// prompt.
	if bootProgressLabel.MatchString(prefix) {
		return false
	}
	return singleTokenPrompt.MatchString(prefix)
}

// bracketPrompt accepts "[user@host ~]", "[root@host /var/log]", and the
// Debian shape "user@host ~/src]" whose closing bracket is not matched by an
// opening one. An opening bracket without its closing partner is not a shape
// this grammar accepts.
func bracketPrompt(prefix string) bool {
	hasOpen := strings.HasPrefix(prefix, "[")
	hasClose := strings.HasSuffix(prefix, "]")
	if !hasOpen && !hasClose {
		return false
	}
	if hasOpen && !hasClose {
		return false
	}
	inner := prefix
	if hasOpen {
		inner = inner[1:]
	}
	if hasClose {
		inner = inner[:len(inner)-1]
	}
	return userHostPrompt.MatchString(inner)
}

// continuationStatusPrompt reports whether a line is a shell continuation
// (PS2) prompt such as "dquote>" or "cmdand>". A bare ">" and the console form
// ">?" are primary prompts the heuristic accepts, so they are excluded.
func continuationStatusPrompt(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == ">" || trimmed == ">?" {
		return false
	}
	if continuationPrompt(trimmed) {
		return true
	}
	return strings.HasSuffix(trimmed, ">?") &&
		continuationPrompt(strings.TrimSuffix(trimmed, "?"))
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

// startupPreShellVisible reports whether the retained output still shows
// pre-shell output after the last recognized shell prompt: a login, username,
// password, or passphrase prompt, a press-Enter notice, a last-login/MOTD
// line, or boot/progress/markup output. The bounded fallback consults it so a
// device that never reaches its shell is left alone instead of having the
// startup command typed into the login or a booting kernel. A markerless MOTD
// cannot be told apart from a device prompt the heuristic does not recognize;
// that remains the fallback's reason to exist.
func startupPreShellVisible(tail string) bool {
	tail = ansiSequence.ReplaceAllString(tail, "")
	tail = strings.ReplaceAll(tail, "\r", "\n")
	lines := strings.Split(tail, "\n")
	// The last non-empty line is newer than every banner above it. A
	// prompt-shaped line that is not itself pre-shell evidence means the
	// console may already be at a prompt the strict grammar does not
	// recognize; an older "Last login:" or "Welcome to" line must not suppress
	// the bounded fallback forever.
	if last := lastNonEmptyLine(lines); promptTerminatedLine(last) && !preShellLine(last) {
		return false
	}
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
		if preShellLine(line) {
			return true
		}
	}
	return false
}

// lastNonEmptyLine returns the newest non-empty line, right-trimmed.
func lastNonEmptyLine(lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimRight(lines[index], " \t\v\f")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// promptTerminatedLine reports whether a line ends in a prompt terminator.
func promptTerminatedLine(line string) bool {
	_, _, ok := splitPromptTerminator(strings.TrimSpace(line))
	return ok
}

// preShellLine reports whether one line is pre-shell evidence: a login or
// continue prompt, a last-login/MOTD banner, a credential angle prompt, a shell
// continuation prompt, or boot/progress/markup output.
func preShellLine(line string) bool {
	return loginPrompt.MatchString(line) || continuePrompt.MatchString(line) ||
		lastLogin.MatchString(line) || bannerText.MatchString(line) ||
		sensitiveAnglePrompt(line) || continuationStatusPrompt(line) ||
		bootProgressOrMarkupLine(line)
}

// bootProgressOrMarkupLine recognizes boot, progress, and markup output that is
// never a shell prompt: a percent sign, a rendered progress bar, a tag or
// dashed angle banner, or a fsck/checking/rebuilding/loading-style label. A
// lone ">" in ordinary output is not enough; an unrecognized genuine prompt
// must still reach the bounded fallback.
func bootProgressOrMarkupLine(line string) bool {
	if strings.Contains(line, "%") {
		return true
	}
	if progressBar.MatchString(line) {
		return true
	}
	if markupText.MatchString(line) {
		return true
	}
	return bootProgressLabel.MatchString(line)
}

// lastOutputLine returns the last non-empty line of output after stripping
// ANSI styling and carriage returns. A chunk may contain several lines.
func lastOutputLine(tail string) string {
	tail = ansiSequence.ReplaceAllString(tail, "")
	tail = strings.ReplaceAll(tail, "\r", "\n")
	return lastNonEmptyLine(strings.Split(tail, "\n"))
}
