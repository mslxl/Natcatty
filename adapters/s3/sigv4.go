// Package s3 is the S3-compatible cloud-sync adapter. It stores the encrypted
// Vault as one object addressed path-style as <endpoint>/<bucket>/<key>, where
// the key is the user's prefix plus netcatty-vault.json, and signs every
// request with AWS Signature Version 4 using an access key, a secret access
// key, and an optional session token.
//
// The signer is implemented in-house with the standard library, matching the
// project's dependency policy. It follows Amazon's S3 variant of SigV4: the
// canonical URI is encoded once and never normalized, so characters such as
// "$", "+", and repeated slashes in an object key survive. Requests never fall
// back to virtual-host addressing, TLS certificate verification is never
// disabled, and a 301 PermanentRedirect is reported as a wrong region rather
// than followed.
package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Algorithm is the SigV4 algorithm identifier used in Authorization headers.
const Algorithm = "AWS4-HMAC-SHA256"

// Service is the SigV4 service name for S3.
const Service = "s3"

// Credentials is the SigV4 material. SessionToken is optional; when it is set
// it is signed and sent as x-amz-security-token.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// SignInput describes one request to sign. Headers carries the additional
// headers to sign; Sign adds host, x-amz-content-sha256, x-amz-date, and
// x-amz-security-token itself. URI is the decoded path (S3 encodes it once),
// and Query is a raw query string without the leading "?".
type SignInput struct {
	Method      string
	Host        string
	URI         string
	Query       string
	Headers     http.Header
	PayloadHash string
	Region      string
	Service     string
	Time        time.Time
}

// Signed is the result of signing one request. CanonicalRequest and
// StringToSign are exposed so tests and the self-test can show what was
// signed; they contain no secret.
type Signed struct {
	Authorization    string
	SignedHeaders    string
	CanonicalRequest string
	StringToSign     string
	Signature        string
}

// Sign computes the SigV4 Authorization header for input.
func Sign(input SignInput, credentials Credentials) (Signed, error) {
	if strings.TrimSpace(credentials.AccessKeyID) == "" || credentials.SecretAccessKey == "" {
		return Signed{}, errors.New("s3: signing requires an access key id and a secret access key")
	}
	if input.Host == "" {
		return Signed{}, errors.New("s3: signing requires a host")
	}
	if input.Method == "" {
		return Signed{}, errors.New("s3: signing requires a method")
	}
	service := input.Service
	if service == "" {
		service = Service
	}
	if input.Region == "" {
		return Signed{}, errors.New("s3: signing requires a region")
	}
	if input.PayloadHash == "" {
		return Signed{}, errors.New("s3: signing requires the payload hash")
	}
	at := input.Time
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	amzDate := at.Format("20060102T150405Z")
	dateStamp := at.Format("20060102")

	headers := cloneHeader(input.Headers)
	headers.Set("host", input.Host)
	headers.Set("x-amz-content-sha256", input.PayloadHash)
	headers.Set("x-amz-date", amzDate)
	if token := strings.TrimSpace(credentials.SessionToken); token != "" {
		headers.Set("x-amz-security-token", token)
	} else {
		headers.Del("x-amz-security-token")
	}

	names := signedHeaderNames(headers)
	canonicalHeaders := canonicalHeadersBlock(headers, names)
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{
		input.Method,
		uriEncode(input.URI, false),
		canonicalQuery(input.Query),
		canonicalHeaders,
		signedHeaders,
		input.PayloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, input.Region, service, "aws4_request"}, "/")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		Algorithm,
		amzDate,
		scope,
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(signingKey(credentials.SecretAccessKey, dateStamp, input.Region, service), []byte(stringToSign)))
	authorization := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		Algorithm, credentials.AccessKeyID, scope, signedHeaders, signature)
	return Signed{
		Authorization:    authorization,
		SignedHeaders:    signedHeaders,
		CanonicalRequest: canonicalRequest,
		StringToSign:     stringToSign,
		Signature:        signature,
	}, nil
}

// PayloadSHA256 returns the lowercase hex SHA-256 of body. S3 requests always
// carry it in x-amz-content-sha256.
func PayloadSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// signingKey derives the SigV4 key ladder: date, region, service, terminator.
func signingKey(secret, dateStamp, region, service string) []byte {
	date := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	regional := hmacSHA256(date, []byte(region))
	serviceKey := hmacSHA256(regional, []byte(service))
	return hmacSHA256(serviceKey, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// cloneHeader copies a header so Sign never mutates the caller's map.
func cloneHeader(source http.Header) http.Header {
	clone := http.Header{}
	for name, values := range source {
		for _, value := range values {
			clone.Add(name, value)
		}
	}
	return clone
}

// signedHeaderNames returns the lowercase header names in canonical order.
// Every header supplied is signed: S3 accepts any signed header set, but
// rejects a request that carries an x-amz-* header it did not sign.
func signedHeaderNames(headers http.Header) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(headers))
	for name := range headers {
		lower := strings.ToLower(name)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		names = append(names, lower)
	}
	sort.Strings(names)
	return names
}

// canonicalHeadersBlock renders "name:value\n" lines in the sorted order of
// names. Values are trimmed and internal whitespace is collapsed, as SigV4
// requires. Multiple values for one name are joined with commas.
func canonicalHeadersBlock(headers http.Header, names []string) string {
	var builder strings.Builder
	for _, name := range names {
		values := headerValues(headers, name)
		for index := range values {
			values[index] = collapseSpaces(values[index])
		}
		builder.WriteString(name)
		builder.WriteString(":")
		builder.WriteString(strings.Join(values, ","))
		builder.WriteString("\n")
	}
	return builder.String()
}

func headerValues(headers http.Header, lowerName string) []string {
	var values []string
	for name, headerValues := range headers {
		if strings.ToLower(name) != lowerName {
			continue
		}
		values = append(values, headerValues...)
	}
	return values
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// canonicalQuery encodes and sorts query parameters. The empty query renders
// as the empty string; "lifecycle" renders as "lifecycle=".
func canonicalQuery(raw string) string {
	raw = strings.TrimPrefix(raw, "?")
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		// A malformed query still has to be signed consistently: fall back to
		// splitting the raw pairs without decoding.
		values = url.Values{}
		for _, pair := range strings.Split(raw, "&") {
			name, value, _ := strings.Cut(pair, "=")
			values.Add(name, value)
		}
	}
	type pair struct{ name, value string }
	pairs := make([]pair, 0, len(values))
	for name, entries := range values {
		encodedName := uriEncode(name, true)
		for _, entry := range entries {
			pairs = append(pairs, pair{name: encodedName, value: uriEncode(entry, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	parts := make([]string, len(pairs))
	for index, entry := range pairs {
		parts[index] = entry.name + "=" + entry.value
	}
	return strings.Join(parts, "&")
}

// uriEncode percent-encodes every byte except A-Z, a-z, 0-9, '-', '_', '.',
// '~', and (unless encodeSlash) '/'. This is SigV4's unreserved set; it is
// stricter than url.PathEscape, which leaves '$', '+', '&', and ':' alone.
// Spaces become %20, never '+'.
func uriEncode(value string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		b := value[index]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			builder.WriteByte(b)
		case b == '/' && !encodeSlash:
			builder.WriteByte(b)
		default:
			builder.WriteByte('%')
			builder.WriteByte(hexDigits[b>>4])
			builder.WriteByte(hexDigits[b&0x0f])
		}
	}
	return builder.String()
}
