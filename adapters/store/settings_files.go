package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// KeyBindingsStore persists the keyboard binding document as a small
// versioned JSON file.
type KeyBindingsStore struct {
	path string
}

// OpenKeyBindings returns a store at path.
func OpenKeyBindings(path string) *KeyBindingsStore { return &KeyBindingsStore{path: path} }

type keyBindingsRecord struct {
	Version         int                         `json:"version"`
	Scheme          domainkeybindings.Scheme    `json:"scheme"`
	Overrides       domainkeybindings.Overrides `json:"overrides,omitempty"`
	SuppressedMenus []string                    `json:"suppressedMenus,omitempty"`
}

const keyBindingsVersion = 1

// Load returns the stored document. A missing file means no overrides; the
// service supplies the platform's default scheme.
func (s *KeyBindingsStore) Load(ctx context.Context) (appkeybindings.State, error) {
	if err := ctx.Err(); err != nil {
		return appkeybindings.State{}, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return appkeybindings.State{}, nil
	}
	if err != nil {
		return appkeybindings.State{}, fmt.Errorf("store: read key bindings: %w", err)
	}
	var record keyBindingsRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// A malformed file must not stop the app from starting.
		return appkeybindings.State{}, nil
	}
	return appkeybindings.State{
		Scheme:          record.Scheme,
		Overrides:       record.Overrides,
		SuppressedMenus: record.SuppressedMenus,
	}, nil
}

// Save atomically replaces the document.
func (s *KeyBindingsStore) Save(ctx context.Context, state appkeybindings.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := keyBindingsRecord{
		Version:         keyBindingsVersion,
		Scheme:          state.Scheme,
		Overrides:       state.Overrides,
		SuppressedMenus: state.SuppressedMenus,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode key bindings: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".keybindings-*.tmp", data)
}

// PreferencesStore persists the typed preferences document.
type PreferencesStore struct {
	path string
	// policy is the release-level transport default. Production uses
	// transport.ReleasePolicy; tests and release tooling can evaluate a
	// different policy, such as the post-switch one.
	policy transportmodel.Policy
	now    func() time.Time
}

// PreferencesOption customizes a PreferencesStore.
type PreferencesOption func(*PreferencesStore)

// WithReleasePolicy evaluates non-explicit transport choices against policy.
// It exists so the default-switch migration can be tested without mutating a
// process-wide value; production opens with transport.ReleasePolicy.
func WithReleasePolicy(policy transportmodel.Policy) PreferencesOption {
	return func(s *PreferencesStore) { s.policy = policy }
}

// WithClock supplies the time waiver expiry is evaluated at, so a test can
// cross a waiver boundary deterministically.
func WithClock(now func() time.Time) PreferencesOption {
	return func(s *PreferencesStore) { s.now = now }
}

// OpenPreferences returns a store at path.
func OpenPreferences(path string, opts ...PreferencesOption) *PreferencesStore {
	store := &PreferencesStore{
		path:   path,
		policy: transportmodel.ReleasePolicy,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(store)
	}
	return store
}

type preferencesRecord struct {
	Version int `json:"version"`
	// The groups are inlined so the file stays readable and a future field
	// is a plain addition.
	domainpreferences.Settings
}

// preferencesVersion 2 adds the transport implementation provenance flag.
// Load still accepts version 1 and documents without a version; the
// migration is field-based and never needs a Host or profile to be re-saved.
const preferencesVersion = 2

// Load starts from the defaults and overlays the stored document, so a
// record written before a field existed keeps the intended default.
func (s *PreferencesStore) Load(ctx context.Context) (domainpreferences.Settings, error) {
	if err := ctx.Err(); err != nil {
		return s.freshSettings(), err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.freshSettings(), nil
	}
	if err != nil {
		return s.freshSettings(), fmt.Errorf("store: read preferences: %w", err)
	}
	settings := s.freshSettings()
	record := preferencesRecord{Settings: settings}
	if err := json.Unmarshal(data, &record); err != nil {
		return settings, nil
	}
	// Unmarshal overlays present fields onto the defaults; absent fields
	// stay at their defaults.
	settings = record.Settings
	if !hasFollowAppTerminalTheme(data) {
		// A profile written before the terminal could follow the app keeps a
		// scheme its user picked, as the original's upgrade did; one still on
		// the default scheme never chose and follows the app.
		settings.Appearance.FollowAppTerminalTheme = settings.Appearance.TerminalTheme == "" ||
			settings.Appearance.TerminalTheme == domainpreferences.DefaultTerminalTheme
	}
	settings = s.resolveTransport(settings, transportChoiceWasExplicit(data))
	return domainpreferences.Normalize(settings), nil
}

// freshSettings is the document a new install sees: release defaults with no
// explicit transport choice, so a later default flip moves it.
func (s *PreferencesStore) freshSettings() domainpreferences.Settings {
	settings := domainpreferences.Defaults()
	settings.Transport.ImplementationExplicit = false
	settings.Transport.Implementation = s.defaultImplementation()
	return settings
}

// transportChoiceWasExplicit reports whether the stored document proves the
// user chose the transport implementation. It reads the raw document rather
// than the overlaid settings: after a default flip, the defaults seed an
// in-process value, and that seed must never be mistaken for a stored choice.
//
// A version 2 document always writes implementationExplicit, so the flag is
// authoritative. A version 1 document has no flag: a stored non-default value
// could only come from an explicit choice, while a stored value equal to the
// old default could be either an explicit choice or a default-written value.
// The migration deliberately treats the ambiguous case as never chosen, so
// users who never touched the setting adopt the new default. The one-time cost
// is documented in docs/releases/transport-rollout.md: a version 1 user who
// explicitly chose the old default may need to choose it once more.
func transportChoiceWasExplicit(data []byte) bool {
	implementation, present := storedTransportImplementation(data)
	implementation = strings.TrimSpace(implementation)
	hasValue := present && implementation != ""

	if explicit, provenancePresent := transportImplementationExplicit(data); provenancePresent {
		return explicit && hasValue
	}
	if !hasValue {
		return false
	}
	return implementation != string(transportmodel.LegacyDefault)
}

// storedTransportImplementation reads the persisted implementation without
// decoding the whole document, so an absent key is distinguishable from a
// value the defaults seeded during unmarshal.
func storedTransportImplementation(data []byte) (string, bool) {
	var probe struct {
		Transport struct {
			Implementation *string `json:"implementation"`
		} `json:"transport"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Transport.Implementation == nil {
		return "", false
	}
	return *probe.Transport.Implementation, true
}

// transportImplementationExplicit reads the provenance flag without decoding
// the whole document, so an absent field is distinguishable from false.
func transportImplementationExplicit(data []byte) (explicit, present bool) {
	var probe struct {
		Transport struct {
			ImplementationExplicit *bool `json:"implementationExplicit"`
		} `json:"transport"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Transport.ImplementationExplicit == nil {
		return false, false
	}
	return *probe.Transport.ImplementationExplicit, true
}

// resolveTransport applies the release default to a document that may not
// hold an explicit choice. Recomputing on every load is what makes a default
// flip and a per-platform waiver expiry reach existing installs without a
// settings or Host migration.
func (s *PreferencesStore) resolveTransport(settings domainpreferences.Settings, explicit bool) domainpreferences.Settings {
	settings.Transport.ImplementationExplicit = explicit
	if !explicit {
		settings.Transport.Implementation = s.defaultImplementation()
	}
	return settings
}

func (s *PreferencesStore) defaultImplementation() transportmodel.Implementation {
	return s.policy.DefaultFor(transportmodel.CurrentPlatform(), s.now())
}

// hasFollowAppTerminalTheme reports whether a stored document already says
// whether the terminal follows the app.
func hasFollowAppTerminalTheme(data []byte) bool {
	var probe struct {
		Appearance struct {
			FollowAppTerminalTheme *bool `json:"followAppTerminalTheme"`
		} `json:"appearance"`
	}
	return json.Unmarshal(data, &probe) == nil && probe.Appearance.FollowAppTerminalTheme != nil
}

// Save atomically replaces the document.
func (s *PreferencesStore) Save(ctx context.Context, settings domainpreferences.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record := preferencesRecord{Version: preferencesVersion, Settings: settings}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode preferences: %w", err)
	}
	data = append(data, '\n')
	return writeSettingsFile(s.path, ".preferences-*.tmp", data)
}

// writeSettingsFile writes data through a synced temporary file and a rename,
// so an interrupted write cannot leave a truncated document.
func writeSettingsFile(path, pattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("store: create temporary settings file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: set settings file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write settings file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync settings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close settings file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("store: replace settings file: %w", err)
	}
	tmpPath = ""
	return nil
}
