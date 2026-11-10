package plugin

import (
	"sort"
	"strings"
	"time"
)

// Source identifies where a plugin came from. The consent screen and the
// plugin list label every plugin with its source; a development directory is
// never reported as a registry-installed package.
type Source string

const (
	// SourceLocalFile is a package side-loaded from the user's filesystem.
	SourceLocalFile Source = "local-file"
	// SourceRegistry is a package downloaded from the static registry.
	SourceRegistry Source = "registry"
	// SourceDevelopment is an unpacked directory loaded in developer mode.
	SourceDevelopment Source = "development"
	// SourceBuiltIn is a first-party plugin compiled into the app.
	SourceBuiltIn Source = "built-in"
)

// Valid reports whether s is a defined source.
func (s Source) Valid() bool {
	switch s {
	case SourceLocalFile, SourceRegistry, SourceDevelopment, SourceBuiltIn:
		return true
	default:
		return false
	}
}

// Install record reason codes. A denial or revocation is stored as a stable
// code plus the permission name so the UI localizes it; application code does
// not build user-facing copy (ADR-0009).
const (
	ReasonPermissionDenied  = "permission-denied"
	ReasonPermissionRevoked = "permission-revoked"
	// ReasonConfigInvalid marks a plugin that is enabled but inactive because
	// its stored configuration does not validate. The reason carries the
	// first field error for the plugin list; the diagnostics view carries the
	// full SettingsValidationError.
	ReasonConfigInvalid = "config-invalid"
	// ReasonInjectionMissing marks a plugin that is enabled but waiting for a
	// required injected service. The reason names the missing services.
	ReasonInjectionMissing = "injection-missing"
	// Cross-plugin service PENDING codes. Each names the condition that kept a
	// required injection from binding; the detail lists the services.
	ReasonServiceMissing         = "service-missing"
	ReasonServiceVersionMismatch = "service-version-mismatch"
	ReasonServiceConflict        = "service-conflict"
)

// ServiceInjectionReason formats a cross-plugin service PENDING reason. The
// services are sorted so the reason is deterministic.
func ServiceInjectionReason(code string, services []string) string {
	if len(services) == 0 {
		return code
	}
	names := append([]string(nil), services...)
	sort.Strings(names)
	return code + ": " + strings.Join(names, ",")
}

// ParseServiceInjectionReason splits a cross-plugin service reason into its
// closed code and the service names it names.
func ParseServiceInjectionReason(reason string) (code string, services []string, ok bool) {
	code, detail, found := strings.Cut(reason, ": ")
	switch code {
	case ReasonServiceMissing, ReasonServiceVersionMismatch, ReasonServiceConflict,
		ReasonPermissionDenied:
	default:
		return "", nil, false
	}
	if !found || detail == "" {
		return code, nil, true
	}
	return code, strings.Split(detail, ","), true
}

// ConfigReason formats a stored configuration reason. The detail is the
// readable validation error, not user content.
func ConfigReason(detail string) string {
	if detail == "" {
		return ReasonConfigInvalid
	}
	return ReasonConfigInvalid + ": " + detail
}

// PermissionReason formats a permission reason code for an install record.
func PermissionReason(code string, permission Permission) string {
	return code + ":" + string(permission)
}

// ParsePermissionReason splits a stored permission reason code. It returns
// false for reasons that are not permission codes, such as an activation
// error.
func ParsePermissionReason(reason string) (code string, permission Permission, ok bool) {
	code, name, found := strings.Cut(reason, ":")
	if !found || name == "" {
		return "", "", false
	}
	switch code {
	case ReasonPermissionDenied, ReasonPermissionRevoked:
		return code, Permission(name), true
	default:
		return "", "", false
	}
}

// PermissionRequest is one manifest permission request. A resource-scoped
// permission must declare a concrete Scope; the manifest validator rejects a
// missing or mismatched scope. First-use consent reuses that declared scope.
type PermissionRequest struct {
	Permission Permission `json:"permission" toml:"-"`
	Scope      Scope      `json:"scope,omitempty" toml:"-"`
}

// PermissionConsent is one row of the enable consent screen.
type PermissionConsent struct {
	Permission Permission `json:"permission"`
	Scope      Scope      `json:"scope,omitempty"`
	Required   bool       `json:"required"`
}

// ConsentReview is the read-only consent screen content for one package. It
// names the package source so a local unverified file is never mistaken for a
// registry install.
type ConsentReview struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Source   Source              `json:"source"`
	Required []PermissionConsent `json:"required"`
	Optional []PermissionConsent `json:"optional"`
}

// PermissionDecision is the user's answer for one permission row. Remember
// only applies to optional permissions and stores the grant for the same
// scope, so the next use does not prompt again.
type PermissionDecision struct {
	Permission Permission `json:"permission"`
	Scope      Scope      `json:"scope,omitempty"`
	Allow      bool       `json:"allow"`
	Remember   bool       `json:"remember,omitempty"`
}

// PermissionGrant is one active or remembered authority grant.
type PermissionGrant struct {
	Permission Permission `json:"permission"`
	Scope      Scope      `json:"scope,omitempty"`
	Optional   bool       `json:"optional,omitempty"`
	// Pending is a defensive state for install records written by older builds
	// that allowed scope-pending consent. Current manifests require a declared
	// scope, so the manager never creates a pending grant; the authority still
	// rejects one and it authorizes nothing.
	Pending   bool      `json:"pending,omitempty"`
	GrantedAt time.Time `json:"granted_at,omitempty"`
}

// RememberedGrant is an optional permission the user allowed for one scope
// and asked the host to remember.
type RememberedGrant struct {
	Permission Permission `json:"permission"`
	Scope      Scope      `json:"scope,omitempty"`
	GrantedAt  time.Time  `json:"granted_at,omitempty"`
}

// InstallRecord is the host-owned install metadata stored beside the versioned
// package directory. It never lives inside the package.
type InstallRecord struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Source        Source    `json:"source"`
	PackageSHA256 string    `json:"package_sha256"`
	InstalledAt   time.Time `json:"installed_at"`
	Enabled       bool      `json:"enabled"`
	Reason        string    `json:"reason,omitempty"`
	// TrustedSource is the trust-on-first-use pin for the plugin id. A source
	// change requires explicit confirmation before it is replaced.
	TrustedSource string              `json:"trusted_source,omitempty"`
	Required      []PermissionRequest `json:"required"`
	Optional      []PermissionRequest `json:"optional"`
	Dependencies  []Dependency        `json:"dependencies,omitempty"`
	Grants        []PermissionGrant   `json:"grants,omitempty"`
	Remembered    []RememberedGrant   `json:"remembered,omitempty"`
	// Previous is the one retained version that rollback restores.
	Previous *PreviousInstall `json:"previous,omitempty"`
}

// PreviousInstall is the retained version metadata for rollback. Grants are
// retained so a rollback can restore permissions the update revoked, subject
// to the same permission diff and consent rules as an update.
type PreviousInstall struct {
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	PackageSHA256 string              `json:"package_sha256"`
	Required      []PermissionRequest `json:"required"`
	Optional      []PermissionRequest `json:"optional"`
	Dependencies  []Dependency        `json:"dependencies,omitempty"`
	Grants        []PermissionGrant   `json:"grants,omitempty"`
	Remembered    []RememberedGrant   `json:"remembered,omitempty"`
}

// DependsOn reports whether the record declares at least one dependency on id.
func (r InstallRecord) DependsOn(id string) bool {
	for _, dependency := range r.Dependencies {
		if dependency.ID == id {
			return true
		}
	}
	return false
}

// SatisfiesDependency reports whether every dependency on id from this record
// is satisfied by version.
func (r InstallRecord) SatisfiesDependency(id, version string) bool {
	parsed, ok := ParseSemver(version)
	if !ok {
		return false
	}
	found := false
	for _, dependency := range r.Dependencies {
		if dependency.ID != id {
			continue
		}
		found = true
		if !RangeContains(dependency.Version, parsed) {
			return false
		}
	}
	return found
}

// Grant returns the grant for permission, if any.
func (r InstallRecord) Grant(permission Permission) (PermissionGrant, bool) {
	for _, grant := range r.Grants {
		if grant.Permission == permission {
			return grant, true
		}
	}
	return PermissionGrant{}, false
}

// HasScopeGrant reports whether the record grants permission for exactly
// scope. A pending grant authorizes nothing.
func (r InstallRecord) HasScopeGrant(permission Permission, scope Scope) bool {
	grant, ok := r.Grant(permission)
	return ok && !grant.Pending && grant.Scope.Equal(scope)
}

// SetGrant inserts or replaces one grant.
func (r *InstallRecord) SetGrant(grant PermissionGrant) {
	for i := range r.Grants {
		if r.Grants[i].Permission == grant.Permission {
			r.Grants[i] = grant
			return
		}
	}
	r.Grants = append(r.Grants, grant)
}

// RemoveGrant drops one permission grant.
func (r *InstallRecord) RemoveGrant(permission Permission) {
	for i := range r.Grants {
		if r.Grants[i].Permission == permission {
			r.Grants = append(r.Grants[:i], r.Grants[i+1:]...)
			return
		}
	}
}

// Remember stores an optional grant for first-use reuse.
func (r *InstallRecord) Remember(grant RememberedGrant) {
	for _, existing := range r.Remembered {
		if existing.Permission == grant.Permission && existing.Scope.Equal(grant.Scope) {
			return
		}
	}
	r.Remembered = append(r.Remembered, grant)
}

// WasRemembered reports whether an optional grant was remembered for scope.
func (r InstallRecord) WasRemembered(permission Permission, scope Scope) bool {
	for _, grant := range r.Remembered {
		if grant.Permission == permission && grant.Scope.Equal(scope) {
			return true
		}
	}
	return false
}

// Request evaluates the review rows for one package.
func (r InstallRecord) Request() (required, optional []PermissionRequest) {
	return append([]PermissionRequest(nil), r.Required...), append([]PermissionRequest(nil), r.Optional...)
}

// ConsentReview builds the consent screen model for one installed record.
func (r InstallRecord) ConsentReview() ConsentReview {
	review := ConsentReview{
		ID:      r.ID,
		Name:    r.Name,
		Version: r.Version,
		Source:  r.Source,
	}
	for _, request := range r.Required {
		review.Required = append(review.Required, PermissionConsent{
			Permission: request.Permission, Scope: request.Scope, Required: true,
		})
	}
	for _, request := range r.Optional {
		review.Optional = append(review.Optional, PermissionConsent{
			Permission: request.Permission, Scope: request.Scope,
		})
	}
	return review
}
