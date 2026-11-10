package plugin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// RegistrySearchResult is one client-side registry search row enriched with
// the host's installed state and selected channel.
type RegistrySearchResult struct {
	ID               string
	Name             string
	Description      string
	Keywords         []string
	Channel          domainplugin.Channel
	LatestVersion    string
	Installed        bool
	InstalledVersion string
	Warnings         []domainplugin.ReleaseWarning
}

// SourceChange is one plugin id, root or dependency, whose trust-on-first-use
// pin differs from the source a planned install or update would use.
type SourceChange struct {
	ID           string
	PinnedSource string
	NewSource    string
}

// ConfirmedSourceChanges is the set of plugin ids whose source change the user
// explicitly confirmed after seeing it. A nil set confirms nothing, and a
// confirmation for one id never covers another.
type ConfirmedSourceChanges map[string]bool

// Contains reports whether the user confirmed the source change for id.
func (c ConfirmedSourceChanges) Contains(id string) bool { return c[id] }

// UpdatePlan is the reviewable, non-mutating result of planning an update. The
// host shows PermissionDiff before applying it; a plan is stale when the
// installed version changes in the meantime.
type UpdatePlan struct {
	ID              string
	From            string
	To              string
	Channel         domainplugin.Channel
	Diff            domainplugin.PermissionDiff
	RequiresConsent []domainplugin.PermissionRequest
	Warnings        []domainplugin.ReleaseWarning
	SourceChanged   bool
	PinnedSource    string
	RegistrySource  string
	// SourceChanges lists every plugin id, root and dependency, whose pin
	// differs from the registry source. The caller must show each row and set
	// ConfirmedSourceChanges for exactly the ids the user confirmed.
	SourceChanges []SourceChange
	// ConfirmedSourceChanges names each changed id the user explicitly
	// confirmed. ApplyUpdate refuses any changed id that is absent.
	ConfirmedSourceChanges ConfirmedSourceChanges
	PackageSHA256          string
	PackageSize            int64
	Dependencies           []domainplugin.ResolvedDependency
}

// AutoUpdateResult reports the outcome of one automatic-update pass.
type AutoUpdateResult struct {
	Updated []string
	Skipped []string
}

// ErrRegistryNotConfigured reports registry use without a configured registry.
var ErrRegistryNotConfigured = errors.New("plugin: registry is not configured")

// ErrAutomaticUpdatesDisabled reports an automatic-update pass while the user
// preference is off. Automatic updates are off by default.
var ErrAutomaticUpdatesDisabled = errors.New("plugin: automatic updates are disabled")

// RegistryPreferences returns the persisted registry policy, defaulting to
// stable channels and automatic updates off.
func (m *Manager) RegistryPreferences(ctx context.Context) (domainplugin.RegistryPreferences, error) {
	if m.preferences == nil {
		return domainplugin.RegistryPreferences{}, nil
	}
	return m.preferences.RegistryPreferences(ctx)
}

// SetChannel records an opt-in beta channel for one plugin. Stable remains the
// default for every plugin the user has not opted in.
func (m *Manager) SetChannel(ctx context.Context, id string,
	channel domainplugin.Channel) error {
	if !channel.Valid() {
		return fmt.Errorf("plugin: invalid channel %q", channel)
	}
	if m.preferences == nil {
		return errors.New("plugin: preferences store is not configured")
	}
	preferences, err := m.preferences.RegistryPreferences(ctx)
	if err != nil {
		return err
	}
	preferences.SetChannel(id, channel)
	return m.preferences.SaveRegistryPreferences(ctx, preferences)
}

// SetAutomaticUpdates records the user's automatic-update preference.
func (m *Manager) SetAutomaticUpdates(ctx context.Context, enabled bool) error {
	if m.preferences == nil {
		return errors.New("plugin: preferences store is not configured")
	}
	preferences, err := m.preferences.RegistryPreferences(ctx)
	if err != nil {
		return err
	}
	preferences.AutomaticUpdates = enabled
	return m.preferences.SaveRegistryPreferences(ctx, preferences)
}

// SearchRegistry filters the static index on the client and enriches each row
// with the plugin's selected channel, latest selectable version, warnings, and
// installed state. No server-side query API exists.
func (m *Manager) SearchRegistry(ctx context.Context, query string) ([]RegistrySearchResult, error) {
	if m.registry == nil {
		return nil, ErrRegistryNotConfigured
	}
	index, err := m.registry.Index(ctx)
	if err != nil {
		return nil, err
	}
	preferences, err := m.RegistryPreferences(ctx)
	if err != nil {
		return nil, err
	}
	records, err := m.store.Records(ctx)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]domainplugin.InstallRecord, len(records))
	for _, record := range records {
		installed[record.ID] = record
	}
	summaries := domainplugin.SearchRegistryIndex(index, query)
	out := make([]RegistrySearchResult, 0, len(summaries))
	for _, summary := range summaries {
		result := RegistrySearchResult{
			ID: summary.ID, Name: summary.Name, Description: summary.Description,
			Keywords: append([]string(nil), summary.Keywords...),
			Channel:  preferences.ChannelFor(summary.ID),
		}
		metadata, loadErr := m.registry.Plugin(ctx, summary.ID)
		if loadErr != nil {
			return nil, loadErr
		}
		if version, ok := metadata.Select("", result.Channel); ok {
			result.LatestVersion = version.Version
			result.Warnings = version.Warnings()
		}
		if record, ok := installed[summary.ID]; ok {
			result.Installed = true
			result.InstalledVersion = record.Version
		}
		out = append(out, result)
	}
	return out, nil
}

// attachRegistryWarnings adds the registry notices for the currently installed
// version. A registry failure leaves the plugin list usable without warnings.
func (m *Manager) attachRegistryWarnings(ctx context.Context, plugin *InstalledPlugin) {
	if m.registry == nil || plugin.Source != domainplugin.SourceRegistry || plugin.Development {
		return
	}
	metadata, err := m.registry.Plugin(ctx, plugin.ID)
	if err != nil {
		return
	}
	for _, version := range metadata.Versions {
		if version.Version == plugin.Version {
			plugin.Warnings = version.Warnings()
			return
		}
	}
}

// RegistryPlugin returns one plugin's registry metadata.
func (m *Manager) RegistryPlugin(ctx context.Context, id string) (domainplugin.RegistryPlugin, error) {
	if m.registry == nil {
		return domainplugin.RegistryPlugin{}, ErrRegistryNotConfigured
	}
	return m.registry.Plugin(ctx, id)
}

// InstallFile validates and installs a local .natplug package. It is the
// legacy no-confirmation entry point; a source-change pin fails the call.
func (m *Manager) InstallFile(ctx context.Context, path string) (domainplugin.InstallRecord, error) {
	return m.InstallFileConfirm(ctx, path, nil)
}

// InstallFileConfirm installs a local .natplug package. confirmed is the set
// of plugin ids whose trust-on-first-use source change the user explicitly
// approved; a local package has one id, but the set keeps the API uniform with
// registry installs that also pull dependencies.
func (m *Manager) InstallFileConfirm(ctx context.Context, path string,
	confirmed ConfirmedSourceChanges) (domainplugin.InstallRecord, error) {
	if m.validator == nil {
		return domainplugin.InstallRecord{}, errors.New("plugin: package validator is not configured")
	}
	if m.store == nil {
		return domainplugin.InstallRecord{}, errors.New("plugin: package store is not configured")
	}
	pkg, err := m.validator.Validate(ctx, path)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.builtinImmutable(pkg.Manifest.ID, "installed"); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	source := localSourceIdentity(path)
	if err := m.checkSourcePin(ctx, pkg.Manifest.ID, source,
		confirmed.Contains(pkg.Manifest.ID)); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record, err := m.store.Install(ctx, pkg,
		InstallOptions{Source: domainplugin.SourceLocalFile, TrustedSource: source}, m.now().UTC())
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	m.recordSourcePin(ctx, record.ID, source)
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditInstall, PluginID: record.ID,
		Source: record.Source, Version: record.Version, Reason: "installed disabled",
	})
	return record, nil
}

// InstallFromRegistry downloads, verifies, and installs one registry version
// disabled. The declared size and SHA-256 are verified before the package is
// validated, trust-on-first-use is recorded for the plugin id and registry
// source, stable is the default channel, and missing package dependencies are
// installed disabled from the same registry. Every changed pin, including a
// dependency's, must be named in confirmed or the install refuses before any
// package is written.
func (m *Manager) InstallFromRegistry(ctx context.Context, id, version string,
	confirmed ConfirmedSourceChanges) (domainplugin.InstallRecord, error) {
	if err := m.builtinImmutable(id, "installed"); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if m.registry == nil {
		return domainplugin.InstallRecord{}, ErrRegistryNotConfigured
	}
	if m.store == nil {
		return domainplugin.InstallRecord{}, errors.New("plugin: package store is not configured")
	}
	if m.validator == nil {
		return domainplugin.InstallRecord{}, errors.New("plugin: package validator is not configured")
	}
	if existing, ok, err := m.store.Record(ctx, id); err != nil {
		return domainplugin.InstallRecord{}, err
	} else if ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s %s",
			ErrAlreadyInstalled, existing.ID, existing.Version)
	}
	metadata, err := m.registry.Plugin(ctx, id)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	channel, err := m.channelFor(ctx, id)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	selected, err := m.selectRegistryVersion(metadata, version, channel)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	source := m.registry.Identity()
	if err := m.checkSourcePin(ctx, id, source, confirmed.Contains(id)); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.verifyEnabledDependents(ctx, id, selected.Version); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	resolution, err := m.resolveRegistryDependencies(ctx, metadata, selected, channel)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.installMissingDependencies(ctx, resolution, source, confirmed); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record, err := m.installRegistryVersion(ctx, metadata.ID, selected, source)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	m.recordSourcePin(ctx, record.ID, source)
	return record, nil
}

// PlanUpdate selects the newest applicable registry version, verifies its
// package, and returns the permission diff without mutating the install.
func (m *Manager) PlanUpdate(ctx context.Context, id string) (UpdatePlan, error) {
	if err := m.builtinImmutable(id, "updated"); err != nil {
		return UpdatePlan{}, err
	}
	if m.registry == nil {
		return UpdatePlan{}, ErrRegistryNotConfigured
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return UpdatePlan{}, err
	}
	if !ok {
		return UpdatePlan{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	if record.Source != domainplugin.SourceRegistry {
		return UpdatePlan{}, &VersionError{ID: id, Problem: "was not installed from a registry"}
	}
	metadata, err := m.registry.Plugin(ctx, id)
	if err != nil {
		return UpdatePlan{}, err
	}
	channel, err := m.channelFor(ctx, id)
	if err != nil {
		return UpdatePlan{}, err
	}
	target, ok := metadata.Select("", channel)
	if !ok || !newerVersion(target.Version, record.Version) {
		return UpdatePlan{}, ErrNoUpdateAvailable
	}
	resolution, err := m.resolveRegistryDependencies(ctx, metadata, target, channel)
	if err != nil {
		return UpdatePlan{}, err
	}
	if err := m.verifyInstalledDependencies(ctx, resolution); err != nil {
		return UpdatePlan{}, err
	}
	pkg, err := m.verifyRegistryPackage(ctx, metadata.ID, target)
	if err != nil {
		return UpdatePlan{}, err
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return UpdatePlan{}, fmt.Errorf("plugin: %w", err)
	}
	diff := domainplugin.DiffPermissions(record.Required, record.Optional, required, optional)
	source := m.registry.Identity()
	pinned := record.TrustedSource
	if pinned == "" {
		pinned = source
	}
	changes, err := m.plannedSourceChanges(ctx, id, resolution, source)
	if err != nil {
		return UpdatePlan{}, err
	}
	return UpdatePlan{
		ID:              id,
		From:            record.Version,
		To:              target.Version,
		Channel:         channel,
		Diff:            diff,
		RequiresConsent: diff.RequiringConsent(),
		Warnings:        target.Warnings(),
		SourceChanged:   pinned != source,
		PinnedSource:    pinned,
		RegistrySource:  source,
		SourceChanges:   changes,
		PackageSHA256:   target.SHA256,
		PackageSize:     target.Size,
		Dependencies:    resolution.Resolved,
	}, nil
}

// ApplyUpdate applies a previously reviewed plan transactionally. The caller
// sets ConfirmedSourceChanges for every source change it showed the user and
// the user approved; any changed id left out refuses the whole update. A new
// required permission that is not allowed keeps the updated plugin inactive
// until consent; removed permissions are revoked automatically. An activation
// failure restores the retained previous version.
func (m *Manager) ApplyUpdate(ctx context.Context, plan UpdatePlan,
	decisions []domainplugin.PermissionDecision) (domainplugin.InstallRecord, error) {
	if err := m.builtinImmutable(plan.ID, "updated"); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if m.registry == nil {
		return domainplugin.InstallRecord{}, ErrRegistryNotConfigured
	}
	record, ok, err := m.store.Record(ctx, plan.ID)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", ErrNotInstalled, plan.ID)
	}
	if record.Version != plan.From {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: installed %s, plan expected %s",
			ErrStaleUpdatePlan, record.Version, plan.From)
	}
	source := m.registry.Identity()
	metadata, err := m.registry.Plugin(ctx, plan.ID)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	channel := plan.Channel
	if !channel.Valid() {
		channel = domainplugin.ChannelStable
	}
	target, err := m.selectRegistryVersion(metadata, plan.To, channel)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	resolution, err := m.resolveRegistryDependencies(ctx, metadata, target, channel)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	// Recompute the source changes from the trust store instead of trusting the
	// plan's rows: a dependency pin can appear between planning and applying,
	// and every changed id needs its own confirmation.
	changes, err := m.plannedSourceChanges(ctx, plan.ID, resolution, source)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	for _, change := range changes {
		if !plan.ConfirmedSourceChanges.Contains(change.ID) {
			return domainplugin.InstallRecord{}, &SourceChangedError{
				ID: change.ID, PinnedSource: change.PinnedSource, NewSource: change.NewSource,
			}
		}
	}
	if err := m.verifyEnabledDependents(ctx, plan.ID, target.Version); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.verifyInstalledDependencies(ctx, resolution); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	pkg, err := m.verifyRegistryPackage(ctx, metadata.ID, target)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("plugin: %w", err)
	}
	// Validate the decisions before any dependency is installed or the running
	// instance is unloaded: an invalid row must not mutate the install set.
	if _, err := decisionIndexFor(required, optional, decisions); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.installMissingDependencies(ctx, resolution, source,
		plan.ConfirmedSourceChanges); err != nil {
		return domainplugin.InstallRecord{}, err
	}

	newRecord := record
	newRecord.Name = pkg.Manifest.Name
	newRecord.Version = target.Version
	newRecord.Source = domainplugin.SourceRegistry
	newRecord.PackageSHA256 = target.SHA256
	newRecord.TrustedSource = source
	newRecord.Required = required
	newRecord.Optional = optional
	newRecord.Dependencies = append([]domainplugin.Dependency(nil), pkg.Manifest.Dependencies...)
	newRecord.Previous = nil

	grants, denied, err := updatedGrants(record, required, optional, decisions, m.now().UTC())
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	newRecord.Grants = grants
	if len(denied) > 0 {
		newRecord.Enabled = false
		newRecord.Reason = domainplugin.PermissionReason(
			domainplugin.ReasonPermissionDenied, denied[0])
		newRecord.Grants = withoutGrant(grants, denied[0])
	} else {
		newRecord.Enabled = record.Enabled
		newRecord.Reason = ""
	}

	// Unload before the swap so a running instance never executes a package
	// whose version directory was just replaced.
	wasEnabled := record.Enabled
	if wasEnabled {
		if fiber, ok := m.fiberFor(record.ID); ok {
			if unloadErr := m.container.Unload(ctx, fiber.ID()); unloadErr != nil {
				return domainplugin.InstallRecord{}, unloadErr
			}
		}
	}
	updated, err := m.store.PromoteVersion(ctx, pkg, newRecord, m.now().UTC())
	if err != nil {
		if wasEnabled {
			m.reloadInstalled(ctx, record)
		}
		return domainplugin.InstallRecord{}, err
	}
	m.recordSourcePin(ctx, updated.ID, source)
	m.auditUpdate(record, updated, plan.Diff, m.now().UTC())

	if len(denied) > 0 {
		for _, permission := range denied {
			m.auditDeny(updated, permission, "update consent")
		}
		m.authority.Reset(installedSubject(updated))
		if saveErr := m.store.SaveRecord(ctx, updated); saveErr != nil {
			return domainplugin.InstallRecord{}, saveErr
		}
		m.audit.Record(domainplugin.AuditEvent{
			Time: m.now().UTC(), Event: domainplugin.AuditActivation, PluginID: updated.ID,
			Source: updated.Source, Version: updated.Version,
			State: domainplugin.StatePending, Reason: updated.Reason,
		})
		return updated, nil
	}

	if !wasEnabled {
		return updated, nil
	}
	m.authority.SetGrants(installedSubject(updated), updated.Grants)
	dir, err := m.store.PackageDir(ctx, updated.ID)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if _, loadErr := m.container.LoadInstalled(ctx, dir, updated.Source); loadErr != nil {
		// The update swapped a package that cannot activate. Restore the
		// retained previous version so the host never keeps a broken update.
		restored, restoreErr := m.store.RestorePrevious(ctx, updated.ID, m.now().UTC())
		if restoreErr != nil {
			return domainplugin.InstallRecord{}, errors.Join(loadErr, restoreErr)
		}
		m.authority.SetGrants(installedSubject(restored), restored.Grants)
		m.audit.Record(domainplugin.AuditEvent{
			Time: m.now().UTC(), Event: domainplugin.AuditRollback, PluginID: restored.ID,
			Source: restored.Source, Version: restored.Version,
			Reason: "activation failed: " + loadErr.Error(),
		})
		m.reloadInstalled(ctx, restored)
		return domainplugin.InstallRecord{}, loadErr
	}
	return updated, nil
}

// Rollback restores the one retained previous version and its metadata. The
// permission diff still governs consent: a permission the old version requires
// but the user has not granted keeps it inactive. The rolled-back version
// becomes the new retained version, so the rollback itself is reversible.
func (m *Manager) Rollback(ctx context.Context, id string,
	decisions []domainplugin.PermissionDecision) (domainplugin.InstallRecord, error) {
	if err := m.builtinImmutable(id, "rolled back"); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record, ok, err := m.store.Record(ctx, id)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if !ok {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", ErrNotInstalled, id)
	}
	previous := record.Previous
	if previous == nil {
		return domainplugin.InstallRecord{}, fmt.Errorf("%w: %s", ErrNoPreviousVersion, id)
	}
	// Validate the decisions against the rollback target before unloading or
	// moving the durable record. An invalid decision must leave the running
	// version and its activation untouched.
	if err := m.validateRollbackDecisions(previous.Required, previous.Optional, decisions); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	if err := m.verifyEnabledDependents(ctx, id, previous.Version); err != nil {
		return domainplugin.InstallRecord{}, err
	}
	diff := domainplugin.DiffPermissions(record.Required, record.Optional,
		previous.Required, previous.Optional)
	wasEnabled := record.Enabled
	if wasEnabled {
		if fiber, ok := m.fiberFor(id); ok {
			if unloadErr := m.container.Unload(ctx, fiber.ID()); unloadErr != nil {
				return domainplugin.InstallRecord{}, unloadErr
			}
		}
	}
	restored, err := m.store.RestorePrevious(ctx, id, m.now().UTC())
	if err != nil {
		if wasEnabled {
			m.reloadInstalled(ctx, record)
		}
		return domainplugin.InstallRecord{}, err
	}
	restored, denied, grantErr := applyConsentToRestored(restored, diff, decisions, m.now().UTC())
	if grantErr != nil {
		return domainplugin.InstallRecord{}, m.revertRollback(ctx, record, grantErr, wasEnabled)
	}
	restored.Enabled = wasEnabled && denied == ""
	if denied != "" {
		restored.Reason = domainplugin.PermissionReason(
			domainplugin.ReasonPermissionDenied, domainplugin.Permission(denied))
		restored.Grants = withoutGrant(restored.Grants, domainplugin.Permission(denied))
	} else {
		restored.Reason = ""
	}
	if err := m.store.SaveRecord(ctx, restored); err != nil {
		return domainplugin.InstallRecord{}, m.revertRollback(ctx, record, err, wasEnabled)
	}
	if denied != "" {
		m.auditDeny(restored, domainplugin.Permission(denied), "rollback consent")
	}
	if !restored.Enabled {
		m.authority.Reset(installedSubject(restored))
		m.audit.Record(domainplugin.AuditEvent{
			Time: m.now().UTC(), Event: domainplugin.AuditRollback, PluginID: restored.ID,
			Source: restored.Source, Version: restored.Version, Reason: restored.Reason,
		})
		return restored, nil
	}
	m.authority.SetGrants(installedSubject(restored), restored.Grants)
	dir, err := m.store.PackageDir(ctx, restored.ID)
	if err != nil {
		return domainplugin.InstallRecord{}, m.revertRollback(ctx, record, err, wasEnabled)
	}
	if _, loadErr := m.container.LoadInstalled(ctx, dir, restored.Source); loadErr != nil {
		// Restore the version that was running before the rollback attempt.
		return domainplugin.InstallRecord{}, m.revertRollback(ctx, record, loadErr, wasEnabled)
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditRollback, PluginID: restored.ID,
		Source: restored.Source, Version: restored.Version,
	})
	return restored, nil
}

// AutoUpdate applies updates that need no new consent while the user's
// automatic-update preference is on. Updates that add a required permission,
// change a required scope, or change a trust pin (root or dependency) are
// skipped for explicit review.
func (m *Manager) AutoUpdate(ctx context.Context) (AutoUpdateResult, error) {
	var result AutoUpdateResult
	preferences, err := m.RegistryPreferences(ctx)
	if err != nil {
		return result, err
	}
	if !preferences.AutomaticUpdates {
		return result, ErrAutomaticUpdatesDisabled
	}
	if m.registry == nil {
		return result, ErrRegistryNotConfigured
	}
	records, err := m.store.Records(ctx)
	if err != nil {
		return result, err
	}
	var errs []error
	for _, record := range records {
		if record.Source != domainplugin.SourceRegistry {
			continue
		}
		plan, planErr := m.PlanUpdate(ctx, record.ID)
		if errors.Is(planErr, ErrNoUpdateAvailable) {
			continue
		}
		if planErr != nil {
			errs = append(errs, planErr)
			continue
		}
		if len(plan.RequiresConsent) > 0 || len(plan.SourceChanges) > 0 {
			result.Skipped = append(result.Skipped, record.ID)
			continue
		}
		if _, applyErr := m.ApplyUpdate(ctx, plan, nil); applyErr != nil {
			errs = append(errs, applyErr)
			continue
		}
		result.Updated = append(result.Updated, record.ID)
	}
	return result, errors.Join(errs...)
}

// selectRegistryVersion selects an explicit or latest version and maps a
// refusal to the precise typed error.
func (m *Manager) selectRegistryVersion(metadata domainplugin.RegistryPlugin,
	version string, channel domainplugin.Channel) (domainplugin.RegistryVersion, error) {
	selected, ok := metadata.Select(version, channel)
	if ok {
		return selected, nil
	}
	if version != "" {
		for _, candidate := range metadata.Versions {
			if candidate.Version != version {
				continue
			}
			switch {
			case candidate.Yanked:
				return domainplugin.RegistryVersion{}, &YankedVersionError{
					ID: metadata.ID, Version: version,
				}
			case !candidate.Compatible():
				return domainplugin.RegistryVersion{}, &VersionError{
					ID: metadata.ID, Version: version,
					Problem: "is not compatible with this host",
				}
			default:
				return domainplugin.RegistryVersion{}, &VersionError{
					ID: metadata.ID, Version: version,
					Problem: "is not on the selected channel",
				}
			}
		}
		return domainplugin.RegistryVersion{}, &VersionError{
			ID: metadata.ID, Version: version, Problem: "is not in the registry",
		}
	}
	return domainplugin.RegistryVersion{}, &VersionError{
		ID: metadata.ID, Problem: "has no compatible release on the selected channel",
	}
}

func (m *Manager) channelFor(ctx context.Context, id string) (domainplugin.Channel, error) {
	preferences, err := m.RegistryPreferences(ctx)
	if err != nil {
		return "", err
	}
	return preferences.ChannelFor(id), nil
}

func (m *Manager) checkSourcePin(ctx context.Context, id, source string,
	confirm bool) error {
	if m.trust == nil {
		return nil
	}
	pin, ok, err := m.trust.SourcePin(ctx, id)
	if err != nil {
		return err
	}
	if ok && pin.Source != source && !confirm {
		return &SourceChangedError{ID: id, PinnedSource: pin.Source, NewSource: source}
	}
	return nil
}

func (m *Manager) recordSourcePin(ctx context.Context, id, source string) {
	if m.trust == nil {
		return
	}
	if err := m.trust.SaveSourcePin(ctx, domainplugin.SourcePin{
		ID: id, Source: source, FirstSeen: m.now().UTC(),
	}); err != nil {
		// A failed pin write must not roll back a verified install; the next
		// install simply re-establishes the pin. The audit event records it.
		m.audit.Record(domainplugin.AuditEvent{
			Time: m.now().UTC(), Event: domainplugin.AuditTrust, PluginID: id,
			Reason: "failed to persist trust pin: " + err.Error(),
		})
	}
}

func localSourceIdentity(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(path))
	}
	return filepath.ToSlash(absolute)
}

// installRegistryVersion verifies one package body and installs it disabled.
func (m *Manager) installRegistryVersion(ctx context.Context, id string,
	version domainplugin.RegistryVersion, source string) (domainplugin.InstallRecord, error) {
	pkg, err := m.verifyRegistryPackage(ctx, id, version)
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	record, err := m.store.Install(ctx, pkg,
		InstallOptions{Source: domainplugin.SourceRegistry, TrustedSource: source}, m.now().UTC())
	if err != nil {
		return domainplugin.InstallRecord{}, err
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditInstall, PluginID: record.ID,
		Source: record.Source, Version: record.Version, Reason: "installed disabled from registry",
	})
	return record, nil
}

// verifyRegistryPackage fetches a package body, verifies the declared size and
// SHA-256, validates the archive, and proves the manifest inside the package
// matches the registry metadata before it can be installed.
func (m *Manager) verifyRegistryPackage(ctx context.Context, id string,
	version domainplugin.RegistryVersion) (Package, error) {
	data, err := m.registry.FetchPackage(ctx, id, version.Version)
	if err != nil {
		return Package{}, err
	}
	if int64(len(data)) != version.Size {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: fmt.Sprintf("package is %d bytes, metadata declares %d", len(data), version.Size)}
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(got)),
		[]byte(strings.ToLower(version.SHA256))) != 1 {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: "package SHA-256 does not match the registry metadata"}
	}
	directory, err := os.MkdirTemp("", "natcatty-plugin-registry-")
	if err != nil {
		return Package{}, fmt.Errorf("plugin: stage registry package: %w", err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, id+"-"+version.Version+".natplug")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Package{}, fmt.Errorf("plugin: stage registry package: %w", err)
	}
	pkg, err := m.validator.Validate(ctx, path)
	if err != nil {
		return Package{}, err
	}
	if pkg.Manifest.ID != id || pkg.Manifest.Version != version.Version {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: fmt.Sprintf("package manifest is %s %s", pkg.Manifest.ID, pkg.Manifest.Version)}
	}
	if pkg.Manifest.ABI != version.ABI {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: "package ABI does not match the registry metadata"}
	}
	if pkg.Manifest.PluginAPI != version.PluginAPI {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: "package Plugin API range does not match the registry metadata"}
	}
	required, optional, err := pkg.Manifest.Permissions.Requests()
	if err != nil {
		return Package{}, fmt.Errorf("plugin: %w", err)
	}
	if !samePermissionNames(required, version.Required) ||
		!samePermissionNames(optional, version.Optional) {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: "package permissions do not match the registry metadata"}
	}
	if !sameDependencies(pkg.Manifest.Dependencies, version.Dependencies) {
		return Package{}, &VersionError{ID: id, Version: version.Version,
			Problem: "package dependencies do not match the registry metadata"}
	}
	pkg.SHA256 = strings.ToLower(version.SHA256)
	return pkg, nil
}

func samePermissionNames(requests []domainplugin.PermissionRequest, names []string) bool {
	if len(requests) != len(names) {
		return false
	}
	want := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := domainplugin.PermissionByName(name); !ok {
			return false
		}
		want = append(want, name)
	}
	sort.Strings(want)
	got := make([]string, 0, len(requests))
	for _, request := range requests {
		got = append(got, string(request.Permission))
	}
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func sameDependencies(a, b []domainplugin.Dependency) bool {
	if len(a) != len(b) {
		return false
	}
	normalized := func(dependencies []domainplugin.Dependency) []string {
		out := make([]string, 0, len(dependencies))
		for _, dependency := range dependencies {
			out = append(out, dependency.ID+"\x00"+dependency.Version)
		}
		sort.Strings(out)
		return out
	}
	left, right := normalized(a), normalized(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// dependencyResolution is the stable result of resolving one package's
// transitive registry dependencies.
type dependencyResolution struct {
	Resolved    []domainplugin.ResolvedDependency
	Constraints map[string][]domainplugin.Dependency
	Versions    map[string]domainplugin.RegistryVersion
}

// resolveRegistryDependencies picks the highest satisfying version for every
// transitive package dependency. Dependency metadata is read from the same
// registry. Cycles and conflicting constraints are rejected.
func (m *Manager) resolveRegistryDependencies(ctx context.Context,
	root domainplugin.RegistryPlugin, rootVersion domainplugin.RegistryVersion,
	channel domainplugin.Channel) (dependencyResolution, error) {
	metadata := map[string]domainplugin.RegistryPlugin{root.ID: root}
	load := func(id string) (domainplugin.RegistryPlugin, error) {
		if cached, ok := metadata[id]; ok {
			return cached, nil
		}
		loaded, err := m.registry.Plugin(ctx, id)
		if err != nil {
			return domainplugin.RegistryPlugin{}, fmt.Errorf("plugin dependency: %s: %w", id, err)
		}
		metadata[id] = loaded
		return loaded, nil
	}
	chosen := map[string]domainplugin.RegistryVersion{root.ID: rootVersion}
	for iteration := 0; iteration < 64; iteration++ {
		graph := make(map[string][]domainplugin.Dependency, len(chosen))
		for id, version := range chosen {
			graph[id] = append([]domainplugin.Dependency(nil), version.Dependencies...)
		}
		// A compiled-in plugin is already present for the life of the process.
		// Its edge is checked against the app-versioned manifest and removed
		// from registry resolution, so no download or install is attempted.
		for id, dependencies := range graph {
			kept := make([]domainplugin.Dependency, 0, len(dependencies))
			for _, dependency := range dependencies {
				version, ok := m.builtinVersion(dependency.ID)
				if !ok {
					kept = append(kept, dependency)
					continue
				}
				if !satisfiesConstraints(dependency.ID, version,
					[]domainplugin.Dependency{dependency}) {
					return dependencyResolution{}, &DependencyError{
						ID: dependency.ID, Problem: fmt.Sprintf(
							"built-in version %s does not satisfy %s",
							version, dependency.Version),
					}
				}
			}
			graph[id] = kept
		}
		ids := make(map[string]bool)
		for _, dependencies := range graph {
			for _, dependency := range dependencies {
				ids[dependency.ID] = true
			}
		}
		available := make(map[string][]string, len(ids))
		for id := range ids {
			plugin, err := load(id)
			if err != nil {
				return dependencyResolution{}, err
			}
			idChannel := channel
			if id != root.ID {
				idChannel, err = m.channelFor(ctx, id)
				if err != nil {
					return dependencyResolution{}, err
				}
			}
			versions := make([]string, 0, len(plugin.Versions))
			for _, candidate := range plugin.Versions {
				if candidate.Selectable() && candidate.Compatible() &&
					idChannel.Allows(candidate.Channel) {
					versions = append(versions, candidate.Version)
				}
			}
			available[id] = versions
		}
		resolved, err := domainplugin.ResolveDependencies(root.ID, graph, available)
		if err != nil {
			return dependencyResolution{}, err
		}
		next := make(map[string]domainplugin.RegistryVersion, len(resolved)+1)
		next[root.ID] = rootVersion
		changed := false
		for _, dependency := range resolved {
			plugin, err := load(dependency.ID)
			if err != nil {
				return dependencyResolution{}, err
			}
			selected, ok := selectExact(plugin, dependency.Version)
			if !ok {
				return dependencyResolution{}, fmt.Errorf(
					"plugin dependency: %s %s is not available", dependency.ID, dependency.Version)
			}
			next[dependency.ID] = selected
			if previous, ok := chosen[dependency.ID]; !ok || previous.Version != selected.Version {
				changed = true
			}
		}
		if len(next) != len(chosen) {
			changed = true
		}
		chosen = next
		if changed {
			continue
		}
		constraints := collectDependencyConstraints(root.ID, graph)
		return dependencyResolution{
			Resolved: resolved, Constraints: constraints, Versions: chosen,
		}, nil
	}
	return dependencyResolution{}, errors.New(
		"plugin dependency: resolution did not converge")
}

func selectExact(plugin domainplugin.RegistryPlugin,
	version string) (domainplugin.RegistryVersion, bool) {
	for _, candidate := range plugin.Versions {
		if candidate.Version == version {
			return candidate, true
		}
	}
	return domainplugin.RegistryVersion{}, false
}

func collectDependencyConstraints(root string,
	graph map[string][]domainplugin.Dependency) map[string][]domainplugin.Dependency {
	out := make(map[string][]domainplugin.Dependency)
	visited := make(map[string]bool)
	var walk func(string)
	walk = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		for _, dependency := range graph[id] {
			out[dependency.ID] = append(out[dependency.ID], dependency)
			walk(dependency.ID)
		}
	}
	walk(root)
	return out
}

// installMissingDependencies installs missing transitive dependencies disabled
// from the same registry. Installed packages are never replaced implicitly.
// Every missing dependency's trust pin is checked before any package is
// installed, so an unconfirmed source change leaves the dependency set
// untouched instead of partially applying the operation.
func (m *Manager) installMissingDependencies(ctx context.Context,
	resolution dependencyResolution, source string,
	confirmed ConfirmedSourceChanges) error {
	type missingDependency struct {
		id      string
		version domainplugin.RegistryVersion
	}
	missing := make([]missingDependency, 0, len(resolution.Resolved))
	for _, dependency := range resolution.Resolved {
		if version, ok := m.builtinVersion(dependency.ID); ok {
			if !satisfiesConstraints(dependency.ID, version,
				resolution.Constraints[dependency.ID]) {
				return &DependencyError{ID: dependency.ID, Problem: fmt.Sprintf(
					"built-in version %s does not satisfy %s",
					version, formatConstraints(resolution.Constraints[dependency.ID]))}
			}
			continue
		}
		existing, ok, err := m.store.Record(ctx, dependency.ID)
		if err != nil {
			return err
		}
		if ok {
			if !satisfiesConstraints(dependency.ID, existing.Version,
				resolution.Constraints[dependency.ID]) {
				return &DependencyError{ID: dependency.ID, Problem: fmt.Sprintf(
					"installed version %s does not satisfy %s",
					existing.Version, formatConstraints(resolution.Constraints[dependency.ID]))}
			}
			continue
		}
		version, ok := resolution.Versions[dependency.ID]
		if !ok {
			return &DependencyError{ID: dependency.ID, Problem: "has no selected version"}
		}
		if err := m.verifyEnabledDependents(ctx, dependency.ID, version.Version); err != nil {
			return err
		}
		if err := m.checkSourcePin(ctx, dependency.ID, source,
			confirmed.Contains(dependency.ID)); err != nil {
			return err
		}
		missing = append(missing, missingDependency{id: dependency.ID, version: version})
	}
	for _, dependency := range missing {
		record, err := m.installRegistryVersion(ctx, dependency.id, dependency.version, source)
		if err != nil {
			return err
		}
		m.recordSourcePin(ctx, record.ID, source)
	}
	return nil
}

// verifyInstalledDependencies rejects a plan when an already-installed
// dependency does not satisfy the target's constraints.
func (m *Manager) verifyInstalledDependencies(ctx context.Context,
	resolution dependencyResolution) error {
	for _, dependency := range resolution.Resolved {
		if version, ok := m.builtinVersion(dependency.ID); ok {
			if !satisfiesConstraints(dependency.ID, version,
				resolution.Constraints[dependency.ID]) {
				return &DependencyError{ID: dependency.ID, Problem: fmt.Sprintf(
					"built-in version %s does not satisfy %s",
					version, formatConstraints(resolution.Constraints[dependency.ID]))}
			}
			continue
		}
		record, ok, err := m.store.Record(ctx, dependency.ID)
		if err != nil {
			return err
		}
		if ok && !satisfiesConstraints(dependency.ID, record.Version,
			resolution.Constraints[dependency.ID]) {
			return &DependencyError{ID: dependency.ID, Problem: fmt.Sprintf(
				"installed version %s does not satisfy %s",
				record.Version, formatConstraints(resolution.Constraints[dependency.ID]))}
		}
	}
	return nil
}

func satisfiesConstraints(id, version string,
	constraints []domainplugin.Dependency) bool {
	parsed, ok := domainplugin.ParseSemver(version)
	if !ok {
		return false
	}
	for _, constraint := range constraints {
		if constraint.ID == id && !domainplugin.RangeContains(constraint.Version, parsed) {
			return false
		}
	}
	return true
}

func formatConstraints(constraints []domainplugin.Dependency) string {
	parts := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		parts = append(parts, constraint.ID+" "+constraint.Version)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func newerVersion(candidate, current string) bool {
	next, ok := domainplugin.ParseSemver(candidate)
	if !ok {
		return false
	}
	existing, ok := domainplugin.ParseSemver(current)
	if !ok {
		return true
	}
	return next.Compare(existing) > 0
}

// updatedGrants computes the grants for an update. Unchanged permissions keep
// their grant, a scope change or a newly required permission needs an explicit
// allow decision, and removed permissions lose their grant. denied names the
// first newly required permission the user did not allow.
func updatedGrants(record domainplugin.InstallRecord,
	required, optional []domainplugin.PermissionRequest,
	decisions []domainplugin.PermissionDecision, now time.Time) (
	[]domainplugin.PermissionGrant, []domainplugin.Permission, error) {
	index, err := decisionIndexFor(required, optional, decisions)
	if err != nil {
		return nil, nil, err
	}
	oldRequired := make(map[domainplugin.Permission]domainplugin.PermissionRequest,
		len(record.Required))
	for _, request := range record.Required {
		oldRequired[request.Permission] = request
	}
	oldOptional := make(map[domainplugin.Permission]domainplugin.PermissionRequest,
		len(record.Optional))
	for _, request := range record.Optional {
		oldOptional[request.Permission] = request
	}
	var grants []domainplugin.PermissionGrant
	var denied []domainplugin.Permission
	for _, request := range required {
		// Only a permission that was already required at the same scope keeps
		// its grant. Moving optional to required is a new required permission
		// and needs a fresh decision.
		if grant, ok := record.Grant(request.Permission); ok {
			if previous, wasRequired := oldRequired[request.Permission]; wasRequired &&
				previous.Scope.Equal(request.Scope) {
				grant.Optional = false
				grants = append(grants, grant)
				continue
			}
		}
		decision, provided := index[request.Permission]
		if provided && decision.Allow {
			grants = append(grants, domainplugin.PermissionGrant{
				Permission: request.Permission, Scope: request.Scope, GrantedAt: now,
			})
			continue
		}
		denied = append(denied, request.Permission)
	}
	for _, request := range optional {
		// Moving required to optional revokes the required grant; the user
		// must grant the optional permission explicitly.
		if grant, ok := record.Grant(request.Permission); ok {
			if previous, wasOptional := oldOptional[request.Permission]; wasOptional &&
				previous.Scope.Equal(request.Scope) {
				grant.Optional = true
				grants = append(grants, grant)
				continue
			}
		}
		decision, provided := index[request.Permission]
		if provided && decision.Allow {
			grants = append(grants, domainplugin.PermissionGrant{
				Permission: request.Permission, Scope: request.Scope,
				Optional: true, GrantedAt: now,
			})
		}
	}
	return grants, denied, nil
}

func decisionIndexFor(required, optional []domainplugin.PermissionRequest,
	decisions []domainplugin.PermissionDecision) (
	map[domainplugin.Permission]domainplugin.PermissionDecision, error) {
	declared := make(map[domainplugin.Permission]bool, len(required)+len(optional))
	for _, request := range required {
		declared[request.Permission] = true
	}
	for _, request := range optional {
		declared[request.Permission] = true
	}
	index := make(map[domainplugin.Permission]domainplugin.PermissionDecision, len(decisions))
	for _, decision := range decisions {
		if !declared[decision.Permission] {
			return nil, &InvalidRequestError{
				Permission: decision.Permission, Problem: "was not requested by the manifest",
			}
		}
		index[decision.Permission] = decision
	}
	return index, nil
}

// validateRollbackDecisions rejects a decision list that names a permission
// the rollback target never requested or supplies an invalid scope for an
// allowed permission. It runs before any unload or store mutation.
func (m *Manager) validateRollbackDecisions(required, optional []domainplugin.PermissionRequest,
	decisions []domainplugin.PermissionDecision) error {
	if _, err := decisionIndexFor(required, optional, decisions); err != nil {
		return err
	}
	for _, decision := range decisions {
		if !decision.Allow {
			continue
		}
		request, ok := declaredPermissionRequest(required, optional, decision.Permission)
		if !ok {
			continue
		}
		if _, err := m.grantFor(request, decision, true, false, m.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func declaredPermissionRequest(required, optional []domainplugin.PermissionRequest,
	permission domainplugin.Permission) (domainplugin.PermissionRequest, bool) {
	for _, request := range required {
		if request.Permission == permission {
			return request, true
		}
	}
	for _, request := range optional {
		if request.Permission == permission {
			return request, true
		}
	}
	return domainplugin.PermissionRequest{}, false
}

// applyConsentToRestored reconciles the retained grants with the permission
// diff of a rollback. It returns the first required permission left ungranted.
func applyConsentToRestored(record domainplugin.InstallRecord,
	diff domainplugin.PermissionDiff, decisions []domainplugin.PermissionDecision,
	now time.Time) (domainplugin.InstallRecord, string, error) {
	index, err := decisionIndexFor(record.Required, record.Optional, decisions)
	if err != nil {
		return domainplugin.InstallRecord{}, "", err
	}
	declared := make(map[domainplugin.Permission]bool, len(record.Required)+len(record.Optional))
	for _, request := range record.Required {
		declared[request.Permission] = true
	}
	for _, request := range record.Optional {
		declared[request.Permission] = true
	}
	var kept []domainplugin.PermissionGrant
	for _, grant := range record.Grants {
		if declared[grant.Permission] {
			kept = append(kept, grant)
		}
	}
	record.Grants = kept
	for _, request := range diff.RequiringConsent() {
		decision, provided := index[request.Permission]
		if provided && decision.Allow {
			record.SetGrant(domainplugin.PermissionGrant{
				Permission: request.Permission, Scope: request.Scope, GrantedAt: now,
			})
		} else {
			record.RemoveGrant(request.Permission)
		}
	}
	for _, request := range record.Optional {
		decision, provided := index[request.Permission]
		if !provided || !decision.Allow {
			continue
		}
		if _, ok := record.Grant(request.Permission); !ok {
			record.SetGrant(domainplugin.PermissionGrant{
				Permission: request.Permission, Scope: request.Scope,
				Optional: true, GrantedAt: now,
			})
		}
	}
	for _, request := range record.Required {
		if _, ok := record.Grant(request.Permission); !ok {
			return record, string(request.Permission), nil
		}
	}
	return record, "", nil
}

func withoutGrant(grants []domainplugin.PermissionGrant,
	permission domainplugin.Permission) []domainplugin.PermissionGrant {
	out := make([]domainplugin.PermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.Permission != permission {
			out = append(out, grant)
		}
	}
	return out
}

func (m *Manager) auditUpdate(previous, updated domainplugin.InstallRecord,
	diff domainplugin.PermissionDiff, now time.Time) {
	m.audit.Record(domainplugin.AuditEvent{
		Time: now, Event: domainplugin.AuditUpdate, PluginID: updated.ID,
		Source: updated.Source, Version: updated.Version,
		Reason: "updated from " + previous.Version,
	})
	for _, request := range append(append([]domainplugin.PermissionRequest(nil),
		diff.RemovedRequired...), diff.RemovedOptional...) {
		m.audit.Record(domainplugin.AuditEvent{
			Time: now, Event: domainplugin.AuditRevoke, PluginID: updated.ID,
			Source: updated.Source, Version: updated.Version,
			Permission: request.Permission, Reason: "update removed permission",
		})
	}
	for _, request := range append(append([]domainplugin.PermissionRequest(nil),
		diff.AddedRequired...), diff.AddedOptional...) {
		if grant, ok := updated.Grant(request.Permission); ok {
			scope := grant.Scope
			m.audit.Record(domainplugin.AuditEvent{
				Time: now, Event: domainplugin.AuditGrant, PluginID: updated.ID,
				Source: updated.Source, Version: updated.Version,
				Permission: request.Permission, Scope: &scope, Reason: "update consent",
			})
		}
	}
}

// reloadInstalled restores a fiber after a failed swap. Any failed fiber from
// the attempted load is discarded first; the error from a second failure is
// left on the install record by the container's normal activation path.
func (m *Manager) reloadInstalled(ctx context.Context, record domainplugin.InstallRecord) {
	if fiber, ok := m.fiberFor(record.ID); ok {
		_ = m.container.Unload(ctx, fiber.ID())
	}
	dir, err := m.store.PackageDir(ctx, record.ID)
	if err != nil {
		return
	}
	if _, err := m.container.LoadInstalled(ctx, dir, record.Source); err != nil {
		record.Reason = err.Error()
		_ = m.store.SaveRecord(ctx, record)
	}
}

// plannedSourceChanges lists every id a plan would install from source, the
// root and each missing dependency, whose trust pin differs. Installed
// dependencies are skipped because they are never replaced implicitly.
func (m *Manager) plannedSourceChanges(ctx context.Context, root string,
	resolution dependencyResolution, source string) ([]SourceChange, error) {
	if m.trust == nil {
		return nil, nil
	}
	ids := []string{root}
	for _, dependency := range resolution.Resolved {
		if _, ok, err := m.store.Record(ctx, dependency.ID); err != nil {
			return nil, err
		} else if !ok {
			ids = append(ids, dependency.ID)
		}
	}
	sort.Strings(ids)
	var changes []SourceChange
	for _, id := range ids {
		pin, ok, err := m.trust.SourcePin(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok && id == root {
			// A failed pin write must not erase the record's last known source:
			// an update planned from the install record still needs consent.
			if record, found, err := m.store.Record(ctx, id); err != nil {
				return nil, err
			} else if found && record.TrustedSource != "" {
				pin.Source, ok = record.TrustedSource, true
			}
		}
		if ok && pin.Source != source {
			changes = append(changes, SourceChange{
				ID: id, PinnedSource: pin.Source, NewSource: source,
			})
		}
	}
	return changes, nil
}

// verifyEnabledDependents refuses a version change that would leave an enabled
// installed dependent with an unsatisfied dependency. Disabled dependents do
// not block. The host never promotes a version outside a dependent's declared
// range unless the user disables that dependent first.
func (m *Manager) verifyEnabledDependents(ctx context.Context, id, version string) error {
	records, err := m.store.Records(ctx)
	if err != nil {
		return err
	}
	var violations []DependentConstraint
	for _, record := range records {
		if record.ID == id || !record.Enabled || !record.DependsOn(id) {
			continue
		}
		if record.SatisfiesDependency(id, version) {
			continue
		}
		for _, dependency := range record.Dependencies {
			if dependency.ID != id {
				continue
			}
			parsed, ok := domainplugin.ParseSemver(version)
			if ok && domainplugin.RangeContains(dependency.Version, parsed) {
				continue
			}
			violations = append(violations, DependentConstraint{
				ID: record.ID, Range: dependency.Version,
			})
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].ID != violations[j].ID {
			return violations[i].ID < violations[j].ID
		}
		return violations[i].Range < violations[j].Range
	})
	return &DependentsConstraintError{ID: id, Version: version, Dependents: violations}
}

// revertRollback swaps the store back to the pre-rollback version after a
// post-swap failure and reloads it when it was active. The original failure is
// returned joined with any revert failure.
func (m *Manager) revertRollback(ctx context.Context, before domainplugin.InstallRecord,
	cause error, wasEnabled bool) error {
	back, err := m.store.RestorePrevious(ctx, before.ID, m.now().UTC())
	if err != nil {
		return errors.Join(cause, err)
	}
	m.authority.SetGrants(installedSubject(back), back.Grants)
	if wasEnabled {
		m.reloadInstalled(ctx, back)
	}
	return cause
}

// auditDeny records a refused required permission with the scope it declared.
func (m *Manager) auditDeny(record domainplugin.InstallRecord,
	permission domainplugin.Permission, reason string) {
	var scope *domainplugin.Scope
	for _, request := range record.Required {
		if request.Permission == permission {
			scope = scopePointer(request.Scope)
			break
		}
	}
	m.audit.Record(domainplugin.AuditEvent{
		Time: m.now().UTC(), Event: domainplugin.AuditDeny, PluginID: record.ID,
		Source: record.Source, Version: record.Version,
		Permission: permission, Scope: scope, Reason: reason,
	})
}

// enabledDependents returns every enabled installed plugin that depends on id.
func (m *Manager) enabledDependents(ctx context.Context, id string) ([]string, error) {
	records, err := m.store.Records(ctx)
	if err != nil {
		return nil, err
	}
	var dependents []string
	for _, record := range records {
		if record.ID == id || !record.Enabled || !record.DependsOn(id) {
			continue
		}
		dependents = append(dependents, record.ID)
	}
	sort.Strings(dependents)
	return dependents, nil
}
