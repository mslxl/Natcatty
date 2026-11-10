package plugin

import (
	"context"
	"errors"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Service method names, shared by the dispatcher and the audit reason.
const (
	serviceMethodSnapshot = "snapshot"
	serviceMethodUpdate   = "update"
)

// PanelSnapshot returns the built-in collectors' normalized output for one
// Host. It requires both system.panel and a Host-scoped sessions.read: the
// reply exposes process, port, and service data, so the panel contribution
// permission alone is not enough. The plugin never names a command.
func (s *ServiceSession) PanelSnapshot(ctx context.Context, hostID string,
	collectors []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, error) {
	const service, method = ServiceSystemPanel, serviceMethodSnapshot
	if s == nil {
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: service, Method: method,
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error(),
			Cause: ErrServiceUnavailable}
	}
	if err := s.check(service, method, domainplugin.PermissionSystemPanel,
		domainplugin.Scope{}); err != nil {
		return domainplugin.PanelSnapshot{}, err
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeSession, Host: hostID}
	if err := s.check(service, method, domainplugin.PermissionSessionsRead, scope); err != nil {
		return domainplugin.PanelSnapshot{}, err
	}
	if s.host.panel == nil {
		return domainplugin.PanelSnapshot{}, &ServiceError{Service: service, Method: method,
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error(),
			Cause: ErrServiceUnavailable}
	}
	snapshot, err := s.host.panel.Snapshot(ctx, hostID, collectors)
	if err != nil {
		return domainplugin.PanelSnapshot{}, s.fail(service, method,
			domainplugin.PermissionSessionsRead, scope,
			serviceFailureCode(err), serviceError(service, method,
				domainplugin.PermissionSessionsRead, scope, err))
	}
	s.record(service, method, domainplugin.PermissionSessionsRead, scope, ServiceCodeOK)
	return snapshot, nil
}

// PanelUpdate publishes one section payload. It requires only system.panel:
// the payload came from the plugin's own data sources, not the host's
// collectors. Host targeting, section ownership, descriptor validity, and the
// push cadence are enforced by the coordinator.
func (s *ServiceSession) PanelUpdate(hostID, sectionID string, raw []byte) error {
	const service, method = ServiceSystemPanel, serviceMethodUpdate
	if s == nil {
		return &ServiceError{Service: service, Method: method,
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error(),
			Cause: ErrServiceUnavailable}
	}
	if err := s.check(service, method, domainplugin.PermissionSystemPanel,
		domainplugin.Scope{}); err != nil {
		return err
	}
	if s.host.panel == nil {
		return &ServiceError{Service: service, Method: method,
			Code: ServiceCodeFault, Message: ErrServiceUnavailable.Error(),
			Cause: ErrServiceUnavailable}
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: hostID}
	if err := s.host.panel.Update(s.ref, hostID, sectionID, raw); err != nil {
		return s.fail(service, method, domainplugin.PermissionSystemPanel, scope,
			serviceFailureCode(err), serviceError(service, method,
				domainplugin.PermissionSystemPanel, scope, err))
	}
	s.record(service, method, domainplugin.PermissionSystemPanel, scope, ServiceCodeOK)
	return nil
}

// serviceFailureCode extracts a stable audit result from a typed service
// error, defaulting to fault for host-side failures.
func serviceFailureCode(err error) string {
	var service *ServiceError
	if errors.As(err, &service) && service.Code != "" {
		return service.Code
	}
	return ServiceCodeFault
}
