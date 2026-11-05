package connection

import (
	"errors"
	"fmt"
)

// Kind classifies a connection failure so the UI can choose a localized
// message. The values mirror the original's observable failure distinctions.
type Kind string

const (
	// KindAuth reports refused credentials or unavailable saved credentials.
	KindAuth Kind = "auth"
	// KindNetwork reports an unreachable or refused endpoint.
	KindNetwork Kind = "network"
	// KindTimeout reports the TCP dial or the authentication-ready deadline.
	KindTimeout Kind = "timeout"
	// KindHostKey reports a host key the user rejected or could not be
	// verified. It is raised before authentication begins.
	KindHostKey Kind = "host_key"
	// KindUnsupported reports an algorithm this transport cannot negotiate.
	KindUnsupported Kind = "unsupported"
	// KindCancelled reports an operation the caller cancelled.
	KindCancelled Kind = "cancelled"
	// KindInternal reports a local failure such as a storage error.
	KindInternal Kind = "internal"
	// KindX11 reports that the SSH Host refused X11 forwarding.
	KindX11 Kind = "x11"
	// KindProvider reports that the Network Provider for a selected Network
	// Profile could not supply a connection: it is not logged in, failed to
	// start, has an invalid configuration, or its tunnel is unavailable. An
	// unreachable target remains KindNetwork and an expired deadline remains
	// KindTimeout, even when a Provider carried the dial.
	KindProvider Kind = "provider"
)

// ProviderPhase is a Network Provider's lifecycle state at the moment a failure
// was observed. It is stable data, never display text.
type ProviderPhase string

const (
	// ProviderPhaseStopped reports a Provider that never started or was shut
	// down. Selecting a profile with no factory or registry also lands here.
	ProviderPhaseStopped ProviderPhase = "stopped"
	// ProviderPhaseStarting reports a Provider that is joining its network.
	ProviderPhaseStarting ProviderPhase = "starting"
	// ProviderPhaseNeedsLogin reports an interactive login that has not
	// completed. A Tailscale Net provider reports it with its Auth URL.
	ProviderPhaseNeedsLogin ProviderPhase = "needs_login"
	// ProviderPhaseRunning reports a Provider that was up when the operation
	// failed, such as a target the tunnel could not reach.
	ProviderPhaseRunning ProviderPhase = "running"
	// ProviderPhaseStopping reports a Provider shutting down.
	ProviderPhaseStopping ProviderPhase = "stopping"
	// ProviderPhaseError reports a Provider that failed to start or lost its
	// tunnel.
	ProviderPhaseError ProviderPhase = "error"
)

// ProviderInfo identifies the Network Profile behind a failure without
// carrying key material. Label is optional display metadata; ProfileID, Kind,
// and Phase are the stable diagnostics.
type ProviderInfo struct {
	ProfileID string
	Label     string
	Kind      string
	Phase     ProviderPhase
}

// Error is a typed connection error. Cause carries the transport or storage
// detail; it is never user-visible text. Hop identifies the chain endpoint
// that failed when the failure happened during a jump chain. Provider
// identifies the Network Profile behind a failure; it never carries key
// material. Error() is a developer diagnostic; the UI localizes Kind, Hop.Label,
// and Provider.Phase instead.
type Error struct {
	Kind     Kind
	Cause    error
	Hop      *HopInfo
	Provider *ProviderInfo
}

func (e *Error) Error() string {
	message := fmt.Sprintf("connection: %s", e.Kind)
	if e.Cause != nil {
		message += fmt.Sprintf(": %v", e.Cause)
	}
	if e.Provider != nil {
		message += fmt.Sprintf(" (network profile %q", e.Provider.ProfileID)
		if e.Provider.Kind != "" {
			message += ", kind " + e.Provider.Kind
		}
		if e.Provider.Phase != "" {
			message += ", phase " + string(e.Provider.Phase)
		}
		message += ")"
	}
	return message + hopErrorText(e.Hop)
}

func (e *Error) Unwrap() error { return e.Cause }

// Sentinels the application and the SSH adapter use to classify failures.
var (
	// ErrHostKeyRejected reports that the user rejected the server's key, or
	// that no trust decision could be made.
	ErrHostKeyRejected = errors.New("connection: host key rejected")
	// ErrAuthentication reports refused credentials.
	ErrAuthentication = errors.New("connection: authentication failed")
	// ErrDialTimeout reports that the TCP connection did not establish in
	// time.
	ErrDialTimeout = errors.New("connection: dial timed out")
	// ErrAuthTimeout reports that authentication did not become ready in
	// time.
	ErrAuthTimeout = errors.New("connection: authentication timed out")
	// ErrNetwork reports an unreachable, refused, or reset endpoint.
	ErrNetwork = errors.New("connection: network failure")
	// ErrUnsupportedAlgorithm reports a negotiation this transport cannot
	// perform.
	ErrUnsupportedAlgorithm = errors.New("connection: unsupported algorithm")
	// ErrNoCredentials reports that the Host has no saved password.
	ErrNoCredentials = errors.New("connection: no saved credential")
	// ErrNoDialer reports a Manager constructed without an SSH dialer.
	ErrNoDialer = errors.New("connection: no dialer")
	// ErrClosed reports an operation on a released lease or closed
	// Connection.
	ErrClosed = errors.New("connection: closed")
	// ErrStaleTransport reports a parked transport whose new shell died
	// before the liveness window elapsed. The transport was discarded, so a
	// retry opens a fresh Connection.
	ErrStaleTransport = errors.New("connection: reused transport closed before use")
	// ErrPromptCancelled reports that the user dismissed a passphrase or
	// keyboard-interactive prompt.
	ErrPromptCancelled = errors.New("connection: authentication prompt cancelled")
	// ErrPromptUnavailable reports that the server requires interactive input
	// but no prompt implementation is wired.
	ErrPromptUnavailable = errors.New("connection: authentication prompt unavailable")
	// ErrKeyUnavailable reports a selected SSH Key that no longer exists or has
	// no stored private material. Explicit key and certificate modes fail
	// closed instead of substituting another credential.
	ErrKeyUnavailable = errors.New("connection: selected SSH Key unavailable")
	// ErrUnsupportedProxy reports a proxy transport this adapter does not
	// implement.
	ErrUnsupportedProxy = errors.New("connection: unsupported proxy type")
	// ErrInvalidProxy reports a proxy configuration the transport cannot use.
	ErrInvalidProxy = errors.New("connection: invalid proxy configuration")
	// ErrProxyHandshake reports a proxy that accepted the connection but
	// refused or malformed its tunnel handshake.
	ErrProxyHandshake = errors.New("connection: proxy handshake failed")
	// ErrX11RequestDenied reports that the SSH Host rejected x11-req.
	ErrX11RequestDenied = errors.New("connection: X11 forwarding was refused")
	// ErrX11Unavailable reports a local X11 bridge that could not start, such
	// as an unavailable random source.
	ErrX11Unavailable = errors.New("connection: X11 forwarding unavailable")
	// ErrNetworkProvider reports that a selected Network Profile has no
	// runtime registry or factory to provide it. The more specific sentinels
	// below cover a closed registry, a missing profile, and an unsupported
	// Kind. Every provider-level failure is KindProvider, and no Network
	// Route may ever fall back to a direct dial.
	ErrNetworkProvider = errors.New("connection: network provider unavailable")
	// ErrNetworkProviderClosed reports a Provider registry that has shut down.
	ErrNetworkProviderClosed = errors.New("connection: network provider is closed")
	// ErrNetworkProviderProfile reports a Network Route without a profile
	// identity, such as a dangling reference.
	ErrNetworkProviderProfile = errors.New("connection: network profile is missing")
	// ErrNetworkProviderKind reports a Network Profile Kind this build cannot
	// run.
	ErrNetworkProviderKind = errors.New("connection: unsupported network profile kind")
	// ErrProviderUnavailable reports a DialRequest that selected a Network
	// Route but carried no opened Provider. The Manager always supplies one;
	// this sentinel guards the adapter contract that a Network Route is never
	// substituted with a direct dial.
	ErrProviderUnavailable = errors.New("connection: network provider was not supplied")
	// ErrProxyCommandNetworkConflict reports that a ProxyCommand and a
	// Network Profile were selected for the same first endpoint. ProxyCommand
	// spawns a local process on the operating system's network, which an
	// in-process Provider cannot carry.
	ErrProxyCommandNetworkConflict = errors.New("connection: ProxyCommand cannot be combined with a Network Profile")
)

// ProxyCommandNetworkConflict returns the typed failure for a route that
// selects both a ProxyCommand and a Network Profile. It names both in its
// diagnostic so neither selection is silently ignored, and carries the
// profile identity for the UI. The caller must raise it before starting the
// ProxyCommand process.
func ProxyCommandNetworkConflict(route NetworkRoute) error {
	return &Error{
		Kind: KindUnsupported,
		Cause: fmt.Errorf("%w: ProxyCommand and Network Profile %q",
			ErrProxyCommandNetworkConflict, route.ProfileID),
		Provider: &ProviderInfo{
			ProfileID: route.ProfileID,
			Label:     route.Label,
			Kind:      route.Kind,
		},
	}
}

// ProviderUnavailable returns the typed failure for a Network Route that
// reached a transport without its opened Provider. It fails closed instead of
// dialing directly, because a route whose Provider is missing would otherwise
// silently bypass the selected Network Profile. Phase is stopped: no Provider
// was ever supplied for the route.
func ProviderUnavailable(route NetworkRoute) error {
	return providerRouteError(route, ProviderPhaseStopped,
		fmt.Errorf("%w: Network Profile %q", ErrProviderUnavailable, route.ProfileID))
}

// TagProvider attaches ProviderInfo to an error without changing a Kind the
// transport already chose. A failure that is not yet classified becomes
// KindProvider, because a plain error from the Provider layer is a provider
// failure, never a reason to dial directly.
func TagProvider(err error, info ProviderInfo) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		clone := *typed
		merged := info
		if typed.Provider != nil {
			mergeProviderInfo(&merged, *typed.Provider)
		}
		clone.Provider = &merged
		return &clone
	}
	return &Error{Kind: KindProvider, Cause: err, Provider: &info}
}

// X11NoticeCode identifies a non-fatal X11 runtime problem. It is an
// identifier, never localized text; the UI resolves it through its catalogs.
type X11NoticeCode string

const (
	// X11NoticeLocalDisplay reports that the local X server could not be
	// reached for a forwarded X11 channel.
	X11NoticeLocalDisplay X11NoticeCode = "local_display"
	// X11NoticeCookie reports that xauth produced no MIT-MAGIC-COOKIE-1 for
	// the local display, so the setup packet could not be rewritten.
	X11NoticeCookie X11NoticeCode = "cookie"
	// X11NoticeChannel reports that an X11 channel could not be accepted.
	X11NoticeChannel X11NoticeCode = "channel"
)

// X11Notice reports a non-fatal X11 forwarding failure. Display is the
// resolved local display target for diagnostics.
type X11Notice struct {
	Code    X11NoticeCode
	Display string
}

// AuthFailure wraps cause as an authentication error. AuthSource
// implementations use it so the Manager reports a missing or unusable
// credential as a typed auth failure rather than an internal error.
func AuthFailure(cause error) error { return authError(cause) }

// Permanent reports whether reconnecting cannot fix this failure. A terminal
// Session must not retry a rejected credential or host key; the user has to
// change the Host or trust decision first.
func (e *Error) Permanent() bool {
	return e != nil && (e.Kind == KindAuth || e.Kind == KindHostKey || e.Kind == KindX11)
}

func hostKeyError(cause error) error     { return &Error{Kind: KindHostKey, Cause: cause} }
func authError(cause error) error        { return &Error{Kind: KindAuth, Cause: cause} }
func networkError(cause error) error     { return &Error{Kind: KindNetwork, Cause: cause} }
func timeoutError(cause error) error     { return &Error{Kind: KindTimeout, Cause: cause} }
func unsupportedError(cause error) error { return &Error{Kind: KindUnsupported, Cause: cause} }
func cancelledError(cause error) error   { return &Error{Kind: KindCancelled, Cause: cause} }
func internalError(cause error) error    { return &Error{Kind: KindInternal, Cause: cause} }
func staleTransportError() error         { return networkError(ErrStaleTransport) }
