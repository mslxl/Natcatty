package connection

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

var totpTestBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func totpTestSecret() string {
	return totpTestBase32.EncodeToString([]byte("12345678901234567890"))
}

func totpTestRound(question string) KeyboardInteractiveRequest {
	return KeyboardInteractiveRequest{
		Hostname: "example.test",
		Username: "alice",
		Prompts:  []KeyboardInteractivePrompt{{Text: question, Echo: true}},
	}
}

func TestTOTPAutoAnswerFillsAnAuthenticatorPrompt(t *testing.T) {
	secret := totpTestSecret()
	at := time.Unix(15, 0)
	want, err := domainvault.TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	for _, question := range []string{"Verification code:", "Verification code: ", "  [OTP Code]:", "[MFA auth]:\t"} {
		t.Run(strings.TrimSpace(question), func(t *testing.T) {
			code, ok := TOTPAutoAnswer(secret, totpTestRound(question), at)
			if !ok {
				t.Fatalf("TOTPAutoAnswer(%q) did not answer", question)
			}
			if code != want {
				t.Fatalf("code = %q, want %q", code, want)
			}
		})
	}
}

func TestTOTPAutoAnswerAcceptsAnOtpauthURI(t *testing.T) {
	secret := totpTestSecret()
	uri := "otpauth://totp/Example:alice?secret=" + secret + "&issuer=Example"
	at := time.Unix(15, 0)
	want, err := domainvault.TOTPCode(uri, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	code, ok := TOTPAutoAnswer(uri, totpTestRound("Verification code:"), at)
	if !ok || code != want {
		t.Fatalf("code = %q ok = %v, want %q", code, ok, want)
	}
}

func TestTOTPAutoAnswerDeclinesEveryOtherRound(t *testing.T) {
	secret := totpTestSecret()
	at := time.Unix(1234567890, 0)
	twoPrompts := KeyboardInteractiveRequest{Prompts: []KeyboardInteractivePrompt{
		{Text: "Verification code:", Echo: true},
		{Text: "Password:", Echo: false},
	}}
	tests := []struct {
		name   string
		secret string
		round  KeyboardInteractiveRequest
	}{
		{"no secret", "", totpTestRound("Verification code:")},
		{"password prompt", secret, totpTestRound("Password:")},
		{"question inside a sentence", secret, totpTestRound("Enter your verification code:")},
		{"two prompts", secret, twoPrompts},
		{"no prompts", secret, KeyboardInteractiveRequest{}},
		{"sms channel", secret, KeyboardInteractiveRequest{
			Instruction: "We sent a code by SMS.",
			Prompts:     []KeyboardInteractivePrompt{{Text: "Verification code:", Echo: true}},
		}},
		{"email instruction", secret, KeyboardInteractiveRequest{
			Instruction: "Check your EMAIL for the code",
			Prompts:     []KeyboardInteractivePrompt{{Text: "Verification code:", Echo: true}},
		}},
		{"confirmation menu", secret, KeyboardInteractiveRequest{
			Prompts: []KeyboardInteractivePrompt{{Text: "[Y/n]", Echo: true}},
		}},
		{"malformed secret", "not a secret", totpTestRound("Verification code:")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if code, ok := TOTPAutoAnswer(test.secret, test.round, at); ok {
				t.Fatalf("unexpected answer %q", code)
			}
		})
	}
}

func TestTOTPAutoAnswerUsesTheNextStepNearExpiry(t *testing.T) {
	secret := totpTestSecret()
	parsed, err := domainvault.ParseTOTP(secret)
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	next, err := parsed.Code(time.Unix(60, 0))
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	code, ok := TOTPAutoAnswer(secret, totpTestRound("Verification code:"), time.Unix(59, 0))
	if !ok || code != next {
		t.Fatalf("code = %q ok = %v, want the next step %q", code, ok, next)
	}
}

func TestBuildAuthPlanCarriesTheTOTPSecret(t *testing.T) {
	plan := BuildAuthPlan(AuthSpec{
		Username:   "alice",
		Mode:       AuthModePassword,
		TOTPSecret: "otpauth://totp/Example:alice?secret=" + totpTestSecret(),
	})
	if plan.TOTPSecret == "" {
		t.Fatal("the plan lost the TOTP secret")
	}
}
