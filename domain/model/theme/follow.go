package theme

import "strings"

// TerminalThemeForPreset returns the scheme matched to a UI preset, as the
// original's "Follow application theme" chose it: a system preset's light or
// dark scheme first, then the core presets' UI-matched schemes. ok is false
// when the preset has no match and the terminal keeps its own scheme.
func TerminalThemeForPreset(dark bool, presetID string) (string, bool) {
	suffix := "-light"
	if dark {
		suffix = "-dark"
	}
	if id := "system-" + presetID + suffix; isBuiltinTerminalTheme(id) {
		return id, true
	}
	if id := "ui-" + presetID; uiMatchTerminalThemeIDs[id] {
		return id, true
	}
	return "", false
}

// PresetForTerminalTheme is the reverse match: the UI preset and appearance a
// follow-app scheme belongs to. Picking such a scheme while the terminal
// follows the app switches the app instead, as the original's side panel did.
func PresetForTerminalTheme(id string) (presetID string, dark bool, ok bool) {
	if strings.HasPrefix(id, "system-") {
		rest := strings.TrimPrefix(id, "system-")
		switch {
		case strings.HasSuffix(rest, "-light"):
			presetID, dark = strings.TrimSuffix(rest, "-light"), false
		case strings.HasSuffix(rest, "-dark"):
			presetID, dark = strings.TrimSuffix(rest, "-dark"), true
		default:
			return "", false, false
		}
	} else if uiMatchTerminalThemeIDs[id] {
		presetID = strings.TrimPrefix(id, "ui-")
		if _, found := LookupPreset(false, presetID); !found {
			dark = true
		}
	} else {
		return "", false, false
	}
	if _, found := LookupPreset(dark, presetID); !found {
		return "", false, false
	}
	if matched, found := TerminalThemeForPreset(dark, presetID); !found || matched != id {
		return "", false, false
	}
	return presetID, dark, true
}

// IsFollowAppTerminalTheme reports whether id is a scheme some UI preset
// follows.
func IsFollowAppTerminalTheme(id string) bool {
	_, _, ok := PresetForTerminalTheme(id)
	return ok
}

// FollowAppTerminalThemes returns the schemes the side panel offers while the
// terminal follows the app, in the original catalog order.
func FollowAppTerminalThemes() []TerminalTheme {
	out := make([]TerminalTheme, 0, 128)
	for _, preset := range builtinTerminalThemes {
		if IsFollowAppTerminalTheme(preset.ID) {
			out = append(out, preset)
		}
	}
	return out
}

func isBuiltinTerminalTheme(id string) bool {
	_, ok := LookupTerminalTheme(id)
	return ok
}
