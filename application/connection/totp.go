package connection

import (
	"regexp"
	"strings"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TOTP prompts the machine answers without asking the user. The original
// matched these exactly, so a server question that merely mentions a code is
// not answered with one.
var totpPromptTexts = []string{
	"[OTP Code]:",
	"[MFA auth]:",
	"Verification code:",
}

// totpExcludedPromptText marks a keyboard-interactive round as something other
// than an authenticator challenge. A code sent by SMS or email, a RADIUS
// password, a passkey prompt, or a menu choice must never receive the TOTP,
// even when the round also talks about a verification code.
var totpExcludedPromptText = regexp.MustCompile(
	`(?i)SMS|EMAIL|RADIUS|PASSKEY|MFA_CUSTOM|短信|邮件|郵件|Option>|\[Y/n\]`)

// TOTPAutoAnswer returns the code that answers req when the Host has a TOTP
// authenticator and the server asks exactly one authenticator question. at is
// the current time; a code that would expire within two seconds is replaced by
// the next step's code.
//
// It reports false for every other round, including a malformed or absent
// secret, so the caller falls back to asking the user. Neither the secret nor
// the code is logged.
func TOTPAutoAnswer(secret string, req KeyboardInteractiveRequest, at time.Time) (string, bool) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", false
	}
	if len(req.Prompts) != 1 {
		return "", false
	}
	question := req.Prompts[0].Text
	if totpExcludedPromptText.MatchString(question) {
		return "", false
	}
	context := strings.Join(nonEmptyTrimmed(req.Name, req.Instruction, question), "\n")
	if totpExcludedPromptText.MatchString(context) {
		return "", false
	}
	if !matchesTOTPPrompt(question) {
		return "", false
	}
	parsed, err := domainvault.ParseTOTP(secret)
	if err != nil {
		return "", false
	}
	code, err := domainvault.NextTOTPCode(parsed, at)
	if err != nil {
		return "", false
	}
	return code, true
}

func matchesTOTPPrompt(question string) bool {
	trimmed := strings.TrimSpace(question)
	for _, known := range totpPromptTexts {
		if trimmed == known {
			return true
		}
	}
	return false
}

func nonEmptyTrimmed(parts ...string) []string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return kept
}
