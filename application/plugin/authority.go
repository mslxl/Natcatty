package plugin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ErrPermissionNotGranted reports a capability check with no matching grant.
var ErrPermissionNotGranted = errors.New("plugin: permission is not granted")

// ErrScopeRequired reports a resource-scoped permission that was consented to
// without a concrete scope. The caller must obtain a first-use scope grant.
var ErrScopeRequired = errors.New("plugin: permission requires a resource scope")

// ErrScopeDenied reports a capability check whose concrete scope does not
// match the granted scope.
var ErrScopeDenied = errors.New("plugin: permission scope does not match the grant")

// CallerKind names who originated a capability check. Every entry point uses
// the same structured permission and scope check; the kind is recorded so a
// host-side caller cannot acquire ambient authority by presenting itself as a
// different surface.
type CallerKind string

const (
	// CallerGuest is a plugin guest calling a host service through the ABI.
	CallerGuest CallerKind = "guest"
	// CallerUI is a host UI surface acting for one plugin, for example a view
	// opening and reading plugin-owned data.
	CallerUI CallerKind = "ui"
	// CallerMCP is an MCP tool invocation routed through the plugin backend.
	CallerMCP CallerKind = "mcp"
	// CallerPlugin is a cross-plugin service call made by another plugin.
	CallerPlugin CallerKind = "plugin"
)

// Caller identifies the authority subject a check is made against and the
// surface that originated it. Subject is always the plugin whose grants are
// consulted; a cross-plugin caller passes the provider's subject and names
// itself in PluginID.
type Caller struct {
	Subject  AuthoritySubject
	Kind     CallerKind
	PluginID string
}

// Authorize applies the same default-deny structured-scope check for every
// caller kind. UI, MCP, and cross-plugin entry points call it exactly as the
// guest host-service boundary does; there is no ambient authority and an empty
// subject is always denied.
func (a *Authority) Authorize(caller Caller, permission domainplugin.Permission,
	scope domainplugin.Scope) error {
	if a == nil || caller.Subject == "" {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	return a.Check(caller.Subject, permission, scope)
}

// Granted reports whether one permission has any active grant. It is used by
// enumeration methods such as sessions.list: the exact resource check still
// gates every named resource, while the grant itself proves the plugin was
// consented to the capability at all.
func (a *Authority) Granted(subject AuthoritySubject, permission domainplugin.Permission) (domainplugin.PermissionGrant, bool) {
	if a == nil {
		return domainplugin.PermissionGrant{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	grant, ok := a.stateLocked(subject).grants[permission]
	if !ok || grant.Pending {
		return domainplugin.PermissionGrant{}, false
	}
	return grant, true
}

// AuthoritySubject identifies the authority domain of one Plugin Instance. It
// combines the install source (or development loader) with the plugin id, so a
// development copy and an installed package that share an id have independent
// grants and independent revocation epochs.
type AuthoritySubject string

// SubjectForInstalled returns the authority subject for an installed package.
func SubjectForInstalled(source domainplugin.Source, id string) AuthoritySubject {
	return AuthoritySubject(string(source) + "/" + id)
}

// SubjectForDevelopment returns the authority subject for an unpacked
// development directory.
func SubjectForDevelopment(id string) AuthoritySubject {
	return SubjectForInstalled(domainplugin.SourceDevelopment, id)
}

// SubjectForRef returns the authority subject for a loaded plugin identity.
func SubjectForRef(ref PluginRef) AuthoritySubject {
	return SubjectForInstalled(ref.Source, ref.ID)
}

// CapabilityRevokedError reports a handle issued under an authority epoch
// that a revocation superseded. Every issued handle fails immediately, even a
// handle for a different permission of the same plugin.
type CapabilityRevokedError struct {
	Subject    AuthoritySubject
	Permission domainplugin.Permission
}

func (e *CapabilityRevokedError) Error() string {
	if e.Permission == "" {
		return fmt.Sprintf("plugin %s: capability was revoked", e.Subject)
	}
	return fmt.Sprintf("plugin %s: capability %s was revoked", e.Subject, e.Permission)
}

// Authority is the host-side default-deny capability authority. Grants are
// resolved into generation-tagged handles; revocation bumps the plugin's
// epoch and invalidates every outstanding handle.
type Authority struct {
	mu     sync.Mutex
	states map[AuthoritySubject]*authorityState
	// onEpochChange runs after a grant replacement, revocation, or reset.
	// The host-service layer uses it to destroy every stateful handle
	// (subscriptions and their queued events) issued under the old epoch.
	onEpochChange func(AuthoritySubject)
}

type authorityState struct {
	epoch  uint64
	grants map[domainplugin.Permission]domainplugin.PermissionGrant
}

// NewAuthority returns an empty default-deny authority.
func NewAuthority() *Authority {
	return &Authority{states: make(map[AuthoritySubject]*authorityState)}
}

// SetGrants replaces one plugin's grants and starts a fresh epoch. Every older
// handle is invalidated.
func (a *Authority) SetGrants(subject AuthoritySubject, grants []domainplugin.PermissionGrant) {
	if a == nil {
		return
	}
	a.mu.Lock()
	state := a.stateLocked(subject)
	state.epoch++
	state.grants = make(map[domainplugin.Permission]domainplugin.PermissionGrant, len(grants))
	for _, grant := range grants {
		state.grants[grant.Permission] = grant
	}
	a.mu.Unlock()
	a.notifyEpochChange(subject)
}

// SetEpochHook installs the observer invoked whenever a subject's epoch
// advances. The host-service layer registers one to invalidate live handles.
// The hook runs without the authority lock held.
func (a *Authority) SetEpochHook(fn func(AuthoritySubject)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.onEpochChange = fn
	a.mu.Unlock()
}

func (a *Authority) notifyEpochChange(subject AuthoritySubject) {
	if a == nil {
		return
	}
	a.mu.Lock()
	hook := a.onEpochChange
	a.mu.Unlock()
	if hook != nil {
		hook(subject)
	}
}

// Grant adds or replaces one grant. It leaves the epoch and other grants
// untouched.
func (a *Authority) Grant(subject AuthoritySubject, grant domainplugin.PermissionGrant) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stateLocked(subject).grants[grant.Permission] = grant
}

// Revoke removes one permission grant and increments the plugin's epoch,
// invalidating every issued handle for the plugin. It reports whether a grant
// existed.
func (a *Authority) Revoke(subject AuthoritySubject, permission domainplugin.Permission) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	state := a.stateLocked(subject)
	state.epoch++
	_, existed := state.grants[permission]
	delete(state.grants, permission)
	a.mu.Unlock()
	a.notifyEpochChange(subject)
	return existed
}

// Epoch returns the current generation for a plugin. Tests and diagnostics use
// it to prove a revocation advanced.
func (a *Authority) Epoch(subject AuthoritySubject) uint64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stateLocked(subject).epoch
}

// Grants returns a copy of one subject's active grants in permission order.
// The diagnostics view uses it to render the grant table; it never issues a
// capability handle.
func (a *Authority) Grants(subject AuthoritySubject) []domainplugin.PermissionGrant {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.stateLocked(subject)
	out := make([]domainplugin.PermissionGrant, 0, len(state.grants))
	for _, grant := range state.grants {
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Permission != out[j].Permission {
			return out[i].Permission < out[j].Permission
		}
		return fmt.Sprintf("%+v", out[i].Scope) < fmt.Sprintf("%+v", out[j].Scope)
	})
	return out
}

// Reset drops every grant and starts a fresh epoch for a plugin.
func (a *Authority) Reset(subject AuthoritySubject) {
	if a == nil {
		return
	}
	a.mu.Lock()
	state := a.stateLocked(subject)
	state.epoch++
	state.grants = make(map[domainplugin.Permission]domainplugin.PermissionGrant)
	a.mu.Unlock()
	a.notifyEpochChange(subject)
}

// Check verifies a concrete resource check against the current grants without
// issuing a handle.
func (a *Authority) Check(subject AuthoritySubject, permission domainplugin.Permission,
	scope domainplugin.Scope) error {
	if a == nil {
		return ErrPermissionNotGranted
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.stateLocked(subject)
	grant, ok := state.grants[permission]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	if grant.Pending {
		return fmt.Errorf("%w: %s", ErrScopeRequired, permission)
	}
	if !grant.Scope.Equal(scope) {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	return nil
}

// CheckNetwork verifies one concrete network endpoint against a network
// grant. The grant may name several ports; the dialed port must be one of
// them. Host, protocol, and TLS policy still match exactly, and no wildcard,
// CIDR, or range is accepted.
func (a *Authority) CheckNetwork(subject AuthoritySubject,
	permission domainplugin.Permission, required domainplugin.Scope) error {
	if a == nil {
		return ErrPermissionNotGranted
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.stateLocked(subject)
	grant, ok := state.grants[permission]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	if grant.Pending {
		return fmt.Errorf("%w: %s", ErrScopeRequired, permission)
	}
	scope := grant.Scope
	if scope.Kind != domainplugin.ScopeNetwork || required.Kind != domainplugin.ScopeNetwork {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	if scope.Host != required.Host || scope.Protocol != required.Protocol ||
		scope.TLS != required.TLS || len(required.Ports) != 1 {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	if len(scope.Ports) == 0 {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	for _, port := range scope.Ports {
		if port == required.Ports[0] {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
}

// CheckService verifies one services.scope. Unlike Scope.Equal, a services
// grant matches semantically: services.provide accepts the plugin's own
// namespace prefix, services.invoke requires the exact service and provider
// identity, a grant's access class covers read when it is write, and a grant's
// semver range must contain the requested version. A grant never matches a
// different provider.
func (a *Authority) CheckService(subject AuthoritySubject, permission domainplugin.Permission,
	required domainplugin.Scope, prefixAllowed bool) error {
	if a == nil {
		return ErrPermissionNotGranted
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.stateLocked(subject)
	grant, ok := state.grants[permission]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
	}
	if grant.Pending {
		return fmt.Errorf("%w: %s", ErrScopeRequired, permission)
	}
	scope := grant.Scope
	if scope.Kind != domainplugin.ScopeServices || required.Kind != domainplugin.ScopeServices {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	switch {
	case prefixAllowed && scope.IsServicePrefix():
		if !strings.HasPrefix(required.Service, scope.Service) {
			return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
		}
	case scope.Service != required.Service:
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	if required.Provider != "" {
		if scope.Provider == "" || scope.Provider != required.Provider {
			return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
		}
	}
	if !serviceAccessCovers(scope.Access, required.Access) {
		return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
	}
	if required.Version != "" && scope.Version != "" {
		version, ok := domainplugin.ParseSemver(required.Version)
		if !ok {
			version, ok = domainplugin.ParseSemver(domainplugin.DefaultServiceProvisionVersion)
		}
		if !ok || !domainplugin.RangeContains(scope.Version, version) {
			return fmt.Errorf("%w: %s", ErrScopeDenied, permission)
		}
	}
	return nil
}

// HasServiceGrant reports whether a services grant covers the required scope.
func (a *Authority) HasServiceGrant(subject AuthoritySubject,
	permission domainplugin.Permission, required domainplugin.Scope,
	prefixAllowed bool) bool {
	return a.CheckService(subject, permission, required, prefixAllowed) == nil
}

// serviceAccessCovers reports whether a granted access class covers a
// required one. A write grant may invoke read methods; a read grant never
// invokes a write method.
func serviceAccessCovers(granted, required string) bool {
	if granted == required {
		return true
	}
	return granted == domainplugin.ServiceAccessWrite && required == domainplugin.ServiceAccessRead
}

// CapabilityHandle is one issued capability. Check fails after any revocation of the
// plugin's authority, including a revocation for another permission.
type CapabilityHandle struct {
	authority  *Authority
	subject    AuthoritySubject
	permission domainplugin.Permission
	scope      domainplugin.Scope
	epoch      uint64
}

// Issue returns a generation-tagged handle for a granted permission and
// scope. It fails when the grant is absent, pending, or scope-mismatched.
func (a *Authority) Issue(subject AuthoritySubject, permission domainplugin.Permission,
	scope domainplugin.Scope) (*CapabilityHandle, error) {
	if err := a.Check(subject, permission, scope); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.stateLocked(subject)
	return &CapabilityHandle{
		authority:  a,
		subject:    subject,
		permission: permission,
		scope:      scope,
		epoch:      state.epoch,
	}, nil
}

// Check verifies that the handle's epoch is still current.
func (h *CapabilityHandle) Check() error {
	if h == nil || h.authority == nil {
		return ErrPermissionNotGranted
	}
	h.authority.mu.Lock()
	defer h.authority.mu.Unlock()
	state := h.authority.stateLocked(h.subject)
	if state.epoch != h.epoch {
		return &CapabilityRevokedError{Subject: h.subject, Permission: h.permission}
	}
	grant, ok := state.grants[h.permission]
	if !ok || grant.Pending || !grant.Scope.Equal(h.scope) {
		return &CapabilityRevokedError{Subject: h.subject, Permission: h.permission}
	}
	return nil
}

// Permission returns the permission the handle carries.
func (h *CapabilityHandle) Permission() domainplugin.Permission {
	if h == nil {
		return ""
	}
	return h.permission
}

// Scope returns the scope the handle carries.
func (h *CapabilityHandle) Scope() domainplugin.Scope {
	if h == nil {
		return domainplugin.Scope{}
	}
	return h.scope
}

func (a *Authority) stateLocked(subject AuthoritySubject) *authorityState {
	state, ok := a.states[subject]
	if !ok {
		state = &authorityState{grants: make(map[domainplugin.Permission]domainplugin.PermissionGrant)}
		a.states[subject] = state
	}
	return state
}
