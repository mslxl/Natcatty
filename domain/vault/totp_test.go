package vault

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B seeds and outputs. The appendix uses 8-digit codes.
var (
	rfcSHA1Seed   = []byte("12345678901234567890")
	rfcSHA256Seed = []byte("12345678901234567890123456789012")
	rfcSHA512Seed = []byte("1234567890123456789012345678901234567890123456789012345678901234")

	rfcVectors = []struct {
		unix   int64
		sha1   string
		sha256 string
		sha512 string
	}{
		{59, "94287082", "46119246", "90693936"},
		{1111111109, "07081804", "68084774", "25091201"},
		{1111111111, "14050471", "67062674", "99943326"},
		{1234567890, "89005924", "91819424", "93441116"},
		{2000000000, "69279037", "90698825", "38618901"},
		{20000000000, "65353130", "77737706", "47863826"},
	}
)

func TestGenerateTOTPMatchesRFC6238Vectors(t *testing.T) {
	cases := []struct {
		algorithm TOTPAlgorithm
		seed      []byte
		column    func(int) string
	}{
		{TOTPAlgorithmSHA1, rfcSHA1Seed, func(i int) string { return rfcVectors[i].sha1 }},
		{TOTPAlgorithmSHA256, rfcSHA256Seed, func(i int) string { return rfcVectors[i].sha256 }},
		{TOTPAlgorithmSHA512, rfcSHA512Seed, func(i int) string { return rfcVectors[i].sha512 }},
	}
	for _, test := range cases {
		for index, vector := range rfcVectors {
			at := time.Unix(vector.unix, 0)
			code, err := GenerateTOTP(test.seed, test.algorithm, 8, 30, at)
			if err != nil {
				t.Fatalf("%s at %d: %v", test.algorithm, vector.unix, err)
			}
			if want := test.column(index); code != want {
				t.Errorf("%s at %d = %q, want %q", test.algorithm, vector.unix, code, want)
			}
		}
	}
}

func TestTOTPRawSecretUsesSixDigitRFC6238Default(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	code, err := TOTPCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	// RFC 6238's 8-digit SHA1 vector is 94287082; a 6-digit code is its last
	// six digits.
	if code != "287082" {
		t.Fatalf("code = %q, want 287082", code)
	}
}

func TestParseTOTPKeepsAlgorithmDigitsPeriodIssuerAndLabel(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	raw := "otpauth://totp/Example:alice%40example.com?secret=" +
		strings.ToLower(secret[:8]) + " " + strings.ToLower(secret[8:]) +
		"&issuer=Example&algorithm=SHA256&digits=7&period=45"

	parsed, err := ParseTOTP(raw)
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	if parsed.Algorithm() != TOTPAlgorithmSHA256 {
		t.Errorf("algorithm = %q", parsed.Algorithm())
	}
	if parsed.Digits() != 7 || parsed.Period() != 45 {
		t.Errorf("digits/period = %d/%d", parsed.Digits(), parsed.Period())
	}
	if parsed.Issuer() != "Example" || parsed.Label() != "alice@example.com" {
		t.Errorf("issuer/label = %q/%q", parsed.Issuer(), parsed.Label())
	}
	if !strings.Contains(parsed.URI(), "algorithm=SHA256") ||
		!strings.Contains(parsed.URI(), "digits=7") ||
		!strings.Contains(parsed.URI(), "period=45") ||
		!strings.Contains(parsed.URI(), "issuer=Example") {
		t.Errorf("canonical URI = %q", parsed.URI())
	}
}

func TestParseTOTPAcceptsRawBase32WithDefaults(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	parsed, err := ParseTOTP("  " + strings.ToLower(secret) + "  ")
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	if parsed.Algorithm() != TOTPAlgorithmSHA1 {
		t.Errorf("algorithm = %q", parsed.Algorithm())
	}
	if parsed.Digits() != 6 || parsed.Period() != 30 {
		t.Errorf("digits/period = %d/%d", parsed.Digits(), parsed.Period())
	}
	if parsed.Issuer() != "" || parsed.Label() != "" {
		t.Errorf("issuer/label = %q/%q", parsed.Issuer(), parsed.Label())
	}
}

func TestParseTOTPAcceptsSHA1SpellingAndUppercaseType(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	parsed, err := ParseTOTP("OTPAUTH://TOTP/JumpServer?secret=" + secret + "&algorithm=SHA-1")
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	if parsed.Algorithm() != TOTPAlgorithmSHA1 {
		t.Errorf("algorithm = %q", parsed.Algorithm())
	}
	if parsed.Issuer() != "" || parsed.Label() != "JumpServer" {
		t.Errorf("issuer/label = %q/%q", parsed.Issuer(), parsed.Label())
	}
}

func TestParseTOTPSplitsIssuerOutOfTheLabel(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	parsed, err := ParseTOTP("otpauth://totp/JumpServer:root?secret=" + secret)
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	if parsed.Issuer() != "JumpServer" || parsed.Label() != "root" {
		t.Fatalf("issuer/label = %q/%q", parsed.Issuer(), parsed.Label())
	}
}

func TestParseTOTPRejectsHOTPMigrationAndBadParameters(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	tests := []struct {
		name   string
		input  string
		reason TOTPErrorReason
	}{
		{"hotp", "otpauth://hotp/Example:alice?secret=" + secret, TOTPReasonHOTP},
		{"migration", "otpauth-migration://offline?data=abc", TOTPReasonMigration},
		{"empty", "   ", TOTPReasonEmpty},
		{"missing secret", "otpauth://totp/Example:alice", TOTPReasonMissingSecret},
		{"unsupported type", "otpauth://push/Example:alice?secret=" + secret, TOTPReasonUnsupportedType},
		{"not a secret", "not a secret", TOTPReasonInvalidSecret},
		{"too short", "AAAA", TOTPReasonInvalidSecret},
		{"algorithm", "otpauth://totp/A?secret=" + secret + "&algorithm=SHA384", TOTPReasonInvalidAlgorithm},
		{"digits low", "otpauth://totp/A?secret=" + secret + "&digits=5", TOTPReasonInvalidDigits},
		{"digits high", "otpauth://totp/A?secret=" + secret + "&digits=9", TOTPReasonInvalidDigits},
		{"period low", "otpauth://totp/A?secret=" + secret + "&period=0", TOTPReasonInvalidPeriod},
		{"period high", "otpauth://totp/A?secret=" + secret + "&period=301", TOTPReasonInvalidPeriod},
		{"bare scheme", "otpauth:", TOTPReasonInvalidURI},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseTOTP(test.input)
			var typed *TOTPError
			if !errors.As(err, &typed) {
				t.Fatalf("ParseTOTP(%q) error = %v, want *TOTPError", test.input, err)
			}
			if typed.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", typed.Reason, test.reason)
			}
			if strings.Contains(err.Error(), "secret") && test.input != "" &&
				strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked the secret: %v", err)
			}
		})
	}
}

func TestTOTPURIRoundTrips(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA256Seed)
	original, err := ParseTOTP("otpauth://totp/Example:alice%40example.com?secret=" + secret +
		"&issuer=Example&algorithm=SHA256&digits=7&period=45")
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	reparsed, err := ParseTOTP(original.URI())
	if err != nil {
		t.Fatalf("ParseTOTP(canonical): %v", err)
	}
	if reparsed.Algorithm() != original.Algorithm() ||
		reparsed.Digits() != original.Digits() ||
		reparsed.Period() != original.Period() ||
		reparsed.Issuer() != original.Issuer() ||
		reparsed.Label() != original.Label() {
		t.Fatalf("round trip = %+v, want %+v", reparsed, original)
	}
	if reparsed.URI() != original.URI() {
		t.Fatalf("canonical URI changed: %q -> %q", original.URI(), reparsed.URI())
	}
	if strings.Contains(original.URI(), "=") && strings.Contains(original.URI(), "secret="+secret+"=") {
		t.Fatalf("secret kept Base32 padding: %q", original.URI())
	}
}

func TestTOTPStringRedactsTheSecret(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	parsed, err := ParseTOTP(secret)
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	if strings.Contains(parsed.String(), secret) || strings.Contains(parsed.String(), "GEZDG") {
		t.Fatalf("String leaked the secret: %q", parsed.String())
	}
}

func TestNextTOTPCodeWaitsOutTheLastTwoSeconds(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	parsed, err := ParseTOTP(secret)
	if err != nil {
		t.Fatalf("ParseTOTP: %v", err)
	}
	soon, err := NextTOTPCode(parsed, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("NextTOTPCode: %v", err)
	}
	next, err := parsed.Code(time.Unix(60, 0))
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if soon != next {
		t.Fatalf("code at 59s = %q, want the 60s step %q", soon, next)
	}
	early, err := NextTOTPCode(parsed, time.Unix(15, 0))
	if err != nil {
		t.Fatalf("NextTOTPCode: %v", err)
	}
	current, err := parsed.Code(time.Unix(15, 0))
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if early != current {
		t.Fatalf("code at 15s = %q, want the current step %q", early, current)
	}
}

// TestNextTOTPCodeScalesTheTailToThePeriod pins the next-step threshold at
// min(2s, period/2). A short period must still answer with the current step;
// only a genuine crossing of the threshold (remaining <= tail) uses the next.
func TestNextTOTPCodeScalesTheTailToThePeriod(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	cases := []struct {
		name     string
		period   int
		unix     int64
		wantNext bool
	}{
		{"period 1 keeps the current step", 1, 100, false},
		{"period 2 keeps the current step at the start", 2, 100, false},
		{"period 2 boundary at half the period returns the next step", 2, 101, true},
		{"period 30 keeps the current step well before the tail", 30, 15, false},
		{"period 30 keeps the current step just above the tail", 30, 27, false},
		{"period 30 boundary at two seconds returns the next step", 30, 28, true},
		{"period 30 inside the tail returns the next step", 30, 29, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParseTOTP("otpauth://totp/A?secret=" + secret +
				"&period=" + strconv.Itoa(test.period))
			if err != nil {
				t.Fatalf("ParseTOTP: %v", err)
			}
			at := time.Unix(test.unix, 0)
			code, err := NextTOTPCode(parsed, at)
			if err != nil {
				t.Fatalf("NextTOTPCode: %v", err)
			}
			step := int64(test.period)
			stepStart := at.Unix() / step * step
			current, err := parsed.Code(time.Unix(stepStart, 0))
			if err != nil {
				t.Fatalf("current code: %v", err)
			}
			next, err := parsed.Code(time.Unix(stepStart+step, 0))
			if err != nil {
				t.Fatalf("next code: %v", err)
			}
			if current == next {
				t.Fatalf("fixture is ambiguous: current and next code are both %q", current)
			}
			want := current
			if test.wantNext {
				want = next
			}
			if code != want {
				t.Fatalf("code at %ds (period %ds) = %q, want %q (current %q, next %q)",
					test.unix, test.period, code, want, current, next)
			}
		})
	}
}

func TestSelectTOTPFromQRPayloads(t *testing.T) {
	secret := totpBase32.EncodeToString(rfcSHA1Seed)
	uri := "otpauth://totp/Example:alice?secret=" + secret
	selected, err := SelectTOTPFromQRPayloads([]string{
		"otpauth://hotp/Example:alice?secret=" + secret,
		uri,
	})
	if err != nil {
		t.Fatalf("SelectTOTPFromQRPayloads: %v", err)
	}
	if !strings.HasPrefix(selected.URI(), "otpauth://totp/") {
		t.Fatalf("selected URI = %q", selected.URI())
	}

	for _, test := range []struct {
		name    string
		payload []string
		reason  TOTPErrorReason
	}{
		{"hotp", []string{"otpauth://hotp/A?secret=" + secret}, TOTPReasonHOTP},
		{"migration", []string{"otpauth-migration://offline?data=abc"}, TOTPReasonMigration},
		{"not found", []string{"hello"}, TOTPReasonNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectTOTPFromQRPayloads(test.payload)
			var typed *TOTPError
			if !errors.As(err, &typed) || typed.Reason != test.reason {
				t.Fatalf("error = %v, want reason %q", err, test.reason)
			}
		})
	}
}
