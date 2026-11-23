package phoneshell

import (
	"strings"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// ProxyCommandCapability reports whether this build may execute a
// ProxyCommand and, when it may not, the reason the Proxy pages show. The
// phone builds keep execution closed until the platform passes device
// acceptance, exactly as the connection stack does (ticket 47); the
// configuration stays editable and sychronizable either way.
type ProxyCommandCapability struct {
	Supported bool
	Reason    string
}

// proxyCommandReason is the sentence the pages show when the platform cannot
// execute a ProxyCommand. It reuses the connection error's wording so the
// editor and the failed connection never disagree.
func proxyCommandReason(capability ProxyCommandCapability) string {
	if capability.Supported {
		return ""
	}
	if reason := strings.TrimSpace(capability.Reason); reason != "" {
		return reason
	}
	return appconnection.ErrProxyCommandUnavailable.Error()
}

// WithProxyCommandCapability overrides the build's platform capability. Tests
// use it to exercise both states on one host; production leaves the build's
// default in place.
func WithProxyCommandCapability(capability ProxyCommandCapability) Option {
	return func(s *Shell) { s.proxyCommand = capability }
}
