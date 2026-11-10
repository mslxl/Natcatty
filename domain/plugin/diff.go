package plugin

// PermissionScopeChange is one permission whose declared resource scope moved
// between versions. Any scope change requires fresh consent: an expansion is
// never silent and a narrowing still needs the new scope confirmed.
type PermissionScopeChange struct {
	Permission Permission
	Required   bool
	Before     Scope
	After      Scope
}

// PermissionDiff is the permission delta an update or rollback shows before it
// can activate. Removed permissions are revoked automatically; added required
// permissions keep the updated plugin inactive until consented.
type PermissionDiff struct {
	AddedRequired   []PermissionRequest
	AddedOptional   []PermissionRequest
	RemovedRequired []PermissionRequest
	RemovedOptional []PermissionRequest
	ScopeChanged    []PermissionScopeChange
}

// Empty reports whether the two versions request the same permissions with the
// same scopes.
func (d PermissionDiff) Empty() bool {
	return len(d.AddedRequired) == 0 && len(d.AddedOptional) == 0 &&
		len(d.RemovedRequired) == 0 && len(d.RemovedOptional) == 0 &&
		len(d.ScopeChanged) == 0
}

// RequiringConsent returns the required permissions that need a fresh decision
// before the target version may activate: newly required permissions and
// required permissions whose scope changed. An unchanged required permission
// keeps its existing grant.
func (d PermissionDiff) RequiringConsent() []PermissionRequest {
	out := make([]PermissionRequest, 0, len(d.AddedRequired)+len(d.ScopeChanged))
	out = append(out, d.AddedRequired...)
	for _, change := range d.ScopeChanged {
		if change.Required {
			out = append(out, PermissionRequest{Permission: change.Permission, Scope: change.After})
		}
	}
	return out
}

// DiffPermissions compares the declared permission requests of two versions.
// A permission moving from optional to required is removed from the optional
// delta and added to the required delta; a required permission that becomes
// optional keeps its row optional only after an explicit fresh grant, so it is
// reported in both deltas and revokes the old required grant.
func DiffPermissions(oldRequired, oldOptional, newRequired, newOptional []PermissionRequest) PermissionDiff {
	oldAll := permissionRequestIndex(oldRequired, oldOptional)
	newRequiredIDs := permissionRequestKeySet(newRequired)
	newOptionalIDs := permissionRequestKeySet(newOptional)
	oldRequiredIDs := permissionRequestKeySet(oldRequired)
	oldOptionalIDs := permissionRequestKeySet(oldOptional)

	var diff PermissionDiff
	for _, request := range newRequired {
		previous, existed := oldAll[request.Permission]
		switch {
		case !existed:
			diff.AddedRequired = append(diff.AddedRequired, request)
		case !oldRequiredIDs[request.Permission]:
			diff.AddedRequired = append(diff.AddedRequired, request)
		case !previous.Scope.Equal(request.Scope):
			diff.ScopeChanged = append(diff.ScopeChanged, PermissionScopeChange{
				Permission: request.Permission, Required: true,
				Before: previous.Scope, After: request.Scope,
			})
		}
	}
	for _, request := range newOptional {
		previous, existed := oldAll[request.Permission]
		switch {
		case !existed:
			diff.AddedOptional = append(diff.AddedOptional, request)
		case !oldOptionalIDs[request.Permission]:
			// Moving required to optional is a permission change; the row is
			// shown again and the previous required grant is dropped.
			diff.AddedOptional = append(diff.AddedOptional, request)
		case !previous.Scope.Equal(request.Scope):
			diff.ScopeChanged = append(diff.ScopeChanged, PermissionScopeChange{
				Permission: request.Permission, Required: false,
				Before: previous.Scope, After: request.Scope,
			})
		}
	}
	for _, request := range oldRequired {
		if !newRequiredIDs[request.Permission] {
			diff.RemovedRequired = append(diff.RemovedRequired, request)
		}
	}
	for _, request := range oldOptional {
		if !newOptionalIDs[request.Permission] {
			diff.RemovedOptional = append(diff.RemovedOptional, request)
		}
	}
	return diff
}

func permissionRequestIndex(required, optional []PermissionRequest) map[Permission]PermissionRequest {
	out := make(map[Permission]PermissionRequest, len(required)+len(optional))
	for _, request := range required {
		out[request.Permission] = request
	}
	for _, request := range optional {
		out[request.Permission] = request
	}
	return out
}

func permissionRequestKeySet(requests []PermissionRequest) map[Permission]bool {
	out := make(map[Permission]bool, len(requests))
	for _, request := range requests {
		out[request.Permission] = true
	}
	return out
}
