package plugin

import (
	"context"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// maxHostImport bounds one hosts.import call. Import is the only bulk
// mutation in the service, so a single call cannot create an unbounded number
// of Vault records.
const maxHostImport = 256

// HostsList returns the Hosts this plugin may read: the exact Host named by
// its hosts.read grant, plus Hosts it created through the service. Secrets are
// never part of a HostSummary.
func (s *ServiceSession) HostsList() ([]HostSummary, error) {
	grant, err := s.requireGrant(ServiceHosts, "list", domainplugin.PermissionHostsRead)
	if err != nil {
		return nil, err
	}
	if s.host.hostCatalog == nil {
		return nil, s.recordFailure(ServiceHosts, "list", domainplugin.PermissionHostsRead,
			grant.Scope, unavailableHostCatalog())
	}
	hosts, err := s.host.hostCatalog.List(context.Background())
	if err != nil {
		return nil, s.recordFailure(ServiceHosts, "list", domainplugin.PermissionHostsRead, grant.Scope, err)
	}
	out := make([]HostSummary, 0, len(hosts))
	for _, host := range hosts {
		if s.hostVisible(host, grant) {
			out = append(out, cloneHostSummary(host))
		}
	}
	s.recordSuccess(ServiceHosts, "list", domainplugin.PermissionHostsRead, grant.Scope)
	return out, nil
}

// HostsGet returns one Host this plugin may read. A Host outside the grant and
// not created by this plugin is a typed denial, never a silent empty result.
func (s *ServiceSession) HostsGet(id string) (HostSummary, error) {
	if strings.TrimSpace(id) == "" {
		return HostSummary{}, s.recordFailure(ServiceHosts, "get",
			domainplugin.PermissionHostsRead, domainplugin.Scope{}, workspaceInvalid("host id must not be empty"))
	}
	// Check the capability before looking the Host up, so an unprivileged
	// caller cannot use the not-found reply as an existence oracle.
	if _, err := s.requireGrant(ServiceHosts, "get", domainplugin.PermissionHostsRead); err != nil {
		return HostSummary{}, err
	}
	host, ok, err := s.lookupHost(id)
	if err != nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, "get",
			domainplugin.PermissionHostsRead, hostScope(id), err)
	}
	if !ok {
		return HostSummary{}, s.recordFailure(ServiceHosts, "get",
			domainplugin.PermissionHostsRead, hostScope(id), workspaceNotFound("host does not exist"))
	}
	scope, err := s.checkHost(ServiceHosts, "get", domainplugin.PermissionHostsRead, host)
	if err != nil {
		return HostSummary{}, err
	}
	s.recordSuccess(ServiceHosts, "get", domainplugin.PermissionHostsRead, scope)
	return cloneHostSummary(host), nil
}

// HostsCreate creates one Host owned by the calling plugin. The persisted
// provenance lets the plugin continue to read, test, and update it without a
// second Host grant; every other plugin still needs the exact Host scope.
func (s *ServiceSession) HostsCreate(draft HostDraft) (HostSummary, error) {
	grant, err := s.requireGrant(ServiceHosts, "create", domainplugin.PermissionHostsWrite)
	if err != nil {
		return HostSummary{}, err
	}
	return s.createHost(grant, "create", draft)
}

// HostsUpdate applies a partial change to a Host this plugin may write. A Host
// outside the write grant and not created by this plugin is denied.
func (s *ServiceSession) HostsUpdate(id string, patch HostPatch) (HostSummary, error) {
	if strings.TrimSpace(id) == "" {
		return HostSummary{}, s.recordFailure(ServiceHosts, "update",
			domainplugin.PermissionHostsWrite, domainplugin.Scope{}, workspaceInvalid("host id must not be empty"))
	}
	if _, err := s.requireGrant(ServiceHosts, "update", domainplugin.PermissionHostsWrite); err != nil {
		return HostSummary{}, err
	}
	host, ok, err := s.lookupHost(id)
	if err != nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, "update",
			domainplugin.PermissionHostsWrite, hostScope(id), err)
	}
	if !ok {
		return HostSummary{}, s.recordFailure(ServiceHosts, "update",
			domainplugin.PermissionHostsWrite, hostScope(id), workspaceNotFound("host does not exist"))
	}
	scope, err := s.checkHost(ServiceHosts, "update", domainplugin.PermissionHostsWrite, host)
	if err != nil {
		return HostSummary{}, err
	}
	if s.host.hostCatalog == nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, "update",
			domainplugin.PermissionHostsWrite, scope, unavailableHostCatalog())
	}
	updated, err := s.host.hostCatalog.Update(context.Background(), id, patch)
	if err != nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, "update",
			domainplugin.PermissionHostsWrite, scope, err)
	}
	s.recordSuccess(ServiceHosts, "update", domainplugin.PermissionHostsWrite, scope)
	return cloneHostSummary(updated), nil
}

// HostsImport creates several Hosts in one call. Every created Host carries
// the calling plugin's provenance. The response preserves input order.
func (s *ServiceSession) HostsImport(drafts []HostDraft) ([]HostSummary, error) {
	grant, err := s.requireGrant(ServiceHosts, "import", domainplugin.PermissionHostsWrite)
	if err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, s.recordFailure(ServiceHosts, "import", domainplugin.PermissionHostsWrite,
			grant.Scope, workspaceInvalid("import requires at least one host"))
	}
	if len(drafts) > maxHostImport {
		return nil, s.recordFailure(ServiceHosts, "import", domainplugin.PermissionHostsWrite,
			grant.Scope, workspaceLimit("import accepts at most %d hosts", maxHostImport))
	}
	if s.host.hostCatalog == nil {
		return nil, s.recordFailure(ServiceHosts, "import", domainplugin.PermissionHostsWrite,
			grant.Scope, unavailableHostCatalog())
	}
	internal := make([]HostDraft, 0, len(drafts))
	for _, draft := range drafts {
		draft.CreatedBy = s.ref.ID
		internal = append(internal, draft)
	}
	imported, err := s.host.hostCatalog.Import(context.Background(), internal)
	if err != nil {
		return nil, s.recordFailure(ServiceHosts, "import", domainplugin.PermissionHostsWrite,
			grant.Scope, err)
	}
	out := make([]HostSummary, 0, len(imported))
	for _, host := range imported {
		out = append(out, cloneHostSummary(host))
	}
	s.recordSuccess(ServiceHosts, "import", domainplugin.PermissionHostsWrite, grant.Scope)
	return out, nil
}

// HostsTest probes one Host this plugin may read. The result carries only
// reachability and latency; credentials never enter the guest response or an
// error message.
func (s *ServiceSession) HostsTest(id string) (HostTestResult, error) {
	if strings.TrimSpace(id) == "" {
		return HostTestResult{}, s.recordFailure(ServiceHosts, "test",
			domainplugin.PermissionHostsRead, domainplugin.Scope{}, workspaceInvalid("host id must not be empty"))
	}
	if _, err := s.requireGrant(ServiceHosts, "test", domainplugin.PermissionHostsRead); err != nil {
		return HostTestResult{}, err
	}
	host, ok, err := s.lookupHost(id)
	if err != nil {
		return HostTestResult{}, s.recordFailure(ServiceHosts, "test",
			domainplugin.PermissionHostsRead, hostScope(id), err)
	}
	if !ok {
		return HostTestResult{}, s.recordFailure(ServiceHosts, "test",
			domainplugin.PermissionHostsRead, hostScope(id), workspaceNotFound("host does not exist"))
	}
	scope, err := s.checkHost(ServiceHosts, "test", domainplugin.PermissionHostsRead, host)
	if err != nil {
		return HostTestResult{}, err
	}
	if s.host.hostCatalog == nil {
		return HostTestResult{}, s.recordFailure(ServiceHosts, "test",
			domainplugin.PermissionHostsRead, scope, unavailableHostCatalog())
	}
	result, err := s.host.hostCatalog.Test(context.Background(), id)
	if err != nil {
		return HostTestResult{}, s.recordFailure(ServiceHosts, "test",
			domainplugin.PermissionHostsRead, scope, err)
	}
	s.recordSuccess(ServiceHosts, "test", domainplugin.PermissionHostsRead, scope)
	return result, nil
}

func (s *ServiceSession) createHost(grant domainplugin.PermissionGrant, method string,
	draft HostDraft) (HostSummary, error) {
	if strings.TrimSpace(draft.Hostname) == "" {
		return HostSummary{}, s.recordFailure(ServiceHosts, method,
			domainplugin.PermissionHostsWrite, grant.Scope, workspaceInvalid("host hostname must not be empty"))
	}
	if s.host.hostCatalog == nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, method,
			domainplugin.PermissionHostsWrite, grant.Scope, unavailableHostCatalog())
	}
	draft.CreatedBy = s.ref.ID
	host, err := s.host.hostCatalog.Create(context.Background(), draft)
	if err != nil {
		return HostSummary{}, s.recordFailure(ServiceHosts, method,
			domainplugin.PermissionHostsWrite, grant.Scope, err)
	}
	s.recordSuccess(ServiceHosts, method, domainplugin.PermissionHostsWrite, grant.Scope)
	return cloneHostSummary(host), nil
}

// lookupHost fetches one Host without an authorization check. Callers must
// authorize before returning any field to the guest.
func (s *ServiceSession) lookupHost(id string) (HostSummary, bool, error) {
	if s.host.hostCatalog == nil {
		return HostSummary{}, false, unavailableHostCatalog()
	}
	return s.host.hostCatalog.Get(context.Background(), id)
}

// checkHost authorizes one fetched Host. Plugin-created Hosts need the
// permission granted but no exact Host scope; every other Host needs the exact
// scope. A denial is audited by check.
func (s *ServiceSession) checkHost(service, method string,
	permission domainplugin.Permission, host HostSummary) (domainplugin.Scope, error) {
	grant, err := s.requireGrant(service, method, permission)
	if err != nil {
		return domainplugin.Scope{}, err
	}
	if host.CreatedBy != "" && host.CreatedBy == s.ref.ID {
		return grant.Scope, nil
	}
	scope := hostScope(host.ID)
	if err := s.check(service, method, permission, scope); err != nil {
		return domainplugin.Scope{}, err
	}
	return scope, nil
}

// hostVisible is the list filter. It never returns a Host outside the exact
// grant or created by this plugin.
func (s *ServiceSession) hostVisible(host HostSummary, grant domainplugin.PermissionGrant) bool {
	if host.CreatedBy != "" && host.CreatedBy == s.ref.ID {
		return true
	}
	return grant.Scope.Kind == domainplugin.ScopeHost && grant.Scope.Host == host.ID
}

func unavailableHostCatalog() error {
	return &workspaceFailure{Code: ServiceCodeFault,
		Problem: "host catalog port is not configured", Cause: ErrServiceUnavailable}
}
