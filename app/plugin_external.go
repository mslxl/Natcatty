package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// The external-access adapters are the only place a plugin's network, auth,
// trust, and Vault requests become real host operations. The application
// plugin package checks scope, field class, quotas, and audit before calling
// these ports; these adapters add no capability of their own.

// pluginNetworkBroker owns DNS, pinned dialing, TLS, and SSH tunnelling. A
// plugin never sees a socket implementation or a credential.
type pluginNetworkBroker struct {
	vault       forwardingVault
	routes      routeSource
	connections *appconnection.Manager
	dialer      net.Dialer
}

var _ appplugin.NetworkBroker = (*pluginNetworkBroker)(nil)

func newPluginNetworkBroker(vault *appvault.Service, routes routeSource,
	connections *appconnection.Manager) *pluginNetworkBroker {
	return &pluginNetworkBroker{
		vault:       vault,
		routes:      routes,
		connections: connections,
		dialer:      net.Dialer{Timeout: appplugin.DefaultNetworkDialTimeout},
	}
}

// Resolve performs one host lookup. The caller pins the returned addresses,
// so a later answer cannot redirect an already-authorized dial (DNS
// rebinding).
func (b *pluginNetworkBroker) Resolve(ctx context.Context, host string,
	policy appplugin.DNSPolicy) ([]netip.Addr, error) {
	if policy != appplugin.DNSPolicyAddress {
		return nil, appplugin.ErrExternalUnsupported
	}
	if b == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]bool, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out, nil
}

// DialPinned connects to one literal address. The address is never resolved a
// second time; the service passes the address it checked.
func (b *pluginNetworkBroker) DialPinned(ctx context.Context,
	target appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	if b == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	if !target.Address.IsValid() {
		return nil, errors.New("plugin network: dial address is not a pinned IP")
	}
	conn, err := b.dialer.DialContext(ctx, "tcp", target.Address.String())
	if err != nil {
		return nil, err
	}
	return b.wrapSocket(ctx, conn, target, nil)
}

// DialViaHost opens a direct-tcpip channel through the named SSH Host. Jump
// chains, proxy configuration, host-key trust, and authentication all come
// from the shared connection Manager; the guest never implements SSH.
func (b *pluginNetworkBroker) DialViaHost(ctx context.Context,
	target appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	if b == nil || b.vault == nil || b.connections == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	host, err := b.vault.Get(ctx, model.HostID(target.ViaHost))
	if err != nil {
		return nil, err
	}
	var route appconnection.Route
	if b.routes != nil {
		route, err = b.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, err
		}
	}
	lease, err := b.connections.Acquire(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		// A plugin channel shares the transport with other channel leases but
		// never with a Session shell, and closing it must not disturb the
		// user's own connection.
		LeaseKind: appconnection.LeaseChannel,
		Dedicated: true,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, pluginConnectionError(err)
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, pluginConnectionError(err)
	}
	conn, err := forwarding.DialContext(ctx, "tcp", target.Address.String())
	if err != nil {
		_ = lease.Discard()
		return nil, pluginConnectionError(err)
	}
	return b.wrapSocket(ctx, conn, target, func() { _ = lease.Release() })
}

// UpgradeTLS performs the single in-band upgrade. The policy, including SNI,
// ALPN, CA roots, and hostname verification, is supplied by the service from
// the granted scope; the guest cannot name a certificate or skip a check.
func (b *pluginNetworkBroker) UpgradeTLS(ctx context.Context,
	socket appplugin.ExternalSocket, policy appplugin.TLSClientPolicy) (appplugin.ExternalSocket, error) {
	if b == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	conn, ok := socket.(net.Conn)
	if !ok {
		return nil, appplugin.ErrExternalUnsupported
	}
	config, err := pluginTLSConfig(policy)
	if err != nil {
		return nil, err
	}
	secured := tls.Client(conn, config)
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", appplugin.ErrExternalTrustRejected, err)
	}
	return &pluginExternalSocket{conn: secured}, nil
}

func (b *pluginNetworkBroker) wrapSocket(ctx context.Context, conn net.Conn,
	target appplugin.NetworkDialTarget, release func()) (appplugin.ExternalSocket, error) {
	base := &pluginExternalSocket{conn: conn, release: release}
	if target.Protocol != domainplugin.NetworkProtocolTLS {
		return base, nil
	}
	config, err := pluginTLSConfig(target.TLS)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	secured := tls.Client(base, config)
	if err := secured.HandshakeContext(ctx); err != nil {
		_ = base.Close()
		return nil, fmt.Errorf("%w: %v", appplugin.ErrExternalTrustRejected, err)
	}
	return &pluginExternalSocket{conn: secured}, nil
}

// pluginTLSConfig builds the host-owned client policy. "verify" and "system"
// both use the platform trust store and normal hostname verification; "none"
// is honored only because the user's grant explicitly named it.
func pluginTLSConfig(policy appplugin.TLSClientPolicy) (*tls.Config, error) {
	serverName := strings.TrimSpace(policy.ServerName)
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}
	switch policy.Mode {
	case "", domainplugin.TLSPolicyVerify, domainplugin.TLSPolicySystem:
		// RootCAs nil means the platform trust store. Verification is never
		// disabled implicitly.
	case domainplugin.TLSPolicyNone:
		config.InsecureSkipVerify = true
	default:
		return nil, fmt.Errorf("plugin network: unsupported TLS policy %q", policy.Mode)
	}
	if len(policy.ALPN) > 0 {
		config.NextProtos = append([]string(nil), policy.ALPN...)
	}
	return config, nil
}

// pluginExternalSocket adapts one host-owned stream to the service interface.
// Closing the socket releases the SSH channel lease, if any.
type pluginExternalSocket struct {
	conn    net.Conn
	release func()
	once    sync.Once
}

func (s *pluginExternalSocket) Read(buffer []byte) (int, error) { return s.conn.Read(buffer) }
func (s *pluginExternalSocket) Write(data []byte) (int, error)  { return s.conn.Write(data) }
func (s *pluginExternalSocket) SetDeadline(t time.Time) error   { return s.conn.SetDeadline(t) }

func (s *pluginExternalSocket) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *pluginExternalSocket) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *pluginExternalSocket) SetReadDeadline(t time.Time) error  { return s.conn.SetReadDeadline(t) }
func (s *pluginExternalSocket) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }

func (s *pluginExternalSocket) CloseWrite() error {
	type closeWriter interface{ CloseWrite() error }
	if writer, ok := s.conn.(closeWriter); ok {
		return writer.CloseWrite()
	}
	return appplugin.ErrExternalUnsupported
}

func (s *pluginExternalSocket) CloseRead() error {
	type closeReader interface{ CloseRead() error }
	if reader, ok := s.conn.(closeReader); ok {
		return reader.CloseRead()
	}
	return appplugin.ErrExternalUnsupported
}

func (s *pluginExternalSocket) Close() error {
	err := s.conn.Close()
	s.once.Do(func() {
		if s.release != nil {
			s.release()
		}
	})
	return err
}

// pluginConnectionError maps a connection-layer trust failure onto the
// external-access vocabulary so the service returns a denial, not a fault.
func pluginConnectionError(err error) error {
	if errors.Is(err, appconnection.ErrForwardingUnavailable) {
		return fmt.Errorf("%w: %v", appplugin.ErrExternalUnsupported, err)
	}
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		switch typed.Kind {
		case appconnection.KindHostKey, appconnection.KindAuth:
			return fmt.Errorf("%w: %v", appplugin.ErrExternalTrustRejected, err)
		}
	}
	return err
}

// pluginKnownHostStore is the narrow Known Host persistence the trust adapter
// needs from the Vault database.
type pluginKnownHostStore interface {
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	UpsertKnownHost(ctx context.Context, record domainconn.KnownHost) error
	DeleteKnownHost(ctx context.Context, id string) error
}

// pluginTrustPins combines the existing Known Host store (SSH host keys) with
// a host-owned JSON pin file (TLS certificates and custom fingerprints). The
// plugin only supplies material; the host computes fingerprints and decides.
type pluginTrustPins struct {
	knownHosts pluginKnownHostStore
	vault      forwardingVault
	path       string

	mu      sync.Mutex
	loaded  bool
	records map[string]appplugin.TrustRecord
}

var _ appplugin.TrustPinStore = (*pluginTrustPins)(nil)

func newPluginTrustPins(knownHosts pluginKnownHostStore, vault *appvault.Service,
	path string) *pluginTrustPins {
	return &pluginTrustPins{knownHosts: knownHosts, vault: vault, path: path}
}

// Pins returns every trust record for one Host and kind. SSH host keys come
// from the same Known Hosts the terminal uses, so a decision made in either
// surface is honored by both.
func (s *pluginTrustPins) Pins(ctx context.Context, hostID string,
	kind appplugin.TrustKind) ([]appplugin.TrustRecord, error) {
	if kind == appplugin.TrustKindSSHHostKey {
		return s.sshPins(ctx, hostID)
	}
	records, err := s.fileRecords()
	if err != nil {
		return nil, err
	}
	out := make([]appplugin.TrustRecord, 0, len(records))
	for _, record := range records {
		if record.HostID == hostID && record.Kind == kind {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *pluginTrustPins) sshPins(ctx context.Context, hostID string) ([]appplugin.TrustRecord, error) {
	if s == nil || s.knownHosts == nil || s.vault == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	host, err := s.vault.Get(ctx, model.HostID(hostID))
	if err != nil {
		return nil, err
	}
	records, err := s.knownHosts.ListKnownHosts(ctx)
	if err != nil {
		return nil, err
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	out := make([]appplugin.TrustRecord, 0, len(records))
	for _, record := range records {
		if !strings.EqualFold(record.Hostname, host.Hostname) || record.Port != port {
			continue
		}
		out = append(out, appplugin.TrustRecord{
			HostID:      hostID,
			Kind:        appplugin.TrustKindSSHHostKey,
			KeyType:     record.KeyType,
			Fingerprint: record.Fingerprint,
			UpdatedAt:   record.LastSeen,
		})
	}
	return out, nil
}

// Save records accepted material. SSH keys are written to the Known Host
// store; other kinds are appended to the host-owned pin file.
func (s *pluginTrustPins) Save(ctx context.Context, record appplugin.TrustRecord) error {
	if record.Kind == appplugin.TrustKindSSHHostKey {
		return s.saveSSH(ctx, record)
	}
	records, err := s.fileRecords()
	if err != nil {
		return err
	}
	records[pluginTrustKey(record)] = record
	return s.persist(records)
}

func (s *pluginTrustPins) saveSSH(ctx context.Context, record appplugin.TrustRecord) error {
	if s == nil || s.knownHosts == nil || s.vault == nil {
		return appplugin.ErrExternalUnavailable
	}
	host, err := s.vault.Get(ctx, model.HostID(record.HostID))
	if err != nil {
		return err
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	saved := domainconn.NewKnownHost(pluginTrustID(), domainconn.Key{
		Hostname:    host.Hostname,
		Port:        port,
		KeyType:     record.KeyType,
		Fingerprint: record.Fingerprint,
	}, orNow(record.UpdatedAt))
	return s.knownHosts.UpsertKnownHost(ctx, saved)
}

// Delete removes one saved trust decision. It is the rollback path for a
// revocation that raced a confirmation, so it must not touch any Host.
func (s *pluginTrustPins) Delete(ctx context.Context, record appplugin.TrustRecord) error {
	if record.Kind == appplugin.TrustKindSSHHostKey {
		return s.deleteSSH(ctx, record)
	}
	if s == nil || s.path == "" {
		return appplugin.ErrExternalUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	delete(s.records, pluginTrustKey(record))
	return s.writeLocked()
}

func (s *pluginTrustPins) deleteSSH(ctx context.Context, record appplugin.TrustRecord) error {
	if s == nil || s.knownHosts == nil || s.vault == nil {
		return appplugin.ErrExternalUnavailable
	}
	host, err := s.vault.Get(ctx, model.HostID(record.HostID))
	if err != nil {
		return err
	}
	records, err := s.knownHosts.ListKnownHosts(ctx)
	if err != nil {
		return err
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	for _, known := range records {
		if !strings.EqualFold(known.Hostname, host.Hostname) || known.Port != port ||
			known.KeyType != record.KeyType || known.Fingerprint != record.Fingerprint {
			continue
		}
		if err := s.knownHosts.DeleteKnownHost(ctx, known.ID); err != nil &&
			!errors.Is(err, appvault.ErrNotFound) {
			return err
		}
	}
	return nil
}

func orNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now()
	}
	return value
}

func pluginTrustKey(record appplugin.TrustRecord) string {
	return record.HostID + "\x00" + string(record.Kind) + "\x00" + record.Fingerprint
}

func pluginTrustID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func (s *pluginTrustPins) fileRecords() (map[string]appplugin.TrustRecord, error) {
	if s == nil || s.path == "" {
		return nil, appplugin.ErrExternalUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	out := make(map[string]appplugin.TrustRecord, len(s.records))
	for key, record := range s.records {
		out[key] = record
	}
	return out, nil
}

func (s *pluginTrustPins) loadLocked() error {
	if s.loaded {
		return nil
	}
	s.records = make(map[string]appplugin.TrustRecord)
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.loaded = true
		return nil
	case err != nil:
		return err
	}
	var stored []appplugin.TrustRecord
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("plugin trust pins: %w", err)
	}
	for _, record := range stored {
		s.records[pluginTrustKey(record)] = record
	}
	s.loaded = true
	return nil
}

func (s *pluginTrustPins) persist(records map[string]appplugin.TrustRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Merge rather than replace so two concurrent confirmations cannot drop
	// each other's pin.
	for key, record := range records {
		s.records[key] = record
	}
	return s.writeLocked()
}

// writeLocked rewrites the pin file from the in-memory set. The caller holds
// s.mu.
func (s *pluginTrustPins) writeLocked() error {
	payload := make([]appplugin.TrustRecord, 0, len(s.records))
	for _, record := range s.records {
		payload = append(payload, record)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".plugin-trust-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	// Flush to disk before the rename so a crash cannot leave a truncated pin
	// file in place of a valid one.
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// pluginTrustConfirmer asks the host user about unknown SSH host keys through
// the same modal the terminal uses. A plugin cannot answer it. Other material
// kinds have no host confirmation surface yet and stay untrusted.
type pluginTrustConfirmer struct {
	prompt *sessionui.TrustPrompt
	vault  forwardingVault
}

var _ appplugin.TrustConfirmer = (*pluginTrustConfirmer)(nil)

func newPluginTrustConfirmer(prompt *sessionui.TrustPrompt,
	vault forwardingVault) *pluginTrustConfirmer {
	return &pluginTrustConfirmer{prompt: prompt, vault: vault}
}

func (c *pluginTrustConfirmer) Confirm(ctx context.Context,
	request appplugin.TrustRequest) (bool, error) {
	if c == nil || c.prompt == nil || c.vault == nil ||
		request.Kind != appplugin.TrustKindSSHHostKey {
		return false, nil
	}
	host, err := c.vault.Get(ctx, model.HostID(request.HostID))
	if err != nil {
		return false, err
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	decision, err := c.prompt.Confirm(ctx, domainconn.Key{
		Hostname:    host.Hostname,
		Port:        port,
		KeyType:     request.KeyType,
		Fingerprint: request.Fingerprint,
		PublicKey:   domainconn.PublicKey(request.KeyType, request.Material),
		Status:      domainconn.StatusUnknown,
	})
	if err != nil {
		return false, err
	}
	return decision.Trust, nil
}

// pluginAuthBroker is the host-owned authentication engine exposed to
// plugins. Credential resolution happens here; the plugin receives a status
// and an opaque, empty lease reference, never plaintext.
type pluginAuthBroker struct {
	vault *appvault.Service

	mu       sync.Mutex
	sequence uint64
	flows    map[string]*pluginAuthFlow
}

type pluginAuthFlow struct {
	hostID string
	method appplugin.AuthMethod
	ready  bool
}

var _ appplugin.AuthBroker = (*pluginAuthBroker)(nil)

func newPluginAuthBroker(vault *appvault.Service) *pluginAuthBroker {
	return &pluginAuthBroker{vault: vault, flows: make(map[string]*pluginAuthFlow)}
}

func (b *pluginAuthBroker) Begin(ctx context.Context,
	request appplugin.AuthBeginRequest) (appplugin.AuthChallenge, error) {
	if b == nil || b.vault == nil {
		return appplugin.AuthChallenge{}, appplugin.ErrExternalUnavailable
	}
	host, err := b.vault.Get(ctx, model.HostID(request.HostID))
	if err != nil {
		return appplugin.AuthChallenge{}, err
	}
	switch request.Method {
	case appplugin.AuthMethodNone, appplugin.AuthMethodPassword,
		appplugin.AuthMethodPublicKey, appplugin.AuthMethodAgent:
	case appplugin.AuthMethodKeyboardInteractive, appplugin.AuthMethodChallenge:
		// Interactive prompts exceed one guest service call and are owned by
		// the connection flow rather than a plugin-driven exchange.
		return appplugin.AuthChallenge{}, fmt.Errorf(
			"interactive authentication is not available to plugin-driven flows")
	default:
		return appplugin.AuthChallenge{}, fmt.Errorf(
			"authentication method %q is not supported", request.Method)
	}
	resolved, err := b.vault.ResolveAuth(ctx, host.ID)
	if err != nil {
		return appplugin.AuthChallenge{}, err
	}
	// The resolved password, private key, and passphrase stay in this stack
	// frame. They are never returned, stored, or logged.
	ready := resolved.Password != "" || resolved.Key != nil || resolved.Selection.UseSSHAgent
	if request.Method == appplugin.AuthMethodNone {
		ready = true
	}
	b.mu.Lock()
	b.sequence++
	id := fmt.Sprintf("auth-%d-%d", b.sequence, time.Now().UnixNano())
	b.flows[id] = &pluginAuthFlow{hostID: request.HostID, method: request.Method, ready: ready}
	b.mu.Unlock()
	return appplugin.AuthChallenge{
		ID:         id,
		HostID:     request.HostID,
		Method:     request.Method,
		Prompt:     "Authenticate " + host.Label + " on the host",
		CanRespond: false,
	}, nil
}

func (b *pluginAuthBroker) Respond(ctx context.Context, challengeID,
	answer string) (appplugin.AuthResult, error) {
	flow, err := b.flow(challengeID, true)
	if err != nil {
		return appplugin.AuthResult{}, err
	}
	// answer is an opaque response to a host prompt. It is not a credential
	// and is intentionally not used to select or supply credentials.
	_ = answer
	status := appplugin.AuthStatusFailed
	if flow.ready {
		status = appplugin.AuthStatusSucceeded
	}
	return appplugin.AuthResult{ChallengeID: challengeID, Status: status}, nil
}

func (b *pluginAuthBroker) Cancel(ctx context.Context, challengeID string) error {
	if _, err := b.flow(challengeID, true); err != nil {
		return err
	}
	return nil
}

func (b *pluginAuthBroker) flow(challengeID string, remove bool) (*pluginAuthFlow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	flow, ok := b.flows[challengeID]
	if !ok {
		return nil, fmt.Errorf("authentication challenge does not exist")
	}
	if remove {
		delete(b.flows, challengeID)
	}
	return flow, nil
}

// pluginVaultFields exposes the Vault's field classes to the external-access
// service. The class is a property of the field, not a plugin request: only a
// Host password is guest-deliverable; passphrases, private keys, and proxy
// passwords never leave the host.
type pluginVaultFields struct {
	vault *appvault.Service
}

var _ appplugin.VaultFieldSource = (*pluginVaultFields)(nil)

func newPluginVaultFields(vault *appvault.Service) *pluginVaultFields {
	return &pluginVaultFields{vault: vault}
}

func (s *pluginVaultFields) Descriptors(ctx context.Context,
	hostID string) ([]appplugin.VaultFieldDescriptor, error) {
	if s == nil || s.vault == nil {
		return nil, appplugin.ErrExternalUnavailable
	}
	host, err := s.vault.Get(ctx, model.HostID(hostID))
	if err != nil {
		return nil, err
	}
	descriptors := []appplugin.VaultFieldDescriptor{
		{
			HostID: hostID, Field: "password", Label: "Password",
			Kind: "password", HostExecuted: false, GuestDeliverable: true,
		},
		{
			HostID: hostID, Field: "passphrase", Label: "Key passphrase",
			Kind: "passphrase", HostExecuted: true,
		},
		{
			HostID: hostID, Field: "private_key", Label: "Private key",
			Kind: "key", HostExecuted: true,
		},
	}
	if host.Proxy != nil || host.ProxyProfileID != "" {
		descriptors = append(descriptors, appplugin.VaultFieldDescriptor{
			HostID: hostID, Field: "proxy_password", Label: "Proxy password",
			Kind: "proxy-password", HostExecuted: true,
		})
	}
	return descriptors, nil
}

func (s *pluginVaultFields) Value(ctx context.Context, hostID,
	field string) (string, error) {
	if s == nil || s.vault == nil {
		return "", appplugin.ErrExternalUnavailable
	}
	// Defense in depth: a host-executed field is denied before this port is
	// called, and this port refuses it as well.
	if field != appvault.FieldPassword {
		return "", fmt.Errorf("vault field %q is host-executed", field)
	}
	return s.vault.Secret(ctx, model.HostID(hostID), field)
}
