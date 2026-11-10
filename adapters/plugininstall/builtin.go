package plugininstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// BuiltinStateFile persists the disabled set of compiled-in first-party
// plugins beside the install layout. Only overrides are stored, so a newly
// added built-in starts enabled without a migration.
const BuiltinStateFile = "builtins.json"

// builtinStateSchema is the on-disk document schema.
const builtinStateSchema = 1

type builtinStateDocument struct {
	Schema   int      `json:"schema"`
	Disabled []string `json:"disabled,omitempty"`
}

var _ appplugin.BuiltinStateStore = (*Store)(nil)

// BuiltinEnabled reports whether a built-in was explicitly disabled. known is
// false when no override exists, which means the host default (enabled).
func (s *Store) BuiltinEnabled(_ context.Context, id string) (bool, bool, error) {
	if err := checkID(id); err != nil {
		return false, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.readBuiltinState()
	if err != nil {
		return false, false, err
	}
	for _, disabled := range document.Disabled {
		if disabled == id {
			return false, true, nil
		}
	}
	return true, false, nil
}

// SaveBuiltinEnabled records or clears the disabled override for one id.
func (s *Store) SaveBuiltinEnabled(_ context.Context, id string, enabled bool) error {
	if err := checkID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.readBuiltinState()
	if err != nil {
		return err
	}
	disabled := make(map[string]bool, len(document.Disabled)+1)
	for _, value := range document.Disabled {
		disabled[value] = true
	}
	if enabled {
		delete(disabled, id)
	} else {
		disabled[id] = true
	}
	document.Schema = builtinStateSchema
	document.Disabled = document.Disabled[:0]
	for value := range disabled {
		document.Disabled = append(document.Disabled, value)
	}
	sort.Strings(document.Disabled)
	if err := writeJSONAtomic(s.pluginRoot, BuiltinStateFile, document); err != nil {
		return fmt.Errorf("plugin install: write built-in state: %w", err)
	}
	return nil
}

func (s *Store) readBuiltinState() (builtinStateDocument, error) {
	document := builtinStateDocument{Schema: builtinStateSchema}
	data, err := os.ReadFile(filepath.Join(s.pluginRoot, BuiltinStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return builtinStateDocument{}, fmt.Errorf("plugin install: read built-in state: %w", err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return builtinStateDocument{}, fmt.Errorf("plugin install: parse built-in state: %w", err)
	}
	return document, nil
}
