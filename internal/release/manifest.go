package release

import (
	"encoding/json"
	"fmt"
)

// Manifest is the part of mygo.json the release pipeline reads. mygo.json is
// the only version source.
type Manifest struct {
	Name       string
	Identifier string
	Version    string

	raw map[string]json.RawMessage
}

// ParseManifest reads mygo.json and validates the fields the release needs:
// a strict version and a non-empty identifier.
func ParseManifest(data []byte) (Manifest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{raw: raw}
	for _, field := range []struct {
		key    string
		target *string
	}{
		{"name", &manifest.Name},
		{"identifier", &manifest.Identifier},
		{"version", &manifest.Version},
	} {
		value, ok := raw[field.key]
		if !ok {
			return Manifest{}, fmt.Errorf("mygo.json has no %s", field.key)
		}
		if err := json.Unmarshal(value, field.target); err != nil {
			return Manifest{}, fmt.Errorf("mygo.json: %s: %w", field.key, err)
		}
	}
	if manifest.Identifier == "" {
		return Manifest{}, fmt.Errorf("mygo.json: identifier is empty")
	}
	if _, err := ParseVersion(manifest.Version); err != nil {
		return Manifest{}, fmt.Errorf("mygo.json: %w", err)
	}
	return manifest, nil
}

// CheckPackaging rejects the configuration that would produce signing,
// notarization, or update artifacts. MyGo's default macOS signature is ad
// hoc and needs no identity.
func (m Manifest) CheckPackaging() error {
	if _, ok := m.raw["updates"]; ok {
		return fmt.Errorf("mygo.json has an updates block; releases must not produce update metadata")
	}
	if raw, ok := m.raw["macos"]; ok {
		var macos map[string]json.RawMessage
		if err := json.Unmarshal(raw, &macos); err != nil {
			return fmt.Errorf("mygo.json: macos: %w", err)
		}
		for _, key := range []string{"signingIdentity", "notarize"} {
			if _, ok := macos[key]; ok {
				return fmt.Errorf("mygo.json: macos.%s is not allowed; the release is ad hoc signed only", key)
			}
		}
	}
	if raw, ok := m.raw["windows"]; ok {
		var windows map[string]json.RawMessage
		if err := json.Unmarshal(raw, &windows); err != nil {
			return fmt.Errorf("mygo.json: windows: %w", err)
		}
		for _, key := range []string{"certificate", "signCommand"} {
			if _, ok := windows[key]; ok {
				return fmt.Errorf("mygo.json: windows.%s is a signing artifact; the release is unsigned", key)
			}
		}
	}
	return nil
}
