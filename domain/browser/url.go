// Package browser owns the host-routed browser's value rules. It has no
// transport or page dependency: the application layer composes these rules
// with a dedicated tunnel and an isolated page surface.
package browser

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Address errors. The application maps them to the panel's localized message;
// they never carry user-visible copy.
var (
	// ErrAddressEmpty reports an address with nothing but whitespace.
	ErrAddressEmpty = errors.New("browser: empty address")
	// ErrAddressTooLong reports an address over the original 2048-character
	// limit.
	ErrAddressTooLong = errors.New("browser: address exceeds 2048 characters")
	// ErrAddressScheme reports a scheme other than HTTP or HTTPS.
	ErrAddressScheme = errors.New("browser: address scheme is not http or https")
	// ErrAddressCredentials reports a URL that embeds a username or password.
	ErrAddressCredentials = errors.New("browser: address contains credentials")
	// ErrAddressInvalid reports an address that does not parse as a web URL.
	ErrAddressInvalid = errors.New("browser: address is not a web URL")
)

// MaxAddressLength is the original limit accepted by the address bar.
const MaxAddressLength = 2048

var (
	// localAddress matches localhost, an IPv4 127/8 address, and a bracketed
	// IPv6 address, optionally with a port and a path. These default to HTTP,
	// as they usually serve a console on the SSH Host itself.
	localAddress = regexp.MustCompile(`(?i)^(?:localhost|127\.\d+\.\d+\.\d+|\[[0-9a-f:]+\])(?::\d+)?(?:[/?#]|$)`)
	// explicitScheme matches a leading scheme. The second pattern keeps a
	// host:port entry, such as example.com:8080, from being read as a scheme.
	explicitScheme = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	hostPort       = regexp.MustCompile(`^[^/:]+:\d+(?:[/?#]|$)`)
)

// NormalizeURL turns address-bar input into the absolute HTTP(S) URL the
// panel loads. It mirrors the original host browser: localhost, 127/8, and
// bracketed IPv6 default to HTTP; every other host defaults to HTTPS; explicit
// HTTP(S) schemes are preserved. Credentials are refused because the panel
// never sends them through the Host tunnel.
func NormalizeURL(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", ErrAddressEmpty
	}
	if len(raw) > MaxAddressLength {
		return "", ErrAddressTooLong
	}
	scheme := ""
	if explicitScheme.MatchString(raw) && !hostPort.MatchString(raw) {
		scheme = strings.ToLower(raw[:strings.IndexByte(raw, ':')])
	}
	if scheme == "" {
		if localAddress.MatchString(raw) {
			scheme = "http"
		} else {
			scheme = "https"
		}
		raw = scheme + "://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrAddressInvalid
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrAddressScheme
	}
	if parsed.Hostname() == "" {
		return "", ErrAddressInvalid
	}
	if parsed.User != nil {
		return "", ErrAddressCredentials
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

// Origin returns the scheme and authority of an absolute web URL, such as
// "https://example.com:8443". It is the unit the gateway isolates and the
// unit a page's cookies belong to.
func Origin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrAddressInvalid
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrAddressScheme
	}
	if parsed.Host == "" {
		return "", ErrAddressInvalid
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
