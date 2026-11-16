package connection

import "strings"

// ProxySource is one level's proxy selection, before inheritance is applied.
// Manual, ProfileID, and Direct are mutually exclusive at a single level; the
// resolver picks the first set value in that order.
type ProxySource struct {
	// Manual is a manually configured transport at this level.
	Manual *Proxy
	// ProfileID selects a saved Proxy Profile. Empty means this level does not
	// choose one.
	ProfileID string
	// Direct is an explicit direct connection. It stops inheritance from the
	// levels below it.
	Direct bool
}

// ProxyChoice is the winning proxy selection for a route's first endpoint.
type ProxyChoice struct {
	// Manual is the winning manual transport, or nil.
	Manual *Proxy
	// ProfileID is the winning saved Proxy Profile, or empty.
	ProfileID string
	// Direct reports that the route must not use a proxy.
	Direct bool
}

// ResolveProxy applies the connection proxy precedence: the Host's own
// setting, the nearest Host Group's setting, the app-wide default, then a
// direct connection. An explicit direct choice at the Host or Group level wins
// over every lower level, including the app-wide default.
func ResolveProxy(host, group ProxySource, globalProfileID string) ProxyChoice {
	if host.Direct {
		return ProxyChoice{Direct: true}
	}
	if host.Manual != nil {
		return ProxyChoice{Manual: host.Manual}
	}
	if profile := strings.TrimSpace(host.ProfileID); profile != "" {
		return ProxyChoice{ProfileID: profile}
	}
	if group.Direct {
		return ProxyChoice{Direct: true}
	}
	if group.Manual != nil {
		return ProxyChoice{Manual: group.Manual}
	}
	if profile := strings.TrimSpace(group.ProfileID); profile != "" {
		return ProxyChoice{ProfileID: profile}
	}
	if profile := strings.TrimSpace(globalProfileID); profile != "" {
		return ProxyChoice{ProfileID: profile}
	}
	return ProxyChoice{Direct: true}
}
