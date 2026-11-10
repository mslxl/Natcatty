package plugin

import (
	"context"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// SessionsList returns the Sessions this plugin may read: the Session named by
// its sessions.read grant, the Sessions its fiber created through
// sessions.connect, and the Sessions its connection provider created. A
// provider-created Session needs no per-session grant.
func (s *ServiceSession) SessionsList() ([]SessionSummary, error) {
	grant, err := s.requireGrant(ServiceSessions, "list", domainplugin.PermissionSessionsRead)
	if err != nil {
		return nil, err
	}
	if s.host.sessionCatalog == nil {
		return nil, s.recordFailure(ServiceSessions, "list", domainplugin.PermissionSessionsRead,
			grant.Scope, unavailableSessionCatalog())
	}
	sessions, err := s.host.sessionCatalog.List(context.Background())
	if err != nil {
		return nil, s.recordFailure(ServiceSessions, "list", domainplugin.PermissionSessionsRead,
			grant.Scope, err)
	}
	out := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		if s.sessionAccessible(session, grant) {
			out = append(out, session)
		}
	}
	s.recordSuccess(ServiceSessions, "list", domainplugin.PermissionSessionsRead, grant.Scope)
	return out, nil
}

// SessionsGet returns one Session this plugin may read.
func (s *ServiceSession) SessionsGet(id string) (SessionSummary, error) {
	if strings.TrimSpace(id) == "" {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "get",
			domainplugin.PermissionSessionsRead, domainplugin.Scope{}, workspaceInvalid("session id must not be empty"))
	}
	if _, err := s.requireGrant(ServiceSessions, "get", domainplugin.PermissionSessionsRead); err != nil {
		return SessionSummary{}, err
	}
	session, ok, err := s.lookupSession(id)
	if err != nil {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "get",
			domainplugin.PermissionSessionsRead, sessionScope(id), err)
	}
	if !ok {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "get",
			domainplugin.PermissionSessionsRead, sessionScope(id), workspaceNotFound("session does not exist"))
	}
	scope, err := s.checkSession(ServiceSessions, "get", domainplugin.PermissionSessionsRead, session, true)
	if err != nil {
		return SessionSummary{}, err
	}
	s.recordSuccess(ServiceSessions, "get", domainplugin.PermissionSessionsRead, scope)
	return session, nil
}

// SessionsConnect opens a Session for one Host. It requires sessions.control
// and, when the Host catalog is wired, a hosts.read view of that same Host:
// the Host scope bounds every Session the fiber creates. The new Session needs
// no per-session grant for this fiber's later read and control calls.
func (s *ServiceSession) SessionsConnect(hostID string) (SessionSummary, error) {
	grant, err := s.requireGrant(ServiceSessions, "connect", domainplugin.PermissionSessionsControl)
	if err != nil {
		return SessionSummary{}, err
	}
	if strings.TrimSpace(hostID) == "" {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "connect",
			domainplugin.PermissionSessionsControl, grant.Scope, workspaceInvalid("host id must not be empty"))
	}
	if err := s.authorizeConnectHost(hostID, grant); err != nil {
		return SessionSummary{}, err
	}
	if s.host.sessionCatalog == nil {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "connect",
			domainplugin.PermissionSessionsControl, grant.Scope, unavailableSessionCatalog())
	}
	session, err := s.host.sessionCatalog.Connect(context.Background(), hostID)
	if err != nil {
		return SessionSummary{}, s.recordFailure(ServiceSessions, "connect",
			domainplugin.PermissionSessionsControl, grant.Scope, err)
	}
	s.mu.Lock()
	if s.createdSessions == nil {
		s.createdSessions = make(map[string]struct{})
	}
	s.createdSessions[session.ID] = struct{}{}
	s.mu.Unlock()
	s.recordSuccess(ServiceSessions, "connect", domainplugin.PermissionSessionsControl, grant.Scope)
	return session, nil
}

// SessionsReconnect reopens one Session's transport. Provider-created and
// fiber-created Sessions need no per-session grant; every other Session must
// be named by the sessions.control scope.
func (s *ServiceSession) SessionsReconnect(id string) error {
	if strings.TrimSpace(id) == "" {
		return s.recordFailure(ServiceSessions, "reconnect",
			domainplugin.PermissionSessionsControl, domainplugin.Scope{},
			workspaceInvalid("session id must not be empty"))
	}
	if _, err := s.requireGrant(ServiceSessions, "reconnect",
		domainplugin.PermissionSessionsControl); err != nil {
		return err
	}
	session, ok, err := s.lookupSession(id)
	if err != nil {
		return s.recordFailure(ServiceSessions, "reconnect",
			domainplugin.PermissionSessionsControl, sessionScope(id), err)
	}
	if !ok {
		return s.recordFailure(ServiceSessions, "reconnect",
			domainplugin.PermissionSessionsControl, sessionScope(id), workspaceNotFound("session does not exist"))
	}
	scope, err := s.checkSession(ServiceSessions, "reconnect",
		domainplugin.PermissionSessionsControl, session, true)
	if err != nil {
		return err
	}
	if s.host.sessionCatalog == nil {
		return s.recordFailure(ServiceSessions, "reconnect",
			domainplugin.PermissionSessionsControl, scope, unavailableSessionCatalog())
	}
	if err := s.host.sessionCatalog.Reconnect(context.Background(), id); err != nil {
		return s.recordFailure(ServiceSessions, "reconnect",
			domainplugin.PermissionSessionsControl, scope, err)
	}
	s.recordSuccess(ServiceSessions, "reconnect", domainplugin.PermissionSessionsControl, scope)
	return nil
}

// SessionsDisconnect closes one Session under the same ownership rules as
// reconnect.
func (s *ServiceSession) SessionsDisconnect(id string) error {
	if strings.TrimSpace(id) == "" {
		return s.recordFailure(ServiceSessions, "disconnect",
			domainplugin.PermissionSessionsControl, domainplugin.Scope{},
			workspaceInvalid("session id must not be empty"))
	}
	if _, err := s.requireGrant(ServiceSessions, "disconnect",
		domainplugin.PermissionSessionsControl); err != nil {
		return err
	}
	session, ok, err := s.lookupSession(id)
	if err != nil {
		return s.recordFailure(ServiceSessions, "disconnect",
			domainplugin.PermissionSessionsControl, sessionScope(id), err)
	}
	if !ok {
		return s.recordFailure(ServiceSessions, "disconnect",
			domainplugin.PermissionSessionsControl, sessionScope(id), workspaceNotFound("session does not exist"))
	}
	scope, err := s.checkSession(ServiceSessions, "disconnect",
		domainplugin.PermissionSessionsControl, session, true)
	if err != nil {
		return err
	}
	if s.host.sessionCatalog == nil {
		return s.recordFailure(ServiceSessions, "disconnect",
			domainplugin.PermissionSessionsControl, scope, unavailableSessionCatalog())
	}
	if err := s.host.sessionCatalog.Disconnect(context.Background(), id); err != nil {
		return s.recordFailure(ServiceSessions, "disconnect",
			domainplugin.PermissionSessionsControl, scope, err)
	}
	s.mu.Lock()
	delete(s.createdSessions, id)
	s.mu.Unlock()
	s.recordSuccess(ServiceSessions, "disconnect", domainplugin.PermissionSessionsControl, scope)
	return nil
}

// authorizeConnectHost bounds a new Session by the Host the plugin may read.
// When no Host catalog is wired the session adapter itself validates the Host
// id; with a catalog, a Host outside hosts.read is a typed denial.
func (s *ServiceSession) authorizeConnectHost(hostID string, controlGrant domainplugin.PermissionGrant) error {
	if s.host.hostCatalog == nil {
		return nil
	}
	host, ok, err := s.lookupHost(hostID)
	if err != nil {
		return s.recordFailure(ServiceSessions, "connect", domainplugin.PermissionSessionsControl,
			controlGrant.Scope, err)
	}
	if !ok {
		return s.recordFailure(ServiceSessions, "connect", domainplugin.PermissionSessionsControl,
			hostScope(hostID), workspaceNotFound("host does not exist"))
	}
	readGrant, granted := s.host.authority.Granted(s.subject, domainplugin.PermissionHostsRead)
	if !granted || !s.hostVisible(host, readGrant) {
		cause := workspaceDenied("host is outside the granted Host scope")
		return s.recordFailure(ServiceSessions, "connect", domainplugin.PermissionHostsRead,
			hostScope(hostID), cause)
	}
	return nil
}

// checkSession authorizes one fetched Session. Provider-created Sessions and,
// when allowFiber is true, Sessions this fiber created need the permission
// granted but no exact per-session scope. Every other Session needs the exact
// scope, and a denial is audited by check.
func (s *ServiceSession) checkSession(service, method string,
	permission domainplugin.Permission, session SessionSummary, allowFiber bool) (domainplugin.Scope, error) {
	grant, err := s.requireGrant(service, method, permission)
	if err != nil {
		return domainplugin.Scope{}, err
	}
	if session.ProviderCreatedBy != "" && session.ProviderCreatedBy == s.ref.ID {
		return grant.Scope, nil
	}
	if allowFiber && s.fiberCreatedSession(session.ID) {
		return grant.Scope, nil
	}
	scope := sessionScope(session.ID)
	if err := s.check(service, method, permission, scope); err != nil {
		return domainplugin.Scope{}, err
	}
	return scope, nil
}

// sessionAccessible is the list filter. It never returns a Session outside the
// exact scope, provider-created by this plugin, or created by this fiber.
func (s *ServiceSession) sessionAccessible(session SessionSummary, grant domainplugin.PermissionGrant) bool {
	if session.ProviderCreatedBy != "" && session.ProviderCreatedBy == s.ref.ID {
		return true
	}
	if s.fiberCreatedSession(session.ID) {
		return true
	}
	return grant.Scope.Kind == domainplugin.ScopeSession && grant.Scope.Session == session.ID
}

func (s *ServiceSession) fiberCreatedSession(id string) bool {
	if s == nil || id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.createdSessions[id]
	return ok
}

func (s *ServiceSession) lookupSession(id string) (SessionSummary, bool, error) {
	if s.host.sessionCatalog == nil {
		return SessionSummary{}, false, unavailableSessionCatalog()
	}
	return s.host.sessionCatalog.Get(context.Background(), id)
}

func sessionScope(id string) domainplugin.Scope {
	return domainplugin.Scope{Kind: domainplugin.ScopeSession, Session: id}
}

func unavailableSessionCatalog() error {
	return &workspaceFailure{Code: ServiceCodeFault,
		Problem: "session catalog port is not configured", Cause: ErrServiceUnavailable}
}
