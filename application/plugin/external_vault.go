package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// secretLease is one guest-deliverable Vault value. The value lives only in
// host memory, is bound to one vault.secrets scope, one Fiber (the session
// owns the map), and optionally one external connection. Revocation, TTL
// expiry, and connection close all clear it.
type secretLease struct {
	handle     Handle
	scope      domainplugin.Scope
	value      []byte
	expires    time.Time
	connection Handle
	bound      bool

	mu      sync.Mutex
	cleared bool
}

// read returns a copy of the value. It reports false after the lease was
// cleared by revocation or expiry.
func (l *secretLease) read() ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cleared {
		return nil, false
	}
	return append([]byte(nil), l.value...), true
}

// clear zeroes the value. It is idempotent.
func (l *secretLease) clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	for i := range l.value {
		l.value[i] = 0
	}
	l.value = nil
	l.cleared = true
	l.mu.Unlock()
}

// expired reports whether the lease lifetime has ended.
func (l *secretLease) expired(now time.Time) bool {
	return l != nil && !now.Before(l.expires)
}

// VaultLease is the non-secret lease descriptor returned to the guest.
type VaultLease struct {
	Handle     Handle    `json:"handle"`
	HostID     string    `json:"hostId"`
	Field      string    `json:"field"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Connection Handle    `json:"connection,omitempty"`
}

// VaultLeaseValue is one read of a guest-deliverable secret. The bytes are
// base64-encoded on the wire; they are never audited.
type VaultLeaseValue struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// VaultMetadata returns descriptors for the Vault fields this Fiber may read
// on hostID. The host's field class decides which descriptors are exposed;
// each included field is checked individually against the exact
// vault.metadata scope, so a Host-wide metadata call never widens a grant.
func (s *ServiceSession) VaultMetadata(ctx context.Context, hostID string) ([]VaultFieldDescriptor, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return nil, s.recordFailure(ServiceVault, "metadata",
			domainplugin.PermissionVaultMetadata, domainplugin.Scope{},
			workspaceInvalid("vault.metadata requires a Host ID"))
	}
	if s == nil || s.host == nil || s.host.authority == nil {
		return nil, &ServiceError{Service: ServiceVault, Method: "metadata",
			Code: ServiceCodeFault, Permission: domainplugin.PermissionVaultMetadata,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	if s.isClosed() {
		return nil, &ServiceError{Service: ServiceVault, Method: "metadata",
			Code: ServiceCodeInvalid, Permission: domainplugin.PermissionVaultMetadata,
			Message: "the plugin instance is closed"}
	}
	fields := s.grantedVaultFields(domainplugin.PermissionVaultMetadata, hostID)
	if len(fields) == 0 {
		scope := domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID}
		s.record(ServiceVault, "metadata", domainplugin.PermissionVaultMetadata,
			scope, ServiceCodeDenied)
		cause := &ServiceError{
			Service: ServiceVault, Method: "metadata", Code: ServiceCodeDenied,
			Permission: domainplugin.PermissionVaultMetadata, Scope: scope,
			Message: "no vault.metadata grant for this Host",
			Cause:   fmt.Errorf("%w: %s", ErrPermissionNotGranted, domainplugin.PermissionVaultMetadata),
		}
		return nil, cause
	}
	if s.host.vault == nil {
		return nil, s.recordFailure(ServiceVault, "metadata",
			domainplugin.PermissionVaultMetadata,
			domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID},
			externalUnavailable("vault field source"))
	}
	descriptors, err := s.host.vault.Descriptors(ctx, hostID)
	if err != nil {
		return nil, s.recordFailure(ServiceVault, "metadata",
			domainplugin.PermissionVaultMetadata,
			domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID},
			workspaceErrorf(ServiceCodeFault, "vault field source: %v", err))
	}
	out := make([]VaultFieldDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Field == "" || (descriptor.HostID != "" && descriptor.HostID != hostID) {
			continue
		}
		scope := domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID, Field: descriptor.Field}
		if err := s.host.authority.Check(s.subject, domainplugin.PermissionVaultMetadata, scope); err != nil {
			continue
		}
		out = append(out, descriptor)
	}
	SortVaultDescriptors(out)
	for _, descriptor := range out {
		s.recordSuccess(ServiceVault, "metadata", domainplugin.PermissionVaultMetadata,
			domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID, Field: descriptor.Field})
	}
	return out, nil
}

// VaultFields returns the host-owned descriptor for one exact field. It
// reveals the field class (host-executed or guest-deliverable) but never a
// value.
func (s *ServiceSession) VaultFields(ctx context.Context, hostID,
	field string) (VaultFieldDescriptor, error) {
	hostID = strings.TrimSpace(hostID)
	field = strings.TrimSpace(field)
	if hostID == "" || field == "" {
		return VaultFieldDescriptor{}, s.recordFailure(ServiceVault, "fields",
			domainplugin.PermissionVaultMetadata, domainplugin.Scope{},
			workspaceInvalid("vault.fields requires a Host ID and a field ID"))
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID, Field: field}
	if err := s.check(ServiceVault, "fields", domainplugin.PermissionVaultMetadata, scope); err != nil {
		return VaultFieldDescriptor{}, err
	}
	if s.host.vault == nil {
		return VaultFieldDescriptor{}, s.recordFailure(ServiceVault, "fields",
			domainplugin.PermissionVaultMetadata, scope, externalUnavailable("vault field source"))
	}
	descriptors, err := s.host.vault.Descriptors(ctx, hostID)
	if err != nil {
		return VaultFieldDescriptor{}, s.recordFailure(ServiceVault, "fields",
			domainplugin.PermissionVaultMetadata, scope,
			workspaceErrorf(ServiceCodeFault, "vault field source: %v", err))
	}
	for _, descriptor := range descriptors {
		if descriptor.Field == field && (descriptor.HostID == "" || descriptor.HostID == hostID) {
			s.recordSuccess(ServiceVault, "fields", domainplugin.PermissionVaultMetadata, scope)
			return descriptor, nil
		}
	}
	return VaultFieldDescriptor{}, s.recordFailure(ServiceVault, "fields",
		domainplugin.PermissionVaultMetadata, scope,
		workspaceNotFound("vault field %q does not exist on this Host", field))
}

// VaultLeaseIssue issues a short-lived lease for one guest-deliverable field.
// A host-executed field is denied regardless of what the plugin asks for; the
// host's field class decides. The lease is bound to the exact vault.secrets
// scope, the Fiber session, and, when connection is non-zero, that live
// external connection.
func (s *ServiceSession) VaultLeaseIssue(ctx context.Context, hostID, field string,
	ttl time.Duration, connection Handle) (VaultLease, error) {
	hostID = strings.TrimSpace(hostID)
	field = strings.TrimSpace(field)
	if hostID == "" || field == "" {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, domainplugin.Scope{},
			workspaceInvalid("vault.lease_issue requires a Host ID and a field ID"))
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeVault, Host: hostID, Field: field}
	if ttl < 0 {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope,
			workspaceInvalid("vault.lease_issue TTL cannot be negative"))
	}
	if err := s.check(ServiceVault, "lease_issue", domainplugin.PermissionVaultSecrets, scope); err != nil {
		return VaultLease{}, err
	}
	if s.host.vault == nil {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope, externalUnavailable("vault field source"))
	}
	if ttl <= 0 || ttl > s.host.limits.SecretLeaseTTL {
		ttl = s.host.limits.SecretLeaseTTL
	}

	// Field class is host-owned: a private key or client certificate can never
	// be delivered to the guest, even with a vault.secrets grant.
	descriptor, ok, err := s.vaultDescriptor(ctx, hostID, field)
	if err != nil {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope, err)
	}
	if !ok {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope,
			workspaceNotFound("vault field %q does not exist on this Host", field))
	}
	if descriptor.HostExecuted || !descriptor.GuestDeliverable {
		s.record(ServiceVault, "lease_issue", domainplugin.PermissionVaultSecrets,
			scope, ServiceCodeDenied)
		return VaultLease{}, &ServiceError{
			Service: ServiceVault, Method: "lease_issue", Code: ServiceCodeDenied,
			Permission: domainplugin.PermissionVaultSecrets, Scope: scope,
			Message: "this vault field is host-executed and cannot be delivered to the guest",
			Cause:   fmt.Errorf("%w: host-executed vault field", ErrScopeDenied),
		}
	}

	var boundConn *networkConnection
	if connection != 0 {
		conn, connErr := s.liveNetworkConnection(connection)
		if connErr != nil {
			return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
				domainplugin.PermissionVaultSecrets, scope,
				workspaceInvalid("vault lease connection %#x is not a live connection",
					uint64(connection)))
		}
		boundConn = conn
	}

	s.reapExpiredLeases()
	epoch, err := s.reserveLeaseSlot()
	if err != nil {
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope, err)
	}
	value, err := s.host.vault.Value(ctx, hostID, field)
	if err != nil {
		s.releaseLeaseSlot()
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope,
			workspaceErrorf(ServiceCodeFault, "vault field source: %v", err))
	}
	if len(value) > s.host.limits.SecretValueBytes {
		s.releaseLeaseSlot()
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope,
			workspaceLimit("vault value exceeds the %d-byte lease limit",
				s.host.limits.SecretValueBytes))
	}
	now := s.host.now()
	lease := &secretLease{
		scope:      scope,
		value:      []byte(value),
		expires:    now.Add(ttl),
		connection: connection,
		bound:      connection != 0,
	}
	handle, err := s.commitLease(epoch, lease, boundConn)
	if err != nil {
		lease.clear()
		return VaultLease{}, s.recordFailure(ServiceVault, "lease_issue",
			domainplugin.PermissionVaultSecrets, scope, err)
	}
	lease.handle = handle
	s.recordSuccess(ServiceVault, "lease_issue", domainplugin.PermissionVaultSecrets, scope)
	return VaultLease{
		Handle:     handle,
		HostID:     hostID,
		Field:      field,
		ExpiresAt:  lease.expires,
		Connection: connection,
	}, nil
}

// VaultLeaseRead returns a copy of a live lease's value. The scope is checked
// again on every read; a denial invalidates the lease.
func (s *ServiceSession) VaultLeaseRead(ctx context.Context,
	handle Handle) (VaultLeaseValue, error) {
	lease, err := s.liveLease(handle)
	if err != nil {
		return VaultLeaseValue{}, s.recordFailure(ServiceVault, "lease_read",
			domainplugin.PermissionVaultSecrets, domainplugin.Scope{}, err)
	}
	if err := s.check(ServiceVault, "lease_read", domainplugin.PermissionVaultSecrets,
		lease.scope); err != nil {
		s.dropLease(handle, lease)
		return VaultLeaseValue{}, err
	}
	if lease.expired(s.host.now()) {
		s.dropLease(handle, lease)
		return VaultLeaseValue{}, s.recordFailure(ServiceVault, "lease_read",
			domainplugin.PermissionVaultSecrets, lease.scope,
			workspaceInvalid("the vault lease expired"))
	}
	if lease.bound && !s.leaseConnectionLive(lease.connection) {
		s.expireConnectionLeases(lease.connection)
		s.dropLease(handle, lease)
		return VaultLeaseValue{}, s.recordFailure(ServiceVault, "lease_read",
			domainplugin.PermissionVaultSecrets, lease.scope,
			workspaceInvalid("the vault lease connection is closed"))
	}
	value, ok := lease.read()
	if !ok {
		s.dropLease(handle, lease)
		return VaultLeaseValue{}, s.recordFailure(ServiceVault, "lease_read",
			domainplugin.PermissionVaultSecrets, lease.scope,
			workspaceInvalid("the vault lease was revoked"))
	}
	s.recordSuccess(ServiceVault, "lease_read", domainplugin.PermissionVaultSecrets, lease.scope)
	return VaultLeaseValue{Value: value, ExpiresAt: lease.expires}, nil
}

// VaultLeaseRelease destroys one lease. It is the explicit counterpart to
// issue; TTL expiry, revocation, unload, and connection close release leases
// without it.
func (s *ServiceSession) VaultLeaseRelease(ctx context.Context, handle Handle) error {
	lease, err := s.liveLease(handle)
	if err != nil {
		return s.recordFailure(ServiceVault, "lease_release",
			domainplugin.PermissionVaultSecrets, domainplugin.Scope{}, err)
	}
	s.dropLease(handle, lease)
	s.recordSuccess(ServiceVault, "lease_release", domainplugin.PermissionVaultSecrets, lease.scope)
	return nil
}

// leaseConnectionLive resolves a lease's bound connection and confirms it is
// still open and within its idle window.
func (s *ServiceSession) leaseConnectionLive(connection Handle) bool {
	conn, err := s.liveNetworkConnection(connection)
	if err != nil {
		return false
	}
	return conn.live()
}

// vaultDescriptor loads the host-owned descriptor for one exact field.
func (s *ServiceSession) vaultDescriptor(ctx context.Context, hostID,
	field string) (VaultFieldDescriptor, bool, error) {
	descriptors, err := s.host.vault.Descriptors(ctx, hostID)
	if err != nil {
		return VaultFieldDescriptor{}, false, workspaceErrorf(ServiceCodeFault,
			"vault field source: %v", err)
	}
	for _, descriptor := range descriptors {
		if descriptor.Field == field && (descriptor.HostID == "" || descriptor.HostID == hostID) {
			return descriptor, true, nil
		}
	}
	return VaultFieldDescriptor{}, false, nil
}

// grantedVaultFields enumerates the fields that carry an exact, non-pending
// grant for one Host. It is the enumeration step; every descriptor still goes
// through an exact Authority check before it is returned.
func (s *ServiceSession) grantedVaultFields(permission domainplugin.Permission,
	hostID string) []string {
	var fields []string
	for _, grant := range s.host.authority.Grants(s.subject) {
		if grant.Permission != permission || grant.Pending {
			continue
		}
		if grant.Scope.Kind != domainplugin.ScopeVault || grant.Scope.Host != hostID {
			continue
		}
		if grant.Scope.Field == "" {
			continue
		}
		fields = append(fields, grant.Scope.Field)
	}
	return fields
}

// reserveLeaseSlot counts an in-flight lease before the slow value read. The
// epoch proves no revocation happened while reading.
func (s *ServiceSession) reserveLeaseSlot() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, workspaceInvalid("the plugin instance is closed")
	}
	if len(s.leases)+s.pendingLeases >= s.host.limits.SecretLeases {
		return 0, workspaceLimit("vault lease limit of %d reached",
			s.host.limits.SecretLeases)
	}
	s.pendingLeases++
	return s.handleEpoch, nil
}

func (s *ServiceSession) releaseLeaseSlot() {
	s.mu.Lock()
	if s.pendingLeases > 0 {
		s.pendingLeases--
	}
	s.mu.Unlock()
}

// commitLease inserts a finished lease only when the authority epoch captured
// before the value read is still current and, for a connection-bound lease,
// the connection is still live.
func (s *ServiceSession) commitLease(epoch uint64, lease *secretLease,
	connection *networkConnection) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingLeases > 0 {
		s.pendingLeases--
	}
	if s.closed || s.handleEpoch != epoch {
		return 0, workspaceInvalid("the plugin instance was revoked while issuing a lease")
	}
	if lease.bound && s.network[lease.connection] != connection {
		return 0, workspaceInvalid("the vault lease connection closed before issue completed")
	}
	if s.handles == nil {
		return 0, &workspaceFailure{Code: ServiceCodeFault,
			Problem: "handle table is not bound", Cause: ErrServiceUnavailable}
	}
	handle, err := s.handles.Issue(lease)
	if err != nil {
		return 0, err
	}
	s.leases[handle] = lease
	return handle, nil
}

// liveLease resolves a lease handle and confirms it is still current.
func (s *ServiceSession) liveLease(handle Handle) (*secretLease, error) {
	if s == nil {
		return nil, invalidVaultLease(handle)
	}
	s.mu.Lock()
	lease := s.leases[handle]
	handles := s.handles
	closed := s.closed
	s.mu.Unlock()
	if closed || lease == nil || handles == nil {
		return nil, invalidVaultLease(handle)
	}
	value, err := handles.Resolve(handle)
	if err != nil || value != lease {
		return nil, invalidVaultLease(handle)
	}
	return lease, nil
}

func invalidVaultLease(handle Handle) error {
	return &ServiceError{
		Service: ServiceVault, Method: "lease", Code: ServiceCodeInvalid,
		Permission: domainplugin.PermissionVaultSecrets,
		Message:    "vault lease handle is invalid, expired, or revoked",
		Cause:      &InvalidHandleError{Handle: handle},
	}
}

// dropLease removes one exact lease, clears its value, and releases its slot.
func (s *ServiceSession) dropLease(handle Handle, lease *secretLease) {
	if s == nil || lease == nil {
		return
	}
	s.mu.Lock()
	if current, ok := s.leases[handle]; ok && current == lease {
		delete(s.leases, handle)
	}
	table := s.handles
	s.mu.Unlock()
	lease.clear()
	if table != nil {
		_ = table.Release(handle)
	}
}

// reapExpiredLeases drops leases whose TTL has passed so an expired lease
// never consumes the per-fiber quota.
func (s *ServiceSession) reapExpiredLeases() {
	if s == nil {
		return
	}
	now := s.host.now()
	type expired struct {
		handle Handle
		lease  *secretLease
	}
	var dead []expired
	s.mu.Lock()
	for handle, lease := range s.leases {
		if lease.expired(now) {
			dead = append(dead, expired{handle: handle, lease: lease})
			delete(s.leases, handle)
		}
	}
	table := s.handles
	s.mu.Unlock()
	for _, entry := range dead {
		entry.lease.clear()
		if table != nil {
			_ = table.Release(entry.handle)
		}
	}
}
