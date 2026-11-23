// Package vnc holds the VNC Session vocabulary shared by the Session start
// spec, the Vault's persisted viewer configuration, and the RFB adapter.
//
// The package is pure data: it imports only the shared host identity type and
// the standard library, so the start section, the persisted record, and the
// adapter can all agree on defaults and validation without depending on the
// UI or on a socket.
package vnc

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
)

// VNC endpoint range. A display number is editor presentation only: a Host
// always stores the real TCP port, so display 0 maps to 5900 and display 99
// maps to 5999.
const (
	// DisplayBasePort is the TCP port of VNC display 0.
	DisplayBasePort = 5900
	// DisplayMax is the highest display number the editor accepts.
	DisplayMax = 99
)

// DefaultPort is the RFB service port, the port of display 0.
const DefaultPort = DisplayBasePort

// WebSocket transport defaults. A noVNC page is served by websockify, which
// upgrades the HTTP request at /websockify and forwards the RFB byte stream;
// its HTTP port is 6080. The path and port are editor hints, not a second
// endpoint: the Host always stores the WebSocket port in Port.
const (
	// DefaultWebSocketPort is the port websockify serves by default.
	DefaultWebSocketPort = 6080
	// DefaultWebSocketPath is the upgrade path websockify serves by default.
	DefaultWebSocketPath = "/websockify"
	// maxWebSocketPathLength bounds a pasted path.
	maxWebSocketPathLength = 1024
)

// NormalizeWebSocketPath canonicalizes the HTTP path of a WebSocket endpoint:
// empty becomes the websockify default, and a path without a leading slash
// gains one. A query string (such as a websockify token) is preserved.
func NormalizeWebSocketPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return DefaultWebSocketPath
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// ValidWebSocketPath reports whether a normalized path can be requested. It
// requires an absolute request target, rejects control characters, whitespace,
// and fragments, and rejects anything the HTTP client could not parse, such as
// a malformed percent-escape.
func ValidWebSocketPath(path string) bool {
	if !strings.HasPrefix(path, "/") || len(path) > maxWebSocketPathLength {
		return false
	}
	for _, char := range path {
		if char <= ' ' || char == 0x7f || char == '#' {
			return false
		}
	}
	if _, err := url.Parse(path); err != nil {
		return false
	}
	return true
}

// DisplayMode selects how the VNC Host editor accepts the endpoint. It is
// presentation state: the Host record always stores the resolved TCP port.
type DisplayMode string

const (
	// DisplayNumber enters the endpoint as a display number ("1" or ":1").
	DisplayNumber DisplayMode = "display"
	// Port enters the endpoint as a raw TCP port.
	Port DisplayMode = "port"
)

// NormalizeDisplayMode maps an empty or unknown value to display-number input,
// the default the editor presents.
func NormalizeDisplayMode(mode DisplayMode) DisplayMode {
	switch mode {
	case Port:
		return Port
	default:
		return DisplayNumber
	}
}

// FitMode selects how the viewer scales the remote desktop.
type FitMode string

const (
	// Fit scales the desktop to fit the pane, preserving the aspect ratio.
	Fit FitMode = "fit"
	// OneToOne shows one remote pixel per pane pixel.
	OneToOne FitMode = "one_to_one"
	// Clip keeps one-to-one pixels and pans a clipped viewport.
	Clip FitMode = "clip"
)

// NormalizeFitMode maps an empty or unknown value to fit-to-window, the
// default the viewer starts with.
func NormalizeFitMode(mode FitMode) FitMode {
	switch mode {
	case OneToOne, Clip:
		return mode
	default:
		return Fit
	}
}

// CursorMode selects whether the local pointer or the server-drawn cursor is
// visible over the framebuffer.
type CursorMode string

const (
	// CursorLocal draws the local pointer from the server's cursor shape.
	CursorLocal CursorMode = "local"
	// CursorServer lets the server paint the cursor into the framebuffer.
	CursorServer CursorMode = "server"
)

// NormalizeCursorMode maps an empty or unknown value to the local cursor, the
// default every other remote-desktop client uses.
func NormalizeCursorMode(mode CursorMode) CursorMode {
	switch mode {
	case CursorServer:
		return CursorServer
	default:
		return CursorLocal
	}
}

// Config is a VNC Host's persisted viewer configuration. The endpoint is not
// stored here: the Host's Port is always the real TCP port, and the
// display-number input mode only decides how the editor presents it.
type Config struct {
	// DisplayMode selects display-number or raw-port input in the editor.
	DisplayMode DisplayMode `json:"displayMode"`
	// FitMode is the viewer's initial scaling behavior.
	FitMode FitMode `json:"fitMode"`
	// ViewOnly suppresses local keyboard and pointer input.
	ViewOnly bool `json:"viewOnly,omitempty"`
	// Shared asks the server to keep other clients connected (ClientInit).
	// It defaults to true; an explicit false is persisted.
	Shared bool `json:"shared"`
	// CursorMode selects the local or server-drawn pointer.
	CursorMode CursorMode `json:"cursorMode"`
	// PlaintextWarningAcknowledged records that the user accepted the
	// unencrypted-transport warning for this Host.
	PlaintextWarningAcknowledged bool `json:"plaintextWarningAcknowledged,omitempty"`
	// WebSocket carries the RFB byte stream inside a WebSocket instead of a
	// raw TCP connection, the transport a noVNC/websockify page uses. The
	// Host's Port is then the WebSocket (HTTP) port.
	WebSocket bool `json:"webSocket,omitempty"`
	// WebSocketPath is the HTTP upgrade path. Empty means
	// DefaultWebSocketPath; it is cleared when WebSocket is off.
	WebSocketPath string `json:"webSocketPath,omitempty"`
	// WebSocketSecure selects wss (TLS) over ws. It is cleared when WebSocket
	// is off.
	WebSocketSecure bool `json:"webSocketSecure,omitempty"`
}

// DefaultConfig is the configuration a new VNC Host starts from and the
// baseline NormalizeConfig fills missing enum values from.
func DefaultConfig() Config {
	return Config{
		DisplayMode: DisplayNumber,
		FitMode:     Fit,
		Shared:      true,
		CursorMode:  CursorLocal,
	}
}

// NormalizeConfig canonicalizes a VNC configuration. A nil value means a new
// or older record, so it starts from the defaults; unknown enum values fall
// back to their defaults. A non-nil Shared=false is an explicit choice and is
// preserved.
func NormalizeConfig(config *Config) *Config {
	normalized := DefaultConfig()
	if config != nil {
		normalized = *config
	}
	normalized.DisplayMode = NormalizeDisplayMode(normalized.DisplayMode)
	normalized.FitMode = NormalizeFitMode(normalized.FitMode)
	normalized.CursorMode = NormalizeCursorMode(normalized.CursorMode)
	if normalized.WebSocket {
		normalized.WebSocketPath = NormalizeWebSocketPath(normalized.WebSocketPath)
	} else {
		// A raw TCP VNC Host keeps no WebSocket settings, so a hidden toggle
		// cannot leave a path or TLS choice behind.
		normalized.WebSocketPath = ""
		normalized.WebSocketSecure = false
	}
	return &normalized
}

// CloneConfig returns a copy safe to store on a Host or draft. It keeps a nil
// value nil so a non-VNC Host serializes exactly as before.
func CloneConfig(config *Config) *Config {
	if config == nil {
		return nil
	}
	copy := *config
	return &copy
}

// PortForDisplay returns the TCP port for a VNC display number. The boolean is
// false for a display outside 0-99.
func PortForDisplay(display int) (int, bool) {
	if display < 0 || display > DisplayMax {
		return 0, false
	}
	return DisplayBasePort + display, true
}

// DisplayForPort returns the display number for a TCP port. The boolean is
// false when the port is outside the 5900-5999 display range.
func DisplayForPort(port int) (int, bool) {
	if port < DisplayBasePort || port > DisplayBasePort+DisplayMax {
		return 0, false
	}
	return port - DisplayBasePort, true
}

// Route identifies the non-secret route selection a VNC Session inherits from
// its Host. It carries references, not resolved endpoints: the app's
// connection manager resolves the jump chain, proxy, and Network Profile
// before the adapter dials. A manual proxy and its credentials therefore stay
// with the Host record and never enter the start section.
type Route struct {
	// Jumps is the ordered jump Host chain, closest to the client first.
	Jumps []model.HostID `json:"jumps,omitempty"`
	// ProxyProfileID selects a saved Proxy Profile for the first endpoint.
	ProxyProfileID string `json:"proxyProfileId,omitempty"`
	// NetworkProfileID selects a saved Network Profile for the first endpoint.
	NetworkProfileID string `json:"networkProfileId,omitempty"`
	// DirectProxy forces a direct connection and stops inherited proxy
	// selection.
	DirectProxy bool `json:"directProxy,omitempty"`
}

// cloneRoute returns a route whose jump slice the caller may keep.
func cloneRoute(route Route) Route {
	if len(route.Jumps) > 0 {
		route.Jumps = append([]model.HostID(nil), route.Jumps...)
	}
	return route
}

// SessionConfig is the kind-selected start-spec section for a VNC Session. It
// carries the endpoint, the credentials, the viewer configuration, and the
// route selection; it is required when a Session's Kind is VNC and ignored by
// every other Kind.
type SessionConfig struct {
	// Hostname is the peer's host name or address.
	Hostname string `json:"hostname"`
	// Port is the RFB service port. Zero uses DefaultPort.
	Port int `json:"port,omitempty"`
	// Username is the VNC account name used by VeNCrypt Plain subtypes. An
	// ordinary VNC password type ignores it.
	Username string `json:"username,omitempty"`
	// Password is credential plaintext in memory only. It is never persisted
	// in the Workspace.
	Password string `json:"-"`
	// Configuration is the persisted viewer configuration, normalized before
	// the Session opens.
	Configuration Config `json:"configuration"`
	// Route selects how the stream reaches the endpoint.
	Route Route `json:"route,omitempty"`
}

// Normalized trims text fields, fills the default port, and canonicalizes the
// viewer configuration.
func (c SessionConfig) Normalized() SessionConfig {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.Username = strings.TrimSpace(c.Username)
	if c.Configuration == (Config{}) {
		// The start section is a value, so its zero form means "unset"; a
		// saved or edited configuration always names at least one enum.
		c.Configuration = DefaultConfig()
	} else {
		c.Configuration = *NormalizeConfig(&c.Configuration)
	}
	if c.Port == 0 {
		// A WebSocket endpoint is an HTTP service (websockify's 6080), not an
		// RFB display port.
		if c.Configuration.WebSocket {
			c.Port = DefaultWebSocketPort
		} else {
			c.Port = DefaultPort
		}
	}
	c.Route = cloneRoute(c.Route)
	return c
}

// ErrInvalidConfig reports a VNC start section that cannot be dialed.
var ErrInvalidConfig = errors.New("vnc: invalid configuration")

// ValidationError names the field and reason a configuration was rejected.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("vnc: %s %s", e.Field, e.Reason)
}

// Is makes every validation failure match ErrInvalidConfig.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Validate checks a normalized section.
func (c SessionConfig) Validate() error {
	if strings.TrimSpace(c.Hostname) == "" {
		return &ValidationError{Field: "hostname", Reason: "is required"}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Field: "port", Reason: "is out of range"}
	}
	if c.Configuration.WebSocket && !ValidWebSocketPath(c.Configuration.WebSocketPath) {
		return &ValidationError{Field: "webSocketPath", Reason: "is not a valid request path"}
	}
	return nil
}
