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
//
// profileExists reports whether a saved Proxy Profile still exists. A reference
// to a Profile that was deleted (or that never existed) is treated exactly as
// if the level had not selected a proxy, so resolution continues down the
// precedence chain instead of dead-ending on an unusable Profile. A nil
// profileExists treats every non-empty Profile ID as available; callers that
// can look Profiles up should always supply one.
func ResolveProxy(host, group ProxySource, globalProfileID string, profileExists func(id string) bool) ProxyChoice {
	profile := func(id string) string {
		id = strings.TrimSpace(id)
		if id == "" {
			return ""
		}
		if profileExists != nil && !profileExists(id) {
			return ""
		}
		return id
	}
	if host.Direct {
		return ProxyChoice{Direct: true}
	}
	if host.Manual != nil {
		return ProxyChoice{Manual: host.Manual}
	}
	if id := profile(host.ProfileID); id != "" {
		return ProxyChoice{ProfileID: id}
	}
	if group.Direct {
		return ProxyChoice{Direct: true}
	}
	if group.Manual != nil {
		return ProxyChoice{Manual: group.Manual}
	}
	if id := profile(group.ProfileID); id != "" {
		return ProxyChoice{ProfileID: id}
	}
	if id := profile(globalProfileID); id != "" {
		return ProxyChoice{ProfileID: id}
	}
	return ProxyChoice{Direct: true}
}
