package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Package is a validated plugin package ready to install. Files is keyed by
// slash-separated package-relative paths and includes plugin.toml and the
// module. The installer writes exactly these files.
type Package struct {
	Manifest domainplugin.Manifest
	Files    map[string][]byte
	SHA256   string
}

// PackageValidator validates a .natplug archive or unpacked directory and
// returns the files and manifest. Validation is structural only; no guest code
// runs.
type PackageValidator interface {
	Validate(ctx context.Context, path string) (Package, error)
}

// ModuleValidator checks a WebAssembly module's exports, imports, memory
// maximum, and forbidden features without executing it.
type ModuleValidator interface {
	ValidateModule(ctx context.Context, wasm []byte, memoryMaxBytes int64) error
}

// ModuleValidationError reports a structural module rule the installer
// rejected. It is stable enough for the plugin manager UI to show the
// problem without matching wazero internals.
type ModuleValidationError struct {
	Problem string
}

func (e *ModuleValidationError) Error() string {
	return "plugin module: " + e.Problem
}

// InstallOptions carries the install source metadata. TrustedSource is the
// source identity pinned by trust-on-first-use; it is independent of the
// package hash and survives version changes.
type InstallOptions struct {
	Source        domainplugin.Source
	TrustedSource string
}

// PackageStore owns the versioned install layout under the user-data
// directory. Implementations must install transactionally and make the end
// state installed-but-disabled.
type PackageStore interface {
	// Install validates the id is not already installed, extracts into
	// staging, atomically renames into the version directory, and writes
	// install.json with Enabled false.
	Install(ctx context.Context, pkg Package, options InstallOptions,
		now time.Time) (domainplugin.InstallRecord, error)
	// Record returns one install record, or false when the id is absent.
	Record(ctx context.Context, id string) (domainplugin.InstallRecord, bool, error)
	// Records returns every install record in id order.
	Records(ctx context.Context) ([]domainplugin.InstallRecord, error)
	// SaveRecord rewrites install.json atomically.
	SaveRecord(ctx context.Context, record domainplugin.InstallRecord) error
	// PromoteVersion atomically replaces the current package of an existing id
	// with pkg. It retains the replaced version as the record's Previous and
	// removes any older retained version. record supplies the new version's
	// metadata; ID and Version must match pkg. On any failure the previous
	// version and install.json remain in place.
	PromoteVersion(ctx context.Context, pkg Package, record domainplugin.InstallRecord,
		now time.Time) (domainplugin.InstallRecord, error)
	// RestorePrevious swaps the current and previous versions and their
	// metadata. It fails when there is no retained previous version.
	RestorePrevious(ctx context.Context, id string, now time.Time) (domainplugin.InstallRecord, error)
	// Remove deletes the package and install metadata. Data is kept unless
	// purge is true; Purging is also available on its own.
	Remove(ctx context.Context, id string, purge bool) error
	// PurgeData deletes the version-independent plugin data directory.
	PurgeData(ctx context.Context, id string) error
	// PackageDir returns the installed version directory.
	PackageDir(ctx context.Context, id string) (string, error)
}

// Registry is the read-only static plugin registry. Identity is the stable
// source identity used for trust-on-first-use pinning; it is not a signature.
type Registry interface {
	Identity() string
	Index(ctx context.Context) (domainplugin.RegistryIndex, error)
	Plugin(ctx context.Context, id string) (domainplugin.RegistryPlugin, error)
	FetchPackage(ctx context.Context, id, version string) ([]byte, error)
}

// TrustStore persists trust-on-first-use source pins per plugin id. A pin is
// recorded after the first successful install from a source and never expires
// silently; a different source requires explicit confirmation.
type TrustStore interface {
	SourcePin(ctx context.Context, id string) (domainplugin.SourcePin, bool, error)
	SaveSourcePin(ctx context.Context, pin domainplugin.SourcePin) error
}

// PreferencesStore persists registry policy. Automatic updates are off by
// default and beta is opt-in per plugin.
type PreferencesStore interface {
	RegistryPreferences(ctx context.Context) (domainplugin.RegistryPreferences, error)
	SaveRegistryPreferences(ctx context.Context, preferences domainplugin.RegistryPreferences) error
}

// AuditSink receives lifecycle and permission events. Implementations append;
// they must never block a security decision on disk I/O. The manager never
// sends payload content.
type AuditSink interface {
	Record(event domainplugin.AuditEvent)
	Close() error
}

// NopAudit discards audit events. It is the disabled default for tests and
// for a composition that has no audit directory.
type NopAudit struct{}

// Record implements AuditSink.
func (NopAudit) Record(domainplugin.AuditEvent) {}

// Close implements AuditSink.
func (NopAudit) Close() error { return nil }

// ErrAlreadyInstalled reports an install for an id that already has a version.
var ErrAlreadyInstalled = errors.New("plugin: already installed")

// ErrNotInstalled reports an action for an unknown plugin id.
var ErrNotInstalled = errors.New("plugin: not installed")

// ErrNoUpdateAvailable reports that the selected channel has no newer
// compatible, non-yanked version.
var ErrNoUpdateAvailable = errors.New("plugin: no update available")

// ErrNoPreviousVersion reports a rollback with no retained version.
var ErrNoPreviousVersion = errors.New("plugin: no previous version retained")

// ErrStaleUpdatePlan reports an update or rollback plan computed for a
// different current version. The host never applies a stale permission diff.
var ErrStaleUpdatePlan = errors.New("plugin: update plan is stale")

// SourceChangedError reports a trust-on-first-use pin for a different source.
// The caller must confirm the source change explicitly.
type SourceChangedError struct {
	ID           string
	PinnedSource string
	NewSource    string
}

func (e *SourceChangedError) Error() string {
	return fmt.Sprintf("plugin: source for %s changed from %q to %q; explicit confirmation is required",
		e.ID, e.PinnedSource, e.NewSource)
}

// YankedVersionError reports a yanked version selected for a new install or
// update. Existing installs keep running; the version is only hidden from
// selection.
type YankedVersionError struct {
	ID      string
	Version string
}

func (e *YankedVersionError) Error() string {
	return fmt.Sprintf("plugin: %s %s is yanked and cannot be installed or updated to", e.ID, e.Version)
}

// VersionError reports an unknown or incompatible registry version.
type VersionError struct {
	ID      string
	Version string
	Problem string
}

func (e *VersionError) Error() string {
	if e.Version == "" {
		return fmt.Sprintf("plugin: %s: %s", e.ID, e.Problem)
	}
	return fmt.Sprintf("plugin: %s %s: %s", e.ID, e.Version, e.Problem)
}

// DependencyError reports a dependency that cannot be resolved.
type DependencyError struct {
	ID      string
	Problem string
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("plugin: dependency %s: %s", e.ID, e.Problem)
}

// DependentsError reports an uninstall blocked by enabled dependents.
type DependentsError struct {
	ID         string
	Dependents []string
}

func (e *DependentsError) Error() string {
	return fmt.Sprintf("plugin: %s is required by enabled dependents: %s",
		e.ID, strings.Join(e.Dependents, ", "))
}

// DependentConstraint is one enabled dependent whose declared dependency range
// does not contain a proposed version.
type DependentConstraint struct {
	ID    string
	Range string
}

// DependentsConstraintError reports a version change blocked because an enabled
// installed dependent declares a range that the proposed version does not
// satisfy. Disabling the dependent or choosing a compatible version is the
// only way through; the host never leaves enabled plugins with an unsatisfied
// dependency.
type DependentsConstraintError struct {
	ID         string
	Version    string
	Dependents []DependentConstraint
}

func (e *DependentsConstraintError) Error() string {
	parts := make([]string, 0, len(e.Dependents))
	for _, dependent := range e.Dependents {
		parts = append(parts, dependent.ID+" requires "+dependent.Range)
	}
	return fmt.Sprintf("plugin: %s %s is outside the range of enabled dependents: %s",
		e.ID, e.Version, strings.Join(parts, ", "))
}

// ErrAlreadyEnabled reports enabling an enabled plugin.
var ErrAlreadyEnabled = errors.New("plugin: already enabled")

// ErrNotEnabled reports disabling or revoking a required permission of a
// disabled plugin.
var ErrNotEnabled = errors.New("plugin: not enabled")

// ErrOptionalNotDeclared reports a first-use request for a permission the
// manifest did not declare optional.
var ErrOptionalNotDeclared = errors.New("plugin: permission is not declared optional")

// PermissionDeniedError reports that the user denied a required permission.
// The plugin stays installed and inactive with Reason set.
type PermissionDeniedError struct {
	Permission domainplugin.Permission
}

func (e *PermissionDeniedError) Error() string {
	return "plugin: required permission denied: " + string(e.Permission)
}

// OptionalDeniedError reports a denied first-use optional permission request.
// The plugin stays active.
type OptionalDeniedError struct {
	Permission domainplugin.Permission
}

func (e *OptionalDeniedError) Error() string {
	return "plugin: optional permission denied: " + string(e.Permission)
}

// InvalidRequestError reports a decision list that omits a required
// permission or names one the manifest never requested.
type InvalidRequestError struct {
	Permission domainplugin.Permission
	Problem    string
}

func (e *InvalidRequestError) Error() string {
	if e.Permission == "" {
		return "plugin: " + e.Problem
	}
	return fmt.Sprintf("plugin: permission %s: %s", e.Permission, e.Problem)
}
