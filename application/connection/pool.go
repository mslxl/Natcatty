package connection

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pool behavior mirrors the original shared SSH transport registry:
// consumers borrow a lease, return it when done, and the last return parks
// the transport for reuse instead of tearing it down.
const (
	// DefaultIdleTTL parks an idle Connection for five minutes.
	DefaultIdleTTL = 5 * time.Minute
	// MaxIdleConnections bounds the number of parked transports. Active
	// connections never count toward or get evicted by this limit.
	MaxIdleConnections = 128
)

// KeepalivePolicy is one Connection's keepalive schedule. A zero Interval
// means the caller did not configure one, so the original 10 s / 3 fallback
// applies. A negative Interval disables keepalive.
type KeepalivePolicy struct {
	Interval time.Duration
	CountMax int
}

const (
	// DefaultKeepaliveInterval and DefaultKeepaliveCountMax are the global
	// terminal defaults.
	DefaultKeepaliveInterval = 30 * time.Second
	DefaultKeepaliveCountMax = 10
	// FallbackKeepaliveInterval and FallbackKeepaliveCountMax apply to callers
	// that omit a policy, matching resolveConnectionKeepalivePolicy.
	FallbackKeepaliveInterval = 10 * time.Second
	FallbackKeepaliveCountMax = 3
)

// DefaultKeepalive is the global terminal keepalive. The app supplies it;
// callers that omit Request.Keepalive get the fallback instead.
var DefaultKeepalive = KeepalivePolicy{
	Interval: DefaultKeepaliveInterval,
	CountMax: DefaultKeepaliveCountMax,
}

// resolved applies the original fallback and disables an unusable schedule.
func (k KeepalivePolicy) resolved() KeepalivePolicy {
	if k.Interval < 0 {
		return KeepalivePolicy{}
	}
	if k.Interval == 0 {
		return KeepalivePolicy{Interval: FallbackKeepaliveInterval, CountMax: FallbackKeepaliveCountMax}
	}
	if k.CountMax < 0 {
		k.CountMax = 0
	}
	return k
}

func (k KeepalivePolicy) key() string {
	k = k.resolved()
	if k.Interval <= 0 {
		return "keepalive:off"
	}
	return "keepalive:" + strconv.FormatInt(k.Interval.Milliseconds(), 10) +
		":" + strconv.Itoa(k.CountMax)
}

// LeaseKind selects the reuse rule. A shell requires an exact agent-forwarding
// match. Channel consumers such as SFTP and forwarding use the original
// asymmetric rule: a forwarding transport may serve a non-forwarding request,
// never the other way around.
type LeaseKind string

const (
	// LeaseShell is an interactive terminal channel.
	LeaseShell LeaseKind = "shell"
	// LeaseChannel is a non-shell channel consumer.
	LeaseChannel LeaseKind = "channel"
)

func (k LeaseKind) orShell() LeaseKind {
	if k == "" {
		return LeaseShell
	}
	return k
}

// endpointConfig is the pool identity of one endpoint plus its effective
// connection configuration. Credential plaintext never reaches the key: Host
// credentials arrive through ConfigFingerprint as an opaque digest, and the
// proxy credential is digested here. Agent-forwarding state is deliberately
// excluded because it is an asymmetric reuse rule, not a distinct transport.
type endpointConfig struct {
	key               string
	hostID            string
	hostname          string
	port              int
	username          string
	agentForwarding   bool
	forwardingAgent   string
	singleChannel     bool
	noReuse           bool
	keepalive         KeepalivePolicy
	configFingerprint string
}

// allowsReuse applies the original findTransportByEndpoint rules.
func (e endpointConfig) allowsReuse(existing endpointConfig, kind LeaseKind) bool {
	if e.key != existing.key {
		return false
	}
	// A single-channel bastion must not lend its transport to another shell
	// or channel, and an X11 transport never enters the general pool.
	if e.noReuse || existing.noReuse || e.singleChannel || existing.singleChannel {
		return false
	}
	if kind.orShell() == LeaseShell {
		if e.agentForwarding != existing.agentForwarding {
			return false
		}
		if e.agentForwarding && e.forwardingAgent != existing.forwardingAgent {
			return false
		}
		return true
	}
	return !e.agentForwarding || existing.agentForwarding
}

// endpointFor derives the pool identity from a Request. Host ID scopes reuse
// to one saved Host, so two Hosts that share an address never silently share
// an authenticated transport.
func endpointFor(req Request) endpointConfig {
	port := req.Port
	if port == 0 {
		port = 22
	}
	username := req.Username
	if username == "" {
		username = "root"
	}
	jumps := make([]string, 0, len(req.Jumps))
	for _, hop := range req.Jumps {
		hopPort := hop.Port
		if hopPort == 0 {
			hopPort = 22
		}
		hopUser := hop.Username
		if hopUser == "" {
			hopUser = "root"
		}
		jumps = append(jumps, digest(
			string(hop.HostID), hop.Hostname, strconv.Itoa(hopPort), hopUser,
			hop.ConfigFingerprint, algorithmFingerprint(hop.Algorithms),
		))
	}
	proxy := "-"
	if req.Proxy != nil {
		proxyPort := req.Proxy.Port
		proxy = digest(
			string(req.Proxy.Type), req.Proxy.Host, strconv.Itoa(proxyPort),
			req.Proxy.Command, req.Proxy.Username, req.Proxy.Password,
		)
	}
	keepalive := req.Keepalive
	if keepalive == nil {
		keepalive = &KeepalivePolicy{}
	}
	resolved := keepalive.resolved()
	return endpointConfig{
		key: digest(
			"ssh", string(req.HostID), req.Hostname, strconv.Itoa(port), username,
			digest(jumps...), proxy, algorithmFingerprint(req.Algorithms),
			resolved.key(), req.ConfigFingerprint,
		),
		hostID:            string(req.HostID),
		hostname:          req.Hostname,
		port:              port,
		username:          username,
		agentForwarding:   req.AgentForwarding,
		forwardingAgent:   req.ForwardingAgentFingerprint,
		singleChannel:     req.SingleChannel,
		noReuse:           req.SingleChannel || req.X11Forwarding,
		keepalive:         resolved,
		configFingerprint: req.ConfigFingerprint,
	}
}

func algorithmFingerprint(policy AlgorithmPolicy) string {
	parts := []string{"legacy:" + strconv.FormatBool(policy.Legacy), "ecdsa:" + strconv.FormatBool(policy.SkipECDSAHostKey)}
	for _, list := range [][]string{policy.Ciphers, policy.KeyExchanges, policy.MACs, policy.HostKeys} {
		parts = append(parts, strings.Join(list, ","))
	}
	return digest(parts...)
}

func digest(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))[:32]
}

// timerScheduler schedules a one-shot callback and returns its cancellation.
// Tests replace it to fire idle deadlines deterministically.
type timerScheduler func(time.Duration, func()) (stop func())

// pendingDial is one in-flight physical dial. Concurrent consumers of the
// same compatible endpoint join it instead of opening a second handshake.
type pendingDial struct {
	endpoint   endpointConfig
	kind       LeaseKind
	registered bool
	done       chan struct{}
	conn       *Connection
	err        error
}

type pool struct {
	mu       sync.Mutex
	byKey    map[string][]*Connection
	pending  map[string][]*pendingDial
	idle     *list.List
	now      func() time.Time
	idleTTL  time.Duration
	maxIdle  int
	schedule timerScheduler
	closed   bool
}

func newPool(idleTTL time.Duration, maxIdle int, now func() time.Time, schedule timerScheduler) *pool {
	if maxIdle <= 0 {
		maxIdle = MaxIdleConnections
	}
	return &pool{
		byKey:    make(map[string][]*Connection),
		pending:  make(map[string][]*pendingDial),
		idle:     list.New(),
		now:      now,
		idleTTL:  idleTTL,
		maxIdle:  maxIdle,
		schedule: schedule,
	}
}

// begin atomically decides between reusing a parked transport, joining a
// compatible in-flight dial, and leading a new physical dial.
func (p *pool) begin(ep endpointConfig, kind LeaseKind) (lease *Lease, wait *pendingDial, lead *pendingDial) {
	var expired []*Connection
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, nil, &pendingDial{endpoint: ep, kind: kind, done: closedChannel(), err: ErrClosed}
	}
	lease, expired = p.borrowLocked(ep, kind)
	if lease != nil {
		p.mu.Unlock()
		closeConnections(expired)
		return lease, nil, nil
	}
	if !ep.noReuse {
		for _, candidate := range p.pending[ep.key] {
			select {
			case <-candidate.done:
				continue
			default:
			}
			if ep.allowsReuse(candidate.endpoint, kind) {
				p.mu.Unlock()
				closeConnections(expired)
				return nil, candidate, nil
			}
		}
	}
	lead = &pendingDial{endpoint: ep, kind: kind, done: make(chan struct{})}
	if !ep.noReuse {
		lead.registered = true
		p.pending[ep.key] = append(p.pending[ep.key], lead)
	}
	p.mu.Unlock()
	closeConnections(expired)
	return nil, nil, lead
}

// borrowLocked returns a lease on the best compatible transport. Live
// transports are preferred over parked ones, then the fewest leases.
func (p *pool) borrowLocked(ep endpointConfig, kind LeaseKind) (*Lease, []*Connection) {
	var expired []*Connection
	var best *Connection
	bestIdle := false
	bestRefs := 0
	for _, candidate := range p.byKey[ep.key] {
		candidate.mu.Lock()
		closed := candidate.closed
		compatible := !closed && ep.allowsReuse(candidate.endpoint, kind)
		idle := candidate.idle
		refs := candidate.refs
		expiredIdle := idle && p.idleTTL > 0 && !candidate.idleSince.IsZero() &&
			p.now().Sub(candidate.idleSince) >= p.idleTTL
		candidate.mu.Unlock()
		if closed {
			continue
		}
		if expiredIdle {
			expired = append(expired, candidate)
			continue
		}
		if !compatible {
			continue
		}
		if best == nil ||
			(bestIdle && !idle) ||
			(bestIdle == idle && refs < bestRefs) {
			best = candidate
			bestIdle = idle
			bestRefs = refs
		}
	}
	for _, candidate := range expired {
		p.removeLocked(candidate)
	}
	if best == nil {
		return nil, expired
	}
	fromIdle := false
	if bestIdle {
		p.wakeLocked(best)
		fromIdle = true
	}
	best.mu.Lock()
	best.refs++
	best.mu.Unlock()
	return &Lease{conn: best, kind: kind, fromIdle: fromIdle}, nil
}

// borrow takes a lease on a completed pending dial.
func (p *pool) borrow(conn *Connection, ep endpointConfig, kind LeaseKind) *Lease {
	if conn == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	conn.mu.Lock()
	// A no-reuse transport is never registered for reuse, so the only borrow
	// it serves is its own leader's first lease. The endpoint key still has to
	// match, but the reuse rules would otherwise reject that lease too.
	compatible := ep.allowsReuse(conn.endpoint, kind)
	if conn.endpoint.noReuse || ep.noReuse {
		compatible = conn.endpoint.key == ep.key
	}
	if conn.closed || !compatible {
		conn.mu.Unlock()
		return nil
	}
	conn.refs++
	conn.mu.Unlock()
	return &Lease{conn: conn, kind: kind}
}

// complete publishes a dial's outcome to every waiter and registers a
// successful transport for reuse. It reports ErrClosed when the pool shut down
// while the dial was in flight.
func (p *pool) complete(lead *pendingDial, conn *Connection, err error) error {
	if lead == nil {
		return err
	}
	p.mu.Lock()
	if lead.registered {
		p.removePendingLocked(lead)
	}
	outcome := err
	if p.closed {
		if outcome == nil {
			outcome = ErrClosed
		}
	} else if err == nil && conn != nil && lead.registered {
		p.byKey[conn.endpoint.key] = append(p.byKey[conn.endpoint.key], conn)
	}
	lead.conn, lead.err = conn, outcome
	p.mu.Unlock()
	close(lead.done)
	if outcome != err && conn != nil {
		_ = conn.closeTransport("manager-closed")
	}
	return outcome
}

func (p *pool) removePendingLocked(lead *pendingDial) {
	list := p.pending[lead.endpoint.key]
	for i, candidate := range list {
		if candidate == lead {
			p.pending[lead.endpoint.key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(p.pending[lead.endpoint.key]) == 0 {
		delete(p.pending, lead.endpoint.key)
	}
}

// park moves the last released transport into idle parking. The caller holds
// no pool lock.
func (p *pool) park(conn *Connection) {
	var evicted []*Connection
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = conn.closeTransport("manager-closed")
		return
	}
	conn.mu.Lock()
	if conn.closed || conn.refs > 0 {
		conn.mu.Unlock()
		p.mu.Unlock()
		return
	}
	if !conn.allowIdlePark || conn.endpoint.noReuse {
		p.removeLocked(conn)
		conn.mu.Unlock()
		p.mu.Unlock()
		_ = conn.closeTransport("no-idle-park")
		return
	}
	conn.idle = true
	conn.idleSince = p.now()
	if conn.lruElement == nil {
		conn.lruElement = p.idle.PushBack(conn)
	}
	evicted = p.enforceIdleLimitLocked()
	if p.idleTTL > 0 {
		conn.stopTimer = p.schedule(p.idleTTL, func() { p.expire(conn) })
	}
	conn.mu.Unlock()
	p.mu.Unlock()
	closeConnections(evicted)
}

// expire closes a parked transport whose idle deadline elapsed.
func (p *pool) expire(conn *Connection) {
	p.mu.Lock()
	conn.mu.Lock()
	if conn.closed || !conn.idle || conn.refs > 0 {
		conn.mu.Unlock()
		p.mu.Unlock()
		return
	}
	p.removeLocked(conn)
	conn.mu.Unlock()
	p.mu.Unlock()
	_ = conn.closeTransport("idle-ttl")
}

// discard removes a transport that can no longer be reused.
func (p *pool) discard(conn *Connection, reason string) {
	if conn == nil {
		return
	}
	p.mu.Lock()
	p.removeLocked(conn)
	p.mu.Unlock()
	_ = conn.closeTransport(reason)
}

// enforceIdleLimitLocked evicts least-recently-used zero-lease transports.
func (p *pool) enforceIdleLimitLocked() []*Connection {
	var evicted []*Connection
	for p.idle.Len() > p.maxIdle {
		element := p.idle.Front()
		if element == nil {
			break
		}
		conn := element.Value.(*Connection)
		conn.mu.Lock()
		eligible := conn.idle && conn.refs == 0
		conn.mu.Unlock()
		if !eligible {
			p.idle.Remove(element)
			conn.mu.Lock()
			conn.lruElement = nil
			conn.mu.Unlock()
			continue
		}
		p.removeLocked(conn)
		if conn.stopTimer != nil {
			conn.stopTimer()
			conn.stopTimer = nil
		}
		evicted = append(evicted, conn)
	}
	return evicted
}

func (p *pool) wakeLocked(conn *Connection) {
	if !conn.idle {
		return
	}
	conn.idle = false
	conn.idleSince = time.Time{}
	if conn.lruElement != nil {
		p.idle.Remove(conn.lruElement)
		conn.lruElement = nil
	}
	if conn.stopTimer != nil {
		conn.stopTimer()
		conn.stopTimer = nil
	}
}

// removeLocked unlinks a transport from every index.
func (p *pool) removeLocked(conn *Connection) {
	list := p.byKey[conn.endpoint.key]
	for i, candidate := range list {
		if candidate == conn {
			p.byKey[conn.endpoint.key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(p.byKey[conn.endpoint.key]) == 0 {
		delete(p.byKey, conn.endpoint.key)
	}
	if conn.lruElement != nil {
		p.idle.Remove(conn.lruElement)
		conn.lruElement = nil
	}
	if conn.stopTimer != nil {
		conn.stopTimer()
		conn.stopTimer = nil
	}
}

// closeAll stops the pool and returns every transport for teardown.
func (p *pool) closeAll() []*Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	seen := make(map[*Connection]bool)
	var connections []*Connection
	for _, list := range p.byKey {
		for _, conn := range list {
			if !seen[conn] {
				seen[conn] = true
				connections = append(connections, conn)
				if conn.stopTimer != nil {
					conn.stopTimer()
					conn.stopTimer = nil
				}
			}
		}
	}
	p.byKey = make(map[string][]*Connection)
	p.idle = list.New()
	// Pending dials complete through their leader; closing here would race the
	// dial goroutine and double-close the client it produces.
	return connections
}

func closeConnections(connections []*Connection) {
	for _, conn := range connections {
		_ = conn.closeTransport("idle-evicted")
	}
}

func closedChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
