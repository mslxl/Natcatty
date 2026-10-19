package forwarding

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// Start begins a rule. It marks the rule connecting and returns; the
// connection, bind, and shell are established on the manager's goroutine, and
// failures become error status events. Starting an already connecting or
// active rule is a no-op.
func (m *Manager) Start(ctx context.Context, id model.ForwardingRuleID) error {
	rule, err := m.getRule(ctx, id)
	if err != nil {
		return err
	}
	if !rule.IsEnabled() {
		return disabled()
	}
	if m.connector == nil {
		return internal(errors.New("forwarding: no connector"))
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return internal(errors.New("forwarding: manager is closed"))
	}
	entry, ok := m.runtime[id]
	if !ok {
		entry = &runtimeEntry{view: View{Rule: rule, Status: domainforward.StatusInactive}}
		m.runtime[id] = entry
	}
	entry.view.Rule = rule
	if entry.view.Status == domainforward.StatusConnecting || entry.view.Status == domainforward.StatusActive {
		m.mu.Unlock()
		return nil
	}
	generation := m.beginLocked(entry, true)
	m.mu.Unlock()

	go m.run(id, generation)
	return nil
}

// Stop stops a running rule and cancels a pending reconnect. It returns as
// soon as the stop is requested; a remote listener that cannot release its
// forward is closed in the background and bounded by the unforward timeout.
func (m *Manager) Stop(ctx context.Context, id model.ForwardingRuleID) error {
	m.mu.Lock()
	entry, ok := m.runtime[id]
	if !ok {
		m.mu.Unlock()
		// A rule that exists but was never touched by this manager still has a
		// meaningful "not found" answer.
		if _, err := m.getRule(ctx, id); err != nil {
			return err
		}
		return nil
	}
	m.stopEntryLocked(entry)
	listener, conn := entry.listener, entry.conn
	entry.listener, entry.conn = nil, nil
	m.mu.Unlock()

	if listener != nil || conn != nil {
		go m.cleanup(entry, listener, conn)
	}
	return nil
}

// AutoStart starts every enabled rule that requests auto-start. Start is
// asynchronous, so app launch is never blocked on a network handshake.
func (m *Manager) AutoStart(ctx context.Context) {
	views, err := m.List(ctx)
	if err != nil {
		return
	}
	for _, view := range views {
		if !view.Rule.AutoStart || !view.Rule.IsEnabled() {
			continue
		}
		_ = m.Start(ctx, view.Rule.ID)
	}
}

// beginLocked starts a new generation for an entry. The caller owns the lock.
// A manual start resets the reconnect attempt count; a scheduled reconnect
// preserves it so the bound applies across the whole outage.
func (m *Manager) beginLocked(entry *runtimeEntry, resetAttempts bool) uint64 {
	entry.generation++
	if entry.stopCh != nil {
		close(entry.stopCh)
	}
	entry.stopCh = make(chan struct{})
	if entry.reconnectStop != nil {
		entry.reconnectStop()
		entry.reconnectStop = nil
	}
	entry.view.Status = domainforward.StatusConnecting
	entry.view.Error = ErrorNone
	entry.view.Detail = ""
	entry.view.Hop = nil
	if resetAttempts {
		entry.view.Attempts = 0
	}
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: entry.view.Rule.ID, View: entry.view,
	})
	return entry.generation
}

// run establishes one attempt. It never blocks Stop: every mutation checks
// that its generation is still current, so a late connection or bind is
// discarded instead of resurrecting a stopped rule.
func (m *Manager) run(id model.ForwardingRuleID, generation uint64) {
	rule, stop, ok := m.attempt(id, generation)
	if !ok {
		return
	}

	conn, err := m.connector.Connect(context.Background(), rule.HostID)
	if conn == nil && err == nil {
		err = errors.New("forwarding: connector returned no connection")
	}
	if !m.isCurrent(id, generation) {
		if conn != nil {
			conn.Discard()
		}
		return
	}
	if err != nil {
		if conn != nil {
			conn.Discard()
		}
		m.fail(id, generation, err, true)
		return
	}

	listener, err := m.establish(rule, conn)
	if err != nil {
		conn.Discard()
		// fail decides retryability from the error's kind: a bind conflict or
		// rejected host key is permanent, a network or timeout failure is not.
		m.fail(id, generation, err, true)
		return
	}
	if !m.activate(id, generation, conn, listener) {
		_ = listener.Close()
		conn.Discard()
		return
	}

	errCh := m.serve(rule, conn, listener)
	var serveErr error
	select {
	case <-stop:
		// Stop owns cleanup; it captured the resources under the lock.
		return
	case serveErr = <-errCh:
	case <-connectionDone(conn):
		// A local listener does not notice its transport dying, so subscribe
		// to the lease's lifetime and treat transport loss as a failure.
		serveErr = runtimeError(ErrorNetwork, errors.New("forwarding: connection closed"))
	}
	if !m.isCurrent(id, generation) {
		return
	}
	m.detach(id, generation)
	// Discard the transport first: for a remote listener, closing the
	// listener can wait for a cancel-tcpip-forward reply that only the
	// transport can unblock.
	conn.Discard()
	go func() { _ = listener.Close() }()
	m.fail(id, generation, serveErr, true)
}

// attempt copies the current rule and stop channel for one generation.
func (m *Manager) attempt(id model.ForwardingRuleID, generation uint64) (domainforward.Rule, <-chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.runtime[id]
	if !ok || entry.generation != generation || entry.stopCh == nil {
		return domainforward.Rule{}, nil, false
	}
	return entry.view.Rule, entry.stopCh, true
}

func (m *Manager) isCurrent(id model.ForwardingRuleID, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.runtime[id]
	return ok && entry.generation == generation && !m.closed
}

// activate publishes the active status and records the resources Stop must
// clean up. It fails when a stop raced the establishment.
func (m *Manager) activate(id model.ForwardingRuleID, generation uint64, conn Connection, listener net.Listener) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.runtime[id]
	if !ok || entry.generation != generation || m.closed {
		return false
	}
	entry.conn = conn
	entry.listener = listener
	entry.view.Status = domainforward.StatusActive
	entry.view.Error = ErrorNone
	entry.view.Detail = ""
	entry.view.Hop = nil
	entry.view.Attempts = 0
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: entry.view.Rule.ID, View: entry.view,
	})
	return true
}

// detach removes a failed attempt's resources so Stop will not close them a
// second time.
func (m *Manager) detach(id model.ForwardingRuleID, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.runtime[id]
	if !ok || entry.generation != generation {
		return
	}
	entry.conn = nil
	entry.listener = nil
}

// fail records a runtime failure and, when retryable and enabled, schedules a
// bounded reconnect. A permanent failure such as a bind conflict or rejected
// host key never retries.
func (m *Manager) fail(id model.ForwardingRuleID, generation uint64, cause error, allowRetry bool) {
	failure := classifyRuntimeError(cause)
	if failure == nil {
		return
	}

	m.mu.Lock()
	entry, ok := m.runtime[id]
	if !ok || entry.generation != generation || m.closed {
		m.mu.Unlock()
		return
	}
	entry.view.Status = domainforward.StatusError
	entry.view.Error = failure.Kind
	entry.view.Detail = failure.Detail
	entry.view.Hop = cloneHop(failure.Hop)

	retry := allowRetry && !failure.Permanent() &&
		entry.view.Rule.IsEnabled() && entry.view.Rule.AutoReconnectEnabled() &&
		entry.view.Attempts < ReconnectAttempts
	if retry {
		entry.view.Attempts++
	}
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: entry.view.Rule.ID, View: entry.view,
	})
	m.mu.Unlock()

	if !retry {
		return
	}
	stop := m.schedule(ReconnectDelay, func() { m.reconnect(id, generation) })
	m.mu.Lock()
	entry, ok = m.runtime[id]
	if ok && entry.generation == generation && entry.view.Status == domainforward.StatusError {
		entry.reconnectStop = stop
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	stop()
}

// reconnect re-reads the rule before each attempt, so disabling
// auto-reconnect or deleting the rule cancels the retry without a manager
// restart.
func (m *Manager) reconnect(id model.ForwardingRuleID, generation uint64) {
	m.mu.Lock()
	entry, ok := m.runtime[id]
	if !ok || entry.generation != generation || m.closed {
		m.mu.Unlock()
		return
	}
	entry.reconnectStop = nil
	m.mu.Unlock()

	rule, err := m.getRule(context.Background(), id)
	if err != nil || !rule.AutoReconnectEnabled() || !rule.IsEnabled() {
		return
	}

	m.mu.Lock()
	entry, ok = m.runtime[id]
	if !ok || entry.generation != generation || m.closed {
		m.mu.Unlock()
		return
	}
	entry.view.Rule = rule
	next := m.beginLocked(entry, false)
	m.mu.Unlock()

	go m.run(id, next)
}

// establish creates the listener for the rule's type.
func (m *Manager) establish(rule domainforward.Rule, conn Connection) (net.Listener, error) {
	var (
		listener net.Listener
		err      error
	)
	switch rule.Type {
	case domainforward.TypeLocal, domainforward.TypeDynamic:
		listener, err = m.listen("tcp", rule.ListenAddress())
	case domainforward.TypeRemote:
		listener, err = conn.ListenTCP(rule.ListenAddress())
	default:
		return nil, runtimeError(ErrorUnsupported, errors.New("forwarding: unsupported rule type"))
	}
	if err != nil {
		return nil, classifyRuntimeError(err)
	}
	return listener, nil
}

// serve accepts inbound connections for one listener.
func (m *Manager) serve(rule domainforward.Rule, conn Connection, listener net.Listener) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		for {
			incoming, err := listener.Accept()
			if err != nil {
				if isClosedError(err) {
					return
				}
				select {
				case errCh <- err:
				default:
				}
				return
			}
			switch rule.Type {
			case domainforward.TypeLocal:
				go m.forwardLocal(conn, rule, incoming)
			case domainforward.TypeDynamic:
				go m.forwardDynamic(conn, incoming)
			case domainforward.TypeRemote:
				go m.forwardRemote(rule, incoming)
			default:
				_ = incoming.Close()
			}
		}
	}()
	return errCh
}

// forwardLocal dials the destination through the SSH Host.
func (m *Manager) forwardLocal(conn Connection, rule domainforward.Rule, incoming net.Conn) {
	target, err := conn.DialContext(context.Background(), "tcp", rule.Destination())
	if err != nil {
		_ = incoming.Close()
		return
	}
	pipe(incoming, target)
}

// forwardRemote receives a connection from the SSH Host and dials the
// destination on this machine. A server-side listener exposes a net.Conn, so
// the direction is the mirror image of a local rule.
func (m *Manager) forwardRemote(rule domainforward.Rule, incoming net.Conn) {
	target, err := m.dialLocal("tcp", rule.Destination())
	if err != nil {
		_ = incoming.Close()
		return
	}
	pipe(incoming, target)
}

// forwardDynamic speaks SOCKS5, then dials the requested destination through
// the SSH Host. The idle deadline bounds the handshake only: once CONNECT
// succeeds the original clears it so a long-lived idle tunnel stays open.
func (m *Manager) forwardDynamic(conn Connection, incoming net.Conn) {
	client := &idleConn{Conn: incoming, timeout: m.socksIdleTimeout}
	destination, err := socks5Handshake(client)
	if err != nil {
		_ = incoming.Close()
		return
	}
	target, err := conn.DialContext(context.Background(), "tcp", destination)
	if err != nil {
		_ = socks5Reply(client, socks5ReplyConnectionRefused)
		_ = incoming.Close()
		return
	}
	if err := socks5Reply(client, socks5ReplySucceeded); err != nil {
		_ = target.Close()
		_ = incoming.Close()
		return
	}
	// Match the original: clear the handshake timeout for the tunnel itself.
	_ = incoming.SetDeadline(time.Time{})
	pipe(incoming, target)
}

// connectionDone exposes an optional transport-liveness signal. Connectors
// that cannot report transport death return nil, which disables the select
// case rather than firing immediately.
func connectionDone(conn Connection) <-chan struct{} {
	watcher, ok := conn.(interface{ Done() <-chan struct{} })
	if !ok {
		return nil
	}
	return watcher.Done()
}

// classifyRuntimeError maps an error to a typed runtime failure. A connector
// that already returns *RuntimeError keeps its classification and hop.
func classifyRuntimeError(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	var typed *RuntimeError
	if errors.As(err, &typed) {
		return typed
	}
	switch {
	case errors.Is(err, syscall.EADDRINUSE),
		errors.Is(err, syscall.EACCES),
		errors.Is(err, syscall.EADDRNOTAVAIL):
		return runtimeError(ErrorBindConflict, err)
	case errors.Is(err, context.Canceled):
		return runtimeError(ErrorCancelled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return runtimeError(ErrorTimeout, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return runtimeError(ErrorTimeout, err)
	}
	return runtimeError(ErrorNetwork, err)
}

func isClosedError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled)
}

func cloneHop(hop *Hop) *Hop {
	if hop == nil {
		return nil
	}
	copy := *hop
	return &copy
}

// pipe copies both directions and closes both sides when either ends.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = copyStream(a, b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = copyStream(b, a)
		done <- struct{}{}
	}()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func copyStream(dst, src net.Conn) (int64, error) {
	buffer := make([]byte, 32<<10)
	var written int64
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// idleConn bounds a SOCKS handshake by resetting the deadline before every
// read and write. The tunnel clears the deadline once CONNECT succeeds.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Write(p)
}
