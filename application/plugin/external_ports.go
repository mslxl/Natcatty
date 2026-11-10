package plugin

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// The external-access services are host-brokered. The application layer owns
// endpoint scope validation, DNS pinning, TLS and SSH trust policy, the
// authentication state machine, and secret-lease lifetime. The ports below are
// consumer-owned seams: the composition root wires the host's real resolver,
// SSH tunnel, trust store, authentication engine, and Vault. A plugin can
// never satisfy one of these ports with a grant to another plugin.

// External-access failure sentinels. The application maps them to the closed
// service response codes; they never carry payload content.
var (
	// ErrExternalUnsupported reports a transport capability the host cannot
	// provide, such as half-closing the read direction on a TLS stream.
	ErrExternalUnsupported = errors.New("external access: operation is not supported")
	// ErrExternalTrustRejected reports material the host refuses to trust.
	// A plugin cannot override it.
	ErrExternalTrustRejected = errors.New("external access: trust verification rejected the material")
	// ErrExternalUnavailable reports a host port that is not wired.
	ErrExternalUnavailable = errors.New("external access: host port is not configured")
)

// DNSPolicy names a permitted resolution policy. V1 offers address records
// only: SRV cannot be represented by an exact network scope and would widen
// the endpoint a grant names, so it is rejected.
type DNSPolicy string

const (
	// DNSPolicyAddress resolves A and AAAA records.
	DNSPolicyAddress DNSPolicy = "a-aaaa"
	// DNSPolicySRV is reserved and rejected in v1.
	DNSPolicySRV DNSPolicy = "srv"
)

// TLSClientPolicy is the host-owned TLS configuration for one connection or
// in-band upgrade. The guest never supplies ServerName or ALPN: the host
// derives the name from the granted endpoint and applies its own ALPN policy.
type TLSClientPolicy struct {
	// Mode is verify, system, or none. It comes from the granted network
	// scope, or from host policy for an in-band upgrade of a TCP grant.
	Mode string
	// ServerName is the host-owned name used for SNI and certificate
	// hostname verification.
	ServerName string
	// ALPN is the host-owned protocol list. Empty means the host offers no
	// ALPN.
	ALPN []string
}

// NetworkDialTarget is one host-owned dial. Address is the pinned, already
// resolved IP:port. Host is retained for SNI, certificate verification, and
// audit; it is never resolved again.
type NetworkDialTarget struct {
	Host     string
	Address  netip.AddrPort
	Protocol string
	TLS      TLSClientPolicy
	// ViaHost is the optional Host whose existing SSH trust, credentials,
	// proxies, and jump chain carry this connection. Empty dials directly.
	ViaHost string
	Timeout time.Duration
}

// ExternalSocket is one host-owned network connection. CloseWrite and
// CloseRead are the half-close operations; an unsupported direction returns
// ErrExternalUnsupported. SetDeadline bounds one read or write.
type ExternalSocket interface {
	Read(buffer []byte) (int, error)
	Write(buffer []byte) (int, error)
	CloseWrite() error
	CloseRead() error
	SetDeadline(deadline time.Time) error
	Close() error
}

// NetworkBroker is the host-side external-network port. Resolve must return
// every address it found and must not be called again for the same dial:
// DialPinned receives the pinned address and never resolves it. DialViaHost
// must use the Host's existing session transport, trust policy, and proxies;
// it must never accept plugin-supplied SSH configuration.
type NetworkBroker interface {
	Resolve(ctx context.Context, host string, policy DNSPolicy) ([]netip.Addr, error)
	DialPinned(ctx context.Context, target NetworkDialTarget) (ExternalSocket, error)
	DialViaHost(ctx context.Context, target NetworkDialTarget) (ExternalSocket, error)
	// UpgradeTLS wraps an existing socket in a client TLS session. The host
	// sends no application bytes during the handshake and owns every policy
	// input.
	UpgradeTLS(ctx context.Context, socket ExternalSocket, policy TLSClientPolicy) (ExternalSocket, error)
}

// TrustKind is the closed set of material the trust service verifies.
type TrustKind string

const (
	// TrustKindSSHHostKey verifies one SSH public host key blob.
	TrustKindSSHHostKey TrustKind = "ssh-hostkey"
	// TrustKindTLSCert verifies one X.509 server certificate.
	TrustKindTLSCert TrustKind = "tls-cert"
	// TrustKindCustomFingerprint verifies an opaque, host-confirmed
	// fingerprint.
	TrustKindCustomFingerprint TrustKind = "custom-fingerprint"
)

// TrustVerdict is the closed trust decision returned to a plugin.
type TrustVerdict string

const (
	// TrustVerdictTrusted means the material matches a pin the host trusts.
	TrustVerdictTrusted TrustVerdict = "trusted"
	// TrustVerdictUnknown means no pin exists. The host may have asked the
	// user; the plugin must not treat it as trusted.
	TrustVerdictUnknown TrustVerdict = "unknown"
	// TrustVerdictChanged means a pin exists for the same identity with
	// different material. It blocks by default.
	TrustVerdictChanged TrustVerdict = "changed"
	// TrustVerdictRevoked means the user revoked trust for this material.
	TrustVerdictRevoked TrustVerdict = "revoked"
)

// TrustRecord is one host-owned trust pin.
type TrustRecord struct {
	HostID      string
	Kind        TrustKind
	KeyType     string
	Fingerprint string
	// Revoked records a user revocation. A revoked material never matches as
	// trusted and cannot be re-confirmed without removing the revocation in
	// the host UI.
	Revoked   bool
	UpdatedAt time.Time
}

// TrustRequest is one verification request after the host computed the
// canonical fingerprint. Material is the original guest bytes, retained only
// so a host confirmer can render it; it is never returned to the guest or
// recorded in audit.
type TrustRequest struct {
	HostID      string
	Kind        TrustKind
	KeyType     string
	Fingerprint string
	Material    []byte
}

// TrustPinStore is the host's persistent trust store.
type TrustPinStore interface {
	// Pins returns every pin for one Host and kind so the caller can detect a
	// changed identity rather than looking only for an exact fingerprint.
	Pins(ctx context.Context, hostID string, kind TrustKind) ([]TrustRecord, error)
	Save(ctx context.Context, record TrustRecord) error
	// Delete removes one saved pin. It is a best-effort rollback for a
	// revocation that races a confirmation: the caller still returns the
	// capability-revoked status when the delete fails. A store that cannot
	// remove a pin returns ErrExternalUnsupported.
	Delete(ctx context.Context, record TrustRecord) error
}

// TrustConfirmer asks the host user to trust previously unknown material. It
// is host-owned UI: the plugin cannot answer, skip, or replace it.
type TrustConfirmer interface {
	Confirm(ctx context.Context, request TrustRequest) (bool, error)
}

// AuthMethod is the closed authentication method vocabulary a plugin may
// request. Credential collection, MFA prompts, and key selection stay
// host-owned.
type AuthMethod string

const (
	AuthMethodNone                AuthMethod = "none"
	AuthMethodPassword            AuthMethod = "password"
	AuthMethodPublicKey           AuthMethod = "publickey"
	AuthMethodAgent               AuthMethod = "agent"
	AuthMethodKeyboardInteractive AuthMethod = "keyboard-interactive"
	AuthMethodChallenge           AuthMethod = "challenge"
)

// AuthBeginRequest asks the host to start one authentication flow for a Host.
type AuthBeginRequest struct {
	HostID string
	Method AuthMethod
}

// AuthChallenge is the non-secret challenge description returned to a plugin.
// Prompt is host-authored UI text. It never contains a credential.
type AuthChallenge struct {
	ID         string
	HostID     string
	Method     AuthMethod
	Prompt     string
	CanRespond bool
}

// AuthStatus is the closed state of one authentication flow.
type AuthStatus string

const (
	AuthStatusPending   AuthStatus = "pending"
	AuthStatusSucceeded AuthStatus = "succeeded"
	AuthStatusFailed    AuthStatus = "failed"
	AuthStatusCancelled AuthStatus = "cancelled"
)

// AuthResult is the non-secret result of one respond or cancel call. Lease is
// an opaque host credential reference, never plaintext.
type AuthResult struct {
	ChallengeID string
	Status      AuthStatus
	Lease       string
}

// AuthBroker is the host-owned authentication engine. The plugin's answer is
// an opaque response to a host prompt; the broker resolves credentials from
// the Vault and never returns them.
type AuthBroker interface {
	Begin(ctx context.Context, request AuthBeginRequest) (AuthChallenge, error)
	Respond(ctx context.Context, challengeID, answer string) (AuthResult, error)
	Cancel(ctx context.Context, challengeID string) error
}

// VaultFieldDescriptor is non-secret metadata for one Vault field. The host,
// not the plugin, decides the class.
type VaultFieldDescriptor struct {
	HostID string `json:"host_id"`
	Field  string `json:"field"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	// HostExecuted fields (SSH private keys, TLS client certificates, and
	// host-side proxy keys) never enter the guest.
	HostExecuted bool `json:"host_executed"`
	// GuestDeliverable reports whether vault.secrets may issue a lease. Only a
	// guest-deliverable field can be leased.
	GuestDeliverable bool `json:"guest_deliverable"`
}

// VaultFieldSource is the host-side Vault port. Descriptors never include a
// value. Value is consulted only after the application checked the field
// class and the exact vault.secrets scope.
type VaultFieldSource interface {
	Descriptors(ctx context.Context, hostID string) ([]VaultFieldDescriptor, error)
	Value(ctx context.Context, hostID, field string) (string, error)
}

// MemoryNetworkBroker is a deterministic NetworkBroker for tests and a
// composition without a real transport. Addresses seed resolution; DialFunc
// and UpgradeFunc supply sockets.
type MemoryNetworkBroker struct {
	mu sync.Mutex

	// Addresses maps a host name to the addresses Resolve returns. A missing
	// name resolves to no addresses.
	Addresses map[string][]netip.Addr
	// ResolveErr, DialErr, and UpgradeErr force a failure.
	ResolveErr error
	DialErr    error
	UpgradeErr error
	// DialFunc and UpgradeFunc, when set, produce the socket.
	DialFunc    func(target NetworkDialTarget) (ExternalSocket, error)
	UpgradeFunc func(socket ExternalSocket, policy TLSClientPolicy) (ExternalSocket, error)

	resolveCalls int
	dials        []NetworkDialTarget
	viaDials     []NetworkDialTarget
	upgrades     []TLSClientPolicy
}

// Resolve implements NetworkBroker.
func (b *MemoryNetworkBroker) Resolve(_ context.Context, host string, policy DNSPolicy) ([]netip.Addr, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolveCalls++
	if b.ResolveErr != nil {
		return nil, b.ResolveErr
	}
	if policy != DNSPolicyAddress {
		return nil, ErrExternalUnsupported
	}
	return append([]netip.Addr(nil), b.Addresses[host]...), nil
}

// DialPinned implements NetworkBroker.
func (b *MemoryNetworkBroker) DialPinned(_ context.Context, target NetworkDialTarget) (ExternalSocket, error) {
	b.mu.Lock()
	b.dials = append(b.dials, target)
	dial := b.DialFunc
	err := b.DialErr
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if dial != nil {
		return dial(target)
	}
	return nil, ErrExternalUnavailable
}

// DialViaHost implements NetworkBroker.
func (b *MemoryNetworkBroker) DialViaHost(_ context.Context, target NetworkDialTarget) (ExternalSocket, error) {
	b.mu.Lock()
	b.viaDials = append(b.viaDials, target)
	dial := b.DialFunc
	err := b.DialErr
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if dial != nil {
		return dial(target)
	}
	return nil, ErrExternalUnavailable
}

// UpgradeTLS implements NetworkBroker.
func (b *MemoryNetworkBroker) UpgradeTLS(_ context.Context, socket ExternalSocket,
	policy TLSClientPolicy) (ExternalSocket, error) {
	b.mu.Lock()
	b.upgrades = append(b.upgrades, policy)
	upgrade := b.UpgradeFunc
	err := b.UpgradeErr
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if upgrade != nil {
		return upgrade(socket, policy)
	}
	return socket, nil
}

// ResolveCalls reports how many times Resolve ran. DNS-rebinding tests use it
// to prove the name was resolved once.
func (b *MemoryNetworkBroker) ResolveCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resolveCalls
}

// Dials returns the direct dial targets in order.
func (b *MemoryNetworkBroker) Dials() []NetworkDialTarget {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]NetworkDialTarget(nil), b.dials...)
}

// ViaDials returns the via-Host dial targets in order.
func (b *MemoryNetworkBroker) ViaDials() []NetworkDialTarget {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]NetworkDialTarget(nil), b.viaDials...)
}

// Upgrades returns the TLS policies used for in-band upgrades in order.
func (b *MemoryNetworkBroker) Upgrades() []TLSClientPolicy {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]TLSClientPolicy(nil), b.upgrades...)
}

// MemoryTrustPinStore is a deterministic TrustPinStore for tests and a
// composition without persistent pins.
type MemoryTrustPinStore struct {
	mu      sync.Mutex
	records []TrustRecord
}

// Pins implements TrustPinStore.
func (s *MemoryTrustPinStore) Pins(_ context.Context, hostID string, kind TrustKind) ([]TrustRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TrustRecord
	for _, record := range s.records {
		if record.HostID == hostID && record.Kind == kind {
			out = append(out, record)
		}
	}
	return out, nil
}

// Save implements TrustPinStore. A matching identity and fingerprint replaces
// the record; otherwise the record is appended so a changed fingerprint stays
// visible until the user revokes it.
func (s *MemoryTrustPinStore) Save(_ context.Context, record TrustRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		existing := &s.records[i]
		if existing.HostID == record.HostID && existing.Kind == record.Kind &&
			existing.KeyType == record.KeyType && existing.Fingerprint == record.Fingerprint {
			*existing = record
			return nil
		}
	}
	s.records = append(s.records, record)
	return nil
}

// Delete implements TrustPinStore. It removes every record for the same
// identity, which also rolls back a Save that replaced an equivalent pin.
func (s *MemoryTrustPinStore) Delete(_ context.Context, record TrustRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	for _, existing := range s.records {
		if existing.HostID == record.HostID && existing.Kind == record.Kind &&
			existing.KeyType == record.KeyType && existing.Fingerprint == record.Fingerprint {
			continue
		}
		kept = append(kept, existing)
	}
	s.records = kept
	return nil
}

// Revoke marks every matching pin revoked, for tests that exercise the
// host-owned revocation path.
func (s *MemoryTrustPinStore) Revoke(hostID string, kind TrustKind, fingerprint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		record := &s.records[i]
		if record.HostID == hostID && record.Kind == kind && record.Fingerprint == fingerprint {
			record.Revoked = true
		}
	}
}

// Records returns a copy of every pin.
func (s *MemoryTrustPinStore) Records() []TrustRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TrustRecord(nil), s.records...)
}

// MemoryTrustConfirmer records confirmation requests and returns a fixed
// decision.
type MemoryTrustConfirmer struct {
	mu        sync.Mutex
	Decision  bool
	Err       error
	Requests  []TrustRequest
	ConfirmFn func(TrustRequest) (bool, error)
}

// Confirm implements TrustConfirmer.
func (c *MemoryTrustConfirmer) Confirm(_ context.Context, request TrustRequest) (bool, error) {
	c.mu.Lock()
	c.Requests = append(c.Requests, request)
	decision := c.Decision
	err := c.Err
	fn := c.ConfirmFn
	c.mu.Unlock()
	if fn != nil {
		return fn(request)
	}
	return decision, err
}

// Confirmed returns a copy of every confirmation request.
func (c *MemoryTrustConfirmer) Confirmed() []TrustRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]TrustRequest(nil), c.Requests...)
}

// MemoryAuthBroker is a deterministic AuthBroker for tests.
type MemoryAuthBroker struct {
	mu sync.Mutex

	BeginErr   error
	RespondErr error
	CancelErr  error
	// Challenge overrides the challenge returned by Begin.
	Challenge *AuthChallenge
	// BeginFunc, when set, produces the challenge and can block. It is called
	// without the broker lock so a test can hold several begins in flight.
	BeginFunc func(request AuthBeginRequest) (AuthChallenge, error)
	// RespondFunc, when set, produces the answer result and can block. It is
	// called without the broker lock so a revocation can race the response.
	RespondFunc func(challengeID, answer string) (AuthResult, error)
	// Result overrides the result returned by Respond.
	Result *AuthResult

	begins   []AuthBeginRequest
	responds []string
	cancels  []string
}

// Begin implements AuthBroker.
func (b *MemoryAuthBroker) Begin(_ context.Context, request AuthBeginRequest) (AuthChallenge, error) {
	b.mu.Lock()
	b.begins = append(b.begins, request)
	err := b.BeginErr
	challenge := b.Challenge
	begin := b.BeginFunc
	b.mu.Unlock()
	if err != nil {
		return AuthChallenge{}, err
	}
	if begin != nil {
		return begin(request)
	}
	if challenge != nil {
		return *challenge, nil
	}
	return AuthChallenge{
		ID: "challenge-" + request.HostID, HostID: request.HostID, Method: request.Method,
		Prompt: "Host will complete this authentication", CanRespond: true,
	}, nil
}

// Respond implements AuthBroker.
func (b *MemoryAuthBroker) Respond(_ context.Context, challengeID, answer string) (AuthResult, error) {
	b.mu.Lock()
	b.responds = append(b.responds, answer)
	err := b.RespondErr
	result := b.Result
	respond := b.RespondFunc
	b.mu.Unlock()
	if err != nil {
		return AuthResult{}, err
	}
	if respond != nil {
		return respond(challengeID, answer)
	}
	if result != nil {
		return *result, nil
	}
	return AuthResult{ChallengeID: challengeID, Status: AuthStatusSucceeded}, nil
}

// Cancel implements AuthBroker.
func (b *MemoryAuthBroker) Cancel(_ context.Context, challengeID string) error {
	b.mu.Lock()
	b.cancels = append(b.cancels, challengeID)
	err := b.CancelErr
	b.mu.Unlock()
	return err
}

// Begins returns the recorded begin requests.
func (b *MemoryAuthBroker) Begins() []AuthBeginRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]AuthBeginRequest(nil), b.begins...)
}

// Answers returns the recorded respond answers.
func (b *MemoryAuthBroker) Answers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.responds...)
}

// Cancels returns the recorded cancel challenge IDs.
func (b *MemoryAuthBroker) Cancels() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.cancels...)
}

// MemoryVaultFields is a deterministic VaultFieldSource for tests.
type MemoryVaultFields struct {
	mu                sync.Mutex
	DescriptorsByHost map[string][]VaultFieldDescriptor
	Values            map[string]string
	DescriptorErr     error
	ValueErr          error
	descriptorCalls   []string
	valueCalls        []string
}

// NewMemoryVaultFields returns an empty Vault field source.
func NewMemoryVaultFields() *MemoryVaultFields {
	return &MemoryVaultFields{
		DescriptorsByHost: make(map[string][]VaultFieldDescriptor),
		Values:            make(map[string]string),
	}
}

// Descriptors implements VaultFieldSource.
func (m *MemoryVaultFields) Descriptors(_ context.Context, hostID string) ([]VaultFieldDescriptor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.descriptorCalls = append(m.descriptorCalls, hostID)
	if m.DescriptorErr != nil {
		return nil, m.DescriptorErr
	}
	return append([]VaultFieldDescriptor(nil), m.DescriptorsByHost[hostID]...), nil
}

// Value implements VaultFieldSource.
func (m *MemoryVaultFields) Value(_ context.Context, hostID, field string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.valueCalls = append(m.valueCalls, hostID+"/"+field)
	if m.ValueErr != nil {
		return "", m.ValueErr
	}
	value, ok := m.Values[hostID+"/"+field]
	if !ok {
		return "", ErrExternalUnavailable
	}
	return value, nil
}

// DescriptorCalls returns the Host IDs whose descriptors were read.
func (m *MemoryVaultFields) DescriptorCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.descriptorCalls...)
}

// ValueCalls returns the "host/field" pairs whose values were read.
func (m *MemoryVaultFields) ValueCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.valueCalls...)
}

// SortVaultDescriptors orders descriptors by field id for stable responses.
func SortVaultDescriptors(descriptors []VaultFieldDescriptor) {
	sort.Slice(descriptors, func(i, j int) bool {
		if descriptors[i].Field != descriptors[j].Field {
			return descriptors[i].Field < descriptors[j].Field
		}
		return descriptors[i].HostID < descriptors[j].HostID
	})
}

// authMethodKnown reports whether method belongs to the closed vocabulary.
func authMethodKnown(method AuthMethod) bool {
	switch method {
	case AuthMethodNone, AuthMethodPassword, AuthMethodPublicKey,
		AuthMethodAgent, AuthMethodKeyboardInteractive, AuthMethodChallenge:
		return true
	default:
		return false
	}
}

// trustKindKnown reports whether kind belongs to the closed trust vocabulary.
func trustKindKnown(kind TrustKind) bool {
	switch kind {
	case TrustKindSSHHostKey, TrustKindTLSCert, TrustKindCustomFingerprint:
		return true
	default:
		return false
	}
}

// canonicalNetworkHost lowercases a DNS name and removes one trailing dot.
// Network scopes are exact: the request must canonicalize to the granted
// spelling.
func canonicalNetworkHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}
