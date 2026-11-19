package autocomplete

import (
	"strings"
	"unicode"
)

// PromptKind classifies what the terminal is waiting for at the cursor.
// Suggestions are suppressed while a command runs because Tab belongs to
// vim, less, and other full-screen programs; a program input prompt (the
// colon-labelled reads of installers, login helpers, and scripts) is the
// exception: it is waiting for typed text, and Vault snippets are useful
// there.
type PromptKind uint8

const (
	// PromptShell is an ordinary shell line editor, or no prompt at all.
	PromptShell PromptKind = iota
	// PromptProgram is a non-shell program waiting for ordinary input.
	PromptProgram
)

// ordinaryInputLabels is the positive allowlist of labels a program may use
// when it reads non-sensitive input. Anything else stays a shell context: a
// custom PS1 such as "dev: " is not a program prompt, and an unknown label
// may be a credential challenge. The Chinese labels mirror the original app.
var ordinaryInputLabels = map[string]bool{
	"email":          true,
	"e-mail":         true,
	"email address":  true,
	"e-mail address": true,
	"username":       true,
	"user name":      true,
	"user":           true,
	"userid":         true,
	"user id":        true,
	"login":          true,
	"login name":     true,
	"name":           true,
	"full name":      true,
	"account":        true,
	"account name":   true,
	"邮箱":             true,
	"电子邮箱":           true,
	"电子邮件":           true,
	"用户名":            true,
	"用户":             true,
	"姓名":             true,
	"账号":             true,
	"帐户":             true,
	"账户":             true,
	"登录名":            true,
}

// untrustedLabelWords are credential-shaped labels. They are refused even if
// the allowlist were widened later: a popup must never render where a secret
// is being read.
var untrustedLabelWords = []string{
	"secret", "passphrase", "password", "passcode", "token", "key", "pin", "otp",
}

var untrustedLabelText = []string{
	"密码", "口令", "令牌", "密钥", "验证码",
}

// sensitiveOrdinaryLabel reports whether label names a credential. Matching
// is by whole ASCII word so "monkey" or "passwordless" do not accidentally
// match "key" or "password", while the Chinese forms are matched as text.
func sensitiveOrdinaryLabel(label string) bool {
	lowered := strings.ToLower(label)
	for _, text := range untrustedLabelText {
		if strings.Contains(lowered, text) {
			return true
		}
	}
	words := strings.FieldsFunc(lowered, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, word := range words {
		for _, blocked := range untrustedLabelWords {
			if word == blocked {
				return true
			}
		}
	}
	return false
}

// ProgramInputPrompt reports whether line shows a positively identified
// program input prompt waiting for ordinary identity data, such as
// "Email: ", "Please enter your username: ", or "用户名：". The match is an
// allowlist so shell prompts and credential prompts never qualify.
//
// line may include echoed typed input after the prompt. Only the text up to
// the colon matters, so a typed address cannot change the classification.
func ProgramInputPrompt(line string) bool {
	runes := []rune(line)
	limit := len(runes)
	if limit > 200 {
		limit = 200
	}
	found := false
	for index := 0; index < limit; index++ {
		if runes[index] != ':' && runes[index] != '：' {
			continue
		}
		// A label is followed by a space, or ends the visible line. Requiring
		// the separator keeps URL schemes and "key:value" data from matching.
		if index+1 < len(runes) && runes[index+1] != ' ' && runes[index+1] != '\t' {
			continue
		}
		label, ok := colonLabel(string(runes[:index+1]))
		if ok && trustedOrdinaryInputLabel(label) {
			found = true
		}
	}
	return found
}

// PromptKindOf is ProgramInputPrompt as a kind.
func PromptKindOf(line string) PromptKind {
	if ProgramInputPrompt(line) {
		return PromptProgram
	}
	return PromptShell
}

// colonLabel extracts the label before a trailing colon. It reports false for
// an empty or over-long label and for anything that is not a short run of
// letters, numbers, spaces, and punctuation that labels use.
func colonLabel(promptText string) (string, bool) {
	trimmed := strings.TrimRight(promptText, " \t")
	if trimmed == "" {
		return "", false
	}
	runes := []rune(trimmed)
	last := runes[len(runes)-1]
	if last != ':' && last != '：' {
		return "", false
	}
	label := strings.TrimSpace(string(runes[:len(runes)-1]))
	if label == "" || len([]rune(label)) > 60 {
		return "", false
	}
	first := true
	for _, r := range label {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) &&
			!strings.ContainsRune(" ._'()-", r) {
			return "", false
		}
		if first && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return "", false
		}
		first = false
	}
	if sensitiveOrdinaryLabel(label) {
		return "", false
	}
	return label, true
}

// trustedOrdinaryInputLabel normalizes an allowlisted label after the polite
// prefixes programs commonly use ("Please enter your ..."), so the same
// prompt reads the same in every language.
func trustedOrdinaryInputLabel(label string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(label), " "))
	for _, prefix := range []string{"please ", "enter ", "input ", "type ", "your "} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	for _, prefix := range []string{"请输入", "请", "输入"} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	normalized = strings.TrimSpace(normalized)
	return ordinaryInputLabels[normalized]
}

// AcceptEraseCount is how many characters to the left of the cursor an
// accepted suggestion replaces. The caller writes the erases and the message
// through the terminal's line editor, so the count must never reach past the
// cursor: text to the right of the cursor stays where the user left it.
//
// At a shell prompt the accepted text replaces the line being typed, so the
// whole prefix left of the cursor is erased. At a program input prompt the
// typed text is a query for the popup, so only the current word is replaced.
// Mid-line acceptance always only removes the prefix left of the cursor,
// which is what keeps the rest of the line intact.
func AcceptEraseCount(input string, cursor int, program bool) int {
	runes := []rune(input)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor == 0 {
		return 0
	}
	if !program || cursor < len(runes) {
		// The shell line is replaced as a unit, and a cursor in the middle of
		// the line may only consume what is to its left.
		return cursor
	}
	prefix := runes[:cursor]
	start := len(prefix)
	for start > 0 {
		last := prefix[start-1]
		if last == ' ' || last == '\t' {
			break
		}
		start--
	}
	wordLength := len(prefix) - start
	if wordLength > 0 {
		return wordLength
	}
	// A query that ends in a space has no current word; the whole typed
	// prefix is the search text.
	return cursor
}
