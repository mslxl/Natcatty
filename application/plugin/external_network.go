package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// networkReadWait bounds one synchronous read when the caller has no shorter
// deadline. Returning would-block keeps the guest in control instead of
// forcing the runtime's call deadline to kill the call.
const networkReadWait = time.Second

// NetworkDialSpec is one external dial request. Every field is validated
// against the exact granted endpoint scope before any name is resolved.
type NetworkDialSpec struct {
	Host     string
	Port     int
	Protocol string
	TLS      string
	DNS      string
	ViaHost  string
	Timeout  time.Duration
}

// NetworkDialResult is the non-secret projection of one established
// connection.
type NetworkDialResult struct {
	Handle   Handle
	Endpoint string
	Protocol string
	TLS      string
	Resolved string
	ViaHost  string
}

// NetworkReadResult is one bounded read. WouldBlock reports that no bytes
// arrived before the read wait; the guest retries.
type NetworkReadResult struct {
	Data       []byte
	EOF        bool
	WouldBlock bool
	Credit     int
}

// NetworkWriteResult reports the bytes written and the current credit.
type NetworkWriteResult struct {
	Written int
	Credit  int
}

// NetworkTLSUpgradeResult reports the host-owned policy applied to an
// in-band upgrade.
type NetworkTLSUpgradeResult struct {
	Mode       string
	ServerName string
}

// networkConnection is one host-owned socket. Reads and writes are
// synchronous: a background pump would race an in-band TLS upgrade and could
// consume encrypted bytes as plaintext. The credit window bounds each
// direction; the handle table bounds the connection's lifetime.
type networkConnection struct {
	handle  Handle
	scope   domainplugin.Scope
	socket  ExternalSocket
	viaHost string
	tlsDone bool
	// upgrading reserves the single TLS upgrade while the broker handshake
	// runs without conn.mu, so a revocation can close the connection and
	// cancel the handshake instead of blocking behind it.
	upgrading bool

	closed      bool
	readClosed  bool
	writeClosed bool
	readCredit  int
	writeCredit int

	chunkLimit  int
	idleTimeout time.Duration
	lastActive  time.Time
	now         func() time.Time

	mu sync.Mutex
}

// NetworkDial validates the exact endpoint scope, resolves the name once,
// pins the resolved address, and connects. The returned handle is bound to the
// fiber and the authority epoch.
func (s *ServiceSession) NetworkDial(ctx context.Context, spec NetworkDialSpec) (NetworkDialResult, error) {
	if s == nil {
		return NetworkDialResult{}, &ServiceError{Service: ServiceNetwork, Method: "dial",
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	host := canonicalNetworkHost(spec.Host)
	protocol := strings.ToLower(strings.TrimSpace(spec.Protocol))
	tlsPolicy := strings.ToLower(strings.TrimSpace(spec.TLS))
	if err := validateNetworkDial(host, spec.Port, protocol, tlsPolicy, spec.DNS); err != nil {
		return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
			domainplugin.PermissionNetworkDial, domainplugin.Scope{}, err)
	}
	if protocol == domainplugin.NetworkProtocolTLS && tlsPolicy == "" {
		// An unqualified TLS dial applies host verification. The exact scope
		// carries the policy that is actually in force, so a grant never
		// silently weakens verification.
		tlsPolicy = domainplugin.TLSPolicyVerify
	}
	scope := domainplugin.Scope{
		Kind:     domainplugin.ScopeNetwork,
		Host:     host,
		Ports:    []int{spec.Port},
		Protocol: protocol,
		TLS:      tlsPolicy,
	}
	if err := s.checkNetwork(ServiceNetwork, "dial", domainplugin.PermissionNetworkDial, scope); err != nil {
		return NetworkDialResult{}, err
	}

	viaHost := strings.TrimSpace(spec.ViaHost)
	if viaHost != "" {
		if err := s.checkNetworkViaHost(viaHost); err != nil {
			return NetworkDialResult{}, err
		}
	}
	if s.host.network == nil {
		return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
			domainplugin.PermissionNetworkDial, scope, externalUnavailable("network broker"))
	}

	timeout := spec.Timeout
	if timeout <= 0 || timeout > s.host.limits.NetworkDialTimeout {
		timeout = s.host.limits.NetworkDialTimeout
	}
	// validateNetworkDial already rejected every other DNS policy.
	dnsPolicy := DNSPolicyAddress

	// Reserve the connection slot before the slow resolve/dial so concurrent
	// calls cannot exceed the quota.
	epoch, err := s.reserveNetworkSlot()
	if err != nil {
		return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
			domainplugin.PermissionNetworkDial, scope, err)
	}

	// The dial is host work, not guest compute: it gets the method's own
	// timeout and stops on unload or revocation.
	workCtx, cancelWork := s.workContext(ctx)
	defer cancelWork()
	dialCtx, cancel := context.WithTimeout(workCtx, timeout)
	defer cancel()
	addresses, resolveErr := s.resolvePinned(dialCtx, host, dnsPolicy)
	if resolveErr != nil {
		s.releaseNetworkSlot()
		if revoked := s.networkCapabilityRevoked(epoch, scope); revoked != nil {
			return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
				domainplugin.PermissionNetworkDial, scope,
				revokedServiceError(ServiceNetwork, "dial", s.subject,
					domainplugin.PermissionNetworkDial, scope,
					"the plugin capability was revoked while dialing"))
		}
		code := ServiceCodeFault
		if errors.Is(resolveErr, errNoAddresses) {
			code = ServiceCodeNotFound
		}
		err := workspaceErrorf(code, "resolve %s: %v", host, resolveErr)
		return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
			domainplugin.PermissionNetworkDial, scope, err)
	}

	var socket ExternalSocket
	var dialErr error
	var resolved netip.Addr
	for _, address := range addresses {
		target := NetworkDialTarget{
			Host:     host,
			Address:  netip.AddrPortFrom(address, uint16(spec.Port)),
			Protocol: protocol,
			TLS: TLSClientPolicy{
				Mode:       tlsPolicy,
				ServerName: host,
			},
			ViaHost: viaHost,
			Timeout: timeout,
		}
		if viaHost != "" {
			socket, dialErr = s.host.network.DialViaHost(dialCtx, target)
		} else {
			socket, dialErr = s.host.network.DialPinned(dialCtx, target)
		}
		if dialErr == nil {
			resolved = address
			break
		}
	}
	if dialErr != nil {
		s.releaseNetworkSlot()
		if revoked := s.networkCapabilityRevoked(epoch, scope); revoked != nil {
			return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
				domainplugin.PermissionNetworkDial, scope,
				revokedServiceError(ServiceNetwork, "dial", s.subject,
					domainplugin.PermissionNetworkDial, scope,
					"the plugin capability was revoked while dialing"))
		}
		return NetworkDialResult{}, s.recordNetworkDialFailure(scope, dialErr)
	}

	conn := &networkConnection{
		scope:       scope,
		socket:      socket,
		viaHost:     viaHost,
		tlsDone:     protocol == domainplugin.NetworkProtocolTLS,
		readCredit:  s.host.limits.NetworkCreditBytes,
		writeCredit: s.host.limits.NetworkCreditBytes,
		chunkLimit:  s.host.limits.NetworkChunkBytes,
		idleTimeout: s.host.limits.NetworkIdleTimeout,
		lastActive:  s.host.now(),
		now:         s.host.now,
	}
	handle, err := s.commitNetworkConnection(epoch, conn)
	if err != nil {
		_ = socket.Close()
		// The broker may have returned a usable socket while a revocation
		// landed; the epoch mismatch is a capability revocation, not a generic
		// fault. Return the recorded typed error so direct callers and the
		// guest envelope both see the spec's revoked status.
		if revoked := s.networkCapabilityRevoked(epoch, scope); revoked != nil {
			return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
				domainplugin.PermissionNetworkDial, scope,
				revokedServiceError(ServiceNetwork, "dial", s.subject,
					domainplugin.PermissionNetworkDial, scope,
					"the plugin capability was revoked while dialing"))
		}
		return NetworkDialResult{}, s.recordFailure(ServiceNetwork, "dial",
			domainplugin.PermissionNetworkDial, scope, err)
	}
	conn.handle = handle
	s.recordSuccess(ServiceNetwork, "dial", domainplugin.PermissionNetworkDial, scope)
	return NetworkDialResult{
		Handle:   handle,
		Endpoint: net.JoinHostPort(host, strconv.Itoa(spec.Port)),
		Protocol: protocol,
		TLS:      tlsPolicy,
		Resolved: resolved.String(),
		ViaHost:  viaHost,
	}, nil
}

func validateNetworkDial(host string, port int, protocol, tlsPolicy, dns string) error {
	if host == "" {
		return workspaceInvalid("network.dial requires a host")
	}
	if strings.ContainsAny(host, " \t\r\n\x00*/?\\") {
		return workspaceInvalid("network.dial host %q is not an exact endpoint name", host)
	}
	if port < 1 || port > 65535 {
		return workspaceInvalid("network.dial port %d is outside 1-65535", port)
	}
	switch protocol {
	case domainplugin.NetworkProtocolTCP:
		if tlsPolicy != "" && tlsPolicy != domainplugin.TLSPolicyNone {
			return workspaceInvalid("tcp dial cannot declare TLS policy %q", tlsPolicy)
		}
	case domainplugin.NetworkProtocolTLS:
		switch tlsPolicy {
		case "":
			// Default the policy to verify; the grant must still match the
			// resolved scope exactly.
		case domainplugin.TLSPolicyVerify, domainplugin.TLSPolicySystem, domainplugin.TLSPolicyNone:
		default:
			return workspaceInvalid("tls dial requires a TLS policy of verify, system, or none")
		}
	default:
		return workspaceInvalid("network.dial protocol must be tcp or tls; UDP is not offered")
	}
	if policy := strings.ToLower(strings.TrimSpace(dns)); policy != "" && policy != string(DNSPolicyAddress) {
		return workspaceInvalid("network.dial dns policy %q is not available", dns)
	}
	return nil
}

func (s *ServiceSession) checkNetworkViaHost(viaHost string) error {
	host, ok, err := s.lookupHost(viaHost)
	if err != nil {
		return s.recordFailure(ServiceNetwork, "dial", domainplugin.PermissionHostsRead,
			domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: viaHost}, err)
	}
	if !ok {
		return s.recordFailure(ServiceNetwork, "dial", domainplugin.PermissionHostsRead,
			domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: viaHost},
			workspaceNotFound("via_host %s does not exist", viaHost))
	}
	if !strings.EqualFold(host.Protocol, "ssh") {
		return s.recordFailure(ServiceNetwork, "dial", domainplugin.PermissionHostsRead,
			hostScope(viaHost), workspaceInvalid("via_host must name an ssh Host"))
	}
	if _, err := s.checkHost(ServiceNetwork, "dial", domainplugin.PermissionHostsRead, host); err != nil {
		return err
	}
	return nil
}

var errNoAddresses = errors.New("no addresses")

func (s *ServiceSession) resolvePinned(ctx context.Context, host string,
	policy DNSPolicy) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	addresses, err := s.host.network.Resolve(ctx, host, policy)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errNoAddresses
	}
	return addresses, nil
}

func (s *ServiceSession) recordNetworkDialFailure(scope domainplugin.Scope, err error) error {
	code := ServiceCodeFault
	message := "external dial failed"
	switch {
	case errors.Is(err, ErrExternalTrustRejected):
		// Trust failures are final: the guest cannot retry with another
		// policy or material.
		code = ServiceCodeDenied
		message = "external TLS trust verification rejected the endpoint"
	case errors.Is(err, ErrExternalUnsupported):
		code = ServiceCodeInvalid
		message = "external transport is not supported"
	case errors.Is(err, context.DeadlineExceeded):
		code = ServiceCodeFault
		message = "external dial timed out"
	default:
		message = fmt.Sprintf("external dial failed: %v", err)
	}
	return s.recordFailure(ServiceNetwork, "dial", domainplugin.PermissionNetworkDial,
		scope, workspaceErrorf(code, "%s", message))
}

// reserveNetworkSlot counts a connection against the fiber quota before the
// slow dial. The returned epoch proves no revocation happened while dialing.
func (s *ServiceSession) reserveNetworkSlot() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, workspaceInvalid("the plugin instance is closed")
	}
	if len(s.network)+s.pendingNetwork >= s.host.limits.NetworkConnections {
		return 0, workspaceLimit("external connection limit of %d reached",
			s.host.limits.NetworkConnections)
	}
	s.pendingNetwork++
	return s.handleEpoch, nil
}

func (s *ServiceSession) releaseNetworkSlot() {
	s.mu.Lock()
	if s.pendingNetwork > 0 {
		s.pendingNetwork--
	}
	s.mu.Unlock()
}

// commitNetworkConnection inserts a finished connection only when the
// authority epoch captured before dialing is still current. A revocation that
// races the dial closes the socket instead of leaking a live capability.
func (s *ServiceSession) commitNetworkConnection(epoch uint64,
	conn *networkConnection) (Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingNetwork > 0 {
		s.pendingNetwork--
	}
	if s.closed || s.handleEpoch != epoch {
		return 0, workspaceInvalid("the plugin instance was revoked while dialing")
	}
	if s.handles == nil {
		return 0, &workspaceFailure{Code: ServiceCodeFault,
			Problem: "handle table is not bound", Cause: ErrServiceUnavailable}
	}
	handle, err := s.handles.Issue(conn)
	if err != nil {
		return 0, err
	}
	s.network[handle] = conn
	return handle, nil
}

// NetworkRead reads at most maxBytes from one connection.
func (s *ServiceSession) NetworkRead(ctx context.Context, handle Handle,
	maxBytes int) (NetworkReadResult, error) {
	conn, err := s.liveNetworkConnection(handle)
	if err != nil {
		return NetworkReadResult{}, s.recordFailure(ServiceNetwork, "read",
			domainplugin.PermissionNetworkDial, domainplugin.Scope{}, err)
	}
	if maxBytes <= 0 {
		maxBytes = conn.chunkLimit
	}
	if maxBytes > conn.chunkLimit {
		err := workspaceLimit("network read accepts at most %d bytes", conn.chunkLimit)
		return NetworkReadResult{}, s.recordFailure(ServiceNetwork, "read",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	result, err := conn.read(ctx, maxBytes)
	if err != nil {
		if errors.Is(err, errNetworkExpired) {
			s.expireConnectionLeases(handle)
		}
		return NetworkReadResult{}, s.recordFailure(ServiceNetwork, "read",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	return result, nil
}

func (c *networkConnection) read(ctx context.Context, maxBytes int) (NetworkReadResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return NetworkReadResult{}, workspaceInvalid("the connection is closed")
	}
	if c.upgrading {
		return NetworkReadResult{}, workspaceConflict("the connection is upgrading TLS")
	}
	if c.expiredLocked() {
		c.closeLocked()
		return NetworkReadResult{}, errNetworkExpiredFailure()
	}
	if c.readClosed {
		return NetworkReadResult{EOF: true, Credit: c.readCredit}, nil
	}
	if maxBytes > c.readCredit {
		return NetworkReadResult{}, workspaceLimit("network read exceeds the %d-byte credit window",
			c.readCredit)
	}
	deadline := c.now().Add(networkReadWait)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	_ = c.socket.SetDeadline(deadline)
	buffer := make([]byte, maxBytes)
	n, err := c.socket.Read(buffer)
	if n > 0 {
		c.lastActive = c.now()
		data := buffer[:n]
		return NetworkReadResult{Data: data, Credit: c.readCredit}, nil
	}
	switch {
	case err == nil:
		return NetworkReadResult{WouldBlock: true, Credit: c.readCredit}, nil
	case errors.Is(err, io.EOF):
		c.readClosed = true
		return NetworkReadResult{EOF: true, Credit: c.readCredit}, nil
	case isTimeout(err):
		return NetworkReadResult{WouldBlock: true, Credit: c.readCredit}, nil
	default:
		return NetworkReadResult{}, fmt.Errorf("network read: %w", err)
	}
}

// NetworkWrite writes one bounded chunk to a connection.
func (s *ServiceSession) NetworkWrite(ctx context.Context, handle Handle,
	data []byte) (NetworkWriteResult, error) {
	conn, err := s.liveNetworkConnection(handle)
	if err != nil {
		return NetworkWriteResult{}, s.recordFailure(ServiceNetwork, "write",
			domainplugin.PermissionNetworkDial, domainplugin.Scope{}, err)
	}
	if len(data) > conn.chunkLimit {
		err := workspaceLimit("network write accepts at most %d bytes", conn.chunkLimit)
		return NetworkWriteResult{}, s.recordFailure(ServiceNetwork, "write",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	result, err := conn.write(ctx, data)
	if err != nil {
		if errors.Is(err, errNetworkExpired) {
			s.expireConnectionLeases(handle)
		}
		return NetworkWriteResult{}, s.recordFailure(ServiceNetwork, "write",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	return result, nil
}

func (c *networkConnection) write(ctx context.Context, data []byte) (NetworkWriteResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return NetworkWriteResult{}, workspaceInvalid("the connection is closed")
	}
	if c.upgrading {
		return NetworkWriteResult{}, workspaceConflict("the connection is upgrading TLS")
	}
	if c.expiredLocked() {
		c.closeLocked()
		return NetworkWriteResult{}, errNetworkExpiredFailure()
	}
	if c.writeClosed {
		return NetworkWriteResult{}, workspaceInvalid("the write direction is closed")
	}
	if len(data) > c.writeCredit {
		return NetworkWriteResult{}, workspaceLimit("network write exceeds the %d-byte credit window",
			c.writeCredit)
	}
	deadline := c.now().Add(networkReadWait)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	_ = c.socket.SetDeadline(deadline)
	written := 0
	for written < len(data) {
		n, err := c.socket.Write(data[written:])
		written += n
		if err != nil {
			if written > 0 {
				c.lastActive = c.now()
			}
			return NetworkWriteResult{}, fmt.Errorf("network write after %d bytes: %w", written, err)
		}
		if n == 0 {
			return NetworkWriteResult{}, errors.New("network write made no progress")
		}
	}
	c.lastActive = c.now()
	return NetworkWriteResult{Written: written, Credit: c.writeCredit}, nil
}

// NetworkShutdown half-closes one direction. Write half-close leaves reads
// open; read half-close leaves writes open.
func (s *ServiceSession) NetworkShutdown(handle Handle, direction string) error {
	conn, err := s.liveNetworkConnection(handle)
	if err != nil {
		return s.recordFailure(ServiceNetwork, "shutdown",
			domainplugin.PermissionNetworkDial, domainplugin.Scope{}, err)
	}
	if err := conn.shutdown(direction); err != nil {
		return s.recordFailure(ServiceNetwork, "shutdown",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	return nil
}

func (c *networkConnection) shutdown(direction string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return workspaceInvalid("the connection is closed")
	}
	if c.upgrading {
		return workspaceConflict("the connection is upgrading TLS")
	}
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "write":
		if c.writeClosed {
			return nil
		}
		if err := c.socket.CloseWrite(); err != nil {
			return externalSocketError("write half-close", err)
		}
		c.writeClosed = true
		return nil
	case "read":
		if c.readClosed {
			return nil
		}
		if err := c.socket.CloseRead(); err != nil {
			return externalSocketError("read half-close", err)
		}
		c.readClosed = true
		return nil
	default:
		return workspaceInvalid("shutdown direction must be read or write")
	}
}

// NetworkClose releases one connection and every lease bound to it. It is
// idempotent: closing an already released handle succeeds.
func (s *ServiceSession) NetworkClose(handle Handle) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	conn := s.network[handle]
	handles := s.handles
	if conn != nil {
		delete(s.network, handle)
	}
	s.mu.Unlock()
	if conn == nil {
		return nil
	}
	conn.close()
	if handles != nil {
		_ = handles.Release(handle)
	}
	s.expireConnectionLeases(handle)
	s.recordSuccess(ServiceNetwork, "close", domainplugin.PermissionNetworkDial, conn.scope)
	return nil
}

// NetworkTLSUpgrade performs the single in-band TLS upgrade allowed on a
// connection. The policy is host-owned: the granted endpoint supplies the
// server name and the host applies its verification policy.
func (s *ServiceSession) NetworkTLSUpgrade(ctx context.Context,
	handle Handle) (NetworkTLSUpgradeResult, error) {
	conn, err := s.liveNetworkConnection(handle)
	if err != nil {
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, domainplugin.Scope{}, err)
	}
	if s.host.network == nil {
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope, externalUnavailable("network broker"))
	}
	policy := TLSClientPolicy{Mode: domainplugin.TLSPolicyVerify, ServerName: conn.scope.Host}
	// The handshake is host work: it may outlive one guest compute slice, but
	// it must stop when the fiber unloads or the capability is revoked. The
	// epoch captured before the broker call is rechecked before any TLS state
	// is committed.
	epoch := s.handleEpochSnapshot()
	workCtx, cancelWork := s.workContext(ctx)
	defer cancelWork()
	socket, err := conn.beginTLSUpgrade()
	if err != nil {
		if errors.Is(err, errNetworkExpired) {
			s.expireConnectionLeases(handle)
		}
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope, err)
	}
	// The broker runs without conn.mu: revocation closes the connection and
	// cancels workCtx while the handshake is in flight, so it never blocks
	// behind the handshake.
	upgraded, upgradeErr := s.host.network.UpgradeTLS(workCtx, socket, policy)
	if revoked := s.networkCapabilityRevoked(epoch, conn.scope); revoked != nil {
		// The upgrade is abandoned before commit: discard the broker's socket,
		// fail the connection closed, and never report success.
		conn.abortTLSUpgrade()
		discardExternalSocket(upgraded)
		conn.close()
		s.expireConnectionLeases(handle)
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope,
			revokedServiceError(ServiceNetwork, "tls_upgrade", s.subject,
				domainplugin.PermissionNetworkDial, conn.scope,
				"the plugin capability was revoked during the TLS upgrade"))
	}
	if upgradeErr != nil {
		conn.abortTLSUpgrade()
		discardExternalSocket(upgraded)
		if errors.Is(upgradeErr, errNetworkExpired) {
			s.expireConnectionLeases(handle)
		}
		// A validation failure keeps its closed code: a second upgrade is a
		// conflict and a half-closed connection is invalid.
		var failure *workspaceFailure
		if errors.As(upgradeErr, &failure) {
			return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork,
				"tls_upgrade", domainplugin.PermissionNetworkDial, conn.scope, upgradeErr)
		}
		// The handshake is not independently framed. After a host-side
		// failure the stream state is unknown, so the connection fails closed
		// instead of letting the guest continue in plaintext or retry. Leases
		// bound to the dead connection die with it.
		conn.close()
		s.expireConnectionLeases(handle)
		code := ServiceCodeFault
		message := fmt.Sprintf("in-band TLS upgrade failed: %v", upgradeErr)
		if errors.Is(upgradeErr, ErrExternalTrustRejected) {
			code = ServiceCodeDenied
			message = "in-band TLS trust verification rejected the endpoint"
		}
		if errors.Is(upgradeErr, ErrExternalUnsupported) {
			code = ServiceCodeInvalid
			message = "in-band TLS upgrade is not supported"
		}
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope,
			workspaceErrorf(code, "%s", message))
	}
	result, commitErr := conn.finishTLSUpgrade(upgraded, policy)
	if commitErr != nil {
		discardExternalSocket(upgraded)
		conn.close()
		s.expireConnectionLeases(handle)
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope, commitErr)
	}
	// A revocation may also land between the pre-commit check and the commit;
	// the committed connection is closed and the call never reports success.
	if revoked := s.networkCapabilityRevoked(epoch, conn.scope); revoked != nil {
		conn.close()
		s.expireConnectionLeases(handle)
		return NetworkTLSUpgradeResult{}, s.recordFailure(ServiceNetwork, "tls_upgrade",
			domainplugin.PermissionNetworkDial, conn.scope,
			revokedServiceError(ServiceNetwork, "tls_upgrade", s.subject,
				domainplugin.PermissionNetworkDial, conn.scope,
				"the plugin capability was revoked during the TLS upgrade"))
	}
	s.recordSuccess(ServiceNetwork, "tls_upgrade", domainplugin.PermissionNetworkDial, conn.scope)
	return result, nil
}

// beginTLSUpgrade validates the connection and reserves the one in-band
// upgrade. The broker call runs after the lock is released so a revocation
// can close the connection and cancel the upgrade instead of waiting behind
// the handshake.
func (c *networkConnection) beginTLSUpgrade() (ExternalSocket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, workspaceInvalid("the connection is closed")
	}
	if c.expiredLocked() {
		c.closeLocked()
		return nil, errNetworkExpiredFailure()
	}
	if c.upgrading {
		return nil, workspaceConflict("the connection is already upgrading TLS")
	}
	if c.tlsDone {
		return nil, workspaceConflict("the connection already completed its one TLS upgrade")
	}
	if c.readClosed || c.writeClosed {
		return nil, workspaceInvalid("a half-closed connection cannot upgrade TLS")
	}
	c.upgrading = true
	return c.socket, nil
}

// finishTLSUpgrade commits the broker's socket. It refuses to commit an
// upgrade whose connection was closed by revocation while the handshake ran.
func (c *networkConnection) finishTLSUpgrade(upgraded ExternalSocket,
	policy TLSClientPolicy) (NetworkTLSUpgradeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.upgrading {
		return NetworkTLSUpgradeResult{}, workspaceInvalid("the TLS upgrade was canceled")
	}
	c.upgrading = false
	if c.closed {
		return NetworkTLSUpgradeResult{}, workspaceInvalid("the connection is closed")
	}
	c.socket = upgraded
	c.tlsDone = true
	c.lastActive = c.now()
	return NetworkTLSUpgradeResult{Mode: policy.Mode, ServerName: policy.ServerName}, nil
}

// abortTLSUpgrade releases the upgrade reservation after a failed or revoked
// handshake. The caller decides how to fail the connection.
func (c *networkConnection) abortTLSUpgrade() {
	c.mu.Lock()
	c.upgrading = false
	c.mu.Unlock()
}

// discardExternalSocket closes a broker socket that was never committed. A nil
// socket is ignored.
func discardExternalSocket(socket ExternalSocket) {
	if socket != nil {
		_ = socket.Close()
	}
}

// liveNetworkConnection resolves a handle and confirms the connection is
// still current. A released or revoked handle is a typed invalid error.
func (s *ServiceSession) liveNetworkConnection(handle Handle) (*networkConnection, error) {
	if s == nil {
		return nil, invalidNetworkHandle(handle)
	}
	s.mu.Lock()
	conn := s.network[handle]
	handles := s.handles
	closed := s.closed
	s.mu.Unlock()
	if closed || conn == nil || handles == nil {
		return nil, invalidNetworkHandle(handle)
	}
	value, err := handles.Resolve(handle)
	if err != nil || value != conn {
		return nil, invalidNetworkHandle(handle)
	}
	return conn, nil
}

func invalidNetworkHandle(handle Handle) error {
	return &ServiceError{
		Service: ServiceNetwork, Method: "handle", Code: ServiceCodeInvalid,
		Permission: domainplugin.PermissionNetworkDial,
		Message:    fmt.Sprintf("network handle %#x is invalid or revoked", uint64(handle)),
		Cause:      &InvalidHandleError{Handle: handle},
	}
}

// expireConnectionLeases clears every lease bound to a closed connection.
func (s *ServiceSession) expireConnectionLeases(connection Handle) {
	s.mu.Lock()
	var expired []*secretLease
	var handles []Handle
	for handle, lease := range s.leases {
		if lease.bound && lease.connection == connection {
			expired = append(expired, lease)
			handles = append(handles, handle)
			delete(s.leases, handle)
		}
	}
	table := s.handles
	s.mu.Unlock()
	for i, lease := range expired {
		lease.clear()
		if table != nil {
			_ = table.Release(handles[i])
		}
	}
}

func (c *networkConnection) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

func (c *networkConnection) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	_ = c.socket.Close()
}

func (c *networkConnection) expiredLocked() bool {
	return c.idleTimeout > 0 && c.now().Sub(c.lastActive) > c.idleTimeout
}

// live reports whether the connection can still carry a lease: it is open
// and has not passed its idle deadline. An expired connection is closed, so a
// connection-bound lease cannot outlive it.
func (c *networkConnection) live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	if c.expiredLocked() {
		c.closeLocked()
		return false
	}
	return true
}

// errNetworkExpired lets the service expire leases bound to a connection that
// the idle sweep closed.
var errNetworkExpired = errors.New("network connection expired")

func errNetworkExpiredFailure() error {
	return &workspaceFailure{Code: ServiceCodeInvalid,
		Problem: "the connection expired after idle timeout", Cause: errNetworkExpired}
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func externalSocketError(operation string, err error) error {
	if errors.Is(err, ErrExternalUnsupported) {
		return workspaceInvalid("%s is not supported by this transport", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func externalUnavailable(port string) error {
	return &workspaceFailure{Code: ServiceCodeFault,
		Problem: port + " is not configured", Cause: ErrExternalUnavailable}
}
