package plugin

import (
	"context"
	"strings"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// TerminalObserve opens a lossy, read-only output tap on one Session. The tap
// owns a bounded buffer; if the guest does not read fast enough, the oldest
// bytes are dropped and counted, never retained without limit. Observation
// grants no filesystem or network capability: it calls only the terminal port.
func (s *ServiceSession) TerminalObserve(sessionID string) (Handle, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, domainplugin.Scope{},
			workspaceInvalid("session id must not be empty"))
	}
	scope, err := s.checkTerminalSession(ServiceTerminal, "observe",
		domainplugin.PermissionTerminalObserve, sessionID)
	if err != nil {
		return 0, err
	}
	if s.host.terminalCatalog == nil {
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, scope, unavailableTerminal())
	}
	observation, err := s.host.terminalCatalog.Observe(context.Background(), sessionID)
	if err != nil {
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, scope, err)
	}
	tap := newTerminalTap(sessionID, observation, s.host.limits.TerminalQueueBytes)
	s.mu.Lock()
	if len(s.terminals) >= s.host.limits.TerminalObservations {
		s.mu.Unlock()
		tap.close()
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, scope,
			workspaceLimit("at most %d terminal observations per plugin instance",
				s.host.limits.TerminalObservations))
	}
	if s.handles == nil {
		s.mu.Unlock()
		tap.close()
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, scope,
			&workspaceFailure{Code: ServiceCodeFault, Problem: "handle table is not bound",
				Cause: ErrServiceUnavailable})
	}
	handle, err := s.handles.Issue(tap)
	if err != nil {
		s.mu.Unlock()
		tap.close()
		return 0, s.recordFailure(ServiceTerminal, "observe",
			domainplugin.PermissionTerminalObserve, scope, err)
	}
	if s.terminals == nil {
		s.terminals = make(map[Handle]*terminalTap)
	}
	s.terminals[handle] = tap
	s.mu.Unlock()
	s.recordSuccess(ServiceTerminal, "observe", domainplugin.PermissionTerminalObserve, scope)
	return handle, nil
}

// TerminalRead drains up to limit buffered bytes from one observation. It
// blocks until output arrives, the tap closes, or ctx is cancelled. dropped is
// the cumulative number of bytes the bounded buffer discarded; closed reports
// that the tap reached its end and the next read has no more output.
func (s *ServiceSession) TerminalRead(ctx context.Context, handle Handle, limit int) ([]byte, uint64, bool, error) {
	tap, err := s.terminalTap(handle)
	if err != nil {
		return nil, 0, false, s.recordFailure(ServiceTerminal, "read",
			domainplugin.PermissionTerminalObserve, domainplugin.Scope{}, err)
	}
	if limit < 0 || limit > s.host.limits.TerminalQueueBytes {
		return nil, 0, false, s.recordFailure(ServiceTerminal, "read",
			domainplugin.PermissionTerminalObserve, sessionScope(tap.sessionID),
			workspaceLimit("read length must be between 0 and %d bytes", s.host.limits.TerminalQueueBytes))
	}
	if limit == 0 {
		limit = s.host.limits.TerminalQueueBytes
	}
	data, dropped, closed, err := tap.read(ctx, limit)
	if err != nil {
		return nil, dropped, closed, s.recordFailure(ServiceTerminal, "read",
			domainplugin.PermissionTerminalObserve, sessionScope(tap.sessionID), err)
	}
	s.recordSuccess(ServiceTerminal, "read", domainplugin.PermissionTerminalObserve,
		sessionScope(tap.sessionID))
	return data, dropped, closed, nil
}

// TerminalClose closes one observation tap and releases its handle.
func (s *ServiceSession) TerminalClose(handle Handle) error {
	s.mu.Lock()
	tap, ok := s.terminals[handle]
	var handles *HandleTable
	if ok {
		delete(s.terminals, handle)
		handles = s.handles
	}
	s.mu.Unlock()
	if !ok {
		return s.recordFailure(ServiceTerminal, "close", "", domainplugin.Scope{},
			workspaceInvalid("handle is not a terminal observation"))
	}
	if handles != nil {
		_ = handles.Release(handle)
	}
	tap.close()
	s.recordSuccess(ServiceTerminal, "close", "", domainplugin.Scope{})
	return nil
}

// TerminalWrite sends input to one Session. It requires the exact
// terminal.write session scope; observation read access never implies input.
func (s *ServiceSession) TerminalWrite(sessionID string, data []byte) error {
	if strings.TrimSpace(sessionID) == "" {
		return s.recordFailure(ServiceTerminal, "write",
			domainplugin.PermissionTerminalWrite, domainplugin.Scope{},
			workspaceInvalid("session id must not be empty"))
	}
	if len(data) > s.host.limits.FileChunkBytes {
		return s.recordFailure(ServiceTerminal, "write",
			domainplugin.PermissionTerminalWrite, sessionScope(sessionID),
			workspaceLimit("write length must not exceed %d bytes", s.host.limits.FileChunkBytes))
	}
	scope := sessionScope(sessionID)
	if err := s.check(ServiceTerminal, "write", domainplugin.PermissionTerminalWrite, scope); err != nil {
		return err
	}
	if s.host.terminalCatalog == nil {
		return s.recordFailure(ServiceTerminal, "write",
			domainplugin.PermissionTerminalWrite, scope, unavailableTerminal())
	}
	if err := s.host.terminalCatalog.Write(context.Background(), sessionID, data); err != nil {
		return s.recordFailure(ServiceTerminal, "write",
			domainplugin.PermissionTerminalWrite, scope, err)
	}
	s.recordSuccess(ServiceTerminal, "write", domainplugin.PermissionTerminalWrite, scope)
	return nil
}

// TerminalInfo returns one Session's title, size, and cursor metadata. It
// requires the separate terminal.info session scope.
func (s *ServiceSession) TerminalInfo(sessionID string) (TerminalInfo, error) {
	if strings.TrimSpace(sessionID) == "" {
		return TerminalInfo{}, s.recordFailure(ServiceTerminal, "info",
			domainplugin.PermissionTerminalInfo, domainplugin.Scope{},
			workspaceInvalid("session id must not be empty"))
	}
	scope := sessionScope(sessionID)
	if err := s.check(ServiceTerminal, "info", domainplugin.PermissionTerminalInfo, scope); err != nil {
		return TerminalInfo{}, err
	}
	if s.host.terminalCatalog == nil {
		return TerminalInfo{}, s.recordFailure(ServiceTerminal, "info",
			domainplugin.PermissionTerminalInfo, scope, unavailableTerminal())
	}
	info, ok, err := s.host.terminalCatalog.Info(context.Background(), sessionID)
	if err != nil {
		return TerminalInfo{}, s.recordFailure(ServiceTerminal, "info",
			domainplugin.PermissionTerminalInfo, scope, err)
	}
	if !ok {
		return TerminalInfo{}, s.recordFailure(ServiceTerminal, "info",
			domainplugin.PermissionTerminalInfo, scope, workspaceNotFound("session does not exist"))
	}
	s.recordSuccess(ServiceTerminal, "info", domainplugin.PermissionTerminalInfo, scope)
	return info, nil
}

// checkTerminalSession authorizes one Session for a terminal operation. A
// Session created by the calling plugin's connection provider needs no
// per-session grant; every other Session must be named by the exact scope.
func (s *ServiceSession) checkTerminalSession(service, method string,
	permission domainplugin.Permission, sessionID string) (domainplugin.Scope, error) {
	grant, err := s.requireGrant(service, method, permission)
	if err != nil {
		return domainplugin.Scope{}, err
	}
	if s.host.sessionCatalog != nil {
		session, ok, err := s.host.sessionCatalog.Get(context.Background(), sessionID)
		if err != nil {
			return domainplugin.Scope{}, s.recordFailure(service, method, permission,
				sessionScope(sessionID), err)
		}
		if !ok {
			return domainplugin.Scope{}, s.recordFailure(service, method, permission,
				sessionScope(sessionID), workspaceNotFound("session does not exist"))
		}
		if session.ProviderCreatedBy != "" && session.ProviderCreatedBy == s.ref.ID {
			return grant.Scope, nil
		}
	}
	scope := sessionScope(sessionID)
	if err := s.check(service, method, permission, scope); err != nil {
		return domainplugin.Scope{}, err
	}
	return scope, nil
}

func (s *ServiceSession) terminalTap(handle Handle) (*terminalTap, error) {
	if s == nil {
		return nil, workspaceInvalid("service session is closed")
	}
	s.mu.Lock()
	tap, ok := s.terminals[handle]
	s.mu.Unlock()
	if !ok {
		return nil, workspaceInvalid("handle is not a terminal observation")
	}
	return tap, nil
}

func unavailableTerminal() error {
	return &workspaceFailure{Code: ServiceCodeFault,
		Problem: "terminal port is not configured", Cause: ErrServiceUnavailable}
}

// terminalTap is one bounded lossy read-only output buffer. A pump goroutine
// drains the host observation so a slow guest drops old bytes instead of
// applying backpressure that would stall the Session.
type terminalTap struct {
	sessionID   string
	observation TerminalObservation
	limit       int

	mu      sync.Mutex
	buffer  []byte
	dropped uint64
	closed  bool
	notify  chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newTerminalTap(sessionID string, observation TerminalObservation, limit int) *terminalTap {
	tap := &terminalTap{
		sessionID:   sessionID,
		observation: observation,
		limit:       limit,
		notify:      make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	go tap.pump()
	return tap
}

func (t *terminalTap) pump() {
	buffer := make([]byte, 4096)
	for {
		n, err := t.observation.Read(context.Background(), buffer)
		if n > 0 {
			t.push(buffer[:n])
		}
		if err != nil {
			// io.EOF is the normal end of an observation; any other error also
			// ends the tap, because a terminal stream cannot be resumed after a
			// host-side read failure.
			t.markClosed()
			return
		}
	}
}

func (t *terminalTap) push(data []byte) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.buffer = append(t.buffer, data...)
	if overflow := len(t.buffer) - t.limit; overflow > 0 {
		copy(t.buffer, t.buffer[overflow:])
		t.buffer = t.buffer[:len(t.buffer)-overflow]
		t.dropped += uint64(overflow)
	}
	t.mu.Unlock()
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *terminalTap) markClosed() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	close(t.done)
}

func (t *terminalTap) read(ctx context.Context, limit int) ([]byte, uint64, bool, error) {
	for {
		t.mu.Lock()
		if len(t.buffer) > 0 {
			n := limit
			if n > len(t.buffer) {
				n = len(t.buffer)
			}
			data := make([]byte, n)
			copy(data, t.buffer[:n])
			t.buffer = t.buffer[n:]
			dropped := t.dropped
			t.mu.Unlock()
			return data, dropped, false, nil
		}
		closed := t.closed
		dropped := t.dropped
		t.mu.Unlock()
		if closed {
			return nil, dropped, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, dropped, false, ctx.Err()
		case <-t.done:
			// Loop once more to drain a final buffered chunk before reporting
			// closed; the next iteration returns any remaining bytes first.
		case <-t.notify:
		}
	}
}

func (t *terminalTap) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		t.markClosed()
		_ = t.observation.Close()
	})
}
