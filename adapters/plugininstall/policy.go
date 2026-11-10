package plugininstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// trustDocument is the on-disk trust-on-first-use pin set. Schema is versioned
// so a future format can be migrated instead of guessed.
type trustDocument struct {
	Schema int                               `json:"schema"`
	Pins   map[string]domainplugin.SourcePin `json:"pins"`
}

const trustSchema = 1

var (
	_ interface {
		SourcePin(context.Context, string) (domainplugin.SourcePin, bool, error)
		SaveSourcePin(context.Context, domainplugin.SourcePin) error
	} = (*Store)(nil)
	_ interface {
		RegistryPreferences(context.Context) (domainplugin.RegistryPreferences, error)
		SaveRegistryPreferences(context.Context, domainplugin.RegistryPreferences) error
	} = (*Store)(nil)
)

// SourcePin returns the trust-on-first-use pin for a plugin id. Pin records
// survive uninstall and version changes; only an explicit source-change
// confirmation replaces one.
func (s *Store) SourcePin(_ context.Context, id string) (domainplugin.SourcePin, bool, error) {
	if err := checkID(id); err != nil {
		return domainplugin.SourcePin{}, false, err
	}
	document, err := s.readTrust()
	if err != nil {
		return domainplugin.SourcePin{}, false, err
	}
	pin, ok := document.Pins[id]
	return pin, ok, nil
}

// SaveSourcePin records or replaces the pin for its plugin id.
func (s *Store) SaveSourcePin(_ context.Context, pin domainplugin.SourcePin) error {
	if err := checkID(pin.ID); err != nil {
		return err
	}
	if pin.Source == "" {
		return fmt.Errorf("plugin install: trust pin for %s has an empty source", pin.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.readTrust()
	if err != nil {
		return err
	}
	if document.Pins == nil {
		document.Pins = make(map[string]domainplugin.SourcePin)
	}
	document.Pins[pin.ID] = pin
	document.Schema = trustSchema
	if err := writeJSONAtomic(s.pluginRoot, TrustFile, document); err != nil {
		return fmt.Errorf("plugin install: write trust pins: %w", err)
	}
	return nil
}

func (s *Store) readTrust() (trustDocument, error) {
	document := trustDocument{Schema: trustSchema, Pins: make(map[string]domainplugin.SourcePin)}
	data, err := os.ReadFile(filepath.Join(s.pluginRoot, TrustFile))
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return trustDocument{}, fmt.Errorf("plugin install: read trust pins: %w", err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return trustDocument{}, fmt.Errorf("plugin install: parse trust pins: %w", err)
	}
	if document.Schema != trustSchema {
		return trustDocument{}, fmt.Errorf("plugin install: unsupported trust schema %d", document.Schema)
	}
	if document.Pins == nil {
		document.Pins = make(map[string]domainplugin.SourcePin)
	}
	return document, nil
}

// RegistryPreferences returns the persisted registry policy. A missing file
// yields the safe defaults: automatic updates off and every plugin on stable.
func (s *Store) RegistryPreferences(_ context.Context) (domainplugin.RegistryPreferences, error) {
	var preferences domainplugin.RegistryPreferences
	data, err := os.ReadFile(filepath.Join(s.pluginRoot, PreferencesFile))
	if errors.Is(err, os.ErrNotExist) {
		return preferences, nil
	}
	if err != nil {
		return domainplugin.RegistryPreferences{}, fmt.Errorf(
			"plugin install: read registry preferences: %w", err)
	}
	if err := json.Unmarshal(data, &preferences); err != nil {
		return domainplugin.RegistryPreferences{}, fmt.Errorf(
			"plugin install: parse registry preferences: %w", err)
	}
	return preferences, nil
}

// SaveRegistryPreferences writes the registry policy atomically.
func (s *Store) SaveRegistryPreferences(_ context.Context,
	preferences domainplugin.RegistryPreferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeJSONAtomic(s.pluginRoot, PreferencesFile, preferences); err != nil {
		return fmt.Errorf("plugin install: write registry preferences: %w", err)
	}
	return nil
}
