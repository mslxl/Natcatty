package vault

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TOTPAlgorithm is the HMAC hash an authenticator uses. SHA1 is the common
// default; SHA256 and SHA512 are accepted because otpauth:// URIs may name
// them.
type TOTPAlgorithm string

const (
	TOTPAlgorithmSHA1   TOTPAlgorithm = "SHA1"
	TOTPAlgorithmSHA256 TOTPAlgorithm = "SHA256"
	TOTPAlgorithmSHA512 TOTPAlgorithm = "SHA512"
)

const (
	// DefaultTOTPDigits and DefaultTOTPPeriod are the RFC 6238 defaults a raw
	// Base32 secret inherits.
	DefaultTOTPDigits = 6
	DefaultTOTPPeriod = 30

	minTOTPSecretBytes = 10
	maxTOTPSecretBytes = 128
	maxTOTPDigits      = 8
	maxTOTPPeriod      = 300
)

// TOTPErrorReason identifies why a TOTP input was rejected. The UI maps it to a
// localized message; the reason string never contains the input.
type TOTPErrorReason string

const (
	TOTPReasonEmpty            TOTPErrorReason = "empty"
	TOTPReasonMigration        TOTPErrorReason = "migration"
	TOTPReasonHOTP             TOTPErrorReason = "hotp"
	TOTPReasonInvalidURI       TOTPErrorReason = "invalid-uri"
	TOTPReasonMissingSecret    TOTPErrorReason = "missing-secret"
	TOTPReasonInvalidSecret    TOTPErrorReason = "invalid-secret"
	TOTPReasonInvalidAlgorithm TOTPErrorReason = "invalid-algorithm"
	TOTPReasonInvalidDigits    TOTPErrorReason = "invalid-digits"
	TOTPReasonInvalidPeriod    TOTPErrorReason = "invalid-period"
	TOTPReasonUnsupportedType  TOTPErrorReason = "unsupported-type"
	TOTPReasonNotFound         TOTPErrorReason = "not-found"
)

// TOTPError reports a rejected TOTP input. It deliberately carries no input,
// so an error message can never leak the secret or a code.
type TOTPError struct {
	Reason TOTPErrorReason
}

func (e *TOTPError) Error() string {
	return "vault: totp " + string(e.Reason)
}

func totpError(reason TOTPErrorReason) error {
	return &TOTPError{Reason: reason}
}

// TOTP is one parsed time-based one-time password configuration. The secret
// stays unexported and the String form is redacted so a stray log statement
// cannot print the key.
type TOTP struct {
	secret    []byte
	algorithm TOTPAlgorithm
	digits    int
	period    int
	issuer    string
	label     string
}

// Algorithm reports the HMAC hash.
func (t TOTP) Algorithm() TOTPAlgorithm { return t.algorithm }

// Digits reports the code length.
func (t TOTP) Digits() int { return t.digits }

// Period reports the code lifetime in seconds.
func (t TOTP) Period() int { return t.period }

// Issuer reports the optional service name.
func (t TOTP) Issuer() string { return t.issuer }

// Label reports the optional account name.
func (t TOTP) Label() string { return t.label }

// String is the redacted diagnostic form. It never contains the secret.
func (t TOTP) String() string {
	return fmt.Sprintf("TOTP(%s:%s/%s/%d/%d)", t.issuer, t.label, t.algorithm, t.digits, t.period)
}

var totpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ParseTOTP accepts an otpauth://totp URI or a raw Base32 secret. It returns a
// *TOTPError with a stable reason when the input cannot describe a TOTP.
func ParseTOTP(input string) (TOTP, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return TOTP{}, totpError(TOTPReasonEmpty)
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "otpauth-migration:") {
		return TOTP{}, totpError(TOTPReasonMigration)
	}
	if strings.HasPrefix(lower, "otpauth:") {
		return parseTOTPURI(text)
	}
	return parseRawTOTPSecret(text)
}

// NormalizeTOTP returns the canonical otpauth://totp URI for a URI or raw
// Base32 secret. The URI carries the secret and must be treated as credential
// material.
func NormalizeTOTP(input string) (string, error) {
	parsed, err := ParseTOTP(input)
	if err != nil {
		return "", err
	}
	return parsed.URI(), nil
}

// TOTPCode computes the current code for a URI or raw Base32 secret.
func TOTPCode(input string, at time.Time) (string, error) {
	parsed, err := ParseTOTP(input)
	if err != nil {
		return "", err
	}
	return parsed.Code(at)
}

// Code computes the code for this configuration at one instant.
func (t TOTP) Code(at time.Time) (string, error) {
	return GenerateTOTP(t.secret, t.algorithm, t.digits, t.period, at)
}

// URI renders the canonical otpauth://totp URI. It contains the secret.
func (t TOTP) URI() string {
	account := t.label
	if account == "" {
		account = t.issuer
	}
	if account == "" {
		account = "TOTP"
	}
	pathLabel := account
	if t.issuer != "" && t.label != "" &&
		!strings.HasPrefix(strings.ToLower(t.label), strings.ToLower(t.issuer)+":") {
		pathLabel = t.issuer + ":" + t.label
	}
	query := url.Values{}
	query.Set("secret", totpBase32.EncodeToString(t.secret))
	query.Set("algorithm", string(t.algorithm))
	query.Set("digits", strconv.Itoa(t.digits))
	query.Set("period", strconv.Itoa(t.period))
	if t.issuer != "" {
		query.Set("issuer", t.issuer)
	}
	return "otpauth://totp/" + url.PathEscape(pathLabel) + "?" + query.Encode()
}

// SelectTOTPFromQRPayloads picks the first decodable TOTP payload. A QR image
// may hold more than one symbol; HOTP and migration codes are reported as
// stable reasons instead of being silently treated as TOTP.
func SelectTOTPFromQRPayloads(payloads []string) (TOTP, error) {
	sawHOTP := false
	sawMigration := false
	for _, payload := range payloads {
		parsed, err := ParseTOTP(payload)
		if err == nil {
			return parsed, nil
		}
		var typed *TOTPError
		if !errors.As(err, &typed) {
			continue
		}
		switch typed.Reason {
		case TOTPReasonHOTP:
			sawHOTP = true
		case TOTPReasonMigration:
			sawMigration = true
		}
	}
	switch {
	case sawHOTP:
		return TOTP{}, totpError(TOTPReasonHOTP)
	case sawMigration:
		return TOTP{}, totpError(TOTPReasonMigration)
	default:
		return TOTP{}, totpError(TOTPReasonNotFound)
	}
}

// GenerateTOTP implements RFC 6238 with the RFC 4226 dynamic truncation. It is
// the primitive ParseTOTP's Code method uses; callers normally parse first.
func GenerateTOTP(secret []byte, algorithm TOTPAlgorithm, digits, period int, at time.Time) (string, error) {
	newHash, ok := totpHash(algorithm)
	if !ok {
		return "", totpError(TOTPReasonInvalidAlgorithm)
	}
	if digits < DefaultTOTPDigits || digits > maxTOTPDigits {
		return "", totpError(TOTPReasonInvalidDigits)
	}
	if period < 1 || period > maxTOTPPeriod {
		return "", totpError(TOTPReasonInvalidPeriod)
	}
	if len(secret) == 0 {
		return "", totpError(TOTPReasonInvalidSecret)
	}
	counter := uint64(at.Unix() / int64(period))
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(newHash, secret)
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	truncated := uint32(sum[offset]&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, truncated%modulus), nil
}

// NextTOTPCode returns the code for the step that starts after the current one
// when less than two seconds of the current step remain. A code generated a
// moment before it expires would otherwise be rejected by the server.
func NextTOTPCode(parsed TOTP, at time.Time) (string, error) {
	period := time.Duration(parsed.period) * time.Second
	if period <= 0 {
		return "", totpError(TOTPReasonInvalidPeriod)
	}
	stepSeconds := int64(parsed.period)
	intoSeconds := at.Unix() % stepSeconds
	if intoSeconds < 0 {
		intoSeconds += stepSeconds
	}
	remaining := period - time.Duration(intoSeconds)*time.Second
	if remaining <= 2*time.Second {
		return parsed.Code(at.Add(remaining))
	}
	return parsed.Code(at)
}

func parseTOTPURI(text string) (TOTP, error) {
	parsedURL, err := url.Parse(text)
	if err != nil || !strings.EqualFold(parsedURL.Scheme, "otpauth") {
		return TOTP{}, totpError(TOTPReasonInvalidURI)
	}
	typ := strings.ToLower(parsedURL.Hostname())
	if typ == "" {
		return TOTP{}, totpError(TOTPReasonInvalidURI)
	}
	switch typ {
	case "totp":
	case "hotp":
		return TOTP{}, totpError(TOTPReasonHOTP)
	default:
		return TOTP{}, totpError(TOTPReasonUnsupportedType)
	}

	query := parsedURL.Query()
	secretText := query.Get("secret")
	if strings.TrimSpace(secretText) == "" {
		return TOTP{}, totpError(TOTPReasonMissingSecret)
	}
	secret, err := decodeTOTPBase32(secretText)
	if err != nil || len(secret) < minTOTPSecretBytes || len(secret) > maxTOTPSecretBytes {
		return TOTP{}, totpError(TOTPReasonInvalidSecret)
	}

	algorithm := TOTPAlgorithm(strings.ToUpper(strings.ReplaceAll(query.Get("algorithm"), "-", "")))
	if algorithm == "" {
		algorithm = TOTPAlgorithmSHA1
	}
	if _, ok := totpHash(algorithm); !ok {
		return TOTP{}, totpError(TOTPReasonInvalidAlgorithm)
	}

	digits, err := totpNumber(query.Get("digits"), DefaultTOTPDigits,
		DefaultTOTPDigits, maxTOTPDigits, TOTPReasonInvalidDigits)
	if err != nil {
		return TOTP{}, err
	}
	period, err := totpNumber(query.Get("period"), DefaultTOTPPeriod,
		1, maxTOTPPeriod, TOTPReasonInvalidPeriod)
	if err != nil {
		return TOTP{}, err
	}

	rawLabel, err := url.PathUnescape(strings.TrimPrefix(parsedURL.EscapedPath(), "/"))
	if err != nil {
		return TOTP{}, totpError(TOTPReasonInvalidURI)
	}
	rawLabel = strings.TrimSpace(rawLabel)
	issuer := strings.TrimSpace(query.Get("issuer"))
	label := rawLabel
	if issuer == "" && strings.Contains(rawLabel, ":") {
		before, after, _ := strings.Cut(rawLabel, ":")
		issuer = strings.TrimSpace(before)
		label = strings.TrimSpace(after)
	} else if issuer != "" && strings.HasPrefix(strings.ToLower(rawLabel), strings.ToLower(issuer)+":") {
		label = strings.TrimSpace(rawLabel[len(issuer)+1:])
	}

	return TOTP{
		secret:    secret,
		algorithm: algorithm,
		digits:    digits,
		period:    period,
		issuer:    issuer,
		label:     label,
	}, nil
}

func parseRawTOTPSecret(text string) (TOTP, error) {
	secret, err := decodeTOTPBase32(text)
	if err != nil || len(secret) < minTOTPSecretBytes || len(secret) > maxTOTPSecretBytes {
		return TOTP{}, totpError(TOTPReasonInvalidSecret)
	}
	return TOTP{
		secret:    secret,
		algorithm: TOTPAlgorithmSHA1,
		digits:    DefaultTOTPDigits,
		period:    DefaultTOTPPeriod,
	}, nil
}

// decodeTOTPBase32 follows the authenticator convention: Base32, uppercase,
// spaces ignored, padding optional.
func decodeTOTPBase32(text string) ([]byte, error) {
	cleaned := strings.ToUpper(text)
	cleaned = strings.Join(strings.Fields(cleaned), "")
	cleaned = strings.TrimRight(cleaned, "=")
	if cleaned == "" {
		return nil, totpError(TOTPReasonInvalidSecret)
	}
	for _, char := range cleaned {
		if (char < 'A' || char > 'Z') && (char < '2' || char > '7') {
			return nil, totpError(TOTPReasonInvalidSecret)
		}
	}
	return totpBase32.DecodeString(cleaned)
}

func totpNumber(text string, fallback, minimum, maximum int, reason TOTPErrorReason) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < minimum || value > maximum {
		return 0, totpError(reason)
	}
	return value, nil
}

func totpHash(algorithm TOTPAlgorithm) (func() hash.Hash, bool) {
	switch algorithm {
	case TOTPAlgorithmSHA1:
		return sha1.New, true
	case TOTPAlgorithmSHA256:
		return sha256.New, true
	case TOTPAlgorithmSHA512:
		return sha512.New, true
	default:
		return nil, false
	}
}
