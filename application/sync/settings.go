package sync

// The original payload's "settings" field is a flat JSON object. It is modeled
// as an object-shaped v2 family so portable settings get causal merge without
// changing the wire envelope. An object-valued document keeps its top-level
// fields as registers; an array or scalar document is wrapped under a marker
// field, because a CRDT entity must be an object.

import (
	"bytes"
	"encoding/json"
	"fmt"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// settingsArrayField wraps a settings document that is not a JSON object.
// The name is deliberately outside the keys this build writes.
const settingsArrayField = "$natcattyArray"

func settingsFamily() domainsync.ConvergentFamily {
	return domainsync.ConvergentFamily{
		Name:        "settings",
		Object:      true,
		Normalize:   normalizeSettingsValue,
		Materialize: materializeSettingsValue,
	}
}

func normalizeSettingsValue(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("settings value is empty")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		// A null document would decode into a zero settings value and reset the
		// device to defaults, so fail the snapshot instead of accepting it.
		return nil, fmt.Errorf("settings value is null")
	}
	if trimmed[0] == '{' {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("settings value must be an object or scalar: %w", err)
		}
		if len(fields) == 1 {
			if _, ok := fields[settingsArrayField]; ok {
				return trimmed, nil
			}
		}
		return json.Marshal(fields)
	}
	fields := map[string]json.RawMessage{settingsArrayField: append(json.RawMessage(nil), trimmed...)}
	return json.Marshal(fields)
}

// portableSettingsSnapshot builds a snapshot containing only the settings
// family. A full payload would carry empty arrays for every Vault family, and
// applying those would tombstone the Vault the migration just adopted.
func portableSettingsSnapshot(settings map[string]json.RawMessage) (domainsync.ConvergentSnapshot, error) {
	parsed, err := payloadSnapshot(Payload{Settings: settings})
	if err != nil {
		return domainsync.ConvergentSnapshot{}, err
	}
	return domainsync.ConvergentSnapshot{
		Families: map[string][]domainsync.ConvergentRecord{
			"settings": parsed.Families["settings"],
		},
	}, nil
}

func materializeSettingsValue(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if len(fields) == 1 {
		if value, ok := fields[settingsArrayField]; ok {
			return value, nil
		}
	}
	return raw, nil
}
