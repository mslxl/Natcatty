package plugin

import (
	"fmt"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// workspaceFailure is a typed workspace-service failure raised by path
// enforcement, an adapter, or the application-level permission model. Code is
// one of the closed service response codes; serviceError preserves it.
type workspaceFailure struct {
	Code    string
	Problem string
	Cause   error
}

func (e *workspaceFailure) Error() string {
	if e == nil {
		return "plugin workspace service failure"
	}
	if e.Problem == "" {
		return "plugin workspace service: " + e.Code
	}
	return "plugin workspace service: " + e.Code + ": " + e.Problem
}

func (e *workspaceFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func workspaceErrorf(code, format string, args ...any) *workspaceFailure {
	return &workspaceFailure{Code: code, Problem: fmt.Sprintf(format, args...)}
}

func workspaceDenied(format string, args ...any) *workspaceFailure {
	return workspaceErrorf(ServiceCodeDenied, format, args...)
}

func workspaceInvalid(format string, args ...any) *workspaceFailure {
	return workspaceErrorf(ServiceCodeInvalid, format, args...)
}

func workspaceNotFound(format string, args ...any) *workspaceFailure {
	return workspaceErrorf(ServiceCodeNotFound, format, args...)
}

func workspaceConflict(format string, args ...any) *workspaceFailure {
	return workspaceErrorf(ServiceCodeConflict, format, args...)
}

func workspaceLimit(format string, args ...any) *workspaceFailure {
	return workspaceErrorf(ServiceCodeLimit, format, args...)
}

// requireGrant checks that a permission has any active, non-pending grant. It
// is the enumeration and capability-level check used by methods such as
// hosts.list and sessions.connect; the exact resource check still runs for
// every named resource. A denial is audited and returned as a typed denial.
func (s *ServiceSession) requireGrant(service, method string,
	permission domainplugin.Permission) (domainplugin.PermissionGrant, error) {
	if s == nil || s.host == nil || s.host.authority == nil {
		return domainplugin.PermissionGrant{}, &ServiceError{
			Service: service, Method: method, Code: ServiceCodeFault,
			Permission: permission, Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable,
		}
	}
	if s.isClosed() {
		return domainplugin.PermissionGrant{}, &ServiceError{
			Service: service, Method: method, Code: ServiceCodeInvalid,
			Permission: permission, Message: "the plugin instance is closed",
		}
	}
	grant, ok := s.host.authority.Granted(s.subject, permission)
	if !ok {
		s.record(service, method, permission, domainplugin.Scope{}, ServiceCodeDenied)
		cause := fmt.Errorf("%w: %s", ErrPermissionNotGranted, permission)
		return domainplugin.PermissionGrant{}, &ServiceError{
			Service: service, Method: method, Code: ServiceCodeDenied,
			Permission: permission, Message: cause.Error(), Cause: cause,
		}
	}
	return grant, nil
}

// workspaceFailure converts a port or validation failure into a typed service
// error and appends one audit row carrying its result code. It returns nil for
// a nil error, so call sites can wrap a result directly.
func (s *ServiceSession) recordFailure(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope,
	err error) error {
	if err == nil {
		return nil
	}
	converted := serviceError(service, method, permission, scope, err)
	s.record(service, method, permission, scope, converted.Code)
	return converted
}

// recordSuccess appends one successful audit row. It never records payload
// content, only the service, method, permission, scope, and result.
func (s *ServiceSession) recordSuccess(service, method string,
	permission domainplugin.Permission, scope domainplugin.Scope) {
	s.record(service, method, permission, scope, ServiceCodeOK)
}
